// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package updater

import "syscall"

// openNonblock keeps open(2) from blocking on a pipe that replaces the source between the type check and the open.
const openNonblock = syscall.O_NONBLOCK
