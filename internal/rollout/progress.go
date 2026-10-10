// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"strings"
	"time"

	"github.com/myriarch/myriarch/internal/domain"
)

// Live progress of version-rollout targets (docs/design/version-selection.md,
// Target progress).
//
// The engine derives a domain.TargetProgress for every in-flight target of a
// version rollout on each tick, stores it with the target when its content
// changed and freezes it when the target reaches a final status. Three
// sources feed it, in this order of preference:
//
//   - the myriarch-updater's health component `dev.myriarch.update` (the steps of
//     its latest update attempt, reported as state, not as events: missing an
//     intermediate report does no harm),
//   - the OpAMP package status of the updater (older or simulated updaters
//     without the component: downloading with a percentage, installing,
//     install_failed),
//   - the executor alone (webhook, awx, manual: offered, installing until the
//     collector reports the version, health wait).
//
// The target status always has the last word: verifying means the collector
// reports the version and the health wait of the policy runs, a final status
// settles every step.

// attemptClockSkew is the tolerance when a reported update attempt is matched
// with a target by its start time (the updater's clock may differ from the
// server's).
const attemptClockSkew = 2 * time.Minute

// maxProgressError bounds the error text kept with a progress.
const maxProgressError = 1000

// hostAttempt is the latest update attempt a myriarch-updater reports.
type hostAttempt struct {
	status     string
	lastError  string
	start      time.Time
	statusTime time.Time
	steps      map[domain.UpdateStepName]*domain.ComponentHealth
}

func (a *hostAttempt) final() bool {
	switch a.status {
	case "succeeded", "failed", "rolled_back":
		return true
	}
	return false
}

func tv(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// reportsUpdateComponent tells whether the updater reports the update
// component at all (an updater that does, reports it for every attempt).
func reportsUpdateComponent(up *domain.Agent) bool {
	return up != nil && up.Health != nil && up.Health.Components[domain.UpdaterUpdateComponent] != nil
}

// attemptFor returns the update attempt of the updater that belongs to the
// target: the offered version equals "v"+toRef and the attempt started not
// before the target (with a tolerance for clock skew). nil if the updater does
// not (yet) report such an attempt.
func attemptFor(up *domain.Agent, t *domain.RolloutTarget) *hostAttempt {
	if !reportsUpdateComponent(up) {
		return nil
	}
	comp := up.Health.Components[domain.UpdaterUpdateComponent]
	ver := comp.Components[domain.UpdaterVersionChild]
	if ver == nil || domain.NormalizeVersion(ver.Status) != t.ToRef {
		return nil
	}
	a := &hostAttempt{
		status: comp.Status, lastError: comp.LastError, start: tv(comp.StartTime), statusTime: tv(comp.StatusTime),
		steps: map[domain.UpdateStepName]*domain.ComponentHealth{},
	}
	if a.statusTime.IsZero() {
		a.statusTime = a.start
	}
	if t.StartedAt != nil && a.start.Before(t.StartedAt.Add(-attemptClockSkew)) {
		return nil // an older attempt
	}
	for name, child := range comp.Components {
		if step, ok := strings.CutPrefix(name, domain.UpdaterStepPrefix); ok && child != nil {
			a.steps[domain.UpdateStepName(step)] = child
		}
	}
	// A retry offers the same version again within the tolerance of the
	// previous attempt: while the target runs and the reported attempt
	// already ended in a failure from before the target started, the updater
	// has not begun the new attempt yet.
	if t.Status == domain.TargetInProgress && t.Attempts > 1 && t.StartedAt != nil &&
		(a.status == "failed" || a.status == "rolled_back") && a.statusTime.Before(*t.StartedAt) {
		return nil
	}
	return a
}

// packageStatusFor returns the collector package status the updater reports
// for the offered version (nil if it has none for this target).
func packageStatusFor(up *domain.Agent, t *domain.RolloutTarget) *domain.PackageStatus {
	if up == nil || up.PackageStatus == nil {
		return nil
	}
	ps := up.PackageStatus.Packages[domain.CollectorPackageName]
	if ps == nil || ps.ServerOfferedVersion != "v"+t.ToRef || !fresh(&up.PackageStatus.UpdatedAt, t.StartedAt) {
		return nil
	}
	return ps
}

func copyTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	c := t
	return &c
}

func ptr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// computeProgress derives the progress of a version-rollout target. col is the
// collector agent, up its linked updater (either may be nil).
func computeProgress(r *domain.Rollout, t *domain.RolloutTarget, col, up *domain.Agent, now time.Time) *domain.TargetProgress {
	if r.Kind != domain.RolloutKindVersion {
		return nil
	}
	switch t.Status {
	case domain.TargetPending, domain.TargetWaitingWindow:
		return nil
	}
	if t.StartedAt == nil && t.Status != domain.TargetFailed {
		return nil // never started (skipped or cancelled while pending)
	}
	var (
		p   *domain.TargetProgress
		att *hostAttempt
	)
	if r.Executor != domain.ExecutorUpdater {
		p = executorProgress(t)
	} else {
		p, att = updaterProgress(t, up)
	}
	p.Attempt, p.FromVersion, p.ToVersion, p.UpdatedAt = t.Attempts, t.FromRef, t.ToRef, now
	settle(p, t, att)
	if len(p.Error) > maxProgressError {
		p.Error = p.Error[:maxProgressError]
	}
	return p
}

// ------------------------------------------------------- executor source ----

// executorProgress is the progress of targets whose update an executor other
// than the updater started: nothing is known about the host, only that the
// update was handed over and when the collector reports the version.
func executorProgress(t *domain.RolloutTarget) *domain.TargetProgress {
	p := &domain.TargetProgress{Source: domain.ProgressExecutor}
	offered := domain.UpdateStep{Name: domain.StepOffered, Status: domain.StepRunning}
	if t.StartedAt != nil {
		offered.Status, offered.StartedAt, offered.FinishedAt = domain.StepDone, ptr(t.StartedAt), ptr(t.StartedAt)
	}
	installing := domain.UpdateStep{Name: domain.StepInstalling, Status: domain.StepPending}
	if t.StartedAt != nil {
		installing.Status, installing.StartedAt = domain.StepRunning, ptr(t.StartedAt)
		installing.Detail = "external update, waiting for version " + t.ToRef
	}
	p.Steps = []domain.UpdateStep{offered, installing, {Name: domain.StepHealthWait, Status: domain.StepPending}}
	return p
}

// -------------------------------------------------------- updater source ----

func updaterProgress(t *domain.RolloutTarget, up *domain.Agent) (*domain.TargetProgress, *hostAttempt) {
	att := attemptFor(up, t)
	ps := packageStatusFor(up, t)

	var p *domain.TargetProgress
	switch {
	case att != nil:
		p = &domain.TargetProgress{Source: domain.ProgressUpdater, Steps: hostSteps(t, att)}
		if att.final() {
			// the host ended its attempt; for a failure the target status settles
			// the rest, for a success the collector's version report follows
			p.Error = att.lastError
		}
	case ps != nil:
		p = &domain.TargetProgress{Source: domain.ProgressPackageStatus, Steps: packageSteps(t, ps)}
	default:
		// the offer was made, the updater has not reported anything for it yet
		src := domain.ProgressPackageStatus
		steps := []domain.UpdateStep{offeredStep(t, nil), {Name: domain.StepHealthWait, Status: domain.StepPending}}
		if reportsUpdateComponent(up) {
			src, steps = domain.ProgressUpdater, hostSteps(t, nil)
		}
		p = &domain.TargetProgress{Source: src, Steps: steps}
	}
	if ps != nil && ps.Status == domain.PackageDownloading {
		pct := ps.DownloadPercent
		p.DownloadPercent = &pct
	}
	return p, att
}

// offeredStep is the first step: done as soon as the target started; it ends
// when the updater began working on the offer (the start of its attempt).
func offeredStep(t *domain.RolloutTarget, att *hostAttempt) domain.UpdateStep {
	s := domain.UpdateStep{Name: domain.StepOffered, Status: domain.StepRunning, StartedAt: ptr(t.StartedAt)}
	if att != nil {
		end := att.start
		if t.StartedAt != nil && end.Before(*t.StartedAt) {
			end = *t.StartedAt
		}
		s.Status, s.FinishedAt = domain.StepDone, copyTime(end)
	}
	return s
}

// hostSteps lists the steps of an update attempt the myriarch-updater reported
// (att may be nil: nothing reported yet, all host steps pending).
func hostSteps(t *domain.RolloutTarget, att *hostAttempt) []domain.UpdateStep {
	steps := []domain.UpdateStep{offeredStep(t, att)}
	names := append([]domain.UpdateStepName(nil), domain.HostSteps...)
	if att != nil && att.steps[domain.StepRollingBack] != nil {
		names = append(names, domain.StepRollingBack)
	}
	for _, name := range names {
		s := domain.UpdateStep{Name: name, Status: domain.StepPending}
		if att != nil {
			if c := att.steps[name]; c != nil {
				s.Status = stepStatus(c.Status)
				s.StartedAt = ptr(c.StartTime)
				if s.Status != domain.StepRunning && s.Status != domain.StepPending {
					s.FinishedAt = ptr(c.StatusTime)
					if s.FinishedAt == nil {
						s.FinishedAt = ptr(c.StartTime)
					}
				}
				if c.LastError != "" {
					s.Detail = c.LastError
				}
			}
		}
		steps = append(steps, s)
	}
	// health_wait sits after rolling_back in the fixed order
	return append(steps, domain.UpdateStep{Name: domain.StepHealthWait, Status: domain.StepPending})
}

func stepStatus(s string) domain.UpdateStepStatus {
	switch domain.UpdateStepStatus(s) {
	case domain.StepRunning, domain.StepDone, domain.StepFailed, domain.StepSkipped:
		return domain.UpdateStepStatus(s)
	}
	return domain.StepPending
}

// packageSteps is the progress derived from the OpAMP package status alone.
func packageSteps(t *domain.RolloutTarget, ps *domain.PackageStatus) []domain.UpdateStep {
	steps := []domain.UpdateStep{
		{Name: domain.StepOffered, Status: domain.StepDone, StartedAt: ptr(t.StartedAt), FinishedAt: ptr(t.StartedAt)},
		{Name: domain.StepDownloading, Status: domain.StepPending},
		{Name: domain.StepInstalling, Status: domain.StepPending},
		{Name: domain.StepHealthWait, Status: domain.StepPending},
	}
	switch ps.Status {
	case domain.PackageDownloading:
		steps[1].Status = domain.StepRunning
	case domain.PackageInstalling:
		steps[1].Status, steps[2].Status = domain.StepDone, domain.StepRunning
	case domain.PackageInstalled:
		steps[1].Status, steps[2].Status = domain.StepDone, domain.StepDone
	case domain.PackageInstallFailed:
		steps[1].Status, steps[2].Status = domain.StepDone, domain.StepFailed
		steps[2].Detail = ps.ErrorMessage
	default: // install_pending: the updater has accepted the offer, nothing runs yet
		steps[0].Status, steps[0].FinishedAt = domain.StepRunning, nil
	}
	return steps
}

// ------------------------------------------------------------ settlement ----

func stepIndex(steps []domain.UpdateStep, name domain.UpdateStepName) int {
	for i := range steps {
		if steps[i].Name == name {
			return i
		}
	}
	return -1
}

// settle lets the target status have the last word over the reported steps and
// sets the phase.
func settle(p *domain.TargetProgress, t *domain.RolloutTarget, att *hostAttempt) {
	hw := stepIndex(p.Steps, domain.StepHealthWait)
	// finishHost resolves the host steps once the collector reports the version.
	finishHost := func(at *time.Time) {
		for i := range p.Steps {
			s := &p.Steps[i]
			if i == hw {
				continue
			}
			if s.Status == domain.StepRunning || s.Status == domain.StepPending {
				s.Status = domain.StepDone
				if s.FinishedAt == nil {
					s.FinishedAt = ptr(at)
				}
			}
		}
	}
	switch t.Status {
	case domain.TargetVerifying:
		finishHost(t.VerifiedAt)
		p.Steps[hw].Status, p.Steps[hw].StartedAt = domain.StepRunning, ptr(t.VerifiedAt)
		p.Phase = domain.PhaseHealthWait
		p.DownloadPercent = nil
	case domain.TargetSucceeded:
		finishHost(t.VerifiedAt)
		p.Steps[hw].Status, p.Steps[hw].StartedAt, p.Steps[hw].FinishedAt = domain.StepDone, ptr(t.VerifiedAt), ptr(t.FinishedAt)
		p.Phase, p.Error, p.DownloadPercent = domain.PhaseSucceeded, "", nil
	case domain.TargetFailed:
		settleFailed(p, t, hw, att)
	case domain.TargetCancelled, domain.TargetSkipped:
		skipRest(p, 0)
		p.Phase, p.DownloadPercent = domain.PhaseCancelled, nil
		if p.Error == "" {
			p.Error = t.LastError
		}
	case domain.TargetRolledBack:
		skipRest(p, 0)
		p.Phase, p.DownloadPercent = domain.PhaseRolledBack, nil
		if p.Error == "" {
			p.Error = t.LastError
		}
	default: // in_progress
		p.Phase = currentPhase(p.Steps)
		if p.Phase != domain.PhaseDownloading {
			p.DownloadPercent = nil
		}
	}
}

// skipRest turns running and pending steps from index from on into skipped.
func skipRest(p *domain.TargetProgress, from int) {
	for i := from; i < len(p.Steps); i++ {
		if s := &p.Steps[i]; s.Status == domain.StepRunning || s.Status == domain.StepPending {
			s.Status = domain.StepSkipped
		}
	}
}

// currentPhase names the step that is running, else the last finished one.
func currentPhase(steps []domain.UpdateStep) domain.UpdatePhase {
	phase := domain.PhaseOffered
	for _, s := range steps {
		switch s.Status {
		case domain.StepRunning:
			return domain.UpdatePhase(s.Name)
		case domain.StepDone:
			phase = domain.UpdatePhase(s.Name)
		}
	}
	return phase
}

// settleFailed marks the step that failed (the reported one, else the one that
// was running or waited for, e.g. a timeout) and resolves the rest.
func settleFailed(p *domain.TargetProgress, t *domain.RolloutTarget, hw int, att *hostAttempt) {
	rolledBack := att != nil && att.status == "rolled_back"
	p.DownloadPercent = nil
	failed := -1
	for i, s := range p.Steps {
		if s.Status == domain.StepFailed {
			failed = i
			break
		}
	}
	if failed < 0 {
		if t.VerifiedAt != nil && hw >= 0 {
			// the collector had reported the version: the health wait failed
			hostBusy := att != nil && !att.final() // the updater still works on its own health check
			for i := range p.Steps {
				if i == hw {
					continue
				}
				switch p.Steps[i].Status {
				case domain.StepRunning:
					if !hostBusy {
						p.Steps[i].Status = domain.StepDone
					}
				case domain.StepPending:
					p.Steps[i].Status = domain.StepDone
				}
			}
			failed = hw
			p.Steps[hw].StartedAt = ptr(t.VerifiedAt)
		} else {
			for i, s := range p.Steps {
				if s.Status == domain.StepRunning {
					failed = i
					break
				}
			}
			if failed < 0 { // nothing runs: the first step that has not finished is the one that did not happen
				for i, s := range p.Steps {
					if s.Status == domain.StepPending {
						failed = i
						break
					}
				}
			}
		}
		if failed >= 0 {
			p.Steps[failed].Status = domain.StepFailed
			p.Steps[failed].FinishedAt = ptr(t.FinishedAt)
			if t.LastError != "" {
				p.Steps[failed].Detail = t.LastError // replaces the "waiting" note of a running step
			}
		}
	}
	if failed >= 0 {
		if att != nil && !att.final() {
			// the host still works (health check, rollback): only what it has not
			// begun is skipped, the running steps stay visible
			for i := failed + 1; i < len(p.Steps); i++ {
				if s := &p.Steps[i]; s.Status == domain.StepPending {
					s.Status = domain.StepSkipped
				}
			}
		} else {
			skipRest(p, failed+1)
		}
	}
	p.Phase = domain.PhaseFailed
	if rolledBack {
		p.Phase = domain.PhaseRolledBack
	}
	// the error: what the failed step reports, else what the host or the engine says
	switch {
	case failed >= 0 && p.Steps[failed].Detail != "":
		p.Error = p.Steps[failed].Detail
	case p.Error == "":
		p.Error = t.LastError
	}
}
