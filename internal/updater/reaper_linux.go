// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package updater

import (
	"os"
	"syscall"
)

const prSetChildSubreaper = 36 // PR_SET_CHILD_SUBREAPER

// becomeReaper makes this process responsible for orphaned descendants: as
// PID 1 that is the case anyway, otherwise the process registers as a child
// subreaper. It reports whether the process reaps all children.
func becomeReaper() bool {
	if os.Getpid() == 1 {
		return true
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0)
	return errno == 0
}
