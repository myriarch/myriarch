// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { RolloutWave } from '@/api/types';
import { renderWithProviders } from '@/test/utils';
import { WaveSteps } from './WaveSteps';

const counts = (over: Partial<RolloutWave['counts']> = {}): RolloutWave['counts'] => ({
  total: 299,
  pending: 294,
  waitingWindow: 0,
  inProgress: 5,
  succeeded: 0,
  failed: 0,
  skipped: 0,
  cancelled: 0,
  rolledBack: 0,
  ...over,
});

const wave = (over: Partial<RolloutWave> = {}): RolloutWave => ({
  index: 1,
  name: 'rest',
  percent: 100,
  requireApproval: true,
  status: 'running',
  approvedBy: 'admin',
  approvedAt: '2026-10-10T21:54:33Z',
  startedAt: '2026-10-10T21:54:33Z',
  counts: counts(),
  ...over,
});

function renderWave(w: RolloutWave, lang: 'en' | 'de' = 'en') {
  return renderWithProviders(
    <WaveSteps waves={[w]} currentWave={w.index} canApprove onApprove={() => undefined} />,
    { role: 'operator', lang },
  );
}

describe('WaveSteps', () => {
  it('shows "Approved" and not "Approval required" once the wave was approved and runs', () => {
    renderWave(wave());
    const card = screen.getByTestId('wave-1');
    expect(within(card).getByText('Running')).toBeTruthy();
    expect(within(card).getByText('Approved')).toBeTruthy();
    expect(within(card).queryByText('Approval required')).toBeNull();
    expect(within(card).getByText(/Approved by admin/)).toBeTruthy();
    expect(within(card).getByText('up to 100 % of all targets')).toBeTruthy();
    expect(screen.queryByTestId('approve-1')).toBeNull();
  });

  it('announces the approval of a pending wave', () => {
    renderWave(wave({ status: 'pending', approvedAt: undefined, approvedBy: undefined }));
    expect(within(screen.getByTestId('wave-1')).getByText('Approval required')).toBeTruthy();
  });

  it('shows the status and the button, but no extra badge, while the wave waits', async () => {
    renderWave(wave({ status: 'waiting_approval', approvedAt: undefined, approvedBy: undefined }));
    const card = screen.getByTestId('wave-1');
    expect(within(card).getByText('Waiting for approval')).toBeTruthy();
    expect(await screen.findByTestId('approve-1')).toBeTruthy();
    expect(within(card).queryByText('Approval required')).toBeNull();
  });

  it('is translated: "Freigegeben", never the label of the approval request after the approval', () => {
    renderWave(wave(), 'de');
    const card = screen.getByTestId('wave-1');
    expect(within(card).getByText('Freigegeben')).toBeTruthy();
    expect(within(card).queryByText('Freigabe nötig')).toBeNull(); // check-language: ignore
    expect(within(card).getByText('bis 100 % aller Ziele')).toBeTruthy();
  });

  it('shows no share for a selector wave', () => {
    renderWave(
      wave({ percent: undefined, selector: { matchAttributes: { 'labels.pilot': 'true' } } }),
    );
    expect(screen.queryByText(/of all targets/)).toBeNull();
  });
});
