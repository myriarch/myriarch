#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Kubernetes probe of the whole system on kind: builds the cluster, the images and the installation from zero and runs the
# numbered checks (PASS / FAIL / SKIP) of the stages listed below. No prompts, no sudo, no /etc/hosts edits, no browser.
#
#   hack/kind/probe.sh --fresh     remove cluster, registry and work directory, then everything (make kind-probe)
#   hack/kind/probe.sh --reset     keep cluster and registry, delete the probe namespaces, then images and all stages
#   hack/kind/probe.sh             all stages; requires hack/kind/up.sh and fails when the probe namespaces already exist
#
# Stages: 0 prerequisites, 1 cluster, 2 images, 3 infrastructure, 4 chart with only the required values, 5 bootstrap admin,
# 6 full configuration, 7 agents, 8 configuration rollout, 9 version update through the UI calls (failure and automatic
# rollback, retry, pod restart, back to the Git target), 10 leader takeover, 11 helm upgrade with a new expiresAt,
# 12 bootstrap token rotation, 13 NetworkPolicy, 14 Dex quickstart, 15 logs and secret hygiene.
#
# Environment (defaults in hack/kind/README.md):
#   PROBE_SKIP=rotation,netpol,dex,downgrade   skip optional parts (each skip is reported as SKIP with its reason)
#   PROBE_STAGES="4 5 6"                       run only these stages against the existing state (development)
#   PROBE_REUSE_IMAGES=1                       keep the last images.env while iterating
#   ALLOW_DIRTY=1                              build the server image from a dirty work tree (never in the final proof)
#   WEB_BUILD=auto|always|never                web UI build for the server image (--fresh uses always)
#   PROBE_PORT_API / PROBE_PORT_DEXAPI / PROBE_PORT_DEX   local ports of the port-forwards (28080 / 28081 / 25554)
#   KIND_CLUSTER_NAME, KIND_REGISTRY_NAME, KIND_REGISTRY_PORT, KIND_NODE_IMAGE, PROBE_WORK
#
# Output: $PROBE_WORK/probe.log (everything), $PROBE_WORK/results.txt (PASS|N.M|name|evidence), diagnostics in
# $PROBE_WORK/diag/<timestamp>/ when a check fails. Exit status 0 only if no check failed. The cluster is never deleted.
set -uo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"
# shellcheck source=hack/kind/lib/stages-core.sh
. "${KIND_DIR}/lib/stages-core.sh"
# shellcheck source=hack/kind/lib/stages-update.sh
. "${KIND_DIR}/lib/stages-update.sh"
# shellcheck source=hack/kind/lib/stages-extra.sh
. "${KIND_DIR}/lib/stages-extra.sh"
cd "${ROOT}" || exit 1

MODE=""
case "${1:-}" in
  "") ;;
  --fresh | --reset) MODE="$1" ;;
  --inner) ;;
  -h | --help) sed -n '2,/^set -uo pipefail$/p' "$0" | sed -e '/^set -uo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) die "usage: hack/kind/probe.sh [--fresh | --reset]" ;;
esac

PROBE_NAMESPACES="${NS_APP} ${NS_DEX} ${NS_DEXIDP} ${NS_INFRA} ${NS_OUTSIDE}"

# ---- outer part: modes, log file -------------------------------------------------------------------------------------
if [ "${1:-}" != --inner ]; then
  case "${MODE}" in
    --fresh)
      "${KIND_DIR}/down.sh" --all || exit 1
      export WEB_BUILD="${WEB_BUILD:-always}"
      ;;
    --reset)
      assert_context
      log "resetting the probe namespaces (cluster and registry stay)"
      pf_stop_all
      coredns_remove_rewrite >/dev/null 2>&1 || true
      for ns in ${PROBE_NAMESPACES}; do k delete namespace "${ns}" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true; done
      rm -f "${PROBE_WORK}"/cookies-* "${PROBE_WORK}"/state.env "${PROBE_WORK}"/pf-*.pid "${PROBE_WORK}"/values-tokens.yaml "${PROBE_WORK}"/phases-*.txt
      ;;
    "")
      if [ -z "${PROBE_STAGES:-}" ] && [ -f "${KUBECONFIG}" ] && kubectl --context "${KIND_CONTEXT}" get namespace "${NS_APP}" >/dev/null 2>&1; then
        die "the probe namespaces already exist: run hack/kind/probe.sh --reset (or --fresh), or set PROBE_STAGES to continue"
      fi
      ;;
  esac
  prepare_work_dirs
  # everything the inner run prints goes to the console and to probe.log
  PROBE_INNER_ARGS=(--inner)
  "$0" "${PROBE_INNER_ARGS[@]}" 2>&1 | tee -a "${PROBE_WORK}/probe.log"
  exit "${PIPESTATUS[0]}"
fi

# ---- inner part: stages -----------------------------------------------------------------------------------------------
prepare_work_dirs
TOTAL_START=${SECONDS}
if [ -z "${PROBE_STAGES:-}" ]; then results_init; fi
STAGES_TO_RUN="${PROBE_STAGES:-0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15}"
if [ -n "${PROBE_STAGES:-}" ]; then
  : > "${PROBE_WORK}/results-partial.txt"
  RESULTS_FILE="${PROBE_WORK}/results-partial.txt"
  DURATIONS_FILE="${PROBE_WORK}/durations-partial.txt"
  : > "${DURATIONS_FILE}"
  state_load
fi

probe_cleanup() {
  local rc=$?
  trap - EXIT
  stage_end
  fault_injector_stop >/dev/null 2>&1 || true
  if [ "${FAILED}" -gt 0 ] && [ -z "${DIAG_DONE:-}" ]; then
    "${KIND_DIR}/diag.sh" >&2 || true
  fi
  pf_stop_all
  exit "${rc}"
}
trap probe_cleanup EXIT
trap 'log "interrupted"; exit 130' INT TERM

summary() {
  local pass fail skip
  pass="$(grep -c '^PASS|' "${RESULTS_FILE}" || true)"
  fail="$(grep -c '^FAIL|' "${RESULTS_FILE}" || true)"
  skip="$(grep -c '^SKIP|' "${RESULTS_FILE}" || true)"
  printf '\n%s== summary ==%s\n' "${C_BOLD}" "${C_OFF}"
  printf 'PASS %s   FAIL %s   SKIP %s   (%ss)\n' "${pass}" "${fail}" "${skip}" "$((SECONDS - TOTAL_START))"
  if [ "${fail}" -gt 0 ]; then
    printf '\nFailed checks:\n'
    grep '^FAIL|' "${RESULTS_FILE}" | while IFS='|' read -r _ id name ev; do printf '  %s %s  [%s]\n' "${id}" "${name}" "${ev}"; done
  fi
  if [ "${skip}" -gt 0 ]; then
    printf '\nSkipped checks:\n'
    grep '^SKIP|' "${RESULTS_FILE}" | while IFS='|' read -r _ id name ev; do printf '  %s %s  [%s]\n' "${id}" "${name}" "${ev}"; done
  fi
  printf '\nStage durations:\n'
  while IFS='|' read -r n secs title; do printf '  %2s  %5ss  %s\n' "${n}" "${secs}" "${title}"; done < "${DURATIONS_FILE}"
  printf '\nVersions: kind %s, Kubernetes %s, kubectl %s, helm %s\n' "$(kind version 2>/dev/null | awk '{print $2}')" "$(state_get K8S_VERSION)" \
    "$(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion)" "$(helm version --short 2>/dev/null)"
  printf 'Node image: %s\n' "${KIND_NODE_IMAGE}"
  printf 'Images: server %s, collector %s\n' "$(state_get SERVER_DIGEST)" "$(state_get COLLECTOR_DIGEST)"
  printf 'Git: HEAD %s, work tree %s\n' "$(git -C "${ROOT}" rev-parse --short HEAD)" "$([ -z "$(git -C "${ROOT}" status --porcelain)" ] && echo clean || echo dirty)"
  if [ -n "$(state_get LEADER_TAKEOVER_SECONDS)" ]; then printf 'Leader takeover: %ss\n' "$(state_get LEADER_TAKEOVER_SECONDS)"; fi
}

# probe_fatal: a failure after which later stages make no sense
probe_fatal() {
  stage_end
  "${KIND_DIR}/diag.sh" >&2 || true
  DIAG_DONE=1
  summary
  exit 1
}

for n in ${STAGES_TO_RUN}; do
  "stage_${n}"
done
stage_end
summary
[ "${FAILED}" = 0 ]
