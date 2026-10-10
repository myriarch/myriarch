// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteDiffAndStaleFiles(t *testing.T) {
	files := map[string][]byte{"_index.md": []byte("index\n"), "a.md": []byte("a\n"), "b.md": []byte("b\n")}
	dir := filepath.Join(t.TempDir(), "gen")

	// a missing directory: everything is missing, then Write creates it
	diff := Diff(dir, files)
	assert.Equal(t, []string{"missing: _index.md", "missing: a.md", "missing: b.md"}, diff)
	require.NoError(t, Write(dir, files))
	assert.Empty(t, Diff(dir, files))

	// changed, extra and missing files
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("changed\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stale.md"), []byte("old\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, "b.md")))
	assert.Equal(t, []string{"changed: a.md", "extra: stale.md", "missing: b.md"}, Diff(dir, files))

	// Write repairs everything and removes the stale page, but leaves other files alone
	require.NoError(t, Write(dir, files))
	assert.Empty(t, Diff(dir, files))
	_, err := os.Stat(filepath.Join(dir, "stale.md"))
	assert.True(t, os.IsNotExist(err), "stale page removed")
	_, err = os.Stat(filepath.Join(dir, "notes.txt"))
	assert.NoError(t, err, "non-Markdown files are kept")
}
