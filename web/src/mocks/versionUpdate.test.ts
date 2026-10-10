// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { getDb } from './db';
import { inFrame, matchesRange } from './frame';

const actor = { name: 'operator' };
const issues = (r: ReturnType<ReturnType<typeof getDb>['selectVersion']>) =>
  r.ok ? [] : (r.issues ?? []).map((i) => i.code);

describe('frame matching', () => {
  it('evaluates comma separated comparators', () => {
    expect(matchesRange('>=0.161.0, <=0.162.0', '0.162.0')).toBe(true);
    expect(matchesRange('>=0.161.0, <=0.162.0', '0.163.0')).toBe(false);
    expect(matchesRange('>=0.161.0, <0.162.0', '0.162.0')).toBe(false);
    expect(matchesRange('>=0.9.0', '0.10.0')).toBe(true);
    expect(matchesRange('>=0.161', '0.162.0')).toBe(false);
  });
  it('a frame is either a range or a list', () => {
    expect(
      inFrame(
        { enabled: true, allowDowngrade: false, requireReason: false, versions: ['0.162.0'] },
        '0.162.0',
      ),
    ).toBe(true);
    expect(
      inFrame(
        { enabled: true, allowDowngrade: false, requireReason: false, versions: ['0.162.0'] },
        '0.161.0',
      ),
    ).toBe(false);
    expect(inFrame(undefined, '0.162.0')).toBe(false);
  });
});

describe('mock version selection', () => {
  it('resolves the effective target: git, ui and hold', () => {
    const db = getDb();
    expect(db.effectiveTarget('k8s-gateway')).toMatchObject({ source: 'git', version: '0.161.0' });
    expect(db.effectiveTarget('datacenter')).toMatchObject({
      source: 'ui',
      version: '0.162.0',
      gitVersion: '0.161.0',
    });
    const hold = db.effectiveTarget('edge-sites');
    expect(hold).toMatchObject({ source: 'hold', gitVersion: '0.161.0' });
    expect(hold.version).toBeUndefined();
    expect(db.effectiveTarget('linux-hosts')).toMatchObject({
      source: 'git',
      selectionEnabled: false,
    });
  });

  it('the effective target is what the collectors are assigned (and the dashboard counts)', () => {
    const db = getDb();
    const datacenterMember = db
      .collectors()
      .find((e) => e.agent.assignment.fleetName === 'datacenter')!;
    expect(datacenterMember.agent.assignment.targetVersion).toBe('0.162.0');
    const holdMember = db.collectors().find((e) => e.agent.assignment.fleetName === 'edge-sites')!;
    expect(holdMember.agent.assignment.targetVersion).toBeUndefined();
    expect(db.fleetCounts('datacenter').targetVersion).toBe('0.162.0');
  });

  it('lists the options newest first with reasons for the ones that cannot be selected', () => {
    const o = getDb().versionOptions('datacenter')!;
    expect(o.options.map((x) => x.version)).toEqual(['0.163.0', '0.162.0', '0.161.0']);
    const by = Object.fromEntries(o.options.map((x) => [x.version, x]));
    expect(by['0.163.0']).toMatchObject({ selectable: true, isLatest: true });
    expect(by['0.162.0']).toMatchObject({
      selectable: false,
      reasonCode: 'current_target',
      isCurrentTarget: true,
    });
    expect(by['0.161.0']).toMatchObject({
      selectable: false,
      reasonCode: 'downgrade_not_allowed',
      isGitVersion: true,
      isDowngrade: true,
    });
    expect(by['0.163.0']!.breakingCount).toBeGreaterThan(0);
    expect(o.versionPolicy).toBe('standard');
  });

  it('refuses with the documented codes', () => {
    const db = getDb();
    expect(db.selectVersion('nope', { version: '0.162.0' }, actor)).toMatchObject({
      ok: false,
      status: 404,
    });
    expect(db.selectVersion('k8s-gateway', { version: 'v0.162.0' }, actor)).toMatchObject({
      ok: false,
      status: 400,
    });
    expect(issues(db.selectVersion('k8s-gateway', { version: '0.163.0' }, actor))).toEqual([
      'version_not_allowed',
    ]);
    expect(issues(db.selectVersion('k8s-gateway', { version: '0.162.0-rc.1' }, actor))).toEqual([
      'prerelease_not_allowed',
    ]);
    expect(
      issues(db.selectVersion('datacenter', { version: '0.161.0', reason: 'x' }, actor)),
    ).toEqual(['downgrade_not_allowed']);
    expect(issues(db.selectVersion('datacenter', { version: '0.163.0' }, actor))).toEqual([
      'reason_required',
    ]);
    expect(db.selectVersion('linux-hosts', { version: '0.163.0' }, actor)).toMatchObject({
      ok: false,
      status: 412,
    });
    expect(
      db.selectVersion(
        'k8s-gateway',
        { version: '0.162.0', expectedTargetVersion: '0.160.0' },
        actor,
      ),
    ).toMatchObject({ ok: false, status: 409 });
    // every refusal is audited as a failure, nothing was stored
    expect(
      db.audit.filter((a) => a.action === 'fleet.version.select' && a.result === 'failure').length,
    ).toBe(6);
    expect(db.selections.has('k8s-gateway')).toBe(false);
  });

  it('a version that already is the target is not stored again', () => {
    const db = getDb();
    const r = db.selectVersion('k8s-gateway', { version: '0.161.0' }, actor);
    expect(r).toMatchObject({ ok: true, response: { planned: false } });
    expect(db.selections.has('k8s-gateway')).toBe(false);
  });

  it('is blocked while a version rollout of the fleet failed', () => {
    const db = getDb();
    const mr = db.findRollout('5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11')!;
    mr.rollout.status = 'failed';
    expect(db.versionOptions('datacenter')).toMatchObject({
      allowed: false,
      notAllowedCode: 'blocking_rollout',
    });
    expect(
      db.selectVersion('datacenter', { version: '0.163.0', reason: 'x' }, actor),
    ).toMatchObject({
      ok: false,
      status: 412,
    });
  });

  it('selecting stores who/when/why, creates the UI rollout and emits the live events', () => {
    const db = getDb();
    const events: string[] = [];
    db.subscribe((t) => events.push(t));
    const r = db.selectVersion('k8s-gateway', { version: '0.162.0', reason: 'because' }, actor);
    expect(r.ok && r.response.planned && r.response.rolloutId).toBeTruthy();
    expect(db.selections.get('k8s-gateway')).toMatchObject({
      selectedBy: 'operator',
      reason: 'because',
      gitVersion: '0.161.0',
    });
    expect(events).toEqual(
      expect.arrayContaining(['fleet.updated', 'rollout.updated', 'stats.changed']),
    );
    expect(db.effectiveTarget('k8s-gateway')).toMatchObject({
      source: 'ui',
      version: '0.162.0',
      gitVersion: '0.161.0',
    });
    expect(db.audit.map((a) => a.action)).toEqual(
      expect.arrayContaining(['fleet.version.select', 'rollout.create']),
    );
    db.dispose();
  });

  it('clearing needs the reason where the frame demands it and is a no-op without selection', () => {
    const db = getDb();
    expect(db.clearSelection('datacenter', undefined, actor)).toMatchObject({
      ok: false,
      status: 422,
    });
    expect(db.clearSelection('k8s-gateway', undefined, actor)).toMatchObject({ ok: true });
    expect(db.audit.some((a) => a.action === 'fleet.version.clear')).toBe(false);
    const r = db.clearSelection('datacenter', 'rolled back', actor);
    expect(r).toMatchObject({ ok: true, target: { source: 'git', version: '0.161.0' } });
    expect(db.findRollout('5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11')!.rollout.status).toBe(
      'superseded',
    );
    expect(db.audit[0]).toMatchObject({
      action: 'fleet.version.clear',
      details: { version: '0.162.0', reason: 'rolled back' },
    });
  });
});
