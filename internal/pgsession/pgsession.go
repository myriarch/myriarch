// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package pgsession creates the dedicated, long-lived PostgreSQL sessions of
// myriarchd (the leader lock and the event bus listener) with server-side
// dead-peer detection.
//
// A session that holds state on the server (a session-level advisory lock, a
// LISTEN registration) is only released when PostgreSQL notices that the client
// is gone. After a hard failure of the client's node (no TCP FIN or RST) that
// takes the operating system's defaults, typically two hours. The sessions of
// this package therefore send TCP keepalive and user timeout settings in the
// startup packet, derived from one timeout, so that the server drops a dead
// peer within that timeout (ha.leaderSessionTimeout).
package pgsession

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bounds of the session timeout accepted by Params (configload enforces the
// same range for ha.leaderSessionTimeout).
const (
	MinTimeout = 10 * time.Second
	MaxTimeout = 10 * time.Minute
)

const (
	userTimeoutParam = "tcp_user_timeout"
	// keepaliveCount is tcp_keepalives_count; the interval is derived so that a
	// server that ignores tcp_user_timeout still stays within the budget.
	keepaliveCount = 5
	// maxRetryInterval is the retry interval of a follower that waits for the
	// lock (before the 20 percent jitter).
	maxRetryInterval = 5 * time.Second
	// retryJitter is the largest factor the jitter applies to the interval.
	retryJitter = 1.2
)

// RetryInterval is the interval at which a follower asks for the lock, for the
// session timeout T: 5 seconds, or T/6 when that is shorter. It is part of the
// end-to-end budget of Detection.
func RetryInterval(timeout time.Duration) time.Duration {
	if r := timeout / 6; r < maxRetryInterval {
		return r
	}
	return maxRetryInterval
}

// Detection is the part of the session timeout T in which the server must drop
// the session of a silently dead client, in whole seconds. A follower takes the
// lock at most one jittered retry interval after that, and a margin of T/20
// covers the acquisition itself, so that the takeover as a whole stays within T:
//
//	Detection(T) + 1.2 * RetryInterval(T) + T/20  <=  T
func Detection(timeout time.Duration) time.Duration {
	wait := time.Duration(float64(RetryInterval(timeout)) * retryJitter)
	d := timeout - wait - timeout/20
	if d < time.Second {
		return time.Second
	}
	return d.Truncate(time.Second)
}

// Params returns the server parameters for the startup packet. With the
// detection budget D = Detection(T) the keepalive interval and idle time are
// both I = D/(count+1), at least 1 second, and:
//
//   - Linux (tcp_user_timeout set): the keepalive timer fires at I, 2I, ... after
//     the last packet received and the connection is dropped at the first firing
//     after at least tcp_user_timeout = D - I; tcp_keepalives_count is not
//     consulted then. A silent peer is dropped after no less than D - I and less
//     than D.
//   - Servers without tcp_user_timeout: the connection is dropped after
//     I + count*I = (count+1)*I <= D.
//
// All values are whole seconds (milliseconds for the user timeout).
func Params(timeout time.Duration) map[string]string {
	d := int64(Detection(timeout) / time.Second)
	i := d / (keepaliveCount + 1)
	if i < 1 {
		i = 1
	}
	secs := strconv.FormatInt(i, 10)
	return map[string]string{
		"tcp_keepalives_idle":     secs,
		"tcp_keepalives_interval": secs,
		"tcp_keepalives_count":    strconv.Itoa(keepaliveCount),
		userTimeoutParam:          strconv.FormatInt((d-i)*1000, 10),
	}
}

// NewPool returns a pool of its own with the connection settings of base and
// the dead-peer parameters of Params(timeout) added to the startup packet. The
// caller owns the pool and closes it. tweak (may be nil) can still change the
// connection settings, for example to route the connections through a proxy.
//
// Platforms whose PostgreSQL server does not support tcp_user_timeout (macOS,
// where only the embedded development server runs) refuse the parameter; the
// pool is then created without it and a warning is logged.
func NewPool(ctx context.Context, base *pgxpool.Pool, timeout time.Duration, tweak func(*pgxpool.Config), log *slog.Logger) (*pgxpool.Pool, error) {
	if log == nil {
		log = slog.Default()
	}
	params := Params(timeout)
	for attempt := 0; ; attempt++ {
		cfg := base.Config().Copy()
		cfg.MinConns, cfg.MaxConns = 0, 2
		rp := map[string]string{}
		for k, v := range cfg.ConnConfig.RuntimeParams {
			rp[k] = v
		}
		for k, v := range params {
			rp[k] = v
		}
		cfg.ConnConfig.RuntimeParams = rp
		if tweak != nil {
			tweak(cfg)
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("pgsession: %w", err)
		}
		// pgxpool connects lazily: probe the startup packet once
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = pool.Ping(pctx)
		cancel()
		if err == nil {
			return pool, nil
		}
		pool.Close()
		var pgErr *pgconn.PgError
		if attempt == 0 && errors.As(err, &pgErr) && pgErr.Code == "22023" {
			if _, ok := params[userTimeoutParam]; ok {
				log.Warn("the database server does not support tcp_user_timeout: dead peers are detected by TCP keepalives only", "error", pgErr.Message)
				delete(params, userTimeoutParam)
				continue
			}
		}
		return nil, fmt.Errorf("pgsession: connect: %w", err)
	}
}
