// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeVersion(t *testing.T) {
	good := map[string]string{"v0.163.0": "0.163.0", "0.163.0": "0.163.0", "v1.2.3-rc.1": "1.2.3-rc.1"}
	for in, want := range good {
		got, err := normalizeVersion(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"", "v", "1.2", "1.2.3.4", "../1.2.3", "1.2.3/../..", "v1.2.3\n", "1.2.3 ; ls", "latest", "1.2.3-", "1.2.3-../x"} {
		_, err := normalizeVersion(in)
		assert.Error(t, err, in)
	}
}

func TestSymlinkActivationIsAtomicAndRelative(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	l := &Layout{InstallDir: root, BinaryName: testBinary, Activation: "symlink"}
	for _, v := range []string{"0.1.0", "0.2.0"} {
		require.NoError(t, os.MkdirAll(l.VersionDir(v), 0o755))
		require.NoError(t, os.WriteFile(l.BinaryPath(v), []byte(v), 0o755))
	}
	cur, err := l.CurrentVersion()
	require.NoError(t, err)
	assert.Equal(t, "", cur)

	require.NoError(t, l.Activate("0.1.0"))
	target, err := os.Readlink(filepath.Join(root, "current"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("versions", "0.1.0"), target, "relative link survives moving the install dir")
	require.NoError(t, l.Activate("0.2.0"))
	cur, _ = l.CurrentVersion()
	assert.Equal(t, "0.2.0", cur)
	data, err := os.ReadFile(filepath.Join(root, "current", testBinary))
	require.NoError(t, err)
	assert.Equal(t, "0.2.0", string(data))
	// no temp links left
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp-")
	}
	assert.Error(t, l.Activate("9.9.9"), "uninstalled version")
	assert.Error(t, l.Activate("../x"))
}

func TestSymlinkActivationRefusesRealDirectory(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	l := &Layout{InstallDir: root, BinaryName: testBinary, Activation: "symlink"}
	require.NoError(t, os.MkdirAll(l.VersionDir("0.1.0"), 0o755))
	require.NoError(t, os.WriteFile(l.BinaryPath("0.1.0"), []byte("x"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "current"), 0o755))
	require.Error(t, l.Activate("0.1.0"))
	_, err := l.CurrentVersion()
	require.Error(t, err)
}

// The Windows code path: binary is copied to bin\ with a .previous backup.
func TestCopyActivationBackupAndRollback(t *testing.T) {
	root := t.TempDir()
	l := &Layout{InstallDir: root, BinaryName: "otelcol-contrib.exe", Activation: "copy"}
	for _, v := range []string{"0.1.0", "0.2.0"} {
		require.NoError(t, os.MkdirAll(l.VersionDir(v), 0o755))
		require.NoError(t, os.WriteFile(l.BinaryPath(v), []byte("exe-"+v), 0o755))
	}
	require.NoError(t, l.Activate("0.1.0"))
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, "bin", name))
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, "exe-0.1.0", read("otelcol-contrib.exe"))
	require.NoError(t, l.Activate("0.2.0"))
	assert.Equal(t, "exe-0.2.0", read("otelcol-contrib.exe"))
	assert.Equal(t, "exe-0.1.0", read("otelcol-contrib.exe.previous"), "old binary kept as .previous")
	cur, _ := l.CurrentVersion()
	assert.Equal(t, "0.2.0", cur)
	// rollback
	require.NoError(t, l.Activate("0.1.0"))
	assert.Equal(t, "exe-0.1.0", read("otelcol-contrib.exe"))
	assert.Equal(t, "exe-0.2.0", read("otelcol-contrib.exe.previous"))
	cur, _ = l.CurrentVersion()
	assert.Equal(t, "0.1.0", cur)
	_, err := os.Stat(filepath.Join(root, "bin", "otelcol-contrib.exe.new"))
	assert.True(t, os.IsNotExist(err))
}

func TestPruneKeepsActiveAndPrevious(t *testing.T) {
	root := t.TempDir()
	l := &Layout{InstallDir: root, BinaryName: testBinary, Activation: "symlink"}
	base := time.Now().Add(-10 * time.Hour)
	for i, v := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
		require.NoError(t, os.MkdirAll(l.VersionDir(v), 0o755))
		require.NoError(t, l.writeMarker(l.VersionDir(v), v, "", []byte{1}, []byte{2}, base.Add(time.Duration(i)*time.Hour)))
	}
	// active = oldest (e.g. after a rollback), previous = 0.4.0
	require.NoError(t, l.Prune(2, "0.1.0", "0.4.0"))
	vs, _ := l.ListVersions()
	var names []string
	for _, v := range vs {
		names = append(names, v.Version)
	}
	assert.ElementsMatch(t, []string{"0.1.0", "0.4.0"}, names)
}

func TestPruneRemovesStaleStaging(t *testing.T) {
	root := t.TempDir()
	l := &Layout{InstallDir: root, BinaryName: testBinary, Activation: "symlink"}
	stale := filepath.Join(l.VersionsDir(), "0.9.0.tmp-abc")
	fresh := filepath.Join(l.VersionsDir(), "0.9.1.tmp-def")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.MkdirAll(fresh, 0o755))
	old := time.Now().Add(-3 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))
	require.NoError(t, l.Prune(2))
	assert.NoDirExists(t, stale)
	assert.DirExists(t, fresh, "a staging dir of a concurrent install must survive")
}
