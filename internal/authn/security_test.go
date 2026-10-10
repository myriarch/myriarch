// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

func memBus() eventbus.Bus { return eventbus.NewMemory("test", 0) }

// AUTH-ROLE-1: IdP users without a mapped role get no access (deny by default).
func TestUnmappedOIDCUserIsDeniedByDefault(t *testing.T) {
	if def := config.Default().Auth.OIDC.DefaultRole; def != "" {
		t.Fatalf("the default role must be empty (deny), is %q", def)
	}
	idp := newIdP(t)
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"unrelated"}} }
	e := newEnv(t, envOpts{httpCli: idp.srv.Client(), auth: func(c *config.AuthConfig) {
		oidcAuth(idp)(c)
		c.OIDC.DefaultRole = config.Default().Auth.OIDC.DefaultRole // production default
	}})
	c := e.newClient()
	cb, _ := startLogin(t, c, idp, "")
	r := c.req("GET", callbackPath(t, cb), nil, false)
	if r.Code != http.StatusFound || r.Header.Get("Location") != "/login?error=access_denied" {
		t.Fatalf("callback: %d %s", r.Code, r.Header.Get("Location"))
	}
	if c.cookie(authn.SessionCookie) != "" {
		t.Fatal("no session for an unmapped user")
	}
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 401 {
		t.Fatalf("/auth/me: %d", r.Code)
	}
	found := false
	for _, ev := range e.findAudit("auth.login") {
		if ev.Result == "failure" && ev.Details["reason"] == "no_role" {
			found = true
		}
	}
	if !found {
		t.Fatal("the denial must be audited")
	}
}

// AUTH-OIDC-1: the login state can be used once (on all replicas), local: subjects are reserved.
func TestOIDCLoginStateIsSingleUseAndLocalSubjectsAreRejected(t *testing.T) {
	idp := newIdP(t)
	idp.claims = func(string) map[string]any { return map[string]any{"groups": []string{"fleet-admins"}} }
	e := newEnv(t, envOpts{auth: oidcAuth(idp), httpCli: idp.srv.Client()})

	c := e.newClient()
	cb, start := startLogin(t, c, idp, "")
	setCookie := start.Header.Get("Set-Cookie")
	cookie := strings.TrimPrefix(strings.SplitN(setCookie, ";", 2)[0], "")
	if r := c.req("GET", callbackPath(t, cb), nil, false); r.Code != http.StatusFound || r.Header.Get("Location") == "/login?error=oidc_error" {
		t.Fatalf("first callback: %d %s", r.Code, r.Header.Get("Location"))
	}
	// the same state cookie and code arrive again (copied cookie, other browser)
	other := e.newClient()
	r := other.req("GET", callbackPath(t, cb), nil, false, "Cookie", cookie)
	if r.Code != http.StatusFound || r.Header.Get("Location") != "/login?error=oidc_error" {
		t.Fatalf("replayed state: %d %s", r.Code, r.Header.Get("Location"))
	}
	if other.cookie(authn.SessionCookie) != "" {
		t.Fatal("a replayed login state must not create a session")
	}
	replayed := false
	for _, ev := range e.findAudit("auth.login") {
		if ev.Details["reason"] == "state_replayed" {
			replayed = true
		}
	}
	if !replayed {
		t.Fatal("replay must be audited")
	}

	// OIDC subjects cannot impersonate local accounts
	idp.claims = func(string) map[string]any {
		return map[string]any{"sub": "local:admin", "groups": []string{"fleet-admins"}}
	}
	c2 := e.newClient()
	cb, _ = startLogin(t, c2, idp, "")
	r = c2.req("GET", callbackPath(t, cb), nil, false)
	if r.Header.Get("Location") != "/login?error=oidc_error" || c2.cookie(authn.SessionCookie) != "" {
		t.Fatalf("reserved subject: %d %s", r.Code, r.Header.Get("Location"))
	}
}

// AUTH-OIDC-1: users are keyed by issuer and subject.
func TestUsersAreIdentifiedByIssuerAndSubject(t *testing.T) {
	e := newEnv(t, envOpts{})
	ids := e.db.Identity()
	ctx := context.Background()
	a, err := ids.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "same-sub", Provider: "oidc", Issuer: "https://idp-a.example.com", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ids.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "same-sub", Provider: "oidc", Issuer: "https://idp-b.example.com", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || b.Role != domain.RoleAdmin || a.Role != domain.RoleViewer {
		t.Fatalf("the same subject of two issuers must be two users: %+v %+v", a, b)
	}
	// a user created before the issuer was recorded adopts it at the next login
	legacy, _ := ids.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "old-sub", Provider: "oidc", Role: domain.RoleViewer})
	again, err := ids.UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "old-sub", Provider: "oidc", Issuer: "https://idp-a.example.com", Role: domain.RoleViewer})
	if err != nil || again.ID != legacy.ID || again.Issuer != "https://idp-a.example.com" {
		t.Fatalf("legacy user: %+v %v", again, err)
	}
}

// AUTH-SESS-2: an absolute lifetime ends even continuously active sessions.
func TestSessionAbsoluteMaxAge(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.SessionTTL = time.Hour; c.SessionMaxAge = 3 * time.Hour }})
	c := e.newClient()
	c.mustDevLogin("viewer")
	for i := 0; i < 4; i++ { // 4 x 50 min of activity: the idle timeout never fires
		e.clock.Advance(50 * time.Minute)
		if i < 3 {
			if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 200 {
				t.Fatalf("activity %d: %d", i, r.Code)
			}
			time.Sleep(300 * time.Millisecond) // async touch
		}
	}
	// 200 minutes after login: past the 3h maximum although the session was used 50 minutes ago
	if r := c.req("GET", "/api/v1/auth/me", nil, false); r.Code != 401 {
		t.Fatalf("a session past its maximum age must be rejected: %d", r.Code)
	}
	// the sliding expiry never moves beyond created_at + maxAge
	c2 := e.newClient()
	c2.mustDevLogin("viewer")
	id := authnSessionID(c2.cookie(authn.SessionCookie))
	sess, err := e.db.Identity().GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.Identity().TouchSession(context.Background(), id, sess.CreatedAt.Add(2*time.Hour+50*time.Minute), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	sess, _ = e.db.Identity().GetSession(context.Background(), id)
	if limit := sess.CreatedAt.Add(3 * time.Hour); sess.ExpiresAt.After(limit.Add(time.Second)) {
		t.Fatalf("expires_at %s exceeds created_at+maxAge %s", sess.ExpiresAt, limit)
	}
}

// AUTH-SESS-4 / AUTH-TOK-1 / AC-03: invalidations reach other replicas immediately.
func TestRoleChangeDisableAndRevocationReachAllReplicas(t *testing.T) {
	bus := memBus()
	e := newEnv(t, envOpts{bus: bus})
	b := e.newReplica()
	admin := e.newClient()
	admin.mustDevLogin("admin")
	viewer := e.newClient()
	viewer.mustDevLogin("viewer")

	var users api.List[domain.User]
	admin.req("GET", "/api/v1/users", nil, false).JSON(&users)
	var viewerID uuid.UUID
	for _, u := range users.Items {
		if u.Name == "viewer" {
			viewerID = u.ID
		}
	}
	// replica B knows the session (and caches it)
	if p, err := authenticate(b, viewer, ""); err != nil || p.Role != domain.RoleViewer {
		t.Fatalf("replica B: %v %+v", p, err)
	}
	// an API token of the viewer user is not possible (admin only); the admin creates one
	var created dto.CreatedAPIToken
	admin.req("POST", "/api/v1/tokens", map[string]any{"name": "ci", "role": "admin"}, true).JSON(&created)
	if p, err := authenticate(b, nil, created.Secret); err != nil || p.Role != domain.RoleAdmin {
		t.Fatalf("token on replica B: %v %+v", p, err)
	}

	// role change: effective on replica B without waiting for the cache TTL (no clock advance)
	if r := admin.req("PATCH", "/api/v1/users/"+viewerID.String(), map[string]any{"role": "operator"}, true); r.Code != 200 {
		t.Fatalf("role change: %d %s", r.Code, r.Body)
	}
	eventuallyAuth(t, "role change on replica B", func() bool {
		p, err := authenticate(b, viewer, "")
		return err == nil && p.Role == domain.RoleOperator
	})

	// token revocation on replica A: replica B rejects the token immediately
	if r := admin.req("DELETE", "/api/v1/tokens/"+created.Token.ID.String(), nil, true); r.Code != 204 {
		t.Fatalf("revoke: %d", r.Code)
	}
	eventuallyAuth(t, "token revocation on replica B", func() bool {
		_, err := authenticate(b, nil, created.Secret)
		return err != nil
	})

	// disabling removes the sessions and revokes the tokens the user created
	viewerTok := e.mustMakeTokenFor(t, viewerID)
	if _, err := authenticate(b, nil, viewerTok); err != nil {
		t.Fatalf("token of the viewer user: %v", err)
	}
	if r := admin.req("PATCH", "/api/v1/users/"+viewerID.String(), map[string]any{"disabled": true}, true); r.Code != 200 {
		t.Fatalf("disable: %d", r.Code)
	}
	eventuallyAuth(t, "disabled user on replica B", func() bool {
		_, err := authenticate(b, viewer, "")
		return err != nil
	})
	if _, err := e.db.Identity().GetSession(context.Background(), authnSessionID(viewer.cookie(authn.SessionCookie))); err == nil {
		t.Fatal("the sessions of a disabled user must be deleted")
	}
	eventuallyAuth(t, "tokens of a disabled user", func() bool {
		_, err := authenticate(b, nil, viewerTok)
		return err != nil
	})
	var toks api.List[domain.APIToken]
	admin.req("GET", "/api/v1/tokens", nil, false).JSON(&toks)
	for _, tk := range toks.Items {
		if tk.Name == "viewer-token" && tk.RevokedAt == nil {
			t.Fatal("the token created by the disabled user must be revoked")
		}
	}
}

// mustMakeTokenFor creates an API token created by the given user directly in the database.
func (e *env) mustMakeTokenFor(t *testing.T, user uuid.UUID) string {
	t.Helper()
	secret, _ := authn.NewToken(domain.APITokenPrefix)
	uid := user
	if err := e.db.Identity().CreateAPIToken(context.Background(), domain.APIToken{
		ID: uuid.New(), Name: "viewer-token", DisplayPrefix: secret[:12], Hash: domain.SHA256([]byte(secret)), Role: domain.RoleAdmin,
		CreatedBy: "user:" + user.String(), CreatedByUserID: &uid, CreatedAt: e.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return secret
}

func eventuallyAuth(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second) // far below the 5s cache TTL, the clock is frozen
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// AUTH-TOK-1: a token never has more rights than its creator.
func TestAPITokenNeverOutranksItsCreator(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	var created dto.CreatedAPIToken
	if r := admin.req("POST", "/api/v1/tokens", map[string]any{"name": "ci", "role": "admin"}, true); r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	} else {
		r.JSON(&created)
	}
	var users api.List[domain.User]
	admin.req("GET", "/api/v1/users", nil, false).JSON(&users)
	var adminID uuid.UUID
	for _, u := range users.Items {
		if u.Name == "admin" {
			adminID = u.ID
		}
	}
	bearer := e.newClient()
	if r := bearer.req("GET", "/api/v1/tokens", nil, false, "Authorization", "Bearer "+created.Secret); r.Code != 200 {
		t.Fatalf("admin token: %d", r.Code)
	}
	// the creator is demoted: the token follows (after the cache window)
	if err := e.db.Identity().SetUserRole(context.Background(), adminID, domain.RoleViewer, "manual"); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(6 * time.Second)
	if r := bearer.req("GET", "/api/v1/tokens", nil, false, "Authorization", "Bearer "+created.Secret); r.Code != 403 {
		t.Fatalf("a token must not keep rights its creator lost: %d", r.Code)
	}
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+created.Secret); r.Code != 200 {
		t.Fatalf("read access remains: %d", r.Code)
	}
	var me dto.Me
	bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+created.Secret).JSON(&me)
	if me.Role != domain.RoleViewer {
		t.Fatalf("effective role of the token: %s", me.Role)
	}
	// the creator is disabled in the database: the token stops working
	if err := e.db.Identity().SetUserDisabled(context.Background(), adminID, true); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(6 * time.Second)
	if r := bearer.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+created.Secret); r.Code != 401 {
		t.Fatalf("token of a disabled creator: %d", r.Code)
	}
	if domain.RoleRank(domain.RoleAdmin) <= domain.RoleRank(domain.RoleOperator) || domain.RoleRank("root") != 0 {
		t.Fatal("role ranks")
	}
}

// AUTH-BRUTE-1: dev login attempts are limited per client address.
func TestDevLoginIsRateLimited(t *testing.T) {
	e := newEnv(t, envOpts{})
	var codes []int
	var retryAfter string
	for i := 0; i < 14; i++ {
		r := e.newClient().devLogin("admin", "wrong")
		codes = append(codes, r.Code)
		if r.Code == 429 {
			retryAfter = r.Header.Get("Retry-After")
			if p := r.Problem(); p.Code != api.ErrorCodeTooManyRequests {
				t.Fatalf("problem code: %+v", p)
			}
		}
	}
	// burst of 10 attempts, then throttled
	if codes[0] != 401 || codes[9] != 401 || codes[10] != 429 || codes[13] != 429 {
		t.Fatalf("status codes: %v", codes)
	}
	if retryAfter == "" || retryAfter == "0" {
		t.Fatalf("Retry-After: %q", retryAfter)
	}
	// even the right password is throttled meanwhile, and it works again after the window
	if r := e.newClient().devLogin("admin", "admin-pw"); r.Code != 429 {
		t.Fatalf("login while throttled: %d", r.Code)
	}
	e.clock.Advance(time.Minute)
	if r := e.newClient().devLogin("admin", "admin-pw"); r.Code != 200 {
		t.Fatalf("login after the window: %d", r.Code)
	}
	if got := e.findAudit("auth.login"); len(got) == 0 {
		t.Fatal("no audit entries")
	}
	limited := 0
	for _, ev := range e.findAudit("auth.login") {
		if ev.Details["reason"] == "rate_limited" {
			limited++
		}
	}
	if limited != 1 {
		t.Fatalf("exactly one rate limit audit entry per window expected, got %d", limited)
	}
}

func TestParallelLoginGuessingIsThrottled(t *testing.T) {
	e := newEnv(t, envOpts{})
	var mu sync.Mutex
	counts := map[int]int{}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.newClient().devLogin("admin", "guess")
			mu.Lock()
			counts[r.Code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts[429] < 25 || counts[200] != 0 {
		t.Fatalf("40 parallel guesses: %v", counts)
	}
}

// AUTH-BRUTE-1 / AUTH-AUDIT-1: invalid bearer tokens are counted, delayed per client address and audited.
func TestInvalidBearerTokensAreBackedOffAndAudited(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	var created dto.CreatedAPIToken
	admin.req("POST", "/api/v1/tokens", map[string]any{"name": "ci", "role": "viewer"}, true).JSON(&created)
	good := e.newClient()
	if r := good.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+created.Secret); r.Code != 200 {
		t.Fatalf("valid token: %d", r.Code)
	}

	attacker := e.newClient()
	var codes []int
	for i := 0; i < 8; i++ {
		r := attacker.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer myra_"+strings.Repeat("x", 31)+string(rune('a'+i)))
		codes = append(codes, r.Code)
	}
	if codes[0] != 401 || codes[4] != 401 || codes[7] != 429 {
		t.Fatalf("status codes: %v", codes)
	}
	// a token that this replica already knows keeps working for the same address
	if r := good.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+created.Secret); r.Code != 200 {
		t.Fatalf("cached valid token while the address is delayed: %d", r.Code)
	}
	if r := attacker.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer myra_"+strings.Repeat("y", 32)); r.Code != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("delayed request: %d retry-after=%q", r.Code, r.Header.Get("Retry-After"))
	}
	if n := len(e.findAudit("auth.token_failed")); n != 1 {
		t.Fatalf("one audit entry per client address and minute expected, got %d", n)
	}
	// the delay ends
	e.clock.Advance(10 * time.Minute)
	if r := attacker.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer myra_"+strings.Repeat("z", 32)); r.Code != 401 {
		t.Fatalf("after the delay: %d", r.Code)
	}
	mfs, _ := e.reg.Gather()
	seen := false
	for _, mf := range mfs {
		if mf.GetName() == "myriarch_auth_token_failures_total" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("myriarch_auth_token_failures_total is not exposed")
	}
}

// AUTH-BRUTE-1: clients that share a client address (all API clients behind an ingress without trustedProxies)
// do not lock each other out.
func TestSharedAddressDoesNotLockOutValidTokens(t *testing.T) {
	e := newEnv(t, envOpts{})
	admin := e.newClient()
	admin.mustDevLogin("admin")
	mk := func(name string) dto.CreatedAPIToken {
		var c dto.CreatedAPIToken
		admin.req("POST", "/api/v1/tokens", map[string]any{"name": name, "role": "viewer"}, true).JSON(&c)
		return c
	}
	stale, fresh, active := mk("stale"), mk("fresh"), mk("active")
	if r := admin.req("DELETE", "/api/v1/tokens/"+stale.Token.ID.String(), nil, true); r.Code/100 != 2 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body)
	}
	me := func(c *client, tok string) int {
		return c.req("GET", "/api/v1/auth/me", nil, false, "Authorization", "Bearer "+tok).Code
	}
	activeClient, staleClient, freshClient := e.newClient(), e.newClient(), e.newClient()
	if c := me(activeClient, active.Secret); c != 200 {
		t.Fatalf("active token: %d", c)
	}
	// a client keeps sending its revoked token: always 401, never a delay, and nobody else is affected
	for i := 0; i < 30; i++ {
		if c := me(staleClient, stale.Secret); c != 401 {
			t.Fatalf("stale token attempt %d: %d", i, c)
		}
		e.clock.Advance(time.Second)
	}
	if c := me(freshClient, fresh.Secret); c != 200 {
		t.Fatalf("valid token that was never used before, behind an address with a stale client: %d", c)
	}
	if c := me(activeClient, active.Secret); c != 200 {
		t.Fatalf("active token after its cache entry expired: %d", c)
	}

	// real guessing (many distinct tokens) still delays the address, but a recently valid token keeps working
	attacker := e.newClient()
	for i := 0; i < 10; i++ {
		tok := "myra_" + strings.Repeat("g", 30) + fmt.Sprintf("%02d", i)
		for me(attacker, tok) == 429 {
			e.clock.Advance(5 * time.Second) // wait out the delay, then continue guessing
		}
	}
	if c := me(attacker, "myra_"+strings.Repeat("h", 32)); c != 429 {
		t.Fatalf("guessing with distinct tokens must be delayed, got %d", c)
	}
	e.clock.Advance(6 * time.Second) // the principal cache (5s) expires, the delay (16s) does not
	if c := me(activeClient, active.Secret); c != 200 {
		t.Fatalf("recently valid token while the address is delayed: %d", c)
	}
	if c := me(attacker, "myra_"+strings.Repeat("h", 32)); c != 429 {
		t.Fatalf("unknown token while the address is delayed: %d", c)
	}
}

// AUTH-DEV-1: sessions of local (dev) accounts do not work where dev login is off.
func TestLocalAccountSessionsAreRejectedWithoutDevLogin(t *testing.T) {
	e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.DevLogin.Enabled = false }})
	ctx := context.Background()
	u, err := e.db.Identity().UpsertUser(ctx, domain.User{ID: uuid.New(), Subject: "local:admin", Provider: "local", Name: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	secret := "left-over-cookie-from-a-dev-database"
	if err := e.db.Identity().CreateSession(ctx, domain.Session{ID: authnSessionID(secret), UserID: u.ID, CSRFToken: "c", CreatedAt: e.clock.Now(),
		LastSeenAt: e.clock.Now(), ExpiresAt: e.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := e.newClient()
	r := c.req("GET", "/api/v1/auth/me", nil, false, "Cookie", authn.SessionCookie+"="+secret)
	if r.Code != 401 {
		t.Fatalf("a local account session must not work: %d %s", r.Code, r.Body)
	}
}

// AUTH-DEV-1: the well-known dev enrollment token is revoked when it shows up outside dev mode.
func TestRevokeDevSeedTokens(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	devCfg := config.Default().Dev
	if err := e.svc.EnsureDevSeed(ctx, devCfg); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.RevokeDevSeedTokens(ctx, devCfg.SeedEnrollmentToken, "")
	if err != nil || n != 1 {
		t.Fatalf("revoked %d: %v", n, err)
	}
	tok, err := e.db.Identity().GetEnrollmentTokenByHash(ctx, domain.SHA256([]byte(devCfg.SeedEnrollmentToken)))
	if err != nil || tok.RevokedAt == nil {
		t.Fatalf("seed token must be revoked: %+v %v", tok, err)
	}
	if n, _ := e.svc.RevokeDevSeedTokens(ctx, devCfg.SeedEnrollmentToken); n != 0 {
		t.Fatal("idempotent")
	}
	revokeAudit := false
	for _, ev := range e.findAudit("enrollment_token.revoke") {
		if ev.ActorType == domain.ActorSystem && ev.TargetID == tok.ID.String() {
			revokeAudit = true
		}
	}
	if !revokeAudit {
		t.Fatal("the revocation must be audited")
	}
	r := httptest.NewRequest("GET", "/v1/opamp", nil)
	r.Header.Set("Authorization", "Bearer "+devCfg.SeedEnrollmentToken)
	if _, err := e.svc.AuthenticateAgent(r); err == nil {
		t.Fatal("the revoked dev token must not authenticate agents")
	}
}

// AUTH-CSRF-1: login requests must be JSON and same-origin.
func TestLoginRequiresJSONAndSameOrigin(t *testing.T) {
	e := newEnv(t, envOpts{})
	c := e.newClient()
	body := `{"username":"admin","password":"admin-pw"}`
	if r := c.req("POST", "/api/v1/auth/dev-login", body, false, "Content-Type", "text/plain"); r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain login: %d", r.Code)
	}
	if r := c.req("POST", "/api/v1/auth/dev-login", body, false, "Content-Type", "application/x-www-form-urlencoded"); r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form login: %d", r.Code)
	}
	if r := c.req("POST", "/api/v1/auth/dev-login", body, false, "Origin", "https://evil.example.net"); r.Code != 403 {
		t.Fatalf("cross-origin login: %d", r.Code)
	}
	if r := c.req("POST", "/api/v1/auth/dev-login", body, false, "Sec-Fetch-Site", "cross-site"); r.Code != 403 {
		t.Fatalf("cross-site fetch: %d", r.Code)
	}
	if c.cookie(authn.SessionCookie) != "" {
		t.Fatal("no session from refused requests")
	}
	u, _ := url.Parse(e.base)
	if r := c.req("POST", "/api/v1/auth/dev-login", body, false, "Origin", u.Scheme+"://"+u.Host, "Sec-Fetch-Site", "same-origin"); r.Code != 200 {
		t.Fatalf("same-origin login: %d %s", r.Code, r.Body)
	}
}

// Cookie attributes: HttpOnly session cookie, SameSite=Lax, Secure by configuration, readable CSRF cookie.
func TestSessionCookieFlags(t *testing.T) {
	for _, secure := range []bool{false, true} {
		e := newEnv(t, envOpts{auth: func(c *config.AuthConfig) { c.CookieSecure = secure }})
		r := e.newClient().devLogin("admin", "admin-pw")
		var session, csrf string
		for _, sc := range r.Header.Values("Set-Cookie") {
			switch {
			case strings.HasPrefix(sc, authn.SessionCookie+"="), strings.HasPrefix(sc, authn.SessionCookieSecure+"="):
				session = sc
			case strings.HasPrefix(sc, authn.CSRFCookie+"="):
				csrf = sc
			}
		}
		if session == "" || csrf == "" {
			t.Fatalf("cookies missing: %v", r.Header.Values("Set-Cookie"))
		}
		if !strings.Contains(session, "HttpOnly") || !strings.Contains(session, "SameSite=Lax") || strings.Contains(session, "Domain=") {
			t.Errorf("session cookie: %s", session)
		}
		if strings.Contains(csrf, "HttpOnly") || !strings.Contains(csrf, "SameSite=Lax") {
			t.Errorf("csrf cookie (script readable by design): %s", csrf)
		}
		for _, c := range []string{session, csrf} {
			if got := strings.Contains(c, "Secure"); got != secure {
				t.Errorf("Secure=%v expected %v: %s", got, secure, c)
			}
		}
	}
}

// AC-03: revoking an enrollment token publishes an event every replica reacts to.
func TestEnrollmentTokenRevocationIsPublished(t *testing.T) {
	bus := memBus()
	e := newEnv(t, envOpts{bus: bus})
	events, cancel := bus.Subscribe(eventbus.EnrollmentTokenRevoked, eventbus.AuthInvalidate)
	defer cancel()
	admin := e.newClient()
	admin.mustDevLogin("admin")
	var created dto.CreatedEnrollmentToken
	admin.req("POST", "/api/v1/enrollment-tokens", map[string]any{"name": "sites"}, true).JSON(&created)
	if r := admin.req("DELETE", "/api/v1/enrollment-tokens/"+created.Token.ID.String(), nil, true); r.Code != 204 {
		t.Fatalf("revoke: %d", r.Code)
	}
	select {
	case ev := <-events:
		var p eventbus.EnrollmentTokenPayload
		if ev.Type != eventbus.EnrollmentTokenRevoked || ev.Decode(&p) != nil || p.TokenID != created.Token.ID {
			t.Fatalf("event: %+v %+v", ev, p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no enrollment_token.revoked event")
	}
	// the other replica no longer accepts the token (cache purged by the event)
	b := e.newReplica()
	r := httptest.NewRequest("GET", "/v1/opamp", nil)
	r.Header.Set("Authorization", "Bearer "+created.Secret)
	if _, err := b.AuthenticateAgent(r); err == nil {
		t.Fatal("revoked token accepted")
	}
}

var _ = store.AuditFilter{}

// authnSessionID is the database id of a session cookie value (SHA-256 hex).
func authnSessionID(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}
