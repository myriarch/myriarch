// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package migrations_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/myriarch/myriarch/internal/store/migrations"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

// TestSchema verifies that the initial schema applies on a real PostgreSQL
// and that the generated config_sync column behaves as specified in
// domain.ConfigSyncState.
func TestSchema(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()

	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 24 {
		t.Fatalf("expected 24 tables, got %d", tables)
	}

	cases := []struct {
		name, setup, want string
	}{
		{"unassigned", `fleet_name = NULL`, "none"},
		{"in_sync", `fleet_name='f', assigned_config_hash='\x01', desired_config_hash='\x01', remote_config_hash='\x01', remote_config_status='applied'`, "in_sync"},
		{"pending", `fleet_name='f', assigned_config_hash='\x01', desired_config_hash='\x01', remote_config_hash='\x02', remote_config_status='applied'`, "pending"},
		{"applying", `fleet_name='f', assigned_config_hash='\x01', desired_config_hash='\x01', remote_config_hash='\x01', remote_config_status='applying'`, "pending"},
		{"drift", `fleet_name='f', assigned_config_hash='\x02', desired_config_hash='\x01', remote_config_hash='\x01', remote_config_status='applied'`, "drift"},
		{"failed", `fleet_name='f', assigned_config_hash='\x01', desired_config_hash='\x01', failed_config_hash='\x01', remote_config_hash='\x00', remote_config_status='failed'`, "failed"},
	}
	for i, c := range cases {
		id := []string{
			"00000000-0000-7000-8000-000000000001", "00000000-0000-7000-8000-000000000002",
			"00000000-0000-7000-8000-000000000003", "00000000-0000-7000-8000-000000000004",
			"00000000-0000-7000-8000-000000000005", "00000000-0000-7000-8000-000000000006",
		}[i]
		if _, err := pool.Exec(ctx, `INSERT INTO agents (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE agents SET `+c.setup+` WHERE id = $1`, id); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got string
		if err := pool.QueryRow(ctx, `SELECT config_sync FROM agents WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: config_sync = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLatestMatchesDatabaseVersion(t *testing.T) {
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	pool := pgtest.NewPool(t)
	var applied int64
	if err := pool.QueryRow(context.Background(), `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != latest {
		t.Fatalf("database at version %d, binary expects %d", applied, latest)
	}
}

// TestDownAndUpAgain checks that the Down section of 00002 reverts cleanly and
// that the migration is re-applicable.
func TestDownAndUpAgain(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	has := func(idx string) bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if !has("rendered_configs_touched_idx") || has("agents_last_seen_idx") {
		t.Fatal("00002 must create its indexes and drop agents_last_seen_idx (heartbeat writes must stay HOT)")
	}
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("down: %v", err)
	}
	if has("rendered_configs_touched_idx") || !has("agents_last_seen_idx") || !has("agents_status_idx") {
		t.Fatal("Down of 00002 must remove exactly its own indexes and restore agents_last_seen_idx")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if !has("rendered_configs_touched_idx") || has("agents_last_seen_idx") {
		t.Fatal("state differs after re-applying")
	}
}

// TestBootstrapAdminMigration checks 00005 (expand only): the users table
// accepts the provider "bootstrap" and nothing else new; Down revokes the API
// tokens of bootstrap users, removes these users and restores the old check.
func TestBootstrapAdminMigration(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()

	ins := `INSERT INTO users (id, subject, provider, role) VALUES ($1, $2, $3, 'admin')`
	const bootstrapID = "00000000-0000-7000-8000-0000000000b1"
	const oidcID = "00000000-0000-7000-8000-0000000000b2"
	if _, err := pool.Exec(ctx, ins, bootstrapID, "bootstrap:admin", "bootstrap"); err != nil {
		t.Fatalf("provider bootstrap must be accepted: %v", err)
	}
	if _, err := pool.Exec(ctx, ins, oidcID, "sub-1", "oidc"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, "00000000-0000-7000-8000-0000000000b3", "x", "other"); err == nil {
		t.Fatal("an unknown provider must still be refused")
	}
	tok := `INSERT INTO api_tokens (id, name, display_prefix, hash, role, created_by, created_by_user_id) VALUES ($1, $2, 'myra_', $3, 'admin', 'user', $4)`
	if _, err := pool.Exec(ctx, tok, "00000000-0000-7000-8000-0000000000c1", "by-bootstrap", []byte("h1"), bootstrapID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, tok, "00000000-0000-7000-8000-0000000000c2", "by-oidc", []byte("h2"), oidcID); err != nil {
		t.Fatal(err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 4); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE provider = 'bootstrap'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Down must remove the bootstrap users: %d %v", n, err)
	}
	var revoked bool
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM api_tokens WHERE name = 'by-bootstrap'`).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("Down must revoke the API tokens of bootstrap users: %v %v", revoked, err)
	}
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM api_tokens WHERE name = 'by-oidc'`).Scan(&revoked); err != nil || revoked {
		t.Fatalf("Down must keep the API tokens of other users: %v %v", revoked, err)
	}
	if _, err := pool.Exec(ctx, ins, bootstrapID, "bootstrap:admin", "bootstrap"); err == nil {
		t.Fatal("after Down the provider bootstrap must be refused again")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if _, err := pool.Exec(ctx, ins, bootstrapID, "bootstrap:admin", "bootstrap"); err != nil {
		t.Fatalf("provider bootstrap after Up again: %v", err)
	}
}

// TestUIVersionSelectionMigration checks 00004 (expand only): the selections
// table with its state check and the nullable progress column of the targets,
// and that the migration is reversible.
func TestUIVersionSelectionMigration(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()

	var progressType, nullable string
	if err := pool.QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
WHERE table_name = 'rollout_targets' AND column_name = 'progress'`).Scan(&progressType, &nullable); err != nil {
		t.Fatalf("rollout_targets.progress missing: %v", err)
	}
	if progressType != "jsonb" || nullable != "YES" {
		t.Fatalf("progress is %s nullable=%s, want a nullable jsonb column", progressType, nullable)
	}
	ins := `INSERT INTO fleet_version_selections (fleet_name, version, state, selected_by, selected_at) VALUES ($1, '0.162.0', $2, 'user:x', now())`
	if _, err := pool.Exec(ctx, ins, "gw", "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, "gw", "active"); err == nil {
		t.Fatal("one selection per fleet: the second insert must fail")
	}
	if _, err := pool.Exec(ctx, ins, "other", "bogus"); err == nil {
		t.Fatal("state must be active or invalidated")
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 3); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'fleet_version_selections'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Down must drop the selections table: %d %v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'rollout_targets' AND column_name = 'progress'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Down must drop the progress column: %d %v", n, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
