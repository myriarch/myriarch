#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Shared helpers of the release scripts in ci/release (ADR-0035). Source it with `. "$(dirname "${BASH_SOURCE[0]}")/common.sh"`.
# It defines functions and the variables SCRIPT, ROOT and TOOLS_BIN and prints nothing. It is plain bash 3.2 (the macOS system
# bash runs the tests) and needs no tool beyond sh, sed, grep, awk, tr, python3 (-I) and a SHA-256 program.
#
# Conventions of every release script (docs/design in pr7-design.md, section 3):
#   - an error is one line "[<script>] ERROR [<code>] <message>" on stderr and exit status 1; the codes are the stable interface
#     that the tests match; a usage error is "[<script>] usage: ..." and exit status 2; info lines are "[<script>] <message>";
#     machine output (GITHUB_OUTPUT lines) goes to stdout only
#   - every input value is matched as a whole string and a control character is refused first
#   - tools come from ${TOOLS_BIN} only (never from PATH) and are checked against the pins of ci/versions.env once
#   - the signing identity comes from ci/release/identity.env and cannot be overridden from the environment

# shellcheck disable=SC2034  # the expressions below are used by the scripts that source this file
if [ -z "${RELEASE_COMMON_LOADED:-}" ]; then
RELEASE_COMMON_LOADED=1
RELEASE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${RELEASE_LIB_DIR}/../.." && pwd)"
TOOLS_BIN="${TOOLS_BIN:-${ROOT}/.cache/bin}"
SCRIPT="$(basename "${BASH_SOURCE[1]:-$0}" .sh)"

# Regular expressions shared by the scripts (used by the scripts that source this file).
TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'
VERSION_RE='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'
SNAPSHOT_VERSION_RE='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*|-snapshot)?$'
COMMIT_RE='^[0-9a-f]{40}$'
DIGEST_RE='^sha256:[0-9a-f]{64}$'
SHA256_RE='^[0-9a-f]{64}$'

info() { printf '[%s] %s\n' "${SCRIPT}" "$*" >&2; }
err() { # code message
  printf '[%s] ERROR [%s] %s\n' "${SCRIPT}" "$1" "$2" >&2
  exit 1
}
usage_err() { # message
  printf '[%s] usage: %s\n' "${SCRIPT}" "$*" >&2
  exit 2
}
have() { command -v "$1" >/dev/null 2>&1; }

# check_text <code> <what> <value>: no control character (a newline cannot carry a second line into a command or a file)
check_text() {
  case "$3" in
    *[[:cntrl:]]*) err "$1" "$2 contains a control character" ;;
  esac
}

# check_match <code> <what> <value> <regex>: the whole value matches the regex
check_match() {
  check_text "$1" "$2" "$3"
  [[ "$3" =~ $4 ]] || err "$1" "$2 has an unexpected format"
}

# need_value <option> <count of remaining arguments>: for option parsers
need_value() { [ "$2" -ge 2 ] || usage_err "$1 needs a value"; }

# regex_escape: stdin to stdout, escapes the characters of an ERE (the form of cert_identity_release in install-linux.sh)
regex_escape() { sed -e 's/[][\\.*+?(){}|^$]/\\&/g'; }

sha256_of() { # file
  if have sha256sum; then sha256sum "$1" | awk '{print $1}'
  elif have shasum; then shasum -a 256 "$1" | awk '{print $1}'
  else err tool_missing "neither sha256sum nor shasum was found"
  fi
}

# --- the frozen identity ------------------------------------------------------------------------------------------------------
# load_identity: reads ci/release/identity.env as data into RELEASE_REPOSITORY, RELEASE_WORKFLOW, RELEASE_OIDC_ISSUER,
# RELEASE_TRIGGER, RELEASE_IMAGE_SERVER and RELEASE_IMAGE_UPDATER. Nothing is evaluated and the environment does not override a value.
load_identity() {
  local file="${RELEASE_LIB_DIR}/identity.env" line key value seen=" "
  [ -f "${file}" ] || err identity_env "ci/release/identity.env is missing"
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in ''|'#'*) continue ;; esac
    check_text identity_env "a line of identity.env" "${line}"
    key="${line%%=*}"
    value="${line#*=}"
    case "${key}" in
      RELEASE_REPOSITORY|RELEASE_WORKFLOW|RELEASE_OIDC_ISSUER|RELEASE_TRIGGER|RELEASE_IMAGE_SERVER|RELEASE_IMAGE_UPDATER) ;;
      *) err identity_env "identity.env has an unexpected key" ;;
    esac
    case "${seen}" in *" ${key} "*) err identity_env "identity.env sets ${key} twice" ;; esac
    seen="${seen}${key} "
    [ -n "${value}" ] || err identity_env "identity.env has an empty value for ${key}"
    case "${key}" in
      RELEASE_REPOSITORY) RELEASE_REPOSITORY="${value}" ;;
      RELEASE_WORKFLOW) RELEASE_WORKFLOW="${value}" ;;
      RELEASE_OIDC_ISSUER) RELEASE_OIDC_ISSUER="${value}" ;;
      RELEASE_TRIGGER) RELEASE_TRIGGER="${value}" ;;
      RELEASE_IMAGE_SERVER) RELEASE_IMAGE_SERVER="${value}" ;;
      RELEASE_IMAGE_UPDATER) RELEASE_IMAGE_UPDATER="${value}" ;;
    esac
  done < "${file}"
  for key in RELEASE_REPOSITORY RELEASE_WORKFLOW RELEASE_OIDC_ISSUER RELEASE_TRIGGER RELEASE_IMAGE_SERVER RELEASE_IMAGE_UPDATER; do
    case "${seen}" in *" ${key} "*) ;; *) err identity_env "identity.env does not set ${key}" ;; esac
  done
  [[ "${RELEASE_REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || err identity_env "RELEASE_REPOSITORY is not owner/name"
  [[ "${RELEASE_WORKFLOW}" =~ ^[A-Za-z0-9_.][A-Za-z0-9_./-]*$ ]] || err identity_env "RELEASE_WORKFLOW is not a path"
  [[ "${RELEASE_OIDC_ISSUER}" =~ ^https://[A-Za-z0-9._:/-]+$ ]] || err identity_env "RELEASE_OIDC_ISSUER is not an https URL"
  [[ "${RELEASE_TRIGGER}" =~ ^[a-z_]+$ ]] || err identity_env "RELEASE_TRIGGER is not an event name"
  [[ "${RELEASE_IMAGE_SERVER}" =~ ^ghcr\.io/[a-z0-9._/-]+$ ]] || err identity_env "RELEASE_IMAGE_SERVER is not a ghcr.io repository"
  [[ "${RELEASE_IMAGE_UPDATER}" =~ ^ghcr\.io/[a-z0-9._/-]+$ ]] || err identity_env "RELEASE_IMAGE_UPDATER is not a ghcr.io repository"
}

# identity_exact <tag> [repository workflow]: the certificate identity of the run for exactly this tag
identity_exact() {
  printf 'https://github.com/%s/%s@refs/tags/%s' "${2:-${RELEASE_REPOSITORY}}" "${3:-${RELEASE_WORKFLOW}}" "$1"
}
# identity_prefix <repository> <workflow>: the ADR-0029 form up to and including the "v" of the tag
identity_pattern() { # [repository workflow]
  local repo wf
  repo="$(printf '%s' "${1:-${RELEASE_REPOSITORY}}" | regex_escape)"
  wf="$(printf '%s' "${2:-${RELEASE_WORKFLOW}}" | regex_escape)"
  printf '^https://github\\.com/%s/%s@refs/tags/v' "${repo}" "${wf}"
}
# identity_bound <version> [repository workflow]: the form of the installers, bound to one version
identity_bound() {
  local ver
  ver="$(printf '%s' "$1" | regex_escape)"
  printf '%s%s$' "$(identity_pattern "${2:-}" "${3:-}")" "${ver}"
}
# signer_workflow <tag>: the form gh attestation verify --signer-workflow takes
signer_workflow() { printf '%s/%s@refs/tags/%s' "${RELEASE_REPOSITORY}" "${RELEASE_WORKFLOW}" "$1"; }

# --- pins and tools -----------------------------------------------------------------------------------------------------------
# pin <NAME>: the value of NAME in ci/versions.env, read as data (the environment does not override a pin)
pin() {
  local file="${RELEASE_LIB_DIR}/../versions.env" line value=""
  [ -f "${file}" ] || err tool_version "ci/versions.env is missing"
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in "$1="*) value="${line#*=}"; break ;; esac
  done < "${file}"
  [ -n "${value}" ] || err tool_version "ci/versions.env has no pin $1"
  printf '%s' "${value}"
}

# need_tool <cosign|crane|gh> ...: the tool exists in TOOLS_BIN and prints its pinned version (once per process)
TOOLS_CHECKED=" "
need_tool() {
  local t want got
  for t in "$@"; do
    case "${TOOLS_CHECKED}" in *" ${t} "*) continue ;; esac
    [ -x "${TOOLS_BIN}/${t}" ] || err tool_missing "${t} is not in TOOLS_BIN (${TOOLS_BIN})"
    case "${t}" in
      cosign)
        want="$(pin COSIGN_VERSION)"
        got="$("${TOOLS_BIN}/cosign" version 2>&1 | sed -n 's/^GitVersion: *//p' | head -n 1)" || got=""
        [ "${got}" = "${want}" ] || err tool_version "cosign reports '${got:-nothing}', the pin is ${want}"
        ;;
      crane)
        want="$(pin CRANE_VERSION)"
        got="$("${TOOLS_BIN}/crane" version 2>&1 | head -n 1)" || got=""
        [ "${got}" = "${want}" ] || err tool_version "crane reports '${got:-nothing}', the pin is ${want}"
        ;;
      gh)
        want="$(pin GH_VERSION)"
        got="$("${TOOLS_BIN}/gh" --version 2>&1 | head -n 1)" || got=""
        case "${got}" in "gh version ${want#v}"|"gh version ${want#v} "*) ;; *) err tool_version "gh reports '${got:-nothing}', the pin is ${want#v}" ;; esac
        ;;
      *) err tool_missing "unknown tool ${t}" ;;
    esac
    TOOLS_CHECKED="${TOOLS_CHECKED}${t} "
  done
}

# --- the asset set -----------------------------------------------------------------------------------------------------------
# asset_set <version> <names|listed|kinds> [assets.json]: from deploy/release/assets.json (data, schema 1), sorted by name
#   names   all 42 names (stage final)
#   unsigned the 40 names of stage unsigned
#   listed  the 39 names that checksums.txt lists (archive, binary, sbom, license)
#   kinds   "<name> <kind>" for all 42
asset_set() {
  local file="${3:-${ROOT}/deploy/release/assets.json}"
  [ -f "${file}" ] || err assets_file "${file} is missing"
  python3 -I - "$1" "$2" "${file}" <<'PY' || err assets_file "deploy/release/assets.json is not a valid asset list"
import json, re, sys

version, mode, path = sys.argv[1:4]
KINDS = ("archive", "binary", "sbom", "license", "checksums", "signature", "provenance")
doc = json.load(open(path, encoding="utf-8"))
if doc.get("schema") != 1 or not isinstance(doc.get("assets"), list) or not doc["assets"]:
    sys.exit(1)
rows, seen = [], set()
for a in doc["assets"]:
    name, kind = a.get("name"), a.get("kind")
    if not isinstance(name, str) or kind not in KINDS or set(a) != {"name", "kind"}:
        sys.exit(1)
    name = name.replace("{version}", version)
    if not re.match(r"^[A-Za-z0-9][A-Za-z0-9._-]*$", name) or name in seen:
        sys.exit(1)
    seen.add(name)
    rows.append((name, kind))
rows.sort(key=lambda r: r[0].encode())
want = {
    "names": KINDS,
    "unsigned": ("archive", "binary", "sbom", "license", "checksums"),
    "listed": ("archive", "binary", "sbom", "license"),
    "kinds": KINDS,
}[mode]
for name, kind in rows:
    if kind in want:
        print("%s %s" % (name, kind) if mode == "kinds" else name)
PY
}

# image_signature_present <digest>: reads the JSON that `cosign verify` printed on stdin and succeeds when at least one entry is a
# signature made by `cosign sign` (critical.type https://sigstore.dev/cosign/sign/v1) for exactly this digest. cosign v3 verifies every
# Sigstore bundle that refers to the digest, so an attestation of the same identity (provenance, SBOM) would satisfy `cosign verify`
# although nothing signed the image itself.
image_signature_present() {
  python3 -I -c '
import json, sys

try:
    doc = json.load(sys.stdin)
except ValueError:
    sys.exit(1)
want = sys.argv[1]
ok = isinstance(doc, list) and any(
    isinstance(e, dict)
    and (e.get("critical") or {}).get("type") == "https://sigstore.dev/cosign/sign/v1"
    and ((e.get("critical") or {}).get("image") or {}).get("docker-manifest-digest") == want
    for e in doc)
sys.exit(0 if ok else 1)
' "$1"
}

# verify_image_signature <repository> <digest> <tag> <commit>: `cosign verify` of REPO@DIGEST against the exact identity of the run (the
# same claims as `verify-blob.sh --mode exact`); the identity values are those of identity.env. The output must contain a cosign
# signature of the digest, not only an attestation (see image_signature_present). [signature_invalid]
verify_image_signature() {
  local errfile outfile last
  need_tool cosign
  errfile="$(mktemp "${TMPDIR:-/tmp}/myriarch-verify-image.XXXXXX")"
  outfile="${errfile}.out"
  if ! "${TOOLS_BIN}/cosign" verify \
      --certificate-identity "$(identity_exact "$3")" \
      --certificate-oidc-issuer "${RELEASE_OIDC_ISSUER}" \
      --certificate-github-workflow-repository "${RELEASE_REPOSITORY}" \
      --certificate-github-workflow-ref "refs/tags/$3" \
      --certificate-github-workflow-sha "$4" \
      --certificate-github-workflow-trigger "${RELEASE_TRIGGER}" \
      "$1@$2" > "${outfile}" 2> "${errfile}"; then
    last="$(grep -v '^[[:space:]]*$' "${errfile}" | tail -n 1 | cut -c 1-300 || true)"
    rm -f "${errfile}" "${outfile}"
    err signature_invalid "signature verification of $1@$2 failed: ${last:-cosign printed nothing}"
  fi
  if ! image_signature_present "$2" < "${outfile}"; then
    rm -f "${errfile}" "${outfile}"
    err signature_invalid "cosign verify of $1@$2 found no cosign signature of the digest (an attestation is not a signature)"
  fi
  rm -f "${errfile}" "${outfile}"
}

# need_gh: gh from TOOLS_BIN at the pinned version and a token in GH_TOKEN (gh reads it itself; it is never printed)
need_gh() {
  need_tool gh
  [ -n "${GH_TOKEN:-}" ] || err gh_token "GH_TOKEN is not set"
  check_text gh_token "GH_TOKEN" "${GH_TOKEN}"
}

# gh_json <description> <gh arguments...>: runs gh, writes its stdout to a new temp file and prints the path; a failure is [gh_failed],
# or - when GH_JSON_NOT_FOUND names a code and gh reports HTTP 404 - that code
gh_json() {
  local what="$1" f last
  shift
  f="$(mktemp "${TMPDIR:-/tmp}/myriarch-gh.XXXXXX")"
  if ! "${TOOLS_BIN}/gh" "$@" > "${f}" 2> "${f}.err"; then
    last="$(grep -v '^[[:space:]]*$' "${f}.err" | tail -n 1 | cut -c 1-200 || true)"
    if [ -n "${GH_JSON_NOT_FOUND:-}" ] && grep -q 'HTTP 404' "${f}.err"; then
      rm -f "${f}" "${f}.err"
      err "${GH_JSON_NOT_FOUND}" "gh could not ${what}: it does not exist (${last:-HTTP 404})"
    fi
    rm -f "${f}" "${f}.err"
    err gh_failed "gh could not ${what}: ${last:-no message}"
  fi
  rm -f "${f}.err"
  printf '%s' "${f}"
}

# release_rows <tag>: one line per release of the repository whose tag_name is TAG (drafts included; a token with contents: write
# sees them): id|draft|author login|prerelease|asset names separated by single spaces (whitespace inside a name becomes "?")
release_rows() {
  local f
  f="$(gh_json "list the releases of ${RELEASE_REPOSITORY}" api --paginate --slurp "repos/${RELEASE_REPOSITORY}/releases?per_page=100")"
  python3 -I - "${f}" "$1" <<'PY' || { rm -f "${f}"; err gh_failed "the release list is not the expected JSON"; }
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if not isinstance(data, list):
    sys.exit(1)
releases = []
for page in data:
    if isinstance(page, list):
        releases.extend(page)
    elif isinstance(page, dict):
        releases.append(page)
for r in releases:
    if not isinstance(r, dict) or r.get("tag_name") != sys.argv[2]:
        continue
    names = [re.sub(r"\s", "?", str(a.get("name", ""))) for a in r.get("assets") or []]
    author = (r.get("author") or {}).get("login", "")
    print("%s|%s|%s|%s|%s" % (r.get("id"), str(bool(r.get("draft"))).lower(), re.sub(r"[|\s]", "?", str(author)),
                              str(bool(r.get("prerelease"))).lower(), " ".join(sorted(names))))
PY
  rm -f "${f}"
}

# summary <line>...: appends to the job summary when GITHUB_STEP_SUMMARY is set
summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then printf '%s\n' "$@" >> "${GITHUB_STEP_SUMMARY}"; fi
}
fi
