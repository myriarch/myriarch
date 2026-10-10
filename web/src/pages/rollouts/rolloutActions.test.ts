// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { Rollout, RolloutStatus } from '@/api/types';
import { availableActions, isTerminal } from './rolloutActions';

const counts = {
  total: 4,
  pending: 0,
  waitingWindow: 0,
  inProgress: 0,
  succeeded: 2,
  failed: 1,
  skipped: 0,
  cancelled: 0,
  rolledBack: 0,
};
const wave = (index: number, over: Record<string, unknown> = {}) =>
  ({
    index,
    name: `w${index}`,
    requireApproval: true,
    status: 'waiting_approval',
    counts,
    ...over,
  }) as never;
const r = (status: RolloutStatus, waves: Rollout['waves'] = []) => ({ status, counts, waves });

describe('availableActions mirrors the engine transition table', () => {
  it('failed is not terminal and can be aborted, rolled back and retried', () => {
    expect(isTerminal('failed')).toBe(false);
    expect(availableActions(r('failed')).actions).toEqual(['abort', 'retry-failed', 'rollback']);
  });

  it.each([
    ['pending', ['resume', 'abort']],
    ['running', ['pause', 'abort', 'retry-failed', 'rollback']],
    ['waiting_approval', ['abort']],
    ['paused', ['resume', 'abort', 'retry-failed', 'rollback']],
    ['completed', []],
    ['aborted', []],
    ['rolled_back', []],
    ['superseded', []],
  ] as const)('%s', (status, expected) => {
    expect(availableActions(r(status)).actions).toEqual(expected);
  });

  it('does not offer to approve a wave that is already approved', () => {
    const waves = [wave(0, { approvedAt: '2026-01-01T00:00:00Z' }), wave(1)];
    expect(availableActions(r('waiting_approval', waves)).approvableWaves).toEqual([1]);
  });
});
