// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import type { Permission } from '@/api/types';
import { useAuth } from '@/auth/useAuth';
import { NoPermission } from './states';

/** Renders children only when the current principal holds the permission (otherwise nothing). */
export function Can({
  permission,
  children,
  fallback = null,
}: {
  permission: Permission;
  children: ReactNode;
  fallback?: ReactNode;
}) {
  const { can } = useAuth();
  return can(permission) ? <>{children}</> : <>{fallback}</>;
}

/** Page level guard: shows the "no permission" empty state instead of the page. */
export function RequirePermission({
  permission,
  children,
}: {
  permission: Permission;
  children: ReactNode;
}) {
  const { can } = useAuth();
  return can(permission) ? <>{children}</> : <NoPermission permission={permission} />;
}
