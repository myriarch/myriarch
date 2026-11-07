#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# One version source: the file VERSION holds the version without the leading "v" (X.Y.Z or X.Y.Z-rc.N), and every place that
# states the product version must equal it (the table is in ci/version-copies.py). No text may still promise a release that exists
# (ci/check-stale-versions.py). Run by ci/check.sh and by the release tag guard (ci/release/guard.sh).
# Fix a drift with scripts/set-version.sh <version>. Needs bash and python3.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"
err() { printf '[ci] ERROR: %s\n' "$*" >&2; }

[ -f VERSION ] || { err "VERSION is missing (it holds the version without the leading v, for example 0.1.0-rc.1)"; exit 1; }
[ "$(wc -l < VERSION | tr -d ' ')" = 1 ] || { err "VERSION must hold exactly one line"; exit 1; }
version="$(cat VERSION)"
version_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'
[[ "${version}" =~ ${version_re} ]] \
  || { err "VERSION '${version}' is not X.Y.Z or X.Y.Z-rc.N (no leading zeros, no leading v)"; exit 1; }
command -v python3 >/dev/null 2>&1 || { err "python3 not found"; exit 1; }

if ! python3 -I ci/version-copies.py check "${version}"; then
  err "a copy of the version differs from VERSION (${version}): run scripts/set-version.sh ${version}"
  exit 1
fi
printf '[ci] check-versions.sh: all copies state %s\n' "${version}" >&2

# A promise of a release that exists is stale: "planned for" and "until" that version, or an older one. A release candidate has not
# released its X.Y.Z, so only older versions count for it.
if ! python3 -I ci/check-stale-versions.py "${version}"; then
  err "a text promises a release that exists now (${version} or an older one): fix the sentences above in the pull request that prepares the release"
  exit 1
fi
