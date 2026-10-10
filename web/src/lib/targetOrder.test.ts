// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { TARGET_STATUSES } from '@/api/enums';
import type { TargetStatus } from '@/api/types';
import { compareTargetsByActivity, TARGET_ACTIVITY_RANK } from './targetOrder';

let seq = 0;
const t = (status: TargetStatus, wave = 0, hostname = 'WIN-EDGE-01') => ({
  status,
  wave,
  hostname,
  agentId: `agent-${String(seq++).padStart(3, '0')}`,
});

describe('target activity order', () => {
  it('ranks every status', () => {
    for (const s of TARGET_STATUSES) expect(TARGET_ACTIVITY_RANK[s]).toBeLessThan(7);
  });

  it('sorts the statuses in the documented order', () => {
    const shuffled = [...TARGET_STATUSES].reverse().map((s) => t(s));
    shuffled.sort(compareTargetsByActivity);
    const groups = shuffled.map((x) => x.status);
    expect(groups.slice(0, 2).sort()).toEqual(['in_progress', 'verifying']);
    expect(groups[2]).toBe('waiting_window');
    expect(groups.slice(3, 5).sort()).toEqual(['failed', 'rolled_back']);
    expect(groups.slice(5)).toEqual(['pending', 'succeeded', 'skipped', 'cancelled']);
  });

  it('breaks ties by wave, then by hostname and agent ID, bytewise like the server', () => {
    const list = [
      t('pending', 1, 'WIN-EDGE-02'),
      t('pending', 0, 'WIN-EDGE-106'),
      t('pending', 0, 'WIN-EDGE-14'),
      t('in_progress', 1, 'WIN-EDGE-99'),
    ];
    list.sort(compareTargetsByActivity);
    expect(list.map((x) => `${x.status}/${x.wave}/${x.hostname}`)).toEqual([
      'in_progress/1/WIN-EDGE-99',
      'pending/0/WIN-EDGE-106',
      'pending/0/WIN-EDGE-14',
      'pending/1/WIN-EDGE-02',
    ]);
  });

  it('falls back to the agent ID for equal hostnames', () => {
    const a = { ...t('pending'), agentId: 'b' };
    const b = { ...t('pending'), agentId: 'a' };
    expect([a, b].sort(compareTargetsByActivity).map((x) => x.agentId)).toEqual(['a', 'b']);
  });
});
