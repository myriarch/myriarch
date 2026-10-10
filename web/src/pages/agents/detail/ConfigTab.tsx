// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Badge, Code, Grid, Group, SegmentedControl, Skeleton, Stack, Text } from '@mantine/core';
import { IconInfoCircle } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAgentDesiredConfig, useAgentEffectiveConfig } from '@/api/hooks/agents';
import { isApiError } from '@/api/errors';
import type { Agent, ConfigDocument } from '@/api/types';
import { CopyIconButton } from '@/components/CopyButton';
import { SectionCard } from '@/components/SectionCard';
import { StatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorState } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { YamlDiff, YamlView } from '@/components/Yaml';
import { shortHash } from '@/lib/format';

type View = 'side' | 'diff';
type Which = 'desired' | 'assigned';

function DocHeader({ doc, hash }: { doc?: ConfigDocument; hash?: string }) {
  const { t } = useTranslation();
  const h = doc?.hash ?? hash;
  return (
    <Group gap="xs" wrap="nowrap">
      {h && (
        <Group gap={2} wrap="nowrap">
          <Text fz="xs" c="dimmed">
            {t('agentDetail.config.hash')}:
          </Text>
          <Code>{shortHash(h, 12)}</Code>
          <CopyIconButton text={h} size="xs" />
        </Group>
      )}
      {doc?.redacted && (
        <Badge size="xs" variant="light" color="yellow" title={t('agentDetail.config.redactedHint')}>
          {t('agentDetail.config.redacted')}
        </Badge>
      )}
      {doc?.updatedAt && (
        <Text fz="xs" c="dimmed">
          <TimeAgo value={doc.updatedAt} />
        </Text>
      )}
    </Group>
  );
}

function DocBody({
  doc,
  loading,
  error,
  onRetry,
  emptyTitle,
}: {
  doc: ConfigDocument | undefined;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  emptyTitle: string;
}) {
  if (loading) return <Skeleton height={260} />;
  if (isApiError(error) && error.status === 404) return <EmptyState compact title={emptyTitle} />;
  if (error) return <ErrorState compact error={error} onRetry={onRetry} />;
  if (!doc || doc.body.trim() === '') return <EmptyState compact title={emptyTitle} />;
  return <YamlView value={doc.body} />;
}

/** Desired/assigned vs. effective config, side by side or as diff. The diff is informative only. */
export function ConfigTab({ agent }: { agent: Agent }) {
  const { t } = useTranslation();
  const [which, setWhich] = useState<Which>('desired');
  const [view, setView] = useState<View>('side');
  const effective = useAgentEffectiveConfig(agent.id);
  const wanted = useAgentDesiredConfig(agent.id, which);

  const rc = agent.remoteConfig;
  return (
    <Stack gap="md">
      <Alert variant="light" color="blue" icon={<IconInfoCircle size={18} />}>
        {t('agentDetail.config.effectiveNote')}
      </Alert>

      <SectionCard title={t('agentDetail.config.remoteStatus')}>
        <Group gap="lg" wrap="wrap">
          <StatusBadge
            tone={
              rc.status === 'applied'
                ? 'success'
                : rc.status === 'failed'
                  ? 'danger'
                  : rc.status === 'applying'
                    ? 'info'
                    : 'neutral'
            }
            label={t(`enum.remoteConfig.${rc.status}`)}
          />
          <Group gap={6}>
            <Text fz="sm" c="dimmed">
              {t('agentDetail.config.lastHash')}:
            </Text>
            <Code>{shortHash(rc.lastHash, 12)}</Code>
          </Group>
          <Group gap={6}>
            <Text fz="sm" c="dimmed">
              {t('agentDetail.desiredHash')}:
            </Text>
            <Code>{shortHash(agent.assignment.desiredConfigHash, 12)}</Code>
          </Group>
          <Group gap={6}>
            <Text fz="sm" c="dimmed">
              {t('agentDetail.config.effectiveHash')}:
            </Text>
            <Code>{shortHash(agent.effectiveConfigHash, 12)}</Code>
          </Group>
        </Group>
        {rc.errorMessage && (
          <Alert color="red" variant="light" mt="sm" title={t('agentDetail.config.errorFromAgent')}>
            <Text fz="sm" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
              {rc.errorMessage}
            </Text>
          </Alert>
        )}
      </SectionCard>

      <Group justify="space-between" wrap="wrap">
        <SegmentedControl
          size="xs"
          value={which}
          onChange={(v) => setWhich(v as Which)}
          data={[
            { value: 'desired', label: t('agentDetail.config.desired') },
            { value: 'assigned', label: t('agentDetail.config.assigned') },
          ]}
          aria-label={t('agentDetail.config.compareWith')}
        />
        <SegmentedControl
          size="xs"
          value={view}
          onChange={(v) => setView(v as View)}
          data={[
            { value: 'side', label: t('agentDetail.config.sideBySide') },
            { value: 'diff', label: t('agentDetail.config.diff') },
          ]}
          aria-label={t('agentDetail.config.view')}
        />
      </Group>

      {view === 'side' ? (
        <Grid gap="md">
          <Grid.Col span={{ base: 12, lg: 6 }}>
            <SectionCard
              title={
                which === 'desired'
                  ? t('agentDetail.config.desired')
                  : t('agentDetail.config.assigned')
              }
              actions={
                <DocHeader
                  doc={wanted.data}
                  hash={
                    which === 'desired'
                      ? agent.assignment.desiredConfigHash
                      : agent.assignment.assignedConfigHash
                  }
                />
              }
            >
              <DocBody
                doc={wanted.data}
                loading={wanted.isPending}
                error={wanted.error}
                onRetry={() => void wanted.refetch()}
                emptyTitle={t('agentDetail.config.noneAssigned')}
              />
            </SectionCard>
          </Grid.Col>
          <Grid.Col span={{ base: 12, lg: 6 }}>
            <SectionCard
              title={t('agentDetail.config.effective')}
              actions={<DocHeader doc={effective.data} hash={agent.effectiveConfigHash} />}
            >
              <DocBody
                doc={effective.data}
                loading={effective.isPending}
                error={effective.error}
                onRetry={() => void effective.refetch()}
                emptyTitle={t('agentDetail.config.noneReported')}
              />
            </SectionCard>
          </Grid.Col>
        </Grid>
      ) : (
        <SectionCard
          title={t('agentDetail.config.diffTitle', {
            left:
              which === 'desired'
                ? t('agentDetail.config.desired')
                : t('agentDetail.config.assigned'),
          })}
          description={t('agentDetail.config.diffInformative')}
        >
          {wanted.isPending || effective.isPending ? (
            <Skeleton height={260} />
          ) : wanted.data && effective.data ? (
            <YamlDiff a={wanted.data.body} b={effective.data.body} />
          ) : (
            <EmptyState compact title={t('agentDetail.config.diffUnavailable')} />
          )}
        </SectionCard>
      )}
    </Stack>
  );
}
