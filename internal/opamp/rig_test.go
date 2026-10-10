// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/eventbus/pgbus"
	"github.com/myriarch/myriarch/internal/leader"
	"github.com/myriarch/myriarch/internal/opamp"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ------------------------------------------------------------- fakes ---

type fakeAuth struct {
	mu       sync.Mutex
	tokens   map[string]*authn.AgentCredential
	required bool
	err      error
	calls    atomic.Int64
}

func (f *fakeAuth) AuthenticateAgent(r *http.Request) (*authn.AgentCredential, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		if f.required {
			return nil, fmt.Errorf("%w: token required", domain.ErrUnauthorized)
		}
		return &authn.AgentCredential{Labels: map[string]string{}}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.tokens[strings.TrimPrefix(h, "Bearer ")]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("%w: bad token", domain.ErrUnauthorized)
}

// fakeAssigner renders a per-fleet config, stores it and applies the bootstrap rule.
type fakeAssigner struct {
	db      *postgres.DB
	calls   atomic.Int64
	err     error
	fleet   string
	body    func(id domain.AgentID) []byte
	mu      sync.Mutex
	calledF []domain.AgentID
}

func (a *fakeAssigner) AssignAgent(ctx context.Context, id domain.AgentID) (domain.Assignment, error) {
	a.calls.Add(1)
	a.mu.Lock()
	a.calledF = append(a.calledF, id)
	a.mu.Unlock()
	if a.err != nil {
		return domain.Assignment{AgentID: id}, a.err
	}
	fleet := a.fleet
	if fleet == "" {
		fleet = "edge-nl"
	}
	body := []byte("receivers:\n  otlp:\n    protocols:\n      grpc: {}\nexporters:\n  debug: {}\nservice:\n  pipelines:\n    metrics:\n      receivers: [otlp]\n      exporters: [debug]\n# fleet " + fleet + "\n")
	if a.body != nil {
		body = a.body(id)
	}
	hash := domain.ConfigHash(body)
	if err := a.db.Configs().PutRendered(ctx, domain.RenderedConfig{Hash: hash, Body: body, FleetName: fleet, CreatedAt: time.Now()}); err != nil {
		return domain.Assignment{}, err
	}
	if err := a.db.Agents().SetAssignment(ctx, id, store.AgentAssignmentUpdate{FleetName: fleet, AssignedConfigHash: hash, TargetVersion: "0.162.0"}); err != nil {
		return domain.Assignment{}, err
	}
	changed, err := a.db.Agents().SetDesiredConfigIfUnset(ctx, []domain.AgentID{id}, hash)
	if err != nil {
		return domain.Assignment{}, err
	}
	return domain.Assignment{AgentID: id, FleetName: fleet, AssignedConfigHash: hash, DesiredChanged: len(changed) > 0}, nil
}

// ------------------------------------------------------------- rig ---

type rigOpts struct {
	cfg       func(c *config.OpAMPConfig)
	replica   string
	pool      *pgxpool.Pool
	noAssign  bool
	requireTk bool
	noRun     bool
	// realAuth uses the real authn.Service (enrollment token lookup in the database)
	realAuth bool
	// downloads mints the download tokens of package offers
	downloads opamp.DownloadAuthorizer
	// elector replaces the always-leader elector
	elector leader.Elector
	// heartbeat replaces the replica heartbeat interval (200 ms by default)
	heartbeat time.Duration
}

type rig struct {
	t        *testing.T
	pool     *pgxpool.Pool
	db       *postgres.DB
	bus      eventbus.Bus
	srv      *opamp.Server
	ts       *httptest.Server
	auth     *fakeAuth
	assigner *fakeAssigner
	ctl      control.AgentControl
	reg      *prometheus.Registry
	replica  string
	cancel   context.CancelFunc
	done     chan struct{}
	realAuth *authn.Service
}

type staticLeader struct{}

func (staticLeader) Run(ctx context.Context, f func(ctx context.Context)) error {
	<-ctx.Done()
	return nil
}
func (staticLeader) IsLeader() bool { return true }

var _ leader.Elector = staticLeader{}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, reg: prometheus.NewRegistry(), replica: o.replica}
	if r.replica == "" {
		r.replica = "replica-" + uuid.NewString()[:8]
	}
	r.pool = o.pool
	if r.pool == nil {
		r.pool = pgtest.NewPool(t)
	}
	r.db = postgres.New(r.pool, quiet)
	bus, err := pgbus.New(context.Background(), r.pool, r.replica, quiet, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	r.bus = bus
	r.ctl = control.NewBusControl(bus)
	r.auth = &fakeAuth{tokens: map[string]*authn.AgentCredential{}, required: o.requireTk}
	r.assigner = &fakeAssigner{db: r.db}

	cfg := config.Default().OpAMP
	cfg.RequireEnrollmentToken = o.requireTk
	cfg.LastSeenFlushInterval = 100 * time.Millisecond
	cfg.OfflineAfter = 2 * time.Second
	cfg.SendTimeout = 2 * time.Second
	cfg.ConnectRateLimit = 5000
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	var assigner control.Assigner = r.assigner
	if o.noAssign {
		assigner = nil
	}
	var auth opamp.AgentAuthenticator = r.auth
	if o.realAuth {
		svc, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, cfg, authn.Deps{Store: r.db, Logger: quiet, Bus: bus})
		if err != nil {
			t.Fatal(err)
		}
		r.realAuth = svc
		auth = svc
	}
	var elector leader.Elector = staticLeader{}
	if o.elector != nil {
		elector = o.elector
	}
	heartbeat := 200 * time.Millisecond
	if o.heartbeat > 0 {
		heartbeat = o.heartbeat
	}
	srv, err := opamp.New(cfg, opamp.Deps{
		Store: r.db, Bus: bus, Assigner: assigner, Auth: auth, Logger: quiet, Registerer: r.reg, ReplicaID: r.replica,
		Elector: elector, HA: config.HAConfig{HeartbeatInterval: heartbeat}, Hostname: "testhost", Version: "test",
		Audit: audit.NewRecorder(r.db.Audit(), quiet), Downloads: o.downloads,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.srv = srv
	r.ts = httptest.NewUnstartedServer(srv.Handler())
	r.ts.Config.ConnContext = srv.ConnContext // mandatory for the HTTP transport
	r.ts.Start()

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	if o.noRun {
		close(r.done)
	} else {
		go func() { defer close(r.done); _ = srv.Run(ctx) }()
	}
	t.Cleanup(r.stop)
	return r
}

func (r *rig) stop() {
	r.cancel()
	<-r.done
	sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	_ = r.srv.Shutdown(sctx)
	r.ts.CloseClientConnections()
	r.ts.Close()
	_ = r.bus.Close()
}

func (r *rig) wsURL() string { return "ws" + strings.TrimPrefix(r.ts.URL, "http") }

func (r *rig) agent(id domain.AgentID) *domain.Agent {
	r.t.Helper()
	a, err := r.db.Agents().Get(context.Background(), id)
	if err != nil {
		r.t.Fatalf("get agent %s: %v", id, err)
	}
	return a
}

func (r *rig) agentOrNil(id domain.AgentID) *domain.Agent {
	a, err := r.db.Agents().Get(context.Background(), id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

// eventually polls cond until it is true.
func eventually(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *rig) eventTypes(id domain.AgentID) []domain.AgentEventType {
	evs, err := r.db.Agents().ListEvents(context.Background(), id, 200, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []domain.AgentEventType
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i].Type)
	}
	return out
}

func (r *rig) hasEvent(id domain.AgentID, typ domain.AgentEventType) bool {
	for _, e := range r.eventTypes(id) {
		if e == typ {
			return true
		}
	}
	return false
}

func (r *rig) waitEvent(id domain.AgentID, typ domain.AgentEventType) {
	r.t.Helper()
	eventually(r.t, fmt.Sprintf("event %s for %s", typ, id), 5*time.Second, func() bool { return r.hasEvent(id, typ) })
}

func (r *rig) countEvents(id domain.AgentID, typ domain.AgentEventType) int {
	n := 0
	for _, e := range r.eventTypes(id) {
		if e == typ {
			n++
		}
	}
	return n
}

// ---------------------------------------------------- raw OpAMP client ---

type rawAgent struct {
	t    *testing.T
	conn *websocket.Conn
	id   domain.AgentID
	seq  uint64
	caps uint64

	msgs   chan *protobufs.ServerToAgent
	closed chan struct{}
}

const (
	capsCollector = uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth | protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat | protobufs.AgentCapabilities_AgentCapabilities_ReportsAvailableComponents)
	capsUpdater = uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus | protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses | protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth)
	flagFullState  = uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState)
	flagComponents = uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportAvailableComponents)
)

func (r *rig) dialRaw(token string, id domain.AgentID, caps uint64) (*rawAgent, *http.Response, error) {
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(r.wsURL(), hdr)
	if err != nil {
		return nil, resp, err
	}
	a := &rawAgent{t: r.t, conn: conn, id: id, caps: caps, msgs: make(chan *protobufs.ServerToAgent, 256), closed: make(chan struct{})}
	go a.readLoop()
	r.t.Cleanup(func() { _ = conn.Close() })
	return a, resp, nil
}

// readLoop reads server messages until the connection ends. (gorilla/websocket
// connections are unusable after a read deadline timeout, so tests must not
// poll with deadlines.)
func (a *rawAgent) readLoop() {
	defer close(a.closed)
	for {
		_, data, err := a.conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) > 0 && data[0] == 0 {
			data = data[1:]
		}
		var m protobufs.ServerToAgent
		if err := proto.Unmarshal(data, &m); err != nil {
			a.t.Errorf("decode server message: %v", err)
			return
		}
		a.msgs <- &m
	}
}

func (r *rig) connect(token string, id domain.AgentID, caps uint64) *rawAgent {
	r.t.Helper()
	a, resp, err := r.dialRaw(token, id, caps)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		r.t.Fatalf("dial: %v (status %d)", err, code)
	}
	return a
}

func (a *rawAgent) sendRaw(msg *protobufs.AgentToServer) {
	a.t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		a.t.Fatal(err)
	}
	hdr := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(hdr, 0)
	if err := a.conn.WriteMessage(websocket.BinaryMessage, append(hdr[:n], raw...)); err != nil {
		a.t.Fatalf("send: %v", err)
	}
}

// send fills instance uid, sequence number and capabilities and sends msg.
func (a *rawAgent) send(msg *protobufs.AgentToServer) {
	a.t.Helper()
	if msg.InstanceUid == nil {
		msg.InstanceUid = a.id[:]
	}
	msg.SequenceNum = a.seq
	a.seq++
	if msg.Capabilities == 0 && a.caps != 0 && (msg.AgentDescription != nil || a.seq == 1) {
		msg.Capabilities = a.caps
	}
	a.sendRaw(msg)
}

func (a *rawAgent) recv(d time.Duration) (*protobufs.ServerToAgent, bool) {
	a.t.Helper()
	select {
	case m := <-a.msgs:
		return m, true
	case <-time.After(d):
		return nil, false
	}
}

func (a *rawAgent) mustRecv() *protobufs.ServerToAgent {
	a.t.Helper()
	m, ok := a.recv(5 * time.Second)
	if !ok {
		a.t.Fatal("expected a message from the server")
	}
	return m
}

func (a *rawAgent) expectSilence(d time.Duration) {
	a.t.Helper()
	if m, ok := a.recv(d); ok {
		a.t.Fatalf("unexpected server message: %v", m)
	}
}

// expectClosed waits until the server closed the connection.
func (a *rawAgent) expectClosed(d time.Duration) {
	a.t.Helper()
	select {
	case <-a.closed:
	case <-time.After(d):
		a.t.Fatal("connection was not closed by the server")
	}
}

func (a *rawAgent) close() { _ = a.conn.Close() }

func kv(k, v string) *protobufs.KeyValue {
	return &protobufs.KeyValue{Key: k, Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}}}
}

type descOpts struct {
	host, version, os, siteType, siteID string
	service                             string
	extra                               []*protobufs.KeyValue
}

func description(id domain.AgentID, o descOpts) *protobufs.AgentDescription {
	if o.service == "" {
		o.service = "otelcol-contrib"
	}
	if o.os == "" {
		o.os = "linux"
	}
	non := []*protobufs.KeyValue{kv("os.type", o.os), kv("host.name", o.host), kv("host.arch", "amd64"), kv("os.description", "test os")}
	if o.siteType != "" {
		non = append(non, kv("site.type", o.siteType))
	}
	if o.siteID != "" {
		non = append(non, kv("site.id", o.siteID))
	}
	non = append(non, o.extra...)
	return &protobufs.AgentDescription{
		IdentifyingAttributes:    []*protobufs.KeyValue{kv("service.name", o.service), kv("service.version", o.version), kv("service.instance.id", id.String())},
		NonIdentifyingAttributes: non,
	}
}

func healthy() *protobufs.ComponentHealth {
	return &protobufs.ComponentHealth{Healthy: true, Status: "StatusOK", StartTimeUnixNano: uint64(time.Now().Add(-time.Minute).UnixNano()),
		ComponentHealthMap: map[string]*protobufs.ComponentHealth{"pipeline:metrics": {Healthy: true, Status: "StatusOK",
			ComponentHealthMap: map[string]*protobufs.ComponentHealth{"receiver:otlp": {Healthy: true, Status: "StatusOK"}}}}}
}

// firstMessage is what a supervisor sends on its very first connection.
func firstMessage(id domain.AgentID, o descOpts) *protobufs.AgentToServer {
	return &protobufs.AgentToServer{
		InstanceUid: id[:], AgentDescription: description(id, o), Health: healthy(),
		EffectiveConfig:     &protobufs.EffectiveConfig{ConfigMap: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{"": {Body: []byte("supervisor: config\n")}}}},
		AvailableComponents: &protobufs.AvailableComponents{Hash: []byte("components-hash-v1")},
		RemoteConfigStatus:  &protobufs.RemoteConfigStatus{Status: protobufs.RemoteConfigStatuses_RemoteConfigStatuses_UNSET},
	}
}

func newAgentID() domain.AgentID { return domain.AgentID(uuid.Must(uuid.NewV7())) }

func statusMsg(hash []byte, st protobufs.RemoteConfigStatuses, errMsg string) *protobufs.AgentToServer {
	return &protobufs.AgentToServer{RemoteConfigStatus: &protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: st, ErrorMessage: errMsg}}
}

func remoteBody(t *testing.T, m *protobufs.ServerToAgent) (hash []byte, body string) {
	t.Helper()
	if m.RemoteConfig == nil {
		t.Fatalf("expected remote_config in %v", m)
	}
	cm := m.RemoteConfig.GetConfig().GetConfigMap()
	if len(cm) != 1 {
		t.Fatalf("exactly one config_map entry expected, got %d", len(cm))
	}
	obj, ok := cm[domain.RemoteConfigKey]
	if !ok {
		t.Fatalf("config_map key must be %q, got %v", domain.RemoteConfigKey, cm)
	}
	if obj.ContentType != domain.RemoteConfigContentType || len(obj.Body) == 0 {
		t.Fatalf("content type %q, body %d bytes", obj.ContentType, len(obj.Body))
	}
	return m.RemoteConfig.ConfigHash, string(obj.Body)
}
