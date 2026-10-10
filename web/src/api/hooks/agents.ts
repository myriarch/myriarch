// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { call, client } from '../client';
import type { ListAgentsQuery } from '../types';
import { qk } from './keys';

export function useAgents(query: ListAgentsQuery) {
  return useQuery({
    queryKey: qk.agents.list(query),
    queryFn: ({ signal }) => call(client.GET('/agents', { params: { query }, signal })),
    placeholderData: keepPreviousData,
  });
}

export function useAgentFacets(query: Omit<ListAgentsQuery, 'sort' | 'limit' | 'offset'>) {
  return useQuery({
    queryKey: qk.agents.facets(query),
    queryFn: ({ signal }) => call(client.GET('/agents/facets', { params: { query }, signal })),
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  });
}

export function useAgent(id: string | undefined) {
  return useQuery({
    queryKey: qk.agents.detail(id ?? ''),
    enabled: !!id,
    queryFn: ({ signal }) =>
      call(client.GET('/agents/{id}', { params: { path: { id: id! } }, signal })),
  });
}

export function useAgentEffectiveConfig(id: string | undefined, enabled = true) {
  return useQuery({
    queryKey: qk.agents.effective(id ?? ''),
    enabled: !!id && enabled,
    queryFn: ({ signal }) =>
      call(client.GET('/agents/{id}/effective-config', { params: { path: { id: id! } }, signal })),
  });
}

export function useAgentDesiredConfig(
  id: string | undefined,
  which: 'desired' | 'assigned',
  enabled = true,
) {
  return useQuery({
    queryKey: qk.agents.desired(id ?? '', which),
    enabled: !!id && enabled,
    queryFn: ({ signal }) =>
      call(
        client.GET('/agents/{id}/desired-config', {
          params: { path: { id: id! }, query: { which } },
          signal,
        }),
      ),
  });
}

export function useAgentComponents(id: string | undefined, enabled = true) {
  return useQuery({
    queryKey: qk.agents.components(id ?? ''),
    enabled: !!id && enabled,
    queryFn: ({ signal }) =>
      call(client.GET('/agents/{id}/components', { params: { path: { id: id! } }, signal })),
  });
}

/** Events are fetched page-wise (newest first) with the `before` cursor; see AgentEventsTab. */
export async function fetchAgentEvents(id: string, before: number, limit: number) {
  return call(
    client.GET('/agents/{id}/events', {
      params: { path: { id }, query: { before, limit } },
    }),
  );
}

export function useRestartAgent() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.agent.restartQueued' },
    mutationFn: (id: string) =>
      call(client.POST('/agents/{id}/actions/restart', { params: { path: { id } } })),
    onSuccess: (_d, id) => qc.invalidateQueries({ queryKey: qk.agents.events(id) }),
  });
}

export function useResyncAgent() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.agent.resyncQueued' },
    mutationFn: (id: string) =>
      call(client.POST('/agents/{id}/actions/resync', { params: { path: { id } } })),
    onSuccess: (_d, id) => qc.invalidateQueries({ queryKey: qk.agents.detail(id) }),
  });
}

export function useDeleteAgent() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.agent.deleted' },
    mutationFn: (id: string) => call(client.DELETE('/agents/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.agents.lists });
      void qc.invalidateQueries({ queryKey: qk.stats.overview });
    },
  });
}

export function useSavedFilters(page = 'agents') {
  return useQuery({
    queryKey: qk.savedFilters(page),
    queryFn: ({ signal }) =>
      call(client.GET('/saved-filters', { params: { query: { page } }, signal })),
  });
}

export function useCreateSavedFilter() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'agents.savedFilters.saved', inlineError: true },
    mutationFn: (body: {
      name: string;
      page: string;
      query: Record<string, unknown>;
      shared: boolean;
    }) => call(client.POST('/saved-filters', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-filters'] }),
  });
}

export function useDeleteSavedFilter() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'agents.savedFilters.deleted' },
    mutationFn: (id: string) =>
      call(client.DELETE('/saved-filters/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-filters'] }),
  });
}
