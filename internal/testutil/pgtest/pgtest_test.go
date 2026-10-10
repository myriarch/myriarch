// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgtest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FX-9: the DSN of the databases of an external server is PGTEST_DSN with the
// database replaced; everything else of the DSN stays.
func TestExternalDSNReplacesOnlyTheDatabase(t *testing.T) {
	for name, tc := range map[string]struct{ dsn, db, want string }{
		"url":                          {"postgres://admin:s3cret@127.0.0.1:5432/postgres?sslmode=disable", "t_1_2", "postgres://admin:s3cret@127.0.0.1:5432/t_1_2?sslmode=disable"},
		"postgresql scheme":            {"postgresql://admin@db.example.com/postgres?sslmode=require&application_name=ci", "x", "postgresql://admin@db.example.com/x?sslmode=require&application_name=ci"},
		"url without database":         {"postgres://admin:pw@127.0.0.1:5433?sslmode=disable", "x", "postgres://admin:pw@127.0.0.1:5433/x?sslmode=disable"},
		"special characters":           {"postgres://ad%40min:p%2Fw@h:5432/postgres", "a b", "postgres://ad%40min:p%2Fw@h:5432/a%20b"},
		"keyword value":                {"host=127.0.0.1 port=5432 user=admin password=pw dbname=postgres sslmode=disable", "t_1", "host=127.0.0.1 port=5432 user=admin password=pw dbname=t_1 sslmode=disable"},
		"keyword value without dbname": {"host=127.0.0.1 user=admin", "t_1", "host=127.0.0.1 user=admin dbname=t_1"},
	} {
		s := &Server{external: tc.dsn}
		if got := s.DSN(tc.db); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", name, got, tc.want)
		}
	}
	// embedded: unchanged
	e := &Server{Port: 5432}
	if got := e.DSN("x"); got != "postgres://myriarch:myriarch@127.0.0.1:5432/x?sslmode=disable" {
		t.Errorf("embedded DSN: %s", got)
	}
}

func TestExternalDSNEnv(t *testing.T) {
	t.Setenv(ExternalDSNEnv, " postgres://a@h/postgres \n")
	if got := ExternalDSN(); got != "postgres://a@h/postgres" {
		t.Errorf("%q", got)
	}
	t.Setenv(ExternalDSNEnv, "")
	if ExternalDSN() != "" {
		t.Error("empty means the embedded server")
	}
	// an unusable DSN is an error without the password
	t.Setenv(ExternalDSNEnv, "postgres://admin:topsecret@[::1/postgres")
	_, err := Start(context.Background())
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Errorf("a broken DSN must be refused without showing it: %v", err)
	}
}

// With PGTEST_DSN set (CI): the template and test databases are created with
// unique names, clones from several goroutines at once work (SQLSTATE 55006
// is retried), and Stop leaves nothing of this binary behind.
func TestExternalServerLifecycle(t *testing.T) {
	if ExternalDSN() == "" {
		t.Skip("PGTEST_DSN is not set")
	}
	ctx := context.Background()
	srv, err := Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.IsExternal() {
		t.Fatal("must use the external server")
	}
	migrate := func(ctx context.Context, p *pgxpool.Pool) error {
		_, err := p.Exec(ctx, "CREATE TABLE marker (id int)")
		return err
	}
	if err := srv.PrepareTemplate(ctx, "myriarch_tmpl", migrate); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srv.template, "myriarch_tmpl_"+srv.suffix) {
		t.Fatalf("template name %q must be unique per binary", srv.template)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	var mu sync.Mutex
	var names []string
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, cleanup, err := srv.NewDatabase(ctx)
			if err != nil {
				errs <- err
				return
			}
			var db string
			if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); err != nil {
				errs <- err
			}
			if _, err := pool.Exec(ctx, "INSERT INTO marker VALUES (1)"); err != nil {
				errs <- err
			}
			mu.Lock()
			names = append(names, db)
			even := len(names)%2 == 0
			mu.Unlock()
			if even { // half of them are dropped by their test, half by Stop
				cleanup()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(names) != 12 {
		t.Fatalf("%d databases", len(names))
	}

	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, srv.adminCfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var left []string
	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE $1`, "%"+srv.suffix+"%")
	if err != nil {
		t.Fatal(err)
	}
	left, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("databases left after Stop: %v", left)
	}
}
