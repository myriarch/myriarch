#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds and pushes the myriarch COLLECTOR image WITHOUT a container runtime (no Docker, Podman or Buildah daemon):
# the upstream OpAMP supervisor image plus the upstream otelcol-contrib binary of the SAME version in one image
# (two processes in one container: the supervisor configures, starts and restarts the collector).
#
#   IMAGE_REPO=registry.example.com/<org>/myriarch-collector contrib/legacy-kubernetes-collector/build-collector-image-crane.sh 0.161.0
#                                                                build + push  <IMAGE_REPO>:0.161.0-<git short sha>
#   IMAGE_REPO=... DRY_RUN=1 contrib/legacy-kubernetes-collector/build-collector-image-crane.sh 0.161.0
#                                                                read + check the upstream images, build the layer, push nothing
#   IMAGE_REPO=... WITH_UPDATER=0 contrib/legacy-kubernetes-collector/build-collector-image-crane.sh 0.162.0
#                                                                the previous image without myriarch-updater (tag <version>)
#   IMAGE_REPO=localhost:5000/myriarch-collector contrib/legacy-kubernetes-collector/build-collector-image-crane.sh 0.162.0
#                                                                local registry (crane registry serve)
#   IMAGE_REPO=<registry>/<path> contrib/legacy-kubernetes-collector/build-collector-image-crane.sh login USER
#                                                                one-time registry login (same as scripts/build-image-crane.sh login)
#   contrib/legacy-kubernetes-collector/build-collector-image-crane.sh layout                print the archive paths of the files of the new layer except the
#                                                                binaries (the license files); needs no IMAGE_REPO and no network,
#                                                                builds nothing (WITH_UPDATER does not change the list)
#
# What it does
#   1. pins the two upstream images by digest (logged, and written into image labels):
#        base     otel/opentelemetry-collector-opampsupervisor:<version>   alpine + /usr/local/bin/opampsupervisor
#        contrib  otel/opentelemetry-collector-contrib:<version>           scratch + /otelcol-contrib
#      and checks that both really are <version> (image label AND Go build info of both binaries): supervisor and
#      collector must have the same version;
#   2. extracts /otelcol-contrib (IMAGE_PLATFORM, default linux/amd64) from the contrib image and checks that it is a static ELF binary of that platform;
#   3. packs it together with the directories the supervisor needs and the license files (see the image contract) into ONE
#      deterministic layer tar;
#   3b. WITH_UPDATER=1 (default): builds myriarch-updater from this repository (static, IMAGE_PLATFORM, `go build -trimpath`, the same
#      -X version flags as the Makefile) and packs it into the same layer (container mode, contrib/legacy-kubernetes-collector/kustomize/README.md);
#   4. `crane mutate --append` puts the layer on top of the supervisor image (IMAGE_PLATFORM) and sets user, entrypoint, cmd,
#      workdir, env and labels in the same push to IMAGE_REPO:TAG (never a half-finished image under the tag);
#   5. reads the pushed config back (contract check, new diff id = layer built here) and runs `crane validate --remote`;
#   6. prints the digest. Lines starting with IMAGE_ on stdout are machine readable (everything else goes to stderr).
#
# Image contract (mirrors contrib/legacy-kubernetes-collector/kustomize/Dockerfile; what deploy manifests may rely on)
#   /usr/local/bin/opampsupervisor    from the upstream supervisor image (ENTRYPOINT opampsupervisor)
#   /usr/local/bin/otelcol-contrib    from the upstream contrib image, 0755 (agent.executable of the supervisor config; in container
#                                     mode it is only the SEED: myriarch-updater copies it to the volume, see below)
#   /usr/local/bin/myriarch-updater      (WITH_UPDATER=1) 0755, `myriarch-updater container --config <file>` = PID 1 of the container
#                                     (the image ENTRYPOINT stays opampsupervisor: old manifests keep working, container mode is
#                                     switched on with `command:` in the manifest)
#   /var/lib/myriarch                (WITH_UPDATER=1) mount point of the volume with the collector binaries (versions/<v> + current
#                                     symlink) and the updater state; owned 10001:0, mode 0775 like the supervisor storage
#   /etc/myriarch/trust              (WITH_UPDATER=1) mount point of trusted_root.json (offline Sigstore verification)
#   /var/lib/otelcol/supervisor       storage of the supervisor (persistent_state.yaml = instance UID, effective config, ...):
#                                     mount a volume here (PVC), otherwise every container start is a new agent
#   /etc/opampsupervisor, /etc/myriarch   mount points for supervisor.yaml and the fallback collector config
#   licenses (0644 root:root; the build fails if a source file is missing):
#     /usr/share/licenses/myriarch/LICENSE, /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES
#                                     the project license and the notices of the third-party software in myriarch-updater
#     /usr/share/licenses/opentelemetry-collector/LICENSE
#                                     Apache-2.0, a copy of the repository LICENSE (the text equals the upstream LICENSE of
#                                     opentelemetry-collector-contrib and -releases apart from one leading empty line)
#     /usr/share/licenses/opentelemetry-collector/README
#                                     names otelcol-contrib and opampsupervisor <version>, "Copyright The OpenTelemetry Authors",
#                                     Apache-2.0, the upstream repositories and tags and the digests of the two upstream images
#                                     this image was built from (generated here; contrib/legacy-kubernetes-collector/kustomize/Dockerfile writes the same
#                                     text with the image references of its build arguments)
#   writable for an arbitrary UID: OpenShift (restricted-v2) starts the container with a UID from the project range and
#   GID 0, so /var/lib/otelcol and /var/lib/otelcol/supervisor are owned by 10001:0 and mode 0775 (group 0 can write);
#   everything else is root owned and not writable: run with readOnlyRootFilesystem and volumes for the storage and /tmp.
#   USER 10001:10001 (NUMERIC; the upstream image names the user "opampsupervisor", which `runAsNonRoot` cannot verify),
#   WORKDIR /var/lib/otelcol, ENTRYPOINT ["opampsupervisor"], CMD ["--config=/etc/opampsupervisor/supervisor.yaml"],
#   ENV HOME=/tmp (and PATH of the base), ports 4317/4318 (OTLP), 13133 (collector health_check), 13134 (supervisor healthcheck).
#   Labels: org.opencontainers.image.* (version = collector version, base.name/base.digest = supervisor image),
#   dev.myriarch.collector.* (version, contrib image digest, sha256 of both binaries), dev.myriarch.updater.* (version, commit,
#   sha256 of myriarch-updater), io.k8s.* / io.openshift.*.
#
# Why the pieces are not "built": there is no official image with both binaries
# (docs/design/upstream-notes.md, Release assets and signatures > Container images), and the binaries
# of the upstream images are the signed release artifacts (contrib/enterprise-mirror/verify-upstream-images.sh proves that bit by bit). This
# script only assembles them; it does not compile anything.
#
# Environment (all optional)
#   IMAGE_REPO        target repository (required, no default): <registry>[:port]/<path> without tag or digest
#   TAG               image tag                              default <version>-<git short sha> (+ "-dirty" for a modified tree);
#                                                            <version> with WITH_UPDATER=0; "-arm64" is appended for arm64
#   IMAGE_PLATFORM    linux/amd64 (default) | linux/arm64    platform of the single-platform image (both upstream images must
#                                                            provide it); arm64 tags get the suffix "-arm64", so they never
#                                                            overwrite the amd64 tag
#   WITH_UPDATER      1 = also pack myriarch-updater            default 1
#   UPDATER_VERSION   version string linked into myriarch-updater   default `git describe --tags --always --dirty`
#   UPDATER_COMMIT    commit linked into myriarch-updater       default `git rev-parse --short HEAD`
#   GO                go binary used to build myriarch-updater  default go
#   SUPERVISOR_IMAGE  base image                             default OTELCOL_SUPERVISOR_IMAGE of ci/versions.env (reviewed digest) for
#   CONTRIB_IMAGE     collector image                        its OTELCOL_VERSION, else otel/opentelemetry-collector-*:<version> (tag
#                     resolved and pinned now). An override must carry @sha256:... (a mirror is fine); ALLOW_UNPINNED_IMAGES=1
#                     allows a tag for a lab build.
#   DRY_RUN           1 = stop before the push (layer.tar and the extracted binaries, ~800 MB, stay in ./.cache/collector-image-<version>)
#   VERIFY            1 = after the push also list the files of the new layer from the registry
#   VERIFY_UPSTREAM   run contrib/enterprise-mirror/verify-upstream-images.sh for the pinned digests: the binaries must be
#                     bit-identical to the release artifacts and their SHA-256 + Sigstore bundle must be valid
#                     (default 1 for a build that pushes, 0 with DRY_RUN=1; set 0 explicitly to skip it for a lab build)
#                     (needs cosign: TOOLS_BIN=<dir> ci/install-tools.sh cosign; downloads ~130 MB from GitHub)
#   DOCKER_CONFIG     directory of crane's registry credentials (config.json); respected, never set by this script; crane's
#                     default is ~/.docker. Point it at a dedicated directory if ~/.docker/config.json names a credential helper
#                     that is not installed. Docker Hub is read anonymously.
#   CRANE             path of the crane binary               default .cache/bin/crane (built with go build from ci/tools/crane)
#   SOURCE_URL        org.opencontainers.image.source        default: https form of `git remote get-url origin`, else LABEL_SOURCE
#                                                            of ci/image-labels.env, else no label
#   SOURCE_DATE_EPOCH timestamp of all layer entries         default creation time of the contrib image (same inputs => same layer)
#   KEEP_WORK         1 = keep the temporary build directory (printed at the end)
#
# Labels that are the same for every image of the project (vendor, licenses, titles) come from ci/image-labels.env.
#
# Licensing of what is distributed: this image is built by the user into the user's own IMAGE_REPO and is not published by the
# project. The upstream OpenTelemetry binaries are Apache-2.0 (LICENSE and README above). The alpine base of the upstream supervisor
# image brings its own packages with their own licenses; whoever distributes the image takes on the obligations of those
# licenses (deploy/README.md, License files). For that reason the image has no org.opencontainers.image.licenses label.
#
# One-time login (token via stdin so it never shows up in the process list):
#   IMAGE_REPO=<registry>/<path> contrib/legacy-kubernetes-collector/build-collector-image-crane.sh login '<registry-user>' < token-file
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"

IMAGE_REPO="${IMAGE_REPO:-}"
DRY_RUN="${DRY_RUN:-0}"
VERIFY="${VERIFY:-0}"
VERIFY_UPSTREAM="${VERIFY_UPSTREAM:-}"   # empty = default: 1 for a build that pushes, 0 for DRY_RUN=1 (see cmd_build)
KEEP_WORK="${KEEP_WORK:-0}"
WITH_UPDATER="${WITH_UPDATER:-1}"
GO="${GO:-go}"
PLATFORM="linux/amd64" # replaced by IMAGE_PLATFORM (image_platform_check, after image-common.sh is sourced)
# the image contract (the same values as contrib/legacy-kubernetes-collector/kustomize/Dockerfile; lists are comma separated like the crane flags)
IMAGE_UID="10001"
IMAGE_USER="10001:10001"
IMAGE_ENTRYPOINT="opampsupervisor"
IMAGE_CMD="--config=/etc/opampsupervisor/supervisor.yaml"
IMAGE_WORKDIR="/var/lib/otelcol"
IMAGE_PORTS="4317/tcp,4318/tcp,13133/tcp,13134/tcp"

log()  { printf '[collector-image] %s\n' "$*" >&2; }
warn() { printf '[collector-image] WARNING: %s\n' "$*" >&2; }
die()  { printf '[collector-image] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# shellcheck source=scripts/lib/image-common.sh
. "${ROOT}/scripts/lib/image-common.sh"
image_platform_check
PLATFORM="${IMAGE_PLATFORM}"

WORK=""
cleanup() {
  if [ -n "${WORK}" ] && [ -d "${WORK}" ]; then
    if [ "${KEEP_WORK}" = 1 ] || [ "${DRY_RUN}" = 1 ]; then log "build directory kept: ${WORK}"; else rm -rf "${WORK}"; fi
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
[collector-image] If a pull from the upstream registry failed: Docker Hub reachable (proxy? anonymous rate limit)? An internal
[collector-image] mirror works as well: SUPERVISOR_IMAGE=<mirror>/opampsupervisor:<version> CONTRIB_IMAGE=<mirror>/contrib:<version>.
[collector-image] If the push failed with UNAUTHORIZED / DENIED, you are not logged in or have no write permission:
[collector-image]   IMAGE_REPO=${IMAGE_REPO} contrib/legacy-kubernetes-collector/build-collector-image-crane.sh login '<registry-user>' < token-file     (stored in $(image_docker_config_desc))
EOF
}

# ---- helpers ------------------------------------------------------------------------------------------------------
# repository part of an image reference without ":tag" (a registry port such as localhost:5000/x is kept)
strip_tag() { # ref
  local r="$1" last="${1##*/}"
  case "${last}" in
    *:*) printf '%s' "${r%:*}" ;;
    *)   printf '%s' "${r}" ;;
  esac
}

# pin_ref REF -> REPOSITORY@sha256:... (the digest of the manifest list / manifest the tag points to right now)
pin_ref() {
  local ref="$1" d
  case "${ref}" in
    *@sha256:????????????????????????????????????????????????????????????????)
      # already pinned (also repo:tag@sha256:...): keep exactly this digest
      printf '%s@%s' "$(strip_tag "${ref%@*}")" "${ref##*@}"
      return 0 ;;
  esac
  d="$("${CRANE}" digest "${ref}")" || { registry_hint; die "cannot resolve ${ref} ('crane digest ${ref}' failed)"; }
  [[ "${d}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "unexpected digest '${d}' for ${ref}"
  printf '%s@%s' "$(strip_tag "${ref}")" "${d}"
}

sha256_of() { # file
  python3 -c 'import hashlib,sys
h = hashlib.sha256()
with open(sys.argv[1], "rb") as f:
    for chunk in iter(lambda: f.read(1 << 20), b""):
        h.update(chunk)
print(h.hexdigest())' "$1"
}

# config_field REF PYTHON_EXPR -> value of an expression over the image config c (platform IMAGE_PLATFORM), e.g. c["config"]["User"]
config_field() { # ref expr
  "${CRANE}" config --platform "${PLATFORM}" "$1" | python3 -c 'import json,sys
c = json.load(sys.stdin)
print(eval(sys.argv[1]))' "$2"
}

# The binary must be a static ELF executable of the target platform (no dynamic loader needed).
check_elf() { # file
  python3 - "$1" "${IMAGE_ELF_MACHINE}" "${PLATFORM}" <<'PY' || die "$1 is not a static ${PLATFORM} ELF executable"
import sys
with open(sys.argv[1], "rb") as f:
    h = f.read(64)
    if h[:4] != b"\x7fELF" or h[4] != 2 or h[5] != 1:
        raise SystemExit(sys.argv[1] + ": not a 64-bit little endian ELF binary")
    if int.from_bytes(h[18:20], "little") != int(sys.argv[2]):
        raise SystemExit(sys.argv[1] + ": not a " + sys.argv[3] + " binary")
    phoff = int.from_bytes(h[32:40], "little")
    phentsize = int.from_bytes(h[54:56], "little")
    phnum = int.from_bytes(h[56:58], "little")
    f.seek(phoff)
    for _ in range(phnum):
        ph = f.read(phentsize)
        if int.from_bytes(ph[0:4], "little") == 3:  # PT_INTERP: dynamically linked
            raise SystemExit(sys.argv[1] + ": dynamically linked")
PY
}

# ---- upstream inputs ----------------------------------------------------------------------------------------------
# Pins both images, checks their versions, extracts the binaries. Sets SUPERVISOR_PINNED, CONTRIB_PINNED, *_PLATFORM_DIGEST,
# BASE_LAYERS, EPOCH, DATE, CONTRIB_SHA, SUPERVISOR_SHA.
fetch_inputs() {
  local v lv created ver_re
  SUPERVISOR_PINNED="$(pin_ref "${SUPERVISOR_IMAGE}")"
  CONTRIB_PINNED="$(pin_ref "${CONTRIB_IMAGE}")"
  SUPERVISOR_PLATFORM_DIGEST="$("${CRANE}" digest --platform "${PLATFORM}" "${SUPERVISOR_PINNED}" 2>/dev/null || true)"
  CONTRIB_PLATFORM_DIGEST="$("${CRANE}" digest --platform "${PLATFORM}" "${CONTRIB_PINNED}" 2>/dev/null || true)"
  log "base     ${SUPERVISOR_PINNED}  (${PLATFORM}: ${SUPERVISOR_PLATFORM_DIGEST:-n/a})"
  log "contrib  ${CONTRIB_PINNED}  (${PLATFORM}: ${CONTRIB_PLATFORM_DIGEST:-n/a})"

  # 1. the image labels name the release (images of a mirror may lack them: then only the build info below counts)
  for v in "${SUPERVISOR_PINNED}" "${CONTRIB_PINNED}"; do
    lv="$(config_field "${v}" 'c["config"].get("Labels", {}).get("org.opencontainers.image.version", "")')" || die "cannot read the config of ${v}"
    if [ -n "${lv}" ] && [ "${lv}" != "${VERSION}" ]; then
      die "${v} is version ${lv}, not ${VERSION} (supervisor and collector must have the same version)"
    fi
  done
  BASE_LAYERS="$(config_field "${SUPERVISOR_PINNED}" 'len(c["rootfs"]["diff_ids"])')"
  [ "$(config_field "${SUPERVISOR_PINNED}" 'c["os"] + "/" + c["architecture"]')" = "${PLATFORM}" ] || die "${SUPERVISOR_PINNED} has no ${PLATFORM} image"
  created="$(config_field "${CONTRIB_PINNED}" 'c.get("created", "")')"

  # 2. the binaries
  mkdir -p "${WORK}/in"
  log "extracting /otelcol-contrib (~400 MB) from the contrib image and /usr/local/bin/opampsupervisor from the base image"
  "${CRANE}" export --platform "${PLATFORM}" "${CONTRIB_PINNED}" - | tar -xOf - otelcol-contrib > "${WORK}/in/otelcol-contrib" \
    || { registry_hint; die "/otelcol-contrib not found in ${CONTRIB_PINNED}"; }
  [ -s "${WORK}/in/otelcol-contrib" ] || die "/otelcol-contrib in ${CONTRIB_PINNED} is empty"
  "${CRANE}" export --platform "${PLATFORM}" "${SUPERVISOR_PINNED}" - | tar -xOf - usr/local/bin/opampsupervisor > "${WORK}/in/opampsupervisor" \
    || { registry_hint; die "/usr/local/bin/opampsupervisor not found in ${SUPERVISOR_PINNED}"; }
  [ -s "${WORK}/in/opampsupervisor" ] || die "/usr/local/bin/opampsupervisor in ${SUPERVISOR_PINNED} is empty"
  check_elf "${WORK}/in/otelcol-contrib"
  check_elf "${WORK}/in/opampsupervisor"
  CONTRIB_SHA="$(sha256_of "${WORK}/in/otelcol-contrib")"
  SUPERVISOR_SHA="$(sha256_of "${WORK}/in/opampsupervisor")"
  log "otelcol-contrib  sha256:${CONTRIB_SHA} ($(wc -c < "${WORK}/in/otelcol-contrib" | tr -d ' ') bytes, static ${PLATFORM} ELF)"
  log "opampsupervisor  sha256:${SUPERVISOR_SHA} ($(wc -c < "${WORK}/in/opampsupervisor" | tr -d ' ') bytes, static ${PLATFORM} ELF)"

  # 3. both binaries carry their version in the Go build info (readable on any OS: `go version -m` parses ELF files)
  if have go; then
    ver_re="${VERSION//./[.]}"
    go version -m "${WORK}/in/opampsupervisor" > "${WORK}/in/opampsupervisor.buildinfo" 2>/dev/null || true
    grep -Eq "^[[:space:]]+mod[[:space:]]+github[.]com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor[[:space:]]+v${ver_re}([+]dirty)?[[:space:]]*$" \
      "${WORK}/in/opampsupervisor.buildinfo" || die "the supervisor binary of ${SUPERVISOR_PINNED} is not opampsupervisor v${VERSION} (go version -m)"
    go version -m "${WORK}/in/otelcol-contrib" > "${WORK}/in/otelcol-contrib.buildinfo" 2>/dev/null || true
    grep -Eq "^[[:space:]]+dep[[:space:]]+go[.]opentelemetry[.]io/collector/otelcol[[:space:]]+v${ver_re}[[:space:]]" \
      "${WORK}/in/otelcol-contrib.buildinfo" || die "the collector binary of ${CONTRIB_PINNED} is not otelcol-contrib v${VERSION} (go version -m)"
    log "build info: opampsupervisor v${VERSION} and otelcol-contrib v${VERSION} (go.opentelemetry.io/collector/otelcol v${VERSION})"
  else
    warn "go not found: the versions of the binaries were not checked (only the image labels)"
  fi

  # 4. timestamp of the layer entries and of the created label: an input of the build, not the build time
  if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
    SOURCE_DATE_EPOCH="$(python3 -c 'import calendar, re, sys, time
m = re.match(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?(Z|\+00:00)$", sys.argv[1])
print(calendar.timegm(time.strptime(m.group(1), "%Y-%m-%dT%H:%M:%S")) if m else "")' "${created}")"
    [ -n "${SOURCE_DATE_EPOCH}" ] || die "cannot derive a timestamp from the contrib image (created='${created}'): set SOURCE_DATE_EPOCH"
  fi
  [[ "${SOURCE_DATE_EPOCH}" =~ ^[0-9]+$ ]] || die "invalid SOURCE_DATE_EPOCH '${SOURCE_DATE_EPOCH}'"
  EPOCH="${SOURCE_DATE_EPOCH}"
  DATE="$(python3 -c 'import datetime,sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))' "${EPOCH}")"
}

# ---- myriarch-updater ------------------------------------------------------------------------------------------------
# Builds the static myriarch-updater (IMAGE_PLATFORM) of this repository into ${WORK}/in/myriarch-updater. Sets UPDATER_SHA.
build_updater() {
  local ver="${UPDATER_VERSION:-}" commit="${UPDATER_COMMIT:-}" dirty=""
  have "${GO}" || die "go not found: myriarch-updater is built from this repository (or set WITH_UPDATER=0)"
  if have git && git -C "${ROOT}" rev-parse --git-dir >/dev/null 2>&1; then
    [ -n "${ver}" ] || ver="$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || true)"
    [ -n "${commit}" ] || commit="$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || true)"
    if [ -n "$(git -C "${ROOT}" status --porcelain --untracked-files=no 2>/dev/null)" ]; then dirty="-dirty"; fi
  fi
  [ -n "${ver}" ] || ver="0.0.0-dev"
  [ -n "${commit}" ] || commit="unknown"
  UPDATER_VERSION="${ver}"; UPDATER_COMMIT="${commit}"; UPDATER_DIRTY="${dirty}"
  log "building myriarch-updater ${ver} (${commit}${dirty}) for ${PLATFORM}"
  mkdir -p "${WORK}/in"
  ( cd "${ROOT}" && CGO_ENABLED=0 GOOS=linux GOARCH="${IMAGE_GOARCH}" "${GO}" build -trimpath \
      -ldflags "-s -w -X ${VERSION_PKG}.Version=${ver} -X ${VERSION_PKG}.Commit=${commit} -X ${VERSION_PKG}.BuildDate=${DATE}" \
      -o "${WORK}/in/myriarch-updater" ./cmd/myriarch-updater ) || die "go build ./cmd/myriarch-updater failed"
  check_elf "${WORK}/in/myriarch-updater"
  UPDATER_SHA="$(sha256_of "${WORK}/in/myriarch-updater")"
  log "myriarch-updater    sha256:${UPDATER_SHA} ($(wc -c < "${WORK}/in/myriarch-updater" | tr -d ' ') bytes, static ${PLATFORM} ELF)"
}

# ---- license files ------------------------------------------------------------------------------------------------
# /usr/share/licenses/opentelemetry-collector/README: facts about the upstream software in this image. contrib/legacy-kubernetes-collector/kustomize/Dockerfile
# writes the same text (there the image references come from its build arguments); change both together.
write_otel_readme() { # file
  local v="${VERSION}"
  {
    printf '%s\n' \
      "OpenTelemetry Collector and OpAMP Supervisor in this image" \
      "==========================================================" \
      "" \
      "This image contains two programs of the OpenTelemetry project, taken unchanged from the upstream container images:" \
      "" \
      "  /usr/local/bin/otelcol-contrib   otelcol-contrib (OpenTelemetry Collector, contrib distribution) ${v}" \
      "  /usr/local/bin/opampsupervisor   opampsupervisor (OpAMP Supervisor) ${v}" \
      "" \
      "Copyright The OpenTelemetry Authors" \
      "License: Apache-2.0. The text is in the file LICENSE next to this README." \
      "" \
      "Upstream repositories and tags:" \
      "  https://github.com/open-telemetry/opentelemetry-collector-releases  v${v} (otelcol-contrib), cmd/opampsupervisor/v${v} (opampsupervisor)" \
      "  https://github.com/open-telemetry/opentelemetry-collector-contrib   v${v}" \
      "  https://github.com/open-telemetry/opentelemetry-collector           v${v}" \
      "" \
      "Upstream images this image was built from:" \
      "  ${SUPERVISOR_PINNED}" \
      "  ${CONTRIB_PINNED}"
    if [ "${WITH_UPDATER}" = 1 ]; then
      printf '%s\n' \
        "" \
        "The software of the myriarch project in this image (myriarch-updater) is licensed under Apache-2.0:" \
        "/usr/share/licenses/myriarch/LICENSE. The notices of its third-party software are in" \
        "/usr/share/licenses/myriarch/THIRD_PARTY_NOTICES."
    fi
  } > "$1"
}

# ---- layer --------------------------------------------------------------------------------------------------------
# Deterministic tar written by Python (no GNU/BSD tar differences): sorted entries, fixed mtime, fixed modes and owners.
# One entry table serves both modes of the program below: "build" packs the layer, "list" prints the archive paths of the files
# of the layer except the binaries (used by `layout`).
# Arguments: build <root> <layer.tar> <epoch> <otelcol-contrib> <uid> <myriarch-updater or ""> <otel README> | list <root>
layer_py() {
  cat <<'PY'
import os, sys, tarfile

mode, root = sys.argv[1], sys.argv[2]
if mode not in ("build", "list"):
    raise SystemExit("usage: build|list <root> ...")
building = mode == "build"
if building:
    out, epoch, binary, uid, updater, readme = sys.argv[3], int(sys.argv[4]), sys.argv[5], int(sys.argv[6]), sys.argv[7], sys.argv[8]
else:
    uid, updater = 10001, "x"  # the directories and the updater do not matter for the listing

# archive path -> (kind, mode, uid, gid, source); parents of every entry are listed (root:root 0755 like in the base)
entries = {}

def add_dir(path, mode=0o755, owner=0, group=0):
    parts = path.strip("/").split("/")
    for i in range(1, len(parts)):
        entries.setdefault("/".join(parts[:i]), ("dir", 0o755, 0, 0, None))
    entries["/".join(parts)] = ("dir", mode, owner, group, None)

def add_file(arc, src, mode):
    add_dir(os.path.dirname(arc))
    entries[arc] = ("file", mode, 0, 0, src)

def regular(path, what):
    if os.path.islink(path) or not os.path.isfile(path):
        raise SystemExit(what + " is missing or not a regular file: " + path)
    return path

# the collector next to the supervisor (agent.executable of the supervisor config)
if building:
    add_file("usr/local/bin/otelcol-contrib", binary, 0o755)
# storage of the supervisor and its parent: the owner is the image user, GROUP 0 may write (OpenShift: arbitrary UID, GID 0)
add_dir("var/lib/otelcol", 0o775, uid, 0)
add_dir("var/lib/otelcol/supervisor", 0o775, uid, 0)
# mount points for the supervisor config and the fallback collector config (read only, root owned)
add_dir("etc/opampsupervisor")
add_dir("etc/myriarch")
if updater:
    # container mode (myriarch-updater as PID 1): the updater, the volume mount point for the collector binaries and its state
    # (writable for GID 0 like the supervisor storage) and the mount point of trusted_root.json
    if building:
        add_file("usr/local/bin/myriarch-updater", updater, 0o755)
    add_dir("var/lib/myriarch", 0o775, uid, 0)
    add_dir("etc/myriarch/trust")

# License files (0644 root:root). The project license and the notices of myriarch-updater, then the Apache-2.0 text of the
# upstream OpenTelemetry software (a copy of the repository LICENSE) and the README that names what it covers.
licenses = os.path.join(root, "LICENSE")
for name in ("LICENSE", "THIRD_PARTY_NOTICES"):
    add_file("usr/share/licenses/myriarch/" + name, regular(os.path.join(root, name), name), 0o644)
add_file("usr/share/licenses/opentelemetry-collector/LICENSE", regular(licenses, "LICENSE"), 0o644)
add_file("usr/share/licenses/opentelemetry-collector/README", regular(readme, "README") if building else None, 0o644)

if not building:
    for arc in sorted(entries):
        if entries[arc][0] == "file":
            print(arc)
    sys.exit(0)

with tarfile.open(out, "w", format=tarfile.PAX_FORMAT) as tar:
    for arc in sorted(entries):
        kind, fmode, owner, group, src = entries[arc]
        ti = tarfile.TarInfo(arc)
        ti.uid, ti.gid = owner, group
        ti.uname = ti.gname = "root" if (owner, group) == (0, 0) else ""
        ti.mtime = epoch
        ti.mode = fmode
        if kind == "dir":
            ti.type = tarfile.DIRTYPE
            tar.addfile(ti)
        else:
            ti.type = tarfile.REGTYPE
            ti.size = os.path.getsize(src)
            with open(src, "rb") as fh:
                tar.addfile(ti, fh)
print("layer: %d files, %d directories" % (sum(1 for e in entries.values() if e[0] == "file"), sum(1 for e in entries.values() if e[0] == "dir")), file=sys.stderr)
PY
}

build_layer() {
  local updater=""
  if [ "${WITH_UPDATER}" = 1 ]; then updater="${WORK}/in/myriarch-updater"; fi
  layer_py | python3 - build "${ROOT}" "${WORK}/layer.tar" "${EPOCH}" "${WORK}/in/otelcol-contrib" "${IMAGE_UID}" "${updater}" "${WORK}/in/otel-README" ||
    die "building the layer failed"
  LAYER_SHA="$(sha256_of "${WORK}/layer.tar")"
  log "layer.tar sha256:${LAYER_SHA} ($(du -h "${WORK}/layer.tar" | cut -f1))"
}

# `layout`: the sorted archive paths of the files of the layer except the binaries (the license files). No network, no IMAGE_REPO,
# no build; the list is the same with and without WITH_UPDATER (the license files do not depend on it).
cmd_layout() {
  have python3 || die "python3 not found"
  layer_py | python3 - list "${ROOT}" || die "cannot list the layout of the layer"
}

# ---- labels -------------------------------------------------------------------------------------------------------
# (label values must not contain commas: crane parses --label as CSV)
compute_labels() {
  local source_url
  source_url="$(image_source_url "${ROOT}")"
  LABELS=(
    "org.opencontainers.image.title=${LABEL_TITLE_COLLECTOR}"
    "org.opencontainers.image.vendor=${LABEL_VENDOR}"
    "org.opencontainers.image.description=OpenTelemetry Collector ${VERSION} (contrib) managed by the OpAMP supervisor ${VERSION}; connects to myriarchd"
    "org.opencontainers.image.version=${VERSION}"
    "org.opencontainers.image.created=${DATE}"
    "org.opencontainers.image.base.name=${SUPERVISOR_IMAGE%%@*}"
    "org.opencontainers.image.base.digest=${SUPERVISOR_PINNED##*@}"
    "dev.myriarch.collector.version=${VERSION}"
    "dev.myriarch.collector.contrib.image=${CONTRIB_PINNED}"
    "dev.myriarch.collector.contrib.sha256=${CONTRIB_SHA}"
    "dev.myriarch.collector.supervisor.sha256=${SUPERVISOR_SHA}"
    "io.k8s.display-name=myriarch collector ${VERSION}"
    "io.k8s.description=OpAMP supervisor and otelcol-contrib ${VERSION} in one image$([ "${WITH_UPDATER}" = 1 ] && printf ' plus myriarch-updater (container mode)')"
    "io.openshift.tags=myriarch opamp opentelemetry collector"
    "vendor=${LABEL_VENDOR}"
  )
  if [ "${WITH_UPDATER}" = 1 ]; then
    LABELS+=("dev.myriarch.updater.version=${UPDATER_VERSION}${UPDATER_DIRTY:-}" "dev.myriarch.updater.commit=${UPDATER_COMMIT}" "dev.myriarch.updater.sha256=${UPDATER_SHA}")
  fi
  if [ -n "${source_url}" ]; then LABELS+=("org.opencontainers.image.source=${source_url}"); fi
}

# ---- push ---------------------------------------------------------------------------------------------------------
push_image() {
  local ref="${IMAGE_REPO}:${TAG}" out l
  local args=()
  image_docker_config_prepare
  log "docker config: $(image_docker_config_desc)"
  compute_labels
  args=(mutate "${SUPERVISOR_PINNED}" --platform "${PLATFORM}" --append "${WORK}/layer.tar"
    --user "${IMAGE_USER}" --entrypoint "${IMAGE_ENTRYPOINT}" "--cmd=${IMAGE_CMD}" --workdir "${IMAGE_WORKDIR}"
    --env "HOME=/tmp" --exposed-ports "${IMAGE_PORTS}")
  for l in "${LABELS[@]}"; do args+=(--label "${l}"); done
  args+=(--tag "${ref}")
  log "crane mutate --append (base ${SUPERVISOR_PINNED##*/}) -> ${ref}"
  if ! out="$("${CRANE}" "${args[@]}" 2>&1)"; then
    printf '%s\n' "${out}" >&2
    registry_hint
    die "pushing ${ref} failed"
  fi
  # ask the registry for the authoritative digest of the tag (`crane mutate` prints <repo>@<digest> as well)
  DIGEST="$("${CRANE}" digest "${ref}")"
  [[ "${DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "unexpected digest '${DIGEST}'"
  case "${out}" in *"${DIGEST}"*) ;; *) warn "digest printed by crane differs from the registry: ${out##*$'\n'} vs ${DIGEST}" ;; esac
}

# Reads the pushed config back and checks the contract; VERIFY=1 additionally lists the new layer.
verify_image() {
  local pinned="${IMAGE_REPO}@${DIGEST}"
  "${CRANE}" config "${pinned}" | python3 -c '
import json, sys
c = json.load(sys.stdin)
cfg = c["config"]
user, entrypoint, cmd, workdir, version, base_layers, layer_sha, goarch = sys.argv[1:9]
errs = []
if c.get("os") != "linux" or c.get("architecture") != goarch: errs.append("platform %s/%s" % (c.get("os"), c.get("architecture")))
if cfg.get("User") != user: errs.append("user %r" % cfg.get("User"))
if cfg.get("Entrypoint") != [entrypoint]: errs.append("entrypoint %r" % cfg.get("Entrypoint"))
if cfg.get("Cmd") != [cmd]: errs.append("cmd %r" % cfg.get("Cmd"))
if cfg.get("WorkingDir") != workdir: errs.append("workdir %r" % cfg.get("WorkingDir"))
if "HOME=/tmp" not in (cfg.get("Env") or []): errs.append("env HOME")
if not any(e.startswith("PATH=") for e in (cfg.get("Env") or [])): errs.append("env PATH (lost from the base)")
if (cfg.get("Labels") or {}).get("org.opencontainers.image.version") != version: errs.append("label org.opencontainers.image.version")
diff_ids = c["rootfs"]["diff_ids"]
if len(diff_ids) != int(base_layers) + 1: errs.append("%d layers (base %s + 1 expected)" % (len(diff_ids), base_layers))
elif diff_ids[-1] != "sha256:" + layer_sha: errs.append("the new layer %s is not the layer built here (sha256:%s)" % (diff_ids[-1], layer_sha))
if errs:
    print("[collector-image] ERROR: pushed image does not match the contract: " + "; ".join(errs), file=sys.stderr); sys.exit(1)
print("[collector-image] config ok: %s/%s user=%s entrypoint=%s cmd=%s workdir=%s layers=%d (new diff id = layer built here)" % (c["os"], c["architecture"], cfg["User"], cfg["Entrypoint"], cfg.get("Cmd"), cfg.get("WorkingDir"), len(diff_ids)), file=sys.stderr)
' "${IMAGE_USER}" "${IMAGE_ENTRYPOINT}" "${IMAGE_CMD}" "${IMAGE_WORKDIR}" "${VERSION}" "${BASE_LAYERS}" "${LAYER_SHA}" "${IMAGE_GOARCH}" || die "verification of the pushed image failed"
  "${CRANE}" validate --remote "${pinned}" >&2 || die "crane validate --remote ${pinned} failed"
  log "crane validate --remote: manifest, config and layer digests are consistent"
  if [ "${VERIFY}" = 1 ]; then
    local layer
    layer="$("${CRANE}" manifest "${pinned}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["layers"][-1]["digest"])')"
    log "files of the new layer ${layer}:"
    "${CRANE}" blob "${IMAGE_REPO}@${layer}" | tar -tvzf - >&2
  fi
}

# Bit-for-bit comparison with the signed release artifacts (contrib/enterprise-mirror/verify-upstream-images.sh), for exactly the
# pinned digests.
verify_upstream() {
  local script="${ROOT}/contrib/enterprise-mirror/verify-upstream-images.sh" crane_dir
  [ -x "${script}" ] || die "${script} not found"
  crane_dir="$(dirname "${CRANE}")"
  log "VERIFY_UPSTREAM=1: contrib/enterprise-mirror/verify-upstream-images.sh for ${VERSION} (release artifacts: SHA-256 + Sigstore bundle, then bit-identical to the image binaries)"
  PATH="${crane_dir}:${TOOLS_BIN}:${PATH}" \
    OTELCOL_VERSION="${VERSION}" OTELCOL_CONTRIB_IMAGE="${CONTRIB_PINNED}" OTELCOL_SUPERVISOR_IMAGE="${SUPERVISOR_PINNED}" \
    "${script}" >&2 || die "the upstream images ${VERSION} do not match the signed release artifacts (see above; cosign: TOOLS_BIN=${TOOLS_BIN} ci/install-tools.sh cosign)"
}

cmd_build() {
  VERSION="${1:-}"
  [[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: contrib/legacy-kubernetes-collector/build-collector-image-crane.sh <collector version, e.g. 0.162.0>  (or: layout | login USER | help)"
  image_require_repo contrib/legacy-kubernetes-collector/build-collector-image-crane.sh "registry.example.com/<org>/myriarch-collector"
  image_labels_load
  case "${WITH_UPDATER}" in 0|1) ;; *) die "WITH_UPDATER must be 0 or 1" ;; esac
  if [ -z "${TAG:-}" ]; then
    TAG="${VERSION}"
    if [ "${WITH_UPDATER}" = 1 ]; then
      # <collector version>-<git short sha>: every change of myriarch-updater is a new tag (the collector version alone would not be)
      local sha="${UPDATER_COMMIT:-}" dirty=""
      if have git && git -C "${ROOT}" rev-parse --git-dir >/dev/null 2>&1; then
        [ -n "${sha}" ] || sha="$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || true)"
        if [ -n "$(git -C "${ROOT}" status --porcelain --untracked-files=no 2>/dev/null)" ]; then dirty="-dirty"; fi
      fi
      [ -n "${sha}" ] || die "cannot derive the image tag (no git commit): set TAG=... or UPDATER_COMMIT=..."
      TAG="${VERSION}-${sha}${dirty}"
    fi
    TAG="${TAG}${IMAGE_TAG_SUFFIX}"
  fi
  [[ "${TAG}" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]] || die "invalid image tag '${TAG}'"
  # Upstream images: an explicit override must be pinned by digest (ALLOW_UNPINNED_IMAGES=1 allows a tag); without an override the
  # reviewed digests of ci/versions.env are used for its OTELCOL_VERSION, any other version resolves the upstream tag now.
  local override
  for override in SUPERVISOR_IMAGE CONTRIB_IMAGE; do
    case "${!override:-}" in
      "") ;;
      *@sha256:????????????????????????????????????????????????????????????????) ;;
      *) [ "${ALLOW_UNPINNED_IMAGES:-0}" = 1 ] || die "${override} must be pinned by digest (repo@sha256:...): a tag can be moved; ALLOW_UNPINNED_IMAGES=1 allows it for a lab build" ;;
    esac
  done
  local pinned_version pinned_supervisor pinned_contrib
  pinned_version="$(sed -n 's/^OTELCOL_VERSION=//p' "${ROOT}/ci/versions.env" | head -n 1)"
  pinned_supervisor="$(sed -n 's/^OTELCOL_SUPERVISOR_IMAGE=//p' "${ROOT}/ci/versions.env" | head -n 1)"
  pinned_contrib="$(sed -n 's/^OTELCOL_CONTRIB_IMAGE=//p' "${ROOT}/ci/versions.env" | head -n 1)"
  if [ "${VERSION}" = "${pinned_version}" ] && [ -n "${pinned_supervisor}" ] && [ -n "${pinned_contrib}" ]; then
    SUPERVISOR_IMAGE="${SUPERVISOR_IMAGE:-${pinned_supervisor}}"
    CONTRIB_IMAGE="${CONTRIB_IMAGE:-${pinned_contrib}}"
  else
    SUPERVISOR_IMAGE="${SUPERVISOR_IMAGE:-otel/opentelemetry-collector-opampsupervisor:${VERSION}}"
    CONTRIB_IMAGE="${CONTRIB_IMAGE:-otel/opentelemetry-collector-contrib:${VERSION}}"
  fi
  # The supervisor image is not signed upstream: for a build that pushes, the binaries are compared with the signed release
  # artifacts unless VERIFY_UPSTREAM=0 is set explicitly.
  if [ -z "${VERIFY_UPSTREAM}" ]; then
    if [ "${DRY_RUN}" = 1 ]; then VERIFY_UPSTREAM=0; else VERIFY_UPSTREAM=1; fi
  fi
  case "${VERIFY_UPSTREAM}" in 0|1) ;; *) die "VERIFY_UPSTREAM must be 0 or 1" ;; esac
  log "upstream images: supervisor ${SUPERVISOR_IMAGE}, contrib ${CONTRIB_IMAGE}; VERIFY_UPSTREAM=${VERIFY_UPSTREAM}"
  have python3 || die "python3 not found (builds the deterministic layer tar)"
  have tar || die "tar not found"
  resolve_crane
  log "collector ${VERSION} -> ${IMAGE_REPO}:${TAG}$([ "${DRY_RUN}" = 1 ] && printf ' (DRY_RUN=1: nothing is pushed)')"
  if [ "${DRY_RUN}" = 1 ]; then
    WORK="${ROOT}/.cache/collector-image-${VERSION}"; rm -rf "${WORK}"; mkdir -p "${WORK}"
  else
    WORK="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-collector-image.XXXXXX")"
  fi
  fetch_inputs
  if [ "${VERIFY_UPSTREAM}" = 1 ]; then verify_upstream; fi
  if [ "${WITH_UPDATER}" = 1 ]; then build_updater; fi
  write_otel_readme "${WORK}/in/otel-README"
  build_layer
  if [ "${DRY_RUN}" = 1 ]; then
    log "DRY_RUN=1: nothing was pushed (layer: ${WORK}/layer.tar; crane mutate ${SUPERVISOR_PINNED##*/} --platform ${PLATFORM} --append layer.tar ... --tag ${IMAGE_REPO}:${TAG})"
    printf 'COLLECTOR_VERSION=%s\n' "${VERSION}"
    printf 'IMAGE_BASE=%s\n' "${SUPERVISOR_PINNED}"
    printf 'IMAGE_CONTRIB=%s\n' "${CONTRIB_PINNED}"
    printf 'IMAGE_CONTRIB_SHA256=%s\n' "${CONTRIB_SHA}"
    printf 'IMAGE_LAYER_SHA256=%s\n' "${LAYER_SHA}"
    printf 'IMAGE_TAG=%s\n' "${TAG}"
    if [ "${WITH_UPDATER}" = 1 ]; then printf 'IMAGE_UPDATER_SHA256=%s\n' "${UPDATER_SHA}"; fi
    return 0
  fi
  push_image
  verify_image
  log "pushed ${IMAGE_REPO}:${TAG}"
  log "digest ${DIGEST}"
  log "pin the digest in your deployment, e.g. kustomize images: newName/digest or Helm image.digest"
  printf 'COLLECTOR_VERSION=%s\n' "${VERSION}"
  printf 'IMAGE_REF=%s:%s\n' "${IMAGE_REPO}" "${TAG}"
  printf 'IMAGE_DIGEST=%s\n' "${DIGEST}"
  printf 'IMAGE_PINNED=%s@%s\n' "${IMAGE_REPO}" "${DIGEST}"
  printf 'IMAGE_BASE=%s\n' "${SUPERVISOR_PINNED}"
  printf 'IMAGE_CONTRIB=%s\n' "${CONTRIB_PINNED}"
  printf 'IMAGE_CONTRIB_SHA256=%s\n' "${CONTRIB_SHA}"
  printf 'IMAGE_LAYER_SHA256=%s\n' "${LAYER_SHA}"
  printf 'IMAGE_TAG=%s\n' "${TAG}"
  if [ "${WITH_UPDATER}" = 1 ]; then printf 'IMAGE_UPDATER_SHA256=%s\n' "${UPDATER_SHA}"; fi
}

usage() {
  sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'
}

case "${1:-help}" in
  -h|--help|help) usage ;;
  login)
    shift
    image_require_repo contrib/legacy-kubernetes-collector/build-collector-image-crane.sh "registry.example.com/<org>/myriarch-collector"
    [ -n "${1:-}" ] || die "usage: IMAGE_REPO=<registry>/<path> contrib/legacy-kubernetes-collector/build-collector-image-crane.sh login <user>  (the token is read from stdin; same credentials as scripts/build-image-crane.sh)"
    exec "${ROOT}/scripts/build-image-crane.sh" login "$@" ;;
  layout) cmd_layout ;;
  build) shift; cmd_build "$@" ;;
  *) cmd_build "$@" ;;
esac
