// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Anchor,
  Drawer,
  Group,
  MultiSelect,
  Paper,
  Select,
  Stack,
  Table,
  Text,
  Tooltip,
} from '@mantine/core';
import { IconListDetails } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { TARGET_STATUSES } from '@/api/enums';
import { useRolloutTargets } from '@/api/hooks/rollouts';
import type { RolloutTarget, RolloutWave, TargetStatus } from '@/api/types';
import { HealthWaitCountdown } from '@/components/HealthWaitCountdown';
import { Pager } from '@/components/Pager';
import { TargetProgressView } from '@/components/TargetProgressView';
import { UpdateStepsBar } from '@/components/UpdateStepsBar';
import { hasProgress } from '@/lib/updates';
import { TargetStatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { agentLink } from '@/lib/links';

export function TargetsTable({
  rolloutId,
  waves,
  wave,
  onWaveChange,
  active,
}: {
  rolloutId: string;
  waves: RolloutWave[];
  wave: number | undefined;
  onWaveChange: (w: number | undefined) => void;
  active: boolean;
}) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<TargetStatus[]>([]);
  const [sort, setSort] = useState<'activity' | 'wave'>('activity');
  const [detailAgent, setDetailAgent] = useState<string | null>(null);
  // The last seen row of the drawer target. It bridges the gap until the drawer's own query has
  // answered: the drawer must not close or flicker when its row leaves the page.
  const [detailSnapshot, setDetailSnapshot] = useState<RolloutTarget | null>(null);
  const [page, setPage] = useState({ limit: 50, offset: 0 });
  // The wave can also change from outside ("show targets" on a wave card): a stale offset would
  // point behind the last row of the smaller result set.
  const [seenWave, setSeenWave] = useState(wave);
  if (seenWave !== wave) {
    setSeenWave(wave);
    setPage((p) => ({ ...p, offset: 0 }));
  }
  const q = useRolloutTargets(
    rolloutId,
    {
      wave,
      status: status.length ? status : undefined,
      sort,
      limit: page.limit,
      offset: page.offset,
    },
    { refetchInterval: active ? 5_000 : false },
  );
  const data = q.data;
  const showFlow = !!data && hasProgress(data.items);
  // A small page (e.g. a single gateway) shows the timelines right away as cards.
  const showCards = !!data && showFlow && data.items.length <= 3;
  // With the activity order a row moves to another page when its status changes (a finished
  // target drops behind all pending ones). The open drawer therefore polls its target by agent ID
  // and stays current wherever the row is.
  const detailQ = useRolloutTargets(
    rolloutId,
    { agent: detailAgent ?? undefined, limit: 1 },
    { refetchInterval: active ? 5_000 : false, enabled: detailAgent !== null },
  );
  const polled = detailQ.data?.items.find((x) => x.agentId === detailAgent);
  const onPage = data?.items.find((x) => x.agentId === detailAgent);
  const fresh = polled ?? onPage;
  if (fresh && fresh !== detailSnapshot) setDetailSnapshot(fresh);
  const detailTarget =
    detailAgent !== null
      ? (fresh ?? (detailSnapshot?.agentId === detailAgent ? detailSnapshot : undefined))
      : undefined;

  return (
    <>
      <Group gap="md" px="md" pb="sm" align="flex-end" wrap="wrap">
        <Select
          label={t('rollouts.wave')}
          size="xs"
          w={200}
          clearable
          placeholder={t('common.all')}
          data={waves.map((w) => ({ value: String(w.index), label: `${w.index + 1}: ${w.name}` }))}
          value={wave === undefined ? null : String(wave)}
          onChange={(v) => {
            onWaveChange(v === null ? undefined : Number(v));
            setPage((p) => ({ ...p, offset: 0 }));
          }}
        />
        <Select
          label={t('rollouts.targetSort')}
          size="xs"
          w={190}
          allowDeselect={false}
          data={[
            { value: 'activity', label: t('rollouts.targetSortActivity') },
            { value: 'wave', label: t('rollouts.targetSortWave') },
          ]}
          value={sort}
          onChange={(v) => {
            if (v !== 'activity' && v !== 'wave') return;
            setSort(v);
            setPage((p) => ({ ...p, offset: 0 }));
          }}
        />
        <MultiSelect
          label={t('common.status')}
          size="xs"
          w={300}
          clearable
          placeholder={t('common.all')}
          data={TARGET_STATUSES.map((s) => ({ value: s, label: t(`enum.targetStatus.${s}`) }))}
          value={status}
          onChange={(v) => {
            setStatus(v as TargetStatus[]);
            setPage((p) => ({ ...p, offset: 0 }));
          }}
        />
      </Group>
      {q.isPending ? (
        <TableSkeleton cols={6} rows={6} />
      ) : q.error && !data ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : data && data.total === 0 ? (
        <EmptyState compact title={t('rollouts.targetsEmpty')} />
      ) : data ? (
        <>
          {showCards && (
            <Stack gap="sm" px="md" pb="md" data-testid="target-cards">
              {data.items
                .filter((r) => r.progress)
                .map((r) => (
                  <Paper key={r.agentId} withBorder p="md" radius="md">
                    <TargetProgressView target={r} />
                  </Paper>
                ))}
            </Stack>
          )}
          <div className="myr-scroll-x" style={{ opacity: q.isPlaceholderData ? 0.6 : 1 }}>
            <Table fz="sm" verticalSpacing={6}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('agents.columns.host')}</Table.Th>
                  <Table.Th>{t('rollouts.wave')}</Table.Th>
                  <Table.Th>{t('common.status')}</Table.Th>
                  {showFlow && <Table.Th miw={190}>{t('rollouts.flow')}</Table.Th>}
                  <Table.Th>{t('rollouts.fromTo')}</Table.Th>
                  <Table.Th ta="right">{t('rollouts.attempts')}</Table.Th>
                  <Table.Th>{t('common.updated')}</Table.Th>
                  <Table.Th>{t('rollouts.lastError')}</Table.Th>
                  {showFlow && <Table.Th w={40} aria-label={t('rollouts.flowDetails')} />}
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.items.map((r) => (
                  <Table.Tr
                    key={r.agentId}
                    data-testid="target-row"
                    onClick={r.progress ? () => setDetailAgent(r.agentId) : undefined}
                    style={r.progress ? { cursor: 'pointer' } : undefined}
                  >
                    <Table.Td>
                      <Anchor
                        component={Link}
                        to={agentLink(r.agentId)}
                        fz="sm"
                        fw={500}
                        onClick={(e) => e.stopPropagation()}
                      >
                        {r.hostname || r.agentId.slice(0, 8)}
                      </Anchor>
                      {r.executorRef && (
                        <Text fz="xs" c="dimmed" className="myr-mono">
                          {r.executorRef}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td className="myr-tabular">{r.wave + 1}</Table.Td>
                    <Table.Td>
                      <Stack gap={2} align="flex-start">
                        <TargetStatusBadge status={r.status} />
                        {r.status === 'verifying' && (
                          <HealthWaitCountdown rolloutId={rolloutId} verifiedAt={r.verifiedAt} />
                        )}
                      </Stack>
                    </Table.Td>
                    {showFlow && (
                      <Table.Td>
                        {r.progress ? (
                          <UpdateStepsBar progress={r.progress} />
                        ) : (
                          <Text c="dimmed" fz="sm">
                            –
                          </Text>
                        )}
                      </Table.Td>
                    )}
                    <Table.Td className="myr-mono">
                      {(r.fromRef ?? '–').slice(0, 12)} → {r.toRef.slice(0, 12)}
                    </Table.Td>
                    <Table.Td ta="right" className="myr-tabular">
                      {r.attempts}
                    </Table.Td>
                    <Table.Td>
                      <TimeAgo value={r.updatedAt} />
                    </Table.Td>
                    <Table.Td maw={380}>
                      {r.lastError ? (
                        <Text fz="xs" c="red" style={{ wordBreak: 'break-word' }}>
                          {r.lastError}
                        </Text>
                      ) : (
                        <Text c="dimmed" fz="sm">
                          –
                        </Text>
                      )}
                    </Table.Td>
                    {showFlow && (
                      <Table.Td>
                        {r.progress && (
                          <Tooltip label={t('rollouts.flowDetails')}>
                            <ActionIcon
                              variant="subtle"
                              color="gray"
                              aria-label={`${t('rollouts.flowDetails')}: ${r.hostname ?? r.agentId.slice(0, 8)}`}
                              onClick={(e) => {
                                e.stopPropagation();
                                setDetailAgent(r.agentId);
                              }}
                            >
                              <IconListDetails size={16} />
                            </ActionIcon>
                          </Tooltip>
                        )}
                      </Table.Td>
                    )}
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </div>
          <Pager
            total={data.total}
            limit={page.limit}
            offset={page.offset}
            onChange={setPage}
            loading={q.isFetching}
          />
        </>
      ) : null}
      <Drawer
        opened={detailTarget !== undefined}
        onClose={() => {
          setDetailAgent(null);
          setDetailSnapshot(null);
        }}
        position="right"
        size="md"
        title={t('rollouts.flowTitle')}
      >
        {detailTarget && <TargetProgressView target={detailTarget} />}
      </Drawer>
    </>
  );
}
