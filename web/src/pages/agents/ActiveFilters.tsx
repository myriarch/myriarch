// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Button, Group, Pill } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { buildChips } from './chips';
import type { AgentFilters } from './filters';

export function ActiveFilters({
  filters,
  onChange,
  onClear,
}: {
  filters: AgentFilters;
  onChange: (patch: Partial<AgentFilters>) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  const chips = buildChips(filters, t);
  if (chips.length === 0) return null;
  return (
    <Group gap={6} wrap="wrap" data-testid="active-filters" aria-label={t('agents.filter.active')}>
      {chips.map((c) => (
        <Pill
          key={c.key}
          size="md"
          withRemoveButton
          onRemove={() => onChange(c.remove)}
          removeButtonProps={{ 'aria-label': `${t('common.remove')}: ${c.label}` }}
        >
          {c.label}
        </Pill>
      ))}
      <Button variant="subtle" size="compact-xs" onClick={onClear}>
        {t('agents.filter.clearAll')}
      </Button>
    </Group>
  );
}
