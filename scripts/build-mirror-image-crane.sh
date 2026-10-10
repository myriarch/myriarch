#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds and pushes the MIRROR image WITHOUT a container runtime (crane): busybox httpd plus the verified release files of
# the OpenTelemetry Collector (contrib) in the layout of the GitHub releases. It is an in-cluster mirror for myriarchd's artifact
# cache (artifacts.sourceBaseUrl, e.g. http://<mirror-service>:8080) for clusters without egress to GitHub.
#
#   IMAGE_REPO=registry.example.com/<org>/myriarch-mirror scripts/build-mirror-image-crane.sh
#                                                                build + push  <IMAGE_REPO>:contrib-0.161.0-0.162.0
#   make mirror-image-crane IMAGE_REPO=registry.example.com/<org>/myriarch-mirror
#                                                                the same through the Makefile
#   IMAGE_REPO=... DRY_RUN=1 scripts/build-mirror-image-crane.sh download + verify everything, build the layer, push nothing
#   OUT_DIR=.e2e/mirror scripts/build-mirror-image-crane.sh      only write the directory <OUT_DIR>/srv/mirror (any platform, no crane and
#                                                                no IMAGE_REPO needed): serve it with
#                                                                `python3 -m http.server --directory <OUT_DIR>/srv/mirror`
#   VERSIONS="0.161.0 0.162.0" PLATFORMS="darwin/arm64" OUT_DIR=... scripts/build-mirror-image-crane.sh      local end-to-end test on a Mac
#   IMAGE_REPO=localhost:5000/myriarch-mirror scripts/build-mirror-image-crane.sh                          local registry (crane registry serve)
#   IMAGE_REPO=<registry>/<path> scripts/build-mirror-image-crane.sh login USER
#                                                                one-time registry login (same as scripts/build-image-crane.sh login)
#
# What it does
#   1. for every VERSION x PLATFORM: downloads  <SOURCE_BASE>/v<ver>/otelcol-contrib_<ver>_<os>_<arch>.tar.gz  and its  .sha256  and
#      .sigstore.json  (https, curl; cached in DOWNLOAD_CACHE and checked again on every run);
#   2. checks every file BEFORE it is packed, and aborts on the first problem:
#        - SHA-256 of the archive against the published .sha256,
#        - the Sigstore bundle OFFLINE with `myriarch-updater verify-bundle --trusted-root` (same code, same identity policy as myriarchd and the
#          updater: release workflow of open-telemetry/opentelemetry-collector-releases for exactly the tag v<ver>);
#   3. gets trusted_root.json: TRUSTED_ROOT=<file>, or fetched via TUF (`myriarch-updater fetch-trusted-root`, cache .cache/mirror/tuf) and
#      used for step 2; it is part of the image (/srv/mirror/trusted_root.json); put the same file into a ConfigMap of your
#      deployment, so that myriarchd and the collectors' updaters verify offline with it;
#   4. packs everything into ONE deterministic layer on top of busybox (pinned by digest, must have the httpd applet) with
#        /srv/mirror/v<ver>/<asset>{,.sha256,.sigstore.json}   0444 root:root
#        /srv/mirror/trusted_root.json, /srv/mirror/SHA256SUMS
#      and pushes it with `crane mutate` (ENTRYPOINT busybox httpd -f -v -p 8080 -h /srv/mirror, user 10001:0, port 8080);
#   5. reads the pushed config back (contract check), runs `crane validate --remote` and prints the digest. Lines starting with MIRROR_ or
#      IMAGE_ on stdout are machine readable (everything else goes to stderr).
#
# Why this is safe without egress: nothing is trusted because it comes from the mirror. myriarchd and myriarch-updater verify SHA-256 and the
# Sigstore bundle against trusted_root.json themselves; the mirror only has to deliver bytes (a compromised mirror can only make an update fail).
# Renew trusted_root.json (Sigstore key rotation, about once a year): run this script again (TUF) and update the ConfigMap of your
# deployment with the printed MIRROR_TRUSTED_ROOT file.
#
# Environment (all optional)
#   VERSIONS          space separated collector versions       default "0.161.0 0.162.0"
#   PLATFORMS         space separated os/arch                  default "linux/amd64" (linux|darwin|windows / amd64|arm64)
#   SOURCE_BASE       release download base                    default https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download
#   IMAGE_REPO        target repository (required unless OUT_DIR is set, no default): <registry>[:port]/<path> without tag or digest
#   TAG               image tag                                default contrib-<versions joined with ->, e.g. contrib-0.161.0-0.162.0
#   BASE_IMAGE        busybox base with the httpd applet, must be pinned by digest: <registry>/<image>:<tag>@sha256:<digest>
#                                                              default: the public index docker.io/library/busybox:1.37 (digest in the script)
#   TRUSTED_ROOT      trusted_root.json to use                 default: fetched via TUF
#   OUT_DIR           write <OUT_DIR>/srv/mirror only          default: build the image
#   DOWNLOAD_CACHE    cache of the downloads                   default .cache/mirror/downloads
#   DRY_RUN           1 = build the layer, push nothing        default 0
#   SOURCE_DATE_EPOCH timestamp of all layer entries           default 1704067200 (fixed: same inputs => same layer)
#   DOCKER_CONFIG     directory of crane's registry credentials (config.json); respected, never set by this script; crane's
#                     default is ~/.docker
#   SOURCE_URL        org.opencontainers.image.source          default: https form of `git remote get-url origin`, else LABEL_SOURCE
#                                                              of ci/image-labels.env, else no label
#   CRANE, GO, KEEP_WORK  as in scripts/build-collector-image-crane.sh
#
# Labels that are the same for every image of the project (vendor, licenses, titles) come from ci/image-labels.env.
#
# Licensing: the project does not publish this image (ADR-0024 lists the published images; this one is built by the user into the
# user's own IMAGE_REPO). It contains busybox (GPL-2.0-only) as the base and the OpenTelemetry Collector release archives
# (Apache-2.0). Whoever pushes or otherwise distributes the image takes on the obligations of those licenses, for busybox the
# offer or provision of the corresponding source for the pinned base image (BASE_IMAGE; source: https://busybox.net). The
# org.opencontainers.image.licenses label (LABEL_LICENSES_MIRROR of ci/image-labels.env) names both. The image holds no LICENSE
# or THIRD_PARTY_NOTICES file of the project. See deploy/README.md (License files).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"

VERSIONS="${VERSIONS:-0.161.0 0.162.0}"
PLATFORMS="${PLATFORMS:-linux/amd64}"
SOURCE_BASE="${SOURCE_BASE:-https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download}"
IMAGE_REPO="${IMAGE_REPO:-}"
# public busybox index, pinned by digest (determined 2026-10-08 with `crane digest docker.io/library/busybox:1.37`; its linux/amd64
# manifest sha256:66a6306db78bf2dbf3487f293aa8d6990d8e506fdffab9cc43fe422becf886e4 contains bin/httpd). Pick a new one on purpose.
BASE_IMAGE="${BASE_IMAGE:-docker.io/library/busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e}"
OUT_DIR="${OUT_DIR:-}"
DOWNLOAD_CACHE="${DOWNLOAD_CACHE:-${ROOT}/.cache/mirror/downloads}"
DRY_RUN="${DRY_RUN:-0}"
KEEP_WORK="${KEEP_WORK:-0}"
GO="${GO:-go}"
EPOCH="${SOURCE_DATE_EPOCH:-1704067200}"
IMAGE_PLATFORM="linux/amd64"
IMAGE_USER="10001:0"
IMAGE_PORT="8080"
MIRROR_DIR="/srv/mirror"
ENTRYPOINT="/bin/busybox,httpd,-f,-v,-p,${IMAGE_PORT},-h,${MIRROR_DIR}"

log()  { printf '[mirror-image] %s\n' "$*" >&2; }
warn() { printf '[mirror-image] WARNING: %s\n' "$*" >&2; }
die()  { printf '[mirror-image] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# shellcheck source=scripts/lib/image-common.sh
. "${ROOT}/scripts/lib/image-common.sh"

WORK=""
cleanup() {
  if [ -n "${WORK}" ] && [ -d "${WORK}" ]; then
    if [ "${KEEP_WORK}" = 1 ] || [ "${DRY_RUN}" = 1 ]; then log "build directory kept: ${WORK}"; else rm -rf "${WORK}"; fi
  fi
}
trap cleanup EXIT

sha256_of() { # file
  python3 -c 'import hashlib,sys
h = hashlib.sha256()
with open(sys.argv[1], "rb") as f:
    for chunk in iter(lambda: f.read(1 << 20), b""):
        h.update(chunk)
print(h.hexdigest())' "$1"
}

resolve_crane() {
  if [ -n "${CRANE:-}" ]; then
    [ -x "${CRANE}" ] || die "CRANE=${CRANE} is not executable"
    return 0
  fi
  if TOOLS_BIN="${TOOLS_BIN}" "${ROOT}/ci/install-tools.sh" crane >&2 && [ -x "${TOOLS_BIN}/crane" ]; then
    CRANE="${TOOLS_BIN}/crane"
  elif have crane; then
    warn "could not build the pinned crane (ci/install-tools.sh crane failed): using $(command -v crane)"
    CRANE="$(command -v crane)"
  else
    die "crane not available: run ci/install-tools.sh crane (needs Go and access to the Go module proxy)"
  fi
}

# ---- inputs -------------------------------------------------------------------------------------------------------
check_inputs() {
  local v p os arch
  # the image is named and based before anything is downloaded; OUT_DIR mode writes a directory only and needs neither
  if [ -z "${OUT_DIR}" ]; then
    image_require_repo scripts/build-mirror-image-crane.sh "registry.example.com/<org>/myriarch-mirror"
    image_require_digest_pin BASE_IMAGE "${BASE_IMAGE}"
    image_labels_load
  fi
  [ -n "${VERSIONS// /}" ] || die "VERSIONS is empty"
  for v in ${VERSIONS}; do
    [[ "${v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid version '${v}' in VERSIONS (expected X.Y.Z without v)"
  done
  for p in ${PLATFORMS}; do
    os="${p%%/*}"; arch="${p#*/}"
    case "${os}" in linux|darwin|windows) ;; *) die "invalid platform '${p}' (os must be linux, darwin or windows)" ;; esac
    [[ "${arch}" =~ ^[a-z0-9]+$ ]] || die "invalid platform '${p}'"
  done
  [[ "${EPOCH}" =~ ^[0-9]+$ ]] || die "invalid SOURCE_DATE_EPOCH '${EPOCH}'"
  have curl || die "curl not found"
  have python3 || die "python3 not found (computes hashes, builds the deterministic layer)"
  have "${GO}" || die "go not found: myriarch-updater (verify-bundle) is built from this repository"
  TAG="${TAG:-contrib-$(printf '%s' "${VERSIONS}" | tr -s ' ' '-' | sed -e 's/^-//' -e 's/-$//')}"
  [[ "${TAG}" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]] || die "invalid image tag '${TAG}'"
}

# myriarch-updater of this repository (host platform): verify-bundle + fetch-trusted-root
build_verifier() {
  log "building myriarch-updater (verify-bundle, fetch-trusted-root)"
  ( cd "${ROOT}" && "${GO}" build -trimpath -o "${WORK}/myriarch-updater" ./cmd/myriarch-updater ) || die "go build ./cmd/myriarch-updater failed"
}

# trusted root: TRUSTED_ROOT or fetched via TUF. Sets ROOT_FILE.
get_trusted_root() {
  if [ -n "${TRUSTED_ROOT:-}" ]; then
    [ -s "${TRUSTED_ROOT}" ] || die "TRUSTED_ROOT=${TRUSTED_ROOT} is not a file"
    ROOT_FILE="${TRUSTED_ROOT}"
    log "trusted root: ${ROOT_FILE} (TRUSTED_ROOT)"
  else
    mkdir -p "${ROOT}/.cache/mirror/tuf"
    ROOT_FILE="${WORK}/trusted_root.json"
    log "trusted root: fetching via TUF (cache ${ROOT}/.cache/mirror/tuf)"
    "${WORK}/myriarch-updater" fetch-trusted-root --out "${ROOT_FILE}" --tuf-cache "${ROOT}/.cache/mirror/tuf" >&2 \
      || die "cannot fetch the Sigstore trusted root via TUF (no network? set TRUSTED_ROOT=<file>)"
  fi
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert "tlogs" in d and "certificateAuthorities" in d' "${ROOT_FILE}" \
    || die "${ROOT_FILE} is not a Sigstore trusted_root.json"
  ROOT_SHA="$(sha256_of "${ROOT_FILE}")"
  log "trusted_root.json sha256:${ROOT_SHA}"
}

# download + verify one file set; arguments: version os arch. Sets nothing, copies into ${STAGE}.
fetch_one() {
  local ver="$1" os="$2" arch="$3" asset dir url want got
  asset="otelcol-contrib_${ver}_${os}_${arch}.tar.gz"
  dir="${DOWNLOAD_CACHE}/v${ver}"
  url="${SOURCE_BASE%/}/v${ver}/${asset}"
  mkdir -p "${dir}"
  local f
  for f in "${asset}" "${asset}.sha256" "${asset}.sigstore.json"; do
    if [ ! -s "${dir}/${f}" ]; then
      log "download ${url%/*}/${f}"
      curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 20 -o "${dir}/${f}.part" "${url%/*}/${f}" \
        || { rm -f "${dir}/${f}.part"; die "download of ${url%/*}/${f} failed"; }
      mv "${dir}/${f}.part" "${dir}/${f}"
    else
      log "cached   ${dir}/${f}"
    fi
  done
  # 1. SHA-256 against the published checksum file ("<hex>  <name>")
  want="$(awk 'NR==1 {print $1}' "${dir}/${asset}.sha256" | tr 'A-F' 'a-f')"
  [[ "${want}" =~ ^[0-9a-f]{64}$ ]] || die "${dir}/${asset}.sha256 does not start with a SHA-256 hex digest"
  got="$(sha256_of "${dir}/${asset}")"
  if [ "${got}" != "${want}" ]; then
    die "SHA-256 mismatch for ${asset}: published ${want}, downloaded ${got} (delete ${dir} and retry; if it persists the release file changed)"
  fi
  # 2. Sigstore bundle, offline against the trusted root, bound to the release tag v<ver>
  "${WORK}/myriarch-updater" verify-bundle --bundle "${dir}/${asset}.sigstore.json" --artifact "${dir}/${asset}" \
    --version "v${ver}" --trusted-root "${ROOT_FILE}" >&2 \
    || die "Sigstore verification of ${asset} failed (offline, trusted root ${ROOT_FILE}); nothing was packed"
  log "verified ${asset}: sha256:${got} + Sigstore bundle (offline)"
  # stage with the layout of the release server: v<ver>/<asset>{,.sha256,.sigstore.json}
  mkdir -p "${STAGE}/v${ver}"
  for f in "${asset}" "${asset}.sha256" "${asset}.sigstore.json"; do cp "${dir}/${f}" "${STAGE}/v${ver}/${f}"; done
  SUMS+=("${got}  v${ver}/${asset}")
}

fetch_all() {
  local v p
  STAGE="${WORK}/srv/mirror"
  mkdir -p "${STAGE}"
  SUMS=()
  for v in ${VERSIONS}; do
    for p in ${PLATFORMS}; do fetch_one "${v}" "${p%%/*}" "${p#*/}"; done
  done
  cp "${ROOT_FILE}" "${STAGE}/trusted_root.json"
  printf '%s\n' "${SUMS[@]}" | sort -k2 > "${STAGE}/SHA256SUMS"
  log "SHA256SUMS: ${#SUMS[@]} archive(s)"
}

# ---- layer --------------------------------------------------------------------------------------------------------
# Deterministic tar written by Python: sorted entries, fixed mtime, root:root, files 0444, directories 0755.
build_layer() {
  python3 - "${WORK}/layer.tar" "${EPOCH}" "${STAGE}" <<'PY' || die "building the layer failed"
import os, sys, tarfile

out, epoch, stage = sys.argv[1], int(sys.argv[2]), sys.argv[3]
entries = {}
for d in ("srv", "srv/mirror"):
    entries[d] = ("dir", None)
for base, dirs, files in os.walk(stage):
    rel = os.path.relpath(base, stage)
    arc = "srv/mirror" if rel == "." else "srv/mirror/" + rel.replace(os.sep, "/")
    entries[arc] = ("dir", None)
    for f in files:
        entries[arc + "/" + f] = ("file", os.path.join(base, f))

with tarfile.open(out, "w", format=tarfile.PAX_FORMAT) as tar:
    for arc in sorted(entries):
        kind, src = entries[arc]
        ti = tarfile.TarInfo(arc)
        ti.uid = ti.gid = 0
        ti.uname = ti.gname = "root"
        ti.mtime = epoch
        if kind == "dir":
            ti.type, ti.mode = tarfile.DIRTYPE, 0o755
            tar.addfile(ti)
        else:
            ti.type, ti.mode, ti.size = tarfile.REGTYPE, 0o444, os.path.getsize(src)
            with open(src, "rb") as fh:
                tar.addfile(ti, fh)
print("layer: %d files, %d directories" % (sum(1 for e in entries.values() if e[0] == "file"), sum(1 for e in entries.values() if e[0] == "dir")), file=sys.stderr)
PY
  LAYER_SHA="$(sha256_of "${WORK}/layer.tar")"
  log "layer.tar sha256:${LAYER_SHA} ($(du -h "${WORK}/layer.tar" | cut -f1))"
}

# ---- base image ---------------------------------------------------------------------------------------------------
strip_tag() { # ref -> repository without :tag (a registry port is kept)
  local r="$1" last="${1##*/}"
  case "${last}" in *:*) printf '%s' "${r%:*}" ;; *) printf '%s' "${r}" ;; esac
}

# pins BASE_IMAGE by digest (already required by check_inputs) and checks that it has the httpd applet. Sets BASE_PINNED.
pin_base() {
  local d listing
  d="${BASE_IMAGE##*@}"
  [[ "${d}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "BASE_IMAGE must be pinned by digest: <repo>:<tag>@sha256:<digest> (crane digest <repo>:<tag>)"
  BASE_PINNED="$(strip_tag "${BASE_IMAGE%@*}")@${d}"
  if listing="$("${CRANE}" export --platform "${IMAGE_PLATFORM}" "${BASE_PINNED}" - 2>/dev/null | tar -tf - 2>/dev/null)"; then
    if printf '%s\n' "${listing}" | grep -Eq '^(\./)?(usr/)?(s?bin)/httpd$'; then
      log "base ${BASE_PINNED} (from ${BASE_IMAGE}): busybox with the httpd applet"
      return 0
    fi
  else
    warn "cannot read the base image ${BASE_PINNED}"
  fi
  die "BASE_IMAGE ${BASE_IMAGE} has no busybox httpd applet: choose a busybox image with httpd, pinned by digest"
}

# ---- push + verification ------------------------------------------------------------------------------------------
push_image() {
  local ref="${IMAGE_REPO}:${TAG}" out
  local first_version="${VERSIONS%% *}" source_url
  source_url="$(image_source_url "${ROOT}")"
  local labels=(
    "org.opencontainers.image.title=${LABEL_TITLE_MIRROR}"
    "org.opencontainers.image.vendor=${LABEL_VENDOR}"
    "org.opencontainers.image.licenses=${LABEL_LICENSES_MIRROR}"
    "org.opencontainers.image.description=busybox httpd serving verified otelcol-contrib release files for myriarchd without egress"
    "org.opencontainers.image.base.name=${BASE_PINNED%@*}" "org.opencontainers.image.base.digest=${BASE_PINNED##*@}"
    "dev.myriarch.mirror.versions=${VERSIONS// /,}" "dev.myriarch.mirror.platforms=${PLATFORMS// /,}"
    "dev.myriarch.mirror.trusted-root.sha256=${ROOT_SHA}" "dev.myriarch.mirror.layer.sha256=${LAYER_SHA}"
    "io.k8s.display-name=${LABEL_TITLE_MIRROR} ${first_version}" "vendor=${LABEL_VENDOR}"
  )
  if [ -n "${source_url}" ]; then labels+=("org.opencontainers.image.source=${source_url}"); fi
  local args=(mutate "${BASE_PINNED}" --platform "${IMAGE_PLATFORM}" --append "${WORK}/layer.tar"
    --user "${IMAGE_USER}" --entrypoint "${ENTRYPOINT}" --cmd "" --workdir "/" --exposed-ports "${IMAGE_PORT}/tcp")
  local l
  for l in "${labels[@]}"; do args+=(--label "${l}"); done
  args+=(--tag "${ref}")
  image_docker_config_prepare
  log "crane mutate --append -> ${ref}"
  if ! out="$("${CRANE}" "${args[@]}" 2>&1)"; then
    printf '%s\n' "${out}" >&2
    cat >&2 <<EOF
[mirror-image] If the push failed with UNAUTHORIZED / DENIED, you are not logged in, have no write permission or the repository
[mirror-image] ${IMAGE_REPO#*/} does not exist yet (create it first):
[mirror-image]   IMAGE_REPO=${IMAGE_REPO} scripts/build-mirror-image-crane.sh login '<registry-user>' < token-file     (stored in $(image_docker_config_desc))
EOF
    die "pushing ${ref} failed"
  fi
  DIGEST="$("${CRANE}" digest "${ref}")"
  [[ "${DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "unexpected digest '${DIGEST}'"
}

verify_image() {
  local pinned="${IMAGE_REPO}@${DIGEST}"
  "${CRANE}" config "${pinned}" | python3 -c '
import json, sys
c = json.load(sys.stdin)
cfg = c["config"]
user, port, mirror_dir, layer_sha = sys.argv[1:5]
errs = []
if c.get("os") != "linux" or c.get("architecture") != "amd64": errs.append("platform %s/%s" % (c.get("os"), c.get("architecture")))
if cfg.get("User") != user: errs.append("user %r" % cfg.get("User"))
ep = cfg.get("Entrypoint") or []
if ep[:2] != ["/bin/busybox", "httpd"] or ep[-2:] != ["-h", mirror_dir] or port not in ep: errs.append("entrypoint %r" % ep)
if cfg.get("Cmd"): errs.append("cmd %r must be empty (it would be appended to the httpd arguments)" % cfg.get("Cmd"))
if not any(e.startswith("PATH=") for e in (cfg.get("Env") or [])): errs.append("env PATH (lost from the base)")
if c["rootfs"]["diff_ids"][-1] != "sha256:" + layer_sha: errs.append("the new layer is not the layer built here")
if errs:
    print("[mirror-image] ERROR: pushed image does not match the contract: " + "; ".join(errs), file=sys.stderr); sys.exit(1)
print("[mirror-image] config ok: user=%s entrypoint=%s cmd=%s layers=%d (new diff id = layer built here)" % (cfg["User"], ep, cfg.get("Cmd"), len(c["rootfs"]["diff_ids"])), file=sys.stderr)
' "${IMAGE_USER}" "${IMAGE_PORT}" "${MIRROR_DIR}" "${LAYER_SHA}" || die "verification of the pushed image failed"
  "${CRANE}" validate --remote "${pinned}" >&2 || die "crane validate --remote ${pinned} failed"
  log "crane validate --remote: manifest, config and layer digests are consistent"
}

# ---- commands -----------------------------------------------------------------------------------------------------
cmd_build() {
  check_inputs
  if [ -z "${OUT_DIR}" ]; then resolve_crane; fi
  if [ -z "${OUT_DIR}" ] && [ "${DRY_RUN}" = 1 ]; then
    WORK="${ROOT}/.cache/mirror-image"; rm -rf "${WORK}"; mkdir -p "${WORK}"
  else
    WORK="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-mirror-image.XXXXXX")"
  fi
  log "versions: ${VERSIONS}; platforms: ${PLATFORMS}"
  build_verifier
  get_trusted_root
  fetch_all

  if [ -n "${OUT_DIR}" ]; then
    mkdir -p "${OUT_DIR}/srv"
    rm -rf "${OUT_DIR}/srv/mirror"
    cp -R "${STAGE}" "${OUT_DIR}/srv/mirror"
    log "mirror directory written: ${OUT_DIR}/srv/mirror"
    printf 'MIRROR_DIR=%s\n' "${OUT_DIR}/srv/mirror"
    printf 'MIRROR_TRUSTED_ROOT=%s\n' "${OUT_DIR}/srv/mirror/trusted_root.json"
    printf 'MIRROR_TRUSTED_ROOT_SHA256=%s\n' "${ROOT_SHA}"
    return 0
  fi

  pin_base
  build_layer
  # keep the trusted root where a deployment can take it from (the work directory is removed)
  mkdir -p "${ROOT}/.cache/mirror/out"
  cp "${ROOT_FILE}" "${ROOT}/.cache/mirror/out/trusted_root.json"
  if [ "${DRY_RUN}" = 1 ]; then
    log "DRY_RUN=1: nothing was pushed (layer: ${WORK}/layer.tar; would push ${IMAGE_REPO}:${TAG})"
    printf 'MIRROR_VERSIONS=%s\n' "${VERSIONS// /,}"
    printf 'MIRROR_TRUSTED_ROOT=%s\n' "${ROOT}/.cache/mirror/out/trusted_root.json"
    printf 'MIRROR_TRUSTED_ROOT_SHA256=%s\n' "${ROOT_SHA}"
    printf 'IMAGE_BASE=%s\n' "${BASE_PINNED}"
    printf 'IMAGE_LAYER_SHA256=%s\n' "${LAYER_SHA}"
    printf 'IMAGE_TAG=%s\n' "${TAG}"
    return 0
  fi
  push_image
  verify_image
  log "pushed ${IMAGE_REPO}:${TAG}"
  log "digest ${DIGEST}"
  log "pin the digest in your deployment, e.g. kustomize images: newName/digest or Helm image.digest"
  printf 'MIRROR_VERSIONS=%s\n' "${VERSIONS// /,}"
  printf 'MIRROR_TRUSTED_ROOT=%s\n' "${ROOT}/.cache/mirror/out/trusted_root.json"
  printf 'MIRROR_TRUSTED_ROOT_SHA256=%s\n' "${ROOT_SHA}"
  printf 'IMAGE_REF=%s:%s\n' "${IMAGE_REPO}" "${TAG}"
  printf 'IMAGE_DIGEST=%s\n' "${DIGEST}"
  printf 'IMAGE_PINNED=%s@%s\n' "${IMAGE_REPO}" "${DIGEST}"
  printf 'IMAGE_BASE=%s\n' "${BASE_PINNED}"
  printf 'IMAGE_LAYER_SHA256=%s\n' "${LAYER_SHA}"
  printf 'IMAGE_TAG=%s\n' "${TAG}"
}

usage() {
  sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'
}

case "${1:-build}" in
  -h|--help|help) usage ;;
  login)
    shift
    image_require_repo scripts/build-mirror-image-crane.sh "registry.example.com/<org>/myriarch-mirror"
    [ -n "${1:-}" ] || die "usage: IMAGE_REPO=<registry>/<path> scripts/build-mirror-image-crane.sh login <user>  (the token is read from stdin; same credentials as scripts/build-image-crane.sh)"
    exec "${ROOT}/scripts/build-image-crane.sh" login "$@" ;;
  build) cmd_build ;;
  *) die "unknown argument '${1}' (usage: scripts/build-mirror-image-crane.sh [build|login USER|help]; configuration via environment, see the header)" ;;
esac
