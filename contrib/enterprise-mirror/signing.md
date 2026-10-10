# Key-based signatures for mirrors

The project's releases carry no key-based signatures: they are signed keyless by the release workflow
([ADR-0023](../../docs/adr/0023-keyless-release-signing.md)). This page is for organizations that **mirror releases and re-sign
them with their own key** (see the [toolkit README](README.md)): it describes the signature format that `sign-blob.sh`,
`verify-release.sh` and `publish-mirror.sh` agree on, and how to handle the key. The host installers no longer take a key: they
verify the keyless bundle of the release ([Verify releases](../../docs/security/verify-releases.md)).

## Signature format contract

For `checksums.txt`, every bare binary and every archive of a mirrored release there is a `<file>.sig` next to it:

* Content: **base64** (one line) of an **ASN.1 DER ECDSA signature** (P-256) over the **SHA-256** of the file.
* Verify without cosign:

  ```bash
  openssl dgst -sha256 -verify cosign.pub -signature <(openssl base64 -d -A -in checksums.txt.sig) checksums.txt   # -> Verified OK
  ```

* Verify with cosign: `cosign verify-blob --key cosign.pub --signature checksums.txt.sig --insecure-ignore-tlog=true checksums.txt`.
* A whole release (mirror or artifact repository):
  `contrib/enterprise-mirror/verify-release.sh --public-key cosign.pub --base-url <url> --assets`.

The contract, not a cosign version, is binding. `sign-blob.sh` creates the signature with the pinned cosign version
(`ci/versions.env`) and re-verifies it with `openssl` immediately; if a later cosign drops the deprecated flags
(`--output-signature`), it switches to bundle mode automatically and reads the signature from the bundle.

## Generating the key pair (once, by the organization that runs the mirror)

On a trusted machine, not in CI:

```bash
export COSIGN_PASSWORD='<long passphrase>'            # keep it out of the shell history: read it from a password manager
cosign generate-key-pair                              # writes cosign.key (encrypted) and cosign.pub
```

1. Keep `cosign.pub` where the mirror scripts find it (`COSIGN_PUBLIC_KEY`, or `deploy/release/cosign.pub` in your fork).
2. Store the secrets in the secret store of the system that signs, so that only the signing job can read them (not test jobs,
   which run third-party code, and not jobs on ordinary branches):
   * `COSIGN_KEY` - the content or path of `cosign.key` (the alias `COSIGN_PRIVATE_KEY` is accepted too)
   * `COSIGN_PASSWORD` - the passphrase, masked
3. Back up `cosign.key` offline (safe, stored separately from the passphrase) and delete it from the machine.
4. Before every signing run `sign-blob.sh` checks that the key matches the public key (a wrong or rotated secret aborts instead of
   producing a wrong signature).

Getting the key to hosts: publish `cosign.pub` next to the mirrored files (`.../v<ver>/cosign.pub`); for a host's trust base verify
the **fingerprint over a second channel** (`openssl pkey -pubin -in cosign.pub -outform DER | openssl dgst -sha256`).

## Rotation

1. Generate a new key pair (as above), publish the new `cosign.pub`, replace the secrets.
2. From the next mirrored release on the new key signs. The installers no longer take a key (`--signing-key`/`-SigningKey` were
   removed), so the key only matters for files that you verify yourself, for example with `verify-release.sh`; distribute the new
   `cosign.pub` to the systems that run that check.
3. Old releases stay verifiable with the old key (keep the old `cosign.pub`, for example as `cosign-<year>.pub` next to the
   release files).
4. Compromise: delete the secrets immediately, rotate the key, withdraw the affected releases (remove the published files) and
   re-sign them; re-assess hosts that were installed with the compromised key.

## A KMS-held key

A key in a secret store is acceptable for a start; for production it belongs in a KMS so that the private key never leaves it. Any
KMS that cosign supports works (AWS KMS, Google Cloud KMS, Azure Key Vault, HashiCorp Vault).

* Create an ECDSA P-256 signing key in the KMS and allow the signing job the sign operation only (no get/export).
* Set `COSIGN_KEY` to the KMS URI (`awskms://...`, `gcpkms://...`, `azurekms://...`, `hashivault://...`) instead of a key file;
  the scripts stay the same, `COSIGN_PASSWORD` is not needed. Authenticate the job with the provider's workload identity.
* `cosign public-key --key <kms-uri> > cosign.pub` and publish it.
* Not exercised by the project's own tests (they use key files).

## Authenticode with a private PKI

The Windows files of a release are signed by the project (Authenticode, see [`deploy/release/README.md`](../../deploy/release/README.md)).
An organization with a private PKI that wants its own signature on `myriarch-updater.exe` re-signs the file with `osslsigncode`
(the version pinned in `ci/versions.env`, `ci/install-tools.sh osslsigncode`):

```bash
osslsigncode sign -pkcs12 <pfx> -readpass <password-file> -h sha256 -ts <timestamp-url> -in <file> -out <file.signed>
```

The hash of the re-signed file differs from the release, so the mirror creates its own `checksums.txt` for the re-signed set and
signs it with its own key (`publish-mirror.sh` and `sign-blob.sh`). The installer can enforce the signature with
`-UpdaterSignerThumbprint`.
