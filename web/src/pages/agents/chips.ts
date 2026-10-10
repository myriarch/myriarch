// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { TFunction } from 'i18next';
import { toggleIn, type AgentFilters } from './filters';

export interface FilterChip {
  key: string;
  label: string;
  remove: Partial<AgentFilters>;
}

/** One removable chip per selected filter value. Exported for tests. */
export function buildChips(f: AgentFilters, t: TFunction): FilterChip[] {
  const chips: FilterChip[] = [];
  if (f.q.trim())
    chips.push({
      key: 'q',
      label: `${t('agents.filter.search')}: ${f.q.trim()}`,
      remove: { q: '' },
    });
  f.status.forEach((v) =>
    chips.push({
      key: `status:${v}`,
      label: `${t('common.status')}: ${t(`enum.connection.${v}`)}`,
      remove: { status: toggleIn(f.status, v) },
    }),
  );
  if (f.healthy !== undefined)
    chips.push({
      key: 'healthy',
      label: `${t('agents.columns.health')}: ${t(`enum.health.${f.healthy ? 'healthy' : 'unhealthy'}`)}`,
      remove: { healthy: undefined },
    });
  f.kind.forEach((v) =>
    chips.push({
      key: `kind:${v}`,
      label: `${t('agents.columns.kind')}: ${t(`enum.kind.${v}`)}`,
      remove: { kind: toggleIn(f.kind, v) },
    }),
  );
  f.configSync.forEach((v) =>
    chips.push({
      key: `configSync:${v}`,
      label: `${t('agents.columns.configSync')}: ${t(`enum.configSync.${v}`)}`,
      remove: { configSync: toggleIn(f.configSync, v) },
    }),
  );
  if (f.unassigned)
    chips.push({
      key: 'unassigned',
      label: `${t('common.fleet')}: ${t('common.unassigned')}`,
      remove: { unassigned: false },
    });
  f.fleet.forEach((v) =>
    chips.push({
      key: `fleet:${v}`,
      label: `${t('common.fleet')}: ${v}`,
      remove: { fleet: toggleIn(f.fleet, v) },
    }),
  );
  f.version.forEach((v) =>
    chips.push({
      key: `version:${v}`,
      label: `${t('common.version')}: ${v}`,
      remove: { version: toggleIn(f.version, v) },
    }),
  );
  if (f.outdated !== undefined)
    chips.push({
      key: 'outdated',
      label: f.outdated ? t('agents.filter.onlyOutdated') : t('agents.filter.onlyUpToDate'),
      remove: { outdated: undefined },
    });
  f.os.forEach((v) =>
    chips.push({
      key: `os:${v}`,
      label: `${t('agents.filter.os')}: ${v}`,
      remove: { os: toggleIn(f.os, v) },
    }),
  );
  f.attr.forEach((v) =>
    chips.push({ key: `attr:${v}`, label: v, remove: { attr: toggleIn(f.attr, v) } }),
  );
  return chips;
}
