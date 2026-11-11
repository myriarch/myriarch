# shellcheck shell=bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Checks on what the browser observed of the update flow (the JSON of ui-update.mjs), sourced by scripts/e2e-ui-updates.sh.
# They read the language-independent phase key (data-phase of the step bar), and they rest on states that last longer than the
# slowest UI refresh (about 3 s), not on a short phase that the documented derivation makes optional (health_check).

# ui_live_before_end JSON: yes when .phaseKeys holds a live phase of the running update before the first final phase; else the keys
ui_live_before_end() {
  printf '%s' "$1" | jq -r '
    (.phaseKeys // []) as $k
    | if ($k | length) == 0 then "no phase key in the page (a UI build without data-phase: make web-build)"
      else
        ($k | map(. as $x | ["succeeded","failed","rolled_back","cancelled"] | any(.[]; . == $x)) | index(true)) as $end
        | ($k | map(. as $x | ["downloading","verifying","installing","restarting","health_check","rolling_back","health_wait"] | any(.[]; . == $x))) as $live
        | if $end != null and ($live[0:$end] | any(.[]; . == true)) then "yes" else "keys: " + ($k | join(" > ")) end
      end'
}

# ui_step_running_before_failed JSON STEP: yes when a step snapshot shows STEP:running before any snapshot that shows STEP:failed
ui_step_running_before_failed() {
  printf '%s' "$1" | jq -r --arg s "$2" '
    (.stepSnapshots // []) as $snaps
    | ($snaps | map(split(",") | any(.[]; . == ($s + ":running"))) | index(true)) as $run
    | ($snaps | map(split(",") | any(.[]; . == ($s + ":failed"))) | index(true)) as $fail
    | if $run != null and ($fail == null or $run < $fail) then "yes"
      else ($s + ":running at " + ($run | tostring) + ", " + $s + ":failed at " + ($fail | tostring)) end'
}
