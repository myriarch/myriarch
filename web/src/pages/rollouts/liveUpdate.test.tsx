// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { qk } from '@/api/hooks/keys';
import { getDb } from '@/mocks/db';
import { STEP_MS } from '@/mocks/updateSim';
import { renderApp } from '@/test/utils';

const ROLLOUT_UI_DC = '5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11';

/** A version rollout started the way the UI does it, with a clock the test controls. */
function startUpdate(reason: string) {
  const db = getDb();
  let now = Date.now();
  db.now = () => now;
  const res = db.selectVersion('k8s-gateway', { version: '0.162.0', reason }, { name: 'operator' });
  if (!res.ok || !res.response.rolloutId) throw new Error('selection was not planned');
  const mr = db.findRollout(res.response.rolloutId)!;
  return {
    db,
    mr,
    id: res.response.rolloutId,
    advance: (ms: number) => {
      now += ms;
      db.syncSimulations();
    },
  };
}

const steps = (root: HTMLElement) =>
  Object.fromEntries(
    [...root.querySelectorAll('[data-step]')].map((el) => [
      el.getAttribute('data-step'),
      el.getAttribute('data-status'),
    ]),
  );

describe('rollout page: live update flow', () => {
  it('follows a UI-triggered update through every step until it succeeded', async () => {
    const { db, mr, id, advance } = startUpdate('Gateway live');
    const { qc } = renderApp({ route: `/rollouts/${id}`, role: 'operator' });
    const refresh = async () => {
      await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    };

    // header: who, when, why
    const origin = await screen.findByTestId('rollout-origin-line');
    expect(origin.textContent).toContain('set via UI');
    expect(origin.textContent).toContain('Selected by operator');
    expect(origin.textContent).toContain('Gateway live');

    // a single target: its timeline is shown right below the waves
    const cards = await screen.findByTestId('target-cards');
    await within(cards).findByTestId('update-timeline');
    expect(steps(cards)).toMatchObject({
      offered: 'done',
      downloading: 'running',
      verifying: 'pending',
    });

    const expected: [string, Record<string, string>][] = [
      ['verifying', { downloading: 'done', verifying: 'running' }],
      ['installing', { verifying: 'done', installing: 'running' }],
      ['restarting', { installing: 'done', restarting: 'running' }],
      ['health check (host)', { restarting: 'done', health_check: 'running' }],
      ['health wait (myriarchd)', { health_check: 'done', health_wait: 'running' }],
    ];
    for (const [phase, stepStates] of expected) {
      advance(STEP_MS);
      await refresh();
      await waitFor(() =>
        expect(
          within(screen.getByTestId('target-progress')).getAllByText(phase).length,
        ).toBeGreaterThan(0),
      );
      expect(steps(screen.getByTestId('target-cards'))).toMatchObject(stepStates);
      // the compact bar of the table shows the same phase
      expect(screen.getByTestId('update-phase').textContent).toContain(phase);
    }

    advance(STEP_MS);
    await refresh();
    await waitFor(() => expect(screen.getAllByText('succeeded').length).toBeGreaterThan(0));
    expect(
      Object.values(steps(screen.getByTestId('target-cards'))).every((s) => s === 'done'),
    ).toBe(true);
    expect(screen.getAllByText('Completed').length).toBeGreaterThan(0);
    expect(db.entries.get(mr.simPlan!.agentId)!.agent.version).toBe('0.162.0');
  });

  it('shows the download percentage and the running time while a step runs', async () => {
    const { id, advance } = startUpdate('x');
    const { qc } = renderApp({ route: `/rollouts/${id}`, role: 'operator' });
    await screen.findByTestId('target-cards');
    advance(STEP_MS / 2);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() =>
      expect(screen.getByTestId('update-phase').textContent).toContain('downloading · 50 %'),
    );
    expect(screen.getByRole('listitem', { name: 'Download: running, 50 %' })).toBeTruthy();
  });

  it('rolls back on a failed health check, highlights "Retry failed" and the retry succeeds', async () => {
    const { db, mr, id, advance } = startUpdate('rollback please');
    const { qc } = renderApp({ route: `/rollouts/${id}`, role: 'operator' });
    await screen.findByTestId('target-cards');
    advance(6 * STEP_MS);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });

    const notice = await screen.findByTestId('rolled-back-notice');
    expect(notice.textContent).toContain(`rolled back to ${mr.rollout.fromRef}`);
    expect(notice.textContent).toContain('health check failed');
    expect(steps(screen.getByTestId('target-cards'))).toMatchObject({
      health_check: 'failed',
      rolling_back: 'done',
      health_wait: 'skipped',
    });
    expect(screen.getAllByText('Paused').length).toBeGreaterThan(0);
    // the collector still runs the old version
    expect(db.entries.get(mr.simPlan!.agentId)!.agent.version).toBe(mr.rollout.fromRef);

    const user = userEvent.setup();
    await user.click(await screen.findByTestId('retry-primary'));
    const dlg = await screen.findByRole('dialog');
    await user.click(within(dlg).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(mr.sim?.attempt).toBe(2));
    advance(6 * STEP_MS);
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() => expect(screen.getAllByText('Completed').length).toBeGreaterThan(0));
    expect(db.entries.get(mr.simPlan!.agentId)!.agent.version).toBe('0.162.0');
    expect(screen.queryByTestId('retry-primary')).toBeNull();
  });

  it('renders updateParams as k=v and never as [object Object]; internal keys are folded away', async () => {
    const { id } = startUpdate('x');
    renderApp({ route: `/rollouts/${id}`, role: 'operator' });
    const ann = await screen.findByTestId('rollout-annotations');
    expect(within(ann).getByText('workload=statefulset/otel-gateway')).toBeTruthy();
    expect(within(ann).getByText('otelcol-contrib')).toBeTruthy();
    expect(document.body.textContent).not.toContain('[object Object]');
    // the selection is in the header, not among the annotations; internals are collapsed JSON
    expect(within(ann).queryByText(/selection/)).toBeNull();
    const user = userEvent.setup();
    await user.click(within(ann).getByRole('button', { name: 'Technical details' }));
    expect(within(ann).getByTestId('technical-details').textContent).toContain(
      '"rollbackSent": false',
    );
  });

  it('after a rollout rollback the UI selection stays active: the page points to the fleet', async () => {
    const { db, mr, id } = startUpdate('x');
    db.setRolloutStatus(mr, 'rolled_back', 'rolled back by operator');
    renderApp({ route: `/rollouts/${id}`, role: 'operator' });
    const hint = await screen.findByTestId('selection-still-active');
    expect(hint.textContent).toContain('UI selection 0.162.0 is still active');
    expect(
      within(hint)
        .getByRole('link', { name: /clear the selection/ })
        .getAttribute('href'),
    ).toBe('/fleets/k8s-gateway');
  });

  it('a Git rollout is marked as such and shows its commit', async () => {
    renderApp({ route: '/rollouts/c3e5f7a9-1b2d-4e6f-8a0c-2d4f6a8c0e33', role: 'viewer' });
    const origin = await screen.findByTestId('rollout-origin-line');
    expect(origin.textContent).toContain('from Git');
    expect(origin.textContent).toContain('Target version from Git (commit 9f3c2a17)');
  });
});

describe('rollout page: targets of a larger rollout', () => {
  it('shows the step bar per target and opens the detail drawer with the timeline', async () => {
    renderApp({ route: `/rollouts/${ROLLOUT_UI_DC}`, role: 'viewer' });
    const rows = await screen.findAllByTestId('target-row');
    expect(rows.length).toBeGreaterThan(3);
    expect(screen.queryByTestId('target-cards')).toBeNull();
    expect(within(rows[0]!).getByTestId('update-steps-bar')).toBeTruthy();
    // the table is not cluttered with a timeline per row, the drawer has it
    expect(screen.queryByTestId('update-timeline')).toBeNull();
    const user = userEvent.setup();
    await user.click(rows[0]!);
    const drawer = await screen.findByRole('dialog');
    expect(within(drawer).getByTestId('target-progress')).toBeTruthy();
    expect(within(drawer).getByTestId('update-timeline')).toBeTruthy();
    expect(within(drawer).getByText(/reported by the myriarch-updater/)).toBeTruthy();
  });

  it('the failed target of the fixture shows where it failed', async () => {
    renderApp({ route: `/rollouts/${ROLLOUT_UI_DC}?wave=1`, role: 'viewer' });
    await screen.findAllByTestId('target-row');
    const failed = getDb()
      .findRollout(ROLLOUT_UI_DC)!
      .targets.find((t) => t.status === 'failed')!;
    expect(failed).toBeTruthy();
    const progress = getDb()
      .targetsOf(getDb().findRollout(ROLLOUT_UI_DC)!)
      .find((t) => t.agentId === failed.agentId)!.progress!;
    expect(progress.phase).toBe('failed');
    expect(progress.steps.find((s) => s.name === 'verifying')?.status).toBe('failed');
  });
});

describe('rollout page: target order', () => {
  const activeStatuses = ['In progress', 'Verifying'];
  const statusOf = (row: HTMLElement) =>
    within(row)
      .getAllByText(/./)
      .map((e) => e.textContent);

  it('lists targets with running work first and can switch to the wave order', async () => {
    renderApp({ route: `/rollouts/${ROLLOUT_UI_DC}`, role: 'viewer' });
    const rows = await screen.findAllByTestId('target-row');
    expect(within(rows[0]!).getByTestId('update-steps-bar')).toBeTruthy();
    expect(activeStatuses.some((s) => statusOf(rows[0]!).includes(s))).toBe(true);

    const user = userEvent.setup();
    await user.click(screen.getAllByLabelText('Order').find((e) => e.tagName === 'INPUT')!);
    await user.click(await screen.findByRole('option', { name: 'By wave and host', hidden: true }));
    await waitFor(() => {
      const first = screen.getAllByTestId('target-row')[0]!;
      expect(within(first).getAllByText('1').length).toBeGreaterThan(0);
      expect(activeStatuses.some((s) => statusOf(first).includes(s))).toBe(false);
    });
  });

  it('keeps the drawer open and current when its target moves to another page', async () => {
    const db = getDb();
    const { qc } = renderApp({ route: `/rollouts/${ROLLOUT_UI_DC}`, role: 'viewer' });
    const rows = await screen.findAllByTestId('target-row');
    const user = userEvent.setup();
    await user.click(rows[0]!);
    const drawer = await screen.findByRole('dialog');
    expect(within(drawer).getByTestId('target-progress')).toBeTruthy();
    const host = within(rows[0]!).getAllByRole('link')[0]!.textContent!;
    // the target finishes and drops behind many others
    const mr = db.findRollout(ROLLOUT_UI_DC)!;
    const target = mr.targets.find((t) => t.hostname === host)!;
    target.status = 'succeeded';
    await qc.invalidateQueries({ queryKey: qk.rollouts.all });
    await waitFor(() =>
      expect(within(screen.getAllByTestId('target-row')[0]!).queryByText(host)).toBeNull(),
    );
    // the drawer follows its target to the other page: it shows the new status, not the old one
    await waitFor(() =>
      expect(within(screen.getByRole('dialog')).getByText('Succeeded')).toBeTruthy(),
    );
    const view = within(screen.getByRole('dialog')).getByTestId('target-progress');
    expect(within(view).queryByText('In progress')).toBeNull();
    expect(within(view).queryByText('Verifying')).toBeNull();
  });
});

describe('origin markers in lists', () => {
  it('rollout list: UI and Git rollouts are told apart', async () => {
    renderApp({ route: '/rollouts' });
    await screen.findAllByTestId('rollout-row');
    expect(screen.getAllByTestId('rollout-origin-ui')).toHaveLength(1);
    expect(screen.getAllByTestId('rollout-origin-gitops')).toHaveLength(2);
  });

  it('fleets list and dashboard mark UI targets and fleets on hold', async () => {
    const v = renderApp({ route: '/fleets' });
    await screen.findAllByText('Edge sites');
    expect(await screen.findAllByTestId('version-source-ui')).toHaveLength(1);
    expect(screen.getAllByTestId('version-source-hold')).toHaveLength(1);
    v.unmount();
    renderApp({ route: '/' });
    await screen.findByText('Version distribution');
    await waitFor(() =>
      expect(screen.getAllByTestId('version-source-ui').length).toBeGreaterThan(0),
    );
    expect(screen.getAllByTestId('rollout-origin-ui').length).toBeGreaterThan(0);
  });
});
