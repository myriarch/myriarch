#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Lints the GitHub Actions workflows: actionlint (syntax, expressions, runner labels, ShellCheck over the run: blocks), zizmor
# (security audits: template injection, dangerous triggers, excessive permissions, credential persistence, cache poisoning,
# unpinned or impostor actions, Dependabot cooldown), then the project policy of ci/check-workflows.py (pinned actions, token
# permissions, triggers, secrets, gate parity, the Scorecard and release workflow rules).
#
#   ci/lint-workflows.sh
#
# The tools come from `ci/install-tools.sh actionlint shellcheck zizmor`. actionlint reads the workflows below .github/workflows
# (GitHub runs both extensions, .yml and .yaml); zizmor reads the same files, the local composite actions below .github/actions
# and .github/dependabot.yml; the policy check also covers the local composite actions.
#
# zizmor runs with the pedantic persona (it also asks for a reason next to a permission and for a concurrency group; the one
# workflow that cannot have one, scorecard.yml, whose shape rule R12 fixes, is ignored in zizmor.yml) and the configuration
# zizmor.yml in the repository root, which lists every ignored finding with its reason; rule R14 of the policy check allows
# nothing but ignore entries there. It runs offline: the audits that need the GitHub API (impostor commits, known vulnerable
# actions, ref confusion, stale action references) are skipped, so the gate needs no network and no token. ZIZMOR_ONLINE=1
# switches them on; the token comes from GH_TOKEN, GITHUB_TOKEN or ZIZMOR_GITHUB_TOKEN (the workflows job of ci.yml sets GH_TOKEN).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
export PATH="${TOOLS_BIN}:${PATH}"
need_cmd actionlint shellcheck zizmor python3
cd "${ROOT}"

# The files that the policy check reads: both extensions, in any letter case, and names with a leading dot (not a link that
# leads nowhere, such as the lock file .#ci.yml of an editor).
shopt -s nullglob dotglob nocaseglob
files=()
for f in .github/workflows/*.yml .github/workflows/*.yaml; do
  if [ -f "${f}" ]; then files+=("${f}"); fi
done
shopt -u nullglob dotglob nocaseglob
[ "${#files[@]}" -gt 0 ] || die "no workflow below .github/workflows"

log "actionlint $(actionlint -version | head -n 1) over ${#files[@]} workflow(s)"
# -pyflakes= switches the Python check off (no Python in the run: blocks); ShellCheck is the pinned binary.
actionlint -no-color -shellcheck "$(command -v shellcheck)" -pyflakes= "${files[@]}"

# The inputs of zizmor: the same workflow files, every action file below .github/actions and the Dependabot configuration.
# The environment must not be able to point zizmor at another configuration or to switch it offline or online by itself.
unset ZIZMOR_CONFIG ZIZMOR_OFFLINE ZIZMOR_NO_ONLINE_AUDITS
[ -f zizmor.yml ] || die "zizmor.yml is missing in the repository root"
inputs=("${files[@]}")
while IFS= read -r f; do inputs+=("${f}"); done < <(find .github/actions -type f \( -iname 'action.yml' -o -iname 'action.yaml' \) 2> /dev/null | LC_ALL=C sort)
for f in .github/dependabot.yml .github/dependabot.yaml; do
  if [ -f "${f}" ]; then inputs+=("${f}"); fi
done
zizmor_args=(--no-progress --persona pedantic --config zizmor.yml --cache-dir "${ROOT}/.cache/zizmor")
if [ "${ZIZMOR_ONLINE:-}" = 1 ]; then
  [ -n "${GH_TOKEN:-${GITHUB_TOKEN:-${ZIZMOR_GITHUB_TOKEN:-}}}" ] || die "ZIZMOR_ONLINE=1 needs GH_TOKEN, GITHUB_TOKEN or ZIZMOR_GITHUB_TOKEN"
  mode="online"
else
  zizmor_args+=(--offline)
  mode="offline"
fi
log "zizmor $(zizmor --version | head -n 1 | awk '{print $NF}') (${mode}) over ${#inputs[@]} file(s)"
zizmor "${zizmor_args[@]}" "${inputs[@]}"

log "policy check (ci/check-workflows.py)"
python3 -I ci/check-workflows.py
