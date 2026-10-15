// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/assignment"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
	"github.com/myriarch/myriarch/internal/store"
)

// ------------------------------------------------------------------ fixtures ----

// fakeArtifacts answers like the artifact cache and records what was asked.
type fakeArtifacts struct {
	mu     sync.Mutex
	state  map[string]artifact.Readiness // "dist/version/os/arch"; missing = ready
	err    map[string]error
	calls  []string
	grants []string
}

func newFakeArtifacts() *fakeArtifacts {
	return &fakeArtifacts{state: map[string]artifact.Readiness{}, err: map[string]error{}}
}

func artKey(dist, version, os, arch string) string {
	return dist + "/" + version + "/" + os + "/" + arch
}

func (f *fakeArtifacts) Prepare(_ context.Context, dist, version, os, arch string) (*domain.Artifact, artifact.Readiness, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := artKey(dist, version, os, arch)
	f.calls = append(f.calls, k)
	if err := f.err[k]; err != nil {
		return nil, artifact.PrepareFailed, err
	}
	if st, ok := f.state[k]; ok && st != artifact.PrepareReady {
		return nil, st, nil
	}
	name := fmt.Sprintf("otelcol-contrib_%s_%s_%s.tar.gz", version, os, arch)
	if dist == domain.DistributionSupervisor {
		name = fmt.Sprintf("opampsupervisor_%s_%s_%s", version, os, arch)
		if os == "windows" {
			name += ".exe"
		}
	}
	return &domain.Artifact{
		ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(k)), Distribution: dist, Version: version, OS: os, Arch: arch, Filename: name,
		Status: domain.ArtifactReady, SHA256: make(domain.Hash, 32), SignatureVerified: true,
	}, artifact.PrepareReady, nil
}

func (f *fakeArtifacts) InstallGrant(a *domain.Artifact, token uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := "myrd_" + base64.RawURLEncoding.EncodeToString([]byte(a.ID.String()+"|"+token.String())) + ".c2ln"
	f.grants = append(f.grants, g)
	return g, nil
}

func (f *fakeArtifacts) InstallURL(a *domain.Artifact) (string, error) {
	return "https://fleet.example.com/api/v1/artifacts/" + a.ID.String() + "/content/" + a.Filename, nil
}

func (f *fakeArtifacts) prepared() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeArtifacts) set(dist, version, os, arch string, st artifact.Readiness) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[artKey(dist, version, os, arch)] = st
}

type planEnv struct {
	t        *testing.T
	st       *memstore.Store
	assigner *assignment.Service
	arts     *fakeArtifacts
	planner  *Planner
	cfg      config.InstallConfig
	cred     Credential
}

func selectorOf(attrs map[string]string, exprs ...domain.SelectorRequirement) domain.Selector {
	return domain.Selector{MatchAttributes: attrs, MatchExpressions: exprs}
}

func fleetOf(name string, prio int, sel domain.Selector, version string, exec domain.ExecutorType, mut ...func(f *domain.Fleet)) domain.Fleet {
	f := domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: name}, FleetSpec: domain.FleetSpec{
		Priority: prio, Selector: sel, Collector: domain.CollectorSpec{Version: version}, Update: domain.UpdateSpec{Executor: exec},
	}}
	for _, m := range mut {
		m(&f)
	}
	return f
}

func in(key string, values ...string) domain.SelectorRequirement {
	return domain.SelectorRequirement{Key: key, Operator: domain.OpIn, Values: values}
}

// standardFleets are the fleets of most tests:
//
//	linux-hosts    linux at edge sites and data centers, 0.162.0, updater, frame >=0.161.0 <=0.162.0
//	windows-hosts  windows, 0.162.0, updater, no frame
//	office         linux, site.type office, 0.162.0, AWX
//	pilot          labels.channel=pilot on linux, priority 300
//	custom         site.type custom, another distribution
//	unmanaged      site.type unmanaged, no version
//	held           site.type held, 0.161.0 (a selection that became invalid is put in by the test)
//	old            site.type old, 0.159.0 (no supervisor release known)
//	ancient        site.type ancient, 0.100.0 (no release known)
//	framed         site.type framed, 0.162.0, frame >=0.150.0 <=0.162.0
//	twin-a/twin-b  site.type twin, the same priority
func standardFleets() []domain.Fleet {
	return []domain.Fleet{
		fleetOf("linux-hosts", 100, selectorOf(map[string]string{"os.type": "linux"}, in("site.type", "edge", "datacenter")), "0.162.0", domain.ExecutorUpdater,
			func(f *domain.Fleet) {
				f.Update.Selection = &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <=0.162.0"}
			}),
		fleetOf("windows-hosts", 100, selectorOf(map[string]string{"os.type": "windows"}), "0.162.0", domain.ExecutorUpdater),
		fleetOf("office", 100, selectorOf(map[string]string{"os.type": "linux", "site.type": "office"}), "0.162.0", domain.ExecutorAWX),
		fleetOf("pilot", 300, selectorOf(map[string]string{"os.type": "linux", "labels.channel": "pilot"}), "0.162.0", domain.ExecutorUpdater),
		fleetOf("custom", 100, selectorOf(map[string]string{"site.type": "custom"}), "0.162.0", domain.ExecutorUpdater,
			func(f *domain.Fleet) { f.Collector.Distribution = "otelcol-custom" }),
		fleetOf("unmanaged", 100, selectorOf(map[string]string{"site.type": "unmanaged"}), "", domain.ExecutorNone),
		fleetOf("held", 100, selectorOf(map[string]string{"site.type": "held"}), "0.161.0", domain.ExecutorUpdater,
			func(f *domain.Fleet) {
				f.Update.Selection = &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.161.0, <=0.162.0"}
			}),
		fleetOf("old", 100, selectorOf(map[string]string{"site.type": "old"}), "0.159.0", domain.ExecutorUpdater),
		fleetOf("ancient", 100, selectorOf(map[string]string{"site.type": "ancient"}), "0.100.0", domain.ExecutorUpdater),
		fleetOf("framed", 100, selectorOf(map[string]string{"site.type": "framed"}), "0.162.0", domain.ExecutorUpdater,
			func(f *domain.Fleet) {
				f.Update.Selection = &domain.VersionSelectionSpec{Enabled: true, VersionRange: ">=0.150.0, <=0.162.0"}
			}),
		fleetOf("twin-a", 100, selectorOf(map[string]string{"site.type": "twin"}), "0.162.0", domain.ExecutorUpdater),
		fleetOf("twin-b", 100, selectorOf(map[string]string{"site.type": "twin"}), "0.162.0", domain.ExecutorUpdater),
	}
}

func newPlanEnv(t *testing.T, mut ...func(e *planEnv)) *planEnv {
	t.Helper()
	e := &planEnv{t: t, st: memstore.New(), arts: newFakeArtifacts(), cfg: config.Default().Install,
		cred: Credential{TokenID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Labels: map[string]string{}}}
	ctx := context.Background()
	_, err := e.st.GitOps().ApplySnapshot(ctx, domain.GitOpsSnapshot{Commit: "c1", Fleets: standardFleets()}, domain.GitOpsSync{})
	require.NoError(t, err)
	for _, r := range []struct{ dist, tag string }{
		{domain.DistributionContrib, "0.162.0"}, {domain.DistributionContrib, "0.161.0"}, {domain.DistributionContrib, "0.160.0"},
		{domain.DistributionContrib, "0.159.0"}, {domain.DistributionContrib, "0.155.0-rc.1"},
		{domain.DistributionSupervisor, "0.162.0"}, {domain.DistributionSupervisor, "0.161.0"}, {domain.DistributionSupervisor, "0.160.0"},
	} {
		_, err := e.st.Releases().Upsert(ctx, domain.Release{Distribution: r.dist, Version: r.tag, Tag: "v" + r.tag})
		require.NoError(t, err)
	}
	e.assigner = assignment.New(assignment.Deps{Store: e.st})
	_, err = e.assigner.Reload(ctx)
	require.NoError(t, err)
	for _, m := range mut {
		m(e)
	}
	e.rebuild()
	return e
}

func (e *planEnv) rebuild() {
	e.t.Helper()
	p, err := NewPlanner(Deps{Store: e.st, Assigner: e.assigner, Artifacts: e.arts, Config: e.cfg, PublicURL: "https://fleet.example.com", OpAMPPath: "/v1/opamp"})
	require.NoError(e.t, err)
	e.planner = p
}

func (e *planEnv) reload() {
	e.t.Helper()
	_, err := e.assigner.Reload(context.Background())
	require.NoError(e.t, err)
}

// request is a host at an edge site on Linux.
func request(mut ...func(r *PlanRequest)) PlanRequest {
	r := PlanRequest{OS: "linux", Arch: "amd64", Attributes: map[string]string{"host.name": "edge-042.example.com", "site.type": "edge", "site.id": "edge-042"}}
	for _, m := range mut {
		m(&r)
	}
	return r
}

func siteType(t string) func(r *PlanRequest) {
	return func(r *PlanRequest) { r.Attributes["site.type"] = t }
}

func (e *planEnv) plan(req PlanRequest) (*Plan, error) {
	return e.planner.Plan(context.Background(), e.cred, req)
}

func (e *planEnv) mustPlan(req PlanRequest) *Plan {
	e.t.Helper()
	p, err := e.plan(req)
	require.NoError(e.t, err)
	require.NotNil(e.t, p)
	return p
}

// refusal asserts that err is the refusal with this class and code and returns it.
func refusal(t *testing.T, err error, class error, code string) *Refusal {
	t.Helper()
	require.Error(t, err)
	var r *Refusal
	require.True(t, errors.As(err, &r), "not a refusal: %v", err)
	assert.ErrorIs(t, err, class)
	assert.Equal(t, code, r.Code, r.Detail)
	assert.NotEmpty(t, r.Detail)
	return r
}

// invalid asserts that err is a validation error with an issue of this code at this path.
func invalid(t *testing.T, err error, code, path string) domain.Issue {
	t.Helper()
	require.Error(t, err)
	var ve *domain.ValidationError
	require.True(t, errors.As(err, &ve), "not a validation error: %v", err)
	assert.ErrorIs(t, err, domain.ErrValidation)
	for _, is := range ve.Issues {
		if is.Code == code && is.Path == path {
			assert.Equal(t, domain.SeverityError, is.Severity)
			assert.NotEmpty(t, is.Message)
			return is
		}
	}
	t.Fatalf("no issue %s at %s in %+v", code, path, ve.Issues)
	return domain.Issue{}
}

func (e *planEnv) rollout(fleet string, kind domain.RolloutKind, status domain.RolloutStatus, from, to string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	require.NoError(e.t, e.st.Rollouts().Create(context.Background(), &domain.Rollout{
		ID: id, Kind: kind, FleetName: fleet, PolicyName: "default", Status: status, FromRef: from, ToRef: to, CreatedBy: "gitops",
	}, nil))
	return id
}

// ----------------------------------------------------------------- the plan ----

func TestPlanForAHostOnALinuxFleet(t *testing.T) {
	e := newPlanEnv(t)
	p := e.mustPlan(request())
	assert.Equal(t, PlanFormat, p.Format)
	assert.Equal(t, "linux-hosts", p.Fleet)
	assert.Equal(t, SourceGit, p.Version.Source)
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", p.OpAMP.Endpoint)
	assert.Equal(t, "0.162.0", p.Collector.Version)
	assert.Equal(t, "0.162.0", p.Supervisor.Version, "the supervisor has the version of the collector")
	assert.True(t, p.Updater.Install, "the fleet updates through the updater")
	assert.Regexp(t, `^https://fleet\.example\.com/api/v1/artifacts/[0-9a-f-]{36}/content/otelcol-contrib_0\.162\.0_linux_amd64\.tar\.gz$`, p.Collector.URL)
	assert.Regexp(t, `^https://fleet\.example\.com/api/v1/artifacts/[0-9a-f-]{36}/content/opampsupervisor_0\.162\.0_linux_amd64$`, p.Supervisor.URL)
	assert.NotEqual(t, p.Collector.Grant, p.Supervisor.Grant, "one grant per artifact")
	assert.ElementsMatch(t, []string{"otelcol-contrib/0.162.0/linux/amd64", "opampsupervisor/0.162.0/linux/amd64"}, e.arts.prepared())

	// the grants are bound to the enrollment token of the request
	for _, g := range []string{p.Collector.Grant, p.Supervisor.Grant} {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(strings.Split(g, ".")[0], "myrd_"))
		require.NoError(t, err)
		assert.Contains(t, string(raw), e.cred.TokenID.String())
	}
	// and the plan can be written in the line format
	text, err := p.Text()
	require.NoError(t, err)
	_, err = ParseText(text)
	assert.NoError(t, err)
}

func TestPlanForWindowsAndArm(t *testing.T) {
	e := newPlanEnv(t)
	p := e.mustPlan(request(func(r *PlanRequest) { r.OS, r.Arch, r.Attributes["site.type"] = "Windows", "x86_64", "edge" }))
	assert.Equal(t, "windows-hosts", p.Fleet)
	assert.Contains(t, p.Collector.URL, "otelcol-contrib_0.162.0_windows_amd64.tar.gz", "tar.gz on every system")
	assert.Contains(t, p.Supervisor.URL, "opampsupervisor_0.162.0_windows_amd64.exe")

	p = e.mustPlan(request(func(r *PlanRequest) { r.Arch = "aarch64" }))
	assert.Equal(t, "linux-hosts", p.Fleet)
	assert.Contains(t, p.Collector.URL, "linux_arm64")
	assert.Contains(t, e.arts.prepared(), "opampsupervisor/0.162.0/linux/arm64")
}

func TestPlanDoesNotInstallTheUpdaterForOtherExecutors(t *testing.T) {
	e := newPlanEnv(t)
	p := e.mustPlan(request(siteType("office")))
	assert.Equal(t, "office", p.Fleet)
	assert.False(t, p.Updater.Install, "a fleet that updates through AWX does not need the updater")
}

// The fleet comes from the same rules as for an agent that connects: reported attributes and the labels of the token.
func TestPlanChoosesTheFleetFromAttributesAndTokenLabels(t *testing.T) {
	e := newPlanEnv(t)
	assert.Equal(t, "linux-hosts", e.mustPlan(request()).Fleet)
	e.cred.Labels = map[string]string{"channel": "pilot"}
	assert.Equal(t, "pilot", e.mustPlan(request()).Fleet, "a label of the token has the higher priority")
	e.cred.Labels = map[string]string{"channel": "other"}
	assert.Equal(t, "linux-hosts", e.mustPlan(request()).Fleet)

	// a host cannot claim a label for itself
	e.cred.Labels = nil
	_, err := e.plan(request(func(r *PlanRequest) { r.Attributes["labels.channel"] = "pilot" }))
	invalid(t, err, CodeUnknownAttribute, "attributes.labels.channel")
}

func TestPlanUsesTheInventoryOfTheRepository(t *testing.T) {
	e := newPlanEnv(t)
	fleets := append(standardFleets(), fleetOf("by-inventory", 400, selectorOf(map[string]string{"os.type": "linux", "labels.zone": "z1"}), "0.161.0", domain.ExecutorUpdater))
	_, err := e.st.GitOps().ApplySnapshot(context.Background(), domain.GitOpsSnapshot{Commit: "c2", Fleets: fleets, Inventories: []domain.Inventory{{
		ObjectMeta: domain.ObjectMeta{Name: "zones"},
		Entries:    []domain.InventoryEntry{{Match: map[string]string{"host.name": "edge-042.example.com"}, Labels: map[string]string{"zone": "z1"}}},
	}}}, domain.GitOpsSync{})
	require.NoError(t, err)
	e.reload()
	p := e.mustPlan(request())
	assert.Equal(t, "by-inventory", p.Fleet, "labels from an inventory entry count like for a connected agent")
	assert.Equal(t, "0.161.0", p.Collector.Version)
	other := e.mustPlan(request(func(r *PlanRequest) { r.Attributes["host.name"] = "edge-043.example.com" }))
	assert.Equal(t, "linux-hosts", other.Fleet)
}

// -------------------------------------------------------------------- fleets ----

func TestPlanFleetHint(t *testing.T) {
	e := newPlanEnv(t)
	// a hint that agrees is fine
	p := e.mustPlan(request(func(r *PlanRequest) { r.Fleet = "linux-hosts" }))
	assert.Equal(t, "linux-hosts", p.Fleet)
	// one that does not is refused, whether the other fleet exists or not, and nothing is prepared
	for _, hint := range []string{"windows-hosts", "no-such-fleet"} {
		before := len(e.arts.prepared())
		_, err := e.plan(request(func(r *PlanRequest) { r.Fleet = hint }))
		r := refusal(t, err, domain.ErrConflict, CodeFleetMismatch)
		assert.Contains(t, r.Detail, "linux-hosts")
		assert.Len(t, e.arts.prepared(), before, "nothing is delivered")
	}
}

func TestPlanWithoutAFleet(t *testing.T) {
	e := newPlanEnv(t)
	_, err := e.plan(request(siteType("lab")))
	refusal(t, err, domain.ErrConflict, CodeNoFleet)
	// the hint does not change that
	_, err = e.plan(request(siteType("lab"), func(r *PlanRequest) { r.Fleet = "linux-hosts" }))
	refusal(t, err, domain.ErrConflict, CodeNoFleet)
	assert.Empty(t, e.arts.prepared())
}

func TestPlanWithSeveralFleetsOfTheSamePriority(t *testing.T) {
	e := newPlanEnv(t)
	_, err := e.plan(request(siteType("twin")))
	r := refusal(t, err, domain.ErrConflict, CodeFleetAmbiguous)
	assert.Contains(t, r.Detail, "twin-a")
	assert.Contains(t, r.Detail, "twin-b")
	// also with a hint that names one of them: the host would be unassigned
	_, err = e.plan(request(siteType("twin"), func(r *PlanRequest) { r.Fleet = "twin-a" }))
	refusal(t, err, domain.ErrConflict, CodeFleetAmbiguous)
	assert.Empty(t, e.arts.prepared())
}

func TestPlanRefusesAnotherDistribution(t *testing.T) {
	e := newPlanEnv(t)
	_, err := e.plan(request(siteType("custom")))
	r := refusal(t, err, domain.ErrPrecondition, CodeUnsupportedDistribution)
	assert.Contains(t, r.Detail, "otelcol-custom")
	assert.Empty(t, e.arts.prepared())
}

// --------------------------------------------------------------- the version ----

func TestPlanFollowsTheVersionChosenInTheUI(t *testing.T) {
	e := newPlanEnv(t)
	_, err := e.st.FleetVersions().Put(context.Background(), domain.FleetVersionSelection{
		Fleet: "linux-hosts", Version: "0.161.0", State: domain.SelectionActive, GitVersion: "0.162.0", SelectedBy: "user:x", SelectedAt: time.Now(),
	}, nil)
	require.NoError(t, err)
	e.reload()
	p := e.mustPlan(request())
	assert.Equal(t, "0.161.0", p.Collector.Version)
	assert.Equal(t, "0.161.0", p.Supervisor.Version)
	assert.Equal(t, SourceUI, p.Version.Source)
}

func TestPlanRefusesAFleetWithoutATargetVersion(t *testing.T) {
	e := newPlanEnv(t)
	_, err := e.plan(request(siteType("unmanaged")))
	r := refusal(t, err, domain.ErrPrecondition, CodeNoTargetVersion)
	assert.Contains(t, r.Detail, "no target version")

	// a fleet on hold: the selection became invalid
	_, err = e.st.FleetVersions().Put(context.Background(), domain.FleetVersionSelection{
		Fleet: "held", Version: "0.163.0", State: domain.SelectionInvalidated, StateReason: "outside the frame", GitVersion: "0.161.0", SelectedBy: "user:x", SelectedAt: time.Now(),
	}, nil)
	require.NoError(t, err)
	e.reload()
	_, err = e.plan(request(siteType("held")))
	r = refusal(t, err, domain.ErrPrecondition, CodeNoTargetVersion)
	assert.Contains(t, r.Detail, "on hold")

	// an explicit version does not help: there is no target to install next to
	_, err = e.plan(request(siteType("held"), func(r *PlanRequest) { r.CollectorVersion = "0.161.0" }))
	refusal(t, err, domain.ErrPrecondition, CodeNoTargetVersion)
	_, err = e.plan(request(siteType("unmanaged"), func(r *PlanRequest) { r.CollectorVersion = "0.162.0" }))
	refusal(t, err, domain.ErrPrecondition, CodeNoTargetVersion)
	assert.Empty(t, e.arts.prepared())
}

// While a version rollout runs, a new host takes the version the fleet ran before, so that it becomes a straggler
// of the last wave and the waves and approvals apply to it as well.
func TestPlanDuringAVersionRolloutTakesTheOldVersion(t *testing.T) {
	for _, st := range []domain.RolloutStatus{domain.RolloutPending, domain.RolloutRunning, domain.RolloutWaitingApproval} {
		t.Run(string(st), func(t *testing.T) {
			e := newPlanEnv(t)
			e.rollout("linux-hosts", domain.RolloutKindVersion, st, "0.161.0", "0.162.0")
			p := e.mustPlan(request())
			assert.Equal(t, "0.161.0", p.Collector.Version, "the fromRef, not the target")
			assert.Equal(t, "0.161.0", p.Supervisor.Version)
			assert.Equal(t, SourceRollout, p.Version.Source)
			assert.Contains(t, p.Collector.URL, "otelcol-contrib_0.161.0_")
		})
	}
}

func TestPlanRefusesWhileTheVersionRolloutIsStuck(t *testing.T) {
	for _, st := range []domain.RolloutStatus{domain.RolloutPaused, domain.RolloutFailed, domain.RolloutRollingBack} {
		t.Run(string(st), func(t *testing.T) {
			e := newPlanEnv(t)
			e.rollout("linux-hosts", domain.RolloutKindVersion, st, "0.161.0", "0.162.0")
			_, err := e.plan(request())
			r := refusal(t, err, domain.ErrConflict, CodeRolloutBlocking)
			assert.Contains(t, r.Detail, strings.ReplaceAll(string(st), "_", " "))
			assert.Empty(t, e.arts.prepared())
		})
	}
	// it blocks even where another rollout of the fleet runs
	e := newPlanEnv(t)
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutRunning, "0.161.0", "0.162.0")
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutPaused, "0.160.0", "0.161.0")
	_, err := e.plan(request())
	refusal(t, err, domain.ErrConflict, CodeRolloutBlocking)
}

func TestPlanRefusesARolloutWithoutASingleStartingVersion(t *testing.T) {
	e := newPlanEnv(t)
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutRunning, "", "0.162.0") // mixed fleet
	_, err := e.plan(request())
	r := refusal(t, err, domain.ErrConflict, CodeRolloutBlocking)
	assert.Contains(t, r.Detail, "different versions")

	e = newPlanEnv(t)
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutRunning, "0.161.0", "0.162.0")
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutPending, "0.160.0", "0.162.0")
	_, err = e.plan(request())
	refusal(t, err, domain.ErrConflict, CodeRolloutBlocking)

	// two rollouts that agree are fine, and "v" is not part of a ref
	e = newPlanEnv(t)
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutRunning, "0.161.0", "0.162.0")
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutPending, "v0.161.0", "0.162.0")
	assert.Equal(t, "0.161.0", e.mustPlan(request()).Collector.Version)
}

func TestPlanIgnoresRolloutsThatDoNotCount(t *testing.T) {
	e := newPlanEnv(t)
	for _, st := range []domain.RolloutStatus{domain.RolloutCompleted, domain.RolloutAborted, domain.RolloutRolledBack, domain.RolloutSuperseded} {
		e.rollout("linux-hosts", domain.RolloutKindVersion, st, "0.160.0", "0.161.0")
	}
	e.rollout("linux-hosts", domain.RolloutKindConfig, domain.RolloutPaused, "abc", "def")            // another kind
	e.rollout("windows-hosts", domain.RolloutKindVersion, domain.RolloutPaused, "0.161.0", "0.162.0") // another fleet
	p := e.mustPlan(request())
	assert.Equal(t, "0.162.0", p.Collector.Version)
	assert.Equal(t, SourceGit, p.Version.Source)
}

func TestPlanWithAVersionRequestedByTheHost(t *testing.T) {
	e := newPlanEnv(t)
	// inside the frame in Git
	p := e.mustPlan(request(func(r *PlanRequest) { r.CollectorVersion = "0.161.0" }))
	assert.Equal(t, "0.161.0", p.Collector.Version)
	assert.Equal(t, "0.161.0", p.Supervisor.Version)
	assert.Equal(t, SourceRequest, p.Version.Source)
	// with a leading v and spaces around it, as people write it; the version that counts is the normalized one
	p = e.mustPlan(request(func(r *PlanRequest) { r.CollectorVersion = " v0.161.0\n" }))
	assert.Equal(t, "0.161.0", p.Collector.Version)
	// the version the host would get anyway is no request
	p = e.mustPlan(request(func(r *PlanRequest) { r.CollectorVersion = "0.162.0" }))
	assert.Equal(t, SourceGit, p.Version.Source)

	// outside the frame: 422 and nothing prepared
	before := len(e.arts.prepared())
	for _, v := range []string{"0.160.0", "0.163.0", "1.0.0"} {
		_, err := e.plan(request(func(r *PlanRequest) { r.CollectorVersion = v }))
		is := invalid(t, err, CodeVersionNotAllowed, "collectorVersion")
		assert.Contains(t, is.Message, "outside the frame")
	}
	assert.Len(t, e.arts.prepared(), before)

	// a fleet without a frame accepts its own version only
	_, err := e.plan(request(func(r *PlanRequest) { r.OS, r.CollectorVersion = "windows", "0.161.0" }))
	is := invalid(t, err, CodeVersionNotAllowed, "collectorVersion")
	assert.Contains(t, is.Message, "no version frame")
	p = e.mustPlan(request(func(r *PlanRequest) { r.OS, r.CollectorVersion = "windows", "0.162.0" }))
	assert.Equal(t, "windows-hosts", p.Fleet)
}

func TestPlanWithAVersionRequestedDuringARollout(t *testing.T) {
	e := newPlanEnv(t)
	e.rollout("linux-hosts", domain.RolloutKindVersion, domain.RolloutRunning, "0.161.0", "0.162.0")
	// the old version is what the plan gives anyway, the new one is the target: a host that asks for it takes it
	// (and skips the waves, which the person who installs it decides)
	p := e.mustPlan(request(func(r *PlanRequest) { r.CollectorVersion = "0.161.0" }))
	assert.Equal(t, SourceRollout, p.Version.Source)
	p = e.mustPlan(request(func(r *PlanRequest) { r.CollectorVersion = "0.162.0" }))
	assert.Equal(t, "0.162.0", p.Collector.Version)
	assert.Equal(t, SourceRequest, p.Version.Source)
	// anything else outside the frame is refused
	_, err := e.plan(request(func(r *PlanRequest) { r.CollectorVersion = "0.160.0" }))
	invalid(t, err, CodeVersionNotAllowed, "collectorVersion")
}

// The release tracker decides which versions exist for the server.
func TestPlanOnlyOffersVersionsTheReleaseTrackerKnows(t *testing.T) {
	e := newPlanEnv(t)
	// the collector release is known, the supervisor release is not
	_, err := e.plan(request(siteType("old")))
	r := refusal(t, err, domain.ErrPrecondition, CodeUnknownSupervisorVersion)
	assert.Contains(t, r.Detail, "0.159.0")
	// neither is known
	_, err = e.plan(request(siteType("ancient")))
	refusal(t, err, domain.ErrPrecondition, CodeUnknownVersion)
	assert.Empty(t, e.arts.prepared(), "nothing is downloaded for a version that nobody knows")

	// a version that the host asks for and the tracker does not know is a problem of the request
	_, err = e.plan(request(siteType("framed"), func(r *PlanRequest) { r.CollectorVersion = "0.155.1" }))
	is := invalid(t, err, CodeUnknownVersion, "collectorVersion")
	assert.Contains(t, is.Message, "0.155.1")
	_, err = e.plan(request(siteType("framed"), func(r *PlanRequest) { r.CollectorVersion = "0.159.0" }))
	invalid(t, err, CodeUnknownSupervisorVersion, "collectorVersion")
	// known and inside the frame
	assert.Equal(t, "0.160.0", e.mustPlan(request(siteType("framed"), func(r *PlanRequest) { r.CollectorVersion = "0.160.0" })).Collector.Version)
}

// --------------------------------------------------------------- the artifacts ----

func TestPlanAnswersPreparingUntilBothArtifactsAreReady(t *testing.T) {
	e := newPlanEnv(t)
	e.arts.set(domain.DistributionContrib, "0.162.0", "linux", "amd64", artifact.PreparePending)
	_, err := e.plan(request())
	r := refusal(t, err, domain.ErrUnavailable, CodePreparing)
	assert.Equal(t, 10*time.Second, r.RetryAfter)
	assert.Contains(t, r.Detail, "linux/amd64")
	assert.Contains(t, r.Detail, "112 MB")
	// both downloads are started, so they run side by side
	assert.ElementsMatch(t, []string{"otelcol-contrib/0.162.0/linux/amd64", "opampsupervisor/0.162.0/linux/amd64"}, e.arts.prepared())

	// the other one is not ready: the same
	e = newPlanEnv(t)
	e.arts.set(domain.DistributionSupervisor, "0.162.0", "linux", "amd64", artifact.PreparePending)
	_, err = e.plan(request())
	refusal(t, err, domain.ErrUnavailable, CodePreparing)

	// a platform has its own artifacts
	p := e.mustPlan(request(func(r *PlanRequest) { r.Arch = "arm64" }))
	assert.Contains(t, p.Collector.URL, "linux_arm64")
}

func TestPlanSaysSoWhenThePreparationFailed(t *testing.T) {
	e := newPlanEnv(t)
	e.arts.set(domain.DistributionSupervisor, "0.162.0", "linux", "amd64", artifact.PrepareFailed)
	_, err := e.plan(request())
	r := refusal(t, err, domain.ErrUnavailable, CodePreparing)
	assert.Equal(t, 30*time.Second, r.RetryAfter)
	assert.Contains(t, r.Detail, "failed")
	assert.NotContains(t, r.Detail, "http", "the reason stays on the server")
}

func TestPlanMapsPrepareErrors(t *testing.T) {
	e := newPlanEnv(t)
	e.arts.err[artKey(domain.DistributionContrib, "0.162.0", "linux", "amd64")] = fmt.Errorf("%w: invalid version", domain.ErrInvalidArgument)
	_, err := e.plan(request())
	refusal(t, err, domain.ErrPrecondition, CodeUnknownVersion)

	e = newPlanEnv(t)
	e.arts.err[artKey(domain.DistributionSupervisor, "0.162.0", "linux", "amd64")] = errors.New("database is down (password=hunter2)")
	_, err = e.plan(request())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	var r *Refusal
	assert.False(t, errors.As(err, &r))
}

// ------------------------------------------------------------------ the request ----

func TestPlanChecksTheRequest(t *testing.T) {
	e := newPlanEnv(t)
	cases := map[string]struct {
		mut  func(r *PlanRequest)
		code string
		path string
	}{
		"no os":                     {func(r *PlanRequest) { r.OS = "" }, CodeInvalidValue, "os"},
		"macOS":                     {func(r *PlanRequest) { r.OS = "darwin" }, CodeInvalidValue, "os"},
		"an os with a path":         {func(r *PlanRequest) { r.OS = "linux/../x" }, CodeInvalidValue, "os"},
		"no arch":                   {func(r *PlanRequest) { r.Arch = "" }, CodeInvalidValue, "arch"},
		"386":                       {func(r *PlanRequest) { r.Arch = "386" }, CodeInvalidValue, "arch"},
		"windows on arm64":          {func(r *PlanRequest) { r.OS, r.Arch = "windows", "arm64" }, CodePlatformNotSupported, "os"},
		"unknown attribute":         {func(r *PlanRequest) { r.Attributes["service.name"] = "x" }, CodeUnknownAttribute, "attributes.service.name"},
		"os.type":                   {func(r *PlanRequest) { r.Attributes["os.type"] = "linux" }, CodeUnknownAttribute, "attributes.os.type"},
		"host.arch":                 {func(r *PlanRequest) { r.Attributes["host.arch"] = "amd64" }, CodeUnknownAttribute, "attributes.host.arch"},
		"a label":                   {func(r *PlanRequest) { r.Attributes["labels.pilot"] = "true" }, CodeUnknownAttribute, "attributes.labels.pilot"},
		"an empty value":            {func(r *PlanRequest) { r.Attributes["site.name"] = "" }, CodeInvalidValue, "attributes.site.name"},
		"a newline":                 {func(r *PlanRequest) { r.Attributes["site.name"] = "a\nb" }, CodeInvalidValue, "attributes.site.name"},
		"a nul":                     {func(r *PlanRequest) { r.Attributes["site.id"] = "a\x00b" }, CodeInvalidValue, "attributes.site.id"},
		"an escape":                 {func(r *PlanRequest) { r.Attributes["site.id"] = "a\x1b[31m" }, CodeInvalidValue, "attributes.site.id"},
		"a leading space":           {func(r *PlanRequest) { r.Attributes["site.name"] = " Edge" }, CodeInvalidValue, "attributes.site.name"},
		"a trailing space":          {func(r *PlanRequest) { r.Attributes["site.name"] = "Edge " }, CodeInvalidValue, "attributes.site.name"},
		"invalid UTF-8":             {func(r *PlanRequest) { r.Attributes["site.name"] = "a\xc3\x28" }, CodeInvalidValue, "attributes.site.name"},
		"too long":                  {func(r *PlanRequest) { r.Attributes["site.name"] = strings.Repeat("a", MaxAttributeBytes+1) }, CodeInvalidValue, "attributes.site.name"},
		"an unprintable key":        {func(r *PlanRequest) { r.Attributes["a\x00b"] = "x" }, CodeUnknownAttribute, "attributes.a?b"},
		"an upper case fleet":       {func(r *PlanRequest) { r.Fleet = "Linux-Hosts" }, CodeInvalidValue, "fleet"},
		"a fleet with a slash":      {func(r *PlanRequest) { r.Fleet = "a/b" }, CodeInvalidValue, "fleet"},
		"a fleet with a newline":    {func(r *PlanRequest) { r.Fleet = "a\nb" }, CodeInvalidValue, "fleet"},
		"a word as the version":     {func(r *PlanRequest) { r.CollectorVersion = "latest" }, CodeInvalidValue, "collectorVersion"},
		"a short version":           {func(r *PlanRequest) { r.CollectorVersion = "0.162" }, CodeInvalidValue, "collectorVersion"},
		"a version with a command":  {func(r *PlanRequest) { r.CollectorVersion = "0.162.0;id" }, CodeInvalidValue, "collectorVersion"},
		"a range as the version":    {func(r *PlanRequest) { r.CollectorVersion = ">=0.161.0" }, CodeInvalidValue, "collectorVersion"},
		"an overlong prerelease":    {func(r *PlanRequest) { r.CollectorVersion = "0.162.0-" + strings.Repeat("a", 80) }, CodeInvalidValue, "collectorVersion"},
		"a version with a space":    {func(r *PlanRequest) { r.CollectorVersion = "0.162.0 -x" }, CodeInvalidValue, "collectorVersion"},
		"a fully qualified name":    {func(r *PlanRequest) { r.CollectorVersion = "otelcol-contrib/0.162.0" }, CodeInvalidValue, "collectorVersion"},
		"a version in the OS field": {func(r *PlanRequest) { r.OS = "0.162.0" }, CodeInvalidValue, "os"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.plan(request(c.mut))
			is := invalid(t, err, c.code, c.path)
			assert.NotContains(t, is.Message, "\x00", "a message never repeats unprintable input")
			assert.NotContains(t, is.Message, "\x1b")
		})
	}
	assert.Empty(t, e.arts.prepared(), "nothing happens for an invalid request")

	// every problem is reported, not only the first
	_, err := e.plan(PlanRequest{OS: "plan9", Arch: "mips", Attributes: map[string]string{"x": "1", "site.id": ""}, Fleet: "A", CollectorVersion: "x"})
	var ve *domain.ValidationError
	require.True(t, errors.As(err, &ve))
	assert.GreaterOrEqual(t, len(ve.Issues), 6, "%+v", ve.Issues)
}

func TestPlanRefusesPlatformsThatAreNotConfigured(t *testing.T) {
	e := newPlanEnv(t, func(e *planEnv) { e.cfg.Platforms = []string{"linux/amd64"} })
	_, err := e.plan(request(func(r *PlanRequest) { r.Arch = "arm64" }))
	is := invalid(t, err, CodePlatformNotSupported, "os")
	assert.Contains(t, is.Message, "linux/amd64")
	assert.Contains(t, is.Message, "install.platforms")
	assert.Equal(t, "linux-hosts", e.mustPlan(request()).Fleet)
}

func TestPlanRefusesAHugeAttributeMap(t *testing.T) {
	e := newPlanEnv(t)
	attrs := map[string]string{}
	for i := 0; i < 500; i++ {
		attrs[fmt.Sprintf("x%d", i)] = "1"
	}
	_, err := e.plan(PlanRequest{OS: "linux", Arch: "amd64", Attributes: attrs})
	var ve *domain.ValidationError
	require.True(t, errors.As(err, &ve))
	assert.Len(t, ve.Issues, 1, "one issue, not one per key")
	assert.Equal(t, "attributes", ve.Issues[0].Path)
}

func TestVersionSourcesAreTheSourcesOfTheTarget(t *testing.T) {
	assert.Equal(t, SourceGit, string(domain.TargetSourceGit))
	assert.Equal(t, SourceUI, string(domain.TargetSourceUI))
}

func TestPlanAcceptsEveryAllowedAttribute(t *testing.T) {
	e := newPlanEnv(t)
	attrs := map[string]string{}
	for _, k := range AllowedAttributes {
		attrs[k] = "value-of-" + k
	}
	attrs["site.type"] = "edge"
	attrs["site.name"] = "Edge site 42 (Amsterdam)"
	attrs["site.country"] = "NL"
	attrs["site.timezone"] = "Europe/Amsterdam"
	p := e.mustPlan(PlanRequest{OS: "linux", Arch: "amd64", Attributes: attrs})
	assert.Equal(t, "linux-hosts", p.Fleet)
}

func TestPlanNeedsTheEnrollmentTokenOfTheRequest(t *testing.T) {
	e := newPlanEnv(t)
	_, err := e.planner.Plan(context.Background(), Credential{}, request())
	require.Error(t, err)
	assert.Empty(t, e.arts.prepared())
}

// ------------------------------------------------------------------ the settings ----

func TestOpAMPEndpoint(t *testing.T) {
	cases := []struct{ public, path, want string }{
		{"https://fleet.example.com", "/v1/opamp", "wss://fleet.example.com/v1/opamp"},
		{"https://fleet.example.com/", "/v1/opamp", "wss://fleet.example.com/v1/opamp"},
		{"http://localhost:8080", "/v1/opamp", "ws://localhost:8080/v1/opamp"},
		{"https://example.com/myriarch/", "/v1/opamp", "wss://example.com/myriarch/v1/opamp"},
		{"https://[2001:db8::1]:8443", "/v1/opamp", "wss://[2001:db8::1]:8443/v1/opamp"},
		{"https://fleet.example.com?x=1#y", "/v1/opamp", "wss://fleet.example.com/v1/opamp"},
	}
	for _, c := range cases {
		got, err := opampEndpoint(c.public, c.path)
		require.NoError(t, err, c.public)
		assert.Equal(t, c.want, got)
	}
	for _, bad := range []string{"", "fleet.example.com", "ftp://fleet.example.com", "https://", "https://user:pw@fleet.example.com", "ws://fleet.example.com"} {
		_, err := opampEndpoint(bad, "/v1/opamp")
		assert.Error(t, err, bad)
	}
}

func TestPlanNeedsAUsablePublicURL(t *testing.T) {
	e := newPlanEnv(t)
	for name, public := range map[string]string{"none": "", "not absolute": "fleet.example.com"} {
		p, err := NewPlanner(Deps{Store: e.st, Assigner: e.assigner, Artifacts: e.arts, Config: e.cfg, PublicURL: public, OpAMPPath: "/v1/opamp"})
		require.NoError(t, err)
		_, err = p.Plan(context.Background(), e.cred, request())
		refusal(t, err, domain.ErrPrecondition, CodeServerSettings)
		_ = name
	}
	// a public URL that the line format cannot hold is a setting to repair, not a plan with a character in it
	p, err := NewPlanner(Deps{Store: e.st, Assigner: e.assigner, Artifacts: e.arts, Config: e.cfg, PublicURL: "https://fleet.example.com/my~path", OpAMPPath: "/v1/opamp"})
	require.NoError(t, err)
	_, err = p.Plan(context.Background(), e.cred, request())
	r := refusal(t, err, domain.ErrPrecondition, CodeServerSettings)
	assert.Contains(t, r.Detail, "server.publicUrl")
}

func TestNewPlanner(t *testing.T) {
	e := newPlanEnv(t)
	_, err := NewPlanner(Deps{Store: e.st, Assigner: e.assigner, Artifacts: e.arts, Config: config.InstallConfig{Platforms: []string{"darwin/arm64"}}})
	assert.Error(t, err)
	_, err = NewPlanner(Deps{Assigner: e.assigner, Artifacts: e.arts, Config: e.cfg})
	assert.Error(t, err)
	_, err = NewPlanner(Deps{Store: e.st, Artifacts: e.arts, Config: e.cfg})
	assert.Error(t, err)
	_, err = NewPlanner(Deps{Store: e.st, Assigner: e.assigner, Config: e.cfg})
	assert.Error(t, err)
	assert.True(t, e.planner.Enabled())
	off := newPlanEnv(t, func(e *planEnv) { e.cfg.Enabled = false })
	assert.False(t, off.planner.Enabled())
}

func TestPlatforms(t *testing.T) {
	got, err := ParsePlatforms([]string{"linux/amd64", "windows/amd64", "linux/amd64"})
	require.NoError(t, err)
	assert.Equal(t, []Platform{{"linux", "amd64"}, {"windows", "amd64"}}, got, "duplicates are dropped")
	for _, bad := range []string{"", "linux", "linux/x86_64", "Linux/amd64", "darwin/arm64", "windows/arm64", "linux/amd64 "} {
		_, err := ParsePlatforms([]string{bad})
		assert.Error(t, err, bad)
	}
	assert.Equal(t, Platform{"linux", "arm64"}, NormalizePlatform(" Linux ", "AARCH64"))
	assert.Equal(t, "windows/amd64", Platform{"windows", "amd64"}.String())
}

// ----------------------------------------------------------------- failures ----

// failingStore fails the reads of the tests.
type failingStore struct {
	store.Store
	rollouts, releases bool
}

func (f failingStore) Rollouts() store.RolloutStore {
	if f.rollouts {
		return failingRollouts{f.Store.Rollouts()}
	}
	return f.Store.Rollouts()
}

func (f failingStore) Releases() store.ReleaseStore {
	if f.releases {
		return failingReleases{f.Store.Releases()}
	}
	return f.Store.Releases()
}

type failingRollouts struct{ store.RolloutStore }

func (failingRollouts) List(context.Context, store.RolloutFilter) ([]domain.Rollout, int, error) {
	return nil, 0, errors.New("connection refused (password=hunter2)")
}

type failingReleases struct{ store.ReleaseStore }

func (failingReleases) Get(context.Context, string, string) (*domain.Release, error) {
	return nil, errors.New("connection refused (password=hunter2)")
}

func TestPlanStoreFailuresAreUnavailable(t *testing.T) {
	for name, fs := range map[string]func(e *planEnv) store.Store{
		"rollouts": func(e *planEnv) store.Store { return failingStore{Store: e.st, rollouts: true} },
		"releases": func(e *planEnv) store.Store { return failingStore{Store: e.st, releases: true} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newPlanEnv(t)
			p, err := NewPlanner(Deps{Store: fs(e), Assigner: e.assigner, Artifacts: e.arts, Config: e.cfg, PublicURL: "https://fleet.example.com", OpAMPPath: "/v1/opamp"})
			require.NoError(t, err)
			_, err = p.Plan(context.Background(), e.cred, request())
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrUnavailable)
			assert.Empty(t, e.arts.prepared(), "nothing is downloaded when the state cannot be read")
		})
	}
}

type failingAssigner struct {
	Assigner
	match, snapshot, selections error
}

func (f failingAssigner) Match(ctx context.Context, v domain.AttributeView) (assignment.MatchResult, error) {
	if f.match != nil {
		return assignment.MatchResult{}, f.match
	}
	return f.Assigner.Match(ctx, v)
}

func (f failingAssigner) Snapshot(ctx context.Context) (*domain.GitOpsSnapshot, error) {
	if f.snapshot != nil {
		return nil, f.snapshot
	}
	return f.Assigner.Snapshot(ctx)
}

func (f failingAssigner) VersionSelections(ctx context.Context) (map[string]*domain.FleetVersionSelection, error) {
	if f.selections != nil {
		return nil, f.selections
	}
	return f.Assigner.VersionSelections(ctx)
}

func TestPlanAssignerFailuresAreUnavailable(t *testing.T) {
	boom := errors.New("snapshot unavailable")
	for name, fa := range map[string]func(a Assigner) Assigner{
		"match":      func(a Assigner) Assigner { return failingAssigner{Assigner: a, match: boom} },
		"snapshot":   func(a Assigner) Assigner { return failingAssigner{Assigner: a, snapshot: boom} },
		"selections": func(a Assigner) Assigner { return failingAssigner{Assigner: a, selections: boom} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newPlanEnv(t)
			p, err := NewPlanner(Deps{Store: e.st, Assigner: fa(e.assigner), Artifacts: e.arts, Config: e.cfg, PublicURL: "https://fleet.example.com", OpAMPPath: "/v1/opamp"})
			require.NoError(t, err)
			_, err = p.Plan(context.Background(), e.cred, request())
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrUnavailable)
		})
	}
}

// ----------------------------------------------------------------- concurrency ----

// Rollouts and version selections change while hosts ask for plans. Whatever the interleaving, a plan names a version
// that belongs together with its source, and a stuck rollout is refused: never the new target with the source
// "rollout", never the old version as if it were the target.
func TestPlanUnderConcurrentChanges(t *testing.T) {
	e := newPlanEnv(t)
	ctx := context.Background()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var plans, refused atomic.Int64

	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p, err := e.plan(request())
				if err != nil {
					var r *Refusal
					if !errors.As(err, &r) || r.Code != CodeRolloutBlocking {
						t.Errorf("unexpected error: %v", err)
						return
					}
					refused.Add(1)
					continue
				}
				plans.Add(1)
				switch p.Version.Source {
				case SourceGit:
					if p.Collector.Version != "0.162.0" {
						t.Errorf("source git with version %s", p.Collector.Version)
					}
				case SourceRollout:
					if p.Collector.Version != "0.161.0" {
						t.Errorf("source rollout with version %s", p.Collector.Version)
					}
				case SourceUI:
					if p.Collector.Version != "0.161.0" {
						t.Errorf("source ui with version %s", p.Collector.Version)
					}
				default:
					t.Errorf("source %s", p.Version.Source)
				}
				if p.Collector.Version != p.Supervisor.Version {
					t.Errorf("collector %s and supervisor %s differ", p.Collector.Version, p.Supervisor.Version)
				}
			}
		}()
	}

	// the leader: a rollout is created, runs, is paused, resumed and completes, again and again
	for i := 0; i < 40; i++ {
		id := uuid.New()
		require.NoError(t, e.st.Rollouts().Create(ctx, &domain.Rollout{ID: id, Kind: domain.RolloutKindVersion, FleetName: "linux-hosts", PolicyName: "default",
			Status: domain.RolloutPending, FromRef: "0.161.0", ToRef: "0.162.0", CreatedBy: "gitops"}, nil))
		for _, step := range [][2]domain.RolloutStatus{
			{domain.RolloutPending, domain.RolloutRunning}, {domain.RolloutRunning, domain.RolloutPaused},
			{domain.RolloutPaused, domain.RolloutRunning}, {domain.RolloutRunning, domain.RolloutCompleted},
		} {
			_, err := e.st.Rollouts().CompareAndSetStatus(ctx, id, []domain.RolloutStatus{step[0]}, step[1], "")
			require.NoError(t, err)
			time.Sleep(time.Millisecond)
		}
		// and a selection comes and goes
		if i%10 == 5 {
			_, err := e.st.FleetVersions().Put(ctx, domain.FleetVersionSelection{Fleet: "linux-hosts", Version: "0.161.0", State: domain.SelectionActive,
				GitVersion: "0.162.0", SelectedBy: "user:x", SelectedAt: time.Now()}, nil)
			require.NoError(t, err)
			e.reload()
			time.Sleep(3 * time.Millisecond)
			_, err = e.st.FleetVersions().Delete(ctx, "linux-hosts", nil)
			require.NoError(t, err)
			e.reload()
		}
	}
	close(stop)
	wg.Wait()
	assert.Greater(t, plans.Load(), int64(0))
	assert.Greater(t, refused.Load(), int64(0), "the paused phases were seen")
}
