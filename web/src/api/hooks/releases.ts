// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { call, client } from '../client';
import type { DistributionName } from '../types';
import { qk } from './keys';

export function useReleases(q: {
  distribution: DistributionName;
  includePrerelease?: boolean;
  limit?: number;
  offset?: number;
}) {
  return useQuery({
    queryKey: qk.releases.list(q),
    queryFn: ({ signal }) => call(client.GET('/releases', { params: { query: q }, signal })),
    placeholderData: keepPreviousData,
  });
}

export function useLatestRelease(distribution: DistributionName = 'otelcol-contrib') {
  return useQuery({
    queryKey: qk.releases.latest(distribution),
    queryFn: ({ signal }) =>
      call(client.GET('/releases/latest', { params: { query: { distribution } }, signal })),
    retry: false,
  });
}

export function useRelease(distribution: string | undefined, version: string | undefined) {
  return useQuery({
    queryKey: qk.releases.detail(distribution ?? '', version ?? ''),
    enabled: !!distribution && !!version,
    queryFn: ({ signal }) =>
      call(
        client.GET('/releases/{distribution}/{version}', {
          params: { path: { distribution: distribution!, version: version! } },
          signal,
        }),
      ),
  });
}

export function useCompareVersions(
  distribution: DistributionName,
  from: string | undefined,
  to: string | undefined,
) {
  return useQuery({
    queryKey: qk.releases.compare(distribution, from ?? '', to ?? ''),
    enabled: !!from && !!to,
    queryFn: ({ signal }) =>
      call(
        client.GET('/releases/compare', {
          params: { query: { distribution, from: from!, to: to! } },
          signal,
        }),
      ),
  });
}

export function useAcknowledgeRelease() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.release.acknowledged' },
    mutationFn: (v: { distribution: string; version: string }) =>
      call(
        client.POST('/releases/{distribution}/{version}/actions/acknowledge', {
          params: { path: v },
        }),
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.releases.all });
      void qc.invalidateQueries({ queryKey: qk.stats.overview });
    },
  });
}

export function useRefreshReleases() {
  const qc = useQueryClient();
  return useMutation({
    meta: { success: 'actions.release.refreshRequested' },
    mutationFn: () => call(client.POST('/releases/actions/refresh')),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.releases.all }),
  });
}
