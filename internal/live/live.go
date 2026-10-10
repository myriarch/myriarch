// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package live implements the Server-Sent Events endpoint
// GET /api/v1/events/stream (operationId streamEvents) for live UI updates.
//
// Protocol (documented in api/openapi.yaml): text/event-stream, one event
// per change: "event: <type>\nid: <monotonic id>\ndata: <json>\n\n", types in
// internal/api/dto (LiveAgentUpdated, LiveFleetUpdated, ...). A comment line ": ping" every
// 15s keeps proxies from timing out. Agent updates are coalesced per agent
// (max 1 per second per agent). If a client's buffer overflows, it receives
// "resync" and must refetch. Headers: Cache-Control: no-cache,
// X-Accel-Buffering: no.
//
// Security: the stream is authenticated when it opens (middleware) and its
// credential is re-validated on every ping and immediately after an
// auth.invalidate event (logout, revocation, disabled user, role change); a
// stream whose credential is no longer valid is closed. Streams live at most
// MaxStreamLifetime, so EventSource reconnects through the middleware. Every
// event type has a declared permission (eventPermissions) checked per client,
// and one principal may hold at most MaxStreamsPerPrincipal streams.
//
// The hub subscribes to the cluster event bus, so every replica serves its own
// UI clients with events from all replicas. Events are only hints: the
// payloads are loaded from the store when they are sent.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/myriarch/myriarch/internal/actorname"
	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/coreapi"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

// Tunables (variables so that tests can shorten them).
var (
	// CoalesceInterval is the window in which updates of one agent or rollout are merged.
	CoalesceInterval = time.Second
	// StatsInterval is the minimum interval between stats.changed hints.
	StatsInterval = 5 * time.Second
	// PingInterval between ": ping" comments.
	PingInterval = 15 * time.Second
	// WriteTimeout bounds a single write to a client (a hung browser must not pin a goroutine).
	WriteTimeout = 10 * time.Second
	// MaxStreamLifetime ends a stream after this time (the browser reconnects).
	MaxStreamLifetime = time.Hour
	// MaxStreamsPerPrincipal limits concurrent streams of one user or token.
	MaxStreamsPerPrincipal = 10
	// recheckDelay lets the authentication caches of this replica process an
	// invalidation event before the credential is checked again.
	recheckDelay = 250 * time.Millisecond
)

// Authenticator re-validates the credential of an open stream
// (*authn.Service; results are cached for 5s).
type Authenticator interface {
	Authenticate(r *http.Request) (*authz.Principal, error)
}

// eventPermissions maps every event type to the permission needed to receive
// it. Unknown event types are delivered to nobody.
var eventPermissions = map[string]authz.Permission{
	dto.LiveAgentUpdated:   authz.PermRead,
	dto.LiveAgentRemoved:   authz.PermRead,
	dto.LiveRolloutUpdated: authz.PermRead,
	dto.LiveFleetUpdated:   authz.PermRead,
	dto.LiveGitOpsSynced:   authz.PermRead,
	dto.LiveReleaseNew:     authz.PermRead,
	dto.LiveStatsChanged:   authz.PermRead,
	dto.LiveResync:         authz.PermRead,
}

const (
	clientBuffer = 256
	maxClients   = 2000
	agentBatch   = 200
)

// Hub fans out bus events to SSE clients.
type Hub struct {
	bus eventbus.Bus
	st  store.Store
	log *slog.Logger

	nextID atomic.Uint64

	mu       sync.Mutex
	clients  map[*client]struct{}
	perOwner map[string]int
	auth     Authenticator

	doneOnce sync.Once
	done     chan struct{} // closed when Run ends: streams terminate

	latestMu  sync.Mutex
	latestVal string
	latestAt  time.Time
}

type client struct {
	ch chan []byte
	// principal of the stream (nil only in tests without authentication).
	principal *authz.Principal
	// recheck asks the stream to validate its credential now.
	recheck chan struct{}
}

// New creates the hub.
func New(bus eventbus.Bus, st store.Store, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{bus: bus, st: st, log: logger.With("component", "live"), clients: map[*client]struct{}{}, perOwner: map[string]int{}, done: make(chan struct{})}
}

// WithAuth sets the authenticator that re-validates open streams.
func (h *Hub) WithAuth(a Authenticator) *Hub {
	h.auth = a
	return h
}

// Clients returns the number of connected streams.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Module returns the SSE route (Streaming: true, Permission: read).
func (h *Hub) Module() api.Module {
	return api.ModuleFunc{ModuleName: "live", RoutesFunc: func() []api.Route {
		return []api.Route{{Method: "GET", Pattern: "/events/stream", OperationID: "streamEvents", Permission: authz.PermRead, Streaming: true, Handler: h.serve}}
	}}
}

// ------------------------------------------------------------- frames ---

func (h *Hub) frame(event string, data any) []byte {
	b, err := json.Marshal(data)
	if err != nil {
		h.log.Error("cannot encode live event", "event", event, "error", err)
		b = []byte("{}")
	}
	return []byte(fmt.Sprintf("event: %s\nid: %d\ndata: %s\n\n", event, h.nextID.Add(1), b))
}

// broadcast offers a frame to all clients; a client whose buffer is full is
// reset and told to resync.
func (h *Hub) broadcast(event string, frame []byte) {
	perm, known := eventPermissions[event]
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !known || (c.principal != nil && !c.principal.Can(perm)) {
			continue
		}
		select {
		case c.ch <- frame:
		default:
			for drained := false; !drained; {
				select {
				case <-c.ch:
				default:
					drained = true
				}
			}
			select {
			case c.ch <- h.frame(dto.LiveResync, struct{}{}):
			default:
			}
		}
	}
}

func (h *Hub) send(event string, data any) { h.broadcast(event, h.frame(event, data)) }

// recheckAll asks every stream to validate its credential (auth.invalidate).
func (h *Hub) recheckAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.recheck <- struct{}{}:
		default:
		}
	}
}

// ------------------------------------------------------------- handler ---

func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	c := &client{ch: make(chan []byte, clientBuffer), recheck: make(chan struct{}, 1), principal: authz.FromContext(r.Context())}
	owner := ""
	if c.principal != nil {
		owner = c.principal.String()
	}
	h.mu.Lock()
	if len(h.clients) >= maxClients {
		h.mu.Unlock()
		w.Header().Set("Retry-After", "10")
		api.WriteError(w, r, fmt.Errorf("%w: too many live connections", domain.ErrUnavailable))
		return
	}
	if owner != "" && h.perOwner[owner] >= MaxStreamsPerPrincipal {
		h.mu.Unlock()
		w.Header().Set("Retry-After", "10")
		api.WriteError(w, r, fmt.Errorf("%w: too many live streams for this user", domain.ErrTooManyRequests))
		return
	}
	h.clients[c] = struct{}{}
	if owner != "" {
		h.perOwner[owner]++
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		if owner != "" {
			if h.perOwner[owner] <= 1 {
				delete(h.perOwner, owner)
			} else {
				h.perOwner[owner]--
			}
		}
		h.mu.Unlock()
	}()
	// stillValid re-runs the authentication of the stream's request.
	stillValid := func() bool {
		if h.auth == nil {
			return true
		}
		p, err := h.auth.Authenticate(r)
		return err == nil && p.Can(authz.PermRead) && (c.principal == nil || p.ID == c.principal.ID)
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Accel-Buffering", "no")
	hdr.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(WriteTimeout))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write([]byte(": connected\n\n")) {
		return
	}
	ping := time.NewTicker(PingInterval)
	defer ping.Stop()
	lifetime := time.NewTimer(MaxStreamLifetime)
	defer lifetime.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case <-lifetime.C:
			return
		case frame := <-c.ch:
			if !write(frame) {
				return
			}
		case <-c.recheck:
			select {
			case <-time.After(recheckDelay):
			case <-r.Context().Done():
				return
			}
			if !stillValid() {
				return
			}
		case <-ping.C:
			if !stillValid() {
				return
			}
			if !write([]byte(": ping\n\n")) {
				return
			}
		}
	}
}

// ----------------------------------------------------------------- run ---

type pending struct {
	agents   map[domain.AgentID]struct{}
	rollouts map[string]struct{} // rollout id (uuid string)
	fleets   map[string]struct{} // fleets whose version target changed
	stats    bool
}

func newPending() *pending {
	return &pending{agents: map[domain.AgentID]struct{}{}, rollouts: map[string]struct{}{}, fleets: map[string]struct{}{}}
}

// Run consumes bus events until ctx is done.
func (h *Hub) Run(ctx context.Context) error {
	defer func() {
		if ctx.Err() != nil { // real shutdown: terminate the streams (a restart after a bus failure keeps them)
			h.doneOnce.Do(func() { close(h.done) })
		}
	}()
	ch, cancel := h.bus.Subscribe(eventbus.AgentStateChanged, eventbus.AgentRemoved, eventbus.AssignmentChanged, eventbus.AgentDesiredChanged,
		eventbus.RolloutChanged, eventbus.FleetVersionChanged, eventbus.GitOpsSynced, eventbus.ReleaseDetected, eventbus.BusReconnected, eventbus.AuthInvalidate)
	defer cancel()

	p := newPending()
	flush := time.NewTicker(CoalesceInterval)
	defer flush.Stop()
	var lastStats time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return errors.New("live: event bus subscription closed")
			}
			h.handle(ctx, ev, p)
		case <-flush.C:
			h.flushPending(ctx, p)
			if p.stats && time.Since(lastStats) >= StatsInterval {
				p.stats = false
				lastStats = time.Now()
				h.send(dto.LiveStatsChanged, struct{}{})
			}
		}
	}
}

func (h *Hub) handle(ctx context.Context, ev eventbus.Event, p *pending) {
	switch ev.Type {
	case eventbus.AuthInvalidate:
		h.recheckAll()
	case eventbus.AgentStateChanged:
		var pl eventbus.AgentStateChangedPayload
		if err := ev.Decode(&pl); err == nil {
			p.agents[pl.AgentID] = struct{}{}
			p.stats = true
		}
	case eventbus.AgentRemoved:
		var pl eventbus.AgentIDsPayload
		if err := ev.Decode(&pl); err == nil {
			for _, id := range pl.AgentIDs {
				delete(p.agents, id)
				h.send(dto.LiveAgentRemoved, dto.LiveAgentRemovedData{ID: id})
			}
			p.stats = true
		}
	case eventbus.AssignmentChanged, eventbus.AgentDesiredChanged:
		var pl eventbus.AgentIDsPayload
		if err := ev.Decode(&pl); err == nil {
			for _, id := range pl.AgentIDs {
				p.agents[id] = struct{}{}
			}
		}
	case eventbus.RolloutChanged:
		var pl eventbus.RolloutChangedPayload
		if err := ev.Decode(&pl); err == nil {
			p.rollouts[pl.RolloutID.String()] = struct{}{}
			p.stats = true
		}
	case eventbus.FleetVersionChanged:
		var pl eventbus.FleetVersionChangedPayload
		if err := ev.Decode(&pl); err == nil && pl.FleetName != "" {
			p.fleets[pl.FleetName] = struct{}{}
		}
	case eventbus.GitOpsSynced:
		h.sendGitOps(ctx)
		p.stats = true
	case eventbus.ReleaseDetected:
		var pl eventbus.ReleaseDetectedPayload
		if err := ev.Decode(&pl); err == nil {
			h.sendRelease(ctx, pl)
			p.stats = true
		}
	case eventbus.BusReconnected:
		h.send(dto.LiveResync, struct{}{})
	}
}

func (h *Hub) flushPending(ctx context.Context, p *pending) {
	if len(p.agents) > 0 {
		ids := make([]domain.AgentID, 0, len(p.agents))
		for id := range p.agents {
			ids = append(ids, id)
		}
		p.agents = map[domain.AgentID]struct{}{}
		latest := h.latestVersion(ctx)
		for start := 0; start < len(ids); start += agentBatch {
			end := min(start+agentBatch, len(ids))
			qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			agents, err := h.st.Agents().GetMany(qctx, ids[start:end])
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					h.log.Warn("cannot load agents for live update, asking clients to resync", "error", err)
					h.send(dto.LiveResync, struct{}{})
				}
				break
			}
			for i := range agents {
				h.send(dto.LiveAgentUpdated, coreapi.AgentSummaryOf(&agents[i], latest))
			}
		}
	}
	for name := range p.fleets {
		delete(p.fleets, name)
		h.send(dto.LiveFleetUpdated, dto.LiveFleetUpdatedData{Name: name})
	}
	var names *actorname.Resolver
	for id := range p.rollouts {
		delete(p.rollouts, id)
		rid, err := parseUUID(id)
		if err != nil {
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ro, err := h.st.Rollouts().Get(qctx, rid)
		cancel()
		if err != nil {
			if ctx.Err() == nil && !isNotFound(err) {
				h.log.Warn("cannot load rollout for live update", "rollout", id, "error", err)
			}
			continue
		}
		if names == nil {
			names = actorname.New(ctx, h.st.Identity())
		}
		h.send(dto.LiveRolloutUpdated, coreapi.RolloutSummaryNamed(ro, names))
	}
}

func (h *Hub) sendGitOps(ctx context.Context) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	gs, err := h.st.GitOps().GetStatus(qctx)
	if err != nil {
		h.log.Warn("cannot load gitops status for live update", "error", err)
		h.send(dto.LiveResync, struct{}{})
		return
	}
	brief := dto.GitOpsBrief{State: gs.State, IssueCount: len(gs.Issues)}
	if gs.AppliedCommit != nil {
		brief.AppliedCommit = gs.AppliedCommit.SHA
	}
	h.send(dto.LiveGitOpsSynced, brief)
}

func (h *Hub) sendRelease(ctx context.Context, pl eventbus.ReleaseDetectedPayload) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rel, err := h.st.Releases().Get(qctx, pl.Distribution, pl.Version)
	if err != nil {
		if !isNotFound(err) {
			h.log.Warn("cannot load release for live update", "release", pl.Distribution+"/"+pl.Version, "error", err)
		}
		return
	}
	sum, err := coreapi.ReleaseSummaryOf(qctx, h.st, rel)
	if err != nil {
		h.log.Warn("cannot summarize release for live update", "error", err)
		return
	}
	h.send(dto.LiveReleaseNew, sum)
}

func (h *Hub) latestVersion(ctx context.Context) string {
	h.latestMu.Lock()
	defer h.latestMu.Unlock()
	if !h.latestAt.IsZero() && time.Since(h.latestAt) < 30*time.Second {
		return h.latestVal
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rel, err := h.st.Releases().Latest(qctx, domain.DistributionContrib, false)
	if err != nil {
		h.latestVal = ""
	} else {
		h.latestVal = rel.Version
	}
	h.latestAt = time.Now()
	return h.latestVal
}
