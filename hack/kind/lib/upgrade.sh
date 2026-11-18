#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Helpers of the upgrade test on kind (hack/kind/upgrade.sh). Sourced, not executed. bash 3.2 compatible (macOS): no associative
# arrays, no mapfile, no ${x,,}. Portable between macOS and Linux. Prints nothing, runs no external command and creates no file when
# sourced.
#
# Two groups. The pure functions need no cluster (upgrade_require_tools, upgrade_split_ref, upgrade_node_version, upgrade_compare,
# upgrade_agents_online); the Go tests in hack/kind call them. The cluster functions (upgrade_capture, upgrade_verify_previous,
# upgrade_read_bootstrap_token, upgrade_sim_restarts, upgrade_agents_online_now) use k, kn and h of lib/common.sh and a of
# lib/api.sh, which the driver sources before it calls them.
#
# Examples in comments use placeholders only (<tag>, <digest>, vX.Y.Z): the pins live in hack/kind/versions.env.

# upgrade_require_tools TOOL...: 0 when every tool is on the PATH. The first missing tool ends the check with exactly one line on
# stderr that names it, and the return value 1. Builtins only.
upgrade_require_tools() {
  local tool
  for tool in "$@"; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
      printf "upgrade: required tool '%s' not found (install it, or run make tools for the pinned one in .cache/bin)\n" "${tool}" >&2
      return 1
    fi
  done
  return 0
}

# upgrade_split_ref REF: splits '<repo>:<tag>@sha256:<64 hex>' (the repo may start with oci:// and carry a registry port) into three
# lines on stdout: repo, tag, digest (with the sha256: prefix). The tag is the part after the last ':' before the '@' that holds
# no '/'. Returns 1 with a message on stderr when the digest or the tag is missing or malformed.
upgrade_split_ref() {
  local ref="$1" digest rest tag repo
  local re_digest='^sha256:[0-9a-f]{64}$'
  case "${ref}" in
    *@*) ;;
    *) printf "upgrade: reference '%s' has no digest (<repo>:<tag>@sha256:<digest> expected)\n" "${ref}" >&2; return 1 ;;
  esac
  digest="${ref##*@}"
  rest="${ref%@*}"
  if ! [[ "${digest}" =~ ${re_digest} ]]; then
    printf "upgrade: reference '%s' has no valid sha256 digest (64 hex characters expected)\n" "${ref}" >&2
    return 1
  fi
  tag="${rest##*:}"
  if [ "${tag}" = "${rest}" ] || [ -z "${tag}" ] || [[ "${tag}" == */* ]]; then
    printf "upgrade: reference '%s' has no tag (<repo>:<tag>@sha256:<digest> expected)\n" "${ref}" >&2
    return 1
  fi
  repo="${rest%:*}"
  printf '%s\n%s\n%s\n' "${repo}" "${tag}" "${digest}"
}

# upgrade_node_version IMAGE: prints the Kubernetes version vX.Y.Z of a node image pin '<repo>:vX.Y.Z@sha256:<digest>'.
# Returns 1 when the digest or a tag of that form is missing.
upgrade_node_version() {
  local ref="$1" rest tag
  local re_digest='@sha256:[0-9a-f]{64}$' re_tag='^v[0-9]+\.[0-9]+\.[0-9]+$'
  if ! [[ "${ref}" =~ ${re_digest} ]]; then
    printf "upgrade: node image '%s' is not pinned by digest\n" "${ref}" >&2
    return 1
  fi
  rest="${ref%@*}"
  tag="${rest##*:}"
  if ! [[ "${tag}" =~ ${re_tag} ]]; then
    printf "upgrade: node image '%s' has no tag of the form vX.Y.Z\n" "${ref}" >&2
    return 1
  fi
  printf '%s\n' "${tag}"
}

# jq definitions of the comparison. sub($a; $b) is the subset relation: every key of an object in $a is in $b with a subset value
# (a field that the new version adds is no difference), arrays need the same length and subset elements, scalars must be equal.
# keyed_reason looks the entries of two arrays up by a key and describes what differs ('' when nothing does); $strict also reports
# entries that are only in $y.
_UPGRADE_JQ_DEFS='
def sub($a; $b):
  if ($a | type) == "object" then
    ($b | type) == "object" and ([$a | keys_unsorted[] as $k | ($b | has($k)) and sub($a[$k]; $b[$k])] | all)
  elif ($a | type) == "array" then
    ($b | type) == "array" and ($a | length) == ($b | length) and ([range(0; $a | length) as $i | sub($a[$i]; $b[$i])] | all)
  else $a == $b end;
def keyed_reason($label; $k; $x; $y; $strict):
  [$x[] | .[$k] as $n | select(all($y[]; .[$k] != $n)) | $n] as $miss
  | [$y[] | .[$k] as $n | select(all($x[]; .[$k] != $n)) | $n] as $add
  | [$x[] | .[$k] as $n | ($y | map(select(.[$k] == $n)) | .[0]) as $o | select($o != null and (sub(.; $o) | not)) | $n] as $chg
  | [ (if ($miss | length) > 0 then "\($label) \($miss | join(", ")) missing" else empty end),
      (if $strict and ($add | length) > 0 then "\($label) \($add | join(", ")) added" else empty end),
      (if ($chg | length) > 0 then "\($label) \($chg | join(", ")) changed" else empty end) ]
  | join("; ");
'

# _upgrade_compare_part PART LABEL KEY STRICT BEFORE_DIR AFTER_DIR: prints 'SAME <part>' or 'DIFF <part>: <reason>'.
# Returns 0 (same), 1 (different) or 2 (jq failed).
_upgrade_compare_part() {
  local part="$1" label="$2" key="$3" strict="$4" before="$5" after="$6" reason
  reason="$(jq -n -r --slurpfile old "${before}/${part}.json" --slurpfile new "${after}/${part}.json" \
    --arg label "${label}" --arg key "${key}" --argjson strict "${strict}" \
    "${_UPGRADE_JQ_DEFS}"'
    $old[0] as $x | $new[0] as $y | keyed_reason($label; $key; $x; $y; $strict)' 2>/dev/null)" || {
    printf 'upgrade: cannot compare %s.json\n' "${part}" >&2
    return 2
  }
  if [ -n "${reason}" ]; then
    printf 'DIFF %s: %s\n' "${part}" "${reason}"
    return 1
  fi
  printf 'SAME %s\n' "${part}"
}

# upgrade_compare BEFORE_DIR AFTER_DIR: compares fleets.json, assignments.json and audit.json of two captures (upgrade_capture).
# fleets: the same set by .name, every fleet of BEFORE equal (subset rule) in AFTER. assignments: the same by .agentId. audit: every
# entry of BEFORE is in AFTER with the same .id and equal (subset rule); further entries in AFTER are allowed (the sign-in after the
# upgrade). Output: one line per part, 'SAME <part>' or 'DIFF <part>: <reason>'.
# Returns 0 when all three are the same, 1 when at least one differs, 2 when a file is missing or not a JSON array (checked for all
# six files before the first comparison) or jq is missing.
upgrade_compare() {
  local before="$1" after="$2" dir file rc=0 r
  if ! command -v jq >/dev/null 2>&1; then
    printf 'upgrade: jq is required for the comparison\n' >&2
    return 2
  fi
  for dir in "${before}" "${after}"; do
    for file in fleets.json assignments.json audit.json; do
      if [ ! -f "${dir}/${file}" ]; then
        printf 'upgrade: %s/%s is missing\n' "${dir}" "${file}" >&2
        return 2
      fi
      if ! jq -e 'type == "array"' "${dir}/${file}" >/dev/null 2>&1; then
        printf 'upgrade: %s/%s is not valid JSON (an array is expected)\n' "${dir}" "${file}" >&2
        return 2
      fi
    done
  done
  _upgrade_compare_part fleets fleet name true "${before}" "${after}"; r=$?
  case "${r}" in 0) ;; 1) rc=1 ;; *) return 2 ;; esac
  _upgrade_compare_part assignments agent agentId true "${before}" "${after}"; r=$?
  case "${r}" in 0) ;; 1) rc=1 ;; *) return 2 ;; esac
  _upgrade_compare_part audit "audit entry" id false "${before}" "${after}"; r=$?
  case "${r}" in 0) ;; 1) rc=1 ;; *) return 2 ;; esac
  return "${rc}"
}

# upgrade_agents_online FILE MIN: FILE holds the answer of GET /agents (an object with .items, each with .status online|offline).
# Prints the number of online entries; returns 0 when it is at least MIN, 1 when it is lower, 2 when FILE is no valid JSON.
upgrade_agents_online() {
  local file="$1" min="$2" n
  n="$(jq -r '[.items[]? | select(.status == "online")] | length' "${file}" 2>/dev/null)" || n=""
  if ! [[ "${n}" =~ ^[0-9]+$ ]]; then
    printf 'upgrade: %s is not valid JSON\n' "${file}" >&2
    return 2
  fi
  printf '%s\n' "${n}"
  [ "${n}" -ge "${min}" ]
}

# ---- cluster functions ---------------------------------------------------------------------------------------------------

# upgrade_agents_online_now MIN: upgrade_agents_online on the current answer of the server (no output but the count).
upgrade_agents_online_now() {
  local f="${PROBE_WORK}/upgrade/agents-now.json"
  mkdir -p "${PROBE_WORK}/upgrade" || return 1
  a "/agents?kind=collector&limit=500" > "${f}" 2>/dev/null || return 1
  upgrade_agents_online "${f}" "$1"
}

# upgrade_capture DIR: writes the state that must survive an upgrade into DIR as three JSON arrays:
#   fleets.json       the fleet objects of GET /fleets, sorted by name
#   assignments.json  per collector {agentId, fleetName, assignedConfigHash, targetVersion} (null fields removed), sorted by agentId
#   audit.json        the audit entries (without details, source address and request id), sorted by id
# The API returns no secrets. Returns 1 when a call fails.
upgrade_capture() {
  local dir="$1" offset page n total id
  mkdir -p "${dir}" || return 1

  a /fleets | jq '[.items[].fleet] | sort_by(.name)' > "${dir}/fleets.json" || return 1

  : > "${dir}/.ids"
  offset=0
  while :; do
    page="$(a "/agents?kind=collector&limit=500&offset=${offset}")" || return 1
    n="$(printf '%s' "${page}" | jq 'if (.items | type) == "array" then .items | length else error("no items") end')" || return 1
    printf '%s' "${page}" | jq -r '.items[].id' >> "${dir}/.ids" || return 1
    [ "${n}" -lt 500 ] && break
    offset=$((offset + 500))
  done
  : > "${dir}/.assignments"
  while IFS= read -r id; do
    [ -n "${id}" ] || continue
    a "/agents/${id}" | jq -c 'if .agent.id == null then error("no agent") else
      {agentId: .agent.id, fleetName: .agent.assignment.fleetName, assignedConfigHash: .agent.assignment.assignedConfigHash,
       targetVersion: .agent.assignment.targetVersion} | with_entries(select(.value != null)) end' >> "${dir}/.assignments" || return 1
  done < "${dir}/.ids"
  jq -s 'sort_by(.agentId)' "${dir}/.assignments" > "${dir}/assignments.json" || return 1

  : > "${dir}/.audit"
  offset=0
  while :; do
    page="$(a "/audit?limit=500&offset=${offset}")" || return 1
    total="$(printf '%s' "${page}" | jq 'if (.total | type) == "number" then .total else error("no total") end')" || return 1
    n="$(printf '%s' "${page}" | jq '.items | length')" || return 1
    printf '%s' "${page}" | jq -c '.items[] | {id, time, actorType, actorId, actorName, action, targetType, targetId, result}' >> "${dir}/.audit" || return 1
    offset=$((offset + 500))
    if [ "${n}" -eq 0 ] || [ "${offset}" -ge "${total}" ]; then break; fi
  done
  jq -s 'sort_by(.id)' "${dir}/.audit" > "${dir}/audit.json" || return 1
  rm -f "${dir}/.ids" "${dir}/.assignments" "${dir}/.audit"
}

# upgrade_verify_previous CHART_REPO CHART_TAG CHART_DIGEST IMAGE_REPO IMAGE_TAG IMAGE_DIGEST VERSION OUT_DIR
# Checks the published chart and image of the previous release before anything is installed, and pulls the chart. Prints one line per
# step on stdout: 'OK <name> <evidence>', and on the first deviation 'FAIL <name> <reason>' and returns 1. After the last step it
# prints 'TGZ <path of the pulled chart archive>'. The steps: chart digest and image digest (crane digest of the tag must equal the
# pin), chart signature and image signature (cosign verify against the digest, with the identity of the release workflow of the tag
# vVERSION, as in docs/operations/install-helm.md), chart pull (helm pull by tag and digest). The output of cosign goes to
# OUT_DIR/cosign-*.txt, not to the terminal.
upgrade_verify_previous() {
  local chart_repo="${1#oci://}" chart_tag="$2" chart_digest="$3" image_repo="$4" image_tag="$5" image_digest="$6" version="$7" out="$8"
  local identity="https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${version}"
  local issuer="https://token.actions.githubusercontent.com" got tgz
  mkdir -p "${out}" || return 1

  got="$(crane digest "${chart_repo}:${chart_tag}" 2>"${out}/crane-chart.txt")" || got=""
  if [ "${got}" != "${chart_digest}" ]; then
    printf 'FAIL chart digest expected %s, got %s\n' "${chart_digest}" "${got:-none (see crane-chart.txt)}"
    return 1
  fi
  printf 'OK chart digest %s\n' "${got}"

  got="$(crane digest "${image_repo}:${image_tag}" 2>"${out}/crane-image.txt")" || got=""
  if [ "${got}" != "${image_digest}" ]; then
    printf 'FAIL image digest expected %s, got %s\n' "${image_digest}" "${got:-none (see crane-image.txt)}"
    return 1
  fi
  printf 'OK image digest %s\n' "${got}"

  if ! cosign verify "${chart_repo}@${chart_digest}" --certificate-identity "${identity}" --certificate-oidc-issuer "${issuer}" \
    > "${out}/cosign-chart.txt" 2>&1; then
    printf 'FAIL chart signature cosign verify failed (see %s/cosign-chart.txt)\n' "${out}"
    return 1
  fi
  printf 'OK chart signature identity %s\n' "${identity}"

  if ! cosign verify "${image_repo}@${image_digest}" --certificate-identity "${identity}" --certificate-oidc-issuer "${issuer}" \
    > "${out}/cosign-image.txt" 2>&1; then
    printf 'FAIL image signature cosign verify failed (see %s/cosign-image.txt)\n' "${out}"
    return 1
  fi
  printf 'OK image signature identity %s\n' "${identity}"

  rm -f "${out}"/*.tgz
  if ! helm pull "oci://${chart_repo}:${chart_tag}@${chart_digest}" --destination "${out}" > "${out}/helm-pull.txt" 2>&1; then
    printf 'FAIL chart pull helm pull failed (see %s/helm-pull.txt)\n' "${out}"
    return 1
  fi
  tgz="$(ls "${out}"/*.tgz 2>/dev/null | head -n 1)"
  if [ -z "${tgz}" ]; then
    printf 'FAIL chart pull no archive in %s\n' "${out}"
    return 1
  fi
  printf 'OK chart pull %s\n' "${tgz##*/}"
  printf 'TGZ %s\n' "${tgz}"
}

# upgrade_read_bootstrap_token FILE: writes the bootstrap token to FILE (mode 0600). First the command that the chart NOTES print
# (bootstrap_token_from_notes of lib/install.sh); when that fails, the Secret myriarch-bootstrap is read directly. The token is never
# printed. Returns 1 when FILE stays empty.
upgrade_read_bootstrap_token() {
  local file="$1"
  if ! { bootstrap_token_from_notes "${file}" >/dev/null 2>&1 && [ -s "${file}" ]; }; then
    ( umask 077; kn "${NS_APP}" get secret myriarch-bootstrap -o jsonpath='{.data.token}' 2>/dev/null | base64 -d > "${file}" )
  fi
  [ -s "${file}" ]
}

# upgrade_sim_restarts: prints the sum of the restart counts of all containers of the simulator pods in the namespace of the app.
upgrade_sim_restarts() {
  kn "${NS_APP}" get pods -l app.kubernetes.io/name=myriarch-sim -o json 2>/dev/null \
    | jq '[.items[].status.containerStatuses[]?.restartCount] | add // 0'
}
