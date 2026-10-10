// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  MultiSelect,
  SegmentedControl,
  Select,
  Stack,
  Switch,
  Table,
  Text,
} from '@mantine/core';
import { IconRocket, IconX } from '@tabler/icons-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { ROLLOUT_KINDS, ROLLOUT_STATUSES } from '@/api/enums';
import { useFleets } from '@/api/hooks/fleets';
import { useRollouts, type RolloutListQuery } from '@/api/hooks/rollouts';
import type { RolloutKind, RolloutStatus } from '@/api/types';
import { PageHeader } from '@/components/PageHeader';
import { Pager } from '@/components/Pager';
import { RolloutProgress } from '@/components/RolloutProgress';
import { RolloutOriginBadge } from '@/components/VersionSourceBadge';
import { RolloutStatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { rolloutLink } from '@/lib/links';
import { parseRolloutSearch, SIZES, toParams, type State } from './rolloutSearch';

export default function RolloutsPage() {
  const { t } = useTranslation();
  const [sp, setSp] = useSearchParams();
  const state = useMemo(() => parseRolloutSearch(sp), [sp]);
  const fleets = useFleets();
  const query: RolloutListQuery = {
    fleet: state.fleet,
    kind: state.kind,
    status: state.status.length ? state.status : undefined,
    active: state.active || undefined,
    limit: state.limit,
    offset: state.offset,
  };
  const { data, isPending, error, refetch, isPlaceholderData } = useRollouts(query, {
    refetchInterval: 30_000,
  });
  const update = (patch: Partial<State>) => {
    const next = { ...state, ...patch };
    if (!('offset' in patch)) next.offset = 0;
    setSp(toParams(next));
  };
  const hasFilters = !!state.fleet || !!state.kind || state.status.length > 0 || state.active;

  let body;
  if (isPending) body = <TableSkeleton cols={7} rows={8} />;
  else if (error && !data) body = <ErrorState error={error} onRetry={() => void refetch()} />;
  else if (data && data.total === 0)
    body = hasFilters ? (
      <EmptyState
        title={t('rollouts.empty.filtered')}
        action={
          <Button
            variant="light"
            size="xs"
            leftSection={<IconX size={14} />}
            onClick={() => setSp({})}
          >
            {t('common.reset')}
          </Button>
        }
      />
    ) : (
      <EmptyState
        icon={<IconRocket size={28} />}
        title={t('rollouts.empty.none')}
        text={t('rollouts.empty.noneHint')}
      />
    );
  else if (data)
    body = (
      <div className="myr-scroll-x" style={{ opacity: isPlaceholderData ? 0.6 : 1 }}>
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.fleet')}</Table.Th>
              <Table.Th>{t('common.type')}</Table.Th>
              <Table.Th>{t('rollouts.change')}</Table.Th>
              <Table.Th>{t('common.status')}</Table.Th>
              <Table.Th miw={200}>{t('rollouts.progress')}</Table.Th>
              <Table.Th>{t('rollouts.wave')}</Table.Th>
              <Table.Th>{t('common.created')}</Table.Th>
              <Table.Th>{t('common.updated')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {data.items.map((r) => (
              <Table.Tr key={r.id} data-testid="rollout-row">
                <Table.Td>
                  <Anchor component={Link} to={rolloutLink(r.id)} fw={600} fz="sm">
                    {r.fleetName}
                  </Anchor>
                  <Text fz="xs" c="dimmed" className="myr-mono">
                    {r.id.slice(0, 8)}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <Stack gap={4} align="flex-start">
                    <Badge variant="outline" color="gray">
                      {t(`enum.rolloutKind.${r.kind}`)}
                    </Badge>
                    <RolloutOriginBadge origin={r.origin} short />
                  </Stack>
                </Table.Td>
                <Table.Td className="myr-mono">
                  {r.kind === 'version'
                    ? `${r.fromRef ?? '?'} → ${r.toRef}`
                    : `${(r.fromRef ?? '').slice(0, 7) || '?'} → ${r.toRef.slice(0, 7)}`}
                </Table.Td>
                <Table.Td>
                  <Stack gap={2}>
                    <RolloutStatusBadge status={r.status} />
                    {r.statusReason && (
                      <Text fz="xs" c="dimmed" maw={220} lineClamp={2}>
                        {r.statusReason}
                      </Text>
                    )}
                  </Stack>
                </Table.Td>
                <Table.Td>
                  <RolloutProgress counts={r.counts} />
                </Table.Td>
                <Table.Td className="myr-tabular">
                  {Math.min(r.currentWave + 1, r.waveCount)} / {r.waveCount}
                </Table.Td>
                <Table.Td>
                  <TimeAgo value={r.createdAt} />
                </Table.Td>
                <Table.Td>
                  <TimeAgo value={r.updatedAt} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
    );

  return (
    <>
      <PageHeader title={t('rollouts.title')} subtitle={t('rollouts.subtitle')} />
      <Card padding="md" mb="md">
        <Group gap="md" align="flex-end" wrap="wrap">
          <Select
            label={t('common.fleet')}
            size="xs"
            w={190}
            clearable
            placeholder={t('common.all')}
            data={(fleets.data?.items ?? []).map((f) => f.fleet.name)}
            value={state.fleet ?? null}
            onChange={(v) => update({ fleet: v ?? undefined })}
          />
          <Stack gap={4}>
            <Text fz="xs" fw={500}>
              {t('common.type')}
            </Text>
            <SegmentedControl
              size="xs"
              value={state.kind ?? 'all'}
              onChange={(v) => update({ kind: v === 'all' ? undefined : (v as RolloutKind) })}
              data={[
                { value: 'all', label: t('common.all') },
                ...ROLLOUT_KINDS.map((k) => ({ value: k, label: t(`enum.rolloutKind.${k}`) })),
              ]}
            />
          </Stack>
          <MultiSelect
            label={t('common.status')}
            size="xs"
            w={260}
            clearable
            placeholder={t('common.all')}
            data={ROLLOUT_STATUSES.map((s) => ({ value: s, label: t(`enum.rolloutStatus.${s}`) }))}
            value={state.status}
            onChange={(v) => update({ status: v as RolloutStatus[] })}
          />
          <Switch
            label={t('rollouts.onlyActive')}
            checked={state.active}
            onChange={(e) => update({ active: e.currentTarget.checked })}
            mb={4}
          />
          {hasFilters && (
            <Button
              variant="subtle"
              size="xs"
              onClick={() => setSp({})}
              leftSection={<IconX size={14} />}
            >
              {t('common.reset')}
            </Button>
          )}
        </Group>
      </Card>
      <Card padding={0}>
        {body}
        {data && data.total > 0 && (
          <Pager
            total={data.total}
            limit={state.limit}
            offset={state.offset}
            pageSizes={SIZES}
            onChange={(p) => update(p)}
            loading={isPlaceholderData}
          />
        )}
      </Card>
    </>
  );
}
