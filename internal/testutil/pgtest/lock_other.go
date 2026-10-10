// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package pgtest

// On Windows we do not serialise extraction; use a per-process BinariesPath there
// (or pre-extract in CI) if test packages run in parallel.
func withBinariesLock(_ string, fn func() error) error { return fn() }
