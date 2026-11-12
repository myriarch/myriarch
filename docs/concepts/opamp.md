---
title: "OpAMP, the Supervisor and the Collector"
weight: 5
description: "What OpAMP is, what the OpAMP Supervisor does and cannot do, how remote configuration reaches a collector, and where myriarch fits, with dated sources."
---

OpAMP, the [Open Agent Management Protocol](https://github.com/open-telemetry/opamp-spec/blob/main/specification.md) of the
OpenTelemetry project, is a network protocol with which a server manages a fleet of telemetry agents: the agents report their
status, and the server sends remote configuration and offers packages. Myriarch is an OpAMP server for OpenTelemetry Collectors.
This page explains the parts of an OpAMP setup, how a configuration reaches a collector and what the upstream OpAMP Supervisor
cannot do. The statements about upstream software come from the [upstream notes](../design/upstream-notes.md), which name a public
source and a date for each of them; upstream may have changed since.

Myriarch is an independent project. OpenTelemetry and Kubernetes are trademarks of The Linux Foundation, and myriarch is not
affiliated with or endorsed by The Linux Foundation or the OpenTelemetry project; the names are used only to describe
compatibility ([ADR-0028](../adr/0028-relationship-to-opentelemetry-and-foundation-path.md)).

## The parts of an OpAMP setup

| Part | What it does | In a myriarch installation |
|---|---|---|
| OpAMP server | Decides what every agent runs and talks to the agents over OpAMP. | `myriarchd`, together with the GitOps syncer, the rollout engine, the web UI and the REST API ([architecture](../architecture.md)). |
| OpAMP Supervisor | The upstream process that starts and monitors a collector and speaks OpAMP for it. It applies remote configuration, reports health and the effective configuration, and restarts the collector. | Runs next to every collector: as a service on a host, or in the same pod as the collector. Myriarch does not replace it. |
| OpenTelemetry Collector | Receives, processes and exports telemetry. The Supervisor adds the OpAMP extension to the configuration it starts the collector with. | OpenTelemetry Collector Contrib (`otelcol-contrib`), the only distribution that a fleet accepts ([schema reference](../reference/gitops-schema.md#fleet)). |
| OpAMP Bridge | Part of the OpenTelemetry Operator on Kubernetes. In its default operator mode it connects as one OpAMP agent for many collector resources. | Recognised as an agent kind of its own: never restarted and never the target of a version rollout, and configured only in standalone mode with `spec.bridge.manage: true` ([Bridge agents](fleets.md#bridge-agents)). |
| `myriarch-updater` | The host agent of myriarch that installs collector binaries, which the Supervisor cannot do. | Optional: next to the Supervisor on a host, or in container mode in a pod ([Executors](executors.md)). |

## How a configuration reaches a collector

1. Git declares blueprints, fleets and rollout policies. `myriarchd` renders one configuration per agent from the blueprints of
   its fleet and identifies it by the hash of its canonical form ([Blueprints](blueprints.md),
   [ADR-0004](../adr/0004-server-side-rendering-to-one-content-addressed-config.md)).
2. A new agent, or one that moves to another fleet, gets the configuration of its fleet at once. Any later change reaches the
   agents through a configuration rollout, which offers the new configuration over OpAMP as remote configuration, wave by wave
   ([Fleets](fleets.md#when-does-the-offered-configuration-change), [Rollouts](rollouts.md)).
3. The Supervisor merges, in this order, its own telemetry configuration, the local configuration files, the configuration of the
   injected OpAMP extension and the remote configuration, so the remote configuration wins over local files. It writes the result
   and restarts the collector, or reloads it when it is configured to.
4. The Supervisor reports a status for the hash it received. An unchanged configuration is `APPLIED` at once. Otherwise it is
   `APPLYING`, then `APPLIED` when the collector is healthy after `config_apply_timeout`, or `FAILED`.
5. The server compares the hash that the agent reports with the hash it offers: the agent is in sync when both are equal and the
   status is `APPLIED` ([Agents](agents.md#assigned-desired-and-failed-configuration)). The configuration that a collector reports
   is expanded, includes default values and shows opaque values as `[REDACTED]`, which is why the comparison uses the hash and not
   the text.

Rollback exists on two levels. On its own host, the Supervisor can go back to the last working remote configuration when
`automatic_config_rollback` is set. It is off by default upstream ([upstream
notes](../design/upstream-notes.md#configuration-keys-that-matter)); the Supervisor configurations that the myriarch installers
and the collector chart write turn it on ([Server behaviour](../design/opamp-server.md#supervisor-configuration)). For a whole
rollout, the rollout policy decides whether a failed wave rolls back or pauses the rollout ([Failure threshold, pause and
rollback](rollouts.md#failure-threshold-pause-and-rollback)).

## What the Supervisor cannot do

The Supervisor manages configuration, health, restarts and the inventory of components, but it cannot replace the collector
binary. Its configuration accepts `accepts_packages`, but in version 0.162.0 its package manager answers "unsupported" at run
time. Upstream tracks the work in [issue #47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272),
a staged plan that was open when the issue was last checked on 2026-10-08; an earlier issue,
[#33947](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/33947), was closed as not planned on 2025-04-04.
The [upstream status](../design/upstream-notes.md#upstream-status-as-of-2026-10-15) has the details.

A new collector version therefore needs something besides the Supervisor. Myriarch starts the update through the executor of the
fleet, for example the host agent `myriarch-updater`, a webhook or an AWX job template, and a host counts as updated only when its
collector reports the new version ([Executors](executors.md),
[ADR-0006](../adr/0006-version-updates-via-exchangeable-executors.md)). [Upgrade the OpenTelemetry Collector on many
hosts](../tasks/upgrade-collectors-on-many-hosts.md) shows the steps for a fleet.

## On Kubernetes

The upstream notes describe two ways to run collectors that an OpAMP server manages on Kubernetes. They name the OpenTelemetry
Operator 0.160.0 as the reviewed version; the identity, the modes and the remote configuration of its OpAMP Bridge were reviewed
again in the sources of 0.160.0 and 0.161.0 on 2026-10-15 ([Kubernetes options](../design/upstream-notes.md#kubernetes-options)).

- **Supervisor and collector in one pod.** Every pod is one OpAMP agent with the full set of capabilities. The Helm chart
  `myriarch-collector` runs this setup with the unchanged upstream images ([Install the collector with
  Helm](../operations/install-collector-helm.md), [ADR-0008](../adr/0008-kubernetes-supervisor-and-collector-in-one-pod.md)). A
  new collector version is normally a new image reference in the deployment repository, and applying it replaces the pod. The
  `webhook` executor can start that change, for example through a pipeline that bumps the image tag; with the `manual` executor,
  myriarch makes no call and only follows a change made outside it. Container mode of `myriarch-updater`, an explicit opt-in
  intended for air-gapped and edge clusters, swaps the binary inside the pod instead ([Container
  mode](../design/container-mode.md),
  [ADR-0026](../adr/0026-collector-version-updates-on-kubernetes.md)).
- **The Operator with its OpAMP Bridge.** In operator mode the Bridge is one OpAMP agent for the collector resources it manages. A
  remote configuration replaces the whole `spec` of each resource, and a message that lacks a resource that was applied earlier
  deletes it. Packages are not implemented, so a version changes only through `spec.image`. Myriarch recognises a Bridge: in
  operator mode it sends the Bridge nothing that changes a resource, and a fleet configures a Bridge only in standalone mode and
  only with `spec.bridge.manage: true` ([Bridge agents](fleets.md#bridge-agents),
  [ADR-0037](../adr/0037-opamp-bridge-agents-until-the-operator-integration.md)).

## Try it with a real Supervisor

[Connect a real supervisor](../tasks/connect-a-real-supervisor.md) runs the upstream Supervisor and `otelcol-contrib` on your
machine against a local server in development mode and shows each step of the chain: the handshake, the assignment to a fleet, the
remote configuration, the effective configuration, health and a restart. [Manage many OpenTelemetry Collectors with OpAMP and
Git](../tasks/manage-many-collectors.md) is the path to a real fleet.
