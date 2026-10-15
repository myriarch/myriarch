// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// databaseConfig writes a configuration file whose database section reads the DSN of pool from a file (no inline secret, so
// the command has nothing to warn about).
func databaseConfig(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	dsnFile := filepath.Join(t.TempDir(), "dsn")
	require.NoError(t, os.WriteFile(dsnFile, []byte(pool.Config().ConnString()), 0o600))
	return writeConfig(t, fmt.Sprintf("database:\n  dsnFile: %q\n", dsnFile))
}

// unsetEnv removes a variable for the rest of the test; the original value comes back afterwards.
func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "") // registers the restore
		require.NoError(t, os.Unsetenv(name))
	}
}

// migrate runs "myriarchd migrate" with args and returns the exit code and the two streams.
func migrate(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = execute(append([]string{"migrate"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// "myriarchd migrate" needs the database section alone: no public URL, no login path, no other secrets.
func TestMigrateNeedsTheDatabaseSettingsOnly(t *testing.T) {
	latest := latestMigration(t)
	level := currentLevel(t, pgtest.NewPool(t))
	want := fmt.Sprintf("schema version before: %d\nschema version after: %d\ncompatibility level: %d\n", latest, latest, level)

	t.Run("a file with the database section only", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		p := writeConfig(t, fmt.Sprintf("database:\n  dsn: %q\n", pool.Config().ConnString()))
		code, stdout, stderr := migrate(t, "--config", p)
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, want, stdout)
		assert.Contains(t, stderr, "database.dsn is an inline secret", "a DSN written into the file is reported, like at the start of the server")
		assert.NotContains(t, stderr, "postgres://", "a warning names the setting, never the value")
	})

	t.Run("no file, the DSN from the environment", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		t.Setenv("MYRIARCH_DATABASE_DSN", pool.Config().ConnString())
		code, stdout, stderr := migrate(t)
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, want, stdout)
	})

	t.Run("the DSN from a DSN file", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		dsnFile := filepath.Join(t.TempDir(), "dsn")
		require.NoError(t, os.WriteFile(dsnFile, []byte(pool.Config().ConnString()+"\n"), 0o600))
		code, stdout, stderr := migrate(t, "--config", writeConfig(t, fmt.Sprintf("database:\n  dsnFile: %q\n", dsnFile)))
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, want, stdout)
	})

	t.Run("a full server configuration, also one that the server would refuse", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		p := writeConfig(t, fmt.Sprintf(`server:
  publicUrl: http://not-https.example.com
auth:
  bootstrapAdmin:
    enabled: true
    tokenFile: /does/not/exist
opamp:
  enrollmentTokens:
    - name: one
      tokenFile: /does/not/exist
somethingFromAnotherVersion: true
database:
  dsn: %q
`, pool.Config().ConnString()))
		code, stdout, stderr := migrate(t, "--config", p)
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, want, stdout)
		// the server refuses the same file, which is why migrate must not depend on it
		var out bytes.Buffer
		assert.Equal(t, 1, execute([]string{"config", "validate", "--config", p, "--skip-files"}, &out, &out))
	})
}

func TestMigrateFailsWithoutUsableDatabaseSettings(t *testing.T) {
	unsetEnv(t, "MYRIARCH_DATABASE_DSN", "MYRIARCH_DATABASE_DSNFILE")
	for name, tc := range map[string]struct {
		args    []string
		content string
		want    string
	}{
		"no settings at all":        {nil, "", "load database settings: validation failed: database.dsn: is required (or database.dsnFile)"},
		"a file without a DSN":      {nil, "server:\n  listenAddr: :8080\n", "database.dsn: is required (or database.dsnFile)"},
		"a typo in the section":     {nil, "database:\n  dsnn: postgres://x\n", "field dsnn not found in type config.DatabaseConfig"},
		"an unreachable DSN file":   {nil, "database:\n  dsnFile: /does/not/exist\n", "database.dsnFile: cannot read /does/not/exist"},
		"an invalid connection URL": {nil, "database:\n  dsn: \"postgres://user:secretpw@host:badport/db\"\n", "is not a valid PostgreSQL connection string"},
	} {
		t.Run(name, func(t *testing.T) {
			args := tc.args
			if tc.content != "" {
				args = append([]string{"--config", writeConfig(t, tc.content)}, args...)
			}
			code, stdout, stderr := migrate(t, args...)
			assert.Equal(t, 1, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.want)
			assert.NotContains(t, stderr, "secretpw")
		})
	}
	t.Run("a configuration file that does not exist", func(t *testing.T) {
		code, _, stderr := migrate(t, "--config", filepath.Join(t.TempDir(), "missing.yaml"))
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr, "read config file")
	})
	t.Run("an argument", func(t *testing.T) {
		code, _, _ := migrate(t, "now")
		assert.Equal(t, 1, code)
	})
}

// migrate applies what is missing and reports the versions before and after; a second run changes nothing.
func TestMigrateAppliesPendingMigrationsAndRunsAgain(t *testing.T) {
	latest := latestMigration(t)
	pool := pgtest.NewPool(t)
	level := currentLevel(t, pool)
	rollBackTo(t, pool, 5)
	cfg := databaseConfig(t, pool)

	code, stdout, stderr := migrate(t, "--config", cfg)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, fmt.Sprintf("schema version before: 5\nschema version after: %d\ncompatibility level: %d\n", latest, level), stdout)
	assert.Contains(t, stderr, "migration applied")
	assert.Contains(t, stderr, "00006_schema_compat.sql")
	fingerprint := schemaFingerprint(t, pool)

	code, stdout, stderr = migrate(t, "--config", cfg)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, fmt.Sprintf("schema version before: %d\nschema version after: %d\ncompatibility level: %d\n", latest, latest, level), stdout)
	assert.NotContains(t, stderr, "migration applied", "the second run applies nothing")
	assert.Equal(t, fingerprint, schemaFingerprint(t, pool), "the second run changes nothing")
}

func TestMigrateNewerCompatibleSchemaIsKeptWithAWarning(t *testing.T) {
	latest := latestMigration(t)
	pool := pgtest.NewPool(t)
	level := currentLevel(t, pool)
	pretendNewerRelease(t, pool, latest+2, 0)
	cfg := databaseConfig(t, pool)
	before := schemaFingerprint(t, pool)

	code, stdout, stderr := migrate(t, "--config", cfg)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, fmt.Sprintf("schema version before: %d\nschema version after: %d\ncompatibility level: %d\n", latest+2, latest+2, level), stdout)
	assert.Contains(t, stderr, "level=WARN")
	assert.Contains(t, stderr, "newer than this myriarchd but compatible with it")
	assert.Equal(t, before, schemaFingerprint(t, pool))
}

func TestMigrateRefusesAnIncompatibleSchema(t *testing.T) {
	latest := latestMigration(t)
	pool := pgtest.NewPool(t)
	pretendNewerRelease(t, pool, latest+2, latest+2)
	cfg := databaseConfig(t, pool)
	before := schemaFingerprint(t, pool)

	code, stdout, stderr := migrate(t, "--config", cfg)
	assert.Equal(t, exitSchemaIncompatible, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "myriarchd: the database schema is incompatible with this myriarchd")
	assert.Contains(t, stderr, fmt.Sprintf("needs a myriarchd with schema level %d or higher (compatibility level %d)", latest+2, latest+2))
	assert.Equal(t, before, schemaFingerprint(t, pool), "an incompatible schema is not touched")
}

// --check reports the state by the exit code and changes nothing.
func TestMigrateCheck(t *testing.T) {
	latest := latestMigration(t)
	level := currentLevel(t, pgtest.NewPool(t))
	for name, tc := range map[string]struct {
		prepare func(t *testing.T, pool *pgxpool.Pool)
		code    int
		stdout  string
		stderr  string // part of it
	}{
		"current": {func(*testing.T, *pgxpool.Pool) {}, 0,
			fmt.Sprintf("schema version: %d\ncompatibility level: %d\nschema level of this myriarchd: %d\nstatus: current\n", latest, level, latest), ""},
		"pending": {func(t *testing.T, pool *pgxpool.Pool) { rollBackTo(t, pool, 5) }, exitMigrationsPending,
			fmt.Sprintf("schema version: 5\ncompatibility level: none\nschema level of this myriarchd: %d\nstatus: pending\n", latest), ""},
		"newer and compatible": {func(t *testing.T, pool *pgxpool.Pool) { pretendNewerRelease(t, pool, latest+1, 0) }, 0,
			fmt.Sprintf("schema version: %d\ncompatibility level: %d\nschema level of this myriarchd: %d\nstatus: newer\n", latest+1, level, latest), ""},
		"incompatible": {func(t *testing.T, pool *pgxpool.Pool) { pretendNewerRelease(t, pool, latest+1, latest+1) }, exitSchemaIncompatible,
			fmt.Sprintf("schema version: %d\ncompatibility level: %d\nschema level of this myriarchd: %d\nstatus: incompatible\n", latest+1, latest+1, latest),
			"the database schema is incompatible with this myriarchd"},
	} {
		t.Run(name, func(t *testing.T) {
			pool := pgtest.NewPool(t)
			tc.prepare(t, pool)
			before := schemaFingerprint(t, pool)
			cfg := databaseConfig(t, pool)

			code, stdout, stderr := migrate(t, "--config", cfg, "--check")
			assert.Equal(t, tc.code, code, stderr)
			assert.Equal(t, tc.stdout, stdout)
			if tc.stderr == "" {
				assert.Empty(t, stderr)
			} else {
				assert.Contains(t, stderr, tc.stderr)
			}
			assert.Equal(t, before, schemaFingerprint(t, pool), "--check changes nothing")
		})
	}

	t.Run("an empty database is pending and stays empty", func(t *testing.T) {
		pool := pgtest.NewPool(t)
		emptyDatabase(t, pool)
		code, stdout, stderr := migrate(t, "--config", databaseConfig(t, pool), "--check")
		assert.Equal(t, exitMigrationsPending, code, stderr)
		assert.Equal(t, fmt.Sprintf("schema version: 0\ncompatibility level: none\nschema level of this myriarchd: %d\nstatus: pending\n", latest), stdout)
		var tables int
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&tables))
		assert.Zero(t, tables, "--check creates nothing, not even the migration table")
	})

	t.Run("no usable settings is an error, not a state", func(t *testing.T) {
		unsetEnv(t, "MYRIARCH_DATABASE_DSN", "MYRIARCH_DATABASE_DSNFILE")
		code, stdout, _ := migrate(t, "--check")
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout)
	})
}

// The exit codes of --check are documented in the help text, which is also the CLI reference.
func TestMigrateHelpDocumentsTheExitCodes(t *testing.T) {
	var out bytes.Buffer
	require.Equal(t, 0, execute([]string{"migrate", "--help"}, &out, &out))
	help := out.String()
	for _, want := range []string{
		"--check", "database.dsn", "MYRIARCH_DATABASE_*",
		fmt.Sprintf("%d with --check: migrations are pending", exitMigrationsPending),
		fmt.Sprintf("%d the schema is incompatible", exitSchemaIncompatible),
	} {
		assert.Contains(t, strings.Join(strings.Fields(help), " "), strings.Join(strings.Fields(want), " "))
	}
}
