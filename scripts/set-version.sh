#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Sets the product version in one step: writes VERSION and every copy (Helm chart version and appVersion, web/package.json,
# OpenAPI info.version, the image tags of the production example and of the GitOps template; the table is in
# ci/version-copies.py), then regenerates what depends on them (make docs-gen, pnpm gen:api in web/) so that the gates stay
# green. Commit the result.
#
#   scripts/set-version.sh 0.1.0-rc.2
#
# The version is X.Y.Z or X.Y.Z-rc.N without a leading v and without leading zeros. VERSION names the NEXT tag
# (docs/releasing.md#versioning). Needs bash, python3, make, Go and pnpm with the dependencies of web/ installed.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
err() { printf '[set-version] ERROR: %s\n' "$*" >&2; exit 1; }

[ "$#" = 1 ] || err "usage: scripts/set-version.sh <X.Y.Z|X.Y.Z-rc.N>"
version="$1"
printf '%s' "${version}" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$' \
  || err "invalid version '${version}': expected X.Y.Z or X.Y.Z-rc.N without a leading v and without leading zeros"
for c in python3 make pnpm; do command -v "${c}" >/dev/null 2>&1 || err "${c} not found"; done

cd "${root}"
# every copy must be found before anything is written (ci/version-copies.py checks that first)
python3 -I ci/version-copies.py write "${version}"
printf '%s\n' "${version}" > VERSION
make docs-gen
(cd web && pnpm gen:api)
ci/check-versions.sh
printf '[set-version] version is now %s: commit VERSION and the changed files\n' "${version}" >&2
