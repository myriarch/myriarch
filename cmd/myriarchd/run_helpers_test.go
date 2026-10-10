// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/eventbus/pgbus"
	"github.com/myriarch/myriarch/internal/store/postgres"
)

// openForReadiness opens the store and bus of cfg like run() does.
func openForReadiness(t *testing.T, cfg config.Config) (*postgres.DB, eventbus.Bus) {
	t.Helper()
	pool, err := postgres.Connect(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := pgbus.New(context.Background(), pool, "readiness-test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(); pool.Close() })
	return postgres.New(pool, nil), bus
}
