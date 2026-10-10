// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Resilient Server-Sent-Events client. Browsers reconnect EventSource themselves, but not after
 * HTTP errors (e.g. 401 or a 502 from a restarting proxy: readyState CLOSED), and they replay
 * nothing. So we always close on error and reconnect with exponential backoff, and report every
 * successful *re*-connect so that the app can refetch everything.
 */
export type LiveStatus = 'connecting' | 'open' | 'reconnecting' | 'closed';

export const LIVE_EVENT_TYPES = [
  'agent.updated',
  'agent.removed',
  'rollout.updated',
  'fleet.updated',
  'gitops.synced',
  'release.new',
  'stats.changed',
  'resync',
] as const;
export type LiveEventType = (typeof LIVE_EVENT_TYPES)[number];

export interface EventSourceLike {
  addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
  close(): void;
  onopen: ((ev: Event) => void) | null;
  onerror: ((ev: Event) => void) | null;
}

export type EventSourceFactory = (url: string) => EventSourceLike;

export interface LiveConnectionOptions {
  url: string;
  factory: EventSourceFactory;
  onEvent: (type: LiveEventType, data: unknown) => void;
  onStatus: (status: LiveStatus) => void;
  /** Called after every successful connect that follows a failure/disconnect. */
  onReconnected: () => void;
  baseDelayMs?: number;
  maxDelayMs?: number;
  /** Injectable for tests. */
  random?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}

export class LiveConnection {
  private source: EventSourceLike | null = null;
  private timer: unknown = null;
  private attempts = 0;
  private everOpened = false;
  private needsResync = false;
  private stopped = true;

  constructor(private readonly opts: LiveConnectionOptions) {}

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    this.clearTimer();
    this.detach();
    this.opts.onStatus('closed');
  }

  /** Delay before reconnect attempt n (1-based): exponential backoff with +-20 % jitter. */
  static delay(attempt: number, base: number, max: number, random: () => number): number {
    const exp = Math.min(max, base * 2 ** Math.max(0, attempt - 1));
    return Math.round(exp * (0.8 + random() * 0.4));
  }

  private connect(): void {
    if (this.stopped) return;
    this.opts.onStatus(this.needsResync || this.everOpened ? 'reconnecting' : 'connecting');
    let es: EventSourceLike;
    try {
      es = this.opts.factory(this.opts.url);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.source = es;
    es.onopen = () => {
      if (this.source !== es) return;
      this.attempts = 0;
      this.opts.onStatus('open');
      if (this.everOpened && this.needsResync) this.opts.onReconnected();
      this.everOpened = true;
      this.needsResync = false;
    };
    es.onerror = () => {
      if (this.source !== es) return;
      this.needsResync = true;
      this.detach();
      this.scheduleReconnect();
    };
    for (const type of LIVE_EVENT_TYPES) {
      es.addEventListener(type, (ev: MessageEvent) => {
        if (this.source !== es) return;
        try {
          this.opts.onEvent(type, ev.data ? JSON.parse(String(ev.data)) : {});
        } catch {
          // malformed payload or failing handler: ignore, the next resync/refetch repairs the view
        }
      });
    }
  }

  private scheduleReconnect(): void {
    if (this.stopped) return;
    this.opts.onStatus('reconnecting');
    this.attempts++;
    const delay = LiveConnection.delay(
      this.attempts,
      this.opts.baseDelayMs ?? 1000,
      this.opts.maxDelayMs ?? 30_000,
      this.opts.random ?? Math.random,
    );
    const set = this.opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.timer = set(() => {
      this.timer = null;
      this.connect();
    }, delay);
  }

  private clearTimer(): void {
    if (this.timer !== null) {
      (this.opts.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>)))(
        this.timer,
      );
      this.timer = null;
    }
  }

  private detach(): void {
    const es = this.source;
    this.source = null;
    if (es) {
      es.onopen = null;
      es.onerror = null;
      es.close();
    }
  }
}
