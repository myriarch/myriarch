#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Installation test of the two Helm charts on kind (docs/development.md, "Chart installation test on kind"; hack/kind/README.md).
# The probe of hack/kind checks the whole system; this test checks only the charts, as a user installs them, and prints PASS / FAIL
# per step like the probe.
#
#   ci/kind-chart-test.sh --fresh     remove cluster, registry and work directory, then everything (CI)
#   ci/kind-chart-test.sh --reset     keep cluster and registry, delete the namespaces of this test, then everything
#   ci/kind-chart-test.sh             everything; fails when the namespaces of this test already exist
#
# Stages: 0 prerequisites, 1 cluster, 2 images of the commit (server and updater), 3 PostgreSQL, 4 server chart (install, helm test),
# 5 collector chart as a StatefulSet under the Pod Security level "restricted" and as a DaemonSet in a "privileged" namespace (the
# documented exception for the hostPath volume of nodeLogs), both registered healthy at the server (REST API), 6 clean uninstall,
# 7 helm upgrade from the chart of the tag v0.1.0 (taken from Git, with the image of that release) to the chart of the commit.
#
# It reuses hack/kind (up.sh, images.sh, diag.sh and the libraries of the probe), so the cluster, the registry, the work directory
# .cache/kind and the node image (KIND_NODE_IMAGE, a digest pin of hack/kind/versions.env) are the same ones. It shares the cluster
# with `make kind-probe`: run one of the two at a time. Nothing is published and no secret is printed; the tokens are generated into
# files of .cache/kind/secrets.
#
# Environment: KIND_NODE_IMAGE, KIND_CLUSTER_NAME, KIND_REGISTRY_NAME, KIND_REGISTRY_PORT, PROBE_WORK (hack/kind/README.md),
# CHART_TEST_UPGRADE_FROM (the tag of the old chart, default v0.1.0), ALLOW_DIRTY=1 (build from a dirty work tree; never in CI).
# Output: $PROBE_WORK/probe.log (everything), $PROBE_WORK/results.txt (PASS|N.M|name|evidence), $PROBE_WORK/durations.txt and the
# diagnostics of hack/kind/diag.sh in $PROBE_WORK/diag/<timestamp>/ when a check fails. Exit status 0 only if no check failed.
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/kind/lib/common.sh
. "${ROOT_DIR}/hack/kind/lib/common.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"
cd "${ROOT}" || exit 1

# the namespaces of this test (the server one is NS_APP of the probe) and the Secrets in them
NS_STS=ct-sts
NS_DS=ct-ds
NS_UPGRADE=ct-upgrade
CT_NAMESPACES="${NS_APP} ${NS_STS} ${NS_DS} ${NS_UPGRADE} ${NS_INFRA}"
UPGRADE_FROM="${CHART_TEST_UPGRADE_FROM:-v0.1.0}"
SERVER_ENDPOINT="ws://myriarch.${NS_APP}.svc:8080/v1/opamp"
export DIAG_EXTRA_NS="${NS_STS} ${NS_DS} ${NS_UPGRADE}"

MODE=""
case "${1:-}" in
  "") ;;
  --fresh | --reset) MODE="$1" ;;
  --inner) ;;
  -h | --help) sed -n '2,/^set -uo pipefail$/p' "$0" | sed -e '/^set -uo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) die "usage: ci/kind-chart-test.sh [--fresh | --reset]" ;;
esac

# ---- outer part: modes, log file -------------------------------------------------------------------------------------
if [ "${1:-}" != --inner ]; then
  case "${MODE}" in
    --fresh)
      "${KIND_DIR}/down.sh" --all || exit 1
      export WEB_BUILD="${WEB_BUILD:-always}"
      ;;
    --reset)
      assert_context
      log "resetting the namespaces of the test (cluster and registry stay)"
      pf_stop_all
      for ns in ${CT_NAMESPACES}; do k delete namespace "${ns}" --ignore-not-found --wait=true --timeout=300s >/dev/null 2>&1 || true; done
      rm -f "${PROBE_WORK}"/cookies-*
      ;;
    "")
      if [ -f "${KUBECONFIG}" ] && kubectl --context "${KIND_CONTEXT}" get namespace "${NS_APP}" >/dev/null 2>&1; then
        die "the namespaces of the test already exist: run ci/kind-chart-test.sh --reset (or --fresh)"
      fi
      ;;
  esac
  prepare_work_dirs
  "$0" --inner 2>&1 | tee -a "${PROBE_WORK}/probe.log"
  exit "${PIPESTATUS[0]}"
fi

# ---- inner part: stages ----------------------------------------------------------------------------------------------
prepare_work_dirs
TOTAL_START=${SECONDS}
results_init
VALUES_FILE="${PROBE_WORK}/values-chart-test.yaml"
OLD_CHART_DIR="${PROBE_WORK}/chart-${UPGRADE_FROM}"
COLLECTOR_CHART_DIR="${ROOT}/deploy/helm/myriarch-collector"
STEP_LOG="${PROBE_WORK}/logs/chart-test-steps.log"
K8S_VERSION=""
DIAG_DONE=""

test_cleanup() {
  local rc=$?
  trap - EXIT
  stage_end
  if [ "${FAILED}" -gt 0 ] && [ -z "${DIAG_DONE}" ]; then "${KIND_DIR}/diag.sh" >&2 || true; fi
  pf_stop_all
  exit "${rc}"
}
trap test_cleanup EXIT
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
  printf '\nStage durations:\n'
  while IFS='|' read -r n secs title; do printf '  %2s  %5ss  %s\n' "${n}" "${secs}" "${title}"; done < "${DURATIONS_FILE}"
  printf '\nVersions: kind %s, Kubernetes %s, kubectl %s, helm %s\n' "$(kind version 2>/dev/null | awk '{print $2}')" "${K8S_VERSION}" \
    "$(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion)" "$(helm version --short 2>/dev/null)"
  printf 'Node image: %s\n' "${KIND_NODE_IMAGE}"
  printf 'Images: server %s, updater %s\n' "${SERVER_DIGEST:-}" "${UPDATER_DIGEST:-}"
  printf 'Git: HEAD %s, work tree %s\n' "$(git -C "${ROOT}" rev-parse --short HEAD)" "$([ -z "$(git -C "${ROOT}" status --porcelain)" ] && echo clean || echo dirty)"
}

# fatal: a failure after which later stages make no sense
fatal() {
  stage_end
  "${KIND_DIR}/diag.sh" >&2 || true
  DIAG_DONE=1
  summary
  exit 1
}

# step NAME COMMAND...: runs the command with its output in the step log; PASS or FAIL (with the last line of the output as
# evidence). Returns the status of the command.
step() {
  local name="$1" rc tail_line
  shift
  { printf '\n$ %s\n' "$*"; } >> "${STEP_LOG}"
  "$@" >> "${STEP_LOG}" 2>&1
  rc=$?
  if [ "${rc}" = 0 ]; then
    pass "${name}"
  else
    tail_line="$(tail -n 3 "${STEP_LOG}" | tr '\n' ' ')"
    fail "${name}" "exit ${rc}: ${tail_line}"
  fi
  return "${rc}"
}

# ---- helpers ----------------------------------------------------------------------------------------------------------
# new_token FILE: an enrollment token (myre_ and 40 letters and digits) in a file of mode 0600; never printed
new_token() {
  ( umask 077; printf 'myre_%s' "$(random_token 40)" > "$1" )
}

write_values() {
  cat > "${VALUES_FILE}" <<EOF
# Test values of the server chart: three enrollment tokens from Secrets that this test creates, the test token of "helm test" (its
# own Secret, not the token of an agent), and the namespaces of the collectors as clients of port 8080.
enrollmentTokens:
  - {name: sts, existingSecret: ct-enroll-sts, labels: {source: chart-test}}
  - {name: ds, existingSecret: ct-enroll-ds, labels: {source: chart-test}}
  - {name: helm-test, existingSecret: ct-enroll-helm-test}
tests:
  token:
    existingSecret: ct-enroll-helm-test
networkPolicy:
  ingress:
    from:
      - namespaceSelector:
          matchLabels:
            myriarch.dev/chart-test-client: "true"
EOF
}

# ct_namespace NAME LEVEL [CLIENT]: namespace with the Pod Security level (enforce, warn and audit); CLIENT=1 labels it as a client of
# the server (NetworkPolicy)
ct_namespace() {
  ensure_namespace "$1" "$2"
  if [ "${3:-}" = 1 ]; then k label namespace "$1" myriarch.dev/chart-test-client=true --overwrite >/dev/null; fi
}

# put_token NAMESPACE SECRET FILE: the Secret with the key "token" (create or update; the value comes from the file)
put_token() {
  apply_secret "$1" "$2" "token=$3"
}

# agents_healthy SITE_ID -> number of collector agents of the site that are online and healthy (REST API of the server)
agents_healthy() {
  a "/agents?attr=site.id%3D$1&kind=collector&limit=200" | jq '[.items[]? | select(.connection.status == "online" and .health.healthy == true)] | length'
}
agents_total() {
  a "/agents?attr=site.id%3D$1&kind=collector&limit=200" | jq '[.items[]?] | length'
}

# leftovers NAMESPACE RELEASE -> the resources of the release that still exist (without PVCs: Kubernetes keeps those of a
# StatefulSet on purpose)
leftovers() {
  kn "$1" get all,configmap,serviceaccount,networkpolicy,poddisruptionbudget,ingress -l "app.kubernetes.io/instance=$2" -o name 2>/dev/null \
    | grep -v '^configmap/kube-root-ca.crt$'
  return 0
}

# ---- stage 0: prerequisites -------------------------------------------------------------------------------------------
stage_begin 0 "prerequisites"
for c in docker git python3 curl jq; do
  if have "${c}"; then pass "command ${c}"; else fail "command ${c}"; fatal; fi
done
if docker info >/dev/null 2>&1; then pass "Docker runs"; else fail "Docker runs"; fatal; fi
check_true "values.schema.json of the server chart exists" test -s "${CHART_DIR}/values.schema.json"
check_true "values.schema.json of the collector chart exists" test -s "${COLLECTOR_CHART_DIR}/values.schema.json"

# ---- stage 1: cluster -------------------------------------------------------------------------------------------------
stage_begin 1 "cluster"
if "${KIND_DIR}/up.sh" >> "${PROBE_WORK}/logs/up.log" 2>&1; then pass "kind cluster and registry are up" "${KIND_NODE_IMAGE%%@*}"; else tail -n 20 "${PROBE_WORK}/logs/up.log" >&2; fail "kind cluster and registry are up"; fatal; fi
assert_context
K8S_VERSION="$(k version -o json | jq -r '.serverVersion.gitVersion')"
check_ne "Kubernetes version of the server is known" "" "${K8S_VERSION}"
ready="$(k get nodes -o json | jq '[.items[] | select(.status.conditions[] | select(.type=="Ready" and .status=="True"))] | length')"
check_eq "three nodes Ready" 3 "${ready}"

# ---- stage 2: images --------------------------------------------------------------------------------------------------
stage_begin 2 "images of the commit"
if IMAGES_SET=server,updater "${KIND_DIR}/images.sh" >> "${PROBE_WORK}/logs/images.log" 2>&1; then pass "server and updater image built and pushed"; else tail -n 20 "${PROBE_WORK}/logs/images.log" >&2; fail "server and updater image built and pushed"; fatal; fi
load_images
check_ne "updater image digest" "" "${UPDATER_DIGEST:-}"

# ---- stage 3: PostgreSQL ----------------------------------------------------------------------------------------------
stage_begin 3 "PostgreSQL"
generate_secrets
step "PostgreSQL applied" install_infra || fatal
step "PostgreSQL ready" wait_infra || fatal
write_values
new_token "$(secret_path enroll-sts)"
new_token "$(secret_path enroll-ds)"
new_token "$(secret_path enroll-helm-test)"

# ---- stage 4: server chart --------------------------------------------------------------------------------------------
stage_begin 4 "server chart: install and helm test"
step "chart lints" h lint "${CHART_DIR}" --set config.server.publicUrl="${PUBLIC_URL}" || true
ct_namespace "${NS_APP}" restricted
apply_secret "${NS_APP}" myriarch-secrets "database-dsn=$(secret_path database-dsn)"
put_token "${NS_APP}" ct-enroll-sts "$(secret_path enroll-sts)"
put_token "${NS_APP}" ct-enroll-ds "$(secret_path enroll-ds)"
put_token "${NS_APP}" ct-enroll-helm-test "$(secret_path enroll-helm-test)"
chart_set_args
step "helm install of the server chart (namespace under Pod Security restricted)" \
  h install myriarch "${CHART_DIR}" -n "${NS_APP}" "${CHART_SET[@]}" -f "${VALUES_FILE}" --wait --timeout 6m || fatal
check_eq "the server pods are Ready" "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.replicas}')" "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.status.readyReplicas}')"
step "helm test: /readyz and the OpAMP WebSocket handshake with a test token" h test myriarch -n "${NS_APP}" --timeout 3m --logs
check_eq "the test pod is gone after a successful test (hook-succeeded)" 0 "$(kn "${NS_APP}" get pods -l app.kubernetes.io/component=test --no-headers 2>/dev/null | wc -l | tr -d ' ')"

# the REST API for the agent checks of stage 5
tokfile="$(secret_path bootstrap.token)"
bootstrap_token_from_notes "${tokfile}" || : > "${tokfile}"
api_use main
if pf_ensure; then pass "port-forward to the server"; else fail "port-forward to the server"; fatal; fi
check_eq "bootstrap sign-in through the API" 200 "$(bootstrap_login "${tokfile}" "${API_JAR}")"
session_refresh

# ---- stage 5: collector chart -----------------------------------------------------------------------------------------
stage_begin 5 "collector chart: StatefulSet and DaemonSet"
collector_args=(--set "server.endpoint=${SERVER_ENDPOINT}" --set server.allowInsecureTransport=true
  --set "images.updater.registry=localhost:${KIND_REGISTRY_PORT}" --set images.updater.repository=myriarch/myriarch-updater
  --set "images.updater.digest=${UPDATER_DIGEST}")
step "collector chart lints as a StatefulSet" h lint "${COLLECTOR_CHART_DIR}" --set mode=statefulset --set "server.endpoint=${SERVER_ENDPOINT}" --set server.allowInsecureTransport=true || true
step "collector chart lints as a DaemonSet" h lint "${COLLECTOR_CHART_DIR}" --set mode=daemonset --set "server.endpoint=${SERVER_ENDPOINT}" --set server.allowInsecureTransport=true || true

ct_namespace "${NS_STS}" restricted 1
put_token "${NS_STS}" myriarch-enrollment "$(secret_path enroll-sts)"
step "helm install, StatefulSet with two replicas under Pod Security restricted" \
  h install ct-sts "${COLLECTOR_CHART_DIR}" -n "${NS_STS}" "${collector_args[@]}" --set mode=statefulset --set replicaCount=2 \
  --set site.id=ct-sts --set site.name=chart-test-sts --wait --timeout 8m
check_eq "StatefulSet: both pods Ready" 2 "$(kn "${NS_STS}" get sts ct-sts -o jsonpath='{.status.readyReplicas}' 2>/dev/null)"
check_eq "StatefulSet: the namespace enforces restricted" restricted "$(k get ns "${NS_STS}" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}')"
check_eq "StatefulSet: the pods run as the numeric user 10001" 10001 "$(kn "${NS_STS}" get pod ct-sts-0 -o jsonpath='{.spec.securityContext.runAsUser}')"

# The documented exception: the hostPath volume of nodeLogs is refused by baseline and restricted, so this namespace is privileged.
ct_namespace "${NS_DS}" privileged 1
put_token "${NS_DS}" myriarch-enrollment "$(secret_path enroll-ds)"
step "helm install, DaemonSet with nodeLogs in a privileged namespace" \
  h install ct-ds "${COLLECTOR_CHART_DIR}" -n "${NS_DS}" "${collector_args[@]}" --set mode=daemonset --set nodeLogs.enabled=true \
  --set site.id=ct-ds --set site.name=chart-test-ds --wait --timeout 8m
want_ds="$(kn "${NS_DS}" get ds ct-ds -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null)"
check_ge "DaemonSet: at least one node is scheduled" 1 "${want_ds}"
check_eq "DaemonSet: every scheduled pod is Ready" "${want_ds}" "$(kn "${NS_DS}" get ds ct-ds -o jsonpath='{.status.numberReady}' 2>/dev/null)"

if wait_until 300 "[ \"\$(agents_healthy ct-sts)\" = 2 ]" 3; then pass "StatefulSet: two agents are registered, online and healthy at the server"; else fail "StatefulSet: two agents are registered, online and healthy at the server" "healthy $(agents_healthy ct-sts) of $(agents_total ct-sts)"; fi
if wait_until 300 "[ \"\$(agents_healthy ct-ds)\" = '${want_ds}' ]" 3; then pass "DaemonSet: one healthy agent per node at the server" "${want_ds}"; else fail "DaemonSet: one healthy agent per node at the server" "healthy $(agents_healthy ct-ds) of $(agents_total ct-ds), want ${want_ds}"; fi

# a restarted pod keeps its identity: the storage volume holds the instance UID
before="$(agents_total ct-sts)"
step "StatefulSet: a deleted pod comes back" kn "${NS_STS}" delete pod ct-sts-0 --wait=true --timeout=120s
step "StatefulSet: the StatefulSet is Ready again" kn "${NS_STS}" rollout status sts/ct-sts --timeout=300s
if wait_until 300 "[ \"\$(agents_healthy ct-sts)\" = 2 ]" 3; then pass "StatefulSet: the restarted pod is the same agent (no third agent)" "$(agents_total ct-sts) agents, before ${before}"; else fail "StatefulSet: the restarted pod is the same agent (no third agent)" "healthy $(agents_healthy ct-sts) of $(agents_total ct-sts)"; fi

# ---- stage 6: uninstall -----------------------------------------------------------------------------------------------
stage_begin 6 "clean uninstall"
step "helm uninstall of the DaemonSet" h uninstall ct-ds -n "${NS_DS}" --wait --timeout 3m
check_eq "DaemonSet: nothing of the release is left" "" "$(leftovers "${NS_DS}" ct-ds)"
step "helm uninstall of the StatefulSet" h uninstall ct-sts -n "${NS_STS}" --wait --timeout 3m
check_eq "StatefulSet: nothing of the release is left (the PVCs stay by design)" "" "$(leftovers "${NS_STS}" ct-sts)"
check_eq "StatefulSet: the two PVCs stay and are removed by hand" 2 "$(kn "${NS_STS}" get pvc --no-headers 2>/dev/null | wc -l | tr -d ' ')"
step "the PVCs of the StatefulSet are deleted" kn "${NS_STS}" delete pvc --all --wait=true --timeout=120s
pf_stop_all
step "helm uninstall of the server chart" h uninstall myriarch -n "${NS_APP}" --wait --timeout 3m
check_eq "server: nothing of the release is left" "" "$(leftovers "${NS_APP}" myriarch)"
check_eq "no Helm release is left in the namespaces of the test" "" "$(for ns in ${NS_APP} ${NS_STS} ${NS_DS}; do h list -n "${ns}" -q; done)"

# ---- stage 7: upgrade from the chart of the tag -----------------------------------------------------------------------
stage_begin 7 "helm upgrade from ${UPGRADE_FROM}"
if git rev-parse -q --verify "refs/tags/${UPGRADE_FROM}^{commit}" >/dev/null 2>&1 \
  || git fetch --quiet --depth=1 origin "refs/tags/${UPGRADE_FROM}:refs/tags/${UPGRADE_FROM}" >> "${STEP_LOG}" 2>&1; then
  pass "the tag ${UPGRADE_FROM} is available"
else
  fail "the tag ${UPGRADE_FROM} is available" "git fetch of the tag failed (the checkout needs the tags: fetch-tags in CI)"
  fatal
fi
rm -rf "${OLD_CHART_DIR}"
mkdir -p "${OLD_CHART_DIR}"
if git archive "${UPGRADE_FROM}" deploy/helm/myriarch | tar -x -C "${OLD_CHART_DIR}"; then pass "the chart of ${UPGRADE_FROM} is taken from Git"; else fail "the chart of ${UPGRADE_FROM} is taken from Git"; fatal; fi
OLD_CHART="${OLD_CHART_DIR}/deploy/helm/myriarch"
old_version="$(sed -n 's/^version: //p' "${OLD_CHART}/Chart.yaml" | head -n 1)"
check_ne "the old chart is not the chart of the commit" "$(sed -n 's/^version: //p' "${CHART_DIR}/Chart.yaml" | head -n 1)" "${old_version}"

# A second database (the Dex stage of the probe uses it too), so that the old image meets an empty schema. The old chart runs the image
# of its own release (appVersion); the chart of the commit runs the image of the commit.
ct_namespace "${NS_UPGRADE}" restricted
apply_secret "${NS_UPGRADE}" myriarch-secrets "database-dsn=$(secret_path database-dsn-dex)"
put_token "${NS_UPGRADE}" ct-enroll-sts "$(secret_path enroll-sts)"
put_token "${NS_UPGRADE}" ct-enroll-ds "$(secret_path enroll-ds)"
put_token "${NS_UPGRADE}" ct-enroll-helm-test "$(secret_path enroll-helm-test)"
step "helm install of the chart of ${UPGRADE_FROM} (its own image)" \
  h install myriarch "${OLD_CHART}" -n "${NS_UPGRADE}" --set "config.server.publicUrl=${PUBLIC_URL}" --wait --timeout 8m || fatal
check_eq "release revision before the upgrade" 1 "$(h list -n "${NS_UPGRADE}" -o json | jq -r '.[0].revision')"
step "helm upgrade to the chart of the commit (image of the commit, test token)" \
  h upgrade myriarch "${CHART_DIR}" -n "${NS_UPGRADE}" "${CHART_SET[@]}" -f "${VALUES_FILE}" --wait --timeout 8m || fatal
check_eq "release revision after the upgrade" 2 "$(h list -n "${NS_UPGRADE}" -o json | jq -r '.[0].revision')"
check_eq "release status after the upgrade" deployed "$(h list -n "${NS_UPGRADE}" -o json | jq -r '.[0].status')"
check_eq "the server runs the image of the commit" "${SERVER_REPO}@${SERVER_DIGEST}" "$(kn "${NS_UPGRADE}" get deploy myriarch -o jsonpath='{.spec.template.spec.containers[0].image}')"
check_eq "the server pods are Ready after the upgrade" "$(kn "${NS_UPGRADE}" get deploy myriarch -o jsonpath='{.spec.replicas}')" "$(kn "${NS_UPGRADE}" get deploy myriarch -o jsonpath='{.status.readyReplicas}')"
step "helm test after the upgrade" h test myriarch -n "${NS_UPGRADE}" --timeout 3m --logs
step "helm uninstall after the upgrade" h uninstall myriarch -n "${NS_UPGRADE}" --wait --timeout 3m
check_eq "upgrade release: nothing is left" "" "$(leftovers "${NS_UPGRADE}" myriarch)"

stage_end
summary
[ "${FAILED}" = 0 ]
