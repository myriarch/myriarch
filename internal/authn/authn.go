// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package authn implements authentication: OIDC login against any standards
// compliant identity provider, the bootstrap admin (a break-glass account whose
// credentials come from files), dev login, server-side sessions, API tokens,
// agent enrollment tokens (created in the UI or declared in the configuration)
// and optional mTLS client certificates; plus the REST endpoints of the tags
// auth, users and tokens.
//
// Security properties (see docs/security/model.md):
//   - Tokens (api `myra_`, enrollment `myre_`) are random, only SHA-256 hashes are
//     stored; lookups happen by hash.
//   - Browser sessions are server side (cookie = 32 random bytes, database id =
//     SHA-256 of it), HttpOnly, SameSite=Lax, Secure by configuration; mutating
//     requests authenticated by the cookie need the double-submit CSRF header.
//   - OIDC uses the authorization code flow with PKCE, state and nonce, bound to
//     the browser by an HMAC-signed short-lived cookie whose key is shared by all
//     replicas through the KV store.
//   - Positive/negative lookups are cached for seconds only (sessions/API
//     tokens: 5s, enrollment tokens: 60s). Logout, revocation, disabling and
//     role changes additionally publish invalidation events on the event bus,
//     so every replica drops its caches immediately (the TTLs are the fallback
//     if an event is lost).
//   - Sessions have an idle timeout (auth.sessionTtl, sliding) and an absolute
//     lifetime (auth.sessionMaxAge).
//   - Failed logins and invalid bearer tokens are rate limited per client IP
//     (and per user name for the dev login); IPv6 clients share one limit per
//     /64 prefix, in every authentication failure limiter and in the OpAMP connect
//     guard (realip.LimiterKey). The bootstrap admin sign-in is limited per
//     client address and by an exponential backoff of the account.
//   - The bootstrap admin is controlled by the configuration only: it is
//     created, enabled and disabled at startup, its credentials are read from
//     files on every attempt, one-time tokens are consumed through the KV store,
//     and its sessions and API tokens stop working on every replica that does
//     not consider it active.
package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn/clientassert"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
	lru "github.com/myriarch/myriarch/internal/lru"
	"github.com/myriarch/myriarch/internal/metrics"
	"github.com/myriarch/myriarch/internal/store"
)

// Cookie names.
const (
	SessionCookie = brand.SessionCookie
	// SessionCookieSecure is the name of the session cookie when auth.cookieSecure is set (the __Host- prefix).
	SessionCookieSecure = brand.SessionCookieHost
	CSRFCookie          = brand.CSRFCookie
	// CSRFHeader must equal the session's CSRF token for mutating requests that
	// are authenticated by the session cookie (double submit).
	CSRFHeader = "X-CSRF-Token"
	// oidcCookie carries the signed OIDC login state.
	oidcCookie = brand.OIDCStateCookie
)

// Cache TTLs and touch intervals.
const (
	principalCacheTTL = 5 * time.Second
	enrollPositiveTTL = 60 * time.Second
	enrollNegativeTTL = 10 * time.Second
	touchInterval     = time.Minute
	cacheSize         = 4096
	// negative enrollment lookups use their own, smaller cache
	enrollNegativeCacheSize = 1024
	// lookups of enrollment tokens without a cache entry: per second and replica, with a burst
	enrollLookupRate  rate.Limit = 100
	enrollLookupBurst            = 200
)

// Deps are the dependencies of the service.
type Deps struct {
	Store      store.Store
	Audit      audit.Recorder
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	DevMode    bool
	// Bus (optional) distributes cache invalidations to all replicas.
	Bus eventbus.Bus
	// HTTPClient is used for OIDC discovery and token exchange (default: 15s timeout).
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
	// UI holds the language options that GET /auth/config hands to the web
	// UI (an empty DefaultLanguage is reported as "en").
	UI config.UIConfig
}

// Service authenticates users, API clients and agents.
type Service struct {
	cfg    config.AuthConfig
	server config.ServerConfig
	opamp  config.OpAMPConfig
	deps   Deps
	log    *slog.Logger
	now    func() time.Time
	ip     *realip.Resolver
	http   *http.Client

	cookieSecret []byte
	clientCAs    *x509.CertPool
	// assertion signs the client assertions of the token request when OIDC
	// uses a client certificate (private_key_jwt) instead of a secret.
	assertion *clientassert.Signer

	sessions  *lru.Cache[string, *principalEntry]
	apiTokens *lru.Cache[string, *apiTokenEntry]
	enroll    *lru.Cache[string, *enrollEntry]
	// enrollNegative holds the expiry of unknown tokens (kept apart from enroll so that a flood of
	// unknown tokens cannot evict valid ones); enrollFlight coalesces concurrent lookups and
	// enrollLookups bounds the lookups of uncached tokens per replica.
	enrollNegative *lru.Cache[string, time.Time]
	enrollFlight   singleflight.Group
	enrollLookups  *rate.Limiter

	touchMu     sync.Mutex
	lastTouch   map[string]time.Time
	enrollUses  map[uuid.UUID]*enrollUse
	oidcMu      sync.RWMutex
	oidc        *oidcClient
	devUsers    []devUser
	metricLogin *prometheus.CounterVec
	metricAgent *prometheus.CounterVec
	metricToken *prometheus.CounterVec

	loginIP   *keyedLimiter
	loginUser *keyedLimiter
	// bootstrapIP and bootstrapBackoff limit sign-ins at the bootstrap admin
	// by client address and for the account as a whole.
	bootstrapIP      *keyedLimiter
	bootstrapBackoff *accountBackoff
	tokenFail        *failureTracker
	tokenGood        *goodTokens
	// oidcFail charges the failed OIDC callbacks of a client address; an address with an empty bucket is refused
	// at both OIDC endpoints. oidcAudit lets one failure per address and minute reach the audit log as long as
	// no valid signed login state was presented.
	oidcFail  *keyedLimiter
	oidcAudit *keyedLimiter
	// installFail charges the failed authentications of the install routes per client address (AuthenticateInstall).
	installFail *keyedLimiter
}

type principalEntry struct {
	principal *authz.Principal
	csrf      string
	sessionID string
	expires   time.Time // session expiry as stored
	loaded    time.Time
}

type apiTokenEntry struct {
	token   *domain.APIToken // nil: unknown token
	creator *domain.User     // creating user (nil: none or unknown)
	loaded  time.Time
}

type enrollEntry struct {
	token *domain.EnrollmentToken // nil: unknown token
	until time.Time
}

type enrollUse struct {
	n    int
	last time.Time
}

type devUser struct {
	username string
	pwHash   [32]byte
	role     domain.Role
}

// New creates the service. With OIDC enabled it performs OIDC discovery
// (retrying in the background if the IdP is unreachable at startup). The
// context bounds the background goroutines (discovery retry, token usage
// flush).
func New(ctx context.Context, cfg config.AuthConfig, server config.ServerConfig, opamp config.OpAMPConfig, deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("authn: store is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		cfg: cfg, server: server, opamp: opamp, deps: deps, log: logger.With("component", "authn"),
		now: deps.Now, http: deps.HTTPClient,
		lastTouch: map[string]time.Time{}, enrollUses: map[uuid.UUID]*enrollUse{},
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: 15 * time.Second}
	}
	{
		// responses of the identity provider are bounded (discovery, key set, token response)
		hc := *s.http
		base := hc.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		hc.Transport = &limitTransport{rt: base, max: maxIdPResponseBytes}
		s.http = &hc
	}
	nets, err := realip.ParseTrusted(server.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("authn: server.trustedProxies: %w", err)
	}
	s.ip = realip.New(nets)

	s.sessions, _ = lru.New[string, *principalEntry](cacheSize)
	s.apiTokens, _ = lru.New[string, *apiTokenEntry](cacheSize)
	s.enroll, _ = lru.New[string, *enrollEntry](cacheSize)
	s.enrollNegative, _ = lru.New[string, time.Time](enrollNegativeCacheSize)
	s.enrollLookups = rate.NewLimiter(enrollLookupRate, enrollLookupBurst)

	s.metricLogin = metrics.Register(deps.Registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_auth_logins_total", Help: "User login attempts by method and result.",
	}, []string{"method", "result"}))
	s.metricAgent = metrics.Register(deps.Registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_auth_agent_requests_total", Help: "Agent (OpAMP) authentication results.",
	}, []string{"result"}))

	s.metricToken = metrics.Register(deps.Registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_auth_token_failures_total", Help: "Rejected API token authentications by reason.",
	}, []string{"reason"}))
	loginLimit := rate.Every(time.Minute / loginRatePerMinute)
	s.loginIP = newKeyedLimiter(loginLimit, loginBurst, s.now)
	s.loginUser = newKeyedLimiter(loginLimit, loginBurst, s.now)
	s.bootstrapIP = newKeyedLimiter(rate.Every(time.Minute/bootstrapRatePerMinute), bootstrapBurst, s.now)
	s.bootstrapBackoff = newAccountBackoff(s.now)
	s.oidcFail = newKeyedLimiter(rate.Every(oidcFailEvery), oidcFailBurst, s.now)
	s.oidcAudit = newKeyedLimiter(rate.Every(time.Minute), 1, s.now)
	s.installFail = newKeyedLimiter(rate.Every(installFailEvery), installFailBurst, s.now)
	s.tokenFail = newFailureTracker(s.now)
	s.tokenGood = newGoodTokens(s.now)

	if err := s.checkBootstrapFiles(); err != nil {
		return nil, err
	}

	if cfg.OIDC.Enabled && cfg.OIDC.DefaultRole != "" && !deps.DevMode {
		s.log.Warn("auth.oidc.defaultRole is set: every user of the identity provider who signs in without a mapped group receives this role; "+
			"prefer an explicit auth.oidc.roleMapping and leave defaultRole empty (deny)", "defaultRole", string(cfg.OIDC.DefaultRole))
	}

	for _, u := range cfg.DevLogin.Users {
		s.devUsers = append(s.devUsers, devUser{username: u.Username, pwHash: sha256.Sum256([]byte(u.Password)), role: u.Role})
	}

	if server.TLS.ClientCAFile != "" {
		pem, err := os.ReadFile(server.TLS.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("authn: read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("authn: client CA file %s contains no certificate", server.TLS.ClientCAFile)
		}
		s.clientCAs = pool
	}

	s.cookieSecret, err = loadOrCreateSecret(ctx, deps.Store.KV(), kvCookieSecret)
	if err != nil {
		return nil, fmt.Errorf("authn: cookie secret: %w", err)
	}

	if cfg.OIDC.Enabled {
		if cfg.OIDC.ClientCertificateFile != "" || cfg.OIDC.ClientKeyFile != "" {
			if cfg.OIDC.ClientSecret != "" || cfg.OIDC.ClientSecretFile != "" {
				return nil, errors.New("authn: auth.oidc: use either a client secret or a client certificate, not both")
			}
			s.assertion, err = clientassert.New(cfg.OIDC.ClientCertificateFile, cfg.OIDC.ClientKeyFile)
			if err != nil {
				return nil, fmt.Errorf("authn: auth.oidc client certificate: %w", err)
			}
			if exp, err := s.assertion.NotAfter(); err == nil && exp.Before(s.now().Add(30*24*time.Hour)) {
				s.log.Warn("the OIDC client certificate expires soon: rotate it in the app registration and in the files", "notAfter", exp.UTC().Format(time.RFC3339))
			}
		}
		s.startOIDC(ctx)
	}
	go s.enrollUsageLoop(ctx)
	if deps.Bus != nil {
		go s.busLoop(ctx)
	}
	return s, nil
}

// ------------------------------------------------------------ principals ---

// Authenticate resolves the principal of a REST request: session cookie
// (users) or "Authorization: Bearer myra_..." (API tokens). Returns
// domain.ErrUnauthorized if no valid credential is present. Used by the HTTP
// middleware (internal/httpserver).
func (s *Service) Authenticate(r *http.Request) (*authz.Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := bearerToken(h)
		if !ok || !strings.HasPrefix(tok, domain.APITokenPrefix) {
			return nil, fmt.Errorf("%w: unsupported or invalid Authorization header", domain.ErrUnauthorized)
		}
		ip := s.clientIP(r)
		// A client address with many recent failures (of distinct tokens) is
		// delayed: tokens that are neither cached nor recently valid are not even
		// looked up (no database load from guessing). Valid tokens of other
		// clients behind the same address keep working. IPv6 clients share one
		// counter per /64 (realip.LimiterKey); the audit entry names the real address.
		th := hashToken(tok).String()
		failKey := realip.LimiterKey(ip)
		blocked := s.tokenFail.blocked(failKey)
		p, err := s.authAPIToken(r.Context(), tok, blocked == 0 || s.tokenGood.known(th))
		if errors.Is(err, errLookupDelayed) {
			s.metricToken.WithLabelValues("rate_limited").Inc()
			return nil, rateLimitedError(nil, blocked, "invalid token attempts")
		}
		switch {
		case err == nil:
			s.tokenGood.add(th)
		case errors.Is(err, domain.ErrUnauthorized):
			s.metricToken.WithLabelValues("invalid").Inc()
			s.tokenGood.remove(th)
			if s.tokenFail.fail(failKey, th) {
				s.record(r, s.tokenFailureAudit(r, ip))
			}
		}
		return p, err
	}
	c, err := r.Cookie(s.sessionCookieName())
	if err != nil || c.Value == "" {
		return nil, fmt.Errorf("%w: authentication required", domain.ErrUnauthorized)
	}
	e, err := s.sessionFromCookie(r.Context(), c.Value)
	if err != nil {
		return nil, err
	}
	return e.principal, nil
}

func bearerToken(h string) (string, bool) {
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	return tok, tok != ""
}

func hashToken(tok string) domain.Hash { return domain.SHA256([]byte(tok)) }

func sessionIDFromCookie(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// errLookupDelayed marks a token that needs a database lookup while its client
// address is being delayed.
var errLookupDelayed = fmt.Errorf("%w: token lookup delayed", domain.ErrTooManyRequests)

func (s *Service) authAPIToken(ctx context.Context, tok string, allowLookup bool) (*authz.Principal, error) {
	if len(tok) > 128 {
		return nil, fmt.Errorf("%w: invalid token", domain.ErrUnauthorized)
	}
	h := hashToken(tok)
	key := h.String()
	now := s.now()
	var t *domain.APIToken
	var creator *domain.User
	if e, ok := s.apiTokens.Get(key); ok && now.Sub(e.loaded) < principalCacheTTL {
		t, creator = e.token, e.creator
	} else {
		if !allowLookup {
			return nil, errLookupDelayed
		}
		got, err := s.deps.Store.Identity().GetAPITokenByHash(ctx, h)
		switch {
		case err == nil:
			t = got
		case errors.Is(err, domain.ErrNotFound):
			t = nil
		default:
			return nil, fmt.Errorf("%w: token lookup: %v", domain.ErrUnavailable, err)
		}
		if t != nil && t.CreatedByUserID != nil {
			u, err := s.deps.Store.Identity().GetUser(ctx, *t.CreatedByUserID)
			switch {
			case err == nil:
				creator = u
			case errors.Is(err, domain.ErrNotFound):
				creator = nil
			default:
				return nil, fmt.Errorf("%w: token creator lookup: %v", domain.ErrUnavailable, err)
			}
		}
		s.apiTokens.Add(key, &apiTokenEntry{token: t, creator: creator, loaded: now})
	}
	if t == nil || t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return nil, fmt.Errorf("%w: invalid, revoked or expired token", domain.ErrUnauthorized)
	}
	if max := s.cfg.APITokenMaxTTL; max > 0 && !now.Before(t.CreatedAt.Add(max)) {
		// effective expiry = min(expiresAt, createdAt + auth.apiTokenMaxTtl): also for tokens that were
		// created before the maximum existed or without an expiry
		return nil, fmt.Errorf("%w: the token is older than the maximum token lifetime", domain.ErrUnauthorized)
	}
	role := t.Role
	if t.CreatedByUserID != nil {
		// A token never outlives or outranks its creator.
		if creator == nil || creator.Disabled {
			return nil, fmt.Errorf("%w: the user who created this token is disabled or removed", domain.ErrUnauthorized)
		}
		if creator.Provider == domain.ProviderBootstrap && !s.bootstrapUserActive(creator) {
			return nil, fmt.Errorf("%w: the bootstrap admin who created this token is not active", domain.ErrUnauthorized)
		}
		if creator.Provider == domain.ProviderLocal && !s.cfg.DevLogin.Enabled {
			// tokens of dev accounts (e.g. left in a database that was used with "myriarchd dev") are worthless
			// where dev login is off, like their sessions
			return nil, fmt.Errorf("%w: local accounts are not accepted by this server", domain.ErrUnauthorized)
		}
		if domain.RoleRank(creator.Role) < domain.RoleRank(role) {
			role = creator.Role
		}
	}
	s.touchAsync("api:"+t.ID.String(), func(c context.Context) error {
		return s.deps.Store.Identity().TouchAPIToken(c, t.ID, now)
	})
	return &authz.Principal{Type: authz.PrincipalToken, ID: t.ID.String(), Name: t.Name, Role: role, AuthMethod: "api_token"}, nil
}

// tokenFailureAudit is the (rate limited) audit entry of repeated invalid bearer tokens.
func (s *Service) tokenFailureAudit(r *http.Request, ip string) domain.AuditEvent {
	return domain.AuditEvent{
		Time: s.now(), ActorType: domain.ActorToken, ActorID: "unknown", ActorName: "unknown", Action: "auth.token_failed",
		TargetType: "api_token", Result: "failure", SourceIP: ip, RequestID: r.Header.Get(api.RequestIDHeader),
		Details: map[string]any{"reason": "invalid_token", "path": truncate(r.URL.Path, 128)},
	}
}

// sessionFromCookie loads (cached) the session identified by the cookie value.
func (s *Service) sessionFromCookie(ctx context.Context, cookieValue string) (*principalEntry, error) {
	if len(cookieValue) > 256 {
		return nil, fmt.Errorf("%w: invalid session", domain.ErrUnauthorized)
	}
	id := sessionIDFromCookie(cookieValue)
	now := s.now()
	if e, ok := s.sessions.Get(id); ok && now.Sub(e.loaded) < principalCacheTTL {
		s.touchSession(id, now)
		return e, nil
	}
	s.sessions.Remove(id)
	sess, err := s.deps.Store.Identity().GetSession(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("%w: session not found", domain.ErrUnauthorized)
		}
		return nil, fmt.Errorf("%w: session lookup: %v", domain.ErrUnavailable, err)
	}
	if !now.Before(sess.ExpiresAt) {
		go s.deleteSession(id)
		return nil, fmt.Errorf("%w: session expired", domain.ErrUnauthorized)
	}
	if !sess.CreatedAt.IsZero() && !now.Before(sess.CreatedAt.Add(s.sessionMaxAge())) {
		// absolute lifetime reached: the user signs in again (identity
		// provider changes take effect at that point at the latest)
		go s.deleteSession(id)
		return nil, fmt.Errorf("%w: session reached its maximum age", domain.ErrUnauthorized)
	}
	u, err := s.deps.Store.Identity().GetUser(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("%w: user not found", domain.ErrUnauthorized)
		}
		return nil, fmt.Errorf("%w: user lookup: %v", domain.ErrUnavailable, err)
	}
	if u.Disabled {
		return nil, fmt.Errorf("%w: account is disabled", domain.ErrUnauthorized)
	}
	if u.Provider == domain.ProviderBootstrap && !s.bootstrapUserActive(u) {
		// the bootstrap admin is managed by the configuration of this replica:
		// a session of a deactivated account is worthless even if the database
		// row was not (yet) disabled
		return nil, fmt.Errorf("%w: the bootstrap admin is not active on this server", domain.ErrUnauthorized)
	}
	if u.Provider == domain.ProviderLocal && !s.cfg.DevLogin.Enabled {
		// sessions of dev accounts (e.g. left in a database that was used with
		// "myriarchd dev") are worthless where dev login is off
		return nil, fmt.Errorf("%w: local accounts are not accepted by this server", domain.ErrUnauthorized)
	}
	name := userDisplayName(u)
	e := &principalEntry{
		principal: &authz.Principal{Type: authz.PrincipalUser, ID: u.ID.String(), Name: name, Email: u.Email, Role: u.Role, AuthMethod: u.Provider},
		csrf:      sess.CSRFToken, sessionID: id, expires: sess.ExpiresAt, loaded: now,
	}
	s.sessions.Add(id, e)
	s.touchSession(id, now)
	return e, nil
}

func (s *Service) deleteSession(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.deps.Store.Identity().DeleteSession(ctx, id)
}

// touchSession extends the sliding session expiry at most once per minute.
func (s *Service) touchSession(id string, now time.Time) {
	s.touchAsync("sess:"+id, func(c context.Context) error {
		return s.deps.Store.Identity().TouchSession(c, id, now, s.sessionMaxAge())
	})
}

// touchAsync runs fn at most once per touchInterval for key, in the background.
func (s *Service) touchAsync(key string, fn func(ctx context.Context) error) {
	now := s.now()
	s.touchMu.Lock()
	last, seen := s.lastTouch[key]
	if seen && now.Sub(last) < touchInterval {
		s.touchMu.Unlock()
		return
	}
	s.lastTouch[key] = now
	if len(s.lastTouch) > 4*cacheSize { // bound the map: forget old entries
		for k, t := range s.lastTouch {
			if now.Sub(t) > 2*touchInterval {
				delete(s.lastTouch, k)
			}
		}
	}
	s.touchMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			s.log.Debug("touch failed", "key", key, "error", err)
		}
	}()
}

// CheckCSRF validates the double-submit token for cookie-authenticated
// mutating requests (POST/PUT/PATCH/DELETE). API-token requests are exempt.
func (s *Service) CheckCSRF(r *http.Request, p *authz.Principal) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if p == nil {
		return fmt.Errorf("%w: csrf check without principal", domain.ErrForbidden)
	}
	if p.AuthMethod == "api_token" || p.Type == authz.PrincipalSystem {
		return nil
	}
	c, err := r.Cookie(s.sessionCookieName())
	if err != nil {
		return fmt.Errorf("%w: csrf token missing or invalid", domain.ErrForbidden)
	}
	e, err := s.sessionFromCookie(r.Context(), c.Value)
	if err != nil {
		return fmt.Errorf("%w: csrf token missing or invalid", domain.ErrForbidden)
	}
	got := r.Header.Get(CSRFHeader)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(e.csrf)) != 1 {
		return fmt.Errorf("%w: csrf token missing or invalid", domain.ErrForbidden)
	}
	return nil
}

// --------------------------------------------------------------- helpers ---

func (s *Service) record(r *http.Request, e domain.AuditEvent) {
	if s.deps.Audit == nil {
		return
	}
	s.deps.Audit.Record(r.Context(), e)
}

func (s *Service) auditRequest(r *http.Request, action, targetType, targetID string, details map[string]any, err error) {
	if s.deps.Audit == nil {
		return
	}
	s.deps.Audit.Record(r.Context(), audit.FromRequest(r, action, targetType, targetID, details, err))
}

func (s *Service) clientIP(r *http.Request) string { return realip.ClientIP(r) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// writeError is api.WriteError.
func writeError(w http.ResponseWriter, r *http.Request, err error) { api.WriteError(w, r, err) }

// userDisplayName is the name shown for a user in audit entries and the UI:
// name, else e-mail, else the identity provider subject.
func userDisplayName(u *domain.User) string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	}
	return u.Subject
}
