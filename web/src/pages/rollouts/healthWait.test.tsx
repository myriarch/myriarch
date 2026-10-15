// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { act, cleanup, configure, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { qk } from '@/api/hooks/keys';
import type { RolloutTarget } from '@/api/types';
import { getDb } from '@/mocks/db';
import { STEP_MS } from '@/mocks/updateSim';
import { renderApp } from '@/test/utils';

/** Data center version rollout of the fixtures: policy "standard" (healthWait 2m), five targets in the health wait. */
const VERSION_ROLLOUT = '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11';
/** Edge sites configuration rollout (completed in the fixtures): policy "edge-careful" (healthWait 10m). */
const CONFIG_ROLLOUT = 'c3e5f7a9-1b2d-4e6f-8a0c-2d4f6a8c0e33';

const T0 = Date.parse('2026-10-15T10:00:00Z');
const iso = (ms: number) => new Date(ms).toISOString();

// The first render of the page loads its code on demand, and the gates run next to other work: the
// default of one second for findBy and waitFor is too short on a busy machine.
configure({ asyncUtilTimeout: 10_000 });

/**
 * The clock of the browser stands still until the test advances it. Only the clock and the interval
 * timers are replaced: the network of the mock backend and Testing Library keep their real timers.
 */
function freezeClock(at: number) {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(at);
}
/** Lets time pass; every interval that falls into it runs (the countdown ticks once per second). */
const advance = (ms: number) =>
  act(async () => {
    vi.advanceTimersByTime(ms);
  });
/** Moves the clock to `at` and lets the countdown take one tick of it, without running every interval in between. */
const jumpTo = async (at: number) => {
  vi.setSystemTime(at - 1000);
  await advance(1000);
};

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** The five targets of the fixture that are in the health wait, ordered by host name. */
function verifyingTargets(rolloutId: string): RolloutTarget[] {
  return getDb()
    .findRollout(rolloutId)!
    .targets.filter((t) => t.status === 'verifying')
    .sort((a, b) => (a.hostname! < b.hostname! ? -1 : 1));
}

/** Starts the health wait of each target `agoMs[i]` before T0. */
function startWaits(targets: RolloutTarget[], agoMs: number[]) {
  targets.forEach((t, i) => {
    t.verifiedAt = iso(T0 - agoMs[i]!);
  });
}
const SAME_AGO = [30_000, 30_000, 30_000, 30_000, 30_000];

const rowOf = (hostname: string) =>
  screen.getAllByTestId('target-row').find((r) => within(r).queryByText(hostname) !== null)!;
const timerOf = (el: HTMLElement) => within(el).getByRole('timer');

describe('rollout page: remaining health wait', () => {
  it('counts down in the target list, to "finishing…", and stays there', async () => {
    const targets = verifyingTargets(VERSION_ROLLOUT);
    expect(targets).toHaveLength(5);
    // waits of 2 minutes: the first began 70 s, the second 30 s before T0 (50 s and 90 s left)
    startWaits(targets, [70_000, 30_000, 40_000, 50_000, 60_000]);
    freezeClock(T0);
    renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });

    await screen.findAllByTestId('target-row');
    const [first, second] = [rowOf(targets[0]!.hostname!), rowOf(targets[1]!.hostname!)];
    expect(timerOf(first).textContent).toBe('0:50 left');
    expect(timerOf(second).textContent).toBe('1:30 left');

    await advance(1000);
    expect(timerOf(first).textContent).toBe('0:49 left');
    expect(timerOf(second).textContent).toBe('1:29 left');

    await advance(47_000);
    expect(timerOf(first).textContent).toBe('0:02 left');
    await advance(1000);
    expect(timerOf(first).textContent).toBe('0:01 left');
    await advance(1000);
    expect(timerOf(first).textContent).toBe('finishing…');
    expect(timerOf(first).getAttribute('data-state')).toBe('finishing');
    // the other target is still counting, the first one waits for the engine
    expect(timerOf(second).textContent).toBe('0:40 left');
    await advance(10_000);
    expect(timerOf(first).textContent).toBe('finishing…');
    expect(timerOf(second).textContent).toBe('0:30 left');
  });

  it('shows the countdown for targets in the health wait and for no other target', async () => {
    const targets = verifyingTargets(VERSION_ROLLOUT);
    startWaits(targets, SAME_AGO);
    freezeClock(T0);
    renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });
    await screen.findAllByTestId('target-row');

    const rows = screen.getAllByTestId('target-row');
    const withTimer = rows.filter((r) => within(r).queryByRole('timer') !== null);
    expect(withTimer.map((r) => within(r).getAllByRole('link')[0]!.textContent).sort()).toEqual(
      targets.map((t) => t.hostname).sort(),
    );
    for (const row of withTimer) expect(within(row).getByText('Verifying')).toBeTruthy();
    // targets in progress, failed and waiting ones have none
    for (const status of ['In progress', 'Failed', 'Waiting for window']) {
      const row = rows.find((r) => within(r).queryByText(status) !== null);
      if (row) expect(within(row).queryByRole('timer')).toBeNull();
    }
    expect(rows.some((r) => within(r).queryByText('In progress') !== null)).toBe(true);
  });

  it('names the latest end on the card of the wave, with the source of the wait', async () => {
    // five waits of 2 minutes; the last one began 20 s before T0, so it ends 100 s after
    startWaits(verifyingTargets(VERSION_ROLLOUT), [90_000, 80_000, 70_000, 60_000, 20_000]);
    freezeClock(T0);
    renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });

    const card = await screen.findByTestId('wave-1');
    await waitFor(() => expect(within(card).queryByTestId('health-wait')).not.toBeNull());
    expect(within(card).getByTestId('health-wait').textContent).toBe(
      'Health wait: 5 targets, the last one ends in 1:40',
    );
    // the source line: the policy (a link), the wait and the path and commit of the snapshot
    const source = within(card).getByTestId('health-wait-source');
    expect(source.textContent).toBe(
      'Policy standard · healthWait 2m · policies/standard.yaml@9f3c2a17',
    );
    expect(within(source).getByRole('link', { name: 'standard' }).getAttribute('href')).toBe(
      '/policies/standard',
    );
    // the wave without targets in flight has none
    expect(within(screen.getByTestId('wave-0')).queryByTestId('health-wait')).toBeNull();

    await jumpTo(T0 + 100_000);
    expect(within(card).getByTestId('health-wait').textContent).toBe(
      'Health wait: 5 targets, finishing…',
    );
  });

  it('follows the targets of the wave: when the one that ends last is done, the next one counts', async () => {
    const targets = verifyingTargets(VERSION_ROLLOUT);
    // ends 30, 40, 50, 60 and 100 s after T0
    startWaits(targets, [90_000, 80_000, 70_000, 60_000, 20_000]);
    freezeClock(T0);
    const { qc } = renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });
    const card = await screen.findByTestId('wave-1');
    await waitFor(() =>
      expect(within(card).getByTestId('health-wait').textContent).toContain('5 targets'),
    );

    targets[4]!.status = 'succeeded';
    targets[4]!.finishedAt = iso(T0);
    getDb().recount(getDb().findRollout(VERSION_ROLLOUT)!);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() =>
      expect(within(card).getByTestId('health-wait').textContent).toBe(
        'Health wait: 4 targets, the last one ends in 1:00',
      ),
    );
    // and the finished target no longer counts in the list (its row moved behind the active ones)
    const rows = screen.getAllByTestId('target-row');
    expect(rows.filter((r) => within(r).queryByRole('timer') !== null)).toHaveLength(4);
    expect(within(rowOf(targets[0]!.hostname!)).getByRole('timer').textContent).toBe('0:30 left');
  });

  it('speaks of "1 target" for a single target, and the card ends with the last target', async () => {
    const targets = verifyingTargets(VERSION_ROLLOUT);
    startWaits(targets, SAME_AGO);
    freezeClock(T0);
    const { qc } = renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });
    const card = await screen.findByTestId('wave-1');
    await waitFor(() => expect(within(card).queryByTestId('health-wait')).not.toBeNull());

    for (const t of targets.slice(1)) t.status = 'succeeded';
    getDb().recount(getDb().findRollout(VERSION_ROLLOUT)!);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() =>
      expect(within(card).getByTestId('health-wait').textContent).toBe(
        'Health wait: 1 target, ends in 1:30',
      ),
    );

    targets[0]!.status = 'succeeded';
    getDb().recount(getDb().findRollout(VERSION_ROLLOUT)!);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() => expect(within(card).queryByTestId('health-wait')).toBeNull());
    expect(within(card).queryByTestId('health-wait-source')).toBeNull();
    expect(screen.queryAllByRole('timer')).toHaveLength(0);
  });

  it.each(['paused', 'waiting_approval'] as const)(
    'keeps counting down while the rollout is %s: the engine keeps evaluating its running targets',
    async (status) => {
      const targets = verifyingTargets(VERSION_ROLLOUT);
      startWaits(targets, SAME_AGO);
      getDb().findRollout(VERSION_ROLLOUT)!.rollout.status = status;
      freezeClock(T0);
      renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });
      await screen.findAllByTestId('target-row');
      const row = rowOf(targets[0]!.hostname!);
      expect(timerOf(row).textContent).toBe('1:30 left');
      await advance(5000);
      expect(timerOf(row).textContent).toBe('1:25 left');
    },
  );

  it.each(['failed', 'rolling_back', 'aborted'] as const)(
    'shows nothing for a %s rollout: the engine no longer evaluates its targets',
    async (status) => {
      startWaits(verifyingTargets(VERSION_ROLLOUT), SAME_AGO);
      getDb().findRollout(VERSION_ROLLOUT)!.rollout.status = status;
      freezeClock(T0);
      renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer' });
      const rows = await screen.findAllByTestId('target-row');
      // the rows say "Verifying", but no end is promised
      expect(rows.some((r) => within(r).queryByText('Verifying') !== null)).toBe(true);
      await advance(1000);
      expect(screen.queryAllByRole('timer')).toHaveLength(0);
      expect(screen.queryByTestId('health-wait-source')).toBeNull();
    },
  );

  it('is translated', async () => {
    const targets = verifyingTargets(VERSION_ROLLOUT);
    startWaits(targets, [90_000, 80_000, 70_000, 60_000, 30_000]);
    freezeClock(T0);
    renderApp({ route: `/rollouts/${VERSION_ROLLOUT}`, role: 'viewer', lang: 'de' });
    await screen.findAllByTestId('target-row');
    const row = rowOf(targets[4]!.hostname!);
    expect(timerOf(row).textContent).toBe('noch 1:30');
    const card = screen.getByTestId('wave-1');
    await waitFor(() => expect(within(card).queryByTestId('health-wait')).not.toBeNull());
    expect(within(card).getByTestId('health-wait').textContent).toBe(
      'Bewährung: 5 Ziele, das letzte endet in 1:30', // check-language: ignore
    );
    expect(within(card).getByTestId('health-wait-source').textContent).toContain('Policy standard');
    await jumpTo(T0 + 90_000);
    expect(timerOf(row).textContent).toBe('wird abgeschlossen …'); // check-language: ignore
    expect(within(card).getByTestId('health-wait').textContent).toBe(
      'Bewährung: 5 Ziele, wird abgeschlossen …', // check-language: ignore
    );
  });
});

describe('rollout page: remaining health wait of a configuration rollout', () => {
  /** The configuration rollout of the fixtures, running its last wave with two targets in the health wait. */
  function runConfigRollout() {
    const db = getDb();
    const mr = db.findRollout(CONFIG_ROLLOUT)!;
    const wave = mr.rollout.waves[3]!;
    const verifying = mr.targets
      .filter((t) => t.wave === wave.index && t.status === 'succeeded')
      .slice(0, 2);
    // the second began 5 minutes before the first, a minute before T0
    startWaits(verifying, [60_000, 360_000]);
    for (const t of verifying) {
      t.status = 'verifying';
      t.finishedAt = undefined;
    }
    mr.rollout.status = 'running';
    wave.status = 'running';
    db.recount(mr);
    return verifying;
  }

  it('shows the remaining time of a target without update progress and the latest end of the wave', async () => {
    const [first, second] = runConfigRollout();
    freezeClock(T0);
    renderApp({ route: `/rollouts/${CONFIG_ROLLOUT}?wave=3`, role: 'viewer' });
    await screen.findAllByTestId('target-row');
    // a configuration target has no step bar, only its status and the time that is left
    expect(screen.queryByTestId('update-steps-bar')).toBeNull();
    // 10 minutes (policy "edge-careful") after 1 and after 6 minutes: 9:00 and 4:00 left
    expect(timerOf(rowOf(first!.hostname!)).textContent).toBe('9:00 left');
    expect(timerOf(rowOf(second!.hostname!)).textContent).toBe('4:00 left');

    const card = screen.getByTestId('wave-3');
    await waitFor(() => expect(within(card).queryByTestId('health-wait')).not.toBeNull());
    expect(within(card).getByTestId('health-wait').textContent).toBe(
      'Health wait: 2 targets, the last one ends in 9:00',
    );
    expect(within(card).getByTestId('health-wait-source').textContent).toBe(
      'Policy edge-careful · healthWait 10m · policies/edge-careful.yaml@9f3c2a17',
    );

    await jumpTo(T0 + 240_000);
    expect(timerOf(rowOf(second!.hostname!)).textContent).toBe('finishing…');
    expect(timerOf(rowOf(first!.hostname!)).textContent).toBe('5:00 left');
  });
});

describe('rollout page: the simulated update of the mock backend', () => {
  it('counts down the health wait of its policy snapshot, which lasts one step like the simulation', async () => {
    freezeClock(T0);
    const db = getDb();
    const res = db.selectVersion(
      'k8s-gateway',
      { version: '0.162.0', reason: 'Gateway live' },
      { name: 'operator' },
    );
    if (!res.ok || !res.response.rolloutId) throw new Error('selection was not planned');
    const id = res.response.rolloutId;
    const { qc } = renderApp({ route: `/rollouts/${id}`, role: 'operator' });
    const cards = await screen.findByTestId('target-cards');
    expect(within(cards).queryByRole('timer')).toBeNull();

    // five steps of two seconds; the sixth is the health wait of the snapshot, 2 s as well
    await advance(5 * STEP_MS);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() => expect(within(cards).queryByRole('timer')).not.toBeNull());
    const snapshot = db.findRollout(id)!.rollout.policy.healthWait;
    expect(snapshot).toBe(`${STEP_MS / 1000}s`);
    expect(within(cards).getByRole('timer').textContent).toBe('0:02 left');
    expect(within(cards).getByTestId('health-wait-source').textContent).toContain(
      'Policy gateway-version · healthWait 2s · ',
    );
    // the table row and the card of the wave count the same time
    expect(within(screen.getAllByTestId('target-row')[0]!).getByRole('timer').textContent).toBe(
      '0:02 left',
    );

    await advance(1000);
    expect(within(cards).getByRole('timer').textContent).toBe('0:01 left');

    // the simulation ends the health wait with the policy wait: succeeded, and the countdown is gone
    await advance(1000);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() => expect(screen.queryAllByRole('timer')).toHaveLength(0));
    expect(screen.getAllByText('succeeded').length).toBeGreaterThan(0);
  });
});
