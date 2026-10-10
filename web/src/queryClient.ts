// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { MutationCache, QueryClient } from '@tanstack/react-query';
import { isRetryable } from '@/api/errors';
import { notifyError, notifySuccess } from '@/lib/notify';

declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: {
      /** i18n key of a success toast */
      success?: string;
      /** The caller renders the error inline (dialog/form): no global error toast. */
      inlineError?: boolean;
    };
  }
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        gcTime: 5 * 60_000,
        refetchOnWindowFocus: true,
        retry: (failureCount, error) => isRetryable(error) && failureCount < 2,
      },
    },
    mutationCache: new MutationCache({
      onError: (error, _vars, _ctx, mutation) => {
        if (!mutation.meta?.inlineError) notifyError(error);
      },
      onSuccess: (_data, _vars, _ctx, mutation) => {
        const key = mutation.meta?.success;
        if (key) notifySuccess(key);
      },
    }),
  });
}
