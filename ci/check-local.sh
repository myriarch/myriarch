#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Local gate runner: runs every check that CI runs (and the end-to-end test) on this machine, one log per gate.
#
#   ci/check-local.sh                       all gates
#   SKIP=e2e,go-test ci/check-local.sh      all gates except these
#   ONLY=web,helm ci/check-local.sh         only these gates (ONLY is applied first, then SKIP removes)
#   ci/check-local.sh --list                list the gates
#
# Gates (in this order): go go-test web build examples shellcheck workflows helm language docs website licenses vulncheck secrets e2e
#
# Environment:
#   SKIP, ONLY               comma-separated gate names
#   LANGUAGE_GUARD           strict (default) | warn | off - how a finding of scripts/check-language.sh in the strict
#                            scope counts: strict = the gate fails, warn = the gate warns, off = the gate is skipped
#   LANGUAGE_GUARD_PATHS     space-separated pathspecs of the strict scope (default: the code, scripts, deploy files,
#                            examples, docs/, .github/ and the root Markdown files; the default excludes of
#                            scripts/check-language.exclude apply)
#   GO_TEST_RACE             go-test: 1 (default) runs go test with -race, 0 without (Windows CI)
#   GO_TEST_DEVPG            go-test: 1 (default) also runs the devpg tests (embedded PostgreSQL), 0 leaves them out
#   GO_TEST_PERF             go-test: 1 (default) also runs the render budget test (5000 agents in under 5 s) alone and without
#                            -race with PERFTEST=1, 0 leaves it out (Windows CI); the other runs never check the budget
#   HELM_KUBERNETES_VERSIONS helm: Kubernetes versions of the kubeconform schemas (default: 1.30.0 1.34.12)
#   CHECK_LOCAL_STREAM       1 streams every gate to the console in a ::group:: block (the default when GITHUB_ACTIONS=true,
#                            the log file is written as well), 0 writes the log file only (the default elsewhere)
#   E2E_*                    passed through to scripts/e2e.sh
#
# Logs: .cache/check-local/<gate>.log, summary: .cache/check-local/summary.txt. Missing tools: `make tools`.
# No global `set -e`: every gate runs in its own subshell with `set -euo pipefail`, the runner always continues.
set -uo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

GATES="go go-test web build examples shellcheck workflows helm language docs website licenses vulncheck secrets e2e"
OUT_REL=".cache/check-local"

usage() {
  cat <<USAGE
usage: ci/check-local.sh [--list] [-h]
  SKIP=g1,g2   skip gates          ONLY=g1,g2   run only these gates
  LANGUAGE_GUARD=strict|warn|off   LANGUAGE_GUARD_PATHS="pathspec ..."
  GO_TEST_RACE=0|1  GO_TEST_DEVPG=0|1  GO_TEST_PERF=0|1  HELM_KUBERNETES_VERSIONS="1.30.0 ..."
  CHECK_LOCAL_STREAM=0|1           stream gates as ::group:: blocks (default 1 in GitHub Actions)
gates: ${GATES}
USAGE
}

case "${1:-}" in
  "") ;;
  --list) printf '%s\n' ${GATES}; exit 0 ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
[ "$#" -le 1 ] || { usage >&2; exit 2; }

cd "${ROOT}"

# ---- selection -----------------------------------------------------------------------------------------------
is_gate() { # name
  local g
  for g in ${GATES}; do [ "${g}" = "$1" ] && return 0; done
  return 1
}
in_list() { # name comma-list
  local item items=()
  IFS=, read -r -a items <<< "$2"
  for item in ${items[@]+"${items[@]}"}; do
    item="${item# }"; item="${item% }"
    [ "${item}" = "$1" ] && return 0
  done
  return 1
}
check_names() { # comma-list
  local item items=()
  IFS=, read -r -a items <<< "$1"
  for item in ${items[@]+"${items[@]}"}; do
    item="${item# }"; item="${item% }"
    [ -z "${item}" ] && continue
    is_gate "${item}" || { echo "check-local: unknown gate '${item}' (gates: ${GATES})" >&2; exit 2; }
  done
}
SKIP_LIST="${SKIP:-}"
ONLY_LIST="${ONLY:-}"
check_names "${SKIP_LIST}"
check_names "${ONLY_LIST}"
# In GitHub Actions every gate streams into a log group (the console is the log); elsewhere the log file is all there is.
if [ -n "${CHECK_LOCAL_STREAM:-}" ]; then
  STREAM="${CHECK_LOCAL_STREAM}"
elif [ "${GITHUB_ACTIONS:-}" = true ]; then
  STREAM=1
else
  STREAM=0
fi
case "${STREAM}" in 0|1) ;; *) echo "check-local: CHECK_LOCAL_STREAM must be 0 or 1 (got '${STREAM}')" >&2; exit 2 ;; esac
GUARD="${LANGUAGE_GUARD:-strict}"
case "${GUARD}" in warn|strict|off) ;; *) echo "check-local: LANGUAGE_GUARD must be strict, warn or off (got '${GUARD}')" >&2; exit 2 ;; esac
# Scope of the strict language run: English is the project language (ADR-0020) in the code, scripts, deploy files,
# examples, the documentation (docs/, .github/) and the root Markdown files.
LANGUAGE_STRICT_PATHS="cmd internal scripts ci contrib deploy examples hack web/src docs .github README.md CHANGELOG.md CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md MAINTAINERS.md GOVERNANCE.md CONTRIBUTOR_LADDER.md ADOPTERS.md ROADMAP.md AGENTS.md CLAUDE.md website"

# ---- gates ---------------------------------------------------------------------------------------------------
# Each gate_<name> runs in a subshell with `set -euo pipefail`; a non-zero exit status fails it.
gate_go() {
  go build ./...
  go build -tags devpg ./...
  # the host updater and its tests also build for Windows (it runs as SYSTEM there): vet everything, compile the tests
  GOOS=windows GOARCH=amd64 go vet ./...
  # the devpg tag on Windows compiles internal/devpg/lock_other.go (devpg && !unix), which CodeQL analyses too
  GOOS=windows GOARCH=amd64 go vet -tags devpg ./...
  GOOS=windows GOARCH=amd64 go test -c -o /dev/null ./internal/updater
  GOOS=darwin GOARCH=arm64 go vet ./...
  ci/lint-go.sh
  ci/check-versions.sh
  ci/check-release-deps.sh
  ci/install-tools.sh go-licenses
  ci/check-release-licenses.sh
}

gate_go_test() {
  ulimit -n 10240 2>/dev/null || ulimit -n "$(ulimit -Hn)" 2>/dev/null || true
  local race="-race"
  [ "${GO_TEST_RACE:-1}" = 0 ] && race=""
  # A wall-clock budget needs an idle machine and go test ./... runs packages in parallel: only the last run below asserts
  # the render budget (TestRenderFleet5000UnderFiveSeconds, PERFTEST=1), so a PERFTEST of the caller must not reach the others.
  unset PERFTEST
  # untagged: covers the devpg stub and TestDevCommandAgainstExternalPostgres (myriarchd dev --dsn without the tag)
  # shellcheck disable=SC2086  # race is empty or one flag
  go test ${race} -count=1 ./...
  # shellcheck disable=SC2086
  if [ "${GO_TEST_DEVPG:-1}" != 0 ]; then go test ${race} -count=1 -tags devpg ./internal/devpg/...; fi
  # the budget test alone and without -race (the race detector slows the renderer down by 5-10x); -v logs the measured time
  if [ "${GO_TEST_PERF:-1}" != 0 ]; then
    echo "== go-test: render budget of internal/blueprint (PERFTEST=1, run alone, without -race)"
    PERFTEST=1 go test -count=1 -v -run '^TestRenderFleet5000UnderFiveSeconds$' ./internal/blueprint
  fi
}

# Not ci/test-web.sh: it may run corepack or a global npm install, which changes the system.
gate_web() {
  export CI=true
  tmp="$(mktemp)"   # global on purpose: the EXIT trap runs after the function returned
  trap 'rm -f "${tmp}"' EXIT
  cd web
  pnpm install --frozen-lockfile
  cp src/api/schema.d.ts "${tmp}"
  pnpm gen:api
  if ! cmp -s "${tmp}" src/api/schema.d.ts; then
    diff -u "${tmp}" src/api/schema.d.ts | head -n 60 || true
    echo "web/src/api/schema.d.ts is out of date: run 'pnpm gen:api' and commit" >&2
    exit 1
  fi
  pnpm typecheck
  pnpm lint
  pnpm format:check
  pnpm test
  pnpm build
  cd "${ROOT}"
  test -f internal/webui/dist/index.html || { echo "UI build did not produce internal/webui/dist/index.html" >&2; exit 1; }
}

gate_build() {
  make build
}

gate_examples() {
  make validate-examples
}

gate_shellcheck() {
  have shellcheck || { echo "shellcheck not found: run 'make tools'" >&2; exit 1; }
  ci/lint-shell.sh
}

# The chart is checked against the schemas of its floor (kubeVersion >=1.30.0) and of a recent Kubernetes release (1.34.12 is
# the newest standalone-strict schema set that kubeconform fetches by default); HELM_KUBERNETES_VERSIONS overrides the list.
gate_helm() {
  local v
  ci/lint-helm.sh
  for v in ${HELM_KUBERNETES_VERSIONS:-1.30.0 1.34.12}; do
    echo "== helm-conform: Kubernetes ${v}"
    KUBERNETES_VERSION="${v}" ci/helm-conform.sh --out "${OUT_REL}/helm/${v}"
  done
}

# GitHub Actions workflows: actionlint (with ShellCheck over the run: blocks), zizmor (security audits, offline unless
# ZIZMOR_ONLINE=1) and the policy check ci/check-workflows.py (pinned actions, read-only token, forbidden triggers, gate parity
# with ci.yml). No network after the tools are installed.
gate_workflows() {
  ci/install-tools.sh actionlint shellcheck zizmor
  ci/lint-workflows.sh
}

# Exit codes of this gate are mapped by the runner: 90 = SKIP (guard missing), 91 = WARN, other = FAIL.
gate_language() {
  [ -x scripts/check-language.sh ] || { echo "scripts/check-language.sh is missing or not executable" >&2; exit 90; }
  local rc=0
  # The strict run; its summary line is the last line of the log, so the runner's note shows it.
  # shellcheck disable=SC2086
  scripts/check-language.sh --untracked -- ${LANGUAGE_GUARD_PATHS:-${LANGUAGE_STRICT_PATHS}} || rc=$?
  case "${rc}" in
    0) exit 0 ;;
    1) if [ "${GUARD}" = warn ]; then exit 91; else exit 1; fi ;;
    *) exit "${rc}" ;;
  esac
}

# Documentation checks: internal links and anchors, front matter, drift of the generated command line reference.
gate_docs() {
  python3 -I scripts/check-docs.py links
  python3 -I scripts/check-docs.py front-matter
  ci/check-docs-gen.sh
}

# The website (ADR-0031): build with --panicOnWarning, `hugo mod verify`, lychee over the built pages (offline, with fragments), markdownlint,
# the front matter of the site content, the checks of website/scripts (resource URLs, local paths, overrides, placeholders, notices) and the
# unit tests of those checks. The first run needs network access (Go proxy, npm registry, release downloads); later runs use the cache.
gate_website() {
  ci/website.sh check
}

# License compliance: the SPDX header of every source file (scripts/license-headers.py), REUSE 3.3 (ci/reuse-lint.sh with
# REUSE.toml and LICENSES/) and the generated THIRD_PARTY_NOTICES. The notices generator reads the dependency tree of the
# web UI, so the UI dependencies are installed first (the same frozen install as in the web gate).
gate_licenses() {
  ci/install-tools.sh reuse go-licenses
  python3 -I scripts/license-headers.py --check
  ci/reuse-lint.sh
  (cd web && CI=true pnpm install --frozen-lockfile)
  scripts/gen-third-party-notices.sh --check
}

# Vulnerabilities in the Go standard library and the modules that the code calls (ci/vulncheck.sh, allowlist
# ci/vulncheck-allowlist.txt). It reads the vulnerability database from vuln.go.dev: offline, skip it with SKIP=vulncheck.
gate_vulncheck() {
  ci/install-tools.sh govulncheck
  ci/vulncheck.sh
}

# Secrets in the committed files (gitleaks, .gitleaks.toml); the work tree variant is `SECRETS_SCAN_WORKTREE=1 ci/secrets-scan.sh`.
gate_secrets() {
  ci/install-tools.sh gitleaks
  ci/secrets-scan.sh
}

gate_e2e() {
  NO_BUILD=1 scripts/e2e.sh
}

# ---- state ---------------------------------------------------------------------------------------------------
# Parallel arrays indexed by the position in GATES (bash 3.2: no associative arrays).
ST=(); DUR=(); NOTE=()
idx_of() { # gate -> index
  local i=0 g
  for g in ${GATES}; do
    [ "${g}" = "$1" ] && { echo "${i}"; return 0; }
    i=$((i + 1))
  done
  return 1
}
set_result() { # gate status duration note
  local i
  i="$(idx_of "$1")"
  ST[i]="$2"; DUR[i]="$3"; NOTE[i]="$4"
}
status_of() { local i; i="$(idx_of "$1")"; echo "${ST[i]:-}"; }

fmt_dur() { # seconds
  if [ "$1" -lt 60 ]; then printf '%ds' "$1"; else printf '%dm%02ds' $(($1 / 60)) $(($1 % 60)); fi
}

last_line() { # logfile
  grep -v '^[[:space:]]*$' "$1" 2>/dev/null | tail -n 1
}

print_summary() {
  local g i n_pass=0 n_fail=0 n_warn=0 n_skip=0 st log
  printf '%-12s %-7s %-9s %-34s %s\n' GATE STATUS DURATION LOG NOTE
  for g in ${GATES}; do
    i="$(idx_of "${g}")"
    st="${ST[i]:-}"
    [ -n "${st}" ] || continue
    log="${OUT_REL}/${g}.log"
    [ "${st}" = SKIP ] && log="-"
    printf '%-12s %-7s %-9s %-34s %s\n' "${g}" "${st}" "${DUR[i]:--}" "${log}" "${NOTE[i]:-}"
    case "${st}" in PASS) n_pass=$((n_pass + 1)) ;; FAIL) n_fail=$((n_fail + 1)) ;; WARN) n_warn=$((n_warn + 1)) ;; SKIP) n_skip=$((n_skip + 1)) ;; esac
  done
  echo
  printf 'check-local: %d passed, %d failed, %d warned, %d skipped (total %s)\n' \
    "${n_pass}" "${n_fail}" "${n_warn}" "${n_skip}" "$(fmt_dur $((SECONDS - T0)))"
}

finish() {
  local g i failed=0
  echo
  print_summary | tee "${OUT_REL}/summary.txt"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -w "${GITHUB_STEP_SUMMARY}" ]; then
    { echo '```'; cat "${OUT_REL}/summary.txt"; echo '```'; } >> "${GITHUB_STEP_SUMMARY}"
  fi
  for g in ${GATES}; do
    i="$(idx_of "${g}")"
    [ "${ST[i]:-}" = FAIL ] && failed=1
  done
  exit "${failed}"
}

CURRENT=""; CUR_START=0
on_int() {
  echo
  if [ -n "${CURRENT}" ]; then set_result "${CURRENT}" FAIL "$(fmt_dur $((SECONDS - CUR_START)))" interrupted; fi
  print_summary | tee "${OUT_REL}/summary.txt"
  exit 130
}

# ---- header --------------------------------------------------------------------------------------------------
pin_of() { # VAR_NAME - the version pinned in ci/versions.env, or empty
  sed -n "s/^$1=//p" "${CI_DIR}/versions.env" | head -n 1
}
ver() { # label command [args...] - prints the first line of the version output or "missing"
  # With PIN=<VAR> in the environment of the call, the version pinned in ci/versions.env is shown next to it.
  local label="$1" v pin=""; shift
  if have "$1" && v="$("$@" 2>/dev/null | head -n 1)" && [ -n "${v}" ]; then :; else v=missing; fi
  if [ -n "${PIN:-}" ] && [ "${v}" != missing ]; then pin="$(pin_of "${PIN}")"; fi
  printf '  %-12s %s%s\n' "${label}" "${v}" "${pin:+ (pinned ${pin})}"
}

rm -rf "${OUT_REL}" && mkdir -p "${OUT_REL}"
T0=${SECONDS}
dirty=""
if have git && [ -n "$(git status --porcelain 2>/dev/null | head -n 1)" ]; then dirty=" (dirty)"; fi
echo "check-local: $(date '+%Y-%m-%d %H:%M:%S') git $(git rev-parse --short HEAD 2>/dev/null || echo unknown)${dirty}"
ver go go version
ver node node --version
ver pnpm pnpm --version
PIN=HELM_VERSION ver helm helm version --short
PIN=KUBECONFORM_VERSION ver kubeconform kubeconform -v
PIN=CRANE_VERSION ver crane crane version
PIN=KIND_VERSION ver kind kind version
PIN=KUBECTL_VERSION ver kubectl kubectl version --client
PIN=REUSE_VERSION ver reuse reuse --version
PIN=HUGO_VERSION ver hugo hugo version
PIN=ACTIONLINT_VERSION ver actionlint actionlint -version
if have shellcheck; then printf '  %-12s %s\n' shellcheck "$(shellcheck --version | sed -n 's/^version: //p')"; else ver shellcheck shellcheck --version; fi
echo

trap on_int INT

# ---- run -----------------------------------------------------------------------------------------------------
for g in ${GATES}; do
  skip_note=""
  if [ -n "${ONLY_LIST}" ] && ! in_list "${g}" "${ONLY_LIST}"; then
    skip_note="ONLY=${ONLY_LIST}"
  elif [ -n "${SKIP_LIST}" ] && in_list "${g}" "${SKIP_LIST}"; then
    skip_note="SKIP=${SKIP_LIST}"
  elif [ "${g}" = language ] && [ "${GUARD}" = off ]; then
    skip_note="LANGUAGE_GUARD=off"
  elif [ "${g}" = e2e ] && [ "$(status_of build)" = FAIL ]; then
    skip_note="build failed"
  fi
  if [ -n "${skip_note}" ]; then
    set_result "${g}" SKIP "-" "${skip_note}"
    continue
  fi

  fn="gate_${g//-/_}"
  log="${OUT_REL}/${g}.log"
  echo "==> ${g}"
  CURRENT="${g}"; CUR_START=${SECONDS}
  if [ "${STREAM}" = 1 ]; then
    echo "::group::${g}"
    ( set -euo pipefail; "${fn}" ) 2>&1 | tee "${log}"
    rc=${PIPESTATUS[0]}
    echo "::endgroup::"
  else
    ( set -euo pipefail; "${fn}" ) >"${log}" 2>&1
    rc=$?
  fi
  d="$(fmt_dur $((SECONDS - CUR_START)))"
  CURRENT=""
  case "${rc}" in
    0) st=PASS; note="" ;;
    90) if [ "${g}" = language ]; then st=SKIP; note="$(last_line "${log}")"; else st=FAIL; note="exit ${rc}"; fi ;;
    91) if [ "${g}" = language ]; then st=WARN; note="$(last_line "${log}")"; else st=FAIL; note="exit ${rc}"; fi ;;
    *)  st=FAIL; note="exit ${rc}"
        # a failed language gate reports the guard's last summary line
        if [ "${g}" = language ]; then note="$(last_line "${log}")"; fi ;;
  esac
  set_result "${g}" "${st}" "${d}" "${note}"
  echo "<- ${g} ${st} (${d})"
  if [ "${st}" = FAIL ]; then
    if [ "${STREAM}" = 1 ]; then
      echo "::error title=gate ${g}::${note}"
    else
      tail -n 25 "${log}" | sed 's/^/    /'
    fi
  fi
done

finish
