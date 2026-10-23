// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { afterAll, afterEach, beforeAll, beforeEach, vi } from 'vitest';
import { cleanup, configure } from '@testing-library/react';
import { setMockLatency } from '@/mocks/handlers';
import { resetDb } from '@/mocks/db';
import { server } from '@/mocks/server';
import { setCsrfToken } from '@/api/session';

// findBy*/waitFor poll for 1s by default. Rendering the whole app against the MSW handlers takes longer than that
// when the machine is loaded (parallel test files, CI runners), which made the update tests time out although the
// UI was correct. The bound stays well below testTimeout (20s), so a real failure still fails fast enough and the
// success path is not slowed down.
configure({ asyncUtilTimeout: 10000 });

// CodeMirror needs layout APIs jsdom does not implement; a plain <pre> is enough for the tests.
vi.mock('@/components/Yaml', () => ({
  YamlView: ({ value }: { value: string }) => <pre data-testid="yaml-view">{value}</pre>,
  YamlDiff: ({ a, b }: { a: string; b: string }) => <pre data-testid="yaml-diff">{a + b}</pre>,
}));

// Mantine needs matchMedia and ResizeObserver, jsdom provides neither.
beforeAll(() => {
  if (!window.matchMedia) {
    window.matchMedia = (query: string) =>
      ({
        matches: false,
        media: query,
        onchange: null,
        addListener: () => undefined,
        removeListener: () => undefined,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        dispatchEvent: () => false,
      }) as MediaQueryList;
  }
  if (!window.ResizeObserver) {
    window.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  }
  window.HTMLElement.prototype.scrollIntoView = () => undefined;
  // used by react-textarea-autosize (Mantine Textarea)
  if (!(document as unknown as { fonts?: unknown }).fonts) {
    Object.defineProperty(document, 'fonts', { value: { addEventListener() {}, removeEventListener() {}, ready: Promise.resolve() } });
  }
  setMockLatency(0);
  server.listen({ onUnhandledFrame: 'error' });
});

beforeEach(() => {
  resetDb();
  setCsrfToken(undefined);
});

afterEach(() => {
  cleanup();
  server.resetHandlers();
});

afterAll(() => server.close());
