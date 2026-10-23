#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# helm lint of the charts. The server chart (deploy/helm/myriarch): with the default values, with the OpenShift profile, with the Dex quickstart and with the
# values of the production example (if present); helm lint checks the values against values.schema.json. Plus a trial package
# (helm package) that checks the Chart.yaml version, that the chart carries the project license (Apache-2.0 section 4(a): the
# published chart is distributed without the repository) and that it ships the values schema but not its hand-written source. The
# collector chart (deploy/helm/myriarch-collector): with the default values, as a DaemonSet, in container mode and with the OpenShift
# profile, plus the same trial package.
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

collector=deploy/helm/myriarch-collector
cmp -s LICENSE "${collector}/LICENSE" || die "${collector}/LICENSE is missing or differs from the LICENSE of the repository root: cp LICENSE ${collector}/LICENSE"
section_start lintcol "helm lint (${collector}: default values, daemonset, container mode, OpenShift profile)"
helm lint "${collector}"
helm lint "${collector}" --set mode=daemonset
helm lint "${collector}" --set containerMode.enabled=true --set containerMode.trustedRoot.existingConfigMap=myriarch-trusted-root
helm lint "${collector}" --set openshift.enabled=true
for f in "${collector}"/examples/*.yaml; do
  helm lint "${collector}" -f "${f}"
done
section_end lintcol

tmp="$(mktemp -d)"; trap 'rm -rf "${tmp}"' EXIT
for c in "${chart}" "${collector}"; do
  name="$(basename "${c}")"
  helm package "${c}" --version 0.0.0-ci --app-version 0.0.0-ci --destination "${tmp}" >/dev/null
  tar -xzOf "${tmp}/${name}-0.0.0-ci.tgz" "${name}/LICENSE" | cmp -s - LICENSE \
    || die "the packaged chart ${name} does not contain the LICENSE of the repository root"
  # the schema is part of the chart (Helm validates the values with it), its hand-written source is not (.helmignore)
  files="$(tar -tzf "${tmp}/${name}-0.0.0-ci.tgz")"
  grep -qx "${name}/values.schema.json" <<<"${files}" || die "the packaged chart ${name} does not contain values.schema.json"
  if grep -q 'values.schema.source.yaml' <<<"${files}"; then die "the packaged chart ${name} contains values.schema.source.yaml: list it in ${c}/.helmignore"; fi
done
log "Helm lint ok"
