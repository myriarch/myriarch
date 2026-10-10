// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/store"
)

func TestAutomaticRollbackOnFailure(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "first", Percent: 50}, allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
	s.OnFailure = domain.FailureRollback
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	// determine who is in the first wave and make one of them fail
	var firstWave []domain.AgentID
	for _, tg := range e.targets(r.ID) {
		if tg.Wave == 0 {
			firstWave = append(firstWave, tg.AgentID)
		}
	}
	require.Len(t, firstWave, 5)
	e.failConfig[firstWave[0]] = true

	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutRolledBack }), "status %s", e.rollout(r.ID).Status)
	done := e.rollout(r.ID)
	assert.NotNil(t, done.FinishedAt)
	assert.Contains(t, done.StatusReason, "rolled back")
	// All touched targets are rolled back, never started targets were cancelled.
	st := e.targetStatus(r.ID)
	assert.Equal(t, 5, st[domain.TargetRolledBack], "%v", st)
	assert.Equal(t, 5, st[domain.TargetCancelled], "%v", st)
	for _, id := range ids {
		assert.Equal(t, oldHash, e.st.Agent(id).Assignment.DesiredConfigHash, "everyone is back on the old config")
	}
	assert.Equal(t, oldHash, e.st.Agent(firstWave[0]).RemoteConfig.LastHash)
	assert.Empty(t, e.st.Agent(firstWave[0]).Assignment.FailedConfigHash, "desired changed: failed marker cleared")
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutRolledBack}, e.notif.events())
	assert.Contains(t, e.auditActions(), "rollout.rollback")
	assert.Contains(t, e.auditActions(), "rollout.rolled_back")
	// The agents drift from Git (assigned != desired) until Git changes.
	ag := e.st.Agent(firstWave[1])
	assert.Equal(t, domain.ConfigSyncDrift, ag.ConfigSyncState())
	// ... and are not re-planned: neither the rolled_back targets nor those
	// cancelled by the rollback get the rejected goal again until Git changes.
	e.plan()
	assert.Len(t, e.rollouts(), 1, "a rollback is not undone by a new rollout of the same goal")
	e.steps(10)
	e.plan()
	assert.Len(t, e.rollouts(), 1, "no cascade of rollouts of the rejected config")
}

func TestManualRollbackWaitsForAgentsAndFailsAfterApplyTimeout(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.ApplyTimeout = domain.Duration(2 * time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(4, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	require.Equal(t, 4, e.targetStatus(r.ID)[domain.TargetVerifying])

	// One agent will not return to the old config (stuck).
	e.noApply[ids[0]] = true
	got, err := e.eng.Action(e.ctx, r.ID, domain.ActionRollback, 0, "bad idea", operator)
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutRollingBack, got.Status)
	assert.Contains(t, got.StatusReason, "bad idea")
	assert.Contains(t, got.StatusReason, "user:u-42")

	require.True(t, e.runUntil(80, func() bool { return e.rollout(r.ID).Status == domain.RolloutFailed }))
	done := e.rollout(r.ID)
	assert.Contains(t, done.StatusReason, "rollback did not complete")
	st := e.targetStatus(r.ID)
	assert.Equal(t, 3, st[domain.TargetRolledBack], "%v", st)
	assert.Equal(t, 1, st[domain.TargetVerifying], "%v: the stuck agent keeps its state", st)
	assert.Contains(t, e.notif.events(), notify.EventRolloutFailed)

	// failed rollouts only move by operator action; abort ends it
	_, err = e.eng.Action(e.ctx, r.ID, domain.ActionAbort, 0, "", operator)
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutAborted, e.rollout(r.ID).Status)
}

func TestSupersedeWhenGoalChanges(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "first", Percent: 20}, allWave())
	s.MaxConcurrent = 4
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(20, "f")
	e.publishSnapshot()
	e.plan()
	r1 := e.only()
	e.steps(8) // some succeeded, some in flight, most pending

	// New commit: the assignment service re-rendered everything (new assigned hash).
	hash2 := domain.SHA256([]byte("config-v2"))
	for _, id := range ids {
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Assignment.AssignedConfigHash = hash2 })
	}
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()

	rs := e.rollouts()
	require.Len(t, rs, 2)
	old := e.rollout(r1.ID)
	assert.Equal(t, domain.RolloutSuperseded, old.Status)
	assert.Contains(t, old.StatusReason, "superseded")
	assert.NotNil(t, old.FinishedAt)
	st := e.targetStatus(r1.ID)
	assert.Equal(t, 0, st[domain.TargetPending]+st[domain.TargetInProgress]+st[domain.TargetVerifying]+st[domain.TargetWaitingWindow], "open targets are cancelled: %v", st)
	assert.Greater(t, st[domain.TargetSucceeded], 0, "finished targets keep their result: %v", st)
	assert.Contains(t, e.auditActions(), "rollout.supersede")

	nw := rs[1]
	assert.Equal(t, "commit-2", nw.ToRef)
	assert.Equal(t, domain.RolloutRunning, nw.Status)
	assert.Equal(t, 20, nw.Counts.Total, "all agents: those already on commit-1 are again relevant (desired != new assigned)")
	for _, tg := range e.targets(nw.ID) {
		assert.Equal(t, hash2.String(), tg.ToRef)
	}
	// the new rollout completes and every agent ends up on the new config
	require.True(t, e.runUntil(400, func() bool { return e.rollout(nw.ID).Status == domain.RolloutCompleted }))
	for _, id := range ids {
		assert.Equal(t, hash2, e.st.Agent(id).Assignment.DesiredConfigHash)
	}
	// at most one active rollout per (fleet, kind) at all times: never more than one non-terminal
	active := 0
	for _, r := range e.rollouts() {
		if !r.Status.IsTerminal() {
			active++
		}
	}
	assert.Equal(t, 0, active)
}

func TestNoSupersedeWhenGoalsStillMatch(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 2
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	e.steps(3)
	e.plan()
	e.plan()
	assert.Len(t, e.rollouts(), 1)
}

func TestLateJoinersAreAddedToTheLastWave(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "first", Percent: 50}, allWave())
	s.MaxConcurrent = 2
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)

	// Two agents show up later (registered or re-assigned to the fleet).
	late := e.addAgents(2, "f")
	e.plan()
	assert.Len(t, e.rollouts(), 1, "no second rollout")
	tg := e.targetOf(r.ID, late[0])
	assert.Equal(t, 1, tg.Wave, "last wave")
	assert.Equal(t, domain.TargetPending, tg.Status)
	assert.Equal(t, newHash.String(), tg.ToRef)
	assert.Equal(t, oldHash.String(), tg.FromRef)
	assert.Equal(t, 12, e.rollout(r.ID).Counts.Total)
	// planning again does not duplicate them
	e.plan()
	assert.Equal(t, 12, e.rollout(r.ID).Counts.Total)

	require.True(t, e.runUntil(300, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 12, e.rollout(r.ID).Counts.Succeeded)
	for _, id := range late {
		assert.Equal(t, newHash, e.st.Agent(id).Assignment.DesiredConfigHash)
	}
}

func TestLateJoinerAfterLastWaveFinishedReopensIt(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(10 * time.Minute) // keep the rollout open for a while
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	late := e.addAgents(1, "f")
	e.plan()
	assert.Equal(t, 4, e.rollout(r.ID).Counts.Total)
	require.True(t, e.runUntil(300, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, newHash, e.st.Agent(late[0]).Assignment.DesiredConfigHash)
}

func TestExclusionsAfterFailureAndAbort(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(6, "f")
	e.publishSnapshot()
	e.plan()
	e.failConfig[ids[0]] = true
	r := e.only()
	require.True(t, e.runUntil(80, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	require.Equal(t, 1, e.rollout(r.ID).Counts.Failed)

	// The failed agent is relevant (desired new, assigned new?...): its desired equals the failed
	// new hash, so it is no longer different from assigned. Make it drift so that the planner sees it.
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) { a.Assignment.DesiredConfigHash = oldHash })
	e.plan()
	assert.Len(t, e.rollouts(), 1, "a failed change is not retried automatically: only a new commit or retry-failed starts it again")

	// New commit with another hash: the failed agent is included again.
	hash2 := domain.SHA256([]byte("config-v2"))
	for _, id := range ids {
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Assignment.AssignedConfigHash = hash2 })
	}
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()
	require.Len(t, e.rollouts(), 2)
	r2 := e.rollouts()[1]
	assert.Equal(t, 6, r2.Counts.Total)

	// Abort: cancelled targets in an aborted rollout are excluded from further planning.
	_, err := e.eng.Action(e.ctx, r2.ID, domain.ActionAbort, 0, "stop", operator)
	require.NoError(t, err)
	aborted := e.rollout(r2.ID)
	assert.Equal(t, domain.RolloutAborted, aborted.Status)
	st := e.targetStatus(r2.ID)
	assert.Equal(t, 6, st[domain.TargetCancelled], "%v", st)
	e.plan()
	assert.Len(t, e.rollouts(), 2, "an operator abort is respected")

	// But a newer commit starts again.
	hash3 := domain.SHA256([]byte("config-v3"))
	for _, id := range ids {
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Assignment.AssignedConfigHash = hash3 })
	}
	e.commit = "commit-3"
	e.publishSnapshot()
	e.plan()
	assert.Len(t, e.rollouts(), 3)
}

func TestSupersededTargetsDoNotExcludeAgents(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 1
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(4, "f")
	e.publishSnapshot()
	e.plan()
	e.steps(2)
	// supersede with the SAME goal impossible; first change the goal and back to the original one
	hash2 := domain.SHA256([]byte("config-v2"))
	for _, id := range ids {
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Assignment.AssignedConfigHash = hash2 })
	}
	e.plan() // supersedes rollout 1
	for _, id := range ids {
		e.st.UpdateAgent(id, func(a *domain.Agent) { a.Assignment.AssignedConfigHash = newHash })
	}
	e.plan() // supersedes rollout 2 and creates rollout 3 with the original goal again
	rs := e.rollouts()
	require.Len(t, rs, 3)
	assert.Equal(t, domain.RolloutSuperseded, rs[0].Status)
	assert.Equal(t, domain.RolloutSuperseded, rs[1].Status)
	assert.Equal(t, domain.RolloutRunning, rs[2].Status)
	assert.Equal(t, 3, rs[2].Counts.Total, "cancelled by superseded does not exclude (the one agent that already received the config is up to date)")
}

func TestCatchUpForSkippedAgentsOfCompletedConfigRollout(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.OfflineGracePeriod = domain.Duration(2 * time.Minute)
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(5, "f")
	e.offline[ids[0]] = true
	e.offline[ids[1]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	require.True(t, e.runUntil(120, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	done := e.rollout(r.ID)
	assert.Equal(t, 3, done.Counts.Succeeded)
	assert.Equal(t, 2, done.Counts.Skipped)
	tg := e.targetOf(r.ID, ids[0])
	assert.Equal(t, domain.TargetSkipped, tg.Status)
	assert.Contains(t, tg.LastError, "offline for more than 2m0s")
	assert.Equal(t, oldHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash)

	pushedBefore := e.ctl.pushed()[ids[0]]
	e.plan()
	assert.Len(t, e.rollouts(), 1, "catch-up creates no rollout")
	for _, i := range []int{0, 1} {
		assert.Equal(t, newHash, e.st.Agent(ids[i]).Assignment.DesiredConfigHash, "desired = assigned (already released)")
	}
	assert.Equal(t, pushedBefore+1, e.ctl.pushed()[ids[0]], "DesiredStateChanged is called")
	e.plan()
	assert.Equal(t, pushedBefore+1, e.ctl.pushed()[ids[0]], "idempotent")
	assert.Len(t, e.rollouts(), 1)
}

func TestOfflineTargetsWaitWithinGracePeriodAndStartWhenBack(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.OfflineGracePeriod = domain.Duration(10 * time.Minute)
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(3, "f")
	e.offline[ids[0]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(30) // 150 s
	assert.Equal(t, domain.TargetPending, e.targetOf(r.ID, ids[0]).Status, "waits for the offline agent")
	assert.Equal(t, domain.RolloutRunning, e.rollout(r.ID).Status, "the wave cannot finish yet")
	e.offline[ids[0]] = false
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 3, e.rollout(r.ID).Counts.Succeeded)
}

func TestOfflineGraceStartsAtWaveStartForAgentsThatWereAlreadyOffline(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "a", Percent: 50}, allWave())
	s.OfflineGracePeriod = domain.Duration(5 * time.Minute)
	s.HealthWait = domain.Duration(time.Second)
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(2, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	var second domain.AgentID
	for _, tg := range e.targets(r.ID) {
		if tg.Wave != 0 {
			second = tg.AgentID
		}
	}
	e.offline[second] = true
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).CurrentWave == 1 }))
	waveStart := *e.rollout(r.ID).Waves[1].StartedAt
	e.steps(50) // 250 s
	assert.Equal(t, domain.TargetPending, e.targetOf(r.ID, second).Status)
	require.True(t, e.runUntil(30, func() bool { return e.targetOf(r.ID, second).Status == domain.TargetSkipped }))
	assert.GreaterOrEqual(t, e.clk.Now().Sub(waveStart), 5*time.Minute)
}

func TestOfflineGraceStartsAtDisconnectForTargetsWaitingForTheirWindow(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.OfflineGracePeriod = domain.Duration(5 * time.Minute)
	s.MaintenanceWindows = []domain.MaintenanceWindow{{Start: "01:00", End: "02:00", Timezone: "UTC"}} // clock is 12:00 UTC
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(1, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(100) // 500 s online, outside the window
	assert.Equal(t, domain.TargetWaitingWindow, e.targetOf(r.ID, ids[0]).Status, "online targets wait for their window as long as needed")
	// now it disconnects: the grace period starts at the disconnect, not at the wave start
	e.offline[ids[0]] = true
	e.steps(40) // 200 s
	assert.Equal(t, domain.TargetWaitingWindow, e.targetOf(r.ID, ids[0]).Status)
	e.steps(30)
	assert.Equal(t, domain.TargetSkipped, e.targetOf(r.ID, ids[0]).Status)
}

func TestActionTransitions(t *testing.T) {
	type tc struct {
		from   domain.RolloutStatus
		action domain.RolloutAction
		to     domain.RolloutStatus // "" = precondition failed
	}
	cases := []tc{
		{domain.RolloutRunning, domain.ActionPause, domain.RolloutPaused},
		{domain.RolloutPaused, domain.ActionPause, ""},
		{domain.RolloutWaitingApproval, domain.ActionPause, ""},
		{domain.RolloutCompleted, domain.ActionPause, ""},
		{domain.RolloutPaused, domain.ActionResume, domain.RolloutRunning},
		{domain.RolloutPending, domain.ActionResume, domain.RolloutRunning},
		{domain.RolloutRunning, domain.ActionResume, ""},
		{domain.RolloutAborted, domain.ActionResume, ""},
		{domain.RolloutRunning, domain.ActionAbort, domain.RolloutAborted},
		{domain.RolloutPaused, domain.ActionAbort, domain.RolloutAborted},
		{domain.RolloutWaitingApproval, domain.ActionAbort, domain.RolloutAborted},
		{domain.RolloutFailed, domain.ActionAbort, domain.RolloutAborted},
		{domain.RolloutPending, domain.ActionAbort, domain.RolloutAborted},
		{domain.RolloutCompleted, domain.ActionAbort, ""},
		{domain.RolloutRolledBack, domain.ActionAbort, ""},
		{domain.RolloutSuperseded, domain.ActionAbort, ""},
		{domain.RolloutRolledBack, domain.ActionRollback, ""},
		{domain.RolloutRunning, domain.ActionRollback, domain.RolloutRollingBack},
		{domain.RolloutPaused, domain.ActionRollback, domain.RolloutRollingBack},
		{domain.RolloutFailed, domain.ActionRollback, domain.RolloutRollingBack},
		{domain.RolloutWaitingApproval, domain.ActionRollback, ""},
		{domain.RolloutCompleted, domain.ActionRollback, ""},
		{domain.RolloutRunning, domain.ActionRetryFailed, domain.RolloutRunning},
		{domain.RolloutPaused, domain.ActionRetryFailed, domain.RolloutRunning},
		{domain.RolloutFailed, domain.ActionRetryFailed, domain.RolloutRunning},
		{domain.RolloutCompleted, domain.ActionRetryFailed, ""},
		{domain.RolloutWaitingApproval, domain.ActionRetryFailed, ""},
	}
	for _, c := range cases {
		t.Run(string(c.from)+"/"+string(c.action), func(t *testing.T) {
			e := newEnv(t)
			e.setPolicy("p", spec(allWave()))
			e.addFleet("f")
			e.addAgents(2, "f")
			e.publishSnapshot()
			e.plan()
			r := e.only()
			// Force the status (CAS from the current one).
			ok, err := e.st.Rollouts().CompareAndSetStatus(e.ctx, r.ID, []domain.RolloutStatus{domain.RolloutRunning}, c.from, "test")
			require.NoError(t, err)
			require.True(t, ok)
			got, err := e.eng.Action(e.ctx, r.ID, c.action, 0, "because", operator)
			if c.to == "" {
				assert.ErrorIs(t, err, domain.ErrPrecondition)
				assert.Nil(t, got)
				assert.Equal(t, c.from, e.rollout(r.ID).Status, "status unchanged")
				assert.NotContains(t, e.auditActions(), "rollout."+string(c.action), "failed actions are not audited as success")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.to, got.Status)
			assert.Equal(t, c.to, e.rollout(r.ID).Status)
			assert.Contains(t, e.auditActions(), "rollout."+string(c.action))
		})
	}
}

func TestActionOnUnknownRolloutAndAction(t *testing.T) {
	e := newEnv(t)
	_, err := e.eng.Action(e.ctx, [16]byte{1}, domain.ActionPause, 0, "", operator)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	e.setPolicy("p", spec(allWave()))
	e.addFleet("f")
	e.addAgents(1, "f")
	e.publishSnapshot()
	e.plan()
	_, err = e.eng.Action(e.ctx, e.only().ID, domain.RolloutAction("explode"), 0, "", operator)
	assert.ErrorIs(t, err, domain.ErrInvalidArgument)
}

func TestAbortCancelsOpenAndInFlightTargetsAndStops(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 3
	s.HealthWait = domain.Duration(10 * time.Minute)
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	require.Equal(t, 3, e.inFlight(r.ID))

	got, err := e.eng.Action(e.ctx, r.ID, domain.ActionAbort, 0, "", nil) // nil principal = system
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutAborted, got.Status)
	assert.Equal(t, 10, got.Counts.Cancelled)
	assert.Equal(t, domain.ActorSystem, e.st.AuditEvents()[len(e.st.AuditEvents())-1].ActorType)
	e.steps(10)
	assert.Equal(t, 10, e.rollout(r.ID).Counts.Cancelled, "terminal rollouts are never touched again")
}

func TestPausedRolloutStartsNothingNew(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 2
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(1)
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionPause, 0, "maintenance", operator)
	require.NoError(t, err)
	e.steps(40)
	st := e.targetStatus(r.ID)
	assert.Equal(t, 8, st[domain.TargetPending], "%v", st)
	assert.Equal(t, 2, st[domain.TargetSucceeded], "in-flight targets finish while paused: %v", st)
	_, err = e.eng.Action(e.ctx, r.ID, domain.ActionResume, 0, "", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(300, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
}

func TestFleetRemovedFromGitFailsTheRollout(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	e.addFleet("f")
	e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(1)
	e.fleets = nil
	e.publishSnapshot()
	e.plan() // refreshes the engine's view of the fleets
	e.steps(2)
	got := e.rollout(r.ID)
	assert.Equal(t, domain.RolloutFailed, got.Status)
	assert.Contains(t, got.StatusReason, "removed from the GitOps repository")
	assert.Contains(t, e.notif.events(), notify.EventRolloutFailed)
	assert.Equal(t, notify.SeverityCritical, e.notif.all()[len(e.notif.all())-1].Severity)
}

// A conflicting write by an operator action during a tick must not be lost
// and the engine re-evaluates (optimistic concurrency).
func TestConcurrentTargetChangeIsNotOverwritten(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 1
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	// Wrap the store so that an abort sneaks in between the engine's read and write.
	hook := &hookStore{Store: e.st, before: func() {
		// operator aborts the rollout right before the engine's UpdateTargets
		_, _ = e.st.Rollouts().CompareAndSetStatus(e.ctx, r.ID, []domain.RolloutStatus{domain.RolloutRunning}, domain.RolloutAborted, "abort")
		_, _ = e.st.Rollouts().CancelOpenTargets(e.ctx, r.ID, "abort")
	}}
	eng := NewEngine(e.cfg, Deps{Store: hook, Bus: e.bus, Control: e.ctl, Executors: nil, Notifier: e.notif, Now: e.clk.Now})
	require.NoError(t, eng.Tick(e.ctx))
	st := e.targetStatus(r.ID)
	assert.Equal(t, 3, st[domain.TargetCancelled], "the abort wins: %v", st)
	assert.Equal(t, domain.RolloutAborted, e.rollout(r.ID).Status)
	assert.Equal(t, oldHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash, "nothing was pushed for the cancelled target")
}

type hookStore struct {
	store.Store
	before func()
	fired  bool
}

func (h *hookStore) Rollouts() store.RolloutStore {
	return &hookRollouts{RolloutStore: h.Store.Rollouts(), h: h}
}

type hookRollouts struct {
	store.RolloutStore
	h *hookStore
}

func (r *hookRollouts) UpdateTargets(ctx context.Context, targets []domain.RolloutTarget) ([]domain.AgentID, error) {
	if !r.h.fired {
		r.h.fired = true
		r.h.before()
	}
	return r.RolloutStore.UpdateTargets(ctx, targets)
}

var errBoom = errors.New("boom")

// A rollback must not overwrite the desired config an agent got from the fleet
// it was moved to.
func TestRollbackSkipsAgentsThatLeftTheFleet(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(2)

	otherHash := domain.ConfigHash([]byte("other fleet config"))
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) {
		a.Assignment.FleetName = "g"
		a.Assignment.DesiredConfigHash = otherHash
		a.Assignment.AssignedConfigHash = otherHash
	})
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionRollback, 0, "undo", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutRolledBack }), "status %s", e.rollout(r.ID).Status)
	assert.Equal(t, otherHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash, "the desired config of the other fleet is untouched")
	assert.Equal(t, domain.TargetSkipped, e.targetOf(r.ID, ids[0]).Status)
	assert.Equal(t, oldHash, e.st.Agent(ids[1]).Assignment.DesiredConfigHash)
}
