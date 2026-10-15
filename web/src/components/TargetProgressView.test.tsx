// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { act, cleanup, configure, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RolloutTarget } from '@/api/types';
import { qk } from '@/api/hooks/keys';
import { getDb } from '@/mocks/db';
import { renderWithProviders } from '@/test/utils';
import { TargetProgressView } from './TargetProgressView';

/** Data center version rollout of the fixtures: policy "standard" (healthWait 2m), five targets in the health wait. */
const ROLLOUT = '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11';
const T0 = Date.parse('2026-10-15T10:00:00Z');

// The gates run next to other work: the default of one second for findBy and waitFor is too short on a busy machine.
configure({ asyncUtilTimeout: 10_000 });

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/**
 * Waits for something that no change of the page announces, such as a query that has answered while
 * there is nothing to show. waitFor re-checks on a DOM change and on its own interval, and that
 * interval runs on the stopped clock of these tests, so it would never look again.
 */
async function until(condition: () => boolean) {
  for (let i = 0; i < 400 && !condition(); i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 25));
    });
  }
  expect(condition()).toBe(true);
}

/** A target in the health wait as the API returns it (with progress), whose wait began 30 s before T0. */
function verifyingTarget(): RolloutTarget {
  const mr = getDb().findRollout(ROLLOUT)!;
  const t = getDb()
    .targetsOf(mr)
    .find((x) => x.status === 'verifying')!;
  return { ...t, verifiedAt: new Date(T0 - 30_000).toISOString() };
}

function renderTarget(target: RolloutTarget) {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(T0);
  return renderWithProviders(<TargetProgressView target={target} />, { role: 'viewer' });
}

const healthWaitStep = () => document.querySelector<HTMLElement>('[data-step="health_wait"]')!;

describe('TargetProgressView: step "Health wait"', () => {
  it('shows the remaining time and the source of the wait while the target is verifying', async () => {
    const target = verifyingTarget();
    renderTarget(target);

    await waitFor(() => expect(within(healthWaitStep()).queryByRole('timer')).not.toBeNull());
    const step = healthWaitStep();
    expect(step.getAttribute('data-status')).toBe('running');
    expect(within(step).getByRole('timer').textContent).toBe('1:30 left');
    const source = within(step).getByTestId('health-wait-source');
    expect(source.textContent).toBe(
      'Policy standard · healthWait 2m · policies/standard.yaml@9f3c2a17',
    );
    expect(within(source).getByRole('link', { name: 'standard' }).getAttribute('href')).toBe(
      '/policies/standard',
    );
    // it is the only step with a countdown
    expect(screen.getAllByRole('timer')).toHaveLength(1);

    await act(async () => {
      vi.advanceTimersByTime(30_000);
    });
    expect(within(step).getByRole('timer').textContent).toBe('1:00 left');
    await act(async () => {
      vi.advanceTimersByTime(60_000);
    });
    expect(within(step).getByRole('timer').textContent).toBe('finishing…');
  });

  it('shows no countdown once the target has a final status, whatever the step says', async () => {
    const target = { ...verifyingTarget(), status: 'succeeded' as const };
    const { qc } = renderTarget(target);
    await screen.findByTestId('target-progress');
    // the step says running, as in the response of a target that was ended from outside a moment ago;
    // the rollout is not even asked for the policy
    expect(healthWaitStep().getAttribute('data-status')).toBe('running');
    expect(qc.getQueryState(qk.rollouts.detail(ROLLOUT))).toBeUndefined();
    expect(screen.queryByRole('timer')).toBeNull();
    expect(screen.queryByTestId('health-wait-source')).toBeNull();
  });

  it('shows no countdown for a target that has not reached the health wait', async () => {
    const mr = getDb().findRollout(ROLLOUT)!;
    const running = getDb()
      .targetsOf(mr)
      .find((x) => x.status === 'in_progress')!;
    renderTarget(running);
    await screen.findByTestId('target-progress');
    expect(healthWaitStep().getAttribute('data-status')).not.toBe('running');
    expect(screen.queryByRole('timer')).toBeNull();
  });

  it('shows no countdown for a rollout that the engine does not evaluate', async () => {
    const target = verifyingTarget();
    getDb().findRollout(ROLLOUT)!.rollout.status = 'failed';
    const { qc } = renderTarget(target);
    await screen.findByTestId('target-progress');
    // the countdown asked for the rollout and found that the engine leaves its targets alone
    await until(() => qc.getQueryData(qk.rollouts.detail(ROLLOUT)) !== undefined);
    expect(screen.queryByRole('timer')).toBeNull();
    // the step itself is still there
    expect(healthWaitStep().getAttribute('data-status')).toBe('running');
  });

  it('falls back to the start of the step when the target has no verifiedAt', async () => {
    const target = verifyingTarget();
    const step = target.progress!.steps.find((s) => s.name === 'health_wait')!;
    step.startedAt = new Date(T0 - 45_000).toISOString();
    renderTarget({ ...target, verifiedAt: undefined });
    await waitFor(() => expect(screen.queryByRole('timer')).not.toBeNull());
    expect(screen.getByRole('timer').textContent).toBe('1:15 left');
  });
});
