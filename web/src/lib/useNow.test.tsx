// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useNow, useSharedNow } from './useNow';

const T0 = Date.parse('2026-10-15T10:00:00Z');

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(T0);
});
afterEach(() => {
  vi.useRealTimers();
});

describe('useNow', () => {
  it('runs a timer only while it is active', () => {
    const { result, rerender } = renderHook(({ active }) => useNow(active), {
      initialProps: { active: false },
    });
    expect(vi.getTimerCount()).toBe(0);
    act(() => vi.advanceTimersByTime(5000));
    expect(result.current).toBe(T0);
    rerender({ active: true });
    expect(vi.getTimerCount()).toBe(1);
    act(() => vi.advanceTimersByTime(2000));
    expect(result.current).toBe(T0 + 5000 + 2000);
  });
});

describe('useSharedNow', () => {
  it('renews the time once per second', () => {
    const { result } = renderHook(() => useSharedNow());
    expect(result.current).toBe(T0);
    act(() => vi.advanceTimersByTime(999));
    expect(result.current).toBe(T0);
    act(() => vi.advanceTimersByTime(1));
    expect(result.current).toBe(T0 + 1000);
    act(() => vi.advanceTimersByTime(3000));
    expect(result.current).toBe(T0 + 4000);
  });

  it('runs one timer for all callers and stops it with the last one', () => {
    expect(vi.getTimerCount()).toBe(0);
    const a = renderHook(() => useSharedNow());
    expect(vi.getTimerCount()).toBe(1);
    const b = renderHook(() => useSharedNow());
    const c = renderHook(() => useSharedNow());
    expect(vi.getTimerCount()).toBe(1);

    a.unmount();
    b.unmount();
    expect(vi.getTimerCount()).toBe(1);
    c.unmount();
    expect(vi.getTimerCount()).toBe(0);

    // and it starts again for the next caller
    const d = renderHook(() => useSharedNow());
    expect(vi.getTimerCount()).toBe(1);
    d.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('gives every caller the same reading in a tick, also one that joined between two ticks', () => {
    const a = renderHook(() => useSharedNow());
    act(() => vi.advanceTimersByTime(400));
    const b = renderHook(() => useSharedNow());
    expect(a.result.current).toBe(T0);
    expect(b.result.current).toBe(T0 + 400);
    act(() => vi.advanceTimersByTime(600));
    expect(a.result.current).toBe(T0 + 1000);
    expect(b.result.current).toBe(T0 + 1000);
    a.unmount();
    b.unmount();
  });
});
