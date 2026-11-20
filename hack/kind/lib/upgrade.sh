#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Helpers of the upgrade test (hack/kind/upgrade.sh). Sourced, not executed. bash 3.2 compatible (macOS), portable between
# macOS and Linux. Prints nothing and creates nothing when sourced, and does not need lib/common.sh: the functions
# upgrade_split_ref, upgrade_normalize and upgrade_compare need only bash and jq, so that a fixture test
# (hack/kind/upgrade_test.go) can source this file alone and run the comparison without a cluster. upgrade_snapshot and
# upgrade_connected_* call the API helper `a` of lib/api.sh and run only inside upgrade.sh.
#
# The comparison covers what an upgrade must not lose: the fleets (name, priority, selector, blueprint), the assignment of
# every agent to a fleet and the audit log. Volatile fields (status, lastSeenAt, health, configSync, version, the Git source of
# a fleet) are left out. The audit log may grow during an upgrade (logins, syncs), but no entry may disappear or change.
# Messages name fleets and ids, never values of a secret.

# upgrade_split_ref REF -> UPGRADE_REF_REPO, UPGRADE_REF_TAG, UPGRADE_REF_DIGEST of "repo:tag@sha256:<64 hex>"; 1 for any other form
upgrade_split_ref() {
  local re='^([^@:]+):([^@:]+)@(sha256:[0-9a-f]{64})$'
  if [[ "${1:-}" =~ ${re} ]]; then
    export UPGRADE_REF_REPO="${BASH_REMATCH[1]}"
    export UPGRADE_REF_TAG="${BASH_REMATCH[2]}"
    export UPGRADE_REF_DIGEST="${BASH_REMATCH[3]}"
    return 0
  fi
  return 1
}

# upgrade_normalize KIND FILE -> canonical JSON of the raw API answer on stdout. KIND: fleets | assignments | audit (2 otherwise).
# fleets: the answer of GET /fleets. assignments and audit: one page or an array of pages of GET /agents and GET /audit.
upgrade_normalize() {
  local kind="${1:-}" file="${2:-}" filter
  case "${kind}" in
    fleets) filter='[.items[].fleet | {name, priority, selector, blueprint}] | sort_by(.name)' ;;
    assignments) filter='(if type == "array" then map(.items[]) else .items end) | map({id, kind, fleetName: (.fleetName // "")}) | unique_by(.id)' ;;
    audit) filter='(if type == "array" then map(.items[]) else .items end) | map({id, time, actorType, actorId, actorName, action, targetType: (.targetType // ""), targetId: (.targetId // ""), result, details: (.details // {})}) | unique_by(.id)' ;;
    *) return 2 ;;
  esac
  jq -S "${filter}" "${file}"
}

# _upgrade_keys_differing KEYFIELD BEFORE_JSON AFTER_JSON -> the values of KEYFIELD whose entries differ, missing on one side
# included, at most ten, comma separated ("..." marks more)
_upgrade_keys_differing() {
  jq -nr --arg key "$1" --argjson b "$2" --argjson a "$3" '
    ([$b[] | .[$key] | tostring] + [$a[] | .[$key] | tostring] | unique) as $keys
    | [$keys[] as $k | select(([$b[] | select((.[$key] | tostring) == $k)]) != ([$a[] | select((.[$key] | tostring) == $k)])) | $k] as $d
    | ($d[0:10] | join(",")) + (if ($d | length) > 10 then ",..." else "" end)'
}

# upgrade_compare BEFORE_DIR AFTER_DIR: both directories hold fleets.json, assignments.json and audit.json (raw API answers).
# 0 when fleets and assignments are equal and every audit entry of BEFORE is in AFTER unchanged (new entries are allowed),
# 1 on a difference (one line on stderr per kind), 2 when a file is missing or not valid JSON.
upgrade_compare() {
  local before="${1:-}" after="${2:-}" kind dir f rc=0
  local nb na lost
  for kind in fleets assignments audit; do
    for dir in "${before}" "${after}"; do
      f="${dir}/${kind}.json"
      if [ ! -s "${f}" ] || ! upgrade_normalize "${kind}" "${f}" >/dev/null 2>&1; then
        echo "upgrade: cannot read ${kind} in ${dir}" >&2
        return 2
      fi
    done
  done

  for kind in fleets assignments; do
    nb="$(upgrade_normalize "${kind}" "${before}/${kind}.json")"
    na="$(upgrade_normalize "${kind}" "${after}/${kind}.json")"
    if [ "${nb}" != "${na}" ]; then
      case "${kind}" in
        fleets) echo "upgrade: fleets differ: $(_upgrade_keys_differing name "${nb}" "${na}")" >&2 ;;
        *) echo "upgrade: assignments differ: agents $(_upgrade_keys_differing id "${nb}" "${na}")" >&2 ;;
      esac
      rc=1
    fi
  done

  nb="$(upgrade_normalize audit "${before}/audit.json")"
  na="$(upgrade_normalize audit "${after}/audit.json")"
  lost="$(jq -nr --argjson b "${nb}" --argjson a "${na}" '
    ($a | map({key: (.id | tostring), value: .}) | from_entries) as $ai
    | [$b[] | select($ai[(.id | tostring)] != .) | .id]
    | "\(length) \(.[0:10] | map(tostring) | join(","))\(if length > 10 then ",..." else "" end)"')"
  if [ "${lost%% *}" != 0 ]; then
    echo "upgrade: audit differs: ${lost%% *} entries lost or changed (ids ${lost#* })" >&2
    rc=1
  fi
  return "${rc}"
}

# ---- run helpers (upgrade.sh only: they use `a` of lib/api.sh) -------------------------------------------------------------
# _upgrade_pages PATH -> all pages of a list call as a JSON array of pages on stdout; 1 when a call fails
_upgrade_pages() {
  local path="$1" sep="?" off=0 total=1 page n all=""
  case "${path}" in *\?*) sep="&" ;; esac
  while [ "${off}" -lt "${total}" ]; do
    page="$(a "${path}${sep}limit=500&offset=${off}")" || return 1
    total="$(printf '%s' "${page}" | jq -e '.total')" || return 1
    n="$(printf '%s' "${page}" | jq -e '.items | length')" || return 1
    [ "${n}" -gt 0 ] || break
    all="${all}${page}"$'\n'
    off=$((off + n))
  done
  printf '%s' "${all}" | jq -s '.'
}

# upgrade_snapshot DIR: fleets.json, assignments.json and audit.json of the current server; 1 when a call fails
upgrade_snapshot() {
  local dir="$1" body
  mkdir -p "${dir}" || return 1
  body="$(a /fleets)" || return 1
  printf '%s' "${body}" | jq -e '.items' >/dev/null || return 1
  printf '%s\n' "${body}" > "${dir}/fleets.json"
  body="$(_upgrade_pages /agents)" || return 1
  printf '%s\n' "${body}" > "${dir}/assignments.json"
  body="$(_upgrade_pages /audit)" || return 1
  printf '%s\n' "${body}" > "${dir}/audit.json"
}

# upgrade_connected_ids -> the ids of the agents with the status online, sorted, one line, comma separated
upgrade_connected_ids() {
  local pages
  pages="$(_upgrade_pages '/agents?status=online')" || return 1
  printf '%s' "${pages}" | jq -r '[.[].items[].id] | sort | join(",")'
}

# upgrade_connected_count -> the number of agents with the status online
upgrade_connected_count() {
  a '/agents?status=online&limit=1' | jq -r '.total // empty'
}
