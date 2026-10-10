// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

func TestTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	// The literal prefix pins the wire format; a change of domain.APITokenPrefix must fail here.
	re := regexp.MustCompile(`^myra_[0-9A-Za-z]{32}$`)
	for i := 0; i < 500; i++ {
		tok, err := authn.NewToken(domain.APITokenPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(tok) {
			t.Fatalf("token %q does not match <prefix><32 base62>", tok)
		}
		if seen[tok] {
			t.Fatal("token collision")
		}
		seen[tok] = true
	}
}

func TestDevLoginSessionCSRFAndLogout(t *testing.T) {
	e := newEnv(t, envOpts{})
	c := e.newClient()

	// public auth config
	r := c.req("GET", "/api/v1/auth/config", nil, false)
	var info dto.AuthConfigInfo
	r.JSON(&info)
	if r.Code != 200 || !info.DevLoginEnabled || info.OIDCEnabled {
		t.Fatalf("auth config: %d %+v", r.Code, info)
	}
	if info.UI.DefaultLanguage != "en" || len(info.UI.Languages) != 0 {
		t.Fatalf("ui language options default to en without restriction: %+v", info.UI)
	}
	if !strings.Contains(string(r.Body), `"ui":{"defaultLanguage":"en"}`) {
		t.Fatalf("auth config body: %s", r.Body)
	}
	// unauthenticated
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 401 || r.Problem().Code != api.ErrorCodeUnauthorized {
		t.Fatalf("me without session: %d %s", r.Code, r.Body)
	}

	// bad credentials: 401, audited as failure, no cookies
	bad := c.devLogin("admin", "wrong")
	if bad.Code != 401 || c.cookie(authn.SessionCookie) != "" {
		t.Fatalf("bad password: %d", bad.Code)
	}
	if r := c.devLogin("nobody", "x"); r.Code != 401 {
		t.Fatalf("unknown user: %d", r.Code)
	}
	if r := c.devLogin("", ""); r.Code != 401 {
		t.Fatalf("empty credentials: %d", r.Code)
	}
	fails := e.findAudit("auth.login")
	if len(fails) != 3 || fails[0].Result != "failure" {
		t.Fatalf("failed logins must be audited: %+v", fails)
	}

	// good login
	r = c.devLogin("operator", "operator-pw")
	var me dto.Me
	r.JSON(&me)
	if r.Code != 200 || me.Role != domain.RoleOperator || me.AuthMethod != "local" || me.Type != "user" || me.CSRFToken == "" || me.Name != "operator" {
		t.Fatalf("login: %d %+v", r.Code, me)
	}
	if me.CSRFToken != c.cookie(authn.CSRFCookie) {
		t.Fatal("csrf token in the response must equal the csrf cookie")
	}
	var sawSession, sawCSRF bool
	for _, line := range r.Header.Values("Set-Cookie") {
		switch {
		case strings.HasPrefix(line, authn.SessionCookie+"="):
			sawSession = true
			for _, want := range []string{"HttpOnly", "SameSite=Lax", "Path=/"} {
				if !strings.Contains(line, want) {
					t.Errorf("session cookie lacks %s: %s", want, line)
				}
			}
			if strings.Contains(line, "Secure") {
				t.Errorf("Secure must follow auth.cookieSecure (false in this test): %s", line)
			}
		case strings.HasPrefix(line, authn.CSRFCookie+"="):
			sawCSRF = true
			if strings.Contains(line, "HttpOnly") {
				t.Errorf("csrf cookie must be readable by the UI: %s", line)
			}
		}
	}
	if !sawSession || !sawCSRF {
		t.Fatal("login must set both cookies")
	}
	if ok := e.findAudit("auth.login"); ok[0].Result != "success" || ok[0].ActorType != domain.ActorUser {
		t.Fatalf("login audit: %+v", ok[0])
	}

	// GET /auth/me
	r = c.req("GET", "/api/v1/auth/me", nil, false)
	r.JSON(&me)
	if r.Code != 200 || me.Role != domain.RoleOperator || len(me.Permissions) == 0 || me.CSRFToken != c.cookie(authn.CSRFCookie) {
		t.Fatalf("me: %d %+v", r.Code, me)
	}
	wantPerms := authz.PermissionsFor(domain.RoleOperator)
	if len(me.Permissions) != len(wantPerms) {
		t.Fatalf("permissions: %v", me.Permissions)
	}

	// the session is stored hashed: the cookie value itself must not be a key
	raw := c.cookie(authn.SessionCookie)
	if _, err := e.db.Identity().GetSession(context.Background(), raw); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the database must only contain the SHA-256 of the cookie secret")
	}

	// mutating request: CSRF
	r = c.req("POST", "/api/v1/auth/logout", nil, false)
	if r.Code != 403 || r.Problem().Code != api.ErrorCodeCSRF {
		t.Fatalf("logout without csrf: %d %s", r.Code, r.Body)
	}
	r = c.req("POST", "/api/v1/auth/logout", nil, false, authn.CSRFHeader, "wrong")
	if r.Code != 403 {
		t.Fatalf("logout with wrong csrf: %d", r.Code)
	}
	// users:manage is admin only
	if r := c.req("GET", "/api/v1/users", nil, false); r.Code != 403 || r.Problem().Code != api.ErrorCodeForbidden {
		t.Fatalf("operator must not list users: %d", r.Code)
	}

	r = c.req("POST", "/api/v1/auth/logout", nil, true)
	if r.Code != 204 {
		t.Fatalf("logout: %d %s", r.Code, r.Body)
	}
	if got := e.findAudit("auth.logout"); len(got) != 1 {
		t.Fatalf("logout audit: %+v", got)
	}
	// the old cookie is dead even if a client keeps sending it
	old := c.cookie(authn.SessionCookie)
	_ = old
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 401 {
		t.Fatalf("session must be invalid after logout: %d", r.Code)
	}
}

func TestSessionLifecycle(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.SessionTTL = time.Hour }})
	c := e.newClient()
	c.mustDevLogin("viewer")

	// sliding expiry: activity within the TTL extends the session
	e.clock.Advance(40 * time.Minute)
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 200 {
		t.Fatalf("within ttl: %d", r.Code)
	}
	time.Sleep(300 * time.Millisecond) // async touch
	e.clock.Advance(40 * time.Minute)  // 80 min after login, 40 min after last activity
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 200 {
		t.Fatalf("sliding expiry must keep an active session alive: %d", r.Code)
	}
	// idle longer than the ttl: expired
	e.clock.Advance(2 * time.Hour)
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 401 {
		t.Fatalf("expired session: %d", r.Code)
	}
	time.Sleep(200 * time.Millisecond) // async delete of the expired row
}

func TestDisabledUserLosesAccess(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	victim := e.newClient()
	victim.mustDevLogin("viewer")
	if r := victim.req("GET", "/api/v1/auth/me", nil, false); r.Code != 200 {
		t.Fatal(r.Code)
	}

	var users api.List[domain.User]
	admin.req("GET", "/api/v1/users", nil, false).JSON(&users)
	var viewerID, adminID uuid.UUID
	for _, u := range users.Items {
		switch u.Name {
		case "viewer":
			viewerID = u.ID
		case "admin":
			adminID = u.ID
		}
	}
	if viewerID == uuid.Nil || adminID == uuid.Nil {
		t.Fatalf("users: %+v", users.Items)
	}

	// self modification is refused
	if r := admin.req("PATCH", "/api/v1/users/"+adminID.String(), map[string]any{"disabled": true}, true); r.Code != 412 {
		t.Fatalf("self disable: %d %s", r.Code, r.Body)
	}
	// validation
	if r := admin.req("PATCH", "/api/v1/users/"+viewerID.String(), map[string]any{}, true); r.Code != 422 {
		t.Fatalf("empty update: %d", r.Code)
	}
	if r := admin.req("PATCH", "/api/v1/users/"+viewerID.String(), map[string]any{"role": "root"}, true); r.Code != 422 {
		t.Fatalf("invalid role: %d", r.Code)
	}
	if r := admin.req("PATCH", "/api/v1/users/"+uuid.NewString(), map[string]any{"disabled": true}, true); r.Code != 404 {
		t.Fatalf("unknown user: %d", r.Code)
	}
	if r := admin.req("PATCH", "/api/v1/users/not-a-uuid", map[string]any{"disabled": true}, true); r.Code != 400 {
		t.Fatalf("bad id: %d", r.Code)
	}

	r := admin.req("PATCH", "/api/v1/users/"+viewerID.String(), map[string]any{"role": "operator", "disabled": true}, true)
	var u domain.User
	r.JSON(&u)
	if r.Code != 200 || u.Role != domain.RoleOperator || u.RoleSource != "manual" || !u.Disabled {
		t.Fatalf("update: %d %+v", r.Code, u)
	}
	if got := e.findAudit("user.update"); len(got) != 1 || got[0].TargetID != viewerID.String() {
		t.Fatalf("user.update audit: %+v", got)
	}
	// effect within the 5s principal cache window
	e.clock.Advance(6 * time.Second)
	if r := victim.req("GET", "/api/v1/auth/me", nil, false); r.Code != 401 {
		t.Fatalf("disabled user must be rejected: %d", r.Code)
	}
	// and cannot log in again
	if r := e.newClient().devLogin("viewer", "viewer-pw"); r.Code != 401 {
		t.Fatalf("disabled user login: %d", r.Code)
	}
}

func TestAuthConfigServesUIOptions(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, err := authn.New(context.Background(), e.authCfg, e.serverCfg, e.opampCfg, authn.Deps{
		Store: e.db, Registerer: prometheus.NewRegistry(), HTTPClient: e.httpCli,
		UI: config.UIConfig{DefaultLanguage: "de", Languages: []string{"de", "en"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var h api.Route
	for _, rt := range svc.Module().Routes() {
		if rt.OperationID == "getAuthConfig" {
			h = rt
		}
	}
	if h.Handler == nil || h.Auth != api.AuthPublic {
		t.Fatalf("getAuthConfig must be a public route: %+v", h)
	}
	rec := httptest.NewRecorder()
	h.Handler(rec, httptest.NewRequest("GET", "/api/v1/auth/config", nil))
	var info dto.AuthConfigInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.UI.DefaultLanguage != "de" || len(info.UI.Languages) != 2 || info.UI.Languages[1] != "en" {
		t.Fatalf("ui options: %+v (%s)", info.UI, rec.Body)
	}
}

func TestDevLoginDisabledIs404(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.DevLogin.Enabled = false }})
	r := e.newClient().devLogin("admin", "admin-pw")
	if r.Code != 404 {
		t.Fatalf("dev login must not exist when disabled: %d", r.Code)
	}
	var info dto.AuthConfigInfo
	e.newClient().req("GET", "/api/v1/auth/config", nil, false).JSON(&info)
	if info.DevLoginEnabled {
		t.Fatal("auth config must report devLogin disabled")
	}
}

func TestAPITokensLifecycle(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	op := e.newClient()
	op.mustDevLogin("operator")

	// only admins manage tokens
	if r := op.req("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer"}, true); r.Code != 403 {
		t.Fatalf("operator creating a token: %d", r.Code)
	}
	if r := admin.req("POST", "/api/v1/tokens", map[string]any{"name": "", "role": "viewer"}, true); r.Code != 422 {
		t.Fatalf("empty name: %d", r.Code)
	}
	if r := admin.req("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "god"}, true); r.Code != 422 {
		t.Fatalf("bad role: %d", r.Code)
	}
	past := time.Now().Add(-time.Hour)
	if r := admin.req("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer", "expiresAt": past}, true); r.Code != 422 {
		t.Fatalf("expiry in the past: %d", r.Code)
	}
	if r := admin.req("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer", "bogus": 1}, true); r.Code != 400 {
		t.Fatalf("unknown field: %d", r.Code)
	}

	r := admin.req("POST", "/api/v1/tokens", map[string]any{"name": "ci pipeline", "role": "operator"}, true)
	var created dto.CreatedAPIToken
	r.JSON(&created)
	if r.Code != 201 || !strings.HasPrefix(created.Secret, "myra_") || len(created.Secret) != len(domain.APITokenPrefix)+32 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	if created.Token.DisplayPrefix != created.Secret[:12] || created.Token.Role != domain.RoleOperator || !strings.HasPrefix(created.Token.CreatedBy, "user:") {
		t.Fatalf("token meta: %+v", created.Token)
	}
	if strings.Contains(string(r.Body), "\"hash\"") {
		t.Fatal("the hash must never be serialized")
	}
	// only the hash is stored
	stored, err := e.db.Identity().GetAPITokenByHash(context.Background(), domain.SHA256([]byte(created.Secret)))
	if err != nil || stored.ID != created.Token.ID {
		t.Fatalf("lookup by hash: %v", err)
	}
	var list api.List[domain.APIToken]
	admin.req("GET", "/api/v1/tokens", nil, false).JSON(&list)
	if len(list.Items) != 1 || strings.Contains(string(admin.req("GET", "/api/v1/tokens", nil, false).Body), created.Secret) {
		t.Fatalf("list must not leak secrets: %+v", list)
	}

	// using the token: bearer, no CSRF, role limits
	bearer := e.newClient()
	use := func(method, path string, body any) result {
		return bearer.req(method, path, body, false, "Authorization", "Bearer "+created.Secret)
	}
	var me dto.Me
	r = use("GET", "/api/v1/auth/me", nil)
	r.JSON(&me)
	if r.Code != 200 || me.Type != "token" || me.AuthMethod != "api_token" || me.Role != domain.RoleOperator || me.CSRFToken != "" || me.Name != "ci pipeline" {
		t.Fatalf("me via token: %d %+v", r.Code, me)
	}
	if r := use("GET", "/api/v1/users", nil); r.Code != 403 {
		t.Fatalf("operator token on admin route: %d", r.Code)
	}
	if r := use("GET", "/api/v1/audit", nil); r.Code == 401 || r.Code == 403 {
		// audit is not part of this module: the route does not exist in this test server
		t.Logf("audit route status %d", r.Code)
	}
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer myra_"+strings.Repeat("x", 32)); r.Code != 401 {
		t.Fatalf("unknown token: %d", r.Code)
	}
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer myre_"+strings.Repeat("x", 32)); r.Code != 401 {
		t.Fatalf("enrollment tokens are not API tokens: %d", r.Code)
	}
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Basic abc"); r.Code != 401 {
		t.Fatalf("other schemes: %d", r.Code)
	}
	// a bad bearer is not silently replaced by a valid session cookie
	if r := admin.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer myra_"+strings.Repeat("y", 32)); r.Code != 401 {
		t.Fatalf("bearer takes precedence over the cookie: %d", r.Code)
	}

	// last_used_at is updated (async, at most once per minute)
	deadline := time.Now().Add(5 * time.Second)
	for {
		tk, _ := e.db.Identity().GetAPITokenByHash(context.Background(), domain.SHA256([]byte(created.Secret)))
		if tk.LastUsedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("last_used_at was not updated")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// revoke
	if r := admin.req("DELETE", "/api/v1/tokens/"+uuid.NewString(), nil, true); r.Code != 404 {
		t.Fatalf("revoke unknown: %d", r.Code)
	}
	if r := admin.req("DELETE", "/api/v1/tokens/"+created.Token.ID.String(), nil, false); r.Code != 403 {
		t.Fatalf("revoke without csrf: %d", r.Code)
	}
	if r := admin.req("DELETE", "/api/v1/tokens/"+created.Token.ID.String(), nil, true); r.Code != 204 {
		t.Fatalf("revoke: %d", r.Code)
	}
	if r := use("GET", "/api/v1/auth/me", nil); r.Code != 401 {
		t.Fatalf("revoked token must be rejected immediately on this replica: %d", r.Code)
	}
	if got := e.findAudit("token.create"); len(got) != 1 || got[0].TargetType != "api_token" {
		t.Fatalf("audit token.create: %+v", got)
	}
	revokes := e.findAudit("token.revoke")
	if len(revokes) != 2 || revokes[0].Result != "success" || revokes[1].Result != "failure" {
		t.Fatalf("audit token.revoke (one failed attempt on an unknown id, one success): %+v", revokes)
	}
	if ev := e.findAudit("token.create")[0]; ev.Details["name"] != "ci pipeline" || strings.Contains(strings.ToLower(ev.ActorName), "myra_") {
		t.Fatalf("audit details: %+v", ev)
	}

	// expiry
	exp := e.clock.Now().Add(time.Hour)
	r = admin.req("POST", "/api/v1/tokens", map[string]any{"name": "short", "role": "viewer", "expiresAt": exp}, true)
	r.JSON(&created)
	short := created.Secret
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+short); r.Code != 200 {
		t.Fatalf("token before expiry: %d %s", r.Code, r.Body)
	}
	e.clock.Advance(2 * time.Hour)
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+short); r.Code != 401 {
		t.Fatalf("expired token: %d", r.Code)
	}
}

func TestEnrollmentTokenAPIAndAgentAuth(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	op := e.newClient()
	op.mustDevLogin("operator")

	if r := op.req("GET", "/api/v1/enrollment-tokens", nil, false); r.Code != 403 {
		t.Fatalf("operator listing enrollment tokens: %d", r.Code)
	}
	if r := admin.req("POST", "/api/v1/enrollment-tokens", map[string]any{"name": "sites", "labels": map[string]string{"bad key": "x"}}, true); r.Code != 422 {
		t.Fatalf("bad label: %d", r.Code)
	}
	r := admin.req("POST", "/api/v1/enrollment-tokens", map[string]any{"name": "edge-nl", "labels": map[string]string{"channel": "edge-nl"}}, true)
	var created dto.CreatedEnrollmentToken
	r.JSON(&created)
	if r.Code != 201 || !strings.HasPrefix(created.Secret, "myre_") || len(created.Secret) != len(domain.EnrollmentTokenPrefix)+32 || created.Token.Labels["channel"] != "edge-nl" {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	var list api.List[domain.EnrollmentToken]
	admin.req("GET", "/api/v1/enrollment-tokens", nil, false).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].Labels["channel"] != "edge-nl" {
		t.Fatalf("list: %+v", list)
	}

	agentReq := func(authHeader string) *http.Request {
		rq := httptest.NewRequest("GET", "/v1/opamp", nil)
		if authHeader != "" {
			rq.Header.Set("Authorization", authHeader)
		}
		return rq
	}

	// agent authentication
	cred, err := e.svc.AuthenticateAgent(agentReq("Bearer " + created.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if cred.EnrollmentTokenID == nil || *cred.EnrollmentTokenID != created.Token.ID || cred.Labels["channel"] != "edge-nl" {
		t.Fatalf("credential: %+v", cred)
	}
	cred.Labels["channel"] = "mutated"
	cred2, _ := e.svc.AuthenticateAgent(agentReq("bearer " + created.Secret)) // scheme is case-insensitive
	if cred2.Labels["channel"] != "edge-nl" {
		t.Fatal("credentials must not share the cached label map")
	}
	for name, h := range map[string]string{
		"missing":       "",
		"wrong prefix":  "Bearer myra_" + strings.Repeat("a", 32),
		"unknown token": "Bearer myre_" + strings.Repeat("a", 32),
		"basic":         "Basic Zm9vOmJhcg==",
		"empty bearer":  "Bearer ",
		"overlong":      "Bearer myre_" + strings.Repeat("a", 300),
	} {
		if _, err := e.svc.AuthenticateAgent(agentReq(h)); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}

	// usage statistics are flushed asynchronously
	e.svc.FlushEnrollmentUsage(context.Background())
	tok, _ := e.db.Identity().GetEnrollmentTokenByHash(context.Background(), domain.SHA256([]byte(created.Secret)))
	if tok.UseCount != 2 || tok.LastUsedAt == nil {
		t.Fatalf("use count after flush: %+v", tok)
	}

	// revocation takes effect after the cache TTL (60s) on other replicas and
	// immediately on the replica that handled the request
	if r := admin.req("DELETE", "/api/v1/enrollment-tokens/"+created.Token.ID.String(), nil, true); r.Code != 204 {
		t.Fatalf("revoke: %d", r.Code)
	}
	if _, err := e.svc.AuthenticateAgent(agentReq("Bearer " + created.Secret)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked token: %v", err)
	}
	if got := e.findAudit("enrollment_token.revoke"); len(got) != 1 {
		t.Fatalf("audit: %+v", got)
	}
	if got := e.findAudit("enrollment_token.create"); len(got) != 1 {
		t.Fatalf("audit: %+v", got)
	}

	// expiry honored even for cached entries
	exp := e.clock.Now().Add(2 * time.Minute)
	r = admin.req("POST", "/api/v1/enrollment-tokens", map[string]any{"name": "temp", "expiresAt": exp}, true)
	r.JSON(&created)
	if _, err := e.svc.AuthenticateAgent(agentReq("Bearer " + created.Secret)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(3 * time.Minute)
	if _, err := e.svc.AuthenticateAgent(agentReq("Bearer " + created.Secret)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestEnrollmentTokenCachingNegativeAndRevocationWindow(t *testing.T) {
	e := newEnv(t, envOpts{})
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	rq := func() *http.Request {
		r := httptest.NewRequest("GET", "/v1/opamp", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		return r
	}
	if _, err := e.svc.AuthenticateAgent(rq()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("unknown token")
	}
	// create the token behind the service's back: the negative result is cached for 10s
	id := uuid.New()
	if err := e.db.Identity().CreateEnrollmentToken(context.Background(), domain.EnrollmentToken{ID: id, Name: "late", Hash: domain.SHA256([]byte(secret)), CreatedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AuthenticateAgent(rq()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("negative cache entry must still apply")
	}
	e.clock.Advance(11 * time.Second)
	if _, err := e.svc.AuthenticateAgent(rq()); err != nil {
		t.Fatalf("after the negative TTL the new token is found: %v", err)
	}
	// revoked behind the back: still valid within the 60s positive TTL, rejected afterwards
	if err := e.db.Identity().RevokeEnrollmentToken(context.Background(), id, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(30 * time.Second)
	if _, err := e.svc.AuthenticateAgent(rq()); err != nil {
		t.Fatalf("within the positive TTL the cached token is used: %v", err)
	}
	e.clock.Advance(31 * time.Second)
	if _, err := e.svc.AuthenticateAgent(rq()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revocation must apply after 60s: %v", err)
	}
}

func TestAgentAuthWithoutTokenModes(t *testing.T) {
	devE := newEnv(t, envOpts{opamp: func(c *config.OpAMPConfig) { c.RequireEnrollmentToken = false }})
	cred, err := devE.svc.AuthenticateAgent(httptest.NewRequest("GET", "/v1/opamp", nil))
	if err != nil || cred == nil || cred.EnrollmentTokenID != nil || cred.Labels == nil {
		t.Fatalf("dev mode accepts anonymous agents: %v %+v", err, cred)
	}
	// ... but a presented bad token is still rejected
	rq := httptest.NewRequest("GET", "/v1/opamp", nil)
	rq.Header.Set("Authorization", "Bearer myre_"+strings.Repeat("z", 32))
	if _, err := devE.svc.AuthenticateAgent(rq); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("a bad token must not be ignored in dev mode: %v", err)
	}

	prod := newEnv(t, envOpts{})
	if _, err := prod.svc.AuthenticateAgent(httptest.NewRequest("GET", "/v1/opamp", nil)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("production requires a token: %v", err)
	}
}

func TestDevSeed(t *testing.T) {
	e := newEnv(t, envOpts{dev: true, opamp: func(c *config.OpAMPConfig) { c.RequireEnrollmentToken = true }})
	dev := config.DevDefaults().Dev
	if err := e.svc.EnsureDevSeed(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.EnsureDevSeed(context.Background(), dev); err != nil {
		t.Fatalf("EnsureDevSeed must be idempotent: %v", err)
	}
	rq := httptest.NewRequest("GET", "/v1/opamp", nil)
	rq.Header.Set("Authorization", "Bearer "+dev.SeedEnrollmentToken)
	cred, err := e.svc.AuthenticateAgent(rq)
	if err != nil || cred.EnrollmentTokenID == nil {
		t.Fatalf("seed token must authenticate agents: %v", err)
	}
	toks, _ := e.db.Identity().ListEnrollmentTokens(context.Background())
	if len(toks) != 1 || toks[0].DisplayPrefix != dev.SeedEnrollmentToken[:12] {
		t.Fatalf("exactly one seed token expected: %+v", toks)
	}
	users, _ := e.db.Identity().ListUsers(context.Background())
	if len(users) != 3 {
		t.Fatalf("dev users: %+v", users)
	}
	bad := dev
	bad.SeedEnrollmentToken = "no-prefix"
	if err := e.svc.EnsureDevSeed(context.Background(), bad); err == nil {
		t.Fatal("a seed token without myre_ prefix can never authenticate: reject")
	}
	bad.SeedEnrollmentToken = domain.EnrollmentTokenPrefix + "short"
	if err := e.svc.EnsureDevSeed(context.Background(), bad); err == nil {
		t.Fatal("a seed token that is not of the documented form can never authenticate: reject")
	}
}

func TestLoadOrCreateDownloadSecret(t *testing.T) {
	e := newEnv(t, envOpts{})
	kv := e.db.KV()
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([][]byte, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := authn.LoadOrCreateDownloadSecret(ctx, config.AuthConfig{}, kv)
			if err != nil {
				t.Error(err)
			}
			results[i] = s
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if len(r) != 32 || string(r) != string(results[0]) {
			t.Fatalf("all replicas must converge on one 32 byte secret: %x vs %x", r, results[0])
		}
	}
	again, _ := authn.LoadOrCreateDownloadSecret(ctx, config.AuthConfig{}, kv)
	if string(again) != string(results[0]) {
		t.Fatal("secret must be stable")
	}

	f := filepath.Join(t.TempDir(), "secret")
	long := strings.Repeat("k", 40)
	if err := os.WriteFile(f, []byte(long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := authn.LoadOrCreateDownloadSecret(ctx, config.AuthConfig{DownloadTokenSecretFile: f}, kv)
	if err != nil || string(fromFile) != long {
		t.Fatalf("file secret: %q %v", fromFile, err)
	}
	_ = os.WriteFile(f, []byte("short"), 0o600)
	if _, err := authn.LoadOrCreateDownloadSecret(ctx, config.AuthConfig{DownloadTokenSecretFile: f}, kv); err == nil {
		t.Fatal("a short secret file must be rejected")
	}
	if _, err := authn.LoadOrCreateDownloadSecret(ctx, config.AuthConfig{DownloadTokenSecretFile: f + ".missing"}, kv); err == nil {
		t.Fatal("missing secret file")
	}
}

func TestPrincipalCacheDoesNotHideRoleChanges(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	u := e.newClient()
	u.mustDevLogin("viewer")
	var me dto.Me
	u.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	var users api.List[domain.User]
	admin.req("GET", "/api/v1/users", nil, false).JSON(&users)
	var id uuid.UUID
	for _, x := range users.Items {
		if x.Name == "viewer" {
			id = x.ID
		}
	}
	admin.req("PATCH", "/api/v1/users/"+id.String(), map[string]any{"role": "admin"}, true)
	e.clock.Advance(6 * time.Second)
	u.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleAdmin {
		t.Fatalf("role change must be visible after the cache TTL, got %s", me.Role)
	}
}
