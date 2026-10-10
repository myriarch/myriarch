// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { getDb } from '@/mocks/db';
import { MOCK_CSRF } from '@/mocks/handlers';
import { server } from '@/mocks/server';
import { renderApp } from '@/test/utils';

const location = () => screen.getByTestId('location').textContent;
const usernameInputs = () => document.querySelectorAll('input[autocomplete="username"]');
const passwordInputs = () => document.querySelectorAll('input[type="password"]');

function problem(
  status: number,
  code: string,
  detail: string,
  headers: Record<string, string> = {},
) {
  return HttpResponse.json(
    { type: 'about:blank', title: detail, status, code, detail },
    { status, headers: { 'Content-Type': 'application/problem+json', ...headers } },
  );
}

function setAuthConfig(c: Partial<ReturnType<typeof getDb>['authConfig']>) {
  Object.assign(getDb().authConfig, c);
}

/** Records the JSON bodies of POST /auth/bootstrap-login and answers like the real server. */
function captureBootstrapBodies() {
  const bodies: unknown[] = [];
  server.use(
    http.post('/api/v1/auth/bootstrap-login', async ({ request }) => {
      bodies.push(await request.json());
      getDb().sessionRole = 'admin';
      getDb().sessionAuthMethod = 'bootstrap';
      return HttpResponse.json({
        type: 'user',
        id: 'u-0001',
        name: 'admin',
        role: 'admin',
        authMethod: 'bootstrap',
        permissions: ['read'],
        csrfToken: MOCK_CSRF,
      });
    }),
  );
  return bodies;
}

describe('login page: bootstrap admin', () => {
  it('shows the expanded form when the bootstrap admin is the only method', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapPasswordEnabled: true });
    renderApp({ route: '/login', role: null });
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(screen.getByLabelText('Password')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /bootstrap admin/i })).toBeNull();
    expect(screen.queryByText(/No sign-in method is enabled/)).toBeNull();
    expect(usernameInputs()).toHaveLength(1);
  });

  it('keeps the bootstrap form behind a toggle next to the dev login', async () => {
    setAuthConfig({ bootstrapPasswordEnabled: true });
    renderApp({ route: '/login', role: null });
    const toggle = await screen.findByRole('button', { name: 'Use the bootstrap admin' });
    // the toggle text must not look like a sign-in button for the UI end-to-end scripts
    expect(toggle.textContent).not.toMatch(/Anmelden|Sign in|Login/i);
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    // the dev form keeps exactly one username and one password input
    expect(usernameInputs()).toHaveLength(1);
    expect(passwordInputs()).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Sign in' })).toHaveLength(1);

    await userEvent.setup().click(toggle);
    await waitFor(() => expect(usernameInputs()).toHaveLength(2));
    expect(passwordInputs()).toHaveLength(2);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    // the dev form stays first in the document
    expect(screen.getAllByRole('button', { name: 'Sign in' })).toHaveLength(2);
  });

  it('collapses the toggle next to single sign-on', async () => {
    setAuthConfig({ oidcEnabled: true, devLoginEnabled: false, bootstrapTokenEnabled: true });
    renderApp({ route: '/login', role: null });
    await screen.findByRole('button', { name: 'Use the bootstrap admin' });
    expect(passwordInputs()).toHaveLength(0);
  });

  it('offers nothing when no bootstrap flag is set', async () => {
    renderApp({ route: '/login', role: null });
    await screen.findByLabelText('Username');
    expect(screen.queryByRole('button', { name: 'Use the bootstrap admin' })).toBeNull();
  });

  it('signs in with username and password and sends exactly those fields', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapPasswordEnabled: true });
    const bodies = captureBootstrapBodies();
    renderApp({ route: '/login?redirect=%2Fagents', role: null });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Username'), ' admin ');
    await user.type(screen.getByLabelText('Password'), 'generated-test-password');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(location()).toBe('/agents'));
    expect(bodies).toEqual([{ username: 'admin', password: 'generated-test-password' }]);
    expect(await screen.findByTestId('bootstrap-banner')).toBeTruthy();
  });

  it('signs in with a one-time token and sends only the token', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapTokenEnabled: true });
    const bodies = captureBootstrapBodies();
    renderApp({ route: '/login', role: null });
    const user = userEvent.setup();
    const field = await screen.findByLabelText('One-time token');
    expect(field.getAttribute('autocomplete')).toBe('one-time-code');
    expect(field.getAttribute('type')).toBe('password');
    expect(usernameInputs()).toHaveLength(0);
    await user.type(field, 'generated-test-token-0123456789abcdef');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(location()).toBe('/'));
    expect(bodies).toEqual([{ token: 'generated-test-token-0123456789abcdef' }]);
    expect(getDb().sessionAuthMethod).toBe('bootstrap');
  });

  it('switches between password and token when both are enabled', async () => {
    setAuthConfig({
      devLoginEnabled: false,
      bootstrapPasswordEnabled: true,
      bootstrapTokenEnabled: true,
    });
    renderApp({ route: '/login', role: null });
    const user = userEvent.setup();
    await screen.findByLabelText('Username');
    await user.click(screen.getByText('With one-time token'));
    await screen.findByLabelText('One-time token');
    expect(usernameInputs()).toHaveLength(0);
    await user.click(screen.getByText('With password'));
    await screen.findByLabelText('Username');
  });

  it('validates required fields before sending', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapPasswordEnabled: true });
    const bodies = captureBootstrapBodies();
    renderApp({ route: '/login', role: null });
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('Username required')).toBeTruthy();
    expect(screen.getByText('Password required')).toBeTruthy();
    expect(bodies).toHaveLength(0);
  });

  it('shows the problem message for invalid credentials (401)', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapPasswordEnabled: true });
    renderApp({ route: '/login', role: null });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Username'), 'unknown-user');
    await user.type(screen.getByLabelText('Password'), 'wrong-test-password');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('invalid bootstrap credentials')).toBeTruthy();
    expect(location()).toBe('/login');
  });

  it('shows the problem message when the bootstrap admin is not enabled (404)', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapPasswordEnabled: true });
    renderApp({ route: '/login', role: null });
    server.use(
      http.post('/api/v1/auth/bootstrap-login', () =>
        problem(404, 'not_found', 'the bootstrap admin is not enabled'),
      ),
    );
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password'), 'some-test-password');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('the bootstrap admin is not enabled')).toBeTruthy();
  });

  it('shows the problem message when sign-in is rate limited (429)', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapTokenEnabled: true });
    renderApp({ route: '/login', role: null });
    server.use(
      http.post('/api/v1/auth/bootstrap-login', () =>
        problem(429, 'too_many_requests', 'too many sign-in attempts, retry later', {
          'Retry-After': '30',
        }),
      ),
    );
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText('One-time token'),
      'generated-test-token-0123456789abcdef',
    );
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('too many sign-in attempts, retry later')).toBeTruthy();
    expect(screen.getByText('Too many requests – please wait a moment.')).toBeTruthy();
  });

  it('rejects a used token with 401 in the mock backend (single use)', async () => {
    setAuthConfig({ devLoginEnabled: false, bootstrapTokenEnabled: true });
    getDb().usedBootstrapTokens.add('already-used-test-token-0123456789abcdef');
    renderApp({ route: '/login', role: null });
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText('One-time token'),
      'already-used-test-token-0123456789abcdef',
    );
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('invalid bootstrap credentials')).toBeTruthy();
  });

  it('names both configuration keys when no method is available', async () => {
    setAuthConfig({ devLoginEnabled: false });
    renderApp({ route: '/login', role: null });
    const alert = await screen.findByText(/No sign-in method is enabled/);
    expect(alert.textContent).toContain('auth.oidc');
    expect(alert.textContent).toContain('auth.bootstrapAdmin');
  });

  it('renders the toggle in German without the word Anmelden', async () => {
    setAuthConfig({ bootstrapPasswordEnabled: true });
    renderApp({ route: '/login', role: null, lang: 'de' });
    const toggle = await screen.findByRole('button', { name: 'Bootstrap-Admin verwenden' });
    expect(toggle.textContent).not.toMatch(/Anmelden|Sign in|Login/i);
  });
});
