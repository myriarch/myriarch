// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgsession

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

func TestParams(t *testing.T) {
	got := Params(30 * time.Second)
	want := map[string]string{"tcp_keepalives_idle": "10", "tcp_keepalives_interval": "5", "tcp_keepalives_count": "3", "tcp_user_timeout": "30000"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// the server detects a silent peer within the timeout: idle + interval*count <= timeout
	for _, d := range []time.Duration{MinTimeout, 7 * time.Second, time.Minute, MaxTimeout} {
		p := Params(d)
		idle, _ := time.ParseDuration(p["tcp_keepalives_idle"] + "s")
		iv, _ := time.ParseDuration(p["tcp_keepalives_interval"] + "s")
		if total := idle + 3*iv; total > d {
			t.Errorf("timeout %v: keepalives detect after %v", d, total)
		}
	}
}

func TestNewPoolSendsTheParameters(t *testing.T) {
	base := pgtest.NewPool(t)
	pool, err := NewPool(context.Background(), base, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var idle, interval, count string
	if err := pool.QueryRow(context.Background(), `SELECT current_setting('tcp_keepalives_idle'), current_setting('tcp_keepalives_interval'), current_setting('tcp_keepalives_count')`).Scan(&idle, &interval, &count); err != nil {
		t.Fatal(err)
	}
	// units differ by server version (the setting is shown with "s" or without)
	if (idle != "10" && idle != "10s") || (interval != "5" && interval != "5s") || count != "3" {
		t.Fatalf("session settings idle=%q interval=%q count=%q", idle, interval, count)
	}
	// the base pool is not changed
	var baseIdle string
	if err := base.QueryRow(context.Background(), `SELECT current_setting('tcp_keepalives_idle')`).Scan(&baseIdle); err != nil {
		t.Fatal(err)
	}
	if baseIdle == idle {
		t.Fatalf("the base pool must keep the server defaults, got %q", baseIdle)
	}
}
