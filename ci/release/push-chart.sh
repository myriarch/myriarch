#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Pushes the packaged server chart to ghcr.io and signs it keyless (step of the job `push-chart`, ADR-0035). Privileged (id-token: write,
# packages: write). It runs strictly before the provenance attestation of the same digest (actions/attest), like sign-image.sh.
#
#   TOOLS_BIN=... GITHUB_WORKFLOW_REF=... ci/release/push-chart.sh --package FILE --tag TAG --commit SHA
#
#   --package  the file that ci/release/chart-package.sh wrote, named myriarch-<TAG without v>.tgz [package_invalid]
#
# 1. guard    GITHUB_WORKFLOW_REF names .github/workflows/release.yml of the release repository at a version tag (ADR-0035 freezes this
#             identity): a guard against a push from a laptop, not a security boundary [workflow_ref]
# 2. package  ci/release/chart-package.sh verify needs the server image digest, which only the job knows: IMAGE_DIGEST (sha256:<64 hex>)
#             must be set, and the package must render every image by exactly that digest [package_invalid] [image_not_digest]
# 3. tag      the chart tag X.Y.Z[-rc.N] must not exist in RELEASE_CHART yet: `helm push` always sets the tag, and a tag of a released
#             chart never moves, so a second run for the same tag refuses and an organization owner removes the version by hand [chart_tag_exists]
# 4. push     `helm push FILE oci://<parent of RELEASE_CHART>` with HELM_REGISTRY_CONFIG=$HOME/.docker/config.json (the file that
#             ci/release/registry-login.sh writes; helm reads the same format). The digest comes from helm's output and is read back with
#             `crane digest` and `crane validate --remote` [chart_digest_mismatch] [push_failed]
# 5. sign     `cosign sign --yes REPO@DIGEST` with the ambient OIDC token, then `cosign verify` with the exact identity of the run (the same
#             claims as sign-image.sh) [sign_failed] [signature_invalid]
#
# stdout: chart-digest=<digest> (a GITHUB_OUTPUT line). Everything else goes to stderr. The registry is never given a tag other than the chart version.
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="push-chart.sh --package FILE --tag TAG --commit SHA (environment: IMAGE_DIGEST, TOOLS_BIN, GITHUB_WORKFLOW_REF)"
package="" tag="" commit=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --package) need_value "$1" "$#"; package="$2"; shift 2 ;;
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${package}" ] && [ -n "${tag}" ] && [ -n "${commit}" ] || usage_err "${USAGE}"
load_identity
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
check_text package_invalid "--package" "${package}"
version="${tag#v}"
[ -f "${package}" ] && [ "$(basename "${package}")" = "myriarch-${version}.tgz" ] || err package_invalid "--package must be the file myriarch-${version}.tgz"
image_digest="${IMAGE_DIGEST:-}"
check_match digest_invalid "IMAGE_DIGEST" "${image_digest}" "${DIGEST_RE}"
need_tool helm cosign crane
[ -n "${HOME:-}" ] && [ -f "${HOME}/.docker/config.json" ] || err push_failed "${HOME:-HOME}/.docker/config.json does not exist: run ci/release/registry-login.sh login first"

# --- 1. guard ----------------------------------------------------------------------------------------------------------------
want_ref="$(printf '%s' "${RELEASE_REPOSITORY}/${RELEASE_WORKFLOW}@refs/tags/${tag}")"
[ "${GITHUB_WORKFLOW_REF:-}" = "${want_ref}" ] \
  || err workflow_ref "refusing to write to ${RELEASE_CHART}: GITHUB_WORKFLOW_REF is '${GITHUB_WORKFLOW_REF:-unset}', expected '${want_ref}'"

# --- 2. package --------------------------------------------------------------------------------------------------------------
"${RELEASE_LIB_DIR}/chart-package.sh" verify --package "${package}" --image-digest "${image_digest}" --version "${version}"

# --- 3. tag ------------------------------------------------------------------------------------------------------------------
errfile="$(mktemp "${TMPDIR:-/tmp}/myriarch-push-chart.XXXXXX")"
trap 'rm -f "${errfile}" "${errfile}.out"' EXIT
if tags="$("${TOOLS_BIN}/crane" ls "${RELEASE_CHART}" 2> "${errfile}")"; then
  while IFS= read -r t; do
    [ "${t}" != "${version}" ] || err chart_tag_exists "${RELEASE_CHART}:${version} exists already; a chart tag is never overwritten"
  done <<EOT
${tags}
EOT
elif grep -qiE 'NAME_UNKNOWN|not found|404' "${errfile}"; then
  info "${RELEASE_CHART} does not exist yet"
else
  err push_failed "crane could not list the tags of ${RELEASE_CHART}: $(grep -v '^[[:space:]]*$' "${errfile}" | tail -n 1 | cut -c 1-300)"
fi

# --- 4. push -----------------------------------------------------------------------------------------------------------------
info "pushing ${package} to ${RELEASE_CHART}:${version}"
HELM_REGISTRY_CONFIG="${HOME}/.docker/config.json" "${TOOLS_BIN}/helm" push "${package}" "oci://${RELEASE_CHART%/*}" > "${errfile}.out" 2> "${errfile}" \
  || err push_failed "helm push failed: $(grep -v '^[[:space:]]*$' "${errfile}" | tail -n 1 | cut -c 1-300)"
digest="$(sed -n 's/^Digest: //p' "${errfile}.out" | head -n 1)"
check_match chart_digest_mismatch "the digest that helm push printed" "${digest}" "${DIGEST_RE}"
got="$("${TOOLS_BIN}/crane" digest "${RELEASE_CHART}:${version}" 2> "${errfile}")" || err chart_digest_mismatch "crane could not read ${RELEASE_CHART}:${version} back"
[ "${got}" = "${digest}" ] || err chart_digest_mismatch "the registry reports ${got} for ${RELEASE_CHART}:${version}, helm push printed ${digest}"
"${TOOLS_BIN}/crane" validate --remote "${RELEASE_CHART}@${digest}" >&2 || err chart_digest_mismatch "the chart ${RELEASE_CHART}@${digest} in the registry is incomplete or invalid"

# --- 5. sign -----------------------------------------------------------------------------------------------------------------
info "signing ${RELEASE_CHART}@${digest}"
"${TOOLS_BIN}/cosign" sign --yes "${RELEASE_CHART}@${digest}" >&2 || err sign_failed "cosign sign failed for ${RELEASE_CHART}@${digest}"
verify_image_signature "${RELEASE_CHART}" "${digest}" "${tag}" "${commit}"
info "verified the signature of ${RELEASE_CHART}@${digest}"
printf 'chart-digest=%s\n' "${digest}"
