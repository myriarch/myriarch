#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Drift check of the generated references: regenerates docs/reference/cli, docs/reference/api and the pages configuration.md,
# environment-variables.md and metrics.md of docs/reference (make docs-gen) and fails when the work tree differs from what is
# committed there. Run it on a clean tree (CI) or with the generated files committed.
#
#   ci/check-docs-gen.sh
#
# Language: everything is English (ADR-0020).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_cmd git make go

cd "${ROOT}"
log "regenerating the generated references: cli, api, configuration, environment variables and metrics (make docs-gen)"
make docs-gen >/dev/null

for target in docs/reference/cli docs/reference/api docs/reference/configuration.md docs/reference/environment-variables.md docs/reference/metrics.md; do
  changed="$(git status --porcelain --untracked-files=all -- "${target}")"
  if [ -n "${changed}" ]; then
    printf '%s\n' "${changed}" >&2
    die "${target} is out of date: run make docs-gen and commit"
  fi
  log "${target} is up to date"
done
