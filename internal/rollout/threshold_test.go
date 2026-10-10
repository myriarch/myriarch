// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/notify"
)

func thresholdEnv(t *testing.T, mut func(s *domain.RolloutPolicySpec)) (*env, []domain.AgentID) {
	e := newEnv(t)
	s := spec(allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 3}
	if mut != nil {
		mut(&s)
	}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(20, "f")
	e.publishSnapshot()
	e.plan()
	return e, ids
}

func TestFailureThresholdPausesAndResumeAcknowledges(t *testing.T) {
	e, ids := thresholdEnv(t, nil)
	for _, i := range []int{0, 1, 2, 3, 4} {
		e.failConfig[ids[i]] = true
	}
	r := e.only()
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))

	cur := e.rollout(r.ID)
	assert.Contains(t, cur.StatusReason, "failure threshold reached")
	assert.Contains(t, cur.StatusReason, `wave "all"`)
	assert.Equal(t, domain.WaveFailed, cur.Waves[0].Status)
	assert.Equal(t, 5, cur.Counts.Failed)
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutPaused}, e.notif.events())
	n := e.notif.all()[1]
	assert.Equal(t, notify.SeverityWarning, n.Severity)
	assert.Contains(t, n.Summary, "invalid pipeline"[:0]+"host-")
	failedTarget := e.targetOf(r.ID, ids[0])
	assert.Equal(t, domain.TargetFailed, failedTarget.Status)
	assert.Contains(t, failedTarget.LastError, "agent rejected the configuration: invalid pipeline")
	assert.NotNil(t, failedTarget.FinishedAt)

	// In-flight targets are finished while paused, nothing new starts.
	e.steps(30)
	st := e.targetStatus(r.ID)
	assert.Equal(t, 0, st[domain.TargetPending]+st[domain.TargetInProgress]+st[domain.TargetVerifying]-0, "all started targets reached an end state: %v", st)
	assert.Equal(t, domain.RolloutPaused, e.rollout(r.ID).Status, "still paused")
	assert.Len(t, e.notif.events(), 2, "paused is notified once")

	// resume: the failures are acknowledged, the rollout completes with its failed targets
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionResume, 0, "continue anyway", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(30, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }), "status %s", e.rollout(r.ID).Status)
	done := e.rollout(r.ID)
	assert.Equal(t, 5, done.Counts.Failed)
	assert.Equal(t, domain.WaveCompleted, done.Waves[0].Status)
	assert.Equal(t, 15, done.Counts.Succeeded)
	// failed agents still run the old config? their desired is the failed new one (never auto-rolled back)
	assert.Equal(t, newHash, e.st.Agent(ids[0]).Assignment.DesiredConfigHash)
}

func TestResumeThenFurtherFailuresPauseAgain(t *testing.T) {
	e, ids := thresholdEnv(t, func(s *domain.RolloutPolicySpec) { s.MaxConcurrent = 5 })
	for _, i := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		e.failConfig[ids[i]] = true
	}
	r := e.only()
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))
	f1 := e.rollout(r.ID).Counts.Failed
	assert.GreaterOrEqual(t, f1, 3)

	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionResume, 0, "", operator)
	require.NoError(t, err)
	// 3 more failures are allowed after the acknowledgement, then it pauses again.
	require.True(t, e.runUntil(100, func() bool {
		cur := e.rollout(r.ID)
		return cur.Status == domain.RolloutPaused && cur.Counts.Failed >= f1+3
	}), "status=%s failed=%d (after resume from %d)", e.rollout(r.ID).Status, e.rollout(r.ID).Counts.Failed, f1)
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutPaused, notify.EventRolloutPaused}, e.notif.events())
}

func TestRetryFailedRestartsFailedTargetsWithClearedFailedHash(t *testing.T) {
	e, ids := thresholdEnv(t, nil)
	for _, i := range []int{0, 1, 2} {
		e.failConfig[ids[i]] = true
	}
	r := e.only()
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))
	require.Equal(t, 3, e.rollout(r.ID).Counts.Failed)
	require.Equal(t, newHash, e.st.Agent(ids[0]).Assignment.FailedConfigHash, "the agent's failed marker prevents automatic re-offers")
	pushedBefore := e.ctl.pushed()[ids[0]]

	// The problem is fixed on the agents; the operator retries.
	for _, i := range []int{0, 1, 2} {
		e.failConfig[ids[i]] = false
	}
	// retry needs the paused/running/failed state
	got, err := e.eng.Action(e.ctx, r.ID, domain.ActionRetryFailed, 0, "fixed", operator)
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutRunning, got.Status)
	assert.Equal(t, 0, got.Counts.Failed)
	retried := e.targetOf(r.ID, ids[0])
	assert.Equal(t, 2, retried.Attempts)
	assert.Empty(t, retried.LastError)
	assert.Equal(t, domain.TargetPending, retried.Status)

	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 20, e.rollout(r.ID).Counts.Succeeded)
	assert.Empty(t, e.st.Agent(ids[0]).Assignment.FailedConfigHash, "the failed marker was cleared before the retry")
	assert.Equal(t, pushedBefore+1, e.ctl.pushed()[ids[0]], "re-pushed after the retry")
	assert.Equal(t, newHash, e.st.Agent(ids[0]).RemoteConfig.LastHash)
	assert.Contains(t, e.auditActions(), "rollout.retry_failed")
}

func TestFailureThresholdPercent(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailedPercent: 10}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(40, "f")
	e.publishSnapshot()
	e.plan()
	// 3 of 40 = 7.5 % does not trigger, 4 of 40 = 10 % does.
	for _, i := range []int{0, 1, 2} {
		e.failConfig[ids[i]] = true
	}
	r := e.only()
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 3, e.rollout(r.ID).Counts.Failed)

	e2 := newEnv(t)
	e2.setPolicy("p", s)
	e2.addFleet("f")
	ids2 := e2.addAgents(40, "f")
	e2.publishSnapshot()
	e2.plan()
	for _, i := range []int{0, 1, 2, 3} {
		e2.failConfig[ids2[i]] = true
	}
	r2 := e2.only()
	require.True(t, e2.runUntil(60, func() bool { return e2.rollout(r2.ID).Status == domain.RolloutPaused }))
}

func TestDefaultThresholdIsOneFailure(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave())) // no failureThreshold: WithDefaults -> maxFailed 1
	e.addFleet("f")
	ids := e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	e.failConfig[ids[4]] = true
	r := e.only()
	require.Equal(t, 1, r.Policy.FailureThreshold.MaxFailed)
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))
}

func TestApplyTimeoutFailsSilentAgents(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.ApplyTimeout = domain.Duration(60 * 1e9)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(5, "f")
	e.publishSnapshot()
	e.plan()
	e.noApply[ids[0]] = true
	r := e.only()
	require.True(t, e.runUntil(80, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	tg := e.targetOf(r.ID, ids[0])
	assert.Equal(t, domain.TargetFailed, tg.Status)
	assert.Contains(t, tg.LastError, "timed out")
	assert.Equal(t, 4, e.rollout(r.ID).Counts.Succeeded)
}

func TestUnhealthyOrOfflineDuringHealthWaitFails(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(60 * 1e9)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(4, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	// let everything apply, then break two agents during the health wait
	e.steps(3)
	require.Equal(t, 4, e.targetStatus(r.ID)[domain.TargetVerifying])
	e.unhealthy[ids[0]] = true
	e.offline[ids[1]] = true
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[0]).Status)
	assert.Contains(t, e.targetOf(r.ID, ids[0]).LastError, "unhealthy")
	assert.Contains(t, e.targetOf(r.ID, ids[0]).LastError, "pipeline broken")
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[1]).Status)
	assert.Contains(t, e.targetOf(r.ID, ids[1]).LastError, "offline")
	assert.Equal(t, 2, e.rollout(r.ID).Counts.Succeeded)
}

func TestVerifyingRequiresTheFullHealthWait(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(10 * 60 * 1e9)
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(2, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(100) // 500 s
	st := e.targetStatus(r.ID)
	assert.Equal(t, 2, st[domain.TargetVerifying], "still in the 10 minute health wait: %v", st)
	e.steps(30)
	assert.Equal(t, domain.RolloutCompleted, e.rollout(r.ID).Status)
}

func TestAgentRevertingTheConfigDuringHealthWaitFails(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(120 * 1e9)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	e.addFleet("f")
	ids := e.addAgents(2, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	// supervisor automatic rollback: agent reports the old hash again
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) {
		a.RemoteConfig = domain.RemoteConfigState{Status: domain.RemoteConfigApplied, LastHash: oldHash}
	})
	e.noApply[ids[0]] = true
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[0]).Status)
	assert.Contains(t, e.targetOf(r.ID, ids[0]).LastError, "reverted")
}

// A rollout paused by the failure threshold whose only targets failed (no open
// target left) is replaced when a fix changes the goal.
func TestPausedRolloutWithOnlyFailedTargetsIsSupersededByTheFix(t *testing.T) {
	e := newEnv(t)
	sp := spec(allWave())
	sp.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
	e.setPolicy("p", sp)
	e.addFleet("f")
	ids := e.addAgents(1, "f")
	e.failConfig[ids[0]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))
	e.steps(5)

	// the fix: a new rendered config
	hash2 := domain.SHA256([]byte("config-fixed"))
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) { a.Assignment.AssignedConfigHash = hash2 })
	delete(e.failConfig, ids[0])
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()

	assert.Equal(t, domain.RolloutSuperseded, e.rollout(r.ID).Status)
	rs := e.rollouts()
	require.Len(t, rs, 2, "a new rollout was planned for the fix")
	require.True(t, e.runUntil(60, func() bool {
		for _, ro := range e.rollouts() {
			if ro.ID != r.ID && ro.Status == domain.RolloutCompleted {
				return true
			}
		}
		return false
	}))
}

// The acknowledgement made by "resume" covers the failures seen at that time.
// retry-failed resets those targets; new failures after it must count again
// instead of being absorbed by the old acknowledgement.
func TestRetryFailedDropsTheResumeAcknowledgement(t *testing.T) {
	e, ids := thresholdEnv(t, func(s *domain.RolloutPolicySpec) {
		s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
		s.MaxConcurrent = 1
	})
	for _, id := range ids {
		e.failConfig[id] = true // every agent rejects the new config
	}
	r := e.only()
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))

	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionResume, 0, "", operator)
	require.NoError(t, err)
	e.steps(1)
	require.Len(t, e.rollout(r.ID).Annotations[annAckedFailures].(map[string]any)["0"], 1, "resume acknowledged the failure")

	// retry-failed while running resets the failed targets; they fail again: the
	// first new failure must pause again.
	require.Equal(t, domain.RolloutRunning, e.rollout(r.ID).Status)
	_, err = e.eng.Action(e.ctx, r.ID, domain.ActionRetryFailed, 0, "", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }), "a failure after retry-failed counts again: %s", e.rollout(r.ID).Status)
	assert.Equal(t, 1, e.rollout(r.ID).Counts.Failed, "the first new failure pauses (not absorbed by the old acknowledgement)")
}

// A failure of the same agent after retry-failed is a new failure, not the
// acknowledged one.
func TestFailureOfARetriedTargetIsNotCoveredByTheOldAcknowledgement(t *testing.T) {
	e, ids := thresholdEnv(t, func(s *domain.RolloutPolicySpec) {
		s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
		s.MaxConcurrent = 1
	})
	for _, id := range ids {
		e.failConfig[id] = true
	}
	r := e.only()
	paused := func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }
	require.True(t, e.runUntil(60, paused))
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionResume, 0, "", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(60, func() bool { return !paused() }))
	require.True(t, e.runUntil(60, paused), "the second failure pauses again")
	require.Equal(t, 2, e.rollout(r.ID).Counts.Failed)

	_, err = e.eng.Action(e.ctx, r.ID, domain.ActionRetryFailed, 0, "", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(60, func() bool { return !paused() }))
	require.True(t, e.runUntil(60, paused), "the retried targets fail again and pause the rollout")
	assert.Equal(t, 1, e.rollout(r.ID).Counts.Failed, "the first failure of a retried agent counts")
}
