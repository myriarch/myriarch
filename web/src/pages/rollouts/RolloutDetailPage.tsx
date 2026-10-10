// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Button,
  Code,
  Collapse,
  Grid,
  Group,
  Menu,
  Skeleton,
  Stack,
  Text,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconArrowBackUp,
  IconChevronDown,
  IconPlayerPause,
  IconPlayerPlay,
  IconPlayerStop,
  IconRepeat,
  IconInfoCircle,
} from '@tabler/icons-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams, useSearchParams } from 'react-router';
import { isApiError } from '@/api/errors';
import { useFleet } from '@/api/hooks/fleets';
import {
  useApproveWave,
  useRollout,
  useRolloutAction,
  type RolloutAction,
} from '@/api/hooks/rollouts';
import { useAuth } from '@/auth/useAuth';
import { Can } from '@/components/Can';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { KeyValue } from '@/components/misc';
import { RolloutOriginBadge } from '@/components/VersionSourceBadge';
import { PageHeader } from '@/components/PageHeader';
import { PolicyView } from '@/components/PolicyView';
import { RolloutProgress } from '@/components/RolloutProgress';
import { SectionCard } from '@/components/SectionCard';
import { RolloutStatusBadge } from '@/components/StatusBadge';
import { ErrorState } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';
import { fleetLink, policyLink } from '@/lib/links';
import { availableActions, isTerminal } from './rolloutActions';
import { RolloutAnnotations } from './RolloutAnnotations';
import { RolloutOriginLine } from './RolloutOriginLine';
import { TargetsTable } from './TargetsTable';
import { WaveSteps } from './WaveSteps';

const ACTION_META: Record<RolloutAction, { icon: ReactNode; color?: string }> = {
  pause: { icon: <IconPlayerPause size={14} /> },
  resume: { icon: <IconPlayerPlay size={14} /> },
  abort: { icon: <IconPlayerStop size={14} />, color: 'red' },
  rollback: { icon: <IconArrowBackUp size={14} />, color: 'red' },
  'retry-failed': { icon: <IconRepeat size={14} /> },
};

type Pending = { kind: 'action'; action: RolloutAction } | { kind: 'approve'; wave: number } | null;

export default function RolloutDetailPage() {
  const { t } = useTranslation();
  const { id } = useParams();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const waveParam = params.get('wave');
  const selectedWave =
    waveParam !== null && Number.isInteger(Number(waveParam)) ? Number(waveParam) : undefined;
  const [pending, setPending] = useState<Pending>(null);
  const [policyOpen, policy] = useDisclosure(false);

  // Live view: SSE rollout.updated invalidates the queries; this polling is the fallback while the
  // rollout is active (a terminal rollout no longer changes).
  const q = useRollout(id, {
    refetchInterval: (rollout) => (rollout && isTerminal(rollout.status) ? false : 5_000),
  });
  // After a rollout rollback the UI selection stays active: point the operator to the fleet.
  // Also for a paused/failed/aborted rollout with failed targets (policy onFailure: pause): the selection
  // stays active there as well, and so does the way out.
  const hintFleet =
    q.data &&
    q.data.origin === 'ui' &&
    (q.data.status === 'rolled_back' ||
      (q.data.counts.failed > 0 &&
        (q.data.status === 'paused' || q.data.status === 'failed' || q.data.status === 'aborted')))
      ? q.data.fleetName
      : undefined;
  const fleetForHint = useFleet(hintFleet);
  const act = useRolloutAction(id ?? '');
  const approve = useApproveWave(id ?? '');
  // A new dialog must not show the error of a previous, different action.
  const openDialog = (p: NonNullable<Pending>) => {
    act.reset();
    approve.reset();
    setPending(p);
  };
  const crumbs = [{ label: t('nav.rollouts'), to: '/rollouts' }, { label: id?.slice(0, 8) ?? '' }];

  if (q.isPending) {
    return (
      <>
        <PageHeader title={t('rollouts.detailTitle')} breadcrumbs={crumbs} />
        <Stack>
          <Skeleton height={120} />
          <Skeleton height={260} />
        </Stack>
      </>
    );
  }
  if (q.error || !q.data) {
    const notFound = isApiError(q.error) && q.error.status === 404;
    return (
      <>
        <PageHeader title={t('rollouts.detailTitle')} breadcrumbs={crumbs} />
        <ErrorState
          error={q.error}
          title={notFound ? t('rollouts.notFound') : undefined}
          onRetry={notFound ? undefined : () => void q.refetch()}
        />
      </>
    );
  }

  const r = q.data;
  const { actions, approvableWaves } = availableActions(r);
  const active = !isTerminal(r.status);
  // A paused/failed rollout with failed targets: the operator's next step is "retry failed".
  const highlightRetry =
    actions.includes('retry-failed') &&
    r.counts.failed > 0 &&
    (r.status === 'paused' || r.status === 'failed');
  const selectionStillActive =
    fleetForHint.data?.status.versionTarget.source === 'ui' &&
    fleetForHint.data.status.versionTarget.version === r.toRef;
  const setWave = (w: number | undefined) => {
    const next = new URLSearchParams(params);
    if (w === undefined) next.delete('wave');
    else next.set('wave', String(w));
    setParams(next, { replace: true });
  };

  const close = () => setPending(null);
  const confirm = (reason?: string) => {
    if (!pending) return;
    if (pending.kind === 'action')
      act.mutate({ action: pending.action, reason }, { onSuccess: close });
    else approve.mutate({ wave: pending.wave, reason }, { onSuccess: close });
  };
  const dlgKey = pending?.kind === 'approve' ? 'approve' : pending?.action;
  const loading = act.isPending || approve.isPending;
  const err = pending?.kind === 'approve' ? approve.error : act.error;

  return (
    <>
      <PageHeader
        title={`${r.fleetName} · ${t(`enum.rolloutKind.${r.kind}`)}`}
        breadcrumbs={crumbs}
        subtitle={<span className="myr-mono">{r.id}</span>}
        badge={
          <Group gap={6}>
            <RolloutStatusBadge status={r.status} size="lg" />
            <RolloutOriginBadge origin={r.origin} size="lg" />
          </Group>
        }
        actions={
          <Can permission="rollouts:operate">
            {highlightRetry && (
              <Button
                size="xs"
                color="orange"
                leftSection={<IconRepeat size={14} />}
                onClick={() => openDialog({ kind: 'action', action: 'retry-failed' })}
                data-testid="retry-primary"
              >
                {t('rollouts.actions.retry-failed')}
              </Button>
            )}
            {approvableWaves.length > 0 && (
              <Button
                size="xs"
                color="yellow"
                onClick={() => openDialog({ kind: 'approve', wave: approvableWaves[0]! })}
                data-testid="approve-primary"
              >
                {t('rollouts.approveWave', { n: approvableWaves[0]! + 1 })}
              </Button>
            )}
            {actions
              .filter((a) => a === 'pause' || a === 'resume')
              .map((a) => (
                <Button
                  key={a}
                  size="xs"
                  variant="default"
                  leftSection={ACTION_META[a].icon}
                  onClick={() => openDialog({ kind: 'action', action: a })}
                >
                  {t(`rollouts.actions.${a}`)}
                </Button>
              ))}
            {(actions.some((a) => a === 'abort' || a === 'rollback') ||
              (actions.includes('retry-failed') && !highlightRetry)) && (
              <Menu position="bottom-end" width={240}>
                <Menu.Target>
                  <Button size="xs" variant="default" rightSection={<IconChevronDown size={14} />}>
                    {t('rollouts.moreActions')}
                  </Button>
                </Menu.Target>
                <Menu.Dropdown>
                  {actions
                    .filter((a) => a === 'retry-failed' && !highlightRetry)
                    .map((a) => (
                      <Menu.Item
                        key={a}
                        leftSection={ACTION_META[a].icon}
                        onClick={() => openDialog({ kind: 'action', action: a })}
                      >
                        {t(`rollouts.actions.${a}`)}
                      </Menu.Item>
                    ))}
                  {actions.some((a) => a === 'abort' || a === 'rollback') && <Menu.Divider />}
                  {actions
                    .filter((a) => a === 'rollback' || a === 'abort')
                    .map((a) => (
                      <Menu.Item
                        key={a}
                        color="red"
                        leftSection={ACTION_META[a].icon}
                        onClick={() => openDialog({ kind: 'action', action: a })}
                      >
                        {t(`rollouts.actions.${a}`)}
                      </Menu.Item>
                    ))}
                </Menu.Dropdown>
              </Menu>
            )}
          </Can>
        }
      />

      <RolloutOriginLine rollout={r} />
      {!can('rollouts:operate') && (
        <Alert color="gray" variant="light" icon={<IconInfoCircle size={16} />} mb="md" py="xs">
          {t('rollouts.readOnlyRole')}
        </Alert>
      )}
      {r.status === 'waiting_approval' && can('rollouts:operate') && (
        <Alert color="yellow" variant="light" mb="md" title={t('rollouts.waitingTitle')}>
          {t('rollouts.waitingText')}
        </Alert>
      )}
      {hintFleet && selectionStillActive && (
        <Alert color="violet" variant="light" mb="md" data-testid="selection-still-active">
          <Group gap="xs" wrap="wrap">
            <Text fz="sm">
              {r.status === 'rolled_back'
                ? t('versionUpdate.rollback.selectionActive', { version: r.toRef })
                : t('versionUpdate.rollback.selectionActiveStopped', { version: r.toRef })}
            </Text>
            <Anchor component={Link} to={fleetLink(r.fleetName)} fz="sm">
              {t('versionUpdate.rollback.clearLink')}
            </Anchor>
          </Group>
        </Alert>
      )}
      {r.statusReason && (
        <Alert
          color={r.status === 'failed' || r.status === 'aborted' ? 'red' : 'blue'}
          variant="light"
          mb="md"
          title={t('rollouts.statusReason')}
        >
          {r.statusReason}
        </Alert>
      )}

      <Grid gap="md">
        <Grid.Col span={{ base: 12, lg: 5 }}>
          <Stack gap="md">
            <SectionCard title={t('rollouts.summary')}>
              <RolloutProgress counts={r.counts} legend height={12} />
              <KeyValue
                labelWidth={140}
                items={[
                  {
                    label: t('common.fleet'),
                    value: (
                      <Anchor component={Link} to={fleetLink(r.fleetName)}>
                        {r.fleetName}
                      </Anchor>
                    ),
                  },
                  {
                    label: t('rollouts.change'),
                    value: (
                      <span className="myr-mono">
                        {r.kind === 'version'
                          ? `${r.fromRef ?? '?'} → ${r.toRef}`
                          : `${(r.fromRef ?? '').slice(0, 8) || '?'} → ${r.toRef.slice(0, 8)}`}
                      </span>
                    ),
                  },
                  {
                    label: t('rollouts.gitCommit'),
                    value: r.gitCommit ? <Code>{r.gitCommit.slice(0, 12)}</Code> : '–',
                    hidden: !r.gitCommit,
                  },
                  {
                    label: t('rollouts.policy'),
                    value: (
                      <Anchor component={Link} to={policyLink(r.policyName)}>
                        {r.policyName}
                      </Anchor>
                    ),
                  },
                  {
                    label: t('fleets.executor'),
                    value: r.executor ? t(`enum.executor.${r.executor}`) : '–',
                    hidden: !r.executor,
                  },
                  { label: t('rollouts.createdBy'), value: r.createdBy },
                  { label: t('common.created'), value: <DateTime value={r.createdAt} /> },
                  { label: t('rollouts.startedAt'), value: <DateTime value={r.startedAt} /> },
                  { label: t('common.updated'), value: <TimeAgo value={r.updatedAt} /> },
                  {
                    label: t('rollouts.finishedAt'),
                    value: <DateTime value={r.finishedAt} />,
                    hidden: !r.finishedAt,
                  },
                ]}
              />
              <RolloutAnnotations annotations={r.annotations} />
            </SectionCard>
          </Stack>
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 7 }}>
          <SectionCard title={t('policy.waves')} description={t('rollouts.wavesHint')}>
            <WaveSteps
              waves={r.waves}
              currentWave={r.currentWave}
              canApprove={can('rollouts:operate')}
              onApprove={(w) => openDialog({ kind: 'approve', wave: w })}
              selected={selectedWave}
              onSelect={setWave}
            />
          </SectionCard>
        </Grid.Col>

        <Grid.Col span={12}>
          <SectionCard title={t('rollouts.targets')} padded={false} id="targets">
            <TargetsTable
              rolloutId={r.id}
              waves={r.waves}
              wave={selectedWave}
              onWaveChange={setWave}
              active={active}
            />
          </SectionCard>
        </Grid.Col>

        <Grid.Col span={12}>
          <SectionCard
            title={t('rollouts.policySnapshot')}
            description={t('rollouts.policySnapshotHint')}
            actions={
              <Button size="xs" variant="subtle" onClick={policy.toggle}>
                {policyOpen ? t('common.collapse') : t('common.expand')}
              </Button>
            }
          >
            <Collapse expanded={policyOpen}>
              <PolicyView policy={r.policy} />
            </Collapse>
          </SectionCard>
        </Grid.Col>
      </Grid>

      <ConfirmDialog
        opened={pending !== null}
        onClose={close}
        loading={loading}
        error={err}
        color={dlgKey === 'abort' || dlgKey === 'rollback' ? 'red' : 'brand'}
        title={dlgKey ? t(`rollouts.dialog.${dlgKey}.title`) : ''}
        confirmLabel={dlgKey ? t(`rollouts.dialog.${dlgKey}.confirm`) : undefined}
        reason={{ label: t('rollouts.reasonLabel') }}
        onConfirm={confirm}
      >
        {dlgKey && (
          <Text fz="sm">
            {dlgKey === 'approve' && pending?.kind === 'approve'
              ? t('rollouts.dialog.approve.text', {
                  n: pending.wave + 1,
                  name: r.waves[pending.wave]?.name ?? '',
                })
              : t(`rollouts.dialog.${dlgKey}.text`)}
          </Text>
        )}
      </ConfirmDialog>
    </>
  );
}
