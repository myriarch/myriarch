---
title: "Upgrade the OpenTelemetry Collector on many hosts"
weight: 45
description: "Change the collector version of a whole fleet when the OpAMP Supervisor cannot: set the target in Git, pick an executor and roll it out in waves."
---

To upgrade the OpenTelemetry Collector on many hosts with Myriarch, set the target version of the fleet in Git, give the fleet an
**executor** that installs it, and let a version rollout move the hosts in waves. The upstream OpAMP Supervisor cannot replace the
collector binary, so something else has to install the new version. The executor does that (the host agent `myriarch-updater`, a
webhook or an AWX job), the rollout engine decides which hosts update when, and a host counts as done only when its collector
reports the new `service.version`.

## Why the supervisor is not enough

As of October 2026, the README of the upstream supervisor lists "Updates the Collector binary" without a checkmark and links
[issue #33947](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/33947) (closed as not planned), and the
OpAMP capabilities `AcceptsPackages` and `ReportsPackageStatuses` as not implemented, with the work tracked in
[issue #47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272) (read on 22 October 2026, see the
[upstream notes](../design/upstream-notes.md) for the sources and the reviewed versions). Without an executor, fleets end up with
many versions and hand-made update procedures.

## 1. Choose the executor of the fleet

| Executor | What installs the new version |
|---|---|
| `updater` | The host agent `myriarch-updater` on Linux and Windows hosts, or in container mode in a Kubernetes pod |
| `webhook` | A call to a URL, for example a pipeline that bumps an image tag |
| `awx` | A job template in Ansible AWX |
| `manual` | Nothing: you change the version by hand and the server tracks the result |
| `none` | The fleet is not updated by the server |

The executor is `spec.update.executor` of the fleet. On Kubernetes, where the version is part of the image, a pipeline changes it
through the `webhook` executor, or container mode lets the updater swap the binary in the pod (container mode is beta). A
`gitops-pr` executor that opens a merge request for the image bump is planned and not part of v0.1.0 ([roadmap](../../ROADMAP.md)).
See [Executors](../concepts/executors.md).

## 2. Set the target version

In the fleet file, `spec.collector.version` is the target (without the `v`). Change it, validate with
`myrctl validate` and merge. The planner creates a version rollout for the fleet. The excerpt follows
`examples/quickstart/fleets/linux-hosts.yaml`; the policy names are those of that example repository, and your own repository
may name its policies differently:

```yaml
spec:
  collector:
    distribution: otelcol-contrib
    version: 0.162.0
  update:
    executor: updater
  rollout:
    versionPolicy: canary-then-all
```

If you want operators to pick the version in the UI, Git must allow it with `spec.update.selection` (`enabled` and a
`versionRange`); the UI then chooses inside that range, with a reason for the audit log, and never outside the frame. See
[Roll out a collector version](roll-out-a-collector-version.md#choose-the-version-in-the-ui) and
[Fleets](../concepts/fleets.md).

## 3. Let the rollout run

With the `updater` executor the server fetches the release archive, verifies the SHA-256 checksum and the Sigstore signature, and
offers the file to the updater of each host. The updater downloads and verifies it again, swaps the binary, restarts the supervisor
service and checks the health of the collector. If the new version does not become healthy, the updater restores the previous one.

The version policy sets the pace: the quickstart policy `canary-then-all` starts targets only inside a maintenance window and, on a
failure, pauses so that a person decides to resume, retry the failed hosts, abort or roll back. The rollout page shows each target;
the actions are buttons in the UI and API calls. Details, including the limits on the host side (`update.minimumVersion`, and no downgrades except the step back to the previous version by default) are in [Roll out a collector version](roll-out-a-collector-version.md).

## 4. Hosts without internet access

`artifacts.sourceBaseUrl` can point to an internal mirror with the layout of the upstream releases, and signature verification
stays on. The helpers for mirroring are in [contrib/enterprise-mirror](../../contrib/enterprise-mirror/README.md), and the settings
are in the [production checklist](../operations/production-checklist.md).

## 5. Keep an eye on new releases

The release tracker detects new upstream collector releases and extracts breaking changes from the changelogs, so you can review
them before you raise a target. See [Track releases and send notifications](track-releases-and-notify.md).

Do not change the configuration and the collector version in the same merge request. A version update restarts the collector,
and a configuration target of the same host can fail during its health wait; retry the failed targets once the version rollout has
finished ([Roll out a configuration change](roll-out-a-config-change.md#good-practice)).
