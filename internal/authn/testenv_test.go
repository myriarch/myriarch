// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/httpserver"
	"github.com/myriarch/myriarch/internal/store"
	"github.com/myriarch/myriarch/internal/store/postgres"
	"github.com/myriarch/myriarch/internal/testutil/pgtest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestMain(m *testing.M) { os.Exit(pgtest.RunWithMigratedTemplate(m)) }

// fakeClock is a controllable clock shared by the service under test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Now().UTC().Truncate(time.Microsecond)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	db    *postgres.DB
	svc   *authn.Service
	srv   *httptest.Server
	clock *fakeClock
	base  string
	reg   *prometheus.Registry
	bus   eventbus.Bus
	// configuration the service was created with (newReplica)
	authCfg   config.AuthConfig
	serverCfg config.ServerConfig
	opampCfg  config.OpAMPConfig
	httpCli   *http.Client
}

type envOpts struct {
	auth    func(c *config.AuthConfig)
	server  func(c *config.ServerConfig)
	opamp   func(c *config.OpAMPConfig)
	httpCli *http.Client
	dev     bool
	// bus (optional) shares authentication invalidations between replicas
	bus eventbus.Bus
}

func devUsers() []config.DevUser {
	return []config.DevUser{
		{Username: "admin", Password: "admin-pw", Role: domain.RoleAdmin},
		{Username: "operator", Password: "operator-pw", Role: domain.RoleOperator},
		{Username: "viewer", Password: "viewer-pw", Role: domain.RoleViewer},
	}
}

func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	db := postgres.New(pgtest.NewPool(t), nil)
	e := &env{t: t, db: db, clock: newClock(), reg: prometheus.NewRegistry()}

	var handler atomic.Pointer[http.Handler]
	nf := http.NotFoundHandler()
	handler.Store(&nf)
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*handler.Load()).ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	e.base = e.srv.URL

	authCfg := config.AuthConfig{
		SessionTTL: 12 * time.Hour, CookieSecure: false,
		DevLogin: config.DevLoginConfig{Enabled: true, Users: devUsers()},
	}
	if o.auth != nil {
		o.auth(&authCfg)
	}
	serverCfg := config.ServerConfig{ListenAddr: ":0", PublicURL: e.srv.URL}
	if o.server != nil {
		o.server(&serverCfg)
	}
	opampCfg := config.Default().OpAMP
	if o.opamp != nil {
		o.opamp(&opampCfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.bus, e.authCfg, e.serverCfg, e.opampCfg, e.httpCli = o.bus, authCfg, serverCfg, opampCfg, o.httpCli
	svc, err := authn.New(ctx, authCfg, serverCfg, opampCfg, authn.Deps{
		Store: db, Audit: audit.NewRecorder(db.Audit(), quiet), Registerer: e.reg, DevMode: o.dev, Logger: quiet,
		HTTPClient: o.httpCli, Now: e.clock.Now, Bus: o.bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	hs, err := httpserver.New(serverCfg, httpserver.Deps{Auth: svc, Modules: []api.Module{svc.Module()}, Registerer: prometheus.NewRegistry(), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	hh := hs.Handler()
	handler.Store(&hh)
	return e
}

// client is a browser-like HTTP client with a cookie jar that does not
// follow redirects.
type client struct {
	t    *testing.T
	base string
	http *http.Client
	jar  *cookiejar.Jar
	csrf string
}

func (e *env) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.base, jar: jar, http: &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) cookie(name string) string {
	u, _ := url.Parse(c.base)
	for _, ck := range c.jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

type result struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (r result) JSON(v any) {
	if err := json.Unmarshal(r.Body, v); err != nil {
		panic(fmt.Sprintf("decode %q: %v", r.Body, err))
	}
}

func (r result) Problem() api.Problem {
	var p api.Problem
	r.JSON(&p)
	return p
}

// req performs a request; csrf == true adds the X-CSRF-Token header of the client.
func (c *client) req(method, path string, body any, csrf bool, hdr ...string) result {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			rd = bytes.NewReader(raw)
		}
	}
	rq, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		rq.Header.Set(authn.CSRFHeader, c.cookie(authn.CSRFCookie))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		rq.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.http.Do(rq)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return result{Code: resp.StatusCode, Header: resp.Header, Body: raw}
}

func (c *client) devLogin(user, pw string) result {
	return c.req("POST", "/api/v1/auth/dev-login", map[string]string{"username": user, "password": pw}, false)
}

func (c *client) mustDevLogin(user string) {
	c.t.Helper()
	pw := map[string]string{"admin": "admin-pw", "operator": "operator-pw", "viewer": "viewer-pw"}[user]
	if r := c.devLogin(user, pw); r.Code != 200 {
		c.t.Fatalf("login %s: %d %s", user, r.Code, r.Body)
	}
}

func (e *env) auditActions() []domain.AuditEvent {
	e.t.Helper()
	// audit writes are synchronous in the handler path
	evs, _, err := e.db.Audit().List(context.Background(), store.AuditFilter{Limit: 500})
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func (e *env) findAudit(action string) []domain.AuditEvent {
	var out []domain.AuditEvent
	for _, a := range e.auditActions() {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// ------------------------------------------------------------- fake IdP ---

type fakeIdP struct {
	t        *testing.T
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	secret   string

	mu       sync.Mutex
	codes    map[string]idpCode
	claims   func(nonce string) map[string]any
	badNonce bool
	down     atomic.Bool
	// jwksPad and discoveryPad append that many bytes of white space to the key set and to the discovery document.
	jwksPad, discoveryPad int
	tokenReq              atomic.Int32

	// assertCerts (when set) makes the IdP require private_key_jwt: the client
	// authenticates with a signed assertion that must verify against one of
	// these certificates; a client secret is refused.
	assertCerts []*x509.Certificate
	assertions  []assertionSeen
	usedJTI     map[string]bool
}

// assertionSeen is what the IdP found in one client assertion.
type assertionSeen struct {
	header, claims map[string]any
	form           url.Values
}

type idpCode struct {
	nonce, challenge, redirect string
}

func newIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{t: t, key: key, clientID: "myriarchd", secret: "s3cret", codes: map[string]idpCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		defer func() { _, _ = w.Write(bytes.Repeat([]byte(" "), p.discoveryPad)) }()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		defer func() { _, _ = w.Write(bytes.Repeat([]byte(" "), p.jwksPad)) }()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (p *fakeIdP) jwt(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

// authorize emulates the user agent visiting the authorization endpoint: it
// validates the request like a real IdP and returns the redirect target with
// code and state.
func (p *fakeIdP) authorize(location string) (callback string, err error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	q := u.Query()
	switch {
	case !strings.HasPrefix(location, p.srv.URL+"/authorize"):
		return "", fmt.Errorf("unexpected authorization endpoint %s", location)
	case q.Get("client_id") != p.clientID:
		return "", fmt.Errorf("client_id %q", q.Get("client_id"))
	case q.Get("response_type") != "code":
		return "", fmt.Errorf("response_type %q", q.Get("response_type"))
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return "", fmt.Errorf("PKCE S256 challenge is required, got method=%q challenge=%q", q.Get("code_challenge_method"), q.Get("code_challenge"))
	case q.Get("state") == "" || q.Get("nonce") == "":
		return "", fmt.Errorf("state and nonce are required")
	case !strings.Contains(" "+q.Get("scope")+" ", " openid "):
		return "", fmt.Errorf("scope %q lacks openid", q.Get("scope"))
	}
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	p.mu.Lock()
	p.codes[code] = idpCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	p.mu.Unlock()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	cb.RawQuery = cq.Encode()
	return cb.String(), nil
}

func (p *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	p.tokenReq.Add(1)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if p.assertCerts != nil {
		if msg := p.checkAssertion(r); msg != "" {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": msg})
			return
		}
		goto authenticated
	}
	{
		id, secret, hasBasic := r.BasicAuth()
		if !hasBasic {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		} else {
			id, _ = url.QueryUnescape(id)
			secret, _ = url.QueryUnescape(secret)
		}
		if id != p.clientID || secret != p.secret {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
	}
authenticated:
	p.mu.Lock()
	entry, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code")) // single use
	p.mu.Unlock()
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != entry.redirect {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if b64(sum[:]) != entry.challenge {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PKCE verification failed"}`))
		return
	}
	nonce := entry.nonce
	if p.badNonce {
		nonce = "attacker-nonce"
	}
	now := time.Now()
	claims := map[string]any{"iss": p.srv.URL, "aud": p.clientID, "sub": "user-1", "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "nonce": nonce, "email": "una@example.com", "name": "Una User", "groups": []string{}}
	if p.claims != nil {
		for k, v := range p.claims(nonce) {
			claims[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": p.jwt(claims)})
}

var _ = bytes.NewReader

// checkAssertion validates the client assertion of a token request like Entra
// ID does ("" = fine, else the reason).
func (p *fakeIdP) checkAssertion(r *http.Request) string {
	f := r.PostForm
	if _, _, basic := r.BasicAuth(); basic || f.Get("client_secret") != "" {
		return "a client secret must not be sent together with a client assertion"
	}
	if f.Get("client_id") != p.clientID {
		return "client_id must be in the body: " + f.Get("client_id")
	}
	if f.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		return "client_assertion_type " + f.Get("client_assertion_type")
	}
	parts := strings.Split(f.Get("client_assertion"), ".")
	if len(parts) != 3 {
		return "client_assertion is not a JWT"
	}
	dec := func(s string, v any) error {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, v)
	}
	var hdr, claims map[string]any
	if dec(parts[0], &hdr) != nil || dec(parts[1], &claims) != nil {
		return "malformed JWT"
	}
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		return fmt.Sprintf("header alg/typ: %v/%v", hdr["alg"], hdr["typ"])
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "signature encoding"
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	var cert *x509.Certificate
	for _, c := range p.assertCerts {
		if pub, ok := c.PublicKey.(*rsa.PublicKey); ok && rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) == nil {
			cert = c
		}
	}
	if cert == nil {
		return "the signature does not verify against a registered certificate"
	}
	s1, s256 := sha1.Sum(cert.Raw), sha256.Sum256(cert.Raw)
	if hdr["x5t"] != b64(s1[:]) || hdr["x5t#S256"] != b64(s256[:]) {
		return "x5t / x5t#S256 do not identify the certificate"
	}
	if claims["aud"] != p.srv.URL+"/token" {
		return fmt.Sprintf("aud %v is not the token endpoint", claims["aud"])
	}
	if claims["iss"] != p.clientID || claims["sub"] != p.clientID {
		return "iss and sub must be the client id"
	}
	num := func(k string) int64 { f, _ := claims[k].(float64); return int64(f) }
	now := time.Now().Unix()
	if num("exp") <= now || num("exp")-num("iat") != 300 || num("nbf") > now+60 || num("iat") > now+60 || num("iat") < now-120 {
		return fmt.Sprintf("iat/nbf/exp out of range: %v", claims)
	}
	jti, _ := claims["jti"].(string)
	p.mu.Lock()
	defer p.mu.Unlock()
	if jti == "" || p.usedJTI[jti] {
		return "jti missing or reused"
	}
	if p.usedJTI == nil {
		p.usedJTI = map[string]bool{}
	}
	p.usedJTI[jti] = true
	p.assertions = append(p.assertions, assertionSeen{header: hdr, claims: claims, form: f})
	return ""
}

// newReplica creates a second authn service on the same database, event bus and
// clock (another myriarchd replica).
func (e *env) newReplica() *authn.Service {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	svc, err := authn.New(ctx, e.authCfg, e.serverCfg, e.opampCfg, authn.Deps{
		Store: e.db, Audit: audit.NewRecorder(e.db.Audit(), quiet), Registerer: prometheus.NewRegistry(), Logger: quiet,
		HTTPClient: e.httpCli, Now: e.clock.Now, Bus: e.bus,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

// authenticate runs the authentication of svc for a request carrying the
// session cookie of c or the bearer token.
func authenticate(svc *authn.Service, c *client, bearer string) (*authz.Principal, error) {
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.RemoteAddr = "192.0.2.10:1234"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	} else if c != nil {
		if v := c.cookie(authn.SessionCookie); v != "" {
			r.AddCookie(&http.Cookie{Name: authn.SessionCookie, Value: v})
		}
	}
	return svc.Authenticate(r)
}
