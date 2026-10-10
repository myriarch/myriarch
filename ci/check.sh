#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Static consistency check of the CI artifacts (runs locally and in CI, needs only bash/awk/grep):
#  - scripts: executable (except the sourced contrib/enterprise-mirror/ca-env.sh), syntax ok (bash -n)
#  - tool modules in ci/tools/<tool>: go.mod matches the version in ci/versions.env
#  - image digests: golang/distroless identical across the Dockerfiles, every FROM and the compose postgres pinned by
#    digest (the postgres service of ci.yml uses the same digest), MYRCTL_BASE_IMAGE matches myrctl.Dockerfile
#  - OTELCOL_* pins agree with each other and with deploy/k8s/collector/Dockerfile
#  - reuse (ci/tools/reuse): requirements.in and requirements.txt pin the version of ci/versions.env, the build
#    requirements pin poetry-core, and every pinned package carries hashes
#  - .node-version holds one exact Node.js release of 22 or newer
#  - goreleaser.yaml references the signing script, which exists
#  - one version source: VERSION and every copy of the product version agree (ci/check-versions.sh)
#  - ci/ and contrib/enterprise-mirror/ script paths mentioned in the Makefile, goreleaser.yaml and the scripts exist
#    (scripts/ is deliberately not covered here)
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions
cd "${ROOT}"
status=0
bad() { printf '[ci] ERROR: %s\n' "$*" >&2; status=1; }

for f in ci/*.sh ci/release/*.sh ci/tests/*.sh ci/tools/*.sh contrib/enterprise-mirror/*.sh contrib/enterprise-mirror/tests/*.sh \
         scripts/set-version.sh deploy/scripts/install-linux.sh deploy/release/authenticode.sh hack/kind/*.sh hack/kind/fault/*.sh; do
  case "${f}" in
    contrib/enterprise-mirror/ca-env.sh) ;;   # sourced, not executed: syntax only (below)
    *) [ -x "${f}" ] || bad "${f} is not executable (chmod +x)" ;;
  esac
  bash -n "${f}" 2>/dev/null || sh -n "${f}" 2>/dev/null || bad "${f}: syntax error"
done

# the libraries of the kind probe are sourced, not executed: syntax only
for f in hack/kind/lib/*.sh hack/kind/git/*.sh; do
  bash -n "${f}" 2>/dev/null || sh -n "${f}" 2>/dev/null || bad "${f}: syntax error"
done

# Tool modules (ci/tools/<tool>/): go.mod requires exactly the version from ci/versions.env, go.sum is in the repository
check_tool() { # tool module versions-variable
  local d="ci/tools/$1" want="${!3}"
  [ -f "${d}/go.mod" ] && [ -f "${d}/go.sum" ] || { bad "${d}: go.mod/go.sum missing (ci/tools/update.sh $1)"; return; }
  grep -Eq "^[[:space:]]*(require[[:space:]]+)?$2[[:space:]]+${want}([[:space:]]|\$)" "${d}/go.mod" \
    || bad "${d}/go.mod: $2 is not ${want} (ci/versions.env ${3}); ci/tools/update.sh $1"
}
check_tool crane       github.com/google/go-containerregistry CRANE_VERSION
check_tool gotestsum   gotest.tools/gotestsum                 GOTESTSUM_VERSION
check_tool govulncheck golang.org/x/vuln                      GOVULNCHECK_VERSION
check_tool go-licenses github.com/google/go-licenses/v2    GOLICENSES_VERSION
check_tool gitleaks    github.com/zricethezav/gitleaks/v8        GITLEAKS_VERSION
check_tool yq          github.com/mikefarah/yq/v4             YQ_VERSION
check_tool kubeconform github.com/yannh/kubeconform           KUBECONFORM_VERSION
check_tool kustomize   sigs.k8s.io/kustomize/kustomize/v5     KUSTOMIZE_VERSION
check_tool helm        helm.sh/helm/v4                        HELM_VERSION
check_tool cosign      github.com/sigstore/cosign/v3          COSIGN_VERSION
check_tool goreleaser  github.com/goreleaser/goreleaser/v2   GORELEASER_VERSION
check_tool kind        sigs.k8s.io/kind                       KIND_VERSION
check_tool actionlint  github.com/rhysd/actionlint            ACTIONLINT_VERSION
check_tool gh          github.com/cli/cli/v2                  GH_VERSION
check_tool go-winres   github.com/tc-hib/go-winres            GOWINRES_VERSION

# reuse (a Python tool, ci/tools/reuse/): the input files and the hash-pinned requirements agree with REUSE_VERSION
pins() { # file requirement - the file has a line that starts with the requirement as its first word
  awk -v want="$2" '$1 == want { found = 1 } END { exit !found }' "$1"
}
pins ci/tools/reuse/requirements.in "reuse==${REUSE_VERSION}" \
  || bad "ci/tools/reuse/requirements.in does not pin reuse==${REUSE_VERSION} (ci/versions.env REUSE_VERSION)"
pins ci/tools/reuse/requirements.txt "reuse==${REUSE_VERSION}" \
  || bad "ci/tools/reuse/requirements.txt does not pin reuse==${REUSE_VERSION}: regenerate it (comment in ci/versions.env)"
grep -Eq '^poetry-core==[0-9][0-9.]*( |$)' ci/tools/reuse/build-requirements.txt \
  || bad "ci/tools/reuse/build-requirements.txt does not pin poetry-core"
for f in ci/tools/reuse/requirements.txt ci/tools/reuse/build-requirements.txt; do
  # every pinned package (a line that starts a requirement) carries at least one hash
  awk '/^[A-Za-z0-9]/ { if (pkg != "" && hashes == 0) missing = 1; pkg = $1; hashes = 0 }
       index($0, "--hash=sha256:") > 0 { hashes++ }
       END { if (pkg != "" && hashes == 0) missing = 1; exit missing }' "${f}" \
    || bad "${f}: a package without --hash (uv pip compile --generate-hashes)"
done

# .node-version: one exact release of an LTS line that satisfies the engines of web/ and website/ (22 or newer)
node_version="$(cat .node-version 2>/dev/null || true)"
printf '%s' "${node_version}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || bad ".node-version must contain one exact version such as 24.21.0 (got '${node_version}')"
[ "${node_version%%.*}" -ge 22 ] 2>/dev/null || bad ".node-version (${node_version}) is older than Node.js 22, the floor of web/ and website/"

# digest of the first line of <file> matching <pattern>
digest_in() { grep -E "$2" "$1" | head -n 1 | sed -n 's/.*@\(sha256:[0-9a-f]\{64\}\).*/\1/p'; }

# MYRCTL_BASE_IMAGE == base of myrctl.Dockerfile
want="$(printf '%s' "${MYRCTL_BASE_IMAGE}" | sed -n 's/.*@\(sha256:[0-9a-f]\{64\}\).*/\1/p')"
got="$(digest_in deploy/docker/myrctl.Dockerfile '^FROM curlimages/curl:')"
[ -n "${want}" ] || bad "no digest in ci/versions.env (MYRCTL_BASE_IMAGE)"
[ "${want}" = "${got}" ] || bad "myrctl base: ci/versions.env (MYRCTL_BASE_IMAGE=${want}) and deploy/docker/myrctl.Dockerfile (${got:-no digest}) differ"

# golang: myriarchd.Dockerfile and myriarch-sim.Dockerfile use the same digest
g1="$(digest_in deploy/docker/myriarchd.Dockerfile '^FROM golang:')"
g2="$(digest_in deploy/docker/myriarch-sim.Dockerfile '^FROM golang:')"
{ [ -n "${g1}" ] && [ "${g1}" = "${g2}" ]; } || bad "golang digest differs between myriarchd.Dockerfile (${g1:-none}) and myriarch-sim.Dockerfile (${g2:-none})"

# distroless: all three Dockerfiles use the same digest
d1="$(digest_in deploy/docker/myriarchd.Dockerfile '^FROM gcr.io/distroless/static')"
d2="$(digest_in deploy/docker/myriarchd-release.Dockerfile '^FROM gcr.io/distroless/static')"
d3="$(digest_in deploy/docker/myriarch-sim.Dockerfile '^FROM gcr.io/distroless/static')"
{ [ -n "${d1}" ] && [ "${d1}" = "${d2}" ] && [ "${d1}" = "${d3}" ]; } || bad "distroless digest differs between myriarchd/myriarchd-release/myriarch-sim.Dockerfile"

# every FROM line of the Dockerfiles and the postgres image of the compose file are pinned by digest
for f in deploy/docker/*.Dockerfile; do
  grep -E '^FROM ' "${f}" | grep -Evq '@sha256:[0-9a-f]{64}' && bad "${f}: FROM without digest"
done
grep -Eq 'image: postgres:[^[:space:]]+@sha256:[0-9a-f]{64}' deploy/compose/docker-compose.yaml || bad "deploy/compose/docker-compose.yaml: postgres image without digest"
# the PostgreSQL service of the Go tests in CI uses the digest of the compose file (a multi-architecture index: amd64 and arm64 runners)
c1="$(digest_in deploy/compose/docker-compose.yaml 'image: postgres:')"
c2="$(digest_in .github/workflows/ci.yml 'image: postgres:')"
{ [ -n "${c1}" ] && [ "${c1}" = "${c2}" ]; } || bad ".github/workflows/ci.yml: the postgres service image (${c2:-no digest}) is not the digest of deploy/compose/docker-compose.yaml (${c1:-none})"

# The kind probe (hack/kind): every image pin carries a digest, and the manifests only use @@PLACEHOLDER@@ images (the
# images are filled in by the scripts, never hard-coded).
while IFS= read -r line; do
  case "${line}" in
    *_IMAGE=*) printf '%s' "${line#*=}" | grep -Eq '@sha256:[0-9a-f]{64}$' || bad "hack/kind/versions.env: ${line%%=*} is not pinned by digest" ;;
  esac
done < hack/kind/versions.env
grep -Eq '^DEX_CHART_SHA256=[0-9a-f]{64}$' hack/kind/versions.env || bad "hack/kind/versions.env: DEX_CHART_SHA256 is missing or not a SHA-256"
for f in hack/kind/manifests/*.yaml; do
  grep -E '^[[:space:]]*(- )?image:' "${f}" | grep -Evq 'image: "@@[A-Z_]+@@"$' && bad "${f}: an image is not an @@PLACEHOLDER@@"
done

grep -q "^ARG OTELCOL_VERSION=${OTELCOL_VERSION}\$" deploy/k8s/collector/Dockerfile || bad "OTELCOL_VERSION (${OTELCOL_VERSION}) does not match the default in deploy/k8s/collector/Dockerfile"
# the image arguments of the collector Dockerfile default to the reviewed digests (same as ci/versions.env and the Go image of the other Dockerfiles)
grep -Fxq "ARG COLLECTOR_IMAGE=${OTELCOL_CONTRIB_IMAGE}" deploy/k8s/collector/Dockerfile || bad "deploy/k8s/collector/Dockerfile: ARG COLLECTOR_IMAGE is not OTELCOL_CONTRIB_IMAGE of ci/versions.env"
grep -Fxq "ARG SUPERVISOR_IMAGE=${OTELCOL_SUPERVISOR_IMAGE}" deploy/k8s/collector/Dockerfile || bad "deploy/k8s/collector/Dockerfile: ARG SUPERVISOR_IMAGE is not OTELCOL_SUPERVISOR_IMAGE of ci/versions.env"
g3="$(digest_in deploy/k8s/collector/Dockerfile '^ARG GO_IMAGE=golang:')"
{ [ -n "${g3}" ] && [ "${g3}" = "${g1}" ]; } || bad "deploy/k8s/collector/Dockerfile: ARG GO_IMAGE (${g3:-no digest}) is not the golang digest of the other Dockerfiles (${g1:-none})"
grep -E '^ARG [A-Z_]*IMAGE=' deploy/k8s/collector/Dockerfile | grep -Evq '@sha256:[0-9a-f]{64}$' && bad "deploy/k8s/collector/Dockerfile: an ARG ...IMAGE default without digest"
for v in SHELLCHECK_SHA256_DARWIN_ARM64 SHELLCHECK_SHA256_DARWIN_AMD64 SHELLCHECK_SHA256_LINUX_ARM64 SHELLCHECK_SHA256_LINUX_AMD64 \
         KUBECTL_SHA256_DARWIN_ARM64 KUBECTL_SHA256_DARWIN_AMD64 KUBECTL_SHA256_LINUX_ARM64 KUBECTL_SHA256_LINUX_AMD64 \
         HUGO_SHA256_DARWIN_UNIVERSAL HUGO_SHA256_LINUX_ARM64 HUGO_SHA256_LINUX_AMD64 \
         DART_SASS_SHA256_DARWIN_ARM64 DART_SASS_SHA256_DARWIN_AMD64 DART_SASS_SHA256_LINUX_ARM64 DART_SASS_SHA256_LINUX_AMD64 \
         LYCHEE_SHA256_DARWIN_ARM64 LYCHEE_SHA256_DARWIN_AMD64 LYCHEE_SHA256_LINUX_ARM64 LYCHEE_SHA256_LINUX_AMD64 \
         SYFT_SHA256_DARWIN_ARM64 SYFT_SHA256_DARWIN_AMD64 SYFT_SHA256_LINUX_ARM64 SYFT_SHA256_LINUX_AMD64 \
         ZIZMOR_SHA256_DARWIN_ARM64 ZIZMOR_SHA256_DARWIN_AMD64 ZIZMOR_SHA256_LINUX_ARM64 ZIZMOR_SHA256_LINUX_AMD64; do
  printf '%s' "${!v:-}" | grep -Eq '^[0-9a-f]{64}$' || bad "${v} is missing or not a SHA-256 in ci/versions.env"
done
# syft and zizmor are release downloads: the version has no "v" prefix (the asset URLs use it with one), and the pins above are the digests of that release
printf '%s' "${SYFT_VERSION:-}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || bad "SYFT_VERSION in ci/versions.env is missing or not a plain version (for example 1.54.1)"
printf '%s' "${ZIZMOR_VERSION:-}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || bad "ZIZMOR_VERSION in ci/versions.env is missing or not a plain version (for example 1.30.1)"
[ ! -e ci/tools/syft ] || bad "ci/tools/syft exists: syft is a release download pinned in ci/versions.env, not a Go module"
case "${OTELCOL_CONTRIB_IMAGE}"    in *":${OTELCOL_VERSION}@sha256:"*) ;; *) bad "OTELCOL_CONTRIB_IMAGE does not match OTELCOL_VERSION" ;; esac
case "${OTELCOL_SUPERVISOR_IMAGE}" in *":${OTELCOL_VERSION}@sha256:"*) ;; *) bad "OTELCOL_SUPERVISOR_IMAGE does not match OTELCOL_VERSION" ;; esac

if [ ! -f contrib/enterprise-mirror/sign-blob.sh ] || ! grep -q 'contrib/enterprise-mirror/sign-blob.sh' deploy/release/goreleaser.yaml; then
  bad "goreleaser.yaml does not reference contrib/enterprise-mirror/sign-blob.sh (or the script is missing)"
fi

# Script paths below ci/ and contrib/enterprise-mirror/ that are mentioned in the Makefile, goreleaser.yaml and the
# ci/ and contrib/ scripts must exist.
while IFS= read -r f; do
  while IFS= read -r p; do
    [ -f "${p}" ] || bad "${f} references ${p}, but the file does not exist"
  done < <(grep -Eo '(^|[^A-Za-z0-9_./-])(ci|contrib/enterprise-mirror)/[A-Za-z0-9_./-]+\.(sh|py|env)' "${f}" | sed -E 's|^[^cC]*||' | sort -u)
done < <({ printf '%s\n' Makefile deploy/release/goreleaser.yaml; find ci contrib -name '*.sh' -type f; } | sort -u)

# one version source: VERSION and every copy of the product version (chart, web UI, OpenAPI, examples) agree
"${CI_DIR}/check-versions.sh" || status=1

# build contexts: Dockerfiles that copy the whole context have an ignore file that covers .gitignore
"${CI_DIR}/check-dockerignore.sh" || status=1

if [ "${status}" = 0 ]; then log "check.sh: all consistent"; fi
exit "${status}"
