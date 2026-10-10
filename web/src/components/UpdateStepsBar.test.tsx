// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { TargetProgress } from '@/api/types';
import { renderWithProviders } from '@/test/utils';
import { UpdateStepsBar } from './UpdateStepsBar';

const progress = (over: Partial<TargetProgress> = {}): TargetProgress => ({
  phase: 'downloading',
  source: 'updater',
  downloadPercent: 42,
  updatedAt: '2026-10-07T10:00:00Z',
  steps: [
    { name: 'offered', status: 'done' },
    { name: 'downloading', status: 'running', startedAt: '2026-10-07T10:00:00Z' },
    { name: 'verifying', status: 'pending' },
    { name: 'installing', status: 'skipped' },
    { name: 'health_check', status: 'failed', detail: 'collector unhealthy' },
  ],
  ...over,
});

describe('UpdateStepsBar', () => {
  it('renders one segment per step with the status as data attribute and an accessible name', () => {
    renderWithProviders(<UpdateStepsBar progress={progress()} />);
    const list = screen.getByRole('list', { name: 'Update steps' });
    const items = within(list).getAllByRole('listitem');
    expect(items.map((i) => [i.getAttribute('data-step'), i.getAttribute('data-status')])).toEqual([
      ['offered', 'done'],
      ['downloading', 'running'],
      ['verifying', 'pending'],
      ['installing', 'skipped'],
      ['health_check', 'failed'],
    ]);
    // color is never the only carrier: every state has its own name (and the download its percent)
    expect(items.map((i) => i.getAttribute('aria-label'))).toEqual([
      'Offered: done',
      'Download: running, 42 %',
      'Verification: pending',
      'Installation: skipped',
      'Health check (host): failed',
    ]);
  });

  it('shows the phase as text, with the percentage while downloading', () => {
    const { unmount } = renderWithProviders(<UpdateStepsBar progress={progress()} />);
    expect(screen.getByTestId('update-phase').textContent).toBe('downloading · 42 %');
    unmount();
    renderWithProviders(
      <UpdateStepsBar progress={progress({ phase: 'rolled_back', downloadPercent: undefined })} />,
    );
    expect(screen.getByTestId('update-phase').textContent).toBe('rolled back');
  });

  it('is localized', () => {
    renderWithProviders(<UpdateStepsBar progress={progress()} />, { lang: 'de' });
    expect(screen.getByTestId('update-phase').textContent).toBe('Download · 42 %');
    expect(screen.getByRole('listitem', { name: 'Download: läuft, 42 %' })).toBeTruthy(); // check-language: ignore
    expect(
      screen.getByRole('listitem', { name: 'Health-Check (Host): fehlgeschlagen' }),
    ).toBeTruthy();
  });
});
