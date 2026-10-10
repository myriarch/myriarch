#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Installs the whole system into the kind cluster without running any check: the building block for a quickstart or a
# demo (hack/kind/probe.sh adds the numbered checks around the same steps).
#
#   hack/kind/up.sh && hack/kind/images.sh && hack/kind/install.sh
#
# What it installs: PostgreSQL and an SSH Git server (namespace probe-infra), the GitOps fixture, the chart (namespace
# myriarch; first with only the required values, then with the full configuration), the simulator (20 collectors) and one
# real collector in container mode. It ends by printing how to reach the UI; the one-time token is never printed.
set -euo pipefail
# shellcheck source=hack/kind/lib/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
# shellcheck source=hack/kind/lib/install.sh
. "${KIND_DIR}/lib/install.sh"
cd "${ROOT}"

assert_context
prepare_work_dirs
load_images
[ -x bin/myriarchd ] && [ -x bin/myrctl ] || make build >&2

log "generating secrets (files below ${SECRETS_DIR})"
generate_secrets
log "installing PostgreSQL and the Git server"
install_infra
wait_infra
gitops_prepare
"${ROOT}/bin/myrctl" validate --strict --components "${ROOT}/examples/sim/components-otelcol-contrib-0.162.0.json" "${GITOPS_WORK}" >&2
gitops_publish "seed"
log "GitOps fixture pushed (${GITOPS_SHA})"

log "installing the chart with the required values only"
chart_install_minimal
kn "${NS_APP}" rollout status deploy/myriarch --timeout=300s >&2
log "upgrading to the full configuration"
write_values_tokens 30
chart_upgrade_full
kn "${NS_APP}" rollout status deploy/myriarch --timeout=300s >&2

log "deploying the simulator and the collector"
deploy_agents
kn "${NS_APP}" rollout status deploy/myriarch-sim --timeout=300s >&2
kn "${NS_APP}" rollout status statefulset/kind-gw --timeout=480s >&2

cat <<TXT

Done. Reach the UI:
  export KUBECONFIG=${KUBECONFIG}
  kubectl -n ${NS_APP} port-forward svc/myriarch 8080:8080      # then open http://localhost:8080
Sign in with the bootstrap admin "admin"; read the one-time token (it works for one sign-in):
  kubectl -n ${NS_APP} get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d
TXT
