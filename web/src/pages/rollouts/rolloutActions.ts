// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { Rollout, RolloutStatus } from '@/api/types';
import type { RolloutAction } from '@/api/hooks/rollouts';

/**
 * Mirrors domain.RolloutStatus.IsTerminal. 'failed' is NOT terminal: the fleet stays blocked until
 * an operator aborts, rolls back or retries it.
 */
const TERMINAL: RolloutStatus[] = ['completed', 'aborted', 'rolled_back', 'superseded'];

export function isTerminal(status: RolloutStatus): boolean {
  return TERMINAL.includes(status);
}

/**
 * Which operator actions make sense in which state. This mirrors the engine's transition table
 * (internal/rollout/actions.go); the server remains the authority and answers 412 otherwise.
 */
export function availableActions(r: Pick<Rollout, 'status' | 'counts' | 'waves'>): {
  actions: RolloutAction[];
  approvableWaves: number[];
} {
  const actions: RolloutAction[] = [];
  const s = r.status;
  if (s === 'running') actions.push('pause');
  if (s === 'paused' || s === 'pending') actions.push('resume');
  if (
    s === 'pending' ||
    s === 'running' ||
    s === 'waiting_approval' ||
    s === 'paused' ||
    s === 'failed'
  )
    actions.push('abort');
  if (
    r.counts.failed + r.counts.skipped > 0 &&
    (s === 'paused' || s === 'running' || s === 'failed')
  )
    actions.push('retry-failed');
  if (s === 'running' || s === 'paused' || s === 'failed') actions.push('rollback');
  // A wave that already has an approval is only waiting for the engine's next tick.
  const approvableWaves =
    s === 'waiting_approval' || s === 'running' || s === 'paused'
      ? r.waves.filter((w) => w.status === 'waiting_approval' && !w.approvedAt).map((w) => w.index)
      : [];
  return { actions, approvableWaves };
}
