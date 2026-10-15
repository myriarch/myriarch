// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { Rollout, RolloutStatus } from '@/api/types';
import {
  DEFAULT_HEALTH_WAIT_MS,
  formatCountdown,
  formatWait,
  healthWaitEnd,
  healthWaitMs,
  healthWaitOf,
  isEvaluated,
  latestVerifiedAt,
  parseGoDuration,
  remainingMs,
} from './healthWait';

const rollout = (
  status: RolloutStatus,
  policy: Partial<Rollout['policy']> = {},
): Pick<Rollout, 'status' | 'policyName' | 'policy'> => ({
  status,
  policyName: 'canary-then-all',
  policy: {
    name: 'canary-then-all',
    waves: [],
    failureThreshold: {},
    source: {
      path: 'policies/canary-then-all.yaml',
      commit: '9f3c2a17d8e44b60a5c1d2e3f4a5b6c7d8e9f001',
    },
    ...policy,
  },
});

describe('parseGoDuration', () => {
  it('reads what time.Duration.String writes and what GitOps files contain', () => {
    expect(parseGoDuration('5m0s')).toBe(300_000);
    expect(parseGoDuration('1m0s')).toBe(60_000);
    expect(parseGoDuration('1h0m0s')).toBe(3_600_000);
    expect(parseGoDuration('2h30m0s')).toBe(9_000_000);
    expect(parseGoDuration('30s')).toBe(30_000);
    expect(parseGoDuration('5m')).toBe(300_000);
    expect(parseGoDuration('1.5s')).toBe(1_500);
    expect(parseGoDuration('250ms')).toBe(250);
    expect(parseGoDuration('1.1s')).toBe(1_100);
    expect(parseGoDuration('500us')).toBe(1);
    expect(parseGoDuration('0')).toBe(0);
    expect(parseGoDuration('0s')).toBe(0);
    expect(parseGoDuration(' 2m ')).toBe(120_000);
  });

  it('refuses everything else', () => {
    for (const bad of ['', 'abc', '5', '5 m', '-5m', '+5m', '5d', 'm5', '1m 30s', '5mm', '.5s']) {
      expect(parseGoDuration(bad), bad).toBeUndefined();
    }
  });
});

describe('healthWaitMs', () => {
  it('is 60 seconds when the policy snapshot sets none, like the policy defaults of the server', () => {
    expect(DEFAULT_HEALTH_WAIT_MS).toBe(60_000);
    expect(healthWaitMs(undefined)).toBe(60_000);
    expect(healthWaitMs('')).toBe(60_000);
    expect(healthWaitMs('  ')).toBe(60_000);
  });

  it('takes the value of the snapshot', () => {
    expect(healthWaitMs('5m0s')).toBe(300_000);
    expect(healthWaitMs('30s')).toBe(30_000);
    expect(healthWaitMs('0s')).toBe(0);
  });

  it('has no value for something that is not a duration: no countdown instead of a wrong one', () => {
    expect(healthWaitMs('soon')).toBeUndefined();
  });
});

describe('the end of a health wait', () => {
  it('is verifiedAt plus the wait', () => {
    const verified = '2026-10-15T10:00:00Z';
    expect(healthWaitEnd(verified, 300_000)).toBe(Date.parse('2026-10-15T10:05:00Z'));
    expect(healthWaitEnd('2026-10-15T10:00:00.250Z', 60_000)).toBe(
      Date.parse('2026-10-15T10:01:00.250Z'),
    );
  });

  it('does not exist without a valid start', () => {
    expect(healthWaitEnd(undefined, 60_000)).toBeUndefined();
    expect(healthWaitEnd('', 60_000)).toBeUndefined();
    expect(healthWaitEnd('yesterday', 60_000)).toBeUndefined();
  });

  it('leaves the time that is left between 0 and the wait itself', () => {
    const end = 1_000_000;
    expect(remainingMs(end, end - 90_000, 300_000)).toBe(90_000);
    expect(remainingMs(end, end, 300_000)).toBe(0);
    expect(remainingMs(end, end + 5_000, 300_000)).toBe(0);
    // a browser clock that runs 10 minutes behind must not promise more than the policy says
    expect(remainingMs(end, end - 600_000, 300_000)).toBe(300_000);
  });
});

describe('formatCountdown', () => {
  it('shows minutes and seconds, and hours only when needed', () => {
    expect(formatCountdown(300_000)).toBe('5:00');
    expect(formatCountdown(247_000)).toBe('4:07');
    expect(formatCountdown(42_000)).toBe('0:42');
    expect(formatCountdown(3_599_000)).toBe('59:59');
    expect(formatCountdown(3_600_000)).toBe('1:00:00');
    expect(formatCountdown(3_723_000)).toBe('1:02:03');
  });

  it('rounds up, so that 0:01 stays on screen until the end', () => {
    expect(formatCountdown(1)).toBe('0:01');
    expect(formatCountdown(1_000)).toBe('0:01');
    expect(formatCountdown(1_001)).toBe('0:02');
    expect(formatCountdown(59_001)).toBe('1:00');
  });

  it('never goes below zero', () => {
    expect(formatCountdown(0)).toBe('0:00');
    expect(formatCountdown(-5_000)).toBe('0:00');
  });
});

describe('formatWait', () => {
  it('writes a wait the way a GitOps file does', () => {
    expect(formatWait(300_000)).toBe('5m');
    expect(formatWait(120_000)).toBe('2m');
    expect(formatWait(30_000)).toBe('30s');
    expect(formatWait(60_000)).toBe('1m');
    expect(formatWait(90_000)).toBe('1m30s');
    expect(formatWait(3_600_000)).toBe('1h');
    expect(formatWait(5_400_000)).toBe('1h30m');
    expect(formatWait(3_661_000)).toBe('1h1m1s');
    expect(formatWait(1_500)).toBe('1.5s');
    expect(formatWait(250)).toBe('250ms');
    expect(formatWait(0)).toBe('0s');
  });

  it('reads back what it writes', () => {
    for (const ms of [250, 1_500, 30_000, 60_000, 90_000, 300_000, 5_400_000, 3_661_000]) {
      expect(parseGoDuration(formatWait(ms)), String(ms)).toBe(ms);
    }
  });
});

describe('healthWaitOf', () => {
  it('names the policy of the snapshot, its wait and where it comes from', () => {
    expect(healthWaitOf(rollout('running', { healthWait: '5m0s' }))).toEqual({
      policyName: 'canary-then-all',
      waitMs: 300_000,
      waitText: '5m',
      path: 'policies/canary-then-all.yaml',
      commit: '9f3c2a17',
      evaluated: true,
    });
  });

  it('applies the default of 60 seconds to a snapshot without a wait', () => {
    expect(healthWaitOf(rollout('running'))).toMatchObject({ waitMs: 60_000, waitText: '1m' });
  });

  it('has an empty source for the built-in policy', () => {
    const built = rollout('running', { name: 'default', source: { path: '', commit: '' } });
    expect(healthWaitOf(built)).toMatchObject({ path: '', commit: '' });
  });

  it('keeps a plain-directory pseudo revision short too', () => {
    const dir = rollout('running', {
      source: { path: 'p.yaml', commit: `dir:${'ab'.repeat(32)}` },
    });
    expect(healthWaitOf(dir)?.commit).toBe('dir:abab');
  });

  it('is undefined for a wait that is not a duration', () => {
    expect(healthWaitOf(rollout('running', { healthWait: 'soon' }))).toBeUndefined();
  });

  it('evaluates the targets of the states in which the engine does', () => {
    const states: [RolloutStatus, boolean][] = [
      ['running', true],
      ['paused', true],
      ['waiting_approval', true],
      ['pending', false],
      ['failed', false],
      ['rolling_back', false],
      ['rolled_back', false],
      ['aborted', false],
      ['completed', false],
      ['superseded', false],
    ];
    for (const [status, evaluated] of states) {
      // the helper that the mock engine shares with the countdown, and what the countdown reads
      expect(isEvaluated(status), status).toBe(evaluated);
      expect(healthWaitOf(rollout(status))?.evaluated, status).toBe(evaluated);
    }
  });
});

describe('latestVerifiedAt', () => {
  it('is the start of the wait that ends last', () => {
    expect(
      latestVerifiedAt([
        { verifiedAt: '2026-10-15T10:00:30Z' },
        { verifiedAt: '2026-10-15T10:02:10Z' },
        { verifiedAt: undefined },
        { verifiedAt: '2026-10-15T10:01:00Z' },
      ]),
    ).toBe('2026-10-15T10:02:10Z');
  });

  it('compares instants, not text', () => {
    expect(
      latestVerifiedAt([
        { verifiedAt: '2026-10-15T11:00:00+02:00' }, // 09:00 UTC, but sorts after the next one as text
        { verifiedAt: '2026-10-15T09:30:00Z' },
      ]),
    ).toBe('2026-10-15T09:30:00Z');
  });

  it('is undefined without a usable start', () => {
    expect(latestVerifiedAt([])).toBeUndefined();
    expect(latestVerifiedAt([{ verifiedAt: undefined }, { verifiedAt: 'never' }])).toBeUndefined();
  });
});
