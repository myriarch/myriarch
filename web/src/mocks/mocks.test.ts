// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { RolloutStatus } from '@/api/types';
import { getDb } from './db';
import { renderMock } from './render';

describe('mock database', () => {
  it('has the fixtures the mock-mode demo relies on', () => {
    const db = getDb();
    const collectors = db.collectors();
    expect(collectors.filter((e) => e.agent.assignment.fleetName).length).toBe(1000);
    const per = (f: string) => collectors.filter((e) => e.agent.assignment.fleetName === f).length;
    expect({
      windowsHosts: per('windows-hosts'),
      edgeSites: per('edge-sites'),
      linuxHosts: per('linux-hosts'),
      datacenter: per('datacenter'),
      gw: per('k8s-gateway'),
    }).toEqual({ windowsHosts: 480, edgeSites: 240, linuxHosts: 120, datacenter: 120, gw: 40 });
    const versions = new Set(collectors.map((e) => e.agent.version));
    for (const v of ['0.160.0', '0.161.0', '0.162.0']) expect(versions.has(v)).toBe(true);
    expect(db.latestVersion()).toBe('0.163.0');
    const statuses = db.rollouts.map((r) => r.rollout.status).sort();
    expect(statuses).toEqual(['completed', 'running', 'waiting_approval']);
    expect(db.gitopsStatus.issues.some((i) => i.severity === 'warning')).toBe(true);
    expect(
      db.releases.find((r) => r.version === '0.163.0')!.breakingChanges.length,
    ).toBeGreaterThan(0);
  });

  it('is deterministic apart from timestamps', () => {
    const a = getDb();
    const ids = [...a.entries.keys()].slice(0, 5);
    // a second generation yields the same ids in the same order
    const again = [...new (a.constructor as new () => typeof a)().entries.keys()].slice(0, 5);
    expect(again).toEqual(ids);
  });

  it('filters, sorts and paginates server side', () => {
    const db = getDb();
    const all = db.queryAgents({}, 'hostname', 50, 0);
    expect(all.items).toHaveLength(50);
    expect(all.total).toBe(db.entries.size);
    const off = db.queryAgents({ status: ['offline'] }, '-lastSeenAt', 500, 0);
    expect(off.items.every((a) => a.status === 'offline')).toBe(true);
    const fleet = db.queryAgents(
      { fleet: ['linux-hosts'], attr: ['site.type=office'] },
      'version',
      500,
      0,
    );
    expect(fleet.total).toBe(120);
    expect(db.queryAgents({ unassigned: true, kind: ['collector'] }).total).toBe(4);
    const facets = db.facets({ fleet: ['linux-hosts'] });
    expect(facets.total).toBe(120);
    expect(facets.fleets.find((f) => f.value === 'linux-hosts')!.count).toBe(120);
  });

  it('renders blueprints with parameter sources and validation issues', () => {
    const r = renderMock({
      blueprint: 'edge-windows',
      attributes: { 'site.id': '1001', 'site.type': 'edge' },
      overrides: { memoryLimitPercent: 95, gatewayEndpoint: 'gw:4317' },
    });
    expect(r.layers).toEqual(['base', 'edge-windows']);
    expect(r.parameterSources['siteId']).toBe('attribute');
    expect(r.parameterSources['memoryLimitPercent']).toBe('override');
    expect(r.issues.map((i) => i.code)).toContain('parameter_out_of_range');
    expect(r.body).toContain('gw:4317');
    const missing = renderMock({ blueprint: 'base', attributes: {} });
    expect(missing.issues.map((i) => i.code)).toContain('missing_required_parameter');
  });

  it('simulation advances approved waves and emits live events', () => {
    const db = getDb();
    const mr = db.findRollout('8a7d9c20-3b1e-4f65-b0a2-6c4e1d9f5a22')!;
    const events: string[] = [];
    db.subscribe((t) => events.push(t));
    // not approved yet: nothing starts
    for (let i = 0; i < 5; i++) db.tick();
    expect(mr.targets.filter((t) => t.wave === 1 && t.status !== 'pending')).toHaveLength(0);
    mr.rollout.waves[1]!.status = 'running';
    mr.rollout.status = 'running';
    for (let i = 0; i < 400 && mr.rollout.waves[1]!.status === 'running'; i++) db.tick();
    expect(mr.rollout.waves[1]!.status).toBe('completed');
    expect(mr.rollout.waves[2]!.status).toBe('running');
    expect(mr.rollout.counts.succeeded).toBeGreaterThan(60);
    expect(events).toContain('agent.updated');
    expect(events).toContain('rollout.updated');
    // agents of succeeded targets report the new version
    const t = mr.targets.find((x) => x.wave === 1 && x.status === 'succeeded')!;
    expect(db.agentOf(t.agentId)!.version).toBe('0.162.0');
  });

  it('serves the rollout targets in wave order or, with sort=activity, running work first', async () => {
    const id = '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11';
    getDb().sessionRole = 'viewer';
    const get = async (q: string) =>
      fetch(`${window.location.origin}/api/v1/rollouts/${id}/targets${q}`);
    const statuses = async (q: string) =>
      (
        (await (await get(q)).json()) as {
          items: { status: string; wave: number; agentId: string }[];
        }
      ).items;

    const byWave = await statuses('?limit=100');
    expect(byWave[0]!.wave).toBe(0);
    expect((await statuses('?sort=wave&limit=100')).map((t) => t.status)).toEqual(
      byWave.map((t) => t.status),
    );

    const active = await statuses('?sort=activity&limit=100');
    expect(['in_progress', 'verifying']).toContain(active[0]!.status);
    const ranks = active.map((t) => ['in_progress', 'verifying'].indexOf(t.status) >= 0);
    const firstOther = ranks.indexOf(false);
    expect(firstOther).toBeGreaterThan(0);
    expect(ranks.slice(firstOther).every((r) => !r)).toBe(true);
    expect(active.at(-1)!.status).not.toBe('in_progress');

    const first = active[0]!;
    const one = (await (await get(`?sort=activity&agent=${first.agentId}&limit=1`)).json()) as {
      total: number;
      items: { agentId: string }[];
    };
    expect(one.total).toBe(1);
    expect(one.items[0]!.agentId).toBe(first.agentId);

    const bad = await get('?sort=nope');
    expect(bad.status).toBe(400);
    expect((await get('?sort=-activity')).status).toBe(400);
  });
});

/** The version rollout of the data center fleet: policy "standard" (healthWait 2m), five targets in the health wait. */
const DC_ROLLOUT = '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11';
const STANDARD_WAIT_MS = 120_000;

describe('mock engine: the health wait', () => {
  /**
   * The data center rollout in `status` (the way the pause action of the mock puts it there), its
   * five targets in the health wait, the instants their waits end (ascending) and a clock that the
   * test sets.
   */
  function dataCenter(status: RolloutStatus) {
    const db = getDb();
    const mr = db.findRollout(DC_ROLLOUT)!;
    if (mr.rollout.status !== status) db.setRolloutStatus(mr, status, 'test');
    const verifying = mr.targets.filter((t) => t.status === 'verifying');
    expect(verifying).toHaveLength(5);
    const ends = verifying
      .map((t) => Date.parse(t.verifiedAt!) + STANDARD_WAIT_MS)
      .sort((a, b) => a - b);
    let now = ends[0]! - 1000;
    db.now = () => now;
    return {
      db,
      mr,
      verifying,
      ends,
      setClock: (ms: number) => {
        now = ms;
      },
      succeeded: () => verifying.filter((t) => t.status === 'succeeded').length,
    };
  }

  // The engine evaluates the targets of a rollout that is running, paused or waiting for an
  // approval (evalInFlight runs before the status switch in internal/rollout/tick.go).
  it.each(['running', 'paused', 'waiting_approval'] as const)(
    'finishes a target when its wait is over while the rollout is %s',
    (status) => {
      const { db, mr, verifying, ends, setClock, succeeded } = dataCenter(status);
      const events: string[] = [];
      db.subscribe((type) => events.push(type));

      // one millisecond before the first wait ends, every target still waits
      setClock(ends[0]! - 1);
      db.tick();
      expect(succeeded()).toBe(0);
      expect(verifying.every((t) => t.status === 'verifying')).toBe(true);

      // a target is done at the instant its own wait ends, and not before: three of five
      setClock(ends[2]!);
      db.tick();
      expect(succeeded()).toBe(3);
      expect(events).toContain('rollout.updated');

      setClock(ends[4]!);
      db.tick();
      expect(succeeded()).toBe(5);
      for (const t of verifying) {
        expect(t.finishedAt).toBeDefined();
        expect(Date.parse(t.finishedAt!)).toBeGreaterThanOrEqual(
          Date.parse(t.verifiedAt!) + STANDARD_WAIT_MS,
        );
      }
      // the counts that the page and the live events show follow the targets
      expect(mr.rollout.counts.succeeded).toBe(
        mr.targets.filter((t) => t.status === 'succeeded').length,
      );
    },
  );

  // A paused rollout starts nothing new and moves no wave on, but it finishes what is in flight.
  it.each(['paused', 'waiting_approval'] as const)(
    'starts and advances nothing else while the rollout is %s',
    (status) => {
      const { db, mr, verifying, ends, setClock, succeeded } = dataCenter(status);
      const others = () => mr.targets.filter((t) => !verifying.includes(t)).map((t) => t.status);
      const waves = () => mr.rollout.waves.map((w) => w.status);
      const [othersBefore, wavesBefore] = [others(), waves()];
      expect(othersBefore).toContain('in_progress');
      expect(othersBefore).toContain('pending');

      setClock(ends[4]!);
      for (let i = 0; i < 20; i++) db.tick();

      expect(succeeded()).toBe(5);
      expect(mr.rollout.status).toBe(status);
      expect(others()).toEqual(othersBefore);
      expect(waves()).toEqual(wavesBefore);
    },
  );

  // The engine does not evaluate the targets of these rollouts: they stay as they are until an
  // operator decides (docs/concepts/rollouts.md, "Health wait").
  it.each(['failed', 'rolling_back', 'aborted'] as const)(
    'leaves the targets in the health wait of a %s rollout alone',
    (status) => {
      const { db, verifying, ends, setClock, succeeded } = dataCenter(status);
      setClock(ends[4]! + 60 * 60 * 1000);
      for (let i = 0; i < 5; i++) db.tick();
      expect(succeeded()).toBe(0);
      expect(verifying.every((t) => t.status === 'verifying')).toBe(true);
    },
  );
});
