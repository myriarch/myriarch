// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWait4 replays scripted wait4 results: a queue of exited pids for
// pid -1 and per-pid answers.
type fakeWait4 struct {
	queue []fakeExit
	calls []int
}

type fakeExit struct {
	pid int
	ws  syscall.WaitStatus
	err error
}

func (f *fakeWait4) fn(pid int, ws *syscall.WaitStatus, _ int) (int, error) {
	f.calls = append(f.calls, pid)
	for i, e := range f.queue {
		if pid == -1 || e.pid == pid {
			f.queue = append(f.queue[:i], f.queue[i+1:]...)
			if e.err != nil {
				return 0, e.err
			}
			*ws = e.ws
			return e.pid, nil
		}
	}
	if pid == -1 {
		return 0, syscall.ECHILD
	}
	return 0, nil
}

// exited builds a WaitStatus: exit code c (<<8) or signal s.
func exitedWS(code int) syscall.WaitStatus { return syscall.WaitStatus(code << 8) }
func signaledWS(sig syscall.Signal) syscall.WaitStatus {
	return syscall.WaitStatus(sig)
}

func TestReaperReapAllDeliversWatchedAndCollectsOrphans(t *testing.T) {
	f := &fakeWait4{queue: []fakeExit{
		{pid: 101, ws: exitedWS(0)},   // an orphan nobody watches (zombie adopted by PID 1)
		{pid: 100, ws: exitedWS(3)},   // the watched child
		{pid: 102, ws: signaledWS(9)}, // another orphan
	}}
	r := newReaper(true)
	r.wait4 = f.fn
	ch := r.Watch(100)
	r.reapOnce()

	select {
	case st := <-ch:
		assert.Equal(t, 3, st.Code)
		assert.Zero(t, st.Signal)
	default:
		t.Fatal("watched pid not delivered")
	}
	assert.Equal(t, 2, r.Reaped(), "both orphans were collected")
	assert.Empty(t, f.queue)
}

func TestReaperExitBeforeWatchIsNotLost(t *testing.T) {
	f := &fakeWait4{queue: []fakeExit{{pid: 200, ws: exitedWS(7)}}}
	r := newReaper(true)
	r.wait4 = f.fn
	r.reapOnce() // exit collected before anyone watches
	st := <-r.Watch(200)
	assert.Equal(t, 7, st.Code)
}

func TestReaperOrphanMemoryIsBounded(t *testing.T) {
	r := newReaper(true)
	for pid := 1; pid <= maxOrphanExits+50; pid++ {
		r.deliver(pid, exitStatus{})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	assert.LessOrEqual(t, len(r.orphans), maxOrphanExits)
	assert.NotContains(t, r.orphans, 1, "oldest entries are dropped first")
}

func TestReaperWatchOnlyWaitsForItsOwnPids(t *testing.T) {
	f := &fakeWait4{queue: []fakeExit{
		{pid: 300, ws: signaledWS(syscall.SIGKILL)},
		{pid: 301, ws: exitedWS(0)}, // somebody else's child: must stay untouched
	}}
	r := newReaper(false)
	r.wait4 = f.fn
	ch := r.Watch(300)
	r.reapOnce()
	st := <-ch
	assert.Equal(t, syscall.SIGKILL, st.Signal)
	assert.NotContains(t, f.calls, -1, "never wait4(-1) outside reapAll mode")
	assert.Len(t, f.queue, 1, "the foreign child was not reaped")
	assert.Contains(t, st.String(), "killed by signal")
}

func TestReaperEchildMeansStatusLost(t *testing.T) {
	f := &fakeWait4{queue: []fakeExit{{pid: 400, err: syscall.ECHILD}}}
	r := newReaper(false)
	r.wait4 = f.fn
	ch := r.Watch(400)
	r.reapOnce()
	st := <-ch
	assert.True(t, st.Lost)
	assert.Contains(t, st.String(), "unknown")
}

func TestReaperCollectsRealChildrenViaSpawn(t *testing.T) {
	rp := testReaper(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "x.sh", "exit 5\n")
	pid, exit, err := rp.Spawn(spawnSpec{Path: script, Stdout: os.Stderr, Stderr: os.Stderr})
	require.NoError(t, err)
	select {
	case st := <-exit:
		assert.Equal(t, 5, st.Code)
	case <-time.After(10 * time.Second):
		t.Fatalf("exit of pid %d not delivered", pid)
	}
	// no zombie left behind
	assert.Eventually(t, func() bool { return !pidAlive(pid) }, 5*time.Second, 10*time.Millisecond)
}

func TestReaperExecutorOutputAndExit(t *testing.T) {
	rp := testReaper(t)
	dir := t.TempDir()
	exe := rp.Executor()
	ctx := context.Background()

	ok := writeScript(t, dir, "ok.sh", "echo hello \"$@\"; echo \"$FOO\"\n")
	out, err := exe(ctx, []string{"FOO=bar"}, ok, "a", "b")
	require.NoError(t, err)
	assert.Equal(t, "hello a b\nbar\n", string(out))

	bad := writeScript(t, dir, "bad.sh", "echo broken; exit 3\n")
	out, err = exe(ctx, nil, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 3")
	assert.Contains(t, string(out), "broken")

	_, err = exe(ctx, nil, filepath.Join(dir, "missing"))
	require.Error(t, err)
}

func TestReaperExecutorTimeoutKillsTheGroup(t *testing.T) {
	rp := testReaper(t)
	dir := t.TempDir()
	t.Setenv("GRANDCHILD", filepath.Join(dir, "gc"))
	script := writeScript(t, dir, "hang.sh", "sleep 300 &\necho $! > \"$GRANDCHILD\"\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	begin := time.Now()
	_, err := rp.Executor()(ctx, nil, script)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(begin), 8*time.Second)
	gc := readPids(t, filepath.Join(dir, "gc"))
	require.Len(t, gc, 1)
	require.Eventually(t, func() bool { return !pidAlive(gc[0]) }, 5*time.Second, 20*time.Millisecond, "grandchild died with the group")
}

func TestReaperExecutorDoesNotHangOnPipeHoldingDescendant(t *testing.T) {
	rp := testReaper(t)
	dir := t.TempDir()
	// the script exits at once but leaves a descendant that keeps the output pipe open
	script := writeScript(t, dir, "leak.sh", "(sleep 300) &\necho started\nexit 0\n")
	begin := time.Now()
	out, err := rp.Executor()(context.Background(), nil, script)
	require.NoError(t, err)
	assert.Contains(t, string(out), "started")
	assert.Less(t, time.Since(begin), 8*time.Second)
}

// The preflight of container mode runs the image binary through the same
// executor: sanity check with a script that looks like otelcol-contrib.
func TestImageVersionParsing(t *testing.T) {
	rp := testReaper(t)
	dir := t.TempDir()
	good := writeScript(t, dir, "otelcol-contrib", "echo \"otelcol-contrib version 0.161.0\"\n")
	v, err := imageVersion(context.Background(), rp.Executor(), good, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "0.161.0", v)

	pre := writeScript(t, dir, "pre", "echo \"otelcol-contrib version v0.163.0-rc.1\"\n")
	v, err = imageVersion(context.Background(), rp.Executor(), pre, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "0.163.0-rc.1", v)

	junk := writeScript(t, dir, "junk", "echo nonsense\n")
	_, err = imageVersion(context.Background(), rp.Executor(), junk, time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find a version")
}

// Real zombie adoption needs PID 1 or a subreaper (Linux only).
func TestReaperCollectsAdoptedOrphansOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("orphans are only re-parented to this process on Linux (PID 1 or PR_SET_CHILD_SUBREAPER); elsewhere launchd adopts them")
	}
	if !becomeReaper() {
		t.Skip("cannot become a subreaper in this environment")
	}
	rp := newReaper(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rp.Run(ctx) }()
	defer func() { cancel(); <-done }()
	dir := t.TempDir()
	// the shell starts a background child and exits: the child is re-parented to us and exits later
	script := writeScript(t, dir, "orphan.sh", "(sleep 0.3; exit 0) &\nexit 0\n")
	_, exit, err := rp.Spawn(spawnSpec{Path: script, Stdout: os.Stderr, Stderr: os.Stderr})
	require.NoError(t, err)
	<-exit
	require.Eventually(t, func() bool { return rp.Reaped() >= 1 }, 10*time.Second, 20*time.Millisecond, "the adopted orphan was reaped")
}

// A child without explicit stdin gets /dev/null (reads return EOF), not a closed descriptor.
func TestReaperSpawnConnectsDevNullToStdin(t *testing.T) {
	rp := testReaper(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := writeScript(t, dir, "stdin.sh", "if read -r line; then echo got >\"$OUT\"; else echo eof >\"$OUT\"; fi\n")
	t.Setenv("OUT", out)
	_, exit, err := rp.Spawn(spawnSpec{Path: script})
	require.NoError(t, err)
	<-exit
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "eof\n", string(b))
}
