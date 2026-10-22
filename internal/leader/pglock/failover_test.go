// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pglock

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// TestFrozenLockConnectionFailoverIsBounded freezes the TCP connection of the
// leader's lock session without closing it, as after the hard failure of the
// leader's node. The leader must give up leadership within the session timeout
// and a second replica must become leader within the session timeout plus a
// small tolerance; the old leader must not act as leader afterwards.
func TestFrozenLockConnectionFailoverIsBounded(t *testing.T) {
	const (
		key       = 777001
		sessionT  = 3 * time.Second
		tolerance = 3 * time.Second // retry interval of the follower and scheduling
	)
	pool := pgtest.NewPool(t)
	cc := pool.Config().ConnConfig
	proxy := pgtest.StartProxy(t, fmt.Sprintf("%s:%d", cc.Host, cc.Port))

	// the leader's lock session goes through the proxy, everything else is direct
	a := startCandidate(t, pool, key, "a", func(c *candidate) {
		c.session = sessionT
		c.tweak = func(cfg *pgxpool.Config) {
			cfg.ConnConfig.Host, cfg.ConnConfig.Port = "127.0.0.1", uint16(proxy.Addr().Port)
			cfg.ConnConfig.Fallbacks = nil
		}
	})
	waitFor(t, "leader a", 20*time.Second, a.el.IsLeader)
	lctx := <-a.elected
	b := startCandidate(t, pool, key, "b", func(c *candidate) { c.session = sessionT })
	time.Sleep(2 * RetryInterval)
	if b.el.IsLeader() {
		t.Fatal("b must not lead while a holds the lock")
	}

	proxy.Freeze()
	t0 := time.Now()
	select {
	case <-lctx.Done():
	case <-time.After(sessionT + tolerance):
		t.Fatalf("a still leads %v after its lock session froze", time.Since(t0))
	}
	if a.el.IsLeader() {
		t.Fatal("a must not report leadership once its leader context is cancelled")
	}
	resigned := time.Since(t0)
	if resigned > sessionT/2+tolerance/2 {
		t.Errorf("a gave up leadership after %v, want about half of the session timeout %v", resigned, sessionT)
	}
	waitFor(t, "b takes over", sessionT+tolerance-resigned, b.el.IsLeader)
	took := time.Since(t0)
	t.Logf("a resigned after %v, b became leader after %v (session timeout %v)", resigned, took, sessionT)
	if took > sessionT+tolerance {
		t.Errorf("failover took %v, want at most %v", took, sessionT+tolerance)
	}
	// no split brain: a never leads again while its session is frozen and b leads
	time.Sleep(2 * RetryInterval)
	if a.el.IsLeader() || !b.el.IsLeader() {
		t.Fatalf("leaders: a=%v b=%v, want only b", a.el.IsLeader(), b.el.IsLeader())
	}
	if holders := lockHolders(t, pool, key); len(holders) != 1 {
		t.Fatalf("one session must hold the lock, got %v", holders)
	}
}
