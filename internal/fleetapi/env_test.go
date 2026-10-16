// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/assignment"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/blueprint"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/control"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/executor"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/notify"
	"github.com/myriarch/myriarch/internal/notify/channels"
	"github.com/myriarch/myriarch/internal/release"
	"github.com/myriarch/myriarch/internal/rollout"
	"github.com/myriarch/myriarch/internal/rollout/memstore"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/testutil/enrollstore"
)

type apiEnv struct {
	t        *testing.T
	st       *memstore.Store
	bus      eventbus.Bus
	spec     *spec
	ops      map[string]operation
	routes   map[string]api.Route
	mux      *http.ServeMux
	svc      *service
	deps     Deps
	cfg      config.Config
	assigner *assignment.Service
	engine   *rollout.Engine
	tracker  *release.Tracker
	cache    *artifact.Cache
	notifier notify.Notifier
	syncer   *gitops.Syncer
	agents   map[string]domain.AgentID
	hook     *hookSink
	upstream *httptest.Server
	clock    time.Time
	exec     *fakeExecutor
	// the installation from the server: enrollment tokens that the artifact cache can read by ID, the
	// authentication of the install routes, the planner, the log and the metrics of the service
	tokens  *enrollstore.Store
	auth    *testInstallAuth
	planner *install.Planner
	logs    *syncBuffer
	reg     *prometheus.Registry
}

type hookSink struct {
	mu   sync.Mutex
	hits int
	fail bool
	srv  *httptest.Server
}

type fakeExecutor struct {
	typ domain.ExecutorType
}

func (f *fakeExecutor) Type() domain.ExecutorType                                  { return f.typ }
func (f *fakeExecutor) Validate(domain.UpdateSpec, *domain.Executor) domain.Issues { return nil }
func (f *fakeExecutor) Start(_ context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	out := make([]executor.TargetResult, len(req.Targets))
	for i, t := range req.Targets {
		out[i] = executor.TargetResult{AgentID: t.AgentID, Started: true}
	}
	return out, nil
}
func (f *fakeExecutor) Cancel(context.Context, executor.CancelRequest) error { return nil }

const repoDir = "../testdata/gitops"

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	e := &apiEnv{t: t, st: memstore.New(), bus: eventbus.NewMemory("t", 0), spec: loadSpec(t), agents: map[string]domain.AgentID{}, clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { e.bus.Close() })
	e.st.Now = func() time.Time { return e.clock }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	e.cfg = config.Default()
	e.cfg.GitOps.UIRepoWebURL = "https://git.example.com/otel/fleet-config/-/blob/{commit}/{path}"
	e.cfg.Server.PublicURL = "https://fleet.example.com"
	e.cfg.Releases.FixtureDir = "../release/testdata/fixtures"

	// GitOps snapshot of the example repository.
	fsys := os.DirFS(repoDir)
	snap, issues := gitops.Parse(fsys, ".", "abc123def456")
	require.Empty(t, issues)
	_, err := e.st.GitOps().ApplySnapshot(context.Background(), *snap, domain.GitOpsSync{Trigger: "poll", Result: "applied", StartedAt: e.clock, FinishedAt: &e.clock, Commit: &domain.GitCommit{SHA: snap.Commit, Time: e.clock}})
	require.NoError(t, err)

	renderer := blueprint.NewRenderer(blueprint.Options{})
	agentCtl := control.NewBusControl(e.bus)
	e.assigner = assignment.New(assignment.Deps{Store: e.st, Bus: e.bus, Control: agentCtl, Renderer: renderer, Logger: logger, Registerer: prometheus.NewRegistry()})
	_, err = e.assigner.Reload(context.Background())
	require.NoError(t, err)

	// notification channels (a Teams channel and a generic webhook)
	e.hook = &hookSink{}
	e.hook.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hook.mu.Lock()
		e.hook.hits++
		fail := e.hook.fail
		e.hook.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(e.hook.srv.Close)
	notifier, err := channels.NewDispatcher(config.NotificationsConfig{Channels: []config.NotificationChannel{
		{Name: "teams-ops", Type: "teams", URL: e.hook.srv.URL + "/workflows/SECRETPATH/trigger?sig=SECRETSIG", Events: []string{"rollout.*"}},
		{Name: "generic", Type: "webhook", URL: e.hook.srv.URL + "/generic"},
	}}, channels.Deps{KV: e.st.KV(), HTTPClient: http.DefaultClient, PublicURL: e.cfg.Server.PublicURL, Logger: logger, Registerer: prometheus.NewRegistry()})
	require.NoError(t, err)
	e.notifier = notifier

	e.exec = &fakeExecutor{typ: domain.ExecutorUpdater}
	auditRec := audit.NewRecorder(e.st.Audit(), logger)
	e.engine = rollout.NewEngine(e.cfg.Rollouts, rollout.Deps{
		Store: e.st, Bus: e.bus, Control: agentCtl, Notifier: notify.Nop{}, Audit: auditRec, Logger: logger,
		Executors: executor.MapRegistry{domain.ExecutorUpdater: e.exec, domain.ExecutorWebhook: &fakeExecutor{typ: domain.ExecutorWebhook}, domain.ExecutorAWX: &fakeExecutor{typ: domain.ExecutorAWX},
			domain.ExecutorManual: &fakeExecutor{typ: domain.ExecutorManual}},
		Now: func() time.Time { return e.clock }, Registerer: prometheus.NewRegistry(),
	})

	// the leader has planned the state of Git by the time hosts ask: the installation waits for the record of the planner
	require.NoError(t, e.engine.Plan(context.Background()))

	// release tracker with the recorded fixtures
	tr, err := release.NewTracker(e.cfg.Releases, release.Deps{Store: e.st, Bus: e.bus, Notifier: notify.Nop{}, HTTPClient: http.DefaultClient, Logger: logger, Registerer: prometheus.NewRegistry()})
	require.NoError(t, err)
	e.tracker = tr
	require.NoError(t, tr.Poll(context.Background()))

	// artifact cache with a tiny fake upstream (no signature verification in these tests)
	archive := []byte(strings.Repeat("artifact-bytes-", 100))
	e.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".tar.gz"), strings.Contains(r.URL.Path, "/opampsupervisor_"):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.upstream.Close)
	acfg := config.Default().Artifacts
	acfg.Dir = t.TempDir()
	acfg.SourceBaseURL = e.upstream.URL
	acfg.Verify.RequireChecksum = false
	acfg.Verify.RequireSignature = false
	e.tokens = enrollstore.Wrap(e.st)
	cache, err := artifact.NewCache(acfg, artifact.Deps{Store: e.tokens, Bus: e.bus, HTTPClient: http.DefaultClient, PublicURL: e.cfg.Server.PublicURL,
		DownloadSecret: []byte("0123456789abcdef0123456789abcdef"), InstallEnabled: e.cfg.Install.Enabled, InstallGrantTTL: e.cfg.Install.GrantTTL, Logger: logger, Registerer: prometheus.NewRegistry()})
	require.NoError(t, err)
	e.cache = cache
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.cache.WaitPrepared(ctx) // downloads that an install plan started
		e.waitNoDownloads(t)
	})

	// syncer on a copy of the example repository
	dir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.CopyFS(dir, os.DirFS(repoDir)))
	gcfg := e.cfg.GitOps
	gcfg.RepoURL, gcfg.WebhookSecret = "dir:"+dir, "s3cret"
	syncer, err := gitops.NewSyncer(gcfg, gitops.Deps{Store: e.st, Bus: e.bus, Renderer: renderer, Audit: auditRec, Logger: logger, Registerer: prometheus.NewRegistry()})
	require.NoError(t, err)
	e.syncer = syncer

	// the installation from the server
	e.logs = &syncBuffer{}
	e.reg = prometheus.NewRegistry()
	e.auth = newTestInstallAuth(e)
	e.planner, err = install.NewPlanner(install.Deps{Store: e.st, Assigner: e.assigner, Artifacts: cache, Config: e.cfg.Install,
		PublicURL: e.cfg.Server.PublicURL, OpAMPPath: e.cfg.OpAMP.Path, Logger: slog.New(slog.NewTextHandler(e.logs, nil))})
	require.NoError(t, err)

	e.deps = Deps{Store: e.st, Syncer: syncer, Assigner: e.assigner, Renderer: renderer, Engine: e.engine, Tracker: tr, Artifacts: cache,
		Notifier: notifier, Audit: auditRec, Bus: e.bus, Config: e.cfg, Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
		Install: e.planner, InstallAuth: e.auth, Registerer: e.reg}
	e.svc = newService(e.deps)
	e.svc.waitMax, e.svc.waitPoll = 400*time.Millisecond, 5*time.Millisecond
	mods := e.svc.modules()
	e.routes = map[string]api.Route{}
	e.mux = http.NewServeMux()
	for _, m := range mods {
		for _, rt := range m.Routes() {
			e.routes[rt.OperationID] = rt
			e.mount(rt)
		}
	}
	e.ops = map[string]operation{}
	for _, op := range e.spec.operations() {
		e.ops[op.id] = op
	}
	return e
}

// mount registers a route like internal/httpserver: authentication and the
// permission check are done by the "middleware".
func (e *apiEnv) mount(rt api.Route) {
	handler := rt.Handler
	e.mux.HandleFunc(rt.Method+" "+api.BasePath+rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
		if rt.Auth == api.AuthRequired {
			role := domain.Role(r.Header.Get("X-Test-Role"))
			if role == "" {
				api.WriteError(w, r, domain.ErrUnauthorized)
				return
			}
			id := "u-" + string(role)
			if v := r.Header.Get("X-Test-User-ID"); v != "" {
				id = v
			}
			p := &authz.Principal{Type: authz.PrincipalUser, ID: id, Name: string(role), Role: role, AuthMethod: "test"}
			if !p.Can(rt.Permission) {
				api.WriteError(w, r, domain.ErrForbidden)
				return
			}
			r = r.WithContext(authz.WithPrincipal(r.Context(), p))
		}
		if rt.MaxBodyBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, rt.MaxBodyBytes)
		}
		handler(w, r)
	})
}

// addAgent registers a sample agent as a collector (assigned through the real
// assignment service).
func (e *apiEnv) addAgent(sample string, mut ...func(a *domain.Agent)) domain.AgentID {
	e.t.Helper()
	fsys := os.DirFS(repoDir)
	samples, issues := gitops.ParseSamples(fsys, ".")
	require.Empty(e.t, issues)
	var sm blueprint.Sample
	for _, s := range samples {
		if s.Name == sample {
			sm = s
		}
	}
	require.NotEmpty(e.t, sm.Name, sample)
	id := uuid.Must(uuid.NewV7())
	a := domain.Agent{
		ID: id, Kind: domain.AgentKindCollector, ServiceName: "otelcol-contrib", Version: sm.Attributes["service.version"],
		Hostname: sm.Attributes["host.name"], OSType: sm.Attributes["os.type"], Arch: sm.Attributes["host.arch"],
		IdentifyingAttributes:    map[string]string{"service.name": "otelcol-contrib", "service.version": sm.Attributes["service.version"]},
		NonIdentifyingAttributes: map[string]string{}, Labels: map[string]string{},
		Connection:   domain.AgentConnection{Status: domain.ConnectionOnline},
		Health:       &domain.AgentHealth{Healthy: true},
		RemoteConfig: domain.RemoteConfigState{Status: domain.RemoteConfigApplied},
		FirstSeenAt:  e.clock,
	}
	for k, v := range sm.Attributes {
		if k != "service.name" && k != "service.version" {
			a.NonIdentifyingAttributes[k] = v
		}
	}
	for _, m := range mut {
		m(&a)
	}
	e.st.PutAgent(a)
	_, err := e.assigner.AssignAgent(context.Background(), id)
	require.NoError(e.t, err)
	// the agent applied its (bootstrap) config
	e.st.UpdateAgent(id, func(ag *domain.Agent) { ag.RemoteConfig.LastHash = ag.Assignment.DesiredConfigHash })
	e.agents[sample] = id
	return id
}

// ------------------------------------------------------------------ calls ----

type resp struct {
	*httptest.ResponseRecorder
}

func (e *apiEnv) do(method, path, role string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(e.t, err)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, api.BasePath+path, rd)
	if rd != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if role != "" {
		r.Header.Set("X-Test-Role", role)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return resp{w}
}

func (e *apiEnv) get(path string) resp { return e.do(http.MethodGet, path, "viewer", nil) }
func (e *apiEnv) post(path string, body any) resp {
	return e.do(http.MethodPost, path, "operator", body)
}

// expect asserts the status and validates the body against the OpenAPI
// schema of the operation's response.
func (e *apiEnv) expect(r resp, opID string, status int) any {
	e.t.Helper()
	require.Equal(e.t, status, r.Code, "body: %s", r.Body.String())
	if status == http.StatusNoContent {
		return nil
	}
	op, ok := e.ops[opID]
	require.True(e.t, ok, "unknown operation %s", opID)
	var v any
	require.NoError(e.t, json.Unmarshal(r.Body.Bytes(), &v), r.Body.String())
	if status >= 400 {
		sc := e.spec.resolve("#/components/schemas/Problem")
		require.NotNil(e.t, sc)
		assert.Empty(e.t, e.spec.validate(v, sc, "$"), "problem body: %s", r.Body.String())
		return v
	}
	sc, ok := e.spec.responseSchema(op, status)
	require.True(e.t, ok, "operation %s documents no JSON response for %d", opID, status)
	errs := e.spec.validate(v, sc, "$")
	assert.Empty(e.t, errs, "response of %s does not match the OpenAPI schema:\n%s", opID, r.Body.String())
	return v
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "%T", v)
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	m := asMap(t, v)
	l, ok := m["items"].([]any)
	require.True(t, ok, "items missing: %v", m)
	return l
}

func storeFilter() store.RolloutFilter { return store.RolloutFilter{} }

// waitNoDownloads waits until no background prefetch is running any more (they
// write into the cache directory, which must stay until they are done).
func (e *apiEnv) waitNoDownloads(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		items, _, err := e.st.Artifacts().List(context.Background(), store.ArtifactFilter{})
		if err == nil {
			busy := false
			for _, a := range items {
				if a.Status == domain.ArtifactDownloading {
					busy = true
				}
			}
			if !busy {
				time.Sleep(50 * time.Millisecond)
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Log("prefetch still running at the end of the test")
}
