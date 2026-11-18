#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Helpers of the upgrade test (hack/kind/upgrade.sh). Sourced, not executed. bash 3.2 compatible (macOS): no associative arrays,
# no mapfile. Sourcing defines functions and variables only: it prints nothing and creates no files.
#
# upgrade_need_tools and upgrade_snapshot need lib/common.sh (die) and lib/api.sh (a). The pin checks, the reference helpers and the
# comparison functions need only bash, jq and printf, so a fixture test can call them without a cluster:
#
#   . hack/kind/lib/upgrade.sh; upgrade_compare DIR_BEFORE DIR_AFTER
#
# A snapshot directory holds fleets.json (GET /fleets), agents.json and audit.json ({"items": [...]}, all pages).

# upgrade_need_tools: one line on stderr and exit 1 for the first tool that is missing
upgrade_need_tools() {
  local t
  for t in kind kubectl helm cosign; do
    command -v "${t}" >/dev/null 2>&1 || die "required tool '${t}' not found on the PATH (make tools installs the pinned tools into .cache/bin)"
  done
}

# upgrade_check_pins: the pins of versions.env have the shape the test relies on (return 1 and one line per wrong pin otherwise)
upgrade_check_pins() {
  local rc=0 name val ver_re img_re
  local digest_re='^sha256:[0-9a-f]{64}$'
  local suffix_re='@sha256:[0-9a-f]{64}$'
  ver_re='^[0-9]+\.[0-9]+\.[0-9]+$'
  if ! [[ "${UPGRADE_PREVIOUS_VERSION:-}" =~ ${ver_re} ]]; then
    printf 'pin UPGRADE_PREVIOUS_VERSION is not a version of the form x.y.z\n' >&2
    rc=1
  else
    img_re="^[^@]+:${UPGRADE_PREVIOUS_VERSION//./\\.}@sha256:[0-9a-f]{64}\$"
    if ! [[ "${UPGRADE_PREVIOUS_SERVER_IMAGE:-}" =~ ${img_re} ]]; then
      printf 'pin UPGRADE_PREVIOUS_SERVER_IMAGE is not <repository>:<previous version>@sha256:<digest>\n' >&2
      rc=1
    fi
  fi
  if ! [[ "${UPGRADE_PREVIOUS_CHART_DIGEST:-}" =~ ${digest_re} ]]; then
    printf 'pin UPGRADE_PREVIOUS_CHART_DIGEST is not sha256:<64 hex digits>\n' >&2
    rc=1
  fi
  for name in KIND_NODE_IMAGE KIND_NODE_IMAGE_PREVIOUS POSTGRES_IMAGE GIT_SERVER_IMAGE REGISTRY_IMAGE; do
    val="${!name:-}"
    if ! [[ "${val}" =~ ${suffix_re} ]]; then
      printf 'pin %s is not pinned by digest (it must end in @sha256:<64 hex digits>)\n' "${name}" >&2
      rc=1
    fi
  done
  return "${rc}"
}

# upgrade_select_node_image: UPGRADE_KUBE=current (default) keeps KIND_NODE_IMAGE, previous uses KIND_NODE_IMAGE_PREVIOUS
upgrade_select_node_image() {
  case "${UPGRADE_KUBE:-current}" in
    current) ;;
    previous) export KIND_NODE_IMAGE="${KIND_NODE_IMAGE_PREVIOUS}" ;;
    *) printf 'UPGRADE_KUBE must be current or previous, not %s\n' "${UPGRADE_KUBE}" >&2; return 1 ;;
  esac
}

# upgrade_ref_repo REF / upgrade_ref_digest REF: the parts of <repository>:<tag>@<digest>
upgrade_ref_repo() {
  local base="${1%@*}"
  printf '%s' "${base%:*}"
}
upgrade_ref_digest() { printf '%s' "${1##*@}"; }

# upgrade_pull_digest: reads the output of `helm pull` on stdin and prints the digest of its "Digest: sha256:..." line (1 without one)
upgrade_pull_digest() {
  local line d="" re='^sha256:[0-9a-f]{64}$'
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in
      'Digest: sha256:'*) d="${line#Digest: }"; break ;;
    esac
  done
  [[ "${d}" =~ ${re} ]] || return 1
  printf '%s\n' "${d}"
}

# upgrade_check_template_images REF: reads rendered manifests on stdin; every "image:" value must be REF (1 otherwise or without one)
upgrade_check_template_images() {
  local want="$1" line v n=0 bad=0
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in
      *'image:'*)
        v="${line#*image:}"
        v="${v# }"; v="${v#\"}"; v="${v%\"}"
        n=$((n + 1))
        if [ "${v}" != "${want}" ]; then bad=$((bad + 1)); printf 'image %s is not the pinned %s\n' "${v}" "${want}" >&2; fi
        ;;
    esac
  done
  [ "${n}" -gt 0 ] && [ "${bad}" -eq 0 ]
}

# ---- snapshots ---------------------------------------------------------------------------------------------------------
# _upgrade_fetch_all PATH OUT: all pages of a list endpoint, merged into {"items": [...]} (1 when a call fails or is no page)
_upgrade_fetch_all() {
  local path="$1" out="$2" offset=0 limit=500 page n i=0
  local pages=()
  while :; do
    page="${out}.page${i}"
    a "${path}?limit=${limit}&offset=${offset}" > "${page}" || { rm -f "${out}".page*; return 1; }
    pages+=("${page}")
    if ! jq -e '.items | type == "array"' "${page}" >/dev/null 2>&1; then rm -f "${out}".page*; return 1; fi
    n="$(jq '.items | length' "${page}")"
    if [ "${n}" -lt "${limit}" ]; then break; fi
    offset=$((offset + limit))
    i=$((i + 1))
  done
  jq -s '{items: (map(.items) | add // [])}' "${pages[@]}" > "${out}" || { rm -f "${out}".page*; return 1; }
  rm -f "${out}".page*
}

# upgrade_snapshot DIR: fleets.json, agents.json and audit.json of the current API target
upgrade_snapshot() {
  local dir="$1"
  mkdir -p "${dir}"
  a /fleets > "${dir}/fleets.json" || return 1
  jq -e '.items | type == "array"' "${dir}/fleets.json" >/dev/null 2>&1 || return 1
  _upgrade_fetch_all /agents "${dir}/agents.json" || return 1
  _upgrade_fetch_all /audit "${dir}/audit.json" || return 1
}

# ---- comparison --------------------------------------------------------------------------------------------------------
# The projections: what must survive an upgrade. Config hashes are left out on purpose (a new version may normalize a document).
UPGRADE_JQ_FLEETS='[.items[].fleet | {name, priority, blueprint, selector, source: {path: .source.path, commit: .source.commit}}] | sort_by(.name)'
UPGRADE_JQ_ASSIGNMENTS='[.items[] | {id, fleetName, targetVersion}] | sort_by(.id)'
UPGRADE_JQ_AUDIT='[.items[] | {id, time, actorType, actorId, actorName, action, targetType, targetId, result, details, sourceIp, requestId}] | sort_by(.id)'

# _upgrade_diff LABEL PROJECTION KEY MODE BEFORE_FILE AFTER_FILE: MODE exact (the projections are equal) or superset (every
# projected item of BEFORE is unchanged in AFTER, which may have more). One line on stderr and return 1 on a difference; it names
# the keys (fleet names, ids), never the contents.
_upgrade_diff() {
  local label="$1" proj="$2" key="$3" mode="$4" before="$5" after="$6" f out prog
  for f in "${before}" "${after}"; do
    if ! jq -e '.items | type == "array"' "${f}" >/dev/null 2>&1; then
      printf '%s: %s is missing or not valid JSON\n' "${label}" "${f}" >&2
      return 1
    fi
  done
  prog="def proj: ${proj};"' ($b[0] | proj) as $pb | ($a[0] | proj) as $pa
    | [$pb[] | .[$key]] as $kb | [$pa[] | .[$key]] as $ka
    | [$kb[] | select(. as $k | $ka | any(. == $k) | not) | tostring] as $missing
    | [$ka[] | select(. as $k | $kb | any(. == $k) | not) | tostring] as $added
    | [$pb[] | . as $o | ($pa[] | select(.[$key] == $o[$key])) as $n | select($n != $o) | $o[$key] | tostring] as $changed
    | [ (if ($missing | length) > 0 then "missing " + ($missing | join(",")) else empty end),
        (if $mode == "exact" and ($added | length) > 0 then "added " + ($added | join(",")) else empty end),
        (if ($changed | length) > 0 then "changed " + ($changed | join(",")) else empty end) ]
    | if length == 0 then empty else $label + ": " + join("; ") end'
  out="$(jq -nr --slurpfile b "${before}" --slurpfile a "${after}" --arg key "${key}" --arg mode "${mode}" --arg label "${label}" "${prog}")" || {
    printf '%s: the comparison failed\n' "${label}" >&2
    return 1
  }
  [ -z "${out}" ] && return 0
  printf '%s\n' "${out}" >&2
  return 1
}

# upgrade_compare BEFORE AFTER: fleets, assignments and the audit log of two snapshot directories (0 equal, 1 different or unreadable).
# All three areas are always checked. Fleets and assignments must be equal; the audit log may only have grown.
upgrade_compare() {
  local before="$1" after="$2" rc=0
  _upgrade_diff 'fleets differ' "${UPGRADE_JQ_FLEETS}" name exact "${before}/fleets.json" "${after}/fleets.json" || rc=1
  _upgrade_diff 'assignments differ' "${UPGRADE_JQ_ASSIGNMENTS}" id exact "${before}/agents.json" "${after}/agents.json" || rc=1
  _upgrade_diff 'audit differs' "${UPGRADE_JQ_AUDIT}" id superset "${before}/audit.json" "${after}/audit.json" || rc=1
  return "${rc}"
}

# upgrade_check_online BEFORE AFTER: every agent that was online is online again, and no id is new (a new id is a registration)
upgrade_check_online() {
  local before="$1/agents.json" after="$2/agents.json" f out
  for f in "${before}" "${after}"; do
    if ! jq -e '.items | type == "array"' "${f}" >/dev/null 2>&1; then
      printf 'agents differ: %s is missing or not valid JSON\n' "${f}" >&2
      return 1
    fi
  done
  out="$(jq -nr --slurpfile b "${before}" --slurpfile a "${after}" '
    $b[0].items as $ib | $a[0].items as $ia
    | [$ib[] | select(.status == "online") | .id] as $on
    | [$on[] | select(. as $i | $ia | any(.id == $i and .status == "online") | not)] as $down
    | [$ia[] | .id | select(. as $i | $ib | any(.id == $i) | not)] as $new
    | [ (if ($down | length) > 0 then "not online again " + ($down | join(",")) else empty end),
        (if ($new | length) > 0 then "new ids (registered anew) " + ($new | join(",")) else empty end) ]
    | if length == 0 then empty else "agents differ: " + join("; ") end')" || {
    printf 'agents differ: the comparison failed\n' >&2
    return 1
  }
  [ -z "${out}" ] && return 0
  printf '%s\n' "${out}" >&2
  return 1
}
