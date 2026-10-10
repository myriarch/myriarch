// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package pgtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// spawnWatchdog starts a detached shell that waits for this test binary to exit and
// then stops postgres (immediate mode) and removes the temp dir. It is a no-op if Stop
// already ran. Setpgid keeps it alive when the terminal sends SIGINT to the group.
func (s *Server) spawnWatchdog() error {
	script := `while kill -0 "$1" 2>/dev/null; do sleep 1; done
[ -f "$3/postmaster.pid" ] && "$2" stop -m immediate -D "$3" >/dev/null 2>&1
rm -rf "$4"`
	cmd := exec.Command("/bin/sh", "-c", script, "pgtest-watchdog",
		strconv.Itoa(os.Getpid()),
		filepath.Join(s.BinDir, "bin", "pg_ctl"),
		filepath.Join(s.BaseDir, "run", "data"),
		s.BaseDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start watchdog: %w", err)
	}
	go func() { _ = cmd.Wait() }() // reap if we outlive it (we normally don't)
	return nil
}
