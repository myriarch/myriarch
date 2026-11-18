#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Upgrade test on kind: installs the published server chart and image of the previous minor release from the OCI registry (pinned by
# digest in hack/kind/versions.env, signature verified with cosign), creates fleets, assignments (simulated agents) and audit entries,
# upgrades to the chart and the images of this commit and proves that fleets, assignments and the audit log are unchanged and that the
# same agents are online again without registering anew. Exit status 0 only if no check failed.
#
#   hack/kind/upgrade.sh --fresh     remove cluster, registry and work directory, then everything
#   hack/kind/upgrade.sh --reset     keep cluster and registry, delete the namespaces myriarch and probe-infra, then everything
#   hack/kind/upgrade.sh             everything; requires hack/kind/up.sh and fails when the namespace myriarch already exists
#
# Needs on the PATH: kind, kubectl, helm and cosign (make tools installs the pinned ones into .cache/bin), and docker, crane, jq, curl,
# git, python3, openssl, ssh-keygen and go. A full run needs Docker (kind) and network access to ghcr.io, Docker Hub, Sigstore (the TUF
# root of cosign) and the Go proxy. The script stops with one line when kind, kubectl, helm or cosign is missing.
#
# Environment: UPGRADE_KUBE=current|previous (node image KIND_NODE_IMAGE or KIND_NODE_IMAGE_PREVIOUS), ALLOW_DIRTY=1 (image of a dirty
# work tree, development only), WEB_BUILD, and the variables of hack/kind/README.md (KIND_CLUSTER_NAME, PROBE_WORK, PROBE_PORT_API, ...).
# The previous version and all image pins are in hack/kind/versions.env (UPGRADE_PREVIOUS_*).
#
# Output: $PROBE_WORK/results.txt (PASS|N.M|name|evidence), the snapshots in $PROBE_WORK/upgrade/before and /after, logs in
# $PROBE_WORK/logs, diagnostics in $PROBE_WORK/diag/<timestamp>/ after a failure. The cluster is never deleted.
set -euo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
# shellcheck source=hack/kind/lib/upgrade.sh
. "${KIND_DIR}/lib/upgrade.sh"

# ---- stage 0: arguments, tools, pins -----------------------------------------------------------------------------------
MODE=""
case "${1:-}" in
  "") ;;
  --fresh | --reset) MODE="$1" ;;
  -h | --help) sed -n '2,/^set -euo pipefail$/p' "$0" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) die "usage: hack/kind/upgrade.sh [--fresh | --reset]" ;;
esac

# first of everything that runs an external command: the four tools of the test
upgrade_need_tools
need_cmd docker crane jq curl git python3 openssl ssh-keygen go
upgrade_select_node_image || die "invalid UPGRADE_KUBE"
upgrade_check_pins || die "the pins of hack/kind/versions.env are not usable"
cd "${ROOT}"

CHART_REPO_PATH=ghcr.io/myriarch/charts/myriarch
SIGN_IDENTITY_BASE=https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags
SIGN_ISSUER=https://token.actions.githubusercontent.com
PREV_SERVER_REPO="$(upgrade_ref_repo "${UPGRADE_PREVIOUS_SERVER_IMAGE}")"
PREV_SERVER_DIGEST="$(upgrade_ref_digest "${UPGRADE_PREVIOUS_SERVER_IMAGE}")"
PREV_SERVER_REF="${PREV_SERVER_REPO}@${PREV_SERVER_DIGEST}"
UPGRADE_WORK="${PROBE_WORK}/upgrade"
PROBE_NAMESPACES="${NS_APP} ${NS_INFRA}"

# ---- modes (before the work directory is prepared: down.sh --all removes it) ------------------------------------------
case "${MODE}" in
  --fresh)
    "${KIND_DIR}/down.sh" --all || exit 1
    export WEB_BUILD="${WEB_BUILD:-always}"
    ;;
  --reset)
    assert_context
    log "resetting the namespaces ${PROBE_NAMESPACES} (cluster and registry stay)"
    pf_stop_all
    for ns in ${PROBE_NAMESPACES}; do k delete namespace "${ns}" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true; done
    rm -f "${PROBE_WORK}"/cookies-* "${PROBE_WORK}"/pf-*.pid "${PROBE_WORK}"/values-tokens.yaml
    ;;
  "")
    [ -f "${KUBECONFIG}" ] || die "no cluster: run hack/kind/up.sh first, or use --fresh"
    if k get namespace "${NS_APP}" >/dev/null 2>&1; then
      die "the namespace ${NS_APP} already exists: run hack/kind/upgrade.sh --reset (or --fresh)"
    fi
    ;;
esac

prepare_work_dirs
results_init
rm -rf "${UPGRADE_WORK}"
mkdir -p "${UPGRADE_WORK}"
TOTAL_START=${SECONDS}

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
  printf '\nUpgrade: %s (chart %s) -> commit %s\n' "${PREV_SERVER_REF}" "${UPGRADE_PREVIOUS_CHART_DIGEST}" "$(git -C "${ROOT}" rev-parse --short HEAD)"
  printf 'Node image: %s\n' "${KIND_NODE_IMAGE}"
}

# upgrade_fatal NAME EVIDENCE: a failure after which later stages make no sense
upgrade_fatal() {
  fail "$1" "${2:-}"
  stage_end
  "${KIND_DIR}/diag.sh" >&2 || true
  DIAG_DONE=1
  summary
  exit 1
}

# must NAME COMMAND...: the command succeeded, or the run ends
must() {
  local name="$1"
  shift
  if "$@"; then pass "${name}"; else upgrade_fatal "${name}" "command failed: $1"; fi
}

gitops_applied() { # SHA: the syncer is ok and has applied exactly this commit
  [ "$(a /gitops/status | jq -r '[.state, (.appliedCommit.sha // "")] | join(" ")')" = "ok $1" ]
}

agents_ready() { # every agent is online and in a fleet, and there is at least one
  a '/agents?limit=500' | jq -e '(.items | length) > 0 and ([.items[] | select(.status != "online" or ((.fleetName // "") == ""))] | length) == 0' >/dev/null
}

deployed_image() { kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.template.spec.containers[0].image}'; }

# sign_in: spends the one-time bootstrap token of the Secret on the cookie jar of the main target
sign_in() {
  ( umask 077; kn "${NS_APP}" get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d > "$(secret_path bootstrap-token)" )
  check_eq "bootstrap token sign-in through the API" 200 "$(bootstrap_login "$(secret_path bootstrap-token)" "${API_JAR}")"
  session_refresh
}

# ---- stage 1: cluster ----------------------------------------------------------------------------------------------------
stage_begin 1 "Cluster and registry"
if [ "${MODE}" = --fresh ]; then
  if KIND_SYSTEM_TOOLS=1 "${KIND_DIR}/up.sh" >> "${PROBE_WORK}/logs/up.log" 2>&1; then
    pass "kind cluster and registry are up" "${KIND_NODE_IMAGE%%@*}"
  else
    tail -n 20 "${PROBE_WORK}/logs/up.log" >&2
    upgrade_fatal "kind cluster and registry are up" "see ${PROBE_WORK}/logs/up.log"
  fi
fi
assert_context
pass "kubectl context is ${KIND_CONTEXT}"

# ---- stage 2: images of the commit ---------------------------------------------------------------------------------------
stage_begin 2 "Images of the commit"
if IMAGES_SET=server "${KIND_DIR}/images.sh" > "${PROBE_WORK}/logs/images.log" 2>&1; then
  pass "server image of the commit built and pushed"
else
  tail -n 20 "${PROBE_WORK}/logs/images.log" >&2
  upgrade_fatal "server image of the commit built and pushed" "see ${PROBE_WORK}/logs/images.log"
fi
load_images
if [[ "${SERVER_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]]; then pass "server image is pinned by digest" "${SERVER_DIGEST}"; else upgrade_fatal "server image is pinned by digest" "'${SERVER_DIGEST}'"; fi

# ---- stage 3: the previous release, verified before anything is installed ------------------------------------------------
stage_begin 3 "Previous release: digest and signature"
write_values_tokens 30
PREV_SET=(--set "config.server.publicUrl=${PUBLIC_URL}" --set "image.repository=${PREV_SERVER_REPO}" --set "image.digest=${PREV_SERVER_DIGEST}")
mkdir -p "${UPGRADE_WORK}/chart"
if pull_out="$(h pull "oci://${CHART_REPO_PATH}" --version "${UPGRADE_PREVIOUS_VERSION}" --destination "${UPGRADE_WORK}/chart" 2>&1)"; then
  pass "helm pull of the previous chart" "version ${UPGRADE_PREVIOUS_VERSION}"
else
  printf '%s\n' "${pull_out}" > "${PROBE_WORK}/logs/helm-pull.log"
  upgrade_fatal "helm pull of the previous chart" "see ${PROBE_WORK}/logs/helm-pull.log"
fi
printf '%s\n' "${pull_out}" > "${PROBE_WORK}/logs/helm-pull.log"
PREV_CHART_TGZ="${UPGRADE_WORK}/chart/myriarch-${UPGRADE_PREVIOUS_VERSION}.tgz"
[ -f "${PREV_CHART_TGZ}" ] || upgrade_fatal "the pulled chart archive exists" "${PREV_CHART_TGZ##*/} is missing"
pulled_digest="$(printf '%s\n' "${pull_out}" | upgrade_pull_digest || true)"
if [ "${pulled_digest}" = "${UPGRADE_PREVIOUS_CHART_DIGEST}" ]; then
  pass "the pulled chart has the pinned digest" "${pulled_digest}"
else
  upgrade_fatal "the pulled chart has the pinned digest" "helm pull reported '${pulled_digest:-none}', the pin is ${UPGRADE_PREVIOUS_CHART_DIGEST}"
fi

cosign_verify() { # NAME REFERENCE
  if cosign verify "$2" --certificate-identity "${SIGN_IDENTITY_BASE}/v${UPGRADE_PREVIOUS_VERSION}" --certificate-oidc-issuer "${SIGN_ISSUER}" \
    > "${PROBE_WORK}/logs/cosign-$1.log" 2>&1; then
    pass "cosign verify of the previous $1" "$2"
  else
    tail -n 10 "${PROBE_WORK}/logs/cosign-$1.log" >&2
    upgrade_fatal "cosign verify of the previous $1" "see ${PROBE_WORK}/logs/cosign-$1.log"
  fi
}
cosign_verify chart "${CHART_REPO_PATH}@${UPGRADE_PREVIOUS_CHART_DIGEST}"
cosign_verify image "${PREV_SERVER_REF}"

if h template myriarch "${PREV_CHART_TGZ}" -n "${NS_APP}" "${PREV_SET[@]}" -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml" \
  > "${UPGRADE_WORK}/previous-rendered.yaml" 2> "${PROBE_WORK}/logs/helm-template-previous.log"; then
  pass "helm template of the previous chart"
else
  upgrade_fatal "helm template of the previous chart" "see ${PROBE_WORK}/logs/helm-template-previous.log"
fi
if upgrade_check_template_images "${PREV_SERVER_REF}" < "${UPGRADE_WORK}/previous-rendered.yaml"; then
  pass "every image of the previous chart is the pinned server image"
else
  upgrade_fatal "every image of the previous chart is the pinned server image" "see ${UPGRADE_WORK}/previous-rendered.yaml"
fi

# ---- stage 4: infrastructure ---------------------------------------------------------------------------------------------
stage_begin 4 "PostgreSQL, Git server, GitOps seed"
generate_secrets
install_infra
must "PostgreSQL and the Git server are ready" wait_infra
gitops_prepare
fleet_files="$(find "${KIND_DIR}/gitops/fleets" -type f | wc -l | tr -d ' ')"
must "the GitOps fixture is published" gitops_publish "upgrade: seed"
state_sha="${GITOPS_SHA}"
pass "seed commit" "${state_sha}"

# ---- stage 5: the previous release ---------------------------------------------------------------------------------------
stage_begin 5 "Install the previous release"
ensure_namespace "${NS_APP}" restricted
upgrade_secrets() {
  apply_secret "${NS_APP}" myriarch-secrets "database-dsn=$(secret_path database-dsn)" \
    "git-ssh-key=$(secret_path git-ssh-key)" "git-known-hosts=$(secret_path git-known-hosts)"
}
upgrade_secrets
if h install myriarch "${PREV_CHART_TGZ}" -n "${NS_APP}" "${PREV_SET[@]}" -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml" \
  > "${PROBE_WORK}/logs/helm-install-previous.log" 2>&1; then
  pass "helm install of the pulled and verified previous chart"
else
  upgrade_fatal "helm install of the pulled and verified previous chart" "see ${PROBE_WORK}/logs/helm-install-previous.log"
fi
api_use main
must "rollout of the previous release" app_rollout_wait "${NS_APP}"
check_eq "the deployment runs the pinned previous image" "${PREV_SERVER_REF}" "$(deployed_image)"
sign_in
if [ "${FAILED}" -gt 0 ]; then upgrade_fatal "sign-in to the previous release" "see the failed check above"; fi
if wait_until 180 "gitops_applied ${state_sha}" 2; then pass "GitOps applied the seed commit" "${state_sha}"; else upgrade_fatal "GitOps applied the seed commit" "$(a /gitops/status | jq -r '[.state, (.appliedCommit.sha // "none")] | join(" ")')"; fi

# ---- stage 6: data -------------------------------------------------------------------------------------------------------
stage_begin 6 "Agents and audit entries"
SERVER_IMAGE="${SERVER_IMAGE_REF}" render_manifest "${KIND_DIR}/manifests/sim.yaml" SERVER_IMAGE | k apply -f - >/dev/null
if kn "${NS_APP}" rollout status deploy/myriarch-sim --timeout=240s > "${PROBE_WORK}/logs/rollout-sim.log" 2>&1; then
  pass "simulator deployed (Pod Security restricted)"
else
  upgrade_fatal "simulator deployed" "see ${PROBE_WORK}/logs/rollout-sim.log"
fi
if wait_until 300 agents_ready 3; then pass "every agent is online and in a fleet"; else upgrade_fatal "every agent is online and in a fleet" "$(a /stats/overview | jq -c '.agents // {}')"; fi
sync_out="$(ap /gitops/actions/sync || true)"
pass "GitOps sync requested" "$(printf '%s' "${sync_out}" | head -c 80 | tr -d '\n')"
for id in $(a '/agents?limit=2' | jq -r '.items[].id'); do
  ap "/agents/${id}/actions/resync" > /dev/null || true
done
sleep 2
check_ge "audit entries exist" 3 "$(a '/audit?limit=1' | jq -r '.total // 0')"

# ---- stage 7: the state before ---------------------------------------------------------------------------------------------
stage_begin 7 "Snapshot before the upgrade"
must "snapshot before the upgrade" upgrade_snapshot "${UPGRADE_WORK}/before"
check_eq "fleets in the snapshot equal the fixture" "${fleet_files}" "$(jq '.items | length' "${UPGRADE_WORK}/before/fleets.json")"
check_ge "agents in the snapshot" 1 "$(jq '.items | length' "${UPGRADE_WORK}/before/agents.json")"
check_eq "every agent in the snapshot is online" 0 "$(jq '[.items[] | select(.status != "online")] | length' "${UPGRADE_WORK}/before/agents.json")"
check_ge "audit events in the snapshot" 1 "$(jq '.items | length' "${UPGRADE_WORK}/before/audit.json")"
if [ "${FAILED}" -gt 0 ]; then upgrade_fatal "the snapshot before the upgrade is usable" "see the failed checks above"; fi

# ---- stage 8: upgrade ----------------------------------------------------------------------------------------------------
stage_begin 8 "Upgrade to the chart and images of the commit"
run_upgrade() { # LOG
  upgrade_secrets
  chart_set_args
  h upgrade myriarch "${CHART_DIR}" -n "${NS_APP}" "${CHART_SET[@]}" -f "${KIND_DIR}/values/probe.yaml" -f "${PROBE_WORK}/values-tokens.yaml" > "$1" 2>&1
}
if run_upgrade "${PROBE_WORK}/logs/helm-upgrade.log"; then pass "helm upgrade to the chart of the commit"; else upgrade_fatal "helm upgrade to the chart of the commit" "see ${PROBE_WORK}/logs/helm-upgrade.log"; fi
must "rollout of the upgraded release" app_rollout_wait "${NS_APP}"
check_eq "the deployment runs the image of the commit" "${SERVER_REPO}@${SERVER_DIGEST}" "$(deployed_image)"
if [ "$(api_status GET /system/info)" = 401 ]; then
  # the one-time bootstrap token activates again after a rollout that lost the session: rotate it as the README describes
  skip "session survives the upgrade" "the API answered 401; the bootstrap token is rotated (Secret deleted, helm upgrade, sign in again)"
  kn "${NS_APP}" delete secret myriarch-bootstrap --ignore-not-found >/dev/null
  run_upgrade "${PROBE_WORK}/logs/helm-upgrade-rotate.log" || upgrade_fatal "helm upgrade after the rotation" "see ${PROBE_WORK}/logs/helm-upgrade-rotate.log"
  must "rollout after the rotation" app_rollout_wait "${NS_APP}"
  sign_in
else
  pass "session survives the upgrade"
fi
if wait_until 300 "gitops_applied ${state_sha}" 2; then pass "GitOps is ok with the same applied commit" "${state_sha}"; else fail "GitOps is ok with the same applied commit" "$(a /gitops/status | jq -r '[.state, (.appliedCommit.sha // "none")] | join(" ")')"; fi
online_ids="$(jq -r '[.items[] | select(.status == "online") | .id] | length' "${UPGRADE_WORK}/before/agents.json")"
agents_back() { # every agent that was online is online again
  a '/agents?limit=500' | jq -e --slurpfile b "${UPGRADE_WORK}/before/agents.json" \
    '. as $now | [$b[0].items[] | select(.status == "online") | .id | select(. as $i | $now.items | any(.id == $i and .status == "online") | not)] | length == 0' >/dev/null
}
if wait_until 300 agents_back 3; then pass "every agent that was online is online again" "${online_ids} agents"; else fail "every agent that was online is online again" "see the comparison below"; fi

# ---- stage 9: the state after and the comparison -------------------------------------------------------------------------
stage_begin 9 "Snapshot after the upgrade and comparison"
must "snapshot after the upgrade" upgrade_snapshot "${UPGRADE_WORK}/after"
if upgrade_compare "${UPGRADE_WORK}/before" "${UPGRADE_WORK}/after" 2> "${UPGRADE_WORK}/compare.log"; then
  pass "fleets, assignments and the audit log are unchanged"
else
  cat "${UPGRADE_WORK}/compare.log" >&2
  fail "fleets, assignments and the audit log are unchanged" "$(tr '\n' ' ' < "${UPGRADE_WORK}/compare.log")"
fi
if upgrade_check_online "${UPGRADE_WORK}/before" "${UPGRADE_WORK}/after" 2> "${UPGRADE_WORK}/online.log"; then
  pass "the same agents are online, none registered anew"
else
  cat "${UPGRADE_WORK}/online.log" >&2
  fail "the same agents are online, none registered anew" "$(tr '\n' ' ' < "${UPGRADE_WORK}/online.log")"
fi

stage_end
summary
pf_stop_all
if [ "${FAILED}" -gt 0 ]; then exit 1; fi
exit 0
