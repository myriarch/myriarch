// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"io/fs"
	"os"
	"testing"
)

func osDirFS(t testing.TB, dir string) fs.FS {
	t.Helper()
	return os.DirFS(dir)
}
