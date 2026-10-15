// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Group, Stack, Text } from '@mantine/core';
import { IconHourglassHigh } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useHealthWait } from '@/api/hooks/rollouts';
import { formatCountdown, healthWaitEnd, remainingMs, type HealthWait } from '@/lib/healthWait';
import { policyLink } from '@/lib/links';
import { useSharedNow } from '@/lib/useNow';

/**
 * "Policy <name> · healthWait <duration> · <path>@<commit>": where the wait comes from. The name links
 * to the policy. Path and commit are those of the policy snapshot of the rollout, which can differ
 * from the policy as it is now: later edits in Git do not change a running rollout.
 */
export function HealthWaitSource({ wait }: { wait: HealthWait }) {
  const { t } = useTranslation();
  const origin = wait.path
    ? `${wait.path}${wait.commit ? `@${wait.commit}` : ''}`
    : t('rollouts.healthWait.builtIn');
  return (
    <Text fz="xs" c="dimmed" data-testid="health-wait-source" style={{ wordBreak: 'break-word' }}>
      {t('rollouts.healthWait.policy')}{' '}
      <Anchor component={Link} to={policyLink(wait.policyName)} fz="xs">
        {wait.policyName}
      </Anchor>
      {' · '}
      <span className="myr-mono">healthWait</span> {wait.waitText}
      {' · '}
      <span className="myr-mono">{origin}</span>
    </Text>
  );
}

/** The time that is left, or "finishing…" once the end has passed and the engine has yet to evaluate the target. */
function Remaining({
  endsAt,
  waitMs,
  count,
}: {
  endsAt: number;
  waitMs: number;
  count: number | undefined;
}) {
  const { t } = useTranslation();
  const now = useSharedNow();
  const left = remainingMs(endsAt, now, waitMs);
  const finishing = left <= 0;
  const time = formatCountdown(left);
  const text =
    count === undefined
      ? finishing
        ? t('rollouts.healthWait.finishing')
        : t('rollouts.healthWait.remaining', { time })
      : finishing
        ? t('rollouts.healthWait.waveFinishing', { count })
        : t('rollouts.healthWait.waveEnds', { count, time });
  return (
    <Text
      role="timer"
      fz="xs"
      fw={finishing ? 400 : 600}
      c={finishing ? 'dimmed' : undefined}
      className="myr-tabular"
      style={count === undefined ? { whiteSpace: 'nowrap' } : undefined}
      data-testid="health-wait"
      data-state={finishing ? 'finishing' : 'running'}
    >
      {text}
    </Text>
  );
}

/**
 * The remaining health wait of a target that is in `verifying`, counted down once per second in the
 * browser: the wait ends at `verifiedAt` plus the `healthWait` of the policy snapshot of the rollout
 * (lib/healthWait.ts has the rule). At 0 it says "finishing…" until the engine has evaluated the target.
 * It keeps running while the rollout is paused, because the engine keeps evaluating running targets.
 * Nothing is shown for a rollout that the engine does not evaluate (failed, rolling back, final), and
 * the caller shows it only for a target that is `verifying`.
 */
export function HealthWaitCountdown({
  rolloutId,
  verifiedAt,
  count,
  showSource = false,
}: {
  rolloutId: string;
  /** Start of the health wait (`RolloutTarget.verifiedAt`); for several targets the latest one. */
  verifiedAt: string | undefined;
  /** Number of targets in the health wait: the countdown then speaks of the wait that ends last. */
  count?: number;
  /** Add the line that names the policy the wait comes from. */
  showSource?: boolean;
}) {
  const wait = useHealthWait(rolloutId);
  if (!wait?.evaluated) return null;
  const endsAt = healthWaitEnd(verifiedAt, wait.waitMs);
  if (endsAt === undefined) return null;
  const remaining = <Remaining endsAt={endsAt} waitMs={wait.waitMs} count={count} />;
  if (!showSource) return remaining;
  return (
    <Stack gap={2}>
      <Group gap={6} wrap="nowrap" align="flex-start">
        <IconHourglassHigh size={14} aria-hidden style={{ flexShrink: 0, marginTop: 1 }} />
        {remaining}
      </Group>
      <HealthWaitSource wait={wait} />
    </Stack>
  );
}
