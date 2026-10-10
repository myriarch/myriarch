#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the secret scan ci/secrets-scan.sh: a token-shaped value outside the allowlist fails the gate, the allowlist
# is limited to its file AND rule, and the current tree passes. The token-like values are assembled at run time so that
# they are not part of this file.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd git tar
export PATH="${TOOLS_BIN}:${PATH}"
have gitleaks || { echo "secrets-scan-test: gitleaks not found: run 'ci/install-tools.sh gitleaks'" >&2; exit 1; }
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

generic="secret_token = \"Zk3pQ9vL2x""R7mT5wY8nB4cD6\""     # rule generic-api-key
aws="AKIA""QYLPMN5HHHFPZAM3"                               # rule aws-access-token

newrepo() { # name
  local d="${TEST_TMP}/$1"
  git init -q -b main "${d}"
  cp "${ROOT}/.gitleaks.toml" "${d}/.gitleaks.toml"
  mkdir -p "${d}/internal/updater" "${d}/docs"
  echo "nothing to see" > "${d}/docs/readme.md"
  printf '%s' "${d}"
}
commit() { git -C "$1" add -A && git -C "$1" commit -q -m test; }
scan() { "${CI_DIR}/secrets-scan.sh" "$1"; }

r="$(newrepo clean)"; commit "${r}"
expect_ok   "a repository without secrets passes" scan "${r}"

r="$(newrepo leak)"; printf '%s\n' "${generic}" > "${r}/docs/notes.txt"; commit "${r}"
expect_fail "a token-shaped value outside the allowlist fails" 'generic-api-key' scan "${r}"

r="$(newrepo allowed)"; printf '%s\n' "${generic}" > "${r}/internal/updater/deploy_test.go"; commit "${r}"
expect_ok   "the same value in an allowlisted file passes (file and rule match)" scan "${r}"

r="$(newrepo otherrule)"; printf '%s\n' "${aws}" > "${r}/internal/updater/deploy_test.go"; commit "${r}"
expect_fail "another rule in an allowlisted file still fails" 'aws-access-token' scan "${r}"

r="$(newrepo uncommitted)"; commit "${r}"; printf '%s\n' "${generic}" > "${r}/docs/later.txt"
expect_ok   "the scan covers the committed state" scan "${r}"
expect_fail "SECRETS_SCAN_WORKTREE=1 covers the work tree" 'generic-api-key' env SECRETS_SCAN_WORKTREE=1 "${CI_DIR}/secrets-scan.sh" "${r}"

# SECRETS_SCAN_RANGE: the commits of a range, including a secret that a later commit removed again
r="$(newrepo range)"; commit "${r}"; base="$(git -C "${r}" rev-parse HEAD)"
printf '%s\n' "${generic}" > "${r}/docs/notes.txt"; commit "${r}"; mid="$(git -C "${r}" rev-parse HEAD)"
git -C "${r}" rm -q docs/notes.txt; commit "${r}"; head="$(git -C "${r}" rev-parse HEAD)"
echo "more text" >> "${r}/docs/readme.md"; commit "${r}"; tip="$(git -C "${r}" rev-parse HEAD)"
rscan() { env SECRETS_SCAN_RANGE="$2" "${CI_DIR}/secrets-scan.sh" "$1"; }
expect_ok   "the committed state is clean although the history is not (no range)" scan "${r}"
expect_fail "a range that contains a secret fails although a later commit removed it" 'generic-api-key' rscan "${r}" "${base}..${head}"
expect_fail "the finding of a range is redacted" 'REDACTED' rscan "${r}" "${base}..${mid}"
expect_ok   "a range after the secret and its removal passes" rscan "${r}" "${head}..${tip}"
expect_ok   "an empty range passes" rscan "${r}" "${tip}..${tip}"
expect_fail "a range with an option is refused" 'SECRETS_SCAN_RANGE must be' rscan "${r}" "--all"
expect_fail "a range with a branch name is refused" 'SECRETS_SCAN_RANGE must be' rscan "${r}" "main..main"

expect_ok   "the current tree passes" scan "${ROOT}"

finish_tests "secrets-scan-test"
