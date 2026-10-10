// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

const effectiveWithSecrets = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  otlphttp/backend:
    endpoint: https://user:hunter2@collector.example.com:4318
    headers:
      Authorization: Bearer s3cr3t-bearer-value
      X-Scope-OrgID: tenant-7
    auth:
      authenticator: basicauth/client
  prometheusremotewrite:
    endpoint: https://mimir.example.com/api/v1/push
    external_labels:
      api_key: abcdef-api-key
extensions:
  basicauth/client:
    client_auth:
      username: svc-fleet
      password: SuperSecretPassword
  bearertokenauth:
    token: eyJhbGciOiJIUzI1NiJ9.payload.sig
    filename: /etc/tokens/bearer
  oauth2client:
    client_id: fleet
    client_secret: oauth-client-secret
    token_url: https://login.example.com/token
service:
  extensions: [basicauth/client]
  telemetry:
    logs:
      level: info
`

var secretValues = []string{"hunter2", "s3cr3t-bearer-value", "abcdef-api-key", "SuperSecretPassword", "eyJhbGciOiJIUzI1NiJ9.payload.sig", "oauth-client-secret"}

func TestEffectiveConfigIsRedactedForEveryRole(t *testing.T) {
	e := newEnv(t, nil)
	id := e.seed(seed{host: "edge-1", version: "0.162.0", online: true})
	ctx := context.Background()
	body := []byte(effectiveWithSecrets)
	h := domain.SHA256(body)
	if err := e.db.Configs().PutEffective(ctx, domain.EffectiveConfig{Hash: h, Body: body, ContentType: "text/yaml", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Agents().SetEffectiveConfig(ctx, id, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"viewer", "operator", "admin"} {
		r := e.do(role, "GET", "/agents/"+id.String()+"/effective-config", nil)
		var doc dto.ConfigDocument
		r.into(&doc)
		if r.Code != 200 || !doc.Redacted || doc.Hash.String() != h.String() {
			t.Fatalf("%s: %d redacted=%v", role, r.Code, doc.Redacted)
		}
		for _, secret := range secretValues {
			if strings.Contains(doc.Body, secret) || strings.Contains(string(r.Body), secret) {
				t.Errorf("%s: secret %q is visible in the response", role, secret)
			}
		}
		// structure and non-secret values stay readable
		for _, keep := range []string{"endpoint: 0.0.0.0:4317", "collector.example.com:4318", "token_url: https://login.example.com/token",
			"filename: /etc/tokens/bearer", "level: info", "client_id: fleet", "[REDACTED]"} {
			if !strings.Contains(doc.Body, keep) {
				t.Errorf("%s: %q missing in\n%s", role, keep, doc.Body)
			}
		}
	}
}

func TestRedactEffectiveConfig(t *testing.T) {
	out, changed := coreapi.RedactEffectiveConfig([]byte(effectiveWithSecrets))
	if !changed {
		t.Fatal("secrets exist: changed must be true")
	}
	for _, secret := range secretValues {
		if strings.Contains(string(out), secret) {
			t.Errorf("secret %q survived", secret)
		}
	}
	// a config without secrets is returned byte for byte
	plain := []byte("exporters:\n  debug: {}\n# comment stays\nservice:\n  pipelines: {}\n")
	got, changed := coreapi.RedactEffectiveConfig(plain)
	if changed || string(got) != string(plain) {
		t.Fatalf("plain config modified: %v %q", changed, got)
	}
	// environment references are not secrets themselves
	ref, changed := coreapi.RedactEffectiveConfig([]byte("extensions:\n  basicauth:\n    client_auth:\n      password: ${env:PASSWORD}\n"))
	if changed || !strings.Contains(string(ref), "${env:PASSWORD}") {
		t.Fatalf("env reference: %v %q", changed, ref)
	}
	// JSON is YAML
	js, _ := coreapi.RedactEffectiveConfig([]byte(`{"exporters":{"x":{"headers":{"api-key":"zzz-key"},"password":"pw-json"}}}`))
	if strings.Contains(string(js), "zzz-key") || strings.Contains(string(js), "pw-json") {
		t.Fatalf("json: %s", js)
	}
	// not parseable: line based fallback
	garbage, changed := coreapi.RedactEffectiveConfig([]byte("\tbroken: [\npassword: fallback-secret\napi_key = abc\nurl: https://u:p4ss@host/x\n"))
	if !changed || strings.Contains(string(garbage), "fallback-secret") || strings.Contains(string(garbage), "p4ss") || strings.Contains(string(garbage), "abc") {
		t.Fatalf("fallback: %q", garbage)
	}
}

// REST-3: bounded filter inputs and offsets.
func TestListInputsAreBounded(t *testing.T) {
	e := newEnv(t, nil)
	e.seed(seed{host: "a", version: "0.162.0"})
	cases := map[string]int{
		"/agents?q=" + strings.Repeat("x", coreapi.MaxQueryLength):   200,
		"/agents?q=" + strings.Repeat("x", coreapi.MaxQueryLength+1): 400,
		"/agents?offset=100000": 200,
		"/agents?offset=100001": 400,
		"/agents?" + repeatParam("fleet", coreapi.MaxFilterValues+1):     400,
		"/agents?" + repeatParam("fleet", coreapi.MaxFilterValues):       200,
		"/agents?" + repeatParam("attr", coreapi.MaxAttrFilters+1, "=v"): 400,
		"/agents?" + repeatParam("attr", coreapi.MaxAttrFilters, "=v"):   200,
		"/agents/facets?" + repeatParam("os", coreapi.MaxFilterValues+1): 400,
		"/agents?limit=501": 400,
	}
	for path, want := range cases {
		if r := e.get(path); r.Code != want {
			t.Errorf("%.80s: %d, want %d", path, r.Code, want)
		}
	}
}

func repeatParam(name string, n int, suffix ...string) string {
	parts := make([]string, n)
	for i := range parts {
		v := fmt.Sprint("v", i)
		if len(suffix) > 0 {
			v = fmt.Sprint("k", i) + suffix[0]
			parts[i] = name + "=" + v
			continue
		}
		parts[i] = name + "=" + v
	}
	return strings.Join(parts, "&")
}

// AC-05: only an admin can clear the credential binding of an agent.
func TestReenrollAgentClearsTheBinding(t *testing.T) {
	e := newEnv(t, nil)
	id := e.seed(seed{host: "edge-1", version: "0.162.0"})
	tok := uuid.New()
	ctx := context.Background()
	if err := e.db.Agents().SetCredentialBinding(ctx, id, &tok, "edge-1.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"viewer", "operator"} {
		if r := e.do(role, "POST", "/agents/"+id.String()+"/actions/reenroll", nil); r.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", role, r.Code)
		}
	}
	if a, _ := e.db.Agents().Get(ctx, id); a.EnrollmentTokenID == nil || a.ClientCertSubject == "" {
		t.Fatal("binding must be unchanged")
	}
	r := e.do("admin", "POST", "/agents/"+id.String()+"/actions/reenroll", nil)
	if r.Code != http.StatusAccepted {
		t.Fatalf("admin: %d %s", r.Code, r.Body)
	}
	a, _ := e.db.Agents().Get(ctx, id)
	if a.EnrollmentTokenID != nil || a.ClientCertSubject != "" {
		t.Fatalf("binding not cleared: %+v", a)
	}
	evs, _, _ := e.db.Audit().List(ctx, store.AuditFilter{Action: "agent.reenroll"})
	if len(evs) != 1 || evs[0].TargetID != id.String() || evs[0].Details["previousTokenId"] != tok.String() {
		t.Fatalf("audit: %+v", evs)
	}
	if r := e.do("admin", "POST", "/agents/"+uuid.NewString()+"/actions/reenroll", nil); r.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", r.Code)
	}
}

// AUTH-AUDIT-1: saved filter changes are audited.
func TestSavedFilterChangesAreAudited(t *testing.T) {
	e := newEnv(t, nil)
	r := e.do("user-operator", "POST", "/saved-filters", map[string]any{"name": "offline", "query": map[string]any{"status": "offline"}})
	if r.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	var f domain.SavedFilter
	r.into(&f)
	if r := e.do("user-operator", "DELETE", "/saved-filters/"+f.ID.String(), nil); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", r.Code)
	}
	if r := e.do("user-operator", "DELETE", "/saved-filters/"+uuid.NewString(), nil); r.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: %d", r.Code)
	}
	ctx := context.Background()
	created, _, _ := e.db.Audit().List(ctx, store.AuditFilter{Action: "saved_filter.create"})
	deleted, _, _ := e.db.Audit().List(ctx, store.AuditFilter{Action: "saved_filter.delete"})
	if len(created) != 1 || created[0].TargetID != f.ID.String() || created[0].Result != "success" {
		t.Fatalf("create audit: %+v", created)
	}
	if len(deleted) != 2 {
		t.Fatalf("delete audit (success and failure): %+v", deleted)
	}
}

// REST-4: unavailable errors do not leak their cause.
func TestUnavailableErrorsAreGeneric(t *testing.T) {
	e := newEnv(t, nil)
	e.db.Close() // every query now fails with an internal cause
	r := e.get("/agents")
	if r.Code < 500 {
		t.Fatalf("status %d", r.Code)
	}
	var p api.Problem
	r.into(&p)
	if strings.Contains(p.Detail, "closed pool") || strings.Contains(p.Detail, "pgx") || strings.Contains(p.Detail, "postgres") {
		t.Fatalf("the cause leaks: %q", p.Detail)
	}
}
