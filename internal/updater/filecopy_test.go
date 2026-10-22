// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
	require.NoError(t, os.Chmod(path, mode))
}

func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.Contains(e.Name(), ".tmp-"), "left over: %s", e.Name())
	}
}

func TestCopySucceedsAndKeepsExecuteBit(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tool")
	writeTestFile(t, src, "binary", 0o700)
	dst := filepath.Join(dir, "out")
	require.NoError(t, os.Mkdir(dst, 0o755))

	stdout, err := runCLI(t, "copy", src, dst) // dst is a directory
	require.NoError(t, err)
	assert.Contains(t, stdout, filepath.Join(dst, "tool"))
	got, err := os.ReadFile(filepath.Join(dst, "tool"))
	require.NoError(t, err)
	assert.Equal(t, "binary", string(got))
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dst, "tool"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	}
	noTempFiles(t, dst)
}

func TestCopyPlainFileGetsMode0644AndReplacesRegularFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "conf")
	writeTestFile(t, src, "new", 0o600)
	dst := filepath.Join(dir, "target")
	writeTestFile(t, dst, "old", 0o600)

	_, err := runCLI(t, "copy", src, dst)
	require.NoError(t, err)
	got, _ := os.ReadFile(dst)
	assert.Equal(t, "new", string(got))
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(dst)
		assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
	}
	noTempFiles(t, dir)
}

func TestCopyFollowsASymlinkedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	writeTestFile(t, real, "data", 0o644)
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(real, link))
	dst := filepath.Join(dir, "copy")
	_, err := runCLI(t, "copy", link, dst)
	require.NoError(t, err)
	got, _ := os.ReadFile(dst)
	assert.Equal(t, "data", string(got))
}

func TestCopyRefusals(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, src, "x", 0o644)

	t.Run("source missing", func(t *testing.T) {
		_, err := runCLI(t, "copy", filepath.Join(dir, "nope"), filepath.Join(dir, "d1"))
		require.ErrorContains(t, err, "open source")
		assert.NoFileExists(t, filepath.Join(dir, "d1"))
	})
	t.Run("source is a directory", func(t *testing.T) {
		_, err := runCLI(t, "copy", dir, filepath.Join(dir, "d2"))
		require.ErrorContains(t, err, "not a regular file")
		assert.NoFileExists(t, filepath.Join(dir, "d2"))
	})
	t.Run("relative paths", func(t *testing.T) {
		_, err := runCLI(t, "copy", "src", filepath.Join(dir, "d3"))
		require.ErrorContains(t, err, "absolute")
		_, err = runCLI(t, "copy", src, "d3")
		require.ErrorContains(t, err, "absolute")
	})
	t.Run("destination directory missing", func(t *testing.T) {
		_, err := runCLI(t, "copy", src, filepath.Join(dir, "missing", "f"))
		require.ErrorContains(t, err, "destination directory")
		assert.NoDirExists(t, filepath.Join(dir, "missing"))
	})
	t.Run("same file", func(t *testing.T) {
		_, err := runCLI(t, "copy", src, src)
		require.ErrorContains(t, err, "same file")
		got, _ := os.ReadFile(src)
		assert.Equal(t, "x", string(got))
	})
	t.Run("wrong argument count", func(t *testing.T) {
		_, err := runCLI(t, "copy", src)
		require.Error(t, err)
	})
	noTempFiles(t, dir)
}

func TestCopyRefusesASymlinkDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, src, "x", 0o644)
	victim := filepath.Join(dir, "victim")
	writeTestFile(t, victim, "keep", 0o644)

	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(victim, link))
	_, err := runCLI(t, "copy", src, link)
	require.ErrorContains(t, err, "symlink")
	got, _ := os.ReadFile(victim)
	assert.Equal(t, "keep", string(got))

	// a symlink to a directory is no directory to copy into
	outside := t.TempDir()
	dirLink := filepath.Join(dir, "dirlink")
	require.NoError(t, os.Symlink(outside, dirLink))
	_, err = runCLI(t, "copy", src, dirLink)
	require.ErrorContains(t, err, "symlink")
	entries, _ := os.ReadDir(outside)
	assert.Empty(t, entries)

	// a dangling link is refused as well
	dangling := filepath.Join(dir, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(dir, "gone"), dangling))
	_, err = runCLI(t, "copy", src, dangling)
	require.ErrorContains(t, err, "symlink")
	assert.NoFileExists(t, filepath.Join(dir, "gone"))
	noTempFiles(t, dir)
}

func TestCopyRefusesADeviceDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no device files")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, src, "x", 0o644)
	_, err := runCLI(t, "copy", src, os.DevNull)
	require.ErrorContains(t, err, "not a regular file")
}

func TestCopyIntoReadOnlyDirectoryFailsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, src, "x", 0o644)
	ro := filepath.Join(dir, "ro")
	require.NoError(t, os.Mkdir(ro, 0o500))
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	_, err := runCLI(t, "copy", src, filepath.Join(ro, "f"))
	require.ErrorContains(t, err, "create temporary file")
	require.ErrorContains(t, err, ro)
	entries, _ := os.ReadDir(ro)
	assert.Empty(t, entries)
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, errors.New("disk on fire")
	}
	r.n--
	copy(p, "partial")
	return len("partial"), nil
}

func TestPartialWriteLeavesNoHalfFileAndKeepsTheOldOne(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	err := writeAtomic(target, &failingReader{n: 1}, 0o644)
	require.ErrorContains(t, err, "disk on fire")
	assert.NoFileExists(t, target)
	noTempFiles(t, dir)

	writeTestFile(t, target, "old", 0o644)
	err = writeAtomic(target, &failingReader{n: 2}, 0o644)
	require.Error(t, err)
	got, _ := os.ReadFile(target)
	assert.Equal(t, "old", string(got))
	noTempFiles(t, dir)
}

func TestInstallSelf(t *testing.T) {
	dir := t.TempDir()
	stdout, err := runCLI(t, "install-self", dir)
	require.NoError(t, err)
	want := filepath.Join(dir, "myriarch-updater"+exeSuffix())
	assert.Contains(t, stdout, want)
	exe, err := os.Executable()
	require.NoError(t, err)
	a, _ := os.ReadFile(exe)
	b, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(want)
		assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	}
	// running it again overwrites a regular file
	_, err = runCLI(t, "install-self", dir)
	require.NoError(t, err)
	noTempFiles(t, dir)
}

func TestInstallSelfRefusals(t *testing.T) {
	dir := t.TempDir()
	_, err := runCLI(t, "install-self", filepath.Join(dir, "missing"))
	require.ErrorContains(t, err, "directory")
	f := filepath.Join(dir, "file")
	writeTestFile(t, f, "x", 0o644)
	_, err = runCLI(t, "install-self", f)
	require.ErrorContains(t, err, "not a directory")
	_, err = runCLI(t, "install-self", "rel")
	require.ErrorContains(t, err, "absolute")

	if runtime.GOOS == "windows" {
		return
	}
	victim := filepath.Join(dir, "victim")
	writeTestFile(t, victim, "keep", 0o644)
	require.NoError(t, os.Symlink(victim, filepath.Join(dir, "myriarch-updater")))
	_, err = runCLI(t, "install-self", dir)
	require.ErrorContains(t, err, "symlink")
	got, _ := os.ReadFile(victim)
	assert.Equal(t, "keep", string(got))

	d2 := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(d2, "myriarch-updater"), 0o755))
	_, err = runCLI(t, "install-self", d2)
	require.ErrorContains(t, err, "directory")
}
