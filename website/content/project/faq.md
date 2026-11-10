---
title: "FAQ"
weight: 15
description: "Short answers about myriarch, OpAMP and the OpAMP Supervisor, the OpenTelemetry Operator, version updates, rollback, what it needs and how to take part."
---

## What is myriarch?

Myriarch is an open-source OpAMP server and fleet manager for OpenTelemetry Collectors. Git is the source of truth for blueprints, fleets,
rollout policies and the allowed collector versions. The web UI and the REST API operate within that frame, and every
operator action is audited. The page [What is Myriarch?](/project/what-is-myriarch/) has the facts in one place; the
[concepts](/docs/concepts/) and the [architecture](/docs/architecture/) explain how it works.

## How does it relate to OpenTelemetry and OpAMP?

Myriarch is an independent project. It is an OpAMP server and control plane for collectors that run under the upstream OpAMP
Supervisor. The protocol, the supervisor and the collector stay upstream; myriarch adds configuration rendering from Git,
rollouts in waves, version updates through executors and an audit trail. The name OpenTelemetry is used only to describe what
myriarch works with. See [ADR-0028](/docs/adr/0028-relationship-to-opentelemetry-and-foundation-path/) and the
[brand and trademark rules](/project/brand/).

## What is OpAMP?

OpAMP, the Open Agent Management Protocol of the OpenTelemetry project, is a network protocol with which a server manages a fleet of telemetry agents: the agents report their status, and the server sends remote configuration and offers packages. Myriarch is an OpAMP server; the protocol, the supervisor and the collector stay upstream. [OpAMP, the Supervisor and the Collector](/docs/concepts/opamp/) explains the parts and how a configuration reaches a collector.

## What can the OpAMP Supervisor do, and what not?

The OpAMP Supervisor is the upstream process that starts and monitors a collector and speaks OpAMP for it. It applies remote configuration, reports health and the effective configuration, and restarts the collector. It cannot replace the collector binary: in version 0.162.0 its package manager answers "unsupported" at run time. Myriarch therefore updates collector versions through executors. The sources and their dates are in the [upstream notes](/docs/design/upstream-notes/#no-binary-updates).

## What is the difference between the OpAMP extension and the OpAMP Supervisor?

The OpAMP extension is a component inside the collector; the OpAMP Supervisor is a separate process. The Supervisor starts the collector, adds the extension to the configuration it starts it with and learns the description and the available components of the collector through it, and it speaks OpAMP to the server for the collector: it applies remote configuration, reports health and the effective configuration, and restarts the collector. Myriarch documents collectors that run under the Supervisor; the core distribution of the collector lacks the extension. See the [upstream notes](/docs/design/upstream-notes/#identity-and-reported-attributes).

## Why is there an updater next to the supervisor?

The upstream OpAMP Supervisor can manage configuration, health and restarts, but it cannot replace the collector binary.
Version updates therefore go through exchangeable executors: the host agent `myriarch-updater`, a webhook or an AWX job. On
Kubernetes the version is part of the image, so a pipeline changes it. See
[Executors](/docs/concepts/executors/) and [ADR-0006](/docs/adr/0006-version-updates-via-exchangeable-executors/).

## Can I keep Ansible AWX or my own pipeline for collector updates?

Yes. The executor of a fleet decides what installs a new version: the `awx` executor launches a job template of Ansible AWX for the hosts of a wave, and the `webhook` executor calls a URL, for example a pipeline that bumps an image tag. The rollout engine still decides which hosts update when, and a host counts as updated only when its collector reports the new version. See [Executors](/docs/concepts/executors/).

## How do I manage many OpenTelemetry Collectors with OpAMP?

Run each collector under the upstream OpAMP Supervisor, run Myriarch as the OpAMP server, and declare blueprints, fleets and
rollout policies in a Git repository. Changes merged in Git are rendered per collector and rolled out in waves. The steps are in
[Manage many OpenTelemetry Collectors with OpAMP and Git](/docs/tasks/manage-many-collectors/).

## Does Myriarch replace the OpAMP Supervisor?

No. The collectors keep running under the upstream supervisor, and Myriarch is the server side. It does not plan its own
supervisor or collector distribution; see the [roadmap](/project/roadmap/#not-planned). The
[comparison](/project/comparison/) shows when the supervisor alone, a server of your own on opamp-go or Myriarch fits, and when
Myriarch is not the right choice.

## Which collector distribution does myriarch manage?

OpenTelemetry Collector Contrib (`otelcol-contrib`), run by the upstream OpAMP Supervisor. The field `spec.collector.distribution` of a fleet accepts only `otelcol-contrib`, and the documentation covers that distribution. See the [schema reference](/docs/reference/gitops-schema/#fleet).

## How do I upgrade the OpenTelemetry Collector on many hosts?

Set the target version of the fleet in Git and give the fleet an executor: the host agent `myriarch-updater`, a webhook or an
AWX job installs it, because the supervisor cannot replace the collector binary. A version rollout moves the hosts in waves and a
host is done when its collector reports the new version. See
[Upgrade the OpenTelemetry Collector on many hosts](/docs/tasks/upgrade-collectors-on-many-hosts/).

## Can collectors be updated without internet access?

Yes, with the `updater` executor. Mirror the official release files one to one into an internal artifact repository and point `artifacts.sourceBaseUrl` of the server at it, with `artifacts.sourceAuth` for its credentials and, behind a proxy, the TUF settings under `artifacts.verify`; signature verification stays on. See [Work offline with a local mirror](/docs/tasks/roll-out-a-collector-version/#work-offline-with-a-local-mirror) and the [production checklist](/docs/operations/production-checklist/).

## How do I roll out a collector configuration change safely?

Change the blueprint in Git and merge. The rollout policy of each affected fleet sets the waves, the approval of the next wave,
the failure threshold and whether a failure rolls back or pauses. See
[Roll out a configuration change](/docs/tasks/roll-out-a-config-change/).

## How do I roll back a bad collector configuration?

The rollout policy decides what happens when a wave reaches its failure threshold: `onFailure: rollback` returns every started target to its previous configuration, and `onFailure: pause` stops the rollout until an operator resumes it, retries the failed targets, aborts it or rolls it back. A rollback contradicts Git, so revert the change in Git as well. On a single host, the upstream supervisor can also return to its last working remote configuration when `automatic_config_rollback` is set; it is off by default upstream, and the supervisor configurations that the myriarch installers and the collector chart write turn it on ([server behaviour](/docs/design/opamp-server/#supervisor-configuration)). See [Failure threshold, pause and rollback](/docs/concepts/rollouts/#failure-threshold-pause-and-rollback).

## How do I check a collector configuration in CI?

`myrctl validate` runs the same validation as the server, and `myrctl render` shows the configuration that a host would
receive. See [Validate a GitOps repository](/docs/tasks/validate-a-gitops-repository/).

## Who changed a configuration?

Declared state changes only through Git, so the history of the repository answers it. Every operator action in the UI and the API,
such as an approval or a version chosen in the UI, is audited with the user and the reason; see the
[security model](/docs/security/model/) and the audit endpoints of the [REST API](/docs/reference/api/).

## What are the support levels?

Myriarch is alpha. In v0.2.0, container mode, Linux hosts and the server Helm chart are beta; Windows hosts and the collector Helm
chart are experimental. The levels and what they mean are in
[Support levels and tested platforms](/docs/operations/tested-platforms/); what has to hold before the project calls itself beta is
in the [roadmap](/project/roadmap/#from-alpha-to-beta).

## Do I need Kubernetes?

No. The server is one binary that needs PostgreSQL. It runs on a host, in a container or on Kubernetes with the
[Helm chart](/docs/reference/helm-chart/). Collectors can run on Linux and Windows hosts at edge sites and in data centers, or
in Kubernetes pods. A development setup needs no container runtime at all: see
[Local development](/docs/getting-started/local-dev/). To try it with containers on one machine, use
[Docker Compose](/docs/getting-started/docker-compose/). To try it on Kubernetes, use
[Kubernetes on kind](/docs/getting-started/kubernetes-kind/); for a real installation see
[Install with Helm](/docs/operations/install-helm/) and [Install the collector with Helm](/docs/operations/install-collector-helm/).

## How does myriarch relate to the OpenTelemetry Operator and its OpAMP Bridge?

On Kubernetes, the Operator manages collectors as custom resources, and its OpAMP Bridge, in its default operator mode, reports them to an OpAMP server as one agent. Myriarch recognises a Bridge as an agent kind of its own. In operator mode it sends the Bridge nothing that changes a collector resource; a fleet configures a Bridge only in standalone mode and only with `spec.bridge.manage: true`. To run collectors that myriarch manages one by one, use the Helm chart `myriarch-collector`, which runs the supervisor and the collector in one pod. See [Bridge agents](/docs/concepts/fleets/#bridge-agents) and [Install the collector with Helm](/docs/operations/install-collector-helm/).

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
