// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Group, Table, Text, Tooltip } from '@mantine/core';
import { IconCpu, IconRefreshDot, IconServer2 } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import type { AgentSort, AgentSummary } from '@/api/types';
import { SortableTh } from '@/components/misc';
import { ConfigSyncBadge, ConnectionBadge, HealthBadge } from '@/components/StatusBadge';
import { TimeAgo } from '@/components/TimeAgo';
import { VersionBadge } from '@/components/VersionBadge';
import { agentLink, fleetLink } from '@/lib/links';
import { shortId } from '@/lib/format';

function SiteCell({ site }: { site: AgentSummary['site'] }) {
  if (!site || (!site['site.type'] && !site['site.id'])) return <Text c="dimmed">–</Text>;
  const label = [site['site.type'], site['site.id']].filter(Boolean).join(' / ');
  const tip = [site['site.name'], site['site.country'], site['site.region']]
    .filter(Boolean)
    .join(' · ');
  const body = (
    <Text fz="sm" className="myr-truncate" maw={200}>
      {label}
    </Text>
  );
  return tip ? <Tooltip label={tip}>{body}</Tooltip> : body;
}

export function AgentRow({ agent, latest }: { agent: AgentSummary; latest?: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <Table.Tr
      className="myr-row-link"
      onClick={(e) => {
        // let real links/buttons inside the row handle their own clicks
        if ((e.target as HTMLElement).closest('a,button')) return;
        void navigate(agentLink(agent.id));
      }}
      data-testid="agent-row"
      data-agent-id={agent.id}
    >
      <Table.Td>
        <Group gap={8} wrap="nowrap">
          {agent.kind === 'updater' ? (
            <Tooltip label={t('enum.kind.updater')}>
              <IconRefreshDot
                size={16}
                style={{ flex: '0 0 auto', opacity: 0.7 }}
                aria-label={t('enum.kind.updater')}
              />
            </Tooltip>
          ) : (
            <IconServer2 size={16} style={{ flex: '0 0 auto', opacity: 0.7 }} aria-hidden />
          )}
          <div style={{ minWidth: 0 }}>
            <Anchor
              component={Link}
              to={agentLink(agent.id)}
              fw={600}
              fz="sm"
              className="myr-truncate"
              maw={260}
            >
              {agent.hostname || shortId(agent.id)}
            </Anchor>
            <Text fz="xs" c="dimmed" className="myr-mono">
              {shortId(agent.id)}
            </Text>
          </div>
        </Group>
      </Table.Td>
      <Table.Td>
        <SiteCell site={agent.site} />
      </Table.Td>
      <Table.Td>
        {agent.fleetName ? (
          <Anchor component={Link} to={fleetLink(agent.fleetName)} fz="sm">
            {agent.fleetName}
          </Anchor>
        ) : (
          <Text c="dimmed" fz="sm">
            –
          </Text>
        )}
      </Table.Td>
      <Table.Td>
        <VersionBadge version={agent.version} outdated={agent.outdated} latest={latest} />
      </Table.Td>
      <Table.Td>
        <ConnectionBadge status={agent.status} />
      </Table.Td>
      <Table.Td>
        <HealthBadge healthy={agent.healthy} detail={agent.healthStatus} />
      </Table.Td>
      <Table.Td>
        <ConfigSyncBadge state={agent.configSync} />
      </Table.Td>
      <Table.Td>
        <TimeAgo value={agent.lastSeenAt} />
      </Table.Td>
      <Table.Td ta="center">
        {agent.hasUpdater && (
          <Tooltip label={t('agents.hasUpdater')}>
            <IconCpu size={16} aria-label={t('agents.hasUpdater')} />
          </Tooltip>
        )}
      </Table.Td>
    </Table.Tr>
  );
}

/** Server-side sorted table; the header cells only emit sort fields understood by listAgents. */
export function AgentTable({
  items,
  sort,
  onSort,
  latest,
}: {
  items: AgentSummary[];
  sort: AgentSort;
  onSort: (sort: AgentSort) => void;
  latest?: string;
}) {
  const { t } = useTranslation();
  const set = (s: string) => onSort(s as AgentSort);
  return (
    <Table
      fz="sm"
      verticalSpacing={6}
      miw={980}
      aria-label={t('nav.agents')}
      className="myr-nowrap-table"
    >
      <Table.Thead>
        <Table.Tr>
          <SortableTh field="hostname" sort={sort} onSort={set}>
            {t('agents.columns.host')}
          </SortableTh>
          <SortableTh field="siteId" sort={sort} onSort={set}>
            {t('agents.columns.site')}
          </SortableTh>
          <SortableTh field="fleet" sort={sort} onSort={set}>
            {t('common.fleet')}
          </SortableTh>
          <SortableTh field="version" sort={sort} onSort={set}>
            {t('common.version')}
          </SortableTh>
          <SortableTh field="status" sort={sort} onSort={set}>
            {t('common.status')}
          </SortableTh>
          <SortableTh field="health" sort={sort} onSort={set}>
            {t('agents.columns.health')}
          </SortableTh>
          <SortableTh field="configSync" sort={sort} onSort={set}>
            {t('agents.columns.configSync')}
          </SortableTh>
          <SortableTh field="lastSeenAt" sort={sort} onSort={set}>
            {t('agents.columns.lastSeen')}
          </SortableTh>
          <Table.Th className="myr-sticky-th" ta="center" w={64}>
            {t('agents.columns.updater')}
          </Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {items.map((a) => (
          <AgentRow key={a.id} agent={a} latest={latest} />
        ))}
      </Table.Tbody>
    </Table>
  );
}
