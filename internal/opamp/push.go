// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"golang.org/x/sync/errgroup"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

var errSendTimeout = errors.New("send to agent timed out")

// Run runs the per-replica background work until ctx is done: bus
// subscriptions (AgentDesiredChanged, AgentCommand, AssignmentChanged), push
// workers, batched last_seen flushes, the event writer and bus publisher,
// and the replica heartbeat (store.ReplicaStore.Heartbeat).
func (s *Server) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { s.runEventWriter(gctx); return nil })
	for i := 0; i < publishWorkers; i++ {
		g.Go(func() error { s.runPublisher(gctx); return nil })
	}
	for i := 0; i < pushWorkers; i++ {
		g.Go(func() error { s.runPushWorker(gctx); return nil })
	}
	g.Go(func() error { s.runLastSeenFlusher(gctx); return nil })
	g.Go(func() error { s.runReplicaHeartbeat(gctx); return nil })
	g.Go(func() error { s.runHTTPSessionJanitor(gctx); return nil })
	g.Go(func() error { s.runBusListener(gctx); return nil })
	g.Go(func() error { s.runTokenWatcher(gctx); return nil })
	_ = g.Wait()
	// drain what is left (best effort, bounded)
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.flushLastSeen(fctx)
	s.flushEvents(fctx)
	return nil
}

// runBusListener subscribes to the events that make the replica push.
func (s *Server) runBusListener(ctx context.Context) {
	ch, cancel := s.deps.Bus.Subscribe(eventbus.AgentDesiredChanged, eventbus.AgentCommand, eventbus.AssignmentChanged, eventbus.BusReconnected)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			s.handleBusEvent(ev)
		}
	}
}

func (s *Server) handleBusEvent(ev eventbus.Event) {
	switch ev.Type {
	case eventbus.AgentDesiredChanged, eventbus.AssignmentChanged:
		var p eventbus.AgentIDsPayload
		if err := ev.Decode(&p); err != nil {
			s.log.Warn("malformed bus event", "type", ev.Type, "error", err)
			return
		}
		for _, id := range p.AgentIDs {
			s.markDirty(id, nil)
		}
	case eventbus.AgentCommand:
		var p eventbus.AgentCommandPayload
		if err := ev.Decode(&p); err != nil {
			s.log.Warn("malformed bus event", "type", ev.Type, "error", err)
			return
		}
		s.markDirty(p.AgentID, &p)
	case eventbus.BusReconnected:
		// events may have been lost: refresh every local session
		s.mu.RLock()
		all := make([]*session, 0, len(s.sessions))
		for _, sess := range s.sessions {
			all = append(all, sess)
		}
		s.mu.RUnlock()
		for _, sess := range all {
			s.enqueuePush(sess)
		}
		s.httpMu.Lock()
		for _, e := range s.httpSessions {
			e.sess.stale.Store(true)
		}
		s.httpMu.Unlock()
	}
}

// markDirty schedules a push for a locally connected WebSocket agent (and
// marks pooled HTTP sessions stale so the next poll re-reads the state).
func (s *Server) markDirty(id domain.AgentID, cmd *eventbus.AgentCommandPayload) {
	s.mu.RLock()
	sess := s.sessions[id]
	s.mu.RUnlock()
	if sess != nil {
		if cmd != nil {
			sess.cmdMu.Lock()
			sess.cmds = append(sess.cmds, *cmd)
			sess.cmdMu.Unlock()
		}
		s.enqueuePush(sess)
		return
	}
	s.httpMu.Lock()
	e := s.httpSessions[id]
	s.httpMu.Unlock()
	if e == nil {
		return
	}
	e.sess.stale.Store(true)
	if cmd != nil {
		e.sess.cmdMu.Lock()
		e.sess.cmds = append(e.sess.cmds, *cmd)
		e.sess.cmdMu.Unlock()
	}
}

func (s *Server) enqueuePush(sess *session) {
	if !sess.pushPending.CompareAndSwap(false, true) {
		return // already queued; the worker reads the latest state
	}
	select {
	case s.pushq <- sess:
	default:
		sess.pushPending.Store(false)
		s.m.dropped.WithLabelValues("push").Inc()
	}
}

func (s *Server) runPushWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sess := <-s.pushq:
			sess.pushPending.Store(false)
			s.push(sess)
		}
	}
}

// push refreshes the cached state of one session and sends what changed
// (remote_config, packages_available, flags) or the queued commands. At most
// one push per connection runs at a time (sess.mu).
func (s *Server) push(sess *session) {
	ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
	defer cancel()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.ci == nil || !sess.established || sess.offlineDone {
		return
	}
	s.mu.RLock()
	current := s.sessions[sess.id] == sess
	s.mu.RUnlock()
	if !current {
		return
	}
	sess.cmdMu.Lock()
	cmds := sess.cmds
	sess.cmds = nil
	sess.cmdMu.Unlock()

	// a resync must never be blocked by the failed hash: clear it here as well
	// (idempotent; the REST handler already cleared it, other callers may not)
	for _, c := range cmds {
		if c.Command == eventbus.CommandResync {
			if err := s.deps.Store.Agents().ClearFailedConfig(ctx, sess.id); err != nil && !errors.Is(err, domain.ErrNotFound) {
				s.log.Warn("cannot clear failed config for resync", "agent_id", sess.id, "error", err)
			}
			break
		}
	}
	if err := s.refresh(ctx, sess); err != nil {
		s.log.Warn("cannot refresh agent state for push", "agent_id", sess.id, "error", err)
		return
	}

	var flags uint64
	for _, c := range cmds {
		switch c.Command {
		case eventbus.CommandRestart:
			s.sendRestart(sess, c)
		case eventbus.CommandReportFullState:
			flags |= uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState)
			s.emit(sess.id, domain.AgentEventCommand, "info", "full state requested"+requestedBy(c), map[string]any{"command": string(c.Command), "requestedBy": c.RequestedBy})
		case eventbus.CommandResync:
			sess.resync = true
			s.emit(sess.id, domain.AgentEventCommand, "info", "resync requested"+requestedBy(c), map[string]any{"command": string(c.Command), "requestedBy": c.RequestedBy})
		}
	}

	resp := s.reconcile(sess, flags)
	if resp.RemoteConfig == nil && resp.PackagesAvailable == nil && resp.Flags == 0 {
		return
	}
	if err := s.send(sess.ci, resp); err != nil {
		s.log.Warn("push to agent failed", "agent_id", sess.id, "error", err)
		if resp.Flags&uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState) != 0 {
			sess.forceFullState = true
		}
		// forget the offer so that the next message offers it again
		sess.offeredConfig, sess.offeredPkgs = nil, nil
		return
	}
	s.m.messages.WithLabelValues("out").Inc()
}

func requestedBy(c eventbus.AgentCommandPayload) string {
	if c.RequestedBy == "" {
		return ""
	}
	return " by " + c.RequestedBy
}

// sendRestart sends the restart command as its own message (a command
// displaces all other fields of ServerToAgent).
func (s *Server) sendRestart(sess *session, c eventbus.AgentCommandPayload) {
	a := sess.agent
	if a == nil || !domain.HasCapability(a.Capabilities, domain.CapAcceptsRestartCommand) {
		s.emit(sess.id, domain.AgentEventCommand, "warning", "restart not supported by this agent", map[string]any{"command": "restart", "requestedBy": c.RequestedBy})
		return
	}
	msg := &protobufs.ServerToAgent{
		InstanceUid: append([]byte(nil), sess.id[:]...),
		Command:     &protobufs.ServerToAgentCommand{Type: protobufs.CommandType_CommandType_Restart},
	}
	if err := s.send(sess.ci, msg); err != nil {
		s.log.Warn("restart command failed", "agent_id", sess.id, "error", err)
		s.emit(sess.id, domain.AgentEventCommand, "error", "restart command could not be delivered: "+err.Error(), map[string]any{"command": "restart", "requestedBy": c.RequestedBy})
		return
	}
	s.m.messages.WithLabelValues("out").Inc()
	s.emit(sess.id, domain.AgentEventCommand, "info", "restart requested"+requestedBy(c), map[string]any{"command": "restart", "requestedBy": c.RequestedBy})
}

// send pushes a message to a WebSocket connection. Connection.Send ignores its
// context and a deadline on the net.Conn is reset by the WebSocket library
// before each frame, so the write runs in its own goroutine and a connection
// that does not accept the data within cfg.SendTimeout is closed (which
// unblocks the write).
func (s *Server) send(ci *connInfo, msg *protobufs.ServerToAgent) error {
	if ci == nil || ci.conn == nil {
		return errors.New("no connection")
	}
	done := make(chan error, 1)
	go func() { done <- ci.conn.Send(context.Background(), msg) }()
	timer := time.NewTimer(s.cfg.SendTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			s.m.pushErrors.Inc()
		}
		return err
	case <-timer.C:
		s.m.pushErrors.Inc()
		ci.disconnect()
		return fmt.Errorf("%w after %s", errSendTimeout, s.cfg.SendTimeout)
	}
}

// ------------------------------------------------------ background jobs ---

func (s *Server) runEventWriter(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flushEvents(ctx)
		}
	}
}

// flushEvents writes all queued agent events in batches of at most 500.
func (s *Server) flushEvents(ctx context.Context) {
	for {
		batch := make([]domain.AgentEvent, 0, 200)
	drain:
		for len(batch) < 500 {
			select {
			case ev := <-s.events:
				batch = append(batch, ev)
			default:
				break drain
			}
		}
		if len(batch) == 0 {
			return
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := s.deps.Store.Agents().AppendEvents(wctx, batch)
		cancel()
		if err != nil {
			s.log.Warn("cannot write agent events", "count", len(batch), "error", err)
			return
		}
	}
}

func (s *Server) runPublisher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-s.pubq:
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := s.deps.Bus.Publish(pctx, it.typ, it.payload); err != nil && ctx.Err() == nil {
				s.log.Warn("cannot publish event", "type", it.typ, "error", err)
			}
			cancel()
		}
	}
}

func (s *Server) runLastSeenFlusher(ctx context.Context) {
	t := time.NewTicker(s.cfg.LastSeenFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			s.flushLastSeen(fctx)
			cancel()
		}
	}
}

// flushLastSeen writes the pending last_seen timestamps with one statement
// per 5 second bucket (UPDATE ... FROM unnest). Entries of failed flushes are
// kept for the next run.
func (s *Server) flushLastSeen(ctx context.Context) {
	s.lastSeenMu.Lock()
	pending := s.lastSeen
	s.lastSeen = make(map[domain.AgentID]time.Time, len(pending))
	s.lastSeenMu.Unlock()
	if len(pending) == 0 {
		return
	}
	const bucket = 5 * time.Second
	type group struct {
		ids []domain.AgentID
		min time.Time
	}
	groups := map[time.Time]*group{}
	for id, t := range pending {
		b := t.Truncate(bucket)
		g := groups[b]
		if g == nil {
			g = &group{min: t}
			groups[b] = g
		}
		g.ids = append(g.ids, id)
		if t.Before(g.min) {
			g.min = t
		}
	}
	for _, g := range groups {
		// one statement per bucket; the oldest member's time is used so that no
		// agent ever appears to have been seen later than it really was
		ids, at := g.ids, g.min
		if err := s.deps.Store.Agents().TouchLastSeen(ctx, ids, at); err != nil {
			s.log.Warn("cannot flush last_seen", "agents", len(ids), "error", err)
			s.lastSeenMu.Lock()
			for _, id := range ids {
				if cur, ok := s.lastSeen[id]; !ok || cur.Before(pending[id]) {
					s.lastSeen[id] = pending[id]
				}
			}
			s.lastSeenMu.Unlock()
		}
	}
}

// leaderWatchInterval is how often the heartbeat loop looks at the elector. A change of leadership is written to the
// replica registry at once instead of with the next regular heartbeat, so that /system/info served by any replica
// shows the leader (and a takeover) within about a second.
const leaderWatchInterval = 500 * time.Millisecond

func (s *Server) runReplicaHeartbeat(ctx context.Context) {
	isLeader := func() bool { return s.deps.Elector != nil && s.deps.Elector.IsLeader() }
	beat := func() bool {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		leading := isLeader()
		err := s.deps.Store.Replicas().Heartbeat(hctx, domain.Replica{
			ID: s.deps.ReplicaID, Hostname: s.deps.Hostname, Version: s.deps.Version, StartedAt: s.startedAt,
			IsLeader: leading, Connections: s.Connections(),
		})
		if err != nil && ctx.Err() == nil {
			s.log.Warn("replica heartbeat failed", "error", err)
		}
		return leading
	}
	written := beat()
	t := time.NewTicker(s.deps.HA.HeartbeatInterval)
	defer t.Stop()
	var watch <-chan time.Time
	if s.deps.Elector != nil {
		w := time.NewTicker(leaderWatchInterval)
		defer w.Stop()
		watch = w.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			written = beat()
		case <-watch:
			if isLeader() != written {
				written = beat()
			}
		}
	}
}

func (s *Server) runHTTPSessionJanitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := s.now().Add(-httpSessionIdleTTL)
			s.httpMu.Lock()
			for id, e := range s.httpSessions {
				if e.lastUsed.Before(cutoff) {
					delete(s.httpSessions, id)
				}
			}
			s.httpMu.Unlock()
		}
	}
}

var _ sync.Mutex
