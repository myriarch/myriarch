// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build embedui

package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded UI build output (internal/webui/dist).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Embedded reports whether the real UI is embedded.
func Embedded() bool { return true }
