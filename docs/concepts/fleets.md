---
title: "Fleets"
weight: 40
description: "How fleets select agents, how the highest priority wins, when the offered configuration changes, and how the target collector version is chosen in the UI within a frame set in Git."
---

A **fleet** groups agents by their attributes and decides what they run: which blueprint is rendered, which parameter
values apply, which collector version is wanted, how a version is changed, and which rollout policies apply to
configuration and version changes.

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata:
  name: linux-hosts
spec:
  displayName: Linux hosts
  priority: 100                    # the higher priority wins when selectors overlap
  selector:
    matchAttributes: {os.type: linux}
    matchExpressions:
      - {key: site.type, operator: In, values: [edge, datacenter]}
  blueprint: linux-host            # required
  parameters:                      # values for the blueprint parameters
    gatewayEndpoint: gateway.example.com:4317
  collector:
    distribution: otelcol-contrib  # the default
    version: 0.162.0               # the Git target, without "v"; empty = versions are not managed
  update:
    executor: updater              # none (default) | updater | webhook | awx | manual
    selection:                     # optional: the frame for choosing the target version in the UI
      enabled: true
      versionRange: ">=0.161.0, <=0.162.0"
  rollout:
    configPolicy: config-canary    # default: "default"
    versionPolicy: canary-then-all # default: "default"
```

All fields are listed in the [schema reference](../reference/gitops-schema.md#fleet). This example is the fleet
`linux-hosts` from [`examples/quickstart/`](../../examples/quickstart/).

## Selectors

A selector chooses agents by their [attribute view](agents.md#the-attribute-view).

- `matchAttributes` requires exact equality for each key.
- `matchExpressions` are set-based conditions with a `key`, an `operator` and `values`:
  - `In`: the attribute exists and its value is one of `values`.
  - `NotIn`: the attribute is missing, or its value is not in `values`.
  - `Exists` and `DoesNotExist`: the attribute exists or is missing (no `values`).
  - `Glob`: the attribute exists and matches one of the patterns, using the syntax of Go's `path.Match`, for example
    `edge-0*`.
- All conditions must hold (logical AND). An **empty selector matches every agent**.
- Keys are keys of the attribute view. Server labels are written `labels.<key>`.

The same selector type is used by rollout waves ([Rollouts](rollouts.md)).

## Assignment and priority

For every collector the server evaluates the fleets in this order:

1. Apply the labels of the [inventory](inventory.md). They are visible to selectors.
2. Collect all fleets whose selector matches. These are the candidates.
3. The candidate with the **highest `priority`** wins. If several candidates share the highest priority, the agent stays
   **unassigned** and its assignment error (`assignment.error`) reads `ambiguous: fleets a, b have equal priority N`.
4. With no candidate the agent is unassigned and has no error.
5. Only agents of the kind `collector` are assigned. A `myriarch-updater` follows its collector.

Assignment is evaluated again when an agent registers, when its description changes, and for every applied GitOps
revision. Give fleets that can match the same agents different priorities. The three quickstart fleets use disjoint
selectors instead, so no agent is ambiguous.

## Bridge agents

*Available from v0.2.0.*

The OpAMP Bridge of the OpenTelemetry Operator connects as one agent for many collectors. In operator mode its remote
configuration is the complete set of the collector resources it manages: an entry replaces a resource, and a message that
lacks the entry of a managed resource deletes it. The server therefore does not treat it like a collector, and a fleet
configures a Bridge only when you say so. If you run the Operator, its Bridge keeps managing the collector resources in
the cluster; myriarch shows it in the inventory and leaves its configuration alone.

- **Recognition.** An agent is a Bridge when it reports the `service.name` `io.opentelemetry.operator-opamp-bridge`
  (operator mode) or the attribute `opentelemetry.io/opamp.bridge.mode` (`operator` or `standalone`; both modes since
  Operator 0.157.0). The agent reports both itself, so this protects against mistakes and is no authentication. The
  details are in the [upstream notes](../design/upstream-notes.md#opamp-bridge-identity-modes-and-remote-config).
- **Opt-in.** `spec.bridge.manage: true` lets a fleet configure the Bridges that its selector matches. The default is
  `false`: a Bridge never receives remote configuration from a fleet without the opt-in.
- **Standalone mode only.** The opt-in takes effect for Bridges in standalone mode, where each client of the Bridge
  manages one workload. A Bridge in operator mode stays locked with or without the opt-in, and so does a Bridge that
  reports no known mode.
- **Warning.** `myrctl validate` and the GitOps sync warn with `bridge_selected` when a fleet without the opt-in has a
  selector that also matches a Bridge. An empty selector does, and so does a selector made only of `NotIn` and
  `DoesNotExist` conditions. The check matches the selector against a synthetic Bridge in each mode (the attributes the
  Bridge reports itself, the [inventory](inventory.md) applied) and against every sample agent that is a Bridge. It
  ignores priorities and cannot evaluate `labels.*` of enrollment tokens. With `--strict` the warning fails the check.
- **What the server holds back.** The server stores a Bridge as its own agent kind, `bridge`, and enforces the rules
  itself; the Git warning only helps you to find a selector that is too broad.
  - *Remote configuration:* a Bridge is a member of the fleet that selects it, but it is offered no configuration unless
    that fleet sets `spec.bridge.manage: true` and the Bridge runs in standalone mode. This holds whatever starts the
    offer: the first message, a heartbeat, a push, a resync or a reconnect.
  - *Restarts:* the restart command is refused for a Bridge, with and without the opt-in. The REST action answers `412`
    and the audit log records the attempt. Restart the workloads through Kubernetes.
  - *Version rollouts:* a Bridge is never a target, not even of a rollout that began before the agent was recognised.
    A Bridge that the fleet configures takes part in config rollouts like a collector. Change a version with `spec.image` of the collector resources in your
    deployment repository, or with a `gitops-pr` executor in a fleet of collectors.
  - *Effective configuration:* the server keeps none for a Bridge, because it holds the complete collector resources.
  - Every action that is held back is an agent event of the type `bridge_locked` and an audit entry
    (`agent.bridge_held_back`) that name the action and the reason. The server writes them once per reason and
    configuration on a connection, not with every heartbeat.
- **Keeping a Bridge out of a fleet.** Select on what a collector reports and a Bridge does not: `os.type`, `site.type`
  and the other `site.*` attributes. Or give the Bridge its own enrollment token and select on its `labels.*`.

The field is listed in the [schema reference](../reference/gitops-schema.md#fleet). The decisions behind these rules are in
[ADR-0037](../adr/0037-opamp-bridge-agents-until-the-operator-integration.md).

## When does the offered configuration change?

Every agent has three configuration hashes: the **assigned** one (what the current revision renders for it), the
**desired** one (what the server offers right now) and the **failed** one (a desired hash the agent reported as failed).
They are described in [Agents](agents.md#assigned-desired-and-failed-configuration).

The rules for the desired hash are:

- **Bootstrap.** An agent that has no desired hash yet (it registered just now), or that moves to another fleet, gets
  desired equal to assigned immediately, and the configuration is pushed.
- **Otherwise only a rollout changes it.** If the assigned hash changes later, for example because a blueprint, a
  parameter or an attribute changed, the desired hash stays as it is until a [rollout](rollouts.md) admits the agent.
  The planner picks such agents up in its next run. In the meantime the agent shows the sync state `drift`.
- **Catch-up.** A completed configuration rollout leaves agents that were offline as `skipped`. If the assigned hash of
  such an agent equals the target hash of its skipped target, the change was approved already. The planner then sets
  desired to assigned at once, without creating a rollout, and the agent receives the configuration the next time it
  connects.

## Target version from the UI within the Git frame

The target version of a fleet has two sources that work together by a fixed rule:

- **Git** decides *whether* and *within which frame* the UI may choose the target version (`spec.update.selection`), and
  which rollout policy then applies. `spec.collector.version` stays the **Git target**.
- **The UI** (the fleet page or the release page, "Update to version X"; REST
  `POST /api/v1/fleets/{name}/actions/select-version`) chooses a version **inside** that frame. The choice is stored in
  the database (the table `fleet_version_selections`, one row per fleet), not in Git. Its history is the audit log.

Principles and decisions are in
[ADR-0013](../adr/0013-target-version-from-the-ui-within-the-git-frame.md). The normative contract with the complete
API, events and audit trail is [Version selection](../design/version-selection.md).

### The frame

The block `spec.update.selection` has the fields `enabled`, `versionRange`, `versions`, `allowDowngrade`,
`requireReason` and `rolloutPolicy`, listed in the [schema reference](../reference/gitops-schema.md#fleet). If the
block is missing, `enabled` is false and the fleet behaves as if the feature did not exist.

- `versionRange` is a constraint in the syntax of Masterminds semver v3 over versions without `v`, for example
  `">=0.161.0, <=0.162.0"`. A prerelease does not satisfy a range that has no prerelease part.
- `versions` is an explicit list of selectable versions. Exactly one of `versionRange` and `versions` is required.
- `allowDowngrade: false` (the default) allows only versions greater than or equal to the current effective target. With
  no target it compares with the highest version among the member collectors.
- `requireReason: true` makes a reason mandatory when choosing and when taking a choice back.
- `rolloutPolicy` names the policy for rollouts caused by a choice in the UI. The default is `rollout.versionPolicy`,
  then `default`.

Validation runs when the server syncs and in `myrctl validate`, with the code `selection_invalid` unless noted:

- `enabled: true` without `versionRange` and `versions`, or with both;
- a `versionRange` that cannot be parsed;
- an entry in `versions` that is not a strict semantic version, has a leading `v`, is empty or appears twice;
- `enabled: true` with the executor `none`, because UI-triggered updates need an update executor;
- a `collector.version` that lies outside the frame;
- a `rolloutPolicy` that does not exist (`policy_not_found`);
- as a **warning**: `enabled: false` together with `versionRange` or `versions`, which are then ignored.

A typing mistake in the block, such as `versionrange`, is an `unknown_field` like everywhere else. The warning that
`collector.version` is empty while an executor is set does not appear when `selection.enabled` is true, because the
version then comes from the UI.

### The effective target

One function decides to which version a fleet is rolled out. The planner, the assignment (`targetVersion` of the
agents), the fleet statistics, the release page, the dashboard and the artifact prefetch all use it.

| Case | Effective version | Source (`versionTarget.source`) |
|---|---|---|
| An active choice from the UI | the chosen version | `ui` |
| A choice that became invalid (rule K2b) | none | `hold` |
| No choice, `collector.version` set | the Git target | `git` |
| No choice, no Git target | none | `none` |

### Conflict rules

When Git changes after a choice was made, a fixed rule applies: **the most recent explicit intent wins, and there are no
surprise downgrades.** The leader evaluates the rules in this order after every applied revision (before agents are
reassigned) and at the start of every planner run. The first matching rule wins. Everything is idempotent and audited
with the actor `gitops`.

| Rule | Condition | Effect | Audit action |
|---|---|---|---|
| K4 | The fleet no longer exists in Git. | The choice is deleted. | `fleet.version.removed` |
| K1 | `collector.version` in Git differs from the Git target at the time of the choice (changed, newly set or removed). | The choice is deleted and the new Git target applies. Git wins. | `fleet.version.superseded` |
| K2a | `selection` is missing or disabled, or the executor is `none`. | The choice is deleted and the Git target applies. Git withdrew the UI control. | `fleet.version.superseded` |
| K2b | The chosen version is no longer inside the frame. | The choice stays as `invalidated`, with the reason (version, commit, frame). The fleet is on **hold**: no target. A running version rollout becomes `superseded` ("the fleet no longer manages the collector version"). Collectors that were already updated stay on their version. | `fleet.version.invalidated` |
| K5 | none of the above | Unchanged. | none |

A hold ends in one of three ways: a new valid choice in the UI, taking the choice back (`POST
/api/v1/fleets/{name}/actions/clear-version-selection`, after which the Git target applies), or a change of
`collector.version` in Git (rule K1). Widening the frame later does **not** reactivate an invalidated choice.
`allowDowngrade` and `requireReason` are checked only when a version is chosen, never during reconciliation.

Consequences worth knowing:

- K1, K2a and taking a choice back can cause a **downgrade** if the Git target is lower than the running version. The
  `myriarch-updater` refuses downgrades other than the return to the immediately previous version (a host policy that
  fails the target). The UI warns before a choice is taken back.
- After a **rollback** of a version rollout, by the updater or by an operator, the choice stays active. The targets are
  `failed` or `rolled_back` and are not rolled out again (see the exclusion rules in
  [Rollouts](rollouts.md#when-do-rollouts-arise)). The operator retries the failed targets or takes the choice back.
- While a version rollout of the fleet is `failed` or `rolling_back`, a new choice is refused with HTTP 412. Abort the
  rollout first.
- A rollout caused by a choice in the UI has the `origin` `ui`, the display name of the operator as `createdBy`, the
  reason as `requestReason` and the annotation `selection` (version, who, when, why, commit). Rollouts whose target comes
  from Git have the `origin` `gitops`. The database stores the actor reference in `created_by` (`gitops`, `user:<uuid>` or
  `token:<uuid>`), and the API shows display names.

### API, permission and audit

| Operation | Purpose |
|---|---|
| `GET /api/v1/fleets/{name}/version-options` | The data of the update dialog: the effective target, the frame, and every candidate version with `selectable` and a `reasonCode`. |
| `POST /api/v1/fleets/{name}/actions/select-version` | Stores a choice and asks the leader to plan the fleet. |
| `POST /api/v1/fleets/{name}/actions/clear-version-selection` | Deletes the choice. The Git target applies again. Allowed even when `selection.enabled` is false. |

Both actions need the permission `fleets:set_version`, which the roles `operator` and `admin` have. The REST contract is
[`api/openapi.yaml`](../../api/openapi.yaml).

**Errors of `select-version`.**

| HTTP status | Code | Cause |
|---|---|---|
| 400 | `bad_request` | The version is not a semantic version without `v`, or the reason is longer than 500 characters. |
| 412 | `precondition_failed` | `selection.enabled` is false, the fleet has no update executor, or a version rollout of the fleet is `failed` or `rolling_back`. |
| 409 | `conflict` | `expectedTargetVersion` does not match the effective target, or the choice was changed by someone else meanwhile. |
| 422 | `validation_failed` | One or more issues, listed below. |

A 422 answer carries these issue codes in `issues[].code`:

| Issue code | Meaning |
|---|---|
| `reason_required` | `requireReason` is set and no reason was given. This also applies to `clear-version-selection`. |
| `version_not_allowed` | The version is outside the frame. |
| `prerelease_not_allowed` | The version is a prerelease. |
| `unknown_version` | The version is not a known release of the distribution and not listed in `selection.versions`. |
| `downgrade_not_allowed` | `allowDowngrade` is false and the version is lower than the current effective target (without a target: lower than the highest member version). |

Choosing the version that already is the effective target changes nothing and answers 200 with `planned: false`.

**Waiting for the plan.** The rollout is created only by the planner on the leader replica, so there is one writer even
with several replicas. The handler stores the choice, announces it on the event bus (`fleet.version_changed`), and waits
up to 10 seconds until the leader has planned the fleet (`plannedAt` is set). It then answers with the `rolloutId` and
`planned: true`. Otherwise it answers `planned: false`, and the UI waits for the events `fleet.updated`
or `rollout.updated`.

**Audit.** Operator actions are recorded with the target type `fleet`: `fleet.version.select` and
`fleet.version.clear`, **including refused attempts** (`result: failure` with `details.error`). Git-driven
reconciliation records `fleet.version.superseded`, `fleet.version.invalidated` and `fleet.version.removed` with the actor
`gitops`. The rollout that follows is recorded as `rollout.create` with `details.origin: ui`. The history of a fleet is
available at `/api/v1/audit?targetType=fleet&targetId=<fleet>`.
