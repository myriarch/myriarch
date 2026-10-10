---
title: "Container mode"
weight: 10
description: "How myriarch-updater runs as the main process of a container and swaps the collector binary on a volume."
---

In container mode `myriarch-updater` is the main process (PID 1) of a container. It starts the OpAMP Supervisor as a child
process and exchanges the collector binary on a persistent volume. The update logic is the same as on a host with a
service manager: download, verification, atomic swap, health check, rollback. Only the "service" being restarted is a
child process instead of a systemd unit or Windows service. See [updater-protocol.md](updater-protocol.md) for the
shared install behaviour.

Container mode is an explicit opt-in. On Kubernetes the preferred way to change a collector version is to change the
image reference through the deployment repository, because swapping a binary makes the running binary differ from the
declared image (see [ADR-0026](../adr/0026-collector-version-updates-on-kubernetes.md)). Container mode is meant for
environments where that is not possible, such as air-gapped or edge clusters.

## Invocation

`myriarch-updater container --config <file>`. The command runs on Linux and on macOS (useful for local end-to-end tests). On
Windows it fails with an explanatory error, and the updater runs there as a service next to the supervisor service
(`myriarch-updater run`). The image entry point stays that of the supervisor image, so the image remains backward
compatible, and the container command overrides it.

## Process model

- The updater is PID 1. It starts `container.supervisorCommand` as a child in its own process group and passes the
  child's output through unchanged.
- One central wait loop collects the exit status of every child. As PID 1 (or as a registered child subreaper on Linux)
  it also reaps orphaned descendants, so the collector process the supervisor spawns never lingers as a zombie. On macOS
  only the processes started through the reaper are collected.
- Shutdown on SIGTERM or SIGINT: a running installation may finish its critical section (the part after the supervisor
  was stopped), then the child's process group receives SIGTERM, then SIGKILL after `stopTimeout`. The exit code is 0.
- SIGHUP, SIGUSR1 and SIGUSR2 are forwarded to the child.
- Service abstraction: the child is driven through the same `ServiceController` interface as a systemd unit. `Exists` is
  always true, `State` is running, starting (waiting for the restart backoff), stopping or stopped, `Stop` and `Start`
  stop and start the child, and `Environment` is the updater's own environment. Installer, health checker and journal
  recovery are unchanged.
- Unexpected end of the child: it is restarted with a delay that starts at `restartDelay` and doubles up to 60 seconds. A
  child that ran for at least a minute resets the delay. More than `maxRestarts` unexpected ends within
  `restartWindow` stop the container with exit code 1, so that the container runtime restarts it and the crash loop is
  visible. While the installer has stopped the child on purpose there is no automatic restart.

## Volume layout

The collector binary lives on a persistent volume in the same layout as on a host:

```text
<collector.installDir>/versions/<version>/<binary>
<collector.installDir>/current -> versions/<version>
```

The supervisor configuration sets `agent.executable: <installDir>/current/<binary>`. The updater state
(`stateDir`: its instance UID, package state, install journal, the last update attempt and the download spool) is on the
same volume. Activation must be `symlink` in container mode. The supervisor's own storage directory (with
`persistent_state.yaml` and therefore the collector's instance UID) should be persistent and independent of the binary
volume. The updater never writes there, so the collector identity survives restarts and binary updates.

Sizing: the volume holds at most the active version, one previous version, a staging copy during an install and the
spool of the downloaded archive. Plan for roughly two to three times the unpacked collector binary plus one archive,
about one gigabyte for the contrib distribution at the time of writing, and set `collector.keepVersions` accordingly.
The volume is a single-writer volume, so the deployment runs a single replica.

## Seeding and precedence

The image carries a collector binary (`container.seedBinary`). At every start, before the child is started, the updater
decides which binary is active:

1. Run `seedBinary --version` to learn the image version X. If that fails and a version is already active, keep the
   active one and log the decision. If nothing is active, the start fails.
2. Decide, per `container.seedPolicy`:

   | Situation | Decision |
   |---|---|
   | no version active yet | Activate the image version X. |
   | policy `image` | Activate X, unless it already is the active, intact, image-sourced binary. This is the emergency switch. |
   | active binary missing, not executable, or different from the hash in its marker | Activate X. |
   | policy `newest`, active version newer than X | Keep the active one. |
   | policy `newest`, active version equal to X | Keep the active one (a tie keeps the volume). |
   | policy `newest`, active version older than X | Activate X. |
   | versions not comparable | Activate X. |

3. Activating the image version copies the binary into `versions/X` through a staging directory with fsync and a SHA-256
   check, writes an install marker with source `image` and with the previously active version as `previousVersion`
   (which keeps the one-step return possible under the anti-downgrade policy), and then switches `current`. An intact
   existing directory of the same version is reused. The image binary is copied only when it is about to be activated.
4. If an update journal from an interrupted update exists, journal recovery runs first and seeding is evaluated again
   afterwards. Until then an existing active version is never replaced.

The rule "the higher version wins" fits the anti-downgrade logic: a new image with a higher collector version is an
explicit way upwards, while an image older than what the updater installed does not roll the collector back.

## Health, probes and rollback

- The update health check is the one of the host mode: the child runs, the supervisor health URL answers, and the
  collector health URL answers (the `health_check` extension of the collector configuration), `healthSuccesses` times in
  a row within `healthTimeout`.
- The updater serves its own endpoints on `container.healthListen`: `GET /healthz` is liveness (the updater's main loop
  has a heartbeat younger than 30 seconds; it is fed during seeding and recovery so that a startup probe sees a live
  container) and `GET /readyz` is readiness (the child runs and no swap is in progress). Both answer 200 or 503 with a
  short text.
- Rollback is unchanged: an error after the swap reactivates the previous version, restarts the child and checks health.
  The journal covers hard kills, and recovery runs at the next start before the child is started.

## Anti-downgrade, identity and connection

- The host policy is unchanged: `update.minimumVersion`, `update.allowDowngrade` and the one-step return to
  `previousVersion`.
- The updater keeps its own instance UID in `stateDir`. The collector's UID is read from `persistent_state.yaml` in the
  supervisor storage directory and reported as `dev.myriarch.collector.instance_uid`. The updater uses the same enrollment
  token and the same host name as the supervisor (in a pod: the same token source and the pod name), which is what
  the server's linking rule requires.
- The attributes `dev.myriarch.updater.install_method=container` and `dev.myriarch.updater.image_collector_version` are
  reported (see [updater-protocol.md](updater-protocol.md#identity-and-attributes)).
- Connection and download: `server.endpoint` points at myriarchd. A plain `ws://` endpoint to a non-loopback host needs
  `server.allowInsecureTransport: true`, appropriate only inside a trusted network. By default the allowed download
  prefix is the artifact path on the host of that endpoint.
- Verification: `update.requireSignature: true` with a `trustedRootFile` for offline verification, and the preflight
  `validate` run under the same user as the supervisor.

## Configuration

The `container` section of `updater.yaml` is required only for the `container` command and is validated there.

```yaml
container:
  supervisorCommand: ["/usr/local/bin/opampsupervisor", "--config=/etc/opampsupervisor/supervisor.yaml"]
  seedBinary: /usr/local/bin/otelcol-contrib   # collector binary in the image
  seedPolicy: newest                           # newest | image
  stopTimeout: 30s
  restartDelay: 2s
  maxRestarts: 5
  restartWindow: 10m
  healthListen: "0.0.0.0:13135"                # empty disables the endpoints
```

Validation: `supervisorCommand` and `seedBinary` are required, `seedPolicy` is one of the two values, `stopTimeout`,
`restartDelay` and `restartWindow` are positive, `maxRestarts` is at least 1, `healthListen` is `host:port` when set, and
`collector.activation` must be `symlink`.

## Operational limits

- Termination grace period: set the pod's `terminationGracePeriodSeconds` to at least two stop timeouts plus two
  health timeouts plus a margin, so that a shutdown during a swap can finish its critical section. With the defaults
  that is about six minutes.
- Memory: the preflight `validate` run starts a second collector process next to the running one. Give the container
  headroom for two collector processes, and measure it.
- Supervisor and collector version skew: after an update the supervisor of the image may control a newer collector.
  Keep selectable versions inside one minor version of the image, or update the image as well.
- Probes: use `/healthz` for startup and liveness (it stays green during seeding and recovery) and the collector's own
  health endpoint or `/readyz` for readiness.
- Exactly one replica per volume. Scaling out needs one volume per pod, for example through a volume claim template.
