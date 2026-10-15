# 0007. Package offers in the upstream draft format with Sigstore bundles

- Status: Accepted, refined by [ADR-0046](0046-host-installation-from-the-server.md)
- Date: 2026-10-07
- Related: [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0023](0023-keyless-release-signing.md), [Updater protocol](../design/updater-protocol.md)

## Context

`myriarch-updater` receives package offers over OpAMP (`PackagesAvailable`). The upstream supervisor is planning the same mechanism for itself ([#47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272)). Its draft offers exactly one package with an empty name, a top-level type, a version such as `v0.135.0`, an archive URL, a SHA-256 content hash and a signature made of a certificate and a signature value.

The official collector releases changed how they are signed. Since release 0.158.0 every asset has a checksum file `<asset>.sha256` and a Sigstore bundle `<asset>.sigstore.json`. A separate certificate and signature value are no longer published ([upstream notes](../design/upstream-notes.md#signatures)). A bundle can be verified offline against a trusted root file.

## Decision

We build package offers in the shape of the upstream draft: one package with an empty name, a top-level type, a `v`-prefixed version, the archive URL and the SHA-256 hash. This keeps the offers compatible with the supervisor's planned update path.

We deviate in one field: `signature` carries the raw Sigstore bundle as JSON instead of the draft's `"<certificate> <signature>"`. The updater verifies the bundle against a Sigstore trusted root (a local `trusted_root.json` for offline verification, or one fetched through TUF when none is configured), the expected certificate identity and the OIDC issuer, and it requires that the tag in the identity matches the version being installed ([updater protocol](../design/updater-protocol.md#signature-verification)).

## Consequences

### Positive

- A later supervisor-native executor can reuse the offer format.
- Verification can run without network access on the host (with a local trusted root) and needs no key distribution.
- The identity binds the artifact to the upstream release workflow and tag, not to a key that could leak.

### Negative

- The `signature` field does not follow the draft literally; a consumer that expects the draft's two-part value cannot use it.
- Offline verification needs a current Sigstore trusted root file on the host; `myriarch-updater fetch-trusted-root` writes one.

## Alternatives considered

- **The draft's `"<certificate> <signature>"` value**: not possible, because releases from 0.158.0 on do not publish those files.
