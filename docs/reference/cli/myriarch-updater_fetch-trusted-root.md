---
title: "myriarch-updater fetch-trusted-root"
weight: 170
description: "Fetch the Sigstore trusted root via TUF and write it as trusted_root.json (diagnostics, mirror image build)."
---

## myriarch-updater fetch-trusted-root

Fetch the Sigstore trusted root via TUF and write it as trusted_root.json (diagnostics, mirror image build)

### Synopsis

Fetches the trusted root of the public Sigstore instance via TUF (needs network) and writes it to --out. The file is what update.trustedRootFile / artifacts.verify.trustedRootFile need for offline verification.

```text
myriarch-updater fetch-trusted-root [flags]
```

### Options

```text
  -h, --help               help for fetch-trusted-root
      --out string         output file (trusted_root.json)
      --tuf-cache string   TUF cache directory
```

### SEE ALSO

- [myriarch-updater](myriarch-updater.md) - Myriarch host agent: installs collector updates offered via OpAMP
