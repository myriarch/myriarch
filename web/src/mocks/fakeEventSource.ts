// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type { EventSourceLike } from '@/live/connection';
import { getDb } from './db';

/**
 * Stand-in for EventSource in mock mode: forwards the mock database's live events (heartbeats,
 * health flaps, rollout progress) with the same event names as GET /events/stream.
 */
const TICK_MS = 2500;
let ticker: ReturnType<typeof setInterval> | null = null;
let connections = 0;
const open = new Set<FakeEventSource>();

function retain() {
  connections++;
  if (!ticker) ticker = setInterval(() => getDb().tick(), TICK_MS);
}
function release() {
  connections = Math.max(0, connections - 1);
  if (connections === 0 && ticker) {
    clearInterval(ticker);
    ticker = null;
  }
}

export class FakeEventSource implements EventSourceLike {
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private listeners = new Map<string, Set<(ev: MessageEvent) => void>>();
  private unsubscribe: (() => void) | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;
  private seq = 0;

  constructor(readonly url: string) {
    retain();
    open.add(this);
    this.timer = setTimeout(() => {
      if (this.closed) return;
      this.unsubscribe = getDb().subscribe((type, data) => this.dispatch(type, data));
      this.onopen?.(new Event('open'));
    }, 200);
  }

  addEventListener(type: string, listener: (ev: MessageEvent) => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }

  private dispatch(type: string, data: unknown) {
    const ev = new MessageEvent(type, {
      data: JSON.stringify(data),
      lastEventId: String(++this.seq),
    });
    this.listeners.get(type)?.forEach((l) => l(ev));
  }

  /** Simulates a dropped connection (used by the demo hook MOCK_DEBUG_HOOK, i.e. window.__myriarchMock.dropStream()). */
  fail(): void {
    if (this.closed) return;
    this.onerror?.(new Event('error'));
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    this.unsubscribe?.();
    open.delete(this);
    release();
  }
}

export function createFakeEventSource(url: string): EventSourceLike {
  return new FakeEventSource(url);
}

export function dropAllStreams(): void {
  for (const es of [...open]) es.fail();
}
