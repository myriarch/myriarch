// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  DEFAULT_STATE,
  countActiveFilters,
  fromSavedQuery,
  parseAgentSearch,
  toApiQuery,
  toSavedQuery,
  toSearchParams,
  toggleAttr,
  attrValues,
} from './filters';

const parse = (qs: string) => parseAgentSearch(new URLSearchParams(qs));

describe('agent filters <-> URL', () => {
  it('parses an empty query to the defaults', () => {
    expect(parse('')).toEqual(DEFAULT_STATE);
  });

  it('round-trips every filter dimension', () => {
    const qs =
      'q=edge&kind=collector&status=online&status=offline&healthy=false&fleet=windows-hosts&fleet=linux-hosts&version=0.161.0&outdated=true&os=windows&attr=site.type%3Dedge&attr=site.type%3Doffice&attr=labels.pilot%3Dtrue&configSync=failed&configSync=drift&sort=-version&limit=100&offset=200';
    const state = parse(qs);
    expect(state).toMatchObject({
      q: 'edge',
      kind: ['collector'],
      status: ['online', 'offline'],
      healthy: false,
      fleet: ['windows-hosts', 'linux-hosts'],
      version: ['0.161.0'],
      outdated: true,
      os: ['windows'],
      attr: ['site.type=edge', 'site.type=office', 'labels.pilot=true'],
      configSync: ['failed', 'drift'],
      sort: '-version',
      limit: 100,
      offset: 200,
    });
    expect(parse(toSearchParams(state).toString())).toEqual(state);
  });

  it('omits defaults so URLs stay short', () => {
    expect(toSearchParams(DEFAULT_STATE).toString()).toBe('');
    expect(
      toSearchParams({ ...DEFAULT_STATE, limit: 50, sort: 'hostname', offset: 0 }).toString(),
    ).toBe('');
  });

  it('drops invalid values instead of failing', () => {
    const s = parse(
      'status=bogus&kind=nope&sort=evil&limit=7&offset=-3&attr=novalue&attr==x&healthy=maybe&configSync=zzz',
    );
    expect(s.status).toEqual([]);
    expect(s.kind).toEqual([]);
    expect(s.sort).toBe('hostname');
    expect(s.limit).toBe(50);
    expect(s.offset).toBe(0);
    expect(s.attr).toEqual([]);
    expect(s.healthy).toBeUndefined();
    expect(s.configSync).toEqual([]);
  });

  it('treats unassigned as exclusive with fleet values', () => {
    const s = parse('unassigned=true&fleet=linux-hosts');
    expect(s.unassigned).toBe(true);
    expect(s.fleet).toEqual([]);
    expect(toApiQuery(s)).toMatchObject({ unassigned: true });
    expect(toApiQuery(s).fleet).toBeUndefined();
  });

  it('builds the API query with repeated array params', () => {
    const q = toApiQuery(
      parse('status=online&version=0.161.0&version=0.162.0&limit=200&offset=400&sort=-lastSeenAt'),
    );
    expect(q).toEqual({
      status: ['online'],
      version: ['0.161.0', '0.162.0'],
      limit: 200,
      offset: 400,
      sort: '-lastSeenAt',
    });
  });

  it('counts active filter dimensions', () => {
    expect(countActiveFilters(DEFAULT_STATE)).toBe(0);
    expect(countActiveFilters(parse('q=x&status=online&status=offline&fleet=a&fleet=b'))).toBe(3);
  });

  it('saved filters keep filters and sort but not the page', () => {
    const state = parse('status=online&attr=site.type%3Dedge&sort=-version&limit=100&offset=100');
    const saved = toSavedQuery(state);
    expect(saved).toEqual({ status: ['online'], attr: ['site.type=edge'], sort: '-version' });
    const restored = fromSavedQuery(saved);
    expect(restored.status).toEqual(['online']);
    expect(restored.sort).toBe('-version');
    expect(restored.offset).toBe(0);
  });

  it('toggles attribute facets per key', () => {
    let attr = toggleAttr([], 'site.type', 'edge');
    attr = toggleAttr(attr, 'site.type', 'office');
    expect(attrValues(attr, 'site.type')).toEqual(['edge', 'office']);
    attr = toggleAttr(attr, 'site.type', 'edge');
    expect(attr).toEqual(['site.type=office']);
  });
});
