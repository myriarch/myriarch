// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package pgtest

// spawnWatchdog is a no-op on non-unix systems: stop the server explicitly
// (TestMain pattern) to avoid orphaned postgres processes.
func (s *Server) spawnWatchdog() error { return nil }
