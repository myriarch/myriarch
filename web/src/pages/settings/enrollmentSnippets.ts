// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  ENROLLMENT_TOKEN_ENV,
  ENROLLMENT_TOKEN_FILE,
  LINUX_COLLECTOR_EXECUTABLE,
  UPDATER_BINARY,
  WEBSITE,
} from '@/brand';

/**
 * Install commands and supervisor snippet of the manual path (v0.1.0). The token dialog no longer shows them: from v0.2.0 the dialog
 * "Add host" renders the commands on the server. The file stays because internal/updater/deploy_snippets_test.go still checks it
 * against the installers.
 *
 * The commands follow the manual path of docs/tasks/connect-a-host.md and name every input that the
 * installers require (deploy/scripts/install-linux.sh: --opamp-endpoint --token-file --site-type --site-id
 * --collector-version, and a Sigstore verifier, --verifier; install-windows.ps1: -OpampEndpoint -TokenFile
 * -SiteType -SiteId -CollectorVersion -Verifier). Without the collector version both installers stop with
 * option_missing, and without a verifier with sigstore_verifier_missing. internal/updater/deploy_snippets_test.go
 * reads the required options from the installers and fails when this file does not name them.
 *
 * Secrets: the token appears exactly once in a command, as the value that a shell builtin (printf) or a cmdlet
 * (Set-Content) writes into a file. No argument of a program, no echo and no log line carries it. The command is text,
 * though: a shell history, a PowerShell transcript or script block log and a terminal recording keep it, and the comments
 * of the examples say so. The file is private before the token is written: mode 0600 on Linux, a DACL for SYSTEM and the
 * administrators on Windows.
 */
export interface SnippetInput {
  endpoint: string;
  secret: string;
  labels?: Record<string, string>;
}

const WINDOWS_TOKEN_FILE = `${ENROLLMENT_TOKEN_FILE}.txt`;
const GUIDE_URL = `${WEBSITE}/docs/tasks/connect-a-host/`;
const LINUX_ARCHIVE = `${UPDATER_BINARY}_<release>_linux_<arch>.tar.gz`;
const WINDOWS_ARCHIVE = `${UPDATER_BINARY}_<release>_windows_<arch>.zip`;

function shellQuote(v: string): string {
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(v) ? v : `'${v.replace(/'/g, `'\\''`)}'`;
}

export function linuxInstallCommand({ endpoint, secret }: SnippetInput): string {
  // The token goes through a file (0600), not the command line: argv shows up in process lists and shell history.
  // The script runs from the unpacked archive, because it reads its templates from the directory above its own, and in a
  // root shell in a directory that only root can write, so that no other account can replace a checked file before root
  // runs it. That is why the command has no sudo.
  const install = [
    './deploy/scripts/install-linux.sh',
    `--opamp-endpoint ${shellQuote(endpoint)}`,
    `--token-file ./${ENROLLMENT_TOKEN_FILE}`,
    '--site-type <edge|office|datacenter|k8s-gateway>',
    '--site-id <id>',
    '--collector-version <collector-version>',
    '--verifier <verifier>',
  ].join(' \\\n  ');
  return [
    `# Run it in a root shell (sudo -i), in the directory of the verified and unpacked archive ${LINUX_ARCHIVE}`,
    '# that only root can write:',
    `# ${GUIDE_URL}`,
    '# <collector-version>: the target version of the fleet, for example 0.162.0',
    '# <verifier>: the full path of cosign 3.0.4 or newer, or of the myriarch-updater of that archive, in a place that only',
    '# root can write. For --with-updater see the page above.',
    '# The token is part of this command: a shell history or the recording of a terminal session keeps it.',
    `(umask 077; printf '%s\\n' ${shellQuote(secret)} > ${ENROLLMENT_TOKEN_FILE})`,
    install,
    `rm -f ./${ENROLLMENT_TOKEN_FILE}`,
  ].join('\n');
}

export function windowsInstallCommand({ endpoint, secret }: SnippetInput): string {
  // The execution policy is set for the one PowerShell process that runs the installer: nothing changes on the machine,
  // and a script that still carries the Mark of the Web stays blocked. Group Policy wins over the parameter.
  const install = [
    'powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\\deploy\\scripts\\install-windows.ps1',
    `-OpampEndpoint '${endpoint.replace(/'/g, "''")}'`,
    `-TokenFile .\\${WINDOWS_TOKEN_FILE}`,
    "-SiteType 'edge'",
    "-SiteId '<id>'",
    "-CollectorVersion '<collector-version>'",
    "-Verifier 'C:\\Program Files\\cosign\\cosign.exe'",
  ].join(' `\n  ');
  // "Only administrators can write" protects the checked files but not the token: a folder below C:\Program Files can be
  // read by every local user. So the token file gets its own DACL (SYSTEM and the administrators) before the token is
  // written, whatever the DACL of the folder is.
  const tokenFile = `.\\${WINDOWS_TOKEN_FILE}`;
  return [
    '# Run it in an elevated Windows PowerShell, in the folder of the verified and unpacked archive',
    `# ${WINDOWS_ARCHIVE}.`,
    '# Only SYSTEM and the administrators may read and write that folder; the page below creates it:',
    `# ${GUIDE_URL}`,
    '# Only after the checks of the archive passed: Unblock-File .\\deploy\\scripts\\install-windows.ps1',
    '# <collector-version>: the target version of the fleet, for example 0.162.0',
    '# -Verifier: cosign.exe 3.0.4 or newer in a folder that only administrators can write, or .\\myriarch-updater.exe of that',
    '# archive. For -WithUpdater see the page above.',
    '# The token is part of this command: a PowerShell transcript, script block logging or the history of the console keeps it.',
    `New-Item -ItemType File -Path ${tokenFile} -Force | Out-Null`,
    `icacls.exe ${tokenFile} /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F'`,
    `Set-Content -Path ${tokenFile} -Value '${secret.replace(/'/g, "''")}' -NoNewline`,
    install,
    `Remove-Item ${tokenFile}`,
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
