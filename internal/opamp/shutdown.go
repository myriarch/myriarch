// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

// Shutdown closes all agent connections (agents reconnect to another
// replica) and flushes pending state: new connections are refused with 503,
// existing WebSocket connections are closed at most 100 per second, pending
// last_seen values and events are written, and all agents still marked as
// connected to this replica are marked offline in one statement.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() { s.shutdownErr = s.shutdown(ctx) })
	return s.shutdownErr
}

func (s *Server) shutdown(ctx context.Context) error {
	s.shuttingDown.Store(true)
	s.mu.RLock()
	list := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	s.mu.RUnlock()
	s.log.Info("shutting down: closing agent connections", "connections", len(list))

	interval := time.Second / shutdownCloseRate
	t := time.NewTicker(interval)
	defer t.Stop()
closing:
	for _, sess := range list {
		select {
		case <-ctx.Done():
			break closing
		case <-t.C:
		}
		if sess.ci != nil {
			sess.ci.disconnect()
		}
	}

	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	s.flushLastSeen(fctx)
	ids, err := s.deps.Store.Agents().MarkReplicaOffline(fctx, s.deps.ReplicaID, s.now())
	if err != nil {
		s.log.Warn("cannot mark agents of this replica offline", "error", err)
	}
	for _, id := range ids {
		s.emit(id, domain.AgentEventDisconnected, "info", "replica "+s.deps.ReplicaID+" shut down", map[string]any{"reason": "replica_shutdown"})
	}
	s.flushEvents(fctx)
	// notify listeners (UI) directly: the publisher workers may be gone already
	pctx, pcancel := context.WithTimeout(fctx, 5*time.Second)
	defer pcancel()
	for _, id := range ids {
		if pctx.Err() != nil {
			break
		}
		_ = s.deps.Bus.Publish(pctx, eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: id, Changes: []string{"connection"}})
	}
	s.mu.Lock()
	s.sessions = map[domain.AgentID]*session{}
	s.mu.Unlock()
	return nil
}
