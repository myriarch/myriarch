// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestWriteFileAndDiffFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "reference")
	path := filepath.Join(dir, "page.md")
	other := filepath.Join(dir, "hand-written.md")

	assert.Equal(t, "missing: page.md", DiffFile(path, []byte("one\n")))
	require.NoError(t, WriteFile(path, []byte("one\n")), "creates the parent directories")
	assert.Equal(t, "", DiffFile(path, []byte("one\n")))
	assert.Equal(t, "changed: page.md", DiffFile(path, []byte("two\n")))

	// other files of the directory are never removed
	require.NoError(t, os.WriteFile(other, []byte("keep\n"), 0o644))
	require.NoError(t, WriteFile(path, []byte("two\n")))
	assert.Equal(t, "", DiffFile(path, []byte("two\n")))
	kept, err := os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, "keep\n", string(kept))

	// identical content is not rewritten
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, old, old))
	require.NoError(t, WriteFile(path, []byte("two\n")))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(old), "an identical file is left untouched")
}
