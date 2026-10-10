# 0008. Kubernetes: supervisor and collector in one pod

- Status: Accepted, superseded in part by [ADR-0024](0024-container-registry-and-image-set.md) (own combined image) and refined by [ADR-0026](0026-collector-version-updates-on-kubernetes.md)
- Date: 2026-10-07
- Related: [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md)

## Context

Collectors in Kubernetes must be managed like collectors on hosts, or the fleet has two different agent models. Two options exist ([upstream notes](../design/upstream-notes.md#kubernetes-options)):

- **Supervisor and collector in one pod.** The supervisor runs next to the collector and speaks OpAMP for it. No official chart or operator mode runs the supervisor, so a custom image and small manifests are needed. Updating the version means changing the image, which restarts the pod.
- **OpenTelemetry Operator with the OpAMP Bridge.** The bridge is one agent per cluster. It reports the pod phase as health and replaces the whole `spec` of a collector resource, which gives the server broad control over the pod specification. Component-level health, inventory and rollback are not available the way they are with a supervisor.

## Decision

In Kubernetes we run the OpAMP Supervisor and the collector together in one pod. Each pod is one agent, with the same model as on a host: full health tree, effective configuration, component inventory and a remote configuration that changes only the collector configuration.

For version changes there are two paths: the pipeline bumps the image tag (the `webhook` executor drives it, [ADR-0006](0006-version-updates-via-exchangeable-executors.md)), or the pod swaps its collector binary itself through the container mode of `myriarch-updater` ([ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md)).

The OpenTelemetry Operator with the OpAMP Bridge is a possible later integration (see the [roadmap](../../ROADMAP.md)).

Two later records change parts of this decision. [ADR-0024](0024-container-registry-and-image-set.md) publishes no combined supervisor-plus-collector image; the pod is composed from the unmodified upstream images with init containers. [ADR-0026](0026-collector-version-updates-on-kubernetes.md) makes the `gitops-pr` executor the default for version changes on Kubernetes and keeps container mode as an opt-in.

## Consequences

### Positive

- One agent model on every platform.
- A small blast radius: a remote configuration reaches one collector and can change nothing else in the pod.
- Full health, effective configuration and component data per pod.

### Negative

- A deployment produces one agent per pod. A cluster with many nodes produces tens of agents, which is well within the design range.
- The pod needs the supervisor, its configuration and a storage directory (a persistent volume or host path, otherwise each pod start gets a new instance UID unless a fixed `agent.instance_id` is configured).
- The supervisor restarts a crashed collector inside the container, so Kubernetes does not see a container restart.

## Alternatives considered

- **OpenTelemetry Operator with the OpAMP Bridge**: one agent per cluster, health based on pod phase only, and a remote configuration that replaces the whole `spec`. Kept as a possible later integration.
