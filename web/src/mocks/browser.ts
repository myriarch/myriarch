// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { setupWorker } from 'msw/browser';
import { MOCK_DEBUG_HOOK } from '@/brand';
import { dropAllStreams } from './fakeEventSource';
import { getDb } from './db';
import { handlers, restoreSession } from './handlers';

/** Starts MSW in the browser (pnpm dev:mock). Unhandled requests (assets, fonts) pass through. */
export async function startMocking(): Promise<void> {
  restoreSession();
  const worker = setupWorker(...handlers);
  await worker.start({
    onUnhandledFrame: 'bypass',
    quiet: true,
    serviceWorker: { url: '/mockServiceWorker.js' },
  });
  // Demo/debug hooks (MOCK_DEBUG_HOOK), e.g. window.__myriarchMock.dropStream() to test reconnect handling.
  (window as unknown as Record<string, unknown>)[MOCK_DEBUG_HOOK] = {
    db: getDb(),
    dropStream: dropAllStreams,
  };
}
