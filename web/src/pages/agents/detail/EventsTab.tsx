// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Badge,
  Button,
  Center,
  Code,
  Collapse,
  Group,
  Skeleton,
  Stack,
  Text,
  Timeline,
  UnstyledButton,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconCircleCheck,
  IconCircleX,
  IconInfoCircle,
  IconChevronDown,
  IconChevronRight,
} from '@tabler/icons-react';
import { useInfiniteQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { fetchAgentEvents } from '@/api/hooks/agents';
import { qk } from '@/api/hooks/keys';
import type { AgentEvent } from '@/api/types';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState, ErrorState } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';

const PAGE = 50;
const SEVERITY = {
  info: { color: 'blue', icon: <IconInfoCircle size={14} /> },
  warning: { color: 'yellow', icon: <IconAlertTriangle size={14} /> },
  error: { color: 'red', icon: <IconCircleX size={14} /> },
} as const;

function EventItem({ e }: { e: AgentEvent }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const hasDetails = e.details && Object.keys(e.details).length > 0;
  return (
    <Stack gap={2}>
      <Group gap={8} wrap="wrap">
        <Badge size="xs" variant="light" color={SEVERITY[e.severity].color}>
          {t(`enum.eventType.${e.type}`)}
        </Badge>
        <Text fz="xs" c="dimmed">
          <TimeAgo value={e.time} /> · <DateTime value={e.time} seconds />
        </Text>
      </Group>
      <Text fz="sm" style={{ wordBreak: 'break-word' }}>
        {e.message}
      </Text>
      {hasDetails && (
        <>
          <UnstyledButton
            onClick={() => setOpen((o) => !o)}
            fz="xs"
            c="dimmed"
            style={{ width: 'fit-content' }}
          >
            <Group gap={4}>
              {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
              {t('common.details')}
            </Group>
          </UnstyledButton>
          <Collapse expanded={open}>
            <Code block fz="xs">
              {JSON.stringify(e.details, null, 2)}
            </Code>
          </Collapse>
        </>
      )}
    </Stack>
  );
}

/** Event timeline, newest first, cursor pagination ("load more"). */
export function EventsTab({ agentId }: { agentId: string }) {
  const { t } = useTranslation();
  const q = useInfiniteQuery({
    queryKey: [...qk.agents.events(agentId), PAGE],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => fetchAgentEvents(agentId, pageParam, PAGE),
    getNextPageParam: (last) =>
      last.items.length >= PAGE && last.nextBefore > 0 ? last.nextBefore : undefined,
  });

  if (q.isPending) return <Skeleton height={260} />;
  if (q.error && !q.data) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  if (items.length === 0) {
    return (
      <SectionCard>
        <EmptyState icon={<IconCircleCheck size={28} />} title={t('agentDetail.events.none')} />
      </SectionCard>
    );
  }
  return (
    <SectionCard
      title={t('agentDetail.events.title')}
      actions={
        <Button
          size="xs"
          variant="default"
          loading={q.isRefetching}
          onClick={() => void q.refetch()}
        >
          {t('common.refresh')}
        </Button>
      }
    >
      <Timeline bulletSize={22} lineWidth={2} active={-1}>
        {items.map((e) => (
          <Timeline.Item
            key={e.id}
            bullet={SEVERITY[e.severity].icon}
            color={SEVERITY[e.severity].color}
          >
            <EventItem e={e} />
          </Timeline.Item>
        ))}
      </Timeline>
      <Center mt="md">
        {q.hasNextPage ? (
          <Button
            variant="light"
            size="xs"
            loading={q.isFetchingNextPage}
            onClick={() => void q.fetchNextPage()}
          >
            {t('common.loadMore')}
          </Button>
        ) : (
          <Text fz="xs" c="dimmed">
            {t('agentDetail.events.end')}
          </Text>
        )}
      </Center>
    </SectionCard>
  );
}
