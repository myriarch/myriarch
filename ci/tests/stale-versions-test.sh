#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intended (text for the fixtures)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of ci/check-stale-versions.py and its call in ci/check-versions.sh: for a final VERSION, "planned for vX.Y.Z" and
# "until vX.Y.Z" with a version up to VERSION, and a placeholder page with `planned` up to VERSION, are reported with the file
# and the line. Temporary git repositories; no network.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
have git || { echo "git is missing: test skipped"; exit 0; }
have python3 || { echo "python3 is missing: test skipped"; exit 0; }
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

CHECK="${CI_DIR}/check-stale-versions.py"
N=0

# repo <file> <text>...: a new git repository whose one tracked file holds the lines; sets R
repo() {
  local f="$1"; shift
  N=$((N + 1)); R="${TEST_TMP}/r${N}"
  mkdir -p "${R}/$(dirname "${f}")"
  printf '%s\n' "$@" > "${R}/${f}"
  git -C "${R}" init -q
  git -C "${R}" add -A
}
stale() { python3 -I "${CHECK}" --root "${R}" "$1"; }

echo "== phrases"
repo docs/a.md 'The tutorial is planned for v0.2.0.' 'More text.'
expect_fail "a sentence that ends with a full stop is found" 'docs/a\.md:1: says "planned for v0\.2\.0"' stale 0.2.0
expect_fail "  and the message says what to do" 'pull request that prepares the release' stale 0.2.0

repo docs/a.md 'Text.' 'The guide comes Until v0.2.0 the kind probe is the way.'
expect_fail "until is found, in any case, on its own line number" 'docs/a\.md:2: says "Until v0\.2\.0"' stale 0.2.0

repo docs/a.md 'The tutorial is planned' 'for v0.2.0 and not here yet.'
expect_fail "a line break in the middle of the phrase is found" 'docs/a\.md:1: says "planned for v0\.2\.0"' stale 0.2.0
repo docs/a.md 'The tutorial is planned' '   for   v0.2.0 and not here yet.'
expect_fail "  also with blanks around the break" 'docs/a\.md:1: says "planned for v0\.2\.0"' stale 0.2.0
repo docs/a.md '> It is planned' '> for v0.2.0.'
expect_fail "  also in a quote" 'docs/a\.md:1: says' stale 0.2.0
repo docs/a.yaml '# This is PLANNED' '# FOR v0.2.0 and later.'
expect_fail "  also in a comment, and with capitals" 'docs/a\.yaml:1: says' stale 0.2.0
repo docs/a.md 'Wait until' 'v0.1.0 is out.'
expect_fail "  also for until" 'docs/a\.md:1: says "until v0\.1\.0"' stale 0.2.0

repo docs/a.md 'It is planned for v0.3.0.' 'Wait until v0.10.0 is out.' 'Or until v1.0.0.'
expect_ok "a version above VERSION passes" stale 0.2.0
repo docs/a.md 'It is planned for v0.2.10.'
expect_ok "  v0.2.10 is above 0.2.1, not a hit for v0.2.1" stale 0.2.1
repo docs/a.md 'It is planned for v0.2.1.'
expect_fail "  but v0.2.1 is a hit for 0.2.10" 'docs/a\.md:1' stale 0.2.10

repo docs/a.md 'It is planned for v0.2.0.'
expect_fail "a version equal to VERSION is found" 'docs/a\.md:1' stale 0.2.0
repo docs/a.md 'It is planned for v0.1.0.'
expect_fail "a version below VERSION is found" 'docs/a\.md:1' stale 0.2.0
repo docs/a.md 'It is planned for v0.1.9.'
expect_fail "  also a patch version below" 'docs/a\.md:1' stale 0.2.0

repo docs/a.md 'It is planned for v0.2.0-rc.1.' 'Before v0.2.0 the chart had no schema.' 'Planned for 0.2.0 is not a promise of this form.'
expect_ok "a release candidate tag, \"before\" and a version without v are not matched" stale 0.2.0

repo docs/a.md 'It is planned for v0.2.0.'
expect_ok "a release candidate as VERSION passes: its own release is still ahead" stale 0.2.0-rc.1
expect_ok "  also for a later release" stale 0.1.0-rc.4
expect_fail "  but a promise of an older release is stale" 'docs/a\.md:1: says "planned for v0\.2\.0"' stale 0.3.0-rc.1
expect_fail "an invalid VERSION is refused" 'invalid version' stale 0.2
expect_fail "  also a leading v" 'invalid version' stale v0.2.0

echo "== wrapped versions and other spellings"
for t in 'planned for [v0.2.0](https://example.com/x)' 'until [v0.2.0](x)' 'planned for `v0.2.0`' 'Until `v0.2.0`' 'planned for **v0.2.0**' \
         'planned for *v0.2.0*' 'planned for _v0.2.0_' 'planned for <code>v0.2.0</code>' 'planned for "v0.2.0"' 'planned for (v0.2.0)' \
         'planned for the v0.2.0 release' 'planned for release v0.2.0' 'planned for the `v0.2.0` release' \
         'planned for [the v0.2.0 release](x)' 'planned for [release v0.2.0](x)' 'planned for <a href="x">v0.2.0</a>' \
         'planned for <code class="x">v0.2.0</code>' 'planned for <i>v0.2.0</i>' 'planned for version v0.2.0' \
         'until the release of v0.2.0' 'until version v0.2.0'; do
  repo docs/a.md "It is ${t}."
  expect_fail "wrapped: ${t}" 'docs/a\.md:1: says' stale 0.2.0
done
repo docs/a.md 'It is planned' '| for v0.2.0 in a table cell.'
expect_fail "a table border at the line break" 'docs/a\.md:1: says' stale 0.2.0
repo docs/a.md "It is planned for$(printf '\302\240')v0.2.0 with a non-breaking space."
expect_fail "a non-breaking space" 'docs/a\.md:1: says' stale 0.2.0
repo docs/a.md 'It is planned for [v0.3.0](x) and `v0.2.0-rc.1`.'
expect_ok "wrapped, but above VERSION or a release candidate tag" stale 0.2.0

repo website/content/docs/b.md '---' 'title: "B"' 'params:' '  placeholder: # a note' '    planned: "v0.1.0"' '---' ''
expect_fail "a comment after the placeholder key does not hide the page" 'b\.md:5: the placeholder page' stale 0.2.0

echo "== excluded paths"
for f in docs/adr/0001-x.md CHANGELOG.md changelog.d/a.md ci/tests/testdata/a.md; do
  repo "${f}" 'It was planned for v0.1.0 and until v0.1.0 it was not there.'
  expect_ok "${f} is not read" stale 0.2.0
done
repo docs/adr/README.md 'It is planned for v0.1.0.'
expect_ok "  and docs/adr/README.md is not read" stale 0.2.0
repo docs/a.txt 'It is planned for v0.1.0.'
expect_fail "other text suffixes are read (.txt)" 'docs/a\.txt:1' stale 0.2.0
for f in docs/a.go docs/a.sh docs/a.png; do
  repo "${f}" 'It is planned for v0.1.0.'
  expect_ok "${f} is not a text file of the list" stale 0.2.0
done
repo docs/a.md 'It is planned for v0.1.0.'
git -C "${R}" rm -q --cached docs/a.md
expect_ok "a file that git does not track is not read" stale 0.2.0
N=$((N + 1)); R="${TEST_TMP}/r${N}"; mkdir -p "${R}/docs"; printf 'It is planned for v0.1.0.\n' > "${R}/docs/a.md"
expect_fail "outside a git work tree every file is read" 'docs/a\.md:1' stale 0.2.0

echo "== placeholder pages"
PAGE() { printf '%s\n' '---' 'title: "Backup"' 'params:' '  placeholder:' '    tracking: "operations-manual"' "    planned: \"$1\"" '---' '' '{{< placeholder >}}'; }
N=$((N + 1)); R="${TEST_TMP}/r${N}"; mkdir -p "${R}/website/content/docs/operations"; PAGE v0.3.0 > "${R}/website/content/docs/operations/backup.md"
git -C "${R}" init -q; git -C "${R}" add -A
expect_ok "a placeholder planned for a later release passes" stale 0.2.0
expect_fail "a placeholder planned for VERSION is found" 'backup\.md:6: the placeholder page is planned for v0\.3\.0' stale 0.3.0
expect_fail "a placeholder planned for an older release is found" 'backup\.md:6' stale 0.4.0
expect_ok "  not for a release candidate" stale 0.3.0-rc.1
PAGE v0.3.0 > "${R}/website/content/docs/operations/other.md"
sed -i.bak 's/^  placeholder:$/  other:/' "${R}/website/content/docs/operations/other.md"; rm -f "${R}/website/content/docs/operations/other.md.bak"
git -C "${R}" add -A
expect_fail "only the page with the placeholder is named" 'backup\.md' stale 0.3.0
OUT="$(stale 0.3.0 2>&1 || true)"
printf '%s' "${OUT}" | grep -q 'other\.md' && fail "a planned field outside a placeholder is reported" || ok "a planned field outside a placeholder is not reported"
repo website/content/docs/b.md '---' 'title: "B"' '---' '' 'planned: "v0.1.0" in the text is no front matter.'
expect_ok "a line in the body is not the field" stale 0.2.0

echo "== ci/check-versions.sh"
# a tree with the scripts and the real copies, as in versions-test.sh
T="${TEST_TMP}/tree"
mkdir -p "${T}/ci" "${T}/docs"
cp "${ROOT}/ci/check-versions.sh" "${ROOT}/ci/version-copies.py" "${ROOT}/ci/check-stale-versions.py" "${T}/ci/"
while IFS= read -r f; do
  mkdir -p "${T}/$(dirname "${f}")"; cp "${ROOT}/${f}" "${T}/${f}"
done < <(python3 -I "${ROOT}/ci/version-copies.py" list | sort -u)
cv() { ( cd "${T}" && ci/check-versions.sh ); }
setv() { python3 -I "${T}/ci/version-copies.py" write "$1" >/dev/null; printf '%s\n' "$1" > "${T}/VERSION"; }
git -C "${T}" init -q
setv 0.2.0
printf 'The guide is planned for v0.2.0.\n' > "${T}/docs/a.md"
git -C "${T}" add -A
expect_fail "a final VERSION fails the version check on a stale promise" 'docs/a\.md:1: says "planned for v0\.2\.0"' cv
expect_fail "  and the check names the cause" 'promises a release that exists now' cv
setv 0.2.0-rc.1
expect_ok "  a release candidate passes" cv
setv 0.1.0
expect_ok "  a version below the promise passes" cv
printf 'The guide is planned for v0.3.0.\n' > "${T}/docs/a.md"
setv 0.3.0
expect_fail "  and fails when that release is the final VERSION" 'docs/a\.md:1' cv
printf 'The guide is released.\n' > "${T}/docs/a.md"
expect_ok "  and passes when the sentence is gone" cv

echo "== this repository"
expect_ok "no text of the repository promises the release of VERSION or an older one" bash -c "cd '${ROOT}' && ci/check-versions.sh"
finish_tests "stale-versions-test"
