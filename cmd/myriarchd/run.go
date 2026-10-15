// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/assignment"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/configload"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/devpg"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/eventbus/pgbus"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/executor/manualexec"
	"github.com/myriarch/myriarch/internal/executor/updaterexec"
	"github.com/myriarch/myriarch/internal/executor/webhookexec"
	"github.com/myriarch/myriarch/internal/fleetapi"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/housekeeping"
	"github.com/myriarch/myriarch/internal/httpserver"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/leader/pglock"
	"github.com/myriarch/myriarch/internal/licenses"
	"github.com/myriarch/myriarch/internal/live"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/netguard"
	"github.com/myriarch/myriarch/internal/notify/channels"
	"github.com/myriarch/myriarch/internal/opamp"
	"github.com/myriarch/myriarch/internal/release"
	"github.com/myriarch/myriarch/internal/rollout"
	"github.com/myriarch/myriarch/internal/secretfile"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/migrations"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/version"
	"github.com/myriarch/myriarch/internal/webui"
)

type runOptions struct {
	dev bool
	// installBundleDir is the directory of installer bundles that a development server serves instead of the bundles
	// of a release (`myriarchd dev --install-bundle-dir`). run refuses it outside development mode.
	installBundleDir string
}

type devOptions struct {
	// allowNonLocal permits dev mode with a non-loopback listener or database,
	// OIDC or TLS (see devNonLocalReasons).
	allowNonLocal    bool
	configPath       string
	gitopsDir        string
	dataDir          string
	listen           string
	releasesFixtures string
	dsn              string
	replicaID        string
	// installBundleDir: see runOptions
	installBundleDir string
}

// runDev prepares the dev configuration (embedded PostgreSQL) and runs.
func runDev(ctx context.Context, o devOptions) error {
	cfg, err := configload.Load(o.configPath, true)
	if err != nil {
		return fmt.Errorf("load dev config: %w", err)
	}
	// Dev mode writes well-known logins and a well-known enrollment token into
	// its database: it refuses anything that looks like a production setup.
	if reasons := devNonLocalReasons(cfg, o); len(reasons) > 0 && !o.allowNonLocal {
		return fmt.Errorf("refusing to start dev mode: %s\n(dev mode creates the logins admin/admin, operator/operator, viewer/viewer and a well-known enrollment token; "+
			"use `myriarchd serve` for real deployments, or pass --allow-nonlocal if you really mean it)", strings.Join(reasons, "; "))
	}
	cfg.Dev.DataDir = o.dataDir
	cfg.Server.ListenAddr = o.listen
	cfg.Server.PublicURL = devPublicURL(cfg.Server.PublicURL, o.listen)
	if o.gitopsDir != "" {
		cfg.GitOps.RepoURL = "dir:" + o.gitopsDir
	}
	if o.releasesFixtures != "" {
		cfg.Releases.FixtureDir = o.releasesFixtures
	}
	if o.replicaID != "" {
		cfg.HA.ReplicaID = o.replicaID
	}
	cfg.Artifacts.Dir = o.dataDir + "/artifacts"
	cfg.GitOps.CloneDir = o.dataDir + "/gitops"

	logger := newLogger(cfg.Telemetry)
	if o.dsn != "" {
		cfg.Database.DSN = o.dsn
	} else {
		dsn, stopPG, err := devpg.Start(ctx, cfg.Dev, logger)
		if err != nil {
			return fmt.Errorf("start embedded postgres: %w", err)
		}
		defer func() {
			if err := stopPG(); err != nil {
				logger.Error("stop embedded postgres", "error", err)
			}
		}()
		cfg.Database.DSN = dsn
		// DEV ONLY: a second dev replica joins with --dsn (docs/tasks/run-two-replicas-locally.md).
		logger.Warn("DEV ONLY: embedded postgres ready (a second dev replica joins with --dsn)", "dsn", dsn)
	}
	cfg.Database.DSNFile = ""

	logDevBanner(ctx, logger,
		"ui", cfg.Server.PublicURL,
		"opamp", strings.Replace(cfg.Server.PublicURL, "http", "ws", 1)+cfg.OpAMP.Path,
		"logins", "admin/admin, operator/operator, viewer/viewer",
		"enrollmentToken", cfg.Dev.SeedEnrollmentToken,
		"gitops", cfg.GitOps.RepoURL,
		"database", maskDSN(cfg.Database.DSN))
	return run(ctx, cfg, runOptions{dev: true, installBundleDir: o.installBundleDir})
}

// devPublicURL returns the public URL of a dev instance. A publicUrl that the -c file or MYRIARCH_SERVER_PUBLICURL sets on purpose (for
// example the route of a dev instance behind a proxy, which needs --allow-nonlocal anyway) is kept: it feeds the OpAMP endpoint shown
// in the UI, artifact download URLs and notification links. Otherwise the URL points at the dev listener.
func devPublicURL(configured, listen string) string {
	if configured != config.DevDefaults().Server.PublicURL {
		return configured
	}
	if _, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		return "http://localhost:" + port
	}
	return configured
}

// run wires all components and blocks until ctx is cancelled.
func run(ctx context.Context, cfg config.Config, opts runOptions) error {
	// A directory of installer bundles has no signature to check: only a development server may serve one.
	if opts.installBundleDir != "" && !opts.dev {
		return errors.New("--install-bundle-dir exists only in development mode (myriarchd dev): a server in production serves the installer bundles of its own release")
	}
	logger := newLogger(cfg.Telemetry)
	slog.SetDefault(logger)
	replicaID := cfg.HA.ReplicaID
	if replicaID == "" {
		replicaID = defaultReplicaID()
	}
	logger = logger.With("replica", replicaID)
	logger.Info("starting myriarchd", "version", version.Get().String(), "dev", opts.dev)

	logConfigWarnings(logger, &cfg, opts.dev)
	if !opts.dev && cfg.Server.TLS.CertFile == "" {
		logger.Warn("myriarchd serves plain HTTP (server.tls.terminatedUpstream is set): TLS must be terminated in front of it and the port must not be reachable otherwise")
	}

	reg := metrics.NewRegistry()
	// All outbound connections (executors, notifier, release tracker, artifact
	// upstream, OIDC, Git over HTTPS) go through the destination policy: the
	// resolved address of every connection is checked. Dev mode allows local
	// targets (mock receivers, mirrors).
	policy, err := newOutboundPolicy(cfg.Outbound, opts.dev)
	if err != nil {
		return err
	}
	httpClient := policy.Client(2 * time.Minute)

	// ---------------------------------------------------------- database --
	pool, err := postgres.Connect(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	// The schema must be one this binary can run on: pending migrations are applied (database.migrateOnStart) or
	// refused, an incompatible schema is refused before anything else touches the database, and a newer but
	// compatible schema is a warning (docs/design/opamp-server.md, "Start, migrations and schema compatibility").
	schema, err := postgres.PrepareSchema(ctx, pool, cfg.Database.MigrateOnStart, logger)
	if err != nil {
		pool.Close()
		return err
	}
	logger.Info("database schema ready", "version", schema.Version, "compatibilityLevel", schema.Level, "schemaLevel", schema.Supported)
	db := postgres.New(pool, logger)
	var st store.Store = db
	warnPoliciesUsingServerTimezone(ctx, st, cfg.Rollouts.DefaultTimezone, logger)
	// Close order matters: the event bus holds a connection of the pool, so it
	// must be closed before the pool (defers run last-in first-out).
	defer st.Close()

	bus, err := pgbus.New(ctx, pool, replicaID, logger, reg)
	if err != nil {
		return fmt.Errorf("event bus: %w", err)
	}
	defer bus.Close()
	ready := readiness(db, bus)
	elector := pglock.New(pool, cfg.HA.LeaderLockKey, replicaID, logger, reg)
	auditRec := audit.NewRecorder(st.Audit(), logger)
	agentCtl := control.NewBusControl(bus)

	// -------------------------------------------------------------- auth --
	authSvc, err := authn.New(ctx, cfg.Auth, cfg.Server, cfg.OpAMP, authn.Deps{
		Store: st, Audit: auditRec, Logger: logger, Registerer: reg, DevMode: opts.dev, Bus: bus,
		HTTPClient: policy.Client(15 * time.Second), UI: cfg.UI,
	})
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if opts.dev {
		if err := authSvc.EnsureDevSeed(ctx, cfg.Dev); err != nil {
			return fmt.Errorf("dev seed: %w", err)
		}
	} else {
		// A database that was used with `myriarchd dev` still holds the well-known
		// enrollment token: it is revoked (and audited) before agents can connect.
		n, err := authSvc.RevokeDevSeedTokens(ctx, cfg.Dev.SeedEnrollmentToken, config.Default().Dev.SeedEnrollmentToken)
		if err != nil {
			return fmt.Errorf("revoke dev seed enrollment token: %w", err)
		}
		if n > 0 {
			logger.Warn("revoked the well-known dev enrollment token found in the database", "tokens", n)
		}
	}
	// The configuration is the only source of truth for the bootstrap admin
	// and for declarative enrollment tokens: both are reconciled into the
	// database before the first request is served.
	if err := authSvc.EnsureBootstrapAdmin(ctx); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	logBootstrapAdmin(logger, cfg.Auth)
	if err := authSvc.EnsureEnrollmentTokens(ctx, cfg.OpAMP.EnrollmentTokens, cfg.Dev.SeedEnrollmentToken); err != nil {
		return fmt.Errorf("enrollment tokens: %w", err)
	}
	downloadSecret, err := authn.LoadOrCreateDownloadSecret(ctx, cfg.Auth, st.KV())
	if err != nil {
		return fmt.Errorf("download secret: %w", err)
	}

	// ----------------------------------- gitops, rollouts, notifications --
	renderer := blueprint.NewRenderer(blueprint.Options{})
	notifier, err := channels.NewDispatcher(cfg.Notifications, channels.Deps{
		KV: st.KV(), HTTPClient: httpClient, PublicURL: cfg.Server.PublicURL, Logger: logger, Registerer: reg,
		QueueSize: channels.DefaultQueueSize, Workers: channels.DefaultWorkers, Context: ctx,
		RequireHTTPS: !opts.dev,
	})
	if err != nil {
		return fmt.Errorf("notifications: %w", err)
	}
	artifacts, err := artifact.NewCache(cfg.Artifacts, artifact.Deps{
		Store: st, Bus: bus, HTTPClient: httpClient, PublicURL: cfg.Server.PublicURL,
		DownloadSecret: downloadSecret, Logger: logger, Registerer: reg, Policy: policy,
		InstallGrantTTL: cfg.Install.GrantTTL,
	})
	if err != nil {
		return fmt.Errorf("artifact cache: %w", err)
	}
	// Outside dev mode a secret that Git references must be bound to its
	// permitted destinations (executors.secrets / secretEnvHosts / secretFileHosts).
	cfg.Executors.RequireSecretHosts = !opts.dev
	executors := executor.MapRegistry{
		domain.ExecutorUpdater: updaterexec.New(updaterexec.Deps{Store: st, Control: agentCtl, Artifacts: artifacts, Logger: logger}),
		domain.ExecutorWebhook: webhookexec.NewWebhook(cfg.Executors, httpClient, logger),
		domain.ExecutorAWX:     webhookexec.NewAWX(cfg.Executors, httpClient, logger),
		domain.ExecutorManual:  manualexec.New(),
	}
	assigner := assignment.New(assignment.Deps{
		Store: st, Bus: bus, Control: agentCtl, Renderer: renderer, Logger: logger, Registerer: reg,
	})
	engine := rollout.NewEngine(cfg.Rollouts, rollout.Deps{
		Store: st, Bus: bus, Control: agentCtl, Executors: executors, Notifier: notifier,
		Audit: auditRec, Logger: logger, Registerer: reg,
	})
	syncer, err := gitops.NewSyncer(cfg.GitOps, gitops.Deps{
		Store: st, Bus: bus, Renderer: renderer, Notifier: notifier, Audit: auditRec, Logger: logger, Registerer: reg,
		HTTPClient: httpClient, Policy: policy,
		OnApplied: func(ctx context.Context, snap *domain.GitOpsSnapshot, changes store.ObjectChanges) error {
			// All steps always run; the syncer retries the whole callback in
			// its next cycle if any fails. The UI version selections are
			// reconciled with the new Git state first (a changed Git target
			// wins, a selection outside the new frame is invalidated), so
			// that the agents are assigned the effective target.
			serr := engine.ReconcileSelections(ctx, snap)
			if serr != nil {
				logger.Error("reconcile version selections after gitops sync", "error", serr, "commit", snap.Commit)
			}
			rerr := assigner.ReassignAll(ctx)
			if rerr != nil {
				logger.Error("reassign after gitops sync", "error", rerr, "commit", snap.Commit)
			}
			perr := engine.Plan(ctx)
			if perr != nil {
				logger.Error("plan rollouts after gitops sync", "error", perr, "commit", snap.Commit)
			}
			return errors.Join(serr, rerr, perr)
		},
	})
	if err != nil {
		return fmt.Errorf("gitops: %w", err)
	}
	tracker, err := release.NewTracker(cfg.Releases, release.Deps{
		Store: st, Bus: bus, Notifier: notifier, HTTPClient: httpClient, Logger: logger, Registerer: reg,
	})
	if err != nil {
		return fmt.Errorf("release tracker: %w", err)
	}

	// ---------------------------------------------------- opamp and http --
	hostname, _ := os.Hostname()
	opampSrv, err := opamp.New(cfg.OpAMP, opamp.Deps{
		Store: st, Bus: bus, Assigner: assigner, Auth: authSvc, Logger: logger, Registerer: reg, ReplicaID: replicaID,
		Elector: elector, HA: cfg.HA, Hostname: hostname, Version: version.Get().Version,
		Audit: auditRec, Downloads: artifacts,
	})
	if err != nil {
		return fmt.Errorf("opamp: %w", err)
	}
	hub := live.New(bus, st, logger).WithAuth(authSvc)
	housekeeper := housekeeping.New(cfg.Retention, st, logger, reg)

	modules := []api.Module{authSvc.Module(), hub.Module()}
	modules = append(modules, coreapi.New(coreapi.Deps{
		Store: st, Bus: bus, Control: agentCtl, Audit: auditRec, Elector: elector, Config: cfg,
		ReplicaID: replicaID, DevMode: opts.dev, Logger: logger,
		SchemaInfo: db.SchemaInfo, FleetCounts: db.FleetCounts, LocalConnections: opampSrv.Connections,
	})...)
	// The installation of hosts from the server (ADR-0046): the plan that a host asks for with its enrollment token, the
	// installer bundles of the release that this server runs and the commands that the web UI renders. A disabled feature
	// is not built, and its routes answer 404.
	var installer *install.Planner
	var bundles *install.Bundles
	if cfg.Install.Enabled {
		platforms, err := install.ParsePlatforms(cfg.Install.Platforms)
		if err != nil {
			return fmt.Errorf("install.platforms: %w", err)
		}
		bundles, err = install.NewBundles(install.BundleDeps{
			Version: version.Get().Version, Dir: artifacts.Dir(), BaseURL: cfg.Install.BundleSourceBaseURL, Platforms: platforms,
			Verifier: install.CacheVerifier(artifacts), HTTPClient: httpClient, Policy: policy, ProxyURL: cfg.Artifacts.ProxyURL,
			AllowedRedirectHosts: cfg.Artifacts.AllowedRedirectHosts, Directory: opts.installBundleDir, DevMode: opts.dev, Logger: logger,
		})
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}
		installer, err = install.NewPlanner(install.Deps{
			Store: st, Assigner: assigner, Artifacts: artifacts, Config: cfg.Install,
			PublicURL: cfg.Server.PublicURL, OpAMPPath: cfg.OpAMP.Path, Logger: logger, Bundles: bundles, DevMode: opts.dev,
		})
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}
	}
	modules = append(modules, fleetapi.New(fleetapi.Deps{
		Store: st, Syncer: syncer, Assigner: assigner, Renderer: renderer, Engine: engine,
		Tracker: tracker, Artifacts: artifacts, Notifier: notifier, Audit: auditRec, Bus: bus, Config: cfg, Logger: logger,
		Install: installer, InstallAuth: authSvc, Registerer: reg,
	})...)

	var metricsHandler http.Handler
	if cfg.Telemetry.Metrics {
		metricsHandler = metrics.Handler(reg)
	}
	httpSrv, err := httpserver.New(cfg.Server, httpserver.Deps{
		Auth:        authSvc,
		Modules:     modules,
		OpAMPPath:   cfg.OpAMP.Path,
		OpAMP:       opampSrv.Handler(),
		ConnContext: opampSrv.ConnContext,
		UI:          webui.Handler(),
		Licenses:    licenses.Handler(),
		Ready:       ready,
		Metrics:     metricsHandler,
		Logger:      logger,
		Registerer:  reg,
	})
	if err != nil {
		return fmt.Errorf("http server: %w", err)
	}

	// ---------------------------------------------------------- run all --
	//
	// Shutdown order (graceful, e.g. Kubernetes SIGTERM):
	//  1. ctx (signal) is cancelled: the OpAMP server refuses new connections
	//     (503 + Retry-After), closes the agent connections gradually and
	//     writes pending state while HTTP, SSE and the background jobs still
	//     run (the event writer and the bus are needed for that);
	//  2. then everything else is stopped: HTTP server (graceful), SSE streams,
	//     leader jobs, leadership is released.
	//  3. finally the bus and the database pool are closed (deferred above).
	serveCtx, stopServing := context.WithCancel(context.Background())
	defer stopServing()
	g, gctx := errgroup.WithContext(serveCtx)
	g.Go(func() error { return httpSrv.Run(gctx) }) // fatal on error (e.g. port in use)
	g.Go(func() error { superviseJob(gctx, logger, "opamp", opampSrv.Run); return nil })
	g.Go(func() error { superviseJob(gctx, logger, "live", hub.Run); return nil })
	g.Go(func() error { superviseJob(gctx, logger, "assignment-snapshot", assigner.Run); return nil })
	g.Go(func() error { superviseJob(gctx, logger, "artifact-cache", artifacts.Run); return nil })
	if bundles != nil {
		g.Go(func() error { superviseJob(gctx, logger, "install-bundles", bundles.Run); return nil })
	}
	g.Go(func() error {
		err := elector.Run(gctx, func(lctx context.Context) {
			logger.Info("acquired leadership")
			defer logger.Info("leadership ended")
			jobs := map[string]func(context.Context) error{
				"opamp-leader":      opampSrv.RunLeader,
				"gitops-syncer":     syncer.Run,
				"rollout-engine":    engine.Run,
				"release-tracker":   tracker.Run,
				"artifact-prefetch": artifacts.RunPrefetch,
				"housekeeping":      housekeeper.Run,
			}
			lg, lgctx := errgroup.WithContext(lctx)
			for name, fn := range jobs {
				lg.Go(func() error { superviseJob(lgctx, logger, name, fn); return nil })
			}
			_ = lg.Wait()
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("leader election: %w", err)
		}
		return nil
	})

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			logger.Info("shutdown requested")
		case <-gctx.Done(): // a component failed
		}
		sctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := opampSrv.Shutdown(sctx); err != nil {
			logger.Error("opamp shutdown", "error", err)
		}
		stopServing()
	}()

	err = g.Wait()
	stopServing()
	<-shutdownDone
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("myriarchd stopped")
	return nil
}

// logConfigWarnings logs the warnings about the configuration (settings that
// weaken a control) and one warning for every secret file that other users of
// the host can read or write. The files of the bootstrap admin and of the
// declarative enrollment tokens are checked by authn when it reads them.
func logConfigWarnings(logger *slog.Logger, cfg *config.Config, dev bool) {
	for _, w := range configload.Warnings(cfg, dev) {
		logger.Warn(w)
	}
	secretfile.WarnLooseModes(logger, cfg)
}

// logBootstrapAdmin logs at info level whether the bootstrap admin accepts
// sign-ins (and with which credentials) or is configured but replaced by
// single sign-on.
func logBootstrapAdmin(logger *slog.Logger, a config.AuthConfig) {
	b := a.BootstrapAdmin
	switch {
	case a.BootstrapActive():
		var kinds []string
		if b.PasswordHashFile != "" {
			kinds = append(kinds, "password")
		}
		if b.TokenFile != "" {
			kinds = append(kinds, "token")
		}
		logger.Info("the bootstrap admin is active", "username", b.Username, "credentials", strings.Join(kinds, ","))
	case b.Enabled && a.OIDC.Enabled:
		logger.Info("the bootstrap admin is configured but inactive because auth.oidc is enabled (auth.bootstrapAdmin.keepWithOidc keeps it)", "username", b.Username)
	}
}

// newOutboundPolicy creates the destination policy and trust store of all
// outbound connections: the allowed CIDRs and the additional CA certificates
// of outbound.caFile (an internal CA). It must exist before the first HTTP
// client is created, because the clients take the trust store from it.
func newOutboundPolicy(o config.OutboundConfig, dev bool) (*netguard.Policy, error) {
	var policy *netguard.Policy
	if dev {
		policy = netguard.Permissive()
	} else {
		var err error
		if policy, err = netguard.New(o.AllowedCIDRs); err != nil {
			return nil, fmt.Errorf("outbound.allowedCidrs: %w", err)
		}
	}
	pool, err := netguard.RootPool(o.CAFile)
	if err != nil {
		return nil, fmt.Errorf("outbound.caFile: %w", err)
	}
	policy.SetRootCAs(pool)
	return policy, nil
}

// readiness returns the /readyz check: the database answers, the schema is at
// the version this binary needs and the event bus listener is connected.
//
// The schema step of the start (postgres.PrepareSchema) has made the schema fit
// before any listener opens, so the schema check passes on the first probe. A
// pass is kept for the lifetime of the process, because migrations only go
// forward: /readyz reads neither the schema version nor the compatibility level
// again, and it notices neither a later restore of an older backup nor a raised
// compatibility level (docs/design/opamp-server.md, "Start, migrations and
// schema compatibility"). After a restore, restart every replica.
func readiness(db *postgres.DB, bus eventbus.Bus) func(ctx context.Context) error {
	var schemaOK atomic.Bool
	return func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return err
		}
		if !schemaOK.Load() {
			want, err := migrations.Latest()
			if err != nil {
				return err
			}
			_, have, err := db.SchemaInfo(ctx)
			if err != nil {
				return fmt.Errorf("schema version: %w", err)
			}
			if have < want {
				return fmt.Errorf("database schema is at version %d, this build needs %d (run migrations)", have, want)
			}
			schemaOK.Store(true) // the start guarantees the schema and migrations only go forward: a pass is kept
		}
		if !pgbus.Connected(bus) {
			return errors.New("event bus is not connected")
		}
		return nil
	}
}

// superviseJob runs fn until ctx is done, restarting it with exponential
// backoff (5s .. 2m) when it returns early. A failing job (e.g. release
// tracker without network) must not take down other jobs.
func superviseJob(ctx context.Context, logger *slog.Logger, name string, fn func(context.Context) error) {
	backoff := 5 * time.Second
	for {
		started := time.Now()
		err := fn(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 10*time.Minute {
			backoff = 5 * time.Second
		}
		logger.Error("job stopped unexpectedly, restarting", "job", name, "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 2*time.Minute {
			backoff *= 2
		}
	}
}

func newLogger(t config.TelemetryConfig) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(t.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if t.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func defaultReplicaID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = brand.ServerBinary
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}

// warnPoliciesUsingServerTimezone logs one warning at startup that lists the
// stored rollout policies whose maintenance windows depend on
// rollouts.defaultTimezone. Policies that arrive later are reported by the
// rollout engine when it first plans a rollout with them.
func warnPoliciesUsingServerTimezone(ctx context.Context, st store.Store, defaultTimezone string, logger *slog.Logger) {
	policies, err := st.GitOps().ListRolloutPolicies(ctx)
	if err != nil {
		logger.Warn("list rollout policies for the time zone check", "error", err)
		return
	}
	rollout.WarnPoliciesUsingServerTimezone(logger, policies, defaultTimezone)
}
