#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Shared helpers of the kind probe (hack/kind). Sourced, not executed. bash 3.2 compatible (macOS): no associative
# arrays, no mapfile, no ${x,,}. Portable between macOS and Linux: no timeout, sed -i, readlink -f, date -d/-v.
#
# Sets the paths and defaults (all overridable through the environment), the logging and result helpers, the kubectl and
# helm wrappers (isolated kubeconfig and helm directories, context guard) and the manifest renderer. Prints nothing and
# changes nothing when sourced, except creating no files at all.

KIND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "${KIND_DIR}/../.." && pwd)"
export KIND_DIR ROOT

# ---- defaults ----------------------------------------------------------------------------------------------------
# versions.env: the pins. Variables that are already set win (KIND_NODE_IMAGE=... make kind-probe).
kind_load_versions() {
  local file line key
  for file in "${ROOT}/ci/versions.env" "${KIND_DIR}/versions.env"; do
    while IFS= read -r line || [ -n "${line}" ]; do
      case "${line}" in '' | '#'*) continue ;; esac
      key="${line%%=*}"
      if [ -z "${!key:-}" ]; then export "${key}=${line#*=}"; fi
    done < "${file}"
  done
}
kind_load_versions

: "${KIND_CLUSTER_NAME:=myriarch}"
: "${KIND_REGISTRY_NAME:=myriarch-kind-registry}"
: "${KIND_REGISTRY_PORT:=5001}"
: "${PROBE_WORK:=${ROOT}/.cache/kind}"
: "${PROBE_PORT_API:=28080}"
: "${PROBE_PORT_DEXAPI:=28081}"
: "${PROBE_PORT_DEX:=25554}"
: "${PROBE_SKIP:=}"
# failedRetryBackoff of the real updater in seconds (install.sh writes it into updater.yaml, stage 9 waits for it)
: "${UPDATER_RETRY_BACKOFF:=15}"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"
export KIND_CLUSTER_NAME KIND_REGISTRY_NAME KIND_REGISTRY_PORT PROBE_WORK PROBE_PORT_API PROBE_PORT_DEXAPI PROBE_PORT_DEX PROBE_SKIP TOOLS_BIN

# The pinned tools come first on the PATH (kind, kubectl, helm, crane, yq from .cache/bin).
case ":${PATH}:" in *":${TOOLS_BIN}:"*) ;; *) PATH="${TOOLS_BIN}:${PATH}" ;; esac
export PATH

# Isolation: nothing touches ~/.kube or ~/.config/helm.
export KUBECONFIG="${PROBE_WORK}/kubeconfig"
export HELM_CACHE_HOME="${PROBE_WORK}/helm/cache"
export HELM_CONFIG_HOME="${PROBE_WORK}/helm/config"
export HELM_DATA_HOME="${PROBE_WORK}/helm/data"
KIND_CONTEXT="kind-${KIND_CLUSTER_NAME}"

NS_APP=myriarch
NS_INFRA=probe-infra
NS_DEX=myriarch-dex
NS_DEXIDP=dex
NS_OUTSIDE=probe-outside
export NS_APP NS_INFRA NS_DEX NS_DEXIDP NS_OUTSIDE

# ---- output ------------------------------------------------------------------------------------------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BOLD=$'\033[1m'; C_OFF=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BOLD=""; C_OFF=""
fi

log()  { printf '%s [kind] %s\n' "$(date +%T)" "$*" >&2; }
warn() { printf '%s [kind] WARNING: %s\n' "$(date +%T)" "$*" >&2; }
die()  { printf '%s [kind] ERROR: %s\n' "$(date +%T)" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
need_cmd() {
  local c
  for c in "$@"; do have "${c}" || die "command '${c}' not found"; done
}

# ---- results -----------------------------------------------------------------------------------------------------
STAGE=0
CHECK=0
STAGE_TITLE=""
STAGE_START=0
RESULTS_FILE="${RESULTS_FILE:-${PROBE_WORK}/results.txt}"
DURATIONS_FILE="${DURATIONS_FILE:-${PROBE_WORK}/durations.txt}"
FAILED=0

results_init() {
  mkdir -p "${PROBE_WORK}"
  : > "${RESULTS_FILE}"
  : > "${DURATIONS_FILE}"
  FAILED=0
}

stage_begin() { # number title
  stage_end
  STAGE="$1"; CHECK=0; STAGE_TITLE="$2"; STAGE_START=${SECONDS}
  printf '\n%s== %s. %s ==%s\n' "${C_BOLD}" "$1" "$2" "${C_OFF}" >&2
}
stage_end() {
  if [ -n "${STAGE_TITLE}" ]; then
    printf '%s|%s|%s\n' "${STAGE}" "$((SECONDS - STAGE_START))" "${STAGE_TITLE}" >> "${DURATIONS_FILE}"
    STAGE_TITLE=""
  fi
}

# Evidence must never contain a secret: the callers pass counts, status words and names only.
_result() { # PASS|FAIL|SKIP name evidence
  local kind="$1" name="$2" ev="${3:-}" color
  CHECK=$((CHECK + 1))
  printf '%s|%s.%s|%s|%s\n' "${kind}" "${STAGE}" "${CHECK}" "${name}" "${ev}" >> "${RESULTS_FILE}"
  case "${kind}" in PASS) color="${C_GREEN}" ;; FAIL) color="${C_RED}"; FAILED=$((FAILED + 1)) ;; *) color="${C_YELLOW}" ;; esac
  # evidence: one line, at most 300 characters
  ev="$(printf '%s' "${ev}" | tr '\n' ' ' | cut -c1-300)"
  printf '  %s%-4s%s %s.%s %s%s\n' "${color}" "${kind}" "${C_OFF}" "${STAGE}" "${CHECK}" "${name}" "${ev:+  [${ev}]}" >&2
}
pass() { _result PASS "$1" "${2:-}"; }
fail() { _result FAIL "$1" "${2:-}"; }
skip() { _result SKIP "$1" "${2:-}"; }

check_eq() { # name want got
  if [ "$2" = "$3" ]; then pass "$1" "$3"; else fail "$1" "expected '$2', got '$3'"; fi
}
check_ne() { # name unwanted got
  if [ "$2" != "$3" ]; then pass "$1" "$3"; else fail "$1" "unexpected '$3'"; fi
}
check_ge() { # name min got
  if [ "${3:-}" -ge "$2" ] 2>/dev/null; then pass "$1" "$3 >= $2"; else fail "$1" "expected >= $2, got '${3:-}'"; fi
}
check_le() { # name max got
  if [ "${3:-}" -le "$2" ] 2>/dev/null; then pass "$1" "$3 <= $2"; else fail "$1" "expected <= $2, got '${3:-}'"; fi
}
check_contains() { # name needle haystack
  if printf '%s' "$3" | grep -qF -- "$2"; then pass "$1" "contains '$2'"; else fail "$1" "'$2' not found in: $(printf '%s' "$3" | head -c 200)"; fi
}
check_not_contains() { # name needle haystack
  if printf '%s' "$3" | grep -qF -- "$2"; then fail "$1" "'$2' found"; else pass "$1" "absent '$2'"; fi
}
check_true() { # name [command...]: the command succeeded
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "${name}"; else fail "${name}" "command failed: $*"; fi
}

# chart_content_state DIR_A DIR_B -> "identical", "different" or "error": the files of two chart directories decide, not the version in
# Chart.yaml (an old and a new chart can carry the same version until the version bump of a release). diff exits 1 for a difference and
# 2 for trouble (a missing directory, an unreadable file): only 1 is "different", so that a failing diff cannot pass check 7.3.
chart_content_state() {
  local rc=0
  diff -r -q "$1" "$2" >/dev/null 2>&1 || rc=$?
  case "${rc}" in
    0) echo identical ;;
    1) echo different ;;
    *) echo error ;;
  esac
}

skipped_by_key() { # key -> 0 when PROBE_SKIP lists it
  case ",${PROBE_SKIP}," in *",$1,"*) return 0 ;; esac
  return 1
}

# wait_until TIMEOUT 'expression' [POLL]
wait_until() {
  local expr="$2" poll="${3:-1}" end=$((SECONDS + $1))
  while [ "${SECONDS}" -lt "${end}" ]; do
    if eval "${expr}" >/dev/null 2>&1; then return 0; fi
    sleep "${poll}"
  done
  return 1
}

# ---- kubectl and helm --------------------------------------------------------------------------------------------
# Every call goes through the isolated kubeconfig and the context guard: a probe never touches another cluster.
assert_context() {
  [ -f "${KUBECONFIG}" ] || die "kubeconfig ${KUBECONFIG} is missing (hack/kind/up.sh)"
  local cur
  cur="$(kubectl config current-context 2>/dev/null || true)"
  [ "${cur}" = "${KIND_CONTEXT}" ] || die "current context is '${cur}', expected '${KIND_CONTEXT}' (KUBECONFIG=${KUBECONFIG})"
}
k() { kubectl --context "${KIND_CONTEXT}" "$@"; }
kn() { local ns="$1"; shift; kubectl --context "${KIND_CONTEXT}" -n "${ns}" "$@"; }
h() { helm --kube-context "${KIND_CONTEXT}" "$@"; }

# ---- files and secrets -------------------------------------------------------------------------------------------
SECRETS_DIR="${PROBE_WORK}/secrets"
prepare_work_dirs() {
  mkdir -p "${PROBE_WORK}"
  chmod 700 "${PROBE_WORK}"
  : > "${PROBE_WORK}/.myriarch-kind-work"   # marker: down.sh --all removes only a directory that carries it
  mkdir -p "${SECRETS_DIR}" "${PROBE_WORK}/helm/cache" "${PROBE_WORK}/helm/config" "${PROBE_WORK}/helm/data" \
    "${PROBE_WORK}/logs" "${PROBE_WORK}/render" "${PROBE_WORK}/trust"
  chmod 700 "${SECRETS_DIR}"
}

sha256_file() { # file
  if have shasum; then shasum -a 256 "$1" | awk '{print $1}'
  elif have sha256sum; then sha256sum "$1" | awk '{print $1}'
  else openssl dgst -sha256 -r "$1" | awk '{print $1}'; fi
}

sha256_stdin() { # stdin -> hex digest
  if have shasum; then shasum -a 256 | awk '{print $1}'
  elif have sha256sum; then sha256sum | awk '{print $1}'
  else openssl dgst -sha256 -r | awk '{print $1}'; fi
}

# openssl: prefer the Homebrew one on macOS (LibreSSL lacks options such as -addext handling and -copy_extensions).
openssl_bin() {
  if [ -x /opt/homebrew/bin/openssl ]; then echo /opt/homebrew/bin/openssl; else command -v openssl; fi
}

# random_token LENGTH -> alphanumeric string (not printed by the probe; written to files only)
random_token() {
  python3 -I -c 'import secrets,string,sys; a=string.ascii_letters+string.digits; print("".join(secrets.choice(a) for _ in range(int(sys.argv[1]))))' "$1"
}

# ---- manifests ---------------------------------------------------------------------------------------------------
# render_manifest SRC VAR... : prints SRC with @@VAR@@ replaced by the value of the shell variable VAR. Non-secret values only
# (the manifests carry images, names and numbers; secrets are mounted from Kubernetes Secrets).
render_manifest() {
  local src="$1" name val
  shift
  local args=()
  for name in "$@"; do
    val="${!name:-}"
    [ -n "${val}" ] || die "render_manifest ${src##*/}: ${name} is empty"
    val="$(printf '%s' "${val}" | sed -e 's/[\\&#]/\\&/g')"
    args+=(-e "s#@@${name}@@#${val}#g")
  done
  local out
  out="$(sed "${args[@]}" "${src}")"
  if printf '%s\n' "${out}" | grep -q '@@[A-Z_]*@@'; then
    die "render_manifest ${src##*/}: unreplaced placeholder: $(printf '%s\n' "${out}" | grep -o '@@[A-Z_]*@@' | sort -u | tr '\n' ' ')"
  fi
  printf '%s\n' "${out}"
}

# ---- misc --------------------------------------------------------------------------------------------------------
# Node architecture as seen by Docker (the kind nodes run linux/<this>).
docker_platform() {
  case "$(docker info --format '{{.Architecture}}' 2>/dev/null)" in
    aarch64 | arm64) echo linux/arm64 ;;
    x86_64 | amd64) echo linux/amd64 ;;
    *) return 1 ;;
  esac
}

# date_plus_days N -> YYYY-MM-DD (UTC), computed by python: BSD and GNU date differ
date_plus_days() {
  python3 -I -c 'import datetime,sys; print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=int(sys.argv[1]))).strftime("%Y-%m-%d"))' "$1"
}
