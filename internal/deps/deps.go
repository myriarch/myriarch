// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package deps holds build-tagged blank imports that keep platform-specific
// dependencies (golang.org/x/sys) in go.mod when the importing code is only
// compiled on another platform (see deps_unix.go, deps_windows.go).
//
// Only imports that no real code provides on every platform belong here.
package deps
