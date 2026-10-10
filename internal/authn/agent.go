// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
)

// AgentCredential is the result of authenticating an OpAMP connection.
type AgentCredential struct {
	// EnrollmentTokenID is set when an enrollment token was presented.
	EnrollmentTokenID *uuid.UUID
	// Labels from the enrollment token (initial agent labels).
	Labels map[string]string
	// ClientCertSubject is set when a verified client certificate was presented.
	ClientCertSubject string
}

// AuthenticateAgent authenticates an OpAMP HTTP/WebSocket handshake request
// (called from the opamp-go OnConnecting callback). Enrollment tokens are
// looked up by SHA-256 with an in-memory LRU cache (TTL 60s) to survive reconnect storms of
// thousands of agents. Only tokens of the documented form are looked up; unknown tokens are cached
// negatively for 10s in a separate, smaller cache, concurrent lookups are coalesced and lookups of
// tokens without a cache entry are bounded per replica (domain.ErrTooManyRequests). Returns domain.ErrUnauthorized when credentials are missing or
// invalid and the config requires them; infrastructure problems (database
// down) are reported as domain.ErrUnavailable.
//
// Rules:
//   - opamp.requireClientCert: a verified client certificate is mandatory.
//   - An "Authorization: Bearer myre_..." header must be valid when present (a bad
//     token is never silently ignored).
//   - opamp.requireEnrollmentToken: a valid token is mandatory, also when a
//     client certificate is presented (both set means token AND certificate).
//   - Without token and with requireEnrollmentToken=false: allowed (mTLS-only
//     setups with requireClientCert, or dev).
func (s *Service) AuthenticateAgent(r *http.Request) (*AgentCredential, error) {
	cred, err := s.authenticateAgent(r)
	switch {
	case err == nil:
		s.metricAgent.WithLabelValues("ok").Inc()
	case errors.Is(err, domain.ErrUnauthorized):
		s.metricAgent.WithLabelValues("unauthorized").Inc()
	case errors.Is(err, domain.ErrTooManyRequests):
		s.metricAgent.WithLabelValues("rate_limited").Inc()
	default:
		s.metricAgent.WithLabelValues("error").Inc()
	}
	return cred, err
}

func (s *Service) authenticateAgent(r *http.Request) (*AgentCredential, error) {
	cert := s.clientCert(r)
	if s.opamp.RequireClientCert && cert == "" {
		return nil, fmt.Errorf("%w: a verified client certificate is required", domain.ErrUnauthorized)
	}

	h := r.Header.Get("Authorization")
	if h != "" {
		tok, ok := bearerToken(h)
		if !ok || !strings.HasPrefix(tok, domain.EnrollmentTokenPrefix) {
			return nil, fmt.Errorf("%w: invalid Authorization header", domain.ErrUnauthorized)
		}
		cred, err := s.authEnrollmentToken(r.Context(), tok)
		if err != nil {
			return nil, err
		}
		cred.ClientCertSubject = cert
		return cred, nil
	}
	if !s.opamp.RequireEnrollmentToken {
		return &AgentCredential{ClientCertSubject: cert, Labels: map[string]string{}}, nil
	}
	return nil, fmt.Errorf("%w: enrollment token required", domain.ErrUnauthorized)
}

func (s *Service) authEnrollmentToken(ctx context.Context, tok string) (*AgentCredential, error) {
	// Only the documented token form reaches the cache and the database.
	if !domain.ValidEnrollmentToken(tok) {
		return nil, fmt.Errorf("%w: invalid enrollment token", domain.ErrUnauthorized)
	}
	h := hashToken(tok)
	key := h.String()
	now := s.now()
	var t *domain.EnrollmentToken
	e, known := s.enroll.Get(key)
	if known && now.Before(e.until) {
		t = e.token
	} else {
		if until, ok := s.enrollNegative.Get(key); ok && now.Before(until) {
			return nil, fmt.Errorf("%w: invalid, revoked or expired enrollment token", domain.ErrUnauthorized)
		}
		// Concurrent lookups of one token share a single database round trip. Lookups of tokens
		// without any cache entry (unknown or new) are bounded per replica; the refresh of a token
		// that was seen before is not charged, so a flood of unknown tokens cannot keep known agents out.
		v, err, _ := s.enrollFlight.Do(key, func() (any, error) {
			if !known && !s.enrollLookups.AllowN(now, 1) {
				return nil, &domain.RateLimitError{Msg: "too many enrollment token lookups", After: time.Second}
			}
			got, err := s.deps.Store.Identity().GetEnrollmentTokenByHash(ctx, h)
			switch {
			case err == nil:
				s.enroll.Add(key, &enrollEntry{token: got, until: now.Add(enrollPositiveTTL)})
				return got, nil
			case errors.Is(err, domain.ErrNotFound):
				s.enrollNegative.Add(key, now.Add(enrollNegativeTTL))
				return (*domain.EnrollmentToken)(nil), nil
			default:
				return nil, fmt.Errorf("%w: enrollment token lookup: %v", domain.ErrUnavailable, err)
			}
		})
		if err != nil {
			return nil, err
		}
		t, _ = v.(*domain.EnrollmentToken)
	}
	if t == nil || t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return nil, fmt.Errorf("%w: invalid, revoked or expired enrollment token", domain.ErrUnauthorized)
	}
	s.countEnrollUse(t.ID, now)
	id := t.ID
	labels := make(map[string]string, len(t.Labels))
	for k, v := range t.Labels {
		labels[k] = v
	}
	return &AgentCredential{EnrollmentTokenID: &id, Labels: labels}, nil
}

// countEnrollUse records one use; the database is updated by enrollUsageLoop
// (a reconnect storm must not serialize thousands of UPDATEs on one row inside
// the handshake).
func (s *Service) countEnrollUse(id uuid.UUID, now time.Time) {
	s.touchMu.Lock()
	u := s.enrollUses[id]
	if u == nil {
		u = &enrollUse{}
		s.enrollUses[id] = u
	}
	u.n++
	u.last = now
	s.touchMu.Unlock()
}

const enrollFlushInterval = 5 * time.Second

func (s *Service) enrollUsageLoop(ctx context.Context) {
	t := time.NewTicker(enrollFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.FlushEnrollmentUsage(fctx)
			cancel()
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			s.FlushEnrollmentUsage(fctx)
			cancel()
		}
	}
}

// FlushEnrollmentUsage writes pending enrollment token usage (use_count,
// last_used_at) to the database.
func (s *Service) FlushEnrollmentUsage(ctx context.Context) {
	s.touchMu.Lock()
	pending := s.enrollUses
	s.enrollUses = map[uuid.UUID]*enrollUse{}
	s.touchMu.Unlock()
	for id, u := range pending {
		// one statement per token and flush, however many uses were counted
		if err := s.deps.Store.Identity().TouchEnrollmentToken(ctx, id, u.last, u.n); err != nil {
			s.log.Warn("enrollment token usage update failed", "token", id, "error", err)
		}
	}
}

// clientCert returns the subject of a verified client certificate ("" if none).
// Sources: the TLS handshake of myriarchd itself (r.TLS.VerifiedChains, verified
// against server.tls.clientCaFile) or the URL-encoded PEM header
// opamp.clientCertHeader set by a TRUSTED proxy (server.trustedProxies),
// verified against the same CA file here.
func (s *Service) clientCert(r *http.Request) string {
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
		return certSubject(r.TLS.VerifiedChains[0][0])
	}
	hdr := s.opamp.ClientCertHeader
	if hdr == "" || s.clientCAs == nil || !s.ip.IsTrusted(realip.PeerIP(r)) {
		return ""
	}
	raw := r.Header.Get(hdr)
	if raw == "" {
		return ""
	}
	cert, err := parseForwardedCert(raw)
	if err != nil {
		s.log.Debug("ignoring forwarded client certificate", "error", err)
		return ""
	}
	opts := x509.VerifyOptions{Roots: s.clientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: s.now()}
	if _, err := cert.Verify(opts); err != nil {
		s.log.Warn("forwarded client certificate rejected", "error", err, "subject", certSubject(cert))
		return ""
	}
	return certSubject(cert)
}

func certSubject(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	return c.Subject.String()
}

// parseForwardedCert decodes the PEM certificate forwarded by a proxy: either
// the URL-encoded PEM (nginx $ssl_client_escaped_cert, ingress-nginx
// ssl-client-cert) or an Envoy/Istio style "Cert=\"<url-encoded pem>\"" element.
func parseForwardedCert(v string) (*x509.Certificate, error) {
	if i := strings.Index(v, `Cert="`); i >= 0 { // x-forwarded-client-cert
		v = v[i+len(`Cert="`):]
		if j := strings.IndexByte(v, '"'); j >= 0 {
			v = v[:j]
		}
	}
	if dec, err := url.QueryUnescape(v); err == nil {
		v = dec
	}
	// proxies often replace newlines with tabs/spaces
	v = strings.ReplaceAll(v, "\t", "\n")
	block, _ := pem.Decode([]byte(v))
	if block == nil {
		return nil, errors.New("no PEM block in forwarded certificate header")
	}
	return x509.ParseCertificate(block.Bytes)
}
