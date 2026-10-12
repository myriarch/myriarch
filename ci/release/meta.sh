#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Computes the release outputs of the guard job (ADR-0035) and enforces the frozen signing identity at run time. It prints GITHUB_OUTPUT
# lines on stdout and nothing else there.
#
#   RELEASE_TAG=v0.1.0-rc.1 RELEASE_COMMIT=<sha> ci/release/meta.sh >> "${GITHUB_OUTPUT}"
#
# Environment (the runner sets the last three):
#   RELEASE_TAG, RELEASE_COMMIT   the pushed tag (vX.Y.Z or vX.Y.Z-rc.N) and the commit of the run
#   GITHUB_REPOSITORY             must equal RELEASE_REPOSITORY of ci/release/identity.env             [repository_mismatch]
#   GITHUB_REF                    must equal refs/tags/<tag>                                           [ref_mismatch]
#   GITHUB_WORKFLOW_REF           must equal <repository>/<workflow>@refs/tags/<tag>: a renamed workflow, a call through a reusable
#                                 workflow or a run for a branch would sign with another identity than the one that every
#                                 installed verifier accepts (ADR-0035)                                [workflow_ref_mismatch]
#
# It also checks the tag format [tag_invalid], the commit [commit_invalid] and deploy/release/windows-signing.env [signing_env], and
# it reads the tags of the checkout (git tag --list 'v*'; the guard job checks out the full history) to find the previous release
# for the generated notes and to decide whether this tag is the latest release.
#
# Output keys, in this order: tag, version, base-version, commit, kind (rc|final), prerelease (true|false), latest (true|false),
# image-tags (rc: X.Y.Z-rc.N; final: X.Y.Z,X.Y; never latest), notes-start-tag, windows-signing, windows-unsigned-final-tag.
# notes-start-tag is the highest TAG below this one (a final tag looks at final tags only, an rc at every tag), or empty. A tag
# is not always a release: draft.sh uses it only when the tag has a published release.
# latest is true for a final tag that is higher than every other final tag. Versions are ordered numerically, and X.Y.Z-rc.N is
# below X.Y.Z.
#
# Exit status: 0 ok, 1 refused, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

[ "$#" -eq 0 ] || usage_err "RELEASE_TAG=<tag> RELEASE_COMMIT=<sha> meta.sh (no arguments)"
load_identity

tag="${RELEASE_TAG:-}"
commit="${RELEASE_COMMIT:-}"
check_match tag_invalid "RELEASE_TAG" "${tag}" "${TAG_RE}"
check_match commit_invalid "RELEASE_COMMIT" "${commit}" "${COMMIT_RE}"
check_text repository_mismatch "GITHUB_REPOSITORY" "${GITHUB_REPOSITORY:-}"
[ "${GITHUB_REPOSITORY:-}" = "${RELEASE_REPOSITORY}" ] \
  || err repository_mismatch "GITHUB_REPOSITORY is not ${RELEASE_REPOSITORY}: only that repository signs releases"
check_text ref_mismatch "GITHUB_REF" "${GITHUB_REF:-}"
[ "${GITHUB_REF:-}" = "refs/tags/${tag}" ] || err ref_mismatch "GITHUB_REF is not refs/tags/${tag}"
check_text workflow_ref_mismatch "GITHUB_WORKFLOW_REF" "${GITHUB_WORKFLOW_REF:-}"
want_ref="${RELEASE_REPOSITORY}/${RELEASE_WORKFLOW}@refs/tags/${tag}"
[ "${GITHUB_WORKFLOW_REF:-}" = "${want_ref}" ] \
  || err workflow_ref_mismatch "the signing identity would not be the frozen identity of ADR-0035 (release.yml at the tag): GITHUB_WORKFLOW_REF is not ${want_ref}"

# --- the signing rule (the same parsing as ci/release/guard.sh) ----------------------------------------------------------------
env_file="${ROOT}/deploy/release/windows-signing.env"
[ -f "${env_file}" ] || err signing_env "deploy/release/windows-signing.env is missing"
mode="" exception="" seen_mode=0 seen_exception=0
while IFS= read -r line || [ -n "${line}" ]; do
  check_text signing_env "a line of windows-signing.env" "${line}"
  case "${line}" in
    ''|'#'*) ;;
    WINDOWS_SIGNING=*)
      [ "${seen_mode}" = 0 ] || err signing_env "WINDOWS_SIGNING is set twice"
      seen_mode=1; mode="${line#*=}" ;;
    WINDOWS_UNSIGNED_FINAL_TAG=*)
      [ "${seen_exception}" = 0 ] || err signing_env "WINDOWS_UNSIGNED_FINAL_TAG is set twice"
      seen_exception=1; exception="${line#*=}" ;;
    *) err signing_env "unexpected line (only comments, WINDOWS_SIGNING and WINDOWS_UNSIGNED_FINAL_TAG are allowed)" ;;
  esac
done < "${env_file}"
[ "${seen_mode}" = 1 ] || err signing_env "WINDOWS_SIGNING is missing"
[ "${seen_exception}" = 1 ] || err signing_env "WINDOWS_UNSIGNED_FINAL_TAG is missing"
case "${mode}" in off|signpath|kms) ;; *) err signing_env "WINDOWS_SIGNING is not off, signpath or kms" ;; esac
if [ -n "${exception}" ]; then
  [[ "${exception}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] \
    || err signing_env "WINDOWS_UNSIGNED_FINAL_TAG must be empty or one final tag vX.Y.Z"
fi

version="${tag#v}"
base="${version%%-*}"
kind=final
prerelease=false
image_tags="${base},${base%.*}"
case "${version}" in
  *-rc.*) kind=rc; prerelease=true; image_tags="${version}" ;;
esac

# --- previous tag and latest -----------------------------------------------------------------------------------------------
have git || err tool_missing "git was not found"
tags="$(git -C "${ROOT}" tag --list 'v*')" || err tag_list "cannot list the tags of the checkout"
res="$(printf '%s\n' "${tags}" | python3 -I -c '
import re, sys
TAG = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.([1-9][0-9]*))?$")
def key(t):
    m = TAG.match(t)
    x, y, z, rc = int(m.group(1)), int(m.group(2)), int(m.group(3)), m.group(4)
    return (x, y, z, 0, int(rc)) if rc else (x, y, z, 1, 0)
cur = sys.argv[1]
tags = sorted({t for t in sys.stdin.read().split() if TAG.match(t)}, key=key)
final = "-rc." not in cur
pool = [t for t in tags if t != cur and (not final or "-rc." not in t) and key(t) < key(cur)]
prev = pool[-1] if pool else ""
latest = final and not any("-rc." not in t and t != cur and key(t) > key(cur) for t in tags)
print(prev)
print("true" if latest else "false")
' "${tag}")" || err tag_list "cannot order the tags of the checkout"
prev="$(printf '%s\n' "${res}" | sed -n '1p')"
latest="$(printf '%s\n' "${res}" | sed -n '2p')"

{
  printf 'tag=%s\n' "${tag}"
  printf 'version=%s\n' "${version}"
  printf 'base-version=%s\n' "${base}"
  printf 'commit=%s\n' "${commit}"
  printf 'kind=%s\n' "${kind}"
  printf 'prerelease=%s\n' "${prerelease}"
  printf 'latest=%s\n' "${latest}"
  printf 'image-tags=%s\n' "${image_tags}"
  printf 'notes-start-tag=%s\n' "${prev}"
  printf 'windows-signing=%s\n' "${mode}"
  printf 'windows-unsigned-final-tag=%s\n' "${exception}"
}
info "${tag}: ${kind} release, latest=${latest}, previous release '${prev:-none}'"
