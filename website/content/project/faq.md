---
title: "FAQ"
weight: 15
description: "Short answers about what myriarch is, how it relates to OpenTelemetry, what it needs to run and how to take part."
---

## What is myriarch?

Myriarch is a fleet manager for OpenTelemetry Collectors, built on OpAMP. Git is the source of truth for blueprints, fleets,
rollout policies and the allowed collector versions. The web UI and the REST API operate within that frame, and every
operator action is audited. Start with the [concepts](/docs/concepts/) or the
[architecture](/docs/architecture/).

## How does it relate to OpenTelemetry and OpAMP?

Myriarch is an independent project. It is an OpAMP server and control plane for collectors that run under the upstream OpAMP
Supervisor. The protocol, the supervisor and the collector stay upstream; myriarch adds configuration rendering from Git,
rollouts in waves, version updates through executors and an audit trail. The name OpenTelemetry is used only to describe what
myriarch works with. See [ADR-0028](/docs/adr/0028-relationship-to-opentelemetry-and-foundation-path/) and the
[brand and trademark rules](/project/brand/).

## Why is there an updater next to the supervisor?

The upstream OpAMP Supervisor can manage configuration, health and restarts, but it cannot replace the collector binary.
Version updates therefore go through exchangeable executors: the host agent `myriarch-updater`, a webhook or an AWX job. On
Kubernetes the version is part of the image, so a pipeline changes it. See
[Executors](/docs/concepts/executors/) and [ADR-0006](/docs/adr/0006-version-updates-via-exchangeable-executors/).

## What are the support levels?

Myriarch is alpha. In the first release, container mode and Linux hosts are beta, Windows hosts are experimental and the Helm
chart is experimental. The levels and what they mean are in the [roadmap](/project/roadmap/) and in
[ADR-0021](/docs/adr/0021-publication-gates-and-v0-1-0-scope/).

## Do I need Kubernetes?

No. The server is one binary that needs PostgreSQL. It runs on a host, in a container or on Kubernetes with the
[Helm chart](/docs/reference/helm-chart/). Collectors can run on Linux and Windows hosts at edge sites and in data centers, or
in Kubernetes pods. A development setup needs no container runtime at all: see
[Local development](/docs/getting-started/local-dev/). To try it with containers on one machine, use
[Docker Compose](/docs/getting-started/docker-compose/), and for Kubernetes see [Install with Helm](/docs/operations/install-helm/).

## How many collectors does it handle?

The design target is 1,000 to 5,000 collectors, each with an optional updater, served by two or three replicas. The numbers
behind this are in the [architecture](/docs/architecture/#scaling), and [Simulate a fleet](/docs/tasks/simulate-a-fleet/)
shows how to test the load yourself.

## Which database does it use?

PostgreSQL holds the state, carries the events between replicas and elects the leader. There is no other stateful component.
See [ADR-0002](/docs/adr/0002-postgresql-for-state-event-bus-and-leader-election/).

## What is the license?

The code is licensed under Apache-2.0 and the documentation under CC BY 4.0. Third-party software keeps its own license. See
[ADR-0018](/docs/adr/0018-license-and-copyright/), [ADR-0030](/docs/adr/0030-reuse-compliance-and-third-party-notices/) and
the [third-party notices](/legal/third-party/) of this website.

## How can I contribute?

Read [Contributing](/project/contributing/). Every commit carries a `Signed-off-by:` line that certifies the Developer
Certificate of Origin; the project uses the DCO and no contributor license agreement
([ADR-0019](/docs/adr/0019-developer-certificate-of-origin/)). The [contributor ladder](/project/contributor-ladder/) describes
the roles, and the [development guide](/docs/development/) the toolchain and the checks.

## How do I report a vulnerability?

Privately, never in a public issue. Use GitHub private vulnerability reporting or write to the address in the
[security policy](/project/security/), which also lists the supported versions and the disclosure process.

## Does the software or this website track users?

This website sets no cookies, runs no analytics and makes no requests to third parties; see its
[privacy statement](/legal/privacy/). The software sends no usage reports to the project. The connections that the server
opens itself are listed in the [security model](/docs/security/model/#outbound-connections), and the operator controls them.
