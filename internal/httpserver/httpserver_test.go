// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/myriarch/myriarch"
	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/licenses"
)

type fakeAuth struct {
	byCookie map[string]*authz.Principal // session cookie value -> principal
	byBearer map[string]*authz.Principal
	csrf     map[string]string // session cookie -> csrf token
	err      error
}

func (f *fakeAuth) Authenticate(r *http.Request) (*authz.Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if p, ok := f.byBearer[strings.TrimPrefix(h, "Bearer ")]; ok {
			return p, nil
		}
		return nil, fmt.Errorf("%w: bad token", domain.ErrUnauthorized)
	}
	if c, err := r.Cookie("sess"); err == nil {
		if p, ok := f.byCookie[c.Value]; ok {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: no credentials", domain.ErrUnauthorized)
}

func (f *fakeAuth) CheckCSRF(r *http.Request, p *authz.Principal) error {
	if p.AuthMethod == "api_token" {
		return nil
	}
	c, _ := r.Cookie("sess")
	if c == nil || r.Header.Get("X-CSRF-Token") == "" || r.Header.Get("X-CSRF-Token") != f.csrf[c.Value] {
		return fmt.Errorf("%w: csrf token missing or invalid", domain.ErrForbidden)
	}
	return nil
}

var (
	viewer   = &authz.Principal{Type: authz.PrincipalUser, ID: "u-viewer", Name: "V", Role: domain.RoleViewer, AuthMethod: "local"}
	operator = &authz.Principal{Type: authz.PrincipalUser, ID: "u-op", Name: "O", Role: domain.RoleOperator, AuthMethod: "local"}
	admin    = &authz.Principal{Type: authz.PrincipalUser, ID: "u-admin", Name: "A", Role: domain.RoleAdmin, AuthMethod: "oidc"}
	apiTok   = &authz.Principal{Type: authz.PrincipalToken, ID: "t-1", Name: "ci", Role: domain.RoleOperator, AuthMethod: "api_token"}
)

func newFakeAuth() *fakeAuth {
	return &fakeAuth{
		byCookie: map[string]*authz.Principal{"v": viewer, "o": operator, "a": admin},
		byBearer: map[string]*authz.Principal{"myra_tok": apiTok},
		csrf:     map[string]string{"v": "cv", "o": "co", "a": "ca"},
	}
}

func echoPrincipal(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	name := "none"
	if p != nil {
		name = p.ID
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"principal": name, "id": r.PathValue("id")})
}

func testModules() []api.Module {
	return []api.Module{
		api.ModuleFunc{ModuleName: "t", RoutesFunc: func() []api.Route {
			return []api.Route{
				{Method: "GET", Pattern: "/things", OperationID: "listThings", Permission: authz.PermRead, Handler: echoPrincipal},
				{Method: "GET", Pattern: "/things/{id}", OperationID: "getThing", Permission: authz.PermRead, Handler: echoPrincipal},
				{Method: "GET", Pattern: "/things/special", OperationID: "getSpecial", Permission: authz.PermRead, Handler: func(w http.ResponseWriter, r *http.Request) {
					api.WriteJSON(w, 200, map[string]string{"which": "special"})
				}},
				{Method: "POST", Pattern: "/things", OperationID: "createThing", Permission: authz.PermAgentsOperate, Handler: func(w http.ResponseWriter, r *http.Request) {
					var v map[string]any
					if err := api.DecodeJSON(r, &v); err != nil {
						api.WriteError(w, r, err)
						return
					}
					api.WriteJSON(w, http.StatusCreated, v)
				}},
				{Method: "DELETE", Pattern: "/things/{id}", OperationID: "deleteThing", Permission: authz.PermTokensManage, Handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }},
				{Method: "GET", Pattern: "/public", OperationID: "pub", Auth: api.AuthPublic, Handler: echoPrincipal},
				{Method: "POST", Pattern: "/public", OperationID: "pubPost", Auth: api.AuthPublic, Handler: echoPrincipal},
				{Method: "POST", Pattern: "/hook", OperationID: "hook", Auth: api.AuthCustom, Handler: func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Secret") != "s3" {
						api.WriteError(w, r, domain.ErrUnauthorized)
						return
					}
					w.WriteHeader(202)
				}},
				{Method: "GET", Pattern: "/panic", OperationID: "boom", Permission: authz.PermRead, Handler: func(w http.ResponseWriter, r *http.Request) { panic("kaboom") }},
				{Method: "POST", Pattern: "/small", OperationID: "small", Permission: authz.PermAgentsOperate, MaxBodyBytes: 16, Handler: func(w http.ResponseWriter, r *http.Request) {
					_, err := io.ReadAll(r.Body)
					if err != nil {
						api.WriteProblem(w, r, api.Problem{Status: http.StatusRequestEntityTooLarge, Code: api.ErrorCodeBadRequest, Detail: err.Error()})
						return
					}
					w.WriteHeader(204)
				}},
				{Method: "GET", Pattern: "/stream", OperationID: "stream", Permission: authz.PermRead, Streaming: true, Handler: func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					rc := http.NewResponseController(w)
					fmt.Fprint(w, "data: 1\n\n")
					if err := rc.Flush(); err != nil {
						fmt.Fprint(w, "flush failed")
					}
				}},
			}
		}},
	}
}

func newTestServer(t *testing.T, mods ...api.Module) (*Server, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	if mods == nil {
		mods = testModules()
	}
	s, err := New(config.ServerConfig{ListenAddr: "127.0.0.1:0", TrustedProxies: []string{"10.0.0.0/8"}}, Deps{
		Auth: newFakeAuth(), Modules: mods, OpAMPPath: "/v1/opamp",
		OpAMP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-OpAMP", "yes")
			w.WriteHeader(http.StatusTeapot)
		}),
		UI:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>ui "+r.URL.Path+"</html>") }),
		Ready:   func(ctx context.Context) error { return nil },
		Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), Registerer: reg,
		Logger: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, reg
}

type resp struct {
	*httptest.ResponseRecorder
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	for k, v := range hdr {
		if k == "Cookie" {
			req.Header.Add("Cookie", v)
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) api.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != api.ProblemContentType {
		t.Fatalf("content type %q, want %s (body %s)", ct, api.ProblemContentType, rec.Body.String())
	}
	var p api.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRoutingAndProblemModel(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	auth := map[string]string{"Cookie": "sess=v"}

	rec := do(t, h, "GET", "/api/v1/things", auth, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"u-viewer"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(api.RequestIDHeader) == "" {
		t.Fatal("every response carries a request id")
	}
	// literal pattern wins over {id}
	rec = do(t, h, "GET", "/api/v1/things/special", auth, "")
	if !strings.Contains(rec.Body.String(), "special") {
		t.Fatalf("specific pattern must win: %s", rec.Body.String())
	}
	rec = do(t, h, "GET", "/api/v1/things/abc", auth, "")
	if !strings.Contains(rec.Body.String(), `"id":"abc"`) {
		t.Fatalf("path value: %s", rec.Body.String())
	}

	// unknown API path -> problem+json 404 (not the SPA)
	rec = do(t, h, "GET", "/api/v1/nope", auth, "")
	p := decodeProblem(t, rec)
	if rec.Code != 404 || p.Code != api.ErrorCodeNotFound || p.RequestID == "" {
		t.Fatalf("unknown api path: %d %+v", rec.Code, p)
	}
	// even without authentication (no information leak either way)
	rec = do(t, h, "GET", "/api/v1/nope", nil, "")
	if rec.Code != 404 {
		t.Fatalf("unauthenticated unknown path: %d", rec.Code)
	}
	// wrong method on a known path -> 405 with Allow
	rec = do(t, h, "PUT", "/api/v1/things", auth, "")
	if rec.Code != 405 {
		t.Fatalf("method not allowed: %d %s", rec.Code, rec.Body.String())
	}
	if a := rec.Header().Get("Allow"); !strings.Contains(a, "GET") || !strings.Contains(a, "POST") {
		t.Fatalf("Allow header: %q", a)
	}
	_ = decodeProblem(t, rec)

	// SPA fallback for everything else, healthz/readyz/metrics
	rec = do(t, h, "GET", "/agents/123", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui /agents/123") {
		t.Fatalf("ui: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, "GET", "/healthz", nil, ""); rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/readyz", nil, ""); rec.Code != 200 {
		t.Fatalf("readyz: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/metrics", nil, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "myriarch_http_requests_total") {
		t.Fatalf("metrics on the main listener when no separate address is configured: %d %s", rec.Code, rec.Body.String())
	}
	// opamp endpoint: no authentication middleware, any method
	for _, m := range []string{"GET", "POST"} {
		rec = do(t, h, m, "/v1/opamp", nil, "")
		if rec.Code != http.StatusTeapot || rec.Header().Get("X-OpAMP") != "yes" || rec.Header().Get(api.RequestIDHeader) == "" {
			t.Fatalf("opamp %s: %d %v", m, rec.Code, rec.Header())
		}
	}
}

func TestAuthModesCSRFAndPermissions(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()

	// unauthenticated -> 401 with problem code
	rec := do(t, h, "GET", "/api/v1/things", nil, "")
	p := decodeProblem(t, rec)
	if rec.Code != 401 || p.Code != api.ErrorCodeUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthenticated: %d %+v", rec.Code, p)
	}
	rec = do(t, h, "GET", "/api/v1/things", map[string]string{"Authorization": "Bearer wrong"}, "")
	if rec.Code != 401 {
		t.Fatalf("bad bearer: %d", rec.Code)
	}
	// 401 comes before CSRF and permission checks, also for mutating requests
	rec = do(t, h, "POST", "/api/v1/things", nil, `{}`)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated POST: %d", rec.Code)
	}
	// bearer token needs no CSRF header
	rec = do(t, h, "POST", "/api/v1/things", map[string]string{"Authorization": "Bearer myra_tok", "Content-Type": "application/json"}, `{"a":1}`)
	if rec.Code != 201 {
		t.Fatalf("api token POST: %d %s", rec.Code, rec.Body.String())
	}

	// cookie session: CSRF required for mutating requests, not for reads
	op := map[string]string{"Cookie": "sess=o"}
	rec = do(t, h, "POST", "/api/v1/things", op, `{"a":1}`)
	p = decodeProblem(t, rec)
	if rec.Code != 403 || p.Code != api.ErrorCodeCSRF {
		t.Fatalf("missing csrf: %d %+v", rec.Code, p)
	}
	rec = do(t, h, "POST", "/api/v1/things", map[string]string{"Cookie": "sess=o", "X-CSRF-Token": "wrong"}, `{"a":1}`)
	if p = decodeProblem(t, rec); rec.Code != 403 || p.Code != api.ErrorCodeCSRF {
		t.Fatalf("wrong csrf: %d %+v", rec.Code, p)
	}
	rec = do(t, h, "POST", "/api/v1/things", map[string]string{"Cookie": "sess=o", "X-CSRF-Token": "co"}, `{"a":1}`)
	if rec.Code != 201 {
		t.Fatalf("valid csrf: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "GET", "/api/v1/things", op, "")
	if rec.Code != 200 {
		t.Fatalf("GET needs no csrf: %d", rec.Code)
	}
	// CSRF is checked before permission (a viewer without token sees csrf_failed)
	rec = do(t, h, "POST", "/api/v1/things", map[string]string{"Cookie": "sess=v"}, `{}`)
	if p = decodeProblem(t, rec); p.Code != api.ErrorCodeCSRF {
		t.Fatalf("csrf before permission: %+v", p)
	}

	// permission: viewer cannot operate, operator cannot manage tokens, admin can
	rec = do(t, h, "POST", "/api/v1/things", map[string]string{"Cookie": "sess=v", "X-CSRF-Token": "cv"}, `{}`)
	if p = decodeProblem(t, rec); rec.Code != 403 || p.Code != api.ErrorCodeForbidden {
		t.Fatalf("viewer POST: %d %+v", rec.Code, p)
	}
	rec = do(t, h, "DELETE", "/api/v1/things/x", map[string]string{"Cookie": "sess=o", "X-CSRF-Token": "co"}, "")
	if rec.Code != 403 {
		t.Fatalf("operator DELETE (admin permission): %d", rec.Code)
	}
	rec = do(t, h, "DELETE", "/api/v1/things/x", map[string]string{"Cookie": "sess=a", "X-CSRF-Token": "ca"}, "")
	if rec.Code != 204 {
		t.Fatalf("admin DELETE: %d", rec.Code)
	}
	rec = do(t, h, "DELETE", "/api/v1/things/x", map[string]string{"Authorization": "Bearer myra_tok"}, "")
	if rec.Code != 403 {
		t.Fatalf("operator token must not delete: %d", rec.Code)
	}

	// public: no authentication, no CSRF, no principal in the context
	rec = do(t, h, "GET", "/api/v1/public", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"none"`) {
		t.Fatalf("public: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "POST", "/api/v1/public", map[string]string{"Content-Type": "application/json"}, "")
	if rec.Code != 200 {
		t.Fatalf("public POST: %d", rec.Code)
	}
	// custom: the handler authenticates itself; no CSRF, no principal
	rec = do(t, h, "POST", "/api/v1/hook", nil, "")
	if rec.Code != 401 {
		t.Fatalf("custom without secret: %d", rec.Code)
	}
	rec = do(t, h, "POST", "/api/v1/hook", map[string]string{"X-Secret": "s3"}, "")
	if rec.Code != 202 {
		t.Fatalf("custom with secret: %d", rec.Code)
	}

	// authentication infrastructure failure -> 503, not 401/500
	fa := newFakeAuth()
	fa.err = fmt.Errorf("%w: db down", domain.ErrUnavailable)
	s2, err := New(config.ServerConfig{ListenAddr: ":0"}, Deps{Auth: fa, Modules: testModules()})
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, s2.Handler(), "GET", "/api/v1/things", map[string]string{"Cookie": "sess=v"}, ""); rec.Code != 503 {
		t.Fatalf("auth backend down: %d", rec.Code)
	}
}

func TestBodyLimitRecoveryAndStreaming(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	hdr := map[string]string{"Cookie": "sess=o", "X-CSRF-Token": "co", "Content-Type": "application/json"}

	rec := do(t, h, "POST", "/api/v1/small", hdr, strings.Repeat("x", 100))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("route body limit: %d", rec.Code)
	}
	rec = do(t, h, "POST", "/api/v1/small", hdr, "ok")
	if rec.Code != 204 {
		t.Fatalf("small body: %d", rec.Code)
	}
	// default limit (1 MiB) applies to JSON decoding
	big := `{"k":"` + strings.Repeat("a", DefaultBodyLimit+10) + `"}`
	rec = do(t, h, "POST", "/api/v1/things", hdr, big)
	if rec.Code != 400 {
		t.Fatalf("default body limit: %d", rec.Code)
	}

	rec = do(t, h, "GET", "/api/v1/panic", map[string]string{"Cookie": "sess=v"}, "")
	p := decodeProblem(t, rec)
	if rec.Code != 500 || p.Code != api.ErrorCodeInternal || strings.Contains(rec.Body.String(), "kaboom") {
		t.Fatalf("panic must become a generic 500 problem: %d %s", rec.Code, rec.Body.String())
	}
	// the server keeps working
	if rec := do(t, h, "GET", "/api/v1/things", map[string]string{"Cookie": "sess=v"}, ""); rec.Code != 200 {
		t.Fatal("server must survive a panic")
	}

	// the wrapper passes Flush through to http.ResponseController (SSE)
	rec = do(t, h, "GET", "/api/v1/stream", map[string]string{"Cookie": "sess=v"}, "")
	if !strings.Contains(rec.Body.String(), "data: 1") || strings.Contains(rec.Body.String(), "flush failed") || !rec.Flushed {
		t.Fatalf("flush through the middleware: flushed=%v body=%q", rec.Flushed, rec.Body.String())
	}
}

func TestRouteValidationErrors(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) {}
	mk := func(routes ...api.Route) []api.Module {
		return []api.Module{api.ModuleFunc{ModuleName: "m", RoutesFunc: func() []api.Route { return routes }}}
	}
	cfg := config.ServerConfig{ListenAddr: ":0"}
	deps := func(mods []api.Module) Deps { return Deps{Auth: newFakeAuth(), Modules: mods} }
	tests := []struct {
		name string
		mods []api.Module
		want string
	}{
		{"duplicate operation id", mk(
			api.Route{Method: "GET", Pattern: "/a", OperationID: "x", Permission: authz.PermRead, Handler: ok},
			api.Route{Method: "GET", Pattern: "/b", OperationID: "x", Permission: authz.PermRead, Handler: ok}), "duplicate operationId"},
		{"duplicate across modules", append(mk(api.Route{Method: "GET", Pattern: "/a", OperationID: "x", Permission: authz.PermRead, Handler: ok}),
			mk(api.Route{Method: "GET", Pattern: "/b", OperationID: "x", Permission: authz.PermRead, Handler: ok})...), "duplicate operationId"},
		{"pattern conflict", mk(
			api.Route{Method: "GET", Pattern: "/a/{x}", OperationID: "1", Permission: authz.PermRead, Handler: ok},
			api.Route{Method: "GET", Pattern: "/a/{y}", OperationID: "2", Permission: authz.PermRead, Handler: ok}), "cannot register"},
		{"same route twice", mk(
			api.Route{Method: "GET", Pattern: "/a", OperationID: "1", Permission: authz.PermRead, Handler: ok},
			api.Route{Method: "GET", Pattern: "/a", OperationID: "2", Permission: authz.PermRead, Handler: ok}), "cannot register"},
		{"ambiguous patterns", mk(
			api.Route{Method: "GET", Pattern: "/a/{x}/b", OperationID: "1", Permission: authz.PermRead, Handler: ok},
			api.Route{Method: "GET", Pattern: "/a/b/{y}", OperationID: "2", Permission: authz.PermRead, Handler: ok}), "cannot register"},
		{"missing permission", mk(api.Route{Method: "GET", Pattern: "/a", OperationID: "1", Handler: ok}), "without permission"},
		{"missing operation id", mk(api.Route{Method: "GET", Pattern: "/a", Permission: authz.PermRead, Handler: ok}), "operationId is required"},
		{"nil handler", mk(api.Route{Method: "GET", Pattern: "/a", OperationID: "1", Permission: authz.PermRead}), "handler is nil"},
		{"relative pattern", mk(api.Route{Method: "GET", Pattern: "a", OperationID: "1", Permission: authz.PermRead, Handler: ok}), "must start with"},
		{"no method", mk(api.Route{Pattern: "/a", OperationID: "1", Permission: authz.PermRead, Handler: ok}), "invalid method"},
		{"invalid pattern syntax", mk(api.Route{Method: "GET", Pattern: "/a/{", OperationID: "1", Permission: authz.PermRead, Handler: ok}), "cannot register"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(cfg, deps(tc.mods))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	// authenticated route without Authenticator
	if _, err := New(cfg, Deps{Modules: mk(api.Route{Method: "GET", Pattern: "/a", OperationID: "1", Permission: authz.PermRead, Handler: ok})}); err == nil {
		t.Fatal("authenticated routes need an Authenticator")
	}
	// public-only modules work without one
	if _, err := New(cfg, Deps{Modules: mk(api.Route{Method: "GET", Pattern: "/a", OperationID: "1", Auth: api.AuthPublic, Handler: ok})}); err != nil {
		t.Fatalf("public-only: %v", err)
	}
	if _, err := New(config.ServerConfig{TrustedProxies: []string{"junk"}}, Deps{}); err == nil {
		t.Fatal("invalid trusted proxy")
	}
}

func TestReadyz(t *testing.T) {
	notReady := errors.New("database ping failed")
	s, err := New(config.ServerConfig{ListenAddr: ":0"}, Deps{Ready: func(ctx context.Context) error { return notReady }})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s.Handler(), "GET", "/readyz", nil, "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "not ready") {
		t.Fatalf("readyz: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "database ping failed") {
		t.Fatalf("readyz must not leak the cause to unauthenticated callers: %s", rec.Body.String())
	}
	if rec := do(t, s.Handler(), "GET", "/healthz", nil, ""); rec.Code != 200 {
		t.Fatal("healthz must stay green when not ready")
	}
	// no UI configured -> plain 404 for unknown non-API paths
	if rec := do(t, s.Handler(), "GET", "/whatever", nil, ""); rec.Code != 404 {
		t.Fatalf("no ui: %d", rec.Code)
	}
}

func TestRealIPAndRequestID(t *testing.T) {
	var seen string
	mod := api.ModuleFunc{ModuleName: "ip", RoutesFunc: func() []api.Route {
		return []api.Route{{Method: "GET", Pattern: "/ip", OperationID: "ip", Auth: api.AuthPublic, Handler: func(w http.ResponseWriter, r *http.Request) {
			seen = r.RemoteAddr + "|" + r.Header.Get(api.RequestIDHeader)
		}}}
	}}
	s, _ := newTestServer(t, mod)
	req := httptest.NewRequest("GET", "/api/v1/ip", nil)
	req.RemoteAddr = "10.1.2.3:555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	req.Header.Set(api.RequestIDHeader, "client-id-1")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if !strings.HasPrefix(seen, "198.51.100.9:") || !strings.HasSuffix(seen, "|client-id-1") || rec.Header().Get(api.RequestIDHeader) != "client-id-1" {
		t.Fatalf("seen %q", seen)
	}
	// an invalid client supplied id is replaced
	req = httptest.NewRequest("GET", "/api/v1/ip", nil)
	req.Header.Set(api.RequestIDHeader, "bad id\nwith newline")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if id := rec.Header().Get(api.RequestIDHeader); id == "" || strings.ContainsAny(id, " \n") {
		t.Fatalf("request id %q", id)
	}
}

func TestMetricsRecorded(t *testing.T) {
	s, reg := newTestServer(t)
	do(t, s.Handler(), "GET", "/api/v1/things", map[string]string{"Cookie": "sess=v"}, "")
	do(t, s.Handler(), "GET", "/api/v1/things", nil, "")
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_http_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			lbl := map[string]string{}
			for _, l := range m.GetLabel() {
				lbl[l.GetName()] = l.GetValue()
			}
			counts[lbl["route"]+"|"+lbl["method"]+"|"+lbl["code"]] = m.GetCounter().GetValue()
		}
	}
	if counts["/things|GET|200"] != 1 || counts["/things|GET|401"] != 1 {
		t.Fatalf("metrics: %v", counts)
	}
}

// ------------------------------------------------ real listener / hijack ---

func startServer(t *testing.T, cfg config.ServerConfig, deps Deps) (*Server, func()) {
	t.Helper()
	cfg.ListenAddr = "127.0.0.1:0"
	s, err := New(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case <-s.Started():
	case err := <-done:
		t.Fatalf("server did not start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server start timeout")
	}
	return s, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	}
}

func TestHijackThroughMiddlewareAndGracefulShutdown(t *testing.T) {
	reg := prometheus.NewRegistry()
	hijacked := make(chan string, 1)
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: 3 * time.Second, MetricsListenAddr: "127.0.0.1:0"}, Deps{
		OpAMPPath: "/v1/opamp",
		OpAMP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, rw, err := w.(http.Hijacker).Hijack() // gorilla/websocket does exactly this
			if err != nil {
				hijacked <- "error: " + err.Error()
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\nhello")
			_ = rw.Flush()
			hijacked <- "ok"
		}),
		Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), Registerer: reg,
		Ready: func(context.Context) error { return nil },
	})
	defer stop()
	addr := s.Addr().String()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET /v1/opamp HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n"))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, "101") {
		t.Fatalf("expected a 101 through the middleware wrapper, got %q (%v)", line, err)
	}
	if v := <-hijacked; v != "ok" {
		t.Fatalf("hijack: %s", v)
	}

	// metrics only on the separate listener
	resp, err := http.Get("http://" + s.MetricsAddr().String() + "/metrics")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("metrics listener: %v %v", resp, err)
	}
	resp.Body.Close()
	resp, err = http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("/metrics must not be exposed on the main listener when a metrics listener is configured")
	}
}

func TestShutdownWaitsForInflightRequestsThenForcesClose(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	mod := api.ModuleFunc{ModuleName: "slow", RoutesFunc: func() []api.Route {
		return []api.Route{{Method: "GET", Pattern: "/slow", OperationID: "slow", Auth: api.AuthPublic, Handler: func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-release:
				fmt.Fprint(w, "done")
			case <-r.Context().Done():
			}
		}}}
	}}
	s, stop := startServer(t, config.ServerConfig{ShutdownTimeout: 300 * time.Millisecond}, Deps{Modules: []api.Module{mod}})
	go func() { _, _ = http.Get("http://" + s.Addr().String() + "/api/v1/slow") }()
	<-started
	t0 := time.Now()
	stop() // the request never finishes: Run must give up after ShutdownTimeout and close
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	close(release)
}

func TestRunPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s, err := New(config.ServerConfig{ListenAddr: ln.Addr().String()}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("Run must fail fast when the port is taken")
	}
}

func TestSecurityHeaders(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s.Handler(), "GET", "/api/v1/things", map[string]string{"Cookie": "sess=v"}, "")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("api headers: %v", rec.Header())
	}
	rec = do(t, s.Handler(), "GET", "/healthz", nil, "")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff on all responses: %v", rec.Header())
	}
}

func newLicensesServer(t *testing.T, publicURL string) *Server {
	t.Helper()
	s, err := New(config.ServerConfig{PublicURL: publicURL}, Deps{
		Auth: newFakeAuth(), Modules: testModules(), Licenses: licenses.Handler(),
		UI:    http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>ui "+r.URL.Path+"</html>") }),
		Ready: func(ctx context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The license texts are public by design: an anonymous request is served even
// though an authenticator is configured, and the base headers apply.
func TestLicensesAreServedAnonymously(t *testing.T) {
	s := newLicensesServer(t, "https://fleet.example.com")
	h := s.Handler()
	for path, want := range map[string]string{
		"/licenses/LICENSE":             myriarch.License(),
		"/licenses/THIRD_PARTY_NOTICES": myriarch.ThirdPartyNotices(),
	} {
		rec := do(t, h, "GET", path, nil, "")
		if rec.Code != 200 || rec.Body.String() != want {
			t.Fatalf("%s: status %d, body matches the embedded text: %v", path, rec.Code, rec.Body.String() == want)
		}
		hd := rec.Header()
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: base headers missing: %v", path, hd)
		}
		if hd.Get("Strict-Transport-Security") == "" || hd.Get(api.RequestIDHeader) == "" {
			t.Errorf("%s: HSTS or request id missing: %v", path, hd)
		}
		if ct := hd.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
		if hd.Get("X-Frame-Options") != "DENY" || hd.Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" || hd.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: handler headers: %v", path, hd)
		}
		if head := do(t, h, "HEAD", path, nil, ""); head.Code != 200 || head.Body.Len() != 0 {
			t.Errorf("HEAD %s: status %d, %d body bytes", path, head.Code, head.Body.Len())
		}
	}
	rec := do(t, h, "GET", "/licenses", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/licenses/THIRD_PARTY_NOTICES") || strings.Contains(rec.Body.String(), "<html>") {
		t.Fatalf("index: %d %q", rec.Code, rec.Body.String())
	}
	// an unknown name is a 404 of the licenses handler, not the SPA
	rec = do(t, h, "GET", "/licenses/unknown", nil, "")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "<html>") {
		t.Fatalf("unknown license name: %d %q", rec.Code, rec.Body.String())
	}
	// the whole subtree belongs to the licenses handler: a trailing slash is the index, deeper paths are a 404 (not the SPA)
	rec = do(t, h, "GET", "/licenses/", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/licenses/THIRD_PARTY_NOTICES") || strings.Contains(rec.Body.String(), "<html>") {
		t.Fatalf("index with a trailing slash: %d %q", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/licenses/a/b", "/licenses/LICENSE/", "/licenses/LICENSE/x"} {
		rec = do(t, h, "GET", p, nil, "")
		if rec.Code != 404 || strings.Contains(rec.Body.String(), "<html>") {
			t.Fatalf("%s: %d %q, want a 404 of the licenses handler", p, rec.Code, rec.Body.String())
		}
	}
	// the UI catch-all still serves everything else
	rec = do(t, h, "GET", "/settings/system", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui /settings/system") {
		t.Fatalf("ui catch-all: %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "GET", "/licenses-of-things", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui /licenses-of-things") {
		t.Fatalf("a path that only shares the prefix belongs to the UI: %d %q", rec.Code, rec.Body.String())
	}
}

// The new patterns carry a declared decision (the enumeration in
// TestRoutesAreClassified requires a decision for every pattern).
func TestLicensesRoutesAreClassifiedPublic(t *testing.T) {
	s := newLicensesServer(t, "")
	kinds := map[string]string{}
	for _, d := range s.Routes() {
		kinds[d.Pattern] = d.Kind
	}
	for _, p := range []string{"GET /licenses", "GET /licenses/"} {
		if kinds[p] != DecisionPublic {
			t.Errorf("%s: %q, want %q", p, kinds[p], DecisionPublic)
		}
	}
	if kinds["/"] != DecisionUI {
		t.Errorf("the UI catch-all must stay registered: %q", kinds["/"])
	}
	// without a handler no pattern is registered
	plain, _ := newTestServer(t)
	for _, d := range plain.Routes() {
		if strings.HasPrefix(d.Pattern, "GET /licenses") {
			t.Errorf("%s registered without a Licenses handler", d.Pattern)
		}
	}
	if got, want := len(s.MuxPatterns()), len(s.Routes()); got != want {
		t.Errorf("%d mux patterns, %d decisions", got, want)
	}
}
