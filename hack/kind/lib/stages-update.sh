#!/usr/bin/env bash
# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Probe stages 9 to 12 (sourced by hack/kind/probe.sh): the version update through the calls the UI dialog makes on the real
# collector (failure with automatic rollback, retry, pod restart, back to the Git target), the leader takeover, a helm
# upgrade that changes an enrollment token expiry, and the rotation of the bootstrap token.

FLEET=kind-gw
GW_POD=kind-gw-0
GW_INSTALL_DIR=/var/lib/myriarch/collector
FAULT_PID_FILE="${PROBE_WORK}/fault-injector.pid"

# ---- helpers -----------------------------------------------------------------------------------------------------
gw_exec() { kn "${NS_APP}" exec "${GW_POD}" -- "$@"; }
gw_current() { gw_exec readlink "${GW_INSTALL_DIR}/current" 2>/dev/null; }
gw_updater_status() { a "/agents/$1" | jq -r '.agent.health.components["dev.myriarch.update"].status // empty'; }
fleet_target() { a "/fleets/${FLEET}" | jq -r '"\(.status.versionTarget.source)/\(.status.versionTarget.version)"'; }

fault_injector_start() { # version
  fault_injector_stop >/dev/null 2>&1 || true
  kubectl --context "${KIND_CONTEXT}" -n "${NS_APP}" exec -i "${GW_POD}" -- sh -s -- "$1" < "${KIND_DIR}/fault/killer.sh" > "${PROBE_WORK}/logs/fault-injector.log" 2>&1 &
  echo $! > "${FAULT_PID_FILE}"
}
fault_injector_stop() {
  local pid
  gw_exec touch /tmp/killer.stop >/dev/null 2>&1 || true
  if [ -f "${FAULT_PID_FILE}" ]; then
    pid="$(cat "${FAULT_PID_FILE}")"
    if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then kill "${pid}" 2>/dev/null || true; wait "${pid}" 2>/dev/null || true; fi
    rm -f "${FAULT_PID_FILE}"
  fi
}

# select_version VERSION REASON EXPECTED -> echoes the rollout id (polls when not planned in time); the response is kept in
# $PROBE_WORK/select-response.json (the function runs in a command substitution)
select_version() {
  local v="$1" reason="$2" expected="$3" resp id
  resp="$(ap "/fleets/${FLEET}/actions/select-version" "$(jq -n --arg v "${v}" --arg r "${reason}" --arg e "${expected}" '{version:$v, reason:$r, expectedTargetVersion:$e}')")"
  printf '%s' "${resp}" > "${PROBE_WORK}/select-response.json"
  id="$(printf '%s' "${resp}" | jq -r '.rolloutId // empty')"
  if [ -z "${id}" ]; then
    wait_until 60 "[ -n \"\$(latest_rollout ${FLEET})\" ]" 2
    id="$(latest_rollout "${FLEET}")"
  fi
  printf '%s' "${id}"
}

# gw_ready_after_restart OLD_UID: the pod kind-gw-0 has a new uid and is Ready
gw_ready_after_restart() {
  local old="$1"
  wait_until 300 "[ \"\$(kn ${NS_APP} get pod ${GW_POD} -o jsonpath='{.metadata.uid}' 2>/dev/null)\" != '${old}' ]" 2 || return 1
  kn "${NS_APP}" wait --for=condition=Ready "pod/${GW_POD}" --timeout=300s >/dev/null 2>&1
}

# ---- stage 9 -----------------------------------------------------------------------------------------------------
stage_9() {
  stage_begin 9 "Version update through the UI calls (real collector)"
  api_use main
  pf_ensure || true
  session_refresh
  local gw upd opt rej
  gw="$(state_get GW_AGENT_ID)"
  upd="$(state_get GW_UPDATER_ID)"
  [ -n "${gw}" ] && [ -n "${upd}" ] || fatal "agent ids known" "run stage 7 first"

  # 9.1 what the dialog shows
  opt="$(a "/fleets/${FLEET}/version-options")"
  check_eq "dialog: selection allowed" true "$(printf '%s' "${opt}" | jq -r .allowed)"
  check_eq "dialog: effective target before" "0.161.0/git" "$(printf '%s' "${opt}" | jq -r '"\(.target.version)/\(.target.source)"')"
  check_eq "dialog: 0.162.0 is selectable" true "$(printf '%s' "${opt}" | jq -r '.options[] | select(.version=="0.162.0") | .selectable')"
  check_ge "dialog: 0.162.0 affects at least one collector" 1 "$(printf '%s' "${opt}" | jq -r '.options[] | select(.version=="0.162.0") | .agentsToUpdate')"
  check_eq "dialog: 0.161.0 is the current target" current_target "$(printf '%s' "${opt}" | jq -r '.options[] | select(.version=="0.161.0") | .reasonCode')"
  check_eq "dialog: the frame lists both versions" "0.161.0,0.162.0" "$(printf '%s' "${opt}" | jq -r '.target.frame.versions | join(",")')"
  rej="$(curl -s -m 30 -b "${API_JAR}" -H "X-CSRF-Token: ${API_CSRF}" -H 'Content-Type: application/json' -w '\n%{http_code}' -X POST \
    "${API_BASE}/api/v1/fleets/${FLEET}/actions/select-version" -d '{"version":"0.163.0","reason":"kind probe outside the frame"}')"
  check_eq "select-version outside the Git frame answers 422" 422 "$(printf '%s' "${rej}" | tail -n 1)"
  check_contains "the rejection names version_not_allowed" version_not_allowed "$(printf '%s' "${rej}" | sed '$d')"
  check_eq "the target is unchanged after the rejection" "git/0.161.0" "$(fleet_target)"
  check_ge "the rejected attempt is audited (fleet.version.select, result failure)" 1 "$(a '/audit?action=fleet.version.select&limit=200' | jq '[.items[] | select(.result=="failure")] | length')"

  # 9.2 failure: the new collector dies during the health check, the host rolls back
  fault_injector_start 0.162.0
  local r1
  r1="$(select_version 0.162.0 "kind probe" 0.161.0)"
  if [ -z "${r1}" ]; then fault_injector_stop; fatal "select-version started a rollout" "response: $(head -c 200 "${PROBE_WORK}/select-response.json")"; fi
  state_set ROLLOUT_ID "${r1}"
  pass "select-version started a version rollout" "planned=$(jq -r .planned "${PROBE_WORK}/select-response.json") rollout ${r1}"
  check_eq "rollout origin" ui "$(a "/rollouts/${r1}" | jq -r .origin)"
  check_eq "versionTarget right after the selection" "ui/0.162.0" "$(fleet_target)"
  local final1
  final1="$(watch_rollout "${r1}" 240)"
  log "phases observed: $(phases_of "${r1}"); rollout status: ${final1}"
  # the killer must go on until the updater has given up on the new version and rolled back
  local ended_at
  if wait_until 150 "case \"\$(gw_updater_status ${upd})\" in rolled_back|failed) true ;; *) false ;; esac" 1; then
    pass "the updater ended the attempt" "$(gw_updater_status "${upd}")"
  else fail "the updater ended the attempt" "component status: $(gw_updater_status "${upd}")"; fi
  ended_at=${SECONDS}
  local kills
  kills="$(gw_exec sh -c 'grep -c "^killed otelcol-contrib" /tmp/killer.log || true' 2>/dev/null | head -n 1)"
  fault_injector_stop
  wait_until 60 "[ \"\$(a /rollouts/${r1} | jq -r .status)\" = paused ]" 1
  check_eq "rollout paused after the failed target (onFailure pause)" paused "$(a "/rollouts/${r1}" | jq -r .status)"
  if [ "$(a "/artifacts" | jq -r '[.items[] | select(.version=="0.162.0" and .os=="linux" and .status=="ready")] | length')" -ge 1 ]; then
    pass "the 0.162.0 artifact is ready (GitHub download, SHA-256 and Sigstore verified by myriarchd)" "$(a "/artifacts" | jq -r '[.items[] | select(.version=="0.162.0" and .os=="linux") | "\(.arch)/\(.status)"] | join(",")')"
  else fail "the 0.162.0 artifact is ready" "$(a "/artifacts" | jq -c '[.items[] | {version, os, arch, status}]' | head -c 300)"; fi
  wait_until 60 "[ \"\$(a /agents/${gw} | jq -r .agent.version)\" = 0.161.0 ]" 2
  # The engine keeps following the updater after it failed the target. It derives the final phase on its next tick after
  # the updater's last report (docs/design/version-selection.md, Target progress > Derivation); that tick runs about
  # minTickGap (1 s) after the report, at the latest after rollouts.tickInterval (5 s). The tick before may still hold the
  # intermediate report (rolling_back done, attempt not ended) and show phase "failed". Wait for the converged phase; the
  # bound is several tick intervals and far below the 15 minutes the engine keeps following the host.
  local settle_from=${SECONDS}
  if wait_until 30 "[ \"\$(a /rollouts/${r1}/targets | jq -r '.items[0].progress.phase')\" = rolled_back ]" 1; then
    log "target progress settled after $((SECONDS - settle_from)) s"
  else
    # not fatal here: the check "target progress.phase" below reports the failure
    warn "target progress did not settle within 30 s (phase: $(a "/rollouts/${r1}/targets" | jq -r '.items[0].progress.phase // "none"'))"
  fi
  check_eq "the collector reports 0.161.0 again" 0.161.0 "$(a "/agents/${gw}" | jq -r .agent.version)"
  check_eq "current -> versions/0.161.0 again" versions/0.161.0 "$(gw_current)"
  check_eq "versions/0.162.0 is gone after the rollback" gone "$(gw_exec test -d "${GW_INSTALL_DIR}/versions/0.162.0" 2>/dev/null && echo present || echo gone)"
  check_contains "step rolling_back done" "rolling_back:done" "$(steps_of "${r1}")"
  check_contains "step health_check failed" "health_check:failed" "$(steps_of "${r1}")"
  check_eq "target progress.phase" rolled_back "$(a "/rollouts/${r1}/targets" | jq -r '.items[0].progress.phase')"
  check_eq "target status" failed "$(a "/rollouts/${r1}/targets" | jq -r '.items[0].status')"
  check_contains "package status of the updater" install_failed "$(a "/agents/${upd}" | jq -r '.agent.packageStatus.packages[""].status // "none"')"
  check_ge "the fault injector killed the new collector at least once" 1 "${kills}"

  # 9.3 retry after the backoff
  # the updater refuses to install the same content again within failedRetryBackoff of the failure: wait for the backoff to
  # elapse since the observed end of the attempt (a later point than the failure itself), not for a fixed time
  wait_until 60 "[ \"\$(a /agents/${gw} | jq -r .agent.health.healthy)\" = true ]" 2
  wait_until $((UPDATER_RETRY_BACKOFF + 45)) "[ \$((SECONDS - ended_at)) -ge $((UPDATER_RETRY_BACKOFF + 2)) ]" 1
  log "failedRetryBackoff (${UPDATER_RETRY_BACKOFF} s) elapsed $((SECONDS - ended_at)) s after the end of the attempt"
  ap "/rollouts/${r1}/actions/retry-failed" '{"reason":"kind probe retry"}' >/dev/null
  local final2 steps obs
  final2="$(watch_rollout "${r1}" 420)"
  log "phases observed: $(phases_of "${r1}"); rollout status: ${final2}"
  check_eq "rollout completed after the retry" completed "${final2}"
  steps="$(steps_of "${r1}")"
  local s
  for s in downloading verifying installing restarting health_check; do check_contains "step ${s} done" "${s}:done" "${steps}"; done
  check_eq "no rolling_back step in the successful attempt" absent "$(step_status "${r1}" rolling_back)"
  obs="$(phases_of "${r1}")"
  check_ge "at least 3 distinct live phases observed" 3 "$(printf '%s' "${obs}" | wc -w | tr -d ' ')"
  check_contains "phase health_wait observed live" health_wait "${obs}"
  check_contains "final phase succeeded" succeeded "${obs}"
  check_eq "target progress.phase" succeeded "$(a "/rollouts/${r1}/targets" | jq -r '.items[0].progress.phase')"
  check_eq "progress source" updater "$(a "/rollouts/${r1}/targets" | jq -r '.items[0].progress.source')"
  wait_until 90 "[ \"\$(a /agents/${gw} | jq -r .agent.version)\" = 0.162.0 ]" 2
  check_eq "the collector reports 0.162.0" 0.162.0 "$(a "/agents/${gw}" | jq -r .agent.version)"
  wait_until 90 "[ \"\$(a /agents/${gw} | jq -r .agent.health.healthy)\" = true ]" 2
  check_eq "the collector is healthy" true "$(a "/agents/${gw}" | jq -r .agent.health.healthy)"
  check_eq "current -> versions/0.162.0" versions/0.162.0 "$(gw_current)"
  check_eq "package status installed" installed "$(a "/agents/${upd}" | jq -r '.agent.packageStatus.packages[""].status // "none"')"
  local fl
  fl="$(a "/fleets/${FLEET}")"
  check_eq "fleet target" "ui/0.162.0" "$(printf '%s' "${fl}" | jq -r '"\(.status.versionTarget.source)/\(.status.versionTarget.version)"')"
  check_eq "selection.selectedBy" admin "$(printf '%s' "${fl}" | jq -r '.status.versionTarget.selection.selectedBy')"
  check_eq "selection reason is kept" "kind probe" "$(printf '%s' "${fl}" | jq -r '.status.versionTarget.selection.reason')"
  check_eq "audit fleet.version.select by admin" admin "$(a '/audit?action=fleet.version.select&limit=200' | jq -r '[.items[] | select(.result=="success")][0].actorName // empty')"
  check_eq "rollout origin / createdBy" "ui/admin" "$(a "/rollouts/${r1}" | jq -r '"\(.origin)/\(.createdBy)"')"

  # 9.4 pod restart: the volume wins over the older image (seedPolicy newest)
  local old_uid
  old_uid="$(kn "${NS_APP}" get pod "${GW_POD}" -o jsonpath='{.metadata.uid}')"
  kn "${NS_APP}" delete pod "${GW_POD}" --wait=true --timeout=240s >/dev/null 2>&1
  if gw_ready_after_restart "${old_uid}"; then pass "the replacement pod is Ready"; else fail "the replacement pod is Ready"; fi
  check_eq "seed decision after the restart" keep-active/0.161.0/0.162.0 \
    "$(kn "${NS_APP}" logs "${GW_POD}" 2>/dev/null | grep -m1 '"msg":"seed decision"' | jq -r '"\(.decision)/\(.imageVersion)/\(.active)"')"
  wait_until 180 "[ \"\$(a /agents/${gw} | jq -r '.agent.connection.status')\" = online ]" 2
  wait_until 120 "[ \"\$(a /agents/${gw} | jq -r .agent.health.healthy)\" = true ]" 2
  check_eq "same agent id after the restart" "${gw}" "$(agent_id_by_site kind-gw)"
  check_eq "the collector stays on 0.162.0" 0.162.0 "$(a "/agents/${gw}" | jq -r .agent.version)"
  wait_until 120 "[ -n \"\$(updater_id_of ${gw})\" ]" 2
  check_eq "the updater is linked again" "${upd}" "$(updater_id_of "${gw}")"

  # 9.5 back to the Git target (the operator's one-step return)
  if skipped_by_key downgrade; then
    skip "back to the Git target" "PROBE_SKIP=downgrade"
  else
    local clr
    clr="$(ap "/fleets/${FLEET}/actions/clear-version-selection" '{"reason":"kind probe back to Git"}')"
    check_eq "clear-version-selection returns the Git target" "git/0.161.0" "$(printf '%s' "${clr}" | jq -r '"\(.source)/\(.version)"')"
    wait_until 60 "[ \"\$(latest_rollout ${FLEET})\" != '${r1}' ]" 2
    local r2 final3
    r2="$(latest_rollout "${FLEET}")"
    check_ne "a new version rollout was created" "${r1}" "${r2}"
    final3="$(watch_rollout "${r2}" 420)"
    log "phases observed: $(phases_of "${r2}"); rollout status: ${final3}"
    check_eq "the return rollout completed" completed "${final3}"
    wait_until 90 "[ \"\$(a /agents/${gw} | jq -r .agent.version)\" = 0.161.0 ]" 2
    check_eq "the collector reports 0.161.0" 0.161.0 "$(a "/agents/${gw}" | jq -r .agent.version)"
    check_eq "current -> versions/0.161.0" versions/0.161.0 "$(gw_current)"
    check_ge "audit fleet.version.clear (success)" 1 "$(a '/audit?action=fleet.version.clear&limit=200' | jq '[.items[] | select(.result=="success")] | length')"
  fi
}

# ---- stage 10 ----------------------------------------------------------------------------------------------------
stage_10() {
  stage_begin 10 "Leader takeover"
  api_use main
  pf_ensure || true
  session_refresh
  local total old t0 new gw
  gw="$(state_get GW_AGENT_ID)"
  total="$(a /stats/overview | jq -r .agents.total)"
  old="$(leader_id)"
  [ -n "${old}" ] || fatal "a leader exists" "leaderId is empty"
  pass "leader before" "${old}"
  t0=${SECONDS}
  kn "${NS_APP}" delete pod "${old}" --wait=false >/dev/null
  new=""
  while [ $((SECONDS - t0)) -lt 120 ]; do
    new="$(sysinfo | jq -r --arg o "${old}" '[.replicas[] | select(.isLeader and .id != $o)][0].id // empty' 2>/dev/null)"
    [ -n "${new}" ] && break
    sleep 1
  done
  local took=$((SECONDS - t0))
  if [ -n "${new}" ]; then pass "another replica became the leader" "${new} after ${took}s"; else fail "another replica became the leader" "no new leader after ${took}s"; fi
  check_le "leader takeover within 60 s" 60 "${took}"
  state_set LEADER_TAKEOVER_SECONDS "${took}"
  app_rollout_wait "${NS_APP}"
  check_eq "deployment is back to 2/2" 2/2 "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')"
  wait_until 120 "[ \"\$(sysinfo | jq '[.replicas[] | select(.isLeader)] | length')\" = 1 ] && [ \"\$(sysinfo | jq '.replicas | length')\" = 2 ]" 2
  check_eq "exactly one leader" 1 "$(sysinfo | jq '[.replicas[] | select(.isLeader)] | length')"
  wait_until 120 "[ \"\$(a /stats/overview | jq -r '.agents.online == .agents.total')\" = true ]" 2
  check_eq "all agents online again" true "$(a /stats/overview | jq -r '.agents.online == .agents.total')"
  check_eq "agent total unchanged (no duplicates)" "${total}" "$(a /stats/overview | jq -r .agents.total)"
  check_eq "the real collector keeps its agent id" "${gw}" "$(agent_id_by_site kind-gw)"
  local et name
  et="$(a /enrollment-tokens)"
  for name in kind-sim kind-gw; do
    check_eq "enrollment token ${name}: still one row" 1 "$(printf '%s' "${et}" | jq --arg n "${name}" '[.items[] | select(.name==$n)] | length')"
    check_eq "audit enrollment_token.create for ${name} still 1" 1 "$(a '/audit?action=enrollment_token.create&limit=200' | jq --arg n "${name}" '[.items[] | select(.details.name==$n)] | length')"
  done
  check_eq "no enrollment_token.update (a restart changes nothing)" 0 "$(audit_count enrollment_token.update)"
  check_eq "auth.bootstrap.activate still 1" 1 "$(audit_count auth.bootstrap.activate)"
  check_eq "the old bootstrap token is still refused (401)" 401 "$(bootstrap_login "$(secret_path bootstrap.token)" "${PROBE_WORK}/cookies-old")"
  rm -f "${PROBE_WORK}/cookies-old"
}

# ---- stage 11 ----------------------------------------------------------------------------------------------------
stage_11() {
  stage_begin 11 "helm upgrade changing expiresAt"
  load_images
  api_use main
  pf_ensure || true
  session_refresh
  local total sec_before sec_after name
  total="$(a /stats/overview | jq -r .agents.total)"
  sec_before="$(for name in myriarch-enrollment-kind-sim myriarch-enrollment-kind-gw myriarch-bootstrap; do kn "${NS_APP}" get secret "${name}" -o jsonpath='{.data.token}'; done | sha256_stdin)"
  write_values_tokens 60
  state_set KIND_SIM_EXPIRES_AT_2 "${KIND_SIM_EXPIRES_AT}"
  if chart_upgrade_full; then pass "helm upgrade with a new expiresAt for kind-sim"; else fatal "helm upgrade with a new expiresAt"; fi
  app_rollout_wait "${NS_APP}"
  check_eq "deployment is 2/2 after the upgrade" 2/2 "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')"
  wait_until 60 "[ \"\$(a /enrollment-tokens | jq -r '.items[] | select(.name==\"kind-sim\") | .expiresAt')\" = '${KIND_SIM_EXPIRES_AT}' ]" 2
  local et
  et="$(a /enrollment-tokens)"
  check_eq "kind-sim: expiresAt updated" "${KIND_SIM_EXPIRES_AT}" "$(printf '%s' "${et}" | jq -r '.items[] | select(.name=="kind-sim") | .expiresAt')"
  check_eq "kind-sim: still one row" 1 "$(printf '%s' "${et}" | jq '[.items[] | select(.name=="kind-sim")] | length')"
  check_eq "kind-gw: still one row" 1 "$(printf '%s' "${et}" | jq '[.items[] | select(.name=="kind-gw")] | length')"
  local upd
  upd="$(a '/audit?action=enrollment_token.update&limit=200')"
  check_eq "audit enrollment_token.update count" 1 "$(printf '%s' "${upd}" | jq '.items | length')"
  check_eq "audit enrollment_token.update is for kind-sim" kind-sim "$(printf '%s' "${upd}" | jq -r '.items[0].details.name // empty')"
  check_contains "audit enrollment_token.update records expiresAt as changed" expiresAt "$(printf '%s' "${upd}" | jq -c '.items[0].details.changed')"
  sec_after="$(for name in myriarch-enrollment-kind-sim myriarch-enrollment-kind-gw myriarch-bootstrap; do kn "${NS_APP}" get secret "${name}" -o jsonpath='{.data.token}'; done | sha256_stdin)"
  if [ "${sec_before}" = "${sec_after}" ]; then pass "enrollment and bootstrap Secret values are unchanged"; else fail "enrollment and bootstrap Secret values are unchanged" "a value differs"; fi
  wait_until 180 "[ \"\$(a /stats/overview | jq -r '.agents.online == .agents.total')\" = true ]" 2
  check_eq "agents back online" true "$(a /stats/overview | jq -r '.agents.online == .agents.total')"
  check_eq "agent total unchanged" "${total}" "$(a /stats/overview | jq -r .agents.total)"
}

# ---- stage 12 ----------------------------------------------------------------------------------------------------
stage_12() {
  stage_begin 12 "Bootstrap token rotation"
  if skipped_by_key rotation; then skip "rotation of the bootstrap token" "PROBE_SKIP=rotation"; return 0; fi
  load_images
  api_use main
  pf_ensure || true
  session_refresh
  local old_tok new_tok
  old_tok="$(secret_path bootstrap.token)"
  new_tok="$(secret_path bootstrap.token.new)"
  cp "${old_tok}" "$(secret_path bootstrap.token.old)"
  kn "${NS_APP}" delete secret myriarch-bootstrap >/dev/null
  if chart_upgrade_full; then pass "helm upgrade after deleting the bootstrap Secret"; else fatal "helm upgrade after deleting the bootstrap Secret"; fi
  app_rollout_wait "${NS_APP}"
  check_eq "deployment is 2/2" 2/2 "$(kn "${NS_APP}" get deploy myriarch -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')"
  bootstrap_token_from_notes "${new_tok}" || : > "${new_tok}"
  if grep -Eq '^[A-Za-z0-9]{48}$' "${new_tok}"; then pass "the NOTES command prints the new token"; else fail "the NOTES command prints the new token"; fi
  if cmp -s "${old_tok}" "${new_tok}"; then fail "the token was rotated" "the value did not change"; else pass "the token was rotated"; fi
  local jar="${PROBE_WORK}/cookies-rotated"
  rm -f "${jar}"
  check_eq "the new token signs in once (200)" 200 "$(bootstrap_login "${new_tok}" "${jar}")"
  check_eq "the old token is refused (401)" 401 "$(bootstrap_login "${old_tok}" "${PROBE_WORK}/cookies-old")"
  rm -f "${PROBE_WORK}/cookies-old"
  check_eq "the new token is spent after one sign-in (401)" 401 "$(bootstrap_login "${new_tok}" "${PROBE_WORK}/cookies-old")"
  rm -f "${PROBE_WORK}/cookies-old"
  # the following stages continue with the session of the new token
  mv "${jar}" "${API_JAR}"
  mv "${new_tok}" "${old_tok}"
  session_refresh
  check_eq "the new session works" admin "$(a /auth/me | jq -r .name)"
  check_eq "audit auth.bootstrap.login successes (stage 5 and the rotation)" 2 "$(audit_count auth.bootstrap.login success)"
  check_ge "audit auth.bootstrap.login failures (refused reuse and old tokens)" 5 "$(audit_count auth.bootstrap.login failure)"
}
