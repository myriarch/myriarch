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
- Kubernetes: the collector runs as a container; see [Installing collectors with Helm](../operations/install-collector-helm.md).

The scripts deliberately do not use the packages of the upstream project (deb, rpm and MSI): the contrib packages start a
collector service of their own, and a supervisor from a package is another service in another place than the one that
myriarch manages.

There are two ways to install a host:

- **One command** (*from v0.2.0*, Linux and Windows): the server renders a command, you paste it into a root shell on the
  host (an elevated Windows PowerShell on Windows), and the host comes up healthy in the right fleet. The server tells the
  installer which version its fleet targets, and the installer verifies everything it runs. [This is the way to
  start](#one-command-install-a-linux-host-from-the-server), and [this is the Windows version](#one-command-install-a-windows-host-from-the-server).
- **The manual, verified path**: you collect the inputs, download and verify the installer yourself and run it with explicit
  options. It stays supported, for sites that cannot accept the trust of the first command (see below), for hosts without access
  to the server and for the installers of v0.1.0. The rest of this page describes it.

The manual path describes the installers of the release v0.1.0; what the installers of v0.2.0 do differently is marked *from
v0.2.0* (they check the host before they change anything, see [When the installer stops](#when-the-installer-stops), and on Linux
the collector version has a default; on Windows the collector version stays required without the server). Steps 1 to 3 are the same on both systems: [create an enrollment token](#1-create-an-enrollment-token), [download and verify the
installer](#2-download-and-verify-the-installer) and
[choose how the host verifies downloads](#3-choose-how-the-host-verifies-downloads). Step 4 [installs on
Linux](#4-install-on-linux), step 5 [installs on Windows](#5-install-on-windows), and step 6 [checks the
result](#6-check-the-result).

## One command: install a Linux host from the server

*Available from v0.2.0 (Linux, amd64 and arm64). The Windows command follows.* A fresh Linux host gets the collector, the
supervisor and, when the fleet uses the updater, the `myriarch-updater` with one command. The server knows what the fleet
targets, so you name neither a version nor an endpoint.

**What you need.** A server of the project's release with `install.enabled` on (the default), a host with systemd, root, `curl`,
`tar` with `gzip`, `sha256sum` and https access to the server and to the Sigstore TUF repository (`tuf-repo-cdn.sigstore.dev`), and a person who may create an enrollment token (`enrollment:manage`). The host reaches
the server only; it downloads nothing from GitHub.

A host that cannot reach the TUF repository passes a `trusted_root.json` instead. Create it on a machine that can reach the
repository with `myriarch-updater fetch-trusted-root --out trusted_root.json`, transfer it by a channel you trust and put it
where only root can write, for example below `/root`; the option takes an absolute path. Do not name
`/etc/myriarch/trusted_root.json`: the installer copies the file there. The file replaces TUF as the trust anchor of the
Sigstore check, so whoever can change it before or during the run decides which signatures count. Append
`'--trusted-root' '<absolute path of the file>'` to the options of the command: on the same line, before
`<<'MYRIARCH_TOKEN_END'`, or at the end of the command in the variant token from a file. Without it, the host verifies
through TUF and stops with `sigstore_verification_failed` when it cannot reach the repository. A fleet with the updater keeps
using the copy in `/etc/myriarch/trusted_root.json` for every later update, so renew the file when Sigstore rotates its keys
([Verify releases](../security/verify-releases.md#offline-and-behind-a-proxy)).

1. **Create an enrollment token** as in [step 1](#1-create-an-enrollment-token). Give it the labels that your fleets select on, if any.
2. **Get the command from the server.** It is rendered by `POST /api/v1/install/commands`
   ([reference](../reference/api/install.md#render-the-install-commands-for-hosts-a-command-for-each-platform)); the *Add host*
   dialog of the web UI shows the same text. The site options become the attributes of the host, and `site.type` and `site.id`
   are required:

   ```bash
   curl -sS -X POST https://fleet.example.com/api/v1/install/commands \
     -H "Authorization: Bearer $MYRIARCH_API_TOKEN" -H 'Content-Type: application/json' \
     -d '{"platforms":["linux/amd64"],"fleet":"linux-hosts","attributes":{"site.type":"edge","site.id":"042"}}' \
     | jq -r '.commands[0].command'
   ```

   The command has the placeholder `@@MYRIARCH_ENROLLMENT_TOKEN@@` where the token goes; the server never sees a token. Replace
   the placeholder with the token of step 1 (the dialog does this in the browser), or use the second variant below.
3. **Paste the command into a shell on the host** (not as root: the command starts the privileged shell itself with `sudo`, and
   asks for nothing else). It does this, and stops at the first failure:
   - it writes the token into a private file below `/root` (mode 0600) and downloads the installer bundle from the server, with the
     token only as a request header;
   - it checks the SHA-256 that the command pins, **before** it unpacks anything, and unpacks the bundle in that private directory;
   - the installer in the bundle checks the host (a collector or supervisor from a deb or rpm package of the upstream project stops
     it with `conflicting_install`, and nothing is removed), asks the server for a plan, and reads the plan as data;
   - it downloads the collector and the supervisor from the server with short-lived install grants and verifies each against its
     SHA-256 and its Sigstore bundle, with an identity, issuer and trust root that the installer builds itself. The verifier is the
     `myriarch-updater` of the bundle, so the host needs no `cosign`. Nothing runs before its check;
   - it installs the units and the configuration, starts the services and deletes the private directory with the token file.
4. **Check the result** as in [step 6](#6-check-the-result): the host appears under *Agents* in the fleet that the server chose, at
   the target version of that fleet, with the configuration `in_sync`.

The first host of a platform makes the server download about 112 MB, so the installer can wait for minutes ("the server is
preparing the artifacts"); it asks again after every `Retry-After` and gives up after 15 minutes (`--plan-wait`).

**Which version?** The effective target of the fleet, as [Fleets](../concepts/fleets.md#the-effective-target) describes it. While a
version rollout of the fleet is *Pending*, *Running* or *Waiting for approval*, the host gets the version the fleet ran before, and
the rollout adds it to its last wave. A fleet on hold, a paused or failed rollout and a host that no fleet selects are refused with
a reason that the installer shows (`plan_rejected`). With `"collectorVersion"` in the request of step 2 you can ask for a version
inside the frame that Git sets.

**The token and the shell history.** The default command contains the token, and text that you paste is recorded by what records
typed text: the clipboard, the shell history, the history file of PSReadLine, PowerShell transcription, PowerShell script block
logging (event 4104) and the recording of a terminal session. Nothing that the command and the installer do puts the token into an
argument list, a URL, a log or an error message, but these records are outside their control, and a token that a record kept counts
as leaked: revoke it. On a host that records its sessions, in production and in automation use the **variant "token from a file"**:
put the token into a file that only root can read (`install -m 0600 /dev/null /etc/myriarch/enrollment-token`, then write it from a
machine you trust) and ask for `"variant":"token_file"` with `"tokenFiles":{"linux":"/etc/myriarch/enrollment-token"}`. That
command contains no token and reads it from the file. A token is a separate token per channel or region
([ADR-0012](../adr/0012-agent-access-via-enrollment-tokens.md)), and an existing token cannot be shown again, because the server keeps
only its hash.

**What you trust.** The first command has nothing on the host to verify the bundle with, so it pins the SHA-256 of the bundle, which
the server shows after it verified the `checksums.txt` of its own release keyless. The first installation therefore trusts the server
that displayed the command (trust on first use). After that, collector and supervisor carry the upstream signature and are checked
on the host against an identity that the server does not control: a wrong server can at most choose another genuine upstream version,
an older one included. A site that cannot accept this uses the manual path below; the pinned hash is the line of the archive in the
signed `checksums.txt`, so you can compare it with the release before the first run
([ADR-0046](../adr/0046-host-installation-from-the-server.md), [Security model](../security/model.md)).

**When it stops.** The last line is `ERROR [<code>] <message>`; the message names the next step. The codes of this way of installing
(`plan_failed`, `plan_invalid`, `plan_rejected`, `plan_unavailable`, `https_required`, `conflicting_install`, `supervisor_missing`) are in
[Installing a host from the server](../operations/troubleshooting.md#installing-a-host-from-the-server). Without
`--server` (for example when you run `install-linux.sh` from a verified archive yourself) the script installs the version that this
release was tested with when you leave `--collector-version` out, and `--collector-version latest` asks GitHub for the newest
release; the manual path below uses explicit versions.

## One command: install a Windows host from the server

*Available from v0.2.0 (Windows on amd64; the upstream project publishes the supervisor for amd64 only).* A fresh Windows host gets
the collector, the supervisor and, when the fleet uses the updater, the `myriarch-updater` with one command, and neither the
execution policy of the machine nor a missing verifier stops it. The server knows what the fleet targets, so you name neither a
version nor an endpoint. Everything that applies to the Linux command applies here: the server renders the command, the token never
reaches the server, the plan is read as data, and the installer verifies what it runs.

**What you need.** A server of the project's release with `install.enabled` on (the default); a Windows 10 (1803 or newer) or Windows
Server 2019 or newer host with `tar.exe`, Windows PowerShell 5.1 or PowerShell 7 and https access to the server and to the Sigstore
TUF repository (or a local `trusted_root.json`, see [What you need](#what-you-need)); an elevated session (*Run as administrator*) or
SYSTEM; the certificate of the server in the certificate store of the machine (the command has no option for a CA file, so import the
CA of a private server first with `Import-Certificate -CertStoreLocation Cert:\LocalMachine\Root`; the CA is then trusted by the whole machine, so use a CA that you would trust for every program on it); and a person who may create an enrollment token (`enrollment:manage`).

1. **Create an enrollment token** as in [step 1](#1-create-an-enrollment-token).
2. **Get the command from the server**, as on Linux with the platform `windows/amd64`
   ([reference](../reference/api/install.md#render-the-install-commands-for-hosts-a-command-for-each-platform)); the *Add host* dialog
   of the web UI shows the same text:

   ```bash
   curl -sS -X POST https://fleet.example.com/api/v1/install/commands \
     -H "Authorization: Bearer $MYRIARCH_API_TOKEN" -H 'Content-Type: application/json' \
     -d '{"platforms":["windows/amd64"],"fleet":"windows-hosts","attributes":{"site.type":"edge","site.id":"042"}}' \
     | jq -r '.commands[0].command'
   ```

   The command has the placeholder `@@MYRIARCH_ENROLLMENT_TOKEN@@` where the token goes; replace it with the token of step 1 (the
   dialog does this in the browser), or use the variant "token from a file" below.
3. **Paste the command into an elevated Windows PowerShell** on the host. It is a script block that uses cmdlets and native programs
   only, so it also runs where App Control puts a typed command into Constrained Language Mode. It stops at the first failure:
   - it checks that the session is elevated or SYSTEM (`privilege_required`) and that Group Policy does not set the execution policy
     to `AllSigned` or `Restricted` (`execution_policy_blocked`; see below);
   - it makes a working directory below `%ProgramFiles%`, whose folder admits no entry from a standard user, and restricts its DACL
     to SYSTEM and the administrators **before** the token or the first byte of the bundle is written into it (`acl_failed`);
   - it writes the token into a private file there, downloads the installer bundle (the zip of the updater release with the script,
     its templates and `myriarch-updater.exe`) from the server with the token only as a request header, and refuses a redirect;
   - it checks the SHA-256 that the command pins with `Get-FileHash` **before** it unpacks anything (`checksum_mismatch`), unpacks
     the zip, runs `Unblock-File` on the verified script and starts it as
     `powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File install-windows.ps1 -Server <server> -TokenFile <file> ...`;
   - the installer checks the language mode first (`language_mode_unsupported`), then the host (an MSI of the upstream project stops
     it with `conflicting_install`, and nothing is removed), asks the server for a plan, reads the plan as data, downloads collector
     and supervisor from the server with short-lived install grants and verifies each against its SHA-256 and its Sigstore bundle
     with an identity, issuer and trust root that it builds itself. The verifier is the `myriarch-updater.exe` of the bundle, so the
     host needs no `cosign.exe`. Nothing runs before its check. The supervisor goes to
     `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe`, the path of the service, and the installer stops with
     `supervisor_missing` if the file is not there afterwards;
   - it creates the service `otelcol-supervisor` under its virtual account, starts it, and the command deletes the working
     directory with the token file.
4. **Check the result** as in [step 6](#6-check-the-result).

**The execution policy.** The command sets the policy for the one new PowerShell process only (`-ExecutionPolicy RemoteSigned`),
after the hash check and `Unblock-File`. It never uses `Bypass` and never changes the policy of the machine or of the user, and the
session of the administrator keeps its own. A policy from Group Policy (the scopes `MachinePolicy` and `UserPolicy`) takes precedence
over the parameter. The command does not work around it: it stops with `execution_policy_blocked` before it downloads anything and
says why. Ask the owner of the policy for an exception, or use a host that the policy does not cover.

**App Control and `AllSigned`.** The Windows files of the release are not Authenticode-signed
([ADR-0034](../adr/0034-authenticode-signing-of-windows-release-artifacts.md)). Under `AllSigned` they cannot run. Under App Control for
Business with script enforcement an unsigned script runs in Constrained Language Mode, and the installer stops with
`language_mode_unsupported` before it changes anything. Allow the files by a hash rule for each release: create the rule from the
files of the bundle that the command pinned (`install-windows.ps1`, `myriarch-updater.exe` and the other binaries that you allow),
with the App Control tools, because the hash that App Control uses for a script is not the SHA-256 of `checksums.txt`. The server
serves the bytes of the release unchanged, so the rule stays valid. Once the files are signed, a publisher rule is enough. Details:
[Windows: Language mode and App Control](../../deploy/windows/README.md#language-mode-and-app-control).

**The token, PSReadLine, transcription and script block logging.** The default command contains the token, and text that you paste is
recorded by what records typed text: the clipboard, the history file of PSReadLine (Windows PowerShell 5.1 keeps it in
`%APPDATA%\Microsoft\Windows\PowerShell\PSReadLine\ConsoleHost_history.txt`), PowerShell transcription, PowerShell script block
logging (event 4104 in `Microsoft-Windows-PowerShell/Operational`, often forwarded to a log collector) and the recording of a terminal
session. Nothing that the command and the installer do puts the token into an argument list, a URL, a log or an error message, but
these records are outside their control, and a token that a record kept counts as leaked: revoke it. On a host that records its
input, in production and in automation use the **variant "token from a file"**. Put the token into a file that only SYSTEM and the
administrators can read, from a machine you trust:

```powershell
New-Item -ItemType Directory -Path 'C:\Program Files\myriarch-setup' -Force | Out-Null
icacls.exe 'C:\Program Files\myriarch-setup' /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F'
if ($LASTEXITCODE -ne 0) { throw 'icacls could not restrict the setup folder' }
# then write the token into C:\Program Files\myriarch-setup\enrollment-token.txt
```

and ask the server for `"variant":"token_file"` with `"tokenFiles":{"windows":"C:\\Program Files\\myriarch-setup\\enrollment-token.txt"}`.
That command contains no token and reads it from the file; delete the file after the run, which the command does not do for a file
that it did not create. A token is a separate token per channel or region
([ADR-0012](../adr/0012-agent-access-via-enrollment-tokens.md)).

**When it stops.** The last line is `ERROR [<code>] <message>`, and the message names the next step. The codes of the command itself
are `privilege_required`, `execution_policy_blocked`, `file_unreadable`, `option_missing`, `option_invalid`, `acl_failed`,
`download_failed`, `checksum_mismatch` and `installer_failed`; the installer adds `language_mode_unsupported`, `conflicting_install`,
`supervisor_missing` and the codes of the server mode (`plan_failed`, `plan_invalid`, `plan_rejected`, `plan_unavailable`,
`https_required`). They are in [Installing a host from the server](../operations/troubleshooting.md#installing-a-host-from-the-server)
and in [When the installer stops](#when-the-installer-stops). The manual path below, step 5, stays the way out for a host that cannot
use the command; the installer there also takes `-Server` when you run it from a verified archive yourself.

## The manual path

The rest of this page is the manual, verified path.

## What you need

| Input | Linux option | Windows parameter | Where it comes from |
|---|---|---|---|
| OpAMP endpoint | `--opamp-endpoint` | `-OpampEndpoint` | `wss://<server>/v1/opamp`, the address under which the host reaches `myriarchd`. *Settings*, tab *System*, shows it as "OpAMP endpoint". A plain `ws://` or `http://` endpoint needs `--insecure` and is for labs only |
| Enrollment token | `--token-file` | `-TokenFile` | [Step 1](#1-create-an-enrollment-token). `--token` and `-Token` exist too, but an argument shows up in the process list |
| Site type and ID | `--site-type`, `--site-id` | `-SiteType`, `-SiteId` | You choose them; the selectors of your fleets match on them (below) |
| **Collector version** | `--collector-version` | `-CollectorVersion` | Without `v`: the version of `otelcol-contrib` to install, for example `0.162.0` (below). **Required** on Windows; `install-linux.sh` of v0.2.0 installs the version its release was tested with when you leave it out, and `latest` asks GitHub for the newest |
| Installer release | none | none | The myriarch release whose archive you unpack, for example `0.1.0` ([step 2](#2-download-and-verify-the-installer)) |
| Sigstore verifier | `--verifier` (or `--with-updater` with `--updater-sha256`) | `-Verifier` (or `-WithUpdater` with `-UpdaterSha256`) | [Step 3](#3-choose-how-the-host-verifies-downloads) |
| Updater (optional) | `--with-updater`, `--updater-url` and a hash source | `-WithUpdater`, `-UpdaterUrl` and a hash source | [Step 3](#3-choose-how-the-host-verifies-downloads) and the commands of step 4 or 5 |
| Sigstore trusted root (optional) | `--trusted-root` | `-TrustedRoot` | Only for a host that cannot reach the Sigstore TUF repository (below): a `trusted_root.json` that you fetched on a connected machine |

**Which collector version?** On the manual path the installer does not ask the server, so you name the version (on Linux, from v0.2.0, leaving it out installs the tested default, which is rarely the target of your fleet). Install the **target version
of the fleet** that the host should join: the page *Fleets* shows it (column *Target version*). It comes from
`spec.collector.version` in the fleet file of your GitOps repository, unless an operator chose another version in the UI
within the frame that Git sets ([Fleets](../concepts/fleets.md#the-effective-target)). Two cases need another answer:

- The column shows "–" for a fleet without a target version, and "–" with the badge *on hold* for a fleet on hold. No version
  rollout will change the version of its hosts, so install the version that the other hosts of the fleet run (page *Agents*,
  filter *Fleet*, column *Version*), or, for the first host, the version that you want it to keep.
- While a version rollout of the fleet is *Pending*, *Running* or *Waiting for approval*, the column already shows the new
  version, and a host that you install with it skips the waves and approvals of the rollout. Install the version that the fleet
  ran before the rollout instead (the page *Rollouts* shows it as `<from> → <to>`): the rollout then adds the host to its last
  wave ([Rollouts](../concepts/rollouts.md#when-do-rollouts-arise)).

If you install a version other than the target, the host runs it until a version rollout of its fleet changes it: the planner
counts a member whose version differs from the target as out of date
([Rollouts](../concepts/rollouts.md#when-do-rollouts-arise)). The installer downloads `otelcol-contrib` and the supervisor of
that version from the upstream releases (a 404 means that no such release exists).

The site options become attributes of the host (`site.type`, `site.id`, `site.name`, `site.country`, `site.region`,
`site.timezone`, and `deployment.environment.name` from `--environment`, default `prod`). The selectors of your fleets match
on them, so choose them to fit your repository: the example fleets select `site.type` `edge` or `datacenter` for hosts and
`cloud` for Kubernetes gateways.

**The host** needs:

- Linux: systemd, root (the steps below use a root shell, `sudo -i`), amd64 or arm64, and the tools `systemctl`, `useradd` or
  `adduser`, `curl`, `tar` together with `gzip` (GNU `tar` runs `gzip` to unpack the `.tar.gz` archives), `install`, `cmp` and
  `sha256sum` or `shasum`. A missing tool stops the script with the error code `tool_missing` and the name of the tool.
- Windows: an elevated Windows PowerShell 5.1 or newer, and `tar.exe` (Windows 10 1803 or Windows Server 2019 and newer). The
  upstream supervisor exists for amd64 only; on Windows ARM64 pass `-Arch amd64`.
- https access to `github.com` and its release hosts for the collector, the supervisor and, if you ask for it, the updater, or
  an internal mirror (`--release-base-url`, `--updater-url`; see [What the installers verify](#what-the-installers-verify)).
- https access to the Sigstore TUF repository, `tuf-repo-cdn.sigstore.dev`. Unless you give a trusted root file, `cosign` and
  `myriarch-updater` fetch the Sigstore trusted root from it, in the checks of step 2 and in the installer. A host without that
  access passes a `trusted_root.json` (`--trusted-root`, `-TrustedRoot`) that you fetched on a connected machine with
  `myriarch-updater fetch-trusted-root --out trusted_root.json`
  ([Verify releases](../security/verify-releases.md#offline-and-behind-a-proxy)). A host with neither the access nor the file
  stops with `sigstore_verification_failed`.

## 1. Create an enrollment token

An agent connects to `myriarchd` with an enrollment token (`myre_...`). Create one as an administrator in the UI under
*Settings*, tab *Tokens*, section *Enrollment tokens*, button *Create token*. Give it a name, optional **labels** and an
optional expiry date. The secret is shown **once**. Labels are *server labels*: they are copied to every agent that enrolls
with the token, and a host cannot claim them for itself. Use them for decisions that matter, such as `update-via=awx`, and the
self-reported `site.*` attributes only for coarse selection of fleets.

The dialog that shows the secret also shows an install example. Treat it as a reminder of the options and take the complete
commands from this page: in v0.1.0 the example leaves out the collector version and the Sigstore verifier, so the installer
stops with `option_missing`, and the Windows example starts the script directly, which the default execution policy refuses.
From v0.2.0 the command of the server replaces the example on Linux ([One command](#one-command-install-a-linux-host-from-the-server)).

Tokens can also be declared in the server configuration (`opamp.enrollmentTokens`, fed from a mounted secret). See
[Authentication](../operations/authentication.md) for that and for how people sign in.

Keep the token in a file that only root (or the administrators) can read and pass the file with `--token-file`
(`-TokenFile`). An argument shows up in the process list and in the shell history, and the script warns about it
(`token_on_command_line`). The commands below create the file from a prompt, so the token is not part of a command line that the
shell history, the history file of PSReadLine or PowerShell script block logging (event 4104) records, and they delete the file
after the run: the installer has copied the token to its own protected location. PowerShell transcription and the recording of a
terminal session still capture what you type at the prompt. On a host that records its sessions, copy the token file from a
machine you trust instead of typing the token.

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
[releases page](https://github.com/myriarch/myriarch/releases).

**Work in a folder that only root (on Windows: SYSTEM and the administrators) can read and write.** An account that can write
the folder can replace a checked file before root or the administrators run it, and the token file of step 4 or 5 lies in the
same folder. The commands below create such a folder: a directory below `/root` on Linux, and on Windows a folder with a DACL of
its own. Paste each block as a whole. The checks are chained, so that the first failure ends the block and nothing is unpacked.
On a host that cannot reach the Sigstore TUF repository, add `--trusted-root trusted_root.json` to the `cosign` command
([What you need](#what-you-need)).

**On Linux**, in a root shell (`sudo -i`). `cosign` 3.0.4 or newer has to be a file that only root can replace, such as
`/usr/local/bin/cosign`; a `cosign` in a home directory is not:

```bash
VERSION=0.1.0                  # the myriarch release, without the "v"
ARCH=amd64                     # or arm64 (uname -m: x86_64 or aarch64)
BASE="https://github.com/myriarch/myriarch/releases/download/v${VERSION}"
SETUP=/root/myriarch-setup
COSIGN=/usr/local/bin/cosign

install -d -m 0700 "$SETUP" && cd "$SETUP" &&
curl -fsSL -O "${BASE}/checksums.txt" -O "${BASE}/checksums.txt.sigstore.json" \
  -O "${BASE}/myriarch-updater_${VERSION}_linux_${ARCH}.tar.gz" &&
"$COSIGN" verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
sha256sum --check --ignore-missing checksums.txt &&
tar -xzf "myriarch-updater_${VERSION}_linux_${ARCH}.tar.gz"
```

A host without `cosign` gets its files from an admin host. There, download the three files (the `curl` part of the block above)
and run the `cosign` and `sha256sum` commands (`shasum -a 256` on macOS). Then create `$SETUP` on the host
(`install -d -m 0700 "$SETUP"`), copy `checksums.txt` and the archive into it, and run the `sha256sum` and `tar` commands of the
block above on the host.

**On Windows**, in an elevated Windows PowerShell. The first block creates the setup folder. A new folder below
`C:\Program Files` inherits read access for the group Users, and `C:\Temp`, a user profile and a download folder can be written
by unprivileged accounts. So the block replaces the DACL of the folder, which leaves SYSTEM (`S-1-5-18`) and the administrators
(`S-1-5-32-544`), and it stops if that fails. `icacls.exe $Setup` should list those two and no one else. The blocks after it end at
the first failure too: a `throw` ends the whole block, so a failed check is never followed by the unpack, not even in a console
that runs a pasted text line by line:

```powershell
$Version = '0.1.0'       # the myriarch release, without the "v"
$Arch    = 'amd64'
$Base    = "https://github.com/myriarch/myriarch/releases/download/v$Version"
$Zip     = "myriarch-updater_${Version}_windows_${Arch}.zip"
$Setup   = 'C:\Program Files\myriarch-setup'
$Cosign  = 'C:\Program Files\cosign\cosign.exe'

& {
  New-Item -ItemType Directory -Path $Setup -Force -ErrorAction Stop | Out-Null
  icacls.exe $Setup /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F'
  if ($LASTEXITCODE -ne 0) { throw "icacls could not restrict $Setup" }
}
```

With `cosign.exe` 3.0.4 or newer on the host, in a folder that only administrators can write (a folder below
`C:\Program Files` is one, see [step 3](#3-choose-how-the-host-verifies-downloads)), download, check and unpack in one block:

```powershell
& {
  Set-Location $Setup -ErrorAction Stop
  curl.exe -fsSL -O "$Base/checksums.txt" -O "$Base/checksums.txt.sigstore.json" -O "$Base/$Zip"
  if ($LASTEXITCODE -ne 0) { throw 'download failed' }
  & $Cosign verify-blob --bundle checksums.txt.sigstore.json `
    --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v$Version" `
    --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
  if ($LASTEXITCODE -ne 0) { throw 'signature check of checksums.txt failed' }
  $lines = @(Get-Content checksums.txt -ErrorAction Stop | Where-Object { $_.EndsWith("  $Zip", [StringComparison]::Ordinal) })
  if ($lines.Count -ne 1) { throw "expected exactly one line for $Zip in checksums.txt" }
  $hash = (Get-FileHash -Algorithm SHA256 $Zip -ErrorAction Stop).Hash.ToLowerInvariant()
  if ($hash -cne $lines[0].Split(' ')[0]) { throw 'SHA-256 mismatch' }
  Expand-Archive $Zip -DestinationPath "$Setup\package"
}
```

A host without `cosign.exe` gets its files from an admin host. There, run the `cosign verify-blob` command of
[Verify releases](../security/verify-releases.md) on `checksums.txt` and download the zip. Copy `checksums.txt` and the zip into
`$Setup` on the host, after the first block has created it, and run only the hash check and the unpack on the host:

```powershell
& {
  Set-Location $Setup -ErrorAction Stop
  $lines = @(Get-Content checksums.txt -ErrorAction Stop | Where-Object { $_.EndsWith("  $Zip", [StringComparison]::Ordinal) })
  if ($lines.Count -ne 1) { throw "expected exactly one line for $Zip in checksums.txt" }
  $hash = (Get-FileHash -Algorithm SHA256 $Zip -ErrorAction Stop).Hash.ToLowerInvariant()
  if ($hash -cne $lines[0].Split(' ')[0]) { throw 'SHA-256 mismatch' }
  Expand-Archive $Zip -DestinationPath "$Setup\package"
}
```

`curl.exe` ships with the Windows versions that have `tar.exe`. If a check fails, do not run anything from the archive; the
reasons are listed in [Verify releases](../security/verify-releases.md).

## 3. Choose how the host verifies downloads

The installer verifies the collector and the supervisor itself: the SHA-256 and the Sigstore bundle of every asset, before
anything runs. It needs a **Sigstore verifier** for that and stops with `sigstore_verifier_missing`, before it downloads the
collector, if it finds none. Choose one:

| Verifier | Use it when | Options |
|---|---|---|
| `cosign` 3.0.4 or newer | The host can have `cosign` | Linux: `--verifier /usr/local/bin/cosign`. The installer runs it as root, so it has to be a file that only root can replace: `/usr/local/bin` is such a place, a home directory or any directory that a user can write is not. Without `--verifier` the installer takes the first `cosign` in the `PATH` of root. Windows: `-Verifier 'C:\Program Files\cosign\cosign.exe'`. The file must be in a folder that only administrators can write (`directory_unsafe` otherwise); a `cosign.exe` in the `PATH` counts only from such a folder |
| The updater of the verified archive | The host has no `cosign` | Linux: `--verifier "$SETUP/myriarch-updater"`. Windows: `-Verifier "$Setup\package\myriarch-updater.exe"`. This is the `myriarch-updater` that you unpacked in [step 2](#2-download-and-verify-the-installer) after the checks, in the folder that only root (SYSTEM and the administrators) can write. The installer calls it as `myriarch-updater verify-bundle`, a file whose name does not start with `cosign` |
| The updater, verified by its SHA-256 | The host has no `cosign` and gets the updater anyway | `--with-updater --updater-url <URL> --updater-sha256 <hex>` (Windows: `-WithUpdater -UpdaterUrl <URL> -UpdaterSha256 <hex>`). Take the hash of the updater from a `checksums.txt` that you verified on another machine. The installer downloads the updater, compares it with the hash and then uses it as the verifier of the collector and the supervisor |
| An updater that is installed already | You run the installer again on a host that has the updater | Nothing to pass: the installed `myriarch-updater` is found. On Linux it comes after a `cosign` in the `PATH`, on Windows before `cosign.exe` |

With `--with-updater` the installer needs a **trusted hash source** for the updater, because the updater runs as root or
SYSTEM: either `--updater-checksums-url` (`-UpdaterChecksumsUrl`), which downloads `checksums.txt` and its bundle and verifies
them with the verifier, so it needs the first, the second or the fourth row; or `--updater-sha256` (`-UpdaterSha256`). Without a
hash source the installer stops with `updater_hash_source_missing`.

Without `--with-updater` the host gets no `myriarch-updater`, so the `updater` executor cannot update its collector. The
verifier is then `cosign` or the updater of the verified archive, passed with `--verifier` (`-Verifier`).

## 4. Install on Linux

Create the token file without putting the token into the shell history. Stay in the root shell of
[step 2](#2-download-and-verify-the-installer), whose variables the commands use, and work in its directory, the one that only
root can write:

```bash
cd "$SETUP" && (umask 077; cat > enrollment-token)        # paste the token, press Enter, then Ctrl-D
```

Then run the script from the unpacked archive. The `cat` above reads every line that follows it, so do not paste both blocks at
once. `VERIFIER` is the verifier that you chose in [step 3](#3-choose-how-the-host-verifies-downloads): `cosign` at a place that
only root can replace, or on a host without `cosign` the updater of the verified archive:

```bash
VERIFIER="$COSIGN"                         # a host without cosign: VERIFIER="$SETUP/myriarch-updater"

./deploy/scripts/install-linux.sh \
  --opamp-endpoint wss://fleet.example.com/v1/opamp \
  --token-file ./enrollment-token \
  --site-type edge --site-id edge-042 --site-name "Edge site 42" --site-country NL --site-timezone Europe/Amsterdam \
  --collector-version 0.162.0 \
  --verifier "$VERIFIER" \
  --with-updater \
  --updater-url "${BASE}/myriarch-updater_${VERSION}_linux_${ARCH}" \
  --updater-checksums-url "${BASE}/checksums.txt"

rm -f enrollment-token
```

`--updater-checksums-url` makes the installer verify `checksums.txt` with the verifier and take the hash of the updater from it.
A host without `cosign` can use the updater of the verified archive as shown, or it passes the hash that it took from the
`checksums.txt` of an admin host, in place of `--verifier` and `--updater-checksums-url`:

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

Continue in the elevated Windows PowerShell of [step 2](#2-download-and-verify-the-installer). The token file goes into the setup
folder. It inherits the DACL of the folder, so only SYSTEM and the administrators can read it. Create it from a prompt, without
putting the token into the command history (a prompt reads every line that follows it, so do not paste both blocks at once):

```powershell
Read-Host 'Enrollment token' | Set-Content -Path "$Setup\enrollment-token.txt" -NoNewline
```

*From v0.2.0* the installer also has the server mode of [the command of the server](#one-command-install-a-windows-host-from-the-server):
`-Server https://fleet.example.com` in place of `-OpampEndpoint`, `-CollectorVersion`, `-Verifier`, `-WithUpdater` and the `-Updater*`
options. The installer then asks the server for the plan (`-Fleet` names the fleet that you expect, `-PlanWait` the seconds to wait
while the server prepares the artifacts), takes collector and supervisor from the server and verifies them with the
`myriarch-updater.exe` of the unpacked archive, which you verified in step 2. The certificate of the server has to be in the
certificate store of the machine. The rest of this step describes the installer without `-Server`.

Then choose the verifier of [step 3](#3-choose-how-the-host-verifies-downloads) and start the installer:

```powershell
$Verifier = $Cosign      # a host without cosign.exe: $Verifier = "$Setup\package\myriarch-updater.exe"

Unblock-File "$Setup\package\deploy\scripts\install-windows.ps1"        # only after the checks of step 2 passed
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File "$Setup\package\deploy\scripts\install-windows.ps1" `
  -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile "$Setup\enrollment-token.txt" `
  -SiteType edge -SiteId edge-042 -SiteName "Edge site 42" -SiteCountry NL -SiteTimezone Europe/Amsterdam `
  -CollectorVersion 0.162.0 -WithUpdater `
  -UpdaterUrl "$Base/myriarch-updater_${Version}_windows_${Arch}.exe" `
  -UpdaterChecksumsUrl "$Base/checksums.txt" `
  -Verifier $Verifier

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
  `install-windows.ps1` fails. Allow it by its hash, after you verified the archive. The execution policy does not change
  this. From v0.2.0 the installer recognizes the mode first and stops with `language_mode_unsupported`
  ([When the installer stops](#when-the-installer-stops)).

**The options.** The trust model is the same as on Linux: `-VerifySignatures`, `-Verifier`, `-Insecure`,
`-UpdaterChecksumsUrl` with `-UpdaterVersion` and `-UpdaterBundleUrl`, or `-UpdaterSha256` for a host without `cosign`. For
that, replace `-Verifier` and `-UpdaterChecksumsUrl` by `-UpdaterSha256 <hex>`, with the hash from the line of
`myriarch-updater_<version>_windows_<arch>.exe` in the `checksums.txt` that you verified. With the files of step 2 this prints it:

```powershell
$Exe   = "myriarch-updater_${Version}_windows_${Arch}.exe"
$lines = @(Get-Content "$Setup\checksums.txt" | Where-Object { $_.EndsWith("  $Exe", [StringComparison]::Ordinal) })
if ($lines.Count -ne 1) { throw "expected exactly one line for $Exe in checksums.txt" }
$lines[0].Split(' ')[0]        # the hash for -UpdaterSha256
```

`cosign.exe` must be 3.0.4 or newer. `-SigningKey` and `-UpdaterChecksumsSigUrl` were removed. In addition
`-UpdaterSignerThumbprint` requires the Authenticode signature of the updater to come from one certificate; a pinned thumbprint
breaks whenever that certificate is renewed, so it fits only a private PKI.

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
| `option_missing` | A required option is absent, and the message names it: the site type, the site ID, the **collector version** on Windows without `-Server` (`-CollectorVersion`; `install-linux.sh` of v0.2.0 has a default), the OpAMP endpoint on Linux without `--server`, the token (`--token-file`, `-TokenFile`) or, with `--with-updater`, `--updater-url`. A missing or malformed endpoint ends with `option_invalid`. | Add the option. The collector version is the input that is forgotten most often, because the install example of the token dialog of v0.1.0 leaves it out; see [What you need](#what-you-need). |
| `usage_error` (Linux) | An option is unknown or has no value, for example `--verifier ""` when a command substitution such as `$(command -v cosign)` expands to nothing because the host has no `cosign`. | Check the names and the values of the options (`./deploy/scripts/install-linux.sh --help` lists them). On Windows PowerShell rejects an unknown or valueless parameter itself, without a `[code]`. |
| `updater_hash_source_missing` | `--with-updater` without `--updater-checksums-url` or `--updater-sha256`. | Add one of them ([step 3](#3-choose-how-the-host-verifies-downloads)). |
| `sigstore_verifier_missing` | The installer found no Sigstore verifier: no `cosign` it may use, no installed updater and no updater verified by `--updater-sha256`. On Linux the `PATH` of root may not contain `cosign`. On Windows a `cosign.exe` in a folder that unprivileged accounts can write is ignored, with a warning. | Choose a verifier as in [step 3](#3-choose-how-the-host-verifies-downloads): pass `--verifier` or `-Verifier` with the full path of a `cosign` that only root (the administrators) can replace, or of the `myriarch-updater` of the verified archive, or add the updater with its SHA-256. |
| `sigstore_verifier_outdated` | `cosign` is older than 3.0.4 (a pre-release of 3.0.4 counts as older). | Install `cosign` 3.0.4 or newer. |
| `file_unreadable` | A file that you named cannot be used: the token file, the CA file, `--trusted-root` (`-TrustedRoot`) or the verifier. On Linux `--verifier` must be executable, on Windows `-Verifier` must exist. | Check the path and, on Linux, the mode of the file. |
| `directory_unsafe` (Windows) | The file of `-Verifier` is in a folder that unprivileged accounts can change, for example `C:\Tools`, or the installer bundle (`-Server`) or an archive was unpacked outside the setup folder of [step 2](#2-download-and-verify-the-installer). | Copy `cosign.exe` into a folder that only administrators can write, for example `C:\Program Files\cosign`, or take the `myriarch-updater.exe` from the archive that you unpacked into the setup folder, and pass that path. |
| `template_missing` | The script runs outside the unpacked archive, so the directory `deploy` with the templates is not next to it. | Run the script from `deploy/scripts/` of the unpacked archive, or pass the `deploy` directory of the archive with `--templates-dir` (`-TemplatesDir`). |
| PowerShell refuses the script (no `[code]`): "running scripts is disabled on this system" or "is not digitally signed" | The execution policy forbids the script (`Restricted`), or the file still carries the Mark of the Web (`RemoteSigned`). | Start the installer as in [step 5](#5-install-on-windows) and run `Unblock-File` after the checks. If Group Policy sets the policy, see the notes of step 5. |
| `language_mode_unsupported` (Windows, from v0.2.0), or the PowerShell error "Cannot invoke method. Method invocation is supported only on core types in this language mode" with the installer of v0.1.0 | PowerShell runs the script in Constrained Language Mode (or another mode below `FullLanguage`). App Control for Business with script enforcement does this to a script that no rule allows, and the unsigned installer of a release is such a script. It is the first check of the installer, and nothing was changed. The message names the file. | Allow `install-windows.ps1` with a hash rule that you create from the copy you verified in [step 2](#2-download-and-verify-the-installer), one rule per release (the hash that App Control uses for a script is not the SHA-256 in `checksums.txt`), or ask the owner of the policy for an exception. `-ExecutionPolicy RemoteSigned` does not change the mode. See [Language mode and App Control](../../deploy/windows/README.md#language-mode-and-app-control). |
| `execution_policy_blocked` (Windows command, from v0.2.0) | Group Policy sets the execution policy of the scope `MachinePolicy` or `UserPolicy` to `AllSigned` or `Restricted`. It takes precedence over `-ExecutionPolicy`, so the unsigned installer cannot run. The command stops before it downloads anything and does not work around the policy. | `Get-ExecutionPolicy -List` shows the policy. Ask its owner for an exception for this host, or install the host on another way that the policy allows. |
| `installer_failed` (Windows command, from v0.2.0) | The installer that the command started ended with a non-zero exit code. Its own message, with its code, is the line above. | Act on that message. |
| `plan_failed`, `plan_invalid`, `plan_rejected`, `plan_unavailable` (from v0.2.0) | The server mode of the installer (`--server`, `-Server`; [Linux](#one-command-install-a-linux-host-from-the-server), [Windows](#one-command-install-a-windows-host-from-the-server)) did not get a usable plan. | [Installing a host from the server](../operations/troubleshooting.md#installing-a-host-from-the-server) has the cause and the next step of each code. |
| `privilege_required` | The installer does not run as root or Administrator. | Use a root shell (`sudo -i`), or an elevated Windows PowerShell. |
| `unsupported_platform` | The installer supports amd64 and arm64 and nothing else. On Linux `uname -m` reports another CPU (`unsupported architecture: <value>`), or the system is not Linux (`only Linux is supported`). On Windows the processor architecture is neither AMD64 nor ARM64, or the installer would use arm64 (an ARM64 host without `-Arch amd64`, or `-Arch arm64`), and it stops because upstream publishes the supervisor for Windows amd64 only (`on ARM64 install with -Arch amd64`). | On Windows ARM64 add `-Arch amd64` ([What you need](#what-you-need)). The installer of v0.1.0 stopped only at the supervisor step: it had stopped the service and put the ARM64 collector into `C:\Program Files\Myriarch\collector\versions\<version>`, and the next run keeps a collector of the same version that it finds there, so delete that folder first if the host should run the amd64 collector. From v0.2.0 the installer stops as soon as it knows the architecture, before it stops a service, downloads or writes anything, and leaves nothing behind. On Linux there is no build for another CPU; `--arch amd64` or `--arch arm64` only replaces the detection and fits a machine that runs binaries of that architecture. |
| `tool_missing` | A tool is absent. The message names it. | Install it ([What you need](#what-you-need)). |
| `download_failed` with HTTP 404 | The release does not exist. A 404 for `opampsupervisor_<version>_...` means that the upstream project published no supervisor for this collector version. | Check the version. Pass `--supervisor-version` (`-SupervisorVersion`) if the supervisor has another version than the collector. |
| `sigstore_verification_failed` or `checksum_mismatch` | The file is not the one that was signed: modified, from another release, or a bundle that does not belong to it. For `sigstore_verification_failed` there is a second cause: the verifier could not fetch the Sigstore trusted root, because the host cannot reach the Sigstore TUF repository (`tuf-repo-cdn.sigstore.dev`), for example behind a proxy or a firewall. The Windows message ends with the output of the verifier; the Linux message carries no detail, because the installer discards that output. | Do not retry with `--insecure`. To tell the causes apart, run `./myriarch-updater fetch-trusted-root --out trusted_root.json` (Windows: `.\myriarch-updater.exe`) in the unpacked archive: if it fails, the host cannot reach the repository, and if it succeeds, pass that file with `--trusted-root` (`-TrustedRoot`) to verify without the repository. Otherwise download the file again from the release and check the version; a mirror must carry `checksums.txt` and the bundles unchanged. |
| `conflicting_install` (from v0.2.0) | An installation of the upstream project is present. The installer stops before it creates a work directory, downloads a file, stops a service or writes anything, and it removes nothing; the message names what it found and the next step. On Windows that is a service `opampsupervisor` (supervisor MSI) or `otelcol-contrib` (contrib MSI), in any state. On Linux it is the unit `otelcol-contrib.service` or `opampsupervisor.service` of a deb or rpm package, active or enabled; a package whose units neither run nor are enabled does not stop the installer. Each of them is a second collector next to the one that myriarch manages. | Remove the package, then run the installer again. Linux: `systemctl disable --now <the units of the message>`, then find the package with `dpkg -S` or `rpm -qf` on the path that `systemctl cat <unit>` prints, and remove it with your package manager. Windows: uninstall the MSI (*Settings*, *Apps*, or `msiexec /x` with the MSI file); a service that you created yourself under one of the names goes with `sc.exe delete <name>`. |
| `supervisor_missing` (from v0.2.0) | When the installation step of the supervisor is over, the file is not where the service expects it. Linux: `/opt/myriarch/supervisor/opampsupervisor` does not exist, is no regular file (a directory, for example) or is not executable. Windows: `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe` does not exist, is not the file whose SHA-256 the installer verified, or is no regular file (a directory or a link). On Windows the code also replaces `service_not_running` when the service did not start because the file of its binPath is gone. The message names the path. A file that is gone, changed or not executable is usually the work of a security tool that removed, quarantined or changed the unsigned binary after the copy; on Linux a file system mounted with `noexec` gives the same result. A directory or a link at the path (the message says "is not a regular file") was put there by someone or something else, and the installer replaces a file and removes neither. | **A file that is gone, changed or not executable.** Windows: look at the protection history (quarantine) of the antivirus or endpoint protection for `opampsupervisor.exe` (Microsoft Defender: *Windows Security*, *Protection history*, or `Get-MpThreatDetection`), allow the folder `C:\Program Files\Myriarch\supervisor`, and run the installer again; it downloads and verifies the file again. Linux: look for a cleanup or security tool that removed or changed the file, check the mount with `findmnt -T /opt/myriarch/supervisor` for `noexec`, then run the installer again. **A directory or a link.** A new run stops at the same item, on Windows before it downloads anything: look at what it is, remove it yourself, then run the installer again. |
| `Get-Service` lists `opampsupervisor` or `otelcol-contrib`, and `otelcol-supervisor` is missing, or `C:\Program Files\Myriarch\supervisor\opampsupervisor.exe` does not exist | A collector or supervisor was installed from an MSI of the upstream project. The supervisor MSI installs a service `opampsupervisor` with manual start in its own folder, and the contrib MSI installs `otelcol-contrib`, which starts at once. myriarch manages neither: the installer creates the service `otelcol-supervisor` with the supervisor in `C:\Program Files\Myriarch\supervisor\`. The installer of v0.1.0 does not look for the upstream services and does not remove them; from v0.2.0 it stops with `conflicting_install` (above). | If `otelcol-supervisor` is missing, the installer stopped earlier: read its last `ERROR [<code>]` line and use this table. Remove the upstream MSI (*Settings*, *Apps*, or `msiexec /x`), then run the installer again. |
| `systemctl status otelcol-contrib` shows a running service (Linux) | The contrib deb or rpm package of the upstream project started a collector of its own, next to the one that myriarch manages. The installer of v0.1.0 does not notice it; from v0.2.0 an active or enabled unit stops the installer with `conflicting_install` (above). | Remove the package with your package manager. |
| `service_not_running` | The supervisor service stopped after the start. A warning on Linux, an error on Windows. From v0.2.0 the Windows installer first checks the file of the service's binPath and reports `supervisor_missing` when it is gone (above). | Read the log: `journalctl -u otelcol-supervisor -n 50`, or the Event Viewer (Windows Logs, Application) and `C:\ProgramData\Myriarch\supervisor\agent.log`. |

## What the installers verify

The details behind the options of the steps above:

- **Before they change anything** (from v0.2.0) the installers look at the host and stop with a code if it is not fit for the
  installation: `language_mode_unsupported` (Windows, PowerShell below `FullLanguage`), `unsupported_platform` (Windows on
  ARM64 without `-Arch amd64`) and `conflicting_install` (a package of the upstream project). When the installation step of
  the supervisor is over, they check that the file is there (`supervisor_missing`). None of these removes anything; the
  messages name the cause and the next step ([When the installer stops](#when-the-installer-stops)).
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
  Both fetch the Sigstore trusted root through TUF from the public Sigstore repository; `--trusted-root FILE` enables offline
  verification.
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
  - **Hosts without a verifier** (no `cosign`, no installed updater) pass the `myriarch-updater` of the verified archive as
    `--verifier`, or they verify `checksums.txt` on an admin host, as described in
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

In the root shell and the directory of [step 2](#2-download-and-verify-the-installer),
`./deploy/scripts/install-linux.sh --uninstall` removes the services and files and keeps the data in `/var/lib/otelcol`; add
`--purge` to delete that as well, including the identity. On Windows use `-Uninstall` and `-Purge`, started as in
[step 5](#5-install-on-windows). Then delete the agent in the UI once it is offline.

Next: the host is now part of a fleet, so you can [roll out a configuration change](roll-out-a-config-change.md) or a
[collector version](roll-out-a-collector-version.md) to it. Details of the supervisor configuration are in
[upstream-notes.md](../design/upstream-notes.md) and the templates in [deploy/supervisor/](../../deploy/supervisor/).
