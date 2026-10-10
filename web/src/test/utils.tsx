// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { MantineProvider } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { App } from '@/app/App';
import { AuthProvider } from '@/auth/AuthProvider';
import i18n, { type Lang } from '@/i18n';
import { getDb } from '@/mocks/db';
import { createQueryClient } from '@/queryClient';
import { cssVariablesResolver, theme } from '@/theme/theme';
import type { Role } from '@/api/types';

export function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="location">{loc.pathname + loc.search}</div>;
}

export function makeClient(): QueryClient {
  const qc = createQueryClient();
  qc.setDefaultOptions({ queries: { retry: false, staleTime: 0, gcTime: Infinity } });
  return qc;
}

interface Options {
  route?: string;
  role?: Role | null;
  lang?: Lang;
}

/** Renders the real app (router, auth, pages) against the MSW mock backend. */
export function renderApp({ route = '/', role = 'admin', lang = 'en' }: Options = {}) {
  getDb().sessionRole = role;
  void i18n.changeLanguage(lang);
  const qc = makeClient();
  const utils = render(
    <MantineProvider theme={theme} cssVariablesResolver={cssVariablesResolver}>
      <I18nextProvider i18n={i18n}>
        <Notifications />
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={[route]}>
            <AuthProvider>
              <App />
              <LocationProbe />
            </AuthProvider>
          </MemoryRouter>
        </QueryClientProvider>
      </I18nextProvider>
    </MantineProvider>,
  );
  return { ...utils, qc };
}

/** Renders a single element inside providers + a router with the given path pattern. */
export function renderWithProviders(
  ui: ReactElement,
  { path = '/', pattern = '*', role = 'admin' as Role | null, lang = 'en' as Lang } = {},
) {
  getDb().sessionRole = role;
  void i18n.changeLanguage(lang);
  const qc = makeClient();
  const utils = render(
    <MantineProvider theme={theme} cssVariablesResolver={cssVariablesResolver}>
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={[path]}>
            <AuthProvider>
              <Routes>
                <Route path={pattern} element={ui} />
              </Routes>
              <LocationProbe />
            </AuthProvider>
          </MemoryRouter>
        </QueryClientProvider>
      </I18nextProvider>
    </MantineProvider>,
  );
  return { ...utils, qc };
}
