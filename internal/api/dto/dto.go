// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package dto contains REST request/response types that are not plain
// domain types. Together with internal/domain and internal/api (Problem,
// Page, List) they define the wire format; they MUST match the schemas in
// api/openapi.yaml exactly (same names, same JSON fields).
package dto

import (
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// ================================================================ agents ===

// AgentSummary is one row of GET /agents (schema AgentSummary).
type AgentSummary struct {
	ID          domain.AgentID   `json:"id"`
	Kind        domain.AgentKind `json:"kind"`
	Hostname    string           `json:"hostname"`
	ServiceName string           `json:"serviceName"`
	Version     string           `json:"version"`
	// Outdated: collectors only (always false for updaters/unknown):
	// semver(version) < latest stable release of otelcol-contrib.
	Outdated      bool                    `json:"outdated"`
	OSType        string                  `json:"osType"`
	Arch          string                  `json:"arch"`
	Status        domain.ConnectionStatus `json:"status"`
	LastSeenAt    *time.Time              `json:"lastSeenAt,omitempty"`
	Healthy       *bool                   `json:"healthy,omitempty"`
	HealthStatus  string                  `json:"healthStatus,omitempty"`
	FleetName     string                  `json:"fleetName,omitempty"`
	ConfigSync    domain.ConfigSyncState  `json:"configSync"`
	TargetVersion string                  `json:"targetVersion,omitempty"`
	// Site attributes (site.type, site.id, site.name, site.country,
	// site.region, site.timezone) copied from non-identifying attributes.
	Site map[string]string `json:"site,omitempty"`
	// Labels are the server-side labels.
	Labels map[string]string `json:"labels,omitempty"`
	// HasUpdater: a myriarch-updater is linked to this collector.
	HasUpdater bool `json:"hasUpdater"`
}

// AgentDetail is GET /agents/{id} (schema AgentDetail).
type AgentDetail struct {
	Agent domain.Agent `json:"agent"`
	// Computed fields.
	ConfigSync           domain.ConfigSyncState `json:"configSync"`
	Outdated             bool                   `json:"outdated"`
	LatestVersion        string                 `json:"latestVersion,omitempty"`
	CapabilityNames      []string               `json:"capabilityNames"`
	ActiveRolloutTargets []domain.RolloutTarget `json:"activeRolloutTargets"`
	// LinkedAgent is a summary of the linked updater/collector.
	LinkedAgent *AgentSummary `json:"linkedAgent,omitempty"`
}

// ConfigDocument is a config body (GET /agents/{id}/effective-config and
// /desired-config).
type ConfigDocument struct {
	Hash        domain.Hash `json:"hash"`
	ContentType string      `json:"contentType"`
	// Body is the YAML text.
	Body      string     `json:"body"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	// Source: "effective" (reported by agent), "desired", "assigned".
	Source string `json:"source"`
	// Redacted: secret-bearing values of an effective config were replaced by
	// "[REDACTED]" (the hash still identifies the stored original).
	Redacted bool `json:"redacted,omitempty"`
}

// AgentEventList is GET /agents/{id}/events.
type AgentEventList struct {
	Items []domain.AgentEvent `json:"items"`
	// NextBefore is the "before" cursor for the next page (0 = no more).
	NextBefore int64 `json:"nextBefore"`
}

// ActionAccepted is the 202 response of asynchronous actions.
type ActionAccepted struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// AgentFacets is GET /agents/facets.
type AgentFacets = store.AgentFacets

// CreateSavedFilterRequest is POST /saved-filters.
type CreateSavedFilterRequest struct {
	Name   string         `json:"name"`
	Page   string         `json:"page"`
	Query  map[string]any `json:"query"`
	Shared bool           `json:"shared"`
}

// ================================================================= stats ===

// OverviewStats is GET /stats/overview (dashboard).
type OverviewStats struct {
	GeneratedAt time.Time          `json:"generatedAt"`
	Agents      AgentCounts        `json:"agents"`
	Versions    []VersionCount     `json:"versions"`
	Outdated    OutdatedSummary    `json:"outdated"`
	ConfigSync  []store.FacetValue `json:"configSync"`
	Fleets      []FleetCounts      `json:"fleets"`
	// Unassigned collectors.
	Unassigned     int              `json:"unassigned"`
	ActiveRollouts []RolloutSummary `json:"activeRollouts"`
	LatestRelease  *ReleaseSummary  `json:"latestRelease,omitempty"`
	GitOps         GitOpsBrief      `json:"gitops"`
}

// AgentCounts aggregates agents (collectors and updaters separately).
type AgentCounts struct {
	Total         int `json:"total"`
	Collectors    int `json:"collectors"`
	Updaters      int `json:"updaters"`
	Online        int `json:"online"`
	Offline       int `json:"offline"`
	Healthy       int `json:"healthy"`
	Unhealthy     int `json:"unhealthy"`
	HealthUnknown int `json:"healthUnknown"`
}

// VersionCount is the number of collectors per version.
type VersionCount struct {
	Version  string `json:"version"`
	Count    int    `json:"count"`
	Outdated bool   `json:"outdated"`
}

// OutdatedSummary summarizes outdated collectors.
type OutdatedSummary struct {
	LatestVersion string  `json:"latestVersion,omitempty"`
	Count         int     `json:"count"`
	Percent       float64 `json:"percent"`
}

// FleetCounts is per-fleet agent state. Exact definitions (collectors with
// fleet_name = Fleet only): Total = count; Online = status online; Healthy =
// healthy is true; InSync = configSync in_sync; Pending = configSync pending OR
// drift; Failed = configSync failed; OnTarget = version == TargetVersion
// (0 if TargetVersion is empty). Used by the stats and the fleet endpoints.
type FleetCounts struct {
	Fleet         string `json:"fleet"`
	Total         int    `json:"total"`
	Online        int    `json:"online"`
	Healthy       int    `json:"healthy"`
	InSync        int    `json:"inSync"`
	Pending       int    `json:"pending"`
	Failed        int    `json:"failed"`
	TargetVersion string `json:"targetVersion,omitempty"`
	OnTarget      int    `json:"onTarget"` // collectors with version == targetVersion
}

// GitOpsBrief is a short GitOps status for the dashboard.
type GitOpsBrief struct {
	State         string `json:"state"`
	AppliedCommit string `json:"appliedCommit,omitempty"`
	IssueCount    int    `json:"issueCount"`
}

// ================================================================ system ===

// SystemInfo is GET /system/info.
type SystemInfo struct {
	Version   string           `json:"version"`
	Commit    string           `json:"commit"`
	BuildDate string           `json:"buildDate"`
	ReplicaID string           `json:"replicaId"`
	LeaderID  string           `json:"leaderId,omitempty"`
	Replicas  []domain.Replica `json:"replicas"`
	PublicURL string           `json:"publicUrl"`
	// OpAMPEndpoint is the full ws(s):// URL agents should use.
	OpAMPEndpoint string          `json:"opampEndpoint"`
	Database      DatabaseInfo    `json:"database"`
	Features      FeatureFlags    `json:"features"`
	Settings      SettingsSummary `json:"settings"`
}

// DatabaseInfo describes the database connection.
type DatabaseInfo struct {
	OK               bool   `json:"ok"`
	ServerVersion    string `json:"serverVersion,omitempty"`
	MigrationVersion int64  `json:"migrationVersion"`
}

// FeatureFlags tells the UI what is enabled.
type FeatureFlags struct {
	OIDC     bool `json:"oidc"`
	DevLogin bool `json:"devLogin"`
	// BootstrapAdmin: the bootstrap admin (break-glass account) accepts sign-ins.
	BootstrapAdmin bool `json:"bootstrapAdmin"`
	GitOps         bool `json:"gitops"`
	Releases       bool `json:"releases"`
	DevMode        bool `json:"devMode"`
}

// SettingsSummary shows effective non-secret settings (read-only UI page).
type SettingsSummary struct {
	GitOpsSource          string   `json:"gitopsSource"`
	GitOpsBranch          string   `json:"gitopsBranch"`
	GitOpsPollInterval    string   `json:"gitopsPollInterval"`
	ReleasesRepo          string   `json:"releasesRepo"`
	ReleasesPollInterval  string   `json:"releasesPollInterval"`
	TrackedDistributions  []string `json:"trackedDistributions"`
	ArtifactSource        string   `json:"artifactSource"`
	SignatureVerification bool     `json:"signatureVerification"`
	DefaultTimezone       string   `json:"defaultTimezone"`
	OfflineAfter          string   `json:"offlineAfter"`
	RetentionAgentEvents  string   `json:"retentionAgentEvents"`
	RetentionAudit        string   `json:"retentionAudit"`
}

// ================================================================== auth ===

// AuthConfigInfo is GET /auth/config (public).
type AuthConfigInfo struct {
	OIDCEnabled     bool   `json:"oidcEnabled"`
	OIDCDisplayName string `json:"oidcDisplayName,omitempty"`
	DevLoginEnabled bool   `json:"devLoginEnabled"`
	// BootstrapPasswordEnabled: the bootstrap admin is active and accepts its
	// password (POST /auth/bootstrap-login with username and password).
	BootstrapPasswordEnabled bool `json:"bootstrapPasswordEnabled"`
	// BootstrapTokenEnabled: the bootstrap admin is active and accepts a
	// one-time token (POST /auth/bootstrap-login with token).
	BootstrapTokenEnabled bool `json:"bootstrapTokenEnabled"`
	// UI carries the language options of the web UI (ui.* in the server
	// configuration); there is no secret in it.
	UI UIConfigInfo `json:"ui"`
}

// UIConfigInfo is the ui part of GET /auth/config.
type UIConfigInfo struct {
	DefaultLanguage string `json:"defaultLanguage"`
	// Languages restricts the offered UI languages; empty = every language
	// the UI ships.
	Languages []string `json:"languages,omitempty"`
}

// DevLoginRequest is POST /auth/dev-login.
type DevLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// BootstrapLoginRequest is POST /auth/bootstrap-login: exactly one of
// password (together with username) and token.
type BootstrapLoginRequest struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

// Me is GET /auth/me.
type Me struct {
	Type        string      `json:"type"` // user|token
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Email       string      `json:"email,omitempty"`
	Role        domain.Role `json:"role"`
	AuthMethod  string      `json:"authMethod"`
	Permissions []string    `json:"permissions"`
	// CSRFToken must be sent as header X-CSRF-Token on mutating requests
	// authenticated by the session cookie (also readable from the
	// `myriarch_csrf` cookie).
	CSRFToken string `json:"csrfToken,omitempty"`
}

// UpdateUserRequest is PATCH /users/{id}.
type UpdateUserRequest struct {
	Role     *domain.Role `json:"role,omitempty"`
	Disabled *bool        `json:"disabled,omitempty"`
}

// CreateAPITokenRequest is POST /tokens.
type CreateAPITokenRequest struct {
	Name      string      `json:"name"`
	Role      domain.Role `json:"role"`
	ExpiresAt *time.Time  `json:"expiresAt,omitempty"`
}

// CreatedAPIToken is the 201 response; Secret is shown only once.
type CreatedAPIToken struct {
	Token  domain.APIToken `json:"token"`
	Secret string          `json:"secret"`
}

// CreateEnrollmentTokenRequest is POST /enrollment-tokens.
type CreateEnrollmentTokenRequest struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	ExpiresAt *time.Time        `json:"expiresAt,omitempty"`
}

// CreatedEnrollmentToken is the 201 response; Secret is shown only once.
type CreatedEnrollmentToken struct {
	Token  domain.EnrollmentToken `json:"token"`
	Secret string                 `json:"secret"`
}

// ================================================================ fleets ===

// FleetStatus are live counts of a fleet.
type FleetStatus struct {
	Agents FleetCounts `json:"agents"`
	// Versions of member collectors.
	Versions       []VersionCount   `json:"versions"`
	ActiveRollouts []RolloutSummary `json:"activeRollouts"`
	// BehindLatest: targetVersion < latest stable release.
	BehindLatest  bool   `json:"behindLatest"`
	LatestVersion string `json:"latestVersion,omitempty"`
	// RenderErrors: number of member agents with assignment errors.
	RenderErrors int `json:"renderErrors"`
	// VersionTarget is the effective target version and where it comes from.
	VersionTarget FleetVersionTarget `json:"versionTarget"`
}

// FleetSummary is one row of GET /fleets.
type FleetSummary struct {
	Fleet  domain.Fleet `json:"fleet"`
	Status FleetStatus  `json:"status"`
}

// FleetDetail is GET /fleets/{name}.
type FleetDetail struct {
	Fleet  domain.Fleet `json:"fleet"`
	Status FleetStatus  `json:"status"`
	// BlueprintChain root-first.
	BlueprintChain []string             `json:"blueprintChain"`
	ConfigPolicy   domain.RolloutPolicy `json:"configPolicy"`
	VersionPolicy  domain.RolloutPolicy `json:"versionPolicy"`
	Executor       *ExecutorSummary     `json:"executor,omitempty"`
	Issues         domain.Issues        `json:"issues"`
	// WebURL links to the source file in the Git web UI (if configured).
	WebURL string `json:"webUrl,omitempty"`
}

// MatchFleetsRequest is POST /fleets/match.
type MatchFleetsRequest struct {
	// AgentID: evaluate an existing agent; or Attributes for a hypothetical one.
	AgentID    *domain.AgentID   `json:"agentId,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// MatchFleetsResponse is the result of POST /fleets/match.
type MatchFleetsResponse struct {
	Fleet      string   `json:"fleet,omitempty"`
	Candidates []string `json:"candidates"`
	Error      string   `json:"error,omitempty"`
	// Labels added by inventory entries.
	InventoryLabels map[string]string `json:"inventoryLabels,omitempty"`
}

// ExecutorSummary describes an Executor object without secrets.
type ExecutorSummary struct {
	Name        string              `json:"name"`
	Type        domain.ExecutorType `json:"type"`
	Description string              `json:"description,omitempty"`
	// Target is the redacted destination (URL without query/credentials).
	Target string           `json:"target"`
	Source domain.SourceRef `json:"source"`
	UsedBy []string         `json:"usedBy"`
}

// ============================================================ blueprints ===

// BlueprintSummary is one row of GET /blueprints.
type BlueprintSummary struct {
	Name           string           `json:"name"`
	Description    string           `json:"description,omitempty"`
	Extends        string           `json:"extends,omitempty"`
	ParameterCount int              `json:"parameterCount"`
	FragmentCount  int              `json:"fragmentCount"`
	UsedBy         []string         `json:"usedBy"`
	Source         domain.SourceRef `json:"source"`
	IssueCount     int              `json:"issueCount"`
}

// BlueprintDetail is GET /blueprints/{name}.
type BlueprintDetail struct {
	Blueprint domain.Blueprint `json:"blueprint"`
	// Chain root-first, e.g. ["base", "edge-windows"].
	Chain []string `json:"chain"`
	// Parameters is the merged parameter list over the chain.
	Parameters []domain.ParameterSpec `json:"parameters"`
	UsedBy     []string               `json:"usedBy"`
	Issues     domain.Issues          `json:"issues"`
	WebURL     string                 `json:"webUrl,omitempty"`
}

// RenderRequest is POST /blueprints/{name}/render.
type RenderRequest struct {
	// AgentID renders for an existing agent (its attributes and labels).
	AgentID *domain.AgentID `json:"agentId,omitempty"`
	// Attributes renders for a hypothetical agent (AttributeView keys).
	Attributes map[string]string `json:"attributes,omitempty"`
	// Fleet supplies fleet parameters (default: the agent's fleet).
	Fleet string `json:"fleet,omitempty"`
	// Parameters override everything (preview only).
	Parameters map[string]any `json:"parameters,omitempty"`
	// Validate also runs structural/component validation (default true).
	Validate *bool `json:"validate,omitempty"`
}

// RenderResponse is the result of a render.
type RenderResponse struct {
	Blueprint        string            `json:"blueprint"`
	Fleet            string            `json:"fleet,omitempty"`
	Layers           []string          `json:"layers"`
	Parameters       map[string]any    `json:"parameters"`
	ParameterSources map[string]string `json:"parameterSources"`
	Hash             domain.Hash       `json:"hash,omitempty"`
	// Body is the canonical YAML (empty if rendering failed).
	Body   string        `json:"body"`
	Issues domain.Issues `json:"issues"`
}

// ============================================================== rollouts ===

// RolloutSummary is one row of GET /rollouts (rollout without policy
// snapshot and annotations).
type RolloutSummary struct {
	ID           uuid.UUID            `json:"id"`
	Kind         domain.RolloutKind   `json:"kind"`
	FleetName    string               `json:"fleetName"`
	PolicyName   string               `json:"policyName"`
	Status       domain.RolloutStatus `json:"status"`
	StatusReason string               `json:"statusReason,omitempty"`
	FromRef      string               `json:"fromRef,omitempty"`
	ToRef        string               `json:"toRef"`
	GitCommit    string               `json:"gitCommit,omitempty"`
	CurrentWave  int                  `json:"currentWave"`
	WaveCount    int                  `json:"waveCount"`
	Counts       domain.RolloutCounts `json:"counts"`
	Origin       domain.RolloutOrigin `json:"origin"`
	// CreatedBy is the display name of the creator (presented by the caller;
	// NewRolloutSummary leaves the stored reference).
	CreatedBy  string     `json:"createdBy,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// NewRolloutSummary converts a rollout (with counts) to its list
// representation. CreatedBy keeps the stored actor reference: callers that
// serve it to users replace it by a display name.
func NewRolloutSummary(r *domain.Rollout) RolloutSummary {
	origin := r.Origin
	if origin == "" {
		origin = domain.OriginOf(r.CreatedBy)
	}
	return RolloutSummary{
		ID: r.ID, Kind: r.Kind, FleetName: r.FleetName, PolicyName: r.PolicyName, Status: r.Status, StatusReason: r.StatusReason,
		FromRef: r.FromRef, ToRef: r.ToRef, GitCommit: r.GitCommit, CurrentWave: r.CurrentWave, WaveCount: len(r.Waves),
		Counts: r.Counts, Origin: origin, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
	}
}

// RolloutActionRequest is the optional body of rollout actions.
type RolloutActionRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ApproveWaveRequest is POST /rollouts/{id}/actions/approve.
type ApproveWaveRequest struct {
	Wave   int    `json:"wave"`
	Reason string `json:"reason,omitempty"`
}

// ============================================================== releases ===

// ReleaseSummary is one row of GET /releases. Definitions: BreakingCount /
// DeprecationCount = len of the release's lists; Acknowledged =
// AcknowledgedAt != nil; IsLatest = equals ReleaseStore.Latest(distribution,
// includePrerelease=false); FleetsBehind = number of fleets with
// spec.collector.distribution == Distribution (default otelcol-contrib), a
// non-empty spec.collector.version and semver(version) < semver(Version).
type ReleaseSummary struct {
	Distribution     string     `json:"distribution"`
	Version          string     `json:"version"`
	Tag              string     `json:"tag"`
	PublishedAt      *time.Time `json:"publishedAt,omitempty"`
	Prerelease       bool       `json:"prerelease"`
	HTMLURL          string     `json:"htmlUrl,omitempty"`
	BreakingCount    int        `json:"breakingCount"`
	DeprecationCount int        `json:"deprecationCount"`
	Acknowledged     bool       `json:"acknowledged"`
	FleetsBehind     int        `json:"fleetsBehind"`
	IsLatest         bool       `json:"isLatest"`
}

// ReleaseDetail is GET /releases/{distribution}/{version}.
type ReleaseDetail struct {
	Release      domain.Release `json:"release"`
	IsLatest     bool           `json:"isLatest"`
	FleetsBehind []FleetBehind  `json:"fleetsBehind"`
	// AgentsBehind: collectors running a lower version.
	AgentsBehind int `json:"agentsBehind"`
	// GitOpsSnippet: YAML patch to set spec.collector.version in fleets.
	GitOpsSnippet string `json:"gitopsSnippet"`
	// UpdatableFleets: every fleet of the release's distribution and whether
	// this release can be selected for it in the UI (selectable first).
	UpdatableFleets []ReleaseFleetOption `json:"updatableFleets"`
}

// ReleaseFleetOption tells whether a release can be selected for a fleet.
type ReleaseFleetOption struct {
	Fleet       string             `json:"fleet"`
	DisplayName string             `json:"displayName,omitempty"`
	Target      FleetVersionTarget `json:"target"`
	Selectable  bool               `json:"selectable"`
	// ReasonCode is a NotAllowed* or a Reason* code (see VersionOption).
	ReasonCode     string `json:"reasonCode,omitempty"`
	Reason         string `json:"reason,omitempty"`
	AgentsToUpdate int    `json:"agentsToUpdate"`
}

// FleetBehind describes a fleet whose target is below a release.
type FleetBehind struct {
	Fleet         string         `json:"fleet"`
	TargetVersion string         `json:"targetVersion"`
	Agents        int            `json:"agents"`
	Versions      []VersionCount `json:"versions"`
	// BreakingChanges between targetVersion and the release.
	BreakingCount int    `json:"breakingCount"`
	SourcePath    string `json:"sourcePath"`
}

// =============================================================== gitops ===

// GitOpsObjectRef points to one synced object.
type GitOpsObjectRef struct {
	Kind   domain.ObjectKind `json:"kind"`
	Name   string            `json:"name"`
	Path   string            `json:"path"`
	Commit string            `json:"commit"`
	WebURL string            `json:"webUrl,omitempty"`
}

// ============================================================= artifacts ===

// PrefetchRequest is POST /artifacts/actions/prefetch.
type PrefetchRequest struct {
	Distribution string `json:"distribution"`
	Version      string `json:"version"`
	// Platforms "os/arch", e.g. ["linux/amd64","windows/amd64"]; empty = the
	// configured artifacts.prefetchPlatforms, else the platforms of the
	// connected collectors that use the distribution.
	Platforms []string `json:"platforms,omitempty"`
}

// ======================================================= version selection ===

// Reason codes of FleetVersionOptions.NotAllowedCode (the fleet cannot be
// updated from the UI now) and VersionOption.ReasonCode (the version cannot
// be selected); ReleaseFleetOption.ReasonCode uses both sets.
const (
	NotAllowedSelectionDisabled = "selection_disabled"
	NotAllowedNoExecutor        = "no_executor"
	NotAllowedBlockingRollout   = "blocking_rollout"

	ReasonCurrentTarget        = "current_target"
	ReasonOutsideFrame         = "outside_frame"
	ReasonDowngradeNotAllowed  = "downgrade_not_allowed"
	ReasonPrereleaseNotAllowed = "prerelease_not_allowed"
	ReasonUnknownVersion       = "unknown_version"
)

// FleetVersionTarget is the effective target version of a fleet and where it
// comes from (schema FleetVersionTarget).
type FleetVersionTarget struct {
	// Version is the effective target (absent: source none or hold).
	Version string                     `json:"version,omitempty"`
	Source  domain.VersionTargetSource `json:"source"`
	// GitVersion is spec.collector.version (absent = not set).
	GitVersion       string                        `json:"gitVersion,omitempty"`
	SelectionEnabled bool                          `json:"selectionEnabled"`
	Frame            *domain.VersionSelectionSpec  `json:"frame,omitempty"`
	Selection        *domain.FleetVersionSelection `json:"selection,omitempty"`
}

// VersionOption is one candidate version of the update dialog.
type VersionOption struct {
	Version    string `json:"version"`
	Selectable bool   `json:"selectable"`
	// ReasonCode / Reason: why the version cannot be selected.
	ReasonCode      string     `json:"reasonCode,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Known           bool       `json:"known"`
	Prerelease      bool       `json:"prerelease"`
	IsLatest        bool       `json:"isLatest"`
	IsCurrentTarget bool       `json:"isCurrentTarget"`
	IsGitVersion    bool       `json:"isGitVersion"`
	IsDowngrade     bool       `json:"isDowngrade"`
	PublishedAt     *time.Time `json:"publishedAt,omitempty"`
	// AgentsToUpdate: member collectors whose reported version differs.
	AgentsToUpdate   int `json:"agentsToUpdate"`
	BreakingCount    int `json:"breakingCount"`
	DeprecationCount int `json:"deprecationCount"`
}

// PlatformCount counts member collectors per os/arch.
type PlatformCount struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Agents int    `json:"agents"`
}

// FleetVersionOptions is GET /fleets/{name}/version-options.
type FleetVersionOptions struct {
	Fleet            string              `json:"fleet"`
	Distribution     string              `json:"distribution"`
	Allowed          bool                `json:"allowed"`
	NotAllowedCode   string              `json:"notAllowedCode,omitempty"`
	NotAllowedReason string              `json:"notAllowedReason,omitempty"`
	Target           FleetVersionTarget  `json:"target"`
	Executor         domain.ExecutorType `json:"executor"`
	VersionPolicy    string              `json:"versionPolicy"`
	ActiveRollout    *RolloutSummary     `json:"activeRollout,omitempty"`
	Options          []VersionOption     `json:"options"`
	Platforms        []PlatformCount     `json:"platforms"`
}

// SelectFleetVersionRequest is POST /fleets/{name}/actions/select-version.
type SelectFleetVersionRequest struct {
	Version string `json:"version"`
	Reason  string `json:"reason,omitempty"`
	// ExpectedTargetVersion is the effective target the operator saw ("" =
	// none); a mismatch is answered with 409.
	ExpectedTargetVersion *string `json:"expectedTargetVersion,omitempty"`
}

// SelectFleetVersionResponse is the 200 response of select-version.
type SelectFleetVersionResponse struct {
	Target    FleetVersionTarget `json:"target"`
	Planned   bool               `json:"planned"`
	RolloutID *uuid.UUID         `json:"rolloutId,omitempty"`
	// Targets is the number of targets of the rollout (with RolloutID).
	Targets int    `json:"targets,omitempty"`
	Message string `json:"message,omitempty"`
}

// ClearFleetVersionSelectionRequest is the optional body of
// POST /fleets/{name}/actions/clear-version-selection.
type ClearFleetVersionSelectionRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ============================================================ live events ===

// LiveEvent types sent on GET /events/stream (SSE "event:" field). The SSE
// "data:" field is the JSON payload described per type in
// api/openapi.yaml (schema LiveEvent*).
const (
	LiveAgentUpdated   = "agent.updated"   // data: AgentSummary
	LiveAgentRemoved   = "agent.removed"   // data: {"id": "<uuid>"}
	LiveRolloutUpdated = "rollout.updated" // data: RolloutSummary
	LiveFleetUpdated   = "fleet.updated"   // data: LiveFleetUpdatedData
	LiveGitOpsSynced   = "gitops.synced"   // data: GitOpsBrief
	LiveReleaseNew     = "release.new"     // data: ReleaseSummary
	LiveStatsChanged   = "stats.changed"   // data: {} (hint: refetch dashboard, max 1/5s)
	LiveResync         = "resync"          // data: {} (client fell behind: refetch everything)
)

// LiveAgentRemovedData is the payload of agent.removed.
type LiveAgentRemovedData struct {
	ID domain.AgentID `json:"id"`
}

// LiveFleetUpdatedData is the payload of fleet.updated.
type LiveFleetUpdatedData struct {
	Name string `json:"name"`
}
