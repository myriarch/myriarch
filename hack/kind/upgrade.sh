#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Upgrade test on kind: installs the published server chart and image of the previous minor release (pins in hack/kind/versions.env),
# verifies their digests and cosign signatures first, creates fleets, agents with assignments and audit entries, upgrades the release
# to the chart and the image of this commit and proves that nothing is lost: fleets and assignments must be unchanged (fields that the
# new version adds are allowed), every earlier audit entry must still be there unchanged, and the agents must be online again without a
# restart. A full run needs Docker (kind) and network access to ghcr.io and Sigstore; without Docker only `go test ./hack/kind/` runs.
#
#   hack/kind/upgrade.sh --fresh     remove cluster, registry and work directory, then everything
#   hack/kind/upgrade.sh --reset     keep cluster and registry, delete the namespaces of the test, then images and all stages
#   hack/kind/upgrade.sh             all stages; requires hack/kind/up.sh and fails when the namespace of the app already exists
#
# Stages: 0 prerequisites, 1 cluster, 2 images of the commit, 3 digests and signatures of the previous release, 4 infrastructure,
# 5 install of the previous release, 6 data (agents, fleets, audit entries), 7 state before, 8 upgrade, 9 comparison.
#
# Tools: kind, kubectl, helm and cosign (also crane, jq, curl and docker) must be on the PATH or in .cache/bin (make tools). The script
# stops with one line that names the first missing tool.
#
# Environment (defaults in hack/kind/README.md):
#   UPGRADE_KUBERNETES=current|previous   Kubernetes version: KIND_NODE_IMAGE (default) or KIND_NODE_IMAGE_PREVIOUS of versions.env
#   WEB_BUILD=auto|always|never           web UI build for the server image (--fresh uses always)
#   ALLOW_DIRTY=1                         build the server image from a dirty work tree (never in the final proof)
#   PROBE_PORT_API, KIND_CLUSTER_NAME, KIND_REGISTRY_NAME, KIND_REGISTRY_PORT, PROBE_WORK, TOOLS_BIN
#
# Output: $PROBE_WORK/upgrade.log (everything), $PROBE_WORK/upgrade-results.txt (PASS|N.M|name|evidence), the captured state in
# $PROBE_WORK/upgrade/before and $PROBE_WORK/upgrade/after, diagnostics in $PROBE_WORK/diag/<timestamp>/ when a check fails. Exit
# status 0 only if no check failed. The cluster is never deleted.
set -uo pipefail

# ---- start: builtins only until the tools are checked ------------------------------------------------------------------------
case "${BASH_SOURCE[0]}" in
  */*) UPGRADE_SELF_DIR="$(cd "${BASH_SOURCE[0]%/*}" && pwd)" ;;
  *) UPGRADE_SELF_DIR="$(pwd)" ;;
esac
# shellcheck source=hack/kind/lib/upgrade.sh
. "${UPGRADE_SELF_DIR}/lib/upgrade.sh"

MODE=""
case "${1:-}" in
  "") ;;
  --fresh | --reset) MODE="$1" ;;
  --inner) ;;
  -h | --help) sed -n '5,/^set -uo pipefail$/p' "$0" | sed -e '/^set -uo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) printf 'usage: hack/kind/upgrade.sh [--fresh | --reset]\n' >&2; exit 2 ;;
esac

UPGRADE_ROOT="${UPGRADE_SELF_DIR%/*}"
UPGRADE_ROOT="${UPGRADE_ROOT%/*}"
TOOLS_BIN="${TOOLS_BIN:-${UPGRADE_ROOT}/.cache/bin}"
PATH="${TOOLS_BIN}:${PATH}"
export TOOLS_BIN PATH
upgrade_require_tools kind kubectl helm cosign crane jq curl docker || exit 1

PROBE_WORK="${PROBE_WORK:-${UPGRADE_ROOT}/.cache/kind}"
export PROBE_WORK
RESULTS_FILE="${PROBE_WORK}/upgrade-results.txt"
DURATIONS_FILE="${PROBE_WORK}/upgrade-durations.txt"
# shellcheck source=hack/kind/lib/common.sh
. "${UPGRADE_SELF_DIR}/lib/common.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"
cd "${ROOT}" || exit 1

# ---- pins ----------------------------------------------------------------------------------------------------------------------
UPGRADE_DIR="${PROBE_WORK}/upgrade"

split_pin() { # NAME REF PREFIX: sets PREFIX_REPO, PREFIX_TAG and PREFIX_DIGEST
  local out
  out="$(upgrade_split_ref "$2")" || die "$1 is not a valid pin"
  { IFS= read -r "$3_REPO"; IFS= read -r "$3_TAG"; IFS= read -r "$3_DIGEST"; } <<< "${out}"
}
split_pin UPGRADE_PREVIOUS_CHART "${UPGRADE_PREVIOUS_CHART:-}" PREV_CHART
split_pin UPGRADE_PREVIOUS_SERVER_IMAGE "${UPGRADE_PREVIOUS_SERVER_IMAGE:-}" PREV_IMAGE
[ -n "${UPGRADE_PREVIOUS_VERSION:-}" ] || die "UPGRADE_PREVIOUS_VERSION is missing in hack/kind/versions.env"
[ "${PREV_CHART_TAG}" = "${UPGRADE_PREVIOUS_VERSION}" ] || die "the tag of UPGRADE_PREVIOUS_CHART (${PREV_CHART_TAG}) is not UPGRADE_PREVIOUS_VERSION (${UPGRADE_PREVIOUS_VERSION})"
[ "${PREV_IMAGE_TAG}" = "${UPGRADE_PREVIOUS_VERSION}" ] || die "the tag of UPGRADE_PREVIOUS_SERVER_IMAGE (${PREV_IMAGE_TAG}) is not UPGRADE_PREVIOUS_VERSION (${UPGRADE_PREVIOUS_VERSION})"

case "${UPGRADE_KUBERNETES:-current}" in
  current) K8S_PIN="${KIND_NODE_IMAGE:-}" ;;
  previous) K8S_PIN="${KIND_NODE_IMAGE_PREVIOUS:-}" ;;
  *) die "UPGRADE_KUBERNETES must be current or previous" ;;
esac
K8S_VERSION_PIN="$(upgrade_node_version "${K8S_PIN}")" || die "the node image pin for UPGRADE_KUBERNETES=${UPGRADE_KUBERNETES:-current} is not valid"
KIND_NODE_IMAGE="${K8S_PIN}"
export KIND_NODE_IMAGE

# ---- outer part: modes, log file -----------------------------------------------------------------------------------------------
if [ "${1:-}" != --inner ]; then
  case "${MODE}" in
    --fresh)
      "${KIND_DIR}/down.sh" --all || exit 1
      export WEB_BUILD="${WEB_BUILD:-always}"
      ;;
    --reset)
      assert_context
      log "resetting the namespaces of the upgrade test (cluster and registry stay)"
      pf_stop_all
      for ns in "${NS_APP}" "${NS_INFRA}"; do k delete namespace "${ns}" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true; done
      rm -f "${PROBE_WORK}"/cookies-* "${PROBE_WORK}"/state.env "${PROBE_WORK}"/pf-*.pid "${PROBE_WORK}"/values-tokens.yaml
      rm -rf "${UPGRADE_DIR}"
      ;;
    "")
      if [ -f "${KUBECONFIG}" ] && kubectl --context "${KIND_CONTEXT}" get namespace "${NS_APP}" >/dev/null 2>&1; then
        die "the namespace ${NS_APP} already exists: run hack/kind/upgrade.sh --reset (or --fresh)"
      fi
      ;;
  esac
  prepare_work_dirs
  # everything the inner run prints goes to the console and to upgrade.log
  "${UPGRADE_SELF_DIR}/upgrade.sh" --inner 2>&1 | tee -a "${PROBE_WORK}/upgrade.log"
  exit "${PIPESTATUS[0]}"
fi

# ---- inner part: stages --------------------------------------------------------------------------------------------------------
prepare_work_dirs
mkdir -p "${UPGRADE_DIR}"
TOTAL_START=${SECONDS}
results_init
UPGRADE_COMPARE_RC=0

upgrade_cleanup() {
  local rc=$?
  trap - EXIT
  stage_end
  if [ "${FAILED}" -gt 0 ] && [ -z "${DIAG_DONE:-}" ]; then
    "${KIND_DIR}/diag.sh" >&2 || true
  fi
  pf_stop_all
  exit "${rc}"
}
trap upgrade_cleanup EXIT
trap 'log "interrupted"; exit 130' INT TERM

upgrade_summary() {
  local pass fail
  pass="$(grep -c '^PASS|' "${RESULTS_FILE}" || true)"
  fail="$(grep -c '^FAIL|' "${RESULTS_FILE}" || true)"
  printf '\n%s== summary ==%s\n' "${C_BOLD}" "${C_OFF}"
  printf 'PASS %s   FAIL %s   (%ss)\n' "${pass}" "${fail}" "$((SECONDS - TOTAL_START))"
  if [ "${fail}" -gt 0 ]; then
    printf '\nFailed checks:\n'
    grep '^FAIL|' "${RESULTS_FILE}" | while IFS='|' read -r _ id name ev; do printf '  %s %s  [%s]\n' "${id}" "${name}" "${ev}"; done
  fi
  printf '\nStage durations:\n'
  while IFS='|' read -r n secs title; do printf '  %2s  %5ss  %s\n' "${n}" "${secs}" "${title}"; done < "${DURATIONS_FILE}"
  printf '\nUpgrade: %s to the chart of this commit, Kubernetes %s\n' "${UPGRADE_PREVIOUS_VERSION}" "${K8S_VERSION_PIN}"
  printf 'Node image: %s\n' "${KIND_NODE_IMAGE}"
  printf 'Git: HEAD %s, work tree %s\n' "$(git -C "${ROOT}" rev-parse --short HEAD)" "$([ -z "$(git -C "${ROOT}" status --porcelain)" ] && echo clean || echo dirty)"
}

# upgrade_abort: a failure after which later stages make no sense
upgrade_abort() {
  stage_end
  "${KIND_DIR}/diag.sh" >&2 || true
  DIAG_DONE=1
  upgrade_summary
  exit 1
}
upgrade_fatal() { fail "$1" "${2:-}"; upgrade_abort; }

chart_name="$(sed -n 's/^name: *//p' "${CHART_DIR}/Chart.yaml" | head -n 1 | tr -d '"')"
chart_version="$(sed -n 's/^version: *//p' "${CHART_DIR}/Chart.yaml" | head -n 1 | tr -d '"')"

# release_chart: chart and version of the release myriarch, as helm list prints them
release_chart() { h list -n "${NS_APP}" -o json 2>/dev/null | jq -r '.[] | select(.name == "myriarch") | .chart'; }

# values_args IMAGE_REPO IMAGE_DIGEST: the values of the release; images only by digest, never by tag; never --reuse-values
values_args() {
  UPGRADE_VALUES=(--set "config.server.publicUrl=${PUBLIC_URL}" --set "image.repository=$1" --set "image.digest=$2"
    -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml")
}

login_bootstrap() { # reads the current bootstrap token and signs in; prints the HTTP status
  upgrade_read_bootstrap_token "$(secret_path bootstrap.token)" || { echo "no token"; return 0; }
  rm -f "${API_JAR}"
  ( umask 077; : > "${API_JAR}" )
  bootstrap_login "$(secret_path bootstrap.token)" "${API_JAR}"
}

# ---- stage 0 -------------------------------------------------------------------------------------------------------------------
stage_begin 0 "Prerequisites"
if docker info >/dev/null 2>&1; then pass "Docker engine reachable" "$(docker info --format '{{.ServerVersion}} {{.Architecture}}')"; else upgrade_fatal "Docker engine reachable" "start Docker"; fi
missing=""
for c in git python3 openssl ssh-keygen go make tar; do have "${c}" || missing="${missing} ${c}"; done
if [ -z "${missing}" ]; then pass "required commands present"; else upgrade_fatal "required commands present" "missing:${missing}"; fi
pass "pins valid" "previous release ${UPGRADE_PREVIOUS_VERSION}, Kubernetes ${K8S_VERSION_PIN} (UPGRADE_KUBERNETES=${UPGRADE_KUBERNETES:-current})"
if platform="$(docker_platform)"; then pass "node platform" "${platform}"; else upgrade_fatal "node platform" "unknown architecture of the Docker engine"; fi

# ---- stage 1 -------------------------------------------------------------------------------------------------------------------
stage_begin 1 "Cluster and registry"
if "${KIND_DIR}/up.sh" >"${PROBE_WORK}/logs/up.log" 2>&1; then pass "up.sh (cluster and registry)" "cluster ${KIND_CLUSTER_NAME}"; else cat "${PROBE_WORK}/logs/up.log" >&2; upgrade_fatal "up.sh (cluster and registry)" "see ${PROBE_WORK}/logs/up.log"; fi
assert_context
nodes_version="$(k get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}' | tr ' ' '\n' | sort -u | tr '\n' ' ' | sed 's/ $//')"
check_eq "Kubernetes version of the nodes is the pinned one" "${K8S_VERSION_PIN}" "${nodes_version}"

# ---- stage 2 -------------------------------------------------------------------------------------------------------------------
stage_begin 2 "Images of the commit"
if IMAGES_SET=server "${KIND_DIR}/images.sh" >"${PROBE_WORK}/logs/images.log" 2>&1; then pass "server image built and pushed"; else tail -n 20 "${PROBE_WORK}/logs/images.log" >&2; upgrade_fatal "server image built and pushed" "see ${PROBE_WORK}/logs/images.log"; fi
load_images
pass "server image digest recorded" "${SERVER_DIGEST}"

# ---- stage 3 -------------------------------------------------------------------------------------------------------------------
stage_begin 3 "Previous release: digests, signatures, chart"
PREV_CHART_TGZ=""
failed_before="${FAILED}"
verify_out="$(upgrade_verify_previous "${PREV_CHART_REPO}" "${PREV_CHART_TAG}" "${PREV_CHART_DIGEST}" "${PREV_IMAGE_REPO}" "${PREV_IMAGE_TAG}" "${PREV_IMAGE_DIGEST}" \
  "${UPGRADE_PREVIOUS_VERSION}" "${UPGRADE_DIR}/previous")"
verify_rc=$?
while IFS= read -r line; do
  case "${line}" in TGZ\ *) PREV_CHART_TGZ="${line#TGZ }"; continue ;; esac
  for name in "chart digest" "image digest" "chart signature" "image signature" "chart pull"; do
    case "${line}" in
      "OK ${name} "*) pass "previous release: ${name}" "${line#"OK ${name} "}" ;;
      "FAIL ${name} "*) fail "previous release: ${name}" "${line#"FAIL ${name} "}" ;;
    esac
  done
done <<< "${verify_out}"
if [ "${verify_rc}" != 0 ] && [ "${FAILED}" = "${failed_before}" ]; then fail "previous release verified" "upgrade_verify_previous returned ${verify_rc}"; fi
if [ "${verify_rc}" != 0 ] || [ -z "${PREV_CHART_TGZ}" ]; then upgrade_abort; fi

# ---- stage 4 -------------------------------------------------------------------------------------------------------------------
stage_begin 4 "Infrastructure (PostgreSQL, Git server, GitOps fixture)"
generate_secrets
write_values_tokens 30
install_infra
if wait_infra; then pass "postgres and git are Ready"; else upgrade_fatal "postgres and git are Ready" "see kubectl -n ${NS_INFRA} get pods"; fi
gitops_prepare
if gitops_publish "upgrade: seed"; then pass "seed commit pushed to the Git server" "${GITOPS_SHA}"; else upgrade_fatal "seed commit pushed to the Git server"; fi

# ---- stage 5 -------------------------------------------------------------------------------------------------------------------
stage_begin 5 "Install of the previous release"
ensure_namespace "${NS_APP}" restricted
apply_secret "${NS_APP}" myriarch-secrets "database-dsn=$(secret_path database-dsn)" \
  "git-ssh-key=$(secret_path git-ssh-key)" "git-known-hosts=$(secret_path git-known-hosts)"
values_args "${PREV_IMAGE_REPO}" "${PREV_IMAGE_DIGEST}"
if h install myriarch "${PREV_CHART_TGZ}" -n "${NS_APP}" "${UPGRADE_VALUES[@]}" --wait --timeout 10m > "${PROBE_WORK}/logs/helm-install-previous.txt" 2>&1; then
  pass "helm install of the published chart (image by digest)"
else
  tail -n 20 "${PROBE_WORK}/logs/helm-install-previous.txt" >&2
  upgrade_fatal "helm install of the published chart" "see ${PROBE_WORK}/logs/helm-install-previous.txt"
fi
check_eq "installed chart is the previous release" "${chart_name}-${UPGRADE_PREVIOUS_VERSION}" "$(release_chart)"
check_eq "pods Ready" "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.replicas}')" "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.status.readyReplicas}')"
check_contains "the deployment runs the image of the previous release" "${PREV_IMAGE_DIGEST}" "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.template.spec.containers[0].image}')"

# ---- stage 6 -------------------------------------------------------------------------------------------------------------------
stage_begin 6 "Data: agents, fleets, audit entries"
api_use main
if pf_ensure; then pass "port-forward to svc/myriarch"; else upgrade_fatal "port-forward to svc/myriarch" "see ${PROBE_WORK}/logs/portforward-api-main.log"; fi
check_eq "bootstrap token sign-in through the API" 200 "$(login_bootstrap)"
session_refresh
# the simulator of the commit (no real collector) registers with the declared token kind-sim
SERVER_IMAGE="${SERVER_IMAGE_REF}" render_manifest "${KIND_DIR}/manifests/sim.yaml" SERVER_IMAGE | k apply -f - >/dev/null
if kn "${NS_APP}" rollout status deploy/myriarch-sim --timeout=240s >"${PROBE_WORK}/logs/rollout-sim.log" 2>&1; then pass "simulator deployed"; else upgrade_fatal "simulator deployed" "see ${PROBE_WORK}/logs/rollout-sim.log"; fi
sim_agents="$(sed -n 's/.*--agents=\([0-9][0-9]*\).*/\1/p' "${KIND_DIR}/manifests/sim.yaml" | head -n 1)"
[ -n "${sim_agents}" ] || upgrade_fatal "number of simulated agents" "no --agents in manifests/sim.yaml"
if wait_until 300 "upgrade_agents_online_now ${sim_agents}" 3; then pass "simulated agents online" "at least ${sim_agents}"; else upgrade_fatal "simulated agents online" "fewer than ${sim_agents} online after 300 s"; fi
UPGRADE_AGENTS="$(upgrade_agents_online_now "${sim_agents}")"
pass "agents online before the upgrade" "${UPGRADE_AGENTS}"
wait_until 180 '[ "$(a /fleets | jq ".items | length")" -ge 1 ]' 2
check_ge "fleets from the GitOps fixture" 1 "$(a /fleets | jq '.items | length')"
for id in $(a '/agents?kind=collector&limit=2' | jq -r '.items[].id'); do ap "/agents/${id}/actions/resync" >/dev/null; done
wait_until 30 '[ "$(audit_count agent.resync success)" -ge 2 ]' 1
check_ge "audit entries agent.resync" 2 "$(audit_count agent.resync success)"
check_ge "audit entries in total" 3 "$(a '/audit?limit=1' | jq '.total')"

# ---- stage 7 -------------------------------------------------------------------------------------------------------------------
stage_begin 7 "State before the upgrade"
if upgrade_capture "${UPGRADE_DIR}/before"; then
  n_fleets="$(jq length "${UPGRADE_DIR}/before/fleets.json")"
  n_assign="$(jq length "${UPGRADE_DIR}/before/assignments.json")"
  n_audit="$(jq length "${UPGRADE_DIR}/before/audit.json")"
  if [ "${n_fleets}" -gt 0 ] && [ "${n_assign}" -gt 0 ] && [ "${n_audit}" -gt 0 ]; then
    pass "state captured" "fleets ${n_fleets}, assignments ${n_assign}, audit entries ${n_audit}"
  else
    upgrade_fatal "state captured" "empty part: fleets ${n_fleets}, assignments ${n_assign}, audit entries ${n_audit}"
  fi
else
  upgrade_fatal "state captured" "a call of the API failed (${UPGRADE_DIR}/before)"
fi
RESTARTS_BEFORE="$(upgrade_sim_restarts)"
pass "simulator restarts before the upgrade" "${RESTARTS_BEFORE}"

# ---- stage 8 -------------------------------------------------------------------------------------------------------------------
stage_begin 8 "Upgrade to the chart and image of the commit"
values_args "${SERVER_REPO}" "${SERVER_DIGEST}"
if h upgrade myriarch "${CHART_DIR}" -n "${NS_APP}" "${UPGRADE_VALUES[@]}" --wait --timeout 10m > "${PROBE_WORK}/logs/helm-upgrade.txt" 2>&1; then
  pass "helm upgrade to the chart of the commit"
else
  tail -n 20 "${PROBE_WORK}/logs/helm-upgrade.txt" >&2
  upgrade_fatal "helm upgrade to the chart of the commit" "see ${PROBE_WORK}/logs/helm-upgrade.txt"
fi
# waits for the rollout and restarts the port-forward to a current pod
if app_rollout_wait "${NS_APP}"; then pass "rollout of the upgrade completes"; else upgrade_fatal "rollout of the upgrade completes"; fi
session_refresh
me_code="$(api_status GET /auth/me)"
if [ "${me_code}" = 401 ]; then
  # the session did not survive: rotate the bootstrap token (delete the Secret, run the same helm upgrade, sign in again)
  kn "${NS_APP}" delete secret myriarch-bootstrap --ignore-not-found >/dev/null
  if h upgrade myriarch "${CHART_DIR}" -n "${NS_APP}" "${UPGRADE_VALUES[@]}" --wait --timeout 10m > "${PROBE_WORK}/logs/helm-upgrade-rotate.txt" 2>&1 && app_rollout_wait "${NS_APP}"; then
    check_eq "sign-in with the rotated bootstrap token" 200 "$(login_bootstrap)"
    session_refresh
  else
    upgrade_fatal "bootstrap token rotation after the upgrade" "see ${PROBE_WORK}/logs/helm-upgrade-rotate.txt"
  fi
fi
check_eq "installed chart is the chart of the commit" "${chart_name}-${chart_version}" "$(release_chart)"
check_contains "the deployment runs the image of the commit" "${SERVER_DIGEST}" "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.template.spec.containers[0].image}')"

# ---- stage 9 -------------------------------------------------------------------------------------------------------------------
stage_begin 9 "Comparison"
if wait_until 300 "upgrade_agents_online_now ${UPGRADE_AGENTS}" 3; then pass "agents online after upgrade" "at least ${UPGRADE_AGENTS}"; else fail "agents online after upgrade" "fewer than ${UPGRADE_AGENTS} online after 300 s"; fi
check_eq "simulator restarts unchanged" "${RESTARTS_BEFORE}" "$(upgrade_sim_restarts)"
if upgrade_capture "${UPGRADE_DIR}/after"; then
  compare_out="$(upgrade_compare "${UPGRADE_DIR}/before" "${UPGRADE_DIR}/after" 2>"${UPGRADE_DIR}/compare.err")"
  UPGRADE_COMPARE_RC=$?
  while IFS= read -r line; do
    case "${line}" in
      "SAME "*) pass "${line#SAME } unchanged" ;;
      "DIFF "*) part="${line#DIFF }"; fail "${part%%:*} differs" "${part#*: }" ;;
    esac
  done <<< "${compare_out}"
  if [ "${UPGRADE_COMPARE_RC}" = 2 ]; then fail "comparison incomplete" "$(head -n 1 "${UPGRADE_DIR}/compare.err")"; fi
else
  UPGRADE_COMPARE_RC=2
  fail "comparison incomplete" "capture after the upgrade failed (${UPGRADE_DIR}/after)"
fi

stage_end
upgrade_summary
[ "${FAILED}" = 0 ] && [ "${UPGRADE_COMPARE_RC}" = 0 ]
