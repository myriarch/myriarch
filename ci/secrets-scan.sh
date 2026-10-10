#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Secret scan (gitleaks, configuration .gitleaks.toml): scans an export of the committed files (git archive HEAD) of
# the repository, so that git-ignored local state is not part of the scan. Findings are printed redacted. Exit status 1
# when gitleaks finds something.
#
#   ci/secrets-scan.sh [repo-dir]              committed state (HEAD) of repo-dir (default: this repository)
#   SECRETS_SCAN_WORKTREE=1 ci/secrets-scan.sh the tracked and untracked, not ignored files of the work tree instead
#                                              (what a commit would contain)
#   GITLEAKS_CONFIG=file                       other configuration (default: .gitleaks.toml of repo-dir)
#
# The history is scanned once before publication, not by this gate: gitleaks git --redact -c .gitleaks.toml
# The tool is built by ci/install-tools.sh gitleaks.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
export PATH="${TOOLS_BIN}:${PATH}"
need_cmd git tar
repo="${1:-${ROOT}}"
config="${GITLEAKS_CONFIG:-${repo}/.gitleaks.toml}"
[ -f "${config}" ] || die "gitleaks configuration ${config} is missing"
have gitleaks || die "gitleaks not found: run ci/install-tools.sh gitleaks"

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-secrets.XXXXXX")"
trap 'rm -rf "${work}"' EXIT
mkdir "${work}/tree"
if [ "${SECRETS_SCAN_WORKTREE:-0}" = 1 ]; then
  # a file that is deleted in the work tree but still tracked is skipped
  ( cd "${repo}" && git ls-files -co --exclude-standard -z | while IFS= read -r -d '' f; do [ -e "$f" ] && printf '%s\0' "$f"; done | tar --null -T - -cf - ) | tar -xf - -C "${work}/tree"
else
  git -C "${repo}" archive --format=tar HEAD | tar -xf - -C "${work}/tree"
fi
log "gitleaks $(gitleaks version 2>/dev/null | head -n 1) over the export of ${repo}"
gitleaks dir --config "${config}" --redact --verbose --no-banner --no-color --exit-code 1 "${work}/tree"
