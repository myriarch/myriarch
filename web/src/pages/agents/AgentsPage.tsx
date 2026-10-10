// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Button, Card, Drawer, Group, Indicator, Stack, TextInput } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconAdjustmentsHorizontal, IconRefresh, IconSearch, IconX } from '@tabler/icons-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { readStorage, writeStorage } from '@/lib/storage';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router';
import { useAgentFacets, useAgents } from '@/api/hooks/agents';
import { useOverviewStats } from '@/api/hooks/overview';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { PageHeader } from '@/components/PageHeader';
import { Pager } from '@/components/Pager';
import { useAuth } from '@/auth/useAuth';
import { STORAGE_KEY_PREFIX } from '@/brand';
import { useLang } from '@/i18n/useLang';
import { formatNumber } from '@/lib/format';
import { ActiveFilters } from './ActiveFilters';
import { AgentFilterPanel } from './AgentFilterPanel';
import { AgentTable } from './AgentTable';
import { SavedFilters } from './SavedFilters';
import {
  AGENT_PAGE_SIZES,
  countActiveFilters,
  DEFAULT_STATE,
  filtersToApiQuery,
  parseAgentSearch,
  toApiQuery,
  toSearchParams,
  type AgentFilters,
  type AgentListState,
} from './filters';

const FILTER_PANEL_KEY = STORAGE_KEY_PREFIX + 'agents.filterPanel';

export default function AgentsPage() {
  const { t } = useTranslation();
  const lang = useLang();
  const { can } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const state = useMemo(() => parseAgentSearch(searchParams), [searchParams]);
  const [drawerOpen, drawer] = useDisclosure(false);
  const [panelOpen, setPanelOpen] = useState(
    () => readStorage(FILTER_PANEL_KEY) !== 'closed',
  );
  const togglePanel = () => {
    setPanelOpen((o) => {
      writeStorage(FILTER_PANEL_KEY, o ? 'closed' : 'open');
      return !o;
    });
  };

  const apiQuery = useMemo(() => toApiQuery(state), [state]);
  const facetQuery = useMemo(() => filtersToApiQuery(state), [state]);
  const agents = useAgents(apiQuery);
  const facets = useAgentFacets(facetQuery);
  const overview = useOverviewStats();
  const latest = overview.data?.outdated.latestVersion;

  // All state lives in the URL: filters survive reloads and can be shared as links.
  const update = useCallback(
    (patch: Partial<AgentListState>, opts: { replace?: boolean } = {}) => {
      const next: AgentListState = { ...state, ...patch };
      if (!('offset' in patch)) next.offset = 0;
      setSearchParams(toSearchParams(next), { replace: opts.replace });
    },
    [state, setSearchParams],
  );
  const updateFilters = useCallback((patch: Partial<AgentFilters>) => update(patch), [update]);
  const clearFilters = useCallback(
    () => update({ ...DEFAULT_STATE, sort: state.sort, limit: state.limit }),
    [update, state.sort, state.limit],
  );

  // Debounced search box <-> URL.
  const [qInput, setQInput] = useState(state.q);
  const [seenUrlQ, setSeenUrlQ] = useState(state.q);
  // The (trimmed) value this page wrote to the URL itself: it must not be written back into the
  // input, otherwise a space typed after a pause ("Site ") is eaten while the user types on.
  const [writtenQ, setWrittenQ] = useState<string | null>(null);
  if (seenUrlQ !== state.q) {
    // URL changed from outside (back button, saved filter, link): adopt it in the input.
    setSeenUrlQ(state.q);
    if (state.q !== writtenQ) setQInput(state.q);
    setWrittenQ(null);
  }
  useEffect(() => {
    if (qInput.trim() === state.q.trim()) return;
    const h = setTimeout(() => {
      setWrittenQ(qInput.trim());
      update({ q: qInput }, { replace: true });
    }, 300);
    return () => clearTimeout(h);
  }, [qInput, state.q, update]);

  const page = agents.data;
  const activeCount = countActiveFilters(state);

  // A live delete or a shrinking result set can leave us behind the last page.
  useEffect(() => {
    if (
      page &&
      !agents.isFetching &&
      page.total > 0 &&
      page.items.length === 0 &&
      state.offset > 0
    ) {
      const last = Math.floor((page.total - 1) / state.limit) * state.limit;
      update({ offset: last }, { replace: true });
    }
  }, [page, agents.isFetching, state.offset, state.limit, update]);

  const filterPanel = (
    <AgentFilterPanel
      filters={state}
      facets={facets.data}
      loading={facets.isPending}
      onChange={updateFilters}
    />
  );

  let body;
  if (agents.isPending) {
    body = <TableSkeleton rows={12} cols={7} />;
  } else if (agents.error && !page) {
    body = <ErrorState error={agents.error} onRetry={() => void agents.refetch()} />;
  } else if (page && page.total === 0) {
    body =
      activeCount > 0 ? (
        <EmptyState
          title={t('agents.empty.filtered')}
          text={t('agents.empty.filteredHint')}
          action={
            <Button
              variant="light"
              size="xs"
              onClick={clearFilters}
              leftSection={<IconX size={14} />}
            >
              {t('agents.filter.clearAll')}
            </Button>
          }
        />
      ) : (
        <EmptyState
          title={t('agents.empty.none')}
          text={t('agents.empty.noneHint')}
          action={
            can('enrollment:manage') ? (
              <Button component={Link} to="/settings/tokens" variant="light" size="xs">
                {t('agents.empty.createToken')}
              </Button>
            ) : undefined
          }
        />
      );
  } else if (page) {
    body = (
      <div
        style={{
          maxHeight: 'calc(100vh - var(--myr-header-height) - 290px)',
          minHeight: 240,
          overflow: 'auto',
          opacity: agents.isPlaceholderData ? 0.6 : 1,
          transition: 'opacity 120ms',
        }}
        className="myr-agent-scroll"
      >
        <AgentTable
          items={page.items}
          sort={state.sort}
          onSort={(sort) => update({ sort })}
          latest={latest}
        />
      </div>
    );
  }

  return (
    <>
      <PageHeader
        title={t('agents.title')}
        subtitle={
          page
            ? t('agents.subtitle', { count: page.total, total: formatNumber(lang, page.total) })
            : undefined
        }
        actions={
          <>
            <SavedFilters
              state={state}
              hasFilters={activeCount > 0}
              onApply={(s) => update({ ...s, limit: state.limit })}
            />
            <Button
              variant="default"
              size="xs"
              leftSection={<IconRefresh size={14} />}
              loading={agents.isFetching && !agents.isPending}
              onClick={() => {
                void agents.refetch();
                void facets.refetch();
              }}
            >
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <Group align="flex-start" gap="md" wrap="nowrap">
        {panelOpen && (
          <Card
            visibleFrom="lg"
            w={256}
            style={{ flex: '0 0 256px' }}
            padding="md"
            data-testid="filter-panel"
          >
            {filterPanel}
          </Card>
        )}

        <Stack gap="sm" style={{ flex: 1, minWidth: 0 }}>
          <Group gap="xs" wrap="nowrap">
            <TextInput
              style={{ flex: 1 }}
              leftSection={<IconSearch size={16} />}
              placeholder={t('agents.searchPlaceholder')}
              aria-label={t('agents.searchPlaceholder')}
              value={qInput}
              onChange={(e) => setQInput(e.currentTarget.value)}
              rightSection={
                qInput ? (
                  <IconX
                    size={14}
                    style={{ cursor: 'pointer' }}
                    onClick={() => setQInput('')}
                    aria-label={t('common.clear')}
                  />
                ) : null
              }
              data-testid="agent-search"
            />
            <Indicator
              disabled={activeCount === 0}
              label={activeCount}
              size={16}
              offset={4}
              hiddenFrom="lg"
              inline
            >
              <Button
                hiddenFrom="lg"
                variant="default"
                leftSection={<IconAdjustmentsHorizontal size={16} />}
                onClick={drawer.open}
              >
                {t('common.filters')}
              </Button>
            </Indicator>
            <Button
              visibleFrom="lg"
              variant="default"
              leftSection={<IconAdjustmentsHorizontal size={16} />}
              onClick={togglePanel}
              aria-pressed={panelOpen}
            >
              {panelOpen ? t('agents.filter.hide') : t('agents.filter.show')}
            </Button>
            {facets.data && (
              <Badge
                visibleFrom="sm"
                variant="light"
                color="gray"
                size="lg"
                style={{ textTransform: 'none' }}
              >
                {t('agents.matching', {
                  count: facets.data.total,
                  total: formatNumber(lang, facets.data.total),
                })}
              </Badge>
            )}
          </Group>

          <ActiveFilters filters={state} onChange={updateFilters} onClear={clearFilters} />

          <Card padding={0} data-testid="agent-list">
            {body}
            {page && page.total > 0 && (
              <Pager
                total={page.total}
                limit={state.limit}
                offset={state.offset}
                pageSizes={AGENT_PAGE_SIZES}
                loading={agents.isFetching}
                onChange={({ limit, offset }) => update({ limit, offset })}
              />
            )}
          </Card>
        </Stack>
      </Group>

      <Drawer
        opened={drawerOpen}
        onClose={drawer.close}
        title={t('common.filters')}
        position="left"
        size={300}
      >
        {filterPanel}
      </Drawer>
    </>
  );
}
