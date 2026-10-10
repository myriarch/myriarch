// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Anchor,
  Badge,
  Button,
  Card,
  Code,
  Collapse,
  Group,
  Stack,
  Table,
  Text,
  TextInput,
  UnstyledButton,
  VisuallyHidden,
} from '@mantine/core';
import { DateTimePicker } from '@mantine/dates';
import {
  IconChevronDown,
  IconChevronRight,
  IconHistory,
  IconRobot,
  IconSearch,
  IconUser,
  IconX,
} from '@tabler/icons-react';
import dayjs from 'dayjs';
import { useMemo, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { useAuditEvents, type AuditQuery } from '@/api/hooks/admin';
import type { AuditEvent } from '@/api/types';
import { RequirePermission } from '@/components/Can';
import { PageHeader } from '@/components/PageHeader';
import { Pager } from '@/components/Pager';
import { StatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { DateTime } from '@/components/TimeAgo';
import { agentLink, blueprintLink, fleetLink, rolloutLink } from '@/lib/links';

const SIZES = [50, 100, 200] as const;

function targetLink(type: string | undefined, id: string | undefined): string | undefined {
  if (!type || !id) return undefined;
  switch (type) {
    case 'agent':
      return agentLink(id);
    case 'rollout':
      return rolloutLink(id);
    case 'fleet':
      return fleetLink(id);
    case 'blueprint':
      return blueprintLink(id);
    default:
      return undefined;
  }
}

function toIso(value: string | null): string | undefined {
  if (!value) return undefined;
  const d = dayjs(value);
  return d.isValid() ? d.toISOString() : undefined;
}

function AuditRow({ e }: { e: AuditEvent }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const link = targetLink(e.targetType, e.targetId);
  const hasDetails = (e.details && Object.keys(e.details).length > 0) || e.requestId;
  return (
    <>
      <Table.Tr data-testid="audit-row">
        <Table.Td w={28}>
          {hasDetails && (
            <UnstyledButton
              onClick={() => setOpen((o) => !o)}
              aria-expanded={open}
              aria-label={open ? t('common.collapse') : t('common.expand')}
            >
              {open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
            </UnstyledButton>
          )}
        </Table.Td>
        <Table.Td>
          <DateTime value={e.time} seconds />
        </Table.Td>
        <Table.Td>
          <Group gap={6} wrap="nowrap">
            {e.actorType === 'user' ? <IconUser size={14} /> : <IconRobot size={14} />}
            <div>
              <Text fz="sm" fw={500}>
                {e.actorName || e.actorId}
              </Text>
              <Text fz="xs" c="dimmed">
                {t(`enum.actorType.${e.actorType}`)}
              </Text>
            </div>
          </Group>
        </Table.Td>
        <Table.Td>
          <Badge variant="light" color="gray" style={{ textTransform: 'none' }} className="myr-mono">
            {e.action}
          </Badge>
        </Table.Td>
        <Table.Td>
          {e.targetType ? (
            <Group gap={4} wrap="nowrap">
              <Text fz="xs" c="dimmed">
                {e.targetType}
              </Text>
              {link ? (
                <Anchor component={Link} to={link} fz="sm" className="myr-mono">
                  {(e.targetId ?? '').slice(0, 18)}
                </Anchor>
              ) : (
                <Text fz="sm" className="myr-mono">
                  {e.targetId}
                </Text>
              )}
            </Group>
          ) : (
            '–'
          )}
        </Table.Td>
        <Table.Td>
          <StatusBadge
            tone={e.result === 'success' ? 'success' : 'danger'}
            label={t(`enum.auditResult.${e.result}`)}
          />
        </Table.Td>
        <Table.Td className="myr-mono" fz="xs">
          {e.sourceIp ?? '–'}
        </Table.Td>
      </Table.Tr>
      {hasDetails && (
        <Table.Tr style={{ background: 'transparent' }}>
          <Table.Td colSpan={7} p={0} style={{ border: open ? undefined : 'none' }}>
            <Collapse expanded={open}>
              <Stack gap={6} p="md">
                {e.requestId && (
                  <Text fz="xs" c="dimmed">
                    {t('errors.requestId')}: <Code>{e.requestId}</Code>
                  </Text>
                )}
                {e.details && (
                  <Code block fz="xs">
                    {JSON.stringify(e.details, null, 2)}
                  </Code>
                )}
              </Stack>
            </Collapse>
          </Table.Td>
        </Table.Tr>
      )}
    </>
  );
}

function AuditContent() {
  const { t } = useTranslation();
  const [sp, setSp] = useSearchParams();
  const applied = useMemo(
    () => ({
      actor: sp.get('actor') ?? '',
      action: sp.get('action') ?? '',
      targetType: sp.get('targetType') ?? '',
      targetId: sp.get('targetId') ?? '',
      from: sp.get('from') ?? '',
      to: sp.get('to') ?? '',
      limit: (SIZES as readonly number[]).includes(Number(sp.get('limit')))
        ? Number(sp.get('limit'))
        : 50,
      offset: Number(sp.get('offset')) > 0 ? Number(sp.get('offset')) : 0,
    }),
    [sp],
  );
  const [draft, setDraft] = useState({
    actor: applied.actor,
    action: applied.action,
    targetType: applied.targetType,
    targetId: applied.targetId,
  });
  const [from, setFrom] = useState<string | null>(
    applied.from ? dayjs(applied.from).format('YYYY-MM-DD HH:mm:ss') : null,
  );
  const [to, setTo] = useState<string | null>(
    applied.to ? dayjs(applied.to).format('YYYY-MM-DD HH:mm:ss') : null,
  );

  const query: AuditQuery = {
    actor: applied.actor || undefined,
    action: applied.action || undefined,
    targetType: applied.targetType || undefined,
    targetId: applied.targetId || undefined,
    from: applied.from || undefined,
    to: applied.to || undefined,
    limit: applied.limit,
    offset: applied.offset,
  };
  const q = useAuditEvents(query);
  const hasFilters = !!(
    applied.actor ||
    applied.action ||
    applied.targetType ||
    applied.targetId ||
    applied.from ||
    applied.to
  );

  const write = (next: Record<string, string | number | undefined>) => {
    const out = new URLSearchParams();
    for (const [k, v] of Object.entries(next)) {
      if (
        v !== undefined &&
        v !== '' &&
        !(k === 'limit' && v === 50) &&
        !(k === 'offset' && v === 0)
      )
        out.set(k, String(v));
    }
    setSp(out);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    write({ ...draft, from: toIso(from), to: toIso(to), limit: applied.limit, offset: 0 });
  };
  const clear = () => {
    setDraft({ actor: '', action: '', targetType: '', targetId: '' });
    setFrom(null);
    setTo(null);
    setSp({});
  };

  let body;
  if (q.isPending) body = <TableSkeleton cols={6} rows={10} />;
  else if (q.error && !q.data)
    body = <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  else if (q.data && q.data.total === 0)
    body = (
      <EmptyState
        icon={<IconHistory size={28} />}
        title={hasFilters ? t('audit.emptyFiltered') : t('audit.empty')}
      />
    );
  else if (q.data)
    body = (
      <div className="myr-scroll-x" style={{ opacity: q.isPlaceholderData ? 0.6 : 1 }}>
        <Table fz="sm" verticalSpacing="xs">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>
                <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
              </Table.Th>
              <Table.Th>{t('audit.time')}</Table.Th>
              <Table.Th>{t('audit.actor')}</Table.Th>
              <Table.Th>{t('audit.action')}</Table.Th>
              <Table.Th>{t('audit.target')}</Table.Th>
              <Table.Th>{t('audit.result')}</Table.Th>
              <Table.Th>{t('audit.sourceIp')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.items.map((e) => (
              <AuditRow key={e.id} e={e} />
            ))}
          </Table.Tbody>
        </Table>
      </div>
    );

  return (
    <>
      <PageHeader title={t('audit.title')} subtitle={t('audit.subtitle')} />
      <Card padding="md" mb="md">
        <form onSubmit={submit}>
          <Group gap="sm" align="flex-end" wrap="wrap">
            <TextInput
              size="xs"
              label={t('audit.actor')}
              description={t('audit.actorHint')}
              w={170}
              value={draft.actor}
              onChange={(e) => setDraft({ ...draft, actor: e.currentTarget.value })}
            />
            <TextInput
              size="xs"
              label={t('audit.action')}
              description={t('audit.actionHint')}
              w={190}
              placeholder="rollout.*"
              value={draft.action}
              onChange={(e) => setDraft({ ...draft, action: e.currentTarget.value })}
            />
            <TextInput
              size="xs"
              label={t('audit.targetType')}
              w={130}
              placeholder="rollout"
              value={draft.targetType}
              onChange={(e) => setDraft({ ...draft, targetType: e.currentTarget.value })}
            />
            <TextInput
              size="xs"
              label={t('audit.targetId')}
              w={170}
              value={draft.targetId}
              onChange={(e) => setDraft({ ...draft, targetId: e.currentTarget.value })}
            />
            <DateTimePicker
              size="xs"
              label={t('audit.from')}
              w={190}
              clearable
              value={from}
              onChange={setFrom}
              valueFormat="DD.MM.YYYY HH:mm"
            />
            <DateTimePicker
              size="xs"
              label={t('audit.to')}
              w={190}
              clearable
              value={to}
              onChange={setTo}
              valueFormat="DD.MM.YYYY HH:mm"
            />
            <Button type="submit" size="xs" leftSection={<IconSearch size={14} />}>
              {t('common.filter')}
            </Button>
            {hasFilters && (
              <Button size="xs" variant="subtle" leftSection={<IconX size={14} />} onClick={clear}>
                {t('common.reset')}
              </Button>
            )}
          </Group>
        </form>
      </Card>
      <Card padding={0}>
        {body}
        {q.data && q.data.total > 0 && (
          <Pager
            total={q.data.total}
            limit={applied.limit}
            offset={applied.offset}
            pageSizes={SIZES}
            loading={q.isFetching}
            onChange={(p) =>
              write({ ...Object.fromEntries(sp.entries()), limit: p.limit, offset: p.offset })
            }
          />
        )}
      </Card>
    </>
  );
}

export default function AuditPage() {
  return (
    <RequirePermission permission="audit:read">
      <AuditContent />
    </RequirePermission>
  );
}
