#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the vulnerability gate ci/vulncheck.sh with a stub govulncheck (no network): called findings fail the gate,
# module-level findings and allowlisted findings do not. The same script writes the reports for GitHub code scanning with --sarif DIR
# (never a gate, accepted findings left out, every result with a location and a fingerprint), and ci/vulncheck-binaries.sh scans the
# binaries of a published release only after the keyless verification of its checksums.txt and the SHA-256 of every archive; its test
# runs the real script and ci/release/verify-blob.sh with doubles of curl, cosign and govulncheck and a release made on the fly; the scan
# runs at module level because the release binaries are stripped, and its results say so. Its size limits are covered as well: every
# download carries curl --max-filesize (the double records and enforces it), and the program in an archive and the number of archives
# are limited by the script. The shape
# of .github/workflows/vulncheck.yml is pinned as well: the only write scope is security-events in the job upload, which runs no script,
# and a run that names a release reports to a category of its own.
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

# An entry accepts until its review date has passed. The gate only warns about a lapsed entry (see above); the reports for code scanning
# show the finding again, so that a lapsed acceptance becomes an alert instead of staying hidden for good.
printf 'GO-2026-9999 | review 2000-01-01 | lapsed for the test\nGO-2026-8888 | review 2999-01-01 | accepted for the test\n' > "${ALLOW}"
expect_ok   "sarif: a finding whose allowlist entry is past its review date is reported again" sarif_run "${TEST_TMP}/called.sarif"
for build in release devpg; do
  sarif_query "${SARIF_OUT}/govulncheck-source-${build}.sarif" '[r["ruleId"] for r in d["runs"][0]["results"]] == ["GO-2026-9999", "GO-2026-7777"] and [x["id"] for x in d["runs"][0]["tool"]["driver"]["rules"]] == ["GO-2026-9999", "GO-2026-7777"]' \
    && ok "sarif: ... in the report of the ${build} build, next to the finding that an entry in force still accepts" || fail "sarif: a lapsed acceptance still hides its finding, or an acceptance in force no longer does (${build} build)"
done
printf '%s' "${OUT}" | grep -q 'kept although the allowlist names them, because the review date has passed: GO-2026-9999 (2000-01-01)' \
  && ok "sarif: ... and the log names the entry that has lapsed" || fail "sarif: the log does not name the lapsed entry"
printf '%s' "${OUT}" | grep -q 'accepted in the allowlist and left out: GO-2026-8888' \
  && ok "sarif: ... and what an entry in force leaves out" || fail "sarif: the log does not name the finding that stays accepted"
printf '%s' "${OUT}" | grep -q 'left out: GO-2026-9999' && fail "sarif: the lapsed entry is logged as accepted" || ok "sarif: ... the lapsed entry is not logged as accepted"

# The day rule: an entry accepts on its review date and has lapsed from the day after (the same function decides for the warning of the gate).
BOUNDARY="${TEST_TMP}/boundary.py"
cat > "${BOUNDARY}" <<'PY'
import datetime
import importlib.util
import os
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("vulncheck_sarif", os.path.join(sys.argv[1], "vulncheck-sarif.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
review = datetime.date(2030, 6, 15)
errors = []
for offset, want in ((-1, "in force"), (0, "in force"), (1, "lapsed")):
    in_force, lapsed = module.read_acceptances(sys.argv[2], review + datetime.timedelta(days=offset))
    got = "in force" if in_force == {"GO-2030-0001"} and not lapsed else "lapsed" if lapsed == {"GO-2030-0001": review} and not in_force else "wrong"
    if got != want:
        errors.append("%d day(s) from the review date the entry is %s, expected %s" % (offset, got, want))
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
PY
printf 'GO-2030-0001 | review 2030-06-15 | boundary of the review date\n' > "${TEST_TMP}/boundary-allowlist.txt"
expect_ok   "sarif: an allowlist entry accepts on its review date and has lapsed from the day after" python3 -I "${BOUNDARY}" "${CI_DIR}" "${TEST_TMP}/boundary-allowlist.txt"

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

# ---- .github/workflows/vulncheck.yml ---------------------------------------------------------------------------------------------
# The control is that the one job with a write scope runs no code of the repository, that the workflow never runs for a pull request, and
# that the upload takes only what the other jobs of the run made. A manual run that names a release reports the binaries to a category of
# its own, so that it does not replace the analysis of the daily scan of the latest stable release. The workflow policy check (R3) only
# asks that a write scope is listed in ci/workflow-permissions.txt.
VULN_WF="${ROOT}/.github/workflows/vulncheck.yml"
WF_CHECK="${TEST_TMP}/workflow-shape.py"
cat > "${WF_CHECK}" <<'PY'
import re
import sys

text = open(sys.argv[1], encoding="utf-8").read()
errors = []
at = text.index("\njobs:\n")
head, body = text[:at], text[at:]

on = re.search(r"^on:\n((?:  .*\n|\n)+)", head, re.M)
triggers = set(re.findall(r"^  ([a-z_]+):", on.group(1), re.M)) if on else set()
if triggers != {"push", "schedule", "workflow_dispatch"}:
    errors.append("the triggers are %s, not push, schedule and workflow_dispatch (a pull request must not run it)" % sorted(triggers))
if not re.search(r"^permissions:\n  contents: read\n", head, re.M):
    errors.append("the workflow must have the top-level permissions contents: read")

jobs, name = {}, None
for line in body.split("\n")[2:]:
    m = re.match(r"^  ([a-z][a-z0-9-]*):$", line)
    if m:
        name = m.group(1)
        jobs[name] = []
    elif name is not None:
        jobs[name].append(line)
if sorted(jobs) != ["binaries", "source", "upload"]:
    errors.append("the jobs are %s, not binaries, source and upload" % sorted(jobs))


def scripts(lines):
    """The text of every run: key of a job (inline or as a block)."""
    out, i = [], 0
    while i < len(lines):
        m = re.match(r"^(\s*)(?:- )?run:\s*(.*)$", lines[i])
        if not m:
            i += 1
            continue
        indent, rest = len(m.group(1)), m.group(2)
        text_ = [rest]
        if rest[:1] in "|>":
            i += 1
            while i < len(lines) and (not lines[i].strip() or len(lines[i]) - len(lines[i].lstrip()) > indent + 1):
                text_.append(lines[i])
                i += 1
        else:
            i += 1
        out.append("\n".join(text_))
    return out


for job, lines in jobs.items():
    block = "\n".join(lines)
    scopes = re.findall(r"^\s+([a-z-]+): write\b", block, re.M)
    if job == "upload":
        if scopes != ["security-events"]:
            errors.append("the job upload must hold security-events: write and no other write scope, it holds %s" % scopes)
        if scripts(lines):
            errors.append("the job upload must run no script")
        used = set(re.findall(r"uses: ([A-Za-z0-9_./-]+)@", block))
        if not used <= {"actions/checkout", "actions/download-artifact", "github/codeql-action/upload-sarif"}:
            errors.append("the job upload uses %s" % sorted(used))
        categories = sorted(re.findall(r"category: (\S+)", block))
        if categories != ["govulncheck/binaries", "govulncheck/binaries-manual", "govulncheck/source-devpg", "govulncheck/source-release"]:
            errors.append("the categories of the uploads are %s" % categories)
        # the report of a run that names a release (release_tag) goes to its own category, every other run keeps govulncheck/binaries
        conditions = {}
        for step in re.split(r"\n(?=      - )", block):
            category = re.search(r"^\s+category: (\S+)$", step, re.M)
            condition = re.search(r"^\s+if: (.*)$", step, re.M)
            if category:
                conditions[category.group(1)] = condition.group(1) if condition else None
        for category, expected in (
            ("govulncheck/binaries", "${{ needs.binaries.result == 'success' && inputs.release_tag == '' }}"),
            ("govulncheck/binaries-manual", "${{ needs.binaries.result == 'success' && inputs.release_tag != '' }}"),
        ):
            if conditions.get(category) != expected:
                errors.append("the upload to %s must run on %s, it runs on %s" % (category, expected, conditions.get(category)))
        if "persist-credentials: false" not in block:
            errors.append("the checkout of the job upload must not persist credentials")
    elif scopes or re.search(r"^    permissions:", block, re.M):
        errors.append("the job %s must keep the read-only token of the workflow (it asks for %s)" % (job, scopes or "a permissions key"))
    for script in scripts(lines):
        if "${{" in script:
            errors.append("the job %s puts an expression into a script (pass it through env:)" % job)
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("ok")
PY
wf_check() { python3 -I "${WF_CHECK}" "$1"; }
wf_mutate() { # name old new: a copy of the workflow with the one occurrence of OLD replaced by NEW
  python3 -I -c '
import sys
src, out, old, new = sys.argv[1:5]
text = open(src, encoding="utf-8").read()
if text.count(old) != 1:
    sys.exit("mutate: %r occurs %d times" % (old, text.count(old)))
open(out, "w", encoding="utf-8").write(text.replace(old, new))
' "${VULN_WF}" "${TEST_TMP}/wf-$1.yml" "$2" "$3" || fail "test setup: could not change the workflow ($1)"
}
expect_ok "workflow: vulncheck.yml has the reviewed shape" wf_check "${VULN_WF}"
wf_mutate pull-request $'  workflow_dispatch:\n' $'  pull_request:\n  workflow_dispatch:\n'
expect_fail "workflow: a pull request trigger is refused" 'a pull request must not run it' wf_check "${TEST_TMP}/wf-pull-request.yml"
wf_mutate source-write $'    name: source\n' $'    name: source\n    permissions:\n      security-events: write # test\n'
expect_fail "workflow: a write scope in a scan job is refused" 'the job source must keep the read-only token' wf_check "${TEST_TMP}/wf-source-write.yml"
wf_mutate binaries-write $'    name: binaries\n' $'    name: binaries\n    permissions:\n      contents: write # test\n'
expect_fail "workflow: another write scope in a scan job is refused" 'the job binaries must keep the read-only token' wf_check "${TEST_TMP}/wf-binaries-write.yml"
wf_mutate upload-contents $'      contents: read # check out' $'      contents: write # check out'
expect_fail "workflow: a second write scope in the upload job is refused" 'the job upload must hold security-events: write and no other write scope' wf_check "${TEST_TMP}/wf-upload-contents.yml"
wf_mutate upload-run $'      - name: Download the reports of the source scan\n' $'      - name: Build something\n        run: make build\n      - name: Download the reports of the source scan\n'
expect_fail "workflow: a script in the upload job is refused" 'the job upload must run no script' wf_check "${TEST_TMP}/wf-upload-run.yml"
wf_mutate upload-action $'      - name: Download the reports of the source scan\n' $'      - name: Another action\n        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0\n      - name: Download the reports of the source scan\n'
expect_fail "workflow: another action in the upload job is refused" 'the job upload uses' wf_check "${TEST_TMP}/wf-upload-action.yml"
wf_mutate category $'category: govulncheck/binaries\n' $'category: binaries\n'
expect_fail "workflow: the categories of the uploads are fixed" 'the categories of the uploads' wf_check "${TEST_TMP}/wf-category.yml"
wf_mutate manual-category 'category: govulncheck/binaries-manual' 'category: govulncheck/binaries'
expect_fail "workflow: a named release cannot be reported to the category of the latest stable release" 'the categories of the uploads' wf_check "${TEST_TMP}/wf-manual-category.yml"
wf_mutate daily-always "needs.binaries.result == 'success' && inputs.release_tag == ''" "needs.binaries.result == 'success'"
expect_fail "workflow: the upload to the category of the latest stable release must not run for a run that names a release" 'the upload to govulncheck/binaries must run on' wf_check "${TEST_TMP}/wf-daily-always.yml"
wf_mutate manual-always "needs.binaries.result == 'success' && inputs.release_tag != ''" "needs.binaries.result == 'success'"
expect_fail "workflow: the upload to the manual category must not run when no release is named" 'the upload to govulncheck/binaries-manual must run on' wf_check "${TEST_TMP}/wf-manual-always.yml"
wf_mutate expression 'run: ci/vulncheck.sh --sarif "${RUNNER_TEMP}/sarif"' 'run: ci/vulncheck.sh --sarif "${{ github.workspace }}/sarif"'
expect_fail "workflow: an expression in a script is refused" 'puts an expression into a script' wf_check "${TEST_TMP}/wf-expression.yml"

# ---- ci/vulncheck-binaries.sh ----------------------------------------------------------------------------------------------------
# The release is made on the fly: archives with a program inside, a checksums.txt that lists them (and files that must not be fetched), and
# a bundle that the cosign double accepts or refuses. curl, cosign and govulncheck are doubles; verify-blob.sh and the script are the real
# ones. A binary whose bytes contain VULNERABLE gets a finding from the govulncheck double.
BINARIES="${CI_DIR}/vulncheck-binaries.sh"
COSIGN_PIN="$(sed -n 's/^COSIGN_VERSION=//p' "${CI_DIR}/versions.env" | head -n 1)"
W="${TEST_TMP}/bin-world"
TB="${W}/tools"; CURLB="${W}/path"; SERVE="${W}/serve"; STATE="${W}/state"; CALLS="${W}/calls"; FIX="${W}/fixtures"
mkdir -p "${TB}" "${CURLB}" "${SERVE}" "${STATE}" "${CALLS}" "${FIX}"
cat > "${CURLB}/curl" <<'SH'
#!/bin/sh
# double of curl: logs the URL (curl.log) and the URL with its --max-filesize (curl-limits.log), serves the files of STUB_SERVE by the last
# segment of the URL, refuses a file above --max-filesize like curl does (exit code 63, after the status line 200), answers the lookup of
# the latest release from STUB_STATE/latest, fails with the exit code in STUB_STATE/curl.fail, and prints what -w asks for (http_code)
url=""; out=""; maxsize=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    --max-filesize) maxsize="$2"; shift 2 ;;
    -w|--connect-timeout|--max-time|--proto|--proto-redir) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "${url}" >> "${STUB_CALLS}/curl.log"
echo "${url} ${maxsize:-none}" >> "${STUB_CALLS}/curl-limits.log"
if [ -f "${STUB_STATE}/curl.fail" ]; then echo "curl: (6) Could not resolve host: github.com" >&2; exit "$(cat "${STUB_STATE}/curl.fail")"; fi
case "${url}" in
  */releases/latest)
    line="404 "; [ -f "${STUB_STATE}/latest" ] && line="$(cat "${STUB_STATE}/latest")"
    printf '%s' "${line}"
    # like curl -f: an answer of 400 or more is a failure with exit code 22
    if [ "${line%% *}" -ge 400 ]; then echo "curl: (22) The requested URL returned error: ${line%% *}" >&2; exit 22; fi
    exit 0 ;;
esac
name="${url##*/}"
if [ -f "${STUB_STATE}/flaky" ] && [ "${name}" = checksums.txt ]; then
  left="$(cat "${STUB_STATE}/flaky")"
  if [ "${left}" -gt 0 ]; then echo $((left - 1)) > "${STUB_STATE}/flaky"; printf '503'; echo "curl: (22) The requested URL returned error: 503" >&2; exit 22; fi
fi
if [ -f "${STUB_SERVE}/${name}" ]; then
  if [ -n "${maxsize}" ] && [ "$(wc -c < "${STUB_SERVE}/${name}" | tr -d ' ')" -gt "${maxsize}" ]; then
    printf '200'; echo "curl: (63) Maximum file size exceeded" >&2; exit 63
  fi
  cp "${STUB_SERVE}/${name}" "${out}"; printf '200'; exit 0
fi
printf '404'; echo "curl: (22) The requested URL returned error: 404" >&2; exit 22
SH
cat > "${TB}/cosign" <<'SH'
#!/bin/sh
echo "$*" >> "${STUB_CALLS}/cosign.log"
case "$1" in
  version) printf 'GitVersion:    %s\n' "${STUB_COSIGN_VERSION}" ;;
  verify-blob)
    if [ -f "${STUB_STATE}/cosign.fail" ]; then echo "Error: none of the expected identities matched what was in the certificate" >&2; exit 1; fi ;;
esac
exit 0
SH
cat > "${TB}/govulncheck" <<'SH'
#!/bin/sh
# prints the report of the fixture: VULNERABLE in the bytes of the binary means a finding (the fixture named in STUB_STATE/govulncheck.report,
# vulnerable by default); STUB_STATE/govulncheck.fail makes the tool fail
echo "$*" >> "${STUB_CALLS}/govulncheck.log"
if [ -f "${STUB_STATE}/govulncheck.fail" ]; then echo "govulncheck: failed to extract build info from the binary" >&2; exit 2; fi
bin=""; for a in "$@"; do bin="$a"; done
fixture=vulnerable; [ -f "${STUB_STATE}/govulncheck.report" ] && fixture="$(cat "${STUB_STATE}/govulncheck.report")"
if grep -q VULNERABLE "${bin}"; then cat "${STUB_FIXTURES}/${fixture}.sarif"; else cat "${STUB_FIXTURES}/clean.sarif"; fi
SH
chmod 0755 "${CURLB}/curl" "${TB}/cosign" "${TB}/govulncheck"
# The shape of the output of `govulncheck -mode binary -scan module -format sarif` (v1.8.0): the configuration of the run as the properties of
# the driver, and a result with a rule id, the level error (the strongest statement of a module-level scan), a message that names the
# modules, and nothing else (no location, no code flow). A scan at the default level of a stripped binary instead reports "Your code calls
# vulnerable functions in ..." with code flows for every package of the affected modules, which the programs do not link.
cat > "${FIX}/vulnerable.sarif" <<'JSON'
{"version":"2.1.0","$schema":"https://json.schemastore.org/sarif-2.1.0.json","runs":[{"tool":{"driver":{"name":"govulncheck","semanticVersion":"v1.8.0","informationUri":"https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck",
 "properties":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scanner_version":"v1.8.0","db":"https://vuln.go.dev","scan_level":"module","scan_mode":"binary"},
 "rules":[{"id":"GO-2026-9999","shortDescription":{"text":"[GO-2026-9999] test vulnerability"},"fullDescription":{"text":"test vulnerability"},"help":{"text":"details"},"helpUri":"https://pkg.go.dev/vuln/GO-2026-9999","properties":{"tags":[]}}]}},
 "results":[{"ruleId":"GO-2026-9999","level":"error","message":{"text":"Your code depends on 2 vulnerable modules (example.test/lib and stdlib). Run the call-level analysis to understand whether your code actually calls the vulnerabilities."}}]}]}
JSON
printf '{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"govulncheck","semanticVersion":"v1.8.0","properties":{"scan_level":"module","scan_mode":"binary"},"rules":[]}},"results":[]}]}\n' > "${FIX}/clean.sarif"
# reports that are not at module level: the default level of the tool, and a source scan
sed 's/"scan_level":"module"/"scan_level":"symbol"/' "${FIX}/vulnerable.sarif" > "${FIX}/symbol-level.sarif"
sed 's/"scan_mode":"binary"/"scan_mode":"source"/' "${FIX}/vulnerable.sarif" > "${FIX}/source-mode.sarif"
export STUB_CALLS="${CALLS}" STUB_STATE="${STATE}" STUB_SERVE="${SERVE}" STUB_FIXTURES="${FIX}" STUB_COSIGN_VERSION="${COSIGN_PIN}"

mkrelease() { # mode: makes the files of the release v9.9.9 in STUB_SERVE (modes: ok, mismatch, no-member, symlink, none, duplicate, big-zip)
  rm -rf "${SERVE}"; mkdir -p "${SERVE}"
  python3 -I - "${SERVE}" "$1" <<'PY'
import hashlib
import io
import os
import sys
import tarfile
import zipfile

serve, mode = sys.argv[1], sys.argv[2]
v = "9.9.9"
sums = []


def put(name, data):
    with open(os.path.join(serve, name), "wb") as f:
        f.write(data)
    return hashlib.sha256(data).hexdigest()


def tgz(name, members, symlink=None):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as t:
        for member, data in members:
            info = tarfile.TarInfo(member)
            info.size = len(data)
            info.mode = 0o755
            t.addfile(info, io.BytesIO(data))
        if symlink:
            info = tarfile.TarInfo(symlink)
            info.type = tarfile.SYMTYPE
            info.linkname = "/etc/passwd"
            t.addfile(info)
    return put(name, buf.getvalue())


def zipf(name, members):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for member, data in members:
            z.writestr(member, data)
    return put(name, buf.getvalue())


if mode != "none":
    if mode == "no-member":
        a = tgz("myrctl_%s_linux_amd64.tar.gz" % v, [("LICENSE", b"license")])
    elif mode == "symlink":
        a = tgz("myrctl_%s_linux_amd64.tar.gz" % v, [("LICENSE", b"license")], symlink="myrctl")
    else:
        a = tgz("myrctl_%s_linux_amd64.tar.gz" % v, [("LICENSE", b"license"), ("myrctl", b"\x7fELF VULNERABLE myrctl")])
    sums.append((a, "myrctl_%s_linux_amd64.tar.gz" % v))
    b = tgz("myriarchd_%s_linux_amd64.tar.gz" % v, [("README.md", b"readme"), ("myriarchd", b"\x7fELF VULNERABLE myriarchd")])
    sums.append((b, "myriarchd_%s_linux_amd64.tar.gz" % v))
    exe = b"MZ clean myrctl"
    if mode == "big-zip":
        exe += b"." * 25  # 40 bytes, the only program above 30 bytes (the others are 22, 25 and 15 bytes)
    c = zipf("myrctl_%s_windows_amd64.zip" % v, [("LICENSE", b"license"), ("myrctl.exe", exe)])
    sums.append((c, "myrctl_%s_windows_amd64.zip" % v))
# entries of checksums.txt that must never be fetched: a bare binary, an SBOM, a license file, an archive of another version
for name in ("myriarch-updater_%s_linux_amd64" % v, "myrctl_%s_linux_amd64.tar.gz.sbom.json" % v, "LICENSE", "myrctl_1.0.0_linux_amd64.tar.gz"):
    sums.append((hashlib.sha256(name.encode()).hexdigest(), name))
if mode == "duplicate":
    sums.append(sums[0])
text = "".join("%s  %s\n" % s for s in sums)
put("checksums.txt", text.encode())
put("checksums.txt.sigstore.json", b"{}\n")
if mode == "mismatch":
    put("myriarchd_%s_linux_amd64.tar.gz" % v, b"not the archive that checksums.txt lists")
PY
}
printf '302 https://github.com/myriarch/myriarch/releases/tag/v9.9.9' > "${STATE}/latest"
reset_world() { rm -f "${STATE}/cosign.fail" "${STATE}/curl.fail" "${STATE}/govulncheck.fail" "${STATE}/govulncheck.report" "${STATE}/flaky"; : > "${CALLS}/curl.log"; : > "${CALLS}/curl-limits.log"; : > "${CALLS}/cosign.log"; : > "${CALLS}/govulncheck.log"; rm -rf "${W}/out"; }
binaries() { # script arguments...: the real script with the doubles; TOOLS_BIN holds cosign and govulncheck, curl comes first in PATH
  env PATH="${CURLB}:${PATH}" TOOLS_BIN="${TB}" VULNCHECK_ALLOWLIST="${ALLOW}" VULNCHECK_DOWNLOAD_RETRY_SECONDS="${RETRY:-0}" \
    VULNCHECK_MAX_BINARY="${LIMIT_BINARY:-}" VULNCHECK_MAX_ARCHIVES="${LIMIT_ARCHIVES:-}" "${BINARIES}" --out "${W}/out" "$@"
}
report_query() { sarif_query "${W}/out/govulncheck-binaries.sarif" "$1"; }
printf '# nothing is accepted\n' > "${ALLOW}"

reset_world; mkrelease ok
expect_ok "binaries: the latest release is verified and its binaries are scanned" binaries
curl_log="$(sed 's#https://github.com/myriarch/myriarch/releases/##' "${CALLS}/curl.log" | tr '\n' ' ')"
[ "${curl_log}" = "latest download/v9.9.9/checksums.txt download/v9.9.9/checksums.txt.sigstore.json download/v9.9.9/myrctl_9.9.9_linux_amd64.tar.gz download/v9.9.9/myriarchd_9.9.9_linux_amd64.tar.gz download/v9.9.9/myrctl_9.9.9_windows_amd64.zip " ] \
  && ok "binaries: the lookup, the signed list and its bundle first, then the three archives of the version (no bare binary, SBOM, license or archive of another version)" \
  || fail "binaries: unexpected downloads: ${curl_log}"
grep -q 'verify-blob' "${CALLS}/cosign.log" && grep -q -- '--certificate-identity-regexp \^https://github\\.com/myriarch/myriarch/\\\.github/workflows/release\\\.yml@refs/tags/v9\\\.9\\\.9\$' "${CALLS}/cosign.log" \
  && grep -q -- '--certificate-oidc-issuer https://token.actions.githubusercontent.com' "${CALLS}/cosign.log" \
  && ok "binaries: the signature is checked against the identity of the release workflow at the tag, bound to the version, and the GitHub Actions issuer" \
  || fail "binaries: the cosign call is not the bound identity ($(grep verify-blob "${CALLS}/cosign.log" | tr '\n' ' '))"
[ "$(grep -c '^-mode binary -scan module -format sarif ' "${CALLS}/govulncheck.log")" = 3 ] \
  && ok "binaries: govulncheck runs once per archive, in binary mode at module level with SARIF output (the binaries are stripped)" \
  || fail "binaries: wrong govulncheck calls ($(tr '\n' ';' < "${CALLS}/govulncheck.log"))"
report_query 'len(d["runs"]) == 1 and [r["ruleId"] for r in d["runs"][0]["results"]] == ["GO-2026-9999"]' && ok "binaries: one result per vulnerability" || fail "binaries: wrong results"
report_query 'd["runs"][0]["results"][0]["properties"]["binaries"] == ["myrctl_9.9.9_linux_amd64.tar.gz", "myriarchd_9.9.9_linux_amd64.tar.gz"] and "2 of the 3 binaries of the release v9.9.9" in d["runs"][0]["results"][0]["message"]["text"]' \
  && ok "binaries: the result names the two binaries that contain the vulnerable code, of three" || fail "binaries: the affected binaries are wrong"
report_query 'all(r["locations"][0]["physicalLocation"]["artifactLocation"]["uri"] == "go.mod" and r["partialFingerprints"] and "codeFlows" not in r and "stacks" not in r for r in d["runs"][0]["results"]) and d["runs"][0]["tool"]["driver"]["properties"]["scan_mode"] == "binary"' \
  && ok "binaries: the result has a location (go.mod) and a fingerprint, and no code flow without a file" || fail "binaries: the result does not fit code scanning"
report_query '"Affected modules: example.test/lib, stdlib (stdlib is the Go version of the build)" in d["runs"][0]["results"][0]["message"]["text"] and d["runs"][0]["tool"]["driver"]["rules"][0]["id"] == "GO-2026-9999"' \
  && ok "binaries: the modules that the tool names in its message and the rule are kept" || fail "binaries: module or rule lost"
report_query 'all(r["level"] == "warning" for r in d["runs"][0]["results"]) and d["runs"][0]["tool"]["driver"]["properties"]["scan_level"] == "module"' \
  && ok "binaries: the result is a warning although the tool calls a finding of a module-level scan an error, and the report says that it is at module level" \
  || fail "binaries: the level of the result is wrong"
report_query 'all("module level" in r["message"]["text"] and "stripped" in r["message"]["text"] and "not that it contains or calls the vulnerable code" in r["message"]["text"] and "vulnerable symbols" not in r["message"]["text"] and "calls vulnerable functions" not in r["message"]["text"] for r in d["runs"][0]["results"])' \
  && ok "binaries: the message says that the scan works at module level and does not claim that the code is contained or called" \
  || fail "binaries: the message does not state the precision of the scan"

reset_world; printf 'GO-2026-9999 | review 2999-01-01 | accepted for the test\n' > "${ALLOW}"
expect_ok "binaries: an accepted finding is left out" binaries --tag v9.9.9
report_query 'd["runs"][0]["results"] == [] and d["runs"][0]["tool"]["driver"]["rules"] == []' && ok "binaries: ... the report holds no result" || fail "binaries: the accepted finding is in the report"
printf '%s' "${OUT}" | grep -q 'accepted in the allowlist and left out: GO-2026-9999' && ok "binaries: ... and the log names it" || fail "binaries: the log does not name the accepted finding"
grep -q 'releases/latest' "${CALLS}/curl.log" && fail "binaries: --tag still looked up the latest release" || ok "binaries: --tag skips the lookup of the latest release"
reset_world; printf 'GO-2026-9999 | review 2000-01-01 | lapsed for the test\n' > "${ALLOW}"
expect_ok "binaries: a finding whose allowlist entry is past its review date is reported again" binaries --tag v9.9.9
report_query '[r["ruleId"] for r in d["runs"][0]["results"]] == ["GO-2026-9999"]' && ok "binaries: ... the report holds the result" || fail "binaries: a lapsed acceptance still hides the finding"
printf '%s' "${OUT}" | grep -q 'kept although the allowlist names them, because the review date has passed: GO-2026-9999 (2000-01-01)' \
  && ok "binaries: ... and the log names the entry that has lapsed" || fail "binaries: the log does not name the lapsed entry"
printf '# nothing is accepted\n' > "${ALLOW}"

# a failed verification stops everything before an archive is fetched
reset_world; mkrelease ok; : > "${STATE}/cosign.fail"
expect_fail "binaries: an invalid signature stops the scan" 'ERROR \[signature_invalid\].*nothing was scanned' binaries --tag v9.9.9
grep -q 'tar.gz\|\.zip' "${CALLS}/curl.log" && fail "binaries: an archive was fetched after the signature failed" || ok "binaries: no archive is fetched when the signature is invalid"
[ ! -s "${CALLS}/govulncheck.log" ] && [ ! -e "${W}/out/govulncheck-binaries.sarif" ] && ok "binaries: govulncheck did not run and no report was written" || fail "binaries: something was scanned after the signature failed"

reset_world; mkrelease mismatch
expect_fail "binaries: an archive that differs from checksums.txt stops the scan" 'ERROR \[checksum_mismatch\] myriarchd_9.9.9_linux_amd64.tar.gz does not match checksums.txt' binaries --tag v9.9.9
[ ! -s "${CALLS}/govulncheck.log" ] && ok "binaries: ... before any binary is scanned" || fail "binaries: a binary was scanned although an archive did not match"

reset_world; mkrelease no-member
expect_fail "binaries: an archive without its program is refused" 'ERROR \[archive_invalid\] myrctl_9.9.9_linux_amd64.tar.gz does not hold myrctl as a regular file' binaries --tag v9.9.9
reset_world; mkrelease symlink
expect_fail "binaries: a symbolic link in place of the program is refused" 'ERROR \[archive_invalid\]' binaries --tag v9.9.9
reset_world; mkrelease none
expect_fail "binaries: a signed list without an archive of the version is refused (not an empty pass)" 'ERROR \[no_archives\]' binaries --tag v9.9.9
reset_world; mkrelease duplicate
expect_fail "binaries: an archive that is listed twice is refused" 'ERROR \[checksum_entry\] checksums.txt lists myrctl_9.9.9_linux_amd64.tar.gz more than once' binaries --tag v9.9.9

reset_world; mkrelease ok; : > "${STATE}/govulncheck.fail"
expect_fail "binaries: a failing govulncheck fails the scan" 'ERROR \[govulncheck_failed\].*failed to extract build info' binaries --tag v9.9.9

# The claim of the results (a binary was built with an affected version, nothing about what it calls) holds for a scan at module level only,
# so the merge refuses the report of another level or mode instead of describing it wrongly.
for other in symbol-level source-mode; do
  reset_world; mkrelease ok; echo "${other}" > "${STATE}/govulncheck.report"
  expect_fail "binaries: a report of the kind ${other} is refused" 'is not the report of govulncheck -mode binary -scan module' binaries --tag v9.9.9
  [ ! -e "${W}/out/govulncheck-binaries.sarif" ] && ok "binaries: ... and no report is written" || fail "binaries: a report was written from a report of the kind ${other}"
done

# The message of a result: the modules are read from the message of the tool (a list of one, two or more), and the text stays within the
# limit that code scanning shows as the title of an alert when the lists are long.
MESSAGE_CHECK="${TEST_TMP}/message-check.py"
cat > "${MESSAGE_CHECK}" <<'PY'
import importlib.util
import os
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("vulncheck_sarif", os.path.join(sys.argv[1], "vulncheck-sarif.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
errors = []


def result(text):
    return {"ruleId": "GO-2026-9999", "message": {"text": text}}


for text, want in (
    ("Your code depends on 1 vulnerable module (stdlib). Run the call-level analysis to understand whether your code actually calls the vulnerabilities.", {"stdlib"}),
    ("Your code depends on 2 vulnerable modules (example.test/lib and stdlib). Run the call-level analysis.", {"example.test/lib", "stdlib"}),
    ("Your code depends on 3 vulnerable modules (a.test/x, b.test/y, and stdlib). Run the call-level analysis.", {"a.test/x", "b.test/y", "stdlib"}),
    ("Your code calls vulnerable functions in 1 package (html/template).", set()),
    ("", set()),
):
    got = module.modules_of(result(text))
    if got != want:
        errors.append("modules_of(%r) = %r, expected %r" % (text[:60], sorted(got), sorted(want)))
if module.modules_of({"ruleId": "x"}) != set() or module.modules_of({"message": {"text": 7}}) != set():
    errors.append("a result without a text of a message names a module")

names = ["myriarch-updater_0.1.0_linux_amd64.tar.gz", "myriarch-updater_0.1.0_linux_arm64.tar.gz", "myriarchd_0.1.0_darwin_arm64.tar.gz"] * 6
mods = ["example.test/module%02d" % i for i in range(30)] + ["stdlib"]
long_text = module.compose("GO-2026-9999", "v0.1.0", names, 18, mods)
if len(long_text) > module.MAX_TEXT:
    errors.append("a message with long lists has %d characters, the limit is %d" % (len(long_text), module.MAX_TEXT))
if " more" not in long_text or "module level" not in long_text or "not that it contains or calls the vulnerable code" not in long_text:
    errors.append("a cut message keeps neither its count nor its statement of the precision: %s" % long_text)
short_text = module.compose("GO-2026-9999", "v0.1.0", ["a.tar.gz", "b.zip"], 3, ["stdlib"])
if "(a.tar.gz, b.zip)" not in short_text or "Affected modules: stdlib " not in short_text or " more" in short_text:
    errors.append("a short message is cut or lost a name: %s" % short_text)
if "not named by govulncheck" not in module.compose("GO-2026-9999", "v0.1.0", ["a.tar.gz"], 1, []):
    errors.append("a result without a named module does not say so")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
PY
expect_ok "binaries: the modules are read from the message of the tool, and long lists are cut within the limit of a message" python3 -I "${MESSAGE_CHECK}" "${CI_DIR}"

# The size limits. The downloads: curl gets --max-filesize (1 MiB for checksums.txt and its bundle, 200 MiB for an archive), the double
# records it and refuses a file above it like curl does (exit code 63), and the script fails on that without a retry. The program in an
# archive and the number of archives are limited by the script itself; the tests lower those two limits through the environment (a
# program of more than 512 MiB is not worth making, and the pattern of the archive names admits 48 different names, so a signed list of
# more than 64 archives cannot exist) and check that the environment cannot raise them. The programs of the release v9.9.9 are 22 bytes
# (myrctl), 25 (myriarchd) and 15 (myrctl.exe), or 40 in the mode big-zip.
grow() { python3 -I -c 'import sys; f = open(sys.argv[1], "r+b"); f.truncate(int(sys.argv[2])); f.close()' "$1" "$2"; }
reset_world; mkrelease ok
expect_ok "binaries: the size limits that apply are logged" binaries --tag v9.9.9
printf '%s' "${OUT}" | grep -q 'size limits: 1048576 bytes for checksums.txt and its bundle, 209715200 for an archive, 536870912 for a program, 64 archives' \
  && ok "binaries: ... 1 MiB for the signed list and its bundle, 200 MiB for an archive, 512 MiB for a program, 64 archives" \
  || fail "binaries: the log does not name the default size limits"
limits="$(sed 's#https://github.com/myriarch/myriarch/releases/##' "${CALLS}/curl-limits.log" | tr '\n' ';')"
want="download/v9.9.9/checksums.txt 1048576;download/v9.9.9/checksums.txt.sigstore.json 1048576;download/v9.9.9/myrctl_9.9.9_linux_amd64.tar.gz 209715200;download/v9.9.9/myriarchd_9.9.9_linux_amd64.tar.gz 209715200;download/v9.9.9/myrctl_9.9.9_windows_amd64.zip 209715200;"
[ "${limits}" = "${want}" ] \
  && ok "binaries: curl is called with --max-filesize for every download of a release file" \
  || fail "binaries: unexpected --max-filesize arguments: ${limits}"

reset_world; mkrelease ok; grow "${SERVE}/checksums.txt" 1048577
RETRY=30 expect_fail "binaries: a signed list above 1 MiB is refused (curl exit code 63) and not retried" 'ERROR \[download_failed\] the download of https://github.com/myriarch/myriarch/releases/download/v9.9.9/checksums.txt failed \(curl exit code 63, HTTP 200; not retried, 1 attempt\): curl: \(63\) Maximum file size exceeded' binaries --tag v9.9.9
[ "$(grep -c 'checksums.txt$' "${CALLS}/curl.log")" = 1 ] && [ ! -s "${CALLS}/cosign.log" ] && [ ! -s "${CALLS}/govulncheck.log" ] \
  && ok "binaries: ... one attempt, no signature check and nothing scanned" || fail "binaries: the list above the limit was retried, verified or scanned"
reset_world; mkrelease ok; grow "${SERVE}/checksums.txt.sigstore.json" 1048577
RETRY=30 expect_fail "binaries: a bundle above 1 MiB is refused and not retried" 'ERROR \[download_failed\] the download of https://github.com/myriarch/myriarch/releases/download/v9.9.9/checksums.txt.sigstore.json failed \(curl exit code 63, HTTP 200; not retried, 1 attempt\)' binaries --tag v9.9.9
[ ! -s "${CALLS}/cosign.log" ] && [ ! -s "${CALLS}/govulncheck.log" ] && ok "binaries: ... and the signature is not checked" || fail "binaries: the signature was checked with a bundle above the limit"
reset_world; mkrelease ok; grow "${SERVE}/myrctl_9.9.9_linux_amd64.tar.gz" 209715201
RETRY=30 expect_fail "binaries: an archive above 200 MiB is refused and not retried" 'ERROR \[download_failed\] the download of https://github.com/myriarch/myriarch/releases/download/v9.9.9/myrctl_9.9.9_linux_amd64.tar.gz failed \(curl exit code 63, HTTP 200; not retried, 1 attempt\)' binaries --tag v9.9.9
[ ! -s "${CALLS}/govulncheck.log" ] && [ ! -e "${W}/out/govulncheck-binaries.sarif" ] && ok "binaries: ... and nothing is scanned" || fail "binaries: something was scanned after an archive above the limit"

reset_world; mkrelease ok
LIMIT_BINARY=25 expect_ok "binaries: a program of exactly the size limit is read out of a tar archive" binaries --tag v9.9.9
reset_world; mkrelease ok
LIMIT_BINARY=24 expect_fail "binaries: a program in a tar archive above the size limit is refused" 'ERROR \[archive_invalid\] myriarchd_9.9.9_linux_amd64.tar.gz does not hold myriarchd as a regular file within the size limit of 24 bytes' binaries --tag v9.9.9
[ ! -e "${W}/out/govulncheck-binaries.sarif" ] && ok "binaries: ... and no report is written" || fail "binaries: a report was written after a program above the limit"
reset_world; mkrelease big-zip
LIMIT_BINARY=40 expect_ok "binaries: a program of exactly the size limit is read out of a zip archive" binaries --tag v9.9.9
reset_world; mkrelease big-zip
LIMIT_BINARY=39 expect_fail "binaries: a program in a zip archive above the size limit is refused" 'ERROR \[archive_invalid\] myrctl_9.9.9_windows_amd64.zip does not hold myrctl.exe as a regular file within the size limit of 39 bytes' binaries --tag v9.9.9

reset_world; mkrelease ok
LIMIT_ARCHIVES=3 expect_ok "binaries: a signed list with exactly as many archives as the limit allows is scanned" binaries --tag v9.9.9
reset_world; mkrelease ok
LIMIT_ARCHIVES=2 expect_fail "binaries: a signed list with more archives than the limit allows is refused" 'ERROR \[no_archives\] the signed checksums.txt of v9.9.9 lists more than 2 archives' binaries --tag v9.9.9
grep -q 'tar.gz\|\.zip' "${CALLS}/curl.log" && fail "binaries: an archive was fetched although the list has too many" || ok "binaries: ... before an archive is fetched"

reset_world; mkrelease ok
LIMIT_BINARY=536870913 expect_fail "binaries: the environment cannot raise the limit of a program" 'ERROR \[input_invalid\] VULNCHECK_MAX_BINARY may only lower the limit of 536870912' binaries --tag v9.9.9
LIMIT_ARCHIVES=65 expect_fail "binaries: the environment cannot raise the limit of the archives" 'ERROR \[input_invalid\] VULNCHECK_MAX_ARCHIVES may only lower the limit of 64' binaries --tag v9.9.9
LIMIT_BINARY=ten expect_fail "binaries: a limit that is no number is refused" 'ERROR \[input_invalid\] VULNCHECK_MAX_BINARY has an unexpected format' binaries --tag v9.9.9
LIMIT_ARCHIVES=0 expect_fail "binaries: a limit of zero is refused" 'ERROR \[input_invalid\] VULNCHECK_MAX_ARCHIVES has an unexpected format' binaries --tag v9.9.9
[ ! -s "${CALLS}/curl.log" ] && ok "binaries: ... all before anything is downloaded" || fail "binaries: a download started with an invalid limit"

# the network
reset_world; mkrelease ok; echo 6 > "${STATE}/curl.fail"
expect_fail "binaries: no network access is a clear failure" 'ERROR \[download_failed\] the download of https://github.com/myriarch/myriarch/releases/download/v9.9.9/checksums.txt failed.*Could not resolve host' binaries --tag v9.9.9
[ ! -s "${CALLS}/govulncheck.log" ] && ok "binaries: ... and nothing is scanned" || fail "binaries: something was scanned without a network"
reset_world; mkrelease ok; echo 2 > "${STATE}/flaky"
RETRY=30 expect_ok "binaries: a transient failure (HTTP 503) of a download is retried" binaries --tag v9.9.9
[ "$(grep -c 'checksums.txt$' "${CALLS}/curl.log")" = 3 ] && ok "binaries: ... twice, and the third attempt succeeds" || fail "binaries: the download was not retried as expected ($(grep -c 'checksums.txt$' "${CALLS}/curl.log"))"
reset_world; rm -rf "${SERVE}"; mkdir -p "${SERVE}"
RETRY=30 expect_fail "binaries: a release that does not exist is not retried (HTTP 404)" 'ERROR \[download_failed\].*not retried, 1 attempt\)' binaries --tag v9.9.9

# which release
reset_world; mkrelease ok; printf '302 https://github.com/myriarch/myriarch/releases' > "${STATE}/latest"
expect_ok "binaries: a repository whose latest page leads to the release list has no stable release" binaries
report_query 'd["runs"][0]["results"] == []' && [ "$(wc -l < "${CALLS}/curl.log" | tr -d ' ')" = 1 ] && ok "binaries: ... the report is empty and nothing is downloaded" || fail "binaries: a missing release is not handled"
reset_world; mkrelease ok; printf '404 ' > "${STATE}/latest"
expect_fail "binaries: a 404 is an error, not an empty report (it would close the alerts of the earlier scans)" 'ERROR \[download_failed\] the lookup of the latest release failed \(curl exit code 22, HTTP 404; not retried' binaries
[ ! -e "${W}/out/govulncheck-binaries.sarif" ] && ok "binaries: ... and no report is written" || fail "binaries: a report was written after a failed lookup"
reset_world; mkrelease ok; printf '302 https://example.test/elsewhere' > "${STATE}/latest"
expect_fail "binaries: a latest release that leads elsewhere is refused" 'ERROR \[release_lookup\]' binaries
reset_world; mkrelease ok; printf '503 ' > "${STATE}/latest"
expect_fail "binaries: a failing GitHub is an error after the retries, not an empty report" 'ERROR \[download_failed\] the lookup of the latest release failed \(curl exit code 22, HTTP 503; the retry window' binaries
reset_world; mkrelease ok; printf '200 ' > "${STATE}/latest"
expect_fail "binaries: an answer that is no redirect is refused" 'ERROR \[release_lookup\] the lookup of the latest release answered HTTP 200' binaries
reset_world; mkrelease ok; printf '302 https://github.com/myriarch/myriarch/releases/tag/not-a-version' > "${STATE}/latest"
expect_fail "binaries: a tag that is no version is refused" 'ERROR \[tag_invalid\]' binaries
reset_world
expect_fail "binaries: an invalid --tag is refused" 'ERROR \[tag_invalid\]' binaries --tag '../v1.0.0'
expect_fail "binaries: --out is required" 'usage: vulncheck-binaries.sh' env PATH="${CURLB}:${PATH}" TOOLS_BIN="${TB}" "${BINARIES}"
expect_fail "binaries: an unknown option is refused" 'usage: vulncheck-binaries.sh' env PATH="${CURLB}:${PATH}" TOOLS_BIN="${TB}" "${BINARIES}" --scan-all
expect_fail "binaries: a missing govulncheck is a clear failure" 'ERROR \[tool_missing\] govulncheck is not executable' env PATH="${CURLB}:${PATH}" TOOLS_BIN="${W}/empty" "${BINARIES}" --out "${W}/out"

finish_tests "vulncheck-test"
