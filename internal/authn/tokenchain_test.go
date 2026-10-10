// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/domain"
)

// AUTH-TOK-1: an API token never has more rights than the user it derives
// from, and a token that is created with a token derives from the same user.
// The tests below run through the REST API against a real database (users,
// api_tokens.created_by_user_id, token expiry, revocation by creator):
//
//   - the child keeps the user of its parent (created_by_user_id) and so the
//     role cap of the user: demoting the user caps the whole chain (403 on what
//     the lost role allowed),
//   - the child never outlives its parent (expiry capped at the parent's),
//   - disabling the user ends the whole chain (401), whether the flag is set in
//     the database or through the API (which also revokes the tokens).

// chain is a token created by a user (parent), tokens created with the parent
// (child, late, short) and a token created with the child (grand).
type chain struct {
	parent, child, late, short, grand dto.CreatedAPIToken
	parentExp, soon                   time.Time
}

type callFn func(method, path string, body any) result

// asSession performs requests with the session of c (CSRF protected).
func asSession(c *client) callFn {
	return func(method, path string, body any) result { return c.req(method, path, body, true) }
}

// asToken performs requests with an API token.
func (e *env) asToken(secret string) callFn {
	c := e.newClient()
	return func(method, path string, body any) result {
		return c.req(method, path, body, false, "Authorization", "Bearer "+secret)
	}
}

func (e *env) createToken(t *testing.T, as callFn, name string, role domain.Role, expires *time.Time) dto.CreatedAPIToken {
	t.Helper()
	body := map[string]any{"name": name, "role": role}
	if expires != nil {
		body["expiresAt"] = *expires
	}
	r := as("POST", "/api/v1/tokens", body)
	if r.Code != 201 {
		t.Fatalf("create token %q: %d %s", name, r.Code, r.Body)
	}
	var c dto.CreatedAPIToken
	r.JSON(&c)
	return c
}

// userID returns the id of the user with the given name (listed by an admin).
func (e *env) userID(t *testing.T, admin *client, name string) uuid.UUID {
	t.Helper()
	var users api.List[domain.User]
	admin.req("GET", "/api/v1/users", nil, false).JSON(&users)
	for _, u := range users.Items {
		if u.Name == name {
			return u.ID
		}
	}
	t.Fatalf("user %q not found", name)
	return uuid.Nil
}

// buildChain lets the user of the session create a parent token with an
// expiry and an admin role, and then creates child tokens with it:
//   - child: admin, no expiry requested,
//   - late: admin, expiry far behind the parent's,
//   - short: viewer, expiry before the parent's,
//   - grand: created with the child, expiry far behind the parent's.
func (e *env) buildChain(t *testing.T, creator *client) chain {
	t.Helper()
	now := e.clock.Now()
	ch := chain{parentExp: now.Add(2 * time.Hour).Truncate(time.Second), soon: now.Add(30 * time.Minute).Truncate(time.Second)}
	behind := ch.parentExp.Add(8 * time.Hour)
	ch.parent = e.createToken(t, asSession(creator), "parent", domain.RoleAdmin, &ch.parentExp)
	asParent := e.asToken(ch.parent.Secret)
	ch.child = e.createToken(t, asParent, "child", domain.RoleAdmin, nil)
	ch.late = e.createToken(t, asParent, "late", domain.RoleAdmin, &behind)
	ch.short = e.createToken(t, asParent, "short", domain.RoleViewer, &ch.soon)
	ch.grand = e.createToken(t, e.asToken(ch.child.Secret), "grand", domain.RoleAdmin, &behind)
	return ch
}

// all returns the tokens of the chain.
func (c chain) all() []dto.CreatedAPIToken {
	return []dto.CreatedAPIToken{c.parent, c.child, c.late, c.short, c.grand}
}

// adminTokens returns the tokens of the chain whose own role is admin.
func (c chain) adminTokens() []dto.CreatedAPIToken {
	return []dto.CreatedAPIToken{c.parent, c.child, c.late, c.grand}
}

func (e *env) stored(t *testing.T, secret string) *domain.APIToken {
	t.Helper()
	tok, err := e.db.Identity().GetAPITokenByHash(context.Background(), domain.SHA256([]byte(secret)))
	if err != nil {
		t.Fatalf("token lookup: %v", err)
	}
	return tok
}

func codeOf(r result) int { return r.Code }

func (e *env) roleOf(t *testing.T, secret string) domain.Role {
	t.Helper()
	r := e.asToken(secret)("GET", "/api/v1/auth/me", nil)
	if r.Code != 200 {
		t.Fatalf("auth/me: %d %s", r.Code, r.Body)
	}
	var me dto.Me
	r.JSON(&me)
	return me.Role
}

func TestChildAPITokenInheritsCreatorLimits(t *testing.T) {
	t.Run("the child derives from the user of its parent and never outlives it", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		admin := e.newClient()
		admin.mustDevLogin("admin")
		adminID := e.userID(t, admin, "admin")
		ch := e.buildChain(t, admin)

		// the answers of the API
		sameSecond := func(got *time.Time, want time.Time) bool { return got != nil && got.Truncate(time.Second).Equal(want) }
		if !sameSecond(ch.parent.Token.ExpiresAt, ch.parentExp) {
			t.Fatalf("parent expiry: %v, want %v", ch.parent.Token.ExpiresAt, ch.parentExp)
		}
		for name, tok := range map[string]dto.CreatedAPIToken{"child (none requested)": ch.child, "late (behind the parent)": ch.late, "grand (behind the parent)": ch.grand} {
			if !sameSecond(tok.Token.ExpiresAt, ch.parentExp) {
				t.Errorf("%s: expiry %v, want the expiry of the parent %v", name, tok.Token.ExpiresAt, ch.parentExp)
			}
		}
		if !sameSecond(ch.short.Token.ExpiresAt, ch.soon) {
			t.Errorf("short: an earlier expiry is kept: %v, want %v", ch.short.Token.ExpiresAt, ch.soon)
		}

		// ... and what the database holds: every token of the chain derives from the
		// user (not from the token), so the user's role and state apply to it
		wantRole := map[string]domain.Role{"parent": domain.RoleAdmin, "child": domain.RoleAdmin, "late": domain.RoleAdmin, "short": domain.RoleViewer, "grand": domain.RoleAdmin}
		createdBy := map[string]dto.CreatedAPIToken{"child": ch.parent, "late": ch.parent, "short": ch.parent, "grand": ch.child}
		for _, tok := range ch.all() {
			st := e.stored(t, tok.Secret)
			if st.CreatedByUserID == nil || *st.CreatedByUserID != adminID {
				t.Errorf("%s: created_by_user_id = %v, want the admin user %s", st.Name, st.CreatedByUserID, adminID)
			}
			if st.Role != wantRole[st.Name] {
				t.Errorf("%s: role %s, want %s", st.Name, st.Role, wantRole[st.Name])
			}
			if by, ok := createdBy[st.Name]; ok && st.CreatedBy != "token:"+by.Token.ID.String() {
				t.Errorf("%s: created_by = %q, want token:%s", st.Name, st.CreatedBy, by.Token.ID)
			}
			if st.Name == "parent" && st.CreatedBy != "user:"+adminID.String() {
				t.Errorf("parent: created_by = %q", st.CreatedBy)
			}
		}

		// a parent without expiry does not impose one
		forever := e.createToken(t, asSession(admin), "forever", domain.RoleAdmin, nil)
		inAnHour := e.clock.Now().Add(time.Hour).Truncate(time.Second)
		noExp := e.createToken(t, e.asToken(forever.Secret), "no-expiry", domain.RoleOperator, nil)
		withExp := e.createToken(t, e.asToken(forever.Secret), "with-expiry", domain.RoleOperator, &inAnHour)
		if noExp.Token.ExpiresAt != nil || !sameSecond(withExp.Token.ExpiresAt, inAnHour) {
			t.Errorf("under a parent without expiry: none = %v, requested = %v", noExp.Token.ExpiresAt, withExp.Token.ExpiresAt)
		}

		// the role of a token limits what it can create: a viewer or operator token cannot
		// create tokens at all, so a child can never outrank the token that created it
		before := len(listTokens(t, e))
		for _, tok := range []dto.CreatedAPIToken{ch.short, noExp} {
			r := e.asToken(tok.Secret)("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer"})
			if r.Code != 403 {
				t.Errorf("%s token creating a token: %d, want 403", tok.Token.Role, r.Code)
			}
		}
		if after := len(listTokens(t, e)); after != before {
			t.Errorf("a refused request created a token: %d -> %d", before, after)
		}
		// an unknown role is refused (and not capped to something valid)
		if r := e.asToken(ch.parent.Secret)("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "root"}); r.Code != 422 {
			t.Errorf("unknown role: %d", r.Code)
		}
	})

	t.Run("the chain stops at the expiry of the parent", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		admin := e.newClient()
		admin.mustDevLogin("admin")
		ch := e.buildChain(t, admin)
		me := func(tok dto.CreatedAPIToken) int {
			return codeOf(e.asToken(tok.Secret)("GET", "/api/v1/auth/me", nil))
		}
		for _, tok := range ch.all() {
			if c := me(tok); c != 200 {
				t.Fatalf("%s before any expiry: %d", tok.Token.Name, c)
			}
		}
		// the short token ends first, the others are alive
		e.clock.Advance(time.Hour)
		if c := me(ch.short); c != 401 {
			t.Fatalf("short token after its expiry: %d", c)
		}
		for _, tok := range ch.adminTokens() {
			if c := me(tok); c != 200 {
				t.Fatalf("%s one hour in (the parent expires after two): %d", tok.Token.Name, c)
			}
		}
		// the parent and everything derived from it end together
		e.clock.Advance(ch.parentExp.Sub(e.clock.Now()) + time.Second)
		for _, tok := range ch.adminTokens() {
			if c := me(tok); c != 401 {
				t.Errorf("%s after the expiry of the parent: %d, want 401", tok.Token.Name, c)
			}
		}
	})

	t.Run("demoting the user caps the whole chain", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		ctx := context.Background()
		admin := e.newClient()
		admin.mustDevLogin("admin")
		adminID := e.userID(t, admin, "admin")
		ch := e.buildChain(t, admin)

		adminOnly := []string{"/api/v1/users", "/api/v1/tokens", "/api/v1/enrollment-tokens"}
		for _, tok := range ch.adminTokens() {
			for _, path := range adminOnly {
				if c := codeOf(e.asToken(tok.Secret)("GET", path, nil)); c != 200 {
					t.Fatalf("%s: GET %s as the admin it is: %d", tok.Token.Name, path, c)
				}
			}
		}

		// demoted to operator: the admin tokens lose the admin routes (403), not their identity (401)
		if err := e.db.Identity().SetUserRole(ctx, adminID, domain.RoleOperator, "manual"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(6 * time.Second) // the cache window of authenticated principals is 5 s
		tokensBefore := len(listTokens(t, e))
		for _, tok := range ch.adminTokens() {
			as := e.asToken(tok.Secret)
			for _, path := range adminOnly {
				if c := codeOf(as("GET", path, nil)); c != 403 {
					t.Errorf("%s: GET %s after the demotion to operator: %d, want 403", tok.Token.Name, path, c)
				}
			}
			if c := codeOf(as("POST", "/api/v1/tokens", map[string]any{"name": "late-child", "role": "viewer"})); c != 403 {
				t.Errorf("%s: creating a token after the demotion: %d, want 403", tok.Token.Name, c)
			}
			if role := e.roleOf(t, tok.Secret); role != domain.RoleOperator {
				t.Errorf("%s: effective role %s, want operator (the role of the user)", tok.Token.Name, role)
			}
		}
		if role := e.roleOf(t, ch.short.Secret); role != domain.RoleViewer {
			t.Errorf("short: a token below the cap keeps its own role, got %s", role)
		}
		if n := len(listTokens(t, e)); n != tokensBefore {
			t.Errorf("demoted tokens created tokens: %d -> %d", tokensBefore, n)
		}

		// demoted to viewer: everything is capped to viewer
		if err := e.db.Identity().SetUserRole(ctx, adminID, domain.RoleViewer, "manual"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(6 * time.Second)
		for _, tok := range ch.all() {
			if role := e.roleOf(t, tok.Secret); role != domain.RoleViewer {
				t.Errorf("%s: effective role %s after the demotion to viewer, want viewer", tok.Token.Name, role)
			}
			// everything the lost roles allowed is refused, reading stays possible
			as := e.asToken(tok.Secret)
			for _, path := range adminOnly {
				if c := codeOf(as("GET", path, nil)); c != 403 {
					t.Errorf("%s: GET %s after the demotion to viewer: %d, want 403", tok.Token.Name, path, c)
				}
			}
			if c := codeOf(as("GET", "/api/v1/auth/me", nil)); c != 200 {
				t.Errorf("%s: GET /auth/me after the demotion to viewer: %d, want 200", tok.Token.Name, c)
			}
		}
	})

	t.Run("a disabled user ends the whole chain", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		admin := e.newClient()
		admin.mustDevLogin("admin")
		adminID := e.userID(t, admin, "admin")
		ch := e.buildChain(t, admin)
		for _, tok := range ch.all() {
			if c := codeOf(e.asToken(tok.Secret)("GET", "/api/v1/auth/me", nil)); c != 200 {
				t.Fatalf("%s before: %d", tok.Token.Name, c)
			}
		}
		// the flag is set in the database only: the tokens are not revoked, the check at use time stops them
		if err := e.db.Identity().SetUserDisabled(context.Background(), adminID, true); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(6 * time.Second)
		for _, tok := range ch.all() {
			if c := codeOf(e.asToken(tok.Secret)("GET", "/api/v1/auth/me", nil)); c != 401 {
				t.Errorf("%s of a disabled user: %d, want 401", tok.Token.Name, c)
			}
			if e.stored(t, tok.Secret).RevokedAt != nil {
				t.Errorf("%s: the test must not revoke the token itself", tok.Token.Name)
			}
		}
	})

	t.Run("disabling the user through the API revokes the whole chain and nothing else", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		ctx := context.Background()
		admin := e.newClient()
		admin.mustDevLogin("admin")
		// a second admin who creates the chain
		first := e.newClient()
		first.mustDevLogin("operator")
		opID := e.userID(t, admin, "operator")
		if err := e.db.Identity().SetUserRole(ctx, opID, domain.RoleAdmin, "manual"); err != nil {
			t.Fatal(err)
		}
		creator := e.newClient()
		creator.mustDevLogin("operator") // a new session: the user is an admin now
		ch := e.buildChain(t, creator)
		bystander := e.createToken(t, asSession(admin), "bystander", domain.RoleAdmin, nil)
		for _, tok := range append(ch.all(), bystander) {
			if c := codeOf(e.asToken(tok.Secret)("GET", "/api/v1/auth/me", nil)); c != 200 {
				t.Fatalf("%s before: %d", tok.Token.Name, c)
			}
		}

		r := admin.req("PATCH", "/api/v1/users/"+opID.String(), map[string]any{"disabled": true}, true)
		if r.Code != 200 {
			t.Fatalf("disable: %d %s", r.Code, r.Body)
		}
		// at once, without waiting for any cache, and in the database
		for _, tok := range ch.all() {
			if c := codeOf(e.asToken(tok.Secret)("GET", "/api/v1/auth/me", nil)); c != 401 {
				t.Errorf("%s after the user was disabled: %d, want 401", tok.Token.Name, c)
			}
			if e.stored(t, tok.Secret).RevokedAt == nil {
				t.Errorf("%s: not revoked although its user was disabled", tok.Token.Name)
			}
		}
		if e.stored(t, bystander.Secret).RevokedAt != nil {
			t.Error("the token of another user was revoked")
		}
		if c := codeOf(e.asToken(bystander.Secret)("GET", "/api/v1/auth/me", nil)); c != 200 {
			t.Errorf("the token of another user: %d, want 200", c)
		}
		if c := codeOf(creator.req("GET", "/api/v1/auth/me", nil, false)); c != 401 {
			t.Errorf("session of the disabled user: %d, want 401", c)
		}
	})
}

func listTokens(t *testing.T, e *env) []domain.APIToken {
	t.Helper()
	toks, err := e.db.Identity().ListAPITokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return toks
}
