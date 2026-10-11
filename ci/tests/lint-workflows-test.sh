#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the zizmor step of ci/lint-workflows.sh with the real zizmor (ci/install-tools.sh zizmor): a finding fails the gate
# (also one in a local action and in the Dependabot configuration), an ignore comment or an entry of zizmor.yml for exactly that
# rule lets it pass, the environment cannot replace the configuration, the online mode needs a token, and the workflows of this
# repository pass. actionlint and ShellCheck are stubs (they are tested by their own gate), the policy check is the real one.
# No network, no ports: zizmor runs with --offline.
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
sed -i.bak 's|^\(        run: echo "\${{ steps.v.outputs.ver }}"\)$|\1 # zizmor: ignore[template-injection]|' "${t}/.github/workflows/ci.yml"; rm -f "${t}/.github/workflows/ci.yml.bak"
expect_ok   "an ignore comment for the rule lets the finding pass" lint "${t}"

t="$(newtree ignore-config)"; inject "${t}"
printf 'rules:\n  template-injection:\n    ignore:\n      - ci.yml\n' > "${t}/zizmor.yml"
expect_ok   "an entry of zizmor.yml for the rule and the file lets the finding pass" lint "${t}"

t="$(newtree ignore-other-rule)"; inject "${t}"
printf 'rules:\n  self-repository:\n    ignore:\n      - ci.yml\n' > "${t}/zizmor.yml"
expect_fail "an ignore entry for another rule does not hide the finding" 'template-injection' lint "${t}"

t="$(newtree ignore-other-file)"; inject "${t}"
printf 'rules:\n  template-injection:\n    ignore:\n      - e2e.yml\n' > "${t}/zizmor.yml"
expect_fail "an ignore entry for another file does not hide the finding" 'template-injection' lint "${t}"

t="$(newtree env-config)"; inject "${t}"
printf 'rules:\n  template-injection:\n    disable: true\n' > "${TEST_TMP}/permissive.yml"
expect_fail "ZIZMOR_CONFIG cannot replace zizmor.yml" 'template-injection' env ZIZMOR_CONFIG="${TEST_TMP}/permissive.yml" bash -c 'cd "$1" && TOOLS_BIN="$2" PATH="$2:$PATH" bash ci/lint-workflows.sh' _ "${t}" "${STUBS}"

t="$(newtree no-config)"; rm "${t}/zizmor.yml"
expect_fail "a missing zizmor.yml fails" 'zizmor.yml is missing' lint "${t}"

t="$(newtree online)"
expect_fail "the online mode needs a token" 'needs GH_TOKEN or GITHUB_TOKEN' env -u GH_TOKEN -u GITHUB_TOKEN ZIZMOR_ONLINE=1 bash -c 'cd "$1" && TOOLS_BIN="$2" PATH="$2:$PATH" bash ci/lint-workflows.sh' _ "${t}" "${STUBS}"

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

# the real configuration over the real inputs (offline)
repo_inputs=(.github/workflows/*.yml .github/actions/*/action.yml .github/dependabot.yml)
expect_ok   "the workflows of this repository pass zizmor with zizmor.yml" bash -c 'cd "$1" && shift && zizmor --offline --no-progress --config zizmor.yml "$@"' _ "${ROOT}" "${repo_inputs[@]}"
expect_fail "the ignores of zizmor.yml are needed (without the configuration zizmor reports findings)" 'self-repository' bash -c 'cd "$1" && shift && zizmor --offline --no-progress --no-config "$@"' _ "${ROOT}" "${repo_inputs[@]}"

finish_tests "lint-workflows-test"
