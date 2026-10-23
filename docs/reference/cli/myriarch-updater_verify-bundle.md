---
title: "myriarch-updater verify-bundle"
weight: 280
description: "Verify an artifact against a Sigstore bundle offline (diagnostics)."
---

## myriarch-updater verify-bundle

Verify an artifact against a Sigstore bundle offline (diagnostics)

### Synopsis

Verifies that the Sigstore bundle authenticates the artifact (or its --sha256) as the release artifact of --version, using the same policy as the updater.

```text
myriarch-updater verify-bundle [flags]
```

### Options

```text
      --artifact string          artifact file (its SHA-256 is computed)
      --bundle string            Sigstore bundle JSON file (<asset>.sigstore.json)
  -h, --help                     help for verify-bundle
      --identity-regexp string   certificate identity (SAN) regexp (default "^https://github\\.com/open-telemetry/opentelemetry-collector-releases/\\.github/workflows/base-release\\.yaml@refs/tags/v[0-9]+\\.[0-9]+\\.[0-9]+$")
      --issuer string            OIDC issuer (default "https://token.actions.githubusercontent.com")
      --sha256 string            artifact SHA-256 (hex) instead of --artifact
      --trusted-root string      trusted_root.json (empty = fetch via TUF)
      --tuf-cache string         TUF cache directory (only without --trusted-root)
      --version string           release version, e.g. v0.162.0
```

### SEE ALSO

- [myriarch-updater](myriarch-updater.md) - Myriarch host agent: installs collector updates offered via OpAMP
