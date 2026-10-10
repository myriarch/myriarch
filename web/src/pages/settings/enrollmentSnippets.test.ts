// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { linuxInstallCommand, windowsInstallCommand } from './enrollmentSnippets';

describe('enrollment install snippets', () => {
  const input = { endpoint: 'wss://fleet.example.com/v1/opamp', secret: 'myre_abc123' };

  it('passes the token through a file, never as a command line argument (Linux)', () => {
    const cmd = linuxInstallCommand(input);
    expect(cmd).toContain('--token-file ./enrollment-token');
    expect(cmd).not.toMatch(/--token\s/);
    expect(cmd).toContain('myre_abc123');
    expect(cmd.indexOf('printf')).toBeLessThan(cmd.indexOf('install-linux.sh'));
    expect(cmd.trimEnd().endsWith('rm -f ./enrollment-token')).toBe(true);
  });

  it('passes the token through a file, never as a command line argument (Windows)', () => {
    const cmd = windowsInstallCommand(input);
    expect(cmd).toContain('-TokenFile .\\enrollment-token.txt');
    expect(cmd).not.toMatch(/-Token\s/);
    expect(cmd.trimEnd().endsWith('Remove-Item .\\enrollment-token.txt')).toBe(true);
  });

  it('quotes secrets with special characters', () => {
    expect(linuxInstallCommand({ ...input, secret: "a'b" })).toContain(`'a'\\''b'`);
    expect(windowsInstallCommand({ ...input, secret: "a'b" })).toContain("'a''b'");
  });
});
