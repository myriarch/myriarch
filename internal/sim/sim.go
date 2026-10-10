// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package sim implements myriarch-sim, a load simulator that runs many
// simulated supervisors (collector agents) and myriarch-updaters in one
// process using the opamp-go client.
//
// Each simulated host consists of a CollectorAgent (behaves like opampsupervisor
// + otelcol-contrib: accepts remote config with APPLYING/APPLIED/FAILED and
// automatic rollback semantics, reports health, effective config, available
// components, restarts) and optionally an UpdaterAgent (behaves like
// myriarch-updater: accepts package offers, reports package statuses and makes
// the collector report the new version).
package sim

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/version"
)

// Options configures a simulation run.
type Options struct {
	// Server is the OpAMP endpoint, e.g. ws://localhost:8080/v1/opamp.
	Server string
	// Token is the enrollment token (Authorization: Bearer).
	Token string
	// Agents is the number of simulated collectors.
	Agents int
	// Updaters enables simulated myriarch-updaters for groups with updater: true.
	Updaters bool
	Profile  *Profile
	// Seed overrides the profile seed when non-nil.
	Seed *int64
	// FakeDownloads: package downloads return a dummy body, nothing is
	// fetched and no hash is checked.
	FakeDownloads bool
	// MetricsListen serves Prometheus metrics (empty = disabled).
	MetricsListen string
	// ControlListen serves the local control API (empty = disabled).
	ControlListen string
	// Duration stops the run after this time (0 = until ctx is cancelled).
	Duration time.Duration
	// SummaryInterval, default 10s.
	SummaryInterval time.Duration
	// ChurnInterval is how often offlineChurnRate is evaluated (default 1m).
	ChurnInterval time.Duration
	// UpdaterVersion is reported as the updater's service.version.
	UpdaterVersion string
	// CAFile / InsecureSkipVerify for wss:// endpoints.
	CAFile             string
	InsecureSkipVerify bool
	// ClientDebug logs opamp-go client debug output (very verbose).
	ClientDebug bool
	Logger      *slog.Logger
}

// Stats is a snapshot of the simulation counters.
type Stats struct {
	Collectors          int
	Updaters            int
	ConnectedCollectors int
	ConnectedUpdaters   int
	ConfigsApplied      int64
	ConfigsFailed       int64
	UpdatesInstalled    int64
	UpdatesFailed       int64
	ConnectErrors       int64
}

// Simulator runs the simulation.
type Simulator struct {
	opts     Options
	log      *slog.Logger
	profile  *Profile
	behavior Behavior
	seed     int64
	comps    *componentCache

	collectors []*CollectorAgent
	updaters   []*UpdaterAgent
	byHost     map[string][]*CollectorAgent

	metrics        *simMetrics
	limiter        *rate.Limiter
	tlsConfig      *tls.Config
	downloadClient *http.Client

	ctx context.Context

	connectedCollectors atomic.Int64
	connectedUpdaters   atomic.Int64
	configsApplied      atomic.Int64
	configsFailed       atomic.Int64
	updatesOK           atomic.Int64
	updatesFailed       atomic.Int64
	connectErrors       atomic.Int64

	startedAt time.Time
}

// New builds a Simulator (agents are created but not started).
func New(opts Options) (*Simulator, error) {
	if opts.Profile == nil {
		return nil, errors.New("profile is required")
	}
	if opts.Server == "" {
		return nil, errors.New("server endpoint is required")
	}
	if opts.Agents <= 0 {
		return nil, errors.New("agents must be > 0")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.SummaryInterval <= 0 {
		opts.SummaryInterval = 10 * time.Second
	}
	if opts.ChurnInterval <= 0 {
		opts.ChurnInterval = time.Minute
	}
	if opts.UpdaterVersion == "" {
		opts.UpdaterVersion = version.Version
	}
	s := &Simulator{opts: opts, log: opts.Logger, profile: opts.Profile, behavior: opts.Profile.Behavior, seed: opts.Profile.Seed}
	if opts.Seed != nil {
		s.seed = *opts.Seed
	}
	s.metrics = newSimMetrics(prometheus.NewRegistry())
	s.limiter = rate.NewLimiter(rate.Limit(s.behavior.ConnectRate), max(1, int(s.behavior.ConnectRate/10)))

	var list *ComponentList
	if p := opts.Profile.ComponentsPath(); p != "" {
		var err error
		if list, err = LoadComponentList(p); err != nil {
			return nil, err
		}
	}
	s.comps = newComponentCache(list)

	tlsCfg, err := clientTLS(opts)
	if err != nil {
		return nil, err
	}
	s.tlsConfig = tlsCfg
	s.downloadClient = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second}}

	specs := opts.Profile.BuildSpecs(opts.Agents, s.seed)
	s.byHost = map[string][]*CollectorAgent{}
	for i, sp := range specs {
		c := newCollectorAgent(s, sp, i)
		s.collectors = append(s.collectors, c)
		key := lower(sp.Hostname)
		s.byHost[key] = append(s.byHost[key], c)
		if opts.Updaters && sp.WithUpdater {
			u := newUpdaterAgent(s, c, i)
			c.updater = u
			s.updaters = append(s.updaters, u)
		}
	}
	s.metrics.agentsTotal.Set(float64(len(s.collectors)))
	return s, nil
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func clientTLS(o Options) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.InsecureSkipVerify} //nolint:gosec // explicit opt-in flag for dev servers
	if o.CAFile != "" {
		pool, err := loadCAPool(o.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// tlsFor returns the TLS config only for TLS endpoints (opamp-go would
// otherwise attempt a TLS handshake on plain ws://).
func (s *Simulator) tlsFor() *tls.Config {
	if strings.HasPrefix(s.opts.Server, "wss://") || strings.HasPrefix(s.opts.Server, "https://") {
		return s.tlsConfig
	}
	return nil
}

func (s *Simulator) authHeader() http.Header {
	h := http.Header{}
	if s.opts.Token != "" {
		h.Set("Authorization", "Bearer "+s.opts.Token)
	}
	h.Set("User-Agent", version.UserAgent(brand.SimBinary))
	return h
}

func (s *Simulator) heartbeat() *time.Duration {
	d := s.behavior.HeartbeatInterval.D()
	return &d
}

type clientLog struct {
	l     *slog.Logger
	debug bool
}

func (c clientLog) Debugf(ctx context.Context, format string, v ...any) {
	if c.debug {
		c.l.Debug(fmt.Sprintf(format, v...))
	}
}
func (c clientLog) Errorf(ctx context.Context, format string, v ...any) {
	c.l.Debug(fmt.Sprintf(format, v...)) // transient connection errors are expected under churn
}

func (s *Simulator) clientLogger(kind, host string) types.Logger {
	return clientLog{l: s.log.With("agent", kind, "host", host), debug: s.opts.ClientDebug}
}

// Collectors returns the simulated collectors.
func (s *Simulator) Collectors() []*CollectorAgent { return s.collectors }

// Updaters returns the simulated updaters.
func (s *Simulator) Updaters() []*UpdaterAgent { return s.updaters }

// FindByHostname returns the collectors with this host name (case-insensitive).
func (s *Simulator) FindByHostname(h string) []*CollectorAgent { return s.byHost[lower(h)] }

// Stats returns a snapshot of the counters.
func (s *Simulator) Stats() Stats {
	return Stats{
		Collectors: len(s.collectors), Updaters: len(s.updaters),
		ConnectedCollectors: int(s.connectedCollectors.Load()), ConnectedUpdaters: int(s.connectedUpdaters.Load()),
		ConfigsApplied: s.configsApplied.Load(), ConfigsFailed: s.configsFailed.Load(),
		UpdatesInstalled: s.updatesOK.Load(), UpdatesFailed: s.updatesFailed.Load(),
		ConnectErrors: s.connectErrors.Load(),
	}
}

// begin prepares a run (file limit, logging).
func (s *Simulator) begin(ctx context.Context) error {
	s.startedAt = time.Now()
	s.ctx = ctx
	if lim, err := RaiseFileLimit(); err != nil {
		s.log.Warn("could not raise the file descriptor limit", "err", err)
	} else if lim > 0 {
		if lim >= 1<<62 {
			s.log.Info("file descriptor limit", "soft", "unlimited")
		} else {
			s.log.Info("file descriptor limit", "soft", lim)
		}
	}
	if need := len(s.collectors) + len(s.updaters); need > 0 {
		s.log.Info("starting simulation", "collectors", len(s.collectors), "updaters", len(s.updaters),
			"server", s.opts.Server, "seed", s.seed, "connectRate", s.behavior.ConnectRate)
	}
	return nil
}

// Run runs the simulation until ctx is cancelled (or Options.Duration
// elapsed), then disconnects everything.
func (s *Simulator) Run(ctx context.Context) error {
	if s.opts.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Duration)
		defer cancel()
	}
	if err := s.begin(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	servers, err := s.startHTTP(ctx)
	if err != nil {
		return err
	}
	wg.Add(1)
	go func() { defer wg.Done(); s.summaryLoop(ctx) }()
	if s.behavior.OfflineChurnRate > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); s.churnLoop(ctx) }()
	}
	s.ramp(ctx)

	<-ctx.Done()
	s.log.Info("stopping simulation")
	s.stopAll()
	wg.Wait()
	for _, srv := range servers {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(shutdown)
		cancel()
	}
	s.logSummary("final")
	return nil
}

// ramp connects all agents, throttled by connectRate (connections/s).
func (s *Simulator) ramp(ctx context.Context) {
	start := time.Now()
	started := 0
	for _, c := range s.collectors {
		if err := s.limiter.Wait(ctx); err != nil {
			return
		}
		if err := c.Start(ctx); err != nil {
			s.log.Error("start collector", "host", c.Hostname(), "err", err)
			s.connectErrors.Add(1)
		}
		started++
		if c.updater != nil {
			if err := s.limiter.Wait(ctx); err != nil {
				return
			}
			if err := c.updater.Start(ctx); err != nil {
				s.log.Error("start updater", "host", c.Hostname(), "err", err)
				s.connectErrors.Add(1)
			}
		}
	}
	s.log.Info("all agents started", "collectors", started, "updaters", len(s.updaters), "took", time.Since(start).Round(time.Millisecond))
}

func (s *Simulator) stopAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	work := make(chan func(), len(s.collectors)+len(s.updaters))
	for _, c := range s.collectors {
		c := c
		work <- func() { c.Stop(ctx) }
	}
	for _, u := range s.updaters {
		u := u
		work <- func() { u.Stop(ctx) }
	}
	close(work)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				f()
			}
		}()
	}
	wg.Wait()
}

func (s *Simulator) summaryLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.SummaryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.logSummary("summary")
		}
	}
}

func (s *Simulator) logSummary(msg string) {
	st := s.Stats()
	s.log.Info(msg,
		"collectorsConnected", fmt.Sprintf("%d/%d", st.ConnectedCollectors, st.Collectors),
		"updatersConnected", fmt.Sprintf("%d/%d", st.ConnectedUpdaters, st.Updaters),
		"configsApplied", st.ConfigsApplied, "configsFailed", st.ConfigsFailed,
		"updatesInstalled", st.UpdatesInstalled, "updatesFailed", st.UpdatesFailed,
		"connectErrors", st.ConnectErrors, "heapMiB", heapMiB(), "goroutines", goroutines())
}

// churnLoop sends random hosts offline for a short time.
func (s *Simulator) churnLoop(ctx context.Context) {
	rng := newChurnRNG(s.seed)
	t := time.NewTicker(s.opts.ChurnInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, c := range s.collectors {
			if rng.Float64() >= s.behavior.OfflineChurnRate {
				continue
			}
			down := time.Duration(5+rng.IntN(25)) * time.Second
			if s.opts.ChurnInterval < time.Minute { // tests with compressed time
				down = s.opts.ChurnInterval / 2
			}
			c.goOffline(ctx, down)
		}
	}
}

func (s *Simulator) startHTTP(ctx context.Context) ([]*http.Server, error) {
	var servers []*http.Server
	serve := func(addr string, h http.Handler, what string) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s (%s): %w", addr, what, err)
		}
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		s.log.Info("listening", "what", what, "addr", ln.Addr().String())
		go func() { _ = srv.Serve(ln) }()
		return nil
	}
	if s.opts.MetricsListen != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", newMetricsHandler(s.metrics))
		if err := serve(s.opts.MetricsListen, mux, "metrics"); err != nil {
			return nil, err
		}
	}
	if s.opts.ControlListen != "" {
		host, _, err := net.SplitHostPort(s.opts.ControlListen)
		if err == nil {
			if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				s.log.Warn("control API is not bound to a loopback address; it is unauthenticated", "addr", s.opts.ControlListen)
			}
		}
		if err := serve(s.opts.ControlListen, s.ControlHandler(), "control"); err != nil {
			for _, sv := range servers {
				_ = sv.Close()
			}
			return nil, err
		}
	}
	return servers, nil
}
