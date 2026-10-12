#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # SC2015: "A && ok || fail" is intentional (ok/fail never fail); SC2016: literal patterns in single quotes
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests the retry policy of the downloads in deploy/scripts/install-linux.sh and ci/release/smoke.sh against a local HTTP server
# that answers scripted faults (ci/tests/http-fault-server.py, 127.0.0.1, ephemeral port, started and stopped here). The
# PowerShell half (install-windows.ps1, ci/release/smoke-windows.ps1) is ci/tests/download-retry.ps1; run-all.sh calls it in
# the group pwsh.
#
# The policy (the same in all four scripts): a transient failure (a timeout, a failed connection, HTTP 408, 429, 500, 502, 503,
# 504) is retried with pauses of 1, 2, 4, ... s, no attempt starts later than 120 s after the first one; every other failure (a
# 404 for example) ends the download at once; the final message names the number of attempts.
#
# How the tests reach the server and keep the runtime small:
#  - The functions download() and fetch() are loaded from the real scripts (the installer without its last line "main", smoke.sh
#    without everything from its "# ---- main ---" marker) into a small harness. A curl shim in front of PATH logs its arguments,
#    sends https://github.com/ to the server, drops the https-only flags (the server speaks plain http) and runs the real curl.
#  - Two more instances of the server play an HTTPS proxy that answers every CONNECT with 503 or 407 (the cases X1 to X4: the
#    status of the proxy decides, because curl reports the status 000 of the server). Those cases keep the https URL and set
#    HTTPS_PROXY; no tunnel is ever opened.
#  - Most cases run on a fake clock: the harness replaces sleep by a function that advances $SECONDS, so the 120 s window takes no
#    wall time. The cases L9 (the full installer) and S5 (fetch) run on the real clock.
#
#   ci/tests/download-retry-test.sh          exit 0 = all cases as expected
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
cd "${ROOT}" || exit 1
need_cmd curl python3 tar gzip awk sed

INSTALLER="${ROOT}/deploy/scripts/install-linux.sh"
SMOKE="${ROOT}/ci/release/smoke.sh"
SERVER="${CI_DIR}/tests/http-fault-server.py"
W="$(cd "${TEST_TMP}" && pwd)"
REAL_CURL="$(command -v curl)"
export REAL_CURL

assert() { local name="$1"; shift; if "$@"; then ok "${name}"; else fail "${name}"; fi; }
eq() { [ "$1" = "$2" ]; }
between() { [ "$1" -ge "$2" ] 2>/dev/null && [ "$1" -le "$3" ] 2>/dev/null; } # value min max

# ---- the payload, the rules and the server ----------------------------------------------------------------------------------
ROOT_DIR="${W}/served"
mkdir -p "${ROOT_DIR}/myriarch/releases/download/v9.9.9"
printf 'payload of the retry tests\n' > "${ROOT_DIR}/payload.bin"
cp "${ROOT_DIR}/payload.bin" "${ROOT_DIR}/myriarch/releases/download/v9.9.9/payload.bin"

# L9: the fake mirror of installer-verify.sh (collector and supervisor), served under /l9/
M="${ROOT_DIR}"
mkdir -p "${M}/v0.161.0" "${M}/cmd/opampsupervisor/v0.161.0" "${W}/stage"
printf '#!/bin/sh\necho "otelcol-contrib version 0.161.0"\n' > "${W}/stage/otelcol-contrib"; chmod 0755 "${W}/stage/otelcol-contrib"
tar -czf "${M}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" -C "${W}/stage" otelcol-contrib
printf '#!/bin/sh\necho supervisor\n' > "${M}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"
for f in "${M}/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz" "${M}/cmd/opampsupervisor/v0.161.0/opampsupervisor_0.161.0_linux_amd64"; do
  sha256_of "${f}" | tr -d '\n' > "${f}.sha256"
done
L9_SHA="/l9/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz.sha256"
SMOKE_FILE="myriarch/releases/download/v9.9.9/payload.bin"

cat > "${W}/rules.json" <<RULES
{
  "/l1/payload.bin": [504, 504, 504, 504],
  "/l2/payload.bin": [408, 429, 500, 502, 503],
  "/l3/payload.bin": 504,
  "/l5a/payload.bin": 403,
  "/l5b/payload.bin": 501,
  "/l9/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz.sha256": [504, 504, 504, 504],
  "/s1/${SMOKE_FILE}": [504, 504, 504, 504],
  "/s2/${SMOKE_FILE}": 504,
  "/s3/${SMOKE_FILE}": 404,
  "/s5/${SMOKE_FILE}": [502, 503]
}
RULES
printf '{"/connect": 503}\n' > "${W}/rules-proxy503.json"
printf '{"/connect": 407}\n' > "${W}/rules-proxy407.json"
SRV_PIDS=()
stop_server() { local p; for p in ${SRV_PIDS[@]+"${SRV_PIDS[@]}"}; do kill "${p}" 2>/dev/null; wait "${p}" 2>/dev/null; done; cleanup_tests; }
trap stop_server EXIT
# start_server NAME RULES-FILE: starts an instance with the log ${W}/NAME.log and sets the variable PORT_OF_NAME (NAME in capitals)
start_server() {
  local name="$1" rules="$2" pf="${W}/$1.port" var
  : > "${W}/$1.log"
  python3 -I "${SERVER}" --rules "${rules}" --log "${W}/$1.log" --port-file "${pf}" --root "${ROOT_DIR}" > "${W}/$1.out" 2>&1 &
  SRV_PIDS+=("$!")
  for _ in $(seq 1 100); do [ -s "${pf}" ] && break; sleep 0.1; done
  [ -s "${pf}" ] || { echo "the fault server ${name} did not start: $(cat "${W}/$1.out")" >&2; exit 1; }
  var="PORT_OF_$(printf '%s' "${name}" | tr '[:lower:]' '[:upper:]')"
  printf -v "${var}" '%s' "$(cat "${pf}")"
}
start_server server "${W}/rules.json"
start_server proxy503 "${W}/rules-proxy503.json"
start_server proxy407 "${W}/rules-proxy407.json"
LOG="${W}/server.log"
PORT="${PORT_OF_SERVER}"
# a port that nothing listens on: bind an ephemeral port and close it again
CLOSED_PORT="$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"

reqs() { awk -v p="$1" '$1 == p { n++ } END { print n + 0 }' "${LOG}"; } # requests to a path
statuses() { awk -v p="$1" '$1 == p { printf "%s ", $2 }' "${LOG}"; }

# ---- the curl shim and the harnesses ---------------------------------------------------------------------------------------
SHIM="${W}/shim"; mkdir -p "${SHIM}"
cat > "${SHIM}/curl" <<'EOF2'
#!/usr/bin/env bash
# test double: logs its arguments (one per line, "----" after each call), sends https://github.com/ to the fault server and
# drops the https-only flags (the server speaks plain http), then runs the real curl
{ for a in "$@"; do printf '%s\n' "$a"; done; printf -- '----\n'; } >> "${SHIM_LOG:-/dev/null}"
args=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --proto | --proto-redir) shift 2 ;;
    --tlsv1.2) shift ;;
    https://github.com/*) if [ -n "${SHIM_KEEP_HTTPS:-}" ]; then args+=("$1"); else args+=("http://127.0.0.1:${FAULT_PORT}/${1#https://github.com/}"); fi; shift ;;
    *) args+=("$1"); shift ;;
  esac
done
exec "${REAL_CURL}" "${args[@]}"
EOF2
chmod 0755 "${SHIM}/curl"

# Under `bash -c` BASH_SOURCE is empty and smoke.sh computes ROOT from it, so the harnesses are files.
cat > "${W}/harness-install.sh" <<'EOF2'
#!/usr/bin/env bash
# usage: harness-install.sh INSTALLER INSECURE URL DEST [fake]: runs download() of the installer
inst="$1" insecure="$2" url="$3" dest="$4" fake="${5:-}"
set --
eval "$(sed -e '/^main$/d' "${inst}")"
INSECURE="${insecure}"
declare -F download >/dev/null || { echo "download() is not defined" >&2; exit 3; }
if [ -n "${fake}" ]; then sleep() { SECONDS=$((SECONDS + $1)); }; fi
trap 'echo "elapsed=${SECONDS}"' EXIT
download "${url}" "${dest}"
EOF2
cat > "${W}/harness-smoke.sh" <<'EOF2'
#!/usr/bin/env bash
# usage: harness-smoke.sh SMOKE CASE NAME DEST [fake]: runs fetch() of smoke.sh for https://github.com/<CASE>/myriarch/...
smoke="$1" case_label="$2" name="$3" dest="$4" fake="${5:-}"
set --
eval "$(sed -e '/^# ---- main ---/,$d' "${smoke}")"
ID_REPO="${case_label}/myriarch"
TAG=v9.9.9
declare -F fetch >/dev/null || { echo "fetch() is not defined" >&2; exit 3; }
if [ -n "${fake}" ]; then sleep() { SECONDS=$((SECONDS + $1)); }; fi
trap 'echo "elapsed=${SECONDS}"' EXIT
fetch "${name}" "${dest}"
EOF2

assert "the installer ends with exactly one line 'main' (the harness cuts it)" eq "$(grep -cx 'main' "${INSTALLER}")" 1
assert "smoke.sh has the marker '# ---- main ---' (the harness cuts from it)" eq "$(grep -c '^# ---- main ---' "${SMOKE}")" 1

# run_case command...: runs a command with the shim in PATH, sets OUT, RC and ELAPSED. The proxy variables of the environment are
# removed; with PROXY_URL set the command runs behind HTTPS_PROXY=$PROXY_URL and the shim leaves the https URL alone.
run_case() {
  : > "${W}/shim.log"
  OUT="$(
    unset http_proxy https_proxy all_proxy no_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY
    if [ -n "${PROXY_URL:-}" ]; then export HTTPS_PROXY="${PROXY_URL}" SHIM_KEEP_HTTPS=1; fi
    PATH="${SHIM}:${PATH}" SHIM_LOG="${W}/shim.log" FAULT_PORT="${FAULT_PORT:-${PORT}}" "$@" 2>&1
  )"; RC=$?
  ELAPSED="$(printf '%s\n' "${OUT}" | sed -n 's/^elapsed=//p' | tail -n 1)"
  return 0
}
dl()  { run_case bash "${W}/harness-install.sh" "${INSTALLER}" "$1" "$2" "${W}/out-$3.bin" "${4-fake}"; } # insecure url label [fake|'']
fe()  { run_case bash "${W}/harness-smoke.sh" "${SMOKE}" "$1" payload.bin "${W}/fetch-$1" "${2-fake}"; } # case [fake|'']
mkdir -p "${W}/fetch-s1" "${W}/fetch-s2" "${W}/fetch-s3" "${W}/fetch-s4" "${W}/fetch-s5"
has() { printf '%s' "${OUT}" | grep -Eq -- "$1"; }
GH=https://github.com

printf '\n== install-linux.sh download() ==\n'
# L1: four 504, then 200: the pauses 1+2+4+8 s, five requests (the old policy made four requests and gave up)
dl 0 "${GH}/l1/payload.bin" l1
assert "L1 504 four times, then 200: success" eq "${RC}" 0
assert "L1 five requests" eq "$(reqs /l1/payload.bin)" 5
assert "L1 the payload arrived intact" cmp -s "${ROOT_DIR}/payload.bin" "${W}/out-l1.bin"
assert "L1 the pauses add up to 15 s" between "${ELAPSED}" 15 17
cp "${W}/shim.log" "${W}/shim-l1.log"
# L2: every transient status is retried
dl 0 "${GH}/l2/payload.bin" l2
assert "L2 408 429 500 502 503, then 200: success" eq "${RC}" 0
assert "L2 six requests" eq "$(reqs /l2/payload.bin)" 6
# L3: the budget and the attempt count
dl 0 "${GH}/l3/payload.bin" l3
assert "L3 always 504: [download_failed]" has '\[download_failed\]'
assert "L3 the message names the status, the window and the attempts" has 'download failed: https://github\.com/l3/payload\.bin \(HTTP 504; retry window of 120 s used up, 8 attempts\)'
assert "L3 exactly eight requests" eq "$(reqs /l3/payload.bin)" 8
assert "L3 the window is used up, not exceeded" between "${ELAPSED}" 120 122
# L4/L5: permanent errors end at once
dl 0 "${GH}/l4/missing.bin" l4
assert "L4 404: [download_failed], not retried, one attempt" has '\[download_failed\].*\(HTTP 404; not retried, 1 attempt\)'
assert "L4 one request" eq "$(reqs /l4/missing.bin)" 1
assert "L4 no pause" between "${ELAPSED}" 0 1
dl 0 "${GH}/l5a/payload.bin" l5a
assert "L5 403: not retried" has '\(HTTP 403; not retried, 1 attempt\)'
assert "L5 403: one request" eq "$(reqs /l5a/payload.bin)" 1
dl 0 "${GH}/l5b/payload.bin" l5b
assert "L5 501: not retried" has '\(HTTP 501; not retried, 1 attempt\)'
assert "L5 501: one request" eq "$(reqs /l5b/payload.bin)" 1
# L6: a refused connection is transient (the old policy gave up after one attempt)
FAULT_PORT="${CLOSED_PORT}" dl 0 "${GH}/l6/payload.bin" l6
assert "L6 refused connection: retried for the whole window" has '\(curl exit code 7; retry window of 120 s used up, 8 attempts\)'
assert "L6 the window is used up, not exceeded" between "${ELAPSED}" 120 122
# L7: the flags. The loop is the only retry layer, and the transport restrictions are unchanged.
calls="$(grep -c -x -- '----' "${W}/shim-l1.log")"
flag_calls() { awk -v f="$1" '$0 == f { n++ } END { print n + 0 }' "${W}/shim-l1.log"; }
pair_calls() { awk -v f="$1" -v v="$2" '$0 == f { getline x; if (x == v) n++ } END { print n + 0 }' "${W}/shim-l1.log"; }
assert "L7 five curl calls for five attempts" eq "${calls}" 5
assert "L7 every call has --fail" eq "$(flag_calls --fail)" 5
assert "L7 every call has --proto =https" eq "$(pair_calls --proto =https)" 5
assert "L7 every call has --proto-redir =https" eq "$(pair_calls --proto-redir =https)" 5
assert "L7 every call has --tlsv1.2" eq "$(flag_calls --tlsv1.2)" 5
assert "L7 every call has --connect-timeout 20" eq "$(pair_calls --connect-timeout 20)" 5
assert "L7 every call has --max-time 900" eq "$(pair_calls --max-time 900)" 5
assert "L7 every call reads the statuses with --write-out" eq "$(pair_calls --write-out '%{http_code} %{http_connect}')" 5
assert "L7 no curl-internal retry option" eq "$(grep -c -E '^--retry' "${W}/shim-l1.log")" 0
# L8: a missing local file (lab mode) is permanent
dl 1 "file://${W}/does-not-exist" l8
assert "L8 file:// not found: [download_failed], not retried, one attempt" has '\[download_failed\].*not retried, 1 attempt\)'
assert "L8 no pause" between "${ELAPSED}" 0 1

# X1/X2: an HTTPS proxy that refuses CONNECT. curl reports the status 000 of the server (exit 22 on curl 8.x, 56 on curl 7.x);
# the status of the proxy decides.
printf '\n== a proxy that refuses CONNECT ==\n'
preqs() { grep -c -x '/connect [0-9]*' "${W}/$1.log"; }
PROXY_URL="http://127.0.0.1:${PORT_OF_PROXY503}" dl 0 "${GH}/x1/payload.bin" x1
assert "X1 install-linux.sh: CONNECT 503 is retried for the whole window" has '\[download_failed\].*\(proxy HTTP 503; retry window of 120 s used up, 8 attempts\)'
assert "X1 install-linux.sh: eight CONNECT requests" eq "$(preqs proxy503)" 8
assert "X1 install-linux.sh: the window is used up, not exceeded" between "${ELAPSED}" 120 122
PROXY_URL="http://127.0.0.1:${PORT_OF_PROXY407}" dl 0 "${GH}/x2/payload.bin" x2
assert "X2 install-linux.sh: CONNECT 407 is not retried" has '\[download_failed\].*\(proxy HTTP 407; not retried, 1 attempt\)'
assert "X2 install-linux.sh: one CONNECT request" eq "$(preqs proxy407)" 1
assert "X2 install-linux.sh: no pause" between "${ELAPSED}" 0 1

# X5: the classification itself, for the exit codes of curl 7.x too (a proxy refusal is exit 56 there). "rc status" pairs.
transient() { # script rc status: prints yes or no (only the function download_transient is loaded)
  bash -c 'eval "$(awk "\$0 == \"download_transient() {\" { p = 1 } p { print } p && \$0 == \"}\" { exit }" "$1")"; if download_transient "$2" "$3"; then echo yes; else echo no; fi' _ "$1" "$2" "$3"
}
for pair in "7 000 yes" "28 000 yes" "56 000 yes" "56 503 yes" "56 407 no" "56 404 no" "22 503 yes" "22 407 no" "22 404 no" "22 000 no" "35 000 no" "60 000 no"; do
  read -r x_rc x_status x_want <<< "${pair}"
  for script in "${INSTALLER}" "${SMOKE}"; do
    assert "X5 $(basename "${script}"): exit ${x_rc}, status ${x_status}: transient=${x_want}" eq "$(transient "${script}" "${x_rc}" "${x_status}")" "${x_want}"
  done
done

# L9: the whole installer on the real clock, the failure of the release run of v0.1.0-rc.3 (four 504 on a .sha256 file in a row)
printf '\n== install-linux.sh as a whole (real clock, about 15 s) ==\n'
root9="${W}/root9"; mkdir -p "${root9}"
OUT9="$(bash "${INSTALLER}" --root "${root9}" --arch amd64 --templates-dir "${ROOT}/deploy" --insecure --verify-signatures skip \
  --release-base-url "http://127.0.0.1:${PORT}/l9" --opamp-endpoint wss://fleet.example.com/v1/opamp --token myre_abcDEF123 \
  --site-type office --site-id OFFICE1 --collector-version 0.161.0 2>&1)"; RC9=$?
assert "L9 four 504 on the .sha256 file do not stop the installation" eq "${RC9}" 0
[ "${RC9}" = 0 ] || printf '%s\n' "${OUT9}" | sed 's/^/        /' >&2
assert "L9 the collector is installed" test -e "${root9}/opt/myriarch/collector/versions/0.161.0/otelcol-contrib"
assert "L9 four retries are logged" eq "$(printf '%s\n' "${OUT9}" | grep -c 'next attempt in')" 4
assert "L9 the server saw four 504 and one 200" eq "$(statuses "${L9_SHA}")" "504 504 504 504 200 "

printf '\n== ci/release/smoke.sh fetch() ==\n'
# S1: four 504, then 200
fe s1
assert "S1 504 four times, then 200: success" eq "${RC}" 0
assert "S1 five requests" eq "$(reqs "/s1/${SMOKE_FILE}")" 5
assert "S1 the file arrived intact" cmp -s "${ROOT_DIR}/payload.bin" "${W}/fetch-s1/payload.bin"
cp "${W}/shim.log" "${W}/shim-s1.log"
# S2: always 504
fe s2
assert "S2 always 504: [download_failed] with the status, the window and the attempts" has "\[download_failed\] download of https://github\.com/s2/myriarch/releases/download/v9\.9\.9/payload\.bin failed \(HTTP 504; retry window of 120 s used up, 8 attempts\)"
assert "S2 exactly eight requests" eq "$(reqs "/s2/${SMOKE_FILE}")" 8
assert "S2 the window is used up, not exceeded" between "${ELAPSED}" 120 122
# S3: 404 ends at once
fe s3
assert "S3 404: [download_failed], not retried, one attempt" has '\[download_failed\].*\(HTTP 404; not retried, 1 attempt\)'
assert "S3 one request" eq "$(reqs "/s3/${SMOKE_FILE}")" 1
# S4: the flags of every call
sflag() { awk -v f="$1" '$0 == f { n++ } END { print n + 0 }' "${W}/shim-s1.log"; }
spair() { awk -v f="$1" -v v="$2" '$0 == f { getline x; if (x == v) n++ } END { print n + 0 }' "${W}/shim-s1.log"; }
assert "S4 five curl calls for five attempts" eq "$(grep -c -x -- '----' "${W}/shim-s1.log")" 5
assert "S4 -q is the first argument of every call" eq "$(awk 'prev == "----" || NR == 1 { if ($0 == "-q") n++ } { prev = $0 } END { print n + 0 }' "${W}/shim-s1.log")" 5
assert "S4 --proto =https and --proto-redir =https in every call" eq "$(( $(spair --proto =https) + $(spair --proto-redir =https) ))" 10
assert "S4 --tlsv1.2 and -fsSL in every call" eq "$(( $(sflag --tlsv1.2) + $(sflag -fsSL) ))" 10
assert "S4 the existing timeouts" eq "$(( $(spair --connect-timeout 20) + $(spair --max-time 900) ))" 10
assert "S4 the statuses are read with -w" eq "$(spair -w '%{http_code} %{http_connect}')" 5
assert "S4 no curl-internal retry option" eq "$(grep -c -E '^--retry' "${W}/shim-s1.log")" 0
# S5: the real sleep path (502, 503, then 200: pauses of 1 and 2 s)
start=$SECONDS
fe s5 ""
wall=$((SECONDS - start))
assert "S5 real clock: 502, 503, then 200: success" eq "${RC}" 0
assert "S5 three requests" eq "$(reqs "/s5/${SMOKE_FILE}")" 3
assert "S5 the pauses were real (3 s)" between "${wall}" 3 10

# X3/X4: the same for smoke.sh fetch()
mkdir -p "${W}/fetch-x3" "${W}/fetch-x4"
: > "${W}/proxy503.log"; : > "${W}/proxy407.log"
PROXY_URL="http://127.0.0.1:${PORT_OF_PROXY503}" fe x3
assert "X3 smoke.sh: CONNECT 503 is retried for the whole window" has '\[download_failed\].*\(proxy HTTP 503; retry window of 120 s used up, 8 attempts\)'
assert "X3 smoke.sh: eight CONNECT requests" eq "$(preqs proxy503)" 8
assert "X3 smoke.sh: the window is used up, not exceeded" between "${ELAPSED}" 120 122
PROXY_URL="http://127.0.0.1:${PORT_OF_PROXY407}" fe x4
assert "X4 smoke.sh: CONNECT 407 is not retried" has '\[download_failed\].*\(proxy HTTP 407; not retried, 1 attempt\)'
assert "X4 smoke.sh: one CONNECT request" eq "$(preqs proxy407)" 1
assert "X4 smoke.sh: no pause" between "${ELAPSED}" 0 1

printf '\n== the four scripts follow one policy ==\n'
WIN_INSTALLER="${ROOT}/deploy/scripts/install-windows.ps1"
WIN_SMOKE="${ROOT}/ci/release/smoke-windows.ps1"
hash_of() { sha256_of /dev/stdin; }
# fn_text FILE NAME: a function from 'name() {' (shell) or 'function Name {' (PowerShell) to the next line that is only '}'
fn_text() { awk -v a="$2() {" -v b="function $2 {" '$0 == a || $0 == b { p = 1 } p { print } p && $0 == "}" { exit }' "$1"; }
sh_set() { fn_text "$1" download_transient | grep -E '408' | tr -d ' \t'; }
assert "P1 download_transient() is identical in install-linux.sh and smoke.sh" eq "$(fn_text "${INSTALLER}" download_transient | hash_of)" "$(fn_text "${SMOKE}" download_transient | hash_of)"
assert "P1 download_transient() lists 408 429 500 502 503 504" eq "$(sh_set "${INSTALLER}")" '22|56)case"$2"in408|429|500|502|503|504)return0;;000)["$1"=56]&&return0;;esac;;'
assert "P1 download_transient() exists" test -n "$(fn_text "${INSTALLER}" download_transient)"
assert "P1 the window is 120 s in both bash scripts" eq "$(grep -h '^readonly DOWNLOAD_RETRY_SECONDS=' "${INSTALLER}" "${SMOKE}" | sort -u)" 'readonly DOWNLOAD_RETRY_SECONDS=120'
assert "P1 the transient HTTP statuses of the PowerShell scripts equal those of the bash scripts" eq \
  "$(fn_text "${WIN_INSTALLER}" Test-TransientDownloadError | grep -E -o '408[0-9, ]*' | head -n 1 | tr -d ' ')" "408,429,500,502,503,504"
for f in "${WIN_INSTALLER}" "${WIN_SMOKE}"; do
  assert "P1 the window is 120 s in $(basename "${f}")" eq "$(grep -c -E '^  \$window = 120$' "${f}")" 1
done
for f in "${INSTALLER}" "${SMOKE}" "${WIN_INSTALLER}" "${WIN_SMOKE}"; do
  assert "P3 $(basename "${f}") is ASCII only" eq "$(LC_ALL=C grep -c '[^[:print:][:space:]]' "${f}")" 0
done
for fn in Get-MonotonicSeconds Get-DownloadStatus Get-DownloadReason Test-TransientDownloadError; do
  assert "P2 ${fn} is text-identical in install-windows.ps1 and smoke-windows.ps1" eq "$(fn_text "${WIN_INSTALLER}" "${fn}" | hash_of)" "$(fn_text "${WIN_SMOKE}" "${fn}" | hash_of)"
  assert "P2 ${fn} exists" test -n "$(fn_text "${WIN_INSTALLER}" "${fn}")"
done
# the code of install-windows.ps1 has to run on Windows PowerShell 5.1: nothing that only PowerShell 7 knows
ps7only='\?\?|\?\.|-SkipHttpErrorCheck|-MaximumRetryCount|-RetryIntervalSec|-StatusCodeVariable|-ConnectionTimeoutSeconds|-OperationTimeoutSeconds|-AllowInsecureRedirect|\[System\.Net\.Http\.'
win_dl="$(awk '/^# ---- Download retry policy/ { p = 1 } /^# Certificate identity/ { p = 0 } p' "${WIN_INSTALLER}")"
assert "P3 the download code of install-windows.ps1 uses no PowerShell 7 construct" eq "$(printf '%s\n' "${win_dl}" | grep -c -E -- "${ps7only}")" 0
assert "P3 the download code of install-windows.ps1 is not empty" test -n "${win_dl}"
for f in "${INSTALLER}" "${SMOKE}"; do
  assert "P4 no curl --retry option (outside comments) in $(basename "${f}")" eq "$(grep -v '^[[:space:]]*#' "${f}" | grep -c -E -- '--retry')" 0
done

finish_tests "download-retry-test.sh"
