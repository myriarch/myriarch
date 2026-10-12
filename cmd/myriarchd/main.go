// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Command myriarchd is the myriarch server: OpAMP endpoint, REST API, embedded
// web UI, GitOps syncer, rollout engine, release tracker, artifact cache and
// notifier in one binary.
//
// Subcommands:
//
//	myriarchd serve   --config myriarchd.yaml     production server (external PostgreSQL)
//	myriarchd dev     [--gitops-dir DIR]       local development: embedded PostgreSQL,
//	                                        dev login, quickstart GitOps repo
//	myriarchd migrate --config myriarchd.yaml     apply database migrations and exit
//	myriarchd config validate --config F       check a configuration without starting
//	                                        (--skip-files: do not read referenced files)
//	myriarchd config print    --config F       print the effective configuration, secrets redacted
//	myriarchd auth hash-password [--cost N]    bcrypt hash of a password read from stdin
//	                                        (password file of the bootstrap admin)
//	myriarchd version
//
// The wiring in run.go calls the constructors of all components.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	// embed the Mozilla root CA set as the fallback trust store: the container
	// image is built FROM scratch (no OS packages, no CA bundle). The fallback
	// is used only when the host has no root certificates of its own
	// (x509.SetFallbackRoots); outbound.caFile and the other configured CAs
	// are added on top of whatever the system pool then returns (netguard).
	_ "golang.org/x/crypto/x509roots/fallback"

	// embed the IANA time zone database: maintenance windows evaluate agent
	// time zones, which must work in minimal container images without tzdata
	_ "time/tzdata"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/configload"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/version"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr))
}

// exitError ends the command with a specific exit code. An empty message
// means the command has already printed what is wrong.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// execute runs myriarchd with the given arguments and returns the exit code: 0 on
// success, the code of an exitError, else 1.
func execute(args []string, stdout, stderr io.Writer) int {
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(stderr, brand.ServerBinary+":", ee.msg)
		}
		return ee.code
	}
	fmt.Fprintln(stderr, brand.ServerBinary+":", err)
	return 1
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           brand.ServerBinary,
		Short:         "Myriarch server - OpAMP based management of OpenTelemetry Collectors",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	var configPath string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the myriarchd server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := configload.Load(configPath, false)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			ctx, stop := signalContext()
			defer stop()
			return run(ctx, cfg, runOptions{})
		},
	}
	serve.Flags().StringVarP(&configPath, "config", "c", "", "path to myriarchd.yaml (env MYRIARCH_* overrides apply)")

	var dev devOptions
	devCmd := &cobra.Command{
		Use:   "dev",
		Short: "Run myriarchd for local development (embedded PostgreSQL, dev login, quickstart GitOps repo)",
		Long: "Run myriarchd for local development (embedded PostgreSQL, dev login, quickstart GitOps repo).\n\n" +
			"The embedded PostgreSQL is only part of builds with the devpg build tag; --dsn is required when myriarchd was built without the devpg build tag.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			return runDev(ctx, dev)
		},
	}
	devCmd.Flags().StringVarP(&dev.configPath, "config", "c", "", "optional myriarchd.yaml applied on top of the dev defaults")
	devCmd.Flags().StringVar(&dev.gitopsDir, "gitops-dir", "examples/quickstart", "GitOps directory (plain directory or Git work tree)")
	devCmd.Flags().StringVar(&dev.dataDir, "data-dir", ".dev", "directory for embedded PostgreSQL data, clones and artifacts")
	devCmd.Flags().StringVar(&dev.listen, "listen", "127.0.0.1:8080", "listen address (dev mode only runs on loopback addresses unless --allow-nonlocal is given)")
	devCmd.Flags().BoolVar(&dev.allowNonLocal, "allow-nonlocal", false, "allow dev mode with a non-loopback listen address or database, OIDC or TLS (well-known logins and token: use with care)")
	devCmd.Flags().StringVar(&dev.releasesFixtures, "releases-fixtures", "", "read release data from recorded fixtures instead of GitHub")
	devCmd.Flags().StringVar(&dev.dsn, "dsn", "", "use this PostgreSQL DSN instead of starting embedded PostgreSQL (second dev replica; required when myriarchd was built without the devpg build tag)")
	devCmd.Flags().StringVar(&dev.replicaID, "replica-id", "", "replica id (default: hostname + random suffix)")

	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations and exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := configload.Load(configPath, false)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			ctx, stop := signalContext()
			defer stop()
			pool, err := postgres.Connect(ctx, cfg.Database)
			if err != nil {
				return err
			}
			defer pool.Close()
			v, err := postgres.Migrate(ctx, pool, nil)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "schema version:", v)
			return nil
		},
	}
	migrate.Flags().StringVarP(&configPath, "config", "c", "", "path to myriarchd.yaml")

	root.AddCommand(serve, devCmd, migrate, newConfigCommand(), newAuthCommand(), &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Get().String())
		},
	})
	return root
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
