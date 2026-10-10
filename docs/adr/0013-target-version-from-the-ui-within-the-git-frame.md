# 0013. Target version from the UI within the Git frame

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md), [Version selection](../design/version-selection.md)

## Context

Operators expect to start a collector update in the UI and to watch it run. [ADR-0003](0003-gitops-as-source-of-truth.md) makes Git the source of truth for declarative objects, and the target version of a fleet (`spec.collector.version`) is such an object. Changing it only through a merge request makes a routine update slow, and leaves the UI without a button that operators look for.

At the same time the version a fleet runs must stay governed: which versions are acceptable, whether a downgrade is allowed and which rollout policy applies are decisions for review in Git.

## Decision

Git sets the frame and the UI chooses within it.

- A fleet may declare `spec.update.selection` (additive, off by default). It states whether UI updates are allowed, which versions are selectable (a range or a list), whether a downgrade is allowed, whether a reason is required and which rollout policy applies.
- A single resolver (`domain.ResolveVersionTarget`) derives the effective target of a fleet from the Git target and the UI selection. The result is one of `git`, `ui`, `hold` or `none`. The planner, assignment, statistics, release pages, dashboard and artifact prefetch all use only this resolver.
- The selection is stored in its own table (`fleet_version_selections`, one row per fleet). It survives restarts and works with several replicas.
- Rollouts are still created only by the planner on the leader. The API stores the selection and asks the leader to plan.
- The history is the audit log: choices and clearing by the operator (including rejected attempts), and the automatic reconciliation by `gitops`.
- When Git changes after a choice, the most recent explicit intent wins. A new Git target or a withdrawn UI control means the Git target applies and the selection is deleted. If the chosen version is no longer inside the new frame, the fleet goes into **hold**: it has no target, a running rollout is superseded, and nothing is downgraded automatically. A deleted fleet drops its selection. The exact rules (K1, K2a, K2b, K4) are in [Version selection](../design/version-selection.md#conflict-rules).

## Consequences

### Positive

- Operators trigger and watch updates in the UI; Git still defines what is allowed.
- Every choice is attributed, reasoned and visible as "set in the UI".
- The behavior is the same with one replica or many.

### Negative

- Some conflict rules can lead to a downgrade (a new Git target that is lower than the running version, a withdrawn UI control, or clearing a selection). The documentation and the UI warn about it, and `myriarch-updater` refuses downgrades beyond one step back unless the host policy allows them.
- The effective target now has two sources, which makes reasoning about "what does this fleet run" more complex than with Git alone.

## Alternatives considered

- **The UI writes to Git (creates the merge request)**: needs write access to the repository and a review process for every operational decision. It is planned only for Kubernetes version updates ([ADR-0026](0026-collector-version-updates-on-kubernetes.md)).
- **The selection as part of the GitOps snapshot**: would create drift against Git and could not be audited.
