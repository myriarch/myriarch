// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package migrations embeds the SQL schema migrations (goose format) and
// applies them.
//
// Rules:
//   - Never edit a migration that may have been applied anywhere; add a new
//     file NNNNN_description.sql instead.
//   - Every migration has "-- +goose Up" and "-- +goose Down" sections.
//   - Migrations run at startup (database.migrateOnStart) under a Postgres
//     session advisory lock, so concurrent replicas are safe.
//   - From 00006 on, the header (the comment before "-- +goose Up") of every
//     migration states its effect on the compatibility level of the schema
//     (table schema_compat) in two lines that agree with each other: either
//     "-- Rolling update: expand only." with "-- Compatibility level:
//     unchanged.", or "-- Rolling update: not compatible, <what stops
//     working>" with "-- Compatibility level: raised to 00NNN." "Expand only"
//     means that the previous version keeps working on the changed schema.
//     A migration that binaries below some level cannot work with is "not
//     compatible" and raises the level: Up sets schema_compat.min_binary_schema
//     to NNN (more than the previous value, at most the number of the
//     migration) and Down sets the previous value again. TestMigrationHeaders
//     and TestCompatibilityLevelFollowsTheHeaders check this (a text of
//     neither form, and a text that contradicts the level line, fail);
//     docs/development.md explains how to choose.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// FS contains all *.sql migrations.
//
//go:embed *.sql
var FS embed.FS

// Up applies all pending migrations and returns the resulting version.
func Up(ctx context.Context, db *sql.DB, logger *slog.Logger) (int64, error) {
	if logger == nil {
		logger = slog.Default()
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return 0, fmt.Errorf("migrations: session locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS, goose.WithSessionLocker(locker))
	if err != nil {
		return 0, fmt.Errorf("migrations: provider: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrations: up: %w", err)
	}
	for _, r := range results {
		logger.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrations: version: %w", err)
	}
	return v, nil
}

// Latest returns the highest migration version contained in the binary: the
// schema level of this myriarchd. A schema must have at least this version
// for this myriarchd to run, and its compatibility level
// (schema_compat.min_binary_schema) must not be higher.
func Latest() (int64, error) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			continue
		}
		if n > latest {
			latest = n
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("migrations: no migration files embedded")
	}
	return latest, nil
}

// Version returns the current schema version.
func Version(ctx context.Context, db *sql.DB) (int64, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}
