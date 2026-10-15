// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/store/migrations"
)

// compatMigration is the migration that creates the table schema_compat. A schema at or above it without that table is damaged.
const compatMigration = 6

// SchemaStatus is the state of the schema of a database relative to the
// migrations that this binary includes (docs/design/opamp-server.md, "Start,
// migrations and schema compatibility").
type SchemaStatus struct {
	// Version is the schema version of the database: its highest applied
	// migration, 0 for a database without migrations.
	Version int64
	// Level is the compatibility level of the database
	// (schema_compat.min_binary_schema): the lowest schema level that a binary
	// must have to run on it. 0 means that the database has no such record yet:
	// it is empty or older than migration 00006.
	Level int64
	// Supported is the schema level of this binary: the highest migration it
	// includes.
	Supported int64
}

// Pending reports whether the database lacks migrations that this binary
// includes.
func (s SchemaStatus) Pending() bool { return s.Version < s.Supported }

// Newer reports whether the database has migrations that this binary does not
// include.
func (s SchemaStatus) Newer() bool { return s.Version > s.Supported }

// Incompatible reports whether this binary cannot run on the database: its
// schema level is below the compatibility level of the database.
func (s SchemaStatus) Incompatible() bool { return s.Level > s.Supported }

// IncompatibleSchemaError is returned for a database whose compatibility level
// is higher than the schema level of this binary. Its message names the levels
// and the two ways out; the database is not changed.
type IncompatibleSchemaError struct{ Status SchemaStatus }

func (e *IncompatibleSchemaError) Error() string {
	s := e.Status
	return fmt.Sprintf("the database schema is incompatible with this %s: the database is at schema version %d and needs a %s with schema level %d or higher (compatibility level %d), "+
		"this %s has schema level %d; start a newer %s release or restore a backup of the database from before the upgrade",
		brand.ServerBinary, s.Version, brand.ServerBinary, s.Level, s.Level, brand.ServerBinary, s.Supported, brand.ServerBinary)
}

// OutdatedSchemaError is returned for a database that lacks migrations of this
// binary while database.migrateOnStart is off.
type OutdatedSchemaError struct{ Status SchemaStatus }

func (e *OutdatedSchemaError) Error() string {
	s := e.Status
	return fmt.Sprintf("the database schema is at version %d but this %s needs version %d and database.migrateOnStart is false: run \"%s migrate\" first, or set database.migrateOnStart to true",
		s.Version, brand.ServerBinary, s.Supported, brand.ServerBinary)
}

// NewerSchemaWarning is the message of the warning for a database that is newer than this binary but compatible with it. The
// attributes schemaVersion, compatibilityLevel and schemaLevel name the numbers. docs/design/opamp-server.md quotes it.
const NewerSchemaWarning = "the database schema is newer than this " + brand.ServerBinary + " but compatible with it (after a rollback, or while a rolling update is under way): " +
	"run the release that matches the schema as soon as you can"

// ReadSchemaStatus reads the schema version and the compatibility level of the
// database behind pool. A database without the migration table has version 0,
// and one that is older than migration 00006 has level 0. It is an error when a
// schema at or above migration 00006 has no table schema_compat, or when that
// table holds no row: the database was changed by hand, and without the level
// nobody can say whether this binary can work with it.
func ReadSchemaStatus(ctx context.Context, pool *pgxpool.Pool) (SchemaStatus, error) {
	supported, err := migrations.Latest()
	if err != nil {
		return SchemaStatus{}, err
	}
	st := SchemaStatus{Supported: supported}
	// The order of the reads matters while another replica migrates: each statement sees what was committed before it
	// started, and a migration commits its tables together with its version. Reading the version first and only then
	// looking for the table schema_compat means that a version at or above 00006 always comes with the table. The other
	// order can see the version of a migration that committed between the two reads, but not its table, and would refuse
	// a sound schema.
	var haveVersions, haveCompat bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&haveVersions); err != nil {
		return st, fmt.Errorf("postgres: read the schema state: %w", err)
	}
	if haveVersions {
		if err := pool.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&st.Version); err != nil {
			return st, fmt.Errorf("postgres: read the schema version: %w", err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('schema_compat') IS NOT NULL`).Scan(&haveCompat); err != nil {
		return st, fmt.Errorf("postgres: read the schema state: %w", err)
	}
	if !haveCompat && st.Version >= compatMigration {
		return st, fmt.Errorf("postgres: the database is at schema version %d but the table schema_compat is missing: restore the database from a backup", st.Version)
	}
	if haveCompat {
		if err := pool.QueryRow(ctx, `SELECT min_binary_schema FROM schema_compat`).Scan(&st.Level); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return st, errors.New("postgres: the table schema_compat has no row: restore the database from a backup or insert the row of the compatibility level")
			}
			return st, fmt.Errorf("postgres: read the compatibility level: %w", err)
		}
	}
	return st, nil
}

// PrepareSchema brings the schema of the database to a state that this binary
// can run on, or refuses. It is the schema step of the start of the server:
//
//   - A compatibility level above the schema level of this binary is refused
//     with an *IncompatibleSchemaError before anything is changed.
//   - A schema older than this binary gets the pending migrations when
//     migrateOnStart is set (under the advisory lock of the migrations; the
//     state is read again afterwards, because another replica may have gone
//     further), and is refused with an *OutdatedSchemaError otherwise.
//   - A schema that is newer than this binary and compatible with it logs a
//     warning and goes on.
//
// The result is the state after this step.
func PrepareSchema(ctx context.Context, pool *pgxpool.Pool, migrateOnStart bool, logger *slog.Logger) (SchemaStatus, error) {
	if logger == nil {
		logger = slog.Default()
	}
	st, err := ReadSchemaStatus(ctx, pool)
	if err != nil {
		return st, err
	}
	if st.Incompatible() {
		return st, &IncompatibleSchemaError{Status: st}
	}
	if st.Pending() {
		if !migrateOnStart {
			return st, &OutdatedSchemaError{Status: st}
		}
		if _, err := Migrate(ctx, pool, logger); err != nil {
			return st, err
		}
		if st, err = ReadSchemaStatus(ctx, pool); err != nil {
			return st, err
		}
		if st.Incompatible() {
			return st, &IncompatibleSchemaError{Status: st}
		}
	}
	if st.Newer() {
		logger.Warn(NewerSchemaWarning, "schemaVersion", st.Version, "compatibilityLevel", st.Level, "schemaLevel", st.Supported)
	}
	return st, nil
}
