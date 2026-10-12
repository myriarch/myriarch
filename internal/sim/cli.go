// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"

	"github.com/spf13/cobra"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/version"
)

func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file %s contains no PEM certificates", path)
	}
	return pool, nil
}

func newChurnRNG(seed int64) *rand.Rand { return rand.New(rand.NewPCG(uint64(seed), 0xc4)) }

// Run executes myriarch-sim with args (without the program name) and returns the process exit code: 0 on success, 1 on an
// error. Output (help, the version, command results) goes to stdout, the error message to stderr. The binary's main
// package passes os.Stdout and os.Stderr; tests pass separate buffers to check the output contract in docs/development.md.
func Run(args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, brand.SimBinary+":", err)
		return 1
	}
	return 0
}

// NewRootCommand returns the root command (run, version).
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           brand.SimBinary,
		Short:         "Simulate OpAMP supervisors and myriarch-updaters against myriarchd",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	var (
		server, token, profilePath string
		agents                     int
		updaters, fake             bool
		seed                       int64
		metricsListen, control     string
		duration                   time.Duration
		logLevel                   string
		caFile                     string
		insecure                   bool
	)
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the simulation",
		RunE: func(cmd *cobra.Command, args []string) error {
			if profilePath == "" {
				return errors.New("--profile is required")
			}
			p, err := LoadProfile(profilePath)
			if err != nil {
				return err
			}
			var lvl slog.Level
			if err := lvl.UnmarshalText([]byte(logLevel)); err != nil {
				return fmt.Errorf("--log-level: %w", err)
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
			opts := Options{
				Server: server, Token: resolveToken(token, os.Getenv), Agents: agents, Updaters: updaters, Profile: p,
				FakeDownloads: fake, MetricsListen: metricsListen, ControlListen: control,
				Duration: duration, CAFile: caFile, InsecureSkipVerify: insecure, Logger: log,
				ClientDebug: lvl <= slog.LevelDebug-4,
			}
			if cmd.Flags().Changed("seed") {
				opts.Seed = &seed
			}
			s, err := New(opts)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return s.Run(ctx)
		},
	}
	f := run.Flags()
	f.StringVar(&server, "server", "ws://localhost:8080/v1/opamp", "myriarchd OpAMP endpoint")
	f.StringVar(&token, "token", "", "enrollment token (myre_...); prefer the environment variable "+brand.EnrollmentTokenEnv+": arguments are visible in process listings")
	f.IntVar(&agents, "agents", 1000, "number of simulated collectors")
	f.BoolVar(&updaters, "updaters", true, "also simulate myriarch-updaters for groups with updater: true")
	f.StringVar(&profilePath, "profile", "", "simulation profile (examples/sim/mixed-fleet.yaml)")
	f.Int64Var(&seed, "seed", 0, "override the profile seed")
	f.StringVar(&metricsListen, "metrics-listen", "", "serve Prometheus metrics, e.g. :9464")
	f.StringVar(&control, "control-listen", "", "serve the local control API, e.g. 127.0.0.1:9465")
	f.DurationVar(&duration, "duration", 0, "stop after this duration (0 = run until interrupted)")
	f.BoolVar(&fake, "fake-downloads", true, "package downloads return a dummy body (no real download, no hash check)")
	f.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")
	f.StringVar(&caFile, "ca-file", "", "CA bundle for wss:// endpoints")
	f.BoolVar(&insecure, "insecure-skip-verify", false, "skip TLS verification (dev only)")

	root.AddCommand(run, &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Get().String())
		},
	})
	return root
}

// resolveToken returns the enrollment token of the --token flag, or the value of the environment variable
// brand.EnrollmentTokenEnv when the flag is empty. A process argument is visible in /proc and in process listings; the
// environment is not, so deployments should pass the token through the environment.
func resolveToken(flag string, getenv func(string) string) string {
	if flag != "" {
		return flag
	}
	return getenv(brand.EnrollmentTokenEnv)
}
