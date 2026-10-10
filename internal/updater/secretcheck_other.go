// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package updater

import "os"

// fileOwner is not available outside Unix.
func fileOwner(os.FileInfo) (uint32, bool) { return 0, false }
