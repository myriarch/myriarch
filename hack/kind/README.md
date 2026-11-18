# Kubernetes probe on kind

`hack/kind` builds a local Kubernetes cluster from zero, installs the whole system into it with the Helm chart from this
repository and checks the flow an operator goes through: sign-in, agent registration, configuration rollout, a collector
version update by the calls of the UI button (including the automatic rollback on the real collector), the restart of a
server pod and the takeover of the leader. Everything runs on your machine with Docker, without sudo, without edits of
`/etc/hosts`, without a browser and without a container runtime other than the one that runs kind.

The probe is a test of its own that CI runs too ([In CI](#in-ci)). Other cluster tests can reuse the scripts (`up.sh`, `images.sh`,
`install.sh`, `diag.sh` and the stage libraries).

## Quickstart for users

`quickstart.sh` is the user-facing sibling of the probe: it installs the **published** charts and release images (or, with
`QUICKSTART_SOURCE=commit`, the charts and images of this commit) with the collector chart as a gateway and as a node agent, and
runs a first configuration and version rollout. [docs/getting-started/kubernetes-kind.md](../../docs/getting-started/kubernetes-kind.md)
is its page, and CI runs the same script (the job `quickstart` of `.github/workflows/e2e-kind.yml`), so the page cannot go stale.

```bash
make kind-quickstart                                 # = hack/kind/quickstart.sh up
make kind-quickstart-smoke                           # pods, "helm test", StatefulSet and DaemonSet; never signs in
hack/kind/quickstart.sh config-change 7s             # the first configuration rollout (push to the Git server of the cluster)
make kind-quickstart-rollouts                        # signs in with the bootstrap token, runs both rollouts through the API
make kind-quickstart-all QUICKSTART_SOURCE=commit    # the CI path: all of it, with the durations in .cache/kind/quickstart-summary.md
```

It shares the cluster, the registry, the work directory and the helpers with the probe (`up.sh`, `images.sh`, `lib/`), so run one
of the two at a time. The Git repository of the cluster is built from `quickstart/gitops` (the blueprint and the two fleets),
`quickstart/sim` (the simulated fleets, `QUICKSTART_SIM=1`; commands without the variable use the simulator if the cluster has it) and the demo policies of `examples/quickstart/policies` (health wait
30 s, `maxConcurrent: 0`, no windows: the same copy as in the Compose Quickstart), plus an inventory that makes the first
gateway pod and the first worker the canary wave. `values/quickstart.yaml` holds the values of the server chart.
`KIND_SYSTEM_TOOLS=1` uses `kind`, `kubectl` and `helm` from the `PATH` instead of the pinned ones.

## Quick start

```bash
make kind-probe        # = hack/kind/probe.sh --fresh: cluster, images, installation, all checks; exit 0 only without FAIL
```

A run from zero takes about 9 minutes on Apple Silicon with Docker Desktop (the first run also downloads the node image,
the upstream images and the collector release from GitHub). The result is `PASS`, `FAIL` or `SKIP` per numbered check
(`N.M`), a summary and the stage durations. The cluster stays up afterwards, so that you can look around:

```bash
export KUBECONFIG="$PWD/.cache/kind/kubeconfig"
kubectl -n myriarch get pods
kubectl -n myriarch port-forward svc/myriarch 8080:8080            # then open http://localhost:8080
```

The bootstrap token of the UI is one-time. The probe spends the first one; rotate it like an operator would
(delete the Secret, run `helm upgrade` again, wait for the rollout) or read the current one:

```bash
kubectl -n myriarch get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d
```

Targets: `make kind-up` (cluster and registry only), `make kind-down` (delete the cluster; `make kind-down ALL=1` also removes
the registry container and the work directory), `make kind-probe`.

## Prerequisites

* Docker Desktop (or another Docker engine) with at least 6 GiB of memory and 4 CPUs for the VM (`up.sh` warns below that).
* The tools the repository already needs: Go, Node.js (`.node-version`) with pnpm (the web UI is built into the server image), `python3`,
  `openssl`, `ssh-keygen`, `curl`, `jq`, `git`, `make`. kind, kubectl, helm, crane and yq are pinned in `ci/versions.env` and
  built or downloaded into `.cache/bin` by `ci/install-tools.sh` (kubectl is a download verified by SHA-256).
* Network access to Docker Hub, `ghcr.io`, GitHub (releases and the Sigstore TUF root), the Go proxy, the Alpine package
  mirror and `charts.dexidp.io`. Anonymous Docker Hub pulls are limited (100 per 6 hours and IP); a Docker Desktop login
  raises the limit and is picked up by the image scripts.
* Free local ports: 5001 (registry), 28080 and 28081 (port-forwards to the two myriarch releases), 25554 (Dex), and 28091 and 28092 (the
  metrics of the two replicas, in the NetworkPolicy stage).

## What the probe builds

| Part | Namespace | Notes |
|---|---|---|
| kind cluster | | One control plane, two workers, fixed pod and service subnets. Node image pinned by digest in `versions.env`. |
| Registry | (Docker) | Container `myriarch-kind-registry`, `127.0.0.1:5001`, reachable from the nodes as `localhost:5001` through `hosts.toml`. |
| Images | | `scripts/build-image-crane.sh` and `contrib/legacy-kubernetes-collector/build-collector-image-crane.sh` for the node architecture (`IMAGE_PLATFORM`), pushed with crane. |
| PostgreSQL 17, SSH Git server | `probe-infra` | The GitOps source must be `ssh://` or `https://` in `serve` mode, so the fixture lives on a real Git server. |
| myriarch chart | `myriarch` | Pod Security `restricted` is enforced. First with only the required values, then with the full configuration. |
| Simulator | `myriarch` | 20 collectors (`examples/sim/small.yaml`), registered with the declared token `kind-sim`. |
| Real collector | `myriarch` | StatefulSet `kind-gw`: `myriarch-updater` as PID 1 (container mode) with the upstream supervisor and `otelcol-contrib`. |
| Dex and a second release | `dex`, `myriarch-dex` | `examples/quickstart-dex.yaml` with `examples/dex/dex.yaml`, a test CA and a CoreDNS rewrite. |
| NetworkPolicy tests | `probe-outside` | A self-test proves that the CNI enforces policies before the chart policy is checked. |

The collector image carries 0.161.0 and the probe updates it to 0.162.0 and back: the artifact comes from GitHub and is
verified with its SHA-256 and the Sigstore bundle by the server, the updater verifies it again offline.

## Stages

| Stage | Content |
|---|---|
| 0 | Prerequisites, tools, `make build` |
| 1 | Cluster and registry (`up.sh`) |
| 2 | Images for the node architecture |
| 3 | PostgreSQL, Git server, the GitOps fixture (`gitops/`), seed commit |
| 4 | Chart with only the required values: `publicUrl`, image, Secret `database-dsn`; the NOTES command prints a usable token |
| 5 | Bootstrap admin: sign-in through the API, reuse of the token refused, audit entries |
| 6 | `helm upgrade` with GitOps over SSH and declared enrollment tokens |
| 7 | Simulator and real collector register (labels, fleets, updater link) |
| 8 | Configuration rollout until every collector is `in_sync` |
| 9 | Version update through the UI calls on the real collector: dialog data, failure with automatic rollback (fault injection), retry, pod restart, back to the Git target |
| 10 | Leader takeover |
| 11 | `helm upgrade` that changes the `expiresAt` of a declared token |
| 12 | Rotation of the bootstrap token (`PROBE_SKIP=rotation`) |
| 13 | NetworkPolicy (`PROBE_SKIP=netpol`) |
| 14 | Dex quickstart with the OIDC login done by curl (`PROBE_SKIP=dex`) |
| 15 | Logs and secret hygiene: no crash, no secret in logs, Helm values, ConfigMaps or pod specs, no restarts |

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `KIND_CLUSTER_NAME` | `myriarch` | Cluster name (context `kind-<name>`) |
| `KIND_REGISTRY_NAME`, `KIND_REGISTRY_PORT` | `myriarch-kind-registry`, `5001` | Registry container and its local port |
| `KIND_NODE_IMAGE` | pinned in `versions.env` | Node image (another Kubernetes version) |
| `PROBE_WORK` | `.cache/kind` | Work directory: kubeconfig, generated secrets (mode 0600), logs, results, diagnostics |
| `PROBE_SKIP` | empty | Comma list of `rotation,netpol,dex,downgrade`; a skip is reported as `SKIP` with its reason |
| `PROBE_STAGES` | all | Run only these stages against the existing state, for example `PROBE_STAGES="9 10"` while developing |
| `PROBE_REUSE_IMAGES` | `0` | `1` keeps the last `images.env` and builds nothing |
| `ALLOW_DIRTY` | `0` | `1` builds the server image from a dirty tree (the image script refuses it otherwise; the stage 0 check reports a `SKIP`) |
| `WEB_BUILD` | `auto`; `--fresh` uses `always` | Web UI build for the server image |
| `PROBE_PORT_API`, `PROBE_PORT_DEXAPI`, `PROBE_PORT_DEX` | `28080`, `28081`, `25554` | Local port-forward ports (different from the ports of `scripts/e2e*.sh`, so both can run together); the NetworkPolicy stage also forwards the metrics of the two replicas to `PROBE_PORT_API` + 11 and + 12 |

Modes of `probe.sh`: `--fresh` removes cluster, registry and work directory first; `--reset` keeps cluster and registry and
deletes the probe namespaces (and the CoreDNS rewrite); without a flag it requires `up.sh` and fails when the namespaces exist.

## Files

* `probe.sh` is the driver, `lib/stages-*.sh` hold the numbered stages, `lib/common.sh`, `lib/api.sh` and `lib/install.sh` the
  helpers (REST calls with a cookie jar and CSRF token, port-forward lifecycle, rollout observation, installation steps).
* `up.sh`, `down.sh`, `images.sh`, `install.sh` (installation without checks), `diag.sh` (diagnostics, called on failure).
  `ci/kind-chart-test.sh` (outside this directory) is the chart installation test that reuses them.
* `upgrade.sh` is the [upgrade test](#upgrade-test-from-the-previous-release), `lib/upgrade.sh` its helpers (pins, snapshots, comparison)
  and `testdata/upgrade/` the fixtures of the tests of the comparison.
* `cluster.yaml`, `versions.env` (all image pins carry a digest; `ci/check.sh` verifies it), `manifests/`, `values/`, `gitops/`,
  `git/` (scripts of the Git server), `fault/killer.sh` (fault injection for the rollback check).

## Chart installation test

`ci/kind-chart-test.sh` is the second test on this cluster. The probe checks the whole system; this one checks only the two Helm
charts, installed the way a user installs them, and prints PASS / FAIL per step in the same format (`results.txt`, `probe.log`).
It reuses `up.sh`, `images.sh` (with `IMAGES_SET=server,updater`: the server image and the static `myriarch-updater` of the commit, no
legacy collector image), `diag.sh` (with `DIAG_EXTRA_NS` for its namespaces) and the libraries of the probe, so the cluster, the
registry, the work directory and the node image are the same ones.

```bash
ci/kind-chart-test.sh --fresh    # cluster, registry and work directory from zero; --reset keeps cluster and registry
```

| Stage | What it checks |
|---|---|
| 0, 1, 2 | Docker and the schemas of both charts, the cluster (three nodes Ready, the Kubernetes version is printed), the images of the commit in the local registry |
| 3 | PostgreSQL, the enrollment tokens (generated into `secrets/`, never printed) |
| 4 | Server chart in a namespace under the Pod Security level `restricted` with test values (declared enrollment tokens, `tests.token`): `helm install --wait`, Pods Ready, `helm test` (the Pod checks `/readyz` and the OpAMP WebSocket handshake with a token that exists for the test only), the test Pod is removed |
| 5 | Collector chart as a StatefulSet with two replicas in a `restricted` namespace and as a DaemonSet with `nodeLogs` in a `privileged` namespace (the documented exception: the PSA levels `baseline` and `restricted` refuse the `hostPath` volume); every agent is registered, online and healthy at the server (REST API, filtered by `site.id`); a deleted StatefulSet Pod comes back as the same agent |
| 6 | `helm uninstall --wait=legacy --cascade foreground` of all three releases (Helm reads every deleted resource until it is gone, within five minutes; the strategy of a bare `--wait` in Helm 4.2 (watcher) can end its wait early, helm/helm#32261) leaves none of their resources (the PVCs of the StatefulSet stay by design and are deleted by the test) |
| 7 | `helm upgrade` from the chart of the tag `v0.1.0` (taken from Git with `git archive`, running the image of that release, pinned by digest as `UPGRADE_FROM_SERVER_IMAGE` (another tag needs `CHART_TEST_UPGRADE_FROM_IMAGE` with a digest), on an empty second database) to the chart and the image of the commit, then `helm test` and a clean uninstall |

`CHART_TEST_UPGRADE_FROM` names another tag for stage 7. The upgrade stage pulls the released server image from `ghcr.io` and the
checkout needs the tag (the script fetches it when it is missing). Not covered, on purpose: a failover with `docker pause`,
an arbitrary user ID, the Compose smoke test, updates of the container mode and a Quickstart on kind. The test shares the cluster
and the work directory with `make kind-probe`: run one of the two at a time.

## Upgrade test from the previous release

`upgrade.sh` checks the upgrade path from the last minor release. It pulls the published server chart of the previous version from the
OCI registry (`helm pull`), compares the digest that Helm reports with the pin, verifies the signature of the chart and of the server image
with `cosign verify` against the identity of the release workflow, and only then installs that chart and image (the pulled archive, not the
registry reference). It publishes the fleets of the GitOps fixture (`gitops/`), connects the simulated agents and creates audit entries. Then it
upgrades to the chart and the images of this commit (`helm upgrade`, never `--reuse-values`) and compares the state before and after:

* the fleets (name, priority, blueprint, selector and source),
* the assignments (the fleet and the target version of every agent),
* the audit log (every event from before is still there and unchanged; new events are allowed), and
* the connections: every agent that was online is online again, and no agent has a new id (a new id would be a new registration).

The script exits non-zero when one of them differs. The comparison is a shell function (`upgrade_compare` in `lib/upgrade.sh`) that works on
three JSON files per state, so `go test ./hack/kind/` tests it against the fixtures in `testdata/upgrade` without a cluster. The same tests check
the syntax of the script, its execute bit, that it stops with one line when a tool is missing, and the pins.

```bash
hack/kind/upgrade.sh --fresh    # cluster, registry and work directory from zero
hack/kind/upgrade.sh --reset    # keep cluster and registry, delete the namespaces myriarch and probe-infra, then run
hack/kind/upgrade.sh            # requires hack/kind/up.sh; stops when the namespace myriarch exists
```

A full run needs Docker (kind) and network access to `ghcr.io`, Docker Hub, Sigstore (the TUF root that `cosign` fetches) and the Go proxy.
`kind`, `kubectl`, `helm` and `cosign` must be on the `PATH` (`make tools` installs the pinned ones into `.cache/bin`); the script stops with one
line that names the first one that is missing. It also needs `docker`, `crane`, `jq`, `curl`, `git`, `python3`, `openssl`, `ssh-keygen` and `go`.

| Variable | Default | Meaning |
|---|---|---|
| `UPGRADE_KUBE` | `current` | `current` runs on `KIND_NODE_IMAGE`, `previous` on `KIND_NODE_IMAGE_PREVIOUS` |
| `KIND_CLUSTER_NAME`, `PROBE_WORK`, `PROBE_PORT_API`, ... | see [Environment](#environment) | The same variables as the probe |

The previous version and its pins are in `versions.env`: `UPGRADE_PREVIOUS_VERSION`, `UPGRADE_PREVIOUS_CHART_DIGEST` (the digest of the OCI
manifest of the chart) and `UPGRADE_PREVIOUS_SERVER_IMAGE` (tag and digest of the index of the image). Renew the three together with `crane digest
ghcr.io/myriarch/charts/myriarch:<version>` and `crane digest ghcr.io/myriarch/myriarch:<version>`, then run `hack/kind/upgrade.sh --fresh`. The
Kubernetes versions are the node image pins of the probe; there is no pin of its own.

The results are below `.cache/kind`: `results.txt` (`PASS|N.M|name|evidence`), `upgrade/before` and `upgrade/after` (the snapshots, `fleets.json`,
`agents.json`, `audit.json`), `upgrade/compare.log` and `logs/` (`cosign-chart.log`, `cosign-image.log`, `helm-*.log`). The test shares the cluster,
the registry and the work directory with the probe and the chart installation test: run one of the three at a time. No CI job runs the script yet, and it
is not part of `ci-ok`.

## Inspecting and cleaning up

Everything the probe writes is below `.cache/kind` (ignored by Git): `probe.log`, `results.txt`, `logs/`, `diag/<timestamp>/`
after a failure (cluster state, logs, Helm status, API snapshots; no Secret is dumped) and `secrets/` (mode 0700). The generated
secrets never appear on a command line, in the log or in a commit.

```bash
cat .cache/kind/results.txt              # PASS|N.M|name|evidence
hack/kind/diag.sh                        # collect diagnostics now
make kind-down                           # delete the cluster
make kind-down ALL=1                     # also the registry container and .cache/kind
```

## In CI

`.github/workflows/e2e-kind.yml` runs `make kind-probe` on an `ubuntu-24.04` runner of GitHub Actions: every night (01:51 UTC), for pull
requests that touch the chart, the updater, the images (their build scripts, labels and pins), the tools or the scripts of the probe (a
path filter that names the files the probe reads or runs besides this directory), and on a manual run (`workflow_dispatch`), which can
set `PROBE_SKIP` (`rotation,netpol,dex,downgrade`). The job installs the pinned tools with
`.github/actions/setup` (kind, kubectl, helm, crane and yq; `ci/install-tools.sh` retries their downloads after a transient network
error), runs the probe without any change and shows the summary block of `probe.log` in the job summary. It is not part of `ci-ok` and
not a gate of the release; before a tag the last green run of the commit does what a local `make kind-probe` did
([Releasing](../../docs/releasing.md)).

The matrix has one entry per Kubernetes version. `image-var` names the pin of `versions.env` with the node image (`KIND_NODE_IMAGE`
and `KIND_NODE_IMAGE_PREVIOUS`) and the workflow passes it as `KIND_NODE_IMAGE`, so the digest is in this file only and `ci/check.sh`
keeps holding it. The probe job (`kind`) runs on the newest version. The second job, `charts`, runs the [chart installation
test](#chart-installation-test) (`ci/kind-chart-test.sh --fresh`) on both versions, starts under the same conditions as the probe,
shows the summary block in the job summary and uploads the same allow-list after a failure (artifact `e2e-kind-charts-<label>`). A
further Kubernetes version is a further pin in `versions.env` and a further entry with its `image-var`; the kubectl pin must stay
within the skew of every node image.

A failed run uploads an allow-list for seven days: `probe.log`, `results.txt`, `durations.txt`, `logs/` and `diag/` below
`.cache/kind`. It never uploads the kubeconfig, `secrets/`, the cookie jars, the Helm credentials in `helm/`, `values-tokens.yaml` or
`logs/hygiene-dump/` (the scan dump of stage 15), because anyone who is signed in to GitHub can download the artifacts of a public
repository; `ci/tests/workflow-artifacts-test.sh` checks the patterns. The step that runs the probe has a timeout of its own, below
that of the job, so that a probe that hangs counts as a failed step and still uploads its logs (a job that runs out of time is
cancelled, and the upload is skipped for a cancelled job); the same test holds the margin and the path filter. Anonymous pulls from
Docker Hub (the node image, `registry`, `postgres`, `alpine`, `busybox`, `curlimages/curl` and the upstream collector images) can hit
the pull limit of a busy day; the workflow has no secret to log in with, so rerun the job.

## Troubleshooting

* **Port 5001 is in use.** `up.sh` stops with a message; set `KIND_REGISTRY_PORT` to a free port.
* **Docker has too little memory.** The collector pod limits at 1.5 GiB and PostgreSQL at 768 MiB; give Docker Desktop at least
  6 GiB. `OOMKilled` containers show in `diag/` (`describe-*.txt`).
* **Docker Hub rate limit.** Pulls of `registry`, `postgres`, `alpine`, `busybox`, `curlimages/curl` and the upstream collector
  images fail with `toomanyrequests`. Log in to Docker Hub in Docker Desktop and rerun with `--reset` (images are kept in the
  local registry; `PROBE_REUSE_IMAGES=1` skips even the registry check).
* **`apk add` fails in the Git server pod.** The pod installs `git` and `openssh` from the Alpine mirror at start (pinned to
  the upstream series `GIT_SERVER_GIT_SERIES` and `GIT_SERVER_OPENSSH_SERIES` in `versions.env`) and retries eight times;
  check the network of the Docker VM. When Alpine moves the 3.22 branch to a new upstream series, update the two pins.
* **A port-forward port is busy.** Set `PROBE_PORT_API`, `PROBE_PORT_DEXAPI` and `PROBE_PORT_DEX`.
* **The NetworkPolicy stage is skipped with "CNI does not enforce NetworkPolicy".** kind's default CNI (kindnet) enforces
  policies since kind v0.24; the self-test in stage 13 proves it on every run. A skip means a different CNI or an old node image.
