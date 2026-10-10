// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package postgres implements store.Store on PostgreSQL with pgx/v5.
//
// One file per sub-store (agents.go, rollouts.go, ...).
//
// Implementation notes:
//   - All sub-stores run their statements through the small querier interface
//     which is implemented by *pgxpool.Pool and pgx.Tx; InTx hands out a DB
//     whose sub-stores use the transaction.
//   - Hot paths (TouchLastSeen, SetRemoteConfigState, SetHealth, ...) are
//     single statements; list queries fetch the page ids first and hydrate
//     only those rows (no N+1, no joins over the whole table).
//   - Unique violations map to domain.ErrConflict, missing rows to
//     domain.ErrNotFound, foreign key violations to domain.ErrNotFound.
//   - All timestamps are returned in UTC; maps are never nil.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/migrations"
)

// DefaultMaxConns is the pool size when the configuration does not set one.
const DefaultMaxConns = 20

// querier is the subset of pgx used by the sub-stores; *pgxpool.Pool and
// pgx.Tx both implement it.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
}

var (
	_ querier = (*pgxpool.Pool)(nil)
	_ querier = (pgx.Tx)(nil)
)

// ResolveDSN returns the DSN of cfg (DSNFile wins over DSN).
func ResolveDSN(cfg config.DatabaseConfig) (string, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if cfg.DSNFile != "" {
		b, err := os.ReadFile(cfg.DSNFile)
		if err != nil {
			return "", fmt.Errorf("postgres: read dsnFile: %w", err)
		}
		dsn = strings.TrimSpace(string(b))
	}
	if dsn == "" {
		return "", fmt.Errorf("%w: database dsn is empty", domain.ErrInvalidArgument)
	}
	return dsn, nil
}

// Connect creates the connection pool (DSN from cfg.DSNFile or cfg.DSN) and
// verifies connectivity. Connection attempts are retried for ~30s so that a
// database that starts slightly later than myriarchd (compose, Kubernetes) does
// not make the process crash-loop.
func Connect(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	dsn, err := ResolveDSN(cfg)
	if err != nil {
		return nil, err
	}
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", errors.New(sanitizeDSNError(err)))
	}
	maxConns := cfg.MaxConns
	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}
	pc.MaxConns = maxConns
	pc.MinConns = 1
	pc.MaxConnLifetime = time.Hour
	pc.MaxConnLifetimeJitter = 5 * time.Minute
	pc.MaxConnIdleTime = 15 * time.Minute
	pc.HealthCheckPeriod = 30 * time.Second
	if pc.ConnConfig.RuntimeParams == nil {
		pc.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, ok := pc.ConnConfig.RuntimeParams["application_name"]; !ok {
		pc.ConnConfig.RuntimeParams["application_name"] = brand.ServerBinary
	}
	// Every statement of the application is bounded on the server side (a
	// runaway query cannot occupy a connection forever). Maintenance jobs
	// (retention deletes, config GC) raise the limit per transaction; migrations
	// run on their own connections without limit.
	if cfg.StatementTimeout > 0 {
		if _, ok := pc.ConnConfig.RuntimeParams["statement_timeout"]; !ok {
			pc.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
		}
	}
	// A crashed client must not leave an open transaction holding locks.
	if _, ok := pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"]; !ok {
		pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	delay := time.Second
	deadline := time.Now().Add(30 * time.Second)
	for {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = pool.Ping(pctx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if ctx.Err() != nil || time.Now().Add(delay).After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("postgres: connect: %w", err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 8*time.Second {
			delay *= 2
		}
	}
}

// sanitizeDSNError removes the password from pgx parse errors.
func sanitizeDSNError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "://"); i >= 0 {
		return "invalid connection string"
	}
	return msg
}

// Migrate applies all pending migrations (advisory-locked) and returns the
// schema version.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (int64, error) {
	// migrations may run long and wait for each other (advisory lock): they use
	// their own connections without the statement timeout of the application
	mc := pool.Config().Copy()
	mc.MaxConns, mc.MinConns = 2, 0
	mc.ConnConfig.RuntimeParams = map[string]string{}
	for k, v := range pool.Config().ConnConfig.RuntimeParams {
		mc.ConnConfig.RuntimeParams[k] = v
	}
	mc.ConnConfig.RuntimeParams["statement_timeout"] = "0"
	mpool, err := pgxpool.NewWithConfig(ctx, mc)
	if err != nil {
		return 0, fmt.Errorf("postgres: migrate: %w", err)
	}
	defer mpool.Close()
	db := stdlib.OpenDBFromPool(mpool)
	defer db.Close()
	v, err := migrations.Up(ctx, db, logger)
	if err != nil {
		return 0, fmt.Errorf("postgres: migrate: %w", err)
	}
	return v, nil
}

// DB implements store.Store.
type DB struct {
	pool *pgxpool.Pool
	q    querier
	tx   bool
	log  *slog.Logger
}

var _ store.Store = (*DB)(nil)

// New returns a store.Store backed by pool.
func New(pool *pgxpool.Pool, logger *slog.Logger) *DB {
	if logger == nil {
		logger = slog.Default()
	}
	return &DB{pool: pool, q: pool, log: logger}
}

// Pool exposes the pool (event bus, leader election, health checks).
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

func (d *DB) Agents() store.AgentStore               { return &agentStore{db: d} }
func (d *DB) Configs() store.ConfigStore             { return &configStore{db: d} }
func (d *DB) GitOps() store.GitOpsStore              { return &gitopsStore{db: d} }
func (d *DB) Rollouts() store.RolloutStore           { return &rolloutStore{db: d} }
func (d *DB) FleetVersions() store.FleetVersionStore { return &fleetVersionStore{db: d} }
func (d *DB) Packages() store.PackageOfferStore      { return &packageStore{db: d} }
func (d *DB) Releases() store.ReleaseStore           { return &releaseStore{db: d} }
func (d *DB) Artifacts() store.ArtifactStore         { return &artifactStore{db: d} }
func (d *DB) Identity() store.IdentityStore          { return &identityStore{db: d} }
func (d *DB) Audit() store.AuditStore                { return &auditStore{db: d} }
func (d *DB) Replicas() store.ReplicaStore           { return &replicaStore{db: d} }
func (d *DB) KV() store.KVStore                      { return &kvStore{db: d} }

// InTx implements store.Store. Calling InTx on a transaction-bound Store joins
// the running transaction.
func (d *DB) InTx(ctx context.Context, fn func(tx store.Store) error) (err error) {
	if d.tx {
		return fn(d)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return dbErr("begin transaction", err)
	}
	txDB := &DB{pool: d.pool, q: tx, tx: true, log: d.log}
	committed := false
	defer func() {
		if committed {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rbErr := tx.Rollback(rctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			d.log.Warn("transaction rollback failed", "error", rbErr)
		}
		if p := recover(); p != nil {
			panic(p)
		}
	}()
	if err := fn(txDB); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return dbErr("commit transaction", err)
	}
	committed = true
	return nil
}

// withTx runs fn in a transaction (joining the current one if any).
func (d *DB) withTx(ctx context.Context, fn func(q querier) error) error {
	if d.tx {
		return fn(d.q)
	}
	return d.InTx(ctx, func(tx store.Store) error { return fn(tx.(*DB).q) })
}

// maintenanceTimeout is the statement limit of maintenance statements.
const maintenanceTimeout = 10 * time.Minute

// execMaintenance runs a long maintenance statement (retention deletes) in a
// transaction with a raised statement timeout and returns the affected rows.
func (d *DB) execMaintenance(ctx context.Context, sql string, args ...any) (int64, error) {
	var n int64
	err := d.withTx(ctx, func(q querier) error {
		if _, err := q.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", maintenanceTimeout.Milliseconds())); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return nil
	})
	return n, err
}

// Ping implements store.Store.
func (d *DB) Ping(ctx context.Context) error {
	if d.pool == nil {
		return fmt.Errorf("%w: no connection pool", domain.ErrUnavailable)
	}
	if err := d.pool.Ping(ctx); err != nil {
		return fmt.Errorf("%w: database ping: %v", domain.ErrUnavailable, err)
	}
	return nil
}

// Close implements store.Store.
func (d *DB) Close() {
	if d.pool != nil && !d.tx {
		d.pool.Close()
	}
}

// SchemaInfo returns the PostgreSQL server version and the highest applied
// migration version (GET /system/info).
func (d *DB) SchemaInfo(ctx context.Context) (serverVersion string, migrationVersion int64, err error) {
	if err = d.q.QueryRow(ctx, `SHOW server_version`).Scan(&serverVersion); err != nil {
		return "", 0, dbErr("server version", err)
	}
	err = d.q.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&migrationVersion)
	if err != nil {
		return serverVersion, 0, dbErr("migration version", err)
	}
	return serverVersion, migrationVersion, nil
}
