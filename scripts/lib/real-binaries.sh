# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Download and verification of the real collector binaries for scripts/e2e.sh (the upstream OpAMP Supervisor and
# otelcol-contrib of one release). Sourced, not executed. Needs bash, curl, tar and python3; no other dependency.
#
#   real_platform                       prints <os>_<arch> (darwin|linux, amd64|arm64); status 1 on any other platform
#   real_prepare DIR VERSION BASE       downloads both assets below BASE, verifies them against the published .sha256
#                                       files and installs DIR/otelcol-contrib and DIR/opampsupervisor
#   real_fetch_verified BASE REL DEST   one asset: BASE/REL and BASE/REL.sha256 to DEST and DEST.sha256
#
# Trust: the .sha256 files come from the same release as the assets, so the check detects corrupt or truncated downloads
# and a cache that changed afterwards, not a compromised release. Every run verifies the kept files again. An asset that
# does not match its checksum is deleted and the call fails; nothing is installed from an unverified file.
#
# Test hooks: REAL_UNAME_S and REAL_UNAME_M replace uname; BASE may be a file:// URL.

REAL_LAST_SHA=""   # SHA-256 of the file that real_fetch_verified verified last

real_log() { printf '%s [real-binaries] %s\n' "$(date +%T)" "$*" >&2; }

real_platform() {
  local os arch
  case "${REAL_UNAME_S:-$(uname -s)}" in Darwin) os=darwin ;; Linux) os=linux ;; *) return 1 ;; esac
  case "${REAL_UNAME_M:-$(uname -m)}" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) return 1 ;; esac
  printf '%s_%s\n' "${os}" "${arch}"
}

real_sha256() { # file
  python3 -I -c 'import hashlib, sys
h = hashlib.sha256()
with open(sys.argv[1], "rb") as f:
    for block in iter(lambda: f.read(1 << 20), b""):
        h.update(block)
print(h.hexdigest())' "$1"
}

# real_check FILE SUMFILE: the first field of SUMFILE is 64 hex digits and equals the SHA-256 of FILE (case-insensitive)
real_check() {
  local file="$1" sumfile="$2" want got
  [ -s "${sumfile}" ] || { real_log "ERROR: ${sumfile} is missing or empty"; return 1; }
  want="$(head -n 1 "${sumfile}" | tr -d '\r' | awk '{print $1}' | tr 'A-F' 'a-f')"
  case "${want}" in
    *[!0-9a-f]*|"") real_log "ERROR: ${sumfile} does not start with a SHA-256 (64 hex digits)"; return 1 ;;
  esac
  [ "${#want}" = 64 ] || { real_log "ERROR: ${sumfile} does not start with a SHA-256 (64 hex digits)"; return 1; }
  got="$(real_sha256 "${file}")" || { real_log "ERROR: cannot hash ${file}"; return 1; }
  if [ "${got}" != "${want}" ]; then
    real_log "ERROR: SHA-256 mismatch for $(basename "${file}"): published ${want}, computed ${got}"
    return 1
  fi
  REAL_LAST_SHA="${got}"
}

real_download() { # url dest
  curl -fsSL --proto '=https,file' --tlsv1.2 --retry 3 --connect-timeout 20 -o "$2" "$1" \
    || { real_log "ERROR: download of $1 failed"; return 1; }
}

real_fetch_verified() { # base rel dest
  local base="$1" rel="$2" dest="$3" name
  name="$(basename "${dest}")"
  mkdir -p "$(dirname "${dest}")" || return 1
  if [ -f "${dest}" ] && [ -f "${dest}.sha256" ]; then
    if real_check "${dest}" "${dest}.sha256"; then
      real_log "${name}: SHA-256 ${REAL_LAST_SHA} verified (kept from an earlier run)"
      return 0
    fi
    rm -f "${dest}" "${dest}.sha256"   # deleted: the next run downloads it again
    return 1
  fi
  rm -f "${dest}" "${dest}.sha256" "${dest}.part" "${dest}.sha256.part"
  if ! { real_download "${base}/${rel}" "${dest}.part" && real_download "${base}/${rel}.sha256" "${dest}.sha256.part"; }; then
    rm -f "${dest}.part" "${dest}.sha256.part"
    return 1
  fi
  if ! real_check "${dest}.part" "${dest}.sha256.part"; then
    rm -f "${dest}.part" "${dest}.sha256.part"
    return 1
  fi
  mv "${dest}.sha256.part" "${dest}.sha256" && mv "${dest}.part" "${dest}" || return 1
  real_log "${name}: downloaded, SHA-256 ${REAL_LAST_SHA} verified"
}

# Status 0 = installed, 1 = download or verification failed, 2 = platform not supported.
real_prepare() { # dir version base
  local dir="$1" version="$2" base="$3" plat contrib sup sha_contrib sha_sup tmp
  plat="$(real_platform)" || { real_log "no release assets for $(uname -s)/$(uname -m)"; return 2; }
  contrib="otelcol-contrib_${version}_${plat}.tar.gz"
  sup="opampsupervisor_${version}_${plat}"
  mkdir -p "${dir}/dl" || return 1
  real_fetch_verified "${base}" "v${version}/${contrib}" "${dir}/dl/${contrib}" || return 1
  sha_contrib="${REAL_LAST_SHA}"
  real_fetch_verified "${base}" "cmd%2Fopampsupervisor%2Fv${version}/${sup}" "${dir}/dl/${sup}" || return 1
  sha_sup="${REAL_LAST_SHA}"
  tmp="$(mktemp -d "${dir}/extract.XXXXXX")" || return 1
  if ! tar -xzf "${dir}/dl/${contrib}" -C "${tmp}" otelcol-contrib || [ ! -f "${tmp}/otelcol-contrib" ]; then
    rm -rf "${tmp}"; real_log "ERROR: ${contrib} has no otelcol-contrib"; return 1
  fi
  chmod 0755 "${tmp}/otelcol-contrib"
  mv -f "${tmp}/otelcol-contrib" "${dir}/otelcol-contrib" && rm -rf "${tmp}" || return 1
  cp "${dir}/dl/${sup}" "${dir}/opampsupervisor.new" && chmod 0755 "${dir}/opampsupervisor.new" \
    && mv -f "${dir}/opampsupervisor.new" "${dir}/opampsupervisor" || return 1
  real_log "installed ${version} for ${plat}: otelcol-contrib sha256:${sha_contrib}, opampsupervisor sha256:${sha_sup}"
}
