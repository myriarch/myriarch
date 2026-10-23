#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Regression test for scripts/check-docs.py (links, front-matter): good and broken files, directories, anchors, duplicate
# heading slugs, code fences and inline code, reference links, HTML attributes, ignored targets, --assume-exists, front
# matter and the exemption of the decision records. Works on a throw-away git repository in ${TEST_TMP}.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u

CHECK="${ROOT}/scripts/check-docs.py"
have python3 || { echo "python3 not found" >&2; exit 1; }
repo="${TEST_TMP}/repo"
mkdir -p "${repo}"
git -C "${repo}" init -q 2>/dev/null

put() { # path line...; one line per argument
  local p="${repo}/$1"; shift
  mkdir -p "$(dirname "${p}")"
  printf '%s\n' "$@" > "${p}"
}

# valid front matter (5 lines) with the given title
FM() { printf '%s\n' '---' "title: \"$1\"" 'weight: 10' 'description: "A page."' '---'; }

put README.md '# Top' \
  '[good file](docs/guide.md)' \
  '[good dir](docs/)' \
  '[good anchor](docs/guide.md#first-steps)' \
  '[good duplicate](docs/guide.md#setup-1)' \
  '[good base](docs/guide.md#setup)' \
  '[explicit id](docs/guide.md#custom-id)' \
  '[explicit name](docs/guide.md#named-one)' \
  '[markup heading](docs/guide.md#some-code-and-more)' \
  '[missing](docs/nope.md)' \
  '[bad anchor](docs/guide.md#nope)' \
  '[absolute](/docs/guide.md)' \
  '[escape](../outside.md)' \
  '[external](https://example.com/x) [mail](mailto:team@example.com) [phone](tel:+10000000)' \
  '[wrong case](docs/Guide.md)' \
  '[spaces](<docs/with space.md>)' \
  '[encoded](docs/with%20space.md)' \
  '[self](#top)' \
  '[self bad](#nothing)' \
  '[title ok](docs/guide.md "a title")' \
  '[parens](docs/a(b).md)' \
  '[file as dir](docs/guide.md/)'
put docs/guide.md "$(FM Guide)" '## First steps' 'text' '## Setup' '## Setup' '## Some `code` and **more**' \
  '<a id="custom-id"></a>' '<a name="named-one"></a>'
put 'docs/with space.md' 'plain'
put 'docs/a(b).md' 'plain'

# fences, inline code, comments, reference definitions, HTML attributes (front matter takes lines 1-5)
put docs/code.md "$(FM Code)" \
  '```' '[x](missing-in-fence.md)' '```' \
  '~~~sh' '[x](missing-in-tilde.md)' '~~~' \
  '`[y](missing-in-code.md)` and ``[y](missing-in-double.md)``' \
  '<!-- [z](missing-in-comment.md) -->' \
  '[real](missing-real.md)' \
  '[ref]: missing-ref.md' \
  '[okref]: guide.md#setup' \
  '<img src="missing.png" alt="x">' \
  '<img src="guide.md" alt="x">' \
  '<a href="https://example.com/y">y</a>' \
  '<img srcset="guide.md 1x, missing-2x.png 2x" src="guide.md">' \
  '[multi][okref]'

# ignored targets
put .gitignore 'ignored.txt' '/gen/'
put ignored.txt 'x'
put gen/out.txt 'x'
put docs/ign.md '[ignored file](../ignored.txt)' '[ignored dir file](../gen/out.txt)' '[tracked](../README.md)'

# --assume-exists
put docs/assume.md '[future](future.md)' '[future anchor](future.md#a)' '[later dir](later/)' '[later file](later/_index.md)' '[missing](nowhere.md)'
put assume.list '# paths another package still writes' 'docs/future.md' '' 'docs/later/_index.md'

# testdata and node_modules are out of the default scope
put internal/testdata/bad.md '[x](nowhere.md)'
put node_modules/pkg/README.md '[x](nowhere.md)'

# front matter
put docs/fm/good.md "$(FM Good)" '## Section' '```' '# a comment in code' '```'
put docs/fm/_index.md "$(FM Index)" 'text'
put docs/fm/none.md '## No front matter'
put docs/fm/unclosed.md '---' 'title: "T"' 'weight: 1' 'description: "D."' '## x'
put docs/fm/order.md '---' 'title: "T"' 'description: "D."' 'weight: 1' '---' 'text'
put docs/fm/h1.md "$(FM H1)" '# Repeated title'
put docs/fm/weight.md '---' 'title: "T"' 'weight: ten' 'description: "D."' '---'
put docs/fm/empty.md '---' 'title: ""' 'weight: 1' 'description: "D."' '---'
put docs/fm/bare.md '---' 'title: Bare' 'weight: 1' 'description: "D."' '---'
put docs/fm/missing.md '---' 'title: "T"' 'weight: 1' '---'
put docs/fm/extra.md '---' 'title: "T"' 'weight: 1' 'description: "D."' 'draft: true' '---'
put docs/adr/0001-record.md '# 0001. Record' '- Status: accepted'
put docs/adr/0000-template.md '# 0000. Template'
put docs/adr/README.md "$(FM ADRs)" 'index'
# website sources: site-absolute links are fine for the site (Hugo and lychee check them), the front matter rules differ
put website/content/links.md '---' 'title: "Links"' 'description: "A page."' '---' '[site absolute](/docs/guide/)' '[missing](nowhere.md)'
put website/content/site/good.md '---' 'title: "Good"' 'description: "No weight."' '---' '## Section'
put website/content/site/weighted.md '---' 'title: "Weighted"' 'weight: 5' 'description: "With weight."' 'linkTitle: "W"' 'params:' '  placeholder:' '    tracking: "example-topic"' '    replaces: "docs/x.md"' 'cascade:' '  - type: docs' '---' '## Section' '```' '# a comment in code' '```'
put website/content/site/noweight-order.md '---' 'description: "D."' 'title: "T"' '---'
put website/content/site/badorder.md '---' 'title: "T"' 'description: "D."' 'weight: 5' '---'
put website/content/site/draft.md '---' 'title: "T"' 'description: "D."' 'draft: true' '---'
put website/content/site/nodesc.md '---' 'title: "T"' 'weight: 1' '---'
put website/content/site/bare.md '---' 'title: T' 'description: "D."' '---'
put website/content/site/h1.md '---' 'title: "T"' 'description: "D."' '---' '# T'
put website/content/site/none.md '## No front matter'
put changelog.d/entry.md '[root relative](README.md)' '[missing](nowhere.md)'
git -C "${repo}" add -A

chk() { (cd "${repo}" && python3 -I "${CHECK}" "$@"); }
# "path:line" of every finding
where() { sed -E 's/^([^:]+:[0-9]+): .*/\1/'; }

# ---- links
rc=0; OUT="$(chk links)" || rc=$?
[ "${rc}" = 1 ] && ok "links: findings give exit 1" || fail "links: exit ${rc}, expected 1"
got="$(printf '%s\n' "${OUT}" | where)"
expected="$(cat <<'WANT'
README.md:10
README.md:11
README.md:12
README.md:13
README.md:15
README.md:19
README.md:22
changelog.d/entry.md:2
docs/assume.md:1
docs/assume.md:2
docs/assume.md:3
docs/assume.md:4
docs/assume.md:5
docs/code.md:14
docs/code.md:15
docs/code.md:17
docs/code.md:20
docs/ign.md:1
docs/ign.md:2
WANT
)"
[ "${got}" = "${expected}" ] && ok "links: exactly the broken links are reported" || { fail "links: findings differ"; diff <(printf '%s\n' "${expected}") <(printf '%s\n' "${got}") | sed 's/^/        /' >&2; }
msg() { printf '%s\n' "${OUT}" | grep -E "^$1: " || true; }
msg 'README.md:10' | grep -q 'broken link' && ok "links: missing file" || fail "links: missing file message"
msg 'README.md:11' | grep -q 'broken anchor' && ok "links: missing anchor" || fail "links: missing anchor message"
msg 'README.md:12' | grep -q 'absolute link' && ok "links: absolute link" || fail "links: absolute link message"
msg 'README.md:13' | grep -q 'leaves the repository' && ok "links: link outside the repository" || fail "links: outside message"
msg 'README.md:15' | grep -q 'broken link' && ok "links: wrong case is a broken link" || fail "links: wrong case"
msg 'README.md:19' | grep -q 'broken anchor' && ok "links: anchor in the same file" || fail "links: same-file anchor"
msg 'README.md:22' | grep -q 'not a directory' && ok "links: trailing slash on a file" || fail "links: trailing slash"
msg 'docs/code.md:15' | grep -q 'broken link' && ok "links: reference definition" || fail "links: reference definition"
msg 'docs/code.md:20' | grep -q 'broken link' && ok "links: HTML srcset" || fail "links: srcset"
msg 'docs/ign.md:1' | grep -q 'ignores' && ok "links: git-ignored file" || fail "links: ignored file"
msg 'docs/ign.md:2' | grep -q 'ignores' && ok "links: file in a git-ignored directory" || fail "links: ignored directory"
printf '%s\n' "${OUT}" | grep -q 'testdata\|node_modules' && fail "links: testdata/node_modules scanned" || ok "links: testdata and node_modules are out of scope"
printf '%s\n' "${OUT}" | grep -q 'missing-in-' && fail "links: code, fence or comment content was checked" || ok "links: fences, inline code and comments are ignored"
[ "$(printf '%s\n' "${OUT}" | grep -vcE '^[^ ]+:[0-9]+: ')" = 0 ] && ok "links: output is path:line: message only" || fail "links: extra output lines"

# the website sources: skipped by default, checked when named
printf '%s\n' "${OUT}" | grep -q '^website/' && fail "links: website/ is in the default scope" || ok "links: the website sources are out of the default scope"
rc=0; OUT2="$(chk links website/content/links.md)" || rc=$?
[ "${rc}" = 1 ] && printf '%s\n' "${OUT2}" | grep -q 'absolute link' && printf '%s\n' "${OUT2}" | grep -q 'broken link' && ok "links: a named path below website/ is checked" || fail "links: website path (exit ${rc})"

# paths narrow the scope
rc=0; OUT="$(chk links docs/guide.md)" || rc=$?
[ "${rc}" = 0 ] && [ -z "${OUT}" ] && ok "links: a clean file passes" || fail "links: docs/guide.md exit ${rc}"
rc=0; OUT="$(chk links docs/code.md)" || rc=$?
[ "${rc}" = 1 ] && [ "$(printf '%s\n' "${OUT}" | grep -c '^docs/code.md:')" = 4 ] && ok "links: a file path narrows the scope" || fail "links: narrowing (exit ${rc})"
rc=0; OUT="$(chk links docs/fm docs/adr)" || rc=$?
[ "${rc}" = 0 ] && ok "links: directories narrow the scope" || fail "links: directory scope exit ${rc}"

# --assume-exists
rc=0; OUT="$(chk links --assume-exists "${repo}/assume.list" docs/assume.md)" || rc=$?
got="$(printf '%s\n' "${OUT}" | where | tr '\n' ' ')"
[ "${rc}" = 1 ] && [ "${got}" = "docs/assume.md:5 " ] && ok "links: --assume-exists accepts files, anchors and directories" || fail "links: --assume-exists (exit ${rc}, got: ${got})"

# ---- front matter
rc=0; OUT="$(chk front-matter docs/fm docs/adr)" || rc=$?
[ "${rc}" = 1 ] && ok "front-matter: findings give exit 1" || fail "front-matter: exit ${rc}, expected 1"
got="$(printf '%s\n' "${OUT}" | sed -E 's/^([^:]+):.*/\1/' | sort -u | tr '\n' ' ')"
want='docs/fm/bare.md docs/fm/empty.md docs/fm/extra.md docs/fm/h1.md docs/fm/missing.md docs/fm/none.md docs/fm/order.md docs/fm/unclosed.md docs/fm/weight.md '
[ "${got}" = "${want}" ] && ok "front-matter: exactly the broken pages are reported" || fail "front-matter: files differ: ${got}"
msg 'docs/fm/none.md:1' | grep -q 'missing front matter' && ok "front-matter: missing block" || fail "front-matter: missing block"
msg 'docs/fm/unclosed.md:1' | grep -q 'not closed' && ok "front-matter: unclosed block" || fail "front-matter: unclosed block"
msg 'docs/fm/order.md:2' | grep -q 'order' && ok "front-matter: wrong key order" || fail "front-matter: key order"
msg 'docs/fm/h1.md:6' | grep -q 'heading' && ok "front-matter: repeated H1" || fail "front-matter: H1"
msg 'docs/fm/weight.md:3' | grep -q 'weight must be an integer' && ok "front-matter: weight not an integer" || fail "front-matter: weight"
msg 'docs/fm/empty.md:2' | grep -q 'title must not be empty' && ok "front-matter: empty title" || fail "front-matter: empty title"
msg 'docs/fm/bare.md:2' | grep -q 'double-quoted' && ok "front-matter: unquoted title" || fail "front-matter: bare title"
msg 'docs/fm/missing.md:1' | grep -q 'description is missing' && ok "front-matter: missing key" || fail "front-matter: missing key"
msg 'docs/fm/extra.md:5' | grep -q 'unexpected' && ok "front-matter: unexpected key" || fail "front-matter: extra key"
printf '%s\n' "${OUT}" | grep -q '^docs/adr/' && fail "front-matter: a file in docs/adr reported" || ok "front-matter: ADR records and the template are exempt, docs/adr/README.md passes"
printf '%s\n' "${OUT}" | grep -q '^docs/fm/good.md\|^docs/fm/_index.md' && fail "front-matter: good page reported" || ok "front-matter: a # line inside a code fence is no H1"
rc=0; OUT="$(chk front-matter docs/fm/good.md docs/adr)" || rc=$?
[ "${rc}" = 0 ] && [ -z "${OUT}" ] && ok "front-matter: paths narrow the scope" || fail "front-matter: narrowing exit ${rc}"
rc=0; OUT="$(chk front-matter)" || rc=$?
printf '%s\n' "${OUT}" | grep -q '^docs/ign.md:1: missing front matter' && [ "${rc}" = 1 ] && ok "front-matter: the default scope is docs/" || fail "front-matter: default scope (exit ${rc})"

# ---- front matter of the website sources (--site)
rc=0; OUT="$(chk front-matter --site)" || rc=$?
[ "${rc}" = 1 ] && ok "front-matter --site: findings give exit 1" || fail "front-matter --site: exit ${rc}, expected 1"
got="$(printf '%s\n' "${OUT}" | sed -E 's/^([^:]+):.*/\1/' | sort -u | tr '\n' ' ')"
want='website/content/site/badorder.md website/content/site/bare.md website/content/site/draft.md website/content/site/h1.md website/content/site/none.md website/content/site/nodesc.md website/content/site/noweight-order.md '
want="$(printf '%s\n' ${want} | sort | tr '\n' ' ')"
[ "${got}" = "${want}" ] && ok "front-matter --site: the default path is website/content and exactly the broken pages are reported" || fail "front-matter --site: files differ: ${got}"
msg 'website/content/site/nodesc.md:1' | grep -q 'description is missing' && ok "front-matter --site: description is required" || fail "front-matter --site: missing description"
msg 'website/content/site/draft.md:4' | grep -q 'unexpected front matter key draft' && ok "front-matter --site: keys outside the list are refused" || fail "front-matter --site: extra key"
msg 'website/content/site/bare.md:2' | grep -q 'double-quoted' && ok "front-matter --site: title must be double-quoted" || fail "front-matter --site: bare title"
msg 'website/content/site/h1.md:5' | grep -q 'heading' && ok "front-matter --site: repeated H1" || fail "front-matter --site: H1"
msg 'website/content/site/none.md:1' | grep -q 'missing front matter' && ok "front-matter --site: missing block" || fail "front-matter --site: missing block"
msg 'website/content/site/noweight-order.md:2' | grep -q 'order' && ok "front-matter --site: description before title is refused" || fail "front-matter --site: key order"
msg 'website/content/site/badorder.md:2' | grep -q 'order' && ok "front-matter --site: weight after description is refused" || fail "front-matter --site: weight order"
printf '%s\n' "${OUT}" | grep -q 'good.md\|weighted.md\|links.md' && fail "front-matter --site: a good page was reported" || ok "front-matter --site: optional weight, allowed keys, structured values and fenced # lines pass"
printf '%s\n' "${OUT}" | grep -q '^docs/' && fail "front-matter --site: docs/ was checked" || ok "front-matter --site: docs/ is out of scope"
rc=0; OUT="$(chk front-matter --site website/content/site/good.md website/content/site/weighted.md)" || rc=$?
[ "${rc}" = 0 ] && [ -z "${OUT}" ] && ok "front-matter --site: paths narrow the scope" || fail "front-matter --site: narrowing exit ${rc}"

# ---- usage and environment errors
rc=0; chk bogus >/dev/null 2>&1 || rc=$?
[ "${rc}" = 2 ] && ok "unknown subcommand exits 2" || fail "unknown subcommand exit ${rc}"
rc=0; chk links no/such/path >/dev/null 2>&1 || rc=$?
[ "${rc}" = 2 ] && ok "links: missing path exits 2" || fail "missing path exit ${rc}"
rc=0; chk links --assume-exists "${TEST_TMP}/missing.list" >/dev/null 2>&1 || rc=$?
[ "${rc}" = 2 ] && ok "links: unreadable --assume-exists file exits 2" || fail "unreadable list exit ${rc}"
mkdir -p "${TEST_TMP}/nogit"
rc=0; (cd "${TEST_TMP}/nogit" && python3 -I "${CHECK}" links >/dev/null 2>&1) || rc=$?
[ "${rc}" = 2 ] && ok "links outside a git work tree exits 2" || fail "non-git exit ${rc}"
rc=0; (cd "${TEST_TMP}/nogit" && python3 -I "${CHECK}" links --root "${repo}" docs/guide.md >/dev/null 2>&1) || rc=$?
[ "${rc}" = 0 ] && ok "--root selects the repository" || fail "--root exit ${rc}"

# ---- the real repository (report only: the exit status must be 0 or 1)
expect_ok "smoke: the checker runs on the real repository" bash -c "cd '${ROOT}' && python3 -I scripts/check-docs.py links >/dev/null; [ \$? -le 1 ]"
finish_tests "check-docs-test"
