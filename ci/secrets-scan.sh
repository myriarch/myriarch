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
#   SECRETS_SCAN_RANGE=<base>..<head>          the commits of that range instead (gitleaks git --log-opts): the pull
#                                              request job uses it, because a secret that one commit adds and a later one
#                                              removes stays in the history after the merge. Both ends are commit ids.
#   GITLEAKS_CONFIG=file                       other configuration (default: .gitleaks.toml of repo-dir)
#
# Without a range the history is not scanned by this gate (once before publication: gitleaks git --redact -c .gitleaks.toml).
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

range="${SECRETS_SCAN_RANGE:-}"
if [ -n "${range}" ]; then
  # only a range of two commit ids: the value goes to `git log`, so it must not be able to carry an option
  printf '%s' "${range}" | grep -Eq '^[0-9a-f]{7,64}\.\.[0-9a-f]{7,64}$' \
    || die "SECRETS_SCAN_RANGE must be <base>..<head> with two commit ids (got '${range}')"
  [ -z "${SECRETS_SCAN_WORKTREE:-}" ] || die "SECRETS_SCAN_RANGE and SECRETS_SCAN_WORKTREE cannot be combined"
  log "gitleaks $(gitleaks version 2>/dev/null | head -n 1) over the commits ${range} of ${repo}"
  exec gitleaks git --config "${config}" --redact --verbose --no-banner --no-color --exit-code 1 --log-opts "${range}" "${repo}"
fi

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
