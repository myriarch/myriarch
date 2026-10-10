// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/open-telemetry/opamp-go/client/types"
)

// slogAdapter adapts slog to the opamp-go client logger interface.
type slogAdapter struct{ l *slog.Logger }

var _ types.Logger = slogAdapter{}

func (a slogAdapter) Debugf(ctx context.Context, format string, v ...any) {
	a.l.Debug(fmt.Sprintf(format, v...), "component", "opamp-client")
}

func (a slogAdapter) Errorf(ctx context.Context, format string, v ...any) {
	a.l.Error(fmt.Sprintf(format, v...), "component", "opamp-client")
}

// newLogger builds the slog logger from the config: stderr plus an optional
// size-rotated file (needed for Windows services where stderr is not
// captured).
func newLogger(cfg LogConfig, stderr io.Writer) (*slog.Logger, io.Closer, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(cfg.Level))); err != nil {
		level = slog.LevelInfo
	}
	var w io.Writer = stderr
	var closer io.Closer = nopCloser{}
	if cfg.File != "" {
		rf, err := newRotatingFile(cfg.File, 10<<20, 3)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		w = io.MultiWriter(stderr, rf)
		closer = rf
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h), closer, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// rotatingFile is a minimal size-based rotating log file (keeps `keep`
// old files: name.1 ... name.N).
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func newRotatingFile(path string, maxBytes int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: maxBytes, keep: keep}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.max && r.size > 0 {
		_ = r.f.Close()
		for i := r.keep - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
		}
		_ = os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
