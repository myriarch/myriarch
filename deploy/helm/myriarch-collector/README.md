# Helm chart `myriarch-collector`

Runs an OpenTelemetry Collector that the Myriarch server manages over OpAMP, as a **StatefulSet** (a gateway with a stable
identity per replica) or a **DaemonSet** (one collector per node). Every pod is its own OpAMP agent: remote configuration with
rollback, health, effective configuration, component list and restart work as on a host.

The chart runs the **unchanged upstream images**. Nothing is built from them:

| Container | Image | What it does |
|---|---|---|
| init `install-updater` | `ghcr.io/myriarch/myriarch-updater` | `myriarch-updater install-self /shared` copies its own static binary to a shared volume |
| init `seed-collector` | `otel/opentelemetry-collector-contrib` | `/shared/myriarch-updater copy /otelcol-contrib /shared/` copies the collector binary (the image has no shell and no `cp`) |
| `collector` | `otel/opentelemetry-collector-opampsupervisor` | the OpAMP Supervisor starts the collector from the shared volume |

The pod has no fixed user ID, adds no capability, has a read-only root file system and runs under the Pod Security level
`restricted` (StatefulSet). The documentation for operators is in
[Install the collector with Helm](../../../docs/operations/install-collector-helm.md); the design of the optional container mode is in
[docs/design/container-mode.md](../../../docs/design/container-mode.md).

> **Available from v0.2.0.** The values are validated against a strict schema, [`values.schema.json`](values.schema.json): `helm
> install`, `upgrade`, `template` and `lint` refuse a key that the chart does not know, a value of the wrong type and a malformed
> value, before anything is rendered.

## Minimal installation

Two things are needed: a Secret with the enrollment token and the `wss://` address of the server.

```bash
kubectl create namespace observability
kubectl -n observability create secret generic myriarch-enrollment --from-literal=token='myre_...'
# from a checkout of the repository, or, from v0.2.0, from the signed OCI chart (verify it first, see the operations guide):
helm install otel-gateway deploy/helm/myriarch-collector -n observability \
  --set server.endpoint=wss://fleet.example.com/v1/opamp --set site.id=k8s-de-prod --set site.name="Regional gateway DE"
helm install otel-gateway oci://ghcr.io/myriarch/charts/myriarch-collector --version 0.2.0 -n observability \
  --set server.endpoint=wss://fleet.example.com/v1/opamp
```

A node agent:

```bash
helm install otel-agent deploy/helm/myriarch-collector -n observability --set mode=daemonset \
  --set server.endpoint=wss://fleet.example.com/v1/opamp --set-json 'tolerations=[{"operator":"Exists"}]'
```

The chart renders without the namespace; it uses the namespace of the release.

## Modes

| | `mode: statefulset` (default) | `mode: daemonset` |
|---|---|---|
| Workload | one pod per replica (`replicaCount`) | one pod per node |
| Identity (instance UID) | a PVC per replica (`persistence.size`) | an `emptyDir` (a restarted pod is a new agent), or a `hostPath` with `persistence.hostPath.enabled` |
| Services | a headless Service and, with `service.enabled`, one for `ports` (OTLP 4317 and 4318) | none |
| `site.type` default | `k8s-gateway` | `k8s-node` |
| Node logs | no | `nodeLogs.enabled`: read-only `hostPath` |
| Container mode | `containerMode.enabled` | not available (the volume of the binaries needs a claim per replica) |

The configuration of the collector is not part of the chart: it comes from the Myriarch server (a fleet that matches `site.type`).
Until it arrives, a fallback configuration with the `health_check` extension runs.

## Pod Security

* **No fixed user ID.** `runAsNonRoot: true` makes the kubelet check that the user of the image is numeric and not 0 (all three
  images are); the pod sets neither `runAsUser` nor `runAsGroup`. The OpenShift profile leaves the user, group and `fsGroup` to the SCC.
* **`fsGroup`** is a group ID, not a user ID: the default `10001` is the group of the user of the supervisor image and makes the PVC
  writable for it. The OpenShift profile drops it; set `podSecurityContext.fsGroup` to `null` for a storage class that needs none.
* **Every container:** `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `readOnlyRootFilesystem: true`,
  `runAsNonRoot: true`; the pod has `seccompProfile: RuntimeDefault`. No Kubernetes API token is mounted
  (`serviceAccount.automountToken: false`).
* **Writable paths:** the storage volume, an `emptyDir` for `/tmp` and, in container mode, the `binaries` volume. The `shared`
  volume is writable for the init containers and read-only for the main container.
* **The only `hostPath` volumes** exist in the DaemonSet and only when you ask for them: `nodeLogs.enabled` (read-only, for a
  `filelog` receiver) and `persistence.hostPath.enabled` (the identity of a node agent). The Pod Security levels `baseline` and
  `restricted` refuse a `hostPath` volume: label the namespace `privileged`, or use an exception of your admission policy. The
  chart has **no root init container**: prepare the directory of `persistence.hostPath` on the nodes, writable for the user of the
  supervisor image (UID 10001); the pod log files must be readable for that user. containerd creates them as `0640 root:root` and CRI-O as `0600`, so neither "others" nor
  the user's group can read them: with containerd set `podSecurityContext.supplementalGroups: [0]` (a group ID, not a user ID; the OpenShift
  profile removes `supplementalGroups`, so grant the group through the SCC there); with CRI-O that is not enough.
* **NetworkPolicy** (`networkPolicy.enabled`): the ports of the receivers are reachable from the release namespace and from the
  namespaces that carry the label `myriarch.dev/otlp-client=true` (and the sources in `networkPolicy.ingress.from`); the health
  ports only from the release namespace. Egress stays open, because the server, the backends and DNS differ per environment.
* **RBAC:** the chart creates no Role. A collector that needs the Kubernetes API (the `k8sattributes` processor) needs a
  ServiceAccount with a read-only Role that you create; set `serviceAccount.create=false`, `serviceAccount.name` and
  `serviceAccount.automountToken=true`.

## OpenShift

`openshift.enabled=true` (or `auto`) selects the OpenShift profile: no `runAsUser`, `runAsGroup` or `fsGroup`, so the SCC
`restricted-v2` assigns them from the range of the project. The StatefulSet runs under `restricted-v2` without further grants. The
DaemonSet with `hostPath` volumes needs an SCC that allows them for its ServiceAccount (and the files of the node logs must be
readable under the SELinux context of the pod); the chart does not create a binding.

## Container mode

`containerMode.enabled=true` makes `myriarch-updater` PID 1 of the main container. It starts the supervisor as a child, swaps the
collector binary on a second volume (`volumeClaimTemplate` `binaries`) when the server asks for a new version, and rolls back on a
failed health check. The image then no longer says which collector runs. Probes use the updater on port 13135 (`/healthz`,
`/readyz`), and `terminationGracePeriodSeconds` is at least 360 seconds. The verification of the downloaded collector needs the
Sigstore trusted root in a ConfigMap (`containerMode.trustedRoot.existingConfigMap`, created from `myriarch-updater
fetch-trusted-root`); switching the verification off is an explicit `containerMode.update.requireSignature=false`.

## Values

The values are validated against [`values.schema.json`](values.schema.json). The table is generated by `make docs-gen` from the
schema and `values.yaml`; edit `values.schema.source.yaml`, never the schema or the table. Defaults and long explanations are in
[`values.yaml`](values.yaml).

<!-- BEGIN GENERATED VALUES TABLE (make docs-gen; do not edit) -->

| Value | Type | Default | Description |
|---|---|---|---|
| `mode` | enum | `"statefulset"` | statefulset - a gateway with a stable identity per replica (a PVC). daemonset - one collector per node. |
| `replicaCount` | integer | `1` | Number of replicas of the StatefulSet. Every replica is its own OpAMP agent. The DaemonSet ignores it. |
| `images.updater.registry` | string | `"ghcr.io"` | Registry host, with an optional port, of the image, for example ghcr.io. It applies when repository has no registry host of its own, and global.imageRegistry takes precedence over it. Empty means the default registry of the container runtime. |
| `images.updater.repository` | string | `"myriarch/myriarch-updater"` | Repository of the image without a registry host, for example myriarch/myriarch-updater. A repository that starts with a registry host is used unchanged and neither registry nor global.imageRegistry applies. |
| `images.updater.tag` | string | `""` | Image tag. Empty means the appVersion of the chart. |
| `images.updater.digest` | string | `""` | Image digest, sha256: followed by 64 hexadecimal characters. When set, the image is referenced as repository@digest and the tag is ignored. |
| `images.updater.pullPolicy` | enum | `"IfNotPresent"` | Image pull policy. |
| `images.collector.registry` | string | `"docker.io"` | Registry host, with an optional port, of the image, for example ghcr.io. It applies when repository has no registry host of its own, and global.imageRegistry takes precedence over it. Empty means the default registry of the container runtime. |
| `images.collector.repository` | string | `"otel/opentelemetry-collector-contrib"` | Repository of the image without a registry host, for example myriarch/myriarch-updater. A repository that starts with a registry host is used unchanged and neither registry nor global.imageRegistry applies. |
| `images.collector.tag` | string | `"0.162.0"` | Image tag. Empty means the appVersion of the chart. |
| `images.collector.digest` | string | see `values.yaml` | Image digest, sha256: followed by 64 hexadecimal characters. When set, the image is referenced as repository@digest and the tag is ignored. |
| `images.collector.pullPolicy` | enum | `"IfNotPresent"` | Image pull policy. |
| `images.supervisor.registry` | string | `"docker.io"` | Registry host, with an optional port, of the image, for example ghcr.io. It applies when repository has no registry host of its own, and global.imageRegistry takes precedence over it. Empty means the default registry of the container runtime. |
| `images.supervisor.repository` | string | `"otel/opentelemetry-collector-opampsupervisor"` | Repository of the image without a registry host, for example myriarch/myriarch-updater. A repository that starts with a registry host is used unchanged and neither registry nor global.imageRegistry applies. |
| `images.supervisor.tag` | string | `"0.162.0"` | Image tag. Empty means the appVersion of the chart. |
| `images.supervisor.digest` | string | see `values.yaml` | Image digest, sha256: followed by 64 hexadecimal characters. When set, the image is referenced as repository@digest and the tag is ignored. |
| `images.supervisor.pullPolicy` | enum | `"IfNotPresent"` | Image pull policy. |
| `imagePullSecrets` | array | `[]` | Secrets for pulling the images from a private registry. |
| `nameOverride` | string | `""` | Overrides the chart name in resource names. |
| `fullnameOverride` | string | `""` | Overrides the complete resource name. |
| `global.imageRegistry` | string or null | unset | Registry host, with an optional port, that replaces images.*.registry, for example the host of a mirror. A repository that has a registry host of its own is not affected. A parent chart may pass null for no mirror. |
| `enabled` | boolean | unset | Read by the dependency condition of a parent chart, not by this chart, which ignores it. It has no default and no effect on a standalone installation. |
| `serviceAccount.create` | boolean | `true` | Create the ServiceAccount. |
| `serviceAccount.name` | string | `""` | Name of the ServiceAccount. Empty means a name generated from the release name. |
| `serviceAccount.annotations` | object | `{}` | Annotations of the ServiceAccount. |
| `serviceAccount.automountToken` | boolean | `false` | Mount the API token into the pod. Off by default: the pod runs configuration from Git. Turn it on only with a dedicated ServiceAccount whose Role is read-only, for a collector that needs the Kubernetes API. |
| `server.endpoint` | string | `"wss://fleet.example.com/v1/opamp"` | OpAMP endpoint of the Myriarch server. wss:// is the default; ws:// is refused unless allowInsecureTransport is true. |
| `server.allowInsecureTransport` | boolean | `false` | Explicit opt-in to an unencrypted ws:// endpoint. The enrollment token then travels in clear text. |
| `server.tls.existingSecret` | string | `""` | Name of an existing Secret with the CA bundle. Empty means none. Not together with existingConfigMap. |
| `server.tls.existingConfigMap` | string | `""` | Name of an existing ConfigMap with the CA bundle. Empty means none. Not together with existingSecret. |
| `server.tls.key` | string | `"ca.pem"` | Key of the PEM bundle in the Secret or ConfigMap. |
| `enrollment.existingSecret` | string | `"myriarch-enrollment"` | Name of the Secret in the release namespace. |
| `enrollment.key` | string | `"token"` | Key of the token in the Secret. |
| `site.type` | string | `""` | site.type. Empty means k8s-gateway for the StatefulSet and k8s-node for the DaemonSet. |
| `site.id` | string | `""` | site.id. |
| `site.name` | string | `""` | site.name. |
| `site.country` | string | `""` | site.country. |
| `site.region` | string | `""` | site.region. |
| `site.timezone` | string | `""` | site.timezone. |
| `site.environment` | string | `""` | deployment.environment.name. |
| `site.clusterName` | string | `""` | k8s.cluster.name. |
| `extraEnv` | array | `[]` | Additional environment variables of the main container, in the format of a Kubernetes container. The collector inherits the environment of the supervisor. |
| `extraEnvFrom` | array | `[]` | Additional environment sources (Secrets, ConfigMaps) of the main container, in the format of a Kubernetes container. |
| `ports` | array | see `values.yaml` | The ports of the receivers of the collector configuration: declared on the container, offered by the Service of the StatefulSet and allowed by the NetworkPolicy. The health ports are always declared. |
| `service.enabled` | boolean | `true` | Create the Service. |
| `service.type` | enum | `"ClusterIP"` | Service type. |
| `service.annotations` | object | `{}` | Annotations of the Service. |
| `persistence.size` | number or string | `"1Gi"` | StatefulSet - size of the PVC "storage" of every replica. |
| `persistence.storageClass` | string | `""` | StatefulSet - storage class of the PVC. Empty means the default class. |
| `persistence.accessModes` | array | `["ReadWriteOnce"]` | StatefulSet - access modes of the PVC. |
| `persistence.hostPath.enabled` | boolean | `false` | Use a hostPath volume for the storage directory. |
| `persistence.hostPath.path` | string | `""` | Absolute directory on the node. Empty means /var/lib/myriarch/\<fullname>. |
| `persistence.hostPath.type` | enum | `"DirectoryOrCreate"` | Type of the hostPath volume. |
| `nodeLogs.enabled` | boolean | `false` | Mount the logs of the node read-only. |
| `nodeLogs.hostPath` | string | `"/var/log/pods"` | Absolute directory on the node. |
| `nodeLogs.mountPath` | string | `"/var/log/pods"` | Absolute mount path in the container. |
| `containerMode.enabled` | boolean | `false` | Run the updater as PID 1 of the main container. |
| `containerMode.binaries.size` | number or string | `"3Gi"` | Size of the PVC "binaries". |
| `containerMode.binaries.storageClass` | string | `""` | Storage class of the PVC. Empty means the default class. |
| `containerMode.seedPolicy` | enum | `"newest"` | newest - the higher version wins, a tie keeps the volume. image - the binary of the image is always activated (emergency switch). |
| `containerMode.keepVersions` | integer | `2` | Versions of the collector kept on the volume, at least 2. |
| `containerMode.update.requireSignature` | boolean | `true` | Verify the Sigstore bundle of a downloaded collector. Needs trustedRoot.existingConfigMap. |
| `containerMode.update.healthTimeout` | string | `"120s"` | Time to wait for the health check after a swap. |
| `containerMode.trustedRoot.existingConfigMap` | string | `""` | Name of an existing ConfigMap with the trusted root. Empty means none. |
| `containerMode.trustedRoot.key` | string | `"trusted_root.json"` | Key of the trusted root in the ConfigMap. |
| `containerMode.stopTimeout` | string | `"30s"` | Time from SIGTERM to SIGKILL for the supervisor. |
| `resources.requests` | object | `{"cpu":"100m","memory":"256Mi"}` | Quantities by resource name, for example cpu, memory and ephemeral-storage. |
| `resources.limits` | object | `{"memory":"512Mi"}` | Quantities by resource name, for example cpu, memory and ephemeral-storage. |
| `initResources.requests` | object | `{"cpu":"10m","memory":"32Mi"}` | Quantities by resource name, for example cpu, memory and ephemeral-storage. |
| `initResources.limits` | object | `{"memory":"256Mi"}` | Quantities by resource name, for example cpu, memory and ephemeral-storage. |
| `terminationGracePeriodSeconds` | integer | `45` | Grace period of the pod on termination, in seconds. In container mode the pod gets at least 360. |
| `podSecurityContext.runAsNonRoot` | boolean | `true` |  |
| `podSecurityContext.runAsUser` | integer | unset |  |
| `podSecurityContext.runAsGroup` | integer | unset |  |
| `podSecurityContext.fsGroup` | integer | `10001` |  |
| `podSecurityContext.fsGroupChangePolicy` | enum | `"OnRootMismatch"` |  |
| `podSecurityContext.seccompProfile.type` | enum | `"RuntimeDefault"` |  |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` |  |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` |  |
| `securityContext.privileged` | boolean | unset |  |
| `securityContext.runAsNonRoot` | boolean | `true` |  |
| `securityContext.runAsUser` | integer | unset |  |
| `securityContext.runAsGroup` | integer | unset |  |
| `securityContext.capabilities.add` | array | unset |  |
| `securityContext.capabilities.drop` | array | `["ALL"]` |  |
| `securityContext.seccompProfile.type` | enum | unset |  |
| `tmpSizeLimit` | number or string | `"256Mi"` | Size limit of the emptyDir for /tmp. |
| `sharedSizeLimit` | number or string | `"1Gi"` | Size limit of the emptyDir "shared" that holds the updater and the seeded collector binary. |
| `tolerations` | array | `[]` | Tolerations of the pods. |
| `nodeSelector` | object | `{}` | Node selector of the pods. |
| `affinity.nodeAffinity` | object | unset |  |
| `affinity.podAffinity` | object | unset |  |
| `affinity.podAntiAffinity` | object | unset |  |
| `topologySpreadConstraints` | array | `[]` | Spreads the replicas, in the format of a Kubernetes pod. The chart adds the label selector of the release to every entry. |
| `priorityClassName` | string | `""` | Priority class of the pods. |
| `podAnnotations` | object | `{}` | Annotations of the pods. |
| `podLabels` | object | `{}` | Additional labels of the pods. |
| `updateStrategy.maxUnavailable` | integer or string | `"10%"` | Maximum number or percentage of unavailable pods during a rolling update. |
| `extraVolumes` | array | `[]` | Additional volumes of the pod, in the format of a Kubernetes pod. |
| `extraVolumeMounts` | array | `[]` | Additional volume mounts of the main container, in the format of a Kubernetes container. |
| `openshift.enabled` | enum | `false` | true, false or auto. |
| `networkPolicy.enabled` | boolean | `true` | Create the NetworkPolicy. |
| `networkPolicy.ingress.allowSameNamespace` | boolean | `true` | Allow every pod of the release namespace to reach the ports. |
| `networkPolicy.ingress.from` | array | see `values.yaml` | Further sources of the ports in NetworkPolicy "from" format. |

<!-- END GENERATED VALUES TABLE -->

## Checks of the chart

`ci/lint-helm.sh` lints the chart (default values, both modes, container mode, the OpenShift profile) and packages it;
`ci/helm-conform.sh` renders it for several Kubernetes versions, validates every manifest with kubeconform and asserts the contract
above, including the guards that make rendering fail (`ws://` without `server.endpoint` opt-in, container mode on a DaemonSet, a
missing trusted root).
