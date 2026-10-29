---
title: "What is Myriarch?"
weight: 12
description: "Myriarch is an open-source, GitOps-governed fleet manager for OpenTelemetry Collectors, built on OpAMP. Facts on scope, license, maturity and version."
---

Myriarch is an open-source fleet manager for OpenTelemetry Collectors, built on OpAMP (the Open Agent Management Protocol). Git is
the source of truth for blueprints, fleets, rollout policies and the allowed collector versions; the web UI and the REST API
operate within that frame, and every operator action is audited. It is an independent project under the Apache-2.0 license,
currently **alpha**, and the latest release is **v0.1.0**.

## At a glance

| | |
|---|---|
| What it is | A server (`myriarchd`) that is an OpAMP server and control plane for OpenTelemetry Collectors, plus the tools `myrctl`, `myriarch-updater` and `myriarch-sim` |
| For | Operators who run many OpenTelemetry Collectors on Linux and Windows hosts, in containers and on Kubernetes, at edge sites, in data centers and in the cloud |
| Size | Designed for around 1,000 collectors, with headroom up to 5,000 (design targets, not measured limits) |
| License | Apache-2.0 for the code, CC BY 4.0 for the documentation |
| Maturity | Alpha. Interfaces, configuration and the GitOps format (`myriarch.dev/v1alpha1`) can still change |
| Latest release | v0.1.0 |
| Needs | PostgreSQL for state, events and leader election; no other stateful component |
| Source | [github.com/myriarch/myriarch](https://github.com/myriarch/myriarch) |

## What it does

- **Keeps the frame in Git.** Blueprints (layered, parameterized collector configurations), fleets (which agents get which
  blueprint and which collector version), rollout policies, executors and the inventory live in a Git repository. The server
  reads it, validates the whole revision and applies it only if it is completely valid. See
  [Git as the source of truth](/docs/concepts/gitops/).
- **Renders and offers configuration.** The server renders one content-addressed configuration per collector from the
  blueprints and offers it over OpAMP.
- **Rolls changes out in waves.** Configuration and version rollouts run in waves with approvals, health gates, failure
  thresholds, pause, abort, rollback and maintenance windows. See [Rollouts](/docs/concepts/rollouts/).
- **Updates collector versions through executors.** The host agent `myriarch-updater`, a webhook or an AWX call installs the
  new version; the target is complete only when the collector reports it. See [Executors](/docs/concepts/executors/).
- **Shows the fleet.** Every collector with version, health, effective configuration, components and host attributes, live;
  new upstream releases are detected and breaking changes are extracted from the changelogs.
- **Audits.** Every operator action, API call and rollout step is recorded. Sign-in works with OpenID Connect or a bootstrap
  administrator, with the roles viewer, operator and admin, and API tokens.

## What it does not do

- It does **not replace the OpAMP Supervisor or the collector.** Collectors keep running under the upstream OpAMP Supervisor, and
  the project does not plan its own collector distribution or an in-process client that replaces the supervisor
  ([roadmap](/project/roadmap/#not-planned)).
- It does **not edit declared objects in the web UI.** Blueprints, fleets and policies stay in Git. The UI can choose a target
  collector version only if Git allows it and only inside the range Git defines.
- It does **not yet offer multi-tenancy**, and the update of the supervisor itself through `myriarch-updater` is a later item
  ([roadmap](/project/roadmap/#later)).

## How it relates to the OpAMP Supervisor and the Collector

The upstream OpAMP Supervisor starts and monitors a collector and speaks OpAMP for it. It manages configuration, health and
restarts, but it cannot replace the collector binary. Myriarch is the server side: it adds configuration rendering from Git,
rollouts in waves, version updates through executors and an audit trail, and it leaves the protocol, the supervisor and the
collector upstream. The facts about the upstream software, each with a public source and a date, are in the
[upstream notes](/docs/design/upstream-notes/). A side-by-side view with the alternatives is on the
[comparison page](/project/comparison/).

Myriarch is not affiliated with or endorsed by The Linux Foundation or the OpenTelemetry project; OpenTelemetry, Kubernetes and
CNCF are trademarks of The Linux Foundation and are named only to describe compatibility
([ADR-0028](/docs/adr/0028-relationship-to-opentelemetry-and-foundation-path/), [brand rules](/project/brand/)).

## Maturity and support levels

Myriarch as a whole is alpha. In v0.2.0, container mode of `myriarch-updater`, Linux hosts (systemd) and the server Helm chart are beta;
Windows hosts and the collector Helm chart are experimental. What the levels mean, and what has to hold before the project calls itself
beta, is in the [roadmap](/project/roadmap/#from-alpha-to-beta). Every release is signed keyless and carries build provenance;
[verify it](/docs/security/verify-releases/) before you run it.

## Where to go next

- Learn the ideas: [concepts](/docs/concepts/) and [architecture](/docs/architecture/).
- Try it on your machine: [getting started](/docs/getting-started/).
- Solve a problem: [manage many OpenTelemetry Collectors with OpAMP and Git](/docs/tasks/manage-many-collectors/) and
  [upgrade the OpenTelemetry Collector on many hosts](/docs/tasks/upgrade-collectors-on-many-hosts/).
- Short answers: the [FAQ](/project/faq/).
- The repository, with code, documentation sources and releases: [github.com/myriarch/myriarch](https://github.com/myriarch/myriarch).
