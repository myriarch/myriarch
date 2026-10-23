#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the chart package of the release (ci/release/chart-package.sh, push-chart.sh, verify-chart.sh; ADR-0035). No network and no
# real registry; the only listener is a throwaway registry of the pinned crane on 127.0.0.1 (ephemeral port):
#  1. chart-package.sh runs with the REAL pinned helm and crane (.cache/bin, `make tools`): the digest lands in the copy and not in the
#     chart of the repository, `helm template` references every image by RELEASE_IMAGE_SERVER@digest, a package without the digest, with
#     the digest of another repository, or with one more image that has a tag fails the check, and the OCI layout of the package pushes
#     by digest without a tag and with the digest that helm push printed. Without the binaries the case is skipped locally and FAILS
#     when CI=true.
#  2. push-chart.sh and verify-chart.sh run with stand-ins for crane push, cosign and gh (the layout of part 1 is real, the pinned helm
#     does the rendering).
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u

REL="${ROOT}/ci/release"
HELM_BIN="${ROOT}/.cache/bin/helm"
CRANE_BIN="${ROOT}/.cache/bin/crane"
if [ ! -x "${HELM_BIN}" ] || [ ! -x "${CRANE_BIN}" ]; then
  if [ "${CI:-}" = true ]; then fail "the pinned helm or crane is missing (ci/install-tools.sh helm crane)"; else printf '  skip  the pinned helm or crane is missing: run make tools\n'; fi
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
cp -R "${CHART}" "${W}/c-repo" && python3 -I -c '
import sys
p = sys.argv[1] + "/values.yaml"
t = open(p).read().replace("  repository: myriarch/myriarch\n", "  repository: example/other\n", 1)
open(p, "w").write(t)' "${W}/c-repo"
expect_fail "package: the digest of another repository is refused" "image_not_digest.*ghcr.io/example/other@${D1}" cp_run package --chart "${W}/c-repo" --version 0.2.0 --image-digest "${D1}" --out "${W}/o6"
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
PKG0="${W}/final/myriarch-0.2.0.tgz"

# layout: the package as an OCI layout, pushed by digest to a throwaway registry of the pinned crane, like push-chart.sh does to ghcr.io
expect_ok "layout: the package" cp_run layout --package "${PKG0}" --out "${W}/lay/chart"
LAYDIG="$(printf '%s\n' "${OUT}" | sed -n 's/^chart-digest=//p')"
[[ "${LAYDIG}" =~ ^sha256:[0-9a-f]{64}$ ]] && ok "layout: prints chart-digest" || fail "layout: no chart-digest line: ${OUT}"
eq "layout: index.json holds exactly the manifest of the digest" "${LAYDIG}" "$(python3 -I -c 'import json,sys; m=json.load(open(sys.argv[1]))["manifests"]; print(m[0]["digest"] if len(m)==1 else "n/a")' "${W}/lay/chart/index.json")"
expect_fail "layout: an output directory with files" "chart_dir.*not empty" cp_run layout --package "${PKG0}" --out "${W}/lay/chart"
expect_fail "layout: a missing package" "chart_dir" cp_run layout --package "${W}/nothing.tgz" --out "${W}/lay/o2"
"${CRANE_BIN}" registry serve --address 127.0.0.1:0 > /dev/null 2> "${W}/reg.log" &
REGPID=$!
REGPORT=""
for _ in $(seq 1 50); do REGPORT="$(sed -n 's/.*serving on port \([0-9][0-9]*\).*/\1/p' "${W}/reg.log" | head -n 1)"; [ -z "${REGPORT}" ] || break; sleep 0.2; done
if [ -n "${REGPORT}" ]; then
  REG="127.0.0.1:${REGPORT}/charts/myriarch"
  "${CRANE_BIN}" push "${W}/lay/chart" "${REG}@${LAYDIG}" > /dev/null 2>&1 && ok "layout: crane push by digest" || fail "layout: crane push by digest failed"
  eq "layout: the pushed digest is the digest that helm push printed" "${LAYDIG}" "$("${CRANE_BIN}" digest "${REG}@${LAYDIG}" 2>/dev/null)"
  eq "layout: a push by digest sets no tag" "" "$("${CRANE_BIN}" ls "${REG}" 2>/dev/null)"
  "${CRANE_BIN}" validate --remote "${REG}@${LAYDIG}" > /dev/null 2>&1 && ok "layout: the pushed chart validates" || fail "layout: the pushed chart does not validate"
  "${CRANE_BIN}" tag "${REG}@${LAYDIG}" 0.2.0 > /dev/null 2>&1 || true
  eq "layout: crane tag sets the version tag afterwards" "0.2.0" "$("${CRANE_BIN}" ls "${REG}" 2>/dev/null)"
  mkdir -p "${W}/pulled"
  ( cd "${W}" && "${HELM_BIN}" pull "oci://127.0.0.1:${REGPORT}/charts/myriarch" --version 0.2.0 --plain-http --destination "${W}/pulled" > /dev/null 2>&1 ) \
    && ok "layout: helm pulls the chart by the version tag" || fail "layout: helm cannot pull the chart by the version tag"
else
  fail "layout: the throwaway registry did not start"
fi
kill "${REGPID}" 2>/dev/null || true
wait "${REGPID}" 2>/dev/null || true

# ==== 2. push-chart.sh and verify-chart.sh ===================================================================================
S="${W}/stubs"; SB="${W}/stubbin"; HOME_DIR="${W}/home"
mkdir -p "${S}/calls" "${SB}" "${HOME_DIR}/.docker"
printf '{"auths":{"ghcr.io":{"auth":"eDp5"}}}\n' > "${HOME_DIR}/.docker/config.json"
cp "${ROOT}"/.cache/bin/.stamp-helm-* "${SB}/"
cat > "${SB}/helm" <<STUB
#!/usr/bin/env bash
set -u
d="\${STUB_DIR:?}"
{ printf '%s\n' "\$@"; echo '----'; } >> "\${d}/calls/helm.log"
exec "${HELM_BIN}" "\$@"
STUB
cat > "${SB}/crane" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = version ]; then echo v0.22.1; exit 0; fi
{ printf '%s\n' "$@"; echo '----'; } >> "${d}/calls/crane.log"
case "$1" in
  push) if [ -f "${d}/crane.push.fail" ]; then echo "Error: push failed" >&2; exit 1; fi ;;
  ls) if [ -f "${d}/crane.ls.err" ]; then cat "${d}/crane.ls.err" >&2; exit 1; fi; cat "${d}/tags" 2>/dev/null ;;
  digest)
    if [[ "$2" == *:* ]] && [[ "$2" != *@* ]] && [ -f "${d}/crane.digest.tag.override" ]; then cat "${d}/crane.digest.tag.override"
    elif [ -f "${d}/crane.digest.override" ]; then cat "${d}/crane.digest.override"
    else cat "${d}/chart.digest"; fi ;;
  validate) [ ! -f "${d}/crane.validate.fail" ] || exit 1 ;;
  manifest) if [[ "$2" == *:artifacthub.io ]]; then cat "${d}/ah-manifest.json"; else cat "${d}/manifest.json"; fi ;;
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
LAY="${W}/lay/chart"
CD="${LAYDIG}"
REF_WF="myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}"
reset() { rm -rf "${S}/calls"; mkdir -p "${S}/calls"; find "${S}" -maxdepth 1 -type f -exec rm -f {} +; printf '%s' "${CD}" > "${S}/chart.digest"; cp "${LAY}/blobs/sha256/${CD#sha256:}" "${S}/manifest.json"; }
run_priv() { ( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}" GITHUB_WORKFLOW_REF="${REF_WF}" IMAGE_DIGEST="${D1}" GH_TOKEN=t; unset DOCKER_CONFIG; "$@" ); }
pc() { run_priv "${REL}/push-chart.sh" --layout "${LAY}" --digest "${CD}" --tag "${TAG}" --commit "${C40}"; }
calls() { cat "${S}/calls/$1.log" 2>/dev/null || true; }
count() { calls "$1" | tr '\n' ' ' | sed 's/ ---- /\n/g' | grep -Ec -- "$2" || true; }

reset
expect_ok "push-chart: the chart" pc
case "${OUT}" in *"chart-digest=${CD}"*) ok "push-chart: prints chart-digest" ;; *) fail "push-chart: no chart-digest line: ${OUT}" ;; esac
eq "push-chart: the layout is pushed by digest" "1" "$(count crane "^push ${LAY} ghcr.io/myriarch/charts/myriarch@${CD}\$")"
eq "push-chart: no tag is set and no tag list is read" "0" "$(count crane '^(tag|ls)')"
eq "push-chart: no push of a reference without a digest" "0" "$(count crane '^push .* ghcr.io/myriarch/charts/myriarch(:[^@]*)?$')"
eq "push-chart: helm is not called" "0" "$(count helm '.')"
eq "push-chart: keyless signature of the chart digest" "1" "$(count cosign "^sign --yes ghcr.io/myriarch/charts/myriarch@${CD}")"
eq "push-chart: verify with the exact identity" "1" "$(count cosign "^verify --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG} --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/${TAG} --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push ghcr.io/myriarch/charts/myriarch@${CD}")"
eq "push-chart: the digest is read back and the image is validated" "1" "$(count crane "^validate --remote ghcr.io/myriarch/charts/myriarch@${CD}")"
eq "push-chart: no recursive signature" "0" "$(count cosign "recursive")"

reset; printf '0.2.0\n' > "${S}/tags"
expect_ok "push-chart: a re-run for a tagged chart pushes the same digest again" pc
reset; touch "${S}/crane.push.fail"
expect_fail "push-chart: crane push fails" "push_failed" pc
eq "push-chart: nothing is signed after a failed push" "0" "$(count cosign '^sign')"
reset; printf '%s' "sha256:$(printf 'e5%.0s' $(seq 32))" > "${S}/crane.digest.override"
expect_fail "push-chart: the registry reports another digest" "chart_digest_mismatch" pc
eq "push-chart: nothing is signed after a digest mismatch" "0" "$(count cosign '^sign')"
reset; printf 'x' >> "${S}/manifest.json"
expect_fail "push-chart: the registry serves another manifest" "chart_digest_mismatch.*hashes to another digest" pc
reset; touch "${S}/crane.validate.fail"
expect_fail "push-chart: an incomplete chart in the registry" "chart_digest_mismatch" pc
reset; echo 'sign' > "${S}/cosign.fail"
expect_fail "push-chart: the signature fails" "sign_failed" pc
reset; echo '^verify' > "${S}/cosign.fail"
expect_fail "push-chart: the verification of the signature fails" "signature_invalid" pc
reset
expect_fail "push-chart: outside the release workflow" "workflow_ref" run_priv env GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/ci.yml@refs/heads/main "${REL}/push-chart.sh" --layout "${LAY}" --digest "${CD}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a workflow at another tag" "workflow_ref" run_priv env GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.2.1 "${REL}/push-chart.sh" --layout "${LAY}" --digest "${CD}" --tag "${TAG}" --commit "${C40}"
eq "push-chart: nothing was pushed by the refused runs" "0" "$(count crane '^push')"
expect_fail "push-chart: a digest that is not the manifest of the layout" "layout_invalid.*exactly the image manifest" run_priv "${REL}/push-chart.sh" --layout "${LAY}" --digest "${D2}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a malformed digest" "digest_invalid" run_priv "${REL}/push-chart.sh" --layout "${LAY}" --digest abc --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a layout that is no directory" "layout_invalid" run_priv "${REL}/push-chart.sh" --layout "${W}/nothing" --digest "${CD}" --tag "${TAG}" --commit "${C40}"
cp -R "${LAY}" "${W}/lay-bad"
lbl="$(python3 -I -c 'import json,sys; m=json.load(open(sys.argv[1])); print([l["digest"] for l in m["layers"]][0][7:])' "${LAY}/blobs/sha256/${CD#sha256:}")"
printf 'tampered' >> "${W}/lay-bad/blobs/sha256/${lbl}"
expect_fail "push-chart: a chart layer that does not match its digest" "layout_invalid.*does not match" run_priv "${REL}/push-chart.sh" --layout "${W}/lay-bad" --digest "${CD}" --tag "${TAG}" --commit "${C40}"
expect_fail "push-chart: a malformed tag" "tag_invalid" run_priv "${REL}/push-chart.sh" --layout "${LAY}" --digest "${CD}" --tag 0.2.0 --commit "${C40}"
expect_fail "push-chart: a malformed commit" "commit_invalid" run_priv "${REL}/push-chart.sh" --layout "${LAY}" --digest "${CD}" --tag "${TAG}" --commit abc
expect_fail "push-chart: no login" "push_failed.*registry-login" run_priv env HOME="${W}/nohome" "${REL}/push-chart.sh" --layout "${LAY}" --digest "${CD}" --tag "${TAG}" --commit "${C40}"
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
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf 'sha256-%s\n' "${MDIGEST#sha256:}" > "${S}/tags"
expect_ok "verify-chart: the version tag does not exist before finalize" vc
eq "verify-chart: the chart is read by its digest" "1" "$(count crane "^digest ghcr.io/myriarch/charts/myriarch@${MDIGEST}\$")"
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '%s' "sha256:$(printf 'f6%.0s' $(seq 32))" > "${S}/crane.digest.tag.override"
expect_fail "verify-chart: the version tag points to another digest" "chart_digest_mismatch.*never moved" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '%s' "sha256:$(printf 'f6%.0s' $(seq 32))" > "${S}/crane.digest.override"
expect_fail "verify-chart: the registry reports another digest" "chart_digest_mismatch" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '0.2.0\nlatest\n' > "${S}/tags"
expect_fail "verify-chart: a tag latest" "chart_tag_unexpected.*latest" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; printf '0.2.0\nnightly\n' > "${S}/tags"
expect_fail "verify-chart: an unexpected tag" "chart_tag_unexpected.*nightly" vc
AHC=application/vnd.cncf.artifacthub.config.v1+yaml AHL=application/vnd.cncf.artifacthub.repository-metadata.layer.v1.yaml
mk_ah() { # config-media-type layer-media-type...
  local c="$1" layers="" m; shift
  for m in "$@"; do layers="${layers:+${layers},}{\"mediaType\":\"${m}\",\"digest\":\"${D2}\",\"size\":1}"; done
  printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"%s","digest":"%s","size":0},"layers":[%s]}' "${c}" "${D2}" "${layers}" > "${S}/ah-manifest.json"
  printf '0.2.0\nsha256-%s\nartifacthub.io\n' "${MDIGEST#sha256:}" > "${S}/tags"
}
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah "${AHC}" "${AHL}"
expect_ok "verify-chart: the tag artifacthub.io with the Artifact Hub media types" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah application/vnd.cncf.helm.config.v1+json "${AHL}"
expect_fail "verify-chart: artifacthub.io with another config media type" "chart_tag_unexpected.*artifacthub.io" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah "${AHC}" application/vnd.cncf.helm.chart.content.v1.tar+gzip
expect_fail "verify-chart: artifacthub.io with the chart layer" "chart_tag_unexpected.*artifacthub.io" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah "${AHC}" "${AHL}" "${AHL}"
expect_fail "verify-chart: artifacthub.io with two layers" "chart_tag_unexpected.*artifacthub.io" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah "${AHC}"
expect_fail "verify-chart: artifacthub.io without a layer" "chart_tag_unexpected.*artifacthub.io" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah "${AHC}" "${AHL}"; printf 'not json' > "${S}/ah-manifest.json"
expect_fail "verify-chart: artifacthub.io that is not a manifest" "chart_tag_unexpected.*artifacthub.io" vc
reset; mk_registry application/vnd.cncf.helm.chart.content.v1.tar+gzip; mk_ah "${AHC}" "${AHL}"; printf '0.2.0\nartifacthub.io.x\nartifacthub\n' > "${S}/tags"
expect_fail "verify-chart: a tag that only looks like artifacthub.io" "chart_tag_unexpected" vc
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
