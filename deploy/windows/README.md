# Windows: services, paths, troubleshooting

Applies to point-of-service devices, workstations and other Windows hosts. Installation uses
[`../scripts/install-windows.ps1`](../scripts/install-windows.ps1) (PowerShell 5.1+, as Administrator), which you start as
described in [Start the installer](#start-the-installer).
Do **not** use the contrib MSI: it installs a service of its own, `otelcol-contrib`, which starts immediately
with its own config (two collectors on one host). The supervisor MSI is no substitute either
([Packages of the upstream project](#packages-of-the-upstream-project)).

## Start the installer

The script is part of the zip archive `myriarch-updater_<version>_windows_<arch>.zip` of a release, next to the templates that it
renders. It reads them from the directory above its own (`deploy\`), so run it from the unpacked archive; a script that was
copied out of the archive stops with `template_missing`.

1. Create a folder that only SYSTEM and the administrators can read and write, and download the archive,
   `checksums.txt` and `checksums.txt.sigstore.json` of the release into it. Verify them as
   [Verify releases](../../docs/security/verify-releases.md) describes, then unpack the archive into its subfolder `package`.
   The download, the checks (which end at the first failure) and the unpack are in step 2 of
   [Connect a host](../../docs/tasks/connect-a-host.md#2-download-and-verify-the-installer). The folder gets a DACL of its own.
   Without it, every local user can read what you put into a new folder below `C:\Program Files`, the token included, and
   unprivileged accounts can write `C:\Temp`, a user profile and a download folder and replace a checked file before the run.

   ```powershell
   New-Item -ItemType Directory -Path 'C:\Program Files\myriarch-setup' -Force | Out-Null
   icacls.exe 'C:\Program Files\myriarch-setup' /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F'
   if ($LASTEXITCODE -ne 0) { throw 'icacls could not restrict the setup folder' }
   ```

2. Write the enrollment token into a file in that folder (it inherits the DACL) and delete the file after the run. Open an
   elevated Windows PowerShell in the unpacked archive, remove the Mark of the Web from the script **only after** the checks of
   step 1 passed, and start the script in a new PowerShell process with the execution policy for that process only:

   ```powershell
   Unblock-File .\deploy\scripts\install-windows.ps1
   powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\deploy\scripts\install-windows.ps1 `
     -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile 'C:\Program Files\myriarch-setup\enrollment-token.txt' `
     -SiteType edge -SiteId 1001 -CollectorVersion 0.162.0 `
     -Verifier 'C:\Program Files\cosign\cosign.exe'
   ```

The inputs that the installer needs are `-OpampEndpoint`, `-TokenFile` (or `-Token`), `-SiteType`, `-SiteId`, `-CollectorVersion`
(without it the installer stops with `option_missing`) and a Sigstore verifier: `-Verifier` (`cosign.exe` 3.0.4 or newer, or the
`myriarch-updater.exe` of the verified archive, in a folder that only administrators can write), or `-WithUpdater` with
`-UpdaterSha256` (without one: `sigstore_verifier_missing`). [Connect a host](../../docs/tasks/connect-a-host.md) lists every
input and the commands for the download and the verification.

What the start does and what it avoids:

- `-ExecutionPolicy RemoteSigned` on the command line applies to this one PowerShell process (scope *Process*) and is gone
  when it ends. A Windows client defaults to `Restricted`, which refuses every script, and Windows Server to `RemoteSigned`,
  which refuses a script that carries the Mark of the Web. `RemoteSigned` runs a script that is local and unblocked, and a file
  that was not unblocked stays blocked. The Windows files of v0.1.0 are not Authenticode-signed.
- `-NoProfile` keeps the profile scripts of the account out of the elevated process.
- Do not use `-ExecutionPolicy Bypass`, which switches the check off, and no `Set-ExecutionPolicy` for the machine or the user,
  which stays after the installation and weakens the host for everyone.
- A policy set by Group Policy wins over the parameter. `Get-ExecutionPolicy -List` shows it: a value other than `Undefined` for
  `MachinePolicy` or `UserPolicy` decides. If that policy forbids the script (`Restricted`, or `AllSigned`, which also refuses
  unsigned files), the installer cannot start; ask the owner of the policy for an exception.
- App Control for Business with script enforcement is a separate limit: see
  [Language mode and App Control](#language-mode-and-app-control).
- `Set-ServiceEnvironment.ps1` is a script too. Start it the same way. `-File` passes strings only, so use `-EnvFile` with it, not
  `-Set`, which takes a hashtable.

## Packages of the upstream project

Install neither MSI of the upstream project on a host that myriarch manages. The contrib MSI installs a service `otelcol-contrib`,
which starts at once with its own configuration. The supervisor MSI installs a service `opampsupervisor` with manual start, in a
folder of its own. myriarch manages neither: the installer creates the service `otelcol-supervisor` and puts the supervisor into
`C:\Program Files\Myriarch\supervisor\`. The installer of v0.1.0 does not look for the upstream services and does not remove
them. From v0.2.0 it looks first: when a service `opampsupervisor` or `otelcol-contrib` exists, in any state, it stops with
`conflicting_install` before it stops a service, downloads or writes anything. The message names the service and the next step,
and nothing is removed. A service that you created yourself under one of these names counts too (`sc.exe delete <name>` removes
it). To see what a host has:

```powershell
Get-Service otelcol-supervisor, opampsupervisor, otelcol-contrib -ErrorAction SilentlyContinue
Test-Path 'C:\Program Files\Myriarch\supervisor\opampsupervisor.exe'
```

If `opampsupervisor` or `otelcol-contrib` is listed, remove the MSI (*Settings*, *Apps*, or `msiexec /x` with the MSI file) and run
the installer again. If `otelcol-supervisor` is missing or the file does not exist, the installer stopped before it got that far:
read its last `ERROR [<code>]` line ([Error codes](#error-codes)).

## Services

| Service | Display name | Binary | Account | Purpose |
|---|---|---|---|---|
| `otelcol-supervisor` | Myriarch Collector (OpAMP Supervisor) | `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe --config C:\ProgramData\Myriarch\config\supervisor.yaml` | `NT SERVICE\otelcol-supervisor` (virtual account) | starts and manages `otelcol-contrib.exe`, talks OpAMP to myriarchd |
| `myriarch-updater` | Myriarch Updater | `C:\Program Files\Myriarch\updater\myriarch-updater.exe run --config C:\ProgramData\Myriarch\config\updater.yaml` | LocalSystem | installs collector updates (download, SHA-256, Sigstore, swap, health check, rollback) |

**Least privilege**: the supervisor and the collector it starts do **not** run as SYSTEM (the collector applies the
remote config). The virtual account has read access only to `supervisor.yaml` and `fallback.yaml`, and modify
rights on `...\supervisor` and `...\storage`; the token, `updater.yaml` and the updater state stay reserved for
SYSTEM/Administrators. For the host receivers the account is added to the groups *Event Log Readers* and
*Performance Monitor Users* (a warning is printed on failure). If a receiver needs further rights, grant them to
the account specifically; do not use LocalSystem. Only `myriarch-updater` runs as SYSTEM.

Startup type *Automatic*; recovery of the supervisor service: restart after 5 s (1st and 2nd failure),
then after 30 s (`sc.exe failure ... actions= restart/5000/restart/5000/restart/30000`).
The updater restarts the **supervisor service** (not just the collector) because the supervisor reads
`agent.executable` and the component list only at startup.

## Paths

| Path | Contents |
|---|---|
| `C:\Program Files\Myriarch\collector\versions\<v>\otelcol-contrib.exe` | installed versions (+ `.myriarch-install.json` = what was verified) |
| `C:\Program Files\Myriarch\collector\bin\otelcol-contrib.exe` | **active** copy that `agent.executable` points to; `.previous` = predecessor (rollback) |
| `C:\Program Files\Myriarch\collector\bin\current-version` | active version (maintained by the updater) |
| `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe` | supervisor |
| `C:\ProgramData\Myriarch\config\` | `supervisor.yaml`, `fallback.yaml`, `updater.yaml`, `enrollment-token`, `trusted_root.json` (SYSTEM/Administrators only) |
| `C:\ProgramData\Myriarch\supervisor\` | supervisor storage: `persistent_state.yaml` (instance UID), `effective.yaml`, `agent.log` |
| `C:\ProgramData\Myriarch\storage\` | persistent send queue of the collector (blueprint parameter `storageDir`) |
| `C:\ProgramData\Myriarch\updater\` | updater state: `instance_uid`, `packages.json`, `update-journal.json`, `downloads\` |
| `C:\ProgramData\Myriarch\logs\myriarch-updater.log` | updater log (rotating) |

The enrollment token is **not** in `supervisor.yaml` but in the service environment
(registry `HKLM\SYSTEM\CurrentControlSet\Services\otelcol-supervisor`, `Environment`, REG_MULTI_SZ). The
ACL of this key is replaced first by a fresh descriptor for SYSTEM/Administrators only and read back; any other allow
entry stops the installation (`acl_failed`), and only then is the token written. The installer takes its Sigstore
verifier from `-Verifier` (in a directory that only Administrators can write), an already installed `myriarch-updater.exe` or `cosign.exe` 3.0.4 or newer (from `PATH`, but
only from a directory that only Administrators can write); the updater that it downloads in the same run is never the
verifier of the `checksums.txt` that carries its own hash. It creates its working directory with a protected DACL, hashes verified files again after they
were copied, and copies a `-CaFile` bundle to `C:\ProgramData\Myriarch\config\ca.pem`. Collector secrets (for example
`OTEL_GATEWAY_TOKEN`) are set with [`Set-ServiceEnvironment.ps1`](Set-ServiceEnvironment.ps1). Start it like the installer
([Start the installer](#start-the-installer)), in the unpacked archive. `-EnvFile` reads one `NAME=VALUE` per line, which keeps
the secrets out of the process logs and the command history. Write that file into the setup folder of
[Start the installer](#start-the-installer) (step 1 creates it, and only SYSTEM and the administrators can read it) and delete it
afterwards:

```powershell
Unblock-File .\deploy\windows\Set-ServiceEnvironment.ps1        # only after the checks of the download passed
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\deploy\windows\Set-ServiceEnvironment.ps1 `
  -EnvFile 'C:\Program Files\myriarch-setup\secrets.env'
Remove-Item 'C:\Program Files\myriarch-setup\secrets.env'
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\deploy\windows\Set-ServiceEnvironment.ps1 -List     # names only
```

Crash or power loss during an update: when the service starts, the updater compares the active binary with the
installed version (`bin\otelcol-contrib.exe` is missing or differs) and restores it from `collector\versions\<v>`
before it checks health; the journal stays until the host is healthy again.

## Golden images / cloning

`C:\ProgramData\Myriarch\supervisor\persistent_state.yaml` and `...\updater\instance_uid` hold the identity
of the host. Before creating an image, empty both directories (stop the services first),
otherwise all clones report the same instance UID and displace each other on the server
(`identity_conflict`). The installer only creates the directories empty.

## Verifying the updater binary (the project's own binary, runs as SYSTEM)

Releases are signed keyless (Sigstore, [ADR-0023](../../docs/adr/0023-keyless-release-signing.md)): the release workflow signs
`checksums.txt` and publishes the bundle `checksums.txt.sigstore.json`. With `-WithUpdater` the installer needs a trusted
hash source for `myriarch-updater.exe` (at least one of the first two):

| Parameter | Meaning |
|---|---|
| `-UpdaterSha256 <hex>` | expected SHA-256 from another trusted source, for example verified on an admin host. This is the way for hosts without a verifier |
| `-UpdaterChecksumsUrl <URL>` | `checksums.txt` of the myriarch release. The installer verifies its bundle (default `<URL>.sigstore.json`, or `-UpdaterBundleUrl`) against the identity `^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v<version>$` with the issuer `https://token.actions.githubusercontent.com`, then takes the hash from the line whose file name matches the one in `-UpdaterUrl` |
| `-UpdaterVersion <X.Y.Z>` | the release version of the updater. Default: the version in the file name `myriarch-updater_<version>_windows_<arch>.exe` of `-UpdaterUrl`; when both are given they must agree. The identity is bound to this version, so a genuine but older release does not pass |
| `-UpdaterBundleUrl <URL>` | different location of the bundle (default: `<checksums-url>.sigstore.json`) |
| `-UpdaterSignerThumbprint <hex>` | optional: the Authenticode signature of the updater must be valid (`Get-AuthenticodeSignature`, status `Valid`) and come from this certificate. A pinned thumbprint breaks at every renewal of the certificate, so use it only with a private PKI whose certificate you control |

The verifier of `checksums.txt` is, in this order: `-Verifier` (an installed `myriarch-updater.exe`, or `cosign.exe`; it must be in
a directory that only Administrators can write, otherwise the installation stops with `directory_unsafe`. A folder that an
administrator creates directly under `C:\` usually grants Modify to every authenticated user, so do not use `C:\Tools`), an
already installed `myriarch-updater.exe`, or `cosign.exe` from `PATH` if its directory can only be written by
administrators. A `cosign.exe` must be version 3.0.4 or newer; an older one stops the installation with
`sigstore_verifier_outdated`. The updater that is being installed is never used for this, because its hash comes from the
file that the verifier has to check. A re-run of the installer therefore verifies a new release with the updater that is
already installed. A host with neither a verifier nor an installed updater passes the `myriarch-updater.exe` of the verified
archive as `-Verifier` (the file in the root of the unpacked zip, not the updater that is being installed), or verifies
`checksums.txt` on an admin host (see [Verify releases](../../docs/security/verify-releases.md)) and passes `-UpdaterSha256`;
the updater that was checked by that hash then also verifies the collector and supervisor bundles.

Example (mirror `myriarch-releases` with anonymous read access from the site network):

```powershell
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File .\deploy\scripts\install-windows.ps1 `
  -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile 'C:\Program Files\myriarch-setup\enrollment-token.txt' `
  -SiteType edge -SiteId 1001 -CollectorVersion 0.161.0 -WithUpdater `
  -UpdaterUrl https://mirror.example.com/myriarch-releases/v<ver>/myriarch-updater_<ver>_windows_amd64.exe `
  -UpdaterChecksumsUrl https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt -Verifier 'C:\Program Files\cosign\cosign.exe'
```

Both hash sources together are allowed (then both must match). The check of `checksums.txt` also applies with
`-Insecure`, but it cannot be combined with `-VerifySignatures skip`. `-SigningKey` and `-UpdaterChecksumsSigUrl` (the
key-signed `checksums.txt.sig` of earlier builds) were removed and fail with `option_removed`; the message names the
replacement. For a mirror that re-signs with its own key, see
[`../../contrib/enterprise-mirror/README.md`](../../contrib/enterprise-mirror/README.md).

Typical errors are `sigstore_verification_failed` (modified `checksums.txt`, another release tag, another workflow or
repository, a bundle that does not belong to the file, or a host that cannot reach the Sigstore TUF repository
`tuf-repo-cdn.sigstore.dev` and got no `-TrustedRoot`), `sigstore_verifier_missing` and `sigstore_verifier_outdated`,
`checksum_entry_count` with 0 lines (`-UpdaterUrl` does not match the release) and `checksum_conflict` (`-UpdaterSha256`
differs from the verified checksum). The internal CA of a mirror must be in the Windows certificate store (`-CaFile` only
applies to the connection to myriarchd).

## Language mode and App Control

With App Control for Business (WDAC) script enforcement a PowerShell script that is not signed runs in Constrained Language
Mode, and `install-windows.ps1` fails there. Allow the installer by its hash, and allow the binaries by a hash rule for each
release until the Windows files are Authenticode-signed (then one publisher rule is enough). Create each rule from the copy
that you verified with the App Control tools: the hash that App Control uses for a script is not the SHA-256 of
`checksums.txt`. App Control decides this independently of the execution policy, and `-ExecutionPolicy RemoteSigned` does not
change it. `$ExecutionContext.SessionState.LanguageMode` shows the mode of a session.

From v0.2.0 the installer checks the language mode first, before it reads an input, stops a service or writes a file. In any mode
other than `FullLanguage` it stops with `language_mode_unsupported`, and the message names the file and the rule that lets it
run. The installer of v0.1.0 fails on its first method call with the PowerShell error "Cannot invoke method. Method invocation
is supported only on core types in this language mode", which does not name the cause.

## Error codes

Every fatal error of `install-windows.ps1` prints exactly one line to stderr and exits with status 1:

```text
[myriarch-install] ERROR [<code>] <message>
```

Warnings look like `WARNING: [myriarch-install] [<code>] <message>` (PowerShell adds the `WARNING: ` prefix itself).
Info lines keep the form `[myriarch-install] <message>`. The codes are lower snake case (`^[a-z][a-z0-9_]*$`) and
a **stable interface**: scripts and tests match the code, never the message text, which may be reworded.
`install-linux.sh` uses the same codes for the same conditions (it prints
`[myriarch-install] ERROR [<code>] <message>` and `[myriarch-install] WARNING [<code>] <message>`); its catalog
is at the top of that script.

| Code | Meaning |
|---|---|
| `usage_error` | raised by `install-linux.sh` only. On Windows, PowerShell itself rejects unknown or valueless parameters before the script runs (non-zero exit, native PowerShell error, no `[code]`) |
| `option_missing` | a required option is absent (site type/id, collector version, token or token file, `-UpdaterUrl` with `-WithUpdater`, `-UpdaterVersion` when the file name in `-UpdaterUrl` carries no version and `-UpdaterChecksumsUrl` is given) |
| `option_invalid` | malformed value: versions, arch, SHA-256 or thumbprint format, control characters, token characters, endpoint scheme (and an unsupported `-ReleaseBaseUrl` scheme), no file name in `-UpdaterUrl`. A plain `http://` or `file://` base URL raises `https_required` instead |
| `option_removed` | a removed option: `-SigningKey` or `-UpdaterChecksumsSigUrl`; the message names the replacement (`-UpdaterChecksumsUrl` with its bundle, `-UpdaterBundleUrl`) |
| `option_conflict` | mutually exclusive or dependent options: `-Token` + `-TokenFile`; updater options (`-UpdaterVersion`, `-UpdaterChecksumsUrl`, `-UpdaterBundleUrl`, `-UpdaterSignerThumbprint`) without `-WithUpdater`; `-UpdaterBundleUrl` without `-UpdaterChecksumsUrl`; `-UpdaterVersion` and the version in `-UpdaterUrl` differ; `-UpdaterChecksumsUrl` with `-VerifySignatures skip` |
| `updater_hash_source_missing` | `-WithUpdater` without `-UpdaterSha256` or `-UpdaterChecksumsUrl` |
| `insecure_required` | plain `ws://`/`http://` endpoint or `-VerifySignatures skip` without `-Insecure` |
| `https_required` | non-https download URL or redirect without `-Insecure` |
| `file_unreadable` | token file, CA file, trusted root or verifier is not usable |
| `tool_missing` | a required tool is absent (`tar.exe`, or `Get-AuthenticodeSignature` with `-UpdaterSignerThumbprint`); the message names the tool |
| `sigstore_verifier_missing` | no Sigstore verifier (`-Verifier`, an installed updater, or `cosign.exe` from an administrator-only directory) |
| `sigstore_verifier_outdated` | `cosign.exe` is older than 3.0.4 or reports no usable version |
| `privilege_required` | not running as Administrator |
| `conflicting_install` | (from v0.2.0) a service `opampsupervisor` (supervisor MSI) or `otelcol-contrib` (contrib MSI) exists, in any state. The check runs before the installer stops a service, downloads or writes anything; the message names the service and the next step, and nothing is removed ([Packages of the upstream project](#packages-of-the-upstream-project)). `install-linux.sh` raises the code for an active or enabled unit `otelcol-contrib.service` or `opampsupervisor.service` of a deb or rpm package |
| `language_mode_unsupported` | (from v0.2.0) PowerShell runs the script in Constrained Language Mode or another mode below `FullLanguage`, usually because App Control for Business with script enforcement allows no unsigned script. It is the first check of a run and changes nothing. Allow the installer with a hash rule ([Language mode and App Control](#language-mode-and-app-control)) |
| `unsupported_platform` | unsupported CPU architecture, or the supervisor on Windows ARM64 (install with `-Arch amd64`) |
| `download_failed` | a download did not succeed: a permanent error (for example HTTP 404) at once, a transient one (timeout, failed connection, HTTP 408, 429, 500, 502, 503, 504) after the retry window of 2 minutes; the message names the number of attempts. Under PowerShell 7.5 or later behind an `HTTPS_PROXY` that refuses the connection, the status of the proxy decides (`proxy HTTP 503` is retried, `proxy HTTP 407` is not); PowerShell 7.4 retries it like a failed connection |
| `template_missing` | template directory or template file not found |
| `template_placeholder` | unreplaced `@@PLACEHOLDER@@` left in a rendered template |
| `checksum_invalid` | malformed `.sha256` file |
| `checksum_mismatch` | SHA-256 of a downloaded file differs (the message names the file) |
| `checksum_entry_count` | `checksums.txt` has N != 1 lines for the asset (the message contains N) |
| `checksum_conflict` | `-UpdaterSha256` contradicts the verified `checksums.txt` |
| `sigstore_verification_failed` | Sigstore bundle verification failed (wrong identity or version, modified file) |
| `authenticode_invalid` | the Authenticode signature of the updater is invalid or from another certificate |
| `archive_invalid` | the collector archive has no regular binary in its root |
| `collector_start_failed` | the new collector binary cannot be started |
| `collector_version_mismatch` | the collector reports another version than requested |
| `supervisor_missing` | (from v0.2.0) the supervisor is not there when its installation step is over: `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe` does not exist, is not a regular file (a directory, a link) or is not the file whose SHA-256 was verified. It is also raised in place of `service_not_running` when the service is not running because the file of its binPath is gone. The message names the path. The usual cause is a security tool that quarantined the unsigned binary: look at its protection history (Microsoft Defender: *Windows Security*, *Protection history*, or `Get-MpThreatDetection`), allow the folder and run the installer again. `install-linux.sh` raises it for a missing or non-executable `/opt/myriarch/supervisor/opampsupervisor` |
| `directory_unsafe` | a data directory is a reparse point, has the wrong owner, has an unexpected ACE, or is not a directory; or `-Verifier` is in a directory that unprivileged accounts can change |
| `acl_failed` | `icacls` failed |
| `service_config_failed` | `sc.exe` failed |
| `updater_install_failed` | `myriarch-updater install` failed |
| `service_not_running` | the supervisor service is not running after the installation (fatal on Windows). From v0.2.0 the installer first checks the file of the service's binPath and reports `supervisor_missing` when it is gone |
| `unexpected_error` | a failure that is not classified above; the original message follows the code |

Warning codes: `token_on_command_line` (`-Token` is visible in process logs), `updater_unauthenticated` (updater
binary without a trusted hash, only with `-Insecure`), `service_not_running` (printed before the fatal error of the same
name) and `group_membership_failed` (the service account could not be added to a local group).

## Troubleshooting

1. **Service status**: `Get-Service otelcol-supervisor, myriarch-updater` or `sc.exe query otelcol-supervisor`.
2. **Event Viewer** (`eventvwr.msc`) -> Windows Logs -> *Application*: sources `otelcol-supervisor`
   (supervisor/service start) and `myriarch-updater` (service errors). Via PowerShell:
   `Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='otelcol-supervisor'} -MaxEvents 30`.
3. **Collector log**: `C:\ProgramData\Myriarch\supervisor\agent.log` (stdout/stderr of the collector, written by
   the supervisor).
4. **Updater log**: `C:\ProgramData\Myriarch\logs\myriarch-updater.log`; incomplete updates are shown by
   `myriarch-updater.exe status --config C:\ProgramData\Myriarch\config\updater.yaml` (the updater repairs them
   when the service starts: new version healthy = keep it, otherwise roll back to `.previous`).
5. **Health**: supervisor `http://localhost:13134/health`, collector `http://localhost:13133/`
   (`Invoke-WebRequest -UseBasicParsing`).
6. **Service does not start** (error 1053/1067): check that `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe` exists
   (security software quarantines unsigned binaries: look at the protection history), check the paths in `supervisor.yaml`
   (`agent.executable` must exist), run `otelcol-contrib.exe --version` manually; a missing `MYRIARCH_ENROLLMENT_TOKEN`
   in the service environment leads to `401` at the server (log `agent.log` / myriarchd audit).
7. **Update fails**: `INSTALL_FAILED` + text in the UI (agent -> packages). Common causes: URL outside
   `allowedUrlPrefixes`, SHA-256/Sigstore mismatch, the new binary rejects the current `effective.yaml`
   (preflight), health check (supervisor/collector) not green after the restart -> automatic rollback.
8. **Uninstall**: `install-windows.ps1 -Uninstall`, started like the installer ([Start the installer](#start-the-installer));
   data under `C:\ProgramData\Myriarch` is kept, `-Purge` also removes identity and queue.
9. **No `otelcol-supervisor` service after the installer ran**: it stopped before it created the service. Read its last
   `ERROR [<code>]` line ([Error codes](#error-codes)). From v0.2.0 the installer names an MSI of the upstream project
   (`conflicting_install`) and a supervisor file that is not there (`supervisor_missing`); with the installer of v0.1.0 check
   whether such an MSI is installed ([Packages of the upstream project](#packages-of-the-upstream-project)).
