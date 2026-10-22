# Legacy Kubernetes collector (deprecated)

**Deprecated since v0.2.0.** Collectors in Kubernetes are installed with the Helm chart
[`deploy/helm/myriarch-collector`](../../deploy/helm/myriarch-collector), which runs the unchanged upstream images
`otel/opentelemetry-collector-opampsupervisor` and `otel/opentelemetry-collector-contrib` together with the published
`myriarch-updater` image. See [Installing collectors with Helm](../../docs/operations/install-collector-helm.md).

The files here are the earlier path with a combined collector image (supervisor, `otelcol-contrib` and `myriarch-updater` in
one image that you build yourself). There is **no support promise**: they receive no new features and no fixes except what
keeps the repository's own gates green, and they will be removed in a later release (planned: when the kind probe and the
production example use the chart).

## Layout

| Path | Contents (previous location) |
|---|---|
| `kustomize/` | Kustomize base, Dockerfile, NetworkPolicies and the old README (`deploy/k8s/collector/`) |
| `supervisor/` | `supervisor.yaml` and `fallback.yaml` templates for Kubernetes (`deploy/supervisor/k8s/`) |
| `updater/updater.yaml` | `myriarch-updater` template for container mode (`deploy/updater/container/updater.yaml`) |
| `build-collector-image-crane.sh` | Builds and pushes the combined collector image with crane, without a container runtime (`scripts/build-collector-image-crane.sh`) |

The make target `collector-image-crane` was removed. Call the script directly, for example
`IMAGE_REPO=registry.example.com/<org>/myriarch-collector contrib/legacy-kubernetes-collector/build-collector-image-crane.sh 0.162.0`
(`... help` lists the options; `UPDATER_VERSION` and `UPDATER_COMMIT` default to the git state).

## What still uses these files

- The kind probe (`hack/kind`) builds the combined image with the script and generates its ConfigMap from `supervisor/` and
  `updater/`.
- The production example overlay `deploy/examples/production/collector` is an overlay on `kustomize/` and is compared with
  `supervisor/supervisor.yaml` by `deploy/examples/production/verify.sh`.
- The Go tests in `internal/updater` and `internal/refdoc` cover the Dockerfile, the manifests and the templates, and the
  generated environment reference lists the variables of the templates.

## Why moved and not deleted

The kind probe (which needs a container runtime and cannot run in every environment) and the production example still consume
the image and the templates, and Go tests cover them. Deleting the files now would break both without a way to verify the
replacement. The follow-up tasks that install the chart on kind and move the production example to the chart remove this
directory together with its tests.
