// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/myriarch/myriarch/internal/authn/clientassert"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
	"github.com/myriarch/myriarch/internal/store"
)

const (
	oidcCookieTTL = 10 * time.Minute
	callbackPath  = "/api/v1/auth/callback"
)

// DiscoveryRetryInitial is the first delay before OIDC discovery is retried
// in the background (doubles up to one minute). Variable for tests.
var DiscoveryRetryInitial = 5 * time.Second

type oidcClient struct {
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

// startOIDC performs discovery; if the IdP is unreachable it retries in the
// background until it succeeds or ctx ends (the login endpoints answer 503
// meanwhile, /auth/config reports OIDC as enabled).
func (s *Service) startOIDC(ctx context.Context) {
	if err := s.discoverOIDC(ctx); err == nil {
		return
	} else {
		s.log.Warn("OIDC discovery failed, will retry in the background", "issuer", s.cfg.OIDC.IssuerURL, "error", err)
	}
	go func() {
		delay := DiscoveryRetryInitial
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if err := s.discoverOIDC(ctx); err == nil {
				return
			} else {
				s.log.Warn("OIDC discovery failed", "issuer", s.cfg.OIDC.IssuerURL, "error", err, "retry_in", delay)
			}
			if delay < time.Minute {
				delay *= 2
			}
		}
	}()
}

func (s *Service) discoverOIDC(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, s.http), 15*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(dctx, s.cfg.OIDC.IssuerURL)
	if err != nil {
		return err
	}
	scopes := s.cfg.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	endpoint := provider.Endpoint()
	if s.assertion != nil {
		// private_key_jwt: client_id travels in the body, there is no secret
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	c := &oidcClient{
		verifier: provider.Verifier(&oidc.Config{ClientID: s.cfg.OIDC.ClientID}),
		oauth: &oauth2.Config{
			ClientID: s.cfg.OIDC.ClientID, ClientSecret: s.cfg.OIDC.ClientSecret,
			Endpoint: endpoint, RedirectURL: s.redirectURL(), Scopes: scopes,
		},
	}
	s.oidcMu.Lock()
	s.oidc = c
	s.oidcMu.Unlock()
	s.log.Info("OIDC provider ready", "issuer", s.cfg.OIDC.IssuerURL)
	return nil
}

func (s *Service) redirectURL() string {
	return strings.TrimRight(s.server.PublicURL, "/") + callbackPath
}

func (s *Service) oidcClient() *oidcClient {
	s.oidcMu.RLock()
	defer s.oidcMu.RUnlock()
	return s.oidc
}

// ----------------------------------------------------- signed state cookie ---

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Redirect string `json:"r"`
	Expires  int64  `json:"e"`
}

func (s *Service) sign(payload []byte) string {
	b := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.cookieSecret)
	mac.Write([]byte(b))
	return b + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifySigned(v string) ([]byte, error) {
	i := strings.IndexByte(v, '.')
	if i <= 0 {
		return nil, errors.New("malformed state cookie")
	}
	mac := hmac.New(sha256.New, s.cookieSecret)
	mac.Write([]byte(v[:i]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(v[i+1:])
	if err != nil || !hmac.Equal(got, want) {
		return nil, errors.New("invalid state cookie signature")
	}
	return base64.RawURLEncoding.DecodeString(v[:i])
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// safeRedirect returns p if it is a safe relative UI path, else "/". Browsers
// read the rest of "//..." and "/\..." as a host name, so neither a slash nor a
// backslash may follow the leading slash; backslashes and control characters
// are refused anywhere in the path.
func safeRedirect(p string) string {
	if p == "" || p[0] != '/' || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.ContainsAny(p, "\\") {
		return "/"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return p
}

// ------------------------------------------------------------- handlers ---

// handleStartOIDC implements startOidcLogin.
func (s *Service) handleStartOIDC(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled {
		writeError(w, r, fmt.Errorf("%w: OIDC login is not enabled", domain.ErrNotFound))
		return
	}
	key := "ip:" + realip.LimiterKey(s.clientIP(r))
	if s.oidcLimited(w, r, key) {
		return
	}
	c := s.oidcClient()
	if c == nil {
		w.Header().Set("Retry-After", "10")
		writeError(w, r, fmt.Errorf("%w: the identity provider is not reachable yet", domain.ErrUnavailable))
		return
	}
	state, err1 := randomString(32)
	nonce, err2 := randomString(32)
	if err := errors.Join(err1, err2); err != nil {
		writeError(w, r, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	st := loginState{State: state, Nonce: nonce, Verifier: verifier, Redirect: safeRedirect(r.URL.Query().Get("redirect")),
		Expires: s.now().Add(oidcCookieTTL).Unix()}
	payload, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: s.sign(payload), Path: "/api/v1/auth/", MaxAge: int(oidcCookieTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	url := c.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusFound)
}

// handleOIDCCallback implements oidcCallback.
func (s *Service) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled {
		writeError(w, r, fmt.Errorf("%w: OIDC login is not enabled", domain.ErrNotFound))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	key := "ip:" + realip.LimiterKey(s.clientIP(r))
	// Limits first: no state is recorded and no request goes to the identity provider for an address that keeps failing.
	if s.oidcLimited(w, r, key) {
		return
	}
	clearOIDCCookie := func() {
		http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/", MaxAge: -1,
			HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	}
	// stateVerified is set once the signed login state cookie was verified: failures before that point can be
	// produced by anyone, so they reach the audit log at most once per minute and address.
	stateVerified := false
	fail := func(reason, uiError string, err error, subject string) {
		clearOIDCCookie()
		s.oidcFail.take(key)
		s.metricLogin.WithLabelValues("oidc", "failure").Inc()
		if stateVerified {
			s.record(r, s.loginAudit(r, subject, subject, "oidc", map[string]any{"reason": reason, "error": errString(err)}, err))
		} else if ok, _, _ := s.oidcAudit.allow(key); ok {
			s.record(r, s.loginAudit(r, subject, subject, "oidc", map[string]any{"reason": reason, "error": errString(err)}, err))
		}
		http.Redirect(w, r, "/login?error="+uiError, http.StatusFound)
	}

	c := s.oidcClient()
	if c == nil {
		fail("provider_unavailable", "oidc_error", errors.New("identity provider not reachable"), "")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		ui := "oidc_error"
		if e == "access_denied" {
			ui = "access_denied"
		}
		fail("idp_error:"+sanitizeAudit(e, 64), ui, fmt.Errorf("identity provider returned %q: %s", sanitizeAudit(e, 64), sanitizeAudit(r.URL.Query().Get("error_description"), 200)), "")
		return
	}
	ck, err := r.Cookie(oidcCookie)
	if err != nil {
		fail("missing_state_cookie", "oidc_error", errors.New("login state cookie missing (expired or cookies disabled)"), "")
		return
	}
	raw, err := s.verifySigned(ck.Value)
	if err != nil {
		fail("invalid_state_cookie", "oidc_error", err, "")
		return
	}
	var st loginState
	if err := json.Unmarshal(raw, &st); err != nil || st.State == "" || st.Nonce == "" || st.Verifier == "" {
		fail("invalid_state_cookie", "oidc_error", errors.New("malformed login state"), "")
		return
	}
	stateVerified = true
	if s.now().Unix() > st.Expires {
		fail("expired_state", "oidc_error", errors.New("login attempt expired"), "")
		return
	}
	state := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(state), []byte(st.State)) != 1 {
		fail("state_mismatch", "oidc_error", errors.New("state mismatch"), "")
		return
	}
	// A login state is single use: the first callback marks it consumed (on all
	// replicas), a replay of the same state is refused.
	sum := sha256.Sum256([]byte(st.State))
	fresh, err := s.deps.Store.KV().PutIfAbsent(r.Context(), store.KVPrefixOIDCUsed+hex.EncodeToString(sum[:]), []byte("1"))
	if err != nil {
		fail("state_store", "oidc_error", fmt.Errorf("%w: cannot record login state", domain.ErrUnavailable), "")
		return
	}
	if !fresh {
		fail("state_replayed", "oidc_error", errors.New("login state was already used"), "")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		fail("missing_code", "oidc_error", errors.New("authorization code missing"), "")
		return
	}

	xctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), s.http), 20*time.Second)
	defer cancel()
	exchangeOpts := []oauth2.AuthCodeOption{oauth2.VerifierOption(st.Verifier)}
	if s.assertion != nil {
		assertion, aerr := s.assertion.Assertion(s.cfg.OIDC.ClientID, c.oauth.Endpoint.TokenURL, s.now())
		if aerr != nil {
			fail("client_assertion", "oidc_error", aerr, "")
			return
		}
		exchangeOpts = append(exchangeOpts,
			oauth2.SetAuthURLParam("client_assertion_type", clientassert.Type),
			oauth2.SetAuthURLParam("client_assertion", assertion))
	}
	tok, err := c.oauth.Exchange(xctx, code, exchangeOpts...)
	if err != nil {
		fail("token_exchange", "oidc_error", err, "")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		fail("no_id_token", "oidc_error", errors.New("token response contains no id_token"), "")
		return
	}
	idt, err := c.verifier.Verify(xctx, rawID)
	if err != nil {
		fail("id_token_invalid", "oidc_error", err, "")
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		fail("nonce_mismatch", "oidc_error", errors.New("nonce mismatch"), idt.Subject)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		fail("claims", "oidc_error", err, idt.Subject)
		return
	}
	if idt.Subject == "" {
		fail("no_subject", "oidc_error", errors.New("id token has no subject"), "")
		return
	}
	for _, prefix := range []string{domain.LocalSubjectPrefix, domain.BootstrapSubjectPrefix} {
		if strings.HasPrefix(idt.Subject, prefix) {
			// the prefixes are reserved for local (dev) accounts and the bootstrap admin
			fail("reserved_subject", "oidc_error", fmt.Errorf("id token subject uses the reserved prefix %q", prefix), "")
			return
		}
	}
	email := claimString(claims, s.emailClaim())
	name := claimString(claims, s.nameClaim())
	if name == "" {
		name = claimString(claims, "preferred_username")
	}
	groups := claimStrings(claims, s.cfg.OIDC.GroupsClaim)
	// email_verified vouches for the standard "email" claim only: with another
	// emailClaim an "email:" mapping entry never matches.
	role := s.roleFor(groups, idt.Subject, email, emailVerified(claims) && s.emailClaim() == "email")
	if role == "" {
		fail("no_role", "access_denied", fmt.Errorf("no role mapped for user (groups: %d)", len(groups)), idt.Subject)
		return
	}
	user, err := s.deps.Store.Identity().UpsertUser(r.Context(), domain.User{
		ID: newUUID(), Subject: idt.Subject, Provider: "oidc", Issuer: s.cfg.OIDC.IssuerURL, Email: truncate(email, 320), Name: truncate(name, 200), Role: role, RoleSource: "mapping",
	})
	if err != nil {
		fail("user_store", "oidc_error", err, idt.Subject)
		return
	}
	if user.Disabled {
		fail("user_disabled", "access_denied", errors.New("account is disabled"), idt.Subject)
		return
	}
	if err := s.startSession(w, r, user, "oidc"); err != nil {
		fail("session", "oidc_error", err, idt.Subject)
		return
	}
	clearOIDCCookie()
	s.metricLogin.WithLabelValues("oidc", "success").Inc()
	s.record(r, s.loginAudit(r, user.ID.String(), userDisplayName(user), "oidc", map[string]any{"subject": user.Subject, "role": string(user.Role), "groups": len(groups)}, nil))
	http.Redirect(w, r, safeRedirect(st.Redirect), http.StatusFound)
}

// errString returns err for audit details: single line, without control
// characters, at most 256 characters (identity provider texts are untrusted).
func errString(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeAudit(err.Error(), 256)
}

func sanitizeAudit(s string, max int) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		out = append(out, r)
		if len(out) >= max {
			break
		}
	}
	return string(out)
}

// nameClaim and emailClaim are the configured claim names; an unset value
// (a configuration that did not start from config.Default()) means the
// standard claims.
func (s *Service) nameClaim() string {
	if s.cfg.OIDC.NameClaim != "" {
		return s.cfg.OIDC.NameClaim
	}
	return "name"
}

func (s *Service) emailClaim() string {
	if s.cfg.OIDC.EmailClaim != "" {
		return s.cfg.OIDC.EmailClaim
	}
	return "email"
}

// equalFoldASCII compares two strings ignoring the case of ASCII letters only (no Unicode folding, so that
// characters such as the Kelvin sign do not match "k").
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// oidcLimited answers the request with a redirect to the login page and a Retry-After header when the client
// address has used up its budget of failed OIDC callbacks; it reports whether it did.
func (s *Service) oidcLimited(w http.ResponseWriter, r *http.Request, key string) bool {
	blocked, retry := s.oidcFail.blocked(key)
	if !blocked {
		return false
	}
	secs := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Cache-Control", "no-store")
	s.metricLogin.WithLabelValues("oidc", "rate_limited").Inc()
	if ok, _, _ := s.oidcAudit.allow(key); ok {
		err := fmt.Errorf("%w: too many failed OIDC logins from this client address", domain.ErrTooManyRequests)
		s.record(r, s.loginAudit(r, "", "", "oidc", map[string]any{"reason": "rate_limited", "limitedBy": "client address"}, err))
	}
	http.Redirect(w, r, "/login?error=oidc_error", http.StatusFound)
	return true
}

// roleFor returns the highest role that the group mapping or the user mapping
// grants, else the configured default role ("" = deny). A "sub:<x>" entry of
// roleMapping.users matches the subject exactly; an "email:<x>" entry matches
// the e-mail address ignoring the case of ASCII letters and only when the identity provider
// states that it verified the address (email_verified, which covers the claim
// "email" only: the caller passes verified=false for another emailClaim).
func (s *Service) roleFor(groups []string, subject, email string, verified bool) domain.Role {
	rm := s.cfg.OIDC.RoleMapping
	hasGroup := func(list []string) bool {
		for _, want := range list {
			for _, g := range groups {
				// exact comparison: no case or Unicode folding of group names
				if strings.TrimSpace(want) == g {
					return true
				}
			}
		}
		return false
	}
	hasUser := func(list []string) bool {
		for _, entry := range list {
			entry = strings.TrimSpace(entry)
			if want, ok := strings.CutPrefix(entry, "sub:"); ok {
				if subject != "" && want == subject {
					return true
				}
			} else if want, ok := strings.CutPrefix(entry, "email:"); ok {
				if verified && email != "" && equalFoldASCII(want, email) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case hasGroup(rm.Admin) || hasUser(rm.Users.Admin):
		return domain.RoleAdmin
	case hasGroup(rm.Operator) || hasUser(rm.Users.Operator):
		return domain.RoleOperator
	case hasGroup(rm.Viewer) || hasUser(rm.Users.Viewer):
		return domain.RoleViewer
	}
	if s.cfg.OIDC.DefaultRole.Valid() {
		return s.cfg.OIDC.DefaultRole
	}
	return ""
}

// emailVerified reports whether the ID token states email_verified: the
// boolean true or the string "true" (some providers send a string).
func emailVerified(claims map[string]any) bool {
	switch v := claims["email_verified"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

// claimValue returns the value of a claim. The claim name is tried as a
// literal key first (URL-like claim names), then as a dotted path
// ("realm_access.roles" for roles nested in an object claim).
func claimValue(claims map[string]any, name string) any {
	if name == "" {
		return nil
	}
	if v, ok := claims[name]; ok {
		return v
	}
	if !strings.Contains(name, ".") {
		return nil
	}
	var cur any = claims
	for _, part := range strings.Split(name, ".") {
		m, isMap := cur.(map[string]any)
		if !isMap {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// claimString extracts a string claim ("" if it is missing or not a string).
func claimString(claims map[string]any, name string) string {
	s, _ := claimValue(claims, name).(string)
	return s
}

// claimStrings extracts a list claim: a string, a list of strings, or an
// object, whose keys count in sorted order (Zitadel lists the project roles of
// a user as an object keyed by role).
func claimStrings(claims map[string]any, name string) []string {
	switch x := claimValue(claims, name).(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	case map[string]any:
		out := make([]string, 0, len(x))
		for k := range x {
			if k != "" {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	return nil
}
