---
title: "Connect a real supervisor"
weight: 70
description: "Run the upstream OpAMP Supervisor and otelcol-contrib on your machine against a local server and watch the whole chain work."
---

The simulator proves the server side. This page proves the real chain on one machine: the OpAMP handshake with an
enrollment header, assignment to a fleet, a `remote_config` offer, the effective configuration, the list of available
components, health, a restart command and an invalid configuration. `E2E_REAL=1 scripts/e2e.sh` automates the same steps;
here you do them by hand.

You need a running server ([Local development](../getting-started/local-dev.md)) on a working copy of the example
repository, `.dev/gitops-work`, and the `api` function from that page.

## Download the binaries

The supervisor and the collector are the unmodified upstream releases. Choose the operating system and architecture of your
machine (the steps were verified on macOS/arm64):

```bash
(
  mkdir -p .dev/real && cd .dev/real
  V=0.162.0; OS=darwin; ARCH=arm64      # for example linux and amd64 on a Linux machine
  B=https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download
  curl -fsSLO $B/v$V/otelcol-contrib_${V}_${OS}_${ARCH}.tar.gz && tar -xzf otelcol-contrib_${V}_${OS}_${ARCH}.tar.gz otelcol-contrib
  curl -fsSL -o opampsupervisor "$B/cmd%2Fopampsupervisor%2Fv$V/opampsupervisor_${V}_${OS}_${ARCH}" && chmod +x opampsupervisor
  ./otelcol-contrib --version        # otelcol-contrib version 0.162.0
)
```

The download is about 125 MB. Every release asset has a `.sha256` file next to it and a Sigstore bundle
(`<asset>.sigstore.json`). The install scripts of [Connect a host](connect-a-host.md) verify both; do the same for anything
that is more than a throw-away experiment.

## Configure the supervisor

Create `.dev/real/supervisor.yaml`. The keys are exactly those of the supervisor v0.162.0; the production templates are in
`deploy/supervisor/`. Replace `/ABSOLUTE/PATH` with the absolute path of your checkout:

```yaml
server:
  endpoint: ws://localhost:8080/v1/opamp
  headers:
    Authorization: "Bearer myre_devtoken_insecure_do_not_use_in_prod"
capabilities:
  accepts_remote_config: true
  accepts_restart_command: true
  reports_effective_config: true
  reports_health: true
  reports_available_components: true
  reports_heartbeat: true
agent:
  executable: /ABSOLUTE/PATH/.dev/real/otelcol-contrib
  config_apply_timeout: 30s
  bootstrap_timeout: 15s
  validate_config: true
  automatic_config_rollback: true
  description:
    non_identifying_attributes:       # these attributes decide the fleet (selectors) and the parameters
      site.type: dev
      site.id: dev-laptop-01
      site.country: NL
      site.timezone: Europe/Amsterdam
storage:
  directory: /ABSOLUTE/PATH/.dev/real/storage
healthcheck:
  endpoint: localhost:13134
```

The token is the well-known development token and works only against `myriarchd dev`. A real installation reads it from the
service environment instead ([Connect a host](connect-a-host.md)).

## Create a fleet for the machine

Add a fleet to the working copy. Its selector matches the attribute `site.type: dev`:

```yaml
# .dev/gitops-work/fleets/dev-laptop.yaml
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata: {name: dev-laptop}
spec:
  priority: 10
  selector: {matchAttributes: {site.type: dev}}
  blueprint: base
  parameters:
    gatewayEndpoint: localhost:14317   # no listener: the exporter only retries, which is harmless
    gatewayInsecure: true
    gatewayAuth: false
    storageDir: /tmp/myriarch-dev-storage
```

Validate, sync and start the supervisor:

```bash
bin/myrctl validate .dev/gitops-work
api -X POST localhost:8080/api/v1/gitops/actions/sync
(cd .dev/real && ./opampsupervisor --config supervisor.yaml > supervisor.log 2>&1 &)
```

On macOS the very first start of `otelcol-contrib` can take 20 to 40 seconds, because Gatekeeper checks the large binary.
Then look at the agent:

```bash
ID=$(api 'localhost:8080/api/v1/agents?attr=site.id%3Ddev-laptop-01' | jq -r '.items[0].id')
api localhost:8080/api/v1/agents/$ID | jq '{configSync, rc: .agent.remoteConfig.status, version: .agent.version, healthy: .agent.health.healthy, caps: .capabilityNames}'
api localhost:8080/api/v1/agents/$ID/components | jq '.components.receivers | keys | length'
api localhost:8080/api/v1/agents/$ID/effective-config | jq -r .body | grep -n 'otlp_grpc/gateway\|^    opamp:'
api -X POST localhost:8080/api/v1/agents/$ID/actions/restart
```

You should see `configSync` `in_sync`, the remote configuration `applied`, the version `0.162.0`, a health tree, more than
100 receivers in the component list, and the rendered exporter `otlp_grpc/gateway` in the effective configuration next to
the `opamp` extension that the supervisor injects. After the restart command the health goes to unhealthy and, a few
seconds later, back to healthy.

## An invalid configuration

Set `batchMaxSize: 10` in the `parameters` of the fleet file and sync. The blueprint writes it to `send_batch_max_size`,
which must not be smaller than `send_batch_size` (2048). `myrctl validate` accepts the file, but the collector rejects the
configuration when the supervisor validates it (`validate_config`). The fleet names no rollout policy, so the built-in default applies: one wave of 100 % and a pause on the
first failure. The supervisor reports `FAILED` and keeps the old
configuration. `myriarchd` sets `failedConfigHash` on the agent and does **not offer the configuration again**:
`supervisor.log` shows exactly one attempt. After you correct the value in Git, the planner replaces the paused rollout
with a new one and the collector returns to `in_sync`.

## Clean up

```bash
pkill -f opampsupervisor      # the supervisor ends the collector with it
api -X DELETE localhost:8080/api/v1/agents/$ID    # forget the agent once it is offline
```

Remove the fleet file and sync again to leave the working copy as it was. For Linux hosts with systemd and for Windows
hosts use the installation scripts in [Connect a host](connect-a-host.md); the OpAMP specifics of the supervisor are in
[upstream-notes.md](../design/upstream-notes.md).
