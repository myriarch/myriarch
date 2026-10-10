// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package artifact

import (
	"fmt"
	"os"
)

// openNoFollow opens a cache file for reading; reparse points (links) are
// refused after opening.
func openNoFollow(path string) (*os.File, error) {
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link", path)
	}
	return os.Open(path)
}
