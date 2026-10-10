// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

export interface KVRow {
  id: number;
  key: string;
  value: string;
}

let nextId = 1;
export const newRow = (key = '', value = ''): KVRow => ({ id: nextId++, key, value });

export function rowsToRecord(rows: KVRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) out[k] = r.value;
  }
  return out;
}
