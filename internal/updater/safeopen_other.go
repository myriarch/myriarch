// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package updater

import (
	"fmt"
	"os"
)

// openNoFollow opens path read-only; symlinks and reparse points are
// rejected up front (Windows has no O_NOFOLLOW).
func openNoFollow(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.Open(path)
}
