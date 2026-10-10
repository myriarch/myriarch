// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query';
import { call, client } from '../client';
import { qk } from './keys';

export function useOverviewStats() {
  return useQuery({
    queryKey: qk.stats.overview,
    queryFn: ({ signal }) => call(client.GET('/stats/overview', { signal })),
    refetchInterval: 60_000,
  });
}

export function useSystemInfo() {
  return useQuery({
    queryKey: qk.system,
    queryFn: ({ signal }) => call(client.GET('/system/info', { signal })),
    refetchInterval: 30_000,
  });
}
