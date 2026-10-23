#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Verifies a pushed chart (the server chart or the collector chart) before the release is published (job `verify-chart` of the release workflow, ADR-0035). Nothing here
# changes the registry; the job holds read scopes only and has logged in with ci/release/registry-login.sh (the package is still private).
#
#   TOOLS_BIN=... GH_TOKEN=... ci/release/verify-chart.sh [--chart-name NAME] --tag TAG --commit SHA --digest CHART_DIGEST --image-digest INDEX_DIGEST
#
#   --chart-name   myriarch (default): RELEASE_CHART, which references the server image; myriarch-collector: RELEASE_CHART_COLLECTOR, which
#                  references the updater image and the two unchanged upstream images [chart_name]
#   --image-digest the index digest of the image of the release that the chart references (the server image or the updater image)
#
# 1. tag        RELEASE_CHART@--digest exists, the version tag does not exist yet (finalize.sh sets it after the second approval) or, in
#               a re-run after finalize, resolves to --digest, and the repository has no tag latest and no tag besides version tags,
#               sha256-<64 hex> signature tags and the tag artifacthub.io, which is accepted only when its manifest is the Artifact Hub
#               metadata artifact (config and one layer with the Artifact Hub media types; ci/release/push-artifacthub.sh)
#                                                                                       [chart_digest_mismatch] [chart_tag_unexpected]
# 2. content    the chart layer (media type application/vnd.cncf.helm.chart.content.v1.tar+gzip) of RELEASE_CHART@--digest is fetched BY
#               DIGEST and ci/release/chart-package.sh verify runs on it: every image of the renderings is the image of the release at
#               --image-digest (the collector chart also the two upstream pins of ci/versions.env), and the chart and app version are
#               the release version                                                                         [chart_layer] [image_not_digest]
# 3. signature  `cosign verify` with the exact identity of the run (tag, commit)                                      [signature_invalid]
# 4. provenance `gh attestation verify oci://RELEASE_CHART@--digest --bundle-from-oci` with the identity of the run [attestation_invalid]
#
# Exit status: 0 verified, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="verify-chart.sh [--chart-name NAME] --tag TAG --commit SHA --digest CHART_DIGEST --image-digest INDEX_DIGEST (environment: TOOLS_BIN, GH_TOKEN)"
tag="" commit="" digest="" image_digest="" name="myriarch"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --chart-name) need_value "$1" "$#"; name="$2"; shift 2 ;;
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
case "${name}" in
  myriarch) repo="${RELEASE_CHART}"; image="${RELEASE_IMAGE_SERVER}"; what="the server image" ;;
  myriarch-collector) repo="${RELEASE_CHART_COLLECTOR}"; image="${RELEASE_IMAGE_UPDATER}"; what="the updater image" ;;
  *) err chart_name "--chart-name must be myriarch or myriarch-collector" ;;
esac
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
got="$("${TOOLS_BIN}/crane" digest "${repo}@${digest}" 2> "${work}/crane.err")" || err chart_digest_mismatch "crane could not read ${repo}@${digest}"
[ "${got}" = "${digest}" ] || err chart_digest_mismatch "${repo}@${digest} is reported as ${got}"
tags="$("${TOOLS_BIN}/crane" ls "${repo}" 2> "${work}/crane.err")" || err chart_tag_unexpected "crane could not list the tags of ${repo}"
while IFS= read -r t; do
  [ -n "${t}" ] || continue
  [ "${t}" != latest ] || err chart_tag_unexpected "${repo} has the tag latest, which must not exist during v0.x"
  [[ "${t}" =~ ^sha256-[0-9a-f]{64}(\.sig)?$ ]] && continue
  if [ "${t}" = artifacthub.io ]; then
    "${TOOLS_BIN}/crane" manifest "${repo}:${t}" > "${work}/ah-manifest.json" 2> "${work}/crane.err" || err chart_tag_unexpected "crane could not read the manifest of ${repo}:${t}"
    python3 -I - "${work}/ah-manifest.json" <<'PY' || exit 1
import json, sys

try:
    m = json.load(open(sys.argv[1], "rb"))
    ok = (m.get("config", {}).get("mediaType") == "application/vnd.cncf.artifacthub.config.v1+yaml"
          and [l.get("mediaType") for l in m.get("layers") or []] == ["application/vnd.cncf.artifacthub.repository-metadata.layer.v1.yaml"])
except (ValueError, AttributeError, TypeError):
    ok = False
if not ok:
    print("[verify-chart] ERROR [chart_tag_unexpected] the tag artifacthub.io is not an Artifact Hub metadata artifact (config and one layer with the Artifact Hub media types)", file=sys.stderr)
    sys.exit(1)
PY
    continue
  fi
  if [[ "${t}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[1-9][0-9]*)?$ ]]; then
    if [ "${t}" = "${version}" ]; then
      got="$("${TOOLS_BIN}/crane" digest "${repo}:${t}" 2> "${work}/crane.err")" || err chart_digest_mismatch "crane could not read ${repo}:${t}"
      [ "${got}" = "${digest}" ] || err chart_digest_mismatch "${repo}:${t} is ${got}, this run pushed ${digest}: a chart tag is never moved"
    fi
    continue
  fi
  err chart_tag_unexpected "${repo} has the tag '${t}', which is neither a version tag, a sha256-<hex> signature tag nor artifacthub.io"
done <<EOT
${tags}
EOT

# --- 2. content --------------------------------------------------------------------------------------------------------------
"${TOOLS_BIN}/crane" manifest "${repo}@${digest}" > "${work}/manifest.json" 2> "${work}/crane.err" || err chart_layer "crane could not read the manifest of ${repo}@${digest}"
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
"${TOOLS_BIN}/crane" blob "${repo}@${layer}" > "${work}/chart/${name}-${version}.tgz" 2> "${work}/crane.err" || err chart_layer "crane could not fetch the layer ${layer}"
[ "sha256:$(sha256_of "${work}/chart/${name}-${version}.tgz")" = "${layer}" ] || err chart_layer "the layer the registry serves does not hash to ${layer}"
"${RELEASE_LIB_DIR}/chart-package.sh" verify --chart-name "${name}" --package "${work}/chart/${name}-${version}.tgz" --image-digest "${image_digest}" --version "${version}"

# --- 3. signature ------------------------------------------------------------------------------------------------------------
verify_image_signature "${repo}" "${digest}" "${tag}" "${commit}"

# --- 4. provenance -----------------------------------------------------------------------------------------------------------
if ! "${TOOLS_BIN}/gh" attestation verify "oci://${repo}@${digest}" --repo "${RELEASE_REPOSITORY}" --bundle-from-oci \
    --cert-identity "$(identity_exact "${tag}")" --source-ref "refs/tags/${tag}" --source-digest "${commit}" --deny-self-hosted-runners \
    > /dev/null 2> "${work}/gh.err"; then
  err attestation_invalid "gh attestation verify failed for ${repo}@${digest}: $(grep -v '^[[:space:]]*$' "${work}/gh.err" | tail -n 1 | cut -c 1-300)"
fi
info "${repo}@${digest}: tag, content, signature and provenance verified"
summary "## Verified chart ${tag}" "" "- \`${repo}@${digest}\` references ${what} \`${image}@${image_digest}\`"
