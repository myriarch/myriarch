#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Local end-to-end test of the UI-triggered version update, WITHOUT containers (fixtures and helpers: scripts/e2e/ui-updates/).
#
#   scripts/e2e-ui-updates.sh            full run (myriarchd + REAL supervisor/collector + myriarch-updater in container mode)
#   make e2e-ui-updates                  the same
#   E2E_BACKEND=0 scripts/e2e-ui-updates.sh   without the new myriarchd API (select-version, progress): the update is triggered by the Git
#                                        target (collector.version) and checked on the health component of the updater
#   NO_BUILD=1                           use the existing binaries (E2E_BIN_DIR, default bin/)
#   E2E_BIN_DIR=<dir>                    directory of myriarchd, myriarch-updater and myrctl (default bin/; with NO_BUILD=1 nothing is built)
#   E2E_GIT_TARGET=git|none              git (default): the fleet has a Git target (collector.version 0.161.0) under the UI selection; none: the fleet has NO Git
#                                        target (the version exists only once an operator selects it in the UI)
#   E2E_UI=auto|1|0                      drive the operator's path through the REAL UI in a headless Chromium-based browser (scripts/e2e/ui-updates/ui-update.mjs:
#                                        dialog, live step bar, retry button, "per UI gesetzt" marker; screenshots in $E2E_DIR/shots); auto = when playwright-core
#                                        (E2E_PLAYWRIGHT_NODE_PATH) and a browser (E2E_BROWSER) are found, else only the REST calls the UI makes
#   Platform mapping: the mirror serves the REAL release files of THIS machine's platform (darwin/arm64 here, PLATFORMS in the mirror build) and myriarchd
#                                        prefetches exactly that platform (artifacts.prefetchPlatforms); the cluster uses linux/amd64 the same way.
#   E2E_KEEP=1                           leave everything running at the end (stop with: scripts/e2e-ui-updates.sh --stop)
#   E2E_DIR=<dir>                        work directory (default .e2e/ui-updates, gitignored)
#
# What runs (everything on this machine, own ports, nothing is touched outside .e2e/ui-updates and .cache):
#   - a mini mirror (python http.server) with the REAL release files of otelcol-contrib 0.161.0 and 0.162.0 in the GitHub layout
#     (scripts/build-mirror-image-crane.sh OUT_DIR=...: SHA-256 + Sigstore bundle checked offline), the trusted_root.json of the mirror
#   - myriarchd (dev mode, embedded PostgreSQL) with artifacts.sourceBaseUrl = the mirror and OFFLINE Sigstore verification
#   - myriarch-updater in container mode (PID 1 stand-in): seeds otelcol-contrib 0.161.0, runs the real opampsupervisor 0.161.0 as its child
#   - the operator's path of the UI: GET version-options, POST select-version (what the dialog does), then the live progress
#     (steps) while the update runs; the failure case is a REAL rollback (killer.sh kills the new collector during the health check),
#     the success case follows after retry-failed.
# Every step prints PASS/FAIL with the evidence; the exit code is 0 only if no step failed. Everything started is stopped again.
#
# Ports: myriarchd 18090, PostgreSQL 54340, mirror 19997, updater probes 23135, supervisor health 23134, collector health 23133, OTLP 24317/24318.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT" || exit 1
FIX="$ROOT/scripts/e2e/ui-updates"

E2E_DIR=${E2E_DIR:-$ROOT/.e2e/ui-updates}
PORT_MYRIARCHD=${E2E_PORT_MYRIARCHD:-18090}
PORT_PG=${E2E_PORT_PG:-54340}
PORT_MIRROR=${E2E_PORT_MIRROR:-19997}
PORT_UPD_HEALTH=${E2E_PORT_UPD_HEALTH:-23135}
PORT_SUP_HEALTH=${E2E_PORT_SUP_HEALTH:-23134}
PORT_COL_HEALTH=${E2E_PORT_COL_HEALTH:-23133}
BACKEND=${E2E_BACKEND:-1}
GIT_TARGET=${E2E_GIT_TARGET:-git}
BIN=${E2E_BIN_DIR:-$ROOT/bin}
TOKEN=myre_devtoken_insecure_do_not_use_in_prod
V_OLD=0.161.0
V_NEW=0.162.0
FLEET=local-gw
BASE="http://127.0.0.1:$PORT_MYRIARCHD"
CJ="$E2E_DIR/cookies"
GITOPS="$E2E_DIR/gitops"
INSTALL_DIR="$E2E_DIR/collector"
STATE_DIR="$E2E_DIR/updater-state"
SUP_STORAGE="$E2E_DIR/supervisor-storage"
PIDS_FILE="$E2E_DIR/pids"
CACHE_DIR="$ROOT/.cache/e2e-ui"
RESULTS=()
FAILED=0

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) OS=darwin; ARCH=arm64 ;;
  Darwin-x86_64) OS=darwin; ARCH=amd64 ;;
  Linux-x86_64) OS=linux; ARCH=amd64 ;;
  Linux-aarch64|Linux-arm64) OS=linux; ARCH=arm64 ;;
  *) echo "unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

# ------------------------------------------------------------------ output --
log()  { printf '%s %s\n' "$(date +%T)" "$*" >&2; }
pass() { RESULTS+=("PASS|$1|$2"); printf '  \033[32mPASS\033[0m %s  [%s]\n' "$1" "$2" >&2; }
fail() { RESULTS+=("FAIL|$1|$2"); FAILED=$((FAILED + 1)); printf '  \033[31mFAIL\033[0m %s  [%s]\n' "$1" "$2" >&2; }
skip() { RESULTS+=("SKIP|$1|$2"); printf '  SKIP %s  [%s]\n' "$1" "$2" >&2; }
section() { printf '\n== %s ==\n' "$*" >&2; }
check_eq() { if [ "$2" = "$3" ]; then pass "$1" "$3"; else fail "$1" "expected '$2', got '$3'"; fi; }
check_ge() { if [ "${3:-0}" -ge "$2" ] 2>/dev/null; then pass "$1" "$3 >= $2"; else fail "$1" "expected >= $2, got '$3'"; fi; }
check_contains() { if printf '%s' "$3" | grep -q -- "$2"; then pass "$1" "contains '$2'"; else fail "$1" "'$2' not found in: $(printf '%s' "$3" | head -c 300)"; fi; }

# wait_until TIMEOUT_SECONDS 'bash expression' [POLL_SECONDS]
wait_until() {
  local expr=$2 poll=${3:-1} end=$((SECONDS + $1))
  while [ $SECONDS -lt $end ]; do
    if eval "$expr" >/dev/null 2>&1; then return 0; fi
    sleep "$poll"
  done
  return 1
}

# settle WHAT TIMEOUT_SECONDS 'command that prints the state' WANTED: wait until the state is WANTED, for state that the checks below
# read. Logs how long it took, or what was found instead; the run goes on, and the check that reads the same state reports the failure.
settle() {
  local what=$1 limit=$2 cmd=$3 want=$4 from=$SECONDS
  if wait_until "$limit" "[ \"\$($cmd)\" = '$want' ]" 1; then
    log "$what settled after $((SECONDS - from))s"
  else
    log "WARNING: $what did not settle within ${limit}s (wanted '$want', found '$(eval "$cmd" 2>/dev/null)')"
  fi
}

# --------------------------------------------------------------------- API --
a()  { curl -s -b "$CJ" "$BASE/api/v1$1" "${@:2}"; }
ap() { # POST PATH [JSON]
  local body=${2:-}; [ -n "$body" ] || body='{}'
  curl -s -b "$CJ" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -X POST "$BASE/api/v1$1" -d "$body"
}
login() {
  rm -f "$CJ"
  curl -s -c "$CJ" -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' "$BASE/api/v1/auth/dev-login" >/dev/null
  CSRF=$(awk '$6=="myriarch_csrf"{print $7}' "$CJ")
}
agent_id() { a "/agents?attr=site.id%3De2e-gw&kind=collector" | jq -r '.items[0].id // empty'; }
updater_id() { a "/agents/$1" | jq -r '.agent.updater.agentId // empty'; }
# latest version rollout of the fleet
latest_rollout() { a "/rollouts?kind=version&fleet=$FLEET&limit=50" | jq -r '[.items[]?]|sort_by(.createdAt)|last|.id // empty'; }

# ---------------------------------------------------------------- processes --
track() { echo "$1 $2" >> "$PIDS_FILE"; }
start_bg() { # NAME LOGFILE CMD...
  local name=$1 logf=$2; shift 2
  ( ulimit -n 65536 2>/dev/null || ulimit -n 10240 2>/dev/null
    export MYRIARCH_ENROLLMENT_TOKEN="$TOKEN"
    exec "$@" ) >"$logf" 2>&1 &
  track "$name" $!
  echo $!
}
stop_named() { # NAME-PREFIX
  local name pid
  [ -f "$PIDS_FILE" ] || return 0
  while read -r name pid; do case "$name" in "$1"*) kill -TERM "$pid" 2>/dev/null ;; esac; done < "$PIDS_FILE"
}
kill_tracked() {
  [ -f "$PIDS_FILE" ] || return 0
  local name pid pass
  # helpers first, the updater (graceful: stops its child group), myriarchd and the mirror last
  for pass in sampler killer updater mirror myriarchd; do
    stop_named "$pass"
    sleep 1
  done
  local end=$((SECONDS + 45))
  while [ $SECONDS -lt $end ]; do
    local alive=0
    while read -r name pid; do kill -0 "$pid" 2>/dev/null && alive=1; done < "$PIDS_FILE"
    [ $alive = 0 ] && break
    sleep 1
  done
  while read -r name pid; do kill -KILL "$pid" 2>/dev/null; done < "$PIDS_FILE"
  : > "$PIDS_FILE"
  # whatever the supervisor/collector left behind (they are children of the updater, normally gone already)
  pkill -f "$E2E_DIR/(real|collector)/" 2>/dev/null
  return 0
}
stop_postgres() {
  local pf="$E2E_DIR/data/postgres/data/postmaster.pid" pid
  [ -f "$pf" ] || return 0
  pid=$(head -1 "$pf")
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null
    local end=$((SECONDS + 30))
    while [ $SECONDS -lt $end ] && kill -0 "$pid" 2>/dev/null; do sleep 1; done
    kill -0 "$pid" 2>/dev/null && kill -KILL "$pid" 2>/dev/null
  fi
}
cleanup() {
  local rc=$?
  if [ "${E2E_KEEP:-0}" = 1 ] && [ "${STOPPING:-0}" != 1 ]; then
    log "E2E_KEEP=1: processes stay up (myriarchd $BASE); stop with: scripts/e2e-ui-updates.sh --stop"
    return
  fi
  section "cleanup"
  kill_tracked
  stop_postgres
  if pgrep -f "$E2E_DIR" >/dev/null 2>&1; then log "WARNING: processes referencing $E2E_DIR are still running: $(pgrep -f "$E2E_DIR" | tr '\n' ' ')"; fi
  return $rc
}
if [ "${1:-}" = "--stop" ]; then STOPPING=1; kill_tracked; stop_postgres; echo "stopped"; exit 0; fi
trap cleanup EXIT
trap 'log "interrupted"; exit 130' INT TERM

# --------------------------------------------------------------- observation --
# watch_rollout ID TIMEOUT -> records the distinct phases of the first target (every 0.5 s) in $E2E_DIR/phases-<id>.txt until the
# rollout is in a final/paused state; echoes the final rollout status.
watch_rollout() {
  local id=$1 end=$((SECONDS + $2)) st phase last=""
  : > "$E2E_DIR/phases-$id.txt"
  while [ $SECONDS -lt $end ]; do
    st=$(a "/rollouts/$id" | jq -r .status)
    if [ "$BACKEND" = 1 ]; then
      phase=$(a "/rollouts/$id/targets" | jq -r '.items[0].progress.phase // empty')
    else
      phase=$(a "/agents/$UPD" | jq -r '.agent.health.components["dev.myriarch.update"].status // empty')
    fi
    if [ -n "$phase" ] && [ "$phase" != "$last" ]; then echo "$phase" >> "$E2E_DIR/phases-$id.txt"; last=$phase; fi
    case "$st" in completed|paused|failed|aborted|rolled_back|superseded) echo "$st"; return 0 ;; esac
    sleep 0.5
  done
  echo "$st"
  return 1
}
phases_of() { [ -f "$E2E_DIR/phases-$1.txt" ] && tr '\n' ' ' < "$E2E_DIR/phases-$1.txt" | sed 's/ $//'; return 0; }

# step statuses of the first target as "name:status,..." (BACKEND) or of the updater health component (no backend)
steps_of() { # ROLLOUT
  if [ "$BACKEND" = 1 ]; then
    a "/rollouts/$1/targets" | jq -r '[.items[0].progress.steps[]? | "\(.name):\(.status)"] | join(",")'
  else
    a "/agents/$UPD" | jq -r '[.agent.health.components["dev.myriarch.update"].components // {} | to_entries[] | select(.key|startswith("step:")) | "\(.key|sub("step:";"")):\(.value.status)"] | sort | join(",")'
  fi
}
step_status() { # ROLLOUT STEP
  if [ "$BACKEND" = 1 ]; then
    a "/rollouts/$1/targets" | jq -r --arg s "$2" '[.items[0].progress.steps[]? | select(.name==$s) | .status][0] // "absent"'
  else
    a "/agents/$UPD" | jq -r --arg s "step:$2" '.agent.health.components["dev.myriarch.update"].components[$s].status // "absent"'
  fi
}

# select_version VERSION REASON -> echoes the rollout id. With the new API this is what the UI dialog does; without it the Git target is changed.
select_version() {
  local v=$1 reason=$2 resp
  if [ "$BACKEND" = 1 ]; then
    resp=$(ap "/fleets/$FLEET/actions/select-version" "{\"version\":\"$v\",\"reason\":\"$reason\",\"expectedTargetVersion\":\"$PRE_VERSION\"}")
    SELECT_RESPONSE=$resp
    echo "$resp" | jq -r '.rolloutId // empty'
  else
    sed -i.bak "s/^    version: .*/    version: $v/" "$GITOPS/fleets/$FLEET.yaml" && rm -f "$GITOPS/fleets/$FLEET.yaml.bak"
    ap /gitops/actions/sync >/dev/null
    wait_until 30 '[ -n "$(latest_rollout)" ]' 1
    latest_rollout
  fi
}

sample_rss() { # background: peak RSS (MB) of updater + supervisor + collector, written to $E2E_DIR/rss-peak.txt
  local peak=0 kb pid
  while :; do
    kb=0
    for pid in $(pgrep -f "$E2E_DIR/(updater.yaml|real/opampsupervisor|collector/versions)" 2>/dev/null); do
      kb=$((kb + $(ps -o rss= -p "$pid" 2>/dev/null | tr -d ' ' || echo 0)))
    done
    [ "$kb" -gt "$peak" ] && { peak=$kb; echo $((peak / 1024)) > "$E2E_DIR/rss-peak.txt"; }
    sleep 1
  done
}

# ------------------------------------------------------------ prerequisites --
section "0. prerequisites and build"
for tool in go jq curl python3 tar lsof; do
  command -v "$tool" >/dev/null || { fail "tool $tool" "not found"; exit 1; }
done
mkdir -p "$E2E_DIR" "$CACHE_DIR"
if [ -f "$PIDS_FILE" ] && [ -s "$PIDS_FILE" ]; then log "stale processes from an earlier run: stopping"; kill_tracked; stop_postgres; fi
: > "$PIDS_FILE"
for p in $PORT_MYRIARCHD $PORT_PG $PORT_MIRROR $PORT_UPD_HEALTH $PORT_SUP_HEALTH $PORT_COL_HEALTH; do
  if curl -s -m 1 "http://127.0.0.1:$p/" >/dev/null 2>&1 || (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
    fail "port $p free" "something is already listening (choose E2E_PORT_* or stop it)"; exit 1
  fi
done
pass "ports free" "$PORT_MYRIARCHD $PORT_PG $PORT_MIRROR $PORT_UPD_HEALTH $PORT_SUP_HEALTH $PORT_COL_HEALTH"
if [ "$BACKEND" != 1 ]; then log "E2E_BACKEND=0: no select-version/progress API; the update is triggered through the Git target"; fi

if [ "${NO_BUILD:-0}" != 1 ] && [ "$BIN" = "$ROOT/bin" ]; then
  if [ ! -f internal/webui/dist/index.html ] && command -v pnpm >/dev/null; then
    log "building the UI (make web-build)"; make web-build >"$E2E_DIR/web-build.log" 2>&1 || { fail "make web-build" "see $E2E_DIR/web-build.log"; exit 1; }
  fi
  if make build >"$E2E_DIR/build.log" 2>&1; then pass "make build" "bin/myriarchd bin/myriarch-updater bin/myrctl"; else fail "make build" "see $E2E_DIR/build.log"; exit 1; fi
fi
for b in myriarchd myriarch-updater myrctl; do [ -x "$BIN/$b" ] || { fail "binary $BIN/$b" "missing"; exit 1; }; done

# The operator's path through the REAL UI (headless browser, scripts/e2e/ui-updates/ui-update.mjs): E2E_UI=1 requires it, 0 uses only the
# REST calls the UI makes, auto (default) uses the browser when playwright-core and a Chromium-based browser are found.
UI=${E2E_UI:-auto}
BROWSER_BIN=${E2E_BROWSER:-}
if [ -z "$BROWSER_BIN" ]; then
  for c in "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser" "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge" /usr/bin/chromium /usr/bin/google-chrome; do
    [ -x "$c" ] && BROWSER_BIN=$c && break
  done
fi
PW_PATH=${E2E_PLAYWRIGHT_NODE_PATH:-}
if [ -z "$PW_PATH" ]; then for c in "$E2E_DIR/node_modules" "$ROOT/web/node_modules" "$ROOT/node_modules"; do [ -d "$c/playwright-core" ] && PW_PATH=$c && break; done; fi
if [ "$UI" = auto ]; then [ -n "$BROWSER_BIN" ] && [ -n "$PW_PATH" ] && [ "$BACKEND" = 1 ] && UI=1 || UI=0; fi
SHOTS="$E2E_DIR/shots"
# ui_run MODE [VAR=value ...] -> the JSON result of ui-update.mjs on stdout (progress on stderr)
ui_run() {
  local mode=$1; shift
  env NODE_PATH="$PW_PATH" BROWSER="$BROWSER_BIN" BASE="$BASE" FLEET="$FLEET" SHOTS="$SHOTS" "$@" node "$FIX/ui-update.mjs" "$mode" 2>>"$E2E_DIR/ui-update.log" | tail -1
}

# ---------------------------------------------------------------- 1. mirror --
section "1. in-namespace mirror stand-in: real release files, checked offline"
MIRROR="$E2E_DIR/mirror"
rm -rf "$MIRROR"
if OUT_DIR="$MIRROR" VERSIONS="$V_OLD $V_NEW" PLATFORMS="$OS/$ARCH" scripts/build-mirror-image-crane.sh >"$E2E_DIR/mirror-build.log" 2>&1; then
  pass "mirror directory built (SHA-256 + Sigstore bundle verified offline)" "$(grep -c 'verified otelcol-contrib' "$E2E_DIR/mirror-build.log") archives"
else fail "mirror build" "see $E2E_DIR/mirror-build.log: $(tail -2 "$E2E_DIR/mirror-build.log" | tr '\n' ' ')"; exit 1; fi
TRUSTED_ROOT="$MIRROR/srv/mirror/trusted_root.json"
if [ "$GIT_TARGET" = none ]; then PRE_VERSION=""; PRE_STR="null/none"; else PRE_VERSION=$V_OLD; PRE_STR="$V_OLD/git"; fi
ASSET_OLD="otelcol-contrib_${V_OLD}_${OS}_${ARCH}.tar.gz"
ASSET_NEW="otelcol-contrib_${V_NEW}_${OS}_${ARCH}.tar.gz"
start_bg mirror "$E2E_DIR/mirror.log" python3 -m http.server "$PORT_MIRROR" --bind 127.0.0.1 --directory "$MIRROR/srv/mirror" >/dev/null
wait_until 20 "curl -sf -o /dev/null http://127.0.0.1:$PORT_MIRROR/v$V_NEW/$ASSET_NEW.sha256"
check_eq "mirror serves the release layout (v$V_NEW/$ASSET_NEW.sha256)" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT_MIRROR/v$V_NEW/$ASSET_NEW.sha256")"
check_eq "mirror serves the Sigstore bundle" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT_MIRROR/v$V_NEW/$ASSET_NEW.sigstore.json")"

# --------------------------------------------- 2. real supervisor + seed binary --
section "2. real opampsupervisor $V_OLD and the seed binary"
SUP_ASSET="opampsupervisor_${V_OLD}_${OS}_${ARCH}"
SUP_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fopampsupervisor%2Fv$V_OLD/$SUP_ASSET"
if [ ! -x "$CACHE_DIR/$SUP_ASSET" ]; then
  log "downloading $SUP_ASSET (~30 MB)"
  curl -fsSL -o "$CACHE_DIR/$SUP_ASSET.part" "$SUP_URL" && curl -fsSL -o "$CACHE_DIR/$SUP_ASSET.sha256" "$SUP_URL.sha256" \
    && mv "$CACHE_DIR/$SUP_ASSET.part" "$CACHE_DIR/$SUP_ASSET" && chmod +x "$CACHE_DIR/$SUP_ASSET" || { fail "download opampsupervisor" "network? $SUP_URL"; exit 1; }
fi
WANT=$(awk 'NR==1{print $1}' "$CACHE_DIR/$SUP_ASSET.sha256" 2>/dev/null)
GOT=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$CACHE_DIR/$SUP_ASSET")
check_eq "opampsupervisor $V_OLD: SHA-256 matches the published checksum" "$WANT" "$GOT"
SEED="$E2E_DIR/seed"
rm -rf "$SEED"; mkdir -p "$SEED"
tar -xzf "$MIRROR/srv/mirror/v$V_OLD/$ASSET_OLD" -C "$SEED" otelcol-contrib && chmod +x "$SEED/otelcol-contrib"
check_eq "seed binary (image stand-in) version" "otelcol-contrib version $V_OLD" "$("$SEED/otelcol-contrib" --version 2>&1 | head -1)"
SUP_BIN="$E2E_DIR/real/opampsupervisor"
mkdir -p "$E2E_DIR/real" && cp "$CACHE_DIR/$SUP_ASSET" "$SUP_BIN" && chmod +x "$SUP_BIN"

# ------------------------------------------------------------------ 3. myriarchd --
section "3. myriarchd (dev mode, offline verification) with the Git frame for fleet $FLEET"
rm -rf "$E2E_DIR/data" "$E2E_DIR/fixtures" "$INSTALL_DIR" "$STATE_DIR" "$SUP_STORAGE" "$GITOPS"
mkdir -p "$SUP_STORAGE" "$E2E_DIR/gitops"
cp -R "$FIX/gitops/." "$GITOPS/"
cp -R internal/release/testdata/fixtures "$E2E_DIR/fixtures"
if [ "$GIT_TARGET" = none ]; then
  # no Git target: the version comes from the UI only
  python3 - "$GITOPS/fleets/$FLEET.yaml" <<'PY'
import re, sys, pathlib
p = pathlib.Path(sys.argv[1]); s = p.read_text()
s = re.sub(r'  collector:\n(    .*\n)+', '', s)
p.write_text(s)
PY
  grep -q 'collector:' "$GITOPS/fleets/$FLEET.yaml" && { fail "E2E_GIT_TARGET=none" "collector block still in the fleet"; exit 1; }
fi
if [ "$BACKEND" != 1 ]; then
  # an older myriarchd does not know update.selection: remove the block (the Git target triggers the update)
  python3 - "$GITOPS/fleets/$FLEET.yaml" <<'PY'
import re, sys, pathlib
p = pathlib.Path(sys.argv[1]); s = p.read_text()
s = re.sub(r'    selection:\n(      .*\n)+', '', s)
p.write_text(s)
PY
fi
sed_tmpl() { # TEMPLATE OUT
  sed -e "s#@@PORT_MYRIARCHD@@#$PORT_MYRIARCHD#g" -e "s#@@PORT_PG@@#$PORT_PG#g" -e "s#@@PORT_MIRROR@@#$PORT_MIRROR#g" \
      -e "s#@@PORT_SUP_HEALTH@@#$PORT_SUP_HEALTH#g" -e "s#@@PORT_COL_HEALTH@@#$PORT_COL_HEALTH#g" -e "s#@@PORT_UPD_HEALTH@@#$PORT_UPD_HEALTH#g" \
      -e "s#@@INSTALL_DIR@@#$INSTALL_DIR#g" -e "s#@@SUPERVISOR_STORAGE@@#$SUP_STORAGE#g" -e "s#@@STATE_DIR@@#$STATE_DIR#g" \
      -e "s#@@TRUSTED_ROOT@@#$TRUSTED_ROOT#g" -e "s#@@HOSTNAME@@#$(hostname)#g" -e "s#@@PLATFORM@@#$OS/$ARCH#g" \
      -e "s#@@SUPERVISOR_BIN@@#$SUP_BIN#g" -e "s#@@SUPERVISOR_CONFIG@@#$E2E_DIR/supervisor.yaml#g" -e "s#@@SEED_BINARY@@#$SEED/otelcol-contrib#g" "$1" > "$2"
}
sed_tmpl "$FIX/myriarchd-overlay.yaml.tmpl" "$E2E_DIR/overlay.yaml"
if [ "$BACKEND" = 1 ]; then
  # artifacts.agentBaseUrls: the updater (ws://127.0.0.1:...) downloads from 127.0.0.1 although server.publicUrl says localhost
  printf '  agentBaseUrls: ["http://127.0.0.1:%s"]\n' "$PORT_MYRIARCHD" > "$E2E_DIR/agent-base-urls.txt"
else
  # without agentBaseUrls the offer URL is built from server.publicUrl, which must then match the updater's allowed host
  printf 'server:\n  publicUrl: http://127.0.0.1:%s\n' "$PORT_MYRIARCHD" > "$E2E_DIR/agent-base-urls.txt"
fi
python3 - "$E2E_DIR/overlay.yaml" "$E2E_DIR/agent-base-urls.txt" <<'PY'
import sys, pathlib
p = pathlib.Path(sys.argv[1]); extra = pathlib.Path(sys.argv[2]).read_text()
p.write_text(p.read_text().replace('@@AGENT_BASE_URLS@@\n', extra).replace('@@AGENT_BASE_URLS@@', extra))
PY
sed_tmpl "$FIX/supervisor.yaml.tmpl" "$E2E_DIR/supervisor.yaml"
sed_tmpl "$FIX/updater.yaml.tmpl" "$E2E_DIR/updater.yaml"
if [ "$BACKEND" = 1 ]; then
  if "$BIN/myrctl" validate "$GITOPS" --strict --components examples/sim/components-otelcol-contrib-0.162.0.json >"$E2E_DIR/validate.log" 2>&1; then
    pass "myrctl validate --strict (frame in Git)" "$(tail -1 "$E2E_DIR/validate.log")"
  else fail "myrctl validate" "$(tail -3 "$E2E_DIR/validate.log" | tr '\n' ' ')"; fi
fi
start_bg myriarchd "$E2E_DIR/myriarchd.log" "$BIN/myriarchd" dev --listen "127.0.0.1:$PORT_MYRIARCHD" --gitops-dir "$GITOPS" --data-dir "$E2E_DIR/data" \
  -c "$E2E_DIR/overlay.yaml" --releases-fixtures "$E2E_DIR/fixtures" --replica-id e2e-ui >/dev/null
if wait_until 120 "curl -sf $BASE/readyz"; then pass "myriarchd /readyz" "200 on $BASE"; else fail "myriarchd /readyz" "see $E2E_DIR/myriarchd.log"; exit 1; fi
login
check_eq "dev login as admin" admin "$(a /auth/me | jq -r .role)"
wait_until 60 '[ "$(a /gitops/status | jq -r .state)" = ok ]'
check_eq "GitOps status" "ok/0" "$(a /gitops/status | jq -r '"\(.state)/\(.issues|length)"')"
check_eq "fleet $FLEET loaded" "$FLEET" "$(a /fleets | jq -r '[.items[].fleet.name]|join(",")')"
check_eq "release fixtures: latest is $V_NEW" "$V_NEW" "$(a /releases/latest | jq -r .release.version)"

# ------------------------------------------- 4. myriarch-updater container mode --
section "4. myriarch-updater in container mode: seed, supervisor child, link"
"$BIN/myriarch-updater" version >/dev/null 2>&1 || true
rm -f "$E2E_DIR/rss-peak.txt"
sample_rss >/dev/null 2>&1 &
track sampler $!
UPD_PID=$(start_bg updater "$E2E_DIR/updater.log" "$BIN/myriarch-updater" container --config "$E2E_DIR/updater.yaml")
if wait_until 90 'grep -q "\"msg\":\"seed decision\"" "$E2E_DIR/updater.log"'; then
  SEEDLINE=$(grep -m1 '"msg":"seed decision"' "$E2E_DIR/updater.log")
  check_eq "seed decision: image $V_OLD activated on the empty volume" "activate-image/$V_OLD/$V_OLD" \
    "$(echo "$SEEDLINE" | jq -r '"\(.decision)/\(.imageVersion)/\(.active)"')"
else fail "seed decision logged" "see $E2E_DIR/updater.log"; fi
check_eq "current -> versions/$V_OLD" "versions/$V_OLD" "$(readlink "$INSTALL_DIR/current" 2>/dev/null)"
wait_until 180 '[ -n "$(agent_id)" ]'
COL=$(agent_id)
if [ -z "$COL" ]; then fail "collector registered" "no agent with site.id=e2e-gw; see $E2E_DIR/updater.log"; exit 1; fi
wait_until 120 "[ \"\$(a /agents/$COL | jq -r .configSync)\" = in_sync ]"
check_eq "collector registered: fleet / version / config" "$FLEET/$V_OLD/in_sync" "$(a /agents/$COL | jq -r '"\(.agent.assignment.fleetName // "?")/\(.agent.version)/\(.configSync)"')"
wait_until 120 "[ -n \"\$(updater_id $COL)\" ]"
UPD=$(updater_id "$COL")
check_eq "myriarch-updater linked to the collector" true "$([ -n "$UPD" ] && echo true || echo false)"
check_eq "updater install_method=container" container "$(a /agents/$UPD | jq -r '.agent.nonIdentifyingAttributes["dev.myriarch.updater.install_method"]')"
check_eq "updater server_endpoint attribute" "ws://127.0.0.1:$PORT_MYRIARCHD" "$(a /agents/$UPD | jq -r '.agent.nonIdentifyingAttributes["dev.myriarch.updater.server_endpoint"]')"
check_eq "updater image_collector_version" "$V_OLD" "$(a /agents/$UPD | jq -r '.agent.nonIdentifyingAttributes["dev.myriarch.updater.image_collector_version"]')"
check_eq "updater probe /healthz" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT_UPD_HEALTH/healthz")"
wait_until 60 "[ \"\$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$PORT_UPD_HEALTH/readyz)\" = 200 ]"
check_eq "updater probe /readyz (child runs)" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT_UPD_HEALTH/readyz")"
check_eq "collector health endpoint" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT_COL_HEALTH/")"

# --------------------------------------------------- 5. the UI path: dialog data --
section "5. what the update dialog shows (GET version-options)"
if [ "$BACKEND" = 1 ]; then
  OPT=$(a "/fleets/$FLEET/version-options")
  check_eq "selection allowed (frame enabled, executor updater)" true "$(echo "$OPT" | jq -r .allowed)"
  check_eq "effective target before: $PRE_STR" "$PRE_STR" "$(echo "$OPT" | jq -r '"\(.target.version)/\(.target.source)"')"
  check_eq "$V_NEW is selectable" true "$(echo "$OPT" | jq -r --arg v "$V_NEW" '.options[]|select(.version==$v)|.selectable')"
  if [ "$GIT_TARGET" = none ]; then
    check_eq "no Git target: no option is the current target" 0 "$(echo "$OPT" | jq '[.options[]|select(.isCurrentTarget)]|length')"
  else
    check_eq "$V_OLD is the current target (not selectable)" current_target "$(echo "$OPT" | jq -r --arg v "$V_OLD" '.options[]|select(.version==$v)|.reasonCode')"
  fi
  check_ge "$V_NEW affects at least one collector" 1 "$(echo "$OPT" | jq -r --arg v "$V_NEW" '.options[]|select(.version==$v)|.agentsToUpdate')"
  check_eq "frame is shown" ">=0.161.0, <=0.162.0" "$(echo "$OPT" | jq -r '.target.frame.versionRange')"
  # a version outside the Git frame is refused with a clear, machine-readable error (and audited as a failed attempt)
  REJ=$(curl -s -b "$CJ" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -w '\n%{http_code}' -X POST "$BASE/api/v1/fleets/$FLEET/actions/select-version" \
        -d '{"version":"0.163.0","reason":"e2e outside the frame"}')
  check_eq "select-version outside the Git frame -> HTTP 422" 422 "$(echo "$REJ" | tail -1)"
  check_contains "rejection names version_not_allowed" version_not_allowed "$(echo "$REJ" | sed '$d')"
  check_eq "target unchanged after the rejection" "$PRE_STR" "$(a "/fleets/$FLEET" | jq -r '"\(.status.versionTarget.version)/\(.status.versionTarget.source)"')"
  check_ge "rejected attempt is audited (result failure)" 1 "$(a "/audit?action=fleet.version.select&targetType=fleet&targetId=$FLEET" | jq '[.items[]|select(.result=="failure")]|length')"
else skip "version-options (dialog data)" "E2E_BACKEND=0"; fi

# ---------------------------------------------------------- 6. failure case --
section "6. failure case: the new collector dies during the health check -> automatic rollback"
KILL_START=$SECONDS
start_bg killer "$E2E_DIR/killer.log" "$FIX/killer.sh" "$INSTALL_DIR" "$V_NEW" 0.3 >/dev/null
if [ "$UI" = 1 ]; then
  log "operator path through the real UI (browser): fleet page -> button -> dialog -> start -> live step bar"
  UIJ=$(ui_run select VERSION="$V_NEW" REASON="e2e failure" TIMEOUT=240 UNTIL="zurückgerollt|abgebrochen")  # check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
  echo "$UIJ" > "$E2E_DIR/ui-select.json"
  R1=$(echo "$UIJ" | jq -r '.rolloutId // empty')
  if [ -z "$R1" ]; then fail "UI: dialog started a rollout" "$(echo "$UIJ" | head -c 400)"; exit 1; fi
  check_contains "UI: fleet page button" "Auf Version $V_NEW aktualisieren" "$(echo "$UIJ" | jq -r .buttonText)"  # check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
  check_contains "UI: dialog offers $V_NEW and the Git frame summary" "$V_NEW" "$(echo "$UIJ" | jq -r .dialogText)"
  UI_PHASES=$(echo "$UIJ" | jq -r '.phases|join(" > ")')
  log "UI phases seen by the browser: $UI_PHASES"
  check_contains "UI showed a live phase before the end (health check)" "Health" "$UI_PHASES"
  check_contains "UI showed the step rolling_back running (live) while the updater rolled back" "rolling_back:running" "$(echo "$UIJ" | jq -r '.stepSnapshots|join(" ")')"
  check_contains "UI showed the step health_check failed" "health_check:failed" "$(echo "$UIJ" | jq -r '.stepSnapshots|join(" ")')"
  check_contains "UI ended on the rolled back phase" "zurückgerollt" "$UI_PHASES"  # check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
  check_eq "UI: no page errors / 5xx" 0 "$(echo "$UIJ" | jq -r '.problems|length')"
else
  R1=$(select_version "$V_NEW" "e2e failure")
  if [ -z "$R1" ]; then fail "select-version started a rollout" "response: ${SELECT_RESPONSE:-<git target changed>}"; exit 1; fi
fi
if [ "$BACKEND" = 1 ]; then
  check_eq "rollout created by the UI action (origin ui)" "ui" "$(a "/rollouts/$R1" | jq -r .origin)"
  check_eq "versionTarget.source=ui right after the selection" "ui/$V_NEW" "$(a /fleets/$FLEET | jq -r '"\(.status.versionTarget.source)/\(.status.versionTarget.version)"')"
fi
if [ "$UI" = 1 ]; then FINAL1=$(a "/rollouts/$R1" | jq -r .status); else FINAL1=$(watch_rollout "$R1" 240); fi
log "phases observed: $(phases_of "$R1"); rollout status: $FINAL1 ($((SECONDS - KILL_START))s)"
# The rollout can pause (collector unhealthy after the version change) while the updater is still in its health check: the killer
# must keep going until the updater has given up on the new version and rolled back.
if wait_until 150 "[ \"\$(a /agents/$UPD | jq -r '.agent.health.components[\"dev.myriarch.update\"].status // empty')\" = rolled_back ] || [ \"\$(a /agents/$UPD | jq -r '.agent.health.components[\"dev.myriarch.update\"].status // empty')\" = failed ]" 1; then
  pass "the updater ended the attempt (rolled back) after $((SECONDS - KILL_START))s" "$(a /agents/$UPD | jq -r '.agent.health.components["dev.myriarch.update"].status')"
else fail "the updater ended the attempt" "component status: $(a /agents/$UPD | jq -r '.agent.health.components["dev.myriarch.update"].status')"; fi
stop_named killer
# The checks below read what the server derived from the last reports of the updater and the collector: wait for that state instead
# of a fixed time. The engine derives the final phase of the failed target on its next tick after the updater's last report (about
# 1 s, at most the tick interval); the version of the restarted old collector and the package status of the updater are separate
# OpAMP messages that may arrive after the last health report of the updater.
if [ "$BACKEND" = 1 ]; then
  settle "target progress phase" 30 "a /rollouts/$R1/targets | jq -r '.items[0].progress.phase'" rolled_back
fi
settle "collector version" 30 "a /agents/$COL | jq -r .agent.version" "$V_OLD"
settle "updater package status" 30 "a /agents/$UPD | jq -r '.agent.packageStatus.packages[\"\"].status // \"none\"'" install_failed
check_eq "rollout paused after the failed target (policy onFailure: pause)" paused "$FINAL1"
check_eq "collector reports $V_OLD again (rolled back)" "$V_OLD" "$(a /agents/$COL | jq -r .agent.version)"
check_eq "current -> versions/$V_OLD again" "versions/$V_OLD" "$(readlink "$INSTALL_DIR/current")"
check_contains "step rolling_back is done" "rolling_back:done" "$(steps_of "$R1")"
check_contains "step health_check failed" "health_check:failed" "$(steps_of "$R1")"
if [ "$BACKEND" = 1 ]; then
  check_eq "target progress.phase=rolled_back" rolled_back "$(a "/rollouts/$R1/targets" | jq -r '.items[0].progress.phase')"
  check_eq "target status failed" failed "$(a "/rollouts/$R1/targets" | jq -r '.items[0].status')"
else
  check_eq "updater component phase rolled_back" rolled_back "$(a /agents/$UPD | jq -r '.agent.health.components["dev.myriarch.update"].status')"
fi
check_contains "package status install_failed on the updater" install_failed "$(a /agents/$UPD | jq -r '.agent.packageStatus.packages[""].status // "none"')"
if [ "$UI" = 1 ]; then
  # The target 0.162.0 stays set while the collector runs the old version again: the fleet page must not say "up to date" and the rollout page
  # points to the way out (the selection is still active, the rollout is paused).
  UIJN=$(ui_run notreached ROLLOUT="$R1")
  echo "$UIJN" > "$E2E_DIR/ui-notreached.json"
  check_contains "UI fleet page: target not reached alert after the host rollback" "noch nicht erreicht" "$(echo "$UIJN" | jq -r '.notReached // ""')"  # check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
  check_eq "UI fleet page does not claim 'Bereits auf der neuesten erlaubten Version'" false "$(echo "$UIJN" | jq -r '.claimsUpToDate')"  # check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
  check_contains "UI rollout page (paused): hint that the UI selection is still active" "UI-Auswahl" "$(echo "$UIJN" | jq -r '.rolloutHint // ""')"
  check_eq "UI: no page errors / 5xx (after rollback)" 0 "$(echo "$UIJN" | jq -r '.problems|length')"
fi
check_ge "killer killed the new collector at least once" 1 "$(grep -c '^killed otelcol-contrib' "$E2E_DIR/killer.log")"
check_eq "the rolled back binary is not in versions/ any more" "no" "$([ -d "$INSTALL_DIR/versions/$V_NEW" ] && echo yes || echo no)"

# ---------------------------------------------------------- 7. success case --
section "7. success case: retry after the backoff"
log "waiting 20 s (failedRetryBackoff 15 s) before retry-failed"
sleep 20
wait_until 60 "[ \"\$(a /agents/$COL | jq -r .agent.health.healthy)\" = true ]"
# the rollout stays the same (paused after the failed target); retry-failed gives the failed target another attempt
R2=$R1
if [ "$UI" = 1 ]; then
  UIJ2=$(ui_run retry ROLLOUT="$R1" REASON="e2e retry" TIMEOUT=420 UNTIL="erfolgreich|zurückgerollt|abgebrochen")  # check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
  echo "$UIJ2" > "$E2E_DIR/ui-retry.json"
  check_contains "UI: retry button of the paused rollout" "wiederholen" "$(echo "$UIJ2" | jq -r '.retryButton // ""')"
  check_contains "UI showed the successful phase" "erfolgreich" "$(echo "$UIJ2" | jq -r '.phases|join(" > ")')"
  log "UI phases of the retry: $(echo "$UIJ2" | jq -r '.phases|join(" > ")')"
  FINAL2=$(a "/rollouts/$R2" | jq -r .status)
  : > "$E2E_DIR/phases-$R2.txt"
  # phase labels of the German UI locale -> engine step names
  phase_map=(
    -e 's/^Health-Check (Host)$/health_check/'   # check-language: ignore
    -e 's/^Bewährung (myriarchd)$/health_wait/'     # check-language: ignore
    -e 's/^erfolgreich$/succeeded/'              # check-language: ignore
    -e 's/^Download$/downloading/'
    -e 's/^Prüfung$/verifying/'                  # check-language: ignore
    -e 's/^Installation$/installing/'
    -e 's/^Neustart$/restarting/'                # check-language: ignore
    -e 's/^angeboten$/offered/'                  # check-language: ignore
  )
  echo "$UIJ2" | jq -r '.phases[]' | sed "${phase_map[@]}" > "$E2E_DIR/phases-$R2.txt"
else
  ap "/rollouts/$R1/actions/retry-failed" '{"reason":"e2e retry"}' >/dev/null
  FINAL2=$(watch_rollout "$R2" 420)
fi
log "phases observed: $(phases_of "$R2"); rollout status: $FINAL2"
check_eq "rollout completed" completed "$FINAL2"
STEPS2=$(steps_of "$R2")
for s in downloading verifying installing restarting health_check; do check_contains "step $s done" "$s:done" "$STEPS2"; done
check_eq "no rolling_back step in the successful attempt" "absent" "$(step_status "$R2" rolling_back)"
OBS=$(phases_of "$R2")
# downloading .. restarting take milliseconds locally, and the host health check of a healthy collector can finish in
# under a second; their steps[] are checked above. Both observers (the UI and watch_rollout) poll twice a second, so
# only the live progress itself is checked here, not a particular short phase.
if [ "$UI" = 1 ]; then
  check_ge "UI observed at least 3 distinct live phases during the retry" 3 "$(echo "$OBS" | wc -w | tr -d ' ')"
else
  check_ge "at least 3 distinct live phases observed during the retry" 3 "$(echo "$OBS" | wc -w | tr -d ' ')"
fi
if [ "$BACKEND" = 1 ]; then
  check_contains "phase health_wait observed live" health_wait "$OBS"
  check_contains "final phase succeeded" succeeded "$OBS"
  check_eq "target progress.phase=succeeded" succeeded "$(a "/rollouts/$R2/targets" | jq -r '.items[0].progress.phase')"
  check_eq "progress source=updater" updater "$(a "/rollouts/$R2/targets" | jq -r '.items[0].progress.source')"
fi
wait_until 60 "[ \"\$(a /agents/$COL | jq -r .agent.version)\" = $V_NEW ]"
check_eq "collector reports $V_NEW" "$V_NEW" "$(a /agents/$COL | jq -r .agent.version)"
check_eq "current -> versions/$V_NEW" "versions/$V_NEW" "$(readlink "$INSTALL_DIR/current")"
check_eq "running collector binary is from versions/$V_NEW" yes "$(pgrep -f "$INSTALL_DIR/current/otelcol-contrib" >/dev/null && echo yes || echo no)"
wait_until 60 "[ \"\$(a /agents/$COL | jq -r .agent.health.healthy)\" = true ]"
check_eq "collector healthy" true "$(a /agents/$COL | jq -r .agent.health.healthy)"
check_eq "package status installed" installed "$(a /agents/$UPD | jq -r '.agent.packageStatus.packages[""].status // "none"')"
if [ "$BACKEND" = 1 ]; then
  FL=$(a /fleets/$FLEET)
  check_eq "fleet target: source ui, version $V_NEW" "ui/$V_NEW" "$(echo "$FL" | jq -r '"\(.status.versionTarget.source)/\(.status.versionTarget.version)"')"
  check_eq "selection.selectedBy=admin" admin "$(echo "$FL" | jq -r '.status.versionTarget.selection.selectedBy')"
  check_eq "selection reason is kept" "e2e failure" "$(echo "$FL" | jq -r '.status.versionTarget.selection.reason')"
  check_ge "audit: fleet.version.select" 1 "$(a "/audit?action=fleet.version.select&targetType=fleet&targetId=$FLEET" | jq '.items|length')"
  check_eq "audit actor of the selection" admin "$(a "/audit?action=fleet.version.select&targetType=fleet&targetId=$FLEET" | jq -r '.items[0].actorName // empty')"
  check_eq "rollout origin ui, createdBy admin" "ui/admin" "$(a "/rollouts/$R2" | jq -r '"\(.origin)/\(.createdBy)"')"
  if [ "$UI" = 1 ]; then
    UIJ3=$(ui_run inspect ROLLOUT="$R2")
    echo "$UIJ3" > "$E2E_DIR/ui-inspect.json"
    check_contains "UI fleet page marks the target 'per UI gesetzt'" "per UI gesetzt" "$(echo "$UIJ3" | jq -r '.sectionText // ""')"
    check_contains "UI shows who set it (admin)" "admin" "$(echo "$UIJ3" | jq -r '.selectionInfo // ""')"
    check_contains "UI shows the reason" "e2e failure" "$(echo "$UIJ3" | jq -r '.selectionInfo // ""')"
    check_eq "UI rollout page renders no [object Object]" false "$(echo "$UIJ3" | jq -r '.objectObject')"
    log "screenshots: $SHOTS"
  fi
fi

# ----------------------------------------- 8. restart keeps the updated version --
section "8. container restart: the volume wins over the older image (seedPolicy newest)"
RSS_PEAK=$(cat "$E2E_DIR/rss-peak.txt" 2>/dev/null || echo "?")
log "peak RSS of myriarch-updater + supervisor + collector during the run: ${RSS_PEAK} MB (pod memory budget: limit 768 Mi)"
stop_named updater
wait_until 60 "! kill -0 $UPD_PID 2>/dev/null"
check_eq "SIGTERM: myriarch-updater exited" gone "$(kill -0 "$UPD_PID" 2>/dev/null && echo alive || echo gone)"
check_eq "SIGTERM: supervisor and collector were stopped with it" none "$(pgrep -f "$E2E_DIR/(real|collector)/" >/dev/null && echo some || echo none)"
mv "$E2E_DIR/updater.log" "$E2E_DIR/updater-run1.log"
UPD_PID=$(start_bg updater "$E2E_DIR/updater.log" "$BIN/myriarch-updater" container --config "$E2E_DIR/updater.yaml")
wait_until 90 'grep -q "\"msg\":\"seed decision\"" "$E2E_DIR/updater.log"'
SEEDLINE=$(grep -m1 '"msg":"seed decision"' "$E2E_DIR/updater.log")
check_eq "seed decision after restart: keep $V_NEW (image is $V_OLD)" "keep-active/$V_OLD/$V_NEW" "$(echo "$SEEDLINE" | jq -r '"\(.decision)/\(.imageVersion)/\(.active)"')"
wait_until 120 "[ \"\$(a /agents/$COL | jq -r .agent.connection.status)\" = online ]"
wait_until 120 "[ \"\$(a /agents/$COL | jq -r .agent.health.healthy)\" = true ]"
check_eq "collector comes back as the same agent on $V_NEW" "$V_NEW" "$(a /agents/$COL | jq -r .agent.version)"
check_eq "current still -> versions/$V_NEW" "versions/$V_NEW" "$(readlink "$INSTALL_DIR/current")"

# ------------------------------------------------------------------- summary --
section "summary"
PASSED=0; SKIPPED=0
for r in "${RESULTS[@]}"; do case "${r%%|*}" in PASS) PASSED=$((PASSED + 1)) ;; SKIP) SKIPPED=$((SKIPPED + 1)) ;; esac; done
printf '%s passed, %s failed, %s skipped\n' "$PASSED" "$FAILED" "$SKIPPED" >&2
if [ "$FAILED" -gt 0 ]; then
  printf '\nFailed steps:\n' >&2
  for r in "${RESULTS[@]}"; do case "${r%%|*}" in FAIL) IFS='|' read -r _ n e <<<"$r"; printf '  - %s [%s]\n' "$n" "$e" >&2 ;; esac; done
  printf '\nLogs: %s/{myriarchd,updater,mirror-build,killer}.log\n' "$E2E_DIR" >&2
  exit 1
fi
exit 0
