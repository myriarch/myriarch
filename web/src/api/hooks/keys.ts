// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { ListAgentsQuery } from '../types';

/** Central query key factory: live updates and mutations invalidate by these prefixes. */
export const qk = {
  auth: { config: ['auth', 'config'] as const, me: ['auth', 'me'] as const },
  agents: {
    all: ['agents'] as const,
    lists: ['agents', 'list'] as const,
    list: (q: ListAgentsQuery) => ['agents', 'list', q] as const,
    facets: (q: Omit<ListAgentsQuery, 'sort' | 'limit' | 'offset'>) =>
      ['agents', 'facets', q] as const,
    detail: (id: string) => ['agents', 'detail', id] as const,
    effective: (id: string) => ['agents', 'effective', id] as const,
    desired: (id: string, which: string) => ['agents', 'desired', id, which] as const,
    components: (id: string) => ['agents', 'components', id] as const,
    events: (id: string) => ['agents', 'events', id] as const,
  },
  savedFilters: (page: string) => ['saved-filters', page] as const,
  stats: { overview: ['stats', 'overview'] as const },
  fleets: {
    all: ['fleets'] as const,
    list: ['fleets', 'list'] as const,
    detail: (name: string) => ['fleets', 'detail', name] as const,
    versionOptions: (name: string) => ['fleets', 'version-options', name] as const,
  },
  blueprints: {
    all: ['blueprints'] as const,
    list: ['blueprints', 'list'] as const,
    detail: (name: string) => ['blueprints', 'detail', name] as const,
  },
  policies: {
    all: ['policies'] as const,
    list: ['policies', 'list'] as const,
    detail: (name: string) => ['policies', 'detail', name] as const,
  },
  executors: ['executors'] as const,
  rollouts: {
    all: ['rollouts'] as const,
    lists: ['rollouts', 'list'] as const,
    list: (q: object) => ['rollouts', 'list', q] as const,
    detail: (id: string) => ['rollouts', 'detail', id] as const,
    targets: (id: string, q: object) => ['rollouts', 'targets', id, q] as const,
    targetsOf: (id: string) => ['rollouts', 'targets', id] as const,
  },
  releases: {
    all: ['releases'] as const,
    list: (q: object) => ['releases', 'list', q] as const,
    latest: (distribution: string) => ['releases', 'latest', distribution] as const,
    detail: (distribution: string, version: string) =>
      ['releases', 'detail', distribution, version] as const,
    compare: (distribution: string, from: string, to: string) =>
      ['releases', 'compare', distribution, from, to] as const,
  },
  gitops: {
    all: ['gitops'] as const,
    status: ['gitops', 'status'] as const,
    syncs: (q: object) => ['gitops', 'syncs', q] as const,
    objects: ['gitops', 'objects'] as const,
  },
  artifacts: {
    all: ['artifacts'] as const,
    list: (q: object) => ['artifacts', 'list', q] as const,
  },
  audit: (q: object) => ['audit', q] as const,
  system: ['system', 'info'] as const,
  notifications: ['notifications', 'channels'] as const,
  apiTokens: ['tokens', 'api'] as const,
  enrollmentTokens: ['tokens', 'enrollment'] as const,
  users: ['users'] as const,
};
