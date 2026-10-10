---
title: "Your first fleet and rollout"
weight: 20
description: "A guided first session: create a fleet in a GitOps repository, enroll collectors and roll out a change in waves."
---

In this tutorial you write your own small GitOps repository with one fleet and one rollout policy, enroll ten simulated
collectors with an enrollment token, and roll out a configuration change in two waves with an approval in between. You
need no container runtime and no external service. Every step was run exactly as written, and the results below are what
the system showed (the exact seconds vary).

What you build:

- a GitOps directory `.dev/first-fleet` with a blueprint, the fleet `linux-hosts` and the rollout policy `two-waves`;
- an enrollment token `lab` that attaches the label `environment=lab` to every collector that registers with it;
- ten simulated collectors that land in the fleet because of that label;
- a configuration rollout: a canary wave of two collectors, your approval, and the remaining eight.

## Before you start

Complete the setup of [Local development](local-dev.md): you need the binaries in `bin/` (`make web-build` once, then
`make build`) and a stopped previous dev server, because this tutorial starts its own on the same port 8080. Run all
commands from the root of the source checkout. Keep three terminals open: one for the server, one for the simulator and
one for everything else. The commands that talk to the server are marked as terminal 3.

## Create the GitOps directory

A GitOps repository for Myriarch is a directory with one folder per kind of object ([GitOps](../concepts/gitops.md)).
Start with the two blueprints of the example repository, which contain the collector configuration, and a sample agent
for the validation commands:

```bash
mkdir -p .dev/first-fleet/{blueprints,fleets,policies,samples/agents}
cp examples/quickstart/blueprints/base.yaml examples/quickstart/blueprints/linux-host.yaml .dev/first-fleet/blueprints/
cp examples/quickstart/samples/agents/linux-edge-01.yaml .dev/first-fleet/samples/agents/
```

The sample agent describes a host as the supervisor would report it. In the copy
`.dev/first-fleet/samples/agents/linux-edge-01.yaml`, replace the line `labels: {}` with the label that your enrollment
token will attach later:

```yaml
labels: {environment: lab}
```

`.dev/` is ignored by Git, so nothing here ends up in a commit.

## Define the fleet

A fleet selects agents, names the blueprint that renders their configuration and the policies that govern changes. Create
`.dev/first-fleet/fleets/linux-hosts.yaml`:

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata:
  name: linux-hosts
  description: Linux lab hosts of the first-fleet tutorial
spec:
  displayName: Linux hosts
  selector:
    matchAttributes:
      os.type: linux
      labels.environment: lab
  blueprint: linux-host
  parameters:
    gatewayEndpoint: gateway.example.com:4317
  collector:
    distribution: otelcol-contrib
    version: 0.162.0
  rollout:
    configPolicy: two-waves
    versionPolicy: two-waves
```

The selector matches Linux agents that carry the server label `environment=lab`. In a selector a server label is
addressed as `labels.<key>`. It comes from the enrollment token an agent registered with (or from the inventory in Git)
and **cannot be set by the agent itself**, unlike the self-reported attributes such as `os.type`; see
[Server labels](../concepts/agents.md#server-labels). That makes the label a safe way to say "these hosts belong to the
lab". The fleet has no `spec.update` block, so no executor is configured: the fleet states a target collector version,
and the version is not changed by the system. All fields are listed in the [GitOps schema](../reference/gitops-schema.md).

## Define the rollout policy

Create `.dev/first-fleet/policies/two-waves.yaml`:

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: RolloutPolicy
metadata:
  name: two-waves
  description: Canary wave of 20 percent, then approval, then everything else
spec:
  waves:
    - name: canary
      percent: 20
    - name: rest
      percent: 100
      requireApproval: true
  maxConcurrent: 5
  healthWait: 15s
  applyTimeout: 5m
  failureThreshold:
    maxFailed: 1
  onFailure: rollback
```

A change reaches 20 percent of the targets first (the wave `canary`). After a successful health wait of 15 seconds, the
wave `rest` takes all remaining targets, but only after a person approved it. A single failed target stops the rollout
and rolls the touched targets back. The policy has no maintenance windows, so changes start immediately. The fleet uses it
for configuration rollouts and for version rollouts.

## Validate and render

Check the directory exactly as the server will check it, and render the collector configuration of the sample agent:

```bash
bin/myrctl validate --strict --components examples/sim/components-otelcol-contrib-0.162.0.json .dev/first-fleet
bin/myrctl render --dir .dev/first-fleet --fleet linux-hosts --sample linux-edge-01
```

The first command prints the summary below and exits with `0`:

```text
OK: 2 Blueprint, 0 Executor, 1 Fleet, 0 Inventory, 1 RolloutPolicy; 1 samples; 0 errors, 0 warnings
```

The second prints the complete collector configuration for the sample host, including
`endpoint: gateway.example.com:4317` and `limit_percentage: 75` of the memory limiter, the value you change at the end.
More in [Validate a GitOps repository](../tasks/validate-a-gitops-repository.md).

## Start the server

In terminal 1, start the development server on your directory. It keeps its data in a separate directory, so
that you can delete it later without touching anything else:

```bash
bin/myriarchd dev --gitops-dir .dev/first-fleet --data-dir .dev/first-fleet-data
```

The first start downloads the PostgreSQL binaries once (see [Local development](local-dev.md#build-and-start)). When the
log shows `http server listening` and a `DEV MODE` warning, open <http://localhost:8080> and sign in as `admin` with the
password `admin`. The fleet `linux-hosts` is shown with 0 agents.

Dev mode is not a production setup: the logins are well known. Sign in on the command line as well, with the `api`
function that [Local development](local-dev.md#call-the-rest-api) defines (terminal 3):

```bash
curl -s -c .dev/cj -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' \
  localhost:8080/api/v1/auth/dev-login | jq .role
CSRF=$(awk '$6=="myriarch_csrf"{print $7}' .dev/cj)
api() { curl -s -b .dev/cj -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' "$@"; }

api localhost:8080/api/v1/gitops/status | jq '{state, issues: (.issues|length)}'
api 'localhost:8080/api/v1/fleets' | jq '[.items[].fleet.name]'
```

The status shows `"state": "ok"` and no issues, and the fleet list is `["linux-hosts"]`.

## Create an enrollment token

Collectors present an enrollment token when they connect. Create a token named `lab` with the label `environment=lab`,
either in the UI (*Settings*, tab *Enrollment tokens*, button *Create token*; the secret is shown once) or with the API:

```bash
api -X POST -d '{"name":"lab","labels":{"environment":"lab"}}' localhost:8080/api/v1/enrollment-tokens | jq -r .secret
```

The command prints the secret, which starts with `myre_`. It is shown only at creation, so copy it now. Dev mode does not *require* a token, but a token that an agent presents is validated, and
its labels are bound to the agent. That is what puts the simulated collectors into your fleet. In a real installation the
server requires a token (see [Connect a host](../tasks/connect-a-host.md)).

## Connect ten simulated collectors

The simulator needs a profile that describes its collectors. Create `.dev/first-fleet-profile.yaml`: one group of Linux
collectors without an updater, and the timing of `examples/sim/small.yaml`, so that a configuration is applied within a
second:

```yaml
seed: 7
components: ../examples/sim/components-otelcol-contrib-0.162.0.json   # relative to this file
groups:
  - name: lab-edge
    weight: 1
    os: linux
    arch: amd64
    version: 0.162.0
    updater: false
    hostnamePattern: "lab-%02d.example.com"
    siteIdPattern: "edge-%03d"
    attributes:
      site.type: edge
      site.timezone: UTC
behavior:
  connectRate: 50
  heartbeatInterval: 30s
  configApplyDelay: 200ms-1s
  configFailureRate: 0.0
  failConfigsContaining: "simulate-config-failure"
  unhealthyAfterApplyRate: 0.0
  restartDuration: 1s
  startupDuration: 1s-2s
  reconnectOnUpgrade: true
  validateConfig: true
  updaterInstallDuration: 2s-6s
  updateFailureRate: 0.0
  versionAfterUpdate: offered
  offlineChurnRate: 0.0
```

In terminal 2, start ten collectors with the token you copied. The simulator reads it from the environment variable
`MYRIARCH_ENROLLMENT_TOKEN`:

```bash
MYRIARCH_ENROLLMENT_TOKEN=<token> bin/myriarch-sim run --agents 10 --profile .dev/first-fleet-profile.yaml
```

The log shows `collectors=10 updaters=0`, and the summary line reports `collectorsConnected=10/10` and
`configsApplied=10`. In the UI the fleet `linux-hosts` now has 10 agents. Check the same on the API (terminal 3):

```bash
api 'localhost:8080/api/v1/agents?kind=collector&limit=20' | jq '.items[] | {hostname, fleetName, configSync, labels}'
```

All ten agents (`lab-01.example.com` and so on) are in the fleet `linux-hosts`, have `"environment": "lab"` under
`labels` and show `"configSync": "in_sync"`: the server assigned the rendered configuration and each collector reports
that it applied it. The simulator stays in the foreground; leave it running.

## Roll out a change

Change a parameter of the fleet. Open `.dev/first-fleet/fleets/linux-hosts.yaml` and add `memoryLimitPercent: 70` under
`spec.parameters`:

```yaml
  parameters:
    gatewayEndpoint: gateway.example.com:4317
    memoryLimitPercent: 70
```

The server re-reads the directory every 10 seconds. To sync at once, press the sync button on the *GitOps* page or call
the API:

```bash
api -X POST localhost:8080/api/v1/gitops/actions/sync
api 'localhost:8080/api/v1/rollouts?kind=config&active=true' | jq '.items[] | {id, fleetName, policyName, status, statusReason}'
```

The server renders the configuration of the fleet again, sees that it changed and starts a configuration rollout with the
policy `two-waves` (status `running`, reason `started automatically`). Open *Rollouts* in the UI to watch it. The
rollout has 10 targets. The wave `canary` has 2 of them (20 percent of 10), `rest` has the other 8. About 15 seconds
after the canary collectors applied the change (the `healthWait`), the rollout stops in the status `waiting_approval`
with the reason `wave "rest" waits for approval`.

Approve the wave in the UI (the rollout page, button *Approve*) or with the API. Wave indexes start at 0, so the wave
`rest` has the index 1:

```bash
R=$(api 'localhost:8080/api/v1/rollouts?kind=config&active=true' | jq -r '.items[0].id')
api localhost:8080/api/v1/rollouts/$R | jq '.waves[] | {index, name, status}'
api -X POST -d '{"wave":1,"reason":"canary hosts are healthy"}' localhost:8080/api/v1/rollouts/$R/actions/approve
```

The approval is recorded in the audit log with your user and the reason. The remaining eight collectors follow, at most
five at the same time, each with a health wait of 15 seconds. After about 45 seconds the rollout ends in the status
`completed` with the reason `all waves completed`, all 10 targets `succeeded`, and no active rollout remains:

```bash
api localhost:8080/api/v1/rollouts/$R | jq '{status, statusReason, counts}'
api 'localhost:8080/api/v1/agents?kind=collector&limit=20' | jq '[.items[].configSync] | group_by(.) | map({(.[0]): length}) | add'
```

The first command shows the final status and `"succeeded": 10` in the counts. The second shows `{"in_sync": 10}`: all 10 agents
are in sync again, now with `limit_percentage: 70`. This is the principle of Myriarch in small: Git holds
the desired state, the policy defines how a change travels, and a person decides at the point that the policy marks.
Rerun `bin/myrctl render` to see the new value in the rendered configuration of the sample agent.

## Try the failure path and a version rollout

Two optional experiments use the same setup:

- Provoke a failing configuration and watch the automatic rollback: see
  [The failure path](../tasks/roll-out-a-config-change.md#the-failure-path). In your fleet file, add the parameters
  `collectJournald: true` and `journaldUnits: [simulate-config-failure]` to `spec.parameters`.
- Roll out a collector version: [Roll out a collector version](../tasks/roll-out-a-collector-version.md) shows how the
  system changes the version of a fleet that has an update executor.

## Clean up

Stop the simulator and the server with `Ctrl+C` in their terminals, then delete the data of this tutorial:

```bash
rm -rf .dev/first-fleet-data
```

The directories `.dev/first-fleet` and the file `.dev/first-fleet-profile.yaml` are yours to keep as a starting point. If the
server was killed with `kill -9`, see [Local development](local-dev.md#stop-and-reset) for the leftover PostgreSQL.

## Next steps

- Turn your directory into a real repository with the template described in
  [gitops-template/README.md](../../gitops-template/README.md).
- Connect a real host: [Connect a host](../tasks/connect-a-host.md).
- Run the whole system in containers on one machine: [Docker Compose](docker-compose.md).
