// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/brand"
)

// AgentKind classifies an OpAMP agent.
type AgentKind string

const (
	// AgentKindCollector is an OpenTelemetry Collector managed through the
	// upstream OpAMP supervisor (service.name is the collector command, e.g.
	// "otelcol-contrib").
	AgentKindCollector AgentKind = "collector"
	// AgentKindUpdater is a myriarch-updater host agent
	// (service.name == UpdaterServiceName).
	AgentKindUpdater AgentKind = "updater"
	// AgentKindUnknown is any other OpAMP agent.
	AgentKindUnknown AgentKind = "unknown"
)

// UpdaterServiceName is the service.name identifying attribute reported by
// myriarch-updater. The OpAMP layer classifies agents with this service.name as
// AgentKindUpdater.
const UpdaterServiceName = brand.UpdaterServiceName

// ConnectionStatus is the online state of an agent as seen by myriarchd.
type ConnectionStatus string

const (
	ConnectionOnline  ConnectionStatus = "online"
	ConnectionOffline ConnectionStatus = "offline"
)

// RemoteConfigStatus mirrors OpAMP RemoteConfigStatuses (lower-case).
type RemoteConfigStatus string

const (
	RemoteConfigUnset    RemoteConfigStatus = "unset"
	RemoteConfigApplying RemoteConfigStatus = "applying"
	RemoteConfigApplied  RemoteConfigStatus = "applied"
	RemoteConfigFailed   RemoteConfigStatus = "failed"
)

// ConfigSyncState is the server-side summary of desired vs. reported config.
type ConfigSyncState string

const (
	// ConfigSyncNone: the agent has no fleet or no assigned config yet (a
	// render error on an agent that already has an assigned config keeps the
	// previous hash and does NOT produce "none").
	ConfigSyncNone ConfigSyncState = "none"
	// ConfigSyncInSync: reported hash == desired hash and status applied.
	ConfigSyncInSync ConfigSyncState = "in_sync"
	// ConfigSyncPending: desired differs from reported and no failure yet
	// (offered, applying, or agent offline).
	ConfigSyncPending ConfigSyncState = "pending"
	// ConfigSyncFailed: the agent reported FAILED for the desired hash.
	ConfigSyncFailed ConfigSyncState = "failed"
	// ConfigSyncDrift: desired == assigned is NOT true, i.e. a rollout has
	// not yet reached this agent or was rolled back (assigned != desired).
	ConfigSyncDrift ConfigSyncState = "drift"
)

// Agent is the full server-side state of one OpAMP agent.
//
// Ownership of fields:
//   - Identity/Description/Connection/Health/RemoteConfig/Effective config/
//     Components/PackageStatuses are written by the OpAMP layer (internal/opamp) from
//     AgentToServer messages.
//   - Assignment is written by the assignment service (internal/assignment) and the
//     rollout engine (internal/rollout).
type Agent struct {
	ID          AgentID   `json:"id"`
	Kind        AgentKind `json:"kind"`
	ServiceName string    `json:"serviceName"`
	// Version is service.version without leading "v".
	Version       string `json:"version"`
	Hostname      string `json:"hostname"`
	OSType        string `json:"osType"`
	OSDescription string `json:"osDescription"`
	Arch          string `json:"arch"`

	// IdentifyingAttributes from AgentDescription (all values stringified,
	// conversion rules in docs/concepts/agents.md, "The attribute view").
	IdentifyingAttributes map[string]string `json:"identifyingAttributes"`
	// NonIdentifyingAttributes from AgentDescription; includes the
	// supervisor's agent.description.non_identifying_attributes (site.*).
	NonIdentifyingAttributes map[string]string `json:"nonIdentifyingAttributes"`
	// Labels are server-side labels (from the enrollment token and GitOps
	// inventory). They are addressed in selectors as "labels.<key>".
	Labels map[string]string `json:"labels"`

	// Capabilities is the raw OpAMP AgentCapabilities bitmask.
	Capabilities uint64 `json:"capabilities"`

	Connection    AgentConnection   `json:"connection"`
	Health        *AgentHealth      `json:"health,omitempty"`
	RemoteConfig  RemoteConfigState `json:"remoteConfig"`
	Assignment    AgentAssignment   `json:"assignment"`
	Updater       *UpdaterLink      `json:"updater,omitempty"`
	PackageStatus *PackageStatuses  `json:"packageStatus,omitempty"`

	// EffectiveConfigHash points into the effective config store (content
	// addressed by SHA-256 of the body reported under config map key "").
	EffectiveConfigHash      Hash       `json:"effectiveConfigHash,omitempty"`
	EffectiveConfigUpdatedAt *time.Time `json:"effectiveConfigUpdatedAt,omitempty"`
	// AvailableComponentsHash is the agent-calculated hash from
	// AvailableComponents.hash; the component set is stored once per hash.
	AvailableComponentsHash Hash `json:"availableComponentsHash,omitempty"`

	// EnrollmentTokenID and ClientCertSubject bind the instance uid to the
	// credential of its first registration (see AC-05): another credential
	// cannot take the uid over.
	EnrollmentTokenID *uuid.UUID `json:"enrollmentTokenId,omitempty"`
	ClientCertSubject string     `json:"-"`
	FirstSeenAt       time.Time  `json:"firstSeenAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// AgentConnection describes the transport state.
type AgentConnection struct {
	Status ConnectionStatus `json:"status"`
	// ReplicaID of the myriarchd replica holding the WebSocket connection
	// (empty when offline).
	ReplicaID string `json:"replicaId,omitempty"`
	// Transport is "websocket" or "http".
	Transport      string     `json:"transport,omitempty"`
	RemoteAddr     string     `json:"remoteAddr,omitempty"`
	ConnectedAt    *time.Time `json:"connectedAt,omitempty"`
	DisconnectedAt *time.Time `json:"disconnectedAt,omitempty"`
	LastSeenAt     *time.Time `json:"lastSeenAt,omitempty"`
}

// AgentHealth is the top-level OpAMP ComponentHealth plus the full tree.
type AgentHealth struct {
	Healthy   bool       `json:"healthy"`
	Status    string     `json:"status,omitempty"`
	LastError string     `json:"lastError,omitempty"`
	StartTime *time.Time `json:"startTime,omitempty"`
	// Components is the recursive component health map (OpAMP
	// component_health_map), e.g. "pipeline:metrics" -> "receiver:otlp".
	Components map[string]*ComponentHealth `json:"components,omitempty"`
	UpdatedAt  time.Time                   `json:"updatedAt"`
}

// ComponentHealth mirrors OpAMP ComponentHealth (recursive).
type ComponentHealth struct {
	Healthy    bool                        `json:"healthy"`
	Status     string                      `json:"status,omitempty"`
	LastError  string                      `json:"lastError,omitempty"`
	StartTime  *time.Time                  `json:"startTime,omitempty"`
	StatusTime *time.Time                  `json:"statusTime,omitempty"`
	Components map[string]*ComponentHealth `json:"components,omitempty"`
}

// RemoteConfigState is what the agent last reported in RemoteConfigStatus.
type RemoteConfigState struct {
	Status RemoteConfigStatus `json:"status"`
	// LastHash is last_remote_config_hash reported by the agent.
	LastHash     Hash       `json:"lastHash,omitempty"`
	ErrorMessage string     `json:"errorMessage,omitempty"`
	UpdatedAt    *time.Time `json:"updatedAt,omitempty"`
}

// AgentAssignment is the server-side desired state of an agent.
//
// Semantics (see docs/architecture.md, "Desired state of an agent"):
//   - AssignedConfigHash: what GitOps says the agent should run (rendered
//     blueprint of its fleet). Written by the assignment service.
//   - DesiredConfigHash: what myriarchd currently offers to the agent via
//     OpAMP remote_config. Equals AssignedConfigHash for new agents
//     (bootstrap) and after a rollout admitted the agent. Written by the
//     assignment service (bootstrap) and the rollout engine.
//   - FailedConfigHash: a desired hash the agent reported FAILED for. The
//     OpAMP layer MUST NOT re-offer this hash automatically (prevents
//     apply/rollback loops with supervisor automatic_config_rollback). Cleared
//     when DesiredConfigHash changes or by a "resync" action.
//   - TargetVersion: collector version the fleet wants (from GitOps).
type AgentAssignment struct {
	FleetName          string     `json:"fleetName,omitempty"`
	AssignedConfigHash Hash       `json:"assignedConfigHash,omitempty"`
	DesiredConfigHash  Hash       `json:"desiredConfigHash,omitempty"`
	FailedConfigHash   Hash       `json:"failedConfigHash,omitempty"`
	TargetVersion      string     `json:"targetVersion,omitempty"`
	Error              string     `json:"error,omitempty"`
	AssignedAt         *time.Time `json:"assignedAt,omitempty"`
}

// UpdaterLink links a collector agent and its myriarch-updater agent.
type UpdaterLink struct {
	// AgentID of the linked agent: for a collector the updater's ID, for an
	// updater the collector's ID.
	AgentID AgentID `json:"agentId"`
	// Online state of the linked agent at read time (informational).
	Online bool `json:"online"`
	// Ambiguous (collector side only): more than one online myriarch-updater
	// with the same credential claims this collector. Package offers are not
	// made in that state.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// PackageStatuses mirrors OpAMP PackageStatuses reported by updater agents.
type PackageStatuses struct {
	ServerProvidedAllPackagesHash Hash                      `json:"serverProvidedAllPackagesHash,omitempty"`
	ErrorMessage                  string                    `json:"errorMessage,omitempty"`
	Packages                      map[string]*PackageStatus `json:"packages"`
	UpdatedAt                     time.Time                 `json:"updatedAt"`
}

// PackageInstallStatus mirrors OpAMP PackageStatusEnum (snake_case).
type PackageInstallStatus string

const (
	PackageInstalled      PackageInstallStatus = "installed"
	PackageInstallPending PackageInstallStatus = "install_pending"
	PackageInstalling     PackageInstallStatus = "installing"
	PackageInstallFailed  PackageInstallStatus = "install_failed"
	PackageDownloading    PackageInstallStatus = "downloading"
)

// PackageStatus mirrors OpAMP PackageStatus.
type PackageStatus struct {
	Name                 string               `json:"name"`
	AgentHasVersion      string               `json:"agentHasVersion,omitempty"`
	AgentHasHash         Hash                 `json:"agentHasHash,omitempty"`
	ServerOfferedVersion string               `json:"serverOfferedVersion,omitempty"`
	ServerOfferedHash    Hash                 `json:"serverOfferedHash,omitempty"`
	Status               PackageInstallStatus `json:"status"`
	ErrorMessage         string               `json:"errorMessage,omitempty"`
	DownloadPercent      float64              `json:"downloadPercent,omitempty"`
}

// ComponentSet is the set of available components reported by an agent
// (OpAMP AvailableComponents), stored once per hash.
type ComponentSet struct {
	Hash Hash `json:"hash"`
	// Components maps kind ("receivers", "processors", "exporters",
	// "extensions", "connectors") to component type -> details.
	Components map[string]map[string]ComponentDetails `json:"components"`
}

// ComponentDetails holds metadata of one available component.
type ComponentDetails struct {
	// Module is the Go module (OpAMP metadata "code.namespace"), e.g.
	// "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/hostmetricsreceiver v0.162.0".
	Module   string            `json:"module,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// AgentEventType classifies entries of an agent's event history.
type AgentEventType string

const (
	AgentEventConnected        AgentEventType = "connected"
	AgentEventDisconnected     AgentEventType = "disconnected"
	AgentEventRegistered       AgentEventType = "registered"
	AgentEventDescription      AgentEventType = "description_changed"
	AgentEventVersionChanged   AgentEventType = "version_changed"
	AgentEventHealthChanged    AgentEventType = "health_changed"
	AgentEventConfigOffered    AgentEventType = "config_offered"
	AgentEventConfigApplied    AgentEventType = "config_applied"
	AgentEventConfigFailed     AgentEventType = "config_failed"
	AgentEventAssigned         AgentEventType = "assigned"
	AgentEventPackageOffered   AgentEventType = "package_offered"
	AgentEventPackageStatus    AgentEventType = "package_status"
	AgentEventCommand          AgentEventType = "command"
	AgentEventRolloutTarget    AgentEventType = "rollout_target"
	AgentEventIdentityConflict AgentEventType = "identity_conflict"
)

// AgentEvent is one entry of an agent's history (timeline in the UI).
type AgentEvent struct {
	ID       int64          `json:"id"`
	AgentID  AgentID        `json:"agentId"`
	Time     time.Time      `json:"time"`
	Type     AgentEventType `json:"type"`
	Severity string         `json:"severity"` // info|warning|error
	Message  string         `json:"message"`
	Details  map[string]any `json:"details,omitempty"`
}

// Assignment is the result of evaluating fleet membership for one agent.
type Assignment struct {
	AgentID   AgentID `json:"agentId"`
	FleetName string  `json:"fleetName,omitempty"`
	// Candidates are all fleets whose selector matched (for diagnostics).
	Candidates         []string `json:"candidates,omitempty"`
	AssignedConfigHash Hash     `json:"assignedConfigHash,omitempty"`
	// DesiredChanged is true if DesiredConfigHash was set by bootstrap rules
	// and the OpAMP layer must push the config.
	DesiredChanged bool   `json:"desiredChanged"`
	TargetVersion  string `json:"targetVersion,omitempty"`
	Error          string `json:"error,omitempty"`
}

// ConfigSyncState derives the server-side config sync state. It mirrors the
// generated column agents.config_sync in internal/store/migrations (keep both
// in sync):
//
//	none    fleet unassigned or no assigned config
//	failed  desired != nil && failed == desired
//	drift   desired != assigned (rollout pending / rolled back / bootstrap missing)
//	in_sync reported hash == desired && status applied
//	pending everything else (offered, applying, agent offline)
func (a *Agent) ConfigSyncState() ConfigSyncState {
	as := a.Assignment
	switch {
	case as.FleetName == "" || as.AssignedConfigHash.IsZero():
		return ConfigSyncNone
	case !as.DesiredConfigHash.IsZero() && as.FailedConfigHash.Equal(as.DesiredConfigHash):
		return ConfigSyncFailed
	case !as.DesiredConfigHash.Equal(as.AssignedConfigHash):
		return ConfigSyncDrift
	case a.RemoteConfig.LastHash.Equal(as.DesiredConfigHash) && a.RemoteConfig.Status == RemoteConfigApplied:
		return ConfigSyncInSync
	default:
		return ConfigSyncPending
	}
}

// IsOnline reports whether the agent is connected.
func (a *Agent) IsOnline() bool { return a.Connection.Status == ConnectionOnline }

// UpdaterMismatch returns why updater must not manage collector ("" = they
// belong together): both have to hold the same credential (enrollment token,
// or client certificate when no token is used) and, when both report one, the
// same host name. An agent therefore cannot attach itself to a collector that
// registered with another credential or lives on another host.
func UpdaterMismatch(updater, collector *Agent) string {
	switch {
	case updater == nil || collector == nil:
		return ""
	case !uuidPtrEqual(updater.EnrollmentTokenID, collector.EnrollmentTokenID):
		return "the myriarch-updater and the collector registered with different enrollment tokens"
	case updater.EnrollmentTokenID == nil && updater.ClientCertSubject != collector.ClientCertSubject:
		return "the myriarch-updater and the collector registered with different client certificates"
	case updater.Hostname != "" && collector.Hostname != "" && !strings.EqualFold(updater.Hostname, collector.Hostname):
		return "the myriarch-updater and the collector report different host names"
	}
	return ""
}

func uuidPtrEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
