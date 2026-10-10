// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pidExists reports whether the kernel still has an entry for pid. A zombie
// counts: it has ended, but nobody has collected its exit status. Use it for
// children that the reaper under test has to collect.
func pidExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// pidAlive reports whether the process has not ended. On Linux a zombie (state
// Z or X in /proc/<pid>/stat) has ended; elsewhere it is pidExists, because
// init or launchd collects orphans there. Use it for descendants that die with
// their process group: once their parent is gone they are re-parented to the
// test binary if an earlier test made it a child subreaper (RunContainer does,
// through becomeReaper, and the setting lasts for the life of the process),
// and no reaper of the tests collects them. They stay zombies, which
// kill(pid, 0) cannot tell from a running process.
func pidAlive(pid int) bool {
	if runtime.GOOS == "linux" {
		if state, ok := procState(pid); ok {
			return state != 'Z' && state != 'X'
		}
	}
	return pidExists(pid)
}

// procState returns the state letter of pid from /proc/<pid>/stat (Linux). It
// fails if the process is gone or /proc cannot be read.
func procState(pid int) (byte, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return procStatState(string(b))
}

// procStatState extracts the state from the content of /proc/<pid>/stat,
// "pid (command) S ppid ...". The command name may contain spaces and
// parentheses, so the state is the field after the last ')'.
func procStatState(stat string) (byte, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) || stat[i+1] != ' ' {
		return 0, false
	}
	c := stat[i+2]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return 0, false
	}
	return c, true
}

func TestProcStatState(t *testing.T) {
	for stat, want := range map[string]byte{
		"1234 (sleep) S 1 1234 1234 0 -1 4194304 100 0 0\n": 'S',
		"1234 (sh) Z 1 1234 1234 0 -1 4194308 0 0 0 0\n":    'Z',
		"1234 (tmux: server) X 1 2 3":                       'X',
		"1234 () R 1 2 3":                                   'R',
		// a command name that looks like the rest of the line: only the last ')' counts
		"1234 (x) S 1 y (z) Z 1 2 3\n": 'Z',
		"1234 (a b) c) D 1 2 3":        'D',
	} {
		got, ok := procStatState(stat)
		require.True(t, ok, stat)
		assert.Equal(t, string(want), string(got), stat)
	}
	for _, stat := range []string{"", "1234", "1234 (sh", "1234 (sh)", "1234 (sh) ", "1234 (sh)S 1", "1234 (sh) 7 1 2", "1234 sh S 1 2"} {
		_, ok := procStatState(stat)
		assert.False(t, ok, "%q", stat)
	}
}

func TestPidAliveFollowsAProcessToItsEnd(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	assert.True(t, pidAlive(pid))
	assert.True(t, pidExists(pid))

	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait() // collects the exit status: "signal: killed"
	assert.False(t, pidAlive(pid))
	assert.False(t, pidExists(pid))
}

// os/exec never waits on its own, so a child that has exited stays a zombie
// until the test waits for it.
func TestPidAliveTreatsAZombieAsEnded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux tells a zombie from a running process (/proc/<pid>/stat); elsewhere pidAlive is kill(pid, 0)")
	}
	cmd := exec.Command("sh", "-c", "exit 0")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Wait() })
	pid := cmd.Process.Pid
	require.Eventually(t, func() bool { s, ok := procState(pid); return ok && s == 'Z' }, 5*time.Second, 5*time.Millisecond, "the child ended and waits to be collected")
	assert.True(t, pidExists(pid), "kill(pid, 0) succeeds for a zombie")
	assert.False(t, pidAlive(pid), "a zombie has ended")

	require.NoError(t, cmd.Wait())
	assert.False(t, pidExists(pid), "collected")
	assert.False(t, pidAlive(pid))
}
