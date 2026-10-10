// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Box, Group, Loader, Stack, Text, Tooltip } from '@mantine/core';
import { IconCheck, IconMinus, IconX } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import type { TargetProgress, UpdateStep } from '@/api/types';
import { downloadPercentOf, phaseTone, stepLabel, stepPercent, stepView } from '@/lib/updates';
import { toneColor } from '@/theme/status';
import { DateTime } from './TimeAgo';
import { StepDuration } from './Elapsed';

function StepIcon({ step, size }: { step: UpdateStep; size: number }) {
  const v = stepView(step.status);
  switch (v.icon) {
    case 'running':
      return <Loader size={size} color="blue" type="oval" />;
    case 'done':
      return <IconCheck size={size} stroke={3} />;
    case 'failed':
      return <IconX size={size} stroke={3} />;
    case 'skipped':
      return <IconMinus size={size} stroke={3} />;
    case 'pending':
      return null;
  }
}

function StepTooltip({ progress, step }: { progress: TargetProgress; step: UpdateStep }) {
  const { t } = useTranslation();
  const percent = stepPercent(progress, step);
  return (
    <Stack gap={2} maw={280}>
      <Text fz="xs" fw={700}>
        {t(`enum.updateStep.${step.name}`)} · {t(`enum.updateStepStatus.${step.status}`)}
        {percent !== undefined ? ` · ${percent} %` : ''}
      </Text>
      {step.startedAt && (
        <Text fz="xs">
          {t('versionUpdate.step.started')}: <DateTime value={step.startedAt} seconds />
        </Text>
      )}
      {step.finishedAt && (
        <Text fz="xs">
          {t('versionUpdate.step.finished')}: <DateTime value={step.finishedAt} seconds />
        </Text>
      )}
      {step.startedAt && (
        <Text fz="xs">
          {t('versionUpdate.step.duration')}: <StepDuration step={step} />
        </Text>
      )}
      {step.detail && (
        <Text fz="xs" style={{ wordBreak: 'break-word' }}>
          {step.detail}
        </Text>
      )}
    </Stack>
  );
}

/**
 * Compact step bar of one target: one segment per step (pending gray, running animated, done green,
 * failed red, skipped dashed) plus the current phase as text. Every segment has an icon and an
 * accessible name, so color is never the only carrier of the state.
 */
export function UpdateStepsBar({ progress }: { progress: TargetProgress }) {
  const { t } = useTranslation();
  const percent = downloadPercentOf(progress);
  const phaseColor = toneColor[phaseTone(progress.phase)];
  return (
    <Stack gap={4} data-testid="update-steps-bar">
      <Group gap={4} wrap="nowrap" role="list" aria-label={t('versionUpdate.step.list')}>
        {progress.steps.map((step) => {
          const v = stepView(step.status);
          const color = toneColor[v.tone];
          const pct = stepPercent(progress, step);
          return (
            <Tooltip key={step.name} label={<StepTooltip progress={progress} step={step} />}>
              <Box
                role="listitem"
                aria-label={stepLabel(t, step, pct)}
                data-step={step.name}
                data-status={step.status}
                tabIndex={0}
                style={{
                  width: 22,
                  height: 18,
                  borderRadius: 4,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  color: `var(--mantine-color-${color}-${v.tone === 'neutral' ? 6 : 7})`,
                  background:
                    v.tone === 'neutral'
                      ? 'var(--myr-track)'
                      : `var(--mantine-color-${color}-light)`,
                  border: `1px ${v.dashed ? 'dashed' : 'solid'} var(--mantine-color-${color}-${v.tone === 'neutral' ? 4 : 6})`,
                }}
              >
                <StepIcon step={step} size={11} />
              </Box>
            </Tooltip>
          );
        })}
      </Group>
      <Text fz="xs" c={phaseColor} fw={600} data-testid="update-phase">
        {t(`enum.updatePhase.${progress.phase}`)}
        {percent !== undefined ? ` · ${percent} %` : ''}
      </Text>
    </Stack>
  );
}
