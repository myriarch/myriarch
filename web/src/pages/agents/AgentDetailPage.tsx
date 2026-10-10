// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Button, Group, Skeleton, Stack, Tabs, Text, Tooltip } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconActivityHeartbeat,
  IconBoxMultiple,
  IconFileCode,
  IconHistory,
  IconInfoCircle,
  IconRefresh,
  IconRestore,
  IconRocket,
  IconTrash,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams, useSearchParams } from 'react-router';
import { useAgent, useDeleteAgent, useRestartAgent, useResyncAgent } from '@/api/hooks/agents';
import { Can } from '@/components/Can';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { PageHeader } from '@/components/PageHeader';
import { ConfigSyncBadge, ConnectionBadge, HealthBadge } from '@/components/StatusBadge';
import { ErrorState } from '@/components/states';
import { isApiError } from '@/api/errors';
import { ComponentsTab } from './detail/ComponentsTab';
import { ConfigTab } from './detail/ConfigTab';
import { EventsTab } from './detail/EventsTab';
import { HealthTab } from './detail/HealthTab';
import { OverviewTab } from './detail/OverviewTab';
import { RolloutsTab } from './detail/RolloutsTab';

const TABS = ['overview', 'health', 'config', 'components', 'events', 'rollouts'] as const;
type TabId = (typeof TABS)[number];

export default function AgentDetailPage() {
  const { t } = useTranslation();
  const { id } = useParams();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tabParam = params.get('tab');
  const tab: TabId = (TABS as readonly string[]).includes(tabParam ?? '')
    ? (tabParam as TabId)
    : 'overview';

  const { data: detail, isPending, error, refetch } = useAgent(id);
  const restart = useRestartAgent();
  const resync = useResyncAgent();
  const del = useDeleteAgent();
  const [restartOpen, restartDlg] = useDisclosure(false);
  const [resyncOpen, resyncDlg] = useDisclosure(false);
  const [deleteOpen, deleteDlg] = useDisclosure(false);

  const crumbs = [
    { label: t('nav.agents'), to: '/agents' },
    { label: detail?.agent.hostname || id || '' },
  ];

  if (isPending) {
    return (
      <>
        <PageHeader
          title={<Skeleton height={28} width={240} />}
          breadcrumbs={crumbs}
          documentTitle={t('nav.agents')}
        />
        <Stack>
          <Skeleton height={40} />
          <Skeleton height={320} />
        </Stack>
      </>
    );
  }
  if (error || !detail) {
    const notFound = isApiError(error) && error.status === 404;
    return (
      <>
        <PageHeader title={t('agentDetail.title')} breadcrumbs={crumbs} />
        <ErrorState
          error={error}
          title={notFound ? t('agentDetail.notFound') : undefined}
          onRetry={notFound ? undefined : () => void refetch()}
        />
      </>
    );
  }

  const { agent } = detail;
  const canRestart = detail.capabilityNames.includes('AcceptsRestartCommand');
  const offline = agent.connection.status === 'offline';

  return (
    <>
      <PageHeader
        title={agent.hostname || agent.id}
        breadcrumbs={crumbs}
        subtitle={
          <Group gap={6} component="span">
            <Text span className="myr-mono" fz="sm">
              {agent.id}
            </Text>
          </Group>
        }
        badge={
          <Group gap={6}>
            <ConnectionBadge status={agent.connection.status} />
            <HealthBadge healthy={agent.health?.healthy} detail={agent.health?.status} />
            <ConfigSyncBadge state={detail.configSync} />
            {agent.kind === 'updater' && (
              <Badge variant="outline" color="gray">
                {t('enum.kind.updater')}
              </Badge>
            )}
          </Group>
        }
        actions={
          <>
            <Can permission="agents:operate">
              <Tooltip
                label={
                  canRestart
                    ? t('agentDetail.actions.restartHint')
                    : t('agentDetail.actions.restartUnsupported')
                }
              >
                <span>
                  <Button
                    variant="default"
                    size="xs"
                    leftSection={<IconRestore size={14} />}
                    disabled={!canRestart || offline}
                    onClick={restartDlg.open}
                  >
                    {t('agentDetail.actions.restart')}
                  </Button>
                </span>
              </Tooltip>
              <Button
                variant="default"
                size="xs"
                leftSection={<IconRefresh size={14} />}
                onClick={resyncDlg.open}
              >
                {t('agentDetail.actions.resync')}
              </Button>
            </Can>
            <Can permission="agents:delete">
              <Tooltip
                label={
                  offline
                    ? t('agentDetail.actions.deleteHint')
                    : t('agentDetail.actions.deleteOnlyOffline')
                }
              >
                <span>
                  <Button
                    variant="light"
                    color="red"
                    size="xs"
                    leftSection={<IconTrash size={14} />}
                    disabled={!offline}
                    onClick={deleteDlg.open}
                  >
                    {t('common.delete')}
                  </Button>
                </span>
              </Tooltip>
            </Can>
          </>
        }
      />

      <Tabs
        value={tab}
        onChange={(v) => setParams(v && v !== 'overview' ? { tab: v } : {}, { replace: true })}
        keepMounted={false}
      >
        <Tabs.List mb="md" style={{ overflowX: 'auto', flexWrap: 'nowrap' }}>
          <Tabs.Tab value="overview" leftSection={<IconInfoCircle size={16} />}>
            {t('agentDetail.tabs.overview')}
          </Tabs.Tab>
          <Tabs.Tab value="health" leftSection={<IconActivityHeartbeat size={16} />}>
            {t('agentDetail.tabs.health')}
          </Tabs.Tab>
          <Tabs.Tab value="config" leftSection={<IconFileCode size={16} />}>
            {t('agentDetail.tabs.config')}
          </Tabs.Tab>
          <Tabs.Tab value="components" leftSection={<IconBoxMultiple size={16} />}>
            {t('agentDetail.tabs.components')}
          </Tabs.Tab>
          <Tabs.Tab value="events" leftSection={<IconHistory size={16} />}>
            {t('agentDetail.tabs.events')}
          </Tabs.Tab>
          <Tabs.Tab
            value="rollouts"
            leftSection={<IconRocket size={16} />}
            rightSection={
              detail.activeRolloutTargets.length > 0 ? (
                <Badge size="xs" circle>
                  {detail.activeRolloutTargets.length}
                </Badge>
              ) : null
            }
          >
            {t('agentDetail.tabs.rollouts')}
          </Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="overview">
          <OverviewTab detail={detail} />
        </Tabs.Panel>
        <Tabs.Panel value="health">
          <HealthTab agent={agent} />
        </Tabs.Panel>
        <Tabs.Panel value="config">
          <ConfigTab agent={agent} />
        </Tabs.Panel>
        <Tabs.Panel value="components">
          <ComponentsTab agentId={agent.id} />
        </Tabs.Panel>
        <Tabs.Panel value="events">
          <EventsTab agentId={agent.id} />
        </Tabs.Panel>
        <Tabs.Panel value="rollouts">
          <RolloutsTab targets={detail.activeRolloutTargets} />
        </Tabs.Panel>
      </Tabs>

      <ConfirmDialog
        opened={restartOpen}
        onClose={restartDlg.close}
        title={t('agentDetail.actions.restartTitle')}
        confirmLabel={t('agentDetail.actions.restart')}
        loading={restart.isPending}
        onConfirm={() => restart.mutate(agent.id, { onSuccess: restartDlg.close })}
      >
        {t('agentDetail.actions.restartText', { host: agent.hostname })}
      </ConfirmDialog>
      <ConfirmDialog
        opened={resyncOpen}
        onClose={resyncDlg.close}
        title={t('agentDetail.actions.resyncTitle')}
        confirmLabel={t('agentDetail.actions.resync')}
        loading={resync.isPending}
        onConfirm={() => resync.mutate(agent.id, { onSuccess: resyncDlg.close })}
      >
        {t('agentDetail.actions.resyncText')}
      </ConfirmDialog>
      <ConfirmDialog
        opened={deleteOpen}
        onClose={deleteDlg.close}
        color="red"
        title={t('agentDetail.actions.deleteTitle')}
        confirmLabel={t('common.delete')}
        loading={del.isPending}
        onConfirm={() =>
          del.mutate(agent.id, {
            onSuccess: () => {
              deleteDlg.close();
              void navigate('/agents', { replace: true });
            },
          })
        }
      >
        {t('agentDetail.actions.deleteText', { host: agent.hostname })}
      </ConfirmDialog>
    </>
  );
}
