#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test for the stamps of ci/install-tools.sh. A Go tool counts as installed only when its binary and a stamp exist, and the name
# of the stamp holds the tool, the version and a digest of ci/tools/<tool>/go.mod and go.sum. A tool cache that
# .github/actions/setup restores under an older key can hold the binary of the same version built from older requirements, and
# the digest is what makes such a tool rebuild. The installer runs from a copy in a scratch tree, next to a stand-in for `go`:
# no network and no real build.
#
# And the retries of the installer: a Go tool is built again (4 attempts) when `go build` fails with a network error of the module
# download and never when its output names a checksum mismatch or anything else; the download of kubectl (and with it every
# download of the script, they share fetch_file) is retried after a timeout, a refused connection or HTTP 408, 429, 5xx, with the
# window of 120 s, and not after a 404 or a checksum that differs from the pin. The downloads run against the scripted HTTP faults of
# ci/tests/http-fault-server.py on 127.0.0.1.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

repo="${TEST_TMP}/repo"; tools="${TEST_TMP}/tools"; stub="${TEST_TMP}/stub"; calls="${TEST_TMP}/go-calls"
mkdir -p "${repo}/ci/tools/helm" "${repo}/ci/tools/go-licenses" "${tools}" "${stub}"
cp "${CI_DIR}/install-tools.sh" "${CI_DIR}/lib.sh" "${CI_DIR}/versions.env" "${repo}/ci/"
printf 'module example.test/root\n\ngo 1.27.2\n' > "${repo}/go.mod"   # go-licenses is built with the toolchain of this module
for t in helm go-licenses; do
  printf 'module example.test/ci/tools/%s\n\ngo 1.26.0\n' "${t}" > "${repo}/ci/tools/${t}/go.mod"
  printf 'example.test/dependency v1.0.0 h1:AAAA\nexample.test/dependency v1.0.0/go.mod h1:BBBB\n' > "${repo}/ci/tools/${t}/go.sum"
done
: > "${calls}"
cat > "${stub}/go" <<'STUB'
#!/bin/sh
# Stand-in for go: records the call, then writes the file that `go build -o <file>` would write
echo "go $* [GOTOOLCHAIN=${GOTOOLCHAIN:-} GOFLAGS=${GOFLAGS:-} dir=$(basename "$(pwd)")]" >> "${FAKE_GO_CALLS}"
# FAKE_GO_FAILURES: a file with one line per call; a line other than "ok" is the output of that failed call (exit status 1)
if [ -n "${FAKE_GO_FAILURES:-}" ] && [ -s "${FAKE_GO_FAILURES}" ]; then
  first="$(head -n 1 "${FAKE_GO_FAILURES}")"
  tail -n +2 "${FAKE_GO_FAILURES}" > "${FAKE_GO_FAILURES}.next"; mv "${FAKE_GO_FAILURES}.next" "${FAKE_GO_FAILURES}"
  if [ "${first}" != ok ]; then printf '%s\n' "${first}" >&2; exit 1; fi
fi
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; shift; fi
  shift
done
[ -n "${out}" ] || { echo "stand-in for go: no -o" >&2; exit 2; }
printf '#!/bin/sh\necho stand-in\n' > "${out}"
chmod +x "${out}"
STUB
chmod +x "${stub}/go"

installer() { # tool... - the copied installer with the stand-in for go; an exported TOOLS_NO_CACHE does not leak in
  ( unset TOOLS_NO_CACHE; PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" FAKE_GO_CALLS="${calls}" "${repo}/ci/install-tools.sh" "$@" )
}
installer_no_cache() { # tool...
  ( TOOLS_NO_CACHE=1 PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" FAKE_GO_CALLS="${calls}" "${repo}/ci/install-tools.sh" "$@" )
}
go_calls() { wc -l < "${calls}" | tr -d ' '; }
stamps() { # tool - the names of its stamp files, one per line
  local f
  for f in "${tools}"/.stamp-"$1"-*; do
    if [ -e "${f}" ]; then basename "${f}"; fi
  done
}
digest() { # tool - what the stamp must hold, computed here and not with ci/lib.sh: 12 hex digits of SHA-256(go.mod + go.sum)
  local d
  cat "${repo}/ci/tools/$1/go.mod" "${repo}/ci/tools/$1/go.sum" > "${TEST_TMP}/module-files"
  d="$(sha256_of "${TEST_TMP}/module-files")"
  printf '%s' "${d:0:12}"
}
expect_calls() { # count what - the number of go builds so far
  [ "$(go_calls)" = "$1" ] && ok "$2" || fail "$2 (expected $1 go builds so far, saw $(go_calls))"
}
expect_stamps() { # tool stamp - the tool has exactly this one stamp
  [ "$(stamps "$1")" = "$2" ] && ok "stamp of $1: $2" || fail "stamp of $1 is '$(stamps "$1" | tr '\n' ' ')', expected '$2'"
}

# ---- a first install and a hit of the cache ---------------------------------------------------------------------------------------
expect_ok "a first install builds the tool" installer helm
expect_calls 1 "... with one go build"
grep -q "build .*-o ${tools}/helm .*\[GOTOOLCHAIN=local GOFLAGS=-mod=readonly dir=helm\]" "${calls}" \
  && ok "... in ci/tools/helm with -mod=readonly" || fail "the go build did not run as expected: $(cat "${calls}")"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
expect_ok "a second install finds the tool" installer helm
case "${OUT}" in *"from the cache"*) ok "... in the cache" ;; *) fail "the second install did not report the cache: ${OUT}" ;; esac
expect_calls 1 "... without another go build"

# ---- a change of a module file rebuilds the tool -----------------------------------------------------------------------------------
printf 'example.test/other v1.0.0 h1:CCCC\n' >> "${repo}/ci/tools/helm/go.sum"
expect_ok "a changed go.sum" installer helm
expect_calls 2 "... rebuilds the tool"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
printf '\nrequire example.test/dependency v1.0.1 // indirect\n' >> "${repo}/ci/tools/helm/go.mod"
expect_ok "a changed go.mod" installer helm
expect_calls 3 "... rebuilds the tool"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"

# ---- the stamp of a tool cache from before the digest, TOOLS_NO_CACHE and other tools --------------------------------------------
rm -f "${tools}"/.stamp-helm-*
: > "${tools}/.stamp-helm-${HELM_VERSION}"
expect_ok "a stamp without the digest (a tool cache from before the digest)" installer helm
expect_calls 4 "... does not count: the tool is rebuilt"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
expect_ok "TOOLS_NO_CACHE=1 with a matching stamp" installer_no_cache helm
expect_calls 5 "... rebuilds the tool"
expect_ok "another tool, whose go is chosen by the main module" installer go-licenses
expect_calls 6 "... is built"
expect_stamps go-licenses ".stamp-go-licenses-${GOLICENSES_VERSION}-$(digest go-licenses)-go1.27.2"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
tail -n 1 "${calls}" | grep -q "GOTOOLCHAIN=go1.27.2 .*dir=go-licenses" && ok "... with the toolchain of the main module" || fail "go-licenses was not built with go1.27.2: $(tail -n 1 "${calls}")"

# ---- a module file that is missing --------------------------------------------------------------------------------------------------
mv "${repo}/ci/tools/helm/go.sum" "${TEST_TMP}/go.sum.aside"
expect_fail "a missing go.sum, although binary and stamp exist" "ci/tools/helm/go.mod or go.sum is missing" installer helm
expect_calls 6 "... starts no build"
mv "${TEST_TMP}/go.sum.aside" "${repo}/ci/tools/helm/go.sum"
expect_ok "go.sum is back" installer helm
expect_calls 6 "... and the tool is still in the cache"
# ---- Go tools: a network error of the module download is retried, nothing else ----------------------------------------------------
failures="${TEST_TMP}/go-failures"
installer_failing() { # tool... - the scripted failures of ${failures}; no pause between the attempts
  ( unset TOOLS_NO_CACHE; PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" FAKE_GO_CALLS="${calls}" FAKE_GO_FAILURES="${failures}" TOOLS_RETRY_PAUSE=0 \
      "${repo}/ci/install-tools.sh" "$@" )
}
fresh_tools() { rm -rf "${tools:?}"; mkdir -p "${tools}"; : > "${calls}"; }
TIMEOUT_ERR='go: example.test/dependency@v1.0.0: Get "https://proxy.golang.org/example.test/dependency/@v/v1.0.0.mod": dial tcp 203.0.113.10:443: i/o timeout'

fresh_tools; printf '%s\nok\n' "${TIMEOUT_ERR}" > "${failures}"
expect_ok "a go build that fails once with a timeout is tried again" installer_failing helm
expect_calls 2 "... with two go builds"
case "${OUT}" in *"failed with a network error (attempt 1 of 4)"*) ok "... and says so" ;; *) fail "no message about the retry: ${OUT}" ;; esac
[ -x "${tools}/helm" ] && ok "... and the tool is installed" || fail "helm was not installed after the second attempt"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"

fresh_tools
printf '%s\n%s\n%s\nok\n' "${TIMEOUT_ERR}" 'go: reading https://proxy.golang.org/example.test/dependency/@v/v1.0.0.info: 503 Service Unavailable' \
  'read tcp 192.0.2.2:5555->203.0.113.10:443: read: connection reset by peer' > "${failures}"
expect_ok "three network errors in a row and then a success is the limit" installer_failing helm
expect_calls 4 "... four go builds"

fresh_tools; printf '%s\n%s\n%s\n%s\nok\n' "${TIMEOUT_ERR}" "${TIMEOUT_ERR}" "${TIMEOUT_ERR}" "${TIMEOUT_ERR}" > "${failures}"
expect_fail "a fourth network error in a row ends the installation" "go build helm.sh/helm/v4/cmd/helm .*failed" installer_failing helm
expect_calls 4 "... after four attempts"
[ ! -x "${tools}/helm" ] && ok "... and installs nothing" || fail "helm exists after four failed attempts"

# these outputs are never retried: a failed verification (also when a timeout is named as well), a missing version, a compile error
for case_text in \
  'SECURITY ERROR: verifying example.test/dependency@v1.0.0: checksum mismatch' \
  'verifying module: example.test/dependency@v1.0.0: Get "https://sum.golang.org/lookup": i/o timeout; SECURITY ERROR: checksum mismatch' \
  'go: example.test/dependency@v9.9.9: reading https://proxy.golang.org/example.test/dependency/@v/v9.9.9.info: 404 Not Found' \
  './main.go:12:2: undefined: foo' \
  'go: updates to go.mod needed; to update it: go mod tidy'; do
  fresh_tools; printf '%s\nok\n' "${case_text}" > "${failures}"
  expect_fail "not tried again: $(printf '%s' "${case_text}" | cut -c1-60)" "go build helm.sh/helm/v4/cmd/helm .*failed" installer_failing helm
  expect_calls 1 "... exactly one go build"
done

# ---- kubectl: the download that all release downloads of the script share (fetch_file) -------------------------------------------
case "$(uname -s)" in Darwin) k_os=darwin ;; *) k_os=linux ;; esac
case "$(uname -m)" in arm64 | aarch64) k_arch=arm64 ;; *) k_arch=amd64 ;; esac
key="KUBECTL_SHA256_$(printf '%s' "${k_os}" | tr '[:lower:]' '[:upper:]')_$(printf '%s' "${k_arch}" | tr '[:lower:]' '[:upper:]')"
served="${TEST_TMP}/served"; kpath="${KUBECTL_VERSION}/bin/${k_os}/${k_arch}/kubectl"
mkdir -p "${served}/$(dirname "${kpath}")"
printf '#!/bin/sh\necho "kubectl stand-in"\n' > "${served}/${kpath}"
ksha="$(sha256_of "${served}/${kpath}")"
set_pin() { # sha - the pin in the copied versions.env
  python3 -I -c '
import re, sys
path, key, sha = sys.argv[1:4]
text = open(path).read()
text, n = re.subn(r"^" + re.escape(key) + r"=.*$", key + "=" + sha, text, flags=re.M)
assert n == 1, key
open(path, "w").write(text)
' "${repo}/ci/versions.env" "${key}" "$1"
}
set_pin "${ksha}"
cat > "${TEST_TMP}/rules.json" <<RULES
{
  "/k1/${kpath}": [503, 503],
  "/k2/${kpath}": 404,
  "/k3/${kpath}": 503,
  "/k4/${kpath}": [429, 408],
  "/k5/${kpath}": [500],
  "/k6/${kpath}": 403
}
RULES
srv_log="${TEST_TMP}/fault.log"; : > "${srv_log}"
python3 -I "${CI_DIR}/tests/http-fault-server.py" --rules "${TEST_TMP}/rules.json" --log "${srv_log}" --port-file "${TEST_TMP}/fault.port" --root "${served}" > "${TEST_TMP}/fault.out" 2>&1 &
SRV_PID=$!
trap 'kill "${SRV_PID}" 2>/dev/null; wait "${SRV_PID}" 2>/dev/null; cleanup_tests' EXIT
for _ in $(seq 1 100); do [ -s "${TEST_TMP}/fault.port" ] && break; sleep 0.1; done
FAULT_PORT="$(cat "${TEST_TMP}/fault.port" 2>/dev/null || true)"
CLOSED_PORT="$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
if [ -z "${FAULT_PORT}" ]; then
  fail "the fault server did not start: $(cat "${TEST_TMP}/fault.out")"
else
  kubectl_install() { # label [VAR=value...]
    local label="$1"; shift
    fresh_tools
    ( unset TOOLS_NO_CACHE; env -u "${key}" PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" KUBECTL_BASE_URL="http://127.0.0.1:${FAULT_PORT}/${label}" "$@" \
        "${repo}/ci/install-tools.sh" kubectl )
  }
  reqs() { awk -v p="/$1/${kpath}" '$1 == p { n++ } END { print n + 0 }' "${srv_log}"; }
  expect_ok "kubectl: two 503 answers and then the file" kubectl_install k1 TOOLS_RETRY_PAUSE=0
  [ "$(reqs k1)" = 3 ] && ok "... three requests" || fail "k1: $(reqs k1) requests"
  [ -x "${tools}/kubectl" ] && [ "$("${tools}/kubectl")" = "kubectl stand-in" ] && ok "... the file is installed" || fail "kubectl was not installed"
  case "${OUT}" in *"attempt 2 for"*"next attempt in 0 s"*) ok "... and the retries are logged" ;; *) fail "no log of the retries: ${OUT}" ;; esac
  expect_fail "kubectl: a 404 ends the download at once" "download of .*/k2/.* failed \(HTTP 404; not retried, 1 attempt" kubectl_install k2 TOOLS_RETRY_PAUSE=0
  [ "$(reqs k2)" = 1 ] && ok "... after one request" || fail "k2: $(reqs k2) requests"
  expect_fail "kubectl: a 403 is no transient error either" "HTTP 403; not retried" kubectl_install k6 TOOLS_RETRY_PAUSE=0
  [ "$(reqs k6)" = 1 ] && ok "... after one request" || fail "k6: $(reqs k6) requests"
  expect_fail "kubectl: a server that keeps answering 503 ends at the window" "HTTP 503; retry window of 3 s used up" kubectl_install k3 TOOLS_RETRY_PAUSE=1 TOOLS_RETRY_SECONDS=3
  n="$(reqs k3)"; [ "${n}" -ge 2 ] && [ "${n}" -le 4 ] && ok "... after ${n} requests" || fail "k3: ${n} requests"
  expect_ok "kubectl: 429 and 408 are retried" kubectl_install k4 TOOLS_RETRY_PAUSE=0
  [ "$(reqs k4)" = 3 ] && ok "... three requests" || fail "k4: $(reqs k4) requests"
  expect_ok "kubectl: a 500 is retried" kubectl_install k5 TOOLS_RETRY_PAUSE=0
  # a file that differs from the pin is no transient error: one request, no retry
  set_pin "$(printf '0%.0s' $(seq 1 64))"
  expect_fail "kubectl: a file that differs from the pin is refused without a retry" "SHA-256 mismatch" kubectl_install k7 TOOLS_RETRY_PAUSE=0
  [ "$(awk -v p="/k7/${kpath}" '$1 == p { n++ } END { print n + 0 }' "${srv_log}")" = 1 ] && ok "... after one request" || fail "k7: more than one request: $(cat "${srv_log}")"
  [ ! -x "${tools}/kubectl" ] && ok "... and installs nothing" || fail "kubectl was installed from a file that differs from the pin"
  set_pin "${ksha}"
  t_refused() { fresh_tools; ( unset TOOLS_NO_CACHE; env -u "${key}" PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" KUBECTL_BASE_URL="http://127.0.0.1:${CLOSED_PORT}/x" TOOLS_RETRY_PAUSE=1 TOOLS_RETRY_SECONDS=2 "${repo}/ci/install-tools.sh" kubectl ); }
  expect_fail "kubectl: a refused connection is retried until the window ends" "curl exit code 7; retry window of 2 s used up" t_refused
fi

finish_tests "install-tools-test"
