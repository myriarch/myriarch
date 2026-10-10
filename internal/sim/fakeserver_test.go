// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	stypes "github.com/open-telemetry/opamp-go/server/types"
	"github.com/stretchr/testify/require"
)

// fakeAgent is the server-side view of one connected agent.
type fakeAgent struct {
	uid         uuid.UUID
	conn        stypes.Connection
	caps        uint64
	ident       map[string]string
	nonIdent    map[string]string
	descCount   int
	health      *protobufs.ComponentHealth
	healthLog   []string
	updateLog   []string // phases of the dev.myriarch.update health component, in order of change
	rcStatus    *protobufs.RemoteConfigStatus
	rcLog       []protobufs.RemoteConfigStatuses
	rcHashLog   [][]byte
	effective   []byte
	components  *protobufs.AvailableComponents
	pkgStatus   map[string]*protobufs.PackageStatus
	pkgLog      []protobufs.PackageStatusEnum
	messages    int
	auth        string
	disconnects int
}

// fakeMyriarchd is a minimal in-process OpAMP server (the role of myriarchd).
type fakeMyriarchd struct {
	t   *testing.T
	srv server.OpAMPServer

	mu       sync.Mutex
	agents   map[uuid.UUID]*fakeAgent
	byConn   map[stypes.Connection]uuid.UUID
	desired  map[string]*protobufs.AgentRemoteConfig // keyed by service.name
	offer    map[string]*protobufs.PackagesAvailable // keyed by service.name
	offered  map[uuid.UUID]string                    // last offered config hash (hex-ish key)
	connects int
}

func newFakeMyriarchd(t testing.TB) *fakeMyriarchd {
	f := &fakeMyriarchd{agents: map[uuid.UUID]*fakeAgent{}, byConn: map[stypes.Connection]uuid.UUID{},
		desired: map[string]*protobufs.AgentRemoteConfig{}, offer: map[string]*protobufs.PackagesAvailable{},
		offered: map[uuid.UUID]string{}}
	if tt, ok := t.(*testing.T); ok {
		f.t = tt
	}
	f.srv = server.New(nil)
	err := f.srv.Start(server.StartSettings{
		ListenEndpoint: "127.0.0.1:0",
		Settings: server.Settings{Callbacks: stypes.Callbacks{OnConnecting: func(r *http.Request) stypes.ConnectionResponse {
			auth := r.Header.Get("Authorization")
			return stypes.ConnectionResponse{Accept: true, ConnectionCallbacks: stypes.ConnectionCallbacks{
				OnConnected: func(ctx context.Context, c stypes.Connection) {
					f.mu.Lock()
					f.connects++
					f.mu.Unlock()
				},
				OnMessage: func(ctx context.Context, c stypes.Connection, m *protobufs.AgentToServer) *protobufs.ServerToAgent {
					return f.onMessage(c, m, auth)
				},
				OnConnectionClose: func(c stypes.Connection) {
					f.mu.Lock()
					if id, ok := f.byConn[c]; ok {
						if a := f.agents[id]; a != nil && a.conn == c {
							a.conn = nil
							a.disconnects++
						}
						delete(f.byConn, c)
					}
					f.mu.Unlock()
				},
			}}
		}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.srv.Stop(context.Background()) })
	return f
}

func (f *fakeMyriarchd) url() string { return "ws://" + f.srv.Addr().String() + "/v1/opamp" }

func attrs(kvs []*protobufs.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.GetStringValue()
	}
	return m
}

func (f *fakeMyriarchd) onMessage(c stypes.Connection, m *protobufs.AgentToServer, auth string) *protobufs.ServerToAgent {
	id, err := uuid.FromBytes(m.InstanceUid)
	if err != nil {
		return &protobufs.ServerToAgent{ErrorResponse: &protobufs.ServerErrorResponse{Type: protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest}}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	if a == nil {
		a = &fakeAgent{uid: id, pkgStatus: map[string]*protobufs.PackageStatus{}}
		f.agents[id] = a
	}
	a.conn = c
	a.auth = auth
	f.byConn[c] = id
	a.messages++
	resp := &protobufs.ServerToAgent{InstanceUid: m.InstanceUid}
	if m.Capabilities != 0 {
		a.caps = m.Capabilities
		resp.Capabilities = uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus |
			protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig |
			protobufs.ServerCapabilities_ServerCapabilities_AcceptsEffectiveConfig |
			protobufs.ServerCapabilities_ServerCapabilities_OffersPackages |
			protobufs.ServerCapabilities_ServerCapabilities_AcceptsPackagesStatus)
	}
	if d := m.AgentDescription; d != nil {
		a.ident, a.nonIdent = attrs(d.IdentifyingAttributes), attrs(d.NonIdentifyingAttributes)
		a.descCount++
	}
	if h := m.Health; h != nil {
		a.health = h
		a.healthLog = append(a.healthLog, h.Status)
		if c := h.ComponentHealthMap[updateComponent]; c != nil && (len(a.updateLog) == 0 || a.updateLog[len(a.updateLog)-1] != c.Status) {
			a.updateLog = append(a.updateLog, c.Status)
		}
	}
	if rs := m.RemoteConfigStatus; rs != nil {
		a.rcStatus = rs
		a.rcLog = append(a.rcLog, rs.Status)
		a.rcHashLog = append(a.rcHashLog, rs.LastRemoteConfigHash)
	}
	if ec := m.EffectiveConfig; ec != nil {
		if o, ok := ec.ConfigMap.GetConfigMap()[""]; ok {
			a.effective = o.Body
		}
	}
	if ac := m.AvailableComponents; ac != nil {
		a.components = ac
	}
	if ps := m.PackageStatuses; ps != nil {
		for n, s := range ps.Packages {
			a.pkgStatus[n] = s
			a.pkgLog = append(a.pkgLog, s.Status)
		}
	}
	// offer desired config / packages like myriarchd does on every message
	if name := a.ident["service.name"]; name != "" {
		if rc := f.desired[name]; rc != nil {
			key := string(rc.ConfigHash)
			reported := a.rcStatus != nil && bytes.Equal(a.rcStatus.LastRemoteConfigHash, rc.ConfigHash) &&
				(a.rcStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED ||
					a.rcStatus.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED)
			if !reported && f.offered[id] != key {
				resp.RemoteConfig = rc
				f.offered[id] = key
			}
		}
		if po := f.offer[name]; po != nil {
			if f.offered[id] != string(po.AllPackagesHash) && a.ident["service.name"] == "myriarch-updater" {
				resp.PackagesAvailable = po
				f.offered[id] = string(po.AllPackagesHash)
			}
		}
	}
	return resp
}

func (f *fakeMyriarchd) count(pred func(*fakeAgent) bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.agents {
		if pred(a) {
			n++
		}
	}
	return n
}

func (f *fakeMyriarchd) connected(serviceName string) int {
	return f.count(func(a *fakeAgent) bool { return a.conn != nil && a.ident["service.name"] == serviceName })
}

// get returns a snapshot of the agent taken under the lock (nil if unknown).
// The live fakeAgent is mutated by the server-side message handler (which runs
// on net/http connection goroutines); tests must never read it unlocked - use
// get, count or with.
func (f *fakeMyriarchd) get(id uuid.UUID) *fakeAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	if a == nil {
		return nil
	}
	return a.snapshot()
}

// snapshot copies the agent's mutable containers. Protobuf messages and the
// attribute maps are replaced, never mutated in place, so sharing them is safe.
func (a *fakeAgent) snapshot() *fakeAgent {
	c := *a
	c.healthLog = append([]string(nil), a.healthLog...)
	c.rcLog = append([]protobufs.RemoteConfigStatuses(nil), a.rcLog...)
	c.rcHashLog = append([][]byte(nil), a.rcHashLog...)
	c.pkgLog = append([]protobufs.PackageStatusEnum(nil), a.pkgLog...)
	c.pkgStatus = make(map[string]*protobufs.PackageStatus, len(a.pkgStatus))
	for k, v := range a.pkgStatus {
		c.pkgStatus[k] = v
	}
	return &c
}

func (f *fakeMyriarchd) with(id uuid.UUID, fn func(a *fakeAgent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.agents[id]; a != nil {
		fn(a)
	}
}

// pushConfig sets the desired config for all collectors and pushes it to the
// connected ones.
func (f *fakeMyriarchd) pushConfig(body string, hash []byte) {
	rc := &protobufs.AgentRemoteConfig{
		Config:     &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{"myriarch.yaml": {Body: []byte(body), ContentType: "text/yaml"}}},
		ConfigHash: hash,
	}
	f.mu.Lock()
	f.desired["otelcol-contrib"] = rc
	var conns []stypes.Connection
	var uids [][]byte
	for id, a := range f.agents {
		if a.conn != nil && a.ident["service.name"] == "otelcol-contrib" {
			conns = append(conns, a.conn)
			uids = append(uids, id[:])
			f.offered[id] = string(hash)
		}
	}
	f.mu.Unlock()
	f.pushAll(conns, uids, func(uid []byte) *protobufs.ServerToAgent {
		return &protobufs.ServerToAgent{InstanceUid: uid, RemoteConfig: rc}
	})
}

func (f *fakeMyriarchd) pushAll(conns []stypes.Connection, uids [][]byte, mk func(uid []byte) *protobufs.ServerToAgent) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i := range conns {
		wg.Add(1)
		sem <- struct{}{}
		go func(c stypes.Connection, uid []byte) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.Send(ctx, mk(uid))
		}(conns[i], uids[i])
	}
	wg.Wait()
}

// sendTo sends a message to one agent.
func (f *fakeMyriarchd) sendTo(id uuid.UUID, msg *protobufs.ServerToAgent) {
	f.mu.Lock()
	a := f.agents[id]
	var c stypes.Connection
	if a != nil {
		c = a.conn
	}
	f.mu.Unlock()
	require.NotNil(f.t, c, "agent %s not connected", id)
	msg.InstanceUid = id[:]
	require.NoError(f.t, c.Send(context.Background(), msg))
}

func (f *fakeMyriarchd) offerPackage(version string, hash []byte) *protobufs.PackagesAvailable {
	po := &protobufs.PackagesAvailable{
		Packages: map[string]*protobufs.PackageAvailable{"": {
			Type: protobufs.PackageType_PackageType_TopLevel, Version: "v" + version, Hash: hash,
			File: &protobufs.DownloadableFile{
				DownloadUrl: "https://fleet.example.com/api/v1/artifacts/x/content/otelcol-contrib_" + version + "_windows_amd64.tar.gz",
				ContentHash: hash,
				Headers:     &protobufs.Headers{Headers: []*protobufs.Header{{Key: "Authorization", Value: "Bearer myrd_x"}}},
			}}},
		AllPackagesHash: hash,
	}
	f.mu.Lock()
	f.offer["myriarch-updater"] = po
	f.mu.Unlock()
	return po
}

// pushOffer pushes the current offer to all connected updaters.
func (f *fakeMyriarchd) pushOffer(po *protobufs.PackagesAvailable) {
	f.mu.Lock()
	var conns []stypes.Connection
	var uids [][]byte
	for id, a := range f.agents {
		if a.conn != nil && a.ident["service.name"] == "myriarch-updater" {
			conns = append(conns, a.conn)
			uids = append(uids, id[:])
			f.offered[id] = string(po.AllPackagesHash)
		}
	}
	f.mu.Unlock()
	f.pushAll(conns, uids, func(uid []byte) *protobufs.ServerToAgent {
		return &protobufs.ServerToAgent{InstanceUid: uid, PackagesAvailable: po}
	})
}

func eventually(t testing.TB, d time.Duration, cond func() bool, msg string, args ...any) {
	t.Helper()
	require.Eventually(t, cond, d, 20*time.Millisecond, append([]any{msg}, args...)...)
}

// Regression test for a flaky data race (go test -race): tests read the
// fakeAgent returned by get() while server-side handlers (net/http connection
// goroutines) kept mutating it. get() must hand out a snapshot.
func TestFakeMyriarchdGetReturnsRaceFreeSnapshot(t *testing.T) {
	f := newFakeMyriarchd(t)
	id := uuid.New()
	msg := func(i int) *protobufs.AgentToServer {
		return &protobufs.AgentToServer{
			InstanceUid: id[:],
			AgentDescription: &protobufs.AgentDescription{IdentifyingAttributes: []*protobufs.KeyValue{
				{Key: "service.name", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "myriarch-updater"}}},
			}},
			PackageStatuses: &protobufs.PackageStatuses{Packages: map[string]*protobufs.PackageStatus{
				"": {Name: "", Status: protobufs.PackageStatusEnum_PackageStatusEnum_Installing, ErrorMessage: string(rune('a' + i%26))},
			}},
			Health: &protobufs.ComponentHealth{Healthy: true, Status: "ok"},
		}
	}
	f.onMessage(nil, msg(0), "Bearer x")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			f.onMessage(nil, msg(i), "Bearer x")
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		a := f.get(id)
		require.NotNil(t, a)
		_ = a.pkgStatus[""]
		_ = len(a.pkgLog) + len(a.healthLog)
		_ = a.ident["service.name"]
	}
}
