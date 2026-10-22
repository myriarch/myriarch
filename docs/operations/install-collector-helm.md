---
title: "Install the collector with Helm"
weight: 45
description: "Run OpenTelemetry Collectors that Myriarch manages on Kubernetes and OpenShift with the Helm chart myriarch-collector: StatefulSet or DaemonSet, unchanged upstream images, Pod Security and container mode."
---

This guide runs collectors on Kubernetes with the Helm chart `deploy/helm/myriarch-collector`. Every pod is its own OpAMP agent
of your Myriarch server: remote configuration with rollback, health, effective configuration, component list and restart work
as on a host. The chart runs the **unchanged upstream images**: the OpAMP Supervisor image of the OpenTelemetry project as the
main container, and the upstream collector (contrib) image to seed the collector binary. The project builds no collector image.

> **Support level.** The collector chart is new in v0.2.0 and experimental, like the server chart. The Kustomize base of v0.1.0
> (`deploy/k8s/collector`) and the combined collector image are deprecated; see [Move from the Kustomize base](#move-from-the-kustomize-base).

## How the pod works

The pod has two init containers and one main container. None of the images is modified.

| Container | Image | What it does |
|---|---|---|
| init `install-updater` | `ghcr.io/myriarch/myriarch-updater` | `myriarch-updater install-self /shared` copies the static updater binary to a shared volume |
| init `seed-collector` | `otel/opentelemetry-collector-contrib` | `/shared/myriarch-updater copy /otelcol-contrib /shared/` copies the collector binary; the image is `FROM scratch`, so the updater from the volume does the copy |
| `collector` | `otel/opentelemetry-collector-opampsupervisor` | the supervisor starts `/shared/otelcol-contrib` and connects to the server |

The two upstream images are pinned by tag and digest in `values.yaml`, equal to `ci/versions.env`. Supervisor and collector must
have the same version. Every image has a tag and an optional digest; with a digest the image is `repository@digest` and the tag
is ignored. The release workflow writes the digest of the signed updater image into the published chart.

## Prerequisites

- Kubernetes 1.30 or newer (`kubeVersion` of the chart), Helm, and a Myriarch server that the pods can reach over `wss://`.
- An enrollment token for the collectors ([create one in the UI](../tasks/connect-a-host.md#1-create-an-enrollment-token), or
  declare it with `enrollmentTokens` of the server chart). A token is never a chart value: the chart reads it from a Secret that
  you create.
- Optional: a CA bundle, when the server certificate comes from a private CA.

## Install a gateway

```bash
kubectl create namespace observability
kubectl -n observability create secret generic myriarch-enrollment --from-literal=token='myre_...'

helm install otel-gateway deploy/helm/myriarch-collector -n observability \
  --set server.endpoint=wss://fleet.example.com/v1/opamp \
  --set site.id=k8s-de-prod --set site.name="Regional gateway DE" --set replicaCount=2
```

From v0.2.0 the chart is also a signed OCI artifact. Verify it before you install it (the identity is the one of the release
files, see [Verify a release](../security/verify-releases.md)):

```bash
VERSION=0.2.0                     # the release you install
CHART_DIGEST="$(crane digest "ghcr.io/myriarch/charts/myriarch-collector:${VERSION}")"
cosign verify "ghcr.io/myriarch/charts/myriarch-collector@${CHART_DIGEST}" \
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
helm install otel-gateway oci://ghcr.io/myriarch/charts/myriarch-collector --version "${VERSION}" -n observability \
  --set server.endpoint=wss://fleet.example.com/v1/opamp
```

The StatefulSet keeps one PVC per replica for the instance UID of the collector, offers the OTLP ports 4317 and 4318 through a
Service and runs with `site.type=k8s-gateway`. The collector configuration comes from the server: create a fleet that matches
`site.type=k8s-gateway` (the `site.*` values are the attributes the fleets match on). Until it arrives a fallback configuration
with the health check runs, so the pod becomes ready.

## Install a node agent

```bash
helm install otel-agent deploy/helm/myriarch-collector -n observability --set mode=daemonset \
  --set server.endpoint=wss://fleet.example.com/v1/opamp --set-json 'tolerations=[{"operator":"Exists"}]'
```

The DaemonSet runs one collector per node with `site.type=k8s-node`. Without `persistence.hostPath` its identity is an
`emptyDir`: a restarted pod is a new agent in the UI. Two options need a `hostPath` volume (see the next section):

- `persistence.hostPath.enabled=true` keeps the instance UID on the node. The directory (default
  `/var/lib/myriarch/<fullname>`) must be writable by the user of the supervisor image (UID 10001), because the chart has no root
  init container that changes the owner: create it on the nodes, for example with your node provisioning.
- `nodeLogs.enabled=true` mounts the logs of the node (default `/var/log/pods`) **read-only** for a `filelog` receiver.

The examples [`gateway.yaml`](../../deploy/helm/myriarch-collector/examples/gateway.yaml),
[`node-agent.yaml`](../../deploy/helm/myriarch-collector/examples/node-agent.yaml),
[`container-mode.yaml`](../../deploy/helm/myriarch-collector/examples/container-mode.yaml) and
[`openshift.yaml`](../../deploy/helm/myriarch-collector/examples/openshift.yaml) are rendered and validated in CI.

## The connection to the server

`server.endpoint` is a `wss://` address and has no insecure default. A `ws://` address is refused when the chart renders, unless
you set `server.allowInsecureTransport=true` on purpose (for example the in-cluster Service of a server in the same namespace
on a network that you trust); the enrollment token then travels in clear text. For a server with a private CA name a Secret or a
ConfigMap with the bundle (`server.tls.existingSecret` or `server.tls.existingConfigMap`, not both, and `server.tls.key`); the
supervisor gets `ca_file` and the bundle is mounted read-only.

Collector secrets (a backend token that the collector configuration reads with `${env:NAME}`) go in `extraEnv` or `extraEnvFrom`
with `secretKeyRef` or `secretRef`; the collector inherits the environment of the supervisor.

## Pod Security

The StatefulSet runs under the Pod Security level **`restricted`**; this is what the chart renders:

| Setting | Value |
|---|---|
| user ID | none fixed: `runAsNonRoot: true`, and no `runAsUser` or `runAsGroup` (all three images have a numeric user) |
| `fsGroup` | 10001 in the standard profile (a group ID, so that the PVC is writable for the supervisor user); dropped in the OpenShift profile |
| capabilities | `drop: [ALL]`, nothing added, never privileged |
| privilege escalation | `allowPrivilegeEscalation: false` |
| root file system | `readOnlyRootFilesystem: true`; writable are the storage volume, `/tmp` and, in container mode, the `binaries` volume |
| seccomp | `RuntimeDefault` |
| API token | not mounted (`serviceAccount.automountToken: false`) |

**The one exception** is a `hostPath` volume in DaemonSet mode, and only on request: `nodeLogs.enabled` (read-only) and
`persistence.hostPath.enabled`. The levels `baseline` and `restricted` refuse a `hostPath` volume, so the namespace of a node agent
with these options needs the level `privileged` (or an exception of your admission policy); everything else in the pod stays as
above. Keep the node agent in a namespace of its own.

The chart creates **no Role** and mounts no API token. A collector that needs the Kubernetes API (the `k8sattributes` processor,
the `kubeletstats` receiver) needs a ServiceAccount with a read-only Role that you create: set `serviceAccount.create=false`,
`serviceAccount.name` and `serviceAccount.automountToken=true`, and do not reuse a shared account.

The **NetworkPolicy** (`networkPolicy.enabled`, on by default) restricts the ingress: the receiver ports are reachable from the
release namespace and from namespaces that carry the label `myriarch.dev/otlp-client=true` (and the sources of
`networkPolicy.ingress.from`), and the health ports (13133, 13134 and, in container mode, 13135) only from the release namespace.
Egress stays open, because the server, the backends and DNS differ per environment; add a policy of your own if you restrict it.
The cluster needs a CNI that enforces NetworkPolicies.

## OpenShift

`openshift.enabled=true` renders the OpenShift profile (`auto` looks for the API `security.openshift.io/v1`, and `helm template`
needs `--api-versions security.openshift.io/v1` for it): the pod has no `runAsUser`, `runAsGroup` or `fsGroup`, and the SCC
`restricted-v2` assigns them from the range of the project. The StatefulSet runs under `restricted-v2` without further grants. A
DaemonSet with a `hostPath` volume needs an SCC that allows host paths for its ServiceAccount (the chart creates no binding), and
the log files must be readable under the SELinux context of the pod.

## Container mode

By default a version change of the collector is a change of the image reference (`helm upgrade --set images.collector.tag=...
--set images.supervisor.tag=...`, or the digests), which fits a GitOps flow. Where that is not possible (air-gapped or edge
clusters), `containerMode.enabled=true` makes `myriarch-updater` PID 1 of the main container. It starts the supervisor as a child
process and swaps the collector binary on a persistent volume when the server asks for a new version (the `updater` executor),
with the same verification, health check and rollback as on a host. The image reference then no longer says which collector runs
([ADR-0026](../adr/0026-collector-version-updates-on-kubernetes.md)). The design is in
[Container mode](../design/container-mode.md).

What the chart changes in container mode: a second `volumeClaimTemplate` `binaries` (`containerMode.binaries.size`, 3 Gi by
default) for the versions of the collector and the state of the updater; probes on port 13135 (`/healthz` for startup and
liveness, `/readyz` for readiness); and `terminationGracePeriodSeconds` of at least 360 seconds, so that a running installation can
finish. The verification of the downloaded collector uses the Sigstore trusted root offline: create a ConfigMap with
`trusted_root.json` (`myriarch-updater fetch-trusted-root`) and name it in `containerMode.trustedRoot.existingConfigMap`.
Rendering fails without it, unless you switch the verification off on purpose with `containerMode.update.requireSignature=false`.
Container mode is a StatefulSet feature: rendering fails for `mode=daemonset`.

## Move from the Kustomize base

The Kustomize base `deploy/k8s/collector`, the combined collector image (`Dockerfile`, `make collector-image-crane`) and the
templates under `deploy/supervisor/k8s` and `deploy/updater/container` moved to
[`contrib/legacy-kubernetes-collector`](../../contrib/legacy-kubernetes-collector/README.md). They are deprecated, receive no
support and will be removed. To move over:

1. Keep the Secret with the enrollment token (the chart default is `myriarch-enrollment`, key `token`, like the base).
2. Put the values of the old site ConfigMap into `site.*` and `server.endpoint`.
3. Install the chart next to the old workload with another name, check that the agents appear, and delete the old workload. The
   PVCs of the old StatefulSet are not reused: the new pods are new agents.

## Check the installation

```bash
kubectl -n observability get pods -l app.kubernetes.io/instance=otel-gateway
kubectl -n observability logs otel-gateway-myriarch-collector-0 -c install-updater
kubectl -n observability logs otel-gateway-myriarch-collector-0 -c seed-collector
kubectl -n observability logs otel-gateway-myriarch-collector-0 -c collector
```

| Symptom | Cause |
|---|---|
| `helm install` refuses a key | the values schema is strict: the key is misspelled or does not exist |
| `ws:// sends the enrollment token in clear text` | use a `wss://` endpoint, or accept the risk with `server.allowInsecureTransport=true` |
| the pod stays `Init:Error` at `seed-collector` | the two upstream images differ in architecture from the node, or the `shared` volume is too small (`sharedSizeLimit`) |
| the collector cannot write its storage | the PVC or the `hostPath` directory is not writable for the supervisor user; check `fsGroup` and the storage class |
| the agent appears with a new instance UID after every restart | the DaemonSet has no `persistence.hostPath`, or the volume is an `emptyDir` |
| a node agent is `Pending` or refused by admission | the `hostPath` volumes of the node agent need a `privileged` namespace or an SCC |
