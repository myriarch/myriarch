// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { RolloutWave } from '@/api/types';
import { waveApproval } from './waveDisplay';

const wave = (over: Partial<RolloutWave>): RolloutWave => ({
  index: 1,
  name: 'rest',
  requireApproval: true,
  status: 'pending',
  counts: {
    total: 3,
    pending: 3,
    waitingWindow: 0,
    inProgress: 0,
    succeeded: 0,
    failed: 0,
    skipped: 0,
    cancelled: 0,
    rolledBack: 0,
  },
  ...over,
});
const APPROVED = '2026-10-10T21:54:33Z';

describe('waveApproval', () => {
  it('is null for a wave without the approval requirement', () => {
    for (const status of ['pending', 'waiting_approval', 'running', 'completed'] as const) {
      expect(waveApproval(wave({ requireApproval: false, status }))).toBeNull();
    }
    expect(
      waveApproval(wave({ requireApproval: false, status: 'running', approvedAt: APPROVED })),
    ).toBeNull();
  });

  it('announces the approval for a pending wave that is not approved yet', () => {
    expect(waveApproval(wave({ status: 'pending' }))).toBe('required');
  });

  it('adds no badge while the wave waits: the status badge and the button say it', () => {
    expect(waveApproval(wave({ status: 'waiting_approval' }))).toBeNull();
  });

  it('is approved whenever approvedAt is set, whatever the status', () => {
    for (const status of ['pending', 'waiting_approval', 'running', 'completed'] as const) {
      expect(waveApproval(wave({ status, approvedAt: APPROVED }))).toBe('approved');
    }
  });

  it('is null for waves that never needed the approval to run', () => {
    expect(waveApproval(wave({ status: 'completed' }))).toBeNull();
    expect(waveApproval(wave({ status: 'skipped' }))).toBeNull();
    expect(waveApproval(wave({ status: 'running' }))).toBeNull();
    expect(waveApproval(wave({ status: 'failed' }))).toBeNull();
  });
});
