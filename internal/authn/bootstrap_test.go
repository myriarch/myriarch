// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/bcrypt"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/httpserver"
	"github.com/myriarch/myriarch/internal/store"
)

// bootstrapFiles are the credential files of a bootstrap admin with generated
// secrets (nothing here is a fixed credential).
type bootstrapFiles struct {
	dir       string
	tokenFile string
	hashFile  string
	token     string
	password  string
	hash      string
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func newBootstrapFiles(t *testing.T) *bootstrapFiles {
	t.Helper()
	f := &bootstrapFiles{dir: t.TempDir(), token: randHex(t, 24), password: randHex(t, 12)}
	h, err := bcrypt.GenerateFromPassword([]byte(f.password), 10)
	if err != nil {
		t.Fatal(err)
	}
	f.hash = string(h)
	f.tokenFile, f.hashFile = filepath.Join(f.dir, "token"), filepath.Join(f.dir, "password-hash")
	f.writeToken(t, f.token)
	f.writeHash(t, f.hash)
	return f
}

func (f *bootstrapFiles) writeToken(t *testing.T, v string) {
	t.Helper()
	if err := os.WriteFile(f.tokenFile, []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *bootstrapFiles) writeHash(t *testing.T, v string) {
	t.Helper()
	if err := os.WriteFile(f.hashFile, []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// bootstrapAuth configures an active bootstrap admin with both credentials
// and no dev login; mutate may change it further.
func bootstrapAuth(f *bootstrapFiles, mutate ...func(c *config.AuthConfig)) func(c *config.AuthConfig) {
	return func(c *config.AuthConfig) {
		c.DevLogin = config.DevLoginConfig{}
		c.BootstrapAdmin = config.BootstrapAdminConfig{Enabled: true, Username: "admin", PasswordHashFile: f.hashFile, TokenFile: f.tokenFile}
		for _, m := range mutate {
			m(c)
		}
	}
}

func (c *client) bootstrapLogin(body any) result {
	c.t.Helper()
	return c.req("POST", "/api/v1/auth/bootstrap-login", body, false)
}

func (c *client) tokenLogin(tok string) result {
	c.t.Helper()
	return c.bootstrapLogin(map[string]string{"token": tok})
}

func (c *client) passwordLogin(user, pw string) result {
	c.t.Helper()
	return c.bootstrapLogin(map[string]string{"username": user, "password": pw})
}

// syncBuffer is a log sink that tests can read while the service writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func bufferLogger(b *syncBuffer) *slog.Logger { return slog.New(slog.NewTextHandler(b, nil)) }

// newService creates another authn service on the database, clock and event
// bus of e (another myriarchd replica, possibly with another configuration).
func newService(t *testing.T, e *env, auth config.AuthConfig, lg *slog.Logger) *authn.Service {
	t.Helper()
	if lg == nil {
		lg = quiet
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc, err := authn.New(ctx, auth, e.serverCfg, e.opampCfg, authn.Deps{
		Store: e.db, Audit: audit.NewRecorder(e.db.Audit(), quiet), Registerer: prometheus.NewRegistry(), Logger: lg,
		HTTPClient: e.httpCli, Now: e.clock.Now, Bus: e.bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// serveService publishes svc over HTTP and returns a client for it.
func (e *env) serveService(svc *authn.Service) *client {
	e.t.Helper()
	hs, err := httpserver.New(e.serverCfg, httpserver.Deps{Auth: svc, Modules: []api.Module{svc.Module()}, Registerer: prometheus.NewRegistry(), Logger: quiet})
	if err != nil {
		e.t.Fatal(err)
	}
	srv := httptest.NewServer(hs.Handler())
	e.t.Cleanup(srv.Close)
	c := e.newClient()
	c.base = srv.URL
	return c
}

func (e *env) bootstrapUsers() []domain.User {
	e.t.Helper()
	users, err := e.db.Identity().ListUsers(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []domain.User
	for _, u := range users {
		if u.Provider == domain.ProviderBootstrap {
			out = append(out, u)
		}
	}
	return out
}

func detailOf(r result) string { return r.Problem().Detail }

func TestBootstrapTokenSignInWorksOnce(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})

	c := e.newClient()
	r := c.tokenLogin(f.token)
	if r.Code != 200 {
		t.Fatalf("token sign-in: %d %s", r.Code, r.Body)
	}
	var me dto.Me
	r.JSON(&me)
	if me.AuthMethod != "bootstrap" || me.Role != domain.RoleAdmin || me.Name != "admin" || me.Type != "user" || me.CSRFToken == "" {
		t.Fatalf("me: %+v", me)
	}
	if c.cookie(authn.SessionCookie) == "" || c.cookie(authn.CSRFCookie) != me.CSRFToken {
		t.Fatal("session and CSRF cookies must be set like for other sign-ins")
	}
	var again dto.Me
	c.req("GET", "/api/v1/auth/me", nil, false).JSON(&again)
	if again.AuthMethod != "bootstrap" || again.CSRFToken != me.CSRFToken {
		t.Fatalf("me with the session: %+v", again)
	}
	users := e.bootstrapUsers()
	if len(users) != 1 || users[0].Subject != "bootstrap:admin" || users[0].Role != domain.RoleAdmin || users[0].Provider != "bootstrap" || users[0].Issuer != "" {
		t.Fatalf("user row: %+v", users)
	}

	// the same token does not work a second time
	c2 := e.newClient()
	r = c2.tokenLogin(f.token)
	if r.Code != 401 || !strings.Contains(detailOf(r), "invalid bootstrap credentials") || c2.cookie(authn.SessionCookie) != "" {
		t.Fatalf("second use of the token: %d %s", r.Code, r.Body)
	}

	// ... also not through another replica on the same database
	other := e.serveService(newService(t, e, e.authCfg, nil))
	if r := other.tokenLogin(f.token); r.Code != 401 {
		t.Fatalf("second replica: %d %s", r.Code, r.Body)
	}

	ok, failed := 0, 0
	for _, ev := range e.findAudit("auth.bootstrap.login") {
		if ev.Details["method"] != "bootstrap" || ev.Details["credential"] != "token" {
			t.Errorf("details: %+v", ev.Details)
		}
		switch ev.Result {
		case "success":
			ok++
			if ev.ActorID != users[0].ID.String() || ev.ActorName != "admin" || ev.TargetID != users[0].ID.String() || ev.ActorType != domain.ActorUser {
				t.Errorf("success audit: %+v", ev)
			}
		case "failure":
			failed++
			if ev.Details["reason"] != "token_used" || ev.ActorName != "bootstrap:token" {
				t.Errorf("failure audit: %+v", ev)
			}
		}
	}
	if ok != 1 || failed != 2 {
		t.Fatalf("audit: %d successes, %d failures", ok, failed)
	}
	// the marker of the consumed token stays in the KV store
	if _, err := e.db.KV().Get(context.Background(), store.KVPrefixBootstrapTokenUsed+sha256Hex(f.token)); err != nil {
		t.Fatalf("consumed marker: %v", err)
	}
}

func TestBootstrapTokenIsConsumedOnceAcrossReplicas(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	clients := []*client{e.newClient(), e.serveService(newService(t, e, e.authCfg, nil)), e.serveService(newService(t, e, e.authCfg, nil))}
	codes := make([]int, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = c.tokenLogin(f.token).Code
		}()
	}
	wg.Wait()
	wins := 0
	for _, code := range codes {
		if code == 200 {
			wins++
		} else if code != 401 {
			t.Errorf("unexpected status %d", code)
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one sign-in may consume the token, got %v", codes)
	}
}

func TestBootstrapTokenRotationNeedsNoRestart(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	if r := e.newClient().tokenLogin(f.token); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	next := randHex(t, 24)
	f.writeToken(t, next)
	if r := e.newClient().tokenLogin(f.token); r.Code != 401 {
		t.Fatalf("the old value must no longer work: %d", r.Code)
	}
	if r := e.newClient().tokenLogin(next); r.Code != 200 {
		t.Fatalf("the rotated token must work without a new service: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapPasswordSignIn(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})

	for name, tc := range map[string][2]string{
		"wrong password":               {"admin", f.password + "x"},
		"wrong user name":              {"root", f.password},
		"empty user name":              {"", f.password},
		"user name with another case":  {"Admin", f.password},
		"hash instead of the password": {"admin", f.hash},
	} {
		c := e.newClient()
		r := c.passwordLogin(tc[0], tc[1])
		if r.Code != 401 || !strings.Contains(detailOf(r), "invalid bootstrap credentials") || c.cookie(authn.SessionCookie) != "" {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	e.clock.Advance(2 * time.Minute) // limits are not under test here

	c := e.newClient()
	r := c.passwordLogin("admin", f.password)
	var me dto.Me
	r.JSON(&me)
	if r.Code != 200 || me.AuthMethod != "bootstrap" || me.Role != domain.RoleAdmin || me.CSRFToken == "" {
		t.Fatalf("password sign-in: %d %s", r.Code, r.Body)
	}
	// a password is not one-time
	if r := e.newClient().passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("second password sign-in: %d %s", r.Code, r.Body)
	}
	for _, ev := range e.findAudit("auth.bootstrap.login") {
		if ev.Details["credential"] != "password" {
			t.Errorf("credential: %+v", ev.Details)
		}
		if ev.Result == "failure" && (ev.Details["reason"] != "bad_credentials" || !strings.HasPrefix(ev.ActorName, "bootstrap:") || ev.ActorType != domain.ActorUser) {
			t.Errorf("failure audit: %+v", ev)
		}
	}
	// the user name given in a failed attempt is audited (at most 64 characters)
	e.clock.Advance(2 * time.Minute)
	long := strings.Repeat("u", 100)
	e.newClient().passwordLogin(long, "x")
	found := false
	for _, ev := range e.findAudit("auth.bootstrap.login") {
		if ev.ActorName == "bootstrap:"+long[:64] {
			found = true
		}
	}
	if !found {
		t.Error("the audited user name is cut at 64 characters")
	}
}

func TestBootstrapLoginRequestShape(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	const want = "exactly one of password and token is required"
	for name, body := range map[string]any{
		"both":         map[string]string{"username": "admin", "password": f.password, "token": f.token},
		"neither":      map[string]string{"username": "admin"},
		"empty object": map[string]string{},
	} {
		r := e.newClient().bootstrapLogin(body)
		if r.Code != 400 || r.Problem().Code != "bad_request" || !strings.Contains(detailOf(r), want) {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	for name, body := range map[string]any{
		"not JSON":      "nope",
		"unknown field": `{"token":"x","extra":1}`,
	} {
		if r := e.newClient().bootstrapLogin(body); r.Code != 400 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	if len(e.findAudit("auth.bootstrap.login")) != 0 {
		t.Error("malformed requests are not audited as sign-in attempts")
	}
	if len(e.bootstrapUsers()) != 0 {
		t.Error("malformed requests must not create the account")
	}
}

func TestBootstrapAdminInactive(t *testing.T) {
	f := newBootstrapFiles(t)
	idp := newIdP(t)

	cases := map[string]func(c *config.AuthConfig){
		"disabled in the configuration": func(c *config.AuthConfig) { c.BootstrapAdmin.Enabled = false },
		"replaced by single sign-on":    func(c *config.AuthConfig) { oidcAuth(idp)(c) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, envOpts{auth: bootstrapAuth(f, mutate), httpCli: idp.srv.Client()})
			c := e.newClient()
			for _, body := range []any{map[string]string{"token": f.token}, map[string]string{"username": "admin", "password": f.password}, map[string]string{}} {
				r := c.bootstrapLogin(body)
				if r.Code != 404 || r.Problem().Code != "not_found" || !strings.Contains(detailOf(r), "the bootstrap admin is not enabled") {
					t.Fatalf("%d %s", r.Code, r.Body)
				}
			}
			var info dto.AuthConfigInfo
			c.req("GET", "/api/v1/auth/config", nil, false).JSON(&info)
			if info.BootstrapPasswordEnabled || info.BootstrapTokenEnabled {
				t.Errorf("auth config: %+v", info)
			}
			if len(e.findAudit("auth.bootstrap.login")) != 0 || len(e.bootstrapUsers()) != 0 {
				t.Error("an inactive bootstrap admin leaves no trace")
			}
		})
	}

	t.Run("kept next to single sign-on", func(t *testing.T) {
		e := newEnv(t, envOpts{auth: bootstrapAuth(f, oidcAuth(idp), func(c *config.AuthConfig) { c.BootstrapAdmin.KeepWithOIDC = true }), httpCli: idp.srv.Client()})
		c := e.newClient()
		var info dto.AuthConfigInfo
		c.req("GET", "/api/v1/auth/config", nil, false).JSON(&info)
		if !info.OIDCEnabled || !info.BootstrapPasswordEnabled || !info.BootstrapTokenEnabled {
			t.Fatalf("auth config: %+v", info)
		}
		if r := c.passwordLogin("admin", f.password); r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
}

func TestAuthConfigReportsTheBootstrapCredentials(t *testing.T) {
	f := newBootstrapFiles(t)
	for name, tc := range map[string]struct {
		mutate         func(c *config.AuthConfig)
		password, tokn bool
	}{
		"both":          {func(c *config.AuthConfig) {}, true, true},
		"password only": {func(c *config.AuthConfig) { c.BootstrapAdmin.TokenFile = "" }, true, false},
		"token only":    {func(c *config.AuthConfig) { c.BootstrapAdmin.PasswordHashFile = "" }, false, true},
	} {
		e := newEnv(t, envOpts{auth: bootstrapAuth(f, tc.mutate)})
		var info dto.AuthConfigInfo
		r := e.newClient().req("GET", "/api/v1/auth/config", nil, false)
		r.JSON(&info)
		if info.BootstrapPasswordEnabled != tc.password || info.BootstrapTokenEnabled != tc.tokn || info.DevLoginEnabled {
			t.Errorf("%s: %+v", name, info)
		}
		if !strings.Contains(string(r.Body), `"bootstrapPasswordEnabled"`) || !strings.Contains(string(r.Body), `"bootstrapTokenEnabled"`) {
			t.Errorf("%s: the flags are always present: %s", name, r.Body)
		}
	}
	// a credential that is not configured cannot be used
	e := newEnv(t, envOpts{auth: bootstrapAuth(f, func(c *config.AuthConfig) { c.BootstrapAdmin.TokenFile = "" })})
	if r := e.newClient().tokenLogin(f.token); r.Code != 401 {
		t.Errorf("token without a token file: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapLimitPerClientAddress(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	c := e.newClient()
	for i := 0; i < 5; i++ {
		if r := c.passwordLogin("admin", "wrong-"+strconv.Itoa(i)); r.Code != 401 {
			t.Fatalf("attempt %d: %d %s", i, r.Code, r.Body)
		}
	}
	r := c.passwordLogin("admin", f.password)
	if r.Code != 429 || r.Problem().Code != "too_many_requests" {
		t.Fatalf("the sixth attempt within a minute: %d %s", r.Code, r.Body)
	}
	if secs, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q", r.Header.Get("Retry-After"))
	}
	// audited once per minute and key, with the reason and the limiting key
	c.passwordLogin("admin", f.password)
	limited := 0
	for _, ev := range e.findAudit("auth.bootstrap.login") {
		if ev.Details["reason"] == "rate_limited" {
			limited++
			if ev.Details["limitedBy"] != "client address" || ev.Result != "failure" {
				t.Errorf("audit: %+v", ev)
			}
		}
	}
	if limited != 1 {
		t.Errorf("%d rate limit audit entries, want 1", limited)
	}
	// the bucket refills
	e.clock.Advance(time.Minute)
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("after a minute: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapAccountBackoff(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	c := e.newClient()
	// five failures are free (a minute apart: the address limit is not under test)
	for i := 0; i < 5; i++ {
		if r := c.passwordLogin("admin", "wrong"); r.Code != 401 {
			t.Fatalf("failure %d: %d %s", i, r.Code, r.Body)
		}
		e.clock.Advance(time.Minute)
	}
	// the sixth failure starts the backoff of one second
	if r := c.passwordLogin("admin", "wrong"); r.Code != 401 {
		t.Fatalf("sixth failure: %d %s", r.Code, r.Body)
	}
	// the credentials files are not read while the account is delayed
	if err := os.Remove(f.hashFile); err != nil {
		t.Fatal(err)
	}
	r := c.passwordLogin("admin", f.password)
	if r.Code != 429 || r.Header.Get("Retry-After") != "1" {
		t.Fatalf("delayed account: %d retry=%q %s", r.Code, r.Header.Get("Retry-After"), r.Body)
	}
	f.writeHash(t, f.hash)
	if r := c.tokenLogin(f.token); r.Code != 429 {
		t.Fatalf("the delay applies to every credential: %d %s", r.Code, r.Body)
	}
	byAccount := 0
	for _, ev := range e.findAudit("auth.bootstrap.login") {
		if ev.Details["reason"] == "rate_limited" && ev.Details["limitedBy"] == "account" {
			byAccount++
		}
	}
	if byAccount != 1 {
		t.Errorf("%d audit entries for the delayed account, want 1 per minute", byAccount)
	}

	// the delay doubles with every further failure
	e.clock.Advance(1100 * time.Millisecond)
	if r := c.passwordLogin("admin", "wrong"); r.Code != 401 {
		t.Fatalf("after the first delay: %d %s", r.Code, r.Body)
	}
	e.clock.Advance(time.Second)
	if r := c.passwordLogin("admin", f.password); r.Code != 429 || r.Header.Get("Retry-After") != "1" && r.Header.Get("Retry-After") != "2" {
		t.Fatalf("second delay (2s): %d retry=%q", r.Code, r.Header.Get("Retry-After"))
	}
	// a success after the delay resets the account
	e.clock.Advance(time.Minute)
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("after the delay: %d %s", r.Code, r.Body)
	}
	if r := c.passwordLogin("admin", "wrong"); r.Code != 401 {
		t.Fatalf("a failure after a success is free again: %d %s", r.Code, r.Body)
	}
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("no delay after one failure: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapFailuresAreForgotten(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	c := e.newClient()
	for i := 0; i < 6; i++ {
		c.passwordLogin("admin", "wrong"+strconv.Itoa(i))
		e.clock.Advance(20 * time.Second)
	}
	e.clock.Advance(16 * time.Minute)
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("failures are forgotten after 15 minutes: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapCredentialFileProblemsAtRuntime(t *testing.T) {
	f := newBootstrapFiles(t)
	var logs syncBuffer
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	svc := newService(t, e, e.authCfg, bufferLogger(&logs))
	c := e.serveService(svc)

	if err := os.Remove(f.hashFile); err != nil {
		t.Fatal(err)
	}
	r := c.passwordLogin("admin", f.password)
	if r.Code != 503 || r.Problem().Code != "unavailable" {
		t.Fatalf("missing hash file: %d %s", r.Code, r.Body)
	}
	f.writeToken(t, "short")
	if r := c.tokenLogin("short"); r.Code != 503 {
		t.Fatalf("invalid token file: %d %s", r.Code, r.Body)
	}
	f.writeHash(t, "$2a$10$notahash")
	if r := c.passwordLogin("admin", f.password); r.Code != 503 {
		t.Fatalf("invalid hash file: %d %s", r.Code, r.Body)
	}
	reasons := 0
	for _, ev := range e.findAudit("auth.bootstrap.login") {
		if ev.Details["reason"] == "credential_unavailable" {
			reasons++
		}
	}
	if reasons != 3 {
		t.Errorf("%d credential_unavailable audit entries, want 3", reasons)
	}
	if !strings.Contains(logs.String(), "the bootstrap credential cannot be used") {
		t.Errorf("a warning is expected: %s", logs.String())
	}
	for _, secret := range []string{f.token, f.password, f.hash, "notahash"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log contains credential content %q", secret)
		}
	}
	// 503 is not a wrong guess: the account is not delayed
	f.writeHash(t, f.hash)
	e.clock.Advance(time.Minute)
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("after repairing the file: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapStartupChecksTheFiles(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	secretToken := "short-" + randHex(t, 4)
	weak, err := bcrypt.GenerateFromPassword([]byte(randHex(t, 8)), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	try := func(b config.BootstrapAdminConfig, mutate func(c *config.AuthConfig)) error {
		auth := e.authCfg
		auth.BootstrapAdmin = b
		if mutate != nil {
			mutate(&auth)
		}
		_, err := authn.New(context.Background(), auth, e.serverCfg, e.opampCfg, authn.Deps{Store: e.db, Logger: quiet, Registerer: prometheus.NewRegistry(), Now: e.clock.Now})
		return err
	}
	active := func(mod func(b *config.BootstrapAdminConfig)) config.BootstrapAdminConfig {
		b := config.BootstrapAdminConfig{Enabled: true, Username: "admin", PasswordHashFile: f.hashFile, TokenFile: f.tokenFile}
		mod(&b)
		return b
	}
	cases := []struct {
		name string
		b    config.BootstrapAdminConfig
		want string
	}{
		{"missing hash file", active(func(b *config.BootstrapAdminConfig) { b.PasswordHashFile = "/does/not/exist/hash" }), "authn: auth.bootstrapAdmin.passwordHashFile: cannot read /does/not/exist/hash: "},
		{"missing token file", active(func(b *config.BootstrapAdminConfig) { b.TokenFile = "/does/not/exist/token" }), "authn: auth.bootstrapAdmin.tokenFile: cannot read /does/not/exist/token: "},
		{"cost 4 hash", active(func(b *config.BootstrapAdminConfig) { b.PasswordHashFile = write("weak", string(weak)) }), "authn: auth.bootstrapAdmin.passwordHashFile: file "},
		{"short token", active(func(b *config.BootstrapAdminConfig) { b.TokenFile = write("short", secretToken) }), "authn: auth.bootstrapAdmin.tokenFile: file "},
		{"user name", active(func(b *config.BootstrapAdminConfig) { b.Username = "a b" }), "authn: auth.bootstrapAdmin.username: "},
	}
	for _, tc := range cases {
		err := try(tc.b, nil)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if strings.Contains(err.Error(), secretToken) || strings.Contains(err.Error(), string(weak)) {
			t.Errorf("%s: the error leaks content: %v", tc.name, err)
		}
	}
	// nothing is read while the bootstrap admin is inactive
	if err := try(active(func(b *config.BootstrapAdminConfig) { b.Enabled = false; b.TokenFile = "/does/not/exist" }), nil); err != nil {
		t.Errorf("disabled: %v", err)
	}
	idp := newIdP(t)
	if err := try(active(func(b *config.BootstrapAdminConfig) { b.TokenFile = "/does/not/exist" }), oidcAuth(idp)); err != nil {
		t.Errorf("replaced by single sign-on: %v", err)
	}
	if err := try(active(func(b *config.BootstrapAdminConfig) {}), nil); err != nil {
		t.Errorf("valid files: %v", err)
	}
}

// ------------------------------------------------- reconciliation ---

func auditCount(e *env, action string) int { return len(e.findAudit(action)) }

func TestEnsureBootstrapAdminLifecycle(t *testing.T) {
	f := newBootstrapFiles(t)
	idp := newIdP(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f), httpCli: idp.srv.Client()})
	ctx := context.Background()
	ids := e.db.Identity()

	// created
	if err := e.svc.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	users := e.bootstrapUsers()
	if len(users) != 1 || users[0].Disabled || users[0].Role != domain.RoleAdmin || users[0].Name != "admin" || users[0].Subject != "bootstrap:admin" {
		t.Fatalf("created user: %+v", users)
	}
	act := e.findAudit("auth.bootstrap.activate")
	if len(act) != 1 || act[0].ActorType != domain.ActorSystem || act[0].TargetType != "user" || act[0].TargetID != users[0].ID.String() ||
		act[0].Details["reason"] != "created" || act[0].Details["username"] != "admin" {
		t.Fatalf("activate audit: %+v", act)
	}
	if creds, _ := json.Marshal(act[0].Details["credentials"]); string(creds) != `["password","token"]` {
		t.Errorf("credentials: %s", creds)
	}
	// an unchanged restart writes nothing
	for i := 0; i < 2; i++ {
		if err := newService(t, e, e.authCfg, nil).EnsureBootstrapAdmin(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.auditActions()); n != 1 {
		t.Fatalf("an unchanged restart must not write audit entries: %d entries", n)
	}

	// two sessions of the account
	for i := 0; i < 2; i++ {
		err := ids.CreateSession(ctx, domain.Session{ID: randHex(t, 8), UserID: users[0].ID, CSRFToken: "x", CreatedAt: e.clock.Now(), LastSeenAt: e.clock.Now(), ExpiresAt: e.clock.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}

	// disabled in the configuration
	off := e.authCfg
	off.BootstrapAdmin.Enabled = false
	offSvc := newService(t, e, off, nil)
	if err := offSvc.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := ids.GetUser(ctx, users[0].ID)
	if !got.Disabled {
		t.Fatal("the account must be disabled")
	}
	deact := e.findAudit("auth.bootstrap.deactivate")
	if len(deact) != 1 || deact[0].Details["reason"] != "disabled_in_config" || deact[0].Details["username"] != "admin" || deact[0].ActorType != domain.ActorSystem || deact[0].TargetID != users[0].ID.String() {
		t.Fatalf("deactivate audit: %+v", deact)
	}
	if got := fmt.Sprint(deact[0].Details["sessionsRemoved"]); got != "2" {
		t.Errorf("sessionsRemoved = %v", got)
	}
	if err := offSvc.EnsureBootstrapAdmin(ctx); err != nil || auditCount(e, "auth.bootstrap.deactivate") != 1 {
		t.Fatalf("a disabled account is not deactivated again: %v", err)
	}

	// enabled again
	if err := newService(t, e, e.authCfg, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids.GetUser(ctx, users[0].ID); got.Disabled {
		t.Fatal("the account must be enabled again")
	}
	act = e.findAudit("auth.bootstrap.activate")
	if len(act) != 2 || act[0].Details["reason"] != "enabled_in_config" {
		t.Fatalf("activate audit: %+v", act)
	}

	// single sign-on replaces it
	if err := newService(t, e, bootstrapAuthWith(e.authCfg, oidcAuth(idp)), nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids.GetUser(ctx, users[0].ID); !got.Disabled {
		t.Fatal("OIDC without keepWithOidc must deactivate the account")
	}
	if deact = e.findAudit("auth.bootstrap.deactivate"); len(deact) != 2 || deact[0].Details["reason"] != "oidc_enabled" {
		t.Fatalf("deactivate audit: %+v", deact)
	}

	// with keepWithOidc it stays (and is enabled again)
	keep := bootstrapAuthWith(e.authCfg, oidcAuth(idp), func(c *config.AuthConfig) { c.BootstrapAdmin.KeepWithOIDC = true })
	if err := newService(t, e, keep, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids.GetUser(ctx, users[0].ID); got.Disabled {
		t.Fatal("keepWithOidc keeps the account")
	}

	// a changed user name replaces the account
	renamed := e.authCfg
	renamed.BootstrapAdmin.Username = "ops"
	if err := newService(t, e, renamed, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	byName := map[string]domain.User{}
	for _, u := range e.bootstrapUsers() {
		byName[u.Name] = u
	}
	if !byName["admin"].Disabled || byName["ops"].Disabled || byName["ops"].Subject != "bootstrap:ops" {
		t.Fatalf("users after the rename: %+v", byName)
	}
	if deact = e.findAudit("auth.bootstrap.deactivate"); deact[0].Details["reason"] != "username_changed" || deact[0].Details["username"] != "admin" {
		t.Fatalf("deactivate audit: %+v", deact[0])
	}
	if act = e.findAudit("auth.bootstrap.activate"); act[0].Details["reason"] != "created" || act[0].Details["username"] != "ops" {
		t.Fatalf("activate audit: %+v", act[0])
	}
}

// bootstrapAuthWith applies mutations to a copy of base.
func bootstrapAuthWith(base config.AuthConfig, mutate ...func(c *config.AuthConfig)) config.AuthConfig {
	c := base
	for _, m := range mutate {
		m(&c)
	}
	return c
}

func TestBootstrapSessionsAndAPITokensStopWhenInactive(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	ctx := context.Background()

	c := e.newClient()
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	r := c.req("POST", "/api/v1/tokens", map[string]any{"name": "automation", "role": "admin"}, true)
	var created dto.CreatedAPIToken
	r.JSON(&created)
	if r.Code != 201 || created.Secret == "" {
		t.Fatalf("api token: %d %s", r.Code, r.Body)
	}
	if p, err := authenticate(e.svc, c, ""); err != nil || p.AuthMethod != "bootstrap" {
		t.Fatalf("session while active: %v %+v", err, p)
	}
	if p, err := authenticate(e.svc, nil, created.Secret); err != nil || p.Role != domain.RoleAdmin {
		t.Fatalf("api token while active: %v %+v", err, p)
	}

	// a replica whose configuration does not activate the account rejects both,
	// without any change in the database
	off := e.authCfg
	off.BootstrapAdmin.Enabled = false
	offSvc := newService(t, e, off, nil)
	if _, err := authenticate(offSvc, c, ""); err == nil {
		t.Fatal("the session of a bootstrap admin must be rejected where it is not active")
	}
	if _, err := authenticate(offSvc, nil, created.Secret); err == nil {
		t.Fatal("the API token of a bootstrap admin must be rejected where it is not active")
	}
	// ... as does a replica with another user name
	other := e.authCfg
	other.BootstrapAdmin.Username = "other"
	otherSvc := newService(t, e, other, nil)
	if _, err := authenticate(otherSvc, c, ""); err == nil {
		t.Fatal("the account of another user name is not the active one")
	}

	// the reconciliation disables the account: it stops everywhere
	if err := offSvc.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute) // cached lookups expire
	if _, err := authenticate(e.svc, c, ""); err == nil {
		t.Fatal("the session of a disabled account")
	}
	if _, err := authenticate(e.svc, nil, created.Secret); err == nil {
		t.Fatal("API tokens stop working with the creator")
	}
}

func TestBootstrapUserIsManagedByConfiguration(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f, func(c *config.AuthConfig) {
		c.DevLogin = config.DevLoginConfig{Enabled: true, Users: devUsers()}
	})})
	if err := e.svc.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	users := e.bootstrapUsers()
	if len(users) != 1 {
		t.Fatal("bootstrap user expected")
	}
	admin := e.newClient()
	admin.mustDevLogin("admin")
	for name, body := range map[string]any{"role": map[string]string{"role": "viewer"}, "disable": map[string]bool{"disabled": true}} {
		r := admin.req("PATCH", "/api/v1/users/"+users[0].ID.String(), body, true)
		if r.Code != 412 || r.Problem().Code != "precondition_failed" || !strings.Contains(detailOf(r), "the bootstrap admin is managed by configuration (auth.bootstrapAdmin)") {
			t.Fatalf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	got, _ := e.db.Identity().GetUser(context.Background(), users[0].ID)
	if got.Role != domain.RoleAdmin || got.Disabled || got.RoleSource != "mapping" {
		t.Fatalf("the user must be unchanged: %+v", got)
	}
	failed := 0
	for _, ev := range e.findAudit("user.update") {
		if ev.Result == "failure" && ev.TargetID == users[0].ID.String() {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("%d failed user.update audit entries, want 2", failed)
	}
	// other users can still be changed
	var list struct {
		Items []domain.User `json:"items"`
	}
	admin.req("GET", "/api/v1/users", nil, false).JSON(&list)
	for _, u := range list.Items {
		if u.Provider == "local" && u.Name == "viewer" {
			if r := admin.req("PATCH", "/api/v1/users/"+u.ID.String(), map[string]string{"role": "operator"}, true); r.Code != 200 {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		}
	}
}

// Nothing a caller or a file provides ends up in an audit entry.
func TestBootstrapAuditNeverContainsCredentials(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	wrongPassword := "wrong-" + randHex(t, 8)
	wrongToken := "wrong-" + randHex(t, 20)

	e.newClient().passwordLogin("admin", wrongPassword)
	e.newClient().tokenLogin(wrongToken)
	e.clock.Advance(time.Minute)
	e.newClient().tokenLogin(f.token)
	e.newClient().tokenLogin(f.token) // token_used
	e.newClient().passwordLogin("admin", f.password)
	e.clock.Advance(time.Minute)
	if err := e.svc.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.findAudit("auth.bootstrap.login")) < 5 {
		t.Fatal("sign-ins are audited")
	}
	all, err := json.Marshal(e.auditActions())
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{"token": f.token, "password": f.password, "hash": f.hash, "hash body": f.hash[7:], "wrong password": wrongPassword, "wrong token": wrongToken} {
		if strings.Contains(string(all), secret) {
			t.Errorf("an audit entry contains the %s", name)
		}
	}
}

func sha256Hex(s string) string { return hex.EncodeToString(domain.SHA256([]byte(s))) }

// Parallel guesses do not pass the failure counter together: only one
// verification runs at a time, the others are delayed as the account.
func TestBootstrapParallelAttemptsAreSerialized(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	const n = 5 // the burst of the address limit
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.newClient().passwordLogin("admin", "wrong-"+strconv.Itoa(i)).Code
		}()
	}
	wg.Wait()
	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	if count[401] != 1 || count[429] != n-1 {
		t.Fatalf("status codes %v: want one verification (401) and %d delayed (429)", codes, n-1)
	}
}

func TestBootstrapAPITokensStayRevokedWhenTheAccountReturns(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})
	ctx := context.Background()
	if err := e.svc.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	c := e.newClient()
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var created dto.CreatedAPIToken
	r := c.req("POST", "/api/v1/tokens", map[string]any{"name": "automation", "role": "admin"}, true)
	r.JSON(&created)
	if r.Code != 201 || created.Secret == "" {
		t.Fatalf("api token: %d %s", r.Code, r.Body)
	}

	off := e.authCfg
	off.BootstrapAdmin.Enabled = false
	if err := newService(t, e, off, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	deact := e.findAudit("auth.bootstrap.deactivate")
	if len(deact) != 1 || fmt.Sprint(deact[0].Details["apiTokensRevoked"]) != "1" {
		t.Fatalf("deactivate audit: %+v", deact)
	}
	// the account comes back: the token of its earlier life does not
	e.clock.Advance(time.Minute)
	if err := newService(t, e, e.authCfg, nil).EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(e.svc, nil, created.Secret); err == nil {
		t.Fatal("an API token created before the deactivation must stay revoked")
	}
	if r := c.passwordLogin("admin", f.password); r.Code != 200 {
		t.Fatalf("the account itself works again: %d %s", r.Code, r.Body)
	}
}

func TestBootstrapWarnsAboutLooseFileModes(t *testing.T) {
	f := newBootstrapFiles(t)
	e := newEnv(t, envOpts{auth: bootstrapAuth(f)})

	var quietLogs syncBuffer
	newService(t, e, e.authCfg, bufferLogger(&quietLogs))
	if strings.Contains(quietLogs.String(), "accessible to other users") {
		t.Fatalf("0600 files must not warn: %s", quietLogs.String())
	}

	if err := os.Chmod(f.tokenFile, 0o644); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	newService(t, e, e.authCfg, bufferLogger(&logs))
	out := logs.String()
	if !strings.Contains(out, "accessible to other users") || !strings.Contains(out, "auth.bootstrapAdmin.tokenFile") {
		t.Fatalf("a world-readable token file must warn: %s", out)
	}
	if strings.Contains(out, f.token) || strings.Contains(out, f.hash) || strings.Contains(out, "passwordHashFile") {
		t.Fatalf("the warning names the setting only: %s", out)
	}
}
