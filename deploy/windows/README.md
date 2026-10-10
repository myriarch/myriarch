# Windows: services, paths, troubleshooting

Applies to point-of-service devices, workstations and other Windows hosts. Installation uses
[`../scripts/install-windows.ps1`](../scripts/install-windows.ps1) (PowerShell 5.1+, as Administrator).
Do **not** use the contrib MSI: it installs a service of its own, `otelcol-contrib`, which starts immediately
with its own config (two collectors on one host).

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
verifier from `-Verifier`, an already installed `myriarch-updater.exe` or `cosign.exe` 3.0.4 or newer (from `PATH`, but
only from a directory that only Administrators can write); the updater that it downloads in the same run is never the
verifier of the `checksums.txt` that carries its own hash. It creates its working directory with a protected DACL, hashes verified files again after they
were copied, and copies a `-CaFile` bundle to `C:\ProgramData\Myriarch\config\ca.pem`. Collector secrets (for example
`OTEL_GATEWAY_TOKEN`) are set with [`Set-ServiceEnvironment.ps1`](Set-ServiceEnvironment.ps1):

```powershell
.\Set-ServiceEnvironment.ps1 -EnvFile C:\Temp\secrets.env   # NAME=VALUE per line, delete the file afterwards (keeps secrets out of process logs)
.\Set-ServiceEnvironment.ps1 -Set @{ OTEL_GATEWAY_TOKEN = '...' }
.\Set-ServiceEnvironment.ps1 -List        # names only
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

The verifier of `checksums.txt` is, in this order: `-Verifier` (an installed `myriarch-updater.exe`, or `cosign.exe`), an
already installed `myriarch-updater.exe`, or `cosign.exe` from `PATH` if its directory can only be written by
administrators. A `cosign.exe` must be version 3.0.4 or newer; an older one stops the installation with
`sigstore_verifier_outdated`. The updater that is being installed is never used for this, because its hash comes from the
file that the verifier has to check. A re-run of the installer therefore verifies a new release with the updater that is
already installed. A host with neither a verifier nor an installed updater verifies `checksums.txt` on an admin host (see
[Verify releases](../../docs/security/verify-releases.md)) and passes `-UpdaterSha256`; the updater that was checked by
that hash then also verifies the collector and supervisor bundles.

Example (mirror `myriarch-releases` with anonymous read access from the site network):

```powershell
.\deploy\scripts\install-windows.ps1 -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile C:\Temp\enrollment-token.txt `
  -SiteType edge -SiteId 1001 -CollectorVersion 0.161.0 -WithUpdater `
  -UpdaterUrl https://mirror.example.com/myriarch-releases/v<ver>/myriarch-updater_<ver>_windows_amd64.exe `
  -UpdaterChecksumsUrl https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt -Verifier C:\Tools\cosign.exe
```

Both hash sources together are allowed (then both must match). The check of `checksums.txt` also applies with
`-Insecure`, but it cannot be combined with `-VerifySignatures skip`. `-SigningKey` and `-UpdaterChecksumsSigUrl` (the
key-signed `checksums.txt.sig` of earlier builds) were removed and fail with `option_removed`; the message names the
replacement. For a mirror that re-signs with its own key, see
[`../../contrib/enterprise-mirror/README.md`](../../contrib/enterprise-mirror/README.md).

Typical errors are `sigstore_verification_failed` (modified `checksums.txt`, another release tag, another workflow or
repository, or a bundle that does not belong to the file), `sigstore_verifier_missing` and `sigstore_verifier_outdated`,
`checksum_entry_count` with 0 lines (`-UpdaterUrl` does not match the release) and `checksum_conflict` (`-UpdaterSha256`
differs from the verified checksum). The internal CA of a mirror must be in the Windows certificate store (`-CaFile` only
applies to the connection to myriarchd).

Known limitation: with App Control for Business (WDAC) script enforcement a PowerShell script that is not signed runs in
Constrained Language Mode, and `install-windows.ps1` fails there. Allow the installer by its hash, and allow the binaries
by a hash rule for each release until the Windows files are Authenticode-signed (then one publisher rule is enough).

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
| `unsupported_platform` | unsupported CPU architecture, or the supervisor on Windows ARM64 (install with `-Arch amd64`) |
| `download_failed` | a download did not succeed (after retries) |
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
| `directory_unsafe` | a data directory is a reparse point, has the wrong owner, has an unexpected ACE, or is not a directory |
| `acl_failed` | `icacls` failed |
| `service_config_failed` | `sc.exe` failed |
| `updater_install_failed` | `myriarch-updater install` failed |
| `service_not_running` | the supervisor service is not running after the installation (fatal on Windows) |
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
6. **Service does not start** (error 1053/1067): check the paths in `supervisor.yaml` (`agent.executable` must
   exist), run `otelcol-contrib.exe --version` manually; a missing `MYRIARCH_ENROLLMENT_TOKEN`
   in the service environment leads to `401` at the server (log `agent.log` / myriarchd audit).
7. **Update fails**: `INSTALL_FAILED` + text in the UI (agent -> packages). Common causes: URL outside
   `allowedUrlPrefixes`, SHA-256/Sigstore mismatch, the new binary rejects the current `effective.yaml`
   (preflight), health check (supervisor/collector) not green after the restart -> automatic rollback.
8. **Uninstall**: `install-windows.ps1 -Uninstall` (data under `C:\ProgramData\Myriarch` is kept; `-Purge`
   also removes identity and queue).
