// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { ComponentHealth } from '@/api/types';

/** An unhealthy subtree is expanded initially so that the failing leaf is visible at once. */
export function hasUnhealthy(c: ComponentHealth): boolean {
  if (!c.healthy) return true;
  return Object.values(c.components ?? {}).some(hasUnhealthy);
}
