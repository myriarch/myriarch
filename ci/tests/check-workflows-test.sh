#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the workflow policy check ci/check-workflows.py: the fixture under ci/tests/testdata/workflows/good and every
# good-* tree pass, and every ci/tests/testdata/workflows/bad-* case breaks exactly the rule or rules named in its `expect`
# file (a case for rule R1 may break R12 as well when the action sits in the wrong job of the Scorecard workflow). The
# cases are small repository trees (.github/ and, where needed, ci/workflow-permissions.txt and ci/workflow-secrets.txt); the gate list
# that rule R8 compares with is passed through CHECK_WORKFLOWS_GATES. A case may also have a `findings` file with the exact
# output of the check, so that it cannot fail for another reason than its own. More cases: a link that leads nowhere is no
# workflow, files with characters on which the check and a YAML parser disagree and files that are not UTF-8 are findings of
# rule R14 (made with printf at test time, the repository holds none of these bytes), and ci/lint-workflows.sh (run with stubs
# for actionlint and ShellCheck) hands actionlint the workflow files that the check reads. No network, no ports. The zizmor step
# of ci/lint-workflows.sh has its own test (lint-workflows-test.sh).
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
CHECK="${CI_DIR}/check-workflows.py"
DATA="${CI_DIR}/tests/testdata/workflows"
export CHECK_WORKFLOWS_GATES="go web e2e"

run_check() { python3 -I "${CHECK}" --root "$1"; }

expect_ok "the good fixture passes" run_check "${DATA}/good"

# more trees that must pass: the shapes of rules R12 and R13 in other spellings, words in comments and prose
for d in "${DATA}"/good-*; do
  expect_ok "${d##*/} passes" run_check "${d}"
done

for d in "${DATA}"/bad-*; do
  name="${d##*/}"
  want="$(tr -s '[:space:]' '\n' < "${d}/expect" | sed '/^$/d' | sort -u | tr '\n' ' ')"
  want="${want% }"
  OUT="$(run_check "${d}" 2>&1)"; rc=$?
  rules="$(printf '%s\n' "${OUT}" | sed -n 's/^[^:]*:[0-9]*: \(R[0-9]*\):.*/\1/p' | sort -u | tr '\n' ' ')"
  if [ "${rc}" != 1 ] || [ "${rules}" != "${want} " ]; then
    fail "${name}: exit ${rc}, rules '${rules}', expected exit 1 with ${want}"
    printf '%s\n' "${OUT}" | sed 's/^/        /' >&2
  elif [ -f "${d}/findings" ] && [ "${OUT}" != "$(cat "${d}/findings")" ]; then
    fail "${name}: the findings differ from ${name}/findings (< actual, > expected)"
    diff <(printf '%s\n' "${OUT}") "${d}/findings" | sed 's/^/        /' >&2
  else
    ok "${name}: exit 1 with ${want} only"
  fi
done

# a link that leads nowhere (the lock file of an editor) is no workflow
cp -R "${DATA}/good" "${TEST_TMP}/dangling"
ln -s "nobody@host.123" "${TEST_TMP}/dangling/.github/workflows/.#ci.yml"
expect_ok "a link that leads nowhere is no workflow" run_check "${TEST_TMP}/dangling"

# Readers that disagree (rule R14). The check splits a file into lines at LF and CR; a YAML parser, the one of zizmor included, also
# takes U+0085, U+2028 and U+2029 for line breaks and rejects or skips control characters and a byte order mark. Each of these
# characters, and a file that is not UTF-8, is a finding in every file that zizmor reads: zizmor.yml, a workflow, a local action and
# the Dependabot configuration (.yml and .yaml). The files are made here with printf (%b escapes): the repository holds none of
# these bytes.
r14_tree() { # <name>: a copy of the fixture good-r14-config; prints its path
  rm -rf "${TEST_TMP:?}/r14-$1"; cp -R "${DATA}/good-r14-config" "${TEST_TMP}/r14-$1"; printf '%s' "${TEST_TMP}/r14-$1"
}
r14_put() { printf '%b' "$3" > "$1/$2"; }   # <tree> <file> <bytes>: replaces the file
r14_add() { printf '%b' "$3" >> "$1/$2"; }  # <tree> <file> <bytes>: appends to the file
# expect_r14 <name> <regex> <tree>: exit 1, no rule but R14, a finding that matches the regex and no traceback
expect_r14() {
  local out rc rules
  out="$(run_check "$3" 2>&1)"; rc=$?
  rules="$(printf '%s\n' "${out}" | sed -n 's/^[^:]*:[0-9]*: \(R[0-9]*\):.*/\1/p' | sort -u | tr '\n' ' ')"
  if [ "${rc}" = 1 ] && [ "${rules}" = "R14 " ] && printf '%s\n' "${out}" | grep -Eq -- "$2" && ! printf '%s\n' "${out}" | grep -q Traceback; then
    ok "$1"
  else
    fail "$1: exit ${rc}, rules '${rules}', expected exit 1 with R14 only and /$2/"; printf '%s\n' "${out}" | sed 's/^/        /' >&2
  fi
}
UTF8_NO="not valid UTF-8"
# the three line breaks in a line that looks like a comment, and the text behind it
t="$(r14_tree nel)";  r14_put "${t}" zizmor.yml 'rules:\n  # a reason\0302\0205  disable: true\n'
expect_r14 "U+0085 in zizmor.yml is a finding" '^zizmor\.yml:2: R14: the file contains U\+0085 \(NEXT LINE\)' "${t}"
t="$(r14_tree ls)";   r14_put "${t}" zizmor.yml 'rules:\n  # a reason\0342\0200\0250  disable: true\n'
expect_r14 "U+2028 in zizmor.yml is a finding" '^zizmor\.yml:2: R14: the file contains U\+2028 \(LINE SEPARATOR\)' "${t}"
t="$(r14_tree ps)";   r14_put "${t}" zizmor.yml 'rules:\n  # a reason\0342\0200\0251  disable: true\n'
expect_r14 "U+2029 in zizmor.yml is a finding" '^zizmor\.yml:2: R14: the file contains U\+2029 \(PARAGRAPH SEPARATOR\)' "${t}"
t="$(r14_tree wf-ls)"; r14_add "${t}" .github/workflows/e2e.yml '# a note\0342\0200\0250 and more\n'
expect_r14 "U+2028 in a workflow is a finding" '^\.github/workflows/e2e\.yml:[0-9]+: R14: the file contains U\+2028' "${t}"
t="$(r14_tree wf-nel)"; r14_add "${t}" .github/workflows/e2e.yml '# a note\0302\0205 and more\n'
expect_r14 "U+0085 in a workflow is a finding" '^\.github/workflows/e2e\.yml:[0-9]+: R14: the file contains U\+0085' "${t}"
t="$(r14_tree act-ps)"; r14_add "${t}" .github/actions/tool/action.yaml '# a note\0342\0200\0251 and more\n'
expect_r14 "U+2029 in a local action is a finding" '^\.github/actions/tool/action\.yaml:[0-9]+: R14: the file contains U\+2029' "${t}"
t="$(r14_tree dep-ls)"; r14_put "${t}" .github/dependabot.yml 'version: 2\n# a note\0342\0200\0250 and more\n'
expect_r14 "U+2028 in dependabot.yml is a finding" '^\.github/dependabot\.yml:2: R14: the file contains U\+2028' "${t}"
t="$(r14_tree dep-yaml-ls)"; r14_put "${t}" .github/dependabot.yaml 'version: 2\n# a note\0342\0200\0250 and more\n'
expect_r14 "U+2028 in dependabot.yaml is a finding" '^\.github/dependabot\.yaml:2: R14: the file contains U\+2028' "${t}"
# control characters (C0 but TAB, LF and CR; DEL; C1) and the byte order mark
t="$(r14_tree soh)";  r14_add "${t}" .github/workflows/e2e.yml '# a note\0001 and more\n'
expect_r14 "U+0001 in a workflow is a finding" '^\.github/workflows/e2e\.yml:[0-9]+: R14: the file contains U\+0001 \(control character\)' "${t}"
t="$(r14_tree nul)";  r14_add "${t}" .github/actions/tool/action.yaml '# a note\0000 and more\n'
expect_r14 "U+0000 in a local action is a finding" '^\.github/actions/tool/action\.yaml:[0-9]+: R14: the file contains U\+0000' "${t}"
t="$(r14_tree ff)";   r14_put "${t}" zizmor.yml 'rules:\n  # a reason\0014  disable: true\n'
expect_r14 "U+000C in zizmor.yml is a finding" '^zizmor\.yml:2: R14: the file contains U\+000C' "${t}"
t="$(r14_tree del)";  r14_put "${t}" .github/dependabot.yml 'version: 2\n# a note\0177 and more\n'
expect_r14 "U+007F in dependabot.yml is a finding" '^\.github/dependabot\.yml:2: R14: the file contains U\+007F' "${t}"
t="$(r14_tree c1)";   r14_add "${t}" .github/workflows/e2e.yml '# a note\0302\0200 and more\n'
expect_r14 "U+0080 in a workflow is a finding" '^\.github/workflows/e2e\.yml:[0-9]+: R14: the file contains U\+0080' "${t}"
t="$(r14_tree bom)";  r14_put "${t}" zizmor.yml '\0357\0273\0277rules: {}\n'
expect_r14 "a byte order mark in zizmor.yml is a finding" '^zizmor\.yml:1: R14: the file contains U\+FEFF \(BYTE ORDER MARK\)' "${t}"
t="$(r14_tree twice)"; r14_put "${t}" zizmor.yml 'rules:\n  # a\0342\0200\0250\n  # b\0342\0200\0250\n'
if [ "$(run_check "${t}" 2>&1 | grep -c 'U+2028')" = 1 ]; then ok "a character is reported once per file, at its first line"; else fail "a character is reported more than once per file"; fi
# a file that is not UTF-8 is a finding, not a traceback, in every kind of file (ci.yml is also read by the gate parity rule)
t="$(r14_tree utf8-zizmor)"; r14_put "${t}" zizmor.yml 'rules: {}\n# a \0377 byte\n'
expect_r14 "a zizmor.yml that is not UTF-8 is a finding" "^zizmor\\.yml:2: R14: the file is ${UTF8_NO}" "${t}"
t="$(r14_tree utf8-ci)"; r14_add "${t}" .github/workflows/ci.yml '# a \0377 byte\n'
expect_r14 "a ci.yml that is not UTF-8 is a finding" "^\\.github/workflows/ci\\.yml:[0-9]+: R14: the file is ${UTF8_NO}" "${t}"
t="$(r14_tree utf8-wf)"; r14_add "${t}" .github/workflows/e2e.yml '# a \0377 byte\n'
expect_r14 "a workflow that is not UTF-8 is a finding" "^\\.github/workflows/e2e\\.yml:[0-9]+: R14: the file is ${UTF8_NO}" "${t}"
t="$(r14_tree utf8-action)"; r14_add "${t}" .github/actions/tool/action.yaml '# a \0377 byte\n'
expect_r14 "a local action that is not UTF-8 is a finding" "^\\.github/actions/tool/action\\.yaml:[0-9]+: R14: the file is ${UTF8_NO}" "${t}"
t="$(r14_tree utf8-dep)"; r14_put "${t}" .github/dependabot.yml 'version: 2\n# a \0377 byte\n'
expect_r14 "a dependabot.yml that is not UTF-8 is a finding" "^\\.github/dependabot\\.yml:2: R14: the file is ${UTF8_NO}" "${t}"
t="$(r14_tree utf8-dep-yaml)"; r14_put "${t}" .github/dependabot.yaml 'version: 2\n# a \0377 byte\n'
expect_r14 "a dependabot.yaml that is not UTF-8 is a finding" "^\\.github/dependabot\\.yaml:2: R14: the file is ${UTF8_NO}" "${t}"
# what stays allowed: a TAB inside a line, CRLF and CR line ends, and non-ASCII text
t="$(r14_tree allowed)"; r14_put "${t}" zizmor.yml 'rules:\r\n  # a reason\0011with a tab, an accent \0303\0251 and a dash \0342\0200\0224\r\n  self-repository:\r\n    ignore:\r\n      - ci.yml\r\n'
expect_ok "a TAB, CRLF line ends and non-ASCII text in zizmor.yml are fine" run_check "${t}"
t="$(r14_tree cr)"; r14_put "${t}" zizmor.yml 'rules:\r  # a reason\r  self-repository:\r    ignore:\r      - ci.yml\r'
expect_ok "CR line ends in zizmor.yml are fine" run_check "${t}"

# ci/lint-workflows.sh hands actionlint the files that the policy check reads: both extensions, in any letter case, and names
# with a leading dot, but neither a backup copy of a workflow nor a link that leads nowhere
lint="${TEST_TMP}/lint"
mkdir -p "${lint}/ci" "${lint}/.github/workflows" "${TEST_TMP}/stubs"
cp "${CI_DIR}/lib.sh" "${CI_DIR}/lint-workflows.sh" "${CI_DIR}/check-workflows.py" "${lint}/ci/"
for f in ci.yml e2e.yaml Release.YML .hidden.yaml ci.yml.bak; do cp "${DATA}/good/.github/workflows/e2e.yml" "${lint}/.github/workflows/${f}"; done
ln -s "nobody@host.123" "${lint}/.github/workflows/.#ci.yml"
printf 'rules: {}\n' > "${lint}/zizmor.yml"
cat > "${TEST_TMP}/stubs/actionlint" <<'EOF'
#!/bin/sh
if [ "$1" = -version ]; then echo "actionlint stub"; exit 0; fi
for a in "$@"; do case "$a" in .github/*) echo "$a" ;; esac; done > "${ACTIONLINT_ARGS}"
EOF
printf '#!/bin/sh\nexit 0\n' > "${TEST_TMP}/stubs/shellcheck"
# zizmor stub: records the .github files it is given
cat > "${TEST_TMP}/stubs/zizmor" <<'ZEOF'
#!/bin/sh
if [ "$1" = --version ]; then echo "zizmor stub"; exit 0; fi
for a in "$@"; do case "$a" in .github/*) echo "$a" ;; esac; done > "${ZIZMOR_ARGS}"
ZEOF
chmod +x "${TEST_TMP}/stubs/actionlint" "${TEST_TMP}/stubs/shellcheck" "${TEST_TMP}/stubs/zizmor"
# TOOLS_BIN points at the stubs: ci/lib.sh exports the real tool directory, and lint-workflows.sh puts TOOLS_BIN first on the PATH
ACTIONLINT_ARGS="${TEST_TMP}/actionlint.args" ZIZMOR_ARGS="${TEST_TMP}/zizmor.args" TOOLS_BIN="${TEST_TMP}/stubs" PATH="${TEST_TMP}/stubs:${PATH}" \
  bash "${lint}/ci/lint-workflows.sh" > /dev/null 2>&1
want_files=".github/workflows/.hidden.yaml .github/workflows/Release.YML .github/workflows/ci.yml .github/workflows/e2e.yaml"
got_files="$(LC_ALL=C sort "${TEST_TMP}/actionlint.args" 2> /dev/null | tr '\n' ' ')"
if [ "${got_files}" = "${want_files} " ]; then
  ok "lint-workflows.sh hands actionlint both extensions, any letter case and hidden names"
else
  fail "lint-workflows.sh hands actionlint '${got_files}', expected '${want_files} '"
fi

got_zizmor="$(LC_ALL=C sort "${TEST_TMP}/zizmor.args" 2> /dev/null | tr '\n' ' ')"
if [ "${got_zizmor}" = "${want_files} " ]; then
  ok "lint-workflows.sh hands zizmor the same workflow files"
else
  fail "lint-workflows.sh hands zizmor '${got_zizmor}', expected '${want_files} '"
fi

# usage errors and the real repository
expect_fail "an unknown option is a usage error" 'usage' python3 -I "${CHECK}" --bogus
empty="${TEST_TMP}/empty"; mkdir -p "${empty}"
expect_fail "a tree without workflows is a usage error" 'no workflow' python3 -I "${CHECK}" --root "${empty}"
unset CHECK_WORKFLOWS_GATES
expect_ok "the workflows of this repository pass" python3 -I "${CHECK}"

finish_tests "check-workflows-test"
