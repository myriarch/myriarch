#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds and pushes the myriarch container image WITHOUT a container runtime (no Docker, Podman or Buildah daemon) and
# WITHOUT a base image: the image is built FROM scratch and contains no operating system packages at all.
#
#   IMAGE_REPO=registry.example.com/<org>/myriarch scripts/build-image-crane.sh
#                                             build + push  <IMAGE_REPO>:<git short sha>
#   make image-crane IMAGE_REPO=registry.example.com/<org>/myriarch
#                                             the same through the Makefile
#   IMAGE_REPO=<registry>/<path> scripts/build-image-crane.sh login USER
#                                             one-time registry login (token on stdin, see below)
#   IMAGE_REPO=registry.example.com/<org>/myriarch DRY_RUN=1 scripts/build-image-crane.sh
#                                             build binaries + layer + image layout only, no registry access at all
#   IMAGE_VARIANT=release IMAGE_REPO=registry.example.com/<org>/myriarch scripts/build-image-crane.sh
#                                             the release variant: no devpg tag, no release fixtures (see below)
#   IMAGE_VARIANT=release scripts/build-image-crane.sh layout
#                                             print the paths of the data and license files of the variant; needs no
#                                             IMAGE_REPO, no network and no repository state, and builds nothing
#   IMAGE_SOURCE=dist IMAGE_KIND=server scripts/build-image-crane.sh
#   IMAGE_SOURCE=dist IMAGE_KIND=updater scripts/build-image-crane.sh
#                                             the release images (see below): from the linux archives of a goreleaser
#                                             build in dist/, writes a layout for linux/amd64 and linux/arm64, one SPDX
#                                             SBOM per platform; pushes nothing and needs no IMAGE_REPO
#
# Two ways to build, selected with IMAGE_SOURCE:
#   build (default)  compiles the binaries from this work tree and builds a single-platform image of the development and
#                    test variants (myriarchd + myrctl + myriarch-sim); pushes it to IMAGE_REPO. Never to ghcr.io/myriarch.
#   dist             builds the published images from the release binaries: ghcr.io/myriarch/myriarch (myriarchd) and
#                    ghcr.io/myriarch/myriarch-updater (ADR-0024, ADR-0029), each an index for linux/amd64 and linux/arm64.
#                    Nothing is compiled: the binaries are the ones in the goreleaser archives (listed in checksums.txt, which
#                    the release workflow signs). The images are pushed by ci/release/push-images.sh, by digest, from the
#                    release workflow only.
#
# What it does
#   1. builds static linux/<arch> binaries (IMAGE_PLATFORM, default linux/amd64) with the ldflags of the Makefile: myriarchd (-tags
#      embedui,devpg in the dev variant: `myriarchd dev` runs with the embedded PostgreSQL; -tags embedui in the release
#      variant; the web UI is built with `make web-build` if internal/webui/dist is missing), myriarch-sim, myrctl; checks that
#      every binary carries the embedded Mozilla root CAs (and, where it needs them, the time zone database);
#   2. packs them into ONE deterministic layer tar (root:root, mtime = commit time, sorted): 0755 binaries in
#      /usr/local/bin, 0644 data below /opt/myriarch (examples/quickstart, examples/sim and, in the dev variant only, release
#      fixtures), the license files below /usr/share/licenses/myriarch, a minimal /etc/passwd and /etc/group (user and
#      group 65532) and an empty /tmp (1777);
#   3. assembles the image and pushes it in ONE step, so there is never a half-finished image under the final tag:
#        BASE_IMAGE=scratch (default)  the layer is gzipped here and written together with the image config and the manifest
#                                      as an OCI image layout (no build timestamps: the same commit gives the same image
#                                      digest), `crane push` uploads it. `crane append` alone cannot set entrypoint, user
#                                      or labels and `crane mutate` needs an image that is already in a registry, so a
#                                      scratch image would need two pushes (layer first, config second) - this does not;
#        BASE_IMAGE=<image>            `crane mutate --append`: the layer goes on top of that image, entrypoint, cmd, env,
#                                      workdir, user and labels are set in the same push;
#   4. prints the digest. Lines starting with IMAGE_ on stdout are machine readable (everything else goes to stderr).
#
# Release images (IMAGE_SOURCE=dist)
#   Inputs: the goreleaser dist directory (DIST_DIR, default ./dist) with checksums.txt and the archives
#   myriarchd_<version>_linux_<arch>.tar.gz (IMAGE_KIND=server) or myriarch-updater_<version>_linux_<arch>.tar.gz
#   (IMAGE_KIND=updater). <version> is IMAGE_RELEASE_VERSION (default: "version" of DIST_DIR/metadata.json, which goreleaser
#   derives from the tag without "v"). Each archive must be listed in checksums.txt with its SHA-256; its binary is checked
#   (static ELF of the platform, embedded root CAs, and for the server the web UI and the time
#   zone database; scripts/lib/oci_image.py). Dev build tags (devpg) are refused.
#   Output: a flat OCI layout in OUT_DIR/<kind> (default .cache/image-dist/<kind>; it is recreated): index.json IS the
#   index with the manifests of both platforms and its annotations, so its SHA-256 is the digest of the image index
#   before anything is pushed. The same inputs give the same bytes (SOURCE_DATE_EPOCH or the commit time). Beside it:
#   OUT_DIR/<kind>.env (the IMAGE_ lines below) and, unless IMAGE_SBOM=0, OUT_DIR/<kind>.sbom/<os>_<arch>.spdx.json from the
#   pinned syft (SYFT_CHECK_FOR_APP_UPDATE=false; SYFT overrides the binary). stdout:
#     IMAGE_KIND, IMAGE_LAYOUT (directory), IMAGE_INDEX_DIGEST (sha256:<sha256 of index.json>),
#     IMAGE_MANIFEST_DIGEST_LINUX_AMD64, IMAGE_MANIFEST_DIGEST_LINUX_ARM64, IMAGE_SBOM_LINUX_AMD64, IMAGE_SBOM_LINUX_ARM64
#   Contract: FROM scratch, ONE layer per platform, user and group 65532 (numeric USER 65532:65532), entrypoint
#   /usr/local/bin/myriarchd (CMD serve --config /etc/myriarchd/myriarchd.yaml) or /usr/local/bin/myriarch-updater, the
#   licenses in /usr/share/licenses/myriarch, no shell. The OCI labels are in the platform configs, the title, description,
#   source, version, revision, created, vendor and licenses also in the index annotations (ghcr.io reads them from there).
#   The source is SOURCE_URL or LABEL_SOURCE of ci/image-labels.env, never the git remote.
#
# Why FROM scratch
#   The binaries are static (CGO_ENABLED=0) and use no system library. The OS packages of a base image are dead weight
#   and are exactly where image vulnerability scanners report findings, none of them in our code. Without a base there is
#   no package database and no os-release to match against. What the binaries need from an operating system is embedded
#   instead:
#     root CAs     golang.org/x/crypto/x509roots/fallback (Mozilla set, imported in cmd/myriarchd, cmd/myriarch-sim, cmd/myrctl).
#                  Go uses it only if the host has no CA bundle of its own (x509.SetFallbackRoots); outbound.caFile, the
#                  gitops/AWX CA files etc. are added on top of it (internal/netguard). A bundle mounted at
#                  /etc/ssl/certs/ca-certificates.crt (or SSL_CERT_FILE) is used instead of the embedded set.
#     time zones   time/tzdata (myriarchd: cmd/myriarchd and internal/blueprint, myrctl: internal/blueprint; myriarch-sim never
#                  loads a zone)
#     DNS          the pure Go resolver; /etc/resolv.conf and /etc/hosts are provided by the container runtime
#   The image has no shell, no curl and no package manager: the only executables are the three Go binaries (the manifests
#   use httpGet probes and no exec).
#
# Image contract (what deploy manifests may rely on)
#   ENTRYPOINT /usr/local/bin/myriarchd, CMD serve --config /etc/myriarchd/myriarchd.yaml, ENV HOME=/tmp (scratch: and
#   PATH=/usr/local/bin), WORKDIR /opt/myriarch, USER 65532 (numeric and non-root for runAsNonRoot; OpenShift replaces the
#   UID by one of the project range, so nothing is written outside of volumes and all files are world readable),
#   myriarch-sim: command /usr/local/bin/myriarch-sim, myrctl: /usr/local/bin/myrctl,
#   data: /opt/myriarch/examples/quickstart, /opt/myriarch/examples/sim, and in the dev variant only
#   /opt/myriarch/release-fixtures (inputs of `myriarchd dev --releases-fixtures`; the release variant does not carry them),
#   licenses (both variants, 0644 root:root): /usr/share/licenses/myriarch/LICENSE (Apache-2.0) and
#   /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES (generated by scripts/gen-third-party-notices.sh); the build fails if one
#   of them is missing. myriarchd serves the same texts under /licenses,
#   /etc/passwd + /etc/group (root, and user and group 65532), an empty /tmp (1777).
#
# Environment
#   IMAGE_SOURCE   build (default) | dist                 see above; dist ignores IMAGE_REPO, IMAGE_VARIANT, BASE_IMAGE, TAG, DRY_RUN
#   IMAGE_KIND     server | updater (dist only, required)
#   DIST_DIR       goreleaser dist directory (dist only)  default ./dist
#   IMAGE_RELEASE_VERSION  release version without "v" (dist only)  default: DIST_DIR/metadata.json
#   IMAGE_PLATFORMS  linux/amd64,linux/arm64 (dist only; the default is the release matrix, the tests use one platform)
#   IMAGE_SBOM     1 (default) | 0: write the SPDX SBOMs of the platforms (dist only)
#   SYFT           path of the syft binary                default .cache/bin/syft (ci/install-tools.sh syft)
#   OUT_DIR        output directory (dist only)           default ./.cache/image-dist
#   IMAGE_REPO     target repository (build only; required, no default): <registry>[:port]/<path> without tag or digest
#   IMAGE_VARIANT  dev (default) | release                dev: myriarchd with the devpg tag (embedded PostgreSQL for `myriarchd dev`)
#                                                         and /opt/myriarch/release-fixtures; release: embedui only, no
#                                                         release fixtures. Examples and license files are in both.
#   TAG            image tag (build only)                 default <git short sha> (+ "-dirty" with ALLOW_DIRTY=1; + "-release" for
#                                                         the release variant; + "-arm64" for arm64), so the variants never
#                                                         overwrite each other
#   IMAGE_PLATFORM linux/amd64 (default) | linux/arm64: the platform of the single-platform image; arm64 tags get the suffix
#                  "-arm64", so they never overwrite the amd64 tag (there is no multi-platform index here)
#   BASE_IMAGE     scratch (default) = no base image, nothing else in the image than listed above. Any other value is an
#                  image reference that must be pinned by digest (<registry>/<image>:<tag>@sha256:<digest>) and
#                  switches to `crane mutate --append` on that base; the image then has the OS packages of the base.
#   ALLOW_DIRTY    1 = build although the work tree has uncommitted changes (refused otherwise)
#   WEB_BUILD      auto (default: build the UI only if internal/webui/dist is missing) | always | never
#   DOCKER_CONFIG  directory of crane's registry credentials (config.json); respected, never set by this script; crane's
#                  default is ~/.docker. Point it at a dedicated directory if ~/.docker/config.json names a credential
#                  helper that is not installed
#   CRANE          path of the crane binary               default .cache/bin/crane (built with go build from ci/tools/crane)
#   SOURCE_URL     org.opencontainers.image.source        default: https form of `git remote get-url origin`, else
#                  LABEL_SOURCE of ci/image-labels.env, else no label
#   SOURCE_DATE_EPOCH  timestamp of all layer entries     default commit time (same commit => same layer digest)
#   DRY_RUN        1 = stop before the push (layer.tar and, for scratch, the OCI layout oci/ are kept in OUT_DIR,
#                  default ./.cache/image); IMAGE_REPO is still required (it names the image)
#   VERIFY         1 = after the push also list the files of the new layer from the registry
#   KEEP_WORK      1 = keep the temporary build directory (printed at the end)
#
# Labels that are the same for every image of the project (vendor, licenses, titles, fallback source URL) come from
# ci/image-labels.env.
#
# One-time login (token via stdin so it never shows up in the process list):
#   IMAGE_REPO=<registry>/<path> scripts/build-image-crane.sh login '<registry-user>' < token-file
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"

IMAGE_REPO="${IMAGE_REPO:-}"
BASE_IMAGE="${BASE_IMAGE:-scratch}"
IMAGE_VARIANT="${IMAGE_VARIANT:-dev}"
ALLOW_DIRTY="${ALLOW_DIRTY:-0}"
WEB_BUILD="${WEB_BUILD:-auto}"
DRY_RUN="${DRY_RUN:-0}"
VERIFY="${VERIFY:-0}"
KEEP_WORK="${KEEP_WORK:-0}"
IMAGE_UID="65532" # the user and group of every image of the project (Helm chart, Dockerfiles, release images)
IMAGE_SOURCE="${IMAGE_SOURCE:-build}"
IMAGE_KIND="${IMAGE_KIND:-}"
DIST_DIR="${DIST_DIR:-${ROOT}/dist}"
IMAGE_SBOM="${IMAGE_SBOM:-1}"
IMAGE_PLATFORMS="${IMAGE_PLATFORMS:-linux/amd64,linux/arm64}"
# the image contract (the same values for the scratch layout and for `crane mutate`; lists are comma separated like the
# crane flags, so their elements must not contain commas)
IMAGE_ENTRYPOINT="/usr/local/bin/myriarchd"
IMAGE_CMD="serve,--config,/etc/myriarchd/myriarchd.yaml"
IMAGE_WORKDIR="/opt/myriarch"
IMAGE_PORTS="8080/tcp,9090/tcp,9464/tcp"
# label values must not contain commas (crane parses --label as CSV)
IMAGE_DESCRIPTION="OpAMP fleet manager for OpenTelemetry Collectors - myriarchd + myriarch-sim + myrctl"
IMAGE_SUMMARY="OpAMP fleet manager for OpenTelemetry Collectors"
IMAGE_CONTENTS="static Go binaries and data only; no OS packages; no shell; root CAs and time zones are embedded in the binaries"
IMAGE_TITLE="" # LABEL_TITLE of ci/image-labels.env (the server image), set once the labels are loaded
BASE_MODE="image"
if [ "${BASE_IMAGE}" = scratch ]; then BASE_MODE="scratch"; fi

log()  { printf '[image] %s\n' "$*" >&2; }
warn() { printf '[image] WARNING: %s\n' "$*" >&2; }
die()  { printf '[image] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# shellcheck source=scripts/lib/image-common.sh
. "${ROOT}/scripts/lib/image-common.sh"

# ---- variant ------------------------------------------------------------------------------------------------------
# dev: devpg build tag + release fixtures; release: neither. Sets MYRIARCHD_TAGS.
image_variant_check() {
  case "${IMAGE_VARIANT}" in
    dev) MYRIARCHD_TAGS="embedui,devpg" ;;
    release) MYRIARCHD_TAGS="embedui" ;;
    *) die "invalid IMAGE_VARIANT '${IMAGE_VARIANT}' (dev or release)" ;;
  esac
  # The dev variant (embedded PostgreSQL, release fixtures) is never pushed to the published repository.
  if image_is_published_repo "${IMAGE_REPO}"; then
    [ "${IMAGE_VARIANT}" = release ] || die "the published repository ${IMAGE_REPO} takes the release variant only: set IMAGE_VARIANT=release (the default is dev)"
  fi
}

# The published repositories hold the release images only: they are built from the release archives (IMAGE_SOURCE=dist) and
# pushed by ci/release/push-images.sh from the release workflow, never from this work tree. A dry run pushes nothing.
image_publish_check() {
  if image_is_published_repo "${IMAGE_REPO}" && [ "${DRY_RUN}" != 1 ]; then
    die "${IMAGE_REPO} is a published repository: its images are built from the release archives (IMAGE_SOURCE=dist) and pushed by ci/release/push-images.sh in the release workflow; this build from source pushes elsewhere (IMAGE_REPO) or runs with DRY_RUN=1"
  fi
}

WORK=""
cleanup() {
  if [ -n "${WORK}" ] && [ -d "${WORK}" ]; then
    if [ "${KEEP_WORK}" = 1 ]; then log "build directory kept: ${WORK}"; else rm -rf "${WORK}"; fi
  fi
}
trap cleanup EXIT

# ---- crane ------------------------------------------------------------------------------------------------------
resolve_crane() {
  if [ -n "${CRANE:-}" ]; then
    [ -x "${CRANE}" ] || die "CRANE=${CRANE} is not executable"
    return 0
  fi
  # the pinned version of ci/versions.env, built from ci/tools/crane (go build, hashes from go.sum; cached by a stamp file)
  if TOOLS_BIN="${TOOLS_BIN}" "${ROOT}/ci/install-tools.sh" crane >&2 && [ -x "${TOOLS_BIN}/crane" ]; then
    CRANE="${TOOLS_BIN}/crane"
  elif have crane; then
    warn "could not build the pinned crane (ci/install-tools.sh crane failed): using $(command -v crane)"
    CRANE="$(command -v crane)"
  else
    die "crane not available: run ci/install-tools.sh crane (needs Go and access to the Go module proxy)"
  fi
}

registry_hint() {
  cat >&2 <<EOF
[image] If the push failed with UNAUTHORIZED / DENIED, you are not logged in or have no write permission:
[image]   IMAGE_REPO=${IMAGE_REPO} scripts/build-image-crane.sh login '<registry-user>' < token-file     (stored in $(image_docker_config_desc))
EOF
  if [ "${BASE_MODE}" = image ]; then
    cat >&2 <<EOF
[image] If the base image cannot be fetched: is the registry reachable, was the pinned digest removed? Pick a new one on
[image] purpose: crane digest <repo>:<tag>
EOF
  fi
}

cmd_login() {
  local user="${1:-}"
  image_require_repo scripts/build-image-crane.sh "registry.example.com/<org>/myriarch"
  [ -n "${user}" ] || die "usage: IMAGE_REPO=<registry>/<path> scripts/build-image-crane.sh login <user>  (the token is read from stdin)"
  [ ! -t 0 ] || die "the token is read from stdin: pass it with '< token-file' or a pipe (never as an argument)"
  resolve_crane
  image_docker_config_prepare
  "${CRANE}" auth login "${IMAGE_REPO%%/*}" --username "${user}" --password-stdin >&2
  if [ -n "${DOCKER_CONFIG:-}" ] && [ -f "${DOCKER_CONFIG}/config.json" ]; then chmod 600 "${DOCKER_CONFIG}/config.json"; fi
  log "credentials for ${IMAGE_REPO%%/*} stored in $(image_docker_config_desc)"
}

# ---- git metadata -------------------------------------------------------------------------------------------------
# git_state: the commit, the commit time and the dirty state of the work tree; sets DIRTY, FULL_SHA, COMMIT, EPOCH and DATE.
git_state() {
  have git || die "git not found"
  git -C "${ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "${ROOT} is not a git work tree (tag, labels and version come from git)"
  DIRTY=0
  if [ -n "$(git -C "${ROOT}" status --porcelain)" ]; then
    DIRTY=1
    if [ "${ALLOW_DIRTY}" != 1 ]; then
      git -C "${ROOT}" status --short >&2
      die "the work tree has uncommitted changes: commit them or set ALLOW_DIRTY=1 (the tag then ends in -dirty)"
    fi
    warn "building from a dirty work tree (ALLOW_DIRTY=1): the image is not reproducible from a commit"
  fi
  FULL_SHA="$(git -C "${ROOT}" rev-parse HEAD)"
  # the same value as the Makefile (COMMIT), so `make build` and the image report the same commit
  COMMIT="${COMMIT:-$(git -C "${ROOT}" rev-parse --short HEAD)}"
  EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "${ROOT}" log -1 --format=%ct)}"
  [[ "${EPOCH}" =~ ^[0-9]+$ ]] || die "invalid SOURCE_DATE_EPOCH '${EPOCH}'"
  DATE="$(python3 -c 'import datetime,sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))' "${EPOCH}")"
}

# git_metadata: git_state plus the version, the tag and the source URL of the build from source.
git_metadata() {
  git_state
  # the same value as the Makefile (VERSION)
  VERSION="${VERSION:-$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || echo "0.0.0-${COMMIT}")}"
  # `git describe --dirty` ignores untracked files, this script does not
  if [ "${DIRTY}" = 1 ]; then
    case "${VERSION}" in *-dirty) ;; *) VERSION="${VERSION}-dirty" ;; esac
  fi
  if [ -z "${TAG:-}" ]; then
    TAG="${COMMIT}"
    if [ "${DIRTY}" = 1 ]; then TAG="${COMMIT}-dirty"; fi
    if [ "${IMAGE_VARIANT}" = release ]; then TAG="${TAG}-release"; fi
    TAG="${TAG}${IMAGE_TAG_SUFFIX}"
  fi
  [[ "${TAG}" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]] || die "invalid image tag '${TAG}'"
  SOURCE_URL="$(image_source_url "${ROOT}")"
}

# ---- web UI -------------------------------------------------------------------------------------------------------
ensure_ui() {
  local index="${ROOT}/internal/webui/dist/index.html" newer=""
  case "${WEB_BUILD}" in
    auto|always|never) ;;
    *) die "WEB_BUILD must be auto, always or never" ;;
  esac
  if [ "${WEB_BUILD}" = always ] || { [ "${WEB_BUILD}" = auto ] && [ ! -f "${index}" ]; }; then
    have make || die "make not found (needed for 'make web-build')"
    log "building the web UI (make web-build)"
    make -C "${ROOT}" web-build >&2
  fi
  [ -f "${index}" ] || die "internal/webui/dist/index.html is missing: run 'make web-build' (the image needs the UI; WEB_BUILD=never forbids building it here)"
  # a stale UI is a silent trap: warn when sources are newer than the build
  newer="$( (find "${ROOT}/web/src" "${ROOT}/web/index.html" "${ROOT}/web/package.json" "${ROOT}/web/vite.config.ts" "${ROOT}/api/openapi.yaml" \
    -type f -newer "${index}" 2>/dev/null || true) | head -n 1)"
  if [ -n "${newer}" ]; then warn "web sources are newer than internal/webui/dist (e.g. ${newer#"${ROOT}"/}): WEB_BUILD=always rebuilds the UI"; fi
}

# ---- binaries -----------------------------------------------------------------------------------------------------
# The image has neither a CA bundle nor zoneinfo files: the binaries carry the Mozilla roots and, where they load time
# zones, the tz database. A missing import would only show at run time (x509: certificate signed by unknown authority for
# every public TLS endpoint, unknown time zone), so it is checked here on the built binary.
check_embedded_data() { # binary tags
  local b="$1" tags="$2" info deps
  info="$(go version -m "${WORK}/bin/${b}")"
  case "${info}" in
    *golang.org/x/crypto/x509roots/fallback*) ;;
    *) die "${b} does not embed the Mozilla root CAs: cmd/${b} must import _ \"golang.org/x/crypto/x509roots/fallback\" (the image has no CA bundle)" ;;
  esac
  case "${b}" in
    myriarchd|myrctl) # both validate time zones of maintenance windows (internal/blueprint, internal/rollout)
      deps="$(cd "${ROOT}" && GOOS=linux GOARCH="${IMAGE_GOARCH}" CGO_ENABLED=0 go list -tags "${tags}" -deps "./cmd/${b}")"
      grep -qx 'time/tzdata' <<<"${deps}" || die "${b} does not embed the time zone database (import _ \"time/tzdata\"; the image has no zoneinfo files)"
      ;;
  esac
}

build_binaries() {
  local ldflags="-s -w -X ${VERSION_PKG}.Version=${VERSION} -X ${VERSION_PKG}.Commit=${COMMIT} -X ${VERSION_PKG}.BuildDate=${DATE}"
  local b tags
  have go || die "go not found"
  # the Go version ends up in the binaries (go version -m): the standard library of an old toolchain brings its known
  # vulnerabilities along (go.mod only demands go 1.26.0), so the log names the toolchain that builds the image
  log "toolchain: $(go version)"
  mkdir -p "${WORK}/bin"
  for b in myriarchd myriarch-sim myrctl; do
    tags=""
    if [ "${b}" = myriarchd ]; then tags="${MYRIARCHD_TAGS}"; fi # dev variant: `myriarchd dev` needs the embedded PostgreSQL (devpg)
    log "go build ${b} (${IMAGE_PLATFORM}, static${tags:+, tags: ${tags}})"
    ( cd "${ROOT}" && CGO_ENABLED=0 GOOS=linux GOARCH="${IMAGE_GOARCH}" go build -trimpath -tags "${tags}" -ldflags "${ldflags}" -o "${WORK}/bin/${b}" "./cmd/${b}" )
    check_embedded_data "${b}" "${tags}"
  done
}

# ---- layer --------------------------------------------------------------------------------------------------------
# Deterministic tar written by Python (no GNU/BSD tar differences): sorted entries, uid/gid 0, fixed mtime, fixed modes.
# One entry table serves both modes of the program below: "build" packs the layer (and checks the binaries), "list" prints the
# archive paths of all files of the layer except the binaries (no ELF check, nothing is built; used by `layout`).
# Arguments: build <root> <variant> <layer.tar> <epoch> <bindir> <elf machine> <platform> | list <root> <variant>
layer_py() {
  cat <<'PY'
import io, os, sys, tarfile

mode, root, variant = sys.argv[1], sys.argv[2], sys.argv[3]
if mode not in ("build", "list") or variant not in ("dev", "release"):
    raise SystemExit("usage: build|list <root> dev|release ...")

# archive path -> (kind, payload, mode); kind "dir" (no payload), "file" (payload = source path), "data" (payload = bytes)
entries = {}

def add_dir(path, mode=0o755):
    parts = path.strip("/").split("/")
    for i in range(1, len(parts)):
        entries.setdefault("/".join(parts[:i]), ("dir", None, 0o755))
    leaf = "/".join(parts)
    if leaf not in entries or mode != 0o755:
        entries[leaf] = ("dir", None, mode)

def add_file(arc, src, mode):
    add_dir(os.path.dirname(arc))
    entries[arc] = ("file", src, mode)

def add_data(arc, data, mode=0o644):
    add_dir(os.path.dirname(arc))
    entries[arc] = ("data", data, mode)

def skip(name):
    return name in (".DS_Store", "Thumbs.db") or name.startswith("._") or name.endswith("~") or name.endswith(".swp")

def add_tree(src_dir, arc_dir):
    if not os.path.isdir(src_dir):
        raise SystemExit("data directory missing: " + src_dir)
    add_dir(arc_dir)
    for dirpath, dirnames, filenames in os.walk(src_dir):
        dirnames[:] = sorted(d for d in dirnames if not skip(d))
        rel = os.path.relpath(dirpath, src_dir)
        base = arc_dir if rel == "." else arc_dir + "/" + rel.replace(os.sep, "/")
        add_dir(base)
        for f in sorted(filenames):
            if skip(f):
                continue
            p = os.path.join(dirpath, f)
            if os.path.islink(p) or not os.path.isfile(p):
                raise SystemExit("refusing non-regular file in data tree: " + p)
            add_file(base + "/" + f, p, 0o644)

def check_elf(path):
    """The binary must be a static ELF executable of the target platform (CGO_ENABLED=0, GOOS=linux, GOARCH of IMAGE_PLATFORM)."""
    with open(path, "rb") as f:
        h = f.read(64)
        if h[:4] != b"\x7fELF" or h[4] != 2 or h[5] != 1:
            raise SystemExit(path + ": not a 64-bit little endian ELF binary")
        if int.from_bytes(h[18:20], "little") != elf_machine:
            raise SystemExit(path + ": not a " + platform + " binary (e_machine %d, expected %d)" % (int.from_bytes(h[18:20], "little"), elf_machine))
        phoff = int.from_bytes(h[32:40], "little")
        phentsize = int.from_bytes(h[54:56], "little")
        phnum = int.from_bytes(h[56:58], "little")
        f.seek(phoff)
        for _ in range(phnum):
            ph = f.read(phentsize)
            if int.from_bytes(ph[0:4], "little") == 3:  # PT_INTERP: dynamically linked
                raise SystemExit(path + ": dynamically linked (static CGO_ENABLED=0 build expected)")

if mode == "build":
    out, epoch, bindir = sys.argv[4], int(sys.argv[5]), sys.argv[6]
    elf_machine, platform = int(sys.argv[7]), sys.argv[8]
    for b in ("myriarchd", "myriarch-sim", "myrctl"):
        p = os.path.join(bindir, b)
        check_elf(p)
        add_file("usr/local/bin/" + b, p, 0o755)

add_tree(os.path.join(root, "examples", "quickstart"), "opt/myriarch/examples/quickstart")
# the profile references components-*.json relative to its own directory: keep the directory together
add_tree(os.path.join(root, "examples", "sim"), "opt/myriarch/examples/sim")
# inputs of `myriarchd dev --releases-fixtures`: the dev variant only (compose and the end-to-end tests mount the repository copy)
if variant == "dev":
    add_tree(os.path.join(root, "internal", "release", "testdata", "fixtures"), "opt/myriarch/release-fixtures")

# License files (both variants): the project license and the notices of the third-party software in the binaries.
for name in ("LICENSE", "THIRD_PARTY_NOTICES"):
    src = os.path.join(root, name)
    if os.path.islink(src) or not os.path.isfile(src):
        raise SystemExit(name + " is missing in the repository root (THIRD_PARTY_NOTICES: scripts/gen-third-party-notices.sh)")
    add_file("usr/share/licenses/myriarch/" + name, src, 0o644)

# Users: there is no base image to bring /etc/passwd. User 65532 (the USER of the image) with a group of the same number.
# OpenShift's restricted SCC runs containers with an arbitrary UID in group 0 (that UID has no entry; Go does not need one):
# everything is world readable and nothing is written outside of volumes.
add_data("etc/passwd", b"root:x:0:0:root:/root:/sbin/nologin\nmyriarch:x:65532:65532:myriarch:/tmp:/sbin/nologin\n")
add_data("etc/group", b"root:x:0:\nmyriarch:x:65532:\n")
# empty, world writable with sticky bit (the manifests mount an emptyDir over it; plain `docker run` writes here)
add_dir("tmp", 0o1777)

if mode == "list":
    for arc in sorted(entries):
        if entries[arc][0] != "dir":
            print(arc)
    sys.exit(0)

with tarfile.open(out, "w", format=tarfile.PAX_FORMAT) as tar:
    for arc in sorted(entries):
        kind, payload, fmode = entries[arc]
        ti = tarfile.TarInfo(arc)
        ti.uid = ti.gid = 0
        ti.uname = ti.gname = "root"
        ti.mtime = epoch
        ti.mode = fmode
        if kind == "dir":
            ti.type = tarfile.DIRTYPE
            tar.addfile(ti)
        elif kind == "file":
            ti.type = tarfile.REGTYPE
            ti.size = os.path.getsize(payload)
            with open(payload, "rb") as fh:
                tar.addfile(ti, fh)
        else:
            ti.type = tarfile.REGTYPE
            ti.size = len(payload)
            tar.addfile(ti, io.BytesIO(payload))

files = sum(1 for k, _, _ in entries.values() if k != "dir")
print("layer: %d files, %d directories" % (files, len(entries) - files), file=sys.stderr)
PY
}

build_layer() {
  layer_py | python3 - build "${ROOT}" "${IMAGE_VARIANT}" "${WORK}/layer.tar" "${EPOCH}" "${WORK}/bin" "${IMAGE_ELF_MACHINE}" "${IMAGE_PLATFORM}" ||
    die "building the layer failed"
  LAYER_SHA="$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "${WORK}/layer.tar")"
  log "layer.tar sha256:${LAYER_SHA} ($(du -h "${WORK}/layer.tar" | cut -f1))"
}

# `layout`: the sorted archive paths of the files of the layer except the binaries (examples, release fixtures of the dev
# variant, license files, /etc/passwd and /etc/group). No network, no IMAGE_REPO, no repository state, no build.
cmd_layout() {
  image_variant_check
  have python3 || die "python3 not found"
  layer_py | python3 - list "${ROOT}" "${IMAGE_VARIANT}" || die "cannot list the layout of the ${IMAGE_VARIANT} variant"
}

# ---- labels -------------------------------------------------------------------------------------------------------
# One list for both ways of building the image (scratch layout and `crane mutate`). Truthful about the base: with scratch
# there is no base.digest and base.name says so; with a base image both come from that image.
compute_labels() {
  LABELS=(
    "org.opencontainers.image.title=${IMAGE_TITLE}"
    "org.opencontainers.image.description=${IMAGE_DESCRIPTION}"
    "org.opencontainers.image.vendor=${LABEL_VENDOR}"
    "org.opencontainers.image.licenses=${LABEL_LICENSES}"
    "org.opencontainers.image.revision=${FULL_SHA}"
    "org.opencontainers.image.version=${VERSION}"
    "org.opencontainers.image.created=${DATE}"
    "name=${IMAGE_TITLE}" "summary=${IMAGE_SUMMARY}"
    "description=${IMAGE_DESCRIPTION}"
    "version=${VERSION}" "release=${COMMIT}"
    "vcs-type=git" "vcs-ref=${FULL_SHA}" "build-date=${DATE}"
    "io.k8s.display-name=${IMAGE_TITLE}" "io.k8s.description=${IMAGE_DESCRIPTION}"
    "io.openshift.tags=myriarch opamp observability"
    "vendor=${LABEL_VENDOR}"
  )
  if [ -n "${SOURCE_URL}" ]; then
    LABELS+=("org.opencontainers.image.source=${SOURCE_URL}" "url=${SOURCE_URL}")
  fi
  if [ "${BASE_MODE}" = scratch ]; then
    LABELS+=(
      "org.opencontainers.image.base.name=scratch"
      "dev.myriarch.image.base=scratch"
      "dev.myriarch.image.variant=${IMAGE_VARIANT}"
      "dev.myriarch.image.contents=${IMAGE_CONTENTS}"
    )
  else
    LABELS+=(
      "org.opencontainers.image.base.name=${BASE_IMAGE%%@*}"
      "org.opencontainers.image.base.digest=${BASE_DIGEST}"
    )
  fi
}

# ---- image layout (scratch) ---------------------------------------------------------------------------------------
# Writes layer + config + manifest as an OCI image layout into ${WORK}/oci: everything is derived from the inputs (the layer
# bytes, the commit time, the labels), nothing from the build time, so the same commit gives the same layer, config and
# manifest digest. The layer is gzipped with a fixed header (no name, no timestamp, OS unknown) and zlib level 9; the
# uncompressed digest (IMAGE_LAYER_SHA256, the "diff id") does not depend on zlib at all. scripts/lib/oci_image.py writes it
# (the same code writes the layout of the release images).
build_image_layout() {
  local meta
  printf '%s\n' "${LABELS[@]}" > "${WORK}/labels.txt"
  python3 "${ROOT}/scripts/lib/oci_image.py" layout --out "${WORK}/oci" --created "${DATE}" --user "${IMAGE_UID}" \
    --entrypoint "${IMAGE_ENTRYPOINT}" --cmd "${IMAGE_CMD}" --env "HOME=/tmp,PATH=/usr/local/bin" --workdir "${IMAGE_WORKDIR}" \
    --ports "${IMAGE_PORTS}" --labels "${WORK}/labels.txt" \
    --history "scripts/build-image-crane.sh (FROM scratch): static binaries and data" \
    --platform "${IMAGE_PLATFORM}=${WORK}/layer.tar" > "${WORK}/layout.meta" || die "building the image layout failed"
  meta="$(cat "${WORK}/layout.meta")"
  local suffix="${IMAGE_PLATFORM/\//_}"
  LAYER_DIGEST="$(sed -n "s/^layer_digest\.${suffix}=//p" <<<"${meta}")"
  CONFIG_DIGEST="$(sed -n "s/^config_digest\.${suffix}=//p" <<<"${meta}")"
  MANIFEST_DIGEST="$(sed -n "s/^manifest_digest\.${suffix}=//p" <<<"${meta}")"
  [[ "${MANIFEST_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "unexpected manifest digest '${MANIFEST_DIGEST}'"
  log "image layout: layer ${LAYER_DIGEST} ($(sed -n "s/^layer_size\.${suffix}=//p" <<<"${meta}") bytes gzipped), config ${CONFIG_DIGEST}, manifest ${MANIFEST_DIGEST}"
}

# ---- push ---------------------------------------------------------------------------------------------------------
push_image() {
  local ref="${IMAGE_REPO}:${TAG}" out l
  local label_args=()
  image_docker_config_prepare
  log "docker config: $(image_docker_config_desc)"

  if [ "${BASE_MODE}" = scratch ]; then
    # one push of the finished image: blobs first, the manifest (and with it the tag) last
    log "crane push (FROM scratch, no base image) -> ${ref}"
    if ! out="$("${CRANE}" push "${WORK}/oci" "${ref}" 2>&1)"; then
      printf '%s\n' "${out}" >&2
      registry_hint
      die "pushing ${ref} failed"
    fi
  else
    BASE_DIGEST="$("${CRANE}" digest "${BASE_IMAGE}" 2>/dev/null)" || { registry_hint; die "base image ${BASE_IMAGE} is not reachable"; }
    [ "${BASE_DIGEST}" = "${BASE_IMAGE##*@}" ] || die "base image ${BASE_IMAGE}: the registry returned ${BASE_DIGEST}, which differs from the pinned digest"
    log "base image: ${BASE_IMAGE}"
    compute_labels
    for l in "${LABELS[@]}"; do label_args+=(--label "${l}"); done
    log "crane mutate --append ${BASE_IMAGE##*/} -> ${ref}"
    if ! out="$("${CRANE}" mutate "${BASE_IMAGE}" --platform "${IMAGE_PLATFORM}" \
        --append "${WORK}/layer.tar" \
        --entrypoint "${IMAGE_ENTRYPOINT}" --cmd "${IMAGE_CMD}" \
        --env "HOME=/tmp" --workdir "${IMAGE_WORKDIR}" --user "${IMAGE_UID}" \
        --exposed-ports "${IMAGE_PORTS}" \
        "${label_args[@]}" \
        --tag "${ref}" 2>&1)"; then
      printf '%s\n' "${out}" >&2
      registry_hint
      die "pushing ${ref} failed"
    fi
  fi
  # ask the registry for the authoritative digest of the tag (`crane push`/`crane mutate` print <repo>@<digest> as well)
  DIGEST="$("${CRANE}" digest "${ref}")"
  [[ "${DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "unexpected digest '${DIGEST}'"
  case "${out}" in *"${DIGEST}"*) ;; *) warn "digest printed by crane differs from the registry: ${out##*$'\n'} vs ${DIGEST}" ;; esac
  if [ "${BASE_MODE}" = scratch ] && [ "${DIGEST}" != "${MANIFEST_DIGEST}" ]; then
    warn "the registry reports ${DIGEST} but the image was built as ${MANIFEST_DIGEST} (manifest rewritten by the registry?): ${DIGEST} is the one to pin"
  fi
}

# Reads the pushed config back and checks the contract; VERIFY=1 additionally lists the new layer.
verify_image() {
  local pinned="${IMAGE_REPO}@${DIGEST}"
  "${CRANE}" config "${pinned}" | python3 -c '
import json, sys
c = json.load(sys.stdin)
cfg = c["config"]
errs = []
if c.get("os") != "linux" or c.get("architecture") != sys.argv[5]: errs.append("platform %s/%s" % (c.get("os"), c.get("architecture")))
if cfg.get("Entrypoint") != [sys.argv[2]]: errs.append("entrypoint %r" % cfg.get("Entrypoint"))
if cfg.get("User") != sys.argv[1]: errs.append("user %r" % cfg.get("User"))
if cfg.get("WorkingDir") != sys.argv[3]: errs.append("workdir %r" % cfg.get("WorkingDir"))
if "HOME=/tmp" not in (cfg.get("Env") or []): errs.append("env HOME")
if sys.argv[4] == "scratch":
    if len(c["rootfs"]["diff_ids"]) != 1: errs.append("%d layers (scratch image: exactly one)" % len(c["rootfs"]["diff_ids"]))
    if (cfg.get("Labels") or {}).get("org.opencontainers.image.base.name") != "scratch": errs.append("label base.name")
if errs:
    print("[image] ERROR: pushed image does not match the contract: " + "; ".join(errs), file=sys.stderr); sys.exit(1)
print("[image] config ok: %s/%s user=%s entrypoint=%s cmd=%s workdir=%s layers=%d" % (c["os"], c["architecture"], cfg["User"], cfg["Entrypoint"], cfg.get("Cmd"), cfg.get("WorkingDir"), len(c["rootfs"]["diff_ids"])), file=sys.stderr)
' "${IMAGE_UID}" "${IMAGE_ENTRYPOINT}" "${IMAGE_WORKDIR}" "${BASE_MODE}" "${IMAGE_GOARCH}" || die "verification of the pushed image failed"
  if [ "${VERIFY}" = 1 ]; then
    local layer
    layer="$("${CRANE}" manifest "${pinned}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["layers"][-1]["digest"])')"
    log "files of the new layer ${layer}:"
    "${CRANE}" blob "${IMAGE_REPO}@${layer}" | tar -tvzf - >&2
  fi
}

cmd_build() {
  image_variant_check
  image_platform_check
  image_require_repo scripts/build-image-crane.sh "registry.example.com/<org>/myriarch"
  image_publish_check
  if [ "${BASE_MODE}" != scratch ]; then image_require_digest_pin BASE_IMAGE "${BASE_IMAGE}"; fi
  image_labels_load
  IMAGE_TITLE="${LABEL_TITLE}"
  have python3 || die "python3 not found (builds the deterministic layer tar)"
  git_metadata
  log "version ${VERSION} (commit ${COMMIT}, ${DATE}) -> ${IMAGE_REPO}:${TAG} (variant ${IMAGE_VARIANT})"
  if [ "${BASE_MODE}" = scratch ]; then log "base: scratch (no OS packages)"; else warn "base image ${BASE_IMAGE}: the image will contain its OS packages"; fi
  ensure_ui
  if [ "${DRY_RUN}" != 1 ]; then resolve_crane; fi
  if [ "${DRY_RUN}" = 1 ]; then
    WORK="${OUT_DIR:-${ROOT}/.cache/image}"; rm -rf "${WORK}"; mkdir -p "${WORK}"; KEEP_WORK=1
  else
    WORK="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-image.XXXXXX")"
  fi
  build_binaries
  build_layer
  if [ "${BASE_MODE}" = scratch ]; then
    compute_labels
    build_image_layout
  fi
  if [ "${DRY_RUN}" = 1 ]; then
    log "DRY_RUN=1: nothing was pushed (layer: ${WORK}/layer.tar)"
    if [ "${BASE_MODE}" = scratch ]; then log "DRY_RUN=1: image layout (config, manifest, gzipped layer) in ${WORK}/oci: crane push ${WORK}/oci <repo>:<tag>"; fi
    printf 'IMAGE_LAYER_SHA256=%s\n' "${LAYER_SHA}"
    if [ "${BASE_MODE}" = scratch ]; then
      printf 'IMAGE_LAYER_DIGEST=%s\n' "${LAYER_DIGEST}"
      printf 'IMAGE_MANIFEST_DIGEST=%s\n' "${MANIFEST_DIGEST}"
    fi
    return 0
  fi
  push_image
  verify_image
  log "pushed ${IMAGE_REPO}:${TAG}"
  log "digest ${DIGEST}"
  log "pin the digest in your deployment, e.g. kustomize images: newName/digest or Helm image.digest"
  printf 'IMAGE_REF=%s:%s\n' "${IMAGE_REPO}" "${TAG}"
  printf 'IMAGE_DIGEST=%s\n' "${DIGEST}"
  printf 'IMAGE_PINNED=%s@%s\n' "${IMAGE_REPO}" "${DIGEST}"
  printf 'IMAGE_LAYER_SHA256=%s\n' "${LAYER_SHA}"
  if [ "${BASE_MODE}" = scratch ]; then printf 'IMAGE_LAYER_DIGEST=%s\n' "${LAYER_DIGEST}"; fi
}

# ---- release images (IMAGE_SOURCE=dist) -----------------------------------------------------------------------------------
resolve_syft() {
  if [ -n "${SYFT:-}" ]; then
    [ -x "${SYFT}" ] || die "SYFT=${SYFT} is not executable"
    return 0
  fi
  # the pinned release of ci/versions.env, verified against its SHA-256 (ci/install-tools.sh)
  TOOLS_BIN="${TOOLS_BIN}" "${ROOT}/ci/install-tools.sh" syft >&2 && [ -x "${TOOLS_BIN}/syft" ] ||
    die "syft not available: run ci/install-tools.sh syft (the SBOMs of the images come from the pinned syft; IMAGE_SBOM=0 skips them)"
  SYFT="${TOOLS_BIN}/syft"
}

# dist_version: the release version of the archives (sets RELEASE_VERSION). When DIST_DIR/metadata.json (goreleaser) exists,
# its commit must be the commit of the work tree and its version the release version.
dist_version() {
  local meta="${DIST_DIR}/metadata.json" from_meta="" meta_commit=""
  [ -d "${DIST_DIR}" ] || die "DIST_DIR ${DIST_DIR} does not exist: run the release build first (make release-snapshot, or goreleaser for a release)"
  if [ -f "${meta}" ]; then
    from_meta="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1])).get("version", ""))' "${meta}")" || die "cannot read ${meta}"
    meta_commit="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1])).get("commit", ""))' "${meta}")" || die "cannot read ${meta}"
    [ "${meta_commit}" = "${FULL_SHA}" ] ||
      die "${meta} was written for commit ${meta_commit:-none}, the work tree is at ${FULL_SHA}: build the archives and the images from the same commit"
  fi
  RELEASE_VERSION="${IMAGE_RELEASE_VERSION:-${from_meta}}"
  [ -n "${RELEASE_VERSION}" ] || die "no release version: set IMAGE_RELEASE_VERSION or provide ${meta}"
  [[ "${RELEASE_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "invalid release version '${RELEASE_VERSION}' (X.Y.Z or X.Y.Z-rc.N, without v)"
  if [ -n "${from_meta}" ] && [ "${RELEASE_VERSION}" != "${from_meta}" ]; then
    die "IMAGE_RELEASE_VERSION ${RELEASE_VERSION} differs from the version of ${meta} (${from_meta})"
  fi
}

# the labels that the index repeats as annotations (ghcr.io takes the metadata of a multi-platform package from the index)
dist_annotations() {
  local l key
  for l in "${LABELS[@]}"; do
    key="${l%%=*}"
    case "${key}" in
      org.opencontainers.image.title | org.opencontainers.image.description | org.opencontainers.image.source | \
        org.opencontainers.image.version | org.opencontainers.image.revision | org.opencontainers.image.created | \
        org.opencontainers.image.vendor | org.opencontainers.image.licenses) printf '%s\n' "${l}" ;;
    esac
  done
}

cmd_dist() {
  local layout out plat suffix meta platforms=() seen="" sbom_dir sum upper
  local plat_args=() strict_vcs="--strict-vcs"
  case "${IMAGE_KIND}" in
    server)
      IMAGE_REPO_NAME="ghcr.io/myriarch/myriarch"
      IMAGE_DESCRIPTION="OpAMP fleet manager for OpenTelemetry Collectors - myriarchd"
      IMAGE_SUMMARY="OpAMP fleet manager for OpenTelemetry Collectors"
      IMAGE_CONTENTS="the static Go binary myriarchd and the license files only; no OS packages; no shell; root CAs and time zones are embedded in the binary" ;;
    updater)
      IMAGE_REPO_NAME="ghcr.io/myriarch/myriarch-updater"
      IMAGE_DESCRIPTION="Host agent that installs OpenTelemetry Collector binary updates - myriarch-updater"
      IMAGE_SUMMARY="Collector binary updater for the myriarch fleet manager"
      IMAGE_CONTENTS="the static Go binary myriarch-updater and the license files only; no OS packages; no shell; root CAs are embedded in the binary" ;;
    "") die "IMAGE_KIND is not set: IMAGE_SOURCE=dist builds the server image (IMAGE_KIND=server) or the updater image (IMAGE_KIND=updater)" ;;
    *) die "invalid IMAGE_KIND '${IMAGE_KIND}' (server or updater)" ;;
  esac
  IFS=, read -r -a platforms <<<"${IMAGE_PLATFORMS}"
  [ "${#platforms[@]}" -ge 1 ] || die "IMAGE_PLATFORMS is empty"
  for plat in "${platforms[@]}"; do
    case "${plat}" in
      linux/amd64 | linux/arm64) ;;
      *) die "unsupported platform '${plat}' in IMAGE_PLATFORMS (linux/amd64 or linux/arm64)" ;;
    esac
    case " ${seen} " in *" ${plat} "*) die "platform ${plat} occurs twice in IMAGE_PLATFORMS" ;; esac
    seen="${seen} ${plat}"
  done
  case "${IMAGE_SBOM}" in 0 | 1) ;; *) die "IMAGE_SBOM must be 0 or 1" ;; esac
  image_labels_load
  case "${IMAGE_KIND}" in server) IMAGE_TITLE="${LABEL_TITLE}" ;; updater) IMAGE_TITLE="${LABEL_TITLE_UPDATER}" ;; esac
  have python3 || die "python3 not found (builds the deterministic layers)"
  have go || die "go not found (the build information of the binaries is checked with go version -m)"
  git_state
  if [ "${DIRTY}" = 1 ] && [ "${ALLOW_DIRTY}" != 1 ]; then
    git -C "${ROOT}" status --short >&2
    die "the work tree has uncommitted changes: release images are built from a committed tree (ALLOW_DIRTY=1 for a local test)"
  fi
  # a release binary records the clean commit it was built from; a local test (ALLOW_DIRTY=1) may use a dirty tree
  if [ "${ALLOW_DIRTY}" = 1 ]; then strict_vcs=""; fi
  dist_version
  VERSION="${RELEASE_VERSION}"
  IMAGE_VARIANT=release
  # not the git remote: a release image must not depend on the checkout it is built in
  SOURCE_URL="${SOURCE_URL:-${LABEL_SOURCE}}"
  case "${SOURCE_URL}" in *,*) die "SOURCE_URL must not contain a comma (crane parses --label as CSV)" ;; esac

  out="${OUT_DIR:-${ROOT}/.cache/image-dist}"
  [ -n "${out}" ] || die "OUT_DIR is empty"
  layout="${out}/${IMAGE_KIND}"
  sbom_dir="${out}/${IMAGE_KIND}.sbom"
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-image.XXXXXX")"
  # recreate the outputs of this kind (IMAGE_KIND is server or updater here, so the paths are below OUT_DIR)
  rm -rf "${layout}" "${sbom_dir}"
  rm -f "${layout}.env"
  mkdir -p "${out}"
  log "release image ${IMAGE_REPO_NAME} ${RELEASE_VERSION} (commit ${COMMIT}, ${DATE}) from ${DIST_DIR}: ${IMAGE_PLATFORMS}"

  compute_labels
  printf '%s\n' "${LABELS[@]}" > "${WORK}/labels.txt"
  dist_annotations > "${WORK}/annotations.txt"
  for plat in "${platforms[@]}"; do plat_args+=(--platform "${plat}"); done
  python3 "${ROOT}/scripts/lib/oci_image.py" dist --kind "${IMAGE_KIND}" --dist "${DIST_DIR}" --version "${RELEASE_VERSION}" \
    --epoch "${EPOCH}" --created "${DATE}" --uid "${IMAGE_UID}" --out "${layout}" --labels "${WORK}/labels.txt" \
    --annotations "${WORK}/annotations.txt" --revision "${FULL_SHA}" ${strict_vcs} "${plat_args[@]}" > "${WORK}/layout.meta" ||
    die "building the release image failed"
  meta="$(cat "${WORK}/layout.meta")"
  IMAGE_INDEX_DIGEST="$(sed -n 's/^index_digest=//p' <<<"${meta}")"
  [[ "${IMAGE_INDEX_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "unexpected index digest '${IMAGE_INDEX_DIGEST}'"
  sum="$(python3 -I -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "${layout}/index.json")"
  [ "sha256:${sum}" = "${IMAGE_INDEX_DIGEST}" ] || die "internal error: index.json hashes to sha256:${sum}, not ${IMAGE_INDEX_DIGEST}"

  {
    printf 'IMAGE_KIND=%s\n' "${IMAGE_KIND}"
    printf 'IMAGE_REPO_NAME=%s\n' "${IMAGE_REPO_NAME}"
    printf 'IMAGE_RELEASE_VERSION=%s\n' "${RELEASE_VERSION}"
    printf 'IMAGE_LAYOUT=%s\n' "${layout}"
    printf 'IMAGE_INDEX_DIGEST=%s\n' "${IMAGE_INDEX_DIGEST}"
  } > "${WORK}/summary.env"
  for plat in "${platforms[@]}"; do
    suffix="${plat/\//_}"
    upper="$(printf '%s' "${suffix}" | tr '[:lower:]' '[:upper:]')"
    printf 'IMAGE_MANIFEST_DIGEST_%s=%s\n' "${upper}" "$(sed -n "s/^manifest_digest\\.${suffix}=//p" <<<"${meta}")" >> "${WORK}/summary.env"
  done

  if [ "${IMAGE_SBOM}" = 1 ]; then
    resolve_syft
    mkdir -p "${sbom_dir}"
    for plat in "${platforms[@]}"; do
      suffix="${plat/\//_}"
      upper="$(printf '%s' "${suffix}" | tr '[:lower:]' '[:upper:]')"
      log "SBOM ${plat}"
      # no update check; the cache stays out of the home directory
      SYFT_CHECK_FOR_APP_UPDATE=false SYFT_CACHE_DIR="${WORK}/syft-cache" "${SYFT}" scan "oci-dir:${layout}" --platform "${plat}" \
        --source-name "${IMAGE_REPO_NAME}" --source-version "$(sed -n "s/^manifest_digest\\.${suffix}=//p" <<<"${meta}")" \
        -o "spdx-json=${sbom_dir}/${suffix}.spdx.json" -q >&2 || die "syft failed for ${plat}"
      [ -s "${sbom_dir}/${suffix}.spdx.json" ] || die "syft wrote no SBOM for ${plat}"
      printf 'IMAGE_SBOM_%s=%s\n' "${upper}" "${sbom_dir}/${suffix}.spdx.json" >> "${WORK}/summary.env"
    done
  fi

  cp "${WORK}/summary.env" "${layout}.env"
  cat "${WORK}/summary.env"
  log "layout ${layout}: push it with ci/release/push-images.sh ${layout} <repository>@${IMAGE_INDEX_DIGEST}"
}

case "${1:-build}" in
  build)
    case "${IMAGE_SOURCE}" in
      build) cmd_build ;;
      dist) cmd_dist ;;
      *) die "invalid IMAGE_SOURCE '${IMAGE_SOURCE}' (build or dist)" ;;
    esac ;;
  layout) cmd_layout ;;
  login) shift; cmd_login "$@" ;;
  -h|--help|help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//' ;;
  *) die "unknown command '$1' (build | layout | login <user> | help)" ;;
esac
