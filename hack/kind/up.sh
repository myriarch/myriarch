#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Creates (or reuses) the kind cluster of the probe and the local registry that serves the freshly built images.
#
#   hack/kind/up.sh
#
# What it does (idempotent: a second run changes nothing and exits 0):
#   1. checks that Docker runs (warns below 6 GiB memory or 4 CPUs) and builds/downloads the pinned tools (kind, kubectl,
#      helm, crane, yq) into .cache/bin;
#   2. starts the registry container (default name myriarch-kind-registry, 127.0.0.1:5001) or reuses it; fails clearly if
#      the port belongs to something else;
#   3. creates the cluster (one control plane, two workers, fixed pod and service subnets) with its own kubeconfig
#      ($PROBE_WORK/kubeconfig; ~/.kube is never touched);
#   4. makes the registry reachable from the nodes as localhost:5001 (containerd hosts.toml in every node, registry
#      attached to the docker network "kind", ConfigMap local-registry-hosting in kube-public).
#
# Environment: KIND_CLUSTER_NAME, KIND_REGISTRY_NAME, KIND_REGISTRY_PORT, KIND_NODE_IMAGE, PROBE_WORK (hack/kind/README.md).
set -euo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
cd "${ROOT}"

need_cmd docker
docker info >/dev/null 2>&1 || die "Docker is not running (start Docker Desktop and retry)"

mem_bytes="$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)"
cpus="$(docker info --format '{{.NCPU}}' 2>/dev/null || echo 0)"
if [ "${mem_bytes:-0}" -lt $((6 * 1024 * 1024 * 1024)) ] 2>/dev/null; then
  warn "Docker has $((mem_bytes / 1024 / 1024)) MiB of memory; the probe needs about 6 GiB (Docker Desktop: Settings > Resources)"
fi
if [ "${cpus:-0}" -lt 4 ] 2>/dev/null; then warn "Docker has ${cpus} CPUs; the probe wants at least 4"; fi

prepare_work_dirs
"${ROOT}/ci/install-tools.sh" kind kubectl helm crane yq >&2

# ---- registry ----------------------------------------------------------------------------------------------------
start_registry() {
  local state
  state="$(docker inspect -f '{{.State.Running}}' "${KIND_REGISTRY_NAME}" 2>/dev/null || true)"
  case "${state}" in
    true) log "registry ${KIND_REGISTRY_NAME}: already running" ;;
    false)
      log "registry ${KIND_REGISTRY_NAME}: starting the stopped container"
      docker start "${KIND_REGISTRY_NAME}" >/dev/null
      ;;
    *)
      log "registry ${KIND_REGISTRY_NAME}: creating (127.0.0.1:${KIND_REGISTRY_PORT})"
      if ! docker run -d --restart=always -p "127.0.0.1:${KIND_REGISTRY_PORT}:5000" --name "${KIND_REGISTRY_NAME}" "${REGISTRY_IMAGE}" >/dev/null; then
        die "cannot start the registry: is port ${KIND_REGISTRY_PORT} in use? Set KIND_REGISTRY_PORT=<free port> or stop the other process"
      fi
      ;;
  esac
  # the container must really publish the configured port (a reused container may have been created with another one)
  local pub
  pub="$(docker port "${KIND_REGISTRY_NAME}" 5000/tcp 2>/dev/null | head -n 1 || true)"
  case "${pub}" in
    *":${KIND_REGISTRY_PORT}") ;;
    *) die "registry container ${KIND_REGISTRY_NAME} publishes '${pub}', not port ${KIND_REGISTRY_PORT}: run hack/kind/down.sh --all" ;;
  esac
  wait_until 30 "curl -fsS -o /dev/null http://127.0.0.1:${KIND_REGISTRY_PORT}/v2/" 1 \
    || die "the registry does not answer on http://127.0.0.1:${KIND_REGISTRY_PORT}/v2/ (port ${KIND_REGISTRY_PORT} belongs to something else?)"
}
start_registry

# ---- cluster -----------------------------------------------------------------------------------------------------
if kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER_NAME}"; then
  log "cluster ${KIND_CLUSTER_NAME}: already exists"
  if [ ! -f "${KUBECONFIG}" ]; then kind export kubeconfig --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}" >&2; fi
else
  log "creating the cluster ${KIND_CLUSTER_NAME} (node image ${KIND_NODE_IMAGE})"
  kind create cluster --name "${KIND_CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}" --config "${KIND_DIR}/cluster.yaml" \
    --kubeconfig "${KUBECONFIG}" --wait 180s >&2
fi
chmod 600 "${KUBECONFIG}"
assert_context
# kind --wait only waits for the control plane: the workers join a little later
k wait --for=condition=Ready node --all --timeout=180s >&2 || die "the nodes did not become Ready within 180 s"

# ---- registry <-> nodes ------------------------------------------------------------------------------------------
if [ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "${KIND_REGISTRY_NAME}" 2>/dev/null)" = null ]; then
  log "connecting the registry to the docker network kind"
  docker network connect kind "${KIND_REGISTRY_NAME}"
fi

nodes="$(kind get nodes --name "${KIND_CLUSTER_NAME}")"
[ -n "${nodes}" ] || die "cluster ${KIND_CLUSTER_NAME} has no nodes"
while IFS= read -r node; do
  # cluster.yaml patches the containerd config; the effective configuration must carry the registry config_path
  if ! docker exec "${node}" containerd config dump 2>/dev/null | grep -q "config_path = '/etc/containerd/certs.d'"; then
    die "node ${node}: containerd has no registry config_path (/etc/containerd/certs.d); delete the cluster (hack/kind/down.sh) and retry"
  fi
  dir="/etc/containerd/certs.d/localhost:${KIND_REGISTRY_PORT}"
  docker exec "${node}" mkdir -p "${dir}"
  printf '[host."http://%s:5000"]\n' "${KIND_REGISTRY_NAME}" | docker exec -i "${node}" sh -c "cat > '${dir}/hosts.toml'"
done <<< "${nodes}"

k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${KIND_REGISTRY_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
YAML

log "cluster ${KIND_CLUSTER_NAME} is ready"
printf 'export KUBECONFIG=%s\n' "${KUBECONFIG}"
