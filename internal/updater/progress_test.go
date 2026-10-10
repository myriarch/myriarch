// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stepsOf returns name -> status of an attempt snapshot.
func stepsOf(a *attemptRecord) map[string]string {
	m := map[string]string{}
	for _, s := range a.Steps {
		m[s.Name] = s.Status
	}
	return m
}

// newTracked wires an attempt tracker into the installer of a test env.
func newTracked(t *testing.T, e *env) *attemptTracker {
	t.Helper()
	tr := newAttemptTracker(e.cfg.StateDir, func() time.Time { return time.Now().UTC() }, nil)
	e.inst.Progress = tr
	return tr
}

func TestProgressSteps(t *testing.T) {
	skipOnWindows(t)
	ctx := context.Background()

	type tc struct {
		name      string
		setup     func(e *env)
		req       func(e *env) InstallRequest
		wantErr   string
		wantPhase string
		wantSteps map[string]string
		unhealthy bool
		from      string
	}
	ok := func(e *env) InstallRequest { return e.request("0.163.0", collectorArchive(t, "0.163.0")) }
	cases := []tc{
		{
			name: "success", setup: func(e *env) { e.installOld("0.162.0") }, req: ok,
			wantPhase: OutcomeSucceeded, from: "v0.162.0",
			wantSteps: map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "done"},
		},
		{
			name: "first install without previous version", setup: func(e *env) {}, req: ok,
			wantPhase: OutcomeSucceeded, from: "",
			wantSteps: map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "done"},
		},
		{
			name: "policy rejects a downgrade", setup: func(e *env) { e.installOld("0.165.0") },
			req:       ok,
			wantErr:   "downgrades are disabled",
			wantPhase: OutcomeFailed, from: "v0.165.0",
			wantSteps: map[string]string{"verifying": "failed", "downloading": "skipped", "installing": "skipped", "restarting": "skipped", "health_check": "skipped"},
		},
		{
			name: "sha mismatch", setup: func(e *env) { e.installOld("0.162.0") },
			req: func(e *env) InstallRequest {
				r := ok(e)
				r.ContentHash = sha([]byte("other"))
				return r
			},
			wantErr: "SHA-256 mismatch", wantPhase: OutcomeFailed, from: "v0.162.0",
			wantSteps: map[string]string{"downloading": "done", "verifying": "failed", "installing": "skipped", "restarting": "skipped", "health_check": "skipped"},
		},
		{
			name: "preflight rejects the binary", setup: func(e *env) { e.installOld("0.162.0") },
			req: func(e *env) InstallRequest {
				return e.request("0.163.0", collectorArchive(t, "0.164.0")) // reports another version
			},
			wantErr: "preflight", wantPhase: OutcomeFailed, from: "v0.162.0",
			wantSteps: map[string]string{"downloading": "done", "verifying": "failed", "installing": "skipped", "restarting": "skipped", "health_check": "skipped"},
		},
		{
			name: "health failure rolls back", setup: func(e *env) {
				e.installOld("0.162.0")
				e.healthy = func(active string) bool { return active != "0.163.0" }
				e.cfg.Update.HealthTimeout = Duration(300 * time.Millisecond)
				e.rebuild()
			},
			req: ok, wantErr: "rolled back to v0.162.0", wantPhase: OutcomeRolledBack, from: "v0.162.0",
			wantSteps: map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "failed", "rolling_back": "done"},
		},
		{
			name: "rollback fails too", setup: func(e *env) {
				e.installOld("0.162.0")
				e.healthy = func(string) bool { return false }
				e.cfg.Update.HealthTimeout = Duration(150 * time.Millisecond)
				e.rebuild()
			},
			req: ok, wantErr: "also failed", wantPhase: OutcomeFailed, unhealthy: true, from: "v0.162.0",
			wantSteps: map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "failed", "rolling_back": "failed"},
		},
		{
			name: "no previous version to roll back to", setup: func(e *env) {
				e.healthy = func(string) bool { return false }
				e.cfg.Update.HealthTimeout = Duration(100 * time.Millisecond)
				e.rebuild()
			},
			req: ok, wantErr: "no previous version", wantPhase: OutcomeFailed, unhealthy: true,
			wantSteps: map[string]string{"downloading": "done", "verifying": "done", "installing": "done", "restarting": "done", "health_check": "failed"},
		},
		{
			name: "supervisor stop fails, nothing changed", setup: func(e *env) {
				e.installOld("0.162.0")
				first := true
				e.svc.OnStop = func(string) error {
					if first {
						first = false
						return errors.New("stop denied")
					}
					return nil
				}
			},
			req: ok, wantErr: "stop supervisor service", wantPhase: OutcomeFailed, from: "v0.162.0",
			wantSteps: map[string]string{"downloading": "done", "verifying": "done", "installing": "failed", "restarting": "skipped", "health_check": "skipped"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "symlink")
			c.setup(e)
			tr := newTracked(t, e)
			err := e.inst.Install(ctx, c.req(e))
			if c.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.wantErr)
			}
			a := tr.snapshot()
			require.NotNil(t, a)
			assert.True(t, a.Final)
			assert.Equal(t, c.wantPhase, a.Phase)
			assert.Equal(t, c.from, a.From)
			assert.Equal(t, c.unhealthy, a.Unhealthy)
			assert.Equal(t, c.wantSteps, stepsOf(a))
			if c.wantErr == "" {
				assert.Empty(t, a.Error)
			} else {
				assert.NotEmpty(t, a.Error)
			}
			for _, s := range a.Steps {
				assert.False(t, s.Started.IsZero(), "step %s has a start time", s.Name)
				assert.False(t, s.Finished.IsZero(), "step %s is finished", s.Name)
				assert.False(t, s.Finished.Before(s.Started), "step %s ends after it started", s.Name)
			}
		})
	}
}

func TestProgressAlreadyActiveIdenticalBinary(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.163.0")
	// the archive holds the very script that installOld wrote
	tr := newTracked(t, e)
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	a := tr.snapshot()
	assert.Equal(t, OutcomeSucceeded, a.Phase)
	assert.Equal(t, map[string]string{"downloading": "done", "verifying": "done", "installing": "skipped", "restarting": "skipped", "health_check": "skipped"}, stepsOf(a))
	assert.Empty(t, e.svc.Calls(), "no service restart for an identical binary")
}

func TestProgressReportsLiveStepOrder(t *testing.T) {
	skipOnWindows(t)
	e := newEnv(t, "symlink")
	e.installOld("0.162.0")
	var phases []string
	tr := newAttemptTracker(e.cfg.StateDir, func() time.Time { return time.Now().UTC() }, nil)
	tr.onChange = func() {
		if a := tr.snapshot(); a != nil && (len(phases) == 0 || phases[len(phases)-1] != a.Phase) {
			phases = append(phases, a.Phase)
		}
	}
	e.inst.Progress = tr
	require.NoError(t, e.inst.Install(context.Background(), e.request("0.163.0", collectorArchive(t, "0.163.0"))))
	assert.Equal(t, []string{"verifying", "downloading", "verifying", "installing", "restarting", "health_check", "succeeded"}, phases)
}

func TestAttemptHealthComponentShape(t *testing.T) {
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clock := start
	tr := newAttemptTracker(t.TempDir(), func() time.Time { return clock }, nil)
	tick := func() { clock = clock.Add(2 * time.Second) }

	tr.Begin("0.162.0", "0.161.0")
	tick()
	tr.Step(StepDownloading, StepRunning, nil)
	tick()
	tr.Step(StepDownloading, StepDone, nil)
	tick()
	tr.Step(StepVerifying, StepRunning, nil)
	tick()
	tr.Step(StepVerifying, StepFailed, errors.New(strings.Repeat("x", 3000)))
	tick()
	tr.End(OutcomeFailed, errors.New(strings.Repeat("y", 3000)), true)

	c := tr.snapshot().healthComponent()
	assert.Equal(t, "failed", c.Status)
	assert.True(t, c.Healthy, "a failure before the swap leaves the host healthy")
	assert.Len(t, c.LastError, maxAttemptErrorLen)
	assert.Equal(t, uint64(start.UnixNano()), c.StartTimeUnixNano)
	assert.Equal(t, uint64(start.Add(10*time.Second).UnixNano()), c.StatusTimeUnixNano)

	m := c.ComponentHealthMap
	require.Contains(t, m, "version")
	assert.Equal(t, "v0.162.0", m["version"].Status)
	assert.Equal(t, "v0.161.0", m["from"].Status)
	dl := m["step:downloading"]
	require.NotNil(t, dl)
	assert.Equal(t, "done", dl.Status)
	assert.Equal(t, uint64(start.Add(2*time.Second).UnixNano()), dl.StartTimeUnixNano)
	assert.Equal(t, uint64(start.Add(4*time.Second).UnixNano()), dl.StatusTimeUnixNano, "status time = end of a finished step")
	v := m["step:verifying"]
	assert.Equal(t, "failed", v.Status)
	assert.False(t, v.Healthy)
	assert.Len(t, v.LastError, maxAttemptErrorLen)
	assert.Equal(t, "skipped", m["step:installing"].Status)
	assert.NotContains(t, m, "step:rolling_back", "rolling_back only exists after a rollback")

	// Unhealthy only when the rollback failed.
	tr.Begin("0.163.0", "0.162.0")
	tr.End(OutcomeFailed, errors.New("boom"), false)
	assert.False(t, tr.snapshot().healthComponent().Healthy)
}

func TestAttemptRunningStepStatusTimeIsItsStart(t *testing.T) {
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	tr := newAttemptTracker(t.TempDir(), func() time.Time { return clock }, nil)
	tr.Begin("0.162.0", "")
	clock = clock.Add(time.Second)
	tr.Step(StepDownloading, StepRunning, nil)
	c := tr.snapshot().healthComponent()
	assert.Equal(t, "downloading", c.Status)
	assert.Equal(t, "", c.ComponentHealthMap["from"].Status)
	d := c.ComponentHealthMap["step:downloading"]
	assert.Equal(t, "running", d.Status)
	assert.Equal(t, d.StartTimeUnixNano, d.StatusTimeUnixNano)
}

func TestAttemptTrackerRestoresLastAttempt(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Now().UTC() }
	tr := newAttemptTracker(dir, now, nil)
	tr.Begin("0.162.0", "0.161.0")
	tr.Step(StepDownloading, StepRunning, nil)
	tr.Step(StepDownloading, StepDone, nil)
	tr.Step(StepVerifying, StepRunning, nil)
	tr.Step(StepVerifying, StepDone, nil)
	tr.Step(StepInstalling, StepRunning, nil)
	tr.Step(StepInstalling, StepDone, nil)
	tr.Step(StepRestarting, StepRunning, nil)
	tr.Step(StepRestarting, StepDone, nil)
	tr.Step(StepHealthCheck, StepRunning, nil)
	tr.End(OutcomeSucceeded, nil, true)

	// a finished attempt is restored as is
	got := newAttemptTracker(dir, now, nil).snapshot()
	require.NotNil(t, got)
	assert.Equal(t, OutcomeSucceeded, got.Phase)
	assert.True(t, got.Final)
	assert.Equal(t, "v0.162.0", got.Version)
	assert.Equal(t, "done", stepsOf(got)["health_check"], "a step still running when the attempt succeeded is done")

	// an attempt cut off by a restart is closed as failed/interrupted
	tr.Begin("0.163.0", "0.162.0")
	tr.Step(StepDownloading, StepRunning, nil)
	got = newAttemptTracker(dir, now, nil).snapshot()
	require.NotNil(t, got)
	assert.Equal(t, OutcomeFailed, got.Phase)
	assert.True(t, got.Final)
	assert.Contains(t, got.Error, "interrupted")
	assert.Equal(t, "failed", stepsOf(got)["downloading"])
	// and that verdict is persisted
	again := newAttemptTracker(dir, now, nil).snapshot()
	assert.Equal(t, OutcomeFailed, again.Phase)

	// garbage on disk does not break start-up
	require.NoError(t, os.WriteFile(lastAttemptPath(dir), []byte("{not json"), 0o600))
	assert.Nil(t, newAttemptTracker(dir, now, nil).snapshot())
}

func TestAttemptTrackerIgnoresStepsAfterEnd(t *testing.T) {
	tr := newAttemptTracker("", func() time.Time { return time.Now() }, nil)
	tr.Step(StepDownloading, StepRunning, nil) // no attempt yet: ignored
	assert.Nil(t, tr.snapshot())
	tr.Begin("0.162.0", "")
	tr.End(OutcomeSucceeded, nil, true)
	tr.Step(StepDownloading, StepRunning, nil)
	assert.Equal(t, OutcomeSucceeded, tr.snapshot().Phase)
}
