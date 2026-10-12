#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Myriarch - installs the OpAMP supervisor + OpenTelemetry Collector Contrib (Linux, systemd)
#
# Installs (idempotent; a repeated run brings the host to the requested state):
#   - service user "otelcol" (no login)
#   - collector (otelcol-contrib .tar.gz, SHA-256 verified) into /opt/myriarch/collector/versions/<v>
#     with a "current" symlink (myriarch-updater swaps this symlink atomically on updates)
#   - OpAMP supervisor (bare binary, SHA-256 verified) into /opt/myriarch/supervisor
#   - /etc/myriarch/{supervisor.yaml,fallback.yaml,myriarch.env,enrollment-token}
#   - systemd units otelcol-supervisor (+ optionally myriarch-updater)
#
# The collector deb/rpm packages are deliberately NOT used: the contrib package starts a service of
# its own immediately.
#
# The supervisor storage directory (/var/lib/otelcol/supervisor) is only created empty, never
# pre-populated: persistent_state.yaml holds the instance UID. Golden images must empty it BEFORE
# cloning (otherwise all clones share one identity).
#
# Example:
#   sudo ./install-linux.sh --opamp-endpoint wss://fleet.example.com/v1/opamp \
#       --token myre_xxx --site-type datacenter --site-id dc-01 --site-name 'Data center 1' \
#       --site-country NL --site-timezone Europe/Amsterdam --environment prod \
#       --collector-version 0.162.0 --with-updater \
#       --updater-url https://fleet.example.com/downloads/myriarch-updater_linux_amd64
#
# The templates live under deploy/ (--templates-dir, default: the directory above this script).
#
# Trust model: all downloads use https only (redirects included). Collector and supervisor are
# verified with SHA-256 AND a Sigstore bundle before they are executed (default: require; the
# verifier is cosign 3.0.4 or newer, or myriarch-updater verify-bundle). The myriarch-updater (the
# project's own binary, runs as root) needs a trusted hash source, at least one of
#   --updater-sha256 HEX                    (hash from another trusted source, for example verified on an admin host)
#   --updater-checksums-url URL             (checksums.txt of the release; its keyless Sigstore bundle, by default
#                                            URL.sigstore.json or --updater-bundle-url, is verified against the release
#                                            identity of the requested version before the hash is read: the line
#                                            whose file name matches the one in --updater-url)
# The version comes from --updater-version or from the file name myriarch-updater_<version>_linux_<arch> in
# --updater-url; when both are given they must agree. The verifier of checksums.txt is cosign 3.0.4 or newer or an
# already installed myriarch-updater (--verifier or /opt/myriarch/updater), never the binary that is being installed.
# Both hash sources together are allowed (then both must match). The check of checksums.txt also applies with
# --insecure, but not with --verify-signatures skip. --insecure disables the remaining checks for lab/tests only.
#
# Retries: a download that fails transiently (a timeout, a failed connection, HTTP 408, 429, 500, 502, 503 or 504) is
# retried with pauses of 1, 2, 4, ... s; no attempt starts later than 2 minutes after the first one. Any other failure,
# a 404 for example, ends the installation at once. Every check above stays as strict as without retries.
#
# ---- Error code catalog (stable interface; install-windows.ps1 uses the same codes) ----------------
# Every fatal error prints exactly one line to stderr and exits with status 1:
#   [myriarch-install] ERROR [<code>] <message>
# Warnings print:
#   [myriarch-install] WARNING [<code>] <message>
# Info lines keep the form "[myriarch-install] <message>". Codes are lower snake case
# (^[a-z][a-z0-9_]*$); tools and tests match the code, never the message text.
#
#   usage_error                     unknown option, or an option without a value
#   option_missing                  a required option is absent (site type/id, collector version, token or
#                                   token file, --updater-url with --with-updater)
#   option_invalid                  malformed value (versions, arch, verify-signatures mode, SHA-256 format,
#                                   control characters, token characters, endpoint scheme, no file name
#                                   in --updater-url; a non-https --release-base-url raises https_required)
#   option_removed                  a removed option (--signing-key, --updater-checksums-sig-url); the message
#                                   names the replacement
#   option_conflict                 mutually exclusive or dependent options (--token + --token-file;
#                                   updater options without --with-updater; --updater-bundle-url without
#                                   checksums URL; --updater-version and the version in --updater-url differ;
#                                   --updater-checksums-url with --verify-signatures skip)
#   updater_hash_source_missing     --with-updater without --updater-sha256 or --updater-checksums-url
#   insecure_required               plain ws/http endpoint or --verify-signatures skip without --insecure
#   https_required                  non-https download URL (or redirect) without --insecure
#   file_unreadable                 token file, CA file, trusted root or verifier is unusable
#   tool_missing                    a required tool is absent (sha256sum/shasum, curl/wget, tar, gzip, install,
#                                   cmp, systemctl, useradd/adduser); the message names the tool
#   sigstore_verifier_missing       no Sigstore verifier (cosign or an installed myriarch-updater)
#   sigstore_verifier_outdated      cosign is older than 3.0.4 or reports no usable version
#   privilege_required              not running as root
#   unsupported_platform            not Linux, or unsupported CPU architecture
#   download_failed                 a download did not succeed (a permanent error at once, a transient one after the
#                                   retry window of 120 s; the message gives the number of attempts)
#   template_missing                template directory or template file not found
#   template_placeholder            unreplaced @@PLACEHOLDER@@ left in a rendered template
#   checksum_invalid                malformed .sha256 file or checksums.txt entry
#   checksum_mismatch               SHA-256 of a downloaded file differs (the message names the file)
#   checksum_entry_count            checksums.txt has N != 1 lines for the asset (the message contains N)
#   checksum_conflict               --updater-sha256 contradicts the signed checksums.txt
#   sigstore_verification_failed    Sigstore bundle verification failed (wrong identity or version, tampered file)
#   archive_invalid                 collector archive has no regular binary in its root
#   collector_start_failed          the new collector binary cannot be started
#   collector_version_mismatch      the collector reports another version than requested
#   service_not_running             WARNING: otelcol-supervisor is not running after the installation
#   token_on_command_line           WARNING: --token is visible in the process list and shell history
#   updater_unauthenticated         WARNING: the updater binary has no trusted hash (--insecure)
#
# Windows-only codes (see install-windows.ps1 and deploy/windows/README.md): authenticode_invalid,
# directory_unsafe, acl_failed, service_config_failed, updater_install_failed, group_membership_failed,
# unexpected_error; service_not_running is fatal there.
# ---- End of error code catalog ---------------------------------------------------------------------
set -euo pipefail

readonly DEFAULT_RELEASE_BASE_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download"
readonly COLLECTOR_ASSET_PREFIX="otelcol-contrib"
readonly SUPERVISOR_ASSET_PREFIX="opampsupervisor"
readonly CERT_ISSUER="https://token.actions.githubusercontent.com"
# Keyless identity of the Myriarch release workflow (ADR-0029, frozen from v0.1.0 on: release.yml is never renamed).
# The repository must equal internal/brand.Repository; a Go test compares both.
readonly RELEASE_REPOSITORY="myriarch/myriarch"
readonly MIN_COSIGN_VERSION="3.0.4"
readonly SERVICE_USER="otelcol"

# bash >= 5.2 expands "&" in ${var//pat/repl}; turn that off so values stay literal
shopt -u patsub_replacement 2>/dev/null || true

OPAMP_ENDPOINT=""
TOKEN=""
TOKEN_FILE=""
TOKEN_FROM_ARGV=0
SITE_TYPE=""
SITE_ID=""
SITE_NAME=""
SITE_COUNTRY=""
SITE_REGION=""
SITE_TIMEZONE=""
ENVIRONMENT="prod"
COLLECTOR_VERSION=""
SUPERVISOR_VERSION=""
RELEASE_BASE_URL="${DEFAULT_RELEASE_BASE_URL}"
UPDATER_URL=""
UPDATER_SHA256=""
UPDATER_VERSION=""
UPDATER_CHECKSUMS_URL=""
UPDATER_BUNDLE_URL=""
WITH_UPDATER=0
UNINSTALL=0
PURGE=0
CA_FILE=""
TRUSTED_ROOT=""
VERIFY_SIGNATURES="require"   # require | skip (skip only with --insecure)
VERIFIER=""                # cosign (3.0.4 or newer) or an installed myriarch-updater binary for "verify-bundle"
INSECURE=0                 # allows http://, file:// and --verify-signatures skip (lab/tests only)
ARCH=""
ROOT_PREFIX=""             # staging mode (image build/tests): all paths under DIR, no useradd/systemctl
TEMPLATES_DIR=""

TMP_DIR=""

usage() {
  cat <<'USAGE'
Usage: install-linux.sh [options]

Required (installation):
  --opamp-endpoint URL      wss://fleet.example.com/v1/opamp (ws|wss|http|https)
  --token-file FILE         enrollment token (myre_...) read from a file (recommended: --token shows up in the
                            process list and in the shell history)   or   --token TOKEN
  --site-type TYPE          edge | datacenter | office | k8s-gateway | ...
  --site-id ID              site number (always a string, leading zeros are kept)
  --collector-version X.Y.Z collector version (without "v")

Optional:
  --site-name NAME  --site-country NL  --site-region REGION  --site-timezone Europe/Amsterdam
  --environment ENV         default: prod
  --supervisor-version X.Y.Z  default: same as --collector-version
  --release-base-url URL    default: GitHub; internal mirror with layout <base>/<tag>/<asset>
                            (collector tag vX.Y.Z, supervisor tag cmd/opampsupervisor/vX.Y.Z)
  --ca-file FILE            custom CA for myriarchd (written into supervisor.yaml/updater.yaml)
  --with-updater            install the myriarch-updater (requires --updater-url)
  --updater-url URL         download URL of the myriarch-updater binary (linux, matching architecture)
  --updater-sha256 HEX      expected SHA-256 of the updater binary
  --updater-version X.Y.Z   release version of the updater (default: the version in the file name
                            myriarch-updater_<version>_linux_<arch> of --updater-url; both must agree)
  --updater-checksums-url URL  checksums.txt of the myriarch release; the expected hash is the line whose file
                            name equals the last path segment of --updater-url. Its keyless Sigstore bundle is
                            verified against the release identity of the updater version first
  --updater-bundle-url URL  different location of that bundle (default: <checksums-url>.sigstore.json)
                            With --with-updater, --updater-sha256 OR --updater-checksums-url is mandatory;
                            both together are allowed (then both must match)
  --trusted-root FILE       Sigstore trusted_root.json for offline verification (installer and myriarch-updater)
  --verify-signatures MODE  require (default: the Sigstore bundle must be valid) | skip (only with --insecure)
  --verifier FILE           cosign (3.0.4 or newer) or an installed myriarch-updater for "verify-bundle" (default: cosign
                            in PATH, then /opt/myriarch/updater/myriarch-updater; with --updater-sha256 and without
                            --updater-checksums-url also the updater downloaded here and checked by that hash)
  --insecure                lab/tests: allows http://, file:// and --verify-signatures skip
  --arch amd64|arm64        default: derived from uname -m
  --templates-dir DIR       directory with supervisor/, systemd/, updater/ (default: <script>/..)
  --root DIR                staging mode: install everything under DIR, without users/systemd
  --uninstall               remove services and files (data in /var/lib/otelcol is kept)
  --purge                   with --uninstall: also remove /var/lib/otelcol and the user
  -h, --help
USAGE
}

log()  { printf '[myriarch-install] %s\n' "$*"; }
# warn <code> <message>   /   die <code> <message>: the code is a stable interface (see the catalog above)
warn() { local code="$1"; shift; printf '[myriarch-install] WARNING [%s] %s\n' "${code}" "$*" >&2; }
die()  { local code="$1"; shift; printf '[myriarch-install] ERROR [%s] %s\n' "${code}" "$*" >&2; exit 1; }

cleanup() {
  if [ -n "${TMP_DIR}" ] && [ -d "${TMP_DIR}" ]; then
    rm -rf "${TMP_DIR}"
  fi
}
trap cleanup EXIT

# ---------------------------------------------------------------- Arguments
need_arg() { [ "$#" -ge 2 ] && [ -n "$2" ] || die usage_error "option $1 requires a value"; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --opamp-endpoint)     need_arg "$@"; OPAMP_ENDPOINT="$2"; shift 2 ;;
    --token)              need_arg "$@"; TOKEN="$2"; TOKEN_FROM_ARGV=1; shift 2 ;;
    --token-file)         need_arg "$@"; TOKEN_FILE="$2"; shift 2 ;;
    --site-type)          need_arg "$@"; SITE_TYPE="$2"; shift 2 ;;
    --site-id)            need_arg "$@"; SITE_ID="$2"; shift 2 ;;
    --site-name)          need_arg "$@"; SITE_NAME="$2"; shift 2 ;;
    --site-country)       need_arg "$@"; SITE_COUNTRY="$2"; shift 2 ;;
    --site-region)        need_arg "$@"; SITE_REGION="$2"; shift 2 ;;
    --site-timezone)      need_arg "$@"; SITE_TIMEZONE="$2"; shift 2 ;;
    --environment)        need_arg "$@"; ENVIRONMENT="$2"; shift 2 ;;
    --collector-version)  need_arg "$@"; COLLECTOR_VERSION="${2#v}"; shift 2 ;;
    --supervisor-version) need_arg "$@"; SUPERVISOR_VERSION="${2#v}"; shift 2 ;;
    --release-base-url)   need_arg "$@"; RELEASE_BASE_URL="${2%/}"; shift 2 ;;
    --updater-url)        need_arg "$@"; UPDATER_URL="$2"; shift 2 ;;
    --updater-sha256)     need_arg "$@"; UPDATER_SHA256="$2"; shift 2 ;;
    --updater-version)    need_arg "$@"; UPDATER_VERSION="${2#v}"; shift 2 ;;
    --updater-checksums-url) need_arg "$@"; UPDATER_CHECKSUMS_URL="$2"; shift 2 ;;
    --updater-bundle-url) need_arg "$@"; UPDATER_BUNDLE_URL="$2"; shift 2 ;;
    --signing-key)        die option_removed "--signing-key was removed: releases are signed keyless. Pass --updater-checksums-url (its Sigstore bundle is verified against the release identity, verifier: cosign 3.0.4 or newer or an installed myriarch-updater) or --updater-sha256" ;;
    --updater-checksums-sig-url) die option_removed "--updater-checksums-sig-url was removed: use --updater-bundle-url (default: <checksums-url>.sigstore.json) with --updater-checksums-url" ;;
    --with-updater)       WITH_UPDATER=1; shift ;;
    --ca-file)            need_arg "$@"; CA_FILE="$2"; shift 2 ;;
    --trusted-root)       need_arg "$@"; TRUSTED_ROOT="$2"; shift 2 ;;
    --verify-signatures)  need_arg "$@"; VERIFY_SIGNATURES="$2"; shift 2 ;;
    --verifier)           need_arg "$@"; VERIFIER="$2"; shift 2 ;;
    --insecure)           INSECURE=1; shift ;;
    --arch)               need_arg "$@"; ARCH="$2"; shift 2 ;;
    --templates-dir)      need_arg "$@"; TEMPLATES_DIR="$2"; shift 2 ;;
    --root)               need_arg "$@"; ROOT_PREFIX="${2%/}"; shift 2 ;;
    --uninstall)          UNINSTALL=1; shift ;;
    --purge)              PURGE=1; shift ;;
    -h|--help)            usage; exit 0 ;;
    *)                    usage >&2; die usage_error "unknown option: $1" ;;
  esac
done

# ---------------------------------------------------------------- Paths
P_OPT="${ROOT_PREFIX}/opt/myriarch"
P_COLLECTOR="${P_OPT}/collector"
P_SUPERVISOR="${P_OPT}/supervisor"
P_UPDATER="${P_OPT}/updater"
P_ETC="${ROOT_PREFIX}/etc/myriarch"
P_VAR="${ROOT_PREFIX}/var/lib/otelcol"
P_UNITS="${ROOT_PREFIX}/etc/systemd/system"

STAGING_MODE=0
[ -n "${ROOT_PREFIX}" ] && STAGING_MODE=1

# ---------------------------------------------------------------- Helpers
have() { command -v "$1" >/dev/null 2>&1; }

sha256_of() {
  if have sha256sum; then sha256sum "$1" | awk '{print $1}'
  elif have shasum; then shasum -a 256 "$1" | awk '{print $1}'
  else die tool_missing "neither sha256sum nor shasum found"
  fi
}

# Certificate identity (SAN) of the Sigstore bundle of one release asset, bound to the requested version:
# the tag in the identity must equal v<version>, so that a genuine but older release served from a mirror
# is not accepted as the requested version. workflow: base-release (collector) or base-binary-release (supervisor).
cert_identity() { # workflow version
  local workflow="$1" version="$2" esc
  esc="$(printf '%s' "${version}" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  printf '^https://github\\.com/open-telemetry/opentelemetry-collector-releases/\\.github/workflows/%s\\.yaml@refs/tags/v%s$' "${workflow}" "${esc}"
}

# Certificate identity (SAN) of the Sigstore bundle of checksums.txt of one Myriarch release, bound to the version:
# the workflow release.yml of this repository, run for the tag v<version>. The tag is escaped and the pattern is
# anchored at both ends, so that v0.1.0 does not accept v0.1.01, v10.1.0 or another workflow or repository.
cert_identity_release() { # version
  local esc repo
  esc="$(printf '%s' "$1" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  repo="$(printf '%s' "${RELEASE_REPOSITORY}" | sed -e 's/[][\\.*+?(){}|^$]/\\&/g')"
  printf '^https://github\\.com/%s/\\.github/workflows/release\\.yml@refs/tags/v%s$' "${repo}" "${esc}"
}

# https only (also after redirects); --insecure allows lab sources.
require_https() { # url what
  case "$1" in
    https://?*) ;;
    *) [ "${INSECURE}" = 1 ] || die https_required "$2 must be an https:// URL (--insecure only for lab/tests): $1" ;;
  esac
}

# ---- Download retry policy -------------------------------------------------------------------------
# One policy for install-linux.sh, install-windows.ps1, ci/release/smoke.sh and ci/release/smoke-windows.ps1. Only a transient
# failure is retried: a timeout, a failed connection (refused, reset, name resolution) or the HTTP status 408, 429, 500, 502,
# 503 or 504. Everything else (404, 403, a TLS or certificate error, a refused redirect, a local write error) fails at once.
# The pause starts at 1 s and doubles; no attempt starts later than DOWNLOAD_RETRY_SECONDS after the first one. The loop is
# ours and not curl --retry: it covers refused and reset connections, it knows the number of attempts for the final message,
# and the other three scripts can follow the same rules.
readonly DOWNLOAD_RETRY_SECONDS=120

# download_transient CURL_EXIT HTTP_STATUS: succeeds when a failed curl attempt is worth another one
download_transient() {
  case "$1" in
    5 | 6 | 7 | 16 | 18 | 28 | 52 | 55 | 56 | 92) return 0 ;;
    22) case "$2" in 408 | 429 | 500 | 502 | 503 | 504) return 0 ;; esac ;;
  esac
  return 1
}

download() { # url dest
  local url="$1" dest="$2"
  require_https "${url}" "download URL"
  log "downloading ${url}"
  if have curl; then
    local proto=(--proto '=https' --proto-redir '=https' --tlsv1.2)
    [ "${INSECURE}" = 1 ] && proto=()
    local attempt=0 pause=1 start="${SECONDS}" elapsed rc status reason noun
    while :; do
      attempt=$((attempt + 1))
      noun=attempts
      [ "${attempt}" = 1 ] && noun=attempt
      rc=0
      status="$(curl --fail --silent --show-error --location ${proto[@]+"${proto[@]}"} --connect-timeout 20 --max-time 900 \
        --write-out '%{http_code}' -o "${dest}" "${url}")" || rc=$?
      [ "${rc}" = 0 ] && return 0
      if [ "${rc}" = 22 ]; then reason="HTTP ${status}"; else reason="curl exit code ${rc}"; fi
      download_transient "${rc}" "${status}" \
        || die download_failed "download failed: ${url} (${reason}; not retried, ${attempt} ${noun})"
      elapsed=$((SECONDS - start))
      [ "${elapsed}" -lt "${DOWNLOAD_RETRY_SECONDS}" ] \
        || die download_failed "download failed: ${url} (${reason}; retry window of ${DOWNLOAD_RETRY_SECONDS} s used up, ${attempt} ${noun})"
      # the last pause ends exactly at the end of the window
      [ $((elapsed + pause)) -le "${DOWNLOAD_RETRY_SECONDS}" ] || pause=$((DOWNLOAD_RETRY_SECONDS - elapsed))
      log "attempt ${attempt} for ${url} failed (${reason}), next attempt in ${pause} s"
      sleep "${pause}"
      pause=$((pause * 2))
    done
  elif [ "${INSECURE}" = 1 ] && have wget; then
    # lab use only: wget cannot restrict redirects to https (see check_prereqs); the one exception to the retry policy above,
    # wget keeps its own three tries
    local limit=()
    have timeout && limit=(timeout 900)
    ${limit[@]+"${limit[@]}"} wget --quiet --tries=3 --timeout=20 --secure-protocol=TLSv1_2 -O "${dest}" "${url}" || die download_failed "download failed: ${url}"
  else
    die tool_missing "curl is required (wget cannot restrict redirects to https; it is accepted only with --insecure)"
  fi
}

# Escape a value for YAML double quotes (\ and ")
yaml_escape() {
  local v="$1"
  v="${v//\\/\\\\}"
  v="${v//\"/\\\"}"
  printf '%s' "${v}"
}

# Render a template: replaces @@KEY@@ from the given KEY=VALUE pairs; lines with a placeholder
# whose value is empty are removed. If CA_FILE is empty, the "tls:" line is dropped as well.
render_template() { # template out KEY=VALUE...
  local template="$1" out="$2"; shift 2
  [ -f "${template}" ] || die template_missing "template not found: ${template}"
  local tmp="${out}.new.$$"
  local line key val pair skip
  local ca_empty=0
  for pair in "$@"; do
    [ "${pair%%=*}" = "CA_FILE" ] && [ -z "${pair#*=}" ] && ca_empty=1
  done
  : > "${tmp}"
  while IFS= read -r line || [ -n "${line}" ]; do
    skip=0
    if [ "${ca_empty}" = 1 ] && [[ "${line}" =~ ^[[:space:]]{2}tls:[[:space:]]*$ ]]; then
      continue
    fi
    for pair in "$@"; do
      key="${pair%%=*}"; val="${pair#*=}"
      if [[ "${line}" == *"@@${key}@@"* ]]; then
        if [ -z "${val}" ]; then skip=1; break; fi
        line="${line//@@${key}@@/$(yaml_escape "${val}")}"
      fi
    done
    [ "${skip}" = 1 ] && continue
    printf '%s\n' "${line}" >> "${tmp}"
  done < "${template}"
  if grep -q '@@[A-Z_]*@@' "${tmp}"; then
    local left; left="$(grep -o '@@[A-Z_]*@@' "${tmp}" | sort -u | tr '\n' ' ')"
    rm -f "${tmp}"
    die template_placeholder "unreplaced placeholders in ${template}: ${left}"
  fi
  mv -f "${tmp}" "${out}"
}

# Write/replace a file only if its content changes; return status 0 = changed
install_if_changed() { # src dest mode
  local src="$1" dest="$2" mode="$3"
  if [ -f "${dest}" ] && cmp -s "${src}" "${dest}"; then
    chmod "${mode}" "${dest}"
    return 1
  fi
  if [ -f "${dest}" ]; then cp -p "${dest}" "${dest}.bak"; fi
  install -m "${mode}" "${src}" "${dest}"
  return 0
}

atomic_symlink() { # target link
  local target="$1" link="$2" tmp="$2.new.$$"
  rm -f "${tmp}"
  ln -s "${target}" "${tmp}"
  if mv -T "${tmp}" "${link}" 2>/dev/null; then return 0; fi   # GNU mv: atomic rename()
  rm -f "${link}" && mv "${tmp}" "${link}"                      # BSD/macOS (tests)
}

set_owner() { # owner:group paths...
  [ "${STAGING_MODE}" = 1 ] && return 0
  local og="$1"; shift
  chown "${og}" "$@"
}

validate_inputs() {
  case "${OPAMP_ENDPOINT}" in
    wss://?*|https://?*) ;;
    ws://?*|http://?*)
      [ "${INSECURE}" = 1 ] || die insecure_required "--opamp-endpoint without TLS (ws://, http://) is not allowed: the token would cross the network in clear text (--insecure only for lab/tests)" ;;
    *) die option_invalid "--opamp-endpoint must start with wss:// or https://" ;;
  esac
  [ -n "${SITE_TYPE}" ] || die option_missing "--site-type is missing"
  [ -n "${SITE_ID}" ]   || die option_missing "--site-id is missing"
  [ -n "${COLLECTOR_VERSION}" ] || die option_missing "--collector-version is missing"
  [[ "${COLLECTOR_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || die option_invalid "invalid --collector-version: ${COLLECTOR_VERSION}"
  [ -n "${SUPERVISOR_VERSION}" ] || SUPERVISOR_VERSION="${COLLECTOR_VERSION}"
  [[ "${SUPERVISOR_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || die option_invalid "invalid --supervisor-version: ${SUPERVISOR_VERSION}"
  local v
  for v in "${SITE_TYPE}" "${SITE_ID}" "${SITE_NAME}" "${SITE_COUNTRY}" "${SITE_REGION}" "${SITE_TIMEZONE}" "${ENVIRONMENT}" "${OPAMP_ENDPOINT}" "${CA_FILE}"; do
    if [[ "${v}" =~ [[:cntrl:]] ]]; then die option_invalid "control character in a parameter value"; fi
  done
  if [ -n "${TOKEN}" ] && [ -n "${TOKEN_FILE}" ]; then die option_conflict "--token and --token-file are mutually exclusive"; fi
  if [ -n "${TOKEN_FILE}" ]; then
    [ -r "${TOKEN_FILE}" ] || die file_unreadable "token file is not readable: ${TOKEN_FILE}"
    TOKEN="$(tr -d '\r\n[:space:]' < "${TOKEN_FILE}")"
  fi
  [ -n "${TOKEN}" ] || die option_missing "--token or --token-file is missing"
  [[ "${TOKEN}" =~ ^[A-Za-z0-9_.~+/=-]+$ ]] || die option_invalid "the token contains forbidden characters"
  if [ -n "${CA_FILE}" ]; then
    case "${CA_FILE}" in /*) ;; *) die option_invalid "--ca-file must be an absolute path" ;; esac
    [ -r "${CA_FILE}" ] || die file_unreadable "CA file is not readable: ${CA_FILE}"
  fi
  if [ -n "${TRUSTED_ROOT}" ]; then [ -r "${TRUSTED_ROOT}" ] || die file_unreadable "trusted_root.json is not readable: ${TRUSTED_ROOT}"; fi
  case "${VERIFY_SIGNATURES}" in
    require) ;;
    skip) [ "${INSECURE}" = 1 ] || die insecure_required "--verify-signatures skip is only allowed together with --insecure (lab/tests)" ;;
    *) die option_invalid "--verify-signatures must be require or skip" ;;
  esac
  if [ -n "${VERIFIER}" ]; then [ -x "${VERIFIER}" ] || die file_unreadable "--verifier is not executable: ${VERIFIER}"; fi
  if [ -n "${UPDATER_VERSION}${UPDATER_CHECKSUMS_URL}${UPDATER_BUNDLE_URL}" ] && [ "${WITH_UPDATER}" = 0 ]; then
    die option_conflict "--updater-version/--updater-checksums-url/--updater-bundle-url only apply together with --with-updater"
  fi
  if [ "${WITH_UPDATER}" = 1 ]; then
    [ -n "${UPDATER_URL}" ] || die option_missing "--with-updater requires --updater-url"
    require_https "${UPDATER_URL}" "--updater-url"
    local updater_asset url_version=""
    updater_asset="${UPDATER_URL%%[?#]*}"; updater_asset="${updater_asset##*/}"
    [ -n "${updater_asset}" ] || die option_invalid "no file name in --updater-url: ${UPDATER_URL}"
    if [[ "${updater_asset}" =~ ^myriarch-updater_([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?)_linux_(amd64|arm64)$ ]]; then
      url_version="${BASH_REMATCH[1]}"
    fi
    if [ -n "${UPDATER_VERSION}" ]; then
      [[ "${UPDATER_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || die option_invalid "invalid --updater-version: ${UPDATER_VERSION}"
      if [ -n "${url_version}" ] && [ "${url_version}" != "${UPDATER_VERSION}" ]; then
        die option_conflict "--updater-version ${UPDATER_VERSION} and the version in --updater-url (${url_version}) differ"
      fi
    else
      UPDATER_VERSION="${url_version}"
    fi
    if [ -n "${UPDATER_BUNDLE_URL}" ] && [ -z "${UPDATER_CHECKSUMS_URL}" ]; then die option_conflict "--updater-bundle-url requires --updater-checksums-url"; fi
    if [ -n "${UPDATER_CHECKSUMS_URL}" ]; then
      [ "${VERIFY_SIGNATURES}" = require ] || die option_conflict "--updater-checksums-url cannot be combined with --verify-signatures skip: checksums.txt would not be authenticated (pass --updater-sha256 instead)"
      [ -n "${UPDATER_VERSION}" ] || die option_missing "--updater-version is missing: the file name in --updater-url does not carry the version (myriarch-updater_<version>_linux_<arch>) and the identity of the release is bound to it"
      require_https "${UPDATER_CHECKSUMS_URL}" "--updater-checksums-url"
      [ -n "${UPDATER_BUNDLE_URL}" ] || UPDATER_BUNDLE_URL="${UPDATER_CHECKSUMS_URL}.sigstore.json"
      require_https "${UPDATER_BUNDLE_URL}" "--updater-bundle-url"
    fi
    if [ -z "${UPDATER_SHA256}" ] && [ -z "${UPDATER_CHECKSUMS_URL}" ] && [ "${INSECURE}" = 0 ]; then
      die updater_hash_source_missing "--with-updater requires --updater-sha256 or --updater-checksums-url - the binary runs as root and needs a trusted hash source (checksums.txt of the release, verified with its Sigstore bundle)"
    fi
    if [ -n "${UPDATER_SHA256}" ] && ! [[ "${UPDATER_SHA256}" =~ ^[0-9a-fA-F]{64}$ ]]; then die option_invalid "--updater-sha256 is not a SHA-256 (64 hex characters)"; fi
  fi
  require_https "${RELEASE_BASE_URL}" "--release-base-url"
  if [ "${TOKEN_FROM_ARGV}" = 1 ]; then
    warn token_on_command_line "--token is visible in the process list and the shell history; prefer --token-file"
  fi
}

detect_arch() {
  if [ -z "${ARCH}" ]; then
    case "$(uname -m)" in
      x86_64|amd64)  ARCH="amd64" ;;
      aarch64|arm64) ARCH="arm64" ;;
      *) die unsupported_platform "unsupported architecture: $(uname -m) (amd64/arm64 only; set --arch)" ;;
    esac
  fi
  case "${ARCH}" in amd64|arm64) ;; *) die option_invalid "--arch must be amd64 or arm64" ;; esac
}

check_prereqs() {
  if [ "${STAGING_MODE}" = 0 ]; then
    [ "$(id -u)" = 0 ] || die privilege_required "run as root (sudo)"
    [ "$(uname -s)" = "Linux" ] || die unsupported_platform "only Linux is supported"
    have systemctl || die tool_missing "systemd (systemctl) not found"
    have useradd || have adduser || die tool_missing "useradd/adduser not found"
  fi
  have tar || die tool_missing "tar not found"
  # GNU tar runs the external gzip for -z; without it the extraction fails and would be reported as a missing binary
  have gzip || die tool_missing "gzip not found (tar needs it to unpack .tar.gz archives)"
  have install || die tool_missing "install not found"
  have cmp || die tool_missing "cmp not found"
  have curl || { [ "${INSECURE}" = 1 ] && have wget; } || die tool_missing "curl is required (wget cannot restrict redirects to https; it is accepted only with --insecure)"
  if [ -z "${TEMPLATES_DIR}" ]; then
    TEMPLATES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  fi
  [ -d "${TEMPLATES_DIR}/supervisor/linux" ] || die template_missing "templates not found under ${TEMPLATES_DIR} (--templates-dir)"
}

# ---------------------------------------------------------------- Signature verification (Sigstore, fail-closed)
# Verifier: cosign 3.0.4 or newer (PATH or --verifier) or "myriarch-updater verify-bundle" (--verifier, an already
# installed one, or - only with --updater-sha256 and without --updater-checksums-url - the updater downloaded here and
# checked by that hash). With --updater-checksums-url the verifier is resolved BEFORE the updater is downloaded and the
# downloaded updater is never used: its hash comes from the file that the verifier has to check. Without a verifier the
# installation aborts, except with --insecure --verify-signatures skip.

# Succeeds when the cosign binary reports a version of MIN_COSIGN_VERSION or newer; otherwise prints the reason.
cosign_version_problem() { # path
  local out ver
  out="$("$1" version 2>&1)" || { printf 'cosign "%s version" failed' "$1"; return 1; }
  ver="$(printf '%s\n' "${out}" | awk '/^GitVersion:/ { print $2; exit }')"
  ver="${ver#v}"
  if ! [[ "${ver}" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-[^+]+)?(\+.*)?$ ]]; then
    printf 'cosign "%s" reports no usable version' "$1"; return 1
  fi
  local want="${MIN_COSIGN_VERSION}" got_major="${BASH_REMATCH[1]}" got_minor="${BASH_REMATCH[2]}" got_patch="${BASH_REMATCH[3]}" got_pre="${BASH_REMATCH[4]}"
  local want_major="${want%%.*}" rest="${want#*.}"
  local want_minor="${rest%%.*}" want_patch="${rest#*.}"
  # a pre-release of the minimum (3.0.4-rc.1) sorts before it
  if [ -n "${got_pre}" ] && [ "${got_major}.${got_minor}.${got_patch}" = "${want}" ]; then
    printf 'cosign %s.%s.%s%s at "%s" is too old (a pre-release of the minimum)' "${got_major}" "${got_minor}" "${got_patch}" "${got_pre}" "$1"
    return 1
  fi
  if [ "${got_major}" -gt "${want_major}" ] \
    || { [ "${got_major}" -eq "${want_major}" ] && [ "${got_minor}" -gt "${want_minor}" ]; } \
    || { [ "${got_major}" -eq "${want_major}" ] && [ "${got_minor}" -eq "${want_minor}" ] && [ "${got_patch}" -ge "${want_patch}" ]; }; then
    return 0
  fi
  printf 'cosign %s.%s.%s at "%s" is too old' "${got_major}" "${got_minor}" "${got_patch}" "$1"
  return 1
}

# Any binary whose name starts with "cosign" (cosign, cosign-linux-amd64, ...) is treated as cosign; everything else is
# called as "myriarch-updater verify-bundle".
is_cosign() { # path
  case "$(basename "$1")" in cosign*) return 0 ;; *) return 1 ;; esac
}

die_cosign_outdated() { # reason
  die sigstore_verifier_outdated "$1: cosign ${MIN_COSIGN_VERSION} or newer is required (older versions have known verification flaws); update cosign, pass --verifier <installed myriarch-updater>, or verify checksums.txt on an admin host and pass --updater-sha256"
}

resolve_verifier() {
  [ "${VERIFY_SIGNATURES}" = skip ] && return 0
  local problem=""
  if [ -n "${VERIFIER}" ]; then
    if is_cosign "${VERIFIER}"; then
      problem="$(cosign_version_problem "${VERIFIER}")" || die_cosign_outdated "${problem}"
    fi
    return 0
  fi
  if have cosign; then
    if problem="$(cosign_version_problem "$(command -v cosign)")"; then VERIFIER="$(command -v cosign)"; return 0; fi
  fi
  if [ -x "${P_UPDATER}/myriarch-updater" ]; then VERIFIER="${P_UPDATER}/myriarch-updater"; return 0; fi
  # the updater downloaded here only counts when its hash comes from --updater-sha256 and not from a file that this
  # verifier is needed for
  if [ -z "${UPDATER_CHECKSUMS_URL}" ] && [ -n "${UPDATER_BIN:-}" ] && [ -x "${UPDATER_BIN}" ] && [ -n "${UPDATER_SHA256}" ]; then VERIFIER="${UPDATER_BIN}"; return 0; fi
  if [ -n "${problem}" ]; then die_cosign_outdated "${problem}"; fi
  die sigstore_verifier_missing "no Sigstore verifier found: install cosign ${MIN_COSIGN_VERSION} or newer, or pass --verifier <installed myriarch-updater>; a host without a verifier can use --with-updater with --updater-sha256 (verify checksums.txt on an admin host first)"
}

verify_signature() { # asset-file bundle-url identity-regexp version
  local file="$1" bundle_url="$2" identity="$3" version="$4"
  [ "${VERIFY_SIGNATURES}" = skip ] && return 0
  local bundle
  bundle="${TMP_DIR}/$(basename "${file}").sigstore.json"
  download "${bundle_url}" "${bundle}"
  if is_cosign "${VERIFIER}"; then
    local args=(verify-blob --bundle "${bundle}" --certificate-identity-regexp "${identity}" --certificate-oidc-issuer "${CERT_ISSUER}")
    [ -n "${TRUSTED_ROOT}" ] && args+=(--trusted-root "${TRUSTED_ROOT}")
    "${VERIFIER}" "${args[@]}" "${file}" >/dev/null 2>&1 || die sigstore_verification_failed "Sigstore verification of $(basename "${file}") failed"
  else
    local args=(verify-bundle --bundle "${bundle}" --artifact "${file}" --version "v${version}" --identity-regexp "${identity}" --issuer "${CERT_ISSUER}")
    [ -n "${TRUSTED_ROOT}" ] && args+=(--trusted-root "${TRUSTED_ROOT}")
    "${VERIFIER}" "${args[@]}" >/dev/null 2>&1 || die sigstore_verification_failed "Sigstore verification of $(basename "${file}") failed"
  fi
  log "Sigstore signature of $(basename "${file}") ok"
}

# Downloads the asset + .sha256 and checks the hash. Result: the file in TMP_DIR
fetch_verified() { # url dest
  local url="$1" dest="$2"
  download "${url}" "${dest}"
  download "${url}.sha256" "${dest}.sha256"
  local want got
  want="$(tr -d '[:space:]' < "${dest}.sha256" | cut -c1-64)"
  [[ "${want}" =~ ^[0-9a-fA-F]{64}$ ]] || die checksum_invalid "invalid .sha256 file for ${url}"
  got="$(sha256_of "${dest}")"
  [ "$(printf '%s' "${want}" | tr 'A-F' 'a-f')" = "${got}" ] || die checksum_mismatch "SHA-256 mismatch: ${url} (expected ${want}, got ${got})"
  log "SHA-256 ok: $(basename "${dest}")"
}

# ---------------------------------------------------------------- Steps
ensure_user() {
  [ "${STAGING_MODE}" = 1 ] && return 0
  if id -u "${SERVICE_USER}" >/dev/null 2>&1; then return 0; fi
  log "creating service user ${SERVICE_USER}"
  if have useradd; then
    useradd --system --no-create-home --home-dir /var/lib/otelcol --shell /usr/sbin/nologin "${SERVICE_USER}"
  else
    adduser --system --no-create-home --home /var/lib/otelcol --group "${SERVICE_USER}"
  fi
}

ensure_dirs() {
  install -d -m 0755 "${P_OPT}" "${P_COLLECTOR}" "${P_COLLECTOR}/versions" "${P_SUPERVISOR}" "${P_UPDATER}" "${P_ETC}"
  # Data: empty directories only, never create files in the supervisor storage
  install -d -m 0750 "${P_VAR}" "${P_VAR}/supervisor" "${P_VAR}/storage"
  set_owner "${SERVICE_USER}:${SERVICE_USER}" "${P_VAR}" "${P_VAR}/supervisor" "${P_VAR}/storage"
}

install_collector() {
  local base="${RELEASE_BASE_URL}/v${COLLECTOR_VERSION}"
  local asset="${COLLECTOR_ASSET_PREFIX}_${COLLECTOR_VERSION}_linux_${ARCH}.tar.gz"
  local vdir="${P_COLLECTOR}/versions/${COLLECTOR_VERSION}"
  local current=""
  if [ -L "${P_COLLECTOR}/current" ]; then current="$(basename "$(readlink "${P_COLLECTOR}/current")")"; fi

  if [ -x "${vdir}/${COLLECTOR_ASSET_PREFIX}" ] && [ -f "${vdir}/.myriarch-install.json" ]; then
    log "Collector v${COLLECTOR_VERSION} is already installed"
  else
    fetch_verified "${base}/${asset}" "${TMP_DIR}/${asset}"
    verify_signature "${TMP_DIR}/${asset}" "${base}/${asset}.sigstore.json" "$(cert_identity base-release "${COLLECTOR_VERSION}")" "${COLLECTOR_VERSION}"
    # extract only the binary from the archive root (no path from the archive is used)
    local stage="${TMP_DIR}/collector-stage"
    rm -rf "${stage}"; mkdir -p "${stage}"
    # owner and mode come from the installer, never from the archive
    tar -xzf "${TMP_DIR}/${asset}" --no-same-owner --no-same-permissions -C "${stage}" "${COLLECTOR_ASSET_PREFIX}" 2>/dev/null \
      || tar -xzf "${TMP_DIR}/${asset}" --no-same-owner --no-same-permissions -C "${stage}" "./${COLLECTOR_ASSET_PREFIX}" 2>/dev/null \
      || die archive_invalid "binary ${COLLECTOR_ASSET_PREFIX} not found in the archive root"
    [ -f "${stage}/${COLLECTOR_ASSET_PREFIX}" ] && [ ! -L "${stage}/${COLLECTOR_ASSET_PREFIX}" ] || die archive_invalid "the archive contains no regular file ${COLLECTOR_ASSET_PREFIX}"
    chmod 0755 "${stage}/${COLLECTOR_ASSET_PREFIX}"
    set_owner "root:root" "${stage}" "${stage}/${COLLECTOR_ASSET_PREFIX}"
    local vout; vout="$("${stage}/${COLLECTOR_ASSET_PREFIX}" --version 2>&1)" || die collector_start_failed "the new collector binary cannot be started: ${vout}"
    case "${vout}" in *"version ${COLLECTOR_VERSION}"*) ;; *) die collector_version_mismatch "collector reports \"${vout}\", expected version ${COLLECTOR_VERSION}" ;; esac
    local archive_sha bin_sha
    archive_sha="$(sha256_of "${TMP_DIR}/${asset}")"; bin_sha="$(sha256_of "${stage}/${COLLECTOR_ASSET_PREFIX}")"
    # marker in the same format as myriarch-updater: identical packages are not installed again later
    printf '{\n  "version": "%s",\n  "contentHash": "%s",\n  "binaryHash": "%s",\n  "installedAt": "%s"\n}\n' \
      "${COLLECTOR_VERSION}" "${archive_sha}" "${bin_sha}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "${stage}/.myriarch-install.json"
    chmod 0644 "${stage}/.myriarch-install.json"
    set_owner "root:root" "${stage}/.myriarch-install.json"
    rm -rf "${vdir}"
    mv "${stage}" "${vdir}"
    chmod 0755 "${vdir}"
    log "Collector v${COLLECTOR_VERSION} installed into ${vdir}"
  fi

  if [ "${current}" != "${COLLECTOR_VERSION}" ]; then
    atomic_symlink "versions/${COLLECTOR_VERSION}" "${P_COLLECTOR}/current"
    CHANGED=1
    log "current -> versions/${COLLECTOR_VERSION}"
  fi
}

install_supervisor_binary() {
  local asset="${SUPERVISOR_ASSET_PREFIX}_${SUPERVISOR_VERSION}_linux_${ARCH}"
  local base="${RELEASE_BASE_URL}/cmd/opampsupervisor/v${SUPERVISOR_VERSION}"
  local target="${P_SUPERVISOR}/opampsupervisor"
  if [ -x "${target}" ] && [ -f "${P_SUPERVISOR}/VERSION" ] && [ "$(cat "${P_SUPERVISOR}/VERSION")" = "${SUPERVISOR_VERSION}" ]; then
    log "Supervisor v${SUPERVISOR_VERSION} is already installed"
    return 0
  fi
  fetch_verified "${base}/${asset}" "${TMP_DIR}/${asset}"
  verify_signature "${TMP_DIR}/${asset}" "${base}/${asset}.sigstore.json" "$(cert_identity base-binary-release "${SUPERVISOR_VERSION}")" "${SUPERVISOR_VERSION}"
  install -m 0755 "${TMP_DIR}/${asset}" "${target}.new"
  mv -f "${target}.new" "${target}"
  printf '%s\n' "${SUPERVISOR_VERSION}" > "${P_SUPERVISOR}/VERSION"
  CHANGED=1
  log "Supervisor v${SUPERVISOR_VERSION} installed"
}

# The CA bundle of myriarchd is copied into the protected configuration directory: the supervisor and the
# updater (root) trust it for the connection that carries the enrollment token, so it must not stay at a
# location that other accounts can write. Result: CA_RENDERED, the path used in the configuration files.
CA_RENDERED=""
install_ca_file() {
  [ -n "${CA_FILE}" ] || return 0
  if install_if_changed "${CA_FILE}" "${P_ETC}/ca.pem" 0644; then CHANGED=1; log "ca.pem written"; fi
  rm -f "${P_ETC}/ca.pem.bak"
  set_owner "root:root" "${P_ETC}/ca.pem"
  CA_RENDERED="/etc/myriarch/ca.pem"
}

install_config() {
  install_ca_file
  local out="${TMP_DIR}/supervisor.yaml"
  render_template "${TEMPLATES_DIR}/supervisor/linux/supervisor.yaml" "${out}" \
    "OPAMP_ENDPOINT=${OPAMP_ENDPOINT}" "CA_FILE=${CA_RENDERED}" \
    "SITE_TYPE=${SITE_TYPE}" "SITE_ID=${SITE_ID}" "SITE_NAME=${SITE_NAME}" "SITE_COUNTRY=${SITE_COUNTRY}" \
    "SITE_REGION=${SITE_REGION}" "SITE_TIMEZONE=${SITE_TIMEZONE}" "ENVIRONMENT=${ENVIRONMENT}"
  if install_if_changed "${out}" "${P_ETC}/supervisor.yaml" 0644; then CHANGED=1; log "supervisor.yaml written"; fi

  # Fallback config: locally customizable, so only created when it is missing
  if [ ! -f "${P_ETC}/fallback.yaml" ]; then
    install -m 0644 "${TEMPLATES_DIR}/supervisor/linux/fallback.yaml" "${P_ETC}/fallback.yaml"
    log "fallback.yaml created"
  fi

  # Token: environment file for the supervisor (0600 root) + file for myriarch-updater
  local env_file="${P_ETC}/myriarch.env" env_tmp="${TMP_DIR}/myriarch.env"
  : > "${env_tmp}"
  if [ -f "${env_file}" ]; then grep -v '^MYRIARCH_ENROLLMENT_TOKEN=' "${env_file}" > "${env_tmp}" || true; fi
  if ! grep -q 'OTEL_GATEWAY_TOKEN' "${env_tmp}"; then
    {
      echo "# Collector secrets (referenced in the blueprint config as \${env:NAME})"
      echo "# OTEL_GATEWAY_TOKEN="
      echo "# BACKEND_API_TOKEN="
    } >> "${env_tmp}"
  fi
  printf 'MYRIARCH_ENROLLMENT_TOKEN=%s\n' "${TOKEN}" >> "${env_tmp}"
  if install_if_changed "${env_tmp}" "${env_file}" 0600; then CHANGED=1; log "myriarch.env written (0600)"; fi
  rm -f "${env_file}.bak" # the backup would contain the old token
  set_owner "root:root" "${env_file}"

  printf '%s\n' "${TOKEN}" > "${TMP_DIR}/enrollment-token"
  install_if_changed "${TMP_DIR}/enrollment-token" "${P_ETC}/enrollment-token" 0600 || true
  rm -f "${P_ETC}/enrollment-token.bak"
  set_owner "root:root" "${P_ETC}/enrollment-token"
}

install_units() {
  [ "${STAGING_MODE}" = 1 ] && install -d -m 0755 "${P_UNITS}"
  if install_if_changed "${TEMPLATES_DIR}/systemd/otelcol-supervisor.service" "${P_UNITS}/otelcol-supervisor.service" 0644; then
    UNITS_CHANGED=1; CHANGED=1
  fi
  rm -f "${P_UNITS}/otelcol-supervisor.service.bak"
}

# SHA-256 of the line "<hex>  <file name>" in a goreleaser checksums.txt; exactly one match.
checksum_from_file() { # checksums-file filename
  local hits count
  hits="$(awk -v want="$2" '{ n = $2; sub(/^\*/, "", n); if (n == want) print $1 }' "$1")"
  count="$(printf '%s' "${hits}" | grep -c . || true)"
  [ "${count}" = 1 ] || die checksum_entry_count "checksums.txt contains ${count} lines for $2 (expected exactly 1): --updater-url and the release do not match"
  [[ "${hits}" =~ ^[0-9a-fA-F]{64}$ ]] || die checksum_invalid "invalid checksum for $2 in checksums.txt"
  printf '%s' "${hits}" | tr 'A-F' 'a-f'
}

# Downloads checksums.txt and its keyless Sigstore bundle, verifies the bundle against the release identity of
# UPDATER_VERSION (VERIFIER is resolved before) and prints the expected SHA-256 of the updater on stdout.
updater_sha256_from_checksums() {
  local sums="${TMP_DIR}/checksums.txt" asset
  asset="${UPDATER_URL%%[?#]*}"; asset="${asset##*/}"
  [ -n "${asset}" ] || die option_invalid "no file name in --updater-url: ${UPDATER_URL}"
  download "${UPDATER_CHECKSUMS_URL}" "${sums}" >&2
  verify_signature "${sums}" "${UPDATER_BUNDLE_URL}" "$(cert_identity_release "${UPDATER_VERSION}")" "${UPDATER_VERSION}" >&2
  checksum_from_file "${sums}" "${asset}"
}

# Downloads the myriarch-updater and checks its SHA-256 before anything is executed (with --updater-sha256 and without
# --updater-checksums-url it also serves as the Sigstore verifier for collector and supervisor). The expected hash comes
# from --updater-sha256 and/or the verified checksums.txt. Result: UPDATER_BIN
UPDATER_BIN=""
fetch_updater() {
  [ "${WITH_UPDATER}" = 1 ] || return 0
  local bin="${TMP_DIR}/myriarch-updater" expected="" from_sums=""
  if [ -n "${UPDATER_SHA256}" ]; then expected="$(printf '%s' "${UPDATER_SHA256}" | tr 'A-F' 'a-f')"; fi
  if [ -n "${UPDATER_CHECKSUMS_URL}" ]; then
    from_sums="$(updater_sha256_from_checksums)"
    if [ -n "${expected}" ] && [ "${expected}" != "${from_sums}" ]; then
      die checksum_conflict "--updater-sha256 (${expected}) contradicts the signed checksums.txt (${from_sums}): hash conflict"
    fi
    expected="${from_sums}"
  fi
  download "${UPDATER_URL}" "${bin}"
  if [ -n "${expected}" ]; then
    [ "$(sha256_of "${bin}")" = "${expected}" ] || die checksum_mismatch "SHA-256 of myriarch-updater does not match"
    log "SHA-256 ok: myriarch-updater"
  else
    warn updater_unauthenticated "neither --updater-sha256 nor --updater-checksums-url given (--insecure): the updater binary is not authenticated"
  fi
  chmod 0755 "${bin}"
  UPDATER_BIN="${bin}"
}

install_updater() {
  [ "${WITH_UPDATER}" = 1 ] || return 0
  local target="${P_UPDATER}/myriarch-updater"
  local bin="${UPDATER_BIN}"
  local trusted_value=""
  if [ -n "${TRUSTED_ROOT}" ]; then
    install -m 0644 "${TRUSTED_ROOT}" "${P_ETC}/trusted_root.json"
    trusted_value="/etc/myriarch/trusted_root.json"
  fi
  local ca_value="${CA_RENDERED}"
  if install_if_changed "${bin}" "${target}" 0755; then UPDATER_CHANGED=1; fi
  rm -f "${target}.bak"
  render_template "${TEMPLATES_DIR}/updater/linux/updater.yaml" "${TMP_DIR}/updater.yaml" \
    "OPAMP_ENDPOINT=${OPAMP_ENDPOINT}" "CA_FILE=${ca_value}" "TRUSTED_ROOT_FILE=${trusted_value}"
  if install_if_changed "${TMP_DIR}/updater.yaml" "${P_ETC}/updater.yaml" 0644; then UPDATER_CHANGED=1; fi
  rm -f "${P_ETC}/updater.yaml.bak"
  if install_if_changed "${TEMPLATES_DIR}/systemd/myriarch-updater.service" "${P_UNITS}/myriarch-updater.service" 0644; then
    UNITS_CHANGED=1; UPDATER_CHANGED=1
  fi
  rm -f "${P_UNITS}/myriarch-updater.service.bak"
  log "myriarch-updater installed"
}

activate_services() {
  [ "${STAGING_MODE}" = 1 ] && { log "staging mode: systemd is not touched"; return 0; }
  if [ "${UNITS_CHANGED}" = 1 ]; then systemctl daemon-reload; fi
  systemctl enable otelcol-supervisor.service >/dev/null
  if systemctl is-active --quiet otelcol-supervisor.service; then
    if [ "${CHANGED}" = 1 ]; then log "restarting otelcol-supervisor"; systemctl restart otelcol-supervisor.service; fi
  else
    systemctl start otelcol-supervisor.service
  fi
  if [ "${WITH_UPDATER}" = 1 ]; then
    systemctl enable myriarch-updater.service >/dev/null
    if systemctl is-active --quiet myriarch-updater.service; then
      if [ "${UPDATER_CHANGED}" = 1 ]; then systemctl restart myriarch-updater.service; fi
    else
      systemctl start myriarch-updater.service
    fi
  fi
  sleep 2
  systemctl is-active --quiet otelcol-supervisor.service || {
    warn service_not_running "otelcol-supervisor is not running; logs: journalctl -u otelcol-supervisor -n 50"
    return 1
  }
  log "otelcol-supervisor is running"
}

do_uninstall() {
  if [ "${STAGING_MODE}" = 0 ]; then
    [ "$(id -u)" = 0 ] || die privilege_required "run as root (sudo)"
    for unit in myriarch-updater otelcol-supervisor; do
      if systemctl list-unit-files "${unit}.service" >/dev/null 2>&1; then
        systemctl disable --now "${unit}.service" >/dev/null 2>&1 || true
      fi
    done
  fi
  rm -f "${P_UNITS}/otelcol-supervisor.service" "${P_UNITS}/myriarch-updater.service"
  [ "${STAGING_MODE}" = 0 ] && systemctl daemon-reload || true
  rm -rf "${P_OPT}" "${P_ETC}"
  if [ "${PURGE}" = 1 ]; then
    rm -rf "${P_VAR}" "${ROOT_PREFIX}/var/lib/myriarch-updater"
    if [ "${STAGING_MODE}" = 0 ] && id -u "${SERVICE_USER}" >/dev/null 2>&1; then userdel "${SERVICE_USER}" 2>/dev/null || true; fi
  else
    log "data under ${P_VAR} is kept (--purge removes it)"
  fi
  log "uninstalled"
}

# ---------------------------------------------------------------- main
CHANGED=0
UNITS_CHANGED=0
UPDATER_CHANGED=0

main() {
  if [ "${UNINSTALL}" = 1 ]; then do_uninstall; exit 0; fi
  validate_inputs
  detect_arch
  check_prereqs
  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-install.XXXXXX")"
  log "installing: collector v${COLLECTOR_VERSION}, supervisor v${SUPERVISOR_VERSION}, linux/${ARCH}, site ${SITE_TYPE}/${SITE_ID}"
  if [ "${WITH_UPDATER}" = 1 ] && [ -n "${UPDATER_CHECKSUMS_URL}" ]; then
    # the verifier of checksums.txt must exist before the updater is downloaded, and it is never that updater
    resolve_verifier
    fetch_updater
  else
    fetch_updater
    resolve_verifier
  fi
  ensure_user
  ensure_dirs
  install_collector
  install_supervisor_binary
  install_config
  install_units
  install_updater
  activate_services
  log "done. The host reports to ${OPAMP_ENDPOINT} as an agent"
}

main
