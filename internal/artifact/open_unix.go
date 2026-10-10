// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package artifact

import (
	"os"
	"syscall"
)

// openNoFollow opens a cache file for reading without following a symbolic
// link at the final path element.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
