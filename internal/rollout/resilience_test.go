// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

// ------------------------------------------------------------- maintenance ----

func TestMaintenanceWindowsPerAgentTimeZone(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaintenanceWindows = []domain.MaintenanceWindow{{Start: "01:00", End: "05:00"}} // "agent" local time
	s.HealthWait = domain.Duration(10 * time.Second)
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(4, "f", func(i int, a *domain.Agent) {
		if i >= 2 {
			a.NonIdentifyingAttributes["site.timezone"] = "America/New_York"
		}
		if i == 3 {
			delete(a.NonIdentifyingAttributes, "site.timezone") // falls back to Europe/Berlin
		}
	})
	e.publishSnapshot()
	e.plan()
	r := e.only()

	// 2026-10-06 12:00Z: 14:00 Berlin, 08:00 New York: everybody waits.
	e.steps(3)
	st := e.targetStatus(r.ID)
	assert.Equal(t, 4, st[domain.TargetWaitingWindow], "%v", st)
	for _, id := range ids {
		assert.Equal(t, oldHash, e.st.Agent(id).Assignment.DesiredConfigHash)
	}
	assert.Empty(t, e.st.Events(ids[0]), "waiting_window flips are not noise in the agent timeline")

	// 23:30Z = 01:30 Berlin (CEST): the Berlin agents (and the one without zone) start, New York (19:30) waits.
	e.clk.Set(time.Date(2026, 10, 6, 23, 30, 0, 0, time.UTC))
	e.steps(3)
	assert.Equal(t, newHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash)
	assert.Equal(t, newHash, e.st.Agent(ids[1]).Assignment.DesiredConfigHash)
	assert.Equal(t, newHash, e.st.Agent(ids[3]).Assignment.DesiredConfigHash, "missing timezone attribute: default timezone of the policy/myriarchd")
	assert.Equal(t, oldHash, e.st.Agent(ids[2]).Assignment.DesiredConfigHash)
	assert.Equal(t, domain.TargetWaitingWindow, e.targetOf(r.ID, ids[2]).Status)

	// Running targets are never interrupted by a closing window; time passes beyond 05:00 Berlin.
	e.clk.Set(time.Date(2026, 10, 7, 3, 5, 0, 0, time.UTC)) // 05:05 Berlin
	e.steps(5)
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, ids[0]).Status)
	assert.Equal(t, domain.TargetWaitingWindow, e.targetOf(r.ID, ids[2]).Status)

	// 05:30Z = 01:30 New York (EDT): now they start.
	e.clk.Set(time.Date(2026, 10, 7, 5, 30, 0, 0, time.UTC))
	require.True(t, e.runUntil(30, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, newHash, e.st.Agent(ids[2]).Assignment.DesiredConfigHash)
}

func TestMaintenanceWindowOnDaylightSavingDay(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaintenanceWindows = []domain.MaintenanceWindow{{Start: "02:30", End: "04:00", Timezone: "Europe/Berlin"}}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(1, "f")
	e.publishSnapshot()
	e.clk.Set(time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC)) // 01:30 CET, before the change
	e.plan()
	r := e.only()
	e.steps(2)
	assert.Equal(t, domain.TargetWaitingWindow, e.targetOf(r.ID, ids[0]).Status)
	e.clk.Set(time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC)) // 01:59 CET
	e.steps(1)
	assert.Equal(t, domain.TargetWaitingWindow, e.targetOf(r.ID, ids[0]).Status)
	e.clk.Set(time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)) // 03:30 CEST: inside 02:30(03:30)-04:00
	e.steps(1)
	assert.Equal(t, domain.TargetInProgress, e.targetOf(r.ID, ids[0]).Status)
}

func TestMaintenanceWindowAndMaxConcurrentTogether(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 2
	s.HealthWait = domain.Duration(time.Hour)
	s.MaintenanceWindows = []domain.MaintenanceWindow{{Start: "00:00", End: "00:00", Timezone: "UTC", Days: []string{"Tue"}}} // 2026-10-06 is a Tuesday
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(6, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	st := e.targetStatus(r.ID)
	assert.Equal(t, 2, st[domain.TargetVerifying], "%v", st)
	assert.Equal(t, 4, st[domain.TargetPending], "window is open, concurrency is exhausted: pending, not waiting_window: %v", st)
}

// ----------------------------------------------------------------- failover ----

func TestLeaderChangeInTheMiddleOfARollout(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", carefulSpec())
	e.addFleet("f")
	ids := e.addAgents(100, "f", pilots(3, 77))
	e.publishSnapshot()
	e.plan()
	r := e.only()
	engines := []*Engine{e.eng, e.newEngine(), e.newEngine()}

	approved := false
	step := 0
	require.True(t, e.runUntil(900, func() bool {
		step++
		// a different "leader" every tick (fresh instances share nothing but the database)
		e.eng = engines[step%len(engines)]
		cur := e.rollout(r.ID)
		if cur.Status == domain.RolloutWaitingApproval && !approved {
			approved = true
			// the operator acts on yet another replica
			other := e.newEngine()
			_, err := other.Action(e.ctx, r.ID, domain.ActionApprove, 1, "", operator)
			require.NoError(t, err)
		}
		return cur.Status == domain.RolloutCompleted
	}))
	assert.True(t, approved)
	done := e.rollout(r.ID)
	assert.Equal(t, 100, done.Counts.Succeeded)
	for id, n := range e.ctl.pushed() {
		assert.Equal(t, 1, n, "agent %s was pushed once despite %d leader changes", id, step)
	}
	for _, id := range ids {
		assert.Equal(t, newHash, e.st.Agent(id).Assignment.DesiredConfigHash)
	}
	events := e.notif.events()
	count := func(ev notify.EventType) int {
		n := 0
		for _, x := range events {
			if x == ev {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, count(notify.EventRolloutStarted))
	assert.Equal(t, 1, count(notify.EventRolloutWaitingApprove), "no duplicate notification after leader changes")
	assert.Equal(t, 1, count(notify.EventRolloutCompleted))
}

func TestLeaderChangeDuringVersionRolloutStartsEachAgentOnce(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "a", Percent: 30}, allWave())
	s.MaxConcurrent = 4
	e.setPolicy("p", s)
	versionFleet(e)
	e.addAgents(20, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	engines := []*Engine{e.eng, e.newEngine(), e.newEngine()}
	i := 0
	require.True(t, e.runUntil(600, func() bool {
		i++
		e.eng = engines[i%3]
		return e.rollout(r.ID).Status == domain.RolloutCompleted
	}))
	for id, n := range e.exec.started() {
		assert.Equal(t, 1, n, "agent %s", id)
	}
	assert.Len(t, e.exec.started(), 20)
}

// faultStore injects failures for resilience tests.
type faultStore struct {
	store.Store
	failSetDesired    atomic.Int32
	failUpdateTargets atomic.Int32
}

func (f *faultStore) Agents() store.AgentStore {
	return &faultAgents{AgentStore: f.Store.Agents(), f: f}
}
func (f *faultStore) Rollouts() store.RolloutStore {
	return &faultRollouts{RolloutStore: f.Store.Rollouts(), f: f}
}

type faultAgents struct {
	store.AgentStore
	f *faultStore
}

func (a *faultAgents) SetDesiredConfig(ctx context.Context, ids []domain.AgentID, h domain.Hash) error {
	if a.f.failSetDesired.Add(-1) >= 0 {
		return errors.New("database unavailable")
	}
	return a.AgentStore.SetDesiredConfig(ctx, ids, h)
}

type faultRollouts struct {
	store.RolloutStore
	f *faultStore
}

func (r *faultRollouts) UpdateTargets(ctx context.Context, ts []domain.RolloutTarget) ([]domain.AgentID, error) {
	if r.f.failUpdateTargets.Add(-1) >= 0 {
		return nil, errors.New("database unavailable")
	}
	return r.RolloutStore.UpdateTargets(ctx, ts)
}

func TestCrashBetweenClaimAndPushIsReconciled(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	e.addFleet("f")
	ids := e.addAgents(5, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()

	fs := &faultStore{Store: e.st}
	fs.failSetDesired.Store(1)
	broken := NewEngine(e.cfg, Deps{Store: fs, Bus: e.bus, Control: e.ctl, Executors: nil, Notifier: e.notif, Now: e.clk.Now})
	err := broken.Tick(e.ctx)
	require.Error(t, err, "the push failed")
	st := e.targetStatus(r.ID)
	assert.Equal(t, 5, st[domain.TargetInProgress], "targets were claimed: %v", st)
	assert.Equal(t, oldHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash, "but nothing was pushed")

	// a new leader reconciles: pushes the desired config for claimed targets
	e.agentsReact()
	e.step()
	assert.Equal(t, newHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash)
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 5, e.rollout(r.ID).Counts.Succeeded)
}

func TestFailedWriteAfterExecutorStartRetriesWithoutLosingTargets(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	e.addAgents(3, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	fs := &faultStore{Store: e.st}
	fs.failUpdateTargets.Store(2) // the claim and the retry in the final flush
	broken := NewEngine(e.cfg, Deps{Store: fs, Bus: e.bus, Control: e.ctl, Executors: e.eng.deps.Executors, Notifier: e.notif, Now: e.clk.Now})
	err := broken.Tick(e.ctx)
	require.Error(t, err)
	assert.Equal(t, 3, e.targetStatus(r.ID)[domain.TargetPending], "targets were not recorded as started")
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	for id, n := range e.exec.started() {
		assert.LessOrEqual(t, n, 2, "at-least-once: at most one repeated start per agent (%s)", id)
	}
	assert.Equal(t, 3, e.rollout(r.ID).Counts.Succeeded)
}

func TestTickWithCancelledContextDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	e.addFleet("f")
	ids := e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.eng.Tick(ctx)
	assert.Error(t, err)
	assert.Equal(t, 3, e.targetStatus(r.ID)[domain.TargetPending])
	assert.Equal(t, oldHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash)
	assert.Error(t, e.eng.Plan(ctx))
}

// ----------------------------------------------------------------- run loop ----

func TestRunLoopTicksPlansAndStopsOnCancel(t *testing.T) {
	e := newEnv(t)
	e.cfg.TickInterval = 20 * time.Millisecond
	s := spec(allWave())
	s.HealthWait = domain.Duration(30 * time.Millisecond)
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(5, "f")
	e.publishSnapshot()
	// real time for this test
	eng := NewEngine(e.cfg, Deps{
		Store: e.st, Bus: e.bus, Control: e.ctl, Executors: nil, Notifier: e.notif, Audit: auditAdapter{e.st},
	})
	e.st.Now = func() time.Time { return time.Now().UTC() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()

	// agents react in the background
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				e.clk.Set(time.Now())
				e.agentsReact()
			}
		}
	}()
	defer close(stop)

	require.Eventually(t, func() bool {
		list, _, _ := e.st.Rollouts().List(e.ctx, store.RolloutFilter{})
		return len(list) == 1 && list[0].Status == domain.RolloutCompleted
	}, 10*time.Second, 20*time.Millisecond, "Run plans (initial plan) and ticks rollouts to completion")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunLoopWakesOnEvents(t *testing.T) {
	e := newEnv(t)
	e.cfg.TickInterval = time.Hour // only events (and the initial run) drive this engine
	s := spec(allWave())
	s.HealthWait = domain.Duration(10 * time.Millisecond)
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(2, "f")
	e.publishSnapshot()
	e.st.Now = func() time.Time { return time.Now().UTC() }
	eng := NewEngine(e.cfg, Deps{Store: e.st, Bus: e.bus, Control: e.ctl, Notifier: e.notif})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()
	// initial plan + tick create and start the rollout
	require.Eventually(t, func() bool {
		list, _, _ := e.st.Rollouts().List(e.ctx, store.RolloutFilter{})
		return len(list) == 1 && list[0].Counts.InProgress == 2
	}, 5*time.Second, 10*time.Millisecond)
	// An agent state change wakes the engine (throttled to once per second).
	e.agentsReact()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, e.bus.Publish(ctx, eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: e.allAgentIDs()[0], Changes: []string{"remote_config"}}))
	require.Eventually(t, func() bool {
		list, _, _ := e.st.Rollouts().List(e.ctx, store.RolloutFilter{})
		return len(list) == 1 && list[0].Status == domain.RolloutCompleted
	}, 5*time.Second, 20*time.Millisecond, "events trigger early ticks")
}

// A rollout of a few thousand agents completes in reasonable time (all in memory).
func TestLargeRolloutScales(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	e := newEnv(t)
	s := spec(
		domain.WaveSpec{Name: "pilot", Percent: 1},
		domain.WaveSpec{Name: "ten", Percent: 10},
		allWave(),
	)
	s.MaxConcurrent = 500
	s.HealthWait = domain.Duration(10 * time.Second)
	s.FailureThreshold = domain.FailureThreshold{MaxFailedPercent: 50}
	e.setPolicy("p", s)
	e.addFleet("f")
	n := 2000
	e.addAgents(n, "f")
	e.publishSnapshot()
	start := time.Now()
	e.plan()
	r := e.only()
	require.Equal(t, n, r.Counts.Total)
	require.True(t, e.runUntil(500, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, n, e.rollout(r.ID).Counts.Succeeded)
	t.Logf("%d agents rolled out in %s (%d ticks of 5s fake time)", n, time.Since(start), int(e.clk.Now().Sub(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))/(5*time.Second)))
	assert.Less(t, time.Since(start), 60*time.Second)
}
