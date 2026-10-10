// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

const mockWorkerPath = fileURLToPath(
  new URL('./node_modules/msw/lib/mockServiceWorker.js', import.meta.url),
);

/**
 * Serves the MSW service worker in mock mode only, so that the production bundle
 * (which is embedded into the myriarchd binary) never contains it.
 */
function mockServiceWorker(): Plugin {
  const sw = () => readFileSync(mockWorkerPath, 'utf8');
  return {
    name: 'myriarch:msw-worker',
    configureServer(server) {
      server.middlewares.use('/mockServiceWorker.js', (_req, res) => {
        res.setHeader('Content-Type', 'text/javascript; charset=utf-8');
        res.setHeader('Service-Worker-Allowed', '/');
        res.end(sw());
      });
    },
    generateBundle() {
      // Only emitted for `build --mode mock` (pnpm build:mock).
      this.emitFile({ type: 'asset', fileName: 'mockServiceWorker.js', source: sw() });
    },
    apply(_config, env) {
      return process.env.VITE_MOCK === '1' || env.mode === 'mock';
    },
  };
}

const backend = process.env.MYRIARCH_BACKEND ?? 'http://localhost:8080';

// https://vite.dev/config/
export default defineConfig({
  base: '/',
  plugins: [react(), mockServiceWorker()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    chunkSizeWarningLimit: 900,
    // License texts of the bundled npm packages: input of scripts/gen-third-party-notices.sh (THIRD_PARTY_NOTICES).
    license: { fileName: '.vite/license.json' },
  },
  server: {
    port: 5173,
    proxy: {
      // SSE (/api/v1/events/stream) works through the plain proxy because the
      // response is streamed and never buffered.
      '/api': { target: backend, changeOrigin: false },
      '/v1': { target: backend, changeOrigin: false, ws: true },
      // License texts served by myriarchd (links on the settings page).
      '/licenses': { target: backend, changeOrigin: false },
    },
  },
  preview: { port: 4173 },
});
