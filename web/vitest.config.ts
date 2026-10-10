// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./vitest.setup.tsx'],
    css: false,
    include: ['src/**/*.test.{ts,tsx}'],
    testTimeout: 20000,
  },
});
