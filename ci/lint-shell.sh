#!/bin/sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Static analysis (ShellCheck) of all scripts we ship or run in CI. POSIX sh, so it runs without bash.
# The tool must be on PATH or in the tool cache (ci/install-tools.sh shellcheck).
#
#   ci/lint-shell.sh
#
# Files listed in SHELLCHECK_SOFT_FILES (default: none) are checked, but their findings are only reported, not fatal.
# The scripts of deploy/examples and hack (the kind probe) are part of the strict set.
set -eu
cd "$(dirname "$0")/.."
# Standalone runs find shellcheck in the repo-local tool cache (ci/install-tools.sh shellcheck); an installed one still works.
PATH="${TOOLS_BIN:-$(pwd)/.cache/bin}:${PATH}"
command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck not found (ci/install-tools.sh shellcheck)" >&2; exit 1; }

strict="$(find assets deploy/scripts deploy/release deploy/examples ci contrib scripts hack -name '*.sh' -type f 2>/dev/null | sort)"
soft="${SHELLCHECK_SOFT_FILES:-}"
status=0

echo "shellcheck $(shellcheck --version | sed -n 's/^version: //p')"
# --severity=warning: errors and warnings are fatal, pure style hints (info/style) are not.
# -x follows "source" directives, -P . resolves ci/lib.sh relative to the repository root.
# shellcheck disable=SC2086
shellcheck --severity=warning -x -P .:SCRIPTDIR ${strict} || status=1

for f in ${soft}; do
  [ -f "${f}" ] || continue
  if ! shellcheck --severity=warning -x -P .:SCRIPTDIR "${f}"; then
    echo "NOTE: findings in ${f} (soft file) - reported, not fatal" >&2
  fi
done
exit "${status}"
