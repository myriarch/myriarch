#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the workflow policy check ci/check-workflows.py: the fixture under ci/tests/testdata/workflows/good passes, and
# every ci/tests/testdata/workflows/bad-* case breaks exactly the rule named in its `expect` file. The cases are small
# repository trees (.github/ and, where needed, ci/workflow-permissions.txt and ci/workflow-secrets.txt); the gate list
# that rule R8 compares with is passed through CHECK_WORKFLOWS_GATES. A case may also have a `findings` file with the exact
# output of the check, so that it cannot fail for another reason than its own. No network, no ports.
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
CHECK="${CI_DIR}/check-workflows.py"
DATA="${CI_DIR}/tests/testdata/workflows"
export CHECK_WORKFLOWS_GATES="go web e2e"

run_check() { python3 -I "${CHECK}" --root "$1"; }

expect_ok "the good fixture passes" run_check "${DATA}/good"

for d in "${DATA}"/bad-*; do
  name="${d##*/}"
  want="$(tr -d '[:space:]' < "${d}/expect")"
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

# usage errors and the real repository
expect_fail "an unknown option is a usage error" 'usage' python3 -I "${CHECK}" --bogus
empty="${TEST_TMP}/empty"; mkdir -p "${empty}"
expect_fail "a tree without workflows is a usage error" 'no workflow' python3 -I "${CHECK}" --root "${empty}"
unset CHECK_WORKFLOWS_GATES
expect_ok "the workflows of this repository pass" python3 -I "${CHECK}"

finish_tests "check-workflows-test"
