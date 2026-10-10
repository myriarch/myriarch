// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Button, Grid, Group, SimpleGrid, Skeleton, Stack, Text } from '@mantine/core';
import {
  IconAlertTriangle,
  IconHeartbeat,
  IconPlugConnected,
  IconRefresh,
  IconServer2,
  IconTargetArrow,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { useOverviewStats } from '@/api/hooks/overview';
import { GitOpsChip } from '@/components/GitOpsChip';
import { PageHeader } from '@/components/PageHeader';
import { StatCard } from '@/components/StatCard';
import { CardSkeleton, ErrorState } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { useLang } from '@/i18n/useLang';
import { formatNumber, formatPercent } from '@/lib/format';
import { agentsLink } from '@/lib/links';
import { ActiveRollouts } from './ActiveRollouts';
import { ConfigSyncCard } from './ConfigSyncCard';
import { FleetsTable } from './FleetsTable';
import { ReleaseBanner } from './ReleaseBanner';
import { VersionDistribution } from './VersionDistribution';

function DashboardSkeleton() {
  return (
    <Stack gap="md" aria-busy="true">
      <SimpleGrid cols={{ base: 1, xs: 2, lg: 4 }}>
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} height={104} radius="md" />
        ))}
      </SimpleGrid>
      <Grid>
        <Grid.Col span={{ base: 12, lg: 7 }}>
          <CardSkeleton height={300} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 5 }}>
          <CardSkeleton height={300} />
        </Grid.Col>
      </Grid>
    </Stack>
  );
}

export default function DashboardPage() {
  const { t } = useTranslation();
  const lang = useLang();
  const { data: stats, isPending, error, refetch, isFetching } = useOverviewStats();

  const header = (
    <PageHeader
      title={t('dashboard.title')}
      subtitle={
        stats ? (
          <>
            {t('dashboard.updatedAt')} <TimeAgo value={stats.generatedAt} />
          </>
        ) : undefined
      }
      actions={
        <Group gap="sm">
          {stats && <GitOpsChip gitops={stats.gitops} />}
          <Button
            variant="default"
            size="xs"
            leftSection={<IconRefresh size={14} />}
            loading={isFetching && !isPending}
            onClick={() => void refetch()}
          >
            {t('common.refresh')}
          </Button>
        </Group>
      }
    />
  );

  if (isPending) {
    return (
      <>
        {header}
        <DashboardSkeleton />
      </>
    );
  }
  if (error || !stats) {
    return (
      <>
        {header}
        <ErrorState error={error} onRetry={() => void refetch()} />
      </>
    );
  }

  const { agents, outdated } = stats;
  const onlinePct = agents.total > 0 ? (agents.online / agents.total) * 100 : 0;
  const fleetTotal = stats.fleets.reduce((s, f) => s + f.total, 0);
  const onTarget = stats.fleets.reduce((s, f) => s + f.onTarget, 0);
  const onTargetPct = fleetTotal > 0 ? (onTarget / fleetTotal) * 100 : 0;

  return (
    <Stack gap="md">
      {header}
      {stats.latestRelease && <ReleaseBanner release={stats.latestRelease} />}

      <SimpleGrid cols={{ base: 1, xs: 2, md: 3, xl: 5 }} spacing="md">
        <StatCard
          label={t('dashboard.tiles.total')}
          value={formatNumber(lang, agents.total)}
          icon={<IconServer2 size={22} />}
          to="/agents"
          sub={t('dashboard.tiles.totalSub', {
            collectors: agents.collectors,
            updaters: agents.updaters,
          })}
        />
        <StatCard
          label={t('dashboard.tiles.online')}
          value={formatNumber(lang, agents.online)}
          color="teal"
          ring={onlinePct}
          icon={<IconPlugConnected size={20} />}
          to={agentsLink({ status: ['online'] })}
          sub={
            <Text span fz="xs" c={agents.offline > 0 ? 'orange' : 'dimmed'}>
              {t('dashboard.tiles.offlineSub', { count: agents.offline })}
            </Text>
          }
        />
        <StatCard
          label={t('dashboard.tiles.healthy')}
          value={formatNumber(lang, agents.healthy)}
          color={agents.unhealthy > 0 ? 'red' : 'teal'}
          icon={<IconHeartbeat size={22} />}
          to={agentsLink({ healthy: false })}
          sub={t('dashboard.tiles.healthySub', {
            unhealthy: agents.unhealthy,
            unknown: agents.healthUnknown,
          })}
        />
        <StatCard
          label={t('dashboard.tiles.outdated')}
          value={formatPercent(lang, outdated.percent, 1)}
          color={outdated.count > 0 ? 'orange' : 'teal'}
          icon={<IconAlertTriangle size={22} />}
          to={agentsLink({ outdated: true })}
          sub={
            outdated.latestVersion
              ? t('dashboard.tiles.outdatedSub', {
                  count: outdated.count,
                  latest: outdated.latestVersion,
                })
              : t('dashboard.tiles.outdatedNoLatest', { count: outdated.count })
          }
        />
        <StatCard
          label={t('dashboard.tiles.onTarget')}
          value={formatPercent(lang, onTargetPct, 1)}
          color={onTargetPct >= 99 ? 'teal' : 'yellow'}
          ring={onTargetPct}
          icon={<IconTargetArrow size={20} />}
          to="/fleets"
          sub={t('dashboard.tiles.onTargetSub', {
            count: onTarget,
            total: formatNumber(lang, fleetTotal),
          })}
        />
      </SimpleGrid>

      <Grid gap="md">
        <Grid.Col span={{ base: 12, lg: 7 }}>
          <VersionDistribution stats={stats} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 5 }}>
          <ConfigSyncCard stats={stats} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 7 }}>
          <FleetsTable stats={stats} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 5 }}>
          <ActiveRollouts rollouts={stats.activeRollouts} />
        </Grid.Col>
      </Grid>
    </Stack>
  );
}
