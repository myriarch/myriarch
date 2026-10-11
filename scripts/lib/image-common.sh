# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Shared helpers of the crane image scripts (scripts/build-image-crane.sh, scripts/build-collector-image-crane.sh,
# scripts/build-mirror-image-crane.sh). Sourced, not executed.
#
# The caller defines die/warn/log BEFORE sourcing this file (each script has its own message prefix) and ROOT (the
# repository root). The functions use only those and plain shell variables; nothing is exported.

# Package that receives the build metadata (-X). Defined once for both crane build scripts; must equal the module path of
# go.mod plus /internal/version (internal/updater/deploy_consistency_test.go checks this and every other definition).
# shellcheck disable=SC2034  # read by the sourcing scripts
VERSION_PKG=github.com/myriarch/myriarch/internal/version

# ---- OCI labels ---------------------------------------------------------------------------------------------------
# Reads ci/image-labels.env (IMAGE_LABELS_FILE overrides the path; for tests only) into the plain shell variables
# LABEL_VENDOR, LABEL_LICENSES, LABEL_LICENSES_MIRROR, LABEL_SOURCE, LABEL_TITLE, LABEL_TITLE_UPDATER, LABEL_TITLE_COLLECTOR
# and LABEL_TITLE_MIRROR.
image_labels_load() {
  local file="${IMAGE_LABELS_FILE:-${ROOT}/ci/image-labels.env}" line key value n=0 name
  [ -f "${file}" ] || die "label file ${file} not found"
  # shellcheck disable=SC2034  # read by the sourcing scripts
  LABEL_VENDOR="" LABEL_LICENSES="" LABEL_LICENSES_MIRROR="" LABEL_SOURCE="" LABEL_TITLE="" LABEL_TITLE_UPDATER="" LABEL_TITLE_COLLECTOR="" LABEL_TITLE_MIRROR=""
  while IFS= read -r line || [ -n "${line}" ]; do
    n=$((n + 1))
    case "${line}" in
      "" | "#"*) continue ;;
    esac
    [[ "${line}" =~ ^[A-Z_]+= ]] || die "${file}:${n}: expected KEY=value"
    key="${line%%=*}"
    value="${line#*=}"
    case "${key}" in
      LABEL_VENDOR | LABEL_LICENSES | LABEL_LICENSES_MIRROR | LABEL_SOURCE | LABEL_TITLE | LABEL_TITLE_UPDATER | LABEL_TITLE_COLLECTOR | LABEL_TITLE_MIRROR) ;;
      *) die "${file}:${n}: unknown key '${key}'" ;;
    esac
    case "${value}" in
      *,*) die "${file}:${n}: the value of ${key} must not contain a comma (crane parses --label as CSV)" ;;
    esac
    printf -v "${key}" '%s' "${value}"
  done < "${file}"
  for name in LABEL_VENDOR LABEL_LICENSES LABEL_LICENSES_MIRROR LABEL_TITLE LABEL_TITLE_UPDATER LABEL_TITLE_COLLECTOR LABEL_TITLE_MIRROR; do
    [ -n "${!name}" ] || die "${file}: ${name} is missing or empty"
  done
}

# ---- target platform ----------------------------------------------------------------------------------------------
# image_platform_check: validates IMAGE_PLATFORM (default linux/amd64; linux/arm64 is the other supported value) and sets
#   IMAGE_PLATFORM       the validated platform
#   IMAGE_GOARCH         GOARCH for the Go builds (amd64 | arm64)
#   IMAGE_ELF_MACHINE    e_machine of the ELF binaries (62 = x86-64, 183 = AArch64)
#   IMAGE_TAG_SUFFIX     "" for amd64, "-arm64" for arm64: the default tags carry it, so a build for another platform never
#                        overwrites the amd64 tag (these scripts push single-platform images, there is no index)
# Call it before any git, UI or build work.
# shellcheck disable=SC2034  # IMAGE_GOARCH, IMAGE_ELF_MACHINE and IMAGE_TAG_SUFFIX are read by the sourcing scripts
image_platform_check() {
  IMAGE_PLATFORM="${IMAGE_PLATFORM:-linux/amd64}"
  case "${IMAGE_PLATFORM}" in
    linux/amd64) IMAGE_GOARCH=amd64; IMAGE_ELF_MACHINE=62; IMAGE_TAG_SUFFIX="" ;;
    linux/arm64) IMAGE_GOARCH=arm64; IMAGE_ELF_MACHINE=183; IMAGE_TAG_SUFFIX="-arm64" ;;
    *) die "unsupported IMAGE_PLATFORM '${IMAGE_PLATFORM}' (linux/amd64 or linux/arm64)" ;;
  esac
}

# ---- repository and digest checks ---------------------------------------------------------------------------------
# image_require_repo <script-path> <example-repo>
image_require_repo() {
  local script="$1" example="$2"
  if [ -z "${IMAGE_REPO:-}" ]; then
    die "IMAGE_REPO is not set: name the target repository without tag, e.g. IMAGE_REPO=${example} ${script} (there is no default registry)"
  fi
  [[ "${IMAGE_REPO}" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$ ]] ||
    die "invalid IMAGE_REPO '${IMAGE_REPO}': expected <registry>[:port]/<path> without tag or digest"
}

# image_require_digest_pin <name> <ref>
image_require_digest_pin() {
  local name="$1" ref="$2"
  [[ "${ref}" =~ @sha256:[0-9a-f]{64}$ ]] ||
    die "${name} must be pinned by digest: <repo>:<tag>@sha256:<digest> (crane digest <repo>:<tag>)"
}

# ---- published repositories ---------------------------------------------------------------------------------------
# image_is_published_repo <repo>: success for the repositories of the project (ghcr.io/myriarch/..., any spelling). Only
# ci/release/push-images.sh, run by the release workflow, writes to them (ADR-0024, ADR-0029). The host and the first path
# segment are compared after the spellings of the same registry are normalised: upper case, a trailing dot of a fully
# qualified host name (ghcr.io.) and any port (ghcr.io:443).
image_is_published_repo() {
  local lower host rest
  lower="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  host="${lower%%/*}"
  rest="${lower#*/}"
  [ "${rest}" != "${lower}" ] || return 1
  host="${host%%:*}"
  host="${host%.}"
  [ "${host}" = ghcr.io ] || return 1
  case "${rest}" in
    myriarch | myriarch/*) return 0 ;;
  esac
  return 1
}

# ---- source URL ---------------------------------------------------------------------------------------------------
# image_source_url [dir]: prints the https URL for org.opencontainers.image.source, or nothing.
# Order: SOURCE_URL (as given), the normalised git remote "origin" of <dir> (default ROOT), LABEL_SOURCE.
image_source_url() {
  local dir="${1:-${ROOT}}" remote url=""
  if [ -n "${SOURCE_URL:-}" ]; then
    case "${SOURCE_URL}" in
      *,*) die "SOURCE_URL must not contain a comma (crane parses --label as CSV)" ;;
    esac
    printf '%s\n' "${SOURCE_URL}"
    return 0
  fi
  remote="$(git -C "${dir}" remote get-url origin 2>/dev/null || true)"
  if [ -n "${remote}" ]; then
    # generic rules only: ssh://user@host[:port]/path, scp-like user@host:path, https://user[:pass]@host/path
    url="$(printf '%s' "${remote}" | sed -E \
      -e 's#^ssh://[^@/]+@([^/:]+)(:[0-9]+)?/#https://\1/#' \
      -e 's#^ssh://([^@/:]+)(:[0-9]+)?/#https://\1/#' \
      -e 's#^[A-Za-z0-9._-]+@([^:/]+):#https://\1/#' \
      -e 's#^https://[^/@]+@#https://#' \
      -e 's#/+$##' -e 's#\.git$##' -e 's#/+$##')"
    case "${url}" in
      https://?*) ;;
      *) url="" ;;
    esac
  fi
  if [ -z "${url}" ] && [ -n "${LABEL_SOURCE:-}" ]; then url="${LABEL_SOURCE}"; fi
  if [ -n "${url}" ]; then printf '%s\n' "${url}"; fi
  return 0
}

# ---- registry credentials -----------------------------------------------------------------------------------------
# crane reads $DOCKER_CONFIG/config.json (default ~/.docker). The scripts never assign a default: they only prepare the
# directory when the caller points DOCKER_CONFIG somewhere.
image_docker_config_prepare() {
  if [ -n "${DOCKER_CONFIG:-}" ]; then
    mkdir -p "${DOCKER_CONFIG}" && chmod 700 "${DOCKER_CONFIG}"
  fi
}

image_docker_config_desc() {
  if [ -n "${DOCKER_CONFIG:-}" ]; then printf '%s' "${DOCKER_CONFIG}"; else printf '%s' "crane default (~/.docker)"; fi
}
