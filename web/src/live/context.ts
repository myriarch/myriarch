// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext } from 'react';
import type { LiveStatus } from './connection';

export const LiveContext = createContext<LiveStatus>('closed');

export function useLiveStatus(): LiveStatus {
  return useContext(LiveContext);
}
