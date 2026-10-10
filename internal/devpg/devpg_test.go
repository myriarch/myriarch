// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build devpg

package devpg_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/devpg"
)

func freePort(t *testing.T) uint32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestStartPersistReuse starts the embedded server, writes data, restarts it
// and checks persistence, and verifies the "reuse a running server" path.
func TestStartPersistReuse(t *testing.T) {
	if os.Getenv("PGTEST_DSN") != "" {
		t.Skip("PGTEST_DSN is set: the database tests use that server; this test starts its own embedded PostgreSQL")
	}
	if testing.Short() {
		t.Skip("starts a real PostgreSQL")
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.DevConfig{DataDir: t.TempDir(), PostgresPort: freePort(t)}
	ctx := context.Background()

	dsn, stop, err := devpg.Start(ctx, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if dsn != devpg.DSN(cfg.PostgresPort) {
		t.Fatalf("dsn %q", dsn)
	}
	c := connect(t, dsn)
	var tz, enc, collate string
	if err := c.QueryRow(ctx, `SHOW timezone`).Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRow(ctx, `SHOW server_encoding`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if err := c.QueryRow(ctx, `SELECT datcollate FROM pg_database WHERE datname = 'myriarch'`).Scan(&collate); err != nil {
		t.Fatal(err)
	}
	if tz != "UTC" || enc != "UTF8" || collate != "C" {
		t.Fatalf("hardening: timezone=%s encoding=%s collate=%s (must not inherit the shell locale)", tz, enc, collate)
	}
	if _, err := c.Exec(ctx, `CREATE TABLE persisted (v text); INSERT INTO persisted VALUES ('survives restarts')`); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)

	// a second Start while the server runs reuses it
	dsn2, stop2, err := devpg.Start(ctx, cfg, quiet)
	if err != nil || dsn2 != dsn {
		t.Fatalf("reuse: %v %q", err, dsn2)
	}
	if err := stop2(); err != nil { // no-op: must not stop the server
		t.Fatal(err)
	}
	c = connect(t, dsn)
	c.Close(ctx)

	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("second stop must be a no-op: %v", err)
	}
	if _, err := pgx.Connect(ctx, dsn); err == nil {
		t.Fatal("server must be down after stop")
	}

	// restart: data is still there
	dsn3, stop3, err := devpg.Start(ctx, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop3() }()
	c = connect(t, dsn3)
	defer c.Close(ctx)
	var v string
	if err := c.QueryRow(ctx, `SELECT v FROM persisted`).Scan(&v); err != nil || v != "survives restarts" {
		t.Fatalf("persisted data: %q %v", v, err)
	}
}
