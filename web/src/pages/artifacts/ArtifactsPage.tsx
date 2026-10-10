// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Code,
  Group,
  MultiSelect,
  SegmentedControl,
  Stack,
  Table,
  Text,
  TextInput,
  Tooltip,
  VisuallyHidden,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconArchive,
  IconCloudDownload,
  IconShieldCheck,
  IconShieldX,
  IconTrash,
} from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ARTIFACT_STATUSES, DISTRIBUTIONS } from '@/api/enums';
import { useArtifacts, useDeleteArtifact } from '@/api/hooks/admin';
import type { Artifact, ArtifactStatus } from '@/api/types';
import { Can } from '@/components/Can';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { CopyIconButton } from '@/components/CopyButton';
import { PageHeader } from '@/components/PageHeader';
import { Pager } from '@/components/Pager';
import { ArtifactStatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { useLang } from '@/i18n/useLang';
import { formatBytes } from '@/lib/format';
import { PrefetchDialog } from './PrefetchDialog';

const SIZES = [25, 50, 100] as const;

function Verification({ a }: { a: Artifact }) {
  const { t } = useTranslation();
  return (
    <Stack gap={2}>
      {a.signatureVerified ? (
        <Tooltip label={a.signatureIdentity ?? t('artifacts.signatureOk')}>
          <Badge
            color="teal"
            variant="light"
            leftSection={<IconShieldCheck size={12} />}
            w="fit-content"
          >
            {t('artifacts.signatureOk')}
          </Badge>
        </Tooltip>
      ) : (
        <Badge color="gray" variant="light" leftSection={<IconShieldX size={12} />} w="fit-content">
          {t('artifacts.signatureNone')}
        </Badge>
      )}
      {a.signatureIdentity && (
        <Text fz="xs" c="dimmed" className="myr-mono" lineClamp={1} maw={260}>
          {a.signatureIdentity}
        </Text>
      )}
      {a.verificationError && (
        <Text fz="xs" c="red" maw={260}>
          {a.verificationError}
        </Text>
      )}
    </Stack>
  );
}

export default function ArtifactsPage() {
  const { t } = useTranslation();
  const lang = useLang();
  const [distribution, setDistribution] = useState<'all' | (typeof DISTRIBUTIONS)[number]>('all');
  const [version, setVersion] = useState('');
  const [status, setStatus] = useState<ArtifactStatus[]>([]);
  const [page, setPage] = useState({ limit: 25, offset: 0 });
  const [dialogOpen, dialog] = useDisclosure(false);
  const [toDelete, setToDelete] = useState<Artifact | null>(null);
  const del = useDeleteArtifact();

  const query = {
    distribution: distribution === 'all' ? undefined : distribution,
    version: version.trim() || undefined,
    status: status.length ? status : undefined,
    ...page,
  };
  const q = useArtifacts(query, { refetchInterval: 5000 });
  const reset = () => setPage((p) => ({ ...p, offset: 0 }));

  let body;
  if (q.isPending) body = <TableSkeleton cols={7} rows={6} />;
  else if (q.error && !q.data)
    body = <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  else if (q.data && q.data.total === 0)
    body = (
      <EmptyState
        icon={<IconArchive size={28} />}
        title={t('artifacts.empty')}
        text={t('artifacts.emptyHint')}
      />
    );
  else if (q.data)
    body = (
      <div className="myr-scroll-x" style={{ opacity: q.isPlaceholderData ? 0.6 : 1 }}>
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.version')}</Table.Th>
              <Table.Th>{t('artifacts.platform')}</Table.Th>
              <Table.Th>{t('artifacts.file')}</Table.Th>
              <Table.Th ta="right">{t('releases.size')}</Table.Th>
              <Table.Th>SHA-256</Table.Th>
              <Table.Th>{t('artifacts.signature')}</Table.Th>
              <Table.Th>{t('common.status')}</Table.Th>
              <Table.Th>
                <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
              </Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.items.map((a) => (
              <Table.Tr key={a.id} data-testid="artifact-row">
                <Table.Td>
                  <Text className="myr-mono" fw={600}>
                    {a.version}
                  </Text>
                  <Text fz="xs" c="dimmed">
                    {a.distribution}
                  </Text>
                </Table.Td>
                <Table.Td>
                  {a.os}/{a.arch}
                </Table.Td>
                <Table.Td>
                  <Text className="myr-mono" fz="xs" style={{ wordBreak: 'break-all' }}>
                    {a.filename}
                  </Text>
                  <Badge size="xs" variant="outline" color="gray">
                    {a.format}
                  </Badge>
                </Table.Td>
                <Table.Td ta="right" className="myr-tabular">
                  {formatBytes(lang, a.size)}
                </Table.Td>
                <Table.Td>
                  {a.sha256 ? (
                    <Group gap={2} wrap="nowrap">
                      <Tooltip label={a.sha256}>
                        <Code>{a.sha256.slice(0, 12)}…</Code>
                      </Tooltip>
                      <CopyIconButton text={a.sha256} size="xs" />
                    </Group>
                  ) : (
                    '–'
                  )}
                </Table.Td>
                <Table.Td>
                  <Verification a={a} />
                </Table.Td>
                <Table.Td>
                  <Stack gap={2}>
                    <ArtifactStatusBadge status={a.status} />
                    {a.error && (
                      <Text fz="xs" c="red" maw={240}>
                        {a.error}
                      </Text>
                    )}
                    <Text fz="xs" c="dimmed">
                      <TimeAgo value={a.updatedAt} />
                    </Text>
                  </Stack>
                </Table.Td>
                <Table.Td>
                  <Can permission="artifacts:manage">
                    <Tooltip label={t('common.delete')}>
                      <ActionIcon
                        variant="subtle"
                        color="red"
                        onClick={() => setToDelete(a)}
                        aria-label={`${t('common.delete')}: ${a.filename}`}
                      >
                        <IconTrash size={16} />
                      </ActionIcon>
                    </Tooltip>
                  </Can>
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
        title={t('artifacts.title')}
        subtitle={t('artifacts.subtitle')}
        actions={
          <Can permission="artifacts:manage">
            <Button size="xs" leftSection={<IconCloudDownload size={14} />} onClick={dialog.open}>
              {t('artifacts.prefetch.open')}
            </Button>
          </Can>
        }
      />
      <Card padding="md" mb="md">
        <Group gap="md" align="flex-end" wrap="wrap">
          <Stack gap={4}>
            <Text fz="xs" fw={500}>
              {t('releases.distribution')}
            </Text>
            <SegmentedControl
              size="xs"
              value={distribution}
              onChange={(v) => {
                setDistribution(v as typeof distribution);
                reset();
              }}
              data={[
                { value: 'all', label: t('common.all') },
                ...DISTRIBUTIONS.map((d) => ({ value: d, label: d })),
              ]}
            />
          </Stack>
          <TextInput
            label={t('common.version')}
            size="xs"
            w={140}
            placeholder="0.162.0"
            value={version}
            onChange={(e) => {
              setVersion(e.currentTarget.value);
              reset();
            }}
          />
          <MultiSelect
            label={t('common.status')}
            size="xs"
            w={260}
            clearable
            placeholder={t('common.all')}
            data={ARTIFACT_STATUSES.map((s) => ({
              value: s,
              label: t(`enum.artifactStatus.${s}`),
            }))}
            value={status}
            onChange={(v) => {
              setStatus(v as ArtifactStatus[]);
              reset();
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

      <PrefetchDialog opened={dialogOpen} onClose={dialog.close} />
      <ConfirmDialog
        opened={toDelete !== null}
        onClose={() => setToDelete(null)}
        color="red"
        title={t('artifacts.deleteTitle')}
        confirmLabel={t('common.delete')}
        loading={del.isPending}
        onConfirm={() =>
          toDelete && del.mutate(toDelete.id, { onSuccess: () => setToDelete(null) })
        }
      >
        <Text fz="sm">{t('artifacts.deleteText', { file: toDelete?.filename ?? '' })}</Text>
      </ConfirmDialog>
    </>
  );
}
