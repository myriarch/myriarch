// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  SegmentedControl,
  Stack,
  Switch,
  Table,
  Text,
  Tooltip,
  VisuallyHidden,
} from '@mantine/core';
import { IconCheck, IconExternalLink, IconRefresh, IconTag } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { DISTRIBUTIONS } from '@/api/enums';
import { useAcknowledgeRelease, useRefreshReleases, useReleases } from '@/api/hooks/releases';
import type { DistributionName } from '@/api/types';
import { Can } from '@/components/Can';
import { PageHeader } from '@/components/PageHeader';
import { Pager } from '@/components/Pager';
import { StatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { DateTime } from '@/components/TimeAgo';
import { releaseLink } from '@/lib/links';
import { VersionCompare } from './VersionCompare';

const SIZES = [25, 50, 100] as const;

export default function ReleasesPage() {
  const { t } = useTranslation();
  const [sp, setSp] = useSearchParams();
  const rawDist = sp.get('distribution');
  const distribution: DistributionName = (DISTRIBUTIONS as readonly string[]).includes(
    rawDist ?? '',
  )
    ? (rawDist as DistributionName)
    : 'otelcol-contrib';
  const [includePre, setIncludePre] = useState(false);
  const [page, setPage] = useState<{ limit: number; offset: number }>({ limit: 25, offset: 0 });
  const q = useReleases({ distribution, includePrerelease: includePre, ...page });
  const refresh = useRefreshReleases();
  const ack = useAcknowledgeRelease();

  const setDistribution = (d: string) => {
    setSp(d === 'otelcol-contrib' ? {} : { distribution: d }, { replace: true });
    setPage((p) => ({ ...p, offset: 0 }));
  };

  let body;
  if (q.isPending) body = <TableSkeleton cols={6} rows={8} />;
  else if (q.error && !q.data)
    body = <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  else if (q.data && q.data.total === 0)
    body = (
      <EmptyState
        icon={<IconTag size={28} />}
        title={t('releases.empty')}
        text={t('releases.emptyHint')}
      />
    );
  else if (q.data)
    body = (
      <div className="myr-scroll-x" style={{ opacity: q.isPlaceholderData ? 0.6 : 1 }}>
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.version')}</Table.Th>
              <Table.Th>{t('releases.published')}</Table.Th>
              <Table.Th ta="right">{t('releases.breakingChanges')}</Table.Th>
              <Table.Th ta="right">{t('releases.deprecations')}</Table.Th>
              <Table.Th ta="right">{t('releases.fleetsBehind')}</Table.Th>
              <Table.Th>{t('releases.acknowledged')}</Table.Th>
              <Table.Th>
                <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
              </Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.items.map((r) => (
              <Table.Tr key={r.version} data-testid="release-row">
                <Table.Td>
                  <Group gap={8} wrap="nowrap">
                    <Anchor
                      component={Link}
                      to={releaseLink(r.distribution, r.version)}
                      fw={700}
                      className="myr-mono"
                    >
                      {r.version}
                    </Anchor>
                    {r.isLatest && (
                      <Badge size="xs" color="teal">
                        {t('releases.latest')}
                      </Badge>
                    )}
                    {r.prerelease && (
                      <Badge size="xs" color="gray" variant="outline">
                        {t('releases.prerelease')}
                      </Badge>
                    )}
                  </Group>
                </Table.Td>
                <Table.Td>
                  <DateTime value={r.publishedAt} />
                </Table.Td>
                <Table.Td ta="right">
                  {r.breakingCount > 0 ? (
                    <Badge color="orange" variant="light">
                      {r.breakingCount}
                    </Badge>
                  ) : (
                    <Text c="dimmed" span>
                      0
                    </Text>
                  )}
                </Table.Td>
                <Table.Td ta="right" className="myr-tabular">
                  {r.deprecationCount}
                </Table.Td>
                <Table.Td ta="right" className="myr-tabular">
                  {r.fleetsBehind > 0 ? (
                    <Badge color="yellow" variant="light">
                      {r.fleetsBehind}
                    </Badge>
                  ) : (
                    <Text c="dimmed" span>
                      0
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  {r.acknowledged ? (
                    <StatusBadge tone="success" label={t('releases.acknowledgedYes')} />
                  ) : r.breakingCount > 0 || r.isLatest ? (
                    <StatusBadge tone="warning" label={t('releases.acknowledgedNo')} />
                  ) : (
                    <Text c="dimmed" span>
                      –
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>
                  <Group gap={4} justify="flex-end" wrap="nowrap">
                    <Can permission="releases:acknowledge">
                      {!r.acknowledged && (
                        <Tooltip label={t('releases.acknowledge')}>
                          <Button
                            size="compact-xs"
                            variant="light"
                            leftSection={<IconCheck size={12} />}
                            loading={ack.isPending && ack.variables?.version === r.version}
                            onClick={() =>
                              ack.mutate({ distribution: r.distribution, version: r.version })
                            }
                          >
                            {t('releases.acknowledge')}
                          </Button>
                        </Tooltip>
                      )}
                    </Can>
                    {r.htmlUrl && (
                      <Anchor
                        href={r.htmlUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        aria-label="GitHub"
                      >
                        <IconExternalLink size={15} />
                      </Anchor>
                    )}
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
    );

  return (
    <>
      <PageHeader
        title={t('releases.title')}
        subtitle={t('releases.subtitle')}
        actions={
          <Can permission="releases:acknowledge">
            <Button
              variant="default"
              size="xs"
              leftSection={<IconRefresh size={14} />}
              loading={refresh.isPending}
              onClick={() => refresh.mutate()}
            >
              {t('releases.refresh')}
            </Button>
          </Can>
        }
      />
      <Stack gap="md">
        <Card padding="md">
          <Group justify="space-between" wrap="wrap" gap="md">
            <SegmentedControl
              value={distribution}
              onChange={setDistribution}
              data={DISTRIBUTIONS.map((d) => ({ value: d, label: d }))}
              aria-label={t('releases.distribution')}
            />
            <Switch
              label={t('releases.includePrerelease')}
              checked={includePre}
              onChange={(e) => {
                setIncludePre(e.currentTarget.checked);
                setPage((p) => ({ ...p, offset: 0 }));
              }}
            />
          </Group>
        </Card>
        <Card padding={0}>
          {body}
          {q.data && q.data.total > 0 && (
            <Pager
              total={q.data.total}
              limit={page.limit}
              offset={page.offset}
              pageSizes={SIZES}
              onChange={setPage}
              loading={q.isFetching}
            />
          )}
        </Card>
        <VersionCompare distribution={distribution} key={distribution} />
      </Stack>
    </>
  );
}
