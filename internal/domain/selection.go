// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/brand"
)

// ---------------------------------------------------------------------------
// Frame in Git: spec.update.selection (docs/concepts/fleets.md, "Target version from the UI within the Git frame")
// ---------------------------------------------------------------------------

// VersionSelectionSpec is spec.update.selection: the frame within which
// operators may choose the target collector version of a fleet in the UI/API.
// Git sets the frame, the UI chooses inside it.
type VersionSelectionSpec struct {
	// Enabled allows UI/API-triggered version updates (default false).
	Enabled bool `json:"enabled" yaml:"enabled"`
	// VersionRange is a Masterminds semver v3 constraint over versions
	// without "v" (">=0.161.0, <=0.162.0"). Exactly one of VersionRange and
	// Versions is set when enabled.
	VersionRange string `json:"versionRange,omitempty" yaml:"versionRange,omitempty"`
	// Versions is an explicit list of selectable versions.
	Versions []string `json:"versions,omitempty" yaml:"versions,omitempty"`
	// AllowDowngrade permits versions lower than the current effective target.
	AllowDowngrade bool `json:"allowDowngrade" yaml:"allowDowngrade,omitempty"`
	// RequireReason makes a reason mandatory when selecting or clearing.
	RequireReason bool `json:"requireReason" yaml:"requireReason,omitempty"`
	// RolloutPolicy names the policy for rollouts caused by a UI selection
	// (default spec.rollout.versionPolicy, then "default").
	RolloutPolicy string `json:"rolloutPolicy,omitempty" yaml:"rolloutPolicy,omitempty"`
}

// Active reports s != nil && s.Enabled.
func (s *VersionSelectionSpec) Active() bool { return s != nil && s.Enabled }

// Allows reports whether version (a strict semantic version, a leading "v" is
// ignored) lies in the frame: VersionRange via the constraint (a prerelease
// never satisfies a range without prerelease part), Versions by exact match.
// A nil spec, an unparsable version or an unparsable range allows nothing.
func (s *VersionSelectionSpec) Allows(version string) bool {
	if s == nil {
		return false
	}
	v, err := semver.StrictNewVersion(NormalizeVersion(version))
	if err != nil {
		return false
	}
	switch {
	case s.VersionRange != "":
		c, err := semver.NewConstraint(s.VersionRange)
		return err == nil && c.Check(v)
	case len(s.Versions) > 0:
		for _, x := range s.Versions {
			if xv, err := semver.StrictNewVersion(NormalizeVersion(x)); err == nil && xv.Equal(v) {
				return true
			}
		}
	}
	return false
}

// FrameText renders the frame for messages: the range, or "[a, b]" for a list.
func (s *VersionSelectionSpec) FrameText() string {
	switch {
	case s == nil:
		return "no frame"
	case s.VersionRange != "":
		return s.VersionRange
	case len(s.Versions) > 0:
		return "[" + strings.Join(s.Versions, ", ") + "]"
	}
	return "no versions"
}

// ---------------------------------------------------------------------------
// Selection (state) and effective target
// ---------------------------------------------------------------------------

// VersionSelectionState is the state of a stored selection.
type VersionSelectionState string

const (
	// SelectionActive: the selected version is the effective target.
	SelectionActive VersionSelectionState = "active"
	// SelectionInvalidated: a Git change excluded the version from the frame;
	// the fleet is on hold (no target).
	SelectionInvalidated VersionSelectionState = "invalidated"
)

// VersionTargetSource tells where the effective target of a fleet comes from.
type VersionTargetSource string

const (
	TargetSourceGit  VersionTargetSource = "git"
	TargetSourceUI   VersionTargetSource = "ui"
	TargetSourceHold VersionTargetSource = "hold"
	TargetSourceNone VersionTargetSource = "none"
)

// FleetVersionSelection is the target version an operator chose in the UI/API
// (at most one per fleet). SelectedBy is the stored actor reference
// ("user:<uuid>", "token:<uuid>"); the API presents display names.
type FleetVersionSelection struct {
	Fleet       string                `json:"fleet"`
	Version     string                `json:"version"`
	State       VersionSelectionState `json:"state"`
	StateReason string                `json:"stateReason,omitempty"`
	// GitVersion / GitCommit: spec.collector.version and the applied GitOps
	// revision at the time of the selection.
	GitVersion string     `json:"gitVersion,omitempty"`
	GitCommit  string     `json:"gitCommit,omitempty"`
	SelectedBy string     `json:"selectedBy"`
	SelectedAt time.Time  `json:"selectedAt"`
	Reason     string     `json:"reason,omitempty"`
	RolloutID  *uuid.UUID `json:"rolloutId,omitempty"`
	PlannedAt  *time.Time `json:"plannedAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// VersionTarget is the effective target version of a fleet.
type VersionTarget struct {
	// Version without "v" ("" = no target).
	Version string
	Source  VersionTargetSource
	// Selection is the stored selection (nil if there is none).
	Selection *FleetVersionSelection
}

// ResolveVersionTarget computes the effective target version of a fleet: an
// active UI selection, else the Git target spec.collector.version. An
// invalidated selection puts the fleet on hold (no target). It is the only
// place that decides which version a fleet rolls out to.
func ResolveVersionTarget(f *Fleet, sel *FleetVersionSelection) VersionTarget {
	if sel != nil {
		if sel.State == SelectionInvalidated {
			return VersionTarget{Source: TargetSourceHold, Selection: sel}
		}
		return VersionTarget{Version: NormalizeVersion(sel.Version), Source: TargetSourceUI, Selection: sel}
	}
	if f != nil {
		if v := NormalizeVersion(f.Collector.Version); v != "" {
			return VersionTarget{Version: v, Source: TargetSourceGit}
		}
	}
	return VersionTarget{Source: TargetSourceNone}
}

// VersionRolloutsEnabled reports whether the fleet changes collector versions
// through an executor (everything but "none").
func VersionRolloutsEnabled(f *Fleet) bool {
	switch f.Update.Executor {
	case ExecutorUpdater, ExecutorWebhook, ExecutorAWX, ExecutorManual:
		return true
	}
	return false
}

// SelectionDecision is the outcome of ReconcileSelection (rules K1 to K5).
type SelectionDecision string

const (
	SelectionKeep       SelectionDecision = "keep"
	SelectionRemove     SelectionDecision = "removed"     // K4: the fleet is gone
	SelectionSupersede  SelectionDecision = "superseded"  // K1: Git changed the target, K2a: Git withdrew the UI control
	SelectionInvalidate SelectionDecision = "invalidated" // K2b: the version is outside the new frame
)

// ReconcileSelection evaluates the conflict rules between a stored selection
// and the current Git state, in this order (first match wins):
//
//	K4  the fleet no longer exists (f == nil)            -> removed
//	K1  spec.collector.version differs from the version
//	    recorded with the selection                      -> superseded (Git wins)
//	K2a selection disabled/removed, or executor none     -> superseded (Git target applies)
//	K2b an active selection lies outside the frame       -> invalidated (hold)
//	K5  otherwise                                        -> keep
//
// The returned reason is a short English text for audit and state_reason.
// commit is the applied GitOps revision (used in the K2b text).
func ReconcileSelection(f *Fleet, sel FleetVersionSelection, commit string) (SelectionDecision, string) {
	if f == nil {
		return SelectionRemove, "the fleet no longer exists in Git"
	}
	if now, was := NormalizeVersion(f.Collector.Version), NormalizeVersion(sel.GitVersion); now != was {
		return SelectionSupersede, fmt.Sprintf("Git changed collector.version from %s to %s", orNoneText(was), orNoneText(now))
	}
	if !f.Update.Selection.Active() {
		return SelectionSupersede, "selection disabled"
	}
	if !VersionRolloutsEnabled(f) {
		return SelectionSupersede, "executor none"
	}
	if sel.State == SelectionActive && !f.Update.Selection.Allows(sel.Version) {
		at := ""
		if commit != "" {
			at = " of commit " + shortCommit(commit)
		}
		return SelectionInvalidate, fmt.Sprintf("version %s is outside the frame%s (%s)", NormalizeVersion(sel.Version), at, f.Update.Selection.FrameText())
	}
	return SelectionKeep, ""
}

func orNoneText(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// ---------------------------------------------------------------------------
// Updater contract (docs/design/updater-protocol.md, Update health component)
// ---------------------------------------------------------------------------

const (
	// UpdaterUpdateComponent is the health component in which the
	// myriarch-updater reports the steps of its latest update attempt.
	UpdaterUpdateComponent = brand.UpdateHealthComponent
	// UpdaterStepPrefix prefixes the step children ("step:downloading").
	UpdaterStepPrefix = "step:"
	// UpdaterVersionChild / UpdaterFromChild carry the offered version and the
	// version before the attempt (status "v0.162.0").
	UpdaterVersionChild = "version"
	UpdaterFromChild    = "from"
)

// ---------------------------------------------------------------------------
// Live progress of a version-rollout target
// ---------------------------------------------------------------------------

// UpdatePhase is the current phase of a version update on one target.
type UpdatePhase string

const (
	PhaseOffered     UpdatePhase = "offered"
	PhaseDownloading UpdatePhase = "downloading"
	PhaseVerifying   UpdatePhase = "verifying"
	PhaseInstalling  UpdatePhase = "installing"
	PhaseRestarting  UpdatePhase = "restarting"
	PhaseHealthCheck UpdatePhase = "health_check"
	PhaseRollingBack UpdatePhase = "rolling_back"
	PhaseHealthWait  UpdatePhase = "health_wait"
	PhaseSucceeded   UpdatePhase = "succeeded"
	PhaseFailed      UpdatePhase = "failed"
	PhaseRolledBack  UpdatePhase = "rolled_back"
	PhaseCancelled   UpdatePhase = "cancelled"
)

// IsFinal reports whether the phase is a final target state.
func (p UpdatePhase) IsFinal() bool {
	switch p {
	case PhaseSucceeded, PhaseFailed, PhaseRolledBack, PhaseCancelled:
		return true
	}
	return false
}

// UpdateStepName names one step of an update.
type UpdateStepName string

const (
	StepOffered     UpdateStepName = "offered"
	StepDownloading UpdateStepName = "downloading"
	StepVerifying   UpdateStepName = "verifying"
	StepInstalling  UpdateStepName = "installing"
	StepRestarting  UpdateStepName = "restarting"
	StepHealthCheck UpdateStepName = "health_check"
	StepRollingBack UpdateStepName = "rolling_back"
	StepHealthWait  UpdateStepName = "health_wait"
)

// HostSteps are the steps the myriarch-updater reports, in order.
var HostSteps = []UpdateStepName{StepDownloading, StepVerifying, StepInstalling, StepRestarting, StepHealthCheck}

// UpdateStepStatus is the state of one step.
type UpdateStepStatus string

const (
	StepPending UpdateStepStatus = "pending"
	StepRunning UpdateStepStatus = "running"
	StepDone    UpdateStepStatus = "done"
	StepFailed  UpdateStepStatus = "failed"
	StepSkipped UpdateStepStatus = "skipped"
)

// ProgressSource tells who reported the steps.
type ProgressSource string

const (
	// ProgressUpdater: steps reported by the myriarch-updater (health component).
	ProgressUpdater ProgressSource = "updater"
	// ProgressPackageStatus: only the OpAMP package status of the updater is known.
	ProgressPackageStatus ProgressSource = "package_status"
	// ProgressExecutor: webhook/awx/manual executors (no host-side steps).
	ProgressExecutor ProgressSource = "executor"
)

// UpdateStep is one step of an update.
type UpdateStep struct {
	Name       UpdateStepName   `json:"name"`
	Status     UpdateStepStatus `json:"status"`
	StartedAt  *time.Time       `json:"startedAt,omitempty"`
	FinishedAt *time.Time       `json:"finishedAt,omitempty"`
	Detail     string           `json:"detail,omitempty"`
}

// TargetProgress is the live progress of a version-rollout target, derived by
// the rollout engine and stored with the target.
type TargetProgress struct {
	Phase           UpdatePhase    `json:"phase"`
	Source          ProgressSource `json:"source"`
	Attempt         int            `json:"attempt,omitempty"`
	FromVersion     string         `json:"fromVersion,omitempty"`
	ToVersion       string         `json:"toVersion,omitempty"`
	DownloadPercent *float64       `json:"downloadPercent,omitempty"`
	Error           string         `json:"error,omitempty"`
	Steps           []UpdateStep   `json:"steps"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// SameContent compares two progress values ignoring UpdatedAt.
func (p *TargetProgress) SameContent(o *TargetProgress) bool {
	if p == nil || o == nil {
		return p == o
	}
	if p.Phase != o.Phase || p.Source != o.Source || p.Attempt != o.Attempt || p.FromVersion != o.FromVersion ||
		p.ToVersion != o.ToVersion || p.Error != o.Error || len(p.Steps) != len(o.Steps) {
		return false
	}
	if (p.DownloadPercent == nil) != (o.DownloadPercent == nil) || (p.DownloadPercent != nil && *p.DownloadPercent != *o.DownloadPercent) {
		return false
	}
	for i := range p.Steps {
		a, b := p.Steps[i], o.Steps[i]
		if a.Name != b.Name || a.Status != b.Status || a.Detail != b.Detail || !sameTime(a.StartedAt, b.StartedAt) || !sameTime(a.FinishedAt, b.FinishedAt) {
			return false
		}
	}
	return true
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// ---------------------------------------------------------------------------
// Rollout origin
// ---------------------------------------------------------------------------

// RolloutOrigin tells what caused a rollout.
type RolloutOrigin string

const (
	// OriginGitOps: the goal comes from Git.
	OriginGitOps RolloutOrigin = "gitops"
	// OriginUI: an operator selected the target version in the UI/API.
	OriginUI RolloutOrigin = "ui"
	// OriginSystem: anything else.
	OriginSystem RolloutOrigin = "system"
)

// OriginOf derives the origin from the stored creator reference.
func OriginOf(createdBy string) RolloutOrigin {
	switch {
	case createdBy == "gitops":
		return OriginGitOps
	case strings.HasPrefix(createdBy, "user:"), strings.HasPrefix(createdBy, "token:"):
		return OriginUI
	}
	return OriginSystem
}

// SelectionAnnotation is the rollout annotation that records a UI selection.
const SelectionAnnotation = "selection"

// ProgressOf returns the progress of a target as the API presents it: a copy
// whose phase and steps agree with a final target status. A target that was
// ended from outside the engine's evaluation (bulk cancel by abort or
// supersede, rollback) keeps the phase it had when it was written; here the
// phase is taken from the status and running or pending steps are
// resolved (cancelled/rolled back/skipped: skipped; failed: the running step
// failed, later steps are skipped; succeeded: done). Nil if the target has no
// progress.
func ProgressOf(t RolloutTarget) *TargetProgress {
	if t.Progress == nil {
		return nil
	}
	p := *t.Progress
	p.Steps = append([]UpdateStep{}, t.Progress.Steps...)
	if t.Progress.DownloadPercent != nil {
		v := *t.Progress.DownloadPercent
		p.DownloadPercent = &v
	}
	final, ok := finalPhaseOf(t.Status)
	// a failed target whose host rolled back keeps the more precise phase
	if !ok || p.Phase == final || (t.Status == TargetFailed && p.Phase == PhaseRolledBack) {
		return &p
	}
	p.Phase = final
	p.DownloadPercent = nil
	failedSeen := false
	for i := range p.Steps {
		s := &p.Steps[i]
		switch s.Status {
		case StepRunning:
			switch t.Status {
			case TargetFailed:
				s.Status, failedSeen = StepFailed, true
				if t.LastError != "" {
					s.Detail = t.LastError
				}
			case TargetSucceeded:
				s.Status = StepDone
			default:
				s.Status = StepSkipped
			}
		case StepPending:
			if t.Status == TargetSucceeded {
				s.Status = StepDone
			} else {
				s.Status = StepSkipped
			}
		}
	}
	if t.Status == TargetFailed && !failedSeen && p.Error == "" {
		p.Error = t.LastError
	}
	if p.Error == "" && (t.Status == TargetFailed || t.Status == TargetCancelled || t.Status == TargetSkipped) {
		p.Error = t.LastError
	}
	return &p
}

// finalPhaseOf maps a final target status to the phase of its progress.
func finalPhaseOf(s TargetStatus) (UpdatePhase, bool) {
	switch s {
	case TargetSucceeded:
		return PhaseSucceeded, true
	case TargetFailed:
		return PhaseFailed, true
	case TargetRolledBack:
		return PhaseRolledBack, true
	case TargetCancelled, TargetSkipped:
		return PhaseCancelled, true
	}
	return "", false
}
