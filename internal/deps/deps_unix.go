// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package deps

import (
	// Unix syscalls (rlimits for myriarch-sim, flock for pgtest).
	_ "golang.org/x/sys/unix"
)
