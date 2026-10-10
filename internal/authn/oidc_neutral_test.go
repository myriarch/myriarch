// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"testing"

	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// signInWith runs the authorization code flow against the fake IdP with the
// given ID token claims on top of the defaults (sub user-1, name Una User,
// email una@example.com, groups []) and returns the callback response and the
// client.
func signInWith(t *testing.T, e *env, idp *fakeIdP, claims map[string]any) (result, *client) {
	t.Helper()
	idp.claims = func(string) map[string]any { return claims }
	c := e.newClient()
	cb, _ := startLogin(t, c, idp, "")
	return c.req("GET", callbackPath(t, cb), nil, false), c
}

func neutralEnv(t *testing.T, idp *fakeIdP, mutate func(o *config.OIDCConfig)) *env {
	t.Helper()
	return newEnv(t, envOpts{httpCli: idp.srv.Client(), auth: func(c *config.AuthConfig) {
		oidcAuth(idp)(c)
		c.OIDC.NameClaim, c.OIDC.EmailClaim = "name", "email"
		if mutate != nil {
			mutate(&c.OIDC)
		}
	}})
}

func meOf(t *testing.T, c *client) dto.Me {
	t.Helper()
	var me dto.Me
	r := c.req("GET", "/api/v1/auth/me", nil, false)
	if r.Code != 200 {
		t.Fatalf("me: %d %s", r.Code, r.Body)
	}
	r.JSON(&me)
	return me
}

func TestOIDCRejectsReservedSubjects(t *testing.T) {
	for _, sub := range []string{"bootstrap:admin", "local:admin", "bootstrap:"} {
		idp := newIdP(t)
		e := neutralEnv(t, idp, nil)
		r, c := signInWith(t, e, idp, map[string]any{"sub": sub, "groups": []string{"fleet-admins"}})
		if r.Code != 302 || r.Header.Get("Location") != "/login?error=oidc_error" || c.cookie(authn.SessionCookie) != "" {
			t.Fatalf("%s: %d %s", sub, r.Code, r.Header.Get("Location"))
		}
		ev := e.findAudit("auth.login")
		if len(ev) != 1 || ev[0].Result != "failure" || ev[0].Details["reason"] != "reserved_subject" {
			t.Fatalf("%s: audit %+v", sub, ev)
		}
		if users, _ := e.db.Identity().ListUsers(context.Background()); len(users) != 0 {
			t.Fatalf("%s: no user may be created", sub)
		}
	}
	// a subject that merely contains the prefix is fine
	idp := newIdP(t)
	e := neutralEnv(t, idp, nil)
	if r, _ := signInWith(t, e, idp, map[string]any{"sub": "x-bootstrap:admin", "groups": []string{"fleet-admins"}}); r.Code != 302 || r.Header.Get("Location") != "/" {
		t.Fatalf("%d %s", r.Code, r.Header.Get("Location"))
	}
}

func TestOIDCNameAndEmailClaims(t *testing.T) {
	idp := newIdP(t)
	e := neutralEnv(t, idp, func(o *config.OIDCConfig) {
		o.NameClaim, o.EmailClaim = "display_name", "profile.mail"
	})
	r, c := signInWith(t, e, idp, map[string]any{
		"groups": []string{"fleet-viewers"}, "name": "Ignored Name", "email": "ignored@example.com",
		"display_name": "Una Display", "profile": map[string]any{"mail": "una@corp.example"},
	})
	if r.Code != 302 {
		t.Fatalf("%d %s", r.Code, r.Header.Get("Location"))
	}
	if me := meOf(t, c); me.Name != "Una Display" || me.Email != "una@corp.example" {
		t.Fatalf("configured claims: %+v", me)
	}

	// the name falls back to preferred_username
	idp2 := newIdP(t)
	e2 := neutralEnv(t, idp2, func(o *config.OIDCConfig) { o.NameClaim = "display_name" })
	_, c2 := signInWith(t, e2, idp2, map[string]any{"groups": []string{"fleet-viewers"}, "name": "Not Used", "preferred_username": "una.u"})
	if me := meOf(t, c2); me.Name != "una.u" || me.Email != "una@example.com" {
		t.Fatalf("fallback: %+v", me)
	}
}

func TestOIDCUserMappings(t *testing.T) {
	type tc struct {
		name   string
		users  config.UserRoleMapping
		groups []string
		claims map[string]any
		want   domain.Role // "" = access denied
	}
	cases := []tc{
		{"sub", config.UserRoleMapping{Operator: []string{"sub:user-1"}}, nil, nil, domain.RoleOperator},
		{"sub is exact and case sensitive", config.UserRoleMapping{Admin: []string{"sub:USER-1", "sub:user-"}}, nil, nil, ""},
		{"email verified", config.UserRoleMapping{Admin: []string{"email:una@example.com"}}, nil, map[string]any{"email_verified": true}, domain.RoleAdmin},
		{"email verified as a string", config.UserRoleMapping{Admin: []string{"email:una@example.com"}}, nil, map[string]any{"email_verified": "true"}, domain.RoleAdmin},
		{"email is case insensitive", config.UserRoleMapping{Viewer: []string{"email:UNA@Example.COM"}}, nil, map[string]any{"email_verified": true}, domain.RoleViewer},
		{"email not verified", config.UserRoleMapping{Admin: []string{"email:una@example.com"}}, nil, map[string]any{"email_verified": false}, ""},
		{"email_verified missing", config.UserRoleMapping{Admin: []string{"email:una@example.com"}}, nil, nil, ""},
		{"email_verified as the string false", config.UserRoleMapping{Admin: []string{"email:una@example.com"}}, nil, map[string]any{"email_verified": "false"}, ""},
		{"other address", config.UserRoleMapping{Admin: []string{"email:other@example.com"}}, nil, map[string]any{"email_verified": true}, ""},
		{"no email in the token", config.UserRoleMapping{Admin: []string{"email:una@example.com"}}, nil, map[string]any{"email": "", "email_verified": true}, ""},
		{"the user mapping wins when higher", config.UserRoleMapping{Admin: []string{"sub:user-1"}}, []string{"fleet-viewers"}, nil, domain.RoleAdmin},
		{"the group mapping wins when higher", config.UserRoleMapping{Viewer: []string{"sub:user-1"}}, []string{"fleet-admins"}, nil, domain.RoleAdmin},
		{"highest of several user entries", config.UserRoleMapping{Viewer: []string{"sub:user-1"}, Operator: []string{"sub:user-1"}}, nil, nil, domain.RoleOperator},
		{"unrelated entries", config.UserRoleMapping{Admin: []string{"sub:someone-else", "email:else@example.com"}}, []string{"nobody"}, map[string]any{"email_verified": true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idp := newIdP(t)
			e := neutralEnv(t, idp, func(o *config.OIDCConfig) { o.RoleMapping.Users = c.users })
			claims := map[string]any{"groups": c.groups}
			for k, v := range c.claims {
				claims[k] = v
			}
			r, cl := signInWith(t, e, idp, claims)
			if c.want == "" {
				if r.Header.Get("Location") != "/login?error=access_denied" || cl.cookie(authn.SessionCookie) != "" {
					t.Fatalf("must be denied: %d %s", r.Code, r.Header.Get("Location"))
				}
				return
			}
			if r.Code != 302 || r.Header.Get("Location") != "/" {
				t.Fatalf("%d %s", r.Code, r.Header.Get("Location"))
			}
			if me := meOf(t, cl); me.Role != c.want {
				t.Fatalf("role %s, want %s", me.Role, c.want)
			}
		})
	}
}

func TestOIDCObjectClaimsCountByKey(t *testing.T) {
	roles := map[string]any{"fleet-ops": map[string]any{"123456": "org.example.test"}, "unrelated": map[string]any{"123456": "org.example.test"}}
	for name, tc := range map[string]struct {
		claim  string
		claims map[string]any
	}{
		"literal URL-like key": {"urn:example:iam:project:roles", map[string]any{"urn:example:iam:project:roles": roles}},
		"dotted path":          {"resource.roles", map[string]any{"resource": map[string]any{"roles": roles}}},
		"plain key":            {"roles", map[string]any{"roles": roles}},
	} {
		t.Run(name, func(t *testing.T) {
			idp := newIdP(t)
			e := neutralEnv(t, idp, func(o *config.OIDCConfig) { o.GroupsClaim = tc.claim })
			r, c := signInWith(t, e, idp, tc.claims)
			if r.Code != 302 || r.Header.Get("Location") != "/" {
				t.Fatalf("%d %s", r.Code, r.Header.Get("Location"))
			}
			if me := meOf(t, c); me.Role != domain.RoleOperator {
				t.Fatalf("the keys of the object are the groups: %+v", me)
			}
		})
	}
	// an object without a mapped key grants nothing
	idp := newIdP(t)
	e := neutralEnv(t, idp, func(o *config.OIDCConfig) { o.GroupsClaim = "roles" })
	if r, _ := signInWith(t, e, idp, map[string]any{"roles": map[string]any{"unmapped": map[string]any{}}}); r.Header.Get("Location") != "/login?error=access_denied" {
		t.Fatalf("%s", r.Header.Get("Location"))
	}
}

// email_verified attests the claim "email" only: with another emailClaim an
// "email:" entry never matches, whatever the token says about its address.
func TestOIDCEmailMappingNeedsTheStandardEmailClaim(t *testing.T) {
	idp := newIdP(t)
	e := neutralEnv(t, idp, func(o *config.OIDCConfig) {
		o.EmailClaim = "profile.mail"
		o.RoleMapping = config.RoleMapping{Users: config.UserRoleMapping{Admin: []string{"email:una@corp.example"}}}
	})
	r, cl := signInWith(t, e, idp, map[string]any{
		"email": "una@corp.example", "email_verified": true, "profile": map[string]any{"mail": "una@corp.example"},
	})
	if r.Header.Get("Location") != "/login?error=access_denied" || cl.cookie(authn.SessionCookie) != "" {
		t.Fatalf("must be denied: %d %s", r.Code, r.Header.Get("Location"))
	}
}
