// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

// The end of an automatic rollback, as the real myriarch-updater reports it
// (installer.go rollback -> run.done(rolling_back) publishes the component with
// status "rolling_back" and the step done; End(rolled_back) follows a few
// milliseconds later), with the target already failed by the engine (health
// wait) and the rollout paused (onFailure pause), like stage 9 of the kind
// probe. rollbackSetup returns the environment, the rollout, the collector,
// the updater, the attempt start and a function that publishes an updater
// report.
func rollbackSetup(t *testing.T) (*env, domain.Rollout, domain.AgentID, uuid.UUID, time.Duration, func(string, map[string]reported)) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(20 * time.Second)
	s.ApplyTimeout = domain.Duration(10 * time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
	s.OnFailure = domain.FailurePause
	e.setPolicy("p", s)
	versionFleet(e)
	updater := uuid.MustParse("00000000-0000-7000-8000-0000000fff01")
	ids := e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Updater = &domain.UpdaterLink{AgentID: updater, Online: true} })
	e.st.PutAgent(domain.Agent{ID: updater, Kind: domain.AgentKindUpdater, Connection: domain.AgentConnection{Status: domain.ConnectionOnline}, Health: &domain.AgentHealth{Healthy: true}})
	e.noInstall[ids[0]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()
	attempt := e.clk.Now().Sub(t0)
	report := func(status string, steps map[string]reported) {
		up := updaterWith("0.162.0", status, attempt, "", steps)
		e.st.UpdateAgent(updater, func(a *domain.Agent) { a.Health = up.Health })
	}
	return e, r, ids[0], updater, attempt, report
}

// The engine keeps following the updater after it has failed the target
// (settling, docs/design/version-selection.md, Target progress > Derivation).
// A tick that reads the intermediate report (status rolling_back, step
// rolling_back done) may still show phase failed, as it does today; the tick
// after the final report (status rolled_back) shows rolled_back. Only the
// converged phase is pinned, not the transient one, so an engine that converges
// sooner keeps this test green. Anything that reads the progress right after
// the updater's last report has to wait for the converged phase (the kind probe
// does).
func TestEngineProgressConvergesToRolledBackAfterTheIntermediateRollingBackReport(t *testing.T) {
	e, r, col, updater, attempt, report := rollbackSetup(t)
	base := map[string]reported{
		"downloading": {"done", attempt, attempt, ""}, "verifying": {"done", attempt, attempt, ""},
		"installing": {"done", attempt, attempt, ""}, "restarting": {"done", attempt, attempt, ""},
		"health_check": {"running", attempt, attempt, ""},
	}
	report("health_check", base)
	e.unhealthy[col] = true
	e.st.UpdateAgent(col, func(a *domain.Agent) { a.Version = "0.162.0" })
	require.True(t, e.runUntil(30, func() bool { return e.targetOf(r.ID, col).Status == domain.TargetFailed }))
	require.True(t, e.runUntil(5, func() bool { return e.rollout(r.ID).Status == domain.RolloutPaused }))

	rb := map[string]reported{}
	for k, v := range base {
		rb[k] = v
	}
	rb["health_check"] = reported{"failed", attempt, attempt, "collector did not become healthy"}
	rb["rolling_back"] = reported{"running", attempt, attempt, ""}
	report("rolling_back", rb)
	e.step()

	// the rollback restarted the old collector
	delete(e.unhealthy, col)
	e.st.UpdateAgent(col, func(a *domain.Agent) { a.Version = "0.161.0" })

	// h1: run.done(rolling_back) -> status still "rolling_back", step done
	rb["rolling_back"] = reported{"done", attempt, attempt, ""}
	report("rolling_back", rb)
	e.step()
	p := e.targetOf(r.ID, col).Progress
	require.NotNil(t, p)
	t.Logf("after h1: phase=%s source=%s steps=%v", p.Phase, p.Source, stepsOf(p))
	// transient, not pinned: failed while the engine has not seen the end of the attempt, rolled_back if it converges at once
	assert.Contains(t, []domain.UpdatePhase{domain.PhaseFailed, domain.PhaseRolledBack}, p.Phase, "the attempt has not ended yet: the phase is still failed or already rolled_back")

	// h2: End(rolled_back) + installFinished (top-level LastError) + INSTALL_FAILED
	report("rolled_back", rb)
	e.st.UpdateAgent(updater, func(a *domain.Agent) {
		a.Health.LastError = "last update failed: rolled back"
		a.PackageStatus = &domain.PackageStatuses{UpdatedAt: e.clk.Now(), Packages: map[string]*domain.PackageStatus{"": {
			Status: domain.PackageInstallFailed, ServerOfferedVersion: "v0.162.0", ErrorMessage: "rolled back to v0.161.0",
		}}}
	})
	e.step()
	p = e.targetOf(r.ID, col).Progress
	t.Logf("after h2: phase=%s source=%s steps=%v", p.Phase, p.Source, stepsOf(p))
	assert.Equal(t, domain.PhaseRolledBack, p.Phase, "the next tick after End(rolled_back) converges")
	assert.Equal(t, domain.RolloutPaused, e.rollout(r.ID).Status)
}
