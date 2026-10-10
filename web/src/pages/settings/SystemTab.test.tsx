// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { renderWithProviders } from '@/test/utils';
import SystemTab from './SystemTab';

describe('SystemTab', () => {
  it('links the license and the third-party notices in the about card', async () => {
    renderWithProviders(<SystemTab />);
    const license = await screen.findByRole('link', { name: 'License (Apache-2.0)' });
    expect(license.getAttribute('href')).toBe('/licenses/LICENSE');
    expect(license.getAttribute('target')).toBe('_blank');
    expect(license.getAttribute('rel')).toBe('noopener noreferrer');
    const notices = screen.getByRole('link', { name: 'Third-party notices' });
    expect(notices.getAttribute('href')).toBe('/licenses/THIRD_PARTY_NOTICES');
    expect(notices.getAttribute('target')).toBe('_blank');
    expect(notices.getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.getByText('Licenses')).toBeTruthy();
  });

  it('is localized', async () => {
    renderWithProviders(<SystemTab />, { lang: 'de' });
    const license = await screen.findByRole('link', { name: 'Lizenz (Apache-2.0)' }); // check-language: ignore
    expect(license.getAttribute('href')).toBe('/licenses/LICENSE');
    expect(screen.getByRole('link', { name: 'Hinweise zu Drittsoftware' })).toBeTruthy(); // check-language: ignore
    expect(screen.getByText('Lizenzen')).toBeTruthy(); // check-language: ignore
  });
});
