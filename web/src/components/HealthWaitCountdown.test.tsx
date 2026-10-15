// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  act,
  cleanup,
  configure,
  fireEvent,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { qk } from '@/api/hooks/keys';
import type { RolloutPolicy } from '@/api/types';
import { getDb } from '@/mocks/db';
import { renderWithProviders } from '@/test/utils';
import { HealthWaitCountdown } from './HealthWaitCountdown';

/** Data center version rollout of the fixtures: policy "standard", healthWait 2m, running. */
const ROLLOUT = '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11';
const T0 = Date.parse('2026-10-15T10:00:00Z');
const ago = (ms: number) => new Date(T0 - ms).toISOString();

// The gates run next to other work: the default of one second for findBy and waitFor is too short on a busy machine.
configure({ asyncUtilTimeout: 10_000 });

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const rollout = () => getDb().findRollout(ROLLOUT)!.rollout;

/** Changes the policy snapshot of the rollout. The fixture policies are shared, so the snapshot is replaced, not edited. */
function setPolicy(patch: Partial<RolloutPolicy>) {
  const r = rollout();
  r.policy = { ...r.policy, ...patch };
}

type Props = Parameters<typeof HealthWaitCountdown>[0];

/**
 * Renders the countdown of a wait that began 30 s before T0 (override with `props`) on a stopped clock.
 * With `control` a second countdown with valid props sits beside it: when it has shown its time, the
 * rollout has loaded, and whatever the first one does not show is really not shown.
 */
function renderCountdown(
  props: Partial<Props> = {},
  { lang = 'en' as 'en' | 'de', control = false } = {},
) {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(T0);
  return renderWithProviders(
    <>
      <HealthWaitCountdown rolloutId={ROLLOUT} verifiedAt={ago(30_000)} {...props} />
      {control && <HealthWaitCountdown rolloutId={ROLLOUT} verifiedAt={ago(30_000)} />}
    </>,
    { role: 'viewer', lang },
  );
}

const tick = (ms: number) =>
  act(async () => {
    vi.advanceTimersByTime(ms);
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

describe('HealthWaitCountdown', () => {
  it('counts down from the end of the wait: verifiedAt plus the healthWait of the snapshot', async () => {
    renderCountdown();
    const timer = await screen.findByRole('timer');
    // a wait of 2 minutes that began 30 seconds ago
    expect(timer.textContent).toBe('1:30 left');
    expect(timer.getAttribute('data-state')).toBe('running');
    await tick(1000);
    expect(timer.textContent).toBe('1:29 left');
    await tick(88_000);
    expect(timer.textContent).toBe('0:01 left');
    await tick(1000);
    expect(timer.textContent).toBe('finishing…');
    expect(timer.getAttribute('data-state')).toBe('finishing');
  });

  it('shows only the time without the source line, and the source line when asked', async () => {
    const { unmount } = renderCountdown();
    await screen.findByRole('timer');
    expect(screen.queryByTestId('health-wait-source')).toBeNull();
    unmount();

    renderCountdown({ showSource: true });
    await screen.findByRole('timer');
    expect(screen.getByTestId('health-wait-source').textContent).toBe(
      'Policy standard · healthWait 2m · policies/standard.yaml@9f3c2a17',
    );
  });

  it('speaks of the targets of a wave when it is given their number', async () => {
    renderCountdown({ count: 3 });
    expect((await screen.findByRole('timer')).textContent).toBe(
      'Health wait: 3 targets, the last one ends in 1:30',
    );
  });

  it('is translated', async () => {
    renderCountdown({ showSource: true }, { lang: 'de' });
    const timer = await screen.findByRole('timer');
    expect(timer.textContent).toBe('noch 1:30');
    expect(screen.getByTestId('health-wait-source').textContent).toBe(
      'Policy standard · healthWait 2m · policies/standard.yaml@9f3c2a17',
    );
    await tick(90_000);
    expect(timer.textContent).toBe('wird abgeschlossen …'); // check-language: ignore
  });

  it('takes the default of 60 seconds for a snapshot without a healthWait', async () => {
    const { healthWait: _dropped, ...withoutWait } = rollout().policy;
    rollout().policy = withoutWait;
    renderCountdown({ showSource: true });
    expect((await screen.findByRole('timer')).textContent).toBe('0:30 left');
    expect(screen.getByTestId('health-wait-source').textContent).toContain('healthWait 1m');
  });

  it('reads the wait the way the server writes it', async () => {
    setPolicy({ healthWait: '1h30m0s' });
    renderCountdown({ verifiedAt: ago(60_000), showSource: true });
    expect((await screen.findByRole('timer')).textContent).toBe('1:29:00 left');
    expect(screen.getByTestId('health-wait-source').textContent).toContain('healthWait 1h30m');
  });

  it('finishes at once for a wait of 0', async () => {
    setPolicy({ healthWait: '0s' });
    renderCountdown();
    expect((await screen.findByRole('timer')).textContent).toBe('finishing…');
  });

  it('says that the built-in policy has no file', async () => {
    rollout().policyName = 'default';
    setPolicy({ name: 'default', source: { path: '', commit: '' } });
    renderCountdown({ showSource: true });
    await screen.findByRole('timer');
    const source = screen.getByTestId('health-wait-source');
    expect(source.textContent).toBe('Policy default · healthWait 2m · built-in policy');
    expect(within(source).getByRole('link', { name: 'default' }).getAttribute('href')).toBe(
      '/policies/default',
    );
  });

  it('shows the path without a commit when the snapshot has none', async () => {
    setPolicy({ source: { path: 'policies/standard.yaml', commit: '' } });
    renderCountdown({ showSource: true });
    await screen.findByRole('timer');
    expect(screen.getByTestId('health-wait-source').textContent).toBe(
      'Policy standard · healthWait 2m · policies/standard.yaml',
    );
  });

  it('encodes the policy name in the link', async () => {
    rollout().policyName = 'canary & co';
    renderCountdown({ showSource: true });
    await screen.findByRole('timer');
    expect(
      within(screen.getByTestId('health-wait-source'))
        .getByRole('link', { name: 'canary & co' })
        .getAttribute('href'),
    ).toBe('/policies/canary%20%26%20co');
  });

  it('never shows more than the wait itself, whatever the browser clock says', async () => {
    // the start lies 5 minutes in the future of this browser: its clock runs behind the server's
    renderCountdown({ verifiedAt: ago(-300_000) });
    expect((await screen.findByRole('timer')).textContent).toBe('2:00 left');
  });

  it('shows nothing without a start of the wait', async () => {
    renderCountdown({ verifiedAt: undefined }, { control: true });
    await screen.findByRole('timer');
    expect(screen.getAllByRole('timer')).toHaveLength(1);
  });

  it('shows nothing for a start that is not a time', async () => {
    renderCountdown({ verifiedAt: 'soon' }, { control: true });
    await screen.findByRole('timer');
    expect(screen.getAllByRole('timer')).toHaveLength(1);
  });

  it('shows nothing for a wait that is not a duration', async () => {
    setPolicy({ healthWait: 'soon' });
    const { qc } = renderCountdown({ showSource: true });
    await until(() => qc.getQueryData(qk.rollouts.detail(ROLLOUT)) !== undefined);
    expect(screen.queryByRole('timer')).toBeNull();
    expect(screen.queryByTestId('health-wait-source')).toBeNull();
  });

  it('shows nothing for a rollout that does not exist', async () => {
    const missing = '00000000-0000-4000-8000-000000000000';
    const { qc } = renderCountdown({ rolloutId: missing });
    await until(() => qc.getQueryState(qk.rollouts.detail(missing))?.status === 'error');
    expect(screen.queryByRole('timer')).toBeNull();
  });

  it('stops when the rollout is no longer evaluated and goes on when it is again', async () => {
    const { qc } = renderCountdown();
    await screen.findByRole('timer');
    rollout().status = 'failed';
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() => expect(screen.queryByRole('timer')).toBeNull());
    rollout().status = 'paused';
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    expect((await screen.findByRole('timer')).textContent).toBe('1:30 left');
  });

  it('starts again with a new start of the wait, as for a target that was retried', async () => {
    function Retry() {
      const [verifiedAt, setVerifiedAt] = useState(ago(100_000));
      return (
        <>
          <button onClick={() => setVerifiedAt(ago(10_000))}>retry</button>
          <HealthWaitCountdown rolloutId={ROLLOUT} verifiedAt={verifiedAt} />
        </>
      );
    }
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
    vi.setSystemTime(T0);
    renderWithProviders(<Retry />, { role: 'viewer' });
    expect((await screen.findByRole('timer')).textContent).toBe('0:20 left');
    fireEvent.click(screen.getByText('retry'));
    expect(screen.getByRole('timer').textContent).toBe('1:50 left');
  });
});
