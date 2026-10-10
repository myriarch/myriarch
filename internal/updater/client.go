// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/version"
)

// Deps are the replaceable collaborators of the Updater (tests inject fakes).
type Deps struct {
	Services ServiceController
	Verifier SignatureVerifier
	Exec     Executor
	Now      func() time.Time
	// Version is reported as service.version (default: build version).
	Version string
	// HTTPClient is used for health checks (default: 5s timeout client).
	HTTPClient *http.Client
	// DownloadClient overrides the artifact download client (tests).
	DownloadClient *http.Client
	// DisableRecovery skips the startup recovery of interrupted updates.
	DisableRecovery bool
	// InstallMethod is reported as dev.myriarch.updater.install_method
	// ("binary" by default, "container" in container mode).
	InstallMethod string
	// ImageCollectorVersion is the collector version baked into the image
	// (container mode, reported as dev.myriarch.updater.image_collector_version).
	ImageCollectorVersion string
	// CollectorUIDRefresh is how often persistent_state.yaml is re-read
	// (default 1m).
	CollectorUIDRefresh time.Duration
	// Heartbeat is called by the main loop of Run every few seconds (container
	// mode feeds /healthz with it; nil = none).
	Heartbeat func()
}

// Updater is the myriarch-updater: an OpAMP client that installs collector
// binaries offered by myriarchd.
type Updater struct {
	cfg  Config
	log  *slog.Logger
	deps Deps

	st       *stateStore
	progress *attemptTracker
	layout   *Layout
	inst     *Installer
	provider *packagesProvider
	offers   *offerBook

	selfUID uuid.UUID
	started time.Time

	mu           sync.Mutex
	collectorUID string
	updating     string // version being installed, "" if idle
	lastErr      string // last update error
	opamp        client.OpAMPClient

	syncMu    sync.Mutex
	pending   types.PackagesSyncer
	kick      chan struct{}
	workerWG  sync.WaitGroup
	runCtx    context.Context
	token     string
	tokenFrom string
}

// New creates an Updater. Nothing is started and nothing on the host is
// touched until Run.
func New(cfg Config, log *slog.Logger, deps Deps) (*Updater, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Services == nil {
		deps.Services = NewServiceController()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.Version == "" {
		deps.Version = version.Version
	}
	if deps.CollectorUIDRefresh <= 0 {
		deps.CollectorUIDRefresh = time.Minute
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if deps.Verifier == nil {
		deps.Verifier = &SigstoreVerifier{
			TrustedRootFile:           cfg.Update.TrustedRootFile,
			CertificateIdentityRegexp: cfg.Update.CertificateIdentityRegexp,
			CertificateOIDCIssuer:     cfg.Update.CertificateOIDCIssuer,
			RequireSignature:          cfg.Update.RequireSignature,
			TUFCacheDir:               cfg.StateDir + string(os.PathSeparator) + "tuf-cache",
		}
	}
	st, err := openStateStore(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	uid, err := loadOrCreateInstanceUID(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	layout := &Layout{InstallDir: cfg.Collector.InstallDir, BinaryName: cfg.Collector.BinaryName, Activation: cfg.Collector.Activation}
	u := &Updater{
		cfg: cfg, log: log, deps: deps, st: st, layout: layout, selfUID: uid,
		offers: &offerBook{}, kick: make(chan struct{}, 1),
	}
	u.progress = newAttemptTracker(cfg.StateDir, deps.Now, u.publishHealth)
	u.inst = &Installer{
		Progress: u.progress,
		Cfg:      cfg, Log: log, Layout: layout, Services: deps.Services,
		Health: &HealthChecker{
			Services:      deps.Services,
			ServiceName:   cfg.Supervisor.ServiceName,
			SupervisorURL: cfg.Supervisor.HealthURL,
			CollectorURL:  cfg.Collector.HealthURL,
			HTTPClient:    deps.HTTPClient,
			Interval:      cfg.Update.HealthInterval.D(),
			Successes:     cfg.Update.HealthSuccesses,
			Timeout:       cfg.Update.HealthTimeout.D(),
		},
		Verifier: deps.Verifier, Exec: deps.Exec, Now: deps.Now,
	}
	u.provider = &packagesProvider{
		st: st, layout: layout, inst: u.inst, offers: u.offers, log: log, now: deps.Now,
		backoff: cfg.Update.FailedRetryBackoff.D(),
		events: installEvents{
			started:  u.installStarted,
			finished: u.installFinished,
		},
	}
	return u, nil
}

// Recover repairs an update that was interrupted by a restart (journal). Run
// does this on its own unless Deps.DisableRecovery is set.
func (u *Updater) Recover(ctx context.Context) error { return u.inst.Recover(ctx) }

// updateBusy reports whether an update attempt is between the stop of the
// supervisor and the end of its health check (the collector is expected to
// be unavailable).
func (u *Updater) updateBusy() bool {
	a := u.progress.snapshot()
	if a == nil || a.Final {
		return false
	}
	switch a.Phase {
	case StepInstalling, StepRestarting, StepHealthCheck, StepRollingBack:
		return true
	}
	return false
}

// InstanceUID returns the updater's own instance UID.
func (u *Updater) InstanceUID() uuid.UUID {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.selfUID
}

func (u *Updater) installStarted(v string) {
	u.mu.Lock()
	u.updating = v
	u.mu.Unlock()
	u.publishHealth()
}

func (u *Updater) installFinished(v string, err error) {
	u.mu.Lock()
	u.updating = ""
	if err != nil {
		u.lastErr = err.Error()
	} else {
		u.lastErr = ""
	}
	u.mu.Unlock()
	u.publishHealth()
}

// health computes the ComponentHealth: healthy if the supervisor state is
// readable (or not yet created) and the supervisor service exists.
func (u *Updater) health(ctx context.Context) *protobufs.ComponentHealth {
	now := uint64(u.deps.Now().UnixNano())
	h := &protobufs.ComponentHealth{
		Healthy:            true,
		StartTimeUnixNano:  uint64(u.started.UnixNano()),
		Status:             "StatusOK",
		StatusTimeUnixNano: now,
	}
	var problems []string
	if ok, err := u.deps.Services.Exists(ctx, u.cfg.Supervisor.ServiceName); err != nil {
		problems = append(problems, fmt.Sprintf("supervisor service check: %v", err))
	} else if !ok {
		problems = append(problems, fmt.Sprintf("supervisor service %q not found", u.cfg.Supervisor.ServiceName))
	}
	if _, err := readSupervisorInstanceID(u.cfg.SupervisorStatePath()); err != nil {
		problems = append(problems, err.Error())
	}
	u.mu.Lock()
	updating, lastErr := u.updating, u.lastErr
	u.mu.Unlock()
	if len(problems) > 0 {
		h.Healthy = false
		h.Status = "StatusRecoverableError"
		h.LastError = strings.Join(problems, "; ")
	} else if updating != "" {
		h.Status = "StatusUpdating"
	} else if lastErr != "" {
		h.LastError = "last update failed: " + lastErr
	}
	if a := u.progress.snapshot(); a != nil {
		h.ComponentHealthMap = map[string]*protobufs.ComponentHealth{UpdateComponent: a.healthComponent()}
	}
	return h
}

func (u *Updater) publishHealth() {
	u.mu.Lock()
	c := u.opamp
	u.mu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.SetHealth(u.health(ctx)); err != nil {
		u.log.Debug("set health", "err", err)
	}
}

func (u *Updater) description() *protobufs.AgentDescription {
	u.mu.Lock()
	cuid, self := u.collectorUID, u.selfUID
	u.mu.Unlock()
	return buildDescriptionFor(installInfo{method: u.deps.InstallMethod, imageCollectorVersion: u.deps.ImageCollectorVersion},
		u.deps.Version, self, u.cfg, cuid)
}

// refreshCollectorUID re-reads the supervisor instance id and updates the
// agent description on change.
func (u *Updater) refreshCollectorUID(initial bool) {
	id, err := readSupervisorInstanceID(u.cfg.SupervisorStatePath())
	if err != nil {
		u.log.Warn("cannot read supervisor state", "err", err)
		return
	}
	u.mu.Lock()
	changed := id != u.collectorUID
	if changed {
		u.collectorUID = id
	}
	c := u.opamp
	u.mu.Unlock()
	if !changed {
		return
	}
	if id == "" {
		u.log.Info("supervisor state not available yet; collector link omitted")
	} else {
		u.log.Info("linked to supervisor instance", "collectorInstanceUid", id)
	}
	if !initial && c != nil {
		if err := c.SetAgentDescription(u.description()); err != nil {
			u.log.Warn("update agent description", "err", err)
		}
	}
}

func (u *Updater) readToken() (string, error) {
	if u.cfg.Server.TokenFile != "" {
		data, err := os.ReadFile(u.cfg.Server.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(u.cfg.Server.Token), nil
}

// headerFunc supplies the enrollment token on every (re)connect so that a
// rotated token file is picked up without restart.
func (u *Updater) headerFunc(h http.Header) http.Header {
	if h == nil {
		h = http.Header{}
	}
	tok, err := u.readToken()
	if err != nil {
		u.log.Error("cannot read enrollment token; reusing the last known one", "err", err)
		tok = u.token
	} else {
		u.token = tok
	}
	if tok != "" {
		h.Set("Authorization", "Bearer "+tok)
	}
	h.Set("User-Agent", brand.UpdaterBinary+"/"+u.deps.Version)
	return h
}

func (u *Updater) downloadClient(_ context.Context, file *protobufs.DownloadableFile) (*http.Client, error) {
	if err := urlAllowed(file.GetDownloadUrl(), u.cfg.Update.AllowedURLPrefixes); err != nil {
		return nil, err
	}
	if err := u.provider.downloadGuard(file.GetContentHash()); err != nil {
		return nil, err
	}
	if u.deps.DownloadClient != nil {
		return u.deps.DownloadClient, nil
	}
	return newDownloadClient(u.cfg)
}

func (u *Updater) onMessage(_ context.Context, msg *types.MessageData) {
	if id := msg.AgentIdentification; id != nil && len(id.NewInstanceUid) == 16 {
		u.mu.Lock()
		cur := u.selfUID
		u.mu.Unlock()
		if nu, err := uuid.FromBytes(id.NewInstanceUid); err == nil && nu != cur {
			if err := saveInstanceUID(u.cfg.StateDir, nu); err != nil {
				u.log.Error("persist new instance uid", "err", err)
			} else {
				u.log.Info("server assigned a new instance uid", "uid", nu)
				u.mu.Lock()
				u.selfUID = nu
				u.mu.Unlock()
			}
		}
	}
	if pa := msg.PackagesAvailable; pa != nil {
		u.offers.record(pa)
		for name, p := range pa.Packages {
			u.log.Info("package offered", "package", name, "version", p.GetVersion(), "url", p.GetFile().GetDownloadUrl())
		}
		if msg.PackageSyncer != nil {
			u.enqueueSync(msg.PackageSyncer)
		}
	}
}

// enqueueSync hands the syncer to the worker (latest offer wins) so that the
// OpAMP receive loop is never blocked by a long installation.
func (u *Updater) enqueueSync(s types.PackagesSyncer) {
	u.syncMu.Lock()
	u.pending = s
	u.syncMu.Unlock()
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

func (u *Updater) syncWorker(ctx context.Context) {
	defer u.workerWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.kick:
		}
		u.syncMu.Lock()
		s := u.pending
		u.pending = nil
		u.syncMu.Unlock()
		if s == nil {
			continue
		}
		if err := s.Sync(ctx); err != nil {
			u.log.Error("package sync failed to start", "err", err)
			continue
		}
		// Wait for completion before the next sync (also lets shutdown wait
		// for an in-flight install, which ignores cancellation once started).
		<-s.Done()
	}
}

func (u *Updater) startSettings(tlsCfg *tls.Config) types.StartSettings {
	var uid types.InstanceUid
	self := u.InstanceUID()
	copy(uid[:], self[:])
	return types.StartSettings{
		OpAMPServerURL: u.cfg.Server.Endpoint,
		HeaderFunc:     u.headerFunc,
		TLSConfig:      tlsCfg,
		InstanceUid:    uid,
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) { u.log.Info("connected to myriarchd") },
			OnConnectFailed: func(ctx context.Context, err error) {
				u.log.Warn("connection to myriarchd failed", "err", err)
			},
			OnError: func(ctx context.Context, e *protobufs.ServerErrorResponse) {
				u.log.Warn("server error response", "type", e.GetType().String(), "message", e.GetErrorMessage())
			},
			OnMessage:          u.onMessage,
			DownloadHTTPClient: u.downloadClient,
		},
		PackagesStateProvider: u.provider,
	}
}

// heartbeatInterval is how often the main loop of Run reports that it lives.
const heartbeatInterval = 5 * time.Second

func (u *Updater) beat() {
	if u.deps.Heartbeat != nil {
		u.deps.Heartbeat()
	}
}

// Run starts the OpAMP client and blocks until ctx is cancelled. An
// in-flight installation is allowed to finish (bounded by its own timeouts)
// so that the host is never left half-updated.
func (u *Updater) Run(ctx context.Context) error {
	u.started = u.deps.Now()
	u.runCtx = ctx
	warnLooseSecrets(u.log, u.cfg)
	u.log.Info("myriarch-updater starting", "version", u.deps.Version, "instanceUid", u.InstanceUID(),
		"supervisorService", u.cfg.Supervisor.ServiceName, "endpoint", u.cfg.Server.Endpoint)

	u.refreshCollectorUID(true)
	u.provider.markInterruptedStatuses()

	// Repair interrupted updates in the background (holds the install lock).
	var recoverWG sync.WaitGroup
	if !u.deps.DisableRecovery {
		recoverWG.Add(1)
		go func() {
			defer recoverWG.Done()
			if err := u.inst.Recover(context.WithoutCancel(ctx)); err != nil {
				u.log.Error("recovery of interrupted update failed", "err", err)
			}
		}()
	}

	var tlsCfg *tls.Config
	if strings.HasPrefix(u.cfg.Server.Endpoint, "wss://") || strings.HasPrefix(u.cfg.Server.Endpoint, "https://") {
		var err error
		tlsCfg, err = tlsConfig(u.cfg.Server.CAFile)
		if err != nil {
			return err
		}
	}

	var oc client.OpAMPClient
	if strings.HasPrefix(u.cfg.Server.Endpoint, "http") {
		oc = client.NewHTTP(slogAdapter{u.log})
	} else {
		oc = client.NewWebSocket(slogAdapter{u.log})
	}
	caps := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat
	if err := oc.SetCapabilities(&caps); err != nil {
		return fmt.Errorf("set capabilities: %w", err)
	}
	if err := oc.SetAgentDescription(u.description()); err != nil {
		return fmt.Errorf("set agent description: %w", err)
	}
	if err := oc.SetHealth(u.health(ctx)); err != nil {
		return fmt.Errorf("set health: %w", err)
	}
	u.mu.Lock()
	u.opamp = oc
	u.mu.Unlock()

	u.workerWG.Add(1)
	go u.syncWorker(ctx)

	if err := oc.Start(ctx, u.startSettings(tlsCfg)); err != nil {
		return fmt.Errorf("start opamp client: %w", err)
	}

	tick := time.NewTicker(u.deps.CollectorUIDRefresh)
	defer tick.Stop()
	healthTick := time.NewTicker(30 * time.Second)
	defer healthTick.Stop()
	beatTick := time.NewTicker(heartbeatInterval)
	defer beatTick.Stop()
	u.beat()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-beatTick.C:
			u.beat()
		case <-tick.C:
			u.refreshCollectorUID(false)
		case <-healthTick.C:
			u.publishHealth()
		}
	}

	u.log.Info("myriarch-updater stopping")
	// Wait for an in-flight installation (its critical section ignores ctx).
	u.workerWG.Wait()
	recoverWG.Wait()
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := oc.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		u.log.Warn("stop opamp client", "err", err)
	}
	return nil
}
