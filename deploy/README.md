# deploy/

Operations artifacts. All supervisor configurations use exactly the keys of opampsupervisor v0.162.0; a test
(`internal/updater/deploy_test.go`) checks this and the consistency of templates, units and updater configuration.

| Directory | Contents |
|---|---|
| [`supervisor/`](supervisor) | Supervisor configuration templates `linux/`, `windows/`, `k8s/`, each with `supervisor.yaml` and `fallback.yaml` |
| [`scripts/`](scripts) | `install-linux.sh` (bash) and `install-windows.ps1` (PowerShell 5.1+) |
| [`systemd/`](systemd) | `otelcol-supervisor.service` (user `otelcol`), `myriarch-updater.service` (root) |
| [`updater/`](updater) | `updater.yaml` templates for Linux and Windows |
| [`windows/`](windows) | Services, paths, troubleshooting, `Set-ServiceEnvironment.ps1` (collector secrets) |
| [`helm/myriarch/`](helm/myriarch) | Helm chart for myriarchd (HA, WebSocket ingress, external PostgreSQL) |
| [`k8s/collector/`](k8s/collector) | Image (supervisor + otelcol-contrib + myriarch-updater), gateway StatefulSet, node DaemonSet, ConfigMaps; container mode of the myriarch-updater |
| [`updater/container/`](updater/container) | `updater.yaml` for container mode (`myriarch-updater container`) |
| [`compose/`](compose) | docker-compose: PostgreSQL 17 + myriarchd (+ optional myriarch-sim) for development |
| [`docker/`](docker) | `myriarchd.Dockerfile` (Node 20 + pnpm -> Go `-tags embedui` -> distroless, local builds), `myriarchd-release.Dockerfile` (runtime image from the signed release binary, CI), `myrctl.Dockerfile` (CI tool image), `myriarch-sim.Dockerfile` |
| [`release/`](release) | `goreleaser.yaml`: release matrix of all binaries (key-based signatures until the keyless GitHub release workflow), `authenticode.sh`, `README.md` (generating/rotating the signing key, signature format contract) |

CI scripts and local gates: [`../ci`](../ci) (`make check-local`); enterprise mirror tools: [`../contrib/enterprise-mirror`](../contrib/enterprise-mirror).

Installer errors carry stable codes (for example `ERROR [checksum_mismatch] ...`); they are documented at the top of
[`scripts/install-linux.sh`](scripts/install-linux.sh) and in [`windows/README.md`](windows/README.md).

## Installing the collector on hosts (supervisor model)

Three components run on every host: the **supervisor** (service `otelcol-supervisor`), the **collector** it starts, and
optionally the **myriarch-updater**, which applies binary updates offered over OpAMP packages. The install scripts use
neither deb/rpm nor the contrib MSI (both start a service of their own immediately).

### Linux (systemd)

```bash
sudo ./deploy/scripts/install-linux.sh \
  --opamp-endpoint wss://fleet.example.com/v1/opamp \
  --token myre_xxx \
  --site-type hq --site-id HQ1 --site-name "Headquarters" --site-country DE --site-timezone Europe/Berlin \
  --collector-version 0.162.0 \
  --with-updater --updater-url https://mirror.example.com/myriarch-releases/v<ver>/myriarch-updater_<ver>_linux_amd64 \
  --updater-checksums-url https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt --signing-key ./cosign.pub \
  --trusted-root ./trusted_root.json
```

* Collector and supervisor are **verified with SHA-256 and a Sigstore bundle** (`<asset>.sha256`,
  `<asset>.sigstore.json`) before anything is executed (`--verify-signatures require`, the default). The verifier is
  `cosign` v3 or `myriarch-updater verify-bundle` (`--verifier FILE`, or the updater downloaded with `--with-updater` and
  verified by SHA-256); without a verifier the installation aborts. `--trusted-root` enables offline verification.
* **Our own** binary (`myriarch-updater`, runs as root) needs a trustworthy hash source, at least one of:
  `--updater-sha256 <hex>` or `--updater-checksums-url <URL> --signing-key cosign.pub`. With the second variant the
  installer downloads `checksums.txt` and `checksums.txt.sig` (default `<URL>.sig`, or `--updater-checksums-sig-url`),
  verifies the signature with the public key (`openssl`, otherwise `cosign verify-blob`) and takes the hash from the line
  whose file name equals the last path segment of `--updater-url`. Both together are allowed (then both must match). The
  signature check also applies with `--insecure`. Format and keys: [`release/README.md`](release/README.md).
* Downloads only over **https** (also after redirects). `--insecure` allows only for labs and tests http/file,
  `--verify-signatures skip`, an updater without a hash source and ws:// to the server. The internal CA for a mirror with
  its own certificate must be in the certificate store of the host (`--ca-file` applies only to the connection to myriarchd).
* Better pass the token with `--token-file`: `--token` shows up in the process list and the shell history.
* Layout: `/opt/myriarch/collector/versions/<v>/otelcol-contrib`, `current` -> active version (symlink),
  `/opt/myriarch/supervisor/opampsupervisor`, configuration under `/etc/myriarch/` (`myriarch.env` and
  `enrollment-token` with mode 0600), data under `/var/lib/otelcol`.
* Idempotent: running it again brings the host to the given state (new version -> `current` switches, the service is
  restarted; local changes to `fallback.yaml` and further secrets in `myriarch.env` are kept).
* Enter collector secrets (for example `OTEL_GATEWAY_TOKEN`) in `/etc/myriarch/myriarch.env` and run
  `systemctl restart otelcol-supervisor`.
* `--root DIR` (staging mode) installs under a prefix without users/systemd - for image builds and tests.
* Removal: `--uninstall` (`--purge` also deletes the identity and the queue).

### Windows

```powershell
.\deploy\scripts\install-windows.ps1 -OpampEndpoint wss://fleet.example.com/v1/opamp -Token myre_xxx `
  -SiteType branch -SiteId BR042 -SiteName "Branch Hamburg" -SiteCountry DE -SiteRegion north `
  -SiteTimezone Europe/Berlin -CollectorVersion 0.162.0 -WithUpdater `
  -UpdaterUrl https://mirror.example.com/myriarch-releases/v<ver>/myriarch-updater_<ver>_windows_amd64.exe `
  -UpdaterChecksumsUrl https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt -SigningKey .\cosign.pub
```

Details, paths, services and troubleshooting (Event Viewer): [`windows/README.md`](windows/README.md).

The supervisor runs as the virtual service account `NT SERVICE\otelcol-supervisor` (not LocalSystem), only the
`myriarch-updater` runs as SYSTEM. Directories under `C:\ProgramData\Myriarch` are never taken over (foreign owner or
reparse point => abort); the ACL is reset and verified. Sigstore verification, the https requirement and the hash source
for the updater (`-UpdaterSha256` or `-UpdaterChecksumsUrl` + `-SigningKey`; the ECDSA signature is verified with .NET
without external tools) work as on Linux (`-VerifySignatures`, `-Verifier`, `-Insecure`); optionally
`-UpdaterSignerThumbprint` (the Authenticode signature of the updater must be valid and issued by this certificate);
better pass the token with `-TokenFile`.

### Important for golden images and cloned VMs

The instance UID of an agent is stored in `<supervisor-storage>/persistent_state.yaml` (Linux
`/var/lib/otelcol/supervisor`, Windows `C:\ProgramData\Myriarch\supervisor`), the one of the updater in
`<stateDir>/instance_uid`. The scripts create these directories only **empty**. Both must be emptied before an image is
created, otherwise all clones report the same identity and displace each other.

## myriarchd in Kubernetes

The chart installs with its defaults: it needs a Secret with the database DSN and the external URL, and it never
starts the dev mode.

```bash
kubectl create namespace myriarch
kubectl -n myriarch create secret generic myriarch-secrets \
  --from-literal=database-dsn='postgres://myriarch:...@db:5432/myriarch?sslmode=verify-full'
helm lint deploy/helm/myriarch
helm install myriarch deploy/helm/myriarch -n myriarch --set config.server.publicUrl=https://fleet.example.com
```

Notes on PostgreSQL (no PgBouncer in transaction pooling mode), secrets, access paths and the OpenShift profile are in
[`helm/myriarch/values.yaml`](helm/myriarch/values.yaml) and [`helm/myriarch/README.md`](helm/myriarch/README.md).

Login and the settings that matter for a real installation (in production myriarchd refuses insecure or missing
settings):

* **Login.** The default login is the bootstrap admin (`bootstrapAdmin`): the chart generates a one-time token in the
  Secret `<release>-bootstrap` (the install notes print the command to read it); use `bootstrapAdmin.existingSecret`
  with a bcrypt password hash for a durable break-glass account. For single sign-on enable `config.auth.oidc` with
  `roleMapping` (deny by default: users without a mapped group or user get no access) and disable the bootstrap admin
  (examples: `helm/myriarch/examples/quickstart-dex.yaml`, `docs/operations/authentication.md`);
* `config.executors.allowedWebhookHosts` is no longer mandatory: an empty list refuses every webhook and AWX target
  (fail closed). List the permitted hosts to use the executors, and bind each executor secret (`MYRIARCH_EXEC_*`,
  `secretEnv`) to its target hosts in `config.executors.secretEnvHosts` (without a binding myriarchd rejects it);
* `config.outbound.allowedCidrs` for internal targets (Git, AWX, mirror, IdP) - loopback, private networks and the
  cloud metadata address are blocked for outbound connections;
* `config.server.trustedProxies` (CIDR of the ingress, Gateway or router; mandatory while the ingress or HTTPRoute is
  enabled) for client IP limits and audit source addresses, together with
  `networkPolicy.ingress.allowSameNamespace=false`;
* `config.server.tls.terminatedUpstream: true` confirms that TLS (at least 1.2) ends at the ingress, Gateway or router;
* **OpenShift.** `openshift.enabled=true` selects the OpenShift profile (Route with edge TLS, no fixed user, group or
  fsGroup, OpenShift DNS rule). `auto` detects the cluster API, but `helm template` without
  `--api-versions security.openshift.io/v1` renders the standard profile. Overlay: `helm/myriarch/examples/openshift.yaml`.

Upgrade to migration `00003` (user identity from provider+issuer+subject): replicas of an older version can no longer
store OIDC users afterwards. For this upgrade reduce to one replica (`replicaCount=1`), upgrade and scale up again;
during a rolling update OIDC logins fail on old replicas until all are updated (sessions, agents and API tokens keep
working).

The dev compose file (`compose/docker-compose.yaml`) starts `myriarchd dev --allow-nonlocal` in the container (ports bound
to 127.0.0.1 only); `myriarchd dev` without this flag listens on loopback only and refuses non-loopback setups.

## Collector in Kubernetes

See [`k8s/collector/README.md`](k8s/collector/README.md) (supervisor and collector in the same container, identity on a
PVC/hostPath, version changes through the image tag).

## Release build

```bash
make release                                   # all binaries for all five platforms (check) -> dist/
make release-snapshot                          # goreleaser snapshot of the matrix (local test ONLY, unsigned; needs a Git checkout)
goreleaser release --snapshot --clean --skip=sign,publish -f deploy/release/goreleaser.yaml    # local snapshot test ONLY
```

Official releases are built from a tagged commit by `ci/build-release.sh release` (goreleaser, nothing is published by
the script) and are **always signed**. Key-based signing is the interim path; the GitHub release workflow replaces it with
keyless Sigstore signing (ADR-0023).

* **cosign with a key** (ECDSA P-256, no entry in a public transparency log) for `checksums.txt`, every binary and every
  archive: `<file>.sig` is the base64 encoding of an ASN.1 DER ECDSA signature over the SHA-256 of the file and can be
  verified with `openssl` (`openssl dgst -sha256 -verify cosign.pub -signature <(openssl base64 -d -A -in x.sig) x`).
  Generating, storing and rotating the key and the path to a KMS-held key: [`release/README.md`](release/README.md).
  The install scripts verify `checksums.txt` with it and take the updater hash from it (see above).
* **Authenticode** for the Windows binaries (`release/authenticode.sh`; release builds abort without a certificate).
* The runtime image of myriarchd (`docker/myriarchd-release.Dockerfile`) contains exactly the signed binary from `checksums.txt`;
  base images are pinned by digest in the Dockerfiles (maintenance: `ci/versions.env`, `ci/pin-images.sh`). The upstream
  images of the collector image are bound to the signed upstream release binaries
  (`contrib/enterprise-mirror/verify-upstream-images.sh`; the supervisor image is not signed with cosign upstream).
  `scripts/build-collector-image-crane.sh` runs this verification by default when it pushes (`VERIFY_UPSTREAM=0` skips
  it for a lab build), uses the reviewed digests of `ci/versions.env` for the pinned version and refuses upstream image
  overrides that are not pinned by digest (`ALLOW_UNPINNED_IMAGES=1` allows a tag for a lab build).
  `scripts/build-image-crane.sh` refuses the `dev` variant for the published repository `ghcr.io/myriarch/*`; build it
  with `IMAGE_VARIANT=release`.

The `myriarch-updater` archives additionally contain `deploy/` (scripts, templates, units) - the package is self-contained
on the host. In addition, bare binaries `myriarch-updater_<ver>_<os>_<arch>[.exe]` are produced for
`--updater-url`/`-UpdaterUrl`. The host artifacts are also copied to the mirror repository `myriarch-releases`
(`<mirror>/myriarch-releases/v<ver>/<file>`, `contrib/enterprise-mirror/publish-mirror.sh`), because hosts download from
there without credentials.

## License files

The project is Apache-2.0 (`LICENSE`). `THIRD_PARTY_NOTICES` reproduces the license texts and NOTICE files of the software the
binaries and the web UI include; it is generated by `scripts/gen-third-party-notices.sh` (`make notices`). Both files travel
with every artifact that is built here, except the mirror image (see below):

* **Archives.** Every `tar.gz` and `zip` archive of the release (`myriarchd`, `myriarch-updater`, `myrctl`, `myriarch-sim`) holds
  `LICENSE` and `THIRD_PARTY_NOTICES` at its root (`release/goreleaser.yaml`, an explicit `files` list per archive). The bare
  binaries (`myriarch-updater_<ver>_<os>_<arch>`) cannot hold files; the updater therefore prints both texts itself
  (`myriarch-updater licenses` and `myriarch-updater licenses --third-party`), also when it is the only file on a host.
* **myriarchd.** The same two texts are compiled into the binary and served without credentials at `/licenses/LICENSE` and
  `/licenses/THIRD_PARTY_NOTICES` (`/licenses` is an index); the web UI links to them under Settings, System.
* **Images.** Every image built from this repository holds `/usr/share/licenses/myriarch/LICENSE` and
  `/usr/share/licenses/myriarch/THIRD_PARTY_NOTICES` (mode 0644, `root:root`): the crane server image
  (`scripts/build-image-crane.sh`), `docker/myriarchd.Dockerfile`, `docker/myriarchd-release.Dockerfile`,
  `docker/myriarch-sim.Dockerfile`, `docker/myrctl.Dockerfile` and the collector image (`k8s/collector/Dockerfile`,
  `scripts/build-collector-image-crane.sh`). The updater image (`ghcr.io/myriarch/myriarch-updater`) follows the same contract
  once it exists. `scripts/build-image-crane.sh layout` and `scripts/build-collector-image-crane.sh layout` print the paths of
  the data and license files of the layer without building anything (a test checks them: `ci/tests/image-crane-test.sh`).
  The mirror image (`scripts/build-mirror-image-crane.sh`) is the exception: it holds busybox and the OpenTelemetry release
  files, not the project's binaries, and no `LICENSE` or `THIRD_PARTY_NOTICES`. Its label names the two licenses of its
  content (`Apache-2.0 AND GPL-2.0-only`); whoever pushes it takes on the obligations of busybox (corresponding source of the
  pinned base image). The label `org.opencontainers.image.licenses` (`Apache-2.0`, value in `ci/image-labels.env`) is set by the
  Dockerfiles of `docker/` and by `scripts/build-image-crane.sh`; the collector image has no such label because of its alpine base.
* **Build contexts of the release images.** The context of `docker/myriarchd-release.Dockerfile` is a directory with exactly
  three files: the binary `myriarchd` (from the goreleaser build) and `LICENSE` and `THIRD_PARTY_NOTICES` of the same commit.
  The context of `docker/myrctl.Dockerfile` is the same with the binary `myrctl`. `docker/myriarchd.Dockerfile` and
  `docker/myriarch-sim.Dockerfile` take the repository root as context, which already holds both files. A test
  (`internal/updater/deploy_consistency_test.go`) checks the COPY sources of the Dockerfiles.

`scripts/build-image-crane.sh` builds two variants of the server image, selected with `IMAGE_VARIANT`:

| `IMAGE_VARIANT` | `myriarchd` build tags | Data below `/opt/myriarch` | Default tag |
|---|---|---|---|
| `dev` (default) | `embedui`, `devpg` (`myriarchd dev` with the embedded PostgreSQL) | `examples/quickstart`, `examples/sim`, `release-fixtures` | `<commit>` |
| `release` | `embedui` | `examples/quickstart`, `examples/sim` | `<commit>-release` |

Both variants hold the license files. The release fixtures are test data for `myriarchd dev --releases-fixtures`; they are
not part of the release variant, of the Dockerfile images or of the collector image. An unknown value is rejected before
anything is built.

**Collector image.** `k8s/collector/Dockerfile` and `scripts/build-collector-image-crane.sh` add the files of the upstream
OpenTelemetry software next to the project files: `/usr/share/licenses/opentelemetry-collector/LICENSE` (the Apache-2.0 text;
it equals the upstream `LICENSE` of `opentelemetry-collector-contrib` and `opentelemetry-collector-releases` at v0.162.0 apart
from one leading empty line) and `/usr/share/licenses/opentelemetry-collector/README`, which names `otelcol-contrib` and
`opampsupervisor` with their version, "Copyright The OpenTelemetry Authors", Apache-2.0, the upstream repositories and tags
and the upstream images (with their digests in the crane build). The README does not list the Go modules linked into the
upstream binaries; their sources and `go.mod` files are in the upstream repositories at the named tags.

**Images that the project does not publish.** The project publishes only `ghcr.io/myriarch/myriarch` and
`ghcr.io/myriarch/myriarch-updater` (ADR-0024). The collector image and the mirror image
(`scripts/build-mirror-image-crane.sh`) are built by users into their own repository; the project takes on no obligation for
them, and whoever distributes them takes on the obligations of what they contain:

* The collector image is based on the upstream supervisor image (Alpine Linux); the packages of that base keep their own
  licenses.
* The mirror image is based on busybox, which is GPL-2.0-only, and holds the OpenTelemetry release archives (Apache-2.0). Whoever
  pushes the mirror image to a registry others can pull from must provide the corresponding source of busybox for the pinned base
  image (`BASE_IMAGE` in the script) and keep the license notices. The `org.opencontainers.image.licenses` label of
  `ci/image-labels.env` (Apache-2.0) does not describe these parts.

## Verification status

Covered by tests (without a container runtime and without a Windows host): configuration keys, install scripts in staging
mode (bash; PowerShell in `pwsh`) including the signature verification of our binaries (`ci/tests/installer-verify.sh`),
`helm lint`/`helm template` and schema validation of the rendered manifests (kubeconform), `goreleaser check` and a snapshot
build of the matrix, mirror upload and signing scripts against throw-away servers (`bash ci/tests/run-all.sh`). **Not**
covered by automated tests: Docker image builds from the Dockerfiles, service creation/registry/ACLs on Windows, the
`ECDsaCng` path of the signature verification on Windows PowerShell 5.1, systemd integration on a real host, applying the
manifests to a cluster.
