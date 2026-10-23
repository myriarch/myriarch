---
title: "Kubernetes on kind"
weight: 35
description: "Start Myriarch on a local kind cluster with the Helm charts: the server, a gateway and a node agent from the collector chart, a first configuration rollout and a first version rollout."
---

This tutorial starts Myriarch on Kubernetes on your machine in about ten minutes (measured in CI, see
[How long it takes](#how-long-it-takes)). You install the server with the Helm chart `myriarch`, run two real collectors from
the Helm chart `myriarch-collector` (a **gateway** as a StatefulSet and a **node agent** as a DaemonSet), sign in to the web UI
and roll out a configuration change and a new collector version.

> **Warning: a demo setup, not a production setup.** The script installs a throw-away PostgreSQL and a throw-away Git server
> into the cluster, the traffic inside the cluster is not encrypted, the generated passwords and keys are files in
> `.cache/kind/secrets` (mode `0600`), and the UI is reachable through a port-forward on `127.0.0.1` only. Use the cluster
> for this tutorial and delete it afterwards. For a real installation read [Install with Helm](../operations/install-helm.md),
> [Install the collector with Helm](../operations/install-collector-helm.md) and the
> [production checklist](../operations/production-checklist.md).

**Available from v0.2.0.** The tutorial installs the published OCI charts `oci://ghcr.io/myriarch/charts/myriarch` and
`oci://ghcr.io/myriarch/charts/myriarch-collector` and the release images. Before v0.2.0 is released, build both from the
checkout instead: add `QUICKSTART_SOURCE=commit` to the commands ([below](#start-from-a-checkout)).

## What you get

| Part | What it is |
|---|---|
| kind cluster `myriarch` | One control plane and two workers, with its own kubeconfig (`.cache/kind/kubeconfig`); `~/.kube` is never touched. |
| `myriarch` (namespace `myriarch`) | The server from the chart `myriarch`, two replicas, GitOps over SSH, Pod Security level `restricted`, a bootstrap admin with a one-time token. |
| PostgreSQL and a Git server (namespace `probe-infra`) | The demo database and the Git repository that holds the fleets, blueprints and policies. |
| `kind-gw` | The chart `myriarch-collector` as a **StatefulSet** with two replicas in container mode: the fleet `kind-gateway`. It starts on collector 0.161.0, so that you can update it. |
| `kind-nodes` | The same chart as a **DaemonSet**, one collector per worker node: the fleet `kind-nodes`. |
| Simulator (optional) | `QUICKSTART_SIM=1` adds 20 simulated collectors in the fleets `sim-edge` and `sim-dc`. |

The Git repository has two demo policies, `config-canary` and `canary-then-all`, copied from
[`examples/quickstart/policies/`](../../examples/quickstart/policies/): two waves (a canary, then the rest after your approval),
**a health wait of 30 seconds**, no limit on parallel targets and no maintenance windows. The health wait is the time a target
must stay healthy after it reported success, so a rollout is `succeeded` about 30 seconds after its last wave started. A
production-like policy waits minutes; see [Rollouts](../concepts/rollouts.md#health-wait).

## Prerequisites

- Docker Desktop or Docker Engine with at least 6 GiB of memory and 4 CPUs for the Docker VM, and the free ports `5001` (a local
  registry) and `8080` (the port-forward to the UI).
- `kind`, `kubectl` and `helm` on the `PATH`, or let the script build the pinned versions into `.cache/bin` (leave
  `KIND_SYSTEM_TOOLS` out; that needs Go, `make` and a few minutes more).
- Go (the script builds the small tool that fetches the Sigstore root of trust and validates the Git repository), `git`, `make`,
  `curl`, `jq`, `python3`, `openssl` and `ssh-keygen`.
- Network access to `ghcr.io`, Docker Hub, GitHub (the collector 0.162.0 is downloaded from its release) and
  `tuf-repo-cdn.sigstore.dev`.

## Start

Take the sources of the release, which carry the script, the demo Git repository and the version of the charts, and start:

```bash
TAG=v0.2.0           # the release you start; the newest one is on https://github.com/myriarch/myriarch/releases
git clone --depth 1 --branch "${TAG}" https://github.com/myriarch/myriarch.git
cd myriarch
KIND_SYSTEM_TOOLS=1 make kind-quickstart
```

`make kind-quickstart` is `hack/kind/quickstart.sh up`. It creates the cluster, installs PostgreSQL and the Git server, pushes
the demo repository, installs the server chart, then the collector chart twice, and waits until every pod is ready. It is
safe to run again: an existing cluster and releases are upgraded in place. It ends with the commands for the next step.
Add `QUICKSTART_SIM=1` to also start 20 simulated collectors.

Check the installation (it never signs in, so it does not use up the token of the next step):

```bash
KIND_SYSTEM_TOOLS=1 make kind-quickstart-smoke
```

It checks that the nodes and pods are ready, runs `helm test` of the server chart (`/readyz` and an OpAMP handshake) and checks the
StatefulSet and the DaemonSet. Use the cluster with its own kubeconfig:

```bash
export KUBECONFIG="$PWD/.cache/kind/kubeconfig"
kubectl -n myriarch get pods
```

## Sign in

```bash
kubectl -n myriarch port-forward svc/myriarch 8080:8080 &
kubectl -n myriarch get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d; echo
```

Open <http://localhost:8080> in a browser (Chrome, Firefox or Edge; they accept a session cookie on `localhost`), choose *Use the bootstrap admin*,
then *With one-time token*, and paste the token. The token works for **one** sign-in. To sign in again, rotate it: delete
the Secret `myriarch-bootstrap` and run `KIND_SYSTEM_TOOLS=1 make kind-quickstart` again; the chart creates a new one.

*Fleets* shows `kind-gateway` with two agents and `kind-nodes` with one agent per worker node. They are `online` and `healthy` a
few seconds after the pods are ready.

## Your first configuration rollout

Git sets the frame and the UI chooses inside it, so a configuration change starts with a commit. Push a new batch timeout
for the blueprint `kind-collector` to the Git server of the cluster:

```bash
hack/kind/quickstart.sh config-change 7s
```

The server reads Git every 10 seconds and starts one configuration rollout for each fleet, `kind-gateway` and `kind-nodes`. Open
*Rollouts*, choose a rollout and watch it: the **canary** wave (the first gateway pod, the first worker node) applies the
change, the target waits for its **health wait of 30 seconds** (the page shows the time that is left and names the policy), and the
rollout stops in `waiting_approval`. Press *Approve* for the wave `rest`; its targets follow and the rollout is `completed`
about 30 seconds later. *Agents* then show the configuration state `in_sync`, and the effective configuration of a collector
contains `timeout: 7s`.

## Your first version rollout

The gateway runs collector 0.161.0; the frame in Git offers 0.161.0 and 0.162.0. Open the fleet `kind-gateway`, choose *Update
version*, select 0.162.0 and confirm. The server downloads the collector from its GitHub release (about 110 MB, once) and
verifies its SHA-256 and Sigstore bundle; the `myriarch-updater` in each gateway pod downloads it from the server, verifies it
again offline against the trusted root in the ConfigMap `myriarch-trust`, swaps the binary on the volume `binaries` and
checks the health of the new process. The wave `canary` (first pod) goes first; approve the wave `rest`. A target is `succeeded`
30 seconds after the new collector reports its version and is healthy, and the rollout is `completed` about a minute after you
approved the last wave. The node agents run the version of their image (`executor: none`) and are not part of a version rollout.

Do both rollouts from a terminal instead, with the calls of the UI: `KIND_SYSTEM_TOOLS=1 make kind-quickstart-rollouts` signs
in with the bootstrap token (so use it **instead of** the UI sign-in, or rotate the token afterwards), runs the configuration
rollout and the version rollout, approves the waves and measures the durations.

## How long it takes

The CI job `quickstart` in `.github/workflows/e2e-kind.yml` runs `hack/kind/quickstart.sh all` (the steps of this page) on
every change of the charts, the updater or the script, and writes the duration of each step into the summary of the run. The
numbers on this page are *measured in CI* once there is a run of a release; a laptop needs more for the first run, because it
downloads the images and the node image. The documented targets are under ten minutes for the installation and about a minute
from the approval of the last wave to the completed version rollout (the script fails above four minutes).

## Start from a checkout

Without a release, the script builds the server image and the updater image from your working tree, pushes them to a local
registry that the nodes can pull from, and installs the charts of the checkout. This is what CI does:

```bash
QUICKSTART_SOURCE=commit make kind-quickstart          # needs a clean work tree (ALLOW_DIRTY=1 while you develop)
QUICKSTART_SOURCE=commit make kind-quickstart-all      # installation, smoke check and both rollouts, with the durations
```

The pinned tool versions of the repository (`make tools`) are the ones that CI tests; omit `KIND_SYSTEM_TOOLS=1` to use them.
The same cluster is used by the [Kubernetes probe](../../hack/kind/README.md); run only one of the two at a time.

## Clean up

```bash
make kind-down ALL=1          # deletes the cluster, the local registry and .cache/kind (with the generated secrets)
```

## Troubleshooting

- **A pod stays `Pending` or restarts.** Give Docker more memory (6 GiB) and CPUs (4), then run `make kind-quickstart` again.
- **`ImagePullBackOff` for an image of `ghcr.io` or Docker Hub.** The limit for anonymous pulls was reached; log in to Docker Hub
  in Docker Desktop and run the command again.
- **The bootstrap sign-in answers 401.** The token was used already; rotate it as described under [Sign in](#sign-in).
- **A rollout stays `pending`.** The agents are not registered yet or not healthy; check the *Agents* page and
  `kubectl -n myriarch get pods`. `hack/kind/diag.sh` collects the diagnostics into `.cache/kind/diag/`.
- **The version rollout fails or pauses.** The updater restored the old collector; the *Rollouts* page shows the step that failed,
  and `kubectl -n myriarch logs kind-gw-myriarch-collector-0` the reason. "Retry failed" starts a new attempt after the retry backoff.

## Next steps

- [Install with Helm](../operations/install-helm.md) and [Install the collector with Helm](../operations/install-collector-helm.md)
  for a real installation, and the [production checklist](../operations/production-checklist.md).
- [Roll out a configuration change](../tasks/roll-out-a-config-change.md) and
  [Roll out a collector version](../tasks/roll-out-a-collector-version.md) for the details of waves, approval and failures.
- [Rollouts](../concepts/rollouts.md#health-wait) for the health wait, and `gitops-template/` for production-like policies.
