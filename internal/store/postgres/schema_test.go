// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/store/migrations"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// lockedBuffer is a log destination that several goroutines write to.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLogger(out *lockedBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

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

// pretendVersion records a migration of a newer release as applied, which is what a database that a newer release has
// migrated looks like to this binary.
func pretendVersion(t *testing.T, pool *pgxpool.Pool, version int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, version)
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

func setLevel(t *testing.T, pool *pgxpool.Pool, level int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE schema_compat SET min_binary_schema = $1`, level)
	require.NoError(t, err)
}

func TestReadSchemaStatus(t *testing.T) {
	latest := latestMigration(t)
	ctx := context.Background()
	level := currentLevel(t, pgtest.NewPool(t))
	type want struct {
		version, level           int64
		pending, newer, incompat bool
	}
	for name, tc := range map[string]struct {
		prepare func(t *testing.T, pool *pgxpool.Pool)
		want    want
	}{
		"current": {func(*testing.T, *pgxpool.Pool) {}, want{version: latest, level: level}},
		"older than the compatibility record": {func(t *testing.T, pool *pgxpool.Pool) { rollBackTo(t, pool, 5) },
			want{version: 5, level: 0, pending: true}},
		"empty database": {emptyDatabase, want{version: 0, level: 0, pending: true}},
		"newer and compatible": {func(t *testing.T, pool *pgxpool.Pool) { pretendVersion(t, pool, latest+2) },
			want{version: latest + 2, level: level, newer: true}},
		"newer and compatible with the level of this binary": {func(t *testing.T, pool *pgxpool.Pool) {
			pretendVersion(t, pool, latest+2)
			setLevel(t, pool, latest)
		}, want{version: latest + 2, level: latest, newer: true}},
		"newer and incompatible": {func(t *testing.T, pool *pgxpool.Pool) {
			pretendVersion(t, pool, latest+2)
			setLevel(t, pool, latest+1)
		}, want{version: latest + 2, level: latest + 1, newer: true, incompat: true}},
	} {
		t.Run(name, func(t *testing.T) {
			pool := pgtest.NewPool(t)
			tc.prepare(t, pool)
			st, err := postgres.ReadSchemaStatus(ctx, pool)
			require.NoError(t, err)
			assert.Equal(t, tc.want.version, st.Version, "version")
			assert.Equal(t, tc.want.level, st.Level, "level")
			assert.Equal(t, latest, st.Supported, "schema level of this binary")
			assert.Equal(t, tc.want.pending, st.Pending(), "pending")
			assert.Equal(t, tc.want.newer, st.Newer(), "newer")
			assert.Equal(t, tc.want.incompat, st.Incompatible(), "incompatible")
		})
	}

	t.Run("a schema from 00006 on without the compatibility table is an error", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		_, err := pool.Exec(ctx, `DROP TABLE schema_compat`)
		require.NoError(t, err)
		_, err = postgres.ReadSchemaStatus(ctx, pool)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the table schema_compat is missing")
		_, err = postgres.PrepareSchema(ctx, pool, true, nil)
		require.Error(t, err, "the start is refused: without the level nobody knows whether this binary fits")
	})

	t.Run("the compatibility table without a row is an error", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		_, err := pool.Exec(ctx, `DELETE FROM schema_compat`)
		require.NoError(t, err)
		_, err = postgres.ReadSchemaStatus(ctx, pool)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "schema_compat has no row")
	})
}

// versionOf returns the highest applied migration and the number of rows of the migration table.
func versionOf(t *testing.T, pool *pgxpool.Pool) (version int64, rows int) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0), count(*) FROM goose_db_version`).Scan(&version, &rows))
	return version, rows
}

func TestPrepareSchemaOnACurrentSchema(t *testing.T) {
	pool := pgtest.NewPool(t)
	var logs lockedBuffer
	versionBefore, rowsBefore := versionOf(t, pool)

	st, err := postgres.PrepareSchema(context.Background(), pool, true, testLogger(&logs))
	require.NoError(t, err)
	assert.Equal(t, latestMigration(t), st.Version)
	assert.Equal(t, currentLevel(t, pool), st.Level)
	assert.Empty(t, logs.String(), "a current schema logs nothing")
	versionAfter, rowsAfter := versionOf(t, pool)
	assert.Equal(t, versionBefore, versionAfter)
	assert.Equal(t, rowsBefore, rowsAfter)

	// without migrateOnStart a current schema starts as well
	_, err = postgres.PrepareSchema(context.Background(), pool, false, testLogger(&logs))
	require.NoError(t, err)
}

// A schema that is newer than the binary but compatible with it logs a warning and starts, with or without migrateOnStart;
// nothing is changed.
func TestPrepareSchemaWarnsOnANewerCompatibleSchema(t *testing.T) {
	latest := latestMigration(t)
	for _, migrate := range []bool{true, false} {
		t.Run(fmt.Sprintf("migrateOnStart=%v", migrate), func(t *testing.T) {
			pool := pgtest.NewPool(t)
			level := currentLevel(t, pool)
			pretendVersion(t, pool, latest+3)
			versionBefore, rowsBefore := versionOf(t, pool)
			var logs lockedBuffer

			st, err := postgres.PrepareSchema(context.Background(), pool, migrate, testLogger(&logs))
			require.NoError(t, err)
			assert.Equal(t, latest+3, st.Version)
			out := logs.String()
			assert.Contains(t, out, "level=WARN")
			assert.Contains(t, out, "newer than this myriarchd but compatible with it")
			assert.Contains(t, out, fmt.Sprintf("schemaVersion=%d", latest+3))
			assert.Contains(t, out, fmt.Sprintf("compatibilityLevel=%d", level))
			assert.Contains(t, out, fmt.Sprintf("schemaLevel=%d", latest))
			assert.NotContains(t, out, "migration applied")
			versionAfter, rowsAfter := versionOf(t, pool)
			assert.Equal(t, versionBefore, versionAfter)
			assert.Equal(t, rowsBefore, rowsAfter)
		})
	}
}

// A compatibility level above the schema level of the binary refuses the start with an error that names the levels and the
// two ways out, with or without migrateOnStart, and changes nothing.
func TestPrepareSchemaRefusesAnIncompatibleSchema(t *testing.T) {
	latest := latestMigration(t)
	for _, migrate := range []bool{true, false} {
		t.Run(fmt.Sprintf("migrateOnStart=%v", migrate), func(t *testing.T) {
			pool := pgtest.NewPool(t)
			pretendVersion(t, pool, latest+2)
			setLevel(t, pool, latest+2)
			versionBefore, rowsBefore := versionOf(t, pool)
			var logs lockedBuffer

			st, err := postgres.PrepareSchema(context.Background(), pool, migrate, testLogger(&logs))
			require.Error(t, err)
			var ie *postgres.IncompatibleSchemaError
			require.True(t, errors.As(err, &ie), "the error is an *IncompatibleSchemaError: %v", err)
			assert.Equal(t, latest+2, st.Version)
			assert.Equal(t, latest+2, st.Level)
			msg := err.Error()
			for _, want := range []string{
				"the database schema is incompatible with this myriarchd",
				fmt.Sprintf("schema version %d", latest+2),
				fmt.Sprintf("schema level %d or higher (compatibility level %d)", latest+2, latest+2),
				fmt.Sprintf("this myriarchd has schema level %d", latest),
				"start a newer myriarchd release or restore a backup of the database from before the upgrade",
			} {
				assert.Contains(t, msg, want)
			}
			assert.NotContains(t, logs.String(), "migration applied", "an incompatible schema is not touched")
			versionAfter, rowsAfter := versionOf(t, pool)
			assert.Equal(t, versionBefore, versionAfter)
			assert.Equal(t, rowsBefore, rowsAfter)
		})
	}
}

// A schema that lacks migrations is refused when migrateOnStart is off (the error names the setting and the command) and
// migrated otherwise.
func TestPrepareSchemaOnAnOlderSchema(t *testing.T) {
	latest := latestMigration(t)

	t.Run("migrateOnStart=false refuses", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		rollBackTo(t, pool, 5)
		var logs lockedBuffer
		st, err := postgres.PrepareSchema(context.Background(), pool, false, testLogger(&logs))
		require.Error(t, err)
		var oe *postgres.OutdatedSchemaError
		require.True(t, errors.As(err, &oe), "the error is an *OutdatedSchemaError: %v", err)
		assert.Equal(t, int64(5), st.Version)
		assert.Contains(t, err.Error(), "the database schema is at version 5 but this myriarchd needs version "+fmt.Sprint(latest))
		assert.Contains(t, err.Error(), "database.migrateOnStart is false")
		assert.Contains(t, err.Error(), `run "myriarchd migrate" first`)
		version, _ := versionOf(t, pool)
		assert.Equal(t, int64(5), version, "nothing is migrated")
		assert.NotContains(t, logs.String(), "migration applied")
	})

	t.Run("migrateOnStart=true migrates, a second start changes nothing", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		level := currentLevel(t, pool)
		rollBackTo(t, pool, 5)
		var logs lockedBuffer
		st, err := postgres.PrepareSchema(context.Background(), pool, true, testLogger(&logs))
		require.NoError(t, err)
		assert.Equal(t, latest, st.Version)
		assert.Equal(t, level, st.Level)
		assert.False(t, st.Pending())
		assert.Contains(t, logs.String(), "migration applied")
		assert.Contains(t, logs.String(), "00006_schema_compat.sql")

		var second lockedBuffer
		again, err := postgres.PrepareSchema(context.Background(), pool, true, testLogger(&second))
		require.NoError(t, err)
		assert.Equal(t, st, again)
		assert.Empty(t, second.String(), "the second start applies nothing and logs nothing")
	})

	t.Run("an empty database is migrated from the start", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		level := currentLevel(t, pool)
		emptyDatabase(t, pool)
		st, err := postgres.ReadSchemaStatus(context.Background(), pool)
		require.NoError(t, err)
		assert.Equal(t, int64(0), st.Version)
		var logs lockedBuffer
		st, err = postgres.PrepareSchema(context.Background(), pool, true, testLogger(&logs))
		require.NoError(t, err)
		assert.Equal(t, latest, st.Version)
		assert.Equal(t, level, st.Level)
		assert.Equal(t, int(latest), strings.Count(logs.String(), "migration applied"))
	})
}

// Replicas that start together on an old schema: the migrations run once (advisory lock), every replica ends on the current
// schema.
func TestPrepareSchemaWithSeveralReplicasStartingTogether(t *testing.T) {
	pool := pgtest.NewPool(t)
	rollBackTo(t, pool, 5)
	latest := latestMigration(t)
	var logs lockedBuffer

	const replicas = 4
	statuses := make([]postgres.SchemaStatus, replicas)
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], errs[i] = postgres.PrepareSchema(context.Background(), pool, true, testLogger(&logs))
		}()
	}
	wg.Wait()
	for i := range replicas {
		require.NoError(t, errs[i], "replica %d", i)
		assert.Equal(t, latest, statuses[i].Version, "replica %d", i)
	}
	assert.Equal(t, int(latest-5), strings.Count(logs.String(), "migration applied"), "every migration is applied exactly once")
}
