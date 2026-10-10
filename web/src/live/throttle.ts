// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/** Leading + trailing throttle: runs immediately, then at most once per `ms` with the last call. */
export function throttle(fn: () => void, ms: number, now: () => number = Date.now) {
  let last = -Infinity;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const run = () => {
    last = now();
    timer = null;
    fn();
  };
  const throttled = () => {
    const wait = ms - (now() - last);
    if (wait <= 0) {
      if (timer) clearTimeout(timer);
      run();
    } else if (!timer) {
      timer = setTimeout(run, wait);
    }
  };
  throttled.cancel = () => {
    if (timer) clearTimeout(timer);
    timer = null;
  };
  return throttled;
}

/** Throttle per key (e.g. per rollout id). */
export function keyedThrottle(fn: (key: string) => void, ms: number, now: () => number = Date.now) {
  const map = new Map<string, ReturnType<typeof throttle>>();
  const call = (key: string) => {
    let t = map.get(key);
    if (!t) {
      t = throttle(() => fn(key), ms, now);
      map.set(key, t);
    }
    t();
    if (map.size > 500) {
      for (const [k, v] of map) {
        v.cancel();
        map.delete(k);
        if (map.size <= 250) break;
      }
    }
  };
  call.cancel = () => {
    for (const t of map.values()) t.cancel();
    map.clear();
  };
  return call;
}
