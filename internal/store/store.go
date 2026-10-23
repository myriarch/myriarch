// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package store defines the persistence interfaces of myriarchd.
//
// Semantics: docs/design/opamp-server.md, Persistence.
//
// The PostgreSQL implementation lives in internal/store/postgres. The schema
// is defined by the SQL migrations in internal/store/migrations. Other
// packages depend ONLY on these interfaces and use hand-written fakes in their
// unit tests (embed the interface in a struct and override the methods you
// need).
//
// General rules for all implementations:
//   - Methods return domain.ErrNotFound (wrapped) when a single requested
//     entity does not exist, domain.ErrConflict on unique violations.
//   - Batch methods with empty input are no-ops.
//   - Methods never return nil slices for lists: empty lists are []T{}.
//   - Map fields of returned domain objects (attributes, labels, details,
//     parameters) are never nil: empty maps are map[K]V{} (JSON "{}", not
//     "null"); the same holds for slice fields documented as required in
//     api/openapi.yaml.
//   - All timestamps are stored/returned in UTC.
//   - Methods must be safe for concurrent use.
//   - IDs and timestamps: callers set UUID ids (uuid.NewV7) and CreatedAt on
//     the values passed to Create*/Upsert* methods (they return no entity);
//     only database sequences (bigserial: agent events, audit events, GitOps
//     syncs) are assigned by the store. Methods that return the stored entity
//     (UpsertDescription, ArtifactStore.Upsert, UpsertUser) document it.
//     UpdatedAt/updated_at columns are always set by the store.
//   - Empty strings for nullable columns (e.g. fleet_name, connected_replica)
//     and empty hashes are stored as NULL and read back as ""/nil.
//   - GitOps objects and other domain values stored as JSON(B) MUST be read
//     back with domain.UnmarshalJSONPreservingInts, so that numbers in
//     interface-typed fields (parameters, blueprint config) stay int instead
//     of float64 (otherwise rendering differs between myriarchd and myrctl).
package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
)

// Store is the root persistence interface.
type Store interface {
	Agents() AgentStore
	Configs() ConfigStore
	GitOps() GitOpsStore
	Rollouts() RolloutStore
	// FleetVersions stores the target versions operators chose in the UI.
	FleetVersions() FleetVersionStore
	Packages() PackageOfferStore
	Releases() ReleaseStore
	Artifacts() ArtifactStore
	Identity() IdentityStore
	Audit() AuditStore
	Replicas() ReplicaStore
	KV() KVStore

	// InTx runs fn inside one database transaction. The Store passed to fn
	// is bound to the transaction; fn must only use that Store. If fn returns
	// an error (or panics) the transaction is rolled back.
	InTx(ctx context.Context, fn func(tx Store) error) error

	// Ping checks database connectivity (used by /readyz).
	Ping(ctx context.Context) error
	// Close releases all resources.
	Close()
}

// --------------------------------------------------------------- Agents ----

// AgentFilter selects agents. All set fields are combined with AND; slice
// fields match any of their values (OR within the field).
type AgentFilter struct {
	IDs []domain.AgentID
	// Query is a case-insensitive substring searched in hostname, agent id
	// (string form), service version and all attribute values.
	Query  string
	Kinds  []domain.AgentKind
	Status []domain.ConnectionStatus
	// Healthy filters by top-level health (agents without health report do
	// not match either value).
	Healthy *bool
	Fleets  []string
	// Unassigned=true selects agents without fleet (fleet_name IS NULL),
	// Unassigned=false agents with a fleet.
	Unassigned *bool
	Versions   []string
	// OutdatedBelow selects COLLECTORS (kind=collector implied) whose parsed
	// semantic version is strictly lower than this version ("0.162.0").
	// Agents with unparsable versions never match. (REST: outdated=true)
	OutdatedBelow string
	// UpToDateFrom selects COLLECTORS whose parsed version is >= this
	// version. (REST: outdated=false)
	UpToDateFrom string
	OSTypes      []string
	// Attributes requires, for every key, that the AttributeView value of the
	// key (identifying, non-identifying, or "labels.<key>") equals one of the
	// listed values (OR within a key, AND across keys). REST: repeated
	// attr=key=value parameters.
	Attributes         map[string][]string
	ConfigSync         []domain.ConfigSyncState
	RemoteConfigStatus []domain.RemoteConfigStatus
	LastSeenBefore     *time.Time
}

// AgentSortFields are the accepted AgentQuery.Sort values (optionally
// prefixed with "-" for descending). Default: "hostname".
var AgentSortFields = []string{"hostname", "version", "status", "fleet", "lastSeenAt", "firstSeenAt", "health", "configSync", "osType", "siteId"}

// AgentQuery is a paginated agent list request.
type AgentQuery struct {
	Filter AgentFilter
	// Sort is one of AgentSortFields, optionally prefixed with "-".
	Sort string
	// Limit 1..500 (0 = default 50). Offset >= 0.
	Limit  int
	Offset int
}

// AgentPage is a page of agents plus the total number of matches.
type AgentPage struct {
	Items []domain.Agent
	Total int
}

// FacetValue is one value with its count.
type FacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// AgentFacets are counts for filter UIs and the dashboard. Computed over the
// agents matching the given filter.
type AgentFacets struct {
	Total      int          `json:"total"`
	Status     []FacetValue `json:"status"`
	Health     []FacetValue `json:"health"` // "healthy", "unhealthy", "unknown"
	Kinds      []FacetValue `json:"kinds"`
	Versions   []FacetValue `json:"versions"`
	Fleets     []FacetValue `json:"fleets"` // "" = unassigned
	OSTypes    []FacetValue `json:"osTypes"`
	SiteTypes  []FacetValue `json:"siteTypes"`
	Countries  []FacetValue `json:"countries"`
	ConfigSync []FacetValue `json:"configSync"`
}

// AgentDescriptionUpdate carries data derived from an AgentDescription.
type AgentDescriptionUpdate struct {
	Kind                     domain.AgentKind
	ServiceName              string
	Version                  string // normalized, without "v"
	Hostname                 string
	OSType                   string
	OSDescription            string
	Arch                     string
	IdentifyingAttributes    map[string]string
	NonIdentifyingAttributes map[string]string
	Capabilities             uint64
	EnrollmentTokenID        *uuid.UUID
	// ClientCertSubject of the verified client certificate ("" = none).
	ClientCertSubject string
	// InitialLabels are applied only when the agent is created.
	InitialLabels map[string]string
}

// AgentAssignmentUpdate is written by the assignment service.
type AgentAssignmentUpdate struct {
	FleetName          string // "" = unassigned
	AssignedConfigHash domain.Hash
	TargetVersion      string
	Error              string
	AssignedAt         time.Time
}

// AgentStore persists agent state. Writers: the OpAMP layer (internal/opamp) for
// reported state, the assignment service and rollout engine for
// desired state. Every Set* method updates only its columns (no lost
// updates between writers) and bumps updated_at.
//
// Hydration: Get, GetMany, List and ListAll all return fully populated
// agents, including Agent.Updater (derived in both directions, see
// SetUpdaterLink, with Online from the linked row) and Agent.PackageStatus.
//
// Desired-state writes (SetAssignment, SetDesiredConfig*, SetFailedConfig,
// ClearFailedConfig, PackageOfferStore.SetOffers/ClearOffers) must be followed
// by control.AgentControl.DesiredStateChanged for the affected agents by the
// caller (the OpAMP layer caches desired state per connection).
type AgentStore interface {
	Get(ctx context.Context, id domain.AgentID) (*domain.Agent, error)
	// GetMany returns the existing agents among ids (missing ids are ignored).
	GetMany(ctx context.Context, ids []domain.AgentID) ([]domain.Agent, error)
	List(ctx context.Context, q AgentQuery) (AgentPage, error)
	// ListAll returns all matching agents without pagination (internal jobs;
	// expected size <= 10k).
	ListAll(ctx context.Context, f AgentFilter) ([]domain.Agent, error)
	Facets(ctx context.Context, f AgentFilter) (AgentFacets, error)

	// UpsertDescription creates the agent if missing (first_seen_at=now,
	// labels=InitialLabels) or updates description fields. Returns the agent
	// after the update and whether it was created.
	UpsertDescription(ctx context.Context, id domain.AgentID, u AgentDescriptionUpdate) (*domain.Agent, bool, error)
	// SetConnection writes Status, ReplicaID (""=NULL), Transport and
	// RemoteAddr; nil timestamp pointers (ConnectedAt, DisconnectedAt,
	// LastSeenAt) leave the respective column unchanged. Returns ErrNotFound
	// if the agent row does not exist yet (call UpsertDescription first).
	//
	// A call with Status=offline AND a non-empty ReplicaID is conditional: it
	// only writes if connected_replica is NULL or equals that replica (and
	// then clears connected_replica). Otherwise it is a no-op without error.
	// This protects an agent that has already reconnected to another replica
	// from the late close handling of its old connection. Offline writes
	// without a ReplicaID are unconditional.
	SetConnection(ctx context.Context, id domain.AgentID, c domain.AgentConnection) error
	// TouchLastSeen sets last_seen_at for many agents (batched heartbeat
	// flush); ids that do not exist are ignored.
	TouchLastSeen(ctx context.Context, ids []domain.AgentID, at time.Time) error
	SetHealth(ctx context.Context, id domain.AgentID, h *domain.AgentHealth) error
	SetRemoteConfigState(ctx context.Context, id domain.AgentID, s domain.RemoteConfigState) error
	SetEffectiveConfig(ctx context.Context, id domain.AgentID, hash domain.Hash, at time.Time) error
	SetAvailableComponentsHash(ctx context.Context, id domain.AgentID, hash domain.Hash) error
	SetPackageStatuses(ctx context.Context, id domain.AgentID, s *domain.PackageStatuses) error
	// SetUpdaterLink stores the collector the updater manages
	// (updater row only; the collector may not exist yet). Passing a zero
	// collectorID removes the link. Get/List derive Agent.Updater in BOTH
	// directions: for an updater from its own link, for a collector from the
	// updater row that links to it (most recently seen updater wins).
	SetUpdaterLink(ctx context.Context, updaterID, collectorID domain.AgentID) error
	// SetCredentialBinding overwrites the credential an agent is bound to
	// (nil token / empty subject clear it). Used by the admin "re-enroll"
	// action and when the bound enrollment token was revoked or expired.
	SetCredentialBinding(ctx context.Context, id domain.AgentID, tokenID *uuid.UUID, certSubject string) error
	SetLabels(ctx context.Context, id domain.AgentID, labels map[string]string) error

	// MarkReplicaOffline marks all agents connected via replicaID offline
	// (disconnected_at=at) and returns their ids.
	MarkReplicaOffline(ctx context.Context, replicaID string, at time.Time) ([]domain.AgentID, error)
	// MarkStaleOffline marks online agents with last_seen_at < before as
	// offline and returns their ids.
	MarkStaleOffline(ctx context.Context, before time.Time) ([]domain.AgentID, error)
	// Delete removes an agent and its events (UI "forget agent").
	Delete(ctx context.Context, id domain.AgentID) error
	// DeleteOfflineBefore purges agents offline since before (retention).
	DeleteOfflineBefore(ctx context.Context, before time.Time) (int64, error)

	// SetAssignment writes fleet membership and assigned config/version.
	SetAssignment(ctx context.Context, id domain.AgentID, u AgentAssignmentUpdate) error
	// CompareAndSetAssignment writes u only if the stored fleet, assigned
	// config hash, target version and assignment error still equal prev
	// (fleet name, assigned config hash, target version and error). It
	// reports whether the row was written. With several replicas exactly one
	// of the callers that saw the same previous assignment wins; the others
	// get false and must not record the change. A missing agent is ErrNotFound.
	CompareAndSetAssignment(ctx context.Context, id domain.AgentID, prev domain.AgentAssignment, u AgentAssignmentUpdate) (bool, error)
	// SetDesiredConfig sets desired_config_hash for all ids; for rows where
	// the value changes, failed_config_hash is cleared.
	SetDesiredConfig(ctx context.Context, ids []domain.AgentID, hash domain.Hash) error
	// SetDesiredConfigIfUnset sets desired_config_hash only where it IS NULL
	// (bootstrap of new agents). Returns the ids that were changed.
	SetDesiredConfigIfUnset(ctx context.Context, ids []domain.AgentID, hash domain.Hash) ([]domain.AgentID, error)
	// SetFleetTargetVersion sets assignment.target_version of all collectors
	// of the fleet whose value differs (after the effective target of the
	// fleet changed) and returns the number of changed rows. The OpAMP layer
	// needs no push: the target version is not part of the desired state.
	SetFleetTargetVersion(ctx context.Context, fleet, version string) (int64, error)
	// SetFailedConfig records that the agent reported FAILED for hash.
	SetFailedConfig(ctx context.Context, id domain.AgentID, hash domain.Hash) error
	// ClearFailedConfig clears failed_config_hash (operator "resync").
	ClearFailedConfig(ctx context.Context, id domain.AgentID) error

	AppendEvents(ctx context.Context, events []domain.AgentEvent) error
	// ListEvents returns events newest first; beforeID > 0 pages backwards.
	ListEvents(ctx context.Context, id domain.AgentID, limit int, beforeID int64) ([]domain.AgentEvent, error)
	DeleteEventsBefore(ctx context.Context, before time.Time) (int64, error)
}

// -------------------------------------------------------------- Configs ----

// ConfigStore stores content-addressed config bodies and component sets.
// Put* methods are idempotent: on conflict they only refresh touched_at
// (protects a reused body from a concurrent GC).
type ConfigStore interface {
	PutRendered(ctx context.Context, c domain.RenderedConfig) error
	GetRendered(ctx context.Context, hash domain.Hash) (*domain.RenderedConfig, error)
	PutEffective(ctx context.Context, c domain.EffectiveConfig) error
	GetEffective(ctx context.Context, hash domain.Hash) (*domain.EffectiveConfig, error)
	PutComponentSet(ctx context.Context, s domain.ComponentSet) error
	GetComponentSet(ctx context.Context, hash domain.Hash) (*domain.ComponentSet, error)
	// HasComponentSet is a cheap existence check (OpAMP hot path).
	HasComponentSet(ctx context.Context, hash domain.Hash) (bool, error)
	// GC deletes rendered/effective configs whose touched_at is before
	// olderThan and that are not referenced by any agent
	// (assigned/desired/failed/effective config hash) or by a target of a
	// non-terminal rollout. Returns the number of deleted rows.
	GC(ctx context.Context, olderThan time.Time) (int64, error)
}

// --------------------------------------------------------------- GitOps ----

// ObjectChanges lists "<Kind>/<name>" keys changed by ApplySnapshot.
type ObjectChanges struct {
	Added   []string
	Changed []string
	Removed []string
}

// GitOpsStore stores the active GitOps snapshot and sync history.
type GitOpsStore interface {
	// ApplySnapshot atomically replaces ALL declarative objects with snap and
	// records the sync (sync.Result should be "applied"). Objects are
	// compared by SourceRef.ContentHash to compute changes.
	ApplySnapshot(ctx context.Context, snap domain.GitOpsSnapshot, sync domain.GitOpsSync) (ObjectChanges, error)
	// GetSnapshot returns the active snapshot (empty snapshot, not an
	// error, if nothing was ever synced). Commit is the commit of the latest
	// sync with result "applied" ("" if none). Objects are decoded with
	// domain.UnmarshalJSONPreservingInts.
	GetSnapshot(ctx context.Context) (*domain.GitOpsSnapshot, error)

	ListFleets(ctx context.Context) ([]domain.Fleet, error)
	GetFleet(ctx context.Context, name string) (*domain.Fleet, error)
	ListBlueprints(ctx context.Context) ([]domain.Blueprint, error)
	GetBlueprint(ctx context.Context, name string) (*domain.Blueprint, error)
	ListRolloutPolicies(ctx context.Context) ([]domain.RolloutPolicy, error)
	GetRolloutPolicy(ctx context.Context, name string) (*domain.RolloutPolicy, error)
	ListExecutors(ctx context.Context) ([]domain.Executor, error)
	GetExecutor(ctx context.Context, name string) (*domain.Executor, error)
	ListInventories(ctx context.Context) ([]domain.Inventory, error)

	// RecordSync stores a sync attempt that did not change the snapshot
	// ("unchanged", "invalid", "error") and returns its id.
	RecordSync(ctx context.Context, sync domain.GitOpsSync) (int64, error)
	ListSyncs(ctx context.Context, limit, offset int) ([]domain.GitOpsSync, int, error)
	// DeleteSyncsBefore deletes sync history entries started before t.
	DeleteSyncsBefore(ctx context.Context, before time.Time) (int64, error)
	// SaveStatus / GetStatus persist the syncer status so that every replica
	// can serve GET /gitops/status. GetStatus returns a status with State
	// "never" (and empty Issues/ObjectCounts) if nothing was saved yet.
	SaveStatus(ctx context.Context, s domain.GitOpsSyncStatus) error
	GetStatus(ctx context.Context) (*domain.GitOpsSyncStatus, error)
}

// ------------------------------------------------------------- Rollouts ----

// RolloutFilter filters rollouts.
type RolloutFilter struct {
	Fleets []string
	Kinds  []domain.RolloutKind
	Status []domain.RolloutStatus
	// ActiveOnly selects non-terminal rollouts.
	ActiveOnly bool
	// Limit is the page size. PostgreSQL uses 50 for Limit <= 0 and caps it at
	// 500 (the in-memory stores return everything for Limit <= 0): use
	// ListActiveRollouts to read all active rollouts.
	Limit  int
	Offset int
}

// TargetOrder selects the sort order of ListTargets.
type TargetOrder string

const (
	// TargetOrderWave is the default and the stable order the rollout engine
	// relies on: wave, hostname, agent ID.
	TargetOrderWave TargetOrder = ""
	// TargetOrderActivity ranks targets by status first (see
	// domain.TargetStatus.ActivityRank: running work first), then by wave,
	// hostname and agent ID.
	TargetOrderActivity TargetOrder = "activity"
)

// TargetFilter filters rollout targets.
type TargetFilter struct {
	Wave   *int
	Status []domain.TargetStatus
	// Agent restricts the list to the target of one agent (at most one row).
	Agent  *domain.AgentID
	Order  TargetOrder
	Limit  int
	Offset int
}

// TargetRef identifies the latest target of an agent for a change, together
// with the status of its rollout (planner exclusion rules).
type TargetRef struct {
	Target        domain.RolloutTarget
	RolloutStatus domain.RolloutStatus
}

// RolloutStore persists rollouts.
//
// Concurrency model (the engine runs on the leader, operator actions on any
// replica):
//   - Rollout status (and status_reason, started_at, finished_at) changes ONLY
//     via CompareAndSetStatus - by the engine and by operator actions alike.
//     A failed CAS means somebody else changed the status: re-read and
//     re-evaluate on the next tick.
//   - Wave approval changes ONLY via SetWaveApproval.
//   - UpdateProgress writes engine progress (current wave, wave status and
//     timestamps, annotations) and never touches status or approval columns.
//   - Targets are written by the engine with optimistic concurrency
//     (UpdateTargets compares updated_at); operator bulk actions use the
//     conditional statements CancelOpenTargets / ResetFailedTargets.
type RolloutStore interface {
	// Create inserts the rollout, its waves (r.Waves) and targets in one
	// transaction.
	Create(ctx context.Context, r *domain.Rollout, targets []domain.RolloutTarget) error
	// Get returns the rollout with waves and computed counts (per wave and
	// total; InProgress = in_progress + verifying).
	Get(ctx context.Context, id uuid.UUID) (*domain.Rollout, error)
	// List returns rollouts newest first, each with Waves and Counts filled
	// (Policy and Annotations may be omitted for list efficiency).
	List(ctx context.Context, f RolloutFilter) ([]domain.Rollout, int, error)
	// UpdateProgress writes current_wave, annotations and, for every wave of
	// r, status, started_at and finished_at. It does NOT write rollout status,
	// status_reason, started_at/finished_at of the rollout, or wave approval
	// columns.
	UpdateProgress(ctx context.Context, r *domain.Rollout) error
	// CompareAndSetStatus sets status and status_reason only if the current
	// status is one of from; sets started_at=now when to == running and
	// started_at IS NULL; sets finished_at=now when to is terminal. Returns
	// false if no row was updated.
	CompareAndSetStatus(ctx context.Context, id uuid.UUID, from []domain.RolloutStatus, to domain.RolloutStatus, reason string) (bool, error)
	// SetWaveApproval records an approval for wave index (only when the wave
	// status is pending or waiting_approval). Returns false otherwise.
	SetWaveApproval(ctx context.Context, id uuid.UUID, wave int, by string, at time.Time) (bool, error)
	ListTargets(ctx context.Context, id uuid.UUID, f TargetFilter) ([]domain.RolloutTarget, int, error)
	// UpdateTargets writes status/attempts/started_at/verified_at/finished_at/
	// last_error/executor_ref/progress of the given targets (matched by rollout id +
	// agent id). Each row is only updated if its updated_at still equals the
	// UpdatedAt of the passed target (optimistic concurrency); the store sets
	// the new updated_at. Returns the agent ids skipped because of a
	// concurrent change.
	UpdateTargets(ctx context.Context, targets []domain.RolloutTarget) (conflicts []domain.AgentID, err error)
	// AddTargets appends targets to an existing rollout (late joiners, always
	// added to the LAST wave by the planner).
	AddTargets(ctx context.Context, id uuid.UUID, targets []domain.RolloutTarget) error
	// CancelOpenTargets sets all targets of the rollout in status pending or
	// waiting_window to cancelled (abort, supersede). Returns the count.
	CancelOpenTargets(ctx context.Context, id uuid.UUID, reason string) (int64, error)
	// ResetFailedTargets sets all failed and skipped targets of the rollout
	// to pending, increments attempts and clears last_error (retry_failed).
	// Returns the count.
	ResetFailedTargets(ctx context.Context, id uuid.UUID) (int64, error)
	// ActiveTargetsForAgent returns targets of non-terminal rollouts for an agent.
	ActiveTargetsForAgent(ctx context.Context, agentID domain.AgentID) ([]domain.RolloutTarget, error)
	// LatestTargetForAgent returns the most recent target (by created/updated
	// time, any rollout status) of the agent in rollouts of the given kind
	// whose TARGET to_ref equals toRef (config hash hex or version), plus the
	// rollout status; ErrNotFound if none. Used by the planner exclusion and
	// catch-up rules (docs/concepts/fleets.md, "When does the offered configuration change?", and docs/concepts/rollouts.md, "When do rollouts arise?").
	LatestTargetForAgent(ctx context.Context, agentID domain.AgentID, kind domain.RolloutKind, toRef string) (*TargetRef, error)
	// DeleteFinishedBefore deletes terminal rollouts finished before t.
	DeleteFinishedBefore(ctx context.Context, before time.Time) (int64, error)
}

// -------------------------------------------------------- Fleet versions ----

// FleetVersionStore persists the UI version selections (one row per fleet).
// The row is the shared state between the replica that serves the REST call
// and the leader that plans the rollout; it is written with compare-and-set
// on updated_at.
type FleetVersionStore interface {
	// Get returns domain.ErrNotFound if the fleet has no selection.
	Get(ctx context.Context, fleet string) (*domain.FleetVersionSelection, error)
	// List returns all selections ordered by fleet name.
	List(ctx context.Context) ([]domain.FleetVersionSelection, error)
	// Put inserts or replaces the selection. expectUpdatedAt == nil: no row
	// may exist; otherwise the row's updated_at must equal it. A mismatch
	// returns domain.ErrConflict. Returns the stored row (updated_at set by
	// the store, planned_at and rollout_id reset by the replacement).
	Put(ctx context.Context, sel domain.FleetVersionSelection, expectUpdatedAt *time.Time) (*domain.FleetVersionSelection, error)
	// Delete removes the row (expectUpdatedAt as in Put; nil = unconditional).
	// Returns false if there was no row. A mismatch returns domain.ErrConflict.
	Delete(ctx context.Context, fleet string, expectUpdatedAt *time.Time) (bool, error)
	// Invalidate sets state=invalidated and the reason (leader, rule K2b) if
	// the row still has the given updated_at (compare-and-set: a selection
	// that was replaced meanwhile is not invalidated). Returns false if no
	// row matched.
	Invalidate(ctx context.Context, fleet, reason string, expectUpdatedAt time.Time) (bool, error)
	// MarkPlanned records that the leader planned the fleet for the selection
	// made at selectedAt (rollout id nil: no rollout was needed). It is a
	// no-op if the row was replaced or removed meanwhile.
	MarkPlanned(ctx context.Context, fleet string, selectedAt time.Time, rolloutID *uuid.UUID, at time.Time) error
}

// ------------------------------------------------------------- Packages ----

// PackageOfferStore holds the desired package set per updater agent. OpAMP
// package offers are always the FULL set: packages missing from the set are
// deleted by the agent's package syncer.
type PackageOfferStore interface {
	// SetOffers replaces the offer set of the updater agent.
	SetOffers(ctx context.Context, agentID domain.AgentID, offers []domain.PackageOffer) error
	GetOffers(ctx context.Context, agentID domain.AgentID) ([]domain.PackageOffer, error)
	// ClearOffers removes all offers of the agent.
	ClearOffers(ctx context.Context, agentID domain.AgentID) error
}

// ------------------------------------------------------------- Releases ----

// ReleaseFilter filters releases.
type ReleaseFilter struct {
	Distribution      string
	IncludePrerelease bool
	// SinceVersion returns releases with version > SinceVersion (semver).
	SinceVersion string
	Limit        int
	Offset       int
}

// ReleaseStore persists detected upstream releases. Lists are ordered by
// semantic version descending.
type ReleaseStore interface {
	// Upsert inserts or updates (all fields except acknowledged_*, detected_at).
	// Returns created=true for new releases.
	Upsert(ctx context.Context, r domain.Release) (bool, error)
	Get(ctx context.Context, distribution, version string) (*domain.Release, error)
	List(ctx context.Context, f ReleaseFilter) ([]domain.Release, int, error)
	// Latest returns the highest version (ErrNotFound if none).
	Latest(ctx context.Context, distribution string, includePrerelease bool) (*domain.Release, error)
	Acknowledge(ctx context.Context, distribution, version, by string, at time.Time) error
}

// ------------------------------------------------------------ Artifacts ----

// ArtifactFilter filters artifacts.
type ArtifactFilter struct {
	Distribution string
	Version      string
	Status       []domain.ArtifactStatus
	Limit        int
	Offset       int
}

// ArtifactStore persists artifact cache metadata (bytes live on disk).
type ArtifactStore interface {
	// Upsert inserts or updates by (distribution, version, os, arch, format);
	// returns the stored artifact (with its id).
	Upsert(ctx context.Context, a domain.Artifact) (*domain.Artifact, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	Find(ctx context.Context, distribution, version, os, arch, format string) (*domain.Artifact, error)
	List(ctx context.Context, f ArtifactFilter) ([]domain.Artifact, int, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// ------------------------------------------------------------- Identity ----

// IdentityStore persists users, sessions, API tokens, enrollment tokens and
// saved filters.
type IdentityStore interface {
	// UpsertUser creates or updates a user by Subject. On update, Email/Name
	// are refreshed; Role is updated only if RoleSource is "mapping".
	UpsertUser(ctx context.Context, u domain.User) (*domain.User, error)
	GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error)
	// LockUser reads the user like GetUser and, inside a transaction (InTx),
	// holds a row lock until the transaction ends, so that a read-modify-write
	// of the account (and of what hangs off it) is not interleaved with the
	// same sequence of another replica. Outside a transaction it is GetUser.
	LockUser(ctx context.Context, id uuid.UUID) (*domain.User, error)
	ListUsers(ctx context.Context) ([]domain.User, error)
	SetUserRole(ctx context.Context, id uuid.UUID, role domain.Role, source string) error
	SetUserDisabled(ctx context.Context, id uuid.UUID, disabled bool) error
	TouchUserLogin(ctx context.Context, id uuid.UUID, at time.Time) error

	CreateSession(ctx context.Context, s domain.Session) error
	GetSession(ctx context.Context, id string) (*domain.Session, error)
	// TouchSession slides the session: last_seen_at becomes at and
	// expires_at moves by the same amount, so the original lifetime
	// (auth.sessionTtl) is kept. Older values of at are ignored (a session is
	// never shortened). Requires CreateSession with last_seen_at=created_at.
	// maxAge > 0 caps the expiry at created_at + maxAge (absolute timeout).
	TouchSession(ctx context.Context, id string, at time.Time, maxAge time.Duration) error
	DeleteSession(ctx context.Context, id string) error
	// DeleteSessionsByUser removes all sessions of a user (disable, role
	// change) and returns how many were removed.
	DeleteSessionsByUser(ctx context.Context, userID uuid.UUID) (int64, error)
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	CreateAPIToken(ctx context.Context, t domain.APIToken) error
	GetAPITokenByHash(ctx context.Context, hash domain.Hash) (*domain.APIToken, error)
	// RevokeAPITokensByCreator revokes all active API tokens created by the
	// user and returns their ids.
	RevokeAPITokensByCreator(ctx context.Context, userID uuid.UUID, at time.Time) ([]uuid.UUID, error)
	ListAPITokens(ctx context.Context) ([]domain.APIToken, error)
	RevokeAPIToken(ctx context.Context, id uuid.UUID, at time.Time) error
	TouchAPIToken(ctx context.Context, id uuid.UUID, at time.Time) error

	CreateEnrollmentToken(ctx context.Context, t domain.EnrollmentToken) error
	GetEnrollmentTokenByHash(ctx context.Context, hash domain.Hash) (*domain.EnrollmentToken, error)
	GetEnrollmentToken(ctx context.Context, id uuid.UUID) (*domain.EnrollmentToken, error)
	// ListActiveEnrollmentTokenIDs returns the ids of tokens that are neither
	// revoked nor expired at the given time.
	ListActiveEnrollmentTokenIDs(ctx context.Context, at time.Time) ([]uuid.UUID, error)
	ListEnrollmentTokens(ctx context.Context) ([]domain.EnrollmentToken, error)
	RevokeEnrollmentToken(ctx context.Context, id uuid.UUID, at time.Time) error
	// UpdateEnrollmentToken changes name, labels and expires_at of the token
	// with t.ID (not the hash, the revocation or the usage counters).
	// ErrNotFound if the token does not exist.
	UpdateEnrollmentToken(ctx context.Context, t domain.EnrollmentToken) error
	// TouchEnrollmentToken sets last_used_at and adds uses (at least 1) to use_count.
	TouchEnrollmentToken(ctx context.Context, id uuid.UUID, at time.Time, uses int) error

	// ListSavedFilters returns the user's own filters plus all shared filters
	// of other users for the page, ordered by name.
	ListSavedFilters(ctx context.Context, userID uuid.UUID, page string) ([]domain.SavedFilter, error)
	CreateSavedFilter(ctx context.Context, f domain.SavedFilter) error
	// DeleteSavedFilter deletes a filter owned by userID (ErrNotFound otherwise).
	DeleteSavedFilter(ctx context.Context, userID, id uuid.UUID) error
}

// ---------------------------------------------------------------- Audit ----

// AuditFilter filters audit events (newest first).
type AuditFilter struct {
	ActorID    string
	Action     string // exact or prefix with trailing "*", e.g. "rollout.*"
	TargetType string
	TargetID   string
	From, To   *time.Time
	Limit      int
	Offset     int
}

// AuditStore is append-only (except retention).
type AuditStore interface {
	Append(ctx context.Context, e domain.AuditEvent) error
	List(ctx context.Context, f AuditFilter) ([]domain.AuditEvent, int, error)
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// ------------------------------------------------------------- Replicas ----

// ReplicaStore tracks running myriarchd replicas (heartbeat every 10s).
type ReplicaStore interface {
	Heartbeat(ctx context.Context, r domain.Replica) error
	List(ctx context.Context) ([]domain.Replica, error)
	// DeleteStale removes replicas with last_heartbeat_at < before and
	// returns their ids (the caller marks their agents offline).
	DeleteStale(ctx context.Context, before time.Time) ([]string, error)
}

// ------------------------------------------------------------------- KV ----

// Key prefixes of short-lived KV markers that housekeeping removes after
// KVMarkerRetention (KVStore.DeleteOlderThan).
const (
	// KVPrefixOIDCUsed marks consumed OIDC login states (single use).
	KVPrefixOIDCUsed = "authn/oidc-used/"
	// KVPrefixWebhookDelivery marks seen GitOps webhook delivery ids.
	KVPrefixWebhookDelivery = "gitops/webhook-delivery/"
	// KVPrefixBootstrapTokenUsed marks consumed one-time tokens of the
	// bootstrap admin (key suffix: hex SHA-256 of the token). Housekeeping
	// never purges these markers: a consumed token stays consumed.
	KVPrefixBootstrapTokenUsed = "authn/bootstrap-token-used/"
	// KVMarkerRetention is how long such markers are kept.
	KVMarkerRetention = time.Hour
)

// KVPrefixPlannedTarget is the prefix of the keys under which the rollout planner records, for each fleet, the
// effective target version that it has handled (PlannedTargetKey). Housekeeping never purges them.
const KVPrefixPlannedTarget = "rollout/planned-target/"

// PlannedTargetKey is the key of the record of a fleet. The value is the effective target version without "v" (empty for
// a fleet without a target): the planner (leader) writes it after it created the version rollout for that target, or found
// that none is needed, and the installation of hosts from the server reads it on every replica. Until the record equals
// the target, a new host is not given the target, because the target reaches the replicas before the leader has
// created the rollout that brings the fleet to it.
func PlannedTargetKey(fleet string) string { return KVPrefixPlannedTarget + fleet }

// KVStore is a small key/value store for component state (release tracker
// ETags, notifier de-duplication, ...). Keys are namespaced by component:
// "release/etag/<repo>", "notify/sent/<key>".
type KVStore interface {
	// Get returns domain.ErrNotFound if the key does not exist.
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	// PutIfAbsent stores only if missing; returns true if stored.
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	Delete(ctx context.Context, key string) error
	// DeleteOlderThan removes the keys with the given prefix that were last
	// written before the cutoff (housekeeping of short-lived markers such as
	// OIDC login states and webhook delivery ids) and returns their number.
	DeleteOlderThan(ctx context.Context, prefix string, before time.Time) (int64, error)
}
