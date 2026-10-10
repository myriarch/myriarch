# 0014. Binary updates in the pod through container mode

- Status: Accepted, refined by [ADR-0026](0026-collector-version-updates-on-kubernetes.md)
- Date: 2026-10-07
- Related: [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md), [Container mode](../design/container-mode.md)

## Context

[ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md) changes the collector version in a pod by changing the image. In some environments that is not practical, such as air-gapped or edge clusters ([ADR-0026](0026-collector-version-updates-on-kubernetes.md)). Hosts already have a tested update path with download, verification, atomic swap, health check and rollback in `myriarch-updater`.

## Decision

The collector in a container can be updated without a new image. `myriarch-updater` offers a container mode (`myriarch-updater container`) in which it is the main process (PID 1) of the container, starts the OpAMP Supervisor as a child process and keeps the collector binaries in versioned directories on a persistent volume, with a `current` symlink.

- The chain is the same as on hosts: SHA-256 and Sigstore verification, a `--version` check, a validation of the effective configuration, the atomic swap, the health check and the automatic rollback. "Restart" means restarting the child process.
- **Precedence of image and volume**: at every start the updater compares the collector in the image with the active one on the volume. By default the higher version wins and a tie keeps the volume. A broken active binary falls back to the image binary. An image bump stays an explicit way upwards, and an emergency policy (`image`) always activates the image binary.
- **Boundary**: the image tag remains authoritative for the supervisor and for `myriarch-updater` itself. They are not updated over OpAMP.
- **Scaling**: the volume is a single-writer volume, so the deployment runs a single replica. More replicas would need a claim template per pod.

[ADR-0026](0026-collector-version-updates-on-kubernetes.md) refines this decision: container mode is an explicit opt-in for air-gapped and edge clusters, and the default on Kubernetes is the `gitops-pr` executor.

## Consequences

### Positive

- One update procedure on every platform.
- Updates in clusters without an image pipeline.
- A damaged volume cannot leave the pod without a working collector, because the image binary is the fallback.

### Negative

- The running binary differs from the declared image. Admission control, image scanning and drift detection of GitOps controllers see the image, not the binary that runs (the reason for [ADR-0026](0026-collector-version-updates-on-kubernetes.md)).
- A second code path and a persistent volume to operate.
- The pod needs headroom for two collector processes during validation and a long termination grace period, so that a shutdown during a swap can finish.

## Alternatives considered

- **Only image-tag changes**: remains the way for environments with a pipeline ([ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md)); it does not serve environments where the image is not changed for each collector version.
