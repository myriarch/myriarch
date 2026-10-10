// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ENROLLMENT_TOKEN_ENV,
  ENROLLMENT_TOKEN_FILE,
  LINUX_COLLECTOR_EXECUTABLE,
} from '@/brand';

/**
 * Install command and supervisor snippet shown after creating an enrollment token. Flag names of
 * the install scripts (deploy/scripts/install-linux.sh: --opamp-endpoint --token-file --site-type --site-id;
 * install-windows.ps1: -OpampEndpoint -TokenFile -SiteType -SiteId).
 */
export interface SnippetInput {
  endpoint: string;
  secret: string;
  labels?: Record<string, string>;
}

const WINDOWS_TOKEN_FILE = `${ENROLLMENT_TOKEN_FILE}.txt`;

function shellQuote(v: string): string {
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(v) ? v : `'${v.replace(/'/g, `'\\''`)}'`;
}

export function linuxInstallCommand({ endpoint, secret }: SnippetInput): string {
  // The token goes through a file (0600), not the command line: argv shows up in process lists and shell history.
  const install = [
    'sudo ./install-linux.sh',
    `--opamp-endpoint ${shellQuote(endpoint)}`,
    `--token-file ./${ENROLLMENT_TOKEN_FILE}`,
    '--site-type <edge|office|datacenter|k8s-gateway>',
    '--site-id <id>',
  ].join(' \\\n  ');
  return [
    `(umask 077; printf '%s\\n' ${shellQuote(secret)} > ${ENROLLMENT_TOKEN_FILE})`,
    install,
    `rm -f ./${ENROLLMENT_TOKEN_FILE}`,
  ].join('\n');
}

export function windowsInstallCommand({ endpoint, secret }: SnippetInput): string {
  const install = [
    '.\\install-windows.ps1',
    `-OpampEndpoint '${endpoint.replace(/'/g, "''")}'`,
    `-TokenFile .\\${WINDOWS_TOKEN_FILE}`,
    "-SiteType 'edge'",
    "-SiteId '<id>'",
  ].join(' `\n  ');
  return [
    `Set-Content -Path .\\${WINDOWS_TOKEN_FILE} -Value '${secret.replace(/'/g, "''")}' -NoNewline`,
    install,
    `Remove-Item .\\${WINDOWS_TOKEN_FILE}`,
  ].join('\n');
}

export function supervisorSnippet({ endpoint }: SnippetInput): string {
  return [
    'server:',
    `  endpoint: ${endpoint}`,
    '  headers:',
    `    Authorization: "Bearer \${env:${ENROLLMENT_TOKEN_ENV}}"`,
    'capabilities:',
    '  accepts_remote_config: true',
    '  reports_effective_config: true',
    '  reports_health: true',
    '  reports_available_components: true',
    '  accepts_restart_command: true',
    'agent:',
    `  executable: ${LINUX_COLLECTOR_EXECUTABLE}`,
    'storage:',
    '  directory: /var/lib/otelcol/supervisor',
    '',
  ].join('\n');
}

export function envFileLine(secret: string): string {
  return `${ENROLLMENT_TOKEN_ENV}=${secret}\n`;
}
