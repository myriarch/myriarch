// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useHealthWait, useRolloutTargets } from '@/api/hooks/rollouts';
import { HealthWaitCountdown } from '@/components/HealthWaitCountdown';
import { latestVerifiedAt } from '@/lib/healthWait';

/**
 * The health wait of a wave: how many of its targets are in `verifying` and when the wait of the one
 * that was verified last ends. The wave card has no target data, so this reads the targets of the wave
 * itself; it is mounted only for a wave with targets in flight.
 */
export function WaveHealthWait({ rolloutId, wave }: { rolloutId: string; wave: number }) {
  const wait = useHealthWait(rolloutId);
  const q = useRolloutTargets(
    rolloutId,
    { wave, status: ['verifying'], limit: 500 },
    { refetchInterval: 5_000, enabled: wait?.evaluated === true },
  );
  const page = q.data;
  // 500 is the most that the API returns on a page. With more targets in the health wait the page
  // would not contain the one that ends last, and the card shows nothing rather than a wrong end.
  if (!page || page.items.length === 0 || page.total > page.items.length) return null;
  return (
    <HealthWaitCountdown
      rolloutId={rolloutId}
      verifiedAt={latestVerifiedAt(page.items)}
      count={page.items.length}
      showSource
    />
  );
}
