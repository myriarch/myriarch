#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# REST access of the kind probe (sourced after lib/common.sh): port-forward lifecycle, session cookies, the calls the UI
# makes, and the rollout observation helpers (the same logic as scripts/e2e-ui-updates.sh).
#
# Two API targets exist: "main" (release myriarch in namespace myriarch, local port PROBE_PORT_API) and "dex" (the Dex
# quickstart release, PROBE_PORT_DEXAPI). api_use selects the target for a() and ap(). Each has its own cookie jar and
# CSRF token. The port-forward is restarted when its pod goes away (pods are deleted on purpose in several stages).
# Secrets (tokens) are read from files and never appear on a command line or in output.


# per-target state (no associative arrays: plain variables with the target as suffix)
API_TARGET=main
API_BASE=""
API_JAR=""
API_CSRF=""
API_NS=""
API_SVC=""
API_PORT=""

api_use() { # main|dex
  API_TARGET="$1"
  case "$1" in
    main) API_NS="${NS_APP}";    API_SVC="svc/myriarch"; API_PORT="${PROBE_PORT_API}" ;;
    dex)  API_NS="${NS_DEX}";    API_SVC="svc/myriarch"; API_PORT="${PROBE_PORT_DEXAPI}" ;;
    *) die "api_use: unknown target $1" ;;
  esac
  API_BASE="http://127.0.0.1:${API_PORT}"
  API_JAR="${PROBE_WORK}/cookies-$1"
  API_CSRF=""
  if [ -f "${API_JAR}" ]; then API_CSRF="$(csrf_from_jar "${API_JAR}")"; fi
}

csrf_from_jar() { awk '$6=="myriarch_csrf"{print $7}' "$1" 2>/dev/null | tail -n 1; }

# ---- port-forward ------------------------------------------------------------------------------------------------
_pf_pid_file() { printf '%s/pf-%s.pid' "${PROBE_WORK}" "$1"; }

pf_start() { # name namespace target local-port remote-port
  local name="$1" ns="$2" target="$3" lport="$4" rport="$5" pidf
  pidf="$(_pf_pid_file "${name}")"
  pf_stop "${name}"
  # nothing else may listen on the local port
  if (exec 3<>"/dev/tcp/127.0.0.1/${lport}") 2>/dev/null; then
    warn "port ${lport} is already in use; the port-forward ${name} cannot start"
    return 1
  fi
  kubectl --context "${KIND_CONTEXT}" -n "${ns}" port-forward "${target}" "${lport}:${rport}" --address 127.0.0.1 \
    > "${PROBE_WORK}/logs/portforward-${name}.log" 2>&1 &
  echo $! > "${pidf}"
  wait_until 30 "(exec 3<>/dev/tcp/127.0.0.1/${lport})" 0.5
}

pf_stop() { # name
  local pidf pid
  pidf="$(_pf_pid_file "$1")"
  if [ -f "${pidf}" ]; then
    pid="$(cat "${pidf}")"
    if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then kill "${pid}" 2>/dev/null || true; wait "${pid}" 2>/dev/null || true; fi
    rm -f "${pidf}"
  fi
}

pf_stop_all() {
  local f
  for f in "${PROBE_WORK}"/pf-*.pid; do
    [ -e "${f}" ] || continue
    pf_stop "$(basename "${f}" .pid | sed 's/^pf-//')"
  done
}

# pf_ensure: the port-forward of the current API target is alive and /readyz answers (restart otherwise)
pf_ensure() {
  local pidf pid
  pidf="$(_pf_pid_file "api-${API_TARGET}")"
  pid="$(cat "${pidf}" 2>/dev/null || true)"
  if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null && curl -s -m 3 -o /dev/null -w '%{http_code}' "${API_BASE}/readyz" 2>/dev/null | grep -q '^200$'; then
    return 0
  fi
  pf_start "api-${API_TARGET}" "${API_NS}" "${API_SVC}" "${API_PORT}" 8080 || return 1
  wait_until 90 "curl -s -m 3 -o /dev/null -w '%{http_code}' ${API_BASE}/readyz | grep -q '^200\$'" 1
}

# pf_restart: a fresh port-forward to a current pod (a forward stays connected to a pod that is terminating after a rollout)
pf_restart() {
  pf_stop "api-${API_TARGET}"
  pf_ensure
}

# app_rollout_wait NAMESPACE: waits for the rollout of deploy/myriarch, then until no old pod is left terminating, and
# restarts the port-forward of the current API target (so that it never talks to a pod that is about to go away)
app_rollout_wait() {
  local ns="$1" want
  kn "${ns}" rollout status deploy/myriarch --timeout=300s >/dev/null 2>&1 || return 1
  want="$(kn "${ns}" get deploy myriarch -o jsonpath='{.spec.replicas}')"
  wait_until 120 "[ \"\$(kubectl --context ${KIND_CONTEXT} -n ${ns} get pods -l app.kubernetes.io/instance=myriarch --no-headers 2>/dev/null | wc -l | tr -d ' ')\" = '${want}' ]" 2 || return 1
  pf_restart
}

# ---- calls -------------------------------------------------------------------------------------------------------
# a PATH [curl args...]: GET with the session cookie; one retry after restarting the port-forward when the connection fails
a() {
  local path="$1" out rc
  shift
  out="$(curl -s -m 20 -b "${API_JAR}" "${API_BASE}/api/v1${path}" "$@")"; rc=$?
  if [ "${rc}" != 0 ]; then
    pf_ensure >/dev/null 2>&1 || true
    out="$(curl -s -m 20 -b "${API_JAR}" "${API_BASE}/api/v1${path}" "$@")"; rc=$?
  fi
  printf '%s' "${out}"
  return "${rc}"
}

# ap PATH [JSON]: POST with CSRF token; the body defaults to {}
ap() {
  local path="$1" body="${2:-}" out rc
  [ -n "${body}" ] || body='{}'
  out="$(curl -s -m 30 -b "${API_JAR}" -H "X-CSRF-Token: ${API_CSRF}" -H 'Content-Type: application/json' -X POST "${API_BASE}/api/v1${path}" -d "${body}")"; rc=$?
  if [ "${rc}" != 0 ]; then
    pf_ensure >/dev/null 2>&1 || true
    out="$(curl -s -m 30 -b "${API_JAR}" -H "X-CSRF-Token: ${API_CSRF}" -H 'Content-Type: application/json' -X POST "${API_BASE}/api/v1${path}" -d "${body}")"; rc=$?
  fi
  printf '%s' "${out}"
  return "${rc}"
}

# api_status METHOD PATH [JSON]: HTTP status code of a call with the session (no body printed)
api_status() {
  local method="$1" path="$2" body="${3:-}"
  [ -n "${body}" ] || body='{}'
  if [ "${method}" = GET ]; then
    curl -s -m 20 -o /dev/null -w '%{http_code}' -b "${API_JAR}" "${API_BASE}/api/v1${path}"
  else
    curl -s -m 30 -o /dev/null -w '%{http_code}' -b "${API_JAR}" -H "X-CSRF-Token: ${API_CSRF}" -H 'Content-Type: application/json' \
      -X "${method}" "${API_BASE}/api/v1${path}" -d "${body}"
  fi
}

# api_code PATH: HTTP status of an unauthenticated GET of any path on the target (/healthz, /readyz, /)
http_code() { curl -s -m 10 -o /dev/null -w '%{http_code}' "${API_BASE}$1"; }

# bootstrap_login TOKENFILE JAR -> HTTP status; the token is read from the file and sent on stdin (never on the command line)
bootstrap_login() {
  local tokenfile="$1" jar="$2"
  jq -Rn --rawfile t "${tokenfile}" '{token:($t|rtrimstr("\n"))}' \
    | curl -s -m 20 -o /dev/null -w '%{http_code}' -c "${jar}" -H 'Content-Type: application/json' -d @- "${API_BASE}/api/v1/auth/bootstrap-login"
}

# session_from_jar: sets the CSRF token of the current target from its cookie jar
session_refresh() { API_CSRF="$(csrf_from_jar "${API_JAR}")"; }

# ---- queries used by several stages ------------------------------------------------------------------------------
sysinfo() { a /system/info; }
leader_id() { sysinfo | jq -r '.leaderId // empty'; }
agent_id_by_site() { a "/agents?attr=site.id%3D$1&kind=collector" | jq -r '.items[0].id // empty'; }
updater_id_of() { a "/agents/$1" | jq -r '.agent.updater.agentId // empty'; }
audit_count() { # action [result]  -> number of matching audit events (limit 200)
  local q="/audit?action=$1&limit=200"
  a "${q}" | jq --arg r "${2:-}" '[.items[]? | select($r == "" or .result == $r)] | length'
}

# ---- rollout observation (scripts/e2e-ui-updates.sh) -------------------------------------------------------------
# latest_rollout FLEET [KIND] -> id of the newest rollout of the kind (default version)
latest_rollout() {
  a "/rollouts?kind=${2:-version}&fleet=$1&limit=50" | jq -r '[.items[]?]|sort_by(.createdAt)|last|.id // empty'
}

# watch_rollout ID TIMEOUT -> echoes the final status; records the distinct phases of the first target (every 0.5 s) in
# $PROBE_WORK/phases-<id>.txt
watch_rollout() {
  local id="$1" end=$((SECONDS + $2)) st phase last=""
  : > "${PROBE_WORK}/phases-${id}.txt"
  while [ "${SECONDS}" -lt "${end}" ]; do
    st="$(a "/rollouts/${id}" | jq -r '.status // empty')"
    phase="$(a "/rollouts/${id}/targets" | jq -r '.items[0].progress.phase // empty')"
    if [ -n "${phase}" ] && [ "${phase}" != "${last}" ]; then echo "${phase}" >> "${PROBE_WORK}/phases-${id}.txt"; last="${phase}"; fi
    case "${st}" in completed | paused | failed | aborted | rolled_back | superseded) echo "${st}"; return 0 ;; esac
    sleep 0.5
  done
  echo "${st}"
  return 1
}
phases_of() { [ -f "${PROBE_WORK}/phases-$1.txt" ] && tr '\n' ' ' < "${PROBE_WORK}/phases-$1.txt" | sed 's/ $//'; return 0; }
steps_of() { a "/rollouts/$1/targets" | jq -r '[.items[0].progress.steps[]? | "\(.name):\(.status)"] | join(",")'; }
step_status() { a "/rollouts/$1/targets" | jq -r --arg s "$2" '[.items[0].progress.steps[]? | select(.name==$s) | .status][0] // "absent"'; }

# wait_rollouts KIND TIMEOUT -> 0 when no rollout of the kind is active; 1 on timeout; 2 when a rollout is paused, failed or
# rolling back (needs attention). Waves that need an approval are not used by the probe policies.
wait_rollouts() {
  local kind="$1" end=$((SECONDS + $2)) act bad
  while [ "${SECONDS}" -lt "${end}" ]; do
    act="$(a "/rollouts?kind=${kind}&active=true&limit=200")"
    bad="$(printf '%s' "${act}" | jq -r '[.items[]? | select(.status=="paused" or .status=="failed" or .status=="rolling_back")] | length')"
    if [ "${bad:-0}" -gt 0 ] 2>/dev/null; then return 2; fi
    if [ "$(printf '%s' "${act}" | jq -r '.items | length')" = 0 ]; then return 0; fi
    sleep 3
  done
  return 1
}
