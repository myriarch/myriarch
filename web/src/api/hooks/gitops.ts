// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { call, client } from '../client';
import { qk } from './keys';

export function useGitOpsStatus() {
  return useQuery({
    queryKey: qk.gitops.status,
    queryFn: ({ signal }) => call(client.GET('/gitops/status', { signal })),
    refetchInterval: 30_000,
  });
}

export function useGitOpsSyncs(q: { limit: number; offset: number }) {
  return useQuery({
    queryKey: qk.gitops.syncs(q),
    queryFn: ({ signal }) => call(client.GET('/gitops/syncs', { params: { query: q }, signal })),
    placeholderData: keepPreviousData,
  });
}

export function useGitOpsObjects() {
  return useQuery({
    queryKey: qk.gitops.objects,
    queryFn: ({ signal }) => call(client.GET('/gitops/objects', { signal })),
  });
}

export function useTriggerGitOpsSync() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.gitops.syncRequested' },
    mutationFn: () => call(client.POST('/gitops/actions/sync')),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.gitops.all }),
  });
}
