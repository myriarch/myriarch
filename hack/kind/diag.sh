#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Diagnostics of the kind probe: cluster state, descriptions, logs, Helm status and (when a session works) API snapshots.
# Called automatically by hack/kind/probe.sh when a check fails; can be run by hand.
#
#   hack/kind/diag.sh            writes $PROBE_WORK/diag/<timestamp>/ and prints the directory
#   DIAG_KIND_LOGS=1 ...         also runs `kind export logs` (large)
#   DIAG_EXTRA_NS="a b" ...      also these namespaces (ci/kind-chart-test.sh)
#
# Secrets are never dumped (no `get secret -o`); the Helm values of the myriarch releases carry no secrets (they are passed
# through Secrets), the values of the Dex demo release do and are left out.
set -uo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
# shellcheck source=hack/kind/lib/api.sh
. "${KIND_DIR}/lib/api.sh"

out="${PROBE_WORK}/diag/$(date +%Y%m%dT%H%M%S)"
mkdir -p "${out}/logs"
chmod 700 "${out}"
log "diagnostics -> ${out}"

have docker && docker ps -a > "${out}/docker-ps.txt" 2>&1
if [ ! -f "${KUBECONFIG}" ] || ! kubectl --context "${KIND_CONTEXT}" get nodes >/dev/null 2>&1; then
  warn "the cluster is not reachable: only Docker information collected"
  echo "${out}"
  exit 0
fi

k get nodes,pods,svc,pvc,networkpolicies,deploy,sts,ds,pdb,events -A -o wide > "${out}/cluster.txt" 2>&1
# DIAG_EXTRA_NS: more namespaces (ci/kind-chart-test.sh), space separated
# shellcheck disable=SC2086
for ns in "${NS_APP}" "${NS_INFRA}" "${NS_DEX}" "${NS_DEXIDP}" "${NS_OUTSIDE}" ${DIAG_EXTRA_NS:-} kube-system; do
  k get ns "${ns}" >/dev/null 2>&1 || continue
  kn "${ns}" describe pods > "${out}/describe-${ns}.txt" 2>&1
  for pod in $(kn "${ns}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null); do
    kn "${ns}" logs "${pod}" --all-containers --prefix --tail=2000 > "${out}/logs/${ns}-${pod}.log" 2>&1
    # one --previous log per container that restarted (with --all-containers kubectl stops at the first container without one)
    for ctr in $(kn "${ns}" get pod "${pod}" -o json 2>/dev/null | jq -r '(.status.initContainerStatuses // []) + (.status.containerStatuses // []) | .[] | select(.restartCount > 0) | .name'); do
      kn "${ns}" logs "${pod}" -c "${ctr}" --previous --tail=500 > "${out}/logs/${ns}-${pod}-${ctr}.previous.log" 2>&1
    done
  done
  # per container: restarts, the last termination (reason, exit code), the effective resources; the events of the namespace by time
  kn "${ns}" get pods -o json 2>/dev/null | jq -r '.items[] | .metadata.name as $p
    | ((.status.initContainerStatuses // []) + (.status.containerStatuses // []))[] | .name as $c
    | [$p, $c, "restarts=\(.restartCount)", "ready=\(.ready)",
       "last=\(.lastState.terminated // {} | "\(.reason // "-") exit=\(.exitCode // "-") at=\(.finishedAt // "-")")",
       "now=\(.state | keys[0])"]
    | join("  ")' > "${out}/containers-${ns}.txt" 2>&1
  kn "${ns}" get pods -o json 2>/dev/null | jq -r '.items[] | .metadata.name as $p | (.spec.initContainers // []) + (.spec.containers // []) | .[] | "\($p)/\(.name) resources=\(.resources | tojson)"' > "${out}/resources-${ns}.txt" 2>&1
  kn "${ns}" get events --sort-by=.lastTimestamp > "${out}/events-${ns}.txt" 2>&1
done
# shellcheck disable=SC2086
for ns in "${NS_APP}" "${NS_DEX}" "${NS_DEXIDP}" ${DIAG_EXTRA_NS:-}; do
  h list -n "${ns}" > "${out}/helm-list-${ns}.txt" 2>&1
  for rel in $(h list -n "${ns}" -q 2>/dev/null); do
    # the values of the upstream Dex release carry the demo client secret inline: status only
    if [ "${ns}" = "${NS_DEXIDP}" ]; then
      h status "${rel}" -n "${ns}" --show-resources=false > "${out}/helm-${ns}-${rel}.txt" 2>&1
    else
      { h status "${rel}" -n "${ns}" --show-resources=false; h get values "${rel}" -n "${ns}"; } > "${out}/helm-${ns}-${rel}.txt" 2>&1
    fi
  done
done
for node in $(kind get nodes --name "${KIND_CLUSTER_NAME}" 2>/dev/null); do
  docker exec "${node}" cat "/etc/containerd/certs.d/localhost:${KIND_REGISTRY_PORT}/hosts.toml" > "${out}/hosts-${node}.txt" 2>&1
done

# API snapshots (only with a working session; the jar holds the session of the probe)
api_use main
if [ -f "${API_JAR}" ] && pf_ensure >/dev/null 2>&1 && [ "$(a /auth/me | jq -r '.role // empty')" != "" ]; then
  a /system/info | jq . > "${out}/api-system-info.json" 2>&1
  a /gitops/status | jq . > "${out}/api-gitops-status.json" 2>&1
  a '/rollouts?limit=50' | jq . > "${out}/api-rollouts.json" 2>&1
  for id in $(a '/rollouts?limit=10' | jq -r '.items[]?.id'); do a "/rollouts/${id}/targets" | jq . > "${out}/api-rollout-${id}-targets.json" 2>&1; done
  a '/agents?limit=200' | jq '[.items[] | {id, kind, hostname, version, status, healthy}]' > "${out}/api-agents.json" 2>&1
  a /stats/overview | jq . > "${out}/api-stats-overview.json" 2>&1
else
  echo "no working API session" > "${out}/api-skipped.txt"
fi
pf_stop_all
if [ "${DIAG_KIND_LOGS:-0}" = 1 ]; then kind export logs "${out}/kind-logs" --name "${KIND_CLUSTER_NAME}" >/dev/null 2>&1; fi
echo "${out}"
