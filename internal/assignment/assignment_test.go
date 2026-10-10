// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package assignment

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
)

type env struct {
	st      *memstore.Store
	bus     eventbus.Bus
	svc     *Service
	snap    *domain.GitOpsSnapshot
	samples []blueprint.Sample
	ctl     *recControl
}

type recControl struct {
	control.AgentControl
	mu      sync.Mutex
	desired [][]domain.AgentID
}

func (r *recControl) DesiredStateChanged(_ context.Context, ids []domain.AgentID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.desired = append(r.desired, append([]domain.AgentID(nil), ids...))
	return nil
}

func (r *recControl) all() []domain.AgentID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.AgentID
	for _, d := range r.desired {
		out = append(out, d...)
	}
	return out
}

func newEnv(t *testing.T) *env {
	t.Helper()
	fsys := os.DirFS("../testdata/gitops")
	snap, issues := gitops.Parse(fsys, ".", "commit-1")
	require.Empty(t, issues)
	samples, sissues := gitops.ParseSamples(fsys, ".")
	require.Empty(t, sissues)

	e := &env{st: memstore.New(), bus: eventbus.NewMemory("t", 0), snap: snap, samples: samples, ctl: &recControl{}}
	t.Cleanup(func() { e.bus.Close() })
	_, err := e.st.GitOps().ApplySnapshot(context.Background(), *snap, domain.GitOpsSync{Commit: &domain.GitCommit{SHA: snap.Commit}})
	require.NoError(t, err)
	e.svc = New(Deps{
		Store: e.st, Bus: e.bus, Control: e.ctl, Renderer: blueprint.NewRenderer(blueprint.Options{}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	})
	return e
}

func (e *env) sample(name string) blueprint.Sample {
	for _, s := range e.samples {
		if s.Name == name {
			return s
		}
	}
	panic("no sample " + name)
}

// addAgent registers a collector with the attributes of a sample.
func (e *env) addAgent(sampleName string, mut ...func(a *domain.Agent)) domain.AgentID {
	sm := e.sample(sampleName)
	id := uuid.Must(uuid.NewV7())
	a := domain.Agent{
		ID: id, Kind: domain.AgentKindCollector, ServiceName: "otelcol-contrib", Version: sm.Attributes["service.version"],
		Hostname: sm.Attributes["host.name"], OSType: sm.Attributes["os.type"], Arch: sm.Attributes["host.arch"],
		IdentifyingAttributes:    map[string]string{},
		NonIdentifyingAttributes: map[string]string{},
		Labels:                   map[string]string{},
		Connection:               domain.AgentConnection{Status: domain.ConnectionOnline},
		FirstSeenAt:              time.Now(),
	}
	for k, v := range sm.Attributes {
		if k == "service.name" || k == "service.version" {
			a.IdentifyingAttributes[k] = v
		} else {
			a.NonIdentifyingAttributes[k] = v
		}
	}
	for _, m := range mut {
		m(&a)
	}
	e.st.PutAgent(a)
	return id
}

func goldenHash(t *testing.T, sample string) domain.Hash {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "blueprint", "testdata", "golden", sample+".result.json"))
	require.NoError(t, err)
	var r struct {
		Hash string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(raw, &r))
	h, err := domain.ParseHash(r.Hash)
	require.NoError(t, err)
	return h
}

// New agents get the golden config of their sample as assigned AND desired
// config (bootstrap), and the rendered body is stored content-addressed.
func TestAssignAgentBootstrapsAllSamplesToGoldenConfigs(t *testing.T) {
	e := newEnv(t)
	wantFleet := map[string]string{
		"edge-nl-1001": "edge-nl", "edge-nl-1003": "edge-nl", "edge-be-2001": "edge-be",
		"office-app-01": "office", "dc-north-01": "datacenter", "k8s-gw-0": "k8s-gateway",
	}
	for name, fleet := range wantFleet {
		id := e.addAgent(name)
		as, err := e.svc.AssignAgent(context.Background(), id)
		require.NoError(t, err, name)
		assert.Equal(t, fleet, as.FleetName, name)
		assert.Equal(t, goldenHash(t, name), as.AssignedConfigHash, name)
		assert.True(t, as.DesiredChanged, name)
		assert.Empty(t, as.Error)
		assert.Equal(t, id, as.AgentID)

		ag := e.st.Agent(id)
		assert.Equal(t, fleet, ag.Assignment.FleetName)
		assert.Equal(t, goldenHash(t, name), ag.Assignment.AssignedConfigHash)
		assert.Equal(t, goldenHash(t, name), ag.Assignment.DesiredConfigHash, "bootstrap: desired = assigned")
		assert.NotNil(t, ag.Assignment.AssignedAt)
		assert.NotEmpty(t, ag.Assignment.TargetVersion)

		rc, err := e.st.Configs().GetRendered(context.Background(), as.AssignedConfigHash)
		require.NoError(t, err)
		golden, _ := os.ReadFile(filepath.Join("..", "blueprint", "testdata", "golden", name+".yaml"))
		assert.Equal(t, string(golden), string(rc.Body))
		assert.Equal(t, fleet, rc.FleetName)
		assert.Equal(t, "commit-1", rc.GitCommit)
		assert.Equal(t, domain.RemoteConfigContentType, rc.ContentType)

		evs := e.st.Events(id)
		require.Len(t, evs, 1)
		assert.Equal(t, domain.AgentEventAssigned, evs[0].Type)
	}
}

func TestAssignAgentTargetVersionAndNoBusPublishInSyncPath(t *testing.T) {
	e := newEnv(t)
	events, cancel := e.bus.Subscribe()
	defer cancel()
	id := e.addAgent("edge-be-2001")
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "0.161.0", as.TargetVersion)
	assert.Equal(t, "0.161.0", e.st.Agent(id).Assignment.TargetVersion)
	assert.Empty(t, events, "no bus publish in the synchronous path (the OpAMP layer pushes in the same response)")
	assert.Empty(t, e.ctl.all(), "no DesiredStateChanged in the synchronous path")
}

func TestOnlyCollectorsAreAssigned(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.Kind = domain.AgentKindUpdater })
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, domain.Assignment{AgentID: id}, as)
	assert.Empty(t, e.st.Agent(id).Assignment.FleetName)
	assert.Empty(t, e.st.Events(id))
}

func TestLabelsAreTokenLabelsPlusInventoryLabels(t *testing.T) {
	e := newEnv(t)
	tokenID := uuid.Must(uuid.NewV7())
	e.st.PutEnrollmentToken(domain.EnrollmentToken{ID: tokenID, Name: "edge-nl", Labels: map[string]string{"channel": "edge-nl", "pilot": "false"}})
	id := e.addAgent("edge-nl-1001", func(a *domain.Agent) {
		a.EnrollmentTokenID = &tokenID
		a.Labels = map[string]string{"channel": "edge-nl"} // what the OpAMP layer wrote at registration (token labels only)
	})
	_, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	// inventory (pilot=true for site 1001) wins over the token's pilot=false
	assert.Equal(t, map[string]string{"channel": "edge-nl", "pilot": "true"}, e.st.Agent(id).Labels)

	// stale labels disappear: an agent without token and without inventory entry
	id2 := e.addAgent("edge-nl-1003", func(a *domain.Agent) { a.Labels = map[string]string{"pilot": "true", "old": "x"} })
	_, err = e.svc.AssignAgent(context.Background(), id2)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{}, e.st.Agent(id2).Labels)

	// token labels are matchable by fleet selectors (labels.<key>)
	m, err := e.svc.Match(context.Background(), domain.AttributeView{"site.type": "edge", "site.country": "NL", "os.type": "windows", "site.id": "1001", "labels.x": "y"})
	require.NoError(t, err)
	assert.Equal(t, "edge-nl", m.Fleet)
	assert.Equal(t, map[string]string{"pilot": "true"}, m.InventoryLabels)
}

func TestExistingAgentInSameFleetGetsNoBootstrapWhenHashChanges(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001")
	_, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	oldHash := e.st.Agent(id).Assignment.AssignedConfigHash

	// New snapshot: different gateway endpoint for the fleet -> different config.
	snap2, _ := gitops.Parse(os.DirFS("../testdata/gitops"), ".", "commit-2")
	for i := range snap2.Fleets {
		if snap2.Fleets[i].Name == "edge-nl" {
			snap2.Fleets[i].Parameters["gatewayEndpoint"] = "other-gw.example.internal:4317"
		}
	}
	_, err = e.st.GitOps().ApplySnapshot(context.Background(), *snap2, domain.GitOpsSync{})
	require.NoError(t, err)

	events, cancel := e.bus.Subscribe(eventbus.AssignmentChanged)
	defer cancel()
	require.NoError(t, e.svc.ReassignAll(context.Background()))

	ag := e.st.Agent(id)
	assert.NotEqual(t, oldHash, ag.Assignment.AssignedConfigHash, "assigned follows git")
	assert.Equal(t, oldHash, ag.Assignment.DesiredConfigHash, "desired only changes through rollouts")
	assert.Equal(t, domain.ConfigSyncDrift, ag.ConfigSyncState())
	assert.Empty(t, e.ctl.all(), "nothing to push without bootstrap")
	require.Len(t, events, 1)
	var pl eventbus.AgentIDsPayload
	require.NoError(t, (<-events).Decode(&pl))
	assert.Equal(t, []domain.AgentID{id}, pl.AgentIDs)
	rc, err := e.st.Configs().GetRendered(context.Background(), ag.Assignment.AssignedConfigHash)
	require.NoError(t, err)
	assert.Equal(t, "commit-2", rc.GitCommit)

	// Reassigning again changes nothing (no assignment write, no event).
	require.NoError(t, e.svc.ReassignAll(context.Background()))
	assert.Empty(t, events)
}

func TestFleetChangeResetsDesiredToAssigned(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001")
	_, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	golden := goldenHash(t, "edge-nl-1001")

	// The agent moves to Belgium (attribute change): fleet edge-be.
	e.st.UpdateAgent(id, func(a *domain.Agent) {
		a.NonIdentifyingAttributes["site.country"] = "BE"
		a.Assignment.FailedConfigHash = golden // failed hash must be cleared when desired changes
	})
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "edge-be", as.FleetName)
	assert.True(t, as.DesiredChanged)
	ag := e.st.Agent(id)
	assert.Equal(t, as.AssignedConfigHash, ag.Assignment.DesiredConfigHash)
	assert.NotEqual(t, golden, ag.Assignment.DesiredConfigHash)
	assert.Empty(t, ag.Assignment.FailedConfigHash)
	evs := e.st.Events(id)
	require.Len(t, evs, 2)
	assert.Contains(t, evs[1].Message, "was edge-nl")
}

func TestUnassignedAndAmbiguous(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.NonIdentifyingAttributes["site.type"] = "warehouse" })
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Empty(t, as.FleetName)
	assert.Empty(t, as.Error, "no candidate is not an error")
	assert.False(t, as.DesiredChanged)
	assert.Empty(t, e.st.Agent(id).Assignment.DesiredConfigHash)

	// ambiguity: a second fleet with the same priority
	snap, _ := gitops.Parse(os.DirFS("../testdata/gitops"), ".", "c3")
	dup := snap.Fleets[0]
	for _, f := range snap.Fleets {
		if f.Name == "edge-nl" {
			dup = f
		}
	}
	dup.Name = "edge-nl-twin"
	snap.Fleets = append(snap.Fleets, dup)
	_, err = e.st.GitOps().ApplySnapshot(context.Background(), *snap, domain.GitOpsSync{})
	require.NoError(t, err)
	require.NoError(t, e.svc.ReassignAll(context.Background()))
	id2 := e.addAgent("edge-nl-1001")
	as, err = e.svc.AssignAgent(context.Background(), id2)
	require.NoError(t, err)
	assert.Empty(t, as.FleetName)
	assert.Equal(t, "ambiguous: fleets edge-nl, edge-nl-twin have equal priority 100", as.Error)
	assert.Equal(t, as.Error, e.st.Agent(id2).Assignment.Error)
}

func TestRenderErrorKeepsPreviousConfig(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001")
	_, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	golden := goldenHash(t, "edge-nl-1001")

	// a snapshot where the blueprint needs a parameter nobody sets
	snap, _ := gitops.Parse(os.DirFS("../testdata/gitops"), ".", "bad")
	delete(snap.Fleets[1].Parameters, "gatewayEndpoint") // fleets sorted by name: datacenter, edge-be, edge-nl, k8s-gateway, office
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == "edge-nl" {
			delete(snap.Fleets[i].Parameters, "gatewayEndpoint")
		}
	}
	_, err = e.st.GitOps().ApplySnapshot(context.Background(), *snap, domain.GitOpsSync{})
	require.NoError(t, err)
	require.NoError(t, e.svc.ReassignAll(context.Background()))

	ag := e.st.Agent(id)
	assert.Equal(t, "edge-nl", ag.Assignment.FleetName, "membership stays")
	assert.Equal(t, golden, ag.Assignment.AssignedConfigHash, "previous assigned hash is kept")
	assert.Equal(t, golden, ag.Assignment.DesiredConfigHash)
	assert.Contains(t, ag.Assignment.Error, "missing_parameter")
	assert.NotEqual(t, domain.ConfigSyncNone, ag.ConfigSyncState())

	// A new agent in that situation has a fleet but no config.
	id2 := e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.NonIdentifyingAttributes["site.id"] = "5555" })
	as, err := e.svc.AssignAgent(context.Background(), id2)
	require.NoError(t, err, "render errors are stored, not returned")
	assert.Equal(t, "edge-nl", as.FleetName)
	assert.Contains(t, as.Error, "missing_parameter")
	assert.False(t, as.DesiredChanged)
	assert.Empty(t, e.st.Agent(id2).Assignment.DesiredConfigHash)
	assert.Equal(t, domain.ConfigSyncNone, e.st.Agent(id2).ConfigSyncState())
}

func TestComponentSetOfTheAgentIsChecked(t *testing.T) {
	e := newEnv(t)
	hash := domain.SHA256([]byte("set"))
	require.NoError(t, e.st.Configs().PutComponentSet(context.Background(), domain.ComponentSet{Hash: hash, Components: map[string]map[string]domain.ComponentDetails{
		"receivers":  {"otlp": {}, "host_metrics": {}, "file_log": {}}, // no windows_event_log
		"processors": {"batch": {}, "memory_limiter": {}, "resource": {}, "resource_detection": {}},
		"exporters":  {"otlp_grpc": {}},
		"extensions": {"health_check": {}, "file_storage": {}},
	}}))
	id := e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.AvailableComponentsHash = hash })
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Contains(t, as.Error, "unknown_component")
	assert.Contains(t, as.Error, "windows_event_log")
	assert.Empty(t, e.st.Agent(id).Assignment.DesiredConfigHash)

	// An agent that reports all components of the contrib build is fine.
	raw, err := os.ReadFile("../../examples/sim/components-otelcol-contrib-0.162.0.json")
	require.NoError(t, err)
	var f struct {
		Components map[string]map[string]domain.ComponentDetails `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	full := domain.SHA256([]byte("full"))
	require.NoError(t, e.st.Configs().PutComponentSet(context.Background(), domain.ComponentSet{Hash: full, Components: f.Components}))
	id2 := e.addAgent("edge-nl-1003", func(a *domain.Agent) { a.AvailableComponentsHash = full })
	as, err = e.svc.AssignAgent(context.Background(), id2)
	require.NoError(t, err)
	assert.Empty(t, as.Error, "%s", as.Error)
	assert.Equal(t, goldenHash(t, "edge-nl-1003"), as.AssignedConfigHash)
}

func TestReassignAllBootstrapsAndPushes(t *testing.T) {
	e := newEnv(t)
	var ids []domain.AgentID
	for i := 0; i < 250; i++ {
		ids = append(ids, e.addAgent("edge-nl-1001", func(a *domain.Agent) {
			a.Hostname = "host-" + uuid.NewString()
			a.NonIdentifyingAttributes["site.id"] = "9" + uuid.NewString()[:3]
		}))
	}
	// One updater and one agent of another fleet.
	e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.Kind = domain.AgentKindUpdater })
	e.addAgent("office-app-01")

	events, cancel := e.bus.Subscribe(eventbus.AssignmentChanged)
	defer cancel()
	require.NoError(t, e.svc.ReassignAll(context.Background()))
	assert.Len(t, e.ctl.all(), 251, "all bootstrapped collectors are pushed")
	got := 0
	for len(events) > 0 {
		var pl eventbus.AgentIDsPayload
		require.NoError(t, (<-events).Decode(&pl))
		assert.LessOrEqual(t, len(pl.AgentIDs), 100, "chunks of at most 100")
		got += len(pl.AgentIDs)
	}
	assert.Equal(t, 251, got)
	for _, id := range ids[:5] {
		assert.NotEmpty(t, e.st.Agent(id).Assignment.DesiredConfigHash)
	}
	assert.GreaterOrEqual(t, len(e.st.Rendered()), 2)
}

func TestConcurrentAssignAndReassign(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := e.svc.AssignAgent(context.Background(), id)
			assert.NoError(t, err)
		}()
		go func() {
			defer wg.Done()
			assert.NoError(t, e.svc.ReassignAll(context.Background()))
		}()
	}
	wg.Wait()
	assert.Equal(t, goldenHash(t, "edge-nl-1001"), e.st.Agent(id).Assignment.DesiredConfigHash)
	assert.Len(t, e.st.Events(id), 1, "the fleet change is recorded once")
}

// A write in the same clock tick as the listing may have made the listed copy
// stale, so it counts as stale. A coarse clock (Windows) gives both the same
// time; counting that as "not later" let ReassignAll work on a stale copy and
// record a fleet change twice.
func TestStaleSinceCountsTheSameTickAsLater(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001")
	tick := time.Now()
	e.svc.writtenMu.Lock()
	e.svc.written[id] = tick
	e.svc.writtenMu.Unlock()
	assert.True(t, e.svc.staleSince(id, tick), "a write in the tick of the listing")
	assert.True(t, e.svc.staleSince(id, tick.Add(-time.Millisecond)), "a write after the listing")
	assert.False(t, e.svc.staleSince(id, tick.Add(time.Millisecond)), "a write before the listing")
	assert.False(t, e.svc.staleSince(id, time.Time{}), "a fresh read is never stale")
}

func TestRunReloadsSnapshotOnEvents(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.svc.Run(ctx) }()
	require.Eventually(t, func() bool { return e.svc.snap.Load() != nil }, 2*time.Second, 5*time.Millisecond, "loaded at start")

	// Change the stored snapshot, notify like the syncer does.
	snap2, _ := gitops.Parse(os.DirFS("../testdata/gitops"), ".", "commit-2")
	_, err := e.st.GitOps().ApplySnapshot(ctx, *snap2, domain.GitOpsSync{})
	require.NoError(t, err)
	require.NoError(t, e.bus.Publish(ctx, eventbus.GitOpsSynced, eventbus.GitOpsSyncedPayload{Commit: "commit-2", Result: "applied"}))
	require.Eventually(t, func() bool { return e.svc.snap.Load().snap.Commit == "commit-2" }, 2*time.Second, 5*time.Millisecond)

	// BusReconnected is delivered locally by the bus; simulate by publishing the type.
	snap3, _ := gitops.Parse(os.DirFS("../testdata/gitops"), ".", "commit-3")
	_, err = e.st.GitOps().ApplySnapshot(ctx, *snap3, domain.GitOpsSync{})
	require.NoError(t, err)
	require.NoError(t, e.bus.Publish(ctx, eventbus.BusReconnected, struct{}{}))
	require.Eventually(t, func() bool { return e.svc.snap.Load().snap.Commit == "commit-3" }, 2*time.Second, 5*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestAssignAgentLoadsSnapshotLazily(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("dc-north-01")
	as, err := e.svc.AssignAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "datacenter", as.FleetName)

	e2 := newEnv(t)
	e2.st.FailGetSnapshot = context.DeadlineExceeded
	id2 := e2.addAgent("dc-north-01")
	_, err = e2.svc.AssignAgent(context.Background(), id2)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "infrastructure errors are returned")

	_, err = e.svc.AssignAgent(context.Background(), uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestMatchWithoutPersistence(t *testing.T) {
	e := newEnv(t)
	m, err := e.svc.Match(context.Background(), domain.AttributeView{"site.type": "k8s-gateway", "os.type": "linux"})
	require.NoError(t, err)
	assert.Equal(t, "k8s-gateway", m.Fleet)
	assert.Equal(t, []string{"k8s-gateway"}, m.Candidates)
	m, err = e.svc.Match(context.Background(), domain.AttributeView{"site.type": "unknown"})
	require.NoError(t, err)
	assert.Empty(t, m.Fleet)
	assert.NotNil(t, m.Candidates)
}

func TestAssignAgentIsFast(t *testing.T) {
	e := newEnv(t)
	ids := make([]domain.AgentID, 200)
	for i := range ids {
		ids[i] = e.addAgent("edge-nl-1001", func(a *domain.Agent) { a.NonIdentifyingAttributes["site.id"] = uuid.NewString()[:4] })
	}
	// warm up
	_, _ = e.svc.AssignAgent(context.Background(), ids[0])
	start := time.Now()
	for _, id := range ids[1:] {
		_, err := e.svc.AssignAgent(context.Background(), id)
		require.NoError(t, err)
	}
	per := time.Since(start) / time.Duration(len(ids)-1)
	t.Logf("AssignAgent: %s per agent", per)
	budget := 20 * time.Millisecond
	if raceEnabled {
		budget *= 5
	}
	assert.Less(t, per, budget)
}

func putSelection(t *testing.T, e *env, fleet, version string, state domain.VersionSelectionState) {
	t.Helper()
	_, err := e.st.FleetVersions().Put(context.Background(), domain.FleetVersionSelection{
		Fleet: fleet, Version: version, State: state, SelectedBy: "user:x", SelectedAt: time.Now(),
	}, nil)
	require.NoError(t, err)
}

// The target version of an agent is the effective target of its fleet: a UI
// selection wins over the Git target, a selection on hold leaves no target.
func TestTargetVersionIsTheEffectiveTarget(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.addAgent("edge-be-2001")
	as, err := e.svc.AssignAgent(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "0.161.0", as.TargetVersion, "the Git target")

	putSelection(t, e, "edge-be", "0.162.0", domain.SelectionActive)
	_, err = e.svc.Reload(ctx)
	require.NoError(t, err)
	as, err = e.svc.AssignAgent(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", as.TargetVersion, "the UI selection")
	assert.Equal(t, "0.162.0", e.st.Agent(id).Assignment.TargetVersion)
	sels, err := e.svc.VersionSelections(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", sels["edge-be"].Version)

	putSelectionAs := func(state domain.VersionSelectionState) {
		_, err := e.st.FleetVersions().Delete(ctx, "edge-be", nil)
		require.NoError(t, err)
		putSelection(t, e, "edge-be", "0.162.0", state)
	}
	putSelectionAs(domain.SelectionInvalidated)
	require.NoError(t, e.svc.ReassignAll(ctx))
	assert.Equal(t, "", e.st.Agent(id).Assignment.TargetVersion, "on hold: no target")

	_, err = e.st.FleetVersions().Delete(ctx, "edge-be", nil)
	require.NoError(t, err)
	require.NoError(t, e.svc.ReassignAll(ctx))
	assert.Equal(t, "0.161.0", e.st.Agent(id).Assignment.TargetVersion, "cleared: back to Git")
}

// Every replica reloads the selections when a fleet.version_changed event arrives.
func TestRunReloadsSelectionsOnFleetVersionChanged(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.svc.Run(ctx) }()
	require.Eventually(t, func() bool { return e.svc.snap.Load() != nil }, 2*time.Second, 5*time.Millisecond)

	putSelection(t, e, "edge-be", "0.162.0", domain.SelectionActive)
	require.NoError(t, e.bus.Publish(ctx, eventbus.FleetVersionChanged, eventbus.FleetVersionChangedPayload{FleetName: "edge-be"}))
	require.Eventually(t, func() bool {
		sels, err := e.svc.VersionSelections(ctx)
		return err == nil && sels["edge-be"] != nil
	}, 2*time.Second, 5*time.Millisecond)

	id := e.addAgent("edge-be-2001")
	as, err := e.svc.AssignAgent(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "0.162.0", as.TargetVersion)
	cancel()
	require.NoError(t, <-done)
}
