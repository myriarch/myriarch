// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/fleetapi"
	"github.com/myriarch/myriarch/internal/gitops"
	"github.com/myriarch/myriarch/internal/httpserver"
	"github.com/myriarch/myriarch/internal/live"
	"github.com/myriarch/myriarch/internal/store"
)

type routesKV struct {
	store.KVStore
	m map[string][]byte
}

func (k *routesKV) Get(_ context.Context, key string) ([]byte, error) {
	if v, ok := k.m[key]; ok {
		return v, nil
	}
	return nil, domain.ErrNotFound
}

func (k *routesKV) PutIfAbsent(_ context.Context, key string, v []byte) (bool, error) {
	if _, ok := k.m[key]; ok {
		return false, nil
	}
	k.m[key] = v
	return true, nil
}

type routesStore struct {
	store.Store
	kv *routesKV
}

func (s routesStore) KV() store.KVStore { return s.kv }

// TestEveryOpenAPIOperationIsRoutedExactlyOnce builds all modules like run()
// does (with fakes), registers them in httpserver.New and compares the route
// table with api/openapi.yaml: every operationId exactly once, same method,
// path and permission; no route without an operation. It also guards the
// structure of the spec that the per-package tests rely on: every operation has
// exactly one tag, that tag is declared at the top level, every declared tag is
// used, and no operation carries an extension other than x-permission.
func TestEveryOpenAPIOperationIsRoutedExactlyOnce(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Tags []struct {
			Name string `yaml:"name"`
		} `yaml:"tags"`
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	declaredTags := map[string]bool{}
	for _, tg := range spec.Tags {
		if declaredTags[tg.Name] {
			t.Fatalf("tag %s is declared twice", tg.Name)
		}
		declaredTags[tg.Name] = true
	}
	usedTags := map[string]bool{}
	type specOp struct{ method, path, perm string }
	want := map[string]specOp{}
	for path, item := range spec.Paths {
		for method, node := range item {
			switch method {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			var op struct {
				OperationID string   `yaml:"operationId"`
				Tags        []string `yaml:"tags"`
				Permission  string   `yaml:"x-permission"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if len(op.Tags) != 1 {
				t.Errorf("%s %s: expected exactly one tag, got %v", method, path, op.Tags)
			} else if !declaredTags[op.Tags[0]] {
				t.Errorf("%s %s: tag %s is not declared at the top level", method, path, op.Tags[0])
			} else {
				usedTags[op.Tags[0]] = true
			}
			// Operation-level extensions: only x-permission is allowed, so that
			// no ownership data can come back.
			for i := 0; i+1 < len(node.Content); i += 2 {
				if key := node.Content[i].Value; strings.HasPrefix(key, "x-") && key != "x-permission" {
					t.Errorf("%s: unexpected extension %s", op.OperationID, key)
				}
			}
			if op.OperationID == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			if _, dup := want[op.OperationID]; dup {
				t.Fatalf("duplicate operationId %s in the spec", op.OperationID)
			}
			want[op.OperationID] = specOp{strings.ToUpper(method), path, op.Permission}
		}
	}
	if len(want) < 60 {
		t.Fatalf("suspiciously few operations in the spec: %d", len(want))
	}
	for name := range declaredTags {
		if !usedTags[name] {
			t.Errorf("tag %s is declared but no operation uses it", name)
		}
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := routesStore{kv: &routesKV{m: map[string][]byte{}}}
	authSvc, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, config.OpAMPConfig{},
		authn.Deps{Store: st, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	hub := live.New(eventbus.NewMemory("x", 0), nil, quiet)
	modules := []api.Module{authSvc.Module(), hub.Module()}
	modules = append(modules, coreapi.New(coreapi.Deps{Logger: quiet})...)
	syncer, err := gitops.NewSyncer(config.GitOpsConfig{}, gitops.Deps{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.NewCache(config.ArtifactsConfig{Dir: t.TempDir()}, artifact.Deps{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	modules = append(modules, fleetapi.New(fleetapi.Deps{Logger: quiet, Syncer: syncer, Artifacts: artifacts})...)

	srv, err := httpserver.New(config.ServerConfig{}, httpserver.Deps{Auth: authSvc, Modules: modules, Logger: quiet})
	if err != nil {
		t.Fatalf("httpserver.New: %v", err) // also catches duplicate operationIds and ServeMux pattern conflicts
	}

	got := map[string]api.Route{}
	for _, m := range modules {
		for _, r := range m.Routes() {
			if _, dup := got[r.OperationID]; dup {
				t.Errorf("operationId %s registered twice", r.OperationID)
			}
			got[r.OperationID] = r
		}
	}
	var ids []string
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := want[id]
		r, ok := got[id]
		if !ok {
			t.Errorf("%s (%s %s) is not routed", id, w.method, w.path)
			continue
		}
		if r.Method != w.method || r.Pattern != w.path {
			t.Errorf("%s: route is %s %s, spec says %s %s", id, r.Method, r.Pattern, w.method, w.path)
		}
		switch w.perm {
		case "public":
			if r.Auth != api.AuthPublic {
				t.Errorf("%s: x-permission public requires AuthPublic", id)
			}
		case "custom":
			if r.Auth != api.AuthCustom {
				t.Errorf("%s: x-permission custom requires AuthCustom", id)
			}
		default:
			if r.Auth != api.AuthRequired || string(r.Permission) != w.perm {
				t.Errorf("%s: auth=%v permission=%q, spec says %q", id, r.Auth, r.Permission, w.perm)
			}
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("route %s is not an operation of the spec", id)
		}
	}

	// The real mux answers every operation's method+path with something other
	// than the catch-all 404 (unauthenticated requests get 401 or the
	// handler's own error).
	for _, id := range ids {
		w := want[id]
		path := "/api/v1" + strings.NewReplacer("{id}", "x", "{name}", "x", "{version}", "0.162.0", "{wave}", "0").Replace(w.path)
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + "x" + path[j+1:]
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(w.method, path, nil))
		if rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s: %s %s answered 405", id, w.method, path)
		}
		if rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "api_not_found") {
			t.Errorf("%s: %s %s fell through to the API catch-all", id, w.method, path)
		}
	}
}

// TestEveryRegisteredRouteHasADeclaredAccessDecision enumerates every pattern
// the HTTP server registers (REST, SSE, artifact download, GitOps webhook, OpAMP,
// probes, metrics, UI) and compares its access decision with the expected table.
// A new route without a decision, or with a different one, fails here. Every
// route that requires authentication must answer an anonymous request with 401
// (deny by default), and unknown API paths are 404 problems, never the UI.
func TestEveryRegisteredRouteHasADeclaredAccessDecision(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := routesStore{kv: &routesKV{m: map[string][]byte{}}}
	authSvc, err := authn.New(context.Background(), config.AuthConfig{SessionTTL: time.Hour}, config.ServerConfig{}, config.OpAMPConfig{},
		authn.Deps{Store: st, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	hub := live.New(eventbus.NewMemory("x", 0), nil, quiet)
	modules := []api.Module{authSvc.Module(), hub.Module()}
	modules = append(modules, coreapi.New(coreapi.Deps{Logger: quiet})...)
	syncer, err := gitops.NewSyncer(config.GitOpsConfig{}, gitops.Deps{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.NewCache(config.ArtifactsConfig{Dir: t.TempDir()}, artifact.Deps{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	modules = append(modules, fleetapi.New(fleetapi.Deps{Logger: quiet, Syncer: syncer, Artifacts: artifacts})...)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv, err := httpserver.New(config.ServerConfig{}, httpserver.Deps{
		Auth: authSvc, Modules: modules, Logger: quiet, OpAMPPath: "/v1/opamp", OpAMP: ok,
		UI: ok, Licenses: ok, Metrics: ok, Ready: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]httpserver.RouteDecision{}
	for _, d := range srv.Routes() {
		if d.Kind == "" {
			t.Errorf("%s has no access decision", d.Pattern)
		}
		if _, dup := got[d.Pattern]; dup {
			t.Errorf("%s registered twice", d.Pattern)
		}
		got[d.Pattern] = d
	}

	// infrastructure routes outside /api/v1
	infra := map[string]string{
		"/v1/opamp":      httpserver.DecisionAgent,
		"GET /healthz":   httpserver.DecisionPublic,
		"GET /readyz":    httpserver.DecisionPublic,
		"GET /metrics":   httpserver.DecisionMetrics,
		"GET /licenses":  httpserver.DecisionPublic, // project license and third-party notices
		"GET /licenses/": httpserver.DecisionPublic,
		"/":              httpserver.DecisionUI,
		"/api/v1/":       httpserver.DecisionPublic, // JSON 404 problem for unknown API paths
	}
	for p, kind := range infra {
		if d, found := got[p]; !found || d.Kind != kind {
			t.Errorf("infrastructure route %q: decision %+v, want %s", p, d, kind)
		}
	}
	// the only REST routes without a permission
	wantPublic := map[string]bool{
		"GET /api/v1/auth/config": true, "GET /api/v1/auth/login": true, "GET /api/v1/auth/callback": true, "POST /api/v1/auth/dev-login": true,
		"POST /api/v1/auth/bootstrap-login": true,
	}
	wantCustom := map[string]bool{
		"POST /api/v1/gitops/webhook":                   true, // shared secret / HMAC signature
		"GET /api/v1/artifacts/{id}/content/{filename}": true, // signed download token
	}
	for p, d := range got {
		if _, isInfra := infra[p]; isInfra {
			continue
		}
		switch d.Kind {
		case httpserver.DecisionPublic:
			if !wantPublic[p] {
				t.Errorf("%s is public: not in the list of intended public routes", p)
			}
		case httpserver.DecisionCustom:
			if !wantCustom[p] {
				t.Errorf("%s authenticates itself: not in the list of intended custom routes", p)
			}
		case httpserver.DecisionPermission:
			if d.Permission == "" {
				t.Errorf("%s requires authentication without a permission", p)
			}
		default:
			t.Errorf("%s has the unexpected decision %q", p, d.Kind)
		}
	}
	for p := range wantPublic {
		if got[p].Kind != httpserver.DecisionPublic {
			t.Errorf("%s must be public (login flow), is %q", p, got[p].Kind)
		}
	}
	for p := range wantCustom {
		if got[p].Kind != httpserver.DecisionCustom {
			t.Errorf("%s must authenticate itself, is %q", p, got[p].Kind)
		}
	}

	// deny by default: an anonymous request to a permission route is 401
	checked := 0
	for p, d := range got {
		if d.Kind != httpserver.DecisionPermission {
			continue
		}
		method, path, _ := strings.Cut(p, " ")
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + "x" + path[j+1:]
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s: status %d, want 401", p, rec.Code)
		}
		checked++
	}
	if checked < 50 {
		t.Errorf("only %d authenticated routes were checked", checked)
	}
	// unknown API paths and methods never reach the UI
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/definitely/unknown", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "problem+json") {
		t.Errorf("unknown API path: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// csrfAuth authenticates the bearer "session" as an operator cookie session and
// accepts only the CSRF token "good" (like the double-submit check of authn).
type csrfAuth struct{ role domain.Role }

func (a csrfAuth) Authenticate(r *http.Request) (*authz.Principal, error) {
	if r.Header.Get("Cookie") == "" {
		return nil, domain.ErrUnauthorized
	}
	return &authz.Principal{Type: authz.PrincipalUser, ID: "u1", Name: "u1", Role: a.role, AuthMethod: "local"}, nil
}

func (csrfAuth) CheckCSRF(r *http.Request, _ *authz.Principal) error {
	if r.Header.Get("X-CSRF-Token") != "good" {
		return domain.ErrForbidden
	}
	return nil
}

// The two operations that change the target version of a fleet sit behind the
// session CSRF check and the permission fleets:set_version like every other
// mutating route.
func TestVersionSelectionRoutesRequireCSRFAndPermission(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	syncer, err := gitops.NewSyncer(config.GitOpsConfig{}, gitops.Deps{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifact.NewCache(config.ArtifactsConfig{Dir: t.TempDir()}, artifact.Deps{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	modules := fleetapi.New(fleetapi.Deps{Logger: quiet, Syncer: syncer, Artifacts: artifacts})
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator} {
		srv, err := httpserver.New(config.ServerConfig{}, httpserver.Deps{Auth: csrfAuth{role: role}, Modules: modules, Logger: quiet})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"/api/v1/fleets/edge-nl/actions/select-version", "/api/v1/fleets/edge-nl/actions/clear-version-selection"} {
			do := func(headers map[string]string) int {
				req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{"version":"0.162.0"}`))
				req.Header.Set("Content-Type", "application/json")
				for k, v := range headers {
					req.Header.Set(k, v)
				}
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				return rec.Code
			}
			if got := do(nil); got != http.StatusUnauthorized {
				t.Errorf("%s anonymous: %d, want 401", p, got)
			}
			if got := do(map[string]string{"Cookie": "s=1"}); got != http.StatusForbidden {
				t.Errorf("%s %s without CSRF token: %d, want 403", role, p, got)
			}
			got := do(map[string]string{"Cookie": "s=1", "X-CSRF-Token": "good"})
			switch role {
			case domain.RoleViewer:
				if got != http.StatusForbidden {
					t.Errorf("%s %s: %d, want 403 (no fleets:set_version)", role, p, got)
				}
			default:
				if got == http.StatusForbidden || got == http.StatusUnauthorized {
					t.Errorf("%s %s: %d, an operator may call it", role, p, got)
				}
			}
		}
	}
}
