// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { renderApp } from '@/test/utils';

describe('release page: update to this version', () => {
  it('lets the operator pick a fleet, then opens the dialog with this version selected', async () => {
    renderApp({ route: '/releases/otelcol-contrib/0.162.0', role: 'operator' });
    const btn = (await screen.findByTestId('release-update')) as HTMLButtonElement;
    expect(btn.textContent).toBe('Update to version 0.162.0');
    expect(btn.disabled).toBe(false);
    const user = userEvent.setup();
    await user.click(btn);
    const pick = await screen.findByRole('dialog');
    // selectable fleet on top; others are disabled and say why
    const ok = within(pick).getByTestId('fleet-choice-k8s-gateway');
    expect(ok.textContent).toContain('Current target');
    expect(ok.textContent).toMatch(/\d+ collectors? will be updated/);
    const linuxHosts = within(pick).getByTestId('fleet-choice-linux-hosts');
    expect(linuxHosts.textContent).toContain('UI updates are not enabled in Git for this fleet.');
    expect(within(pick).getByTestId('fleet-choice-datacenter').textContent).toContain(
      'Already the target version.',
    );
    expect(within(pick).queryByRole('button', { name: 'Choose fleet linux-hosts' })).toBeNull();

    await user.click(within(pick).getByRole('button', { name: 'Choose fleet k8s-gateway' }));
    await waitFor(() => expect(screen.getAllByRole('dialog')).toHaveLength(1));
    const dlg = await screen.findByRole('dialog');
    await within(dlg).findByRole('radio', { name: /0\.162\.0/ });
    expect(
      (within(dlg).getByRole('radio', { name: /0\.162\.0/ }) as HTMLInputElement).checked,
    ).toBe(true);
    expect(within(dlg).getByText('Update version: k8s-gateway')).toBeTruthy();
  });

  it('shows per fleet whether a UI update is possible', async () => {
    renderApp({ route: '/releases/otelcol-contrib/0.163.0?tab=fleets', role: 'viewer' });
    // viewers have no button
    await screen.findByText('Affected fleets', { selector: 'h2' });
    expect(screen.queryByTestId('release-update')).toBeNull();
    await waitFor(() => expect(screen.getAllByTestId('ui-update-yes').length).toBeGreaterThan(0));
    // 0.163.0 is in datacenter's list of versions but outside the frame of k8s-gateway
    expect(screen.getAllByTestId('ui-update-no').length).toBeGreaterThan(0);
  });

  it('is disabled when no fleet can select the version', async () => {
    renderApp({ route: '/releases/otelcol-contrib/0.150.0', role: 'operator' });
    const btn = (await screen.findByTestId('release-update')) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  it('the comparison link of an option prefills the compare tab', async () => {
    renderApp({
      route: '/releases/otelcol-contrib/0.163.0?tab=compare&from=0.162.0',
      role: 'viewer',
    });
    expect(await screen.findByTestId('compare-result')).toBeTruthy();
  });
});
