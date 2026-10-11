---
title: "Docker Compose"
weight: 30
description: "Start Myriarch with Docker Compose on one machine: server, PostgreSQL and 200 simulated collectors for an evaluation."
---

This tutorial starts a complete system on one machine with Docker Compose: the server `myriarchd`, a PostgreSQL container and
200 simulated collectors that connect to the example GitOps repository. You need no checkout of the source code and no
Go or Node.js toolchain when you use the release images.

> **Warning: development mode, not a production setup.** The Compose file runs `myriarchd dev`. The logins (`admin` /
> `admin`, `operator` / `operator`, `viewer` / `viewer`) and the enrollment token are well known, the cookies are not
> marked `Secure`, there is no TLS, and the GitOps source is a directory without Git. The port is published on
> `127.0.0.1` only; do not change that and do not expose the server to a network. `myriarchd serve` refuses exactly
> these settings, so a single-machine setup without TLS and without a Git server can only use dev mode. For a real
> installation, follow [Install with Helm](../operations/install-helm.md) and the
> [production checklist](../operations/production-checklist.md). A production Compose setup with TLS and a Git source
> is planned for v0.2.0.

## What you get

| Service | What it is |
|---|---|
| `postgres` | PostgreSQL 17, the database of the server. Not published on the host; the data lives in the volume `pgdata`. |
| `myriarchd` | The server in dev mode with the example GitOps repository `examples/quickstart` from the image. Web UI, REST API, OpAMP endpoint and `/metrics` on <http://localhost:8080>. Its data directory `/var/lib/myriarchd` (artifact cache and clone directory) is on the volume `myriarchd-data`. |
| `myriarch-sim` | The simulator: 200 collectors and 180 updaters, spread over the fleets `linux-hosts`, `windows-hosts` and `k8s-gateway`. |

## Prerequisites

- Docker Engine or Docker Desktop with Compose v2 (the command `docker compose`).
- Internet access to pull the images. The release tracker also polls `api.github.com` for new collector releases; without
  that access the *Releases* page of the UI stays empty and everything else works.

## Start with the release images

Available from v0.1.0. The images are `ghcr.io/myriarch/myriarch` (the server, `myrctl` and the simulator) with the tags
`X.Y.Z` and `X.Y`, and `X.Y.Z-rc.N` for release candidates. There is no tag `latest`. Before the first release the images
do not exist; use the checkout path below instead.

Download the Compose file of the release and start it:

```bash
curl -fsSLO https://raw.githubusercontent.com/myriarch/myriarch/v0.1.0/deploy/compose/quickstart.yaml
docker compose -f quickstart.yaml up -d
docker compose -f quickstart.yaml logs -f myriarchd
```

The file is self-contained. Its default image tag is the version of the release it belongs to, so the file of every tag,
release candidates included, starts the image of that tag (`ghcr.io/myriarch/myriarch:0.1.0` in the file of `v0.1.0`; use
`v0.1.0-rc.1` in the URL for a release candidate). `QUICKSTART_IMAGE` selects any other image, for example one that you
pinned by digest (`ghcr.io/myriarch/myriarch@sha256:...`) or copied to your own registry:

```bash
QUICKSTART_IMAGE=ghcr.io/myriarch/myriarch@sha256:<digest> docker compose -f quickstart.yaml up -d
```

[Verify releases](../security/verify-releases.md) explains how to check the files of a release, and
[how to verify the container images](../security/verify-releases.md#verify-the-container-images) (signature, provenance, SBOM
and the digest to pin) before you start them.

The log of `myriarchd` shows the `DEV MODE` warning and `http server listening` once the database is ready. Open
<http://localhost:8080> and sign in as `admin` with the password `admin`. After a short ramp-up the agents of the fleets
`linux-hosts`, `windows-hosts` and `k8s-gateway` appear. `SIM_AGENTS` changes the number of simulated collectors
(default 200):

```bash
SIM_AGENTS=50 docker compose -f quickstart.yaml up -d
```

The containers run as the user `65532` with a read-only root file system, an empty capability set and
`no-new-privileges`. The image contains the empty data directory `/var/lib/myriarchd` owned by that user, so Docker
creates the volume `myriarchd-data` writable for it. The image has no shell, so the services have no health check;
`docker compose ps` shows them as running.

## Start from a checkout (before the first release)

Without a release image, build the images from the source tree. The build runs inside Docker (Node.js for the web UI, Go
for the binaries), so no toolchain is needed on the host, but it takes several minutes the first time:

```bash
git clone https://github.com/myriarch/myriarch.git
cd myriarch
docker compose -f deploy/compose/docker-compose.yaml up --build                 # PostgreSQL and myriarchd
docker compose -f deploy/compose/docker-compose.yaml --profile sim up           # additionally 200 simulated collectors
```

This development file mounts the example GitOps repository from the checkout and reads recorded release data from
`internal/release/testdata/fixtures`, so the release tracker needs no network access at runtime. It is the same system:
the UI is on <http://localhost:8080> with the same logins. See [`deploy/README.md`](../../deploy/README.md) for the files under `deploy/`.

## Look around

The endpoints below are all on the one published port:

```bash
curl localhost:8080/readyz     # ready
curl localhost:8080/healthz    # ok
curl -s localhost:8080/metrics | grep '^myriarch_' | head
```

The REST API accepts the development login; the sign-in and the `api` shell function are explained in
[Local development](local-dev.md#call-the-rest-api) (replace the cookie file name as you like):

```bash
curl -s -c cj -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' \
  localhost:8080/api/v1/auth/dev-login | jq .role
```

The UI shows what the simulator reports: the fleets, the agents with their configuration state and the rollouts. To see
how a change travels through the fleets, continue with [Your first fleet and rollout](first-fleet-and-rollout.md), which
uses the same dev mode.

## Use your own GitOps directory

The server reads the directory `/opt/myriarch/examples/quickstart` of the image. To work on your own copy, copy the example
repository out of the container and mount it back:

```bash
docker compose -f quickstart.yaml cp myriarchd:/opt/myriarch/examples/quickstart ./gitops
```

Create the override file `gitops.yaml` next to it:

```yaml
services:
  myriarchd:
    volumes:
      - ./gitops:/gitops:ro
```

and start the stack with both files and the directory name inside the container:

```bash
QUICKSTART_GITOPS_DIR=/gitops docker compose -f quickstart.yaml -f gitops.yaml up -d
```

The server re-reads the directory every 10 seconds, so an edit in `./gitops` reaches the system without a restart; a change
of a blueprint or a fleet starts a configuration rollout (see [Roll out a configuration change](../tasks/roll-out-a-config-change.md)).
The files must be readable by the non-root user of the container: keep them world-readable (mode `0644` for files, `0755`
for directories). In the example repository all three fleets use the configuration policy `config-canary`, which has no
maintenance windows: a configuration change rolls out at once, and the wave `rest` waits for your approval. Version
rollouts use the policy `canary-then-all` with night-time windows; for a version rollout that you want to watch within
seconds, point `versionPolicy` of your copy at `config-canary` as described in
[`examples/quickstart/README.md`](../../examples/quickstart/README.md). `myrctl validate` checks the directory before the
server does ([Validate a GitOps repository](../tasks/validate-a-gitops-repository.md)).

## Connect a real collector

OpAMP is served on the same port. A real OpAMP Supervisor on the host connects to `ws://localhost:8080/v1/opamp`; in dev
mode it needs no enrollment token, and the well-known token `myre_devtoken_insecure_do_not_use_in_prod` is accepted as well.
[Connect a real supervisor](../tasks/connect-a-real-supervisor.md) walks through the setup, with the endpoint of this
page instead of a server started from source.

## Stop and reset

```bash
docker compose -f quickstart.yaml down       # stops the containers, keeps the volumes pgdata and myriarchd-data
docker compose -f quickstart.yaml down -v    # also deletes both volumes: the next start begins from scratch
```

Add the same `-f` options and variables that you used for `up`. All state of the server is in the database; the volume
`myriarchd-data` holds only derived data (the artifact cache and the clone directory), which the server rebuilds on
demand, so it survives restarts but you can delete it at any time.

## Next steps

- [Your first fleet and rollout](first-fleet-and-rollout.md): write a fleet and a rollout policy yourself and roll out a
  change in waves.
- [Install with Helm](../operations/install-helm.md): the chart for a real installation on Kubernetes.
- [Production checklist](../operations/production-checklist.md): the settings that matter once you leave a laptop.
