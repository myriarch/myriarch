// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { RolloutWave } from '@/api/types';

/**
 * Which approval badge a wave card shows:
 * - `approved`: the wave was approved (also while it is still pending, running or completed),
 * - `required`: the wave will stop for an approval that has not been given yet (status pending),
 * - `null`: nothing. While the wave waits, its status badge and the approve button say it already.
 */
export function waveApproval(w: RolloutWave): 'required' | 'approved' | null {
  if (!w.requireApproval) return null;
  if (w.approvedAt) return 'approved';
  return w.status === 'pending' ? 'required' : null;
}
