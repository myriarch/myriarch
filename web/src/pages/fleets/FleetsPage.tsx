// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Anchor, Badge, Group, Progress, Table, Tabs, Text, Tooltip } from '@mantine/core';
import {
  IconAlertTriangle,
  IconGitBranch,
  IconHierarchy2,
  IconPlayerPlay,
  IconShieldCheck,
  IconTargetArrow,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { useExecutors, useFleets, useRolloutPolicies } from '@/api/hooks/fleets';
import { PageHeader } from '@/components/PageHeader';
import { SectionCard } from '@/components/SectionCard';
import { SelectorView } from '@/components/misc';
import { VersionSourceBadge } from '@/components/VersionSourceBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { useLang } from '@/i18n/useLang';
import { formatNumber } from '@/lib/format';
import { agentsLink, blueprintLink, fleetLink, policyLink } from '@/lib/links';
import { FleetMatchChecker } from './FleetMatchChecker';

const TABS = ['fleets', 'policies', 'executors', 'match'] as const;
type TabId = (typeof TABS)[number];

function FleetsTab() {
  const { t } = useTranslation();
  const lang = useLang();
  const { data, isPending, error, refetch } = useFleets();
  if (isPending)
    return (
      <SectionCard padded={false}>
        <TableSkeleton cols={7} rows={5} />
      </SectionCard>
    );
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  if (data.items.length === 0) {
    return (
      <SectionCard>
        <EmptyState
          icon={<IconHierarchy2 size={28} />}
          title={t('fleets.empty')}
          text={t('fleets.emptyHint')}
        />
      </SectionCard>
    );
  }
  const items = [...data.items].sort(
    (a, b) => b.fleet.priority - a.fleet.priority || a.fleet.name.localeCompare(b.fleet.name),
  );
  return (
    <SectionCard padded={false}>
      <div className="myr-scroll-x">
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.fleet')}</Table.Th>
              <Table.Th ta="right">{t('fleets.priority')}</Table.Th>
              <Table.Th>{t('fleets.selector')}</Table.Th>
              <Table.Th>{t('fleets.blueprint')}</Table.Th>
              <Table.Th ta="right">{t('common.agents')}</Table.Th>
              <Table.Th>{t('dashboard.fleets.inSync')}</Table.Th>
              <Table.Th>{t('dashboard.fleets.targetVersion')}</Table.Th>
              <Table.Th>{t('fleets.executor')}</Table.Th>
              <Table.Th>{t('common.status')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {items.map(({ fleet, status }) => {
              const pct =
                status.agents.total > 0 ? (status.agents.inSync / status.agents.total) * 100 : 0;
              return (
                <Table.Tr key={fleet.name}>
                  <Table.Td>
                    <Anchor component={Link} to={fleetLink(fleet.name)} fw={600}>
                      {fleet.displayName || fleet.name}
                    </Anchor>
                    <Text fz="xs" c="dimmed" className="myr-mono">
                      {fleet.name}
                    </Text>
                  </Table.Td>
                  <Table.Td ta="right" className="myr-tabular">
                    {fleet.priority}
                  </Table.Td>
                  <Table.Td maw={320}>
                    <SelectorView selector={fleet.selector} />
                  </Table.Td>
                  <Table.Td>
                    <Anchor component={Link} to={blueprintLink(fleet.blueprint)} fz="sm">
                      {fleet.blueprint}
                    </Anchor>
                  </Table.Td>
                  <Table.Td ta="right" className="myr-tabular">
                    <Anchor component={Link} to={agentsLink({ fleet: [fleet.name] })} c="inherit">
                      {formatNumber(lang, status.agents.total)}
                    </Anchor>
                    <Text fz="xs" c="dimmed">
                      {t('fleets.onlineCount', { count: status.agents.online })}
                    </Text>
                  </Table.Td>
                  <Table.Td miw={130}>
                    <Group gap={6} wrap="nowrap">
                      <Progress
                        value={pct}
                        size="sm"
                        color={pct === 100 ? 'teal' : 'yellow'}
                        style={{ flex: 1 }}
                        aria-label={`${Math.round(pct)}%`}
                      />
                      <Text fz="xs" className="myr-tabular" w={34} ta="right">
                        {Math.round(pct)}%
                      </Text>
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <Group gap={6} wrap="nowrap">
                      {/* on hold there is no target: the Git version is not what the fleet aims at */}
                      <Text className="myr-mono">{status.versionTarget.version ?? '–'}</Text>
                      <VersionSourceBadge source={status.versionTarget.source} size="xs" short />
                      {status.behindLatest && (
                        <Tooltip label={t('fleets.behindLatest', { latest: status.latestVersion })}>
                          <Badge size="xs" color="orange" variant="light">
                            {t('version.outdated')}
                          </Badge>
                        </Tooltip>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>{t(`enum.executor.${fleet.update.executor ?? 'none'}`)}</Table.Td>
                  <Table.Td>
                    <Group gap={4} wrap="nowrap">
                      {status.activeRollouts.length > 0 && (
                        <Tooltip
                          label={t('fleets.activeRollouts', {
                            count: status.activeRollouts.length,
                          })}
                        >
                          <Badge
                            size="xs"
                            color="blue"
                            variant="light"
                            leftSection={<IconPlayerPlay size={10} />}
                            component={Link}
                            to={`/rollouts?fleet=${fleet.name}&active=true`}
                            style={{ cursor: 'pointer' }}
                          >
                            {status.activeRollouts.length}
                          </Badge>
                        </Tooltip>
                      )}
                      {status.renderErrors > 0 && (
                        <Tooltip label={t('fleets.renderErrors', { count: status.renderErrors })}>
                          <Badge
                            size="xs"
                            color="red"
                            variant="light"
                            leftSection={<IconAlertTriangle size={10} />}
                          >
                            {status.renderErrors}
                          </Badge>
                        </Tooltip>
                      )}
                    </Group>
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      </div>
    </SectionCard>
  );
}

function PoliciesTab() {
  const { t } = useTranslation();
  const policies = useRolloutPolicies();
  const fleets = useFleets();
  if (policies.isPending)
    return (
      <SectionCard padded={false}>
        <TableSkeleton cols={5} rows={4} />
      </SectionCard>
    );
  if (policies.error)
    return <ErrorState error={policies.error} onRetry={() => void policies.refetch()} />;
  if (policies.data.items.length === 0) {
    return (
      <SectionCard>
        <EmptyState icon={<IconShieldCheck size={28} />} title={t('policy.empty')} />
      </SectionCard>
    );
  }
  const usedBy = (name: string) =>
    (fleets.data?.items ?? [])
      .filter(
        (f) =>
          (f.fleet.rollout.configPolicy ?? 'default') === name ||
          (f.fleet.rollout.versionPolicy ?? 'default') === name,
      )
      .map((f) => f.fleet.name);
  return (
    <SectionCard padded={false}>
      <div className="myr-scroll-x">
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('common.description')}</Table.Th>
              <Table.Th>{t('policy.waves')}</Table.Th>
              <Table.Th>{t('policy.onFailure')}</Table.Th>
              <Table.Th>{t('policy.windows')}</Table.Th>
              <Table.Th>{t('policy.usedBy')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {policies.data.items.map((p) => (
              <Table.Tr key={p.name}>
                <Table.Td>
                  <Anchor component={Link} to={policyLink(p.name)} fw={600}>
                    {p.name}
                  </Anchor>
                </Table.Td>
                <Table.Td maw={320}>
                  <Text fz="sm" c="dimmed">
                    {p.description ?? '–'}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {p.waves.map((w) => (
                      <Badge
                        key={w.name}
                        size="sm"
                        variant="light"
                        color={w.requireApproval ? 'yellow' : 'gray'}
                        style={{ textTransform: 'none' }}
                      >
                        {w.name}
                        {w.percent !== undefined ? ` ${w.percent}%` : ''}
                      </Badge>
                    ))}
                  </Group>
                </Table.Td>
                <Table.Td>{p.onFailure ? t(`enum.onFailure.${p.onFailure}`) : '–'}</Table.Td>
                <Table.Td>
                  {(p.maintenanceWindows ?? []).length > 0
                    ? t('policy.windowCount', { count: p.maintenanceWindows!.length })
                    : t('policy.noWindowsShort')}
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {usedBy(p.name).map((f) => (
                      <Badge
                        key={f}
                        variant="outline"
                        color="gray"
                        component={Link}
                        to={fleetLink(f)}
                        style={{ textTransform: 'none', cursor: 'pointer' }}
                      >
                        {f}
                      </Badge>
                    ))}
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
    </SectionCard>
  );
}

function ExecutorsTab() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useExecutors();
  if (isPending)
    return (
      <SectionCard padded={false}>
        <TableSkeleton cols={5} rows={3} />
      </SectionCard>
    );
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  if (data.items.length === 0) {
    return (
      <SectionCard>
        <EmptyState title={t('executors.empty')} text={t('executors.emptyHint')} />
      </SectionCard>
    );
  }
  return (
    <SectionCard padded={false}>
      <div className="myr-scroll-x">
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('common.type')}</Table.Th>
              <Table.Th>{t('executors.target')}</Table.Th>
              <Table.Th>{t('common.source')}</Table.Th>
              <Table.Th>{t('policy.usedBy')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {data.items.map((e) => (
              <Table.Tr key={e.name}>
                <Table.Td>
                  <Text fw={600} fz="sm">
                    {e.name}
                  </Text>
                  {e.description && (
                    <Text fz="xs" c="dimmed">
                      {e.description}
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  <Badge variant="light" color="gray">
                    {t(`enum.executor.${e.type}`)}
                  </Badge>
                </Table.Td>
                <Table.Td className="myr-mono" style={{ wordBreak: 'break-all' }}>
                  {e.target || '–'}
                </Table.Td>
                <Table.Td>
                  <Group gap={4} wrap="nowrap">
                    <IconGitBranch size={13} />
                    <Text className="myr-mono" fz="xs">
                      {e.source.path}
                    </Text>
                  </Group>
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {e.usedBy.map((f) => (
                      <Badge
                        key={f}
                        variant="outline"
                        color="gray"
                        component={Link}
                        to={fleetLink(f)}
                        style={{ textTransform: 'none', cursor: 'pointer' }}
                      >
                        {f}
                      </Badge>
                    ))}
                    {e.usedBy.length === 0 && (
                      <Text c="dimmed" fz="sm">
                        –
                      </Text>
                    )}
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
    </SectionCard>
  );
}

export default function FleetsPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: TabId = (TABS as readonly string[]).includes(raw ?? '') ? (raw as TabId) : 'fleets';
  return (
    <>
      <PageHeader title={t('fleets.title')} subtitle={t('fleets.subtitle')} />
      <Alert variant="light" color="gray" mb="md" icon={<IconGitBranch size={16} />} py="xs">
        {t('common.readOnlyGit')}
      </Alert>
      <Tabs
        value={tab}
        onChange={(v) => setParams(v && v !== 'fleets' ? { tab: v } : {}, { replace: true })}
      >
        <Tabs.List mb="md" style={{ flexWrap: 'nowrap', overflowX: 'auto' }}>
          <Tabs.Tab value="fleets" leftSection={<IconHierarchy2 size={16} />}>
            {t('fleets.tabs.fleets')}
          </Tabs.Tab>
          <Tabs.Tab value="policies" leftSection={<IconShieldCheck size={16} />}>
            {t('fleets.tabs.policies')}
          </Tabs.Tab>
          <Tabs.Tab value="executors" leftSection={<IconPlayerPlay size={16} />}>
            {t('fleets.tabs.executors')}
          </Tabs.Tab>
          <Tabs.Tab value="match" leftSection={<IconTargetArrow size={16} />}>
            {t('fleets.tabs.match')}
          </Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="fleets">
          <FleetsTab />
        </Tabs.Panel>
        <Tabs.Panel value="policies">
          <PoliciesTab />
        </Tabs.Panel>
        <Tabs.Panel value="executors">
          <ExecutorsTab />
        </Tabs.Panel>
        <Tabs.Panel value="match">
          <FleetMatchChecker />
        </Tabs.Panel>
      </Tabs>
    </>
  );
}
