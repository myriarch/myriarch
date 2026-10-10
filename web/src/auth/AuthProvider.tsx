// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useMemo, type ReactNode } from 'react';
import { call, client } from '@/api/client';
import { isApiError, isRetryable } from '@/api/errors';
import { qk } from '@/api/hooks/keys';
import { onUnauthorized, setCsrfToken } from '@/api/session';
import type { Me, Permission } from '@/api/types';
import { applyServerLanguageConfig } from '@/i18n';
import {
  AuthContext,
  type AuthContextValue,
  type AuthStatus,
  type BootstrapCredentials,
} from './context';

async function fetchMe(): Promise<Me | null> {
  try {
    return await call(client.GET('/auth/me'));
  } catch (e) {
    if (isApiError(e) && e.status === 401) return null;
    throw e;
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();

  const meQuery = useQuery({
    queryKey: qk.auth.me,
    queryFn: fetchMe,
    retry: (n, e) => isRetryable(e) && n < 2,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
  const configQuery = useQuery({
    queryKey: qk.auth.config,
    queryFn: () => call(client.GET('/auth/config')),
    retry: (n, e) => isRetryable(e) && n < 2,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });

  const me = meQuery.data ?? null;

  // The server's language options (ui.defaultLanguage / ui.languages) apply as soon as they are known.
  const uiConfig = configQuery.data?.ui;
  useEffect(() => {
    if (uiConfig) applyServerLanguageConfig(uiConfig);
  }, [uiConfig]);

  useEffect(() => {
    setCsrfToken(me?.csrfToken);
  }, [me?.csrfToken]);

  // Any 401 from a regular request means the session is gone: go back to the login page.
  useEffect(
    () =>
      onUnauthorized(() => {
        qc.setQueryData(qk.auth.me, null);
      }),
    [qc],
  );

  const can = useCallback(
    (permission: Permission) => !!me && me.permissions.includes(permission),
    [me],
  );

  const devLogin = useCallback(
    async (username: string, password: string) => {
      const result = await call(client.POST('/auth/dev-login', { body: { username, password } }));
      setCsrfToken(result.csrfToken);
      qc.setQueryData(qk.auth.me, result);
      return result;
    },
    [qc],
  );

  const bootstrapLogin = useCallback(
    async (credentials: BootstrapCredentials) => {
      const result = await call(client.POST('/auth/bootstrap-login', { body: { ...credentials } }));
      setCsrfToken(result.csrfToken);
      qc.setQueryData(qk.auth.me, result);
      return result;
    },
    [qc],
  );

  const logout = useCallback(async () => {
    try {
      await call(client.POST('/auth/logout'));
    } catch (e) {
      if (!(isApiError(e) && e.status === 401)) throw e;
    }
    setCsrfToken(undefined);
    qc.clear();
    qc.setQueryData(qk.auth.me, null);
  }, [qc]);

  const reload = useCallback(() => {
    void meQuery.refetch();
    void configQuery.refetch();
  }, [meQuery, configQuery]);

  let status: AuthStatus;
  if (meQuery.isPending) status = 'loading';
  else if (meQuery.isError) status = 'error';
  else status = me ? 'authenticated' : 'anonymous';

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      me,
      config: configQuery.data,
      error: meQuery.error,
      can,
      devLogin,
      bootstrapLogin,
      logout,
      reload,
    }),
    [status, me, configQuery.data, meQuery.error, can, devLogin, bootstrapLogin, logout, reload],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
