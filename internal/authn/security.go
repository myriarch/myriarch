// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	lru "github.com/myriarch/myriarch/internal/lru"
)

// Brute-force protection parameters.
const (
	// dev login: 5 attempts per minute and key (client IP, user name), burst 10.
	loginRatePerMinute = 5
	loginBurst         = 10
	// bearer token lookups: this many consecutive failures per client IP are
	// free, then the IP is delayed exponentially (1s, 2s, ... up to 5 minutes).
	tokenFailGrace      = 5
	tokenFailMaxBackoff = 5 * time.Minute
	tokenFailForget     = 10 * time.Minute
	// failures are counted per distinct token: one stale token that a client
	// keeps presenting does not count again. This many distinct tokens are
	// remembered per client address.
	tokenFailDistinct = 32
	// a token that authenticated successfully is exempt from the address delay
	// for this long (it is still checked against the database).
	tokenGoodTTL = 15 * time.Minute
	// bootstrap admin sign-in: 5 attempts per minute and client address, burst 5.
	bootstrapRatePerMinute = 5
	bootstrapBurst         = 5
	// bootstrap admin account backoff: this many consecutive failures are
	// free, then sign-in is refused for 1s, doubling up to 15 minutes; the
	// failures are forgotten after 15 minutes without a new one.
	bootstrapFailGrace  = 5
	bootstrapMaxBackoff = 15 * time.Minute
	bootstrapFailForget = 15 * time.Minute
	// OIDC endpoints: failed callbacks per client address (/64 for IPv6) refill one token per oidcFailEvery,
	// burst oidcFailBurst; with an empty bucket both endpoints refuse the address.
	oidcFailEvery = 6 * time.Second
	oidcFailBurst = 10
	// install routes (plan, installer bundle): failed authentications per client address (/64 for IPv6) refill one token per
	// installFailEvery, burst installFailBurst; with an empty bucket the address is refused before any token lookup.
	installFailEvery = time.Second
	installFailBurst = 30
	// rate-limit audit entries are written at most once per key and minute.
	limitAuditInterval = time.Minute
	// DefaultSessionMaxAge applies when auth.sessionMaxAge is not set.
	DefaultSessionMaxAge = 12 * time.Hour
)

// keyedLimiter is a token bucket per key (bounded LRU).
type keyedLimiter struct {
	mu     sync.Mutex
	cache  *lru.Cache[string, *limiterEntry]
	limit  rate.Limit
	burst  int
	nowFn  func() time.Time
	audits time.Duration
}

type limiterEntry struct {
	lim       *rate.Limiter
	auditedAt time.Time
}

func newKeyedLimiter(limit rate.Limit, burst int, now func() time.Time) *keyedLimiter {
	c, _ := lru.New[string, *limiterEntry](4096)
	return &keyedLimiter{cache: c, limit: limit, burst: burst, nowFn: now, audits: limitAuditInterval}
}

// allow takes one token for key. When the bucket is empty it returns the time
// until the next token and whether this denial should be audited (first one
// per audit interval).
func (k *keyedLimiter) allow(key string) (ok bool, retry time.Duration, audit bool) {
	now := k.nowFn()
	k.mu.Lock()
	defer k.mu.Unlock()
	e, found := k.cache.Get(key)
	if !found {
		e = &limiterEntry{lim: rate.NewLimiter(k.limit, k.burst)}
		k.cache.Add(key, e)
	}
	r := e.lim.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		if now.Sub(e.auditedAt) >= k.audits {
			e.auditedAt = now
			audit = true
		}
		return false, d, audit
	}
	return true, 0, false
}

// blocked reports whether the bucket of key is empty (nothing is consumed) and when a token is available again.
func (k *keyedLimiter) blocked(key string) (bool, time.Duration) {
	now := k.nowFn()
	k.mu.Lock()
	defer k.mu.Unlock()
	e, found := k.cache.Get(key)
	if !found {
		return false, 0
	}
	tokens := e.lim.TokensAt(now)
	if tokens >= 1 {
		return false, 0
	}
	return true, time.Duration((1 - tokens) / float64(k.limit) * float64(time.Second))
}

// take consumes one token of key if there is one (the bucket never goes below empty).
func (k *keyedLimiter) take(key string) {
	now := k.nowFn()
	k.mu.Lock()
	defer k.mu.Unlock()
	e, found := k.cache.Get(key)
	if !found {
		e = &limiterEntry{lim: rate.NewLimiter(k.limit, k.burst)}
		k.cache.Add(key, e)
	}
	e.lim.AllowN(now, 1)
}

// failureTracker delays clients that repeatedly present invalid bearer tokens.
type failureTracker struct {
	mu    sync.Mutex
	cache *lru.Cache[string, *failEntry]
	now   func() time.Time
}

type failEntry struct {
	// seen holds the hashes of the invalid tokens counted so far.
	seen         map[string]struct{}
	n            int
	last         time.Time
	blockedUntil time.Time
	auditedAt    time.Time
}

func newFailureTracker(now func() time.Time) *failureTracker {
	c, _ := lru.New[string, *failEntry](8192)
	return &failureTracker{cache: c, now: now}
}

// blocked returns the remaining delay for key (0 = not blocked).
func (f *failureTracker) blocked(key string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.cache.Peek(key); ok {
		if d := e.blockedUntil.Sub(f.now()); d > 0 {
			return d
		}
	}
	return 0
}

// fail records a failed attempt of token (hash) from the client key. Only
// distinct tokens count: a client that keeps presenting the same stale token
// does not drive the address into a delay that would also hit other clients
// behind it. audit is true for the first failure per minute after the grace
// count is exceeded.
func (f *failureTracker) fail(key, token string) (audit bool) {
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache.Get(key)
	if !ok || now.Sub(e.last) > tokenFailForget {
		e = &failEntry{}
		f.cache.Add(key, e)
	}
	// the failure count decays by one per idle minute (a success does NOT
	// reset it: an attacker holding one valid token must not be able to)
	if idle := int(now.Sub(e.last) / time.Minute); idle > 0 {
		e.n = max(0, e.n-idle)
	}
	if e.n == 0 {
		e.seen = nil
	}
	if _, repeated := e.seen[token]; repeated {
		return false
	}
	if e.seen == nil {
		e.seen = make(map[string]struct{}, 8)
	}
	if len(e.seen) < tokenFailDistinct {
		e.seen[token] = struct{}{}
	}
	e.n++
	e.last = now
	if e.n > tokenFailGrace {
		shift := e.n - tokenFailGrace - 1
		if shift > 10 {
			shift = 10
		}
		d := time.Second << shift
		if d > tokenFailMaxBackoff {
			d = tokenFailMaxBackoff
		}
		e.blockedUntil = now.Add(d)
		if now.Sub(e.auditedAt) >= limitAuditInterval {
			e.auditedAt = now
			return true
		}
	}
	return false
}

// accountBackoff delays sign-in attempts at the bootstrap admin after
// consecutive failures, whatever the client address (per replica). A success
// resets it.
type accountBackoff struct {
	mu           sync.Mutex
	now          func() time.Time
	n            int
	last         time.Time
	blockedUntil time.Time
	auditedAt    time.Time
	inFlight     bool
}

func newAccountBackoff(now func() time.Time) *accountBackoff { return &accountBackoff{now: now} }

// forgetLocked drops the failures after bootstrapFailForget without a new one.
func (b *accountBackoff) forgetLocked(now time.Time) {
	if b.n > 0 && now.Sub(b.last) > bootstrapFailForget {
		b.n, b.blockedUntil = 0, time.Time{}
	}
}

// acquire hands out the single verification slot. It refuses (acquired is
// false) while a delay is running or another attempt is in flight, and
// returns the remaining wait and whether this denial should be audited (first
// one per minute). The caller releases the slot with release.
func (b *accountBackoff) acquire() (retry time.Duration, audit, acquired bool) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forgetLocked(now)
	d := b.blockedUntil.Sub(now)
	if d <= 0 && b.inFlight {
		d = time.Second
	}
	if d <= 0 {
		b.inFlight = true
		return 0, false, true
	}
	if now.Sub(b.auditedAt) >= limitAuditInterval {
		b.auditedAt = now
		return d, true, false
	}
	return d, false, false
}

// release gives the verification slot back.
func (b *accountBackoff) release() {
	b.mu.Lock()
	b.inFlight = false
	b.mu.Unlock()
}

// fail records a failed attempt.
func (b *accountBackoff) fail() {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forgetLocked(now)
	b.n++
	b.last = now
	if b.n > bootstrapFailGrace {
		d := bootstrapMaxBackoff
		if shift := b.n - bootstrapFailGrace - 1; shift < 20 {
			d = min(time.Second<<shift, bootstrapMaxBackoff)
		}
		b.blockedUntil = now.Add(d)
	}
}

// reset forgets all failures (successful sign-in).
func (b *accountBackoff) reset() {
	b.mu.Lock()
	b.n, b.blockedUntil = 0, time.Time{}
	b.mu.Unlock()
}

// goodTokens remembers (bounded, with TTL) the tokens that recently
// authenticated successfully, so a delayed client address does not lock out
// legitimate API clients behind the same proxy address.
type goodTokens struct {
	cache *lru.Cache[string, time.Time]
	now   func() time.Time
}

func newGoodTokens(now func() time.Time) *goodTokens {
	c, _ := lru.New[string, time.Time](4096)
	return &goodTokens{cache: c, now: now}
}

func (g *goodTokens) known(hash string) bool {
	until, ok := g.cache.Get(hash)
	return ok && g.now().Before(until)
}

func (g *goodTokens) add(hash string)    { g.cache.Add(hash, g.now().Add(tokenGoodTTL)) }
func (g *goodTokens) remove(hash string) { g.cache.Remove(hash) }

// rateLimitedError builds the error of a throttled request (429 with Retry-After).
func rateLimitedError(w http.ResponseWriter, retry time.Duration, what string) error {
	secs := int(retry.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	if w != nil {
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	}
	return &domain.RateLimitError{Msg: fmt.Sprintf("too many %s, retry in %ds", what, secs), After: time.Duration(secs) * time.Second}
}

// ------------------------------------------------------------ invalidation ---

// sessionMaxAge is the absolute session lifetime.
func (s *Service) sessionMaxAge() time.Duration {
	if s.cfg.SessionMaxAge > 0 {
		return s.cfg.SessionMaxAge
	}
	return DefaultSessionMaxAge
}

// publishInvalidate drops cached authentication results on every replica.
// The local caches are purged immediately; remote replicas follow through the
// event bus (their TTL of 5s remains the fallback if an event is lost).
func (s *Service) publishInvalidate(ctx context.Context, kind, id string) {
	s.purgeAuthCaches(kind, id)
	if s.deps.Bus == nil {
		return
	}
	if err := s.deps.Bus.Publish(context.WithoutCancel(ctx), eventbus.AuthInvalidate, eventbus.AuthInvalidatePayload{Kind: kind, ID: id}); err != nil {
		s.log.Warn("publish auth invalidation failed (caches expire on their own)", "kind", kind, "error", err)
	}
}

func (s *Service) purgeAuthCaches(kind, id string) {
	switch kind {
	case "session":
		if id != "" {
			s.sessions.Remove(id)
			return
		}
		s.sessions.Purge()
	case "api_token":
		s.apiTokens.Purge()
	case "enrollment_token":
		s.enroll.Purge()
	default: // "user": sessions and the creator checks of API tokens
		s.sessions.Purge()
		s.apiTokens.Purge()
	}
}

// publishEnrollmentRevoked tells all replicas to drop the token and to
// disconnect agent sessions that authenticated with it.
func (s *Service) publishEnrollmentRevoked(ctx context.Context, id uuid.UUID, reason string) {
	s.enroll.Purge()
	if s.deps.Bus == nil {
		return
	}
	if err := s.deps.Bus.Publish(context.WithoutCancel(ctx), eventbus.EnrollmentTokenRevoked, eventbus.EnrollmentTokenPayload{TokenID: id, Reason: reason}); err != nil {
		s.log.Warn("publish enrollment token revocation failed (sessions are re-validated periodically)", "token", id, "error", err)
	}
}

// busLoop applies invalidation events published by any replica.
func (s *Service) busLoop(ctx context.Context) {
	ch, cancel := s.deps.Bus.Subscribe(eventbus.AuthInvalidate, eventbus.EnrollmentTokenRevoked, eventbus.BusReconnected)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Type {
			case eventbus.AuthInvalidate:
				var p eventbus.AuthInvalidatePayload
				if err := ev.Decode(&p); err == nil {
					s.purgeAuthCaches(p.Kind, p.ID)
				}
			case eventbus.EnrollmentTokenRevoked, eventbus.BusReconnected:
				s.enroll.Purge()
				if ev.Type == eventbus.BusReconnected {
					s.sessions.Purge()
					s.apiTokens.Purge()
				}
			}
		}
	}
}

// EnrollmentTokenState reports whether the enrollment token is still valid
// (not revoked, not expired). It reads the database (no cache) and is used to
// re-validate established agent connections.
func (s *Service) EnrollmentTokenState(ctx context.Context, id uuid.UUID) error {
	t, err := s.deps.Store.Identity().GetEnrollmentToken(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("%w: enrollment token no longer exists", domain.ErrUnauthorized)
		}
		return fmt.Errorf("%w: enrollment token lookup: %v", domain.ErrUnavailable, err)
	}
	now := s.now()
	if t.RevokedAt != nil {
		return fmt.Errorf("%w: enrollment token was revoked", domain.ErrUnauthorized)
	}
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return fmt.Errorf("%w: enrollment token expired", domain.ErrUnauthorized)
	}
	return nil
}
