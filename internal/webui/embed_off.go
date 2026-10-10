// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !embedui

package webui

import (
	"embed"
	"io/fs"
)

//go:embed placeholder/index.html
var placeholder embed.FS

// FS returns the UI files (placeholder page without the embedui build tag).
func FS() fs.FS {
	sub, err := fs.Sub(placeholder, "placeholder")
	if err != nil {
		panic(err)
	}
	return sub
}

// Embedded reports whether the real UI is embedded.
func Embedded() bool { return false }
