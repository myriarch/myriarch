#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# myrctl validate for the quickstart repository, the GitOps test data and the generic GitOps starter:
#   myrctl validate --strict --components <list> examples/quickstart
#   myrctl validate --strict internal/testdata/gitops
#   myrctl validate --strict --components <list> gitops-template/config
# myrctl comes from the build artifact (dist/myrctl_linux_amd64_v1/myrctl; MYRCTL_BIN overrides it).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
myrctl="${MYRCTL_BIN:-$(dist_binary myrctl)}"
components=examples/sim/components-otelcol-contrib-0.162.0.json
[ -f "${components}" ] || die "${components} is missing"
for dir in examples/quickstart gitops-template/config; do
  [ -d "${dir}" ] || die "${dir} is missing"
  "${myrctl}" validate --strict --components "${components}" "${dir}"
done
# The test data deliberately contains constructs the component list does not cover (see internal/testdata/gitops).
[ -d internal/testdata/gitops ] || die "internal/testdata/gitops is missing"
"${myrctl}" validate --strict internal/testdata/gitops
log "GitOps validation ok"
