-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- Target versions chosen in the UI within the frame from Git, and live
-- progress of version-rollout targets.
--
-- fleet_version_selections: at most one row per fleet (the fleet name is the
-- key; the fleets themselves live in the GitOps snapshot, so there is no
-- foreign key: the leader removes the row when the fleet disappears from Git).
-- state: active = the selected version is the effective target of the fleet,
-- invalidated = a Git change excluded it from the frame (the fleet is on
-- hold). git_version / git_commit record spec.collector.version and the
-- applied revision at selection time (conflict rule K1). The history of
-- selections is the audit log (actions fleet.version.*), not this table.
--
-- rollout_targets.progress: derived live progress of a version-rollout target
-- (phase and steps, jsonb), written by the leader's rollout engine and frozen
-- when the target reaches a final status. NULL = no progress known.
--
-- Rolling update: expand only. Replicas that still run the previous version
-- ignore the new table and column (their statements name their columns).

-- +goose Up

CREATE TABLE fleet_version_selections (
    fleet_name   text        PRIMARY KEY,
    version      text        NOT NULL,
    state        text        NOT NULL CHECK (state IN ('active', 'invalidated')),
    state_reason text        NOT NULL DEFAULT '',
    git_version  text        NOT NULL DEFAULT '',
    git_commit   text        NOT NULL DEFAULT '',
    selected_by  text        NOT NULL,
    selected_at  timestamptz NOT NULL,
    reason       text        NOT NULL DEFAULT '',
    rollout_id   uuid,
    planned_at   timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE rollout_targets ADD COLUMN progress jsonb;

-- +goose Down

ALTER TABLE rollout_targets DROP COLUMN IF EXISTS progress;
DROP TABLE IF EXISTS fleet_version_selections;
