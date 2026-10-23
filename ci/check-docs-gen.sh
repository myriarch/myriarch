#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Drift check of the generated references: regenerates docs/reference/cli, docs/reference/api, the pages configuration.md,
# environment-variables.md and metrics.md of docs/reference, the values schema of the Helm chart,
# deploy/helm/myriarch/values.schema.json, and the values table in deploy/helm/myriarch/README.md (make docs-gen), and fails when the work tree differs from what is committed there. Run
# it on a clean tree (CI) or with the generated files committed.
#
#   ci/check-docs-gen.sh
#
# Language: everything is English (ADR-0020).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_cmd git make go

cd "${ROOT}"
log "regenerating the generated references: cli, api, configuration, environment variables, metrics, the values schemas and values tables of both charts (make docs-gen)"
make docs-gen >/dev/null

for target in docs/reference/cli docs/reference/api docs/reference/configuration.md docs/reference/environment-variables.md docs/reference/metrics.md deploy/helm/myriarch/values.schema.json deploy/helm/myriarch/README.md deploy/helm/myriarch-collector/values.schema.json deploy/helm/myriarch-collector/README.md; do
  changed="$(git status --porcelain --untracked-files=all -- "${target}")"
  if [ -n "${changed}" ]; then
    printf '%s\n' "${changed}" >&2
    die "${target} is out of date: run make docs-gen and commit"
  fi
  log "${target} is up to date"
done
