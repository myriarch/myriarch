#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# The Kubernetes Quickstart on kind (docs/getting-started/kubernetes-kind.md): a cluster, the server chart, the collector chart as a
# gateway (StatefulSet in container mode) and as a node agent (DaemonSet), an optional simulator, a smoke check and a first
# configuration and version rollout. The documentation and the CI job run these same commands, so the page cannot go stale.
#
#   hack/kind/quickstart.sh up         cluster, installation and the wait for the pods (idempotent: a second run upgrades in place)
#   hack/kind/quickstart.sh smoke      checks the installation (pods, "helm test", the collectors); never signs in
#   hack/kind/quickstart.sh rollouts   signs in with the one-time bootstrap token and runs a configuration and a version rollout
#                                      through the REST API, like the UI does (approves the waves); measures the durations
#   hack/kind/quickstart.sh all        up, smoke and rollouts, with the durations in $PROBE_WORK/quickstart-summary.md (CI)
#   hack/kind/quickstart.sh config-change [TIMEOUT]   pushes a change of the batch timeout of the blueprint (default 7s) to the Git repository of
#                                      the cluster: the first configuration rollout, which you approve in the UI
#   hack/kind/quickstart.sh validate   validates the GitOps repository of the Quickstart (with and without the simulator); needs Go only
#   hack/kind/quickstart.sh down       deletes the cluster (same as hack/kind/down.sh --all)
#
# Where the charts and images come from (QUICKSTART_SOURCE):
#   release (default)  the published charts oci://ghcr.io/myriarch/charts/myriarch and .../myriarch-collector and the release images
#                      that they name; the version of the charts is the one of this checkout (use a checkout of the release tag).
#                      Available from v0.2.0.
#   commit             the charts of this checkout and images built from it (hack/kind/images.sh); needs a clean work tree
#                      (ALLOW_DIRTY=1 for development). CI uses this.
#
# Environment: QUICKSTART_SOURCE, QUICKSTART_SIM=1 (also start 20 simulated collectors; without it a command uses the simulator if the
# cluster has it, 0 if the cluster says it has none, and stops if it cannot tell), QUICKSTART_CHART_VERSION (release: default is
# the version of deploy/helm/myriarch/Chart.yaml), QUICKSTART_OCI_BASE (default oci://ghcr.io/myriarch/charts), KIND_SYSTEM_TOOLS=1 (use
# kind, kubectl and helm from the PATH instead of building the pinned ones), and the variables of hack/kind/README.md (KIND_CLUSTER_NAME,
# KIND_NODE_IMAGE, PROBE_WORK, PROBE_PORT_API, ...). The rollout step needs Go (it builds the small tool that fetches the Sigstore trusted
# root), "up" needs Docker, git, curl, jq, python3, openssl and ssh-keygen.
#
# DEMO SETUP ONLY: PostgreSQL and the Git server are throw-away pods of the namespace probe-infra, the network inside the cluster is
# unencrypted, and the generated secrets are files in $PROBE_WORK/secrets (mode 0600). Nothing is printed that is a secret. The bootstrap
# token is read by "rollouts" for one sign-in only; if you want to sign in to the UI yourself, run "rollouts" instead of the UI, or rotate
# the token as the page describes.
set -euo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"
cd "${ROOT}"

: "${QUICKSTART_SOURCE:=release}"
: "${QUICKSTART_OCI_BASE:=oci://ghcr.io/myriarch/charts}"
QS_GW_RELEASE='kind-gw'
QS_NODES_RELEASE='kind-nodes'
QS_GW_STS="${QS_GW_RELEASE}-myriarch-collector"
QS_NODES_DS="${QS_NODES_RELEASE}-myriarch-collector"
QS_SUMMARY="${PROBE_WORK}/quickstart-summary.md"
QS_TIMINGS="${PROBE_WORK}/quickstart-timings.txt"
# the collector the gateway starts on and the one the first version rollout goes to (the frame in Git offers both)
QS_FROM_VERSION=0.161.0
QS_TO_VERSION=0.162.0
# upper bounds of the rollout checks (seconds); the measured values are far below them
QS_CONFIG_TIMEOUT=300
QS_VERSION_TIMEOUT=900
# after the approval of the last wave a version rollout is about a minute from "succeeded" (30 s health wait, restart, download)
QS_VERSION_AFTER_APPROVAL_MAX=240

case "${QUICKSTART_SOURCE}" in release | commit) ;; *) die "QUICKSTART_SOURCE must be release or commit, not '${QUICKSTART_SOURCE}'" ;; esac

timing() { # name seconds
  printf '%s|%s\n' "$1" "$2" >> "${QS_TIMINGS}"
}

# image_part IMAGE_REF PART -> repository | tag | digest of "repo:tag@sha256:..." (the pins of versions.env)
image_part() {
  local ref="$1" base
  base="${ref%@*}"
  case "$2" in
    repository) printf '%s' "${base%:*}" ;;
    tag) printf '%s' "${base##*:}" ;;
    digest) printf '%s' "${ref##*@}" ;;
  esac
}

server_chart_version() {
  sed -n 's/^version: //p' "${CHART_DIR}/Chart.yaml" | head -n 1
}

# ---- tools built from this checkout -----------------------------------------------------------------------------------
ensure_bins() {
  need_cmd go
  mkdir -p bin
  [ -x bin/myrctl ] || go build -o bin/myrctl ./cmd/myrctl
  [ -x bin/myriarch-updater ] || go build -o bin/myriarch-updater ./cmd/myriarch-updater
}

# the offline Sigstore trusted root for the updater of the gateway (ConfigMap myriarch-trust); fetched through TUF, needs network once
ensure_trusted_root() {
  local f="${PROBE_WORK}/trust/trusted_root.json"
  [ -s "${f}" ] && return 0
  ensure_bins
  log "fetching the Sigstore trusted root (TUF)"
  bin/myriarch-updater fetch-trusted-root --out "${f}" >&2 || die "fetch-trusted-root failed (network access to tuf-repo-cdn.sigstore.dev?)"
}

# ---- GitOps repository ------------------------------------------------------------------------------------------------
# qs_gitops_prepare: the Quickstart repository in $GITOPS_WORK: the fleets and the blueprint of hack/kind/quickstart/gitops, the demo
# policies of examples/quickstart (one copy of the demo values for both Quickstarts) and the canary wave of this cluster.
qs_gitops_prepare() {
  rm -rf "${GITOPS_WORK}"
  mkdir -p "${GITOPS_WORK}/policies" "${GITOPS_WORK}/inventory"
  cp -R "${KIND_DIR}/quickstart/gitops/." "${GITOPS_WORK}/"
  cp "${ROOT}/examples/quickstart/policies/canary-then-all.yaml" "${ROOT}/examples/quickstart/policies/config-canary.yaml" "${GITOPS_WORK}/policies/"
  cat > "${GITOPS_WORK}/inventory/waves.yaml" <<EOF
# The canary wave of the Quickstart: the first gateway pod and the node agent of the first worker node (the gateway pods report k8s.node.name too, so the node entry names the site; the rest wave follows after your approval).
apiVersion: myriarch.dev/v1alpha1
kind: Inventory
metadata:
  name: waves
  description: Canary wave of the kind Quickstart
spec:
  entries:
    - match:
        k8s.pod.name: ${QS_GW_STS}-0
      labels:
        wave: canary
    - match:
        site.id: kind-nodes
        k8s.node.name: ${KIND_CLUSTER_NAME}-worker
      labels:
        wave: canary
EOF
  if [ "${QUICKSTART_SIM}" = 1 ]; then
    cp -R "${KIND_DIR}/quickstart/sim/." "${GITOPS_WORK}/"
    cat >> "${GITOPS_WORK}/inventory/waves.yaml" <<'EOF'
    - match:
        site.id: edge-001
      labels:
        wave: canary
    - match:
        site.id: dc-01
      labels:
        wave: canary
EOF
  fi
  ensure_bins
  bin/myrctl validate --strict --components "${ROOT}/examples/sim/components-otelcol-contrib-0.162.0.json" "${GITOPS_WORK}" >&2
}

# check_canary_entries: the gateway pods report k8s.node.name as well, so an entry that matches a node must name the node site too
# (the keys of one entry are ANDed); otherwise both gateway pods can land in the canary wave and the rest wave stays empty.
check_canary_entries() {
  local bad
  bad="$(awk '/k8s.node.name:/ && prev !~ /site.id: kind-nodes/ { print NR } { prev = $0 }' "${GITOPS_WORK}/inventory/waves.yaml")"
  [ -z "${bad}" ] || die "inventory/waves.yaml: the node entry (line ${bad}) must also match site.id: kind-nodes"
  grep -q 'k8s.pod.name: .*-0$' "${GITOPS_WORK}/inventory/waves.yaml" || die "inventory/waves.yaml: the canary wave lacks the first gateway pod"
}

# ---- charts -----------------------------------------------------------------------------------------------------------
# chart_ref NAME DIR -> the chart to install: the directory of the commit, or the published OCI chart
chart_ref() {
  if [ "${QUICKSTART_SOURCE}" = commit ]; then printf '%s' "$2"; else printf '%s/%s' "${QUICKSTART_OCI_BASE}" "$1"; fi
}
chart_version_args() { # sets CHART_VER_ARGS
  CHART_VER_ARGS=()
  if [ "${QUICKSTART_SOURCE}" = release ]; then CHART_VER_ARGS=(--version "${QUICKSTART_CHART_VERSION:-$(server_chart_version)}"); fi
}

install_server() {
  ensure_namespace "${NS_APP}" restricted
  apply_secret "${NS_APP}" myriarch-secrets "database-dsn=$(secret_path database-dsn)" \
    "git-ssh-key=$(secret_path git-ssh-key)" "git-known-hosts=$(secret_path git-known-hosts)"
  local args=(--set "config.server.publicUrl=${PUBLIC_URL}")
  if [ "${QUICKSTART_SOURCE}" = commit ]; then args+=(--set "image.repository=${SERVER_REPO}" --set "image.digest=${SERVER_DIGEST}"); fi
  chart_version_args
  log "installing the server chart (${QUICKSTART_SOURCE})"
  h upgrade --install myriarch "$(chart_ref myriarch "${CHART_DIR}")" "${CHART_VER_ARGS[@]}" -n "${NS_APP}" "${args[@]}" \
    -f "${KIND_DIR}/values/quickstart.yaml" --wait --timeout 8m > "${PROBE_WORK}/notes-quickstart.txt"
}

# collector_values GATEWAY|NODES -> the values file of the release (no secrets: names, images and sizes)
collector_values() {
  local kind="$1" file="${PROBE_WORK}/values-quickstart-${1}.yaml" mode site_id site_name token
  if [ "${kind}" = GATEWAY ]; then mode=statefulset; site_id='kind-gateway'; site_name="kind gateway"; token='kind-gw'
  else mode=daemonset; site_id='kind-nodes'; site_name="kind nodes"; token='kind-nodes'; fi
  {
    cat <<EOF
mode: ${mode}
server:
  endpoint: ws://myriarch:8080/v1/opamp
  allowInsecureTransport: true
enrollment:
  existingSecret: myriarch-enrollment-${token}
site:
  id: ${site_id}
  name: ${site_name}
  region: local
  timezone: UTC
  environment: quickstart
  clusterName: kind-${KIND_CLUSTER_NAME}
EOF
    if [ "${kind}" = GATEWAY ]; then
      # two replicas: the first is the canary wave, the second the rest. The collector starts on the older version, so that the version
      # rollout of this Quickstart has something to update; the image digests are the pins of hack/kind/versions.env.
      cat <<EOF
replicaCount: 2
containerMode:
  enabled: true
  trustedRoot:
    existingConfigMap: myriarch-trust
resources:
  requests: {cpu: 250m, memory: 768Mi}
  limits: {memory: 768Mi}
images:
  collector:
    repository: $(image_part "${OTELCOL_PREV_CONTRIB_IMAGE}" repository)
    tag: "$(image_part "${OTELCOL_PREV_CONTRIB_IMAGE}" tag)"
    digest: $(image_part "${OTELCOL_PREV_CONTRIB_IMAGE}" digest)
  supervisor:
    repository: $(image_part "${OTELCOL_PREV_SUPERVISOR_IMAGE}" repository)
    tag: "$(image_part "${OTELCOL_PREV_SUPERVISOR_IMAGE}" tag)"
    digest: $(image_part "${OTELCOL_PREV_SUPERVISOR_IMAGE}" digest)
EOF
    fi
    if [ "${QUICKSTART_SOURCE}" = commit ]; then
      # the updater image of the commit in the local registry (the nodes reach it as localhost:5001)
      if [ "${kind}" = GATEWAY ]; then printf '  updater:\n'; else printf 'images:\n  updater:\n'; fi
      cat <<EOF
    registry: localhost:${KIND_REGISTRY_PORT}
    repository: myriarch/myriarch-updater
    digest: ${UPDATER_DIGEST}
EOF
    fi
  } > "${file}"
  printf '%s' "${file}"
}

install_collectors() {
  local gw nodes
  gw="$(collector_values GATEWAY)"
  nodes="$(collector_values NODES)"
  kn "${NS_APP}" create configmap myriarch-trust --from-file="trusted_root.json=${PROBE_WORK}/trust/trusted_root.json" --dry-run=client -o yaml | k apply -f - >/dev/null
  chart_version_args
  log "installing the collector chart as a gateway (StatefulSet, container mode)"
  h upgrade --install "${QS_GW_RELEASE}" "$(chart_ref myriarch-collector "${ROOT}/deploy/helm/myriarch-collector")" "${CHART_VER_ARGS[@]}" \
    -n "${NS_APP}" -f "${gw}" --wait --timeout 10m > "${PROBE_WORK}/notes-quickstart-gateway.txt"
  log "installing the collector chart as a node agent (DaemonSet)"
  h upgrade --install "${QS_NODES_RELEASE}" "$(chart_ref myriarch-collector "${ROOT}/deploy/helm/myriarch-collector")" "${CHART_VER_ARGS[@]}" \
    -n "${NS_APP}" -f "${nodes}" --wait --timeout 10m > "${PROBE_WORK}/notes-quickstart-nodes.txt"
}

install_sim() {
  [ "${QUICKSTART_SIM}" = 1 ] || return 0
  local image
  image="$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.template.spec.containers[0].image}')"
  log "starting the simulator (20 collectors)"
  SERVER_IMAGE="${image}" render_manifest "${KIND_DIR}/manifests/sim.yaml" SERVER_IMAGE | k apply -f - >/dev/null
  kn "${NS_APP}" rollout status deploy/myriarch-sim --timeout=300s >&2
}

# resolve_sim: QUICKSTART_SIM as the cluster has it when the caller does not say: 1 if the simulator is installed, else 0. Every command
# rebuilds the Git state from this choice, so a later command without QUICKSTART_SIM=1 must not drop the simulated fleets.
resolve_sim() {
  if [ -z "${QUICKSTART_SIM:-}" ]; then
    local found
    # --ignore-not-found: an absent simulator is 0, but an error (the API server is unreachable, the context is wrong) stops the command
    # instead of being read as "no simulator", which would drop the simulated fleets from the Git state.
    found="$(kn "${NS_APP}" get deploy myriarch-sim --ignore-not-found -o name)" || die "cannot tell whether the simulator is installed; set QUICKSTART_SIM=0 or 1 or fix the access to the cluster"
    if [ -n "${found}" ]; then QUICKSTART_SIM=1; else QUICKSTART_SIM=0; fi
  fi
  export QUICKSTART_SIM
}

# ---- up ---------------------------------------------------------------------------------------------------------------
cmd_up() {
  local start=${SECONDS}
  need_cmd docker git curl jq python3 openssl ssh-keygen
  prepare_work_dirs
  "${KIND_DIR}/up.sh"
  assert_context
  if [ "${QUICKSTART_SOURCE}" = commit ]; then
    log "building the images of the commit"
    IMAGES_SET=server,updater "${KIND_DIR}/images.sh" >&2
  fi
  ensure_trusted_root
  if [ "${QUICKSTART_SOURCE}" = commit ]; then load_images; fi
  generate_secrets
  log "installing PostgreSQL and the Git server"
  install_infra
  wait_infra
  resolve_sim
  qs_gitops_prepare
  # A second run keeps the repository as it is: the tutorial may have changed it since (config-change). Only the simulated fleets are
  # added when QUICKSTART_SIM=1 and they are missing; that publish resets a batch timeout set by config-change.
  if GITOPS_SHA="$(kn "${NS_INFRA}" exec deploy/git -- git --git-dir=/srv/git/fleet.git rev-parse --verify -q refs/heads/main 2>/dev/null)"; then
    if [ "${QUICKSTART_SIM}" = 1 ] && ! kn "${NS_INFRA}" exec deploy/git -- git --git-dir=/srv/git/fleet.git cat-file -e main:fleets/sim-edge.yaml 2>/dev/null; then
      warn "the GitOps repository lacks the simulated fleets; publishing the initial state again (a batch timeout from config-change is reset)"
      gitops_publish "quickstart: initial state with the simulator"
      log "GitOps repository pushed (${GITOPS_SHA})"
    else
      log "the GitOps repository has commits already (${GITOPS_SHA}); it stays as it is"
    fi
  else
    gitops_publish "quickstart: initial state"
    log "GitOps repository pushed (${GITOPS_SHA})"
  fi
  install_server
  install_collectors
  install_sim
  timing "up" "$((SECONDS - start))"
  cat <<TXT

The installation is ready ($((SECONDS - start)) s). Reach the UI:
  export KUBECONFIG=${KUBECONFIG}
  kubectl -n ${NS_APP} port-forward svc/myriarch 8080:8080      # then open http://localhost:8080
Sign in as the bootstrap admin "admin"; read the one-time token (it works for one sign-in):
  kubectl -n ${NS_APP} get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d
TXT
}

# ---- smoke ------------------------------------------------------------------------------------------------------------
SMOKE_FAILED=0
smoke_check() { # name command...
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then printf '  ok    %s\n' "${name}" >&2; else printf '  FAIL  %s\n' "${name}" >&2; SMOKE_FAILED=$((SMOKE_FAILED + 1)); fi
}
ready_all() { # kind name  -> every wanted replica is ready
  local want got
  want="$(kn "${NS_APP}" get "$1" "$2" -o jsonpath="{.${3}}")"
  got="$(kn "${NS_APP}" get "$1" "$2" -o jsonpath="{.${4}}")"
  [ -n "${want}" ] && [ "${want}" = "${got:-0}" ]
}
cmd_smoke() {
  assert_context
  resolve_sim
  SMOKE_FAILED=0
  smoke_check "all nodes are Ready" k wait --for=condition=Ready node --all --timeout=60s
  smoke_check "the server pods are Ready" ready_all deploy myriarch spec.replicas status.readyReplicas
  smoke_check "helm test: /readyz and the OpAMP handshake" h test myriarch -n "${NS_APP}" --timeout 3m
  smoke_check "the gateway pods are Ready (StatefulSet)" ready_all statefulset "${QS_GW_STS}" spec.replicas status.readyReplicas
  smoke_check "every node agent is Ready (DaemonSet)" ready_all daemonset "${QS_NODES_DS}" status.desiredNumberScheduled status.numberReady
  if [ "${QUICKSTART_SIM}" = 1 ]; then smoke_check "the simulator is Ready" ready_all deploy myriarch-sim spec.replicas status.readyReplicas; fi
  [ "${SMOKE_FAILED}" = 0 ] || die "smoke check: ${SMOKE_FAILED} check(s) failed (hack/kind/diag.sh collects diagnostics)"
  log "smoke check passed"
}

# ---- rollouts ---------------------------------------------------------------------------------------------------------
agents_healthy() { # site.id
  a "/agents?attr=site.id%3D$1&kind=collector&limit=200" | jq '[.items[]? | select(.status == "online" and .healthy == true)] | length'
}

qs_login() {
  api_use main
  pf_ensure || die "no port-forward to the server"
  local tokfile="${SECRETS_DIR}/bootstrap.token" code
  bootstrap_token_from_notes "${tokfile}" || die "cannot read the bootstrap token (rotate it as docs/getting-started/kubernetes-kind.md describes)"
  code="$(bootstrap_login "${tokfile}" "${API_JAR}")"
  [ "${code}" = 200 ] || die "the bootstrap sign-in answered ${code}: the one-time token is spent (you signed in already?). Rotate it as docs/getting-started/kubernetes-kind.md describes"
  session_refresh
}

# drive_rollout ID TIMEOUT -> 0 when the rollout completed; approves every wave that waits for it. Sets DRIVE_APPROVED_AT (the second
# of the last approval) and DRIVE_DONE_AT.
DRIVE_APPROVED_AT=0
DRIVE_DONE_AT=0
drive_rollout() {
  local id="$1" end=$((SECONDS + $2)) st wave
  DRIVE_APPROVED_AT=0
  while [ "${SECONDS}" -lt "${end}" ]; do
    st="$(a "/rollouts/${id}" | jq -r '.status // empty')"
    case "${st}" in
      waiting_approval)
        wave="$(a "/rollouts/${id}" | jq -r '[.waves[]? | select(.status == "waiting_approval")][0].index // empty')"
        if [ -n "${wave}" ]; then
          ap "/rollouts/${id}/actions/approve" "$(jq -n --argjson w "${wave}" '{wave:$w, reason:"kind Quickstart"}')" >/dev/null
          DRIVE_APPROVED_AT=${SECONDS}
          log "rollout ${id}: wave ${wave} approved"
        fi
        ;;
      completed)
        DRIVE_DONE_AT=${SECONDS}
        [ "${DRIVE_APPROVED_AT}" != 0 ] || { warn "rollout ${id} completed without waiting for an approval (an empty rest wave?); check the waves in inventory/waves.yaml"; return 1; }
        return 0
        ;;
      paused | failed | aborted | rolling_back | rolled_back | superseded)
        warn "rollout ${id} ended as ${st}"
        return 1
        ;;
    esac
    sleep 1
  done
  warn "rollout ${id} did not finish in $2 s (status ${st:-unknown})"
  return 1
}

# cmd_config_change [TIMEOUT]: the blueprint of the Quickstart with another batch timeout, pushed to the Git repository of the cluster
# (everything else is the initial state, so a second call with another value replaces the first)
cmd_config_change() {
  local timeout="${1:-7s}"
  case "${timeout}" in [0-9]*s) ;; *) die "the batch timeout must be a number of seconds like 7s, not '${timeout}'" ;; esac
  assert_context
  prepare_work_dirs
  resolve_sim
  qs_gitops_prepare
  sed -e "s/default: 5s/default: ${timeout}/" "${GITOPS_WORK}/blueprints/kind-collector.yaml" > "${GITOPS_WORK}/blueprints/kind-collector.yaml.new"
  mv "${GITOPS_WORK}/blueprints/kind-collector.yaml.new" "${GITOPS_WORK}/blueprints/kind-collector.yaml"
  gitops_publish "quickstart: batch timeout ${timeout}"
  log "pushed the batch timeout ${timeout} (commit ${GITOPS_SHA}); the server reads Git every 10 seconds and starts the configuration rollouts"
}

cmd_rollouts() {
  assert_context
  prepare_work_dirs
  qs_login
  local want_nodes id start t_cfg
  want_nodes="$(kn "${NS_APP}" get ds "${QS_NODES_DS}" -o jsonpath='{.status.desiredNumberScheduled}')"
  log "waiting for the collectors to register"
  wait_until 300 "[ \"\$(agents_healthy kind-gateway)\" = 2 ] && [ \"\$(agents_healthy kind-nodes)\" = '${want_nodes}' ]" 3 \
    || die "the collectors did not register healthy (gateway $(agents_healthy kind-gateway) of 2, nodes $(agents_healthy kind-nodes) of ${want_nodes})"

  # ---- configuration rollout: a change of the blueprint in Git, like a merge request would bring
  log "configuration rollout: batch timeout 5s to 7s in Git"
  start=${SECONDS}
  cmd_config_change 7s
  ap /gitops/actions/sync >/dev/null
  wait_until 120 "[ \"\$(a /gitops/status | jq -r .appliedCommit.sha)\" = '${GITOPS_SHA}' ]" 2 || die "the server did not apply the commit ${GITOPS_SHA}"
  for fleet in kind-gateway kind-nodes; do
    wait_until 90 "[ -n \"\$(latest_rollout ${fleet} config)\" ]" 2 || die "no configuration rollout for the fleet ${fleet}"
    id="$(latest_rollout "${fleet}" config)"
    drive_rollout "${id}" "${QS_CONFIG_TIMEOUT}" || die "the configuration rollout of ${fleet} did not complete"
    timing "config-rollout-${fleet}-after-approval" "$((DRIVE_DONE_AT - DRIVE_APPROVED_AT))"
  done
  t_cfg=$((SECONDS - start))
  timing "config-rollouts" "${t_cfg}"
  log "configuration rollouts completed in ${t_cfg} s"

  # ---- version rollout of the gateway: the dialog of the UI calls select-version
  log "version rollout: gateway ${QS_FROM_VERSION} to ${QS_TO_VERSION}"
  start=${SECONDS}
  local resp
  resp="$(ap /fleets/kind-gateway/actions/select-version "$(jq -n --arg v "${QS_TO_VERSION}" '{version:$v, reason:"kind Quickstart"}')")"
  id="$(printf '%s' "${resp}" | jq -r '.rolloutId // empty')"
  if [ -z "${id}" ]; then
    wait_until 90 "[ -n \"\$(latest_rollout kind-gateway version)\" ]" 2 || die "no version rollout for the fleet kind-gateway: ${resp}"
    id="$(latest_rollout kind-gateway version)"
  fi
  drive_rollout "${id}" "${QS_VERSION_TIMEOUT}" || die "the version rollout did not complete"
  timing "version-rollout" "$((SECONDS - start))"
  timing "version-rollout-after-last-approval" "$((DRIVE_DONE_AT - DRIVE_APPROVED_AT))"
  log "version rollout completed ($((SECONDS - start)) s; $((DRIVE_DONE_AT - DRIVE_APPROVED_AT)) s after the approval of the last wave)"
  [ "$((DRIVE_DONE_AT - DRIVE_APPROVED_AT))" -le "${QS_VERSION_AFTER_APPROVAL_MAX}" ] \
    || die "the version rollout took $((DRIVE_DONE_AT - DRIVE_APPROVED_AT)) s after the last approval (limit ${QS_VERSION_AFTER_APPROVAL_MAX} s)"
  wait_until 90 "[ \"\$(a '/agents?attr=site.id%3Dkind-gateway&kind=collector&limit=200' | jq '[.items[]? | select(.version == \"${QS_TO_VERSION}\")] | length')\" = 2 ]" 3 \
    || die "the gateway agents do not report ${QS_TO_VERSION}"
  log "both gateway agents report ${QS_TO_VERSION}"
  pf_stop_all
}

# ---- summary ----------------------------------------------------------------------------------------------------------
write_summary() { # total-seconds
  {
    printf '### Kubernetes Quickstart on kind\n\n'
    printf 'Source: %s, commit %s. Total: %s s (%s min %s s).\n\n' "${QUICKSTART_SOURCE}" "$(git rev-parse --short HEAD)" "$1" "$(($1 / 60))" "$(($1 % 60))"
    printf '| Step | Seconds |\n|---|---|\n'
    while IFS='|' read -r n s; do printf '| %s | %s |\n' "${n}" "${s}"; done < "${QS_TIMINGS}"
  } > "${QS_SUMMARY}"
  cat "${QS_SUMMARY}"
}

cmd_all() {
  local start=${SECONDS}
  prepare_work_dirs
  : > "${QS_TIMINGS}"
  cmd_up
  local s=${SECONDS}
  cmd_smoke
  timing "smoke" "$((SECONDS - s))"
  cmd_rollouts
  timing "total" "$((SECONDS - start))"
  write_summary "$((SECONDS - start))"
}

case "${1:-}" in
  up) prepare_work_dirs; cmd_up ;;
  validate) prepare_work_dirs; QUICKSTART_SIM=0 qs_gitops_prepare; check_canary_entries; QUICKSTART_SIM=1 qs_gitops_prepare; check_canary_entries ;;
  config-change) shift; cmd_config_change "$@" ;;
  smoke) cmd_smoke ;;
  rollouts) cmd_rollouts ;;
  all) cmd_all ;;
  down) exec "${KIND_DIR}/down.sh" --all ;;
  -h | --help | "") sed -n '2,/^set -euo pipefail$/p' "$0" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; [ -n "${1:-}" ] ;;
  *) die "usage: hack/kind/quickstart.sh up | smoke | rollouts | config-change [TIMEOUT] | all | validate | down" ;;
esac
