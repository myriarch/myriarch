// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package webhookexec

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// A registry entry that points to a FIFO is refused without blocking the executor.
func TestRegistryFileThatIsAFIFOIsRefused(t *testing.T) {
	e := newExec(t, domain.ExecutorWebhook, config.ExecutorsConfig{}, nil)
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.fromFile(fifo, "webhook secret", true); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO must be refused")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}
