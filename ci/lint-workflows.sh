#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Lints the GitHub Actions workflows: actionlint (syntax, expressions, runner labels, ShellCheck over the run: blocks),
# then the project policy of ci/check-workflows.py (pinned actions, token permissions, triggers, secrets, gate parity).
#
#   ci/lint-workflows.sh
#
# The tools come from `ci/install-tools.sh actionlint shellcheck`. actionlint reads .github/workflows; the policy check
# also covers the local composite actions below .github/actions.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
export PATH="${TOOLS_BIN}:${PATH}"
need_cmd actionlint shellcheck python3
cd "${ROOT}"

files=(.github/workflows/*.yml)
[ -f "${files[0]}" ] || die "no workflow below .github/workflows"

log "actionlint $(actionlint -version | head -n 1) over ${#files[@]} workflow(s)"
# -pyflakes= switches the Python check off (no Python in the run: blocks); ShellCheck is the pinned binary.
actionlint -no-color -shellcheck "$(command -v shellcheck)" -pyflakes= "${files[@]}"

log "policy check (ci/check-workflows.py)"
python3 -I ci/check-workflows.py
