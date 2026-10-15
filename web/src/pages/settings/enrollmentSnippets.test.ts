// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { WEBSITE } from '@/brand';

import { linuxInstallCommand, windowsInstallCommand } from './enrollmentSnippets';

/** The lines of a snippet that are not comments. */
function codeLines(cmd: string): string[] {
  return cmd.split('\n').filter((l) => !l.trimStart().startsWith('#'));
}

/** True when an option stands as a word of its own in the code of the snippet. */
function names(cmd: string, option: string): boolean {
  return codeLines(cmd).some((l) => new RegExp(`(^|\\s)${option}(\\s|$)`).test(l));
}

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

  it('names every input that the Linux installer requires', () => {
    // deploy/scripts/install-linux.sh stops without the first five (option_missing, option_invalid for the endpoint) and
    // without a verifier (sigstore_verifier_missing); internal/updater/deploy_snippets_test.go reads the required options
    // from the script
    const cmd = linuxInstallCommand(input);
    for (const option of [
      '--opamp-endpoint',
      '--token-file',
      '--site-type',
      '--site-id',
      '--collector-version',
      '--verifier',
    ]) {
      expect(names(cmd, option), option).toBe(true);
    }
    expect(cmd).toContain('--opamp-endpoint wss://fleet.example.com/v1/opamp');
    expect(cmd).toContain('--collector-version <collector-version>');
  });

  it('names every input that the Windows installer requires', () => {
    const cmd = windowsInstallCommand(input);
    for (const parameter of [
      '-OpampEndpoint',
      '-TokenFile',
      '-SiteType',
      '-SiteId',
      '-CollectorVersion',
      '-Verifier',
    ]) {
      expect(names(cmd, parameter), parameter).toBe(true);
    }
    expect(cmd).toContain("-OpampEndpoint 'wss://fleet.example.com/v1/opamp'");
    expect(cmd).toContain("-CollectorVersion '<collector-version>'");
    // a cosign.exe is only taken from a folder that only administrators can write
    expect(cmd).toContain("-Verifier 'C:\\Program Files\\cosign\\cosign.exe'");
  });

  it('runs the scripts from the unpacked archive, where their templates are', () => {
    const linux = linuxInstallCommand(input);
    expect(linux).toContain('sudo ./deploy/scripts/install-linux.sh');
    expect(linux).not.toContain('sudo ./install-linux.sh');
    const windows = windowsInstallCommand(input);
    expect(windows).toContain('-File .\\deploy\\scripts\\install-windows.ps1');
    expect(windows).not.toContain('\n.\\install-windows.ps1');
  });

  it('starts the Windows installer with an execution policy for that one process', () => {
    const cmd = windowsInstallCommand(input);
    const start = codeLines(cmd).find((l) => l.includes('install-windows.ps1'));
    expect(start).toBe(
      'powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\\deploy\\scripts\\install-windows.ps1 `',
    );
    // never a weaker or a lasting policy: Bypass switches the check off, Set-ExecutionPolicy changes the machine or the user
    expect(cmd).not.toMatch(/bypass|unrestricted|set-executionpolicy|-scope/i);
  });

  it('leaves Unblock-File to the operator, after the checks of the archive', () => {
    const cmd = windowsInstallCommand(input);
    expect(cmd).toContain('Unblock-File');
    expect(codeLines(cmd).join('\n')).not.toContain('Unblock-File');
  });

  it('points to the guide on the website, built from the brand constants', () => {
    expect(linuxInstallCommand(input)).toContain(`# ${WEBSITE}/docs/tasks/connect-a-host/`);
    expect(windowsInstallCommand(input)).toContain(`# ${WEBSITE}/docs/tasks/connect-a-host/`);
  });

  it('keeps the token out of every line but the one that writes it to the file', () => {
    for (const cmd of [linuxInstallCommand(input), windowsInstallCommand(input)]) {
      const withSecret = cmd.split('\n').filter((l) => l.includes(input.secret));
      expect(withSecret).toHaveLength(1);
      // a shell builtin or a cmdlet writes the file: no program gets the token in its arguments
      expect(withSecret[0]).toMatch(/^(\(umask 077; printf |Set-Content )/);
      expect(cmd).not.toMatch(/\becho\b|write-host|write-output|set -x|start-transcript|\btee\b/i);
    }
  });
});
