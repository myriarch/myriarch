# Release signing (cosign with a key)

> **Interim.** Releases are currently signed with a **cosign key pair** (ECDSA P-256). Upstream releases of the project
> switch to **keyless Sigstore signing** in the GitHub release workflow (ADR-0023). The key-based toolkit
> stays available in [`contrib/enterprise-mirror/`](../../contrib/enterprise-mirror) for organizations that mirror releases
> and re-sign them with their own keys.

Releases are signed with a cosign key pair (ECDSA P-256) **without** an entry in a public transparency log (no repository or
workflow identities in the public Rekor, verification also works offline). The public key belongs into this directory as
**`deploy/release/cosign.pub`**; as long as it is missing, release builds abort (`ci/build-release.sh`) and hosts cannot
verify anything. There is deliberately no placeholder file.

## Signature format contract

For `checksums.txt`, every bare binary and every archive of a release there is a `<file>.sig` next to it:

* Content: **base64** (one line) of an **ASN.1 DER ECDSA signature** (P-256) over the **SHA-256** of the file.
* Verify without cosign:

  ```bash
  openssl dgst -sha256 -verify cosign.pub -signature <(openssl base64 -d -A -in checksums.txt.sig) checksums.txt   # -> Verified OK
  ```

* Verify with cosign: `cosign verify-blob --key cosign.pub --signature checksums.txt.sig --insecure-ignore-tlog=true checksums.txt`.
* A whole release (mirror or artifact repository):
  `contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --base-url <url> --assets`.

The contract, not a cosign version, is binding. `contrib/enterprise-mirror/sign-blob.sh` creates the signature with the pinned
cosign version (`ci/versions.env`) and re-verifies it with `openssl` immediately; if a later cosign drops the deprecated
flags (`--output-signature`), it switches to bundle mode automatically and reads the signature from the bundle.

## Generating the key pair (once, by the people who own the release)

On a trusted machine, not in CI:

```bash
export COSIGN_PASSWORD='<long passphrase>'            # keep it out of the shell history: read it from a password manager
cosign generate-key-pair                              # writes cosign.key (encrypted) and cosign.pub
```

1. Commit `cosign.pub` as `deploy/release/cosign.pub` (reviewed change).
2. Store the secrets in the secret store of the release system so that **only the release job on protected tags** can read
   them (not the test jobs, which run third-party code such as npm packages, and not jobs on ordinary branches):
   * `COSIGN_KEY` - the content or path of `cosign.key` (the alias `COSIGN_PRIVATE_KEY` is accepted too)
   * `COSIGN_PASSWORD` - the passphrase, masked
   * for a private PKI only: `AUTHENTICODE_PFX` and `AUTHENTICODE_PFX_PASSWORD_FILE` (see "Authenticode" below)
3. Back up `cosign.key` offline (safe, stored separately from the passphrase) and delete it from the machine. Protect the tags
   `v*` (maintainers only), otherwise the secrets are not available to the release job. The tagged commit must be on the
   release branch (`ci/build-release.sh` checks this, see `RELEASE_BRANCH` in `ci/lib.sh`): a protected tag only restricts who
   may create it, not which commit it points to.
4. Before every signing run the script checks that the key matches `deploy/release/cosign.pub` (a wrong or rotated secret
   aborts instead of producing a wrong signature).

Getting the key to hosts: `cosign.pub` is also published next to the release files (`.../v<ver>/cosign.pub`); for a host's
trust base verify the **fingerprint over a second channel**
(`openssl pkey -pubin -in cosign.pub -outform DER | openssl dgst -sha256`).

## Rotation

1. Generate a new key pair (as above), commit the new `cosign.pub` in a reviewed change, replace the secrets.
2. From the next release on the new key signs. The installers no longer take a key (`--signing-key`/`-SigningKey` were
   removed; they verify the keyless bundle of the release, see
   [Verify releases](../../docs/security/verify-releases.md)), so the key only matters for files that you verify
   yourself, for example with `contrib/enterprise-mirror/verify-release.sh`.
3. Old releases stay verifiable with the old key (keep the old `cosign.pub`, for example as `cosign-<year>.pub` next to the
   release files).
4. Compromise: delete the secrets immediately, rotate the key, withdraw the affected releases (remove the published files and
   the release page entry) and rebuild them; re-assess hosts that were installed with the compromised key.

## Target before production: a KMS-held key

A key in a secret store is accepted for the early phase; before production it belongs in a KMS so that the private key never
leaves it. Any KMS that cosign supports works (AWS KMS, Google Cloud KMS, Azure Key Vault, HashiCorp Vault); the project does
not depend on a single vendor.

* Create an ECDSA P-256 signing key in the KMS and allow the release job the sign operation only (no get/export).
* Set `COSIGN_KEY` to the KMS URI (`awskms://...`, `gcpkms://...`, `azurekms://...`, `hashivault://...`) instead of a key file;
  the scripts stay the same (`contrib/enterprise-mirror/sign-blob.sh`), `COSIGN_PASSWORD` is not needed. Authenticate the job
  with the provider's workload identity (or the provider's usual credentials as protected secrets).
* `cosign public-key --key <kms-uri> > deploy/release/cosign.pub` and commit it.
* Not exercised by the project's own tests (they use key files).

## Authenticode (Windows)

Authenticode is the signature that Windows evaluates (App Control for Business and AppLocker publisher rules, Smart App
Control, the execution policy for PowerShell scripts). It adds to the Sigstore signature of a release and never replaces it;
installers and the updater trust the Sigstore identity of the release workflow. The decision is
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

**Status of this tree.** The settings file, the signing hook contract, the version resources and the acceptance check arrive
with the release build change. Until then `authenticode.sh` signs with a PFX file and a password file, and
`ci/build-release.sh` still requires `AUTHENTICODE_PFX` in release mode. A public certificate authority cannot deliver such a
file, so this path is for private PKIs only (see `contrib/enterprise-mirror/`), with these settings: `AUTHENTICODE_PFX` (file),
`AUTHENTICODE_PFX_PASSWORD_FILE` (file) and optionally `AUTHENTICODE_TIMESTAMP_URL`. `AUTHENTICODE_CA_FILE` and
`AUTHENTICODE_TSA_CA_FILE` (default: `EXTRA_CA_FILE`) name the trust anchors of the verification step. The installer option
`-UpdaterSignerThumbprint` pins the leaf certificate of the updater for operators with a private PKI; it breaks at every
certificate renewal (at most every 460 days).

## Outlook: keyless signing

Releases of the project move to keyless Sigstore signing (ADR-0023): the GitHub release workflow signs with its workflow
identity, and verification checks that identity instead of a long-lived key. The key-based toolkit remains in
[`contrib/enterprise-mirror/`](../../contrib/enterprise-mirror).
