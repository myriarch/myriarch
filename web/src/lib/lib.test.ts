// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  compareVersions,
  formatBytes,
  formatPercent,
  formatRelative,
  formatUtc,
  formatNumber,
} from './format';
import { toYaml, parseLooseValue } from './yaml';
import { windowSegments, mergeSegments } from './windows';
import { readCookie } from '@/api/session';
import { toApiError } from '@/api/errors';
import { safeRedirectPath } from '@/auth/useAuth';

describe('format', () => {
  it('compares versions numerically', () => {
    expect(compareVersions('0.162.0', '0.161.0')).toBe(1);
    expect(compareVersions('0.99.0', '0.100.0')).toBe(-1);
    expect(compareVersions('0.162.0', 'v0.162.0')).toBe(0);
    expect(compareVersions('0.162', '0.162.1')).toBe(-1);
  });
  it('formats numbers with the active language', () => {
    expect(formatNumber('de', 1095)).toBe('1.095');
    expect(formatNumber('en', 1095)).toBe('1,095');
    expect(formatNumber('de', undefined)).toBe('–');
    expect(formatPercent('de', 97.65, 1)).toContain('97,7');
  });
  it('formats bytes and UTC', () => {
    expect(formatBytes('en', 1536)).toBe('1.5 KB');
    expect(formatUtc('2026-10-06T09:30:00.123Z')).toBe('2026-10-06 09:30:00 UTC');
    expect(formatUtc(undefined)).toBe('');
  });
  it('formats relative times', () => {
    const now = Date.parse('2026-10-06T10:00:00Z');
    expect(formatRelative('en', '2026-10-06T09:55:00Z', now)).toBe('5 minutes ago');
    expect(formatRelative('de', '2026-10-06T09:55:00Z', now)).toBe('vor 5 Minuten');
  });
});

describe('yaml emitter', () => {
  it('emits nested maps, lists and quotes ambiguous strings', () => {
    const y = toYaml({
      receivers: { otlp: { protocols: { grpc: { endpoint: 'localhost:4317' } } } },
      list: ['a', 'b'],
      items: [{ key: 'x', value: 'yes' }],
      flag: true,
      n: 5,
      empty: {},
      s: '[[ .params.x ]]',
      z: '0.5',
    });
    expect(y).toContain(
      'receivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: "localhost:4317"',
    );
    expect(y).toContain('list:\n  - a\n  - b');
    expect(y).toContain('items:\n  - key: x\n    value: "yes"');
    expect(y).toContain('flag: true');
    expect(y).toContain('empty: {}');
    expect(y).toContain('s: "[[ .params.x ]]"');
    expect(y).toContain('z: "0.5"');
  });
  it('parses loose override values', () => {
    expect(parseLooseValue('42')).toBe(42);
    expect(parseLooseValue('true')).toBe(true);
    expect(parseLooseValue('["a","b"]')).toEqual(['a', 'b']);
    expect(parseLooseValue('otel-gw:4317')).toBe('otel-gw:4317');
  });
});

describe('maintenance windows', () => {
  it('splits windows crossing midnight onto the next day', () => {
    const seg = windowSegments([
      { days: ['Mon'], start: '21:30', end: '06:30', timezone: 'agent' },
    ]);
    expect(seg.Mon).toEqual([[21 * 60 + 30, 1440]]);
    expect(seg.Tue).toEqual([[0, 6 * 60 + 30]]);
    expect(seg.Wed).toEqual([]);
  });
  it('treats start == end as the whole day and empty days as every day', () => {
    const seg = windowSegments([
      { days: ['Sun'], start: '00:00', end: '00:00', timezone: 'agent' },
      { start: '01:00', end: '05:00', timezone: 'Europe/Berlin' },
    ]);
    expect(seg.Sun).toEqual([[0, 1440]]);
    expect(seg.Thu).toEqual([[60, 300]]);
  });
  it('merges overlapping segments', () => {
    expect(
      mergeSegments([
        [0, 100],
        [50, 200],
        [300, 400],
      ]),
    ).toEqual([
      [0, 200],
      [300, 400],
    ]);
  });
});

describe('api helpers', () => {
  it('reads cookies', () => {
    expect(readCookie('myriarch_csrf', 'a=1; myriarch_csrf=tok%20en; b=2')).toBe('tok en');
    expect(readCookie('missing', 'a=1')).toBeUndefined();
  });
  it('maps problem bodies to ApiError with stable code', () => {
    const e = toApiError(422, {
      type: 'about:blank',
      title: 'Validation failed',
      status: 422,
      code: 'validation_failed',
      detail: 'bad',
      issues: [{ severity: 'error', code: 'x', message: 'm' }],
      requestId: 'r1',
    });
    expect(e.code).toBe('validation_failed');
    expect(e.issues).toHaveLength(1);
    expect(e.requestId).toBe('r1');
    expect(toApiError(502, '<html>bad gateway</html>').code).toBe('unknown');
    expect(
      toApiError(500, { code: 'something_new', status: 500, title: 't', type: 'x' }).code,
    ).toBe('unknown');
  });
  it('only allows same-origin redirect targets', () => {
    expect(safeRedirectPath('/agents?x=1')).toBe('/agents?x=1');
    expect(safeRedirectPath('//evil.com')).toBe('/');
    expect(safeRedirectPath('https://evil.com')).toBe('/');
    expect(safeRedirectPath('/\\evil.com')).toBe('/');
    expect(safeRedirectPath('/login')).toBe('/');
    expect(safeRedirectPath(null)).toBe('/');
  });
});
