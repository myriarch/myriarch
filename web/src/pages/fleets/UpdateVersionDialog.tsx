// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Alert,
  Anchor,
  Badge,
  Button,
  Group,
  Loader,
  Modal,
  Paper,
  Radio,
  Skeleton,
  Stack,
  Text,
  Textarea,
} from '@mantine/core';
import { IconAlertTriangle, IconArrowRight, IconInfoCircle } from '@tabler/icons-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { isApiError } from '@/api/errors';
import {
  useFleet,
  useFleetVersionOptions,
  useRolloutPolicy,
  useSelectFleetVersion,
} from '@/api/hooks/fleets';
import type { FleetVersionOptions, SelectFleetVersionResponse, VersionOption } from '@/api/types';
import { WaveFlow } from '@/components/PolicyView';
import { ErrorAlert } from '@/components/states';
import { DateTime } from '@/components/TimeAgo';
import { VersionBadge } from '@/components/VersionBadge';
import { VersionSourceBadge } from '@/components/VersionSourceBadge';
import { notifySuccess } from '@/lib/notify';
import { policyLink, rolloutLink } from '@/lib/links';
import { compareVersions } from '@/lib/format';
import { compareReference, highestSelectable, selectionIssueCodes } from '@/lib/updates';

/** Max time the dialog waits for the leader to plan the rollout (planned=false), as in the API contract. */
const WAIT_MS = 30_000;
const POLL_MS = 2_000;

export interface UpdateVersionDialogProps {
  fleet: string;
  /** Pre-selected version (e.g. from the release page); falls back to the highest selectable one. */
  initialVersion?: string;
  opened: boolean;
  onClose: () => void;
}

/** "Auf Version X aktualisieren": pick a version within the Git frame, give a reason, start the rollout. */
export function UpdateVersionDialog(props: UpdateVersionDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal
      opened={props.opened}
      onClose={props.onClose}
      title={t('versionUpdate.dialog.title', { fleet: props.fleet })}
      size="lg"
      trapFocus
      returnFocus
    >
      {/* The body mounts per open, so its state (pick, reason, result) starts fresh every time. */}
      <DialogBody {...props} />
    </Modal>
  );
}

function SelectionError({ error, onConflict }: { error: unknown; onConflict: () => void }) {
  const { t } = useTranslation();
  const conflict = isApiError(error) && error.status === 409;
  // 412: the frame was switched off, the executor is none or a version rollout needs attention since
  // the dialog opened. The options carry the translated reason (notAllowedCode) once reloaded.
  const precondition = isApiError(error) && error.status === 412;
  useEffect(() => {
    if (conflict || precondition) onConflict();
    // onConflict identity changes every render; the effect is meant to run once per error
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [error]);
  if (!error) return null;
  if (precondition) {
    return (
      <Alert
        color="yellow"
        variant="light"
        icon={<IconAlertTriangle size={18} />}
        data-testid="select-precondition"
      >
        {t('versionUpdate.error.precondition')}
      </Alert>
    );
  }
  if (conflict) {
    return (
      <Alert
        color="yellow"
        variant="light"
        icon={<IconAlertTriangle size={18} />}
        data-testid="select-conflict"
      >
        {t('versionUpdate.error.conflict')}
      </Alert>
    );
  }
  const codes = selectionIssueCodes(error);
  if (codes.length > 0) {
    return (
      <Alert
        color="red"
        variant="light"
        icon={<IconAlertTriangle size={18} />}
        title={t('versionUpdate.error.refused')}
        data-testid="select-refused"
      >
        <Stack gap={2}>
          {codes.map((c) => (
            <Text key={c} fz="sm">
              {t(`versionUpdate.error.${c}`)}
            </Text>
          ))}
        </Stack>
      </Alert>
    );
  }
  return <ErrorAlert error={error} />;
}

function OptionRow({
  option,
  distribution,
  reference,
}: {
  option: VersionOption;
  distribution: string;
  reference: string | undefined;
}) {
  const { t } = useTranslation();
  const compare =
    option.breakingCount > 0 && reference && compareVersions(reference, option.version) < 0
      ? `/releases/${encodeURIComponent(distribution)}/${encodeURIComponent(option.version)}?tab=compare&from=${encodeURIComponent(reference)}`
      : undefined;
  return (
    <Stack gap={2}>
      <Group gap={6} wrap="wrap">
        <Text className="myr-mono" fw={600} fz="sm">
          {option.version}
        </Text>
        {option.isLatest && (
          <Badge size="xs" color="teal" variant="light">
            {t('versionUpdate.option.latest')}
          </Badge>
        )}
        {option.isCurrentTarget && (
          <Badge size="xs" color="blue" variant="light">
            {t('versionUpdate.option.currentTarget')}
          </Badge>
        )}
        {option.isGitVersion && (
          <Badge size="xs" color="gray" variant="outline">
            {t('versionUpdate.option.gitTarget')}
          </Badge>
        )}
        {option.isDowngrade && (
          <Badge size="xs" color="orange" variant="light">
            {t('versionUpdate.option.downgrade')}
          </Badge>
        )}
        {option.prerelease && (
          <Badge size="xs" color="gray" variant="outline">
            {t('releases.prerelease')}
          </Badge>
        )}
        {!option.known && (
          <Badge size="xs" color="gray" variant="outline">
            {t('versionUpdate.option.unknown')}
          </Badge>
        )}
        {option.breakingCount > 0 && (
          <Badge size="xs" color="red" variant="filled" data-testid="option-breaking">
            {t('versionUpdate.option.breaking', { count: option.breakingCount })}
          </Badge>
        )}
      </Group>
      <Group gap="md" wrap="wrap">
        {option.publishedAt && (
          <Text fz="xs" c="dimmed">
            {t('releases.published')}: <DateTime value={option.publishedAt} />
          </Text>
        )}
        <Text fz="xs" c="dimmed">
          {t('versionUpdate.option.agentsToUpdate', { count: option.agentsToUpdate })}
        </Text>
        {compare && (
          <Anchor
            href={compare}
            target="_blank"
            rel="noopener noreferrer"
            fz="xs"
            onClick={(e) => e.stopPropagation()}
          >
            {t('versionUpdate.option.compare')}
          </Anchor>
        )}
      </Group>
      {!option.selectable && option.reasonCode && (
        <Text fz="xs" c="orange.8" data-testid={`option-reason-${option.version}`}>
          {t(`enum.versionReason.${option.reasonCode}`)}
        </Text>
      )}
    </Stack>
  );
}

function Summary({ data, version }: { data: FleetVersionOptions; version: string }) {
  const { t } = useTranslation();
  const policy = useRolloutPolicy(data.versionPolicy);
  const running = data.activeRollout?.kind === 'version' ? data.activeRollout : undefined;
  return (
    <Paper withBorder p="sm" radius="md" data-testid="update-summary">
      <Stack gap="xs">
        <Group gap="xs" wrap="wrap">
          <Text fz="sm" fw={600}>
            {t('versionUpdate.summary.title')}
          </Text>
        </Group>
        <Group gap={8} wrap="wrap">
          <Text fz="sm" c="dimmed">
            {t('versionUpdate.summary.currentTarget')}
          </Text>
          {/* hold/none: there is no effective target, the Git version is not in effect */}
          <VersionBadge version={data.target.version} />
          <VersionSourceBadge source={data.target.source} size="xs" />
          <IconArrowRight size={14} aria-hidden />
          <Text className="myr-mono" fw={700} fz="sm">
            {version}
          </Text>
        </Group>
        {data.target.source !== 'git' && data.target.source !== 'ui' && data.target.gitVersion && (
          <Text fz="xs" c="dimmed" data-testid="summary-git-target">
            {t('versionUpdate.summary.gitTarget')}:{' '}
            <span className="myr-mono">{data.target.gitVersion}</span>
          </Text>
        )}
        <Group gap="lg" wrap="wrap">
          <Text fz="sm">
            <Text span c="dimmed">
              {t('fleets.executor')}:{' '}
            </Text>
            {t(`enum.executor.${data.executor}`)}
          </Text>
          <Text fz="sm">
            <Text span c="dimmed">
              {t('fleets.versionPolicy')}:{' '}
            </Text>
            <Anchor component={Link} to={policyLink(data.versionPolicy)} target="_blank" fz="sm">
              {data.versionPolicy}
            </Anchor>
          </Text>
          {data.platforms.length > 0 && (
            <Text fz="sm">
              <Text span c="dimmed">
                {t('versionUpdate.summary.platforms')}:{' '}
              </Text>
              {data.platforms.map((p) => `${p.os}/${p.arch}: ${p.agents}`).join(', ')}
            </Text>
          )}
        </Group>
        {policy.isPending ? (
          <Skeleton height={48} />
        ) : policy.data ? (
          <Stack gap={4}>
            <WaveFlow policy={policy.data} />
            <Group gap="md" wrap="wrap">
              {policy.data.maxConcurrent !== undefined && (
                <Text fz="xs" c="dimmed">
                  {t('policy.maxConcurrent')}: {policy.data.maxConcurrent}
                </Text>
              )}
              {policy.data.healthWait && (
                <Text fz="xs" c="dimmed">
                  {t('policy.healthWait')}: {policy.data.healthWait}
                </Text>
              )}
              <Text fz="xs" c="dimmed">
                {(policy.data.maintenanceWindows ?? []).length > 0
                  ? t('versionUpdate.summary.windowsYes')
                  : t('versionUpdate.summary.windowsNo')}
              </Text>
            </Group>
          </Stack>
        ) : null}
        {running && (
          <Alert color="blue" variant="light" py="xs" icon={<IconInfoCircle size={16} />}>
            {t('versionUpdate.summary.supersedes', { version: running.toRef })}
          </Alert>
        )}
      </Stack>
    </Paper>
  );
}

function DialogBody({ fleet, initialVersion, onClose }: UpdateVersionDialogProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const options = useFleetVersionOptions(fleet);
  const select = useSelectFleetVersion(fleet);
  const [picked, setPicked] = useState<string | null>(null);
  const [reason, setReason] = useState('');
  const [info, setInfo] = useState<string | null>(null);
  const [waiting, setWaiting] = useState(false);
  const [timedOut, setTimedOut] = useState(false);
  const fleetQuery = useFleet(waiting ? fleet : undefined, {
    refetchInterval: waiting && !timedOut ? POLL_MS : false,
  });

  const data = options.data;
  const selectable = (v: string | null | undefined) =>
    !!v && !!data?.options.find((o) => o.version === v && o.selectable);
  const version = selectable(picked)
    ? picked!
    : selectable(initialVersion)
      ? initialVersion!
      : (highestSelectable(data?.options)?.version ?? '');
  const requireReason = !!data?.target.frame?.requireReason;
  const reasonMissing = requireReason && reason.trim() === '';

  const go = (rolloutId: string) => {
    notifySuccess('versionUpdate.dialog.started', { version });
    onClose();
    void navigate(rolloutLink(rolloutId));
  };

  // Waiting for the leader (planned=false): the fleet view gets the rollout id once it is planned.
  const plannedRolloutId = fleetQuery.data?.status.versionTarget.selection?.rolloutId;
  useEffect(() => {
    if (waiting && plannedRolloutId) {
      notifySuccess('versionUpdate.dialog.started', { version });
      onClose();
      void navigate(rolloutLink(plannedRolloutId));
    }
    // version/onClose/navigate are stable for the lifetime of this dialog instance
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [waiting, plannedRolloutId]);
  useEffect(() => {
    if (!waiting) return undefined;
    const id = setTimeout(() => setTimedOut(true), WAIT_MS);
    return () => clearTimeout(id);
  }, [waiting]);

  const handleResult = (res: SelectFleetVersionResponse) => {
    const sel = res.target.selection;
    if (res.rolloutId) return go(res.rolloutId);
    if (res.planned) return setInfo(res.message ?? '');
    if (sel?.version === version && sel.state === 'active') {
      if (sel.rolloutId) return go(sel.rolloutId);
      return setWaiting(true);
    }
    return setInfo(res.message ?? '');
  };

  const submit = () => {
    if (!data || !version) return;
    select.mutate(
      {
        version,
        reason: reason.trim() || undefined,
        expectedTargetVersion: data.target.version ?? '',
      },
      { onSuccess: handleResult },
    );
  };

  if (options.isPending) {
    return (
      <Stack gap="sm" aria-busy="true">
        <Skeleton height={20} width="40%" />
        <Skeleton height={64} />
        <Skeleton height={64} />
      </Stack>
    );
  }
  if (options.error || !data) {
    return (
      <Stack gap="md">
        <ErrorAlert error={options.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.close')}
          </Button>
        </Group>
      </Stack>
    );
  }

  if (waiting) {
    return (
      <Stack gap="md" data-testid="update-waiting">
        <Group gap="sm">
          {!timedOut && <Loader size="sm" />}
          <Text fz="sm">
            {timedOut ? t('versionUpdate.dialog.waitTimeout') : t('versionUpdate.dialog.waiting')}
          </Text>
        </Group>
        <Group justify="flex-end">
          {timedOut && (
            <Button
              component={Link}
              to={`/rollouts?fleet=${encodeURIComponent(fleet)}`}
              variant="light"
              onClick={onClose}
            >
              {t('versionUpdate.dialog.toRollouts')}
            </Button>
          )}
          <Button variant="default" onClick={onClose}>
            {t('common.close')}
          </Button>
        </Group>
      </Stack>
    );
  }

  if (info !== null) {
    return (
      <Stack gap="md" data-testid="update-nothing">
        <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />}>
          <Text fz="sm" fw={600}>
            {t('versionUpdate.dialog.nothingToUpdate')}
          </Text>
          {info && (
            <Text fz="xs" c="dimmed">
              {info}
            </Text>
          )}
        </Alert>
        <Group justify="flex-end">
          <Button onClick={onClose}>{t('common.close')}</Button>
        </Group>
      </Stack>
    );
  }

  const reference = compareReference(data.target);
  return (
    <Stack gap="md">
      {!data.allowed && (
        <Alert
          color="orange"
          variant="light"
          icon={<IconAlertTriangle size={18} />}
          title={t('versionUpdate.dialog.notAllowed')}
          data-testid="not-allowed"
        >
          {data.notAllowedCode ? t(`enum.notAllowed.${data.notAllowedCode}`) : ''}
          {data.notAllowedReason && (
            <Text fz="xs" c="dimmed">
              {data.notAllowedReason}
            </Text>
          )}
        </Alert>
      )}
      <Radio.Group
        value={version}
        onChange={setPicked}
        label={t('versionUpdate.dialog.pick')}
        description={t('versionUpdate.dialog.pickHint')}
      >
        <Stack gap="xs" mt="xs">
          {data.options.map((o) => (
            <Paper
              key={o.version}
              withBorder
              p="sm"
              radius="md"
              style={{
                opacity: o.selectable ? 1 : 0.65,
                borderColor: version === o.version ? 'var(--mantine-color-brand-5)' : undefined,
              }}
            >
              <Radio
                value={o.version}
                disabled={!o.selectable || !data.allowed}
                label={
                  <OptionRow option={o} distribution={data.distribution} reference={reference} />
                }
                styles={{ body: { alignItems: 'flex-start' }, label: { cursor: 'inherit' } }}
              />
            </Paper>
          ))}
          {data.options.length === 0 && (
            <Text c="dimmed" fz="sm">
              {t('versionUpdate.dialog.noOptions')}
            </Text>
          )}
        </Stack>
      </Radio.Group>

      {version && <Summary data={data} version={version} />}

      <Textarea
        label={t('versionUpdate.dialog.reason')}
        description={
          requireReason ? t('versionUpdate.dialog.reasonRequired') : t('common.optional')
        }
        value={reason}
        onChange={(e) => setReason(e.currentTarget.value)}
        maxLength={500}
        required={requireReason}
        autosize
        minRows={2}
        maxRows={5}
        error={undefined}
      />

      <SelectionError error={select.error} onConflict={() => void options.refetch()} />

      <Group justify="flex-end" gap="xs">
        <Button variant="default" onClick={onClose} disabled={select.isPending}>
          {t('common.cancel')}
        </Button>
        <Button
          onClick={submit}
          loading={select.isPending}
          disabled={!data.allowed || !version || reasonMissing}
          data-testid="start-update"
        >
          {version
            ? t('versionUpdate.dialog.start', { version })
            : t('versionUpdate.dialog.startNone')}
        </Button>
      </Group>
    </Stack>
  );
}
