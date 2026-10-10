---
title: "Operations"
weight: 50
description: "Run myriarchd in production: authentication, the settings that matter for security, and troubleshooting."
---

These pages are for the people who run a Myriarch installation. They are written for a reader who already knows the
[concepts](../concepts/_index.md) and wants to operate the system reliably.

| Page | What it covers |
|---|---|
| [Authentication](authentication.md) | How people sign in (OpenID Connect, the bootstrap admin) and how agents enroll, with recipes for common identity providers. |
| [Production checklist](production-checklist.md) | The settings of `myriarchd serve` that matter for security, and how to check a configuration without starting the server. |
| [Troubleshooting](troubleshooting.md) | Symptoms, causes and fixes, and the endpoints that help. |

Guides for installation with Helm, upgrades, backup and restore, high availability, air-gapped installation, an artifact
mirror and monitoring are being written for the first release.

Related: the [security model](../security/model.md) explains the controls behind these settings, the
[Helm chart README](../../deploy/helm/myriarch/README.md) covers Kubernetes, and
[deploy/README.md](../../deploy/README.md) lists the deployment artifacts and the host installation.
