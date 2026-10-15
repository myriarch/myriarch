# 0046. Host installation from the server: versions, artifacts and the installer bundle as data, integrity anchored on the host

- Status: Accepted
- Date: 2026-10-15
- Related: [ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md), [ADR-0012](0012-agent-access-via-enrollment-tokens.md), [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md), [ADR-0035](0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)

## Context

**How a host is connected in v0.1.0.** The installation scripts `install-linux.sh` and `install-windows.ps1` ship in the release archive of the updater (`myriarch-updater_<version>_<os>_<arch>`), next to the templates they render. The operator has to bring everything the scripts need:

- The collector version. It is required, and without it a script stops with `option_missing`. Nothing tells the host which version its fleet targets.
- The archive. It should be downloaded from the release, verified with cosign (on an admin host, see [Verify releases](../security/verify-releases.md)) and unpacked in a directory that only root (on Windows: SYSTEM and the administrators) can write, and the script must run from the unpacked archive, because it reads its templates from the directory above its own (`template_missing` otherwise).
- A Sigstore verifier on the host: `cosign` 3.0.4 or newer, or a `myriarch-updater` (the one in the verified archive, or an updater that the script downloads and checks against a SHA-256 that the operator took from a verified `checksums.txt`). On Windows the verifier must lie in a directory that only administrators can write. Without a verifier the script stops with `sigstore_verifier_missing`, before it downloads the collector.
- On Windows, a way to start an unsigned script. A client runs scripts under the execution policy `Restricted`, and the guide did not say how to start the script without changing the policy of the machine.

The install example in the enrollment-token dialog of v0.1.0 names none of this: it leaves out the collector version and the verifier, and it starts `install-windows.ps1` directly. A fresh Windows client first refuses to run the script, then stops with `option_missing` for the collector version, then with `sigstore_verifier_missing`.

**What the server already knows.** The target version of a fleet follows from Git and from the choice in the UI ([ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md)). The artifact cache holds collector and supervisor releases, checked by SHA-256 and Sigstore bundle. The server knows its own OpAMP endpoint. A host that is not enrolled yet has no agent identity and no pending package offer, so the download tokens of the updater, which are bound to one agent and one offer ([Artifact verification](../security/model.md#artifact-verification)), cannot serve it.

**Trust.** The server already sends configuration that runs with the rights of the collector's service account, and it offers package updates that the updater verifies again on the host ([ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md)). A script that the server provides would run as root or SYSTEM at the first installation, before anything on the host could verify it.

**Windows.** The Windows files of v0.1.0 are not Authenticode-signed ([ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md)). A Windows client runs scripts under `Restricted` and Windows Server under `RemoteSigned`, which refuses a file that carries the Mark of the Web. A policy set by Group Policy (the scopes `MachinePolicy` and `UserPolicy`) takes precedence over every other scope, and App Control for Business can put an unsigned script into Constrained Language Mode.

**Packages of the upstream project.** The MSI, deb and rpm packages of the upstream project either start a collector service of their own or install a supervisor under another service name and path ([upstream notes](../design/upstream-notes.md#packages-and-what-they-install)). The scripts deliberately use none of them, and in v0.1.0 they do not notice an installed package.

## Decision

We will install hosts from the server: the server provides data, and the host verifies code. The work is part of v0.2.0. Until it ships, the manual path of [Connect a host](../tasks/connect-a-host.md) is the only path, and every page that describes the new path says "Available from v0.2.0".

1. **The server is the source of versions, artifacts and the installer bundle.** For a host that presents an enrollment token, `myriarchd` will provide:
   - a **plan**: the fleet the host would join, the collector version, the supervisor version (the collector version, when the release tracker knows that release), the OpAMP endpoint, whether the updater is installed, and the URLs of the collector and supervisor artifacts with short-lived download grants;
   - the **artifact bytes**, with their `.sha256` and `.sigstore.json`, from the artifact cache;
   - the **installer bundle**: the updater archive of the release the server itself runs (scripts, templates and the `myriarch-updater` binary), the file an operator would download by hand.

   Hosts then download from `myriarchd` only. The copied command is a short bootstrap: it downloads the bundle, checks it, unpacks it and starts the installer from it. The plan is data. The installer takes it as input and never as code.

2. **Integrity stays anchored on the host.** The installer checks every file it installs or runs, before it runs it. Collector and supervisor are verified by SHA-256 and by their Sigstore bundle against an identity that the installer builds itself: the upstream release workflow at the tag of the version (`base-release` for the collector, `base-binary-release` for the supervisor), as the installers of v0.1.0 do ([Host-side checks of the updater](../security/model.md#host-side-checks-of-the-updater)). The verifier is the `myriarch-updater` of the bundle (`verify-bundle`), so the host needs no `cosign`. The OIDC issuer is fixed in the installer, and the Sigstore trusted root comes from the TUF root embedded in the verifier or from a local file (`--trusted-root`). **Identity, issuer and trust root never come from the server.** The updater of the bundle counts only as a regular file from the same archive as the script, which makes the archive the unit of integrity, and it is hashed again when it is copied. A wrong or compromised server can therefore at most choose another genuine upstream version, an older one included: the rules of decision 7 bind only a server that works as intended, and the host checks that an artifact is genuine for the version the plan names, not that the version is a good one. It cannot make an installer run collector or supervisor code that lacks the upstream signature.

3. **Trust on first use for the bootstrap.** The first command has nothing on the host to verify the bundle with. It pins the SHA-256 of the bundle file. The server shows that hash after it verified the `checksums.txt` of its own release keyless (the identity of `release.yml` at the tag, [ADR-0023](0023-keyless-release-signing.md), [ADR-0029](0029-host-under-the-myriarch-organization.md) and [ADR-0035](0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)) and compared the archive with its line. The bootstrap checks the pinned hash before it unpacks or runs anything, and stops if it differs. The first installation therefore trusts the server that displayed the command. This is documented in the guide and in the security model. For sites that cannot accept it, the **manual verified path stays supported and documented**: download the archive from the release, verify `checksums.txt` and the archive on an admin host, and install with the options of the scripts. The pinned hash is the line of the archive in the signed `checksums.txt`, so an operator can also compare it with the release before the first run.

4. **Bytes are never changed.** The server serves the archives and scripts of the release exactly as released. It does not template, re-encode or fill in a script. Parameters travel in the command line and in the plan. This keeps the hash rules of App Control valid, which match byte-identical files only, and it keeps later Authenticode signatures ([ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md)) valid.

5. **The enrollment token authorizes the installation.** Plan, bundle download and artifact bytes for a host need a valid enrollment token, checked by the same authentication as the OpAMP endpoint ([ADR-0012](0012-agent-access-via-enrollment-tokens.md)), and also when `opamp.requireEnrollmentToken` is off. Installation with mutual TLS but without a token is not supported in v0.2.0. The server decides the fleet from the attributes the host reports and the `labels.*` of the token (`assignment.Service.Match`); a fleet hint that does not agree is refused with 409. Artifacts reach the host with an **install grant**: a second kind of `myrd_` download token, bound to one artifact and to the ID of the enrollment token, with a short lifetime, valid only while that enrollment token is valid, and not interchangeable with the download tokens of an updater.

6. **The token in the copied command.** The default command contains the token, under these rules:
   - only a freshly created token goes into a command; the server cannot show an existing token again, because it stores only the hash;
   - a shell builtin or cmdlet (`printf` on Linux, `Set-Content` on Windows) writes the token into a private file: mode `0600` on Linux, a directory with its own DACL for SYSTEM and Administrators on Windows. The installer reads it with `--token-file` (`-TokenFile`), and the file is deleted after the run;
   - nothing that the server, the installers or the bootstrap write contains the token: it does not appear in an argument vector, a URL or query, a log, an error text, an audit detail or a metric label. It leaves the host only as a request header (`curl -K -` reading from standard input, `-Headers` in PowerShell).

   The command itself is text, and what records typed text keeps the token: the clipboard, the shell history, the history file of PSReadLine (Windows PowerShell 5.1), PowerShell transcription, PowerShell script block logging (event 4104, often forwarded to a log collector) and the recording of a terminal session. These are outside the controls above, which do not depend on history filters. A second variant, **token from a file**, leaves the token out of the command and fits production, automation and every host that records its input; the documentation names each of these records, warns about them and sends such hosts to the variant. A token that a record kept counts as leaked. No new kind of token comes with v0.2.0: install codes or one token per host would need a record of their own.

7. **Version rule of the plan.** The plan gives the effective target of the fleet (`domain.ResolveVersionTarget`, [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md)), the target that the planner uses for the version rollouts of [ADR-0006](0006-version-updates-via-exchangeable-executors.md). While a version rollout of the fleet is `pending`, `running` or `waiting_approval`, it gives the `fromRef` of that rollout instead, so that the new host becomes a straggler of the last wave and waves and approvals apply to new hosts as well. Without a uniform `fromRef` (a mixed fleet) the answer is 409. A rollout that is `paused`, `failed` or `rolling_back` answers 409 `rollout_blocking`. A fleet on `hold` or without a target answers 412. An explicit version is accepted only inside the Git frame (otherwise 422).

8. **Default without a server.** Called without `--server` and without a collector version, the installers use the tested version `OTELCOL_VERSION` of `ci/versions.env`, which a test keeps in step. The default is deterministic and needs no lookup. The newest stable upstream version is used only on request (`--collector-version latest`), because it is not the version that was tested and its lookup depends on GitHub and its rate limits.

9. **Who may use the dialog.** Everyone with the permission `read` can open "Add host" and render commands. Only users with `enrollment:manage` (administrators) can create the token. No new permission is added.

10. **Windows: an execution policy for one process only.** The command starts the installer as `powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File <script>`, after the hash check and after `Unblock-File` on the verified script. It never uses `Bypass`, and it never changes the policy of the machine or of the user. The policy applies to the new PowerShell process only, and the session of the administrator keeps its own. A policy from Group Policy takes precedence over the parameter: the installer does not work around it, and the bootstrap reports it with a clear message. While the Windows files are unsigned ([ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md)), `AllSigned` and App Control block the scripts; under App Control the operator allows them by a hash rule per release. Once the files are signed, a publisher rule is enough and the bootstrap also checks `Get-AuthenticodeSignature`. The Windows bootstrap uses only cmdlets and native programs, because under App Control the typed command itself runs in Constrained Language Mode.

11. **Packages of the upstream project stop the installation.** The installers detect an installed MSI, deb or rpm of the collector or of the supervisor (the services `otelcol-contrib` and `opampsupervisor`) and stop with `conflicting_install`. The message names the service and the next step. Nothing is removed automatically.

This record refines [ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md) (install grants are a second kind of download credential next to the tokens of package offers, and the trust root of a host never comes from the server), [ADR-0012](0012-agent-access-via-enrollment-tokens.md) (the enrollment token also authorizes the installation of a host, with rules for the token in a command) and [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md) (the effective target also decides what a new host installs).

### Controls

#### Server

- The install routes need a valid enrollment token, always. An unknown, revoked or expired token gets 401 without a hint whether it exists, a limited lookup gets 429, and a database failure gets 503.
- The fleet comes from the assignment of the server, never from the request. A hint that disagrees gets 409, and nothing is delivered.
- The version follows decision 7 and never lies outside the Git frame or outside the releases that the release tracker knows (409, 412 or 422).
- A grant is issued only for an artifact in the status `ready`, with a pinned SHA-256 and a verified bundle. Downloads from outside go through the existing cache with its outbound policy, redirect rules, size limit and limit of three concurrent downloads. An artifact that is not ready yet answers 503 `preparing` with `Retry-After`.
- An install grant is an HMAC over artifact, enrollment-token ID and time. Its lifetime is configurable and, in production, positive and at most `artifacts.downloadTokenTtl`. A grant of a revoked or expired token, an expired grant and a grant for another artifact get 401.
- Bundles exist only for the strict release version that the server runs (from 0.2.0, release candidates included), after the keyless verification of `checksums.txt` (the identity is derived from `brand.Repository`, the tag equals the version, the issuer is GitHub OIDC) and the check of the archive against its line. A bundle is stored under its SHA-256 and hashed again before every delivery. A local build gets none, except in development mode with an explicit directory; `myriarchd serve` refuses that flag at start. Any failure leaves the bundle `failed` and delivers nothing.
- A command is rendered from inputs that were checked against allowlists (printable ASCII subset, lengths). Quotation marks including U+2018 to U+201E, the backtick, `$`, `;`, control characters and overlong values are refused with 422. Both shells get single-quote quoting. The response never contains a token.
- The token is never written to logs, error texts, audit details, metric labels or URLs. Only the token ID is logged and counted.

#### Installer

- The values of the plan are data: known keys only, strict patterns, URLs of the origin given with `--server` only, https only, redirects only to https on the same origin. Anything else ends with `plan_invalid` or `https_required`.
- Collector and supervisor are verified as in decision 2. Nothing runs before the check.
- The token reaches the installer through a file and leaves it as a header only.
- Before any action the installer checks the Constrained Language Mode (`language_mode_unsupported`) and foreign installations (`conflicting_install`). After the download it checks that the supervisor file lies at the path of the service (`supervisor_missing`).
- The execution policy is set for the process only (decision 10).

#### Bootstrap and web UI

- The pinned SHA-256 is verified before unpacking and running (`sha256sum -c`, `Get-FileHash`). A difference ends the command with nothing unpacked.
- All steps after the token is written run in a private working directory. On Linux that is a `mktemp -d` of `root`, and `sudo sh -c` receives the values as positional parameters instead of text pasted into the script. On Windows it is a directory below `%ProgramData%` whose DACL admits SYSTEM and Administrators only and is set before the first download. This closes the gap between a check and the use of the file.
- The secret exists only in the dialog. It is never stored, never sent back, never put into a URL, and it is dropped when the dialog closes.

#### Tests on real hosts

- The end-to-end test on Linux and Windows hosts runs the copied command unchanged. It creates the token at run time, masks it in the log and uploads only the files of an allow-list, which never holds the token file or the rendered command. Its workflow is read-only and has no secrets.

### Interface sketch

The sketch fixes the shape and the limits. Once the API exists, `api/openapi.yaml` and [the server design](../design/opamp-server.md) are normative and win over the names below.

| Route | Operation | Who |
|---|---|---|
| `POST /api/v1/install/plan` | `getInstallPlan` | enrollment token |
| `GET /api/v1/install/bundles` | `listInstallBundles` | permission `read` |
| `POST /api/v1/install/commands` | `renderInstallCommands` | permission `read`, with CSRF protection |
| `GET /api/v1/install/bundles/{filename}` | `downloadInstallBundle` | enrollment token |

The artifact bytes use the existing route `/api/v1/artifacts/{id}/content/{filename}` with an install grant.

The plan is `text/plain`: one `key=value` line each, values from the characters `A-Z a-z 0-9 . _ : / @ + = -`, at most 64 lines and 8 KiB, so that bash without `jq` and Windows PowerShell 5.1 can read it. Tools get JSON with `Accept: application/json`. The installer sends the platform, the reported site attributes, an optional fleet hint and an optional wanted collector version as the request body, which holds no secret. The answer of format 1 uses these keys:

| Key | Value |
|---|---|
| `format` | `1`; an installer rejects a format it does not know |
| `fleet` | the name of the fleet the host would join |
| `version.source` | `git`, `ui` or `rollout` |
| `opamp.endpoint` | `wss://` or `https://` URL |
| `collector.version`, `supervisor.version` | version without `v` |
| `collector.url`, `supervisor.url` | artifact URL on the origin of the server; the installer adds `.sha256` and `.sigstore.json` |
| `collector.grant`, `supervisor.grant` | install grant (`myrd_` token) |
| `updater.install` | `true` or `false` |

Error codes of the server: `fleet_mismatch`, `no_fleet`, `fleet_ambiguous`, `rollout_blocking`, `no_target_version` and `preparing`. Error codes of the installers: `plan_failed`, `plan_invalid`, `plan_rejected` (with the code of the server), `plan_unavailable`, `conflicting_install`, `supervisor_missing` and `language_mode_unsupported`. The settings are `install.enabled`, `install.grantTtl`, `install.bundleSourceBaseUrl` (the release source, with the layout `<base>/v<version>/<asset>`) and `install.platforms`.

## Consequences

### Positive

- The operator copies one command from the UI. The version, the endpoint, the bundle and the verifier no longer have to be assembled by hand, and the server renders the command, so it cannot lack an input that the installer needs.
- New hosts follow the Git frame ([ADR-0003](0003-gitops-as-source-of-truth.md)): the plan uses the effective target, and a running rollout treats the new host as a straggler of its last wave.
- Hosts download from `myriarchd` only. Artifacts come through the cache and its mirror settings, and bundles through `install.bundleSourceBaseUrl`, which can point to a mirror. This suits sites without direct internet access. The one remaining outside source of a host is the Sigstore trusted root, which comes from TUF or from a local `trusted_root.json`.
- Beyond the first bundle, integrity does not depend on the server: collector and supervisor code carries the upstream signature and is verified on the host with an identity, issuer and trust root that the server does not control.
- Because the bytes are those of the release, hash rules of App Control and later Authenticode signatures stay valid.
- The Windows start changes no setting of the machine.

### Negative

- The first installation trusts the server that displays the command (trust on first use). The pinned hash shows that the host received the bundle that the server vouched for, not that the server is honest. The manual verified path is the compensation.
- A wrong or compromised server can steer a new host to an older genuine upstream version, one with known vulnerabilities included (a downgrade). The signature proves the origin of an artifact, not that it is current, and this record decides no installer-side floor. A site that cannot accept this installs on the manual path with an explicit `--collector-version`; a minimum that the installer enforces needs a record of its own.
- The default command carries the token as text. Where input is recorded (PowerShell transcription or script block logging, terminal session recording), the record holds a token that stays valid until it is revoked. Such hosts use the variant with the token from a file.
- `myriarchd` gets a new outbound connection (the release source of the bundles) and stores bundles on disk. Both follow the existing outbound policy and the cache limits.
- The server gains four routes and a second kind of download grant. The change needs a control review before it is merged.
- On Windows, `AllSigned` and App Control block the unsigned scripts until the files are signed. A policy from Group Policy overrides the parameter of the process.
- Only enrollment tokens authorize an installation. Hosts that use mutual TLS without a token cannot use the path.
- The first request for a platform makes the server download about 112 MB of artifacts, so the plan can answer `preparing` and the installer waits (15 minutes by default).
- A server built from a development tree serves no bundles. A new token for a host that is enrolled already leads to `identity_conflict`; the administrator action `reenroll` or `opamp.autoRebindOnTokenRotation` resolves it.
- Two paths have to be maintained and tested: the one from the server and the manual one.

## Alternatives considered

- **Only GitHub, with the server showing a command**: the host downloads everything from GitHub, and the server pins URLs and hashes in the command. It needs no bundle store and no new routes, but every host needs internet access or its own mirror for the bundle, the operator still has to supply version and endpoint, and the Git frame does not reach new hosts. It stays possible through the mirror settings of the installers and is the fallback if the bundle store proves too costly.
- **Install codes or one token per host**: a single-use code that the server resolves would keep the long-lived token out of the command. It is a new credential with its own lifetime, storage, revocation, audit and UI, and [ADR-0012](0012-agent-access-via-enrollment-tokens.md) intends one token per channel or region. It needs a record of its own and can follow.
- **Templating the script on the server**: the server would fill in the parameters. It changes the bytes, which breaks hash rules and Authenticode, and it would make the server part of the integrity of the installer.
- **`cosign` as a requirement on every host**: it moves a manual step, with a version floor and on Windows a directory that only administrators can write, to every host. The updater of the verified bundle verifies the same bundles (`verify-bundle`). `cosign` remains a supported verifier.
- **`Bypass` or a machine-wide policy change on Windows**: `Bypass` removes the check that keeps a file with the Mark of the Web from running, and a changed policy outlives the installation and weakens the host for everyone. Group Policy takes precedence over both anyway.
- **Removing foreign packages automatically**: uninstalling a package of someone else is destructive and may remove a collector that the operator relies on. The installer stops and says what to do.
- **The newest upstream version as the default**: it is not the version the installers and blueprints were tested with, it is not deterministic, and its lookup depends on GitHub and its rate limits.
- **The server as a TUF mirror or source of the trust root for hosts**: this would make the server the anchor of the verification that is meant to be independent of it. Hosts without internet access use a local `trusted_root.json`.

## Sources

Checked in October 2026.

- Microsoft Learn, [about execution policies](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_execution_policies) (defaults per Windows edition, the `-ExecutionPolicy` parameter for one session, precedence of the Group Policy scopes) and [App Control script enforcement](https://learn.microsoft.com/en-us/windows/security/application-security/application-control/app-control-for-business/design/script-enforcement).
