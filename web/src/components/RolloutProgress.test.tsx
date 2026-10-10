// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { RolloutCounts } from '@/api/types';
import { renderWithProviders } from '@/test/utils';
import { RolloutProgress } from './RolloutProgress';

const counts = (over: Partial<RolloutCounts> = {}): RolloutCounts => ({
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

const segment = (key: string) => document.querySelector(`[data-segment="${key}"]`);

describe('RolloutProgress', () => {
  it('draws pending targets as empty track and does not use a combined waiting segment', () => {
    renderWithProviders(<RolloutProgress counts={counts()} />);
    expect(segment('pending')?.getAttribute('data-fill')).toBe('track');
    expect(segment('waiting')).toBeNull();
    expect(segment('inProgress')?.getAttribute('data-fill')).toBe('solid');
    expect(segment('succeeded')).toBeNull();
  });

  it('says what the numbers count', () => {
    renderWithProviders(<RolloutProgress counts={counts()} />);
    expect(screen.getByText('0 of 299 succeeded (0 %)')).toBeTruthy();
    expect(screen.getByText('5 in progress')).toBeTruthy();
    const label = screen.getByRole('img').getAttribute('aria-label') ?? '';
    expect(label).toContain('Pending: 294');
    expect(label).toContain('In progress: 5');
    expect(label).not.toContain('Succeeded');
  });

  it('shows no in-progress hint when nothing runs', () => {
    renderWithProviders(<RolloutProgress counts={counts({ inProgress: 0, pending: 299 })} />);
    expect(screen.queryByText(/in progress/)).toBeNull();
  });

  it('uses yellow only for targets waiting for a window', () => {
    renderWithProviders(<RolloutProgress counts={counts({ waitingWindow: 4, pending: 290 })} />);
    const el = segment('waitingWindow') as HTMLElement;
    expect(el).toBeTruthy();
    expect(el.getAttribute('data-tone')).toBe('warning');
    expect(segment('pending')?.getAttribute('data-tone')).not.toBe('warning');
    expect(segment('inProgress')?.getAttribute('data-tone')).toBe('info');
  });

  it('lists the legend in the documented order', () => {
    renderWithProviders(
      <RolloutProgress
        legend
        counts={counts({
          total: 40,
          succeeded: 8,
          failed: 2,
          rolledBack: 1,
          skipped: 1,
          cancelled: 1,
          inProgress: 5,
          waitingWindow: 3,
          pending: 19,
        })}
      />,
    );
    const entries = [...document.querySelectorAll('[data-legend]')].map((e) =>
      e.getAttribute('data-legend'),
    );
    expect(entries).toEqual([
      'succeeded',
      'failed',
      'rolledBack',
      'skipped',
      'inProgress',
      'waitingWindow',
      'pending',
    ]);
    expect(
      document.querySelector('[data-legend="pending"] [data-swatch]')?.getAttribute('data-fill'),
    ).toBe('track');
  });
});
