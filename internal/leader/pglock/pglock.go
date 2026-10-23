// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package pglock implements leader.Elector with a PostgreSQL session-level
// advisory lock (pg_try_advisory_lock(key)) held on a dedicated connection.
//
// Semantics: the replica that acquires the lock is leader until its
// connection breaks (lock released by the server) or ctx ends. The failover is
// bounded by the session timeout T (ha.leaderSessionTimeout): the lock session
// is opened with TCP keepalive and user timeout settings derived from T
// (internal/pgsession), so the server drops a session whose client node died
// silently within pgsession.Detection(T), and a follower, asking every
// min(5s, T/6) (+20% jitter), takes the lock within T in total. The leader
// pings the session every T/6 with a timeout of T/3 and gives up leadership at
// the latest T/2 after the last confirmation, before the server can hand the
// lock to another replica (no split brain). It then asks the server, over a
// regular pool connection, to end the lock session at once. Never run singleton
// work without a live lock connection: a failed or timed-out health check ends
// leadership even if the server has not yet noticed that the session is gone.
// IsLeader reports false from that moment on, i.e. it is
// already false when the leader context is observed to be cancelled (the
// leader function may need some time to wind down).
//
// The lock connection is always closed (never returned to the pool) when
// leadership ends, so a session-level lock can not leak into pooled
// connections.
package pglock

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/leader"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/pgsession"
)

// DefaultSessionTimeout is the session timeout of an elector without
// WithSessionTimeout (the default of ha.leaderSessionTimeout).
const DefaultSessionTimeout = 30 * time.Second

// Timing parameters (variables so tests can shorten them).
var (
	// RetryInterval between acquisition attempts of a non-leader, at most; a
	// session timeout below 30s shortens it (pgsession.RetryInterval), so that
	// the retry fits into the failover budget.
	RetryInterval = 5 * time.Second
)

// Option configures an Elector.
type Option func(*elector)

// WithSessionTimeout bounds the failover: see the package documentation. It
// must be positive (the default is DefaultSessionTimeout).
func WithSessionTimeout(d time.Duration) Option {
	return func(e *elector) {
		if d > 0 {
			e.sessionTimeout = d
		}
	}
}

// New returns an Elector using advisory lock key. pool is the regular pool of
// the process: the elector opens its lock session on a pool of its own with the
// settings of pool and the dead-peer parameters of the session timeout, and
// uses pool to end a lock session that it can not reach any more.
func New(pool *pgxpool.Pool, key int64, replicaID string, logger *slog.Logger, reg prometheus.Registerer, opts ...Option) leader.Elector {
	if logger == nil {
		logger = slog.Default()
	}
	e := &elector{
		pool: pool, key: key, replicaID: replicaID, log: logger.With("component", "leader"),
		sessionTimeout: DefaultSessionTimeout,
		gauge: metrics.Register(reg, prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "myriarch_leader", Help: "1 while this replica holds the leader lock, else 0.",
		})),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

type elector struct {
	pool           *pgxpool.Pool // regular pool
	lockPool       *pgxpool.Pool // pool of the lock session (created by Run)
	sessionTimeout time.Duration
	connTweak      func(*pgxpool.Config) // tests: route the lock session through a proxy
	// tests: fixed timing of the health check
	checkInterval, checkTimeout time.Duration
	key                         int64
	replicaID                   string
	log                         *slog.Logger
	gauge                       prometheus.Gauge
	leading                     atomic.Bool
}

var _ leader.Elector = (*elector)(nil)

// retryInterval is the interval between acquisition attempts of a non-leader.
func (e *elector) retryInterval() time.Duration {
	return min(RetryInterval, pgsession.RetryInterval(e.sessionTimeout))
}

// IsLeader implements leader.Elector.
func (e *elector) IsLeader() bool { return e.leading.Load() }

// Run implements leader.Elector.
func (e *elector) Run(ctx context.Context, onElected func(ctx context.Context)) error {
	e.gauge.Set(0)
	defer func() {
		if e.lockPool != nil {
			e.lockPool.Close()
			e.lockPool = nil
		}
	}()
	for ctx.Err() == nil {
		if e.lockPool == nil {
			p, err := pgsession.NewPool(ctx, e.pool, e.sessionTimeout, e.connTweak, e.log)
			if err != nil {
				if ctx.Err() == nil {
					e.log.Warn("leader election: open lock session pool failed", "error", err)
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(jitter(e.retryInterval())):
				}
				continue
			}
			e.lockPool = p
		}
		held := e.tryLead(ctx, onElected)
		if ctx.Err() != nil {
			return nil
		}
		if held {
			// leadership ended (connection lost or onElected returned): retry
			// promptly, another replica may already hold the lock.
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitter(e.retryInterval())):
		}
	}
	return nil
}

// tryLead makes one acquisition attempt; it returns true if this replica
// became leader (and the leadership has ended by now).
func (e *elector) tryLead(ctx context.Context, onElected func(ctx context.Context)) bool {
	actx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := e.lockPool.Acquire(actx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			e.log.Warn("leader election: acquire connection failed", "error", err)
		}
		return false
	}
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	var got bool
	var pid int32
	err = conn.QueryRow(qctx, "SELECT pg_try_advisory_lock($1), pg_backend_pid()", e.key).Scan(&got, &pid)
	qcancel()
	if err != nil || !got {
		if err != nil && ctx.Err() == nil {
			e.log.Warn("leader election: lock attempt failed", "error", err)
			// the connection state is unknown: drop it
			closeConn(conn)
		} else {
			conn.Release()
		}
		return false
	}

	// ---- leader ----
	e.leading.Store(true)
	e.gauge.Set(1)
	e.log.Info("leader lock acquired", "key", e.key)
	lctx, lcancel := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		e.monitor(lctx, lcancel, conn, pid)
	}()

	onElected(lctx)

	lcancel()
	<-monitorDone
	e.leading.Store(false)
	e.gauge.Set(0)
	// Unlock explicitly (best effort), then close the session in any case.
	uctx, ucancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", e.key)
	ucancel()
	closeConn(conn)
	e.log.Info("leader lock released")
	return true
}

// monitor pings the lock connection; on failure it clears the leader state and
// cancels the leader context.
func (e *elector) monitor(ctx context.Context, cancel context.CancelFunc, conn *pgxpool.Conn, pid int32) {
	// health check every T/6 with a timeout of T/3: leadership ends at the
	// latest T/2 after the last confirmed check
	interval, timeout := e.sessionTimeout/6, e.sessionTimeout/3
	if e.checkInterval > 0 {
		interval, timeout = e.checkInterval, e.checkTimeout
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, pcancel := context.WithTimeout(ctx, timeout)
		var one int
		err := conn.QueryRow(pctx, "SELECT 1").Scan(&one)
		pcancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.log.Warn("leader lock connection lost, giving up leadership", "error", err)
			// first the state, then the context: whoever sees the cancelled
			// context sees that this replica is no longer the leader
			e.leading.Store(false)
			e.gauge.Set(0)
			cancel()
			e.terminateSession(pid)
			return
		}
	}
}

// terminateSession asks the server over the regular pool to end the lock
// session of backend pid, so that another replica need not wait until the
// server notices the dead connection. Only a backend that holds the lock is
// ended. It is best effort: if the database is unreachable, the keepalive
// settings of the session end it.
func (e *elector) terminateSession(pid int32) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := e.pool.Exec(ctx, `SELECT pg_terminate_backend(l.pid) FROM pg_locks l
WHERE l.pid = $1 AND l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND l.classid = (($2::bigint >> 32) & 4294967295)::bigint::oid
  AND l.objid = ($2::bigint & 4294967295)::bigint::oid`, pid, e.key)
	if err != nil {
		e.log.Warn("could not end the lock session on the server, it ends by itself", "error", err)
	}
}

func closeConn(conn *pgxpool.Conn) {
	raw := conn.Hijack()
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = raw.Close(cctx)
}

// jitter spreads retries of several replicas (+-20%).
func jitter(d time.Duration) time.Duration {
	n := time.Now().UnixNano()
	f := 0.8 + 0.4*float64(n%1000)/1000
	return time.Duration(float64(d) * f)
}
