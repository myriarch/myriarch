// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { ApiError } from '@/api/errors';
import type { FleetVersionTarget, TargetProgress, VersionOption } from '@/api/types';
import {
  clearWouldDowngrade,
  compareReference,
  downloadPercentOf,
  formatAnnotation,
  formatDurationMs,
  highestSelectable,
  isFinalPhase,
  phaseTone,
  selectionAnnotation,
  selectionIssueCodes,
  sortFleetOptions,
  splitAnnotations,
  stepDurationMs,
  stepPercent,
  stepView,
} from './updates';

const option = (version: string, over: Partial<VersionOption> = {}): VersionOption => ({
  version,
  selectable: true,
  known: true,
  prerelease: false,
  isLatest: false,
  isCurrentTarget: false,
  isGitVersion: false,
  isDowngrade: false,
  agentsToUpdate: 1,
  breakingCount: 0,
  deprecationCount: 0,
  ...over,
});

describe('stepView', () => {
  it('maps every step status to a distinct presentation (icon carries the state, not only color)', () => {
    expect(stepView('pending')).toMatchObject({ tone: 'neutral', icon: 'pending', dashed: false });
    expect(stepView('running')).toMatchObject({ tone: 'info', icon: 'running', animated: true });
    expect(stepView('done')).toMatchObject({ tone: 'success', icon: 'done' });
    expect(stepView('failed')).toMatchObject({ tone: 'danger', icon: 'failed' });
    expect(stepView('skipped')).toMatchObject({ tone: 'neutral', icon: 'skipped', dashed: true });
    const icons = (['pending', 'running', 'done', 'failed', 'skipped'] as const).map(
      (s) => stepView(s).icon,
    );
    expect(new Set(icons).size).toBe(5);
  });

  it('phases: rollback is violet-ish (drift), failure red, final phases are final', () => {
    expect(phaseTone('succeeded')).toBe('success');
    expect(phaseTone('failed')).toBe('danger');
    expect(phaseTone('rolled_back')).toBe('drift');
    expect(isFinalPhase('rolled_back')).toBe(true);
    expect(isFinalPhase('health_wait')).toBe(false);
  });
});

describe('progress numbers', () => {
  const p = (over: Partial<TargetProgress>): TargetProgress => ({
    phase: 'downloading',
    source: 'updater',
    steps: [],
    updatedAt: 'x',
    ...over,
  });
  it('download percent only while downloading, clamped and rounded', () => {
    expect(downloadPercentOf(p({ downloadPercent: 41.6 }))).toBe(42);
    expect(downloadPercentOf(p({ downloadPercent: 120 }))).toBe(100);
    expect(downloadPercentOf(p({ phase: 'verifying', downloadPercent: 100 }))).toBeUndefined();
    expect(downloadPercentOf(undefined)).toBeUndefined();
    expect(downloadPercentOf(p({}))).toBeUndefined();
  });

  it('stepPercent belongs to the running download step only', () => {
    const prog = p({ downloadPercent: 42 });
    expect(stepPercent(prog, { name: 'downloading', status: 'running' })).toBe(42);
    expect(stepPercent(prog, { name: 'downloading', status: 'done' })).toBeUndefined();
    expect(stepPercent(prog, { name: 'verifying', status: 'running' })).toBeUndefined();
  });

  it('step duration counts up for running steps and is fixed for finished ones', () => {
    const t0 = Date.parse('2026-10-07T10:00:00Z');
    expect(
      stepDurationMs(
        { name: 'installing', status: 'running', startedAt: '2026-10-07T10:00:00Z' },
        t0 + 3500,
      ),
    ).toBe(3500);
    expect(
      stepDurationMs(
        {
          name: 'installing',
          status: 'done',
          startedAt: '2026-10-07T10:00:00Z',
          finishedAt: '2026-10-07T10:00:02Z',
        },
        t0 + 99_000,
      ),
    ).toBe(2000);
    expect(stepDurationMs({ name: 'installing', status: 'pending' }, t0)).toBeUndefined();
    expect(
      stepDurationMs({ name: 'installing', status: 'skipped', startedAt: 'x' }, t0),
    ).toBeUndefined();
  });

  it('formats durations', () => {
    expect(formatDurationMs(1234)).toBe('1.2 s');
    expect(formatDurationMs(34_000)).toBe('34 s');
    expect(formatDurationMs(125_000)).toBe('2 min 05 s');
  });
});

describe('version options', () => {
  it('highestSelectable picks the highest selectable version, ignoring disabled ones', () => {
    const opts = [
      option('0.163.0', { selectable: false, reasonCode: 'outside_frame' }),
      option('0.162.0'),
      option('0.161.0'),
    ];
    expect(highestSelectable(opts)?.version).toBe('0.162.0');
    expect(highestSelectable([option('0.161.0', { selectable: false })])).toBeUndefined();
    expect(highestSelectable(undefined)).toBeUndefined();
    // numeric, not lexical
    expect(highestSelectable([option('0.9.0'), option('0.10.0')])?.version).toBe('0.10.0');
  });

  it('compare reference is the effective target, else the Git target', () => {
    const t = (over: Partial<FleetVersionTarget>): FleetVersionTarget => ({
      source: 'git',
      selectionEnabled: true,
      ...over,
    });
    expect(compareReference(t({ version: '0.162.0', gitVersion: '0.161.0' }))).toBe('0.162.0');
    expect(compareReference(t({ source: 'hold', gitVersion: '0.161.0' }))).toBe('0.161.0');
    expect(compareReference(t({ source: 'none' }))).toBeUndefined();
  });

  it('clearing downgrades when a member runs a higher version than the Git target', () => {
    expect(clearWouldDowngrade('0.161.0', ['0.161.0', '0.162.0'])).toBe(true);
    expect(clearWouldDowngrade('0.162.0', ['0.161.0', '0.162.0'])).toBe(false);
    expect(clearWouldDowngrade(undefined, ['0.162.0'])).toBe(false);
  });

  it('sorts fleets selectable first, then by name', () => {
    const f = (fleet: string, selectable: boolean) => ({
      fleet,
      selectable,
      target: { source: 'git' as const, selectionEnabled: true },
      agentsToUpdate: 0,
    });
    expect(
      sortFleetOptions([f('b', false), f('z', true), f('a', false), f('m', true)]).map(
        (x) => x.fleet,
      ),
    ).toEqual(['m', 'z', 'a', 'b']);
  });
});

describe('error codes', () => {
  const problem = (codes: string[]) =>
    new ApiError({
      status: 422,
      code: 'validation_failed',
      message: 'x',
      problem: {
        type: 'about:blank',
        title: 't',
        status: 422,
        code: 'validation_failed',
        issues: codes.map((code) => ({ severity: 'error' as const, code, message: code })),
      },
    });
  it('extracts the known selection issue codes once and ignores the rest', () => {
    expect(
      selectionIssueCodes(
        problem(['version_not_allowed', 'reason_required', 'version_not_allowed', 'weird']),
      ),
    ).toEqual(['version_not_allowed', 'reason_required']);
    expect(selectionIssueCodes(new Error('x'))).toEqual([]);
  });
});

describe('rollout annotations', () => {
  it('renders an object annotation as k=v pairs, never [object Object]', () => {
    expect(formatAnnotation('updateParams', { workload: 'statefulset/otel-gateway' })).toEqual([
      'workload=statefulset/otel-gateway',
    ]);
    expect(formatAnnotation('updateParams', { a: 1, b: { c: true } })).toEqual([
      'a=1',
      'b={"c":true}',
    ]);
  });

  it('scalars as text, arrays comma separated, empty values vanish', () => {
    expect(formatAnnotation('distribution', 'otelcol-contrib')).toEqual(['otelcol-contrib']);
    expect(formatAnnotation('x', 42)).toEqual(['42']);
    expect(formatAnnotation('x', true)).toEqual(['true']);
    expect(formatAnnotation('x', ['a', 'b', { c: 1 }])).toEqual(['a, b, {"c":1}']);
    expect(formatAnnotation('x', [])).toEqual([]);
    expect(formatAnnotation('x', '')).toEqual([]);
    expect(formatAnnotation('x', null)).toEqual([]);
    expect(formatAnnotation('x', {})).toEqual([]);
  });

  it('shows only executorRef/distribution/updateParams; selection goes to the header, the rest is technical', () => {
    const { visible, technical } = splitAnnotations({
      healthTrack: { 'agent-1': '2026-10-07T10:00:00Z' },
      updateParams: { workload: 'statefulset/otel-gateway' },
      executorRef: '',
      distribution: 'otelcol-contrib',
      selection: { version: '0.162.0', selectedBy: 'Anna' },
      ackedFailures: [],
      rollbackSent: false,
      somethingNew: { a: 1 },
    });
    expect(visible).toEqual([
      { key: 'distribution', items: ['otelcol-contrib'] },
      { key: 'updateParams', items: ['workload=statefulset/otel-gateway'] },
    ]);
    expect(Object.keys(technical).sort()).toEqual([
      'ackedFailures',
      'healthTrack',
      'rollbackSent',
      'somethingNew',
    ]);
    expect(splitAnnotations(undefined)).toEqual({ visible: [], technical: {} });
  });

  it('reads the selection annotation defensively', () => {
    expect(
      selectionAnnotation({
        selection: { version: '0.162.0', selectedBy: 'Anna', selectedAt: 't', reason: '' },
      }),
    ).toEqual({ version: '0.162.0', selectedBy: 'Anna', selectedAt: 't', reason: undefined });
    expect(selectionAnnotation({ selection: 'oops' })).toEqual({});
    expect(selectionAnnotation(undefined)).toEqual({});
  });
});
