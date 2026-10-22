#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Check of the artifact allow-lists and the shape of the two end-to-end workflows that run on GitHub runners
(.github/workflows/host-e2e.yml and e2e-kind.yml). ci/tests/workflow-artifacts-test.sh runs it on the repository and on copies that
it breaks on purpose.

    python3 -I ci/tests/workflow-artifacts-check.py [--root DIR]

Anyone who is signed in to GitHub can download the artifacts of a public repository, so a failed run uploads an allow-list of log
files and never a token, a kubeconfig, a cookie jar, a key or the generated secrets. The check reads the `path:` of every
actions/upload-artifact step of the two workflows and decides, for a list of file names that must be uploaded and a list of file names
that must never be, which of them the patterns match. The matching follows @actions/glob, which upload-artifact uses: the patterns are
applied in order (a line is added by a pattern and taken away again by a later line that starts with `!`), `*` and `?` stay within
one path segment, `**` is any number of segments, files whose name starts with a dot are matched, and a pattern that does not end in
`**` also matches everything below it (a directory).

It also checks the shape that the documentation promises: the upload runs only for a failed job, includes hidden files (the work
directories are dot directories), ignores a missing directory, keeps the files for seven days at most, the host test runs the scenario
by name with HOST_E2E=1 on that step only, and the matrix of the kind test takes the node image from a pin of hack/kind/versions.env
that is held to a digest (no image reference in the workflow).

Two more rules keep the logs of a run that hangs. A job that runs out of time is cancelled, and `failure()` is false for a cancelled
job, so the upload would not run; a step that runs out of time counts as failed. The steps that run the test (the scenario steps of
the host test, the probe of the kind test) therefore have a `timeout-minutes` of their own, and together they stay at least
STEP_TIMEOUT_MARGIN minutes below the timeout of the job (the checkout, the toolchain, the build and the upload need that time). And
the path filter of pull requests of the kind test names the files outside hack/kind that the probe reads or runs (KIND_PATHS): a
change that touches only one of them must start the probe. The output is one line per finding, "<file>: <message>"; the exit
status is 1 when there is a finding. Standard library only.
"""

import argparse
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_ROOT = os.path.dirname(os.path.dirname(HERE))

# What a failed run may upload, and what it must never upload. The second list names the files that exist on a runner: the work
# files of the scripts, the generated secrets, the keys of the throwaway CA, the databases, the mirror, the binaries and every place a
# credential could be. Both lists are relative to the root of the checkout.
POLICY = {
    "host-e2e.yml": {
        "must_match": [
            ".cache/host-e2e/upload/manual/results.txt",
            ".cache/host-e2e/upload/manual/myriarchd.log",
            ".cache/host-e2e/upload/manual/install-1.log",
            ".cache/host-e2e/upload/manual/diag/journal-otelcol-supervisor.txt",
            ".cache/host-e2e/upload/manual/diag/updater.yaml",
            ".cache/host-e2e/upload/manual/api/agents.json",
            ".cache/host-e2e/upload/mirror-build.log",
            # room for a second scenario
            ".cache/host-e2e/upload/one-command/results.txt",
            ".cache/host-e2e/upload/one-command/diag/journal-otelcol-supervisor.txt",
            # the Windows host test (scripts/host-e2e/windows.ps1)
            ".cache/host-e2e/upload/one-command/install-command.log",
            ".cache/host-e2e/upload/one-command/myriarchd.out.log",
            ".cache/host-e2e/upload/one-command/diag/status-otelcol-supervisor.txt",
            ".cache/host-e2e/upload/one-command/diag/events-myriarch-updater.txt",
            ".cache/host-e2e/upload/one-command/diag/acl.txt",
            ".cache/host-e2e/upload/one-command/diag/updater-log.txt",
        ],
        "must_not_match": [
            ".cache/host-e2e/manual/enrollment-token",
            ".cache/host-e2e/manual/cookies",
            ".cache/host-e2e/manual/myriarchd.log",
            ".cache/host-e2e/manual/results.txt",
            ".cache/host-e2e/manual/overlay.yaml",
            ".cache/host-e2e/manual/data/postgres/data/PG_VERSION",
            ".cache/host-e2e/manual/gitops/fleets/host-e2e.yaml",
            ".cache/host-e2e/manual/templates/updater/linux/updater.yaml",
            ".cache/host-e2e/tls/ca.key",
            ".cache/host-e2e/tls/ca.pem",
            ".cache/host-e2e/tls/server.key",
            ".cache/host-e2e/tls/server.pem",
            ".cache/host-e2e/mirror/srv/mirror/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz",
            ".cache/host-e2e/mirror-evil/v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz",
            ".cache/host-e2e/one-command/enrollment-token",
            # the rendered command holds the token, and the response of the server holds the command
            ".cache/host-e2e/one-command/command.sh",
            ".cache/host-e2e/one-command/commands-response.json",
            ".cache/host-e2e/bundles/myriarch-updater_0.2.0_linux_amd64.tar.gz",
            ".cache/host-e2e/bundles/myriarch-updater_0.2.0_windows_amd64.zip",
            ".cache/host-e2e/one-command/command.ps1",
            ".cache/host-e2e/one-command/gitops/fleets/windows-hosts.yaml",
            ".cache/host-e2e/mirror/srv/mirror/cmd/opampsupervisor/v0.162.0/opampsupervisor_0.162.0_windows_amd64.exe",
            ".cache/host-e2e/upload/one-command/command.ps1",
            ".cache/host-e2e/upload/one-command/command.sh",
            ".cache/host-e2e/upload/one-command/commands-response.json",
            ".cache/host-e2e/upload/one-command/enrollment-token",
            "bin/myriarchd.exe",
            # a stray secret that gets into the upload directory is not matched either (the second line of defence)
            ".cache/host-e2e/upload/manual/cookies",
            ".cache/host-e2e/upload/manual/enrollment-token",
            ".cache/host-e2e/upload/manual/myriarch-enrollment-token.txt",
            ".cache/host-e2e/upload/manual/ca.key",
            ".cache/host-e2e/upload/manual/server.key",
            ".cache/host-e2e/upload/manual/kubeconfig",
            ".cache/host-e2e/upload/manual/secrets/bootstrap.token",
            "bin/myriarchd",
            "web/node_modules/.bin/vite",
            ".git/config",
            ".env",
        ],
    },
    "e2e-kind.yml": {
        "must_match": [
            ".cache/kind/probe.log",
            ".cache/kind/results.txt",
            ".cache/kind/durations.txt",
            ".cache/kind/logs/up.log",
            ".cache/kind/logs/images.log",
            ".cache/kind/logs/pods/myriarch-myriarch-0.log",
            ".cache/kind/diag/20261015T010101/cluster.txt",
            ".cache/kind/diag/20261015T010101/logs/myriarch-myriarch-0.log",
            ".cache/kind/diag/20261015T010101/api-rollouts.json",
        ],
        "must_not_match": [
            ".cache/kind/kubeconfig",
            ".cache/kind/secrets/bootstrap.token",
            ".cache/kind/secrets/bootstrap.token.old",
            ".cache/kind/secrets/postgres-password",
            ".cache/kind/secrets/database-dsn",
            ".cache/kind/secrets/git-ssh-key",
            ".cache/kind/secrets/.pattern",
            ".cache/kind/secrets/.hits",
            ".cache/kind/cookies-main",
            ".cache/kind/cookies-old",
            ".cache/kind/cookies-rotated",
            ".cache/kind/state.env",
            ".cache/kind/images.env",
            ".cache/kind/values-tokens.yaml",
            ".cache/kind/gitops.tar",
            ".cache/kind/trust/trusted_root.json",
            ".cache/kind/helm/config/registry/config.json",
            ".cache/kind/render/deployed-myriarch.myriarchd.yaml",
            ".cache/kind/logs/hygiene-dump/helm-myriarch-myriarch.txt",
            ".cache/kind/logs/hygiene-dump/probe-own/probe.log",
            ".cache/kind/diag/20261015T010101/secrets/x",
            ".cache/kind/diag/20261015T010101/kubeconfig",
            ".cache/kind/diag/20261015T010101/cookies-main",
            ".cache/bin/kubectl",
            ".cache/kind/.myriarch-kind-work",
            "bin/myriarchd",
            ".git/config",
            ".env",
        ],
    },
}

# Minutes that the timeout of the job keeps over the timeouts of the steps that run the test.
STEP_TIMEOUT_MARGIN = 5

# The files outside hack/kind that the probe reads or runs, and that a pull request can change without touching hack/kind: the
# image build scripts and what they source and read, the pins, the tool installer and the module files of the tools it builds, the
# simulator profile and component list, the Makefile target, the setup action of the job and the Node.js version it installs.
# Entries of the path filter, as written there. The code of the product that goes into the images is not on the list; the nightly
# run covers it.
KIND_PATHS = [
    "scripts/build-image-crane.sh",
    "scripts/build-collector-image-crane.sh",
    "scripts/lib/image-common.sh",
    "scripts/lib/oci_image.py",
    "ci/image-labels.env",
    "ci/install-tools.sh",
    "ci/lib.sh",
    "ci/versions.env",
    "ci/tools/kind/**",
    "ci/tools/helm/**",
    "ci/tools/crane/**",
    "ci/tools/yq/**",
    "examples/sim/**",
    "Makefile",
    ".github/actions/setup/**",
    ".node-version",
]


# ------------------------------------------------------------------------------------------------------------- globs ---
def segment_regex(segment):
    out = []
    for ch in segment:
        if ch == "*":
            out.append("[^/]*")
        elif ch == "?":
            out.append("[^/]")
        else:
            out.append(re.escape(ch))
    return "".join(out)


def pattern_regex(pattern):
    """The regular expression of one pattern of @actions/glob (minimatch with dot files, without braces and extglobs)."""
    segments = pattern.split("/")
    out = ["^"]
    for index, segment in enumerate(segments):
        last = index == len(segments) - 1
        if segment == "**":
            out.append(".*" if last else "(?:.*/)?")
        else:
            out.append(segment_regex(segment))
            if not last:
                out.append("/")
    out.append("$")
    return re.compile("".join(out))


def matched_files(patterns, files):
    """The files that the patterns select, in the way of @actions/glob: in order, `!` takes away what the lines before added."""
    selected = set()
    for raw in patterns:
        negate = raw.startswith("!")
        pattern = raw[1:] if negate else raw
        pattern = pattern.strip()
        if not pattern:
            continue
        variants = [pattern.rstrip("/")]
        if pattern.endswith("/") or not pattern.rstrip("/").endswith("**"):
            variants.append(pattern.rstrip("/") + "/**")  # implicit descendants
        regexes = [pattern_regex(v) for v in variants]
        for f in files:
            if any(r.match(f) for r in regexes):
                if negate:
                    selected.discard(f)
                else:
                    selected.add(f)
    return selected


# ------------------------------------------------------------------------------------------------------ the workflow ---
def read_steps(text):
    """The steps of a workflow as (start line, lines): a step starts with `- ` at the indentation of the steps."""
    lines = text.split("\n")
    steps = []
    current = None
    indent = None
    for number, line in enumerate(lines, 1):
        m = re.match(r"^( +)- (name|uses|run|id|if|env|with|shell|working-directory):", line)
        if m and (indent is None or len(m.group(1)) == indent) and len(m.group(1)) >= 6:
            indent = len(m.group(1))
            if current:
                steps.append(current)
            current = (number, [line])
        elif current:
            if line.strip() and not line.startswith(" " * (indent + 1)) and not line.startswith(" " * indent + "-"):
                steps.append(current)
                current = None
                indent = None
            else:
                current[1].append(line)
    if current:
        steps.append(current)
    return steps


def step_value(lines, key):
    """The scalar value of `key:` inside a step (the key may be the first key after `- `)."""
    for line in lines:
        m = re.match(r"^ *(?:- )?" + re.escape(key) + r":[ \t]*(.*?)[ \t]*(?:#.*)?$", line)
        if m:
            return m.group(1).strip("'\"")
    return None


def step_block(lines, key):
    """The lines of a block scalar `key: |` (or the single value) inside a step."""
    for i, line in enumerate(lines):
        m = re.match(r"^( *)" + re.escape(key) + r":[ \t]*(\|[-+]?|>[-+]?)?[ \t]*(.*?)[ \t]*$", line)
        if not m:
            continue
        if m.group(2):
            base = len(m.group(1))
            block = []
            for follow in lines[i + 1:]:
                if follow.strip() == "":
                    continue
                if len(follow) - len(follow.lstrip(" ")) <= base:
                    break
                block.append(follow.strip())
            return block
        return [m.group(3).strip("'\"")] if m.group(3) else []
    return []


def upload_steps(text):
    found = []
    for number, lines in read_steps(text):
        uses = step_value(lines, "uses")
        if uses and uses.startswith("actions/upload-artifact@"):
            found.append((number, lines))
    return found


def check_uploads(name, text, policy, findings):
    steps = upload_steps(text)
    if not steps:
        findings.append("%s: no actions/upload-artifact step" % name)
        return
    for number, lines in steps:
        where = "%s:%d" % (name, number)
        patterns = step_block(lines, "path")
        if not patterns:
            findings.append("%s: the upload step has no path" % where)
            continue
        condition = step_value(lines, "if") or ""
        if condition != "failure()":
            findings.append("%s: the upload must run only for a failed job (`if: failure()`), found `%s`" % (where, condition))
        if step_value(lines, "include-hidden-files") != "true":
            findings.append("%s: include-hidden-files must be true (the work directories are dot directories)" % where)
        if step_value(lines, "if-no-files-found") != "ignore":
            findings.append("%s: if-no-files-found must be ignore" % where)
        retention = step_value(lines, "retention-days") or ""
        if not retention.isdigit() or not 1 <= int(retention) <= 7:
            findings.append("%s: retention-days must be at most 7, found `%s`" % (where, retention))
        universe = list(policy["must_match"]) + list(policy["must_not_match"])
        selected = matched_files(patterns, universe)
        for f in policy["must_match"]:
            if f not in selected:
                findings.append("%s: the upload does not match %s, which a failed run needs" % (where, f))
        for f in policy["must_not_match"]:
            if f in selected:
                findings.append("%s: the upload matches %s, which must never be uploaded" % (where, f))
        for p in patterns:
            if p.lstrip("!").strip() in ("*", "**", ".", "./", "**/*") or p.lstrip("!").strip().startswith(("/", "~", "..")):
                findings.append("%s: the path `%s` is not an allow-list" % (where, p))


def check_common(name, text, findings):
    if re.search(r"^[ \t]*(pull_request_target|workflow_run)[ \t]*:", text, re.M):
        findings.append("%s: pull_request_target and workflow_run are forbidden" % name)
    if re.search(r"\bsecrets\.", text):
        findings.append("%s: the workflow must not use a secret" % name)
    m = re.search(r"^permissions:\n((?:  .*\n)+)", text, re.M)
    if not m or m.group(1).strip() != "contents: read":
        findings.append("%s: the permissions of the workflow must be exactly `contents: read`" % name)
    if re.search(r"^[ \t]+permissions:", text, re.M):
        findings.append("%s: no job may raise its permissions" % name)
    if not re.search(r"^[ \t]+- '\.github/workflows/%s'$" % re.escape(name), text, re.M):
        findings.append("%s: the path filter of pull requests must name the workflow itself" % name)
    if "persist-credentials: false" not in text:
        findings.append("%s: the checkout must not persist credentials" % name)
    if not re.search(r"^  CI: 'true'$", text, re.M):
        findings.append("%s: CI must be 'true' for the whole workflow" % name)
    if "ci-ok" in text and re.search(r"needs:.*\bci-ok\b", text):
        findings.append("%s: it must not depend on ci-ok" % name)


def job_blocks(text):
    """The jobs of a workflow as (job id, text of the job): a job starts with its id at the indentation of two spaces below `jobs:`."""
    head, sep, rest = text.partition("\njobs:\n")
    if not sep:
        return []
    blocks = []
    current = None
    for line in rest.split("\n"):
        m = re.match(r"^  ([A-Za-z0-9_-]+):[ \t]*(?:#.*)?$", line)
        if m:
            current = (m.group(1), [line])
            blocks.append(current)
        elif current is not None:
            current[1].append(line)
    return [(job, "\n".join(lines)) for job, lines in blocks]


def check_timeouts(name, text, steps, findings, job=""):
    """`steps` are the (line, lines) pairs of the steps that run the test: each has a timeout of its own, and together they leave the
    margin below the timeout of the job. `text` is the text of one job."""
    where = "%s%s" % (name, " (job %s)" % job if job else "")
    jobs = re.findall(r"^    timeout-minutes:[ \t]*(\S*)[ \t]*(?:#.*)?$", text, re.M)
    if len(jobs) != 1 or not jobs[0].isdigit():
        findings.append("%s: the job must have one timeout-minutes, a number of minutes" % where)
        return
    limit = int(jobs[0])
    total = 0
    for _, lines in steps:
        value = step_value(lines, "timeout-minutes") or ""
        if not value.isdigit() or int(value) < 1:
            findings.append("%s: the step `%s` needs a timeout-minutes of its own, a number of minutes, found `%s` (a job that runs out"
                            " of time is cancelled and uploads nothing, a step that runs out of time counts as failed)"
                            % (where, step_value(lines, "name") or "?", value))
        else:
            total += int(value)
    if total and total + STEP_TIMEOUT_MARGIN > limit:
        findings.append("%s: the timeouts of the steps that run the test (%d minutes together) must stay at least %d minutes below that"
                        " of the job (%d minutes), so that the upload still runs when a step runs out of time"
                        % (where, total, STEP_TIMEOUT_MARGIN, limit))


def check_host(text, findings):
    name = "host-e2e.yml"
    if not re.search(r"^[ \t]+run: scripts/host-e2e/linux\.sh manual$", text, re.M):
        findings.append("%s: a step must call the scenario by name: scripts/host-e2e/linux.sh manual" % name)
    # HOST_E2E=1 belongs to the step that runs a scenario, not to the whole workflow or job
    top = text.split("\njobs:\n", 1)[0]
    if re.search(r"^[ \t]+HOST_E2E:", top, re.M):
        findings.append("%s: HOST_E2E must not be set for the whole workflow" % name)
    scenario_steps = [(number, lines) for number, lines in read_steps(text) if (step_value(lines, "run") or "").startswith("scripts/host-e2e/")]
    if not scenario_steps:
        findings.append("%s: no step runs a scenario" % name)
    for _, lines in scenario_steps:
        if not re.search(r"^[ \t]+HOST_E2E: '1'$", "\n".join(lines), re.M):
            findings.append("%s: a step that runs a scenario must set HOST_E2E: '1'" % name)
    jobs = dict(job_blocks(text))
    for job in ("linux", "windows"):
        if job not in jobs:
            findings.append("%s: the job %s is missing" % (name, job))
    # every job that runs a scenario has the timeouts of its steps below its own
    for job, block in job_blocks(text):
        job_steps = [(number, lines) for number, lines in read_steps(block) if (step_value(lines, "run") or "").startswith("scripts/host-e2e/")]
        if job_steps:
            check_timeouts(name, block, job_steps, findings, job)
    if text.count("HOST_E2E:") != len(scenario_steps):
        findings.append("%s: HOST_E2E is set somewhere else than on the steps that run a scenario" % name)
    for runner in ("ubuntu-24.04", "ubuntu-24.04-arm"):
        if runner not in jobs.get("linux", ""):
            findings.append("%s: the matrix of the job linux must contain %s" % (name, runner))
    windows = jobs.get("windows", "")
    if windows:
        if not re.search(r"^    runs-on: windows-\d{4}$", windows, re.M):
            findings.append("%s: the job windows runs on one GitHub-hosted runner with a fixed image (windows-NNNN, never windows-latest)" % name)
        if not re.search(r"^        shell: \[powershell, pwsh\]$", windows, re.M):
            findings.append("%s: the job windows must run in Windows PowerShell 5.1 and in PowerShell 7 (matrix shell: [powershell, pwsh])" % name)
        if not re.search(r"^[ \t]+run: scripts/host-e2e/windows\.ps1 one-command$", windows, re.M):
            findings.append("%s: the job windows must call the scenario by name: scripts/host-e2e/windows.ps1 one-command" % name)
        # GitHub allows an expression in the default shell of a job, not in the shell of a step
        default = re.search(r"^    defaults:\n      run:\n        shell: (.+)$", windows, re.M)
        job_shell = default.group(1).strip() if default else ""
        for _, lines in read_steps(windows):
            if (step_value(lines, "run") or "").startswith("scripts/host-e2e/"):
                shell = step_value(lines, "shell") or job_shell
                if shell != "${{ matrix.shell }}":
                    findings.append("%s: the scenario step of the job windows must run in the shell of the matrix (defaults.run.shell: ${{ matrix.shell }}, no shell on the step)" % name)
    if re.search(r"self-hosted", text):
        findings.append("%s: the job installs as root: never a self-hosted runner" % name)


def check_kind(text, root, findings):
    name = "e2e-kind.yml"
    if re.search(r"kindest/node|@sha256:", text):
        findings.append("%s: an image reference does not belong in the workflow: use a pin of hack/kind/versions.env (ci/check.sh holds it to a digest)" % name)
    pins = {}
    versions = os.path.join(root, "hack", "kind", "versions.env")
    if os.path.isfile(versions):
        with open(versions, encoding="utf-8") as f:
            for line in f:
                if "=" in line and not line.lstrip().startswith("#"):
                    key, _, value = line.rstrip("\n").partition("=")
                    pins[key] = value
    variables = re.findall(r"^[ \t]+image-var: (\S+)$", text, re.M)
    if not variables:
        findings.append("%s: the matrix names no image-var" % name)
    for v in variables:
        if not v.startswith("KIND_NODE_IMAGE"):
            findings.append("%s: image-var %s is not a node image pin (KIND_NODE_IMAGE...)" % (name, v))
        elif not re.search(r"@sha256:[0-9a-f]{64}$", pins.get(v, "")):
            findings.append("%s: image-var %s is no pin of hack/kind/versions.env, or it has no digest" % (name, v))
    if "make kind-probe" not in text:
        findings.append("%s: the probe runs through make kind-probe" % name)
    probe_steps = [(number, lines) for number, lines in read_steps(text) if any("make kind-probe" in line for line in step_block(lines, "run"))]
    check_timeouts(name, text, probe_steps, findings)
    filter_entries = set(re.findall(r"^[ \t]+- '([^']+)'$", text, re.M))
    for entry in KIND_PATHS:
        if entry not in filter_entries:
            findings.append("%s: the path filter of pull requests lacks `%s`, which the probe reads or runs" % (name, entry))
    if re.search(r"^[ \t]+HOST_E2E:", text, re.M):
        findings.append("%s: HOST_E2E belongs to the host test" % name)
    if "docker login" in text or "docker/login-action" in text:
        findings.append("%s: no registry login (the workflow has no secret)" % name)


def code_lines(text):
    """The text without the lines that hold only a comment (the checks look at the workflow, not at what its comments say)."""
    return "\n".join(line for line in text.split("\n") if not line.lstrip().startswith("#"))


def run(root):
    findings = []
    for name, policy in sorted(POLICY.items()):
        path = os.path.join(root, ".github", "workflows", name)
        if not os.path.isfile(path):
            findings.append("%s: the workflow does not exist" % name)
            continue
        with open(path, encoding="utf-8") as f:
            text = code_lines(f.read())
        check_common(name, text, findings)
        check_uploads(name, text, policy, findings)
        if name == "host-e2e.yml":
            check_host(text, findings)
        else:
            check_kind(text, root, findings)
    return findings


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--root", default=DEFAULT_ROOT, help="repository root (default: this repository)")
    args = parser.parse_args(argv)
    findings = run(args.root)
    for finding in findings:
        print(finding)
    if findings:
        return 1
    print("workflow-artifacts-check: %d workflow(s): all consistent" % len(POLICY))
    return 0


if __name__ == "__main__":
    sys.exit(main())
