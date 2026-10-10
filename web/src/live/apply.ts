// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { QueryClient } from '@tanstack/react-query';
import { qk } from '@/api/hooks/keys';
import type {
  AgentDetail,
  AgentSummary,
  AgentSummaryPage,
  LiveAgentRemovedData,
  LiveFleetUpdatedData,
  OverviewStats,
  ReleaseSummary,
  RolloutSummary,
  RolloutSummaryPage,
} from '@/api/types';
import { keyedThrottle, throttle } from './throttle';
import type { LiveEventType } from './connection';

export interface LiveHandlers {
  onNewRelease?: (release: ReleaseSummary) => void;
}

/**
 * Pure cache patches (exported for tests). They return undefined when nothing changes: TanStack
 * Query then leaves the query alone, otherwise even a no-op patch would mark the list as freshly
 * fetched and it would never refetch on mount or focus while events keep arriving.
 */
export function patchAgentPage(page: AgentSummaryPage | undefined, agent: AgentSummary) {
  if (!page) return undefined;
  const idx = page.items.findIndex((a) => a.id === agent.id);
  if (idx < 0) return undefined;
  const items = page.items.slice();
  // The event carries the complete summary: replace the row so that fields the server omits
  // (e.g. fleetName of an agent that became unassigned) are cleared instead of kept.
  items[idx] = agent;
  return { ...page, items };
}

/**
 * Patches every cached query under `key` without touching its freshness: a patch is not a
 * refetch, so staleness (and with it refetch on mount/focus) must keep counting from the last
 * real fetch.
 */
function patchCachedQueries<T>(
  qc: QueryClient,
  key: readonly unknown[],
  patch: (old: T | undefined) => T | undefined,
): void {
  for (const q of qc.getQueryCache().findAll({ queryKey: key })) {
    const next = patch(q.state.data as T | undefined);
    if (next === undefined) continue;
    qc.setQueryData<T>(q.queryKey, next, { updatedAt: q.state.dataUpdatedAt });
  }
}

export function removeFromAgentPage(page: AgentSummaryPage | undefined, id: string) {
  if (!page || !page.items.some((a) => a.id === id)) return undefined;
  return {
    ...page,
    items: page.items.filter((a) => a.id !== id),
    total: Math.max(0, page.total - 1),
  };
}

export function patchAgentDetail(detail: AgentDetail | undefined, s: AgentSummary) {
  if (!detail || detail.agent.id !== s.id) return detail;
  const a = detail.agent;
  return {
    ...detail,
    configSync: s.configSync,
    outdated: s.outdated,
    agent: {
      ...a,
      version: s.version,
      connection: {
        ...a.connection,
        status: s.status,
        lastSeenAt: s.lastSeenAt ?? a.connection.lastSeenAt,
      },
      health:
        s.healthy === undefined
          ? a.health
          : {
              ...(a.health ?? { updatedAt: new Date().toISOString() }),
              healthy: s.healthy,
              status: s.healthStatus ?? a.health?.status,
            },
      assignment: { ...a.assignment, fleetName: s.fleetName, targetVersion: s.targetVersion },
    },
  } satisfies AgentDetail;
}

export function patchRolloutPage(page: RolloutSummaryPage | undefined, r: RolloutSummary) {
  if (!page) return page;
  const idx = page.items.findIndex((x) => x.id === r.id);
  if (idx < 0) return page;
  const items = page.items.slice();
  items[idx] = r;
  return { ...page, items };
}

export function patchOverviewRollouts(stats: OverviewStats | undefined, r: RolloutSummary) {
  if (!stats) return stats;
  const idx = stats.activeRollouts.findIndex((x) => x.id === r.id);
  if (idx < 0) return stats;
  // 'failed' stays listed: the server keeps it active until an operator aborts it.
  const finished =
    r.status === 'completed' ||
    r.status === 'aborted' ||
    r.status === 'rolled_back' ||
    r.status === 'superseded';
  const next = stats.activeRollouts.slice();
  if (finished) next.splice(idx, 1);
  else next[idx] = { ...next[idx], ...r };
  return { ...stats, activeRollouts: next };
}

/**
 * Applies one SSE event to the TanStack Query cache. Returns a disposer for the pending
 * throttled work. Rules (event types: docs/design/opamp-server.md, Live stream):
 * agent.updated/removed and rollout.updated patch caches, fleet.updated invalidates the fleet views, gitops.synced and release.new
 * invalidate, stats.changed refetches the dashboard at most once per 5 s, resync/reconnect
 * invalidates everything.
 */
export function createLiveApplier(qc: QueryClient, handlers: LiveHandlers = {}) {
  const invalidateStats = throttle(
    () => void qc.invalidateQueries({ queryKey: qk.stats.overview }),
    5000,
  );
  const invalidateRolloutDetail = keyedThrottle((id) => {
    void qc.invalidateQueries({ queryKey: qk.rollouts.detail(id) });
    void qc.invalidateQueries({ queryKey: qk.rollouts.targetsOf(id) });
  }, 2000);
  // Effective target or UI selection of a fleet changed: its views, the releases' updatable fleets
  // and the dashboard (the latter on its own 5 s throttle).
  const invalidateFleet = keyedThrottle(() => {
    void qc.invalidateQueries({ queryKey: qk.fleets.all });
    void qc.invalidateQueries({ queryKey: qk.releases.all });
  }, 2000);
  const invalidateAgentDetail = keyedThrottle((id) => {
    void qc.invalidateQueries({ queryKey: qk.agents.detail(id) });
  }, 5000);

  function apply(type: LiveEventType, data: unknown): void {
    switch (type) {
      case 'agent.updated': {
        const agent = data as AgentSummary;
        if (!agent?.id) return;
        patchCachedQueries<AgentSummaryPage>(qc, qk.agents.lists, (old) =>
          patchAgentPage(old, agent),
        );
        qc.setQueryData<AgentDetail>(qk.agents.detail(agent.id), (old) =>
          patchAgentDetail(old, agent),
        );
        if (qc.getQueryData(qk.agents.detail(agent.id))) invalidateAgentDetail(agent.id);
        return;
      }
      case 'agent.removed': {
        const { id } = data as LiveAgentRemovedData;
        if (!id) return;
        patchCachedQueries<AgentSummaryPage>(qc, qk.agents.lists, (old) =>
          removeFromAgentPage(old, id),
        );
        qc.removeQueries({ queryKey: qk.agents.detail(id) });
        invalidateStats();
        return;
      }
      case 'rollout.updated': {
        const r = data as RolloutSummary;
        if (!r?.id) return;
        qc.setQueriesData<RolloutSummaryPage>({ queryKey: qk.rollouts.lists }, (old) =>
          patchRolloutPage(old, r),
        );
        qc.setQueryData<OverviewStats>(qk.stats.overview, (old) => patchOverviewRollouts(old, r));
        invalidateRolloutDetail(r.id);
        // The fleet page shows the running version rollout (status, progress) and the update
        // button: they follow the rollout, not only selection changes.
        if (r.kind === 'version' && r.fleetName) invalidateFleet(r.fleetName);
        // A new rollout (not yet in any cached list) must show up: refetch lists lazily.
        void qc.invalidateQueries({ queryKey: qk.rollouts.lists, refetchType: 'none' });
        return;
      }
      case 'fleet.updated': {
        const { name } = data as LiveFleetUpdatedData;
        if (!name) return;
        invalidateFleet(name);
        invalidateStats();
        return;
      }
      case 'gitops.synced': {
        for (const key of [
          qk.gitops.all,
          qk.fleets.all,
          qk.blueprints.all,
          qk.policies.all,
          qk.executors,
          qk.stats.overview,
        ]) {
          void qc.invalidateQueries({ queryKey: key });
        }
        return;
      }
      case 'release.new': {
        void qc.invalidateQueries({ queryKey: qk.releases.all });
        void qc.invalidateQueries({ queryKey: qk.stats.overview });
        handlers.onNewRelease?.(data as ReleaseSummary);
        return;
      }
      case 'stats.changed':
        invalidateStats();
        return;
      case 'resync':
        void qc.invalidateQueries();
        return;
    }
  }

  return {
    apply,
    /** After a reconnect nothing was replayed: reload everything that is on screen. */
    reconnected: () => void qc.invalidateQueries(),
    dispose: () => {
      invalidateStats.cancel();
      invalidateRolloutDetail.cancel();
      invalidateFleet.cancel();
      invalidateAgentDetail.cancel();
    },
  };
}
