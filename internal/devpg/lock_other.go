// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build devpg && !unix

package devpg

// withBinariesLock has no cross-process lock on this platform.
func withBinariesLock(binDir string, fn func() error) error { return fn() }
