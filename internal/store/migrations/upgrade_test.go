// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/myriarch/myriarch/internal/store/migrations"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// v010Schema is the highest migration of v0.1.0, and v010Hashes are the SHA-256
// sums of its migration files. A database of v0.1.0 has exactly the schema of
// these files; they must never change (the upgrade test builds that schema from
// the files of this tree).
const v010Schema = 5

var v010Hashes = map[string]string{
	"00001_initial.sql":              "04fd32cec0aff98d7b3363d784e9f48a831f853334fc00510baa1d69b3c72160",
	"00002_indexes.sql":              "e3f8a51ea62ab486ddc7a2d8681c275f999bb40144fec91d7a02c8f883f2b6b7",
	"00003_security_bindings.sql":    "6882adf942e564671de54b9b4b8153d03df8ea31830f71e9f4ba117bb1602959",
	"00004_ui_version_selection.sql": "709b5a3b4f939db498707d82e9787b366a8e8fcc6150e48b89defd0579d4828a",
	"00005_bootstrap_admin.sql":      "2969147c63aa212dfe4a10e132338ca074e6c9811f17e3c869f262bf915d2921",
}

func TestMigrationsOfV010AreUnchanged(t *testing.T) {
	for name, want := range v010Hashes {
		b, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s changed since v0.1.0 (sha256 %s): never edit a migration that was released, add a new one", name, got)
		}
	}
}

func provider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUpgradeFromEmptyDatabase applies every migration, one after the other, to
// an empty database and checks the result against the template database that the
// other tests use (all migrations in one go).
func TestUpgradeFromEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewEmptyPool(t)
	p := provider(t, pool)
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	for v := int64(1); v <= latest; v++ {
		if _, err := p.UpByOne(ctx); err != nil {
			t.Fatalf("migration %d on an empty database: %v", v, err)
		}
		if got, err := p.GetDBVersion(ctx); err != nil || got != v {
			t.Fatalf("after migration %d the version is %d (%v)", v, got, err)
		}
	}
	if _, err := p.UpByOne(ctx); err == nil {
		t.Fatal("there must be no migration after the latest one")
	}
	if got, want := schemaDigest(t, pool), schemaDigest(t, pgtest.NewPool(t)); got != want {
		t.Fatalf("stepwise upgrade of an empty database gives a different schema than the template:\n%s\n----\n%s", got, want)
	}
}

// TestUpgradeFromV010 builds the schema of v0.1.0, puts data into it and
// upgrades to the latest migration. The data survives and the schema is the one
// of a new installation.
func TestUpgradeFromV010(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewEmptyPool(t)
	p := provider(t, pool)
	if _, err := p.UpTo(ctx, v010Schema); err != nil {
		t.Fatalf("schema of v0.1.0: %v", err)
	}
	const agent = "00000000-0000-7000-8000-0000000000a1"
	const user = "00000000-0000-7000-8000-0000000000b1"
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id) VALUES ($1)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, subject, provider, role) VALUES ($1, 'bootstrap:admin', 'bootstrap', 'admin')`, user); err != nil {
		t.Fatal(err)
	}

	v, err := migrations.Up(ctx, stdlib.OpenDBFromPool(pool), nil)
	if err != nil {
		t.Fatalf("upgrade from v0.1.0: %v", err)
	}
	latest, _ := migrations.Latest()
	if v != latest {
		t.Fatalf("version after the upgrade = %d, want %d", v, latest)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agents WHERE id = $1) + (SELECT count(*) FROM users WHERE id = $2)`, agent, user).Scan(&n); err != nil || n != 2 {
		t.Fatalf("data of v0.1.0 after the upgrade: %d rows (%v), want 2", n, err)
	}
	if got, want := schemaDigest(t, pool), schemaDigest(t, pgtest.NewPool(t)); got != want {
		t.Fatalf("upgraded schema differs from a new installation:\n%s\n----\n%s", got, want)
	}
}

// TestDatabaseEnvironment checks that the database environment that the test
// matrix asked for (PGTEST_TLS, PGTEST_COLLATION) is the one the tests ran on,
// so a matrix entry can not pass on the default environment by accident.
func TestDatabaseEnvironment(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	if os.Getenv(pgtest.TLSEnv) != "" {
		var ssl bool
		if err := pool.QueryRow(ctx, `SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&ssl); err != nil || !ssl {
			t.Fatalf("%s is set but the connection is not encrypted (%v)", pgtest.TLSEnv, err)
		}
	}
	if want := os.Getenv(pgtest.CollationEnv); want != "" {
		var provider, collate string
		if err := pool.QueryRow(ctx, `SELECT datlocprovider::text, datcollate FROM pg_database WHERE datname = current_database()`).Scan(&provider, &collate); err != nil {
			t.Fatal(err)
		}
		if collate == "C" && provider != "i" {
			t.Fatalf("%s=%q is set but the database uses the C collation", pgtest.CollationEnv, want)
		}
		// the collation is not the byte order: "a" sorts before "B"
		var first string
		if err := pool.QueryRow(ctx, `SELECT min(x) FROM (VALUES ('B'), ('a')) AS v(x)`).Scan(&first); err != nil {
			t.Fatal(err)
		}
		if first != "a" {
			t.Fatalf("min('B','a') = %q: the collation is the byte order", first)
		}
	}
}

// schemaDigest lists the columns, constraints and indexes of the public schema
// in a stable order.
func schemaDigest(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	const q = `SELECT l FROM (
SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
  FROM information_schema.columns WHERE table_schema = 'public'
UNION ALL
SELECT 'constraint ' || conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid)
  FROM pg_constraint WHERE connamespace = 'public'::regnamespace
UNION ALL
SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public'
) AS s(l) ORDER BY l COLLATE "C"`
	rows, err := pool.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []byte
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l...)
		out = append(out, '\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return string(out)
}
