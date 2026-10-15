// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';

/** Re-renders every `ms` while `active` and returns the current time in ms (no timer when inactive). */
export function useNow(active: boolean, ms = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return undefined;
    const id = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(id);
  }, [active, ms]);
  return now;
}

type ClockListener = (now: number) => void;
const clockListeners = new Set<ClockListener>();
let clockTimer: ReturnType<typeof setInterval> | undefined;

function subscribeClock(listener: ClockListener): () => void {
  clockListeners.add(listener);
  clockTimer ??= setInterval(() => {
    const now = Date.now();
    clockListeners.forEach((l) => l(now));
  }, 1000);
  return () => {
    clockListeners.delete(listener);
    if (clockListeners.size === 0 && clockTimer !== undefined) {
      clearInterval(clockTimer);
      clockTimer = undefined;
    }
  };
}

/**
 * The current time in ms, renewed once per second. Unlike `useNow`, every caller shares one timer
 * and one reading, so a table full of countdowns runs a single interval and all its rows change
 * their seconds at the same moment.
 */
export function useSharedNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => subscribeClock(setNow), []);
  return now;
}
