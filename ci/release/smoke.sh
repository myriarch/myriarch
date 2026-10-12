#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Smoke tests of a published release (ADR-0035). The release workflow runs them after finalize, in jobs that hold only read scopes.
# They use the release the way a user does: everything is downloaded over https without credentials, and the registry is read
# anonymously (an empty DOCKER_CONFIG). The Windows counterpart is ci/release/smoke-windows.ps1.
#
#   ci/release/smoke.sh assets    --tag vX.Y.Z --commit SHA
#   ci/release/smoke.sh linux     --tag vX.Y.Z --arch amd64|arm64 [--commit SHA]
#   ci/release/smoke.sh image-run --image REPO@sha256:<64 hex> --kind server|updater --version X.Y.Z [--pull-tag TAG]
#   ci/release/smoke.sh kind      --version X.Y.Z --server-index sha256:<64 hex> --server-amd64 sha256:<64 hex>
#
# assets     Downloads the 42 release files of deploy/release/assets.json, checks the file set and checksums.txt
#            (verify-dist.sh --stage final), verifies the keyless signature of checksums.txt with the exact identity of the tag and
#            with the ADR-0029 pattern (verify-blob.sh), proves with a tampered copy that the verification can fail, asks GitHub for
#            the release attestations (gh release verify, verify-asset), verifies the build provenance of two files with the
#            bundle asset and of one file through the attestation API (gh attestation verify), and checks both images by their
#            tags: crane digest, cosign verify and the provenance attestation of the index.
#            Environment: GH_TOKEN (gh refuses to run without one; the registry reads stay anonymous), TOOLS_BIN (cosign, crane,
#            gh), SERVER_INDEX_DIGEST and UPDATER_INDEX_DIGEST (sha256:<64 hex>). IMAGE_TAGS may name the expected image tags
#            (comma separated); they must equal the tags that the tag rule gives (X.Y.Z-rc.N, or X.Y.Z and X.Y).
# linux      Downloads checksums.txt, its bundle, the updater archive and the bare updater of the architecture, verifies them,
#            runs the install-linux.sh from the ARCHIVE in staging mode (--root) with the keyless verification of the updater and
#            the real upstream collector, checks that the installed updater is the downloaded binary and that its verify-bundle
#            accepts the bundle of checksums.txt, then runs image-run for both images from their tags without credentials.
#            Environment: TOOLS_BIN (cosign), SERVER_INDEX_DIGEST, UPDATER_INDEX_DIGEST. The commit (--commit or RELEASE_COMMIT) is
#            optional: with it the signature is also checked against the exact identity including the commit.
# image-run  Pulls the image (by digest, or with --pull-tag by REPO:TAG without credentials and then checks that the tag points
#            at the digest), checks user 65532:65532 and the architecture of the host, runs `version` of every binary with
#            --network none --read-only and reads it from stdout (the output contract of the binaries, "CLI output" in
#            docs/development.md), and proves that the image has no shell. Without --pull-tag the credentials of the host
#            are used (the verify-images job runs before the packages are public).
# kind       Creates a kind cluster (hack/kind/cluster.yaml, node image of hack/kind/versions.env), installs PostgreSQL and the
#            chart of this checkout (the tag) with the default image, and checks the rollout, the non-root pods, /healthz,
#            /readyz and the sign-in of the bootstrap admin. The cluster is deleted at the end. Tools: kind, kubectl and helm from
#            TOOLS_BIN. The checkout must be the tag.
#
# Environment (all subcommands)
#   TOOLS_BIN          directory with cosign, crane, gh (and kind, kubectl, helm); default .cache/bin. The tools are never looked up
#                      in PATH, and their versions must be the pins of ci/versions.env (checked once).
#   SMOKE_RETRIES      attempts of a check that talks to a service, default 3
#   SMOKE_RETRY_DELAY  seconds between attempts, default 10
#   SMOKE_WAIT_SECONDS how long the kind test waits for /healthz and /readyz, default 90
#
# Errors: one line "[smoke] ERROR [<code>] <message>" on stderr, exit 1. Usage errors "[smoke] usage: ...", exit 2. Info lines
# "[smoke] <message>" on stderr. The codes are the stable interface of the tests. Tokens are read from the environment only, are
# never an argument and are never printed; the throw-away values that the smoke tests generate (an enrollment token, a database
# password) go to files with mode 0600 and are never printed either.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"
SMOKE_RETRIES="${SMOKE_RETRIES:-3}"
SMOKE_RETRY_DELAY="${SMOKE_RETRY_DELAY:-10}"
SMOKE_WAIT_SECONDS="${SMOKE_WAIT_SECONDS:-90}"

log() { printf '[smoke] %s\n' "$*" >&2; }
fail() { local code="$1"; shift; printf '[smoke] ERROR [%s] %s\n' "${code}" "$*" >&2; exit 1; }
usage_error() { printf '[smoke] usage: %s\n' "$*" >&2; exit 2; }
have() { command -v "$1" >/dev/null 2>&1; }

# ---- data files --------------------------------------------------------------------------------------------------------
# env_value FILE KEY: the value of the line KEY=value (data: nothing is evaluated), or empty.
env_value() {
  local line
  line="$(grep -E "^$2=" "$1" 2>/dev/null | head -n 1)" || true
  printf '%s' "${line#*=}"
}

IDENTITY_ENV="${ROOT}/ci/release/identity.env"
VERSIONS_ENV="${ROOT}/ci/versions.env"
ASSETS_JSON="${ROOT}/deploy/release/assets.json"
VERIFY_DIST="${ROOT}/ci/release/verify-dist.sh"
VERIFY_BLOB="${ROOT}/ci/release/verify-blob.sh"

ID_REPO="" ID_WORKFLOW="" ID_ISSUER="" ID_TRIGGER="" ID_IMAGE_SERVER="" ID_IMAGE_UPDATER=""
COSIGN_VERSION="" CRANE_VERSION="" GH_VERSION="" OTELCOL_VERSION=""

load_identity() {
  [ -f "${IDENTITY_ENV}" ] || fail identity_missing "ci/release/identity.env not found"
  ID_REPO="$(env_value "${IDENTITY_ENV}" RELEASE_REPOSITORY)"
  ID_WORKFLOW="$(env_value "${IDENTITY_ENV}" RELEASE_WORKFLOW)"
  ID_ISSUER="$(env_value "${IDENTITY_ENV}" RELEASE_OIDC_ISSUER)"
  ID_TRIGGER="$(env_value "${IDENTITY_ENV}" RELEASE_TRIGGER)"
  ID_IMAGE_SERVER="$(env_value "${IDENTITY_ENV}" RELEASE_IMAGE_SERVER)"
  ID_IMAGE_UPDATER="$(env_value "${IDENTITY_ENV}" RELEASE_IMAGE_UPDATER)"
  [[ "${ID_REPO}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail identity_invalid "RELEASE_REPOSITORY in identity.env is not owner/name"
  [[ "${ID_WORKFLOW}" =~ ^\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml$ ]] || fail identity_invalid "RELEASE_WORKFLOW in identity.env is not a workflow path"
  [[ "${ID_ISSUER}" =~ ^https://[A-Za-z0-9._:/-]+$ ]] || fail identity_invalid "RELEASE_OIDC_ISSUER in identity.env is not an https URL"
  [[ "${ID_TRIGGER}" =~ ^[a-z_]+$ ]] || fail identity_invalid "RELEASE_TRIGGER in identity.env is not an event name"
  [[ "${ID_IMAGE_SERVER}" =~ ^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$ ]] || fail identity_invalid "RELEASE_IMAGE_SERVER in identity.env is not a repository"
  [[ "${ID_IMAGE_UPDATER}" =~ ^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$ ]] || fail identity_invalid "RELEASE_IMAGE_UPDATER in identity.env is not a repository"
}

load_versions() {
  [ -f "${VERSIONS_ENV}" ] || fail versions_missing "ci/versions.env not found"
  COSIGN_VERSION="$(env_value "${VERSIONS_ENV}" COSIGN_VERSION)"
  CRANE_VERSION="$(env_value "${VERSIONS_ENV}" CRANE_VERSION)"
  GH_VERSION="$(env_value "${VERSIONS_ENV}" GH_VERSION)"
  OTELCOL_VERSION="$(env_value "${VERSIONS_ENV}" OTELCOL_VERSION)"
  local v
  for v in "${COSIGN_VERSION}" "${CRANE_VERSION}" "${GH_VERSION}"; do
    [[ "${v}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail versions_invalid "a tool version in ci/versions.env is missing or malformed"
  done
  [[ "${OTELCOL_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail versions_invalid "OTELCOL_VERSION in ci/versions.env is missing or malformed"
}

# ---- tools -------------------------------------------------------------------------------------------------------------
need_tool() { # name
  [ -f "${TOOLS_BIN}/$1" ] && [ -x "${TOOLS_BIN}/$1" ] || fail tool_missing "$1 not found in TOOLS_BIN (${TOOLS_BIN})"
}
check_cosign() {
  need_tool cosign
  local got
  got="$("${TOOLS_BIN}/cosign" version 2>&1 | awk '/^GitVersion:/ { print $2; exit }')" || true
  [ "${got}" = "${COSIGN_VERSION}" ] || fail tool_version "cosign must be ${COSIGN_VERSION} (ci/versions.env), found '${got:-none}'"
}
check_crane() {
  need_tool crane
  local got
  got="$("${TOOLS_BIN}/crane" version 2>&1 | head -n 1)" || true
  [ "${got}" = "${CRANE_VERSION}" ] || fail tool_version "crane must be ${CRANE_VERSION} (ci/versions.env), found '${got:-none}'"
}
check_gh() {
  need_tool gh
  local got
  got="$("${TOOLS_BIN}/gh" --version 2>&1 | head -n 1)" || true
  case "${got}" in
    "gh version ${GH_VERSION#v}" | "gh version ${GH_VERSION#v} "*) ;;
    *) fail tool_version "gh must be ${GH_VERSION#v} (ci/versions.env), found '${got:-none}'" ;;
  esac
  [ -n "${GH_TOKEN:-}" ] || fail gh_token_missing "GH_TOKEN is not set (gh refuses to run without a token; set it from github.token)"
}
check_docker() {
  have docker || fail tool_missing "docker not found in PATH"
}

# ---- inputs ------------------------------------------------------------------------------------------------------------
# Every value is matched as a whole string, and a control character is refused first.
check_value() { # name value regex
  case "$2" in *[[:cntrl:]]*) fail input_invalid "$1 contains a control character" ;; esac
  [[ "$2" =~ $3 ]] || fail input_invalid "$1 is not valid: '$2'"
}
TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'
VERSION_RE='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'
COMMIT_RE='^[0-9a-f]{40}$'
DIGEST_RE='^sha256:[0-9a-f]{64}$'

need_arg() { # option value
  [ -n "${2-}" ] || usage_error "option $1 needs a value"
}

# Tags the images carry after finalize (the rule of ci/release/meta.sh): X.Y.Z-rc.N, or X.Y.Z and X.Y. Prints one per line.
expected_image_tags() { # version
  case "$1" in
    *-rc.*) printf '%s\n' "$1" ;;
    *) printf '%s\n' "$1" "${1%.*}" ;;
  esac
}

# ---- helpers -----------------------------------------------------------------------------------------------------------
WORK=""
PF_PID=""
KIND_CREATED=0
KIND_NAME="myriarch-smoke"
cleanup() {
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  if [ "${KIND_CREATED}" = 1 ]; then
    log "deleting the kind cluster ${KIND_NAME}"
    "${TOOLS_BIN}/kind" delete cluster --name "${KIND_NAME}" >&2 || true
  fi
  if [ -n "${WORK}" ]; then rm -rf "${WORK}"; fi
}
trap cleanup EXIT

make_work() {
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-smoke.XXXXXX")"
  mkdir -p "${WORK}/anon-docker"
  chmod 700 "${WORK}"
}

# anon COMMAND...: runs the command without registry credentials (an empty Docker configuration).
anon() { DOCKER_CONFIG="${WORK}/anon-docker" "$@"; }

last_line() { # file
  tail -n 1 "$1" 2>/dev/null | tr -d '[:cntrl:]' | cut -c1-300
}

# check CODE WHAT COMMAND...: runs the command up to SMOKE_RETRIES times (stdout discarded, or written to $CHECK_OUT when it is set;
# stderr kept for the message); fails with CODE when it never succeeds.
check() {
  local code="$1" what="$2"; shift 2
  local attempt=1 err="${WORK}/check-err.txt"
  while :; do
    if "$@" >"${CHECK_OUT:-/dev/null}" 2>"${err}"; then return 0; fi
    if [ "${attempt}" -ge "${SMOKE_RETRIES}" ]; then
      fail "${code}" "${what} failed: $(last_line "${err}")"
    fi
    log "${what} failed (attempt ${attempt} of ${SMOKE_RETRIES}), trying again in ${SMOKE_RETRY_DELAY}s"
    attempt=$((attempt + 1))
    sleep "${SMOKE_RETRY_DELAY}"
  done
}

sha256_file() {
  if have sha256sum; then sha256sum "$1" | awk '{print $1}'
  elif have shasum; then shasum -a 256 "$1" | awk '{print $1}'
  else fail tool_missing "neither sha256sum nor shasum found"
  fi
}

# fetch NAME DESTDIR: anonymous https download of one release file of $TAG.
fetch() {
  local name="$1" dest="$2"
  local url="https://github.com/${ID_REPO}/releases/download/${TAG}/${name}"
  have curl || fail tool_missing "curl not found in PATH"
  curl -q --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 3 --connect-timeout 20 --max-time 900 \
    -o "${dest}/${name}" "${url}" || fail download_failed "download of ${url} failed"
}
release_url() { printf 'https://github.com/%s/releases/download/%s/%s' "${ID_REPO}" "${TAG}" "$1"; }

# asset_names VERSION: the names of deploy/release/assets.json, one per line.
asset_names() {
  [ -f "${ASSETS_JSON}" ] || fail assets_json "deploy/release/assets.json not found"
  python3 -I - "${ASSETS_JSON}" "$1" <<'PY' || fail assets_json "deploy/release/assets.json is not a valid asset list"
import json, re, sys

path, version = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f:
    doc = json.load(f)
if doc.get("schema") != 1 or not isinstance(doc.get("assets"), list) or not doc["assets"]:
    sys.exit(1)
names = []
for a in doc["assets"]:
    name = a["name"].replace("{version}", version)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name) or name in names:
        sys.exit(1)
    names.append(name)
for must in ("checksums.txt", "checksums.txt.sigstore.json", "myriarch_%s_provenance.intoto.jsonl" % version):
    if must not in names:
        sys.exit(1)
print("\n".join(names))
PY
}

# checksum_of FILE NAME: the SHA-256 that checksums.txt lists for NAME; exactly one line must name it.
checksum_of() {
  local sums="$1" name="$2" esc line
  esc="$(printf '%s' "${name}" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  line="$(grep -E -- "^[0-9a-f]{64}  ${esc}\$" "${sums}" || true)"
  [ -n "${line}" ] && [ "$(printf '%s\n' "${line}" | wc -l | tr -d ' ')" = 1 ] || fail checksum_entry "checksums.txt must list ${name} exactly once"
  printf '%s' "${line%% *}"
}
check_listed_file() { # sums name file
  local want got
  want="$(checksum_of "$1" "$2")"
  got="$(sha256_file "$3")"
  [ "${want}" = "${got}" ] || fail checksum_mismatch "$2 does not match checksums.txt (expected ${want}, got ${got})"
}

# The identity strings (ci/release/identity.env, ADR-0029).
exact_identity() { printf 'https://github.com/%s/%s@refs/tags/%s' "${ID_REPO}" "${ID_WORKFLOW}" "${TAG}"; }
bound_identity_regexp() { # version
  local esc_v esc_r esc_w
  esc_v="$(printf '%s' "$1" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  esc_r="$(printf '%s' "${ID_REPO}" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  esc_w="$(printf '%s' "${ID_WORKFLOW}" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  printf '^https://github\\.com/%s/%s@refs/tags/v%s$' "${esc_r}" "${esc_w}" "${esc_v}"
}
signer_workflow() { printf '%s/%s@refs/tags/%s' "${ID_REPO}" "${ID_WORKFLOW}" "${TAG}"; }

# blob_verify MODE BUNDLE BLOB: verify-blob.sh, the one place that composes cosign verify-blob.
blob_verify() { # mode bundle blob
  local mode="$1" bundle="$2" blob="$3"
  local args=(--mode "${mode}" --bundle "${bundle}" --repository "${ID_REPO}" --workflow "${ID_WORKFLOW}" --tag "${TAG}")
  if [ "${mode}" = exact ]; then args+=(--commit "${COMMIT}"); fi
  [ -x "${VERIFY_BLOB}" ] || fail tool_missing "ci/release/verify-blob.sh not found or not executable"
  TOOLS_BIN="${TOOLS_BIN}" "${VERIFY_BLOB}" "${args[@]}" "${blob}"
}

# cosign_verify_image REPO@DIGEST: cosign verify with the exact identity flags (3.8 of the design), anonymously. cosign v3 also accepts
# the attestations of the same identity, so the output must contain a cosign signature (critical.type .../cosign/sign/v1) of the digest.
cosign_verify_image() { # repo@digest
  anon "${TOOLS_BIN}/cosign" verify \
    --certificate-identity "$(exact_identity)" \
    --certificate-oidc-issuer "${ID_ISSUER}" \
    --certificate-github-workflow-repository "${ID_REPO}" \
    --certificate-github-workflow-ref "refs/tags/${TAG}" \
    --certificate-github-workflow-sha "${COMMIT}" \
    --certificate-github-workflow-trigger "${ID_TRIGGER}" \
    "$1" | python3 -I -c '
import json, sys

try:
    doc = json.load(sys.stdin)
except ValueError:
    doc = None
want = sys.argv[1].split("@", 1)[1]
ok = isinstance(doc, list) and any(
    isinstance(e, dict)
    and (e.get("critical") or {}).get("type") == "https://sigstore.dev/cosign/sign/v1"
    and ((e.get("critical") or {}).get("image") or {}).get("docker-manifest-digest") == want
    for e in doc)
if not ok:
    print("cosign verify found no cosign signature of %s (an attestation is not a signature)" % want, file=sys.stderr)
sys.exit(0 if ok else 1)
' "$1"
}

# moving_tag_is_newer REF VERSION: the image behind the moving tag X.Y has a version label above VERSION (the rule of finalize.sh)
moving_tag_is_newer() { # ref version
  local out
  out="$(anon "${TOOLS_BIN}/crane" config "$1" 2>"${WORK}/crane-config-err.txt")" || return 1
  printf '%s' "${out}" | python3 -I -c '
import json, re, sys

def key(v):
    m = re.match(r"^(\d+)\.(\d+)\.(\d+)$", v or "")
    return tuple(int(x) for x in m.groups()) if m else None

try:
    label = json.load(sys.stdin)["config"]["Labels"]["org.opencontainers.image.version"]
except Exception:
    sys.exit(1)
a, b = key(label), key(sys.argv[1])
sys.exit(0 if a is not None and b is not None and a > b else 1)
' "$2"
}

# ---- assets ------------------------------------------------------------------------------------------------------------
cmd_assets() {
  TAG="" COMMIT="${RELEASE_COMMIT:-}"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --tag) need_arg "$@"; TAG="$2"; shift 2 ;;
      --commit) need_arg "$@"; COMMIT="$2"; shift 2 ;;
      *) usage_error "smoke.sh assets --tag vX.Y.Z --commit SHA (unknown argument '$1')" ;;
    esac
  done
  [ -n "${TAG}" ] && [ -n "${COMMIT}" ] || usage_error "smoke.sh assets --tag vX.Y.Z --commit SHA"
  check_value tag "${TAG}" "${TAG_RE}"
  check_value commit "${COMMIT}" "${COMMIT_RE}"
  local version="${TAG#v}"
  local server_index="${SERVER_INDEX_DIGEST:-}" updater_index="${UPDATER_INDEX_DIGEST:-}"
  check_value SERVER_INDEX_DIGEST "${server_index}" "${DIGEST_RE}"
  check_value UPDATER_INDEX_DIGEST "${updater_index}" "${DIGEST_RE}"

  local tags want_tags
  want_tags="$(expected_image_tags "${version}")"
  tags="${want_tags}"
  if [ -n "${IMAGE_TAGS:-}" ]; then
    check_value IMAGE_TAGS "${IMAGE_TAGS}" '^[0-9A-Za-z.,-]+$'
    [ "$(printf '%s' "${IMAGE_TAGS}" | tr ',' '\n')" = "${want_tags}" ] ||
      fail image_tags "IMAGE_TAGS '${IMAGE_TAGS}' is not what the tag rule gives for ${TAG} ($(printf '%s' "${want_tags}" | tr '\n' ','))"
  fi

  load_identity
  load_versions
  check_cosign
  check_crane
  check_gh
  make_work

  # 1. the files
  local dl="${WORK}/release" names name n=0
  mkdir -p "${dl}"
  names="$(asset_names "${version}")"
  while IFS= read -r name; do
    fetch "${name}" "${dl}"
    n=$((n + 1))
  done <<< "${names}"
  log "downloaded ${n} release files of ${TAG} without credentials"
  [ -x "${VERIFY_DIST}" ] || fail tool_missing "ci/release/verify-dist.sh not found or not executable"
  "${VERIFY_DIST}" --dir "${dl}" --version "${version}" --stage final || fail verify_dist "the file set or checksums.txt of ${TAG} is not what the release defines"

  # 2. the signature of checksums.txt
  local sums="${dl}/checksums.txt" bundle="${dl}/checksums.txt.sigstore.json"
  blob_verify exact "${bundle}" "${sums}" || fail signature_invalid "exact verification of checksums.txt failed (identity of ${TAG} at ${COMMIT:0:12})"
  blob_verify pattern "${bundle}" "${sums}" || fail signature_invalid "pattern verification of checksums.txt failed (ADR-0029 identity)"
  log "checksums.txt: signature ok (exact identity and ADR-0029 pattern)"

  # negative control: a tampered copy must NOT verify, otherwise the checks above prove nothing
  mkdir -p "${WORK}/tampered"
  python3 -I - "${sums}" "${WORK}/tampered/checksums.txt" <<'PY'
import sys

data = bytearray(open(sys.argv[1], "rb").read())
if not data:
    sys.exit(1)
data[0] ^= 0x01
open(sys.argv[2], "wb").write(bytes(data))
PY
  if blob_verify exact "${bundle}" "${WORK}/tampered/checksums.txt" >/dev/null 2>&1; then
    fail negative_control "a checksums.txt with one changed byte verifies: the signature check does not check the content"
  fi
  log "negative control: a tampered checksums.txt is rejected"

  # 3. GitHub's release attestation
  local gh="${TOOLS_BIN}/gh" archive="myrctl_${version}_linux_amd64.tar.gz" updater="myriarch-updater_${version}_linux_amd64"
  check release_attestation "gh release verify ${TAG}" "${gh}" release verify "${TAG}" --repo "${ID_REPO}"
  check release_asset_attestation "gh release verify-asset checksums.txt" "${gh}" release verify-asset "${TAG}" "${sums}" --repo "${ID_REPO}"
  check release_asset_attestation "gh release verify-asset ${archive}" "${gh}" release verify-asset "${TAG}" "${dl}/${archive}" --repo "${ID_REPO}"
  log "release attestation of ${TAG}: ok (release, checksums.txt, ${archive})"

  # 4. build provenance: with the bundle asset (offline policy check), and once through the attestation API
  local provenance="${dl}/myriarch_${version}_provenance.intoto.jsonl" f
  for f in "${updater}" "${archive}"; do
    check provenance_invalid "gh attestation verify ${f} (bundle asset)" "${gh}" attestation verify "${dl}/${f}" \
      --bundle "${provenance}" --repo "${ID_REPO}" --cert-identity "$(exact_identity)" \
      --source-ref "refs/tags/${TAG}" --source-digest "${COMMIT}" --deny-self-hosted-runners
  done
  check provenance_invalid "gh attestation verify ${updater} (attestation API)" "${gh}" attestation verify "${dl}/${updater}" \
    --repo "${ID_REPO}" --signer-workflow "$(signer_workflow)" \
    --source-ref "refs/tags/${TAG}" --source-digest "${COMMIT}" --deny-self-hosted-runners
  log "build provenance: ok (bundle asset for 2 files, attestation API for 1)"

  # 5. the images, by their tags and anonymously
  local repo digest t got
  for repo in "${ID_IMAGE_SERVER}" "${ID_IMAGE_UPDATER}"; do
    if [ "${repo}" = "${ID_IMAGE_SERVER}" ]; then digest="${server_index}"; else digest="${updater_index}"; fi
    while IFS= read -r t; do
      CHECK_OUT="${WORK}/crane-digest.txt" check image_tag_mismatch "crane digest ${repo}:${t} (without credentials)" \
        anon "${TOOLS_BIN}/crane" digest "${repo}:${t}"
      got="$(tr -d '[:space:]' < "${WORK}/crane-digest.txt")"
      if [ "${got}" != "${digest}" ] && [[ "${t}" =~ ^[0-9]+\.[0-9]+$ ]] && moving_tag_is_newer "${repo}:${t}" "${version}"; then
        # finalize.sh leaves the moving tag X.Y alone when it already points at a newer patch release (an older patch released later)
        log "${repo}:${t} points at a newer release than ${version}, as finalize leaves it"
        continue
      fi
      [ "${got}" = "${digest}" ] || fail image_tag_mismatch "${repo}:${t} points at ${got}, the release built ${digest}"
    done <<< "${tags}"
    check signature_invalid "cosign verify ${repo}@${digest}" cosign_verify_image "${repo}@${digest}"
    check attestation_invalid "gh attestation verify oci://${repo}@${digest}" anon "${gh}" attestation verify "oci://${repo}@${digest}" \
      --bundle-from-oci --repo "${ID_REPO}" --signer-workflow "$(signer_workflow)"
    log "${repo}: tags $(printf '%s' "${tags}" | tr '\n' ' ')-> ${digest}, signature and provenance ok"
  done
  log "assets: ok"
}

# ---- image-run ---------------------------------------------------------------------------------------------------------
DOCKER_ANON=0
docker_cmd() { if [ "${DOCKER_ANON}" = 1 ]; then anon docker "$@"; else docker "$@"; fi; }

host_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) printf 'amd64' ;;
    aarch64 | arm64) printf 'arm64' ;;
    *) fail unsupported_platform "unsupported CPU architecture '$(uname -m)'" ;;
  esac
}

# first_word OUTPUT: the first word of the "version" output (stdout only: a program that prints its version to stderr fails the
# check, see "CLI output" in docs/development.md).
first_word() { local w; read -r w _ <<< "$1" || true; printf '%s' "${w}"; }

run_version() { # ref label entrypoint-or-empty
  local ref="$1" label="$2" entry="$3" out args
  args=(run --rm --network none --read-only)
  if [ -n "${entry}" ]; then args+=(--entrypoint "${entry}"); fi
  out="$(docker_cmd "${args[@]}" "${ref}" version 2>"${WORK}/docker-err.txt")" ||
    fail image_version "${label} of ${ref} does not run: $(last_line "${WORK}/docker-err.txt")"
  [ "$(first_word "${out}")" = "${IR_VERSION}" ] ||
    fail image_version "${label} of ${ref} reports '$(printf '%s' "${out}" | head -n 1 | cut -c1-80)', expected version ${IR_VERSION}"
  log "${label}: version ${IR_VERSION}"
}

image_run() { # image kind version pull-tag
  local image="$1" kind="$2" version="$3" tag="$4"
  IR_VERSION="${version}"
  check_value image "${image}" '^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64}$'
  check_value version "${version}" "${VERSION_RE}"
  case "${kind}" in server | updater) ;; *) usage_error "--kind is server or updater" ;; esac
  local repo="${image%@*}" digest="${image##*@}" want_repo
  if [ "${kind}" = server ]; then want_repo="${ID_IMAGE_SERVER}"; else want_repo="${ID_IMAGE_UPDATER}"; fi
  [ "${repo}" = "${want_repo}" ] || fail image_invalid "a ${kind} image is ${want_repo}, not ${repo}"
  check_docker
  [ -n "${WORK}" ] || make_work

  local ref="${image}"
  DOCKER_ANON=0
  if [ -n "${tag}" ]; then
    check_value pull-tag "${tag}" '^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$'
    ref="${repo}:${tag}"
    DOCKER_ANON=1
  fi
  log "pulling ${ref}$([ "${DOCKER_ANON}" = 1 ] && printf ' (without credentials)')"
  check image_pull "docker pull ${ref}" docker_cmd pull --quiet "${ref}"

  if [ -n "${tag}" ]; then
    local digests
    digests="$(docker_cmd image inspect --format '{{json .RepoDigests}}' "${ref}" 2>/dev/null)" || fail image_digest "cannot inspect ${ref}"
    case "${digests}" in
      *"\"${repo}@${digest}\""*) ;;
      *) fail image_digest "${ref} does not carry the digest ${digest} (RepoDigests: ${digests})" ;;
    esac
  fi

  local meta user arch os
  meta="$(docker_cmd image inspect --format '{{.Config.User}}|{{.Architecture}}|{{.Os}}' "${ref}" 2>/dev/null)" || fail image_user "cannot inspect ${ref}"
  IFS='|' read -r user arch os <<< "${meta}"
  [ "${user}" = "65532:65532" ] || fail image_user "${ref} runs as '${user}', expected 65532:65532"
  [ "${os}" = linux ] && [ "${arch}" = "$(host_arch)" ] || fail image_arch "${ref} is ${os}/${arch}, the host is linux/$(host_arch)"

  if [ "${kind}" = server ]; then
    run_version "${ref}" myriarchd ""
    run_version "${ref}" myrctl /usr/local/bin/myrctl
    run_version "${ref}" myriarch-sim /usr/local/bin/myriarch-sim
  else
    run_version "${ref}" myriarch-updater ""
  fi

  if docker_cmd run --rm --network none --entrypoint /bin/sh "${ref}" -c true >/dev/null 2>&1; then
    fail image_has_shell "${ref} contains /bin/sh: the images are built FROM scratch"
  fi
  log "${ref}: user 65532:65532, linux/${arch}, no shell, read-only run ok"
}

cmd_image_run() {
  local image="" kind="" version="" tag=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --image) need_arg "$@"; image="$2"; shift 2 ;;
      --kind) need_arg "$@"; kind="$2"; shift 2 ;;
      --version) need_arg "$@"; version="$2"; shift 2 ;;
      --pull-tag) need_arg "$@"; tag="$2"; shift 2 ;;
      *) usage_error "smoke.sh image-run --image REPO@sha256:<64 hex> --kind server|updater --version X.Y.Z [--pull-tag TAG] (unknown argument '$1')" ;;
    esac
  done
  [ -n "${image}" ] && [ -n "${kind}" ] && [ -n "${version}" ] ||
    usage_error "smoke.sh image-run --image REPO@sha256:<64 hex> --kind server|updater --version X.Y.Z [--pull-tag TAG]"
  load_identity
  image_run "${image}" "${kind}" "${version}" "${tag}"
}

# ---- linux -------------------------------------------------------------------------------------------------------------
cmd_linux() {
  TAG="" COMMIT="${RELEASE_COMMIT:-}"
  local arch=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --tag) need_arg "$@"; TAG="$2"; shift 2 ;;
      --arch) need_arg "$@"; arch="$2"; shift 2 ;;
      --commit) need_arg "$@"; COMMIT="$2"; shift 2 ;;
      *) usage_error "smoke.sh linux --tag vX.Y.Z --arch amd64|arm64 [--commit SHA] (unknown argument '$1')" ;;
    esac
  done
  [ -n "${TAG}" ] && [ -n "${arch}" ] || usage_error "smoke.sh linux --tag vX.Y.Z --arch amd64|arm64 [--commit SHA]"
  check_value tag "${TAG}" "${TAG_RE}"
  case "${arch}" in amd64 | arm64) ;; *) usage_error "--arch is amd64 or arm64" ;; esac
  if [ -n "${COMMIT}" ]; then check_value commit "${COMMIT}" "${COMMIT_RE}"; fi
  local version="${TAG#v}"
  local server_index="${SERVER_INDEX_DIGEST:-}" updater_index="${UPDATER_INDEX_DIGEST:-}"
  check_value SERVER_INDEX_DIGEST "${server_index}" "${DIGEST_RE}"
  check_value UPDATER_INDEX_DIGEST "${updater_index}" "${DIGEST_RE}"

  load_identity
  load_versions
  check_cosign
  check_docker
  make_work

  local dl="${WORK}/download" f
  mkdir -p "${dl}"
  local archive="myriarch-updater_${version}_linux_${arch}.tar.gz" bare="myriarch-updater_${version}_linux_${arch}"
  for f in checksums.txt checksums.txt.sigstore.json "${archive}" "${bare}"; do fetch "${f}" "${dl}"; done
  log "downloaded checksums.txt, its bundle, ${archive} and ${bare} without credentials"

  local sums="${dl}/checksums.txt" bundle="${dl}/checksums.txt.sigstore.json"
  if [ -n "${COMMIT}" ]; then
    blob_verify exact "${bundle}" "${sums}" || fail signature_invalid "exact verification of checksums.txt failed"
  fi
  blob_verify pattern "${bundle}" "${sums}" || fail signature_invalid "pattern verification of checksums.txt failed"
  blob_verify bound "${bundle}" "${sums}" || fail signature_invalid "version-bound verification of checksums.txt failed"
  check_listed_file "${sums}" "${archive}" "${dl}/${archive}"
  check_listed_file "${sums}" "${bare}" "${dl}/${bare}"
  log "checksums.txt: signature ok, ${archive} and ${bare} match it"

  # the installer under test is the one inside the verified archive
  local pkg="${WORK}/archive" member
  mkdir -p "${pkg}"
  while IFS= read -r member; do
    case "${member}" in
      /* | ../* | */../* | .. | *[[:cntrl:]]*) fail archive_invalid "${archive} has an unsafe entry '${member}'" ;;
    esac
  done < <(tar -tzf "${dl}/${archive}")
  tar -xzf "${dl}/${archive}" -C "${pkg}" --no-same-owner
  local installer="${pkg}/deploy/scripts/install-linux.sh"
  [ -f "${installer}" ] && [ -d "${pkg}/deploy" ] || fail archive_invalid "${archive} has no deploy/scripts/install-linux.sh"

  # a throw-away enrollment token: no secret, nothing connects to the endpoint (staging mode starts no service)
  local token_file="${WORK}/enrollment-token" root="${WORK}/root"
  ( umask 077; printf 'myre_%s\n' "$(python3 -I -c 'import secrets; print(secrets.token_hex(24))')" > "${token_file}" )
  mkdir -p "${root}"
  log "installing from the archive into a staging root (keyless verification, collector ${OTELCOL_VERSION})"
  bash "${installer}" --root "${root}" \
    --opamp-endpoint wss://fleet.example.com/v1/opamp --token-file "${token_file}" \
    --site-type datacenter --site-id smoke-01 --collector-version "${OTELCOL_VERSION}" \
    --with-updater --updater-url "$(release_url "${bare}")" --updater-checksums-url "$(release_url checksums.txt)" \
    --verifier "${TOOLS_BIN}/cosign" --arch "${arch}" --templates-dir "${pkg}/deploy" >&2 ||
    fail install_failed "the installer of ${archive} failed"

  local installed="${root}/opt/myriarch/updater/myriarch-updater"
  [ -f "${installed}" ] || fail install_failed "the installer left no updater at opt/myriarch/updater/myriarch-updater"
  cmp -s "${installed}" "${dl}/${bare}" || fail install_failed "the installed updater is not the downloaded ${bare}"
  "${installed}" verify-bundle --artifact "${sums}" --bundle "${bundle}" --version "${TAG}" \
    --identity-regexp "$(bound_identity_regexp "${version}")" --issuer "${ID_ISSUER}" >/dev/null 2>"${WORK}/vb-err.txt" ||
    fail verify_bundle "myriarch-updater verify-bundle rejects the bundle of checksums.txt: $(last_line "${WORK}/vb-err.txt")"
  log "installed updater equals ${bare}; verify-bundle accepts the bundle of checksums.txt"

  image_run "${ID_IMAGE_SERVER}@${server_index}" server "${version}" "$(expected_image_tags "${version}" | head -n 1)"
  image_run "${ID_IMAGE_UPDATER}@${updater_index}" updater "${version}" "$(expected_image_tags "${version}" | head -n 1)"
  log "linux/${arch}: ok"
}

# ---- kind --------------------------------------------------------------------------------------------------------------
KUBE_CONTEXT=""
kc() { "${TOOLS_BIN}/kubectl" --context "${KUBE_CONTEXT}" "$@"; }

free_port() {
  python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

# wait_for_code URL CODE SECONDS: polls until the URL answers with the status code
wait_for_code() { # url code seconds
  local url="$1" want="$2" deadline=$((SECONDS + $3)) got=""
  while :; do
    got="$(curl -s -m 5 -o /dev/null -w '%{http_code}' "${url}" 2>/dev/null)" || got=""
    [ "${got}" = "${want}" ] && return 0
    [ "${SECONDS}" -lt "${deadline}" ] || return 1
    sleep 2
  done
}

cmd_kind() {
  local version="" server_index="" server_amd64=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --version) need_arg "$@"; version="$2"; shift 2 ;;
      --server-index) need_arg "$@"; server_index="$2"; shift 2 ;;
      --server-amd64) need_arg "$@"; server_amd64="$2"; shift 2 ;;
      *) usage_error "smoke.sh kind --version X.Y.Z --server-index sha256:<64 hex> --server-amd64 sha256:<64 hex> (unknown argument '$1')" ;;
    esac
  done
  [ -n "${version}" ] && [ -n "${server_index}" ] && [ -n "${server_amd64}" ] ||
    usage_error "smoke.sh kind --version X.Y.Z --server-index sha256:<64 hex> --server-amd64 sha256:<64 hex>"
  check_value version "${version}" "${VERSION_RE}"
  check_value server-index "${server_index}" "${DIGEST_RE}"
  check_value server-amd64 "${server_amd64}" "${DIGEST_RE}"

  load_identity
  have curl || fail tool_missing "curl not found in PATH"
  have python3 || fail tool_missing "python3 not found in PATH"
  check_docker
  local t
  for t in kind kubectl helm; do need_tool "${t}"; done

  local kind_dir="${ROOT}/hack/kind" chart="${ROOT}/deploy/helm/myriarch"
  [ -f "${kind_dir}/cluster.yaml" ] && [ -f "${kind_dir}/versions.env" ] && [ -f "${kind_dir}/manifests/postgres.yaml" ] ||
    fail kind_files "hack/kind (cluster.yaml, versions.env, manifests/postgres.yaml) is missing from this checkout"
  [ -f "${chart}/Chart.yaml" ] || fail chart_missing "deploy/helm/myriarch is missing from this checkout"
  local app_version
  app_version="$(sed -n 's/^appVersion:[[:space:]]*"\{0,1\}\([^" ]*\)"\{0,1\}[[:space:]]*$/\1/p' "${chart}/Chart.yaml" | head -n 1)"
  [ "${app_version}" = "${version}" ] ||
    fail chart_version "the chart of this checkout has appVersion '${app_version}', the release is ${version}: the job must check out the tag"

  local node_image postgres_image
  node_image="$(env_value "${kind_dir}/versions.env" KIND_NODE_IMAGE)"
  postgres_image="$(env_value "${kind_dir}/versions.env" POSTGRES_IMAGE)"
  check_value KIND_NODE_IMAGE "${node_image}" '^[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$'
  check_value POSTGRES_IMAGE "${postgres_image}" '^[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$'

  make_work
  export KUBECONFIG="${WORK}/kubeconfig"
  KUBE_CONTEXT="kind-${KIND_NAME}"
  local ns_app=myriarch ns_infra=probe-infra

  log "creating the kind cluster ${KIND_NAME} (${node_image%%@*})"
  KIND_CREATED=1
  "${TOOLS_BIN}/kind" create cluster --name "${KIND_NAME}" --config "${kind_dir}/cluster.yaml" --image "${node_image}" \
    --kubeconfig "${KUBECONFIG}" --wait 180s >&2 || fail kind_create "kind could not create the cluster"

  # PostgreSQL of the kind probe (the same manifest and pinned image); the password is generated here and goes to files only
  local secrets="${WORK}/secrets"
  mkdir -m 700 "${secrets}"
  ( umask 077
    python3 -I -c 'import secrets; print(secrets.token_hex(24), end="")' > "${secrets}/password"
    printf 'postgres://myriarch:%s@postgres.%s.svc:5432/myriarch?sslmode=disable' "$(cat "${secrets}/password")" "${ns_infra}" > "${secrets}/database-dsn" )
  kc create namespace "${ns_infra}" >&2 || fail postgres_install "cannot create the namespace ${ns_infra}"
  kc -n "${ns_infra}" create secret generic postgres-password "--from-file=password=${secrets}/password" >/dev/null || fail postgres_install "cannot create the PostgreSQL secret"
  sed -e "s#@@POSTGRES_IMAGE@@#${postgres_image}#g" "${kind_dir}/manifests/postgres.yaml" | kc apply -f - >/dev/null ||
    fail postgres_install "cannot apply hack/kind/manifests/postgres.yaml"
  kc -n "${ns_infra}" rollout status statefulset/postgres --timeout=300s >&2 || fail postgres_install "PostgreSQL did not become ready"

  # the namespace enforces the restricted Pod Security level: a chart that does not comply cannot start
  kc apply -f - >/dev/null <<EOF || fail chart_install "cannot create the namespace ${ns_app}"
apiVersion: v1
kind: Namespace
metadata:
  name: ${ns_app}
  labels:
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
EOF
  kc -n "${ns_app}" create secret generic myriarch-secrets "--from-file=database-dsn=${secrets}/database-dsn" >/dev/null ||
    fail chart_install "cannot create the Secret myriarch-secrets"

  # the chart of the tag with only the required values: the default image (repository and tag = appVersion), pulled anonymously
  log "installing the chart of ${version} from this checkout"
  "${TOOLS_BIN}/helm" install myriarch "${chart}" -n "${ns_app}" --kube-context "${KUBE_CONTEXT}" \
    --set "config.server.publicUrl=https://fleet.example.test" >"${WORK}/notes.txt" 2>"${WORK}/helm-err.txt" ||
    fail chart_install "helm install failed: $(last_line "${WORK}/helm-err.txt")"
  kc -n "${ns_app}" rollout status deploy/myriarch --timeout=300s >&2 || fail rollout "the deployment myriarch did not become ready"

  # every pod: non-root with the effective security context, and the image of this release
  kc -n "${ns_app}" get pods -l app.kubernetes.io/instance=myriarch -o json >"${WORK}/pods.json" || fail pod_security "cannot list the pods"
  python3 -I - "${WORK}/pods.json" "${ID_IMAGE_SERVER}:${version}" "${server_index}" "${server_amd64}" <<'PY' ||
import json, sys

path, image, *digests = sys.argv[1:]
items = json.load(open(path, encoding="utf-8")).get("items", [])

def bad(msg):
    print("[smoke] ERROR [pod_security] " + msg, file=sys.stderr)
    sys.exit(1)

if not items:
    bad("no pod of the release found")
for pod in items:
    name = pod["metadata"]["name"]
    if pod.get("status", {}).get("phase") != "Running":
        bad("pod %s is not running" % name)
    pod_sc = pod["spec"].get("securityContext") or {}
    containers = pod["spec"].get("initContainers", []) + pod["spec"]["containers"]
    for c in containers:
        sc = c.get("securityContext") or {}
        non_root = sc.get("runAsNonRoot", pod_sc.get("runAsNonRoot"))
        user = sc.get("runAsUser", pod_sc.get("runAsUser"))
        if non_root is not True or user != 65532:
            bad("container %s of pod %s: runAsNonRoot=%s runAsUser=%s, expected true and 65532" % (c["name"], name, non_root, user))
    statuses = pod["status"].get("containerStatuses", []) + pod["status"].get("initContainerStatuses", [])
    if not statuses:
        bad("pod %s reports no container status" % name)
    for s in statuses:
        if s.get("image") not in (image, "docker.io/" + image):
            bad("container %s of pod %s runs the image '%s', expected the chart default %s" % (s["name"], name, s.get("image"), image))
        image_id = s.get("imageID", "")
        if not any(image_id.endswith(d) for d in digests):
            bad("container %s of pod %s has the imageID '%s', expected the index digest or the amd64 digest of the release" % (s["name"], name, image_id))
PY
    fail pod_security "the pods of the release do not meet the contract"
  log "pods: non-root (65532), image ${ID_IMAGE_SERVER}:${version}, imageID is the release digest"

  # the server answers through a port-forward
  local port base
  port="$(free_port)"
  base="http://127.0.0.1:${port}"
  "${TOOLS_BIN}/kubectl" --context "${KUBE_CONTEXT}" -n "${ns_app}" port-forward deploy/myriarch "${port}:8080" --address 127.0.0.1 \
    >"${WORK}/port-forward.log" 2>&1 &
  PF_PID=$!
  wait_for_code "${base}/healthz" 200 "${SMOKE_WAIT_SECONDS}" || fail healthz "GET /healthz did not answer 200"
  wait_for_code "${base}/readyz" 200 "${SMOKE_WAIT_SECONDS}" || fail readyz "GET /readyz did not answer 200"
  log "healthz and readyz answer 200"

  # the bootstrap admin: the NOTES name the Secret that holds the one-time token (to a file, never printed)
  local notes_line re secret_ns secret_name secret_key
  notes_line="$(grep 'get secret' "${WORK}/notes.txt" | head -n 1 | sed 's/^ *//')" || true
  re="^kubectl (-n|--namespace) ([a-z0-9][a-z0-9.-]*) get secret ([a-z0-9][a-z0-9.-]*) -o jsonpath='\\{\\.data\\.([A-Za-z0-9_.-]+)\\}' \\| base64 (-d|--decode)$"
  [[ "${notes_line}" =~ ${re} ]] || fail bootstrap_login "the chart NOTES name no bootstrap token Secret in the expected form"
  secret_ns="${BASH_REMATCH[2]}"; secret_name="${BASH_REMATCH[3]}"; secret_key="${BASH_REMATCH[4]}"
  [ "${secret_ns}" = "${ns_app}" ] || fail bootstrap_login "the NOTES name the namespace '${secret_ns}', expected ${ns_app}"
  ( umask 077
    kc -n "${secret_ns}" get secret "${secret_name}" -o "jsonpath={.data.${secret_key}}" | base64 -d > "${secrets}/bootstrap-token" ) ||
    fail bootstrap_login "cannot read the bootstrap token Secret"
  [ -s "${secrets}/bootstrap-token" ] || fail bootstrap_login "the bootstrap token Secret is empty"
  local code
  code="$(python3 -I -c 'import json, sys; print(json.dumps({"token": open(sys.argv[1], encoding="utf-8").read().strip()}))' "${secrets}/bootstrap-token" |
    curl -s -m 20 -o /dev/null -w '%{http_code}' -c "${WORK}/cookies.txt" -H 'Content-Type: application/json' -d @- "${base}/api/v1/auth/bootstrap-login")" || code=""
  [ "${code}" = 200 ] || fail bootstrap_login "POST /api/v1/auth/bootstrap-login answered '${code:-no answer}', expected 200"
  log "bootstrap admin: sign-in ok"
  log "kind: ok"
}

# ---- main --------------------------------------------------------------------------------------------------------------
usage_all() {
  sed -n '/^# Smoke tests of a published release/,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'
}

case "${1:-}" in
  -h | --help | help) usage_all; exit 0 ;;
  assets) shift; cmd_assets "$@" ;;
  linux) shift; cmd_linux "$@" ;;
  image-run) shift; cmd_image_run "$@" ;;
  kind) shift; cmd_kind "$@" ;;
  *) usage_error "smoke.sh assets|linux|image-run|kind ... (see the header of ci/release/smoke.sh)" ;;
esac
