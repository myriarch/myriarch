// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { ChangelogEntry } from '@/api/types';

/** Groups changelog entries by component; entries without a component land in "general". */
export function groupByComponent(
  entries: ChangelogEntry[],
  query: string,
): { component: string; entries: ChangelogEntry[] }[] {
  const needle = query.trim().toLowerCase();
  const map = new Map<string, ChangelogEntry[]>();
  for (const e of entries) {
    if (needle && !`${e.component ?? ''} ${e.text} ${e.version}`.toLowerCase().includes(needle))
      continue;
    const key = e.component ?? '';
    const list = map.get(key);
    if (list) list.push(e);
    else map.set(key, [e]);
  }
  return [...map.entries()]
    .map(([component, list]) => ({ component, entries: list }))
    .sort((a, b) => {
      if (a.component === '') return 1;
      if (b.component === '') return -1;
      return a.component.localeCompare(b.component);
    });
}
