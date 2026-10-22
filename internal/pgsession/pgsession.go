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
	MinTimeout = 5 * time.Second
	MaxTimeout = 10 * time.Minute
)

const userTimeoutParam = "tcp_user_timeout"

// Params returns the server parameters for the startup packet. With timeout T
// the server probes an idle peer after T/3 and then every T/6, three times, so
// a silent peer is detected after 5T/6; tcp_user_timeout bounds unacknowledged
// data to T. All values are whole seconds (milliseconds for the user timeout)
// and at least 1.
func Params(timeout time.Duration) map[string]string {
	secs := func(d time.Duration) string {
		s := int64(d / time.Second)
		if s < 1 {
			s = 1
		}
		return strconv.FormatInt(s, 10)
	}
	return map[string]string{
		"tcp_keepalives_idle":     secs(timeout / 3),
		"tcp_keepalives_interval": secs(timeout / 6),
		"tcp_keepalives_count":    "3",
		userTimeoutParam:          strconv.FormatInt(timeout.Milliseconds(), 10),
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
