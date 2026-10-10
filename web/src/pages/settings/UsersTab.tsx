// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Select, Skeleton, Switch, Table, Text, Tooltip } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { ROLES } from '@/api/enums';
import { useUpdateUser, useUsers } from '@/api/hooks/admin';
import type { Role } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { RequirePermission } from '@/components/Can';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState, ErrorState } from '@/components/states';
import { DateTime, TimeAgo } from '@/components/TimeAgo';

function UsersContent() {
  const { t } = useTranslation();
  const { me } = useAuth();
  const { data, isPending, error, refetch } = useUsers();
  const update = useUpdateUser();
  if (isPending) return <Skeleton height={220} />;
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  return (
    <SectionCard
      padded={false}
      title={t('settings.users.title')}
      description={t('settings.users.hint')}
    >
      {data.items.length === 0 ? (
        <EmptyState compact title={t('settings.users.empty')} />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.name')}</Table.Th>
                <Table.Th>{t('settings.users.provider')}</Table.Th>
                <Table.Th>{t('common.role')}</Table.Th>
                <Table.Th>{t('settings.users.roleSource')}</Table.Th>
                <Table.Th>{t('settings.users.lastLogin')}</Table.Th>
                <Table.Th>{t('common.enabled')}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {data.items.map((u) => {
                const self = me?.id === u.id;
                // The bootstrap admin is managed by configuration (auth.bootstrapAdmin): no controls.
                const managed = u.provider === 'bootstrap';
                return (
                  <Table.Tr key={u.id} data-testid="user-row">
                    <Table.Td>
                      <Text fw={600} fz="sm">
                        {u.name || u.subject}
                      </Text>
                      <Text fz="xs" c="dimmed">
                        {u.email ?? u.subject}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Badge variant="outline" color="gray">
                        {u.provider}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      {managed ? (
                        <Text fz="sm">{t(`enum.role.${u.role}`)}</Text>
                      ) : (
                        <Select
                          size="xs"
                          w={170}
                          value={u.role}
                          data={ROLES.map((r) => ({ value: r, label: t(`enum.role.${r}`) }))}
                          allowDeselect={false}
                          disabled={self || update.isPending}
                          aria-label={`${t('common.role')}: ${u.name || u.subject}`}
                          onChange={(r) =>
                            r &&
                            r !== u.role &&
                            update.mutate({ id: u.id, body: { role: r as Role } })
                          }
                        />
                      )}
                    </Table.Td>
                    <Table.Td>
                      {managed ? (
                        <Tooltip label={t('settings.users.bootstrapManagedHint')}>
                          <Badge variant="light" color="orange" data-testid="bootstrap-managed">
                            {t('settings.users.bootstrapManaged')}
                          </Badge>
                        </Tooltip>
                      ) : (
                        <Tooltip
                          label={
                            u.roleSource === 'mapping'
                              ? t('settings.users.mappingHint')
                              : t('settings.users.manualHint')
                          }
                        >
                          <Badge
                            variant="light"
                            color={u.roleSource === 'manual' ? 'violet' : 'gray'}
                          >
                            {t(`enum.roleSource.${u.roleSource}`)}
                          </Badge>
                        </Tooltip>
                      )}
                    </Table.Td>
                    <Table.Td>
                      {u.lastLoginAt ? (
                        <TimeAgo value={u.lastLoginAt} />
                      ) : (
                        <DateTime value={u.createdAt} fallback={t('common.never')} />
                      )}
                    </Table.Td>
                    <Table.Td>
                      {managed ? (
                        <Text fz="sm">
                          {u.disabled ? t('common.disabled') : t('common.enabled')}
                        </Text>
                      ) : (
                        <Switch
                          checked={!u.disabled}
                          disabled={self || update.isPending}
                          aria-label={`${t('common.enabled')}: ${u.name || u.subject}`}
                          onChange={(e) =>
                            update.mutate({
                              id: u.id,
                              body: { disabled: !e.currentTarget.checked },
                            })
                          }
                        />
                      )}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </div>
      )}
    </SectionCard>
  );
}

export default function UsersTab() {
  return (
    <RequirePermission permission="users:manage">
      <UsersContent />
    </RequirePermission>
  );
}
