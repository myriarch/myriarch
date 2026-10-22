// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { getDb } from '@/mocks/db';
import { server } from '@/mocks/server';
import { renderApp } from '@/test/utils';

const location = () => screen.getByTestId('location').textContent;
const ROLLOUT_WAITING = '8a7d9c20-3b1e-4f65-b0a2-6c4e1d9f5a22';

describe('authentication', () => {
  it('redirects anonymous users to the login page with the target path', async () => {
    renderApp({ route: '/agents?status=offline', role: null });
    await screen.findByRole('button', { name: 'Sign in' });
    expect(location()).toBe('/login?redirect=%2Fagents%3Fstatus%3Doffline');
  });

  it('dev login returns to the requested page and CSRF token is sent afterwards', async () => {
    renderApp({ route: '/agents?status=offline', role: null });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Username'), 'operator');
    await user.type(screen.getByLabelText('Password'), 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(location()).toBe('/agents?status=offline'));
    await screen.findAllByTestId('agent-row');
    expect(getDb().sessionRole).toBe('operator');
  });

  it('shows a localized error for invalid credentials', async () => {
    renderApp({ route: '/login', role: null });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Username'), 'x');
    await user.type(screen.getByLabelText('Password'), 'y');
    server.use(
      http.post('/api/v1/auth/dev-login', () =>
        HttpResponse.json(
          { type: 'about:blank', title: 'Unauthorized', status: 401, code: 'unauthorized' },
          { status: 401, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('Session expired or not signed in.')).toBeTruthy();
  });

  it('a 401 from any request logs the user out', async () => {
    renderApp({ route: '/' });
    await screen.findByText('Total agents');
    server.use(
      http.get('/api/v1/agents', () =>
        HttpResponse.json(
          { type: 'about:blank', title: 'Unauthorized', status: 401, code: 'unauthorized' },
          { status: 401, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );
    await userEvent.setup().click(screen.getAllByRole('link', { name: 'Agents' })[0]!);
    await screen.findByRole('button', { name: 'Sign in' });
  });

  it('shows the bootstrap banner on every page only for the bootstrap admin session', async () => {
    getDb().sessionAuthMethod = 'bootstrap';
    renderApp({ route: '/' });
    const banner = await screen.findByTestId('bootstrap-banner');
    expect(banner.textContent).toContain('Signed in with the bootstrap admin');
    expect(banner.textContent).toContain('disable the bootstrap admin');
    await userEvent.setup().click(screen.getAllByRole('link', { name: 'Agents' })[0]!);
    await screen.findAllByTestId('agent-row');
    expect(screen.getByTestId('bootstrap-banner')).toBeTruthy();
  });

  it('shows no bootstrap banner for other sign-in methods', async () => {
    renderApp({ route: '/' });
    await screen.findByText('Total agents');
    expect(screen.queryByTestId('bootstrap-banner')).toBeNull();
  });

  it('renders German by default text when language is de', async () => {
    renderApp({ route: '/', lang: 'de' });
    expect(await screen.findByText('Agents gesamt')).toBeTruthy();
    expect(screen.getAllByText('Übersicht').length).toBeGreaterThan(0); // check-language: ignore
  });
});

describe('dashboard', () => {
  it('shows tiles, version distribution, fleets, rollouts and the release banner', async () => {
    renderApp({ route: '/' });
    await screen.findByText('Total agents');
    const stats = getDb().overview();
    expect(await screen.findByText(/New collector version 0\.163\.0/)).toBeTruthy();
    expect(screen.getByText('(4 breaking changes)')).toBeTruthy();
    expect(
      screen.getAllByText(String(stats.agents.total).replace(/(\d)(\d{3})$/, '$1,$2')).length,
    ).toBeGreaterThan(0);
    expect(screen.getByText('Version distribution')).toBeTruthy();
    expect(screen.getAllByText('windows-hosts').length).toBeGreaterThan(0);
    expect(screen.getByText('Active rollouts')).toBeTruthy();
    expect(screen.getByTestId('gitops-chip').textContent).toContain('1 issue');
  });

  it('acknowledging the release hides the banner (operator)', async () => {
    renderApp({ route: '/', role: 'operator' });
    const banner = await screen.findByTestId('release-banner');
    await userEvent.setup().click(within(banner).getByRole('button', { name: 'Acknowledge' }));
    await waitFor(() => expect(screen.queryByTestId('release-banner')).toBeNull());
    expect(getDb().releases.find((r) => r.version === '0.163.0')!.acknowledgedAt).toBeTruthy();
  });

  it('viewers cannot acknowledge', async () => {
    renderApp({ route: '/', role: 'viewer' });
    const banner = await screen.findByTestId('release-banner');
    expect(within(banner).queryByRole('button', { name: 'Acknowledge' })).toBeNull();
  });

  it('shows a localized error state with retry when the API fails', async () => {
    server.use(
      http.get('/api/v1/stats/overview', () =>
        HttpResponse.json(
          { type: 'about:blank', title: 'x', status: 503, code: 'unavailable', requestId: 'req_1' },
          { status: 503, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );
    renderApp({ route: '/' });
    expect(await screen.findByText('Data could not be loaded')).toBeTruthy();
    expect(screen.getByText(/The server is temporarily unavailable/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });
});

describe('agents list', () => {
  it('is server-side paginated and mirrors filters in the URL', async () => {
    renderApp({ route: '/agents?status=offline&limit=50' });
    const rows = await screen.findAllByTestId('agent-row');
    const offline = getDb().queryAgents({ status: ['offline'] }, 'hostname', 500, 0);
    expect(rows.length).toBe(Math.min(50, offline.total));
    expect(within(rows[0]!).getByText('Offline')).toBeTruthy();
    expect(screen.getByTestId('active-filters').textContent).toContain('Status: Offline');
    const user = userEvent.setup();
    // toggle a facet: URL gets the new filter, offset resets
    await user.click(within(screen.getByTestId('facet-health')).getByLabelText(/Healthy/));
    await waitFor(() => expect(location()).toContain('healthy=true'));
    expect(location()).toContain('status=offline');
  });

  it('search is debounced into the URL (q) and survives a reload', async () => {
    const first = renderApp({ route: '/agents' });
    const user = userEvent.setup();
    await user.type(await screen.findByTestId('agent-search'), 'edge-1001');
    await waitFor(() => expect(location()).toBe('/agents?q=edge-1001'));
    await waitFor(() =>
      expect(screen.getAllByTestId('agent-row').length).toBe(
        getDb().queryAgents({ q: 'edge-1001' }).total,
      ),
    );
    first.unmount();
    renderApp({ route: '/agents?q=edge-1001' });
    expect(((await screen.findByTestId('agent-search')) as HTMLInputElement).value).toBe(
      'edge-1001',
    );
    await waitFor(() => expect(screen.getAllByTestId('agent-row').length).toBe(4));
  });

  it('pages through results and keeps sort/limit in the URL', async () => {
    renderApp({ route: '/agents?limit=100&sort=-version' });
    await screen.findAllByTestId('agent-row');
    expect(screen.getAllByTestId('agent-row')).toHaveLength(100);
    const user = userEvent.setup();
    await user.click(screen.getAllByRole('button', { name: 'Page 2' })[0]!);
    await waitFor(() => expect(location()).toBe('/agents?sort=-version&limit=100&offset=100'));
  });

  it('shows the empty state for filters without results and can reset', async () => {
    renderApp({ route: '/agents?q=does-not-exist-anywhere' });
    expect(await screen.findByText('No agents match these filters')).toBeTruthy();
    await userEvent.setup().click(screen.getAllByRole('button', { name: 'Clear all filters' })[0]!);
    await waitFor(() => expect(location()).toBe('/agents'));
  });

  it('live agent.updated patches the visible row', async () => {
    const { qc } = renderApp({ route: '/agents?q=edge-1001' });
    const rows = await screen.findAllByTestId('agent-row');
    const id = rows[0]!.getAttribute('data-agent-id')!;
    const { createLiveApplier } = await import('@/live/apply');
    const applier = createLiveApplier(qc);
    const s = getDb().summary(id)!;
    applier.apply('agent.updated', { ...s, status: 'offline', version: '9.9.9' });
    await waitFor(() =>
      expect(within(screen.getAllByTestId('agent-row')[0]!).getByText('9.9.9')).toBeTruthy(),
    );
    applier.dispose();
  });
});

describe('agent detail', () => {
  it('shows tabs, and restart/resync/delete follow permissions', async () => {
    const db = getDb();
    const online = db.queryAgents({ status: ['online'], kind: ['collector'] }, 'hostname', 1, 0)
      .items[0]!;
    const view = renderApp({ route: `/agents/${online.id}`, role: 'viewer' });
    await screen.findByRole('tab', { name: /Health/ });
    expect(screen.queryByRole('button', { name: 'Restart' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Resync' })).toBeNull();
    view.unmount();
    renderApp({ route: `/agents/${online.id}`, role: 'operator' });
    expect(await screen.findByRole('button', { name: 'Restart' })).toBeTruthy();
    // online agents cannot be deleted
    expect((screen.getByRole('button', { name: 'Delete' }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it('restart sends the command with CSRF and confirms with a toast', async () => {
    const db = getDb();
    const online = db.queryAgents({ status: ['online'], kind: ['collector'] }, 'hostname', 1, 0)
      .items[0]!;
    renderApp({ route: `/agents/${online.id}` });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Restart' }));
    const dlg = await screen.findByRole('dialog');
    await user.click(within(dlg).getByRole('button', { name: 'Restart' }));
    expect(await screen.findByText('Restart command sent')).toBeTruthy();
    expect(db.audit[0]!.action).toBe('agent.restart');
  });

  it('shows the not found state', async () => {
    renderApp({ route: '/agents/00000000-0000-4000-8000-000000000000' });
    expect(await screen.findByText('Agent not found')).toBeTruthy();
  });

  it('config tab shows desired and effective side by side with the informative note', async () => {
    const online = getDb().queryAgents(
      { kind: ['collector'], fleet: ['windows-hosts'] },
      'hostname',
      1,
      0,
    ).items[0]!;
    renderApp({ route: `/agents/${online.id}?tab=config` });
    expect(
      await screen.findByText(
        /differences to the desired configuration are therefore informative only/,
      ),
    ).toBeTruthy();
    expect(await screen.findByText('Effective (from agent)')).toBeTruthy();
  });
});

describe('rollouts', () => {
  it('lists the three fixture rollouts', async () => {
    renderApp({ route: '/rollouts' });
    const rows = await screen.findAllByTestId('rollout-row');
    expect(rows).toHaveLength(3);
  });

  it('viewer sees no actions, operator can approve the waiting wave', async () => {
    const v = renderApp({ route: `/rollouts/${ROLLOUT_WAITING}`, role: 'viewer' });
    await screen.findByText('Waves');
    expect(screen.queryByTestId('approve-primary')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull();
    expect(screen.getByText(/you can only view rollouts/)).toBeTruthy();
    v.unmount();

    renderApp({ route: `/rollouts/${ROLLOUT_WAITING}`, role: 'operator' });
    const user = userEvent.setup();
    await user.click(await screen.findByTestId('approve-primary'));
    const dlg = await screen.findByRole('dialog');
    await user.type(within(dlg).getByLabelText(/Reason/), 'Pilot ok');
    await user.click(within(dlg).getByRole('button', { name: 'Approve' }));
    expect(await screen.findByText('Wave approved')).toBeTruthy();
    await waitFor(() => expect(screen.queryByTestId('approve-primary')).toBeNull());
    const wave = getDb().findRollout(ROLLOUT_WAITING)!.rollout.waves[1]!;
    expect(wave.status).toBe('running');
    expect(getDb().audit[0]).toMatchObject({
      action: 'rollout.approve',
      details: { wave: 1, reason: 'Pilot ok' },
    });
  });

  it('pause then resume works and invalid transitions are shown as localized errors', async () => {
    renderApp({ route: `/rollouts/${ROLLOUT_WAITING}`, role: 'operator' });
    const user = userEvent.setup();
    // pausing is only possible for a running rollout (the engine answers 412 otherwise)
    expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull();
    await user.click(await screen.findByTestId('approve-primary'));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Approve' }),
    );
    await waitFor(() => expect(screen.queryByTestId('approve-primary')).toBeNull());
    await user.click(await screen.findByRole('button', { name: 'Pause' }));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Pause' }),
    );
    expect(await screen.findByText('Rollout paused')).toBeTruthy();
    expect(await screen.findByRole('button', { name: 'Resume' })).toBeTruthy();
    // server-side state changed behind our back: pausing again must produce a 412 toast
    getDb().findRollout(ROLLOUT_WAITING)!.rollout.status = 'completed';
    await user.click(screen.getByRole('button', { name: 'Resume' }));
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Resume' }),
    );
    expect(
      await screen.findByText('The action is not possible in the current state.'),
    ).toBeTruthy();
    // shown inline in the dialog only, not additionally as toast
    expect(screen.getAllByText('The action is not possible in the current state.')).toHaveLength(1);
  });
});

describe('blueprints and fleets', () => {
  it('renders a preview with hash and parameter sources', async () => {
    renderApp({ route: '/blueprints/edge-windows?tab=preview' });
    const user = userEvent.setup();
    // no fleet is pre-selected: the blueprint alone lacks its required parameters
    const fleet = (await screen.findAllByLabelText(/^Fleet$/)).find(
      (e) => e instanceof HTMLInputElement,
    ) as HTMLInputElement;
    expect(fleet.value).toBe('');
    await user.click(await screen.findByRole('option', { name: /Windows hosts/, hidden: true }));
    await user.click(await screen.findByRole('button', { name: 'Render' }));
    const res = await screen.findByTestId('render-result');
    expect(within(res).getByTestId('render-hash').textContent).toMatch(/^[0-9a-f]{16}$/);
    expect(await screen.findByText('Parameters and their sources')).toBeTruthy();
    expect(screen.getAllByText('Agent attribute').length).toBeGreaterThan(0);
  });

  it('answers "which fleet?" for hypothetical attributes', async () => {
    renderApp({ route: '/fleets?tab=match' });
    const user = userEvent.setup();
    await user.click(await screen.findByText('Enter attributes'));
    await user.click(await screen.findByRole('button', { name: 'Check assignment' }));
    const result = await screen.findByTestId('match-result');
    expect(within(result).getByText('windows-hosts')).toBeTruthy();
  });

  it('fleet detail shows selector, blueprint chain and policy waves', async () => {
    renderApp({ route: '/fleets/windows-hosts' });
    expect(await screen.findByText('Windows hosts')).toBeTruthy();
    expect(screen.getByText('site.country = NL')).toBeTruthy();
    expect(screen.getAllByText('pilot').length).toBeGreaterThan(0);
    expect(screen.getByText('Approval required')).toBeTruthy();
  });
});

describe('settings and tokens', () => {
  it('admin creates an enrollment token and sees the secret once, with the way into Add host', async () => {
    renderApp({ route: '/settings/tokens' });
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole('button', { name: 'Create token' }))[0]!);
    await user.type(await screen.findByLabelText('Name'), 'sites-test');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    const secret = await screen.findByTestId('token-secret');
    expect(secret.textContent).toMatch(/^myre_[0-9a-f]{40}$/);
    expect(screen.getByRole('button', { name: 'Add host with this token' })).toBeTruthy();
    expect(screen.queryByText('Installation')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'I have saved the token' }));
    await waitFor(() => expect(screen.queryByTestId('token-secret')).toBeNull());
    expect(await screen.findByText('sites-test')).toBeTruthy();
    expect(document.body.textContent).not.toContain(secret.textContent!);
  });

  it('shows the bootstrap admin without role and enable controls', async () => {
    renderApp({ route: '/settings/users' });
    const rows = await screen.findAllByTestId('user-row');
    const row = rows.find((r) => within(r).queryByTestId('bootstrap-managed'));
    expect(row).toBeTruthy();
    expect(within(row!).queryByRole('textbox')).toBeNull();
    expect(within(row!).queryByRole('switch')).toBeNull();
    expect(within(row!).getByText('Managed by configuration')).toBeTruthy();
    // other users keep their controls
    expect(screen.getAllByRole('switch').length).toBe(rows.length - 1);
  });

  it('marks enrollment tokens declared in the configuration', async () => {
    renderApp({ route: '/settings/tokens' });
    await userEvent.setup().click(await screen.findByRole('tab', { name: 'Enrollment tokens' }));
    const badges = await screen.findAllByTestId('config-badge');
    expect(badges).toHaveLength(1);
    expect(badges[0]!.textContent).toBe('Configuration');
  });

  it('operators do not see the token and user tabs', async () => {
    renderApp({ route: '/settings/system', role: 'operator' });
    await screen.findByText('Replicas');
    expect(screen.queryByRole('tab', { name: 'Tokens' })).toBeNull();
    expect(screen.queryByRole('tab', { name: 'Users & roles' })).toBeNull();
  });

  it('audit page requires audit:read', async () => {
    renderApp({ route: '/audit', role: 'viewer' });
    expect(await screen.findByText('No permission')).toBeTruthy();
  });
});
