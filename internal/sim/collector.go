// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// collectorCaps are the capabilities of the opampsupervisor with the
// recommended config (docs/design/opamp-server.md, Collector installation on
// hosts > Supervisor configuration; docs/design/upstream-notes.md, Supervisor
// limits > Identity and reported attributes: 31751).
const collectorCaps = protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
	protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
	protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsAvailableComponents |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat

// bootstrapConfig is what a fresh supervisor runs before the first remote
// config (noop pipeline).
const bootstrapConfig = "receivers:\n  nop:\nexporters:\n  nop:\nservice:\n  pipelines:\n    traces:\n      receivers: [nop]\n      exporters: [nop]\n"

// CollectorAgent simulates supervisor + collector of one host.
type CollectorAgent struct {
	sim  *Simulator
	spec AgentSpec

	// lifecycle serializes Start against Stop, so that Stop never stops a client whose Start is still running (opamp-go
	// does not allow Start and Stop of one client to overlap). It is taken before mu.
	lifecycle   sync.Mutex
	statusMu    sync.Mutex // serializes reportRemoteStatus; taken before mu
	mu          sync.Mutex
	uid         uuid.UUID
	version     string
	startedAt   time.Time
	cli         client.OpAMPClient
	running     bool // client is supposed to be connected
	connected   bool
	epoch       int // incremented on every stop; invalidates timers
	healthy     bool
	healthState string
	healthErr   string

	effective   []byte // effective config body
	appliedHash []byte // last working remote config hash
	lastStatus  *protobufs.RemoteConfigStatus
	applyGen    int

	restarting       bool
	bootOnStart      bool // the next Start is a process start: report the boot sequence
	pendingVersion   string
	pendingCallbacks []func()

	updater *UpdaterAgent
	rng     *rand.Rand

	// per-agent counters (for the control API)
	configsApplied, configsFailed, restarts int
	lastConfigError                         string
}

func newCollectorAgent(s *Simulator, spec AgentSpec, idx int) *CollectorAgent {
	h := fnvHash(spec.CollectorUID.String())
	return &CollectorAgent{
		sim: s, spec: spec, uid: spec.CollectorUID, version: spec.Version,
		healthy: true, healthState: "StatusOK",
		effective: []byte(bootstrapConfig),
		rng:       rand.New(rand.NewPCG(uint64(s.seed), h^uint64(idx))),
	}
}

func fnvHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func (a *CollectorAgent) rand() float64 { return a.rng.Float64() } // caller holds a.mu

// description builds the AgentDescription (caller holds a.mu).
func (a *CollectorAgent) description() *protobufs.AgentDescription {
	non := []*protobufs.KeyValue{
		strAttr("os.type", a.spec.OS),
		strAttr("host.arch", a.spec.Arch),
		strAttr("host.name", a.spec.Hostname),
		strAttr("os.description", a.spec.OSDescription),
	}
	keys := make([]string, 0, len(a.spec.Attributes))
	for k := range a.spec.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		non = append(non, strAttr(k, a.spec.Attributes[k]))
	}
	non = append(non, strAttr("site.id", a.spec.SiteID))
	return &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			strAttr("service.name", "otelcol-contrib"),
			strAttr("service.version", a.version),
			strAttr("service.instance.id", a.uid.String()),
		},
		NonIdentifyingAttributes: non,
	}
}

// healthMsg builds the health message (caller holds a.mu).
func (a *CollectorAgent) healthMsg() *protobufs.ComponentHealth {
	return &protobufs.ComponentHealth{
		Healthy:            a.healthy,
		StartTimeUnixNano:  uint64(a.startedAt.UnixNano()),
		LastError:          a.healthErr,
		Status:             a.healthState,
		StatusTimeUnixNano: uint64(time.Now().UnixNano()),
	}
}

// Start connects the agent (idempotent).
func (a *CollectorAgent) Start(ctx context.Context) error {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return nil
	}
	a.running = true
	a.epoch++
	a.startedAt = time.Now()
	// An apply that was in flight when the process went away never finishes (finishApply is bound to the epoch), so
	// the new process must not replay APPLYING: it reports the config it actually runs, and the server offers the
	// desired one again.
	if a.lastStatus != nil && a.lastStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING {
		if len(a.appliedHash) > 0 {
			a.lastStatus = &protobufs.RemoteConfigStatus{LastRemoteConfigHash: a.appliedHash, Status: protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED}
		} else {
			a.lastStatus = nil
		}
	}
	boot := a.bootOnStart
	a.bootOnStart = false
	switch {
	case boot:
		a.healthy, a.healthState, a.healthErr = false, "", ""
	case !a.restarting:
		a.healthy, a.healthState, a.healthErr = true, "StatusOK", ""
	}
	epoch := a.epoch
	c := client.NewWebSocket(a.sim.clientLogger("collector", a.spec.Hostname))
	caps := collectorCaps
	if err := c.SetCapabilities(&caps); err != nil {
		a.running = false
		a.mu.Unlock()
		return err
	}
	if err := c.SetAgentDescription(a.description()); err != nil {
		a.running = false
		a.mu.Unlock()
		return err
	}
	if err := c.SetHealth(a.healthMsg()); err != nil {
		a.running = false
		a.mu.Unlock()
		return err
	}
	if err := c.SetAvailableComponents(a.sim.comps.forVersion(a.version)); err != nil {
		a.running = false
		a.mu.Unlock()
		return err
	}
	var uid types.InstanceUid
	copy(uid[:], a.uid[:])
	settings := types.StartSettings{
		OpAMPServerURL:     a.sim.opts.Server,
		Header:             a.sim.authHeader(),
		TLSConfig:          a.sim.tlsFor(),
		InstanceUid:        uid,
		HeartbeatInterval:  a.sim.heartbeat(),
		RemoteConfigStatus: a.lastStatus,
		Callbacks: types.Callbacks{
			OnConnect:       func(context.Context) { a.onConnect() },
			OnConnectFailed: func(_ context.Context, err error) { a.onConnectFailed(err) },
			OnError: func(_ context.Context, e *protobufs.ServerErrorResponse) {
				a.sim.log.Debug("server error response", "host", a.spec.Hostname, "type", e.GetType().String(), "msg", e.GetErrorMessage())
			},
			OnMessage:          a.onMessage,
			OnCommand:          a.onCommand,
			GetEffectiveConfig: a.getEffectiveConfig,
		},
	}
	a.cli = c
	a.mu.Unlock()

	if err := c.Start(ctx, settings); err != nil {
		a.mu.Lock()
		a.running = false
		a.cli = nil
		a.mu.Unlock()
		return fmt.Errorf("start %s: %w", a.spec.Hostname, err)
	}
	if boot {
		a.bootSequence(epoch)
	}
	return nil
}

// Stop disconnects the agent (sends AgentDisconnect) and invalidates timers.
func (a *CollectorAgent) Stop(ctx context.Context) {
	// Wait for a Start in progress: the client it publishes is stopped only after its Start returned. The lock is
	// released before the callbacks run and the client stops; a later Start creates a new client.
	a.lifecycle.Lock()
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		a.lifecycle.Unlock()
		return
	}
	a.running = false
	a.epoch++
	cli := a.cli
	a.cli = nil
	was := a.connected
	a.connected = false
	cbs := a.pendingCallbacks
	a.pendingCallbacks = nil
	a.restarting = false
	a.mu.Unlock()
	a.lifecycle.Unlock()
	if was {
		a.sim.connectedCollectors.Add(-1)
		a.sim.metrics.agentsConnected.Dec()
	}
	for _, cb := range cbs {
		cb()
	}
	if cli != nil {
		_ = cli.Stop(ctx)
	}
}

func (a *CollectorAgent) onConnect() {
	a.mu.Lock()
	was := a.connected
	a.connected = a.running
	a.mu.Unlock()
	if !was {
		a.sim.connectedCollectors.Add(1)
		a.sim.metrics.agentsConnected.Inc()
	}
}

func (a *CollectorAgent) onConnectFailed(err error) {
	a.mu.Lock()
	was := a.connected
	a.connected = false
	a.mu.Unlock()
	if was {
		a.sim.connectedCollectors.Add(-1)
		a.sim.metrics.agentsConnected.Dec()
	}
	a.sim.metrics.connectFailures.Inc()
	a.sim.connectErrors.Add(1)
	a.sim.log.Debug("connect failed", "host", a.spec.Hostname, "err", err)
}

func (a *CollectorAgent) getEffectiveConfig(context.Context) (*protobufs.EffectiveConfig, error) {
	a.mu.Lock()
	body := a.effective
	a.mu.Unlock()
	return &protobufs.EffectiveConfig{ConfigMap: &protobufs.AgentConfigMap{
		ConfigMap: map[string]*protobufs.AgentConfigObject{"": {Body: body, ContentType: "text/yaml"}},
	}}, nil
}

func (a *CollectorAgent) onMessage(_ context.Context, msg *types.MessageData) {
	if id := msg.AgentIdentification; id != nil && len(id.NewInstanceUid) == 16 {
		nu, err := uuid.FromBytes(id.NewInstanceUid)
		if err == nil {
			a.mu.Lock()
			changed := nu != a.uid
			a.uid = nu
			a.mu.Unlock()
			if changed && a.updater != nil {
				a.updater.collectorUIDChanged(nu)
			}
		}
	}
	if rc := msg.RemoteConfig; rc != nil {
		a.applyRemoteConfig(rc)
	}
}

// reportRemoteStatus records st as the current remote config status and hands it to the OpAMP client, unless the
// apply identified by (epoch, gen) has been superseded: by a newer config (applyGen) or by a stop of the process
// (epoch). Every report of an apply goes through here, and statusMu keeps the check, the stored status and the
// hand-over to the client in one order. Without that, the tail of an apply that has just finished (its APPLIED report
// runs after the state was updated, outside of mu) could overwrite the APPLYING report of the next config, and the
// server would see APPLIED for the old hash while the new one is being applied.
func (a *CollectorAgent) reportRemoteStatus(epoch, gen int, st protobufs.RemoteConfigStatuses, hash []byte, errMsg string) {
	s := &protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: st, ErrorMessage: errMsg}
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	a.mu.Lock()
	if a.epoch != epoch || a.applyGen != gen {
		a.mu.Unlock()
		return
	}
	a.lastStatus = s
	cli := a.cli
	a.mu.Unlock()
	if cli != nil {
		_ = cli.SetRemoteConfigStatus(s)
	}
}

// configBody flattens the offered config map into one text (sorted by key,
// "" last - the supervisor merge order).
func configBody(cfg *protobufs.AgentConfigMap) []byte {
	if cfg == nil || len(cfg.ConfigMap) == 0 {
		return nil
	}
	keys := make([]string, 0, len(cfg.ConfigMap))
	for k := range cfg.ConfigMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == "") != (keys[j] == "") {
			return keys[j] == ""
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 1 {
		return cfg.ConfigMap[keys[0]].GetBody()
	}
	var buf bytes.Buffer
	for i, k := range keys {
		if i > 0 {
			buf.WriteString("\n---\n")
		}
		buf.Write(cfg.ConfigMap[k].GetBody())
	}
	return buf.Bytes()
}

// applyRemoteConfig emulates the supervisor with automatic_config_rollback:
// unchanged result -> immediate APPLIED; otherwise APPLYING and after the
// apply delay APPLIED or FAILED. A failed config is followed by APPLIED for
// the previous working hash (docs/design/upstream-notes.md, Supervisor limits >
// Remote configuration handling).
func (a *CollectorAgent) applyRemoteConfig(rc *protobufs.AgentRemoteConfig) {
	hash := append([]byte(nil), rc.ConfigHash...)
	body := configBody(rc.Config)

	a.mu.Lock()
	if bytes.Equal(hash, a.appliedHash) && a.lastStatus != nil && a.lastStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED &&
		bytes.Equal(a.lastStatus.LastRemoteConfigHash, hash) {
		a.mu.Unlock()
		return // nothing to do
	}
	a.applyGen++
	gen, epoch := a.applyGen, a.epoch
	b := a.sim.behavior
	delay := b.ConfigApplyDelay.Pick(a.rand())
	var failMsg string
	switch {
	case b.FailConfigsContaining != "" && bytes.Contains(body, []byte(b.FailConfigsContaining)):
		failMsg = fmt.Sprintf("invalid configuration: simulated failure (config contains %q)", b.FailConfigsContaining)
	case a.rand() < b.ConfigFailureRate:
		failMsg = "Config apply timeout exceeded (simulated random failure)"
	}
	unhealthy := failMsg == "" && a.rand() < b.UnhealthyAfterApplyRate
	a.mu.Unlock()

	a.reportRemoteStatus(epoch, gen, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, hash, "")
	time.AfterFunc(delay, func() { a.finishApply(epoch, gen, hash, body, failMsg, unhealthy) })
}

func (a *CollectorAgent) finishApply(epoch, gen int, hash, body []byte, failMsg string, unhealthy bool) {
	a.mu.Lock()
	if a.epoch != epoch || a.applyGen != gen || !a.running {
		a.mu.Unlock() // superseded by a newer config, or the agent went offline
		return
	}
	prev := a.appliedHash
	if failMsg != "" {
		a.configsFailed++
		a.lastConfigError = failMsg
		a.mu.Unlock()
		a.sim.metrics.configsFailed.Inc()
		a.sim.configsFailed.Add(1)
		a.reportRemoteStatus(epoch, gen, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, hash, failMsg)
		if len(prev) > 0 && !a.sim.behavior.ValidateConfig {
			// automatic rollback: the previous config is running again
			time.AfterFunc(200*time.Millisecond, func() {
				a.reportRemoteStatus(epoch, gen, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, prev, "")
			})
		}
		return
	}
	a.effective = body
	if len(body) == 0 {
		a.effective = []byte(bootstrapConfig) // empty map -> noop pipeline (upstream-notes.md, Remote configuration handling)
	}
	a.appliedHash = hash
	a.configsApplied++
	a.lastConfigError = ""
	if unhealthy {
		a.healthy, a.healthState, a.healthErr = false, "StatusRecoverableError", "simulated: collector unhealthy after config apply"
	} else if !a.healthy && !a.restarting {
		a.healthy, a.healthState, a.healthErr = true, "StatusOK", ""
	}
	h := a.healthMsg()
	cli := a.cli
	a.mu.Unlock()
	a.sim.metrics.configsApplied.Inc()
	a.sim.configsApplied.Add(1)
	if cli != nil {
		_ = cli.UpdateEffectiveConfig(context.Background())
	}
	a.reportRemoteStatus(epoch, gen, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, hash, "")
	if cli != nil {
		_ = cli.SetHealth(h)
	}
}

func (a *CollectorAgent) setHealth(healthy bool, state, lastErr string) {
	a.mu.Lock()
	a.healthy, a.healthState, a.healthErr = healthy, state, lastErr
	h := a.healthMsg()
	cli := a.cli
	a.mu.Unlock()
	if cli != nil {
		_ = cli.SetHealth(h)
	}
}

// onCommand handles the restart command.
func (a *CollectorAgent) onCommand(_ context.Context, cmd *protobufs.ServerToAgentCommand) error {
	if cmd.GetType() == protobufs.CommandType_CommandType_Restart {
		a.restart("", nil)
	}
	return nil
}

// restart emulates a collector restart: health "stopping" for
// restartDuration, then a (possibly new-version) description and healthy
// state with a new start time. cb (optional) is called when the restart is
// complete.
func (a *CollectorAgent) restart(newVersion string, cb func()) {
	a.mu.Lock()
	if !a.running {
		// offline: the change takes effect when the agent comes back
		if newVersion != "" {
			a.version = newVersion
		}
		a.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}
	if a.restarting {
		if newVersion != "" {
			a.pendingVersion = newVersion
		}
		if cb != nil {
			a.pendingCallbacks = append(a.pendingCallbacks, cb)
		}
		a.mu.Unlock()
		return
	}
	a.restarting = true
	if cb != nil {
		a.pendingCallbacks = append(a.pendingCallbacks, cb)
	}
	if newVersion != "" {
		a.pendingVersion = newVersion
	}
	epoch := a.epoch
	d := a.sim.behavior.RestartDuration.Pick(a.rand())
	a.mu.Unlock()
	a.sim.metrics.restarts.Inc()
	a.setHealth(false, "StatusStopping", "")
	time.AfterFunc(d, func() { a.finishRestart(epoch) })
}

func (a *CollectorAgent) finishRestart(epoch int) {
	a.mu.Lock()
	if a.epoch != epoch || !a.running {
		a.mu.Unlock()
		return // Stop() already ran the callbacks
	}
	a.restarting = false
	versionChanged := false
	if a.pendingVersion != "" && a.pendingVersion != a.version {
		a.version = a.pendingVersion
		versionChanged = true
	}
	a.pendingVersion = ""
	a.startedAt = time.Now()
	a.restarts++
	if versionChanged && a.sim.behavior.ReconnectOnUpgrade {
		cbs := a.pendingCallbacks
		a.pendingCallbacks = nil
		a.mu.Unlock()
		go a.reconnectAfterUpgrade(cbs)
		return
	}
	// The first message of the new process: new description, {healthy:false}
	// without a status; StatusStarting and StatusOK follow (bootSequence).
	a.healthy, a.healthState, a.healthErr = false, "", ""
	desc := a.description()
	h := a.healthMsg()
	version := a.version
	cli := a.cli
	cbs := a.pendingCallbacks
	a.pendingCallbacks = nil
	a.mu.Unlock()
	if cli != nil {
		_ = cli.SetAgentDescription(desc)
		if versionChanged {
			_ = cli.SetAvailableComponents(a.sim.comps.forVersion(version))
		}
		_ = cli.SetHealth(h)
	}
	a.bootSequence(epoch)
	for _, cb := range cbs {
		cb()
	}
}

// bootSequence reports what a starting collector reports after its first
// message: StatusStarting, then StatusOK after StartupDuration.
func (a *CollectorAgent) bootSequence(epoch int) {
	a.mu.Lock()
	startup := a.sim.behavior.StartupDuration.Pick(a.rand())
	a.mu.Unlock()
	still := func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.epoch == epoch && a.running && !a.restarting
	}
	time.AfterFunc(startup/4, func() {
		if still() {
			a.setHealth(false, "StatusStarting", "")
		}
	})
	time.AfterFunc(startup, func() {
		if still() {
			a.setHealth(true, "StatusOK", "")
		}
	})
}

// reconnectAfterUpgrade emulates an upgrade that restarts the supervisor (or
// pod): the connection drops and a fresh client connects with the new version
// and {healthy:false}, followed by the boot sequence.
func (a *CollectorAgent) reconnectAfterUpgrade(cbs []func()) {
	ctx := context.Background()
	a.Stop(ctx)
	a.mu.Lock()
	a.bootOnStart = true
	a.mu.Unlock()
	if err := a.Start(ctx); err != nil {
		a.sim.log.Warn("reconnect after upgrade failed", "host", a.spec.Hostname, "err", err)
	}
	for _, cb := range cbs {
		cb()
	}
}

// upgrade restarts the collector with a new version; the returned channel is
// closed when the new version has been reported.
func (a *CollectorAgent) upgrade(version string) <-chan struct{} {
	done := make(chan struct{})
	a.restart(version, func() { close(done) })
	return done
}

// goOffline disconnects the agent for d (churn), then reconnects.
func (a *CollectorAgent) goOffline(ctx context.Context, d time.Duration) {
	a.Stop(ctx)
	if a.updater != nil {
		a.updater.Stop(ctx)
	}
	time.AfterFunc(d, func() {
		if ctx.Err() != nil {
			return
		}
		if err := a.Start(ctx); err != nil {
			a.sim.log.Warn("reconnect after churn failed", "host", a.spec.Hostname, "err", err)
		}
		if a.updater != nil {
			if err := a.updater.Start(ctx); err != nil {
				a.sim.log.Warn("updater reconnect after churn failed", "host", a.spec.Hostname, "err", err)
			}
		}
	})
}

// State is the debug view of a simulated agent (GET /control/agents).
type AgentState struct {
	Hostname        string        `json:"hostname"`
	Group           string        `json:"group"`
	SiteID          string        `json:"siteId"`
	InstanceUID     string        `json:"instanceUid"`
	Version         string        `json:"version"`
	Connected       bool          `json:"connected"`
	Healthy         bool          `json:"healthy"`
	HealthStatus    string        `json:"healthStatus"`
	Restarting      bool          `json:"restarting"`
	StartedAt       string        `json:"startedAt"`
	RemoteConfig    string        `json:"remoteConfigHash,omitempty"`
	RemoteStatus    string        `json:"remoteConfigStatus,omitempty"`
	StatusHash      string        `json:"remoteConfigStatusHash,omitempty"`
	EffectiveBytes  int           `json:"effectiveConfigBytes"`
	ConfigsApplied  int           `json:"configsApplied"`
	ConfigsFailed   int           `json:"configsFailed"`
	LastConfigError string        `json:"lastConfigError,omitempty"`
	Restarts        int           `json:"restarts"`
	Updater         *UpdaterState `json:"updater,omitempty"`
}

// State returns a snapshot.
func (a *CollectorAgent) State() AgentState {
	a.mu.Lock()
	st := AgentState{
		Hostname: a.spec.Hostname, Group: a.spec.Group, SiteID: a.spec.SiteID,
		InstanceUID: a.uid.String(), Version: a.version, Connected: a.connected,
		Healthy: a.healthy, HealthStatus: a.healthState, Restarting: a.restarting,
		StartedAt:      a.startedAt.UTC().Format(time.RFC3339),
		RemoteConfig:   hex.EncodeToString(a.appliedHash),
		EffectiveBytes: len(a.effective),
		ConfigsApplied: a.configsApplied, ConfigsFailed: a.configsFailed,
		LastConfigError: a.lastConfigError, Restarts: a.restarts,
	}
	if a.lastStatus != nil {
		st.RemoteStatus = a.lastStatus.Status.String()
		st.StatusHash = hex.EncodeToString(a.lastStatus.LastRemoteConfigHash)
	}
	u := a.updater
	a.mu.Unlock()
	if u != nil {
		us := u.State()
		st.Updater = &us
	}
	return st
}

// Hostname returns the simulated host name.
func (a *CollectorAgent) Hostname() string { return a.spec.Hostname }

// Version returns the currently reported collector version.
func (a *CollectorAgent) Version() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.version
}
