// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
	lru "github.com/myriarch/myriarch/internal/lru"
)

// Connection guards: per-IP rate limit, per-token connection cap, idle
// deadlines, credential binding of instance uids and re-validation of live
// connections against revoked or expired enrollment tokens.

// Variables so that tests can shorten them.
var (
	// firstMessageTimeout: a WebSocket connection must send its first message
	// within this time.
	firstMessageTimeout = 30 * time.Second
	// tokenRecheckInterval: established connections are re-validated against
	// their enrollment token this often (in addition to the revocation event).
	tokenRecheckInterval = time.Minute
)

const (
	// conflictDisconnectDelay lets the error response reach the agent before
	// the connection of a rejected session is closed.
	conflictDisconnectDelay = 500 * time.Millisecond
	// conflictEventInterval limits identity conflict events per agent.
	conflictEventInterval = time.Minute
)

// TokenChecker is implemented by the authenticator (*authn.Service); it lets
// the server re-validate the enrollment token of an established connection.
type TokenChecker interface {
	// EnrollmentTokenState returns domain.ErrUnauthorized when the token is
	// revoked, expired or gone and domain.ErrUnavailable on infrastructure errors.
	EnrollmentTokenState(ctx context.Context, id uuid.UUID) error
}

// guard holds the connection limiting state of one server.
type guard struct {
	ipMu  sync.Mutex
	ips   *lru.Cache[string, *rate.Limiter]
	ipLim rate.Limit
	ipBst int

	tokMu     sync.Mutex
	tokenConn map[uuid.UUID]int

	conflictMu   sync.Mutex
	conflictSeen map[domain.AgentID]time.Time
}

func newGuard(perIP float64) *guard {
	c, _ := lru.New[string, *rate.Limiter](16384)
	burst := int(2 * perIP)
	if burst < 1 {
		burst = 1
	}
	return &guard{
		ips: c, ipLim: rate.Limit(perIP), ipBst: burst,
		tokenConn: map[uuid.UUID]int{}, conflictSeen: map[domain.AgentID]time.Time{},
	}
}

// allowIP charges the per-client-IP bucket (the client address is the real
// address when the request came through a trusted proxy).
func (g *guard) allowIP(remoteAddr string) bool {
	if g.ipLim <= 0 {
		return true
	}
	// one bucket per IPv4 address and per IPv6 /64 (the same key as the authentication failure limits)
	host := realip.LimiterKey(hostOnly(remoteAddr))
	g.ipMu.Lock()
	defer g.ipMu.Unlock()
	l, ok := g.ips.Get(host)
	if !ok {
		l = rate.NewLimiter(g.ipLim, g.ipBst)
		g.ips.Add(host, l)
	}
	return l.Allow()
}

func (g *guard) tokenCount(id uuid.UUID) int {
	g.tokMu.Lock()
	defer g.tokMu.Unlock()
	return g.tokenConn[id]
}

// reserveToken counts one more WebSocket connection of the token; false if
// the cap (> 0) is reached.
func (g *guard) reserveToken(id uuid.UUID, max int) bool {
	g.tokMu.Lock()
	defer g.tokMu.Unlock()
	if max > 0 && g.tokenConn[id] >= max {
		return false
	}
	g.tokenConn[id]++
	return true
}

func (g *guard) releaseToken(id uuid.UUID) {
	g.tokMu.Lock()
	defer g.tokMu.Unlock()
	if g.tokenConn[id] <= 1 {
		delete(g.tokenConn, id)
		return
	}
	g.tokenConn[id]--
}

// conflictDue reports whether a conflict event for id should be written now.
func (g *guard) conflictDue(id domain.AgentID, now time.Time) bool {
	g.conflictMu.Lock()
	defer g.conflictMu.Unlock()
	if len(g.conflictSeen) > 10000 {
		for k, t := range g.conflictSeen {
			if now.Sub(t) > conflictEventInterval {
				delete(g.conflictSeen, k)
			}
		}
	}
	if t, ok := g.conflictSeen[id]; ok && now.Sub(t) < conflictEventInterval {
		return false
	}
	g.conflictSeen[id] = now
	return true
}

// idleTimeout is the read deadline of an established WebSocket connection:
// twice the offline threshold (agents send at least a heartbeat per 30s).
func (s *Server) idleTimeout() time.Duration { return 2 * s.cfg.OfflineAfter }

// extendDeadline moves the read deadline of a WebSocket connection.
func (ci *connInfo) extendDeadline(d time.Duration) {
	if ci.transport != transportWS || ci.conn == nil {
		return
	}
	if c := ci.conn.Connection(); c != nil {
		_ = c.SetReadDeadline(time.Now().Add(d))
	}
}

// ------------------------------------------------------ credential binding ---

// bindingConflict returns why cred may not use the instance uid of agent a
// ("" = fine). The uid is bound to the credential of its first registration:
// the enrollment token and, if one was presented, the client certificate.
func bindingConflict(a *domain.Agent, cred *authn.AgentCredential) string {
	if a == nil {
		return ""
	}
	if a.EnrollmentTokenID != nil {
		if cred == nil || cred.EnrollmentTokenID == nil || *cred.EnrollmentTokenID != *a.EnrollmentTokenID {
			return "token"
		}
	}
	if a.ClientCertSubject != "" {
		if cred == nil || cred.ClientCertSubject != a.ClientCertSubject {
			return "certificate"
		}
	}
	return ""
}

// checkBinding enforces the credential binding for an established or
// freshly loaded agent. If opamp.autoRebindOnTokenRotation is on and the bound
// enrollment token was revoked or expired (token rotation) the agent is
// re-bound to the new credential; otherwise (and for any other mismatch) the
// connection is rejected and an admin re-enrolls the agent
// (POST /agents/{id}/actions/reenroll): the returned response must be sent and nothing else
// may happen for this message.
func (s *Server) checkBinding(ctx context.Context, sess *session, ci *connInfo, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	a := sess.agent
	why := bindingConflict(a, ci.cred)
	if why == "" {
		return nil
	}
	rotation := s.cfg.AutoRebindOnTokenRotation && why == "token" && ci.cred != nil && ci.cred.EnrollmentTokenID != nil && ci.cred.ClientCertSubject == a.ClientCertSubject
	if rotation {
		if tc, ok := s.deps.Auth.(TokenChecker); ok {
			err := tc.EnrollmentTokenState(ctx, *a.EnrollmentTokenID)
			if errors.Is(err, domain.ErrUnauthorized) {
				// the old token is gone: the agent moves to the credential it presents now
				old := *a.EnrollmentTokenID
				if err := s.deps.Store.Agents().SetCredentialBinding(ctx, sess.id, ci.cred.EnrollmentTokenID, ci.cred.ClientCertSubject); err != nil {
					s.log.Warn("cannot re-bind agent after token rotation", "agent_id", sess.id, "error", err)
					return s.rejectConflict(sess, ci, msg, "binding update failed")
				}
				a.EnrollmentTokenID = ci.cred.EnrollmentTokenID
				details := map[string]any{"previousTokenId": old.String(), "tokenId": ci.cred.EnrollmentTokenID.String(), "remoteAddr": ci.remoteAddr}
				s.emit(sess.id, domain.AgentEventIdentityConflict, "warning",
					"agent re-enrolled with a new enrollment token (the previous token is revoked or expired)", details)
				s.auditEvent(audit.System(domain.ActorAgent, "agent.rebind", "agent", sess.id.String(), details))
				return nil
			}
		}
	}
	return s.rejectConflict(sess, ci, msg, why)
}

// rejectConflict answers a message of a connection that presents the wrong
// credential for an instance uid: error response, warning event and audit
// entry (rate limited per agent), and the connection is closed shortly after.
func (s *Server) rejectConflict(sess *session, ci *connInfo, msg *protobufs.AgentToServer, why string) *protobufs.ServerToAgent {
	s.m.rejected.WithLabelValues("identity_conflict").Inc()
	if s.guard.conflictDue(sess.id, s.now()) {
		details := map[string]any{"mismatch": why, "remoteAddr": ci.remoteAddr, "transport": ci.transport}
		if ci.cred != nil && ci.cred.EnrollmentTokenID != nil {
			details["presentedTokenId"] = ci.cred.EnrollmentTokenID.String()
		}
		s.log.Warn("connection presents another credential than the one the instance uid is bound to; rejected",
			"agent_id", sess.id, "mismatch", why, "remote", ci.remoteAddr)
		s.emit(sess.id, domain.AgentEventIdentityConflict, "warning",
			"a connection with another credential tried to use this instance uid and was rejected ("+why+" mismatch)", details)
		ev := audit.System(domain.ActorAgent, "agent.identity_conflict", "agent", sess.id.String(), details)
		ev.Result = "failure"
		ev.SourceIP = hostOnly(ci.remoteAddr)
		s.auditEvent(ev)
	}
	ci.block()
	return errorResponse(msg.InstanceUid, protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest,
		"the instance uid is bound to another credential", 0)
}

// block marks the connection as rejected (all further messages are refused)
// and closes it shortly after the current response was sent.
func (ci *connInfo) block() {
	if ci.blocked.Swap(true) {
		return
	}
	if ci.transport == transportWS {
		time.AfterFunc(conflictDisconnectDelay, ci.disconnect)
	}
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func (s *Server) auditEvent(e domain.AuditEvent) {
	if s.deps.Audit == nil {
		return
	}
	s.deps.Audit.Record(context.Background(), e)
}

// pinUID enforces that a WebSocket connection keeps the instance uid of its
// first message. A change is only accepted to a uid the server issued on this
// connection (duplicate uid handling). It returns false if the message must be
// refused.
func (ci *connInfo) pinUID(id domain.AgentID) bool {
	if ci.transport != transportWS {
		return true
	}
	ci.mu.Lock()
	defer ci.mu.Unlock()
	switch {
	case ci.pinned == (domain.AgentID{}):
		ci.pinned = id
	case ci.pinned != id:
		if _, ok := ci.issued[id]; !ok {
			return false
		}
		delete(ci.issued, id)
		ci.pinned = id
	}
	return true
}

func (ci *connInfo) issueUID(id domain.AgentID) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	if ci.issued == nil {
		ci.issued = map[domain.AgentID]struct{}{}
	}
	ci.issued[id] = struct{}{}
}

// ------------------------------------------------------ token re-validation ---

// runTokenWatcher disconnects agent sessions whose enrollment token was
// revoked (bus event, immediately) or expired/revoked unnoticed (periodic
// check every minute).
func (s *Server) runTokenWatcher(ctx context.Context) {
	ch, cancel := s.deps.Bus.Subscribe(eventbus.EnrollmentTokenRevoked, eventbus.BusReconnected)
	defer cancel()
	t := time.NewTicker(s.tokenRecheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type == eventbus.BusReconnected {
				s.recheckTokens(ctx)
				continue
			}
			var p eventbus.EnrollmentTokenPayload
			if err := ev.Decode(&p); err != nil {
				continue
			}
			reason := p.Reason
			if reason == "" {
				reason = "revoked"
			}
			s.disconnectToken(p.TokenID, reason)
		case <-t.C:
			s.recheckTokens(ctx)
		}
	}
}

// recheckTokens validates the enrollment token of every live session once.
func (s *Server) recheckTokens(ctx context.Context) {
	tc, ok := s.deps.Auth.(TokenChecker)
	if !ok {
		return
	}
	for id := range s.liveTokenIDs() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := tc.EnrollmentTokenState(cctx, id)
		cancel()
		if errors.Is(err, domain.ErrUnauthorized) {
			s.disconnectToken(id, "revoked or expired")
		}
	}
}

func (s *Server) liveTokenIDs() map[uuid.UUID]struct{} {
	ids := map[uuid.UUID]struct{}{}
	s.mu.RLock()
	for _, sess := range s.sessions {
		if sess.ci != nil && sess.ci.cred != nil && sess.ci.cred.EnrollmentTokenID != nil {
			ids[*sess.ci.cred.EnrollmentTokenID] = struct{}{}
		}
	}
	s.mu.RUnlock()
	s.httpMu.Lock()
	for _, e := range s.httpSessions {
		if c := e.sess.cred.Load(); c != nil && c.EnrollmentTokenID != nil {
			ids[*c.EnrollmentTokenID] = struct{}{}
		}
	}
	s.httpMu.Unlock()
	return ids
}

// disconnectToken closes the WebSocket connections and drops the pooled HTTP
// sessions that authenticated with the token; each forced disconnect is audited.
func (s *Server) disconnectToken(id uuid.UUID, reason string) {
	var ws []*session
	s.mu.RLock()
	for _, sess := range s.sessions {
		if sess.ci != nil && sess.ci.cred != nil && sess.ci.cred.EnrollmentTokenID != nil && *sess.ci.cred.EnrollmentTokenID == id {
			ws = append(ws, sess)
		}
	}
	s.mu.RUnlock()
	var httpDropped []domain.AgentID
	s.httpMu.Lock()
	for aid, e := range s.httpSessions {
		if c := e.sess.cred.Load(); c != nil && c.EnrollmentTokenID != nil && *c.EnrollmentTokenID == id {
			delete(s.httpSessions, aid)
			httpDropped = append(httpDropped, aid)
		}
	}
	s.httpMu.Unlock()
	if len(ws)+len(httpDropped) == 0 {
		return
	}
	s.log.Warn("enrollment token no longer valid: disconnecting its agents", "token", id, "reason", reason, "connections", len(ws), "http_sessions", len(httpDropped))
	for _, sess := range ws {
		details := map[string]any{"reason": "enrollment_token_" + reasonKey(reason), "tokenId": id.String(), "transport": transportWS}
		s.emit(sess.id, domain.AgentEventDisconnected, "warning", fmt.Sprintf("connection closed: enrollment token %s", reason), details)
		s.auditEvent(audit.System(domain.ActorSystem, "agent.disconnect", "agent", sess.id.String(), details))
		s.m.rejected.WithLabelValues("token_revoked").Inc()
		sess.ci.disconnect()
	}
	for _, aid := range httpDropped {
		details := map[string]any{"reason": "enrollment_token_" + reasonKey(reason), "tokenId": id.String(), "transport": transportHTTP}
		s.auditEvent(audit.System(domain.ActorSystem, "agent.disconnect", "agent", aid.String(), details))
	}
}

func reasonKey(r string) string {
	if r == "expired" {
		return "expired"
	}
	return "revoked"
}
