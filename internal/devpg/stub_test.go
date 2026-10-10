// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !devpg

package devpg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/devpg"
)

func TestStartWithoutBuildTag(t *testing.T) {
	dsn, stop, err := devpg.Start(context.Background(), config.DevConfig{DataDir: t.TempDir()}, nil)
	if !errors.Is(err, devpg.ErrNotBuiltIn) {
		t.Fatalf("err = %v, want ErrNotBuiltIn", err)
	}
	if dsn != "" {
		t.Fatalf("dsn = %q, want empty", dsn)
	}
	if !strings.Contains(err.Error(), "--dsn") || !strings.Contains(err.Error(), "devpg") {
		t.Fatalf("message does not point to --dsn and the devpg tag: %v", err)
	}
	if stop == nil {
		t.Fatal("stop must be non-nil so that callers can defer it safely")
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestDSN(t *testing.T) {
	if got, want := devpg.DSN(54329), "postgres://myriarch:myriarch@127.0.0.1:54329/myriarch?sslmode=disable"; got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
}
