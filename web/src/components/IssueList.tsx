// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Code, Group, Stack, Table, Text } from '@mantine/core';
import { IconCircleCheck } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import type { Issue } from '@/api/types';
import { SeverityBadge } from './StatusBadge';
import { EmptyState } from './states';

/** Validation/render issues: severity, code, message, location (file:line / path / object). */
export function IssueList({
  issues,
  emptyText,
  compact,
}: {
  issues: Issue[];
  emptyText?: string;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  if (issues.length === 0) {
    return (
      <EmptyState
        compact
        icon={<IconCircleCheck size={22} />}
        title={emptyText ?? t('issues.none')}
      />
    );
  }
  return (
    <div className="myr-scroll-x">
      <Table verticalSpacing={compact ? 4 : 'xs'} fz="sm">
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={110}>{t('common.status')}</Table.Th>
            <Table.Th>{t('common.message')}</Table.Th>
            <Table.Th>{t('issues.location')}</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {issues.map((i, idx) => (
            <Table.Tr key={idx}>
              <Table.Td>
                <SeverityBadge severity={i.severity} />
              </Table.Td>
              <Table.Td>
                <Stack gap={2}>
                  <Text fz="sm">{i.message}</Text>
                  <Code fz="xs" style={{ width: 'fit-content' }}>
                    {i.code}
                  </Code>
                </Stack>
              </Table.Td>
              <Table.Td>
                <Group gap={6} wrap="wrap">
                  {i.file && (
                    <Code fz="xs">
                      {i.file}
                      {i.line ? `:${i.line}` : ''}
                    </Code>
                  )}
                  {i.object && <Code fz="xs">{i.object}</Code>}
                  {i.path && (
                    <Text fz="xs" c="dimmed">
                      {i.path}
                    </Text>
                  )}
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </div>
  );
}
