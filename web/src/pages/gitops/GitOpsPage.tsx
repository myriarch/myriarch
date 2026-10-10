// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Code,
  Collapse,
  Grid,
  Group,
  MultiSelect,
  SimpleGrid,
  Skeleton,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  UnstyledButton,
  VisuallyHidden,
} from '@mantine/core';
import {
  IconChevronDown,
  IconChevronRight,
  IconExternalLink,
  IconGitBranch,
  IconGitCommit,
  IconHistory,
  IconListDetails,
  IconRefresh,
  IconSearch,
} from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { OBJECT_KINDS } from '@/api/enums';
import {
  useGitOpsObjects,
  useGitOpsStatus,
  useGitOpsSyncs,
  useTriggerGitOpsSync,
} from '@/api/hooks/gitops';
import type { GitCommit, GitOpsSync, ObjectKind } from '@/api/types';
import { Can } from '@/components/Can';
import { IssueList } from '@/components/IssueList';
import { KeyValue } from '@/components/misc';
import { PageHeader } from '@/components/PageHeader';
import { Pager } from '@/components/Pager';
import { SectionCard } from '@/components/SectionCard';
import { StatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';
import { blueprintLink, fleetLink, policyLink } from '@/lib/links';
import type { Tone } from '@/theme/status';

const stateTone: Record<string, Tone> = {
  ok: 'success',
  error: 'danger',
  syncing: 'info',
  never: 'neutral',
};
const resultTone: Record<GitOpsSync['result'], Tone> = {
  applied: 'success',
  unchanged: 'neutral',
  invalid: 'danger',
  error: 'danger',
};

function Commit({ commit }: { commit: GitCommit | undefined }) {
  const { t } = useTranslation();
  if (!commit) return <Text c="dimmed">–</Text>;
  return (
    <Stack gap={0}>
      <Group gap={6} wrap="nowrap">
        <IconGitCommit size={14} />
        <Code>{commit.sha.slice(0, 10)}</Code>
        <Text fz="xs" c="dimmed">
          <TimeAgo value={commit.time} />
        </Text>
      </Group>
      {commit.message && (
        <Text fz="xs" lineClamp={2}>
          {commit.message}
        </Text>
      )}
      {commit.author && (
        <Text fz="xs" c="dimmed">
          {t('gitops.by', { author: commit.author })}
        </Text>
      )}
    </Stack>
  );
}

function StatusTab() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useGitOpsStatus();
  const sync = useTriggerGitOpsSync();
  if (isPending) return <Skeleton height={260} />;
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  const behind =
    !!data.latestCommit && !!data.appliedCommit && data.latestCommit.sha !== data.appliedCommit.sha;
  const errors = data.issues.filter((i) => i.severity === 'error').length;
  const warnings = data.issues.length - errors;

  return (
    <Stack gap="md">
      {data.state === 'error' && (
        <Alert color="red" variant="light" title={t('gitops.errorTitle')}>
          {t('gitops.errorText', { count: errors })}
        </Alert>
      )}
      {data.state === 'never' && (
        <Alert color="gray" variant="light">
          {t('gitops.neverText')}
        </Alert>
      )}
      <Grid gap="md">
        <Grid.Col span={{ base: 12, lg: 7 }}>
          <SectionCard
            title={t('gitops.status')}
            actions={
              <Can permission="gitops:sync">
                <Button
                  size="xs"
                  leftSection={<IconRefresh size={14} />}
                  loading={sync.isPending || data.state === 'syncing'}
                  onClick={() => sync.mutate()}
                >
                  {t('gitops.syncNow')}
                </Button>
              </Can>
            }
          >
            <KeyValue
              labelWidth={150}
              items={[
                {
                  label: t('common.status'),
                  value: (
                    <StatusBadge
                      tone={stateTone[data.state] ?? 'neutral'}
                      label={t(`enum.gitopsState.${data.state}`)}
                    />
                  ),
                },
                { label: t('gitops.source'), value: <Code>{data.source}</Code> },
                {
                  label: t('gitops.branch'),
                  value: data.branch ? <Code>{data.branch}</Code> : '–',
                },
                {
                  label: t('common.path'),
                  value: data.path ? <Code>{data.path}</Code> : '–',
                  hidden: !data.path,
                },
                { label: t('gitops.appliedCommit'), value: <Commit commit={data.appliedCommit} /> },
                {
                  label: t('gitops.latestCommit'),
                  value: (
                    <Stack gap={4}>
                      <Commit commit={data.latestCommit} />
                      {behind && (
                        <Badge color="yellow" variant="light" w="fit-content">
                          {t('gitops.behind')}
                        </Badge>
                      )}
                      {!behind && data.latestCommit && data.appliedCommit && (
                        <Badge color="teal" variant="light" w="fit-content">
                          {t('gitops.upToDate')}
                        </Badge>
                      )}
                    </Stack>
                  ),
                },
                {
                  label: t('gitops.lastSync'),
                  value: <DateTime value={data.lastSyncAt} seconds />,
                },
                {
                  label: t('gitops.lastSuccess'),
                  value: <DateTime value={data.lastSuccessAt} seconds />,
                },
              ]}
            />
          </SectionCard>
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 5 }}>
          <SectionCard title={t('gitops.objects')} description={t('gitops.objectsHint')}>
            <SimpleGrid cols={{ base: 2, xs: 3 }} spacing="sm">
              {OBJECT_KINDS.map((k) => (
                <Stack
                  key={k}
                  gap={0}
                  p="sm"
                  style={{
                    border: '1px solid var(--myr-border)',
                    borderRadius: 'var(--mantine-radius-md)',
                  }}
                >
                  <Text fz={24} fw={700} className="myr-tabular" lh={1.2}>
                    {data.objectCounts[k] ?? 0}
                  </Text>
                  <Text fz="xs" c="dimmed">
                    {t(`enum.objectKind.${k}`)}
                  </Text>
                </Stack>
              ))}
            </SimpleGrid>
          </SectionCard>
        </Grid.Col>
      </Grid>
      <SectionCard
        title={t('common.issues')}
        description={t('gitops.issuesHint')}
        actions={
          data.issues.length > 0 ? (
            <Group gap={6}>
              {errors > 0 && (
                <Badge color="red" variant="light">
                  {t('gitops.errorsCount', { count: errors })}
                </Badge>
              )}
              {warnings > 0 && (
                <Badge color="yellow" variant="light">
                  {t('gitops.warningsCount', { count: warnings })}
                </Badge>
              )}
            </Group>
          ) : undefined
        }
      >
        <IssueList issues={data.issues} emptyText={t('gitops.noIssues')} />
      </SectionCard>
    </Stack>
  );
}

function SyncRow({ s }: { s: GitOpsSync }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const changes = (s.added?.length ?? 0) + (s.changed?.length ?? 0) + (s.removed?.length ?? 0);
  const detail = changes > 0 || s.issues.length > 0;
  return (
    <>
      <Table.Tr>
        <Table.Td w={32}>
          {detail && (
            <UnstyledButton
              onClick={() => setOpen((o) => !o)}
              aria-label={open ? t('common.collapse') : t('common.expand')}
              aria-expanded={open}
            >
              {open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
            </UnstyledButton>
          )}
        </Table.Td>
        <Table.Td>
          <DateTime value={s.startedAt} seconds />
        </Table.Td>
        <Table.Td>{t(`enum.syncTrigger.${s.trigger}`)}</Table.Td>
        <Table.Td>
          <StatusBadge tone={resultTone[s.result]} label={t(`enum.syncResult.${s.result}`)} />
        </Table.Td>
        <Table.Td>{s.commit ? <Code>{s.commit.sha.slice(0, 10)}</Code> : '–'}</Table.Td>
        <Table.Td>
          <Group gap={4}>
            {(s.added?.length ?? 0) > 0 && (
              <Badge size="xs" color="teal" variant="light">
                +{s.added!.length}
              </Badge>
            )}
            {(s.changed?.length ?? 0) > 0 && (
              <Badge size="xs" color="blue" variant="light">
                ~{s.changed!.length}
              </Badge>
            )}
            {(s.removed?.length ?? 0) > 0 && (
              <Badge size="xs" color="red" variant="light">
                −{s.removed!.length}
              </Badge>
            )}
            {changes === 0 && (
              <Text c="dimmed" fz="sm">
                –
              </Text>
            )}
          </Group>
        </Table.Td>
        <Table.Td>
          {s.issues.length > 0 ? (
            <Badge size="sm" color="red" variant="light">
              {s.issues.length}
            </Badge>
          ) : (
            <Text c="dimmed" fz="sm">
              0
            </Text>
          )}
        </Table.Td>
      </Table.Tr>
      {detail && (
        <Table.Tr style={{ background: 'transparent' }}>
          <Table.Td colSpan={7} p={0} style={{ border: open ? undefined : 'none' }}>
            <Collapse expanded={open}>
              <Stack gap="sm" p="md">
                {[
                  ['added', s.added, 'teal'],
                  ['changed', s.changed, 'blue'],
                  ['removed', s.removed, 'red'],
                ].map(([k, list, color]) =>
                  (list as string[] | undefined)?.length ? (
                    <Group key={k as string} gap={6} align="flex-start" wrap="wrap">
                      <Badge color={color as string} variant="light" size="sm">
                        {t(`gitops.${k as 'added'}`)}
                      </Badge>
                      {(list as string[]).map((p) => (
                        <Code key={p}>{p}</Code>
                      ))}
                    </Group>
                  ) : null,
                )}
                {s.issues.length > 0 && <IssueList issues={s.issues} compact />}
              </Stack>
            </Collapse>
          </Table.Td>
        </Table.Tr>
      )}
    </>
  );
}

function HistoryTab() {
  const { t } = useTranslation();
  const [page, setPage] = useState({ limit: 25, offset: 0 });
  const q = useGitOpsSyncs(page);
  if (q.isPending)
    return (
      <SectionCard padded={false}>
        <TableSkeleton cols={6} rows={6} />
      </SectionCard>
    );
  if (q.error && !q.data) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data || q.data.total === 0) {
    return (
      <SectionCard>
        <EmptyState icon={<IconHistory size={28} />} title={t('gitops.noHistory')} />
      </SectionCard>
    );
  }
  return (
    <SectionCard padded={false}>
      <div className="myr-scroll-x">
        <Table fz="sm" verticalSpacing="xs">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>
                <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
              </Table.Th>
              <Table.Th>{t('gitops.startedAt')}</Table.Th>
              <Table.Th>{t('gitops.trigger')}</Table.Th>
              <Table.Th>{t('gitops.result')}</Table.Th>
              <Table.Th>{t('gitops.commit')}</Table.Th>
              <Table.Th>{t('gitops.changes')}</Table.Th>
              <Table.Th>{t('common.issues')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.items.map((s) => (
              <SyncRow key={s.id} s={s} />
            ))}
          </Table.Tbody>
        </Table>
      </div>
      <Pager
        total={q.data.total}
        limit={page.limit}
        offset={page.offset}
        pageSizes={[25, 50, 100]}
        onChange={setPage}
        loading={q.isFetching}
      />
    </SectionCard>
  );
}

function internalLink(kind: ObjectKind, name: string): string | undefined {
  switch (kind) {
    case 'Fleet':
      return fleetLink(name);
    case 'Blueprint':
      return blueprintLink(name);
    case 'RolloutPolicy':
      return policyLink(name);
    case 'Executor':
      return '/fleets?tab=executors';
    default:
      return undefined;
  }
}

function ObjectsTab() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useGitOpsObjects();
  const [kinds, setKinds] = useState<ObjectKind[]>([]);
  const [q, setQ] = useState('');
  const items = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data?.items ?? []).filter(
      (o) =>
        (kinds.length === 0 || kinds.includes(o.kind)) &&
        (!needle || `${o.name} ${o.path}`.toLowerCase().includes(needle)),
    );
  }, [data, kinds, q]);

  if (isPending)
    return (
      <SectionCard padded={false}>
        <TableSkeleton cols={4} rows={6} />
      </SectionCard>
    );
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  return (
    <SectionCard
      padded={false}
      actions={
        <Group gap="xs">
          <MultiSelect
            size="xs"
            w={240}
            clearable
            placeholder={t('gitops.allKinds')}
            data={OBJECT_KINDS.map((k) => ({ value: k, label: t(`enum.objectKind.${k}`) }))}
            value={kinds}
            onChange={(v) => setKinds(v as ObjectKind[])}
            aria-label={t('common.type')}
          />
          <TextInput
            size="xs"
            w={200}
            leftSection={<IconSearch size={14} />}
            placeholder={t('common.search')}
            aria-label={t('common.search')}
            value={q}
            onChange={(e) => setQ(e.currentTarget.value)}
          />
        </Group>
      }
    >
      {items.length === 0 ? (
        <EmptyState compact icon={<IconListDetails size={22} />} title={t('gitops.noObjects')} />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.type')}</Table.Th>
                <Table.Th>{t('common.name')}</Table.Th>
                <Table.Th>{t('common.file')}</Table.Th>
                <Table.Th>{t('gitops.commit')}</Table.Th>
                <Table.Th>
                  <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
                </Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {items.map((o) => {
                const link = internalLink(o.kind, o.name);
                return (
                  <Table.Tr key={`${o.kind}/${o.name}`}>
                    <Table.Td>
                      <Badge variant="light" color="gray">
                        {t(`enum.objectKind.${o.kind}`)}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      {link ? (
                        <Anchor component={Link} to={link} fw={600}>
                          {o.name}
                        </Anchor>
                      ) : (
                        <Text fw={600} fz="sm">
                          {o.name}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td className="myr-mono" fz="xs">
                      {o.path}
                    </Table.Td>
                    <Table.Td>
                      <Code>{o.commit.slice(0, 10)}</Code>
                    </Table.Td>
                    <Table.Td ta="right">
                      {o.webUrl && (
                        <Anchor href={o.webUrl} target="_blank" rel="noopener noreferrer" fz="sm">
                          <Group gap={4} wrap="nowrap" component="span">
                            <IconGitBranch size={13} />
                            Git
                            <IconExternalLink size={12} />
                          </Group>
                        </Anchor>
                      )}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </div>
      )}
    </SectionCard>
  );
}

const TABS = ['status', 'history', 'objects'] as const;

export default function GitOpsPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab = (TABS as readonly string[]).includes(raw ?? '')
    ? (raw as (typeof TABS)[number])
    : 'status';
  return (
    <>
      <PageHeader title={t('gitops.title')} subtitle={t('gitops.subtitle')} />
      <Tabs
        value={tab}
        onChange={(v) => setParams(v && v !== 'status' ? { tab: v } : {}, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="status" leftSection={<IconGitBranch size={16} />}>
            {t('gitops.tabs.status')}
          </Tabs.Tab>
          <Tabs.Tab value="history" leftSection={<IconHistory size={16} />}>
            {t('gitops.tabs.history')}
          </Tabs.Tab>
          <Tabs.Tab value="objects" leftSection={<IconListDetails size={16} />}>
            {t('gitops.tabs.objects')}
          </Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="status">
          <StatusTab />
        </Tabs.Panel>
        <Tabs.Panel value="history">
          <HistoryTab />
        </Tabs.Panel>
        <Tabs.Panel value="objects">
          <ObjectsTab />
        </Tabs.Panel>
      </Tabs>
    </>
  );
}
