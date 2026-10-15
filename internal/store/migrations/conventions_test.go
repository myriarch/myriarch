// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/store/migrations"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

const (
	// conventionSince is the first migration whose header states its effect on the compatibility level.
	conventionSince = 6
	// baselineLevel is the compatibility level of every database that reaches migration 00006: 00003 is the last migration that
	// binaries before it cannot work with.
	baselineLevel = 3
)

var (
	rollingUpdateRE = regexp.MustCompile(`(?m)^-- Rolling update: (\S.*)$`)
	levelRE         = regexp.MustCompile(`(?m)^-- Compatibility level: (unchanged|raised to (\d+))\b`)
)

type migrationFile struct {
	version int64
	name    string
	header  string // the comment block before "-- +goose Up"
}

// readMigrations returns the migrations of fsys by version.
func readMigrations(t *testing.T, fsys fs.FS) []migrationFile {
	t.Helper()
	entries, err := fs.ReadDir(fsys, ".")
	require.NoError(t, err)
	var files []migrationFile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		require.True(t, ok, "%s: the file name must be NNNNN_description.sql", e.Name())
		v, err := strconv.ParseInt(prefix, 10, 64)
		require.NoError(t, err, "%s: the file name must start with the migration number", e.Name())
		raw, err := fs.ReadFile(fsys, e.Name())
		require.NoError(t, err)
		body := string(raw)
		up := strings.Index(body, "-- +goose Up")
		require.GreaterOrEqual(t, up, 0, "%s has no \"-- +goose Up\" section", e.Name())
		require.Contains(t, body, "-- +goose Down", "%s has no \"-- +goose Down\" section", e.Name())
		files = append(files, migrationFile{version: v, name: e.Name(), header: body[:up]})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files
}

// The schema level of a binary is the highest migration it embeds, and a database is compared with it by migration
// number: the numbers must have no gaps and no duplicates.
func TestMigrationNumbersAreConsecutive(t *testing.T) {
	files := readMigrations(t, migrations.FS)
	for i, f := range files {
		assert.Equal(t, int64(i+1), f.version, "%s: the migrations are numbered 1, 2, 3, ... without gaps", f.name)
	}
	latest, err := migrations.Latest()
	require.NoError(t, err)
	assert.Equal(t, files[len(files)-1].version, latest)
}

// declaredLevel returns what the header says about the compatibility level: 0 for "unchanged", otherwise the level. ok is
// false unless the header has exactly one "Compatibility level:" line.
func declaredLevel(f migrationFile) (level int64, ok bool) {
	m := levelRE.FindAllStringSubmatch(f.header, -1)
	if len(m) != 1 {
		return 0, false
	}
	if m[0][1] == "unchanged" {
		return 0, true
	}
	n, err := strconv.ParseInt(m[0][2], 10, 64)
	return n, err == nil
}

// headerProblems returns what is wrong with the header of a migration of the numbered convention.
func headerProblems(f migrationFile) []string {
	var out []string
	rolling := rollingUpdateRE.FindAllStringSubmatch(f.header, -1)
	if len(rolling) != 1 {
		out = append(out, `the header needs exactly one line "-- Rolling update: expand only." or "-- Rolling update: not compatible, <what stops working>"`)
	}
	level, ok := declaredLevel(f)
	if !ok {
		return append(out, `the header needs exactly one line "-- Compatibility level: unchanged." or "-- Compatibility level: raised to 00NNN."`)
	}
	if len(rolling) == 1 && strings.HasPrefix(rolling[0][1], "expand only") && level != 0 {
		out = append(out, `"expand only" means that the previous version keeps working: the compatibility level has to stay "unchanged"`)
	}
	if level != 0 && level > f.version {
		out = append(out, fmt.Sprintf("a migration cannot raise the level to %d, above its own number %d", level, f.version))
	}
	if level != 0 && level < baselineLevel {
		out = append(out, fmt.Sprintf("the level cannot be raised to %d, below the baseline %d", level, baselineLevel))
	}
	return out
}

// From 00006 on the header of a migration states two things: what a replica of the previous version does on the changed
// schema ("Rolling update:") and what happens to the compatibility level ("Compatibility level:").
func TestMigrationHeaders(t *testing.T) {
	checked := 0
	for _, f := range readMigrations(t, migrations.FS) {
		if f.version < conventionSince {
			continue
		}
		checked++
		assert.Empty(t, headerProblems(f), f.name)
	}
	require.NotZero(t, checked, "migration %d has to exist", conventionSince)
}

// The check of the headers must reject what it is meant to reject.
func TestHeaderCheckRejectsViolations(t *testing.T) {
	good := "-- Rolling update: expand only.\n-- Compatibility level: unchanged.\n"
	for name, tc := range map[string]struct {
		version int64
		header  string
		want    string // part of the expected problem, "" = no problem
	}{
		"expand only and unchanged":      {7, good, ""},
		"not compatible and raised":      {9, "-- Rolling update: not compatible, the column x is gone.\n-- Compatibility level: raised to 9.\n", ""},
		"raised to an earlier migration": {12, "-- Rolling update: not compatible, y.\n-- Compatibility level: raised to 10.\n", ""},
		"no rolling update line":         {7, "-- Compatibility level: unchanged.\n", "Rolling update"},
		"empty rolling update line":      {7, "-- Rolling update:\n-- Compatibility level: unchanged.\n", "Rolling update"},
		"two rolling update lines":       {7, good + "-- Rolling update: expand only.\n", "Rolling update"},
		"no level line":                  {7, "-- Rolling update: expand only.\n", "Compatibility level"},
		"a level line that says nothing": {7, "-- Rolling update: expand only.\n-- Compatibility level: maybe.\n", "Compatibility level"},
		"expand only but raised":         {7, "-- Rolling update: expand only.\n-- Compatibility level: raised to 7.\n", "expand only"},
		"raised above the own number":    {7, "-- Rolling update: not compatible, z.\n-- Compatibility level: raised to 8.\n", "above its own number"},
		"raised below the baseline":      {7, "-- Rolling update: not compatible, z.\n-- Compatibility level: raised to 2.\n", "below the baseline"},
	} {
		t.Run(name, func(t *testing.T) {
			problems := headerProblems(migrationFile{version: tc.version, name: "x.sql", header: tc.header})
			if tc.want == "" {
				assert.Empty(t, problems)
				return
			}
			require.NotEmpty(t, problems)
			assert.Contains(t, strings.Join(problems, "\n"), tc.want)
		})
	}
}

// levelOf returns the compatibility level of the database; ok is false while the table schema_compat does not exist.
func levelOf(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (level int64, ok bool) {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('schema_compat') IS NOT NULL`).Scan(&exists))
	if !exists {
		return 0, false
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT min_binary_schema FROM schema_compat`).Scan(&level))
	return level, true
}

// levelProblems applies the migrations of fsys from 00006 on, one after the other, down and up again, and compares the
// level in the database with the header: "unchanged" keeps the previous level, "raised to N" sets N (more than before),
// and Down restores the previous level. The level never exceeds the schema version. pool is a migrated database.
func levelProblems(ctx context.Context, t *testing.T, pool *pgxpool.Pool, fsys fs.FS) []string {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys)
	require.NoError(t, err)
	_, err = p.DownTo(ctx, conventionSince-1)
	require.NoError(t, err)
	_, ok := levelOf(ctx, t, pool)
	require.False(t, ok, "before migration %d the database has no compatibility level", conventionSince)

	var out []string
	problem := func(f migrationFile, format string, args ...any) {
		out = append(out, f.name+": "+fmt.Sprintf(format, args...))
	}
	before := int64(baselineLevel)
	for _, f := range readMigrations(t, fsys) {
		if f.version < conventionSince {
			continue
		}
		declared, _ := declaredLevel(f)
		res, err := p.UpByOne(ctx)
		require.NoError(t, err, f.name)
		require.Equal(t, f.version, res.Source.Version)
		after, ok := levelOf(ctx, t, pool)
		if !ok {
			problem(f, "Up does not leave the table schema_compat")
			return out
		}
		switch {
		case declared == 0 && after != before:
			problem(f, `declares "Compatibility level: unchanged" but Up changes min_binary_schema from %d to %d`, before, after)
		case declared != 0 && after != declared:
			problem(f, `declares "Compatibility level: raised to %d" but Up leaves min_binary_schema at %d`, declared, after)
		case declared != 0 && after <= before:
			problem(f, "raises the level to %d, which is not more than the previous level %d", after, before)
		}
		if after > f.version {
			problem(f, "min_binary_schema %d exceeds the highest applied migration %d", after, f.version)
		}

		_, err = p.Down(ctx)
		require.NoError(t, err, f.name)
		restored, ok := levelOf(ctx, t, pool)
		switch {
		case f.version == conventionSince && ok:
			problem(f, "Down does not drop the table schema_compat")
		case f.version != conventionSince && (!ok || restored != before):
			problem(f, "Down leaves min_binary_schema at %d instead of restoring %d", restored, before)
		}
		_, err = p.UpByOne(ctx)
		require.NoError(t, err, f.name)
		if again, _ := levelOf(ctx, t, pool); again != after {
			problem(f, "Up again gives the level %d instead of %d", again, after)
		}
		before = after
	}
	return out
}

// TestCompatibilityLevelFollowsTheHeaders checks the embedded migrations.
func TestCompatibilityLevelFollowsTheHeaders(t *testing.T) {
	assert.Empty(t, levelProblems(context.Background(), t, pgtest.NewPool(t), migrations.FS))
}

// TestLevelCheckRejectsViolations adds one more migration to a copy of the embedded ones, once good and several times wrong,
// and shows that the check names each violation.
func TestLevelCheckRejectsViolations(t *testing.T) {
	latest, err := migrations.Latest()
	require.NoError(t, err)
	n := latest + 1 // the number of the added migration
	set := func(level int64) string {
		return fmt.Sprintf("UPDATE schema_compat SET min_binary_schema = %d;", level)
	}
	migration := func(rolling, level, up, down string) string {
		return "-- Rolling update: " + rolling + "\n-- Compatibility level: " + level + "\n\n-- +goose Up\n" + up + "\n\n-- +goose Down\n" + down + "\n"
	}
	raised := fmt.Sprintf("raised to %d.", n)
	for name, tc := range map[string]struct {
		sql  func(prev int64) string // prev is the level after the embedded migrations
		want string                  // part of the expected problem, "" = none
	}{
		"expand only": {func(int64) string { return migration("expand only.", "unchanged.", "SELECT 1;", "SELECT 1;") }, ""},
		"raises and restores": {func(prev int64) string {
			return migration("not compatible, x.", raised, set(n), set(prev))
		}, ""},
		"claims unchanged but raises": {func(prev int64) string {
			return migration("not compatible, x.", "unchanged.", set(n), set(prev))
		}, `declares "Compatibility level: unchanged" but Up changes`},
		"claims a raise but does not": {func(int64) string {
			return migration("not compatible, x.", raised, "SELECT 1;", "SELECT 1;")
		}, fmt.Sprintf(`declares "Compatibility level: raised to %d" but Up leaves`, n)},
		"raises to another value than declared": {func(prev int64) string {
			return migration("not compatible, x.", raised, set(n+1), set(prev))
		}, fmt.Sprintf(`declares "Compatibility level: raised to %d" but Up leaves min_binary_schema at %d`, n, n+1)},
		"raises but does not restore": {func(int64) string {
			return migration("not compatible, x.", raised, set(n), "SELECT 1;")
		}, fmt.Sprintf("Down leaves min_binary_schema at %d instead of restoring", n)},
		"raises above the version": {func(prev int64) string {
			return migration("not compatible, x.", raised, set(n+1), set(prev))
		}, "exceeds the highest applied migration"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pool := pgtest.NewPool(t)
			prev, ok := levelOf(ctx, t, pool)
			require.True(t, ok)
			fsys := fstest.MapFS{fmt.Sprintf("%05d_x.sql", n): {Data: []byte(tc.sql(prev))}}
			entries, err := fs.ReadDir(migrations.FS, ".")
			require.NoError(t, err)
			for _, e := range entries {
				b, err := fs.ReadFile(migrations.FS, e.Name())
				require.NoError(t, err)
				fsys[e.Name()] = &fstest.MapFile{Data: b}
			}
			problems := levelProblems(ctx, t, pool, fsys)
			if tc.want == "" {
				assert.Empty(t, problems)
				return
			}
			require.NotEmpty(t, problems)
			assert.Contains(t, strings.Join(problems, "\n"), tc.want)
		})
	}
}

// TestSchemaCompatMigration checks 00006 on its own: one row with the baseline level, a table that cannot hold a second row or a
// level below 1, Down drops it and Up creates it again.
func TestSchemaCompatMigration(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, conventionSince-1)
	require.NoError(t, err)
	_, ok := levelOf(ctx, t, pool)
	require.False(t, ok, "before migration %d the database has no compatibility level", conventionSince)

	_, err = p.UpTo(ctx, conventionSince)
	require.NoError(t, err)
	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM schema_compat`).Scan(&rows))
	assert.Equal(t, 1, rows)
	level, ok := levelOf(ctx, t, pool)
	require.True(t, ok)
	assert.Equal(t, int64(baselineLevel), level)

	_, err = pool.Exec(ctx, `INSERT INTO schema_compat (min_binary_schema) VALUES (4)`)
	assert.Error(t, err, "the table holds exactly one row")
	_, err = pool.Exec(ctx, `INSERT INTO schema_compat (id, min_binary_schema) VALUES (false, 4)`)
	assert.Error(t, err, "the only allowed key is true")
	_, err = pool.Exec(ctx, `UPDATE schema_compat SET min_binary_schema = 0`)
	assert.Error(t, err, "a level below 1 is refused")

	_, err = p.Down(ctx)
	require.NoError(t, err)
	_, ok = levelOf(ctx, t, pool)
	assert.False(t, ok, "Down must drop schema_compat")
	_, err = p.UpByOne(ctx)
	require.NoError(t, err)
	level, ok = levelOf(ctx, t, pool)
	require.True(t, ok)
	assert.Equal(t, int64(baselineLevel), level, "Up again creates the row again")
}

// TestUpIsIdempotent applies the pending migrations twice: the second run changes nothing and reports the same version.
func TestUpIsIdempotent(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	latest, err := migrations.Latest()
	require.NoError(t, err)
	levelBefore, ok := levelOf(ctx, t, pool)
	require.True(t, ok)
	_, err = p.DownTo(ctx, conventionSince-1)
	require.NoError(t, err)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	first, err := migrations.Up(ctx, db, logger)
	require.NoError(t, err)
	assert.Equal(t, latest, first)
	assert.Equal(t, int(latest-conventionSince+1), strings.Count(logs.String(), "migration applied"), "the first run applies every pending migration")

	logs.Reset()
	second, err := migrations.Up(ctx, db, logger)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotContains(t, logs.String(), "migration applied", "the second run must apply nothing")
	level, ok := levelOf(ctx, t, pool)
	require.True(t, ok)
	assert.Equal(t, levelBefore, level, "applying the migrations again gives the level of the first time")
}
