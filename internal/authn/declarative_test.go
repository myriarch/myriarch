// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// declared writes a generated enrollment token into a file and returns the
// entry that refers to it and the token.
func declared(t *testing.T, name string, labels map[string]string, expires string) (config.EnrollmentTokenConfig, string) {
	t.Helper()
	tok, err := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.EnrollmentTokenConfig{Name: name, TokenFile: p, Labels: labels, ExpiresAt: expires}, tok
}

func (e *env) tokens() []domain.EnrollmentToken {
	e.t.Helper()
	ts, err := e.db.Identity().ListEnrollmentTokens(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return ts
}

func (e *env) agentAuth(tok string) (*authn.AgentCredential, error) {
	r := httptest.NewRequest("GET", "/v1/opamp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return e.svc.AuthenticateAgent(r)
}

// noDevSeedToken is the value for EnsureEnrollmentTokens when no dev.seedEnrollmentToken is configured.
const noDevSeedToken = ""

func TestDeclarativeTokenIsCreatedWithHashAndPrefixOnly(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	expires := e.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	entry, tok := declared(t, "edge-nl", map[string]string{"channel": "edge", "site": "nl"}, expires.Format(time.RFC3339))

	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	ts := e.tokens()
	if len(ts) != 1 {
		t.Fatalf("%d tokens", len(ts))
	}
	got := ts[0]
	if got.CreatedBy != "config" || got.Name != "edge-nl" || got.DisplayPrefix != tok[:12] || len(got.DisplayPrefix) != 12 ||
		string(got.Hash) != string(domain.SHA256([]byte(tok))) || got.RevokedAt != nil || got.Labels["channel"] != "edge" || got.Labels["site"] != "nl" ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("token: %+v", got)
	}
	ev := e.findAudit("enrollment_token.create")
	if len(ev) != 1 || ev[0].ActorType != domain.ActorSystem || ev[0].TargetType != "enrollment_token" || ev[0].TargetID != got.ID.String() ||
		ev[0].Details["source"] != "config" || ev[0].Details["name"] != "edge-nl" || ev[0].Details["prefix"] != tok[:12] || ev[0].Details["expiresAt"] != expires.Format(time.RFC3339) {
		t.Fatalf("audit: %+v", ev)
	}
	// nothing stores or reports the plaintext
	everything, _ := json.Marshal([]any{ts, e.auditActions()})
	if strings.Contains(string(everything), tok) || strings.Contains(string(everything), tok[12:]) {
		t.Fatal("the token value must not appear in the database rows or the audit log")
	}
	// and agents authenticate with it like with a token from the UI
	cred, err := e.agentAuth(tok)
	if err != nil || cred.EnrollmentTokenID == nil || *cred.EnrollmentTokenID != got.ID || cred.Labels["site"] != "nl" {
		t.Fatalf("agent authentication: %v %+v", err, cred)
	}
}

func TestDeclarativeTokensAreIdempotent(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	a, _ := declared(t, "alpha", map[string]string{"a": "1"}, "")
	b, _ := declared(t, "beta", nil, "2099-01-01T00:00:00Z")
	entries := []config.EnrollmentTokenConfig{a, b}
	if err := e.svc.EnsureEnrollmentTokens(ctx, entries, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	rows, audits := len(e.tokens()), len(e.auditActions())
	if rows != 2 || audits != 2 {
		t.Fatalf("%d rows, %d audit entries", rows, audits)
	}
	for _, svc := range []*authn.Service{e.svc, newService(t, e, e.authCfg, nil)} {
		if err := svc.EnsureEnrollmentTokens(ctx, entries, noDevSeedToken); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.tokens()) != rows || len(e.auditActions()) != audits {
		t.Fatalf("a repeated start changed something: %d rows, %d audit entries", len(e.tokens()), len(e.auditActions()))
	}
}

func TestDeclarativeTokenUpdates(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	entry, tok := declared(t, "edge", map[string]string{"site": "a"}, "")
	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	// warm the agent cache with the old labels
	if cred, err := e.agentAuth(tok); err != nil || cred.Labels["site"] != "a" {
		t.Fatalf("%v %+v", err, cred)
	}
	id := e.tokens()[0].ID

	expires := e.clock.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	steps := []struct {
		name    string
		mutate  func(c *config.EnrollmentTokenConfig)
		changed []string
	}{
		{"name", func(c *config.EnrollmentTokenConfig) { c.Name = "edge-renamed" }, []string{"name"}},
		{"labels", func(c *config.EnrollmentTokenConfig) { c.Labels = map[string]string{"site": "b", "tier": "1"} }, []string{"labels"}},
		{"expiry set", func(c *config.EnrollmentTokenConfig) { c.ExpiresAt = expires.Format(time.RFC3339) }, []string{"expiresAt"}},
		{"expiry moved", func(c *config.EnrollmentTokenConfig) { c.ExpiresAt = expires.Add(time.Hour).Format(time.RFC3339) }, []string{"expiresAt"}},
		{"expiry in another zone but the same instant", func(c *config.EnrollmentTokenConfig) {
			c.ExpiresAt = expires.Add(time.Hour).In(time.FixedZone("x", 2*3600)).Format(time.RFC3339)
		}, nil},
		{"all", func(c *config.EnrollmentTokenConfig) {
			c.Name, c.Labels, c.ExpiresAt = "edge-final", nil, ""
		}, []string{"name", "labels", "expiresAt"}},
	}
	for _, st := range steps {
		before := len(e.findAudit("enrollment_token.update"))
		st.mutate(&entry)
		if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		ups := e.findAudit("enrollment_token.update")
		if st.changed == nil {
			if len(ups) != before {
				t.Errorf("%s: no change expected", st.name)
			}
			continue
		}
		if len(ups) != before+1 {
			t.Fatalf("%s: %d update entries, want %d", st.name, len(ups), before+1)
		}
		ev := ups[0]
		changed, _ := json.Marshal(ev.Details["changed"])
		want, _ := json.Marshal(st.changed)
		if string(changed) != string(want) || ev.Details["source"] != "config" || ev.TargetID != id.String() || ev.ActorType != domain.ActorSystem {
			t.Errorf("%s: audit %+v (changed %s, want %s)", st.name, ev, changed, want)
		}
	}
	if len(e.tokens()) != 1 {
		t.Fatal("updates keep one row")
	}
	got := e.tokens()[0]
	if got.ID != id || got.Name != "edge-final" || len(got.Labels) != 0 || got.ExpiresAt != nil || got.CreatedBy != "config" || got.DisplayPrefix != tok[:12] {
		t.Fatalf("final state: %+v", got)
	}
	// the local cache was purged: the labels of the second step apply at once
	entry.Labels = map[string]string{"site": "c"}
	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	if cred, err := e.agentAuth(tok); err != nil || cred.Labels["site"] != "c" {
		t.Fatalf("labels after an update: %v %+v", err, cred)
	}
}

func TestDeclarativeTokenExpiryIsEnforced(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	entry, tok := declared(t, "short-lived", nil, e.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	id := e.tokens()[0].ID
	if _, err := e.agentAuth(tok); err != nil {
		t.Fatalf("before the expiry: %v", err)
	}
	if err := e.svc.EnrollmentTokenState(ctx, id); err != nil {
		t.Fatalf("state before the expiry: %v", err)
	}
	e.clock.Advance(2 * time.Hour)
	if _, err := e.agentAuth(tok); err == nil {
		t.Fatal("after the expiry")
	}
	if err := e.svc.EnrollmentTokenState(ctx, id); err == nil {
		t.Fatal("state after the expiry")
	}
	// a restart with the same, now expired, entry changes nothing and does not fail
	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	if len(e.findAudit("enrollment_token.update")) != 0 {
		t.Fatal("no update")
	}
}

func TestDeclarativeTokenThatWasRevokedStaysRevoked(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	var logs syncBuffer
	svc := newService(t, e, e.authCfg, bufferLogger(&logs))
	entry, tok := declared(t, "revoked", map[string]string{"x": "1"}, "")
	if err := svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	id := e.tokens()[0].ID
	if err := e.db.Identity().RevokeEnrollmentToken(ctx, id, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	entry.Labels = map[string]string{"x": "2"} // even a changed entry does not touch it
	if err := svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	ts := e.tokens()
	if len(ts) != 1 || ts[0].RevokedAt == nil || ts[0].Labels["x"] != "1" {
		t.Fatalf("a revoked token is left alone: %+v", ts)
	}
	if _, err := e.agentAuth(tok); err == nil {
		t.Fatal("the revoked token must not authenticate")
	}
	if !strings.Contains(logs.String(), "declarative enrollment token is revoked; rotate its value to issue a new one") || !strings.Contains(logs.String(), id.String()) {
		t.Fatalf("a warning with name and id is expected: %s", logs.String())
	}
	// a new value in the same file issues a new token
	next, err := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.TokenFile, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	if len(e.tokens()) != 2 {
		t.Fatalf("rotation: %d tokens", len(e.tokens()))
	}
	if _, err := e.agentAuth(next); err != nil {
		t.Fatalf("the rotated token: %v", err)
	}
}

func TestDeclarativeTokenWithTheValueOfAUITokenIsLeftAlone(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	var logs syncBuffer
	svc := newService(t, e, e.authCfg, bufferLogger(&logs))
	entry, tok := declared(t, "declared", map[string]string{"from": "config"}, "")
	ui := domain.EnrollmentToken{ID: uuid.New(), Name: "created in the UI", DisplayPrefix: tok[:12], Hash: domain.SHA256([]byte(tok)),
		Labels: map[string]string{"from": "ui"}, CreatedBy: "user:alice", CreatedAt: e.clock.Now()}
	if err := e.db.Identity().CreateEnrollmentToken(ctx, ui); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	ts := e.tokens()
	if len(ts) != 1 || ts[0].Name != "created in the UI" || ts[0].Labels["from"] != "ui" || ts[0].CreatedBy != "user:alice" {
		t.Fatalf("the UI token must stay as it is: %+v", ts)
	}
	if len(e.auditActions()) != 0 {
		t.Fatalf("no audit entry: %+v", e.auditActions())
	}
	if !strings.Contains(logs.String(), "left unchanged") || !strings.Contains(logs.String(), ui.ID.String()) {
		t.Fatalf("a warning is expected: %s", logs.String())
	}
}

func TestDeclarativeTokenThatIsNoLongerConfiguredStaysValid(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	var logs syncBuffer
	svc := newService(t, e, e.authCfg, bufferLogger(&logs))
	keep, _ := declared(t, "kept", nil, "")
	gone, goneTok := declared(t, "gone", nil, "")
	if err := svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{keep, gone}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	logs.mu.Lock()
	logs.b.Reset()
	logs.mu.Unlock()

	if err := svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{keep}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if strings.Count(out, "declarative enrollment token is no longer configured but still valid; revoke it in the UI or with DELETE /api/v1/enrollment-tokens/{id}") != 1 ||
		!strings.Contains(out, "name=gone") || strings.Contains(out, "name=kept") {
		t.Fatalf("exactly the removed token is reported once: %s", out)
	}
	for _, tk := range e.tokens() {
		if tk.RevokedAt != nil {
			t.Fatalf("nothing is revoked automatically: %+v", tk)
		}
	}
	if _, err := e.agentAuth(goneTok); err != nil {
		t.Fatalf("the removed token keeps working until it is revoked: %v", err)
	}
	if len(e.findAudit("enrollment_token.revoke")) != 0 {
		t.Fatal("no revocation")
	}
	// an empty list reports it as well
	logs.mu.Lock()
	logs.b.Reset()
	logs.mu.Unlock()
	if err := svc.EnsureEnrollmentTokens(ctx, nil, noDevSeedToken); err != nil || strings.Count(logs.String(), "no longer configured") != 2 {
		t.Fatalf("%v: %s", err, logs.String())
	}
}

func TestDeclarativeTokenProblemsAbortBeforeAnyChange(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	good, _ := declared(t, "good", nil, "")

	short := filepath.Join(t.TempDir(), "short")
	secret := "myre_short" + strings.Repeat("s", 3)
	if err := os.WriteFile(short, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	dev := filepath.Join(t.TempDir(), "dev")
	if err := os.WriteFile(dev, []byte(config.Default().Dev.SeedEnrollmentToken), 0o600); err != nil {
		t.Fatal(err)
	}
	dup, _ := declared(t, "dup", nil, "")
	dup2 := dup
	dup2.Name = "dup2"

	for name, tc := range map[string]struct {
		bad  config.EnrollmentTokenConfig
		want string
	}{
		"missing file":      {config.EnrollmentTokenConfig{Name: "bad", TokenFile: "/does/not/exist/token"}, "opamp.enrollmentTokens[1].tokenFile: cannot read /does/not/exist/token: "},
		"malformed token":   {config.EnrollmentTokenConfig{Name: "bad", TokenFile: short}, "opamp.enrollmentTokens[1].tokenFile: file " + short + " must contain an enrollment token"},
		"development token": {config.EnrollmentTokenConfig{Name: "bad", TokenFile: dev}, "opamp.enrollmentTokens[1].tokenFile: file " + dev + " contains the well-known development enrollment token"},
		"no file":           {config.EnrollmentTokenConfig{Name: "bad"}, "opamp.enrollmentTokens[1].tokenFile: is required"},
		"bad name":          {config.EnrollmentTokenConfig{Name: "Bad Name", TokenFile: good.TokenFile}, "opamp.enrollmentTokens[1].name: must be a DNS label"},
		"bad expiry":        {config.EnrollmentTokenConfig{Name: "bad", TokenFile: good.TokenFile, ExpiresAt: "tomorrow"}, "opamp.enrollmentTokens[1].expiresAt: must be an RFC 3339 timestamp"},
		"bad labels":        {config.EnrollmentTokenConfig{Name: "bad", TokenFile: good.TokenFile, Labels: map[string]string{"a b": "c"}}, "opamp.enrollmentTokens[1].labels: "},
	} {
		err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{good, tc.bad}, noDevSeedToken)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error leaks the file content", name)
		}
		if n := len(e.tokens()); n != 0 {
			t.Fatalf("%s: %d tokens exist, the valid entry must not be created before the problem is found", name, n)
		}
	}
	err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{dup, dup2}, noDevSeedToken)
	if err == nil || !strings.Contains(err.Error(), "opamp.enrollmentTokens[1].tokenFile: file "+dup2.TokenFile+" contains the same token as opamp.enrollmentTokens[0]") {
		t.Errorf("duplicate value: %v", err)
	}
	if len(e.tokens()) != 0 || len(e.auditActions()) != 0 {
		t.Fatal("nothing may change")
	}
}

func TestDeclarativeTokensFromTwoReplicasEndWithOneRow(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	entry, _ := declared(t, "shared", map[string]string{"a": "b"}, "")
	const replicas = 6
	svcs := make([]*authn.Service, replicas)
	for i := range svcs {
		svcs[i] = newService(t, e, e.authCfg, nil)
	}
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, svc := range svcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
	}
	if n := len(e.tokens()); n != 1 {
		t.Fatalf("%d rows", n)
	}
	if n := len(e.findAudit("enrollment_token.create")); n != 1 {
		t.Fatalf("%d create entries, want exactly the winner's", n)
	}
	if n := len(e.findAudit("enrollment_token.update")); n != 0 {
		t.Fatalf("%d update entries", n)
	}
}

func TestDeclarativeTokensAreListedWithTheirOrigin(t *testing.T) {
	e := newEnv(t, envOpts{})
	entry, tok := declared(t, "listed", nil, "")
	if err := e.svc.EnsureEnrollmentTokens(context.Background(), []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	c := e.newClient()
	c.mustDevLogin("admin")
	r := c.req("GET", "/api/v1/enrollment-tokens", nil, false)
	if r.Code != 200 || !strings.Contains(string(r.Body), `"createdBy":"config"`) || !strings.Contains(string(r.Body), `"name":"listed"`) {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if strings.Contains(string(r.Body), tok) {
		t.Fatal("the list never contains the token")
	}
	// revoking works like for UI tokens
	var list struct {
		Items []domain.EnrollmentToken `json:"items"`
	}
	r.JSON(&list)
	if len(list.Items) != 1 {
		t.Fatalf("%+v", list)
	}
	if r := c.req("DELETE", "/api/v1/enrollment-tokens/"+list.Items[0].ID.String(), nil, true); r.Code != 204 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body)
	}
	if _, err := e.agentAuth(tok); err == nil {
		t.Fatal("revoked")
	}
}

func TestDeclarativeTokenRefusesTheConfiguredDevSeedToken(t *testing.T) {
	e := newEnv(t, envOpts{})
	custom, err := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "seed")
	if err := os.WriteFile(p, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := config.EnrollmentTokenConfig{Name: "seeded", TokenFile: p}
	err = e.svc.EnsureEnrollmentTokens(context.Background(), []config.EnrollmentTokenConfig{entry}, custom)
	if err == nil || !strings.Contains(err.Error(), "contains the well-known development enrollment token") {
		t.Fatalf("custom dev seed token: %v", err)
	}
	if len(e.tokens()) != 0 {
		t.Fatal("nothing may change")
	}
}

// Another replica drops its cached row when a declarative token changes.
func TestDeclarativeTokenUpdateReachesOtherReplicas(t *testing.T) {
	bus := memBus()
	e := newEnv(t, envOpts{bus: bus})
	other := newService(t, e, e.authCfg, nil)
	ctx := context.Background()
	entry, tok := declared(t, "edge", map[string]string{"site": "a"}, "")
	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	auth := func(svc *authn.Service) (*authn.AgentCredential, error) {
		r := httptest.NewRequest("GET", "/v1/opamp", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return svc.AuthenticateAgent(r)
	}
	if cred, err := auth(other); err != nil || cred.Labels["site"] != "a" {
		t.Fatalf("before: %v %+v", err, cred)
	}
	entry.Labels = map[string]string{"site": "b"}
	if err := e.svc.EnsureEnrollmentTokens(ctx, []config.EnrollmentTokenConfig{entry}, noDevSeedToken); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cred, err := auth(other)
		if err == nil && cred.Labels["site"] == "b" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the other replica kept the old labels: %v %+v", err, cred)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
