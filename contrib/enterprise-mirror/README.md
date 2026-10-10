# Enterprise mirror toolkit

Scripts for organizations that **mirror releases into a private registry or artifact repository and re-sign them with their
own keys** (for example behind a proxy, without access to GitHub, or with their own PKI). The toolkit is key-based:
signatures follow the format contract in [`deploy/release/README.md`](../../deploy/release/README.md) and are checked with
`openssl` (cosign is optional).

Until the keyless release pipeline ([ADR-0023](../../docs/adr/0023-keyless-release-signing.md)) is in place, the interim
key-based release path (`ci/build-release.sh`, `deploy/release/goreleaser.yaml`) signs with `sign-blob.sh` and cross-checks with
`verify-release.sh` from this directory. After that, the project's releases are signed keyless with Sigstore, and this toolkit
serves only operators who mirror and re-sign releases.

## Mirroring keyless releases

The installers (`install-linux.sh`, `install-windows.ps1`) authenticate the updater through the **keyless Sigstore bundle of the
project's release**, not through a key of the mirror. A mirror therefore has to carry the release files **unchanged**,
including `checksums.txt` and `checksums.txt.sigstore.json`, below the same directory (`<base>/v<version>/`):

1. Verify the release before you publish it: `cosign verify-blob` of `checksums.txt` with its bundle and the exact identity,
   then `sha256sum -c`. The commands are in [Verify releases](../../docs/security/verify-releases.md). Mirror only what
   passed.
2. Copy all files of the release unchanged. A rewritten `checksums.txt` (a different line, a different line ending, a line
   for a file that you added) no longer matches the bundle, and hosts stop with `sigstore_verification_failed`.
3. Point the hosts at the mirror: `--updater-url`, `--updater-checksums-url` (the bundle is expected at
   `<checksums-url>.sigstore.json`, or pass `--updater-bundle-url`) and, for hosts without access to the Sigstore
   infrastructure, `--trusted-root` with a `trusted_root.json` that you fetched with `myriarch-updater fetch-trusted-root`.
   The host still needs a verifier: `cosign` 3.0.4 or newer or an installed `myriarch-updater`.
4. Re-signing with your own key stays optional and is for your own tooling (`verify-release.sh`, `publish-mirror.sh`). The
   installers no longer read a key: `--signing-key`/`-SigningKey` and the `checksums.txt.sig` options were removed. Hosts for
   which you want to rely on your own signature get `--updater-sha256` from a check that you ran yourself.

The scripts in this directory still follow the key-based format below and have not been changed for keyless files.

| Script | Purpose |
|---|---|
| `publish-mirror.sh` | Copies the host artifacts of a release into an Artifactory or Nexus repository. Immutable (a file with different content is never overwritten), verifies all signatures before the upload and every file after it. |
| `verify-release.sh` | Verifies a release (from a mirror URL, a directory or a goreleaser `dist/`): signature of `checksums.txt`, optionally every file against `checksums.txt` and its own `.sig`. The auth header is never forwarded to another host on a redirect. |
| `sign-blob.sh` | Signs a file with `cosign sign-blob` and a key (key file or any KMS URI cosign supports), without a transparency log, and re-verifies the result with `openssl`. Used by `deploy/release/goreleaser.yaml`. |
| `verify-upstream-images.sh` | Checks that the binaries in the pinned upstream collector images are bit-identical to the signed upstream release artifacts (SHA-256 and Sigstore bundle). |
| `ca-env.sh` | Makes an extra CA file known to go, crane, cosign, helm, node, curl, python and git (source it; see below). |

## Requirements

* bash, curl and openssl for all scripts.
* `cosign` for `sign-blob.sh` and `verify-upstream-images.sh`, `crane` and `tar` for `verify-upstream-images.sh`
  (`ci/install-tools.sh cosign crane` builds the pinned versions into `.cache/bin`).
* python3 for the tests.

## Extra CA

`ca-env.sh` reads **`EXTRA_CA_FILE`** (a PEM file). If it is set, the script exports a bundle of the system roots plus that CA
as `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE` and `GIT_SSL_CAINFO`, and sets `NODE_EXTRA_CA_CERTS`. Source it
explicitly before calling the other tools:

```bash
EXTRA_CA_FILE=/path/to/ca.crt . contrib/enterprise-mirror/ca-env.sh
```

`ci/lib.sh` does not source it. The scripts that talk to a mirror directly use `MIRROR_CA_FILE` (`publish-mirror.sh`) or
`CURL_CA_FILE` (`verify-release.sh`, `verify-upstream-images.sh`).

## Tests

```bash
bash ci/tests/run-all.sh enterprise-mirror
```

runs the tests in [`tests/`](tests) against throw-away local servers. `sign-blob-test.sh` needs cosign. On macOS the Python
case of `ca-env-test.sh` fails when the Command Line Tools Python is first in `PATH`, because it ignores the CA bundle.
