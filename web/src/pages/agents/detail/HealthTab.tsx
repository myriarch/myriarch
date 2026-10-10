// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Box, Collapse, Group, Stack, Text, UnstyledButton } from '@mantine/core';
import {
  IconChevronDown,
  IconChevronRight,
  IconCircleCheck,
  IconCircleX,
} from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { Agent, ComponentHealth } from '@/api/types';
import { hasUnhealthy } from './healthUtil';
import { SectionCard } from '@/components/SectionCard';
import { HealthBadge } from '@/components/StatusBadge';
import { EmptyState } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';
import { KeyValue } from '@/components/misc';

function Node({ name, health, depth }: { name: string; health: ComponentHealth; depth: number }) {
  const { t } = useTranslation();
  const children = Object.entries(health.components ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const [open, setOpen] = useState(() => depth < 1 || hasUnhealthy(health));
  const Icon = health.healthy ? IconCircleCheck : IconCircleX;
  return (
    <Box role="treeitem" aria-expanded={children.length ? open : undefined}>
      <Group
        gap={6}
        wrap="nowrap"
        align="flex-start"
        py={5}
        pl={depth * 20}
        style={{ borderBottom: '1px solid var(--myr-border)' }}
      >
        {children.length > 0 ? (
          <UnstyledButton
            onClick={() => setOpen((o) => !o)}
            aria-label={open ? t('common.collapse') : t('common.expand')}
            mt={2}
          >
            {open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
          </UnstyledButton>
        ) : (
          <Box w={14} />
        )}
        <Icon
          size={16}
          color={`var(--mantine-color-${health.healthy ? 'teal' : 'red'}-6)`}
          style={{ marginTop: 2, flex: '0 0 auto' }}
          aria-label={health.healthy ? t('enum.health.healthy') : t('enum.health.unhealthy')}
        />
        <Stack gap={1} style={{ minWidth: 0, flex: 1 }}>
          <Group gap={8} wrap="wrap">
            <Text className="myr-mono" fz="sm" fw={600}>
              {name}
            </Text>
            {health.status && (
              <Badge
                size="xs"
                variant="outline"
                color={health.healthy ? 'teal' : 'red'}
                styles={{ label: { textTransform: 'none' } }}
              >
                {health.status}
              </Badge>
            )}
            {health.statusTime && (
              <Text fz="xs" c="dimmed">
                <TimeAgo value={health.statusTime} />
              </Text>
            )}
          </Group>
          {health.lastError && (
            <Text fz="xs" c="red" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
              {health.lastError}
            </Text>
          )}
        </Stack>
      </Group>
      {children.length > 0 && (
        <Collapse expanded={open}>
          {children.map(([n, h]) => (
            <Node key={n} name={n} health={h} depth={depth + 1} />
          ))}
        </Collapse>
      )}
    </Box>
  );
}

export function HealthTab({ agent }: { agent: Agent }) {
  const { t } = useTranslation();
  const h = agent.health;
  if (!h) {
    return (
      <SectionCard>
        <EmptyState title={t('agentDetail.health.none')} text={t('agentDetail.health.noneHint')} />
      </SectionCard>
    );
  }
  const comps = Object.entries(h.components ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return (
    <Stack gap="md">
      <SectionCard title={t('agentDetail.health.overall')}>
        <KeyValue
          items={[
            { label: 'Health', value: <HealthBadge healthy={h.healthy} detail={h.status} /> },
            {
              label: t('common.status'),
              value: h.status ? <span className="myr-mono">{h.status}</span> : '–',
            },
            { label: t('agentDetail.health.startTime'), value: <DateTime value={h.startTime} /> },
            { label: t('common.updated'), value: <TimeAgo value={h.updatedAt} /> },
            {
              label: t('agentDetail.health.lastError'),
              value: (
                <Text c="red" fz="sm" style={{ whiteSpace: 'pre-wrap' }}>
                  {h.lastError}
                </Text>
              ),
              hidden: !h.lastError,
            },
          ]}
        />
      </SectionCard>
      <SectionCard
        title={t('agentDetail.health.components')}
        description={t('agentDetail.health.componentsHint')}
        padded={false}
      >
        {comps.length === 0 ? (
          <EmptyState compact title={t('agentDetail.health.noComponents')} />
        ) : (
          <Box role="tree" px="md" pb="md">
            {comps.map(([n, c]) => (
              <Node key={n} name={n} health={c} depth={0} />
            ))}
          </Box>
        )}
      </SectionCard>
    </Stack>
  );
}
