#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# End-to-end test of myriarch without containers (see docs/development.md).
#
#   scripts/e2e.sh                 smoke run: 32 simulated collectors, all rollout types, HA, (UI/real supervisor if available)
#   E2E_AGENTS=1000 scripts/e2e.sh load run (1000 collectors + 687 updaters; E2E_PROFILE selects another profile, default e2e)
#   E2E_REAL=1 scripts/e2e.sh      additionally: REAL opampsupervisor + otelcol-contrib of the OTELCOL_VERSION of ci/versions.env,
#                                  downloaded into .e2e/real/dl (darwin and linux, amd64 and arm64) and verified against the
#                                  published SHA-256 on every run; a missing prerequisite or a failed check is a FAIL, not a
#                                  SKIP (E2E_REAL=auto, the default, runs the step only when the binaries are present;
#                                  E2E_REAL_BASE_URL replaces the GitHub release download URL, file:// works for tests)
#   E2E_UI=1 scripts/e2e.sh        the UI smoke test is a FAIL instead of a SKIP when playwright-core or a browser is missing
#   E2E_UI=0 / E2E_HA=0 / E2E_REAL=0   switch single steps off
#   E2E_KEEP=1                     leave myriarchd/sim running at the end (clean up with scripts/e2e.sh --stop)
#   NO_BUILD=1                     use the existing bin/ binaries
#
# A load run (more than 100 collectors) raises maxConcurrent of the gateway rollout policy from 1 to 10 in its working copy of
# the test data: serially, one rollout of the 125 gateways takes about 13 minutes (a health wait of about 6 s per gateway) and
# exceeds E2E_STEP_TIMEOUT, and 10 equals the batchSize of the webhook executor, so the load run also sends batched webhook
# calls, which the serial smoke run never does.
#
# The run is self-contained: own ports (18080/18081, PostgreSQL 54339), own work dir .e2e/, a local release
# mirror (no network access needed), a mock receiver for webhook/AWX/notifications. Every step prints
# PASS/FAIL with the evidence; the exit code is 0 only if no step failed. Everything started is stopped again.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT" || exit 1

E2E_DIR=${E2E_DIR:-$ROOT/.e2e}
PORT_A=${E2E_PORT_A:-18080}
PORT_B=${E2E_PORT_B:-18081}
PORT_PG=${E2E_PORT_PG:-54339}
PORT_MOCK=${E2E_PORT_MOCK:-19999}
PORT_MIRROR=${E2E_PORT_MIRROR:-19998}
PORT_SIM_CTL=${E2E_PORT_SIM_CTL:-19465}
PORT_SIM_MET=${E2E_PORT_SIM_MET:-19464}
PORT_SIMB_MET=${E2E_PORT_SIMB_MET:-19466}
PORT_SIMB_CTL=${E2E_PORT_SIMB_CTL:-19467}
PORT_SIMD_CTL=${E2E_PORT_SIMD_CTL:-19468}
PORT_SIMD_MET=${E2E_PORT_SIMD_MET:-19469}
PROFILE=${E2E_PROFILE:-e2e}
AGENTS=${E2E_AGENTS:-32}
TOKEN=myre_devtoken_insecure_do_not_use_in_prod
STEP_TIMEOUT=${E2E_STEP_TIMEOUT:-600}
BASE_A="http://127.0.0.1:$PORT_A"
BASE_B="http://127.0.0.1:$PORT_B"
CJ="$E2E_DIR/cookies"
GITOPS="$E2E_DIR/gitops"
REAL_DIR=${E2E_REAL_DIR:-$E2E_DIR/real}

PIDS_FILE="$E2E_DIR/pids"
RESULTS=()
FAILED=0
CURRENT_BASE=$BASE_A

# ------------------------------------------------------------------ output --
log()  { printf '%s %s\n' "$(date +%T)" "$*" >&2; }
pass() { RESULTS+=("PASS|$1|$2"); printf '  \033[32mPASS\033[0m %s  [%s]\n' "$1" "$2" >&2; }
fail() { RESULTS+=("FAIL|$1|$2"); FAILED=$((FAILED + 1)); printf '  \033[31mFAIL\033[0m %s  [%s]\n' "$1" "$2" >&2; }
skip() { RESULTS+=("SKIP|$1|$2"); printf '  SKIP %s  [%s]\n' "$1" "$2" >&2; }
section() { printf '\n== %s ==\n' "$*" >&2; }

# check NAME EXPECTED ACTUAL  (string equality)
check_eq() { if [ "$2" = "$3" ]; then pass "$1" "$3"; else fail "$1" "expected '$2', got '$3'"; fi; }
# check_ge NAME MIN ACTUAL
check_ge() { if [ "${3:-0}" -ge "$2" ] 2>/dev/null; then pass "$1" "$3 >= $2"; else fail "$1" "expected >= $2, got '$3'"; fi; }

# wait_until TIMEOUT_SECONDS 'bash expression'   -> 0 once the expression succeeds
wait_until() {
  local expr=$2 end=$((SECONDS + $1))
  while [ $SECONDS -lt $end ]; do
    if eval "$expr" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  return 1
}

# --------------------------------------------------------------------- API --
a()  { curl -s -b "$CJ" "$CURRENT_BASE/api/v1$1" "${@:2}"; }                 # GET (extra curl args)
ap() { # POST PATH [JSON]
  local body=${2:-}; [ -n "$body" ] || body='{}'
  curl -s -b "$CJ" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -X POST "$CURRENT_BASE/api/v1$1" -d "$body"
}
# latest_rollout KIND FLEET -> id of the newest rollout (list order is not guaranteed)
latest_rollout() { a "/rollouts?kind=$1&fleet=$2&limit=50" | jq -r '[.items[]?]|sort_by(.createdAt)|last|.id // empty'; }
login() {
  rm -f "$CJ"
  curl -s -c "$CJ" -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' \
    "$CURRENT_BASE/api/v1/auth/dev-login" >/dev/null
  CSRF=$(awk '$6=="myriarch_csrf"{print $7}' "$CJ")
}

# approve_waiting -> approves the first waiting wave of every rollout that waits for approval
approve_waiting() {
  local id wave n=0
  for id in $(a '/rollouts?active=true&limit=200' | jq -r '.items[]? | select(.status=="waiting_approval") | .id'); do
    wave=$(a "/rollouts/$id" | jq -r '[.waves[]? | select(.status=="waiting_approval")][0].index // empty')
    if [ -n "$wave" ]; then ap "/rollouts/$id/actions/approve" "{\"wave\":$wave,\"reason\":\"e2e\"}" >/dev/null; n=$((n + 1)); fi
  done
  echo $n
}

# rollouts_summary [kind] -> compact JSON of the (active) rollouts
rollouts_summary() {
  a "/rollouts?${1:+kind=$1&}limit=200" | jq -c '[.items[]? | {k:.kind,f:.fleetName,s:.status,ok:.counts.succeeded,fl:.counts.failed,t:.counts.total}]'
}

# wait_rollouts KIND TIMEOUT -> approves waves; returns 0 when no rollout of the kind is active; 1 on timeout;
# 2 if a rollout is paused/failed (needs attention)
wait_rollouts() {
  local kind=$1 end=$((SECONDS + $2)) act bad
  while [ $SECONDS -lt $end ]; do
    approve_waiting >/dev/null
    act=$(a "/rollouts?kind=$kind&active=true&limit=200")
    bad=$(echo "$act" | jq -r '[.items[]? | select(.status=="paused" or .status=="failed" or .status=="rolling_back")] | length')
    [ "${bad:-0}" -gt 0 ] && return 2
    [ "$(echo "$act" | jq -r '.items | length')" = "0" ] && return 0
    sleep 3
  done
  return 1
}

# HMAC secret shared by myriarchd (executor + notifier) and the mock receiver (at least 32 bytes)
E2E_SECRET=${E2E_SECRET:-devsecret-devsecret-devsecret-devsecret}

# ---------------------------------------------------------------- processes --
track() { echo "$1 $2" >> "$PIDS_FILE"; }

start_bg() { # NAME LOGFILE CMD...
  local name=$1 logf=$2; shift 2
  ( ulimit -n 65536 2>/dev/null || ulimit -n 10240 2>/dev/null
    export MYRIARCH_EXEC_WEBHOOK_SECRET_K8S="$E2E_SECRET" MYRIARCH_EXEC_AWX_TOKEN=devtoken
    exec "$@" ) >"$logf" 2>&1 &
  track "$name" $!
  echo $!
}

kill_tracked() {
  [ -f "$PIDS_FILE" ] || return 0
  local name pid
  # stop clients first, myriarchd replicas (graceful) last
  for pass in sim sup mock mirror myriarchd; do
    while read -r name pid; do
      case "$name" in
        "$pass"*) kill -TERM "$pid" 2>/dev/null ;;
      esac
    done < "$PIDS_FILE"
    sleep 1
  done
  local end=$((SECONDS + 40))
  while [ $SECONDS -lt $end ]; do
    local alive=0
    while read -r name pid; do kill -0 "$pid" 2>/dev/null && alive=1; done < "$PIDS_FILE"
    [ $alive = 0 ] && break
    sleep 1
  done
  while read -r name pid; do kill -KILL "$pid" 2>/dev/null; done < "$PIDS_FILE"
  : > "$PIDS_FILE"
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
    log "E2E_KEEP=1: processes stay up (myriarchd $BASE_A); stop with: scripts/e2e.sh --stop"
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

# ------------------------------------------------------------ prerequisites --
section "0. prerequisites and build"
for tool in go jq curl python3; do
  command -v "$tool" >/dev/null || { fail "tool $tool" "not found"; exit 1; }
done
mkdir -p "$E2E_DIR"
if [ -f "$PIDS_FILE" ] && [ -s "$PIDS_FILE" ]; then log "stale processes from an earlier run: stopping"; kill_tracked; stop_postgres; fi
: > "$PIDS_FILE"
for p in $PORT_A $PORT_B $PORT_PG $PORT_MOCK $PORT_MIRROR $PORT_SIM_CTL; do
  if curl -s -m 1 "http://127.0.0.1:$p/" >/dev/null 2>&1 || (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
    fail "port $p free" "something is already listening (choose E2E_PORT_* or stop it)"; exit 1
  fi
done
pass "ports free" "$PORT_A $PORT_B $PORT_PG $PORT_MOCK $PORT_MIRROR $PORT_SIM_CTL"

if [ "${NO_BUILD:-0}" != 1 ]; then
  if [ ! -f internal/webui/dist/index.html ] && command -v pnpm >/dev/null; then
    log "building the UI (make web-build)"; make web-build >"$E2E_DIR/web-build.log" 2>&1 || { fail "make web-build" "see $E2E_DIR/web-build.log"; exit 1; }
  fi
  if make build >"$E2E_DIR/build.log" 2>&1; then pass "make build" "bin/myriarchd bin/myriarch-sim bin/myrctl bin/myriarch-updater"; else fail "make build" "see $E2E_DIR/build.log"; exit 1; fi
fi
for b in myriarchd myriarch-sim myrctl; do [ -x "bin/$b" ] || { fail "binary bin/$b" "missing"; exit 1; }; done

# ------------------------------------------------------- 1. myriarchd in dev mode --
section "1. myriarchd (dev mode, embedded PostgreSQL, test data repo)"
rm -rf "$E2E_DIR/data" "$E2E_DIR/b" "$E2E_DIR/a2" "$E2E_DIR/fixtures" "$E2E_DIR/sim-state"
python3 scripts/e2e/prepare_repo.py internal/testdata/gitops "$GITOPS" "$PORT_MOCK" || { fail "prepare repo" "script failed"; exit 1; }
# Load run: one gateway at a time would exceed STEP_TIMEOUT (see the header). Only the working copy changes, never
# internal/testdata; the smoke run, with few gateways, keeps maxConcurrent 1 and the serial path.
if [ "$AGENTS" -gt 100 ]; then
  GATEWAY_POLICY="$GITOPS/policies/gateway.yaml"
  sed -i.bak 's/^  maxConcurrent: 1$/  maxConcurrent: 10/' "$GATEWAY_POLICY" && rm -f "$GATEWAY_POLICY.bak"
  if [ "$(grep -c '^  maxConcurrent: 10$' "$GATEWAY_POLICY")" = 1 ]; then
    pass "load run: gateway policy maxConcurrent 10 (batchSize of the webhook executor)" "$GATEWAY_POLICY"
  else
    fail "load run: gateway policy maxConcurrent 10" "the edit of $GATEWAY_POLICY did not apply (expected one line '  maxConcurrent: 1')"; exit 1
  fi
fi
cp -R internal/release/testdata/fixtures "$E2E_DIR/fixtures"
printf "%s" "$E2E_SECRET" > "$E2E_DIR/notify.secret"
# generated credentials for the declarative configuration (mode 0600, never printed)
( umask 077
  openssl rand -base64 36 | tr -d '\n' > "$E2E_DIR/bootstrap.token"
  printf 'myre_%s' "$(openssl rand -hex 24)" > "$E2E_DIR/enroll.token" )
cat > "$E2E_DIR/overlay.yaml" <<EOF
dev:
  postgresPort: $PORT_PG
rollouts:
  tickInterval: 1s
artifacts:
  sourceBaseUrl: http://127.0.0.1:$PORT_MIRROR
  verify:
    requireSignature: false
auth:
  bootstrapAdmin:
    enabled: true
    tokenFile: $E2E_DIR/bootstrap.token
opamp:
  enrollmentTokens:
    - name: e2e-declared
      tokenFile: $E2E_DIR/enroll.token
      labels:
        source: declarative
executors:
  # an empty allowlist refuses every webhook and AWX target (fail closed): the mock receiver is listed explicitly
  allowedWebhookHosts: ["http://127.0.0.1:$PORT_MOCK"]
  # secrets referenced from Git are bound to their destination (the mock receiver)
  secretEnvHosts:
    MYRIARCH_EXEC_WEBHOOK_SECRET_K8S: ["127.0.0.1:$PORT_MOCK"]
    MYRIARCH_EXEC_AWX_TOKEN: ["127.0.0.1:$PORT_MOCK"]
notifications:
  channels:
    - name: mock
      type: webhook
      url: http://127.0.0.1:$PORT_MOCK/notify
      secretFile: $E2E_DIR/notify.secret
EOF
if "$ROOT/bin/myrctl" validate "$GITOPS" >"$E2E_DIR/validate.log" 2>&1; then pass "myrctl validate (working copy)" "$(tail -1 "$E2E_DIR/validate.log")"; else fail "myrctl validate" "$(tail -3 "$E2E_DIR/validate.log" | tr '\n' ' ')"; fi

start_bg mirror "$E2E_DIR/mirror.log" env PORT=$PORT_MIRROR python3 scripts/e2e/mirror.py >/dev/null
start_bg mock "$E2E_DIR/mock.log" env PORT=$PORT_MOCK SECRET="$E2E_SECRET" SIM_CONTROL=127.0.0.1:$PORT_SIM_CTL,127.0.0.1:$PORT_SIMB_CTL python3 scripts/e2e/mock_receiver.py >/dev/null
MYRIARCHD_A_PID=$(start_bg myriarchd-a "$E2E_DIR/myriarchd-a.log" ./bin/myriarchd dev --listen "127.0.0.1:$PORT_A" --gitops-dir "$GITOPS" --data-dir "$E2E_DIR/data" \
  -c "$E2E_DIR/overlay.yaml" --releases-fixtures "$E2E_DIR/fixtures" --replica-id replica-a)
if wait_until 90 "curl -sf $BASE_A/readyz"; then pass "myriarchd /readyz" "200 on $BASE_A"; else fail "myriarchd /readyz" "not ready; see $E2E_DIR/myriarchd-a.log"; exit 1; fi
DSN=$(grep -o 'postgres://[^ "]*' "$E2E_DIR/myriarchd-a.log" | head -1)
login
check_eq "dev login as admin" admin "$(a /auth/me | jq -r .role)"
# bootstrap admin (break-glass): one-time token from the mounted file; the token is never printed
BJ="$E2E_DIR/cookies-bootstrap"
check_eq "bootstrap admin: /auth/config offers the token sign-in" true "$(curl -s $BASE_A/api/v1/auth/config | jq -r .bootstrapTokenEnabled)"
boot_login() { # BASE JAR -> HTTP status of a token sign-in with the configured token file
  jq -Rn --rawfile t "$E2E_DIR/bootstrap.token" '{token:$t}' | curl -s -o /dev/null -w '%{http_code}' -c "$2" -H 'Content-Type: application/json' -d @- "$1/api/v1/auth/bootstrap-login"
}
rm -f "$BJ"
check_eq "bootstrap admin: token sign-in" 200 "$(boot_login $BASE_A $BJ)"
check_eq "bootstrap admin: session has authMethod bootstrap" "bootstrap/admin" "$(curl -s -b "$BJ" $BASE_A/api/v1/auth/me | jq -r '"\(.authMethod)/\(.role)"')"
check_eq "bootstrap admin: the one-time token is refused on reuse" 401 "$(boot_login $BASE_A "$E2E_DIR/cookies-bootstrap-2")"
check_eq "bootstrap admin: audit has one success and one failure" "1/1" "$(a '/audit?action=auth.bootstrap.login&limit=50' | jq -r '"\([.items[]|select(.result=="success")]|length)/\([.items[]|select(.result=="failure")]|length)"')"
check_eq "bootstrap admin: audit has the activation" 1 "$(a '/audit?action=auth.bootstrap.activate&limit=50' | jq '.items|length')"
check_eq "declarative enrollment token listed exactly once with createdBy config" "1/config" "$(a /enrollment-tokens | jq -r '[.items[]|select(.name=="e2e-declared")] | "\(length)/\(.[0].createdBy)"')"
wait_until 60 '[ "$(a /gitops/status | jq -r .state)" = ok ]'
check_eq "GitOps status" "ok/0" "$(a /gitops/status | jq -r '"\(.state)/\(.issues|length)"')"
check_eq "5 fleets" "datacenter,edge-be,edge-nl,k8s-gateway,office" "$(a /fleets | jq -r '[.items[].fleet.name]|sort|join(",")')"
check_eq "blueprints / policies / executors" "5/4/2" "$(echo "$(a /blueprints | jq '.items|length')/$(a /rollout-policies | jq '.items|length')/$(a /executors | jq '.items|length')")"
check_eq "releases from fixtures (latest)" "0.162.0" "$(a /releases/latest | jq -r .release.version)"
check_ge "latest release breaking changes" 1 "$(a /releases/latest | jq '.release.breakingChanges|length')"
check_eq "UI served (embedded)" "200 text/html; charset=utf-8" "$(curl -s -o /dev/null -w '%{http_code} %{content_type}' $BASE_A/)"
# The exact .ico type depends on the host's mime.types, so only the image/ prefix is checked.
check_eq "UI icons served as images" "200 image 200 image 200 image" "$(for p in favicon.svg favicon.ico apple-touch-icon.png; do
  curl -s -o /dev/null -w '%{http_code} %{content_type}\n' "$BASE_A/$p"; done | sed 's|/.*||' | paste -sd' ' -)"
# License texts (public, no login): the embedded copies equal the files of the repository this binary was built from.
for lic in LICENSE THIRD_PARTY_NOTICES; do
  if curl -sf "$BASE_A/licenses/$lic" | cmp -s - "$ROOT/$lic"; then pass "licenses: /licenses/$lic equals $lic" "byte-identical"; else fail "licenses: /licenses/$lic equals $lic" "differs from $ROOT/$lic or not served"; fi
done
check_eq "licenses: served as text/plain" "200 text/plain; charset=utf-8" "$(curl -s -o /dev/null -w '%{http_code} %{content_type}' "$BASE_A/licenses/THIRD_PARTY_NOTICES")"
check_eq "unknown API path is JSON 404" "404 application/problem+json" "$(curl -s -o /dev/null -w '%{http_code} %{content_type}' $BASE_A/api/v1/nope | sed 's/;.*//')"

# --------------------------------------------------------------- 2. simulator --
section "2. myriarch-sim: $AGENTS collectors (profile $PROFILE)"
SIM_START=$SECONDS
start_bg sim "$E2E_DIR/sim.log" ./bin/myriarch-sim run --server "ws://127.0.0.1:$PORT_A/v1/opamp" --token $TOKEN --agents "$AGENTS" \
  --updaters=true --fake-downloads=false --profile "examples/sim/$PROFILE.yaml" --control-listen 127.0.0.1:$PORT_SIM_CTL \
  --metrics-listen 127.0.0.1:$PORT_SIM_MET >/dev/null
if wait_until 300 "[ \"\$(a /stats/overview | jq -r '.agents.collectors')\" = $AGENTS ] && [ \"\$(a /stats/overview | jq -r '.agents.online')\" = \"\$(a /stats/overview | jq -r '.agents.total')\" ]"; then
  pass "all simulated agents connected" "$(a /stats/overview | jq -c '.agents') in $((SECONDS - SIM_START))s"
else fail "all simulated agents connected" "$(a /stats/overview | jq -c '.agents')"; fi
wait_until 120 "[ \"\$(a '/agents/facets?kind=collector' | jq -r '[.configSync[]?|select(.value==\"in_sync\")][0].count // 0')\" = $AGENTS ]"
check_eq "bootstrap: all collectors in_sync" "$AGENTS" "$(a '/agents/facets?kind=collector' | jq -r '[.configSync[]?|select(.value=="in_sync")][0].count // 0')"
check_eq "no unassigned collector" 0 "$(a '/agents?kind=collector&unassigned=true&limit=1' | jq .total)"
check_eq "agents of all 5 fleets" 5 "$(a '/agents/facets?kind=collector' | jq '[.fleets[]|select(.value!="")]|length')"
check_ge "pilot labels from the inventory" 1 "$(a '/agents?attr=labels.pilot%3Dtrue&limit=50' | jq '.items|length')"
check_eq "AgentSummary.site keys carry the site. prefix" true "$(a '/agents?kind=collector&limit=1' | jq -r '.items[0].site|keys|all(startswith("site."))')"
if [ "$AGENTS" = 1000 ]; then
  # pinned by TestE2EProfileDistributionAt1000Agents in internal/sim/profile_test.go (profile e2e)
  if [ "$PROFILE" = e2e ]; then
    check_eq "fleet distribution 375/125/188/187/125" "datacenter:187,edge-be:125,edge-nl:375,k8s-gateway:125,office:188" \
      "$(a '/agents/facets?kind=collector' | jq -r '[.fleets[]|"\(.value):\(.count)"]|sort|join(",")')"
    check_eq "updaters" 687 "$(a /stats/overview | jq -r .agents.updaters)"
  else
    skip "fleet distribution 375/125/188/187/125" "profile $PROFILE: the exact numbers are only pinned for the e2e profile"
  fi
elif [ "$AGENTS" -gt 1000 ]; then
  skip "fleet distribution 375/125/188/187/125" "E2E_AGENTS=$AGENTS: the exact numbers are only pinned for 1000 agents"
  check_eq "fleet counts add up to the agent count" "$AGENTS" "$(a '/agents/facets?kind=collector' | jq -r '[.fleets[]|select(.value!="")|.count]|add')"
fi
LAT=$(curl -s -o /dev/null -b "$CJ" -w '%{time_total}' "$BASE_A/api/v1/agents?limit=200&sort=-lastSeenAt")
if awk "BEGIN{exit !($LAT < 0.2)}"; then pass "GET /agents?limit=200&sort=-lastSeenAt < 200 ms" "${LAT}s"; else fail "agents list latency" "${LAT}s"; fi
check_ge "metrics myriarch_opamp_connections" "$AGENTS" "$(curl -s $BASE_A/metrics | awk '/^myriarch_opamp_connections\{/{s+=$2} END{print int(s)}')"
log "myriarchd RSS: $(ps -o rss= -p "$MYRIARCHD_A_PID" | awk '{printf "%.0f MB", $1/1024}'), CPU $(ps -o pcpu= -p "$MYRIARCHD_A_PID")%"

# ------------------------------------------------ 3. version rollouts (updater) --
section "3. version rollouts via the updater executor (local mirror, real downloads)"
wait_until 60 '[ "$(a "/rollouts?kind=version&limit=50" | jq ".items|length")" -ge 3 ]'
check_eq "version rollouts planned for edge-nl, edge-be, datacenter" "datacenter,edge-be,edge-nl" "$(a '/rollouts?kind=version&limit=50' | jq -r '[.items[].fleetName]|sort|unique|join(",")')"
wait_until 90 '[ "$(a /artifacts | jq "[.items[]|select(.status==\"ready\")]|length")" -ge 3 ]'
check_ge "artifacts ready (SHA-256 verified)" 3 "$(a /artifacts | jq '[.items[]|select(.status=="ready")]|length')"
wait_rollouts version "$STEP_TIMEOUT"; rc=$?
SUMMARY=$(rollouts_summary version)
if [ $rc = 0 ] && [ "$(echo "$SUMMARY" | jq 'map(select(.s=="completed"))|length')" -ge 3 ]; then pass "version rollouts completed (pilot, approval, waves)" "$SUMMARY"; else fail "version rollouts" "rc=$rc $SUMMARY"; fi
check_eq "fleets on target version" 0 "$(a /stats/overview | jq '[.fleets[]|select(.targetVersion!=null and .onTarget!=.total)]|length')"
A_ID=$(a '/artifacts' | jq -r '[.items[]|select(.status=="ready" and .os=="windows")][0].id')
A_FILE=$(a "/artifacts/$A_ID" | jq -r .filename)
check_eq "artifact download without token -> 401" 401 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE_A/api/v1/artifacts/$A_ID/content/$A_FILE")"
check_ge "simulated updaters installed packages (real download + SHA-256)" 1 "$(curl -s 127.0.0.1:$PORT_SIM_MET/metrics | awk '/^myriarch_sim_updates_total\{result="installed"\}/{print int($2)}')"
check_eq "no failed package install" 0 "$(curl -s 127.0.0.1:$PORT_SIM_MET/metrics | awk '/^myriarch_sim_updates_total\{result="failed"\}/{print int($2)}')"
check_ge "artifact content served with a download token (200) in the myriarchd log" 1 "$(grep -c "/content/.* status=200" "$E2E_DIR/myriarchd-a.log")"

# ------------------------------------------------ 4. config rollouts: waves ... --
section "4. config rollout: pilot, approval, waves"
sed -i.bak 's/default: 75/default: 70/' "$GITOPS/blueprints/base.yaml" && rm -f "$GITOPS/blueprints/base.yaml.bak"
ap /gitops/actions/sync >/dev/null
wait_until 60 '[ "$(a "/rollouts?kind=config&limit=50" | jq ".items|length")" -ge 5 ]'
check_eq "one config rollout per fleet" "datacenter,edge-be,edge-nl,k8s-gateway,office" "$(a '/rollouts?kind=config&limit=50' | jq -r '[.items[].fleetName]|sort|unique|join(",")')"
# edge-be has no pilot sites: the first wave is skipped and the 10 % wave waits for approval (10 percent of edge-nl are already covered by its pilot)
wait_until 90 '[ "$(a "/rollouts/$(latest_rollout config edge-be)" | jq -r .status)" = waiting_approval ]' \
  || log "edge-be config rollout: $(rollouts_summary config)"
R=$(latest_rollout config edge-be)
check_eq "edge-be: no pilot sites -> wave 'early' waits for approval" "pilot:skipped,early:waiting_approval" \
  "$(a "/rollouts/$R" | jq -r '[.waves[0:2][]|"\(.name):\(.status)"]|join(",")')"
check_eq "policy applied (edge-careful)" edge-careful "$(a "/rollouts/$R" | jq -r .policyName)"
check_eq "k8s-gateway: first wave 34 percent of the pods, rest needs approval (policy gateway)" "gateway" "$(a "/rollouts/$(latest_rollout config k8s-gateway)" | jq -r .policyName)"
wait_rollouts config "$STEP_TIMEOUT"; rc=$?
SUMMARY=$(rollouts_summary config)
if [ $rc = 0 ] && [ "$(echo "$SUMMARY" | jq 'map(select(.s=="completed"))|length')" -ge 5 ]; then pass "config rollouts completed" "$SUMMARY"; else fail "config rollouts" "rc=$rc $SUMMARY"; fi
wait_until 60 "[ \"\$(a '/agents/facets?kind=collector' | jq -r '[.configSync[]?|select(.value==\"in_sync\")][0].count // 0')\" = $AGENTS ]"
check_eq "after the rollouts: all in_sync (new hash)" "$AGENTS" "$(a '/agents/facets?kind=collector' | jq -r '[.configSync[]?|select(.value=="in_sync")][0].count // 0')"

section "4b. config rollout failure path (office): pause -> rollback -> revert"
OFFICE_FILE="$GITOPS/fleets/office.yaml"
cp "$OFFICE_FILE" "$E2E_DIR/office.yaml.orig"
python3 - "$OFFICE_FILE" <<'EOF'
import pathlib, sys
p = pathlib.Path(sys.argv[1]); s = p.read_text()
n = s.replace('journaldUnits: [sshd.service, nginx.service, postgresql.service]', 'journaldUnits: [sshd.service, simulate-config-failure.service]')
assert n != s; p.write_text(n)
EOF
ap /gitops/actions/sync >/dev/null
wait_until 90 '[ "$(a "/rollouts/$(latest_rollout config office)" | jq -r .status)" = paused ]'
R=$(latest_rollout config office)
check_eq "office rollout paused by the failure threshold" paused "$(a "/rollouts/$R" | jq -r .status)"
check_ge "failed targets" 1 "$(a "/rollouts/$R" | jq .counts.failed)"
check_ge "agents with configSync=failed" 1 "$(a '/agents?fleet=office&configSync=failed&limit=1' | jq .total)"
check_eq "automatic notification: rollout.paused delivered to the mock channel" 1 "$([ "$(grep -c '"event": "rollout.paused"' "$E2E_DIR/mock.log")" -ge 1 ] && echo 1 || echo 0)"
ap "/rollouts/$R/actions/rollback" '{"reason":"bad config (e2e)"}' >/dev/null
wait_until 90 '[ "$(a "/rollouts/'"$R"'" | jq -r .status)" = rolled_back ]'
check_eq "office rollout rolled back" rolled_back "$(a "/rollouts/$R" | jq -r .status)"
check_eq "repo differs from the rolled back state: drift" "$(a '/agents?fleet=office&kind=collector&limit=1' | jq .total)" "$(a '/agents/facets?fleet=office' | jq -r '[.configSync[]?|select(.value=="drift")][0].count // 0')"
cp "$E2E_DIR/office.yaml.orig" "$OFFICE_FILE"
ap /gitops/actions/sync >/dev/null
wait_rollouts config 120; rc=$?
wait_until 60 '[ "$(a "/agents/facets?fleet=office" | jq -r "[.configSync[]?|select(.value==\"drift\" or .value==\"failed\")]|length")" = 0 ]'
check_eq "after reverting the change: no drift/failed in office" 0 "$(a '/agents/facets?fleet=office' | jq -r '[.configSync[]?|select(.value=="drift" or .value=="failed")]|length')"

# --------------------------------------------- 5. webhook + AWX executors (mock) --
section "5. webhook (k8s-gateway) and AWX (office) executors against the mock receiver"
OFFICE_N=$(a '/agents?fleet=office&kind=collector&limit=1' | jq .total); K8S_N=$(a '/agents?fleet=k8s-gateway&kind=collector&limit=1' | jq .total)
sed -i.bak 's/version: 0.162.0/version: 0.163.0/' "$GITOPS/fleets/k8s-gateway.yaml" "$GITOPS/fleets/office.yaml"; rm -f "$GITOPS"/fleets/*.bak
ap /gitops/actions/sync >/dev/null
wait_until 60 '[ "$(a "/rollouts?kind=version&limit=50" | jq "[.items[]|select((.fleetName==\"office\" or .fleetName==\"k8s-gateway\") and .toRef==\"0.163.0\")]|length")" = 2 ]'
check_eq "version rollouts for office (awx) and k8s-gateway (webhook)" "awx,webhook" "$(for f in office k8s-gateway; do a "/rollouts/$(latest_rollout version $f)" | jq -r .executor; done | sort | paste -sd, -)"
wait_rollouts version "$STEP_TIMEOUT"; rc=$?
SUMMARY=$(rollouts_summary version)
if [ $rc = 0 ]; then pass "webhook/AWX version rollouts completed (completion = reported version)" "$SUMMARY"; else fail "webhook/AWX version rollouts" "rc=$rc $SUMMARY"; fi
check_eq "office collectors on 0.163.0" "$OFFICE_N" "$(a '/agents?fleet=office&kind=collector&version=0.163.0&limit=1' | jq .total)"
check_eq "k8s-gateway collectors on 0.163.0" "$K8S_N" "$(a '/agents?fleet=k8s-gateway&kind=collector&version=0.163.0&limit=1' | jq .total)"
check_ge "mock: signed webhook calls (signature_ok=True)" 1 "$(grep -c '^/hook signature_ok=True' "$E2E_DIR/mock.log")"
check_eq "mock: no invalid signature" 0 "$(grep -c 'signature_ok=False' "$E2E_DIR/mock.log")"
check_ge "mock: AWX launches with limit" 1 "$(grep '^/api/v2/job_templates/' "$E2E_DIR/mock.log" | grep -c '"limit"')"

# ------------------------------------------------ 6. release tracking + notify --
section "6. release tracking and notification"
check_eq "channel test" true "$(ap /notifications/channels/mock/actions/test | jq -r .accepted)"
wait_until 20 "grep -q '\"event\": \"test\"' $E2E_DIR/mock.log"
check_eq "test notification arrived signed" 1 "$(grep -c '"event": "test"' "$E2E_DIR/mock.log" | awk '{print ($1>=1)?1:0}')"
python3 - "$E2E_DIR/fixtures" <<'EOF'
import copy, json, pathlib, shutil, sys
base = pathlib.Path(sys.argv[1])
p = base / 'github/repos/open-telemetry/opentelemetry-collector-releases/releases.json'
d = json.load(open(p))
n = json.loads(json.dumps(copy.deepcopy(d[0])).replace('0.162.0', '0.163.0').replace('2026-09-29', '2026-10-06'))
n['id'] += 1
d.insert(0, n); json.dump(d, open(p, 'w'), indent=1)
for repo in ('opentelemetry-collector', 'opentelemetry-collector-contrib'):
    shutil.copytree(base / f'raw/open-telemetry/{repo}/v0.162.0', base / f'raw/open-telemetry/{repo}/v0.163.0')
EOF
ap /releases/actions/refresh >/dev/null
wait_until 60 '[ "$(a /releases/latest | jq -r .release.version)" = 0.163.0 ]'
check_eq "new release detected" 0.163.0 "$(a /releases/latest | jq -r .release.version)"
wait_until 30 "grep -q '\"event\": \"release.new\"' $E2E_DIR/mock.log"
check_eq "release.new notification (signed)" 1 "$(grep '"event": "release.new"' "$E2E_DIR/mock.log" | head -1 | grep -c 'signature_ok=True' | awk '{print ($1>=1)?1:0}')"
check_ge "compare releases" 3 "$(a '/releases/compare?from=0.158.0&to=0.162.0' | jq '.versions|length')"
check_ge "audit log has the actions of the steps above" 8 "$(a '/audit?limit=200' | jq '[.items[].action]|unique|length')"

# ------------------------------------------------------ 7. real supervisor --
# The collector version comes from ci/versions.env. On darwin and linux (amd64, arm64) both binaries are downloaded from
# the upstream release (E2E_REAL_BASE_URL) and verified against the published SHA-256 on EVERY run (scripts/lib/real-binaries.sh).
# E2E_REAL=1 makes an unsupported platform or a failed download or check a FAIL; auto only runs when the binaries exist.
# shellcheck source=scripts/lib/real-binaries.sh
. "$ROOT/scripts/lib/real-binaries.sh"
REAL=${E2E_REAL:-auto}
REAL_FORCED=0; [ "$REAL" = 1 ] && REAL_FORCED=1
REAL_V=$(sed -n 's/^OTELCOL_VERSION=//p' "$ROOT/ci/versions.env" | head -n 1)
REAL_BASE=${E2E_REAL_BASE_URL:-https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download}
REAL_PLAT=$(real_platform) || REAL_PLAT=""
if [ "$REAL" = auto ]; then [ -x "$REAL_DIR/opampsupervisor" ] && [ -x "$REAL_DIR/otelcol-contrib" ] && REAL=1 || REAL=0; fi
if [ "$REAL" = 1 ]; then
  section "7. REAL opampsupervisor + otelcol-contrib ${REAL_V} (${REAL_PLAT:-$(uname -s)/$(uname -m)})"
  mkdir -p "$REAL_DIR"
  if [ -z "$REAL_V" ]; then
    fail "real binaries: version" "OTELCOL_VERSION not found in ci/versions.env"; REAL=0
  elif [ -n "$REAL_PLAT" ]; then
    if real_prepare "$REAL_DIR" "$REAL_V" "$REAL_BASE"; then
      pass "real binaries: downloaded and SHA-256 verified" "$REAL_PLAT, otelcol-contrib and opampsupervisor $REAL_V, files in $REAL_DIR/dl"
    else
      fail "real binaries: download and SHA-256 verification" "see the [real-binaries] lines above (network? E2E_REAL_BASE_URL?)"; REAL=0
    fi
  elif [ "$REAL_FORCED" = 1 ]; then
    fail "real binaries" "no release assets for $(uname -s)/$(uname -m) (supported: darwin and linux on amd64 and arm64)"; REAL=0
  elif [ -x "$REAL_DIR/opampsupervisor" ] && [ -x "$REAL_DIR/otelcol-contrib" ]; then
    log "real binaries: unverified, placed by hand in $REAL_DIR (platform $(uname -s)/$(uname -m) has no release assets)"
  else
    skip "real binaries" "no release assets for $(uname -s)/$(uname -m); put opampsupervisor and otelcol-contrib into $REAL_DIR"; REAL=0
  fi
fi
if [ "$REAL" = 1 ]; then
  check_eq "otelcol-contrib version" "otelcol-contrib version $REAL_V" "$("$REAL_DIR/otelcol-contrib" --version)"
  SUP_DIR="$E2E_DIR/supervisor"; rm -rf "$SUP_DIR" /tmp/myriarch-e2e-storage; mkdir -p "$SUP_DIR/storage" /tmp/myriarch-e2e-storage
  cat > "$SUP_DIR/supervisor.yaml" <<EOF
server:
  endpoint: ws://localhost:$PORT_A/v1/opamp
  headers:
    Authorization: "Bearer $TOKEN"
capabilities:
  accepts_remote_config: true
  accepts_restart_command: true
  reports_effective_config: true
  reports_health: true
  reports_available_components: true
  reports_heartbeat: true
agent:
  executable: $REAL_DIR/otelcol-contrib
  config_apply_timeout: 30s
  bootstrap_timeout: 15s
  validate_config: true
  automatic_config_rollback: true
  description:
    non_identifying_attributes:
      site.type: dev
      site.id: mac-01
      site.country: NL
      site.timezone: Europe/Amsterdam
storage:
  directory: $SUP_DIR/storage
healthcheck:
  endpoint: localhost:13134
EOF
  cat > "$GITOPS/fleets/dev-mac.yaml" <<'EOF'
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata: {name: dev-mac}
spec:
  priority: 10
  selector: {matchAttributes: {site.type: dev}}
  blueprint: base
  parameters:
    gatewayEndpoint: localhost:14317   # no listener: the exporter only retries
    gatewayInsecure: true
    gatewayAuth: false
    storageDir: /tmp/myriarch-e2e-storage
EOF
  ap /gitops/actions/sync >/dev/null
  start_bg sup "$SUP_DIR/supervisor.log" "$REAL_DIR/opampsupervisor" --config "$SUP_DIR/supervisor.yaml" >/dev/null
  wait_until 180 'a "/agents?attr=site.id%3Dmac-01" | jq -e ".items|length>0" >/dev/null'
  MAC=$(a '/agents?attr=site.id%3Dmac-01' | jq -r '.items[0].id')
  wait_until 180 "[ \"\$(a /agents/$MAC | jq -r '.configSync + \"/\" + .agent.remoteConfig.status')\" = in_sync/applied ]"
  check_eq "real collector: configSync / remoteConfig" "in_sync/applied" "$(a /agents/$MAC | jq -r '.configSync + "/" + .agent.remoteConfig.status')"
  check_eq "real collector: version" "$REAL_V" "$(a /agents/$MAC | jq -r .agent.version)"
  check_eq "real collector: healthy" true "$(a /agents/$MAC | jq -r .agent.health.healthy)"
  check_ge "available components (receivers)" 100 "$(a /agents/$MAC/components | jq '.components.receivers|keys|length')"
  check_ge "effective config contains the rendered exporter" 1 "$(a /agents/$MAC/effective-config | jq -r .body | grep -c 'otlp_grpc/gateway')"
  check_ge "effective config contains the injected opamp extension" 1 "$(a /agents/$MAC/effective-config | jq -r .body | grep -c '^    opamp:')"
  check_eq "capabilities reported" true "$(a /agents/$MAC | jq -r '.capabilityNames|index("AcceptsRemoteConfig")!=null and index("ReportsHealth")!=null')"
  ap "/agents/$MAC/actions/restart" >/dev/null
  wait_until 60 "a /agents/$MAC/events | jq -e '[.items[:10][]|select(.type==\"health_changed\" and (.message|test(\"unhealthy\")))]|length>0' >/dev/null"
  wait_until 90 "[ \"\$(a /agents/$MAC | jq -r .agent.health.healthy)\" = true ]"
  check_eq "restart command: recorded, collector restarted and healthy again" "true true" "$(a /agents/$MAC/events | jq -r '[.items[]|select(.type=="command")]|length>0') $(a /agents/$MAC | jq -r .agent.health.healthy)"
  # failed config: batch processor with an invalid size rule -> collector validation fails -> FAILED, no re-offer loop
  cat > "$GITOPS/blueprints/dev-bad.yaml" <<'EOF'
apiVersion: myriarch.dev/v1alpha1
kind: Blueprint
metadata: {name: dev-bad}
spec:
  extends: base
  config:
    processors:
      batch:
        send_batch_size: 2048
        send_batch_max_size: 10
EOF
  sed -i.bak 's/blueprint: base/blueprint: dev-bad/' "$GITOPS/fleets/dev-mac.yaml"; rm -f "$GITOPS/fleets/dev-mac.yaml.bak"
  ap /gitops/actions/sync >/dev/null
  wait_until 120 "[ \"\$(a /agents/$MAC | jq -r .configSync)\" = failed ]"
  check_eq "invalid config: configSync failed (agent rejected it)" failed "$(a /agents/$MAC | jq -r .configSync)"
  check_eq "failedConfigHash is set" true "$(a /agents/$MAC | jq -r '(.agent.assignment.failedConfigHash // "")|length>0')"
  sleep 20
  check_eq "failed config offered exactly once (no loop)" 1 "$(grep -c 'Config validation failed' "$SUP_DIR/supervisor.log")"
  rm "$GITOPS/blueprints/dev-bad.yaml"; sed -i.bak 's/blueprint: dev-bad/blueprint: base/' "$GITOPS/fleets/dev-mac.yaml"; rm -f "$GITOPS/fleets/dev-mac.yaml.bak"
  ap /gitops/actions/sync >/dev/null
  wait_until 120 "[ \"\$(a /agents/$MAC | jq -r .configSync)\" = in_sync ]"
  check_eq "after the fix in Git: paused rollout superseded, collector in_sync again" in_sync "$(a /agents/$MAC | jq -r .configSync)"
  # stop the supervisor (and with it the collector), remove its fleet and the agent before the HA step
  grep '^sup ' "$PIDS_FILE" | while read -r name pid; do kill -TERM "$pid" 2>/dev/null; done
  wait_until 30 "! pgrep -f '$REAL_DIR/otelcol-contrib' >/dev/null"
  rm -f "$GITOPS/fleets/dev-mac.yaml"
  ap /gitops/actions/sync >/dev/null
  sleep 5
  curl -s -o /dev/null -b "$CJ" -H "X-CSRF-Token: $CSRF" -X DELETE "$CURRENT_BASE/api/v1/agents/$MAC"
  # a rollout of a removed fleet fails ("removed from the GitOps repository") and waits for the operator: abort it
  for rid in $(a '/rollouts?active=true&fleet=dev-mac&limit=20' | jq -r '.items[]?|select(.status=="failed")|.id'); do ap "/rollouts/$rid/actions/abort" '{"reason":"fleet removed (e2e)"}' >/dev/null; done
  check_eq "no active rollout left for the removed dev-mac fleet" 0 "$(a '/rollouts?active=true&fleet=dev-mac&limit=20' | jq '.items|length')"
else
  section "7. real supervisor"
  skip "real opampsupervisor + otelcol-contrib" "E2E_REAL=1 downloads and verifies them (into $REAL_DIR); E2E_REAL=0 or no binaries and E2E_REAL=auto skip this step"
fi

# --------------------------------------------------------------------- 8. HA --
if [ "${E2E_HA:-1}" = 1 ]; then
  section "8. high availability: two replicas, one database, failover"
  start_bg myriarchd-b "$E2E_DIR/myriarchd-b.log" ./bin/myriarchd dev --dsn "$DSN" --listen "127.0.0.1:$PORT_B" --replica-id replica-b --gitops-dir "$GITOPS" \
    --data-dir "$E2E_DIR/b" -c "$E2E_DIR/overlay.yaml" --releases-fixtures "$E2E_DIR/fixtures" >/dev/null
  wait_until 60 "curl -sf $BASE_B/readyz"
  B_AGENTS=10
  start_bg sim-b "$E2E_DIR/sim-b.log" ./bin/myriarch-sim run --server "ws://127.0.0.1:$PORT_B/v1/opamp" --token $TOKEN --agents $B_AGENTS --seed 99 \
    --updaters=true --fake-downloads=false --profile "examples/sim/e2e.yaml" --control-listen 127.0.0.1:$PORT_SIMB_CTL --metrics-listen 127.0.0.1:$PORT_SIMB_MET >/dev/null
  wait_until 60 '[ "$(a /system/info | jq "[.replicas[]|select(.id==\"replica-b\")][0].connections // 0")" -ge 10 ]'
  INFO=$(a /system/info)
  check_eq "exactly one leader" 1 "$(echo "$INFO" | jq '[.replicas[]|select(.isLeader)]|length')"
  check_eq "both replicas listed" "replica-a,replica-b" "$(echo "$INFO" | jq -r '[.replicas[].id]|sort|join(",")')"
  # late joiners (older versions) are rolled out; approve everything
  wait_rollouts version "$STEP_TIMEOUT"; rc=$?
  if [ $rc = 0 ]; then pass "late joiners on replica-b (older versions) rolled out" "$(rollouts_summary version | jq -c 'map(select(.s=="completed"))|length') version rollouts completed"; else fail "late joiners version rollouts" "rc=$rc $(rollouts_summary version)"; fi
  wait_rollouts config 120; rc=$?
  if [ $rc = 0 ]; then pass "config rollouts for the late joiners done" "rc=0"; else fail "config rollouts for the late joiners" "rc=$rc $(rollouts_summary config)"; fi
  sim_b_applied() { curl -s 127.0.0.1:$PORT_SIMB_MET/metrics | awk '/^myriarch_sim_configs_applied_total/{print int($2)}'; }
  APPLIED_BEFORE=$(sim_b_applied)
  sed -i.bak 's/default: 70/default: 65/' "$GITOPS/blueprints/base.yaml"; rm -f "$GITOPS/blueprints/base.yaml.bak"
  ap /gitops/actions/sync >/dev/null
  # the config rollouts start asynchronously after the sync: an empty "active" list right away does not mean "done", so
  # wait until the first agents of replica-b have applied the new config (best effort, the checks below decide)
  wait_until 60 '[ "$(sim_b_applied)" -gt "$APPLIED_BEFORE" ]' || log "no agent of replica-b applied the new config within 60s"
  wait_rollouts config "$STEP_TIMEOUT"; rc=$?
  APPLIED_AFTER=$(sim_b_applied)
  if [ $rc != 0 ]; then
    # rc=1 (timeout) and rc=2 (paused/failed): show every unfinished rollout and the targets that are not done
    for rid in $(a '/rollouts?active=true&limit=100' | jq -r '.items[]?|.id'); do
      log "attention: $(a "/rollouts/$rid" | jq -c '{f:.fleetName,k:.kind,s:.status,r:.statusReason}') open targets: $(a "/rollouts/$rid/targets?limit=200" | jq -c '[.items[]|select(.status!="succeeded" and .status!="skipped")|{h:.hostname,s:.status,e:.lastError,at:.attempts,u:.updatedAt}]')"
    done
  fi
  if [ $rc = 0 ] && [ "$APPLIED_AFTER" -gt "$APPLIED_BEFORE" ]; then pass "config change reaches agents on BOTH replicas" "replica-b agents applied $APPLIED_BEFORE -> $APPLIED_AFTER; $(rollouts_summary config | jq -c 'map(select(.s=="completed"))|length') rollouts completed"; else fail "config change reaches agents on both replicas" "rc=$rc applied $APPLIED_BEFORE -> $APPLIED_AFTER $(rollouts_summary config)"; fi
  TOTAL_BEFORE=$(a /stats/overview | jq -r .agents.total)
  PID_A=$(grep '^myriarchd-a ' "$PIDS_FILE" | awk '{print $2}')
  log "kill -9 replica-a (pid $PID_A); embedded PostgreSQL keeps running"
  kill -9 "$PID_A"
  CURRENT_BASE=$BASE_B; login
  KILL_AT=$SECONDS
  if wait_until 60 '[ "$(a /system/info | jq -r .leaderId)" = replica-b ]'; then pass "failover: replica-b became leader" "$((SECONDS - KILL_AT))s after kill -9"; else fail "failover" "leader: $(a /system/info | jq -r .leaderId)"; fi
  check_eq "REST API on replica-b still serves" "$TOTAL_BEFORE" "$(a /stats/overview | jq -r .agents.total)"
  # restart replica A as a client of the same database
  start_bg myriarchd-a2 "$E2E_DIR/myriarchd-a2.log" ./bin/myriarchd dev --dsn "$DSN" --listen "127.0.0.1:$PORT_A" --replica-id replica-a2 --gitops-dir "$GITOPS" \
    --data-dir "$E2E_DIR/a2" -c "$E2E_DIR/overlay.yaml" --releases-fixtures "$E2E_DIR/fixtures" >/dev/null
  wait_until 60 "curl -sf $BASE_A/readyz"
  if wait_until 180 "[ \"\$(a /stats/overview | jq -r '.agents.online')\" = \"$TOTAL_BEFORE\" ]"; then pass "agents of the failed replica reconnected, none counted twice" "total=$TOTAL_BEFORE online=$(a /stats/overview | jq -r .agents.online)"; else fail "reconnect after failover" "$(a /stats/overview | jq -c .agents) expected total $TOTAL_BEFORE"; fi
  check_eq "agent total unchanged by the failover" "$TOTAL_BEFORE" "$(a /stats/overview | jq -r .agents.total)"
  check_eq "one leader after rejoin" 1 "$(a /system/info | jq '[.replicas[]|select(.isLeader)]|length')"
  check_eq "declarative enrollment token still exists exactly once after the restarts" 1 "$(a /enrollment-tokens | jq '[.items[]|select(.name=="e2e-declared")]|length')"
  check_eq "bootstrap admin: the consumed token is refused on replica-b (shared marker)" 401 "$(boot_login $BASE_B "$E2E_DIR/cookies-bootstrap-3")"
  CURRENT_BASE=$BASE_A; login
else
  skip "high availability" "E2E_HA=0"
fi

# ------------------------------------------------------------------ 9. UI --
UI=${E2E_UI:-auto}
BROWSER_BIN=${E2E_BROWSER:-}
if [ -z "$BROWSER_BIN" ]; then
  for c in "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser" "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge" /usr/bin/chromium /usr/bin/google-chrome; do
    [ -x "$c" ] && BROWSER_BIN=$c && break
  done
fi
PW_PATH=${E2E_PLAYWRIGHT_NODE_PATH:-}
if [ -z "$PW_PATH" ]; then for c in "$E2E_DIR/node_modules" "$ROOT/web/node_modules" "$ROOT/node_modules"; do [ -d "$c/playwright-core" ] && PW_PATH=$c && break; done; fi
if [ "$UI" = auto ]; then [ -n "$BROWSER_BIN" ] && [ -n "$PW_PATH" ] && UI=1 || UI=0; fi
if [ "$UI" = 1 ] && { [ -z "$BROWSER_BIN" ] || [ -z "$PW_PATH" ]; }; then
  section "9. UI smoke test"
  fail "UI smoke test" "E2E_UI=1 needs playwright-core (cd web && pnpm install, or E2E_PLAYWRIGHT_NODE_PATH) and a Chromium-based browser (E2E_BROWSER)"
elif [ "$UI" = 1 ]; then
  section "9. UI smoke test (headless browser)"
  if (cd "$ROOT" && NODE_PATH=$PW_PATH BASE=$BASE_A BROWSER=$BROWSER_BIN MIN_AGENTS=$AGENTS node scripts/e2e/ui-smoke.mjs) >"$E2E_DIR/ui-smoke.log" 2>&1; then
    pass "UI smoke test (all pages light+dark, login form, viewer permissions)" "$(grep -c '^ok' "$E2E_DIR/ui-smoke.log") checks, see $E2E_DIR/ui-smoke.log"
  else fail "UI smoke test" "$(tail -5 "$E2E_DIR/ui-smoke.log" | tr '\n' ' ')"; fi
else
  section "9. UI smoke test"
  skip "UI smoke test" "needs playwright-core (cd web && pnpm install, or E2E_PLAYWRIGHT_NODE_PATH) and a Chromium-based browser (E2E_BROWSER)"
fi

# ------------------------------------------- 9b. agent with a declarative token --
section "9b. agent enrolls with the declarative enrollment token"
start_bg sim-declared "$E2E_DIR/sim-declared.log" ./bin/myriarch-sim run --server "ws://127.0.0.1:$PORT_A/v1/opamp" --token "$(cat "$E2E_DIR/enroll.token")" --agents 1 --seed 7 \
  --updaters=false --fake-downloads=false --profile "examples/sim/e2e.yaml" --control-listen 127.0.0.1:$PORT_SIMD_CTL --metrics-listen 127.0.0.1:$PORT_SIMD_MET >/dev/null
if wait_until 60 '[ "$(a "/agents?attr=labels.source%3Ddeclarative&limit=5" | jq ".items|length")" -ge 1 ]'; then
  pass "agent enrolled with the declarative token carries its labels" "labels.source=declarative"
else fail "agent enrolled with the declarative token" "no agent with label source=declarative; see $E2E_DIR/sim-declared.log"; fi
DECL_PID=$(grep '^sim-declared ' "$PIDS_FILE" | awk '{print $2}')
kill -TERM "$DECL_PID" 2>/dev/null

# ------------------------------------------------------------- 10. log checks --
section "10. log checks"
PANICS=$(grep -l -E 'panic:|DATA RACE|fatal error' "$E2E_DIR"/myriarchd-*.log "$E2E_DIR"/sim*.log 2>/dev/null | wc -l | tr -d ' ')
check_eq "no panic/fatal in myriarchd and sim logs" 0 "$PANICS"
ERRS=$(grep -h 'level=ERROR' "$E2E_DIR"/myriarchd-*.log 2>/dev/null | grep -v -E 'connection refused|leader|bus' | wc -l | tr -d ' ')
log "myriarchd ERROR log lines (informational): $ERRS"

# ------------------------------------------------------------------ summary --
section "summary"
PASSN=0; FAILN=0; SKIPN=0
for r in "${RESULTS[@]}"; do case "${r%%|*}" in PASS) PASSN=$((PASSN+1));; FAIL) FAILN=$((FAILN+1));; SKIP) SKIPN=$((SKIPN+1));; esac; done
printf '%d passed, %d failed, %d skipped\n' $PASSN $FAILN $SKIPN >&2
[ $FAILN -gt 0 ] && { for r in "${RESULTS[@]}"; do [ "${r%%|*}" = FAIL ] && echo "  $r" >&2; done; }
printf '%s\n' "${RESULTS[@]}" > "$E2E_DIR/results.txt"
[ $FAILN -eq 0 ]
