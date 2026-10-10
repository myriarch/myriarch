// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package sim

// RaiseFileLimit is a no-op on platforms without RLIMIT_NOFILE.
func RaiseFileLimit() (uint64, error) { return 0, nil }
