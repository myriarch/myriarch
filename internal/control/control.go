// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package control defines the service interfaces between the OpAMP layer
// (internal/opamp) and the fleet management layer (internal/assignment,
// internal/rollout).
//
//	BusControl implements AgentControl (this package; executed by internal/opamp) -> used by the fleet and agent REST APIs
//	internal/assignment implements Assigner -> used by internal/opamp
package control

import (
	"context"

	"github.com/myriarch/myriarch/internal/domain"
)

// AgentControl lets any component act on agents without knowing which
// replica holds an agent's connection. The implementation is BusControl (this
// package): every method publishes an event; the OpAMP server (internal/opamp) on the
// replica that holds the WebSocket connection performs the push. Methods
// return after publishing (they do not wait for delivery); agents that are
// offline get the current desired state when they reconnect.
type AgentControl interface {
	// DesiredStateChanged MUST be called after EVERY store write that affects
	// what the OpAMP layer offers to an agent: assignment, desired config,
	// failed config (set/clear) and package offers (set/clear). The
	// connection-holding replica refreshes its per-connection cache from the
	// store and sends ServerToAgent with remote_config and/or
	// packages_available if they differ from what the agent reported.
	DesiredStateChanged(ctx context.Context, ids []domain.AgentID) error
	// Restart asks the connection-holding replica to send
	// ServerToAgentCommand RESTART. Callers (REST handler) check the
	// AcceptsRestartCommand capability and the online state BEFORE calling and
	// return 412 themselves; the OpAMP layer ignores the command (and records
	// an agent event) if the agent cannot restart.
	Restart(ctx context.Context, id domain.AgentID, requestedBy string) error
	// RequestFullState sets the ReportFullState flag for the agent.
	RequestFullState(ctx context.Context, id domain.AgentID, requestedBy string) error
	// Resync re-sends the desired config and requests full state ("force
	// resync" in the UI). Callers clear the failed config hash in the store
	// first (AgentStore.ClearFailedConfig), so that it also takes effect for
	// offline agents when they reconnect.
	Resync(ctx context.Context, id domain.AgentID, requestedBy string) error
}

// Assigner evaluates fleet membership and the assigned config of agents.
// Implemented by internal/assignment.
//
// Called by the OpAMP layer (internal/opamp) synchronously when an agent is registered
// (first AgentDescription) and whenever its AgentDescription (attributes)
// changes, BEFORE the server answers that message, so that a new agent
// receives its config in the first response.
type Assigner interface {
	// AssignAgent evaluates inventory labels, fleet selectors and priorities,
	// renders the fleet blueprint for the agent, stores the rendered config
	// (ConfigStore.PutRendered) and persists the assignment
	// (AgentStore.SetAssignment). Bootstrap rule: if the agent has no
	// desired config yet, or its fleet changed, DesiredConfigHash is set to
	// the assigned hash immediately (Assignment.DesiredChanged=true);
	// otherwise changes go through a rollout.
	//
	// Must be safe for concurrent use and fast (in-memory snapshot of GitOps
	// objects; typical < 20 ms). Rendering errors are not returned as error:
	// they are stored in the assignment (Assignment.Error) and the agent keeps
	// its previous desired config. A returned error means infrastructure
	// failure (e.g. database).
	AssignAgent(ctx context.Context, id domain.AgentID) (domain.Assignment, error)
}
