#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Creates/updates ci/tools/<tool>/go.mod and go.sum from the versions in ci/versions.env.
#
#   ci/tools/update.sh              all tools
#   ci/tools/update.sh cosign helm  only these
#
# Afterwards review go.mod/go.sum (read the diff: new dependencies) and commit them. Needs network access (GOPROXY).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
load_versions

# tool -> "<module-with-cmd> <version-variable>" (no associative array: also runs with bash 3.2)
spec() {
  case "$1" in
    crane)       echo "github.com/google/go-containerregistry/cmd/crane CRANE_VERSION" ;;
    gotestsum)   echo "gotest.tools/gotestsum GOTESTSUM_VERSION" ;;
    govulncheck) echo "golang.org/x/vuln/cmd/govulncheck GOVULNCHECK_VERSION" ;;
    go-licenses) echo "github.com/google/go-licenses/v2 GOLICENSES_VERSION" ;;
    gitleaks)    echo "github.com/zricethezav/gitleaks/v8 GITLEAKS_VERSION" ;;
    yq)          echo "github.com/mikefarah/yq/v4 YQ_VERSION" ;;
    kubeconform) echo "github.com/yannh/kubeconform/cmd/kubeconform KUBECONFORM_VERSION" ;;
    kustomize)   echo "sigs.k8s.io/kustomize/kustomize/v5 KUSTOMIZE_VERSION" ;;
    helm)        echo "helm.sh/helm/v4/cmd/helm HELM_VERSION" ;;
    cosign)      echo "github.com/sigstore/cosign/v3/cmd/cosign COSIGN_VERSION" ;;
    kind)        echo "sigs.k8s.io/kind KIND_VERSION" ;;
    actionlint)  echo "github.com/rhysd/actionlint/cmd/actionlint ACTIONLINT_VERSION" ;;
    goreleaser)  echo "github.com/goreleaser/goreleaser/v2 GORELEASER_VERSION" ;;
    *)           return 1 ;;
  esac
}

tools=("$@"); [ "$#" -gt 0 ] || tools=(crane gotestsum govulncheck go-licenses gitleaks yq kubeconform kustomize helm cosign goreleaser kind actionlint)
for t in "${tools[@]}"; do
  sp="$(spec "${t}")" || die "unknown tool: ${t}"
  mod="${sp%% *}"; var="${sp##* }"
  d="${CI_DIR}/tools/${t}"; mkdir -p "${d}"
  rm -f "${d}/go.mod" "${d}/go.sum"
  printf 'module github.com/myriarch/myriarch/ci/tools/%s\n\ngo 1.26.0\n' "${t}" > "${d}/go.mod"
  ( cd "${d}" && GOFLAGS=-mod=mod GOTOOLCHAIN=local go get -tool "${mod}@${!var}" ) || die "go get -tool ${mod} failed"
  log "${t}: ${!var}"
done
