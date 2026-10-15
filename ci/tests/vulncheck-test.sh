#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the vulnerability gate ci/vulncheck.sh with a stub govulncheck (no network): called findings fail the gate,
# module-level findings and allowlisted findings do not. The same script writes the reports for GitHub code scanning with --sarif DIR
# (never a gate, accepted findings left out, every result with a location and a fingerprint).
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

# ---- ci/vulncheck.sh --sarif DIR -------------------------------------------------------------------------------------------------
have python3 || die "python3 is required"
SARIF_STUB="${TEST_TMP}/govulncheck-sarif"
cat > "${SARIF_STUB}" <<'SH'
#!/bin/sh
# prints the report in VULN_SARIF, logs the arguments (one call per line); VULN_FAIL=1 simulates a tool failure
echo "$*" >> "${VULN_ARGS_LOG}"
[ "${VULN_FAIL:-0}" = 1 ] && { echo "vuln.go.dev unreachable" >&2; exit 2; }
cat "${VULN_SARIF}"
SH
chmod 0755 "${SARIF_STUB}"
cat > "${TEST_TMP}/called.sarif" <<'JSON'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"govulncheck","rules":[
 {"id":"GO-2026-9999","shortDescription":{"text":"[GO-2026-9999] test vulnerability"}},
 {"id":"GO-2026-8888","shortDescription":{"text":"[GO-2026-8888] module only"}},
 {"id":"GO-2026-7777","shortDescription":{"text":"[GO-2026-7777] no location"}}]}},
 "results":[
 {"ruleId":"GO-2026-9999","level":"error","message":{"text":"called"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/x/y.go","uriBaseId":"%SRCROOT%"},"region":{"startLine":42}}}]},
 {"ruleId":"GO-2026-8888","level":"note","message":{"text":"module"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"go.mod","uriBaseId":"%SRCROOT%"},"region":{"startLine":1}}}]},
 {"ruleId":"GO-2026-7777","level":"warning","message":{"text":"no location"}}]}]}
JSON
printf '{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"govulncheck"}}}]}\n' > "${TEST_TMP}/nothing.sarif"
printf 'this is not json\n' > "${TEST_TMP}/broken.sarif"
SARIF_OUT="${TEST_TMP}/sarif-out"
sarif_run() { # fixture [env...]: ci/vulncheck.sh --sarif with the double
  local fixture="$1"; shift
  rm -rf "${SARIF_OUT}"; : > "${TEST_TMP}/args.log"
  env GOVULNCHECK="${SARIF_STUB}" VULN_SARIF="${fixture}" VULN_ARGS_LOG="${TEST_TMP}/args.log" VULNCHECK_ALLOWLIST="${ALLOW}" "$@" "${CI_DIR}/vulncheck.sh" --sarif "${SARIF_OUT}"
}
sarif_query() { # file python-expression over the parsed report d
  python3 -I -c 'import json, sys; d = json.load(open(sys.argv[1])); sys.exit(0 if eval(sys.argv[2]) else 1)' "$1" "$2"
}
printf '# nothing is accepted\n' > "${ALLOW}"
expect_ok   "sarif: findings do not fail (the gate stays the blocking check)" sarif_run "${TEST_TMP}/called.sarif"
[ -s "${SARIF_OUT}/govulncheck-source-release.sarif" ] && [ -s "${SARIF_OUT}/govulncheck-source-devpg.sarif" ] \
  && ok "sarif: one report per build" || fail "sarif: the reports of the two builds are missing"
grep -q -- '-format sarif ./...' "${TEST_TMP}/args.log" && grep -q -- '-format sarif -tags devpg ./...' "${TEST_TMP}/args.log" \
  && ok "sarif: the tool runs in sarif format for the release and the devpg build" || fail "sarif: wrong tool arguments ($(tr '\n' ';' < "${TEST_TMP}/args.log"))"
sarif_query "${SARIF_OUT}/govulncheck-source-release.sarif" 'len(d["runs"][0]["results"]) == 3' \
  && ok "sarif: all three findings are reported" || fail "sarif: findings are missing"
sarif_query "${SARIF_OUT}/govulncheck-source-release.sarif" 'all(r["locations"][0]["physicalLocation"]["artifactLocation"]["uri"] and r["partialFingerprints"] for r in d["runs"][0]["results"])' \
  && ok "sarif: every result has a location and a fingerprint (a result without a location gets go.mod)" || fail "sarif: a result lacks a location or a fingerprint"
sarif_query "${SARIF_OUT}/govulncheck-source-release.sarif" 'd["runs"][0]["results"][2]["locations"][0]["physicalLocation"]["artifactLocation"]["uri"] == "go.mod" and d["runs"][0]["results"][0]["locations"][0]["physicalLocation"]["region"]["startLine"] == 42' \
  && ok "sarif: a location that the tool gave stays as it is" || fail "sarif: a location was changed"
sarif_query "${SARIF_OUT}/govulncheck-source-release.sarif" 'len({r["partialFingerprints"]["govulncheck/v1"] for r in d["runs"][0]["results"]}) == 3' \
  && ok "sarif: the fingerprints differ from result to result" || fail "sarif: fingerprints collide"

printf 'GO-2026-9999 | review 2999-01-01 | accepted for the test\n' > "${ALLOW}"
expect_ok   "sarif: an accepted finding is left out" sarif_run "${TEST_TMP}/called.sarif"
sarif_query "${SARIF_OUT}/govulncheck-source-release.sarif" '[r["ruleId"] for r in d["runs"][0]["results"]] == ["GO-2026-8888", "GO-2026-7777"] and [x["id"] for x in d["runs"][0]["tool"]["driver"]["rules"]] == ["GO-2026-8888", "GO-2026-7777"]' \
  && ok "sarif: the results and the rules of an accepted finding are gone, the others stay" || fail "sarif: the allowlist was not applied"
printf '%s' "${OUT}" | grep -q 'accepted in the allowlist and left out: GO-2026-9999' && ok "sarif: the log names what was left out" || fail "sarif: the log does not name the accepted finding"

printf '# nothing is accepted\n' > "${ALLOW}"
expect_ok   "sarif: a report without results is written (it closes the alerts of an earlier run)" sarif_run "${TEST_TMP}/nothing.sarif"
sarif_query "${SARIF_OUT}/govulncheck-source-devpg.sarif" 'd["runs"][0]["results"] == [] and d["runs"][0]["tool"]["driver"]["rules"] == []' \
  && ok "sarif: ... with an empty list of results and rules" || fail "sarif: the empty report is not empty"
expect_fail "sarif: a failing tool fails" 'govulncheck failed' sarif_run "${TEST_TMP}/called.sarif" VULN_FAIL=1
expect_fail "sarif: a report that is not SARIF fails" 'is not a readable SARIF file' sarif_run "${TEST_TMP}/broken.sarif"
printf 'not an entry\n' > "${ALLOW}"
expect_fail "sarif: a malformed allowlist is refused" "expected 'ID \\| review" sarif_run "${TEST_TMP}/called.sarif"
printf '# empty\n' > "${ALLOW}"
expect_fail "sarif: --sarif needs a directory" 'usage: ci/vulncheck.sh' env GOVULNCHECK="${SARIF_STUB}" "${CI_DIR}/vulncheck.sh" --sarif
expect_fail "sarif: an unknown option is refused" 'usage: ci/vulncheck.sh' env GOVULNCHECK="${SARIF_STUB}" "${CI_DIR}/vulncheck.sh" --json x

finish_tests "vulncheck-test"
