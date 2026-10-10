# 0023. Keyless release signing with Sigstore

- Status: Accepted; the certificate identity and the planned identity change are superseded by [ADR-0029](0029-host-under-the-myriarch-organization.md)
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md), [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0029](0029-host-under-the-myriarch-organization.md)

## Context

The current release scripts sign images, the Helm chart and blobs with cosign using a long-lived ECDSA key pair (`ci/sign-images.sh`, `ci/sign-blob.sh`, public key expected at `deploy/release/cosign.pub`). They verify with `--insecure-ignore-tlog=true`, that is, without a transparency log.

For a public project a long-lived signing secret is a liability (custody, rotation, compromise), and verifiers cannot audit signing events. [ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md) already uses Sigstore bundles for package offers.

## Decision

Releases are signed only keyless, from the release workflow only: cosign with the GitHub Actions OIDC identity, Fulcio short-lived certificates and Rekor transparency log entries.

- Signed objects: all images (by digest), the OCI Helm chart, release archives and checksums, SBOMs, and provenance attestations (SLSA build provenance).
- The trust anchor is the workflow identity:
  - certificate identity `https://github.com/gilfy/myriarch/.github/workflows/release.yml@refs/tags/v*`
  - issuer `https://token.actions.githubusercontent.com`
  - The identity changes with the organization move ([ADR-0017](0017-repository-hosting-and-public-identity.md)).
- Installers and myriarch-updater verify downloaded artifacts with Sigstore against this identity, not against a static public key.
- The existing key-based tooling is kept as an optional toolkit for organizations that mirror releases into private registries and re-sign them with their own keys (for example `contrib/enterprise-mirror`). It is not used for upstream releases.

## Consequences

### Positive

- No long-lived signing secret.
- Publicly auditable signatures and standard verification commands.
- Aligns with OpenSSF Scorecard and SLSA expectations.

### Negative

- Verification needs Sigstore tooling and, for online verification, access to the public-good instance or a bundled trusted root.
- Air-gapped verification requires distributing bundles and the trusted root.
- The identity changes with the repository move.
- An offline, openssl-only check is no longer possible for upstream releases.

## Alternatives considered

- **Keyless plus a project KMS key for openssl verification**: rejected, it reintroduces a long-lived key and creates two trust paths.
- **Key-only signing**: rejected, it keeps the custody and audit problems.
