# Release build

`goreleaser.yaml` in this directory describes the release build of all four programs for every platform. It holds **no signing
key** and has no `signs:` block: releases are signed keyless by the release workflow
([ADR-0023](../../docs/adr/0023-keyless-release-signing.md)), and the Windows files get an Authenticode signature through the hook
described below ([ADR-0034](../../docs/adr/0034-authenticode-signing-of-windows-release-artifacts.md)). Nothing in this directory publishes anything.

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

* Archives: `myriarchd_<version>_<os>_<arch>.tar.gz`, `myriarch-updater_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows),
  `myrctl_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows) and `myriarch-sim_<version>_<os>_<arch>.tar.gz`: 16 archives.
* The bare updater binaries `myriarch-updater_<version>_<os>_<arch>[.exe]` (the target of `--updater-url` / `-UpdaterUrl`).
* One SBOM per archive, `<archive>.sbom.json`, and `myriarch-webui_<version>.sbom.json` for the web UI (SPDX 2.3 JSON).
* `checksums.txt` lists all of them and also `LICENSE` and `THIRD_PARTY_NOTICES`: 39 entries for a release (16 archives, 4 bare
  binaries, 16 archive SBOMs, the web UI SBOM and the two license files). goreleaser does not copy the two license files into
  `dist/`; the release workflow uploads them from the repository root.
* The Windows scripts (`install-windows.ps1`, `Set-ServiceEnvironment.ps1`) are in the Windows updater archives only.

## Reproducibility

Binaries and archives are identical across two builds of a commit with the same Go toolchain and the same goreleaser, because
timestamps come from the commit (`mod_timestamp`, `{{ .CommitDate }}`), the builds use `-trimpath`, the Windows version resources
are deterministic, and the generated `.syso` files are git-ignored (so Go stamps `vcs.modified=false`).

The SBOMs are not reproducible: syft writes a random `documentNamespace`, the creation time and an entry for its own temporary
directory, even with `SOURCE_DATE_EPOCH`. `checksums.txt` differs between two builds for that reason. The rehearsal compares SBOMs
without these fields.

## SBOMs

`sbom.sh` runs the pinned syft (it checks the version) with `syft.yaml`: no network access, no `--enrich`, `SYFT_CHECK_FOR_APP_UPDATE=false`,
and no `SYFT_*` variable of the caller. `-c` also stops syft from reading a `.syft.yaml` of the working directory. goreleaser
calls the script with the working directory `dist/`.

The web UI SBOM is made from `web/pnpm-lock.yaml` reduced to the production dependencies of the root project
(`ci/release/pnpm-prod-lock.py`): syft cannot drop the dev dependencies of a pnpm lockfile (`javascript.include-dev-dependencies`
has no effect on lockfile version 9), so without the reduction the SBOM would list the test and build tools as well.

## Windows version resources

`windows-signed.sh prepare` runs the pinned go-winres for `myriarch-updater` and `myrctl`: product "Myriarch", file and product
version from the tag, the original file name `<program>.exe` and the copyright line. It writes
`cmd/<program>/rsrc_windows_<arch>.syso`, which Go links into the Windows builds only. The files are git-ignored, are generated
in an empty working directory (no stray icon is picked up), use no manifest (the runtime behavior is unchanged) and are removed by
`ci/build-release.sh`, `ci/release/rehearsal.sh`, `windows-signed.sh clean` and `make clean`.

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
| `WINDOWS_SIGNING` | the mode of the hook: `off` (or `signpath`, `kms`, `test`, see below); the release workflow takes it from `deploy/release/windows-signing.env` |
| `TOOLS_BIN` | where go-winres and osslsigncode are found, default `.cache/bin` |

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
   (`[provider_test_release]`).
3. If nothing is signed, the file in the signed directory must still equal the unsigned build (`[unsigned_changed]`).
4. If signed, the signature is extracted from the provider's file and attached to the packaging job's own build (a signature that
   does not cover that build is `[signature_mismatch]`), and the result is verified with osslsigncode against `ca.pem` and
   `tsa-ca.pem`: exactly one signature (`[signature_count]`), digest SHA-256 (`[digest_weak]`), the expected subject
   (`[subject_mismatch]`), a valid RFC 3161 timestamp (`[timestamp_missing]`, `[timestamp_invalid]`) and a valid chain
   (`[signature_invalid]`). osslsigncode exits 0 for a missing or invalid timestamp as long as the certificate is valid, so the output
   is parsed. The verified file replaces the rebuilt one.

The scripts take the same path. In addition, the text before `# SIG # Begin signature block` must equal the repository file
(`[script_changed]`). The accepted files are staged in `.cache/release/windows/archive/windows/`, from where the Windows archives pick
them up; the tracked scripts are never modified. `windows-signed.sh check-dist` then verifies that `dist/` holds exactly the accepted
files (also in the zip archives and in `checksums.txt`), that the Windows scripts are in the Windows updater archives only, that the
binaries carry the version resources, and that every archive, bare binary, SBOM, `LICENSE` and `THIRD_PARTY_NOTICES` is listed in
`checksums.txt` with the right hash (`[dist_mismatch]`). It also catches a skipped post-build hook (`--skip=post-hooks`).

### Trust anchors

`deploy/release/authenticode/` holds the chain, the timestamp roots and the expected subject
([`authenticode/README.md`](authenticode/README.md)). Until the provider pull request commits them, only unsigned files are accepted.

### Rehearsal

`make release-rehearsal` runs the path above in a snapshot. The signing pass uses a throwaway CA that `ci/release/authenticode-test-ca.sh`
creates in a temporary directory outside the repository (RSA 3072, valid for seven days, no network access) and deletes on exit; the key
is never published and never trusted anywhere else. The timestamp comes from the built-in RFC 3161 time stamping authority of
osslsigncode, so the rehearsal runs offline. The tests of the hook (`ci/tests/windows-hook-test.sh`) cover the failure cases: a changed
byte, a foreign CA, a missing timestamp, a foreign timestamp authority, a wrong subject, a weak digest, an unsigned file that was
changed, and the staging rules.

## Authenticode (Windows)

Authenticode is the signature that Windows evaluates (App Control for Business and AppLocker publisher rules, Smart App
Control, the execution policy for PowerShell scripts). It adds to the Sigstore signature of a release and never replaces it;
with the keyless release workflow ([ADR-0023](../../docs/adr/0023-keyless-release-signing.md)), installers and the updater trust its Sigstore identity. The decision is
[ADR-0034](../../docs/adr/0034-authenticode-signing-of-windows-release-artifacts.md).

**Target design.**

* Signed: `myriarch-updater.exe` and `myrctl.exe` for amd64 and arm64, `install-windows.ps1` and `Set-ServiceEnvironment.ps1`,
  with SHA-256 and an RFC 3161 timestamp, and a Windows version resource (product name, file and product version) on both
  executables.
* The signer is the SignPath Foundation (free for open source). The release workflow submits the unsigned files with a short
  `curl` client from one job of the `release` environment; SignPath fetches them from the run and a person approves every
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
