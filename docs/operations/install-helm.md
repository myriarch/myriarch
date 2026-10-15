---
title: "Install with Helm"
weight: 40
description: "Install the Myriarch server on Kubernetes with the Helm chart: image, database, first sign-in, GitOps repository, access for agents and enrollment tokens."
---

This guide installs `myriarchd` on Kubernetes with the Helm chart `deploy/helm/myriarch`: two replicas, an external
PostgreSQL database, a GitOps repository over HTTPS and access for agents through a Gateway API `HTTPRoute`. It builds up
the values file step by step; the complete file is
[`deploy/helm/myriarch/examples/install-guide.yaml`](../../deploy/helm/myriarch/examples/install-guide.yaml), and CI
renders it with every change of the chart.

> **Support level.** The Helm chart is experimental in the first release ([ADR-0021](../adr/0021-publication-gates-and-v0-1-0-scope.md)).
> It is installed from a checkout of the repository at a release tag. A signed OCI chart is planned for v0.2.0. Read the
> [production checklist](production-checklist.md) before you rely on an installation.

## Prerequisites

- **Kubernetes 1.30 or newer** (`kubeVersion` of the chart), with a CNI that enforces NetworkPolicies, `kubectl` and
  Helm. CI renders the chart with Helm 4 (the version is pinned in `ci/versions.env`).
- **PostgreSQL 14 or newer** that you operate or buy as a managed service: a database, a user and a direct connection or
  session pooling. **Not behind PgBouncer in transaction pooling mode**: the server uses `LISTEN`/`NOTIFY` and session
  advisory locks for the leader election. Keep at least 60 free connections for two replicas.
- **Access for agents outside the cluster:** a Gateway API `Gateway` (recommended) or an ingress controller, with
  WebSocket support, long timeouts and TLS. OpAMP connections stay open permanently.
- **Outbound HTTPS** from the pods to GitHub (the release tracker and the collector downloads) or to an artifact mirror, and
  to your Git server.

## Get the chart and the image

The chart is part of the repository. Take the tag of the release you want to install; the default image of the chart is
`ghcr.io/myriarch/myriarch:<appVersion of the tag>`:

```bash
TAG=v0.1.0           # the release you install; the newest one is on https://github.com/myriarch/myriarch/releases
git clone --depth 1 --branch "${TAG}" https://github.com/myriarch/myriarch.git
```

The images `ghcr.io/myriarch/myriarch` (the server) and `ghcr.io/myriarch/myriarch-updater` are published with the tags
`X.Y.Z` and `X.Y` (`X.Y.Z-rc.N` for release candidates). There is no tag `latest`. **They exist from v0.1.0-rc.3 on.** Pin
the image by digest in production (`image.digest`, see below).

### Registry and repository of the image

> **Available from v0.2.0.** The chart of v0.1.0 has the registry in `image.repository` and no `image.registry`.

The chart builds the image reference from these values:

| Value | Default | Meaning |
|---|---|---|
| `image.registry` | `ghcr.io` | Registry host with an optional port (`registry.example.com:5000`). |
| `image.repository` | `myriarch/myriarch` | Repository without the registry host. |
| `image.tag` | the `appVersion` of the chart | Tag of the image. |
| `image.digest` | empty | `sha256:` and 64 hexadecimal characters. When set, the image is `<repository>@<digest>` and the tag is ignored. |
| `global.imageRegistry` | not set | Replaces `image.registry`, so that a parent chart can point all images to a mirror. |

If your cluster pulls from a mirror of `ghcr.io`, set the registry and keep the repository; pin the image with `image.digest`
as described above:

```yaml
image:
  registry: registry.example.com
```

A repository that starts with a registry host is used as it is, and then neither `image.registry` nor `global.imageRegistry`
applies. The first path segment is a registry host when it contains a `.` or a `:` or is `localhost`. The values of v0.1.0
(`image.repository: registry.example.com/myriarch/myriarch`) are of that form and render the same image as before. A repository
**without** a host takes `image.registry` now. If you set such a repository to pull through the default registry of your
container runtime, set `image.registry: ""` as well. The schema ([below](#the-values-schema)) refuses a registry with a scheme
or a path, and a repository with a tag, a digest or upper case letters.

### Build the image yourself

The published release images are the supported way to install: they are built from the release archives that the release
workflow signs, and they are an index for `linux/amd64` and `linux/arm64`. Build an image yourself only to run an unreleased
commit, or when your cluster cannot pull from `ghcr.io` and you do not mirror the release image. The script needs no
container runtime, but it needs the build toolchain of the repository (Go, Node.js and pnpm, see
[Local development](../getting-started/local-dev.md#prerequisites); the script builds the web UI when it is missing) and
`python3`. It compiles the binaries from your work tree, packs them into a `FROM scratch` image and pushes it to your own
registry with `crane`; the work tree must be clean (`ALLOW_DIRTY=1` overrides this for experiments). The script never pushes
to `ghcr.io/myriarch/*`, which belongs to the release workflow.

```bash
make tools TOOLS=crane                                         # installs crane into .cache/bin
IMAGE_REPO=registry.example.com/myriarch/myriarch scripts/build-image-crane.sh login USER < token-file
IMAGE_VARIANT=release IMAGE_REPO=registry.example.com/myriarch/myriarch scripts/build-image-crane.sh | tee image.out
sed -n 's/^IMAGE_DIGEST=//p' image.out
```

The last command prints the `sha256:...` digest of the pushed image. Use it in the installation with
`--set image.registry=registry.example.com --set image.repository=myriarch/myriarch --set image.digest=sha256:...` (or with the
repository in the form `registry.example.com/myriarch/myriarch` and no registry). Use
`IMAGE_VARIANT=release`: the default variant `dev` is for `myriarchd dev` and holds the embedded PostgreSQL and test
fixtures, and a production installation does not need them. The image is built for one platform, `linux/amd64`;
`IMAGE_PLATFORM=linux/arm64` builds an arm64 image with a tag that ends in `-arm64`. There is no multi-platform index.

The image has the same files, user and entry point as the release image, but it is built from your work tree. It differs
in the metadata (its working directory is `/opt/myriarch`, the labels differ), it has one platform and it carries no
SBOM:

- **User:** `65532:65532`, the user the chart runs the pod as (`podSecurityContext.runAsUser`). The image has no shell and
  no package database.
- **Programs:** `/usr/local/bin/myriarchd` is the entry point, with the default arguments `serve --config
  /etc/myriarchd/myriarchd.yaml`; `/usr/local/bin/myrctl` and `/usr/local/bin/myriarch-sim` are next to it.
- **Data:** the example repositories below `/opt/myriarch/examples` and the licenses below `/usr/share/licenses/myriarch`,
  all world-readable.
- **Data directory:** the empty directory `/var/lib/myriarchd` (mode `0750`, owned by `65532:65532`). The chart mounts its
  data volume there (an `emptyDir` or a PVC, `persistence.enabled`) and puts the GitOps clone and the artifact cache below
  it; the volume is group-writable for the pod through `fsGroup: 65532`.
- **Scratch space:** an empty `/tmp`. The chart mounts an `emptyDir` there and keeps the root file system read-only.

`IMAGE_VARIANT=release scripts/build-image-crane.sh layout` lists the files of the image without building anything.
`scripts/build-image-crane.sh` documents all variables and its output; read the header of the script in your checkout
rather than relying on this page for details of options.

## Install with the minimal values

The chart installs with its defaults once two things exist: a Secret with the database connection string and the
external URL of the server. Create the namespace and the Secret. The commands read the connection string without echo,
so that it does not appear in your shell history or on the screen, and write it to a file that only you can read. Paste
the string at the prompt (for example `postgres://myriarch:<password>@pg.example.com:5432/myriarch?sslmode=verify-full`)
and press Enter:

```bash
kubectl create namespace myriarch
(umask 077; read -rs DSN; printf '%s' "$DSN" > database-dsn)
kubectl -n myriarch create secret generic myriarch-secrets --from-file=database-dsn=database-dsn
rm database-dsn
```

Write the values file `myriarch-values.yaml`:

```yaml
config:
  server:
    publicUrl: https://fleet.example.com
```

and install the chart from the checkout:

```bash
helm install myriarch ./myriarch/deploy/helm/myriarch -n myriarch -f myriarch-values.yaml
kubectl -n myriarch rollout status deploy/myriarch
```

(With a self-built image add `--set image.registry=... --set image.repository=... --set image.digest=sha256:...`.) The server
runs in production mode, never in dev mode. It applies the database migrations at its first start. `helm install` prints notes
that repeat the commands below. Without an `HTTPRoute`, `Ingress` or `Route` nothing is reachable from outside the cluster yet.

## Sign in

Until you configure single sign-on, the login is the bootstrap admin, a break-glass administrator that the chart creates
with a one-time token. Open the UI through a port-forward (the NetworkPolicy does not affect it):

```bash
kubectl -n myriarch port-forward svc/myriarch 8080:8080     # then open http://localhost:8080
```

Read the token (the command does not print it anywhere else) and paste it into the bootstrap admin form on the login
page:

```bash
kubectl -n myriarch get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d
```

The token works for **one** sign-in. For a new one, delete the Secret and run `helm upgrade` again. For a durable
break-glass account use a bcrypt password hash in your own Secret (`bootstrapAdmin.existingSecret` and
`bootstrapAdmin.passwordHashKey`). Both modes, the audit events and the network protection of the sign-in path are described in
[Authentication](authentication.md#2-bootstrap-admin).

## Connect the GitOps repository

Git is the source of truth for blueprints, fleets and rollout policies. Start with an empty repository on your Git server
and fill it from the template, as described in [`gitops-template/README.md`](../../gitops-template/README.md). Create a
read-only access token for the repository and add it to the Secret under the key `git-token`. The patch keeps the
existing keys. The token is read without echo and goes through a temporary file that only you can read, instead of a
command line argument or your shell history:

```bash
(umask 077; read -rs TOKEN; printf '{"stringData":{"git-token":"%s"}}' "$TOKEN" > git-token-patch.json)
kubectl -n myriarch patch secret myriarch-secrets --type merge --patch-file git-token-patch.json
rm git-token-patch.json
```

Then add the GitOps settings to `myriarch-values.yaml`:

```yaml
config:
  server:
    publicUrl: https://fleet.example.com
  gitops:
    enabled: true
    repoUrl: https://git.example.com/fleet/fleet-config.git
    auth:
      tokenFile: /etc/myriarchd/secrets/git-token
```

A Git server at a private address is refused until you allow its range in `config.outbound.allowedCidrs` (for example
`[198.51.100.0/24]`). Apply the change:

```bash
helm upgrade myriarch ./myriarch/deploy/helm/myriarch -n myriarch -f myriarch-values.yaml
```

Open the *GitOps* page of the UI. It shows the state of the synchronization and the revision that the server applied; the
same information is in `GET /api/v1/gitops/status`. A repository with errors is not applied; the page lists the issues. If
you want to check a repository before it reaches the server, use `myrctl validate`
([Validate a GitOps repository](../tasks/validate-a-gitops-repository.md)).

## Make the server reachable for agents

Collectors and users reach the server through the Gateway. The chart creates the `HTTPRoute`, not the Gateway. Ingress
controllers and OpenShift work too: the overlays in `deploy/helm/myriarch/examples/` (for example
[`ingress-traefik.yaml`](../../deploy/helm/myriarch/examples/ingress-traefik.yaml); there are also
`ingress-haproxy.yaml`, `ingress-f5-nginx.yaml` and `openshift.yaml` with a Route, and `ingress-nginx.yaml` as a legacy
example, because ingress-nginx is no longer maintained) replace the `httpRoute` block. By default the chart requires
three settings, because the server must trust the forwarded headers of the proxy only:

- `config.server.trustedProxies`: the addresses the server sees the connections from, that is the pods of the Gateway or
  ingress controller (not the address of the load balancer);
- `networkPolicy.ingress.from`: the namespace of those pods, so that port 8080 is open to them only;
- `networkPolicy.ingress.allowSameNamespace: false`, because any other pod of the namespace could forge the forwarded
  headers otherwise.

Rendering fails with a message that names the value if one of them is missing. Two exceptions exist: `httpRoute` and
`ingress` accept an empty `trustedProxies` only with `allowWithoutTrustedProxies: true` (the client address is then not
evaluated), and the OpenShift Route adds the namespace of the router to the NetworkPolicy by itself. The agents then connect to
`wss://fleet.example.com/v1/opamp`. The complete values file of this guide, with the sections from above, the route and
a declared enrollment token (next section):

```yaml
# deploy/helm/myriarch/examples/install-guide.yaml
config:
  server:
    publicUrl: https://fleet.example.com
    # REPLACE: the addresses myriarchd sees the connections from (the Gateway pods)
    trustedProxies:
      - 192.0.2.0/24
  gitops:
    enabled: true
    repoUrl: https://git.example.com/fleet/fleet-config.git
    auth:
      tokenFile: /etc/myriarchd/secrets/git-token

httpRoute:
  enabled: true
  parentRefs:
    - name: gateway
      namespace: gateway-system
  hostnames:
    - fleet.example.com
  tlsTerminatedAtGateway: true

networkPolicy:
  ingress:
    from:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: gateway-system
    allowSameNamespace: false

enrollmentTokens:
  - name: linux-hosts
    labels:
      environment: production
```

(The file in the repository carries comments. `fleet.example.com`, `git.example.com` and `192.0.2.0/24` are documentation
placeholders.) The file's `tlsTerminatedAtGateway: true` confirms that the Gateway listener terminates TLS; the chart cannot
check it. Find the pod addresses of your Gateway with `kubectl get pods -o wide` in its namespace and replace
`192.0.2.0/24`. Check the file before you apply it (next sections), then:

```bash
helm upgrade myriarch ./myriarch/deploy/helm/myriarch -n myriarch -f myriarch-values.yaml
```

## Enrollment tokens and collectors

Collectors present an enrollment token when they connect. The block `enrollmentTokens` of the values above declares a
token: the chart generates the Secret `myriarch-enrollment-linux-hosts` with a random token and the server registers it
at its start, with the label `environment=production` that every collector inherits. Read it with:

```bash
kubectl -n myriarch get secret myriarch-enrollment-linux-hosts -o jsonpath='{.data.token}' | base64 -d
```

You can also create tokens in the UI (*Settings*, tab *Enrollment tokens*). Declared tokens show `config` as their creator.
Generated values survive upgrades, but a tool that renders the chart without cluster access (`helm template` in Argo CD
or Flux) would generate a new value on every render: use `enrollmentTokens[].existingSecret` there.

- **Hosts** (Linux servers, Windows): [Connect a host](../tasks/connect-a-host.md) installs the supervisor, the collector
  and the updater and enrolls them with such a token.
- **Collectors in Kubernetes:** [`deploy/k8s/collector/README.md`](../../deploy/k8s/collector/README.md) describes the
  gateway StatefulSet and the node DaemonSet. Container mode, in which the `myriarch-updater` changes the collector version
  inside the pod, is beta and needs a collector image that you build yourself (`make collector-image-crane`) until the
  collector chart arrives in v0.2.0.

## Single sign-on

The bootstrap admin is meant for the initial setup. Configure OpenID Connect as described in
[Authentication](authentication.md) (recipes for Keycloak, Dex, Microsoft Entra ID, Okta, authentik, Zitadel and Google),
then switch the bootstrap admin off with `bootstrapAdmin.enabled=false`, or keep it as a break-glass account with a
password hash in a vault (`keepWithOidc: true`). With OIDC enabled and no `keepWithOidc`, the chart no longer mounts the
bootstrap credentials and the server deactivates the account.

## The values schema

> **Available from v0.2.0.** The chart of v0.1.0 has no schema and ignores a key that it does not know.

The chart ships a strict schema for its values,
[`values.schema.json`](../../deploy/helm/myriarch/values.schema.json). Helm checks your values against it in `helm install`,
`helm upgrade`, `helm template` and `helm lint`, before it renders anything, and stops with the path of the value that does not
fit. The wording depends on the Helm version; Helm 4 prints, for a misspelled key:

```text
Error: values don't meet the specifications of the schema(s) in the following chart(s):
myriarch:
- at '/config/server': additional properties 'listenAdr' not allowed
```

The schema refuses:

- **A key that the chart does not know**, also inside `config`, where the keys are those of the
  [configuration reference](../reference/configuration.md). A misspelled setting used to be ignored without a word.
- **A value of the wrong type** or in the wrong form: a word for a port, a string for a list, a registry with a scheme, a digest
  without `sha256:`, a Kubernetes name with an underscore, a quantity such as `lots`, or a duration without a Go unit
  (`shutdownTimeout: 30` and `audit: 30d` are errors; write `30s` and `720h`). A switch must be a boolean: a template counts the
  string `"false"` as true, so `ingress.allowWithoutTls: "false"` used to switch the TLS check of the Ingress off.
- **A secret as a value.** The schema has no key for a database DSN, an OIDC client secret, a webhook secret, the URL of a
  notification channel or an enrollment token. Put them into the Secret and use the `*File` settings
  (`database.dsnFile`, `auth.oidc.clientSecretFile`, `gitops.webhookSecretFile`, `notifications.channels[].urlFile`); a declared
  token in `enrollmentTokens` names a Secret or is generated, and a Secret reference that looks like an enrollment token
  (`myre_...`) is refused.
- **Keys that the chart generates or does not offer:** `config.auth.bootstrapAdmin` and `config.opamp.enrollmentTokens`
  (use `bootstrapAdmin` and `enrollmentTokens` at the top level) and the development mode (`config.dev`, `config.auth.devLogin`).

A parent chart that includes this chart as a dependency passes its values under the name of the dependency (`myriarch:`), and
Helm checks them against the same schema. The key `enabled`, which the usual `condition: myriarch.enabled` reads, and the shared
values under `global` are accepted; any other key that the chart does not know is refused there as well.

It does not judge whether a configuration makes sense, and it does not relate values to each other. The templates still stop an
Ingress without `trustedProxies`, and `myriarchd config validate` ([below](#check-before-you-deploy)) still checks the rendered
configuration. Helm can skip the schema with `--skip-schema-validation`; the checks of the templates remain, but a misspelled key
is then ignored again, so use the flag only to get past a finding while you investigate it.

**Before you upgrade from v0.1.0,** run `helm lint` or `helm template` with your values file and the chart of the new release.
If the release was installed with `--set` or upgraded with `--reuse-values`, write its values to a file first:
`helm get values myriarch -n myriarch -o yaml > myriarch-values.yaml`. Values that the old chart ignored can fail now. Typical
findings and what to do:

| Finding | What to do |
|---|---|
| `additional properties 'x' not allowed` | The chart never read this key. Correct the spelling or remove it. |
| `got number, want string`, `got string, want integer` | Use the type of the setting. A duration is a string with a unit (`30s`), a port is a number. Quote numbers that must stay strings (`--set-string`). |
| A key of `config` that you took from the configuration file of a standalone server | Use only the keys of the configuration reference; the settings that the chart generates or refuses (above) have top-level values or are not available. |
| `config.database.dsn` or another secret as a value | Move the secret into the Secret and set the `*File` setting. The chart refused to render such values into the ConfigMap before as well. |

The hand-written part of the schema is `values.schema.source.yaml` next to it; `make docs-gen` assembles the JSON file from it and
from `internal/config` ([Development guide](../development.md#generated-references)).

## Check before you deploy

The chart and the rendered server configuration can be checked without a cluster and without secrets. You need `helm`,
`yq` (mikefarah, version 4) and a `myriarchd` binary: `make tools TOOLS="helm yq"` in the checkout installs the first two
into `.cache/bin`, and `make web-build && make build` builds `bin/myriarchd`:

```bash
helm lint ./myriarch/deploy/helm/myriarch -f myriarch-values.yaml
helm template myriarch ./myriarch/deploy/helm/myriarch -n myriarch -f myriarch-values.yaml \
  | yq 'select(.kind == "ConfigMap").data["myriarchd.yaml"]' > myriarchd.yaml
bin/myriarchd config validate --config myriarchd.yaml --skip-files
```

`helm lint` and `helm template` check the values against the schema first. `config validate` applies the checks of
`myriarchd serve` (it refuses what the server refuses) and with `--skip-files` it does not need the secret files. Then read the
[production checklist](production-checklist.md). A complete, validated example with OIDC, an artifact mirror and a gateway
collector is in [`deploy/examples/production/README.md`](../../deploy/examples/production/README.md).

## Upgrade and uninstall

Upgrade from the checkout of a newer tag, with the same values file; read the changelog of the release first:

```bash
git -C myriarch fetch --depth 1 origin tag <newer tag>
git -C myriarch checkout <newer tag>
helm upgrade myriarch ./myriarch/deploy/helm/myriarch -n myriarch -f myriarch-values.yaml
```

The server applies new database migrations at its start. From v0.2.0 on the chart validates its values against a strict schema:
run `helm lint` with your values file before the upgrade ([The values schema](#the-values-schema)). An upgrade guide is planned
for v0.2.0.

`helm uninstall myriarch -n myriarch` removes what the chart created, including the generated Secrets (the bootstrap
token and the declared enrollment tokens). It keeps the Secret `myriarch-secrets`, the namespace and every external
Secret you referenced with `existingSecret`, and the database with all its data. Delete the database yourself if you want
a fresh start.

An uninstall does **not** revoke enrollment tokens. The token records live in the database, and the server never revokes a
declared token automatically: the old token keeps working for as long as the database exists and nobody revokes it. A
reinstall generates a new Secret with a new token value, and the server logs a warning for the old token that is no
longer configured. Do not treat a reinstall as a token rotation. To retire the old token, revoke it in the UI (*Settings*,
tab *Enrollment tokens*) or with `DELETE /api/v1/enrollment-tokens/{id}`; see
[Authentication](authentication.md#63-rotation) for the rotation without a gap.
