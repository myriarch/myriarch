// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

func selection(fleet, version string) domain.FleetVersionSelection {
	return domain.FleetVersionSelection{
		Fleet: fleet, Version: version, State: domain.SelectionActive, GitVersion: "0.161.0", GitCommit: "abc123",
		SelectedBy: "user:" + uuid.NewString(), SelectedAt: time.Now().UTC().Truncate(time.Microsecond), Reason: "weekly maintenance",
	}
}

func TestFleetVersionSelectionsCompareAndSet(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	fv := db.FleetVersions()

	if _, err := fv.Get(ctx, "gw"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get of a missing selection: %v", err)
	}
	if list, err := fv.List(ctx); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty List must be [] not nil: %v %v", list, err)
	}

	stored, err := fv.Put(ctx, selection("gw", "0.162.0"), nil)
	mustNoErr(t, err)
	if stored.State != domain.SelectionActive || stored.Version != "0.162.0" || stored.UpdatedAt.IsZero() || stored.PlannedAt != nil || stored.RolloutID != nil {
		t.Fatalf("stored: %+v", stored)
	}
	// inserting again without expectation conflicts
	if _, err := fv.Put(ctx, selection("gw", "0.163.0"), nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second insert must conflict: %v", err)
	}
	// replacing with a stale updated_at conflicts, with the current one succeeds
	stale := stored.UpdatedAt.Add(-time.Second)
	if _, err := fv.Put(ctx, selection("gw", "0.163.0"), &stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale replace must conflict: %v", err)
	}
	next := selection("gw", "0.163.0")
	replaced, err := fv.Put(ctx, next, &stored.UpdatedAt)
	mustNoErr(t, err)
	if replaced.Version != "0.163.0" || !replaced.UpdatedAt.After(stored.UpdatedAt) {
		t.Fatalf("replaced: %+v", replaced)
	}
	// replacing a row that vanished conflicts as well
	if _, err := fv.Put(ctx, selection("other", "0.162.0"), &stored.UpdatedAt); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replace of a missing row must conflict: %v", err)
	}

	got, err := fv.Get(ctx, "gw")
	mustNoErr(t, err)
	if got.SelectedBy != next.SelectedBy || got.Reason != "weekly maintenance" || got.GitVersion != "0.161.0" || got.GitCommit != "abc123" ||
		!got.SelectedAt.Equal(next.SelectedAt) {
		t.Fatalf("round trip: %+v want %+v", got, next)
	}
	_, err = fv.Put(ctx, selection("alpha", "0.162.0"), nil)
	mustNoErr(t, err)
	list, err := fv.List(ctx)
	mustNoErr(t, err)
	if len(list) != 2 || list[0].Fleet != "alpha" || list[1].Fleet != "gw" {
		t.Fatalf("list must be ordered by fleet: %+v", list)
	}

	// delete with CAS
	if ok, err := fv.Delete(ctx, "gw", &stored.UpdatedAt); !errors.Is(err, domain.ErrConflict) || ok {
		t.Fatalf("stale delete must conflict: %v %v", ok, err)
	}
	if ok, err := fv.Delete(ctx, "gw", &replaced.UpdatedAt); err != nil || !ok {
		t.Fatalf("delete with the current version: %v %v", ok, err)
	}
	if ok, err := fv.Delete(ctx, "gw", &replaced.UpdatedAt); err != nil || ok {
		t.Fatalf("delete of a missing row is false without error: %v %v", ok, err)
	}
	if ok, err := fv.Delete(ctx, "alpha", nil); err != nil || !ok {
		t.Fatalf("unconditional delete: %v %v", ok, err)
	}
}

func TestFleetVersionSelectionsInvalidateAndMarkPlanned(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	fv := db.FleetVersions()
	stored, err := fv.Put(ctx, selection("gw", "0.162.0"), nil)
	mustNoErr(t, err)

	rid := uuid.New()
	planned := time.Now().UTC().Truncate(time.Microsecond)
	mustNoErr(t, fv.MarkPlanned(ctx, "gw", stored.SelectedAt, &rid, planned))
	got, _ := fv.Get(ctx, "gw")
	if got.PlannedAt == nil || !got.PlannedAt.Equal(planned) || got.RolloutID == nil || *got.RolloutID != rid {
		t.Fatalf("MarkPlanned: %+v", got)
	}
	if !got.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatal("MarkPlanned must not change updated_at (it is the compare-and-set version of the selection)")
	}

	// a replaced selection (other selected_at) is not marked by the old planner run
	replaced, err := fv.Put(ctx, selection("gw", "0.163.0"), &got.UpdatedAt)
	mustNoErr(t, err)
	if replaced.PlannedAt != nil || replaced.RolloutID != nil {
		t.Fatalf("a new selection starts unplanned: %+v", replaced)
	}
	mustNoErr(t, fv.MarkPlanned(ctx, "gw", stored.SelectedAt, &rid, planned))
	after, _ := fv.Get(ctx, "gw")
	if after.PlannedAt != nil {
		t.Fatalf("MarkPlanned for a replaced selection must be a no-op: %+v", after)
	}
	// nothing to mark for a missing row: no error
	mustNoErr(t, fv.MarkPlanned(ctx, "missing", stored.SelectedAt, nil, planned))
	// planned without a rollout
	mustNoErr(t, fv.MarkPlanned(ctx, "gw", replaced.SelectedAt, nil, planned))
	after, _ = fv.Get(ctx, "gw")
	if after.PlannedAt == nil || after.RolloutID != nil {
		t.Fatalf("planned without rollout: %+v", after)
	}

	// compare-and-set: a stale version does not invalidate (the selection was replaced meanwhile)
	if ok, err := fv.Invalidate(ctx, "gw", "stale", replaced.UpdatedAt.Add(-time.Second)); err != nil || ok {
		t.Fatalf("stale Invalidate must not match: %v %v", ok, err)
	}
	if ok, err := fv.Invalidate(ctx, "gw", "version 0.163.0 is outside the frame", replaced.UpdatedAt); err != nil || !ok {
		t.Fatalf("Invalidate: %v %v", ok, err)
	}
	inv, _ := fv.Get(ctx, "gw")
	if inv.State != domain.SelectionInvalidated || inv.StateReason != "version 0.163.0 is outside the frame" || !inv.UpdatedAt.After(replaced.UpdatedAt) {
		t.Fatalf("Invalidate: %+v", inv)
	}
	if ok, err := fv.Invalidate(ctx, "missing", "x", replaced.UpdatedAt); err != nil || ok {
		t.Fatalf("Invalidate of a missing row is false without error: %v %v", ok, err)
	}
}

func TestSetFleetTargetVersion(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	as := db.Agents()
	mk := func(host string, kind domain.AgentKind, fleet string) domain.AgentID {
		id := newID()
		_, _, err := as.UpsertDescription(ctx, id, agentSpec{id: id, host: host, version: "0.161.0", kind: kind}.update())
		mustNoErr(t, err)
		mustNoErr(t, as.SetAssignment(ctx, id, store.AgentAssignmentUpdate{FleetName: fleet, TargetVersion: "0.161.0"}))
		return id
	}
	a, b := mk("a", domain.AgentKindCollector, "gw"), mk("b", domain.AgentKindCollector, "gw")
	other := mk("c", domain.AgentKindCollector, "edge")
	updater := mk("u", domain.AgentKindUpdater, "gw")

	n, err := as.SetFleetTargetVersion(ctx, "gw", "0.162.0")
	mustNoErr(t, err)
	if n != 2 {
		t.Fatalf("changed rows = %d, want the two collectors of the fleet", n)
	}
	for id, want := range map[domain.AgentID]string{a: "0.162.0", b: "0.162.0", other: "0.161.0", updater: "0.161.0"} {
		ag, err := as.Get(ctx, id)
		mustNoErr(t, err)
		if ag.Assignment.TargetVersion != want {
			t.Fatalf("agent %s target version %q, want %q", ag.Hostname, ag.Assignment.TargetVersion, want)
		}
	}
	if n, err := as.SetFleetTargetVersion(ctx, "gw", "0.162.0"); err != nil || n != 0 {
		t.Fatalf("idempotent: %d %v", n, err)
	}
	if n, err := as.SetFleetTargetVersion(ctx, "gw", ""); err != nil || n != 2 {
		t.Fatalf("clearing the target (hold): %d %v", n, err)
	}
}

func TestRolloutTargetProgressRoundTrip(t *testing.T) {
	db, ctx := newDB(t), ctxT(t)
	rs := db.Rollouts()
	r := newRollout(domain.RolloutKindVersion, domain.RolloutRunning, 1)
	r.CreatedBy = "user:5f1c2d3e-0000-4000-8000-000000000001"
	r.Annotations = map[string]any{domain.SelectionAnnotation: map[string]any{"version": "0.162.0", "reason": "maintenance window"}}
	tg := targetsFor(2, 1, "0.162.0")
	now := time.Now().UTC().Truncate(time.Microsecond)
	pct := 42.5
	tg[0].Progress = &domain.TargetProgress{
		Phase: domain.PhaseDownloading, Source: domain.ProgressUpdater, Attempt: 1, FromVersion: "0.161.0", ToVersion: "0.162.0",
		DownloadPercent: &pct, UpdatedAt: now,
		Steps: []domain.UpdateStep{{Name: domain.StepOffered, Status: domain.StepDone, StartedAt: &now, FinishedAt: &now},
			{Name: domain.StepDownloading, Status: domain.StepRunning, StartedAt: &now, Detail: "42%"}},
	}
	mustNoErr(t, rs.Create(ctx, r, tg))

	got, err := rs.Get(ctx, r.ID)
	mustNoErr(t, err)
	if got.Origin != domain.OriginUI || got.RequestReason != "maintenance window" {
		t.Fatalf("origin/reason of Get: %q %q", got.Origin, got.RequestReason)
	}
	list, _, err := rs.List(ctx, store.RolloutFilter{})
	mustNoErr(t, err)
	if len(list) != 1 || list[0].Origin != domain.OriginUI || list[0].RequestReason != "maintenance window" {
		t.Fatalf("origin/reason of List: %+v", list)
	}

	ts, _, err := rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	mustNoErr(t, err)
	var withProgress, without *domain.RolloutTarget
	for i := range ts {
		if ts[i].Progress != nil {
			withProgress = &ts[i]
		} else {
			without = &ts[i]
		}
	}
	if withProgress == nil || without == nil {
		t.Fatalf("exactly one target has progress: %+v", ts)
	}
	p := withProgress.Progress
	if p.Phase != domain.PhaseDownloading || p.Source != domain.ProgressUpdater || p.DownloadPercent == nil || *p.DownloadPercent != 42.5 ||
		len(p.Steps) != 2 || p.Steps[1].Detail != "42%" || p.Steps[0].FinishedAt == nil || !p.Steps[0].FinishedAt.Equal(now) || p.FromVersion != "0.161.0" {
		t.Fatalf("progress round trip: %+v", p)
	}

	// UpdateTargets writes progress (and clears it again)
	without.Progress = &domain.TargetProgress{Phase: domain.PhaseOffered, Source: domain.ProgressExecutor, Steps: []domain.UpdateStep{}, UpdatedAt: now}
	withProgress.Progress = nil
	conflicts, err := rs.UpdateTargets(ctx, []domain.RolloutTarget{*without, *withProgress})
	mustNoErr(t, err)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts: %v", conflicts)
	}
	ts, _, _ = rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	n := 0
	for _, x := range ts {
		if x.Progress != nil {
			n++
			if x.AgentID != without.AgentID || x.Progress.Phase != domain.PhaseOffered || x.Progress.Steps == nil {
				t.Fatalf("updated progress: %+v", x.Progress)
			}
		}
	}
	if n != 1 {
		t.Fatalf("progress rows after update: %d", n)
	}

	// retry-failed starts the next attempt without the old progress
	failed := ts[0]
	failed.Status = domain.TargetFailed
	failed.Progress = &domain.TargetProgress{Phase: domain.PhaseFailed, Source: domain.ProgressUpdater, Steps: []domain.UpdateStep{}, UpdatedAt: now}
	_, err = rs.UpdateTargets(ctx, []domain.RolloutTarget{failed})
	mustNoErr(t, err)
	reset, err := rs.ResetFailedTargets(ctx, r.ID)
	mustNoErr(t, err)
	if reset != 1 {
		t.Fatalf("reset = %d", reset)
	}
	ts, _, _ = rs.ListTargets(ctx, r.ID, store.TargetFilter{})
	for _, x := range ts {
		if x.AgentID == failed.AgentID && x.Progress != nil {
			t.Fatalf("ResetFailedTargets must clear the progress: %+v", x.Progress)
		}
	}

	// a rollout created by Git
	g := newRollout(domain.RolloutKindConfig, domain.RolloutRunning, 1)
	mustNoErr(t, rs.Create(ctx, g, nil))
	gg, _ := rs.Get(ctx, g.ID)
	if gg.Origin != domain.OriginGitOps || gg.RequestReason != "" {
		t.Fatalf("gitops rollout: %q %q", gg.Origin, gg.RequestReason)
	}
}
