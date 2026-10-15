#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Probe stages 0 to 8 (sourced by hack/kind/probe.sh after lib/common.sh, lib/install.sh and lib/api.sh): prerequisites,
# cluster, images, infrastructure, the chart with only the required values, the bootstrap admin, the full configuration,
# the agents and the configuration rollout. Every check prints PASS, FAIL or SKIP with N.M numbering (lib/common.sh).
#
# Checks never run in a subshell or a pipeline (the counters would be lost). Secret values are never printed: tokens live in
# files below $PROBE_WORK/secrets, comparisons print only equal / not equal.

STATE_FILE="${PROBE_WORK}/state.env"
state_set() { # key value (no secrets)
  mkdir -p "${PROBE_WORK}"
  if [ -f "${STATE_FILE}" ]; then grep -v "^$1=" "${STATE_FILE}" > "${STATE_FILE}.tmp" || true; mv "${STATE_FILE}.tmp" "${STATE_FILE}"; fi
  printf '%s=%s\n' "$1" "$2" >> "${STATE_FILE}"
}
state_get() { [ -f "${STATE_FILE}" ] && sed -n "s/^$1=//p" "${STATE_FILE}" | tail -n 1; return 0; }
state_load() { # exports the saved values as STATE_<KEY>
  local line
  [ -f "${STATE_FILE}" ] || return 0
  while IFS= read -r line; do
    [ -n "${line}" ] || continue
    export "STATE_${line%%=*}=${line#*=}"
  done < "${STATE_FILE}"
}

port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

# fatal MESSAGE: a failed check after which later stages make no sense (probe.sh defines probe_fatal)
fatal() { fail "$1" "${2:-}"; probe_fatal; }

# ---- stage 0 -----------------------------------------------------------------------------------------------------
stage_0() {
  stage_begin 0 "Prerequisites"
  local c missing=""
  if docker info >/dev/null 2>&1; then pass "Docker engine reachable" "$(docker info --format '{{.ServerVersion}} {{.Architecture}} {{.NCPU}} CPUs')"; else fatal "Docker engine reachable" "start Docker Desktop"; fi
  for c in git curl jq python3 openssl ssh-keygen go make tar; do have "${c}" || missing="${missing} ${c}"; done
  if [ -z "${missing}" ]; then pass "required commands present"; else fatal "required commands present" "missing:${missing}"; fi
  local p busy=""
  for p in "${PROBE_PORT_API}" "${PROBE_PORT_DEXAPI}" "${PROBE_PORT_DEX}"; do port_free "${p}" || busy="${busy} ${p}"; done
  if [ -z "${busy}" ]; then pass "local ports free" "${PROBE_PORT_API} ${PROBE_PORT_DEXAPI} ${PROBE_PORT_DEX}"; else fatal "local ports free" "in use:${busy} (PROBE_PORT_API, PROBE_PORT_DEXAPI, PROBE_PORT_DEX)"; fi
  # cosign: the image build checks the upstream collector images against the signed release artifacts (stage 2.1)
  if "${ROOT}/ci/install-tools.sh" kind kubectl helm crane yq cosign >"${PROBE_WORK}/logs/tools.log" 2>&1; then
    pass "pinned tools installed" "kind $(kind version | awk '{print $2}'), kubectl $(kubectl version --client -o json | jq -r .clientVersion.gitVersion), helm $(helm version --short), crane $(crane version), cosign $(cosign version --json 2>/dev/null | jq -r .gitVersion)"
  else fatal "pinned tools installed" "see ${PROBE_WORK}/logs/tools.log"; fi
  if make -C "${ROOT}" build >"${PROBE_WORK}/logs/make-build.log" 2>&1; then pass "make build" "bin/myriarchd bin/myriarch-updater bin/myrctl bin/myriarch-sim"; else fatal "make build" "see ${PROBE_WORK}/logs/make-build.log"; fi
  local head dirty=""
  head="$(git -C "${ROOT}" rev-parse --short HEAD)"
  if [ -n "$(git -C "${ROOT}" status --porcelain)" ]; then dirty=1; fi
  state_set GIT_HEAD "${head}"
  if [ -z "${dirty}" ]; then pass "work tree clean" "HEAD ${head}"; else skip "work tree clean" "HEAD ${head} with uncommitted changes (ALLOW_DIRTY=1); the final proof needs a clean tree"; fi
}

# ---- stage 1 -----------------------------------------------------------------------------------------------------
stage_1() {
  stage_begin 1 "Cluster"
  if "${KIND_DIR}/up.sh" >"${PROBE_WORK}/logs/up.log" 2>&1; then pass "up.sh (cluster and registry)" "cluster ${KIND_CLUSTER_NAME}"; else cat "${PROBE_WORK}/logs/up.log" >&2; fatal "up.sh (cluster and registry)" "see ${PROBE_WORK}/logs/up.log"; fi
  assert_context
  local ready names
  ready="$(k get nodes -o json | jq '[.items[] | select(.status.conditions[] | select(.type=="Ready" and .status=="True"))] | length')"
  check_eq "three nodes Ready" 3 "${ready}"
  names="$(k get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}' | tr ' ' '\n' | sort -u | tr '\n' ' ')"
  pass "Kubernetes version" "${names}"
  state_set K8S_VERSION "$(printf '%s' "${names}" | awk '{print $1}')"
  # the node container runs the pinned image (tag and digest as given to kind)
  local want_tag img
  want_tag="${KIND_NODE_IMAGE%%@*}"
  img="$(docker inspect -f '{{.Config.Image}}' "${KIND_CLUSTER_NAME}-control-plane" 2>/dev/null || true)"
  case "${img}" in
    "${want_tag}"*) pass "node image is the pinned one" "${img}" ;;
    *) fail "node image is the pinned one" "expected ${want_tag}, got '${img}'" ;;
  esac
  local node missing=0
  for node in $(kind get nodes --name "${KIND_CLUSTER_NAME}"); do
    docker exec "${node}" test -s "/etc/containerd/certs.d/localhost:${KIND_REGISTRY_PORT}/hosts.toml" || missing=$((missing + 1))
  done
  check_eq "hosts.toml present in every node" 0 "${missing}"
  check_eq "registry answers /v2/" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${KIND_REGISTRY_PORT}/v2/")"
  check_eq "kindnet DaemonSet ready" "3/3" "$(k -n kube-system get ds kindnet -o jsonpath='{.status.numberReady}/{.status.desiredNumberScheduled}')"
}

# ---- stage 2 -----------------------------------------------------------------------------------------------------
stage_2() {
  stage_begin 2 "Images"
  if "${KIND_DIR}/images.sh" >"${PROBE_WORK}/logs/images.log" 2>&1; then pass "images built and pushed (crane, no container runtime)"; else tail -n 20 "${PROBE_WORK}/logs/images.log" >&2; fatal "images built and pushed" "see ${PROBE_WORK}/logs/images.log"; fi
  load_images
  local node_arch img_arch ref
  node_arch="$(k get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')"
  for ref in "${SERVER_REPO}@${SERVER_DIGEST}" "${COLLECTOR_REPO}@${COLLECTOR_DIGEST}"; do
    img_arch="$(crane config "${ref}" | jq -r .architecture)"
    check_eq "architecture of ${ref%%@*} equals the node architecture" "${node_arch}" "${img_arch}"
  done
  check_eq "server image runs as a non-root numeric user" 65532 "$(crane config "${SERVER_REPO}@${SERVER_DIGEST}" | jq -r '.config.User')"
  check_eq "collector image runs as a non-root numeric user" 10001:10001 "$(crane config "${COLLECTOR_REPO}@${COLLECTOR_DIGEST}" | jq -r '.config.User')"
  pass "image digests recorded" "server ${SERVER_DIGEST}, collector ${COLLECTOR_DIGEST}"
  state_set SERVER_DIGEST "${SERVER_DIGEST}"
  state_set COLLECTOR_DIGEST "${COLLECTOR_DIGEST}"
}

# ---- stage 3 -----------------------------------------------------------------------------------------------------
stage_3() {
  stage_begin 3 "Infrastructure (PostgreSQL, Git server, GitOps fixture)"
  load_images
  generate_secrets
  install_infra
  if wait_infra; then pass "postgres and git are Ready"; else fatal "postgres and git are Ready" "see kubectl -n ${NS_INFRA} get pods"; fi
  gitops_prepare
  if "${ROOT}/bin/myrctl" validate --strict --components "${ROOT}/examples/sim/components-otelcol-contrib-0.162.0.json" "${GITOPS_WORK}" >"${PROBE_WORK}/logs/validate-gitops.log" 2>&1; then
    pass "myrctl validate --strict of the fixture" "$(tail -n 1 "${PROBE_WORK}/logs/validate-gitops.log")"
  else fatal "myrctl validate --strict of the fixture" "$(tail -n 3 "${PROBE_WORK}/logs/validate-gitops.log" | tr '\n' ' ')"; fi
  if gitops_publish "probe: seed"; then pass "seed commit pushed to the Git server" "${GITOPS_SHA}"; else fatal "seed commit pushed to the Git server"; fi
  state_set SEED_SHA "${GITOPS_SHA}"
}

# ---- stage 4 -----------------------------------------------------------------------------------------------------
stage_4() {
  stage_begin 4 "Chart with only the required values"
  load_images
  if chart_install_minimal; then pass "helm install with image, publicUrl and the Secret database-dsn only"; else fatal "helm install with only the required values"; fi
  if check_deployed_config "${NS_APP}"; then pass "deployed myriarchd.yaml passes config validate"; else fail "deployed myriarchd.yaml passes config validate" "$(tail -n 3 "${PROBE_WORK}/render/deployed-${NS_APP}.validate.txt" | tr '\n' ' ')"; fi
  if kn "${NS_APP}" rollout status deploy/myriarch --timeout=300s >"${PROBE_WORK}/logs/rollout-a.log" 2>&1; then pass "kubectl rollout status succeeds"; else fatal "kubectl rollout status succeeds" "see ${PROBE_WORK}/logs/rollout-a.log"; fi
  check_eq "readyReplicas" 2 "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.status.readyReplicas}')"
  local nodes
  nodes="$(kn "${NS_APP}" get pods -l app.kubernetes.io/instance=myriarch -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u | grep -c .)"
  check_eq "the two pods run on different workers" 2 "${nodes}"
  check_eq "PodDisruptionBudget minAvailable" 1 "$(kn "${NS_APP}" get pdb myriarch -o jsonpath='{.spec.minAvailable}' 2>/dev/null)"
  check_eq "NetworkPolicy exists" myriarch "$(kn "${NS_APP}" get networkpolicy myriarch -o jsonpath='{.metadata.name}' 2>/dev/null)"
  local args
  args="$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.spec.template.spec.containers[0].args[0]}')"
  check_eq "container runs 'serve'" serve "${args}"
  check_eq "rendered config has no devLogin or dev section" 0 "$(yq '[(.dev // null), (.auth.devLogin // null)] | map(select(. != null)) | length' "${PROBE_WORK}/render/deployed-${NS_APP}.myriarchd.yaml")"
  api_use main
  if pf_ensure; then pass "port-forward to svc/myriarch"; else fatal "port-forward to svc/myriarch" "see ${PROBE_WORK}/logs/portforward-api-main.log"; fi
  check_eq "/healthz answers 200" 200 "$(http_code /healthz)"
  check_eq "/readyz answers 200" 200 "$(http_code /readyz)"
  check_eq "Secret myriarch-bootstrap exists" myriarch-bootstrap "$(kn "${NS_APP}" get secret myriarch-bootstrap -o jsonpath='{.metadata.name}' 2>/dev/null)"
  # the NOTES command as printed (its shape is verified; the token goes to a file, never to the log)
  local tokfile
  tokfile="$(secret_path bootstrap.token)"
  bootstrap_token_from_notes "${tokfile}" || : > "${tokfile}"
  if grep -Eq '^[A-Za-z0-9]{48}$' "${tokfile}"; then pass "the NOTES command prints a usable token (48 alphanumeric characters)"; else fail "the NOTES command prints a usable token" "the printed command has an unexpected shape or the Secret is unreadable"; fi
  local out
  out="$(h template myriarch "${CHART_DIR}" --set config.server.publicUrl="${PUBLIC_URL}" --set bootstrapAdmin.enabled=false 2>&1 || true)"
  check_contains "bootstrapAdmin.enabled=false without another login path is refused" "no login path is configured" "${out}"
}

# ---- stage 5 -----------------------------------------------------------------------------------------------------
stage_5() {
  stage_begin 5 "Bootstrap admin"
  api_use main
  pf_ensure || true
  local cfg
  cfg="$(curl -s -m 10 "${API_BASE}/api/v1/auth/config")"
  check_eq "/auth/config bootstrapTokenEnabled" true "$(printf '%s' "${cfg}" | jq -r .bootstrapTokenEnabled)"
  check_eq "/auth/config oidcEnabled" false "$(printf '%s' "${cfg}" | jq -r .oidcEnabled)"
  check_eq "/auth/config devLoginEnabled" false "$(printf '%s' "${cfg}" | jq -r .devLoginEnabled)"
  check_eq "POST /auth/dev-login does not exist (404)" 404 "$(curl -s -m 10 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "${API_BASE}/api/v1/auth/dev-login")"
  rm -f "${API_JAR}"
  ( umask 077; : > "${API_JAR}" )
  check_eq "bootstrap token sign-in through the API (cookie jar)" 200 "$(bootstrap_login "$(secret_path bootstrap.token)" "${API_JAR}")"
  session_refresh
  local me
  me="$(a /auth/me)"
  check_eq "/auth/me authMethod" bootstrap "$(printf '%s' "${me}" | jq -r .authMethod)"
  check_eq "/auth/me role" admin "$(printf '%s' "${me}" | jq -r .role)"
  check_eq "/auth/me name" admin "$(printf '%s' "${me}" | jq -r .name)"
  if [ -n "${API_CSRF}" ]; then pass "CSRF cookie in the jar (myriarch_csrf)"; else fail "CSRF cookie in the jar (myriarch_csrf)" "cookie missing: Secure cookies not sent over http://127.0.0.1?"; fi
  # system info after the sign-in (read permission)
  # replicas and leader are eventually consistent: a replica registers at its start, the leader flag follows the election
  # (the product writes it within about a second). Wait for the settled state, record how long it took, then assert on a fresh read.
  local info t0="${SECONDS}"
  wait_until 60 "[ \"\$(sysinfo | jq '[.replicas[] | select(.isLeader)] | length')\" = 1 ] && [ \"\$(sysinfo | jq '.replicas | length')\" = 2 ]" 2
  info="$(sysinfo)"
  check_eq "/system/info replicas" 2 "$(printf '%s' "${info}" | jq '.replicas | length')"
  check_eq "/system/info exactly one leader" 1 "$(printf '%s' "${info}" | jq '[.replicas[] | select(.isLeader)] | length')"
  pass "replicas and leader settled" "$((SECONDS - t0)) s after the first read"
  check_eq "/system/info database ok" true "$(printf '%s' "${info}" | jq -r .database.ok)"
  check_eq "/system/info features.bootstrapAdmin" true "$(printf '%s' "${info}" | jq -r .features.bootstrapAdmin)"
  check_eq "/system/info features.gitops (not configured yet)" false "$(printf '%s' "${info}" | jq -r .features.gitops)"
  check_eq "/system/info features.devMode" false "$(printf '%s' "${info}" | jq -r .features.devMode)"
  # the one-time token is spent: a second sign-in with a fresh jar is refused
  local jar2="${PROBE_WORK}/cookies-reuse"
  rm -f "${jar2}"
  check_eq "reuse of the one-time token is refused (401)" 401 "$(bootstrap_login "$(secret_path bootstrap.token)" "${jar2}")"
  rm -f "${jar2}"
  check_eq "audit auth.bootstrap.login success" 1 "$(audit_count auth.bootstrap.login success)"
  check_eq "audit auth.bootstrap.login failure" 1 "$(audit_count auth.bootstrap.login failure)"
  check_eq "audit auth.bootstrap.activate (two replicas started concurrently)" 1 "$(audit_count auth.bootstrap.activate)"
  local ctype
  ctype="$(curl -s -m 10 -o /dev/null -w '%{http_code} %{content_type}' "${API_BASE}/")"
  case "${ctype}" in "200 text/html"*) pass "GET / serves the web UI (200 text/html)" ;; *) fail "GET / serves the web UI" "${ctype}" ;; esac
}

# ---- stage 6 -----------------------------------------------------------------------------------------------------
stage_6() {
  stage_begin 6 "Full configuration (helm upgrade)"
  load_images
  api_use main
  write_values_tokens 30
  state_set KIND_SIM_EXPIRES_AT "${KIND_SIM_EXPIRES_AT}"
  ( umask 077; kn "${NS_APP}" get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d | sha256_stdin > "$(secret_path bootstrap.sha)" )
  if chart_upgrade_full; then pass "helm upgrade with the probe values and the declared enrollment tokens"; else fatal "helm upgrade with the probe values"; fi
  if check_deployed_config "${NS_APP}"; then pass "deployed myriarchd.yaml passes config validate"; else fail "deployed myriarchd.yaml passes config validate" "$(tail -n 3 "${PROBE_WORK}/render/deployed-${NS_APP}.validate.txt" | tr '\n' ' ')"; fi
  if app_rollout_wait "${NS_APP}"; then pass "rollout of the upgrade completes"; else fatal "rollout of the upgrade completes"; fi
  local ports
  ports="$(kn "${NS_APP}" get networkpolicy myriarch -o json | jq -r '[.spec.egress[]?.ports[]?.port] | unique | map(tostring) | join(",")')"
  check_contains "NetworkPolicy egress ports include 2222 (added from the ssh:// URL)" 2222 "${ports}"
  check_eq "the phase A session survives the rolling restart (sessions live in the database)" admin "$(a /auth/me | jq -r .name)"
  wait_until 180 "[ \"\$(a /gitops/status | jq -r .state)\" = ok ]" 2
  local gs
  gs="$(a /gitops/status)"
  check_eq "/gitops/status state" ok "$(printf '%s' "${gs}" | jq -r .state)"
  check_eq "/gitops/status appliedCommit equals the seed commit" "$(state_get SEED_SHA)" "$(printf '%s' "${gs}" | jq -r .appliedCommit.sha)"
  check_eq "/gitops/status issues" 0 "$(printf '%s' "${gs}" | jq '.issues | length')"
  check_eq "/gitops/status objectCounts" "3/2/2" "$(printf '%s' "${gs}" | jq -r '"\(.objectCounts.Fleet)/\(.objectCounts.Blueprint)/\(.objectCounts.RolloutPolicy)"')"
  check_eq "/system/info features.gitops" true "$(sysinfo | jq -r .features.gitops)"
  check_eq "Secret myriarch-enrollment-kind-sim exists" myriarch-enrollment-kind-sim "$(kn "${NS_APP}" get secret myriarch-enrollment-kind-sim -o jsonpath='{.metadata.name}' 2>/dev/null)"
  check_eq "Secret myriarch-enrollment-kind-gw exists" myriarch-enrollment-kind-gw "$(kn "${NS_APP}" get secret myriarch-enrollment-kind-gw -o jsonpath='{.metadata.name}' 2>/dev/null)"
  local et name
  et="$(a /enrollment-tokens)"
  for name in kind-sim kind-gw; do
    check_eq "enrollment token ${name}: exactly one row" 1 "$(printf '%s' "${et}" | jq --arg n "${name}" '[.items[] | select(.name==$n)] | length')"
    check_eq "enrollment token ${name}: createdBy" config "$(printf '%s' "${et}" | jq -r --arg n "${name}" '.items[] | select(.name==$n) | .createdBy')"
    check_eq "enrollment token ${name}: labels.source" "${name}" "$(printf '%s' "${et}" | jq -r --arg n "${name}" '.items[] | select(.name==$n) | .labels.source')"
  done
  check_eq "enrollment token kind-sim: expiresAt equals the declared value" "${KIND_SIM_EXPIRES_AT}" "$(printf '%s' "${et}" | jq -r '.items[] | select(.name=="kind-sim") | .expiresAt')"
  check_eq "enrollment token kind-gw: no expiry" null "$(printf '%s' "${et}" | jq -r '.items[] | select(.name=="kind-gw") | .expiresAt')"
  for name in kind-sim kind-gw; do
    check_eq "audit enrollment_token.create for ${name}" 1 "$(a '/audit?action=enrollment_token.create&limit=200' | jq --arg n "${name}" '[.items[] | select(.details.name==$n)] | length')"
  done
  check_eq "the old bootstrap token is still refused (401)" 401 "$(bootstrap_login "$(secret_path bootstrap.token)" "${PROBE_WORK}/cookies-old")"
  rm -f "${PROBE_WORK}/cookies-old"
  local now
  now="$(kn "${NS_APP}" get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d | sha256_stdin)"
  if [ "${now}" = "$(cat "$(secret_path bootstrap.sha)")" ]; then pass "the bootstrap Secret value is unchanged by the upgrade"; else fail "the bootstrap Secret value is unchanged by the upgrade" "value differs"; fi
}

# ---- stage 7 -----------------------------------------------------------------------------------------------------
stage_7() {
  stage_begin 7 "Agents (simulator and the real collector)"
  load_images
  api_use main
  pf_ensure || true
  deploy_agents
  if kn "${NS_APP}" rollout status deploy/myriarch-sim --timeout=240s >"${PROBE_WORK}/logs/rollout-sim.log" 2>&1; then pass "simulator deployed (Pod Security restricted)"; else fatal "simulator deployed" "see ${PROBE_WORK}/logs/rollout-sim.log"; fi
  # the collector pulls ~400 MB and seeds the volume: allow time
  if kn "${NS_APP}" rollout status statefulset/kind-gw --timeout=420s >"${PROBE_WORK}/logs/rollout-gw.log" 2>&1; then pass "collector StatefulSet kind-gw Ready (container mode)"; else fatal "collector StatefulSet kind-gw Ready" "see ${PROBE_WORK}/logs/rollout-gw.log"; fi
  wait_until 240 "[ \"\$(a /stats/overview | jq -r '.agents.collectors')\" = 21 ]" 2
  local st
  st="$(a /stats/overview)"
  check_eq "stats: collectors" 21 "$(printf '%s' "${st}" | jq -r .agents.collectors)"
  wait_until 120 "[ \"\$(a /stats/overview | jq -r '.agents.updaters')\" = 1 ]" 2
  check_eq "stats: updaters" 1 "$(a /stats/overview | jq -r .agents.updaters)"
  wait_until 120 "[ \"\$(a /stats/overview | jq -r '.agents.online == .agents.total')\" = true ]" 2
  check_eq "stats: online equals total" true "$(a /stats/overview | jq -r '.agents.online == .agents.total')"
  check_eq "20 collectors carry the label source=kind-sim" 20 "$(a '/agents?attr=labels.source%3Dkind-sim&kind=collector&limit=1' | jq -r .total)"
  local fac
  fac="$(a '/agents/facets?kind=collector')"
  check_eq "fleet facets sim-edge / sim-dc / kind-gw" "15/5/1" "$(printf '%s' "${fac}" | jq -r '[(.fleets[]|select(.value=="sim-edge")|.count), (.fleets[]|select(.value=="sim-dc")|.count), (.fleets[]|select(.value=="kind-gw")|.count)] | map(tostring) | join("/")')"
  check_eq "no unassigned collector" 0 "$(a /stats/overview | jq -r .unassigned)"
  # the real collector
  wait_until 180 "[ -n \"\$(agent_id_by_site kind-gw)\" ]" 2
  local gw
  gw="$(agent_id_by_site kind-gw)"
  state_set GW_AGENT_ID "${gw}"
  [ -n "${gw}" ] || fatal "real collector registered" "no collector with site.id=kind-gw"
  wait_until 120 "[ \"\$(a /agents/${gw} | jq -r .configSync)\" = in_sync ]" 2
  wait_until 120 "[ -n \"\$(updater_id_of ${gw})\" ]" 2
  local ag upd
  ag="$(a "/agents/${gw}")"
  check_eq "real collector: fleet / version / configSync" "kind-gw/0.161.0/in_sync" "$(printf '%s' "${ag}" | jq -r '"\(.agent.assignment.fleetName)/\(.agent.version)/\(.configSync)"')"
  check_eq "real collector: remoteConfig.status" applied "$(printf '%s' "${ag}" | jq -r .agent.remoteConfig.status)"
  check_eq "real collector: labels.source" kind-gw "$(printf '%s' "${ag}" | jq -r .agent.labels.source)"
  upd="$(updater_id_of "${gw}")"
  state_set GW_UPDATER_ID "${upd}"
  if [ -n "${upd}" ]; then pass "myriarch-updater linked to the collector"; else fail "myriarch-updater linked to the collector"; fi
  local ua
  ua="$(a "/agents/${upd}" | jq -c '.agent.nonIdentifyingAttributes')"
  check_eq "updater install_method" container "$(printf '%s' "${ua}" | jq -r '."dev.myriarch.updater.install_method"')"
  check_eq "updater server_endpoint" "ws://myriarch:8080" "$(printf '%s' "${ua}" | jq -r '."dev.myriarch.updater.server_endpoint"')"
  check_eq "updater image_collector_version" 0.161.0 "$(printf '%s' "${ua}" | jq -r '."dev.myriarch.updater.image_collector_version"')"
  local seed
  seed="$(kn "${NS_APP}" logs kind-gw-0 2>/dev/null | grep -m1 '"msg":"seed decision"' | jq -r '"\(.decision)/\(.imageVersion)/\(.active)"')"
  check_eq "updater log: seed decision" activate-image/0.161.0/0.161.0 "${seed}"
  check_eq "current -> versions/0.161.0 on the volume" versions/0.161.0 "$(kn "${NS_APP}" exec kind-gw-0 -- readlink /var/lib/myriarch/collector/current 2>/dev/null)"
  if kn "${NS_APP}" exec kind-gw-0 -- sh -c 'command -v pgrep >/dev/null && command -v readlink >/dev/null' >/dev/null 2>&1; then
    pass "pgrep and readlink exist in the collector container (needed by the fault injection)"
  else fail "pgrep and readlink exist in the collector container" "the image has no pgrep"; fi
  check_eq "collector pod is Ready" True "$(kn "${NS_APP}" get pod kind-gw-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
}

# ---- stage 8 -----------------------------------------------------------------------------------------------------
stage_8() {
  stage_begin 8 "Configuration rollout to in_sync"
  api_use main
  pf_ensure || true
  local f
  for f in kind-gw sim; do
    sed -e 's/timeout: 5s/timeout: 7s/' "${GITOPS_WORK}/blueprints/${f}.yaml" > "${GITOPS_WORK}/blueprints/${f}.yaml.new"
    mv "${GITOPS_WORK}/blueprints/${f}.yaml.new" "${GITOPS_WORK}/blueprints/${f}.yaml"
  done
  if gitops_publish "probe: batch timeout 7s"; then pass "blueprint change pushed" "${GITOPS_SHA}"; else fatal "blueprint change pushed"; fi
  state_set CONFIG_SHA "${GITOPS_SHA}"
  ap /gitops/actions/sync >/dev/null
  if wait_until 120 "[ \"\$(a /gitops/status | jq -r .appliedCommit.sha)\" = ${GITOPS_SHA} ]" 2; then pass "GitOps applied the new commit"; else fail "GitOps applied the new commit" "$(a /gitops/status | jq -r '.appliedCommit.sha // "none"')"; fi
  wait_until 60 "[ \"\$(a '/rollouts?kind=config&limit=50' | jq '.items | length')\" -ge 3 ]" 2
  check_eq "one config rollout per fleet" "kind-gw,sim-dc,sim-edge" "$(a '/rollouts?kind=config&limit=50' | jq -r '[.items[].fleetName] | unique | join(",")')"
  local rc=0
  wait_rollouts config 300 || rc=$?
  if [ "${rc}" = 0 ]; then pass "config rollouts completed" "$(a '/rollouts?kind=config&limit=50' | jq -r '[.items[] | "\(.fleetName):\(.status)"] | join(",")')"; else fail "config rollouts completed" "rc=${rc} $(a '/rollouts?kind=config&limit=50' | jq -r '[.items[] | "\(.fleetName):\(.status)"] | join(",")')"; fi
  wait_until 120 "[ \"\$(a '/agents/facets?kind=collector' | jq -r '[.configSync[]? | select(.value==\"in_sync\")][0].count // 0')\" = 21 ]" 2
  check_eq "all 21 collectors report configSync in_sync" 21 "$(a '/agents/facets?kind=collector' | jq -r '[.configSync[]? | select(.value=="in_sync")][0].count // 0')"
  local gw ec
  gw="$(state_get GW_AGENT_ID)"
  ec="$(a "/agents/${gw}/effective-config")"
  check_contains "effective config of the real collector contains the new batch timeout" "timeout: 7s" "${ec}"
  check_eq "real collector: remoteConfig.status applied" applied "$(a "/agents/${gw}" | jq -r .agent.remoteConfig.status)"
}
