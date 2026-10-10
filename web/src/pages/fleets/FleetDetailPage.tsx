// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Grid,
  Group,
  Skeleton,
  Stack,
  Table,
  Text,
  Tooltip,
} from '@mantine/core';
import { IconArrowRight, IconGitBranch } from '@tabler/icons-react';
import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { isApiError } from '@/api/errors';
import { useFleet } from '@/api/hooks/fleets';
import { GitLink, KeyValue, SelectorView } from '@/components/misc';
import { PageHeader } from '@/components/PageHeader';
import { PolicyView } from '@/components/PolicyView';
import { RolloutProgress } from '@/components/RolloutProgress';
import { RolloutStatusBadge } from '@/components/StatusBadge';
import { SectionCard } from '@/components/SectionCard';
import { IssueList } from '@/components/IssueList';
import { ErrorState } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { VersionBadge } from '@/components/VersionBadge';
import { useLang } from '@/i18n/useLang';
import { compareVersions, formatNumber } from '@/lib/format';
import { agentsLink, blueprintLink, policyLink, releaseLink, rolloutLink } from '@/lib/links';
import { ParametersTable } from '@/components/ParametersTable';
import { FleetVersionSection } from './FleetVersionSection';

export default function FleetDetailPage() {
  const { t } = useTranslation();
  const lang = useLang();
  const { name } = useParams();
  const { data, isPending, error, refetch } = useFleet(name);
  const crumbs = [{ label: t('nav.fleets'), to: '/fleets' }, { label: name ?? '' }];

  if (isPending) {
    return (
      <>
        <PageHeader title={name ?? ''} breadcrumbs={crumbs} />
        <Stack>
          <Skeleton height={160} />
          <Skeleton height={260} />
        </Stack>
      </>
    );
  }
  if (error || !data) {
    const notFound = isApiError(error) && error.status === 404;
    return (
      <>
        <PageHeader title={name ?? ''} breadcrumbs={crumbs} />
        <ErrorState
          error={error}
          title={notFound ? t('fleets.notFound') : undefined}
          onRetry={notFound ? undefined : () => void refetch()}
        />
      </>
    );
  }

  const { fleet, status, blueprintChain, configPolicy, versionPolicy, executor, issues } = data;
  const latest = status.latestVersion;
  const targetBehind =
    !!fleet.collector.version && !!latest && compareVersions(fleet.collector.version, latest) < 0;
  const counts = status.agents;

  return (
    <>
      <PageHeader
        title={fleet.displayName || fleet.name}
        subtitle={fleet.description}
        breadcrumbs={crumbs}
        badge={
          <Group gap={6}>
            <Badge
              variant="outline"
              color="gray"
              className="myr-mono"
              style={{ textTransform: 'none' }}
            >
              {fleet.name}
            </Badge>
            {status.renderErrors > 0 && (
              <Badge color="red" variant="light">
                {t('fleets.renderErrors', { count: status.renderErrors })}
              </Badge>
            )}
          </Group>
        }
        actions={
          <>
            <Button
              component={Link}
              to={agentsLink({ fleet: [fleet.name] })}
              variant="default"
              size="xs"
            >
              {t('fleets.showMembers', { count: counts.total })}
            </Button>
            <GitLink url={data.webUrl} />
          </>
        }
      />
      <Alert variant="light" color="gray" mb="md" icon={<IconGitBranch size={16} />} py="xs">
        {t('common.readOnlyGit')}{' '}
        <Text span className="myr-mono" fz="xs">
          {fleet.source.path}
        </Text>
        {fleet.source.commit ? (
          <Text span className="myr-mono" fz="xs">
            {' '}
            @ {fleet.source.commit.slice(0, 8)}
          </Text>
        ) : null}
        {status.versionTarget.frame?.enabled && (
          <>
            {' · '}
            {t('versionUpdate.section.uiSelectable')}
          </>
        )}
      </Alert>

      <Grid gap="md">
        <Grid.Col span={12}>
          <FleetVersionSection detail={data} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 6 }}>
          <SectionCard title={t('fleets.definition')}>
            <KeyValue
              items={[
                { label: t('fleets.priority'), value: fleet.priority },
                { label: t('fleets.selector'), value: <SelectorView selector={fleet.selector} /> },
                {
                  label: t('fleets.blueprintChain'),
                  value: (
                    <Group gap={6} wrap="wrap">
                      {(blueprintChain.length ? blueprintChain : [fleet.blueprint]).map((b, i) => (
                        <Fragment key={b}>
                          {i > 0 && (
                            <IconArrowRight size={14} style={{ opacity: 0.5 }} aria-hidden />
                          )}
                          <Anchor
                            component={Link}
                            to={blueprintLink(b)}
                            fz="sm"
                            className="myr-mono"
                          >
                            {b}
                          </Anchor>
                        </Fragment>
                      ))}
                    </Group>
                  ),
                },
                {
                  label: t('fleets.gitVersion'),
                  value: (
                    <Group gap={8}>
                      <VersionBadge
                        version={fleet.collector.version}
                        outdated={targetBehind}
                        latest={latest}
                      />
                      <Text fz="xs" c="dimmed">
                        {fleet.collector.distribution ?? 'otelcol-contrib'}
                      </Text>
                      {latest && (
                        <Anchor
                          component={Link}
                          to={releaseLink(
                            fleet.collector.distribution ?? 'otelcol-contrib',
                            latest,
                          )}
                          fz="xs"
                        >
                          {t('fleets.latestIs', { latest })}
                        </Anchor>
                      )}
                    </Group>
                  ),
                },
                {
                  label: t('fleets.executor'),
                  value: (
                    <Stack gap={2}>
                      <Text fz="sm">
                        {t(`enum.executor.${fleet.update.executor ?? 'none'}`)}
                        {fleet.update.executorRef ? ` · ${fleet.update.executorRef}` : ''}
                      </Text>
                      {executor && (
                        <Text
                          className="myr-mono"
                          fz="xs"
                          c="dimmed"
                          style={{ wordBreak: 'break-all' }}
                        >
                          {executor.target}
                        </Text>
                      )}
                    </Stack>
                  ),
                },
                {
                  label: t('fleets.policies'),
                  value: (
                    <Group gap="md" wrap="wrap">
                      <Text fz="sm">
                        {t('fleets.configPolicy')}:{' '}
                        <Anchor component={Link} to={policyLink(configPolicy.name)}>
                          {configPolicy.name}
                        </Anchor>
                      </Text>
                      <Text fz="sm">
                        {t('fleets.versionPolicy')}:{' '}
                        <Anchor component={Link} to={policyLink(versionPolicy.name)}>
                          {versionPolicy.name}
                        </Anchor>
                      </Text>
                    </Group>
                  ),
                },
                {
                  label: t('common.description'),
                  value: fleet.description ?? '–',
                  hidden: !fleet.description,
                },
                {
                  label: t('fleets.labels'),
                  hidden: !fleet.labels || Object.keys(fleet.labels).length === 0,
                  value: (
                    <Group gap={6}>
                      {Object.entries(fleet.labels ?? {}).map(([k, v]) => (
                        <Badge
                          key={k}
                          variant="outline"
                          color="gray"
                          style={{ textTransform: 'none' }}
                        >
                          {k}={v}
                        </Badge>
                      ))}
                    </Group>
                  ),
                },
              ]}
            />
          </SectionCard>
        </Grid.Col>

        <Grid.Col span={{ base: 12, lg: 6 }}>
          <Stack gap="md">
            <SectionCard title={t('fleets.members')}>
              <Group gap="xl" wrap="wrap" mb="sm">
                <Stat
                  label={t('common.agents')}
                  value={formatNumber(lang, counts.total)}
                  to={agentsLink({ fleet: [fleet.name] })}
                />
                <Stat
                  label={t('common.online')}
                  value={formatNumber(lang, counts.online)}
                  to={agentsLink({ fleet: [fleet.name], status: ['online'] })}
                />
                <Stat
                  label={t('enum.configSync.in_sync')}
                  value={formatNumber(lang, counts.inSync)}
                  to={agentsLink({ fleet: [fleet.name], configSync: ['in_sync'] })}
                />
                <Stat
                  label={t('enum.configSync.pending')}
                  value={formatNumber(lang, counts.pending)}
                  // the server counts pending and drift (waiting for the rollout) as "pending"
                  to={agentsLink({ fleet: [fleet.name], configSync: ['pending', 'drift'] })}
                />
                <Stat
                  label={t('enum.configSync.failed')}
                  value={formatNumber(lang, counts.failed)}
                  to={agentsLink({ fleet: [fleet.name], configSync: ['failed'] })}
                  danger={counts.failed > 0}
                />
                <Stat
                  label={t('dashboard.fleets.onTarget')}
                  value={formatNumber(lang, counts.onTarget)}
                />
              </Group>
              <Text
                fz="xs"
                fw={700}
                tt="uppercase"
                c="dimmed"
                mb={4}
                style={{ letterSpacing: '0.05em' }}
              >
                {t('dashboard.versions.title')}
              </Text>
              <Group gap={6} wrap="wrap">
                {status.versions.map((v) => (
                  <Tooltip
                    key={v.version}
                    label={t('dashboard.versions.rowHint', { version: v.version })}
                  >
                    <Badge
                      component={Link}
                      to={agentsLink({ fleet: [fleet.name], version: [v.version] })}
                      color={v.outdated ? 'orange' : 'teal'}
                      variant="light"
                      size="lg"
                      style={{ textTransform: 'none', cursor: 'pointer' }}
                    >
                      <span className="myr-mono">{v.version}</span> · {v.count}
                    </Badge>
                  </Tooltip>
                ))}
                {status.versions.length === 0 && (
                  <Text c="dimmed" fz="sm">
                    –
                  </Text>
                )}
              </Group>
            </SectionCard>

            <SectionCard title={t('fleets.activeRolloutsTitle')} padded={false}>
              {status.activeRollouts.length === 0 ? (
                <Text c="dimmed" fz="sm" px="md" pb="md">
                  {t('fleets.noActiveRollouts')}
                </Text>
              ) : (
                <Table fz="sm" verticalSpacing="xs">
                  <Table.Tbody>
                    {status.activeRollouts.map((r) => (
                      <Table.Tr key={r.id}>
                        <Table.Td>
                          <Anchor component={Link} to={rolloutLink(r.id)} fz="sm">
                            {t(`enum.rolloutKind.${r.kind}`)} ·{' '}
                            <span className="myr-mono">
                              {r.kind === 'version' ? r.toRef : r.toRef.slice(0, 8)}
                            </span>
                          </Anchor>
                          <Text fz="xs" c="dimmed">
                            <TimeAgo value={r.updatedAt} />
                          </Text>
                        </Table.Td>
                        <Table.Td w="40%">
                          <RolloutProgress counts={r.counts} />
                        </Table.Td>
                        <Table.Td>
                          <RolloutStatusBadge status={r.status} />
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              )}
            </SectionCard>
          </Stack>
        </Grid.Col>

        <Grid.Col span={12}>
          <SectionCard title={t('fleets.parameters')} description={t('fleets.parametersHint')}>
            <ParametersTable values={fleet.parameters ?? {}} />
          </SectionCard>
        </Grid.Col>

        <Grid.Col span={12}>
          <SectionCard
            title={`${t('fleets.configPolicy')}: ${configPolicy.name}`}
            actions={
              <Anchor component={Link} to={policyLink(configPolicy.name)} fz="sm">
                {t('common.details')}
              </Anchor>
            }
          >
            <PolicyView policy={configPolicy} compact />
          </SectionCard>
        </Grid.Col>
        {versionPolicy.name !== configPolicy.name && (
          <Grid.Col span={12}>
            <SectionCard
              title={`${t('fleets.versionPolicy')}: ${versionPolicy.name}`}
              actions={
                <Anchor component={Link} to={policyLink(versionPolicy.name)} fz="sm">
                  {t('common.details')}
                </Anchor>
              }
            >
              <PolicyView policy={versionPolicy} compact />
            </SectionCard>
          </Grid.Col>
        )}

        <Grid.Col span={12}>
          <SectionCard title={t('common.issues')} description={t('fleets.issuesHint')}>
            <IssueList issues={issues} />
          </SectionCard>
        </Grid.Col>
      </Grid>
    </>
  );
}

function Stat({
  label,
  value,
  to,
  danger,
}: {
  label: string;
  value: string;
  to?: string;
  danger?: boolean;
}) {
  const inner = (
    <Stack gap={0}>
      <Text fz={22} fw={700} c={danger ? 'red' : undefined} className="myr-tabular" lh={1.2}>
        {value}
      </Text>
      <Text fz="xs" c="dimmed">
        {label}
      </Text>
    </Stack>
  );
  return to ? (
    <Anchor component={Link} to={to} c="inherit" underline="never">
      {inner}
    </Anchor>
  ) : (
    inner
  );
}
