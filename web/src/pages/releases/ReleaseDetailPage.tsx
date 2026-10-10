// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Group,
  Skeleton,
  Stack,
  Table,
  Tabs,
  Text,
  Tooltip,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconCheck,
  IconExternalLink,
  IconFileCode,
  IconPackage,
  IconSitemap,
  IconTag,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link, useParams, useSearchParams } from 'react-router';
import { isApiError } from '@/api/errors';
import { useAcknowledgeRelease, useRelease } from '@/api/hooks/releases';
import { Can } from '@/components/Can';
import { CopyTextButton } from '@/components/CopyButton';
import { KeyValue } from '@/components/misc';
import { PageHeader } from '@/components/PageHeader';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState, ErrorState } from '@/components/states';
import { DateTime } from '@/components/TimeAgo';
import { VersionBadge } from '@/components/VersionBadge';
import { YamlView } from '@/components/Yaml';
import { useLang } from '@/i18n/useLang';
import { formatBytes, formatNumber } from '@/lib/format';
import { agentsLink, fleetLink } from '@/lib/links';
import { ChangeList } from './ChangeList';
import { VersionCompare } from './VersionCompare';
import { ReleaseUpdateButton } from './ReleaseUpdateButton';
import { VersionSourceBadge } from '@/components/VersionSourceBadge';
import type { DistributionName, ReleaseFleetOption } from '@/api/types';

const TABS = ['breaking', 'deprecations', 'fleets', 'assets', 'notes', 'compare'] as const;
type TabId = (typeof TABS)[number];

export default function ReleaseDetailPage() {
  const { t } = useTranslation();
  const lang = useLang();
  const { distribution, version } = useParams();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: TabId = (TABS as readonly string[]).includes(raw ?? '') ? (raw as TabId) : 'breaking';
  const { data, isPending, error, refetch } = useRelease(distribution, version);
  const ack = useAcknowledgeRelease();
  const crumbs = [{ label: t('nav.releases'), to: '/releases' }, { label: version ?? '' }];

  if (isPending) {
    return (
      <>
        <PageHeader title={version ?? ''} breadcrumbs={crumbs} />
        <Stack>
          <Skeleton height={100} />
          <Skeleton height={240} />
        </Stack>
      </>
    );
  }
  if (error || !data) {
    const notFound = isApiError(error) && error.status === 404;
    return (
      <>
        <PageHeader title={version ?? ''} breadcrumbs={crumbs} />
        <ErrorState
          error={error}
          title={notFound ? t('releases.notFound') : undefined}
          onRetry={notFound ? undefined : () => void refetch()}
        />
      </>
    );
  }
  const rel = data.release;
  const acknowledged = !!rel.acknowledgedAt;
  const setTab = (v: string | null) =>
    setParams(v && v !== 'breaking' ? { tab: v } : {}, { replace: true });

  return (
    <>
      <PageHeader
        title={`${rel.distribution} ${rel.version}`}
        breadcrumbs={crumbs}
        subtitle={rel.tag}
        badge={
          <Group gap={6}>
            {data.isLatest && <Badge color="teal">{t('releases.latest')}</Badge>}
            {rel.prerelease && (
              <Badge color="gray" variant="outline">
                {t('releases.prerelease')}
              </Badge>
            )}
            {acknowledged ? (
              <Badge color="teal" variant="light" leftSection={<IconCheck size={11} />}>
                {t('releases.acknowledgedYes')}
              </Badge>
            ) : (
              <Badge color="yellow" variant="light">
                {t('releases.acknowledgedNo')}
              </Badge>
            )}
          </Group>
        }
        actions={
          <>
            <ReleaseUpdateButton version={rel.version} fleets={data.updatableFleets} />
            <Can permission="releases:acknowledge">
              {!acknowledged && (
                <Button
                  size="xs"
                  leftSection={<IconCheck size={14} />}
                  loading={ack.isPending}
                  onClick={() =>
                    ack.mutate({ distribution: rel.distribution, version: rel.version })
                  }
                >
                  {t('releases.acknowledge')}
                </Button>
              )}
            </Can>
            {rel.htmlUrl && (
              <Button
                component="a"
                href={rel.htmlUrl}
                target="_blank"
                rel="noopener noreferrer"
                variant="default"
                size="xs"
                rightSection={<IconExternalLink size={14} />}
              >
                GitHub
              </Button>
            )}
          </>
        }
      />

      {rel.breakingChanges.length > 0 && !acknowledged && (
        <Alert
          color="orange"
          variant="light"
          icon={<IconAlertTriangle size={18} />}
          mb="md"
          title={t('releases.breakingNotice', { count: rel.breakingChanges.length })}
        >
          {t('releases.breakingNoticeText')}
        </Alert>
      )}

      <Stack gap="md">
        <SectionCard>
          <Group gap="xl" wrap="wrap">
            <Stat
              label={t('releases.breakingChanges')}
              value={rel.breakingChanges.length}
              color={rel.breakingChanges.length > 0 ? 'orange' : undefined}
            />
            <Stat label={t('releases.deprecations')} value={rel.deprecations.length} />
            <Stat label={t('releases.fleetsBehind')} value={data.fleetsBehind.length} />
            <Stat
              label={t('releases.agentsBehind')}
              value={formatNumber(lang, data.agentsBehind)}
            />
            <KeyValue
              labelWidth={130}
              items={[
                { label: t('releases.published'), value: <DateTime value={rel.publishedAt} /> },
                { label: t('releases.detected'), value: <DateTime value={rel.detectedAt} /> },
                {
                  label: t('releases.acknowledgedBy'),
                  value: acknowledged ? (
                    <span>
                      {rel.acknowledgedBy} · <DateTime value={rel.acknowledgedAt} />
                    </span>
                  ) : (
                    '–'
                  ),
                },
              ]}
            />
          </Group>
        </SectionCard>

        <Tabs value={tab} onChange={setTab} keepMounted={false}>
          <Tabs.List mb="md" style={{ flexWrap: 'nowrap', overflowX: 'auto' }}>
            <Tabs.Tab value="breaking" leftSection={<IconAlertTriangle size={16} />}>
              {t('releases.breakingChanges')} ({rel.breakingChanges.length})
            </Tabs.Tab>
            <Tabs.Tab value="deprecations" leftSection={<IconTag size={16} />}>
              {t('releases.deprecations')} ({rel.deprecations.length})
            </Tabs.Tab>
            <Tabs.Tab value="fleets" leftSection={<IconSitemap size={16} />}>
              {t('releases.affectedFleets')} ({data.fleetsBehind.length})
            </Tabs.Tab>
            <Tabs.Tab value="assets" leftSection={<IconPackage size={16} />}>
              {t('releases.assets')} ({rel.assets.length})
            </Tabs.Tab>
            <Tabs.Tab value="notes" leftSection={<IconFileCode size={16} />}>
              {t('releases.notes')}
            </Tabs.Tab>
            <Tabs.Tab value="compare">{t('releases.compare.tab')}</Tabs.Tab>
          </Tabs.List>

          <Tabs.Panel value="breaking">
            <SectionCard>
              <ChangeList entries={rel.breakingChanges} emptyTitle={t('releases.noBreaking')} />
            </SectionCard>
          </Tabs.Panel>
          <Tabs.Panel value="deprecations">
            <SectionCard>
              <ChangeList entries={rel.deprecations} emptyTitle={t('releases.noDeprecations')} />
            </SectionCard>
          </Tabs.Panel>

          <Tabs.Panel value="fleets">
            <Stack gap="md">
              <SectionCard
                padded={false}
                title={t('releases.affectedFleets')}
                description={t('releases.affectedFleetsHint')}
              >
                {data.fleetsBehind.length === 0 ? (
                  <EmptyState compact title={t('releases.noFleetsBehind')} />
                ) : (
                  <div className="myr-scroll-x">
                    <Table fz="sm" verticalSpacing="sm">
                      <Table.Thead>
                        <Table.Tr>
                          <Table.Th>{t('common.fleet')}</Table.Th>
                          <Table.Th>{t('dashboard.fleets.targetVersion')}</Table.Th>
                          <Table.Th ta="right">{t('common.agents')}</Table.Th>
                          <Table.Th>{t('dashboard.versions.title')}</Table.Th>
                          <Table.Th ta="right">{t('releases.breakingChanges')}</Table.Th>
                          <Table.Th>{t('versionUpdate.release.uiUpdate')}</Table.Th>
                          <Table.Th>{t('common.source')}</Table.Th>
                        </Table.Tr>
                      </Table.Thead>
                      <Table.Tbody>
                        {data.fleetsBehind.map((f) => (
                          <Table.Tr key={f.fleet}>
                            <Table.Td>
                              <Anchor component={Link} to={fleetLink(f.fleet)} fw={600}>
                                {f.fleet}
                              </Anchor>
                            </Table.Td>
                            <Table.Td>
                              <VersionBadge version={f.targetVersion} />
                            </Table.Td>
                            <Table.Td ta="right">
                              <Anchor
                                component={Link}
                                to={agentsLink({ fleet: [f.fleet] })}
                                c="inherit"
                              >
                                {formatNumber(lang, f.agents)}
                              </Anchor>
                            </Table.Td>
                            <Table.Td>
                              <Group gap={4}>
                                {f.versions.map((v) => (
                                  <Badge
                                    key={v.version}
                                    size="sm"
                                    variant="light"
                                    color={v.outdated ? 'orange' : 'teal'}
                                    style={{ textTransform: 'none' }}
                                  >
                                    <span className="myr-mono">{v.version}</span> · {v.count}
                                  </Badge>
                                ))}
                              </Group>
                            </Table.Td>
                            <Table.Td ta="right">{f.breakingCount}</Table.Td>
                            <Table.Td>
                              <UiUpdateCell
                                option={data.updatableFleets.find((u) => u.fleet === f.fleet)}
                              />
                            </Table.Td>
                            <Table.Td className="myr-mono" fz="xs">
                              {f.sourcePath}
                            </Table.Td>
                          </Table.Tr>
                        ))}
                      </Table.Tbody>
                    </Table>
                  </div>
                )}
              </SectionCard>
              <SectionCard
                title={t('releases.gitopsSnippet')}
                description={t('releases.gitopsSnippetHint')}
                actions={
                  data.gitopsSnippet ? (
                    <CopyTextButton text={data.gitopsSnippet} label={t('releases.copySnippet')} />
                  ) : undefined
                }
              >
                {data.gitopsSnippet ? (
                  <YamlView value={data.gitopsSnippet} maxHeight="240px" />
                ) : (
                  <Text c="dimmed" fz="sm">
                    –
                  </Text>
                )}
              </SectionCard>
            </Stack>
          </Tabs.Panel>

          <Tabs.Panel value="assets">
            <SectionCard padded={false}>
              {rel.assets.length === 0 ? (
                <EmptyState compact title={t('releases.noAssets')} />
              ) : (
                <div className="myr-scroll-x">
                  <Table fz="sm" verticalSpacing="xs">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>{t('common.name')}</Table.Th>
                        <Table.Th>OS</Table.Th>
                        <Table.Th>{t('releases.arch')}</Table.Th>
                        <Table.Th>{t('releases.format')}</Table.Th>
                        <Table.Th ta="right">{t('releases.size')}</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {rel.assets.map((a) => (
                        <Table.Tr key={a.name}>
                          <Table.Td>
                            <Anchor
                              href={a.url}
                              target="_blank"
                              rel="noopener noreferrer"
                              className="myr-mono"
                              fz="sm"
                              style={{ wordBreak: 'break-all' }}
                            >
                              {a.name}
                            </Anchor>
                          </Table.Td>
                          <Table.Td>{a.os ?? '–'}</Table.Td>
                          <Table.Td>{a.arch ?? '–'}</Table.Td>
                          <Table.Td>{a.format ?? '–'}</Table.Td>
                          <Table.Td ta="right" className="myr-tabular">
                            {formatBytes(lang, a.size)}
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </div>
              )}
            </SectionCard>
          </Tabs.Panel>

          <Tabs.Panel value="notes">
            <SectionCard>
              {rel.notes ? (
                <YamlView value={rel.notes} language="plain" maxHeight="640px" />
              ) : (
                <EmptyState compact title={t('releases.noNotes')} />
              )}
            </SectionCard>
          </Tabs.Panel>

          <Tabs.Panel value="compare">
            <VersionCompare
              distribution={rel.distribution as DistributionName}
              initialFrom={params.get('from') ?? undefined}
              initialTo={rel.version}
            />
          </Tabs.Panel>
        </Tabs>
      </Stack>
    </>
  );
}

/** "UI update possible?" of one fleet row: yes, or no with the translated reason in the tooltip. */
function UiUpdateCell({ option }: { option: ReleaseFleetOption | undefined }) {
  const { t } = useTranslation();
  if (!option) return <Text c="dimmed">–</Text>;
  if (option.selectable)
    return (
      <Badge color="teal" variant="light" data-testid="ui-update-yes">
        {t('common.yes')}
      </Badge>
    );
  return (
    <Tooltip label={option.reasonCode ? t(`enum.versionReason.${option.reasonCode}`) : ''}>
      <Group gap={4} wrap="nowrap">
        <Badge color="gray" variant="outline" data-testid="ui-update-no">
          {t('common.no')}
        </Badge>
        <VersionSourceBadge source={option.target.source} size="xs" short />
      </Group>
    </Tooltip>
  );
}

function Stat({ label, value, color }: { label: string; value: number | string; color?: string }) {
  return (
    <Stack gap={0}>
      <Text fz={26} fw={700} c={color} className="myr-tabular" lh={1.2}>
        {value}
      </Text>
      <Text fz="xs" c="dimmed">
        {label}
      </Text>
    </Stack>
  );
}
