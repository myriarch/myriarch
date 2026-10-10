#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Helper functions for the script tests under ci/tests (source with `. ci/tests/lib-test.sh`).
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"

PASS=0
FAIL=0
TEST_TMP="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-citest.XXXXXX")"
cleanup_tests() { rm -rf "${TEST_TMP}"; }
trap cleanup_tests EXIT

ok()   { PASS=$((PASS + 1)); printf '  ok    %s\n' "$*"; }
fail() { FAIL=$((FAIL + 1)); printf '  FAIL  %s\n' "$*" >&2; }

# expect_ok <name> <command...>: expects exit status 0
expect_ok() {
  local name="$1"; shift
  if OUT="$("$@" 2>&1)"; then ok "${name}"; else fail "${name} (non-zero exit)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
}

# expect_fail <name> <message-regex> <command...>: expects a non-zero exit status AND a matching message
expect_fail() {
  local name="$1" want="$2"; shift 2
  if OUT="$("$@" 2>&1)"; then
    fail "${name} (exit 0, expected a failure)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2
  elif printf '%s' "${OUT}" | grep -Eq -- "${want}"; then
    ok "${name}"
  else
    fail "${name} (message does not match /${want}/)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2
  fi
}

# Call the tests in a subshell so that exit/die does not end the test run
finish_tests() {
  printf '\n%s: %d passed, %d failed\n' "${1:-Tests}" "${PASS}" "${FAIL}"
  [ "${FAIL}" = 0 ]
}
