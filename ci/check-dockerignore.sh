#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Build context hygiene: a Dockerfile that copies the whole context (COPY . .) needs an ignore file next to it
# (<Dockerfile>.dockerignore) that covers every pattern of .gitignore, so that git-ignored local state (.cache/, .e2e/,
# ci-out/, editor and agent files, local binaries) does not enter a build stage or an exported build cache.
#
#   ci/check-dockerignore.sh [root]       root defaults to the repository
#
# Pattern mapping: an anchored or path pattern of .gitignore (/bin/, web/node_modules) needs the same path in the ignore file
# (bin, web/node_modules); a floating pattern (*.test, .DS_Store) needs the "**/" form (**/*.test, **/.DS_Store).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
root="${1:-${ROOT}}"
cd "${root}"
status=0
bad() { printf '[ci] ERROR: %s\n' "$*" >&2; status=1; }

[ -f .gitignore ] || { echo "check-dockerignore: no .gitignore in ${root}" >&2; exit 1; }
while IFS= read -r df; do
  grep -Eq '^[[:space:]]*COPY[[:space:]]+(--[a-z]+=[^[:space:]]+[[:space:]]+)*\.[[:space:]]+\.?/?[[:space:]]*$|^[[:space:]]*COPY[[:space:]]+(--[a-z]+=[^[:space:]]+[[:space:]]+)*\.[[:space:]]+[^[:space:]]+[[:space:]]*$' "${df}" || continue
  ig="${df}.dockerignore"
  if [ ! -f "${ig}" ]; then bad "${df} copies the whole build context, but ${ig} does not exist"; continue; fi
  while IFS= read -r pattern; do
    case "${pattern}" in ''|'#'*) continue ;; esac
    anchored=0
    case "${pattern}" in /*) anchored=1 ;; esac
    n="${pattern#/}"; n="${n%/}"
    case "${n}" in */*) anchored=1 ;; esac
    if [ "${anchored}" = 1 ]; then
      grep -Fxq -e "${n}" -e "${n}/" -e "/${n}" "${ig}" || bad "${ig} does not cover the .gitignore pattern ${pattern} (add: ${n})"
    else
      grep -Fxq -e "**/${n}" -e "**/${n}/" "${ig}" || bad "${ig} does not cover the .gitignore pattern ${pattern} (add: **/${n})"
    fi
  done < .gitignore
done < <(find . -name '*Dockerfile*' -type f ! -name '*.dockerignore' -not -path './.git/*' -not -path './web/node_modules/*' -not -path './.cache/*' | sed 's|^\./||' | sort)
exit "${status}"
