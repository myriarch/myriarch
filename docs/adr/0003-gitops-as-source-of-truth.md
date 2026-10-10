# 0003. GitOps is the source of truth; the UI operates within the Git frame

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md), [ADR-0004](0004-server-side-rendering-to-one-content-addressed-config.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md)

## Context

A fleet manager changes the configuration of thousands of collectors. Such changes need review, a history, a way to go back, and validation before they reach a single agent. Operators also expect to start and watch an operation, such as a rollout, in the web UI.

Two ways to hold declarative objects (blueprints, fleets, rollout policies, executors, inventory) are possible: in the server's database with a UI to edit them, or in a Git repository that the server reads.

## Decision

Declarative objects live in a Git repository. The server reads them (poll or webhook), validates the whole repository, and applies a revision atomically or not at all ([GitOps concepts](../concepts/gitops.md)).

- The UI and the API are operational. They start, pause, approve, abort and retry rollouts, acknowledge releases, and manage tokens. They do not edit declarative objects.
- CI validates the repository before a merge with `myrctl validate` ([reference](../reference/gitops-schema.md)).
- The target version of a fleet is the one deliberate exception: Git decides whether and within which frame an operator may choose it in the UI (`spec.update.selection`, [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md)). The choice itself is an audited operational decision, like pausing a rollout.

## Consequences

### Positive

- Every change goes through the review process of the Git host and leaves a commit history.
- An invalid revision never becomes active: the previously applied state stays in force and the status shows the error.
- Validation runs in CI with the same code the server uses.

### Negative

- An operational rollback in the UI does not change Git. The difference between what runs and what Git declares is resolved by reverting in Git.
- Creating merge or pull requests from the UI is not part of the first releases (see the [roadmap](../../ROADMAP.md)); [ADR-0026](0026-collector-version-updates-on-kubernetes.md) plans it for Kubernetes version updates.
- Operators who want to edit a fleet in the UI cannot do so.

## Alternatives considered

- **Edit the declarative objects in the UI**: stays out of scope; it would lose review, history and CI validation. Editing declarative objects in the UI is listed under "Not planned" in the [roadmap](../../ROADMAP.md).
