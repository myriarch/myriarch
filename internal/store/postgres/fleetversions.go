// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type fleetVersionStore struct{ db *DB }

var _ store.FleetVersionStore = (*fleetVersionStore)(nil)

const fleetVersionColumns = `fleet_name, version, state, state_reason, git_version, git_commit, selected_by, selected_at, reason,
  rollout_id, planned_at, updated_at`

func scanFleetVersion(r rowScanner) (domain.FleetVersionSelection, error) {
	var (
		sel     domain.FleetVersionSelection
		state   string
		rollout *uuid.UUID
		planned *time.Time
	)
	if err := r.Scan(&sel.Fleet, &sel.Version, &state, &sel.StateReason, &sel.GitVersion, &sel.GitCommit, &sel.SelectedBy,
		&sel.SelectedAt, &sel.Reason, &rollout, &planned, &sel.UpdatedAt); err != nil {
		return sel, err
	}
	sel.State = domain.VersionSelectionState(state)
	sel.SelectedAt, sel.UpdatedAt = sel.SelectedAt.UTC(), sel.UpdatedAt.UTC()
	sel.RolloutID, sel.PlannedAt = rollout, utcPtr(planned)
	return sel, nil
}

// Get implements store.FleetVersionStore.
func (s *fleetVersionStore) Get(ctx context.Context, fleet string) (*domain.FleetVersionSelection, error) {
	sel, err := scanFleetVersion(s.db.q.QueryRow(ctx, `SELECT `+fleetVersionColumns+` FROM fleet_version_selections WHERE fleet_name = $1`, fleet))
	if err != nil {
		return nil, dbErr("fleetVersions.get", err)
	}
	return &sel, nil
}

// List implements store.FleetVersionStore.
func (s *fleetVersionStore) List(ctx context.Context) ([]domain.FleetVersionSelection, error) {
	rows, err := s.db.q.Query(ctx, `SELECT `+fleetVersionColumns+` FROM fleet_version_selections ORDER BY fleet_name`)
	if err != nil {
		return nil, dbErr("fleetVersions.list", err)
	}
	defer rows.Close()
	out := []domain.FleetVersionSelection{}
	for rows.Next() {
		sel, err := scanFleetVersion(rows)
		if err != nil {
			return nil, dbErr("fleetVersions.list scan", err)
		}
		out = append(out, sel)
	}
	return out, dbErr("fleetVersions.list", rows.Err())
}

// Put implements store.FleetVersionStore (compare-and-set on updated_at).
func (s *fleetVersionStore) Put(ctx context.Context, sel domain.FleetVersionSelection, expectUpdatedAt *time.Time) (*domain.FleetVersionSelection, error) {
	state := string(sel.State)
	if state == "" {
		state = string(domain.SelectionActive)
	}
	selectedAt := sel.SelectedAt
	if selectedAt.IsZero() {
		selectedAt = time.Now()
	}
	args := []any{sel.Fleet, sel.Version, state, cleanText(sel.StateReason), sel.GitVersion, sel.GitCommit, sel.SelectedBy,
		selectedAt.UTC(), cleanText(sel.Reason)}
	var row rowScanner
	if expectUpdatedAt == nil {
		// no row may exist: a concurrent insert or an existing row is a conflict
		row = s.db.q.QueryRow(ctx, `INSERT INTO fleet_version_selections (fleet_name, version, state, state_reason, git_version, git_commit,
    selected_by, selected_at, reason, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, clock_timestamp())
ON CONFLICT (fleet_name) DO NOTHING
RETURNING `+fleetVersionColumns, args...)
	} else {
		row = s.db.q.QueryRow(ctx, `UPDATE fleet_version_selections SET version = $2, state = $3, state_reason = $4, git_version = $5,
    git_commit = $6, selected_by = $7, selected_at = $8, reason = $9, rollout_id = NULL, planned_at = NULL, updated_at = clock_timestamp()
WHERE fleet_name = $1 AND updated_at = $10
RETURNING `+fleetVersionColumns, append(args, expectUpdatedAt.UTC())...)
	}
	got, err := scanFleetVersion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, dbErr("fleetVersions.put", domain.ErrConflict)
		}
		return nil, dbErr("fleetVersions.put", err)
	}
	return &got, nil
}

// Delete implements store.FleetVersionStore.
func (s *fleetVersionStore) Delete(ctx context.Context, fleet string, expectUpdatedAt *time.Time) (bool, error) {
	if expectUpdatedAt == nil {
		tag, err := s.db.q.Exec(ctx, `DELETE FROM fleet_version_selections WHERE fleet_name = $1`, fleet)
		if err != nil {
			return false, dbErr("fleetVersions.delete", err)
		}
		return tag.RowsAffected() > 0, nil
	}
	tag, err := s.db.q.Exec(ctx, `DELETE FROM fleet_version_selections WHERE fleet_name = $1 AND updated_at = $2`, fleet, expectUpdatedAt.UTC())
	if err != nil {
		return false, dbErr("fleetVersions.delete", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var exists bool
	if err := s.db.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fleet_version_selections WHERE fleet_name = $1)`, fleet).Scan(&exists); err != nil {
		return false, dbErr("fleetVersions.delete", err)
	}
	if exists {
		return false, dbErr("fleetVersions.delete", domain.ErrConflict)
	}
	return false, nil
}

// Invalidate implements store.FleetVersionStore (compare-and-set on updated_at).
func (s *fleetVersionStore) Invalidate(ctx context.Context, fleet, reason string, expectUpdatedAt time.Time) (bool, error) {
	tag, err := s.db.q.Exec(ctx, `UPDATE fleet_version_selections SET state = 'invalidated', state_reason = $2, updated_at = clock_timestamp()
WHERE fleet_name = $1 AND updated_at = $3`, fleet, cleanText(reason), expectUpdatedAt.UTC())
	if err != nil {
		return false, dbErr("fleetVersions.invalidate", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkPlanned implements store.FleetVersionStore. updated_at stays untouched:
// it is the version of the selection itself, which callers hold for CAS.
func (s *fleetVersionStore) MarkPlanned(ctx context.Context, fleet string, selectedAt time.Time, rolloutID *uuid.UUID, at time.Time) error {
	_, err := s.db.q.Exec(ctx, `UPDATE fleet_version_selections SET planned_at = $3, rollout_id = $4
WHERE fleet_name = $1 AND selected_at = $2`, fleet, selectedAt.UTC(), at.UTC(), uuidPtrArg(rolloutID))
	return dbErr("fleetVersions.markPlanned", err)
}
