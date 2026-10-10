// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// supervisorClient is a real opamp-go WebSocket client behaving like the
// upstream supervisor: reports health, effective config and components,
// applies (or rejects) remote configs and reports the status.
type supervisorClient struct {
	t       *testing.T
	id      domain.AgentID
	host    string
	cli     client.OpAMPClient
	rejects bool // report FAILED for every config containing "REJECT"

	configs     atomic.Int32
	lastHash    atomic.Value // []byte
	lastKey     atomic.Value // string
	effReported atomic.Int32
	restarts    atomic.Int32
	restartMsgs atomic.Int32
	connects    atomic.Int32
	mu          sync.Mutex
	lastBody    string
	otherFields atomic.Int32 // remote config messages that carried a command or other payload
}

type quietLogger struct{}

func (quietLogger) Debugf(context.Context, string, ...any) {}
func (quietLogger) Errorf(context.Context, string, ...any) {}

func startSupervisor(t *testing.T, ctx context.Context, url string, id domain.AgentID, host string, token string) *supervisorClient {
	t.Helper()
	sc := &supervisorClient{t: t, id: id, host: host}
	sc.cli = client.NewWebSocket(quietLogger{})
	if err := sc.cli.SetAgentDescription(description(id, descOpts{host: host, version: "0.162.0", os: "linux", siteType: "edge", siteID: host})); err != nil {
		t.Fatal(err)
	}
	caps := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth | protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat | protobufs.AgentCapabilities_AgentCapabilities_ReportsAvailableComponents
	if err := sc.cli.SetCapabilities(&caps); err != nil {
		t.Fatal(err)
	}
	if err := sc.cli.SetHealth(healthy()); err != nil {
		t.Fatal(err)
	}
	comps := &protobufs.AvailableComponents{Hash: []byte("scale-components-v1"), Components: map[string]*protobufs.ComponentDetails{
		"receivers": {SubComponentMap: map[string]*protobufs.ComponentDetails{"otlp": {Metadata: []*protobufs.KeyValue{kv("code.namespace", "otlpreceiver v0.162.0")}}}},
	}}
	if err := sc.cli.SetAvailableComponents(comps); err != nil {
		t.Fatal(err)
	}
	hdr := http.Header{}
	if token != "" {
		hb := "Bearer " + token
		hdr.Set("Authorization", hb)
	}
	hb := 400 * time.Millisecond
	err := sc.cli.Start(ctx, types.StartSettings{
		OpAMPServerURL: url, Header: hdr, InstanceUid: types.InstanceUid(id), HeartbeatInterval: &hb,
		Callbacks: types.Callbacks{
			OnConnect: func(context.Context) { sc.connects.Add(1) },
			OnMessage: func(ctx context.Context, msg *types.MessageData) {
				if msg.RemoteConfig == nil {
					return
				}
				sc.configs.Add(1)
				hash := msg.RemoteConfig.ConfigHash
				sc.lastHash.Store(hash)
				cm := msg.RemoteConfig.GetConfig().GetConfigMap()
				body := ""
				for k, o := range cm {
					sc.lastKey.Store(k)
					body = string(o.Body)
				}
				sc.mu.Lock()
				sc.lastBody = body
				sc.mu.Unlock()
				status := protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED
				errMsg := ""
				if sc.rejects && strings.Contains(body, "REJECT") {
					status, errMsg = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "invalid config"
				}
				_ = sc.cli.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{LastRemoteConfigHash: hash, Status: status, ErrorMessage: errMsg})
			},
			GetEffectiveConfig: func(context.Context) (*protobufs.EffectiveConfig, error) {
				sc.effReported.Add(1)
				return &protobufs.EffectiveConfig{ConfigMap: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{
					"": {Body: []byte("effective: " + host + "\n"), ContentType: "text/yaml"}}}}, nil
			},
			OnCommand:              func(context.Context, *protobufs.ServerToAgentCommand) error { sc.restarts.Add(1); return nil },
			SaveRemoteConfigStatus: func(context.Context, *protobufs.RemoteConfigStatus) {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// tcpProxy forwards TCP connections to upstream and can drop all of them
// (simulates a network failure or a load balancer restart; hijacked WebSocket
// connections are not tracked by httptest.Server.CloseClientConnections).
type tcpProxy struct {
	ln       net.Listener
	upstream string
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func newProxy(t *testing.T, upstream string) *tcpProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, upstream: upstream, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() { _ = ln.Close(); p.Drop() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = c.Close()
				continue
			}
			p.mu.Lock()
			p.conns[c], p.conns[u] = struct{}{}, struct{}{}
			p.mu.Unlock()
			go func() { _, _ = io.Copy(u, c); _ = u.Close(); _ = c.Close() }()
			go func() { _, _ = io.Copy(c, u); _ = u.Close(); _ = c.Close() }()
		}
	}()
	return p
}

func (p *tcpProxy) URL() string { return "ws://" + p.ln.Addr().String() }

func (p *tcpProxy) Drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}

func scaleN(t *testing.T) int {
	n := 200
	if v := os.Getenv("OPAMP_TEST_CLIENTS"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	if testing.Short() && n > 30 {
		n = 30
	}
	return n
}

// TestTwoHundredSupervisorsLifecycle is the acceptance test of the OpAMP
// server: N real opamp-go WebSocket clients connect to an httptest server
// backed by PostgreSQL, get registered (a fake assigner sets the desired
// config), receive the remote config under the key myriarch.yaml, report
// APPLIED (config_sync = in_sync), one client reports FAILED (no re-offer,
// resync re-offers), restart arrives as own command, and a reconnect storm
// (all connections dropped) leads to full state requests without re-offers.
func TestTwoHundredSupervisorsLifecycle(t *testing.T) {
	n := scaleN(t)
	raiseNoFile(t, 8192)
	r := newRig(t, rigOpts{cfg: func(c *config.OpAMPConfig) { c.OfflineAfter = 30 * time.Second }})
	r.assigner.body = func(id domain.AgentID) []byte {
		body := "receivers:\n  otlp: {}\nservice: {}\n# shared\n"
		if id == failID.Load().(domain.AgentID) {
			body += "# REJECT\n"
		}
		return []byte(body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxy := newProxy(t, strings.TrimPrefix(r.ts.URL, "http://"))
	clients := make([]*supervisorClient, n)
	failer := newAgentID()
	failID.Store(failer)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 40)
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			id := newAgentID()
			if i == 0 {
				id = failer
			}
			clients[i] = startSupervisor(t, ctx, proxy.URL(), id, fmt.Sprintf("%04d", i), "")
			if i == 0 {
				clients[i].rejects = true
			}
		}(i)
	}
	wg.Wait()
	t.Cleanup(func() {
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		for _, cl := range clients {
			_ = cl.cli.Stop(sctx)
		}
	})

	facetCount := func(f store.AgentFilter, pick func(store.AgentFacets) []store.FacetValue, value string) int {
		fc, err := r.db.Agents().Facets(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range pick(fc) {
			if v.Value == value {
				return v.Count
			}
		}
		return 0
	}
	eventually(t, fmt.Sprintf("all %d agents registered, offered and in sync (except the rejecting one)", n), 60*time.Second, func() bool {
		return facetCount(store.AgentFilter{}, func(f store.AgentFacets) []store.FacetValue { return f.ConfigSync }, "in_sync") == n-1
	})
	t.Logf("%d supervisors registered and in sync after %v", n, time.Since(start))

	for i, cl := range clients {
		if cl.configs.Load() < 1 {
			t.Fatalf("client %d never received a config", i)
		}
		if k, _ := cl.lastKey.Load().(string); k != domain.RemoteConfigKey {
			t.Fatalf("client %d: config_map key %q, want %q", i, k, domain.RemoteConfigKey)
		}
	}
	if facetCount(store.AgentFilter{}, func(f store.AgentFacets) []store.FacetValue { return f.Status }, "online") != n {
		t.Fatal("all agents must be online")
	}
	if facetCount(store.AgentFilter{}, func(f store.AgentFacets) []store.FacetValue { return f.Kinds }, "collector") != n {
		t.Fatal("all agents are collectors")
	}

	// the rejecting client: FAILED stored, not offered again
	failedAgent := func() *domain.Agent { return r.agent(failer) }
	eventually(t, "failed hash recorded", 20*time.Second, func() bool { return !failedAgent().Assignment.FailedConfigHash.IsZero() })
	if failedAgent().ConfigSyncState() != domain.ConfigSyncFailed {
		t.Fatalf("config sync of the rejecting agent: %s", failedAgent().ConfigSyncState())
	}
	time.Sleep(1500 * time.Millisecond) // several heartbeats (400ms)
	if c := clients[0].configs.Load(); c != 1 {
		t.Fatalf("the failed config was offered %d times, want exactly once", c)
	}
	// operator resync: clear + resync -> offered again (and the client reports the full state again)
	eff := clients[0].effReported.Load()
	if err := r.db.Agents().ClearFailedConfig(context.Background(), failer); err != nil {
		t.Fatal(err)
	}
	if err := r.ctl.Resync(context.Background(), failer, "user:1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "resync re-offers the config", 10*time.Second, func() bool { return clients[0].configs.Load() == 2 })
	eventually(t, "resync requests the full state", 10*time.Second, func() bool { return clients[0].effReported.Load() > eff })

	// restart command
	if err := r.ctl.Restart(context.Background(), clients[1].id, "user:1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "restart command delivered", 10*time.Second, func() bool { return clients[1].restarts.Load() == 1 })

	// a push to a single agent reaches only that agent
	target := clients[2]
	hash := newDesired(t, r, target.id, "service: {}\n# only for one\n")
	eventually(t, "targeted push", 10*time.Second, func() bool { return target.configs.Load() == 2 })
	got, _ := target.lastHash.Load().([]byte)
	if string(got) != string(hash) {
		t.Fatal("pushed hash mismatch")
	}
	for i, cl := range clients {
		if i != 2 && i != 0 && cl.configs.Load() != 1 {
			t.Fatalf("client %d received %d configs, want 1", i, cl.configs.Load())
		}
	}

	// reconnect storm: drop every connection; clients reconnect with an empty first message
	effBefore := make([]int32, n)
	for i, cl := range clients {
		effBefore[i] = cl.effReported.Load()
	}
	cfgBefore := make([]int32, n)
	for i, cl := range clients {
		cfgBefore[i] = cl.configs.Load()
	}
	proxy.Drop()
	stormDeadline := time.Now().Add(90 * time.Second)
	for {
		lacking, notReconnected := 0, 0
		for i, cl := range clients {
			if cl.effReported.Load() <= effBefore[i] {
				lacking++
			}
			if cl.connects.Load() < 2 {
				notReconnected++
			}
		}
		online := facetCount(store.AgentFilter{}, func(f store.AgentFacets) []store.FacetValue { return f.Status }, "online")
		if lacking == 0 && online == n {
			break
		}
		if time.Now().After(stormDeadline) {
			t.Fatalf("reconnect storm: %d clients did not report their full state again, %d did not reconnect, %d/%d online, server connections %d",
				lacking, notReconnected, online, n, r.srv.Connections())
		}
		time.Sleep(200 * time.Millisecond)
	}
	for i, cl := range clients {
		if cl.configs.Load() != cfgBefore[i] {
			t.Fatalf("client %d got a config re-offered after reconnecting (%d -> %d)", i, cfgBefore[i], cl.configs.Load())
		}
	}
	if got := facetCount(store.AgentFilter{}, func(f store.AgentFacets) []store.FacetValue { return f.ConfigSync }, "in_sync"); got < n-2 {
		t.Fatalf("in_sync after reconnect storm: %d of %d", got, n)
	}

	// heartbeats keep everybody alive and last_seen moves
	time.Sleep(1500 * time.Millisecond)
	r.srv.SweepOffline(context.Background())
	if facetCount(store.AgentFilter{}, func(f store.AgentFacets) []store.FacetValue { return f.Status }, "online") != n {
		t.Fatal("heartbeating agents must stay online")
	}
	if c := r.srv.Connections(); c != n {
		t.Fatalf("server reports %d connections, want %d", c, n)
	}
	t.Logf("scale test with %d clients finished in %v", n, time.Since(start))
}

var failID atomic.Value
