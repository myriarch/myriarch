# Quickstart GitOps repository

A small, generic GitOps repository for a local demo: `make dev` starts myriarchd with this directory as its GitOps
source, and `make sim` connects simulated collectors that match its fleets. It is the default of
`myriarchd dev --gitops-dir`.

For your own deployment use `gitops-template/` as the starting point. It has the same structure, with placeholders to
replace and a CI pipeline. This directory is tuned for the simulator profile `examples/sim/mixed-fleet.yaml`
(site IDs, host names and canary entries line up), the template is tuned for real hosts.

## Layout

```text
blueprints/    base, linux-host, windows-host, k8s-gateway (collector configuration templates)
fleets/        linux-hosts, windows-hosts, k8s-gateway (which agents get which blueprint and collector version)
policies/      canary-then-all (version rollouts), config-canary (config rollouts): demo values
inventory/     waves (canary wave assignment)
samples/       sample agents for myrctl (not myriarchd objects)
```

| Fleet | Selector | Blueprint | Collector | Update path |
|---|---|---|---|---|
| `linux-hosts` | `os.type=linux`, `site.type` in `edge`, `datacenter` | `linux-host` | 0.162.0 | myriarch-updater |
| `windows-hosts` | `os.type=windows` | `windows-host` | 0.162.0 | myriarch-updater |
| `k8s-gateway` | `site.type=cloud` | `k8s-gateway` | 0.162.0 | none (container image) |

All fleets use priority 100; their selectors are disjoint, so every agent matches at most one fleet. The version
fleets allow versions `>=0.161.0, <=0.162.0` in the UI (`update.selection`). The simulated edge hosts start on
0.161.0, so a version update to 0.162.0 can be triggered from the UI.

## Run it

```bash
make dev    # myriarchd dev mode with embedded PostgreSQL, UI on http://localhost:8080
make sim    # in a second terminal: simulated collectors and myriarch-updaters (examples/sim/mixed-fleet.yaml)
```

Validate and render without a running server:

```bash
bin/myrctl validate --strict --components examples/sim/components-otelcol-contrib-0.162.0.json examples/quickstart
bin/myrctl render --dir examples/quickstart --fleet windows-hosts --sample windows-edge-01
```

`make build` creates `bin/myrctl`; `make validate-examples` runs the validation above.

## Rollouts, waves and demo values

* **Waves.** Both policies start with the wave `canary` (hosts with the inventory label `wave=canary`, assigned in
  `inventory/waves.yaml`), then wait for approval in the UI before the `rest` wave follows.
* **Config rollouts** use `config-canary`, **version rollouts** use `canary-then-all`. A failed configuration rolls back
  automatically; a failed version update pauses the rollout, because the updater already restored the host.
* **Demo values.** Both policies are written for a demo, which has to show a rollout within about a minute after you approve
  the last wave: `healthWait: 30s` (how long a target must stay healthy after it reported success), `maxConcurrent: 0`
  (every target of a wave at the same time) and no maintenance windows. This repository is the Git source of the
  [Docker Compose](../../docs/getting-started/docker-compose.md) Quickstart, and the
  [Kubernetes on kind](../../docs/getting-started/kubernetes-kind.md) Quickstart copies these two policies into its own Git
  repository, so there is one copy of the demo values. The pages name the duration.
* **Production-like values** are in `gitops-template/config/policies/`: `healthWait` 2m for a configuration and 5m for a
  version, `maxConcurrent: 5` and a commented maintenance window in the local time of the site, each with the reason. Do not copy the
  demo values into a real fleet: a short wait lets a collector that crashes late pass, and no limit lets a bad change reach
  every host at once. [Rollouts](../../docs/concepts/rollouts.md#health-wait) explains the health wait.

## From the quickstart to your own repository

Copy `gitops-template/` (not this directory) into an empty repository and adapt fleets, selectors, gateway
endpoints and site IDs. Keep the structure: myriarchd reads every `*.yaml` file below its GitOps path as an object,
except the `samples/` directory.
