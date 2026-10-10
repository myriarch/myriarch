// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/notify"
)

var operator = &authz.Principal{Type: authz.PrincipalUser, ID: "u-42", Name: "Olli Operator", Role: domain.RoleOperator}

func carefulSpec() domain.RolloutPolicySpec {
	s := spec(
		domain.WaveSpec{Name: "pilot", Selector: &domain.Selector{MatchAttributes: map[string]string{"labels.pilot": "true"}}},
		domain.WaveSpec{Name: "early", Percent: 10, RequireApproval: true},
		domain.WaveSpec{Name: "half", Percent: 50},
		domain.WaveSpec{Name: "all", Percent: 100},
	)
	s.MaxConcurrent = 20
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 3}
	return s
}

func TestConfigRolloutPilotApprovalWavesAndMaxConcurrent(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", carefulSpec())
	e.addFleet("f")
	ids := e.addAgents(100, "f", pilots(3, 77))
	e.publishSnapshot()
	e.plan()

	r := e.only()
	assert.Equal(t, domain.RolloutKindConfig, r.Kind)
	assert.Equal(t, domain.RolloutRunning, r.Status, "autoStart")
	assert.Equal(t, "gitops", r.CreatedBy)
	assert.Equal(t, "commit-1", r.ToRef)
	assert.Equal(t, "commit-1", r.GitCommit)
	assert.Equal(t, "f", r.FleetName)
	assert.Equal(t, "p", r.PolicyName)
	require.Len(t, r.Waves, 4)
	var counts []int
	for _, w := range r.Waves {
		counts = append(counts, w.Counts.Total)
	}
	assert.Equal(t, []int{2, 8, 40, 50}, counts)
	assert.Equal(t, 100, r.Counts.Total)
	assert.Equal(t, 100, r.Counts.Pending)
	assert.Equal(t, 20, r.Policy.MaxConcurrent)
	assert.Equal(t, 3, r.Policy.FailureThreshold.MaxFailed)
	assert.Equal(t, domain.FailurePause, r.Policy.OnFailure, "policy snapshot has defaults applied")
	assert.Equal(t, "Europe/Berlin", r.Policy.DefaultTimezone)
	assert.Equal(t, "site.timezone", r.Policy.TimezoneAttribute)
	for _, tg := range e.targets(r.ID) {
		assert.Equal(t, oldHash.String(), tg.FromRef)
		assert.Equal(t, newHash.String(), tg.ToRef)
		assert.NotEmpty(t, tg.Hostname)
		assert.Equal(t, 1, tg.Attempts)
	}
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted}, e.notif.events())
	assert.Equal(t, []string{"rollout.create"}, e.auditActions())
	assert.Equal(t, domain.ActorGitOps, e.st.AuditEvents()[0].ActorType)

	// Planning again changes nothing: at most one active rollout per (fleet, kind).
	e.plan()
	assert.Len(t, e.rollouts(), 1)

	// Pilot wave runs, then the rollout waits for the approval of "early".
	maxInFlight := 0
	require.True(t, e.runUntil(200, func() bool {
		if n := e.inFlight(r.ID); n > maxInFlight {
			maxInFlight = n
		}
		return e.rollout(r.ID).Status == domain.RolloutWaitingApproval
	}))
	cur := e.rollout(r.ID)
	assert.Equal(t, 1, cur.CurrentWave)
	assert.Equal(t, domain.WaveCompleted, cur.Waves[0].Status)
	assert.Equal(t, domain.WaveWaitingApproval, cur.Waves[1].Status)
	assert.Equal(t, 2, cur.Counts.Succeeded)
	for _, i := range []int{3, 77} {
		assert.Equal(t, newHash, e.st.Agent(ids[i]).Assignment.DesiredConfigHash, "pilot agents got the new config")
	}
	assert.Equal(t, oldHash, e.st.Agent(ids[5]).Assignment.DesiredConfigHash, "others still run the old config")
	assert.Contains(t, cur.StatusReason, "waits for approval")
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutWaitingApprove}, e.notif.events())

	// While waiting nothing starts.
	e.steps(20)
	assert.Equal(t, domain.RolloutWaitingApproval, e.rollout(r.ID).Status)
	assert.Equal(t, 98, e.targetStatus(r.ID)[domain.TargetPending]+0, "no further target was started")
	assert.Len(t, e.notif.events(), 2, "the approval request is notified once")

	// Approve (a wave that does not need approval, an unknown wave and a wave
	// that already finished are preconditions failures).
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionApprove, 2, "", operator)
	assert.ErrorIs(t, err, domain.ErrPrecondition)
	_, err = e.eng.Action(e.ctx, r.ID, domain.ActionApprove, 9, "", operator)
	assert.ErrorIs(t, err, domain.ErrPrecondition)
	_, err = e.eng.Action(e.ctx, r.ID, domain.ActionApprove, 0, "", operator)
	assert.ErrorIs(t, err, domain.ErrPrecondition)
	got, err := e.eng.Action(e.ctx, r.ID, domain.ActionApprove, 1, "looks good", operator)
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutRunning, got.Status)
	assert.Equal(t, "user:u-42", got.Waves[1].ApprovedBy)
	assert.NotNil(t, got.Waves[1].ApprovedAt)

	require.True(t, e.runUntil(600, func() bool {
		if n := e.inFlight(r.ID); n > maxInFlight {
			maxInFlight = n
		}
		return e.rollout(r.ID).Status == domain.RolloutCompleted
	}), "rollout did not complete: %+v", e.rollout(r.ID).Counts)
	assert.LessOrEqual(t, maxInFlight, 20, "maxConcurrent")
	assert.Greater(t, maxInFlight, 10, "the limit is actually used")

	done := e.rollout(r.ID)
	assert.Equal(t, 100, done.Counts.Succeeded)
	assert.NotNil(t, done.FinishedAt)
	assert.NotNil(t, done.StartedAt)
	for i := 0; i < len(done.Waves); i++ {
		w := done.Waves[i]
		assert.Equal(t, domain.WaveCompleted, w.Status, "wave %d", i)
		assert.NotNil(t, w.StartedAt)
		assert.NotNil(t, w.FinishedAt)
		if i > 0 {
			assert.False(t, w.StartedAt.Before(*done.Waves[i-1].FinishedAt), "wave %d starts after wave %d finished", i, i-1)
		}
	}
	for _, id := range ids {
		assert.Equal(t, newHash, e.st.Agent(id).Assignment.DesiredConfigHash)
	}
	// Each agent was pushed exactly once.
	pushed := e.ctl.pushed()
	assert.Len(t, pushed, 100)
	for id, n := range pushed {
		assert.Equal(t, 1, n, "agent %s", id)
	}
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutWaitingApprove, notify.EventRolloutCompleted}, e.notif.events())
	assert.Equal(t, []string{"rollout.create", "rollout.wave_waiting_approval", "rollout.approve", "rollout.complete"}, e.auditActions())
	ev := e.st.AuditEvents()[2]
	assert.Equal(t, domain.ActorUser, ev.ActorType)
	assert.Equal(t, "u-42", ev.ActorID)
	assert.Equal(t, "looks good", ev.Details["reason"])

	// Nothing left to do: no new rollout.
	e.plan()
	assert.Len(t, e.rollouts(), 1)
	// agent events document the transitions
	assert.NotEmpty(t, e.st.Events(ids[3]))

	// metrics
	e.step()
	assert.Equal(t, 0.0, gaugeValue(t, e.eng, "myriarch_rollouts_active", "kind", "config"))
}

func TestConfigRolloutStartsWithoutApprovalWhenAutoStartIsOffAndWaitsForResume(t *testing.T) {
	e := newEnv(t)
	e.cfg.AutoStart = false
	e.eng = e.newEngine()
	e.setPolicy("p", spec(allWave()))
	e.addFleet("f")
	e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	assert.Equal(t, domain.RolloutPending, r.Status)
	assert.Empty(t, e.notif.events(), "not started yet")
	e.steps(5)
	assert.Equal(t, domain.RolloutPending, e.rollout(r.ID).Status, "ticks do not start pending rollouts")
	assert.Equal(t, 3, e.rollout(r.ID).Counts.Pending)

	got, err := e.eng.Action(e.ctx, r.ID, domain.ActionResume, 0, "", operator)
	require.NoError(t, err)
	assert.Equal(t, domain.RolloutRunning, got.Status)
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
}

func TestFirstWaveWithApprovalWaitsImmediately(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "first", Percent: 50, RequireApproval: true}, allWave())
	e.setPolicy("p", s)
	e.addFleet("f")
	e.addAgents(10, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	assert.Equal(t, domain.RolloutWaitingApproval, r.Status)
	assert.Equal(t, domain.WaveWaitingApproval, r.Waves[0].Status)
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutWaitingApprove}, e.notif.events())
	e.steps(5)
	assert.Equal(t, 10, e.targetStatus(r.ID)[domain.TargetPending])

	// Pre-approval of a later wave is possible while it is pending.
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionApprove, 0, "", operator)
	require.NoError(t, err)
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
}

func TestRolloutWithoutRelevantAgentsIsNotCreated(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	e.addFleet("f")
	// bootstrap agents (no desired), up to date agents, agents with errors, other kinds
	e.addAgents(1, "f", func(i int, a *domain.Agent) { a.Assignment.DesiredConfigHash = nil })
	e.addAgents(1, "f", func(i int, a *domain.Agent) { a.Assignment.DesiredConfigHash = a.Assignment.AssignedConfigHash })
	e.addAgents(1, "f", func(i int, a *domain.Agent) { a.Assignment.Error = "render failed" })
	e.addAgents(1, "f", func(i int, a *domain.Agent) { a.Kind = domain.AgentKindUpdater })
	e.addAgents(1, "other")
	e.publishSnapshot()
	e.plan()
	assert.Empty(t, e.rollouts(), "bootstrap, up-to-date, erroneous, non-collector and unknown-fleet agents are not relevant")
}

func gaugeValue(t *testing.T, eng *Engine, name, label, value string) float64 {
	t.Helper()
	return metricValue(t, eng, name, map[string]string{label: value})
}

func TestDefaultPolicyWhenFleetReferencesNone(t *testing.T) {
	e := newEnv(t)
	e.addFleet("f", func(f *domain.Fleet) { f.Rollout = domain.FleetRolloutSpec{} })
	e.addAgents(3, "f")
	e.publishSnapshot()
	e.plan()
	r := e.only()
	assert.Equal(t, "default", r.PolicyName)
	assert.Equal(t, 50, r.Policy.MaxConcurrent, "built-in default policy")
	assert.Equal(t, 10*time.Minute, r.Policy.ApplyTimeout.D(), "config default apply timeout")
	require.Len(t, r.Waves, 1)
	assert.Equal(t, "all", r.Waves[0].Name)
}
