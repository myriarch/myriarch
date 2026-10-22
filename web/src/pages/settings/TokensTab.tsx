// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Code,
  Group,
  Modal,
  Select,
  Skeleton,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  Tooltip,
  VisuallyHidden,
} from '@mantine/core';
import { DateTimePicker } from '@mantine/dates';
import { useDisclosure } from '@mantine/hooks';
import {
  IconAlertTriangle,
  IconKey,
  IconPlus,
  IconServerCog,
  IconTrash,
} from '@tabler/icons-react';
import dayjs from 'dayjs';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ROLES } from '@/api/enums';
import {
  useApiTokens,
  useCreateApiToken,
  useCreateEnrollmentToken,
  useEnrollmentTokens,
  useRevokeApiToken,
  useRevokeEnrollmentToken,
} from '@/api/hooks/admin';
import type { APIToken, EnrollmentToken, Role } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { AddHostDialog } from '@/components/AddHostDialog';
import { CopyIconButton } from '@/components/CopyButton';
import { KeyValueEditor } from '@/components/KeyValueEditor';
import { rowsToRecord, type KVRow } from '@/components/kv';
import { SectionCard } from '@/components/SectionCard';
import { StatusBadge } from '@/components/StatusBadge';
import { EmptyState, ErrorAlert, ErrorState, NoPermission } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';

function tokenState(tok: {
  revokedAt?: string;
  expiresAt?: string;
}): 'revoked' | 'expired' | 'active' {
  if (tok.revokedAt) return 'revoked';
  if (tok.expiresAt && new Date(tok.expiresAt).getTime() < Date.now()) return 'expired';
  return 'active';
}

function StateBadge({ tok }: { tok: { revokedAt?: string; expiresAt?: string } }) {
  const { t } = useTranslation();
  const s = tokenState(tok);
  return (
    <StatusBadge
      tone={s === 'active' ? 'success' : s === 'expired' ? 'warning' : 'neutral'}
      label={t(`settings.tokens.state.${s}`)}
    />
  );
}

function toIso(v: string | null): string | undefined {
  if (!v) return undefined;
  const d = dayjs(v);
  return d.isValid() ? d.toISOString() : undefined;
}

/** Shown exactly once after creation; the secret lives only in this component's props. */
function SecretDialog({
  secret,
  kind,
  onClose,
}: {
  secret: string | null;
  kind: 'api' | 'enrollment';
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [addHostOpen, addHost] = useDisclosure(false);
  return (
    <Modal
      opened={secret !== null}
      onClose={onClose}
      title={t('settings.tokens.secretTitle')}
      size="lg"
      closeOnClickOutside={false}
    >
      {secret && (
        <Stack gap="md">
          <Alert color="yellow" variant="light" icon={<IconAlertTriangle size={18} />}>
            {t('settings.tokens.secretWarning')}
          </Alert>
          <Group gap="xs" wrap="nowrap">
            <Code block style={{ flex: 1, wordBreak: 'break-all' }} data-testid="token-secret">
              {secret}
            </Code>
            <CopyIconButton text={secret} />
          </Group>
          {kind === 'enrollment' && (
            <Stack gap="xs">
              <Text fz="sm">{t('settings.tokens.addHostHint')}</Text>
              <Group>
                <Button
                  variant="light"
                  size="xs"
                  leftSection={<IconServerCog size={14} />}
                  onClick={addHost.open}
                >
                  {t('settings.tokens.addHostWithToken')}
                </Button>
              </Group>
            </Stack>
          )}
          <Group justify="flex-end">
            <Button onClick={onClose}>{t('settings.tokens.secretDone')}</Button>
          </Group>
        </Stack>
      )}
      {secret && kind === 'enrollment' && (
        <AddHostDialog opened={addHostOpen} onClose={addHost.close} freshToken={secret} />
      )}
    </Modal>
  );
}

function ApiTokensSection() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useApiTokens();
  const create = useCreateApiToken();
  const revoke = useRevokeApiToken();
  const [open, dlg] = useDisclosure(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<Role>('viewer');
  const [expires, setExpires] = useState<string | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const [toRevoke, setToRevoke] = useState<APIToken | null>(null);

  const submit = () =>
    create.mutate(
      { name: name.trim(), role, expiresAt: toIso(expires) },
      {
        onSuccess: (res) => {
          setSecret(res.secret);
          dlg.close();
          setName('');
          setExpires(null);
          setRole('viewer');
        },
      },
    );

  return (
    <SectionCard
      padded={false}
      title={t('settings.tokens.apiTitle')}
      description={t('settings.tokens.apiHint')}
      actions={
        <Button size="xs" leftSection={<IconPlus size={14} />} onClick={dlg.open}>
          {t('settings.tokens.create')}
        </Button>
      }
    >
      {isPending ? (
        <Skeleton height={140} m="md" />
      ) : error ? (
        <ErrorState compact error={error} onRetry={() => void refetch()} />
      ) : data.items.length === 0 ? (
        <EmptyState compact icon={<IconKey size={22} />} title={t('settings.tokens.apiEmpty')} />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.name')}</Table.Th>
                <Table.Th>{t('settings.tokens.prefix')}</Table.Th>
                <Table.Th>{t('common.role')}</Table.Th>
                <Table.Th>{t('common.status')}</Table.Th>
                <Table.Th>{t('common.created')}</Table.Th>
                <Table.Th>{t('common.expires')}</Table.Th>
                <Table.Th>{t('common.lastUsed')}</Table.Th>
                <Table.Th>
                  <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
                </Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {data.items.map((tok) => (
                <Table.Tr key={tok.id}>
                  <Table.Td fw={600}>
                    {tok.name}
                    <Text fz="xs" c="dimmed">
                      {tok.createdBy}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Code>{tok.displayPrefix}…</Code>
                  </Table.Td>
                  <Table.Td>
                    <Badge variant="light">{t(`enum.role.${tok.role}`)}</Badge>
                  </Table.Td>
                  <Table.Td>
                    <StateBadge tok={tok} />
                  </Table.Td>
                  <Table.Td>
                    <DateTime value={tok.createdAt} />
                  </Table.Td>
                  <Table.Td>
                    <DateTime value={tok.expiresAt} fallback={t('settings.tokens.noExpiry')} />
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={tok.lastUsedAt} fallback={t('common.never')} />
                  </Table.Td>
                  <Table.Td ta="right">
                    {!tok.revokedAt && (
                      <Tooltip label={t('settings.tokens.revoke')}>
                        <ActionIcon
                          variant="subtle"
                          color="red"
                          onClick={() => setToRevoke(tok)}
                          aria-label={`${t('settings.tokens.revoke')}: ${tok.name}`}
                        >
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Tooltip>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </div>
      )}

      <Modal opened={open} onClose={dlg.close} title={t('settings.tokens.createApiTitle')}>
        <Stack gap="md">
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            maxLength={100}
            data-autofocus
          />
          <Select
            label={t('common.role')}
            value={role}
            onChange={(v) => v && setRole(v as Role)}
            data={ROLES.map((r) => ({
              value: r,
              label: `${t(`enum.role.${r}`)} – ${t(`enum.roleHint.${r}`)}`,
            }))}
            allowDeselect={false}
          />
          <DateTimePicker
            label={t('settings.tokens.expiresOptional')}
            clearable
            value={expires}
            onChange={setExpires}
            minDate={new Date()}
            valueFormat="DD.MM.YYYY HH:mm"
          />
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={dlg.close}>
              {t('common.cancel')}
            </Button>
            <Button onClick={submit} loading={create.isPending} disabled={!name.trim()}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      </Modal>
      <SecretDialog secret={secret} kind="api" onClose={() => setSecret(null)} />
      <ConfirmDialog
        opened={toRevoke !== null}
        onClose={() => setToRevoke(null)}
        color="red"
        title={t('settings.tokens.revokeTitle')}
        confirmLabel={t('settings.tokens.revoke')}
        loading={revoke.isPending}
        onConfirm={() =>
          toRevoke && revoke.mutate(toRevoke.id, { onSuccess: () => setToRevoke(null) })
        }
      >
        <Text fz="sm">{t('settings.tokens.revokeApiText', { name: toRevoke?.name ?? '' })}</Text>
      </ConfirmDialog>
    </SectionCard>
  );
}

function EnrollmentTokensSection() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useEnrollmentTokens();
  const create = useCreateEnrollmentToken();
  const revoke = useRevokeEnrollmentToken();
  const [open, dlg] = useDisclosure(false);
  const [name, setName] = useState('');
  const [labels, setLabels] = useState<KVRow[]>([]);
  const [expires, setExpires] = useState<string | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const [addHostOpen, addHost] = useDisclosure(false);
  const [toRevoke, setToRevoke] = useState<EnrollmentToken | null>(null);

  const submit = () => {
    const l = rowsToRecord(labels);
    create.mutate(
      {
        name: name.trim(),
        labels: Object.keys(l).length ? l : undefined,
        expiresAt: toIso(expires),
      },
      {
        onSuccess: (res) => {
          setSecret(res.secret);
          dlg.close();
          setName('');
          setLabels([]);
          setExpires(null);
        },
      },
    );
  };

  return (
    <SectionCard
      padded={false}
      title={t('settings.tokens.enrollmentTitle')}
      description={t('settings.tokens.enrollmentHint')}
      actions={
        <Group gap="xs">
          <Button
            size="xs"
            variant="default"
            leftSection={<IconServerCog size={14} />}
            onClick={addHost.open}
          >
            {t('addHost.title')}
          </Button>
          <Button size="xs" leftSection={<IconPlus size={14} />} onClick={dlg.open}>
            {t('settings.tokens.create')}
          </Button>
        </Group>
      }
    >
      {isPending ? (
        <Skeleton height={140} m="md" />
      ) : error ? (
        <ErrorState compact error={error} onRetry={() => void refetch()} />
      ) : data.items.length === 0 ? (
        <EmptyState
          compact
          icon={<IconKey size={22} />}
          title={t('settings.tokens.enrollmentEmpty')}
          text={t('settings.tokens.enrollmentEmptyHint')}
        />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.name')}</Table.Th>
                <Table.Th>{t('settings.tokens.prefix')}</Table.Th>
                <Table.Th>{t('common.status')}</Table.Th>
                <Table.Th>Labels</Table.Th>
                <Table.Th ta="right">{t('settings.tokens.uses')}</Table.Th>
                <Table.Th>{t('common.expires')}</Table.Th>
                <Table.Th>{t('common.lastUsed')}</Table.Th>
                <Table.Th>
                  <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
                </Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {data.items.map((tok) => (
                <Table.Tr key={tok.id}>
                  <Table.Td fw={600}>
                    <Group gap={6} wrap="nowrap">
                      {tok.name}
                      {tok.createdBy === 'config' && (
                        <Tooltip label={t('settings.tokens.configBadgeHint')} multiline w={280}>
                          <Badge
                            size="xs"
                            variant="light"
                            color="violet"
                            data-testid="config-badge"
                          >
                            {t('settings.tokens.configBadge')}
                          </Badge>
                        </Tooltip>
                      )}
                    </Group>
                    <Text fz="xs" c="dimmed">
                      {tok.createdBy} · <DateTime value={tok.createdAt} />
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Code>{tok.displayPrefix}…</Code>
                  </Table.Td>
                  <Table.Td>
                    <StateBadge tok={tok} />
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {Object.entries(tok.labels ?? {}).map(([k, v]) => (
                        <Badge
                          key={k}
                          size="xs"
                          variant="outline"
                          color="gray"
                          style={{ textTransform: 'none' }}
                        >
                          {k}={v}
                        </Badge>
                      ))}
                    </Group>
                  </Table.Td>
                  <Table.Td ta="right" className="myr-tabular">
                    {tok.useCount}
                  </Table.Td>
                  <Table.Td>
                    <DateTime value={tok.expiresAt} fallback={t('settings.tokens.noExpiry')} />
                  </Table.Td>
                  <Table.Td>
                    <TimeAgo value={tok.lastUsedAt} fallback={t('common.never')} />
                  </Table.Td>
                  <Table.Td ta="right">
                    {!tok.revokedAt && (
                      <Tooltip label={t('settings.tokens.revoke')}>
                        <ActionIcon
                          variant="subtle"
                          color="red"
                          onClick={() => setToRevoke(tok)}
                          aria-label={`${t('settings.tokens.revoke')}: ${tok.name}`}
                        >
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Tooltip>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </div>
      )}

      <Modal opened={open} onClose={dlg.close} title={t('settings.tokens.createEnrollmentTitle')}>
        <Stack gap="md">
          <TextInput
            label={t('common.name')}
            description={t('settings.tokens.enrollmentNameHint')}
            value={name}
            onChange={(e) => setName(e.currentTarget.value)}
            maxLength={100}
            data-autofocus
          />
          <Stack gap={4}>
            <Text fz="sm" fw={500}>
              Labels
            </Text>
            <Text fz="xs" c="dimmed">
              {t('settings.tokens.labelsHint')}
            </Text>
            <KeyValueEditor
              rows={labels}
              onChange={setLabels}
              keyPlaceholder="pilot"
              valuePlaceholder="true"
              addLabel={t('settings.tokens.addLabel')}
            />
          </Stack>
          <DateTimePicker
            label={t('settings.tokens.expiresOptional')}
            clearable
            value={expires}
            onChange={setExpires}
            minDate={new Date()}
            valueFormat="DD.MM.YYYY HH:mm"
          />
          <ErrorAlert error={create.error} />
          <Group justify="flex-end">
            <Button variant="default" onClick={dlg.close}>
              {t('common.cancel')}
            </Button>
            <Button onClick={submit} loading={create.isPending} disabled={!name.trim()}>
              {t('common.create')}
            </Button>
          </Group>
        </Stack>
      </Modal>
      <AddHostDialog opened={addHostOpen} onClose={addHost.close} />
      <SecretDialog secret={secret} kind="enrollment" onClose={() => setSecret(null)} />
      <ConfirmDialog
        opened={toRevoke !== null}
        onClose={() => setToRevoke(null)}
        color="red"
        title={t('settings.tokens.revokeTitle')}
        confirmLabel={t('settings.tokens.revoke')}
        loading={revoke.isPending}
        onConfirm={() =>
          toRevoke && revoke.mutate(toRevoke.id, { onSuccess: () => setToRevoke(null) })
        }
      >
        <Text fz="sm">
          {t('settings.tokens.revokeEnrollmentText', { name: toRevoke?.name ?? '' })}
        </Text>
        {toRevoke?.createdBy === 'config' && (
          <Text fz="sm" mt="xs">
            {t('settings.tokens.configBadgeHint')}
          </Text>
        )}
      </ConfirmDialog>
    </SectionCard>
  );
}

export default function TokensTab() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canApi = can('tokens:manage');
  const canEnroll = can('enrollment:manage');
  const [tab, setTab] = useState<string>(canEnroll ? 'enrollment' : 'api');
  if (!canApi && !canEnroll) return <NoPermission permission="tokens:manage" />;
  return (
    <Stack gap="md">
      <Tabs value={tab} onChange={(v) => v && setTab(v)} keepMounted={false}>
        <Tabs.List mb="md">
          {canEnroll && (
            <Tabs.Tab value="enrollment">{t('settings.tokens.enrollmentTab')}</Tabs.Tab>
          )}
          {canApi && <Tabs.Tab value="api">{t('settings.tokens.apiTab')}</Tabs.Tab>}
        </Tabs.List>
        {canEnroll && (
          <Tabs.Panel value="enrollment">
            <EnrollmentTokensSection />
          </Tabs.Panel>
        )}
        {canApi && (
          <Tabs.Panel value="api">
            <ApiTokensSection />
          </Tabs.Panel>
        )}
      </Tabs>
    </Stack>
  );
}
