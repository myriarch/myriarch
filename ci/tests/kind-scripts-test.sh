#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the scripts for kind that need neither Docker, kind nor a network:
#  - bash 3.2 (the /bin/bash of macOS) treats an empty array as unset: "${ARR[@]}" under `set -u` stops with "unbound variable". The
#    chart installs of hack/kind/quickstart.sh and the Secret helper of hack/kind/lib/install.sh run with an empty array in the
#    commit mode; they run here with fakes for h/kn/k, with /bin/bash when that is bash 3.x and else with the bash of the PATH;
#  - check 7.3 of ci/kind-chart-test.sh compares the content of the old and the new chart (chart_content_state), not the version;
#  - the uninstall steps of ci/kind-chart-test.sh (6.1, 6.3, 6.7, 7.13) wait with the Helm strategy legacy and name what is left.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

QS="${ROOT}/hack/kind/quickstart.sh"
INSTALL_LIB="${ROOT}/hack/kind/lib/install.sh"
COMMON_LIB="${ROOT}/hack/kind/lib/common.sh"

# the bash under test: /bin/bash when it is bash 3.x (the macOS one), else the bash of the PATH
TEST_BASH=bash
if [ -x /bin/bash ] && /bin/bash -c '[ "${BASH_VERSINFO[0]}" = 3 ]' 2>/dev/null; then TEST_BASH=/bin/bash; fi
echo "kind-scripts-test: bash under test: ${TEST_BASH} ($("${TEST_BASH}" -c 'echo "${BASH_VERSION}"'))"

# fn_of FILE NAME: the text of the function NAME of FILE (from "NAME() {" to the closing "}" in column 0)
fn_of() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {p=1} p {print} p && /^}/ {exit}' "$1"; }

harness="${TEST_TMP}/harness.sh"
calls="${TEST_TMP}/calls.log"
{
  echo 'set -euo pipefail'
  echo 'log() { :; }; die() { echo "die: $*" >&2; exit 1; }'
  echo 'h() { printf "%s\n" "$*" >> "${CALLS}"; }'
  echo 'k() { cat >/dev/null; }; kn() { shift; printf "%s\n" "$*" >> "${CALLS}"; }'
  echo 'ensure_namespace() { :; }; secret_path() { echo /dev/null; }; apply_secret() { :; }'
  echo 'collector_values() { echo /dev/null; }; server_chart_version() { echo 9.9.9; }'
  echo 'NS_APP=app; PUBLIC_URL=http://x; SERVER_REPO=r; SERVER_DIGEST=sha256:0; KIND_DIR=/k; CHART_DIR=/c; ROOT=/r; PROBE_WORK='"${TEST_TMP}"
  echo 'QUICKSTART_OCI_BASE=oci://example/charts; QS_GW_RELEASE=gw; QS_NODES_RELEASE=nodes'
  for f in chart_ref chart_version_args install_server install_collectors; do fn_of "${QS}" "$f"; done
  fn_of "${INSTALL_LIB}" apply_secret
} > "${harness}"

# run_case SOURCE FUNCTION: runs the function of the quickstart under set -u with the fakes; the helm calls land in ${calls}
run_case() {
  : > "${calls}"
  CALLS="${calls}" QUICKSTART_SOURCE="$1" "${TEST_BASH}" -c ". '${harness}'; $2"
}
expect_ok "commit mode: the server chart installs with an empty version array" run_case commit install_server
expect_ok "commit mode: the collector charts install with an empty version array" run_case commit install_collectors
expect_ok "commit mode: the server chart and both collector charts reach helm (3 calls), none with --version" \
  "${TEST_BASH}" -c "CALLS='${calls}' QUICKSTART_SOURCE=commit; . '${harness}'; : > '${calls}'; install_server; install_collectors; [ \"\$(grep -c '^upgrade --install' '${calls}')\" = 3 ] && ! grep -q -- '--version' '${calls}'"
expect_ok "release mode: the chart version reaches helm" \
  "${TEST_BASH}" -c "CALLS='${calls}' QUICKSTART_SOURCE=release QUICKSTART_CHART_VERSION=1.2.3; . '${harness}'; : > '${calls}'; install_server; grep -q -- '--version 1.2.3' '${calls}'"
expect_ok "apply_secret without a file (empty array) does not stop under set -u" \
  "${TEST_BASH}" -c "CALLS='${calls}'; . '${harness}'; apply_secret ns name"

# check 7.3: other content under the same version is different (the check passes), equal content is identical (the check fails)
state() { bash -c '. "$1" >/dev/null 2>&1; chart_content_state "$2" "$3"' _ "${COMMON_LIB}" "$1" "$2"; }
old="${TEST_TMP}/old"; new="${TEST_TMP}/new"; same="${TEST_TMP}/same"
mkdir -p "${old}/templates" "${new}/templates" "${same}/templates"
for d in "${old}" "${new}" "${same}"; do printf 'name: myriarch\nversion: 0.1.0\n' > "${d}/Chart.yaml"; printf 'kind: ConfigMap\n' > "${d}/templates/a.yaml"; done
printf 'kind: Secret\n' > "${new}/templates/a.yaml"
if [ "$(state "${old}" "${same}")" = identical ]; then ok "check 7.3: equal content under the same version is identical (the check fails)"; else fail "check 7.3: equal content is identical"; fi
if [ "$(state "${old}" "${new}")" = different ]; then ok "check 7.3: other content under the same version is different (the check passes)"; else fail "check 7.3: other content is different"; fi
cp "${old}/templates/a.yaml" "${new}/templates/a.yaml"; printf 'x\n' > "${new}/templates/extra.yaml"
if [ "$(state "${old}" "${new}")" = different ]; then ok "check 7.3: an added file is different"; else fail "check 7.3: an added file is different"; fi
# diff exits 2 for a missing directory: that is an error, never "different" (fail-open) and never "identical"
if [ "$(state "${old}" "${TEST_TMP}/missing")" = error ]; then ok "check 7.3: a failing diff (missing directory) is an error, so the check fails"; else fail "check 7.3: a failing diff is an error"; fi

# an unreadable file in an existing directory also makes diff exit 2: an error, not "different" (skipped as root, which reads anything)
printf 'secret\n' > "${new}/templates/locked.yaml"; printf 'secret\n' > "${old}/templates/locked.yaml"; chmod 000 "${new}/templates/locked.yaml"
if [ -r "${new}/templates/locked.yaml" ]; then
  echo "  skip  check 7.3: an unreadable file (running as a user that reads anything)"
elif [ "$(state "${old}" "${new}")" = error ]; then ok "check 7.3: diff exit 2 with existing directories (unreadable file) is an error"; else fail "check 7.3: diff exit 2 is an error"; fi
chmod 600 "${new}/templates/locked.yaml"

# hack/kind/diag.sh: the per-container jq of the report must survive a container status without "state" (a pod that is not yet
# scheduled has no state). The filter is taken from the script and run on a status that has none.
filter="$(awk "/^CONTAINERS_JQ='/ {p=1; sub(/^CONTAINERS_JQ='/, \"\")} p {print} p && /'\$/ {exit}" "${ROOT}/hack/kind/diag.sh" | sed "\$ s/'\$//")"
nostate='{"items":[{"metadata":{"name":"p"},"status":{"containerStatuses":[{"name":"c","restartCount":0,"ready":false}],"initContainerStatuses":[{"name":"i","restartCount":1,"ready":false,"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137}}}]}}]}'
if out="$(printf '%s' "${nostate}" | jq -r "${filter}" 2>&1)" && printf '%s' "${out}" | grep -q 'OOMKilled' && printf '%s' "${out}" | grep -q 'now=-'; then ok "diag.sh: a container status without state is reported (now=-)"; else fail "diag.sh: a container status without state is reported" "${out}"; fi

# uninstall_clean of ci/kind-chart-test.sh: the wait strategy legacy, and a resource that stays fails the step and is named
cat > "${TEST_TMP}/uninstall.sh" <<EOS
h() { printf '%s\n' "\$*" >> "\${CALLS}"; return "\${H_RC:-0}"; }
kn() { shift; printf '%s\n' "\${LEFT}"; }
$(fn_of "${ROOT}/ci/kind-chart-test.sh" uninstall_clean)
$(fn_of "${ROOT}/ci/kind-chart-test.sh" leftovers)
EOS
run_un() { : > "${calls}"; CALLS="${calls}" H_RC="$1" LEFT="$2" "${TEST_BASH}" -c ". '${TEST_TMP}/uninstall.sh'; uninstall_clean ns rel"; }
out="$(run_un 0 '')"; rc=$?
if [ "${rc}" = 0 ] && [ "$(cat "${calls}")" = 'uninstall rel -n ns --wait=legacy --cascade foreground --timeout 5m' ]; then ok "uninstall_clean: helm uninstall --wait=legacy (not the watcher), status 0"; else fail "uninstall_clean: the helm call" "$(cat "${calls}")"; fi
out="$(run_un 1 'deployment.apps/rel')"; rc=$?
if [ "${rc}" != 0 ] && [ "${out}" = 'left of the release: deployment.apps/rel ' ]; then ok "uninstall_clean: a resource that stays fails the step and is named"; else fail "uninstall_clean: failure case" "rc=${rc} ${out}"; fi
out="$(run_un 1 'configmap/kube-root-ca.crt')"
if [ "${out}" = 'left of the release: ' ]; then ok "uninstall_clean: configmap/kube-root-ca.crt is never reported as left"; else fail "uninstall_clean: kube-root-ca.crt" "${out}"; fi

finish_tests "kind-scripts-test"
