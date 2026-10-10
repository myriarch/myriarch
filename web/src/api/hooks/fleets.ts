// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { call, client } from '../client';
import type {
  ClearFleetVersionSelectionRequest,
  MatchFleetsRequest,
  RenderRequest,
  SelectFleetVersionRequest,
} from '../types';
import { qk } from './keys';

export function useFleets() {
  return useQuery({
    queryKey: qk.fleets.list,
    queryFn: ({ signal }) => call(client.GET('/fleets', { signal })),
  });
}

export function useFleet(name: string | undefined, options?: { refetchInterval?: number | false }) {
  return useQuery({
    queryKey: qk.fleets.detail(name ?? ''),
    enabled: !!name,
    refetchInterval: options?.refetchInterval,
    queryFn: ({ signal }) =>
      call(client.GET('/fleets/{name}', { params: { path: { name: name! } }, signal })),
  });
}

export function useMatchFleets() {
  return useMutation({
    mutationFn: (body: MatchFleetsRequest) => call(client.POST('/fleets/match', { body })),
  });
}

export function useRolloutPolicies() {
  return useQuery({
    queryKey: qk.policies.list,
    queryFn: ({ signal }) => call(client.GET('/rollout-policies', { signal })),
  });
}

export function useRolloutPolicy(name: string | undefined) {
  return useQuery({
    queryKey: qk.policies.detail(name ?? ''),
    enabled: !!name,
    queryFn: ({ signal }) =>
      call(client.GET('/rollout-policies/{name}', { params: { path: { name: name! } }, signal })),
  });
}

export function useExecutors() {
  return useQuery({
    queryKey: qk.executors,
    queryFn: ({ signal }) => call(client.GET('/executors', { signal })),
  });
}

export function useBlueprints() {
  return useQuery({
    queryKey: qk.blueprints.list,
    queryFn: ({ signal }) => call(client.GET('/blueprints', { signal })),
  });
}

export function useBlueprint(name: string | undefined) {
  return useQuery({
    queryKey: qk.blueprints.detail(name ?? ''),
    enabled: !!name,
    queryFn: ({ signal }) =>
      call(client.GET('/blueprints/{name}', { params: { path: { name: name! } }, signal })),
  });
}

export function useRenderBlueprint(name: string) {
  return useMutation({
    mutationFn: (body: RenderRequest) =>
      call(client.POST('/blueprints/{name}/render', { params: { path: { name } }, body })),
  });
}

/** Dialog data of the "update to version" flow: the frame from Git and every candidate version. */
export function useFleetVersionOptions(name: string | undefined, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: qk.fleets.versionOptions(name ?? ''),
    enabled: !!name && (options?.enabled ?? true),
    queryFn: ({ signal }) =>
      call(
        client.GET('/fleets/{name}/version-options', {
          params: { path: { name: name! } },
          signal,
        }),
      ),
  });
}

/** What a version selection or its removal changes: fleet views, rollouts, releases, dashboard. */
function useInvalidateVersionViews() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: qk.fleets.all });
    void qc.invalidateQueries({ queryKey: qk.rollouts.lists });
    void qc.invalidateQueries({ queryKey: qk.releases.all });
    void qc.invalidateQueries({ queryKey: qk.stats.overview });
    void qc.invalidateQueries({ queryKey: ['audit'] });
  };
}

/**
 * Selects the target version within the Git frame. The error is shown inline in the dialog
 * (translated issue codes), so there is no global error toast.
 */
export function useSelectFleetVersion(name: string) {
  const invalidate = useInvalidateVersionViews();
  return useMutation({
    meta: { inlineError: true },
    mutationFn: (body: SelectFleetVersionRequest) =>
      call(
        client.POST('/fleets/{name}/actions/select-version', {
          params: { path: { name } },
          body,
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useClearFleetVersionSelection(name: string) {
  const invalidate = useInvalidateVersionViews();
  return useMutation({
    meta: { inlineError: true, success: 'versionUpdate.clear.done' },
    mutationFn: (body: ClearFleetVersionSelectionRequest) =>
      call(
        client.POST('/fleets/{name}/actions/clear-version-selection', {
          params: { path: { name } },
          body,
        }),
      ),
    onSuccess: invalidate,
  });
}
