// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package updater implements myriarch-updater, the host agent that installs
// collector binary updates offered via OpAMP PackagesAvailable (Linux
// systemd, Windows SCM).
//
// Safety model (in this order, nothing on the host is touched before step 4):
//
//  1. the download is spooled to disk and its SHA-256 compared with the
//     offered content hash,
//  2. the Sigstore bundle is verified offline against the artifact digest,
//     the pinned certificate identity and the release tag of the version,
//  3. the archive is extracted safely (only the collector binary from the
//     archive root, size limits, no links),
//  4. preflight: the new binary must report the expected version and accept
//     the supervisor's effective config,
//  5. the supervisor service is stopped, the new version activated atomically
//     (Linux: symlink swap, Windows: copy with .previous backup) and the
//     service started,
//  6. health check (service running, supervisor and collector health
//     endpoints, N consecutive successes) - on failure automatic rollback.
package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/version"
)

// systemdUnitTemplate is rendered by kardianos/service (its own mini
// template language) for "myriarch-updater install" on Linux. Keep it in sync
// with deploy/systemd/myriarch-updater.service (checked by a unit test).
const systemdUnitTemplate = `[Unit]
Description={{Description}}
After=network-online.target
Wants=network-online.target
ConditionFileIsExecutable={{Path | cmdEscape}}

[Service]
Type=simple
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
User=root
Restart=always
RestartSec=10
TimeoutStopSec=300
LimitNOFILE=4096
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
# Sandbox: the updater parses network data (OpAMP, archives, Sigstore) and runs as root; it may only write the
# collector installation tree and its own state (different install directory -> drop-in).
ProtectSystem=strict
ReadWritePaths=-/opt/myriarch/collector
StateDirectory=myriarch-updater
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
PrivateDevices=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictNamespaces=true
RestrictSUIDSGID=true
LockPersonality=true
SystemCallArchitectures=native
# DAC_*: read supervisor files; SETUID/SETGID/KILL: run the preflight as the supervisor's account and kill it on timeout
CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_FOWNER CAP_CHOWN CAP_SETUID CAP_SETGID CAP_KILL

[Install]
WantedBy=multi-user.target
`

// serviceConfig builds the kardianos service definition.
func serviceConfig(configPath string) *service.Config {
	return &service.Config{
		Name:        ServiceName,
		DisplayName: ServiceDisplayName,
		Description: "Myriarch updater: installs OpenTelemetry Collector updates offered via OpAMP",
		Arguments:   []string{"run", "--config", configPath},
		Option: service.KeyValue{
			"SystemdScript": systemdUnitTemplate,
			// Windows
			"StartType":              "automatic",
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "10s",
			"OnFailureResetPeriod":   3600,
		},
	}
}

// program adapts the Updater to kardianos/service.
type program struct {
	cfg    Config
	deps   Deps
	cancel context.CancelFunc
	done   chan struct{}
	logger service.Logger
	closer func() error
	err    error
}

func (p *program) Start(s service.Service) error {
	log, closer, err := newLogger(p.cfg.Log, os.Stderr)
	if err != nil {
		return err
	}
	p.closer = closer.Close
	u, err := New(p.cfg, log, p.deps)
	if err != nil {
		_ = closer.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := u.Run(ctx); err != nil {
			p.err = err
			log.Error("myriarch-updater terminated with error", "err", err)
			if p.logger != nil {
				_ = p.logger.Errorf("myriarch-updater terminated: %v", err)
			}
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(5 * time.Minute):
			return errors.New("timed out waiting for myriarch-updater to stop")
		}
	}
	if p.closer != nil {
		_ = p.closer()
	}
	return p.err
}

func absPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("--config is required")
	}
	return filepath.Abs(p)
}

// Run executes myriarch-updater with args (without the program name) and returns the process exit code: 0 on success, 1 on an
// error. Output (help, the version, command results) goes to stdout, the error message to stderr. The binary's main
// package passes os.Stdout and os.Stderr; tests pass separate buffers to check the output contract in docs/development.md.
func Run(args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, brand.UpdaterBinary+":", err)
		return 1
	}
	return 0
}

// NewRootCommand returns the root command: run, container, install,
// uninstall, start, stop, status, verify-bundle, fetch-trusted-root, licenses,
// version.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           brand.UpdaterBinary,
		Short:         "Myriarch host agent: installs collector updates offered via OpAMP",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	var cfgPath string
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the updater (foreground or as OS service)",
		RunE: func(cmd *cobra.Command, args []string) error {
			abs, err := absPath(cfgPath)
			if err != nil {
				return err
			}
			cfg, err := LoadConfig(abs)
			if err != nil {
				return err
			}
			p := &program{cfg: cfg}
			s, err := service.New(p, serviceConfig(abs))
			if err != nil {
				return err
			}
			if l, err := s.Logger(nil); err == nil {
				p.logger = l
			}
			return s.Run()
		},
	}
	run.Flags().StringVarP(&cfgPath, "config", "c", "", "path to updater.yaml")

	var containerCfg string
	container := &cobra.Command{
		Use:   "container",
		Short: "Run as PID 1 of a container: supervise the OpAMP supervisor and update the collector binary on a volume",
		Long: "Container mode (Linux, macOS): myriarch-updater starts the OpAMP supervisor as a child process, " +
			"keeps the collector binaries on a persistent volume (versions/<v> + current symlink), seeds them " +
			"from the container image and installs offered updates with the same verification, health check " +
			"and rollback as the service mode.",
		RunE: func(cmd *cobra.Command, args []string) error { return runContainerCommand(containerCfg) },
	}
	container.Flags().StringVarP(&containerCfg, "config", "c", "", "path to updater.yaml")

	var startAfter bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install myriarch-updater as OS service (systemd unit / Windows service)",
		RunE: func(cmd *cobra.Command, args []string) error {
			abs, err := absPath(cfgPath)
			if err != nil {
				return err
			}
			if _, err := LoadConfig(abs); err != nil {
				return fmt.Errorf("refusing to install with an invalid config: %w", err)
			}
			s, err := service.New(&program{}, serviceConfig(abs))
			if err != nil {
				return err
			}
			if err := s.Install(); err != nil {
				return fmt.Errorf("install service: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "service %s installed (config %s)\n", ServiceName, abs)
			if startAfter {
				if err := s.Start(); err != nil {
					return fmt.Errorf("start service: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "service %s started\n", ServiceName)
			}
			return nil
		},
	}
	install.Flags().StringVarP(&cfgPath, "config", "c", "", "path to updater.yaml")
	install.Flags().BoolVar(&startAfter, "start", false, "start the service after installing")

	simple := func(use, short string, fn func(service.Service) error, done string) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := service.New(&program{}, serviceConfig(""))
				if err != nil {
					return err
				}
				if err := fn(s); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), done)
				return nil
			},
		}
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the myriarch-updater service",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := service.New(&program{}, serviceConfig(""))
			if err != nil {
				return err
			}
			_ = s.Stop() // ignore: may not be running
			if err := s.Uninstall(); err != nil {
				return fmt.Errorf("uninstall service: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "service %s removed\n", ServiceName)
			return nil
		},
	}
	start := simple("start", "Start the service", func(s service.Service) error { return s.Start() }, "service started")
	stop := simple("stop", "Stop the service", func(s service.Service) error { return s.Stop() }, "service stopped")

	var statusCfg string
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the service state (and, with --config, the installed collector version)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := service.New(&program{}, serviceConfig(""))
			if err != nil {
				return err
			}
			st, err := s.Status()
			switch {
			case errors.Is(err, service.ErrNotInstalled):
				fmt.Fprintf(cmd.OutOrStdout(), "%s: not installed\n", ServiceName)
			case err != nil:
				fmt.Fprintf(cmd.OutOrStdout(), "%s: unknown (%v)\n", ServiceName, err)
			case st == service.StatusRunning:
				fmt.Fprintf(cmd.OutOrStdout(), "%s: running\n", ServiceName)
			case st == service.StatusStopped:
				fmt.Fprintf(cmd.OutOrStdout(), "%s: stopped\n", ServiceName)
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "%s: unknown\n", ServiceName)
			}
			if statusCfg != "" {
				return printHostStatus(cmd, statusCfg)
			}
			return nil
		},
	}
	status.Flags().StringVarP(&statusCfg, "config", "c", "", "updater.yaml (prints installed collector versions and the supervisor link)")

	var vb verifyBundleFlags
	verifyCmd := &cobra.Command{
		Use:   "verify-bundle",
		Short: "Verify an artifact against a Sigstore bundle offline (diagnostics)",
		Long: "Verifies that the Sigstore bundle authenticates the artifact (or its --sha256) as the " +
			"release artifact of --version, using the same policy as the updater.",
		RunE: func(cmd *cobra.Command, args []string) error { return runVerifyBundle(cmd, vb) },
	}
	verifyCmd.Flags().StringVar(&vb.bundle, "bundle", "", "Sigstore bundle JSON file (<asset>.sigstore.json)")
	verifyCmd.Flags().StringVar(&vb.artifact, "artifact", "", "artifact file (its SHA-256 is computed)")
	verifyCmd.Flags().StringVar(&vb.sha256, "sha256", "", "artifact SHA-256 (hex) instead of --artifact")
	verifyCmd.Flags().StringVar(&vb.version, "version", "", "release version, e.g. v0.162.0")
	verifyCmd.Flags().StringVar(&vb.trustedRoot, "trusted-root", "", "trusted_root.json (empty = fetch via TUF)")
	verifyCmd.Flags().StringVar(&vb.identity, "identity-regexp", DefaultCertificateIdentityRegexp, "certificate identity (SAN) regexp")
	verifyCmd.Flags().StringVar(&vb.issuer, "issuer", DefaultCertificateOIDCIssuer, "OIDC issuer")
	verifyCmd.Flags().StringVar(&vb.tufCache, "tuf-cache", "", "TUF cache directory (only without --trusted-root)")

	var trOut, trCache string
	trustedRootCmd := &cobra.Command{
		Use:   "fetch-trusted-root",
		Short: "Fetch the Sigstore trusted root via TUF and write it as trusted_root.json (diagnostics, mirror image build)",
		Long: "Fetches the trusted root of the public Sigstore instance via TUF (needs network) and writes it to --out. " +
			"The file is what update.trustedRootFile / artifacts.verify.trustedRootFile need for offline verification.",
		RunE: func(cmd *cobra.Command, args []string) error { return runFetchTrustedRoot(cmd, trOut, trCache) },
	}
	trustedRootCmd.Flags().StringVar(&trOut, "out", "", "output file (trusted_root.json)")
	trustedRootCmd.Flags().StringVar(&trCache, "tuf-cache", "", "TUF cache directory")

	root.AddCommand(run, container, install, uninstall, start, stop, status, verifyCmd, trustedRootCmd, newLicensesCommand(), &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Get().String())
		},
	})
	return root
}
