// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// The routes of the installation from the server (plan, installer bundle) authenticate with an enrollment
// token like the OpAMP endpoint does, and always: also where the OpAMP endpoint accepts hosts without one.

func installReq(tok, remote string) *http.Request {
	r := enrollReq(tok)
	r.RemoteAddr = remote + ":40000"
	return r
}

// enrollmentTokenWith creates an enrollment token and returns its secret and its ID.
func enrollmentTokenWith(t *testing.T, e *env, mut func(tok *domain.EnrollmentToken)) (string, uuid.UUID) {
	t.Helper()
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	tok := domain.EnrollmentToken{ID: uuid.New(), Name: "t-" + secret[5:9], Hash: domain.SHA256([]byte(secret)), CreatedBy: "t", CreatedAt: e.clock.Now()}
	if mut != nil {
		mut(&tok)
	}
	if err := e.db.Identity().CreateEnrollmentToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	return secret, tok.ID
}

func TestAuthenticateInstallNeedsATokenEvenWhenOpAMPDoesNot(t *testing.T) {
	e := newEnv(t, envOpts{opamp: func(c *config.OpAMPConfig) { c.RequireEnrollmentToken = false }})
	noToken := httpRequestWithoutToken()
	if _, err := e.svc.AuthenticateAgent(noToken); err != nil {
		t.Fatalf("the OpAMP endpoint accepts a host without a token in this setup: %v", err)
	}
	if _, err := e.svc.AuthenticateInstall(noToken); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("the install routes accepted a request without a token: %v", err)
	}
	// a token that is presented must be valid, as on the OpAMP endpoint
	unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	if _, err := e.svc.AuthenticateInstall(installReq(unknown, "192.0.2.10")); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("unknown token: %v", err)
	}
	// other credentials do not do: an API token, a token of another form, another scheme
	for name, h := range map[string]string{
		"api token":     "Bearer myra_" + strings.Repeat("a", 32),
		"download":      "Bearer myrd_" + strings.Repeat("a", 32),
		"basic":         "Basic " + strings.Repeat("a", 20),
		"bearer only":   "Bearer ",
		"empty header":  " ",
		"two tokens":    "Bearer myre_" + strings.Repeat("a", 32) + " myre_" + strings.Repeat("b", 32),
		"lower scheme":  "bearer myre_" + strings.Repeat("a", 31) + "!",
		"overlong":      "Bearer myre_" + strings.Repeat("a", 200),
		"token as user": "Bearer " + strings.Repeat("a", 50),
	} {
		r := noToken.Clone(context.Background())
		r.Header.Set("Authorization", h)
		if _, err := e.svc.AuthenticateInstall(r); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func httpRequestWithoutToken() *http.Request {
	r := enrollReq("x")
	r.Header.Del("Authorization")
	r.RemoteAddr = "192.0.2.9:40000"
	return r
}

func TestAuthenticateInstallReturnsTheTokenIDAndLabels(t *testing.T) {
	e := newEnv(t, envOpts{})
	secret, id := enrollmentTokenWith(t, e, func(tok *domain.EnrollmentToken) { tok.Labels = map[string]string{"channel": "edge-eu"} })
	cred, err := e.svc.AuthenticateInstall(installReq(secret, "192.0.2.10"))
	if err != nil {
		t.Fatal(err)
	}
	if cred.EnrollmentTokenID == nil || *cred.EnrollmentTokenID != id || cred.Labels["channel"] != "edge-eu" {
		t.Fatalf("credential: %+v", cred)
	}
	// a copy: the caller may change the labels
	cred.Labels["channel"] = "changed"
	cred2, err := e.svc.AuthenticateInstall(installReq(secret, "192.0.2.10"))
	if err != nil || cred2.Labels["channel"] != "edge-eu" {
		t.Fatalf("labels are shared with the cache: %+v %v", cred2, err)
	}
}

// A request cannot tell whether a token exists: unknown, revoked and expired tokens fail with the same error.
func TestAuthenticateInstallDoesNotRevealWhetherATokenExists(t *testing.T) {
	e := newEnv(t, envOpts{})
	revoked, revokedID := enrollmentTokenWith(t, e, nil)
	past := e.clock.Now().Add(-time.Hour)
	expired, _ := enrollmentTokenWith(t, e, func(tok *domain.EnrollmentToken) { tok.ExpiresAt = &past })
	if err := e.db.Identity().RevokeEnrollmentToken(context.Background(), revokedID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	var msgs []string
	for name, tok := range map[string]string{"unknown": unknown, "revoked": revoked, "expired": expired} {
		_, err := e.svc.AuthenticateInstall(installReq(tok, "192.0.2.11"))
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(err.Error(), tok) {
			t.Errorf("%s: the error repeats the token", name)
		}
		msgs = append(msgs, err.Error())
	}
	if msgs[0] != msgs[1] || msgs[1] != msgs[2] {
		t.Errorf("the errors differ: %q", msgs)
	}
}

func TestAuthenticateInstallKeepsTheClientCertificateRule(t *testing.T) {
	e := newEnv(t, envOpts{opamp: func(c *config.OpAMPConfig) { c.RequireClientCert = true }})
	secret, _ := enrollmentTokenWith(t, e, nil)
	r := installReq(secret, "192.0.2.12")
	if _, err := e.svc.AuthenticateInstall(r); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("a valid token without the required client certificate: %v", err)
	}
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: "host-1.example.com"}}}}}
	cred, err := e.svc.AuthenticateInstall(r)
	if err != nil {
		t.Fatalf("token and client certificate: %v", err)
	}
	if cred.ClientCertSubject != "host-1.example.com" {
		t.Errorf("certificate subject: %q", cred.ClientCertSubject)
	}
}

// Failed authentications are charged to the client address; an address that used up its budget is refused
// before a lookup, even with a valid token, until the budget is refilled. Other addresses are not affected.
func TestAuthenticateInstallLimitsFailuresPerClientAddress(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	valid, _ := enrollmentTokenWith(t, e, nil)
	if _, err := svc.AuthenticateInstall(installReq(valid, "192.0.2.20")); err != nil {
		t.Fatal(err)
	}
	// 30 failures are free, then the address is refused. The header-less requests cost no lookup.
	for i := 0; i < 30; i++ {
		r := httpRequestWithoutToken()
		r.RemoteAddr = "198.51.100.7:1"
		if _, err := svc.AuthenticateInstall(r); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("failure %d: %v", i, err)
		}
	}
	before := cs.lookups.Load()
	_, err := svc.AuthenticateInstall(installReq(valid, "198.51.100.7"))
	var rl *domain.RateLimitError
	if !errors.Is(err, domain.ErrTooManyRequests) || !errors.As(err, &rl) || rl.After < time.Second {
		t.Fatalf("an address without budget: %v", err)
	}
	if cs.lookups.Load() != before {
		t.Error("a refused request reached the store")
	}
	// another address is not affected, nor is another IPv6 prefix; the same /64 shares the budget
	if _, err := svc.AuthenticateInstall(installReq(valid, "198.51.100.8")); err != nil {
		t.Errorf("another IPv4 address: %v", err)
	}
	for i := 0; i < 30; i++ {
		r := httpRequestWithoutToken()
		r.RemoteAddr = "[2001:db8:1:2::1]:1"
		_, _ = svc.AuthenticateInstall(r)
	}
	r := installReq(valid, "")
	r.RemoteAddr = "[2001:db8:1:2:ffff::9]:40000"
	if _, err := svc.AuthenticateInstall(r); !errors.Is(err, domain.ErrTooManyRequests) {
		t.Errorf("the same /64: %v", err)
	}
	r.RemoteAddr = "[2001:db8:1:3::9]:40000"
	if _, err := svc.AuthenticateInstall(r); err != nil {
		t.Errorf("another /64: %v", err)
	}
	// the budget refills with time (one failure per second)
	e.clock.Advance(3 * time.Second)
	if _, err := svc.AuthenticateInstall(installReq(valid, "198.51.100.7")); err != nil {
		t.Errorf("after the budget refilled: %v", err)
	}
	// successes are free: many valid requests from one address never use the budget up
	for i := 0; i < 200; i++ {
		if _, err := svc.AuthenticateInstall(installReq(valid, "192.0.2.21")); err != nil {
			t.Fatalf("valid request %d: %v", i, err)
		}
	}
}

// A flood of unknown tokens from one address is stopped by the address budget long before the lookups of the
// replica are used up, so it cannot keep the agents from enrolling.
func TestInstallFloodFromOneAddressDoesNotUseUpTheReplicaLookups(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	for i := 0; i < 1000; i++ {
		unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
		_, _ = svc.AuthenticateInstall(installReq(unknown, "203.0.113.5"))
	}
	if n := cs.lookups.Load(); n > 40 {
		t.Fatalf("%d lookups from one address, want at most the budget of about 30", n)
	}
	valid, _ := enrollmentTokenWith(t, e, nil)
	if _, err := svc.AuthenticateAgent(enrollReq(valid)); err != nil {
		t.Fatalf("an agent that enrolls after the flood: %v", err)
	}
}

// Many addresses can still use up the lookups of the replica: that bound (429) holds on the install routes as
// on the OpAMP endpoint, and a refused lookup costs no store call.
func TestInstallLookupsAreBoundedPerReplica(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	refused := 0
	for i := 0; i < 1000; i++ {
		unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
		before := cs.lookups.Load()
		_, err := svc.AuthenticateInstall(installReq(unknown, fmt.Sprintf("10.%d.%d.1", i/250, i%250)))
		if errors.Is(err, domain.ErrTooManyRequests) {
			refused++
			if cs.lookups.Load() != before {
				t.Fatal("a refused lookup reached the store")
			}
		}
	}
	if refused == 0 || cs.lookups.Load() >= 1000 {
		t.Fatalf("lookups = %d, refused = %d: the replica bound did not apply", cs.lookups.Load(), refused)
	}
}

// A database failure is an availability problem (503), not an unauthorized request, and it is not charged to
// the client address.
func TestAuthenticateInstallDatabaseFailureIsUnavailable(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	failing := &failingIdentityStore{Store: e.db}
	svc, err := authn.New(ctx, e.authCfg, e.serverCfg, e.opampCfg, authn.Deps{Store: failing, Logger: quiet, Now: e.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	for i := 0; i < 50; i++ {
		_, err := svc.AuthenticateInstall(installReq(secret, "192.0.2.30"))
		if !errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("the error repeats the token")
		}
	}
}

type failingIdentityStore struct{ store.Store }

func (f *failingIdentityStore) Identity() store.IdentityStore {
	return &failingIdentity{IdentityStore: f.Store.Identity()}
}

type failingIdentity struct{ store.IdentityStore }

func (f *failingIdentity) GetEnrollmentTokenByHash(context.Context, domain.Hash) (*domain.EnrollmentToken, error) {
	return nil, errors.New("connection refused (password=hunter2)")
}

// The install routes are no OpAMP connections: their results stay out of the counter that the alert on unauthorized
// connects reads.
func TestAuthenticateInstallIsNotCountedAsAnOpAMPConnection(t *testing.T) {
	e := newEnv(t, envOpts{})
	secret, _ := enrollmentTokenWith(t, e, nil)
	unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	_, _ = e.svc.AuthenticateInstall(installReq(secret, "192.0.2.40"))
	_, _ = e.svc.AuthenticateInstall(installReq(unknown, "192.0.2.40"))
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "myriarch_auth_agent_requests_total" {
			for _, m := range mf.GetMetric() {
				if m.GetCounter().GetValue() != 0 {
					t.Errorf("the install routes were counted: %v", m)
				}
			}
		}
	}
	if _, err := e.svc.AuthenticateAgent(enrollReq(secret)); err != nil {
		t.Fatal(err)
	}
}
