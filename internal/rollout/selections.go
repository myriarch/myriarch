// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/actorname"
	"github.com/myriarch/myriarch/internal/audit"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
	"github.com/myriarch/myriarch/internal/store"
)

// Audit actions of the UI version selection that the engine writes (actor
// gitops). The REST layer writes fleet.version.select and .clear.
const (
	AuditSelectionSuperseded  = "fleet.version.superseded"
	AuditSelectionInvalidated = "fleet.version.invalidated"
	AuditSelectionRemoved     = "fleet.version.removed"
)

// ReconcileSelections applies the conflict rules between the stored UI
// version selections and the given GitOps snapshot (docs/concepts/fleets.md,
// "Conflict rules": K4, K1, K2a, K2b; leader only). It runs after every applied
// snapshot before the agents are reassigned and at the start of every
// planning run, is idempotent and never stops at the first failing fleet.
func (e *Engine) ReconcileSelections(ctx context.Context, snap *domain.GitOpsSnapshot) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reconcileSelections(ctx, snap)
}

func (e *Engine) reconcileSelections(ctx context.Context, snap *domain.GitOpsSnapshot) error {
	if snap == nil || snap.Commit == "" {
		return nil // nothing was ever synced: a missing fleet means nothing yet
	}
	sels, err := e.deps.Store.FleetVersions().List(ctx)
	if err != nil {
		return fmt.Errorf("list version selections: %w", err)
	}
	fleets := make(map[string]*domain.Fleet, len(snap.Fleets))
	for i := range snap.Fleets {
		fleets[snap.Fleets[i].Name] = &snap.Fleets[i]
	}
	var errs []error
	for _, sel := range sels {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.reconcileOne(ctx, snap, fleets[sel.Fleet], sel); err != nil {
			e.log.Error("reconcile version selection failed", "fleet", sel.Fleet, "error", err)
			errs = append(errs, fmt.Errorf("fleet %s: %w", sel.Fleet, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) reconcileOne(ctx context.Context, snap *domain.GitOpsSnapshot, f *domain.Fleet, sel domain.FleetVersionSelection) error {
	decision, reason := domain.ReconcileSelection(f, sel, snap.Commit)
	if decision == domain.SelectionKeep {
		return nil
	}
	details := map[string]any{"version": sel.Version, "commit": snap.Commit, "reason": reason, "selectedBy": sel.SelectedBy}
	action := ""
	changed := false
	switch decision {
	case domain.SelectionRemove, domain.SelectionSupersede:
		ok, err := e.deps.Store.FleetVersions().Delete(ctx, sel.Fleet, &sel.UpdatedAt)
		if errors.Is(err, domain.ErrConflict) {
			return nil // replaced meanwhile: the next run evaluates the new selection
		}
		if err != nil {
			return fmt.Errorf("delete selection: %w", err)
		}
		changed = ok
		if decision == domain.SelectionRemove {
			action = AuditSelectionRemoved
		} else {
			action = AuditSelectionSuperseded
			details["gitVersionBefore"] = sel.GitVersion
			details["gitVersionNow"] = domain.NormalizeVersion(f.Collector.Version)
		}
	case domain.SelectionInvalidate:
		ok, err := e.deps.Store.FleetVersions().Invalidate(ctx, sel.Fleet, reason, sel.UpdatedAt)
		if err != nil {
			return fmt.Errorf("invalidate selection: %w", err)
		}
		changed = ok
		action = AuditSelectionInvalidated
		details["frame"] = f.Update.Selection.FrameText()
	}
	if !changed {
		return nil
	}
	e.log.Info("version selection reconciled with Git", "fleet", sel.Fleet, "rule", decision, "version", sel.Version, "reason", reason)
	ev := audit.System(domain.ActorGitOps, action, "fleet", sel.Fleet, details)
	ev.Time = e.now()
	e.audit(ctx, ev)
	if f != nil {
		// the effective target changed: the members' target version follows at once
		var cur *domain.FleetVersionSelection
		if decision == domain.SelectionInvalidate {
			c := sel
			c.State = domain.SelectionInvalidated
			cur = &c
		}
		e.syncTargetVersion(ctx, f, domain.ResolveVersionTarget(f, cur))
	}
	e.publishFleetVersionChanged(ctx, sel.Fleet)
	return nil
}

// syncTargetVersion writes the effective target version to the member
// collectors of the fleet when it differs from what this engine wrote last
// (the assignment service writes the same value on its next pass).
func (e *Engine) syncTargetVersion(ctx context.Context, f *domain.Fleet, vt domain.VersionTarget) {
	e.fleetsMu.Lock()
	last, known := e.lastTarget[f.Name]
	e.fleetsMu.Unlock()
	if known && last == vt.Version {
		return
	}
	if _, err := e.deps.Store.Agents().SetFleetTargetVersion(ctx, f.Name, vt.Version); err != nil {
		e.log.Warn("set the target version of the fleet members failed", "fleet", f.Name, "error", err)
		return
	}
	e.fleetsMu.Lock()
	if e.lastTarget == nil {
		e.lastTarget = map[string]string{}
	}
	e.lastTarget[f.Name] = vt.Version
	e.fleetsMu.Unlock()
}

func (e *Engine) publishFleetVersionChanged(ctx context.Context, fleet string) {
	if e.deps.Bus == nil {
		return
	}
	if err := e.deps.Bus.Publish(ctx, eventbus.FleetVersionChanged, eventbus.FleetVersionChangedPayload{FleetName: fleet}); err != nil {
		e.log.Warn("publish fleet.version_changed failed", "fleet", fleet, "error", err)
	}
}

// markPlanned tells the REST layer (and the UI waiting for it) that the
// planner has handled the selection: it records the planning time and the
// version rollout that brings the fleet to the selected version (nil if no
// collector needs an update).
func (e *Engine) markPlanned(ctx context.Context, f *domain.Fleet, sel *domain.FleetVersionSelection, vt domain.VersionTarget) {
	if sel == nil || vt.Source != domain.TargetSourceUI {
		return
	}
	active, _, err := e.deps.Store.Rollouts().List(ctx, store.RolloutFilter{
		Fleets: []string{f.Name}, Kinds: []domain.RolloutKind{domain.RolloutKindVersion}, ActiveOnly: true,
	})
	if err != nil {
		e.log.Warn("mark selection planned: list rollouts failed", "fleet", f.Name, "error", err)
		return
	}
	var rid *uuid.UUID
	for i := range active {
		if active[i].ToRef == vt.Version {
			id := active[i].ID
			rid = &id
			break
		}
	}
	// plannedAt/rolloutId are set once per selection and follow a newer
	// active rollout, but never get cleared again: when the rollout has
	// finished (completed, failed, aborted, superseded) the selection keeps
	// the link to the rollout that carried it out.
	if sel.PlannedAt != nil && (rid == nil || sameUUID(sel.RolloutID, rid)) {
		return
	}
	if err := e.deps.Store.FleetVersions().MarkPlanned(ctx, f.Name, sel.SelectedAt, rid, e.now()); err != nil {
		e.log.Warn("mark selection planned failed", "fleet", f.Name, "error", err)
		return
	}
	e.publishFleetVersionChanged(ctx, f.Name)
}

// markTargetPlanned records the effective target version of the fleet that this run has handled (store.PlannedTargetKey):
// the version rollout for it exists, or none is needed. The installation of hosts from the server reads the record on
// every replica and gives a new host the target only when the record equals it. Without it, a host that asks between the
// moment the target reaches the replicas (the event of a Git sync or of a selection in the UI) and the moment this run
// has created the rollout would take the new version at once and skip the waves and approvals of the rollout.
//
// It is called after the version rollout of the fleet was planned without an error, like markPlanned, and for every
// source of the target, so a selection that is cleared (the target falls back to Git) is a new target too. The record is
// written when it changes. It is written after the rollout was created, so that a reader that sees the record also
// sees the rollout.
func (e *Engine) markTargetPlanned(ctx context.Context, f *domain.Fleet, vt domain.VersionTarget) {
	e.fleetsMu.RLock()
	last, known := e.lastPlanned[f.Name]
	e.fleetsMu.RUnlock()
	if known && last == vt.Version {
		return
	}
	if err := e.deps.Store.KV().Put(ctx, store.PlannedTargetKey(f.Name), []byte(vt.Version)); err != nil {
		if ctx.Err() == nil {
			e.log.Warn("record the planned target version failed (new hosts wait for it)", "fleet", f.Name, "error", err)
		}
		return
	}
	e.fleetsMu.Lock()
	if e.lastPlanned == nil {
		e.lastPlanned = map[string]string{}
	}
	e.lastPlanned[f.Name] = vt.Version
	e.fleetsMu.Unlock()
}

// forgetPlanned makes the next planning run write the record of every fleet again (see lastPlanned).
func (e *Engine) forgetPlanned() {
	e.fleetsMu.Lock()
	e.lastPlanned = nil
	e.fleetsMu.Unlock()
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// selectionAnnotation is the value of the rollout annotation "selection".
func selectionAnnotation(sel *domain.FleetVersionSelection) map[string]any {
	return map[string]any{
		"version": sel.Version, "selectedBy": sel.SelectedBy, "selectedAt": sel.SelectedAt.UTC().Format(time.RFC3339Nano),
		"reason": sel.Reason, "gitCommit": sel.GitCommit,
	}
}

// selectionNote describes the selection behind a rollout for notifications.
func (e *Engine) selectionNote(ctx context.Context, r *domain.Rollout) string {
	m, ok := r.Annotations[domain.SelectionAnnotation].(map[string]any)
	if !ok {
		return ""
	}
	by, _ := m["selectedBy"].(string)
	reason, _ := m["reason"].(string)
	note := "selected in the UI by " + actorname.New(ctx, e.deps.Store.Identity()).Name(by)
	if reason != "" {
		note += ": " + reason
	}
	return note
}

// createAudit builds the audit event of a created rollout: the actor is the
// operator for rollouts that a UI selection caused, gitops otherwise.
func (e *Engine) createAudit(ctx context.Context, r *domain.Rollout, details map[string]any) domain.AuditEvent {
	if r.Origin != domain.OriginUI {
		details["origin"] = string(domain.OriginGitOps)
		return audit.System(domain.ActorGitOps, "rollout.create", "rollout", r.ID.String(), details)
	}
	typ, id := actorname.Actor(r.CreatedBy)
	details["origin"] = string(domain.OriginUI)
	details["selectedBy"] = r.CreatedBy
	if m, ok := r.Annotations[domain.SelectionAnnotation].(map[string]any); ok {
		details["reason"], _ = m["reason"].(string)
	}
	ev := audit.System(typ, "rollout.create", "rollout", r.ID.String(), details)
	ev.ActorID = id
	ev.ActorName = actorname.New(ctx, e.deps.Store.Identity()).Name(r.CreatedBy)
	return ev
}
