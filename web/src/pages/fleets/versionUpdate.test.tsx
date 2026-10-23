// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';
import { getDb } from '@/mocks/db';
import { server } from '@/mocks/server';
import { FLEET_BY_NAME } from '@/mocks/static';
import { renderApp } from '@/test/utils';

const location = () => screen.getByTestId('location').textContent ?? '';
const problem = (status: number, code: string, issues?: { code: string; message: string }[]) =>
  HttpResponse.json(
    {
      type: 'about:blank',
      title: 'x',
      status,
      code,
      issues: issues?.map((i) => ({ severity: 'error', ...i })),
    },
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  );

/** Waits until the button knows the highest selectable version ("Update to version X"). */
async function updateButton(version: string) {
  const btn = await screen.findByTestId('update-to-version');
  await waitFor(() => expect(btn.textContent).toContain(`Update to version ${version}`));
  return btn;
}

describe('fleet detail: target version section', () => {
  it('Git source: shows the frame, the Git badge and the update button for the highest allowed version', async () => {
    renderApp({ route: '/fleets/k8s-gateway', role: 'operator' });
    const btn = await updateButton('0.162.0');
    expect((btn as HTMLButtonElement).disabled).toBe(false);
    expect(await screen.findByTestId('version-source-git')).toBeTruthy();
    expect(screen.getByTestId('frame-enabled').textContent).toBe('UI updates allowed');
    expect(screen.getByText('>=0.161.0, <=0.162.0')).toBeTruthy();
    expect(screen.getByText(/Downgrade: No/)).toBeTruthy();
    expect(screen.getByText(/target version selectable in the UI/)).toBeTruthy();
  });

  it('UI source: marks the target as set via UI with who, when and why, links the rollout and lists the history', async () => {
    renderApp({ route: '/fleets/datacenter', role: 'operator' });
    expect(await screen.findByTestId('version-source-ui')).toBeTruthy();
    const info = await screen.findByTestId('selection-info');
    expect(info.textContent).toContain('Selected by Alice Operator');
    expect(info.textContent).toContain('Canary in the night window was uneventful');
    expect(screen.getByText('View rollout')).toBeTruthy();
    // the audit log of the fleet: who selected what
    const history = await screen.findByTestId('version-history');
    await waitFor(() => expect(within(history).getByText('Version selected')).toBeTruthy());
    expect(within(history).getByText('Alice Operator')).toBeTruthy();
    // the running version rollout with its origin
    const active = screen.getByTestId('active-version-rollouts');
    expect(within(active).getByTestId('rollout-origin-ui')).toBeTruthy();
  });

  it('hold: explains why the target is on hold and offers both ways out', async () => {
    renderApp({ route: '/fleets/edge-sites', role: 'operator' });
    const hold = await screen.findByTestId('hold-alert');
    expect(hold.textContent).toContain('0.162.0');
    expect(hold.textContent).toContain('outside the frame of commit');
    expect(hold.textContent).toContain('No version rollouts are started');
    expect(within(hold).getByRole('button', { name: 'Select another version' })).toBeTruthy();
    expect(
      within(hold).getByRole('button', { name: 'Clear selection (Git target applies)' }),
    ).toBeTruthy();
  });

  it('without a frame in Git there is no way to update from the UI', async () => {
    renderApp({ route: '/fleets/windows-hosts', role: 'operator' });
    expect(await screen.findByTestId('frame-missing')).toBeTruthy();
    const btn = (await screen.findByTestId('update-to-version')) as HTMLButtonElement;
    await waitFor(() => expect(btn.disabled).toBe(true));
  });

  it('no target at all: says so and leaves the choice to the operator', async () => {
    // linux-hosts has no frame; take its Git version away for this test
    const fleet = FLEET_BY_NAME.get('linux-hosts')!;
    const git = fleet.collector.version;
    fleet.collector.version = undefined;
    try {
      renderApp({ route: '/fleets/linux-hosts', role: 'operator' });
      expect(
        await screen.findByText('no target version', { selector: 'p, span, div' }),
      ).toBeTruthy();
      expect(screen.getByTestId('version-source-none')).toBeTruthy();
    } finally {
      fleet.collector.version = git;
    }
  });

  it('target set but not reached and nothing running: no "up to date", points to the rollout and the way out', async () => {
    // e.g. after a host rollback: 0.162.0 is the target, the collector runs 0.161.0, 0.161.0 is a (forbidden) downgrade
    const base = getDb().versionOptions('k8s-gateway')!;
    server.use(
      http.get('/api/v1/fleets/k8s-gateway/version-options', () =>
        HttpResponse.json({
          ...base,
          options: [
            {
              ...base.options[0],
              version: '0.162.0',
              isCurrentTarget: true,
              selectable: false,
              reasonCode: 'current_target',
              agentsToUpdate: 1,
            },
            {
              ...base.options[0],
              version: '0.161.0',
              isCurrentTarget: false,
              selectable: false,
              reasonCode: 'downgrade_not_allowed',
              agentsToUpdate: 0,
            },
          ],
        }),
      ),
    );
    renderApp({ route: '/fleets/k8s-gateway', role: 'operator' });
    const alert = await screen.findByTestId('target-not-reached');
    expect(alert.textContent).toContain('Target 0.162.0 not reached yet');
    expect(alert.textContent).toContain('Not on 0.162.0 yet: 1 collector(s)');
    const btn = (await screen.findByTestId('update-to-version')) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(screen.queryByText('Already on the newest allowed version.')).toBeNull();
  });

  it('viewers see the information but no button', async () => {
    renderApp({ route: '/fleets/k8s-gateway', role: 'viewer' });
    expect(await screen.findByTestId('frame-enabled')).toBeTruthy();
    expect(screen.queryByTestId('update-to-version')).toBeNull();
    expect(screen.getByText('Updating requires the operator role.')).toBeTruthy();
  });

  it('clearing the selection asks for confirmation, requires a reason when the frame says so and warns about downgrades', async () => {
    renderApp({ route: '/fleets/datacenter', role: 'operator' });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'More actions' }));
    await user.click(
      await screen.findByRole('menuitem', { name: 'Clear selection', hidden: true }),
    );
    const dlg = await screen.findByRole('dialog');
    // Git target 0.161.0 is below the collectors that already run 0.162.0
    expect(within(dlg).getByText(/Collectors will be downgraded to 0\.161\.0/)).toBeTruthy();
    const confirm = within(dlg).getByRole('button', { name: 'Clear selection' });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await user.type(within(dlg).getByLabelText(/Reason/), 'back to Git');
    await user.click(confirm);
    await waitFor(() => expect(getDb().selections.has('datacenter')).toBe(false));
    expect(getDb().audit[0]).toMatchObject({
      action: 'fleet.version.clear',
      details: { version: '0.162.0', reason: 'back to Git' },
    });
    await waitFor(() => expect(screen.getByTestId('version-source-git')).toBeTruthy());
  });
});

describe('update dialog', () => {
  async function openDialog(
    route = '/fleets/k8s-gateway',
    role: 'operator' | 'admin' = 'operator',
  ) {
    renderApp({ route, role });
    const user = userEvent.setup();
    await user.click(
      await screen.findByTestId('update-to-version').then(async (b) => {
        await waitFor(() => expect(b.textContent).toMatch(/Update to version/));
        return b;
      }),
    );
    const dlg = await screen.findByRole('dialog');
    await within(dlg).findByRole('radio', { name: /0\.162\.0/ });
    return { user, dlg };
  }

  it('pre-selects the highest selectable version and disables the others with the reason', async () => {
    const { dlg } = await openDialog();
    expect(
      (within(dlg).getByRole('radio', { name: /0\.162\.0/ }) as HTMLInputElement).checked,
    ).toBe(true);
    const current = within(dlg).getByRole('radio', { name: /0\.161\.0/ }) as HTMLInputElement;
    expect(current.disabled).toBe(true);
    expect(within(dlg).getByTestId('option-reason-0.161.0').textContent).toBe(
      'Already the target version.',
    );
    // summary: executor, policy, wave preview, platforms
    const summary = within(dlg).getByTestId('update-summary');
    expect(within(summary).getByText('0.162.0')).toBeTruthy();
    expect(within(summary).getByText('gateway-version')).toBeTruthy();
    await within(summary).findByText('canary');
    expect(within(summary).getByText(/linux\/arm64: 40/)).toBeTruthy();
  });

  it('starts the rollout, stores who/why, audits and jumps to the live rollout page', async () => {
    const { user, dlg } = await openDialog();
    await user.type(within(dlg).getByLabelText(/Reason/), 'Gateway update for the test');
    await user.click(within(dlg).getByTestId('start-update'));
    await waitFor(() => expect(location()).toMatch(/^\/rollouts\/[0-9a-f-]{36}$/));
    const db = getDb();
    const sel = db.selections.get('k8s-gateway')!;
    expect(sel).toMatchObject({ version: '0.162.0', state: 'active', selectedBy: 'operator' });
    expect(location()).toBe(`/rollouts/${sel.rolloutId}`);
    expect(
      db.audit.some((a) => a.action === 'fleet.version.select' && a.result === 'success'),
    ).toBe(true);
    const mr = db.findRollout(sel.rolloutId!)!;
    expect(mr.rollout).toMatchObject({
      origin: 'ui',
      createdBy: 'operator',
      requestReason: 'Gateway update for the test',
    });
    // the page the operator lands on shows it
    expect(await screen.findByTestId('rollout-origin-line')).toBeTruthy();
  });

  it('requires a reason when Git says so and tells that a running rollout is superseded', async () => {
    const { user, dlg } = await openDialog('/fleets/datacenter');
    // datacenter: 0.162.0 is the UI target; 0.163.0 is the next selectable version
    await within(dlg).findByRole('radio', { name: /0\.163\.0/ });
    expect(
      (within(dlg).getByRole('radio', { name: /0\.163\.0/ }) as HTMLInputElement).checked,
    ).toBe(true);
    expect(
      within(dlg).getByText(/running version rollout to 0\.162\.0 will be superseded/),
    ).toBeTruthy();
    const start = within(dlg).getByTestId('start-update') as HTMLButtonElement;
    expect(start.disabled).toBe(true);
    expect(within(dlg).getByText('Required by the Git frame')).toBeTruthy();
    await user.type(within(dlg).getByLabelText(/Reason/), 'Neue Version testen');
    expect(start.disabled).toBe(false);
    await user.click(start);
    await waitFor(() => expect(location()).toMatch(/^\/rollouts\//));
    const db = getDb();
    expect(db.findRollout('5b0c1a3e-7d2f-4c88-9a41-0e6d3f2a7b11')!.rollout.status).toBe(
      'superseded',
    );
    expect(db.effectiveTarget('datacenter')).toMatchObject({ version: '0.163.0', source: 'ui' });
  });

  it('shows the translated reasons of a refused selection (422)', async () => {
    const { user, dlg } = await openDialog();
    server.use(
      http.post('/api/v1/fleets/k8s-gateway/actions/select-version', () =>
        problem(422, 'validation_failed', [
          { code: 'version_not_allowed', message: '0.164.0 is outside >=0.161.0' },
          { code: 'reason_required', message: 'a reason is required' },
        ]),
      ),
    );
    await user.click(within(dlg).getByTestId('start-update'));
    const refused = await within(dlg).findByTestId('select-refused');
    expect(refused.textContent).toContain('The version is outside the frame that Git allows.');
    expect(refused.textContent).toContain('The Git frame requires a reason.');
    // the dialog stays open and usable
    expect(location()).not.toMatch(/rollouts/);
  });

  it('on a conflict (409) says so and reloads the options', async () => {
    const { user, dlg } = await openDialog();
    server.use(
      http.post('/api/v1/fleets/k8s-gateway/actions/select-version', () => {
        // someone else changed the target while the dialog was open
        return problem(409, 'conflict');
      }),
    );
    const loads = vi.spyOn(getDb(), 'versionOptions');
    const before = loads.mock.calls.length;
    await user.click(within(dlg).getByTestId('start-update'));
    const conflict = await within(dlg).findByTestId('select-conflict');
    expect(conflict.textContent).toContain('The target version has changed in the meantime');
    // the options are fetched again (the dialog stays usable with fresh data)
    await waitFor(() => expect(loads.mock.calls.length).toBeGreaterThan(before));
    expect(within(dlg).getByRole('radio', { name: /0\.162\.0/ })).toBeTruthy();
  });

  it('on a precondition failure (412) says so and reloads the options', async () => {
    const { user, dlg } = await openDialog();
    server.use(
      http.post('/api/v1/fleets/k8s-gateway/actions/select-version', () =>
        problem(412, 'precondition_failed'),
      ),
    );
    const loads = vi.spyOn(getDb(), 'versionOptions');
    const before = loads.mock.calls.length;
    await user.click(within(dlg).getByTestId('start-update'));
    const alert = await within(dlg).findByTestId('select-precondition');
    expect(alert.textContent).toContain('not possible in the current state');
    await waitFor(() => expect(loads.mock.calls.length).toBeGreaterThan(before));
  });

  it('hold: the dialog does not present the Git version as the current target', async () => {
    renderApp({ route: '/fleets/edge-sites', role: 'operator' });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Select another version' }));
    const dlg = await screen.findByRole('dialog');
    const summary = await within(dlg).findByTestId('update-summary');
    expect(within(summary).getByTestId('summary-git-target').textContent).toContain('Git target');
    // no effective target: a dash, not the Git version
    expect(within(summary).getByText('–')).toBeTruthy();
  });

  it('planned=false: waits for the leader, then jumps to the rollout', async () => {
    const db = getDb();
    db.deferPlanning = true;
    db.deferPlanMs = 200;
    const { user, dlg } = await openDialog();
    await user.click(within(dlg).getByTestId('start-update'));
    expect(await screen.findByTestId('update-waiting')).toBeTruthy();
    expect(screen.getByText('Creating the rollout …')).toBeTruthy();
    await waitFor(() => expect(location()).toMatch(/^\/rollouts\/[0-9a-f-]{36}$/));
    expect(location()).toBe(`/rollouts/${db.selections.get('k8s-gateway')!.rolloutId}`);
  });

  it('planned=true without a rollout: nothing to update', async () => {
    const { user, dlg } = await openDialog();
    server.use(
      http.post('/api/v1/fleets/k8s-gateway/actions/select-version', () =>
        HttpResponse.json({
          target: { source: 'ui', version: '0.162.0', selectionEnabled: true },
          planned: true,
          message: 'no collector of the fleet needs an update',
        }),
      ),
    );
    await user.click(within(dlg).getByTestId('start-update'));
    const nothing = await screen.findByTestId('update-nothing');
    expect(nothing.textContent).toContain('No collectors to update');
    expect(nothing.textContent).toContain('no collector of the fleet needs an update');
  });
});
