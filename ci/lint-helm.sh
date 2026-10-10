#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# helm lint of the chart: with the default values, with the OpenShift profile, with the Dex quickstart and with the
# values of the production example (if present); plus a trial package (helm package) that checks the Chart.yaml version and
# that the chart carries the project license (Apache-2.0 section 4(a): the published chart is distributed without the repository).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
need_cmd helm

chart=deploy/helm/myriarch
cmp -s LICENSE "${chart}/LICENSE" || die "${chart}/LICENSE is missing or differs from the LICENSE of the repository root: cp LICENSE ${chart}/LICENSE"
section_start lint "helm lint (default values)"
helm lint "${chart}"
section_end lint

section_start lintos "helm lint (--set openshift.enabled=true)"
helm lint "${chart}" --set openshift.enabled=true
section_end lintos

section_start lintdex "helm lint (examples/quickstart-dex.yaml)"
helm lint "${chart}" -f "${chart}/examples/quickstart-dex.yaml"
section_end lintdex

values=deploy/examples/production/myriarch-values.yaml
if [ -f "${values}" ]; then
  section_start lintenv "helm lint (${values})"
  helm lint "${chart}" -f "${values}"
  section_end lintenv
else
  warn "${values} is missing: second run skipped"
fi

tmp="$(mktemp -d)"; trap 'rm -rf "${tmp}"' EXIT
helm package "${chart}" --version 0.0.0-ci --app-version 0.0.0-ci --destination "${tmp}" >/dev/null
tar -xzOf "${tmp}/$(basename "${chart}")-0.0.0-ci.tgz" "$(basename "${chart}")/LICENSE" | cmp -s - LICENSE \
  || die "the packaged chart does not contain the LICENSE of the repository root"
log "Helm lint ok"
