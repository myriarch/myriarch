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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
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

// overlap is what PrepareSchema reports for a start that overlaps with another replica that goes further.
type overlap struct {
	pool   *pgxpool.Pool
	status postgres.SchemaStatus
	err    error
	logs   string
}

// startWhileAnotherReplicaMigrates runs PrepareSchema (with migrateOnStart) on a schema at version 5 while another replica
// holds the advisory lock of the migrations (lock.DefaultLockID). As soon as PrepareSchema waits for the lock, the other replica
// applies the remaining migrations, records the migration latest+1 of a newer release, sets the compatibility level to
// newerLevel (0 leaves it alone) and releases the lock. It takes about five seconds, the interval in which the migration library
// tries the lock again.
func startWhileAnotherReplicaMigrates(t *testing.T, newerLevel int64) overlap {
	t.Helper()
	ctx := context.Background()
	latest := latestMigration(t)
	pool := pgtest.NewPool(t)
	rollBackTo(t, pool, 5)

	other, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	require.NoError(t, err)
	defer other.Close(ctx) // the end of the session releases the lock as well
	_, err = other.Exec(ctx, `SELECT pg_advisory_lock($1)`, lock.DefaultLockID)
	require.NoError(t, err)

	// A test that fails before the lock is released must not leave PrepareSchema waiting for it: the cleanup stops it before the
	// database is dropped.
	startCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	t.Cleanup(func() { cancel(); <-finished })
	var logs lockedBuffer
	done := make(chan overlap, 1)
	go func() {
		defer close(finished)
		st, err := postgres.PrepareSchema(startCtx, pool, true, testLogger(&logs))
		done <- overlap{pool: pool, status: st, err: err}
	}()

	// PrepareSchema has found pending migrations and waits for the lock as soon as the migration library has tried it and failed:
	// its connection then sits idle, and its last statement is the attempt. (starts_with: this query itself names the function,
	// and an idle pool connection that ran it before must not count.)
	require.Eventually(t, func() bool {
		var waiting int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND state = 'idle' AND starts_with(query, 'SELECT pg_try_advisory_lock')`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 20*time.Second, 25*time.Millisecond, "PrepareSchema does not wait for the advisory lock of the migrations")
	select {
	case o := <-done:
		t.Fatalf("PrepareSchema returned while the other replica held the lock: %v", o.err)
	default:
	}

	// The other replica holds the lock itself, so it applies the migrations without asking for it.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	_, err = p.Up(ctx)
	require.NoError(t, err)
	pretendVersion(t, pool, latest+1)
	if newerLevel != 0 {
		setLevel(t, pool, newerLevel)
	}
	var released bool
	require.NoError(t, other.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, lock.DefaultLockID).Scan(&released))
	require.True(t, released, "the other replica held the lock")

	select {
	case o := <-done:
		o.logs = logs.String()
		return o
	case <-time.After(30 * time.Second):
		t.Fatal("PrepareSchema did not return after the other replica released the lock")
		return overlap{}
	}
}

// While this replica waits for the advisory lock of the migrations, another one can go further than this binary: it applies the
// rest, and a migration of a newer release raises the compatibility level above the schema level of this binary. PrepareSchema
// reads the state again after its migration step, refuses, and applies nothing twice.
func TestPrepareSchemaRefusesASchemaThatAnotherReplicaMadeIncompatibleMeanwhile(t *testing.T) {
	latest := latestMigration(t)
	o := startWhileAnotherReplicaMigrates(t, latest+1)

	var ie *postgres.IncompatibleSchemaError
	require.True(t, errors.As(o.err, &ie), "the start is refused with an *IncompatibleSchemaError: %v", o.err)
	want := postgres.SchemaStatus{Version: latest + 1, Level: latest + 1, Supported: latest}
	assert.Equal(t, want, ie.Status)
	assert.Equal(t, want, o.status)
	assert.Contains(t, o.err.Error(), fmt.Sprintf("needs a myriarchd with schema level %d or higher", latest+1))
	assert.NotContains(t, o.logs, "migration applied", "this replica applied nothing: the other one had gone further")
	var recordedTwice int
	require.NoError(t, o.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM (SELECT version_id FROM goose_db_version WHERE is_applied GROUP BY version_id HAVING count(*) > 1) d`).Scan(&recordedTwice))
	assert.Zero(t, recordedTwice, "no migration is recorded twice")
}

// The same overlap with a newer release that leaves the compatibility level alone (the normal case of a rolling update): this
// binary is still compatible, so the start goes on with the warning for a newer schema.
func TestPrepareSchemaStartsWhenAnotherReplicaWentFurtherAndLeftTheLevel(t *testing.T) {
	latest := latestMigration(t)
	o := startWhileAnotherReplicaMigrates(t, 0)

	require.NoError(t, o.err)
	level := currentLevel(t, o.pool)
	assert.Equal(t, postgres.SchemaStatus{Version: latest + 1, Level: level, Supported: latest}, o.status)
	assert.Contains(t, o.logs, "level=WARN")
	assert.Contains(t, o.logs, "newer than this myriarchd but compatible with it")
	assert.Contains(t, o.logs, fmt.Sprintf("schemaVersion=%d", latest+1))
	assert.NotContains(t, o.logs, "migration applied", "this replica applied nothing: the other one had gone further")
}
