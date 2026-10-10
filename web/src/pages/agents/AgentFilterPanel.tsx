// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ActionIcon,
  Anchor,
  Box,
  Checkbox,
  Group,
  ScrollArea,
  SegmentedControl,
  Skeleton,
  Stack,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { IconPlus, IconSearch } from '@tabler/icons-react';
import { useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { CONFIG_SYNC_STATES } from '@/api/enums';
import type { AgentFacets, FacetValue } from '@/api/types';
import { useLang } from '@/i18n/useLang';
import { compareVersions, formatNumber } from '@/lib/format';
import { attrValues, isAttrFilter, toggleAttr, toggleIn, type AgentFilters } from './filters';

export interface FacetOption {
  value: string;
  label: ReactNode;
  count?: number;
  disabled?: boolean;
}

function FacetGroup({
  title,
  options,
  selected,
  onToggle,
  searchable,
  initialVisible = 8,
  loading,
  testId,
}: {
  title: string;
  options: FacetOption[];
  selected: string[];
  onToggle: (value: string) => void;
  searchable?: boolean;
  initialVisible?: number;
  loading?: boolean;
  testId?: string;
}) {
  const { t } = useTranslation();
  const lang = useLang();
  const [expanded, setExpanded] = useState(false);
  const [filter, setFilter] = useState('');

  // Selected values stay visible even when their count dropped to zero.
  const merged = useMemo(() => {
    const known = new Set(options.map((o) => o.value));
    const extra: FacetOption[] = selected
      .filter((s) => !known.has(s))
      .map((s) => ({ value: s, label: s, count: 0 }));
    return [...options, ...extra];
  }, [options, selected]);

  const visibleBase = filter
    ? merged.filter((o) => o.value.toLowerCase().includes(filter.toLowerCase()))
    : merged;
  const visible = expanded || filter ? visibleBase : visibleBase.slice(0, initialVisible);
  const hidden = visibleBase.length - visible.length;

  if (!loading && merged.length === 0) return null;

  return (
    <Stack gap={6} data-testid={testId}>
      <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
        {title}
      </Text>
      {loading ? (
        <Stack gap={6}>
          <Skeleton height={14} />
          <Skeleton height={14} width="70%" />
        </Stack>
      ) : (
        <>
          {searchable && merged.length > initialVisible && (
            <TextInput
              size="xs"
              placeholder={t('agents.filter.searchValues')}
              leftSection={<IconSearch size={12} />}
              value={filter}
              onChange={(e) => setFilter(e.currentTarget.value)}
              aria-label={`${title}: ${t('agents.filter.searchValues')}`}
            />
          )}
          <ScrollArea.Autosize mah={expanded ? 260 : undefined} type="auto" offsetScrollbars>
            <Stack gap={5}>
              {visible.map((o) => (
                <Checkbox
                  key={o.value || '__empty'}
                  size="xs"
                  checked={selected.includes(o.value)}
                  disabled={o.disabled || (o.count === 0 && !selected.includes(o.value))}
                  onChange={() => onToggle(o.value)}
                  label={
                    <Group gap={6} justify="space-between" wrap="nowrap" w="100%">
                      <Text component="span" fz="sm" truncate>
                        {o.label}
                      </Text>
                      {o.count !== undefined && (
                        <Text component="span" fz="xs" c="dimmed" className="myr-tabular">
                          {formatNumber(lang, o.count)}
                        </Text>
                      )}
                    </Group>
                  }
                  styles={{
                    label: { width: '100%', paddingInlineStart: 8 },
                    body: { width: '100%', alignItems: 'center' },
                    labelWrapper: { width: '100%' },
                  }}
                />
              ))}
            </Stack>
          </ScrollArea.Autosize>
          {!filter && (hidden > 0 || expanded) && visibleBase.length > initialVisible && (
            <Anchor
              component="button"
              type="button"
              fz="xs"
              onClick={() => setExpanded((e) => !e)}
              style={{ width: 'fit-content' }}
            >
              {expanded ? t('common.collapse') : t('agents.filter.showMore', { count: hidden })}
            </Anchor>
          )}
        </>
      )}
    </Stack>
  );
}

function toOptions(
  values: FacetValue[] | undefined,
  label: (v: string) => ReactNode,
): FacetOption[] {
  return (values ?? []).map((v) => ({ value: v.value, label: label(v.value), count: v.count }));
}

/** Facet based filter sidebar of the agent list. Counts reflect the other active filters. */
export function AgentFilterPanel({
  filters,
  facets,
  loading,
  onChange,
}: {
  filters: AgentFilters;
  facets: AgentFacets | undefined;
  loading: boolean;
  onChange: (patch: Partial<AgentFilters>) => void;
}) {
  const { t } = useTranslation();
  const [attrInput, setAttrInput] = useState('');
  const attrError = attrInput !== '' && !isAttrFilter(attrInput);

  const healthOptions: FacetOption[] = (['healthy', 'unhealthy', 'unknown'] as const).map((h) => ({
    value: h,
    label: t(`enum.health.${h}`),
    count: facets?.health.find((v) => v.value === h)?.count ?? 0,
    disabled: h === 'unknown',
  }));
  const healthSelected =
    filters.healthy === undefined ? [] : [filters.healthy ? 'healthy' : 'unhealthy'];

  const versions = useMemo(
    () => [...(facets?.versions ?? [])].sort((a, b) => compareVersions(b.value, a.value)),
    [facets?.versions],
  );
  const configSync = useMemo(
    () =>
      [...(facets?.configSync ?? [])].sort(
        (a, b) =>
          CONFIG_SYNC_STATES.indexOf(a.value as (typeof CONFIG_SYNC_STATES)[number]) -
          CONFIG_SYNC_STATES.indexOf(b.value as (typeof CONFIG_SYNC_STATES)[number]),
      ),
    [facets?.configSync],
  );

  const fleetOptions = toOptions(facets?.fleets, (v) =>
    v === '' ? <i>{t('common.unassigned')}</i> : v,
  );
  const fleetSelected = filters.unassigned ? [''] : filters.fleet;

  const addAttr = () => {
    if (!isAttrFilter(attrInput)) return;
    if (!filters.attr.includes(attrInput)) onChange({ attr: [...filters.attr, attrInput] });
    setAttrInput('');
  };

  return (
    <Stack gap="lg" component="section" aria-label={t('common.filters')}>
      <FacetGroup
        testId="facet-status"
        title={t('common.status')}
        loading={loading && !facets}
        options={toOptions(facets?.status, (v) =>
          t(`enum.connection.${v as 'online'}`, { defaultValue: v }),
        )}
        selected={filters.status}
        onToggle={(v) => onChange({ status: toggleIn(filters.status, v as 'online') })}
      />
      <FacetGroup
        testId="facet-health"
        title={t('agents.columns.health')}
        loading={loading && !facets}
        options={healthOptions}
        selected={healthSelected}
        onToggle={(v) => {
          const next = v === 'healthy' ? true : false;
          onChange({ healthy: filters.healthy === next ? undefined : next });
        }}
      />
      <FacetGroup
        testId="facet-configSync"
        title={t('agents.columns.configSync')}
        loading={loading && !facets}
        options={toOptions(configSync, (v) =>
          t(`enum.configSync.${v as 'in_sync'}`, { defaultValue: v }),
        )}
        selected={filters.configSync}
        onToggle={(v) => onChange({ configSync: toggleIn(filters.configSync, v as 'in_sync') })}
      />
      <Stack gap={6}>
        <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
          {t('agents.filter.versionState')}
        </Text>
        <SegmentedControl
          size="xs"
          fullWidth
          value={filters.outdated === undefined ? 'all' : filters.outdated ? 'outdated' : 'current'}
          onChange={(v) => onChange({ outdated: v === 'all' ? undefined : v === 'outdated' })}
          data={[
            { value: 'all', label: t('common.all') },
            { value: 'outdated', label: t('version.outdated') },
            { value: 'current', label: t('agents.filter.upToDate') },
          ]}
          aria-label={t('agents.filter.versionState')}
        />
      </Stack>
      <FacetGroup
        testId="facet-fleet"
        title={t('common.fleet')}
        loading={loading && !facets}
        options={fleetOptions}
        selected={fleetSelected}
        searchable
        onToggle={(v) => {
          if (v === '') onChange({ unassigned: !filters.unassigned, fleet: [] });
          else onChange({ unassigned: false, fleet: toggleIn(filters.fleet, v) });
        }}
      />
      <FacetGroup
        testId="facet-version"
        title={t('common.version')}
        loading={loading && !facets}
        options={toOptions(versions, (v) => (
          <span className="myr-mono">{v}</span>
        ))}
        selected={filters.version}
        searchable
        initialVisible={6}
        onToggle={(v) => onChange({ version: toggleIn(filters.version, v) })}
      />
      <FacetGroup
        testId="facet-kind"
        title={t('agents.columns.kind')}
        loading={loading && !facets}
        options={toOptions(facets?.kinds, (v) =>
          t(`enum.kind.${v as 'collector'}`, { defaultValue: v }),
        )}
        selected={filters.kind}
        onToggle={(v) => onChange({ kind: toggleIn(filters.kind, v as 'collector') })}
      />
      <FacetGroup
        testId="facet-os"
        title={t('agents.filter.os')}
        loading={loading && !facets}
        options={toOptions(facets?.osTypes, (v) => v)}
        selected={filters.os}
        onToggle={(v) => onChange({ os: toggleIn(filters.os, v) })}
      />
      <FacetGroup
        testId="facet-siteType"
        title={t('agents.filter.siteType')}
        loading={loading && !facets}
        options={toOptions(facets?.siteTypes, (v) => v)}
        selected={attrValues(filters.attr, 'site.type')}
        onToggle={(v) => onChange({ attr: toggleAttr(filters.attr, 'site.type', v) })}
      />
      <FacetGroup
        testId="facet-country"
        title={t('agents.filter.country')}
        loading={loading && !facets}
        options={toOptions(facets?.countries, (v) => v)}
        selected={attrValues(filters.attr, 'site.country')}
        onToggle={(v) => onChange({ attr: toggleAttr(filters.attr, 'site.country', v) })}
      />
      <Stack gap={6}>
        <Text fz="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.05em' }}>
          {t('agents.filter.attribute')}
        </Text>
        <Group gap={6} wrap="nowrap" align="flex-start">
          <TextInput
            size="xs"
            style={{ flex: 1 }}
            placeholder="site.region=north"
            aria-label={t('agents.filter.attribute')}
            value={attrInput}
            error={attrError ? t('agents.filter.attributeInvalid') : undefined}
            onChange={(e) => setAttrInput(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                addAttr();
              }
            }}
          />
          <Tooltip label={t('common.add')}>
            <ActionIcon
              size={30}
              variant="light"
              onClick={addAttr}
              disabled={!isAttrFilter(attrInput)}
              aria-label={t('common.add')}
            >
              <IconPlus size={16} />
            </ActionIcon>
          </Tooltip>
        </Group>
        <Box>
          <Text fz="xs" c="dimmed">
            {t('agents.filter.attributeHint')}
          </Text>
        </Box>
      </Stack>
    </Stack>
  );
}
