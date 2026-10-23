// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pglock

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myriarch/myriarch/internal/pgsession"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

// TestFrozenLockConnectionFailoverIsBounded freezes the TCP connection of the
// leader's lock session without closing it, while the rest of the database
// stays reachable. The leader must give up leadership within half of the
// session timeout and a second replica must become leader within the session
// timeout, with the production retry interval; the old leader must not act as
// leader afterwards.
//
// Failure model proven here: the leader cannot confirm its lock session (a
// partition of that connection, not of its node). The leader resigns itself and
// ends the session over a regular connection (terminateSession). It does NOT
// prove the server's own expiry by TCP keepalives: the proxy's kernel keeps
// acknowledging the server's probes, and a hard node failure cannot be
// reproduced on one host. That budget is covered by pgsession.TestBudget (the
// timer model of the parameters) and TestNewPoolSendsTheParameters (the server
// receives them); a measurement with a real node failure is open.
func TestFrozenLockConnectionFailoverIsBounded(t *testing.T) {
	const (
		key      = 777001
		sessionT = pgsession.MinTimeout // the smallest valid timeout: the tightest budget
	)
	// production retry of the followers: 5s, shortened to T/6 by the elector
	defer func(old time.Duration) { RetryInterval = old }(RetryInterval)
	RetryInterval = 5 * time.Second
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
	time.Sleep(2 * b.el.retryInterval())
	if b.el.IsLeader() {
		t.Fatal("b must not lead while a holds the lock")
	}

	proxy.Freeze()
	t0 := time.Now()
	select {
	case <-lctx.Done():
	case <-time.After(sessionT):
		t.Fatalf("a still leads %v after its lock session froze", time.Since(t0))
	}
	if a.el.IsLeader() {
		t.Fatal("a must not report leadership once its leader context is cancelled")
	}
	resigned := time.Since(t0)
	if resigned > sessionT/2+time.Second {
		t.Errorf("a gave up leadership after %v, want about half of the session timeout %v", resigned, sessionT)
	}
	waitFor(t, "b takes over", sessionT-resigned, b.el.IsLeader)
	took := time.Since(t0)
	t.Logf("a resigned after %v, b became leader after %v (session timeout %v)", resigned, took, sessionT)
	if took > sessionT {
		t.Errorf("failover took %v, want at most %v", took, sessionT)
	}
	// no split brain: a never leads again while its session is frozen and b leads
	time.Sleep(2 * b.el.retryInterval())
	if a.el.IsLeader() || !b.el.IsLeader() {
		t.Fatalf("leaders: a=%v b=%v, want only b", a.el.IsLeader(), b.el.IsLeader())
	}
	if holders := lockHolders(t, pool, key); len(holders) != 1 {
		t.Fatalf("one session must hold the lock, got %v", holders)
	}
}
