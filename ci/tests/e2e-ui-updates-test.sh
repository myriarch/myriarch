#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the UI phase checks of scripts/e2e-ui-updates.sh (scripts/e2e/ui-updates/ui-phases.sh) with JSON fixtures of what the
# browser observed. Needs jq, binds no port.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
# shellcheck source=scripts/e2e/ui-updates/ui-phases.sh
. "${ROOT}/scripts/e2e/ui-updates/ui-phases.sh"

# case NAME EXPECTED ACTUAL: EXPECTED "yes" or "not yes"
is_yes() { if [ "$2" = yes ]; then ok "$1"; else fail "$1" "$2"; fi; }
not_yes() { if [ "$2" != yes ]; then ok "$1 ($2)"; else fail "$1"; fi; }

# the failing CI run: no health_check phase, the health_check step runs after it was marked done and fails later
ci='{"phaseKeys":["verifying","health_wait","failed","rolled_back"],"stepSnapshots":["offered:done,verifying:running,health_check:pending","offered:done,verifying:done,health_check:done","offered:done,health_check:running","offered:done,health_check:failed,rolling_back:running","offered:done,health_check:failed,rolling_back:done"]}'
is_yes "failing CI run: a live phase before the end" "$(ui_live_before_end "${ci}")"
is_yes "failing CI run: the health check step ran before it failed" "$(ui_step_running_before_failed "${ci}" health_check)"
local_run='{"phaseKeys":["offered","verifying","health_wait","failed","rolled_back"],"stepSnapshots":["health_check:running","health_check:failed"]}'
is_yes "local run starting with offered" "$(ui_live_before_end "${local_run}")"
nightly='{"phaseKeys":["verifying","health_check","health_wait","failed","rolled_back"],"stepSnapshots":[]}'
is_yes "passing nightly with health_check" "$(ui_live_before_end "${nightly}")"
never_failed='{"stepSnapshots":["health_check:pending","health_check:running","health_check:done"]}'
is_yes "a step that ran and never failed is accepted" "$(ui_step_running_before_failed "${never_failed}" health_check)"

not_yes "offered > failed > rolled_back: no live phase" "$(ui_live_before_end '{"phaseKeys":["offered","failed","rolled_back"]}')"
not_yes "verifying > health_wait: no end" "$(ui_live_before_end '{"phaseKeys":["verifying","health_wait"]}')"
not_yes "offered > failed > health_wait: live only after the end" "$(ui_live_before_end '{"phaseKeys":["offered","failed","health_wait"]}')"
empty="$(ui_live_before_end '{"phaseKeys":[]}')"
not_yes "an empty key list" "${empty}"
case "${empty}" in *"make web-build"*) ok "an empty key list names make web-build" ;; *) fail "an empty key list names make web-build" "${empty}" ;; esac
not_yes "snapshots without health_check:running" "$(ui_step_running_before_failed '{"stepSnapshots":["offered:done","health_check:done","health_check:failed"]}' health_check)"
not_yes "health_check:failed before health_check:running" "$(ui_step_running_before_failed '{"stepSnapshots":["health_check:failed","health_check:running"]}' health_check)"

finish_tests "e2e-ui-updates-test"
