// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package opamp_test

import (
	"syscall"
	"testing"
)

// raiseNoFile lifts the soft file descriptor limit (macOS defaults to 256).
func raiseNoFile(t *testing.T, want uint64) {
	t.Helper()
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return
	}
	if lim.Cur >= want {
		return
	}
	lim.Cur = want
	if lim.Max < want {
		lim.Max = want
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Logf("cannot raise RLIMIT_NOFILE to %d: %v", want, err)
	}
}
