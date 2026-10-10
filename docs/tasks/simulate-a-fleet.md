---
title: "Simulate a fleet"
weight: 60
description: "Connect thousands of simulated supervisors, collectors and updaters to a server with myriarch-sim, using ready-made profiles."
---

`myriarch-sim` simulates the OpAMP Supervisor, the collector and, optionally, the `myriarch-updater` of many hosts over real
OpAMP connections. It counts the configurations, restarts and installations it performs. Use it to fill a development
system with agents, to exercise rollouts without real hosts, to test failure handling and to run load tests.

## Run a simulation

Start a server first ([Local development](../getting-started/local-dev.md)), then:

```bash
ulimit -n 65536   # the server needs many file descriptors, too: 1,900 connections for the default run
make sim
```

`make sim` runs `myriarch-sim run` with 1,000 agents and the profile `examples/sim/mixed-fleet.yaml`. The make variables
`SIM_AGENTS`, `SIM_UPDATERS`, `SIM_SERVER` and `SIM_TOKEN` change the defaults. The same with the binary and all common
options:

```bash
bin/myriarch-sim run --server ws://localhost:8080/v1/opamp \
  --agents 1000 --updaters=true --profile examples/sim/mixed-fleet.yaml \
  --control-listen 127.0.0.1:9465 --metrics-listen 127.0.0.1:9464
```

| Option | Default | Meaning |
|---|---|---|
| `--server` | `ws://localhost:8080/v1/opamp` | The OpAMP endpoint. |
| `--token` | none | Enrollment token. Prefer the environment variable `MYRIARCH_ENROLLMENT_TOKEN`: arguments are visible in process listings. Dev mode does not require a token. |
| `--profile` | required | The simulation profile (YAML). |
| `--agents` | `1000` | Number of simulated collectors. The group weights of the profile are scaled to this number (the sum is exactly N). |
| `--updaters` | `true` | Also simulate a `myriarch-updater` for the groups that have `updater: true`. |
| `--seed` | from the profile | Overrides the profile seed. The same seed gives the same host names and instance UIDs. |
| `--duration` | `0` | Stop after this time; `0` runs until interrupted. |
| `--fake-downloads` | `true` | Package downloads return a dummy body. With `--fake-downloads=false` the simulated updaters download the real artifact from the server with their download token and verify its SHA-256. |
| `--metrics-listen` | off | Serve Prometheus metrics, for example `:9464`. |
| `--control-listen` | off | Serve the local control API, for example `127.0.0.1:9465`. |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error`. |
| `--ca-file`, `--insecure-skip-verify` | off | For `wss://` endpoints: an additional CA bundle, or no TLS verification (development only). |

Use a `ws://` endpoint for a plain local server. The simulator raises its own file descriptor limit as far as the system
allows and logs a summary every 10 seconds. The full list is in the [`myriarch-sim run` reference](../reference/cli/myriarch-sim_run.md).

After about 20 seconds the overview of the server shows 1,000 collectors and 900 updaters, assigned to the fleets
`linux-hosts`, `windows-hosts` and `k8s-gateway` of the example repository. The simulated edge hosts are older than the Git
target version, so version rollouts appear ([Roll out a collector version](roll-out-a-collector-version.md)).

## Profiles

A profile describes groups of hosts and how they behave. The directory `examples/sim/` has four, plus the component list
that the simulated collectors report:

| Profile | Purpose |
|---|---|
| `mixed-fleet.yaml` | The default of `make sim`, matching the quickstart repository: 40 % Linux edge hosts, 20 % Linux data center hosts, 30 % Windows edge hosts and 10 % Kubernetes gateways. The groups with `updater: true` get an updater, so 900 of 1,000 agents. |
| `small.yaml` | 20 agents with short durations for a fast round trip against the quickstart repository. |
| `chaos.yaml` | Failure scenarios for rollout tests: about 5 % of the configuration applies fail, 3 % of the hosts become unhealthy after an apply, 10 % of the updates fail and 2 % of the agents go offline briefly per minute. |
| `e2e.yaml` | 32 agents with short durations for `scripts/e2e.sh`, matching the test data repository `internal/testdata/gitops`. |

A profile has the top-level keys `seed`, `components` (a component list, relative to the profile), `groups` and `behavior`.
Each group sets `name`, `weight`, `os`, `arch`, `version`, `updater`, `hostnamePattern`, `siteIdPattern` and `attributes` (the
`site.*` attributes the supervisor reports, which decide the fleet), and optionally `fixedSiteIds`. The `behavior` block tunes
the simulation, for example:

| Key | Effect |
|---|---|
| `connectRate` | New connections per second during ramp-up. |
| `configApplyDelay`, `restartDuration`, `startupDuration`, `updaterInstallDuration` | A duration (`3s`) or a range (`1s-5s`). |
| `configFailureRate`, `unhealthyAfterApplyRate`, `updateFailureRate`, `offlineChurnRate` | Probabilities between 0 and 1. |
| `failConfigsContaining` | A configuration that contains this text always fails (`simulate-config-failure` in the shipped profiles). |
| `validateConfig` | Like `validate_config` of the supervisor: an invalid configuration is reported `FAILED` and the old one keeps running. |
| `reconnectOnUpgrade` | A version change drops the connection and reconnects, as when the supervisor is restarted. |
| `versionAfterUpdate` | `offered` (default) or `unchanged`. |

Copy a shipped profile and adapt it. Keep the `attributes` consistent with the selectors of your fleets, and the fixed site
IDs consistent with the inventory entries that put hosts into the canary wave.

## Observe and control

Metrics at `/metrics` of the `--metrics-listen` address:

| Metric | Meaning |
|---|---|
| `myriarch_sim_agents_total` | Number of simulated collectors. |
| `myriarch_sim_agents_connected` | Established connections of collectors and updaters. |
| `myriarch_sim_updaters_connected` | Established updater connections. |
| `myriarch_sim_configs_applied_total`, `myriarch_sim_configs_failed_total` | Configurations that were applied or failed. |
| `myriarch_sim_updates_total{result}` | Package installations by result, `installed` or `failed`. |
| `myriarch_sim_restarts_total` | Collector restarts (command, upgrade, failed update). |
| `myriarch_sim_connect_failures_total` | Failed connection attempts. |

The control API at the `--control-listen` address lets a script act from the outside:

```bash
curl -s localhost:9465/control/summary
curl -s 'localhost:9465/control/agents?hostname=linux-edge-01.example.com'
curl -s -X POST -d '{"hostnames":["linux-edge-01.example.com"],"version":"0.163.0"}' localhost:9465/control/upgrade
```

`POST /control/upgrade` makes the named hosts "update from the outside" to the version, as a webhook or AWX pipeline would;
the receiver script `scripts/e2e/mock_receiver.py` uses it. `GET /control/agents` needs the `hostname` parameter.

When the server restarts, it answers new connections with `503` and a `Retry-After` header while it shuts down. The
simulated agents reconnect with backoff, so the `connectErrors` counter in the summary grows for a minute or two with 1,900
agents. That is expected.

For automated runs, `scripts/e2e.sh` starts the simulator with the profile `e2e` (`E2E_AGENTS=1000 scripts/e2e.sh` for a
load run); see the [development guide](../development.md). The next page connects a real supervisor instead:
[Connect a real supervisor](connect-a-real-supervisor.md).
