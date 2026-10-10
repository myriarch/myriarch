// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Badge, Code, Grid, Group, Skeleton, Stack, Table, Text } from '@mantine/core';
import { IconCrown } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { useSystemInfo } from '@/api/hooks/overview';
import { CopyIconButton } from '@/components/CopyButton';
import { KeyValue } from '@/components/misc';
import { SectionCard } from '@/components/SectionCard';
import { StatusBadge } from '@/components/StatusBadge';
import { ErrorState } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';

export default function SystemTab() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useSystemInfo();
  if (isPending) return <Skeleton height={320} />;
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  const s = data.settings;
  const features = [
    ['oidc', data.features.oidc],
    ['devLogin', data.features.devLogin],
    ['bootstrapAdmin', data.features.bootstrapAdmin],
    ['gitops', data.features.gitops],
    ['releases', data.features.releases],
    ['devMode', data.features.devMode],
  ] as const;

  return (
    <Grid gap="md">
      <Grid.Col span={{ base: 12, lg: 6 }}>
        <Stack gap="md">
          <SectionCard title={t('settings.system.about')}>
            <KeyValue
              labelWidth={150}
              items={[
                {
                  label: t('common.version'),
                  value: <span className="myr-mono">{data.version}</span>,
                },
                {
                  label: t('settings.system.commit'),
                  value: <Code>{data.commit.slice(0, 12)}</Code>,
                },
                {
                  label: t('settings.system.buildDate'),
                  value: <DateTime value={data.buildDate} />,
                },
                {
                  label: t('settings.system.replica'),
                  value: <span className="myr-mono">{data.replicaId}</span>,
                },
                {
                  label: t('settings.system.publicUrl'),
                  value: <span className="myr-mono">{data.publicUrl}</span>,
                },
                {
                  label: t('settings.system.opampEndpoint'),
                  value: (
                    <Group gap={4} wrap="nowrap">
                      <Code>{data.opampEndpoint}</Code>
                      <CopyIconButton text={data.opampEndpoint} size="xs" />
                    </Group>
                  ),
                },
                {
                  label: t('settings.system.database'),
                  value: (
                    <Group gap={8}>
                      <StatusBadge
                        tone={data.database.ok ? 'success' : 'danger'}
                        label={data.database.ok ? t('common.ok') : t('common.error')}
                      />
                      <Text fz="xs" c="dimmed">
                        {data.database.serverVersion} · {t('settings.system.migration')}{' '}
                        {data.database.migrationVersion}
                      </Text>
                    </Group>
                  ),
                },
                {
                  label: t('settings.system.licenses'),
                  value: (
                    <Group gap="md" wrap="wrap">
                      <Anchor
                        href="/licenses/LICENSE"
                        target="_blank"
                        rel="noopener noreferrer"
                        fz="sm"
                      >
                        {t('settings.system.license')}
                      </Anchor>
                      <Anchor
                        href="/licenses/THIRD_PARTY_NOTICES"
                        target="_blank"
                        rel="noopener noreferrer"
                        fz="sm"
                      >
                        {t('settings.system.thirdPartyNotices')}
                      </Anchor>
                    </Group>
                  ),
                },
              ]}
            />
          </SectionCard>
          <SectionCard title={t('settings.system.features')}>
            <Group gap={6} wrap="wrap">
              {features.map(([k, on]) => (
                <Badge
                  key={k}
                  color={on ? 'teal' : 'gray'}
                  variant={on ? 'light' : 'outline'}
                  style={{ textTransform: 'none' }}
                >
                  {t(`settings.system.feature.${k}`)}:{' '}
                  {on ? t('common.enabled') : t('common.disabled')}
                </Badge>
              ))}
            </Group>
          </SectionCard>
        </Stack>
      </Grid.Col>

      <Grid.Col span={{ base: 12, lg: 6 }}>
        <SectionCard
          title={t('settings.system.effective')}
          description={t('settings.system.effectiveHint')}
        >
          <KeyValue
            labelWidth={190}
            items={[
              {
                label: t('settings.system.gitopsSource'),
                value: <span className="myr-mono">{s.gitopsSource}</span>,
              },
              {
                label: t('gitops.branch'),
                value: <span className="myr-mono">{s.gitopsBranch}</span>,
              },
              { label: t('settings.system.gitopsPoll'), value: s.gitopsPollInterval },
              {
                label: t('settings.system.releasesRepo'),
                value: <span className="myr-mono">{s.releasesRepo}</span>,
              },
              { label: t('settings.system.releasesPoll'), value: s.releasesPollInterval },
              {
                label: t('settings.system.distributions'),
                value: s.trackedDistributions.join(', ') || '–',
              },
              {
                label: t('settings.system.artifactSource'),
                value: <span className="myr-mono">{s.artifactSource}</span>,
              },
              {
                label: t('settings.system.signatureVerification'),
                value: s.signatureVerification ? t('common.enabled') : t('common.disabled'),
              },
              { label: t('settings.system.defaultTimezone'), value: s.defaultTimezone },
              { label: t('settings.system.offlineAfter'), value: s.offlineAfter },
              { label: t('settings.system.retentionEvents'), value: s.retentionAgentEvents },
              { label: t('settings.system.retentionAudit'), value: s.retentionAudit },
            ]}
          />
        </SectionCard>
      </Grid.Col>

      <Grid.Col span={12}>
        <SectionCard
          padded={false}
          title={t('settings.system.replicas')}
          description={t('settings.system.replicasHint')}
        >
          <div className="myr-scroll-x">
            <Table fz="sm" verticalSpacing="xs">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('settings.system.replica')}</Table.Th>
                  <Table.Th>{t('settings.system.hostname')}</Table.Th>
                  <Table.Th>{t('common.version')}</Table.Th>
                  <Table.Th>{t('settings.system.startedAt')}</Table.Th>
                  <Table.Th>{t('settings.system.heartbeat')}</Table.Th>
                  <Table.Th ta="right">{t('settings.system.connections')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.replicas.map((r) => (
                  <Table.Tr key={r.id} data-testid="replica-row">
                    <Table.Td>
                      <Group gap={6} wrap="nowrap">
                        <Text className="myr-mono" fz="sm">
                          {r.id}
                        </Text>
                        {r.isLeader && (
                          <Badge size="xs" color="yellow" leftSection={<IconCrown size={10} />}>
                            {t('settings.system.leader')}
                          </Badge>
                        )}
                        {r.id === data.replicaId && (
                          <Badge size="xs" variant="outline" color="gray">
                            {t('settings.system.thisReplica')}
                          </Badge>
                        )}
                      </Group>
                    </Table.Td>
                    <Table.Td className="myr-mono">{r.hostname}</Table.Td>
                    <Table.Td className="myr-mono">{r.version}</Table.Td>
                    <Table.Td>
                      <DateTime value={r.startedAt} />
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={r.lastHeartbeatAt} />
                    </Table.Td>
                    <Table.Td ta="right" className="myr-tabular">
                      {r.connections}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
        </SectionCard>
      </Grid.Col>
    </Grid>
  );
}
