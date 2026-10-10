// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/** localStorage wrapper that never throws (private mode, blocked storage, SSR/tests). */
export function readStorage(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeStorage(key: string, value: string | null): void {
  try {
    if (value === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value);
  } catch {
    /* storage unavailable: the preference is simply not persisted */
  }
}
