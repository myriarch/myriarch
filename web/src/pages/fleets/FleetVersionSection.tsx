// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Alert,
  Anchor,
  Badge,
  Box,
  Button,
  Group,
  Menu,
  Stack,
  Table,
  Text,
  Tooltip,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconArrowBackUp,
  IconDotsVertical,
  IconGitBranch,
  IconRocket,
} from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useAuditEvents } from '@/api/hooks/admin';
import { useClearFleetVersionSelection, useFleetVersionOptions } from '@/api/hooks/fleets';
import type { FleetDetail } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { KeyValue } from '@/components/misc';
import { RolloutOriginBadge, VersionSourceBadge } from '@/components/VersionSourceBadge';
import { RolloutProgress } from '@/components/RolloutProgress';
import { SectionCard } from '@/components/SectionCard';
import { RolloutStatusBadge } from '@/components/StatusBadge';
import { DateTime, TimeAgo } from '@/components/TimeAgo';
import { VersionBadge } from '@/components/VersionBadge';
import { policyLink, rolloutLink } from '@/lib/links';
import { clearWouldDowngrade, highestSelectable } from '@/lib/updates';
import { UpdateVersionDialog } from './UpdateVersionDialog';

/** Recent `fleet.version.*` audit entries of the fleet (who, when, what, why). Needs audit:read. */
function VersionHistory({ fleet }: { fleet: string }) {
  const { t } = useTranslation();
  const q = useAuditEvents({
    targetType: 'fleet',
    targetId: fleet,
    action: 'fleet.version.*',
    limit: 5,
  });
  const items = q.data?.items ?? [];
  const link = `/audit?targetType=fleet&targetId=${encodeURIComponent(fleet)}&action=${encodeURIComponent('fleet.version.*')}`;
  return (
    <Stack gap={4} data-testid="version-history">
      <Group justify="space-between">
        <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
          {t('versionUpdate.history.title')}
        </Text>
        <Anchor component={Link} to={link} fz="xs">
          {t('versionUpdate.history.all')}
        </Anchor>
      </Group>
      {items.length === 0 ? (
        <Text fz="sm" c="dimmed">
          {q.isPending ? t('common.loading') : t('versionUpdate.history.empty')}
        </Text>
      ) : (
        <Table fz="xs" verticalSpacing={4}>
          <Table.Tbody>
            {items.map((e) => {
              const version =
                typeof e.details?.['version'] === 'string' ? e.details['version'] : '';
              const reason = typeof e.details?.['reason'] === 'string' ? e.details['reason'] : '';
              return (
                <Table.Tr key={e.id}>
                  <Table.Td w={110}>
                    <TimeAgo value={e.time} />
                  </Table.Td>
                  <Table.Td>{e.actorName}</Table.Td>
                  <Table.Td>
                    {t(`versionUpdate.history.actions.${e.action.replace('fleet.version.', '')}`, {
                      defaultValue: e.action,
                    })}
                    {e.details?.['noop'] === true && (
                      <Badge ml={6} size="xs" color="gray" variant="light">
                        {t('versionUpdate.history.noop')}
                      </Badge>
                    )}
                    {e.result === 'failure' && (
                      <Badge ml={6} size="xs" color="red" variant="light">
                        {t('enum.auditResult.failure')}
                      </Badge>
                    )}
                  </Table.Td>
                  <Table.Td className="myr-mono">{version}</Table.Td>
                  <Table.Td c="dimmed">{reason}</Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}

/**
 * "Zielversion": the effective target (Git or UI), the Git frame, the update button, the running
 * version rollouts and the history. The UI chooses within the frame that Git defines.
 */
export function FleetVersionSection({ detail }: { detail: FleetDetail }) {
  const { t } = useTranslation();
  const { can } = useAuth();
  const { fleet, status, versionPolicy } = detail;
  const vt = status.versionTarget;
  const frame = vt.frame;
  const sel = vt.selection;
  const canSet = can('fleets:set_version');
  const options = useFleetVersionOptions(fleet.name, { enabled: canSet });
  const clear = useClearFleetVersionSelection(fleet.name);
  const best = highestSelectable(options.data?.options);
  const [updateOpen, setUpdateOpen] = useState(false);
  const [clearOpen, setClearOpen] = useState(false);
  const versionRollouts = status.activeRollouts.filter((r) => r.kind === 'version');
  const downgrade = clearWouldDowngrade(
    vt.gitVersion,
    status.versions.map((v) => v.version),
  );

  // The target is set (UI or Git) but collectors still run something else and no version rollout
  // is on its way (host rollback, paused/failed/aborted rollout): "up to date" would be wrong.
  const notReached =
    vt.version !== undefined && vt.version !== ''
      ? options.data?.options.find((o) => o.isCurrentTarget && o.agentsToUpdate > 0)
      : undefined;
  const rolloutMoving = versionRollouts.some(
    (r) =>
      r.status === 'running' ||
      r.status === 'pending' ||
      r.status === 'waiting_approval' ||
      r.status === 'rolling_back',
  );
  const showNotReached = !!notReached && !rolloutMoving;

  let disabledReason: string | undefined;
  if (options.data && !options.data.allowed)
    disabledReason = options.data.notAllowedCode
      ? t(`enum.notAllowed.${options.data.notAllowedCode}`)
      : (options.data.notAllowedReason ?? t('versionUpdate.section.notAllowed'));
  else if (options.data && !best)
    disabledReason = notReached
      ? t('versionUpdate.section.notReachedShort')
      : t('versionUpdate.section.upToDate');

  const openClear = () => {
    clear.reset();
    setClearOpen(true);
  };

  const effective = (() => {
    switch (vt.source) {
      case 'git':
        return (
          <Group gap={8} wrap="wrap">
            <VersionBadge version={vt.version} size="md" />
            <VersionSourceBadge source="git" />
          </Group>
        );
      case 'ui':
        return (
          <Stack gap={4}>
            <Group gap={8} wrap="wrap">
              <VersionBadge version={vt.version} size="md" />
              <VersionSourceBadge source="ui" />
            </Group>
            {sel && (
              <Text fz="sm" data-testid="selection-info">
                {t('versionUpdate.section.selectedBy', { by: sel.selectedBy })}{' '}
                <TimeAgo value={sel.selectedAt} />
                {' · '}
                <DateTime value={sel.selectedAt} />
                {sel.reason ? (
                  <>
                    {' · '}
                    {t('versionUpdate.section.reason')}: {sel.reason}
                  </>
                ) : null}
              </Text>
            )}
            <Group gap="md" wrap="wrap">
              <Text fz="xs" c="dimmed">
                {t('versionUpdate.section.gitTarget')}:{' '}
                <span className="myr-mono">{vt.gitVersion ?? '–'}</span>
              </Text>
              {sel?.rolloutId && (
                <Anchor component={Link} to={rolloutLink(sel.rolloutId)} fz="xs">
                  {t('versionUpdate.section.toRollout')}
                </Anchor>
              )}
            </Group>
          </Stack>
        );
      case 'hold':
        return (
          <Alert
            color="orange"
            variant="light"
            icon={<IconAlertTriangle size={18} />}
            title={t('versionUpdate.section.holdTitle')}
            data-testid="hold-alert"
          >
            <Stack gap="xs">
              <Text fz="sm">
                {t('versionUpdate.section.holdText', {
                  version: sel?.version ?? '?',
                  reason: sel?.stateReason ?? '–',
                })}
              </Text>
              <Group gap="xs">
                {canSet && (
                  <>
                    <Button
                      size="xs"
                      variant="default"
                      onClick={() => setUpdateOpen(true)}
                      disabled={!best}
                    >
                      {t('versionUpdate.section.pickOther')}
                    </Button>
                    <Button
                      size="xs"
                      variant="default"
                      leftSection={<IconArrowBackUp size={14} />}
                      onClick={openClear}
                    >
                      {t('versionUpdate.section.clearGitApplies')}
                    </Button>
                  </>
                )}
              </Group>
            </Stack>
          </Alert>
        );
      case 'none':
        return (
          <Text c="dimmed" data-testid="version-source-none">
            {t('versionUpdate.section.noTarget')}
          </Text>
        );
    }
  })();

  const policyName = frame?.rolloutPolicy || versionPolicy.name;

  return (
    <>
      <SectionCard
        title={t('versionUpdate.section.title')}
        description={t('versionUpdate.section.hint')}
        actions={
          canSet ? (
            <Group gap={6} wrap="nowrap">
              <Tooltip label={disabledReason} disabled={!disabledReason} multiline maw={320}>
                <Box>
                  <Button
                    size="xs"
                    leftSection={<IconRocket size={14} />}
                    disabled={!best || !options.data?.allowed}
                    loading={options.isPending}
                    onClick={() => setUpdateOpen(true)}
                    data-testid="update-to-version"
                  >
                    {best
                      ? t('versionUpdate.section.updateTo', { version: best.version })
                      : t('versionUpdate.section.update')}
                  </Button>
                </Box>
              </Tooltip>
              {sel && (
                <Menu position="bottom-end" width={260}>
                  <Menu.Target>
                    <ActionIcon
                      variant="default"
                      size="md"
                      aria-label={t('versionUpdate.section.moreActions')}
                    >
                      <IconDotsVertical size={16} />
                    </ActionIcon>
                  </Menu.Target>
                  <Menu.Dropdown>
                    <Menu.Item leftSection={<IconArrowBackUp size={14} />} onClick={openClear}>
                      {t('versionUpdate.section.clear')}
                    </Menu.Item>
                  </Menu.Dropdown>
                </Menu>
              )}
            </Group>
          ) : undefined
        }
      >
        <Stack gap="md">
          {effective}

          {showNotReached && notReached && (
            <Alert
              color="orange"
              variant="light"
              icon={<IconAlertTriangle size={18} />}
              title={t('versionUpdate.section.notReachedTitle', { version: notReached.version })}
              data-testid="target-not-reached"
            >
              <Stack gap="xs">
                <Text fz="sm">
                  {t('versionUpdate.section.notReachedText', {
                    version: notReached.version,
                    count: notReached.agentsToUpdate,
                  })}
                </Text>
                <Group gap="md" wrap="wrap">
                  {(sel?.rolloutId ?? versionRollouts[0]?.id) && (
                    <Anchor
                      component={Link}
                      to={rolloutLink((sel?.rolloutId ?? versionRollouts[0]?.id) as string)}
                      fz="sm"
                    >
                      {t('versionUpdate.section.toRollout')}
                    </Anchor>
                  )}
                  {canSet && sel && (
                    <Button
                      size="xs"
                      variant="default"
                      leftSection={<IconArrowBackUp size={14} />}
                      onClick={openClear}
                      data-testid="not-reached-clear"
                    >
                      {t('versionUpdate.section.notReachedClear')}
                    </Button>
                  )}
                </Group>
              </Stack>
            </Alert>
          )}

          <KeyValue
            labelWidth={150}
            items={[
              {
                label: t('versionUpdate.frame.title'),
                value: frame ? (
                  <Stack gap={4}>
                    <Group gap={6} wrap="wrap">
                      <Badge
                        size="sm"
                        color={frame.enabled ? 'teal' : 'gray'}
                        variant="light"
                        data-testid="frame-enabled"
                      >
                        {frame.enabled
                          ? t('versionUpdate.frame.allowed')
                          : t('versionUpdate.frame.notAllowed')}
                      </Badge>
                      {frame.versionRange && (
                        <Badge variant="outline" color="gray" className="myr-mono" size="sm">
                          {frame.versionRange}
                        </Badge>
                      )}
                      {(frame.versions ?? []).map((v) => (
                        <Badge
                          key={v}
                          variant="outline"
                          color="gray"
                          className="myr-mono"
                          size="sm"
                        >
                          {v}
                        </Badge>
                      ))}
                    </Group>
                    <Text fz="xs" c="dimmed">
                      {t('versionUpdate.frame.downgrade')}:{' '}
                      {frame.allowDowngrade ? t('common.yes') : t('common.no')}
                      {' · '}
                      {t('versionUpdate.frame.requireReason')}:{' '}
                      {frame.requireReason ? t('common.yes') : t('common.no')}
                    </Text>
                  </Stack>
                ) : (
                  <Group gap={6} align="flex-start" wrap="nowrap">
                    <IconGitBranch size={14} style={{ marginTop: 3 }} aria-hidden />
                    <Text fz="sm" c="dimmed" data-testid="frame-missing">
                      {t('versionUpdate.frame.missing')}
                    </Text>
                  </Group>
                ),
              },
              {
                label: t('fleets.versionPolicy'),
                value: (
                  <Anchor component={Link} to={policyLink(policyName)} fz="sm">
                    {policyName}
                  </Anchor>
                ),
              },
              {
                label: t('versionUpdate.section.gitTarget'),
                value: <VersionBadge version={vt.gitVersion} />,
                hidden: vt.source === 'ui',
              },
            ]}
          />

          {!canSet && frame?.enabled && (
            <Text fz="xs" c="dimmed">
              {t('versionUpdate.section.needsOperator')}
            </Text>
          )}

          {versionRollouts.length > 0 && (
            <Stack gap={4} data-testid="active-version-rollouts">
              <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
                {t('versionUpdate.section.activeRollout')}
              </Text>
              <Table fz="sm" verticalSpacing="xs">
                <Table.Tbody>
                  {versionRollouts.map((r) => (
                    <Table.Tr key={r.id}>
                      <Table.Td>
                        <Group gap={6} wrap="nowrap">
                          <span className="myr-mono">
                            {r.fromRef ?? '?'} → {r.toRef}
                          </span>
                          <RolloutOriginBadge origin={r.origin} />
                        </Group>
                      </Table.Td>
                      <Table.Td w="30%">
                        <RolloutProgress counts={r.counts} />
                      </Table.Td>
                      <Table.Td>
                        <RolloutStatusBadge status={r.status} />
                      </Table.Td>
                      <Table.Td ta="right">
                        <Anchor component={Link} to={rolloutLink(r.id)} fz="sm">
                          {t('versionUpdate.section.viewFlow')}
                        </Anchor>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Stack>
          )}

          {can('audit:read') && <VersionHistory fleet={fleet.name} />}
        </Stack>
      </SectionCard>

      <UpdateVersionDialog
        fleet={fleet.name}
        opened={updateOpen}
        onClose={() => setUpdateOpen(false)}
      />
      <ConfirmDialog
        opened={clearOpen}
        onClose={() => setClearOpen(false)}
        title={t('versionUpdate.clear.title')}
        confirmLabel={t('versionUpdate.clear.confirm')}
        loading={clear.isPending}
        error={clear.error}
        reason={{
          label: t('versionUpdate.dialog.reason'),
          required: !!frame?.requireReason,
        }}
        onConfirm={(reason) => clear.mutate({ reason }, { onSuccess: () => setClearOpen(false) })}
      >
        <Stack gap="xs">
          <Text fz="sm">
            {vt.gitVersion
              ? t('versionUpdate.clear.text', { gitVersion: vt.gitVersion })
              : t('versionUpdate.clear.textNoGit')}
          </Text>
          {downgrade && (
            <Alert color="orange" variant="light" icon={<IconAlertTriangle size={16} />} py="xs">
              {t('versionUpdate.clear.downgrade', { gitVersion: vt.gitVersion })}
            </Alert>
          )}
        </Stack>
      </ConfirmDialog>
    </>
  );
}
