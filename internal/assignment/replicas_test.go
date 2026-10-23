// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package assignment

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

// countingStore counts the desired-state resets that the replicas make.
type countingStore struct {
	store.Store
	agents *countingAgents
}

type countingAgents struct {
	store.AgentStore
	mu     sync.Mutex
	resets map[domain.AgentID]int
}

func (c *countingStore) Agents() store.AgentStore { return c.agents }

func (c *countingAgents) SetDesiredConfig(ctx context.Context, ids []domain.AgentID, h domain.Hash) error {
	c.mu.Lock()
	for _, id := range ids {
		c.resets[id]++
	}
	c.mu.Unlock()
	return c.AgentStore.SetDesiredConfig(ctx, ids, h)
}

func (c *countingAgents) resetsOf(id domain.AgentID) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resets[id]
}

// replicas are two services on one database, as two server replicas.
type replicas struct {
	db      *postgres.DB
	counter *countingAgents
	a, b    *Service
	sample  blueprint.Sample
}

func newReplicas(t *testing.T) *replicas {
	t.Helper()
	fsys := os.DirFS("../testdata/gitops")
	snap, issues := gitops.Parse(fsys, ".", "commit-1")
	require.Empty(t, issues)
	samples, sissues := gitops.ParseSamples(fsys, ".")
	require.Empty(t, sissues)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := postgres.New(pgtest.NewPool(t), quiet)
	_, err := db.GitOps().ApplySnapshot(context.Background(), *snap, domain.GitOpsSync{Commit: &domain.GitCommit{SHA: snap.Commit}})
	require.NoError(t, err)

	r := &replicas{db: db, counter: &countingAgents{AgentStore: db.Agents(), resets: map[domain.AgentID]int{}}}
	for _, s := range samples {
		if s.Name == "edge-nl-1001" {
			r.sample = s
		}
	}
	mk := func() *Service {
		bus := eventbus.NewMemory("t", 0)
		t.Cleanup(func() { bus.Close() })
		return New(Deps{
			Store: &countingStore{Store: db, agents: r.counter}, Bus: bus, Renderer: blueprint.NewRenderer(blueprint.Options{}),
			Logger: quiet, Registerer: prometheus.NewRegistry(),
		})
	}
	r.a, r.b = mk(), mk()
	return r
}

func (r *replicas) addAgents(t *testing.T, n int) []domain.AgentID {
	t.Helper()
	ids := make([]domain.AgentID, n)
	for i := range ids {
		ids[i] = uuid.Must(uuid.NewV7())
		ident, non := map[string]string{}, map[string]string{}
		for k, v := range r.sample.Attributes {
			if k == "service.name" || k == "service.version" {
				ident[k] = v
			} else {
				non[k] = v
			}
		}
		_, _, err := r.db.Agents().UpsertDescription(context.Background(), ids[i], store.AgentDescriptionUpdate{
			Kind: domain.AgentKindCollector, ServiceName: "otelcol-contrib", Version: r.sample.Attributes["service.version"],
			Hostname: r.sample.Attributes["host.name"], OSType: r.sample.Attributes["os.type"], Arch: r.sample.Attributes["host.arch"],
			IdentifyingAttributes: ident, NonIdentifyingAttributes: non,
		})
		require.NoError(t, err)
	}
	return ids
}

func (r *replicas) assignedEvents(t *testing.T, id domain.AgentID) int {
	t.Helper()
	evs, err := r.db.Agents().ListEvents(context.Background(), id, 100, 0)
	require.NoError(t, err)
	n := 0
	for _, e := range evs {
		if e.Type == domain.AgentEventAssigned {
			n++
		}
	}
	return n
}

// Two replicas assign the same agents at the same time, one through
// AssignAgent and the other through ReassignAll, and the other way round: each
// fleet change is recorded once and resets the desired state once.
func TestTwoReplicasRecordAFleetChangeOnce(t *testing.T) {
	const agents = 24
	ctx := context.Background()
	r := newReplicas(t)
	ids := r.addAgents(t, agents)

	var wg sync.WaitGroup
	start := make(chan struct{})
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; f() }()
	}
	for _, id := range ids {
		run(func() { _, err := r.a.AssignAgent(ctx, id); assert.NoError(t, err) })
		run(func() { _, err := r.b.AssignAgent(ctx, id); assert.NoError(t, err) })
	}
	run(func() { assert.NoError(t, r.a.ReassignAll(ctx)) })
	run(func() { assert.NoError(t, r.b.ReassignAll(ctx)) })
	close(start)
	wg.Wait()

	want := goldenHash(t, "edge-nl-1001")
	for _, id := range ids {
		ag, err := r.db.Agents().Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "edge-nl", ag.Assignment.FleetName)
		assert.Equal(t, want, ag.Assignment.DesiredConfigHash)
		assert.Equal(t, 1, r.assignedEvents(t, id), "the fleet change is recorded once")
		assert.Equal(t, 1, r.counter.resetsOf(id), "the desired state is reset once")
	}
}

// A replica that read the agent before another one assigned it records nothing
// and resets nothing; it finds the assignment settled.
func TestStaleReplicaFindsTheAssignmentSettled(t *testing.T) {
	ctx := context.Background()
	r := newReplicas(t)
	id := r.addAgents(t, 1)[0]
	stale, err := r.db.Agents().Get(ctx, id)
	require.NoError(t, err)

	_, err = r.a.AssignAgent(ctx, id)
	require.NoError(t, err)

	st, err := r.b.current(ctx)
	require.NoError(t, err)
	out, err := r.b.assignOne(ctx, st, stale, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, "edge-nl", out.assignment.FleetName)
	assert.False(t, out.assignmentWrote)
	assert.False(t, out.assignment.DesiredChanged)
	assert.Equal(t, 1, r.assignedEvents(t, id))
	assert.Equal(t, 1, r.counter.resetsOf(id))
}

// The compare-and-set of the store writes only against the assignment it was
// given.
func TestCompareAndSetAssignment(t *testing.T) {
	ctx := context.Background()
	r := newReplicas(t)
	id := r.addAgents(t, 1)[0]
	ag := r.db.Agents()
	h := domain.SHA256([]byte("a"))

	ok, err := ag.CompareAndSetAssignment(ctx, id, domain.AgentAssignment{}, store.AgentAssignmentUpdate{FleetName: "f1", AssignedConfigHash: h, TargetVersion: "1"})
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = ag.CompareAndSetAssignment(ctx, id, domain.AgentAssignment{}, store.AgentAssignmentUpdate{FleetName: "f2"})
	require.NoError(t, err)
	assert.False(t, ok, "the previous assignment no longer matches")
	got, err := ag.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "f1", got.Assignment.FleetName)

	ok, err = ag.CompareAndSetAssignment(ctx, id, got.Assignment, store.AgentAssignmentUpdate{FleetName: "f2", Error: "x"})
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = ag.CompareAndSetAssignment(ctx, id, domain.AgentAssignment{FleetName: "f2", Error: "x"}, store.AgentAssignmentUpdate{})
	require.NoError(t, err)
	assert.True(t, ok, "an empty hash and version match NULL and empty")

	_, err = ag.CompareAndSetAssignment(ctx, uuid.Must(uuid.NewV7()), domain.AgentAssignment{}, store.AgentAssignmentUpdate{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

// AssignAgent reads the snapshot after it has the agent's lock: a call that
// waited for the lock works on the snapshot that is current when it gets it.
func TestAssignAgentReadsTheSnapshotAfterTheLock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := addBridge(e, "standalone")
	_, err := e.svc.Reload(ctx) // the snapshot without the opt-in
	require.NoError(t, err)

	lk := e.svc.lockFor(id)
	lk.Lock()
	var res domain.Assignment
	var aerr error
	done := make(chan struct{})
	go func() { defer close(done); res, aerr = e.svc.AssignAgent(ctx, id) }()
	time.Sleep(100 * time.Millisecond) // let the call reach the lock

	setManage(t, e, true)
	_, err = e.svc.Reload(ctx)
	require.NoError(t, err)
	lk.Unlock()
	<-done

	require.NoError(t, aerr)
	assert.Empty(t, res.Error, "assigned with the snapshot that has the opt-in")
	assert.False(t, res.AssignedConfigHash.IsZero())
}

// A listing older than the age of the write record is stale: pruned entries
// can no longer vouch for it.
func TestStaleSinceTreatsAnOldListingAsStale(t *testing.T) {
	e := newEnv(t)
	id := e.addAgent("edge-nl-1001")
	assert.True(t, e.svc.staleSince(id, time.Now().Add(-writtenMaxAge-time.Second)))
	assert.False(t, e.svc.staleSince(id, time.Now().Add(-writtenMaxAge/2)), "no write recorded")
}

// Pruning the write record is throttled: not a scan of the whole map on every
// write.
func TestMarkWrittenPrunesAtMostOncePerGap(t *testing.T) {
	e := newEnv(t)
	old := time.Now().Add(-2 * writtenMaxAge)
	fill := func() {
		e.svc.writtenMu.Lock()
		for i := 0; i <= writtenPruneSize; i++ {
			e.svc.written[uuid.Must(uuid.NewV7())] = old
		}
		e.svc.writtenMu.Unlock()
	}
	size := func() int {
		e.svc.writtenMu.Lock()
		defer e.svc.writtenMu.Unlock()
		return len(e.svc.written)
	}
	fill()
	e.svc.markWritten(uuid.Must(uuid.NewV7()))
	assert.Equal(t, 1, size(), "the old entries are pruned")
	fill()
	e.svc.markWritten(uuid.Must(uuid.NewV7()))
	assert.Greater(t, size(), writtenPruneSize, "the next write within the gap does not scan again")
	e.svc.writtenMu.Lock()
	e.svc.lastPrune = time.Now().Add(-writtenPruneGap)
	e.svc.writtenMu.Unlock()
	e.svc.markWritten(uuid.Must(uuid.NewV7()))
	assert.Less(t, size(), 10, "pruned again after the gap")
}
