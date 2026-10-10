#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Regression test for scripts/check-language.sh: German umlauts and stop words are reported, English code
# words (die, also, was, ...) are not, excluded paths, binaries, URLs and suppressed lines are skipped.
# Works on a throw-away git repository in ${TEST_TMP}; the real default excludes are used.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u

GUARD="${ROOT}/scripts/check-language.sh"
EXCLUDES="${ROOT}/scripts/check-language.exclude"
repo="${TEST_TMP}/repo"
mkdir -p "${repo}"
git -C "${repo}" init -q 2>/dev/null

# UTF-8 bytes written via printf escapes, so that this file stays ASCII
AE="$(printf '\xc3\xa4')"
UE="$(printf '\xc3\xbc')"

put() { # path line...; one line per argument
  local p="${repo}/$1"; shift
  mkdir -p "$(dirname "${p}")"
  printf '%s\n' "$@" > "${p}"
}

put a.go    'package a' "// Gr${UE}n ist die Farbe"
put b.sh    '#!/bin/sh' 'die "file not found"' '[ -f x ] || die "missing"'
put b2.sh   '#!/bin/sh' 'die "Datei fehlt"'
put c.ts    '// Konfiguration ist nicht gesetzt'
put d.md    'see the der encoding' 'under wider ein-fach'
put e.txt   'die also was will an in so man hat am um im es er Standard'
put f.txt   "Gr${UE}n und ist nicht check-language: ignore" 'plain english text'
put g.md    'see https://example.com/und/oder/nicht/mit for details'
put h.txt   "Die Datei ist nicht gefunden: F${AE}hler"
put src/m.go "// F${UE}r die Welt"
put src/deep/er/n.go "// Gr${UE}n"
put i.txt   'Fuehrt das Skript von Hand aus'
put j.txt   'Der Test der Runde'
put k.txt   'Alle Dateien unter Linux'
put web/src/i18n/de.json "{\"a\": \"Gr${UE}n und ist nicht\"}"
put pkg/testdata/x.txt "Gr${UE}n und ist nicht"
put web/src/mocks/db.ts "// Gr${UE}n und ist nicht"
put x.golden "Gr${UE}n und ist nicht"
printf 'abc\0\xc3\xa4\xc3\xa4\n' > "${repo}/bin.dat"
printf '\xff\xfe und ist nicht\n' > "${repo}/invalid.txt"
git -C "${repo}" add -A
put u.txt 'Das ist nicht gut'   # untracked

guard() { "${GUARD}" --exclude-file "${EXCLUDES}" -C "${repo}" "$@"; }
findings() { grep -E '^[^ ].*:[0-9]+: (umlaut|stopwords)' || true; }

expected="$(cat <<'WANT'
a.go:2: umlaut
b2.sh:2: stopwords (Datei, fehlt)
c.ts:1: stopwords (ist, nicht)
h.txt:1: umlaut
i.txt:1: stopwords (das, von, aus)
k.txt:1: stopwords (Alle, unter)
src/deep/er/n.go:1: umlaut
src/m.go:1: umlaut
WANT
)"
want_summary='check-language: 8 finding(s) in 8 file(s) [umlaut: 4, stopwords: 4]; scanned 14 file(s), skipped 2'

# default run: exit 1 and exactly the expected findings
rc=0; OUT="$(guard)" || rc=$?
[ "${rc}" = 1 ] && ok "default run exits 1" || fail "default run exit ${rc}, expected 1"
got="$(printf '%s\n' "${OUT}" | findings)"
[ "${got}" = "${expected}" ] && ok "findings match the expectation" || { fail "findings differ"; diff <(printf '%s\n' "${expected}") <(printf '%s\n' "${got}") | sed 's/^/        /' >&2; }
[ "$(printf '%s\n' "${OUT}" | tail -n 1)" = "${want_summary}" ] && ok "summary line counts" || { fail "summary line"; printf '%s\n' "${OUT}" | tail -n 1 | sed 's/^/        /' >&2; }
# the report never contains line text
! printf '%s\n' "${OUT}" | grep -Eq 'Farbe|Konfiguration|gesetzt|Welt' && ok "line text is not printed" || fail "line text leaked into the report"

# individual categories
printf '%s\n' "${got}" | grep -q '^b\.sh:' && fail "die in b.sh counted" || ok "shell function die is not a stop word"
printf '%s\n' "${got}" | grep -q '^d\.md:' && fail "d.md reported" || ok "single stop word and substrings do not count"
printf '%s\n' "${got}" | grep -q '^e\.txt:' && fail "e.txt reported" || ok "English words (also, was, will, ...) do not count"
printf '%s\n' "${got}" | grep -q '^f\.txt:' && fail "suppressed line reported" || ok "check-language: ignore suppresses a line"
printf '%s\n' "${got}" | grep -q '^g\.md:' && fail "URL reported" || ok "stop words inside URLs are ignored"
printf '%s\n' "${got}" | grep -q '^j\.txt:' && fail "j.txt reported" || ok "stop words are counted distinct ignoring case"
printf '%s\n' "${got}" | grep -q '^i\.txt:1: stopwords (das, von, aus)$' && ok "von/aus are stop words" || fail "i.txt not reported"
printf '%s\n' "${got}" | grep -q '^h\.txt:1: umlaut$' && ok "umlaut wins over stopwords" || fail "h.txt should be umlaut only"

# --warn
rc=0; OUT="$(guard --warn)" || rc=$?
[ "${rc}" = 0 ] && ok "--warn exits 0" || fail "--warn exit ${rc}"
[ "$(printf '%s\n' "${OUT}" | findings)" = "${expected}" ] && ok "--warn reports the same findings" || fail "--warn findings differ"

# --min-words
OUT="$(guard --min-words 1 || true)"
printf '%s\n' "${OUT}" | grep -q '^d\.md:1: stopwords (der)$' && ok "--min-words 1 hits a single stop word" || fail "--min-words 1 does not report d.md:1"
expect_fail "--min-words 0 is a usage error" "positive integer" guard --min-words 0

# pathspecs and excludes
expect_ok "an excluded directory alone is clean" guard web/src/mocks
rc=0; OUT="$(guard --exclude-file /dev/null web/src/mocks)" || rc=$?
[ "${rc}" = 1 ] && ok "an excluded directory without default excludes is reported" || fail "excluded directory without excludes exit ${rc}"
rc=0; OUT="$(guard src)" || rc=$?
[ "${rc}" = 1 ] && printf '%s\n' "${OUT}" | grep -q '^src/m\.go:1: umlaut$' && ok "pathspec src selects only src/" || fail "pathspec src"
# git 2.52 lists no files for some nested pathspecs when an exclude pathspec follows them, so excludes go first
rc=0; OUT="$(guard src/deep/er)" || rc=$?
[ "${rc}" = 1 ] && printf '%s\n' "${OUT}" | grep -q '^src/deep/er/n\.go:1: umlaut$' && ok "nested pathspec with default excludes" || fail "nested pathspec src/deep/er (exit ${rc})"
OUT="$(guard --exclude 'src/**' || true)"
printf '%s\n' "${OUT}" | grep -q '^src/' && fail "--exclude ignored" || ok "--exclude adds a glob"

# --untracked
OUT="$(guard || true)"
printf '%s\n' "${OUT}" | grep -q '^u\.txt:' && fail "untracked file reported by default" || ok "untracked files are ignored by default"
OUT="$(guard --untracked || true)"
printf '%s\n' "${OUT}" | grep -q '^u\.txt:1: stopwords (Das, ist, nicht)$' && ok "--untracked reports untracked files" || fail "--untracked misses u.txt"

# --summary-only
OUT="$(guard --summary-only || true)"
want_dirs="$(printf '  ./: 6\n  src/: 2')"
[ "$(printf '%s\n' "${OUT}" | head -n 2)" = "${want_dirs}" ] && ok "--summary-only per-directory counts" || { fail "--summary-only directories"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; }
printf '%s\n' "${OUT}" | grep -q ':[0-9]*: ' && fail "--summary-only prints findings" || ok "--summary-only prints no single findings"

# usage and environment errors
expect_fail "unknown option exits 2 with usage" "Usage:" guard --bogus
rc=0; guard --bogus >/dev/null 2>&1 || rc=$?
[ "${rc}" = 2 ] && ok "unknown option exit code 2" || fail "unknown option exit ${rc}"
mkdir -p "${TEST_TMP}/nogit"
rc=0; "${GUARD}" -C "${TEST_TMP}/nogit" >/dev/null 2>&1 || rc=$?
[ "${rc}" = 2 ] && ok "-C on a non-git directory exits 2" || fail "-C non-git exit ${rc}"
rc=0; "${GUARD}" --exclude-file "${TEST_TMP}/missing" -C "${repo}" >/dev/null 2>&1 || rc=$?
[ "${rc}" = 2 ] && ok "unreadable exclude file exits 2" || fail "missing exclude file exit ${rc}"
rc=0; OUT="$("${GUARD}" --help)" || rc=$?
[ "${rc}" = 0 ] && printf '%s\n' "${OUT}" | grep -q '^Usage:' && ok "--help exits 0" || fail "--help"

# smoke test on the real repository (report only)
expect_ok "smoke: real repository in warn mode" "${GUARD}" --warn --summary-only
finish_tests "check-language-test"
