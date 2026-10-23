#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds the two images of the probe for the node architecture and pushes them to the local registry (localhost:5001),
# without a container runtime (the crane scripts of scripts/). Writes $PROBE_WORK/images.env.
#
#   hack/kind/images.sh
#
#   server     scripts/build-image-crane.sh            localhost:5001/myriarch/myriarch:<sha>[-arm64]
#   collector  contrib/legacy-kubernetes-collector/build-collector-image-crane.sh  localhost:5001/myriarch/myriarch-collector:<otel version>-<sha>[-arm64]
#              (upstream supervisor + otelcol-contrib of OTELCOL_PREV_VERSION + myriarch-updater)
#
#   updater    (only with IMAGES_SET containing updater) the static myriarch-updater of the commit alone, as the first init container of
#              the collector chart expects it: localhost:5001/myriarch/myriarch-updater:<sha>[-arm64]
#
# Environment: IMAGES_SET=server,collector (default; a comma list of server, collector and updater: ci/kind-chart-test.sh builds
# server,updater), ALLOW_DIRTY=1 (the server script refuses a dirty work tree; the final proof never sets it),
# WEB_BUILD=auto|always|never (default auto), PROBE_REUSE_IMAGES=1 (keep the last images.env, no rebuild),
# IMAGE_PLATFORM (default: the architecture of the Docker engine), KIND_REGISTRY_PORT.
# A tag that already exists in the registry is not rebuilt while the work tree is clean (same commit = same image).
set -euo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
cd "${ROOT}"

prepare_work_dirs
IMAGES_ENV="${PROBE_WORK}/images.env"

if [ "${PROBE_REUSE_IMAGES:-0}" = 1 ] && [ -f "${IMAGES_ENV}" ]; then
  log "PROBE_REUSE_IMAGES=1: keeping ${IMAGES_ENV}"
  exit 0
fi

need_cmd docker git python3 crane
curl -fsS -o /dev/null "http://127.0.0.1:${KIND_REGISTRY_PORT}/v2/" || die "the registry on 127.0.0.1:${KIND_REGISTRY_PORT} does not answer (hack/kind/up.sh)"

platform="${IMAGE_PLATFORM:-$(docker_platform)}" || die "cannot determine the platform of the Docker engine; set IMAGE_PLATFORM=linux/arm64|linux/amd64"
suffix=""
if [ "${platform}" = linux/arm64 ]; then suffix="-arm64"; fi
commit="$(git rev-parse --short HEAD)"
clean=1
if [ -n "$(git status --porcelain)" ]; then clean=0; fi

server_repo="localhost:${KIND_REGISTRY_PORT}/myriarch/myriarch"
collector_repo="localhost:${KIND_REGISTRY_PORT}/myriarch/myriarch-collector"
updater_repo="localhost:${KIND_REGISTRY_PORT}/myriarch/myriarch-updater"
web_build="${WEB_BUILD:-auto}"
IMAGES_SET="${IMAGES_SET:-server,collector}"
case ",${IMAGES_SET}," in *,server,*) ;; *) die "IMAGES_SET '${IMAGES_SET}' lacks server" ;; esac
wants() { case ",${IMAGES_SET}," in *",$1,"*) return 0 ;; esac; return 1; }

# digest_of <repo> <tag> -> digest or empty
digest_of() { crane digest "$1:$2" 2>/dev/null || true; }

# parse_digest <output> -> the IMAGE_DIGEST value
parse_digest() { printf '%s\n' "$1" | sed -n 's/^IMAGE_DIGEST=//p' | tail -n 1; }

build_server() {
  local tag="${commit}${suffix}" d out
  d="$(digest_of "${server_repo}" "${tag}")"
  if [ "${clean}" = 1 ] && [ -n "${d}" ] && [ "${web_build}" != always ]; then
    log "server image ${tag}: already in the registry (${d})"
    SERVER_DIGEST="${d}"
    return 0
  fi
  log "building the server image (${platform})"
  out="$(IMAGE_REPO="${server_repo}" IMAGE_PLATFORM="${platform}" WEB_BUILD="${web_build}" scripts/build-image-crane.sh)" \
    || die "the server image build failed"
  SERVER_DIGEST="$(parse_digest "${out}")"
}

build_collector() {
  local tag="${OTELCOL_PREV_VERSION}-${commit}${suffix}" d out
  d="$(digest_of "${collector_repo}" "${tag}")"
  if [ "${clean}" = 1 ] && [ -n "${d}" ]; then
    log "collector image ${tag}: already in the registry (${d})"
    COLLECTOR_DIGEST="${d}"
    return 0
  fi
  log "building the collector image ${OTELCOL_PREV_VERSION} (${platform}, upstream images pulled once)"
  out="$(IMAGE_REPO="${collector_repo}" IMAGE_PLATFORM="${platform}" \
    SUPERVISOR_IMAGE="${OTELCOL_PREV_SUPERVISOR_IMAGE}" CONTRIB_IMAGE="${OTELCOL_PREV_CONTRIB_IMAGE}" \
    contrib/legacy-kubernetes-collector/build-collector-image-crane.sh "${OTELCOL_PREV_VERSION}")" || die "the collector image build failed"
  COLLECTOR_DIGEST="$(parse_digest "${out}")"
}

# build_updater: the static binary of the commit as the only file of an image with the entrypoint of the release image. The layer is
# deterministic (fixed owner, mode and time), so the same commit gives the same digest.
build_updater() {
  local tag="${commit}${suffix}" d work out
  d="$(digest_of "${updater_repo}" "${tag}")"
  if [ "${clean}" = 1 ] && [ -n "${d}" ]; then
    log "updater image ${tag}: already in the registry (${d})"
    UPDATER_DIGEST="${d}"
    return 0
  fi
  work="$(mktemp -d)"
  log "building the updater image (${platform})"
  ( cd "${ROOT}" && CGO_ENABLED=0 GOOS=linux GOARCH="${platform#linux/}" go build -trimpath -ldflags "-s -w" -o "${work}/myriarch-updater" ./cmd/myriarch-updater ) \
    || { rm -rf "${work}"; die "go build ./cmd/myriarch-updater failed"; }
  python3 -I -c '
import os, sys, tarfile
src, dst = sys.argv[1:3]
with tarfile.open(dst, "w", format=tarfile.PAX_FORMAT) as tar:
    ti = tarfile.TarInfo("usr/local/bin/myriarch-updater")
    ti.size = os.path.getsize(src)
    ti.mode = 0o755
    ti.uid = ti.gid = 0
    ti.uname = ti.gname = "root"
    ti.mtime = 0
    with open(src, "rb") as fh:
        tar.addfile(ti, fh)
' "${work}/myriarch-updater" "${work}/layer.tar" || { rm -rf "${work}"; die "cannot pack the updater layer"; }
  crane append -f "${work}/layer.tar" -t "${updater_repo}:${tag}" >/dev/null || { rm -rf "${work}"; die "crane append failed"; }
  crane mutate "${updater_repo}:${tag}" --set-platform "${platform}" --entrypoint /usr/local/bin/myriarch-updater \
    -t "${updater_repo}:${tag}" >/dev/null || { rm -rf "${work}"; die "crane mutate failed"; }
  rm -rf "${work}"
  UPDATER_DIGEST="$(digest_of "${updater_repo}" "${tag}")"
}

SERVER_DIGEST="" COLLECTOR_DIGEST="" UPDATER_DIGEST=""
build_server
if wants collector; then build_collector; fi
if wants updater; then build_updater; fi
[[ "${SERVER_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "no usable digest for the server image: '${SERVER_DIGEST}'"
if wants collector; then [[ "${COLLECTOR_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "no usable digest for the collector image: '${COLLECTOR_DIGEST}'"; fi
if wants updater; then [[ "${UPDATER_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "no usable digest for the updater image: '${UPDATER_DIGEST}'"; fi

# the images must be what the nodes can run
refs=("${server_repo}@${SERVER_DIGEST}")
if wants collector; then refs+=("${collector_repo}@${COLLECTOR_DIGEST}"); fi
if wants updater; then refs+=("${updater_repo}@${UPDATER_DIGEST}"); fi
for ref in "${refs[@]}"; do
  arch="$(crane config "${ref}" | python3 -I -c 'import json,sys; c=json.load(sys.stdin); print(c["os"]+"/"+c["architecture"])')"
  [ "${arch}" = "${platform}" ] || die "${ref} is ${arch}, expected ${platform}"
done

# the offline Sigstore trusted root for the updater (ConfigMap myriarch-trust); fetched through TUF, needs network once
trusted_root="${PROBE_WORK}/trust/trusted_root.json"
if [ ! -s "${trusted_root}" ]; then
  if [ ! -x bin/myriarch-updater ]; then make -C "${ROOT}" build >&2; fi
  log "fetching the Sigstore trusted root (TUF)"
  bin/myriarch-updater fetch-trusted-root --out "${trusted_root}" >&2 || die "fetch-trusted-root failed (network access to tuf-repo-cdn.sigstore.dev?)"
fi

umask 077
cat > "${IMAGES_ENV}" <<EOT
PLATFORM=${platform}
COMMIT=${commit}
CLEAN=${clean}
SERVER_REPO=${server_repo}
SERVER_DIGEST=${SERVER_DIGEST}
COLLECTOR_REPO=${collector_repo}
COLLECTOR_DIGEST=${COLLECTOR_DIGEST}
UPDATER_REPO=${updater_repo}
UPDATER_DIGEST=${UPDATER_DIGEST}
EOT
log "server    ${server_repo}@${SERVER_DIGEST}"
if wants collector; then log "collector ${collector_repo}@${COLLECTOR_DIGEST}"; fi
if wants updater; then log "updater   ${updater_repo}@${UPDATER_DIGEST}"; fi
