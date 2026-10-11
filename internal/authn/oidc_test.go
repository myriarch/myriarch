// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

func oidcAuth(idp *fakeIdP) func(c *config.AuthConfig) {
	return func(c *config.AuthConfig) {
		c.DevLogin.Enabled = false
		c.OIDC = config.OIDCConfig{
			Enabled: true, IssuerURL: idp.srv.URL, ClientID: idp.clientID, ClientSecret: idp.secret,
			Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", DisplayName: "Test SSO",
			RoleMapping: config.RoleMapping{Admin: []string{"fleet-admins"}, Operator: []string{"fleet-ops"}, Viewer: []string{"fleet-viewers"}},
			DefaultRole: "",
		}
	}
}

// startLogin performs GET /auth/login and returns the IdP callback URL (after
// the fake IdP authorized the request).
func startLogin(t *testing.T, c *client, idp *fakeIdP, redirect string) (callback string, loginResp result) {
	t.Helper()
	path := "/api/v1/auth/login"
	if redirect != "" {
		path += "?redirect=" + url.QueryEscape(redirect)
	}
	r := c.req("GET", path, nil, false)
	if r.Code != http.StatusFound {
		t.Fatalf("login start: %d %s", r.Code, r.Body)
	}
	cb, err := idp.authorize(r.Header.Get("Location"))
	if err != nil {
		t.Fatalf("idp rejected the authorization request: %v", err)
	}
	return cb, r
}

func callbackPath(t *testing.T, cb string) string {
	t.Helper()
	u, err := url.Parse(cb)
	if err != nil {
		t.Fatal(err)
	}
	return u.RequestURI()
}

func TestOIDCAuthorizationCodeFlowWithPKCE(t *testing.T) {
	idp := newIdP(t)
	idp.claims = func(nonce string) map[string]any {
		return map[string]any{"groups": []string{"unrelated", "fleet-ops", "fleet-viewers"}}
	}
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	c := e.newClient()

	var info dto.AuthConfigInfo
	c.req("GET", "/api/v1/auth/config", nil, false).JSON(&info)
	if !info.OIDCEnabled || info.OIDCDisplayName != "Test SSO" || info.DevLoginEnabled {
		t.Fatalf("auth config: %+v", info)
	}

	cb, start := startLogin(t, c, idp, "/rollouts?x=1")
	if sc := start.Header.Get("Set-Cookie"); !strings.Contains(sc, "myriarch_oidc=") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Lax") || !strings.Contains(sc, "Max-Age=600") {
		t.Fatalf("state cookie attributes: %s", sc)
	}
	loc, _ := url.Parse(start.Header.Get("Location"))
	if loc.Query().Get("redirect_uri") != e.base+"/api/v1/auth/callback" {
		t.Fatalf("redirect_uri = %s", loc.Query().Get("redirect_uri"))
	}
	if start.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the login redirect must not be cached")
	}

	r := c.req("GET", callbackPath(t, cb), nil, false)
	if r.Code != http.StatusFound || r.Header.Get("Location") != "/rollouts?x=1" {
		t.Fatalf("callback: %d location=%q body=%s", r.Code, r.Header.Get("Location"), r.Body)
	}
	if idp.tokenReq.Load() != 1 {
		t.Fatal("exactly one token exchange expected")
	}
	if c.cookie(authn.SessionCookie) == "" || c.cookie(authn.CSRFCookie) == "" {
		t.Fatal("session cookies must be set")
	}
	var me dto.Me
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleOperator || me.AuthMethod != "oidc" || me.Email != "una@example.com" || me.Name != "Una User" {
		t.Fatalf("highest matching role must win (operator over viewer; groups match exactly): %+v", me)
	}
	user, err := e.db.Identity().ListUsers(context.Background())
	if err != nil || len(user) != 1 || user[0].Subject != "user-1" || user[0].RoleSource != "mapping" || user[0].LastLoginAt == nil {
		t.Fatalf("user: %+v %v", user, err)
	}
	ev := e.findAudit("auth.login")
	if len(ev) != 1 || ev[0].Result != "success" || ev[0].Details["method"] != "oidc" {
		t.Fatalf("login audit: %+v", ev)
	}

	// the state cookie is single use: replaying the callback fails
	r = c.req("GET", callbackPath(t, cb), nil, false)
	if r.Code != http.StatusFound || r.Header.Get("Location") != "/login?error=oidc_error" {
		t.Fatalf("replayed callback: %d %s", r.Code, r.Header.Get("Location"))
	}
}

func TestOIDCRoleMappingAndAccessDenied(t *testing.T) {
	idp := newIdP(t)
	groups := []string{}
	idp.claims = func(string) map[string]any { return map[string]any{"groups": groups} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})

	login := func() (result, *client) {
		c := e.newClient()
		cb, _ := startLogin(t, c, idp, "")
		return c.req("GET", callbackPath(t, cb), nil, false), c
	}

	// no matching group and no default role: denied
	r, c := login()
	if r.Code != 302 || r.Header.Get("Location") != "/login?error=access_denied" || c.cookie(authn.SessionCookie) != "" {
		t.Fatalf("no role: %d %s", r.Code, r.Header.Get("Location"))
	}
	if ev := e.findAudit("auth.login"); len(ev) != 1 || ev[0].Result != "failure" || ev[0].Details["reason"] != "no_role" {
		t.Fatalf("denied login audit: %+v", ev)
	}
	if users, _ := e.db.Identity().ListUsers(context.Background()); len(users) != 0 {
		t.Fatal("denied users must not be created")
	}

	groups = []string{"fleet-viewers"}
	r, c = login()
	if r.Header.Get("Location") != "/" {
		t.Fatalf("viewer login: %s", r.Header.Get("Location"))
	}
	var me dto.Me
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleViewer {
		t.Fatalf("viewer: %+v", me)
	}

	// group changes are applied at the next login (roleSource mapping)
	groups = []string{"fleet-admins"}
	_, c = login()
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleAdmin {
		t.Fatalf("mapped role must be refreshed at login: %+v", me)
	}

	// a manual override by an admin survives later logins
	users, _ := e.db.Identity().ListUsers(context.Background())
	if err := e.db.Identity().SetUserRole(context.Background(), users[0].ID, domain.RoleViewer, "manual"); err != nil {
		t.Fatal(err)
	}
	groups = []string{"fleet-admins"}
	_, c = login()
	e.clock.Advance(10 * time.Second)
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleViewer {
		t.Fatalf("manual role must survive login, got %s", me.Role)
	}

	// disabled accounts are refused
	if err := e.db.Identity().SetUserDisabled(context.Background(), users[0].ID, true); err != nil {
		t.Fatal(err)
	}
	r, _ = login()
	if r.Header.Get("Location") != "/login?error=access_denied" {
		t.Fatalf("disabled user: %s", r.Header.Get("Location"))
	}
}

func TestOIDCDefaultRoleAndGroupClaimShapes(t *testing.T) {
	idp := newIdP(t)
	e := newEnv(t, envOpts{httpCli: idp.srv.Client(), auth: func(c *config.AuthConfig) {
		oidcAuth(idp)(c)
		c.OIDC.DefaultRole = domain.RoleViewer
		c.OIDC.GroupsClaim = "realm_access.roles" // Keycloak style nested claim
	}})
	idp.claims = func(string) map[string]any {
		return map[string]any{"realm_access": map[string]any{"roles": []string{"fleet-admins"}}}
	}
	c := e.newClient()
	cb, _ := startLogin(t, c, idp, "")
	c.req("GET", callbackPath(t, cb), nil, false)
	var me dto.Me
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleAdmin {
		t.Fatalf("nested claim: %+v", me)
	}

	// missing claim: default role
	idp.claims = func(string) map[string]any { return map[string]any{"sub": "other-user"} }
	c2 := e.newClient()
	cb, _ = startLogin(t, c2, idp, "")
	c2.req("GET", callbackPath(t, cb), nil, false)
	c2.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleViewer {
		t.Fatalf("default role: %+v", me)
	}

	// single string claim (Entra app roles can be a string array, some IdPs send a string)
	idp.claims = func(string) map[string]any {
		return map[string]any{"sub": "third", "realm_access": map[string]any{"roles": "fleet-ops"}}
	}
	c3 := e.newClient()
	cb, _ = startLogin(t, c3, idp, "")
	c3.req("GET", callbackPath(t, cb), nil, false)
	c3.req("GET", "/api/v1/auth/me", nil, false).JSON(&me)
	if me.Role != domain.RoleOperator {
		t.Fatalf("string claim: %+v", me)
	}
}

func TestOIDCSecurityChecks(t *testing.T) {
	idp := newIdP(t)
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"fleet-admins"}} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})

	// Failures before a valid signed login state are audited once per minute and address and use up the failure
	// budget: every scenario starts a minute later.
	tick := func() { e.clock.Advance(61 * time.Second) }
	expectFail := func(name, wantReason string, r result) {
		t.Helper()
		if r.Code != http.StatusFound || r.Header.Get("Location") != "/login?error=oidc_error" {
			t.Errorf("%s: %d location=%q", name, r.Code, r.Header.Get("Location"))
		}
		found := false
		for _, ev := range e.findAudit("auth.login") {
			if ev.Result == "failure" && ev.Details["reason"] == wantReason {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no failure audit with reason %q", name, wantReason)
		}
	}
	assertNoSession := func(c *client, name string) {
		t.Helper()
		if c.cookie(authn.SessionCookie) != "" {
			t.Errorf("%s: session was created", name)
		}
	}

	tick()
	// 1. state mismatch (CSRF / login injection)
	c := e.newClient()
	cb, _ := startLogin(t, c, idp, "")
	u, _ := url.Parse(cb)
	q := u.Query()
	q.Set("state", "attacker-state")
	u.RawQuery = q.Encode()
	expectFail("state mismatch", "state_mismatch", c.req("GET", u.RequestURI(), nil, false))
	assertNoSession(c, "state mismatch")

	tick()
	// 2. no state cookie (callback not initiated by this browser)
	c = e.newClient()
	other := e.newClient()
	cb, _ = startLogin(t, other, idp, "")
	expectFail("missing cookie", "missing_state_cookie", c.req("GET", callbackPath(t, cb), nil, false))
	assertNoSession(c, "missing cookie")

	tick()
	// 3. tampered state cookie (signature)
	c = e.newClient()
	cb, start := startLogin(t, c, idp, "/x")
	raw := start.Header.Get("Set-Cookie")
	val := strings.TrimPrefix(strings.SplitN(raw, ";", 2)[0], "myriarch_oidc=")
	parts := strings.SplitN(val, ".", 2)
	forged := parts[0] + "." + strings.Repeat("A", len(parts[1]))
	rq := callbackPath(t, cb)
	r := c.req("GET", rq, nil, false, "Cookie", "myriarch_oidc="+forged)
	expectFail("forged cookie", "invalid_state_cookie", r)

	tick()
	// 4. nonce mismatch (token replay from another login)
	idp.badNonce = true
	c = e.newClient()
	cb, _ = startLogin(t, c, idp, "")
	expectFail("nonce mismatch", "nonce_mismatch", c.req("GET", callbackPath(t, cb), nil, false))
	assertNoSession(c, "nonce mismatch")
	idp.badNonce = false

	tick()
	// 5. wrong PKCE verifier: the IdP refuses the code exchange. Simulate with a cookie of another login.
	c = e.newClient()
	other = e.newClient()
	_, start2 := startLogin(t, other, idp, "")
	cb, _ = startLogin(t, c, idp, "")
	val2 := strings.TrimPrefix(strings.SplitN(start2.Header.Get("Set-Cookie"), ";", 2)[0], "myriarch_oidc=")
	// the cookie of login 2 carries another state: even with a matching IdP code the state check fails first
	r = c.req("GET", callbackPath(t, cb), nil, false, "Cookie", "myriarch_oidc="+val2)
	if r.Code != 302 || r.Header.Get("Location") != "/login?error=oidc_error" {
		t.Errorf("mixed-up cookie: %d %s", r.Code, r.Header.Get("Location"))
	}
	assertNoSession(c, "mixed cookie")

	tick()
	// 6. IdP error response
	c = e.newClient()
	_, _ = startLogin(t, c, idp, "")
	r = c.req("GET", "/api/v1/auth/callback?error=access_denied&error_description=nope", nil, false)
	if r.Code != 302 || r.Header.Get("Location") != "/login?error=access_denied" {
		t.Errorf("idp access_denied: %d %s", r.Code, r.Header.Get("Location"))
	}
	r = c.req("GET", "/api/v1/auth/callback?error=server_error", nil, false)
	if r.Header.Get("Location") != "/login?error=oidc_error" {
		t.Errorf("idp server_error: %s", r.Header.Get("Location"))
	}
	tick()
	// 7. missing code
	c = e.newClient()
	cb, _ = startLogin(t, c, idp, "")
	u, _ = url.Parse(cb)
	q = u.Query()
	q.Del("code")
	u.RawQuery = q.Encode()
	expectFail("missing code", "missing_code", c.req("GET", u.RequestURI(), nil, false))
	tick()
	// 8. invalid code
	c = e.newClient()
	cb, _ = startLogin(t, c, idp, "")
	u, _ = url.Parse(cb)
	q = u.Query()
	q.Set("code", "forged")
	u.RawQuery = q.Encode()
	expectFail("invalid code", "token_exchange", c.req("GET", u.RequestURI(), nil, false))
	// 9. expired login attempt
	c = e.newClient()
	cb, _ = startLogin(t, c, idp, "")
	e.clock.Advance(11 * time.Minute)
	expectFail("expired", "expired_state", c.req("GET", callbackPath(t, cb), nil, false))

	// no failure above created a user
	if users, _ := e.db.Identity().ListUsers(context.Background()); len(users) != 0 {
		t.Fatalf("failed logins created users: %+v", users)
	}
}

func TestOIDCRedirectIsSanitized(t *testing.T) {
	idp := newIdP(t)
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"fleet-viewers"}} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	for redirect, want := range map[string]string{
		"/agents?x=1":               "/agents?x=1",
		"":                          "/",
		"//evil.example.com":        "/",
		"https://evil.example.com/": "/",
		"/\\evil.example.com":       "/",
		"/agents\\evil.example.com": "/", // backslash later in the path
		"/\t/evil.example.com":      "/", // control character right after the leading slash
		"agents":                    "/",
		"/ok\r\nSet-Cookie: a=b":    "/",
		"javascript:alert(1)":       "/",
		"/%2f%2fevil":               "/%2f%2fevil", // path stays on this origin
	} {
		c := e.newClient()
		cb, _ := startLogin(t, c, idp, redirect)
		r := c.req("GET", callbackPath(t, cb), nil, false)
		if got := r.Header.Get("Location"); got != want {
			t.Errorf("redirect %q -> %q, want %q", redirect, got, want)
		}
	}
}

func TestOIDCDiscoveryRetriedInBackground(t *testing.T) {
	old := authn.DiscoveryRetryInitial
	authn.DiscoveryRetryInitial = 100 * time.Millisecond
	defer func() { authn.DiscoveryRetryInitial = old }()

	idp := newIdP(t)
	idp.down.Store(true)
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"fleet-viewers"}} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	c := e.newClient()

	// OIDC is reported as enabled even though the IdP is down
	var info dto.AuthConfigInfo
	c.req("GET", "/api/v1/auth/config", nil, false).JSON(&info)
	if !info.OIDCEnabled {
		t.Fatal("auth config must report OIDC enabled while discovery is pending")
	}
	r := c.req("GET", "/api/v1/auth/login", nil, false)
	if r.Code != 503 || r.Problem().Code != api.ErrorCodeUnavailable || r.Header.Get("Retry-After") == "" {
		t.Fatalf("login while IdP is down: %d %s", r.Code, r.Body)
	}
	idp.down.Store(false)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r = c.req("GET", "/api/v1/auth/login", nil, false)
		if r.Code == http.StatusFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery never recovered: %d %s", r.Code, r.Body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestOIDCDisabledEndpointsAre404(t *testing.T) {
	e := newEnv(t, envOpts{})
	c := e.newClient()
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/callback?code=x&state=y"} {
		if r := c.req("GET", path, nil, false); r.Code != 404 {
			t.Errorf("%s with OIDC disabled: %d", path, r.Code)
		}
	}
}

func TestSecureCookieFlagFollowsConfig(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.CookieSecure = true }})
	r := e.newClient().devLogin("admin", "admin-pw")
	var n int
	for _, line := range r.Header.Values("Set-Cookie") {
		if strings.Contains(line, "; Secure") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("both cookies must be Secure when auth.cookieSecure is set: %v", r.Header.Values("Set-Cookie"))
	}
}
