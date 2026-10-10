# Production example

A neutral, production-style example for running myriarchd on Kubernetes with the Helm chart in
[`deploy/helm/myriarch`](../../helm/myriarch): two replicas with a PodDisruptionBudget and a NetworkPolicy, generic OIDC
login, an external PostgreSQL database, a generic artifact mirror for the collector binaries, a Git server for the
GitOps repository and a Slack notification channel. It also contains an overlay for a gateway collector.

All host names, IDs and addresses are placeholders: host names use `example.com`, addresses come from the RFC 5737
documentation ranges (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`). The only other ranges are the documented
Kubernetes defaults (`10.96.0.0/12` service CIDR and `10.244.0.0/16` pod CIDR of kubeadm, `10.128.0.0/14` and
`172.30.0.0/16` of OpenShift), each marked as such in the comments. Nothing in this directory is a real value.

| Path | Purpose |
|---|---|
| `myriarch-values.yaml` | Helm values (`helm upgrade --install myriarch <chart> -n myriarch -f ...`) |
| `myriarchd.yaml` | standalone `myriarchd serve` configuration, equivalent to the `config:` block of the values |
| `create-secrets.example.sh` | example script for namespaces, CA bundle, TLS secret, pull secret and secrets (reads files only) |
| `collector/` | kustomize overlay for the gateway collector (`kubectl apply -k`) |
| `monitoring/prometheusrule.yaml` | optional alerting rules |
| `verify.sh` | offline verification (lint, render, `myriarchd config validate`, kustomize, optional kubeconform) |

The ingress variants for other controllers live next to the chart, in
[`deploy/helm/myriarch/examples/`](../../helm/myriarch/examples) (`ingress-nginx.yaml`, `ingress-f5-nginx.yaml`,
`ingress-traefik.yaml`, `ingress-haproxy.yaml`), and `openshift.yaml` switches to the OpenShift profile with a Route
instead of an Ingress. `quickstart-dex.yaml` (with `dex/dex.yaml`) is a separate demo on top of the chart defaults, not
of this example. None of them is part of the packaged chart.

## Adapt the example

Copy this directory into your own deployment repository and replace the placeholders:

1. **Registry (`IMAGE_REGISTRY`).** The example uses `registry.example.com/myriarch/...`. Replace
   `registry.example.com` with your registry, for example:

   ```bash
   export IMAGE_REGISTRY=registry.mycompany.example
   sed -i.bak "s#registry.example.com#${IMAGE_REGISTRY}#g" myriarch-values.yaml collector/kustomization.yaml create-secrets.example.sh
   ```

   Pin the images by digest (`image.digest` in the values, `digest:` in `collector/kustomization.yaml`); `verify.sh`
   fails if a rendered image is not digest-pinned. The pull secret is called `registry-pull-secret` everywhere.
2. **OIDC.** Set `config.auth.oidc.issuerUrl`, `clientId` and the role mapping in `myriarch-values.yaml` and in
   `myriarchd.yaml` (provider-specific issuer forms and claims: `docs/operations/authentication.md`). The client secret
   goes into the Secret. The bootstrap admin is off in this example; the values show how to keep a break-glass account
   next to OIDC (`keepWithOidc`, password hash in a Secret made with `myriarchd auth hash-password`).
3. **Database.** Put the PostgreSQL DSN into the Secret key `database-dsn` (`config.database.dsnFile`). Keep at least
   60 free connections for two replicas.
4. **Artifact mirror.** Point `config.artifacts.sourceBaseUrl` to a mirror that copies the GitHub release downloads of
   the collector 1:1, including the `.sha256` and `.sigstore.json` files. Add the addresses of the mirror, the Git
   server and AWX to `config.outbound.allowedCidrs`.
5. **Ingress.** The values use ingress-nginx. For another controller add one of the files from
   `deploy/helm/myriarch/examples/` as a second values file (`-f`). Adjust `server.publicUrl`, the Ingress host and
   `server.trustedProxies` (the pod addresses of your controller). `networkPolicy.ingress.allowSameNamespace` stays
   `false` because `trustedProxies` is set (rendering fails otherwise). `openshift.yaml` replaces the controller by the
   OpenShift router and Route.
6. **Proxy and CA.** The proxy block is commented out. Enable it only if your cluster reaches the internet through a
   proxy. The CA bundle mount (`internal-ca`) is for services that use a private PKI; remove it if you do not need it.
7. **Both files.** `myriarch-values.yaml` and `myriarchd.yaml` carry the same configuration. Change both; `verify.sh`
   compares them.

Create the secrets with `create-secrets.example.sh` (read its header first), install the chart, then create an
enrollment token in the UI and deploy the gateway overlay with `kubectl apply -k collector`.

## Verify

`verify.sh` needs `helm`, `yq` (mikefarah v4), `kustomize` (or `kubectl`) and `go` (or a `MYRIARCHD_BIN`). It writes only
to a temporary directory and exits with 1 if any check fails:

```bash
deploy/examples/production/verify.sh
KUBECONFORM=kubeconform deploy/examples/production/verify.sh   # also validate all manifests against Kubernetes schemas
VERIFY_NO_PLACEHOLDERS=1 deploy/examples/production/verify.sh  # go-live gate: fail while placeholders remain
```

The script renders the chart with a fixed test digest and checks, among other things, the replica count, the
NetworkPolicy and PodDisruptionBudget, a Route instead of an Ingress for the OpenShift variant, digest pinning of the myriarchd and gateway images, that `myriarchd.yaml` is
equivalent to the rendered ConfigMap, that every secret file referenced by the configuration has a key in
`create-secrets.example.sh`, and that the collector overlay renders no DaemonSet and uses a `wss://` endpoint.
