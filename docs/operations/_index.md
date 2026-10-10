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

A guide for the installation with Helm is being written. Until it is published, the
[Helm chart README](../../deploy/helm/myriarch/README.md) describes the installation.

Guides for upgrades, backup and restore, high availability, monitoring, air-gapped installation and an artifact mirror are
planned for v0.2.0. Until then, the [architecture](../architecture.md) describes high availability, the data model and its
retention, the artifact cache and the metrics and health endpoints of the server.

Related: the [security model](../security/model.md) explains the controls behind these settings, the
[Helm chart README](../../deploy/helm/myriarch/README.md) covers Kubernetes, and
[deploy/README.md](../../deploy/README.md) lists the deployment artifacts and the host installation.
