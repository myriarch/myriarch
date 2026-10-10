// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"time"

	"github.com/google/uuid"
)

// RolloutKind distinguishes config and version rollouts.
type RolloutKind string

const (
	RolloutKindConfig  RolloutKind = "config"
	RolloutKindVersion RolloutKind = "version"
)

// RolloutStatus is the state of a rollout (semantics: docs/concepts/rollouts.md,
// "Failure threshold, pause and rollback"). Valid transitions (enforced by the rollout engine, fleet;
// status changes are persisted only via RolloutStore.CompareAndSetStatus):
//
//	pending -> running | waiting_approval | superseded | aborted
//	running -> waiting_approval | paused | completed | failed | aborted | rolling_back | superseded
//	waiting_approval -> running | aborted | superseded
//	paused -> running (resume) | aborted | rolling_back | superseded
//	failed -> rolling_back | aborted | running (retry_failed) (no automatic transitions)
//	rolling_back -> rolled_back | failed
//	completed, aborted, rolled_back, superseded: terminal
//
// "failed" is entered when (a) a rollback could not be completed (targets did
// not return to their FromRef within ApplyTimeout) or (b) the rollout cannot
// proceed at all (e.g. executor misconfigured: every startable target of a
// wave failed in executor.Start, or the policy/fleet disappeared). A reached
// failure threshold leads to "paused" (OnFailure=pause) or "rolling_back"
// (OnFailure=rollback); the wave itself is marked WaveFailed in both cases.
type RolloutStatus string

const (
	RolloutPending         RolloutStatus = "pending"
	RolloutRunning         RolloutStatus = "running"
	RolloutWaitingApproval RolloutStatus = "waiting_approval"
	RolloutPaused          RolloutStatus = "paused"
	RolloutCompleted       RolloutStatus = "completed"
	RolloutFailed          RolloutStatus = "failed"
	RolloutAborted         RolloutStatus = "aborted"
	RolloutRollingBack     RolloutStatus = "rolling_back"
	RolloutRolledBack      RolloutStatus = "rolled_back"
	RolloutSuperseded      RolloutStatus = "superseded"
)

// IsTerminal reports whether no further automatic transitions happen.
func (s RolloutStatus) IsTerminal() bool {
	switch s {
	case RolloutCompleted, RolloutAborted, RolloutRolledBack, RolloutSuperseded:
		return true
	}
	return false
}

// WaveStatus is the state of one wave.
type WaveStatus string

const (
	WavePending         WaveStatus = "pending"
	WaveWaitingApproval WaveStatus = "waiting_approval"
	WaveRunning         WaveStatus = "running"
	WaveCompleted       WaveStatus = "completed"
	WaveFailed          WaveStatus = "failed"
	WaveSkipped         WaveStatus = "skipped"
)

// TargetStatus is the state of one agent within a rollout.
type TargetStatus string

const (
	TargetPending       TargetStatus = "pending"
	TargetWaitingWindow TargetStatus = "waiting_window" // outside maintenance window
	TargetInProgress    TargetStatus = "in_progress"    // change offered / executor started
	TargetVerifying     TargetStatus = "verifying"      // success reported, health wait running
	TargetSucceeded     TargetStatus = "succeeded"
	TargetFailed        TargetStatus = "failed"
	TargetSkipped       TargetStatus = "skipped" // offline beyond grace period / left fleet
	TargetCancelled     TargetStatus = "cancelled"
	TargetRolledBack    TargetStatus = "rolled_back"
)

// AllTargetStatuses lists every defined target status.
func AllTargetStatuses() []TargetStatus {
	return []TargetStatus{
		TargetPending, TargetWaitingWindow, TargetInProgress, TargetVerifying, TargetSucceeded,
		TargetFailed, TargetSkipped, TargetCancelled, TargetRolledBack,
	}
}

// UnknownActivityRank is the ActivityRank of a status that is not defined.
const UnknownActivityRank = 7

// ActivityRank orders target statuses by how much attention they need right
// now; lower ranks come first in the "active first" list of a rollout:
// 0 in_progress and verifying (work is running), 1 waiting_window, 2 failed
// and rolled_back, 3 pending, 4 succeeded, 5 skipped, 6 cancelled. An unknown
// status ranks UnknownActivityRank. The web UI mirrors this table in
// web/src/lib/targetOrder.ts; keep both in sync.
func (s TargetStatus) ActivityRank() int {
	switch s {
	case TargetInProgress, TargetVerifying:
		return 0
	case TargetWaitingWindow:
		return 1
	case TargetFailed, TargetRolledBack:
		return 2
	case TargetPending:
		return 3
	case TargetSucceeded:
		return 4
	case TargetSkipped:
		return 5
	case TargetCancelled:
		return 6
	}
	return UnknownActivityRank
}

// Rollout is a persisted rollout state machine instance (owned by the
// rollout engine; written only by the leader replica).
type Rollout struct {
	ID         uuid.UUID     `json:"id"`
	Kind       RolloutKind   `json:"kind"`
	FleetName  string        `json:"fleetName"`
	PolicyName string        `json:"policyName"`
	Policy     RolloutPolicy `json:"policy"` // snapshot at creation
	Status     RolloutStatus `json:"status"`
	// StatusReason explains the last transition (English, shown in UI).
	StatusReason string `json:"statusReason,omitempty"`
	// FromRef/ToRef of the ROLLOUT: config rollouts use Git commits (ToRef =
	// commit of the snapshot that produced the new assigned configs, FromRef =
	// previously applied commit or ""), version rollouts use versions without
	// "v" (FromRef "" when targets start from mixed versions). Per-agent
	// config hashes are in RolloutTarget.FromRef/ToRef.
	FromRef string `json:"fromRef,omitempty"`
	ToRef   string `json:"toRef"`
	// GitCommit that triggered the rollout (empty for manual).
	GitCommit string       `json:"gitCommit,omitempty"`
	Executor  ExecutorType `json:"executor,omitempty"` // version rollouts only
	// CurrentWave is the index of the active (or next) wave.
	CurrentWave int           `json:"currentWave"`
	Waves       []RolloutWave `json:"waves"`
	Counts      RolloutCounts `json:"counts"`
	CreatedBy   string        `json:"createdBy"` // stored reference "gitops", "user:<id>", "token:<id>", "system"; the API presents display names
	// Origin is derived from the stored CreatedBy (OriginOf) by the store.
	Origin RolloutOrigin `json:"origin"`
	// RequestReason is the reason an operator gave when selecting the target
	// version (annotation "selection", origin ui).
	RequestReason string         `json:"requestReason,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
	StartedAt     *time.Time     `json:"startedAt,omitempty"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
	Annotations   map[string]any `json:"annotations,omitempty"`
}

// RolloutWave is the runtime state of one wave.
type RolloutWave struct {
	Index           int        `json:"index"`
	Name            string     `json:"name"`
	Selector        *Selector  `json:"selector,omitempty"`
	Percent         int        `json:"percent,omitempty"`
	RequireApproval bool       `json:"requireApproval"`
	Status          WaveStatus `json:"status"`
	ApprovedBy      string     `json:"approvedBy,omitempty"`
	ApprovedAt      *time.Time `json:"approvedAt,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	// Counts for this wave (computed from targets).
	Counts RolloutCounts `json:"counts"`
}

// RolloutCounts aggregates target states.
type RolloutCounts struct {
	Total         int `json:"total"`
	Pending       int `json:"pending"`
	WaitingWindow int `json:"waitingWindow"`
	InProgress    int `json:"inProgress"` // in_progress + verifying
	Succeeded     int `json:"succeeded"`
	Failed        int `json:"failed"`
	Skipped       int `json:"skipped"`
	Cancelled     int `json:"cancelled"`
	RolledBack    int `json:"rolledBack"`
}

// RolloutTarget is one agent in a rollout.
type RolloutTarget struct {
	RolloutID uuid.UUID    `json:"rolloutId"`
	AgentID   AgentID      `json:"agentId"`
	Hostname  string       `json:"hostname,omitempty"` // denormalized for UI
	Wave      int          `json:"wave"`
	Status    TargetStatus `json:"status"`
	// FromRef/ToRef of the TARGET: config rollouts use config hashes (hex,
	// FromRef = desired hash before the rollout), version rollouts use
	// versions without "v".
	FromRef    string     `json:"fromRef,omitempty"`
	ToRef      string     `json:"toRef"`
	Attempts   int        `json:"attempts"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"` // success reported, health wait began
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	LastError  string     `json:"lastError,omitempty"`
	// ExecutorRef is an external reference (e.g. AWX job id, webhook delivery id).
	ExecutorRef string    `json:"executorRef,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Progress is the live progress of a version-rollout target (nil for
	// config rollouts and targets that never started).
	Progress *TargetProgress `json:"progress,omitempty"`
}

// RolloutAction is an operator action on a rollout.
type RolloutAction string

const (
	ActionPause       RolloutAction = "pause"
	ActionResume      RolloutAction = "resume"
	ActionAbort       RolloutAction = "abort"
	ActionApprove     RolloutAction = "approve"
	ActionRollback    RolloutAction = "rollback"
	ActionRetryFailed RolloutAction = "retry_failed"
)

// PackageOffer is a package the server offers to an updater agent (OpAMP
// PackagesAvailable entry). Written by the updater executor (internal/executor/updaterexec), read by
// the OpAMP layer (internal/opamp) which builds the protobuf message.
//
// Layout follows the upstream supervisor "Collector Executable Updates"
// draft (cmd/opampsupervisor/specification/README.md) where possible: the
// collector package has the EMPTY name "", type top-level, Version
// "v0.163.0", DownloadURL pointing to the release archive (.tar.gz for all
// OSes), ContentHash = SHA-256 of the archive (raw 32 bytes).
//
// Deviation (documented in
// docs/adr/0007-package-offers-in-upstream-format-with-sigstore-bundles.md): Signature carries
// the raw Sigstore bundle JSON (content of "<asset>.sigstore.json", media type
// application/vnd.dev.sigstore.bundle.v0.3+json) instead of the draft's
// "<b64 cert> <b64 sig>" pair, because releases >= v0.158.0 only publish
// bundles and bundles can be verified offline (Rekor inclusion proof + TSA
// timestamp included). myriarch-updater detects the format by the leading "{".
type PackageOffer struct {
	AgentID AgentID `json:"agentId"` // the updater agent
	// Name is the OpAMP package name. CollectorPackageName ("") for the
	// collector; SupervisorPackageName for the supervisor (future).
	Name        string            `json:"name"`
	Version     string            `json:"version"` // with leading "v" (upstream convention)
	DownloadURL string            `json:"downloadUrl"`
	ContentHash Hash              `json:"contentHash"`
	Signature   []byte            `json:"signature,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	// RolloutID that created the offer (for correlation).
	RolloutID *uuid.UUID `json:"rolloutId,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

const (
	// CollectorPackageName is the OpAMP package name of the collector binary
	// (upstream supervisor convention: empty name).
	CollectorPackageName = ""
	// SupervisorPackageName is reserved for supervisor binary updates
	// (roadmap; not offered in MVP).
	SupervisorPackageName = "opampsupervisor"
)
