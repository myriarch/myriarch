# Helm chart `myriarch`

Runs myriarchd (the myriarch server) with high availability: `replicas: 2` (all replicas serve agents and the API; the
singleton jobs run on the leader elected through a PostgreSQL advisory lock), a Service (8080 HTTP/UI/REST/OpAMP, 9090
metrics), a PodDisruptionBudget, a NetworkPolicy, a ConfigMap with `myriarchd.yaml`, and secrets mounted as files.
Access from outside the cluster is optional: Gateway API `HTTPRoute` (recommended), `Ingress`, or an OpenShift `Route`.

The chart installs with its defaults, never starts the dev mode and has a login without an enterprise identity provider
(the bootstrap admin). Single sign-on with any OpenID Connect provider is a configuration change:
[docs/operations/authentication.md](../../../docs/operations/authentication.md).

The values are validated against a strict schema, [`values.schema.json`](values.schema.json): `helm install`, `upgrade`,
`template` and `lint` refuse a key that the chart does not know, a value of the wrong type and a malformed value, before
anything is rendered. See [Values schema](#values-schema).

## Minimal installation

Two things are needed: a Secret with the database DSN and the external URL.

```bash
kubectl create namespace myriarch
kubectl -n myriarch create secret generic myriarch-secrets \
  --from-literal=database-dsn='postgres://myriarch:<password>@db.example.com:5432/myriarch?sslmode=verify-full'
# from a checkout of the repository (a signed OCI chart is planned for v0.2.0):
helm install myriarch deploy/helm/myriarch -n myriarch --set config.server.publicUrl=https://fleet.example.com
```

The documented release name and namespace are both `myriarch`, so the Service, Deployment, ConfigMap and ServiceAccount
are all called `myriarch`. A signed OCI chart is planned for v0.2.0; until then install from a checkout of the repository.

### Access

Without Ingress, HTTPRoute or Route nothing is exposed. Use a port-forward (the NetworkPolicy does not affect it):

```bash
kubectl -n myriarch port-forward svc/myriarch 8080:8080     # http://localhost:8080
```

For access from outside the cluster choose one:

| Path | Values | Notes |
|---|---|---|
| Gateway API (recommended) | `httpRoute.enabled`, `parentRefs`, `hostnames`, `tlsTerminatedAtGateway` | the chart creates the `HTTPRoute`, not the Gateway |
| Ingress | `ingress.enabled`, `className`, `annotations`, `tls` | ingress-nginx is no longer maintained since March 2026; examples for maintained controllers are in `examples/ingress-*.yaml` |
| OpenShift Route | `openshift.enabled=true` | Route with edge TLS, see "Profiles" |

Ingress and HTTPRoute need `config.server.trustedProxies` (CIDR of the controller or Gateway data plane) and
`networkPolicy.ingress.from` (their namespace) and `networkPolicy.ingress.allowSameNamespace=false`.

### First login

The default login is the bootstrap admin, a break-glass administrator that is configured declaratively
(`bootstrapAdmin`). The chart generates the Secret `myriarch-bootstrap` with a one-time token; the install notes print
the command to read it (the notes never print a secret):

```bash
kubectl -n myriarch get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d
```

Open the UI and paste the token into the bootstrap admin form (it sits behind "Use the bootstrap admin" when single
sign-on is offered as well). The token works for **one** sign-in. For a new one
delete the Secret and run `helm upgrade`. GitOps tools that render offline (`helm template`, Argo CD) cannot see existing
Secrets and would generate a new token on every render: use `bootstrapAdmin.existingSecret` there.

For a durable break-glass account use a bcrypt password hash in your own Secret:

```bash
printf '%s' "$PASSWORD" | myriarchd auth hash-password > password-hash       # 12 to 72 characters, bcrypt cost 12
kubectl -n myriarch create secret generic my-bootstrap --from-file=password-hash=password-hash
helm upgrade myriarch ... --set bootstrapAdmin.existingSecret=my-bootstrap \
  --set bootstrapAdmin.tokenKey= --set bootstrapAdmin.passwordHashKey=password-hash
```

The bootstrap admin is meant for the initial setup: configure `config.auth.oidc` and set `bootstrapAdmin.enabled=false`.
With OIDC enabled the chart does not mount its credentials (and the server deactivates the account) unless
`bootstrapAdmin.keepWithOidc=true`. Every sign-in is audited as `auth.bootstrap.login`.

**Restrict network access to the bootstrap login.** The account has an account-wide backoff: after five failures in a
row the server delays every further attempt, doubling the delay up to 15 minutes, whoever sends it. Anyone who can reach
`POST /api/v1/auth/bootstrap-login` can therefore keep the break-glass login in these delays with wrong credentials, and
the legitimate administrator gets `429` as well. Allow this path only from the networks of your administrators (a rule
for the path at the ingress controller, Gateway or WAF, or block it at the edge and use `kubectl port-forward` when the
break-glass account is needed). The counter is held in memory per replica, so restarting the pods clears it:
`kubectl -n myriarch rollout restart deployment/myriarch`. Details and the audit entries to watch:
[docs/operations/authentication.md](../../../docs/operations/authentication.md#27-rate-limit-and-backoff).

## Single sign-on with Dex (quickstart)

`examples/quickstart-dex.yaml` (values for this chart) and `examples/dex/dex.yaml` (values for the upstream Dex chart)
set up Dex as a demo identity provider with one static user:

```bash
helm upgrade --install dex dex/dex -n dex -f deploy/helm/myriarch/examples/dex/dex.yaml     # see the header of the file
helm upgrade --install myriarch deploy/helm/myriarch -n myriarch -f deploy/helm/myriarch/examples/quickstart-dex.yaml
```

The files contain placeholders only (`<bcrypt hash>`, `<client secret>`); replace them and never commit real values. For
Keycloak, Okta, Entra ID, Google, authentik and Zitadel see
[docs/operations/authentication.md](../../../docs/operations/authentication.md).

## Single sign-on switched on through the environment

The chart reads `config.auth.oidc.enabled` to decide whether a login path exists and whether the bootstrap admin is
active (its Secret is mounted only then). If your platform switches single sign-on on with environment variables instead
(`MYRIARCH_AUTH_OIDC_ENABLED=true` in `extraEnv`, or injected from a ConfigMap or a Secret), the chart cannot see it, and
with `bootstrapAdmin.enabled=false` it stops with `bootstrapAdmin.enabled: no login path is configured`. Declare the
environment with `oidcFromEnvironment`:

```yaml
oidcFromEnvironment: true        # the chart treats OIDC as enabled; the server reads the environment
bootstrapAdmin:
  enabled: false
extraEnv:
  - {name: MYRIARCH_AUTH_OIDC_ENABLED, value: "true"}
  - {name: MYRIARCH_AUTH_OIDC_CLIENTID, value: myriarch}
```

`oidcFromEnvironment` only declares what the chart assumes: no login path error, no bootstrap Secret unless
`bootstrapAdmin.keepWithOidc=true`, and the `config.auth.oidc.*File` fields stay in the rendered configuration (set an
empty value to drop one, for example `clientSecretFile` when the client secret comes from the environment). The server
reads the environment at runtime and still refuses to start when no login path remains.

## Declarative enrollment tokens

Tokens for agents that exist from the first start, without the UI (`enrollmentTokens`):

```yaml
enrollmentTokens:
  - name: gateway            # DNS label, unique; the file is /etc/myriarchd/enrollment/gateway
    labels: {site: hq}       # copied to the agents at registration
    expiresAt: ""            # RFC 3339, empty = no expiry
    # existingSecret: ""     # empty = the chart generates <fullname>-enrollment-<name> (myre_ + 40 characters)
    # key: token
```

The chart renders `opamp.enrollmentTokens` and mounts the Secrets as one projected volume. Agents in the cluster can
mount the same Secret. The server stores only the SHA-256 hash of each token. Generated values survive upgrades; the
GitOps caveat above applies.

## Prerequisites

1. **External PostgreSQL** (>= 14) with a database and a user. myriarchd runs migrations itself
   (`database.migrateOnStart`, protected by an advisory lock, so several replicas are safe).
   **No PgBouncer in transaction pooling mode**: LISTEN/NOTIFY and session advisory locks (leader election)
   need a direct connection or session pooling.
2. **Secret** (`existingSecret`, default `myriarch-secrets`) with at least `database-dsn`. Optional keys, depending on the
   configuration: `oidc-client-secret` (or `oidc-client-cert` and `oidc-client-key`), `download-token-secret`,
   `git-token`, `gitops-webhook-secret`, `github-token`, `mirror-token`, `mirror-password`, notification URLs. It is
   mounted completely at `/etc/myriarchd/secrets` (mode 0440); the configuration points to the files through its
   `*File` fields. Executor secrets (webhook/AWX tokens) are passed as environment variables through `secretEnv`.
3. **Access with WebSocket support and long timeouts** for the OpAMP connections (see "Access"). Session affinity is not
   needed.
4. **Kubernetes >= 1.30** (native `preStop` sleep action; the distroless image has no `sleep`; `Chart.yaml` enforces
   this through `kubeVersion`).

## Profiles: standard Kubernetes and OpenShift

`openshift.enabled` selects the profile:

| Value | Meaning |
|---|---|
| `false` (default) | standard profile: fixed user and group 65532 and `fsGroup` 65532 |
| `true` | OpenShift profile |
| `auto` | OpenShift profile when the cluster offers the API `security.openshift.io/v1` |

**Caveat for `auto`:** `helm template` has no cluster access and silently renders the *standard* profile unless you pass
`--api-versions security.openshift.io/v1`. GitOps tools that template offline need the same flag, or use `true`/`false`.

The OpenShift profile

* drops `runAsUser`, `runAsGroup` and `fsGroup` from the pod security context (restricted-v2 assigns them from the
  project range; `runAsNonRoot` and the seccomp profile stay; no `runAsUser` appears anywhere in the Deployment),
* creates a `Route` (`openshift.route.*`): edge TLS, the host of `openshift.route.host` or of `config.server.publicUrl`,
  router timeouts `haproxy.router.openshift.io/timeout` and `timeout-tunnel` (`openshift.route.timeout`, default `3600s`
  for the long-lived OpAMP WebSockets), `insecureEdgeTerminationPolicy`, optional `externalCertificateSecret`,
* adds the router namespaces (`openshift.routerNamespaceSelector`) as source of port 8080 and uses the OpenShift DNS
  rule (`openshift.dns`: namespace `openshift-dns`, ports 5353 and 53).

The Route does not require `trustedProxies`, but without it all clients appear with the address of the router. Set it to
the pod CIDR of the router and `networkPolicy.ingress.allowSameNamespace=false`. `examples/openshift.yaml` is an overlay of
the production example. Every container has requests and limits for cpu, memory and ephemeral-storage, as LimitRanges on
OpenShift usually demand.

## Network policy

`networkPolicy.enabled` (default) restricts the pods. A CNI with NetworkPolicy support is a prerequisite.

* **Port 8080** (UI, REST, OpAMP) is reachable from: the own namespace (`networkPolicy.ingress.allowSameNamespace`,
  default `true`; agents and collector gateways next to the pods), plus `networkPolicy.ingress.from`, plus the router
  namespaces in the OpenShift profile with a Route. An empty set means no rule for 8080 (denied); `kubectl port-forward`
  still works. **`allowSameNamespace` must be `false` while `config.server.trustedProxies` or
  `config.opamp.clientCertHeader` is set** (rendering fails otherwise): any pod of the namespace could forge the
  forwarded headers.
* **Port 9090** (`/metrics`, unauthenticated) only from `networkPolicy.metrics.from`; empty means nobody.
* **Egress** (`networkPolicy.egress.enabled`): DNS (`egress.dns`; the OpenShift profile uses `openshift.dns`, UDP and TCP
  per port), and one rule for TCP `egress.ports` (default 5432 PostgreSQL, 443 HTTPS) plus `egress.extraPorts` plus the Git
  SSH port (22, or the port of an `ssh://host:port` URL; for `ssh://` and scp-like `user@host:path` URLs while
  `config.gitops.enabled`), duplicates removed. Destinations are all addresses except `egress.exceptCidrs` (cloud metadata
  service) and every pod of the cluster (PostgreSQL or an IdP such as Dex inside the cluster). `egress.extraRules` are
  appended in NetworkPolicy format. In addition myriarchd checks every outbound connection against
  `config.outbound.allowedCidrs` itself.

## Storage, resources and secrets

* `emptyDir.sizeLimit` (default 4Gi) holds the GitOps clone and the artifact cache of each replica;
  `config.artifacts.maxCacheBytes` (default 3 GiB) must stay below it. `resources.limits.ephemeral-storage` (default 5Gi)
  must stay above `emptyDir.sizeLimit` plus the 256Mi `tmp` volume plus the container logs, otherwise the kubelet evicts
  the pod. With `persistence.enabled` a shared PVC (`persistence.size`, ReadWriteMany) is mounted instead.
* `replicaCount` defaults to 2. The PodDisruptionBudget is created only while `replicaCount` is greater than 1 (with one
  replica `minAvailable: 1` would block every node drain); `podDisruptionBudget.maxUnavailable` replaces `minAvailable` when
  it is set.
* `secretChecksum.enabled` (default) adds the annotation `checksum/secret` to the pods: the SHA-256 over the uid and
  resourceVersion of every Secret the pod mounts (`existingSecret`, the bootstrap Secret, the enrollment Secrets) as
  seen through `lookup`, so a changed Secret rolls the pods on `helm upgrade` (no digest of the secret data is stored in
  the Deployment). It is constant under `helm template`. The first upgrade after an
  installation with generated Secrets restarts the pods once. An upgrade that creates a generated Secret (a new
  enrollment token entry, or a generated Secret you deleted to rotate it) restarts the pods too: the checksum then
  contains the release revision, so even a second rotation rolls the pods (an enrollment token is reconciled at startup). Changes to `config:` roll the pods through
  `checksum/config`.
* Mounted files: `/etc/myriarchd/secrets/<key>` (`existingSecret`), `/etc/myriarchd/bootstrap/token` and
  `/etc/myriarchd/bootstrap/password-hash` (Secret `bootstrapAdmin.existingSecret` or the generated
  `<fullname>-bootstrap`), `/etc/myriarchd/enrollment/<name>` (one projected volume). All with mode 0440, read-only.
* `*File` fields of disabled features are not rendered: `auth.oidc.*File` while OIDC is off (`oidcFromEnvironment=true`
  counts as on), the gitops token, key, CA and webhook secret while gitops is off, `releases.tokenFile` while releases is
  off; empty `*File` values are dropped.
  `config.auth.downloadTokenSecretFile` is optional (empty = a random key stored in the database and shared by all
  replicas).
* `config.executors.allowedWebhookHosts` empty means fail closed: every webhook and AWX target is refused (myriarchd
  still starts). List the permitted hosts (`host`, `host:port`, `*.example.com`, `http://host` for plain HTTP) and bind
  each executor secret to its hosts in `config.executors.secretEnvHosts`.

## Operation

* Rolling update: `maxUnavailable: 0`, `preStop` 10 s, `terminationGracePeriodSeconds: 60`; agents reconnect with
  backoff through other replicas. Exception for the upgrade to migration `00003`: replicas of an older version can no
  longer store OIDC users afterwards; for this upgrade set `replicaCount=1` and scale up again afterwards.
* Security: non-root, `readOnlyRootFilesystem`, all capabilities dropped, seccomp `RuntimeDefault`; only
  `/var/lib/myriarchd` (GitOps clone, artifact cache) and `/tmp` are writable. The image creates `/var/lib/myriarchd`
  (0750, owned by 65532:65532) and the chart mounts the data volume there with `fsGroup` 65532. The chart always starts `myriarchd serve`;
  the dev mode and the dev login are not available (setting `config.dev` or `config.auth.devLogin` fails the rendering).
* `config.server.tls.terminatedUpstream` (default `true`) confirms to myriarchd that TLS ends at the ingress, Gateway or
  router (otherwise myriarchd does not start in production). `secretEnv` names must start with
  `executors.secretEnvPrefix` (`MYRIARCH_EXEC_`). `config.auth.sessionTtl` and `sessionMaxAge` limit sessions (2 h idle,
  12 h absolute).
* TLS: if `config.server.publicUrl` uses https, the Ingress needs `ingress.tls` (`ingress.allowWithoutTls=true` only when
  TLS terminates outside the ingress); an HTTPRoute needs `httpRoute.tlsTerminatedAtGateway=true`. The Route terminates TLS at
  the router and redirects plain HTTP requests (`openshift.route.tls.insecureEdgeTerminationPolicy: Redirect`); `Allow` serves
  plain HTTP at the router, and so `ws://`, and `None` serves none.
* Metrics: port `metrics` (9090); `serviceMonitor.enabled=true` creates a ServiceMonitor.
* Image: `image.registry` (default `ghcr.io`) and `image.repository` (default `myriarch/myriarch`) give the repository, and
  `image.tag` (default: `appVersion`) or, immutable, `image.digest` (`sha256:<64 hex>`; when set, the image is referenced as
  `<repository>@<digest>` and the tag is ignored) the version. Private registry: `imagePullSecrets`. See [Image](#image).
* Proxy and internal CA for outbound connections: `extraEnv` (`HTTPS_PROXY`, `NO_PROXY`) and `extraVolumes` /
  `extraVolumeMounts` for a CA ConfigMap, together with `config.outbound.caFile` and `config.outbound.allowedCidrs`.

## Image

The image is `<registry>/<repository>:<tag>`, or `<registry>/<repository>@<digest>` with `image.digest`:

| Value | Default | Meaning |
|---|---|---|
| `image.registry` | `ghcr.io` | Registry host with an optional port. It applies when the repository has no registry host of its own. Empty means the default registry of the container runtime. |
| `image.repository` | `myriarch/myriarch` | Repository without the registry host. |
| `image.tag` | empty | Empty means the `appVersion` of the chart. |
| `image.digest` | empty | `sha256:` and 64 hexadecimal characters. It replaces the tag. |
| `global.imageRegistry` | not set | Replaces `image.registry`, for example with the host of a mirror, also when a parent chart passes it. |

A repository that starts with a registry host is used **unchanged**, and neither `image.registry` nor `global.imageRegistry`
applies to it. The first path segment of the repository is a registry host when it contains a `.` or a `:` or is `localhost`.
This is the form of the values of v0.1.0 (`image.repository: registry.example.com/myriarch/myriarch`), which therefore render
the same image as before. A repository without a host now takes `image.registry`; if you pulled such an image through the
default registry of your container runtime, set `image.registry` to the empty string or put the host into the repository.

```bash
# the image of a mirror, pinned by digest
helm install myriarch ... --set image.registry=registry.example.com:5000 --set image.digest=sha256:<64 hex>
# the v0.1.0 form still works
helm install myriarch ... --set image.repository=registry.example.com/myriarch/myriarch
```

## Values schema

[`values.schema.json`](values.schema.json) is a JSON Schema (draft 2020-12) that Helm reads for `helm install`, `upgrade`,
`template` and `lint`. It is **strict**:

* A key that the chart does not read is an error, also inside `config`. Before, Helm ignored such a key and the chart
  rendered without it, so a misspelled setting went unnoticed.
* A value has the type that the chart needs: a number for a port, a string for a name, a list for a list, a boolean for a
  switch. Strings that name something have the syntax of the receiving system: a Kubernetes name, a quantity such as `512Mi`,
  a registry host, a digest, a duration in Go syntax (`30s`, `1m30s`; not `30` and not `30d`). The type of a switch matters for
  the TLS checks: a template counts the string `"false"` as true, so `ingress.allowWithoutTls: "false"` used to switch the check
  of the Ingress off, and the schema refuses it.
* The block `config` mirrors the configuration file of the server. It is generated from the configuration structure of the
  server (`internal/config`), with the same keys, types and descriptions as the
  [configuration reference](../../../docs/reference/configuration.md), so it cannot drift from the server.
* **Secrets are not values.** The schema has no key for a database DSN, an OIDC client secret, a webhook secret, the URL of a
  notification channel or an enrollment token: use the matching `*File` settings and the Secret `existingSecret`. A declared
  enrollment token (`enrollmentTokens[]`) names a Secret or lets the chart generate one, and a Secret reference that has the
  form of an enrollment token (`myre_...`) is refused. The settings that the chart generates (`config.auth.bootstrapAdmin`,
  `config.opamp.enrollmentTokens`) and the development mode (`config.dev`, `config.auth.devLogin`) are not offered in `config`.
* The few places that take a Kubernetes object as it is (`extraVolumes`, `extraVolumeMounts`, `podSecurityContext`,
  `securityContext`, `topologySpreadConstraints`, `affinity`, the Gateway API references) are open objects; the API server
  validates them.
* A parent chart that includes this chart as a dependency can use it: `global` is open (the chart reads only
  `global.imageRegistry`), and the key `enabled`, which Helm reads for `condition: myriarch.enabled`, is accepted and ignored by
  the chart. Every other key that the parent passes under `myriarch:` is checked like a key of a standalone installation.
* `null` removes a value that `values.yaml` defines, as in Helm. For a key that `values.yaml` does not define Helm keeps the
  null, and the schema accepts it only for the entries of the maps of the chart (annotations, labels, selectors), where the
  values files of `examples/` use it to switch off an entry of an earlier file, and for `global.imageRegistry`. It refuses it
  everywhere else, also for the entries of the maps in `config` (`executors.secrets`, `executors.secretEnvHosts`,
  `executors.secretFileHosts`): the chart would render the null into the ConfigMap, and the server refuses it when it starts.

An error names the path of the value, for example `at '/image/tagg': additional properties 'tagg' not allowed` (the wording
depends on the Helm version). The schema does not replace the checks of the templates, which relate several values
(an Ingress needs `trustedProxies` and a source in the network policy) and which also hold when the validation is switched
off with `--skip-schema-validation`; and it does not replace `myriarchd config validate`, which checks the meaning of the
configuration ([Verification](#verification)).

`values.schema.json` is generated by `make docs-gen` from [`values.schema.source.yaml`](values.schema.source.yaml), which holds
the hand-written part, and from `internal/config`; do not edit the JSON file. A new value of the chart goes into `values.yaml`,
the templates and `values.schema.source.yaml`; the tests fail when `values.yaml` or a template uses a value that the schema does
not list.

## Misconfiguration messages

Two layers refuse a value, and both name it. The **schema** refuses what one value can get wrong (an unknown key, a wrong type,
a malformed string) before it renders anything; Helm reports the path of the value as a JSON pointer. The **templates** refuse
what depends on several values, and they repeat a few checks of the schema (the digest, the OpenShift mode, the names, the keys
that the chart generates or does not offer) for the case that the validation is off. A template message is an `execution error`
that starts with the value path, followed by what is wrong and how to fix it, for example
`config.server.trustedProxies: must not be empty when ingress.enabled is true; ...`. The complete list of paths:

| Path | Cause |
|---|---|
| `image.digest` | not of the form `sha256:<64 hex>` |
| `config.server.trustedProxies` | empty while `ingress.enabled` or `httpRoute.enabled` (unless `allowWithoutTrustedProxies`) |
| `ingress.tls` | empty with an https `publicUrl` (unless `allowWithoutTls`) |
| `networkPolicy.ingress.from` | empty while `networkPolicy.enabled` and `ingress.enabled` or `httpRoute.enabled` |
| `networkPolicy.ingress.allowSameNamespace` | `true` while `trustedProxies` or `opamp.clientCertHeader` is set |
| `httpRoute.parentRefs` | empty while `httpRoute.enabled` |
| `httpRoute.hostnames` | empty while `httpRoute.enabled` |
| `httpRoute.tlsTerminatedAtGateway` | not `true` with an https `publicUrl` |
| `openshift.enabled` | not `true`, `false` or `auto` |
| `openshift.route.host` | no host derivable from `openshift.route.host` or `publicUrl` |
| `config.auth.bootstrapAdmin` | set in `config`; use the top-level `bootstrapAdmin` values |
| `config.opamp.enrollmentTokens` | set in `config`; use the top-level `enrollmentTokens` values |
| `config.auth.devLogin` | not available in this chart |
| `config.dev` | not available in this chart |
| `bootstrapAdmin.enabled` | no login path: `bootstrapAdmin.enabled` is `false` while neither `config.auth.oidc.enabled` nor `oidcFromEnvironment` is `true` |
| `bootstrapAdmin.tokenKey` | `existingSecret` set without `tokenKey` and `passwordHashKey` |
| `bootstrapAdmin.passwordHashKey` | set without `existingSecret` |
| `bootstrapAdmin.username` | not 1 to 64 characters of letters, digits, `.`, `_`, `@`, `-` |
| `enrollmentTokens[<i>].name` | not a DNS label, or duplicate |

## Production example

A complete, neutral example (HA values with OIDC, gateway collector overlay, secrets script and an offline verification
script) lives in `deploy/examples/production/`; see its README. The overlays in `deploy/helm/myriarch/examples/`
(`ingress-*.yaml`, `openshift.yaml`, `quickstart-dex.yaml`, `dex/dex.yaml`, `install-guide.yaml`) are not part of the packaged
chart. `install-guide.yaml` is the values file of the guide [Install with Helm](../../../docs/operations/install-helm.md): minimal
installation, GitOps over HTTPS, an `HTTPRoute` and one declared enrollment token.

## Verification

```bash
helm lint deploy/helm/myriarch
helm template myriarch deploy/helm/myriarch -f my-values.yaml | kubectl apply --dry-run=server -f -
# validate the rendered configuration without starting myriarchd (files and secrets do not have to exist):
helm template myriarch deploy/helm/myriarch -f my-values.yaml | yq 'select(.kind == "ConfigMap").data["myriarchd.yaml"]' > myriarchd.yaml
myriarchd config validate --config myriarchd.yaml --skip-files
# render the OpenShift profile without a cluster:
helm template myriarch deploy/helm/myriarch --set openshift.enabled=true
helm template myriarch deploy/helm/myriarch --set openshift.enabled=auto --api-versions security.openshift.io/v1
# everything together for the production example (lint, render, config validate, kustomize, kubeconform):
deploy/examples/production/verify.sh
# CI: both profiles, all examples and the assertions of the chart contract:
ci/helm-conform.sh
```
