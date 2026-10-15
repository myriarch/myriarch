#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the harness of the host end-to-end test (scripts/host-e2e). The scenarios change the machine they run on and run on a
# throwaway runner of GitHub Actions only, so this test covers what can be proven without such a machine:
#   - the guard: no scenario, no sudo and no installation without CI=true, HOST_E2E=1, Linux, a user other than root, passwordless
#     sudo and a host without a trace of an earlier installation (the commands uname, id, sudo, pgrep, ss and systemctl are stand-ins
#     on PATH, so the same cases run on macOS and on Linux), that the one gate for host changes (he_sudo) checks it again and that
#     no script of the harness calls sudo anywhere else;
#   - the artifacts: the scrubber removes tokens, credentials, DSNs, cookies and private keys, the upload directory holds the
#     allow-listed files only (never the token file, the cookie jar, the keys, the database or the templates), and a secret in it
#     removes it;
#   - the pieces that need no host: the throwaway CA (SAN, chain, validity), the https file server (listing, traversal, methods, a
#     wrong CA), the update-wait tuning of the updater template, the GitOps fixture, the versions against ci/versions.env.
# The scenario itself is proven by the workflow .github/workflows/host-e2e.yml on a runner; the workflow shape and its artifact
# allow-list are checked by workflow-artifacts-test.sh. No network access, no containers; Go is needed for the fixture and the template
# check (they are skipped without it).
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
HE="${ROOT}/scripts/host-e2e"
LIB="${HE}/lib.sh"
export HOST_E2E_DIR="${TEST_TMP}/work"
W="${TEST_TMP}"

# ---- stand-ins for the commands the guard asks, first on PATH of the cases that use STUBS ------------------------------------
STUBS="${W}/stubs"
mkdir -p "${STUBS}"
cat > "${STUBS}/uname" <<'STUB'
#!/bin/sh
case "$1" in -s) echo "${FAKE_UNAME:-Linux}" ;; -m) echo x86_64 ;; *) echo "${FAKE_UNAME:-Linux}" ;; esac
STUB
cat > "${STUBS}/id" <<'STUB'
#!/bin/sh
# id -u: the user id of the fake user; id -u NAME: that user exists only when FAKE_USER_OTELCOL is set
if [ "$#" = 2 ]; then [ -n "${FAKE_USER_OTELCOL:-}" ] && [ "$2" = otelcol ] && { echo 998; exit 0; }; exit 1; fi
echo "${FAKE_UID:-1000}"
STUB
cat > "${STUBS}/sudo" <<'STUB'
#!/bin/sh
echo "$*" >> "${FAKE_SUDO_LOG:-/dev/null}"
[ -z "${FAKE_NO_SUDO:-}" ] || exit 1
[ "$1" != -n ] || shift
exec "$@"
STUB
cat > "${STUBS}/pgrep" <<'STUB'
#!/bin/sh
# pgrep -x NAME: a process only for the names in FAKE_PROCESSES (comm, cut to 15 characters)
for n in ${FAKE_PROCESSES:-}; do [ "$(printf '%.15s' "$n")" = "$2" ] && { echo 4242; exit 0; }; done
exit 1
STUB
cat > "${STUBS}/ss" <<'STUB'
#!/bin/sh
[ -z "${FAKE_LISTENER:-}" ] || printf 'LISTEN 0 4096 127.0.0.1:%s 0.0.0.0:*\n' "${FAKE_LISTENER}"
STUB
cat > "${STUBS}/systemctl" <<'STUB'
#!/bin/sh
# list-unit-files: a line for FAKE_UNIT, and with FAKE_UNIT_FOOTER the summary line that systemctl prints without --no-legend
[ -z "${FAKE_UNIT:-}" ] || printf '%s enabled enabled\n' "${FAKE_UNIT}"
[ -z "${FAKE_UNIT_FOOTER:-}" ] || printf '\n0 unit files listed.\n'
STUB
chmod +x "${STUBS}"/*

# a shell with the stand-ins in front, the library sourced and the given command run
in_lib() { # [VAR=value...] -- command...
  local envs=()
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  env ${envs[@]+"${envs[@]}"} PATH="${STUBS}:${PATH}" bash -c '. "$1"; shift; "$@"' _ "${LIB}" "$@"
}

# ---- the library has no side effect when it is sourced ---------------------------------------------------------------------------
OUT="$(env -u CI -u HOST_E2E bash -c ". '${LIB}'" 2>&1)"
[ -z "${OUT}" ] && [ ! -e "${HOST_E2E_DIR}" ] && ok "sourcing lib.sh prints nothing and creates nothing" || fail "sourcing lib.sh has side effects: '${OUT}'"
t_syntax_32() { /bin/bash -n "${LIB}" && /bin/bash -n "${HE}/linux.sh"; }
expect_ok "lib.sh and linux.sh are valid for the bash of the system" t_syntax_32

# ---- the guard of the entry point ------------------------------------------------------------------------------------------------
t_noargs() { env -u CI -u HOST_E2E bash "${HE}/linux.sh"; }
expect_fail "linux.sh without a scenario prints the usage and fails" "usage: scripts/host-e2e/linux.sh" t_noargs
t_help() { env -u CI -u HOST_E2E bash "${HE}/linux.sh" --help; }
expect_ok "linux.sh --help works anywhere" t_help
case "${OUT}" in *"scenarios: manual"*) ok "... and lists the scenarios" ;; *) fail "the usage does not list the scenario manual: ${OUT}" ;; esac
t_unknown() { env -u CI -u HOST_E2E bash "${HE}/linux.sh" nonsense; }
expect_fail "an unknown scenario is refused" "unknown scenario 'nonsense'" t_unknown
t_no_ci() { env -u CI -u HOST_E2E bash "${HE}/linux.sh" manual; }
expect_fail "linux.sh manual without CI=true refuses to run" "refusing to run: CI=true is not set" t_no_ci
t_no_he() { env -u HOST_E2E CI=true bash "${HE}/linux.sh" manual; }
expect_fail "linux.sh manual with CI=true but without HOST_E2E=1 refuses to run" "refusing to run: HOST_E2E=1 is not set" t_no_he
t_ci_false() { env CI=false HOST_E2E=1 bash "${HE}/linux.sh" manual; }
expect_fail "CI=false is not CI" "CI=true is not set" t_ci_false
[ ! -e "${HOST_E2E_DIR}" ] && ok "... and none of the refusals created the work directory" || fail "a refusal left ${HOST_E2E_DIR}"

# ---- the guard functions, with stand-ins for the commands -------------------------------------------------------------------------
t_other_os() { in_lib CI=true HOST_E2E=1 FAKE_UNAME=Darwin -- he_require_ci; }
expect_fail "another operating system is refused" "this is not Linux" t_other_os
t_root() { in_lib CI=true HOST_E2E=1 FAKE_UID=0 -- he_require_ci; }
expect_fail "root is refused" "refusing to run as root" t_root
t_ok_guard() { in_lib CI=true HOST_E2E=1 -- he_require_ci; }
expect_ok "CI=true, HOST_E2E=1, Linux and a normal user pass the guard" t_ok_guard
t_nosudo() { in_lib CI=true HOST_E2E=1 FAKE_NO_SUDO=1 -- he_require_sudo; }
expect_fail "a host without passwordless sudo is refused" "passwordless sudo is needed" t_nosudo
SUDO_LOG="${W}/sudo.log"; : > "${SUDO_LOG}"
expect_fail "he_sudo, the one gate for changes of the host, checks the guard itself" "CI=true is not set" env -u CI -u HOST_E2E PATH="${STUBS}:${PATH}" FAKE_SUDO_LOG="${SUDO_LOG}" bash -c ". '${LIB}'; he_sudo true"
[ ! -s "${SUDO_LOG}" ] && ok "... and sudo was not called" || fail "sudo was called without the guard: $(cat "${SUDO_LOG}")"
t_sudo_ci() { in_lib CI=true HOST_E2E=1 FAKE_SUDO_LOG="${SUDO_LOG}" -- he_sudo true; }
expect_ok "he_sudo runs the command through sudo -n when the guard passes" t_sudo_ci
grep -q '^-n true$' "${SUDO_LOG}" && ok "... without a password prompt (sudo -n)" || fail "sudo was not called with -n: $(cat "${SUDO_LOG}")"
# no script of the harness calls sudo except through the gate: the direct calls are the probe of he_require_sudo and he_sudo itself
SUDO_DIRECT="$(grep -hE '(^|[^[:alnum:]_-])sudo[[:space:]]+-n' "${HE}"/*.sh | grep -vE '^[[:space:]]*#' | sed 's/^[[:space:]]*//')"
SUDO_N=0; SUDO_BAD=""
while IFS= read -r line; do
  [ -n "${line}" ] || continue
  SUDO_N=$((SUDO_N + 1))
  case "${line}" in
    'sudo -n true 2>/dev/null || he_die '* | 'sudo -n "$@"') ;;
    *) SUDO_BAD="${SUDO_BAD} [${line}]" ;;
  esac
done <<< "${SUDO_DIRECT}"
[ "${SUDO_N}" = 2 ] && [ -z "${SUDO_BAD}" ] && ok "sudo is called in two places only: the probe of he_require_sudo and the gate he_sudo" \
  || fail "unexpected direct calls of sudo (${SUDO_N} lines):${SUDO_BAD}"

# a fresh host: no trace of an installation
FS="${W}/fs"
mkdir -p "${FS}"
t_fresh() { in_lib CI=true HOST_E2E=1 HE_FS_ROOT="${FS}" -- he_require_fresh_host; }
expect_ok "a host without a trace of an installation is fresh" t_fresh
left() { in_lib CI=true HOST_E2E=1 HE_FS_ROOT="${FS}" "$@" -- he_host_leftovers; }
mkdir -p "${FS}/opt/myriarch"
OUT="$(left)"; [ "${OUT}" = "path /opt/myriarch" ] && ok "a directory of an earlier installation is reported" || fail "leftovers: '${OUT}'"
t_not_fresh() { in_lib CI=true HOST_E2E=1 HE_FS_ROOT="${FS}" -- he_require_fresh_host; }
expect_fail "a host with an installation is not fresh: refused, with the traces" "this host is not fresh.*path /opt/myriarch" t_not_fresh
rmdir "${FS}/opt/myriarch" "${FS}/opt"
mkdir -p "${FS}/etc/systemd/system/multi-user.target.wants"
ln -s /nowhere "${FS}/etc/systemd/system/multi-user.target.wants/otelcol-supervisor.service"
OUT="$(left)"; case "${OUT}" in *"otelcol-supervisor.service"*) ok "a dangling unit symlink is a trace" ;; *) fail "the unit symlink is not reported: '${OUT}'" ;; esac
rm -rf "${FS:?}/etc"
OUT="$(left FAKE_USER_OTELCOL=1)"; [ "${OUT}" = "user otelcol" ] && ok "the service user is a trace" || fail "leftovers with the user: '${OUT}'"
OUT="$(left FAKE_PROCESSES='myriarch-updater')"; case "${OUT}" in "process myriarch-updater (pid 4242)") ok "a running updater is found by its comm (cut to 15 characters)" ;; *) fail "the updater process is not reported: '${OUT}'" ;; esac
OUT="$(left FAKE_PROCESSES='opampsupervisor otelcol-contrib')"; [ "$(printf '%s\n' "${OUT}" | wc -l | tr -d ' ')" = 2 ] && ok "a running supervisor and collector are traces" || fail "processes: '${OUT}'"
OUT="$(left FAKE_LISTENER=13133)"; [ "${OUT}" = "listener 127.0.0.1:13133" ] && ok "a listener on the health port of the collector is a trace" || fail "listener: '${OUT}'"
OUT="$(left FAKE_LISTENER=8080)"; [ -z "${OUT}" ] && ok "other ports are not" || fail "port 8080 reported: '${OUT}'"
OUT="$(left FAKE_UNIT=myriarch-updater.service)"; [ "${OUT}" = "unit-file myriarch-updater.service" ] && ok "an installed unit file (a package) is a trace" || fail "unit file: '${OUT}'"
OUT="$(left FAKE_UNIT_FOOTER=1)"; [ -z "${OUT}" ] && ok "the summary line of systemctl, without a unit, is not a trace" || fail "the footer of systemctl is reported: '${OUT}'"

# ---- identifiers from internal/brand ----------------------------------------------------------------------------------------------
brand() { bash -c ". '${LIB}'; he_brand $1"; }
for pair in ServerBinary:myriarchd UpdaterBinary:myriarch-updater CLIBinary:myrctl; do
  c="${pair%%:*}"; d="${pair#*:}"
  [ "$(brand "${c}")" = "${d}" ] && [ -d "${ROOT}/cmd/${d}" ] && ok "he_brand ${c} is the name of cmd/${d}" || fail "he_brand ${c} is '$(brand "${c}")', expected ${d}"
done
[ -n "$(brand CSRFCookie)" ] && [ -n "$(brand SessionCookie)" ] && [ -n "$(brand UpdateHealthComponent)" ] && ok "he_brand finds the cookies and the update health component" || fail "he_brand does not find the cookies"
[ "$(brand NoSuchConstant)" = "" ] && ok "he_brand prints nothing for an unknown constant" || fail "he_brand NoSuchConstant printed something"

# ---- the scrubber ----------------------------------------------------------------------------------------------------------------
SECRET_IN="${W}/secrets.txt"
# the markers of the key block are put together here: the secret scan (gitleaks, rule private-key) reads a whole block in a file as a key
PEM_BEGIN='-----BEGIN'
PEM_END='-----END'
cat > "${SECRET_IN}" <<EOF
time=2026-10-15T10:39:45 level=WARN msg="DEV ONLY" dsn="postgres://myriarch:hunter2hunter2@127.0.0.1:54341/myriarch?sslmode=disable"
enrollmentToken=myre_AbCdEfGhIjKlMnOpQrStUvWxYz012345
api token myra_AbCdEfGhIjKlMnOpQrStUvWxYz012345 and download token myrd_eyJrIjoiaSJ9.AbCdEfGh-_
Authorization: Bearer abcdef0123456789abcdef
curl -H "authorization=Basic dXNlcjpwYXNzd29yZA==" https://example.com
Set-Cookie: myriarch_session=0123456789abcdef; Path=/; HttpOnly
Cookie: myriarch_csrf=zzzz1234; myriarch_session=yyyy5678
#HttpOnly_127.0.0.1	FALSE	/	TRUE	0	myriarch_session=plainvalue
${PEM_BEGIN} PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg1234567890abcdef
${PEM_END} PRIVATE KEY-----
level=INFO msg="starting myriarchd" version=v0.2.0 rollout=ok
EOF
SCRUBBED="$(bash -c ". '${LIB}'; he_scrub < '${SECRET_IN}'")"
for needle in hunter2hunter2 AbCdEfGhIjKlMnOpQrStUvWxYz012345 eyJrIjoiaSJ9 abcdef0123456789abcdef dXNlcjpwYXNzd29yZA== 0123456789abcdef zzzz1234 yyyy5678 plainvalue MIGHAgEAMBMG; do
  case "${SCRUBBED}" in *"${needle}"*) fail "the scrubber leaves '${needle}' in the text" ;; *) ok "scrubbed: ${needle%"${needle#????????}"}..." ;; esac
done
case "${SCRUBBED}" in *"level=INFO msg=\"starting myriarchd\" version=v0.2.0 rollout=ok"*) ok "... and ordinary log lines are kept" ;; *) fail "the scrubber changed an ordinary line: ${SCRUBBED}" ;; esac
case "${SCRUBBED}" in *"[private key removed]"*) ok "... a private key block becomes one marker" ;; *) fail "no marker for the private key" ;; esac
printf 'plain text without a secret\nline 2\n' > "${W}/plain.txt"
[ "$(bash -c ". '${LIB}'; he_scrub < '${W}/plain.txt'")" = "$(cat "${W}/plain.txt")" ] && ok "text without a secret passes unchanged" || fail "he_scrub changed plain text"

# ---- the artifacts: an allow-list, assembled and checked ---------------------------------------------------------------------------
TOKEN_VALUE="myre_ThisIsTheTokenOfTheTestRunOnly0123456789"
prepare_scenario_dir() { # a scenario directory with files that belong into the upload and files that never do
  local d="${HOST_E2E_DIR}/manual"
  rm -rf "${HOST_E2E_DIR}"
  bash -c ". '${LIB}'; he_init manual"
  mkdir -p "${d}/data/postgres" "${d}/gitops" "${d}/templates" "${d}/fixtures" "${HOST_E2E_DIR}/mirror/srv/mirror" "${HOST_E2E_DIR}/tls"
  printf '%s\n' "${TOKEN_VALUE}" > "${d}/enrollment-token"
  printf 'PASS|a check|evidence\nFAIL|another|oops\n' > "${d}/results.txt"
  printf 'dsn="postgres://u:p@h/db" token=%s\n' "${TOKEN_VALUE}" > "${d}/myriarchd.log"
  printf 'ok\n' > "${d}/validate.log"
  printf 'GET /x 200\n' > "${d}/mirror.log"
  printf 'GET /y 200\n' > "${d}/mirror-evil.log"
  printf 'killed otelcol-contrib pid 1\n' > "${d}/fault.log"
  printf '[myriarch-install] done\n' > "${d}/install-1.log"
  printf '[myriarch-install] ERROR [sigstore_verification_failed]\n' > "${d}/install-tampered.log"
  printf '[myriarch-install] uninstalled\n' > "${d}/uninstall-101010.log"
  printf 'journal line\n' > "${d}/diag/journal-otelcol-supervisor.txt"
  printf 'server: {}\n' > "${d}/diag/supervisor.yaml"
  printf '[]\n' > "${d}/api/agents.json"
  # these are never in the list
  printf 'jar\n' > "${d}/cookies"
  printf 'x\n' > "${d}/data/postgres/PG_VERSION"
  printf 'x\n' > "${d}/overlay.yaml"
  printf 'x\n' > "${d}/pids"
  printf 'secret\n' > "${d}/diag/myriarch.env"
  printf 'secret\n' > "${d}/diag/enrollment-token"
  printf 'x\n' > "${d}/templates/updater.yaml"
  printf 'x\n' > "${d}/gitops/fleet.yaml"
  printf 'x\n' > "${d}/fixtures/x.json"
  printf 'x\n' > "${d}/kubeconfig"
  printf 'big\n' > "${HOST_E2E_DIR}/mirror/srv/mirror/archive.tar.gz"
  printf 'key\n' > "${HOST_E2E_DIR}/tls/ca.key"
}
prepare_scenario_dir
OUT="$(bash -c ". '${LIB}'; HE_SCENARIO=manual; HE_DIR='${HOST_E2E_DIR}/manual'; HE_UP='${HOST_E2E_DIR}/upload/manual'; he_collect_artifacts" 2>&1)"
UP="${HOST_E2E_DIR}/upload/manual"
GOT="$(cd "${UP}" 2>/dev/null && find . -type f | sed 's|^\./||' | sort | tr '\n' ' ')"
WANT="api/agents.json diag/journal-otelcol-supervisor.txt diag/supervisor.yaml fault.log install-1.log install-tampered.log mirror-evil.log mirror.log myriarchd.log results.txt uninstall-101010.log validate.log "
[ "${GOT}" = "${WANT}" ] && ok "the upload directory holds exactly the allow-listed files" || fail "upload directory: got '${GOT}', expected '${WANT}' (${OUT})"
for f in enrollment-token cookies data overlay.yaml pids templates gitops fixtures kubeconfig diag/myriarch.env diag/enrollment-token tls mirror; do
  [ ! -e "${UP}/${f}" ] && ok "never uploaded: ${f}" || fail "${f} is in the upload directory"
done
grep -rq "${TOKEN_VALUE}" "${UP}" && fail "the token is in the upload directory" || ok "the token value is nowhere in the upload directory"
grep -q 'postgres://\[removed\]' "${UP}/myriarchd.log" && ok "the log was scrubbed on the way (DSN)" || fail "myriarchd.log was not scrubbed: $(cat "${UP}/myriarchd.log")"
# a secret that gets into the directory removes it
assert_clean() { bash -c ". '${LIB}'; HE_DIR='${HOST_E2E_DIR}/manual'; he_assert_no_secrets '${UP}'"; }
printf '%s\n' "${TOKEN_VALUE}" > "${UP}/stray.txt"
expect_fail "a token in the upload directory is found, and the directory is removed" "secret was found in the upload directory" assert_clean
[ ! -e "${UP}" ] && ok "... the upload directory is gone" || fail "the upload directory is still there"
mkdir -p "${UP}"; printf 'a line with myre_0123456789abcdef0123456789abcdef in it\n' > "${UP}/shape.txt"
expect_fail "something shaped like a token is found too" "secret was found in the upload directory" assert_clean
mkdir -p "${UP}"; printf '%s EC PRIVATE KEY-----\nabc\n%s EC PRIVATE KEY-----\n' "${PEM_BEGIN}" "${PEM_END}" > "${UP}/key.txt"
expect_fail "a private key is found" "secret was found in the upload directory" assert_clean
mkdir -p "${UP}"; cp "${HOST_E2E_DIR}/tls/ca.key" "${UP}/ca-key-copy.txt"
expect_fail "the content of a key file of the run is found" "secret was found in the upload directory" assert_clean
mkdir -p "${UP}"; printf 'clean\n' > "${UP}/clean.txt"
expect_ok "a clean directory passes" assert_clean

# ---- the throwaway CA and the https file server -----------------------------------------------------------------------------------
mode_of() { case "$(uname -s)" in Darwin) stat -f %Lp "$1" ;; *) stat -c %a "$1" ;; esac; }
if openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout /dev/null -out /dev/null -days 1 -subj /CN=t -addext basicConstraints=critical,CA:TRUE >/dev/null 2>&1; then
  T="${W}/tls"
  t_ca() { bash -c ". '${LIB}'; he_ca_create '${T}'"; }
  expect_ok "he_ca_create makes a CA and a server certificate" t_ca
  [ "$(mode_of "${T}/ca.key")" = 600 ] && [ "$(mode_of "${T}/server.key")" = 600 ] && ok "... the keys are 0600" || fail "the keys are not 0600"
  [ "$(ls "${T}" | tr '\n' ' ')" = "ca.key ca.pem server.key server.pem " ] && ok "... and nothing else is left in the directory" || fail "unexpected files in the CA directory: $(ls "${T}" | tr '\n' ' ')"
  openssl verify -CAfile "${T}/ca.pem" "${T}/server.pem" >/dev/null 2>&1 && ok "... the server certificate is signed by the CA" || fail "the chain does not verify"
  TXT="$(openssl x509 -in "${T}/server.pem" -noout -text)"
  case "${TXT}" in *"IP Address:127.0.0.1"*) ok "... valid for 127.0.0.1" ;; *) fail "SAN 127.0.0.1 missing: $(printf '%s' "${TXT}" | grep -A1 'Alternative')" ;; esac
  case "${TXT}" in *"DNS:localhost"*) ok "... valid for localhost" ;; *) fail "SAN localhost missing" ;; esac
  case "${TXT}" in *"CA:FALSE"*) ok "... and it is not a CA" ;; *) fail "the server certificate may sign certificates" ;; esac
  CATXT="$(openssl x509 -in "${T}/ca.pem" -noout -text)"
  case "${CATXT}" in *"CA:TRUE, pathlen:0"*) ok "... the CA may sign leaf certificates only (pathlen 0)" ;; *) fail "the CA constraints are wrong: $(printf '%s' "${CATXT}" | grep -A1 'Basic')" ;; esac
  if openssl x509 -in "${T}/server.pem" -noout -checkend $((3 * 86400)) >/dev/null 2>&1; then fail "the server certificate lives longer than three days"; else ok "... valid for days, not for years (two days)"; fi
  t_ca2() { bash -c ". '${LIB}'; he_ca_create '${W}/tls-other'"; }
  expect_ok "a second CA for the negative case" t_ca2

  # the https file server, started by he_https_start as the scenarios do
  DOC="${W}/docroot"
  mkdir -p "${DOC}/v1" "${W}/outside"
  printf 'the file\n' > "${DOC}/v1/file.txt"
  printf 'outside\n' > "${W}/outside/secret.txt"
  PORT="$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
  cat > "${W}/serve-check.sh" <<'CHECK'
#!/usr/bin/env bash
# arguments: library, CA directory, other CA directory, document root, port
set -uo pipefail
. "$1"
he_init serve
HE_TLS="$2"
he_https_start m "$4" "$5" || { echo "start failed"; exit 1; }
url="https://127.0.0.1:$5"
code() { curl -sS --cacert "$2/ca.pem" -o /dev/null -w '%{http_code}' "$@" 2>/dev/null; }
echo "file $(curl -sS --cacert "$2/ca.pem" -o /dev/null -w '%{http_code}' "${url}/v1/file.txt")"
echo "body $(curl -sS --cacert "$2/ca.pem" "${url}/v1/file.txt")"
echo "head $(curl -sS --cacert "$2/ca.pem" -I -o /dev/null -w '%{http_code}' "${url}/v1/file.txt")"
echo "listing $(curl -sS --cacert "$2/ca.pem" -o /dev/null -w '%{http_code}' "${url}/v1/")"
echo "root $(curl -sS --cacert "$2/ca.pem" -o /dev/null -w '%{http_code}' "${url}/")"
echo "traversal $(curl -sS --cacert "$2/ca.pem" --path-as-is -o /dev/null -w '%{http_code}' "${url}/../outside/secret.txt")"
echo "encoded $(curl -sS --cacert "$2/ca.pem" --path-as-is -o /dev/null -w '%{http_code}' "${url}/%2e%2e/outside/secret.txt")"
echo "post $(curl -sS --cacert "$2/ca.pem" -X POST -o /dev/null -w '%{http_code}' "${url}/v1/file.txt")"
echo "put $(curl -sS --cacert "$2/ca.pem" -X PUT -d x -o /dev/null -w '%{http_code}' "${url}/v1/file.txt")"
echo "query $(curl -sS --cacert "$2/ca.pem" -o /dev/null -w '%{http_code}' "${url}/v1/file.txt?token=abc")"
echo "localhost $(curl -sS --cacert "$2/ca.pem" -o /dev/null -w '%{http_code}' "https://localhost:$5/v1/file.txt")"
curl -sS --cacert "$3/ca.pem" -o /dev/null "${url}/v1/file.txt" 2>/dev/null; echo "otherca $?"
curl -sS -o /dev/null "${url}/v1/file.txt" 2>/dev/null; echo "systemca $?"
he_stop_tracked
CHECK
  OUT="$(bash "${W}/serve-check.sh" "${LIB}" "${T}" "${W}/tls-other" "${DOC}" "${PORT}" 2>&1)"
  want_line() { printf '%s\n' "${OUT}" | grep -qx "$1" && ok "https server: $2" || fail "https server: $2 (output: $(printf '%s' "${OUT}" | tr '\n' ' ' | cut -c1-500))"; }
  want_line "file 200" "a file is served over TLS with the throwaway CA"
  want_line "body the file" "... with its content"
  want_line "head 200" "... HEAD works"
  want_line "listing 404" "a directory has no listing"
  want_line "root 404" "the root has no listing"
  want_line "traversal 404" "a path above the document root is not served"
  want_line "encoded 404" "... also in an encoded form"
  want_line "post 405" "POST is refused"
  want_line "put 405" "PUT is refused"
  want_line "query 200" "a query string does not matter"
  want_line "localhost 200" "the certificate is valid for localhost"
  # curl exits with 60 (the certificate does not verify) or, with the TLS stack of macOS and a certificate made by OpenSSL 4, with 35
  # (the handshake failed); the same server with the right CA answers 200 above, so either code shows that the other CA is refused
  printf '%s\n' "${OUT}" | grep -qxE 'otherca (35|60)' && ok "https server: another CA does not verify the certificate" \
    || fail "https server: another CA does not verify the certificate (output: $(printf '%s' "${OUT}" | tr '\n' ' ' | cut -c1-500))"
  printf '%s\n' "${OUT}" | grep -qx 'systemca 0' && fail "the system trust store accepted the throwaway CA" || ok "the throwaway CA is not trusted by the system, only by the process that is given it"
  LOGF="${HOST_E2E_DIR}/serve/m.log"
  if grep -q 'token=abc' "${LOGF}"; then fail "the request log keeps a query string"; else ok "the request log has no query string"; fi
  grep -q '"GET /v1/file.txt HTTP/1.1" 200' "${LOGF}" && ok "the request log names the request and the status" || fail "request log: $(tail -n 3 "${LOGF}")"
  t_bind() { python3 -I "${HE}/httpsd.py" --directory "${DOC}" --port "${PORT}" --cert "${T}/server.pem" --key "${T}/server.key" --bind 192.0.2.1; }
  expect_fail "the file server refuses a bind address that is not loopback" "must be a loopback IPv4 address" t_bind
  if pgrep -f "httpsd.py --directory ${DOC}" >/dev/null 2>&1; then fail "the file server is still running after he_stop_tracked"; else ok "he_stop_tracked stopped the file server"; fi
else
  ok "SKIP: this openssl cannot make the throwaway CA (it needs -addext); the CA and https server cases are skipped"
fi

# ---- the GitOps fixture, the versions, the template of the updater ----------------------------------------------------------------
BIN="${W}/bin"
if have go; then
  mkdir -p "${BIN}"
  if ( cd "${ROOT}" && go build -o "${BIN}/myrctl" ./cmd/myrctl && go build -o "${BIN}/myriarch-updater" ./cmd/myriarch-updater ) >"${W}/go-build.log" 2>&1; then
    t_fixture() { "${BIN}/myrctl" validate --strict --components "${ROOT}/examples/sim/components-otelcol-contrib-0.162.0.json" "${HE}/gitops"; }
    expect_ok "the GitOps fixture of the host test passes myrctl validate --strict" t_fixture
    # the template with shorter waits is still a configuration the updater accepts
    TPL="$(bash -c ". '${LIB}'; HE_DIR='${W}/tpl'; HE_DEPLOY='${ROOT}/deploy'; mkdir -p '${W}/tpl'; he_prepare_templates")"
    UY="${TPL}/updater/linux/updater.yaml"
    [ "$(grep -c '^  healthTimeout: 40s$' "${UY}")" = 1 ] && [ "$(grep -c '^  healthInterval: 2s$' "${UY}")" = 1 ] && [ "$(grep -c '^  failedRetryBackoff: 15s$' "${UY}")" = 1 ] \
      && ok "the updater template of the test waits less, each key once" || fail "the tuned template is wrong: $(grep -n 'health\|Backoff' "${UY}")"
    diff -r "${ROOT}/deploy/supervisor" "${TPL}/supervisor" >/dev/null && diff -r "${ROOT}/deploy/systemd" "${TPL}/systemd" >/dev/null \
      && [ "$(diff "${ROOT}/deploy/updater/linux/updater.yaml" "${UY}" | grep -c '^[<>]')" -le 6 ] \
      && ok "... and nothing else differs from the templates of the checkout" || fail "the template copy differs from the checkout in more than the three keys"
    sed -e 's|@@OPAMP_ENDPOINT@@|wss://127.0.0.1:18443/v1/opamp|' -e '/@@CA_FILE@@/d' -e '/@@TRUSTED_ROOT_FILE@@/d' "${UY}" > "${W}/updater-rendered.yaml"
    t_loads() { "${BIN}/myriarch-updater" status --config "${W}/updater-rendered.yaml"; }
    expect_ok "... and it loads in the updater (strict configuration, no duplicate key)" t_loads
    rm -rf "${W}/tpl"
  else
    fail "go build of myrctl and myriarch-updater failed: $(tail -n 5 "${W}/go-build.log")"
  fi
else
  ok "SKIP: go is not installed; the fixture and the template are not checked"
fi
NEW="$(bash -c ". '${LIB}'; echo \${HE_VERSION_NEW}")"; OLD="$(bash -c ". '${LIB}'; echo \${HE_VERSION_OLD}")"
[ "${NEW}" = "$(sed -n 's/^OTELCOL_VERSION=//p' "${ROOT}/ci/versions.env")" ] && ok "the new version of the test is OTELCOL_VERSION of ci/versions.env (${NEW})" || fail "HE_VERSION_NEW ${NEW} differs from OTELCOL_VERSION"
grep -q "versionRange: \">=${OLD}, <=${NEW}\"" "${HE}/gitops/fleets/host-e2e.yaml" && grep -q "^    version: ${OLD} " "${HE}/gitops/fleets/host-e2e.yaml" \
  && ok "the frame and the Git target of the fixture fleet are the two versions of the test" || fail "the fleet of the fixture does not match ${OLD} and ${NEW}"
[ -d "${ROOT}/internal/release/testdata/fixtures" ] && [ -f "${ROOT}/examples/sim/components-otelcol-contrib-${NEW}.json" ] && ok "the release fixtures and the component list for ${NEW} exist" || fail "release fixtures or component list for ${NEW} are missing"

# ---- the scripts and the workflow agree --------------------------------------------------------------------------------------------
grep -q 'COMPONENTS="contrib supervisor"' "${LIB}" && grep -q 'VERIFIER_BIN=' "${LIB}" && ok "the mirror is built with the supervisor and the updater of this checkout" || fail "he_mirror_build does not pass COMPONENTS and VERIFIER_BIN"
[ -x "${HE}/linux.sh" ] && [ -x "${HE}/httpsd.py" ] && ok "linux.sh and httpsd.py are executable" || fail "a script of scripts/host-e2e is not executable"

finish_tests "host-e2e-test"
