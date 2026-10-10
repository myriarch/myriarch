// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { UpdateStep } from '@/api/types';
import { formatDurationMs, stepDurationMs } from '@/lib/updates';
import { useNow } from '@/lib/useNow';

/** Duration of a step; a running step counts up once per second (the ticker only runs then). */
export function StepDuration({ step }: { step: UpdateStep }) {
  const now = useNow(step.status === 'running');
  const ms = stepDurationMs(step, now);
  if (ms === undefined) return null;
  return <span className="myr-tabular">{formatDurationMs(ms)}</span>;
}
