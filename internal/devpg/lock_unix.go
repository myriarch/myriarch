// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build devpg && unix

package devpg

import (
	"os"
	"path/filepath"
	"syscall"
)

// withBinariesLock runs fn (embeddedpostgres.Start) so that only one process
// ever extracts binDir: the first one holds an exclusive cross-process flock
// while it extracts+starts and then writes a ".complete" marker. Later
// processes see the marker and start without holding the lock.
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
	if fileExists(marker) {
		f.Close()
		return fn()
	}
	defer f.Close()
	// a half extracted tree of a crashed run would be reused by the library
	if !fileExists(filepath.Join(binDir, "bin", "pg_ctl")) {
		_ = os.RemoveAll(binDir)
	}
	if err := fn(); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o644)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
