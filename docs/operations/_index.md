---
title: "Operations"
weight: 50
description: "Run myriarchd in production: installation with Helm, authentication, the settings that matter for security, and troubleshooting."
---

These pages are for the people who run a Myriarch installation. They are written for a reader who already knows the
[concepts](../concepts/_index.md) and wants to operate the system reliably.

| Page | What it covers |
|---|---|
| [Install with Helm](install-helm.md) | The server on Kubernetes with the experimental Helm chart: image, database, first sign-in, GitOps repository, access for agents and enrollment tokens. |
| [Authentication](authentication.md) | How people sign in (OpenID Connect, the bootstrap admin) and how agents enroll, with recipes for common identity providers. |
| [High availability](high-availability.md) | The leader failover after a hard failure and the setting that bounds it, and how to size `shutdownTimeout` and `terminationGracePeriodSeconds`. |
| [Production checklist](production-checklist.md) | The settings of `myriarchd serve` that matter for security, and how to check a configuration without starting the server. |
| [Troubleshooting](troubleshooting.md) | Symptoms, causes and fixes, and the endpoints that help. |

Guides for upgrades, backup and restore, monitoring, air-gapped installation and an artifact mirror are
planned for v0.2.0. Until then, the [architecture](../architecture.md) describes high availability, the data model and its
retention, the artifact cache and the metrics and health endpoints of the server.

Related: the [security model](../security/model.md) explains the controls behind these settings, the
[Helm chart README](../../deploy/helm/myriarch/README.md) covers Kubernetes, and
[deploy/README.md](../../deploy/README.md) lists the deployment artifacts and the host installation.
