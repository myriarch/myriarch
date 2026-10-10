#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Deletes the kind cluster of the probe. Idempotent: a missing cluster is not an error.
#
#   hack/kind/down.sh          delete the cluster (the registry container and the work directory stay)
#   hack/kind/down.sh --all    also remove the registry container and $PROBE_WORK (kubeconfig, secrets, logs, results)
set -euo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

all=0
case "${1:-}" in
  "") ;;
  --all) all=1 ;;
  -h | --help) sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) die "usage: hack/kind/down.sh [--all]" ;;
esac

if have docker && docker info >/dev/null 2>&1; then
  if have kind && kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER_NAME}"; then
    log "deleting the cluster ${KIND_CLUSTER_NAME}"
    kind delete cluster --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}" >&2 || kind delete cluster --name "${KIND_CLUSTER_NAME}" >&2
  else
    log "cluster ${KIND_CLUSTER_NAME}: not present"
  fi
  if [ "${all}" = 1 ]; then
    if docker inspect "${KIND_REGISTRY_NAME}" >/dev/null 2>&1; then
      log "removing the registry container ${KIND_REGISTRY_NAME}"
      docker rm -f "${KIND_REGISTRY_NAME}" >/dev/null
    fi
  fi
else
  warn "Docker is not running: nothing to delete there"
fi

if [ "${all}" = 1 ]; then
  # only a directory that up.sh/the probe created (marker file) is removed
  if [ -f "${PROBE_WORK}/.myriarch-kind-work" ]; then
    log "removing ${PROBE_WORK}"
    rm -rf "${PROBE_WORK}"
  elif [ -e "${PROBE_WORK}" ]; then
    warn "${PROBE_WORK} has no .myriarch-kind-work marker: not removed"
  fi
else
  rm -f "${KUBECONFIG}"
fi
