#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Verifies the upstream images pinned by digest (ci/versions.env: OTELCOL_CONTRIB_IMAGE, OTELCOL_SUPERVISOR_IMAGE) before
# the collector image (deploy/k8s/collector/Dockerfile) is built from them:
#
#   The binaries in the image must be bit-identical to the upstream release artifacts, and these must be valid
#   (SHA-256 from <asset>.sha256 AND Sigstore bundle <asset>.sigstore.json against the identity of the release workflow
#   github.com/open-telemetry/opentelemetry-collector-releases, issuer token.actions.githubusercontent.com).
#
# Why not "cosign verify <image>"? The supervisor image is not signed with cosign upstream, and the contrib image
# of 0.162.0 is signed by a maintainer account instead of by the workflow (checked on 2026-10-07). The signed
# release binaries are the reliable anchor; the image digest is bound to them this way.
#
#   contrib/enterprise-mirror/verify-upstream-images.sh
#
# Environment:
#   UPSTREAM_RELEASE_BASE_URL  download base of the release artifacts, layout <base>/<tag>/<asset>
#                              (default: GitHub; with a mirror: https://artifactory.example.com/artifactory/otelcol-github-releases)
#   COSIGN_TRUSTED_ROOT        optional: trusted_root.json for offline verification (otherwise TUF through HTTPS_PROXY)
#   HTTPS_PROXY/NO_PROXY, CURL_CA_FILE (internal CA) as usual.
# Needs crane, cosign, curl, tar (ci/install-tools.sh crane cosign).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=ci/lib.sh
. "${HERE}/../../ci/lib.sh"
load_versions
need_cmd crane cosign curl tar
need_env OTELCOL_VERSION OTELCOL_CONTRIB_IMAGE OTELCOL_SUPERVISOR_IMAGE

base="${UPSTREAM_RELEASE_BASE_URL:-https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download}"
base="${base%/}"
ver="${OTELCOL_VERSION}"
issuer="https://token.actions.githubusercontent.com"
id_contrib='^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
id_supervisor='^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-binary-release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
work="$(mktemp -d)"; trap 'rm -rf "${work}"' EXIT

ca=()
while IFS= read -r a; do [ -n "${a}" ] && ca+=("${a}"); done < <(curl_ca_args)
fetch() { curl -fsSL --retry 3 --connect-timeout 20 ${ca[@]+"${ca[@]}"} -o "$2" "$1" || die "download failed: $1"; }

check() { # label image asset-url identity-regexp path-in-image extract-from-asset(tar-member|"")
  local label="$1" image="$2" url="$3" identity="$4" imgpath="$5" member="$6"
  local asset="${work}/${label}.asset" want got imgbin="${work}/${label}.image-bin" relbin
  case "${image}" in *@sha256:????????????????????????????????????????????????????????????????) ;; *) die "${label}: image is not pinned by digest: ${image}" ;; esac
  log "${label}: downloading ${url}"
  fetch "${url}" "${asset}"
  fetch "${url}.sha256" "${asset}.sha256"
  fetch "${url}.sigstore.json" "${asset}.sigstore.json"
  want="$(tr -d '[:space:]' < "${asset}.sha256" | cut -c1-64)"
  got="$(sha256_of "${asset}")"
  [ "${want}" = "${got}" ] || die "[checksum_mismatch] ${label}: SHA-256 of the release artifact does not match (expected ${want}, got ${got})"
  local args=(verify-blob --bundle "${asset}.sigstore.json" --certificate-identity-regexp "${identity}" --certificate-oidc-issuer "${issuer}")
  if [ -n "${COSIGN_TRUSTED_ROOT:-}" ]; then args+=(--trusted-root "${COSIGN_TRUSTED_ROOT}"); fi
  cosign "${args[@]}" "${asset}" >/dev/null 2>&1 || die "[signature_invalid] ${label}: Sigstore verification of the release artifact failed (identity/bundle; HTTPS_PROXY for TUF or COSIGN_TRUSTED_ROOT?)"
  log "${label}: release artifact valid (SHA-256 + Sigstore)"
  if [ -n "${member}" ]; then
    relbin="${work}/${label}.release-bin"
    tar -xzOf "${asset}" "${member}" > "${relbin}" || die "${label}: ${member} is not in the archive"
  else
    relbin="${asset}"
  fi
  crane export --platform linux/amd64 "${image}" - | tar -xOf - "${imgpath}" > "${imgbin}" || die "${label}: ${imgpath} is not in the image ${image}"
  [ -s "${imgbin}" ] || die "${label}: ${imgpath} in the image is empty"
  if [ "$(sha256_of "${imgbin}")" != "$(sha256_of "${relbin}")" ]; then
    die "[checksum_mismatch] ${label}: the binary in the image ${image} is NOT identical to the signed release artifact"
  fi
  log "${label}: the binary in the image is bit-identical to the signed release artifact ($(sha256_of "${imgbin}"))"
}

check contrib "${OTELCOL_CONTRIB_IMAGE}" "${base}/v${ver}/otelcol-contrib_${ver}_linux_amd64.tar.gz" "${id_contrib}" otelcol-contrib otelcol-contrib
check supervisor "${OTELCOL_SUPERVISOR_IMAGE}" "${base}/cmd/opampsupervisor/v${ver}/opampsupervisor_${ver}_linux_amd64" "${id_supervisor}" usr/local/bin/opampsupervisor ""
log "OK: both upstream images match the signed release artifacts ${ver}"
