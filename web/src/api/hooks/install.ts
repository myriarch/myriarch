// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery } from '@tanstack/react-query';
import { call, client } from '../client';
import type { InstallCommandsRequest } from '../types';
import { qk } from './keys';

/** The installer bundles of this server. Polls while a bundle is still being prepared. */
export function useInstallBundles(enabled = true) {
  return useQuery({
    queryKey: qk.installBundles,
    enabled,
    queryFn: ({ signal }) => call(client.GET('/install/bundles', { signal })),
    refetchInterval: (q) =>
      q.state.data?.items.some((b) => b.status === 'pending' || b.status === 'preparing')
        ? 3_000
        : false,
  });
}

/**
 * Renders the install commands. The request holds no secret, and neither does the answer: the token is
 * put into the commands in the browser, outside of this mutation.
 */
export function useRenderInstallCommands() {
  return useMutation({
    meta: { inlineError: true },
    gcTime: 0,
    mutationFn: (body: InstallCommandsRequest) => call(client.POST('/install/commands', { body })),
  });
}
