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

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := t0.Add(d); return &t }

// reported is one step child of the update component.
type reported struct {
	status string
	start  time.Duration
	end    time.Duration // status_time
	err    string
}

// updaterWith builds an updater agent that reports an update attempt.
func updaterWith(version, status string, start time.Duration, lastErr string, steps map[string]reported) *domain.Agent {
	comps := map[string]*domain.ComponentHealth{
		domain.UpdaterVersionChild: {Healthy: true, Status: "v" + version},
		domain.UpdaterFromChild:    {Healthy: true, Status: "v0.161.0"},
	}
	for name, s := range steps {
		comps[domain.UpdaterStepPrefix+name] = &domain.ComponentHealth{Healthy: s.status != "failed", Status: s.status, LastError: s.err, StartTime: at(s.start), StatusTime: at(s.end)}
	}
	return &domain.Agent{
		ID: domain.AgentID(uuid.New()), Kind: domain.AgentKindUpdater,
		Health: &domain.AgentHealth{Healthy: true, Components: map[string]*domain.ComponentHealth{
			domain.UpdaterUpdateComponent: {Healthy: status != "failed", Status: status, LastError: lastErr, StartTime: at(start), StatusTime: at(start), Components: comps},
		}},
	}
}

func withPackage(a *domain.Agent, status domain.PackageInstallStatus, version string, pct float64, errMsg string, updated time.Duration) *domain.Agent {
	a.PackageStatus = &domain.PackageStatuses{UpdatedAt: t0.Add(updated), Packages: map[string]*domain.PackageStatus{"": {
		Name: "", Status: status, ServerOfferedVersion: "v" + version, DownloadPercent: pct, ErrorMessage: errMsg,
	}}}
	return a
}

func versionRollout(exec domain.ExecutorType) *domain.Rollout {
	return &domain.Rollout{ID: uuid.New(), Kind: domain.RolloutKindVersion, Executor: exec, ToRef: "0.162.0"}
}

func target(status domain.TargetStatus, mut ...func(t *domain.RolloutTarget)) *domain.RolloutTarget {
	t := &domain.RolloutTarget{AgentID: domain.AgentID(uuid.New()), Status: status, FromRef: "0.161.0", ToRef: "0.162.0", Attempts: 1, StartedAt: at(0)}
	for _, m := range mut {
		m(t)
	}
	return t
}

type stepView struct {
	Name   domain.UpdateStepName
	Status domain.UpdateStepStatus
}

func stepsOf(p *domain.TargetProgress) []stepView {
	out := make([]stepView, len(p.Steps))
	for i, s := range p.Steps {
		out[i] = stepView{s.Name, s.Status}
	}
	return out
}

func sv(name domain.UpdateStepName, status domain.UpdateStepStatus) stepView {
	return stepView{name, status}
}

func TestProgressOfTargetsWithoutProgress(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	for _, st := range []domain.TargetStatus{domain.TargetPending, domain.TargetWaitingWindow} {
		assert.Nil(t, computeProgress(r, target(st), nil, nil, t0), "%s targets have not started", st)
	}
	assert.Nil(t, computeProgress(r, target(domain.TargetSkipped, func(x *domain.RolloutTarget) { x.StartedAt = nil }), nil, nil, t0), "never started")
	assert.Nil(t, computeProgress(r, target(domain.TargetCancelled, func(x *domain.RolloutTarget) { x.StartedAt = nil }), nil, nil, t0))
	cfg := versionRollout(domain.ExecutorUpdater)
	cfg.Kind = domain.RolloutKindConfig
	assert.Nil(t, computeProgress(cfg, target(domain.TargetInProgress), nil, nil, t0), "config rollouts have no steps")
}

func TestProgressWithoutHostSteps(t *testing.T) {
	for _, exec := range []domain.ExecutorType{domain.ExecutorWebhook, domain.ExecutorAWX, domain.ExecutorManual} {
		r := versionRollout(exec)
		p := computeProgress(r, target(domain.TargetInProgress), nil, nil, t0.Add(time.Minute))
		require.NotNil(t, p, exec)
		assert.Equal(t, domain.ProgressExecutor, p.Source)
		assert.Equal(t, domain.PhaseInstalling, p.Phase)
		assert.Equal(t, []stepView{sv("offered", "done"), sv("installing", "running"), sv("health_wait", "pending")}, stepsOf(p))
		assert.Equal(t, "external update, waiting for version 0.162.0", p.Steps[1].Detail)
		assert.Equal(t, 1, p.Attempt)
		assert.Equal(t, "0.161.0", p.FromVersion)
		assert.Equal(t, "0.162.0", p.ToVersion)
		assert.True(t, p.UpdatedAt.Equal(t0.Add(time.Minute)))
	}
	r := versionRollout(domain.ExecutorWebhook)
	ver := target(domain.TargetVerifying, func(x *domain.RolloutTarget) { x.VerifiedAt = at(time.Minute) })
	p := computeProgress(r, ver, nil, nil, t0)
	assert.Equal(t, domain.PhaseHealthWait, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("installing", "done"), sv("health_wait", "running")}, stepsOf(p))
	assert.True(t, p.Steps[1].FinishedAt.Equal(t0.Add(time.Minute)))
	assert.True(t, p.Steps[2].StartedAt.Equal(t0.Add(time.Minute)))

	done := target(domain.TargetSucceeded, func(x *domain.RolloutTarget) { x.VerifiedAt, x.FinishedAt = at(time.Minute), at(2*time.Minute) })
	p = computeProgress(r, done, nil, nil, t0)
	assert.Equal(t, domain.PhaseSucceeded, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("installing", "done"), sv("health_wait", "done")}, stepsOf(p))
	assert.True(t, p.Steps[2].FinishedAt.Equal(t0.Add(2*time.Minute)))
	assert.Empty(t, p.Error)

	// the external update never arrived
	timeout := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.FinishedAt, x.LastError = at(10*time.Minute), "timed out: the version 0.162.0 was not reported within 10m0s"
	})
	p = computeProgress(r, timeout, nil, nil, t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("installing", "failed"), sv("health_wait", "skipped")}, stepsOf(p))
	assert.Equal(t, timeout.LastError, p.Error)
	assert.Equal(t, timeout.LastError, p.Steps[1].Detail)

	// the collector reported the version but did not stay healthy
	unhealthy := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.VerifiedAt, x.FinishedAt, x.LastError = at(time.Minute), at(2*time.Minute), "agent became unhealthy during the health wait: pipeline broken"
	})
	p = computeProgress(r, unhealthy, nil, nil, t0)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("installing", "done"), sv("health_wait", "failed")}, stepsOf(p))
	assert.Equal(t, unhealthy.LastError, p.Error)

	// the executor refused it at the start: it never started
	refused := target(domain.TargetFailed, func(x *domain.RolloutTarget) { x.StartedAt, x.LastError = nil, "no myriarch-updater linked" })
	p = computeProgress(r, refused, nil, nil, t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "failed"), sv("installing", "skipped"), sv("health_wait", "skipped")}, stepsOf(p))
	assert.Equal(t, "no myriarch-updater linked", p.Error)
}

func TestProgressFromTheUpdatersStepReports(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	tg := target(domain.TargetInProgress)

	// nothing reported yet: the offer is out
	p := computeProgress(r, tg, nil, &domain.Agent{Kind: domain.AgentKindUpdater, Health: &domain.AgentHealth{Healthy: true}}, t0)
	assert.Equal(t, domain.ProgressPackageStatus, p.Source, "an updater without the component is known by its package status only")
	assert.Equal(t, domain.PhaseOffered, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "running"), sv("health_wait", "pending")}, stepsOf(p))

	// downloading, with the percentage of the package status
	up := updaterWith("0.162.0", "downloading", 5*time.Second, "", map[string]reported{"downloading": {"running", 5 * time.Second, 5 * time.Second, ""}})
	withPackage(up, domain.PackageDownloading, "0.162.0", 42.5, "", 6*time.Second)
	p = computeProgress(r, tg, nil, up, t0)
	assert.Equal(t, domain.ProgressUpdater, p.Source)
	assert.Equal(t, domain.PhaseDownloading, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "running"), sv("verifying", "pending"), sv("installing", "pending"),
		sv("restarting", "pending"), sv("health_check", "pending"), sv("health_wait", "pending")}, stepsOf(p))
	require.NotNil(t, p.DownloadPercent)
	assert.Equal(t, 42.5, *p.DownloadPercent)
	assert.True(t, p.Steps[0].FinishedAt.Equal(t0.Add(5*time.Second)), "offered ends when the updater began")
	assert.True(t, p.Steps[1].StartedAt.Equal(t0.Add(5*time.Second)))
	assert.Nil(t, p.Steps[1].FinishedAt, "a running step has no end")

	// verified, installing, restarting, health check
	steps := map[string]reported{
		"downloading":  {"done", 5 * time.Second, 20 * time.Second, ""},
		"verifying":    {"done", 20 * time.Second, 22 * time.Second, ""},
		"installing":   {"done", 22 * time.Second, 25 * time.Second, ""},
		"restarting":   {"done", 25 * time.Second, 27 * time.Second, ""},
		"health_check": {"running", 27 * time.Second, 27 * time.Second, ""},
	}
	up = updaterWith("0.162.0", "health_check", 5*time.Second, "", steps)
	p = computeProgress(r, tg, nil, up, t0)
	assert.Equal(t, domain.PhaseHealthCheck, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("verifying", "done"), sv("installing", "done"),
		sv("restarting", "done"), sv("health_check", "running"), sv("health_wait", "pending")}, stepsOf(p))
	assert.Nil(t, p.DownloadPercent, "no percentage after the download")
	assert.True(t, p.Steps[2].FinishedAt.Equal(t0.Add(22*time.Second)))

	// the updater is done, the collector has not reported the version yet
	steps["health_check"] = reported{"done", 27 * time.Second, 40 * time.Second, ""}
	up = updaterWith("0.162.0", "succeeded", 5*time.Second, "", steps)
	p = computeProgress(r, tg, nil, up, t0)
	assert.Equal(t, domain.PhaseHealthCheck, p.Phase, "all host steps done, still waiting for the collector's report")

	// the collector reports the version while the updater still polls its health: the target status decides
	up = updaterWith("0.162.0", "health_check", 5*time.Second, "", steps)
	up.Health.Components[domain.UpdaterUpdateComponent].Components["step:health_check"].Status = "running"
	ver := target(domain.TargetVerifying, func(x *domain.RolloutTarget) { x.VerifiedAt = at(30 * time.Second) })
	p = computeProgress(r, ver, nil, up, t0)
	assert.Equal(t, domain.PhaseHealthWait, p.Phase)
	assert.Equal(t, domain.StepDone, p.Steps[5].Status, "health_check resolved")
	assert.True(t, p.Steps[5].FinishedAt.Equal(t0.Add(30*time.Second)))
	assert.Equal(t, domain.StepRunning, p.Steps[6].Status)

	// succeeded
	okT := target(domain.TargetSucceeded, func(x *domain.RolloutTarget) { x.VerifiedAt, x.FinishedAt = at(30*time.Second), at(90*time.Second) })
	p = computeProgress(r, okT, nil, up, t0)
	assert.Equal(t, domain.PhaseSucceeded, p.Phase)
	for _, s := range p.Steps {
		assert.Equal(t, domain.StepDone, s.Status, s.Name)
	}
	assert.Empty(t, p.Error)
}

func TestProgressWhenAHostStepFails(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	steps := map[string]reported{
		"downloading": {"done", 5 * time.Second, 20 * time.Second, ""},
		"verifying":   {"failed", 20 * time.Second, 21 * time.Second, "sigstore verification failed: bad signature"},
	}
	up := updaterWith("0.162.0", "failed", 5*time.Second, "sigstore verification failed: bad signature", steps)
	failed := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.FinishedAt, x.LastError = at(25*time.Second), "myriarch-updater reported INSTALL_FAILED: sigstore verification failed: bad signature"
	})
	p := computeProgress(r, failed, nil, up, t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("verifying", "failed"), sv("installing", "skipped"),
		sv("restarting", "skipped"), sv("health_check", "skipped"), sv("health_wait", "skipped")}, stepsOf(p))
	assert.Equal(t, "sigstore verification failed: bad signature", p.Error, "the error of the failed step")
	assert.Equal(t, "sigstore verification failed: bad signature", p.Steps[2].Detail)
	assert.Nil(t, p.DownloadPercent)
}

func TestProgressOfARealRollback(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	steps := map[string]reported{
		"downloading":  {"done", 5 * time.Second, 20 * time.Second, ""},
		"verifying":    {"done", 20 * time.Second, 22 * time.Second, ""},
		"installing":   {"done", 22 * time.Second, 25 * time.Second, ""},
		"restarting":   {"done", 25 * time.Second, 27 * time.Second, ""},
		"health_check": {"failed", 27 * time.Second, 60 * time.Second, "collector did not become healthy: process exited"},
		"rolling_back": {"done", 60 * time.Second, 70 * time.Second, ""},
	}
	up := updaterWith("0.162.0", "rolled_back", 5*time.Second, "collector did not become healthy: process exited", steps)
	failed := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.FinishedAt, x.LastError = at(75*time.Second), "myriarch-updater reported INSTALL_FAILED: rolled back to v0.161.0"
	})
	p := computeProgress(r, failed, nil, up, t0)
	assert.Equal(t, domain.PhaseRolledBack, p.Phase, "the host runs the previous version again")
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("verifying", "done"), sv("installing", "done"),
		sv("restarting", "done"), sv("health_check", "failed"), sv("rolling_back", "done"), sv("health_wait", "skipped")}, stepsOf(p))
	assert.Equal(t, "collector did not become healthy: process exited", p.Error)

	// while the rollback runs the phase says so
	running := updaterWith("0.162.0", "rolling_back", 5*time.Second, "", map[string]reported{
		"downloading": {"done", 5 * time.Second, 20 * time.Second, ""}, "verifying": {"done", 20 * time.Second, 22 * time.Second, ""},
		"installing": {"done", 22 * time.Second, 25 * time.Second, ""}, "restarting": {"done", 25 * time.Second, 27 * time.Second, ""},
		"health_check": {"failed", 27 * time.Second, 60 * time.Second, "x"}, "rolling_back": {"running", 60 * time.Second, 60 * time.Second, ""},
	})
	p = computeProgress(r, target(domain.TargetInProgress), nil, running, t0)
	assert.Equal(t, domain.PhaseRollingBack, p.Phase)

	// a rollback that did not work: the update component says failed
	broken := updaterWith("0.162.0", "failed", 5*time.Second, "rollback failed", steps)
	p = computeProgress(r, failed, nil, broken, t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase, "no rolled_back phase unless the host really rolled back")
}

func TestProgressFailureWithoutAHostReport(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	// timeout: the updater never reported anything
	timeout := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.FinishedAt, x.LastError = at(30*time.Minute), "timed out: the version 0.162.0 was not reported within 30m0s"
	})
	p := computeProgress(r, timeout, nil, nil, t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase)
	assert.Equal(t, domain.StepFailed, p.Steps[0].Status, "the offer was not taken up")
	assert.Equal(t, timeout.LastError, p.Error)

	// the host finished its steps, the collector crash-looped in the health wait
	steps := map[string]reported{
		"downloading": {"done", 0, 5 * time.Second, ""}, "verifying": {"done", 5 * time.Second, 6 * time.Second, ""},
		"installing": {"done", 6 * time.Second, 8 * time.Second, ""}, "restarting": {"done", 8 * time.Second, 9 * time.Second, ""},
		"health_check": {"done", 9 * time.Second, 15 * time.Second, ""},
	}
	up := updaterWith("0.162.0", "succeeded", 0, "", steps)
	crash := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.VerifiedAt, x.FinishedAt, x.LastError = at(20*time.Second), at(80*time.Second), "collector restarts repeatedly (crash loop)"
	})
	p = computeProgress(r, crash, nil, up, t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase)
	assert.Equal(t, domain.StepFailed, p.Steps[len(p.Steps)-1].Status)
	assert.Equal(t, domain.StepHealthWait, p.Steps[len(p.Steps)-1].Name)
	assert.Equal(t, "collector restarts repeatedly (crash loop)", p.Error)
	for _, s := range p.Steps[:len(p.Steps)-1] {
		assert.Equal(t, domain.StepDone, s.Status, s.Name)
	}

	// the target failed mid-way without the host saying so (e.g. the agent went offline)
	steps2 := map[string]reported{"downloading": {"done", 0, 5 * time.Second, ""}, "verifying": {"running", 5 * time.Second, 5 * time.Second, ""}}
	up2 := updaterWith("0.162.0", "verifying", 0, "", steps2)
	offline := target(domain.TargetFailed, func(x *domain.RolloutTarget) { x.FinishedAt, x.LastError = at(time.Minute), "myriarch-updater offline" })
	p = computeProgress(r, offline, nil, up2, t0)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("verifying", "failed"), sv("installing", "skipped"),
		sv("restarting", "skipped"), sv("health_check", "skipped"), sv("health_wait", "skipped")}, stepsOf(p), "the running step is the one that failed")
	assert.Equal(t, "myriarch-updater offline", p.Error)
}

func TestProgressFromThePackageStatusOnly(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	tg := target(domain.TargetInProgress)
	up := func(status domain.PackageInstallStatus, pct float64, msg string) *domain.Agent {
		return withPackage(&domain.Agent{Kind: domain.AgentKindUpdater}, status, "0.162.0", pct, msg, time.Second)
	}
	p := computeProgress(r, tg, nil, up(domain.PackageDownloading, 63, ""), t0)
	assert.Equal(t, domain.ProgressPackageStatus, p.Source)
	assert.Equal(t, domain.PhaseDownloading, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "running"), sv("installing", "pending"), sv("health_wait", "pending")}, stepsOf(p))
	assert.Equal(t, 63.0, *p.DownloadPercent)

	p = computeProgress(r, tg, nil, up(domain.PackageInstalling, 0, ""), t0)
	assert.Equal(t, domain.PhaseInstalling, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("installing", "running"), sv("health_wait", "pending")}, stepsOf(p))
	assert.Nil(t, p.DownloadPercent)

	p = computeProgress(r, tg, nil, up(domain.PackageInstalled, 0, ""), t0)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("installing", "done"), sv("health_wait", "pending")}, stepsOf(p))

	failed := target(domain.TargetFailed, func(x *domain.RolloutTarget) {
		x.FinishedAt, x.LastError = at(time.Minute), "myriarch-updater reported INSTALL_FAILED: disk full"
	})
	p = computeProgress(r, failed, nil, up(domain.PackageInstallFailed, 0, "disk full"), t0)
	assert.Equal(t, domain.PhaseFailed, p.Phase)
	assert.Equal(t, []stepView{sv("offered", "done"), sv("downloading", "done"), sv("installing", "failed"), sv("health_wait", "skipped")}, stepsOf(p))
	assert.Equal(t, "disk full", p.Error)

	// a package status of another version, or from before the target started, says nothing
	other := withPackage(&domain.Agent{Kind: domain.AgentKindUpdater}, domain.PackageInstalling, "0.161.0", 0, "", time.Second)
	assert.Equal(t, domain.PhaseOffered, computeProgress(r, tg, nil, other, t0).Phase)
	old := withPackage(&domain.Agent{Kind: domain.AgentKindUpdater}, domain.PackageInstalling, "0.162.0", 0, "", -time.Hour)
	assert.Equal(t, domain.PhaseOffered, computeProgress(r, tg, nil, old, t0).Phase)
	install := target(domain.TargetInProgress, func(x *domain.RolloutTarget) { x.StartedAt = at(0) })
	assert.Equal(t, domain.PhaseInstalling, computeProgress(r, install, nil, up(domain.PackageInstalling, 0, ""), t0).Phase)
}

// The attempt of the updater is matched to the target by version and start time.
func TestProgressMatchesAttemptsByVersionAndStartTimeWithClockSkew(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	steps := map[string]reported{"downloading": {"running", 0, 0, ""}}
	tg := target(domain.TargetInProgress, func(x *domain.RolloutTarget) { x.StartedAt = at(10 * time.Minute) })

	cases := []struct {
		name    string
		version string
		start   time.Duration
		matched bool
	}{
		{"same time", "0.162.0", 10 * time.Minute, true},
		{"updater clock a minute behind", "0.162.0", 9 * time.Minute, true},
		{"updater clock 2 minutes behind", "0.162.0", 8 * time.Minute, true},
		{"an older attempt", "0.162.0", 7*time.Minute + 59*time.Second, false},
		{"updater clock ahead", "0.162.0", 12 * time.Minute, true},
		{"another version", "0.163.0", 10 * time.Minute, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := computeProgress(r, tg, nil, updaterWith(c.version, "downloading", c.start, "", steps), t0)
			if c.matched {
				assert.Equal(t, domain.ProgressUpdater, p.Source)
				assert.Equal(t, domain.PhaseDownloading, p.Phase)
			} else {
				assert.Equal(t, domain.PhaseOffered, p.Phase, "not begun as far as this target is concerned")
				assert.Equal(t, domain.ProgressUpdater, p.Source, "the updater reports the component in general")
				assert.Equal(t, domain.StepPending, p.Steps[1].Status)
			}
		})
	}
	// version with and without the "v" of the report
	up := updaterWith("0.162.0", "downloading", 10*time.Minute, "", steps)
	up.Health.Components[domain.UpdaterUpdateComponent].Components[domain.UpdaterVersionChild].Status = "0.162.0"
	assert.Equal(t, domain.PhaseDownloading, computeProgress(r, tg, nil, up, t0).Phase)
}

// A retry offers the same version again: the failed attempt of before is not the new one.
func TestProgressDoesNotShowTheFailedAttemptOfBeforeAfterARetry(t *testing.T) {
	r := versionRollout(domain.ExecutorUpdater)
	failedAttempt := updaterWith("0.162.0", "rolled_back", 0, "collector did not become healthy", map[string]reported{
		"health_check": {"failed", 10 * time.Second, 40 * time.Second, "x"}, "rolling_back": {"done", 40 * time.Second, 50 * time.Second, ""},
	})
	failedAttempt.Health.Components[domain.UpdaterUpdateComponent].StatusTime = at(50 * time.Second)
	retry := target(domain.TargetInProgress, func(x *domain.RolloutTarget) { x.Attempts = 2; x.StartedAt = at(time.Minute) })
	p := computeProgress(r, retry, nil, failedAttempt, t0)
	assert.Equal(t, domain.PhaseOffered, p.Phase, "the updater has not begun the new attempt")
	assert.NotEqual(t, domain.StepFailed, p.Steps[len(p.Steps)-2].Status)

	// the first attempt of a target that already shows a failed host attempt is its own
	first := target(domain.TargetInProgress, func(x *domain.RolloutTarget) { x.StartedAt = at(0) })
	p = computeProgress(r, first, nil, failedAttempt, t0)
	assert.NotEqual(t, domain.PhaseOffered, p.Phase)

	// once the new attempt shows up (later start) it is used
	next := updaterWith("0.162.0", "downloading", 65*time.Second, "", map[string]reported{"downloading": {"running", 65 * time.Second, 65 * time.Second, ""}})
	p = computeProgress(r, retry, nil, next, t0)
	assert.Equal(t, domain.PhaseDownloading, p.Phase)
}

func TestProgressOfTargetsEndedFromOutside(t *testing.T) {
	r := versionRollout(domain.ExecutorWebhook)
	for status, phase := range map[domain.TargetStatus]domain.UpdatePhase{
		domain.TargetCancelled: domain.PhaseCancelled, domain.TargetRolledBack: domain.PhaseRolledBack, domain.TargetSkipped: domain.PhaseCancelled,
	} {
		p := computeProgress(r, target(status, func(x *domain.RolloutTarget) { x.FinishedAt, x.LastError = at(time.Minute), "stopped" }), nil, nil, t0)
		require.NotNil(t, p, status)
		assert.Equal(t, phase, p.Phase, status)
		assert.Equal(t, []stepView{sv("offered", "done"), sv("installing", "skipped"), sv("health_wait", "skipped")}, stepsOf(p), status)
		assert.Equal(t, "stopped", p.Error)
	}
}

func TestProgressErrorTextIsBounded(t *testing.T) {
	r := versionRollout(domain.ExecutorWebhook)
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	p := computeProgress(r, target(domain.TargetFailed, func(x *domain.RolloutTarget) { x.LastError = string(long) }), nil, nil, t0)
	assert.Len(t, p.Error, maxProgressError)
}

// ------------------------------------------------------- engine integration ----

// The engine writes the progress while the update runs, keeps it when the
// target ends, and a retry starts from scratch.
func TestEngineStoresAndFreezesTheProgress(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(10 * time.Second)
	s.FailureThreshold = domain.FailureThreshold{MaxFailed: 5}
	e.setPolicy("p", s)
	versionFleet(e)
	updater := uuid.MustParse("00000000-0000-7000-8000-0000000fff01")
	ids := e.addAgents(2, "f", inSync(), func(i int, a *domain.Agent) {
		if i == 0 {
			a.Updater = &domain.UpdaterLink{AgentID: updater, Online: true}
		}
	})
	e.st.PutAgent(domain.Agent{ID: updater, Kind: domain.AgentKindUpdater, Connection: domain.AgentConnection{Status: domain.ConnectionOnline},
		Health: &domain.AgentHealth{Healthy: true}})
	e.noInstall[ids[0]] = true // the collector changes its version when the updater says so
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()

	prog := func(id domain.AgentID) *domain.TargetProgress { return e.targetOf(r.ID, id).Progress }
	require.NotNil(t, prog(ids[0]))
	assert.Equal(t, domain.PhaseOffered, prog(ids[0]).Phase, "offered, nothing reported yet")
	require.NotNil(t, prog(ids[1]))
	assert.Equal(t, domain.ProgressPackageStatus, prog(ids[1]).Source, "this collector has no updater linked")

	// the updater reports the steps one after the other (state, not events)
	attempt := e.clk.Now().Sub(t0)
	report := func(status string, steps map[string]reported) {
		up := updaterWith("0.162.0", status, attempt, "", steps)
		e.st.UpdateAgent(updater, func(a *domain.Agent) { a.Health = up.Health })
	}
	now := e.clk.Now().Sub(t0)
	report("downloading", map[string]reported{"downloading": {"running", now, now, ""}})
	e.st.UpdateAgent(updater, func(a *domain.Agent) {
		a.PackageStatus = &domain.PackageStatuses{UpdatedAt: e.clk.Now(), Packages: map[string]*domain.PackageStatus{"": {
			Status: domain.PackageDownloading, ServerOfferedVersion: "v0.162.0", DownloadPercent: 37,
		}}}
	})
	e.step()
	p := prog(ids[0])
	assert.Equal(t, domain.PhaseDownloading, p.Phase)
	assert.Equal(t, domain.ProgressUpdater, p.Source)
	require.NotNil(t, p.DownloadPercent)
	assert.Equal(t, 37.0, *p.DownloadPercent)

	now = e.clk.Now().Sub(t0)
	report("health_check", map[string]reported{
		"downloading": {"done", 0, now - 10*time.Second, ""}, "verifying": {"done", now - 10*time.Second, now - 8*time.Second, ""},
		"installing": {"done", now - 8*time.Second, now - 5*time.Second, ""}, "restarting": {"done", now - 5*time.Second, now - 3*time.Second, ""},
		"health_check": {"running", now - 3*time.Second, now - 3*time.Second, ""},
	})
	e.step()
	assert.Equal(t, domain.PhaseHealthCheck, prog(ids[0]).Phase)

	// the collector reports the new version: the health wait of the policy runs
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) { a.Version = "0.162.0" })
	e.step()
	assert.Equal(t, domain.TargetVerifying, e.targetOf(r.ID, ids[0]).Status)
	assert.Equal(t, domain.PhaseHealthWait, prog(ids[0]).Phase)

	require.True(t, e.runUntil(30, func() bool { return e.rollout(r.ID).Status == domain.RolloutCompleted }))
	p = prog(ids[0])
	assert.Equal(t, domain.PhaseSucceeded, p.Phase)
	for _, s := range p.Steps {
		assert.Equal(t, domain.StepDone, s.Status, s.Name)
	}
	frozen := *p

	// further ticks and an unrelated change of the updater do not touch the finished progress
	e.st.UpdateAgent(updater, func(a *domain.Agent) { a.Health.Components[domain.UpdaterUpdateComponent].Status = "downloading" })
	e.steps(3)
	assert.True(t, frozen.SameContent(prog(ids[0])), "frozen at the final status")
	assert.Equal(t, domain.PhaseSucceeded, prog(ids[1]).Phase)
}

func TestEngineProgressOfARolledBackUpdateAndRetry(t *testing.T) {
	e := newEnv(t)
	e.setPolicy("p", spec(allWave()))
	versionFleet(e)
	updater := uuid.MustParse("00000000-0000-7000-8000-0000000fff01")
	ids := e.addAgents(1, "f", inSync(), func(i int, a *domain.Agent) { a.Updater = &domain.UpdaterLink{AgentID: updater, Online: true} })
	e.st.PutAgent(domain.Agent{ID: updater, Kind: domain.AgentKindUpdater, Connection: domain.AgentConnection{Status: domain.ConnectionOnline}, Health: &domain.AgentHealth{Healthy: true}})
	e.noInstall[ids[0]] = true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()

	now := e.clk.Now().Sub(t0)
	rolled := updaterWith("0.162.0", "rolled_back", now, "collector did not become healthy", map[string]reported{
		"downloading": {"done", now, now, ""}, "verifying": {"done", now, now, ""}, "installing": {"done", now, now, ""}, "restarting": {"done", now, now, ""},
		"health_check": {"failed", now, now, "collector did not become healthy"}, "rolling_back": {"done", now, now, ""},
	})
	rolled.PackageStatus = &domain.PackageStatuses{UpdatedAt: e.clk.Now(), Packages: map[string]*domain.PackageStatus{"": {
		Status: domain.PackageInstallFailed, ServerOfferedVersion: "v0.162.0", ErrorMessage: "rolled back to v0.161.0: collector did not become healthy",
	}}}
	e.st.UpdateAgent(updater, func(a *domain.Agent) { a.Health, a.PackageStatus = rolled.Health, rolled.PackageStatus })
	e.step()

	tg := e.targetOf(r.ID, ids[0])
	assert.Equal(t, domain.TargetFailed, tg.Status)
	require.NotNil(t, tg.Progress)
	assert.Equal(t, domain.PhaseRolledBack, tg.Progress.Phase)
	assert.Equal(t, domain.StepFailed, tg.Progress.Steps[5].Status)
	assert.Equal(t, "collector did not become healthy", tg.Progress.Error)
	assert.Equal(t, domain.RolloutPaused, e.rollout(r.ID).Status, "onFailure pause")

	// retry-failed: the target starts over without the progress of the failed attempt
	e.st.UpdateAgent(updater, func(a *domain.Agent) { a.Health, a.PackageStatus = &domain.AgentHealth{Healthy: true}, nil })
	_, err := e.st.Rollouts().ResetFailedTargets(e.ctx, r.ID)
	require.NoError(t, err)
	assert.Nil(t, e.targetOf(r.ID, ids[0]).Progress)
}

// Targets that an abort ends from outside keep what was known, settled.
func TestEngineAbortFreezesTheProgressOfInFlightTargets(t *testing.T) {
	e := newEnv(t)
	e.exec.typ = domain.ExecutorWebhook
	e.setPolicy("p", spec(allWave()))
	versionFleet(e, func(f *domain.Fleet) {
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorWebhook, ExecutorRef: "hook"}
	})
	e.executors = []domain.Executor{{ObjectMeta: domain.ObjectMeta{Name: "hook"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook, Webhook: &domain.WebhookExecutorSpec{URL: "https://ci.example.com/hook"}}}}
	ids := e.addAgents(2, "f", inSync())
	e.noInstall[ids[0]], e.noInstall[ids[1]] = true, true
	e.publishSnapshot()
	e.plan()
	r := e.only()
	e.step()
	require.NotNil(t, e.targetOf(r.ID, ids[0]).Progress)
	assert.Equal(t, domain.PhaseInstalling, e.targetOf(r.ID, ids[0]).Progress.Phase)

	_, err := e.eng.Action(e.ctx, r.ID, domain.ActionAbort, 0, "stop", nil)
	require.NoError(t, err)
	for _, id := range ids {
		tg := e.targetOf(r.ID, id)
		assert.Equal(t, domain.TargetCancelled, tg.Status)
		require.NotNil(t, tg.Progress)
		assert.Equal(t, domain.PhaseCancelled, tg.Progress.Phase, "stored settled")
		assert.Equal(t, domain.StepSkipped, tg.Progress.Steps[1].Status)
	}
}

// The engine fails the target on an unhealthy collector while the updater is
// still in its own health check: the progress keeps following the host until
// the updater ended the attempt, so the UI sees the rollback (D-08).
func TestEngineProgressFollowsTheHostRollbackAfterTheTargetFailed(t *testing.T) {
	e := newEnv(t)
	s := spec(allWave())
	s.HealthWait = domain.Duration(30 * time.Second)
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
	base := map[string]reported{
		"downloading": {"done", attempt, attempt, ""}, "verifying": {"done", attempt, attempt, ""},
		"installing": {"done", attempt, attempt, ""}, "restarting": {"done", attempt, attempt, ""},
		"health_check": {"running", attempt, attempt, ""},
	}
	report("health_check", base)
	// the new collector reports its version and is unhealthy
	e.unhealthy[ids[0]] = true
	e.st.UpdateAgent(ids[0], func(a *domain.Agent) { a.Version = "0.162.0" })
	require.True(t, e.runUntil(30, func() bool { return e.targetOf(r.ID, ids[0]).Status == domain.TargetFailed }))
	p := e.targetOf(r.ID, ids[0]).Progress
	require.NotNil(t, p)
	assert.Equal(t, domain.PhaseFailed, p.Phase, "the engine gave up, the host still checks")
	assert.Equal(t, domain.StepRunning, stepsOf(p)[5].Status, "health_check keeps running")

	// the updater rolls back later
	rb := map[string]reported{}
	for k, v := range base {
		rb[k] = v
	}
	rb["health_check"] = reported{"failed", attempt, attempt, "collector did not become healthy"}
	rb["rolling_back"] = reported{"running", attempt, attempt, ""}
	report("rolling_back", rb)
	e.step()
	p = e.targetOf(r.ID, ids[0]).Progress
	assert.Equal(t, domain.StepRunning, stepsOf(p)[6].Status, "rolling_back is visible while it runs")

	rb["rolling_back"] = reported{"done", attempt, attempt, ""}
	report("rolled_back", rb)
	e.step()
	p = e.targetOf(r.ID, ids[0]).Progress
	assert.Equal(t, domain.PhaseRolledBack, p.Phase)
	assert.Equal(t, domain.StepFailed, stepsOf(p)[5].Status)
	assert.Equal(t, domain.StepDone, stepsOf(p)[6].Status)
	assert.Equal(t, domain.TargetFailed, e.targetOf(r.ID, ids[0]).Status)
	frozen := *p
	e.steps(3)
	assert.True(t, frozen.SameContent(e.targetOf(r.ID, ids[0]).Progress), "frozen once the attempt ended")
}
