// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix && !linux

package updater

// becomeReaper: outside Linux (macOS during local tests) only the processes
// started through the reaper are collected; orphans are adopted by launchd.
func becomeReaper() bool { return false }
