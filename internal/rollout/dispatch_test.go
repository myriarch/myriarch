// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
)

// slowExec is an executor whose calls block until released (or until their
// context ends): a hanging webhook/AWX receiver.
type slowExec struct {
	typ     domain.ExecutorType
	release chan struct{} // close to let calls succeed
	mu      sync.Mutex
	starts  int
	cancels []executor.CancelRequest
	// cancelBlock makes Cancel hang until its context ends.
	cancelBlock bool
}

func newSlowExec(typ domain.ExecutorType) *slowExec {
	return &slowExec{typ: typ, release: make(chan struct{})}
}

func (s *slowExec) Type() domain.ExecutorType { return s.typ }
func (s *slowExec) Validate(domain.UpdateSpec, *domain.Executor) domain.Issues {
	return nil
}

func (s *slowExec) Start(ctx context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	s.mu.Lock()
	s.starts++
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([]executor.TargetResult, len(req.Targets))
	for i, t := range req.Targets {
		out[i] = executor.TargetResult{AgentID: t.AgentID, Started: true, ExternalRef: "slow-" + t.AgentID.String()[:8]}
	}
	return out, nil
}

func (s *slowExec) Cancel(ctx context.Context, req executor.CancelRequest) error {
	s.mu.Lock()
	s.cancels = append(s.cancels, req)
	block := s.cancelBlock
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (s *slowExec) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

// waitStarts waits until the (background) call goroutines have entered Start n times.
func (s *slowExec) waitStarts(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return s.startCount() >= n }, 5*time.Second, 2*time.Millisecond)
}

func (s *slowExec) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cancels)
}

// slowEnv: fleet "slow" updates through the (hanging) webhook executor, fleet
// "fast" through the updater executor.
func slowEnv(t *testing.T, mut ...func(*env)) (*env, *slowExec) {
	t.Helper()
	e := newEnv(t)
	for _, m := range mut {
		m(e)
	}
	slow := newSlowExec(domain.ExecutorWebhook)
	e.eng = NewEngine(e.cfg, Deps{
		Store: e.st, Bus: e.bus, Control: e.ctl,
		Executors: executor.MapRegistry{domain.ExecutorUpdater: e.exec, domain.ExecutorWebhook: slow},
		Notifier:  e.notif, Audit: auditAdapter{e.st}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registerer: prometheus.NewRegistry(), Now: e.clk.Now,
	})
	e.eng.execWait = 40 * time.Millisecond
	e.eng.tickWait = 200 * time.Millisecond
	e.setPolicy("p", spec(allWave()))
	e.addFleet("slow", func(f *domain.Fleet) {
		f.Collector = domain.CollectorSpec{Version: "0.162.0"}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorWebhook}
	})
	e.addFleet("fast", func(f *domain.Fleet) {
		f.Collector = domain.CollectorSpec{Version: "0.162.0"}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorUpdater}
	})
	return e, slow
}

func rolloutOf(t *testing.T, e *env, fleet string) domain.Rollout {
	t.Helper()
	for _, r := range e.rollouts() {
		if r.FleetName == fleet {
			return r
		}
	}
	t.Fatalf("no rollout for fleet %s", fleet)
	return domain.Rollout{}
}

func TestSlowExecutorDoesNotStallTickPlannerOrOtherRollouts(t *testing.T) {
	e, slow := slowEnv(t)
	e.addAgents(3, "slow", inSync())
	e.addAgents(3, "fast", inSync())
	e.publishSnapshot()
	e.plan()
	rs, rf := rolloutOf(t, e, "slow"), rolloutOf(t, e, "fast")

	begin := time.Now()
	require.NoError(t, e.eng.Tick(e.ctx))
	assert.Less(t, time.Since(begin), 2*time.Second, "a hanging executor must not hold the tick")
	slow.waitStarts(t, 1)
	assert.Equal(t, 3, e.targetStatus(rf.ID)[domain.TargetInProgress], "the other rollout started in the same tick")
	assert.Equal(t, 3, e.targetStatus(rs.ID)[domain.TargetPending], "targets of the hanging call are untouched (still pending in the database)")
	assert.True(t, e.eng.outstanding(rs.ID))

	// further ticks do not pile up calls, and the planner is not blocked
	for i := 0; i < 3; i++ {
		begin = time.Now()
		require.NoError(t, e.eng.Tick(e.ctx))
		assert.Less(t, time.Since(begin), 2*time.Second)
	}
	assert.Equal(t, 1, slow.startCount(), "no second call while the first is outstanding")
	begin = time.Now()
	require.NoError(t, e.eng.Plan(e.ctx))
	assert.Less(t, time.Since(begin), 2*time.Second, "the planner is not blocked by the hanging call")

	// the receiver recovers: the result is applied by a later tick without a new call
	close(slow.release)
	require.Eventually(t, func() bool { return !e.eng.outstanding(rs.ID) }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, e.eng.Tick(e.ctx))
	st := e.targetStatus(rs.ID)
	assert.Equal(t, 3, st[domain.TargetInProgress], "%v", st)
	for _, tg := range e.targets(rs.ID) {
		assert.Contains(t, tg.ExecutorRef, "slow-")
	}
	assert.Equal(t, 1, slow.startCount(), "idempotency: exactly one call per batch")
}

func TestHangingExecutorCallIsBoundedAndFailsTargets(t *testing.T) {
	e, slow := slowEnv(t, func(e *env) { e.cfg.ExecutorTimeout = 150 * time.Millisecond })
	e.addAgents(2, "slow", inSync())
	e.publishSnapshot()
	e.plan()
	rs := rolloutOf(t, e, "slow")

	require.NoError(t, e.eng.Tick(e.ctx)) // call starts, tick does not wait for it
	slow.waitStarts(t, 1)
	require.Eventually(t, func() bool { return !e.eng.outstanding(rs.ID) }, 5*time.Second, 5*time.Millisecond, "the call is cut off by executorTimeout")
	require.NoError(t, e.eng.Tick(e.ctx))

	r := e.rollout(rs.ID)
	assert.Equal(t, domain.RolloutFailed, r.Status, "every target failed at the executor: %s", r.StatusReason)
	for _, tg := range e.targets(rs.ID) {
		assert.Equal(t, domain.TargetFailed, tg.Status)
		assert.Contains(t, tg.LastError, "executor call failed")
	}
}

func TestSlowExecutorBudgetPerTickIsBounded(t *testing.T) {
	e, slow := slowEnv(t)
	e.eng.execWait = 300 * time.Millisecond
	e.eng.tickWait = 350 * time.Millisecond
	// several rollouts that all hang: the tick spends at most tickWait waiting in total
	for i := 0; i < 5; i++ {
		name := "slow" + string(rune('a'+i))
		e.addFleet(name, func(f *domain.Fleet) {
			f.Collector = domain.CollectorSpec{Version: "0.162.0"}
			f.Update = domain.UpdateSpec{Executor: domain.ExecutorWebhook}
		})
		e.addAgents(1, name, inSync())
	}
	e.publishSnapshot()
	e.plan()
	begin := time.Now()
	require.NoError(t, e.eng.Tick(e.ctx))
	assert.Less(t, time.Since(begin), 1500*time.Millisecond, "5 hanging calls x 300ms would exceed the 350ms tick budget")
	require.Eventually(t, func() bool { return slow.startCount() >= 5 }, 5*time.Second, 5*time.Millisecond, "every rollout still gets its call, just without waiting for it")
	close(slow.release)
}

func TestAbortWhileExecutorCallRunsWithdrawsStartedUpdates(t *testing.T) {
	e, slow := slowEnv(t)
	e.addAgents(2, "slow", inSync())
	e.publishSnapshot()
	e.plan()
	rs := rolloutOf(t, e, "slow")
	require.NoError(t, e.eng.Tick(e.ctx))
	require.True(t, e.eng.outstanding(rs.ID))

	_, err := e.eng.Action(e.ctx, rs.ID, domain.ActionAbort, 0, "stop", authz.System)
	require.NoError(t, err)
	close(slow.release)
	require.Eventually(t, func() bool { return !e.eng.outstanding(rs.ID) }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, e.eng.Tick(e.ctx)) // sweeps the finished call of the aborted rollout

	require.Eventually(t, func() bool { return slow.cancelCount() >= 2 }, 5*time.Second, 5*time.Millisecond,
		"abort cancels once, the sweep withdraws what the late call started")
	last := slow.cancels[len(slow.cancels)-1]
	assert.Len(t, last.AgentIDs, 2)
	for _, tg := range e.targets(rs.ID) {
		assert.Equal(t, domain.TargetCancelled, tg.Status)
	}
}

func TestExecutorCancelDoesNotBlockOperatorAction(t *testing.T) {
	e, slow := slowEnv(t)
	e.addAgents(2, "slow", inSync())
	e.publishSnapshot()
	e.plan()
	rs := rolloutOf(t, e, "slow")
	close(slow.release)
	require.NoError(t, e.eng.Tick(e.ctx))
	require.Equal(t, 2, e.targetStatus(rs.ID)[domain.TargetInProgress])

	slow.mu.Lock()
	slow.cancelBlock = true
	slow.mu.Unlock()
	begin := time.Now()
	_, err := e.eng.Action(e.ctx, rs.ID, domain.ActionAbort, 0, "stop", authz.System)
	require.NoError(t, err)
	assert.Less(t, time.Since(begin), 2*time.Second, "a hanging Cancel is not waited for")
	assert.Equal(t, domain.RolloutAborted, e.rollout(rs.ID).Status)
}

func TestSlowRollbackCallIsCollectedOnLaterTick(t *testing.T) {
	e, slow := slowEnv(t)
	e.addAgents(2, "slow", inSync())
	e.publishSnapshot()
	e.plan()
	rs := rolloutOf(t, e, "slow")
	close(slow.release) // the start call succeeds
	require.NoError(t, e.eng.Tick(e.ctx))
	require.Equal(t, 2, e.targetStatus(rs.ID)[domain.TargetInProgress])
	for _, id := range e.allAgentIDs() { // the updates were installed
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Version = "0.162.0" })
	}
	before := slow.startCount()

	// from here on calls hang again
	slow.mu.Lock()
	slow.release = make(chan struct{})
	slow.mu.Unlock()
	_, err := e.eng.Action(e.ctx, rs.ID, domain.ActionRollback, 0, "bad", authz.System)
	require.NoError(t, err)
	begin := time.Now()
	require.NoError(t, e.eng.Tick(e.ctx))
	assert.Less(t, time.Since(begin), 2*time.Second)
	slow.waitStarts(t, before+1)
	assert.Equal(t, before+1, slow.startCount(), "one rollback call is running")
	require.NoError(t, e.eng.Tick(e.ctx))
	assert.Equal(t, before+1, slow.startCount(), "no duplicate rollback call while it is outstanding")
}

// A call launched under a leadership term that ended while it ran must not
// fail the targets in the next term: the targets stay pending and are
// dispatched again.
func TestStaleDispatchOfEndedLeadershipTermDoesNotFailTargets(t *testing.T) {
	e, slow := slowEnv(t)
	e.addAgents(3, "slow", inSync())
	e.publishSnapshot()
	e.plan()
	rs := rolloutOf(t, e, "slow")

	lctx, lose := context.WithCancel(e.ctx)
	_ = e.eng.Tick(lctx) // the call hangs in the background
	slow.waitStarts(t, 1)
	lose() // leadership lost: the call ends with context.Canceled
	require.Eventually(t, func() bool { return !e.eng.outstanding(rs.ID) }, 5*time.Second, 5*time.Millisecond)

	// next term: same engine, new context
	require.NoError(t, e.eng.Tick(e.ctx))
	st := e.targetStatus(rs.ID)
	assert.Zero(t, st[domain.TargetFailed], "%v", st)
	assert.Equal(t, 3, st[domain.TargetPending], "%v", st)
	assert.Equal(t, domain.RolloutRunning, e.rollout(rs.ID).Status)
	assert.Zero(t, slow.cancelCount(), "nothing of the stale term is withdrawn")

	// the new term calls the executor again and applies its result
	slow.waitStarts(t, 2)
	close(slow.release)
	require.Eventually(t, func() bool { return !e.eng.outstanding(rs.ID) }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, e.eng.Tick(e.ctx))
	assert.Equal(t, 3, e.targetStatus(rs.ID)[domain.TargetInProgress])
}
