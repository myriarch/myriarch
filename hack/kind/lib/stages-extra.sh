#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Probe stages 13 to 15 (sourced by hack/kind/probe.sh): NetworkPolicy enforcement, the Dex quickstart, and the log and
# secret hygiene of everything the probe started.

# shellcheck source=hack/kind/lib/dex.sh
. "${KIND_DIR}/lib/dex.sh"

# curl_in NAMESPACE POD URL -> exit status of curl inside the pod (28 = timeout, i.e. the packets were dropped)
curl_in() {
  kn "$1" exec "$2" -c curl -- curl -s -m 5 -o /dev/null -w '%{http_code}' "$3" >/dev/null 2>&1
  return $?
}
curl_code_in() { kn "$1" exec "$2" -c curl -- curl -s -m 5 -o /dev/null -w '%{http_code}' "$3" 2>/dev/null; }

# ---- stage 13 ----------------------------------------------------------------------------------------------------
stage_13() {
  stage_begin 13 "NetworkPolicy"
  if skipped_by_key netpol; then skip "NetworkPolicy checks" "PROBE_SKIP=netpol"; return 0; fi
  api_use main
  pf_ensure || true
  session_refresh
  ensure_namespace "${NS_OUTSIDE}"
  BUSYBOX_IMAGE="${BUSYBOX_IMAGE}" CURL_IMAGE="${CURL_IMAGE}" render_manifest "${KIND_DIR}/manifests/netpol-test.yaml" BUSYBOX_IMAGE CURL_IMAGE | k apply -f - >/dev/null
  kn "${NS_OUTSIDE}" wait --for=condition=Ready pod/np-target pod/np-client --timeout=180s >/dev/null 2>&1
  kn "${NS_APP}" wait --for=condition=Ready pod/np-client --timeout=180s >/dev/null 2>&1

  # self-test: does the CNI enforce NetworkPolicy at all?
  wait_until 60 "[ \"\$(kubectl --context ${KIND_CONTEXT} -n ${NS_OUTSIDE} exec np-client -c curl -- curl -s -m 3 -o /dev/null -w '%{http_code}' http://np-target:8080/ 2>/dev/null)\" = 200 ]" 2
  local reach
  reach="$(curl_code_in "${NS_OUTSIDE}" np-client http://np-target:8080/)"
  check_eq "self-test: the target is reachable without a policy" 200 "${reach}"
  if [ "${reach}" != 200 ]; then skip "NetworkPolicy checks of the chart" "the self-test target is not reachable even without a policy"; cleanup_np; return 0; fi
  k apply -f "${KIND_DIR}/manifests/np-deny.yaml" >/dev/null
  local enforced=0
  if wait_until 40 "kubectl --context ${KIND_CONTEXT} -n ${NS_OUTSIDE} exec np-client -c curl -- curl -s -m 3 -o /dev/null http://np-target:8080/ >/dev/null 2>&1; [ \$? = 28 ]" 2; then enforced=1; fi
  if [ "${enforced}" = 1 ]; then
    pass "self-test: a deny-ingress policy blocks the client (the CNI enforces NetworkPolicy)"
  else
    skip "NetworkPolicy checks of the chart" "CNI does not enforce NetworkPolicy"
    cleanup_np
    return 0
  fi

  # the policy of the chart
  local rc
  kn "${NS_OUTSIDE}" exec np-client -c curl -- curl -s -m 5 -o /dev/null "http://myriarch.${NS_APP}.svc:8080/healthz" >/dev/null 2>&1; rc=$?
  check_eq "from outside the namespace: myriarch:8080 is blocked (curl timeout, exit 28)" 28 "${rc}"
  check_eq "from a pod in the release namespace: myriarch:8080/healthz answers 200" 200 "$(curl_code_in "${NS_APP}" np-client "http://myriarch.${NS_APP}.svc:8080/healthz")"
  local podip
  podip="$(kn "${NS_APP}" get pods -l app.kubernetes.io/instance=myriarch -o jsonpath='{.items[0].status.podIP}')"
  kn "${NS_APP}" exec np-client -c curl -- curl -s -m 5 -o /dev/null "http://${podip}:9090/metrics" >/dev/null 2>&1; rc=$?
  check_eq "from a pod in the release namespace: :9090/metrics is blocked (metrics.from is empty)" 28 "${rc}"
  # the metrics port is open and populated (port-forward is not affected by the policy), summed over both replicas
  local total=0 pods pod i=0 port n
  pods="$(kn "${NS_APP}" get pods -l app.kubernetes.io/instance=myriarch -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
  for pod in ${pods}; do
    i=$((i + 1))
    port=$((PROBE_PORT_API + 10 + i))
    if pf_start "metrics-${i}" "${NS_APP}" "pod/${pod}" "${port}" 9090; then
      n="$(curl -s -m 10 "http://127.0.0.1:${port}/metrics" | awk '/^myriarch_opamp_connections(\{[^}]*\})? /{s+=$2} END{printf "%d", s}')"
      total=$((total + ${n:-0}))
    fi
    pf_stop "metrics-${i}"
  done
  check_ge "metrics through a port-forward: myriarch_opamp_connections over both replicas" 22 "${total}"
  # egress under the policy: the dependencies work (database, Git over SSH, GitHub downloads, DNS)
  check_eq "egress: database reachable" true "$(sysinfo | jq -r .database.ok)"
  check_eq "egress: Git over SSH (2222) reachable" ok "$(a /gitops/status | jq -r .state)"
  check_ge "egress: GitHub release downloads (ready artifacts)" 1 "$(a /artifacts | jq '[.items[] | select(.status=="ready")] | length')"
  check_ge "egress: release tracker has releases (GitHub API over 443)" 1 "$(a '/releases?limit=5' | jq '.items | length')"
  cleanup_np
}

cleanup_np() {
  kn "${NS_OUTSIDE}" delete pod np-client np-target --now --ignore-not-found >/dev/null 2>&1 || true
  kn "${NS_APP}" delete pod np-client --now --ignore-not-found >/dev/null 2>&1 || true
}

# ---- stage 14 ----------------------------------------------------------------------------------------------------
stage_14() {
  stage_begin 14 "Dex quickstart (OIDC)"
  if skipped_by_key dex; then skip "Dex quickstart" "PROBE_SKIP=dex"; return 0; fi
  load_images
  if dex_make_certs; then pass "test CA and server certificate for ${DEX_HOST} (openssl verify)"; else fatal "test CA and server certificate" "openssl failed"; fi
  if dex_make_values; then pass "Dex values from examples/dex/dex.yaml (client secret and bcrypt hash filled in)"; else fatal "Dex values" "yq or myriarchd auth hash-password failed"; fi
  if dex_pull_chart; then pass "Dex chart ${DEX_CHART_VERSION} pulled, SHA-256 matches the pin"; else fatal "Dex chart pulled and verified" "helm pull failed or the archive differs from the pin"; fi
  if dex_install; then pass "Dex installed with the image pinned by digest"; else fatal "Dex installed" "see ${PROBE_WORK}/logs/dex-install.log"; fi
  if kn "${NS_DEXIDP}" rollout status deployment/dex --timeout=240s >"${PROBE_WORK}/logs/dex-rollout.log" 2>&1; then pass "Dex is Ready"; else fatal "Dex is Ready" "see ${PROBE_WORK}/logs/dex-rollout.log"; fi
  if coredns_add_rewrite; then pass "CoreDNS rewrite ${DEX_HOST} -> dex.${NS_DEXIDP}.svc.cluster.local"; else fatal "CoreDNS rewrite" "could not edit the Corefile"; fi
  ensure_namespace "${NS_OUTSIDE}"
  local dns want
  want="$(kn "${NS_DEXIDP}" get svc dex -o jsonpath='{.spec.clusterIP}')"
  # busybox prints the resolver as "10.96.0.10:53" (older builds "#53"); only the Address lines after "Name:" are answers. A lost
  # attach stream (kubectl falls back to logs and may cut the output) leaves the result empty, so retry a few times.
  for _ in 1 2 3; do
    dns="$(kn "${NS_OUTSIDE}" run dns-check --rm -i --restart=Never --image="${BUSYBOX_IMAGE}" --command -- nslookup "${DEX_HOST}" 2>/dev/null | awk '/^Name:/ {n=1} n && /^Address/ {print $NF}' | tail -n 1)"
    [ -n "${dns}" ] && break
    sleep 2
  done
  check_eq "nslookup ${DEX_HOST} from a pod gives the ClusterIP of the Dex Service" "${want}" "${dns}"
  if dex_myriarch_install; then pass "myriarch installed with examples/quickstart-dex.yaml and the overlay"; else fatal "myriarch installed with the Dex quickstart" "see ${PROBE_WORK}/logs/dex-myriarch-install.log"; fi
  if kn "${NS_DEX}" rollout status deploy/myriarch --timeout=300s >"${PROBE_WORK}/logs/dex-myriarch-rollout.log" 2>&1; then pass "myriarch (Dex release) rollout completes"; else fatal "myriarch (Dex release) rollout completes" "see ${PROBE_WORK}/logs/dex-myriarch-rollout.log"; fi
  check_deployed_config "${NS_DEX}" && pass "deployed myriarchd.yaml (Dex release) passes config validate" || fail "deployed myriarchd.yaml (Dex release) passes config validate"
  api_use dex
  rm -f "${API_JAR}"
  pf_restart || fatal "port-forward to the Dex release" "see ${PROBE_WORK}/logs/portforward-api-dex.log"
  pf_start dex "${NS_DEXIDP}" svc/dex "${PROBE_PORT_DEX}" 5554 || fatal "port-forward to Dex" "see ${PROBE_WORK}/logs/portforward-dex.log"
  local cfg
  cfg="$(curl -s -m 10 "${API_BASE}/api/v1/auth/config")"
  check_eq "/auth/config oidcEnabled" true "$(printf '%s' "${cfg}" | jq -r .oidcEnabled)"
  check_eq "/auth/config bootstrapTokenEnabled" false "$(printf '%s' "${cfg}" | jq -r .bootstrapTokenEnabled)"
  ( umask 077; : > "${API_JAR}" )
  local code
  code="$(dex_login "${API_JAR}")"
  check_eq "OIDC login through Dex (callback answers 302 to the UI)" 302 "${code}"
  session_refresh
  local me
  me="$(a /auth/me)"
  check_eq "/auth/me authMethod" oidc "$(printf '%s' "${me}" | jq -r .authMethod)"
  check_eq "/auth/me role (roleMapping.users email:admin@example.test)" admin "$(printf '%s' "${me}" | jq -r .role)"
  check_eq "/auth/me email" admin@example.test "$(printf '%s' "${me}" | jq -r .email)"
  check_ge "audit auth.login (OIDC)" 1 "$(audit_count auth.login success)"
  check_eq "audit auth.bootstrap.login is empty in the Dex release" 0 "$(audit_count auth.bootstrap.login)"
  pf_stop dex
  pf_stop api-dex
  api_use main
}

# ---- stage 15 ----------------------------------------------------------------------------------------------------
stage_15() {
  stage_begin 15 "Logs and secret hygiene"
  local logs="${PROBE_WORK}/logs/pods" ns pod rc
  rm -rf "${logs}"
  mkdir -p "${logs}"
  for ns in "${NS_APP}" "${NS_INFRA}" "${NS_DEX}" "${NS_DEXIDP}"; do
    k get namespace "${ns}" >/dev/null 2>&1 || continue
    for pod in $(kn "${ns}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'); do
      kn "${ns}" logs "${pod}" --all-containers --prefix > "${logs}/${ns}-${pod}.log" 2>&1
      kn "${ns}" logs "${pod}" --all-containers --prefix --previous > "${logs}/${ns}-${pod}.previous.log" 2>/dev/null || rm -f "${logs}/${ns}-${pod}.previous.log"
    done
  done
  local files
  files="$(find "${logs}" -type f | wc -l | tr -d ' ')"
  check_ge "pod logs collected" 4 "${files}"
  # 15.1 crashes and races in the logs of myriarchd, the simulator and the collector
  local bad
  bad="$(grep -lE 'panic:|fatal error|DATA RACE' "${logs}"/${NS_APP}-*.log "${logs}"/${NS_DEX}-*.log 2>/dev/null | wc -l | tr -d ' ')"
  local srv
  srv="$(grep -l '"msg":"starting myriarchd"' "${logs}"/*.log 2>/dev/null | tr '\n' ' ')"
  check_ge "logs of the myriarchd pods found" 3 "$(printf '%s' "${srv}" | wc -w | tr -d ' ')"
  check_eq "no panic, fatal error or data race in the logs of myriarchd, the simulator and the collector" 0 "${bad}"
  # 15.2 no generated secret in a log, in the Helm values or in a ConfigMap
  local dump="${PROBE_WORK}/logs/hygiene-dump"
  rm -rf "${dump}"
  mkdir -p "${dump}"
  cp "${logs}"/*.log "${dump}/" 2>/dev/null || true
  for ns in "${NS_APP}" "${NS_DEX}" "${NS_DEXIDP}"; do
    # The upstream Dex chart takes the demo client secret and the password hash inline in its values (examples/dex/dex.yaml
    # documents it as a demo): its release values are not part of the hygiene scan, the myriarch releases are.
    if [ "${ns}" != "${NS_DEXIDP}" ]; then
      for rc in $(h list -n "${ns}" -q 2>/dev/null); do
        h get values "${rc}" -n "${ns}" --all > "${dump}/helm-${ns}-${rc}.txt" 2>&1
      done
    fi
    kn "${ns}" get configmap -o yaml > "${dump}/configmaps-${ns}.yaml" 2>&1
    kn "${ns}" get pods -o yaml > "${dump}/pods-${ns}.yaml" 2>&1
  done
  kn "${NS_INFRA}" get configmap,pods -o yaml > "${dump}/infra.yaml" 2>&1
  # the probe's own outputs: a regression in the scripts must not leak a secret into them either (never secrets/)
  local own="${dump}/probe-own"
  mkdir -p "${own}"
  cp "${PROBE_WORK}/probe.log" "${PROBE_WORK}/results.txt" "${own}/" 2>/dev/null || true
  cp "${PROBE_WORK}/logs"/*.log "${own}/" 2>/dev/null || true
  if [ -d "${PROBE_WORK}/diag" ]; then cp -R "${PROBE_WORK}/diag" "${own}/diag" 2>/dev/null || true; fi
  check_ge "the probe's own outputs are part of the scan" 2 "$(find "${own}" -type f | wc -l | tr -d ' ')"
  secret_hits "bootstrap token (current)" "$(secret_path bootstrap.token)" "${dump}"
  secret_hits "bootstrap token (rotated away)" "$(secret_path bootstrap.token.old)" "${dump}"
  secret_hits "PostgreSQL password" "$(secret_path postgres-password)" "${dump}"
  secret_hits "database DSN" "$(secret_path database-dsn)" "${dump}"
  secret_hits "database DSN (Dex release)" "$(secret_path database-dsn-dex)" "${dump}"
  secret_hits "Git SSH private key" "$(secret_path git-ssh-key)" "${dump}" keybody
  secret_hits "Git host private key" "$(secret_path git-host-key)" "${dump}" keybody
  if [ -s "$(secret_path dex-client-secret)" ]; then secret_hits "Dex client secret" "$(secret_path dex-client-secret)" "${dump}"; fi
  if [ -s "$(secret_path dex-password)" ]; then secret_hits "Dex demo password" "$(secret_path dex-password)" "${dump}"; fi
  local sec pat="${PROBE_WORK}/secrets/.pattern"
  for sec in myriarch-enrollment-kind-sim myriarch-enrollment-kind-gw; do
    ( umask 077; kn "${NS_APP}" get secret "${sec}" -o jsonpath='{.data.token}' | base64 -d > "${pat}" )
    secret_hits "enrollment token ${sec#myriarch-enrollment-}" "${pat}" "${dump}"
    rm -f "${pat}"
  done
  rm -rf "${dump}"
  # 15.3 restarts of our components; Dex (upstream chart, demo) is reported separately: its first start races with the
  # registration of its own CRDs ("Too Many Requests" from the API server) and it restarts once
  local restarts dexr
  restarts="$(k get pods -A -o json | jq -r --arg a "${NS_APP}" --arg b "${NS_INFRA}" --arg c "${NS_DEX}" \
    '[.items[] | select(.metadata.namespace==$a or .metadata.namespace==$b or .metadata.namespace==$c) | .status.containerStatuses[]? | .restartCount] | add // 0')"
  check_eq "restartCount of every probe container (myriarch, simulator, collector, PostgreSQL, Git server)" 0 "${restarts}"
  if [ "${restarts}" != 0 ]; then
    log "containers with restarts: $(k get pods -A -o json | jq -r '[.items[] | .metadata.namespace as $n | .metadata.name as $p | .status.containerStatuses[]? | select(.restartCount>0) | "\($n)/\($p)/\(.name)=\(.restartCount)(\(.lastState.terminated.reason // "?"))"] | join(" ")')"
  fi
  dexr="$(k get pods -n "${NS_DEXIDP}" -o json 2>/dev/null | jq -r '[.items[].status.containerStatuses[]?.restartCount] | add // 0')"
  pass "restarts of the Dex demo container (informational)" "${dexr}"
  # 15.4 informational: ERROR lines of myriarchd
  local errs top
  # shellcheck disable=SC2086  # srv is a list of file names without spaces
  errs="$(cat ${srv} 2>/dev/null | grep -c '"level":"ERROR"' || true)"
  # shellcheck disable=SC2086
  top="$(cat ${srv} 2>/dev/null | grep '"level":"ERROR"' | sed -E 's/.*"msg":"([^"]*)".*/\1/' | cut -c1-80 | sort | uniq -c | sort -rn | head -n 5 | awk '{c=$1; $1=""; printf "%sx%s; ", c, $0}')"
  pass "myriarchd ERROR lines (informational)" "${errs} lines: ${top:-none}"
  state_set MYRIARCHD_ERROR_LINES "${errs}"
}

# secret_hits NAME SECRETFILE DIR [keybody]: number of files in DIR that contain the secret value (only the count is printed)
secret_hits() {
  local name="$1" file="$2" dir="$3" mode="${4:-}" pat="${PROBE_WORK}/secrets/.hits" n
  [ -s "${file}" ] || { skip "no occurrence of the ${name}" "secret file missing"; return 0; }
  ( umask 077
    if [ "${mode}" = keybody ]; then grep -v -e '^-----' -e '^$' "${file}" > "${pat}"; else tr -d '\n' < "${file}" > "${pat}"; printf '\n' >> "${pat}"; fi )
  n="$(grep -rlF -f "${pat}" "${dir}" 2>/dev/null | wc -l | tr -d ' ')"
  rm -f "${pat}"
  check_eq "no occurrence of the ${name} in logs, Helm values, ConfigMaps, pod specs or the probe outputs" 0 "${n}"
}
