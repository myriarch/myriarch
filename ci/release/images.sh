#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Builds the OCI layouts of both release images from the staged release assets (job `images` of the release workflow, ADR-0035). The
# images are assembled from the released archives, which checksums.txt lists, so nothing is compiled here and nothing is pushed.
#
#   TOOLS_BIN=... ci/release/images.sh --release DIR --version V --out DIR
#
#   --release DIR  the unpacked release-assets artifact: DIR/assets/<42 names minus the two signature files> and DIR/meta/metadata.json
#   --version V    the release version without "v" (X.Y.Z or X.Y.Z-rc.N); metadata.json must say the same
#   --out DIR      new or empty; receives server/ and updater/ (flat OCI layouts), server.env, updater.env and the SBOM directories
#                  server.sbom/ and updater.sbom/ (linux_amd64.spdx.json and linux_arm64.spdx.json)
#
# For each kind it runs, with a temporary DIST_DIR that holds copies of the assets and of metadata.json:
#   IMAGE_SOURCE=dist IMAGE_KIND=server|updater DIST_DIR=... OUT_DIR=... scripts/build-image-crane.sh
# which needs a clean work tree, Go (for go version -m), the pinned syft in TOOLS_BIN and crane (CRANE, or TOOLS_BIN/crane; the
# release workflow passes the crane of the signing set in CRANE). The test knobs of that script (ALLOW_DIRTY,
# IMAGE_PLATFORMS, IMAGE_SBOM, SOURCE_URL, IMAGE_RELEASE_VERSION) must not be set [image_env].
#
# stdout (GITHUB_OUTPUT lines): server-index, server-amd64, server-arm64, updater-index, updater-amd64, updater-arm64 (sha256:<64 hex>).
# A digest that is malformed or a missing SBOM file is [image_output]; a failed build is [image_build].
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="images.sh --release DIR --version V --out DIR"
release="" version="" out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --release) need_value "$1" "$#"; release="$2"; shift 2 ;;
    --version) need_value "$1" "$#"; version="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${release}" ] && [ -n "${version}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
check_match version_invalid "--version" "${version}" "${VERSION_RE}"
check_text image_output "--release" "${release}"
check_text image_output "--out" "${out}"
for v in ALLOW_DIRTY IMAGE_PLATFORMS IMAGE_SBOM SOURCE_URL IMAGE_RELEASE_VERSION; do
  [ -z "${!v:-}" ] || err image_env "${v} is set: the release images are built without the test knobs of scripts/build-image-crane.sh"
done
[ -d "${release}/assets" ] || err file_missing "${release}/assets is missing"
[ -f "${release}/meta/metadata.json" ] || err file_missing "${release}/meta/metadata.json is missing"
meta_version="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8")).get("version", ""))' "${release}/meta/metadata.json" 2>/dev/null)" || meta_version=""
[ "${meta_version}" = "${version}" ] || err version_mismatch "meta/metadata.json says version '${meta_version}', not ${version}"
if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err out_not_empty "${out} exists and is not empty"; fi
mkdir -p "${out}"
out="$(cd "${out}" && pwd)"

dist="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-images.XXXXXX")"
logs="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-images-log.XXXXXX")"
trap 'rm -rf "${dist}" "${logs}"' EXIT
for f in "${release}"/assets/*; do
  [ -f "${f}" ] && [ ! -L "${f}" ] || err file_missing "${f} is not a regular file"
  ln "${f}" "${dist}/$(basename "${f}")" 2>/dev/null || cp "${f}" "${dist}/$(basename "${f}")"
done
cp "${release}/meta/metadata.json" "${dist}/metadata.json"

pick() { sed -n "s/^$2=//p" "$1" | head -n 1; }

emit() { # kind
  local kind="$1" log="${logs}/$1.out" k
  info "building the ${kind} image layout"
  if ! IMAGE_SOURCE=dist IMAGE_KIND="${kind}" DIST_DIR="${dist}" OUT_DIR="${out}" TOOLS_BIN="${TOOLS_BIN}" \
       "${ROOT}/scripts/build-image-crane.sh" > "${log}"; then
    err image_build "scripts/build-image-crane.sh failed for the ${kind} image"
  fi
  local index amd64 arm64 sbom_amd64 sbom_arm64
  index="$(pick "${log}" IMAGE_INDEX_DIGEST)"
  amd64="$(pick "${log}" IMAGE_MANIFEST_DIGEST_LINUX_AMD64)"
  arm64="$(pick "${log}" IMAGE_MANIFEST_DIGEST_LINUX_ARM64)"
  sbom_amd64="$(pick "${log}" IMAGE_SBOM_LINUX_AMD64)"
  sbom_arm64="$(pick "${log}" IMAGE_SBOM_LINUX_ARM64)"
  for k in "${index}" "${amd64}" "${arm64}"; do
    [[ "${k}" =~ ${DIGEST_RE} ]] || err image_output "the ${kind} build reported a malformed digest"
  done
  [ "${amd64}" != "${arm64}" ] || err image_output "the ${kind} platform digests are equal"
  [ -s "${sbom_amd64}" ] && [ -s "${sbom_arm64}" ] || err image_output "the ${kind} SBOM files are missing"
  [ -f "${out}/${kind}/index.json" ] || err image_output "the ${kind} layout has no index.json"
  printf '%s-index=%s\n%s-amd64=%s\n%s-arm64=%s\n' "${kind}" "${index}" "${kind}" "${amd64}" "${kind}" "${arm64}" >> "${logs}/result"
}

emit server
emit updater
cat "${logs}/result"
