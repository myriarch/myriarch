---
title: "Rollouts"
weight: 60
description: "How changes reach a fleet: rollout policies, waves, targets and success, maintenance windows, failure handling, the planner and the live progress of an update."
---

A **rollout** moves a change to the agents of one fleet in a controlled way. There are two kinds:

- a **config rollout** brings agents to a new rendered configuration (a new desired hash), and
- a **version rollout** brings collectors to a new collector version, using an [executor](executors.md).

How a rollout proceeds is described by a **RolloutPolicy**. The rules below are implemented by the rollout engine,
which runs on the leader replica. The design is in
[`docs/architecture.md`](../architecture.md) and in the
[server design](../design/opamp-server.md).

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: RolloutPolicy
metadata:
  name: canary-then-all
spec:
  waves:
    - name: canary
      selector: {matchAttributes: {labels.wave: canary}}   # an explicit canary wave
    - name: rest
      percent: 100
      requireApproval: true            # starts only after an operator approves
  maxConcurrent: 5                     # targets in progress at the same time (0 = unlimited)
  healthWait: 5m                       # how long a target must stay healthy after success
  applyTimeout: 30m                    # the longest time until success is reported
  offlineGracePeriod: 30m              # how long a wave waits for offline agents
  failureThreshold: {maxFailed: 1}
  onFailure: pause                     # pause (default) | rollback
  timezoneAttribute: site.timezone
  defaultTimezone: UTC
  maintenanceWindows:
    - {days: [Mon, Tue, Wed, Thu, Fri], start: "22:00", end: "05:00"}
    - {days: [Sat, Sun], start: "00:00", end: "00:00", timezone: UTC}
```

This is the policy from [`examples/quickstart/policies/`](../../examples/quickstart/policies/). All fields and their
defaults are in the [schema reference](../reference/gitops-schema.md#rolloutpolicy).

## States

A rollout has one of these statuses: `pending`, `running`, `waiting_approval`, `paused`, `completed`, `failed`,
`aborted`, `rolling_back`, `rolled_back` or `superseded`. The statuses `completed`, `aborted`, `rolled_back` and
`superseded` are final. `failed` is not final but has no automatic transition: a person decides (`rollback`, `abort` or
`retry-failed`). A wave is `pending`, `waiting_approval`, `running`, `completed`, `failed` or `skipped`. A target is
`pending`, `waiting_window`, `in_progress`, `verifying`, `succeeded`, `failed`, `skipped`, `cancelled` or `rolled_back`.

## Which policy a fleet uses

A fleet names one policy for configuration changes (`rollout.configPolicy`) and one for version changes
(`rollout.versionPolicy`). Without a reference the policy named `default` is used. If no object with that name exists,
the built-in policy applies: one wave of 100 percent, `maxConcurrent: 50`, `healthWait: 60s`, `offlineGracePeriod: 15m`,
`failureThreshold.maxFailed: 1`, `onFailure: pause`, no maintenance windows, and the default `applyTimeout` of the kind
(10 minutes for configuration, 30 minutes for versions). A rollout stores a snapshot of the policy with all defaults
filled in, so later edits in Git do not change a running rollout.

## Waves

- Every wave has **either** a `selector` **or** a `percent` (1 to 100), otherwise the policy is invalid (`wave_invalid`).
  The `percent` values of the waves must strictly increase.
- When a rollout is created, its targets are distributed over the waves (a snapshot):
  - A **selector wave** takes all targets that are not yet assigned and whose attribute view matches.
  - A **percent wave** takes not yet assigned targets in a stable pseudo-random order (ascending by
    `sha256(rolloutID || agentID)`) until the **cumulative** share of all targets reaches
    `ceil(percent x total / 100)`. Targets of earlier selector waves count toward that share.
  - If the last wave does not reach 100 percent, a final wave `rest` with 100 percent is appended implicitly. This also
    happens when the last wave is a selector wave.
  - Waves without targets are marked `skipped`.
- Waves run **one after the other**. Wave n+1 starts when every target of wave n has a final state (`succeeded`,
  `failed`, `skipped` or `cancelled`) and the failure threshold was not reached.
- With `requireApproval` the wave (and the rollout) goes to `waiting_approval` and starts only after
  `POST /api/v1/rollouts/{id}/actions/approve` with `{"wave": n}`, which the UI offers as a button.

## Targets and success

A **target** is one agent within a rollout.

- **Start** (`pending` to `in_progress`). A target starts only if the agent is online, its local maintenance window is
  open, and `maxConcurrent` is not exhausted. For a configuration, the desired hash of the agent is set to the target hash
  and pushed. For a version, the executor's `Start` is called (an updater package offer, a webhook or an AWX job).
- **Success reported** (`in_progress` to `verifying`). For a configuration the agent reports `remote_config_status =
  APPLIED` for the target hash. For a version the collector reports `service.version` equal to the target version.
  Success is **never** reported by an executor.
- **Success** (`verifying` to `succeeded`). The agent stays online and healthy for `healthWait`.
  - An update or a configuration change restarts the collector. An unhealthy report that was sent **before** the target
    entered `verifying`, from the old process that is stopping, does not count as a failure. The target waits, for at most
    2 minutes, for the health report of the new process.
  - The normal start-up of the new process is no failure either. The real supervisor first reports the new version with
    `healthy=false` and no status, then the collector reports `StatusStarting`, and only then `StatusOK`. Such start-up
    reports (unhealthy with the status `StatusStarting`, `StatusStopping` or no status, and without `lastError`) let the
    target wait in `verifying` for at most `applyTimeout`. If the process is not healthy by then, the target fails.
  - Any other unhealthy report (an error status or a `lastError`) fails the target.
  - **Crash loops** are detected for version rollouts: if the collector was healthy once on the new version and then falls
    back to a start-up report more than once, the target fails. A single restart after the update, for example because
    a configuration was applied, is allowed.
- **Failure** (`failed`). The agent reports `FAILED` (configuration), or the updater reports `INSTALL_FAILED`. Or the
  agent becomes unhealthy or goes offline during the health wait, `applyTimeout` is exceeded, or the executor returns an
  error.
- **Offline.** Targets that are offline at their planned start wait. After `offlineGracePeriod`, counted from the start
  of the wave, they become `skipped`.
- **Abort.** Targets that have not started become `cancelled`.
- **One of each kind per fleet.** At most one configuration rollout and one version rollout run at the same time for a
  fleet, independent of each other. If a version update restarts the collector of a host while its configuration target
  is in the health wait, the configuration target fails. Use `retry-failed`, or finish the version rollout first.
- **Supersede.** When the target changes (a new rendered hash or a new target version), a new rollout replaces the
  running one. This applies to rollouts in `pending`, `running`, `waiting_approval` and `paused`. For `paused` rollouts,
  failed targets count as well, so that a fix in Git replaces a paused rollout. Rollouts in `failed` (for example because
  the fleet was removed from Git) and in `rolling_back` are **never** replaced automatically. A person confirms with
  `abort`, or the rollback runs to its end.
- `resume` after a failure-threshold pause acknowledges the failures so far (the rollout annotation `ackedFailures`
  lists per wave the acknowledged failed attempts as `<agentId>#<attempt>`). `retry-failed` resets failed targets and
  increases the attempt counter, so a new failure of the same agent counts toward the threshold again.
- Starting a version through a webhook or AWX is *at-least-once*. A deterministic `X-Myriarch-Delivery` ID lets the
  receiver de-duplicate calls.

## Health wait

The **health wait** is the time that a target must stay healthy after it has reported success. It is the safety margin of
a rollout: a collector that crashes, restarts in a loop or turns unhealthy shortly after a change fails its target while
the rollout is still working on its current wave.

- **Rule.** A target enters `verifying` when its agent reports the new configuration (`APPLIED`) or the new version. The
  server stores that moment as `verifiedAt` of the target; for a version rollout it is also the start of the step
  `health_wait`. The target becomes `succeeded` when the agent is still online, still reports the target configuration or
  version, is healthy and `now - verifiedAt >= healthWait`. The health wait therefore ends at `verifiedAt + healthWait`.
- **Value.** `healthWait` is `spec.healthWait` of the [rollout policy](../reference/gitops-schema.md#rolloutpolicy). A
  policy that sets none, or `0`, waits 60 seconds. A rollout keeps the value of its policy snapshot, so an edit of the
  policy in Git changes neither a rollout that already runs nor its health waits. The wait is a trade-off: a longer wait
  gives a bad change more time to show itself before the rollout goes on, and it makes every round of `maxConcurrent`
  targets last that much longer. The two Quickstarts ([Docker Compose](../getting-started/docker-compose.md) and
  [Kubernetes on kind](../getting-started/kubernetes-kind.md)) use the demo policies in
  [`examples/quickstart/policies/`](../../examples/quickstart/policies/) with `healthWait: 30s`, `maxConcurrent: 0`
  (unlimited) and no maintenance windows, so that a rollout is `succeeded` about a minute after you approve the last wave.
  Those are demo values. The policies of [`gitops-template/config/policies/`](../../gitops-template/config/policies/) are
  production-like: `2m` for a configuration, `5m` for a version, and `maxConcurrent: 5`; their comments say why. When a
  rollout "hangs" in the Quickstart, look at the health wait first: it is the time a target spends in `verifying`.
- **When it ends.** The engine checks the rule on every tick (`rollouts.tickInterval`, 5 seconds by default, and sooner
  after agent events), so a target finishes up to one tick after its wait has ended. It can take longer when the agent has
  not yet reported the health of the new process: the old process that is stopping and the normal start-up of the new one
  neither fail the target nor let it succeed (see [Targets and success](#targets-and-success)).
- **Failure during the wait.** A target whose agent goes offline, reports an error or no longer reports the target
  configuration or version fails at once, and the wait does not start again within an attempt. `retry-failed` starts a new
  attempt with a new wait.
- **Pause.** `pause` stops new targets from starting, but a target that is in `verifying` keeps its wait and the engine
  evaluates it to the end, also while the rollout is `paused` or `waiting_approval`. The engine does not evaluate a rollout
  in the status `failed`: its targets stay in `verifying` until an operator aborts, rolls back or retries the rollout. A
  rollout that rolls back resets its targets and does not let them finish.

*Available from v0.2.0.* The rollout page shows the remaining time:

- **Where.** In the status column of the target list, for every target in `verifying`. In the step *Health wait* of the
  progress of a version target. On the card of a wave, as the number of its targets in the health wait and the time until
  the wait that ends last is over.
- **At 0** the page says "finishing…" until the engine has evaluated the target. The count does not stop while the
  rollout is paused, because the engine does not stop either, and it disappears when the target reaches a final status. The
  page shows no remaining time for a rollout that the engine does not evaluate (the statuses `failed`, `rolling_back` and
  the final ones).
- **Source.** The step and the wave card also say where the wait comes from, in a line like
  `Policy canary-then-all · healthWait 5m · policies/canary-then-all.yaml@9f3c2a17`. The policy name links to the policy.
  Path and commit belong to the policy snapshot of the rollout and can differ from the policy as it is now. The built-in
  policy has no file, and the line says so instead of the path.
- **No field for the end.** The API has no field for the end of the wait. The browser computes `verifiedAt` plus
  `policy.healthWait` from the targets of `GET /api/v1/rollouts/{id}/targets` and from `GET /api/v1/rollouts/{id}`, with
  the rule above (60 seconds when the snapshot has no value), and counts down with its own clock. A browser clock that
  differs from the clock of the server shifts the display, not the decision of the engine.

## Maintenance windows

- A target may **start** only while the current time is inside at least one window. Running targets are not
  interrupted. Without windows a target may start at any time.
- The time zone is the `timezone` of the window. `agent` (the default) means the value of the agent attribute named by
  `timezoneAttribute` (default `site.timezone`). If that is missing or not a valid zone, the policy's `defaultTimezone`
  is used, then the server setting `rollouts.defaultTimezone` (default `UTC`), then UTC. Times are computed with the
  IANA database, including daylight saving time. The zone name `Local` is never accepted, so the result does not depend on
  the server host.
- `days` lists the weekdays on which a window **begins** (`Mon` to `Sun`, empty means every day).
- If `end` is not later than `start`, the window crosses midnight and ends on the next day (`22:00` to `05:00`). With
  `start` equal to `end` the window lasts 24 hours from `start`.
- Targets outside a window have the status `waiting_window`.

## Failure threshold, pause and rollback

- The threshold is checked per wave. It is reached if `maxFailed > 0` and the number of failed targets is at least
  `maxFailed`, **or** `maxFailedPercent > 0` and `failed x 100 >= maxFailedPercent x targets of the wave`. The wave
  becomes `failed`. If both values are 0, `maxFailed: 1` applies. If only `maxFailedPercent` is set, only the
  percentage rule is used.
- `onFailure: pause` pauses the rollout (`paused`, with the reason in `statusReason` and the notification
  `rollout.paused`). Running targets are evaluated to the end, no new ones start. The operator chooses `resume`,
  `retry-failed`, `abort` or `rollback`.
- `onFailure: rollback` rolls back automatically. All targets in `in_progress`, `verifying`, `succeeded` or `failed` are
  reset to their starting state: for a configuration the desired hash returns to the `from` hash, for a version the
  package offer or executor call is repeated with the previous version. They end as `rolled_back`, and the rollout goes
  through `rolling_back` to `rolled_back` (notification `rollout.rolled_back`).
- The rollout status `failed` (notification `rollout.failed`) arises only when a rollback cannot finish within
  `applyTimeout`, or when the rollout cannot continue at all, for example when the executor is misconfigured and all
  startable targets of a wave fail in `Start` or `Validate`, or when the policy or the fleet disappeared.
- A rollback contradicts Git. The agent shows `drift` until Git is reverted or a new commit starts the rollout again.
  Targets that the rollback cancelled without ever starting them are **not** rolled out again either (see below),
  otherwise the rejected change would reach the rest of the fleet wave by wave. Agents that moved to another fleet
  meanwhile are not touched by the rollback and become `skipped`.

## When do rollouts arise?

The planner runs on the leader replica after every applied revision and additionally once a minute. Runs are serialized.
There is **at most one active rollout per fleet and kind**.

**The target per agent** (only assigned collectors without an assignment error):

- *Configuration.* The assigned hash. An agent is relevant if its desired hash is set and differs from the assigned one.
  (Agents without a desired hash get the bootstrap rule, see [Fleets](fleets.md#when-does-the-offered-configuration-change).)
- *Version.* The **effective** target version of the fleet ([Fleets](fleets.md#the-effective-target): an active choice in
  the UI, else `collector.version`). An agent is relevant if the fleet's executor is not `none`, an effective target exists
  (so not on hold) and the agent's version differs from it.

**Exclusions** for a relevant agent. The last target of the agent for this target is:

- `failed` or `rolled_back`. The change failed there. Only a new commit, a new version or `retry-failed` by an operator
  starts it again.
- `cancelled` in a rollout with the status `aborted`, `rolling_back` or `rolled_back`. An operator or the failure policy
  stopped or rolled back on purpose. The change is rolled out again only with a new target.

A target that was `cancelled` because of `superseded` is **not** excluded. A `skipped` target of a completed
**configuration** rollout leads to the catch-up (no rollout) described in
[Fleets](fleets.md#when-does-the-offered-configuration-change). For **versions** there is no catch-up. Such agents are
relevant as usual (stragglers or a new rollout, so that maintenance windows and `maxConcurrent` apply). One exception
applies: an agent whose version target became `skipped` because of `offlineGracePeriod` gets a new rollout only after
it has connected again since. Otherwise a permanently switched-off edge machine would trigger a new rollout, with its
`rollout.started` and `rollout.completed` notifications, after every completed rollout.

**The procedure per fleet and kind:**

0. Before anything else the planner reconciles the choices made in the UI with Git (rules K4, K1, K2a and K2b from
   [Fleets](fleets.md#conflict-rules)) and writes the effective target version to the member agents.
1. If an active rollout exists whose open targets (`pending`, `waiting_window`, `in_progress`, `verifying`) have a
   **different** target than their agent's current target (a new commit with another hash, a changed target version, a
   new choice in the UI, a hold), that rollout becomes `superseded`. Its open targets are cancelled, and for versions
   `Cancel` is called on the executor. Continue with step 3.
2. If an active rollout exists and all its open targets still have the current target, relevant agents that have **no**
   target in it yet (stragglers, for example agents that connected later) are added to the **last wave**. Done.
3. Without an active rollout, a new rollout is created with all relevant, not excluded agents. Its `toRef` is the current
   commit or target version, and its targets carry the `fromRef` and `toRef` hashes or versions. If no agent is relevant,
   no rollout is created.

**Origin.** Every rollout records what triggered it: `gitops` (the target comes from Git: a new commit, or
`collector.version`, also after an operator took a choice back), `ui` (an operator chose the target version, with the
operator's name as `createdBy` and the reason as `requestReason`) or `system`. A rollout caused by the UI uses the policy
from `update.selection.rolloutPolicy` (default `rollout.versionPolicy`).

## Operator actions

| Action | Effect |
|---|---|
| `pause` | Stops starting new targets. |
| `resume` | Continues a paused rollout and acknowledges the failures so far. |
| `approve` | Releases a wave that requires approval. |
| `abort` | Stops the rollout. Targets that have not started are cancelled. |
| `rollback` | Returns started targets to their starting state. |
| `retry-failed` | Resets failed targets and runs them again. |

They are available at `/api/v1/rollouts/{id}/actions/<action>` and need the permission `rollouts:operate`, which the roles
`operator` and `admin` have.

## Live progress per target

For version rollouts the engine derives a **progress** record for each running target on every tick (the `progress`
field of the rollout target), stores it and freezes it with the final status of the target.

- `phase`: `offered`, `downloading`, `verifying`, `installing`, `restarting`, `health_check`, `rolling_back`,
  `health_wait`, `succeeded`, `failed`, `rolled_back` or `cancelled`.
- `source`: `updater` (steps reported by the `myriarch-updater`), `package_status` (only the OpAMP package status of the
  updater is known, for example with an older or simulated updater) or `executor` (webhook, AWX or manual: `offered`, then
  `installing`, which means "external update, waiting for version X", then `health_wait`).
- `steps[]` in the fixed order `offered`, `downloading`, `verifying`, `installing`, `restarting`, `health_check`,
  optionally `rolling_back`, then `health_wait`, each with a `status` (`pending`, `running`, `done`, `failed` or
  `skipped`), `startedAt`, `finishedAt` and `detail`. Also `downloadPercent` from the package status, and `error`,
  `attempt`, `fromVersion` and `toVersion`.

The updater reports its steps as **state**, not as events: as the health component `dev.myriarch.update` with the
children `version` (the offered version, with a leading `v`), `from` and `step:<name>`, each with a status, start time,
status time and `last_error`. The server assigns the reported attempt to a target when `version == "v" + toRef` and
the attempt did not begin earlier than 2 minutes before the target (a tolerance for clock differences). Otherwise the
attempt counts as not yet begun (`offered`). Missed intermediate reports do no harm.

The rollout page lists the targets with running work first (in progress and verifying, then waiting for a maintenance
window, failed and rolled back, pending, succeeded, skipped, cancelled) and offers the wave order as an alternative.

The status of the target always has the last word: `verifying` means the collector reports the version and the health
wait (`healthWait`) is running (`health_wait`). A final status resolves all steps. After a rollback on the host the
phase is `rolled_back` and `health_wait` is `skipped`. The read API resolves targets that were ended from outside, by
abort, supersede or rollback, to a consistent final picture. The reporting protocol is described in the
[updater protocol](../design/updater-protocol.md).
