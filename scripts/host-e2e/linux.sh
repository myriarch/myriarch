#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Host end-to-end test, Linux: the installer and the myriarch-updater on a real host with systemd, against a myriarchd built from
# this checkout. It runs on a throwaway runner of GitHub Actions (.github/workflows/host-e2e.yml) and nowhere else: it installs a
# service user, units and files below /etc, /opt and /var, so it refuses to run unless CI=true and HOST_E2E=1 are set, on Linux,
# not as root, and on a host without a trace of an installation. Run `make build` first.
#
#   scripts/host-e2e/linux.sh manual         the scenario "manual": the installer as docs/tasks/connect-a-host.md describes it
#   scripts/host-e2e/linux.sh one-command    the scenario "one-command": the command that the server renders, run unchanged
#
# Scenario "manual" (every step prints PASS, FAIL or SKIP with evidence; the exit status is 0 only without a FAIL):
#   1. myriarchd (dev mode, https with a throwaway CA, the GitOps fixture of scripts/host-e2e/gitops, a mirror of the real release
#      files of the collector and supervisor in 0.161.0 and 0.162.0 as artifact source, verified offline)
#   2. an enrollment token through the API (a file with mode 0600; never printed)
#   3. install-linux.sh as root without --insecure: collector 0.161.0, supervisor, myriarch-updater (built from this checkout,
#      checked by its SHA-256), the CA of myriarchd for the supervisor and the updater, offline Sigstore verification
#   4. the host registers: collector and updater are agents, the fleet host-e2e matches, the configuration is in_sync, healthy
#   5. a second run of the installer changes nothing (no restart)
#   6. a collector archive that is not what the release signed is refused (sigstore_verification_failed), the old version runs on
#   7. the update to 0.162.0 as an operator starts it (select-version): the new collector is killed during the health check, the
#      updater rolls back by itself, the rollout pauses
#   8. retry-failed: the update succeeds (download, verification, installation, restart, health check, health wait)
#   9. install-linux.sh --uninstall --purge, and nothing is left (files, user, units, processes, ports)
# Scenario "one-command" (ADR-0046; the board's acceptance of a release candidate on real runners uses it with HOST_E2E_RELEASE_TAG):
#   1. the installer bundle: built from this checkout in the layout of the release archive, or, with HOST_E2E_RELEASE_TAG, the
#      published archive of that release after the keyless check of its checksums.txt
#   2. myriarchd (dev mode, https with a throwaway CA, the GitOps fixture, the mirror) serving that bundle from a directory
#   3. an enrollment token created through the API at run time (a file with mode 0600; masked in the log, never printed)
#   4. the command rendered through POST /api/v1/install/commands, as the dialog does; the response holds no token
#   5. that command run unchanged as it stands (the command starts the privileged shell itself): the bundle with its pinned SHA-256,
#      the plan from the server, collector and supervisor from the server with install grants, the checks of the installer itself
#   6. the host registers: a healthy collector in the expected fleet at the target version of that fleet, the updater linked
#   7. a configuration rollout: a changed blueprint in Git reaches the collector
#   8. a version update through the updater (the new collector is killed during the health check: rollback by the updater; then the
#      retry succeeds)
#   9. install-linux.sh --uninstall --purge, and nothing is left (also the working directory of the command below /root)
# The token is masked in the log; the upload takes the files of an allow-list only, never the token file or the rendered command.
# After a failure the logs, the journal of both services and the state of the host are collected as artifacts (lib.sh,
# he_collect_artifacts: an allow-list, scrubbed).
#
# Structure (so that a second scenario, for example the one-command installation, is cheap): scenarios are functions in the
# scripts of this directory, the harness is lib.sh, the workflow calls a scenario by name, and every scenario ends with
# he_reset_host (the uninstall and the proof that nothing is left).
set -uo pipefail
# shellcheck source=scripts/host-e2e/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SCENARIOS="manual one-command"

usage() {
  cat >&2 <<EOF
usage: scripts/host-e2e/linux.sh <scenario>...
scenarios: ${SCENARIOS}
Runs on a throwaway CI runner only (CI=true, HOST_E2E=1); see docs/development.md, "Host end-to-end test".
EOF
}

# ---- predicates for he_wait_for ------------------------------------------------------------------------------------------
he_gitops_ok() { [ "$(he_api GET /gitops/status | jq -r .state)" = ok ]; }
he_collector_registered() { [ -n "$(he_agent_id site.id "$1")" ]; }
he_updater_linked() { [ -n "$(he_updater_id "$1")" ]; }
he_update_component_ended() { # updater-agent-id: the updater has ended its attempt
  local st
  st="$(he_api GET "/agents/$1" | jq -r --arg c "$(he_brand UpdateHealthComponent)" '.agent.health.components[$c].status // empty')"
  [ "${st}" = rolled_back ] || [ "${st}" = failed ]
}
he_unit_state() { he_sudo systemctl is-active "$1.service" 2>/dev/null; }
he_main_pid() { he_sudo systemctl show -p MainPID --value "$1.service" 2>/dev/null; }
he_collector_pid() { pgrep -x otelcol-contrib 2>/dev/null | head -n 1; }
he_collector_running() { [ -n "$(he_collector_pid)" ]; }
he_collector_binary() { # the binary the running collector was started from
  local pid
  pid="$(he_collector_pid)"
  if [ -n "${pid}" ]; then he_sudo readlink "/proc/${pid}/exe" 2>/dev/null; fi
}

# The update through the updater, as the UI starts it, in two attempts (steps n1 and n2): the new collector is killed during the health
# check, the updater rolls back by itself and the rollout pauses; retry-failed then succeeds. Shared by the scenarios that have an
# installed collector at the old version, an updater linked to it and the fleet of the GitOps fixture.
he_update_with_rollback() { # fleet collector-agent-id updater-agent-id old-version new-version n1 n2
  local fleet="$1" col="$2" upd="$3" v_old="$4" v_new="$5" n1="$6" n2="$7" r1 s
  he_section "${n1}. update to ${v_new} as the UI starts it: the new collector dies during the health check, the updater rolls back"
  he_fault_start "${v_new}" || he_warn "the fault injector did not report its pid"
  r1="$(he_select_version "${fleet}" "${v_new}" 'host-e2e failure case' "${v_old}")"
  if [ -z "${r1}" ]; then he_fail "select-version started a rollout"; return 1; fi
  he_check_eq "rollout origin" ui "$(he_api GET "/rollouts/${r1}" | jq -r .origin)"
  he_check_eq "target of the fleet is set by the UI" "ui/${v_new}" "$(he_api GET "/fleets/${fleet}" | jq -r '"\(.status.versionTarget.source)/\(.status.versionTarget.version)"')"
  he_wait_for 300 he_rollout_final "${r1}"
  if he_wait_for 200 he_update_component_ended "${upd}"; then
    he_pass "the updater ended the attempt" "$(he_api GET "/agents/${upd}" | jq -r --arg c "$(he_brand UpdateHealthComponent)" '.agent.health.components[$c].status')"
  else
    he_fail "the updater ended the attempt" "no rolled_back or failed status within 200 s"
  fi
  he_fault_stop
  # A preflight that the system refused to start ("operation not permitted") is no failure of the collector: find out which sandbox
  # setting of the updater unit refuses it while the installation is still in place (the uninstall at the end removes it).
  if he_sudo journalctl -u myriarch-updater.service --no-pager -n 400 2>/dev/null | grep -q 'operation not permitted'; then
    he_probe_preflight_sandbox
  fi
  he_wait_for 60 he_agent_is "${col}" .agent.version "${v_old}"
  he_wait_for 30 sh -c "[ \"\$(curl -s -o /dev/null -w '%{http_code}' http://localhost:13133/)\" = 200 ]"
  # The checks below read what the server derived from the last reports of the updater and the collector, and the API returns the
  # stored copy. The engine derives the phase and the steps of the target on its next tick after the last report of the updater (up
  # to rollouts.tickInterval, 1 s here), so a read right after the report can still show rolling_back:running; the package status is
  # a message of its own. Wait for that state instead of reading at a fixed moment (docs/design/version-selection.md, "Target
  # progress", "Derivation"; scripts/e2e-ui-updates.sh waits for the same two states). The wait records PASS or FAIL with the last
  # value it saw, and the checks go on either way.
  he_settle "the engine derived the phase of the target" 30 rolled_back he_target_phase "${r1}"
  he_settle "package status of the updater reports the failed install" 30 install_failed he_package_status "${upd}"
  he_check_eq "rollout paused after the failed target (policy onFailure: pause)" paused "$(he_api GET "/rollouts/${r1}" | jq -r .status)"
  he_check_eq "collector reports ${v_old} again" "${v_old}" "$(he_api GET "/agents/${col}" | jq -r .agent.version)"
  he_check_eq "current -> versions/${v_old} again" "versions/${v_old}" "$(readlink /opt/myriarch/collector/current)"
  he_check_contains "step rolling_back is done" "rolling_back:done" "$(he_steps_of "${r1}")"
  he_check_contains "step health_check failed" "health_check:failed" "$(he_steps_of "${r1}")"
  he_check_eq "target status" failed "$(he_api GET "/rollouts/${r1}/targets" | jq -r '.items[0].status')"
  he_check_eq "the rolled back version is removed from versions/" no "$([ -e "/opt/myriarch/collector/versions/${v_new}" ] && echo yes || echo no)"
  he_check_ge "the fault injector killed the new collector at least once" 1 "$(grep -c '^killed otelcol-contrib' "${HE_DIR}/fault.log")"
  he_check_eq "otelcol-supervisor is active again" active "$(he_unit_state otelcol-supervisor)"

  he_section "${n2}. retry-failed: the update succeeds"
  he_log "waiting 20 s (failedRetryBackoff 15 s) before retry-failed"
  sleep 20
  he_wait_for 120 he_agent_is "${col}" .agent.health.healthy true
  he_api POST "/rollouts/${r1}/actions/retry-failed" '{"reason":"host-e2e retry"}' > /dev/null
  he_wait_for 420 he_rollout_final "${r1}"
  he_check_eq "rollout completed" completed "$(he_api GET "/rollouts/${r1}" | jq -r .status)"
  for s in downloading verifying installing restarting health_check; do
    he_check_contains "step ${s} is done" "${s}:done" "$(he_steps_of "${r1}")"
  done
  he_check_not_contains "no rolling_back step in the successful attempt" "rolling_back" "$(he_steps_of "${r1}")"
  he_wait_for 60 he_agent_is "${col}" .agent.version "${v_new}"
  he_check_eq "collector reports ${v_new}" "${v_new}" "$(he_api GET "/agents/${col}" | jq -r .agent.version)"
  he_check_eq "current -> versions/${v_new}" "versions/${v_new}" "$(readlink /opt/myriarch/collector/current)"
  he_check_eq "the running collector is the binary of versions/${v_new}" "/opt/myriarch/collector/versions/${v_new}/otelcol-contrib" "$(he_collector_binary)"
  he_wait_for 60 he_agent_is "${col}" .agent.health.healthy true
  he_check_eq "collector healthy" true "$(he_api GET "/agents/${col}" | jq -r .agent.health.healthy)"
  he_check_eq "package status installed" installed "$(he_package_status "${upd}")"
  he_check_eq "the old version is kept for the step back (keepVersions 2)" yes "$([ -d "/opt/myriarch/collector/versions/${v_old}" ] && echo yes || echo no)"
  he_check_eq "selection recorded: who and why" "admin/host-e2e failure case" \
    "$(he_api GET "/fleets/${fleet}" | jq -r '"\(.status.versionTarget.selection.selectedBy)/\(.status.versionTarget.selection.reason)"')"
  he_check_ge "audit: fleet.version.select" 1 "$(he_api GET "/audit?action=fleet.version.select&targetType=fleet&targetId=${fleet}" | jq '.items | length')"
}

# ---- scenario "manual" ---------------------------------------------------------------------------------------------------
scenario_manual() {
  local fleet=host-e2e site_id=host-e2e-01 v_old="${HE_VERSION_OLD}" v_new="${HE_VERSION_NEW}"
  local arch endpoint token_file updater_asset updater_sha templates ilog col upd r1 mirror_url evil_url s
  local server_attr method_attr pid_a pid_b main_a main_b upd_main_a upd_main_b

  arch="$(he_arch)" || { he_fail "supported CPU architecture" "$(uname -m)"; return 1; }
  endpoint="wss://127.0.0.1:${HE_PORT_SERVER}/v1/opamp"
  token_file="${HE_DIR}/enrollment-token"
  updater_asset="$(he_updater_asset)"
  updater_sha="$(he_sha256 "${HE_BIN}/$(he_brand UpdaterBinary)")"
  mirror_url="https://127.0.0.1:${HE_PORT_MIRROR}"
  evil_url="https://127.0.0.1:${HE_PORT_EVIL}"
  server_attr="$(he_brand AttrUpdaterServerEndpoint)"
  method_attr="$(he_brand AttrUpdaterInstallMethod)"
  templates="$(he_prepare_templates)" || { he_fail "templates for the installer" "could not prepare ${HE_DIR}/templates"; return 1; }

  # the arguments of the installer for the manual path: the same run, again and again
  local install_args=(
    --opamp-endpoint "${endpoint}" --token-file "${token_file}"
    --site-type edge --site-id "${site_id}" --site-name 'Host end-to-end test' --site-country NL --site-timezone Europe/Amsterdam
    --collector-version "${v_old}" --release-base-url "${mirror_url}"
    --ca-file "${HE_TLS}/ca.pem" --trusted-root "${HE_MIRROR}/srv/mirror/trusted_root.json"
    --with-updater --updater-url "${mirror_url}/updater/${updater_asset}" --updater-sha256 "${updater_sha}"
    --templates-dir "${templates}"
  )

  # ---- 1. the server
  he_section "1. myriarchd in dev mode over https, with the GitOps fixture and the mirror"
  he_server_start || return 1
  he_login || { he_fail "dev login as admin"; return 1; }
  he_check_eq "dev login as admin" admin "$(he_api GET /auth/me | jq -r .role)"
  he_wait_for 60 he_gitops_ok
  he_check_eq "GitOps status" "ok/0" "$(he_api GET /gitops/status | jq -r '"\(.state)/\(.issues | length)"')"
  he_check_eq "fleet ${fleet} loaded" "${fleet}" "$(he_api GET /fleets | jq -r '[.items[].fleet.name] | join(",")')"
  he_check_eq "Git target of the fleet" "${v_old}/git" "$(he_api GET "/fleets/${fleet}" | jq -r '"\(.status.versionTarget.version)/\(.status.versionTarget.source)"')"
  he_check_eq "release fixtures: latest is ${v_new}" "${v_new}" "$(he_api GET /releases/latest | jq -r .release.version)"

  # ---- 2. the token
  he_section "2. enrollment token"
  if he_token_create host-e2e "${token_file}"; then
    he_pass "enrollment token created through the API" "secret only in a file, not printed"
  else
    he_fail "enrollment token created through the API"
    return 1
  fi
  he_check_eq "token file mode" 600 "$(stat -c %a "${token_file}")"

  # ---- 3. the installation
  he_section "3. install-linux.sh as root, https with the throwaway CA, offline Sigstore verification, without --insecure"
  ilog="${HE_DIR}/install-1.log"
  if he_installer "${install_args[@]}" > "${ilog}" 2>&1; then
    he_pass "installer: collector ${v_old}, supervisor and myriarch-updater" "$(tail -n 1 "${ilog}")"
  else
    he_fail "installer: collector ${v_old}, supervisor and myriarch-updater" "$(grep -E 'ERROR|WARNING' "${ilog}" | tail -n 2 | tr '\n' ' ')"
    return 1
  fi
  he_check_contains "installer verified the collector against its Sigstore bundle" "Sigstore signature of otelcol-contrib_${v_old}_linux_${arch}.tar.gz ok" "$(cat "${ilog}")"
  he_check_contains "installer verified the supervisor against its Sigstore bundle" "Sigstore signature of opampsupervisor_${v_old}_linux_${arch} ok" "$(cat "${ilog}")"
  he_check_eq "otelcol-supervisor is active" active "$(he_unit_state otelcol-supervisor)"
  he_check_eq "myriarch-updater is active" active "$(he_unit_state myriarch-updater)"
  he_check_eq "both units are enabled" "enabled/enabled" "$(he_sudo systemctl is-enabled otelcol-supervisor.service)/$(he_sudo systemctl is-enabled myriarch-updater.service)"
  he_check_eq "current -> versions/${v_old}" "versions/${v_old}" "$(readlink /opt/myriarch/collector/current)"
  he_check_eq "supervisor version file" "${v_old}" "$(cat /opt/myriarch/supervisor/VERSION)"
  he_check_eq "myriarch.env and the token file are 0600 root:root" "600 root:root/600 root:root" \
    "$(he_sudo stat -c '%a %U:%G' /etc/myriarch/myriarch.env)/$(he_sudo stat -c '%a %U:%G' /etc/myriarch/enrollment-token)"
  he_check_eq "the CA of myriarchd is in the configuration directory" "644 root:root" "$(he_sudo stat -c '%a %U:%G' /etc/myriarch/ca.pem)"
  he_wait_for 60 he_collector_running
  he_check_eq "the collector runs as otelcol" otelcol "$(ps -o user= -p "$(he_collector_pid)" 2>/dev/null | tr -d ' ')"

  # ---- 4. the host registers
  he_section "4. the host registers: collector and updater, fleet, configuration, health"
  he_wait_for 240 he_collector_registered "${site_id}"
  col="$(he_agent_id site.id "${site_id}")"
  if [ -z "${col}" ]; then he_fail "collector registered" "no agent with site.id=${site_id}"; return 1; fi
  he_wait_for 240 he_agent_is "${col}" .configSync in_sync
  he_check_eq "collector registered: fleet / version / configuration" "${fleet}/${v_old}/in_sync" \
    "$(he_api GET "/agents/${col}" | jq -r '"\(.agent.assignment.fleetName // "?")/\(.agent.version)/\(.configSync)"')"
  he_wait_for 120 he_agent_is "${col}" .agent.health.healthy true
  he_check_eq "collector healthy and online" "true/online" "$(he_api GET "/agents/${col}" | jq -r '"\(.agent.health.healthy)/\(.agent.connection.status)"')"
  he_check_eq "health endpoint of the collector" 200 "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:13133/)"
  he_wait_for 120 he_updater_linked "${col}"
  upd="$(he_updater_id "${col}")"
  if [ -z "${upd}" ]; then he_fail "myriarch-updater linked to the collector" "no updater agent"; return 1; fi
  he_pass "myriarch-updater linked to the collector" "${upd}"
  he_check_eq "updater install_method" binary "$(he_api GET "/agents/${upd}" | jq -r --arg k "${method_attr}" '.agent.nonIdentifyingAttributes[$k]')"
  he_check_eq "updater server_endpoint" "wss://127.0.0.1:${HE_PORT_SERVER}" "$(he_api GET "/agents/${upd}" | jq -r --arg k "${server_attr}" '.agent.nonIdentifyingAttributes[$k]')"

  # ---- 5. a second run changes nothing
  he_section "5. a second run of the installer changes nothing"
  pid_a="$(he_collector_pid)"
  main_a="$(he_main_pid otelcol-supervisor)"
  upd_main_a="$(he_main_pid myriarch-updater)"
  if he_installer "${install_args[@]}" > "${HE_DIR}/install-2.log" 2>&1; then
    he_pass "installer: second run with the same arguments"
  else
    he_fail "installer: second run with the same arguments" "$(grep -E 'ERROR' "${HE_DIR}/install-2.log" | tail -n 1)"
  fi
  pid_b="$(he_collector_pid)"
  main_b="$(he_main_pid otelcol-supervisor)"
  upd_main_b="$(he_main_pid myriarch-updater)"
  he_check_eq "the supervisor and the collector were not restarted" "${main_a}/${pid_a}" "${main_b}/${pid_b}"
  he_check_eq "the updater was not restarted" "${upd_main_a}" "${upd_main_b}"

  # ---- 6. a tampered archive
  he_section "6. a collector archive that the release did not sign is refused, the old version keeps running"
  he_mirror_tamper "${v_new}" || { he_fail "tampered mirror prepared"; return 1; }
  he_https_start mirror-evil "${HE_WORK}/mirror-evil" "${HE_PORT_EVIL}" || { he_fail "tampered mirror started" "port ${HE_PORT_EVIL}"; return 1; }
  if he_installer --opamp-endpoint "${endpoint}" --token-file "${token_file}" --site-type edge --site-id "${site_id}" \
      --collector-version "${v_new}" --release-base-url "${evil_url}" --ca-file "${HE_TLS}/ca.pem" \
      --trusted-root "${HE_MIRROR}/srv/mirror/trusted_root.json" --templates-dir "${templates}" > "${HE_DIR}/install-tampered.log" 2>&1; then
    he_fail "installer refuses a tampered collector archive" "the installer succeeded"
  else
    he_check_contains "installer refuses a tampered collector archive" "[sigstore_verification_failed]" "$(cat "${HE_DIR}/install-tampered.log")"
  fi
  he_check_eq "current still -> versions/${v_old}" "versions/${v_old}" "$(readlink /opt/myriarch/collector/current)"
  he_check_eq "nothing of ${v_new} was installed" no "$([ -e "/opt/myriarch/collector/versions/${v_new}" ] && echo yes || echo no)"
  he_check_eq "the supervisor keeps running" active "$(he_unit_state otelcol-supervisor)"
  he_check_eq "the collector still reports ${v_old}" "${v_old}" "$(he_api GET "/agents/${col}" | jq -r .agent.version)"

  # ---- 7. and 8. the update with a failure, the rollback and the retry
  he_update_with_rollback "${fleet}" "${col}" "${upd}" "${v_old}" "${v_new}" 7 8 || return 1

  # ---- 9. uninstall
  he_section "9. uninstall --purge, and nothing is left"
  he_reset_host
}

# ---- scenario "one-command" ----------------------------------------------------------------------------------------------
he_config_rollouts_above() { # fleet count: a predicate, there are more configuration rollouts than count
  [ "$(he_api GET "/rollouts?kind=config&fleet=$1&limit=50" | jq '[.items[]?] | length')" -gt "$2" ]
}
he_effective_config_has() { # agent-id text: a predicate, the effective configuration the collector reports contains the text
  he_api GET "/agents/$1/effective-config" | jq -r '.body // empty' | grep -qF -- "$2"
}

scenario_one_command() {
  local fleet=host-e2e site_id=host-e2e-01 v_old="${HE_VERSION_OLD}" v_new="${HE_VERSION_NEW}"
  local arch token_file cmd_file ilog extra col upd target blueprint n_before r2 left log

  arch="$(he_arch)" || { he_fail "supported CPU architecture" "$(uname -m)"; return 1; }
  token_file="${HE_DIR}/enrollment-token"
  cmd_file="${HE_DIR}/command.sh"
  ilog="${HE_DIR}/install-command.log"
  extra="${HE_DIR}/overlay-extra.yaml"
  blueprint="${HE_DIR}/gitops/blueprints/host-e2e.yaml"

  # ---- 1. the bundle
  he_section "1. the installer bundle${HE_RELEASE_TAG:+ of the release ${HE_RELEASE_TAG}}"
  if he_bundle_prepare; then
    he_pass "installer bundle ready" "${HE_BUNDLE_FILE}${HE_RELEASE_TAG:+, published with ${HE_RELEASE_TAG}, checksums.txt verified keyless}"
  else
    he_fail "installer bundle ready" "${HE_RELEASE_TAG:+release ${HE_RELEASE_TAG}: }see the log above"
    return 1
  fi

  # ---- 2. the server
  he_section "2. myriarchd in dev mode over https, serving the bundle from a directory"
  printf 'install:\n  platforms: [linux/%s]\n' "${arch}" > "${extra}"
  HE_OVERLAY_EXTRA="${extra}"
  he_server_start --install-bundle-dir "${HE_BUNDLES}" || return 1
  he_login || { he_fail "dev login as admin"; return 1; }
  he_wait_for 60 he_gitops_ok
  he_check_eq "GitOps status" "ok/0" "$(he_api GET /gitops/status | jq -r '"\(.state)/\(.issues | length)"')"
  he_check_eq "fleet ${fleet} loaded" "${fleet}" "$(he_api GET /fleets | jq -r '[.items[].fleet.name] | join(",")')"
  target="$(he_api GET "/fleets/${fleet}" | jq -r '.status.versionTarget.version')"
  he_check_eq "target version of the fleet" "${v_old}" "${target}"
  he_check_eq "the server serves the bundle: source/status/file/SHA-256" "directory/ready/${HE_BUNDLE_FILE}/${HE_BUNDLE_SHA}" \
    "$(he_api GET /install/bundles | jq -r --arg f "${HE_BUNDLE_FILE}" '[.source, (.items[]? | select(.filename == $f) | .status, .filename, .sha256)] | join("/")')"

  # ---- 3. the token
  he_section "3. enrollment token created at run time"
  if he_token_create host-e2e-one-command "${token_file}"; then
    he_pass "enrollment token created through the API" "secret only in a file, masked in the log, not printed"
  else
    he_fail "enrollment token created through the API"
    return 1
  fi
  he_check_eq "token file mode" 600 "$(stat -c %a "${token_file}")"

  # ---- 4. the command
  he_section "4. the command, rendered through POST /api/v1/install/commands"
  if he_command_render "${fleet}" "${site_id}" "${token_file}" "${cmd_file}"; then
    he_pass "command rendered for linux/${arch}" "variant token_inline, fleet ${fleet}, site edge/${site_id}"
  else
    he_fail "command rendered for linux/${arch}" "$(head -c 300 "${HE_DIR}/commands-response.json" 2>/dev/null | tr '\n' ' ')"
    return 1
  fi
  if grep -qF -f "${token_file}" "${HE_DIR}/commands-response.json"; then
    he_fail "the response of the server holds no token" "the token is in the response"
  else
    he_pass "the response of the server holds no token"
  fi
  he_check_contains "the command pins the SHA-256 of the bundle" "${HE_BUNDLE_SHA}" "$(jq -r '.commands[0].command' "${HE_DIR}/commands-response.json")"
  he_check_contains "the command names the bundle" "${HE_BUNDLE_FILE}" "$(jq -r '.commands[0].command' "${HE_DIR}/commands-response.json")"

  # ---- 5. the command, unchanged
  he_section "5. the command, unchanged"
  if he_command_run "${cmd_file}" "${ilog}"; then
    he_pass "the command ran to the end" "$(tail -n 1 "${ilog}")"
  else
    he_fail "the command ran to the end" "$(grep -E 'ERROR|WARNING' "${ilog}" | tail -n 2 | tr '\n' ' ')"
    rm -f "${cmd_file}"
    return 1
  fi
  rm -f "${cmd_file}"
  log="$(cat "${ilog}")"
  if grep -qF -f "${token_file}" "${ilog}"; then he_fail "the token is not in the output of the command" "it is"; else he_pass "the token is not in the output of the command"; fi
  he_check_contains "the bundle was checked against the pinned SHA-256 before it was unpacked" "the SHA-256 of the installer bundle is the one in the command" "${log}"
  he_check_contains "the plan names the fleet and the version" "plan: fleet ${fleet}, collector v${v_old} and supervisor v${v_old} (version from git), updater: true" "${log}"
  he_check_contains "the installer verified the collector against its Sigstore bundle" "Sigstore signature of otelcol-contrib_${v_old}_linux_${arch}.tar.gz ok" "${log}"
  he_check_contains "the installer verified the supervisor against its Sigstore bundle" "Sigstore signature of opampsupervisor_${v_old}_linux_${arch} ok" "${log}"
  he_check_eq "otelcol-supervisor is active" active "$(he_unit_state otelcol-supervisor)"
  he_check_eq "myriarch-updater is active" active "$(he_unit_state myriarch-updater)"
  he_check_eq "both units are enabled" "enabled/enabled" "$(he_sudo systemctl is-enabled otelcol-supervisor.service)/$(he_sudo systemctl is-enabled myriarch-updater.service)"
  he_check_eq "current -> versions/${v_old}" "versions/${v_old}" "$(readlink /opt/myriarch/collector/current)"
  he_check_eq "myriarch.env and the token file are 0600 root:root" "600 root:root/600 root:root" \
    "$(he_sudo stat -c '%a %U:%G' /etc/myriarch/myriarch.env)/$(he_sudo stat -c '%a %U:%G' /etc/myriarch/enrollment-token)"
  left="$(he_sudo bash -c 'ls -d /root/myriarch-setup.* 2>/dev/null' || true)"
  he_check_eq "the working directory of the command below /root is gone" "" "${left}"
  he_wait_for 60 he_collector_running
  he_check_eq "the collector runs as otelcol" otelcol "$(ps -o user= -p "$(he_collector_pid)" 2>/dev/null | tr -d ' ')"

  # ---- 6. the host registers
  he_section "6. the host registers: a healthy collector in the expected fleet at its target version"
  he_wait_for 240 he_collector_registered "${site_id}"
  col="$(he_agent_id site.id "${site_id}")"
  if [ -z "${col}" ]; then he_fail "collector registered" "no agent with site.id=${site_id}"; return 1; fi
  he_wait_for 240 he_agent_is "${col}" .configSync in_sync
  he_wait_for 120 he_agent_is "${col}" .agent.health.healthy true
  target="$(he_api GET "/fleets/${fleet}" | jq -r '.status.versionTarget.version')"
  he_check_eq "collector registered: fleet / version / configuration / health" "${fleet}/${target}/in_sync/true" \
    "$(he_api GET "/agents/${col}" | jq -r '"\(.agent.assignment.fleetName // "?")/\(.agent.version)/\(.configSync)/\(.agent.health.healthy)"')"
  he_check_eq "health endpoint of the collector" 200 "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:13133/)"
  he_wait_for 120 he_updater_linked "${col}"
  upd="$(he_updater_id "${col}")"
  if [ -z "${upd}" ]; then he_fail "myriarch-updater linked to the collector" "no updater agent"; return 1; fi
  he_pass "myriarch-updater linked to the collector" "${upd}"

  # The templates of the bundle are the ones of the product (health timeout 2 minutes, a failed version retried after 10 minutes).
  # An operator may shorten the waits in /etc/myriarch/updater.yaml, and the test does so, so that the rollback and the retry take
  # minutes: the same values as the scenario "manual" puts into its templates.
  if he_tune_installed_updater; then he_pass "waits of the installed updater shortened for the test (/etc/myriarch/updater.yaml)"; else he_fail "waits of the installed updater shortened for the test"; return 1; fi
  he_wait_for 120 he_agent_is "${upd}" .agent.connection.status online
  he_check_eq "myriarch-updater is active after the restart" active "$(he_unit_state myriarch-updater)"

  # ---- 7. a configuration rollout
  he_section "7. a configuration rollout: a changed blueprint in Git reaches the collector"
  n_before="$(he_api GET "/rollouts?kind=config&fleet=${fleet}&limit=50" | jq '[.items[]?] | length')"
  sed -i.bak 's/timeout: 5s/timeout: 7s/' "${blueprint}" && rm -f "${blueprint}.bak"
  he_check_eq "the blueprint of the GitOps fixture was changed" 1 "$(grep -c 'timeout: 7s' "${blueprint}")"
  he_api POST /gitops/actions/sync > /dev/null
  he_wait_for 90 he_config_rollouts_above "${fleet}" "${n_before}"
  r2="$(he_api GET "/rollouts?kind=config&fleet=${fleet}&limit=50" | jq -r '[.items[]?] | sort_by(.createdAt) | last | .id // empty')"
  if [ -z "${r2}" ]; then he_fail "a configuration rollout started" "none after the sync"; return 1; fi
  he_wait_for 180 he_rollout_final "${r2}"
  he_check_eq "configuration rollout completed" completed "$(he_api GET "/rollouts/${r2}" | jq -r .status)"
  he_wait_for 90 he_agent_is "${col}" .configSync in_sync
  he_check_eq "collector in sync again" in_sync "$(he_api GET "/agents/${col}" | jq -r .configSync)"
  he_wait_for 90 he_effective_config_has "${col}" 'timeout: 7s'
  he_check_true "the collector reports the changed configuration" he_effective_config_has "${col}" 'timeout: 7s'
  he_wait_for 60 he_agent_is "${col}" .agent.health.healthy true
  he_check_eq "collector healthy after the rollout" true "$(he_api GET "/agents/${col}" | jq -r .agent.health.healthy)"

  # ---- 8. the update and the rollback
  he_update_with_rollback "${fleet}" "${col}" "${upd}" "${v_old}" "${v_new}" 8 9 || return 1

  # ---- 10. uninstall
  he_section "10. uninstall --purge, and nothing is left"
  he_reset_host
  left="$(he_sudo bash -c 'ls -d /root/myriarch-setup.* 2>/dev/null' || true)"
  he_check_eq "no working directory of the command is left below /root" "" "${left}"
  he_check_eq "the rendered command is gone" no "$([ -e "${cmd_file}" ] && echo yes || echo no)"
}

# ---- main ----------------------------------------------------------------------------------------------------------------
main() {
  local s
  case "${1:-}" in
    -h | --help | help) usage; exit 0 ;;
    "") usage; exit 2 ;;
  esac
  for s in "$@"; do
    case " ${SCENARIOS} " in
      *" ${s} "*) ;;
      *) usage; he_die "unknown scenario '${s}'" ;;
    esac
  done
  he_require_ci
  he_require_sudo
  local rc=0
  for s in "$@"; do
    he_run_scenario "${s}" "scenario_${s//-/_}" || rc=1
  done
  exit "${rc}"
}

main "$@"
