# Enterprise mirror toolkit

Scripts for organizations that **mirror releases into a private registry or artifact repository**, for example behind a proxy,
without access to GitHub, or with their own PKI. The scripts work in two modes:

| Mode | For | How the release is checked |
|---|---|---|
| **Keyless** (`--keyless`) | the project's own releases, mirrored **unchanged** | `cosign verify-blob` of `checksums.txt` with `checksums.txt.sigstore.json` against the identity of `release.yml` at the tag of the version, then the SHA-256 of every file against `checksums.txt`. No key, no `.sig` files. |
| **Key-based** (default, `--public-key`) | releases that your organization **re-signs with its own key** | the signature of `checksums.txt` and of every file with your public key, in the format of [`signing.md`](signing.md), checked with `openssl` (cosign is optional) |

The project's releases carry no key-based signatures: they are signed keyless with Sigstore by the release workflow
([ADR-0023](../../docs/adr/0023-keyless-release-signing.md), [ADR-0035](../../docs/adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)).
Use the keyless mode to mirror them. The key-based mode is for operators who want hosts to rely on their own signature.

## Mirroring keyless releases

The installers (`install-linux.sh`, `install-windows.ps1`) authenticate the updater through the **keyless Sigstore bundle of the
project's release**, not through a key of the mirror. A mirror therefore has to carry the release files **unchanged**,
including `checksums.txt` and `checksums.txt.sigstore.json`, below the same directory (`<base>/v<version>/`):

1. Download the files of the release from its GitHub release page into one directory: at least `checksums.txt`,
   `checksums.txt.sigstore.json` and the files you want to mirror (the bare `myriarch-updater` binaries and the archives of
   `myriarch-updater` and `myrctl` for the platforms of your hosts).
2. Publish them with the keyless mode. It verifies the release first (the bundle against the exact identity
   `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v<version>` and issuer
   `https://token.actions.githubusercontent.com`, then every file against `checksums.txt`) and uploads only what passed:

   ```bash
   export MIRROR_REPO_URL=https://artifactory.example.com/artifactory/myriarch-releases MIRROR_TYPE=artifactory MIRROR_TOKEN=...
   contrib/enterprise-mirror/publish-mirror.sh --keyless --dir ./release-0.1.0 --version 0.1.0 --dry-run     # check first
   contrib/enterprise-mirror/publish-mirror.sh --keyless --dir ./release-0.1.0 --version 0.1.0
   ```

   The files of the host scope (bare updater binaries, updater and `myrctl` archives) go first, then `checksums.txt`, then
   `checksums.txt.sigstore.json` **last**: an installer that finds the bundle finds all files. `SCOPE=all` also uploads every
   other file that `checksums.txt` lists and the provenance file. A file that already exists is never overwritten: it is skipped
   when its SHA-256 is the same and aborts the run when it differs. If the machine that publishes cannot reach the Sigstore
   infrastructure, pass `--trusted-root trusted_root.json` (fetched with `myriarch-updater fetch-trusted-root`).
3. Check the mirror at any time (the files are fetched from the mirror; `VERIFY_AUTH_HEADER` carries its credentials):

   ```bash
   contrib/enterprise-mirror/verify-release.sh --keyless --version 0.1.0 \
     --base-url https://artifactory.example.com/artifactory/myriarch-releases/v0.1.0 --assets
   ```

   It needs cosign 3.0.4 or newer (`[cosign_outdated]` otherwise) and the exact version: a bundle of another tag fails with
   `[signature_invalid]`, a changed file with `[checksum_mismatch]`.
4. Point the hosts at the mirror: `--updater-url`, `--updater-checksums-url` (the bundle is expected at
   `<checksums-url>.sigstore.json`, or pass `--updater-bundle-url`) and, for hosts without access to the Sigstore
   infrastructure, `--trusted-root` with a `trusted_root.json` that you fetched with `myriarch-updater fetch-trusted-root`.
   The host still needs a verifier: `cosign` 3.0.4 or newer or an installed `myriarch-updater`.
5. A rewritten `checksums.txt` (a different line, a different line ending, a line for a file that you added) no longer
   matches the bundle, and hosts stop with `sigstore_verification_failed`. Do not edit the files of a keyless mirror.
6. The installers no longer read a key: `--signing-key`/`-SigningKey` and the `checksums.txt.sig` options were removed. Hosts for
   which you want to rely on your own signature get `--updater-sha256` from a check that you ran yourself, or you mirror in key
   mode (below).

## Key-based mode

`publish-mirror.sh` without `--keyless` copies the host artifacts of a goreleaser `dist/` (`--dist`) or a staged release and
checks the key-based signatures of [`signing.md`](signing.md) with your public key (`COSIGN_PUBLIC_KEY`); `verify-release.sh
--public-key` does the same for a mirror, a directory or a `dist/`. The two modes exclude each other (`--keyless` with
`--public-key` is an error). The format contract, the creation of the key and its rotation are in [`signing.md`](signing.md).

| Script | Purpose |
|---|---|
| `publish-mirror.sh` | Copies the host artifacts of a release into an Artifactory or Nexus repository (`--keyless --dir` for the project's release, `--dist` for a re-signed one). Immutable (a file with different content is never overwritten), verifies all signatures before the upload and every file after it. |
| `verify-release.sh` | Verifies a release (from a mirror URL, a directory or, in key mode, a goreleaser `dist/`): the bundle (`--keyless`) or the signature (`--public-key`) of `checksums.txt`, optionally every file against `checksums.txt` (and in key mode its own `.sig`). The auth header is never forwarded to another host on a redirect. |
| `sign-blob.sh` | Signs a file with `cosign sign-blob` and a key (key file or any KMS URI cosign supports), without a transparency log, and re-verifies the result with `openssl`. |
| `verify-upstream-images.sh` | Checks that the binaries in the pinned upstream collector images are bit-identical to the signed upstream release artifacts (SHA-256 and Sigstore bundle). |
| `ca-env.sh` | Makes an extra CA file known to go, crane, cosign, helm, node, curl, python and git (source it; see below). |

## Requirements

* bash, curl and openssl for all scripts.
* `cosign` 3.0.4 or newer for the keyless mode of `verify-release.sh` and `publish-mirror.sh`, `cosign` for `sign-blob.sh` and
  `verify-upstream-images.sh`, `crane` and `tar` for `verify-upstream-images.sh`
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

runs the tests in [`tests/`](tests) against throw-away local servers (random ports on 127.0.0.1). `sign-blob-test.sh` needs cosign.
The keyless cases use a cosign stub (`cosign-stub.sh`) and, when a real cosign is installed, the committed OpenTelemetry
fixture, whose genuine bundle must be refused for the Myriarch identity; they are skipped without cosign and required when
`CI=true`. On macOS the Python
case of `ca-env-test.sh` fails when the Command Line Tools Python is first in `PATH`, because it ignores the CA bundle.
