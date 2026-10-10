// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Accordion, Badge, Group, Skeleton, Stack, Table, Text, TextInput } from '@mantine/core';
import { IconSearch } from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAgentComponents } from '@/api/hooks/agents';
import { isApiError } from '@/api/errors';
import { EmptyState, ErrorState } from '@/components/states';
import { SectionCard } from '@/components/SectionCard';

const KIND_ORDER = ['receivers', 'processors', 'exporters', 'connectors', 'extensions'];

/** Components reported by the agent, grouped by kind and searchable (type, module, metadata). */
export function ComponentsTab({ agentId }: { agentId: string }) {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useAgentComponents(agentId);
  const [q, setQ] = useState('');

  const groups = useMemo(() => {
    if (!data) return [];
    const needle = q.trim().toLowerCase();
    return Object.entries(data.components)
      .sort(([a], [b]) => {
        const ia = KIND_ORDER.indexOf(a);
        const ib = KIND_ORDER.indexOf(b);
        return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || a.localeCompare(b);
      })
      .map(([kind, byType]) => {
        const rows = Object.entries(byType)
          .filter(([type, d]) => {
            if (!needle) return true;
            return (
              type.toLowerCase().includes(needle) ||
              (d.module ?? '').toLowerCase().includes(needle) ||
              Object.values(d.metadata ?? {}).some((v) => v.toLowerCase().includes(needle))
            );
          })
          .sort(([a], [b]) => a.localeCompare(b));
        return { kind, rows };
      })
      .filter((g) => g.rows.length > 0);
  }, [data, q]);

  if (isPending) return <Skeleton height={240} />;
  if (isApiError(error) && error.status === 404) {
    return (
      <SectionCard>
        <EmptyState
          title={t('agentDetail.components.none')}
          text={t('agentDetail.components.noneHint')}
        />
      </SectionCard>
    );
  }
  if (error) return <ErrorState error={error} onRetry={() => void refetch()} />;

  const total = Object.values(data.components).reduce(
    (s, byType) => s + Object.keys(byType).length,
    0,
  );

  return (
    <Stack gap="md">
      <Group justify="space-between">
        <TextInput
          leftSection={<IconSearch size={16} />}
          placeholder={t('agentDetail.components.search')}
          aria-label={t('agentDetail.components.search')}
          value={q}
          onChange={(e) => setQ(e.currentTarget.value)}
          w={{ base: '100%', sm: 360 }}
        />
        <Text fz="sm" c="dimmed">
          {t('agentDetail.components.total', { count: total })}
        </Text>
      </Group>
      {groups.length === 0 ? (
        <SectionCard>
          <EmptyState
            compact
            title={
              total === 0 ? t('agentDetail.components.none') : t('agentDetail.components.noMatch')
            }
          />
        </SectionCard>
      ) : (
        <Accordion
          multiple
          defaultValue={q ? groups.map((g) => g.kind) : ['receivers', 'exporters']}
          variant="separated"
          radius="md"
        >
          {groups.map((g) => (
            <Accordion.Item key={g.kind} value={g.kind}>
              <Accordion.Control>
                <Group gap={8}>
                  <Text fw={600} tt="capitalize">
                    {g.kind}
                  </Text>
                  <Badge size="sm" variant="light" color="gray">
                    {g.rows.length}
                  </Badge>
                </Group>
              </Accordion.Control>
              <Accordion.Panel>
                <div className="myr-scroll-x">
                  <Table fz="sm" verticalSpacing={4}>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>{t('common.type')}</Table.Th>
                        <Table.Th>{t('agentDetail.components.module')}</Table.Th>
                        <Table.Th>{t('agentDetail.components.metadata')}</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {g.rows.map(([type, d]) => (
                        <Table.Tr key={type}>
                          <Table.Td className="myr-mono">{type}</Table.Td>
                          <Table.Td className="myr-mono" style={{ wordBreak: 'break-all' }}>
                            {d.module ?? '–'}
                          </Table.Td>
                          <Table.Td>
                            <Group gap={4} wrap="wrap">
                              {Object.entries(d.metadata ?? {}).map(([k, v]) => (
                                <Badge
                                  key={k}
                                  size="xs"
                                  variant="outline"
                                  color="gray"
                                  styles={{ label: { textTransform: 'none' } }}
                                >
                                  {k}: {v}
                                </Badge>
                              ))}
                            </Group>
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </div>
              </Accordion.Panel>
            </Accordion.Item>
          ))}
        </Accordion>
      )}
    </Stack>
  );
}
