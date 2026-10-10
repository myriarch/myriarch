// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
)

// AC-13 / HDR-1: baseline headers on every response, HSTS when TLS ends in front of or in myriarchd.
func TestSecurityHeadersHSTSAndFraming(t *testing.T) {
	mk := func(publicURL string) *Server {
		s, err := New(config.ServerConfig{PublicURL: publicURL}, Deps{Auth: newFakeAuth(), Modules: testModules(),
			UI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ui") }), Ready: func(context.Context) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	https := mk("https://fleet.example.com")
	for _, path := range []string{"/api/v1/things", "/healthz", "/readyz", "/ui/page", "/api/v1/nonexistent"} {
		rec := do(t, https.Handler(), "GET", path, map[string]string{"Cookie": "sess=v"}, "")
		h := rec.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: baseline headers missing: %v", path, h)
		}
		if got := h.Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=31536000") || !strings.Contains(got, "includeSubDomains") {
			t.Errorf("%s: HSTS for an https public URL: %q", path, got)
		}
	}
	// API responses forbid framing and caching
	rec := do(t, https.Handler(), "GET", "/api/v1/things", map[string]string{"Cookie": "sess=v"}, "")
	if rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("API headers: %v", rec.Header())
	}
	// plain http deployments (dev) do not send HSTS
	if got := do(t, mk("http://localhost:8080").Handler(), "GET", "/healthz", nil, "").Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS over http: %q", got)
	}
	// myriarchd terminating TLS itself
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	mk("").Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS when myriarchd terminates TLS")
	}
}

// AUTH-CSRF-1: unauthenticated state changes (login) are not accepted cross-site.
func TestPublicMutationsRefuseCrossSiteRequests(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	post := func(hdr map[string]string) int {
		hdr2 := map[string]string{"Content-Type": "application/json"}
		for k, v := range hdr {
			hdr2[k] = v
		}
		return do(t, h, "POST", "/api/v1/public", hdr2, `{}`).Code
	}
	if c := do(t, h, "POST", "/api/v1/public", nil, `{}`).Code; c != http.StatusUnsupportedMediaType {
		t.Fatalf("no Content-Type: %d, want 415", c)
	}
	if c := post(nil); c != 200 {
		t.Fatalf("no browser headers (script): %d", c)
	}
	if c := post(map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}); c != 200 { // httptest host is example.com
		t.Fatalf("same origin: %d", c)
	}
	for name, hdr := range map[string]map[string]string{
		"cross-site fetch": {"Sec-Fetch-Site": "cross-site"},
		"same-site fetch":  {"Sec-Fetch-Site": "same-site"},
		"foreign origin":   {"Origin": "https://evil.example.net"},
		"opaque origin":    {"Origin": "null"},
		"garbage origin":   {"Origin": "::"},
	} {
		if c := post(hdr); c != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, c)
		}
	}
	// safe methods and custom routes are not affected
	if c := do(t, h, "GET", "/api/v1/public", map[string]string{"Origin": "https://evil.example.net"}, "").Code; c != 200 {
		t.Errorf("GET: %d", c)
	}
	// the public URL counts as the own origin (proxies may rewrite the Host header)
	s2, err := New(config.ServerConfig{PublicURL: "https://fleet.example.com"}, Deps{Auth: newFakeAuth(), Modules: testModules()})
	if err != nil {
		t.Fatal(err)
	}
	if c := do(t, s2.Handler(), "POST", "/api/v1/public", map[string]string{"Origin": "https://fleet.example.com", "Content-Type": "application/json"}, `{}`).Code; c != 200 {
		t.Errorf("public url origin: %d", c)
	}
}

// REST-3: API handlers have a deadline, streams do not.
func TestHandlerDeadline(t *testing.T) {
	var apiDeadline, streamDeadline bool
	mods := []api.Module{api.ModuleFunc{ModuleName: "d", RoutesFunc: func() []api.Route {
		return []api.Route{
			{Method: "GET", Pattern: "/d", OperationID: "d", Permission: authz.PermRead, Handler: func(w http.ResponseWriter, r *http.Request) {
				dl, ok := r.Context().Deadline()
				apiDeadline = ok && time.Until(dl) <= RequestTimeout && time.Until(dl) > RequestTimeout-5*time.Second
			}},
			{Method: "GET", Pattern: "/s", OperationID: "s", Permission: authz.PermRead, Streaming: true, Handler: func(w http.ResponseWriter, r *http.Request) {
				_, ok := r.Context().Deadline()
				streamDeadline = ok
			}},
		}
	}}}
	s, _ := newTestServer(t, mods...)
	do(t, s.Handler(), "GET", "/api/v1/d", map[string]string{"Cookie": "sess=v"}, "")
	do(t, s.Handler(), "GET", "/api/v1/s", map[string]string{"Cookie": "sess=v"}, "")
	if !apiDeadline {
		t.Error("non-streaming handlers must run under a deadline")
	}
	if streamDeadline {
		t.Error("streaming handlers must not have a deadline")
	}
}

// LOG-1: the method label cannot be used to create unbounded metric series.
func TestMetricMethodLabelIsBounded(t *testing.T) {
	s, reg := newTestServer(t)
	for i := 0; i < 30; i++ {
		do(t, s.Handler(), fmt.Sprintf("X%dMETHOD", i), "/some/ui/path", nil, "")
	}
	do(t, s.Handler(), "GET", "/some/ui/path", nil, "")
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{}
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_http_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			methods[labelValue(m, "method")] = true
		}
	}
	if !methods["OTHER"] || !methods["GET"] || len(methods) > 3 {
		t.Fatalf("method labels: %v", methods)
	}
	if metricMethod("PROPFIND") != "OTHER" || metricMethod("PATCH") != "PATCH" {
		t.Fatal("metricMethod")
	}
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// AUTH-RBAC-1: infrastructure routes are classified.
func TestRoutesAreClassified(t *testing.T) {
	s, _ := newTestServer(t)
	kinds := map[string]string{}
	for _, d := range s.Routes() {
		kinds[d.Pattern] = d.Kind
		if d.Kind == "" {
			t.Errorf("%s: no decision", d.Pattern)
		}
		if d.Kind == DecisionPermission && d.Permission == "" {
			t.Errorf("%s: authenticated without permission", d.Pattern)
		}
	}
	for p, want := range map[string]string{
		"/v1/opamp": DecisionAgent, "GET /healthz": DecisionPublic, "GET /readyz": DecisionPublic, "/": DecisionUI,
		"GET /metrics": DecisionMetrics, "GET /api/v1/things": DecisionPermission, "POST /api/v1/hook": DecisionCustom, "GET /api/v1/public": DecisionPublic,
	} {
		if kinds[p] != want {
			t.Errorf("%s: %q, want %q", p, kinds[p], want)
		}
	}
	// with a separate metrics listener the main mux does not serve /metrics
	sep, err := New(config.ServerConfig{MetricsListenAddr: ":9090"}, Deps{Metrics: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range sep.Routes() {
		if d.Pattern == "GET /metrics" {
			t.Errorf("metrics must not be served on the main listener when metricsListenAddr is set")
		}
	}
}
