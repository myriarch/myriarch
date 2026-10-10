// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useContext } from 'react';
import type { Permission } from '@/api/types';
import { AuthContext, type AuthContextValue } from './context';

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>');
  return ctx;
}

/** `useCan('rollouts:operate')` - false while not authenticated. */
export function useCan(permission: Permission): boolean {
  return useAuth().can(permission);
}

/** Only same-origin absolute paths are valid post-login targets (prevents open redirects). */
export function safeRedirectPath(value: string | null | undefined): string {
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) {
    return '/';
  }
  if (value.startsWith('/login')) return '/';
  return value;
}
