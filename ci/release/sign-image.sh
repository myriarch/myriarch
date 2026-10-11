#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Signs one release image keyless and verifies the result (step of the job `push-images`, ADR-0035). Privileged (id-token: write).
#
#   TOOLS_BIN=... ci/release/sign-image.sh --repository REPO --index DIGEST --platform DIGEST --platform DIGEST --tag TAG --commit SHA
#
#   --repository  ghcr.io/myriarch/myriarch or ghcr.io/myriarch/myriarch-updater (RELEASE_IMAGE_SERVER or RELEASE_IMAGE_UPDATER of
#                 identity.env) [repository_invalid]
#   --index       the digest of the image index; --platform twice: the digests of the linux/amd64 and linux/arm64 manifests
#                 (sha256:<64 hex>, three different digests) [digest_invalid]
#
# `cosign sign --yes --recursive REPO@INDEX` signs the index and both platform manifests with the ambient OIDC token [sign_failed].
# Then `cosign verify` runs for the index and for each platform digest with the exact identity of the run: the certificate identity
# of release.yml at TAG, the issuer, and the claims repository, ref, sha (SHA) and trigger of identity.env [signature_invalid].
# GHCR has no referrers API, so cosign keeps the signatures in sha256-<hex> fallback tags; run this strictly before the attest steps
# of the same digest (they read-modify-write the same fallback tag).
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="sign-image.sh --repository REPO --index DIGEST --platform DIGEST --platform DIGEST --tag TAG --commit SHA"
repo="" index="" tag="" commit=""
platforms=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repository) need_value "$1" "$#"; repo="$2"; shift 2 ;;
    --index) need_value "$1" "$#"; index="$2"; shift 2 ;;
    --platform) need_value "$1" "$#"; platforms+=("$2"); shift 2 ;;
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${repo}" ] && [ -n "${index}" ] && [ -n "${tag}" ] && [ -n "${commit}" ] && [ "${#platforms[@]}" -eq 2 ] || usage_err "${USAGE}"
load_identity
check_text repository_invalid "--repository" "${repo}"
[ "${repo}" = "${RELEASE_IMAGE_SERVER}" ] || [ "${repo}" = "${RELEASE_IMAGE_UPDATER}" ] \
  || err repository_invalid "${repo} is not one of the release images (${RELEASE_IMAGE_SERVER}, ${RELEASE_IMAGE_UPDATER})"
check_match digest_invalid "--index" "${index}" "${DIGEST_RE}"
for d in "${platforms[@]}"; do check_match digest_invalid "--platform" "${d}" "${DIGEST_RE}"; done
[ "${index}" != "${platforms[0]}" ] && [ "${index}" != "${platforms[1]}" ] && [ "${platforms[0]}" != "${platforms[1]}" ] \
  || err digest_invalid "the index and the two platform digests must be three different digests"
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
need_tool cosign

info "signing ${repo}@${index} (recursive)"
"${TOOLS_BIN}/cosign" sign --yes --recursive "${repo}@${index}" >&2 || err sign_failed "cosign sign failed for ${repo}@${index}"
for d in "${index}" "${platforms[@]}"; do
  verify_image_signature "${repo}" "${d}" "${tag}" "${commit}"
  info "verified the signature of ${repo}@${d}"
done
