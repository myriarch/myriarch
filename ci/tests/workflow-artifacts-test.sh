#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the artifact allow-lists of the two end-to-end workflows that run on GitHub runners (.github/workflows/host-e2e.yml and
# e2e-kind.yml), with ci/tests/workflow-artifacts-check.py. Anyone who is signed in to GitHub can download the artifacts of a public
# repository, so a failed run uploads an allow-list of log files and never a token, a kubeconfig, a cookie jar, a key or a generated
# secret. The check emulates @actions/glob (what upload-artifact uses) for the patterns of the upload steps and holds them against
# lists of file names that must and must not be uploaded; this test runs it on the repository, proves the emulation with small
# cases, and then breaks copies of the workflows in the ways that matter (a path that is too wide, a missing exclusion, an upload
# for every job, a secret, a registry login, an image reference) and requires a finding for each. The same goes for the logs of a
# run that hangs (a step that runs the test needs a timeout of its own, below that of the job: a job that runs out of time is
# cancelled and uploads nothing) and for the path filter of the kind workflow (a file that the probe depends on must be in it).
# No network access.
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
CHECK="${CI_DIR}/tests/workflow-artifacts-check.py"
run_check() { python3 -I "${CHECK}" --root "$1"; }

expect_ok "the allow-lists and the shape of host-e2e.yml and e2e-kind.yml are consistent" run_check "${ROOT}"

# ---- the emulation of @actions/glob, with small cases -----------------------------------------------------------------------------
glob_case() { # expected-files patterns-json files-json
  python3 -I - "$1" "$2" "$3" <<'EOF_PY'
import importlib.util, json, os, sys
spec = importlib.util.spec_from_file_location("check", os.environ["CHECK_PY"])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
want = sorted(json.loads(sys.argv[1]))
got = sorted(mod.matched_files(json.loads(sys.argv[2]), json.loads(sys.argv[3])))
if got != want:
    print("expected %s, got %s" % (want, got))
    sys.exit(1)
EOF_PY
}
export CHECK_PY="${CHECK}"
expect_ok "a/** matches everything below a, not a itself and not ab/c" glob_case '["a/b","a/b/c"]' '["a/**"]' '["a","a/b","a/b/c","ab/c"]'
expect_ok "a/**/b matches b at any depth below a" glob_case '["a/b","a/x/b","a/x/y/b"]' '["a/**/b"]' '["a/b","a/x/b","a/x/y/b","a/xb","b"]'
expect_ok "* stays inside one segment" glob_case '["x.log"]' '["*.log"]' '["x.log","d/x.log","x.txt"]'
expect_ok "? is one character of one segment" glob_case '["a1","a2"]' '["a?"]' '["a1","a2","a","a12","a/"]'
expect_ok "a directory pattern matches what is below it (implicit descendants)" glob_case '["dir/x","dir/y/z"]' '["dir/"]' '["dir/x","dir/y/z","dirx/y"]'
expect_ok "a name without a glob also matches what is below it" glob_case '["d/f","d/g/h"]' '["d"]' '["d/f","d/g/h","e/f"]'
expect_ok "dot files and dot directories are matched" glob_case '[".x/.y",".x/z"]' '[".x/**"]' '[".x/.y",".x/z",".xx/z"]'
expect_ok "a ! line takes away what the lines before added" glob_case '["a/c"]' '["a/**","!a/b/**"]' '["a/c","a/b/d"]'
expect_ok "... and a later line adds it again (the order matters)" glob_case '["a/c","a/b/d"]' '["!a/b/**","a/**"]' '["a/c","a/b/d"]'
expect_ok "** inside a ! line reaches every depth" glob_case '["k/logs/a"]' '["k/**","!k/**/secrets/**"]' '["k/logs/a","k/secrets/x","k/diag/t/secrets/y"]'
expect_ok "a ! pattern without ** also excludes what is below it, and no name that merely starts with it" glob_case '["k/a","k/hidden"]' '["k/**","!k/hide"]' '["k/a","k/hide/x","k/hidden"]'

# ---- breaking copies of the workflows ---------------------------------------------------------------------------------------------
T="${TEST_TMP}/tree"
fresh_tree() {
  rm -rf "${T}"
  mkdir -p "${T}/.github/workflows" "${T}/hack/kind"
  cp "${ROOT}/.github/workflows/host-e2e.yml" "${ROOT}/.github/workflows/e2e-kind.yml" "${T}/.github/workflows/"
  cp "${ROOT}/hack/kind/versions.env" "${T}/hack/kind/"
}
edit() { # file old new [count]: replaces the text exactly count times (default 1)
  python3 -I -c '
import sys
path, old, new, count = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
text = open(path).read()
if text.count(old) != count:
    sys.exit("edit: %r found %d times, expected %d in %s" % (old, text.count(old), count, path))
open(path, "w").write(text.replace(old, new))
' "$1" "$2" "$3" "${4:-1}"
}
removed_line() { # file text [count]: removes the lines that contain the text (exactly count lines, default 1)
  python3 -I -c '
import sys
path, needle, count = sys.argv[1], sys.argv[2], int(sys.argv[3])
lines = open(path).read().split("\n")
kept = [l for l in lines if needle not in l]
if len(kept) != len(lines) - count:
    sys.exit("removed_line: %r matches %d lines in %s, expected %d" % (needle, len(lines) - len(kept), path, count))
open(path, "w").write("\n".join(kept))
' "$1" "$2" "${3:-1}"
}
bites() { # name regex: the check on the broken tree fails and names the problem
  local name="$1" want="$2"
  if OUT="$(run_check "${T}" 2>&1)"; then
    fail "${name} (the check passed)"
  elif printf '%s' "${OUT}" | grep -Eq -- "${want}"; then
    ok "${name}"
  else
    fail "${name} (no finding matches /${want}/)"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2
  fi
}
HOST="${T}/.github/workflows/host-e2e.yml"
KIND="${T}/.github/workflows/e2e-kind.yml"

fresh_tree; edit "${HOST}" '.cache/host-e2e/upload/**
' '.cache/host-e2e/**
'
bites "host-e2e: an upload of the whole work directory is found" 'host-e2e.yml:[0-9]+: the upload matches \.cache/host-e2e/manual/enrollment-token'
case "${OUT}" in *"tls/ca.key"*"data/postgres"* | *"data/postgres"*"tls/ca.key"*) ok "... together with the keys and the database" ;; *) printf '%s' "${OUT}" | grep -q 'ca.key' && printf '%s' "${OUT}" | grep -q 'PG_VERSION' && ok "... together with the keys and the database" || fail "the keys and the database are not named: ${OUT}" ;; esac
fresh_tree; removed_line "${HOST}" '!.cache/host-e2e/upload/**/cookies*'
bites "host-e2e: without the exclusion of cookie jars a stray jar in the upload directory is found" 'matches \.cache/host-e2e/upload/manual/cookies'
fresh_tree; removed_line "${HOST}" '!.cache/host-e2e/upload/**/*token*'
bites "host-e2e: without the exclusion of token files a stray token file is found" 'matches \.cache/host-e2e/upload/manual/enrollment-token'
fresh_tree; removed_line "${HOST}" '!.cache/host-e2e/upload/**/*.key'
bites "host-e2e: without the exclusion of keys a stray key is found" 'matches \.cache/host-e2e/upload/manual/ca\.key'
fresh_tree; edit "${HOST}" 'if: failure()' 'if: always()'
bites "host-e2e: an upload for every run is found" 'must run only for a failed job'
fresh_tree; removed_line "${HOST}" 'include-hidden-files: true'
bites "host-e2e: an upload that skips the dot directory is found" 'include-hidden-files must be true'
fresh_tree; edit "${HOST}" 'retention-days: 7' 'retention-days: 90'
bites "host-e2e: a retention of 90 days is found" 'retention-days must be at most 7'
fresh_tree; edit "${HOST}" "          HOST_E2E: '1'
" ""
bites "host-e2e: a scenario step without HOST_E2E is found" "must set HOST_E2E: '1'"
fresh_tree; edit "${HOST}" "  CI: 'true'
" "  CI: 'true'
  HOST_E2E: '1'
"
bites "host-e2e: HOST_E2E for the whole workflow is found" 'HOST_E2E must not be set for the whole workflow'
fresh_tree; edit "${HOST}" 'run: scripts/host-e2e/linux.sh manual' 'run: echo skipped'
bites "host-e2e: a workflow that runs no scenario is found" 'must call the scenario by name'
fresh_tree; edit "${HOST}" 'contents: read' 'contents: write'
bites "host-e2e: a write permission is found" 'permissions of the workflow must be exactly'
fresh_tree; edit "${HOST}" "        os: [ubuntu-24.04, ubuntu-24.04-arm]" "        os: [self-hosted]"
bites "host-e2e: a self-hosted runner is found" 'never a self-hosted runner'
fresh_tree; edit "${HOST}" "      - '.github/workflows/host-e2e.yml'
" ""
bites "host-e2e: a path filter that does not name the workflow is found" 'path filter of pull requests must name the workflow itself'
fresh_tree; edit "${HOST}" 'persist-credentials: false' 'persist-credentials: true'
bites "host-e2e: a checkout that keeps its credentials is found" 'must not persist credentials'
fresh_tree; edit "${HOST}" '          name: host-e2e-linux-${{ matrix.os }}' '          env:
            TOKEN: ${{ secrets.NAME }}
          name: host-e2e-linux-${{ matrix.os }}'
bites "host-e2e: a secret is found" 'must not use a secret'
fresh_tree; edit "${HOST}" 'path: |
            .cache/host-e2e/upload/**' 'path: |
            .cache/host-e2e/upload/**
            .cache/host-e2e/tls/ca.key'
bites "host-e2e: an extra path to a key is found" 'matches \.cache/host-e2e/tls/ca\.key'
fresh_tree; edit "${HOST}" '.cache/host-e2e/upload/**
' '**
'
bites "host-e2e: a path that matches everything is no allow-list" 'is not an allow-list'
fresh_tree; rm "${HOST}"
bites "host-e2e: a missing workflow is found" 'host-e2e.yml: the workflow does not exist'
fresh_tree; removed_line "${HOST}" 'timeout-minutes: 35'
bites "host-e2e: a scenario step without a timeout of its own is found" 'host-e2e.yml: the step `Scenario manual[^`]*` needs a timeout-minutes of its own'
fresh_tree; edit "${HOST}" '        timeout-minutes: 35' '        timeout-minutes: 41'
bites "host-e2e: a step timeout less than 5 minutes below that of the job is found" 'steps that run the test \(41 minutes together\) must stay at least 5 minutes below that of the job \(45 minutes\)'
fresh_tree; edit "${HOST}" '        timeout-minutes: 35' '        timeout-minutes: 40'
expect_ok "host-e2e: a step timeout exactly 5 minutes below that of the job is fine" run_check "${T}"
fresh_tree; edit "${HOST}" '        timeout-minutes: 35' '        timeout-minutes: soon'
bites "host-e2e: a step timeout that is no number is found" 'needs a timeout-minutes of its own, a number of minutes, found `soon`'
fresh_tree; removed_line "${HOST}" '    timeout-minutes: 45'
bites "host-e2e: a job without a timeout is found" 'the job must have one timeout-minutes'
fresh_tree; edit "${HOST}" "    steps:
" "    steps:
      - name: A second scenario
        timeout-minutes: 35
        env:
          HOST_E2E: '1'
        run: scripts/host-e2e/linux.sh one-command
"
bites "host-e2e: scenario steps whose timeouts together do not fit into the job are found" 'steps that run the test \(70 minutes together\)'

fresh_tree; edit "${KIND}" '            .cache/kind/logs/**
            .cache/kind/diag/**' '            .cache/kind/**' 3
bites "e2e-kind: an upload of the whole work directory is found" 'matches \.cache/kind/values-tokens\.yaml'
case "${OUT}" in *"helm/config/registry/config.json"*) ok "... together with the registry credentials of Helm" ;; *) fail "the Helm registry credentials are not named: ${OUT}" ;; esac
fresh_tree; removed_line "${KIND}" '!.cache/kind/**/secrets/**' 3
bites "e2e-kind: without the exclusion of secrets/ a secrets directory in the diagnostics is found" 'matches \.cache/kind/diag/20261015T010101/secrets/x'
# the two uploads are checked one by one: a change to the upload of the job "charts" alone is found as well
fresh_tree; edit "${KIND}" '          name: e2e-kind-charts-${{ matrix.label }}
          path: |
' '          name: e2e-kind-charts-${{ matrix.label }}
          path: |
            .cache/kind/values-chart-test.yaml
'
bites "e2e-kind: the generated values in the upload of the charts job alone are found" 'matches \.cache/kind/values-chart-test\.yaml'
fresh_tree; edit "${KIND}" '          name: e2e-kind-charts-${{ matrix.label }}
          path: |
' '          name: e2e-kind-charts-${{ matrix.label }}
          path: |
            .cache/kind/chart-v0.1.0/**
'
bites "e2e-kind: the checkout of the old chart in the upload of the charts job alone is found" 'matches \.cache/kind/chart-v0\.1\.0/deploy/helm/myriarch/values\.yaml'
fresh_tree; removed_line "${KIND}" '!.cache/kind/**/cookies*' 3
bites "e2e-kind: without the exclusion of cookie jars a jar in the diagnostics is found" 'matches \.cache/kind/diag/20261015T010101/cookies-main'
fresh_tree; removed_line "${KIND}" '!.cache/kind/**/kubeconfig*' 3
bites "e2e-kind: without the exclusion of kubeconfig a kubeconfig in the diagnostics is found" 'matches \.cache/kind/diag/20261015T010101/kubeconfig'
fresh_tree; removed_line "${KIND}" '!.cache/kind/logs/hygiene-dump/**' 3
bites "e2e-kind: without the exclusion of the scan dump of the hygiene stage it is found" 'matches \.cache/kind/logs/hygiene-dump/'
# an extra line counts after the exclusions that come before it: a path that names a secret directly, at the end of the list
fresh_tree; edit "${KIND}" '            !.cache/kind/**/*.pem
' '            !.cache/kind/**/*.pem
            .cache/kind/kubeconfig
' 3
bites "e2e-kind: an extra path to the kubeconfig after the exclusions is found" 'matches \.cache/kind/kubeconfig'
fresh_tree; edit "${KIND}" '            .cache/kind/results.txt
' '' 3
bites "e2e-kind: an allow-list that lacks the result list is found" 'does not match \.cache/kind/results\.txt'
fresh_tree; edit "${KIND}" 'if: failure()' 'if: always()' 3
bites "e2e-kind: an upload for every run is found" 'must run only for a failed job'
fresh_tree; edit "${KIND}" 'image-var: KIND_NODE_IMAGE' 'image-var: KIND_NODE_IMAGE_OLDEST' 3
bites "e2e-kind: a matrix entry whose pin does not exist is found" 'is no pin of hack/kind/versions\.env'
fresh_tree; edit "${KIND}" 'image-var: KIND_NODE_IMAGE' 'image-var: POSTGRES_IMAGE' 3
bites "e2e-kind: a pin that is not a node image is found" 'is not a node image pin'
fresh_tree; edit "${KIND}" '          export KIND_NODE_IMAGE' '          export KIND_NODE_IMAGE=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5' 3
bites "e2e-kind: an image reference in the workflow is found" 'an image reference does not belong in the workflow'
fresh_tree; edit "${KIND}" '          make kind-probe' '          docker login ghcr.io
          make kind-probe'
bites "e2e-kind: a registry login is found" 'no registry login'
fresh_tree; edit "${KIND}" '          make kind-probe' '          make kind-up'
bites "e2e-kind: a run without the probe is found" 'runs through make kind-probe'
fresh_tree; edit "${KIND}" 'contents: read' 'contents: read
  packages: write'
bites "e2e-kind: an additional permission is found" 'permissions of the workflow must be exactly'
fresh_tree; edit "${KIND}" '    timeout-minutes: 75
' '    timeout-minutes: 75
    permissions:
      contents: write
'
bites "e2e-kind: a job that raises its permissions is found" 'no job may raise its permissions'
fresh_tree; edit "${KIND}" "          PROBE_SKIP: \${{ inputs.skip }}" "          PROBE_SKIP: \${{ inputs.skip }}
          HOST_E2E: '1'"
bites "e2e-kind: HOST_E2E in the cluster test is found" 'HOST_E2E belongs to the host test'
fresh_tree; removed_line "${KIND}" 'timeout-minutes: 65'
bites "e2e-kind: a probe step without a timeout of its own is found" 'e2e-kind.yml: the step `Run the probe` needs a timeout-minutes of its own'
fresh_tree; edit "${KIND}" '        timeout-minutes: 65' '        timeout-minutes: 71'
bites "e2e-kind: a probe timeout less than 5 minutes below that of the job is found" 'steps that run the test \(71 minutes together\) must stay at least 5 minutes below that of the job \(75 minutes\)'
fresh_tree; edit "${KIND}" '        timeout-minutes: 65' '        timeout-minutes: 70'
expect_ok "e2e-kind: a probe timeout exactly 5 minutes below that of the job is fine" run_check "${T}"
fresh_tree; removed_line "${KIND}" '    timeout-minutes: 75'
bites "e2e-kind: a job without a timeout is found" 'the job must have one timeout-minutes'
# the charts job: one timeout of its own, two Kubernetes versions
fresh_tree; removed_line "${KIND}" 'timeout-minutes: 50'
bites "e2e-kind: a chart test step without a timeout of its own is found" 'e2e-kind.yml: the step `Run the chart installation test` needs a timeout-minutes of its own'
fresh_tree; edit "${KIND}" '        timeout-minutes: 50' '        timeout-minutes: 56'
bites "e2e-kind: a chart test timeout less than 5 minutes below that of the job is found" 'steps that run the test \(56 minutes together\) must stay at least 5 minutes below that of the job \(60 minutes\)'
fresh_tree; removed_line "${KIND}" 'image-var: KIND_NODE_IMAGE_PREVIOUS'
bites "e2e-kind: a charts job on one Kubernetes version only is found" 'runs on two Kubernetes versions'
fresh_tree; removed_line "${KIND}" 'ci/kind-chart-test.sh --fresh'
bites "e2e-kind: a charts job that does not run the test is found" 'the charts job runs ci/kind-chart-test.sh'
fresh_tree; removed_line "${KIND}" "      - 'ci/kind-chart-test.sh'"
bites "e2e-kind: a path filter without the chart test is found" 'path filter of pull requests lacks `ci/kind-chart-test.sh`'
# the quickstart job: one timeout of its own for the step that runs the script of the page, and the demo policies in the path filter
fresh_tree; removed_line "${KIND}" 'timeout-minutes: 45'
bites "e2e-kind: a quickstart step without a timeout of its own is found" 'e2e-kind.yml: the step `Run the Quickstart from the start to the end` needs a timeout-minutes of its own'
fresh_tree; edit "${KIND}" 'make kind-quickstart-all' 'make kind-quickstart'
bites "e2e-kind: a quickstart job that does not run the script of the page is found" 'the quickstart job runs make kind-quickstart-all'
fresh_tree; removed_line "${KIND}" "      - 'examples/quickstart/**'"
bites "e2e-kind: a path filter without the demo policies of the Quickstart is found" 'path filter of pull requests lacks `examples/quickstart/\*\*`'
# the path filter of pull requests: a file that only the probe depends on must start it
fresh_tree; removed_line "${KIND}" "      - 'ci/versions.env'"
bites "e2e-kind: a path filter without the pins of ci/versions.env is found" 'path filter of pull requests lacks `ci/versions.env`'
fresh_tree; removed_line "${KIND}" "      - 'scripts/lib/image-common.sh'"
bites "e2e-kind: a path filter without the library of the image scripts is found" 'path filter of pull requests lacks `scripts/lib/image-common.sh`'
fresh_tree; removed_line "${KIND}" "      - '.github/actions/setup/**'"
bites "e2e-kind: a path filter without the setup action of the job is found" 'path filter of pull requests lacks `.github/actions/setup/\*\*`'

# the unbroken copy is fine, so the cases above fail for their own reason
fresh_tree
expect_ok "the unbroken copy passes" run_check "${T}"

finish_tests "workflow-artifacts-test"
