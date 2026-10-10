// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Button, Group, Paper, Stack, Text, ThemeIcon, Tooltip } from '@mantine/core';
import {
  IconCheck,
  IconCircleDashed,
  IconClockPause,
  IconPlayerPlay,
  IconSquareRoundedX,
  IconPlayerSkipForward,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import type { RolloutWave } from '@/api/types';
import { Can } from '@/components/Can';
import { SelectorView } from '@/components/misc';
import { RolloutProgress } from '@/components/RolloutProgress';
import { WaveStatusBadge } from '@/components/StatusBadge';
import { DateTime } from '@/components/TimeAgo';
import { toneColor, waveTone } from '@/theme/status';
import { waveApproval } from './waveDisplay';

const icons = {
  pending: <IconCircleDashed size={18} />,
  waiting_approval: <IconClockPause size={18} />,
  running: <IconPlayerPlay size={18} />,
  completed: <IconCheck size={18} />,
  failed: <IconSquareRoundedX size={18} />,
  skipped: <IconPlayerSkipForward size={18} />,
} as const;

/** Waves as steps with their own progress; waves waiting for approval get an approve button. */
export function WaveSteps({
  waves,
  currentWave,
  canApprove,
  onApprove,
  selected,
  onSelect,
}: {
  waves: RolloutWave[];
  currentWave: number;
  canApprove: boolean;
  onApprove: (wave: number) => void;
  selected?: number;
  onSelect?: (wave: number | undefined) => void;
}) {
  const { t } = useTranslation();
  return (
    <Stack gap="sm" role="list" aria-label={t('policy.waves')}>
      {waves.map((w) => {
        const color = toneColor[waveTone[w.status]];
        const isSel = selected === w.index;
        const approval = waveApproval(w);
        return (
          <Paper
            key={w.index}
            withBorder
            p="md"
            radius="md"
            role="listitem"
            data-testid={`wave-${w.index}`}
            style={{
              borderColor:
                w.index === currentWave && w.status !== 'completed'
                  ? `var(--mantine-color-${color}-5)`
                  : undefined,
              boxShadow: isSel ? `0 0 0 2px var(--mantine-color-brand-4)` : undefined,
            }}
          >
            <Group wrap="nowrap" align="flex-start" gap="md">
              <ThemeIcon variant="light" color={color} size={36} radius="xl" aria-hidden>
                {icons[w.status]}
              </ThemeIcon>
              <Stack gap={8} style={{ flex: 1, minWidth: 0 }}>
                <Group justify="space-between" wrap="wrap" gap="xs">
                  <Group gap="sm" wrap="wrap">
                    <Text fw={700}>
                      {t('rollouts.waveNumber', { n: w.index + 1 })}: {w.name}
                    </Text>
                    <WaveStatusBadge status={w.status} />
                    {approval === 'required' && (
                      <Badge size="xs" color="gray" variant="outline">
                        {t('policy.requiresApproval')}
                      </Badge>
                    )}
                    {approval === 'approved' && (
                      <Badge size="xs" color="teal" variant="light">
                        {t('rollouts.approvedBadge')}
                      </Badge>
                    )}
                  </Group>
                  <Group gap="xs">
                    {onSelect && (
                      <Button
                        size="compact-xs"
                        variant={isSel ? 'filled' : 'subtle'}
                        onClick={() => onSelect(isSel ? undefined : w.index)}
                      >
                        {t('rollouts.showTargets')}
                      </Button>
                    )}
                    {w.status === 'waiting_approval' && !w.approvedAt && (
                      <Can permission="rollouts:operate">
                        <Button
                          size="xs"
                          color="yellow"
                          onClick={() => onApprove(w.index)}
                          data-testid={`approve-${w.index}`}
                          disabled={!canApprove}
                        >
                          {t('rollouts.approve')}
                        </Button>
                      </Can>
                    )}
                  </Group>
                </Group>
                {w.percent !== undefined && (
                  <Tooltip
                    multiline
                    w={300}
                    label={t('rollouts.waveShareHint', {
                      percent: w.percent,
                      count: w.counts.total,
                    })}
                  >
                    <Text fz="xs" c="dimmed" w="fit-content" tabIndex={0}>
                      {t('rollouts.waveShare', { percent: w.percent })}
                    </Text>
                  </Tooltip>
                )}
                {w.selector &&
                  (Object.keys(w.selector.matchAttributes ?? {}).length > 0 ||
                    (w.selector.matchExpressions ?? []).length > 0) && (
                    <SelectorView selector={w.selector} />
                  )}
                <RolloutProgress counts={w.counts} />
                <Group gap="lg" wrap="wrap">
                  {w.startedAt && (
                    <Text fz="xs" c="dimmed">
                      {t('rollouts.startedAt')}: <DateTime value={w.startedAt} />
                    </Text>
                  )}
                  {w.finishedAt && (
                    <Text fz="xs" c="dimmed">
                      {t('rollouts.finishedAt')}: <DateTime value={w.finishedAt} />
                    </Text>
                  )}
                  {w.approvedAt && (
                    <Text fz="xs" c="dimmed">
                      {t('rollouts.approvedBy', { by: w.approvedBy ?? '?' })}{' '}
                      <DateTime value={w.approvedAt} />
                    </Text>
                  )}
                </Group>
              </Stack>
            </Group>
          </Paper>
        );
      })}
    </Stack>
  );
}
