// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Anchor, Badge, Group, Loader, Stack, Text, Timeline } from '@mantine/core';
import { IconArrowRight, IconCheck, IconCircleDashed, IconMinus, IconX } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import type { RolloutTarget, UpdateStep } from '@/api/types';
import { agentLink } from '@/lib/links';
import { phaseTone, stepPercent, stepView } from '@/lib/updates';
import { toneColor } from '@/theme/status';
import { StepDuration } from './Elapsed';
import { HealthWaitCountdown } from './HealthWaitCountdown';
import { TargetStatusBadge } from './StatusBadge';
import { DateTime } from './TimeAgo';

function bullet(step: UpdateStep) {
  switch (stepView(step.status).icon) {
    case 'running':
      return <Loader size={12} color="blue" type="oval" />;
    case 'done':
      return <IconCheck size={14} stroke={3} />;
    case 'failed':
      return <IconX size={14} stroke={3} />;
    case 'skipped':
      return <IconMinus size={14} stroke={3} />;
    case 'pending':
      return <IconCircleDashed size={14} />;
  }
}

/** Vertical timeline of the update steps (start time, duration, detail, error). */
export function UpdateTimeline({ target }: { target: RolloutTarget }) {
  const { t } = useTranslation();
  const progress = target.progress;
  if (!progress) return null;
  const lastActive = progress.steps.reduce((acc, s, i) => (s.status !== 'pending' ? i : acc), -1);
  return (
    <Timeline
      active={lastActive}
      bulletSize={22}
      lineWidth={2}
      data-testid="update-timeline"
      aria-label={t('versionUpdate.step.list')}
    >
      {progress.steps.map((step) => {
        const v = stepView(step.status);
        const pct = stepPercent(progress, step);
        return (
          <Timeline.Item
            key={step.name}
            bullet={bullet(step)}
            color={toneColor[v.tone]}
            lineVariant={
              step.status === 'pending' || step.status === 'skipped' ? 'dashed' : 'solid'
            }
            data-step={step.name}
            data-status={step.status}
            title={
              <Group gap={8} wrap="wrap">
                <Text fw={600} fz="sm">
                  {t(`enum.updateStep.${step.name}`)}
                </Text>
                <Badge
                  size="xs"
                  variant="light"
                  color={toneColor[v.tone]}
                  styles={{ label: { textTransform: 'none' } }}
                >
                  {t(`enum.updateStepStatus.${step.status}`)}
                  {pct !== undefined ? ` · ${pct} %` : ''}
                </Badge>
              </Group>
            }
          >
            <Stack gap={0}>
              <Text fz="xs" c="dimmed">
                {step.startedAt ? (
                  <>
                    <DateTime value={step.startedAt} seconds />
                    {' · '}
                    <StepDuration step={step} />
                  </>
                ) : (
                  t('versionUpdate.step.notStarted')
                )}
              </Text>
              {step.detail && (
                <Text
                  fz="xs"
                  c={step.status === 'failed' ? 'red' : undefined}
                  style={{ wordBreak: 'break-word' }}
                >
                  {step.detail}
                </Text>
              )}
              {step.name === 'health_wait' &&
                step.status === 'running' &&
                target.status === 'verifying' && (
                  <HealthWaitCountdown
                    rolloutId={target.rolloutId}
                    verifiedAt={target.verifiedAt ?? step.startedAt}
                    showSource
                  />
                )}
            </Stack>
          </Timeline.Item>
        );
      })}
    </Timeline>
  );
}

/** Header facts + notices + timeline of one target (drawer content and the cards of small rollouts). */
export function TargetProgressView({ target }: { target: RolloutTarget }) {
  const { t } = useTranslation();
  const p = target.progress;
  return (
    <Stack gap="sm" data-testid="target-progress">
      <Group justify="space-between" wrap="wrap" gap="xs">
        <Anchor component={Link} to={agentLink(target.agentId)} fw={600}>
          {target.hostname || target.agentId.slice(0, 8)}
        </Anchor>
        <Group gap={6}>
          <TargetStatusBadge status={target.status} />
          {p && (
            <Badge
              variant="light"
              color={toneColor[phaseTone(p.phase)]}
              styles={{ label: { textTransform: 'none' } }}
            >
              {t(`enum.updatePhase.${p.phase}`)}
            </Badge>
          )}
        </Group>
      </Group>
      <Group gap="lg" wrap="wrap">
        <Text fz="xs" c="dimmed">
          {t('rollouts.wave')} {target.wave + 1}
        </Text>
        <Text fz="xs" c="dimmed">
          {t('rollouts.attempt')} {p?.attempt ?? target.attempts}
        </Text>
        <Group gap={4} wrap="nowrap">
          <Text fz="xs" className="myr-mono">
            {p?.fromVersion ?? target.fromRef ?? '?'}
          </Text>
          <IconArrowRight size={12} aria-hidden />
          <Text fz="xs" className="myr-mono">
            {p?.toVersion ?? target.toRef}
          </Text>
        </Group>
        {p && (
          <Text fz="xs" c="dimmed">
            {t('versionUpdate.progressSource')}: {t(`enum.progressSource.${p.source}`)}
          </Text>
        )}
      </Group>
      {p?.phase === 'rolled_back' && (
        <Alert color="violet" variant="light" py="xs" data-testid="rolled-back-notice">
          {t('versionUpdate.rolledBackNotice', {
            version: p.fromVersion ?? target.fromRef ?? '?',
            error: p.error ?? target.lastError ?? '–',
          })}
        </Alert>
      )}
      {p?.phase === 'failed' && (p.error || target.lastError) && (
        <Alert color="red" variant="light" py="xs">
          {p.error ?? target.lastError}
        </Alert>
      )}
      {p ? (
        <UpdateTimeline target={target} />
      ) : (
        <Text c="dimmed" fz="sm">
          {t('versionUpdate.noProgress')}
        </Text>
      )}
    </Stack>
  );
}
