// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Center, Loader, Stack, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { Navigate, Outlet, useLocation } from 'react-router';
import { useAuth } from '@/auth/useAuth';
import { ErrorState } from '@/components/states';
import { LogoMark } from '@/components/Logo';

export function FullPageLoader() {
  const { t } = useTranslation();
  return (
    <Center h="100vh">
      <Stack align="center" gap="sm" aria-busy="true">
        <LogoMark size={44} />
        <Loader size="sm" />
        <Text c="dimmed" fz="sm">
          {t('common.loading')}
        </Text>
      </Stack>
    </Center>
  );
}

/** Route guard: loading -> spinner, no session -> /login?redirect=<current path>. */
export function RequireAuth() {
  const { status, error, reload } = useAuth();
  const location = useLocation();

  if (status === 'loading') return <FullPageLoader />;
  if (status === 'error') {
    return (
      <Center h="100vh">
        <ErrorState error={error} onRetry={reload} />
      </Center>
    );
  }
  if (status === 'anonymous') {
    const target = location.pathname + location.search + location.hash;
    const redirect = target && target !== '/' ? `?redirect=${encodeURIComponent(target)}` : '';
    return <Navigate to={`/login${redirect}`} replace />;
  }
  return <Outlet />;
}
