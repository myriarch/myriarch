// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package pgbus implements eventbus.Bus with PostgreSQL LISTEN/NOTIFY.
//
// Design: one dedicated connection (acquired from the pool, never returned
// while listening) runs `LISTEN myriarch_events` and dispatches
// notifications to local subscribers; Publish uses
// `SELECT pg_notify('myriarch_events', $1)` on any pool connection. The
// listener reconnects with backoff and re-LISTENs if the connection is lost.
// Notifications sent while disconnected are lost (bus semantics are best
// effort); consumers re-read authoritative state periodically anyway (rollout
// engine ticks, OpAMP reconciliation on every agent message, live hub sends
// a "resync" event to UI clients after a listener reconnect).
//
// The listener connection is checked every 30s with a ping so that half-open
// TCP connections (firewalls, failovers) are detected and replaced.
package pgbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/metrics"
)

// Channel is the Postgres NOTIFY channel name.
const Channel = brand.NotifyChannel

const (
	subscriberBuffer = 256
	pingInterval     = 30 * time.Second
	minBackoff       = time.Second
	maxBackoff       = 30 * time.Second
)

// Bus is the PostgreSQL implementation of eventbus.Bus.
type Bus struct {
	pool      *pgxpool.Pool
	replicaID string
	log       *slog.Logger
	dropped   *prometheus.CounterVec

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	connected atomic.Bool
	reconnect atomic.Int64 // number of re-established listeners (for tests/diagnostics)

	mu     sync.Mutex
	subs   map[int]*subscription
	nextID int
	closed bool
}

var _ eventbus.Bus = (*Bus)(nil)

type subscription struct {
	name  string
	ch    chan eventbus.Event
	types map[eventbus.Type]struct{}
}

// New starts the listener and returns the bus. replicaID is used as
// Event.Origin. The first LISTEN is established before New returns, so events
// published afterwards (by any replica) are delivered.
func New(ctx context.Context, pool *pgxpool.Pool, replicaID string, logger *slog.Logger, reg prometheus.Registerer) (eventbus.Bus, error) {
	return newBus(ctx, pool, replicaID, logger, reg)
}

func newBus(ctx context.Context, pool *pgxpool.Pool, replicaID string, logger *slog.Logger, reg prometheus.Registerer) (*Bus, error) {
	if pool == nil {
		return nil, errors.New("pgbus: pool is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	bctx, cancel := context.WithCancel(context.Background())
	b := &Bus{
		pool: pool, replicaID: replicaID, log: logger.With("component", "eventbus"),
		dropped: metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "myriarch_eventbus_dropped_total",
			Help: "Events dropped because a local subscriber was too slow.",
		}, []string{"subscriber"})),
		ctx: bctx, cancel: cancel, subs: map[int]*subscription{},
	}
	conn, err := b.listen(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("pgbus: listen: %w", err)
	}
	b.connected.Store(true)
	b.wg.Add(1)
	go b.run(conn)
	return b, nil
}

// Connected reports whether the listener connection is established (used by
// /readyz). It returns true for buses of other implementations.
func Connected(bus eventbus.Bus) bool {
	if b, ok := bus.(*Bus); ok {
		return b.connected.Load()
	}
	return bus != nil
}

// listen acquires a dedicated connection and issues LISTEN.
func (b *Bus) listen(ctx context.Context) (*pgxpool.Conn, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := b.pool.Acquire(cctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(cctx, "LISTEN "+Channel); err != nil {
		discard(conn)
		return nil, err
	}
	return conn, nil
}

// discard closes a pooled connection instead of returning it (a connection
// with a LISTEN registration must never go back to the pool).
func discard(conn *pgxpool.Conn) {
	raw := conn.Hijack()
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = raw.Close(cctx)
}

func (b *Bus) run(conn *pgxpool.Conn) {
	defer b.wg.Done()
	backoff := minBackoff
	for {
		started := time.Now()
		err := b.serve(conn)
		b.connected.Store(false)
		discard(conn)
		if b.ctx.Err() != nil {
			return
		}
		b.log.Warn("event bus listener lost, reconnecting", "error", err)
		if time.Since(started) > time.Minute {
			backoff = minBackoff
		}
		for {
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(backoff):
			}
			var lerr error
			conn, lerr = b.listen(b.ctx)
			if lerr == nil {
				break
			}
			b.log.Warn("event bus reconnect failed", "error", lerr, "retry_in", backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
		b.connected.Store(true)
		b.reconnect.Add(1)
		b.log.Info("event bus listener re-established")
		b.deliverLocal(eventbus.Event{ID: uuid.New(), Type: eventbus.BusReconnected, Time: time.Now().UTC(), Origin: b.replicaID})
	}
}

// serve waits for notifications on conn until the connection fails or the bus
// is closed.
func (b *Bus) serve(conn *pgxpool.Conn) error {
	for {
		wctx, cancel := context.WithTimeout(b.ctx, pingInterval)
		n, err := conn.Conn().WaitForNotification(wctx)
		cancel()
		if err != nil {
			if b.ctx.Err() != nil {
				return b.ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				// idle: verify the connection is still alive
				pctx, pcancel := context.WithTimeout(b.ctx, 5*time.Second)
				perr := conn.Conn().Ping(pctx)
				pcancel()
				if perr != nil {
					return fmt.Errorf("ping: %w", perr)
				}
				continue
			}
			return err
		}
		var ev eventbus.Event
		if err := json.Unmarshal([]byte(n.Payload), &ev); err != nil {
			b.log.Warn("dropping malformed event", "error", err, "size", len(n.Payload))
			continue
		}
		b.deliverLocal(ev)
	}
}

func (b *Bus) deliverLocal(ev eventbus.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, s := range b.subs {
		if len(s.types) > 0 {
			if _, ok := s.types[ev.Type]; !ok {
				continue
			}
		}
		select {
		case s.ch <- ev:
		default:
			b.dropped.WithLabelValues(s.name).Inc()
		}
	}
}

// Publish implements eventbus.Bus.
func (b *Bus) Publish(ctx context.Context, typ eventbus.Type, payload any) error {
	if typ == eventbus.BusReconnected {
		return fmt.Errorf("eventbus: %s is a local-only event", typ)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("eventbus: marshal payload: %w", err)
	}
	ev := eventbus.Event{ID: uuid.New(), Type: typ, Time: time.Now().UTC(), Origin: b.replicaID, Payload: raw}
	enc, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("eventbus: marshal event: %w", err)
	}
	if len(enc) > eventbus.MaxEventSize {
		return fmt.Errorf("eventbus: event %s too large (%d > %d bytes)", typ, len(enc), eventbus.MaxEventSize)
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return eventbus.ErrClosed
	}
	if _, err := b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(enc)); err != nil {
		return fmt.Errorf("eventbus: publish %s: %w", typ, err)
	}
	return nil
}

// Subscribe implements eventbus.Bus.
func (b *Bus) Subscribe(types ...eventbus.Type) (<-chan eventbus.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &subscription{ch: make(chan eventbus.Event, subscriberBuffer), types: map[eventbus.Type]struct{}{}}
	names := make([]string, 0, len(types))
	for _, t := range types {
		s.types[t] = struct{}{}
		names = append(names, string(t))
	}
	sort.Strings(names)
	s.name = "all"
	if len(names) > 0 {
		s.name = strings.Join(names, ",")
	}
	if b.closed {
		close(s.ch)
		return s.ch, func() {}
	}
	id := b.nextID
	b.nextID++
	b.subs[id] = s
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(s.ch)
			}
		})
	}
}

// Close implements eventbus.Bus: stops the listener and closes all
// subscriber channels.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	b.wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, s := range b.subs {
		close(s.ch)
		delete(b.subs, id)
	}
	return nil
}
