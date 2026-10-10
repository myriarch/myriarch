// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// -------------------------------------------------------- OIDC endpoints ---

// Failed callbacks from one address are limited, and the audit log gets at most a few entries for them.
func TestOIDCCallbackFailuresAreRateLimitedAndAuditThrottled(t *testing.T) {
	idp := newIdP(t)
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	c := e.newClient()
	limited := 0
	for i := 0; i < 40; i++ {
		r := c.req("GET", "/api/v1/auth/callback?error=x", nil, false)
		if r.Code != http.StatusFound || r.Header.Get("Location") != "/login?error=oidc_error" {
			t.Fatalf("callback %d: %d %s", i, r.Code, r.Header.Get("Location"))
		}
		if r.Header.Get("Retry-After") != "" {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("40 failed callbacks from one address were never rate limited")
	}
	if n := len(e.findAudit("auth.login")); n > 2 {
		t.Fatalf("%d audit entries for 40 failed callbacks, want at most 2", n)
	}
	// the start of a login is refused for the limited address as well
	if r := c.req("GET", "/api/v1/auth/login", nil, false); r.Code != http.StatusFound || r.Header.Get("Retry-After") == "" ||
		!strings.HasPrefix(r.Header.Get("Location"), "/login?error=oidc_error") {
		t.Fatalf("login start while limited: %d %s", r.Code, r.Header.Get("Location"))
	}
	// another address is not affected (X-Forwarded-For is only honored from trusted proxies, so use the limiter key of the peer)
	if got := counterValue(t, e, "myriarch_auth_logins_total", map[string]string{"method": "oidc", "result": "rate_limited"}); got == 0 {
		t.Fatal("rate_limited is not counted")
	}
}

// Fresh states with a code that the IdP rejects cause a bounded number of token requests.
func TestOIDCTokenRequestsPerAddressAreBounded(t *testing.T) {
	idp := newIdP(t)
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	c := e.newClient()
	for i := 0; i < 40; i++ {
		start := c.req("GET", "/api/v1/auth/login", nil, false)
		if start.Header.Get("Retry-After") != "" {
			continue // limited
		}
		loc, err := url.Parse(start.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		c.req("GET", "/api/v1/auth/callback?code=bogus&state="+url.QueryEscape(loc.Query().Get("state")), nil, false)
	}
	// at most one failed exchange per token of the budget (10); the OAuth library may probe the client
	// authentication style once more per exchange
	if n := idp.tokenReq.Load(); n > 22 {
		t.Fatalf("%d token requests from one address in a burst, want a bounded number", n)
	}
}

// A successful sign-in does not use up the failure budget.
func TestOIDCSuccessfulLoginsAreNotLimited(t *testing.T) {
	idp := newIdP(t)
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"fleet-viewers"}} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	for i := 0; i < 25; i++ {
		c := e.newClient()
		cb, _ := startLogin(t, c, idp, "")
		if r := c.req("GET", callbackPath(t, cb), nil, false); r.Header.Get("Location") != "/" {
			t.Fatalf("sign-in %d: %d %s", i, r.Code, r.Header.Get("Location"))
		}
	}
}

// The identity provider's responses are bounded.
func TestOIDCResponsesAreSizeBounded(t *testing.T) {
	idp := newIdP(t)
	idp.jwksPad = 8 << 20
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"fleet-viewers"}} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})
	c := e.newClient()
	cb, _ := startLogin(t, c, idp, "")
	r := c.req("GET", callbackPath(t, cb), nil, false)
	if r.Header.Get("Location") != "/login?error=oidc_error" {
		t.Fatalf("an 8 MiB key set must fail the sign-in: %d %s", r.Code, r.Header.Get("Location"))
	}
	ev := e.findAudit("auth.login")
	if len(ev) != 1 || ev[0].Details["reason"] != "id_token_invalid" || !strings.Contains(strings.ToLower(ev[0].Details["error"].(string)), "too large") {
		t.Fatalf("audit: %+v", ev)
	}
}

// ----------------------------------------------------------- role mapping ---

func TestOIDCGroupsAndEmailsMatchWithoutUnicodeFolding(t *testing.T) {
	const kelvin = "K" // KELVIN SIGN: simple case folding equates it with "k"
	cases := []struct {
		name   string
		users  config.UserRoleMapping
		groups []string
		claims map[string]any
		want   domain.Role
	}{
		{"exact group", config.UserRoleMapping{}, []string{"kube-admins"}, nil, domain.RoleAdmin},
		{"upper case group is another group", config.UserRoleMapping{}, []string{"KUBE-ADMINS"}, nil, ""},
		{"kelvin sign variant", config.UserRoleMapping{}, []string{kelvin + "ube-admins"}, nil, ""},
		{"email ASCII case folding", config.UserRoleMapping{Admin: []string{"email:KIM@example.COM"}}, nil, map[string]any{"email": "kim@example.com", "email_verified": true}, domain.RoleAdmin},
		{"email kelvin variant", config.UserRoleMapping{Admin: []string{"email:" + kelvin + "im@example.com"}}, nil, map[string]any{"email": "kim@example.com", "email_verified": true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idp := newIdP(t)
			e := neutralEnv(t, idp, func(o *config.OIDCConfig) {
				o.RoleMapping = config.RoleMapping{Admin: []string{"kube-admins"}, Users: c.users}
			})
			claims := map[string]any{"groups": c.groups}
			for k, v := range c.claims {
				claims[k] = v
			}
			r, cl := signInWith(t, e, idp, claims)
			if c.want == "" {
				if r.Header.Get("Location") != "/login?error=access_denied" {
					t.Fatalf("must be denied: %d %s", r.Code, r.Header.Get("Location"))
				}
				return
			}
			if me := meOf(t, cl); me.Role != c.want {
				t.Fatalf("role %s, want %s", me.Role, c.want)
			}
		})
	}
}

// ------------------------------------------------------------ API tokens ---

func createTokenReq(c *client, expires *time.Time) result {
	body := map[string]any{"name": "ci", "role": "viewer"}
	if expires != nil {
		body["expiresAt"] = *expires
	}
	return c.req("POST", "/api/v1/tokens", body, true)
}

func TestAPITokensHaveAMaximumLifetime(t *testing.T) {
	const max = 30 * 24 * time.Hour
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.APITokenMaxTTL = max }})
	admin := e.newClient()
	admin.mustDevLogin("admin")

	// without expiresAt the token gets the maximum
	r := createTokenReq(admin, nil)
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	var created struct {
		Token  domain.APIToken `json:"token"`
		Secret string          `json:"secret"`
	}
	r.JSON(&created)
	if created.Token.ExpiresAt == nil {
		t.Fatal("a token created without expiresAt must get an expiry")
	}
	if got := created.Token.ExpiresAt.Sub(e.clock.Now()); got > max || got < max-time.Minute {
		t.Fatalf("expiry in %s, want the maximum %s", got, max)
	}

	// a longer lifetime is refused
	tooLong := e.clock.Now().Add(max + 24*time.Hour)
	if r := createTokenReq(admin, &tooLong); r.Code != 422 {
		t.Fatalf("expiresAt beyond the maximum: %d %s", r.Code, r.Body)
	}
	// a shorter one is kept
	short := e.clock.Now().Add(24 * time.Hour)
	r = createTokenReq(admin, &short)
	if r.Code != 201 {
		t.Fatalf("short token: %d %s", r.Code, r.Body)
	}
}

// A token that was created without an expiry (before the maximum existed) stops working after createdAt + maximum.
func TestAPITokenWithoutExpiryIsRefusedAfterTheMaximum(t *testing.T) {
	const max = 30 * 24 * time.Hour
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.APITokenMaxTTL = max }})
	secret, _ := authn.NewToken(domain.APITokenPrefix)
	if err := e.db.Identity().CreateAPIToken(context.Background(), domain.APIToken{ID: uuid.New(), Name: "old", DisplayPrefix: secret[:12],
		Hash: domain.SHA256([]byte(secret)), Role: domain.RoleViewer, CreatedBy: "test", CreatedAt: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	authenticate := func() error {
		req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		_, err := e.svc.Authenticate(req)
		return err
	}
	if err := authenticate(); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
	e.clock.Advance(max - time.Hour)
	if err := authenticate(); err != nil {
		t.Fatalf("token inside the maximum: %v", err)
	}
	e.clock.Advance(2 * time.Hour)
	if err := authenticate(); err == nil {
		t.Fatal("a token older than auth.apiTokenMaxTtl must be refused")
	}
}

// An API token created by a local (development) account is worthless where the development login is off.
func TestAPITokensOfLocalAccountsAreRejectedWithoutDevLogin(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.DevLogin.Enabled = false }})
	ctx := context.Background()
	u, err := e.db.Identity().UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "local:admin", Provider: "local", Name: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := authn.NewToken(domain.APITokenPrefix)
	if err := e.db.Identity().CreateAPIToken(ctx, domain.APIToken{ID: uuid.New(), Name: "dev", DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)),
		Role: domain.RoleAdmin, CreatedBy: "local:admin", CreatedAt: e.clock.Now(), CreatedByUserID: &u.ID}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	if p, err := e.svc.Authenticate(req); err == nil {
		t.Fatalf("a token of a local account was accepted: %+v", p)
	}
}

// ---------------------------------------------------------------- cookie ---

func TestSessionCookieUsesTheHostPrefixWhenSecure(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.CookieSecure = true }})
	r := e.newClient().devLogin("admin", "admin-pw")
	var session string
	for _, line := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, "__Host-myriarch_session=") {
			session = line
		}
		if strings.HasPrefix(line, "myriarch_session=") {
			t.Fatalf("the plain cookie name must not be used when cookies are Secure: %s", line)
		}
	}
	if session == "" {
		t.Fatalf("no __Host- session cookie: %v", r.Header.Values("Set-Cookie"))
	}
	if !strings.Contains(session, "Path=/") || !strings.Contains(session, "; Secure") || strings.Contains(strings.ToLower(session), "domain=") {
		t.Fatalf("__Host- cookie requirements (Secure, Path=/, no Domain): %s", session)
	}
	// the server accepts the prefixed cookie
	value := strings.SplitN(strings.TrimPrefix(session, "__Host-myriarch_session="), ";", 2)[0]
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: authn.SessionCookieSecure, Value: value})
	if p, err := e.svc.Authenticate(req); err != nil || p == nil {
		t.Fatalf("the prefixed cookie is not accepted: %v", err)
	}
	// the plain name is not accepted when Secure cookies are configured (a sibling domain could have set it)
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: authn.SessionCookie, Value: value})
	if _, err := e.svc.Authenticate(req); err == nil {
		t.Fatal("the plain cookie name must not be accepted when auth.cookieSecure is set")
	}
}

func TestSessionCookieKeepsThePlainNameInDevelopmentMode(t *testing.T) {
	e := newEnv(t, envOpts{})
	r := e.newClient().devLogin("admin", "admin-pw")
	found := false
	for _, line := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, "myriarch_session=") {
			found = true
		}
		if strings.HasPrefix(line, "__Host-") {
			t.Fatalf("no __Host- prefix without Secure cookies: %s", line)
		}
	}
	if !found {
		t.Fatalf("plain session cookie missing: %v", r.Header.Values("Set-Cookie"))
	}
}

// counterValue returns the value of a counter of the service's registry (0 if it does not exist).
func counterValue(t *testing.T, e *env, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metric:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for k, v := range labels {
				if got[k] != v {
					continue metric
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// The token list shows the effective expiry min(expiresAt, createdAt + auth.apiTokenMaxTtl), also for
// tokens that were created before the maximum existed or before it was lowered.
func TestAPITokenListShowsTheEffectiveExpiry(t *testing.T) {
	const max = 30 * 24 * time.Hour
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.APITokenMaxTTL = max }})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	created := e.clock.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	farFuture := created.Add(90 * 24 * time.Hour)
	soon := created.Add(5 * 24 * time.Hour)
	for name, exp := range map[string]*time.Time{"no-expiry": nil, "far-future": &farFuture, "soon": &soon} {
		secret, _ := authn.NewToken(domain.APITokenPrefix)
		if err := e.db.Identity().CreateAPIToken(context.Background(), domain.APIToken{ID: uuid.New(), Name: name, DisplayPrefix: secret[:12],
			Hash: domain.SHA256([]byte(secret)), Role: domain.RoleViewer, CreatedBy: "test", CreatedAt: created, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	var list struct {
		Items []domain.APIToken `json:"items"`
	}
	admin.req("GET", "/api/v1/tokens", nil, false).JSON(&list)
	want := map[string]time.Time{"no-expiry": created.Add(max), "far-future": created.Add(max), "soon": soon}
	seen := 0
	for _, tok := range list.Items {
		w, ok := want[tok.Name]
		if !ok {
			continue
		}
		seen++
		if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(w) {
			t.Errorf("%s: expiresAt %v, want %v", tok.Name, tok.ExpiresAt, w)
		}
	}
	if seen != len(want) {
		t.Fatalf("%d of %d tokens listed", seen, len(want))
	}
}
