// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/httpserver"
	"github.com/myriarch/myriarch/internal/leader"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeAuth maps the bearer "role:<name>" to a principal (API token semantics: no CSRF).
type fakeAuth struct {
	userID uuid.UUID
}

func (f *fakeAuth) Authenticate(r *http.Request) (*authz.Principal, error) {
	h := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch h {
	case "viewer", "operator", "admin":
		return &authz.Principal{Type: authz.PrincipalToken, ID: "tok-" + h, Name: h, Role: domain.Role(h), AuthMethod: "api_token"}, nil
	case "user-viewer", "user-operator":
		return &authz.Principal{Type: authz.PrincipalUser, ID: f.userID.String(), Name: h, Role: domain.Role(strings.TrimPrefix(h, "user-")), AuthMethod: "local"}, nil
	}
	return nil, fmt.Errorf("%w: no credentials", domain.ErrUnauthorized)
}
func (f *fakeAuth) CheckCSRF(*http.Request, *authz.Principal) error { return nil }

type staticLeader struct{}

func (staticLeader) Run(ctx context.Context, f func(ctx context.Context)) error {
	<-ctx.Done()
	return nil
}
func (staticLeader) IsLeader() bool { return true }

var _ leader.Elector = staticLeader{}

type fakeControl struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (c *fakeControl) record(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, s)
	return c.err
}
func (c *fakeControl) DesiredStateChanged(_ context.Context, ids []domain.AgentID) error {
	return c.record(fmt.Sprint("desired:", len(ids)))
}
func (c *fakeControl) Restart(_ context.Context, id domain.AgentID, by string) error {
	return c.record("restart:" + id.String() + ":" + by)
}
func (c *fakeControl) RequestFullState(_ context.Context, id domain.AgentID, by string) error {
	return c.record("fullstate:" + id.String() + ":" + by)
}
func (c *fakeControl) Resync(_ context.Context, id domain.AgentID, by string) error {
	return c.record("resync:" + id.String() + ":" + by)
}
func (c *fakeControl) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

type env struct {
	t    *testing.T
	db   *postgres.DB
	srv  *httptest.Server
	ctl  *fakeControl
	bus  eventbus.Bus
	auth *fakeAuth
	cfg  config.Config
	now  time.Time
}

func newEnv(t *testing.T, mutate func(c *config.Config)) *env {
	t.Helper()
	db := postgres.New(pgtest.NewPool(t), quiet)
	e := &env{t: t, db: db, ctl: &fakeControl{}, bus: eventbus.NewMemory("test", 0), auth: &fakeAuth{userID: uuid.New()}, now: time.Now().UTC()}
	e.cfg = config.Default()
	e.cfg.Server.PublicURL = "https://fleet.example.com"
	e.cfg.GitOps.RepoURL = "https://oauth2:secret-token@git.example.com/otel/config.git?token=abc"
	if mutate != nil {
		mutate(&e.cfg)
	}
	if _, err := db.Identity().UpsertUser(context.Background(), domain.User{ID: e.auth.userID, Subject: "local:tester", Provider: "local", Role: domain.RoleOperator}); err != nil {
		t.Fatal(err)
	}
	mods := coreapi.New(coreapi.Deps{
		Store: db, Bus: e.bus, Control: e.ctl, Audit: audit.NewRecorder(db.Audit(), quiet), Elector: staticLeader{}, Config: e.cfg, LocalConnections: func() int { return 7 },
		ReplicaID: "replica-1", DevMode: true, Logger: quiet, SchemaInfo: db.SchemaInfo, FleetCounts: db.FleetCounts, Now: func() time.Time { return e.now },
	})
	hs, err := httpserver.New(e.cfg.Server, httpserver.Deps{Auth: e.auth, Modules: mods, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(hs.Handler())
	t.Cleanup(e.srv.Close)
	t.Cleanup(func() { _ = e.bus.Close() })
	return e
}

type resp struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (r resp) into(v any) {
	if err := json.Unmarshal(r.Body, v); err != nil {
		panic(fmt.Sprintf("decode %q: %v", r.Body, err))
	}
}

func (e *env) do(role, method, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+api.BasePath+path, rd)
	if role != "" {
		req.Header.Set("Authorization", "Bearer "+role)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{Code: res.StatusCode, Header: res.Header, Body: raw}
}

func (e *env) get(path string) resp { return e.do("viewer", "GET", path, nil) }

func (e *env) problemCode(r resp) string {
	var p api.Problem
	r.into(&p)
	return p.Code
}

// seed creates an agent.
type seed struct {
	id       domain.AgentID
	host     string
	version  string
	kind     domain.AgentKind
	os       string
	site     string
	siteType string
	labels   map[string]string
	online   bool
	fleet    string
	caps     uint64
	healthy  *bool
	replica  string
}

func (e *env) seed(s seed) domain.AgentID {
	e.t.Helper()
	ctx := context.Background()
	if s.id == (domain.AgentID{}) {
		s.id = domain.AgentID(uuid.Must(uuid.NewV7()))
	}
	if s.kind == "" {
		s.kind = domain.AgentKindCollector
	}
	if s.os == "" {
		s.os = "linux"
	}
	if s.caps == 0 {
		s.caps = domain.CapAcceptsRemoteConfig | domain.CapAcceptsRestartCommand | domain.CapReportsHealth
	}
	non := map[string]string{"host.name": s.host, "os.type": s.os}
	if s.site != "" {
		non["site.id"] = s.site
	}
	if s.siteType != "" {
		non["site.type"] = s.siteType
		non["site.country"] = "NL"
	}
	if _, _, err := e.db.Agents().UpsertDescription(ctx, s.id, store.AgentDescriptionUpdate{
		Kind: s.kind, ServiceName: "otelcol-contrib", Version: s.version, Hostname: s.host, OSType: s.os, Arch: "amd64",
		IdentifyingAttributes: map[string]string{"service.name": "otelcol-contrib"}, NonIdentifyingAttributes: non, Capabilities: s.caps, InitialLabels: s.labels,
	}); err != nil {
		e.t.Fatal(err)
	}
	now := time.Now().UTC()
	status := domain.ConnectionOffline
	if s.online {
		status = domain.ConnectionOnline
	}
	rep := s.replica
	if !s.online {
		rep = ""
	} else if rep == "" {
		rep = "replica-1"
	}
	if err := e.db.Agents().SetConnection(ctx, s.id, domain.AgentConnection{Status: status, ReplicaID: rep, Transport: "websocket", LastSeenAt: &now}); err != nil {
		e.t.Fatal(err)
	}
	if s.healthy != nil {
		if err := e.db.Agents().SetHealth(ctx, s.id, &domain.AgentHealth{Healthy: *s.healthy, Status: "StatusOK", UpdatedAt: now}); err != nil {
			e.t.Fatal(err)
		}
	}
	if s.fleet != "" {
		hash := domain.SHA256([]byte("cfg-" + s.fleet))
		_ = e.db.Configs().PutRendered(ctx, domain.RenderedConfig{Hash: hash, Body: []byte("cfg-" + s.fleet + "\n"), CreatedAt: now})
		_ = e.db.Agents().SetAssignment(ctx, s.id, store.AgentAssignmentUpdate{FleetName: s.fleet, AssignedConfigHash: hash, TargetVersion: "0.162.0"})
		_ = e.db.Agents().SetDesiredConfig(ctx, []domain.AgentID{s.id}, hash)
		_ = e.db.Agents().SetRemoteConfigState(ctx, s.id, domain.RemoteConfigState{Status: domain.RemoteConfigApplied, LastHash: hash})
	}
	return s.id
}

func (e *env) addRelease(version string) {
	e.t.Helper()
	pub := time.Now().UTC()
	if _, err := e.db.Releases().Upsert(context.Background(), domain.Release{Distribution: domain.DistributionContrib, Version: version, Tag: "v" + version, PublishedAt: &pub}); err != nil {
		e.t.Fatal(err)
	}
}

func bptr(b bool) *bool { return &b }

func qs(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return "?" + v.Encode()
}
