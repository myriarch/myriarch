// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { DatesProvider } from '@mantine/dates';
import { Notifications } from '@mantine/notifications';
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router';
import { AuthProvider } from '@/auth/AuthProvider';
import { useLang } from '@/i18n/useLang';
import { firstDayOfWeek } from '@/lib/format';
import { LiveProvider } from '@/live/LiveProvider';
import { App } from './App';

/** Providers that depend on the router/auth/language (Mantine + i18n providers sit above). */
export function Providers({ queryClient }: { queryClient: QueryClient }) {
  const lang = useLang();
  return (
    <DatesProvider settings={{ locale: lang, firstDayOfWeek: firstDayOfWeek(lang) }}>
      <Notifications position="top-right" limit={4} />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <AuthProvider>
            <LiveProvider>
              <App />
            </LiveProvider>
          </AuthProvider>
        </BrowserRouter>
      </QueryClientProvider>
    </DatesProvider>
  );
}
