// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Button, Group, Skeleton, Table, Text, VisuallyHidden } from '@mantine/core';
import { IconBell, IconSend } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { useNotificationChannels, useTestNotificationChannel } from '@/api/hooks/admin';
import { Can } from '@/components/Can';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState, ErrorState } from '@/components/states';

export default function NotificationsTab() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useNotificationChannels();
  const test = useTestNotificationChannel();
  if (isPending) return <Skeleton height={200} />;
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;
  return (
    <SectionCard
      padded={false}
      title={t('settings.notifications.title')}
      description={t('settings.notifications.hint')}
    >
      {data.items.length === 0 ? (
        <EmptyState
          icon={<IconBell size={28} />}
          title={t('settings.notifications.empty')}
          text={t('settings.notifications.emptyHint')}
        />
      ) : (
        <div className="myr-scroll-x">
          <Table fz="sm" verticalSpacing="sm">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('common.name')}</Table.Th>
                <Table.Th>{t('common.type')}</Table.Th>
                <Table.Th>{t('settings.notifications.events')}</Table.Th>
                <Table.Th>{t('settings.notifications.minSeverity')}</Table.Th>
                <Table.Th>{t('settings.notifications.target')}</Table.Th>
                <Table.Th>
                  <VisuallyHidden>{t('common.actions')}</VisuallyHidden>
                </Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {data.items.map((c) => (
                <Table.Tr key={c.name}>
                  <Table.Td fw={600}>{c.name}</Table.Td>
                  <Table.Td>
                    <Badge variant="light" color="gray">
                      {t(`enum.channelType.${c.type}`)}
                    </Badge>
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {c.events.map((e) => (
                        <Badge
                          key={e}
                          size="xs"
                          variant="outline"
                          color="gray"
                          className="myr-mono"
                          style={{ textTransform: 'none' }}
                        >
                          {e}
                        </Badge>
                      ))}
                      {c.events.length === 0 && (
                        <Text c="dimmed" fz="sm">
                          {t('common.all')}
                        </Text>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <Badge
                      size="sm"
                      color={
                        c.minSeverity === 'critical'
                          ? 'red'
                          : c.minSeverity === 'warning'
                            ? 'yellow'
                            : 'blue'
                      }
                      variant="light"
                    >
                      {t(`enum.severity.${c.minSeverity}`)}
                    </Badge>
                  </Table.Td>
                  <Table.Td className="myr-mono" fz="xs" style={{ wordBreak: 'break-all' }}>
                    {c.target}
                  </Table.Td>
                  <Table.Td ta="right">
                    <Can permission="notifications:test">
                      <Button
                        size="compact-xs"
                        variant="light"
                        leftSection={<IconSend size={12} />}
                        loading={test.isPending && test.variables === c.name}
                        onClick={() => test.mutate(c.name)}
                      >
                        {t('settings.notifications.test')}
                      </Button>
                    </Can>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </div>
      )}
    </SectionCard>
  );
}
