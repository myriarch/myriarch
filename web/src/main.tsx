// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import '@mantine/core/styles.css';
import '@mantine/notifications/styles.css';
import '@mantine/dates/styles.css';
import '@mantine/charts/styles.css';
import './theme/global.css';

import { localStorageColorSchemeManager, MantineProvider } from '@mantine/core';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { I18nextProvider } from 'react-i18next';
import { Providers } from './app/Providers';
import { STORAGE_KEY_PREFIX } from './brand';
import i18n from './i18n';
import { createQueryClient } from './queryClient';
import { cssVariablesResolver, theme } from './theme/theme';

const colorSchemeManager = localStorageColorSchemeManager({ key: STORAGE_KEY_PREFIX + 'color-scheme' });
const queryClient = createQueryClient();

async function bootstrap() {
  if (import.meta.env.VITE_MOCK === '1') {
    const { startMocking } = await import('./mocks/browser');
    await startMocking();
  }
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <MantineProvider
        theme={theme}
        cssVariablesResolver={cssVariablesResolver}
        defaultColorScheme="auto"
        colorSchemeManager={colorSchemeManager}
      >
        <I18nextProvider i18n={i18n}>
          <Providers queryClient={queryClient} />
        </I18nextProvider>
      </MantineProvider>
    </StrictMode>,
  );
}

void bootstrap();
