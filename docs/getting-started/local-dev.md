---
title: "Local development"
weight: 10
description: "Build Myriarch from source and run the server with an embedded database, sample GitOps repository and development logins."
---

This page takes you from a source checkout to a running system on one machine: `myriarchd` with an embedded PostgreSQL,
the example GitOps repository `examples/quickstart` and, in a second terminal, simulated collectors. No container runtime
and no external service is involved. The steps were written for macOS and Linux; the one step that differs is the
download of real collector binaries in [Connect a real supervisor](../tasks/connect-a-real-supervisor.md).

## Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | 1.26.6 or newer (the `go` line of `go.mod`) | building everything |
| Node.js and pnpm | Node 20.19 or newer, pnpm 10 | building the web UI (`make web-build`) |
| make, curl, jq | any recent version | the make targets and the examples on this page |
| Python 3 | 3.9 or newer | only the helper scripts of the end-to-end tests |

## Build and start

```bash
make web-build   # build the React UI into internal/webui/dist (once, and after UI changes)
make build       # bin/myriarchd, bin/myriarch-updater, bin/myrctl, bin/myriarch-sim, with the UI embedded
make dev         # start myriarchd in dev mode: embedded PostgreSQL, example GitOps repository, development logins
```

`make dev` runs `myriarchd dev --gitops-dir examples/quickstart --data-dir .dev`. The very first start downloads the
PostgreSQL binaries (about 30 MB) once into the user cache directory (`myriarch/embedded-postgres` below it); later
starts work offline. When the log shows that the server is listening, the system is ready:

| | |
|---|---|
| Web UI | <http://localhost:8080> |
| Development logins | `admin` / `admin` (full access), `operator` / `operator`, `viewer` / `viewer` |
| OpAMP endpoint | `ws://localhost:8080/v1/opamp` |
| Enrollment token (development only) | `myre_devtoken_insecure_do_not_use_in_prod` |
| Health | `curl localhost:8080/readyz` answers `ready`, `curl localhost:8080/healthz` answers `ok` |

The logins and the token are well known on purpose and are **insecure**. They exist only in dev mode (see the safety rails
below). Never use them on a shared or reachable machine.

To connect simulated collectors, open a second terminal:

```bash
ulimit -n 65536   # the macOS default of 256 open files is too low for thousands of WebSocket connections
make sim          # myriarch-sim: 1000 collectors and 900 updaters against ws://localhost:8080/v1/opamp
```

After a short while the overview shows 1,900 agents, spread over the fleets `linux-hosts` (60 %), `windows-hosts` (30 %)
and `k8s-gateway` (10 %). The simulator is described in [Simulate a fleet](../tasks/simulate-a-fleet.md).

## Stop and reset

Stop both processes with `Ctrl+C`. All state (database, artifact cache, Git clone) lives in `.dev/` and survives
restarts. `rm -rf .dev` resets the system to a fresh start. If `myriarchd` was killed with `kill -9`, the embedded
PostgreSQL keeps running; stop it with `kill $(head -1 .dev/postgres/data/postmaster.pid)`.

## Dev mode

`myriarchd dev` is `myriarchd serve` with development presets: an embedded PostgreSQL (port 54329, data below
`<data-dir>/postgres`), development logins instead of single sign-on, no enrollment token required for agents, cookies
without the `Secure` flag, a GitOps source from a local directory (re-read every 10 seconds) and text logs. The complete
list of options is in the [`myriarchd dev` reference](../reference/cli/myriarchd_dev.md).

```bash
bin/myriarchd dev --help
bin/myriarchd dev --gitops-dir .dev/gitops-work --data-dir .dev --listen 127.0.0.1:8080
```

| Flag | Meaning |
|---|---|
| `--gitops-dir` | GitOps repository: a plain directory or a Git work tree. Default `examples/quickstart`. |
| `--data-dir` | PostgreSQL data, artifact cache and Git clone. Default `.dev`. |
| `--listen` | HTTP address. Default `127.0.0.1:8080`. |
| `--allow-nonlocal` | Allows dev mode with a non-loopback address or database, OIDC, TLS or an https `publicUrl`. See the safety rails. |
| `-c file.yaml` | An additional `myriarchd` configuration layered over the dev presets, for example `dev.postgresPort`, `notifications.channels` or `artifacts.sourceBaseUrl`. |
| `--dsn` | Use this PostgreSQL instead of starting the embedded one. This is how a second replica joins, see [Run two replicas locally](../tasks/run-two-replicas-locally.md). |
| `--replica-id` | Name of the replica. Default: host name plus a random suffix. |
| `--releases-fixtures` | Read release data from recorded fixtures instead of GitHub, for offline work. |

### Safety rails

Dev mode is not an operating mode. It writes the well-known logins and the well-known enrollment token into its database,
so it protects itself:

- It listens on loopback addresses only and **refuses to start** when its settings look like production: a `--listen`
  address that is not a loopback address (for example `:8080`), a `--dsn` that points to another host, OIDC or TLS in the
  `-c` file, or an https or non-local `server.publicUrl`. Only an explicit `--allow-nonlocal` overrides the check.
- At start it logs a warning with the logins and the token, and repeats it every 30 minutes.
- When `myriarchd serve` starts on a database that still contains the well-known token, it revokes the token and writes an
  audit entry. Sessions of the local development accounts are not accepted there.

## Work on a copy of the example repository

Do not edit `examples/quickstart` directly. Make a working copy and point dev mode at it:

```bash
cp -R examples/quickstart .dev/gitops-work
bin/myriarchd dev --gitops-dir .dev/gitops-work --data-dir .dev
```

(`make dev` always uses `examples/quickstart`, so call the binary for a working copy.) `myriarchd` reads the directory every 10 seconds. To sync at once, press the sync button on the *GitOps* page of the UI or
call `POST /api/v1/gitops/actions/sync` (next section). The repository layout and the object kinds are explained in
[GitOps](../concepts/gitops.md).

The example policies contain night-time maintenance windows and long waiting times. In your working copy, shorten them or
point the fleets at the policy `config-canary` (see the README of the example repository), so that rollouts finish while
you watch.

## Call the REST API

The contract of the REST API is [api/openapi.yaml](../../api/openapi.yaml). In dev mode, sign in with the development
login and keep the cookies:

```bash
curl -s -c .dev/cj -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' \
  localhost:8080/api/v1/auth/dev-login | jq .role
CSRF=$(awk '$6=="myriarch_csrf"{print $7}' .dev/cj)
api() { curl -s -b .dev/cj -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' "$@"; }

api localhost:8080/api/v1/gitops/status | jq '{state, issues: (.issues|length)}'
api localhost:8080/api/v1/fleets | jq '[.items[].fleet.name]'
api 'localhost:8080/api/v1/agents?kind=collector&limit=5' | jq '.items[] | {hostname, version, fleetName, configSync}'
api localhost:8080/api/v1/agents/facets | jq '.configSync'
```

Calls that change state need the CSRF token in the header `X-CSRF-Token`. An API token (`Authorization: Bearer myra_...`,
created under *Settings*, tab *API tokens*) needs no CSRF header, which makes it the right credential for scripts and CI.
The `api` function defined above is used again on the task pages.

## Validate and render with myrctl

`myrctl` checks a GitOps repository exactly as the server does before it applies a revision, and renders the collector
configuration of a sample agent. No server is needed:

```bash
bin/myrctl validate --strict --components examples/sim/components-otelcol-contrib-0.162.0.json examples/quickstart
bin/myrctl render --dir examples/quickstart --fleet windows-hosts --sample windows-edge-01
```

The first command exits with `0` when the repository is valid; `--strict` turns warnings into errors. The second prints
the rendered configuration of the sample agent `windows-edge-01`. More in
[Validate a GitOps repository](../tasks/validate-a-gitops-repository.md) and in the [`myrctl` reference](../reference/cli/myrctl.md).

`myr` is a short alias of `myrctl` that is only documented: no binary of that name is built or shipped. Create it yourself
if you like it: `alias myr=myrctl` in POSIX shells, `Set-Alias myr myrctl` in PowerShell, or a symbolic link named `myr`.

## Where to go next

- [Simulate a fleet](../tasks/simulate-a-fleet.md): profiles, scale, failure scenarios and metrics of the simulator.
- [Roll out a configuration change](../tasks/roll-out-a-config-change.md) and
  [roll out a collector version](../tasks/roll-out-a-collector-version.md): the rollout engine at work.
- [Connect a real supervisor](../tasks/connect-a-real-supervisor.md): a real OpAMP Supervisor and `otelcol-contrib` on your machine.
- [Development guide](../development.md): tests, the end-to-end suite and the other checks that run before a commit.
- [Troubleshooting](../operations/troubleshooting.md) when something does not behave as described.
- Kubernetes: the kind probe in [hack/kind/README.md](../../hack/kind/README.md) installs the Helm chart into a local cluster.
