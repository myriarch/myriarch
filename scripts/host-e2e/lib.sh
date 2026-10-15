# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Harness of the host end-to-end test (docs/development.md, "Host end-to-end test"). Sourced by the scenario scripts
# (scripts/host-e2e/linux.sh), never executed. bash 3.2 compatible, so that the unit test (ci/tests/host-e2e-test.sh) runs on a
# developer Mac too; the scenarios themselves run on Ubuntu runners of GitHub Actions only.
#
# Why this is a library: a scenario is a function that uses the pieces below, and a second scenario (another way to install the
# host, another platform) reuses them. The structure is part of the design:
#   * lib.sh holds the harness: the guard, the results, a throwaway CA, an https file server, the mirror of the collector and
#     supervisor files, the myriarchd server (he_server_start accepts extra flags), the REST helper, the host reset and the
#     collection of the artifacts that CI uploads;
#   * a scenario lives in a script of its own (linux.sh: "manual", the installer as docs/tasks/connect-a-host.md describes it);
#   * every scenario ends with `install-linux.sh --uninstall --purge` and the check that nothing is left (he_reset_host), and
#     the workflow calls scenarios by name, so adding one means adding a function and a workflow step.
#
# The scripts change the machine they run on (a service user, systemd units, files below /etc, /opt and /var, the trust of a
# throwaway CA only for the installer process). They therefore refuse to run unless CI=true and HOST_E2E=1 are set, on Linux,
# not as root, and on a host without any trace of an installation (he_require_ci, he_require_fresh_host). Every call that
# changes the host goes through he_sudo, which checks the guard again; ci/tests/host-e2e-test.sh fails when a script of this
# directory uses the word sudo anywhere but in a comment, in he_sudo and in the probe of he_require_sudo (so a message names
# he_sudo, not sudo).
#
# Files (below $HOST_E2E_DIR, default .cache/host-e2e of the repository, git-ignored):
#   tls/                    the throwaway CA and the server certificate (keys 0600; never uploaded)
#   mirror/srv/mirror/      the collector and supervisor files in the layout of the upstream releases, plus updater/
#   <scenario>/             logs, results.txt, the work files of one scenario (the token file, the cookie jar and the database
#                           are in there and are never uploaded)
#   upload/<scenario>/      what the workflow uploads on a failure: an allow-list of files, scrubbed (he_collect_artifacts)
#
# Environment: CI, HOST_E2E (guard), HOST_E2E_DIR (work directory), HOST_E2E_BIN_DIR (myriarchd, myriarch-updater and myrctl,
# default bin/), HOST_E2E_DEPLOY_DIR (the deploy/ tree with the installer and templates, default the checkout),
# HOST_E2E_PORT_SERVER, _PG, _MIRROR, _EVIL (ports, defaults 18443, 54341, 19443, 19444).

# shellcheck disable=SC2034  # the HE_* settings are used by the scripts that source this library
HE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HE_ROOT="$(cd "${HE_LIB_DIR}/../.." && pwd)"
HE_WORK="${HOST_E2E_DIR:-${HE_ROOT}/.cache/host-e2e}"
HE_BIN="${HOST_E2E_BIN_DIR:-${HE_ROOT}/bin}"
HE_DEPLOY="${HOST_E2E_DEPLOY_DIR:-${HE_ROOT}/deploy}"
HE_TLS="${HE_WORK}/tls"
HE_MIRROR="${HE_WORK}/mirror"
# Prefix of the paths that he_host_leftovers looks at: empty on a real host, a scratch directory in the unit test.
HE_FS_ROOT="${HE_FS_ROOT:-}"

HE_PORT_SERVER="${HOST_E2E_PORT_SERVER:-18443}"
HE_PORT_PG="${HOST_E2E_PORT_PG:-54341}"
HE_PORT_MIRROR="${HOST_E2E_PORT_MIRROR:-19443}"
HE_PORT_EVIL="${HOST_E2E_PORT_EVIL:-19444}"
HE_BASE="https://127.0.0.1:${HE_PORT_SERVER}"

# The two collector versions of the update. They are tied to the GitOps fixture (the frame of fleets/host-e2e.yaml), to the
# component list of examples/sim and to the release fixtures of internal/release/testdata; ci/tests/host-e2e-test.sh keeps them
# in step with OTELCOL_VERSION of ci/versions.env.
HE_VERSION_OLD=0.161.0
HE_VERSION_NEW=0.162.0

# State of the run (he_init sets the per scenario values)
HE_SCENARIO=""
HE_DIR=""
HE_UP=""
HE_PIDS=""
HE_RESULTS=""
HE_JAR=""
HE_CSRF=""
HE_ACTIVE=0
HE_ABORTED=0
HE_SERVER_PID=""
HE_PASSED=0
HE_FAILED=0
HE_SKIPPED=0

# ------------------------------------------------------------------------------------------------------ output ---
he_log()  { printf '%s [host-e2e] %s\n' "$(date +%T)" "$*" >&2; }
he_warn() { printf '%s [host-e2e] WARNING: %s\n' "$(date +%T)" "$*" >&2; }
he_die()  { printf '%s [host-e2e] ERROR: %s\n' "$(date +%T)" "$*" >&2; exit 1; }
he_have() { command -v "$1" >/dev/null 2>&1; }
he_section() { printf '\n== %s ==\n' "$*" >&2; }

# ----------------------------------------------------------------------------------------------------- results ---
# Evidence must never contain a secret: the callers pass names, counts and status words. Never call these in a subshell
# (a pipeline, $(...)): the counters would be lost.
he_result() { # PASS|FAIL|SKIP name [evidence]
  local kind="$1" name="$2" ev="${3:-}"
  ev="$(printf '%s' "${ev}" | tr '\n' ' ' | cut -c1-300)"
  if [ -n "${HE_RESULTS}" ]; then printf '%s|%s|%s\n' "${kind}" "${name}" "${ev}" >> "${HE_RESULTS}"; fi
  case "${kind}" in
    PASS) HE_PASSED=$((HE_PASSED + 1)) ;;
    FAIL) HE_FAILED=$((HE_FAILED + 1)) ;;
    *) HE_SKIPPED=$((HE_SKIPPED + 1)) ;;
  esac
  printf '  %-4s %s%s\n' "${kind}" "${name}" "${ev:+  [${ev}]}" >&2
}
he_pass() { he_result PASS "$1" "${2:-}"; }
he_fail() { he_result FAIL "$1" "${2:-}"; }
he_skip() { he_result SKIP "$1" "${2:-}"; }
he_check_eq() { if [ "$2" = "$3" ]; then he_pass "$1" "$3"; else he_fail "$1" "expected '$2', got '$3'"; fi; }
he_check_ne() { if [ "$2" != "$3" ]; then he_pass "$1" "$3"; else he_fail "$1" "unexpected '$3'"; fi; }
he_check_ge() { if [ "${3:-}" -ge "$2" ] 2>/dev/null; then he_pass "$1" "$3 >= $2"; else he_fail "$1" "expected >= $2, got '${3:-}'"; fi; }
he_check_contains() { if printf '%s' "$3" | grep -qF -- "$2"; then he_pass "$1" "contains '$2'"; else he_fail "$1" "'$2' not found in: $(printf '%s' "$3" | head -c 200)"; fi; }
he_check_not_contains() { if printf '%s' "$3" | grep -qF -- "$2"; then he_fail "$1" "'$2' found"; else he_pass "$1" "absent '$2'"; fi; }
he_check_true() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then he_pass "$name"; else he_fail "$name" "command failed: $*"; fi; }

# Table of the results for the job summary of GitHub Actions (when the runner provides a file) and the console.
he_summary() {
  printf '\n%s passed, %s failed, %s skipped\n' "${HE_PASSED}" "${HE_FAILED}" "${HE_SKIPPED}" >&2
  if [ "${HE_FAILED}" -gt 0 ] && [ -f "${HE_RESULTS:-/nonexistent}" ]; then
    printf '\nFailed checks:\n' >&2
    grep '^FAIL|' "${HE_RESULTS}" | while IFS='|' read -r _ name ev; do printf '  - %s [%s]\n' "${name}" "${ev}" >&2; done
  fi
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -f "${HE_RESULTS:-/nonexistent}" ]; then
    {
      printf '### Host end-to-end test: %s (%s)\n\n' "${HE_SCENARIO}" "$(uname -m)"
      printf '%s passed, %s failed, %s skipped.\n\n| Result | Check | Evidence |\n|---|---|---|\n' "${HE_PASSED}" "${HE_FAILED}" "${HE_SKIPPED}"
      awk -F'|' '{ ev = $3; for (i = 4; i <= NF; i++) ev = ev "/" $i; gsub(/`/, "", ev); n = $2; gsub(/`/, "", n); printf "| %s | %s | %s |\n", $1, n, ev }' "${HE_RESULTS}"
      printf '\n'
    } >> "${GITHUB_STEP_SUMMARY}" 2>/dev/null || true
  fi
  [ "${HE_FAILED}" = 0 ]
}

# --------------------------------------------------------------------------------------------------- identifiers ---
# Product identifiers come from internal/brand (AGENTS.md): the value of one constant of internal/brand/brand.go.
he_brand() { # Name
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\\(.*\\)\"[[:space:]]*\$/\\1/p" "${HE_ROOT}/internal/brand/brand.go" | head -n 1
}

he_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) return 1 ;;
  esac
}

he_sha256() { # file
  if he_have sha256sum; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# ----------------------------------------------------------------------------------------------------- the guard ---
# The scripts install users, services and files below /etc, /opt and /var. They run on a throwaway runner of CI, nowhere else.
he_require_ci() {
  [ "${CI:-}" = true ] \
    || he_die "refusing to run: CI=true is not set. These scripts install a service user, systemd units and files below /etc, /opt and /var; they run on a throwaway CI runner only (docs/development.md, Host end-to-end test)"
  [ "${HOST_E2E:-}" = 1 ] \
    || he_die "refusing to run: HOST_E2E=1 is not set. It confirms that this machine is a throwaway runner (docs/development.md, Host end-to-end test)"
  [ "$(uname -s)" = Linux ] || he_die "refusing to run: this is not Linux ($(uname -s))"
  [ "$(id -u)" != 0 ] \
    || he_die "refusing to run as root: the embedded PostgreSQL of myriarchd refuses root; the calls that change the host go through he_sudo"
}

he_require_sudo() {
  sudo -n true 2>/dev/null || he_die "refusing to run: passwordless sudo is needed for the installer (the runner user of GitHub-hosted runners has it)"
}

# Prints one line for every trace of an installation (files, user, units, processes, listeners); nothing means clean. Also the
# check of he_reset_host ("nothing is left") and, before a scenario, the proof that the host is fresh.
he_host_leftovers() {
  local p name pid f
  for p in /opt/myriarch /etc/myriarch /var/lib/otelcol /var/lib/myriarch-updater \
    /etc/systemd/system/otelcol-supervisor.service /etc/systemd/system/myriarch-updater.service; do
    if [ -e "${HE_FS_ROOT}${p}" ] || [ -L "${HE_FS_ROOT}${p}" ]; then echo "path ${p}"; fi
  done
  if [ -d "${HE_FS_ROOT}/etc/systemd/system" ]; then
    find "${HE_FS_ROOT}/etc/systemd/system" -maxdepth 3 \( -name 'otelcol-supervisor*' -o -name 'myriarch-updater*' \) 2>/dev/null \
      | while IFS= read -r f; do echo "path ${f#"${HE_FS_ROOT}"}"; done
  fi
  if id -u otelcol >/dev/null 2>&1; then echo "user otelcol"; fi
  if he_have pgrep; then
    for name in opampsupervisor otelcol-contrib myriarch-updater; do
      # the comm of a process is cut to 15 characters
      pid="$(pgrep -x "$(printf '%.15s' "${name}")" 2>/dev/null | head -n 1)"
      if [ -n "${pid}" ]; then echo "process ${name} (pid ${pid})"; fi
    done
  fi
  if he_have ss; then
    ss -Hltn 2>/dev/null | awk '{print $4}' | grep -E ':(13133|13134)$' | while IFS= read -r f; do echo "listener ${f}"; done
  fi
  if he_have systemctl; then
    systemctl list-unit-files --no-legend 'otelcol-supervisor.service' 'myriarch-updater.service' 2>/dev/null \
      | awk '$1 ~ /^(otelcol-supervisor|myriarch-updater)\.service$/ {print "unit-file " $1}'
  fi
}

he_require_fresh_host() {
  local left
  left="$(he_host_leftovers)"
  if [ -n "${left}" ]; then
    he_die "refusing to run: this host is not fresh. A scenario starts on a machine without any trace of an installation, found: $(printf '%s' "${left}" | tr '\n' ';')"
  fi
}

# The one gate through which the host is changed: root through sudo, after the guard.
he_sudo() {
  he_require_ci
  sudo -n "$@"
}

# ------------------------------------------------------------------------------------------------------ processes ---
he_wait_for() { # timeout command...: polls once a second until the command succeeds
  local end=$((SECONDS + $1))
  shift
  while [ "${SECONDS}" -lt "${end}" ]; do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# he_settle NAME TIMEOUT WANTED COMMAND...: waits until the output of the command is WANTED, then records PASS (with the time it took)
# or, when the time is up, FAIL (with what the command printed last). For state that the server does not compute when it is asked:
# the engine of the rollouts derives the phase and the steps of a target on its tick, after the last message of the updater, and the
# API returns the stored copy (docs/design/version-selection.md, "Target progress", "Derivation"); a package status is a message of
# its own. A check that reads such state at a fixed moment fails on the runs where the read comes before the tick. Call it
# directly, not in a subshell: the counters of the results would be lost.
he_settle() {
  local name="$1" limit="$2" want="$3" from="${SECONDS}" got=""
  shift 3
  while :; do
    got="$("$@" 2>/dev/null)"
    if [ "${got}" = "${want}" ]; then
      he_pass "${name}" "${want}, after $((SECONDS - from)) s"
      return 0
    fi
    [ $((SECONDS - from)) -lt "${limit}" ] || break
    sleep 1
  done
  he_fail "${name}" "wanted '${want}', found '${got}' after ${limit} s"
  return 1
}

he_port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# he_spawn NAME LOGFILE COMMAND...: starts a background process of this run (stopped by he_stop_tracked); its pid is HE_LAST_PID.
# The process gets a higher limit of open files (the embedded PostgreSQL wants it).
HE_LAST_PID=""
he_spawn() {
  local name="$1" logf="$2"
  shift 2
  ( ulimit -n 65536 2>/dev/null || ulimit -n 10240 2>/dev/null; exec "$@" ) >"${logf}" 2>&1 &
  HE_LAST_PID=$!
  # not a job of this shell any more: bash does not report its end ("Terminated") when he_stop_tracked stops it
  disown "${HE_LAST_PID}" 2>/dev/null || true
  printf '%s %s\n' "${name}" "${HE_LAST_PID}" >> "${HE_PIDS}"
}

# Stops the processes of this run in the reverse order of their start: SIGTERM, up to 40 s, then SIGKILL.
he_stop_tracked() {
  local name pid i alive
  [ -s "${HE_PIDS:-/nonexistent}" ] || return 0
  # the order of the file is the order of the start
  local lines
  lines="$(awk '{a[NR]=$0} END {for (i = NR; i >= 1; i--) print a[i]}' "${HE_PIDS}")"
  while read -r name pid; do
    if kill -0 "${pid}" 2>/dev/null; then kill -TERM "${pid}" 2>/dev/null; fi
  done <<< "${lines}"
  for _ in $(seq 1 40); do
    alive=0
    while read -r name pid; do
      if kill -0 "${pid}" 2>/dev/null; then alive=1; fi
    done <<< "${lines}"
    [ "${alive}" = 0 ] && break
    sleep 1
  done
  while read -r name pid; do
    if kill -0 "${pid}" 2>/dev/null; then he_warn "${name} (pid ${pid}) ignored SIGTERM: SIGKILL"; kill -KILL "${pid}" 2>/dev/null; fi
  done <<< "${lines}"
  : > "${HE_PIDS}"
  # an embedded PostgreSQL that outlived its parent
  local pm="${HE_DIR}/data/postgres/data/postmaster.pid" pgpid
  if [ -f "${pm}" ]; then
    pgpid="$(head -n 1 "${pm}")"
    if [ -n "${pgpid}" ] && kill -0 "${pgpid}" 2>/dev/null; then
      kill -TERM "${pgpid}" 2>/dev/null
      he_wait_for 30 sh -c "! kill -0 ${pgpid}" || kill -KILL "${pgpid}" 2>/dev/null
    fi
  fi
  return 0
}

# --------------------------------------------------------------------------------------------------- scenario state ---
he_init() { # scenario
  HE_SCENARIO="$1"
  HE_DIR="${HE_WORK}/${HE_SCENARIO}"
  HE_UP="${HE_WORK}/upload/${HE_SCENARIO}"
  HE_PIDS="${HE_DIR}/pids"
  HE_RESULTS="${HE_DIR}/results.txt"
  HE_JAR="${HE_DIR}/cookies"
  ( umask 077; mkdir -p "${HE_WORK}" ) && chmod 700 "${HE_WORK}"
  rm -rf "${HE_DIR}" "${HE_UP}"
  ( umask 077; mkdir -p "${HE_DIR}" "${HE_DIR}/diag" "${HE_DIR}/api" )
  : > "${HE_PIDS}"
  : > "${HE_RESULTS}"
  HE_PASSED=0
  HE_FAILED=0
  HE_SKIPPED=0
}

# --------------------------------------------------------------------------------------------------------- TLS ---
# A throwaway CA (EC P-256, two days) and a server certificate for 127.0.0.1 and localhost. The installers need an https:// URL,
# the supervisor and the updater a certificate they can verify: this is the strict path, without --insecure. Only the installer
# process trusts the CA (CURL_CA_BUNDLE, he_installer); the trust store of the host is not touched. The keys stay in HE_TLS.
he_ca_create() { # [dir]
  local d="${1:-${HE_TLS}}"
  he_have openssl || return 1
  ( umask 077
    mkdir -p "${d}" || exit 1
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout "${d}/ca.key" -out "${d}/ca.pem" -days 2 \
      -subj "/CN=myriarch host-e2e throwaway CA" -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
      -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1 || exit 1
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout "${d}/server.key" -out "${d}/server.csr" \
      -subj "/CN=localhost" >/dev/null 2>&1 || exit 1
    printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n' > "${d}/server.ext"
    openssl x509 -req -in "${d}/server.csr" -CA "${d}/ca.pem" -CAkey "${d}/ca.key" -CAcreateserial -out "${d}/server.pem" -days 2 \
      -extfile "${d}/server.ext" >/dev/null 2>&1 || exit 1
    rm -f "${d}/server.csr" "${d}/server.ext" "${d}/ca.srl"
  ) || return 1
  chmod 644 "${d}/ca.pem" "${d}/server.pem"
}

# ------------------------------------------------------------------------------------------------ https file server ---
he_curl() { curl -sS --cacert "${HE_TLS}/ca.pem" --max-time "${HE_CURL_TIMEOUT:-60}" "$@"; }

he_https_start() { # name docroot port: serves the directory over https on 127.0.0.1 (httpsd.py)
  local name="$1" root="$2" port="$3"
  he_have python3 || return 1
  he_port_free "${port}" || { he_warn "port ${port} is in use"; return 1; }
  he_spawn "${name}" "${HE_DIR}/${name}.log" python3 -I "${HE_LIB_DIR}/httpsd.py" --directory "${root}" --port "${port}" \
    --cert "${HE_TLS}/server.pem" --key "${HE_TLS}/server.key"
  he_wait_for 30 sh -c "curl -sS --cacert '${HE_TLS}/ca.pem' -o /dev/null https://127.0.0.1:${port}/" || return 1
}

# ----------------------------------------------------------------------------------------------------- the mirror ---
# The collector and supervisor files of both versions in the layout of the upstream releases (scripts/build-mirror-image-crane.sh),
# plus the myriarch-updater of this checkout under updater/. The mirror builder verifies SHA-256 and the Sigstore bundle of every
# file offline against a trusted root it fetches through TUF, so the test runs real upstream files and not copies of them.
he_updater_asset() { # prints the file name of the updater below updater/ (version of this checkout, as in a release)
  printf 'myriarch-updater_%s_linux_%s' "$(tr -d '[:space:]' < "${HE_ROOT}/VERSION")" "$(he_arch)"
}

he_mirror_build() {
  local log="${HE_WORK}/mirror-build.log" dir="${HE_MIRROR}/srv/mirror"
  if [ ! -s "${dir}/SHA256SUMS" ]; then
    he_log "building the mirror of the collector and supervisor files ($(he_arch), ${HE_VERSION_OLD} and ${HE_VERSION_NEW})"
    OUT_DIR="${HE_MIRROR}" COMPONENTS="contrib supervisor" VERSIONS="${HE_VERSION_OLD} ${HE_VERSION_NEW}" PLATFORMS="linux/$(he_arch)" \
      VERIFIER_BIN="${HE_BIN}/$(he_brand UpdaterBinary)" "${HE_ROOT}/scripts/build-mirror-image-crane.sh" > "${log}" 2>&1 \
      || { tail -n 20 "${log}" >&2; return 1; }
  else
    he_log "the mirror is already there (${dir})"
  fi
  mkdir -p "${dir}/updater"
  cp "${HE_BIN}/$(he_brand UpdaterBinary)" "${dir}/updater/$(he_updater_asset)"
  chmod 644 "${dir}/updater/$(he_updater_asset)"
}

# A second mirror root with one collector archive that is not what the release signed: the checksum file is adjusted to the
# changed file, so only the Sigstore bundle can tell. Prints nothing; the directory is HE_WORK/mirror-evil.
he_mirror_tamper() { # version
  local ver="$1" src="${HE_MIRROR}/srv/mirror" dst="${HE_WORK}/mirror-evil" asset
  asset="otelcol-contrib_${ver}_linux_$(he_arch).tar.gz"
  rm -rf "${dst}"
  mkdir -p "${dst}/v${ver}" || return 1
  cp "${src}/v${ver}/${asset}.sigstore.json" "${dst}/v${ver}/" || return 1
  printf 'this is not the archive that the release signed\n' > "${dst}/v${ver}/${asset}"
  he_sha256 "${dst}/v${ver}/${asset}" | tr -d '\n' > "${dst}/v${ver}/${asset}.sha256"
  cp "${src}/trusted_root.json" "${dst}/"
}

# -------------------------------------------------------------------------------------------------- the server ---
# myriarchd in dev mode on 127.0.0.1 with TLS (the dev guard asks for --allow-nonlocal for that), the GitOps fixture of this
# directory, the release fixtures of the tests, and the mirror as artifact source with offline Sigstore verification against the
# trusted root of the mirror. Extra arguments are passed on to `myriarchd dev` (a scenario that needs a flag adds it here).
# HE_OVERLAY_EXTRA, a file, is appended to the configuration overlay for further keys.
he_server_start() { # [extra myriarchd dev flags...]
  local gitops="${HE_DIR}/gitops" fixtures="${HE_DIR}/fixtures" overlay="${HE_DIR}/overlay.yaml" srv
  srv="${HE_BIN}/$(he_brand ServerBinary)"
  [ -x "${srv}" ] || { he_fail "myriarchd binary" "${srv} is missing"; return 1; }
  rm -rf "${gitops}" "${fixtures}" "${HE_DIR}/data"
  mkdir -p "${gitops}"
  cp -R "${HE_LIB_DIR}/gitops/." "${gitops}/"
  cp -R "${HE_ROOT}/internal/release/testdata/fixtures" "${fixtures}"
  if "${HE_BIN}/myrctl" validate --strict --components "${HE_ROOT}/examples/sim/components-otelcol-contrib-0.162.0.json" "${gitops}" > "${HE_DIR}/validate.log" 2>&1; then
    he_pass "myrctl validate --strict of the GitOps fixture" "$(tail -n 1 "${HE_DIR}/validate.log")"
  else
    he_fail "myrctl validate --strict of the GitOps fixture" "$(tail -n 3 "${HE_DIR}/validate.log" | tr '\n' ' ')"
    return 1
  fi
  cat > "${overlay}" <<EOF
server:
  publicUrl: ${HE_BASE}
  tls:
    certFile: ${HE_TLS}/server.pem
    keyFile: ${HE_TLS}/server.key
dev:
  postgresPort: ${HE_PORT_PG}
rollouts:
  tickInterval: 1s
outbound:
  caFile: ${HE_TLS}/ca.pem
artifacts:
  sourceBaseUrl: https://127.0.0.1:${HE_PORT_MIRROR}
  prefetchPlatforms: [linux/$(he_arch)]
  verify:
    requireChecksum: true
    requireSignature: true
    trustedRootFile: ${HE_MIRROR}/srv/mirror/trusted_root.json
EOF
  if [ -n "${HE_OVERLAY_EXTRA:-}" ]; then cat "${HE_OVERLAY_EXTRA}" >> "${overlay}"; fi
  he_spawn myriarchd "${HE_DIR}/myriarchd.log" "${srv}" dev --allow-nonlocal --listen "127.0.0.1:${HE_PORT_SERVER}" --gitops-dir "${gitops}" \
    --data-dir "${HE_DIR}/data" -c "${overlay}" --releases-fixtures "${fixtures}" --replica-id host-e2e ${1+"$@"}
  HE_SERVER_PID="${HE_LAST_PID}"
  # the first start downloads the PostgreSQL binaries; a server that has died is not waited for
  local end=$((SECONDS + 240)) ready=0
  while [ "${SECONDS}" -lt "${end}" ]; do
    kill -0 "${HE_SERVER_PID}" 2>/dev/null || break
    if he_curl -f -o /dev/null "${HE_BASE}/readyz" 2>/dev/null; then ready=1; break; fi
    sleep 1
  done
  if [ "${ready}" != 1 ]; then
    he_fail "myriarchd /readyz" "no answer; the last lines of myriarchd.log: $(tail -n 3 "${HE_DIR}/myriarchd.log" | tr '\n' ' ' | cut -c1-250)"
    return 1
  fi
  he_pass "myriarchd /readyz over https" "${HE_BASE}"
}

# -------------------------------------------------------------------------------------------------------- REST ---
he_api() { # METHOD PATH [JSON]: the body of the response; the session of he_login
  local method="$1" path="$2" body="${3:-}"
  case "${method}" in
    GET | HEAD) he_curl -X "${method}" -b "${HE_JAR}" "${HE_BASE}/api/v1${path}" ;;
    *)
      [ -n "${body}" ] || body='{}'
      he_curl -X "${method}" -b "${HE_JAR}" -H "X-CSRF-Token: ${HE_CSRF}" -H 'Content-Type: application/json' -d "${body}" "${HE_BASE}/api/v1${path}" ;;
  esac
}

he_api_code() { # METHOD PATH [JSON]: only the HTTP status
  local method="$1" path="$2" body="${3:-}"
  case "${method}" in
    GET | HEAD) he_curl -o /dev/null -w '%{http_code}' -X "${method}" -b "${HE_JAR}" "${HE_BASE}/api/v1${path}" ;;
    *)
      [ -n "${body}" ] || body='{}'
      he_curl -o /dev/null -w '%{http_code}' -X "${method}" -b "${HE_JAR}" -H "X-CSRF-Token: ${HE_CSRF}" -H 'Content-Type: application/json' \
        -d "${body}" "${HE_BASE}/api/v1${path}" ;;
  esac
}

# The well-known login of dev mode (admin/admin); the cookie jar and the CSRF token of the session are in HE_JAR and HE_CSRF.
he_login() {
  local code csrf_cookie
  rm -f "${HE_JAR}"
  ( umask 077; : > "${HE_JAR}" )
  code="$(he_curl -c "${HE_JAR}" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"admin"}' "${HE_BASE}/api/v1/auth/dev-login")"
  csrf_cookie="$(he_brand CSRFCookie)"
  HE_CSRF="$(awk -v n="${csrf_cookie}" '$6 == n {print $7}' "${HE_JAR}")"
  [ "${code}" = 200 ] || [ "${code}" = 204 ] || { he_warn "dev login answered ${code}"; return 1; }
  [ -n "${HE_CSRF}" ]
}

# Prints the ::add-mask:: command of GitHub Actions for a secret in a file, so that the runner hides it in every log.
he_mask_file() { # file
  if [ "${GITHUB_ACTIONS:-}" = true ]; then printf '::add-mask::%s\n' "$(tr -d '\r\n' < "$1")"; fi
}

# Creates an enrollment token through the API and writes the secret to a file (0600); the secret is never printed.
he_token_create() { # name file
  local name="$1" file="$2" prefix
  prefix="$(he_brand EnrollmentTokenPrefix)"
  ( umask 077; he_api POST /enrollment-tokens "{\"name\":\"${name}\"}" | jq -r '.secret // empty' > "${file}" )
  [ -s "${file}" ] && grep -Eq "^${prefix}[A-Za-z0-9_-]{32,}\$" "${file}" || return 1
  he_mask_file "${file}"
}

# ---- queries used by the scenarios
he_agent_id() { # attribute-key attribute-value [kind]
  he_api GET "/agents?attr=$1%3D$2&kind=${3:-collector}" | jq -r '.items[0].id // empty'
}
he_updater_id() { # collector-agent-id
  he_api GET "/agents/$1" | jq -r '.agent.updater.agentId // empty'
}
he_agent_is() { # agent-id jq-filter wanted: a predicate for he_wait_for
  [ "$(he_api GET "/agents/$1" | jq -r "$2")" = "$3" ]
}
he_latest_rollout() { # fleet
  he_api GET "/rollouts?kind=version&fleet=$1&limit=50" | jq -r '[.items[]?] | sort_by(.createdAt) | last | .id // empty'
}
he_rollout_final() { # rollout-id: a predicate: the rollout is done, paused or ended some other way
  case "$(he_api GET "/rollouts/$1" | jq -r .status)" in completed | paused | failed | aborted | rolled_back | superseded) return 0 ;; esac
  return 1
}
he_steps_of() { # rollout-id: "name:status,..." of the first target
  he_api GET "/rollouts/$1/targets" | jq -r '[.items[0].progress.steps[]? | "\(.name):\(.status)"] | join(",")'
}
# The phase and the steps of a target are derived by the engine on its tick and stored: read them after he_settle, never at a fixed
# moment after the updater has reported (the engine needs up to rollouts.tickInterval for it).
he_target_phase() { # rollout-id: the stored phase of the first target; nothing before there is one
  he_api GET "/rollouts/$1/targets" | jq -r '.items[0].progress.phase // empty'
}
he_package_status() { # updater-agent-id: the OpAMP package status of the collector package; "none" before the first report
  he_api GET "/agents/$1" | jq -r '.agent.packageStatus.packages[""].status // "none"'
}
he_select_version() { # fleet version reason expected-target-version: prints the id of the rollout the UI action starts
  local resp="${HE_DIR}/api/select-version-$2.json"
  he_api POST "/fleets/$1/actions/select-version" "{\"version\":\"$2\",\"reason\":\"$3\",\"expectedTargetVersion\":\"$4\"}" > "${resp}"
  jq -r '.rolloutId // empty' "${resp}"
}

# ------------------------------------------------------------------------------------------------- the host side ---
HE_INSTALLER_NAME=install-linux.sh
HE_INSTALL_ROOT=/opt/myriarch

# The installer of the checkout, as root, with the throwaway CA for the downloads of this process only.
he_installer() { # installer arguments...
  he_sudo env "CURL_CA_BUNDLE=${HE_TLS}/ca.pem" "${HE_DEPLOY}/scripts/${HE_INSTALLER_NAME}" "$@"
}

# `--uninstall --purge`, then the proof that nothing is left. With a first argument "quiet" it only cleans up (the exit path).
he_reset_host() { # [quiet]
  local quiet="${1:-}" left
  he_log "uninstall --purge"
  if he_installer --uninstall --purge > "${HE_DIR}/uninstall-$(date +%H%M%S).log" 2>&1; then
    [ "${quiet}" = quiet ] || he_pass "install-linux.sh --uninstall --purge"
  else
    [ "${quiet}" = quiet ] || he_fail "install-linux.sh --uninstall --purge" "$(tail -n 2 "${HE_DIR}"/uninstall-*.log | tr '\n' ' ' | cut -c1-200)"
  fi
  left="$(he_host_leftovers)"
  if [ "${quiet}" = quiet ]; then
    if [ -n "${left}" ]; then he_warn "left on the host after the reset: $(printf '%s' "${left}" | tr '\n' ';')"; fi
    return 0
  fi
  if [ -z "${left}" ]; then he_pass "nothing is left on the host (files, user, units, processes, ports)"; else he_fail "nothing is left on the host" "$(printf '%s' "${left}" | tr '\n' ';' | cut -c1-280)"; fi
}

# Fault injection: kills every collector process whose binary is below <install dir>/versions/<version>/ (scripts/e2e/ui-updates/killer.sh,
# the same helper as the local end-to-end test), as root, because the collector runs as its own user.
HE_KILLER="${HE_ROOT}/scripts/e2e/ui-updates/killer.sh"
he_fault_start() { # version
  HE_FAULT_PIDFILE="${HE_DIR}/fault.pid"
  rm -f "${HE_FAULT_PIDFILE}"
  # the log belongs to the user of the test: the redirect is that of the function call, only the command inside runs as root
  he_sudo bash -c 'echo $$ > "$1"; shift; exec "$@"' _ "${HE_FAULT_PIDFILE}" "${HE_KILLER}" "${HE_INSTALL_ROOT}/collector" "$1" 0.3 \
    > "${HE_DIR}/fault.log" 2>&1 &
  he_wait_for 20 test -s "${HE_FAULT_PIDFILE}"
}
he_fault_stop() {
  if [ -n "${HE_FAULT_PIDFILE:-}" ] && [ -s "${HE_FAULT_PIDFILE}" ]; then
    he_sudo kill "$(cat "${HE_FAULT_PIDFILE}")" 2>/dev/null || true
    rm -f "${HE_FAULT_PIDFILE}"
  fi
  return 0
}

# The installer reads its templates from a directory of its own (--templates-dir): a copy of the templates of the checkout in
# which the updater waits less (health timeout 40 s instead of 120 s, a check every 2 s instead of 5 s, a retry of a failed
# version after 15 s instead of 10 min), so that the rollback and the retry of the scenario take minutes and not half an hour.
# Nothing else differs; the script and the other templates are the ones of the checkout. Prints the directory.
he_prepare_templates() {
  local dir="${HE_DIR}/templates"
  rm -rf "${dir}"
  mkdir -p "${dir}" || return 1
  cp -R "${HE_DEPLOY}/supervisor" "${HE_DEPLOY}/systemd" "${HE_DEPLOY}/updater" "${dir}/" || return 1
  python3 -I -c '
import re, sys
path = sys.argv[1]
text = open(path).read()
if not re.search(r"^update:[ \t]*\n", text, re.M):
    sys.exit("no update: block in " + path)
for key, value in (("healthTimeout", "40s"), ("healthInterval", "2s"), ("failedRetryBackoff", "15s")):
    pattern = re.compile(r"^(  " + key + r":).*$", re.M)
    if pattern.search(text):
        text = pattern.sub(lambda m: m.group(1) + " " + value, text, count=1)
    else:
        text = re.sub(r"^(update:[ \t]*\n)", lambda m: m.group(1) + "  " + key + ": " + value + "\n", text, count=1, flags=re.M)
open(path, "w").write(text)
' "${dir}/updater/linux/updater.yaml" || return 1
  printf '%s' "${dir}"
}

# What the host looks like, for the artifacts of a failed run. Names, modes and logs; never the content of myriarch.env or of
# the token file. The files are written by the user of the test (the redirects run without sudo on purpose); the content comes
# from root.
he_diag() {
  local d="${HE_DIR}/diag" u
  he_log "collecting diagnostics"
  for u in otelcol-supervisor myriarch-updater; do
    he_sudo systemctl status "${u}.service" --no-pager -l > "${d}/status-${u}.txt" 2>&1 || true
    he_sudo journalctl -u "${u}.service" --no-pager -n 600 > "${d}/journal-${u}.txt" 2>&1 || true
  done
  he_sudo find /opt/myriarch /etc/myriarch /var/lib/otelcol /var/lib/myriarch-updater -maxdepth 4 -ls > "${d}/files.txt" 2>&1 || true
  he_sudo cat /etc/myriarch/supervisor.yaml > "${d}/supervisor.yaml" 2>/dev/null || true
  he_sudo cat /etc/myriarch/updater.yaml > "${d}/updater.yaml" 2>/dev/null || true
  ps -eo pid,user,args 2>/dev/null | grep -E 'opampsupervisor|otelcol-contrib|myriarch|python3' | grep -v grep > "${d}/processes.txt" || true
  if he_have ss; then ss -ltnp > "${d}/listeners.txt" 2>&1 || true; fi
  if [ -f "${HE_JAR}" ] && [ -n "${HE_CSRF:-}" ]; then
    he_api GET '/agents?limit=50' 2>/dev/null | jq '[.items[]? | {id, kind, hostname, version, status: .connection.status, healthy: .health.healthy, configSync}]' > "${HE_DIR}/api/agents.json" 2>/dev/null || true
    he_api GET '/rollouts?limit=20' > "${HE_DIR}/api/rollouts.json" 2>/dev/null || true
    he_api GET '/gitops/status' > "${HE_DIR}/api/gitops-status.json" 2>/dev/null || true
  fi
}

# he_probe_preflight_sandbox: the updater could not start the preflight of a new collector as the account of the supervisor (its
# journal says "operation not permitted"). Starts the installed collector the same way, in transient units with the sandbox of
# myriarch-updater.service: with all of its settings, without any, and without each one of them in turn. Each start runs twice: from
# Python (setgroups, setgid, setuid, exec) and from a small Go program that starts it like internal/updater/exec_unix.go
# (os/exec with a Credential and Setpgid), and once more as root from Go. The table shows which setting refuses which start; it goes
# to diag/preflight-sandbox.txt and to the log. The probe files live in /usr/local/libexec while it runs (ProtectHome and PrivateTmp
# hide the work directory from the units) and are removed afterwards.
he_probe_preflight_sandbox() {
  local out="${HE_DIR}/diag/preflight-sandbox.txt" unit=/etc/systemd/system/myriarch-updater.service account bin stage line i j label
  local dir="${HE_WORK}/preflight-probe" lib=/usr/local/libexec/he-preflight-probe target
  local -a props=() args=() targets=()
  mkdir -p "${dir}" "${HE_DIR}/diag"
  : > "${out}"
  account="$(he_sudo systemctl show -p User --value otelcol-supervisor.service 2>/dev/null)"
  bin="$(readlink -f /opt/myriarch/collector/current 2>/dev/null)/otelcol-contrib"
  if [ -z "${account}" ] || [ ! -x "${bin}" ]; then echo "nothing to probe (account '${account}', binary ${bin})" >> "${out}"; return 0; fi
  while IFS= read -r line; do props+=("${line}"); done < <(sed -n '/^\[Service\]/,/^\[Install\]/p' "${unit}" \
    | grep -E '^(NoNewPrivileges|PrivateTmp|ProtectHome|ProtectSystem|ReadWritePaths|StateDirectory|ProtectKernelTunables|ProtectKernelModules|ProtectKernelLogs|ProtectControlGroups|ProtectClock|ProtectHostname|PrivateDevices|RestrictAddressFamilies|RestrictNamespaces|RestrictSUIDSGID|LockPersonality|SystemCallArchitectures|CapabilityBoundingSet|LimitNOFILE)=')
  cat > "${dir}/probe.py" <<'PY'
import os, pwd, sys
path, account = sys.argv[1], sys.argv[2]
pw = pwd.getpwnam(account)
r, w = os.pipe()
pid = os.fork()
if pid == 0:
    os.close(r)
    step = "setgroups"
    try:
        os.setgroups([])
        step = "setgid"; os.setgid(pw.pw_gid)
        step = "setuid"; os.setuid(pw.pw_uid)
        step = "chdir"; os.chdir("/")
        step = "execve"
        nul = os.open(os.devnull, os.O_WRONLY)
        os.dup2(nul, 1); os.dup2(nul, 2)
        os.execv(path, [path, "--version"])
    except OSError as e:
        os.write(w, ("%s: %s" % (step, e.strerror)).encode())
        os._exit(97)
os.close(w)
msg = os.read(r, 512).decode()
_, status = os.waitpid(pid, 0)
print(msg or "ok (exit %d)" % os.waitstatus_to_exitcode(status))
PY
  cat > "${dir}/main.go" <<'GO'
// Starts PATH --version as ACCOUNT the way internal/updater/exec_unix.go starts the preflight, or as root with "root".
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

func main() {
	path, account := os.Args[1], os.Args[2]
	cmd := exec.Command(path, "--version")
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if account != "root" {
		u, err := user.Lookup(account)
		if err != nil {
			fmt.Println("lookup:", err)
			return
		}
		uid, _ := strconv.ParseUint(u.Uid, 10, 32)
		gid, _ := strconv.ParseUint(u.Gid, 10, 32)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + u.HomeDir, "USER=" + u.Username}
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	if err := cmd.Run(); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("ok")
}
GO
  if ! go build -o "${dir}/probe" "${dir}/main.go" > "${dir}/build.log" 2>&1; then
    echo "the Go probe does not build: $(tail -n 1 "${dir}/build.log")" >> "${out}"
    return 0
  fi
  he_sudo install -d -m 0755 "${lib}" && he_sudo install -m 0755 "${dir}/probe" "${dir}/probe.py" "${lib}/" || return 0
  # the installed binary, and a copy in a staging directory as the updater makes one (versions/<v>.tmp-<suffix>, 0755)
  stage=/opt/myriarch/collector/versions/probe.tmp-he
  he_sudo install -d -m 0755 "${stage}" && he_sudo install -m 0755 "${bin}" "${stage}/otelcol-contrib" && targets+=("${stage}/otelcol-contrib")
  targets=("${bin}" "${targets[@]}")
  {
    echo "account ${account}; settings of ${unit}:"
    printf '  %s\n' "${props[@]}"
  } >> "${out}"
  for target in "${targets[@]}"; do
    echo "target ${target}" >> "${out}"
    for ((i = -2; i < ${#props[@]}; i++)); do
      args=()
      if [ "${i}" = -2 ]; then label="no setting"
      else
        if [ "${i}" = -1 ]; then label="all settings"; else label="all but ${props[i]%%=*}"; fi
        for ((j = 0; j < ${#props[@]}; j++)); do
          [ "${j}" = "${i}" ] && continue
          args+=(-p "${props[j]}")
        done
      fi
      printf '  %-40s python: %-28s go as %s: %-36s go as root: %s\n' "${label}" \
        "$(he_sudo systemd-run --quiet --wait --pipe --collect -p User=root "${args[@]}" /usr/bin/python3 -I "${lib}/probe.py" "${target}" "${account}" 2>&1 | tail -n 1)" \
        "${account}" \
        "$(he_sudo systemd-run --quiet --wait --pipe --collect -p User=root "${args[@]}" "${lib}/probe" "${target}" "${account}" 2>&1 | tail -n 1)" \
        "$(he_sudo systemd-run --quiet --wait --pipe --collect -p User=root "${args[@]}" "${lib}/probe" "${target}" root 2>&1 | tail -n 1)" >> "${out}"
    done
  done
  he_sudo rm -rf "${lib}" "${stage}" || true
  he_log "preflight sandbox probe (${out}):"
  sed 's/^/    /' "${out}" >&2
}

# ------------------------------------------------------------------------------------------------------ artifacts ---
# What leaves the runner is an allow-list, assembled here and scrubbed: the workflow uploads HE_UP and nothing else. These files
# are never in the list, so that they cannot be uploaded by accident: the token file, the cookie jar, the keys of the CA, the
# database (data/), the mirror and every file of the host that holds a secret (myriarch.env, enrollment-token).
HE_ARTIFACT_FILES="results.txt validate.log myriarchd.log mirror.log mirror-evil.log fault.log install-*.log uninstall-*.log diag/*.txt diag/*.yaml api/*.json"

# Removes what must not appear in a log that anyone who is signed in to GitHub can download: enrollment, API and download
# tokens, bearer credentials, DSNs, session cookies and private keys. A filter from standard input to standard output.
he_scrub() {
  local cookies
  cookies="$(he_brand SessionCookie)|$(he_brand CSRFCookie)"
  python3 -I -c '
import re, sys
cookies = sys.argv[1]
text = sys.stdin.buffer.read().decode("utf-8", "replace")
rules = [
    (r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----", "[private key removed]", re.S),
    (r"myr[aed]_[A-Za-z0-9_.~-]+", "myrX_[removed]", 0),
    (r"(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}", r"\1 [removed]", 0),
    (r"(?i)\b(authorization\s*[:=]\s*)\S.*", r"\1[removed]", 0),
    (r"postgres(?:ql)?://[^\s\"<>]+", "postgres://[removed]", 0),
    (r"(?i)\b((?:__Host-)?(?:" + cookies + r")=)[^;\s\"]+", r"\1[removed]", 0),
    (r"(?i)\b(set-cookie|cookie)\s*:.*", r"\1: [removed]", 0),
]
for pattern, repl, flags in rules:
    text = re.sub(pattern, repl, text, flags=flags)
sys.stdout.write(text)
' "${cookies}"
}

# Fails (and removes the directory) when a secret of this run, or something shaped like one, is in a directory that is to be uploaded.
he_assert_no_secrets() { # dir
  local dir="$1" f tmp hits=""
  [ -d "${dir}" ] || return 0
  tmp="$(mktemp "${TMPDIR:-/tmp}/he-secrets.XXXXXX")"
  for f in "${HE_DIR}/enrollment-token" "${HE_TLS}/ca.key" "${HE_TLS}/server.key"; do
    [ -s "${f}" ] || continue
    grep -v '^$' "${f}" > "${tmp}"
    if grep -rlF -f "${tmp}" "${dir}" >/dev/null 2>&1; then hits="${hits} content-of-$(basename "${f}")"; fi
  done
  rm -f "${tmp}"
  if grep -rEl 'myr[aed]_[A-Za-z0-9]{16,}|-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----' "${dir}" >/dev/null 2>&1; then hits="${hits} token-or-key-pattern"; fi
  if [ -n "${hits}" ]; then
    rm -rf "${dir}"
    he_warn "a secret was found in the upload directory (${hits# }): the directory was removed"
    return 1
  fi
  return 0
}

# Assembles HE_UP from the allow-list. Every call starts from an empty directory and ends with the same check, so it can be
# repeated: he_finish_scenario calls it after the diagnostics of a failure and again at the end.
he_collect_artifacts() {
  local pat f rel out pats
  [ -n "${HE_DIR}" ] && [ -d "${HE_DIR}" ] || return 0
  rm -rf "${HE_UP}"
  mkdir -p "${HE_UP}"
  # split into words without expanding the patterns against the current directory
  read -r -a pats <<< "${HE_ARTIFACT_FILES}"
  for pat in "${pats[@]}"; do
    # the patterns are relative to the scenario directory and expanded here
    for f in "${HE_DIR}"/${pat}; do
      [ -f "${f}" ] || continue
      rel="${f#"${HE_DIR}"/}"
      out="${HE_UP}/${rel}"
      mkdir -p "$(dirname "${out}")"
      he_scrub < "${f}" > "${out}"
    done
  done
  if [ -f "${HE_WORK}/mirror-build.log" ]; then he_scrub < "${HE_WORK}/mirror-build.log" > "${HE_UP}/mirror-build.log"; fi
  he_assert_no_secrets "${HE_UP}" || return 1
  he_log "artifacts for the upload: ${HE_UP} ($(find "${HE_UP}" -type f 2>/dev/null | wc -l | tr -d ' ') files)"
}

# ---------------------------------------------------------------------------------------------------- scenarios ---
# What every scenario needs once: the guard, binaries, the CA, the mirror (and its server). A scenario that has started with
# he_run_scenario finds all of it in place.
he_prepare_shared() {
  local b
  he_have curl && he_have jq && he_have python3 && he_have openssl || he_die "curl, jq, python3 and openssl are needed"
  for b in "$(he_brand ServerBinary)" "$(he_brand UpdaterBinary)" "$(he_brand CLIBinary)"; do
    [ -x "${HE_BIN}/${b}" ] || he_die "${HE_BIN}/${b} is missing: run make build first"
  done
  [ -x "${HE_DEPLOY}/scripts/${HE_INSTALLER_NAME}" ] || he_die "${HE_DEPLOY}/scripts/${HE_INSTALLER_NAME} is missing"
  mkdir -p "${HE_WORK}"
  if [ ! -s "${HE_TLS}/ca.pem" ]; then
    he_ca_create || he_die "cannot create the throwaway CA (openssl)"
    he_log "throwaway CA created (${HE_TLS})"
  fi
  he_mirror_build || he_die "the mirror could not be built (see ${HE_WORK}/mirror-build.log)"
  he_https_start mirror "${HE_MIRROR}/srv/mirror" "${HE_PORT_MIRROR}" || he_die "the mirror server does not answer on port ${HE_PORT_MIRROR} (see ${HE_DIR}/mirror.log)"
  he_pass "mirror: collector and supervisor files of ${HE_VERSION_OLD} and ${HE_VERSION_NEW}, verified offline" "https://127.0.0.1:${HE_PORT_MIRROR}"
  if [ "$(he_curl -o /dev/null -w '%{http_code}' "https://127.0.0.1:${HE_PORT_MIRROR}/cmd/opampsupervisor/v${HE_VERSION_NEW}/opampsupervisor_${HE_VERSION_NEW}_linux_$(he_arch).sha256")" = 200 ]; then
    he_pass "mirror serves the supervisor in the layout cmd/opampsupervisor/v<version>/"
  else he_fail "mirror serves the supervisor in the layout cmd/opampsupervisor/v<version>/"; fi
}

# Ends a scenario: the fault injector, diagnostics after a failure, the servers, a reset of the host, the artifacts.
# After a failure the artifacts are collected twice. The first copy comes right after the diagnostics, before the part that can
# take long (stopping the servers waits up to 40 s, the reset runs the uninstaller): a runner that cancels the step, for example
# when the step runs out of time, gives the script only a few seconds before it kills it. The second copy, at the end, replaces
# the first with a complete one (he_collect_artifacts starts from an empty directory, so calling it again is safe).
he_finish_scenario() {
  [ "${HE_ACTIVE}" = 1 ] || return 0
  HE_ACTIVE=0
  he_fault_stop
  if [ "${HE_FAILED}" -gt 0 ] || [ "${HE_ABORTED:-0}" = 1 ]; then
    he_diag
    he_collect_artifacts || true
  fi
  he_stop_tracked
  if [ -n "$(he_host_leftovers)" ]; then he_reset_host quiet; fi
  he_collect_artifacts || true
  he_summary || true
}

he_on_exit() { # exit-status
  local rc="$1"
  if [ "${HE_ACTIVE}" = 1 ]; then
    if [ "${rc}" != 0 ]; then HE_ABORTED=1; he_fail "scenario ${HE_SCENARIO}" "ended early with status ${rc}"; fi
    he_finish_scenario
  fi
  if [ "${rc}" = 0 ] && [ "${HE_FAILED}" -gt 0 ]; then exit 1; fi
}

he_run_scenario() { # name function
  local name="$1" func="$2" rc
  he_init "${name}"
  HE_ABORTED=0
  trap 'he_on_exit $?' EXIT
  trap 'he_log "interrupted"; exit 130' INT TERM
  he_section "scenario ${name}"
  he_require_ci
  he_require_sudo
  he_require_fresh_host
  HE_ACTIVE=1
  he_prepare_shared
  "${func}"
  rc=$?
  # a scenario that stops early without a recorded failure still fails
  if [ "${rc}" != 0 ] && [ "${HE_FAILED}" = 0 ]; then he_fail "scenario ${name}" "stopped early with status ${rc}"; fi
  he_finish_scenario
  [ "${HE_FAILED}" = 0 ]
}
