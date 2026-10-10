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
    gh)          echo "github.com/cli/cli/v2/cmd/gh GH_VERSION" ;;
    go-winres)   echo "github.com/tc-hib/go-winres GOWINRES_VERSION" ;;
    *)           return 1 ;;
  esac
}

# Dependencies that are raised above the version the tool requires, because govulncheck reports a vulnerability in the pinned
# one. Policy: after a version change, run `govulncheck -mode=binary -show verbose .cache/bin/<tool>` on the built tool and raise
# every module that it lists with a fixed version (called or not) to the lowest version that fixes all of its advisories; the
# standard library is fixed by the Go toolchain, not here. Modules that report "Fixed in: N/A" cannot be raised. A raise never
# lowers a module: if the tool already requires an equal or newer version, the entry is skipped with a log line and is obsolete
# (remove it). One "<module> <version>" per line. syft is a release download and has no module here.
raised() {
  case "$1" in
    go-winres) echo "golang.org/x/image v0.45.0" ;;  # GO-2026-6222, -5066, -5062, -5061, -4961
    gh)        echo "golang.org/x/net v0.60.0" ;;    # GO-2026-6617, -6612, -6611, -6610, -6603
    *)         return 0 ;;
  esac
}

# semver_ge a b: a >= b for plain vX.Y.Z versions (sort -V orders them correctly)
semver_ge() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]; }

tools=("$@"); [ "$#" -gt 0 ] || tools=(crane gotestsum govulncheck go-licenses gitleaks yq kubeconform kustomize helm cosign goreleaser kind actionlint gh go-winres)
for t in "${tools[@]}"; do
  sp="$(spec "${t}")" || die "unknown tool: ${t}"
  mod="${sp%% *}"; var="${sp##* }"
  d="${CI_DIR}/tools/${t}"; mkdir -p "${d}"
  rm -f "${d}/go.mod" "${d}/go.sum"
  printf 'module github.com/myriarch/myriarch/ci/tools/%s\n\ngo 1.26.0\n' "${t}" > "${d}/go.mod"
  ( cd "${d}" && GOFLAGS=-mod=mod GOTOOLCHAIN=local go get -tool "${mod}@${!var}" ) || die "go get -tool ${mod} failed"
  while IFS= read -r rs; do
    [ -n "${rs}" ] || continue
    rmod="${rs%% *}"; rver="${rs##* }"
    cur="$( cd "${d}" && GOFLAGS=-mod=mod GOTOOLCHAIN=local go list -m -f '{{.Version}}' "${rmod}" 2>/dev/null )" || cur=""
    if [ -n "${cur}" ] && semver_ge "${cur}" "${rver}"; then
      log "${t}: ${rmod} is already ${cur}, the raise to ${rver} is obsolete: remove it from raised() in ci/tools/update.sh"
      continue
    fi
    ( cd "${d}" && GOFLAGS=-mod=mod GOTOOLCHAIN=local go get "${rmod}@${rver}" ) || die "go get ${rs} (${t}) failed"
    log "${t}: raised ${rmod} to ${rver}"
  done < <(raised "${t}")
  log "${t}: ${!var}"
done
