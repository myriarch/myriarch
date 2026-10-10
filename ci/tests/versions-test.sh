#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intended (code for bash -c)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the one version source and the release tag guard:
#   scripts/set-version.sh, ci/check-versions.sh and ci/version-copies.py (VERSION and the copies),
#   ci/release/guard.sh (every refusal, an rc that passes, a final tag with the reviewed exception).
# Temporary git repositories (origin = bare repository), a stub curl for the GitHub API and stubs for make and pnpm on PATH.
# No network, no fixed ports.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
have git || { echo "git is missing: test skipped"; exit 0; }
have python3 || { echo "python3 is missing: test skipped"; exit 0; }
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

TOKEN="ghs_TestTokenValue0123456789"
STUB_BIN="${TEST_TMP}/stubbin"
mkdir -p "${STUB_BIN}"

# --- stubs ----------------------------------------------------------------------------------------------------------------
# curl: records its arguments and its standard input, writes the fixture STUB_CURL_BODY to the --output file and prints the
# status STUB_CURL_STATUS for --write-out; STUB_CURL_EXIT makes it fail like a network error.
cat > "${STUB_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
out="" url=""
printf '%s\n' "$*" >> "${STUB_CURL_LOG:?}"
while [ $# -gt 0 ]; do
  case "$1" in
    --output|-o) out="$2"; shift 2 ;;
    --write-out|-w|--config|-K|--header|-H|--max-time|--retry|--proto) shift 2 ;;
    --*) shift ;;
    *) url="$1"; shift ;;
  esac
done
cat >> "${STUB_CURL_STDIN:?}"
printf '%s\n' "${url}" >> "${STUB_CURL_URLS:?}"
[ "${STUB_CURL_EXIT:-0}" = 0 ] || exit "${STUB_CURL_EXIT}"
[ -z "${out}" ] || cp "${STUB_CURL_BODY:?}" "${out}"
printf '%s' "${STUB_CURL_STATUS:-200}"
EOF
printf '#!/usr/bin/env bash\nprintf "make %%s\\n" "$*" >> "${STUB_GEN_LOG:?}"\n' > "${STUB_BIN}/make"
printf '#!/usr/bin/env bash\nprintf "pnpm %%s (in %%s)\\n" "$*" "$(basename "$PWD")" >> "${STUB_GEN_LOG:?}"\n' > "${STUB_BIN}/pnpm"
chmod +x "${STUB_BIN}/curl" "${STUB_BIN}/make" "${STUB_BIN}/pnpm"
export STUB_CURL_LOG="${TEST_TMP}/curl.args" STUB_CURL_STDIN="${TEST_TMP}/curl.stdin" STUB_CURL_URLS="${TEST_TMP}/curl.urls"
export STUB_GEN_LOG="${TEST_TMP}/gen.log"
: > "${STUB_CURL_LOG}"; : > "${STUB_CURL_STDIN}"; : > "${STUB_CURL_URLS}"; : > "${STUB_GEN_LOG}"

# python3 text edit: edit <file> <old> <new> (the old text must occur)
edit() { python3 -I - "$1" "$2" "$3" <<'PY'
import sys
p, old, new = sys.argv[1:4]
s = open(p, encoding="utf-8").read()
if old not in s:
    sys.exit("edit: %r not in %s" % (old, p))
open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
}

# --- the baseline tree: the real scripts and copies, set to 0.1.0-rc.1 -------------------------------------------------------
# tree <dir>: scripts, the files named in ci/version-copies.py, a CHANGELOG and the signing file
tree() {
  local d="$1" f
  mkdir -p "${d}/ci/release" "${d}/scripts" "${d}/deploy/release"
  cp "${ROOT}/ci/check-versions.sh" "${ROOT}/ci/version-copies.py" "${d}/ci/"
  cp "${ROOT}/ci/release/guard.sh" "${d}/ci/release/"
  cp "${ROOT}/scripts/set-version.sh" "${d}/scripts/"
  cp "${ROOT}/deploy/release/windows-signing.env" "${d}/deploy/release/"
  while IFS= read -r f; do
    mkdir -p "${d}/$(dirname "${f}")"
    cp "${ROOT}/${f}" "${d}/${f}"
  done < <(python3 -I "${ROOT}/ci/version-copies.py" list | sort -u)
  python3 -I "${d}/ci/version-copies.py" write 0.1.0-rc.1 >/dev/null
  printf '0.1.0-rc.1\n' > "${d}/VERSION"
  # the baseline signing file: mode off, no exception
  printf 'WINDOWS_SIGNING=off\nWINDOWS_UNSIGNED_FINAL_TAG=\n' > "${d}/deploy/release/windows-signing.env"
  cat > "${d}/CHANGELOG.md" <<'EOF'
# Changelog

## Unreleased

### Added

- A new feature.

## [0.0.1] - 2026-01-01

- An old feature.
EOF
}

# ===== set-version.sh, check-versions.sh, version-copies.py ================================================================
echo "== VERSION and the copies"
V="${TEST_TMP}/v"; tree "${V}"
cv() { ( cd "${V}" && ci/check-versions.sh ); }
sv() { ( cd "${V}" && PATH="${STUB_BIN}:${PATH}" scripts/set-version.sh "$@" ); }
snapshot() { ( cd "${V}" && find . -type f -print0 | sort -z | xargs -0 cksum ); }

expect_ok "check-versions passes on a consistent tree" cv
copies="$(python3 -I "${V}/ci/version-copies.py" list | sort -u)"
[ "$(printf '%s\n' "${copies}" | wc -l | tr -d ' ')" -ge 5 ] && ok "the copy table names at least five files" || fail "the copy table names fewer than five files"
for f in deploy/helm/myriarch/Chart.yaml web/package.json api/openapi.yaml; do
  printf '%s\n' "${copies}" | grep -qx "${f}" && ok "the copy table names ${f}" || fail "the copy table does not name ${f}"
done

for bad in v0.1.0 0.1 0.1.0.1 01.2.3 1.02.3 1.2.03 0.1.0-rc.0 0.1.0-rc.01 0.1.0-rc 0.1.0-beta.1 1.0.0-alpha.1 0.1.0-rc.1+build ' 0.1.0' '0.1.0 ' ''; do
  before="$(snapshot)"
  : > "${STUB_GEN_LOG}"
  expect_fail "set-version refuses '${bad}'" "invalid version|usage" sv "${bad}"
  [ "$(snapshot)" = "${before}" ] && ok "  and changes no file" || fail "set-version '${bad}' changed files"
  [ ! -s "${STUB_GEN_LOG}" ] && ok "  and runs no generator" || fail "set-version '${bad}' ran a generator"
done
expect_fail "set-version without an argument" "usage" sv
expect_fail "set-version with two arguments" "usage" sv 0.1.0 0.1.1

: > "${STUB_GEN_LOG}"
expect_ok "set-version 0.2.0" sv 0.2.0
[ "$(cat "${V}/VERSION")" = 0.2.0 ] && ok "  VERSION holds 0.2.0" || fail "  VERSION is '$(cat "${V}/VERSION")'"
expect_ok "  check-versions passes afterwards" cv
python3 -I "${V}/ci/version-copies.py" read | awk -F'\t' '$3 != "0.2.0" { bad = 1 } END { exit bad }' && ok "  every copy states 0.2.0" || fail "  a copy does not state 0.2.0"
grep -qx 'make docs-gen' "${STUB_GEN_LOG}" && ok "  runs make docs-gen" || fail "  does not run make docs-gen"
grep -qx 'pnpm gen:api (in web)' "${STUB_GEN_LOG}" && ok "  runs pnpm gen:api in web/" || fail "  does not run pnpm gen:api in web/"
grep -q '^  version: 0.2.0$' "${V}/api/openapi.yaml" && ok "  OpenAPI info.version moved" || fail "  OpenAPI info.version did not move"
grep -q '^appVersion: "0.2.0"$' "${V}/deploy/helm/myriarch/Chart.yaml" && ok "  appVersion moved" || fail "  appVersion did not move"
expect_ok "set-version 0.2.1-rc.12" sv 0.2.1-rc.12
expect_ok "  check-versions passes for an rc" cv
expect_ok "set-version is idempotent" sv 0.2.1-rc.12

# drift: each file of the table, changed alone
while IFS= read -r f; do
  cp -R "${V}" "${TEST_TMP}/drift" 2>/dev/null
  python3 -I - "${TEST_TMP}/drift/${f}" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
new = s.replace("0.2.1-rc.12", "9.9.9")
if new == s:
    sys.exit("fixture does not contain the version")
open(p, "w", encoding="utf-8").write(new)
PY
  expect_fail "drift in ${f} is detected" "${f}.*9\.9\.9|differs from VERSION" bash -c "cd '${TEST_TMP}/drift' && ci/check-versions.sh"
  rm -rf "${TEST_TMP}/drift"
done <<< "${copies}"

# VERSION itself
cp -R "${V}" "${TEST_TMP}/vv"
vcv() { ( cd "${TEST_TMP}/vv" && ci/check-versions.sh ); }
printf '0.2.2\n' > "${TEST_TMP}/vv/VERSION";            expect_fail "VERSION differs from the copies" "differs from VERSION" vcv
printf 'v0.2.1-rc.12\n' > "${TEST_TMP}/vv/VERSION";     expect_fail "VERSION with a leading v" "is not X.Y.Z" vcv
printf '0.2.1-rc.12\n0.2.1-rc.12\n' > "${TEST_TMP}/vv/VERSION"; expect_fail "VERSION with two lines" "exactly one line" vcv
printf '01.2.1\n' > "${TEST_TMP}/vv/VERSION";           expect_fail "VERSION with a leading zero" "is not X.Y.Z" vcv
rm -f "${TEST_TMP}/vv/VERSION";                          expect_fail "VERSION missing" "VERSION is missing" vcv
printf '0.2.1-rc.12\n' > "${TEST_TMP}/vv/VERSION"
edit "${TEST_TMP}/vv/web/package.json" '"version": "0.2.1-rc.12",' '"name2": "x",'
expect_fail "a copy whose layout changed (no place found)" "expected exactly one place" vcv
rm -rf "${TEST_TMP}/vv"

# ===== ci/release/guard.sh ==================================================================================================
echo "== release tag guard"
BASE="${TEST_TMP}/base"
mkdir -p "${BASE}/seed"
tree "${BASE}/seed"
git init -q -b main "${BASE}/seed"
git -C "${BASE}/seed" add -A
git -C "${BASE}/seed" commit -q -m "baseline"
git clone -q --bare "${BASE}/seed" "${BASE}/origin.git"

S="${TEST_TMP}/s"; W="${S}/work"
fresh() { # new origin and clone from the baseline; sets W
  rm -rf "${S}"; mkdir -p "${S}"
  cp -R "${BASE}/origin.git" "${S}/origin.git"
  git clone -q "${S}/origin.git" "${W}" 2>/dev/null
  : > "${STUB_CURL_LOG}"; : > "${STUB_CURL_STDIN}"; : > "${STUB_CURL_URLS}"
  ci_body success
  export STUB_CURL_STATUS=200 STUB_CURL_EXIT=0
}
# ci_body <conclusion> [status] [app slug]: the answer of the check-runs API with one ci-ok run
ci_body() {
  local concl="$1" status="${2:-completed}" slug="${3:-github-actions}"
  export STUB_CURL_BODY="${TEST_TMP}/body.json"
  printf '{"total_count":1,"check_runs":[{"id":10,"name":"ci-ok","status":"%s","conclusion":"%s","app":{"slug":"%s"}}]}\n' \
    "${status}" "${concl}" "${slug}" > "${STUB_CURL_BODY}"
}
head_sha() { git -C "${W}" rev-parse HEAD; }
# push_main <message>: commits the work tree changes to main and pushes
push_main() { git -C "${W}" add -A; git -C "${W}" commit -q -m "$1"; git -C "${W}" push -q origin main; }
# tag_annotated <tag> [commit]: annotated tag in the clone and on origin
tag_annotated() { git -C "${W}" tag -a -m "release ${1}" "$1" ${2:+"$2"}; git -C "${W}" push -q origin "refs/tags/${1}"; }
# final_tree <X.Y.Z>: VERSION, copies and CHANGELOG of a final release
final_tree() {
  python3 -I "${W}/ci/version-copies.py" write "$1" >/dev/null
  printf '%s\n' "$1" > "${W}/VERSION"
  python3 -I - "${W}/CHANGELOG.md" "$1" <<'PY'
import sys
p, v = sys.argv[1:3]
s = open(p, encoding="utf-8").read()
s = s.replace("## Unreleased\n", "## Unreleased\n\n## [%s] - 2026-10-20\n" % v, 1)
open(p, "w", encoding="utf-8").write(s)
PY
}
signing() { printf 'WINDOWS_SIGNING=%s\nWINDOWS_UNSIGNED_FINAL_TAG=%s\n' "$1" "${2:-}" > "${W}/deploy/release/windows-signing.env"; }

# guard <tag> <sha> [VAR=value ...]: runs the guard in the clone with the standard environment
guard() {
  local tag="$1" sha="$2"; shift 2
  ( cd "${W}" && env PATH="${STUB_BIN}:${PATH}" RELEASE_TAG="${tag}" RELEASE_COMMIT="${sha}" GITHUB_REPOSITORY=example/myriarch \
      GITHUB_API_URL=https://api.example.test GITHUB_TOKEN="${TOKEN}" "$@" bash ci/release/guard.sh )
}
no_token() { case "${OUT}" in *"${TOKEN}"*) fail "  the guard output contains the token" ;; *) : ;; esac; }
# gpass / grefuse <name> [<message regex>] <tag> <sha> [VAR=value ...]
gpass() { local n="$1"; shift; expect_ok "${n}" guard "$@"; no_token; }
grefuse() { local n="$1" m="$2"; shift 2; expect_fail "${n}" "guard: REFUSED.*(${m})" guard "$@"; no_token; }

# --- an rc that passes -------------------------------------------------------------------------------------------------
fresh; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
gpass "rc tag on main with an annotated tag, ci-ok green" v0.1.0-rc.1 "${sha}"
grep -q "guard: tag v0.1.0-rc.1 may be released" <<< "${OUT}" && ok "  reports the tag as releasable" || fail "  no final message"
grep -q '/repos/example/myriarch/commits/'"${sha}"'/check-runs?check_name=ci-ok' "${STUB_CURL_URLS}" && ok "  asks the check-runs API for the commit" || fail "  wrong API URL: $(cat "${STUB_CURL_URLS}")"
grep -q "Authorization: Bearer ${TOKEN}" "${STUB_CURL_STDIN}" && ok "  sends the token in the curl config on stdin" || fail "  the token did not reach curl on stdin"
grep -q "${TOKEN}" "${STUB_CURL_LOG}" && fail "  the token is in the curl arguments" || ok "  the token is not in the curl arguments"
# a checkout may have turned the annotated tag into a lightweight one: the guard fetches the real tag object again
git -C "${W}" tag -d v0.1.0-rc.1 >/dev/null; git -C "${W}" tag v0.1.0-rc.1 "${sha}"
gpass "rc tag whose local ref is lightweight but whose tag on origin is annotated" v0.1.0-rc.1 "${sha}"

# --- tag names ---------------------------------------------------------------------------------------------------------------
for bad in 0.1.0-rc.1 v1 v1.0 v01.0.0 v0.01.0 v0.1.00 v0.1.0-rc.0 v0.1.0-rc.01 v0.1.0-rc v0.1.0-beta.1 v0.1.0-rc.1.1 v0.1.0+build v0.1.0-rc.1\ ; do
  grefuse "tag name '${bad}' is refused" "not a release tag" "${bad}" "${sha}"
done
grefuse "empty tag is refused" "RELEASE_TAG is not set" "" "${sha}"
grefuse "short SHA is refused" "not a full commit SHA" v0.1.0-rc.1 "${sha:0:12}"
grefuse "upper-case SHA is refused" "not a full commit SHA" v0.1.0-rc.1 "$(printf '%s' "${sha}" | tr a-f A-F)"
grefuse "a tag that does not exist on origin" "could not fetch tag" v0.1.0-rc.7 "${sha}"
grefuse "a plain-http API URL" "not an https URL" v0.1.0-rc.1 "${sha}" GITHUB_API_URL=http://api.example.test
grefuse "a repository name with a space" "not owner/name" v0.1.0-rc.1 "${sha}" "GITHUB_REPOSITORY=example/my repo"
grefuse "no token" "GITHUB_TOKEN is not set" v0.1.0-rc.1 "${sha}" GITHUB_TOKEN=

# --- lightweight tag -----------------------------------------------------------------------------------------------------------
fresh; sha="$(head_sha)"; git -C "${W}" tag v0.1.0-rc.1; git -C "${W}" push -q origin refs/tags/v0.1.0-rc.1
grefuse "lightweight tag" "lightweight" v0.1.0-rc.1 "${sha}"

# --- tag, SHA and checkout -----------------------------------------------------------------------------------------------------
fresh; old="$(head_sha)"; printf 'x\n' > "${W}/note.txt"; push_main "second commit"; new="$(head_sha)"
tag_annotated v0.1.0-rc.1 "${old}"
grefuse "tag points at another commit than the run's SHA" "points at .*not at the commit of this run" v0.1.0-rc.1 "${new}"
git -C "${W}" checkout -q "${old}" 2>/dev/null
tag_annotated v0.1.0-rc.2 "${new}"
grefuse "checkout is not at the SHA" "not at commit" v0.1.0-rc.2 "${new}"
git -C "${W}" checkout -q main
printf 'dirty\n' >> "${W}/VERSION"
grefuse "checkout with changes against the commit" "has changes" v0.1.0-rc.2 "${new}"
git -C "${W}" checkout -q -- VERSION

# --- not on origin/main --------------------------------------------------------------------------------------------------------
fresh; git -C "${W}" checkout -q -b feature; printf 'f\n' > "${W}/feature.txt"; git -C "${W}" add -A; git -C "${W}" commit -q -m feature
git -C "${W}" push -q origin feature; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "commit only on a feature branch" "not on origin/main" v0.1.0-rc.1 "${sha}"
# a shallow checkout is completed by the guard
fresh; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
rm -rf "${W}"; git clone -q --depth 1 "file://${S}/origin.git" "${W}" 2>/dev/null
gpass "shallow checkout is unshallowed and passes" v0.1.0-rc.1 "${sha}"

# --- VERSION and copies ---------------------------------------------------------------------------------------------------------
fresh; printf '0.1.0-rc.2\n' > "${W}/VERSION"; push_main "VERSION without copies"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.2
grefuse "VERSION differs from the copies" "differs from VERSION" v0.1.0-rc.2 "${sha}"
fresh; python3 -I "${W}/ci/version-copies.py" write 0.1.0-rc.2 >/dev/null; push_main "copies without VERSION"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.2
grefuse "VERSION differs from the tag" "VERSION holds '0.1.0-rc.1' but the tag v0.1.0-rc.2 needs 0.1.0-rc.2" v0.1.0-rc.2 "${sha}"
fresh; edit "${W}/deploy/helm/myriarch/Chart.yaml" 'appVersion: "0.1.0-rc.1"' 'appVersion: "0.1.0"'; push_main "drifted appVersion"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "a copy (Chart appVersion) differs from VERSION" "copy of the version differs" v0.1.0-rc.1 "${sha}"
fresh; edit "${W}/api/openapi.yaml" 'version: 0.1.0-rc.1' 'version: 1.0.0-alpha.1'; push_main "drifted OpenAPI version"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "a copy (OpenAPI info.version) differs from VERSION" "copy of the version differs" v0.1.0-rc.1 "${sha}"
fresh; edit "${W}/web/package.json" '"version": "0.1.0-rc.1"' '"version": "0.1.0-rc.0"'; push_main "drifted web version"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "a copy (web/package.json) differs from VERSION" "copy of the version differs" v0.1.0-rc.1 "${sha}"

# --- CHANGELOG -----------------------------------------------------------------------------------------------------------------
fresh; printf '# Changelog\n\n## Unreleased\n\n### Added\n\n## [0.0.1] - 2026-01-01\n\n- Old.\n' > "${W}/CHANGELOG.md"; push_main "empty Unreleased"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "rc with an empty Unreleased section (only a subheading)" "Unreleased section.*empty" v0.1.0-rc.1 "${sha}"
fresh; printf '# Changelog\n\n## [0.0.1] - 2026-01-01\n\n- Old.\n' > "${W}/CHANGELOG.md"; push_main "no Unreleased"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "rc without an Unreleased heading" "Unreleased section.*missing" v0.1.0-rc.1 "${sha}"
fresh; printf '# Changelog\n\n## Unreleased\n\n\n\n## [0.0.1] - 2026-01-01\n\n- Old entry in an older section.\n' > "${W}/CHANGELOG.md"; push_main "blank Unreleased"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "rc whose Unreleased section is blank (content only below the next heading)" "Unreleased section.*empty" v0.1.0-rc.1 "${sha}"
fresh; printf '# Changelog\n\n## [Unreleased]\n\n- Entry.\n' > "${W}/CHANGELOG.md"; push_main "bracketed Unreleased"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
gpass "rc with a bracketed [Unreleased] heading and an entry" v0.1.0-rc.1 "${sha}"

# --- final tags and the signing rule ----------------------------------------------------------------------------------------------
final() { # <tag> <signing mode> <exception> -> commit and tag a final release state; prints nothing, sets sha
  fresh; final_tree "${1#v}"; signing "$2" "${3:-}"; push_main "prepare ${1}"; sha="$(head_sha)"; tag_annotated "$1"
}
final v0.1.0 off v0.1.0
gpass "final tag with mode off and the exception for exactly this tag" v0.1.0 "${sha}"
grep -q "unsigned under the reviewed exception" <<< "${OUT}" && ok "  says that the exception applies" || fail "  does not mention the exception"
final v0.1.0 off ""
grefuse "final tag with mode off and an empty exception" "names no exception" v0.1.0 "${sha}"
final v0.1.0 off v0.2.0
grefuse "final tag with mode off and the exception for another final tag" "exception in .* is for v0.2.0" v0.1.0 "${sha}"
final v0.1.0 off v0.1.0-rc.1
grefuse "final tag with mode off and an rc tag as the exception" "must be empty or one final tag" v0.1.0 "${sha}"
final v0.1.0 off v0.1.0-rc.1
final v0.1.0 off "v0.1.*"
grefuse "final tag with mode off and a pattern as the exception" "must be empty or one final tag" v0.1.0 "${sha}"
final v0.1.1 off v0.1.0
grefuse "the exception does not carry over to the next final tag" "exception in .* is for v0.1.0" v0.1.1 "${sha}"
final v0.1.0 signpath ""
gpass "final tag with mode signpath and no exception" v0.1.0 "${sha}"
final v0.1.0 kms ""
gpass "final tag with mode kms and no exception" v0.1.0 "${sha}"
final v0.1.0 sign-path v0.1.0
grefuse "unknown mode fails closed" "not off, signpath or kms" v0.1.0 "${sha}"
final v0.1.0 OFF v0.1.0
grefuse "mode in the wrong letter case fails closed" "not off, signpath or kms" v0.1.0 "${sha}"
final v0.1.0 "" v0.1.0
grefuse "empty mode fails closed" "not off, signpath or kms" v0.1.0 "${sha}"

# rc tags may be off, with or without an exception; an unknown mode still fails closed
fresh; signing off v0.1.0; push_main "off with an exception for a final tag"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
gpass "rc tag with mode off and a final exception" v0.1.0-rc.1 "${sha}"
fresh; signing weird ""; push_main "unknown mode"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "rc tag with an unknown mode fails closed" "not off, signpath or kms" v0.1.0-rc.1 "${sha}"
fresh; signing kms ""; push_main "kms"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
gpass "rc tag with mode kms" v0.1.0-rc.1 "${sha}"

# the signing file itself
fresh; rm "${W}/deploy/release/windows-signing.env"; push_main "no signing file"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "signing file missing" "windows-signing.env is missing" v0.1.0-rc.1 "${sha}"
fresh; printf 'WINDOWS_SIGNING=off\n' > "${W}/deploy/release/windows-signing.env"; push_main "no exception line"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "signing file without the exception line" "WINDOWS_UNSIGNED_FINAL_TAG is missing" v0.1.0-rc.1 "${sha}"
fresh; printf 'WINDOWS_SIGNING=off\nWINDOWS_SIGNING=kms\nWINDOWS_UNSIGNED_FINAL_TAG=\n' > "${W}/deploy/release/windows-signing.env"; push_main "twice"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "signing file that sets the mode twice" "set twice" v0.1.0-rc.1 "${sha}"
fresh; printf 'WINDOWS_SIGNING=off\nWINDOWS_UNSIGNED_FINAL_TAG=\nEXTRA=1\n' > "${W}/deploy/release/windows-signing.env"; push_main "extra"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
grefuse "signing file with an unexpected line" "unexpected line" v0.1.0-rc.1 "${sha}"
fresh; printf '# comment\n\nWINDOWS_SIGNING=off\n# more\nWINDOWS_UNSIGNED_FINAL_TAG=\n' > "${W}/deploy/release/windows-signing.env"; push_main "comments"; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
gpass "signing file with comments and blank lines" v0.1.0-rc.1 "${sha}"

# final tags and the CHANGELOG
final v0.1.0 signpath ""
edit "${W}/CHANGELOG.md" '## [0.1.0] - 2026-10-20' '## [0.1.0] - 2026-10-20 '
git -C "${W}" commit -q -a -m "trailing space"; git -C "${W}" push -q origin main; git -C "${W}" tag -d v0.1.0 >/dev/null; git -C "${W}" push -q origin :refs/tags/v0.1.0; sha="$(head_sha)"; tag_annotated v0.1.0
gpass "final tag whose CHANGELOG heading has a trailing space" v0.1.0 "${sha}"
final v0.1.0 signpath ""
edit "${W}/CHANGELOG.md" '## [0.1.0] - 2026-10-20' '## 0.1.0 - 2026-10-20'
git -C "${W}" commit -q -a -m "heading without brackets"; git -C "${W}" push -q origin main; git -C "${W}" tag -d v0.1.0 >/dev/null; git -C "${W}" push -q origin :refs/tags/v0.1.0; sha="$(head_sha)"; tag_annotated v0.1.0
grefuse "final tag, CHANGELOG heading without brackets" "no heading '## \[0.1.0\] - YYYY-MM-DD'" v0.1.0 "${sha}"
final v0.1.0 signpath ""
edit "${W}/CHANGELOG.md" '## [0.1.0] - 2026-10-20' '## [0.1.0] - 2026-13-45'
git -C "${W}" commit -q -a -m "bad date"; git -C "${W}" push -q origin main; git -C "${W}" tag -d v0.1.0 >/dev/null; git -C "${W}" push -q origin :refs/tags/v0.1.0; sha="$(head_sha)"; tag_annotated v0.1.0
grefuse "final tag, CHANGELOG heading with an impossible date" "valid date" v0.1.0 "${sha}"
final v0.1.0 signpath ""
edit "${W}/CHANGELOG.md" '## [0.1.0] - 2026-10-20' '## [0.1.1] - 2026-10-20'
git -C "${W}" commit -q -a -m "other version"; git -C "${W}" push -q origin main; git -C "${W}" tag -d v0.1.0 >/dev/null; git -C "${W}" push -q origin :refs/tags/v0.1.0; sha="$(head_sha)"; tag_annotated v0.1.0
grefuse "final tag, CHANGELOG heading for another version" "no heading" v0.1.0 "${sha}"
final v0.1.0 signpath ""
printf '# Changelog\n\n## Unreleased\n\n- Entry.\n' > "${W}/CHANGELOG.md"
git -C "${W}" commit -q -a -m "no release heading"; git -C "${W}" push -q origin main; git -C "${W}" tag -d v0.1.0 >/dev/null; git -C "${W}" push -q origin :refs/tags/v0.1.0; sha="$(head_sha)"; tag_annotated v0.1.0
grefuse "final tag without any release heading" "no heading" v0.1.0 "${sha}"

# --- ci-ok ------------------------------------------------------------------------------------------------------------------------
fresh; sha="$(head_sha)"; tag_annotated v0.1.0-rc.1
printf '{"total_count":0,"check_runs":[]}\n' > "${STUB_CURL_BODY}"
grefuse "no ci-ok check run" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
printf '{"total_count":1,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}]}\n' > "${STUB_CURL_BODY}"
grefuse "only other check runs" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body failure;           grefuse "ci-ok failed" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body cancelled;         grefuse "ci-ok cancelled" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body skipped;           grefuse "ci-ok skipped" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body neutral;           grefuse "ci-ok neutral" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body null in_progress;  grefuse "ci-ok still running" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body success completed some-other-app; grefuse "ci-ok reported by another app" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
printf '{"total_count":2,"check_runs":[{"id":1,"name":"ci-ok","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}]}\n' > "${STUB_CURL_BODY}"
grefuse "incomplete answer (more runs than returned)" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
printf '{"total_count":2,"check_runs":[{"id":1,"name":"ci-ok","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}},{"id":2,"name":"ci-ok","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}]}\n' > "${STUB_CURL_BODY}"
gpass "a failed run followed by a successful re-run passes" v0.1.0-rc.1 "${sha}"
printf '{"total_count":2,"check_runs":[{"id":1,"name":"ci-ok","status":"completed","conclusion":"success","app":{"slug":"github-actions"}},{"id":2,"name":"ci-ok","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}}]}\n' > "${STUB_CURL_BODY}"
grefuse "a successful run followed by a failed re-run is refused" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
printf 'not json\n' > "${STUB_CURL_BODY}"
grefuse "an answer that is not JSON" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
printf '{"message":"Not Found"}\n' > "${STUB_CURL_BODY}"
grefuse "an answer without a check-run list" "no check run 'ci-ok'" v0.1.0-rc.1 "${sha}"
ci_body success
grefuse "HTTP 404 from the API" "HTTP 404" v0.1.0-rc.1 "${sha}" STUB_CURL_STATUS=404
grefuse "HTTP 403 from the API" "HTTP 403" v0.1.0-rc.1 "${sha}" STUB_CURL_STATUS=403
grefuse "HTTP 500 from the API" "HTTP 500" v0.1.0-rc.1 "${sha}" STUB_CURL_STATUS=500
grefuse "HTTP 204 is not a check-run list" "HTTP 204" v0.1.0-rc.1 "${sha}" STUB_CURL_STATUS=204
grefuse "curl fails (network error)" "call .* failed" v0.1.0-rc.1 "${sha}" STUB_CURL_EXIT=7
grefuse "a token with a quote is refused before any call" "unexpected format" v0.1.0-rc.1 "${sha}" 'GITHUB_TOKEN=a"b'
: > "${STUB_CURL_URLS}"
grefuse "the checks that need no network run before the API call (tag name)" "not a release tag" v0.1.1-rc.0 "${sha}"
[ ! -s "${STUB_CURL_URLS}" ] && ok "  no API call was made" || fail "  the API was called for a refused tag name"

finish_tests "versions-test"
