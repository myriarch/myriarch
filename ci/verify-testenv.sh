#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Checks the production example (deploy/examples/production): myriarchd config validate, helm template, kustomize,
# optional kubeconform - via deploy/examples/production/verify.sh (see deploy/examples/production/README.md).
# Needs the myriarchd binary (linux/amd64) from the build artifact (dist/myriarchd_linux_amd64_v1/myriarchd, overridable with MYRIARCHD_BIN).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions
cd "${ROOT}"
verify="${VERIFY_SCRIPT:-deploy/examples/production/verify.sh}"   # VERIFY_SCRIPT: only for ci/tests/verify-testenv-test.sh
ci_out="${CI_OUT_DIR:-${ROOT}/ci-out}"
[ -x "${verify}" ] || [ -f "${verify}" ] || die "${verify} is missing"
tools=(helm yq kustomize)
# kubeconform fetches the Kubernetes JSON schemas from raw.githubusercontent.com by default (reachable only through
# HTTPS_PROXY in restricted networks): step 5 of verify.sh is therefore opt-in (CI variable VERIFY_KUBECONFORM=1).
if [ "${VERIFY_KUBECONFORM:-0}" = 1 ]; then tools+=(kubeconform); fi
"${CI_DIR}/install-tools.sh" "${tools[@]}"
export MYRIARCHD_BIN="${MYRIARCHD_BIN:-$(dist_binary myriarchd)}"
export HELM="${HELM:-helm}" YQ="${YQ:-yq}" KUSTOMIZE="${KUSTOMIZE:-kustomize}"
if [ "${VERIFY_KUBECONFORM:-0}" = 1 ]; then export KUBECONFORM="${KUBECONFORM:-kubeconform}"; else unset KUBECONFORM; fi
# verify.sh rejects an output directory inside the repository; therefore work outside the checkout and copy to
# ci-out/verify-testenv only on failure (job artifact, on_failure).
out_dir="${VERIFY_OUT:-$(mktemp -d)}"
export VERIFY_OUT="${out_dir}"
mkdir -p "${VERIFY_OUT}"
log "myriarchd: $("${MYRIARCHD_BIN}" version 2>&1 | head -n 1 || true)"
rc=0
bash "${verify}" || rc=$?
if [ "${rc}" -ne 0 ]; then
  rm -rf "${ci_out}/verify-testenv"
  mkdir -p "${ci_out}"
  cp -R "${VERIFY_OUT}" "${ci_out}/verify-testenv" 2>/dev/null || true
  exit "${rc}"
fi
