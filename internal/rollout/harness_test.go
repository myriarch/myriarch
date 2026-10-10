// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
	"github.com/myriarch/myriarch/internal/store"
)

// ------------------------------------------------------------------ clock ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *clock { return &clock{t: t} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// ------------------------------------------------------------------ fakes ----

type recControl struct {
	mu    sync.Mutex
	calls [][]domain.AgentID
}

var _ control.AgentControl = (*recControl)(nil)

func (r *recControl) DesiredStateChanged(_ context.Context, ids []domain.AgentID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]domain.AgentID(nil), ids...))
	return nil
}
func (r *recControl) Restart(context.Context, domain.AgentID, string) error          { return nil }
func (r *recControl) RequestFullState(context.Context, domain.AgentID, string) error { return nil }
func (r *recControl) Resync(context.Context, domain.AgentID, string) error           { return nil }

func (r *recControl) pushed() map[domain.AgentID]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[domain.AgentID]int{}
	for _, c := range r.calls {
		for _, id := range c {
			m[id]++
		}
	}
	return m
}

type recNotifier struct {
	mu   sync.Mutex
	list []notify.Notification
}

func (r *recNotifier) Notify(_ context.Context, n notify.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, n)
	return nil
}
func (r *recNotifier) Test(context.Context, string) error { return nil }
func (r *recNotifier) Channels() []notify.ChannelInfo     { return nil }

func (r *recNotifier) events() []notify.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []notify.EventType
	for _, n := range r.list {
		out = append(out, n.Event)
	}
	return out
}

func (r *recNotifier) all() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Notification(nil), r.list...)
}

// fakeExec is a configurable executor.
type fakeExec struct {
	mu          sync.Mutex
	typ         domain.ExecutorType
	startCalls  []executor.UpdateRequest
	cancelCalls []executor.CancelRequest
	validate    domain.Issues
	startErr    error
	// result customizes the per-target outcome (default: started).
	result func(req executor.UpdateRequest, t executor.Target) executor.TargetResult
	// installs: agent -> version the (simulated) updater/pipeline installs.
	installs map[domain.AgentID]string
}

func (f *fakeExec) Type() domain.ExecutorType { return f.typ }

func (f *fakeExec) Validate(spec domain.UpdateSpec, def *domain.Executor) domain.Issues {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validate
}

func (f *fakeExec) Start(_ context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, req)
	if f.startErr != nil {
		return nil, f.startErr
	}
	out := make([]executor.TargetResult, len(req.Targets))
	for i, t := range req.Targets {
		if f.result != nil {
			out[i] = f.result(req, t)
		} else {
			out[i] = executor.TargetResult{AgentID: t.AgentID, Started: true, ExternalRef: "job-" + t.AgentID.String()[:8]}
		}
		if out[i].Started && out[i].Error == "" {
			if f.installs == nil {
				f.installs = map[domain.AgentID]string{}
			}
			f.installs[t.AgentID] = req.ToVersion
		}
	}
	return out, nil
}

func (f *fakeExec) Cancel(_ context.Context, req executor.CancelRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, req)
	return nil
}

func (f *fakeExec) started() map[domain.AgentID]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[domain.AgentID]int{}
	for _, c := range f.startCalls {
		for _, t := range c.Targets {
			m[t.AgentID]++
		}
	}
	return m
}

func (f *fakeExec) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startCalls)
}

// ------------------------------------------------------------------- env ----

var (
	oldHash = domain.SHA256([]byte("config-old"))
	newHash = domain.SHA256([]byte("config-new"))
)

type env struct {
	t     *testing.T
	clk   *clock
	st    *memstore.Store
	bus   eventbus.Bus
	ctl   *recControl
	notif *recNotifier
	exec  *fakeExec
	cfg   config.RolloutsConfig
	reg   *prometheus.Registry
	eng   *Engine
	ctx   context.Context

	fleets    []domain.Fleet
	policies  []domain.RolloutPolicy
	executors []domain.Executor
	commit    string

	// agent simulation
	failConfig map[domain.AgentID]bool // reject the offered config
	offline    map[domain.AgentID]bool
	unhealthy  map[domain.AgentID]bool
	// staleHealth: the agent still reports the unhealthy state of its old process
	// (reported long before the update) until the entry is deleted.
	staleHealth map[domain.AgentID]bool
	// starting: the agent reports the start-up state of its new process
	// (fresh StatusStarting, unhealthy) like a real supervisor.
	starting  map[domain.AgentID]bool
	noApply   map[domain.AgentID]bool // never apply (silent)
	noInstall map[domain.AgentID]bool // updates are accepted but never installed
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		t: t, clk: newClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)),
		st: memstore.New(), bus: eventbus.NewMemory("test", 0), ctl: &recControl{}, notif: &recNotifier{},
		exec: &fakeExec{typ: domain.ExecutorUpdater}, reg: prometheus.NewRegistry(), ctx: context.Background(),
		cfg:        config.RolloutsConfig{TickInterval: 5 * time.Second, DefaultTimezone: "Europe/Berlin", AutoStart: true},
		failConfig: map[domain.AgentID]bool{}, offline: map[domain.AgentID]bool{}, unhealthy: map[domain.AgentID]bool{}, staleHealth: map[domain.AgentID]bool{}, starting: map[domain.AgentID]bool{},
		noApply: map[domain.AgentID]bool{}, noInstall: map[domain.AgentID]bool{}, commit: "commit-1",
	}
	e.st.Now = e.clk.Now
	t.Cleanup(func() { e.bus.Close() })
	e.eng = e.newEngine()
	return e
}

func (e *env) newEngine() *Engine {
	return NewEngine(e.cfg, Deps{
		Store: e.st, Bus: e.bus, Control: e.ctl,
		Executors: executor.MapRegistry{domain.ExecutorUpdater: e.exec, domain.ExecutorWebhook: e.exec, domain.ExecutorAWX: e.exec, domain.ExecutorManual: e.exec},
		Notifier:  e.notif, Audit: auditAdapter{e.st}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registerer: prometheus.NewRegistry(), Now: e.clk.Now,
	})
}

type auditAdapter struct{ st *memstore.Store }

func (a auditAdapter) Record(ctx context.Context, ev domain.AuditEvent) {
	_ = a.st.Audit().Append(ctx, ev)
}

// publishSnapshot stores the snapshot built from the registered objects.
func (e *env) publishSnapshot() {
	e.t.Helper()
	snap := domain.GitOpsSnapshot{Commit: e.commit, Fleets: e.fleets, RolloutPolicies: e.policies, Executors: e.executors}
	_, err := e.st.GitOps().ApplySnapshot(e.ctx, snap, domain.GitOpsSync{Commit: &domain.GitCommit{SHA: e.commit}})
	require.NoError(e.t, err)
}

// configFleet registers fleet "f" (config rollouts, default executor none).
func (e *env) addFleet(name string, mut ...func(f *domain.Fleet)) *domain.Fleet {
	f := domain.Fleet{
		ObjectMeta: domain.ObjectMeta{Name: name},
		FleetSpec: domain.FleetSpec{
			Blueprint: "bp", Collector: domain.CollectorSpec{}, Update: domain.UpdateSpec{Executor: domain.ExecutorNone},
			Rollout: domain.FleetRolloutSpec{ConfigPolicy: "p", VersionPolicy: "p"},
		},
	}
	for _, m := range mut {
		m(&f)
	}
	e.fleets = append(e.fleets, f)
	return &e.fleets[len(e.fleets)-1]
}

func (e *env) setPolicy(name string, spec domain.RolloutPolicySpec) {
	for i := range e.policies {
		if e.policies[i].Name == name {
			e.policies[i].RolloutPolicySpec = spec
			return
		}
	}
	e.policies = append(e.policies, domain.RolloutPolicy{ObjectMeta: domain.ObjectMeta{Name: name}, RolloutPolicySpec: spec})
}

// agentOpt customizes a test agent.
type agentOpt func(i int, a *domain.Agent)

// addAgents creates n collector agents of a fleet whose assigned config
// differs from the desired one (a pending config change) and whose version is
// 0.161.0.
func (e *env) addAgents(n int, fleet string, opts ...agentOpt) []domain.AgentID {
	ids := make([]domain.AgentID, n)
	start := len(e.allAgentIDs())
	for i := 0; i < n; i++ {
		idx := start + i
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", idx))
		a := domain.Agent{
			ID: id, Kind: domain.AgentKindCollector, ServiceName: "otelcol-contrib", Version: "0.161.0",
			Hostname: fmt.Sprintf("host-%04d", idx), OSType: "linux", Arch: "amd64",
			IdentifyingAttributes: map[string]string{}, Labels: map[string]string{},
			NonIdentifyingAttributes: map[string]string{"site.timezone": "Europe/Berlin", "site.id": fmt.Sprintf("%04d", idx)},
			Connection:               domain.AgentConnection{Status: domain.ConnectionOnline},
			Health:                   &domain.AgentHealth{Healthy: true},
			RemoteConfig:             domain.RemoteConfigState{Status: domain.RemoteConfigApplied, LastHash: oldHash},
			Assignment: domain.AgentAssignment{
				FleetName: fleet, AssignedConfigHash: newHash, DesiredConfigHash: oldHash, TargetVersion: "0.162.0",
			},
		}
		for _, o := range opts {
			o(i, &a)
		}
		e.st.PutAgent(a)
		ids[i] = id
	}
	return ids
}

func (e *env) allAgentIDs() []domain.AgentID {
	all, _ := e.st.Agents().ListAll(e.ctx, store.AgentFilter{})
	ids := make([]domain.AgentID, len(all))
	for i, a := range all {
		ids[i] = a.ID
	}
	return ids
}

func pilots(idxs ...int) agentOpt {
	set := map[int]bool{}
	for _, i := range idxs {
		set[i] = true
	}
	return func(i int, a *domain.Agent) {
		if set[i] {
			a.Labels["pilot"] = "true"
		}
	}
}

// plan runs the planner.
func (e *env) plan() {
	e.t.Helper()
	e.agentsReact() // agents report their initial state (offline/online) before planning
	require.NoError(e.t, e.eng.Plan(e.ctx))
}

// agentsReact simulates the OpAMP layer and the collectors: they apply (or
// reject) the offered config and report health/connectivity.
func (e *env) agentsReact() {
	all, _ := e.st.Agents().ListAll(e.ctx, store.AgentFilter{})
	now := e.clk.Now()
	for _, a := range all {
		id := a.ID
		e.st.UpdateAgent(id, func(ag *domain.Agent) {
			if e.offline[id] {
				ag.Connection.Status = domain.ConnectionOffline
				if ag.Connection.DisconnectedAt == nil {
					t := now
					ag.Connection.DisconnectedAt = &t
				}
				return
			}
			ag.Connection.Status = domain.ConnectionOnline
			ag.Connection.DisconnectedAt = nil
			seen := now
			ag.Connection.LastSeenAt = &seen
			if ag.Kind == domain.AgentKindUpdater {
				return // an updater reports what the test says (steps, package status)
			}
			ag.Health = &domain.AgentHealth{Healthy: !e.unhealthy[id], LastError: map[bool]string{true: "pipeline broken"}[e.unhealthy[id]]}
			if e.staleHealth[id] {
				ag.Health = &domain.AgentHealth{Healthy: false, Status: "StatusStopping", UpdatedAt: now.Add(-time.Hour)}
			}
			if e.starting[id] {
				ag.Health = &domain.AgentHealth{Healthy: false, Status: "StatusStarting", UpdatedAt: now}
			}
			if v, ok := e.exec.installed(id); ok && !e.noInstall[id] {
				ag.Version = v
			}
			if e.noApply[id] {
				return
			}
			d := ag.Assignment.DesiredConfigHash
			if d.IsZero() || (ag.RemoteConfig.LastHash.Equal(d) && ag.RemoteConfig.Status != domain.RemoteConfigFailed) {
				return
			}
			if ag.Assignment.FailedConfigHash.Equal(d) {
				return
			}
			t := now
			if e.failConfig[id] && !d.Equal(oldHash) { // rejects every new config but accepts the old one
				ag.RemoteConfig = domain.RemoteConfigState{Status: domain.RemoteConfigFailed, LastHash: d, ErrorMessage: "invalid pipeline", UpdatedAt: &t}
				ag.Assignment.FailedConfigHash = d
				return
			}
			ag.RemoteConfig = domain.RemoteConfigState{Status: domain.RemoteConfigApplied, LastHash: d, UpdatedAt: &t}
		})
	}
}

// step is one engine tick followed by the agents' reaction and 5 seconds of
// time.
func (e *env) step() {
	e.t.Helper()
	require.NoError(e.t, e.eng.Tick(e.ctx))
	e.agentsReact()
	e.clk.Advance(5 * time.Second)
}

// steps runs n steps.
func (e *env) steps(n int) {
	for i := 0; i < n; i++ {
		e.step()
	}
}

// runUntil steps until cond holds (at most max steps).
func (e *env) runUntil(max int, cond func() bool) bool {
	e.t.Helper()
	for i := 0; i < max; i++ {
		if cond() {
			return true
		}
		e.step()
	}
	return cond()
}

func (e *env) rollout(id uuid.UUID) *domain.Rollout {
	e.t.Helper()
	r, err := e.st.Rollouts().Get(e.ctx, id)
	require.NoError(e.t, err)
	return r
}

// rollouts returns all rollouts oldest first.
func (e *env) rollouts() []domain.Rollout {
	e.t.Helper()
	list, _, err := e.st.Rollouts().List(e.ctx, store.RolloutFilter{})
	require.NoError(e.t, err)
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

// only returns the single rollout of the test.
func (e *env) only() domain.Rollout {
	e.t.Helper()
	rs := e.rollouts()
	require.Len(e.t, rs, 1, "expected exactly one rollout, got %d", len(rs))
	return rs[0]
}

func (e *env) targets(id uuid.UUID) []domain.RolloutTarget {
	e.t.Helper()
	ts, _, err := e.st.Rollouts().ListTargets(e.ctx, id, store.TargetFilter{})
	require.NoError(e.t, err)
	return ts
}

func (e *env) targetStatus(id uuid.UUID) map[domain.TargetStatus]int {
	m := map[domain.TargetStatus]int{}
	for _, t := range e.targets(id) {
		m[t.Status]++
	}
	return m
}

func (e *env) targetOf(id uuid.UUID, agent domain.AgentID) domain.RolloutTarget {
	e.t.Helper()
	for _, t := range e.targets(id) {
		if t.AgentID == agent {
			return t
		}
	}
	e.t.Fatalf("agent %s is not a target of rollout %s", agent, id)
	return domain.RolloutTarget{}
}

func (e *env) auditActions() []string {
	var out []string
	for _, ev := range e.st.AuditEvents() {
		out = append(out, ev.Action)
	}
	return out
}

func (e *env) inFlight(id uuid.UUID) int {
	n := 0
	for _, t := range e.targets(id) {
		if t.Status == domain.TargetInProgress || t.Status == domain.TargetVerifying {
			n++
		}
	}
	return n
}

func sortedIDs(ids []domain.AgentID) []domain.AgentID {
	out := append([]domain.AgentID(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// simple policy spec helpers

func spec(waves ...domain.WaveSpec) domain.RolloutPolicySpec {
	return domain.RolloutPolicySpec{Waves: waves, HealthWait: domain.Duration(30 * time.Second), ApplyTimeout: domain.Duration(5 * time.Minute)}
}

func allWave() domain.WaveSpec { return domain.WaveSpec{Name: "all", Percent: 100} }

// metricValue reads a gauge/counter of the engine's own registry.
func metricValue(t *testing.T, eng *Engine, name string, labels map[string]string) float64 {
	t.Helper()
	var c prometheus.Collector
	switch name {
	case "myriarch_rollouts_active":
		c = eng.met.active
	case "myriarch_rollout_targets":
		c = eng.met.targets
	case "myriarch_rollout_transitions_total":
		c = eng.met.transitions
	default:
		t.Fatalf("unknown metric %s", name)
	}
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := true
			for _, lp := range m.GetLabel() {
				if want, ok := labels[lp.GetName()]; ok && want != lp.GetValue() {
					match = false
				}
			}
			if match {
				if m.GetCounter() != nil {
					return m.GetCounter().GetValue()
				}
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}

func (f *fakeExec) installed(id domain.AgentID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.installs[id]
	return v, ok
}

// inSync makes an agent's desired config equal to the assigned one (no pending config change).
func inSync() agentOpt {
	return func(i int, a *domain.Agent) {
		a.Assignment.DesiredConfigHash = a.Assignment.AssignedConfigHash
		a.RemoteConfig.LastHash = a.Assignment.AssignedConfigHash
	}
}
