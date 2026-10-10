# 0006. Version updates through exchangeable executors

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md), [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md), [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md), [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md)

## Context

The OpAMP Supervisor can manage configuration, health and restarts, but it cannot replace the collector binary. It accepts the `accepts_packages` setting, but its package manager answers "unsupported" at run time. The upstream work is tracked in [opentelemetry-collector-contrib #47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272) ([upstream notes](../design/upstream-notes.md#no-binary-updates)).

Fleets differ in how software reaches a host. Some hosts have no software distribution at all. Others already use configuration management or an automation platform. In Kubernetes the version is part of the image.

## Decision

A fleet's version update runs through an executor that the fleet selects in Git. The rollout engine, with its waves, windows and failure thresholds, is the same for every executor. Only the way the new version is brought to the agent differs.

| Executor | What it does |
|---|---|
| `none` | The version is only reported, never changed. |
| `updater` | Offers a package to `myriarch-updater` on the host over OpAMP. |
| `webhook` | Sends a signed HTTP call to an external system, for example a pipeline that bumps an image tag. |
| `awx` | Launches an Ansible AWX job template. |
| `manual` | Makes no external call. The rollout tracks a change made outside the system. |

A target is complete only when the agent itself reports the new collector version. No callback from an external system marks a target as finished. An executor that reports an error fails the target immediately; one that never produces the new version ends at the apply timeout.

## Consequences

### Positive

- Hosts without software distribution get an update path (`myriarch-updater`); hosts with existing tooling keep it.
- One rollout model, one audit trail and one definition of "done" for all update paths.
- New executors can be added without touching the engine.

### Negative

- The `updater` executor needs the project's own host agent on every host, which is a privileged component ([security model](../security/model.md#host-side-checks-of-the-updater)).
- For the external executors the system can only observe the outcome; whether the external change was correct is judged by the reported version and health.
- Several executors mean several configuration surfaces and test paths.

## Alternatives considered

- **Wait for the supervisor to support packages (#47272)**: the feature is unfinished upstream, so there would be no update path today. The package offer format follows the upstream draft so that a supervisor-native executor can be added when it exists ([ADR-0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md)).
