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

type fleetVersionStore struct{ s *Store }

var _ store.FleetVersionStore = (*fleetVersionStore)(nil)

func cloneSelection(sel domain.FleetVersionSelection) domain.FleetVersionSelection {
	if sel.RolloutID != nil {
		id := *sel.RolloutID
		sel.RolloutID = &id
	}
	if sel.PlannedAt != nil {
		t := *sel.PlannedAt
		sel.PlannedAt = &t
	}
	return sel
}

func (fs *fleetVersionStore) Get(ctx context.Context, fleet string) (*domain.FleetVersionSelection, error) {
	fs.s.mu.Lock()
	defer fs.s.mu.Unlock()
	sel, ok := fs.s.fleetVers[fleet]
	if !ok {
		return nil, fmt.Errorf("version selection of fleet %s: %w", fleet, domain.ErrNotFound)
	}
	c := cloneSelection(sel)
	return &c, nil
}

func (fs *fleetVersionStore) List(ctx context.Context) ([]domain.FleetVersionSelection, error) {
	fs.s.mu.Lock()
	defer fs.s.mu.Unlock()
	out := make([]domain.FleetVersionSelection, 0, len(fs.s.fleetVers))
	for _, sel := range fs.s.fleetVers {
		out = append(out, cloneSelection(sel))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fleet < out[j].Fleet })
	return out, nil
}

func (fs *fleetVersionStore) Put(ctx context.Context, sel domain.FleetVersionSelection, expectUpdatedAt *time.Time) (*domain.FleetVersionSelection, error) {
	fs.s.mu.Lock()
	defer fs.s.mu.Unlock()
	cur, exists := fs.s.fleetVers[sel.Fleet]
	switch {
	case expectUpdatedAt == nil && exists:
		return nil, fmt.Errorf("version selection of fleet %s: %w", sel.Fleet, domain.ErrConflict)
	case expectUpdatedAt != nil && (!exists || !cur.UpdatedAt.Equal(*expectUpdatedAt)):
		return nil, fmt.Errorf("version selection of fleet %s: %w", sel.Fleet, domain.ErrConflict)
	}
	if sel.State == "" {
		sel.State = domain.SelectionActive
	}
	sel.RolloutID, sel.PlannedAt = nil, nil
	sel.UpdatedAt = fs.s.uniqueNow(cur.UpdatedAt)
	fs.s.fleetVers[sel.Fleet] = cloneSelection(sel)
	c := cloneSelection(sel)
	return &c, nil
}

func (fs *fleetVersionStore) Delete(ctx context.Context, fleet string, expectUpdatedAt *time.Time) (bool, error) {
	fs.s.mu.Lock()
	defer fs.s.mu.Unlock()
	cur, exists := fs.s.fleetVers[fleet]
	if !exists {
		return false, nil
	}
	if expectUpdatedAt != nil && !cur.UpdatedAt.Equal(*expectUpdatedAt) {
		return false, fmt.Errorf("version selection of fleet %s: %w", fleet, domain.ErrConflict)
	}
	delete(fs.s.fleetVers, fleet)
	return true, nil
}

func (fs *fleetVersionStore) Invalidate(ctx context.Context, fleet, reason string, expectUpdatedAt time.Time) (bool, error) {
	fs.s.mu.Lock()
	defer fs.s.mu.Unlock()
	cur, ok := fs.s.fleetVers[fleet]
	if !ok || !cur.UpdatedAt.Equal(expectUpdatedAt) {
		return false, nil
	}
	cur.State, cur.StateReason = domain.SelectionInvalidated, reason
	cur.UpdatedAt = fs.s.uniqueNow(cur.UpdatedAt)
	fs.s.fleetVers[fleet] = cur
	return true, nil
}

func (fs *fleetVersionStore) MarkPlanned(ctx context.Context, fleet string, selectedAt time.Time, rolloutID *uuid.UUID, at time.Time) error {
	fs.s.mu.Lock()
	defer fs.s.mu.Unlock()
	cur, ok := fs.s.fleetVers[fleet]
	if !ok || !cur.SelectedAt.Equal(selectedAt) {
		return nil
	}
	t := at
	cur.PlannedAt = &t
	cur.RolloutID = nil
	if rolloutID != nil {
		id := *rolloutID
		cur.RolloutID = &id
	}
	fs.s.fleetVers[fleet] = cur
	return nil
}
