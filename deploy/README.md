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
| [`docker/`](docker) | `myriarchd.Dockerfile` (Node 24 + pnpm -> Go `-tags embedui` -> distroless) and `myriarch-sim.Dockerfile`, for local builds. The published images are not built from Dockerfiles (see "Published images") |
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
  --updater-checksums-url https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt \
  --trusted-root ./trusted_root.json
```

* Collector and supervisor are **verified with SHA-256 and a Sigstore bundle** (`<asset>.sha256`,
  `<asset>.sigstore.json`) before anything is executed (`--verify-signatures require`, the default). The verifier is
  `cosign` 3.0.4 or newer or `myriarch-updater verify-bundle` (`--verifier FILE`, or an installed updater, or - with
  `--updater-sha256` - the updater downloaded with `--with-updater` and verified by that hash); without a verifier the
  installation aborts. `--trusted-root` enables offline verification.
* **Our own** binary (`myriarch-updater`, runs as root) needs a trustworthy hash source, at least one of:
  `--updater-sha256 <hex>` or `--updater-checksums-url <URL>`. With the second variant the installer downloads
  `checksums.txt` and its keyless Sigstore bundle (default `<URL>.sigstore.json`, or `--updater-bundle-url`), verifies the
  bundle against the release identity `release.yml@refs/tags/v<version>` of this repository with the version from
  `--updater-version` or from the file name of `--updater-url`, and takes the hash from the line whose file name equals the
  last path segment of `--updater-url`. The verifier is `cosign` 3.0.4 or newer or an already installed updater, never the
  binary that is being installed; a host without a verifier verifies `checksums.txt` on an admin host and passes
  `--updater-sha256`. Both together are allowed (then both must match). The check also applies with `--insecure`.
  `--signing-key` and `--updater-checksums-sig-url` were removed. Steps and commands:
  [Verify releases](../docs/security/verify-releases.md), [Connect a host](../docs/tasks/connect-a-host.md).
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
  -UpdaterChecksumsUrl https://mirror.example.com/myriarch-releases/v<ver>/checksums.txt
```

Details, paths, services and troubleshooting (Event Viewer): [`windows/README.md`](windows/README.md).

The supervisor runs as the virtual service account `NT SERVICE\otelcol-supervisor` (not LocalSystem), only the
`myriarch-updater` runs as SYSTEM. Directories under `C:\ProgramData\Myriarch` are never taken over (foreign owner or
reparse point => abort); the ACL is reset and verified. Sigstore verification, the https requirement and the hash source
for the updater (`-UpdaterSha256` or `-UpdaterChecksumsUrl` with its keyless bundle; `-UpdaterVersion`,
`-UpdaterBundleUrl`) work as on Linux (`-VerifySignatures`, `-Verifier`, `-Insecure`); optionally
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
* The published images (`ghcr.io/myriarch/myriarch`, `ghcr.io/myriarch/myriarch-updater`) contain exactly the binaries and
  example files of the release archives that `checksums.txt` lists (see "Published images"); base images are pinned by digest in the local
  Dockerfiles (maintenance: `ci/versions.env`, `ci/pin-images.sh`). The upstream
  images of the collector image are bound to the signed upstream release binaries
  (`contrib/enterprise-mirror/verify-upstream-images.sh`; the supervisor image is not signed with cosign upstream).
  `scripts/build-collector-image-crane.sh` runs this verification by default when it pushes (`VERIFY_UPSTREAM=0` skips
  it for a lab build), uses the reviewed digests of `ci/versions.env` for the pinned version and refuses upstream image
  overrides that are not pinned by digest (`ALLOW_UNPINNED_IMAGES=1` allows a tag for a lab build).
  `scripts/build-image-crane.sh` in its default mode (build from source) refuses `ghcr.io/myriarch/*` for every variant
  (`DRY_RUN=1` only builds); the published images are built with `IMAGE_SOURCE=dist` and pushed by
  `ci/release/push-images.sh` from `release.yml`.

The `myriarch-updater` archives additionally contain `deploy/` (scripts, templates, units) - the package is self-contained
on the host. In addition, bare binaries `myriarch-updater_<ver>_<os>_<arch>[.exe]` are produced for
`--updater-url`/`-UpdaterUrl`. The host artifacts are also copied to the mirror repository `myriarch-releases`
(`<mirror>/myriarch-releases/v<ver>/<file>`, `contrib/enterprise-mirror/publish-mirror.sh`), because hosts download from
there without credentials.

## Published images

The project publishes two images, each an image index for `linux/amd64` and `linux/arm64` ([ADR-0024](../docs/adr/0024-container-registry-and-image-set.md)):

| Image | Programs | Entrypoint | Default command |
|---|---|---|---|
| `ghcr.io/myriarch/myriarch` | `myriarchd` with the embedded web UI, `myrctl` and `myriarch-sim`, with the example repositories | `/usr/local/bin/myriarchd` | `serve --config /etc/myriarchd/myriarchd.yaml` |
| `ghcr.io/myriarch/myriarch-updater` | `myriarch-updater` | `/usr/local/bin/myriarch-updater` | none |

The images contain the binaries and example files of the linux archives of the release build (`myriarchd_<version>_linux_<arch>.tar.gz`,
`myrctl_<version>_linux_<arch>.tar.gz`, `myriarch-sim_<version>_linux_<arch>.tar.gz` for the server image and
`myriarch-updater_<version>_linux_<arch>.tar.gz` for the updater image, all listed in `checksums.txt`), not a second compilation. The release tags are
`X.Y.Z-rc.N` for release candidates and `X.Y.Z` and `X.Y` for final releases; there is no `latest` during v0.x. Pin the digest
in deployments.

* **Contract.** `FROM scratch`, one layer per platform, `USER 65532:65532` (the Helm chart uses the same user and group), no
  shell, no package manager and no package database, `HOME=/tmp`, `PATH=/usr/local/bin`, working directory `/`.
  The license files are in `/usr/share/licenses/myriarch/` (`LICENSE`, `THIRD_PARTY_NOTICES`), `/etc/passwd` and `/etc/group` name
  the user 65532, `/tmp` is empty with mode 1777. Everything else belongs to `root` and is world readable (programs 0755, data
  0644, directories 0755), except the data directory of the server image. The root CAs are embedded in the binaries
  (`golang.org/x/crypto/x509roots/fallback`, used only when the host has no CA bundle), and so is the time zone database of
  `myriarchd` and `myrctl`.
  * **Server image.** `/usr/local/bin/myriarchd` (entrypoint), `/usr/local/bin/myrctl` and `/usr/local/bin/myriarch-sim`, the
    example repositories `/opt/myriarch/examples/quickstart` (a GitOps repository for a first try) and
    `/opt/myriarch/examples/sim` (profiles of the simulator, which refer to the component list in the same directory), and the
    empty data directory `/var/lib/myriarchd` with mode 0750, owned by `65532:65532`. The data directory is where the
    server keeps the GitOps clone and the artifact cache (`gitops.cloneDir` and `artifacts.dir` of the Helm chart are
    below it), so a named volume (Docker and Compose copy owner and mode of the directory into an empty volume), an
    `emptyDir` or a PVC (with the `fsGroup` 65532 of the chart) mounted there is writable for the non-root user, and the
    artifact cache survives restarts when the volume does. Nothing is written outside of volumes, so the root file system can be
    read-only (OpenShift runs the image with another user ID; it needs a volume at the data directory like every
    non-root image). Exposed ports 8080 (UI, REST, OpAMP), 9090 (metrics of the server) and 9464 (metrics of the simulator).
    The default command starts the server; start the other programs with another entrypoint, for example
    `docker run --entrypoint /usr/local/bin/myriarch-sim IMAGE run --profile /opt/myriarch/examples/sim/small.yaml` or
    `docker run --entrypoint /usr/local/bin/myrctl IMAGE validate /opt/myriarch/examples/quickstart`.
    The working directory is `/`, which is not writable for the user 65532, and the release `myriarchd` has no embedded
    PostgreSQL (no `devpg` tag). `myriarchd dev` with this image therefore needs the paths and the database spelled out:
    `dev --gitops-dir /opt/myriarch/examples/quickstart --data-dir /var/lib/myriarchd --dsn <PostgreSQL DSN>` (the defaults
    `examples/quickstart` and `.dev` are relative to `/`), plus `--allow-nonlocal` and a `--listen` address that the container
    port can reach, with a volume at the data directory. The image built from source (`dev` variant) has the embedded
    PostgreSQL and the working directory `/opt/myriarch`.
  * **Updater image.** `/usr/local/bin/myriarch-updater` (entrypoint, no default command). It has no data directory: the
    updater image is the source of the binary for the collector image (`UPDATER_SOURCE=image`) and the state directory of
    container mode (`stateDir`, `installDir`) is on the volume of the image that runs the updater
    (`docs/design/container-mode.md`).
  The image has no shell, so it cannot serve as the image of a CI job that needs `sh` (the GitLab starter in `gitops-template/`
  builds a small image of its own from the `myrctl` release archive); the project publishes no separate image of `myrctl`.
* **Labels.** `org.opencontainers.image.{title,description,source,version,revision,created,vendor,licenses}` and the base name
  `scratch` in the config of every platform; the same keys without the base name as annotations of the index, from which ghcr.io
  links the package to `https://github.com/myriarch/myriarch`. Values common to all images are in `ci/image-labels.env`.
* **Build.** `IMAGE_SOURCE=dist IMAGE_KIND=server|updater scripts/build-image-crane.sh` reads the archives from `dist/`
  (`DIST_DIR`), checks them (SHA-256 from `checksums.txt`; static ELF of the platform; embedded root CAs; for `myriarchd` the web
  UI without `devpg`; the time zone database of `myriarchd` and `myrctl`; the commit of a clean work tree recorded in the
  binaries; the same `LICENSE` and `THIRD_PARTY_NOTICES` in all archives of an image) and writes an OCI layout
  below `.cache/image-dist/` (`OUT_DIR`) whose `index.json` is the index. Fixed timestamps and sorted entries make it
  deterministic: the same archives and commit give the same uncompressed layer (`diff_id`) everywhere, and the same layer,
  manifest and index digests with the same zlib (the compressed layer comes from the zlib that Python links; a different
  implementation such as zlib-ng can compress to other bytes, so the build logs the zlib version). The release build (without
  `ALLOW_DIRTY=1`) also needs a clean tree, the `metadata.json` of goreleaser for the same commit, both platforms and the SBOMs.
  It also writes one SPDX SBOM per platform with the pinned
  syft (`IMAGE_SBOM=0` skips it). Nothing is compiled and nothing is pushed. Try it with `make release-snapshot` and
  `ALLOW_DIRTY=1 IMAGE_SOURCE=dist IMAGE_KIND=updater scripts/build-image-crane.sh`.
* **Push.** `ci/release/push-images.sh <layout> <repository>@sha256:<digest>` pushes by digest with `crane push --index`,
  compares the digest with `index.json` before and with the registry afterwards, and never sets a tag. It refuses a reference
  without digest, because `crane` then creates the tag `latest`. The repositories `ghcr.io/myriarch/*` are written only from
  `.github/workflows/release.yml` at a version tag (`GITHUB_WORKFLOW_REF`); the release tags are added after signing and
  verification. A build from source (`IMAGE_SOURCE=build`, below) refuses these repositories.
* **Origin of the example files.** The example repositories are not read from the work tree: `examples/quickstart` is part of
  the `myriarchd` archive and `examples/sim` of the `myriarch-sim` archive (`release/goreleaser.yaml`), so `checksums.txt`, and
  with it the signature of the release, covers every file of the image, and the image can be rebuilt from `dist/` alone. The
  builder reads only regular files below those two directories and refuses links, paths that leave them, a file that is the parent
  of another file and leftovers of a local work tree (`.DS_Store`, `._*`, `*~`, `*.swp`): a release is built from a fresh checkout.
* **Verification.** The signatures and attestations of the images and how to verify them come with the release workflow.

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
  `/usr/share/licenses/myriarch/THIRD_PARTY_NOTICES` (mode 0644, `root:root`): the published server and updater images
  (`scripts/build-image-crane.sh` with `IMAGE_SOURCE=dist`), the crane image built from source, `docker/myriarchd.Dockerfile`,
  `docker/myriarch-sim.Dockerfile` and the collector image (`k8s/collector/Dockerfile`,
  `scripts/build-collector-image-crane.sh`). `scripts/build-image-crane.sh layout` and
  `scripts/build-collector-image-crane.sh layout` print the paths of the data and license files of the layer without building
  anything, and `scripts/build-image-crane.sh layout-dirs` prints its directories with mode and owner (a test checks them:
  `ci/tests/image-crane-test.sh`).
  The mirror image (`scripts/build-mirror-image-crane.sh`) is the exception: it holds busybox and the OpenTelemetry release
  files, not the project's binaries, and no `LICENSE` or `THIRD_PARTY_NOTICES`. Its label names the two licenses of its
  content (`Apache-2.0 AND GPL-2.0-only`); whoever pushes it takes on the obligations of busybox (corresponding source of the
  pinned base image). The label `org.opencontainers.image.licenses` (`Apache-2.0`, value in `ci/image-labels.env`) is set by the
  Dockerfiles of `docker/` and by `scripts/build-image-crane.sh`; the collector image has no such label because of its alpine base.
* **Files of the published images.** They come from the release archives of the programs (`LICENSE` and `THIRD_PARTY_NOTICES`
  at the archive root, see above; the archives of one image must carry the same texts), not from a build context. `docker/myriarchd.Dockerfile` and `docker/myriarch-sim.Dockerfile`
  take the repository root as context, which already holds both files. A test
  (`internal/updater/deploy_consistency_test.go`) checks the COPY sources of the Dockerfiles.

The single-platform server image that `scripts/build-image-crane.sh` builds from the work tree (`IMAGE_SOURCE=build`, the default;
development, tests and the kind probe; it runs as `65532:65532`, contains `myriarchd`, `myrctl` and `myriarch-sim` and is pushed
to the `IMAGE_REPO` you name, never to `ghcr.io/myriarch/*`) comes in two variants, selected with `IMAGE_VARIANT`:

| `IMAGE_VARIANT` | `myriarchd` build tags | Data below `/opt/myriarch` | Default tag |
|---|---|---|---|
| `dev` (default) | `embedui`, `devpg` (`myriarchd dev` with the embedded PostgreSQL) | `examples/quickstart`, `examples/sim`, `release-fixtures` | `<commit>` |
| `release` | `embedui` | `examples/quickstart`, `examples/sim` | `<commit>-release` |

Both variants hold the license files. The release fixtures are test data for `myriarchd dev --releases-fixtures`; they are
not part of the release variant, of the Dockerfile images or of the collector image. An unknown value is rejected before
anything is built.

**Data directory of the local images.** The local images follow the data directory contract of the published server image (see
"Published images"): the image built from source and `docker/myriarchd.Dockerfile` contain the empty directory
`/var/lib/myriarchd` (0750, owned by `65532:65532`), so that a named volume mounted there is writable for the non-root user.
`compose/docker-compose.yaml` mounts the named volume `myriarchd-data` there and starts `myriarchd dev --data-dir
/var/lib/myriarchd`; a bind mount would keep the owner of the host directory. `docker/myriarch-sim.Dockerfile` has no data
directory because the simulator writes nothing. A test (`internal/updater/deploy_image_contract_test.go`) checks the
Dockerfile, the Compose file, the build script and the Helm chart against each other; the Docker build itself is not run by
the tests.

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
mode (bash; PowerShell in `pwsh`) including the verification of the keyless release bundle (`ci/tests/installer-verify.sh`),
`helm lint`/`helm template` and schema validation of the rendered manifests (kubeconform), `goreleaser check` and a snapshot
build of the matrix, mirror upload and signing scripts against throw-away servers (`bash ci/tests/run-all.sh`). **Not**
covered by automated tests: Docker image builds from the Dockerfiles, service creation/registry/ACLs on Windows, systemd integration on a real host, applying the
manifests to a cluster. The release images are covered up to the registry: the build from release archives (contract, determinism,
SBOMs) and the push by digest run against a registry on `127.0.0.1` in `ci/tests/image-crane-test.sh`; whether ghcr.io accepts
a tag-less push by digest and rejects a wrong digest is checked with the first release candidate.
