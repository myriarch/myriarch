#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the vulnerability gate ci/vulncheck.sh with a stub govulncheck (no network): called findings fail the gate,
# module-level findings and allowlisted findings do not.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3

STUB="${TEST_TMP}/govulncheck"
cat > "${STUB}" <<'SH'
#!/bin/sh
# prints the fixture named by VULN_FIXTURE; VULN_FAIL=1 simulates a tool failure (no network)
[ "${VULN_FAIL:-0}" = 1 ] && { echo "vuln.go.dev unreachable" >&2; exit 2; }
cat "${VULN_FIXTURE}"
SH
chmod 0755 "${STUB}"

CALLED="${TEST_TMP}/called.json"
cat > "${CALLED}" <<'JSON'
{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck"}}
{"osv":{"id":"GO-2026-9999","summary":"test vulnerability in net/http"}}
{"finding":{"osv":"GO-2026-9999","fixed_version":"go1.99.9","trace":[{"module":"stdlib","package":"net/http","function":"Serve"}]}}
JSON
MODULE_ONLY="${TEST_TMP}/module.json"
cat > "${MODULE_ONLY}" <<'JSON'
{"osv":{"id":"GO-2026-8888","summary":"vulnerability in a required module"}}
{"finding":{"osv":"GO-2026-8888","trace":[{"module":"example.test/lib"}]}}
JSON
NONE="${TEST_TMP}/none.json"
printf '{"config":{"protocol_version":"v1.0.0"}}\n' > "${NONE}"

ALLOW="${TEST_TMP}/allowlist.txt"
printf '# test\n' > "${ALLOW}"
gate() { # fixture [env...]
  local fixture="$1"; shift
  env GOVULNCHECK="${STUB}" VULN_FIXTURE="${fixture}" VULNCHECK_ALLOWLIST="${ALLOW}" "$@" "${CI_DIR}/vulncheck.sh"
}

expect_fail "a called vulnerability fails the gate" 'ERROR GO-2026-9999 is called' gate "${CALLED}"
expect_ok   "no findings pass" gate "${NONE}"
expect_ok   "a finding at module level does not fail" gate "${MODULE_ONLY}"

printf 'GO-2026-9999 | review 2999-01-01 | accepted for the test\n' > "${ALLOW}"
expect_ok   "a called vulnerability in the allowlist passes" gate "${CALLED}"

printf 'GO-2026-8888 | review 2000-01-01 | old entry\n' > "${ALLOW}"
expect_ok   "an entry past its review date still passes" gate "${MODULE_ONLY}"
printf '%s' "${OUT}" | grep -q 'WARNING the allowlist entry GO-2026-8888 is past its review date' && ok "  ... with a warning" || fail "no warning for an entry past its review date"
printf 'not an entry\n' > "${ALLOW}"
expect_fail "a malformed allowlist is refused" "expected 'ID \\| review" gate "${NONE}"

printf '# empty\n' > "${ALLOW}"
expect_fail "a failing tool fails the gate" 'govulncheck failed' gate "${NONE}" VULN_FAIL=1

finish_tests "vulncheck-test"
