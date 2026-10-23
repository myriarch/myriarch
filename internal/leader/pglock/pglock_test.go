// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pglock

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	// shorten the retry so the tests are fast; the production default is 5s
	RetryInterval = 300 * time.Millisecond
	os.Exit(pgtest.RunWithMigratedTemplate(m))
}

type candidate struct {
	name    string
	el      *elector
	elected chan context.Context
	ended   chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	count   atomic.Int32
	// windDown is how long the leader function needs to stop after its
	// context was cancelled.
	windDown time.Duration
	// session is the session timeout (default: the elector's).
	session time.Duration
	// tweak changes the connection settings of the lock session.
	tweak func(*pgxpool.Config)
}

// withWindDown makes the leader function of a candidate slow to stop.
func withWindDown(d time.Duration) func(*candidate) { return func(c *candidate) { c.windDown = d } }

func startCandidate(t *testing.T, pool *pgxpool.Pool, key int64, name string, opts ...func(*candidate)) *candidate {
	t.Helper()
	c := &candidate{name: name, elected: make(chan context.Context, 10), ended: make(chan struct{}, 10), done: make(chan struct{})}
	for _, o := range opts {
		o(c)
	}
	c.el = New(pool, key, name, nil, prometheus.NewRegistry(), WithSessionTimeout(c.session)).(*elector)
	c.el.connTweak = c.tweak
	// shortened timing; a health check that is merely slow ends leadership
	// (that is what the timeout is for), but the tests never make the
	// connection hang, and a loaded host must not turn a delayed goroutine into
	// a lost lock: the timeout is far above any scheduling delay
	if c.session == 0 { // otherwise the timing follows from the session timeout
		c.el.checkInterval, c.el.checkTimeout = 300*time.Millisecond, 10*time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go func() {
		defer close(c.done)
		_ = c.el.Run(ctx, func(lctx context.Context) {
			c.count.Add(1)
			c.elected <- lctx
			<-lctx.Done()
			time.Sleep(c.windDown)
			c.ended <- struct{}{}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-c.done:
		case <-time.After(20 * time.Second):
			t.Errorf("elector %s did not stop", name)
		}
	})
	return c
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExactlyOneLeader(t *testing.T) {
	pool := pgtest.NewPool(t)
	const key = 7012024
	cs := []*candidate{startCandidate(t, pool, key, "a"), startCandidate(t, pool, key, "b"), startCandidate(t, pool, key, "c")}
	waitFor(t, "a leader", 20*time.Second, func() bool {
		n := 0
		for _, c := range cs {
			if c.el.IsLeader() {
				n++
			}
		}
		return n == 1
	})
	// stays exactly one for a while
	for i := 0; i < 15; i++ {
		n := 0
		for _, c := range cs {
			if c.el.IsLeader() {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d leaders", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var leader *candidate
	for _, c := range cs {
		if c.el.IsLeader() {
			leader = c
		}
	}
	// the gauge reports 1 for the leader only
	if v := gaugeValue(t, leader.el); v != 1 {
		t.Fatalf("leader gauge = %v", v)
	}
	for _, c := range cs {
		if c != leader && gaugeValue(t, c.el) != 0 {
			t.Fatal("follower gauge must be 0")
		}
	}

	// graceful stop of the leader: another one takes over
	leader.cancel()
	<-leader.done
	if leader.el.IsLeader() {
		t.Fatal("stopped elector must not be leader")
	}
	waitFor(t, "takeover", 20*time.Second, func() bool {
		for _, c := range cs {
			if c != leader && c.el.IsLeader() {
				return true
			}
		}
		return false
	})
}

func gaugeValue(t *testing.T, e *elector) float64 {
	t.Helper()
	var m = &dtoMetric{}
	return m.read(t, e.gauge)
}

// gatedPool is a pool on the database of another pool that counts every
// connection acquisition and can hold acquisitions back, so a test controls
// when an elector gets to talk to the database again.
type gatedPool struct {
	*pgxpool.Pool
	acquisitions atomic.Int64

	mu   sync.Mutex
	hold chan struct{} // non-nil while acquisitions are held back
}

func newGatedPool(t *testing.T, base *pgxpool.Pool) *gatedPool {
	t.Helper()
	g := &gatedPool{}
	cfg := base.Config()
	cfg.PrepareConn = func(ctx context.Context, _ *pgx.Conn) (bool, error) {
		g.acquisitions.Add(1)
		g.mu.Lock()
		hold := g.hold
		g.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return true, nil
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close) // runs after the electors that use the pool have stopped
	g.Pool = p
	return g
}

// holdBack makes acquisitions wait until release is called.
func (g *gatedPool) holdBack() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold == nil {
		g.hold = make(chan struct{})
	}
}

func (g *gatedPool) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold != nil {
		close(g.hold)
		g.hold = nil
	}
}

// lockHolders returns the backends that hold the advisory lock key in the
// database of pool (and only those: the sessions of other databases and other
// keys are none of the test's business).
func lockHolders(t *testing.T, pool *pgxpool.Pool, key int64) []int32 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT l.pid FROM pg_locks l
WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND l.classid = (($1::bigint >> 32) & 4294967295)::bigint::oid
  AND l.objid = ($1::bigint & 4294967295)::bigint::oid`, key)
	if err != nil {
		t.Fatal(err)
	}
	pids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		t.Fatal(err)
	}
	return pids
}

// When the lock connection of the leader dies on the server, the leader gives
// up (flag first, then its context) and another candidate takes the lock.
//
// Who wins the free lock is a race by design: the old leader retries at once
// (its failure detection is a ticker), the follower polls on its own timer.
// The test therefore holds the old leader's access to the database back until
// the follower leads, and it waits for conditions instead of sleeping. The
// leader function of a needs a quarter of a second to stop: the leader flag
// must not wait for it.
func TestLeaderConnectionKilledAnotherTakesOver(t *testing.T) {
	pool := pgtest.NewPool(t)
	const key = 424242
	poolA, poolB := newGatedPool(t, pool), newGatedPool(t, pool)

	a := startCandidate(t, poolA.Pool, key, "a", withWindDown(250*time.Millisecond))
	waitFor(t, "first leader", 20*time.Second, a.el.IsLeader)
	lctx := <-a.elected
	holders := lockHolders(t, pool, key)
	if len(holders) != 1 {
		t.Fatalf("one session must hold the lock, got %v", holders)
	}
	killed := holders[0]

	// b has tried to take the lock several times and failed: it is a follower
	// because of the lock, not because it did not get around to asking
	b := startCandidate(t, poolB.Pool, key, "b")
	waitFor(t, "lock attempts of b", 20*time.Second, func() bool { return poolB.acquisitions.Load() >= 3 })
	if b.el.IsLeader() {
		t.Fatal("b must not lead while a holds the lock")
	}

	// kill the lock session of a on the server
	poolA.holdBack()
	t0 := time.Now()
	var terminated bool
	if err := pool.QueryRow(context.Background(), `SELECT pg_terminate_backend($1)`, killed).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate the lock session (backend %d): %v %v", killed, terminated, err)
	}
	select {
	case <-lctx.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("leader context must be cancelled when the lock connection dies")
	}
	// the flag goes first, then the context: nobody who sees the cancelled
	// context sees a leader, however long the leader function takes to stop
	if a.el.IsLeader() {
		t.Fatal("a must not report leadership once its leader context is cancelled")
	}
	if v := gaugeValue(t, a.el); v != 0 {
		t.Fatalf("gauge of a = %v once its leader context is cancelled", v)
	}
	waitFor(t, "b takes over", 20*time.Second, b.el.IsLeader)
	t.Logf("takeover after pg_terminate_backend took %v", time.Since(t0))
	if a.el.IsLeader() {
		t.Fatal("a must have lost leadership")
	}
	if now := lockHolders(t, pool, key); len(now) != 1 || now[0] == killed {
		t.Fatalf("the lock must be held by one new session (killed %d), got %v", killed, now)
	}
	// (the gauge of a new leader follows its flag a moment later)
	waitFor(t, "gauge of b", 15*time.Second, func() bool { return gaugeValue(t, b.el) == 1 })

	// a is a follower again: with its access restored it asks for the lock and does not get it
	poolA.release()
	asked := poolA.acquisitions.Load()
	waitFor(t, "lock attempts of a", 20*time.Second, func() bool { return poolA.acquisitions.Load() >= asked+2 })
	if a.el.IsLeader() || !b.el.IsLeader() {
		t.Fatalf("leaders: a=%v b=%v, want only b", a.el.IsLeader(), b.el.IsLeader())
	}

	// ... and it takes over again when b stops
	b.cancel()
	<-b.done
	select {
	case <-a.elected:
	case <-time.After(15 * time.Second):
		t.Fatal("a must be elected again when b stops")
	}
	if n := a.count.Load(); n != 2 {
		t.Fatalf("a was elected %d times, want 2", n)
	}
}

func TestOnElectedReturnsReleasesLock(t *testing.T) {
	pool := pgtest.NewPool(t)
	const key = 99
	var calls atomic.Int32
	el := New(pool, key, "x", nil, nil).(*elector)
	el.checkInterval, el.checkTimeout = 300*time.Millisecond, 10*time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = el.Run(ctx, func(lctx context.Context) {
			// leadership work ends by itself after a short time
			calls.Add(1)
			time.Sleep(200 * time.Millisecond)
		})
	}()
	// a second elector (other pool connection) can take over between runs; the
	// lock must be free after onElected returned
	waitFor(t, "two elections by the same elector", 20*time.Second, func() bool { return calls.Load() >= 2 })
	cancel()
	<-done
	// lock is free: a third party can take it right away
	var got bool
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := conn.QueryRow(context.Background(), "SELECT pg_try_advisory_lock($1)", int64(key)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("advisory lock still held after the elector stopped")
	}
	_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(key))
}

func TestRunStopsPromptlyAndDoesNotLeakLock(t *testing.T) {
	pool := pgtest.NewPool(t)
	c := startCandidate(t, pool, 5, "a")
	waitFor(t, "leader", 20*time.Second, func() bool { return c.el.IsLeader() })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); c.cancel(); <-c.done }()
	wg.Wait()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted
AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d advisory locks left after shutdown", n)
	}
	if st := pool.Stat(); st.AcquiredConns() != 0 {
		t.Fatalf("%d connections still acquired after shutdown", st.AcquiredConns())
	}
}
