// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/store/migrations"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func latestMigration(t *testing.T) int64 {
	t.Helper()
	latest, err := migrations.Latest()
	require.NoError(t, err)
	return latest
}

// rollBackTo reverts the migrations above version with their Down sections.
func rollBackTo(t *testing.T, pool *pgxpool.Pool, version int64) {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	_, err = p.DownTo(context.Background(), version)
	require.NoError(t, err)
}

// currentLevel returns the compatibility level of a database that is migrated to the latest migration (a clone of the migrated
// template). The tests compare with it instead of a literal, so that a later migration that raises the level does not break them.
func currentLevel(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var level int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT min_binary_schema FROM schema_compat`).Scan(&level))
	return level
}

// emptyDatabase removes everything that the migrations created, including the migration table: what a new database looks like.
func emptyDatabase(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rollBackTo(t, pool, 0)
	_, err := pool.Exec(context.Background(), `DROP TABLE goose_db_version`)
	require.NoError(t, err)
}

// pretendNewerRelease records a migration of a newer release as applied and sets the compatibility level the way that
// release would (0: leave the level alone).
func pretendNewerRelease(t *testing.T, pool *pgxpool.Pool, version, level int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, version)
	require.NoError(t, err)
	if level != 0 {
		_, err = pool.Exec(context.Background(), `UPDATE schema_compat SET min_binary_schema = $1`, level)
		require.NoError(t, err)
	}
}

// schemaFingerprint is what a command must not change when it only checks or refuses: the applied migrations and the level.
func schemaFingerprint(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var versions, level string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COALESCE(string_agg(version_id::text || ':' || is_applied::text, ',' ORDER BY id), '') FROM goose_db_version`).Scan(&versions))
	var haveCompat bool
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT to_regclass('schema_compat') IS NOT NULL`).Scan(&haveCompat))
	if haveCompat {
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT min_binary_schema::text FROM schema_compat`).Scan(&level))
	}
	return versions + "|" + level
}

// captureStderr redirects os.Stderr, where run() creates its logger, until the returned function is called (or the test ends).
// The function restores the stream and the default logger and returns everything that was written. Call it after the server
// has stopped.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig, prevLogger := os.Stderr, slog.Default()
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, r); close(done) }()
	var once sync.Once
	var out string
	stop := func() string {
		once.Do(func() {
			os.Stderr = orig
			slog.SetDefault(prevLogger)
			_ = w.Close()
			<-done
			_ = r.Close()
			out = buf.String()
		})
		return out
	}
	t.Cleanup(func() { stop() })
	return stop
}

// runUntilItReturns runs the server and waits for run() to return on its own, which happens when the start is refused.
func runUntilItReturns(t *testing.T, addr string, runIt func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runIt(ctx) }()
	select {
	case err := <-done:
		// a refused start never opened the listener
		if c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond); derr == nil {
			_ = c.Close()
			t.Errorf("something listens on %s after the start was refused", addr)
		}
		return err
	case <-time.After(30 * time.Second):
		cancel()
		<-done
		t.Fatal("run() did not return: the start was not refused")
		return nil
	}
}

// An incompatible schema is refused before anything else starts, whatever database.migrateOnStart says, and is not touched.
func TestStartRefusesAnIncompatibleSchema(t *testing.T) {
	latest := latestMigration(t)
	for _, migrate := range []bool{true, false} {
		t.Run(fmt.Sprintf("migrateOnStart=%v", migrate), func(t *testing.T) {
			pool := pgtest.NewPool(t)
			pretendNewerRelease(t, pool, latest+2, latest+2)
			before := schemaFingerprint(t, pool)
			cfg, addr := devConfigDSN(t, pool.Config().ConnString())
			cfg.Database.MigrateOnStart = migrate

			err := runUntilItReturns(t, addr, func(ctx context.Context) error { return run(ctx, cfg, runOptions{dev: true}) })
			var incompatible *postgres.IncompatibleSchemaError
			require.True(t, errors.As(err, &incompatible), "the start is refused with an *IncompatibleSchemaError: %v", err)
			assert.Equal(t, fmt.Sprintf("the database schema is incompatible with this myriarchd: the database is at schema version %d and needs a myriarchd with schema level %d or higher (compatibility level %d), "+
				"this myriarchd has schema level %d; start a newer myriarchd release or restore a backup of the database from before the upgrade", latest+2, latest+2, latest+2, latest), err.Error())
			assert.Equal(t, before, schemaFingerprint(t, pool))
		})
	}
}

// A schema that lacks migrations is refused at once when database.migrateOnStart is off: the server does not start and fail
// later in some component.
func TestStartRefusesAnOlderSchemaWithoutMigrateOnStart(t *testing.T) {
	pool := pgtest.NewPool(t)
	rollBackTo(t, pool, 5)
	before := schemaFingerprint(t, pool)
	cfg, addr := devConfigDSN(t, pool.Config().ConnString())
	cfg.Database.MigrateOnStart = false

	err := runUntilItReturns(t, addr, func(ctx context.Context) error { return run(ctx, cfg, runOptions{dev: true}) })
	var outdated *postgres.OutdatedSchemaError
	require.True(t, errors.As(err, &outdated), "the start is refused with an *OutdatedSchemaError: %v", err)
	assert.Contains(t, err.Error(), "the database schema is at version 5")
	assert.Contains(t, err.Error(), `run "myriarchd migrate" first, or set database.migrateOnStart to true`)
	assert.Equal(t, before, schemaFingerprint(t, pool), "nothing is migrated")
}

// With database.migrateOnStart (the default) a schema that lacks migrations is migrated and the server starts.
func TestStartMigratesAnOlderSchema(t *testing.T) {
	latest := latestMigration(t)
	pool := pgtest.NewPool(t)
	level := currentLevel(t, pool)
	rollBackTo(t, pool, 5)
	cfg, addr := devConfigDSN(t, pool.Config().ConnString())
	require.True(t, cfg.Database.MigrateOnStart)

	startRun(t, cfg, addr) // waits for /readyz
	status, err := postgres.ReadSchemaStatus(context.Background(), pool)
	require.NoError(t, err)
	assert.Equal(t, latest, status.Version)
	assert.Equal(t, level, status.Level)
}

// A schema that is newer than the binary but compatible with it starts the server with a warning (the situation after a
// rollback to an older release, or while a rolling update is under way).
func TestStartWarnsOnANewerCompatibleSchemaAndStarts(t *testing.T) {
	latest := latestMigration(t)
	for _, migrate := range []bool{true, false} {
		t.Run(fmt.Sprintf("migrateOnStart=%v", migrate), func(t *testing.T) {
			pool := pgtest.NewPool(t)
			level := currentLevel(t, pool)
			pretendNewerRelease(t, pool, latest+2, 0)
			before := schemaFingerprint(t, pool)
			cfg, addr := devConfigDSN(t, pool.Config().ConnString())
			cfg.Database.MigrateOnStart = migrate
			logs := captureStderr(t)

			rs := startRun(t, cfg, addr) // waits for /readyz: the server is ready
			require.NoError(t, rs.stop(t))
			out := logs()
			assert.Contains(t, out, "WARN")
			assert.Contains(t, out, "the database schema is newer than this myriarchd but compatible with it")
			assert.Contains(t, out, fmt.Sprintf("schemaVersion=%d", latest+2))
			assert.Contains(t, out, fmt.Sprintf("compatibilityLevel=%d", level))
			assert.Contains(t, out, fmt.Sprintf("schemaLevel=%d", latest))
			assert.Equal(t, before, schemaFingerprint(t, pool), "the newer schema is left alone")
		})
	}
}

// The log line of a normal start names the three numbers.
func TestStartLogsTheSchemaState(t *testing.T) {
	latest := latestMigration(t)
	pool := pgtest.NewPool(t)
	level := currentLevel(t, pool)
	cfg, addr := devConfigDSN(t, pool.Config().ConnString())
	cfg.Telemetry.LogLevel = "info"
	logs := captureStderr(t)

	rs := startRun(t, cfg, addr)
	require.NoError(t, rs.stop(t))
	out := logs()
	assert.Contains(t, out, "database schema ready")
	assert.Contains(t, out, fmt.Sprintf("version=%d", latest))
	assert.Contains(t, out, fmt.Sprintf("compatibilityLevel=%d", level))
	assert.Contains(t, out, fmt.Sprintf("schemaLevel=%d", latest))
	assert.NotContains(t, out, "newer than this myriarchd")
}

// /readyz checks the schema version only until that check first succeeds (docs/design/opamp-server.md, "Start, migrations and
// schema compatibility"): the start has made the schema fit, so the first probe passes, and a running replica reads neither the
// schema version nor the compatibility level again. A restore of an older backup or a raised compatibility level is therefore
// not noticed; a closure that has not passed yet still reports a schema that is behind (TestReadinessReportsSchemaProblems).
func TestReadinessChecksTheSchemaOnlyUntilItFirstSucceeds(t *testing.T) {
	latest := latestMigration(t)
	for name, tc := range map[string]struct {
		change       func(t *testing.T, pool *pgxpool.Pool)
		freshFailure string // what a closure that has not passed yet reports after the change, "" = ready
	}{
		"an older backup is restored": {
			func(t *testing.T, pool *pgxpool.Pool) { rollBackTo(t, pool, 5) },
			"database schema is at version 5",
		},
		"another replica raises the compatibility level": {
			func(t *testing.T, pool *pgxpool.Pool) { pretendNewerRelease(t, pool, latest+1, latest+1) },
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pool := pgtest.NewPool(t)
			cfg, _ := devConfigDSN(t, pool.Config().ConnString())
			db, bus := openForReadiness(t, cfg)
			ready := readiness(db, bus)
			require.NoError(t, ready(ctx), "the first probe passes on the schema that the start left")

			tc.change(t, pool)
			assert.NoError(t, ready(ctx), "a pass is kept: the running replica does not read the schema again")

			fresh := readiness(db, bus)(ctx)
			if tc.freshFailure == "" {
				assert.NoError(t, fresh, "/readyz does not read the compatibility level at all")
				return
			}
			require.Error(t, fresh)
			assert.Contains(t, fresh.Error(), tc.freshFailure)
		})
	}
}
