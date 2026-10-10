// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"net/url"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/config"
)

// oidcFor returns a production configuration whose OIDC issuer is issuer.
func oidcFor(issuer string, mutate func(o *config.OIDCConfig)) config.Config {
	c := prodConfig()
	c.Server.TrustedProxies = []string{"10.0.0.0/8"}
	c.Auth.OIDC.IssuerURL = issuer
	if mutate != nil {
		mutate(&c.Auth.OIDC)
	}
	return c
}

func TestProviderIssuerChecks(t *testing.T) {
	cases := []struct {
		issuer string
		want   string // "" = valid, else the exact message under auth.oidc.issuerUrl
	}{
		// Keycloak
		{"https://idp.example.com/realms/otel", ""},
		{"https://idp.example.com/auth/realms/otel", ""},
		{"https://idp.example.com/realms/otel/", `Keycloak issuers have the form https://<host>/realms/<realm> (no trailing slash, no endpoint path); got "/realms/otel/"`},
		{"https://idp.example.com/realms/otel/protocol/openid-connect", `Keycloak issuers have the form https://<host>/realms/<realm> (no trailing slash, no endpoint path); got "/realms/otel/protocol/openid-connect"`},
		{"https://idp.example.com/auth/realms/", `Keycloak issuers have the form https://<host>/realms/<realm> (no trailing slash, no endpoint path); got "/auth/realms/"`},
		// Okta
		{"https://acme.okta.com", ""},
		{"https://acme.oktapreview.com", ""},
		{"https://acme.okta-emea.com", ""},
		{"https://acme.okta.com/oauth2/default", ""},
		{"https://acme.okta.com/oauth2/aus1abc", ""},
		{"https://acme.okta.com/", `Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, without endpoint path or trailing slash; got "/"`},
		{"https://acme.okta.com/oauth2/default/", `Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, without endpoint path or trailing slash; got "/oauth2/default/"`},
		{"https://acme.okta.com/oauth2/v1", `Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, without endpoint path or trailing slash; got "/oauth2/v1"`},
		{"https://acme.okta.com/oauth2/v1/authorize", `Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, without endpoint path or trailing slash; got "/oauth2/v1/authorize"`},
		{"https://acme.okta.com/oauth2/default/v1/token", `Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, without endpoint path or trailing slash; got "/oauth2/default/v1/token"`},
		// Dex: no issuer error
		{"https://dex.example.com/dex", ""},
		{"https://example.com/platform/dex", ""},
		// Google
		{"https://accounts.google.com", ""},
		{"https://accounts.google.com/", `the Google issuer is https://accounts.google.com (no path, no trailing slash); got "/"`},
		{"https://accounts.google.com/o/oauth2/v2/auth", `the Google issuer is https://accounts.google.com (no path, no trailing slash); got "/o/oauth2/v2/auth"`},
		// Entra ID keeps its text
		{"https://login.microsoftonline.com/" + tenant + "/v2.0", ""},
		{"https://login.microsoftonline.com/common/v2.0", `Entra ID needs the tenant ID (GUID) and the endpoint version in the issuer URL: https://login.microsoftonline.com/<TENANT-GUID>/v2.0 (not "common", "organizations" or a domain name, no trailing slash); got "/common/v2.0"`},
		{"https://login.microsoftonline.us/" + tenant + "/v2.0/", `Entra ID needs the tenant ID (GUID) and the endpoint version in the issuer URL: https://login.microsoftonline.us/<TENANT-GUID>/v2.0 (not "common", "organizations" or a domain name, no trailing slash); got "/` + tenant + `/v2.0/"`},
		// providers without a row are not constrained (authentik, Zitadel, anything else)
		{"https://auth.example.com/application/o/myriarch/", ""},
		{"https://myriarch-abc.zitadel.cloud", ""},
		{"https://sso.example.com/some/path/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.issuer, func(t *testing.T) {
			c := oidcFor(tc.issuer, nil)
			err := Validate(&c, false)
			got, bad := problemMap(err)["auth.oidc.issuerUrl"]
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("must be valid: %v", err)
			case tc.want != "" && (!bad || got != tc.want):
				t.Errorf("message:\n got %q\nwant %q", got, tc.want)
			}
			if tc.want != "" && len(Problems(err)) != 1 {
				t.Errorf("exactly one problem expected: %v", Problems(err))
			}
		})
	}
}

func TestProviderWarnings(t *testing.T) {
	groups := func(o *config.OIDCConfig) { o.RoleMapping = config.RoleMapping{Admin: []string{"admins"}} }
	users := func(o *config.OIDCConfig) {
		o.RoleMapping = config.RoleMapping{Users: config.UserRoleMapping{Admin: []string{"email:a@example.test"}}}
	}

	const dexWarn = `auth.oidc.scopes does not request "groups" although auth.oidc.roleMapping maps groups and the issuer is a Dex instance: Dex sends the groups claim only for the scope groups`
	const googleWarn = "auth.oidc.roleMapping maps groups, but Google ID tokens carry no groups claim: map users with auth.oidc.roleMapping.users (email:<address>) or leave the group lists empty"

	cases := []struct {
		name   string
		issuer string
		mutate func(o *config.OIDCConfig)
		want   []string
	}{
		{"dex without groups scope", "https://dex.example.com/dex", groups, []string{dexWarn}},
		{"dex with groups scope", "https://dex.example.com/dex", func(o *config.OIDCConfig) {
			groups(o)
			o.Scopes = []string{"openid", "profile", "email", "groups"}
		}, nil},
		{"dex with users only", "https://dex.example.com/dex", users, nil},
		{"google with groups", "https://accounts.google.com", groups, []string{googleWarn}},
		{"google with users", "https://accounts.google.com", users, nil},
		{"google with a default role only", "https://accounts.google.com", func(o *config.OIDCConfig) {
			o.RoleMapping, o.DefaultRole = config.RoleMapping{}, "viewer"
		}, []string{`auth.oidc.defaultRole is "viewer": every user of the identity provider without a mapped group gets this role; leave it empty (deny) and map groups explicitly`}},
		{"keycloak", "https://idp.example.com/realms/otel", groups, nil},
		{"okta", "https://acme.okta.com/oauth2/default", groups, nil},
		{"unknown provider", "https://sso.example.com", groups, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := oidcFor(tc.issuer, tc.mutate)
			if err := Validate(&c, false); err != nil {
				t.Fatalf("must be valid: %v", err)
			}
			got := Warnings(&c, false)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("warnings:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The Entra warning keeps its text byte for byte (the command tests and the
// documentation rely on it).
func TestEntraWarningText(t *testing.T) {
	c := oidcFor("https://login.microsoftonline.com/"+tenant+"/v2.0", nil)
	want := `auth.oidc.groupsClaim is "groups" with an Entra ID issuer: Entra ID omits the groups claim for users in more than 200 groups (group overage) and lists object IDs only; ` +
		`use app roles instead (groupsClaim: roles, roleMapping with the role values of the app registration, e.g. myriarch.admin)`
	if got := Warnings(&c, false); len(got) != 1 || got[0] != want {
		t.Errorf("warnings: %q", got)
	}
	c.Auth.OIDC.GroupsClaim = "roles"
	if got := Warnings(&c, false); len(got) != 0 {
		t.Errorf("app roles: %q", got)
	}
}

// The first matching row of the table applies: Entra hosts never match the other rows.
func TestProviderTableOrder(t *testing.T) {
	for _, tc := range []struct{ issuer, name string }{
		{"https://login.microsoftonline.com/" + tenant + "/v2.0", "Microsoft Entra ID"},
		{"https://idp.example.com/realms/x", "Keycloak"},
		{"https://acme.okta.com", "Okta"},
		{"https://dex.example.com/dex", "Dex"},
		{"https://accounts.google.com", "Google"},
	} {
		u := mustParse(t, tc.issuer)
		if h := hintFor(u); h == nil || h.name != tc.name {
			t.Errorf("%s: %+v", tc.issuer, h)
		}
	}
	if h := hintFor(mustParse(t, "https://auth.example.com/application/o/app/")); h != nil {
		t.Errorf("authentik has no row: %s", h.name)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
