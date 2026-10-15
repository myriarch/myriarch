// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/refdoc"
	"github.com/myriarch/myriarch/internal/testutil/clitest"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	clitest.RunAsMain(main) // the process-level output contract (TestOutputContract) re-executes this binary
	os.Exit(pgtest.RunWithMigratedTemplate(m))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func devConfig(t *testing.T) (config.Config, string) {
	t.Helper()
	return devConfigDSN(t, pgtest.NewPool(t).Config().ConnString())
}

func devConfigDSN(t *testing.T, dsn string) (config.Config, string) {
	t.Helper()
	cfg := config.DevDefaults()
	cfg.Database.DSN = dsn
	addr := freeAddr(t)
	cfg.Server.ListenAddr = addr
	cfg.Server.PublicURL = "http://" + addr
	cfg.Server.ShutdownTimeout = 10 * time.Second
	cfg.Releases.Enabled = false // no network in tests
	cfg.GitOps.RepoURL = "dir:" + filepath.Join("..", "..", "internal", "testdata", "gitops")
	cfg.Dev.DataDir = t.TempDir()
	cfg.Artifacts.Dir = filepath.Join(cfg.Dev.DataDir, "artifacts")
	cfg.GitOps.CloneDir = filepath.Join(cfg.Dev.DataDir, "gitops")
	cfg.Telemetry.LogLevel = "warn"
	return cfg, addr
}

type runningServer struct {
	addr   string
	cancel context.CancelFunc
	done   chan error
}

func startRun(t *testing.T, cfg config.Config, addr string) *runningServer {
	t.Helper()
	return startRunWith(t, cfg, addr, runOptions{dev: true})
}

func startRunWith(t *testing.T, cfg config.Config, addr string, opts runOptions) *runningServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rs := &runningServer{addr: addr, cancel: cancel, done: make(chan error, 1)}
	go func() { rs.done <- run(ctx, cfg, opts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-rs.done:
		case <-time.After(20 * time.Second):
			t.Error("myriarchd did not stop")
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-rs.done:
			t.Fatalf("myriarchd exited during startup: %v", err)
		default:
		}
		resp, err := http.Get("http://" + addr + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return rs
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("/readyz never became ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (rs *runningServer) stop(t *testing.T) error {
	rs.cancel()
	select {
	case err := <-rs.done:
		rs.done <- err // for the cleanup
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("myriarchd did not stop")
		return nil
	}
}

func opampFirstMessage(id uuid.UUID, host string) []byte {
	kv := func(k, v string) *protobufs.KeyValue {
		return &protobufs.KeyValue{Key: k, Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}}}
	}
	msg := &protobufs.AgentToServer{
		InstanceUid: id[:], SequenceNum: 0,
		Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus | protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth),
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{kv("service.name", "otelcol-contrib"), kv("service.version", "0.161.0"), kv("service.instance.id", id.String())},
			NonIdentifyingAttributes: []*protobufs.KeyValue{kv("os.type", "windows"), kv("host.name", host), kv("host.arch", "amd64"),
				kv("site.type", "edge"), kv("site.id", "1001"), kv("site.country", "NL"), kv("site.timezone", "Europe/Amsterdam")},
		},
		Health: &protobufs.ComponentHealth{Healthy: true, Status: "StatusOK"},
	}
	raw, _ := proto.Marshal(msg)
	hdr := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(hdr, 0)
	return append(hdr[:n], raw...)
}

func TestDevModeStartsServesAndShutsDownGracefully(t *testing.T) {
	cfg, addr := devConfig(t)
	rs := startRun(t, cfg, addr)
	base := "http://" + addr

	// public endpoints
	get := func(path string) (int, string) {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/healthz"); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	if code, body := get("/api/v1/auth/config"); code != 200 || !strings.Contains(body, `"devLoginEnabled":true`) || !strings.Contains(body, `"ui":{"defaultLanguage":"en"`) {
		t.Fatalf("auth config: %d %s", code, body)
	}
	if code, body := get("/metrics"); code != 200 || !strings.Contains(body, "myriarch_leader") || !strings.Contains(body, "myriarch_http_requests_total") {
		t.Fatalf("metrics (served on the main port in dev): %d", code)
	}
	if code, _ := get("/api/v1/agents"); code != 401 {
		t.Fatalf("API without credentials: %d", code)
	}
	if code, body := get("/"); code != 200 || !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("UI fallback: %d", code)
	}

	// dev login + CSRF protected request
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	login, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin"})
	resp, err := hc.Post(base+"/api/v1/auth/dev-login", "application/json", bytes.NewReader(login))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("dev login: %v %v", resp, err)
	}
	var me dto.Me
	_ = json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me.Role != domain.RoleAdmin || me.CSRFToken == "" {
		t.Fatalf("me: %+v", me)
	}

	// live stream (SSE) with the session cookie
	sreq, _ := http.NewRequest("GET", base+"/api/v1/events/stream", nil)
	sresp, err := hc.Do(sreq)
	if err != nil || sresp.StatusCode != 200 {
		t.Fatalf("sse: %v %v", sresp, err)
	}
	defer sresp.Body.Close()
	lines := make(chan string, 256)
	go func() {
		buf := make([]byte, 4096)
		var pending string
		for {
			n, err := sresp.Body.Read(buf)
			pending += string(buf[:n])
			for {
				i := strings.Index(pending, "\n")
				if i < 0 {
					break
				}
				lines <- pending[:i]
				pending = pending[i+1:]
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	// an agent connects with the dev setup (no enrollment token required)
	id := uuid.Must(uuid.NewV7())
	conn, wresp, err := websocket.DefaultDialer.Dial("ws://"+addr+cfg.OpAMP.Path, nil)
	if err != nil {
		t.Fatalf("opamp dial: %v %v", err, wresp)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, opampFirstMessage(id, "EDGE-1001-01")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no reply from the OpAMP server: %v", err)
	}
	var reply protobufs.ServerToAgent
	if err := proto.Unmarshal(bytes.TrimPrefix(data, []byte{0}), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ErrorResponse != nil || reply.Capabilities == 0 {
		t.Fatalf("reply: %v", &reply)
	}
	if reply.RemoteConfig != nil {
		cm := reply.RemoteConfig.GetConfig().GetConfigMap()
		if obj, ok := cm[domain.RemoteConfigKey]; !ok || len(obj.Body) == 0 {
			t.Fatalf("remote config must use key %q: %v", domain.RemoteConfigKey, cm)
		}
		t.Logf("agent was assigned and received a %d byte config", len(cm[domain.RemoteConfigKey].Body))
	} else {
		t.Log("agent registered but received no config (fleet assignment not active?)")
	}

	// it is visible through the REST API (session cookie, CSRF only for mutations)
	var found bool
	deadline := time.Now().Add(10 * time.Second)
	for !found && time.Now().Before(deadline) {
		r, err := hc.Get(base + "/api/v1/agents?q=EDGE-1001-01")
		if err != nil {
			t.Fatal(err)
		}
		var page api.Page[dto.AgentSummary]
		_ = json.NewDecoder(r.Body).Decode(&page)
		r.Body.Close()
		found = page.Total == 1 && page.Items[0].Status == domain.ConnectionOnline && page.Items[0].ID == id
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("agent not visible as online through GET /api/v1/agents")
	}
	// live update arrived
	var sawUpdate bool
	timeout := time.After(10 * time.Second)
	for !sawUpdate {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("SSE stream ended")
			}
			if l == "event: agent.updated" {
				sawUpdate = true
			}
		case <-timeout:
			t.Fatal("no agent.updated SSE event")
		}
	}
	// mutating request needs the CSRF header
	req, _ := http.NewRequest("POST", base+"/api/v1/agents/"+id.String()+"/actions/resync", nil)
	if r, err := hc.Do(req); err != nil || r.StatusCode != 403 {
		t.Fatalf("resync without CSRF header: %v %v", r, err)
	}
	req, _ = http.NewRequest("POST", base+"/api/v1/agents/"+id.String()+"/actions/resync", nil)
	req.Header.Set("X-CSRF-Token", me.CSRFToken)
	if r, err := hc.Do(req); err != nil || r.StatusCode != 202 {
		t.Fatalf("resync with CSRF header: %v %v", r, err)
	}
	var info dto.SystemInfo
	r, _ := hc.Get(base + "/api/v1/system/info")
	_ = json.NewDecoder(r.Body).Decode(&info)
	r.Body.Close()
	if len(info.Replicas) != 1 || info.Replicas[0].Connections != 1 || info.LeaderID == "" || !info.Database.OK {
		t.Fatalf("system info: %+v", info)
	}

	// every metric the running server exposes is in the metrics reference (docs/reference/metrics.md is generated from the metric
	// definitions in the sources; this catches a metric that the source scan would miss)
	_, metricsBody := get("/metrics")
	documented, err := refdoc.ScanMetrics(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, m := range documented {
		types[m.Name] = m.Type
	}
	seen := 0
	for _, line := range strings.Split(metricsBody, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 || f[0] != "#" || f[1] != "TYPE" || !strings.HasPrefix(f[2], brand.MetricsNamespace+"_") {
			continue
		}
		seen++
		if want, ok := types[f[2]]; !ok || want != f[3] {
			t.Errorf("the server exposes %s (%s), which the metrics reference does not list with that type (found: %q)", f[2], f[3], want)
		}
	}
	if seen < 10 {
		t.Errorf("only %d myriarch_ metrics found in the /metrics output, the cross-check looks broken", seen)
	}

	// graceful shutdown: the agent connection is closed by the server, the agent is marked offline, run returns nil
	t0 := time.Now()
	if err := rs.stop(t); err != nil {
		t.Fatalf("run returned an error on graceful shutdown: %v", err)
	}
	if d := time.Since(t0); d > 15*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	dbc, err := pgx.Connect(context.Background(), cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close(context.Background())
	var status string
	var replica *string
	if err := dbc.QueryRow(context.Background(), `SELECT status, connected_replica FROM agents WHERE id = $1`, id).Scan(&status, &replica); err != nil {
		t.Fatal(err)
	}
	if status != "offline" || replica != nil {
		t.Fatalf("after shutdown the agent must be offline without replica: %s %v", status, replica)
	}
	_ = fmt.Sprint
}

func TestReadinessReportsSchemaProblems(t *testing.T) {
	cfg, addr := devConfig(t)
	cfg.Database.MigrateOnStart = false // the template database is migrated already
	startRun(t, cfg, addr)
	// a database whose schema is older than the binary is not ready
	dbc, err := pgx.Connect(context.Background(), cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close(context.Background())
	if _, err := dbc.Exec(context.Background(), `UPDATE goose_db_version SET is_applied = false WHERE version_id = (SELECT max(version_id) FROM goose_db_version)`); err != nil {
		t.Fatal(err)
	}
	// the positive result is cached for the process lifetime (forward-only migrations): a fresh readiness check proves the logic
	db, bus := openForReadiness(t, cfg)
	defer bus.Close()
	if err := readiness(db, bus)(context.Background()); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("readiness with an outdated schema: %v", err)
	}
}

func TestInvalidProductionConfigIsRejected(t *testing.T) {
	cmd := newRootCommand()
	cmd.SetArgs([]string{"serve", "--config", filepath.Join(t.TempDir(), "missing.yaml")})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("serve with a missing config file: %v", err)
	}
	// production validation: dev defaults are not acceptable
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("auth:\n  devLogin:\n    enabled: true\n    users: [{username: a, password: b, role: admin}]\nserver:\n  publicUrl: https://x\ndatabase:\n  dsn: postgres://x\ngitops:\n  repoUrl: https://git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = newRootCommand()
	cmd.SetArgs([]string{"serve", "--config", p})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "devLogin") {
		t.Fatalf("dev login in production config must be rejected: %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := execute([]string{"version"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "0.0.0-dev") || stderr.Len() != 0 {
		t.Fatalf("version: exit code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// TestTwoReplicasEndToEnd runs two complete myriarchd instances against one
// database: exactly one is leader, and an operator action on replica A reaches
// the agent connected to replica B (LISTEN/NOTIFY fan-out).
func TestTwoReplicasEndToEnd(t *testing.T) {
	dsn := pgtest.NewPool(t).Config().ConnString()
	cfgA, addrA := devConfigDSN(t, dsn)
	cfgA.HA.ReplicaID = "replica-a"
	cfgB, addrB := devConfigDSN(t, dsn)
	cfgB.HA.ReplicaID = "replica-b"
	startRun(t, cfgA, addrA)
	startRun(t, cfgB, addrB)

	login := func(addr string) (*http.Client, dto.Me) {
		jar, _ := cookiejar.New(nil)
		hc := &http.Client{Jar: jar, Timeout: 20 * time.Second}
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin"})
		resp, err := hc.Post("http://"+addr+"/api/v1/auth/dev-login", "application/json", bytes.NewReader(body))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("login on %s: %v %v", addr, resp, err)
		}
		var me dto.Me
		_ = json.NewDecoder(resp.Body).Decode(&me)
		resp.Body.Close()
		return hc, me
	}
	hcA, meA := login(addrA)

	// exactly one leader, visible on both replicas, both replicas registered
	var leaderOnA string
	deadline := time.Now().Add(30 * time.Second)
	for {
		var info dto.SystemInfo
		r, err := hcA.Get("http://" + addrA + "/api/v1/system/info")
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(r.Body).Decode(&info)
		r.Body.Close()
		leaders := 0
		for _, rep := range info.Replicas {
			if rep.IsLeader {
				leaders++
			}
		}
		if len(info.Replicas) == 2 && leaders == 1 && info.LeaderID != "" {
			leaderOnA = info.LeaderID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replicas/leader: %+v", info)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if leaderOnA != "replica-a" && leaderOnA != "replica-b" {
		t.Fatalf("leader %q", leaderOnA)
	}

	// the agent connects to replica B (without enrollment token, dev mode)
	id := uuid.Must(uuid.NewV7())
	conn, resp, err := websocket.DefaultDialer.Dial("ws://"+addrB+cfgB.OpAMP.Path, nil)
	if err != nil {
		t.Fatalf("dial replica b: %v %v", err, resp)
	}
	defer conn.Close()
	first := opampFirstMessage(id, "EDGE-1001-02")
	// restart capability so that the command is accepted
	var msg protobufs.AgentToServer
	_ = proto.Unmarshal(bytes.TrimPrefix(first, []byte{0}), &msg)
	msg.Capabilities |= uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand)
	raw, _ := proto.Marshal(&msg)
	if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, raw...)); err != nil {
		t.Fatal(err)
	}
	msgs := make(chan *protobufs.ServerToAgent, 16)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				close(msgs)
				return
			}
			var m protobufs.ServerToAgent
			if proto.Unmarshal(bytes.TrimPrefix(data, []byte{0}), &m) == nil {
				msgs <- &m
			}
		}
	}()
	select {
	case m := <-msgs:
		if m.ErrorResponse != nil {
			t.Fatalf("%v", m)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no reply from replica b")
	}

	// the operator uses replica A: restart (POST needs the CSRF header)
	deadline = time.Now().Add(15 * time.Second)
	for {
		req, _ := http.NewRequest("POST", "http://"+addrA+"/api/v1/agents/"+id.String()+"/actions/restart", nil)
		req.Header.Set("X-CSRF-Token", meA.CSRFToken)
		r, err := hcA.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode == 202 {
			break
		}
		if time.Now().After(deadline) { // the agent row may not be online yet
			t.Fatalf("restart via replica a: %d", r.StatusCode)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("connection closed")
			}
			if m.Command != nil && m.Command.Type == protobufs.CommandType_CommandType_Restart {
				return
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the restart command issued on replica a never reached the agent on replica b")
		}
	}
}

// TestDevCommandAgainstExternalPostgres: `myriarchd dev --dsn` (second dev replica) uses the given database
// instead of starting an embedded one.
func TestDevCommandAgainstExternalPostgres(t *testing.T) {
	dsn := pgtest.NewPool(t).Config().ConnString()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runDev(ctx, devOptions{
			gitopsDir: filepath.Join("..", "..", "internal", "testdata", "gitops"), dataDir: t.TempDir(), listen: addr, dsn: dsn,
			releasesFixtures: filepath.Join("..", "..", "internal", "release", "testdata", "fixtures"), replicaID: "dev-second",
		})
	}()
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("exited: %v", err)
		default:
		}
		resp, err := http.Get("http://" + addr + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	resp, err := http.Get("http://" + addr + "/api/v1/auth/config")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("did not stop")
	}
}
