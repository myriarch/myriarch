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
# its own immediately. A host that has such a package is not touched: when otelcol-contrib.service or
# opampsupervisor.service of the upstream project is active or enabled, the installer stops with
# conflicting_install before it creates a user, a directory or a file, and it removes nothing.
#
# The supervisor storage directory (/var/lib/otelcol/supervisor) is only created empty, never
# pre-populated: persistent_state.yaml holds the instance UID. Golden images must empty it BEFORE
# cloning (otherwise all clones share one identity).
#
# Example, in a root shell (sudo -i), from the unpacked release archive that you verified, in a directory that only root can
# write (docs/tasks/connect-a-host.md has the steps: BASE, VERSION and ARCH are the variables of its step 2). The file
# enrollment-token holds the token, so that it appears in no argument list, and is deleted after the run. The verifier is a
# cosign 3.0.4 or newer that only root can replace, or the myriarch-updater of the verified archive:
#   ./deploy/scripts/install-linux.sh --opamp-endpoint wss://fleet.example.com/v1/opamp \
#       --token-file ./enrollment-token --site-type datacenter --site-id dc-01 --site-name 'Data center 1' \
#       --site-country NL --site-timezone Europe/Amsterdam --environment prod \
#       --collector-version 0.162.0 --verifier /usr/local/bin/cosign --with-updater \
#       --updater-url "${BASE}/myriarch-updater_${VERSION}_linux_${ARCH}" \
#       --updater-checksums-url "${BASE}/checksums.txt"
#   rm -f enrollment-token
#
# The templates live under deploy/ (--templates-dir, default: the directory above this script).
#
# Server mode (ADR-0046): with --server the installer takes the versions, the OpAMP endpoint and the artifacts from the server
# instead of from the operator. The copied command of the web UI downloads the installer bundle of the server, checks its pinned
# SHA-256, unpacks it and starts this script from it:
#   install-linux.sh --server https://fleet.example.com --token-file ./enrollment-token --site-type edge --site-id 042
# It asks POST <server>/api/v1/install/plan for a plan (the token only as a request header, read by curl from standard input) and
# reads the answer strictly as data: known keys only, each value against a pattern, at most 64 lines and 8 KiB, URLs of the
# origin of --server only, https only, redirects only to the same origin. Collector and supervisor are downloaded from the
# server with the install grants of the plan and verified with SHA-256 and their Sigstore bundle against an identity that this
# script builds itself (never one the server names); the verifier is the myriarch-updater of the same bundle (verify-bundle).
# Nothing runs before its check. A plan that the server is still preparing (first use of a platform, a new target) is awaited
# for --plan-wait seconds (default 900). The updater is installed from the bundle when the plan says so.
#
# Without --server and without --collector-version the tested version of this release is installed (DEFAULT_COLLECTOR_VERSION,
# the OTELCOL_VERSION of ci/versions.env). --collector-version latest asks GitHub for the newest release and is used only on
# request: it is not the version the installers were tested with.
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
#   option_missing                  a required option is absent (site type/id, --opamp-endpoint without --server, token or
#                                   token file, --updater-url with --with-updater)
#   option_invalid                  malformed value (versions, arch, verify-signatures mode, SHA-256 format,
#                                   control characters, token characters, endpoint scheme, no file name
#                                   in --updater-url; a non-https --release-base-url raises https_required)
#   option_removed                  a removed option (--signing-key, --updater-checksums-sig-url); the message
#                                   names the replacement
#   option_conflict                 mutually exclusive or dependent options (--token + --token-file; --fleet,
#                                   --plan-wait or --collector-version latest without/with --server; --server with
#                                   --with-updater, --updater-*, --release-base-url, --supervisor-version or
#                                   --verify-signatures skip;
#                                   updater options without --with-updater; --updater-bundle-url without
#                                   checksums URL; --updater-version and the version in --updater-url differ;
#                                   --updater-checksums-url with --verify-signatures skip)
#   updater_hash_source_missing     --with-updater without --updater-sha256 or --updater-checksums-url
#   insecure_required               plain ws/http endpoint or --verify-signatures skip without --insecure
#   https_required                  non-https download URL, --server, plan URL or redirect without --insecure
#   file_unreadable                 token file, CA file, trusted root or verifier is unusable
#   tool_missing                    a required tool is absent (sha256sum/shasum, curl/wget, tar, gzip, install, awk, od,
#                                   cmp, systemctl, useradd/adduser); the message names the tool
#   sigstore_verifier_missing       no Sigstore verifier (cosign or an installed myriarch-updater)
#   sigstore_verifier_outdated      cosign is older than 3.0.4 or reports no usable version
#   privilege_required              not running as root
#   unsupported_platform            not Linux, or unsupported CPU architecture
#   plan_failed                     --server: the request for the plan did not get an answer (no connection, a TLS error,
#                                   a server error); the message gives the cause and the next step
#   plan_invalid                    --server: the plan or a redirect breaks the rules (unknown or repeated key, a value that
#                                   does not match its pattern, too many lines or bytes, a URL outside the origin of --server,
#                                   a version other than the one asked for)
#   plan_rejected                   --server: the server refused the plan; the message carries the code of the server
#                                   (for example fleet_mismatch, no_fleet, rollout_blocking, unauthorized) and what to do
#   plan_unavailable                --server: the server cannot give a plan now (503 or 429), or it was still preparing the
#                                   artifacts after --plan-wait seconds
#   conflicting_install             otelcol-contrib.service or opampsupervisor.service of a deb or rpm package of the upstream
#                                   project is active or enabled (Windows: a service otelcol-contrib or opampsupervisor of an
#                                   MSI exists); the message names the unit or service and the next step, nothing is removed
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
#   supervisor_missing              the supervisor binary is not there when the installation step is over: Linux, a missing,
#                                   non-executable or no regular file at /opt/myriarch/supervisor/opampsupervisor; Windows, a
#                                   missing file, a file that differs from the verified one or an item that is no regular file at
#                                   ...\supervisor\opampsupervisor.exe, and a service that did not start because the file of its
#                                   binPath is gone (a security tool that quarantined the binary is the usual cause). The message
#                                   names the path and the next step: a file that is gone or changed is installed again by the next
#                                   run, a directory (on Windows a link too) is not replaced and has to be removed first
#   service_not_running             WARNING: otelcol-supervisor is not running after the installation
#   token_on_command_line           WARNING: --token is visible in the process list and shell history
#   updater_unauthenticated         WARNING: the updater binary has no trusted hash (--insecure)
#
# Windows-only codes (see install-windows.ps1 and deploy/windows/README.md): authenticode_invalid,
# directory_unsafe, acl_failed, service_config_failed, updater_install_failed, group_membership_failed,
# language_mode_unsupported, unexpected_error; service_not_running is fatal there.
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
readonly UPDATER_BINARY_NAME="myriarch-updater"
# The collector version that this release was tested with: the OTELCOL_VERSION of ci/versions.env. The default of an installation
# without --server and without --collector-version; ci/tests/versions-test.sh keeps both in step.
readonly DEFAULT_COLLECTOR_VERSION="0.162.0"
# Where --collector-version latest asks: the redirect of this page names the newest release (no API, no rate limit).
readonly LATEST_RELEASE_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/latest"
# Limits of the plan of the server (ADR-0046, "Interface sketch"; internal/install/grammar.go).
readonly PLAN_MAX_BYTES=8192
readonly PLAN_MAX_LINES=64

# bash >= 5.2 expands "&" in ${var//pat/repl}; turn that off so values stay literal
shopt -u patsub_replacement 2>/dev/null || true

SERVER=""                  # --server: https://host[:port], the origin of every request of the server mode
FLEET=""
PLAN_WAIT=900              # seconds to await a plan that the server is still preparing
PLAN_WAIT_SET=0
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
RELEASE_BASE_SET=0
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
  --opamp-endpoint URL      wss://fleet.example.com/v1/opamp (ws|wss|http|https); with --server it comes from the plan
  --token-file FILE         enrollment token (myre_...) read from a file (recommended: --token shows up in the
                            process list and in the shell history)   or   --token TOKEN
  --site-type TYPE          edge | datacenter | office | k8s-gateway | ...
  --site-id ID              site number (always a string, leading zeros are kept)

Server mode (versions, endpoint, collector and supervisor come from the server; the token goes to it as a header only):
  --server URL              https://fleet.example.com (the origin of the server, no path)
  --fleet NAME              the fleet you expect; the server decides and refuses another one
  --plan-wait SECONDS       how long to await a plan that the server is still preparing (default 900)
  --ca-file FILE            CA of the server, also used for the requests to it
  The verifier is the myriarch-updater of the installer bundle this script runs from (--verifier overrides it). The
  updater is installed when the plan says so.

Without --server:
  --collector-version X.Y.Z collector version (without "v"); default: the version this release was tested with
                            (DEFAULT_COLLECTOR_VERSION); "latest" asks GitHub for the newest release, on request only
                            With --server it asks the server for exactly that version (inside the frame of the fleet).

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
    --server)             need_arg "$@"; SERVER="${2%/}"; shift 2 ;;
    --fleet)              need_arg "$@"; FLEET="$2"; shift 2 ;;
    --plan-wait)          need_arg "$@"; PLAN_WAIT="$2"; PLAN_WAIT_SET=1; shift 2 ;;
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
    --release-base-url)   need_arg "$@"; RELEASE_BASE_URL="${2%/}"; RELEASE_BASE_SET=1; shift 2 ;;
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
    *) [ "${INSECURE}" = 1 ] || die https_required "$2 must be an https:// URL (--insecure only for lab/tests): $1 - use the https address of the server" ;;
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

# download_transient CURL_EXIT STATUS: succeeds when a failed curl attempt is worth another one. STATUS is the effective HTTP status
# of the attempt: the status of the server or, when the server was not reached because an HTTPS proxy refused CONNECT, the status
# of the proxy (000 when there was none).
download_transient() {
  case "$1" in
    5 | 6 | 7 | 16 | 18 | 28 | 52 | 55 | 92) return 0 ;;
    22 | 56) case "$2" in 408 | 429 | 500 | 502 | 503 | 504) return 0 ;; 000) [ "$1" = 56 ] && return 0 ;; esac ;;
  esac
  return 1
}

download() { # url dest
  local url="$1" dest="$2"
  require_https "${url}" "download URL"
  log "downloading ${url}"
  if [ -n "${SERVER}" ]; then
    download_from_server "${url}" "${dest}"
    return 0
  fi
  if have curl; then
    local proto=(--proto '=https' --proto-redir '=https' --tlsv1.2)
    [ "${INSECURE}" = 1 ] && proto=()
    local attempt=0 pause=1 start="${SECONDS}" elapsed rc out status pstatus reason noun
    while :; do
      attempt=$((attempt + 1))
      noun=attempts
      [ "${attempt}" = 1 ] && noun=attempt
      rc=0
      out="$(curl --fail --silent --show-error --location ${proto[@]+"${proto[@]}"} --connect-timeout 20 --max-time 900 \
        --write-out '%{http_code} %{http_connect}' -o "${dest}" "${url}")" || rc=$?
      [ "${rc}" = 0 ] && return 0
      status="${out%% *}" pstatus="${out##* }"
      status="${status:-000}" pstatus="${pstatus:-000}"
      # An HTTPS proxy that answers CONNECT with an error leaves the status 000 (curl exit 22 or 56, depending on the curl
      # version); the status of the proxy decides then.
      if { [ "${rc}" = 22 ] || [ "${rc}" = 56 ]; } && [ "${status}" = 000 ] && [ "${pstatus}" != 000 ] && [ "${pstatus}" != 200 ]; then
        status="${pstatus}"
        reason="proxy HTTP ${status}"
      elif [ "${rc}" = 22 ]; then
        reason="HTTP ${status}"
      else
        reason="curl exit code ${rc}"
      fi
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

VERSION_RE='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'

# The scheme of the OpAMP endpoint: TLS, unless --insecure.
check_endpoint_scheme() {
  case "${OPAMP_ENDPOINT}" in
    wss://?*|https://?*) ;;
    ws://?*|http://?*)
      [ "${INSECURE}" = 1 ] || die insecure_required "--opamp-endpoint without TLS (ws://, http://) is not allowed: the token would cross the network in clear text (--insecure only for lab/tests)" ;;
    *) die option_invalid "--opamp-endpoint must start with wss:// or https://" ;;
  esac
}

# The options of the server mode: the address of the server, the fleet hint, the time to wait, and the options that cannot be
# combined with it (the plan decides what they would set).
validate_server_options() {
  local re_server='^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$'
  [[ "${SERVER}" =~ ${re_server} ]] || die option_invalid "--server must be the address of the server, https://host[:port] without a path"
  require_https "${SERVER}" "--server"
  [ "${WITH_UPDATER}" = 0 ] && [ -z "${UPDATER_URL}${UPDATER_SHA256}${UPDATER_VERSION}${UPDATER_CHECKSUMS_URL}${UPDATER_BUNDLE_URL}" ] \
    || die option_conflict "--server decides whether the updater is installed and takes it from the installer bundle: --with-updater and the --updater-* options do not apply"
  [ "${RELEASE_BASE_SET}" = 0 ] || die option_conflict "--server downloads collector and supervisor from the server: --release-base-url does not apply"
  [ -z "${SUPERVISOR_VERSION}" ] || die option_conflict "--server takes the supervisor version from the plan: --supervisor-version does not apply"
  [ "${VERIFY_SIGNATURES}" = require ] || die option_conflict "--server cannot be combined with --verify-signatures skip: everything the server provides is verified"
  [ "${COLLECTOR_VERSION}" != latest ] || die option_conflict "--collector-version latest asks GitHub and does not apply with --server: leave the version out and the server gives the target of the fleet"
  [[ "${PLAN_WAIT}" =~ ^[0-9]{1,5}$ ]] && [ "${PLAN_WAIT}" -gt 0 ] || die option_invalid "--plan-wait must be a number of seconds, 1 to 99999"
  [ -z "${FLEET}" ] || [[ "${FLEET}" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] || die option_invalid "--fleet is not the name of a fleet (lower case letters, digits and -)"
  [ -z "${OPAMP_ENDPOINT}" ] || check_endpoint_scheme
}

validate_inputs() {
  if [ -n "${SERVER}" ]; then
    validate_server_options
  else
    [ -z "${FLEET}" ] || die option_conflict "--fleet only applies together with --server"
    [ "${PLAN_WAIT_SET}" = 0 ] || die option_conflict "--plan-wait only applies together with --server"
    [ -n "${OPAMP_ENDPOINT}" ] || die option_missing "--opamp-endpoint is missing (with --server it comes from the plan)"
    check_endpoint_scheme
  fi
  [ -n "${SITE_TYPE}" ] || die option_missing "--site-type is missing"
  [ -n "${SITE_ID}" ]   || die option_missing "--site-id is missing"
  # without --server the version is settled later: empty is the tested default, "latest" is looked up on request
  if [ -n "${COLLECTOR_VERSION}" ] && [ "${COLLECTOR_VERSION}" != latest ]; then
    [[ "${COLLECTOR_VERSION}" =~ ${VERSION_RE} ]] || die option_invalid "invalid --collector-version: ${COLLECTOR_VERSION}"
  fi
  if [ -n "${SUPERVISOR_VERSION}" ]; then
    [[ "${SUPERVISOR_VERSION}" =~ ${VERSION_RE} ]] || die option_invalid "invalid --supervisor-version: ${SUPERVISOR_VERSION}"
  fi
  local v
  for v in "${SITE_TYPE}" "${SITE_ID}" "${SITE_NAME}" "${SITE_COUNTRY}" "${SITE_REGION}" "${SITE_TIMEZONE}" "${ENVIRONMENT}" "${OPAMP_ENDPOINT}" "${CA_FILE}" "${FLEET}"; do
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
  have curl || { [ -z "${SERVER}" ] && [ "${INSECURE}" = 1 ] && have wget; } || die tool_missing "curl is required (wget cannot restrict redirects to https; it is accepted only with --insecure, and not with --server)"
  if [ -n "${SERVER}" ]; then
    have awk || die tool_missing "awk is needed to read the answers of the server"
    have od || die tool_missing "od is needed to read the plan of the server"
  fi
  if [ -z "${TEMPLATES_DIR}" ]; then
    TEMPLATES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  fi
  [ -d "${TEMPLATES_DIR}/supervisor/linux" ] || die template_missing "templates not found under ${TEMPLATES_DIR} (--templates-dir)"
}

# ---------------------------------------------------------------- Packages of the upstream project
# The deb and rpm packages of the upstream project bring units of their own: otelcol-contrib.service (the contrib package
# enables and starts it at once) and opampsupervisor.service (the supervisor package enables it when its configuration file
# exists; docs/design/upstream-notes.md, "Packages and what they install"). A unit that runs or is enabled is a second collector
# next to the one that myriarch manages. The installer stops before it creates a user, a directory or a file, and it removes
# nothing: which package goes is the decision of the operator. A package whose unit neither runs nor is enabled (the rpm
# packages since 0.157.0 start nothing) runs no second collector and does not stop the installation.
FOREIGN_UNITS=(otelcol-contrib.service opampsupervisor.service)

# How a unit of the upstream packages is set up on this host: "active", "enabled", "active, enabled", or nothing.
# A root directory (--root) has no running manager of its own and the manager of this host says nothing about it, so there only
# the unit files below the directory tell whether the unit is enabled (systemctl --root).
foreign_unit_state() { # unit
  local unit="$1" state found=""
  have systemctl || return 0
  if [ "${STAGING_MODE}" = 0 ]; then
    state="$(systemctl is-active "${unit}" 2>/dev/null || true)"
    case "${state}" in active | activating | reloading) found="active" ;; esac
    state="$(systemctl is-enabled "${unit}" 2>/dev/null || true)"
  else
    state="$(systemctl --root="${ROOT_PREFIX}" is-enabled "${unit}" 2>/dev/null || true)"
  fi
  case "${state}" in enabled | enabled-runtime) found="${found:+${found}, }enabled" ;; esac
  printf '%s' "${found}"
}

check_foreign_installations() {
  local unit state found="" units=""
  for unit in "${FOREIGN_UNITS[@]}"; do
    state="$(foreign_unit_state "${unit}")"
    if [ -n "${state}" ]; then
      found="${found}${found:+, }${unit} (${state})"
      units="${units}${units:+ }${unit}"
    fi
  done
  [ -z "${found}" ] || die conflicting_install "an installation of the upstream project is present: ${found}; it provides a second collector next to the one that myriarch manages, and the installer removes nothing - stop and disable it ('systemctl disable --now ${units}'), remove its package with your package manager and run the installer again"
}

# The supervisor must be an executable file at the path of the unit when the installation step is over. A security tool that
# removes an unsigned binary, a mount with noexec or a copy that went wrong would otherwise show up only as a service that does
# not start (status 203/EXEC). The next step depends on the case: a file that is gone or cannot run is installed again by the next
# run, but a directory is not replaced by it (the test of the step that skips an installed supervisor is true for a directory), so
# the next run would stop at the same item until it is removed.
check_supervisor_binary() {
  local target="${P_SUPERVISOR}/opampsupervisor" why="" next=""
  local again="look for a cleanup or security tool that removed or changed it and for a noexec mount of its file system (findmnt -T ${target}), then run the installer again"
  if [ ! -e "${target}" ]; then why="does not exist"; next="${again}"
  elif [ ! -f "${target}" ]; then why="is not a regular file"; next="remove the item at ${target} yourself (the installer does not replace a directory), then run the installer again"
  elif [ ! -x "${target}" ]; then why="is not executable"; next="${again}"
  fi
  [ -z "${why}" ] || die supervisor_missing "the supervisor binary ${why}: ${target}; ${next}"
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
    # the verifier of the server mode is the updater of this release; its TUF cache lives in the private working directory, so
    # that the check does not depend on the home directory of the account that runs the installer
    [ -n "${SERVER}" ] && [ -z "${TRUSTED_ROOT}" ] && args+=(--tuf-cache "${TMP_DIR}/tuf-cache")
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

# ---------------------------------------------------------------- Server mode (ADR-0046)
# The server provides data and the host verifies code. Every request goes to the origin of --server (https only, a redirect only to
# that origin), the enrollment token reaches the server as a request header only, and the plan is read as data: nothing in it is
# run, evaluated or used before a strict pattern accepted it.
readonly PLAN_PATH="/api/v1/install/plan"
readonly PLAN_RETRY_SECONDS=6      # a connection that fails is tried again for this long (the plan is asked again after "preparing" anyway)
SERVER_HEADERS=""                  # the response headers of the last request
CURL_RC=0                          # exit code of curl for the last request
HTTP_STATUS=000                    # HTTP status of the last request
DL_BEARER=""                       # the install grant for the artifact that is being downloaded
PLAN_FORMAT="" PLAN_FLEET="" PLAN_SOURCE="" PLAN_OPAMP="" PLAN_COLLECTOR_VERSION="" PLAN_COLLECTOR_URL="" PLAN_COLLECTOR_GRANT=""
PLAN_SUPERVISOR_VERSION="" PLAN_SUPERVISOR_URL="" PLAN_SUPERVISOR_GRANT="" PLAN_UPDATER=""
BUNDLE_UPDATER_SHA=""              # SHA-256 of the myriarch-updater that was copied from the installer bundle

json_escape() {
  local v="$1"
  v="${v//\\/\\\\}"
  v="${v//\"/\\\"}"
  printf '%s' "${v}"
}

# ,"name":"value" for the JSON object of the attributes, or nothing when the value is empty.
json_attr() { # name value
  [ -n "$2" ] || return 0
  printf ',"%s":"%s"' "$1" "$(json_escape "$2")"
}

# The body of the plan request. It holds no secret: the platform, the attributes the host would report, the fleet the person
# expects and the version they ask for.
plan_request_body() {
  local attrs host
  host="$(hostname 2>/dev/null || true)"
  [[ "${host}" =~ ^[A-Za-z0-9._-]{1,253}$ ]] || host=""
  attrs="$(json_attr host.name "${host}")$(json_attr site.type "${SITE_TYPE}")$(json_attr site.id "${SITE_ID}")$(json_attr site.name "${SITE_NAME}")"
  attrs="${attrs}$(json_attr site.country "${SITE_COUNTRY}")$(json_attr site.region "${SITE_REGION}")$(json_attr site.timezone "${SITE_TIMEZONE}")"
  attrs="${attrs}$(json_attr deployment.environment.name "${ENVIRONMENT}")"
  printf '{"os":"linux","arch":"%s","attributes":{%s}' "${ARCH}" "${attrs#,}"
  [ -z "${FLEET}" ] || printf ',"fleet":"%s"' "$(json_escape "${FLEET}")"
  [ -z "${COLLECTOR_VERSION}" ] || printf ',"collectorVersion":"%s"' "$(json_escape "${COLLECTOR_VERSION}")"
  printf '}\n'
}

# Last value of a response header of the last request (names in lower case), without the line end.
header_value() { # name
  tr -d '\r' < "${SERVER_HEADERS}" | awk -v want="$1" '{
    i = index($0, ":")
    if (i > 0 && tolower(substr($0, 1, i - 1)) == want) { v = substr($0, i + 1); sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v); r = v }
  } END { print r }'
}

# Seconds to wait from Retry-After (a number; a date is not used), between 1 and 60; the default when it is absent or odd.
retry_after_seconds() { # default
  local v
  v="$(header_value retry-after)"
  if [[ "${v}" =~ ^[0-9]{1,4}$ ]] && [ "${v}" -ge 1 ]; then
    [ "${v}" -le 60 ] || v=60
    printf '%s' "${v}"
  else
    printf '%s' "$1"
  fi
}

# One request, redirects not followed. The token or grant is the bearer: curl reads it as a config line from standard input, so it
# is in no argument list and no environment. Sets CURL_RC and HTTP_STATUS; the body is in out, the headers in SERVER_HEADERS.
server_once() { # method url out body bearer maxsize maxtime
  local method="$1" url="$2" out="$3" body="$4" bearer="$5" maxsize="$6" maxtime="$7"
  local proto='=https' rc=0 code=""
  [ "${INSECURE}" = 1 ] && proto='=http,https'
  local args=(--silent --show-error --globoff --proto "${proto}" --tlsv1.2 --connect-timeout 20 --max-time "${maxtime}"
    -D "${SERVER_HEADERS}" -o "${out}" -w '%{http_code}')
  [ -z "${maxsize}" ] || args+=(--max-filesize "${maxsize}")
  [ -z "${CA_FILE}" ] || args+=(--cacert "${CA_FILE}")
  if [ "${method}" = POST ]; then
    args+=(-X POST -H 'Content-Type: application/json' -H 'Accept: text/plain' --data-binary "@${body}")
  fi
  if [ -n "${bearer}" ]; then
    code="$(printf 'header = "Authorization: Bearer %s"\n' "${bearer}" | curl -K - "${args[@]}" "${url}")" || rc=$?
  else
    code="$(curl "${args[@]}" "${url}")" || rc=$?
  fi
  CURL_RC="${rc}"
  HTTP_STATUS="${code}"
  [[ "${HTTP_STATUS}" =~ ^[0-9]{3}$ ]] || HTTP_STATUS=000
  return 0
}

# The URL of a redirect, after the checks: https (unless --insecure) and the origin of --server, nothing else.
redirect_target() {
  local loc
  loc="$(header_value location)"
  [ -n "${loc}" ] || die plan_invalid "the server answered HTTP ${HTTP_STATUS} without a Location: it is not a server this installer can talk to"
  case "${loc}" in *[!A-Za-z0-9._~:/@%+=?\&-]*) die plan_invalid "the server redirected to a URL with characters that a URL of this installation does not have" ;; esac
  case "${loc}" in
    //*) die plan_invalid "the server redirected to another origin (a URL without a scheme): the installer follows redirects only on the origin of --server" ;;
    /*) loc="${SERVER}${loc}" ;;
    https://* | http://*) ;;
    *) die plan_invalid "the server redirected to something that is not a URL on its origin: the installer follows redirects only on the origin of --server" ;;
  esac
  require_https "${loc}" "the redirect of the server"
  case "${loc}" in
    "${SERVER}" | "${SERVER}/"* | "${SERVER}?"*) ;;
    *) die plan_invalid "the server redirected to another origin: the installer follows redirects only on the origin of --server, because the request carries the enrollment token or a grant" ;;
  esac
  printf '%s' "${loc}"
}

# One request with the redirects that the rules allow (at most three).
server_exchange() { # method url out body bearer maxsize maxtime
  local method="$1" url="$2" hops=0
  shift 2
  while :; do
    server_once "${method}" "${url}" "$@"
    [ "${CURL_RC}" = 0 ] || return 0
    case "${HTTP_STATUS}" in
      301 | 302 | 303 | 307 | 308)
        hops=$((hops + 1))
        [ "${hops}" -le 3 ] || die plan_invalid "the server redirected more than three times"
        url="$(redirect_target)" ;;
      *) return 0 ;;
    esac
  done
}

# A request that is tried again while it fails in a way that can pass (the policy of download(): a connection that fails, and the
# statuses in "transient"), for at most "window" seconds. The result is left in CURL_RC and HTTP_STATUS for the caller.
server_call() { # window transient method url out body bearer maxsize maxtime
  local window="$1" transient="$2" attempt=0 pause=1 start="${SECONDS}" elapsed why
  shift 2
  while :; do
    attempt=$((attempt + 1))
    server_exchange "$@"
    if [ "${CURL_RC}" = 0 ]; then
      case " ${transient} " in *" ${HTTP_STATUS} "*) why="HTTP ${HTTP_STATUS}" ;; *) return 0 ;; esac
    else
      download_transient "${CURL_RC}" 000 || return 0
      why="curl exit code ${CURL_RC}"
    fi
    elapsed=$((SECONDS - start))
    [ "${elapsed}" -lt "${window}" ] || return 0
    [ $((elapsed + pause)) -le "${window}" ] || pause=$((window - elapsed))
    log "attempt ${attempt} failed (${why}), next attempt in ${pause} s"
    sleep "${pause}"
    pause=$((pause * 2))
  done
}

# The code of the reason in a problem document of the server: the code of the first issue, else the class. Printed only when it
# has the shape of a code; the answer is untrusted data.
problem_code() { # body-file
  local text first rest issue top
  text="$(head -c 65536 "$1" 2>/dev/null | tr -d '\r\n')"
  first='"code"[[:space:]]*:[[:space:]]*"[a-z][a-z0-9_]*"'
  top="$(printf '%s' "${text%%\"issues\"*}" | grep -o "${first}" | head -n 1 | sed 's/.*"\([a-z0-9_]*\)"$/\1/' || true)"
  case "${text}" in
    *'"issues"'*)
      rest="${text#*\"issues\"}"
      issue="$(printf '%s' "${rest}" | grep -o "${first}" | head -n 1 | sed 's/.*"\([a-z0-9_]*\)"$/\1/' || true)" ;;
    *) issue="" ;;
  esac
  printf '%s' "${issue:-${top}}"
}

# The detail sentence of a problem document, cut to harmless characters: it is shown to the person who reads the message.
problem_detail() { # body-file
  head -c 65536 "$1" 2>/dev/null | tr -d '\r\n' | grep -o '"detail"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 \
    | sed 's/^"detail"[[:space:]]*:[[:space:]]*"//; s/"$//' | LC_ALL=C tr -cd 'A-Za-z0-9 .,:;()/_=@+-' | cut -c1-300 || true
}

# What to do about a reason that the server gave for refusing the plan.
plan_hint() { # code
  case "$1" in
    unauthorized) printf 'the enrollment token is unknown, revoked or expired, or it was not sent: create a new token in the web UI and run the command again' ;;
    not_found) printf 'this server does not offer the installation (install.enabled is off) or --server does not name a myriarch server: check the address, or use the manual path of docs/tasks/connect-a-host.md' ;;
    forbidden) printf 'the server does not allow this token to install: ask the administrator of the server' ;;
    fleet_mismatch) printf 'the server assigns the host to another fleet than --fleet: leave --fleet out, or correct the site attributes and the labels of the enrollment token' ;;
    no_fleet) printf 'no fleet selects this host: check --site-type, --site-id and the other site options and the labels of the enrollment token against the selectors of the fleets in Git' ;;
    fleet_ambiguous) printf 'more than one fleet selects this host: give the fleets different priorities in Git or narrow their selectors' ;;
    rollout_blocking) printf 'a version rollout of the fleet is paused, failed or rolling back: resolve it in the web UI, then run the command again' ;;
    no_target_version) printf 'the fleet has no target version (it is on hold or none is set): set one in Git or choose one in the web UI, then run the command again' ;;
    unsupported_distribution) printf 'the fleet uses a collector distribution that cannot be installed from the server: use the manual path of docs/tasks/connect-a-host.md' ;;
    unknown_version | unknown_supervisor_version) printf 'the release tracker of the server does not know that release: wait for the next poll, or name a version the server knows (--collector-version)' ;;
    version_not_allowed) printf 'the version is outside the frame of the fleet in Git: leave --collector-version out, or name a version inside the frame' ;;
    invalid_value | unknown_attribute) printf 'the server did not accept a site option: check the values of --site-* and --environment' ;;
    platform_not_supported) printf 'the server does not offer this platform: use the manual path of docs/tasks/connect-a-host.md' ;;
    server_settings) printf 'the server is not set up for host installations (server.publicUrl, opamp.path, client certificates): ask its administrator' ;;
    *) printf 'see docs/operations/troubleshooting.md, "When the installer stops", or use the manual path of docs/tasks/connect-a-host.md' ;;
  esac
}

# Asks the server for the plan; waits while it prepares the artifacts. Result: the plan file.
PLAN_FILE=""
fetch_plan() {
  local body="${TMP_DIR}/plan-request.json" deadline wait remaining code detail
  PLAN_FILE="${TMP_DIR}/plan.txt"
  SERVER_HEADERS="${TMP_DIR}/response.headers"
  plan_request_body > "${body}"
  deadline=$((SECONDS + PLAN_WAIT))
  log "asking ${SERVER} for the plan"
  while :; do
    server_call "${PLAN_RETRY_SECONDS}" "408 500 502 504" POST "${SERVER}${PLAN_PATH}" "${PLAN_FILE}" "${body}" "${TOKEN}" 1048576 60
    [ "${CURL_RC}" = 0 ] || die plan_failed "the request for the plan to ${SERVER} failed (curl exit code ${CURL_RC}): check the address, the network and the certificate of the server (--ca-file names the CA of a private server), then run the command again"
    case "${HTTP_STATUS}" in
      200) return 0 ;;
      503)
        code="$(problem_code "${PLAN_FILE}")"
        if [ "${code}" = preparing ]; then
          remaining=$((deadline - SECONDS))
          [ "${remaining}" -gt 0 ] || die plan_unavailable "the server was still preparing the artifacts after ${PLAN_WAIT} s: run the command again later, or give --plan-wait more seconds; the log of the server tells whether a download fails"
          wait="$(retry_after_seconds 10)"
          [ "${wait}" -le "${remaining}" ] || wait="${remaining}"
          log "the server is preparing the artifacts (the first request for a platform downloads about 112 MB); asking again in ${wait} s"
          sleep "${wait}"
          continue
        fi
        die plan_unavailable "the server cannot give a plan now (HTTP 503${code:+, ${code}}): run the command again in a minute; if it stays, look at the log of the server (database, release tracker)" ;;
      429)
        die plan_unavailable "the server limits the requests from this address (HTTP 429): run the command again after $(retry_after_seconds 60) s" ;;
      408 | 500 | 502 | 504)
        die plan_failed "the server answered HTTP ${HTTP_STATUS} for the plan: run the command again; if it stays, look at the log of the server" ;;
      *)
        code="$(problem_code "${PLAN_FILE}")"
        detail="$(problem_detail "${PLAN_FILE}")"
        [ -n "${code}" ] || code="http_${HTTP_STATUS}"
        die plan_rejected "the server refused the plan: ${code} (HTTP ${HTTP_STATUS})${detail:+ - ${detail}}: $(plan_hint "${code}")" ;;
    esac
  done
}

# True when the value is the plan URL of an artifact: https (unless --insecure), on the origin of --server, no path tricks, and the
# file name that the installer expects.
check_plan_url() { # name url file
  local name="$1" url="$2" file="$3" rest
  require_https "${url}" "the ${name} URL of the plan"
  case "${url}" in
    "${SERVER}/"*) ;;
    *) die plan_invalid "the ${name} URL of the plan is not on the origin of --server: the installer downloads from the server it was started for only" ;;
  esac
  rest="${url#"${SERVER}"}"
  case "${rest}" in
    *"//"* | *"/../"* | *"/./"* | */.. | */.) die plan_invalid "the ${name} URL of the plan has a path that is not plain" ;;
  esac
  [ "${url##*/}" = "${file}" ] || die plan_invalid "the ${name} URL of the plan does not end in the file name ${file}"
}

# The OpAMP endpoint of the plan names the host and port of --server (the server derives it from the same address), with the
# scheme that goes with it: wss or https for an https server, ws or http for a lab server.
check_plan_endpoint() {
  local url="$1" scheme authority
  scheme="${url%%://*}"
  authority="${url#*://}"
  authority="${authority%%/*}"
  case "${SERVER%%://*}:${scheme}" in
    https:wss | https:https | http:ws | http:http) ;;
    *) die plan_invalid "opamp.endpoint of the plan does not use the scheme that goes with --server (wss:// or https:// for an https server)" ;;
  esac
  [ "${authority}" = "${SERVER#*://}" ] || die plan_invalid "opamp.endpoint of the plan names another host or port than --server: the enrollment token would go to a server that was not asked for"
}

plan_value_check() { # key value
  local key="$1" value="$2" re_url re_version re_grant
  re_url='^(https?|wss?)://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?(/[A-Za-z0-9._:/@+=-]*)?$'
  re_version='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
  re_grant='^myrd_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$'
  [ -n "${value}" ] || die plan_invalid "the plan has no value for ${key}"
  case "${key}" in
    format) [ "${value}" = 1 ] || die plan_invalid "the plan has the format ${value:0:20}, this installer reads format 1: use the installer bundle of the server" ;;
    fleet) [[ "${value}" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] || die plan_invalid "fleet in the plan is not the name of a fleet" ;;
    version.source) case "${value}" in git | ui | rollout | request) ;; *) die plan_invalid "version.source in the plan is not a source this installer knows" ;; esac ;;
    opamp.endpoint | collector.url | supervisor.url)
      [[ "${value}" =~ ${re_url} ]] || die plan_invalid "${key} in the plan is not an http(s) or ws(s) URL of the allowed shape" ;;
    collector.version | supervisor.version)
      { [[ "${value}" =~ ${re_version} ]] && [ "${#value}" -le 64 ]; } || die plan_invalid "${key} in the plan is not a version" ;;
    collector.grant | supervisor.grant) [[ "${value}" =~ ${re_grant} ]] || die plan_invalid "${key} in the plan is not an install grant" ;;
    updater.install) case "${value}" in true | false) ;; *) die plan_invalid "updater.install in the plan must be true or false" ;; esac ;;
  esac
}

# Reads the plan as data (ADR-0046, "Interface sketch"; internal/install/grammar.go is the reference): at most 8 KiB and 64
# lines of "key=value", only the characters A-Z a-z 0-9 . _ : / @ + = - and the brackets of an IPv6 literal, the key "format" first
# and equal to 1, only known keys, each exactly once, every value against the pattern of its key. Nothing in it is run.
parse_plan() { # file
  local f="$1" size good lines last line key value n=0 seen=" " known=" format fleet version.source opamp.endpoint collector.version collector.url collector.grant supervisor.version supervisor.url supervisor.grant updater.install "
  size="$(wc -c < "${f}" | tr -d ' ')"
  [ "${size}" -gt 0 ] || die plan_invalid "the plan of the server is empty"
  [ "${size}" -le "${PLAN_MAX_BYTES}" ] || die plan_invalid "the plan of the server is longer than ${PLAN_MAX_BYTES} bytes"
  good="$(LC_ALL=C tr -cd 'A-Za-z0-9._:/@+=\133\135\012-' < "${f}" | wc -c | tr -d ' ')"
  [ "${good}" = "${size}" ] || die plan_invalid "the plan of the server holds a character that a plan may not have (only letters, digits and . _ : / @ + = - and the brackets of an IPv6 address)"
  last="$(tail -c 1 "${f}" | od -An -tx1 | tr -d ' \n')"
  [ "${last}" = 0a ] || die plan_invalid "the plan of the server does not end in a line end"
  lines="$(wc -l < "${f}" | tr -d ' ')"
  [ "${lines}" -le "${PLAN_MAX_LINES}" ] || die plan_invalid "the plan of the server has more than ${PLAN_MAX_LINES} lines"
  while IFS= read -r line; do
    n=$((n + 1))
    case "${line}" in *=*) ;; *) die plan_invalid "line ${n} of the plan has no =" ;; esac
    key="${line%%=*}"
    value="${line#*=}"
    if [ "${n}" = 1 ] && [ "${key}" != format ]; then die plan_invalid "the plan does not start with the key format"; fi
    case "${known}" in *" ${key} "*) ;; *) die plan_invalid "line ${n} of the plan has a key this installer does not know: ${key:0:40}" ;; esac
    case "${seen}" in *" ${key} "*) die plan_invalid "the key ${key} is repeated in the plan" ;; esac
    seen="${seen}${key} "
    plan_value_check "${key}" "${value}"
    case "${key}" in
      format) PLAN_FORMAT="${value}" ;;
      fleet) PLAN_FLEET="${value}" ;;
      version.source) PLAN_SOURCE="${value}" ;;
      opamp.endpoint) PLAN_OPAMP="${value}" ;;
      collector.version) PLAN_COLLECTOR_VERSION="${value}" ;;
      collector.url) PLAN_COLLECTOR_URL="${value}" ;;
      collector.grant) PLAN_COLLECTOR_GRANT="${value}" ;;
      supervisor.version) PLAN_SUPERVISOR_VERSION="${value}" ;;
      supervisor.url) PLAN_SUPERVISOR_URL="${value}" ;;
      supervisor.grant) PLAN_SUPERVISOR_GRANT="${value}" ;;
      updater.install) PLAN_UPDATER="${value}" ;;
    esac
  done < "${f}"
  for key in format fleet version.source opamp.endpoint collector.version collector.url collector.grant supervisor.version supervisor.url supervisor.grant updater.install; do
    case "${seen}" in *" ${key} "*) ;; *) die plan_invalid "the plan lacks the key ${key}" ;; esac
  done
}

# Checks what the plan says against what this host asked for and against the origin of --server, and takes it over.
apply_plan() {
  local v
  [ -z "${FLEET}" ] || [ "${PLAN_FLEET}" = "${FLEET}" ] || die plan_invalid "the plan names the fleet ${PLAN_FLEET}, not ${FLEET}: leave --fleet out, or correct the site options"
  for v in "${PLAN_COLLECTOR_VERSION}" "${PLAN_SUPERVISOR_VERSION}"; do
    [[ "${v}" =~ ${VERSION_RE} ]] || die plan_invalid "the plan names the version ${v:0:64}, which this installer cannot install (a plain X.Y.Z or X.Y.Z-pre.release)"
  done
  [ -z "${COLLECTOR_VERSION}" ] || [ "${PLAN_COLLECTOR_VERSION}" = "${COLLECTOR_VERSION}" ] \
    || die plan_invalid "the plan names the collector version ${PLAN_COLLECTOR_VERSION}, not the ${COLLECTOR_VERSION} that --collector-version asked for"
  check_plan_endpoint "${PLAN_OPAMP}"
  check_plan_url collector "${PLAN_COLLECTOR_URL}" "${COLLECTOR_ASSET_PREFIX}_${PLAN_COLLECTOR_VERSION}_linux_${ARCH}.tar.gz"
  check_plan_url supervisor "${PLAN_SUPERVISOR_URL}" "${SUPERVISOR_ASSET_PREFIX}_${PLAN_SUPERVISOR_VERSION}_linux_${ARCH}"
  COLLECTOR_VERSION="${PLAN_COLLECTOR_VERSION}"
  SUPERVISOR_VERSION="${PLAN_SUPERVISOR_VERSION}"
  [ -n "${OPAMP_ENDPOINT}" ] || OPAMP_ENDPOINT="${PLAN_OPAMP}"
  [ "${PLAN_UPDATER}" = true ] && WITH_UPDATER=1
  log "plan: fleet ${PLAN_FLEET}, collector v${COLLECTOR_VERSION} and supervisor v${SUPERVISOR_VERSION} (version from ${PLAN_SOURCE}), updater: ${PLAN_UPDATER}"
}

# The verifier and the updater come from the installer bundle this script was started from: the myriarch-updater next to deploy/,
# a regular file of the archive whose SHA-256 the command pinned. It is copied into the private working directory first and that
# copy is used for the checks and installed, so that nothing can replace it in between (install_updater hashes it again).
prepare_bundle_updater() {
  local here root src copy
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  root="$(cd "${here}/../.." && pwd)"
  src="${root}/${UPDATER_BINARY_NAME}"
  if [ -f "${src}" ] && [ ! -L "${src}" ] && [ -x "${src}" ]; then
    copy="${TMP_DIR}/${UPDATER_BINARY_NAME}"
    cp "${src}" "${copy}"
    chmod 0755 "${copy}"
    BUNDLE_UPDATER_SHA="$(sha256_of "${copy}")"
    UPDATER_BIN="${copy}"
    [ -n "${VERIFIER}" ] || VERIFIER="${copy}"
    return 0
  fi
  [ -z "${VERIFIER}" ] || [ "${WITH_UPDATER}" = 0 ] \
    || die file_unreadable "the plan asks for the updater, but the installer bundle has no regular, executable file ${src}: run the installer from the unpacked installer bundle of the server"
  [ -n "${VERIFIER}" ] \
    || die sigstore_verifier_missing "no Sigstore verifier: the installer bundle has no regular, executable file ${src}; run the installer from the unpacked installer bundle of the server (the command of the web UI does), or pass --verifier <cosign ${MIN_COSIGN_VERSION} or newer>"
}

# The download of the server mode: only from the origin of --server, with the grant of the artifact as the bearer.
download_from_server() { # url dest
  local url="$1" dest="$2" why
  SERVER_HEADERS="${TMP_DIR}/response.headers"
  case "${url}" in
    "${SERVER}/"*) ;;
    *) die plan_invalid "a download outside the origin of --server was asked for: ${url}" ;;
  esac
  server_call "${DOWNLOAD_RETRY_SECONDS}" "408 429 500 502 503 504" GET "${url}" "${dest}" "" "${DL_BEARER}" "" 900
  [ "${CURL_RC}" = 0 ] || die download_failed "download failed: ${url} (curl exit code ${CURL_RC}; check the network and the certificate of the server, --ca-file)"
  case "${HTTP_STATUS}" in
    200) return 0 ;;
    401) why="the install grant was refused, it expired or the enrollment token was revoked: run the command again to get a new plan" ;;
    404) why="the server has no such artifact: run the command again to get a new plan" ;;
    *) why="run the command again; if it stays, look at the log of the server" ;;
  esac
  die download_failed "download failed: ${url} (HTTP ${HTTP_STATUS}; ${why})"
}

# --collector-version latest: the redirect of the page of the newest release names it. On request only.
latest_collector_version() {
  have curl || die tool_missing "curl is required to look up the newest collector release"
  local head loc
  head="$(curl --silent --show-error --head --proto '=https' --tlsv1.2 --connect-timeout 20 --max-time 60 "${LATEST_RELEASE_URL}" 2>/dev/null)" \
    || die download_failed "could not ask GitHub for the newest collector release: name the version with --collector-version X.Y.Z"
  loc="$(printf '%s\n' "${head}" | tr -d '\r' | awk 'tolower($1) == "location:" { v = $2 } END { print v }')"
  [[ "${loc}" =~ ^https://github\.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v([0-9]+\.[0-9]+\.[0-9]+)$ ]] \
    || die download_failed "GitHub did not name the newest collector release in the expected form: name the version with --collector-version X.Y.Z"
  printf '%s' "${BASH_REMATCH[1]}"
}

# Without --server: the version is the one given, the newest on request, else the tested one of this release.
resolve_collector_version() {
  if [ -z "${COLLECTOR_VERSION}" ]; then
    COLLECTOR_VERSION="${DEFAULT_COLLECTOR_VERSION}"
    log "no --collector-version: installing ${COLLECTOR_VERSION}, the version this release was tested with (--collector-version latest asks GitHub for the newest)"
  elif [ "${COLLECTOR_VERSION}" = latest ]; then
    COLLECTOR_VERSION="$(latest_collector_version)"
    log "--collector-version latest: the newest collector release is ${COLLECTOR_VERSION}"
  fi
  [ -n "${SUPERVISOR_VERSION}" ] || SUPERVISOR_VERSION="${COLLECTOR_VERSION}"
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
  local url="${base}/${asset}"
  [ -z "${SERVER}" ] || url="${PLAN_COLLECTOR_URL}"
  local vdir="${P_COLLECTOR}/versions/${COLLECTOR_VERSION}"
  local current=""
  if [ -L "${P_COLLECTOR}/current" ]; then current="$(basename "$(readlink "${P_COLLECTOR}/current")")"; fi

  if [ -x "${vdir}/${COLLECTOR_ASSET_PREFIX}" ] && [ -f "${vdir}/.myriarch-install.json" ]; then
    log "Collector v${COLLECTOR_VERSION} is already installed"
  else
    DL_BEARER="${PLAN_COLLECTOR_GRANT}"
    fetch_verified "${url}" "${TMP_DIR}/${asset}"
    verify_signature "${TMP_DIR}/${asset}" "${url}.sigstore.json" "$(cert_identity base-release "${COLLECTOR_VERSION}")" "${COLLECTOR_VERSION}"
    DL_BEARER=""
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
  local target="${P_SUPERVISOR}/opampsupervisor" url
  url="${base}/${asset}"
  [ -z "${SERVER}" ] || url="${PLAN_SUPERVISOR_URL}"
  if [ -x "${target}" ] && [ -f "${P_SUPERVISOR}/VERSION" ] && [ "$(cat "${P_SUPERVISOR}/VERSION")" = "${SUPERVISOR_VERSION}" ]; then
    log "Supervisor v${SUPERVISOR_VERSION} is already installed"
    return 0
  fi
  DL_BEARER="${PLAN_SUPERVISOR_GRANT}"
  fetch_verified "${url}" "${TMP_DIR}/${asset}"
  verify_signature "${TMP_DIR}/${asset}" "${url}.sigstore.json" "$(cert_identity base-binary-release "${SUPERVISOR_VERSION}")" "${SUPERVISOR_VERSION}"
  DL_BEARER=""
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
  # the updater of the installer bundle is hashed again when it is copied: it is the file that was used as the verifier
  if [ -n "${BUNDLE_UPDATER_SHA}" ] && [ "$(sha256_of "${bin}")" != "${BUNDLE_UPDATER_SHA}" ]; then
    die checksum_mismatch "SHA-256 of the myriarch-updater of the installer bundle changed during the installation"
  fi
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
  check_foreign_installations
  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-install.XXXXXX")"
  if [ -n "${SERVER}" ]; then
    fetch_plan
    parse_plan "${PLAN_FILE}"
    apply_plan
    prepare_bundle_updater
  else
    resolve_collector_version
  fi
  log "installing: collector v${COLLECTOR_VERSION}, supervisor v${SUPERVISOR_VERSION}, linux/${ARCH}, site ${SITE_TYPE}/${SITE_ID}"
  if [ -n "${SERVER}" ]; then
    resolve_verifier
  elif [ "${WITH_UPDATER}" = 1 ] && [ -n "${UPDATER_CHECKSUMS_URL}" ]; then
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
  check_supervisor_binary
  install_config
  install_units
  install_updater
  activate_services
  log "done. The host reports to ${OPAMP_ENDPOINT} as an agent"
}

main
