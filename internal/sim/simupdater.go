// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/brand"
)

const updaterCaps = protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
	protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat

// UpdaterAgent simulates a myriarch-updater next to a simulated collector.
type UpdaterAgent struct {
	sim *Simulator
	col *CollectorAgent
	uid uuid.UUID

	mu        sync.Mutex
	cli       client.OpAMPClient
	running   bool
	connected bool
	startedAt time.Time
	offered   map[string]offeredPkg // by download URL
	rng       *rand.Rand
	packages  *memPackages

	attempt    *simAttempt // latest update attempt (reported as dev.myriarch.update)
	installing string
	installs   int
	failures   int
	lastError  string
}

type offeredPkg struct{ name, version string }

func newUpdaterAgent(s *Simulator, col *CollectorAgent, idx int) *UpdaterAgent {
	u := &UpdaterAgent{
		sim: s, col: col, uid: col.spec.UpdaterUID,
		offered:  map[string]offeredPkg{},
		rng:      rand.New(rand.NewPCG(uint64(s.seed)^0x5eed, fnvHash(col.spec.UpdaterUID.String())^uint64(idx))),
		packages: newMemPackages(),
	}
	u.packages.updater = u
	return u
}

func (u *UpdaterAgent) rand() float64 { return u.rng.Float64() } // caller holds u.mu

func (u *UpdaterAgent) description() *protobufs.AgentDescription {
	u.col.mu.Lock()
	cuid := u.col.uid
	u.col.mu.Unlock()
	spec := u.col.spec
	d := &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			strAttr("service.name", brand.UpdaterServiceName),
			strAttr("service.version", u.sim.opts.UpdaterVersion),
			strAttr("service.instance.id", u.uid.String()),
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			strAttr("os.type", spec.OS),
			strAttr("host.arch", spec.Arch),
			strAttr("host.name", spec.Hostname),
			strAttr(brand.AttrCollectorInstanceUID, cuid.String()),
			strAttr(brand.AttrUpdaterSupervisorService, "otelcol-supervisor"),
			strAttr(attrInstallMethod, "binary"),
		},
	}
	if ep := normalizeServerEndpoint(u.sim.opts.Server); ep != "" {
		d.NonIdentifyingAttributes = append(d.NonIdentifyingAttributes, strAttr(attrServerEndpoint, ep))
	}
	return d
}

// normalizeServerEndpoint reduces the OpAMP endpoint to scheme://host:port
// (explicit port, no path/credentials), the form the real myriarch-updater
// reports as dev.myriarch.updater.server_endpoint. "" if not parsable.
func normalizeServerEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "ws", "http":
			port = "80"
		case "wss", "https":
			port = "443"
		default:
			return ""
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// healthMsg is the updater health; the caller may hold u.mu.
func (u *UpdaterAgent) healthMsg() *protobufs.ComponentHealth {
	h := &protobufs.ComponentHealth{Healthy: true, StartTimeUnixNano: uint64(u.startedAt.UnixNano()),
		Status: "StatusOK", StatusTimeUnixNano: uint64(time.Now().UnixNano())}
	if u.attempt != nil {
		h.ComponentHealthMap = map[string]*protobufs.ComponentHealth{updateComponent: u.attempt.component()}
	}
	return h
}

// publishHealth sends the current health (update progress) to myriarchd.
func (u *UpdaterAgent) publishHealth() {
	u.mu.Lock()
	cli := u.cli
	var h *protobufs.ComponentHealth
	if cli != nil {
		h = u.healthMsg()
	}
	u.mu.Unlock()
	if cli != nil {
		_ = cli.SetHealth(h)
	}
}

// Start connects the updater (idempotent).
func (u *UpdaterAgent) Start(ctx context.Context) error {
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return nil
	}
	u.running = true
	u.startedAt = time.Now()
	c := client.NewWebSocket(u.sim.clientLogger("updater", u.col.spec.Hostname))
	caps := updaterCaps
	if err := c.SetCapabilities(&caps); err != nil {
		u.running = false
		u.mu.Unlock()
		return err
	}
	if err := c.SetAgentDescription(u.description()); err != nil {
		u.running = false
		u.mu.Unlock()
		return err
	}
	if err := c.SetHealth(u.healthMsg()); err != nil {
		u.running = false
		u.mu.Unlock()
		return err
	}
	var uid types.InstanceUid
	copy(uid[:], u.uid[:])
	settings := types.StartSettings{
		OpAMPServerURL:    u.sim.opts.Server,
		Header:            u.sim.authHeader(),
		TLSConfig:         u.sim.tlsFor(),
		InstanceUid:       uid,
		HeartbeatInterval: u.sim.heartbeat(),
		Callbacks: types.Callbacks{
			OnConnect:          func(context.Context) { u.onConnect() },
			OnConnectFailed:    func(_ context.Context, err error) { u.onConnectFailed(err) },
			OnMessage:          u.onMessage,
			DownloadHTTPClient: u.downloadClient,
		},
		PackagesStateProvider: u.packages,
	}
	u.cli = c
	u.mu.Unlock()
	if err := c.Start(ctx, settings); err != nil {
		u.mu.Lock()
		u.running, u.cli = false, nil
		u.mu.Unlock()
		return fmt.Errorf("start updater %s: %w", u.col.spec.Hostname, err)
	}
	return nil
}

// Stop disconnects the updater.
func (u *UpdaterAgent) Stop(ctx context.Context) {
	u.mu.Lock()
	if !u.running {
		u.mu.Unlock()
		return
	}
	u.running = false
	cli := u.cli
	u.cli = nil
	was := u.connected
	u.connected = false
	u.mu.Unlock()
	if was {
		u.sim.connectedUpdaters.Add(-1)
		u.sim.metrics.updatersConnected.Dec()
		u.sim.metrics.agentsConnected.Dec()
	}
	if cli != nil {
		_ = cli.Stop(ctx)
	}
}

func (u *UpdaterAgent) onConnect() {
	u.mu.Lock()
	was := u.connected
	u.connected = u.running
	u.mu.Unlock()
	if !was {
		u.sim.connectedUpdaters.Add(1)
		u.sim.metrics.updatersConnected.Inc()
		u.sim.metrics.agentsConnected.Inc()
	}
}

func (u *UpdaterAgent) onConnectFailed(err error) {
	u.mu.Lock()
	was := u.connected
	u.connected = false
	u.mu.Unlock()
	if was {
		u.sim.connectedUpdaters.Add(-1)
		u.sim.metrics.updatersConnected.Dec()
		u.sim.metrics.agentsConnected.Dec()
	}
	u.sim.metrics.connectFailures.Inc()
	u.sim.connectErrors.Add(1)
	u.sim.log.Debug("updater connect failed", "host", u.col.spec.Hostname, "err", err)
}

// collectorUIDChanged re-sends the description with the new link.
func (u *UpdaterAgent) collectorUIDChanged(uuid.UUID) {
	u.mu.Lock()
	cli := u.cli
	u.mu.Unlock()
	if cli != nil {
		_ = cli.SetAgentDescription(u.description())
	}
}

func (u *UpdaterAgent) onMessage(ctx context.Context, msg *types.MessageData) {
	if id := msg.AgentIdentification; id != nil && len(id.NewInstanceUid) == 16 {
		if nu, err := uuid.FromBytes(id.NewInstanceUid); err == nil {
			u.mu.Lock()
			u.uid = nu
			u.mu.Unlock()
		}
	}
	pa := msg.PackagesAvailable
	if pa == nil {
		return
	}
	u.mu.Lock()
	u.offered = map[string]offeredPkg{}
	for name, p := range pa.Packages {
		u.offered[p.GetFile().GetDownloadUrl()] = offeredPkg{name: name, version: p.GetVersion()}
	}
	u.mu.Unlock()
	if msg.PackageSyncer != nil {
		syncer := msg.PackageSyncer
		go func() { // Sync blocks while another sync is running: never block the receive loop
			if err := syncer.Sync(u.sim.ctx); err != nil {
				u.sim.log.Warn("package sync failed", "host", u.col.spec.Hostname, "err", err)
			}
		}()
	}
}

// fakeTransport answers every request with a small dummy body (no network).
type fakeTransport struct{}

func (fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := strings.Repeat("myriarch-sim fake package\n", 128)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Length": []string{fmt.Sprint(len(body))}, "Content-Type": []string{"application/gzip"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func (u *UpdaterAgent) downloadClient(_ context.Context, _ *protobufs.DownloadableFile) (*http.Client, error) {
	if u.sim.opts.FakeDownloads {
		return &http.Client{Transport: fakeTransport{}}, nil
	}
	return u.sim.downloadClient, nil
}

// Shares of the simulated install duration per step.
const (
	shareDownload = 0.30
	shareVerify   = 0.20
	shareInstall  = 0.10
	shareRestart  = 0.15
	shareHealth   = 0.25
)

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// failure kinds of a simulated update (UpdateFailureRate, half and half).
const (
	failNone         = ""
	failVerification = "verification" // before the swap: failed, host untouched
	failHealth       = "health"       // after the swap: rolled_back
)

// UpdateContent emulates the myriarch-updater installation (see package doc)
// including its step report (health component `dev.myriarch.update`).
func (u *UpdaterAgent) UpdateContent(ctx context.Context, name, downloadURL string, data io.Reader, contentHash, signature []byte) error {
	if name != "" {
		return fmt.Errorf("unsupported package %q", name)
	}
	u.mu.Lock()
	off, ok := u.offered[downloadURL]
	u.mu.Unlock()
	if !ok || off.version == "" {
		return fmt.Errorf("no package offer with a version known for %s", downloadURL)
	}
	version, err := normalizeVersion(off.version)
	if err != nil {
		return err
	}

	from := u.col.Version()
	fromV := ""
	if from != "" {
		fromV = "v" + from
	}
	a := newSimAttempt(version, fromV, time.Now())
	u.mu.Lock()
	u.attempt = a
	u.installing = version
	d := u.sim.behavior.UpdaterInstallDuration.Pick(u.rand())
	kind := failNone
	if u.rand() < u.sim.behavior.UpdateFailureRate {
		kind = failVerification
		if u.rand() < 0.5 {
			kind = failHealth
		}
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.installing = ""
		u.mu.Unlock()
	}()
	u.publishHealth() // attempt begun

	return u.simulateInstall(ctx, a, version, from, d, kind, data, contentHash)
}

// simulateInstall runs the steps of one attempt. kind decides how it ends.
func (u *UpdaterAgent) simulateInstall(ctx context.Context, a *simAttempt, version, from string, d time.Duration, kind string, data io.Reader, contentHash []byte) error {
	set := func(step, status, errText string) {
		a.set(step, status, errText, time.Now())
		u.publishHealth()
	}
	// fail ends the attempt in the failed state at the current step.
	fail := func(step string, err error) error {
		set(step, stepFailed, err.Error())
		a.end(outcomeFailed, err.Error(), false, time.Now())
		return u.failInstall(version, err)
	}
	frac := func(f float64) time.Duration { return time.Duration(float64(d) * f) }

	// 1. download: drain the stream (the real updater spools it to disk)
	set(stepDownloading, stepRunning, "")
	h := sha256.New()
	if _, err := io.Copy(h, data); err != nil {
		return fail(stepDownloading, fmt.Errorf("download: %w", err))
	}
	if err := sleepCtx(ctx, frac(shareDownload)); err != nil {
		return fail(stepDownloading, err)
	}
	set(stepDownloading, stepDone, "")

	// 2. verification: SHA-256 (real downloads only), signature, preflight
	set(stepVerifying, stepRunning, "")
	if !u.sim.opts.FakeDownloads {
		if len(contentHash) != sha256.Size {
			return fail(stepVerifying, errors.New("package offer carries no valid SHA-256 content hash"))
		}
		if got := h.Sum(nil); string(got) != string(contentHash) {
			return fail(stepVerifying, fmt.Errorf("SHA-256 mismatch: offered %x, downloaded %x", contentHash, got))
		}
	}
	if err := sleepCtx(ctx, frac(shareVerify)); err != nil {
		return fail(stepVerifying, err)
	}
	if kind == failVerification {
		return fail(stepVerifying, errors.New("signature verification: no valid Sigstore bundle for the artifact (simulated)"))
	}
	set(stepVerifying, stepDone, "")

	// 3. install: stop the supervisor, swap the binary
	set(stepInstalling, stepRunning, "")
	if err := sleepCtx(ctx, frac(shareInstall)); err != nil {
		return fail(stepInstalling, err)
	}
	set(stepInstalling, stepDone, "")

	// 4. restart
	set(stepRestarting, stepRunning, "")
	if err := sleepCtx(ctx, frac(shareRestart)); err != nil {
		return fail(stepRestarting, err)
	}
	if kind == failHealth {
		set(stepRestarting, stepDone, "")
		// 5'. the new collector never gets healthy: roll back
		set(stepHealthCheck, stepRunning, "")
		if err := sleepCtx(ctx, frac(shareHealth)); err != nil {
			return fail(stepHealthCheck, err)
		}
		cause := errors.New("health check: not healthy within 120s (simulated)")
		set(stepHealthCheck, stepFailed, cause.Error())
		set(stepRollingBack, stepRunning, "")
		u.col.restart("", nil) // swap + rollback look like restarts to the fleet
		set(stepRollingBack, stepDone, "")
		err := fmt.Errorf("update to v%s failed: %w; rolled back to v%s", version, cause, from)
		a.end(outcomeRolledBack, err.Error(), false, time.Now())
		return u.failInstall(version, err)
	}
	if u.sim.behavior.VersionAfterUpdate == "offered" {
		select {
		case <-u.col.upgrade(version):
		case <-ctx.Done():
			return fail(stepRestarting, ctx.Err())
		}
	} else {
		// "unchanged": the host restarts but keeps reporting the old version
		u.col.restart("", nil)
	}
	set(stepRestarting, stepDone, "")

	// 5. health check
	set(stepHealthCheck, stepRunning, "")
	if err := sleepCtx(ctx, frac(shareHealth)); err != nil {
		return fail(stepHealthCheck, err)
	}
	set(stepHealthCheck, stepDone, "")
	a.end(outcomeSucceeded, "", false, time.Now())
	u.publishHealth()

	u.mu.Lock()
	u.installs++
	u.lastError = ""
	u.mu.Unlock()
	u.sim.metrics.updates.WithLabelValues("installed").Inc()
	u.sim.updatesOK.Add(1)
	return nil
}

func (u *UpdaterAgent) failInstall(version string, err error) error {
	u.mu.Lock()
	u.failures++
	u.lastError = err.Error()
	u.mu.Unlock()
	u.publishHealth()
	u.sim.metrics.updates.WithLabelValues("failed").Inc()
	u.sim.updatesFailed.Add(1)
	return err
}

// UpdaterState is the debug view of a simulated updater.
type UpdaterState struct {
	InstanceUID string `json:"instanceUid"`
	Connected   bool   `json:"connected"`
	Installing  string `json:"installing,omitempty"`
	Installs    int    `json:"installs"`
	Failures    int    `json:"failures"`
	LastError   string `json:"lastError,omitempty"`
}

// State returns a snapshot.
func (u *UpdaterAgent) State() UpdaterState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return UpdaterState{InstanceUID: u.uid.String(), Connected: u.connected, Installing: u.installing,
		Installs: u.installs, Failures: u.failures, LastError: u.lastError}
}

// memPackages is an in-memory types.PackagesStateProvider.
type memPackages struct {
	mu       sync.Mutex
	allHash  []byte
	pkgs     map[string]types.PackageState
	files    map[string][]byte // package -> content hash
	statuses *protobufs.PackageStatuses
	updater  *UpdaterAgent
}

func newMemPackages() *memPackages {
	return &memPackages{pkgs: map[string]types.PackageState{}, files: map[string][]byte{}}
}

func (m *memPackages) AllPackagesHash() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allHash, nil
}
func (m *memPackages) SetAllPackagesHash(h []byte) error {
	m.mu.Lock()
	m.allHash = append([]byte(nil), h...)
	m.mu.Unlock()
	return nil
}
func (m *memPackages) Packages() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n []string
	for k := range m.pkgs {
		n = append(n, k)
	}
	return n, nil
}
func (m *memPackages) PackageState(name string) (types.PackageState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pkgs[name], nil
}
func (m *memPackages) SetPackageState(name string, s types.PackageState) error {
	m.mu.Lock()
	m.pkgs[name] = s
	m.mu.Unlock()
	return nil
}
func (m *memPackages) CreatePackage(name string, typ protobufs.PackageType) error {
	m.mu.Lock()
	m.pkgs[name] = types.PackageState{Exists: true, Type: typ}
	m.mu.Unlock()
	return nil
}
func (m *memPackages) FileContentHash(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files[name], nil
}
func (m *memPackages) DeletePackage(name string) error {
	m.mu.Lock()
	delete(m.pkgs, name)
	delete(m.files, name)
	m.mu.Unlock()
	return nil
}
func (m *memPackages) LastReportedStatuses() (*protobufs.PackageStatuses, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statuses, nil
}
func (m *memPackages) SetLastReportedStatuses(s *protobufs.PackageStatuses) error {
	m.mu.Lock()
	m.statuses = s
	m.mu.Unlock()
	return nil
}

// UpdateContent delegates to the owning updater; the content hash is
// remembered only after success.
func (m *memPackages) UpdateContent(ctx context.Context, name, url string, data io.Reader, contentHash, sig []byte) error {
	if err := m.updater.UpdateContent(ctx, name, url, data, contentHash, sig); err != nil {
		return err
	}
	m.mu.Lock()
	m.files[name] = append([]byte(nil), contentHash...)
	m.mu.Unlock()
	return nil
}
