// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package memstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type rolloutRec struct {
	r       domain.Rollout
	targets map[domain.AgentID]*domain.RolloutTarget
	seq     int
}

type rolloutStore struct {
	store.RolloutStore
	s *Store
}

func cloneRollout(r domain.Rollout) domain.Rollout {
	c := r
	c.Waves = make([]domain.RolloutWave, len(r.Waves))
	for i, w := range r.Waves {
		cw := w
		if w.Selector != nil {
			sel := *w.Selector
			cw.Selector = &sel
		}
		c.Waves[i] = cw
	}
	c.Policy.Waves = append([]domain.WaveSpec(nil), r.Policy.Waves...)
	c.Policy.MaintenanceWindows = append([]domain.MaintenanceWindow(nil), r.Policy.MaintenanceWindows...)
	if r.Annotations != nil {
		c.Annotations = map[string]any{}
		for k, v := range r.Annotations {
			c.Annotations[k] = v
		}
	}
	return c
}

func countTargets(ts []*domain.RolloutTarget, wave int) domain.RolloutCounts {
	var c domain.RolloutCounts
	for _, t := range ts {
		if wave >= 0 && t.Wave != wave {
			continue
		}
		c.Total++
		switch t.Status {
		case domain.TargetPending:
			c.Pending++
		case domain.TargetWaitingWindow:
			c.WaitingWindow++
		case domain.TargetInProgress, domain.TargetVerifying:
			c.InProgress++
		case domain.TargetSucceeded:
			c.Succeeded++
		case domain.TargetFailed:
			c.Failed++
		case domain.TargetSkipped:
			c.Skipped++
		case domain.TargetCancelled:
			c.Cancelled++
		case domain.TargetRolledBack:
			c.RolledBack++
		}
	}
	return c
}

// cloneProgress copies a progress value (steps and their time pointers are
// never shared with the caller).
func cloneProgress(p *domain.TargetProgress) *domain.TargetProgress {
	if p == nil {
		return nil
	}
	c := *p
	c.Steps = append([]domain.UpdateStep(nil), p.Steps...)
	if p.DownloadPercent != nil {
		v := *p.DownloadPercent
		c.DownloadPercent = &v
	}
	return &c
}

func cloneTarget(t domain.RolloutTarget) domain.RolloutTarget {
	t.Progress = cloneProgress(t.Progress)
	return t
}

// requestReason reads the reason of the "selection" annotation.
func requestReason(r domain.Rollout) string {
	m, _ := r.Annotations[domain.SelectionAnnotation].(map[string]any)
	reason, _ := m["reason"].(string)
	return reason
}

func (rs *rolloutStore) withCounts(rec *rolloutRec) domain.Rollout {
	out := cloneRollout(rec.r)
	out.Origin, out.RequestReason = domain.OriginOf(out.CreatedBy), requestReason(out)
	ts := make([]*domain.RolloutTarget, 0, len(rec.targets))
	for _, t := range rec.targets {
		ts = append(ts, t)
	}
	out.Counts = countTargets(ts, -1)
	for i := range out.Waves {
		out.Waves[i].Counts = countTargets(ts, out.Waves[i].Index)
	}
	return out
}

func (rs *rolloutStore) Create(ctx context.Context, r *domain.Rollout, targets []domain.RolloutTarget) error {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	if _, ok := rs.s.rollouts[r.ID]; ok {
		return fmt.Errorf("rollout %s: %w", r.ID, domain.ErrConflict)
	}
	rs.s.rolloutSq++
	now := rs.s.now()
	rec := &rolloutRec{r: cloneRollout(*r), targets: map[domain.AgentID]*domain.RolloutTarget{}, seq: rs.s.rolloutSq}
	rec.r.UpdatedAt = now
	if rec.r.CreatedAt.IsZero() {
		rec.r.CreatedAt = now
	}
	for _, t := range targets {
		t := cloneTarget(t)
		t.RolloutID = r.ID
		t.UpdatedAt = now
		rec.targets[t.AgentID] = &t
	}
	rs.s.rollouts[r.ID] = rec
	return nil
}

func (rs *rolloutStore) Get(ctx context.Context, id uuid.UUID) (*domain.Rollout, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return nil, fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	out := rs.withCounts(rec)
	return &out, nil
}

func (rs *rolloutStore) List(ctx context.Context, f store.RolloutFilter) ([]domain.Rollout, int, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	var recs []*rolloutRec
	for _, rec := range rs.s.rollouts {
		r := rec.r
		if len(f.Fleets) > 0 && !containsStr(f.Fleets, r.FleetName) {
			continue
		}
		if len(f.Kinds) > 0 {
			ok := false
			for _, k := range f.Kinds {
				ok = ok || k == r.Kind
			}
			if !ok {
				continue
			}
		}
		if len(f.Status) > 0 {
			ok := false
			for _, st := range f.Status {
				ok = ok || st == r.Status
			}
			if !ok {
				continue
			}
		}
		if f.ActiveOnly && r.Status.IsTerminal() {
			continue
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].seq > recs[j].seq })
	total := len(recs)
	if f.Offset > len(recs) {
		f.Offset = len(recs)
	}
	recs = recs[f.Offset:]
	if f.Limit > 0 && len(recs) > f.Limit {
		recs = recs[:f.Limit]
	}
	out := make([]domain.Rollout, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rs.withCounts(rec))
	}
	return out, total, nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (rs *rolloutStore) UpdateProgress(ctx context.Context, r *domain.Rollout) error {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[r.ID]
	if !ok {
		return fmt.Errorf("rollout %s: %w", r.ID, domain.ErrNotFound)
	}
	rec.r.CurrentWave = r.CurrentWave
	if r.Annotations != nil {
		rec.r.Annotations = map[string]any{}
		for k, v := range r.Annotations {
			rec.r.Annotations[k] = v
		}
	}
	for _, w := range r.Waves {
		if w.Index >= 0 && w.Index < len(rec.r.Waves) {
			cur := &rec.r.Waves[w.Index]
			cur.Status, cur.StartedAt, cur.FinishedAt = w.Status, w.StartedAt, w.FinishedAt
		}
	}
	rec.r.UpdatedAt = rs.s.uniqueNow(rec.r.UpdatedAt)
	return nil
}

func (rs *rolloutStore) CompareAndSetStatus(ctx context.Context, id uuid.UUID, from []domain.RolloutStatus, to domain.RolloutStatus, reason string) (bool, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return false, fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	match := false
	for _, f := range from {
		if f == rec.r.Status {
			match = true
		}
	}
	if !match {
		return false, nil
	}
	now := rs.s.now()
	rec.r.Status, rec.r.StatusReason = to, reason
	if to == domain.RolloutRunning && rec.r.StartedAt == nil {
		rec.r.StartedAt = &now
	}
	if to.IsTerminal() {
		rec.r.FinishedAt = &now
	}
	rec.r.UpdatedAt = rs.s.uniqueNow(rec.r.UpdatedAt)
	return true, nil
}

func (rs *rolloutStore) SetWaveApproval(ctx context.Context, id uuid.UUID, wave int, by string, at time.Time) (bool, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return false, fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	if wave < 0 || wave >= len(rec.r.Waves) {
		return false, nil
	}
	w := &rec.r.Waves[wave]
	if w.Status != domain.WavePending && w.Status != domain.WaveWaitingApproval {
		return false, nil
	}
	w.ApprovedBy, w.ApprovedAt = by, &at
	rec.r.UpdatedAt = rs.s.uniqueNow(rec.r.UpdatedAt)
	return true, nil
}

func (rs *rolloutStore) ListTargets(ctx context.Context, id uuid.UUID, f store.TargetFilter) ([]domain.RolloutTarget, int, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return nil, 0, fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	var out []domain.RolloutTarget
	for _, t := range rec.targets {
		if f.Wave != nil && t.Wave != *f.Wave {
			continue
		}
		if f.Agent != nil && t.AgentID != *f.Agent {
			continue
		}
		if len(f.Status) > 0 {
			ok := false
			for _, st := range f.Status {
				ok = ok || st == t.Status
			}
			if !ok {
				continue
			}
		}
		out = append(out, cloneTarget(*t))
	}
	sort.Slice(out, func(i, j int) bool {
		if f.Order == store.TargetOrderActivity {
			if ri, rj := out[i].Status.ActivityRank(), out[j].Status.ActivityRank(); ri != rj {
				return ri < rj
			}
		}
		if out[i].Wave != out[j].Wave {
			return out[i].Wave < out[j].Wave
		}
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].AgentID.String() < out[j].AgentID.String()
	})
	total := len(out)
	if f.Offset > len(out) {
		f.Offset = len(out)
	}
	out = out[f.Offset:]
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	if out == nil {
		out = []domain.RolloutTarget{}
	}
	return out, total, nil
}

func (rs *rolloutStore) UpdateTargets(ctx context.Context, targets []domain.RolloutTarget) ([]domain.AgentID, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	var conflicts []domain.AgentID
	for _, t := range targets {
		rec, ok := rs.s.rollouts[t.RolloutID]
		if !ok {
			return nil, fmt.Errorf("rollout %s: %w", t.RolloutID, domain.ErrNotFound)
		}
		cur, ok := rec.targets[t.AgentID]
		if !ok || !cur.UpdatedAt.Equal(t.UpdatedAt) {
			conflicts = append(conflicts, t.AgentID)
			continue
		}
		cur.Status, cur.Attempts = t.Status, t.Attempts
		cur.StartedAt, cur.VerifiedAt, cur.FinishedAt = t.StartedAt, t.VerifiedAt, t.FinishedAt
		cur.LastError, cur.ExecutorRef = t.LastError, t.ExecutorRef
		cur.Progress = cloneProgress(t.Progress)
		cur.UpdatedAt = rs.s.uniqueNow(cur.UpdatedAt)
	}
	return conflicts, nil
}

func (rs *rolloutStore) AddTargets(ctx context.Context, id uuid.UUID, targets []domain.RolloutTarget) error {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	now := rs.s.now()
	for _, t := range targets {
		t := cloneTarget(t)
		t.RolloutID = id
		t.UpdatedAt = now
		rec.targets[t.AgentID] = &t
	}
	rec.r.UpdatedAt = rs.s.uniqueNow(rec.r.UpdatedAt)
	return nil
}

func (rs *rolloutStore) CancelOpenTargets(ctx context.Context, id uuid.UUID, reason string) (int64, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return 0, fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	var n int64
	now := rs.s.now()
	for _, t := range rec.targets {
		if t.Status == domain.TargetPending || t.Status == domain.TargetWaitingWindow {
			t.Status, t.LastError, t.FinishedAt = domain.TargetCancelled, reason, &now
			t.UpdatedAt = rs.s.uniqueNow(t.UpdatedAt)
			n++
		}
	}
	return n, nil
}

func (rs *rolloutStore) ResetFailedTargets(ctx context.Context, id uuid.UUID) (int64, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	rec, ok := rs.s.rollouts[id]
	if !ok {
		return 0, fmt.Errorf("rollout %s: %w", id, domain.ErrNotFound)
	}
	var n int64
	for _, t := range rec.targets {
		if t.Status == domain.TargetFailed || t.Status == domain.TargetSkipped {
			t.Status, t.LastError, t.Progress = domain.TargetPending, "", nil
			t.Attempts++
			t.StartedAt, t.VerifiedAt, t.FinishedAt = nil, nil, nil
			t.UpdatedAt = rs.s.uniqueNow(t.UpdatedAt)
			n++
		}
	}
	return n, nil
}

func (rs *rolloutStore) ActiveTargetsForAgent(ctx context.Context, agentID domain.AgentID) ([]domain.RolloutTarget, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	out := []domain.RolloutTarget{}
	for _, rec := range rs.s.rollouts {
		if rec.r.Status.IsTerminal() {
			continue
		}
		if t, ok := rec.targets[agentID]; ok {
			out = append(out, cloneTarget(*t))
		}
	}
	return out, nil
}

func (rs *rolloutStore) LatestTargetForAgent(ctx context.Context, agentID domain.AgentID, kind domain.RolloutKind, toRef string) (*store.TargetRef, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	var best *rolloutRec
	var bt *domain.RolloutTarget
	for _, rec := range rs.s.rollouts {
		if rec.r.Kind != kind {
			continue
		}
		t, ok := rec.targets[agentID]
		if !ok || t.ToRef != toRef {
			continue
		}
		if best == nil || rec.seq > best.seq {
			best, bt = rec, t
		}
	}
	if best == nil {
		return nil, fmt.Errorf("target: %w", domain.ErrNotFound)
	}
	return &store.TargetRef{Target: cloneTarget(*bt), RolloutStatus: best.r.Status}, nil
}

func (rs *rolloutStore) DeleteFinishedBefore(ctx context.Context, before time.Time) (int64, error) {
	rs.s.mu.Lock()
	defer rs.s.mu.Unlock()
	var n int64
	for id, rec := range rs.s.rollouts {
		if rec.r.Status.IsTerminal() && rec.r.FinishedAt != nil && rec.r.FinishedAt.Before(before) {
			delete(rs.s.rollouts, id)
			n++
		}
	}
	return n, nil
}
