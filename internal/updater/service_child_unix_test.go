// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testReaper runs a (watch-only) reaper for the duration of the test.
func testReaper(t *testing.T) *reaper {
	t.Helper()
	rp := newReaper(false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rp.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return rp
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755))
	return p
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func readPids(t testing.TB, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		n, err := strconv.Atoi(f)
		require.NoError(t, err)
		pids = append(pids, n)
	}
	return pids
}

func newTestChild(t *testing.T, script string, mod func(*ChildOptions)) *ChildController {
	t.Helper()
	o := ChildOptions{
		Command: []string{script}, StopTimeout: 3 * time.Second, RestartDelay: 20 * time.Millisecond,
		MaxRestarts: 5, RestartWindow: time.Minute, Log: testLogger(t), Reaper: testReaper(t),
		Stdout: os.Stderr, Stderr: os.Stderr,
	}
	if mod != nil {
		mod(&o)
	}
	c := NewChildController(o)
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	return c
}

// A supervisor stand-in that records its pid, terminates cleanly on SIGTERM.
const sleepyChild = `echo $$ >> "$STARTS"
trap 'exit 0' TERM
while :; do sleep 0.05; done
`

func TestChildStartStopCycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	c := newTestChild(t, writeScript(t, dir, "sup.sh", sleepyChild), nil)
	ctx := context.Background()

	st, _ := c.State(ctx, "x")
	assert.Equal(t, ServiceStopped, st)
	require.NoError(t, c.Start(ctx, "x"))
	st, _ = c.State(ctx, "x")
	assert.Equal(t, ServiceRunning, st)
	pid := c.Pid()
	require.NotZero(t, pid)
	require.NoError(t, c.Start(ctx, "x"), "starting a running child is not an error")
	assert.Equal(t, pid, c.Pid())
	require.Eventually(t, func() bool { return len(readPids(t, filepath.Join(dir, "starts"))) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // the shell has installed its TERM trap

	require.NoError(t, c.Stop(ctx, "x"))
	st, _ = c.State(ctx, "x")
	assert.Equal(t, ServiceStopped, st)
	assert.False(t, pidAlive(pid), "child is gone")
	require.NoError(t, c.Stop(ctx, "x"), "stopping a stopped child is not an error")

	time.Sleep(150 * time.Millisecond) // no restart after a deliberate stop
	assert.Len(t, readPids(t, filepath.Join(dir, "starts")), 1)

	require.NoError(t, c.Start(ctx, "x"))
	assert.NotEqual(t, pid, c.Pid())
	require.Eventually(t, func() bool { return len(readPids(t, filepath.Join(dir, "starts"))) == 2 }, 5*time.Second, 10*time.Millisecond)
	env, err := c.Environment(ctx, "x")
	require.NoError(t, err)
	assert.NotEmpty(t, env)
	ok, _ := c.Exists(ctx, "x")
	assert.True(t, ok)
}

func TestChildStopKillsStubbornChildAfterTimeout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	script := writeScript(t, dir, "sup.sh", `echo $$ >> "$STARTS"
trap '' TERM
while :; do sleep 0.05; done
`)
	c := newTestChild(t, script, func(o *ChildOptions) { o.StopTimeout = 300 * time.Millisecond })
	require.NoError(t, c.Start(context.Background(), "x"))
	pid := c.Pid()
	// let the shell install its trap
	require.Eventually(t, func() bool { return len(readPids(t, filepath.Join(dir, "starts"))) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	begin := time.Now()
	require.NoError(t, c.Stop(context.Background(), "x"))
	took := time.Since(begin)
	assert.GreaterOrEqual(t, took, 250*time.Millisecond, "SIGTERM was ignored, so the full timeout is waited")
	assert.Less(t, took, 3*time.Second)
	assert.False(t, pidAlive(pid))
}

func TestChildStopKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	t.Setenv("GRANDCHILD", filepath.Join(dir, "grandchild"))
	script := writeScript(t, dir, "sup.sh", `echo $$ >> "$STARTS"
sleep 300 &
echo $! > "$GRANDCHILD"
trap 'exit 0' TERM
while :; do sleep 0.05; done
`)
	c := newTestChild(t, script, nil)
	require.NoError(t, c.Start(context.Background(), "x"))
	require.Eventually(t, func() bool { return len(readPids(t, filepath.Join(dir, "grandchild"))) == 1 }, 5*time.Second, 10*time.Millisecond)
	gc := readPids(t, filepath.Join(dir, "grandchild"))[0]
	require.True(t, pidAlive(gc))

	require.NoError(t, c.Stop(context.Background(), "x"))
	require.Eventually(t, func() bool { return !pidAlive(gc) }, 5*time.Second, 20*time.Millisecond, "grandchild ended with its group")
}

func TestChildRestartsAfterUnexpectedExitWithBackoff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	// exits on its own after a moment, every time
	script := writeScript(t, dir, "sup.sh", `echo $$ >> "$STARTS"
sleep 0.1
exit 3
`)
	c := newTestChild(t, script, func(o *ChildOptions) { o.RestartDelay = 50 * time.Millisecond; o.MaxRestarts = 100 })
	require.NoError(t, c.Start(context.Background(), "x"))
	require.Eventually(t, func() bool { return len(readPids(t, filepath.Join(dir, "starts"))) >= 3 }, 10*time.Second, 20*time.Millisecond, "restarted again and again")
	select {
	case <-c.Fatal():
		t.Fatal("not fatal below maxRestarts")
	default:
	}
	// while waiting for the backoff the state is "starting", never "stopped"
	st, _ := c.State(context.Background(), "x")
	assert.NotEqual(t, ServiceStopped, st)
}

func TestChildGivesUpAfterMaxRestarts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	script := writeScript(t, dir, "sup.sh", "echo $$ >> \"$STARTS\"\nexit 1\n")
	c := newTestChild(t, script, func(o *ChildOptions) { o.RestartDelay = 10 * time.Millisecond; o.MaxRestarts = 3 })
	require.NoError(t, c.Start(context.Background(), "x"))
	select {
	case <-c.Fatal():
	case <-time.After(15 * time.Second):
		t.Fatal("child did not give up")
	}
	require.Error(t, c.Err())
	assert.Contains(t, c.Err().Error(), "ended unexpectedly")
	assert.Contains(t, c.Err().Error(), "exited with code 1")
	n := len(readPids(t, filepath.Join(dir, "starts")))
	assert.Equal(t, 4, n, "the start plus maxRestarts restarts")
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, n, len(readPids(t, filepath.Join(dir, "starts"))), "no further restarts after giving up")
}

func TestChildStartFailsForMissingBinary(t *testing.T) {
	c := newTestChild(t, "/nonexistent/supervisor", nil)
	err := c.Start(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start /nonexistent/supervisor")
}

func TestChildSignalForwardingReachesTheLeader(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	t.Setenv("GOT", filepath.Join(dir, "got"))
	// the traps are installed before the pid is announced: a signal must never
	// reach the shell before its handler exists (flaky on a loaded machine)
	script := writeScript(t, dir, "sup.sh", `trap 'echo hup >> "$GOT"' HUP
trap 'exit 0' TERM
echo $$ >> "$STARTS"
while :; do sleep 0.05; done
`)
	c := newTestChild(t, script, nil)
	require.NoError(t, c.Start(context.Background(), "x"))
	require.Eventually(t, func() bool { return len(readPids(t, filepath.Join(dir, "starts"))) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	c.Signal(syscall.SIGHUP)
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "got"))
		return strings.Contains(string(b), "hup")
	}, 5*time.Second, 20*time.Millisecond)
}

func TestChildStopWhileWaitingForRestartCancelsIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTS", filepath.Join(dir, "starts"))
	script := writeScript(t, dir, "sup.sh", "echo $$ >> \"$STARTS\"\nexit 1\n")
	c := newTestChild(t, script, func(o *ChildOptions) { o.RestartDelay = 400 * time.Millisecond; o.MaxRestarts = 50 })
	require.NoError(t, c.Start(context.Background(), "x"))
	require.Eventually(t, func() bool {
		st, _ := c.State(context.Background(), "x")
		return st == ServiceStarting
	}, 5*time.Second, 5*time.Millisecond, "child ended, restart is pending")
	require.NoError(t, c.Stop(context.Background(), "x"))
	n := len(readPids(t, filepath.Join(dir, "starts")))
	time.Sleep(600 * time.Millisecond)
	assert.Equal(t, n, len(readPids(t, filepath.Join(dir, "starts"))), "the pending restart was cancelled")
}
