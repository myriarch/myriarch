// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgtest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/store/migrations"
)

var (
	shared   *Server
	sharedMu sync.Mutex
)

// TemplateName is the name of the migrated template database.
const TemplateName = brand.Name + "_tmpl"

// RunWithMigratedTemplate starts the server, migrates the template database
// with all myriarch migrations, runs the tests and stops the server. Use it
// from TestMain: os.Exit(pgtest.RunWithMigratedTemplate(m)).
//
// Set PGTEST_SKIP=1 to skip database tests (they then call t.Skip).
func RunWithMigratedTemplate(m *testing.M) int {
	if os.Getenv("PGTEST_SKIP") != "" {
		return m.Run()
	}
	ctx := context.Background()
	srv, err := Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	defer func() { _ = srv.Stop() }()
	err = srv.PrepareTemplate(ctx, TemplateName, func(ctx context.Context, p *pgxpool.Pool) error {
		db := stdlib.OpenDBFromPool(p)
		defer db.Close()
		_, err := migrations.Up(ctx, db, nil)
		return err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: migrate template:", err, "\n", srv.Log())
		return 1
	}
	sharedMu.Lock()
	shared = srv
	sharedMu.Unlock()
	return m.Run()
}

// NewPool returns a pool to a fresh database cloned from the migrated
// template. The database is dropped when the test ends. Requires
// RunWithMigratedTemplate in TestMain; otherwise the test is skipped.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	sharedMu.Lock()
	srv := shared
	sharedMu.Unlock()
	if srv == nil {
		t.Skip("pgtest: no server (use pgtest.RunWithMigratedTemplate in TestMain, or PGTEST_SKIP is set)")
	}
	pool, cleanup, err := srv.NewDatabase(context.Background())
	if err != nil {
		t.Fatalf("pgtest: new database: %v", err)
	}
	t.Cleanup(cleanup)
	return pool
}

// NewEmptyPool returns a pool to a fresh database without a schema (for tests
// of the migrations themselves). The database is dropped when the test ends.
// Requires RunWithMigratedTemplate in TestMain; otherwise the test is skipped.
func NewEmptyPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	srv := Shared()
	if srv == nil {
		t.Skip("pgtest: no server (use pgtest.RunWithMigratedTemplate in TestMain, or PGTEST_SKIP is set)")
	}
	pool, cleanup, err := srv.NewEmptyDatabase(context.Background())
	if err != nil {
		t.Fatalf("pgtest: new empty database: %v", err)
	}
	t.Cleanup(cleanup)
	return pool
}

// Shared returns the running server (nil outside RunWithMigratedTemplate).
func Shared() *Server {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	return shared
}
