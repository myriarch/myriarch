// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "1" in mock mode (pnpm dev:mock): MSW answers all API calls in the browser. */
  readonly VITE_MOCK?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
