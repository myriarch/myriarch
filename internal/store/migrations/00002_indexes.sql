-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- Schema tuning for the query patterns of the PostgreSQL stores
-- (internal/store/postgres).
--
-- Heartbeats: every online agent gets its last_seen_at written in a batch every
-- ~15s (UPDATE ... FROM unnest). With an index on last_seen_at each of these
-- updates would be a non-HOT update (new index entries, bloat). Nothing needs
-- that index at 5,000 agents: lists sort by last_seen_at in memory and the
-- offline sweeper scans the (small) online set. Dropping it and leaving free
-- space in the heap pages makes the batched heartbeat writes HOT updates.

-- +goose Up

DROP INDEX IF EXISTS agents_last_seen_idx;
ALTER TABLE agents SET (fillfactor = 75);

-- config GC (ConfigStore.GC) selects unreferenced rows by touched_at
CREATE INDEX IF NOT EXISTS rendered_configs_touched_idx ON rendered_configs (touched_at);
CREATE INDEX IF NOT EXISTS effective_configs_touched_idx ON effective_configs (touched_at);

-- audit prefix filters ("rollout.*") must work with non-C collations
CREATE INDEX IF NOT EXISTS audit_events_action_prefix_idx ON audit_events (action text_pattern_ops, time DESC);

-- retention of finished rollouts (RolloutStore.DeleteFinishedBefore)
CREATE INDEX IF NOT EXISTS rollouts_finished_idx ON rollouts (finished_at) WHERE finished_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS rollouts_finished_idx;
DROP INDEX IF EXISTS audit_events_action_prefix_idx;
DROP INDEX IF EXISTS effective_configs_touched_idx;
DROP INDEX IF EXISTS rendered_configs_touched_idx;
ALTER TABLE agents RESET (fillfactor);
CREATE INDEX IF NOT EXISTS agents_last_seen_idx ON agents (last_seen_at);
