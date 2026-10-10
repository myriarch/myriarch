// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Code, Table, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { formatValue } from '@/lib/format';

/** Key/value table of arbitrary (JSON) parameter values. */
export function ParametersTable({
  values,
  sources,
}: {
  values: Record<string, unknown>;
  sources?: Record<string, string>;
}) {
  const { t } = useTranslation();
  const entries = Object.entries(values).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) {
    return (
      <Text c="dimmed" fz="sm">
        {t('parameters.none')}
      </Text>
    );
  }
  return (
    <div className="myr-scroll-x">
      <Table fz="sm" verticalSpacing={5}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('common.name')}</Table.Th>
            <Table.Th>{t('common.value')}</Table.Th>
            {sources && <Table.Th>{t('common.source')}</Table.Th>}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {entries.map(([k, v]) => (
            <Table.Tr key={k}>
              <Table.Td className="myr-mono">{k}</Table.Td>
              <Table.Td style={{ wordBreak: 'break-all' }}>
                <Code>{formatValue(v)}</Code>
              </Table.Td>
              {sources && (
                <Table.Td>
                  {sources[k]
                    ? t(`enum.paramSource.${sources[k] as 'default'}`, { defaultValue: sources[k] })
                    : '–'}
                </Table.Td>
              )}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </div>
  );
}
