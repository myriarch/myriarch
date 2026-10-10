// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventbus defines the cluster-wide event bus of myriarchd.
//
// The PostgreSQL LISTEN/NOTIFY implementation lives in
// internal/eventbus/pgbus. NewMemory (this package) is a working
// single-process implementation for tests.
//
// Semantics:
//   - Publish delivers the event to all subscribers on ALL replicas,
//     including the publishing replica (at-most-once, best effort; events may
//     be lost on reconnects). Consumers must therefore treat events as hints
//     and re-read authoritative state from the store.
//   - Event payloads are JSON and must be small: the encoded Event must be
//     <= MaxEventSize bytes (Postgres NOTIFY limit is 8000 bytes). Use
//     ChunkAgentIDs for large id lists.
//   - Subscribers receive events on a buffered channel. If a subscriber is
//     too slow and its buffer is full, events are dropped for that
//     subscriber and the drop is counted (metric myriarch_eventbus_dropped_total).
package eventbus

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
)

// MaxEventSize is the maximum JSON size of one encoded Event.
const MaxEventSize = 7500

// Type identifies an event type. Types are dot separated "<area>.<what>".
type Type string

const (
	// AgentStateChanged: reported state of an agent changed (connection,
	// health, version, remote config status, description). Payload:
	// AgentStateChangedPayload. Published by the OpAMP layer (internal/opamp).
	// Consumers: live UI stream (internal/live), rollout engine wake-up
	// (internal/rollout). Fleet assignment is NOT driven by this event: the OpAMP
	// layer calls control.Assigner synchronously.
	AgentStateChanged Type = "agent.state_changed"
	// AgentRemoved: an agent was deleted. Payload: AgentIDsPayload.
	AgentRemoved Type = "agent.removed"
	// AgentDesiredChanged: desired config and/or package offers changed for
	// agents; the replica holding the connection pushes. Payload:
	// AgentIDsPayload. Published via control.AgentControl.
	AgentDesiredChanged Type = "agent.desired_changed"
	// AgentCommand: send a command to an agent. Payload: AgentCommandPayload.
	AgentCommand Type = "agent.command"
	// AssignmentChanged: fleet membership/assigned config of agents changed.
	// Payload: AgentIDsPayload. Published by fleet (assignment).
	AssignmentChanged Type = "assignment.changed"
	// RolloutChanged: a rollout was created or changed state/progress.
	// Payload: RolloutChangedPayload. Published by fleet.
	RolloutChanged Type = "rollout.changed"
	// FleetVersionChanged: the UI version selection of a fleet was set,
	// cleared, superseded, invalidated or planned, or the effective target
	// version of a fleet changed in another way. Payload:
	// FleetVersionChangedPayload. Published by the REST API (select/clear)
	// and by the leader (conflict rules, planning); consumers: rollout engine
	// wake-up (leader), assignment selection cache (every replica), live UI
	// stream.
	FleetVersionChanged Type = "fleet.version_changed"
	// GitOpsSyncRequested: a webhook/manual sync was requested on any
	// replica; the leader performs it. Payload: GitOpsSyncRequestedPayload.
	GitOpsSyncRequested Type = "gitops.sync_requested"
	// GitOpsSynced: a sync finished. Payload: GitOpsSyncedPayload.
	GitOpsSynced Type = "gitops.synced"
	// ReleaseDetected: a new upstream release was stored. Payload:
	// ReleaseDetectedPayload.
	ReleaseDetected Type = "release.detected"
	// ArtifactChanged: artifact cache entry changed. Payload: ArtifactChangedPayload.
	ArtifactChanged Type = "artifact.changed"
	// ReleaseRefreshRequested: a non-leader asks the leader to poll releases
	// now (POST /releases/actions/refresh). Payload: {}.
	ReleaseRefreshRequested Type = "release.refresh_requested"
	// AuthInvalidate: sessions, users, API tokens or enrollment tokens changed
	// in a way that cached authentication results must be dropped on every
	// replica (logout, revocation, disable, role change). Payload:
	// AuthInvalidatePayload. Published by authn; consumed by authn (caches)
	// and the live hub (open SSE streams).
	AuthInvalidate Type = "auth.invalidate"
	// EnrollmentTokenRevoked: an enrollment token was revoked (or found
	// expired). Every replica purges its token cache and disconnects the
	// agent sessions that authenticated with it. Payload:
	// EnrollmentTokenPayload.
	EnrollmentTokenRevoked Type = "enrollment_token.revoked"
	// BusReconnected is a LOCAL-ONLY event (never published cluster-wide):
	// implementations deliver it to their own subscribers after the
	// underlying listener reconnected, because events may have been lost.
	// Consumers re-read state (live hub sends "resync" to UI clients).
	BusReconnected Type = "bus.reconnected"
)

// Event is the envelope sent over the bus.
type Event struct {
	ID   uuid.UUID `json:"id"`
	Type Type      `json:"type"`
	Time time.Time `json:"time"`
	// Origin is the replica id of the publisher.
	Origin  string          `json:"origin"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Decode unmarshals the payload into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Payload, v) }

// Bus is the event bus.
type Bus interface {
	// Publish sends an event cluster-wide. The implementation fills ID, Time
	// and Origin if empty. Returns an error if the encoded event exceeds
	// MaxEventSize or the bus is closed.
	Publish(ctx context.Context, typ Type, payload any) error
	// Subscribe returns a channel receiving events of the given types (all
	// types if none given) and a cancel function that must be called to
	// release the subscription. The channel is closed after cancel or when
	// the bus is closed.
	Subscribe(types ...Type) (<-chan Event, func())
	// Close stops the bus.
	Close() error
}

// ----------------------------------------------------------- Payloads ----

// AuthInvalidatePayload identifies what to drop from the authentication
// caches.
type AuthInvalidatePayload struct {
	// Kind: "session" (ID = session id), "user" (ID = user id: all sessions
	// and tokens of the user), "api_token" (ID = token id) or
	// "enrollment_token" (ID = token id: the cached enrollment token rows;
	// agent sessions stay connected, see EnrollmentTokenRevoked).
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// EnrollmentTokenPayload identifies an enrollment token.
type EnrollmentTokenPayload struct {
	TokenID uuid.UUID `json:"tokenId"`
	// Reason: "revoked" or "expired".
	Reason string `json:"reason,omitempty"`
}

// AgentIDsPayload carries agent ids.
type AgentIDsPayload struct {
	AgentIDs []domain.AgentID `json:"agentIds"`
}

// AgentStateChangedPayload describes what changed for one agent.
type AgentStateChangedPayload struct {
	AgentID domain.AgentID `json:"agentId"`
	// Changes: any of "connection", "health", "version", "remote_config",
	// "description", "effective_config", "components", "packages", "created".
	Changes []string `json:"changes"`
	// Online/Healthy/Version reflect the new state for cheap consumers.
	Online  bool   `json:"online"`
	Healthy *bool  `json:"healthy,omitempty"`
	Version string `json:"version,omitempty"`
}

// CommandType is an agent command.
type CommandType string

const (
	// CommandRestart sends OpAMP ServerToAgentCommand RESTART.
	CommandRestart CommandType = "restart"
	// CommandReportFullState sets the ReportFullState flag on the next push.
	CommandReportFullState CommandType = "report_full_state"
	// CommandResync re-sends the desired remote config even if hashes match
	// (and clears the failed hash first) plus ReportFullState.
	CommandResync CommandType = "resync"
)

// AgentCommandPayload carries a command.
type AgentCommandPayload struct {
	AgentID domain.AgentID `json:"agentId"`
	Command CommandType    `json:"command"`
	// RequestedBy is the principal (for agent event history).
	RequestedBy string `json:"requestedBy,omitempty"`
}

// RolloutChangedPayload identifies a rollout.
type RolloutChangedPayload struct {
	RolloutID uuid.UUID            `json:"rolloutId"`
	FleetName string               `json:"fleetName"`
	Status    domain.RolloutStatus `json:"status"`
}

// FleetVersionChangedPayload identifies the fleet whose version selection
// or effective target changed.
type FleetVersionChangedPayload struct {
	FleetName string `json:"fleetName"`
}

// GitOpsSyncRequestedPayload requests a sync.
type GitOpsSyncRequestedPayload struct {
	Trigger     string `json:"trigger"` // webhook|manual
	RequestedBy string `json:"requestedBy,omitempty"`
}

// GitOpsSyncedPayload reports a finished sync.
type GitOpsSyncedPayload struct {
	Commit string `json:"commit"`
	Result string `json:"result"`
}

// ReleaseDetectedPayload identifies a release.
type ReleaseDetectedPayload struct {
	Distribution string `json:"distribution"`
	Version      string `json:"version"`
}

// ArtifactChangedPayload identifies an artifact.
type ArtifactChangedPayload struct {
	ArtifactID uuid.UUID             `json:"artifactId"`
	Status     domain.ArtifactStatus `json:"status"`
	// SHA256 (hex) of the file; set for status "deleted" so that replicas can
	// remove the content addressed file.
	SHA256 string `json:"sha256,omitempty"`
}

// ChunkAgentIDs splits ids into chunks that fit into one event
// (100 ids ~ 4 KB JSON).
func ChunkAgentIDs(ids []domain.AgentID) [][]domain.AgentID {
	const n = 100
	var out [][]domain.AgentID
	for len(ids) > 0 {
		k := n
		if len(ids) < k {
			k = len(ids)
		}
		out = append(out, ids[:k])
		ids = ids[k:]
	}
	return out
}
