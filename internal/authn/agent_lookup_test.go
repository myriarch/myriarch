// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// countingStore counts the enrollment token lookups and usage updates of a store.
type countingStore struct {
	store.Store
	lookups, touches atomic.Int64
	delay            time.Duration
}

func (c *countingStore) Identity() store.IdentityStore {
	return &countingIdentity{IdentityStore: c.Store.Identity(), c: c}
}

type countingIdentity struct {
	store.IdentityStore
	c *countingStore
}

func (i *countingIdentity) GetEnrollmentTokenByHash(ctx context.Context, h domain.Hash) (*domain.EnrollmentToken, error) {
	i.c.lookups.Add(1)
	if i.c.delay > 0 {
		time.Sleep(i.c.delay)
	}
	return i.IdentityStore.GetEnrollmentTokenByHash(ctx, h)
}

func (i *countingIdentity) TouchEnrollmentToken(ctx context.Context, id uuid.UUID, at time.Time, uses int) error {
	i.c.touches.Add(1)
	return i.IdentityStore.TouchEnrollmentToken(ctx, id, at, uses)
}

func newCountingService(t *testing.T, e *env, delay time.Duration) (*authn.Service, *countingStore) {
	t.Helper()
	cs := &countingStore{Store: e.db, delay: delay}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc, err := authn.New(ctx, e.authCfg, e.serverCfg, e.opampCfg, authn.Deps{
		Store: cs, Audit: audit.NewRecorder(e.db.Audit(), quiet), Registerer: prometheus.NewRegistry(), Logger: quiet,
		Now: e.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, cs
}

func enrollReq(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/v1/opamp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func newEnrollmentToken(t *testing.T, e *env) string {
	t.Helper()
	secret, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	if err := e.db.Identity().CreateEnrollmentToken(context.Background(), domain.EnrollmentToken{
		ID: uuid.New(), Name: "t-" + secret[5:9], Hash: domain.SHA256([]byte(secret)), CreatedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	return secret
}

// Unknown tokens neither evict valid tokens from the cache nor reach the database when malformed. The
// replica lookup bound is kept out of the picture (the fake clock refills its bucket), so that only the
// separate negative cache keeps the valid token cached.
func TestUnknownEnrollmentTokensDoNotEvictValidOnes(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	valid := newEnrollmentToken(t, e)
	if _, err := svc.AuthenticateAgent(enrollReq(valid)); err != nil {
		t.Fatal(err)
	}
	if n := cs.lookups.Load(); n != 1 {
		t.Fatalf("lookups after the first authentication: %d, want 1", n)
	}
	// More unknown tokens than the shared cache holds, within the validity of the cached token (60 s)
	// and at a pace the lookup bound (100 per second) allows.
	const flood = 5000
	for i := 0; i < flood; i++ {
		e.clock.Advance(10 * time.Millisecond)
		unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
		if _, err := svc.AuthenticateAgent(enrollReq(unknown)); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("unknown token %d: %v", i, err)
		}
	}
	if n := cs.lookups.Load(); n != 1+flood {
		t.Fatalf("%d lookups, want %d: the lookup bound interfered with the test", n, 1+flood)
	}
	before := cs.lookups.Load()
	if _, err := svc.AuthenticateAgent(enrollReq(valid)); err != nil {
		t.Fatalf("valid token after the flood: %v", err)
	}
	if n := cs.lookups.Load(); n != before {
		t.Fatalf("the valid token caused %d more lookups, want none", n-before)
	}
}

func TestMalformedEnrollmentTokensCauseNoLookup(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	for name, tok := range map[string]string{
		"short":      "myre_abc",
		"bad chars":  "myre_" + strings.Repeat("a", 31) + "!",
		"overlong":   "myre_" + strings.Repeat("a", 121),
		"whitespace": "myre_" + strings.Repeat("a", 20) + " " + strings.Repeat("b", 20),
	} {
		if _, err := svc.AuthenticateAgent(enrollReq(tok)); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := cs.lookups.Load(); n != 0 {
		t.Fatalf("%d lookups for malformed tokens", n)
	}
}

func TestConcurrentLookupsOfOneTokenAreCoalesced(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 100*time.Millisecond)
	unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.AuthenticateAgent(enrollReq(unknown))
		}()
	}
	wg.Wait()
	if n := cs.lookups.Load(); n != 1 {
		t.Fatalf("%d lookups for one token, want 1", n)
	}
}

// Cache-miss lookups are bounded per replica; the refusal needs no store call and maps to 429.
func TestEnrollmentLookupsAreBoundedPerReplica(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	refused := 0
	for i := 0; i < 1000; i++ {
		unknown, _ := authn.NewToken(domain.EnrollmentTokenPrefix)
		before := cs.lookups.Load()
		_, err := svc.AuthenticateAgent(enrollReq(unknown))
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
	// after the bucket refilled the lookups work again
	e.clock.Advance(10 * time.Second)
	valid := newEnrollmentToken(t, e)
	if _, err := svc.AuthenticateAgent(enrollReq(valid)); err != nil {
		t.Fatalf("valid token after the bucket refilled: %v", err)
	}
}

// The usage of a token is written with one statement per token and flush.
func TestEnrollmentUsageIsFlushedWithOneStatementPerToken(t *testing.T) {
	e := newEnv(t, envOpts{})
	svc, cs := newCountingService(t, e, 0)
	valid := newEnrollmentToken(t, e)
	for i := 0; i < 1000; i++ {
		if _, err := svc.AuthenticateAgent(enrollReq(valid)); err != nil {
			t.Fatal(err)
		}
	}
	svc.FlushEnrollmentUsage(context.Background())
	if n := cs.touches.Load(); n != 1 {
		t.Fatalf("%d usage statements, want 1", n)
	}
	tok, err := e.db.Identity().GetEnrollmentTokenByHash(context.Background(), domain.SHA256([]byte(valid)))
	if err != nil || tok.UseCount != 1000 {
		t.Fatalf("use count: %+v %v", tok, err)
	}
}
