// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/store"
)

// ------------------------------------------------------------------- helpers ----

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testInstallAuth stands in for authn.Service.AuthenticateInstall (tested in internal/authn): it knows the
// tokens of the test and answers the failures with messages that differ, on purpose, so that the tests see
// whether the handler flattens them.
type testInstallAuth struct {
	e       *apiEnv
	mu      sync.Mutex
	secrets map[string]uuid.UUID
	labels  map[uuid.UUID]map[string]string
	fail    error
	calls   int
}

func newTestInstallAuth(e *apiEnv) *testInstallAuth {
	return &testInstallAuth{e: e, secrets: map[string]uuid.UUID{}, labels: map[uuid.UUID]map[string]string{}}
}

const leakMarker = "LEAK-MARKER-DO-NOT-ANSWER"

// newToken creates an enrollment token that the artifact cache can read by ID and returns its secret and ID.
func (a *testInstallAuth) newToken(labels map[string]string, mut ...func(t *domain.EnrollmentToken)) (string, uuid.UUID) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	secret := domain.EnrollmentTokenPrefix + hex.EncodeToString(b)
	tok := domain.EnrollmentToken{Name: "t-" + secret[5:9], Labels: labels, CreatedAt: time.Now()}
	for _, m := range mut {
		m(&tok)
	}
	tok = a.e.tokens.Create(tok)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.secrets[secret] = tok.ID
	a.labels[tok.ID] = labels
	return secret, tok.ID
}

func (a *testInstallAuth) AuthenticateInstall(r *http.Request) (*authn.AgentCredential, error) {
	a.mu.Lock()
	a.calls++
	fail := a.fail
	a.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	h := r.Header.Get("Authorization")
	secret, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return nil, fmt.Errorf("%w: enrollment token required (%s)", domain.ErrUnauthorized, leakMarker)
	}
	a.mu.Lock()
	id, known := a.secrets[secret]
	labels := a.labels[id]
	a.mu.Unlock()
	if !known {
		return nil, fmt.Errorf("%w: unknown token (%s)", domain.ErrUnauthorized, leakMarker)
	}
	tok, err := a.e.tokens.Identity().GetEnrollmentToken(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if tok.RevokedAt != nil {
		return nil, fmt.Errorf("%w: token %s is revoked (%s)", domain.ErrUnauthorized, id, leakMarker)
	}
	if tok.ExpiresAt != nil && !time.Now().Before(*tok.ExpiresAt) {
		return nil, fmt.Errorf("%w: token %s has expired (%s)", domain.ErrUnauthorized, id, leakMarker)
	}
	cp := map[string]string{}
	for k, v := range labels {
		cp[k] = v
	}
	return &authn.AgentCredential{EnrollmentTokenID: &id, Labels: cp}, nil
}

func (a *testInstallAuth) setFail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail = err
}

func (a *testInstallAuth) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// remount builds the service again with changed dependencies and mounts its routes on a new mux.
func (e *apiEnv) remount(mut func(d *Deps)) {
	e.t.Helper()
	d := e.deps
	mut(&d)
	e.deps = d
	e.svc = newService(d)
	e.routes = map[string]api.Route{}
	e.mux = http.NewServeMux()
	for _, m := range e.svc.modules() {
		for _, rt := range m.Routes() {
			e.routes[rt.OperationID] = rt
			e.mount(rt)
		}
	}
}

// datacenterHost is a Linux host at a data center: the fleet datacenter of the test repository (0.162.0, updater).
func datacenterHost(mut ...func(b map[string]any)) map[string]any {
	b := map[string]any{"os": "linux", "arch": "amd64", "attributes": map[string]string{"host.name": "dc-01.example.test", "site.type": "datacenter", "site.id": "dc-01"}}
	for _, m := range mut {
		m(b)
	}
	return b
}

func (e *apiEnv) planRequest(secret string, body any, hdr ...string) resp {
	e.t.Helper()
	h := append([]string{}, hdr...)
	if secret != "" {
		h = append(h, "Authorization", "Bearer "+secret)
	}
	return e.do(http.MethodPost, "/install/plan", "", body, h...)
}

func (e *apiEnv) waitPrepared() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(e.t, e.cache.WaitPrepared(ctx))
}

// readyPlan asks for the plan until the artifacts are prepared. A download that failed once (a transient error of
// the machine) is started again by the next request, because the store of the tests runs on a fake clock and its
// failed record is older than the retry interval of the cache; so one round of waiting is not enough, and the
// test asks again until a deadline.
func (e *apiEnv) readyPlan(secret string, body any, hdr ...string) resp {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	r := e.planRequest(secret, body, hdr...)
	for r.Code == http.StatusServiceUnavailable && time.Now().Before(deadline) {
		e.waitPrepared()
		r = e.planRequest(secret, body, hdr...)
	}
	require.NotEqual(e.t, http.StatusServiceUnavailable, r.Code, "install plan still unavailable after 30s: %s\nartifacts: %s", r.Body.String(), e.artifactStates())
	return r
}

// artifactStates lists status and error of every artifact record, for the message of a failed readyPlan.
func (e *apiEnv) artifactStates() string {
	list, _, err := e.st.Artifacts().List(context.Background(), store.ArtifactFilter{})
	if err != nil {
		return "list failed: " + err.Error()
	}
	var b strings.Builder
	for _, a := range list {
		fmt.Fprintf(&b, "\n  %s %s %s/%s: %s %q", a.Distribution, a.Version, a.OS, a.Arch, a.Status, a.Error)
	}
	return b.String()
}

func problemOf(t *testing.T, r resp) api.Problem {
	t.Helper()
	var p api.Problem
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &p), r.Body.String())
	return p
}

func (e *apiEnv) counter(result string) float64 {
	e.t.Helper()
	mfs, err := e.reg.Gather()
	require.NoError(e.t, err)
	for _, mf := range mfs {
		if mf.GetName() != "myriarch_install_plans_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------- the answer ----

// The first request for a platform makes the server download the artifacts: the answer is 503 preparing with
// Retry-After, and the installer asks again.
func TestInstallPlanIsPreparedInTheBackground(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)

	r := e.planRequest(secret, datacenterHost())
	require.Equal(t, http.StatusServiceUnavailable, r.Code, r.Body.String())
	assert.Equal(t, "10", r.Header().Get("Retry-After"))
	assert.Contains(t, r.Header().Get("Content-Type"), "problem+json")
	body := asMap(t, e.expect(r, "getInstallPlan", http.StatusServiceUnavailable))
	assert.Equal(t, "unavailable", body["code"])
	issues, _ := body["issues"].([]any)
	require.Len(t, issues, 1)
	assert.Equal(t, "preparing", asMap(t, issues[0])["code"])
	assert.NotContains(t, r.Body.String(), "request id", "the preparing answer keeps its own text")
	assert.Contains(t, body["detail"], "112 MB")

	r = e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
}

func TestInstallPlanText(t *testing.T) {
	e := newAPIEnv(t)
	secret, id := e.auth.newToken(nil)
	r := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", r.Header().Get("Content-Type"))
	assert.Equal(t, "Accept", r.Header().Get("Vary"))

	got, err := install.ParseText(r.Body.String())
	require.NoError(t, err, r.Body.String())
	assert.Equal(t, "datacenter", got[install.KeyFleet])
	assert.Equal(t, "git", got[install.KeyVersionSource])
	assert.Equal(t, "0.162.0", got[install.KeyCollectorVersion])
	assert.Equal(t, "0.162.0", got[install.KeySupervisorVersion])
	assert.Equal(t, "true", got[install.KeyUpdaterInstall])
	assert.Equal(t, "wss://fleet.example.com/v1/opamp", got[install.KeyOpAMPEndpoint])
	assert.Regexp(t, `^https://fleet\.example\.com/api/v1/artifacts/[0-9a-f-]{36}/content/otelcol-contrib_0\.162\.0_linux_amd64\.tar\.gz$`, got[install.KeyCollectorURL])
	assert.Regexp(t, `^https://fleet\.example\.com/api/v1/artifacts/[0-9a-f-]{36}/content/opampsupervisor_0\.162\.0_linux_amd64$`, got[install.KeySupervisorURL])
	assert.True(t, strings.HasPrefix(got[install.KeyCollectorGrant], "myrd_"))
	assert.NotEqual(t, got[install.KeyCollectorGrant], got[install.KeySupervisorGrant])
	assert.NotContains(t, r.Body.String(), secret, "the enrollment token is never part of the answer")
	assert.NotContains(t, r.Body.String(), id.String(), "nor is its ID")
	// one line each, no more
	assert.Len(t, strings.Split(strings.TrimSuffix(r.Body.String(), "\n"), "\n"), len(install.PlanKeys))
}

func TestInstallPlanJSON(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	text := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, text.Code)
	want, err := install.ParseText(text.Body.String())
	require.NoError(t, err)

	for _, accept := range []string{"application/json", "application/json, text/plain;q=0.5", "Application/JSON; charset=utf-8"} {
		r := e.planRequest(secret, datacenterHost(), "Accept", accept)
		require.Equal(t, http.StatusOK, r.Code, accept)
		assert.Contains(t, r.Header().Get("Content-Type"), "application/json")
		assert.Equal(t, "Accept", r.Header().Get("Vary"))
		v := asMap(t, e.expect(r, "getInstallPlan", http.StatusOK)) // validated against the schema InstallPlan
		assert.EqualValues(t, 1, v["format"])
		assert.Equal(t, want[install.KeyFleet], v["fleet"])
		assert.Equal(t, want[install.KeyVersionSource], asMap(t, v["version"])["source"])
		assert.Equal(t, want[install.KeyOpAMPEndpoint], asMap(t, v["opamp"])["endpoint"])
		col, sup := asMap(t, v["collector"]), asMap(t, v["supervisor"])
		assert.Equal(t, want[install.KeyCollectorVersion], col["version"])
		assert.Equal(t, want[install.KeyCollectorURL], col["url"])
		assert.Equal(t, want[install.KeySupervisorURL], sup["url"])
		assert.Equal(t, true, asMap(t, v["updater"])["install"])
		assert.True(t, strings.HasPrefix(col["grant"].(string), "myrd_"))
	}
	// everything else is the text format
	for _, accept := range []string{"", "*/*", "text/plain", "text/*", "text/plain, application/json", "application/json;q=0", "image/png", "garbage;;;"} {
		hdr := []string{}
		if accept != "" {
			hdr = []string{"Accept", accept}
		}
		r := e.planRequest(secret, datacenterHost(), hdr...)
		require.Equal(t, http.StatusOK, r.Code, accept)
		assert.Equal(t, "text/plain; charset=utf-8", r.Header().Get("Content-Type"), accept)
	}
}

// The grants of the plan download exactly the artifacts of the plan, with their checksum, and only while the
// enrollment token is valid.
func TestInstallPlanGrantsDownloadTheArtifacts(t *testing.T) {
	e := newAPIEnv(t)
	secret, id := e.auth.newToken(nil)
	r := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	plan, err := install.ParseText(r.Body.String())
	require.NoError(t, err)
	path := func(key string) string {
		return strings.TrimPrefix(plan[key], "https://fleet.example.com"+api.BasePath)
	}
	bearer := func(grant string) []string { return []string{"Authorization", "Bearer " + plan[grant]} }

	archive := []byte(strings.Repeat("artifact-bytes-", 100))
	w := e.do(http.MethodGet, path(install.KeyCollectorURL), "", nil, bearer(install.KeyCollectorGrant)...)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, archive, w.Body.Bytes())
	w = e.do(http.MethodGet, path(install.KeySupervisorURL), "", nil, bearer(install.KeySupervisorGrant)...)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, archive, w.Body.Bytes())
	w = e.do(http.MethodGet, path(install.KeyCollectorURL)+".sha256", "", nil, bearer(install.KeyCollectorGrant)...)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Regexp(t, `^[0-9a-f]{64}  otelcol-contrib_0\.162\.0_linux_amd64\.tar\.gz\n$`, w.Body.String())

	// each grant belongs to its artifact
	w = e.do(http.MethodGet, path(install.KeyCollectorURL), "", nil, bearer(install.KeySupervisorGrant)...)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	// and to the enrollment token: when it is revoked, the grants stop
	e.tokens.Revoke(id, time.Now())
	w = e.do(http.MethodGet, path(install.KeyCollectorURL), "", nil, bearer(install.KeyCollectorGrant)...)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, http.StatusUnauthorized, e.do(http.MethodGet, path(install.KeySupervisorURL)+".sha256", "", nil, bearer(install.KeySupervisorGrant)...).Code)
}

func TestInstallPlanFollowsTheFleetOfTheToken(t *testing.T) {
	e := newAPIEnv(t)
	// the test repository selects the fleet by site and system, not by labels: the host that the token gives no
	// special label gets the fleet of its attributes, and the answer follows a change of them
	secret, _ := e.auth.newToken(map[string]string{"channel": "edge-eu"})
	r := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	assert.Contains(t, r.Body.String(), "fleet=datacenter\n")
	office := datacenterHost(func(b map[string]any) { b["attributes"] = map[string]string{"site.type": "office"} })
	r = e.readyPlan(secret, office)
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	assert.Contains(t, r.Body.String(), "fleet=office\n")
	assert.Contains(t, r.Body.String(), "updater.install=false\n", "the office fleet updates through AWX")
}

// --------------------------------------------------------------- authentication ----

// Every failed authentication gets the same answer: the handler does not pass on what the authentication knows.
func TestInstallPlanAuthenticationFailsTheSameWay(t *testing.T) {
	e := newAPIEnv(t)
	revoked, revokedID := e.auth.newToken(nil)
	e.tokens.Revoke(revokedID, time.Now())
	past := time.Now().Add(-time.Hour)
	expired, _ := e.auth.newToken(nil, func(tok *domain.EnrollmentToken) { tok.ExpiresAt = &past })
	valid, _ := e.auth.newToken(nil)
	_ = valid

	type attempt struct {
		name   string
		header []string
	}
	attempts := []attempt{
		{"no header", nil},
		{"unknown token", []string{"Authorization", "Bearer myre_" + strings.Repeat("a", 32)}},
		{"revoked token", []string{"Authorization", "Bearer " + revoked}},
		{"expired token", []string{"Authorization", "Bearer " + expired}},
		{"another scheme", []string{"Authorization", "Basic abc"}},
	}
	var details []string
	for _, a := range attempts {
		r := e.planRequest("", datacenterHost(), a.header...)
		require.Equal(t, http.StatusUnauthorized, r.Code, a.name)
		assert.Contains(t, r.Header().Get("WWW-Authenticate"), "Bearer", a.name)
		assert.NotContains(t, r.Body.String(), leakMarker, a.name)
		assert.NotContains(t, r.Body.String(), revokedID.String(), a.name)
		assert.NotContains(t, r.Body.String(), revoked, a.name)
		e.expect(r, "getInstallPlan", http.StatusUnauthorized)
		p := problemOf(t, r)
		assert.Equal(t, "unauthorized", p.Code)
		details = append(details, p.Detail)
	}
	for _, d := range details[1:] {
		assert.Equal(t, details[0], d, "the same answer whether the token exists or not")
	}
	assert.Equal(t, float64(len(attempts)), e.counter("unauthorized"))
}

func TestInstallPlanRateLimitAndDatabaseFailure(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)

	e.auth.setFail(&domain.RateLimitError{Msg: "too many failed authentications from this client address, retry in 5s", After: 5 * time.Second})
	r := e.planRequest(secret, datacenterHost())
	require.Equal(t, http.StatusTooManyRequests, r.Code)
	assert.Equal(t, "5", r.Header().Get("Retry-After"))
	e.expect(r, "getInstallPlan", http.StatusTooManyRequests)
	assert.Equal(t, "too_many_requests", problemOf(t, r).Code)

	e.auth.setFail(fmt.Errorf("%w: enrollment token lookup: connection refused (password=hunter2)", domain.ErrUnavailable))
	r = e.planRequest(secret, datacenterHost())
	require.Equal(t, http.StatusServiceUnavailable, r.Code)
	assert.NotContains(t, r.Body.String(), "hunter2", "the cause stays in the log")
	assert.NotContains(t, r.Body.String(), "connection refused")
	assert.Equal(t, "unavailable", problemOf(t, r).Code)

	assert.Equal(t, 1.0, e.counter("rate_limited"))
	assert.Equal(t, 1.0, e.counter("unavailable"))
	e.auth.setFail(nil)
	assert.Equal(t, http.StatusServiceUnavailable, e.planRequest(secret, datacenterHost()).Code) // preparing
	assert.Equal(t, 1.0, e.counter("preparing"))
}

// A server that does not offer the installation answers 404 to everybody, before it looks at a token.
func TestInstallPlanDisabled(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	off := e.cfg.Install
	off.Enabled = false
	planner, err := install.NewPlanner(install.Deps{Store: e.st, Assigner: e.assigner, Artifacts: e.cache, Config: off, PublicURL: e.cfg.Server.PublicURL, OpAMPPath: e.cfg.OpAMP.Path})
	require.NoError(t, err)
	for name, mut := range map[string]func(d *Deps){
		"install.enabled off": func(d *Deps) { d.Install = planner },
		"no planner":          func(d *Deps) { d.Install = nil },
		"no authentication":   func(d *Deps) { d.InstallAuth = nil },
	} {
		t.Run(name, func(t *testing.T) {
			e.remount(func(d *Deps) { d.Install, d.InstallAuth = e.planner, e.auth; mut(d) })
			before := e.auth.callCount()
			for _, tok := range []string{secret, "", "myre_" + strings.Repeat("x", 32)} {
				r := e.planRequest(tok, datacenterHost())
				require.Equal(t, http.StatusNotFound, r.Code, "token %q", tok)
				e.expect(r, "getInstallPlan", http.StatusNotFound)
				assert.Equal(t, "not_found", problemOf(t, r).Code)
			}
			assert.Equal(t, before, e.auth.callCount(), "no token was looked at")
		})
	}
	assert.GreaterOrEqual(t, e.counter("disabled"), 9.0)
}

// ----------------------------------------------------------------- the request ----

func TestInstallPlanRequestErrors(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	cases := map[string]struct {
		body   any
		hdr    []string
		status int
	}{
		"empty body":         {"", nil, http.StatusBadRequest},
		"not json":           {"os=linux", nil, http.StatusBadRequest},
		"two documents":      {`{"os":"linux","arch":"amd64"}{"os":"linux"}`, nil, http.StatusBadRequest},
		"unknown field":      {`{"os":"linux","arch":"amd64","token":"x"}`, nil, http.StatusBadRequest},
		"a number":           {`{"os":1,"arch":"amd64"}`, nil, http.StatusBadRequest},
		"attributes as list": {`{"os":"linux","arch":"amd64","attributes":["a"]}`, nil, http.StatusBadRequest},
		"form content type":  {`os=linux&arch=amd64`, []string{"Content-Type", "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		"too large":          {`{"os":"linux","arch":"amd64","fleet":"` + strings.Repeat("a", 20<<10) + `"}`, nil, http.StatusBadRequest},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := e.planRequest(secret, c.body, c.hdr...)
			require.Equal(t, c.status, r.Code, r.Body.String())
			assert.Contains(t, r.Header().Get("Content-Type"), "problem+json")
			assert.NotContains(t, r.Body.String(), secret)
		})
	}
	arts, _, err := e.st.Artifacts().List(context.Background(), store.ArtifactFilter{})
	require.NoError(t, err)
	assert.Empty(t, arts, "no artifact was requested for a bad request")
}

func TestInstallPlanValidation(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	r := e.planRequest(secret, map[string]any{"os": "plan9", "arch": "mips", "attributes": map[string]string{"service.name": "x", "site.id": ""}, "fleet": "Bad Fleet", "collectorVersion": "latest"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Code, r.Body.String())
	body := asMap(t, e.expect(r, "getInstallPlan", http.StatusUnprocessableEntity))
	assert.Equal(t, "validation_failed", body["code"])
	paths := map[string]string{}
	for _, is := range body["issues"].([]any) {
		m := asMap(t, is)
		paths[m["path"].(string)] = m["code"].(string)
	}
	assert.Equal(t, map[string]string{
		"os": "invalid_value", "arch": "invalid_value", "attributes.service.name": "unknown_attribute", "attributes.site.id": "invalid_value",
		"fleet": "invalid_value", "collectorVersion": "invalid_value",
	}, paths)
	assert.Equal(t, 1.0, e.counter("invalid_request"))
}

func TestInstallPlanRefusals(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	check := func(name string, body any, status int, class, reason string) {
		t.Helper()
		r := e.planRequest(secret, body)
		require.Equal(t, status, r.Code, "%s: %s", name, r.Body.String())
		doc := asMap(t, e.expect(r, "getInstallPlan", status))
		assert.Equal(t, class, doc["code"], name)
		issues := doc["issues"].([]any)
		require.NotEmpty(t, issues, name)
		assert.Equal(t, reason, asMap(t, issues[0])["code"], name)
		assert.NotContains(t, r.Body.String(), secret)
	}
	check("fleet hint", datacenterHost(func(b map[string]any) { b["fleet"] = "edge-nl" }), http.StatusConflict, "conflict", "fleet_mismatch")
	check("no fleet", datacenterHost(func(b map[string]any) { b["attributes"] = map[string]string{"site.type": "lab"} }), http.StatusConflict, "conflict", "no_fleet")
	check("version outside the frame", datacenterHost(func(b map[string]any) { b["collectorVersion"] = "0.150.0" }), http.StatusUnprocessableEntity, "validation_failed", "version_not_allowed")

	// a version rollout that is paused: nobody is installed until it is resolved
	require.NoError(t, e.st.Rollouts().Create(context.Background(), &domain.Rollout{ID: uuid.New(), Kind: domain.RolloutKindVersion, FleetName: "datacenter", PolicyName: "standard",
		Status: domain.RolloutPaused, FromRef: "0.161.0", ToRef: "0.162.0", CreatedBy: "gitops"}, nil))
	check("paused rollout", datacenterHost(), http.StatusConflict, "conflict", "rollout_blocking")
	assert.Equal(t, 1.0, e.counter("rollout_blocking"))
	assert.Equal(t, 1.0, e.counter("fleet_mismatch"))
	assert.Equal(t, 1.0, e.counter("no_fleet"))
}

func TestInstallPlanDuringARunningRollout(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	require.NoError(t, e.st.Rollouts().Create(context.Background(), &domain.Rollout{ID: uuid.New(), Kind: domain.RolloutKindVersion, FleetName: "datacenter", PolicyName: "standard",
		Status: domain.RolloutRunning, FromRef: "0.161.0", ToRef: "0.162.0", CreatedBy: "gitops"}, nil))
	r := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	got, err := install.ParseText(r.Body.String())
	require.NoError(t, err)
	assert.Equal(t, "0.161.0", got[install.KeyCollectorVersion], "the version before the rollout, so that the host joins its last wave")
	assert.Equal(t, "rollout", got[install.KeyVersionSource])
}

// gitMoves applies a snapshot in which the fleet has another Git target and lets the replicas load it, as the event of a
// sync does. The leader has not planned it yet.
func (e *apiEnv) gitMoves(fleet, version string) {
	e.t.Helper()
	ctx := context.Background()
	snap, err := e.st.GitOps().GetSnapshot(ctx)
	require.NoError(e.t, err)
	next := *snap
	next.Fleets = append([]domain.Fleet(nil), snap.Fleets...)
	next.Commit = "moved-" + fleet + "-" + version
	for i := range next.Fleets {
		if next.Fleets[i].Name == fleet {
			next.Fleets[i].Collector.Version = version
		}
	}
	_, err = e.st.GitOps().ApplySnapshot(ctx, next, domain.GitOpsSync{Trigger: "poll", Result: "applied", StartedAt: e.clock, FinishedAt: &e.clock, Commit: &domain.GitCommit{SHA: next.Commit, Time: e.clock}})
	require.NoError(e.t, err)
	_, err = e.assigner.Reload(ctx)
	require.NoError(e.t, err)
}

// A new Git target reaches the replicas before the leader has planned the rollout for it. A host that asks in between
// waits instead of taking the new version past the waves, and once the real planner has created the rollout, the host
// takes the version before it, as a straggler of the last wave.
func TestInstallPlanWaitsForTheRolloutPlannerAfterANewGitTarget(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	secret, _ := e.auth.newToken(nil)

	// the fleet runs 0.161.0 (Git says so, the planner has handled it)
	e.gitMoves("datacenter", "0.161.0")
	e.addAgent("dc-north-01", func(a *domain.Agent) { a.Version = "0.161.0" })
	require.NoError(t, e.engine.Plan(ctx))
	r := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	got, err := install.ParseText(r.Body.String())
	require.NoError(t, err)
	require.Equal(t, "0.161.0", got[install.KeyCollectorVersion])
	require.Equal(t, "git", got[install.KeyVersionSource])

	// Git moves the fleet to 0.162.0; the replica has loaded it, the leader has not planned it
	e.gitMoves("datacenter", "0.162.0")
	r = e.planRequest(secret, datacenterHost())
	require.Equal(t, http.StatusServiceUnavailable, r.Code, r.Body.String())
	assert.Equal(t, "5", r.Header().Get("Retry-After"))
	doc := asMap(t, e.expect(r, "getInstallPlan", http.StatusServiceUnavailable))
	assert.Equal(t, "unavailable", doc["code"])
	assert.Equal(t, "preparing", asMap(t, doc["issues"].([]any)[0])["code"])
	assert.Contains(t, doc["detail"], "waves and approvals")
	assert.NotContains(t, r.Body.String(), "0.162.0_linux", "no artifact of the new version is named")
	assert.NotContains(t, r.Body.String(), "myrd_", "and no grant is given")

	// the leader plans: it creates the rollout from 0.161.0 to 0.162.0 and records the target
	require.NoError(t, e.engine.Plan(ctx))
	rollouts, _, err := e.st.Rollouts().List(ctx, store.RolloutFilter{Fleets: []string{"datacenter"}, Kinds: []domain.RolloutKind{domain.RolloutKindVersion}, ActiveOnly: true})
	require.NoError(t, err)
	require.Len(t, rollouts, 1)
	assert.Equal(t, "0.161.0", rollouts[0].FromRef)
	assert.Equal(t, "0.162.0", rollouts[0].ToRef)

	r = e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	got, err = install.ParseText(r.Body.String())
	require.NoError(t, err)
	assert.Equal(t, "0.161.0", got[install.KeyCollectorVersion], "the version before the rollout, so that the host joins its last wave")
	assert.Equal(t, "rollout", got[install.KeyVersionSource])
}

// The token is read from one header and written nowhere: not into the log, not into a metric, not into an audit
// entry, not into an answer.
func TestInstallPlanNeverRecordsTheToken(t *testing.T) {
	e := newAPIEnv(t)
	rec := &recordingAudit{}
	e.remount(func(d *Deps) { d.Audit = rec })
	secret, id := e.auth.newToken(nil)
	revoked, revokedID := e.auth.newToken(nil)
	e.tokens.Revoke(revokedID, time.Now())

	var bodies []string
	for _, r := range []resp{
		e.planRequest(secret, datacenterHost()),                                                  // preparing
		e.planRequest(secret, datacenterHost(func(b map[string]any) { b["fleet"] = "edge-nl" })), // 409
		e.planRequest(secret, "{"),                                                               // 400
		e.planRequest(secret, datacenterHost(func(b map[string]any) { b["os"] = "x" })),          // 422
		e.planRequest(revoked, datacenterHost()),                                                 // 401
		e.planRequest("", datacenterHost(), "Authorization", "Bearer "+secret+"x"),               // 401
	} {
		bodies = append(bodies, r.Body.String())
		for _, vs := range r.Header() {
			bodies = append(bodies, strings.Join(vs, ","))
		}
	}
	e.waitPrepared()
	ok := e.readyPlan(secret, datacenterHost())
	require.Equal(t, http.StatusOK, ok.Code)
	for _, hv := range ok.Header() {
		bodies = append(bodies, strings.Join(hv, ","))
	}

	var metrics bytes.Buffer
	mfs, err := e.reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		_, err := expfmt.MetricFamilyToText(&metrics, mf)
		require.NoError(t, err)
	}
	var out []string
	out = append(out, bodies...)
	out = append(out, e.logs.String(), metrics.String())
	for _, s := range out {
		for _, secretValue := range []string{secret, revoked} {
			assert.NotContains(t, s, secretValue)
		}
	}
	assert.Contains(t, e.logs.String(), id.String(), "the log names the token by its ID")
	assert.Contains(t, e.logs.String(), "install plan issued")
	assert.Equal(t, 0, rec.count(), "a plan request is not an operator action: nothing is audited, and nothing carries the token")
}

type recordingAudit struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (r *recordingAudit) Record(_ context.Context, e domain.AuditEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingAudit) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

var _ audit.Recorder = (*recordingAudit)(nil)

// Hosts that install at the same time: every request ends with the plan or with preparing, and the server fetches
// each artifact once.
func TestInstallPlanConcurrentRequests(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	var wg sync.WaitGroup
	codes := make(chan int, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.planRequest(secret, datacenterHost())
			codes <- r.Code
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		assert.Contains(t, []int{http.StatusOK, http.StatusServiceUnavailable}, c)
	}
	assert.Equal(t, http.StatusOK, e.readyPlan(secret, datacenterHost()).Code)
	arts, _, err := e.st.Artifacts().List(context.Background(), store.ArtifactFilter{})
	require.NoError(t, err)
	assert.Len(t, arts, 2, "one record for the collector and one for the supervisor")
}

func TestWantsJSON(t *testing.T) {
	cases := []struct {
		accept []string
		want   bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"application/json"}, true},
		{[]string{"APPLICATION/JSON"}, true},
		{[]string{"application/json; charset=utf-8"}, true},
		{[]string{"application/json, text/plain"}, true},
		{[]string{"text/plain, application/json"}, false},
		{[]string{"text/plain;q=0.1, application/json"}, false}, // order counts, not the weights
		{[]string{"*/*"}, false},
		{[]string{"application/*"}, false},
		{[]string{"application/json;q=0"}, false},
		{[]string{"application/json;q=0, text/plain"}, false},
		{[]string{"image/png", "application/json"}, true},
		{[]string{"garbage", "application/json"}, true},
		{[]string{";;;"}, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		for _, a := range c.accept {
			r.Header.Add("Accept", a)
		}
		assert.Equal(t, c.want, wantsJSON(r), "%q", c.accept)
	}
}

// A fleet on hold has no version to install: 412 no_target_version, and nothing is prepared or delivered.
func TestInstallPlanOfAFleetOnHoldIsRefusedWith412(t *testing.T) {
	e := newAPIEnv(t)
	secret, _ := e.auth.newToken(nil)
	_, err := e.st.FleetVersions().Put(context.Background(), domain.FleetVersionSelection{
		Fleet: "datacenter", Version: "0.163.0", State: domain.SelectionInvalidated, StateReason: "outside the frame", GitVersion: "0.162.0",
		SelectedBy: "user:x", SelectedAt: time.Now(),
	}, nil)
	require.NoError(t, err)
	_, err = e.assigner.Reload(context.Background())
	require.NoError(t, err)
	r := e.planRequest(secret, datacenterHost())
	require.Equal(t, http.StatusPreconditionFailed, r.Code, r.Body.String())
	doc := asMap(t, e.expect(r, "getInstallPlan", http.StatusPreconditionFailed))
	assert.Equal(t, "precondition_failed", doc["code"])
	assert.Equal(t, "no_target_version", asMap(t, doc["issues"].([]any)[0])["code"])
	assert.NotContains(t, r.Body.String(), secret)
	assert.Equal(t, 1.0, e.counter("no_target_version"))
}
