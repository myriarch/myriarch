// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { TargetStatus } from '@/api/types';

/**
 * Rank of a target status in the "active first" list of a rollout; lower comes first. Mirrors
 * `domain.TargetStatus.ActivityRank` in internal/domain/rollout.go: keep both in sync.
 */
export const TARGET_ACTIVITY_RANK: Record<TargetStatus, number> = {
  in_progress: 0,
  verifying: 0,
  waiting_window: 1,
  failed: 2,
  rolled_back: 2,
  pending: 3,
  succeeded: 4,
  skipped: 5,
  cancelled: 6,
};

type Sortable = {
  status: TargetStatus;
  wave: number;
  hostname?: string | null;
  agentId: string;
};

const bytewise = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

/**
 * Sort order of the mock backend for `sort=wave`, like the server: wave, hostname (bytewise, so
 * WIN-EDGE-106 comes before WIN-EDGE-14), agent ID.
 */
export function compareTargetsByWave(a: Sortable, b: Sortable): number {
  return (
    a.wave - b.wave ||
    bytewise(a.hostname ?? '', b.hostname ?? '') ||
    bytewise(a.agentId, b.agentId)
  );
}

/** Sort order of the mock backend for `sort=activity`: rank, then the wave order. */
export function compareTargetsByActivity(a: Sortable, b: Sortable): number {
  return (
    (TARGET_ACTIVITY_RANK[a.status] ?? 7) - (TARGET_ACTIVITY_RANK[b.status] ?? 7) ||
    compareTargetsByWave(a, b)
  );
}
