---
title: "Connect a host"
weight: 10
description: "Enroll a Linux or Windows host with the installation scripts: supervisor, collector and optionally the myriarch-updater."
---

A managed host runs three pieces: the upstream **OpAMP Supervisor** (the service `otelcol-supervisor`), the **collector**
(`otelcol-contrib`) that the supervisor starts, and optionally the **`myriarch-updater`**, which installs the collector
updates that `myriarchd` offers. The installation scripts set up exactly this, idempotently:

- Linux with systemd: [`deploy/scripts/install-linux.sh`](../../deploy/scripts/install-linux.sh) (bash). Support level: beta.
- Windows: [`deploy/scripts/install-windows.ps1`](../../deploy/scripts/install-windows.ps1) (PowerShell 5.1 or newer, run as
  Administrator). Support level: experimental.
- Kubernetes: the collector runs as a container; see [deploy/k8s/collector/README.md](../../deploy/k8s/collector/README.md).

The scripts deliberately do not use the deb/rpm packages or the contrib MSI, because both start a collector service of their
own immediately.

## 1. Create an enrollment token

An agent connects to `myriarchd` with an enrollment token (`myre_...`). Create one as an administrator in the UI under
*Settings*, tab *Enrollment tokens*, button *Create token*. Give it a name, optional **labels** and an optional expiry date.
The secret is shown **once**; the dialog also shows install examples. Labels are *server labels*: they are copied to every
agent that enrolls with the token, and a host cannot claim them for itself. Use them for decisions that matter, such as
`update-via=awx`, and the self-reported `site.*` attributes only for coarse selection of fleets.

Tokens can also be declared in the server configuration (`opamp.enrollmentTokens`, fed from a mounted secret). See
[Authentication](../operations/authentication.md) for that and for how people sign in.

Save the token on the host in a file that only root (or Administrators) can read, for example `/root/enrollment-token`
with mode `0600`. Prefer `--token-file` over `--token`: an argument shows up in the process list and in the shell
history, and the script warns about it.

## 2. Install on Linux

The script runs as root and needs these tools on the host: `systemctl`, `useradd` or `adduser`, `curl`, `tar` together with
`gzip` (GNU `tar` runs `gzip` to unpack the `.tar.gz` archives), `install`, `cmp` and `sha256sum` or `shasum`. A missing
tool stops the script with the error code `tool_missing` and the name of the tool.

```bash
sudo ./deploy/scripts/install-linux.sh \
  --opamp-endpoint wss://fleet.example.com/v1/opamp \
  --token-file /root/enrollment-token \
  --site-type edge --site-id edge-042 --site-name "Edge site 42" --site-country NL --site-timezone Europe/Amsterdam \
  --collector-version 0.162.0 \
  --with-updater \
  --updater-url https://mirror.example.com/myriarch-releases/v<ver>/myriarch-updater_<ver>_linux_amd64 \
  --updater-checksums-url https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt
```

The host needs a Sigstore verifier for this command: `cosign` 3.0.4 or newer in `PATH`, or an installed `myriarch-updater`.
A host without one uses `--updater-sha256 <hex>` instead of `--updater-checksums-url`; see below.

The site options become attributes of the host (`site.type`, `site.id`, `site.name`, `site.country`, `site.region`,
`site.timezone`, and `deployment.environment.name` from `--environment`, default `prod`). The selectors of your fleets match
on them, so choose them to fit your repository: the example fleets select `site.type` `edge` or `datacenter` for hosts and
`cloud` for Kubernetes gateways.

What the script verifies, and why you may be asked for more options:

- **Downloads use https only**, also after redirects. `--insecure` is for labs and tests: it allows `http://`, `file://` and
  `ws://` to the server and skipping the Sigstore check.
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
  - **Verify the download first.** The release archive of the updater contains the installation scripts. Verify the archive
    against `checksums.txt` and its bundle before you run a script from it; see
    [Verify releases](../security/verify-releases.md).
- `--release-base-url` points the collector and supervisor downloads to an internal mirror with the layout
  `<base>/<tag>/<asset>`. `--ca-file` is the CA for the connection to `myriarchd`; a mirror with its own CA must be trusted
  by the certificate store of the host.

Layout on the host: collector versions in `/opt/myriarch/collector/versions/<v>/` with a `current` symlink that the updater
switches atomically, the supervisor in `/opt/myriarch/supervisor/`, configuration in `/etc/myriarch/` (`supervisor.yaml`,
`fallback.yaml`, `myriarch.env` and `enrollment-token` with mode `0600`) and data in `/var/lib/otelcol`. Running the script
again brings the host to the state you give it: a new version switches `current` and restarts the service, and local
changes to `fallback.yaml` and further secrets in `myriarch.env` are kept.

Secrets that the collector needs (for example `OTEL_GATEWAY_TOKEN`, referenced as `${env:OTEL_GATEWAY_TOKEN}` in a
blueprint) go into `/etc/myriarch/myriarch.env`. Then restart with `systemctl restart otelcol-supervisor`.

## 3. Install on Windows

```powershell
.\deploy\scripts\install-windows.ps1 -OpampEndpoint wss://fleet.example.com/v1/opamp -TokenFile C:\Temp\enrollment-token.txt `
  -SiteType edge -SiteId edge-042 -SiteName "Edge site 42" -SiteCountry NL -SiteTimezone Europe/Amsterdam `
  -CollectorVersion 0.162.0 -WithUpdater `
  -UpdaterUrl https://mirror.example.com/myriarch-releases/v<ver>/myriarch-updater_<ver>_windows_amd64.exe `
  -UpdaterChecksumsUrl https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt `
  -Verifier 'C:\Program Files\cosign\cosign.exe'
```

The trust model is the same as on Linux: `-VerifySignatures`, `-Verifier`, `-Insecure`, `-UpdaterChecksumsUrl` with
`-UpdaterVersion` and `-UpdaterBundleUrl`, or `-UpdaterSha256` for a host without a verifier. `cosign.exe` must be 3.0.4 or
newer and is only taken from a directory that only administrators can write; a verifier passed with `-Verifier` must be in such a
directory too (`directory_unsafe` otherwise). `-SigningKey` and
`-UpdaterChecksumsSigUrl` were removed. Extract the release archive and run `Unblock-File` on the script **only after** you
verified the archive ([Verify releases](../security/verify-releases.md)). In addition `-UpdaterSignerThumbprint` requires the
Authenticode signature of the updater to come from one certificate; a pinned thumbprint breaks whenever that certificate is
renewed, so it fits only a private PKI. Under App Control for Business with script enforcement an unsigned script runs in
Constrained Language Mode and `install-windows.ps1` fails; allow it by its hash. The supervisor runs as the virtual service account `NT SERVICE\otelcol-supervisor`, not as `LocalSystem`; only
the `myriarch-updater` service runs as SYSTEM. Services, paths and troubleshooting are in
[deploy/windows/README.md](../../deploy/windows/README.md). Collector secrets are set with `Set-ServiceEnvironment.ps1`.

## 4. Check the result

Within a few seconds the host shows up under *Agents*: one entry for the collector and, with `--with-updater`, one for the
updater. Open the collector and check three things:

- the **fleet**: if it says *unassigned*, no selector matches. Compare the attributes of the agent with the `selector` of your
  fleets (the page *Fleets* has a "Which fleet?" check);
- the **configuration state** `in_sync`: the host received and applied the rendered configuration of its fleet;
- the **health** and the effective configuration.

If the host does not appear, read the installer message first. Every error is one line `ERROR [<code>] <message>` with a
stable code (the catalog is at the top of `install-linux.sh`), for example `checksum_mismatch` or `https_required`. Then
check the service (`systemctl status otelcol-supervisor`, the Event Viewer on Windows) and the
[troubleshooting table](../operations/troubleshooting.md), for example `401` in the handshake or "the instance uid is bound
to another credential".

## Golden images and cloned machines

The identity of an agent is its instance UID: `persistent_state.yaml` in the supervisor storage (`/var/lib/otelcol/supervisor`
on Linux, `C:\ProgramData\Myriarch\supervisor` on Windows) and `instance_uid` in the updater state. The scripts create
these directories **empty**. Empty both before you create an image or clone a machine; otherwise all clones report the same
identity and displace each other.

## Remove a host

`sudo ./deploy/scripts/install-linux.sh --uninstall` removes the services and files and keeps the data in `/var/lib/otelcol`;
add `--purge` to delete that as well, including the identity. On Windows use `-Uninstall` and `-Purge`. Then delete the
agent in the UI once it is offline.

Next: the host is now part of a fleet, so you can [roll out a configuration change](roll-out-a-config-change.md) or a
[collector version](roll-out-a-collector-version.md) to it. Details of the supervisor configuration are in
[upstream-notes.md](../design/upstream-notes.md) and the templates in [deploy/supervisor/](../../deploy/supervisor/).
