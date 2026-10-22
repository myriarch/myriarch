#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the chart package of the release (ci/release/chart-package.sh, push-chart.sh, verify-chart.sh; ADR-0035). No network, no
# registry, no ports:
#  1. chart-package.sh runs with the REAL pinned helm (.cache/bin/helm, `make tools`): the digest lands in the copy and not in the chart of
#     the repository, `helm template` references every image by the digest, a package without the digest fails the check, and so does a
#     chart with one more image that has a tag. Without the binary the case is skipped locally and FAILS when CI=true.
#  2. push-chart.sh and verify-chart.sh run with stand-ins for helm push, crane, cosign and gh (the pinned helm does the rendering).
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u

REL="${ROOT}/ci/release"
HELM_BIN="${ROOT}/.cache/bin/helm"
if [ ! -x "${HELM_BIN}" ]; then
  if [ "${CI:-}" = true ]; then fail "the pinned helm is missing (ci/install-tools.sh helm)"; else printf '  skip  the pinned helm is missing: run make tools\n'; fi
  finish_tests chart-package-test
  exit $?
fi
ls "${ROOT}"/.cache/bin/.stamp-helm-* >/dev/null 2>&1 || { fail "the pinned helm has no build stamp"; finish_tests chart-package-test; exit 1; }

W="${TEST_TMP}"
D1="sha256:$(printf 'a1%.0s' $(seq 32))"
D2="sha256:$(printf 'b2%.0s' $(seq 32))"
CHART="${ROOT}/deploy/helm/myriarch"
C40="$(printf 'c%.0s' $(seq 40))"
cp_run() { ( export TOOLS_BIN="${ROOT}/.cache/bin"; "${REL}/chart-package.sh" "$@" ); }
eq() { if [ "$2" = "$3" ]; then ok "$1"; else fail "$1 (expected '$2', got '$3')"; fi; }

# ==== 1. chart-package.sh ====================================================================================================
before="$(cat "${CHART}/values.yaml" "${CHART}/Chart.yaml" | cksum)"
expect_ok "package: a release candidate" cp_run package --chart "${CHART}" --version 0.2.0-rc.1 --image-digest "${D1}" --out "${W}/rc"
case "${OUT}" in *"CHART_PACKAGE=${W}/rc/myriarch-0.2.0-rc.1.tgz"*) ok "package: prints the package path" ;; *) fail "package: prints no CHART_PACKAGE line: ${OUT}" ;; esac
expect_ok "package: a final version" cp_run package --chart "${CHART}" --version 0.2.0 --image-digest "${D1}" --out "${W}/final"
eq "package: the chart of the repository is unchanged" "${before}" "$(cat "${CHART}/values.yaml" "${CHART}/Chart.yaml" | cksum)"
eq "package: the chart version and the app version are the version" "appVersion: 0.2.0
version: 0.2.0" "$("${HELM_BIN}" show chart "${W}/final/myriarch-0.2.0.tgz" | grep -E '^(version|appVersion):' | sort)"
imgs="$("${HELM_BIN}" template r "${W}/final/myriarch-0.2.0.tgz" | sed -n 's/^[[:space:]-]*image:[[:space:]]*//p' | tr -d '"' | sort -u)"
eq "package: helm template references the image by the digest only" "ghcr.io/myriarch/myriarch@${D1}" "${imgs}"
imgs="$("${HELM_BIN}" template r "${W}/final/myriarch-0.2.0.tgz" --set openshift.enabled=true --set image.tag=9.9.9 | sed -n 's/^[[:space:]-]*image:[[:space:]]*//p' | tr -d '"' | sort -u)"
eq "package: a tag value does not win over the digest" "ghcr.io/myriarch/myriarch@${D1}" "${imgs}"
"${HELM_BIN}" show chart "${W}/final/myriarch-0.2.0.tgz" | grep -q "image: ghcr.io/myriarch/myriarch@${D1}\$" \
  && ok "package: the Artifact Hub annotation names the digest" || fail "package: the Artifact Hub annotation lacks the digest"
tar -xzOf "${W}/final/myriarch-0.2.0.tgz" myriarch/values.schema.source.yaml >/dev/null 2>&1 \
  && fail "package: values.schema.source.yaml is part of the package" || ok "package: values.schema.source.yaml is not part of the package"

expect_ok "verify: the package" cp_run verify --package "${W}/final/myriarch-0.2.0.tgz" --image-digest "${D1}" --version 0.2.0
expect_fail "verify: another digest" "image_not_digest" cp_run verify --package "${W}/final/myriarch-0.2.0.tgz" --image-digest "${D2}"
expect_fail "verify: another version" "chart_version" cp_run verify --package "${W}/final/myriarch-0.2.0.tgz" --image-digest "${D1}" --version 0.2.1
mkdir -p "${W}/plain"
"${HELM_BIN}" package "${CHART}" --version 0.2.0 --app-version 0.2.0 --destination "${W}/plain" >/dev/null
expect_fail "verify: a package without a digest fails" "image_not_digest.*ghcr.io/myriarch/myriarch:0.2.0" cp_run verify --package "${W}/plain/myriarch-0.2.0.tgz" --image-digest "${D1}"
expect_fail "verify: a missing file" "package_invalid" cp_run verify --package "${W}/nothing.tgz" --image-digest "${D1}"

expect_fail "package: a version with v" "version_invalid" cp_run package --chart "${CHART}" --version v0.2.0 --image-digest "${D1}" --out "${W}/o1"
expect_fail "package: a snapshot version" "version_invalid" cp_run package --chart "${CHART}" --version 0.2.0-snapshot --image-digest "${D1}" --out "${W}/o1"
expect_fail "package: a digest without sha256" "digest_invalid" cp_run package --chart "${CHART}" --version 0.2.0 --image-digest "${D1#sha256:}" --out "${W}/o1"
expect_fail "package: a short digest" "digest_invalid" cp_run package --chart "${CHART}" --version 0.2.0 --image-digest sha256:abc --out "${W}/o1"
expect_fail "package: an output directory with files" "chart_dir.*not empty" cp_run package --chart "${CHART}" --version 0.2.0 --image-digest "${D1}" --out "${W}/final"
expect_fail "package: not a chart" "chart_dir" cp_run package --chart "${W}" --version 0.2.0 --image-digest "${D1}" --out "${W}/o2"
expect_fail "package: no arguments" "usage:" cp_run package
expect_fail "package: an unknown command" "usage:" cp_run frobnicate --image-digest "${D1}"

# a chart whose values.yaml has no slot for the digest, one without the annotation entry, and one with a second image that has a tag
cp -R "${CHART}" "${W}/c-noslot" && python3 -I -c '
import sys
p = sys.argv[1] + "/values.yaml"
t = open(p).read().replace("  digest: \"\"\n", "  digest: \"sha256:\"\n", 1)
open(p, "w").write(t)' "${W}/c-noslot"
expect_fail "package: values.yaml without the empty digest" "chart_digest_slot" cp_run package --chart "${W}/c-noslot" --version 0.2.0 --image-digest "${D1}" --out "${W}/o3"
cp -R "${CHART}" "${W}/c-noann" && python3 -I -c '
import sys
p = sys.argv[1] + "/Chart.yaml"
t = open(p).read().replace("image: ghcr.io/myriarch/myriarch\n", "image: ghcr.io/example/other\n", 1)
open(p, "w").write(t)' "${W}/c-noann"
expect_fail "package: Chart.yaml without the placeholder entry" "chart_annotation_slot" cp_run package --chart "${W}/c-noann" --version 0.2.0 --image-digest "${D1}" --out "${W}/o4"
cp -R "${CHART}" "${W}/c-extra"
printf 'apiVersion: v1\nkind: Pod\nmetadata:\n  name: extra\nspec:\n  containers:\n    - name: x\n      image: ghcr.io/example/tools:1.0\n' > "${W}/c-extra/templates/extra.yaml"
expect_fail "package: a second image that has a tag" "image_not_digest.*ghcr.io/example/tools:1.0" cp_run package --chart "${W}/c-extra" --version 0.2.0 --image-digest "${D1}" --out "${W}/o5"
expect_fail "usage: the digest is required" "usage:" cp_run verify --package "${W}/final/myriarch-0.2.0.tgz"

# ==== 2. push-chart.sh and verify-chart.sh ===================================================================================
S="${W}/stubs"; SB="${W}/stubbin"; HOME_DIR="${W}/home"
mkdir -p "${S}/calls" "${SB}" "${HOME_DIR}/.docker"
printf '{"auths":{"ghcr.io":{"auth":"eDp5"}}}\n' > "${HOME_DIR}/.docker/config.json"
cp "${ROOT}"/.cache/bin/.stamp-helm-* "${SB}/"
cat > "${SB}/helm" <<STUB
#!/usr/bin/env bash
set -u
d="\${STUB_DIR:?}"
if [ "\${1:-}" = push ]; then
  { printf '%s\n' "\$@"; echo "HELM_REGISTRY_CONFIG=\${HELM_REGISTRY_CONFIG-unset}"; echo '----'; } >> "\${d}/calls/helm.log"
  [ ! -f "\${d}/helm.push.fail" ] || { echo "Error: push failed" >&2; exit 1; }
  printf 'Pushed: %s/myriarch:0.2.0\nDigest: %s\n' "\${2%/*}" "\$(cat "\${d}/chart.digest")"
  exit 0
fi
exec "${HELM_BIN}" "\$@"
STUB
cat > "${SB}/crane" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = version ]; then echo v0.22.1; exit 0; fi
{ printf '%s\n' "$@"; echo '----'; } >> "${d}/calls/crane.log"
case "$1" in
  ls) if [ -f "${d}/crane.ls.err" ]; then cat "${d}/crane.ls.err" >&2; exit 1; fi; cat "${d}/tags" 2>/dev/null ;;
  digest) if [ -f "${d}/crane.digest.override" ]; then cat "${d}/crane.digest.override"; else cat "${d}/chart.digest"; fi ;;
  validate) [ ! -f "${d}/crane.validate.fail" ] || exit 1 ;;
  manifest) cat "${d}/manifest.json" ;;
  blob) cat "${d}/chart.tgz" ;;
esac
exit 0
STUB
cat > "${SB}/cosign" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = version ]; then printf 'GitVersion:    v3.1.3\n'; exit 0; fi
{ printf '%s\n' "$@"; echo '----'; } >> "${d}/calls/cosign.log"
if [ -f "${d}/cosign.fail" ] && [[ "$*" =~ $(cat "${d}/cosign.fail") ]]; then echo "Error: configured stub failure" >&2; exit 1; fi
if [ "$1" = verify ]; then
  ref="${*: -1}"
  printf '[{"critical":{"identity":{"docker-reference":"%s"},"image":{"docker-manifest-digest":"%s"},"type":"https://sigstore.dev/cosign/sign/v1"},"optional":{}}]\n' "${ref%@*}" "${ref#*@}"
fi
exit 0
STUB
cat > "${SB}/gh" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = --version ]; then printf 'gh version 2.102.0 (2026-10-01)\n'; exit 0; fi
{ printf '%s\n' "$@"; echo '----'; } >> "${d}/calls/gh.log"
if [ -f "${d}/gh.fail" ]; then echo "Error: no attestation" >&2; exit 1; fi
exit 0
STUB
chmod +x "${SB}"/*
RB="${W}/restricted"; mkdir -p "${RB}"
for t in bash sh env python3 sed awk grep find mktemp cat cp ln mv rm mkdir chmod ls head tail cut tr wc sort sleep basename dirname uniq cmp touch sha256sum shasum tar gzip; do
  p="$(command -v "${t}" 2>/dev/null || true)"; [ -n "${p}" ] || continue
  printf '#!/bin/sh\nexec "%s" "$@"\n' "${p}" > "${RB}/${t}"; chmod +x "${RB}/${t}"
done
( export PATH="${RB}"; for t in go node pnpm npm make goreleaser helm cosign crane gh; do ! command -v "${t}" >/dev/null 2>&1 || exit 1; done ) \
  && ok "the restricted PATH has no go, node, pnpm, npm, make, goreleaser, helm, cosign, crane or gh" || fail "the restricted PATH is not restricted"

TAG=v0.2.0; VER=0.2.0
PKG="${W}/final/myriarch-${VER}.tgz"
CD="sha256:$(printf 'd4%.0s' $(seq 32))"
REF_WF="myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}"
reset() { rm -rf "${S}/calls"; mkdir -p "${S}/calls"; find "${S}" -maxdepth 1 -type f -exec rm -f {} +; printf '%s' "${CD}" > "${S}/chart.digest"; }
run_priv() { ( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}" GITHUB_WORKFLOW_REF="${REF_WF}" IMAGE_DIGEST="${D1}" GH_TOKEN=t; unset DOCKER_CONFIG; "$@" ); }
pc() { run_priv "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit "${C40}"; }
calls() { cat "${S}/calls/$1.log" 2>/dev/null || true; }
count() { calls "$1" | tr '\n' ' ' | sed 's/ ---- /\n/g' | grep -Ec -- "$2" || true; }

reset
expect_ok "push-chart: the chart" pc
case "${OUT}" in *"chart-digest=${CD}"*) ok "push-chart: prints chart-digest" ;; *) fail "push-chart: no chart-digest line: ${OUT}" ;; esac
eq "push-chart: helm push to the parent of the chart repository" "1" "$(count helm "^push ${PKG} oci://ghcr.io/myriarch/charts HELM_REGISTRY_CONFIG=${HOME_DIR}/.docker/config.json")"
eq "push-chart: keyless signature of the chart digest" "1" "$(count cosign "^sign --yes ghcr.io/myriarch/charts/myriarch@${CD}")"
eq "push-chart: verify with the exact identity" "1" "$(count cosign "^verify --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG} --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/${TAG} --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push ghcr.io/myriarch/charts/myriarch@${CD}")"
eq "push-chart: the digest is read back and the image is validated" "1" "$(count crane "^validate --remote ghcr.io/myriarch/charts/myriarch@${CD}")"
eq "push-chart: no recursive signature" "0" "$(count cosign "recursive")"

reset; printf '0.1.0\n0.2.0\n' > "${S}/tags"
expect_fail "push-chart: an existing chart tag is never overwritten" "chart_tag_exists" pc
eq "push-chart: nothing was pushed" "0" "$(count helm '^push')"
reset; printf '0.1.0\n' > "${S}/tags"
expect_ok "push-chart: other tags do not matter" pc
reset; echo 'Error: NAME_UNKNOWN: repository name not known to registry' > "${S}/crane.ls.err"
expect_ok "push-chart: the first chart ever (repository unknown)" pc
reset; echo 'Error: connection refused' > "${S}/crane.ls.err"
expect_fail "push-chart: a failure to list the tags" "push_failed.*connection refused" pc
eq "push-chart: nothing was pushed after the listing failed" "0" "$(count helm '^push')"
reset; touch "${S}/helm.push.fail"
expect_fail "push-chart: helm push fails" "push_failed" pc
eq "push-chart: nothing is signed after a failed push" "0" "$(count cosign '^sign')"
reset; printf '%s' "sha256:$(printf 'e5%.0s' $(seq 32))" > "${S}/crane.digest.override"
expect_fail "push-chart: the registry reports another digest" "chart_digest_mismatch" pc
eq "push-chart: nothing is signed after a digest mismatch" "0" "$(count cosign '^sign')"
reset; touch "${S}/crane.validate.fail"
expect_fail "push-chart: an incomplete chart in the registry" "chart_digest_mismatch" pc
reset; echo 'sign' > "${S}/cosign.fail"
expect_fail "push-chart: the signature fails" "sign_failed" pc
reset; echo '^verify' > "${S}/cosign.fail"
expect_fail "push-chart: the verification of the signature fails" "signature_invalid" pc
reset
expect_fail "push-chart: outside the release workflow" "workflow_ref" run_priv env GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/ci.yml@refs/heads/main "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a workflow at another tag" "workflow_ref" run_priv env GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.1 "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a package of another version" "package_invalid" run_priv "${REL}/push-chart.sh" --package "${W}/rc/myriarch-0.2.0-rc.1.tgz" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a package without the digest" "image_not_digest" run_priv "${REL}/push-chart.sh" --package "${W}/plain/myriarch-0.2.0.tgz" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a package of another image digest" "image_not_digest" run_priv env IMAGE_DIGEST="${D2}" "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: no IMAGE_DIGEST" "digest_invalid" run_priv env -u IMAGE_DIGEST "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a malformed tag" "tag_invalid" run_priv "${REL}/push-chart.sh" --package "${PKG}" --tag 0.2.0 --commit "${C40}"
expect_fail "push-chart: a malformed commit" "commit_invalid" run_priv "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit abc
expect_fail "push-chart: no login" "push_failed.*registry-login" run_priv env HOME="${W}/nohome" "${REL}/push-chart.sh" --package "${PKG}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: usage" "usage:" run_priv "${REL}/push-chart.sh"

# verify-chart.sh: the manifest of the chart in the registry is served by the stand-in and must hash to the digest
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
mk_registry() { # layer-media-type
  local layer
  cp "${PKG}" "${S}/chart.tgz"
  layer="sha256:$(sha "${PKG}")"
  printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"%s","size":2},"layers":[{"mediaType":"%s","digest":"%s","size":1}]}' \
    "${D2}" "$1" "${layer}" > "${S}/manifest.json"
  MDIGEST="sha256:$(sha "${S}/manifest.json")"
  printf '%s' "${MDIGEST}" > "${S}/chart.digest"
  printf '0.2.0\nsha256-%s\n' "${MDIGEST#sha256:}" > "${S}/tags"
}
vc() { run_priv "${REL}/verify-chart.sh" --tag "${TAG}" --commit "${C40}" --digest "${MDIGEST}" --image-digest "${D1}"; }
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip
expect_ok "verify-chart: the chart" vc
eq "verify-chart: the signature is verified for the chart digest" "1" "$(count cosign "^verify .* ghcr.io/myriarch/charts/myriarch@${MDIGEST}\$")"
eq "verify-chart: the provenance is verified from the registry with the identity of the run" "1" "$(count gh "^attestation verify oci://ghcr.io/myriarch/charts/myriarch@${MDIGEST} --repo myriarch/myriarch --bundle-from-oci --cert-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG} --source-ref refs/tags/${TAG} --source-digest ${C40} --deny-self-hosted-runners\$")"
eq "verify-chart: the layer is fetched by its digest" "1" "$(count crane "^blob ghcr.io/myriarch/charts/myriarch@sha256:")"
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '%s' "sha256:$(printf 'f6%.0s' $(seq 32))" > "${S}/crane.digest.override"
expect_fail "verify-chart: the tag points to another digest" "chart_digest_mismatch" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '0.2.0\nlatest\n' > "${S}/tags"
expect_fail "verify-chart: a tag latest" "chart_tag_unexpected.*latest" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '0.2.0\nnightly\n' > "${S}/tags"
expect_fail "verify-chart: an unexpected tag" "chart_tag_unexpected.*nightly" vc
reset; mk_registry application/octet-stream
expect_fail "verify-chart: a manifest without the chart layer" "chart_layer" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; cp "${W}/plain/myriarch-0.2.0.tgz" "${S}/chart.tgz"
expect_fail "verify-chart: a layer that does not hash to its digest" "chart_layer.*does not hash" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf 'x' >> "${S}/manifest.json"
expect_fail "verify-chart: a manifest that does not hash to the digest" "chart_layer.*does not hash" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip
expect_fail "verify-chart: a chart of another image digest" "image_not_digest" run_priv "${REL}/verify-chart.sh" --tag "${TAG}" --commit "${C40}" --digest "${MDIGEST}" --image-digest "${D2}"
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; echo '^verify' > "${S}/cosign.fail"
expect_fail "verify-chart: the signature does not verify" "signature_invalid" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; touch "${S}/gh.fail"
expect_fail "verify-chart: the provenance does not verify" "attestation_invalid" vc
expect_fail "verify-chart: usage" "usage:" run_priv "${REL}/verify-chart.sh"

finish_tests chart-package-test
