---
title: "Documentation"
weight: 1
description: "A map of the myriarch documentation in six groups: tutorials, how-to guides, concepts, reference, operations and security, and the project."
---

Myriarch manages fleets of OpenTelemetry Collectors over OpAMP. Git holds the declared state, the web UI and the REST API
operate within it, and every action is audited. The documentation is organized by what you want to do with it, following the
[Diátaxis](https://diataxis.fr/) framework: learn by doing, solve a problem, understand, look something up. If you are new,
start with the tutorials, then read the concepts.

## Tutorials: learn by doing

[Getting started](getting-started/_index.md) takes you from a fresh clone to a running system: the build, the development mode
with an embedded PostgreSQL, the example GitOps repository, the simulator and the REST API. The central page is
[Local development](getting-started/local-dev.md); [Your first fleet and rollout](getting-started/first-fleet-and-rollout.md)
writes a fleet and a rollout policy and rolls out a change in waves, and [Docker Compose](getting-started/docker-compose.md)
starts the whole system from the release images. The tutorial for Kubernetes on a local kind cluster is planned for v0.2.0.
Until then, the kind probe is described in [hack/kind/README.md](../hack/kind/README.md).

## How-to guides: solve a problem

[How-to guides](tasks/_index.md) are step-by-step instructions for one job each, for example
[connect a host](tasks/connect-a-host.md), [validate a GitOps repository](tasks/validate-a-gitops-repository.md),
[roll out a configuration change](tasks/roll-out-a-config-change.md),
[roll out a collector version](tasks/roll-out-a-collector-version.md),
[track releases and notify](tasks/track-releases-and-notify.md), [simulate a fleet](tasks/simulate-a-fleet.md),
[connect a real supervisor](tasks/connect-a-real-supervisor.md) and
[run two replicas locally](tasks/run-two-replicas-locally.md). To start your own GitOps repository, begin with the template
described in [gitops-template/README.md](../gitops-template/README.md).

## Concepts: understand how it works

[Concepts](concepts/_index.md) explains how the system works: [GitOps](concepts/gitops.md) as the source of truth,
[agents](concepts/agents.md) and their identity, [blueprints](concepts/blueprints.md) and how they are rendered,
[fleets](concepts/fleets.md) and the target version, the [inventory](concepts/inventory.md), [rollouts](concepts/rollouts.md)
in waves and the [executors](concepts/executors.md) that apply version updates. The [architecture](architecture.md) describes
the components, the data flows and the rollout engine, and the [security model](security/model.md) the controls.

## Reference: look something up

[Reference](reference/_index.md) holds the exact definitions: the [GitOps schema](reference/gitops-schema.md) with every kind,
field, template function and issue code, the generated [command-line reference](reference/cli/_index.md), the generated
references for the [server configuration](reference/configuration.md), the
[environment variables](reference/environment-variables.md) and the [metrics](reference/metrics.md), the REST contract in
[api/openapi.yaml](../api/openapi.yaml), and the values of the [Helm chart](../deploy/helm/myriarch/README.md). The design
contracts in [design/](design/README.md) pin down protocols and behavior that code and tests follow.

## Operations and security: run it

[Operations](operations/_index.md) is for people who run myriarch: [Install with Helm](operations/install-helm.md) for
Kubernetes, [authentication](operations/authentication.md) (single sign-on, the bootstrap administrator, API tokens), the
[production checklist](operations/production-checklist.md) with the security-relevant settings, and
[troubleshooting](operations/troubleshooting.md).

Guides for upgrades, backup and restore, high availability, monitoring, air-gapped installation and an artifact mirror are
planned for v0.2.0.

The [security section](security/_index.md) describes the controls of the system: trust boundaries, agent and user
authentication, artifact verification, outbound connections and network policy. To report a vulnerability, read
[SECURITY.md](../SECURITY.md).

## Project: why and how it is built

The [decision records](adr/README.md) explain why the system is built the way it is. The [development guide](development.md)
covers the toolchain, the gates, the web UI and the end-to-end tests, and [releasing](releasing.md) describes the versioning
and the checks that a release tag must pass. The [roadmap](../ROADMAP.md) lists what is planned, and
the community files are [CONTRIBUTING.md](../CONTRIBUTING.md), [CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md),
[GOVERNANCE.md](../GOVERNANCE.md), [MAINTAINERS.md](../MAINTAINERS.md) and [CHANGELOG.md](../CHANGELOG.md).

## License

The documentation is licensed under CC-BY-4.0 ([license text](../LICENSES/CC-BY-4.0.txt)), and code snippets inside it are
licensed under Apache-2.0 ([LICENSE](../LICENSE)). The same holds for the other Markdown files of the repository, apart from
the exceptions named in the [README](../README.md#license). See [ADR-0018](adr/0018-license-and-copyright.md) and
[ADR-0030](adr/0030-reuse-compliance-and-third-party-notices.md).
