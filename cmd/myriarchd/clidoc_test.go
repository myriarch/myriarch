// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/clidoc"
	"github.com/myriarch/myriarch/internal/docgen"
	"github.com/myriarch/myriarch/internal/myrctl"
	"github.com/myriarch/myriarch/internal/sim"
	"github.com/myriarch/myriarch/internal/updater"
)

// TestCLIReference renders the command line reference of all four binaries into docs/reference/cli and compares it
// with the committed files. The root command of myriarchd lives in package main, which is why the generator is a test
// of this package. "make docs-gen" runs it with CLIDOC_UPDATE=1 to write the files; without the variable it fails
// when a command, flag or help text changed and the reference was not regenerated.
func TestCLIReference(t *testing.T) {
	files, err := clidoc.Render(newRootCommand(), myrctl.NewRootCommand(), updater.NewRootCommand(), sim.NewRootCommand())
	require.NoError(t, err)
	dir := filepath.Join("..", "..", "docs", "reference", "cli")
	if os.Getenv("CLIDOC_UPDATE") == "1" {
		require.NoError(t, docgen.Write(dir, files))
		return
	}
	if diff := docgen.Diff(dir, files); len(diff) > 0 {
		t.Fatalf("docs/reference/cli is out of date (run make docs-gen and commit):\n  %s", strings.Join(diff, "\n  "))
	}
}
