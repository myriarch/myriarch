// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { ActionIcon, Button, Group, Stack, TextInput } from '@mantine/core';
import { IconPlus, IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { newRow, type KVRow } from './kv';

/** Editor for string maps (hypothetical agent attributes, parameter overrides). */
export function KeyValueEditor({
  rows,
  onChange,
  keyLabel,
  valueLabel,
  keyPlaceholder = 'site.type',
  valuePlaceholder = 'edge',
  addLabel,
  datalistId,
  suggestions,
}: {
  rows: KVRow[];
  onChange: (rows: KVRow[]) => void;
  keyLabel?: string;
  valueLabel?: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  addLabel?: string;
  datalistId?: string;
  suggestions?: string[];
}) {
  const { t } = useTranslation();
  const set = (id: number, patch: Partial<KVRow>) =>
    onChange(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  return (
    <Stack gap="xs">
      {suggestions && datalistId && (
        <datalist id={datalistId}>
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      )}
      {rows.map((r, i) => (
        <Group key={r.id} gap="xs" wrap="nowrap" align="flex-end">
          <TextInput
            style={{ flex: 1 }}
            label={i === 0 ? (keyLabel ?? t('common.key')) : undefined}
            aria-label={keyLabel ?? t('common.key')}
            placeholder={keyPlaceholder}
            list={datalistId}
            value={r.key}
            onChange={(e) => set(r.id, { key: e.currentTarget.value })}
          />
          <TextInput
            style={{ flex: 1 }}
            label={i === 0 ? (valueLabel ?? t('common.value')) : undefined}
            aria-label={valueLabel ?? t('common.value')}
            placeholder={valuePlaceholder}
            value={r.value}
            onChange={(e) => set(r.id, { value: e.currentTarget.value })}
          />
          <ActionIcon
            variant="subtle"
            color="red"
            size={36}
            onClick={() => onChange(rows.filter((x) => x.id !== r.id))}
            aria-label={t('common.remove')}
          >
            <IconTrash size={16} />
          </ActionIcon>
        </Group>
      ))}
      <Button
        variant="subtle"
        size="xs"
        leftSection={<IconPlus size={14} />}
        onClick={() => onChange([...rows, newRow()])}
        style={{ width: 'fit-content' }}
      >
        {addLabel ?? t('common.add')}
      </Button>
    </Stack>
  );
}
