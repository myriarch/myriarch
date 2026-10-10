// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { AGENT_KINDS, CONFIG_SYNC_STATES, CONNECTION_STATUSES } from '@/api/enums';
import type {
  AgentKind,
  AgentSort,
  ConfigSyncState,
  ConnectionStatus,
  ListAgentsQuery,
} from '@/api/types';

/** Page sizes offered by the agent list (the API maximum is 500). */
export const AGENT_PAGE_SIZES = [50, 100, 200] as const;
export const DEFAULT_SORT: AgentSort = 'hostname';

export interface AgentFilters {
  q: string;
  kind: AgentKind[];
  status: ConnectionStatus[];
  /** true = healthy only, false = unhealthy only, undefined = any */
  healthy?: boolean;
  fleet: string[];
  unassigned: boolean;
  version: string[];
  /** true = outdated only, false = up to date only */
  outdated?: boolean;
  os: string[];
  /** "key=value" pairs; the same key is ORed, different keys ANDed (server side) */
  attr: string[];
  configSync: ConfigSyncState[];
}

export interface AgentListState extends AgentFilters {
  sort: AgentSort;
  limit: number;
  offset: number;
}

export const EMPTY_FILTERS: AgentFilters = {
  q: '',
  kind: [],
  status: [],
  healthy: undefined,
  fleet: [],
  unassigned: false,
  version: [],
  outdated: undefined,
  os: [],
  attr: [],
  configSync: [],
};

export const DEFAULT_STATE: AgentListState = {
  ...EMPTY_FILTERS,
  sort: DEFAULT_SORT,
  limit: AGENT_PAGE_SIZES[0],
  offset: 0,
};

const SORT_FIELDS = [
  'hostname',
  'version',
  'status',
  'fleet',
  'lastSeenAt',
  'firstSeenAt',
  'health',
  'configSync',
  'osType',
  'siteId',
] as const;

export function isAgentSort(v: string | null | undefined): v is AgentSort {
  if (!v) return false;
  const field = v.startsWith('-') ? v.slice(1) : v;
  return (SORT_FIELDS as readonly string[]).includes(field);
}

function uniq<T>(xs: T[]): T[] {
  return [...new Set(xs)];
}

function onlyOf<T extends string>(values: string[], allowed: readonly T[]): T[] {
  return uniq(values.filter((v): v is T => (allowed as readonly string[]).includes(v)));
}

function parseBool(v: string | null): boolean | undefined {
  if (v === 'true') return true;
  if (v === 'false') return false;
  return undefined;
}

function parseIntSafe(v: string | null, fallback: number): number {
  if (v === null || v === '') return fallback;
  const n = Number(v);
  return Number.isInteger(n) && n >= 0 ? n : fallback;
}

/** Valid "key=value" attribute filter (non-empty key and value). */
export function isAttrFilter(s: string): boolean {
  const i = s.indexOf('=');
  return i > 0 && i < s.length - 1;
}

/** URL query -> typed state; invalid values are dropped instead of failing. */
export function parseAgentSearch(sp: URLSearchParams): AgentListState {
  const limitRaw = parseIntSafe(sp.get('limit'), AGENT_PAGE_SIZES[0]);
  const limit = (AGENT_PAGE_SIZES as readonly number[]).includes(limitRaw)
    ? limitRaw
    : AGENT_PAGE_SIZES[0];
  const sortRaw = sp.get('sort');
  const unassigned = parseBool(sp.get('unassigned')) === true;
  return {
    q: sp.get('q') ?? '',
    kind: onlyOf(sp.getAll('kind'), AGENT_KINDS),
    status: onlyOf(sp.getAll('status'), CONNECTION_STATUSES),
    healthy: parseBool(sp.get('healthy')),
    fleet: unassigned ? [] : uniq(sp.getAll('fleet').filter(Boolean)),
    unassigned,
    version: uniq(sp.getAll('version').filter(Boolean)),
    outdated: parseBool(sp.get('outdated')),
    os: uniq(sp.getAll('os').filter(Boolean)),
    attr: uniq(sp.getAll('attr').filter(isAttrFilter)),
    configSync: onlyOf(sp.getAll('configSync'), CONFIG_SYNC_STATES),
    sort: isAgentSort(sortRaw) ? sortRaw : DEFAULT_SORT,
    limit,
    offset: parseIntSafe(sp.get('offset'), 0),
  };
}

/** Typed state -> URL query. Defaults are omitted so that URLs stay short and shareable. */
export function toSearchParams(state: AgentListState): URLSearchParams {
  const sp = new URLSearchParams();
  if (state.q.trim()) sp.set('q', state.q.trim());
  state.kind.forEach((v) => sp.append('kind', v));
  state.status.forEach((v) => sp.append('status', v));
  if (state.healthy !== undefined) sp.set('healthy', String(state.healthy));
  if (state.unassigned) sp.set('unassigned', 'true');
  else state.fleet.forEach((v) => sp.append('fleet', v));
  state.version.forEach((v) => sp.append('version', v));
  if (state.outdated !== undefined) sp.set('outdated', String(state.outdated));
  state.os.forEach((v) => sp.append('os', v));
  state.attr.forEach((v) => sp.append('attr', v));
  state.configSync.forEach((v) => sp.append('configSync', v));
  if (state.sort !== DEFAULT_SORT) sp.set('sort', state.sort);
  if (state.limit !== AGENT_PAGE_SIZES[0]) sp.set('limit', String(state.limit));
  if (state.offset > 0) sp.set('offset', String(state.offset));
  return sp;
}

/** Filters only (no paging/sorting) as a plain object, used for facets and saved filters. */
export function filtersToApiQuery(
  f: AgentFilters,
): Omit<ListAgentsQuery, 'sort' | 'limit' | 'offset'> {
  const q: Omit<ListAgentsQuery, 'sort' | 'limit' | 'offset'> = {};
  if (f.q.trim()) q.q = f.q.trim();
  if (f.kind.length) q.kind = f.kind;
  if (f.status.length) q.status = f.status;
  if (f.healthy !== undefined) q.healthy = f.healthy;
  if (f.unassigned) q.unassigned = true;
  else if (f.fleet.length) q.fleet = f.fleet;
  if (f.version.length) q.version = f.version;
  if (f.outdated !== undefined) q.outdated = f.outdated;
  if (f.os.length) q.os = f.os;
  if (f.attr.length) q.attr = f.attr;
  if (f.configSync.length) q.configSync = f.configSync;
  return q;
}

export function toApiQuery(state: AgentListState): ListAgentsQuery {
  return {
    ...filtersToApiQuery(state),
    sort: state.sort,
    limit: state.limit,
    offset: state.offset,
  };
}

/** Number of active filter dimensions (for the badge on the filter button). */
export function countActiveFilters(f: AgentFilters): number {
  return (
    (f.q.trim() ? 1 : 0) +
    (f.kind.length ? 1 : 0) +
    (f.status.length ? 1 : 0) +
    (f.healthy !== undefined ? 1 : 0) +
    (f.fleet.length || f.unassigned ? 1 : 0) +
    (f.version.length ? 1 : 0) +
    (f.outdated !== undefined ? 1 : 0) +
    (f.os.length ? 1 : 0) +
    (f.attr.length ? 1 : 0) +
    (f.configSync.length ? 1 : 0)
  );
}

/** Saved filters store the filters + sort as the plain API-style object. */
export function toSavedQuery(state: AgentListState): Record<string, unknown> {
  const q: Record<string, unknown> = { ...filtersToApiQuery(state) };
  if (state.sort !== DEFAULT_SORT) q.sort = state.sort;
  return q;
}

export function fromSavedQuery(query: Record<string, unknown>): AgentListState {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (Array.isArray(v)) v.forEach((x) => sp.append(k, String(x)));
    else if (v !== undefined && v !== null && v !== '') sp.set(k, String(v));
  }
  sp.delete('limit');
  sp.delete('offset');
  return parseAgentSearch(sp);
}

/** Toggles `value` in a list. */
export function toggleIn<T>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter((x) => x !== value) : [...list, value];
}

/** attr entries of one key, e.g. attrValues(["site.type=pos","site.type=hq"], "site.type"). */
export function attrValues(attr: string[], key: string): string[] {
  const prefix = `${key}=`;
  return attr.filter((a) => a.startsWith(prefix)).map((a) => a.slice(prefix.length));
}

export function toggleAttr(attr: string[], key: string, value: string): string[] {
  return toggleIn(attr, `${key}=${value}`);
}
