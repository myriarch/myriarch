#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Verifies the pushed server chart before the release is published (job `verify-chart` of the release workflow, ADR-0035). Nothing here
# changes the registry; the job holds read scopes only and has logged in with ci/release/registry-login.sh (the package is still private).
#
#   TOOLS_BIN=... GH_TOKEN=... ci/release/verify-chart.sh --tag TAG --commit SHA --digest CHART_DIGEST --image-digest SERVER_INDEX_DIGEST
#
# 1. tag        RELEASE_CHART@--digest exists, the version tag does not exist yet (finalize.sh sets it after the second approval) or, in
#               a re-run after finalize, resolves to --digest, and the repository has no tag latest and no tag besides version tags and
#               sha256-<64 hex> signature tags                                          [chart_digest_mismatch] [chart_tag_unexpected]
# 2. content    the chart layer (media type application/vnd.cncf.helm.chart.content.v1.tar+gzip) of RELEASE_CHART@--digest is fetched BY
#               DIGEST and ci/release/chart-package.sh verify runs on it: every image of the default and the OpenShift rendering is
#               the server image at --image-digest, and the chart and app version are the release version     [chart_layer] [image_not_digest]
# 3. signature  `cosign verify` with the exact identity of the run (tag, commit)                                      [signature_invalid]
# 4. provenance `gh attestation verify oci://RELEASE_CHART@--digest --bundle-from-oci` with the identity of the run [attestation_invalid]
#
# Exit status: 0 verified, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="verify-chart.sh --tag TAG --commit SHA --digest CHART_DIGEST --image-digest SERVER_INDEX_DIGEST (environment: TOOLS_BIN, GH_TOKEN)"
tag="" commit="" digest="" image_digest=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    --digest) need_value "$1" "$#"; digest="$2"; shift 2 ;;
    --image-digest) need_value "$1" "$#"; image_digest="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${tag}" ] && [ -n "${commit}" ] && [ -n "${digest}" ] && [ -n "${image_digest}" ] || usage_err "${USAGE}"
load_identity
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
check_match digest_invalid "--digest" "${digest}" "${DIGEST_RE}"
check_match digest_invalid "--image-digest" "${image_digest}" "${DIGEST_RE}"
version="${tag#v}"
need_gh
need_tool helm cosign crane
work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-verify-chart.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

# --- 1. tag ------------------------------------------------------------------------------------------------------------------
got="$("${TOOLS_BIN}/crane" digest "${RELEASE_CHART}@${digest}" 2> "${work}/crane.err")" || err chart_digest_mismatch "crane could not read ${RELEASE_CHART}@${digest}"
[ "${got}" = "${digest}" ] || err chart_digest_mismatch "${RELEASE_CHART}@${digest} is reported as ${got}"
tags="$("${TOOLS_BIN}/crane" ls "${RELEASE_CHART}" 2> "${work}/crane.err")" || err chart_tag_unexpected "crane could not list the tags of ${RELEASE_CHART}"
while IFS= read -r t; do
  [ -n "${t}" ] || continue
  [ "${t}" != latest ] || err chart_tag_unexpected "${RELEASE_CHART} has the tag latest, which must not exist during v0.x"
  [[ "${t}" =~ ^sha256-[0-9a-f]{64}(\.sig)?$ ]] && continue
  if [[ "${t}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[1-9][0-9]*)?$ ]]; then
    if [ "${t}" = "${version}" ]; then
      got="$("${TOOLS_BIN}/crane" digest "${RELEASE_CHART}:${t}" 2> "${work}/crane.err")" || err chart_digest_mismatch "crane could not read ${RELEASE_CHART}:${t}"
      [ "${got}" = "${digest}" ] || err chart_digest_mismatch "${RELEASE_CHART}:${t} is ${got}, this run pushed ${digest}: a chart tag is never moved"
    fi
    continue
  fi
  err chart_tag_unexpected "${RELEASE_CHART} has the tag '${t}', which is neither a version tag nor a sha256-<hex> signature tag"
done <<EOT
${tags}
EOT

# --- 2. content --------------------------------------------------------------------------------------------------------------
"${TOOLS_BIN}/crane" manifest "${RELEASE_CHART}@${digest}" > "${work}/manifest.json" 2> "${work}/crane.err" || err chart_layer "crane could not read the manifest of ${RELEASE_CHART}@${digest}"
layer="$(python3 -I - "${work}/manifest.json" "${digest}" <<'PY'
import hashlib, json, sys

path, digest = sys.argv[1:3]
raw = open(path, "rb").read()
if "sha256:" + hashlib.sha256(raw).hexdigest() != digest:
    print("[verify-chart] ERROR [chart_layer] the manifest the registry serves does not hash to %s" % digest, file=sys.stderr)
    sys.exit(1)
layers = [l for l in json.loads(raw).get("layers") or [] if l.get("mediaType") == "application/vnd.cncf.helm.chart.content.v1.tar+gzip"]
if len(layers) != 1 or not str(layers[0].get("digest", "")).startswith("sha256:"):
    print("[verify-chart] ERROR [chart_layer] the chart manifest must have exactly one chart content layer", file=sys.stderr)
    sys.exit(1)
print(layers[0]["digest"])
PY
)" || exit 1
check_match chart_layer "the chart layer digest" "${layer}" "${DIGEST_RE}"
mkdir -p "${work}/chart"
"${TOOLS_BIN}/crane" blob "${RELEASE_CHART}@${layer}" > "${work}/chart/myriarch-${version}.tgz" 2> "${work}/crane.err" || err chart_layer "crane could not fetch the layer ${layer}"
[ "sha256:$(sha256_of "${work}/chart/myriarch-${version}.tgz")" = "${layer}" ] || err chart_layer "the layer the registry serves does not hash to ${layer}"
"${RELEASE_LIB_DIR}/chart-package.sh" verify --package "${work}/chart/myriarch-${version}.tgz" --image-digest "${image_digest}" --version "${version}"

# --- 3. signature ------------------------------------------------------------------------------------------------------------
verify_image_signature "${RELEASE_CHART}" "${digest}" "${tag}" "${commit}"

# --- 4. provenance -----------------------------------------------------------------------------------------------------------
if ! "${TOOLS_BIN}/gh" attestation verify "oci://${RELEASE_CHART}@${digest}" --repo "${RELEASE_REPOSITORY}" --bundle-from-oci \
    --cert-identity "$(identity_exact "${tag}")" --source-ref "refs/tags/${tag}" --source-digest "${commit}" --deny-self-hosted-runners \
    > /dev/null 2> "${work}/gh.err"; then
  err attestation_invalid "gh attestation verify failed for ${RELEASE_CHART}@${digest}: $(grep -v '^[[:space:]]*$' "${work}/gh.err" | tail -n 1 | cut -c 1-300)"
fi
info "${RELEASE_CHART}@${digest}: tag, content, signature and provenance verified"
summary "## Verified chart ${tag}" "" "- \`${RELEASE_CHART}@${digest}\` references the server image \`${RELEASE_IMAGE_SERVER}@${image_digest}\`"
