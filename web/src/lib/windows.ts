// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { MaintenanceWindow } from '@/api/types';

export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const;
export type Weekday = (typeof WEEKDAYS)[number];
export type Segment = [startMinute: number, endMinute: number];

export function toMinutes(hhmm: string): number {
  const [h, m] = hhmm.split(':').map(Number);
  return (h ?? 0) * 60 + (m ?? 0);
}

/**
 * Expands maintenance windows into per-weekday minute segments (0..1440). A window that ends before
 * it starts crosses midnight and continues on the next day; start == end means the whole day.
 * Empty `days` means every day.
 */
export function windowSegments(windows: MaintenanceWindow[]): Record<Weekday, Segment[]> {
  const out: Record<Weekday, Segment[]> = {
    Mon: [],
    Tue: [],
    Wed: [],
    Thu: [],
    Fri: [],
    Sat: [],
    Sun: [],
  };
  for (const w of windows) {
    const days: readonly Weekday[] = w.days && w.days.length > 0 ? (w.days as Weekday[]) : WEEKDAYS;
    const s = toMinutes(w.start);
    const e = toMinutes(w.end);
    for (const d of days) {
      const idx = WEEKDAYS.indexOf(d);
      if (idx < 0) continue;
      if (s === e) {
        out[d].push([0, 1440]);
      } else if (s < e) {
        out[d].push([s, e]);
      } else {
        out[d].push([s, 1440]);
        out[WEEKDAYS[(idx + 1) % 7]!].push([0, e]);
      }
    }
  }
  for (const d of WEEKDAYS) out[d] = mergeSegments(out[d]);
  return out;
}

export function mergeSegments(segs: Segment[]): Segment[] {
  const sorted = [...segs].sort((a, b) => a[0] - b[0]);
  const merged: Segment[] = [];
  for (const s of sorted) {
    const last = merged[merged.length - 1];
    if (last && s[0] <= last[1]) last[1] = Math.max(last[1], s[1]);
    else merged.push([s[0], s[1]]);
  }
  return merged;
}
