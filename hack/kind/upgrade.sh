#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Upgrade test on kind: installs the published server chart and image of the previous minor release (pinned by digest in
# hack/kind/versions.env, signatures verified with cosign), creates fleets, assignments and audit entries, upgrades to the chart
# and the images of this commit and proves that fleets, assignments and the audit log are unchanged and that the same agents are
# connected again. Needs Docker, kind, kubectl, helm, cosign (make tools), crane, jq and network access to ghcr.io and Sigstore.
#
#   hack/kind/upgrade.sh --fresh     remove cluster, registry and work directory, then everything
#   hack/kind/upgrade.sh --reset     keep cluster and registry, delete the namespaces, then images and all stages
#   hack/kind/upgrade.sh             all stages; requires hack/kind/up.sh and fails when the namespaces already exist
#
# Stages: 0 prerequisites, 1 cluster, 2 previous release (digests, signatures, chart), 3 images of the commit and infrastructure,
# 4 install of the previous release, 5 data (fleets, agents, audited actions), 6 state before, 7 upgrade, 8 proof (comparison,
# agents connected).
#
# Environment (defaults in hack/kind/README.md):
#   UPGRADE_NODE_IMAGE_VAR=KIND_NODE_IMAGE|KIND_NODE_IMAGE_PREVIOUS   which Kubernetes version of versions.env the cluster runs
#   PROBE_REUSE_IMAGES=1   keep the last images.env while iterating
#   ALLOW_DIRTY=1          build the server image from a dirty work tree (never in the final proof)
#   WEB_BUILD=auto|always|never   web UI build for the server image (--fresh uses always)
#   KIND_CLUSTER_NAME, KIND_REGISTRY_NAME, KIND_REGISTRY_PORT, PROBE_PORT_API, PROBE_WORK
#
# Output: $PROBE_WORK/upgrade.log (everything), $PROBE_WORK/results.txt (PASS|N.M|name|evidence), the answers compared in
# $PROBE_WORK/upgrade/before and $PROBE_WORK/upgrade/after, diagnostics in $PROBE_WORK/diag/<timestamp>/ when a check fails.
# Exit status 0 only if no check failed. The cluster is never deleted.
set -uo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"
# shellcheck source=hack/kind/lib/stages-core.sh
. "${KIND_DIR}/lib/stages-core.sh"
# shellcheck source=hack/kind/lib/upgrade.sh
. "${KIND_DIR}/lib/upgrade.sh"
cd "${ROOT}" || exit 1

case "${1:-}" in
  -h | --help) sed -n '2,/^set -uo pipefail$/p' "$0" | sed -e '/^set -uo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
esac

# The tools come first: nothing is created or changed when one is missing.
need_cmd kind kubectl helm cosign
need_cmd crane jq curl docker git python3 openssl ssh-keygen yq

# ---- pins (hack/kind/versions.env) -------------------------------------------------------------------------------------------
[[ "${UPGRADE_PREVIOUS_VERSION:-}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "UPGRADE_PREVIOUS_VERSION in hack/kind/versions.env is empty or not a version (x.y.z)"
upgrade_split_ref "${UPGRADE_PREVIOUS_CHART:-}" || die "UPGRADE_PREVIOUS_CHART in hack/kind/versions.env must have the form repo:tag@sha256:<digest>"
CHART_PIN_REPO="${UPGRADE_REF_REPO}"; CHART_PIN_TAG="${UPGRADE_REF_TAG}"; CHART_PIN_DIGEST="${UPGRADE_REF_DIGEST}"
upgrade_split_ref "${UPGRADE_PREVIOUS_SERVER_IMAGE:-}" || die "UPGRADE_PREVIOUS_SERVER_IMAGE in hack/kind/versions.env must have the form repo:tag@sha256:<digest>"
IMAGE_PIN_REPO="${UPGRADE_REF_REPO}"; IMAGE_PIN_TAG="${UPGRADE_REF_TAG}"; IMAGE_PIN_DIGEST="${UPGRADE_REF_DIGEST}"
[ "${CHART_PIN_TAG}" = "${UPGRADE_PREVIOUS_VERSION}" ] || die "the tag of UPGRADE_PREVIOUS_CHART is '${CHART_PIN_TAG}', expected UPGRADE_PREVIOUS_VERSION"
[ "${IMAGE_PIN_TAG}" = "${UPGRADE_PREVIOUS_VERSION}" ] || die "the tag of UPGRADE_PREVIOUS_SERVER_IMAGE is '${IMAGE_PIN_TAG}', expected UPGRADE_PREVIOUS_VERSION"
UPGRADE_NODE_IMAGE_VAR="${UPGRADE_NODE_IMAGE_VAR:-KIND_NODE_IMAGE}"
case "${UPGRADE_NODE_IMAGE_VAR}" in
  KIND_NODE_IMAGE | KIND_NODE_IMAGE_PREVIOUS) ;;
  *) die "UPGRADE_NODE_IMAGE_VAR must be KIND_NODE_IMAGE or KIND_NODE_IMAGE_PREVIOUS" ;;
esac
KIND_NODE_IMAGE="${!UPGRADE_NODE_IMAGE_VAR:-}"
export KIND_NODE_IMAGE
NODE_DIGEST_RE='@sha256:[0-9a-f]{64}$'
[[ "${KIND_NODE_IMAGE}" =~ ${NODE_DIGEST_RE} ]] || die "${UPGRADE_NODE_IMAGE_VAR} in hack/kind/versions.env must be pinned by digest"

MODE=""
case "${1:-}" in
  "") ;;
  --fresh | --reset) MODE="$1" ;;
  --inner) ;;
  *) die "usage: hack/kind/upgrade.sh [--fresh | --reset]" ;;
esac

UPGRADE_NAMESPACES="${NS_APP} ${NS_INFRA}"
UPGRADE_DIR="${PROBE_WORK}/upgrade"
IDENTITY="https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${UPGRADE_PREVIOUS_VERSION}"
OIDC_ISSUER="https://token.actions.githubusercontent.com"

# ---- outer part: modes, log file -------------------------------------------------------------------------------------
if [ "${1:-}" != --inner ]; then
  case "${MODE}" in
    --fresh)
      "${KIND_DIR}/down.sh" --all || exit 1
      export WEB_BUILD="${WEB_BUILD:-always}"
      ;;
    --reset)
      assert_context
      log "resetting the namespaces (cluster and registry stay)"
      pf_stop_all
      for ns in ${UPGRADE_NAMESPACES}; do k delete namespace "${ns}" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true; done
      ;;
    "")
      if [ -f "${KUBECONFIG}" ] && kubectl --context "${KIND_CONTEXT}" get namespace "${NS_APP}" >/dev/null 2>&1; then
        die "the namespace ${NS_APP} already exists: run hack/kind/upgrade.sh --reset (or --fresh)"
      fi
      ;;
  esac
  prepare_work_dirs
  rm -rf "${UPGRADE_DIR}"
  rm -f "${PROBE_WORK}"/cookies-* "${STATE_FILE}" "${PROBE_WORK}"/pf-*.pid "${PROBE_WORK}/values-tokens.yaml"
  # everything the inner run prints goes to the console and to upgrade.log
  "$0" --inner 2>&1 | tee -a "${PROBE_WORK}/upgrade.log"
  exit "${PIPESTATUS[0]}"
fi

# ---- inner part: stages -----------------------------------------------------------------------------------------------
prepare_work_dirs
TOTAL_START=${SECONDS}
results_init

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

summary() {
  local pass fail
  pass="$(grep -c '^PASS|' "${RESULTS_FILE}" || true)"
  fail="$(grep -c '^FAIL|' "${RESULTS_FILE}" || true)"
  printf '\n%s== summary ==%s\n' "${C_BOLD}" "${C_OFF}"
  printf 'PASS %s   FAIL %s   (%ss)\n' "${pass}" "${fail}" "$((SECONDS - TOTAL_START))"
  if [ "${fail}" -gt 0 ]; then
    printf '\nFailed checks:\n'
    grep '^FAIL|' "${RESULTS_FILE}" | while IFS='|' read -r _ id name ev; do printf '  %s %s  [%s]\n' "${id}" "${name}" "${ev}"; done
  fi
  printf '\nUpgrade: %s -> commit %s, Kubernetes %s\n' "${UPGRADE_PREVIOUS_VERSION}" "$(git -C "${ROOT}" rev-parse --short HEAD)" "$(state_get K8S_VERSION)"
  printf 'Node image: %s\n' "${KIND_NODE_IMAGE}"
  printf 'Before: server image %s, chart %s\n' "${IMAGE_PIN_REPO}@${IMAGE_PIN_DIGEST}" "${CHART_PIN_REPO}@${CHART_PIN_DIGEST}"
  printf 'After:  server image %s\n' "${SERVER_REPO:-}@$(state_get SERVER_DIGEST)"
}

# probe_fatal: a failure after which later stages make no sense (lib/stages-core.sh calls it from fatal)
probe_fatal() {
  stage_end
  "${KIND_DIR}/diag.sh" >&2 || true
  DIAG_DONE=1
  summary
  exit 1
}

# the images of the running pods of the release, one sorted unique line
app_images() { kn "${NS_APP}" get pods -l app.kubernetes.io/instance=myriarch -o jsonpath='{range .items[*]}{.spec.containers[0].image}{"\n"}{end}' 2>/dev/null | sort -u | tr '\n' ' ' | sed 's/ $//'; }
# the agents that carry no fleet, among the first 500
unassigned_agents() { a '/agents?limit=500' | jq -r '[.items[] | select((.fleetName // "") == "")] | length'; }
agents_online_as_before() { [ "$(upgrade_connected_ids)" = "$(state_get BEFORE_IDS)" ]; }
fleets_answer() { a /fleets | jq -e '.items | length > 0'; }

# cosign_verify NAME REPO DIGEST: the keyless signature of the release workflow at the tag of the previous version
cosign_verify() {
  cosign verify "$2@$3" --certificate-identity "${IDENTITY}" --certificate-oidc-issuer "${OIDC_ISSUER}" > "${PROBE_WORK}/render/cosign-$1.txt" 2>&1
}

# ---- stage 0 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_0() {
  stage_begin 0 "Prerequisites"
  if docker info >/dev/null 2>&1; then pass "Docker engine reachable" "$(docker info --format '{{.ServerVersion}} {{.Architecture}}')"; else fatal "Docker engine reachable" "start Docker Desktop"; fi
  if port_free "${PROBE_PORT_API}"; then pass "local port free" "${PROBE_PORT_API}"; else fatal "local port free" "in use: ${PROBE_PORT_API} (PROBE_PORT_API)"; fi
  pass "pinned previous release" "${UPGRADE_PREVIOUS_VERSION}"
  pass "tools present" "kind $(kind version | awk '{print $2}'), helm $(helm version --short), cosign $(cosign version --json 2>/dev/null | jq -r .gitVersion)"
}

# ---- stage 1 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_1() {
  stage_begin 1 "Cluster"
  if kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER_NAME}"; then
    pass "cluster exists" "${KIND_CLUSTER_NAME}"
  elif "${KIND_DIR}/up.sh" >"${PROBE_WORK}/logs/up.log" 2>&1; then
    pass "up.sh (cluster and registry)" "cluster ${KIND_CLUSTER_NAME}"
  else
    cat "${PROBE_WORK}/logs/up.log" >&2
    fatal "up.sh (cluster and registry)" "see ${PROBE_WORK}/logs/up.log"
  fi
  assert_context
  local names
  names="$(k get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}' | tr ' ' '\n' | sort -u | tr '\n' ' ')"
  pass "Kubernetes version" "${names}"
  state_set K8S_VERSION "$(printf '%s' "${names}" | awk '{print $1}')"
}

# ---- stage 2 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_2() {
  stage_begin 2 "Previous release: digests, signatures, chart"
  local ref name repo tag digest got
  for ref in "chart ${CHART_PIN_REPO} ${CHART_PIN_TAG} ${CHART_PIN_DIGEST}" "image ${IMAGE_PIN_REPO} ${IMAGE_PIN_TAG} ${IMAGE_PIN_DIGEST}"; do
    read -r name repo tag digest <<< "${ref}"
    got="$(crane digest "${repo}:${tag}" 2>/dev/null || true)"
    if [ "${got}" = "${digest}" ]; then pass "registry digest of the ${name} equals the pin" "${repo}:${tag}"; else fatal "registry digest of the ${name} equals the pin" "${repo}:${tag} is '${got}'"; fi
  done
  for ref in "chart ${CHART_PIN_REPO} ${CHART_PIN_DIGEST}" "image ${IMAGE_PIN_REPO} ${IMAGE_PIN_DIGEST}"; do
    read -r name repo digest <<< "${ref}"
    if cosign_verify "${name}" "${repo}" "${digest}"; then pass "cosign signature of the ${name}" "identity of release.yml at tag v${UPGRADE_PREVIOUS_VERSION}"; else fatal "cosign signature of the ${name}" "see ${PROBE_WORK}/render/cosign-${name}.txt"; fi
  done
  rm -rf "${PROBE_WORK}/render/previous"
  mkdir -p "${PROBE_WORK}/render/previous"
  if h pull "oci://${CHART_PIN_REPO}:${CHART_PIN_TAG}@${CHART_PIN_DIGEST}" --destination "${PROBE_WORK}/render/previous" --untar >"${PROBE_WORK}/logs/helm-pull.log" 2>&1 \
    && [ -f "${PROBE_WORK}/render/previous/myriarch/Chart.yaml" ]; then
    pass "chart pulled by digest" "${PROBE_WORK}/render/previous/myriarch"
  else fatal "chart pulled by digest" "see ${PROBE_WORK}/logs/helm-pull.log"; fi
  write_values_tokens 30
  if h template myriarch "${PROBE_WORK}/render/previous/myriarch" -n "${NS_APP}" --set "config.server.publicUrl=${PUBLIC_URL}" \
    --set "image.repository=${IMAGE_PIN_REPO}" --set "image.digest=${IMAGE_PIN_DIGEST}" \
    -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml" >"${PROBE_WORK}/render/previous.yaml" 2>"${PROBE_WORK}/logs/helm-template.log"; then
    pass "pulled chart renders with the values of the installation"
  else fatal "pulled chart renders with the values of the installation" "$(tail -n 3 "${PROBE_WORK}/logs/helm-template.log" | tr '\n' ' ')"; fi
  if grep -qF "@${IMAGE_PIN_DIGEST}" "${PROBE_WORK}/render/previous.yaml"; then pass "the chart renders the server image by the pinned digest"; else fatal "the chart renders the server image by the pinned digest"; fi
}

# ---- stage 3 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_3() {
  stage_begin 3 "Images of the commit and infrastructure"
  if IMAGES_SET=server "${KIND_DIR}/images.sh" >"${PROBE_WORK}/logs/images.log" 2>&1; then pass "server image built and pushed (crane, no container runtime)"; else tail -n 20 "${PROBE_WORK}/logs/images.log" >&2; fatal "server image built and pushed" "see ${PROBE_WORK}/logs/images.log"; fi
  load_images
  state_set SERVER_DIGEST "${SERVER_DIGEST}"
  pass "image digest recorded" "server ${SERVER_DIGEST}"
  generate_secrets
  install_infra
  if wait_infra; then pass "postgres and git are Ready"; else fatal "postgres and git are Ready" "see kubectl -n ${NS_INFRA} get pods"; fi
  gitops_prepare
  if gitops_publish "upgrade test: seed"; then pass "seed commit pushed to the Git server" "${GITOPS_SHA}"; else fatal "seed commit pushed to the Git server"; fi
}

# ---- stage 4 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_4() {
  stage_begin 4 "Install of the previous release"
  ensure_namespace "${NS_APP}" restricted
  apply_secret "${NS_APP}" myriarch-secrets "database-dsn=$(secret_path database-dsn)" \
    "git-ssh-key=$(secret_path git-ssh-key)" "git-known-hosts=$(secret_path git-known-hosts)"
  if h install myriarch "${PROBE_WORK}/render/previous/myriarch" -n "${NS_APP}" --set "config.server.publicUrl=${PUBLIC_URL}" \
    --set "image.repository=${IMAGE_PIN_REPO}" --set "image.digest=${IMAGE_PIN_DIGEST}" \
    -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml" >"${PROBE_WORK}/notes-install.txt" 2>"${PROBE_WORK}/logs/helm-install.log"; then
    pass "helm install of the pulled chart" "${UPGRADE_PREVIOUS_VERSION}"
  else fatal "helm install of the pulled chart" "$(tail -n 3 "${PROBE_WORK}/logs/helm-install.log" | tr '\n' ' ')"; fi
  if kn "${NS_APP}" rollout status deploy/myriarch --timeout=300s >"${PROBE_WORK}/logs/rollout-previous.log" 2>&1; then pass "rollout of the previous release completes"; else fatal "rollout of the previous release completes" "see ${PROBE_WORK}/logs/rollout-previous.log"; fi
  check_eq "the pods run the pinned image" "${IMAGE_PIN_REPO}@${IMAGE_PIN_DIGEST}" "$(app_images)"
}

# ---- stage 5 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_5() {
  stage_begin 5 "Data: fleets, agents, audited actions"
  api_use main
  if pf_ensure; then pass "port-forward to svc/myriarch"; else fatal "port-forward to svc/myriarch" "see ${PROBE_WORK}/logs/portforward-api-main.log"; fi
  local tokfile
  tokfile="$(secret_path bootstrap.token)"
  bootstrap_token_from_notes "${tokfile}" || : > "${tokfile}"
  rm -f "${API_JAR}"
  ( umask 077; : > "${API_JAR}" )
  check_eq "bootstrap token sign-in through the API" 200 "$(bootstrap_login "${tokfile}" "${API_JAR}")"
  session_refresh
  check_eq "/auth/me role" admin "$(a /auth/me | jq -r .role)"
  # GitOps: the seed commit is applied, then one more fleet is added
  wait_until 180 "[ \"\$(a /gitops/status | jq -r .state)\" = ok ]" 2
  check_eq "/gitops/status state" ok "$(a /gitops/status | jq -r .state)"
  cat > "${GITOPS_WORK}/fleets/upgrade-edge.yaml" <<'EOF'
# Fleet that exists before the upgrade test upgrades the server (simulated edge collectors).
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata:
  name: upgrade-edge
  description: fleet created before the upgrade
spec:
  displayName: Upgrade edge
  priority: 50
  selector:
    matchAttributes:
      site.type: edge
  blueprint: sim
  collector:
    distribution: otelcol-contrib
    version: 0.161.0
  update:
    executor: none
  rollout:
    configPolicy: probe-config
EOF
  if gitops_publish "upgrade test: fleet upgrade-edge"; then pass "fleet upgrade-edge pushed" "${GITOPS_SHA}"; else fatal "fleet upgrade-edge pushed"; fi
  ap /gitops/actions/sync >/dev/null
  if wait_until 120 "[ \"\$(a /fleets | jq -r '[.items[].fleet.name | select(. == \"upgrade-edge\")] | length')\" = 1 ]" 2; then pass "GitOps applied the new fleet"; else fatal "GitOps applied the new fleet" "$(a /fleets | jq -r '[.items[].fleet.name] | join(",")')"; fi
  # agents: the simulator only (20 collectors), assigned to fleets by their attributes
  SERVER_IMAGE="${SERVER_IMAGE_REF}" render_manifest "${KIND_DIR}/manifests/sim.yaml" SERVER_IMAGE | k apply -f - >/dev/null
  if kn "${NS_APP}" rollout status deploy/myriarch-sim --timeout=240s >"${PROBE_WORK}/logs/rollout-sim.log" 2>&1; then pass "simulator deployed"; else fatal "simulator deployed" "see ${PROBE_WORK}/logs/rollout-sim.log"; fi
  wait_until 240 "[ \"\$(upgrade_connected_count)\" = 20 ]" 2
  check_eq "20 agents connected" 20 "$(upgrade_connected_count)"
  wait_until 120 "[ \"\$(unassigned_agents)\" = 0 ]" 2
  check_eq "every agent is assigned to a fleet" 0 "$(unassigned_agents)"
  # audited operator actions: a version selection (refused for a fleet without an executor, audited all the same) and its
  # removal, and a resync of one agent
  local audit_before agent
  audit_before="$(a '/audit?limit=1' | jq -r '.total')"
  ap /fleets/sim-dc/actions/select-version '{"version":"0.162.0","reason":"upgrade test"}' >/dev/null
  ap /fleets/sim-dc/actions/clear-version-selection '{"reason":"upgrade test"}' >/dev/null
  agent="$(a '/agents?kind=collector&limit=1' | jq -r '.items[0].id // empty')"
  [ -n "${agent}" ] || fatal "an agent to resync" "no collector listed"
  ap "/agents/${agent}/actions/resync" >/dev/null
  check_ge "audit entries of the operator actions" "$((audit_before + 2))" "$(a '/audit?limit=1' | jq -r '.total')"
  check_ge "audit entry fleet.version.select" 1 "$(audit_count fleet.version.select)"
}

# ---- stage 6 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_6() {
  stage_begin 6 "State before the upgrade"
  api_use main
  pf_ensure || true
  if upgrade_snapshot "${UPGRADE_DIR}/before"; then pass "snapshot of fleets, assignments and audit log" "${UPGRADE_DIR}/before"; else fatal "snapshot of fleets, assignments and audit log"; fi
  check_ge "fleets before" 4 "$(jq '.items | length' "${UPGRADE_DIR}/before/fleets.json")"
  check_eq "agents before" 20 "$(jq '[.[].items[]] | length' "${UPGRADE_DIR}/before/assignments.json")"
  check_ge "audit entries before" 3 "$(jq '[.[].items[]] | length' "${UPGRADE_DIR}/before/audit.json")"
  state_set BEFORE_CONNECTED "$(upgrade_connected_count)"
  state_set BEFORE_IDS "$(upgrade_connected_ids)"
  check_eq "connected agents before" 20 "$(state_get BEFORE_CONNECTED)"
}

# ---- stage 7 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_7() {
  stage_begin 7 "Upgrade to the chart and images of the commit"
  load_images
  # the same values as the installation; never --reuse-values
  if chart_upgrade_full; then pass "helm upgrade to the chart of the commit"; else fatal "helm upgrade to the chart of the commit"; fi
  if app_rollout_wait "${NS_APP}"; then pass "rollout of the upgrade completes"; else fatal "rollout of the upgrade completes"; fi
  check_eq "the pods run the image of the commit" "${SERVER_REPO}@${SERVER_DIGEST}" "$(app_images)"
}

# ---- stage 8 -----------------------------------------------------------------------------------------------------------------
upgrade_stage_8() {
  stage_begin 8 "Proof: nothing lost, agents connected"
  api_use main
  pf_restart || true
  # the session of stage 5 lives in the database; the one-time bootstrap token cannot sign in a second time
  if wait_until 180 fleets_answer 2; then pass "the API answers with the old session"; else fatal "the API answers with the old session" "GET /fleets after the upgrade"; fi
  check_eq "/auth/me name" admin "$(a /auth/me | jq -r .name)"
  if upgrade_snapshot "${UPGRADE_DIR}/after"; then pass "snapshot after the upgrade" "${UPGRADE_DIR}/after"; else fatal "snapshot after the upgrade"; fi
  local rc=0
  upgrade_compare "${UPGRADE_DIR}/before" "${UPGRADE_DIR}/after" 2>"${UPGRADE_DIR}/compare.txt" || rc=$?
  cat "${UPGRADE_DIR}/compare.txt" >&2
  if [ "${rc}" = 0 ]; then
    pass "fleets, assignments and audit log are unchanged" "$(jq '.items | length' "${UPGRADE_DIR}/after/fleets.json") fleets, $(jq '[.[].items[]] | length' "${UPGRADE_DIR}/after/audit.json") audit entries"
  else fail "fleets, assignments and audit log are unchanged" "$(tr '\n' ' ' < "${UPGRADE_DIR}/compare.txt")"; fi
  wait_until 180 agents_online_as_before 2
  check_eq "the same agents are connected" "$(state_get BEFORE_IDS)" "$(upgrade_connected_ids)"
  check_eq "connected agents after" "$(state_get BEFORE_CONNECTED)" "$(upgrade_connected_count)"
  check_eq "no agent is offline" 0 "$(a '/agents?status=offline&limit=1' | jq -r '.total')"
}

for n in 0 1 2 3 4 5 6 7 8; do
  "upgrade_stage_${n}"
done
stage_end
summary
[ "${FAILED}" = 0 ]
