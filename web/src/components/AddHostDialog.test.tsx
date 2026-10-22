// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import type { Role } from '@/api/types';
import { server } from '@/mocks/server';
import { renderApp } from '@/test/utils';

const PLACEHOLDER = '@@MYRIARCH_ENROLLMENT_TOKEN@@';
const PASTED = 'myre_0123456789abcdef0123456789abcdef01234567';

afterEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

function problem(status: number, code: string, reason?: string, message = 'refused') {
  return HttpResponse.json(
    {
      type: 'about:blank',
      title: 'x',
      status,
      code,
      issues: reason ? [{ severity: 'error', code: reason, message }] : undefined,
    },
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  );
}

/** Records the bodies of the render route and lets the default handler answer. */
function recordRenderBodies(): string[] {
  const bodies: string[] = [];
  server.use(
    http.post('/api/v1/install/commands', async ({ request }) => {
      bodies.push(await request.clone().text());
    }),
  );
  return bodies;
}

async function openDialog(role: Role = 'admin') {
  renderApp({ route: '/agents', role });
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Add host' }));
  const dialog = await screen.findByRole('dialog', { name: 'Add host' });
  await screen.findByText('Linux amd64');
  await user.type(within(dialog).getByLabelText(/Site ID/), 'site-1');
  return { user, dialog };
}

const showCommands = (user: ReturnType<typeof userEvent.setup>) =>
  user.click(screen.getByRole('button', { name: 'Show commands' }));

async function createToken(user: ReturnType<typeof userEvent.setup>, name = 'new-host') {
  await user.type(screen.getByLabelText('Name of the new token'), name);
  await user.click(screen.getByRole('button', { name: 'Create token' }));
  await screen.findByText(/Token created\./);
}

describe('Add host dialog: permissions', () => {
  it('a viewer can open it and render commands, but cannot create a token', async () => {
    const { user, dialog } = await openDialog('viewer');
    // The variant "token from a file" is the default without enrollment:manage, and the dialog says why.
    expect(
      (within(dialog).getByRole('radio', { name: 'Token from a file' }) as HTMLInputElement)
        .checked,
    ).toBe(true);
    await user.click(within(dialog).getByText('Token in the command'));
    expect(within(dialog).getByText(/Your role cannot create enrollment tokens/)).toBeTruthy();
    expect(within(dialog).queryByRole('button', { name: 'Create token' })).toBeNull();
    expect(within(dialog).queryByText('Create a token')).toBeNull();
    // Without any token the commands cannot be copied.
    await user.click(within(dialog).getByText('Token from a file'));
    await showCommands(user);
    const cmd = await screen.findByTestId('command-linux/amd64');
    expect(within(cmd).getByText(/token_file|read from the token file/)).toBeTruthy();
  });

  it('a viewer pastes an existing token, and it only goes into the copied command', async () => {
    const bodies = recordRenderBodies();
    const { user, dialog } = await openDialog('viewer');
    await user.click(within(dialog).getByText('Token in the command'));
    const show = screen.getByRole('button', { name: 'Show commands' });
    expect(show.hasAttribute('disabled')).toBe(true);
    await user.type(within(dialog).getByLabelText(/^Enrollment token/), PASTED);
    expect(show.hasAttribute('disabled')).toBe(false);
    await user.click(show);
    const cmd = await screen.findByTestId('command-linux/amd64');
    // Shown masked, copied in full.
    expect(cmd.textContent).not.toContain(PASTED);
    expect(cmd.textContent).not.toContain(PLACEHOLDER);
    await user.click(within(cmd).getByRole('button', { name: 'Copy command' }));
    const copied = await navigator.clipboard.readText();
    expect(copied).toContain(PASTED);
    expect(copied).not.toContain(PLACEHOLDER);
    // The render request never carried it.
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).not.toContain(PASTED);
    expect(bodies[0]).not.toContain('myre_');
  });

  it('rejects a pasted value that is not an enrollment token', async () => {
    const { user, dialog } = await openDialog('viewer');
    await user.click(within(dialog).getByText('Token in the command'));
    await user.type(within(dialog).getByLabelText(/^Enrollment token/), 'abc def');
    expect(await within(dialog).findByText(/does not look like an enrollment token/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Show commands' }).hasAttribute('disabled')).toBe(
      true,
    );
  });
});

describe('Add host dialog: the token', () => {
  it('an administrator creates a token that goes into every command, never into a URL or storage', async () => {
    const bodies = recordRenderBodies();
    const { user, dialog } = await openDialog('admin');
    expect(within(dialog).getByTestId('add-host-warning').textContent).toMatch(
      /PSReadLine.*transcription.*script block logging.*recording/is,
    );
    await createToken(user);
    await showCommands(user);
    const linux = await screen.findByTestId('command-linux/amd64');
    const windows = await screen.findByTestId('command-windows/amd64');
    await user.click(within(linux).getByRole('button', { name: 'Copy command' }));
    const copiedLinux = await navigator.clipboard.readText();
    expect(copiedLinux).toMatch(/--token myre_[0-9a-f]{40}/);
    expect(copiedLinux).not.toContain(PLACEHOLDER);
    await user.click(within(windows).getByRole('button', { name: 'Copy command' }));
    const token = /myre_[0-9a-f]{40}/.exec(copiedLinux)![0];
    expect(await navigator.clipboard.readText()).toContain(token);
    // Nowhere but in the copied text.
    expect(bodies.join('')).not.toContain(token);
    expect(screen.getByTestId('location').textContent).toBe('/agents');
    expect(JSON.stringify({ ...localStorage })).not.toContain(token);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(token);
    expect(linux.textContent).not.toContain(token);
  });

  it('drops the token when the dialog closes', async () => {
    const { user, dialog } = await openDialog('admin');
    await createToken(user);
    await showCommands(user);
    await screen.findByTestId('command-linux/amd64');
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.body.textContent).not.toMatch(/myre_[0-9a-f]{40}/);
    // A new dialog starts without a token, and without commands.
    await user.click(screen.getByRole('button', { name: 'Add host' }));
    const again = await screen.findByRole('dialog', { name: 'Add host' });
    expect(within(again).getByLabelText('Name of the new token')).toBeTruthy();
    expect(within(again).queryByTestId('add-host-commands')).toBeNull();
    expect(within(again).queryByText(/Token created\./)).toBeNull();
  });

  it('uses the token that the token dialog just created, and drops it with that dialog', async () => {
    renderApp({ route: '/settings/tokens' });
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole('button', { name: 'Create token' }))[0]!);
    await user.type(await screen.findByLabelText('Name'), 'sites-test');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    const secret = (await screen.findByTestId('token-secret')).textContent!;
    await user.click(screen.getByRole('button', { name: 'Add host with this token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add host' });
    await user.type(within(dialog).getByLabelText(/Site ID/), 'site-1');
    await user.click(within(dialog).getByRole('button', { name: 'Show commands' }));
    const cmd = await screen.findByTestId('command-linux/amd64');
    await user.click(within(cmd).getByRole('button', { name: 'Copy command' }));
    expect(await navigator.clipboard.readText()).toContain(secret);
    expect(screen.getByTestId('location').textContent).toBe('/settings/tokens');
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    await user.click(screen.getByRole('button', { name: 'I have saved the token' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.body.textContent).not.toContain(secret);
  });
});

describe('Add host dialog: states and errors', () => {
  it('the variant "token from a file" renders without a token and sends the file paths', async () => {
    const bodies = recordRenderBodies();
    const { user, dialog } = await openDialog('admin');
    await user.click(within(dialog).getByText('Token from a file'));
    await user.type(within(dialog).getByLabelText(/Token file on Linux/), '/etc/myriarch/token');
    await showCommands(user);
    await screen.findByTestId('add-host-commands');
    expect(JSON.parse(bodies[0]!)).toMatchObject({
      variant: 'token_file',
      tokenFiles: { linux: '/etc/myriarch/token' },
      attributes: { 'site.type': 'edge', 'site.id': 'site-1' },
    });
    expect(within(dialog).queryByTestId('add-host-warning')).toBeNull();
    // Copy works without any token.
    const cmd = screen.getByTestId('command-linux/amd64');
    await userEvent.setup().click(within(cmd).getByRole('button', { name: 'Copy command' }));
    expect(await navigator.clipboard.readText()).toContain('install --server');
  });

  it('a server without a bundle (a local build) says so and offers no form', async () => {
    server.use(
      http.get('/api/v1/install/bundles', () =>
        HttpResponse.json({ source: 'none', reason: 'local_build', items: [] }),
      ),
    );
    renderApp({ route: '/agents' });
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Add host' }));
    const state = await screen.findByTestId('add-host-no-bundle');
    expect(state.textContent).toMatch(/local build/);
    expect(screen.queryByRole('button', { name: 'Show commands' })).toBeNull();
  });

  it('a server with install turned off says so', async () => {
    server.use(http.get('/api/v1/install/bundles', () => problem(404, 'not_found')));
    renderApp({ route: '/agents' });
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Add host' }));
    expect(await screen.findByText(/turned off on this server/)).toBeTruthy();
  });

  it('shows a bundle that is being prepared and lets the person try again', async () => {
    let calls = 0;
    server.use(
      http.post('/api/v1/install/commands', () => {
        calls++;
        return calls === 1 ? problem(503, 'unavailable', 'bundle_preparing') : undefined;
      }),
    );
    const { user } = await openDialog('admin');
    await user.click(screen.getByText('Token from a file'));
    await showCommands(user);
    const err = await screen.findByTestId('add-host-error');
    expect(err.textContent).toMatch(/being prepared/);
    expect(err.textContent).toMatch(/try again/i);
    await user.click(within(err).getByRole('button', { name: 'Try again' }));
    expect(await screen.findByTestId('add-host-commands')).toBeTruthy();
    expect(screen.queryByTestId('add-host-error')).toBeNull();
  });

  it.each([
    [412, 'precondition_failed', 'local_build', /local build/],
    [412, 'precondition_failed', 'server_settings', /client certificates/],
    [422, 'validation_failed', 'variant_not_available', /https/],
    [422, 'validation_failed', 'platform_not_supported', /platform is not supported/],
    [503, 'unavailable', 'bundle_failed', /failed/],
    [409, 'conflict', undefined, /conflicts with the state/],
  ] as const)('explains %i %s %s with a next step', async (status, code, reason, text) => {
    server.use(http.post('/api/v1/install/commands', () => problem(status, code, reason)));
    const { user } = await openDialog('admin');
    await user.click(screen.getByText('Token from a file'));
    await showCommands(user);
    const err = await screen.findByTestId('add-host-error');
    expect(err.textContent).toMatch(text);
    expect(err.textContent!.length).toBeGreaterThan(60);
    expect(screen.queryByTestId('add-host-commands')).toBeNull();
  });

  it('shows the issues of a refused input (422)', async () => {
    server.use(
      http.post('/api/v1/install/commands', () =>
        problem(
          422,
          'validation_failed',
          'invalid_value',
          'site.id: a quotation mark is not allowed',
        ),
      ),
    );
    const { user } = await openDialog('admin');
    await user.click(screen.getByText('Token from a file'));
    await showCommands(user);
    expect(await screen.findByText(/a quotation mark is not allowed/)).toBeTruthy();
  });

  it('notes that installation with mutual TLS but without a token is not supported', async () => {
    const { dialog } = await openDialog('admin');
    expect(within(dialog).getByText(/mutual TLS but without an enrollment token/)).toBeTruthy();
  });
});
