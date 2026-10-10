// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { setupServer } from 'msw/node';
import { handlers } from './handlers';

/** Node MSW server for tests (vitest, jsdom). */
export const server = setupServer(...handlers);
