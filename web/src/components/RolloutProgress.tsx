// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Group, Stack, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import type { RolloutCounts } from '@/api/types';
import { useLang } from '@/i18n/useLang';
import { formatNumber } from '@/lib/format';
import { SegmentBar } from './misc';

/**
 * Stacked progress of rollout targets: finished states first, then work in progress, then targets
 * that wait for a maintenance window (the only yellow). Pending targets have no fill: the empty
 * track stands for work that has not started.
 */
export function RolloutProgress({
  counts,
  legend = false,
  height = 8,
}: {
  counts: RolloutCounts;
  legend?: boolean;
  height?: number;
}) {
  const { t } = useTranslation();
  const lang = useLang();
  const done = counts.succeeded;
  const pct = counts.total > 0 ? Math.round((done / counts.total) * 100) : 0;
  return (
    <Stack gap={4}>
      <SegmentBar
        height={height}
        legend={legend}
        segments={[
          {
            key: 'succeeded',
            value: counts.succeeded,
            tone: 'success',
            label: t('enum.targetStatus.succeeded'),
          },
          {
            key: 'failed',
            value: counts.failed,
            tone: 'danger',
            label: t('enum.targetStatus.failed'),
          },
          {
            key: 'rolledBack',
            value: counts.rolledBack,
            tone: 'drift',
            label: t('enum.targetStatus.rolled_back'),
          },
          {
            key: 'skipped',
            value: counts.skipped + counts.cancelled,
            tone: 'neutral',
            label: t('rollouts.skippedCancelled'),
          },
          {
            key: 'inProgress',
            value: counts.inProgress,
            tone: 'info',
            label: t('enum.targetStatus.in_progress'),
          },
          {
            key: 'waitingWindow',
            value: counts.waitingWindow,
            tone: 'warning',
            label: t('enum.targetStatus.waiting_window'),
          },
          {
            key: 'pending',
            value: counts.pending,
            tone: 'neutral',
            fill: 'track',
            label: t('enum.targetStatus.pending'),
          },
        ]}
      />
      {!legend && (
        <Group justify="space-between" gap="xs">
          <Group gap="xs">
            <Text fz="xs" className="myr-tabular">
              {t('rollouts.progressText', {
                done: formatNumber(lang, done),
                total: formatNumber(lang, counts.total),
                percent: pct,
              })}
            </Text>
            {counts.inProgress > 0 && (
              <Text fz="xs" c="blue" className="myr-tabular">
                {t('rollouts.inProgressCount', { count: counts.inProgress })}
              </Text>
            )}
          </Group>
          {counts.failed > 0 && (
            <Text fz="xs" c="red" className="myr-tabular">
              {t('rollouts.failedCount', { count: counts.failed })}
            </Text>
          )}
        </Group>
      )}
    </Stack>
  );
}
