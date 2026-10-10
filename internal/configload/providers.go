// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package configload

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/myriarch/myriarch/internal/config"
)

// providerHint is one row of the provider hint table: what is known about the
// issuer URLs and the claims of an identity provider. Any standards compliant
// provider works; the table only turns mistakes that would show up as a login
// failure at runtime into errors and warnings at startup. The first matching
// row applies. authentik and Zitadel need no row (documentation only, see
// docs/operations/authentication.md).
type providerHint struct {
	name string
	// match reports whether the issuer URL belongs to the provider.
	match func(u *url.URL) bool
	// checkIssuer returns the reason an issuer URL of the provider is wrong
	// ("" = fine). go-oidc compares the issuer of the discovery document
	// exactly, so a wrong form would only fail when the first user signs in.
	checkIssuer func(u *url.URL) string
	// warn returns warnings about the rest of the OIDC settings.
	warn func(o config.OIDCConfig) []string
}

// providerHints is the provider hint table.
var providerHints = []providerHint{
	{name: "Microsoft Entra ID", match: isEntra, checkIssuer: checkEntraIssuer, warn: warnEntra},
	{name: "Keycloak", match: isKeycloak, checkIssuer: checkKeycloakIssuer},
	{name: "Okta", match: isOkta, checkIssuer: checkOktaIssuer},
	{name: "Dex", match: isDex, warn: warnDex},
	{name: "Google", match: isGoogle, checkIssuer: checkGoogleIssuer, warn: warnGoogle},
}

// hintFor returns the row for an issuer URL, or nil.
func hintFor(u *url.URL) *providerHint {
	for i := range providerHints {
		if providerHints[i].match(u) {
			return &providerHints[i]
		}
	}
	return nil
}

// checkIssuerHint returns the reason the issuer URL is wrong for the provider
// it belongs to ("" = fine or unknown provider).
func checkIssuerHint(u *url.URL) string {
	if h := hintFor(u); h != nil && h.checkIssuer != nil {
		return h.checkIssuer(u)
	}
	return ""
}

// providerWarnings returns the warnings of the provider that the issuer URL
// of the OIDC settings belongs to.
func providerWarnings(o config.OIDCConfig) []string {
	u, err := url.Parse(o.IssuerURL)
	if err != nil {
		return nil
	}
	if h := hintFor(u); h != nil && h.warn != nil {
		return h.warn(o)
	}
	return nil
}

func groupMappingUsed(rm config.RoleMapping) bool {
	return len(rm.Admin)+len(rm.Operator)+len(rm.Viewer) > 0
}

// ---------------------------------------------------------------- Entra ---

// entraIssuerPath is the path of an Entra ID issuer: the tenant GUID and the
// version of the endpoints.
var entraIssuerPath = regexp.MustCompile(`^/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/v2\.0$`)

// entraHosts are the sign-in hosts of Microsoft Entra ID (public cloud and the
// sovereign clouds).
var entraHosts = map[string]bool{
	"login.microsoftonline.com":        true,
	"login.microsoftonline.us":         true,
	"login.partner.microsoftonline.cn": true,
}

func isEntra(u *url.URL) bool { return entraHosts[strings.ToLower(u.Hostname())] }

// checkEntraIssuer: with "common", "organizations", a domain name or a
// trailing slash the login would only fail at runtime.
func checkEntraIssuer(u *url.URL) string {
	if !entraIssuerPath.MatchString(u.Path) || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Sprintf("Entra ID needs the tenant ID (GUID) and the endpoint version in the issuer URL: https://%s/<TENANT-GUID>/v2.0 "+
			"(not \"common\", \"organizations\" or a domain name, no trailing slash); got %q", strings.ToLower(u.Host), u.Path)
	}
	return ""
}

func warnEntra(o config.OIDCConfig) []string {
	if o.GroupsClaim == "roles" {
		return nil
	}
	return []string{fmt.Sprintf("auth.oidc.groupsClaim is %q with an Entra ID issuer: Entra ID omits the groups claim for users in more than 200 groups (group overage) and lists object IDs only; "+
		"use app roles instead (groupsClaim: roles, roleMapping with the role values of the app registration, e.g. myriarch.admin)", o.GroupsClaim)}
}

// ------------------------------------------------------------- Keycloak ---

var keycloakIssuerPath = regexp.MustCompile(`^(/auth)?/realms/[^/]+$`)

func isKeycloak(u *url.URL) bool {
	return strings.HasPrefix(u.Path, "/realms/") || strings.HasPrefix(u.Path, "/auth/realms/")
}

func checkKeycloakIssuer(u *url.URL) string {
	if !keycloakIssuerPath.MatchString(u.Path) || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Sprintf("Keycloak issuers have the form https://<host>/realms/<realm> (no trailing slash, no endpoint path); got %q", u.Path)
	}
	return ""
}

// ----------------------------------------------------------------- Okta ---

var oktaServerPath = regexp.MustCompile(`^/oauth2/([^/]+)$`)

func isOkta(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return strings.HasSuffix(h, ".okta.com") || strings.HasSuffix(h, ".oktapreview.com") || strings.HasSuffix(h, ".okta-emea.com")
}

func checkOktaIssuer(u *url.URL) string {
	ok := u.Path == ""
	if m := oktaServerPath.FindStringSubmatch(u.Path); m != nil && m[1] != "v1" {
		ok = true
	}
	if !ok || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Sprintf("Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, "+
			"without endpoint path or trailing slash; got %q", u.Path)
	}
	return ""
}

// ------------------------------------------------------------------ Dex ---

func isDex(u *url.URL) bool { return u.Path == "/dex" || strings.HasSuffix(u.Path, "/dex") }

func warnDex(o config.OIDCConfig) []string {
	if groupMappingUsed(o.RoleMapping) && !contains(o.Scopes, "groups") {
		return []string{"auth.oidc.scopes does not request \"groups\" although auth.oidc.roleMapping maps groups and the issuer is a Dex instance: Dex sends the groups claim only for the scope groups"}
	}
	return nil
}

// --------------------------------------------------------------- Google ---

func isGoogle(u *url.URL) bool { return strings.EqualFold(u.Hostname(), "accounts.google.com") }

func checkGoogleIssuer(u *url.URL) string {
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Sprintf("the Google issuer is https://accounts.google.com (no path, no trailing slash); got %q", u.Path)
	}
	return ""
}

func warnGoogle(o config.OIDCConfig) []string {
	if groupMappingUsed(o.RoleMapping) {
		return []string{"auth.oidc.roleMapping maps groups, but Google ID tokens carry no groups claim: map users with auth.oidc.roleMapping.users (email:<address>) or leave the group lists empty"}
	}
	return nil
}
