#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the zizmor step of ci/lint-workflows.sh with the real zizmor (ci/install-tools.sh zizmor): a finding fails the gate (also
# one in a local action, one in the Dependabot configuration and one that only the pedantic persona reports), so does an input that
# zizmor cannot parse, an ignore comment or an entry of zizmor.yml for exactly that rule lets a finding pass, the environment
# cannot replace the configuration (a stub of zizmor records the options and the variables that reach it), the online mode needs a
# token, and the workflows of this repository pass. actionlint and ShellCheck are stubs (they are tested by their own gate), the
# policy check is the real one. No network, no ports: zizmor runs with --offline.
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
export PATH="${TOOLS_BIN}:${PATH}"
have zizmor || { echo "lint-workflows-test: zizmor not found: run 'ci/install-tools.sh zizmor'" >&2; exit 1; }
export CHECK_WORKFLOWS_GATES="go"

STUBS="${TEST_TMP}/stubs"
mkdir -p "${STUBS}"
cat > "${STUBS}/actionlint" <<'STUB'
#!/bin/sh
if [ "$1" = -version ]; then echo "actionlint stub"; fi
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' > "${STUBS}/shellcheck"
chmod +x "${STUBS}/actionlint" "${STUBS}/shellcheck"
ln -s "$(command -v zizmor)" "${STUBS}/zizmor"

# newtree <name>: a small repository tree with a clean workflow, an empty zizmor.yml and the lint scripts; prints its path
newtree() {
  local d="${TEST_TMP}/$1"
  mkdir -p "${d}/ci" "${d}/.github/workflows"
  cp "${CI_DIR}/lib.sh" "${CI_DIR}/lint-workflows.sh" "${CI_DIR}/check-workflows.py" "${d}/ci/"
  : > "${d}/ci/workflow-permissions.txt"
  : > "${d}/ci/workflow-secrets.txt"
  printf 'rules: {}\n' > "${d}/zizmor.yml"
  cat > "${d}/.github/workflows/ci.yml" <<'YAML'
name: ci
on:
  push:
    branches: [main]
permissions:
  contents: read
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
jobs:
  go:
    name: go
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - name: Check out the repository
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - name: Run the gate
        env:
          ONLY: go
        run: ci/check-local.sh
YAML
  printf '%s' "${d}"
}
lint() { (cd "$1" && TOOLS_BIN="${STUBS}" PATH="${STUBS}:${PATH}" bash ci/lint-workflows.sh); }
# A finding of zizmor that the policy check does not know: the output of an earlier step is expanded into a script
inject() { # tree: appends two steps
  cat >> "$1/.github/workflows/ci.yml" <<'YAML'
      - name: Produce a value
        id: v
        run: echo "ver=1" >> "${GITHUB_OUTPUT}"
      - name: Use the value
        run: echo "${{ steps.v.outputs.ver }}"
YAML
}

t="$(newtree clean)"
expect_ok   "a clean workflow passes (zizmor offline, policy check)" lint "${t}"

t="$(newtree finding)"; inject "${t}"
expect_fail "a finding of zizmor fails the gate" 'template-injection' lint "${t}"
if ! lint "${t}" 2>&1 | grep -q 'policy check'; then ok "the gate stops at zizmor, before the policy check"; else fail "the gate went on to the policy check"; fi
expect_ok   "the same workflow passes the policy check on its own" python3 -I "${t}/ci/check-workflows.py" --root "${t}"

t="$(newtree ignore-comment)"; inject "${t}"
sed -i.bak 's|^\(        run: echo "\${{ steps.v.outputs.ver }}"\)$|\1 # zizmor: ignore[template-injection] the value is a constant of this test|' "${t}/.github/workflows/ci.yml"; rm -f "${t}/.github/workflows/ci.yml.bak"
expect_ok   "an ignore comment for the rule lets the finding pass" lint "${t}"

t="$(newtree ignore-config)"; inject "${t}"
printf 'rules:\n  # the value is a constant of this test\n  template-injection:\n    ignore:\n      - ci.yml\n' > "${t}/zizmor.yml"
expect_ok   "an entry of zizmor.yml for the rule and the file lets the finding pass" lint "${t}"

t="$(newtree ignore-other-rule)"; inject "${t}"
printf 'rules:\n  self-repository:\n    ignore:\n      - ci.yml\n' > "${t}/zizmor.yml"
expect_fail "an ignore entry for another rule does not hide the finding" 'template-injection' lint "${t}"

t="$(newtree ignore-other-file)"; inject "${t}"
printf 'rules:\n  template-injection:\n    ignore:\n      - e2e.yml\n' > "${t}/zizmor.yml"
expect_fail "an ignore entry for another file does not hide the finding" 'template-injection' lint "${t}"

# a finding that only the pedantic persona reports (a workflow without a concurrency group) fails the gate: the script asks for it
t="$(newtree pedantic-only)"
sed -i.bak '/^concurrency:$/,/^  cancel-in-progress: true$/d' "${t}/.github/workflows/ci.yml"; rm -f "${t}/.github/workflows/ci.yml.bak"
expect_fail "a finding of the pedantic persona alone fails the gate" 'concurrency-limits' lint "${t}"
expect_ok   "the same workflow passes zizmor with the regular persona" bash -c 'cd "$1" && zizmor --offline --no-progress --persona regular --config zizmor.yml .github/workflows/ci.yml' _ "${t}"

t="$(newtree env-config)"; inject "${t}"
printf 'rules:\n  template-injection:\n    disable: true\n' > "${TEST_TMP}/permissive.yml"
expect_fail "ZIZMOR_CONFIG cannot replace zizmor.yml" 'template-injection' env ZIZMOR_CONFIG="${TEST_TMP}/permissive.yml" bash -c 'cd "$1" && TOOLS_BIN="$2" PATH="$2:$PATH" bash ci/lint-workflows.sh' _ "${t}" "${STUBS}"

t="$(newtree no-config)"; rm "${t}/zizmor.yml"
expect_fail "a missing zizmor.yml fails" 'zizmor.yml is missing' lint "${t}"

t="$(newtree online)"
expect_fail "the online mode needs a token" 'needs GH_TOKEN, GITHUB_TOKEN or ZIZMOR_GITHUB_TOKEN' env -u GH_TOKEN -u GITHUB_TOKEN -u ZIZMOR_GITHUB_TOKEN ZIZMOR_ONLINE=1 bash -c 'cd "$1" && TOOLS_BIN="$2" PATH="$2:$PATH" bash ci/lint-workflows.sh' _ "${t}" "${STUBS}"

t="$(newtree action)"
mkdir -p "${t}/.github/actions/tool"
cat > "${t}/.github/actions/tool/action.yml" <<'YAML'
name: tool
description: writes to the PATH of the following steps
runs:
  using: composite
  steps:
    - name: Put a directory on PATH
      shell: bash
      run: echo "${GITHUB_WORKSPACE}/bin" >> "${GITHUB_PATH}"
YAML
expect_fail "a finding in a local action fails" 'github-env' lint "${t}"

# an input that zizmor cannot parse fails the gate; without --strict-collection zizmor skips it with a warning
t="$(newtree broken-action)"
mkdir -p "${t}/.github/actions/tool"
printf 'name: tool\nruns: [\n' > "${t}/.github/actions/tool/action.yml"
expect_fail "an action that zizmor cannot parse fails the gate" 'failed to load .*action\.yml' lint "${t}"
t="$(newtree control-character)"
mkdir -p "${t}/.github/actions/tool"
printf 'name: tool\ndescription: a tool\nruns:\n  using: composite\n  steps:\n    - shell: bash\n      run: echo ok\n# a note\001\n' > "${t}/.github/actions/tool/action.yml"
expect_fail "an action with a control character fails the gate" 'control characters are not allowed' lint "${t}"
OUT="$(lint "${t}" 2>&1)"
if ! printf '%s\n' "${OUT}" | grep -q 'policy check'; then ok "the gate stops at zizmor, which would otherwise skip that action"; else fail "zizmor skipped the action and the gate went on"; fi
expect_fail "the policy check on its own reports that control character" 'R14: the file contains U\+0001' python3 -I "${t}/ci/check-workflows.py" --root "${t}"

t="$(newtree dependabot)"
cat > "${t}/.github/dependabot.yml" <<'YAML'
version: 2
updates:
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
YAML
expect_fail "a Dependabot entry without a cooldown fails" 'dependabot-cooldown' lint "${t}"
sed -i.bak 's|^      interval: weekly$|      interval: weekly\n    cooldown:\n      default-days: 7|' "${t}/.github/dependabot.yml"; rm -f "${t}/.github/dependabot.yml.bak"
expect_ok   "the same entry with a cooldown passes" lint "${t}"

# a configuration that switches an audit off fails the policy check, which the gate runs after zizmor (more cases of rule R14 are
# fixtures of check-workflows-test.sh; zizmor itself rejects unknown keys)
t="$(newtree config-disable)"; inject "${t}"
printf 'rules:\n  template-injection:\n    disable: true\n' > "${t}/zizmor.yml"
expect_fail "zizmor.yml with disable: true fails the gate" 'R14' lint "${t}"

# the environment cannot drop the online audits, switch zizmor offline, replace the configuration or send the audits and the token
# to another host: a stub records the options and the variables that reach zizmor
t="$(newtree no-online-env)"
ENVSTUBS="${TEST_TMP}/envstubs"; mkdir -p "${ENVSTUBS}"
cp "${STUBS}/actionlint" "${STUBS}/shellcheck" "${ENVSTUBS}/"
cat > "${ENVSTUBS}/zizmor" <<'ZEOF'
#!/bin/sh
if [ "$1" = --version ]; then echo "zizmor stub"; exit 0; fi
{
  echo "no-online-audits=${ZIZMOR_NO_ONLINE_AUDITS-unset}"
  echo "offline=${ZIZMOR_OFFLINE-unset}"
  echo "config=${ZIZMOR_CONFIG-unset}"
  echo "gh-host=${GH_HOST-unset}"
  for a in "$@"; do echo "arg=${a}"; done
} > "${ZIZMOR_ENV_FILE}"
ZEOF
chmod +x "${ENVSTUBS}/zizmor"
hostile_env() { # <file for the record of the stub> <value of ZIZMOR_ONLINE>: runs the gate with a hostile environment and the stub
  ZIZMOR_ENV_FILE="$1" ZIZMOR_ONLINE="$2" ZIZMOR_NO_ONLINE_AUDITS=1 ZIZMOR_OFFLINE=1 ZIZMOR_CONFIG="${TEST_TMP}/permissive.yml" \
    GH_HOST=ghes.example.test GH_TOKEN=x TOOLS_BIN="${ENVSTUBS}" PATH="${ENVSTUBS}:${PATH}" bash -c 'cd "$1" && bash ci/lint-workflows.sh' _ "${t}" > /dev/null 2>&1
}
hostile_env "${TEST_TMP}/zizmor-online.env" 1
for v in no-online-audits offline config gh-host; do
  if grep -Fxq "${v}=unset" "${TEST_TMP}/zizmor-online.env" 2> /dev/null; then ok "the variable behind ${v} does not reach zizmor (online mode)"; else fail "the variable behind ${v} reached zizmor (online mode)"; fi
done
if grep -Fx -A1 'arg=--gh-hostname' "${TEST_TMP}/zizmor-online.env" 2> /dev/null | grep -Fxq 'arg=github.com'; then ok "zizmor gets --gh-hostname github.com"; else fail "zizmor does not get --gh-hostname github.com"; fi
if grep -Fx -A1 'arg=--persona' "${TEST_TMP}/zizmor-online.env" 2> /dev/null | grep -Fxq 'arg=pedantic'; then ok "zizmor gets --persona pedantic"; else fail "zizmor does not get --persona pedantic"; fi
if grep -Fx -A1 'arg=--config' "${TEST_TMP}/zizmor-online.env" 2> /dev/null | grep -Fxq 'arg=zizmor.yml'; then ok "zizmor gets --config zizmor.yml"; else fail "zizmor does not get --config zizmor.yml"; fi
if grep -Fxq 'arg=--strict-collection' "${TEST_TMP}/zizmor-online.env" 2> /dev/null; then ok "zizmor gets --strict-collection"; else fail "zizmor does not get --strict-collection"; fi
if ! grep -Fxq 'arg=--offline' "${TEST_TMP}/zizmor-online.env" 2> /dev/null; then ok "ZIZMOR_ONLINE=1 does not run zizmor offline"; else fail "zizmor runs offline although ZIZMOR_ONLINE=1"; fi
hostile_env "${TEST_TMP}/zizmor-offline.env" ""
if grep -Fxq 'arg=--offline' "${TEST_TMP}/zizmor-offline.env" 2> /dev/null && grep -Fxq 'offline=unset' "${TEST_TMP}/zizmor-offline.env" && grep -Fxq 'gh-host=unset' "${TEST_TMP}/zizmor-offline.env"; then ok "the default mode is offline by option, not by variable"; else fail "the default mode of the gate is not offline by option"; fi
NOZ="${TEST_TMP}/nozizmor"; mkdir -p "${NOZ}"; cp "${STUBS}/actionlint" "${STUBS}/shellcheck" "${NOZ}/"
expect_fail "a missing zizmor fails" "command 'zizmor' not found" bash -c 'cd "$1" && TOOLS_BIN="$2" PATH="$2:/usr/bin:/bin" bash ci/lint-workflows.sh' _ "${t}" "${NOZ}"

# the real gate over the real repository (offline), and the real configuration without its ignores
repo_inputs=("${ROOT}"/.github/workflows/*.yml "${ROOT}"/.github/actions/*/action.yml "${ROOT}/.github/dependabot.yml")
repo_inputs=("${repo_inputs[@]#"${ROOT}"/}")
expect_ok   "the workflows of this repository pass zizmor (pedantic) with zizmor.yml" bash -c 'cd "$1" && shift && zizmor --offline --no-progress --persona pedantic --config zizmor.yml "$@"' _ "${ROOT}" "${repo_inputs[@]}"
expect_fail "the ignores of zizmor.yml are needed (without the configuration zizmor reports findings)" 'self-repository' bash -c 'cd "$1" && shift && zizmor --offline --no-progress --persona pedantic --no-config "$@"' _ "${ROOT}" "${repo_inputs[@]}"
expect_fail "the ignore of concurrency-limits is needed" 'concurrency-limits' bash -c 'cd "$1" && shift && zizmor --offline --no-progress --persona pedantic --no-config "$@"' _ "${ROOT}" "${repo_inputs[@]}"

finish_tests "lint-workflows-test"
