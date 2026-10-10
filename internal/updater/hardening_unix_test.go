// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- files owned by the supervisor's (less privileged) account -------------

func TestReadRegularFileRejectsSymlinkFIFOAndHugeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file types")
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.yaml")
	require.NoError(t, os.WriteFile(plain, []byte("a: 1\n"), 0o600))

	data, err := readRegularFile(plain, 1024)
	require.NoError(t, err)
	assert.Equal(t, "a: 1\n", string(data))

	link := filepath.Join(dir, "link.yaml")
	require.NoError(t, os.Symlink(plain, link))
	_, err = readRegularFile(link, 1024)
	require.Error(t, err, "a symlink in the last path component is not followed")

	fifo := filepath.Join(dir, "fifo.yaml")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	done := make(chan error, 1)
	go func() { _, err := readRegularFile(fifo, 1024); done <- err }()
	select {
	case err := <-done:
		require.Error(t, err, "a FIFO is rejected instead of blocking the caller")
	case <-time.After(5 * time.Second):
		t.Fatal("readRegularFile blocked on a FIFO")
	}

	big := filepath.Join(dir, "big.yaml")
	require.NoError(t, os.WriteFile(big, []byte(strings.Repeat("x", 2048)), 0o600))
	_, err = readRegularFile(big, 1024)
	require.Error(t, err)

	_, err = readRegularFile(filepath.Join(dir, "missing"), 1024)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReadSupervisorInstanceIDDoesNotBlockOnFIFOAndHidesContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file types")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "persistent_state.yaml")
	require.NoError(t, syscall.Mkfifo(p, 0o600))
	done := make(chan error, 1)
	go func() { _, err := readSupervisorInstanceID(p); done <- err }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("readSupervisorInstanceID blocked on a FIFO")
	}

	require.NoError(t, os.Remove(p))
	require.NoError(t, os.WriteFile(p, []byte("instance_id: TOP-SECRET-VALUE\n"), 0o600))
	_, err := readSupervisorInstanceID(p)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "TOP-SECRET-VALUE", "file content must not leak into errors")
}

// --- preflight: validate never resolves the config with the updater's rights -
