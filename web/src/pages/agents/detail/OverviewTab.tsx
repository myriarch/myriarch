// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Badge, Code, Grid, Group, Stack, Table, Text, Tooltip } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import type { AgentDetail } from '@/api/types';
import { CopyIconButton } from '@/components/CopyButton';
import { KeyValue } from '@/components/misc';
import { SectionCard } from '@/components/SectionCard';
import {
  ConfigSyncBadge,
  ConnectionBadge,
  HealthBadge,
  StatusBadge,
} from '@/components/StatusBadge';
import { DateTime, TimeAgo } from '@/components/TimeAgo';
import { VersionBadge } from '@/components/VersionBadge';
import { EmptyState } from '@/components/states';
import { agentLink, fleetLink } from '@/lib/links';
import { shortHash } from '@/lib/format';
import { compareVersions } from '@/lib/format';
import type { Tone } from '@/theme/status';

function Hash({ value }: { value: string | undefined }) {
  if (!value) return <Text c="dimmed">–</Text>;
  return (
    <Group gap={4} wrap="nowrap">
      <Tooltip label={value}>
        <Code>{shortHash(value, 12)}</Code>
      </Tooltip>
      <CopyIconButton text={value} size="xs" />
    </Group>
  );
}

function AttrTable({ title, attrs }: { title: string; attrs: Record<string, string> }) {
  const entries = Object.entries(attrs).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    <Stack gap={4}>
      <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
        {title}
      </Text>
      <Table fz="sm" verticalSpacing={4} withRowBorders>
        <Table.Tbody>
          {entries.map(([k, v]) => (
            <Table.Tr key={k}>
              <Table.Td w="38%" className="myr-mono" style={{ wordBreak: 'break-all' }}>
                {k}
              </Table.Td>
              <Table.Td style={{ wordBreak: 'break-all' }}>{v}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

const packageTone: Record<string, Tone> = {
  installed: 'success',
  install_pending: 'warning',
  installing: 'info',
  downloading: 'info',
  install_failed: 'danger',
};

export function OverviewTab({ detail }: { detail: AgentDetail }) {
  const { t } = useTranslation();
  const { agent, linkedAgent } = detail;
  const target = agent.assignment.targetVersion;
  const behindTarget = !!target && compareVersions(agent.version, target) < 0;
  const pkgs = Object.values(agent.packageStatus?.packages ?? {});

  return (
    <Grid gap="md">
      <Grid.Col span={{ base: 12, lg: 6 }}>
        <SectionCard title={t('agentDetail.identity')}>
          <KeyValue
            items={[
              { label: t('common.name'), value: agent.hostname || '–' },
              {
                label: 'ID',
                value: (
                  <Group gap={4} wrap="nowrap">
                    <Code>{agent.id}</Code>
                    <CopyIconButton text={agent.id} size="xs" />
                  </Group>
                ),
              },
              { label: t('agents.columns.kind'), value: t(`enum.kind.${agent.kind}`) },
              {
                label: t('agentDetail.serviceName'),
                value: <span className="myr-mono">{agent.serviceName}</span>,
              },
              {
                label: t('common.version'),
                value: (
                  <VersionBadge
                    version={agent.version}
                    outdated={detail.outdated}
                    latest={detail.latestVersion}
                  />
                ),
              },
              {
                label: t('agentDetail.targetVersion'),
                value: target ? (
                  <Group gap={6}>
                    <span className="myr-mono">{target}</span>
                    {behindTarget && (
                      <Badge color="yellow" size="xs" variant="light">
                        {t('agentDetail.behindTarget')}
                      </Badge>
                    )}
                    {!behindTarget && agent.version === target && (
                      <Badge color="teal" size="xs" variant="light">
                        {t('agentDetail.onTarget')}
                      </Badge>
                    )}
                  </Group>
                ) : (
                  '–'
                ),
              },
              {
                label: t('agentDetail.latestVersion'),
                value: detail.latestVersion ? (
                  <span className="myr-mono">{detail.latestVersion}</span>
                ) : (
                  '–'
                ),
              },
              {
                label: t('agentDetail.os'),
                value: `${agent.osType} · ${agent.arch}${agent.osDescription ? ` · ${agent.osDescription}` : ''}`,
              },
              { label: t('agentDetail.firstSeen'), value: <DateTime value={agent.firstSeenAt} /> },
              { label: t('common.updated'), value: <TimeAgo value={agent.updatedAt} /> },
            ]}
          />
        </SectionCard>
      </Grid.Col>

      <Grid.Col span={{ base: 12, lg: 6 }}>
        <Stack gap="md">
          <SectionCard title={t('agentDetail.connection')}>
            <KeyValue
              items={[
                {
                  label: t('common.status'),
                  value: <ConnectionBadge status={agent.connection.status} />,
                },
                {
                  label: 'Health',
                  value: (
                    <HealthBadge healthy={agent.health?.healthy} detail={agent.health?.status} />
                  ),
                },
                {
                  label: t('agents.columns.lastSeen'),
                  value: <TimeAgo value={agent.connection.lastSeenAt} />,
                },
                {
                  label: t('agentDetail.connectedAt'),
                  value: <DateTime value={agent.connection.connectedAt} />,
                },
                {
                  label: t('agentDetail.disconnectedAt'),
                  value: <DateTime value={agent.connection.disconnectedAt} />,
                },
                { label: t('agentDetail.transport'), value: agent.connection.transport ?? '–' },
                {
                  label: t('agentDetail.remoteAddr'),
                  value: <span className="myr-mono">{agent.connection.remoteAddr ?? '–'}</span>,
                },
                {
                  label: t('agentDetail.replica'),
                  value: <span className="myr-mono">{agent.connection.replicaId ?? '–'}</span>,
                },
              ]}
            />
          </SectionCard>
          <SectionCard title={t('agentDetail.assignment')}>
            <KeyValue
              items={[
                {
                  label: t('common.fleet'),
                  value: agent.assignment.fleetName ? (
                    <Anchor component={Link} to={fleetLink(agent.assignment.fleetName)}>
                      {agent.assignment.fleetName}
                    </Anchor>
                  ) : (
                    <Text c="dimmed" span>
                      {t('common.unassigned')}
                    </Text>
                  ),
                },
                {
                  label: t('agents.columns.configSync'),
                  value: <ConfigSyncBadge state={detail.configSync} />,
                },
                {
                  label: t('agentDetail.desiredHash'),
                  value: <Hash value={agent.assignment.desiredConfigHash} />,
                },
                {
                  label: t('agentDetail.assignedHash'),
                  value: <Hash value={agent.assignment.assignedConfigHash} />,
                },
                {
                  label: t('agentDetail.failedHash'),
                  value: <Hash value={agent.assignment.failedConfigHash} />,
                  hidden: !agent.assignment.failedConfigHash,
                },
                {
                  label: t('agentDetail.assignedAt'),
                  value: <DateTime value={agent.assignment.assignedAt} />,
                },
                {
                  label: t('agentDetail.remoteConfig'),
                  value: (
                    <Stack gap={2}>
                      <StatusBadge
                        tone={
                          agent.remoteConfig.status === 'applied'
                            ? 'success'
                            : agent.remoteConfig.status === 'failed'
                              ? 'danger'
                              : agent.remoteConfig.status === 'applying'
                                ? 'info'
                                : 'neutral'
                        }
                        label={t(`enum.remoteConfig.${agent.remoteConfig.status}`)}
                      />
                      {agent.remoteConfig.errorMessage && (
                        <Text fz="xs" c="red" style={{ whiteSpace: 'pre-wrap' }}>
                          {agent.remoteConfig.errorMessage}
                        </Text>
                      )}
                    </Stack>
                  ),
                },
                {
                  label: t('agentDetail.assignmentError'),
                  value: (
                    <Text c="red" fz="sm">
                      {agent.assignment.error}
                    </Text>
                  ),
                  hidden: !agent.assignment.error,
                },
              ]}
            />
          </SectionCard>
        </Stack>
      </Grid.Col>

      <Grid.Col span={{ base: 12, lg: 6 }}>
        <SectionCard title={t('agentDetail.attributes')}>
          <Stack gap="md">
            <AttrTable title={t('agentDetail.identifying')} attrs={agent.identifyingAttributes} />
            <AttrTable
              title={t('agentDetail.nonIdentifying')}
              attrs={agent.nonIdentifyingAttributes}
            />
            <AttrTable title={t('agentDetail.labels')} attrs={agent.labels} />
            {Object.keys(agent.identifyingAttributes).length +
              Object.keys(agent.nonIdentifyingAttributes).length +
              Object.keys(agent.labels).length ===
              0 && <EmptyState compact title={t('agentDetail.noAttributes')} />}
          </Stack>
        </SectionCard>
      </Grid.Col>

      <Grid.Col span={{ base: 12, lg: 6 }}>
        <Stack gap="md">
          <SectionCard
            title={t('agentDetail.capabilities')}
            description={t('agentDetail.capabilitiesHint')}
          >
            <Group gap={6} wrap="wrap">
              {detail.capabilityNames.length === 0 && (
                <Text c="dimmed" fz="sm">
                  –
                </Text>
              )}
              {detail.capabilityNames.map((c) => (
                <Badge
                  key={c}
                  variant="outline"
                  color="gray"
                  size="sm"
                  styles={{ label: { textTransform: 'none' } }}
                >
                  {c}
                </Badge>
              ))}
            </Group>
          </SectionCard>
          <SectionCard title={t('agentDetail.updaterAndPackages')}>
            <Stack gap="sm">
              {linkedAgent ? (
                <Group gap="sm" wrap="nowrap">
                  <Text fz="sm">{t('agentDetail.linkedAgent')}:</Text>
                  <Anchor component={Link} to={agentLink(linkedAgent.id)} fz="sm" fw={600}>
                    {linkedAgent.hostname || linkedAgent.id.slice(0, 8)}
                  </Anchor>
                  <Badge size="xs" variant="outline" color="gray">
                    {t(`enum.kind.${linkedAgent.kind}`)}
                  </Badge>
                  <ConnectionBadge status={linkedAgent.status} />
                </Group>
              ) : agent.updater ? (
                <Group gap="sm">
                  <Text fz="sm">{t('agentDetail.linkedAgent')}:</Text>
                  <Anchor component={Link} to={agentLink(agent.updater.agentId)} fz="sm" fw={600}>
                    {agent.updater.agentId.slice(0, 8)}
                  </Anchor>
                  <StatusBadge
                    tone={agent.updater.online ? 'success' : 'neutral'}
                    label={t(
                      agent.updater.online ? 'enum.connection.online' : 'enum.connection.offline',
                    )}
                  />
                </Group>
              ) : (
                <Text c="dimmed" fz="sm">
                  {t('agentDetail.noUpdater')}
                </Text>
              )}
              {pkgs.length > 0 ? (
                <div className="myr-scroll-x">
                  <Table fz="sm" verticalSpacing={4}>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>{t('common.name')}</Table.Th>
                        <Table.Th>{t('agentDetail.installed')}</Table.Th>
                        <Table.Th>{t('agentDetail.offered')}</Table.Th>
                        <Table.Th>{t('common.status')}</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {pkgs.map((p) => (
                        <Table.Tr key={p.name}>
                          <Table.Td className="myr-mono">{p.name}</Table.Td>
                          <Table.Td className="myr-mono">{p.agentHasVersion ?? '–'}</Table.Td>
                          <Table.Td className="myr-mono">{p.serverOfferedVersion ?? '–'}</Table.Td>
                          <Table.Td>
                            <Stack gap={2}>
                              <StatusBadge
                                tone={packageTone[p.status] ?? 'neutral'}
                                label={`${t(`enum.packageStatus.${p.status}`)}${p.status === 'downloading' && p.downloadPercent !== undefined ? ` ${Math.round(p.downloadPercent)}%` : ''}`}
                              />
                              {p.errorMessage && (
                                <Text fz="xs" c="red">
                                  {p.errorMessage}
                                </Text>
                              )}
                            </Stack>
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </div>
              ) : (
                <Text c="dimmed" fz="sm">
                  {t('agentDetail.noPackages')}
                </Text>
              )}
              {agent.packageStatus?.errorMessage && (
                <Text c="red" fz="sm">
                  {agent.packageStatus.errorMessage}
                </Text>
              )}
            </Stack>
          </SectionCard>
        </Stack>
      </Grid.Col>
    </Grid>
  );
}
