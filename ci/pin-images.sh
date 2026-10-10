#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Checks/updates the image digests in ci/versions.env (maintainer helper, does not run in CI).
#
#   ci/pin-images.sh           shows for each *_IMAGE entry whether the digest still matches the tag
#   ci/pin-images.sh --write   writes the current digests to ci/versions.env
#
# Needs crane (ci/install-tools.sh crane) and access to the registries (HTTPS_PROXY if needed). A new digest does NOT
# mean the image was reviewed: review the change (upstream release notes) before committing.
# The digest of MYRCTL_BASE_IMAGE is also pinned in deploy/docker/myrctl.Dockerfile; ci/check.sh compares them.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_cmd crane

write=0
[ "${1:-}" = "--write" ] && write=1
file="${CI_DIR}/versions.env"
status=0

lines=()
while IFS= read -r l || [ -n "${l}" ]; do lines+=("${l}"); done < "${file}"
for line in "${lines[@]}"; do
  case "${line}" in *_IMAGE=*@sha256:*) ;; *) continue ;; esac
  name="${line%%=*}"; ref="${line#*=}"
  tagref="${ref%@*}"; old="${ref#*@}"
  new="$(crane digest "${tagref}")" || { warn "${name}: ${tagref} cannot be resolved"; status=1; continue; }
  if [ "${new}" = "${old}" ]; then
    log "${name}: up to date (${new})"
  else
    log "${name}: ${old} -> ${new}"
    if [ "${write}" = 1 ]; then
      tmp="$(mktemp)"
      sed "s|^${name}=.*|${name}=${tagref}@${new}|" "${file}" > "${tmp}"
      cat "${tmp}" > "${file}"
      rm -f "${tmp}"
    else
      status=2
    fi
  fi
done
exit "${status}"
