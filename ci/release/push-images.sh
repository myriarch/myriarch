#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Pushes a release image (the OCI layout that scripts/build-image-crane.sh writes with IMAGE_SOURCE=dist) to a registry BY DIGEST
# and verifies what the registry holds. It never sets a tag: the release tags (X.Y.Z-rc.N, X.Y.Z, X.Y) are added by the
# finalize step after the signatures and the verification (ADR-0035), with `crane tag <repo>@<digest> <tag>`.
#
#   ci/release/push-images.sh <layout> <repository>@sha256:<64 hex>
#
#   <layout>      directory with oci-layout, index.json and blobs/sha256/: a flat layout whose index.json IS the image index
#                 (IMAGE_LAYOUT of scripts/build-image-crane.sh)
#   <repository>  <registry>[:port]/<path> without tag, followed by the digest of the index. The digest is the SHA-256 of
#                 index.json (IMAGE_INDEX_DIGEST of scripts/build-image-crane.sh); the script compares them before it pushes.
#
# Rules (spike: scratchpad/spike-oci/REPORT.md)
#   - A reference without @sha256:<64 hex> is refused. `crane push` and `crane copy` create the tag "latest" when they get a
#     repository without tag and digest, and `latest` must not exist during v0.x.
#   - The push is `crane push --index <layout> <repository>@<digest>`. crane uses the digest of the reference as the
#     identifier of the index and does not compare it with the content, and a test registry may accept a wrong one. The script
#     therefore (1) computes the digest from the layout before the push and (2) after the push reads the index back and checks
#     that its SHA-256, `crane digest`, the bytes of index.json and the whole image (`crane validate --remote`) are the ones
#     it pushed.
#   - The repositories of the project (ghcr.io/myriarch/...) are written only from the release workflow: GITHUB_WORKFLOW_REF
#     must name .github/workflows/release.yml of myriarch/myriarch at a version tag (ADR-0035 freezes this signing identity). This is a
#     guard against a push from a laptop, not a security boundary: what GITHUB_TOKEN may write is decided by GitHub.
#   - For the repositories of the project the layout must also be a complete release image: index manifests for exactly
#     linux/amd64 and linux/arm64, and the source org.opencontainers.image.source of ci/image-labels.env (LABEL_SOURCE) in the
#     index annotations and in every platform config (ghcr.io links the package to the repository through it).
#   - Credentials: the script does not log in and does not touch DOCKER_CONFIG. The release workflow runs
#     `crane auth login ghcr.io` with the default HOME beforehand (actions/attest reads only $HOME/.docker/config.json).
#   - Pushing the same digest again changes nothing.
#
# Environment
#   CRANE   path of the crane binary   default .cache/bin/crane (the pinned version, built by ci/install-tools.sh crane)
#
# Output (stdout): IMAGE_PUSHED=<repository>@<digest>, IMAGE_INDEX_DIGEST=<digest>. Everything else goes to stderr.
# Exit status: 0 = pushed and verified, 1 = refused or failed, 2 = usage error.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"

log() { printf '[push-images] %s\n' "$*" >&2; }
die() { printf '[push-images] ERROR: %s\n' "$*" >&2; exit 1; }
usage() { printf 'usage: ci/release/push-images.sh <layout> <repository>@sha256:<64 hex>\n' >&2; exit 2; }
have() { command -v "$1" >/dev/null 2>&1; }

# shellcheck source=scripts/lib/image-common.sh
. "${ROOT}/scripts/lib/image-common.sh"

case "${1:-}" in
  -h | --help | help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
esac
[ "$#" -eq 2 ] || usage
LAYOUT="$1"
REF="$2"

# ---- the reference ------------------------------------------------------------------------------------------------
[[ "${REF}" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]] ||
  die "refusing '${REF}': the reference must be <registry>[:port]/<path>@sha256:<64 hex> (no tag; without a digest crane creates the tag latest)"
REPO="${REF%@*}"
DIGEST="${REF##*@}"

# ---- the release workflow ------------------------------------------------------------------------------------------
if image_is_published_repo "${REPO}"; then
  [[ "${GITHUB_WORKFLOW_REF:-}" =~ ^myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
    die "refusing to write to ${REPO}: the published repositories are written only by .github/workflows/release.yml at a version tag (GITHUB_WORKFLOW_REF is '${GITHUB_WORKFLOW_REF:-unset}')"
fi

# ---- the layout ----------------------------------------------------------------------------------------------------
[ -d "${LAYOUT}" ] || die "layout ${LAYOUT} is not a directory"
have python3 || die "python3 not found"
PUBLISHED=0
WANT_SOURCE=""
if image_is_published_repo "${REPO}"; then
  PUBLISHED=1
  image_labels_load
  WANT_SOURCE="${LABEL_SOURCE}"
  [ -n "${WANT_SOURCE}" ] || die "LABEL_SOURCE is empty in ci/image-labels.env"
fi
# index.json is a flat index (every manifest has a platform), every blob it needs is present with the right size and digest,
# and the SHA-256 of index.json is the digest of the reference.
python3 -I - "${LAYOUT}" "${DIGEST}" "${PUBLISHED}" "${WANT_SOURCE}" <<'PY' || die "the layout ${LAYOUT} is not a valid release image for ${DIGEST}"
import hashlib, json, os, sys

layout, want, published, want_source = sys.argv[1], sys.argv[2], sys.argv[3] == "1", sys.argv[4]
SOURCE = "org.opencontainers.image.source"

def fail(msg):
    print("[push-images] ERROR: " + msg, file=sys.stderr)
    sys.exit(1)

def read(path):
    try:
        with open(path, "rb") as f:
            return f.read()
    except OSError as e:
        fail("cannot read " + path + ": " + e.strerror)

def blob(desc):
    d = desc.get("digest", "")
    if not d.startswith("sha256:") or len(d) != 71:
        fail("unsupported digest '%s'" % d)
    data = read(os.path.join(layout, "blobs", "sha256", d[7:]))
    if hashlib.sha256(data).hexdigest() != d[7:]:
        fail("blob %s does not match its digest" % d)
    if len(data) != desc.get("size"):
        fail("blob %s has the size %d, the descriptor says %s" % (d, len(data), desc.get("size")))
    return data

if read(os.path.join(layout, "oci-layout")).strip() != b'{"imageLayoutVersion":"1.0.0"}':
    fail("oci-layout is not an OCI image layout 1.0.0")
raw = read(os.path.join(layout, "index.json"))
got = "sha256:" + hashlib.sha256(raw).hexdigest()
if got != want:
    fail("index.json has the digest %s, the reference says %s" % (got, want))
index = json.loads(raw)
if index.get("mediaType") != "application/vnd.oci.image.index.v1+json" or not index.get("manifests"):
    fail("index.json is not an image index with manifests (a layout with a nested index is not pushed with --index)")
platforms = []
if published and (index.get("annotations") or {}).get(SOURCE) != want_source:
    fail("the index lacks the annotation %s=%s (ghcr.io links the package to the repository through it)" % (SOURCE, want_source))
for m in index["manifests"]:
    if m.get("mediaType") != "application/vnd.oci.image.manifest.v1+json":
        fail("index.json lists a descriptor of type %s, expected image manifests only" % m.get("mediaType"))
    plat = m.get("platform") or {}
    if not plat.get("os") or not plat.get("architecture"):
        fail("a manifest of index.json has no platform")
    platforms.append("%s/%s" % (plat["os"], plat["architecture"]))
    manifest = json.loads(blob(m))
    config = json.loads(blob(manifest["config"]))
    if published and ((config.get("config") or {}).get("Labels") or {}).get(SOURCE) != want_source:
        fail("the config of %s lacks the label %s=%s" % (platforms[-1], SOURCE, want_source))
    for layer in manifest["layers"]:
        blob(layer)
if published and sorted(platforms) != ["linux/amd64", "linux/arm64"]:
    fail("a release image has exactly the platforms linux/amd64 and linux/arm64, the index has %s" % ", ".join(sorted(platforms)))
PY

# ---- crane ---------------------------------------------------------------------------------------------------------
if [ -n "${CRANE:-}" ]; then
  [ -x "${CRANE}" ] || die "CRANE=${CRANE} is not executable"
else
  TOOLS_BIN="${TOOLS_BIN}" "${ROOT}/ci/install-tools.sh" crane >&2 && [ -x "${TOOLS_BIN}/crane" ] ||
    die "crane not available: run ci/install-tools.sh crane"
  CRANE="${TOOLS_BIN}/crane"
fi

# ---- push and verification -----------------------------------------------------------------------------------------
log "pushing ${LAYOUT} -> ${REF}"
if ! out="$("${CRANE}" push --index "${LAYOUT}" "${REF}" 2>&1)"; then
  printf '%s\n' "${out}" >&2
  die "pushing ${REF} failed (not logged in? crane auth login <registry> beforehand)"
fi

got="$("${CRANE}" digest "${REF}")" || die "cannot read the digest of ${REF} back"
[ "${got}" = "${DIGEST}" ] || die "the registry reports ${got} for ${REF}"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-push.XXXXXX")"
trap 'rm -rf "${tmp}"' EXIT
"${CRANE}" manifest "${REF}" > "${tmp}/index.json" || die "cannot read the index of ${REF} back"
sum="$(python3 -I -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "${tmp}/index.json")"
[ "sha256:${sum}" = "${DIGEST}" ] || die "the index the registry serves for ${REF} hashes to sha256:${sum}"
cmp -s "${tmp}/index.json" "${LAYOUT}/index.json" || die "the index the registry serves for ${REF} differs from ${LAYOUT}/index.json"
"${CRANE}" validate --remote "${REF}" >&2 || die "the image ${REF} in the registry is incomplete or invalid"

log "pushed and verified ${REF}"
printf 'IMAGE_PUSHED=%s\n' "${REF}"
printf 'IMAGE_INDEX_DIGEST=%s\n' "${DIGEST}"
