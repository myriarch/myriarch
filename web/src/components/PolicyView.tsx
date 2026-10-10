// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Box, Group, Paper, SimpleGrid, Stack, Text, Tooltip } from '@mantine/core';
import { IconArrowRight, IconShieldCheck, IconTargetArrow } from '@tabler/icons-react';
import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';
import type { RolloutPolicy } from '@/api/types';
import { WEEKDAYS, windowSegments } from '@/lib/windows';
import { KeyValue, SelectorView } from './misc';

/** Waves as a left-to-right flow of steps. */
export function WaveFlow({ policy }: { policy: RolloutPolicy }) {
  const { t } = useTranslation();
  return (
    <Group gap="xs" wrap="wrap" align="stretch" role="list" aria-label={t('policy.waves')}>
      {policy.waves.map((w, i) => (
        <Fragment key={`${w.name}-${i}`}>
          {i > 0 && (
            <IconArrowRight size={16} style={{ alignSelf: 'center', opacity: 0.5 }} aria-hidden />
          )}
          <Paper withBorder p="sm" radius="md" role="listitem" miw={150}>
            <Stack gap={4}>
              <Group gap={6} wrap="nowrap">
                <Badge circle size="sm" variant="filled">
                  {i + 1}
                </Badge>
                <Text fw={600} fz="sm">
                  {w.name}
                </Text>
              </Group>
              {w.percent !== undefined && (
                <Text fz="xs" c="dimmed">
                  {t('policy.percentOfTargets', { percent: w.percent })}
                </Text>
              )}
              {w.selector &&
                (Object.keys(w.selector.matchAttributes ?? {}).length > 0 ||
                  (w.selector.matchExpressions ?? []).length > 0) && (
                  <Box>
                    <Group gap={4} mb={2}>
                      <IconTargetArrow size={12} />
                      <Text fz="xs" c="dimmed">
                        {t('policy.selector')}
                      </Text>
                    </Group>
                    <SelectorView selector={w.selector} />
                  </Box>
                )}
              {w.requireApproval && (
                <Badge
                  size="xs"
                  color="yellow"
                  variant="light"
                  leftSection={<IconShieldCheck size={11} />}
                >
                  {t('policy.requiresApproval')}
                </Badge>
              )}
            </Stack>
          </Paper>
        </Fragment>
      ))}
    </Group>
  );
}

/** Weekly grid (Mon..Sun x 24 h) with the allowed maintenance windows highlighted. */
export function WindowGrid({ windows }: { windows: RolloutPolicy['maintenanceWindows'] }) {
  const { t } = useTranslation();
  const segs = windowSegments(windows ?? []);
  const hours = [0, 6, 12, 18, 24];
  return (
    <Stack gap={4} role="img" aria-label={t('policy.windowsAria')}>
      <Box
        style={{ display: 'grid', gridTemplateColumns: '34px 1fr', gap: 6, alignItems: 'center' }}
      >
        <span />
        <Box style={{ position: 'relative', height: 14 }}>
          {hours.map((h) => (
            <Text
              key={h}
              fz={10}
              c="dimmed"
              style={{
                position: 'absolute',
                left: `${(h / 24) * 100}%`,
                transform: 'translateX(-50%)',
              }}
            >
              {String(h).padStart(2, '0')}
            </Text>
          ))}
        </Box>
        {WEEKDAYS.map((d) => (
          <Fragment key={d}>
            <Text fz="xs" c="dimmed">
              {t(`enum.weekday.${d}`)}
            </Text>
            <Box
              style={{
                position: 'relative',
                height: 14,
                borderRadius: 4,
                background: 'var(--myr-track)',
                overflow: 'hidden',
              }}
              className="myr-window-row"
            >
              {segs[d].map(([s, e], i) => (
                <Tooltip key={i} label={`${fmt(s)}–${fmt(e)}`}>
                  <Box
                    style={{
                      position: 'absolute',
                      left: `${(s / 1440) * 100}%`,
                      width: `${((e - s) / 1440) * 100}%`,
                      top: 0,
                      bottom: 0,
                      background: 'var(--mantine-color-teal-6)',
                    }}
                  />
                </Tooltip>
              ))}
            </Box>
          </Fragment>
        ))}
      </Box>
    </Stack>
  );
}

function fmt(min: number): string {
  const m = Math.min(1440, min);
  const hh = Math.floor(m / 60) % 24;
  const mm = m % 60;
  return m === 1440 ? '24:00' : `${String(hh).padStart(2, '0')}:${String(mm).padStart(2, '0')}`;
}

/** Complete policy rendering (waves, windows, thresholds, timeouts). */
export function PolicyView({ policy, compact }: { policy: RolloutPolicy; compact?: boolean }) {
  const { t } = useTranslation();
  const th = policy.failureThreshold;
  const hasWindows = (policy.maintenanceWindows ?? []).length > 0;
  return (
    <Stack gap="lg">
      <Stack gap={6}>
        <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
          {t('policy.waves')}
        </Text>
        <WaveFlow policy={policy} />
      </Stack>
      <SimpleGrid cols={{ base: 1, md: compact ? 1 : 2 }} spacing="xl">
        <KeyValue
          labelWidth={180}
          items={[
            { label: t('policy.maxConcurrent'), value: policy.maxConcurrent ?? '–' },
            { label: t('policy.healthWait'), value: policy.healthWait ?? '–' },
            { label: t('policy.applyTimeout'), value: policy.applyTimeout ?? '–' },
            { label: t('policy.offlineGrace'), value: policy.offlineGracePeriod ?? '–' },
            {
              label: t('policy.failureThreshold'),
              value:
                th.maxFailed === undefined && th.maxFailedPercent === undefined
                  ? '–'
                  : [
                      th.maxFailed !== undefined
                        ? t('policy.maxFailed', { count: th.maxFailed })
                        : null,
                      th.maxFailedPercent !== undefined
                        ? t('policy.maxFailedPercent', { percent: th.maxFailedPercent })
                        : null,
                    ]
                      .filter(Boolean)
                      .join(` ${t('policy.or')} `),
            },
            {
              label: t('policy.onFailure'),
              value: policy.onFailure ? t(`enum.onFailure.${policy.onFailure}`) : '–',
            },
            {
              label: t('policy.timezone'),
              value: policy.timezoneAttribute
                ? `${policy.timezoneAttribute} (${policy.defaultTimezone ?? '–'})`
                : (policy.defaultTimezone ?? '–'),
            },
          ]}
        />
        <Stack gap={6}>
          <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
            {t('policy.windows')}
          </Text>
          {hasWindows ? (
            <>
              <WindowGrid windows={policy.maintenanceWindows} />
              <Text fz="xs" c="dimmed">
                {t('policy.windowsHint')}
              </Text>
              <Group gap={6} wrap="wrap">
                {(policy.maintenanceWindows ?? []).map((w, i) => (
                  <Badge
                    key={i}
                    variant="outline"
                    color="gray"
                    size="sm"
                    styles={{ label: { textTransform: 'none' } }}
                  >
                    {w.days && w.days.length > 0
                      ? w.days.map((d) => t(`enum.weekday.${d}`)).join(', ')
                      : t('policy.everyDay')}{' '}
                    {w.start}–{w.end}
                    {w.timezone && w.timezone !== 'agent' ? ` ${w.timezone}` : ''}
                  </Badge>
                ))}
              </Group>
            </>
          ) : (
            <Text fz="sm" c="dimmed">
              {t('policy.noWindows')}
            </Text>
          )}
        </Stack>
      </SimpleGrid>
    </Stack>
  );
}
