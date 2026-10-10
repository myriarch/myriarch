# 0026. Collector version updates on Kubernetes

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md), [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md), [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md)

## Context

Version updates run through exchangeable executors ([ADR-0006](0006-version-updates-via-exchangeable-executors.md)). Three packages exist under `internal/executor`:

- `manualexec` (type `manual`): the server tracks the rollout while the version is changed outside it, for example by an image-tag merge request or an operator.
- `updaterexec` (type `updater`): packages offers to the host updater, on hosts and in container mode ([ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md)).
- `webhookexec` (types `webhook` and `awx`): `webhook` is a generic HMAC-signed HTTP POST that can drive an image-tag bump as in [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md), and `awx` launches an Ansible AWX job template.

Git sets the frame for allowed versions, and the UI chooses within that frame ([ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md)). A rollout counts as finished only when the agent reports the new version ([ADR-0006](0006-version-updates-via-exchangeable-executors.md)).

On Kubernetes, swapping the collector binary in place makes the running binary differ from the declared image. That undermines admission control, image scanning and the drift status of GitOps controllers, which all assume that the image reference describes what runs.

## Decision

- The default on Kubernetes is a new **`gitops-pr` executor**. An update opens a pull or merge request in the deployment repository that bumps the collector image. Completion is still determined only by the version the agent reports ([ADR-0006](0006-version-updates-via-exchangeable-executors.md)). A later change adds the executor.
- The **container mode** of `myriarch-updater` (binary swap on a persistent volume, [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md)) remains an explicit opt-in, intended for air-gapped and edge clusters.
- A managing integration with the OpenTelemetry Operator and its OpAMP Bridge comes later. Until then, agents identified as Bridge-managed receive no remote configuration unless explicitly opted in.
- The server (`myriarchd`) gets no Kubernetes API access and holds no cluster credentials. The Helm chart grants no RBAC for workload mutation.

## Consequences

### Positive

- The declared image and the running binary stay identical.
- It fits the Git-as-frame principle: allowed versions come from Git, and the change itself is reviewed in Git.
- No central server holds cluster-wide credentials.
- The audit trail also exists in the deployment repository.

### Negative

- The server needs write access (token or app installation) to the deployment repository, and a GitOps controller must apply the change.
- Update latency depends on review and merge.
- Container mode remains a second code path to maintain.
- Users of the OpenTelemetry Operator and OpAMP Bridge have to wait for the integration.

## Alternatives considered

- **An in-cluster agent with minimal RBAC**: rejected because it needs cluster credentials, and if it patches workloads the image and binary diverge again.
- **OpenTelemetry Operator with OpAMP Bridge, bumping `spec.image`, as the default**: deferred; planned as a later integration.
- **Container mode as the default**: rejected because the image and the running binary diverge.
