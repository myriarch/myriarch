// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Badge, Group, Progress, Table, Text, Tooltip } from '@mantine/core';
import { IconAlertTriangle } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useFleets } from '@/api/hooks/fleets';
import type { OverviewStats } from '@/api/types';
import { VersionSourceBadge } from '@/components/VersionSourceBadge';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState } from '@/components/states';
import { useLang } from '@/i18n/useLang';
import { compareVersions, formatNumber } from '@/lib/format';
import { agentsLink, fleetLink } from '@/lib/links';

function Ratio({ value, total, color }: { value: number; total: number; color: string }) {
  const lang = useLang();
  const pct = total > 0 ? (value / total) * 100 : 0;
  return (
    <Tooltip label={`${formatNumber(lang, value)} / ${formatNumber(lang, total)}`}>
      <Group gap={6} wrap="nowrap" miw={84}>
        <Progress
          value={pct}
          color={color}
          size="sm"
          radius="xl"
          style={{ flex: 1 }}
          aria-label={`${Math.round(pct)}%`}
        />
        <Text fz="xs" className="myr-tabular" w={32} ta="right">
          {Math.round(pct)}%
        </Text>
      </Group>
    </Tooltip>
  );
}

/** Fleets with agent counts, config sync and target version progress. */
export function FleetsTable({ stats }: { stats: OverviewStats }) {
  const { t } = useTranslation();
  const lang = useLang();
  const latest = stats.outdated.latestVersion;
  // FleetCounts only carries the effective version; where it comes from (UI/hold) is in the fleet list.
  const sources = new Map(
    (useFleets().data?.items ?? []).map((f) => [f.fleet.name, f.status.versionTarget.source]),
  );
  return (
    <SectionCard
      h="100%"
      padded={false}
      title={t('dashboard.fleets.title')}
      actions={
        <Anchor component={Link} to="/fleets" fz="sm">
          {t('common.viewAll')}
        </Anchor>
      }
    >
      {stats.fleets.length === 0 ? (
        <EmptyState
          compact
          title={t('dashboard.fleets.empty')}
          text={t('dashboard.fleets.emptyHint')}
        />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="xs" horizontalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.fleet')}</Table.Th>
                <Table.Th ta="right">{t('common.agents')}</Table.Th>
                <Table.Th>{t('common.online')}</Table.Th>
                <Table.Th>{t('dashboard.fleets.inSync')}</Table.Th>
                <Table.Th>{t('dashboard.fleets.onTarget')}</Table.Th>
                <Table.Th>{t('dashboard.fleets.targetVersion')}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {stats.fleets.map((f) => (
                <Table.Tr key={f.fleet}>
                  <Table.Td>
                    <Anchor
                      component={Link}
                      to={fleetLink(f.fleet)}
                      fw={600}
                      fz="sm"
                      style={{ whiteSpace: 'nowrap' }}
                    >
                      {f.fleet}
                    </Anchor>
                  </Table.Td>
                  <Table.Td ta="right" className="myr-tabular">
                    <Anchor
                      component={Link}
                      to={agentsLink({ fleet: [f.fleet] })}
                      c="inherit"
                      fz="sm"
                    >
                      {formatNumber(lang, f.total)}
                    </Anchor>
                  </Table.Td>
                  <Table.Td>
                    <Ratio value={f.online} total={f.total} color="teal" />
                  </Table.Td>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      <Ratio value={f.inSync} total={f.total} color="teal" />
                      {f.failed > 0 && (
                        <Badge size="xs" color="red" variant="light">
                          {f.failed}
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <Ratio
                      value={f.onTarget}
                      total={f.total}
                      color={f.onTarget === f.total ? 'teal' : 'orange'}
                    />
                  </Table.Td>
                  <Table.Td>
                    {f.targetVersion ? (
                      <Group gap={6} wrap="nowrap">
                        <Text className="myr-mono" fz="sm">
                          {f.targetVersion}
                        </Text>
                        <VersionSourceBadge
                          source={sources.get(f.fleet) ?? 'git'}
                          size="xs"
                          short
                        />
                        {latest && compareVersions(f.targetVersion, latest) < 0 && (
                          <Tooltip label={t('dashboard.fleets.behindLatest', { latest })}>
                            <IconAlertTriangle
                              size={14}
                              color="var(--mantine-color-orange-6)"
                              aria-label={t('version.outdated')}
                            />
                          </Tooltip>
                        )}
                      </Group>
                    ) : (
                      <Group gap={6} wrap="nowrap">
                        <Text c="dimmed">–</Text>
                        <VersionSourceBadge
                          source={sources.get(f.fleet) ?? 'none'}
                          size="xs"
                          short
                        />
                      </Group>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </div>
      )}
    </SectionCard>
  );
}
