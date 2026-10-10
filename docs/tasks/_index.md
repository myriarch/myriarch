---
title: "How-to guides"
weight: 20
description: "Step-by-step guides for the common jobs: enrolling hosts, validating the GitOps repository, rolling out changes and trying things locally."
---

Each page is a how-to guide: it solves one job from start to finish and assumes that you know what you want to achieve. The
[concepts](../concepts/_index.md) explain why the system behaves as it does, and the [reference](../reference/_index.md) lists
every option. To learn the system from the start, use the [tutorials](../getting-started/_index.md).

| Guide | What it covers |
|---|---|
| [Connect a host](connect-a-host.md) | Enroll a Linux or Windows host with the installation scripts. |
| [Validate a GitOps repository](validate-a-gitops-repository.md) | Check and render configuration with `myrctl`, locally and in CI. |
| [Roll out a configuration change](roll-out-a-config-change.md) | Waves, approval, failure handling and rollback for a blueprint change. |
| [Roll out a collector version](roll-out-a-collector-version.md) | Version updates with the updater, webhook or AWX executor, and the choice in the UI within the Git frame. |
| [Track releases and send notifications](track-releases-and-notify.md) | Upstream release tracking, breaking changes and notification channels. |
| [Simulate a fleet](simulate-a-fleet.md) | Thousands of simulated supervisors and updaters with `myriarch-sim`. |
| [Connect a real supervisor](connect-a-real-supervisor.md) | The upstream OpAMP Supervisor and `otelcol-contrib` on your machine. |
| [Run two replicas locally](run-two-replicas-locally.md) | Leader election and failover with two `myriarchd` replicas. |

To start your own GitOps repository, begin with the template described in
[gitops-template/README.md](../../gitops-template/README.md). The pages from "Roll out a configuration change" on assume the running system of
[Local development](../getting-started/local-dev.md). For operating a real installation see
[Operations](../operations/_index.md).
