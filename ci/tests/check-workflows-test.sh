#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the workflow policy check ci/check-workflows.py: the fixture under ci/tests/testdata/workflows/good and every
# good-* tree pass, and every ci/tests/testdata/workflows/bad-* case breaks exactly the rule or rules named in its `expect`
# file (a case for rule R1 may break R12 as well when the action sits in the wrong job of the Scorecard workflow). The
# cases are small repository trees (.github/ and, where needed, ci/workflow-permissions.txt and ci/workflow-secrets.txt); the gate list
# that rule R8 compares with is passed through CHECK_WORKFLOWS_GATES. A case may also have a `findings` file with the exact
# output of the check, so that it cannot fail for another reason than its own. Two more cases: a link that leads nowhere is
# no workflow, and ci/lint-workflows.sh (run with stubs for actionlint and ShellCheck) hands actionlint the workflow files
# that the check reads. No network, no ports. The zizmor step of ci/lint-workflows.sh has its own test (lint-workflows-test.sh).
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
