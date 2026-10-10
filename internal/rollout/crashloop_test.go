// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

func crashLoopEnv(t *testing.T) (*env, domain.Rollout, []domain.AgentID) {
	t.Helper()
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(2 * time.Minute)
	s.ApplyTimeout = domain.Duration(10 * time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	return e, r, ids
}

// untilVerifying steps until both targets verify (the new version was reported).
func untilVerifying(t *testing.T, e *env, r domain.Rollout) {
	t.Helper()
	require.True(t, e.runUntil(20, func() bool { return e.targetStatus(r.ID)[domain.TargetVerifying] == 2 }), "%v", e.targetStatus(r.ID))
}

// A collector that keeps falling back from OK to starting is crash-looping:
// the health gate must not pass it just because the last report before the
// end of the health wait is OK.
func TestVersionCrashLoopingCollectorFailsTheTarget(t *testing.T) {
	e, r, ids := crashLoopEnv(t)
	untilVerifying(t, e, r)
	loop, good := ids[0], ids[1]

	// OK -> starting -> OK -> starting -> OK: two regressions
	for _, starting := range []bool{false, true, false, true, false} {
		if starting {
			e.starting[loop] = true
		} else {
			delete(e.starting, loop)
		}
		e.steps(2)
	}
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	tg := e.targetOf(r.ID, loop)
	assert.Equal(t, domain.TargetFailed, tg.Status)
	assert.Contains(t, tg.LastError, "crash loop")
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, good).Status, "a healthy neighbour is unaffected")
	assert.NotContains(t, e.rollout(r.ID).Annotations, annHealthTrack, "tracks of finished targets are dropped")
}

// One restart after the update (e.g. an applied config) is normal.
func TestVersionSingleRestartAfterUpdateIsTolerated(t *testing.T) {
	e, r, ids := crashLoopEnv(t)
	untilVerifying(t, e, r)
	e.steps(2) // healthy
	e.starting[ids[0]] = true
	e.steps(2) // one regression
	delete(e.starting, ids[0])
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, ids[0]).Status)
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, ids[1]).Status)
}

// The normal start-up (starting before the first OK) is not a regression, however long it takes.
func TestVersionSlowFirstStartupIsNotACrashLoop(t *testing.T) {
	e, r, ids := crashLoopEnv(t)
	e.starting[ids[0]] = true
	e.starting[ids[1]] = true
	untilVerifying(t, e, r)
	e.steps(6)
	assert.Equal(t, 2, e.targetStatus(r.ID)[domain.TargetVerifying], "still starting up: %v", e.targetStatus(r.ID))
	delete(e.starting, ids[0])
	delete(e.starting, ids[1])
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 2, e.rollout(r.ID).Counts.Succeeded)
}

// The track survives a leader change: it lives in the rollout's annotations.
func TestVersionCrashLoopTrackIsPersistedWithTheRollout(t *testing.T) {
	e, r, ids := crashLoopEnv(t)
	untilVerifying(t, e, r)
	e.steps(1)
	e.starting[ids[0]] = true
	e.steps(1)
	m, _ := e.rollout(r.ID).Annotations[annHealthTrack].(map[string]any)
	require.Contains(t, m, ids[0].String())

	e.eng = e.newEngine() // new leader
	delete(e.starting, ids[0])
	e.steps(1)
	e.starting[ids[0]] = true // second regression, counted on top of the persisted first one
	require.True(t, e.runUntil(20, func() bool { return e.targetOf(r.ID, ids[0]).Status == domain.TargetFailed }))
}
