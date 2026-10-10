// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/notify"
)

func versionFleet(e *env, mut ...func(f *domain.Fleet)) {
	e.addFleet("f", func(f *domain.Fleet) {
		f.Collector = domain.CollectorSpec{Version: "0.162.0"}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorUpdater}
		for _, m := range mut {
			m(f)
		}
	})
}

func TestVersionRolloutWavesBatchesAndCompletion(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "first", Percent: 20}, allWave())
	s.MaxConcurrent = 3
	e.setPolicy("p", s)
	versionFleet(e)
	updater := uuid.MustParse("00000000-0000-7000-8000-00000000ffff")
	ids := e.addAgents(10, "f", inSync(), func(i int, a *domain.Agent) {
		if i == 0 {
			a.Updater = &domain.UpdaterLink{AgentID: updater, Online: true}
		}
	})
	e.st.PutAgent(domain.Agent{ID: updater, Kind: domain.AgentKindUpdater, Connection: domain.AgentConnection{Status: domain.ConnectionOnline}})
	e.publishSnapshot()
	e.plan()

	r := e.only()
	assert.Equal(t, domain.RolloutKindVersion, r.Kind)
	assert.Equal(t, "0.162.0", r.ToRef)
	assert.Equal(t, "0.161.0", r.FromRef, "all targets start from the same version")
	assert.Equal(t, domain.ExecutorUpdater, r.Executor)
	assert.Equal(t, 30*time.Second, r.Policy.HealthWait.D())
	assert.Equal(t, 5*time.Minute, r.Policy.ApplyTimeout.D(), "explicit")
	assert.Equal(t, "otelcol-contrib", r.Annotations["distribution"])
	for _, tg := range e.targets(r.ID) {
		assert.Equal(t, "0.161.0", tg.FromRef)
		assert.Equal(t, "0.162.0", tg.ToRef)
	}

	e.step()
	require.Equal(t, 1, e.exec.calls(), "one Start call per tick and wave with all startable targets")
	req := e.exec.startCalls[0]
	assert.Equal(t, r.ID, req.RolloutID)
	assert.Equal(t, 0, req.Wave)
	assert.Equal(t, "f", req.Fleet)
	assert.Equal(t, "otelcol-contrib", req.Distribution)
	assert.Equal(t, "0.162.0", req.ToVersion)
	assert.Equal(t, domain.ExecutorUpdater, req.Spec.Executor)
	assert.Nil(t, req.Definition)
	require.Len(t, req.Targets, 2, "first wave: ceil(20%% of 10) = 2 (<= maxConcurrent 3)")
	for _, tg := range req.Targets {
		assert.Equal(t, "0.161.0", tg.CurrentVersion)
		assert.Equal(t, "linux", tg.OSType)
		assert.Equal(t, "amd64", tg.Arch)
		assert.NotEmpty(t, tg.Hostname)
		assert.Equal(t, 1, tg.Attempt)
		assert.NotEmpty(t, tg.Attributes["site.id"])
		if tg.AgentID == ids[0] {
			require.NotNil(t, tg.UpdaterAgentID)
			assert.Equal(t, updater, *tg.UpdaterAgentID)
		}
	}
	for _, tg := range e.targets(r.ID) {
		if tg.Status == domain.TargetInProgress {
			assert.True(t, len(tg.ExecutorRef) > 4 && tg.ExecutorRef[:4] == "job-", "external ref stored: %q", tg.ExecutorRef)
		}
	}

	require.True(t, e.runUntil(300, func() bool {
		if n := e.inFlight(r.ID); n > 3 {
			t.Fatalf("maxConcurrent exceeded: %d", n)
		}
		return e.rollout(r.ID).Status == domain.RolloutCompleted
	}))
	done := e.rollout(r.ID)
	assert.Equal(t, 10, done.Counts.Succeeded)
	for _, id := range ids {
		assert.Equal(t, "0.162.0", e.st.Agent(id).Version)
	}
	started := e.exec.started()
	assert.Len(t, started, 10)
	for id, n := range started {
		assert.Equal(t, 1, n, "agent %s is started exactly once", id)
	}
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutCompleted}, e.notif.events())
	// executors were validated before they were used
	e.plan()
	assert.Len(t, e.rollouts(), 1)
}

func TestVersionPlanningRules(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	e.addFleet("none", func(f *domain.Fleet) { f.Collector.Version = "0.162.0" }) // executor none
	e.addFleet("novers", func(f *domain.Fleet) { f.Update.Executor = domain.ExecutorUpdater })
	good := e.addAgents(2, "f", inSync())
	e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Version = "0.162.0" })         // up to date
	e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Version = "v0.162.0" })        // normalized
	e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Version = "" })                // unknown version
	e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Assignment.Error = "x" })      // render error
	down := e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Version = "0.163.0" }) // downgrade counts
	e.addAgents(2, "none", inSync())
	e.addAgents(2, "novers", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	var got []domain.AgentID
	for _, tg := range e.targets(r.ID) {
		got = append(got, tg.AgentID)
	}
	assert.ElementsMatch(t, append(good, down...), got)
	assert.Equal(t, "", r.FromRef, "mixed source versions")
	assert.Equal(t, "0.163.0", e.targetOf(r.ID, down[0]).FromRef)
}

func TestVersionExecutorErrorsFailTargets(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"validate", func(e *env) {
			e.exec.validate = domain.Issues{{Severity: domain.SeverityError, Code: "executor_invalid", Message: "secret env MYRIARCH_X is not set"}}
		}, "executor validation failed: secret env MYRIARCH_X is not set"},
		{"call error", func(e *env) { e.exec.startErr = errors.New("connection refused") }, "executor call failed: connection refused"},
		{"per target error", func(e *env) {
			e.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
				return executor.TargetResult{AgentID: tg.AgentID, Error: "no myriarch-updater linked"}
			}
		}, "no myriarch-updater linked"},
		{"not started", func(e *env) {
			e.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
				return executor.TargetResult{AgentID: tg.AgentID}
			}
		}, "executor did not start the update"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.setPolicy("p", spec(domain.WaveSpec{Name: "w", Percent: 50}, allWave()))
			versionFleet(e)
			e.addAgents(6, "f", inSync())
			e.publishSnapshot()
			c.setup(e)
			e.plan()
			r := e.only()
			e.step()
			got := e.rollout(r.ID)
			assert.Equal(t, domain.RolloutFailed, got.Status, "no target of the wave could be started: the rollout cannot proceed")
			assert.Contains(t, got.StatusReason, c.want)
			st := e.targetStatus(r.ID)
			assert.Equal(t, 3, st[domain.TargetFailed], "%v", st)
			assert.Equal(t, 3, st[domain.TargetPending], "second wave untouched: %v", st)
			assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutFailed}, e.notif.events())
			assert.Contains(t, e.targets(r.ID)[0].LastError, c.want)
		})
	}
}

func TestVersionRolloutWithMixedExecutorResultsFollowsTheThreshold(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(4, "f", inSync())
	bad := ids[1]
	e.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
		if tg.AgentID == bad {
			return executor.TargetResult{AgentID: tg.AgentID, Error: "myriarch-updater offline"}
		}
		return executor.TargetResult{AgentID: tg.AgentID, Started: true}
	}
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()
	got := e.rollout(r.ID)
	assert.Equal(t, domain.RolloutPaused, got.Status, "some targets work: the failure threshold decides (pause)")
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, bad).Status)
	assert.Equal(t, "myriarch-updater offline", e.targetOf(r.ID, bad).LastError)
	assert.Equal(t, "started", e.targetOf(r.ID, ids[0]).ExecutorRef, "no external ref -> marker")
}

func TestVersionExecutorRefResolvesTheDefinition(t *testing.T) {
	e := newEnv(t)
	e.exec.typ = domain.ExecutorWebhook
	e.setPolicy("p", spec(allWave()))
	versionFleet(e, func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorWebhook, ExecutorRef: "hook", Params: map[string]any{"deployment": "otel-gw"}}
		f.Collector.Distribution = "otelcol-contrib"
	})
	e.executors = []domain.Executor{{ObjectMeta: domain.ObjectMeta{Name: "hook"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook, Webhook: &domain.WebhookExecutorSpec{URL: "https://ci.example.com/hook"}}}}
	e.addAgents(2, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	assert.Equal(t, "hook", r.Annotations["executorRef"])
	e.step()
	require.Equal(t, 1, e.exec.calls())
	req := e.exec.startCalls[0]
	assert.Equal(t, "hook", req.Spec.ExecutorRef)
	assert.Equal(t, map[string]any{"deployment": "otel-gw"}, req.Spec.Params)
	require.NotNil(t, req.Definition)
	assert.Equal(t, "hook", req.Definition.Name)
	assert.Equal(t, "https://ci.example.com/hook", req.Definition.Webhook.URL)

	// A vanished executor object fails the targets with a clear message.
	e2 := newEnv(t)
	e2.exec.typ = domain.ExecutorWebhook
	e2.setPolicy("p", spec(allWave()))
	versionFleet(e2, func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorWebhook, ExecutorRef: "gone"}
	})
	e2.addAgents(2, "f", inSync())
	e2.publishSnapshot()
	e2.plan()
	r2 := e2.only()
	e2.step()
	assert.Equal(t, domain.RolloutFailed, e2.rollout(r2.ID).Status)
	assert.Contains(t, e2.rollout(r2.ID).StatusReason, `executor "gone" does not exist`)
	assert.Equal(t, 0, e2.exec.calls())
}

func TestVersionInstallFailedAndTimeout(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.ApplyTimeout = domain.Duration(2 * time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	versionFleet(e)
	updaters := []uuid.UUID{uuid.MustParse("00000000-0000-7000-8000-0000000fff01"), uuid.MustParse("00000000-0000-7000-8000-0000000fff02")}
	ids := e.addAgents(3, "f", inSync(), func(i int, a *domain.Agent) {
		if i < 2 {
			a.Updater = &domain.UpdaterLink{AgentID: updaters[i], Online: true}
		}
	})
	for _, u := range updaters {
		e.st.PutAgent(domain.Agent{ID: u, Kind: domain.AgentKindUpdater, Connection: domain.AgentConnection{Status: domain.ConnectionOnline}})
	}
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.noInstall[ids[0]] = true
	e.noInstall[ids[1]] = true
	e.noInstall[ids[2]] = true
	e.step()
	// agent 0's updater reports INSTALL_FAILED for the offered version; agent 1's reports an
	// old failure (before the attempt started) which must be ignored; agent 2 is silent.
	now := e.clk.Now()
	e.st.UpdateAgent(updaters[0], func(a *domain.Agent) {
		a.PackageStatus = &domain.PackageStatuses{UpdatedAt: now, Packages: map[string]*domain.PackageStatus{"": {
			Name: "", Status: domain.PackageInstallFailed, ServerOfferedVersion: "v0.162.0", ErrorMessage: "sha256 mismatch",
		}}}
	})
	e.st.UpdateAgent(updaters[1], func(a *domain.Agent) {
		a.PackageStatus = &domain.PackageStatuses{UpdatedAt: now.Add(-time.Hour), Packages: map[string]*domain.PackageStatus{"": {
			Name: "", Status: domain.PackageInstallFailed, ServerOfferedVersion: "v0.162.0", ErrorMessage: "old",
		}}}
	})
	e.step()
	tg := e.targetOf(r.ID, ids[0])
	assert.Equal(t, domain.TargetFailed, tg.Status)
	assert.Equal(t, "myriarch-updater reported INSTALL_FAILED: sha256 mismatch", tg.LastError)
	assert.Equal(t, domain.TargetInProgress, e.targetOf(r.ID, ids[1]).Status, "stale failure ignored")

	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	for _, i := range []int{1, 2} {
		got := e.targetOf(r.ID, ids[i])
		assert.Equal(t, domain.TargetFailed, got.Status)
		assert.Contains(t, got.LastError, "timed out: the version 0.162.0 was not reported within 2m0s")
	}
}

func TestVersionVerifyingNeedsHealthyOnlineAgentOnTheNewVersion(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(3, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	require.Equal(t, 3, e.targetStatus(r.ID)[domain.TargetVerifying])
	e.unhealthy[ids[0]] = true
	e.st.UpdateAgent(ids[1], func(a *domain.Agent) { a.Version = "0.161.0" }) // flipped back by something else
	delete(e.exec.installs, ids[1])
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[0]).Status)
	assert.Contains(t, e.targetOf(r.ID, ids[1]).LastError, "agent reports version 0.161.0 instead of 0.162.0")
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, ids[2]).Status)
}

// After an update the new version is reported before the new process reports
// its health: the unhealthy state of the old (stopping) process must not fail
// the target; a fresh unhealthy report still does.
func TestVersionStaleHealthOfTheOldProcessDoesNotFailTheTarget(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(2, "f", inSync())
	e.staleHealth[ids[0]] = true
	e.staleHealth[ids[1]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(4)
	require.Equal(t, 2, e.targetStatus(r.ID)[domain.TargetVerifying], "stale unhealthy health keeps the targets verifying")
	// the new process reports healthy (agent 0) / unhealthy (agent 1)
	delete(e.staleHealth, ids[0])
	delete(e.staleHealth, ids[1])
	e.unhealthy[ids[1]] = true
	require.True(t, e.runUntil(60, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, ids[0]).Status)
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[1]).Status)
}

// The real supervisor reports the new version with {healthy:false}, then the
// collector's StatusStarting (a fresh report after the version), then OK. The
// start-up state must not fail the target; a process that never becomes
// healthy times out.
func TestVersionStartupHealthAfterVersionReportDoesNotFailTheTarget(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(time.Minute)
	s.ApplyTimeout = domain.Duration(10 * time.Minute)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 100}
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(2, "f", inSync())
	e.starting[ids[0]] = true
	e.starting[ids[1]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(6)
	require.Equal(t, 2, e.targetStatus(r.ID)[domain.TargetVerifying], "StatusStarting keeps the targets verifying: %v", e.targetStatus(r.ID))
	delete(e.starting, ids[0]) // becomes healthy; agent 1 never does
	require.True(t, e.runUntil(300, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetSucceeded, e.targetOf(r.ID, ids[0]).Status)
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[1]).Status)
}

// An agent that stays offline is skipped once; it must not create a new
// rollout (and notifications) after every completed one. It is planned again
// once it has connected.
func TestVersionOfflineAgentDoesNotCauseAnEndlessChainOfRollouts(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.OfflineGracePeriod = domain.Duration(2 * time.Minute)
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(3, "f", inSync())
	e.offline[ids[0]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	require.True(t, e.runUntil(200, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, domain.TargetSkipped, e.targetOf(r.ID, ids[0]).Status)

	for i := 0; i < 5; i++ {
		e.steps(30)
		e.plan()
	}
	assert.Len(t, e.rollouts(), 1, "no new rollout while the agent stays offline: %v", e.notif.events())
	assert.Equal(t, []notify.EventType{notify.EventRolloutStarted, notify.EventRolloutCompleted}, e.notif.events())

	// back online: it gets its own rollout
	delete(e.offline, ids[0])
	e.steps(2)
	e.plan()
	assert.Len(t, e.rollouts(), 2)
}

func TestVersionSupersedeCancelsExecutor(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 2
	e.setPolicy("p", s)
	versionFleet(e)
	e.addAgents(5, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(2)
	e.fleets[0].Collector.Version = "0.163.0"
	e.commit = "commit-2"
	e.publishSnapshot()
	e.plan()
	old := e.rollout(r.ID)
	assert.Equal(t, domain.RolloutSuperseded, old.Status)
	require.Len(t, e.exec.cancelCalls, 1)
	cr := e.exec.cancelCalls[0]
	assert.Equal(t, r.ID, cr.RolloutID)
	assert.Len(t, cr.AgentIDs, 5, "all agents with open targets")
	assert.Equal(t, domain.ExecutorUpdater, cr.Spec.Executor)
	rs := e.rollouts()
	require.Len(t, rs, 2)
	assert.Equal(t, "0.163.0", rs[1].ToRef)
	assert.Equal(t, domain.RolloutRunning, rs[1].Status)

	// Removing the version (or the executor) from the fleet supersedes without a new rollout.
	e.fleets[0].Collector.Version = ""
	e.commit = "commit-3"
	e.publishSnapshot()
	e.plan()
	assert.Equal(t, domain.RolloutSuperseded, e.rollout(rs[1].ID).Status)
	assert.Contains(t, e.rollout(rs[1].ID).StatusReason, "no longer manages the collector version")
	assert.Len(t, e.rollouts(), 2)
}

func TestVersionAbortCancelsExecutor(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.MaxConcurrent = 2
	e.setPolicy("p", s)
	versionFleet(e)
	e.addAgents(4, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(2)
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionAbort, 0, "", operator)
	require.NoError(t, err)
	require.Len(t, e.exec.cancelCalls, 1)
	assert.Len(t, e.exec.cancelCalls[0].AgentIDs, 4)
	assert.Equal(t, 4, e.rollout(r.ID).Counts.Cancelled)
}

func TestVersionRollbackStartsTheOldVersion(t *testing.T) {
	e := newEnv(t)
	s := spec(domain.WaveSpec{Name: "first", Percent: 50}, allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
	s.OnFailure = domain.FailureRollback
	s.HealthWait = domain.Duration(time.Minute)
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(6, "f", inSync())
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.steps(3)
	// one of the updated agents becomes unhealthy during its health wait -> automatic rollback
	e.unhealthy[ids[0]] = true
	var first domain.AgentID
	for _, tg := range e.targets(r.ID) {
		if tg.Wave == 0 {
			first = tg.AgentID
			break
		}
	}
	e.unhealthy[first] = true
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutRolledBack }), "status %s", e.rollout(r.ID).Status)
	// The executor was asked to install the OLD version for every touched agent, exactly once each.
	var rollbackReqs []executor.UpdateRequest
	for _, c := range e.exec.startCalls {
		if c.ToVersion == "0.161.0" {
			rollbackReqs = append(rollbackReqs, c)
		}
	}
	require.Len(t, rollbackReqs, 1, "grouped by previous version, sent once")
	assert.Len(t, rollbackReqs[0].Targets, 3, "the three started targets of the first wave")
	for _, id := range ids {
		assert.Equal(t, "0.161.0", e.st.Agent(id).Version)
	}
	st := e.targetStatus(r.ID)
	assert.Equal(t, 3, st[domain.TargetRolledBack], "%v", st)
	assert.Equal(t, 3, st[domain.TargetCancelled], "%v", st)
	assert.Contains(t, e.notif.events(), notify.EventRolloutRolledBack)
}

func TestVersionRolloutAfterAbortAndFailureIsExcluded(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 1}
	e.setPolicy("p", s)
	versionFleet(e)
	ids := e.addAgents(3, "f", inSync())
	e.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
		if tg.AgentID == ids[0] {
			return executor.TargetResult{AgentID: tg.AgentID, Error: "boom"}
		}
		return executor.TargetResult{AgentID: tg.AgentID, Started: true}
	}
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()
	require.Equal(t, domain.RolloutPaused, e.rollout(r.ID).Status)
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionAbort, 0, "", operator)
	require.NoError(t, err)
	e.plan()
	rs := e.rollouts()
	assert.Len(t, rs, 1, "the failed agent is excluded, the other ones were cancelled by an operator abort")
}

func TestRetryLaterKeepsTargetsPendingWithoutFailing(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	ids := e.addAgents(3, "f", inSync())
	var calls int
	e.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
		if calls < 3 && tg.AgentID == ids[0] {
			return executor.TargetResult{AgentID: tg.AgentID, RetryLater: true} // artifact still downloading
		}
		return executor.TargetResult{AgentID: tg.AgentID, Started: true}
	}
	e.publishSnapshot()
	e.plan()
	r := e.only()
	for calls = 0; calls < 3; calls++ {
		e.step()
		assert.Equal(t, domain.TargetPending, e.targetOf(r.ID, ids[0]).Status, "call %d", calls)
		assert.Equal(t, domain.RolloutRunning, e.rollout(r.ID).Status)
	}
	require.True(t, e.runUntil(100, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	assert.Equal(t, 3, e.rollout(r.ID).Counts.Succeeded)

	// If ALL targets wait for their artifact, the rollout is not "failed" either.
	e2 := newEnv(t)
	e2.setPolicy("p", spec(allWave()))
	versionFleet(e2)
	e2.addAgents(2, "f", inSync())
	e2.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
		return executor.TargetResult{AgentID: tg.AgentID, RetryLater: true}
	}
	e2.publishSnapshot()
	e2.plan()
	r2 := e2.only()
	e2.steps(5)
	assert.Equal(t, domain.RolloutRunning, e2.rollout(r2.ID).Status)
	assert.Equal(t, 2, e2.targetStatus(r2.ID)[domain.TargetPending])
}

func TestAbortPassesExecutorRefsToTheExecutor(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(time.Hour)
	e.setPolicy("p", s)
	versionFleet(e, func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorAWX, ExecutorRef: "awx", Params: map[string]any{"k": "v"}}
	})
	e.executors = []domain.Executor{{ObjectMeta: domain.ObjectMeta{Name: "awx"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorAWX, AWX: &domain.AWXExecutorSpec{URL: "https://awx.example.com", JobTemplateID: 1, TokenEnv: "X"}}}}
	ids := e.addAgents(2, "f", inSync())
	e.exec.result = func(req executor.UpdateRequest, tg executor.Target) executor.TargetResult {
		return executor.TargetResult{AgentID: tg.AgentID, Started: true, ExternalRef: "awx-job:5"}
	}
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()
	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionAbort, 0, "", operator)
	require.NoError(t, err)
	require.Len(t, e.exec.cancelCalls, 1)
	cr := e.exec.cancelCalls[0]
	assert.Equal(t, "awx-job:5", cr.Refs[ids[0]])
	assert.Equal(t, "v", cr.Spec.Params["k"], "the fleet's params stay")
	require.NotNil(t, cr.Definition)
	assert.Equal(t, "awx", cr.Definition.Name)
}
