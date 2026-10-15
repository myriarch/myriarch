#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the vulnerability gate ci/vulncheck.sh with a stub govulncheck (no network): called findings fail the gate,
# module-level findings and allowlisted findings do not. The same script writes the reports for GitHub code scanning with --sarif DIR
# (never a gate, accepted findings left out, every result with a location and a fingerprint), and ci/vulncheck-binaries.sh scans the
# binaries of a published release only after the keyless verification of its checksums.txt and the SHA-256 of every archive; its test
# runs the real script and ci/release/verify-blob.sh with doubles of curl, cosign and govulncheck and a release made on the fly.
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
# double of curl: logs the URL, serves the files of STUB_SERVE by the last segment of the URL, answers the lookup of the latest release
# from STUB_STATE/latest, fails with the exit code in STUB_STATE/curl.fail, and prints what -w asks for (http_code)
url=""; out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w|--max-filesize|--connect-timeout|--max-time|--proto|--proto-redir) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "${url}" >> "${STUB_CALLS}/curl.log"
if [ -f "${STUB_STATE}/curl.fail" ]; then echo "curl: (6) Could not resolve host: github.com" >&2; exit "$(cat "${STUB_STATE}/curl.fail")"; fi
case "${url}" in
  */releases/latest)
    if [ -f "${STUB_STATE}/latest" ]; then cat "${STUB_STATE}/latest"; else printf '404 '; fi
    exit 0 ;;
esac
name="${url##*/}"
if [ -f "${STUB_STATE}/flaky" ] && [ "${name}" = checksums.txt ]; then
  left="$(cat "${STUB_STATE}/flaky")"
  if [ "${left}" -gt 0 ]; then echo $((left - 1)) > "${STUB_STATE}/flaky"; printf '503'; echo "curl: (22) The requested URL returned error: 503" >&2; exit 22; fi
fi
if [ -f "${STUB_SERVE}/${name}" ]; then cp "${STUB_SERVE}/${name}" "${out}"; printf '200'; exit 0; fi
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
# prints the report of the fixture: VULNERABLE in the bytes of the binary means a finding; STUB_STATE/govulncheck.fail makes the tool fail
echo "$*" >> "${STUB_CALLS}/govulncheck.log"
if [ -f "${STUB_STATE}/govulncheck.fail" ]; then echo "govulncheck: failed to extract build info from the binary" >&2; exit 2; fi
bin=""; for a in "$@"; do bin="$a"; done
if grep -q VULNERABLE "${bin}"; then cat "${STUB_FIXTURES}/vulnerable.sarif"; else cat "${STUB_FIXTURES}/clean.sarif"; fi
SH
chmod 0755 "${CURLB}/curl" "${TB}/cosign" "${TB}/govulncheck"
cat > "${FIX}/vulnerable.sarif" <<'JSON'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"govulncheck","semanticVersion":"v1.8.0","informationUri":"https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck",
 "properties":{"scan_mode":"binary","scan_level":"symbol"},
 "rules":[{"id":"GO-2026-9999","shortDescription":{"text":"[GO-2026-9999] test vulnerability"},"helpUri":"https://pkg.go.dev/vuln/GO-2026-9999","properties":{"tags":[]}}]}},
 "results":[{"ruleId":"GO-2026-9999","level":"error","message":{"text":"Your code calls vulnerable functions in 1 package (example.test/lib)."},
 "codeFlows":[{"threadFlows":[{"locations":[{"module":"example.test/lib@v1.2.3","location":{"physicalLocation":{"artifactLocation":{},"region":{}},"message":{"text":"example.test/lib.Func"}}}]}],"message":{"text":"flow"}}],
 "stacks":[{"message":{"text":"stack"},"frames":[{"module":"example.test/lib@v1.2.3","location":{"physicalLocation":{"artifactLocation":{},"region":{}}}}]}]}]}]}
JSON
printf '{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"govulncheck","semanticVersion":"v1.8.0"}}}]}\n' > "${FIX}/clean.sarif"
export STUB_CALLS="${CALLS}" STUB_STATE="${STATE}" STUB_SERVE="${SERVE}" STUB_FIXTURES="${FIX}" STUB_COSIGN_VERSION="${COSIGN_PIN}"

mkrelease() { # mode: makes the files of the release v9.9.9 in STUB_SERVE (modes: ok, mismatch, no-member, symlink, none, duplicate)
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
    c = zipf("myrctl_%s_windows_amd64.zip" % v, [("LICENSE", b"license"), ("myrctl.exe", b"MZ clean myrctl")])
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
reset_world() { rm -f "${STATE}/cosign.fail" "${STATE}/curl.fail" "${STATE}/govulncheck.fail" "${STATE}/flaky"; : > "${CALLS}/curl.log"; : > "${CALLS}/cosign.log"; : > "${CALLS}/govulncheck.log"; rm -rf "${W}/out"; }
binaries() { # script arguments...: the real script with the doubles; TOOLS_BIN holds cosign and govulncheck, curl comes first in PATH
  env PATH="${CURLB}:${PATH}" TOOLS_BIN="${TB}" VULNCHECK_ALLOWLIST="${ALLOW}" VULNCHECK_DOWNLOAD_RETRY_SECONDS="${RETRY:-0}" "${BINARIES}" --out "${W}/out" "$@"
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
[ "$(grep -c '^-mode binary -format sarif ' "${CALLS}/govulncheck.log")" = 3 ] && ok "binaries: govulncheck runs once per archive, in binary mode with SARIF output" || fail "binaries: wrong govulncheck calls ($(tr '\n' ';' < "${CALLS}/govulncheck.log"))"
report_query 'len(d["runs"]) == 1 and [r["ruleId"] for r in d["runs"][0]["results"]] == ["GO-2026-9999"]' && ok "binaries: one result per vulnerability" || fail "binaries: wrong results"
report_query 'd["runs"][0]["results"][0]["properties"]["binaries"] == ["myrctl_9.9.9_linux_amd64.tar.gz", "myriarchd_9.9.9_linux_amd64.tar.gz"] and "2 of the 3 binaries of the release v9.9.9" in d["runs"][0]["results"][0]["message"]["text"]' \
  && ok "binaries: the result names the two binaries that contain the vulnerable code, of three" || fail "binaries: the affected binaries are wrong"
report_query 'all(r["locations"][0]["physicalLocation"]["artifactLocation"]["uri"] == "go.mod" and r["partialFingerprints"] and "codeFlows" not in r and "stacks" not in r for r in d["runs"][0]["results"]) and d["runs"][0]["tool"]["driver"]["properties"]["scan_mode"] == "binary"' \
  && ok "binaries: the result has a location (go.mod) and a fingerprint, and no code flow without a file" || fail "binaries: the result does not fit code scanning"
report_query '"example.test/lib@v1.2.3" in d["runs"][0]["results"][0]["message"]["text"] and d["runs"][0]["tool"]["driver"]["rules"][0]["id"] == "GO-2026-9999"' && ok "binaries: the module and the rule are kept" || fail "binaries: module or rule lost"

reset_world; printf 'GO-2026-9999 | review 2999-01-01 | accepted for the test\n' > "${ALLOW}"
expect_ok "binaries: an accepted finding is left out" binaries --tag v9.9.9
report_query 'd["runs"][0]["results"] == [] and d["runs"][0]["tool"]["driver"]["rules"] == []' && ok "binaries: ... the report holds no result" || fail "binaries: the accepted finding is in the report"
printf '%s' "${OUT}" | grep -q 'accepted in the allowlist and left out: GO-2026-9999' && ok "binaries: ... and the log names it" || fail "binaries: the log does not name the accepted finding"
grep -q 'releases/latest' "${CALLS}/curl.log" && fail "binaries: --tag still looked up the latest release" || ok "binaries: --tag skips the lookup of the latest release"
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
reset_world; mkrelease ok; printf '404 ' > "${STATE}/latest"
expect_ok "binaries: a repository without a release has nothing to scan" binaries
report_query 'd["runs"][0]["results"] == []' && [ "$(wc -l < "${CALLS}/curl.log" | tr -d ' ')" = 1 ] && ok "binaries: ... the report is empty and nothing is downloaded" || fail "binaries: a missing release is not handled"
reset_world; mkrelease ok; printf '302 https://github.com/myriarch/myriarch/releases' > "${STATE}/latest"
expect_ok "binaries: a repository whose latest page leads to the release list has no stable release" binaries
report_query 'd["runs"][0]["results"] == []' && ok "binaries: ... the report is empty" || fail "binaries: the release list is not handled"
reset_world; mkrelease ok; printf '302 https://example.test/elsewhere' > "${STATE}/latest"
expect_fail "binaries: a latest release that leads elsewhere is refused" 'ERROR \[release_lookup\]' binaries
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
