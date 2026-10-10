// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package pgbus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

func newBusT(t *testing.T, pool *pgxpool.Pool, replica string) (*Bus, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	b, err := newBus(context.Background(), pool, replica, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, reg
}

func recv(t *testing.T, ch <-chan eventbus.Event, d time.Duration) (eventbus.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(d):
		return eventbus.Event{}, false
	}
}

func TestTwoReplicasExchangeEvents(t *testing.T) {
	pool := pgtest.NewPool(t)
	a, _ := newBusT(t, pool, "replica-a")
	b, _ := newBusT(t, pool, "replica-b")
	ctx := context.Background()

	chA, cancelA := a.Subscribe(eventbus.AgentDesiredChanged)
	defer cancelA()
	chB, cancelB := b.Subscribe(eventbus.AgentDesiredChanged, eventbus.AgentCommand)
	defer cancelB()
	chAll, cancelAll := b.Subscribe()
	defer cancelAll()
	chOther, cancelOther := b.Subscribe(eventbus.RolloutChanged)
	defer cancelOther()

	ids := []domain.AgentID{domain.AgentID(uuid.New()), domain.AgentID(uuid.New())}
	if err := a.Publish(ctx, eventbus.AgentDesiredChanged, eventbus.AgentIDsPayload{AgentIDs: ids}); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]<-chan eventbus.Event{"publisher replica": chA, "other replica": chB, "unfiltered subscriber": chAll} {
		ev, ok := recv(t, ch, 5*time.Second)
		if !ok {
			t.Fatalf("%s did not receive the event", name)
		}
		if ev.Type != eventbus.AgentDesiredChanged || ev.Origin != "replica-a" || ev.ID == uuid.Nil || ev.Time.IsZero() {
			t.Fatalf("%s: envelope not filled: %+v", name, ev)
		}
		var p eventbus.AgentIDsPayload
		if err := ev.Decode(&p); err != nil || len(p.AgentIDs) != 2 || p.AgentIDs[0] != ids[0] {
			t.Fatalf("%s: payload %+v %v", name, p, err)
		}
	}
	if ev, ok := recv(t, chOther, 300*time.Millisecond); ok {
		t.Fatalf("subscriber filtered by type received %+v", ev)
	}

	// and in the other direction
	if err := b.Publish(ctx, eventbus.AgentCommand, eventbus.AgentCommandPayload{AgentID: ids[0], Command: eventbus.CommandRestart, RequestedBy: "user:1"}); err != nil {
		t.Fatal(err)
	}
	ev, ok := recv(t, chB, 5*time.Second)
	if !ok || ev.Origin != "replica-b" || ev.Type != eventbus.AgentCommand {
		t.Fatalf("command event: %+v %v", ev, ok)
	}
	if ev, ok := recv(t, chA, 300*time.Millisecond); ok {
		t.Fatalf("replica A is not subscribed to commands: %+v", ev)
	}
}

func TestEventOrderAndLargeIDLists(t *testing.T) {
	pool := pgtest.NewPool(t)
	a, _ := newBusT(t, pool, "a")
	b, _ := newBusT(t, pool, "b")
	ch, cancel := b.Subscribe(eventbus.RolloutChanged)
	defer cancel()
	for i := 0; i < 50; i++ {
		if err := a.Publish(context.Background(), eventbus.RolloutChanged, eventbus.RolloutChangedPayload{RolloutID: uuid.New(), FleetName: fmt.Sprint(i), Status: domain.RolloutRunning}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		ev, ok := recv(t, ch, 5*time.Second)
		if !ok {
			t.Fatalf("event %d missing", i)
		}
		var p eventbus.RolloutChangedPayload
		_ = ev.Decode(&p)
		if p.FleetName != fmt.Sprint(i) {
			t.Fatalf("events of one publisher must stay in order: got %s at %d", p.FleetName, i)
		}
	}

	// a chunk of 100 ids (the documented maximum) fits into one event
	ids := make([]domain.AgentID, 100)
	for i := range ids {
		ids[i] = domain.AgentID(uuid.New())
	}
	chunks := eventbus.ChunkAgentIDs(append(ids, domain.AgentID(uuid.New())))
	if len(chunks) != 2 {
		t.Fatal("chunking")
	}
	ch2, cancel2 := b.Subscribe(eventbus.AgentDesiredChanged)
	defer cancel2()
	if err := a.Publish(context.Background(), eventbus.AgentDesiredChanged, eventbus.AgentIDsPayload{AgentIDs: chunks[0]}); err != nil {
		t.Fatalf("a full chunk must be publishable: %v", err)
	}
	if _, ok := recv(t, ch2, 5*time.Second); !ok {
		t.Fatal("full chunk not delivered")
	}
}

func TestSizeLimitAndErrors(t *testing.T) {
	pool := pgtest.NewPool(t)
	a, _ := newBusT(t, pool, "a")
	big := make([]domain.AgentID, 300)
	err := a.Publish(context.Background(), eventbus.AgentDesiredChanged, eventbus.AgentIDsPayload{AgentIDs: big})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized event must be rejected, got %v", err)
	}
	if err := a.Publish(context.Background(), eventbus.AgentCommand, make(chan int)); err == nil {
		t.Fatal("unmarshalable payload must fail")
	}
	if err := a.Publish(context.Background(), eventbus.BusReconnected, struct{}{}); err == nil {
		t.Fatal("bus.reconnected is local only")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Publish(ctx, eventbus.AgentCommand, struct{}{}); err == nil {
		t.Fatal("cancelled context must fail the publish")
	}
}

func counterValue(t *testing.T, reg *prometheus.Registry, name, label string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == label {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

var _ = dto.MetricFamily{}

func TestSlowSubscriberDropsAndCounts(t *testing.T) {
	pool := pgtest.NewPool(t)
	a, reg := newBusT(t, pool, "a")
	slow, cancelSlow := a.Subscribe(eventbus.AgentCommand)
	defer cancelSlow()
	fast, cancelFast := a.Subscribe(eventbus.AgentDesiredChanged)
	defer cancelFast()
	var got int
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range fast {
			mu.Lock()
			got++
			mu.Unlock()
		}
	}()
	for i := 0; i < 300; i++ {
		if err := a.Publish(context.Background(), eventbus.AgentCommand, eventbus.AgentCommandPayload{AgentID: domain.AgentID(uuid.New()), Command: eventbus.CommandResync}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Publish(context.Background(), eventbus.AgentDesiredChanged, eventbus.AgentIDsPayload{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		g := got
		mu.Unlock()
		if g == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fast subscriber blocked by the slow one")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// wait until all notifications were dispatched
	deadline = time.Now().Add(5 * time.Second)
	for counterValue(t, reg, "myriarch_eventbus_dropped_total", string(eventbus.AgentCommand)) < 44 {
		if time.Now().After(deadline) {
			t.Fatalf("expected >= 44 dropped events for the slow subscriber, metric = %v",
				counterValue(t, reg, "myriarch_eventbus_dropped_total", string(eventbus.AgentCommand)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(slow) != 256 {
		t.Fatalf("slow subscriber buffer holds %d events, want 256", len(slow))
	}
	cancelFast()
	<-done
}

func TestReconnectAfterListenerLossSendsBusReconnected(t *testing.T) {
	pool := pgtest.NewPool(t)
	a, _ := newBusT(t, pool, "a")
	pub, _ := newBusT(t, pool, "pub")
	ch, cancel := a.Subscribe(eventbus.AgentCommand, eventbus.BusReconnected)
	defer cancel()
	ctx := context.Background()

	if !Connected(a) {
		t.Fatal("bus must be connected after New")
	}
	// kill the LISTEN backends (both buses; the publisher keeps working through the pool).
	// Only those of this test's database: with PGTEST_DSN the server is shared with the
	// other test binaries.
	var n int
	err := pool.QueryRow(ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
WHERE datname = current_database() AND query LIKE 'LISTEN `+Channel+`%' AND pid <> pg_backend_pid()`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("expected to terminate 2 listener sessions, got %d", n)
	}

	ev, ok := recv(t, ch, 15*time.Second)
	if !ok || ev.Type != eventbus.BusReconnected || ev.Origin != "a" {
		t.Fatalf("expected a local bus.reconnected event, got %+v ok=%v", ev, ok)
	}
	// delivery works again
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := pub.Publish(ctx, eventbus.AgentCommand, eventbus.AgentCommandPayload{AgentID: domain.AgentID(uuid.New()), Command: eventbus.CommandRestart}); err != nil {
			t.Fatal(err)
		}
		if ev, ok := recv(t, ch, 500*time.Millisecond); ok && ev.Type == eventbus.AgentCommand {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no delivery after reconnect")
		}
	}
	if !Connected(a) {
		t.Fatal("bus must report connected again")
	}
}

func TestCloseSemantics(t *testing.T) {
	pool := pgtest.NewPool(t)
	a, _ := newBusT(t, pool, "a")
	ch, cancel := a.Subscribe()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscriber channel must be closed by Close")
	}
	cancel() // must not panic after Close
	cancel()
	if err := a.Publish(context.Background(), eventbus.AgentCommand, struct{}{}); !errors.Is(err, eventbus.ErrClosed) {
		t.Fatalf("publish after close: %v", err)
	}
	ch2, cancel2 := a.Subscribe()
	defer cancel2()
	if _, ok := <-ch2; ok {
		t.Fatal("subscribe after close returns a closed channel")
	}
	if err := a.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	if Connected(a) {
		t.Fatal("closed bus is not connected")
	}

	// closing must release the dedicated connection back to PostgreSQL
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND query LIKE 'LISTEN `+Channel+`%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d listener sessions left after Close", n)
	}
}

func TestListenerConnectionIsNotReturnedToPool(t *testing.T) {
	// A pool with 3 connections: the bus holds one for good, so the pool must
	// still serve concurrent queries with the remaining two.
	base := pgtest.NewPool(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 3
	small, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close) // runs after the bus cleanup registered below (LIFO): Close blocks on acquired connections
	b, _ := newBusT(t, small, "a")
	_ = b
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := small.Exec(context.Background(), "SELECT pg_sleep(0.05)"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if st := small.Stat(); st.AcquiredConns() != 1 {
		t.Fatalf("exactly the listener connection stays acquired, got %d", st.AcquiredConns())
	}
}

func TestNewFailsWithoutDatabase(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://x:y@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := New(ctx, pool, "r", nil, nil); err == nil {
		t.Fatal("New must fail when LISTEN cannot be established")
	}
	if _, err := New(context.Background(), nil, "r", nil, nil); err == nil {
		t.Fatal("nil pool")
	}
}
