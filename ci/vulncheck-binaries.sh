#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Scan of the released binaries for known vulnerabilities: govulncheck in binary mode over the programs of a published release, reported
# as SARIF for GitHub code scanning (.github/workflows/vulncheck.yml). The source scan (ci/vulncheck.sh) reads the code; this one reads
# what users run, so it also finds the vulnerabilities of a toolchain or a dependency that a release was built with after the release.
#
#   ci/vulncheck-binaries.sh [--tag vX.Y.Z] --out DIR
#
# Writes DIR/govulncheck-binaries.sarif. The release is --tag, else VULNCHECK_RELEASE_TAG, else the latest stable release of the
# repository in ci/release/identity.env (the one that https://github.com/<repository>/releases/latest leads to; a pre-release is scanned
# only when it is named). A repository without a published release has nothing to scan: the report holds no result and the status is 0.
#
# Nothing is scanned before the files are verified, and every step fails closed (the codes in brackets are the stable interface):
#   1. checksums.txt and its keyless bundle are downloaded over https, without credentials [download_failed];
#   2. ci/release/verify-blob.sh (mode bound) checks the bundle: the identity of release.yml of this repository at the tag of the
#      version, issued by GitHub Actions (ci/release/identity.env, ADR-0029) [signature_invalid]. Until this succeeds nothing else is downloaded;
#   3. every archive of myriarchd, myriarch-updater, myrctl and myriarch-sim that the verified checksums.txt lists for the version is
#      downloaded and compared with its SHA-256 there [checksum_mismatch, checksum_entry, no_archives];
#   4. only then the one binary of each archive is read out (the member named like the program, a regular file within a size limit;
#      nothing else is unpacked) [archive_invalid] and scanned with `govulncheck -mode binary -format sarif` [govulncheck_failed].
# Findings that ci/vulncheck-allowlist.txt accepts are left out of the report (ci/vulncheck-sarif.py). Binary mode reports the vulnerable
# symbols that a binary contains; it cannot tell whether the program reaches them, so it reports more than the source scan.
#
# Environment: TOOLS_BIN (cosign for the verification and govulncheck; default .cache/bin; the tools are never looked up in PATH),
# GOVULNCHECK (another govulncheck), VULNCHECK_ALLOWLIST (another allowlist), VULNCHECK_RELEASE_TAG, VULNCHECK_DOWNLOAD_RETRY_SECONDS
# (retry window of a download, default 120). The downloads retry only transient failures (a timeout, a failed connection, HTTP 408, 429,
# 500, 502, 503, 504) with pauses of 1, 2, 4, ... s, the policy of the installers and the smoke tests.
#
# Errors: one line "[vulncheck-binaries] ERROR [<code>] <message>" on stderr, exit 1. Usage errors exit 2.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/release/common.sh"

CI_DIR="$(cd "${RELEASE_LIB_DIR}/.." && pwd)"
USAGE="vulncheck-binaries.sh [--tag vX.Y.Z] --out DIR"
RETRY_SECONDS="${VULNCHECK_DOWNLOAD_RETRY_SECONDS:-120}"
MAX_SMALL=1048576        # checksums.txt and its bundle
MAX_ARCHIVE=209715200    # one archive: 200 MiB (the largest of v0.1.0 is 13 MiB)
MAX_BINARY=536870912     # one binary in an archive: 512 MiB
MAX_ARCHIVES=64
PROGRAMS='myriarchd|myriarch-updater|myrctl|myriarch-sim'

tag="${VULNCHECK_RELEASE_TAG:-}" out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${out}" ] || usage_err "${USAGE}"
check_text input_invalid "--out" "${out}"
if [ -n "${tag}" ]; then check_match tag_invalid "the release tag" "${tag}" "${TAG_RE}"; fi
RETRY_SECONDS_RE='^[0-9]{1,4}$'
check_match input_invalid "VULNCHECK_DOWNLOAD_RETRY_SECONDS" "${RETRY_SECONDS}" "${RETRY_SECONDS_RE}"

load_identity
have curl || err tool_missing "curl was not found in PATH"
have python3 || err tool_missing "python3 was not found in PATH"
govulncheck="${GOVULNCHECK:-${TOOLS_BIN}/govulncheck}"
[ -x "${govulncheck}" ] || err tool_missing "govulncheck is not executable at ${govulncheck} (ci/install-tools.sh govulncheck)"
allowlist="${VULNCHECK_ALLOWLIST:-${CI_DIR}/vulncheck-allowlist.txt}"
[ -f "${allowlist}" ] || err allowlist_missing "the allowlist ${allowlist} is missing"
mkdir -p "${out}"
report="${out}/govulncheck-binaries.sarif"

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-vulncheck-binaries.XXXXXX")"
trap 'rm -rf "${work}"' EXIT
mkdir -p "${work}/dl" "${work}/bin" "${work}/sarif"
chmod 700 "${work}"

# ---- downloads ------------------------------------------------------------------------------------------------------------------
# transient CURL_EXIT STATUS: a failed attempt is worth another one (the policy of install-linux.sh and ci/release/smoke.sh)
transient() {
  case "$1" in
    5 | 6 | 7 | 16 | 18 | 28 | 52 | 55 | 92) return 0 ;;
    22 | 56) case "$2" in 408 | 429 | 500 | 502 | 503 | 504) return 0 ;; 000) [ "$1" = 56 ] && return 0 ;; esac ;;
  esac
  return 1
}

# retry_curl WHAT CURL_ARGS...: runs curl (stdout is its -w output), retries a transient failure, and prints the output of the last
# attempt; a failure that is not transient, or one after the retry window, is [download_failed]
retry_curl() {
  local what="$1" attempt=0 pause=1 start="${SECONDS}" rc output status elapsed noun detail
  shift
  while :; do
    attempt=$((attempt + 1))
    rc=0
    output="$(curl -q --proto '=https' --proto-redir '=https' --tlsv1.2 -sS --connect-timeout 20 --max-time 900 "$@" 2> "${work}/curl.err")" || rc=$?
    if [ "${rc}" = 0 ]; then printf '%s' "${output}"; return 0; fi
    status="${output%% *}"; status="${status:-000}"
    detail="$(grep -v '^[[:space:]]*$' "${work}/curl.err" | tail -n 1 | tr -d '[:cntrl:]' | cut -c 1-200 || true)"
    noun=attempts; [ "${attempt}" = 1 ] && noun=attempt
    if ! transient "${rc}" "${status}"; then
      err download_failed "${what} failed (curl exit code ${rc}, HTTP ${status}; not retried, ${attempt} ${noun}): ${detail}"
    fi
    elapsed=$((SECONDS - start))
    if [ "${elapsed}" -ge "${RETRY_SECONDS}" ]; then
      err download_failed "${what} failed (curl exit code ${rc}, HTTP ${status}; the retry window of ${RETRY_SECONDS} s is used up, ${attempt} ${noun}): ${detail}"
    fi
    [ $((elapsed + pause)) -le "${RETRY_SECONDS}" ] || pause=$((RETRY_SECONDS - elapsed))
    info "${what} failed (curl exit code ${rc}, HTTP ${status}: ${detail}), next attempt in ${pause} s"
    sleep "${pause}"
    pause=$((pause * 2))
  done
}

# fetch URL DEST MAX_BYTES: an anonymous download over https; redirects stay on https
fetch() {
  retry_curl "the download of $1" -fL --max-filesize "$3" -w '%{http_code}' -o "$2" "$1" > /dev/null
}

# ---- the release ----------------------------------------------------------------------------------------------------------------
empty_report() { # reason: a report without a result, so that code scanning closes the alerts of an earlier release
  info "$1"
  python3 -I "${CI_DIR}/vulncheck-sarif.py" binaries "${allowlist}" "${report}" none
  summary "### Vulnerability scan of the released binaries" "" "$1"
}

repo_url="https://github.com/${RELEASE_REPOSITORY}"
if [ -z "${tag}" ]; then
  # one request without credentials: a published, non-draft, non-pre-release release redirects to its tag page
  answer="$(retry_curl "the lookup of the latest release" -o /dev/null -w '%{http_code} %{redirect_url}' "${repo_url}/releases/latest")"
  code="${answer%% *}"
  location="${answer#* }"
  case "${code}" in
    301 | 302 | 303 | 307 | 308) ;;
    404) empty_report "${repo_url} has no published release: nothing to scan"; exit 0 ;;
    *) err release_lookup "the lookup of the latest release answered HTTP ${code}" ;;
  esac
  case "${location}" in
    "${repo_url}/releases" | "${repo_url}/releases/") empty_report "${repo_url} has no stable release: nothing to scan"; exit 0 ;;
    "${repo_url}/releases/tag/"*) tag="${location#"${repo_url}/releases/tag/"}" ;;
    *) err release_lookup "the latest release leads to an unexpected location" ;;
  esac
  check_match tag_invalid "the tag of the latest release" "${tag}" "${TAG_RE}"
fi
version="${tag#v}"
info "release ${tag}"
base="${repo_url}/releases/download/${tag}"

# 1 and 2: the signed list of checksums; nothing else is fetched before the signature is valid
fetch "${base}/checksums.txt" "${work}/dl/checksums.txt" "${MAX_SMALL}"
fetch "${base}/checksums.txt.sigstore.json" "${work}/dl/checksums.txt.sigstore.json" "${MAX_SMALL}"
TOOLS_BIN="${TOOLS_BIN}" "${RELEASE_LIB_DIR}/verify-blob.sh" --mode bound --bundle "${work}/dl/checksums.txt.sigstore.json" \
  --repository "${RELEASE_REPOSITORY}" --workflow "${RELEASE_WORKFLOW}" --tag "${tag}" "${work}/dl/checksums.txt" \
  || err signature_invalid "checksums.txt of ${tag} is not signed by the release workflow of ${RELEASE_REPOSITORY}; nothing was scanned"

# 3: the archives that the verified list names
version_re="$(printf '%s' "${version}" | regex_escape)"
line_re="^([0-9a-f]{64})  (${PROGRAMS})_${version_re}_(linux|darwin|windows)_(amd64|arm64)\\.(tar\\.gz|zip)\$"
names=() sums=() programs=() oses=()
seen=" "
while IFS= read -r line || [ -n "${line}" ]; do
  [[ "${line}" =~ ${line_re} ]] || continue
  name="${line:66}"
  case "${seen}" in *" ${name} "*) err checksum_entry "checksums.txt lists ${name} more than once" ;; esac
  seen="${seen}${name} "
  sums+=("${BASH_REMATCH[1]}"); programs+=("${BASH_REMATCH[2]}"); oses+=("${BASH_REMATCH[3]}"); names+=("${name}")
done < "${work}/dl/checksums.txt"
[ "${#names[@]}" -gt 0 ] || err no_archives "the signed checksums.txt of ${tag} lists no archive of ${PROGRAMS//|/, } for ${version}"
[ "${#names[@]}" -le "${MAX_ARCHIVES}" ] || err no_archives "the signed checksums.txt of ${tag} lists more than ${MAX_ARCHIVES} archives"
for i in "${!names[@]}"; do
  fetch "${base}/${names[${i}]}" "${work}/dl/${names[${i}]}" "${MAX_ARCHIVE}"
  got="$(sha256_of "${work}/dl/${names[${i}]}")"
  [ "${got}" = "${sums[${i}]}" ] || err checksum_mismatch "${names[${i}]} does not match checksums.txt (expected ${sums[${i}]}, got ${got}); nothing was scanned"
done
info "verified the signature of checksums.txt and the SHA-256 of ${#names[@]} archive(s)"

# 4: the binaries, one per archive (the member named like the program), then the scan
labelled=()
for i in "${!names[@]}"; do
  member="${programs[${i}]}"
  if [ "${oses[${i}]}" = windows ]; then member="${member}.exe"; fi
  binary="${work}/bin/${names[${i}]}.bin"
  python3 -I - "${work}/dl/${names[${i}]}" "${member}" "${binary}" "${MAX_BINARY}" <<'PY' || err archive_invalid "${names[${i}]} does not hold ${member} as a regular file within the size limit"
import os
import sys
import tarfile
import zipfile

archive, member, dest, limit = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])


def copy(src):
    """Writes the member to dest (a new file, mode 0600) and refuses more than limit bytes."""
    size = 0
    fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as dst:
        while True:
            chunk = src.read(1 << 20)
            if not chunk:
                return
            size += len(chunk)
            if size > limit:
                raise ValueError("larger than the limit")
            dst.write(chunk)


def run():
    if archive.endswith(".zip"):
        with zipfile.ZipFile(archive) as z:
            infos = [i for i in z.infolist() if i.filename == member]
            if len(infos) != 1 or infos[0].is_dir() or ((infos[0].external_attr >> 16) & 0o170000) not in (0, 0o100000):
                raise ValueError("no single regular member")
            if infos[0].file_size > limit:
                raise ValueError("larger than the limit")
            with z.open(infos[0]) as src:
                copy(src)
    else:
        with tarfile.open(archive, "r:gz") as t:
            infos = [m for m in t.getmembers() if m.name in (member, "./" + member)]
            if len(infos) != 1 or not infos[0].isreg() or infos[0].size > limit:
                raise ValueError("no single regular member within the limit")
            with t.extractfile(infos[0]) as src:
                copy(src)


try:
    run()
except (OSError, ValueError, tarfile.TarError, zipfile.BadZipFile) as e:
    print(e, file=sys.stderr)
    sys.exit(1)
PY
  info "scanning ${names[${i}]} (${member})"
  "${govulncheck}" -mode binary -format sarif "${binary}" > "${work}/sarif/${i}.sarif" 2> "${work}/sarif/${i}.err" \
    || err govulncheck_failed "govulncheck failed for ${member} of ${names[${i}]}: $(grep -v '^[[:space:]]*$' "${work}/sarif/${i}.err" | tail -n 1 | tr -d '[:cntrl:]' | cut -c 1-200)"
  labelled+=("${names[${i}]}=${work}/sarif/${i}.sarif")
done

python3 -I "${CI_DIR}/vulncheck-sarif.py" binaries "${allowlist}" "${report}" "${tag}" "${labelled[@]}"
findings="$(python3 -I -c 'import json, sys; print(sum(len(r.get("results") or []) for r in json.load(open(sys.argv[1]))["runs"]))' "${report}")"
info "${#names[@]} binaries of ${tag} scanned, ${findings} finding(s) for code scanning in ${report}"
summary "### Vulnerability scan of the released binaries" "" \
  "Release ${tag}: the signature of checksums.txt and the SHA-256 of ${#names[@]} archives were verified before the scan; the binaries were scanned with govulncheck in binary mode. ${findings} finding(s) went to code scanning (the findings that \`ci/vulncheck-allowlist.txt\` accepts are left out)."
