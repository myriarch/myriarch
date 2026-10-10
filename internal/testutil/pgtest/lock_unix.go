// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package pgtest

import (
	"os"
	"path/filepath"
	"syscall"
)

// withBinariesLock runs fn (embeddedpostgres.Start) so that only one process ever
// extracts binDir: the first one holds an exclusive cross-process flock while it
// extracts+starts and then writes a ".complete" marker. Later/waiting processes see
// the marker and start without holding the lock (no serialisation once warm).
func withBinariesLock(binDir string, fn func() error) error {
	marker := filepath.Join(binDir, ".complete")
	if fileExists(marker) {
		return fn()
	}
	f, err := os.OpenFile(binDir+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return err
	}
	if fileExists(marker) { // another process finished extraction while we waited
		f.Close() // closing the fd releases the flock
		return fn()
	}
	defer f.Close()
	_ = os.RemoveAll(binDir) // drop any half-extracted tree from a crashed run
	if err := fn(); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o644)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
