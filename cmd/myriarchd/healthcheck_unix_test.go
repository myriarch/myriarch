// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FIFO or a device named as the token file or the CA file is refused before it is opened: opening a FIFO waits for a writer.
func TestHealthcheckRefusesFilesThatAreNotRegular(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "token")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	// a writer that ends the wait if the command opens the FIFO although it must not
	t.Cleanup(func() {
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
	for _, flag := range []string{"--token-file", "--ca-file"} {
		done := make(chan [2]string, 1)
		var code int
		go func() {
			c, _, stderr := healthcheckRun(t, flag, fifo)
			code = c
			done <- [2]string{"", stderr}
		}()
		select {
		case got := <-done:
			assert.Equal(t, exitUsage, code, flag)
			assert.Contains(t, got[1], flag+": "+fifo+" is not a regular file")
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the command waits for a writer of the FIFO", flag)
		}
	}
	code, _, stderr := healthcheckRun(t, "--token-file", "/dev/null")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, stderr, "--token-file: /dev/null is not a regular file")
}
