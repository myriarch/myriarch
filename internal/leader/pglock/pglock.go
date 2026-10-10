// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package pglock implements leader.Elector with a PostgreSQL session-level
// advisory lock (pg_try_advisory_lock(key)) held on a dedicated connection.
//
// Semantics: the replica that acquires the lock is leader until its
// connection breaks (lock released by the server) or ctx ends. The elector
// checks the connection every 5s (SELECT 1); on failure it cancels the
// leader context immediately, then retries acquisition every 5s. Never
// run singleton work without a live lock connection: a failed or timed-out
// health check ends leadership even if the server has not yet noticed that
// the session is gone. IsLeader reports false from that moment on, i.e. it is
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
)

// Timing parameters (variables so tests can shorten them).
var (
	// RetryInterval between acquisition attempts of a non-leader.
	RetryInterval = 5 * time.Second
	// CheckInterval between connection health checks of the leader.
	CheckInterval = 5 * time.Second
	// CheckTimeout for one health check.
	CheckTimeout = 3 * time.Second
)

// New returns an Elector using advisory lock key.
func New(pool *pgxpool.Pool, key int64, replicaID string, logger *slog.Logger, reg prometheus.Registerer) leader.Elector {
	if logger == nil {
		logger = slog.Default()
	}
	return &elector{
		pool: pool, key: key, replicaID: replicaID, log: logger.With("component", "leader"),
		gauge: metrics.Register(reg, prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "myriarch_leader", Help: "1 while this replica holds the leader lock, else 0.",
		})),
	}
}

type elector struct {
	pool      *pgxpool.Pool
	key       int64
	replicaID string
	log       *slog.Logger
	gauge     prometheus.Gauge
	leading   atomic.Bool
}

var _ leader.Elector = (*elector)(nil)

// IsLeader implements leader.Elector.
func (e *elector) IsLeader() bool { return e.leading.Load() }

// Run implements leader.Elector.
func (e *elector) Run(ctx context.Context, onElected func(ctx context.Context)) error {
	e.gauge.Set(0)
	for ctx.Err() == nil {
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
		case <-time.After(jitter(RetryInterval)):
		}
	}
	return nil
}

// tryLead makes one acquisition attempt; it returns true if this replica
// became leader (and the leadership has ended by now).
func (e *elector) tryLead(ctx context.Context, onElected func(ctx context.Context)) bool {
	actx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := e.pool.Acquire(actx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			e.log.Warn("leader election: acquire connection failed", "error", err)
		}
		return false
	}
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	var got bool
	err = conn.QueryRow(qctx, "SELECT pg_try_advisory_lock($1)", e.key).Scan(&got)
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
		e.monitor(lctx, lcancel, conn)
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
func (e *elector) monitor(ctx context.Context, cancel context.CancelFunc, conn *pgxpool.Conn) {
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, pcancel := context.WithTimeout(ctx, CheckTimeout)
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
			return
		}
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
