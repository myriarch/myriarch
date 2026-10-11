#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the smoke tests of a published release (ci/release/smoke.sh, ci/release/smoke-windows.ps1; ADR-0035).
#
# smoke.sh runs in a temporary root that holds a copy of the script, ci/versions.env, the real hack/kind files and doubles of
# everything that belongs to other files of the release tooling: ci/release/verify-dist.sh, ci/release/verify-blob.sh,
# ci/release/identity.env and deploy/release/assets.json. cosign, crane, gh, kind, kubectl and helm (TOOLS_BIN) and curl and
# docker (PATH) are stubs that log their arguments (one per line, a "----" line between calls) and answer from state files.
# Nothing here talks to the network, starts a container or listens on a port. smoke-windows.ps1 gets a parse check and a test of its
# parameter handling (needs pwsh; the Windows jobs of the release run the rest).
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
cd "${ROOT}" || exit 1
have python3 || die "python3 is required"

VERSIONS="${ROOT}/ci/versions.env"
val() { grep -E "^$1=" "${VERSIONS}" | head -n 1 | cut -d= -f2-; }
COSIGN_V="$(val COSIGN_VERSION)"
CRANE_V="$(val CRANE_VERSION)"
GH_V="$(val GH_VERSION)"
OTEL_V="$(val OTELCOL_VERSION)"

TAG=v9.9.9
VER=9.9.9
COMMIT=0123456789abcdef0123456789abcdef01234567
SIDX="sha256:$(printf '1%.0s' $(seq 64))"
UIDX="sha256:$(printf '2%.0s' $(seq 64))"
SAMD="sha256:$(printf '3%.0s' $(seq 64))"
GH_TOKEN_VALUE="stub-token-9f8e7d6c5b4a"
BOOTSTRAP_VALUE="stub-bootstrap-1a2b3c4d5e6f"
SERVER_REPO=ghcr.io/myriarch/myriarch
UPDATER_REPO=ghcr.io/myriarch/myriarch-updater
ISSUER=https://token.actions.githubusercontent.com
WORKFLOW=.github/workflows/release.yml
IDENTITY="https://github.com/myriarch/myriarch/${WORKFLOW}@refs/tags/${TAG}"
SIGNER="myriarch/myriarch/${WORKFLOW}@refs/tags/${TAG}"

assert() { local name="$1"; shift; if "$@"; then ok "${name}"; else fail "${name}"; fi; }
# rx TEXT: TEXT as a literal in an extended regular expression
rx() { printf '%s' "$1" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g'; }
TMP_BASE="$(cd "${TEST_TMP}" && pwd)"

# ---- the temporary root ------------------------------------------------------------------------------------------------
stub_head() { # tool: the common start of every stub
  sed -e "s/__TOOL__/$1/g" <<'EOF'
#!/bin/sh
{ for a in "$@"; do printf '%s\n' "$a"; done; printf -- '----\n'; } >> "${STUB_CALLS}/__TOOL__.log"
if [ -n "${DOCKER_CONFIG+x}" ]; then
  if [ -d "${DOCKER_CONFIG}" ] && [ -z "$(ls -A "${DOCKER_CONFIG}")" ]; then printf 'DOCKER_CONFIG=empty\n'; else printf 'DOCKER_CONFIG=other\n'; fi
else
  printf 'DOCKER_CONFIG=unset\n'
fi >> "${STUB_CALLS}/__TOOL__.env"
joined="$*"
if [ -f "${STUB_STATE}/__TOOL__.fail" ]; then
  while IFS= read -r pat; do
    [ -n "${pat}" ] || continue
    case "${joined}" in *"${pat}"*) echo "stub: injected failure" >&2; exit 1 ;; esac
  done < "${STUB_STATE}/__TOOL__.fail"
fi
EOF
}
mkstub() { # path tool body
  { stub_head "$2"; printf '%s\n' "$3"; } > "$1"
  chmod +x "$1"
}

host_arch() { case "$(uname -m)" in x86_64 | amd64) echo amd64 ;; *) echo arm64 ;; esac; }

new_root() { # name: sets R and exports what the stubs read
  local name="$1"
  R="${TMP_BASE}/${name}"
  export STUB_CALLS="${R}/calls" STUB_STATE="${R}/state" STUB_SERVE="${R}/serve"
  export STUB_COSIGN_VERSION="${COSIGN_V}" STUB_CRANE_VERSION="${CRANE_V}" STUB_GH_VERSION="${GH_V#v}" STUB_IMAGE_VERSION="${VER}"
  STUB_BOOTSTRAP_B64="$(printf '%s' "${BOOTSTRAP_VALUE}" | base64 | tr -d '\n')"
  export STUB_BOOTSTRAP_B64
  mkdir -p "${R}/ci/release" "${R}/deploy/release" "${R}/deploy/helm/myriarch" "${R}/hack/kind/manifests" "${R}/bin" "${R}/path" \
    "${STUB_CALLS}" "${STUB_STATE}/crane-digest" "${STUB_SERVE}"
  cp "${ROOT}/ci/release/smoke.sh" "${R}/ci/release/smoke.sh"
  cp "${ROOT}/ci/lib.sh" "${R}/ci/lib.sh"
  cp "${VERSIONS}" "${R}/ci/versions.env"
  cp "${ROOT}/hack/kind/cluster.yaml" "${ROOT}/hack/kind/versions.env" "${R}/hack/kind/"
  cp "${ROOT}/hack/kind/manifests/postgres.yaml" "${R}/hack/kind/manifests/"
  printf 'apiVersion: v2\nname: myriarch\nversion: %s\nappVersion: "%s"\n' "${VER}" "${VER}" > "${R}/deploy/helm/myriarch/Chart.yaml"

  # doubles of the files of the scripts lane
  cat > "${R}/ci/release/identity.env" <<'EOF'
RELEASE_REPOSITORY=myriarch/myriarch
RELEASE_WORKFLOW=.github/workflows/release.yml
RELEASE_OIDC_ISSUER=https://token.actions.githubusercontent.com
RELEASE_TRIGGER=push
RELEASE_IMAGE_SERVER=ghcr.io/myriarch/myriarch
RELEASE_IMAGE_UPDATER=ghcr.io/myriarch/myriarch-updater
EOF
  mkstub "${R}/ci/release/verify-dist.sh" verify-dist 'exit 0'
  # the double of verify-blob.sh fails like a real signature check when the blob is not the signed one
  mkstub "${R}/ci/release/verify-blob.sh" verify-blob '
blob=""; for a in "$@"; do blob="$a"; done
if [ ! -f "${STUB_STATE}/verify-blob.broken" ] && [ -f "${STUB_STATE}/good-blob" ]; then
  cmp -s "${blob}" "${STUB_STATE}/good-blob" || { echo "stub: invalid signature" >&2; exit 1; }
fi
exit 0'
  python3 -I - "${R}/deploy/release/assets.json" <<'PY'
import json, sys

v = "{version}"
archives = (
    ["myriarchd_%s_%s.tar.gz" % (v, p) for p in ("darwin_arm64", "linux_amd64", "linux_arm64")]
    + ["myriarch-updater_%s_%s.tar.gz" % (v, p) for p in ("linux_amd64", "linux_arm64")]
    + ["myriarch-updater_%s_windows_%s.zip" % (v, a) for a in ("amd64", "arm64")]
    + ["myrctl_%s_%s.tar.gz" % (v, p) for p in ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64")]
    + ["myrctl_%s_windows_%s.zip" % (v, a) for a in ("amd64", "arm64")]
    + ["myriarch-sim_%s_%s.tar.gz" % (v, p) for p in ("darwin_arm64", "linux_amd64", "linux_arm64")]
)
binaries = ["myriarch-updater_%s_linux_%s" % (v, a) for a in ("amd64", "arm64")] + [
    "myriarch-updater_%s_windows_%s.exe" % (v, a) for a in ("amd64", "arm64")
]
assets = (
    [(n, "archive") for n in archives]
    + [(n, "binary") for n in binaries]
    + [(n + ".sbom.json", "sbom") for n in archives]
    + [("myriarch-webui_%s.sbom.json" % v, "sbom")]
    + [("LICENSE", "license"), ("THIRD_PARTY_NOTICES", "license"), ("checksums.txt", "checksums"),
       ("checksums.txt.sigstore.json", "signature"), ("myriarch_%s_provenance.intoto.jsonl" % v, "provenance")]
)
assert len(assets) == 42, len(assets)
json.dump({"schema": 1, "assets": [{"name": n, "kind": k} for n, k in sorted(assets)]}, open(sys.argv[1], "w"), indent=2)
PY

  # tools (TOOLS_BIN)
  mkstub "${R}/bin/cosign" cosign 'case "$1" in
  version) printf "GitVersion:    %s\n" "${STUB_COSIGN_VERSION}" ;;
  verify)
    # like cosign v3: one JSON entry per verified bundle; cosign.type picks what the registry holds (a signature by default)
    for ref in "$@"; do :; done
    type=https://sigstore.dev/cosign/sign/v1
    [ ! -f "${STUB_STATE}/cosign.type" ] || type="$(cat "${STUB_STATE}/cosign.type")"
    printf "[{\"critical\":{\"identity\":{\"docker-reference\":\"%s\"},\"image\":{\"docker-manifest-digest\":\"%s\"},\"type\":\"%s\"},\"optional\":{}}]\n" "${ref%@*}" "${ref#*@}" "${type}" ;;
esac
exit 0'
  mkstub "${R}/bin/crane" crane 'case "$1" in
  version) echo "${STUB_CRANE_VERSION}" ;;
  digest) f="${STUB_STATE}/crane-digest/$(printf "%s" "$2" | tr "/:" "__")"; if [ -f "$f" ]; then cat "$f"; else echo "stub: no such image" >&2; exit 1; fi ;;
esac
exit 0'
  mkstub "${R}/bin/gh" gh 'case "$1" in
  --version) printf "gh version %s (2026-01-01)\nhttps://github.com/cli/cli/releases/tag/v%s\n" "${STUB_GH_VERSION}" "${STUB_GH_VERSION}" ;;
esac
exit 0'
  mkstub "${R}/bin/kind" kind 'case "$1 $2" in
  "create cluster") kc=""; while [ "$#" -gt 0 ]; do [ "$1" = --kubeconfig ] && kc="$2"; shift; done; [ -z "${kc}" ] || : > "${kc}" ;;
esac
exit 0'
  mkstub "${R}/bin/kubectl" kubectl 'case "$*" in
  *"apply -f -"*) n="$(ls "${STUB_CALLS}" | grep -c "^kubectl-apply-")"; cat > "${STUB_CALLS}/kubectl-apply-$((n + 1)).yaml" ;;
  *"get pods"*) cat "${STUB_STATE}/pods.json" ;;
  *"get secret"*jsonpath*) printf "%s" "${STUB_BOOTSTRAP_B64}" ;;
  *port-forward*) exec sleep 300 ;;
esac
exit 0'
  mkstub "${R}/bin/helm" helm 'case "$1" in
  install) cat "${STUB_STATE}/notes.txt" ;;
esac
exit 0'
  cat > "${STUB_STATE}/notes.txt" <<'EOF'
myriarchd 9.9.9 was installed as release "myriarch".

Sign in (bootstrap admin "admin"):
  The chart generated a one-time token. Read it (the value is not printed here):
    kubectl -n myriarch get secret myriarch-bootstrap -o jsonpath='{.data.token}' | base64 -d
EOF

  # curl and docker (PATH)
  mkstub "${R}/path/curl" curl 'url=""; out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -d) [ "$2" != "@-" ] || cat > "${STUB_CALLS}/curl-stdin.txt"; shift 2 ;;
    -w|-m|-c|-H|-u|--proto|--proto-redir|--retry|--connect-timeout|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "${url}" in
  http://127.0.0.1:*/api/v1/auth/bootstrap-login) printf "%s" "${STUB_LOGIN_CODE:-200}" ;;
  http://127.0.0.1:*) printf "%s" "${STUB_HEALTH_CODE:-200}" ;;
  https://*) f="${STUB_SERVE}/${url##*/}"; [ -f "${f}" ] || exit 22; cp "${f}" "${out}" ;;
  *) exit 1 ;;
esac
exit 0'
  mkstub "${R}/path/docker" docker 'case "$1" in
  pull) exit 0 ;;
  image) case "$4" in *RepoDigests*) cat "${STUB_STATE}/docker-repodigests" ;; *) cat "${STUB_STATE}/docker-meta" ;; esac ;;
  run)
    case "$*" in
      *"--entrypoint /bin/sh"*) [ ! -f "${STUB_STATE}/docker-has-shell" ] || exit 0; echo "exec: /bin/sh: not found" >&2; exit 127 ;;
      *) printf "%s (abc1234, 2026-01-01T00:00:00Z, go1.26, linux/amd64)\n" "${STUB_IMAGE_VERSION}" ;;
    esac ;;
esac
exit 0'
  printf '65532:65532|%s|linux\n' "$(host_arch)" > "${STUB_STATE}/docker-meta"
  printf '["%s@%s","%s@%s"]\n' "${SERVER_REPO}" "${SIDX}" "${UPDATER_REPO}" "${UIDX}" > "${STUB_STATE}/docker-repodigests"
}

# fill_release VERSION: every asset of assets.json with the tags of the final images (VERSION must be X.Y.Z)
fill_release() {
  local v="${1:-${VER}}" n
  python3 -I - "${R}/deploy/release/assets.json" "${STUB_SERVE}" "${v}" <<'PY'
import json, os, sys

for a in json.load(open(sys.argv[1]))["assets"]:
    name = a["name"].replace("{version}", sys.argv[3])
    open(os.path.join(sys.argv[2], name), "w").write("content of %s\n" % name)
PY
  cp "${STUB_SERVE}/checksums.txt" "${STUB_STATE}/good-blob"
  for n in ${v} ${v%.*}; do
    printf '%s' "${SIDX}" > "${STUB_STATE}/crane-digest/ghcr.io_myriarch_myriarch_${n}"
    printf '%s' "${UIDX}" > "${STUB_STATE}/crane-digest/ghcr.io_myriarch_myriarch-updater_${n}"
  done
}

# run_smoke ARGS...: smoke.sh of the current root with the stubs, the digests and the token of the job
run_smoke() {
  env PATH="${R}/path:${PATH}" TOOLS_BIN="${R}/bin" GH_TOKEN="${GH_TOKEN_VALUE}" SMOKE_RETRIES=1 SMOKE_RETRY_DELAY=0 SMOKE_WAIT_SECONDS=0 \
    SERVER_INDEX_DIGEST="${SIDX}" UPDATER_INDEX_DIGEST="${UIDX}" bash "${R}/ci/release/smoke.sh" "$@"
}

# calls TOOL: one line per call of the stub, the arguments separated by spaces
calls() { awk '/^----$/ { print line; line = ""; next } { line = (line == "" ? $0 : line " " $0) }' "${STUB_CALLS}/$1.log" 2>/dev/null; }
call_is() { calls "$1" | grep -qxF -- "$2"; }
call_like() { calls "$1" | grep -Eq -- "$2"; }
call_count() { local n; n="$(calls "$1" | grep -Ec -- "$2")"; printf '%s' "${n}"; }
count_is() { [ "$(call_count "$1" "$2")" = "$3" ]; }
# anonymous TOOL N: N calls of the stub ran with an empty Docker configuration, and no call ran with another one
anonymous() { [ "$(grep -c '^DOCKER_CONFIG=empty$' "${STUB_CALLS}/$1.env")" = "$2" ] && ! grep -q '^DOCKER_CONFIG=other$' "${STUB_CALLS}/$1.env"; }
no_secret_in_logs() { ! grep -rqF --exclude=curl-stdin.txt -e "${GH_TOKEN_VALUE}" -e "${BOOTSTRAP_VALUE}" -e "myre_" "${STUB_CALLS}" 2>/dev/null; }

# ---- static checks of the script -----------------------------------------------------------------------------------------
echo "smoke.sh: static"
SMOKE="${ROOT}/ci/release/smoke.sh"
assert "smoke.sh is executable" test -x "${SMOKE}"
assert "smoke.sh has the SPDX header" bash -c "head -n 3 '${SMOKE}' | grep -q 'SPDX-License-Identifier: Apache-2.0'"
assert "smoke.sh never writes to a registry or to a release" bash -c "! grep -E 'crane (push|copy|tag)|release (create|edit|delete|upload)|gh api -X|docker (login|push)|auth login' <(grep -v '^[[:space:]]*#' '${SMOKE}')"
assert "smoke.sh has no insecure or hash shortcut for the installer" bash -c "! grep -E -- '--insecure|--updater-sha256|--verify-signatures|--signing-key' <(grep -v '^[[:space:]]*#' '${SMOKE}')"
assert "smoke.sh takes no token from an argument" bash -c "! grep -E -- '--token[ \"=]|--password' <(grep -v '^[[:space:]]*#' '${SMOKE}')"
assert "smoke.sh looks no tool up in PATH and does not change PATH" bash -c "! grep -E '(command -v|have|which|type) +(cosign|crane|gh|kind|kubectl|helm)|PATH=' <(grep -v '^[[:space:]]*#' '${SMOKE}')"
expect_ok "--help prints the usage" bash "${SMOKE}" --help
expect_fail "no subcommand is a usage error" 'usage:' bash "${SMOKE}"
expect_fail "an unknown subcommand is a usage error" 'usage:' bash "${SMOKE}" frobnicate

# ---- assets ----------------------------------------------------------------------------------------------------------------
echo "smoke.sh assets"
new_root assets-ok
fill_release
expect_ok "assets: a published release passes" run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
URLS="$(grep -h '^https://' "${STUB_CALLS}/curl.log" 2>/dev/null | LC_ALL=C sort)"
WANT_URLS="$(python3 -I -c 'import json, sys; [print("https://github.com/myriarch/myriarch/releases/download/v%s/%s" % (sys.argv[2], a["name"].replace("{version}", sys.argv[2]))) for a in json.load(open(sys.argv[1]))["assets"]]' "${R}/deploy/release/assets.json" "${VER}" | LC_ALL=C sort)"
assert "assets: all 42 files are downloaded from the release of the tag" test "$(printf '%s\n' "${URLS}" | wc -l | tr -d ' ')" = 42 -a "${URLS}" = "${WANT_URLS}"
assert "assets: every download is https only with TLS 1.2 or newer" bash -c "
  [ \"\$(grep -c '^https://' '${STUB_CALLS}/curl.log')\" = 42 ] && ! grep -q '^http://' '${STUB_CALLS}/curl.log' &&
  [ \"\$(grep -c -x -e '=https' '${STUB_CALLS}/curl.log')\" = 84 ] && [ \"\$(grep -c -x -e '--tlsv1.2' '${STUB_CALLS}/curl.log')\" = 42 ]"
assert "assets: the downloads are anonymous (no header, no user, no netrc)" bash -c "! grep -E -x -e '-H|--header|-u|--user|-n|--netrc|-K|--config' '${STUB_CALLS}/curl.log'"
assert "assets: the first argument of curl disables curlrc" test "$(head -n 1 "${STUB_CALLS}/curl.log")" = -q
assert "assets: verify-dist.sh runs over the download with --stage final" call_like verify-dist "^--dir [^ ]*/release --version ${VER} --stage final\$"
SIG="--bundle [^ ]*/checksums.txt.sigstore.json --repository myriarch/myriarch --workflow $(rx "${WORKFLOW}") --tag $(rx "${TAG}")"
assert "assets: verify-blob runs exact, pattern, then the negative control (a tampered copy)" test "$(calls verify-blob | wc -l | tr -d ' ')" = 3
assert "assets: verify-blob exact gets the commit" bash -c "$(declare -f calls call_like); STUB_CALLS='${STUB_CALLS}'; calls verify-blob | sed -n 1p | grep -Eq -- '^--mode exact ${SIG} --commit ${COMMIT} [^ ]*/release/checksums\.txt\$'"
assert "assets: verify-blob pattern gets no commit" bash -c "$(declare -f calls call_like); STUB_CALLS='${STUB_CALLS}'; calls verify-blob | sed -n 2p | grep -Eq -- '^--mode pattern ${SIG} [^ ]*/release/checksums\.txt\$'"
assert "assets: the negative control verifies a copy" bash -c "$(declare -f calls call_like); STUB_CALLS='${STUB_CALLS}'; calls verify-blob | sed -n 3p | grep -Eq -- '^--mode exact .* [^ ]*/tampered/checksums\.txt\$'"
assert "assets: gh release verify" call_is gh "release verify ${TAG} --repo myriarch/myriarch"
assert "assets: gh release verify-asset checksums.txt" call_like gh "^release verify-asset ${TAG} [^ ]*/release/checksums\.txt --repo myriarch/myriarch\$"
assert "assets: gh release verify-asset of an archive" call_like gh "^release verify-asset ${TAG} [^ ]*/release/myrctl_${VER}_linux_amd64\.tar\.gz --repo myriarch/myriarch\$"
EXTRA="--source-ref refs/tags/${TAG} --source-digest ${COMMIT} --deny-self-hosted-runners"
PROV="[^ ]*/myriarch_${VER}_provenance\.intoto\.jsonl"
assert "assets: provenance of the updater with the bundle asset" call_like gh "^attestation verify [^ ]*/release/myriarch-updater_${VER}_linux_amd64 --bundle ${PROV} --repo myriarch/myriarch --cert-identity $(rx "${IDENTITY}") ${EXTRA}\$"
assert "assets: provenance of an archive with the bundle asset" call_like gh "^attestation verify [^ ]*/release/myrctl_${VER}_linux_amd64\.tar\.gz --bundle ${PROV} --repo myriarch/myriarch --cert-identity $(rx "${IDENTITY}") ${EXTRA}\$"
assert "assets: provenance through the attestation API with --signer-workflow" call_like gh "^attestation verify [^ ]*/release/myriarch-updater_${VER}_linux_amd64 --repo myriarch/myriarch --signer-workflow $(rx "${SIGNER}") ${EXTRA}\$"
assert "assets: the image tags 9.9.9 and 9.9 of both images are resolved" count_is crane '^digest ghcr\.io/myriarch/myriarch(-updater)?:9\.9(\.9)?$' 4
assert "assets: the registry reads of crane are anonymous" anonymous crane 4
COSIGN_LINE="verify --certificate-identity ${IDENTITY} --certificate-oidc-issuer ${ISSUER} --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/${TAG} --certificate-github-workflow-sha ${COMMIT} --certificate-github-workflow-trigger push"
assert "assets: cosign verify with the exact flags on the server index" call_is cosign "${COSIGN_LINE} ${SERVER_REPO}@${SIDX}"
assert "assets: cosign verify with the exact flags on the updater index" call_is cosign "${COSIGN_LINE} ${UPDATER_REPO}@${UIDX}"
assert "assets: cosign runs twice, anonymously" bash -c "$(declare -f calls call_count anonymous); STUB_CALLS='${STUB_CALLS}'; [ \"\$(calls cosign | grep -c '^verify ')\" = 2 ] && grep -c '^DOCKER_CONFIG=empty\$' '${STUB_CALLS}/cosign.env' | grep -qx 2"
assert "assets: gh attestation verify oci:// (server index)" call_is gh "attestation verify oci://${SERVER_REPO}@${SIDX} --bundle-from-oci --repo myriarch/myriarch --signer-workflow ${SIGNER}"
assert "assets: gh attestation verify oci:// (updater index)" call_is gh "attestation verify oci://${UPDATER_REPO}@${UIDX} --bundle-from-oci --repo myriarch/myriarch --signer-workflow ${SIGNER}"
assert "assets: the two registry reads of gh are anonymous" test "$(grep -c '^DOCKER_CONFIG=empty$' "${STUB_CALLS}/gh.env")" = 2
assert "assets: no token in any call or log" no_secret_in_logs

new_root assets-rc
fill_release 9.9.9-rc.1
rm -f "${STUB_STATE}"/crane-digest/*
printf '%s' "${SIDX}" > "${STUB_STATE}/crane-digest/ghcr.io_myriarch_myriarch_9.9.9-rc.1"
printf '%s' "${UIDX}" > "${STUB_STATE}/crane-digest/ghcr.io_myriarch_myriarch-updater_9.9.9-rc.1"
expect_ok "assets: an rc tag expects the tag 9.9.9-rc.1 only" run_smoke assets --tag v9.9.9-rc.1 --commit "${COMMIT}"
assert "assets: an rc resolves one tag per image" count_is crane '^digest ' 2

new_root assets-negative
fill_release
: > "${STUB_STATE}/verify-blob.broken"
expect_fail "assets: a verifier that accepts a tampered checksums.txt fails the run" '\[negative_control\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-dist
fill_release
mkstub "${R}/ci/release/verify-dist.sh" verify-dist 'echo "[verify-dist] ERROR [asset_missing] x" >&2; exit 1'
expect_fail "assets: a wrong file set fails" '\[verify_dist\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-sig
fill_release
printf -- '--mode exact\n' > "${STUB_STATE}/verify-blob.fail"
expect_fail "assets: a failing exact verification" '\[signature_invalid\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-cosign
fill_release
printf '%s\n' "${UPDATER_REPO}@${UIDX}" > "${STUB_STATE}/cosign.fail"
expect_fail "assets: a failing image signature" '\[signature_invalid\] cosign verify' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-attestation-only
fill_release
printf 'https://spdx.dev/Document\n' > "${STUB_STATE}/cosign.type"
expect_fail "assets: an attestation of the identity is not an image signature" '\[signature_invalid\] cosign verify.*no cosign signature' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-attest
fill_release
printf 'oci://%s\n' "${SERVER_REPO}" > "${STUB_STATE}/gh.fail"
expect_fail "assets: a failing image attestation" '\[attestation_invalid\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-provenance
fill_release
printf -- '--bundle\n' > "${STUB_STATE}/gh.fail"
expect_fail "assets: a failing build provenance" '\[provenance_invalid\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-release-attestation
fill_release
printf 'release verify v9.9.9\n' > "${STUB_STATE}/gh.fail"
expect_fail "assets: a release without GitHub's attestation" '\[release_attestation\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
printf 'release verify-asset\n' > "${STUB_STATE}/gh.fail"
expect_fail "assets: an asset without GitHub's attestation" '\[release_asset_attestation\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-tag
fill_release
printf '%s' "${UIDX}" > "${STUB_STATE}/crane-digest/ghcr.io_myriarch_myriarch_9.9"
expect_fail "assets: a tag that points at another digest" '\[image_tag_mismatch\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
rm -f "${STUB_STATE}/crane-digest/ghcr.io_myriarch_myriarch_9.9"
expect_fail "assets: a tag that cannot be read" '\[image_tag_mismatch\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-fail-download
fill_release
rm -f "${STUB_SERVE}/myrctl_${VER}_linux_arm64.tar.gz"
expect_fail "assets: a missing release file" '\[download_failed\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

new_root assets-inputs
fill_release
expect_fail "assets: a malformed tag" '\[input_invalid\]' run_smoke assets --tag v09.9.9 --commit "${COMMIT}"
expect_fail "assets: a malformed commit" '\[input_invalid\]' run_smoke assets --tag "${TAG}" --commit abc123
expect_fail "assets: a tag with a control character" '\[input_invalid\]' run_smoke assets --tag "$(printf 'v9.9.9\nx')" --commit "${COMMIT}"
expect_fail "assets: the commit is required" 'usage:' run_smoke assets --tag "${TAG}"
expect_fail "assets: an unknown option" 'usage:' run_smoke assets --tag "${TAG}" --commit "${COMMIT}" --insecure
IMAGE_TAGS="9.9.9,latest" expect_fail "assets: image tags other than the tag rule" '\[image_tags\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
IMAGE_TAGS="9.9.9,9.9" expect_ok "assets: image tags that equal the tag rule" run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
expect_fail "assets: no GH_TOKEN" '\[gh_token_missing\]' env -u GH_TOKEN PATH="${R}/path:${PATH}" TOOLS_BIN="${R}/bin" SERVER_INDEX_DIGEST="${SIDX}" UPDATER_INDEX_DIGEST="${UIDX}" bash "${R}/ci/release/smoke.sh" assets --tag "${TAG}" --commit "${COMMIT}"
expect_fail "assets: no digest of the server index" '\[input_invalid\]' env PATH="${R}/path:${PATH}" TOOLS_BIN="${R}/bin" GH_TOKEN=x SERVER_INDEX_DIGEST= UPDATER_INDEX_DIGEST="${UIDX}" bash "${R}/ci/release/smoke.sh" assets --tag "${TAG}" --commit "${COMMIT}"
STUB_COSIGN_VERSION=v3.0.0 expect_fail "assets: a cosign that is not the pinned version" '\[tool_version\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
STUB_GH_VERSION=2.0.0 expect_fail "assets: a gh that is not the pinned version" '\[tool_version\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
STUB_CRANE_VERSION=v0.1.0 expect_fail "assets: a crane that is not the pinned version" '\[tool_version\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"
rm -f "${R}/bin/crane"
expect_fail "assets: a missing crane" '\[tool_missing\]' run_smoke assets --tag "${TAG}" --commit "${COMMIT}"

# ---- image-run -------------------------------------------------------------------------------------------------------------
echo "smoke.sh image-run"
new_root image-run
expect_ok "image-run: the server image by digest" run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"
assert "image-run: pull by digest" call_is docker "pull --quiet ${SERVER_REPO}@${SIDX}"
assert "image-run: without --pull-tag the credentials of the host are used" bash -c "! grep -q '^DOCKER_CONFIG=empty\$' '${STUB_CALLS}/docker.env'"
R_IMG="${SERVER_REPO}@${SIDX}"
assert "image-run: myriarchd runs with --network none --read-only" call_is docker "run --rm --network none --read-only ${R_IMG} version"
assert "image-run: myrctl runs with --network none --read-only" call_is docker "run --rm --network none --read-only --entrypoint /usr/local/bin/myrctl ${R_IMG} version"
assert "image-run: myriarch-sim runs with --network none --read-only" call_is docker "run --rm --network none --read-only --entrypoint /usr/local/bin/myriarch-sim ${R_IMG} version"
assert "image-run: the shell probe runs without a network" call_is docker "run --rm --network none --entrypoint /bin/sh ${R_IMG} -c true"

new_root image-run-updater
expect_ok "image-run: the updater image" run_smoke image-run --image "${UPDATER_REPO}@${UIDX}" --kind updater --version "${VER}"
assert "image-run: the updater image runs only myriarch-updater version" count_is docker '^run --rm --network none --read-only ' 1

new_root image-run-tag
expect_ok "image-run: --pull-tag pulls REPO:TAG and checks the digest" run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}" --pull-tag "${VER}"
assert "image-run: the tag is pulled, not the digest" call_is docker "pull --quiet ${SERVER_REPO}:${VER}"
assert "image-run: with --pull-tag every docker call has an empty Docker configuration" bash -c "! grep -v '^DOCKER_CONFIG=empty\$' '${STUB_CALLS}/docker.env' | grep -q ."
printf '["%s@%s"]\n' "${SERVER_REPO}" "${UIDX}" > "${STUB_STATE}/docker-repodigests"
expect_fail "image-run: a tag that points at another digest" '\[image_digest\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}" --pull-tag "${VER}"

new_root image-run-shell
: > "${STUB_STATE}/docker-has-shell"
expect_fail "image-run: an image with /bin/sh" '\[image_has_shell\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"

new_root image-run-user
printf '1001:0|%s|linux\n' "$(host_arch)" > "${STUB_STATE}/docker-meta"
expect_fail "image-run: a user other than 65532:65532" '\[image_user\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"
printf '65532:65532|s390x|linux\n' > "${STUB_STATE}/docker-meta"
expect_fail "image-run: an architecture other than the host's" '\[image_arch\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"

new_root image-run-fail
STUB_IMAGE_VERSION=0.0.1 expect_fail "image-run: a binary of another version" '\[image_version\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"
printf 'run\n' > "${STUB_STATE}/docker.fail"
expect_fail "image-run: a binary that does not run" '\[image_version\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"
printf 'pull\n' > "${STUB_STATE}/docker.fail"
expect_fail "image-run: an image that cannot be pulled" '\[image_pull\]' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server --version "${VER}"
rm -f "${STUB_STATE}/docker.fail"
expect_fail "image-run: the updater repository is not a server image" '\[image_invalid\]' run_smoke image-run --image "${UPDATER_REPO}@${UIDX}" --kind server --version "${VER}"
expect_fail "image-run: an image by tag only" '\[input_invalid\]' run_smoke image-run --image "${SERVER_REPO}:${VER}" --kind server --version "${VER}"
expect_fail "image-run: an unknown kind" 'usage:' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind collector --version "${VER}"
expect_fail "image-run: a missing version" 'usage:' run_smoke image-run --image "${SERVER_REPO}@${SIDX}" --kind server

# ---- linux -----------------------------------------------------------------------------------------------------------------
echo "smoke.sh linux"
# make_linux_release ARCH: serves checksums.txt, its bundle, the updater archive (with a stub installer) and the bare updater
make_linux_release() {
  local arch="$1" stage="${R}/stage" bare archive
  bare="myriarch-updater_${VER}_linux_${arch}"
  archive="${bare}.tar.gz"
  cat > "${STUB_SERVE}/${bare}" <<'EOF'
#!/bin/sh
{ for a in "$@"; do printf '%s\n' "$a"; done; printf -- '----\n'; } >> "${STUB_CALLS}/updater.log"
[ ! -f "${STUB_STATE}/updater.fail" ] || exit 1
exit 0
EOF
  chmod +x "${STUB_SERVE}/${bare}"
  mkdir -p "${stage}/deploy/scripts" "${stage}/deploy/systemd"
  cat > "${stage}/deploy/scripts/install-linux.sh" <<'EOF'
#!/bin/bash
{ for a in "$@"; do printf '%s\n' "$a"; done; printf -- '----\n'; } >> "${STUB_CALLS}/installer.log"
[ ! -f "${STUB_STATE}/installer.fail" ] || exit 1
root=""; token=""
while [ "$#" -gt 0 ]; do
  case "$1" in --root) root="$2" ;; --token-file) token="$2" ;; esac
  shift
done
grep -Eq '^myre_[0-9a-f]{48}$' "${token}" || { echo "stub: the token file has no token" >&2; exit 1; }
mode="$(stat -c %a "${token}" 2>/dev/null || stat -f %Lp "${token}")"
[ "${mode}" = 600 ] || { echo "stub: the token file is readable by others" >&2; exit 1; }
mkdir -p "${root}/opt/myriarch/updater"
if [ -f "${STUB_STATE}/installer.other-updater" ]; then
  echo different > "${root}/opt/myriarch/updater/myriarch-updater"
else
  cp "${STUB_UPDATER_FILE}" "${root}/opt/myriarch/updater/myriarch-updater"
fi
chmod +x "${root}/opt/myriarch/updater/myriarch-updater"
EOF
  chmod +x "${stage}/deploy/scripts/install-linux.sh"
  : > "${stage}/deploy/systemd/otelcol-supervisor.service"
  tar -czf "${STUB_SERVE}/${archive}" -C "${stage}" deploy
  printf 'bundle\n' > "${STUB_SERVE}/checksums.txt.sigstore.json"
  write_sums "${arch}"
  export STUB_UPDATER_FILE="${STUB_SERVE}/${bare}"
}
write_sums() { # arch: checksums.txt for the current archive and bare updater
  local bare="myriarch-updater_${VER}_linux_$1"
  printf '%s  %s\n%s  %s\n' "$(sha256_of "${STUB_SERVE}/${bare}.tar.gz")" "${bare}.tar.gz" "$(sha256_of "${STUB_SERVE}/${bare}")" "${bare}" > "${STUB_SERVE}/checksums.txt"
  cp "${STUB_SERVE}/checksums.txt" "${STUB_STATE}/good-blob"
}

new_root linux-ok
make_linux_release amd64
expect_ok "linux: install from the verified archive and run both images" run_smoke linux --tag "${TAG}" --arch amd64
assert "linux: only the four files are downloaded" test "$(grep -c '^https://' "${STUB_CALLS}/curl.log")" = 4
assert "linux: verify-blob pattern and bound, no exact without a commit" bash -c "$(declare -f calls); STUB_CALLS='${STUB_CALLS}'; [ \"\$(calls verify-blob | wc -l | tr -d ' ')\" = 2 ] && calls verify-blob | sed -n 1p | grep -q '^--mode pattern ' && calls verify-blob | sed -n 2p | grep -q '^--mode bound '"
BASE="https://github.com/myriarch/myriarch/releases/download/${TAG}"
assert "linux: the installer of the archive gets exactly the keyless options" call_like installer "^--root [^ ]*/root --opamp-endpoint wss://fleet\.example\.com/v1/opamp --token-file [^ ]*/enrollment-token --site-type datacenter --site-id smoke-01 --collector-version $(rx "${OTEL_V}") --with-updater --updater-url $(rx "${BASE}")/myriarch-updater_$(rx "${VER}")_linux_amd64 --updater-checksums-url $(rx "${BASE}")/checksums\.txt --verifier $(rx "${R}")/bin/cosign --arch amd64 --templates-dir [^ ]*/archive/deploy\$"
assert "linux: no insecure option and no hash shortcut reaches the installer" bash -c "! grep -E -x -e '--insecure|--updater-sha256|--verify-signatures|--token|--signing-key' '${STUB_CALLS}/installer.log'"
assert "linux: the installed updater verifies the bundle of checksums.txt with the version-bound identity" call_like updater "^verify-bundle --artifact [^ ]*/checksums\.txt --bundle [^ ]*/checksums\.txt\.sigstore\.json --version $(rx "${TAG}") --identity-regexp \\^https://github\\\\\\.com/myriarch/myriarch/\\\\\\.github/workflows/release\\\\\\.yml@refs/tags/v9\\\\\\.9\\\\\\.9\\\$ --issuer ${ISSUER}\$"
assert "linux: the server image is pulled by its tag without credentials" call_is docker "pull --quiet ${SERVER_REPO}:${VER}"
assert "linux: the updater image is pulled by its tag without credentials" call_is docker "pull --quiet ${UPDATER_REPO}:${VER}"
assert "linux: no docker call has credentials" bash -c "! grep -v '^DOCKER_CONFIG=empty\$' '${STUB_CALLS}/docker.env' | grep -q ."
assert "linux: no token in any call or log" no_secret_in_logs

new_root linux-commit
make_linux_release arm64
expect_ok "linux: arm64, with the commit" run_smoke linux --tag "${TAG}" --arch arm64 --commit "${COMMIT}"
assert "linux: with a commit the exact identity is verified first" bash -c "$(declare -f calls); STUB_CALLS='${STUB_CALLS}'; [ \"\$(calls verify-blob | wc -l | tr -d ' ')\" = 3 ] && calls verify-blob | sed -n 1p | grep -q -- '^--mode exact .* --commit ${COMMIT} '"
assert "linux: the installer is told the architecture" bash -c "grep -qx -e arm64 '${STUB_CALLS}/installer.log'"
assert "linux: the arm64 files are the ones downloaded" bash -c "grep -q 'myriarch-updater_${VER}_linux_arm64.tar.gz\$' '${STUB_CALLS}/curl.log'"

new_root linux-fail-digest
make_linux_release amd64
printf '["%s@%s"]\n' "${SERVER_REPO}" "${SIDX}" > "${STUB_STATE}/docker-repodigests"
expect_fail "linux: an updater image whose tag does not carry the digest of the release" '\[image_digest\]' run_smoke linux --tag "${TAG}" --arch amd64

new_root linux-fail-checksum
make_linux_release amd64
printf '%s  myriarch-updater_%s_linux_amd64.tar.gz\n%s  myriarch-updater_%s_linux_amd64\n' "$(printf '0%.0s' $(seq 64))" "${VER}" "$(sha256_of "${STUB_SERVE}/myriarch-updater_${VER}_linux_amd64")" "${VER}" > "${STUB_SERVE}/checksums.txt"
cp "${STUB_SERVE}/checksums.txt" "${STUB_STATE}/good-blob"
expect_fail "linux: a file that does not match checksums.txt" '\[checksum_mismatch\]' run_smoke linux --tag "${TAG}" --arch amd64
assert "linux: nothing is installed after a failed check" test ! -e "${STUB_CALLS}/installer.log"
printf '%s  myriarch-updater_%s_linux_amd64\n' "$(sha256_of "${STUB_SERVE}/myriarch-updater_${VER}_linux_amd64")" "${VER}" > "${STUB_SERVE}/checksums.txt"
cp "${STUB_SERVE}/checksums.txt" "${STUB_STATE}/good-blob"
expect_fail "linux: a file that checksums.txt does not list" '\[checksum_entry\]' run_smoke linux --tag "${TAG}" --arch amd64

new_root linux-fail-sig
make_linux_release amd64
printf -- '--mode bound\n' > "${STUB_STATE}/verify-blob.fail"
expect_fail "linux: a failing version-bound verification" '\[signature_invalid\]' run_smoke linux --tag "${TAG}" --arch amd64

new_root linux-fail-installer
make_linux_release amd64
: > "${STUB_STATE}/installer.fail"
expect_fail "linux: a failing installer" '\[install_failed\]' run_smoke linux --tag "${TAG}" --arch amd64

new_root linux-fail-other-updater
make_linux_release amd64
: > "${STUB_STATE}/installer.other-updater"
expect_fail "linux: an installed updater that is not the downloaded one" '\[install_failed\]' run_smoke linux --tag "${TAG}" --arch amd64

new_root linux-fail-bundle
make_linux_release amd64
: > "${STUB_STATE}/updater.fail"
expect_fail "linux: verify-bundle of the updater rejects the bundle" '\[verify_bundle\]' run_smoke linux --tag "${TAG}" --arch amd64

new_root linux-fail-archive
make_linux_release amd64
python3 -I - "${STUB_SERVE}/myriarch-updater_${VER}_linux_amd64.tar.gz" <<'PY'
import io, sys, tarfile

with tarfile.open(sys.argv[1], "w:gz") as t:
    info = tarfile.TarInfo("../evil")
    info.size = 1
    t.addfile(info, io.BytesIO(b"x"))
PY
write_sums amd64
expect_fail "linux: an archive with an entry outside its directory" '\[archive_invalid\]' run_smoke linux --tag "${TAG}" --arch amd64
assert "linux: that archive reaches no installer" test ! -e "${STUB_CALLS}/installer.log"

new_root linux-inputs
make_linux_release amd64
expect_fail "linux: an unknown architecture" 'usage:' run_smoke linux --tag "${TAG}" --arch riscv64
expect_fail "linux: the architecture is required" 'usage:' run_smoke linux --tag "${TAG}"
expect_fail "linux: a malformed commit" '\[input_invalid\]' run_smoke linux --tag "${TAG}" --arch amd64 --commit nope

# ---- kind ------------------------------------------------------------------------------------------------------------------
echo "smoke.sh kind"
write_pods() { # user digest
  python3 -I - "${STUB_STATE}/pods.json" "$1" "$2" "${SERVER_REPO}:${VER}" <<'PY'
import json, sys

path, user, digest, image = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
items = []
for n in ("myriarch-6d5f7-aaaaa", "myriarch-6d5f7-bbbbb"):
    items.append({
        "metadata": {"name": n},
        "spec": {
            "securityContext": {"runAsNonRoot": True, "runAsUser": user},
            "containers": [{"name": "myriarchd", "image": image, "securityContext": {"readOnlyRootFilesystem": True}}],
        },
        "status": {
            "phase": "Running",
            "containerStatuses": [{"name": "myriarchd", "image": image, "imageID": "ghcr.io/myriarch/myriarch@" + digest}],
        },
    })
json.dump({"items": items}, open(path, "w"))
PY
}
new_kind_root() { new_root "$1"; write_pods 65532 "${SIDX}"; }
KIND_ARGS=(kind --version "${VER}" --server-index "${SIDX}" --server-amd64 "${SAMD}")
NODE_IMAGE="$(grep -E '^KIND_NODE_IMAGE=' "${ROOT}/hack/kind/versions.env" | cut -d= -f2-)"
PG_IMAGE="$(grep -E '^POSTGRES_IMAGE=' "${ROOT}/hack/kind/versions.env" | cut -d= -f2-)"

new_kind_root kind-ok
expect_ok "kind: the chart of the tag on a kind cluster" run_smoke "${KIND_ARGS[@]}"
assert "kind: the cluster comes from hack/kind with the pinned node image" call_like kind "^create cluster --name myriarch-smoke --config $(rx "${R}")/hack/kind/cluster\.yaml --image $(rx "${NODE_IMAGE}") --kubeconfig [^ ]+ --wait 180s\$"
assert "kind: the cluster is deleted at the end" bash -c "$(declare -f calls); STUB_CALLS='${STUB_CALLS}'; calls kind | tail -n 1 | grep -qx 'delete cluster --name myriarch-smoke'"
assert "kind: helm installs the chart of the checkout with the required value only" call_is helm "install myriarch ${R}/deploy/helm/myriarch -n myriarch --kube-context kind-myriarch-smoke --set config.server.publicUrl=https://fleet.example.test"
assert "kind: helm gets no image override and no values file" bash -c "! grep -E 'image|^-f\$|--values|--reuse-values' '${STUB_CALLS}/helm.log'"
assert "kind: PostgreSQL gets the pinned image of hack/kind/versions.env" bash -c "grep -rqF '${PG_IMAGE}' '${STUB_CALLS}'/kubectl-apply-*.yaml && ! grep -rq '@@' '${STUB_CALLS}'/kubectl-apply-*.yaml"
assert "kind: the application namespace enforces the restricted Pod Security level" bash -c "grep -rqx '    pod-security.kubernetes.io/enforce: restricted' '${STUB_CALLS}'/kubectl-apply-*.yaml"
assert "kind: every kubectl call names the context of the cluster" bash -c "$(declare -f calls); STUB_CALLS='${STUB_CALLS}'; ! calls kubectl | grep -v '^--context kind-myriarch-smoke '"
assert "kind: the rollout of the deployment is awaited" call_like kubectl "^--context kind-myriarch-smoke -n myriarch rollout status deploy/myriarch "
assert "kind: the port-forward goes to the local interface" call_like kubectl "^--context kind-myriarch-smoke -n myriarch port-forward deploy/myriarch [0-9]+:8080 --address 127\.0\.0\.1\$"
assert "kind: /healthz is asked over the forwarded port" grep -Eqx 'http://127.0.0.1:[0-9]+/healthz' "${STUB_CALLS}/curl.log"
assert "kind: /readyz is asked over the forwarded port" grep -Eqx 'http://127.0.0.1:[0-9]+/readyz' "${STUB_CALLS}/curl.log"
assert "kind: the bootstrap sign-in is posted" grep -Eqx 'http://127.0.0.1:[0-9]+/api/v1/auth/bootstrap-login' "${STUB_CALLS}/curl.log"
assert "kind: the bootstrap token reaches curl on stdin only" bash -c "grep -qF '${BOOTSTRAP_VALUE}' '${STUB_CALLS}/curl-stdin.txt' && ! grep -qF '${BOOTSTRAP_VALUE}' '${STUB_CALLS}'/*.log"
assert "kind: no secret in any call or log" no_secret_in_logs

new_kind_root kind-fail-chart-version
printf 'apiVersion: v2\nname: myriarch\nversion: 0.0.1\nappVersion: "0.0.1"\n' > "${R}/deploy/helm/myriarch/Chart.yaml"
expect_fail "kind: a checkout that is not the tag" '\[chart_version\]' run_smoke "${KIND_ARGS[@]}"
assert "kind: no cluster is created for the wrong checkout" test ! -e "${STUB_CALLS}/kind.log"

new_kind_root kind-fail-root
write_pods 0 "${SIDX}"
expect_fail "kind: a pod that runs as root" '\[pod_security\]' run_smoke "${KIND_ARGS[@]}"
assert "kind: the cluster is deleted after a failure" call_is kind "delete cluster --name myriarch-smoke"

new_kind_root kind-fail-image
write_pods 65532 "sha256:$(printf '9%.0s' $(seq 64))"
expect_fail "kind: a pod that runs another image than the release" '\[pod_security\]' run_smoke "${KIND_ARGS[@]}"
write_pods 65532 "${SAMD}"
expect_ok "kind: the amd64 digest is accepted as the imageID" run_smoke "${KIND_ARGS[@]}"

new_kind_root kind-fail-health
STUB_HEALTH_CODE=503 expect_fail "kind: a server that is not ready" '\[healthz\]' run_smoke "${KIND_ARGS[@]}"

new_kind_root kind-fail-login
STUB_LOGIN_CODE=401 expect_fail "kind: a bootstrap sign-in that is refused" '\[bootstrap_login\]' run_smoke "${KIND_ARGS[@]}"

new_kind_root kind-fail-notes
printf 'myriarchd 9.9.9 was installed.\n' > "${STUB_STATE}/notes.txt"
expect_fail "kind: NOTES without a bootstrap token Secret" '\[bootstrap_login\]' run_smoke "${KIND_ARGS[@]}"

new_kind_root kind-inputs
expect_fail "kind: the server index is required" 'usage:' run_smoke kind --version "${VER}" --server-amd64 "${SAMD}"
expect_fail "kind: a malformed digest" '\[input_invalid\]' run_smoke kind --version "${VER}" --server-index sha256:abc --server-amd64 "${SAMD}"
expect_fail "kind: a malformed version" '\[input_invalid\]' run_smoke kind --version 1.0 --server-index "${SIDX}" --server-amd64 "${SAMD}"
rm -f "${R}/bin/helm"
expect_fail "kind: a missing helm" '\[tool_missing\]' run_smoke "${KIND_ARGS[@]}"

# ---- smoke-windows.ps1 ------------------------------------------------------------------------------------------------------
echo "smoke-windows.ps1"
PS1="${ROOT}/ci/release/smoke-windows.ps1"
assert "smoke-windows.ps1 has the SPDX header" bash -c "head -n 3 '${PS1}' | grep -q 'SPDX-License-Identifier: Apache-2.0'"
assert "smoke-windows.ps1 writes to no registry or release and has no insecure shortcut" bash -c "! grep -E -i -- '-Insecure|-UpdaterSha256|-VerifySignatures|crane|gh release (create|edit|delete)|docker login' <(grep -v '^[[:space:]]*#' '${PS1}')"
assert "smoke-windows.ps1 unblocks the files only after the checks against checksums.txt" bash -c "
  [ \"\$(grep -n 'Get-ChildItem.*Unblock-File' '${PS1}' | head -n 1 | cut -d: -f1)\" -gt \"\$(grep -n 'does not match checksums.txt' '${PS1}' | head -n 1 | cut -d: -f1)\" ]"
if have pwsh; then
  assert "smoke-windows.ps1 parses" pwsh -NoProfile -Command "\$e = \$null; \$t = \$null; [void][System.Management.Automation.Language.Parser]::ParseFile('${PS1}', [ref]\$t, [ref]\$e); if (\$e.Count) { \$e | ForEach-Object { \$_.ToString() }; exit 1 }"
  winerr() { pwsh -NoProfile -NonInteractive -File "${PS1}" "$@" 2>&1; }
  winexit() { pwsh -NoProfile -NonInteractive -File "${PS1}" "$@" >/dev/null 2>&1; echo "$?"; }
  expect_fail "smoke-windows.ps1: no mode" 'usage:' winerr
  expect_fail "smoke-windows.ps1: an unknown mode" 'usage:' winerr -Mode Bogus
  expect_fail "smoke-windows.ps1: Assets needs its parameters" 'usage:.*-Mode Assets' winerr -Mode Assets
  expect_fail "smoke-windows.ps1: Assets with a malformed version" 'usage:.*Version is not valid' winerr -Mode Assets -AssetsDir x -Version 1.0 -Signer y
  expect_fail "smoke-windows.ps1: Install needs its parameters" 'usage:.*-Mode Install' winerr -Mode Install -Tag v1.0.0
  expect_fail "smoke-windows.ps1: Install with a tag that is not the version" 'usage:.*do not belong together' winerr -Mode Install -Tag v1.0.0 -Version 1.0.1 -Cosign c -WindowsSigning off
  expect_fail "smoke-windows.ps1: Install with an unknown signing mode" 'usage:.*WindowsSigning is not valid' winerr -Mode Install -Tag v1.0.0 -Version 1.0.0 -Cosign c -WindowsSigning test
  expect_fail "smoke-windows.ps1: Install with a malformed commit" 'usage:.*Commit is not valid' winerr -Mode Install -Tag v1.0.0 -Version 1.0.0 -Cosign c -WindowsSigning off -Commit abc
  assert "smoke-windows.ps1: a usage error exits with 2" test "$(winexit -Mode Bogus)" = 2
  if pwsh -NoProfile -Command 'if ($IsWindows) { exit 0 } else { exit 1 }' >/dev/null 2>&1; then
    echo "  note  running on Windows: the refusal on other platforms is not tested"
  else
    expect_fail "smoke-windows.ps1: valid parameters on another platform are refused" '\[unsupported_platform\]' winerr -Mode Install -Tag v1.0.0 -Version 1.0.0 -Cosign c -WindowsSigning off
  fi
else
  echo "  note  pwsh not found: the parse check and the parameter tests of smoke-windows.ps1 are skipped (ci/install-tools.sh pwsh)"
  if [ "${CI:-}" = true ]; then fail "pwsh is required in CI for the tests of smoke-windows.ps1"; fi
fi

finish_tests "release-smoke-test"
