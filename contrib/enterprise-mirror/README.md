# Enterprise mirror toolkit

Scripts for organizations that **mirror releases into a private registry or artifact repository and re-sign them with their
own keys** (for example behind a proxy, without access to GitHub, or with their own PKI). The toolkit is key-based:
signatures follow the format contract in [`deploy/release/README.md`](../../deploy/release/README.md) and are checked with
`openssl` (cosign is optional).

Until the keyless release pipeline ([ADR-0023](../../docs/adr/0023-keyless-release-signing.md)) is in place, the interim
key-based release path (`ci/build-release.sh`, `deploy/release/goreleaser.yaml`) signs with `sign-blob.sh` and cross-checks with
`verify-release.sh` from this directory. After that, the project's releases are signed keyless with Sigstore, and this toolkit
serves only operators who mirror and re-sign releases.

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
