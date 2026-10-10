#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intentional (code for bash -c)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Regression test for ci/verify-testenv.sh: verify.sh rejects an output directory inside the repository; the wrapper
# must therefore work outside the checkout (default) and copy the result to ci-out/verify-testenv on failure.
# Uses stand-ins for helm/yq/kustomize (pre-stamped, no network) and a stand-in for verify.sh.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

tools="${TEST_TMP}/tools"; mkdir -p "${tools}"
for t in helm:HELM yq:YQ kustomize:KUSTOMIZE; do
  name="${t%%:*}"; var="${t##*:}_VERSION"
  printf '#!/bin/sh\necho %s\n' "${name}" > "${tools}/${name}"; chmod +x "${tools}/${name}"
  : > "${tools}/$(go_tool_stamp "${name}" "${!var}")"   # the stamp of the current go.mod and go.sum (see install-tools-test.sh)
done
printf '#!/bin/sh\necho "myriarchd test"\n' > "${TEST_TMP}/myriarchd"; chmod +x "${TEST_TMP}/myriarchd"

cat > "${TEST_TMP}/verify-fake.sh" <<'FAKE'
#!/usr/bin/env bash
# Stand-in for deploy/examples/production/verify.sh: same rule for VERIFY_OUT, result depends on FAKE_RESULT
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "${VERIFY_ROOT:-}" in "") ;; *) root="${VERIFY_ROOT}" ;; esac
out="$(cd "${VERIFY_OUT}" && pwd)"
case "${out}" in "${root}"|"${root}"/*) echo "VERIFY_OUT must be outside the repository (${root})" >&2; exit 2 ;; esac
echo detail > "${out}/report.txt"
[ "${FAKE_RESULT:-ok}" = ok ]
FAKE

run() { # extra env...; runs the wrapper with the stand-ins
  env TOOLS_BIN="${tools}" MYRIARCHD_BIN="${TEST_TMP}/myriarchd" VERIFY_SCRIPT="${TEST_TMP}/verify-fake.sh" \
    VERIFY_ROOT="${ROOT}" CI_OUT_DIR="${TEST_TMP}/ci-out" "$@" "${CI_DIR}/verify-testenv.sh"
}

expect_ok "the default output directory is outside the repository" run FAKE_RESULT=ok
[ ! -e "${TEST_TMP}/ci-out/verify-testenv" ] && ok "success: no artifact" || fail "success creates ci-out/verify-testenv"
expect_fail "a failure of verify.sh fails the job" "." run FAKE_RESULT=fail
[ -f "${TEST_TMP}/ci-out/verify-testenv/report.txt" ] && ok "failure: the result is copied to ci-out/verify-testenv" || fail "ci-out/verify-testenv/report.txt is missing"
# An explicit VERIFY_OUT inside the repository is a configuration error and must fail loudly, not succeed silently
expect_fail "VERIFY_OUT inside the repository is rejected" "outside the repository" run FAKE_RESULT=ok VERIFY_OUT="${ROOT}/ci-out/should-not-exist"
rm -rf "${ROOT}/ci-out/should-not-exist"
finish_tests "verify-testenv-test"
