// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { QueryClient } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import { qk } from '@/api/hooks/keys';
import type {
  AgentDetail,
  AgentSummary,
  AgentSummaryPage,
  OverviewStats,
  RolloutSummary,
  RolloutSummaryPage,
} from '@/api/types';
import { createLiveApplier, patchAgentPage, removeFromAgentPage } from './apply';
import { LIVE_EVENT_TYPES, LiveConnection, type EventSourceLike } from './connection';
import { throttle } from './throttle';

const summary = (id: string, over: Partial<AgentSummary> = {}): AgentSummary => ({
  id,
  kind: 'collector',
  hostname: id,
  serviceName: 'otelcol-contrib',
  version: '0.161.0',
  outdated: true,
  osType: 'linux',
  arch: 'amd64',
  status: 'online',
  configSync: 'in_sync',
  hasUpdater: false,
  ...over,
});
const page = (items: AgentSummary[]): AgentSummaryPage => ({
  items,
  total: 100,
  limit: 50,
  offset: 0,
});
const rollout = (id: string, over: Partial<RolloutSummary> = {}): RolloutSummary => ({
  id,
  kind: 'version',
  fleetName: 'dc',
  policyName: 'standard',
  status: 'running',
  toRef: '0.162.0',
  currentWave: 0,
  waveCount: 2,
  counts: {
    total: 10,
    pending: 5,
    waitingWindow: 0,
    inProgress: 1,
    succeeded: 4,
    failed: 0,
    skipped: 0,
    cancelled: 0,
    rolledBack: 0,
  },
  origin: 'gitops',
  createdAt: 'x',
  updatedAt: 'x',
  ...over,
});

describe('live cache patches', () => {
  it('agent.updated patches rows of every cached list page, others untouched', () => {
    const qc = new QueryClient();
    qc.setQueryData(qk.agents.list({ limit: 50 }), page([summary('a'), summary('b')]));
    qc.setQueryData(qk.agents.list({ limit: 50, offset: 50 }), page([summary('c')]));
    const applier = createLiveApplier(qc);
    applier.apply('agent.updated', summary('b', { status: 'offline', version: '0.162.0' }));
    const p1 = qc.getQueryData<AgentSummaryPage>(qk.agents.list({ limit: 50 }))!;
    expect(p1.items[1]).toMatchObject({ id: 'b', status: 'offline', version: '0.162.0' });
    expect(p1.items[0]!.status).toBe('online');
    expect(
      qc.getQueryData<AgentSummaryPage>(qk.agents.list({ limit: 50, offset: 50 }))!.items[0]!.id,
    ).toBe('c');
    applier.dispose();
  });

  it('agent.updated patches a cached detail view', () => {
    const qc = new QueryClient();
    const detail = {
      agent: {
        id: 'a',
        version: '0.161.0',
        connection: { status: 'online' },
        health: { healthy: true, updatedAt: 'x' },
        assignment: {},
      },
      configSync: 'in_sync',
      outdated: true,
      capabilityNames: [],
      activeRolloutTargets: [],
    } as unknown as AgentDetail;
    qc.setQueryData(qk.agents.detail('a'), detail);
    const applier = createLiveApplier(qc);
    applier.apply(
      'agent.updated',
      summary('a', { healthy: false, configSync: 'failed', status: 'offline' }),
    );
    const d = qc.getQueryData<AgentDetail>(qk.agents.detail('a'))!;
    expect(d.configSync).toBe('failed');
    expect(d.agent.connection.status).toBe('offline');
    expect(d.agent.health?.healthy).toBe(false);
    applier.dispose();
  });

  it('agent.removed removes the row, decrements total and drops the detail', () => {
    const qc = new QueryClient();
    qc.setQueryData(qk.agents.list({ limit: 50 }), page([summary('a'), summary('b')]));
    qc.setQueryData(qk.agents.detail('a'), {});
    const applier = createLiveApplier(qc);
    applier.apply('agent.removed', { id: 'a' });
    const p = qc.getQueryData<AgentSummaryPage>(qk.agents.list({ limit: 50 }))!;
    expect(p.items.map((i) => i.id)).toEqual(['b']);
    expect(p.total).toBe(99);
    expect(qc.getQueryData(qk.agents.detail('a'))).toBeUndefined();
    applier.dispose();
    expect(removeFromAgentPage(undefined, 'x')).toBeUndefined();
    expect(patchAgentPage(page([]), summary('z'))).toBeUndefined();
  });

  it('agent.updated for an agent that is on no cached page does not mark the lists fresh', () => {
    const qc = new QueryClient();
    const key = qk.agents.list({ limit: 50 });
    qc.setQueryData(key, page([summary('a')]), { updatedAt: 1000 });
    const applier = createLiveApplier(qc);
    applier.apply('agent.updated', summary('unrelated'));
    expect(qc.getQueryState(key)!.dataUpdatedAt).toBe(1000);
    // a real patch replaces the row but keeps the freshness of the last fetch
    applier.apply('agent.updated', summary('a', { status: 'offline' }));
    expect(qc.getQueryData<AgentSummaryPage>(key)!.items[0]!.status).toBe('offline');
    expect(qc.getQueryState(key)!.dataUpdatedAt).toBe(1000);
    applier.dispose();
  });

  it('agent.updated replaces the row: fields the server omits are cleared', () => {
    const qc = new QueryClient();
    const key = qk.agents.list({ limit: 50 });
    qc.setQueryData(key, page([summary('a', { fleetName: 'hq' })]));
    const applier = createLiveApplier(qc);
    applier.apply('agent.updated', summary('a'));
    expect(qc.getQueryData<AgentSummaryPage>(key)!.items[0]!.fleetName).toBeUndefined();
    applier.dispose();
  });

  it('a failed rollout stays on the dashboard until an operator resolves it', () => {
    const qc = new QueryClient();
    qc.setQueryData(qk.stats.overview, {
      activeRollouts: [rollout('r1')],
    } as unknown as OverviewStats);
    const applier = createLiveApplier(qc);
    applier.apply('rollout.updated', rollout('r1', { status: 'failed' }));
    const active = qc.getQueryData<OverviewStats>(qk.stats.overview)!.activeRollouts;
    expect(active.map((r) => r.status)).toEqual(['failed']);
    applier.dispose();
  });

  it('rollout.updated patches lists and drops finished rollouts from the dashboard', () => {
    const qc = new QueryClient();
    const lists: RolloutSummaryPage = { items: [rollout('r1')], total: 1, limit: 25, offset: 0 };
    qc.setQueryData(qk.rollouts.list({ limit: 25 }), lists);
    qc.setQueryData(qk.stats.overview, {
      activeRollouts: [rollout('r1')],
    } as unknown as OverviewStats);
    const applier = createLiveApplier(qc);
    applier.apply('rollout.updated', rollout('r1', { status: 'completed' }));
    expect(
      qc.getQueryData<RolloutSummaryPage>(qk.rollouts.list({ limit: 25 }))!.items[0]!.status,
    ).toBe('completed');
    expect(qc.getQueryData<OverviewStats>(qk.stats.overview)!.activeRollouts).toEqual([]);
    applier.dispose();
  });

  it('a version rollout refreshes the fleet views (progress, button), a config rollout does not', () => {
    vi.useFakeTimers();
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, 'invalidateQueries');
    const applier = createLiveApplier(qc);
    applier.apply('rollout.updated', rollout('r1', { kind: 'config', fleetName: 'k8s-gateway' }));
    expect(spy).not.toHaveBeenCalledWith({ queryKey: qk.fleets.all });
    applier.apply('rollout.updated', rollout('r2', { kind: 'version', fleetName: 'k8s-gateway' }));
    expect(spy).toHaveBeenCalledWith({ queryKey: qk.fleets.all });
    applier.dispose();
    vi.useRealTimers();
  });

  it('gitops.synced and release.new invalidate, release.new notifies', async () => {
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, 'invalidateQueries');
    const onNewRelease = vi.fn();
    const applier = createLiveApplier(qc, { onNewRelease });
    applier.apply('gitops.synced', { state: 'ok', issueCount: 0 });
    expect(spy).toHaveBeenCalledWith({ queryKey: qk.fleets.all });
    applier.apply('release.new', {
      distribution: 'otelcol-contrib',
      version: '0.164.0',
      breakingCount: 2,
    });
    expect(spy).toHaveBeenCalledWith({ queryKey: qk.releases.all });
    expect(onNewRelease).toHaveBeenCalledOnce();
    applier.dispose();
  });

  it('fleet.updated refreshes fleet views, releases and the dashboard, throttled per fleet', () => {
    vi.useFakeTimers();
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, 'invalidateQueries');
    const applier = createLiveApplier(qc);
    applier.apply('fleet.updated', { name: 'k8s-gateway' });
    expect(spy).toHaveBeenCalledWith({ queryKey: qk.fleets.all });
    expect(spy).toHaveBeenCalledWith({ queryKey: qk.releases.all });
    expect(spy).toHaveBeenCalledWith({ queryKey: qk.stats.overview });
    expect(spy).toHaveBeenCalledTimes(3);
    // a burst for the same fleet is throttled (2 s), another fleet is not
    for (let i = 0; i < 10; i++) applier.apply('fleet.updated', { name: 'k8s-gateway' });
    expect(spy).toHaveBeenCalledTimes(3);
    applier.apply('fleet.updated', { name: 'dc' });
    expect(spy).toHaveBeenCalledTimes(5);
    vi.advanceTimersByTime(2100);
    expect(spy).toHaveBeenCalledTimes(7);
    // an event without a fleet name is ignored
    applier.apply('fleet.updated', {});
    expect(spy).toHaveBeenCalledTimes(7);
    applier.dispose();
    vi.useRealTimers();
  });

  it('knows the fleet.updated event on the stream', () => {
    expect(LIVE_EVENT_TYPES).toContain('fleet.updated');
  });

  it('resync and reconnect invalidate everything', () => {
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, 'invalidateQueries');
    const applier = createLiveApplier(qc);
    applier.apply('resync', {});
    applier.reconnected();
    expect(spy).toHaveBeenNthCalledWith(1);
    expect(spy).toHaveBeenNthCalledWith(2);
    applier.dispose();
  });

  it('stats.changed refetches at most once per 5 s', () => {
    vi.useFakeTimers();
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, 'invalidateQueries');
    const applier = createLiveApplier(qc);
    for (let i = 0; i < 20; i++) applier.apply('stats.changed', {});
    expect(spy).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(5100);
    expect(spy).toHaveBeenCalledTimes(2);
    applier.dispose();
    vi.useRealTimers();
  });
});

describe('throttle', () => {
  it('runs leading and trailing', () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const t = throttle(fn, 1000, () => Date.now());
    t();
    t();
    t();
    expect(fn).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1001);
    expect(fn).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });
});

class FakeES implements EventSourceLike {
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  closed = false;
  handlers = new Map<string, (ev: MessageEvent) => void>();
  addEventListener(type: string, l: (ev: MessageEvent) => void) {
    this.handlers.set(type, l);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data: string) {
    this.handlers.get(type)?.(new MessageEvent(type, { data }));
  }
}

describe('LiveConnection', () => {
  function setup() {
    const sources: FakeES[] = [];
    const timers: { fn: () => void; ms: number }[] = [];
    const statuses: string[] = [];
    const events: [string, unknown][] = [];
    const onReconnected = vi.fn();
    const conn = new LiveConnection({
      url: '/api/v1/events/stream',
      factory: () => {
        const s = new FakeES();
        sources.push(s);
        return s;
      },
      onEvent: (t, d) => events.push([t, d]),
      onStatus: (s) => statuses.push(s),
      onReconnected,
      random: () => 0.5,
      setTimer: (fn, ms) => {
        timers.push({ fn, ms });
        return timers.length;
      },
      clearTimer: () => undefined,
    });
    return { conn, sources, timers, statuses, events, onReconnected };
  }

  it('parses events and does not report the first connect as a reconnect', () => {
    const { conn, sources, statuses, events, onReconnected } = setup();
    conn.start();
    expect(statuses).toEqual(['connecting']);
    sources[0]!.onopen?.(new Event('open'));
    expect(statuses.at(-1)).toBe('open');
    sources[0]!.emit('agent.updated', JSON.stringify({ id: 'a' }));
    sources[0]!.emit('stats.changed', '');
    sources[0]!.emit('agent.updated', '{broken');
    expect(events).toEqual([
      ['agent.updated', { id: 'a' }],
      ['stats.changed', {}],
    ]);
    expect(onReconnected).not.toHaveBeenCalled();
  });

  it('closes on error, reconnects with exponential backoff and signals the reconnect', () => {
    const { conn, sources, timers, statuses, onReconnected } = setup();
    conn.start();
    sources[0]!.onopen?.(new Event('open'));
    sources[0]!.onerror?.(new Event('error'));
    expect(sources[0]!.closed).toBe(true);
    expect(statuses.at(-1)).toBe('reconnecting');
    expect(timers[0]!.ms).toBe(1000);
    timers[0]!.fn();
    expect(sources).toHaveLength(2);
    sources[1]!.onerror?.(new Event('error'));
    expect(timers[1]!.ms).toBe(2000);
    timers[1]!.fn();
    sources[2]!.onopen?.(new Event('open'));
    expect(onReconnected).toHaveBeenCalledOnce();
    expect(statuses.at(-1)).toBe('open');
    // backoff resets after success
    sources[2]!.onerror?.(new Event('error'));
    expect(timers[2]!.ms).toBe(1000);
  });

  it('caps the backoff and stops cleanly', () => {
    expect(LiveConnection.delay(10, 1000, 30000, () => 0.5)).toBe(30000);
    const { conn, sources, statuses } = setup();
    conn.start();
    conn.stop();
    expect(sources[0]!.closed).toBe(true);
    expect(statuses.at(-1)).toBe('closed');
  });
});
