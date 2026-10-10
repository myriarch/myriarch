// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"os"
	"syscall"
)

// fileOwner returns the numeric owner of a file.
func fileOwner(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
