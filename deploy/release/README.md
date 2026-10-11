# Release build

`goreleaser.yaml` in this directory describes the release build of all four programs for every platform. It holds **no signing
key** and has no `signs:` block: releases are signed keyless by the release workflow
([ADR-0023](../../docs/adr/0023-keyless-release-signing.md), [ADR-0035](../../docs/adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)),
and the Windows files get an Authenticode signature through the hook described below
([ADR-0034](../../docs/adr/0034-authenticode-signing-of-windows-release-artifacts.md)). Nothing in this directory publishes anything.
How a maintainer releases is in [`docs/releasing.md`](../../docs/releasing.md); how users verify a release is in
[`docs/security/verify-releases.md`](../../docs/security/verify-releases.md).

## Commands

| Command | What it does |
|---|---|
| `make release-snapshot` (`ci/build-release.sh snapshot`) | A goreleaser snapshot of the whole matrix, unsigned, for a local test. Windows files stay as built (stage `off`). |
| `make release-rehearsal` (`ci/release/rehearsal.sh`) | The path of a release in a snapshot: stage `collect`, one signing pass with a throwaway test CA, stage `apply`, checks of `dist/` and a comparison of both builds. |

Prerequisites: the built web UI (`make web-build`; the release build never runs Node), the tools of `ci/install-tools.sh` (goreleaser,
syft, go-winres and yq; the commands install them), python3, and for the rehearsal `osslsigncode`, which is built from a pinned
source archive and needs a C compiler with the OpenSSL 3 and zlib headers (`docs/development.md`, section on tools). Releases are
built only by the release workflow; `ci/build-release.sh release` does not exist any more.

## Assets

`assets.json` in this directory is the complete list of the files of a release, with `{version}` for the version without `v`. A
release has **42 assets**: the 40 files that the build produces and two signature files that the release workflow adds.
`ci/release/stage-assets.sh` and `ci/release/verify-dist.sh` read the list, so a file that is missing or unexpected fails the
build, the verification and the publication.

| Kind | Files | Count |
|---|---|---|
| archive | `myriarchd_<version>_<os>_<arch>.tar.gz`, `myriarch-updater_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows), `myrctl_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows) and `myriarch-sim_<version>_<os>_<arch>.tar.gz` | 16 |
| binary | the bare updater binaries `myriarch-updater_<version>_<os>_<arch>[.exe]` (the target of `--updater-url` / `-UpdaterUrl`) | 4 |
| sbom | one SBOM per archive, `<archive>.sbom.json`, and `myriarch-webui_<version>.sbom.json` for the web UI (SPDX 2.3 JSON) | 17 |
| license | `LICENSE` and `THIRD_PARTY_NOTICES` | 2 |
| checksums | `checksums.txt` | 1 |
| signature | `checksums.txt.sigstore.json`, the keyless cosign bundle over `checksums.txt` | 1 |
| provenance | `myriarch_<version>_provenance.intoto.jsonl`, the GitHub build provenance of the 39 files | 1 |

* The first five kinds (40 files) are the stage `unsigned` of `verify-dist.sh`; with the last two it is the stage `final`.
* `checksums.txt` lists exactly 39 entries: the archives, the bare binaries, the SBOMs and the two license files. It does not
  list itself, its bundle or the provenance file. goreleaser does not copy the two license files into `dist/`;
  `stage-assets.sh` copies them from the repository root.
* The Windows scripts (`install-windows.ps1`, `Set-ServiceEnvironment.ps1`) are in the Windows updater archives only.
* The release workflow publishes a draft with all 42 files and no other.

## Reproducibility

Binaries and archives are identical across two builds of a commit with the same Go toolchain and the same goreleaser, because
timestamps come from the commit (`mod_timestamp`, `{{ .CommitDate }}`), the builds use `-trimpath`, the Windows version resources
are deterministic, and the generated `.syso` files are git-ignored (so Go stamps `vcs.modified=false`).

The SBOMs are not reproducible: syft writes a random `documentNamespace` and the creation time, even with `SOURCE_DATE_EPOCH`.
`checksums.txt` differs between two builds for that reason. The rehearsal compares SBOMs without these two fields. `sbom.sh` removes the
file entry that syft adds for its own temporary extraction directory of an archive (it names the build host's `TMPDIR` and is not in the
archive) together with the relationships that name it.

## SBOMs

`sbom.sh` runs the pinned syft (it checks the version) with `syft.yaml`: no network access, no `--enrich`, `SYFT_CHECK_FOR_APP_UPDATE=false`,
and no `SYFT_*` variable of the caller. `-c` also stops syft from reading a `.syft.yaml` of the working directory. goreleaser
calls the script with the working directory `dist/`.

goreleaser starts an `sboms` command with a reduced environment: from its own environment it passes only variables of the
`HOME`, `USER`, `TMPDIR` and `PATH` kind, plus `sboms[].env`. Hooks, in contrast, get the whole environment. `goreleaser.yaml`
therefore hands `TOOLS_BIN` to both `sboms` entries (`env: ["TOOLS_BIN={{ .Env.TOOLS_BIN }}"]`), and `sbom.sh` finds syft and yq
only in `${TOOLS_BIN}`: it stops with `[tools_bin_missing]` when the variable is not set and does not use `PATH` or the default
directory. The release workflow keeps the pinned tools out of `PATH` on purpose (it never writes `GITHUB_PATH`), so this is the only way
they reach the script. A new `sboms` entry that calls a pinned tool needs the same `env` line; `ci/tests/release-workflow-test.sh`
checks it, together with the `TOOLS_BIN` of every workflow step that uses a pinned tool, and `ci/tests/release-scripts-test.sh` scans the
release scripts for a pinned tool called by its bare name or looked up with `have` or `need_cmd`.

The web UI SBOM is made from `web/pnpm-lock.yaml` reduced to the production dependencies of the root project
(`ci/release/pnpm-prod-lock.py`): syft cannot drop the dev dependencies of a pnpm lockfile (`javascript.include-dev-dependencies`
has no effect on lockfile version 9), so without the reduction the SBOM would list the test and build tools as well.

## Windows version resources

`windows-signed.sh prepare` runs the pinned go-winres for `myriarch-updater` and `myrctl`: product "Myriarch", file and product
version from the tag, the original file name `<program>.exe` and the copyright line. It writes
`cmd/<program>/rsrc_windows_<arch>.syso`, which Go links into the Windows builds only. The files are git-ignored, are generated
in an empty working directory (no stray icon is picked up), use no manifest (the runtime behavior is unchanged) and are removed by
`ci/build-release.sh`, `ci/release/rehearsal.sh`, `windows-signed.sh clean`, `make release` (at its start) and `make clean`. A direct
`goreleaser` call, as in the release workflow, leaves them in place; run `windows-signed.sh clean` before any other Windows build in the
same work tree, because Go links whatever `rsrc_windows_*.syso` it finds.

## Windows signing hook

The build cannot sign: it runs without a key, and the signature comes from a provider after the unsigned files exist. The hook
splits the release into three steps that may run in different jobs, with the files travelling as plain directories.

### Stages

`MYRIARCH_WINDOWS_STAGE` is required; unset or any other value fails.

| Stage | Who runs it | What the hooks do |
|---|---|---|
| `off` | `make release-snapshot`, snapshots only | Nothing is collected or checked. A release build refuses it (`[stage_off_release]`). |
| `collect` | the build job: `goreleaser build --id myriarch-updater --id myrctl`, then `windows-signed.sh seal` | The post-build hook copies the four `.exe` files to the unsigned directory; `seal` adds the two scripts and `SHA256SUMS`. |
| `apply` | the packaging job: `goreleaser release --skip=publish`, then `windows-signed.sh check-dist` | The post-build hook accepts the four `.exe` files and the before hook the two scripts from the signed directory (acceptance check below). |

Between the stages the sign job runs `ci/release/windows-sign-hook.sh`. `windows-signed.sh check-dist` follows a goreleaser run in
`collect` or `apply` and checks `dist/` (below). The build and packaging jobs have to use the same Go toolchain and the same
goreleaser; otherwise stage `apply` fails with `[not_reproducible]`.

### Environment

| Variable | Meaning |
|---|---|
| `MYRIARCH_WINDOWS_STAGE` | `off`, `collect` or `apply` |
| `MYRIARCH_WINDOWS_UNSIGNED_DIR` | the unsigned set, default `.cache/release/windows-unsigned` |
| `MYRIARCH_WINDOWS_SIGNED_DIR` | the signed set, default `.cache/release/windows-signed` |
| `MYRIARCH_AUTHENTICODE_DIR` | the trust anchors, default `deploy/release/authenticode`; only snapshot builds may set it |
| `MYRIARCH_WINDOWS_SIGNING` | stage `apply`: the configured signing mode (`off`, `signpath` or `kms`), parsed by the release workflow from `deploy/release/windows-signing.env` and set in the packaging job. Required for release builds (`[mode_missing]`); `signer.json` has to name the same provider (`[provider_mismatch]`). Snapshots may leave it unset. |
| `WINDOWS_SIGNING` | the mode of the hook: `off` (or `signpath`, `kms`, `test`, see below); the release workflow takes it from `deploy/release/windows-signing.env` |
| `TOOLS_BIN` | where the pinned tools are found (go-winres, osslsigncode, syft and yq here), default `.cache/bin`; `sbom.sh` has no default. go-winres is accepted only with the stamp of the pinned version next to it (`[winres_missing]`) |

The unsigned and signed directories are staging areas: inside the work tree they have to be git-ignored (use `.cache/` or a path
outside the repository), otherwise `[staging_dir]`. The state of the hooks is in `.cache/release/windows`.

### Hook contract

`WINDOWS_SIGNING=<mode> ci/release/windows-sign-hook.sh --in <unsigned-dir> --out <signed-dir>`

* **Input:** the four files `myriarch-updater_windows_amd64.exe`, `myriarch-updater_windows_arm64.exe`,
  `myrctl_windows_amd64.exe` and `myrctl_windows_arm64.exe`, plus `install-windows.ps1`, `Set-ServiceEnvironment.ps1` and
  `SHA256SUMS` (`sha256sum` format, sorted by name). Anything else is `[set_incomplete]`, a wrong hash `[sums_mismatch]`.
* **Output:** the same seven names (`SHA256SUMS` over the outputs) plus `signer.json`. The output directory has to be new or empty.
* **Modes:** `off` copies the files unchanged. `signpath` and `kms` fail with `[provider_unavailable]` until the provider pull request
  adds the clients. `test` signs with a throwaway CA from `WINDOWS_SIGNING_TEST_DIR` and exists for the rehearsal; it is not a
  committed mode and is refused on tags (`GITHUB_REF`, `GITHUB_REF_TYPE`) and in `release.yml` (`GITHUB_WORKFLOW_REF`) with
  `[test_mode_on_tag]`. An unset mode is `[mode_missing]`, an unknown one `[mode_invalid]`.
* The hook never reads `windows-signing.env` itself.

`signer.json` (schema 1, keys sorted, indent 2) is read by stage `apply` and by the release notes:

```json
{
  "files": {
    "Set-ServiceEnvironment.ps1": {"sha256": "<hex>", "unsigned_sha256": "<hex>", "subject": "...", "issuer": "...", "serial": "...", "timestamp": "..."},
    "install-windows.ps1": { "...": "the same fields" },
    "myrctl_windows_amd64.exe": { "...": "" },
    "myrctl_windows_arm64.exe": {},
    "myriarch-updater_windows_amd64.exe": {},
    "myriarch-updater_windows_arm64.exe": {}
  },
  "provider": "off",
  "schema": 1,
  "signed": false,
  "subject": null
}
```

`provider` is `off`, `test`, `signpath` or `kms`; `off` and `signed: false` belong together, and then `subject` is `null` and the
per-file entries have only `sha256` and `unsigned_sha256`. When signed, `subject` is the RFC 2253 subject of the signing certificate,
and every file entry adds `subject`, `issuer`, `serial` and `timestamp`.

### Acceptance check (stage `apply`)

A provider's files are not shipped as they are. For every `.exe` the post-build hook does the following (`ci/release/authenticode.py`,
the only code that runs osslsigncode):

1. The rebuilt file must equal the unsigned build (`SHA256SUMS` of the unsigned directory), else `[not_reproducible]`.
2. `signer.json` is validated against both sets (`[signer_json]`); a `test` provider is refused in release builds
   (`[provider_test_release]`). In a release build `MYRIARCH_WINDOWS_SIGNING` has to be set and equal to the provider in
   `signer.json` (`[mode_missing]`, `[provider_mismatch]`), so a hook cannot turn a signed release into an unsigned one.
3. If nothing is signed, the file in the signed directory must still equal the unsigned build (`[unsigned_changed]`).
4. If signed, the signature is extracted from the provider's file and attached to the packaging job's own build (a signature that
   does not cover that build is `[signature_mismatch]`), and the result is verified with osslsigncode against `ca.pem` and
   `tsa-ca.pem`: exactly one signature (`[signature_count]`), digest SHA-256 (`[digest_weak]`), the expected subject
   (`[subject_mismatch]`), a valid RFC 3161 timestamp (`[timestamp_missing]`, `[timestamp_invalid]`) and a valid chain
   (`[signature_invalid]`). osslsigncode exits 0 for a missing or invalid timestamp as long as the certificate is valid, so the output
   is parsed. The verified file replaces the rebuilt one.

The scripts take the same path. In addition, the text before `# SIG # Begin signature block` must equal the repository file
(`[script_changed]`). The signature block of a provider's script has a defined format that is checked before osslsigncode reads the
file (on `accept-scripts` and on `verify`): the begin marker on a line of its own, then one or more lines that consist of `# ` followed
by base64 characters only (`A-Z a-z 0-9 + /`; the padding `=`, up to two, only at the end of the last line), then the end marker at
the end of the file, every line ended by LF or CRLF (exactly one CR before the LF). Any other byte, such as a space or tab at the end of
a line, a second CR, an empty line, padding in the middle of the stream, or text after a marker, is refused with `[signature_malformed]`,
and the provider's bytes are never normalized. A block with LF line endings is accepted: the signature is extracted from it and attached
to the repository file. The check applies to `.ps1`, `.psd1` and `.psm1` files, by the extension in any case (osslsigncode chooses its
script parser the same way); `authenticode.py verify` refuses the script formats with another comment syntax (`.ps1xml`, `.psc1`,
`.cdxml`, `.mof`, `.js`) with `[script_unsupported]`. This is what Set-AuthenticodeSignature, signtool and SignPath
write; the check exists because the base64 decoder inside osslsigncode is lenient in a way that depends on the OpenSSL build, so the
same file would otherwise be accepted on one host and refused on another. The accepted files are staged in `.cache/release/windows/archive/windows/`, from where the Windows archives pick
them up; the tracked scripts are never modified. `windows-signed.sh check-dist` then verifies that `dist/` holds exactly the accepted
files (also in the zip archives and in `checksums.txt`), that the Windows scripts are in the Windows updater archives only, that the
binaries carry the version resources, and that every archive, bare binary, SBOM, `LICENSE` and `THIRD_PARTY_NOTICES` is listed in
`checksums.txt` with the right hash (`[dist_mismatch]`). It also catches a skipped post-build hook (`--skip=post-hooks`).

### Trust anchors

`deploy/release/authenticode/` holds the chain, the timestamp roots and the expected subject
([`authenticode/README.md`](authenticode/README.md)). Until the provider pull request commits them, only unsigned files are accepted.

### Rehearsal

The rehearsal reaches the pinned tools the way the release workflow does: `ci/release/rehearsal.sh` (and `ci/build-release.sh`, `make
release-snapshot`) link goreleaser, syft, go-winres, yq and osslsigncode into a temporary directory, make it `TOOLS_BIN` and take `.cache/bin`
off `PATH` (`isolate_tools` in `ci/lib.sh`). A script, or a goreleaser `sboms` entry, that looks for a tool on `PATH` or in the default
directory therefore fails in the rehearsal, which the CI job `build` runs, and not only in the release.

`make release-rehearsal` runs the path above in a snapshot. The signing pass uses a throwaway CA that `ci/release/authenticode-test-ca.sh`
creates in a temporary directory outside the repository (RSA 3072, valid for seven days, no network access) and deletes on exit; the key
is never published and never trusted anywhere else. The timestamp comes from the built-in RFC 3161 time stamping authority of
osslsigncode, so the rehearsal runs offline. The tests of the hook (`ci/tests/windows-hook-test.sh`) cover the failure cases: a changed
byte, a foreign CA, a missing timestamp, a foreign timestamp authority, a wrong subject, a weak digest, an unsigned file that was
changed, a provider file that differs only in a region that no signature covers (the PE checksum, whitespace in a script's signature
block; the staged file is still the job's own build with the signature attached), a nested second signature, a mode that differs from
`signer.json`, and the staging rules.

## How the release workflow uses this build

`.github/workflows/release.yml` ([ADR-0035](../../docs/adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md))
connects to the build through these points. The jobs hand directories to each other as tar files that are checked by artifact ID
and SHA-256 (`ci/release/artifact.sh`).

| Job | Step of the build |
|---|---|
| `ui` | `make web-build`, then the release gates (below). The release build never runs Node.js. |
| `build-windows` | `goreleaser build --clean --id myriarch-updater --id myrctl` with `MYRIARCH_WINDOWS_STAGE=collect`, then `windows-signed.sh seal`. |
| `sign-windows` | `ci/release/windows-sign-hook.sh` with `WINDOWS_SIGNING` from `windows-signing.env` (job with the first approval). |
| `package` | `goreleaser release --clean --skip=publish` with `MYRIARCH_WINDOWS_STAGE=apply` and `MYRIARCH_WINDOWS_SIGNING`, then `windows-signed.sh check-dist`, then `ci/release/stage-assets.sh`, which copies the 40 files under their upload names and ends with `ci/release/verify-dist.sh --stage unsigned`. |
| `sign-blobs`, `draft`, `verify` | `checksums.txt.sigstore.json` and the provenance file are added to the 40 files; `verify-dist.sh --stage final` checks all 42. |

Points that the build and the workflow depend on:

* **Mode binding.** The workflow parses `windows-signing.env` as data (in the job `guard`, `ci/release/meta.sh`), passes the mode
  to the sign job as `WINDOWS_SIGNING` and to the packaging job as `MYRIARCH_WINDOWS_SIGNING`. Without the second variable a
  release build fails, and `signer.json` has to name the configured provider.
* **Release gates.** `ci/build-release.sh` and goreleaser do not run `ci/check-release-deps.sh`, `ci/check-release-licenses.sh` or
  `scripts/gen-third-party-notices.sh --check`. The job `ui` of the release workflow runs them before anything is packaged. They
  also run on every commit in the gates `go` and `licenses` of `ci-ok`, which the release guard requires on the tagged commit.
* **No UI in the `before:` hook.** The check for `internal/webui/dist/index.html` is a pre-build hook of `myriarchd` only. The `before:`
  hook runs `windows-signed.sh prepare`, because the job that builds the Windows files (stage `collect`) builds without the web UI.
* **Direct goreleaser calls** leave the generated `rsrc_windows_*.syso` files in place; see the section on version resources.
* **The rehearsal covers the staging.** `make release-rehearsal` runs `ci/release/stage-assets.sh` after the second pass, so every
  pull request checks `assets.json` against the real goreleaser output.

## Release notes template

`notes.md.tmpl` is the template of the release notes. `ci/release/notes.sh` renders it in the job `draft` with the tag, the
commit, the image digests, the Windows signing state (`signer.json`) and the section of `CHANGELOG.md`: "Unreleased" for a
release candidate, `## [X.Y.Z] - YYYY-MM-DD` for a final release.

* The leading `#` lines (license header and a comment) are dropped. Blocks `<!-- if NAME -->` ... `<!-- end NAME -->` with the names
  `rc`, `final`, `unsigned`, `unsigned-exception` and `signed` show text only when they apply; they do not nest and every block
  must be closed.
* Placeholders have the form `{{NAME}}`; an unknown placeholder or a leftover `{{` fails the rendering. The complete set:
  `TAG`, `VERSION`, `BASE_VERSION`, `COMMIT`, `REPOSITORY`, `IDENTITY` (the exact certificate identity of the tag),
  `IDENTITY_REGEXP` (the pattern of ADR-0029), `IDENTITY_BOUND_REGEXP` (the pattern bound to the version, as the installers use it),
  `OIDC_ISSUER`, `SIGNER_WORKFLOW`, `SERVER_IMAGE`, `UPDATER_IMAGE`, `SERVER_DIGEST`, `UPDATER_DIGEST`, `IMAGE_TAGS`,
  `PROVENANCE_ASSET`, `UPDATER_ASSET_LINUX_AMD64`, `CHANGELOG_SECTION`, `CHANGELOG_URL`, and, inside `signed` only,
  `WINDOWS_SIGNER_SUBJECT` and `WINDOWS_SIGNER_PROVIDER`.
* The notes carry the banners (release candidate; Windows files unsigned, with the sentence about the reviewed exception for a
  final tag), the support levels, the known limitations, the image table, the commands of
  [Verify releases](../../docs/security/verify-releases.md) with the exact tag, identity and digests, and the changelog section.
  GitHub appends the generated list of merged pull requests. It starts at the previous tag only when that tag has a published release
  (`draft.sh`); a candidate that follows one that failed before publication leaves the start to GitHub.
* The rendered text must be at most 100,000 characters and must not contain `SPDX-License-Identifier`.

## Authenticode (Windows)

Authenticode is the signature that Windows evaluates (App Control for Business and AppLocker publisher rules, Smart App
Control, the execution policy for PowerShell scripts). It adds to the Sigstore signature of a release and never replaces it;
with the keyless release workflow ([ADR-0023](../../docs/adr/0023-keyless-release-signing.md), [ADR-0035](../../docs/adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)),
installers and the updater trust its Sigstore identity. The decision is
[ADR-0034](../../docs/adr/0034-authenticode-signing-of-windows-release-artifacts.md).

**Target design.**

* Signed: `myriarch-updater.exe` and `myrctl.exe` for amd64 and arm64, `install-windows.ps1` and `Set-ServiceEnvironment.ps1`,
  with SHA-256 and an RFC 3161 timestamp, and a Windows version resource (product name, file and product version) on both
  executables.
* The signer is the SignPath Foundation (free for open source). The release workflow submits the unsigned files with a short
  `curl` client from the job `sign-windows`, which uses the `release` environment; SignPath fetches them from the run and a person approves every
  release there. The only secret is a SignPath API token that can only submit requests. If SignPath declines or does not decide
  within four weeks, the fallback is an individual-validation certificate with its key in a Google Cloud KMS HSM, used through
  GitHub OIDC and `jsign` (no secret); the maintainer decides before anything is bought, within a budget of US$700 for the
  first year, and the legal name and locality appear in every signature.
* No PFX file and no long-lived signing secret in the release workflow.

**v0.1.0 is unsigned.** The Windows files of `v0.1.0` carry no Authenticode signature, under a reviewed exception for exactly
that tag (`WINDOWS_UNSIGNED_FINAL_TAG` in `deploy/release/windows-signing.env`). Verify them with cosign before you run
them; the release notes say so.

**In this tree.** The build holds no key. The signing hook contract, the Windows version resources and the acceptance check are
described above; the clients of the providers and the trust anchors in `authenticode/` arrive with the provider change, until then only the
modes `off` and `test` work and only unsigned files are accepted. The installer option `-UpdaterSignerThumbprint` pins the leaf
certificate of the updater for operators with a private PKI; it breaks at every certificate renewal (at most every 460 days).

## Key-based signatures (enterprise mirrors)

The project's releases carry no key-based `.sig` files. The format contract of those signatures and the handling of keys and rotation
moved to [`contrib/enterprise-mirror/signing.md`](../../contrib/enterprise-mirror/signing.md) for organizations that mirror releases
and re-sign them.
