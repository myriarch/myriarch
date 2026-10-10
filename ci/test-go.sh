#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Go tests with -race against an external PostgreSQL 17 (service container, PGTEST_DSN) as a NON-root user.
#
#   ci/test-go.sh [go-test-arguments]     default: -race -count=1 ./...
#
# Why non-root: the embedded PostgreSQL (internal/testutil/pgtest) refuses to run as root and downloads its binaries
# from Maven (no internet), hence the service container; as root the file permission tests
# (internal/updater/hardening_test.go) would also behave differently than locally. The job starts as root (image
# default), creates the user "ci" (UID 1000), hands over the project and cache directories and runs the tests
# through setpriv.
#
# Environment: POSTGRES_PASSWORD (password of the service container, CI-internal only), PGTEST_HOST (default
# 127.0.0.1; in a Kubernetes executor services are reachable through localhost), PGTEST_PORT (5432), JUNIT_FILE
# (default reports/junit.xml, only with gotestsum). PGTEST_SKIP is deliberately NOT set.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions
cd "${ROOT}"
need_env POSTGRES_PASSWORD
host="${PGTEST_HOST:-127.0.0.1}"; port="${PGTEST_PORT:-5432}"

# ---- wait for PostgreSQL -----------------------------------------------------------------------------
section_start pgwait "Waiting for PostgreSQL ${host}:${port}"
ready=0
for _ in $(seq 1 60); do
  if (exec 3<>"/dev/tcp/${host}/${port}") 2>/dev/null; then ready=1; break; fi
  sleep 1
done
section_end pgwait
[ "${ready}" = 1 ] || die "PostgreSQL ${host}:${port} is not reachable (postgres service in the job? Kubernetes executor: alias/localhost)"
export PGTEST_DSN="postgres://postgres:${POSTGRES_PASSWORD}@${host}:${port}/postgres?sslmode=disable"
unset PGTEST_SKIP

# ---- tools (installed as root, afterwards readable for ci) ---------------------------------------------
"${CI_DIR}/install-tools.sh" gotestsum
mkdir -p "${ROOT}/reports" "${ROOT}/.cache"
args=("$@"); [ "${#args[@]}" -gt 0 ] || args=(-race -count=1 ./...)

ulimit -n 10240 2>/dev/null || warn "ulimit -n 10240 not possible (limit: $(ulimit -n))"
if [ "$(id -u)" = 0 ]; then
  section_start user "Preparing user ci (UID 1000)"
  have setpriv || die "setpriv is missing (util-linux in the job image)"
  id ci >/dev/null 2>&1 || useradd --uid 1000 --create-home --shell /bin/bash ci
  # The project (tests may write into it), caches and reports belong to ci afterwards; GOCACHE/GOMODCACHE live under .cache
  chown -R 1000:1000 "${ROOT}"
  section_end user
  runner=(setpriv --reuid=1000 --regid=1000 --init-groups --)
  export HOME=/home/ci
else
  log "already non-root (UID $(id -u)): no user switch needed"
  runner=()
fi

junit="${JUNIT_FILE:-reports/junit.xml}"
if have gotestsum; then
  cmd=(gotestsum --junitfile "${junit}" --format dots-v2 -- "${args[@]}")
else
  cmd=(go test "${args[@]}")
fi
log "starting: ${cmd[*]} (as UID $(${runner[@]+"${runner[@]}"} id -u))"
${runner[@]+"${runner[@]}"} env HOME="${HOME}" PATH="${PATH}" "${cmd[@]}"

# Second run with the devpg build tag: the embedded PostgreSQL implementation of internal/devpg (its tests skip under PGTEST_DSN,
# like before). The untagged run above covers the stub. Only for the default invocation; explicit arguments are the caller's choice.
if [ "$#" -eq 0 ]; then
  if have gotestsum; then
    tagged=(gotestsum --junitfile "${junit%.xml}-devpg.xml" --format dots-v2 -- -race -count=1 -tags devpg ./internal/devpg/...)
  else
    tagged=(go test -race -count=1 -tags devpg ./internal/devpg/...)
  fi
  log "starting: ${tagged[*]}"
  ${runner[@]+"${runner[@]}"} env HOME="${HOME}" PATH="${PATH}" "${tagged[@]}"
fi
