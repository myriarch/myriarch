// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { DonutChart } from '@mantine/charts';
import { Anchor, Box, Group, Stack, Text, UnstyledButton } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { CONFIG_SYNC_STATES } from '@/api/enums';
import type { ConfigSyncState, OverviewStats } from '@/api/types';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState } from '@/components/states';
import { useLang } from '@/i18n/useLang';
import { formatNumber } from '@/lib/format';
import { agentsLink } from '@/lib/links';
import { configSyncTone, toneColor } from '@/theme/status';

function isState(v: string): v is ConfigSyncState {
  return (CONFIG_SYNC_STATES as readonly string[]).includes(v);
}

/** Donut of config sync states (in sync / pending / failed / drift / none) with clickable legend. */
export function ConfigSyncCard({ stats }: { stats: OverviewStats }) {
  const { t } = useTranslation();
  const lang = useLang();
  const navigate = useNavigate();
  const rows = stats.configSync
    .filter((r) => isState(r.value))
    .sort(
      (a, b) =>
        CONFIG_SYNC_STATES.indexOf(a.value as ConfigSyncState) -
        CONFIG_SYNC_STATES.indexOf(b.value as ConfigSyncState),
    );
  const total = rows.reduce((s, r) => s + r.count, 0);
  const inSync = rows.find((r) => r.value === 'in_sync')?.count ?? 0;
  const problems =
    (rows.find((r) => r.value === 'failed')?.count ?? 0) +
    (rows.find((r) => r.value === 'drift')?.count ?? 0);

  return (
    <SectionCard
      h="100%"
      title={t('dashboard.configSync.title')}
      description={t('dashboard.configSync.subtitle')}
    >
      {total === 0 ? (
        <EmptyState compact title={t('dashboard.configSync.empty')} />
      ) : (
        <Group gap="lg" wrap="wrap" align="center" justify="center">
          <DonutChart
            size={150}
            thickness={20}
            withTooltip={false}
            data={rows.map((r) => ({
              name: t(`enum.configSync.${r.value as ConfigSyncState}`),
              value: r.count,
              color: `${toneColor[configSyncTone[r.value as ConfigSyncState]]}.${configSyncTone[r.value as ConfigSyncState] === 'neutral' ? 4 : 6}`,
            }))}
            chartLabel={`${formatNumber(lang, total > 0 ? Math.round((inSync / total) * 100) : 0)} %`}
          />
          <Stack gap={4} style={{ minWidth: 180, flex: 1 }}>
            {rows.map((r) => {
              const tone = configSyncTone[r.value as ConfigSyncState];
              return (
                <UnstyledButton
                  key={r.value}
                  onClick={() =>
                    void navigate(agentsLink({ configSync: [r.value], kind: ['collector'] }))
                  }
                  px={6}
                  py={3}
                  style={{ borderRadius: 6 }}
                >
                  <Group justify="space-between" wrap="nowrap" gap="sm">
                    <Group gap={8} wrap="nowrap">
                      <Box
                        w={10}
                        h={10}
                        style={{
                          borderRadius: 3,
                          background: `var(--mantine-color-${toneColor[tone]}-${tone === 'neutral' ? 4 : 6})`,
                        }}
                      />
                      <Text fz="sm">{t(`enum.configSync.${r.value as ConfigSyncState}`)}</Text>
                    </Group>
                    <Text fz="sm" fw={600} className="myr-tabular">
                      {formatNumber(lang, r.count)}
                    </Text>
                  </Group>
                </UnstyledButton>
              );
            })}
          </Stack>
        </Group>
      )}
      {total > 0 && (
        <Group
          gap="lg"
          mt="md"
          pt="sm"
          style={{ borderTop: '1px solid var(--myr-border)' }}
          wrap="wrap"
        >
          <Anchor
            component={Link}
            to={agentsLink({ configSync: ['failed', 'drift'], kind: ['collector'] })}
            fz="sm"
            c={problems > 0 ? 'red' : 'dimmed'}
            data-testid="config-problems"
          >
            {t('dashboard.configSync.problems', { count: problems })}
          </Anchor>
          <Anchor
            component={Link}
            to={agentsLink({ unassigned: true, kind: ['collector'] })}
            fz="sm"
            c="dimmed"
            data-testid="unassigned-link"
          >
            {t('dashboard.configSync.unassigned', { count: stats.unassigned })}
          </Anchor>
        </Group>
      )}
    </SectionCard>
  );
}
