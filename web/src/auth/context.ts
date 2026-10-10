// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext } from 'react';
import type { AuthConfigInfo, Me, Permission } from '@/api/types';

export type AuthStatus = 'loading' | 'anonymous' | 'authenticated' | 'error';

export type BootstrapCredentials = { username: string; password: string } | { token: string };

export interface AuthContextValue {
  status: AuthStatus;
  me: Me | null;
  config: AuthConfigInfo | undefined;
  error: unknown;
  can: (permission: Permission) => boolean;
  devLogin: (username: string, password: string) => Promise<Me>;
  /** Break-glass sign-in: either username + password or a one-time token. */
  bootstrapLogin: (credentials: BootstrapCredentials) => Promise<Me>;
  logout: () => Promise<void>;
  reload: () => void;
}

export const AuthContext = createContext<AuthContextValue | null>(null);
