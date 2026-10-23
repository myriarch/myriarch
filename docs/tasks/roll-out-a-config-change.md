---
title: "Roll out a configuration change"
weight: 30
description: "Change a blueprint in Git and watch the rollout engine apply it in waves, with approval, failure handling and rollback."
---

A change to a blueprint, to the parameters of a fleet or to the inventory creates one **configuration rollout** per
affected fleet. The rollout policy of the fleet decides the waves, whether a wave waits for approval, when a failure stops
the rollout and what happens then. This page walks through the happy path and the failure path on the local system.

You need the running system of [Local development](../getting-started/local-dev.md) with simulated collectors
([Simulate a fleet](simulate-a-fleet.md)), started on a working copy of the example repository:

```bash
cp -R examples/quickstart .dev/gitops-work
bin/myriarchd dev --gitops-dir .dev/gitops-work --data-dir .dev
```

The commands below use the `api` shell function from the section "Call the REST API" of that page.

## Change the configuration

The base blueprint has a parameter `memoryLimitPercent` with the default `75`. Lower it for all fleets and sync:

```bash
sed -i.bak 's/default: 75/default: 70/' .dev/gitops-work/blueprints/base.yaml && rm .dev/gitops-work/blueprints/base.yaml.bak
api -X POST localhost:8080/api/v1/gitops/actions/sync
api 'localhost:8080/api/v1/rollouts?kind=config&active=true' | jq '.items[] | {fleetName, policyName, status, statusReason}'
```

Without the explicit sync, `myriarchd` notices the change within 10 seconds in dev mode. It renders every fleet again and
creates a configuration rollout for each fleet whose rendered configuration changed. Here that is `linux-hosts`,
`windows-hosts` and `k8s-gateway`, all with the policy `config-canary`.

## Waves and approval

The policy `config-canary` has two waves. The wave `canary` contains the hosts that the inventory marks with
`labels.wave=canary`; the wave `rest` follows with all other hosts and **requires approval**. So the rollout applies the
change to the canary hosts, waits for the health check (`healthWait`, 30 seconds in the example policy), and then stops in the status `waiting_approval`.

Approve the next wave in the UI (*Rollouts*, the rollout, then *Approve*) or with the API. Wave indexes start at 0, so the
wave `rest` has the index 1:

```bash
R=<rollout-id>
api localhost:8080/api/v1/rollouts/$R | jq '.waves[] | {index, name, status}'
api -X POST -d '{"wave":1,"reason":"canary hosts are healthy"}' localhost:8080/api/v1/rollouts/$R/actions/approve
```

Waves with no targets (for example a percentage that rounds to zero hosts) are skipped. Every approval is recorded in the
audit log with the user and the reason. The [rollouts concept](../concepts/rollouts.md) describes waves, targets, success
criteria, maintenance windows and the planner in detail.

## The failure path

The simulator makes any configuration that contains the text `simulate-config-failure` fail, like a collector that
rejects an invalid configuration. Provoke this in the fleet file `.dev/gitops-work/fleets/linux-hosts.yaml` by adding to
`spec.parameters`:

```yaml
    collectJournald: true
    journaldUnits: [simulate-config-failure]
```

Sync again. The canary hosts report the failure, so the rollout reaches its failure threshold (`maxFailed: 1`). What
happens next is the `onFailure` setting of the policy:

- `rollback` (the policy `config-canary`): the rollout goes through `rolling_back` to `rolled_back` and restores the
  previous configuration on every target it touched.
- `pause` (the version policy `canary-then-all`): the rollout stops in the status `paused` and waits for a decision.

The agents that rejected the configuration show `configSync = failed`, and `myriarchd` does **not offer the failed
configuration again**, so there is no retry loop. Because Git still contains the broken change, an agent whose
configuration was rolled back shows `drift` (desired state differs from the assigned state) until the repository is
corrected.

Ways forward:

| Action | When |
|---|---|
| Fix the change in Git | The normal way. A new commit creates a new rollout, and an active or paused rollout of the fleet whose targets differ is replaced (`superseded`). A rollout in `failed` or `rolling_back` is never replaced automatically: abort it first. `git revert` takes a change back. |
| `POST /api/v1/rollouts/{id}/actions/retry-failed` | Reset failed targets to `pending` and try again, for a rollout that is paused, running or failed. |
| `POST /api/v1/rollouts/{id}/actions/resume` | Continue a paused rollout. |
| `POST /api/v1/rollouts/{id}/actions/rollback` | Roll back all touched targets to their previous configuration. |
| `POST /api/v1/rollouts/{id}/actions/abort` | Stop starting targets and cancel the pending ones. Updated agents keep the new state. Needed for a rollout that failed because its fleet was removed from Git. |
| `POST /api/v1/rollouts/{id}/actions/pause` | Stop starting new targets; targets in progress finish. |

All of these are buttons on the rollout page as well. The request body is optional and takes a `reason` for the audit log.

To end the experiment, restore the files of your working copy (or copy `examples/quickstart` again) and sync.

## Good practice

- **Never change configuration and collector version in the same merge request.** If a version update restarts a
  collector while the configuration target of that host is still in its probation time, the configuration target fails.
  The two kinds of rollout run independently per fleet.
- Review the **rendered** configuration, not only the blueprint source: use `myrctl render` as described in
  [Validate a GitOps repository](validate-a-gitops-repository.md).
- A configuration target can fail with "agent became unhealthy during the health wait" when the collector restarts during
  the `healthWait`, for example because of a version update of the same host. Retry the failed targets after the
  version rollout has finished.

Next: [roll out a collector version](roll-out-a-collector-version.md).
