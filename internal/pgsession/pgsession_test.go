// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgsession

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

func TestParams(t *testing.T) {
	got := Params(30 * time.Second)
	want := map[string]string{"tcp_keepalives_idle": "3", "tcp_keepalives_interval": "3", "tcp_keepalives_count": "5", "tcp_user_timeout": "19000"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// secs reads a whole-second server parameter.
func secs(t *testing.T, p map[string]string, k string) time.Duration {
	t.Helper()
	n, err := strconv.Atoi(p[k])
	if err != nil || n < 1 {
		t.Fatalf("%s = %q", k, p[k])
	}
	return time.Duration(n) * time.Second
}

// linuxDrop models the keepalive timer of Linux with a non-zero
// TCP_USER_TIMEOUT: it fires idle after the last packet received and then every
// interval; from the second firing on (a probe is outstanding) the connection is
// dropped if at least the user timeout has passed. The count is not consulted.
func linuxDrop(idle, interval, user time.Duration) time.Duration {
	for k, at := 0, idle; ; k, at = k+1, at+interval {
		if k >= 1 && at >= user {
			return at
		}
	}
}

// countDrop models a server without a user timeout: idle, then count probes.
func countDrop(idle, interval time.Duration, count int) time.Duration {
	return idle + time.Duration(count)*interval
}

// TestBudget is the regression for the end-to-end failover bound: for every
// timeout, also those that are not whole seconds, the server drops a silent
// session after the leader gave up (no split brain: T/2 after its last
// confirmation) and at the latest after Detection(T); a follower then takes the
// lock within the longest retry, and everything together stays within T.
func TestBudget(t *testing.T) {
	var timeouts []time.Duration
	for ms := MinTimeout.Milliseconds(); ms <= MaxTimeout.Milliseconds(); ms += 997 { // prime step: not whole seconds
		timeouts = append(timeouts, time.Duration(ms)*time.Millisecond)
	}
	timeouts = append(timeouts, MinTimeout, 30*time.Second, 599*time.Second, MaxTimeout)
	for _, T := range timeouts {
		p := Params(T)
		idle, iv := secs(t, p, "tcp_keepalives_idle"), secs(t, p, "tcp_keepalives_interval")
		count, _ := strconv.Atoi(p["tcp_keepalives_count"])
		ms, err := strconv.Atoi(p["tcp_user_timeout"])
		if err != nil || ms < 1000 {
			t.Fatalf("T=%v: tcp_user_timeout = %q", T, p["tcp_user_timeout"])
		}
		user := time.Duration(ms) * time.Millisecond
		resign := T / 2 // the leader gives up at the latest this long after its last answer
		detect := Detection(T)
		takeover := func(drop time.Duration) time.Duration {
			return drop + time.Duration(float64(RetryInterval(T))*1.2) + T/20
		}

		drop := linuxDrop(idle, iv, user)
		if drop > detect {
			t.Errorf("T=%v: Linux drops after %v, want at most %v", T, drop, detect)
		}
		if drop <= resign {
			t.Errorf("T=%v: Linux drops after %v, before the leader gave up at %v", T, drop, resign)
		}
		if got := takeover(drop); got > T {
			t.Errorf("T=%v: Linux takeover bound %v exceeds the timeout", T, got)
		}
		cdrop := countDrop(idle, iv, count)
		if cdrop > detect || takeover(cdrop) > T {
			t.Errorf("T=%v: count-based drop after %v (detection %v)", T, cdrop, detect)
		}
	}
}

func TestRetryInterval(t *testing.T) {
	for _, c := range []struct{ t, want time.Duration }{
		{10 * time.Second, 10 * time.Second / 6}, {30 * time.Second, 5 * time.Second}, {10 * time.Minute, 5 * time.Second},
	} {
		if got := RetryInterval(c.t); got != c.want {
			t.Errorf("RetryInterval(%v) = %v, want %v", c.t, got, c.want)
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
	if (idle != "3" && idle != "3s") || (interval != "3" && interval != "3s") || count != "5" {
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

// tlsOnly skips unless the embedded server runs with TLS (PGTEST_TLS=1).
func tlsOnly(t *testing.T) {
	t.Helper()
	if os.Getenv(pgtest.TLSEnv) == "" {
		t.Skip("needs PGTEST_TLS=1")
	}
}

// TestDedicatedSessionUsesVerifiedTLS checks, with the TLS test environment,
// that a dedicated session copies the TLS settings of the main pool: it is
// encrypted, and a certificate the client does not trust or a host name the
// certificate does not cover is refused, not silently accepted.
func TestDedicatedSessionUsesVerifiedTLS(t *testing.T) {
	tlsOnly(t)
	base := pgtest.NewPool(t)
	if base.Config().ConnConfig.TLSConfig == nil {
		t.Fatal("the test pool must use TLS")
	}
	ctx := context.Background()

	pool, err := NewPool(ctx, base, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ssl bool
	if err := pool.QueryRow(ctx, `SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&ssl); err != nil || !ssl {
		t.Fatalf("dedicated session encrypted = %v, err %v", ssl, err)
	}
	pool.Close()

	change := func(f func(*tls.Config)) func(*pgxpool.Config) {
		return func(cfg *pgxpool.Config) {
			tc := cfg.ConnConfig.TLSConfig.Clone()
			f(tc)
			cfg.ConnConfig.TLSConfig = tc
			for _, fb := range cfg.ConnConfig.Fallbacks {
				fb.TLSConfig = tc
			}
		}
	}
	for name, tweak := range map[string]func(*pgxpool.Config){
		"untrusted certificate": change(func(c *tls.Config) { c.RootCAs = x509.NewCertPool() }),
		"host name mismatch":    change(func(c *tls.Config) { c.ServerName = "other.invalid" }),
	} {
		p, err := NewPool(ctx, base, 30*time.Second, tweak, nil)
		if err == nil {
			p.Close()
			t.Errorf("%s: the dedicated session was accepted", name)
		}
	}
}
