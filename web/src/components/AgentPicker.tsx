// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Group, Loader, Select, Text } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAgents } from '@/api/hooks/agents';
import type { AgentSummary } from '@/api/types';

/** Server-side searchable agent select (search via listAgents?q=, max 15 suggestions). */
export function AgentPicker({
  value,
  onChange,
  label,
  description,
  placeholder,
}: {
  value: AgentSummary | null;
  onChange: (agent: AgentSummary | null) => void;
  label?: string;
  description?: string;
  placeholder?: string;
}) {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search, 250);
  const { data, isFetching } = useAgents({
    q: debounced || undefined,
    limit: 15,
    sort: 'hostname',
  });
  const items = data?.items ?? [];
  const options = items.map((a) => ({
    value: a.id,
    label: `${a.hostname || a.id.slice(0, 8)}${a.fleetName ? ` · ${a.fleetName}` : ''}`,
  }));
  if (value && !options.some((o) => o.value === value.id)) {
    options.unshift({ value: value.id, label: value.hostname || value.id.slice(0, 8) });
  }

  return (
    <Select
      label={label ?? t('picker.agent')}
      description={description}
      placeholder={placeholder ?? t('picker.agentPlaceholder')}
      searchable
      clearable
      allowDeselect
      data={options}
      value={value?.id ?? null}
      searchValue={search}
      onSearchChange={setSearch}
      filter={({ options: o }) => o}
      onChange={(id) =>
        onChange(items.find((a) => a.id === id) ?? (value && value.id === id ? value : null))
      }
      rightSection={isFetching ? <Loader size={14} /> : undefined}
      nothingFoundMessage={t('picker.nothingFound')}
      renderOption={({ option }) => {
        const a = items.find((x) => x.id === option.value);
        return (
          <Group justify="space-between" w="100%" wrap="nowrap" gap="sm">
            <Text fz="sm" truncate>
              {option.label}
            </Text>
            {a && (
              <Text fz="xs" c="dimmed" className="myr-mono">
                {a.version}
              </Text>
            )}
          </Group>
        );
      }}
    />
  );
}
