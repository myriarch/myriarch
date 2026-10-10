// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- systemd (Linux code path, fake systemctl) --------------------------

type fakeSystemctl struct {
	mu    sync.Mutex
	calls []string
	reply func(args []string) ([]byte, error)
}

func (f *fakeSystemctl) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	f.mu.Unlock()
	return f.reply(args)
}

func TestSystemdControllerCommands(t *testing.T) {
	f := &fakeSystemctl{reply: func(args []string) ([]byte, error) { return nil, nil }}
	c := &SystemdController{Run: f.run}
	ctx := context.Background()
	require.NoError(t, c.Stop(ctx, "otelcol-supervisor"))
	require.NoError(t, c.Start(ctx, "otelcol-supervisor"))
	require.NoError(t, c.Stop(ctx, "custom.service"))
	assert.Equal(t, []string{
		"systemctl stop otelcol-supervisor.service",
		"systemctl start otelcol-supervisor.service",
		"systemctl stop custom.service",
	}, f.calls)
}

func TestSystemdControllerErrorsAreWrapped(t *testing.T) {
	f := &fakeSystemctl{reply: func(args []string) ([]byte, error) { return nil, errors.New("Failed to stop: Unit not loaded") }}
	c := &SystemdController{Run: f.run}
	err := c.Stop(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "systemctl stop x")
	assert.Contains(t, err.Error(), "Unit not loaded")
}

func TestSystemdControllerState(t *testing.T) {
	cases := map[string]ServiceState{
		"active\n": ServiceRunning, "activating\n": ServiceStarting, "deactivating\n": ServiceStopping,
		"inactive\n": ServiceStopped, "failed\n": ServiceStopped, "weird\n": ServiceUnknown,
	}
	for out, want := range cases {
		f := &fakeSystemctl{reply: func(args []string) ([]byte, error) {
			// is-active exits non-zero for everything but active
			if out != "active\n" {
				return []byte(out), errors.New("exit status 3")
			}
			return []byte(out), nil
		}}
		c := &SystemdController{Run: f.run}
		got, err := c.State(context.Background(), "u")
		require.NoError(t, err, out)
		assert.Equal(t, want, got, out)
	}
	// no output + error = real failure (e.g. systemctl missing)
	f := &fakeSystemctl{reply: func([]string) ([]byte, error) { return nil, errors.New("executable file not found") }}
	_, err := (&SystemdController{Run: f.run}).State(context.Background(), "u")
	require.Error(t, err)
}

func TestSystemdControllerExists(t *testing.T) {
	for out, want := range map[string]bool{"loaded\n": true, "not-found\n": false, "masked\n": false} {
		f := &fakeSystemctl{reply: func([]string) ([]byte, error) { return []byte(out), nil }}
		got, err := (&SystemdController{Run: f.run}).Exists(context.Background(), "u")
		require.NoError(t, err)
		assert.Equal(t, want, got, out)
	}
}

func TestSystemdControllerEnvironment(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "myriarch.env")
	require.NoError(t, os.WriteFile(envFile, []byte("# comment\nMYRIARCH_ENROLLMENT_TOKEN=myre_x\nOTEL_GATEWAY_TOKEN=\"quoted value\"\n\nBAD LINE\nSINGLE='s'\n"), 0o600))
	f := &fakeSystemctl{reply: func(args []string) ([]byte, error) {
		return []byte("Environment=A=1 \"B=two words\" C=3\nEnvironmentFiles=" + envFile + " (ignore_errors=no) /missing/optional.env (ignore_errors=yes)\n"), nil
	}}
	env, err := (&SystemdController{Run: f.run}).Environment(context.Background(), "otelcol-supervisor")
	require.NoError(t, err)
	assert.Equal(t, []string{"A=1", "B=two words", "C=3", "MYRIARCH_ENROLLMENT_TOKEN=myre_x", "OTEL_GATEWAY_TOKEN=quoted value", "SINGLE=s"}, env)

	// a missing mandatory EnvironmentFile is reported (but the rest is returned)
	f2 := &fakeSystemctl{reply: func(args []string) ([]byte, error) {
		return []byte("Environment=A=1\nEnvironmentFiles=/missing/mandatory.env (ignore_errors=no)\n"), nil
	}}
	env, err = (&SystemdController{Run: f2.run}).Environment(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, []string{"A=1"}, env)
}

func TestSplitSystemdWords(t *testing.T) {
	assert.Equal(t, []string{"A=1", "B=x y", `C=q"uote`}, splitSystemdWords(`A=1 "B=x y" "C=q\"uote"`))
	assert.Empty(t, splitSystemdWords(""))
}

func TestSystemdControllerTimeoutIsApplied(t *testing.T) {
	var deadlineSet bool
	c := &SystemdController{Timeout: 50 * time.Millisecond, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, deadlineSet = ctx.Deadline()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	err := c.Stop(context.Background(), "x")
	require.Error(t, err)
	assert.True(t, deadlineSet)
}

// --- Windows SCM (fake API) --------------------------------------------

type fakeSCM struct {
	mu         sync.Mutex
	exists     bool
	state      ServiceState
	stopAfter  int // Query calls until the service reaches Stopped after RequestStop
	startAfter int
	pending    int
	target     ServiceState
	env        []string
	stopErr    error
	calls      []string
}

func (f *fakeSCM) Open(name string) (scmHandle, error) {
	if !f.exists {
		return nil, ErrServiceNotFound
	}
	return &fakeSCMHandle{f: f}, nil
}
func (f *fakeSCM) Environment(string) ([]string, error) { return f.env, nil }

type fakeSCMHandle struct{ f *fakeSCM }

func (h *fakeSCMHandle) Query() (ServiceState, error) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if h.f.pending > 0 {
		h.f.pending--
		if h.f.pending == 0 {
			h.f.state = h.f.target
		}
	}
	return h.f.state, nil
}
func (h *fakeSCMHandle) RequestStop() error {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	h.f.calls = append(h.f.calls, "stop")
	if h.f.stopErr != nil {
		return h.f.stopErr
	}
	h.f.state, h.f.target, h.f.pending = ServiceStopping, ServiceStopped, h.f.stopAfter
	if h.f.pending == 0 {
		h.f.state = ServiceStopped
	}
	return nil
}
func (h *fakeSCMHandle) RequestStart() error {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	h.f.calls = append(h.f.calls, "start")
	h.f.state, h.f.target, h.f.pending = ServiceStarting, ServiceRunning, h.f.startAfter
	if h.f.pending == 0 {
		h.f.state = ServiceRunning
	}
	return nil
}
func (h *fakeSCMHandle) Close() error { return nil }

func TestSCMControllerStopWaitsUntilStopped(t *testing.T) {
	f := &fakeSCM{exists: true, state: ServiceRunning, stopAfter: 4}
	c := &SCMController{api: f, Timeout: 2 * time.Second, Poll: time.Millisecond}
	require.NoError(t, c.Stop(context.Background(), "otelcol-supervisor"))
	st, _ := c.State(context.Background(), "otelcol-supervisor")
	assert.Equal(t, ServiceStopped, st)
	assert.Equal(t, []string{"stop"}, f.calls)
	// stopping a stopped service is a no-op
	require.NoError(t, c.Stop(context.Background(), "otelcol-supervisor"))
	assert.Equal(t, []string{"stop"}, f.calls)
}

func TestSCMControllerStopTimeout(t *testing.T) {
	f := &fakeSCM{exists: true, state: ServiceRunning, stopAfter: 1 << 30}
	c := &SCMController{api: f, Timeout: 30 * time.Millisecond, Poll: time.Millisecond}
	err := c.Stop(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not reach state stopped")
}

func TestSCMControllerStopError(t *testing.T) {
	f := &fakeSCM{exists: true, state: ServiceRunning, stopErr: errors.New("Access is denied")}
	c := &SCMController{api: f, Timeout: time.Second, Poll: time.Millisecond}
	require.ErrorContains(t, c.Stop(context.Background(), "x"), "Access is denied")
}

func TestSCMControllerStartWaitsUntilRunning(t *testing.T) {
	f := &fakeSCM{exists: true, state: ServiceStopped, startAfter: 3}
	c := &SCMController{api: f, Timeout: 2 * time.Second, Poll: time.Millisecond}
	require.NoError(t, c.Start(context.Background(), "x"))
	assert.Equal(t, []string{"start"}, f.calls)
	require.NoError(t, c.Start(context.Background(), "x"), "already running")
	assert.Equal(t, []string{"start"}, f.calls)
}

func TestSCMControllerStartWhileStoppingWaitsFirst(t *testing.T) {
	f := &fakeSCM{exists: true, state: ServiceStopping, target: ServiceStopped, pending: 2}
	c := &SCMController{api: f, Timeout: 2 * time.Second, Poll: time.Millisecond}
	require.NoError(t, c.Start(context.Background(), "x"))
	assert.Equal(t, []string{"start"}, f.calls)
}

func TestSCMControllerNotFoundAndEnvironment(t *testing.T) {
	f := &fakeSCM{exists: false, env: []string{"A=b"}}
	c := &SCMController{api: f}
	ok, err := c.Exists(context.Background(), "x")
	require.NoError(t, err)
	assert.False(t, ok)
	_, err = c.State(context.Background(), "x")
	require.ErrorIs(t, err, ErrServiceNotFound)
	require.ErrorIs(t, c.Stop(context.Background(), "x"), ErrServiceNotFound)
	env, _ := c.Environment(context.Background(), "x")
	assert.Equal(t, []string{"A=b"}, env)
}

func TestSCMControllerContextCancel(t *testing.T) {
	f := &fakeSCM{exists: true, state: ServiceRunning, stopAfter: 1 << 30}
	c := &SCMController{api: f, Timeout: time.Minute, Poll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.Stop(ctx, "x"), context.DeadlineExceeded)
}

func TestFakeServiceController(t *testing.T) {
	f := NewFakeServiceController("a")
	ctx := context.Background()
	require.NoError(t, f.Stop(ctx, "a"))
	st, _ := f.State(ctx, "a")
	assert.Equal(t, ServiceStopped, st)
	require.NoError(t, f.Start(ctx, "a"))
	_, err := f.State(ctx, "nope")
	require.ErrorIs(t, err, ErrServiceNotFound)
	assert.Equal(t, []string{"stop:a", "start:a"}, f.Calls())
	require.NoError(t, waitState(ctx, f, "a", ServiceRunning, time.Second, time.Millisecond))
	require.Error(t, waitState(ctx, f, "a", ServiceStopped, 20*time.Millisecond, time.Millisecond))
}
