---
title: "Connect a host"
weight: 10
description: "Enroll a Linux or Windows host with the installation scripts: the inputs they need, how to download and verify the installer, the exact commands and what to do when the installer stops."
---

A managed host runs three pieces: the upstream **OpAMP Supervisor** (the service `otelcol-supervisor`), the **collector**
(`otelcol-contrib`) that the supervisor starts, and optionally the **`myriarch-updater`**, which installs the collector
updates that `myriarchd` offers. The installation scripts set up exactly this, idempotently:

- Linux with systemd: [`deploy/scripts/install-linux.sh`](../../deploy/scripts/install-linux.sh) (bash). Support level: beta.
- Windows: [`deploy/scripts/install-windows.ps1`](../../deploy/scripts/install-windows.ps1) (PowerShell 5.1 or newer, run as
  Administrator). Support level: experimental.
- Kubernetes: the collector runs as a container; see [deploy/k8s/collector/README.md](../../deploy/k8s/collector/README.md).

The scripts deliberately do not use the packages of the upstream project (deb, rpm and MSI): the contrib packages start a
collector service of their own, and a supervisor from a package is another service in another place than the one that
myriarch manages.

This guide describes the installers of the release v0.1.0. You collect the inputs, download and verify the installer
yourself and run it with explicit options. Steps 1 to 3 are the same on both systems: [create an enrollment
token](#1-create-an-enrollment-token), [download and verify the installer](#2-download-and-verify-the-installer) and
[choose how the host verifies downloads](#3-choose-how-the-host-verifies-downloads). Step 4 [installs on
Linux](#4-install-on-linux), step 5 [installs on Windows](#5-install-on-windows), and step 6 [checks the
result](#6-check-the-result).

## What you need

| Input | Linux option | Windows parameter | Where it comes from |
|---|---|---|---|
| OpAMP endpoint | `--opamp-endpoint` | `-OpampEndpoint` | `wss://<server>/v1/opamp`, the address under which the host reaches `myriarchd`. *Settings*, tab *System*, shows it as "OpAMP endpoint". A plain `ws://` or `http://` endpoint needs `--insecure` and is for labs only |
| Enrollment token | `--token-file` | `-TokenFile` | [Step 1](#1-create-an-enrollment-token). `--token` and `-Token` exist too, but an argument shows up in the process list |
| Site type and ID | `--site-type`, `--site-id` | `-SiteType`, `-SiteId` | You choose them; the selectors of your fleets match on them (below) |
| **Collector version** | `--collector-version` | `-CollectorVersion` | **Required**, without `v`: the version of `otelcol-contrib` to install, for example `0.162.0` (below) |
| Installer release | none | none | The myriarch release whose archive you unpack, for example `0.1.0` ([step 2](#2-download-and-verify-the-installer)) |
| Sigstore verifier | `--verifier` (or `--with-updater` with `--updater-sha256`) | `-Verifier` (or `-WithUpdater` with `-UpdaterSha256`) | [Step 3](#3-choose-how-the-host-verifies-downloads) |
| Updater (optional) | `--with-updater`, `--updater-url` and a hash source | `-WithUpdater`, `-UpdaterUrl` and a hash source | [Step 3](#3-choose-how-the-host-verifies-downloads) and the commands of step 4 or 5 |

**Which collector version?** The installer does not ask the server, so you name the version. Install the **target version
of the fleet** that the host should join: the page *Fleets* shows it (column *Target version*). It comes from
`spec.collector.version` in the fleet file of your GitOps repository, unless an operator chose another version in the UI
within the frame that Git sets ([Fleets](../concepts/fleets.md#the-effective-target)). If you install another version, the host
runs it until a version rollout of its fleet changes it: the planner counts a member whose version differs from the target as
out of date ([Rollouts](../concepts/rollouts.md#when-do-rollouts-arise)). The installer downloads `otelcol-contrib` and the
supervisor of that version from the upstream releases (a 404 means that no such release exists).

The site options become attributes of the host (`site.type`, `site.id`, `site.name`, `site.country`, `site.region`,
`site.timezone`, and `deployment.environment.name` from `--environment`, default `prod`). The selectors of your fleets match
on them, so choose them to fit your repository: the example fleets select `site.type` `edge` or `datacenter` for hosts and
`cloud` for Kubernetes gateways.

**The host** needs:

- Linux: systemd, root (`sudo`), amd64 or arm64, and the tools `systemctl`, `useradd` or `adduser`, `curl`, `tar` together
  with `gzip` (GNU `tar` runs `gzip` to unpack the `.tar.gz` archives), `install`, `cmp` and `sha256sum` or `shasum`. A missing
  tool stops the script with the error code `tool_missing` and the name of the tool.
- Windows: an elevated Windows PowerShell 5.1 or newer, and `tar.exe` (Windows 10 1803 or Windows Server 2019 and newer). The
  upstream supervisor exists for amd64 only; on Windows ARM64 pass `-Arch amd64`.
- https access to `github.com` and its release hosts for the collector, the supervisor and, if you ask for it, the updater, or
  an internal mirror (`--release-base-url`, `--updater-url`; see [What the installers verify](#what-the-installers-verify)).

## 1. Create an enrollment token

An agent connects to `myriarchd` with an enrollment token (`myre_...`). Create one as an administrator in the UI under
*Settings*, tab *Tokens*, section *Enrollment tokens*, button *Create token*. Give it a name, optional **labels** and an
optional expiry date. The secret is shown **once**. Labels are *server labels*: they are copied to every agent that enrolls
with the token, and a host cannot claim them for itself. Use them for decisions that matter, such as `update-via=awx`, and the
self-reported `site.*` attributes only for coarse selection of fleets.

The dialog that shows the secret also shows an install example. Treat it as a reminder of the options and take the complete
commands from this page: in v0.1.0 the example leaves out the collector version and the Sigstore verifier, so the installer
stops with `option_missing`, and the Windows example starts the script directly, which the default execution policy refuses.

Tokens can also be declared in the server configuration (`opamp.enrollmentTokens`, fed from a mounted secret). See
[Authentication](../operations/authentication.md) for that and for how people sign in.

Keep the token in a file that only root (or the administrators) can read and pass the file with `--token-file`
(`-TokenFile`). An argument shows up in the process list and in the shell history, and the script warns about it
(`token_on_command_line`). The commands below create the file without putting the token into the shell history, and delete it
after the run: the installer has copied the token to its own protected location.

## 2. Download and verify the installer

The installers are in the release archive of the updater: `myriarch-updater_<version>_linux_<arch>.tar.gz` or
`myriarch-updater_<version>_windows_<arch>.zip`. It holds the script (`deploy/scripts/install-linux.sh` or
`install-windows.ps1`), the templates and units that the script uses (`deploy/supervisor/`, `deploy/updater/` and
`deploy/systemd/`) and the `myriarch-updater` binary. **Run the script from the unpacked archive:** it reads its templates from
the directory above its own, and a script that you copied out of the archive stops with `template_missing`.

The script runs as root or Administrator, so verify the archive before you unpack it, as
[Verify releases](../security/verify-releases.md) describes: check `checksums.txt` against its Sigstore bundle with the
identity of the release, then check the archive against `checksums.txt`. Use the release that matches your server (*Settings*,
tab *System*, shows its version); the newest release is on the
[releases page](https://github.com/myriarch/myriarch/releases). Work in a folder that other users cannot write, so that nobody
can replace a file between the check and the run.

On Linux, with `cosign` 3.0.4 or newer on the machine where you check the files. If that is an admin host, copy
`checksums.txt` and the archive to the host afterwards and repeat the `sha256sum` check there:

```bash
VERSION=0.1.0        # the myriarch release, without the "v"
ARCH=amd64           # or arm64 (uname -m: x86_64 or aarch64)
BASE="https://github.com/myriarch/myriarch/releases/download/v${VERSION}"

install -d -m 0700 ~/myriarch-setup && cd ~/myriarch-setup
curl -fsSL -O "${BASE}/checksums.txt" -O "${BASE}/checksums.txt.sigstore.json" \
  -O "${BASE}/myriarch-updater_${VERSION}_linux_${ARCH}.tar.gz"

cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum --check --ignore-missing checksums.txt        # the archive must say OK
tar -xzf "myriarch-updater_${VERSION}_linux_${ARCH}.tar.gz"
```

On Windows, in an elevated Windows PowerShell. The setup folder and the folder of `cosign.exe` must be folders that only
administrators can write: a folder below `C:\Program Files` is one, while `C:\Temp`, a user profile and a download folder are
not. The commands expect `cosign.exe` 3.0.4 or newer at `C:\Program Files\cosign\cosign.exe` (see
[step 3](#3-choose-how-the-host-verifies-downloads)):

```powershell
$Version = '0.1.0'       # the myriarch release, without the "v"
$Arch    = 'amd64'
$Base    = "https://github.com/myriarch/myriarch/releases/download/v$Version"
$Zip     = "myriarch-updater_${Version}_windows_${Arch}.zip"
$Setup   = 'C:\Program Files\myriarch-setup'

New-Item -ItemType Directory -Path $Setup -Force | Out-Null
Set-Location $Setup
curl.exe -fsSL -O "$Base/checksums.txt" -O "$Base/checksums.txt.sigstore.json" -O "$Base/$Zip"

& 'C:\Program Files\cosign\cosign.exe' verify-blob --bundle checksums.txt.sigstore.json `
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v$Version" `
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt

$lines = @(Get-Content checksums.txt | Where-Object { $_.EndsWith("  $Zip", [StringComparison]::Ordinal) })
if ($lines.Count -ne 1) { throw "expected exactly one line for $Zip in checksums.txt" }
if ((Get-FileHash -Algorithm SHA256 $Zip).Hash.ToLowerInvariant() -cne $lines[0].Split(' ')[0]) { throw 'SHA-256 mismatch' }

Expand-Archive $Zip -DestinationPath "$Setup\package"
```

`curl.exe` ships with the Windows versions that have `tar.exe`. If a check fails, do not run anything from the archive; the
reasons are listed in [Verify releases](../security/verify-releases.md).

## 3. Choose how the host verifies downloads

The installer verifies the collector and the supervisor itself: the SHA-256 and the Sigstore bundle of every asset, before
anything runs. It needs a **Sigstore verifier** for that and stops with `sigstore_verifier_missing`, before it downloads the
collector, if it finds none. Choose one:

| Verifier | Use it when | Options |
|---|---|---|
| `cosign` 3.0.4 or newer | The host can have `cosign` | Linux: `cosign` in the `PATH` of root, or `--verifier /path/to/cosign`. `sudo` often resets the `PATH` (the `secure_path` setting), so pass the full path if `cosign` is in your home directory. The installer runs it as root, so keep it where only root can replace it, for example in `/usr/local/bin`. Windows: `-Verifier 'C:\Program Files\cosign\cosign.exe'`. The file must be in a folder that only administrators can write (`directory_unsafe` otherwise); a `cosign.exe` in the `PATH` counts only from such a folder |
| The updater, verified by its SHA-256 | The host has no `cosign` and gets the updater anyway | `--with-updater --updater-url <URL> --updater-sha256 <hex>` (Windows: `-WithUpdater -UpdaterUrl <URL> -UpdaterSha256 <hex>`). Take the hash of the updater from a `checksums.txt` that you verified on another machine. The installer downloads the updater, compares it with the hash and then uses it as the verifier of the collector and the supervisor |
| An updater that is installed already | You run the installer again on a host that has the updater | Nothing to pass: the installed `myriarch-updater` is found. On Linux it comes after a `cosign` in the `PATH`, on Windows before `cosign.exe` |

With `--with-updater` the installer needs a **trusted hash source** for the updater, because the updater runs as root or
SYSTEM: either `--updater-checksums-url` (`-UpdaterChecksumsUrl`), which downloads `checksums.txt` and its bundle and verifies
them with the verifier, so it needs the first or the third row; or `--updater-sha256` (`-UpdaterSha256`). Without a hash
source the installer stops with `updater_hash_source_missing`.

Without `--with-updater` the host gets no `myriarch-updater`, so the `updater` executor cannot update its collector, and the
host needs `cosign` as its verifier.

## 4. Install on Linux

Create the token file without putting the token into the shell history, then run the script from the unpacked archive. The
variables are those of [step 2](#2-download-and-verify-the-installer):

```bash
cd ~/myriarch-setup
(umask 077; cat > enrollment-token)        # paste the token, press Enter, then Ctrl-D

sudo ./deploy/scripts/install-linux.sh \
  --opamp-endpoint wss://fleet.example.com/v1/opamp \
  --token-file ./enrollment-token \
  --site-type edge --site-id edge-042 --site-name "Edge site 42" --site-country NL --site-timezone Europe/Amsterdam \
  --collector-version 0.162.0 \
  --verifier "$(command -v cosign)" \
  --with-updater \
  --updater-url "${BASE}/myriarch-updater_${VERSION}_linux_${ARCH}" \
  --updater-checksums-url "${BASE}/checksums.txt"

rm -f enrollment-token
```

`--updater-checksums-url` makes the installer verify `checksums.txt` and take the hash of the updater from it. A host without
`cosign` verifies `checksums.txt` on another machine and passes the hash instead, in place of `--verifier` and
`--updater-checksums-url`:

```bash
awk -v f="myriarch-updater_${VERSION}_linux_${ARCH}" '$2 == f { print $1 }' checksums.txt     # the hash, from the file you verified
```

```text
  --with-updater \
  --updater-url "${BASE}/myriarch-updater_${VERSION}_linux_${ARCH}" \
  --updater-sha256 <the hash>
```

Without `--with-updater` (and the options that belong to it) the host gets no updater.

Layout on the host: collector versions in `/opt/myriarch/collector/versions/<v>/` with a `current` symlink that the updater
switches atomically, the supervisor in `/opt/myriarch/supervisor/`, configuration in `/etc/myriarch/` (`supervisor.yaml`,
`fallback.yaml`, `myriarch.env` and `enrollment-token` with mode `0600`) and data in `/var/lib/otelcol`. Running the script
again brings the host to the state you give it: a new version switches `current` and restarts the service, and local
changes to `fallback.yaml` and further secrets in `myriarch.env` are kept.

Secrets that the collector needs (for example `OTEL_GATEWAY_TOKEN`, referenced as `${env:OTEL_GATEWAY_TOKEN}` in a
blueprint) go into `/etc/myriarch/myriarch.env`. Then restart with `systemctl restart otelcol-supervisor`.

## 5. Install on Windows

Continue in the elevated Windows PowerShell of step 2. Create the token file in the same folder, without putting the token
into the command history, and start the installer:

```powershell
Read-Host 'Enrollment token' | Set-Content -Path "$Setup\enrollment-token.txt" -NoNewline

Unblock-File "$Setup\package\deploy\scripts\install-windows.ps1"        # only after the checks of step 2 passed
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File "$Setup\package\deploy\scripts\install-windows.ps1" `
  -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile "$Setup\enrollment-token.txt" `
  -SiteType edge -SiteId edge-042 -SiteName "Edge site 42" -SiteCountry NL -SiteTimezone Europe/Amsterdam `
  -CollectorVersion 0.162.0 -WithUpdater `
  -UpdaterUrl "$Base/myriarch-updater_${Version}_windows_${Arch}.exe" `
  -UpdaterChecksumsUrl "$Base/checksums.txt" `
  -Verifier 'C:\Program Files\cosign\cosign.exe'

Remove-Item "$Setup\enrollment-token.txt"
```

**Why this start.** A Windows client runs scripts under the execution policy `Restricted`, which refuses every script, and
Windows Server under `RemoteSigned`, which refuses a script that still carries the Mark of the Web. The Windows files of
v0.1.0 are not Authenticode-signed. Do not change the policy of the machine to get past this:

- `-ExecutionPolicy RemoteSigned` on the command line sets the policy for this one PowerShell process (scope *Process*), and it
  is gone when the process ends. `RemoteSigned` runs a script that is local and unblocked and still refuses one that carries the
  Mark of the Web, so a file that was not unblocked stays blocked. `Unblock-File` removes the mark that a browser adds to a
  download; run it **only after** the checks of step 2 passed.
- `-NoProfile` keeps the profile scripts of the account out of the elevated process.
- Do not use `-ExecutionPolicy Bypass`, which turns the check off, and no `Set-ExecutionPolicy` for the machine or the user,
  which stays after the installation and weakens the host for everyone.
- A policy set by Group Policy wins over the parameter. `Get-ExecutionPolicy -List` shows it: a value other than `Undefined`
  for `MachinePolicy` or `UserPolicy` decides. If it forbids the script (`Restricted`, or `AllSigned`, which also refuses the
  unsigned files of v0.1.0), the installer cannot start; ask the owner of the policy for an exception.
- Under App Control for Business with script enforcement an unsigned script runs in Constrained Language Mode and
  `install-windows.ps1` fails. Allow it by its hash, after you verified the archive.

**The options.** The trust model is the same as on Linux: `-VerifySignatures`, `-Verifier`, `-Insecure`,
`-UpdaterChecksumsUrl` with `-UpdaterVersion` and `-UpdaterBundleUrl`, or `-UpdaterSha256` for a host without `cosign`. For
that, replace `-Verifier` and `-UpdaterChecksumsUrl` by `-UpdaterSha256 <hex>`, with the hash from the line of
`myriarch-updater_<version>_windows_<arch>.exe` in the `checksums.txt` that you verified. `cosign.exe` must be 3.0.4 or newer.
`-SigningKey` and `-UpdaterChecksumsSigUrl` were removed. In addition `-UpdaterSignerThumbprint` requires the Authenticode
signature of the updater to come from one certificate; a pinned thumbprint breaks whenever that certificate is renewed, so it
fits only a private PKI.

The supervisor runs as the virtual service account `NT SERVICE\otelcol-supervisor`, not as `LocalSystem`; only the
`myriarch-updater` service runs as SYSTEM. Services, paths and troubleshooting are in
[deploy/windows/README.md](../../deploy/windows/README.md). Collector secrets are set with `Set-ServiceEnvironment.ps1`.

Downloads are retried with the same 2-minute window as on Linux. The status of a proxy that refuses the connection decides
under PowerShell 7.5 or later; PowerShell 7.4 retries such a refusal like a failed connection.

## 6. Check the result

Within a few seconds the host shows up under *Agents*: one entry for the collector and, with `--with-updater`, one for the
updater. Open the collector and check three things:

- the **fleet**: if it says *unassigned*, no selector matches. Compare the attributes of the agent with the `selector` of your
  fleets (the page *Fleets* has a "Which fleet?" check);
- the **configuration state** `in_sync`: the host received and applied the rendered configuration of its fleet;
- the **health** and the effective configuration.

If the host does not appear, read the installer message first. Every error is one line `ERROR [<code>] <message>` with a
stable code (the catalog is at the top of `install-linux.sh`), and the table below lists the codes that a first installation
meets. Then check the service (`systemctl status otelcol-supervisor`, the Event Viewer on Windows) and the
[troubleshooting table](../operations/troubleshooting.md), for example `401` in the handshake or "the instance uid is bound
to another credential".

## When the installer stops

| Code or symptom | Cause | What to do |
|---|---|---|
| `option_missing` | A required option is absent, and the message names it: the site type, the site ID, the **collector version** (`--collector-version`, `-CollectorVersion`), the token (`--token-file`, `-TokenFile`) or, with `--with-updater`, `--updater-url`. A missing or malformed endpoint ends with `option_invalid`. | Add the option. The collector version is the input that is forgotten most often, because the install example of the token dialog of v0.1.0 leaves it out; see [What you need](#what-you-need). |
| `updater_hash_source_missing` | `--with-updater` without `--updater-checksums-url` or `--updater-sha256`. | Add one of them ([step 3](#3-choose-how-the-host-verifies-downloads)). |
| `sigstore_verifier_missing` | The installer found no Sigstore verifier: no `cosign` it may use, no installed updater and no updater verified by `--updater-sha256`. On Linux the `PATH` of `sudo` may not contain your `cosign`. On Windows a `cosign.exe` in a folder that unprivileged accounts can write is ignored, with a warning. | Choose a verifier as in [step 3](#3-choose-how-the-host-verifies-downloads): pass `--verifier` or `-Verifier` with the full path, or add the updater with its SHA-256. |
| `sigstore_verifier_outdated` | `cosign` is older than 3.0.4 (a pre-release of 3.0.4 counts as older). | Install `cosign` 3.0.4 or newer. |
| `directory_unsafe` (Windows) | The file of `-Verifier` is in a folder that unprivileged accounts can change, for example `C:\Tools`. | Copy `cosign.exe` into a folder that only administrators can write, for example `C:\Program Files\cosign`, and pass that path. |
| `template_missing` | The script runs outside the unpacked archive, so the directory `deploy` with the templates is not next to it. | Run the script from `deploy/scripts/` of the unpacked archive, or pass the `deploy` directory of the archive with `--templates-dir` (`-TemplatesDir`). |
| PowerShell refuses the script (no `[code]`): "running scripts is disabled on this system" or "is not digitally signed" | The execution policy forbids the script (`Restricted`), or the file still carries the Mark of the Web (`RemoteSigned`). | Start the installer as in [step 5](#5-install-on-windows) and run `Unblock-File` after the checks. If Group Policy sets the policy, see the notes of step 5. |
| `privilege_required` | The installer does not run as root or Administrator. | Use `sudo`, or an elevated Windows PowerShell. |
| `tool_missing` | A tool is absent. The message names it. | Install it ([What you need](#what-you-need)). |
| `download_failed` with HTTP 404 | The release does not exist. A 404 for `opampsupervisor_<version>_...` means that the upstream project published no supervisor for this collector version. | Check the version. Pass `--supervisor-version` (`-SupervisorVersion`) if the supervisor has another version than the collector. |
| `sigstore_verification_failed` or `checksum_mismatch` | The file is not the one that was signed: modified, from another release, or a bundle that does not belong to it. | Do not retry with `--insecure`. Download the file again from the release and check the version; a mirror must carry `checksums.txt` and the bundles unchanged. |
| `Get-Service` lists `opampsupervisor` or `otelcol-contrib`, and `otelcol-supervisor` is missing, or `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe` does not exist | A collector or supervisor was installed from an MSI of the upstream project. The supervisor MSI installs a service `opampsupervisor` with manual start in its own folder, and the contrib MSI installs `otelcol-contrib`, which starts at once. myriarch manages neither: the installer creates the service `otelcol-supervisor` with the supervisor in `C:\Program Files\Myriarch\supervisor\`. The installer of v0.1.0 does not look for the upstream services and does not remove them. | If `otelcol-supervisor` is missing, the installer stopped earlier: read its last `ERROR [<code>]` line and use this table. Remove the upstream MSI (*Settings*, *Apps*, or `msiexec /x`), then run the installer again. |
| `systemctl status otelcol-contrib` shows a running service (Linux) | The contrib deb or rpm package of the upstream project started a collector of its own, next to the one that myriarch manages. | Remove the package with your package manager. The installer of v0.1.0 does not notice it. |
| `service_not_running` | The supervisor service stopped after the start. A warning on Linux, an error on Windows. | Read the log: `journalctl -u otelcol-supervisor -n 50`, or the Event Viewer (Windows Logs, Application) and `C:\ProgramData\Myriarch\supervisor\agent.log`. |

## What the installers verify

The details behind the options of the steps above:

- **Downloads use https only**, also after redirects. `--insecure` is for labs and tests: it allows `http://`, `file://` and
  `ws://` to the server and skipping the Sigstore check.
- **Downloads are retried after a transient failure.** A timeout, a failed connection or HTTP 408, 429, 500, 502, 503 or 504
  is retried with pauses of 1, 2, 4, ... seconds, and no attempt starts later than 2 minutes after the first one, so a short
  outage of github.com does not stop the installation. Behind an `HTTPS_PROXY` the status of the proxy decides when it refuses
  the connection (503 is retried, 407 is not). Any other error, for example a 404, stops it at once. Both end with
  `download_failed`, and the message names the number of attempts. The checks below apply to every attempt unchanged.
- **Collector and supervisor** are verified with their **SHA-256** file (`<asset>.sha256`) **and** the **Sigstore bundle**
  (`<asset>.sigstore.json`) before anything runs (`--verify-signatures require`, the default). The verifier is `cosign`
  3.0.4 or newer (an older one stops the script with `sigstore_verifier_outdated`), or `myriarch-updater verify-bundle`
  (`--verifier FILE`, or an updater that is already installed). Without a verifier the installation stops.
  `--trusted-root FILE` enables offline verification.
- **The updater** is the project's own binary and runs as root, so it needs a trustworthy hash source: either
  `--updater-checksums-url <URL>` or `--updater-sha256 <hex>` (both together are allowed, then both must match).
  - With `--updater-checksums-url` the script downloads `checksums.txt` and its keyless bundle (`<URL>.sigstore.json`, or
    `--updater-bundle-url`), verifies the bundle against the release identity of the version, and takes the hash from the line
    with the file name of `--updater-url`. The identity is
    `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v<version>`, so a genuine but older release,
    another workflow or another repository is refused. The version is `--updater-version`, or the one in the file name
    `myriarch-updater_<version>_linux_<arch>` of `--updater-url`; if both are given they must agree. The verifier of
    `checksums.txt` is the file given with `--verifier`, otherwise `cosign` 3.0.4 or newer if it is in `PATH`, otherwise the updater
    that is already installed on the host; it is never the updater that is being installed.
  - **Hosts without a verifier** (no `cosign`, no installed updater) verify `checksums.txt` on an admin host, as described in
    [Verify releases](../security/verify-releases.md), and pass the hash of the updater as `--updater-sha256`. The installer
    then checks the downloaded updater against that hash and also uses it to verify the collector and supervisor bundles.
  - **Upgrades:** run the script again with the new version and URLs. On Linux a `cosign` 3.0.4 or newer in `PATH` verifies the
    bundle of the new release, and the updater that is already installed does so on a host without `cosign` (on Windows the
    installed updater comes before `cosign.exe`); then the new updater replaces it.
  - **Removed options:** `--signing-key` and `--updater-checksums-sig-url` (the key-signed `checksums.txt.sig` of earlier
    builds) fail with the code `option_removed`; the message names the replacement.
- `--release-base-url` points the collector and supervisor downloads to an internal mirror with the layout
  `<base>/<tag>/<asset>`. `--ca-file` is the CA for the connection to `myriarchd`; a mirror with its own CA must be trusted
  by the certificate store of the host.

## Golden images and cloned machines

The identity of an agent is its instance UID: `persistent_state.yaml` in the supervisor storage (`/var/lib/otelcol/supervisor`
on Linux, `C:\ProgramData\Myriarch\supervisor` on Windows) and `instance_uid` in the updater state. The scripts create
these directories **empty**. Empty both before you create an image or clone a machine; otherwise all clones report the same
identity and displace each other.

## Remove a host

`sudo ./deploy/scripts/install-linux.sh --uninstall` removes the services and files and keeps the data in `/var/lib/otelcol`;
add `--purge` to delete that as well, including the identity. On Windows use `-Uninstall` and `-Purge`, started as in
[step 5](#5-install-on-windows). Then delete the agent in the UI once it is offline.

Next: the host is now part of a fleet, so you can [roll out a configuration change](roll-out-a-config-change.md) or a
[collector version](roll-out-a-collector-version.md) to it. Details of the supervisor configuration are in
[upstream-notes.md](../design/upstream-notes.md) and the templates in [deploy/supervisor/](../../deploy/supervisor/).
