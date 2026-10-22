// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package updater

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyRefusesANamedPipeSourceWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	dst := filepath.Join(dir, "out")

	done := make(chan error, 1)
	go func() {
		_, err := runCLI(t, "copy", fifo, dst)
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "named pipe")
	case <-time.After(10 * time.Second):
		t.Fatal("copy blocked on a named pipe")
	}
	_, err := os.Lstat(dst)
	assert.True(t, os.IsNotExist(err))
}

func TestCopyRefusesADeviceSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out")
	_, err := runCLI(t, "copy", os.DevNull, dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device")
	_, err = os.Lstat(dst)
	assert.True(t, os.IsNotExist(err))
}

func TestFileCommandsRefuseASymlinkedDestinationDirectory(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(real, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(real, link))
	src := filepath.Join(base, "src")
	writeTestFile(t, src, "x", 0o644)

	_, err := runCLI(t, "install-self", link)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	_, err = runCLI(t, "copy", src, filepath.Join(link, "f"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	entries, err := os.ReadDir(real)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
