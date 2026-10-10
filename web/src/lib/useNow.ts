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
