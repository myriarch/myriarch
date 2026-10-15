// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/configload"
	"github.com/myriarch/myriarch/internal/secretfile"
	"github.com/myriarch/myriarch/internal/store/postgres"
)

// Exit codes of "myriarchd migrate" besides 0 (done) and 1 (every error: settings, connection, a failed migration). The code
// 2 stays free: it means a usage error in the other commands.
const (
	exitMigrationsPending  = 3 // --check: the database lacks migrations of this version
	exitSchemaIncompatible = 4 // the schema is incompatible with this version (also without --check)
)

// newMigrateCommand returns "myriarchd migrate".
func newMigrateCommand() *cobra.Command {
	var configPath string
	var check bool
	cmd := &cobra.Command{
		Use:   "migrate [--config FILE] [--check]",
		Short: "Apply database migrations and exit",
		Long: `Applies the pending database migrations and exits.

The command needs only the database settings: the section "database" of the configuration file (database.dsn or
database.dsnFile) and the MYRIARCH_DATABASE_* environment variables. Every other section of the file and every other setting
is ignored, so the command runs where the server configuration, the login path and the secrets of other features do not
exist, for example in a job that runs before the server is updated.

Without --check the command applies the migrations that this version has and the database lacks, and prints the schema
version before and after and the compatibility level of the database. Running it again changes nothing. A database whose
schema is newer than this version but compatible with it is not changed (a warning is logged). A database whose
compatibility level is higher than the schema level of this version is refused.

With --check the command changes nothing. It prints the schema version, the compatibility level, the schema level of this
version and a status: current, pending (migrations are missing), newer (compatible) or incompatible.

Results go to stdout, log lines and errors to stderr.
Exit codes: 0 done (with --check: no migration is pending and this version can run on the schema), 1 error (settings,
connection, a failed migration), 3 with --check: migrations are pending, 4 the schema is incompatible with this version.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrate(cmd, configPath, check)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to myriarchd.yaml; only its database section is used (environment MYRIARCH_DATABASE_* overrides apply)")
	cmd.Flags().BoolVar(&check, "check", false, "change nothing: print the state of the schema and tell it by the exit code (0 ready, 3 migrations pending, 4 incompatible)")
	return cmd
}

func runMigrate(cmd *cobra.Command, configPath string, check bool) error {
	stdout := cmd.OutOrStdout()
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	dbCfg, warnings, err := configload.LoadDatabase(configPath)
	if err != nil {
		return fmt.Errorf("load database settings: %w", err)
	}
	for _, w := range warnings {
		logger.Warn(w)
	}
	secretfile.WarnLooseModes(logger, &config.Config{Database: dbCfg})

	ctx, stop := signalContext()
	defer stop()
	pool, err := postgres.Connect(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	before, err := postgres.ReadSchemaStatus(ctx, pool)
	if err != nil {
		return err
	}
	if check {
		return reportSchemaCheck(stdout, before)
	}
	// the same step as the start of the server with database.migrateOnStart set
	after, err := postgres.PrepareSchema(ctx, pool, true, logger)
	if err != nil {
		var incompatible *postgres.IncompatibleSchemaError
		if errors.As(err, &incompatible) {
			return &exitError{code: exitSchemaIncompatible, msg: err.Error()}
		}
		return err
	}
	fmt.Fprintf(stdout, "schema version before: %d\nschema version after: %d\ncompatibility level: %s\n",
		before.Version, after.Version, levelText(after.Level))
	return nil
}

// reportSchemaCheck prints the state of the schema (--check) and returns the exit code that tells it.
func reportSchemaCheck(stdout io.Writer, st postgres.SchemaStatus) error {
	status := "current"
	switch {
	case st.Incompatible():
		status = "incompatible"
	case st.Pending():
		status = "pending"
	case st.Newer():
		status = "newer"
	}
	fmt.Fprintf(stdout, "schema version: %d\ncompatibility level: %s\nschema level of this %s: %d\nstatus: %s\n",
		st.Version, levelText(st.Level), brand.ServerBinary, st.Supported, status)
	switch status {
	case "incompatible":
		return &exitError{code: exitSchemaIncompatible, msg: (&postgres.IncompatibleSchemaError{Status: st}).Error()}
	case "pending":
		return &exitError{code: exitMigrationsPending}
	}
	return nil
}

// levelText prints a compatibility level; 0 is a database that has none yet (empty, or older than migration 00006).
func levelText(level int64) string {
	if level == 0 {
		return "none"
	}
	return strconv.FormatInt(level, 10)
}
