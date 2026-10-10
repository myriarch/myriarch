// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Box, Group, Stack, Text, Tooltip, UnstyledButton } from '@mantine/core';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import type { OverviewStats } from '@/api/types';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState } from '@/components/states';
import { useLang } from '@/i18n/useLang';
import { compareVersions, formatNumber, formatPercent } from '@/lib/format';
import { agentsLink } from '@/lib/links';

/** Horizontal bars per collector version; outdated versions are orange, current ones teal. */
export function VersionDistribution({ stats }: { stats: OverviewStats }) {
  const { t } = useTranslation();
  const lang = useLang();
  const navigate = useNavigate();
  const rows = useMemo(
    () => [...stats.versions].sort((a, b) => compareVersions(b.version, a.version)),
    [stats.versions],
  );
  const total = rows.reduce((s, r) => s + r.count, 0);
  const max = rows.reduce((m, r) => Math.max(m, r.count), 0);
  const latest = stats.outdated.latestVersion;

  return (
    <SectionCard
      h="100%"
      title={t('dashboard.versions.title')}
      description={latest ? t('dashboard.versions.subtitle', { latest }) : undefined}
      actions={
        <Group gap={12}>
          <Legend color="teal" label={t('dashboard.versions.current')} />
          <Legend color="orange" label={t('dashboard.versions.outdated')} />
        </Group>
      }
    >
      {rows.length === 0 ? (
        <EmptyState compact title={t('dashboard.versions.empty')} />
      ) : (
        <Stack gap={6} aria-label={t('dashboard.versions.title')}>
          {rows.map((r) => {
            const pct = total > 0 ? (r.count / total) * 100 : 0;
            return (
              <Tooltip
                key={r.version}
                label={t('dashboard.versions.rowHint', { version: r.version })}
                openDelay={500}
              >
                <UnstyledButton
                  onClick={() =>
                    void navigate(agentsLink({ version: [r.version], kind: ['collector'] }))
                  }
                  style={{ borderRadius: 6 }}
                >
                  <Group gap="sm" wrap="nowrap">
                    <Text className="myr-mono myr-tabular" fz="sm" w={78}>
                      {r.version}
                    </Text>
                    <Box
                      style={{
                        flex: 1,
                        height: 18,
                        borderRadius: 4,
                        background: 'var(--myr-track)',
                      }}
                      className="myr-bar-track"
                    >
                      <Box
                        style={{
                          width: `${max > 0 ? Math.max(1.5, (r.count / max) * 100) : 0}%`,
                          height: '100%',
                          borderRadius: 4,
                          background: r.outdated
                            ? 'var(--mantine-color-orange-5)'
                            : 'var(--mantine-color-teal-6)',
                          transition: 'width 400ms ease',
                        }}
                      />
                    </Box>
                    <Text fz="sm" fw={600} w={52} ta="right" className="myr-tabular">
                      {formatNumber(lang, r.count)}
                    </Text>
                    <Text fz="xs" c="dimmed" w={48} ta="right" className="myr-tabular">
                      {formatPercent(lang, pct, 0)}
                    </Text>
                  </Group>
                </UnstyledButton>
              </Tooltip>
            );
          })}
        </Stack>
      )}
    </SectionCard>
  );
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <Group gap={5} wrap="nowrap">
      <Box
        w={9}
        h={9}
        style={{
          borderRadius: 2,
          background: `var(--mantine-color-${color}-${color === 'teal' ? 6 : 5})`,
        }}
      />
      <Text fz="xs" c="dimmed">
        {label}
      </Text>
    </Group>
  );
}
