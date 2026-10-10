// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
	"golang.org/x/time/rate"

	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

const (
	transportWS   = "websocket"
	transportHTTP = "http"
)

// connInfo is the per-connection state (one per WebSocket connection and per
// plain HTTP request).
type connInfo struct {
	srv         *Server
	transport   string
	remoteAddr  string
	cred        *authn.AgentCredential
	connectedAt time.Time
	conn        types.Connection

	mu      sync.Mutex
	sess    *session // WebSocket: the registered session (nil until established)
	counted bool
	closed  bool
	// pinned is the instance uid of the first message of a WebSocket
	// connection; issued holds uids the server assigned on this connection.
	pinned domain.AgentID
	issued map[domain.AgentID]struct{}
	// tokenHeld: the connection counts against its token's connection cap.
	tokenHeld bool
	// blocked: the connection presented a wrong credential or switched
	// identity; every further message is refused.
	blocked atomic.Bool
}

// session is the server side state of one agent as seen through one
// connection (WebSocket: bound to the connection; HTTP: pooled per agent so
// that sequence numbers and the cached desired state survive across polls).
type session struct {
	srv *Server
	id  domain.AgentID

	mu          sync.Mutex // serializes message processing and pushes
	ci          *connInfo  // WebSocket connection (nil for pooled HTTP sessions)
	transport   string
	established bool
	offlineDone bool
	// cred is the credential of the latest message (pooled HTTP sessions:
	// used to drop sessions of revoked enrollment tokens).
	cred atomic.Pointer[authn.AgentCredential]
	// storeBudget limits how often one agent can make the server store a
	// new effective config or component set.
	storeBudget     *rate.Limiter
	oversizedConfig domain.Hash
	reservedWarned  bool

	agent       *domain.Agent // cached state (nil: agent unknown to the database)
	refreshedAt time.Time
	onlineAt    time.Time

	offers          []domain.PackageOffer
	offersHash      domain.Hash
	reportedPkgHash domain.Hash
	// sentOfferCreated is the creation time of the newest package offer sent on this
	// connection (see retryOffer).
	sentOfferCreated time.Time

	lastSeq uint64
	hasSeq  bool

	resync          bool
	forceFullState  bool
	offeredConfig   domain.Hash
	offeredConfigAt time.Time
	offeredPkgs     domain.Hash
	offeredPkgsAt   time.Time

	// asynchronous requests from the bus (set without holding mu)
	stale       atomic.Bool
	pushPending atomic.Bool
	cmdMu       sync.Mutex
	cmds        []eventbus.AgentCommandPayload
}

func (s *Server) newSession(id domain.AgentID, transport string) *session {
	// one new effective config / component set per 10s on average, bursts of 6
	return &session{srv: s, id: id, transport: transport, storeBudget: rate.NewLimiter(rate.Every(10*time.Second), 6)}
}

// ------------------------------------------------------------ callbacks ---

func (ci *connInfo) onConnected(_ context.Context, conn types.Connection) {
	ci.conn = conn
	s := ci.srv
	ci.mu.Lock()
	ci.counted = true
	ci.mu.Unlock()
	if ci.transport == transportWS {
		s.wsCount.Add(1)
		if ci.cred != nil && ci.cred.EnrollmentTokenID != nil {
			s.guard.reserveToken(*ci.cred.EnrollmentTokenID, 0)
			ci.mu.Lock()
			ci.tokenHeld = true
			ci.mu.Unlock()
		}
		// a connection that never sends anything must not keep its slot
		ci.extendDeadline(s.firstMsgTimeout)
	}
	s.m.connections.WithLabelValues(ci.transport).Inc()
}

func (ci *connInfo) onMessage(_ context.Context, _ types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	ci.extendDeadline(ci.srv.idleTimeout())
	return ci.srv.handleMessage(ci, msg)
}

func (ci *connInfo) onReadError(_ types.Connection, mt int, _ []byte, err error) {
	ci.srv.log.Debug("cannot read agent message", "error", err, "remote", ci.remoteAddr, "message_type", mt)
}

func (ci *connInfo) onResponseError(_ types.Connection, _ *protobufs.ServerToAgent, err error) {
	ci.srv.log.Warn("cannot send response to agent", "error", err, "remote", ci.remoteAddr)
}

func (ci *connInfo) onClose(_ types.Connection) {
	s := ci.srv
	ci.mu.Lock()
	sess := ci.sess
	counted := ci.counted
	tokenHeld := ci.tokenHeld
	ci.closed = true
	ci.counted = false
	ci.tokenHeld = false
	ci.mu.Unlock()
	if tokenHeld && ci.cred != nil && ci.cred.EnrollmentTokenID != nil {
		s.guard.releaseToken(*ci.cred.EnrollmentTokenID)
	}
	if counted {
		if ci.transport == transportWS {
			s.wsCount.Add(-1)
		}
		s.m.connections.WithLabelValues(ci.transport).Dec()
	}
	if ci.transport != transportWS || sess == nil {
		return // HTTP polls never cause an offline transition (leader sweeper)
	}
	s.mu.Lock()
	current := s.sessions[sess.id] == sess
	if current {
		delete(s.sessions, sess.id)
	}
	s.mu.Unlock()
	if !current || s.shuttingDown.Load() {
		return // replaced by a newer connection / bulk offline marking in Shutdown
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	s.markOffline(ctx, sess, "connection closed")
}

func (ci *connInfo) disconnect() {
	if ci.conn != nil {
		_ = ci.conn.Disconnect()
	}
}

// ----------------------------------------------------------- messaging ---

func parseUID(b []byte) (domain.AgentID, error) {
	id, err := domain.AgentIDFromBytes(b)
	if err != nil {
		return id, err
	}
	if id == (domain.AgentID{}) {
		return id, fmt.Errorf("%w: instance_uid must not be the nil UUID", domain.ErrInvalidArgument)
	}
	return id, nil
}

func errorResponse(uid []byte, typ protobufs.ServerErrorResponseType, msg string, retry time.Duration) *protobufs.ServerToAgent {
	er := &protobufs.ServerErrorResponse{Type: typ, ErrorMessage: msg}
	if retry > 0 {
		er.Details = &protobufs.ServerErrorResponse_RetryInfo{RetryInfo: &protobufs.RetryInfo{RetryAfterNanoseconds: uint64(retry)}}
	}
	return &protobufs.ServerToAgent{InstanceUid: uid, ErrorResponse: er}
}

// handleMessage processes one AgentToServer message (docs/design/opamp-server.md,
// OpAMP server > Message processing; the order is binding) and returns the
// reply.
func (s *Server) handleMessage(ci *connInfo, msg *protobufs.AgentToServer) (resp *protobufs.ServerToAgent) {
	start := time.Now()
	s.m.messages.WithLabelValues("in").Inc()
	defer func() {
		s.m.duration.Observe(time.Since(start).Seconds())
		if resp != nil {
			s.m.messages.WithLabelValues("out").Inc()
		}
	}()

	if ci.blocked.Load() {
		return errorResponse(msg.InstanceUid, protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest, "connection rejected", 0)
	}
	id, err := parseUID(msg.InstanceUid)
	if err != nil {
		return errorResponse(msg.InstanceUid, protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest, err.Error(), 0)
	}
	// A WebSocket connection speaks for exactly one instance uid (plus uids the
	// server issues on it): it cannot be used to register or take over others.
	if !ci.pinUID(id) {
		s.m.rejected.WithLabelValues("uid_switch").Inc()
		s.log.Warn("connection switched its instance uid; closing", "agent_id", id, "remote", ci.remoteAddr)
		ci.block()
		return errorResponse(msg.InstanceUid, protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest,
			"a connection must keep its instance uid", 0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
	defer cancel()

	sess, fresh := s.lookupSession(ci, id)
	sess.mu.Lock()
	defer sess.mu.Unlock()

	if fresh && ci.transport == transportWS {
		// the connection switched to another instance uid: the old one is gone
		ci.mu.Lock()
		old := ci.sess
		ci.sess = nil
		ci.mu.Unlock()
		if old != nil && old != sess {
			go func() {
				c, cc := context.WithTimeout(context.Background(), closeTimeout)
				defer cc()
				old.mu.Lock()
				defer old.mu.Unlock()
				s.unregister(old)
				s.markOffline(c, old, "agent changed its instance uid")
			}()
		}
	}

	if !sess.established {
		clone, err := s.establish(ctx, sess, ci, msg)
		if err != nil {
			s.log.Warn("cannot establish agent session", "agent_id", id, "error", err)
			if ci.transport == transportHTTP {
				s.dropHTTPSession(sess)
			}
			return errorResponse(msg.InstanceUid, protobufs.ServerErrorResponseType_ServerErrorResponseType_Unavailable, "temporarily unavailable", 10*time.Second)
		}
		if clone != nil {
			return clone
		}
	} else {
		if sess.stale.Swap(false) || s.now().Sub(sess.refreshedAt) > viewRefreshAfter {
			_ = s.refresh(ctx, sess)
		}
		// pooled HTTP sessions are shared by all polls of the uid: every poll
		// must present the credential the uid is bound to
		if resp := s.checkBinding(ctx, sess, ci, msg); resp != nil {
			return resp
		}
	}
	// recorded only for a credential the uid is bound to: a rejected poll must
	// not replace the credential the pooled session is tracked with
	sess.cred.Store(ci.cred)
	if ci.transport == transportHTTP {
		s.touchHTTP(ctx, sess, ci)
		if s.drainHTTPCommands(sess) {
			s.touchLastSeen(id)
			return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid, Command: &protobufs.ServerToAgentCommand{Type: protobufs.CommandType_CommandType_Restart}}
		}
	}

	// ---- 3. agent_disconnect
	if msg.AgentDisconnect != nil {
		if ci.transport == transportWS {
			s.unregister(sess)
		}
		s.markOffline(ctx, sess, "agent disconnected")
		return nil
	}

	// ---- 4. last seen (flushed in batches)
	s.touchLastSeen(id)

	// ---- 5. full state needed?
	first := !sess.hasSeq
	known := sess.agent != nil
	seqGap := sess.hasSeq && msg.SequenceNum != sess.lastSeq+1
	needFull := msg.AgentDescription == nil && (!known || first || seqGap)
	sess.lastSeq, sess.hasSeq = msg.SequenceNum, true
	if sess.forceFullState {
		needFull = true
		sess.forceFullState = false
	}
	if sess.agent != nil && msg.Capabilities != 0 && msg.Capabilities != sess.agent.Capabilities && msg.AgentDescription == nil {
		sess.agent.Capabilities = msg.Capabilities
	}

	pr := &processResult{}
	if err := s.processMessage(ctx, sess, ci, msg, pr); err != nil {
		if errors.Is(err, errAgentUnknown) {
			// unknown agent without description: ask for the full state, store nothing
			return s.reconcile(sess, uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState))
		}
		s.log.Warn("processing agent message failed", "agent_id", id, "error", err)
		return errorResponse(msg.InstanceUid, protobufs.ServerErrorResponseType_ServerErrorResponseType_Unavailable, "temporarily unavailable", 10*time.Second)
	}
	if len(pr.changes) > 0 {
		s.publishState(sess, pr.changes)
	}

	var flags uint64
	if needFull {
		flags |= uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState)
	}
	if pr.reportComponents {
		flags |= uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportAvailableComponents)
	}
	return s.reconcile(sess, flags)
}

// lookupSession returns the session for a message. fresh is true if a new
// (not yet established) session was created.
func (s *Server) lookupSession(ci *connInfo, id domain.AgentID) (sess *session, fresh bool) {
	if ci.transport == transportHTTP {
		return s.httpSession(id)
	}
	ci.mu.Lock()
	cur := ci.sess
	ci.mu.Unlock()
	if cur != nil && cur.id == id {
		return cur, false
	}
	return s.newSession(id, transportWS), true
}

var errAgentUnknown = errors.New("agent unknown")

// establish loads the agent, detects duplicate instance uids, registers the
// WebSocket session and marks the agent online. If it returns a non-nil
// response the message must be answered with it and nothing else happens
// (duplicate instance uid).
func (s *Server) establish(ctx context.Context, sess *session, ci *connInfo, msg *protobufs.AgentToServer) (*protobufs.ServerToAgent, error) {
	a, err := s.deps.Store.Agents().Get(ctx, sess.id)
	switch {
	case err == nil:
		sess.agent = a
	case errors.Is(err, domain.ErrNotFound):
		sess.agent = nil
	default:
		return nil, fmt.Errorf("load agent: %w", err)
	}
	now := s.now()
	sess.refreshedAt = now

	// The instance uid is bound to the credential of its first registration.
	if sess.agent != nil {
		if resp := s.checkBinding(ctx, sess, ci, msg); resp != nil {
			return resp, nil
		}
	}

	// Duplicate instance uids (cloned images): the same uid with a different
	// host.name while the original is online gets a fresh uid.
	if sess.agent != nil && msg.AgentDescription != nil {
		if resp := s.detectClone(sess, ci, msg); resp != nil {
			return resp, nil
		}
	}

	if ci.transport == transportWS {
		sess.ci = ci // immutable from here on: readers get the session through s.mu
		s.register(sess, ci)
	}
	sess.established = true
	if sess.agent != nil {
		if err := s.loadOffers(ctx, sess); err != nil {
			s.log.Warn("cannot load package offers", "agent_id", sess.id, "error", err)
		}
		if err := s.markOnline(ctx, sess, ci); err != nil {
			s.log.Warn("cannot mark agent online", "agent_id", sess.id, "error", err)
		}
	}
	return nil, nil
}

func hostOf(attrs map[string]string, id map[string]string) string {
	if v := id[domain.AttrHostName]; v != "" {
		return v
	}
	return attrs[domain.AttrHostName]
}

// detectClone returns a response assigning a new instance uid if the message
// comes from a different host than the agent currently online with this uid.
func (s *Server) detectClone(sess *session, ci *connInfo, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	a := sess.agent
	newHost := hostOf(attributesToMap(msg.AgentDescription.GetNonIdentifyingAttributes()), attributesToMap(msg.AgentDescription.GetIdentifyingAttributes()))
	oldHost := a.Hostname
	if newHost == "" || oldHost == "" || newHost == oldHost {
		return nil
	}
	lastSeen := a.Connection.LastSeenAt
	if !a.IsOnline() || lastSeen == nil || s.now().Sub(*lastSeen) > s.cfg.OfflineAfter {
		return nil // the original is gone (host renamed or replaced): same agent
	}
	newUID := uuid.Must(uuid.NewV7())
	ci.issueUID(newUID)
	s.log.Warn("duplicate instance uid detected (cloned image?), assigning a new uid",
		"agent_id", sess.id, "existing_host", oldHost, "new_host", newHost, "remote", ci.remoteAddr, "new_instance_uid", newUID)
	s.emit(sess.id, domain.AgentEventIdentityConflict, "warning",
		fmt.Sprintf("another agent with the same instance uid connected from host %q; it was assigned a new uid", newHost),
		map[string]any{"existingHost": oldHost, "newHost": newHost, "remoteAddr": ci.remoteAddr, "newInstanceUid": newUID.String()})
	return &protobufs.ServerToAgent{
		InstanceUid:         msg.InstanceUid,
		Capabilities:        serverCapabilities,
		AgentIdentification: &protobufs.AgentIdentification{NewInstanceUid: newUID[:]},
		Flags:               uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState),
	}
}

// register makes sess the current WebSocket session of the agent; an older
// connection for the same uid is closed.
func (s *Server) register(sess *session, ci *connInfo) {
	s.mu.Lock()
	old := s.sessions[sess.id]
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	ci.mu.Lock()
	ci.sess = sess
	ci.mu.Unlock()
	if old != nil && old != sess && old.ci != nil && old.ci != ci {
		s.log.Info("closing stale connection of the same agent", "agent_id", sess.id)
		s.emit(sess.id, domain.AgentEventIdentityConflict, "info", "a new connection for this agent replaced the previous one",
			map[string]any{"remoteAddr": ci.remoteAddr})
		old.ci.disconnect()
	}
}

func (s *Server) unregister(sess *session) {
	s.mu.Lock()
	if s.sessions[sess.id] == sess {
		delete(s.sessions, sess.id)
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------- HTTP pool ---

// drainHTTPCommands applies queued commands of a polling agent (HTTP agents
// cannot be pushed to). It returns true if a restart command must be answered
// as the only content of this response.
func (s *Server) drainHTTPCommands(sess *session) (restart bool) {
	sess.cmdMu.Lock()
	cmds := sess.cmds
	sess.cmds = nil
	sess.cmdMu.Unlock()
	for _, c := range cmds {
		switch c.Command {
		case eventbus.CommandRestart:
			if sess.agent != nil && domain.HasCapability(sess.agent.Capabilities, domain.CapAcceptsRestartCommand) {
				restart = true
				s.emit(sess.id, domain.AgentEventCommand, "info", "restart requested"+requestedBy(c), map[string]any{"command": "restart", "requestedBy": c.RequestedBy})
			} else {
				s.emit(sess.id, domain.AgentEventCommand, "warning", "restart not supported by this agent", map[string]any{"command": "restart", "requestedBy": c.RequestedBy})
			}
		case eventbus.CommandReportFullState:
			sess.forceFullState = true
		case eventbus.CommandResync:
			if err := s.deps.Store.Agents().ClearFailedConfig(context.Background(), sess.id); err != nil && !errors.Is(err, domain.ErrNotFound) {
				s.log.Warn("cannot clear failed config for resync", "agent_id", sess.id, "error", err)
			}
			sess.stale.Store(true)
			sess.resync = true
			sess.forceFullState = true
		}
	}
	return restart
}

func (s *Server) httpSession(id domain.AgentID) (*session, bool) {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	e := s.httpSessions[id]
	if e == nil {
		if len(s.httpSessions) >= s.cfg.MaxConnectionsPerReplica {
			s.evictHTTPSessionsLocked()
		}
		e = &httpEntry{sess: s.newSession(id, transportHTTP)}
		s.httpSessions[id] = e
		e.lastUsed = s.now()
		return e.sess, true
	}
	e.lastUsed = s.now()
	return e.sess, false
}

// evictHTTPSessionsLocked removes the least recently used 1/16 of the pooled
// HTTP sessions (the pool is bounded by maxConnectionsPerReplica; evicted
// agents simply re-establish their session with their next poll). The caller
// holds httpMu.
func (s *Server) evictHTTPSessionsLocked() {
	type item struct {
		id   domain.AgentID
		used time.Time
	}
	items := make([]item, 0, len(s.httpSessions))
	for id, e := range s.httpSessions {
		items = append(items, item{id, e.lastUsed})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].used.Before(items[j].used) })
	n := len(items)/16 + 1
	for _, it := range items[:min(n, len(items))] {
		delete(s.httpSessions, it.id)
	}
	s.m.dropped.WithLabelValues("http_sessions").Add(float64(n))
}

func (s *Server) dropHTTPSession(sess *session) {
	s.httpMu.Lock()
	if e := s.httpSessions[sess.id]; e != nil && e.sess == sess {
		delete(s.httpSessions, sess.id)
	}
	s.httpMu.Unlock()
}

// touchHTTP re-asserts the online state of polling agents (they never hold a
// connection, so the leader sweeper may have flipped them to offline).
func (s *Server) touchHTTP(ctx context.Context, sess *session, ci *connInfo) {
	if sess.agent == nil {
		return
	}
	if s.now().Sub(sess.onlineAt) > s.cfg.OfflineAfter/3 || !sess.agent.IsOnline() {
		if err := s.markOnline(ctx, sess, ci); err != nil {
			s.log.Warn("cannot mark polling agent online", "agent_id", sess.id, "error", err)
		}
	}
}

// ------------------------------------------------------ state refresh ---

// refresh reloads the cached state from the database.
func (s *Server) refresh(ctx context.Context, sess *session) error {
	a, err := s.deps.Store.Agents().Get(ctx, sess.id)
	switch {
	case err == nil:
		sess.agent = a
	case errors.Is(err, domain.ErrNotFound):
		sess.agent = nil
	default:
		return err
	}
	sess.refreshedAt = s.now()
	if err := s.loadOffers(ctx, sess); err != nil {
		return err
	}
	// self healing: the leader sweeper may have marked a live connection offline
	if sess.agent != nil && sess.ci != nil && !sess.agent.IsOnline() && !sess.offlineDone {
		if err := s.markOnline(ctx, sess, sess.ci); err != nil {
			return err
		}
	}
	return nil
}

// loadOffers reads the package offers of updaters (cached per session).
func (s *Server) loadOffers(ctx context.Context, sess *session) error {
	a := sess.agent
	if a == nil || !domain.HasCapability(a.Capabilities, domain.CapAcceptsPackages) {
		sess.offers, sess.offersHash = nil, nil
		return nil
	}
	offers, err := s.deps.Store.Packages().GetOffers(ctx, sess.id)
	if err != nil {
		return err
	}
	sess.offers = offers
	sess.offersHash = nil
	if len(offers) > 0 {
		sess.offersHash = domain.AllPackagesHash(offers)
	}
	return nil
}

// ------------------------------------------------------ connection state ---

// markOnline writes the connection state and records the connected event.
func (s *Server) markOnline(ctx context.Context, sess *session, ci *connInfo) error {
	a := sess.agent
	if a == nil {
		return nil
	}
	now := s.now()
	conn := domain.AgentConnection{Status: domain.ConnectionOnline, Transport: ci.transport, RemoteAddr: ci.remoteAddr, ConnectedAt: &now, LastSeenAt: &now}
	if ci.transport == transportWS {
		conn.ReplicaID = s.deps.ReplicaID
	}
	prev := a.Connection.Status
	if err := s.deps.Store.Agents().SetConnection(ctx, sess.id, conn); err != nil {
		return err
	}
	a.Connection = conn
	sess.onlineAt = now
	sess.offlineDone = false
	if prev != domain.ConnectionOnline {
		s.emit(sess.id, domain.AgentEventConnected, "info", fmt.Sprintf("connected via %s from %s", ci.transport, ci.remoteAddr),
			map[string]any{"transport": ci.transport, "remoteAddr": ci.remoteAddr, "replica": s.deps.ReplicaID})
		s.publishState(sess, []string{"connection"})
	}
	return nil
}

// markOffline writes the offline state (conditional on this replica owning
// the connection), records the event and notifies listeners. The caller holds
// sess.mu.
func (s *Server) markOffline(ctx context.Context, sess *session, reason string) {
	if sess.offlineDone {
		return
	}
	sess.offlineDone = true
	lastSeen, hasLastSeen := s.popLastSeen(sess.id)
	a := sess.agent
	if a == nil {
		return
	}
	now := s.now()
	conn := domain.AgentConnection{Status: domain.ConnectionOffline, Transport: a.Connection.Transport, RemoteAddr: a.Connection.RemoteAddr, DisconnectedAt: &now}
	if sess.transport == transportWS {
		conn.ReplicaID = s.deps.ReplicaID // conditional write: only if still owned by this replica
	}
	if hasLastSeen {
		conn.LastSeenAt = &lastSeen
	}
	if err := s.deps.Store.Agents().SetConnection(ctx, sess.id, conn); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return // agent deleted meanwhile
		}
		s.log.Warn("cannot mark agent offline", "agent_id", sess.id, "error", err)
		return
	}
	a.Connection.Status, a.Connection.ReplicaID, a.Connection.DisconnectedAt = domain.ConnectionOffline, "", &now
	s.emit(sess.id, domain.AgentEventDisconnected, "info", "disconnected: "+reason, map[string]any{"reason": reason, "replica": s.deps.ReplicaID})
	s.publishState(sess, []string{"connection"})
}

// ------------------------------------------------------------- events ---

// emit queues an agent event (written in batches by the event writer).
func (s *Server) emit(id domain.AgentID, typ domain.AgentEventType, severity, message string, details map[string]any) {
	ev := domain.AgentEvent{AgentID: id, Time: s.now(), Type: typ, Severity: severity, Message: message, Details: details}
	select {
	case s.events <- ev:
	default:
		s.m.dropped.WithLabelValues("events").Inc()
	}
}

// publishState queues an agent.state_changed bus event.
func (s *Server) publishState(sess *session, changes []string) {
	p := eventbus.AgentStateChangedPayload{AgentID: sess.id, Changes: changes}
	if a := sess.agent; a != nil {
		p.Online, p.Version = a.IsOnline(), a.Version
		if a.Health != nil {
			h := a.Health.Healthy
			p.Healthy = &h
		}
	}
	s.publish(eventbus.AgentStateChanged, p)
}

func (s *Server) publish(typ eventbus.Type, payload any) {
	select {
	case s.pubq <- pubItem{typ: typ, payload: payload}:
	default:
		s.m.dropped.WithLabelValues("publish").Inc()
	}
}

// ---------------------------------------------------------- last seen ---

func (s *Server) touchLastSeen(id domain.AgentID) {
	now := s.now()
	s.lastSeenMu.Lock()
	s.lastSeen[id] = now
	s.lastSeenMu.Unlock()
}

// popLastSeen removes and returns the pending entry of id (its value is
// written together with the offline state).
func (s *Server) popLastSeen(id domain.AgentID) (time.Time, bool) {
	s.lastSeenMu.Lock()
	defer s.lastSeenMu.Unlock()
	t, ok := s.lastSeen[id]
	delete(s.lastSeen, id)
	return t, ok
}
