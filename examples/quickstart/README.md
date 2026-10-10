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
policies/      canary-then-all (version rollouts, with maintenance windows), config-canary (config rollouts)
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

## Rollouts, waves and maintenance windows

* **Waves.** Both policies start with the wave `canary` (hosts with the inventory label `wave=canary`, assigned in
  `inventory/waves.yaml`), then wait for approval in the UI before the `rest` wave follows.
* **Config rollouts** use `config-canary`: no maintenance windows, so changes show up immediately; a failure rolls
  the change back.
* **Version rollouts** use `canary-then-all`: targets may only *start* inside a maintenance window, and a failure
  pauses the rollout. The policy uses two time zones on purpose:
  * Monday to Friday 22:00-05:00 in the **site-local time** of each host. The zone comes from the attribute
    `site.timezone` (for example `Europe/Amsterdam`, which observes daylight saving time); hosts without it use
    `defaultTimezone` (UTC). The window crosses midnight and belongs to the day it starts on.
  * Saturday and Sunday around the clock in **UTC**.

  Outside a window targets wait in `waiting_window`. To see version updates without waiting for the evening, point
  the fleets at `config-canary` for `versionPolicy` in a scratch copy of this directory.

## From the quickstart to your own repository

Copy `gitops-template/` (not this directory) into an empty repository and adapt fleets, selectors, gateway
endpoints and site IDs. Keep the structure: myriarchd reads every `*.yaml` file below its GitOps path as an object,
except the `samples/` directory.
