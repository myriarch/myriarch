// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { afterAll, afterEach, beforeAll, beforeEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';
import { setMockLatency } from '@/mocks/handlers';
import { resetDb } from '@/mocks/db';
import { server } from '@/mocks/server';
import { setCsrfToken } from '@/api/session';

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
  server.listen({ onUnhandledRequest: 'error' });
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
