#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Release tag guard: the first step of the release workflow. It refuses a tag that must not be released and touches
# nothing else. It reads its inputs from the environment only and never prints the token.
#
#   RELEASE_TAG          pushed tag, vX.Y.Z or vX.Y.Z-rc.N (no leading zeros)
#   RELEASE_COMMIT       full SHA (40 hex characters) the workflow was started for; it is also the checked-out commit
#   GITHUB_REPOSITORY    owner/name
#   GITHUB_API_URL       base URL of the REST API, https only (https://api.github.com)
#   GITHUB_TOKEN         token with `checks: read` on the repository (the guard job needs nothing else)
#
# It runs in the checkout of the tag, with `origin` as the remote that has the tag and `main`. It refuses (exit 1, one
# "guard: REFUSED" message) when:
#   1. the tag is not ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$
#   2. the tag is lightweight (the tag object is fetched from origin again: a checkout may have turned it into a lightweight one)
#   3. the tag points at another commit than RELEASE_COMMIT, or the checkout is not at RELEASE_COMMIT or has changes
#   4. the commit is not on origin/main
#   5. VERSION differs from the tag, or a copy of the version differs from VERSION (ci/check-versions.sh)
#   6. a final tag has no "## [X.Y.Z] - YYYY-MM-DD" heading in CHANGELOG.md
#   7. an rc tag finds the "Unreleased" section of CHANGELOG.md empty
#   8. the signing rule of deploy/release/windows-signing.env is broken: the mode must be off, signpath or kms (anything else
#      fails closed); a final tag with mode off passes only when WINDOWS_UNSIGNED_FINAL_TAG is exactly that tag; rc tags
#      may use off
#   9. the check run "ci-ok" of GitHub Actions is missing on the commit or not completed with success, or the API call fails
# Order: the checks that need no network first, the API call last.
set -euo pipefail
set +x
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

refuse() { printf 'guard: REFUSED: %s\n' "$*" >&2; exit 1; }
pass() { printf 'guard: ok: %s\n' "$*" >&2; }

tag="${RELEASE_TAG:-}"
sha="${RELEASE_COMMIT:-}"
repo="${GITHUB_REPOSITORY:-}"
api="${GITHUB_API_URL:-}"
token="${GITHUB_TOKEN:-}"

# --- 1. inputs and the tag name -----------------------------------------------------------------------------------------
tag_re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'
[ -n "${tag}" ] || refuse "RELEASE_TAG is not set"
printf '%s' "${tag}" | grep -Eq "${tag_re}" \
  || refuse "tag '${tag}' is not a release tag: expected vX.Y.Z or vX.Y.Z-rc.N (no leading zeros, rc number from 1)"
printf '%s' "${sha}" | grep -Eq '^[0-9a-f]{40}$' || refuse "RELEASE_COMMIT is not a full commit SHA (40 lower-case hex characters)"
printf '%s' "${repo}" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || refuse "GITHUB_REPOSITORY is not owner/name"
printf '%s' "${api}" | grep -Eq '^https://[A-Za-z0-9._:/-]+$' || refuse "GITHUB_API_URL is not an https URL"
[ -n "${token}" ] || refuse "GITHUB_TOKEN is not set (the check-run lookup needs a token with checks: read)"
printf '%s' "${token}" | grep -Eq '^[A-Za-z0-9_.~+/=-]+$' || refuse "GITHUB_TOKEN has an unexpected format"
for c in git python3 curl; do command -v "${c}" >/dev/null 2>&1 || refuse "${c} not found"; done
version="${tag#v}"
final=1; kind=final; case "${version}" in *-rc.*) final=0; kind=rc ;; esac
pass "tag ${tag} has a valid name (${kind} tag)"

# --- 2. annotated tag, fetched from origin ------------------------------------------------------------------------------
git fetch --quiet --no-tags origin "+refs/tags/${tag}:refs/tags/${tag}" || refuse "could not fetch tag ${tag} from origin"
[ "$(git cat-file -t "refs/tags/${tag}" 2>/dev/null || true)" = tag ] \
  || refuse "tag ${tag} is a lightweight tag: create an annotated one (git tag -a)"
pass "tag ${tag} is annotated"

# --- 3. the tag, the given commit and the checkout agree ----------------------------------------------------------------
tag_commit="$(git rev-parse "refs/tags/${tag}^{commit}" 2>/dev/null || true)"
[ "${tag_commit}" = "${sha}" ] \
  || refuse "tag ${tag} points at ${tag_commit:0:12}, not at the commit of this run (${sha:0:12}); the tag was moved or the run is for another commit"
[ "$(git rev-parse HEAD 2>/dev/null || true)" = "${sha}" ] || refuse "the checkout is not at commit ${sha:0:12}"
[ -z "$(git status --porcelain --untracked-files=no)" ] || refuse "the checkout has changes against commit ${sha:0:12}"
pass "tag ${tag} points at ${sha:0:12}, the checked-out commit"

# --- 4. the commit is on origin/main --------------------------------------------------------------------------------------
if [ "$(git rev-parse --is-shallow-repository 2>/dev/null || true)" = true ]; then
  git fetch --quiet --no-tags --unshallow origin 2>/dev/null || refuse "shallow checkout and --unshallow failed: fetch the full history (fetch-depth 0)"
fi
git fetch --quiet --no-tags origin "+refs/heads/main:refs/remotes/origin/main" || refuse "could not fetch branch main from origin"
git merge-base --is-ancestor "${sha}" refs/remotes/origin/main \
  || refuse "commit ${sha:0:12} of tag ${tag} is not on origin/main: release only from merged history; merge into main first and tag there"
pass "commit ${sha:0:12} is on origin/main"

# --- 5. VERSION and the copies -------------------------------------------------------------------------------------------
[ -f VERSION ] || refuse "VERSION is missing"
[ "$(cat VERSION)" = "${version}" ] \
  || refuse "VERSION holds '$(head -c 64 VERSION | tr -d '\r\n')' but the tag ${tag} needs ${version}: run scripts/set-version.sh ${version} in a pull request"
if ! drift="$(ci/check-versions.sh 2>&1)"; then
  printf '%s\n' "${drift}" | sed 's/^/guard:   /' >&2
  refuse "a copy of the version differs from VERSION (${version}): run scripts/set-version.sh ${version}"
fi
pass "VERSION and all copies state ${version}"

# --- 6. and 7. CHANGELOG -------------------------------------------------------------------------------------------------
[ -f CHANGELOG.md ] || refuse "CHANGELOG.md is missing"
if [ "${final}" = 1 ]; then
  python3 -I - "${version}" <<'PY' || refuse "CHANGELOG.md has no heading '## [${version}] - YYYY-MM-DD' with a valid date: move the Unreleased entries under it"
import datetime, re, sys
version = sys.argv[1]
pat = re.compile(r'^## \[' + re.escape(version) + r'\] - (\d{4}-\d{2}-\d{2})\s*$')
for line in open('CHANGELOG.md', encoding='utf-8'):
    m = pat.match(line)
    if m:
        try:
            datetime.date.fromisoformat(m.group(1))
        except ValueError:
            continue
        sys.exit(0)
sys.exit(1)
PY
  pass "CHANGELOG.md has the heading for ${version}"
else
  python3 -I - <<'PY' || refuse "the Unreleased section of CHANGELOG.md is missing or empty: an rc needs the changes it ships"
import re, sys
inside = False
content = False
for line in open('CHANGELOG.md', encoding='utf-8'):
    if re.match(r'^## ', line):
        if inside:
            break
        inside = bool(re.match(r'^## \[?Unreleased\]?\s*$', line))
        continue
    if inside and line.strip() and not line.startswith('#'):
        content = True
sys.exit(0 if content else 1)
PY
  pass "the Unreleased section of CHANGELOG.md has content"
fi

# --- 8. the signing rule -------------------------------------------------------------------------------------------------
env_file=deploy/release/windows-signing.env
[ -f "${env_file}" ] || refuse "${env_file} is missing"
mode="" exception="" seen_mode=0 seen_exception=0
while IFS= read -r line || [ -n "${line}" ]; do
  case "${line}" in
    ''|'#'*) ;;
    WINDOWS_SIGNING=*) [ "${seen_mode}" = 0 ] || refuse "${env_file}: WINDOWS_SIGNING is set twice"; seen_mode=1; mode="${line#*=}" ;;
    WINDOWS_UNSIGNED_FINAL_TAG=*) [ "${seen_exception}" = 0 ] || refuse "${env_file}: WINDOWS_UNSIGNED_FINAL_TAG is set twice"; seen_exception=1; exception="${line#*=}" ;;
    *) refuse "${env_file}: unexpected line (only comments, WINDOWS_SIGNING and WINDOWS_UNSIGNED_FINAL_TAG are allowed)" ;;
  esac
done < "${env_file}"
[ "${seen_mode}" = 1 ] || refuse "${env_file}: WINDOWS_SIGNING is missing"
[ "${seen_exception}" = 1 ] || refuse "${env_file}: WINDOWS_UNSIGNED_FINAL_TAG is missing (it may be empty, but the line must exist)"
case "${mode}" in off|signpath|kms) ;; *) refuse "${env_file}: WINDOWS_SIGNING is not off, signpath or kms (unknown modes fail closed)" ;; esac
if [ -n "${exception}" ]; then
  printf '%s' "${exception}" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' \
    || refuse "${env_file}: WINDOWS_UNSIGNED_FINAL_TAG must be empty or one final tag vX.Y.Z"
fi
if [ "${final}" = 1 ] && [ "${mode}" = off ]; then
  [ -n "${exception}" ] || refuse "final tag ${tag} would ship unsigned Windows files (WINDOWS_SIGNING=off) and ${env_file} names no exception: sign the files or set WINDOWS_UNSIGNED_FINAL_TAG=${tag} in a reviewed pull request"
  [ "${exception}" = "${tag}" ] || refuse "final tag ${tag} would ship unsigned Windows files (WINDOWS_SIGNING=off), but the exception in ${env_file} is for ${exception}"
  pass "final tag ${tag} is unsigned under the reviewed exception"
else
  pass "signing rule holds (mode ${mode}, ${tag})"
fi

# --- 9. the check run "ci-ok" on the commit -----------------------------------------------------------------------------
body="$(mktemp)"
trap 'rm -f "${body}"' EXIT
url="${api%/}/repos/${repo}/commits/${sha}/check-runs?check_name=ci-ok&filter=latest&per_page=100"
# the token goes in through a curl config on stdin, so it appears neither in the process list nor in the output
code="$(printf 'header = "Authorization: Bearer %s"\n' "${token}" | curl --config - --silent --show-error --proto '=https' --tlsv1.2 \
  --max-time 30 --retry 2 --output "${body}" --write-out '%{http_code}' \
  --header 'Accept: application/vnd.github+json' --header 'X-GitHub-Api-Version: 2022-11-28' --header 'User-Agent: myriarch-release-guard' \
  "${url}" 2>/dev/null)" || refuse "the GitHub API call for the check runs of ${sha:0:12} failed (network or TLS error)"
[ "${code}" = 200 ] || refuse "the GitHub API answered HTTP ${code} for the check runs of ${sha:0:12} (token without checks: read, or wrong repository?)"
python3 -I - "${body}" <<'PY' || refuse "no check run 'ci-ok' with conclusion success on commit ${sha:0:12}: wait for CI on main, or fix it, then tag again"
import json, sys
try:
    data = json.load(open(sys.argv[1], encoding='utf-8'))
    runs = data['check_runs']
    total = data['total_count']
    assert isinstance(runs, list) and isinstance(total, int)
except Exception:
    print('guard: the API answer is not a check-run list', file=sys.stderr)
    sys.exit(1)
if total > len(runs):
    print('guard: the API answer is incomplete (%d of %d check runs)' % (len(runs), total), file=sys.stderr)
    sys.exit(1)
mine = [r for r in runs if isinstance(r, dict) and r.get('name') == 'ci-ok'
        and isinstance(r.get('app'), dict) and r['app'].get('slug') == 'github-actions']
if not mine:
    print('guard: there is no check run ci-ok from GitHub Actions', file=sys.stderr)
    sys.exit(1)
latest = max(mine, key=lambda r: r.get('id', 0) if isinstance(r.get('id'), int) else 0)
if latest.get('status') != 'completed' or latest.get('conclusion') != 'success':
    print('guard: the latest ci-ok is %s/%s' % (latest.get('status'), latest.get('conclusion')), file=sys.stderr)
    sys.exit(1)
PY
pass "ci-ok succeeded on ${sha:0:12}"
printf 'guard: tag %s may be released\n' "${tag}" >&2
