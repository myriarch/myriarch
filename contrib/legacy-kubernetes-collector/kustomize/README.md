# Collector + supervisor in Kubernetes (deprecated)

> Deprecated since v0.2.0: use the Helm chart `deploy/helm/myriarch-collector`. See [the README one level up](../README.md).

The OpAMP supervisor and `otelcol-contrib` run in the **same container**; every pod or node is its own OpAMP agent with
full functionality (remote configuration with rollback, health, effective configuration, component list, restart).

| File | Contents |
|---|---|
| `Dockerfile` | Image built from the supervisor image + the contrib binary (same version!) |
| `supervisor-configmap.yaml` | `supervisor.yaml` and `fallback.yaml` (identical to `contrib/legacy-kubernetes-collector/supervisor/`) |
| `site-configmap.yaml` | Cluster-specific values (OpAMP endpoint, cluster name, country, time zone) |
| `secret.example.yaml` | Examples for the enrollment token and collector secrets (do not commit) |
| `gateway.yaml` | StatefulSet `otel-gateway` with a PVC (stable identity) + Services (OTLP 4317/4318) |
| `daemonset.yaml` | Node agent `otel-agent` with hostPath storage |
| `networkpolicy.yaml` | NetworkPolicies: OTLP only from the namespace and from namespaces labeled `myriarch.dev/otlp-client=true` |
| `kustomization.yaml` | `kubectl apply -k`, image tag bump through `images:` |

## Image without a container runtime

`contrib/legacy-kubernetes-collector/build-collector-image-crane.sh <version>` builds the same image with crane
from the two upstream images (pinned by digest, versions checked) and pushes it; the image contract (user, directories
with group 0, ENTRYPOINT/CMD) matches the `Dockerfile` here: change both together.
Details: `contrib/legacy-kubernetes-collector/build-collector-image-crane.sh help` (IMAGE_REPO is required).

## Container mode (myriarch-updater in the pod)

With `WITH_UPDATER=1` (the default of the crane script) the image additionally contains `/usr/local/bin/myriarch-updater`.
**ENTRYPOINT/CMD remain the supervisor** (older manifests run unchanged); container mode is switched on in the manifest
with `command`. The **myriarch-updater** then replaces the image tag path: the same installer, the same verification
(SHA-256 + Sigstore bundle), the same health check and rollback as on Windows hosts and Linux servers, only without
systemd.

```yaml
command: ["/usr/local/bin/myriarch-updater", "container", "--config=/etc/myriarch/updater.yaml"]   # PID 1
# Volumes: PVC (collector binaries + updater state) at /var/lib/myriarch, ConfigMap with trusted_root.json at /etc/myriarch/trust,
#          updater.yaml (template: contrib/legacy-kubernetes-collector/updater/updater.yaml), supervisor storage as before
# Probes:  startup/liveness GET :13135/healthz (myriarch-updater), readiness GET :13133/ (collector)
```

| Topic | Definition |
|---|---|
| Process model | myriarch-updater is PID 1, starts the supervisor as a child process in its own process group, reaps all zombies and forwards SIGHUP/SIGUSR1/SIGUSR2 to the child; SIGTERM/SIGINT first let a running installation finish (critical section), then end the child group (SIGTERM, SIGKILL after `stopTimeout`), exit 0 |
| Supervisor config | `agent.executable: /var/lib/myriarch/collector/current/otelcol-contrib` (symlink to `versions/<v>`) |
| Binary storage | PVC `otel-gateway-collector` (separate, not the supervisor PVC with the instance UID): two versions of about 400 MB each + staging + archive, 3 Gi |
| Image vs. PVC precedence | At startup the version of the image binary (`seedBinary --version`) is compared with the active one on the PVC (`seedPolicy: newest`): the higher one wins, a tie keeps the PVC (so a pod restart after a UI update stays on the new version), a higher image (image bump) is activated (a predecessor is an allowed step back), a missing or modified active binary falls back to the image. `seedPolicy: image` always activates the image binary (emergency switch). The image binary is copied only when it is activated |
| Failure case | If the supervisor crashes unexpectedly: restart with backoff (`restartDelay` doubles up to 60 s); more than `maxRestarts` within `restartWindow` -> exit 1 (the kubelet restarts the container, the crash loop is visible). A stop requested by the installer does not trigger a restart |
| Resources | four processes share the limit (myriarch-updater, supervisor, collector, and during verification a second collector for `validate`): gateway 768 Mi memory, `memory_limiter` at about 60 % |
| Limits | one replica per PVC (RWO; scaling needs a claim template), the supervisor version stays that of the image (only the collector binary is swapped; keep the supervisor/collector version skew within one minor version), Linux and macOS (development) only |

## Installation

```bash
# Build context = repository root (myriarch-updater is built as well)
docker build -f contrib/legacy-kubernetes-collector/kustomize/Dockerfile --build-arg OTELCOL_VERSION=0.162.0 -t <registry>/observability/otel-supervisor-collector:0.162.0 .
docker push <registry>/observability/otel-supervisor-collector:0.162.0

kubectl apply -f namespace.yaml
kubectl -n observability create secret generic myriarch-enrollment --from-literal=token=myre_...
kubectl -n observability create secret generic otel-collector-secrets --from-literal=BACKEND_API_TOKEN=...
# adjust site-configmap.yaml and image:, then
kubectl apply -k .
```

## Notes

* **Identity**: the instance UID is stored in `<storage>/persistent_state.yaml`. Without durable storage (emptyDir)
  every new pod gets a new UID and appears as a new agent. Hence a PVC (StatefulSet) or hostPath (DaemonSet). An
  `agent.instance_id` in `supervisor.yaml` would only take effect as long as no `persistent_state.yaml` exists yet.
* **Versions**: the supervisor cannot replace binaries itself (packages are not implemented upstream yet). A version
  change happens either through the image tag (GitOps/pipeline) - myriarchd triggers the pipeline with a webhook executor
  (see the `webhook` executor in [Executors](../../../docs/concepts/executors.md)) and
  recognizes completion by the newly reported `service.version`, in which case there is **no** myriarch-updater in the pod -
  or through **container mode** (see above): the myriarch-updater in the pod swaps the binary on the volume, triggered
  through the `updater` executor.
* **No Kubernetes API credential in the pod**: both manifests set `automountServiceAccountToken: false`, because the
  pod runs configuration from Git. A collector that needs the API (the `k8sattributes` processor, for example) gets its
  own ServiceAccount with a read-only Role (get, list and watch on pods and namespaces, and nodes where needed) and
  `serviceAccountName` plus `automountServiceAccountToken: true` on that pod template only; do not reuse a default or
  shared account. The DaemonSet init container changes the owner of the storage directory itself, not recursively.
* **Image arguments** of the Dockerfile default to the reviewed digests of `ci/versions.env`. For another version pass all
  three of `OTELCOL_VERSION`, `COLLECTOR_IMAGE` and `SUPERVISOR_IMAGE` (by digest).
* **Secrets** only through `${env:...}` (pod environment) or `${file:...}` in the collector configuration; the effective
  configuration shows `configopaque` values redacted.
* **Logs**: `agent.passthrough_logs: true` forwards the collector logs to stdout (container logging).
* **readOnlyRootFilesystem** is enabled; only the storage volume and `/tmp` are writable. This has to be verified in the
  cluster (receivers that write to further paths need additional volumes).
* **Two processes, one container**: a collector crash does not restart the container - the supervisor restarts it and
  reports `unhealthy`. The liveness probe therefore checks the supervisor, the readiness probe the collector
  (`health_check` extension from the blueprint or the fallback configuration).
* **Resources** (two processes): set the memory limit for both together; set `memory_limiter` in the blueprint
  configuration to about 75 % of the limit.
