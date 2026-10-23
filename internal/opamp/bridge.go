// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import (
	"context"
	"errors"
	"fmt"

	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/domain"
)

// OpAMP Bridge agents (docs/design/opamp-server.md, "Bridge agents"; ADR-0037).
// The Bridge of the OpenTelemetry Operator speaks for many collector resources,
// and a message that lacks the key of one of them deletes it. The server
// therefore holds back from a Bridge every action that changes something:
// remote config unless a fleet opted in, restart commands always. Every
// function here fails closed.

// bridgeConfigLocked reports whether remote config must not be offered to the
// session's agent, and records the held-back offer once per reason and desired
// hash on this connection (an event and an audit entry, not one per heartbeat).
// The caller holds sess.mu. An agent that is no Bridge is never locked.
func (s *Server) bridgeConfigLocked(sess *session, a *domain.Agent) bool {
	if a.Kind != domain.AgentKindBridge {
		return false
	}
	reason := s.bridgeLockReason(a)
	if reason == "" {
		return false
	}
	desired := a.Assignment.DesiredConfigHash
	if key := reason + "|" + desired.String(); sess.bridgeHeld != key {
		sess.bridgeHeld = key
		s.noteBridgeHeld(sess.id, "remote_config", reason, map[string]any{"hash": desired.String()})
	}
	return true
}

// bridgeLockReason returns why the fleet of the Bridge a may not send it remote
// config, or "" when it may. A fleet that cannot be read locks the Bridge.
func (s *Server) bridgeLockReason(a *domain.Agent) string {
	var fleet *domain.Fleet
	if name := a.Assignment.FleetName; name != "" {
		ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
		defer cancel()
		f, err := s.deps.Store.GitOps().GetFleet(ctx, name)
		switch {
		case err == nil:
			fleet = f
		case errors.Is(err, domain.ErrNotFound):
		default:
			s.log.Warn("cannot read the fleet of an OpAMP Bridge; no remote config is offered", "agent_id", a.ID, "fleet", name, "error", err)
			return "the fleet of the OpAMP Bridge could not be read"
		}
	}
	return domain.BridgeConfigLock(a, fleet)
}

// noteBridgeHeld records an action that the server held back from a Bridge.
func (s *Server) noteBridgeHeld(id domain.AgentID, action, reason string, extra map[string]any) {
	details := map[string]any{"action": action, "reason": reason}
	for k, v := range extra {
		details[k] = v
	}
	s.emit(id, domain.AgentEventBridgeLocked, "warning", fmt.Sprintf("%s held back from an OpAMP Bridge: %s", actionText(action), reason), details)
	s.auditEvent(audit.System(domain.ActorSystem, "agent.bridge_held_back", "agent", id.String(), details))
}

func actionText(action string) string {
	if action == "remote_config" {
		return "remote config"
	}
	return action
}

// refuseBridgeRestart reports whether a restart command is refused because the
// agent is a Bridge (always, with or without the opt-in), and records it.
func (s *Server) refuseBridgeRestart(a *domain.Agent, requestedBy string) bool {
	if a == nil || a.Kind != domain.AgentKindBridge {
		return false
	}
	s.noteBridgeHeld(a.ID, "restart", domain.BridgeRestartRefused, map[string]any{"requestedBy": requestedBy})
	return true
}
