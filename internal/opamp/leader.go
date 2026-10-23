// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"fmt"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

const (
	sweepInterval    = 15 * time.Second
	reapInterval     = 10 * time.Second
	gaugeInterval    = 30 * time.Second
	replicaDeadAfter = 30 * time.Second
)

// RunLeader runs leader-only work until ctx is done: offline sweeper
// (AgentStore.MarkStaleOffline every 15s), dead replica reaping
// (ReplicaStore.DeleteStale + AgentStore.MarkReplicaOffline every 10s) and the
// agent gauges (every 30s).
func (s *Server) RunLeader(ctx context.Context) error {
	defer func() { // a former leader must not keep reporting stale gauges
		s.m.agents.Reset()
		s.m.configSync.Reset()
	}()
	s.sweepOffline(ctx)
	s.reapReplicas(ctx)
	s.updateGauges(ctx)
	sweep, reap, gauges := time.NewTicker(sweepInterval), time.NewTicker(reapInterval), time.NewTicker(gaugeInterval)
	defer sweep.Stop()
	defer reap.Stop()
	defer gauges.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sweep.C:
			s.sweepOffline(ctx)
		case <-reap.C:
			s.reapReplicas(ctx)
		case <-gauges.C:
			s.updateGauges(ctx)
		}
	}
}

// SweepOffline marks agents without messages for cfg.OfflineAfter offline.
// Exposed for tests; RunLeader calls it periodically.
func (s *Server) SweepOffline(ctx context.Context) { s.sweepOffline(ctx) }

func (s *Server) sweepOffline(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ids, err := s.deps.Store.Agents().MarkStaleOffline(sctx, s.now().Add(-s.cfg.OfflineAfter))
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("offline sweeper failed", "error", err)
		}
		return
	}
	if len(ids) == 0 {
		return
	}
	s.log.Info("marked stale agents offline", "count", len(ids), "after", s.cfg.OfflineAfter)
	s.announceOffline(ids, fmt.Sprintf("no message received for %s", s.cfg.OfflineAfter), "offline_timeout")
}

// ReapReplicas marks the agents of replicas without heartbeat offline.
func (s *Server) ReapReplicas(ctx context.Context) { s.reapReplicas(ctx) }

func (s *Server) reapReplicas(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dead, err := s.deps.Store.Replicas().DeleteStale(rctx, s.now().Add(-replicaDeadAfter))
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("replica reaper failed", "error", err)
		}
		return
	}
	for _, rid := range dead {
		if rid == s.deps.ReplicaID {
			continue // never reap ourselves (the next heartbeat re-registers)
		}
		ids, err := s.deps.Store.Agents().MarkReplicaOffline(rctx, rid, s.now())
		if err != nil {
			s.log.Warn("cannot mark agents of a dead replica offline", "replica", rid, "error", err)
			continue
		}
		s.log.Warn("replica considered dead, its agents were marked offline", "replica", rid, "agents", len(ids))
		s.announceOffline(ids, "replica "+rid+" stopped sending heartbeats", "replica_dead")
	}
}

func (s *Server) announceOffline(ids []domain.AgentID, message, reason string) {
	for _, id := range ids {
		s.emit(id, domain.AgentEventDisconnected, "warning", message, map[string]any{"reason": reason})
		s.publish(eventbus.AgentStateChanged, eventbus.AgentStateChangedPayload{AgentID: id, Changes: []string{"connection"}})
	}
}

func (s *Server) updateGauges(ctx context.Context) {
	gctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ag := s.deps.Store.Agents()
	s.m.agents.Reset()
	for _, kind := range []domain.AgentKind{domain.AgentKindCollector, domain.AgentKindUpdater, domain.AgentKindBridge, domain.AgentKindUnknown} {
		f, err := ag.Facets(gctx, store.AgentFilter{Kinds: []domain.AgentKind{kind}})
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("cannot compute agent gauges", "error", err)
			}
			return
		}
		for _, st := range []string{"online", "offline"} {
			n := 0
			for _, v := range f.Status {
				if v.Value == st {
					n = v.Count
				}
			}
			s.m.agents.WithLabelValues(st, string(kind)).Set(float64(n))
		}
	}
	f, err := ag.Facets(gctx, store.AgentFilter{})
	if err != nil {
		return
	}
	s.m.configSync.Reset()
	for _, st := range []domain.ConfigSyncState{domain.ConfigSyncNone, domain.ConfigSyncInSync, domain.ConfigSyncPending, domain.ConfigSyncFailed, domain.ConfigSyncDrift} {
		n := 0
		for _, v := range f.ConfigSync {
			if v.Value == string(st) {
				n = v.Count
			}
		}
		s.m.configSync.WithLabelValues(string(st)).Set(float64(n))
	}
}
