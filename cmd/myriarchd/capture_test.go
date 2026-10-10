// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"sync"
)

// captureHandler records "LEVEL message" lines.
type captureHandler struct {
	mu    sync.Mutex
	lines *[]string
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.lines = append(*c.lines, r.Level.String()+" "+r.Message)
	return nil
}
func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }

func newCaptureLogger(h slog.Handler) *slog.Logger { return slog.New(h) }
