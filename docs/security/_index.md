---
title: "Security"
weight: 60
description: "How myriarch protects the control plane, its agents and the software it delivers, and where to report a vulnerability."
---

This section describes the security controls of myriarch. It is written for operators who decide how to deploy the system and for reviewers who assess it.

- [Security model](model.md) lists the trust boundaries and, for each area, the control, the setting that governs it, its default and the risk that remains.
- [Verify releases](verify-releases.md) shows how to check a release before you run it: `checksums.txt`, its keyless Sigstore bundle and the exact signing identity, then the build provenance, the immutable release, the container images and the macOS binaries.
- [Export control](export-control.md) is the maintainers' assessment of the cryptography in the published software.
- [Authentication](../operations/authentication.md) is the operator guide for sign-in: the bootstrap admin, single sign-on with OpenID Connect and enrollment tokens.
- [Production checklist](../operations/production-checklist.md) collects the settings of `myriarchd serve` that matter for a production installation.

To report a vulnerability, follow [SECURITY.md](../../SECURITY.md). Please do not open a public issue for it.
