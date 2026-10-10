// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { STEP_MS, scriptDurationMs, simulateUpdate, type UpdateScript } from './updateSim';

const T0 = Date.parse('2026-10-07T10:00:00Z');
const at = (script: UpdateScript, ms: number) =>
  simulateUpdate({ startedAt: T0, now: T0 + ms, script, from: '0.161.0', to: '0.162.0' });
const statuses = (script: UpdateScript, ms: number) =>
  Object.fromEntries(at(script, ms).progress.steps.map((s) => [s.name, s.status]));

describe('simulated update (success)', () => {
  it('starts with offered done and the download running', () => {
    const r = at('success', 0);
    expect(r.progress.phase).toBe('downloading');
    expect(r.targetStatus).toBe('in_progress');
    expect(statuses('success', 0)).toMatchObject({
      offered: 'done',
      downloading: 'running',
      verifying: 'pending',
    });
    expect(r.progress.steps.map((s) => s.name)).not.toContain('rolling_back');
    expect(r.final).toBe(false);
  });

  it('moves through every phase in order', () => {
    const phases = [0, 1, 2, 3, 4, 5].map((i) => at('success', i * STEP_MS + 10).progress.phase);
    expect(phases).toEqual([
      'downloading',
      'verifying',
      'installing',
      'restarting',
      'health_check',
      'health_wait',
    ]);
  });

  it('reports a growing download percentage and only then', () => {
    expect(at('success', STEP_MS / 2).progress.downloadPercent).toBe(50);
    expect(at('success', STEP_MS + 1).progress.downloadPercent).toBeUndefined();
  });

  it('the health wait is the verifying target status of myriarchd', () => {
    expect(at('success', 5 * STEP_MS + 1).targetStatus).toBe('verifying');
  });

  it('ends succeeded with every step done', () => {
    const r = at('success', scriptDurationMs('success'));
    expect(r.final).toBe(true);
    expect(r.progress.phase).toBe('succeeded');
    expect(r.targetStatus).toBe('succeeded');
    expect(Object.values(statuses('success', scriptDurationMs('success')))).toEqual(
      Array(7).fill('done'),
    );
  });
});

describe('simulated update (rollback)', () => {
  it('fails the health check and the host rolls back', () => {
    const mid = at('rollback', 5 * STEP_MS + 10);
    expect(mid.progress.phase).toBe('rolling_back');
    expect(mid.final).toBe(false);
    expect(statuses('rollback', 5 * STEP_MS + 10)).toMatchObject({
      health_check: 'failed',
      rolling_back: 'running',
      health_wait: 'skipped',
    });
    const end = at('rollback', scriptDurationMs('rollback'));
    expect(end.final).toBe(true);
    expect(end.progress.phase).toBe('rolled_back');
    expect(end.targetStatus).toBe('failed');
    expect(end.progress.error).toMatch(/health check failed/);
    expect(statuses('rollback', scriptDurationMs('rollback'))).toMatchObject({
      health_check: 'failed',
      rolling_back: 'done',
    });
  });
});

describe('simulated update (verification fails)', () => {
  it('stops before anything was changed and skips the rest', () => {
    const end = at('verify_fail', scriptDurationMs('verify_fail'));
    expect(end.progress.phase).toBe('failed');
    expect(end.targetStatus).toBe('failed');
    expect(statuses('verify_fail', scriptDurationMs('verify_fail'))).toMatchObject({
      downloading: 'done',
      verifying: 'failed',
      installing: 'skipped',
      health_wait: 'skipped',
    });
    expect(end.progress.steps.map((s) => s.name)).not.toContain('rolling_back');
  });
});
