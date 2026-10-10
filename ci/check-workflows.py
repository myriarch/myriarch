#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Policy check of the GitHub Actions workflows (ADR-0032), the part that actionlint does not cover.

    python3 -I ci/check-workflows.py [--root DIR] [--remote]

Scope: <root>/.github/workflows/*.yml and <root>/.github/actions/**/action.yml (root defaults to the repository).
The check is line based (standard library only, Python 3.9 compatible) and relies on the layout that actionlint
accepts: two-space indentation, one key per line. It prints one line per finding, "<file>:<line>: <rule>: <message>",
and exits with 1 when there is a finding, 2 on a usage error and 0 otherwise.

Rules
  R1  `uses:` is a local path (./...) or an action of the owners actions or github, pinned to a full commit SHA with
      the release in a trailing comment: `uses: actions/checkout@<40 hex> # v1.2.3`. No docker:// references, no
      tags, no branches, no other owners.
  R2  The same action is pinned to the same SHA and release everywhere.
  R3  Every workflow has a top-level `permissions:` with exactly `contents: read`. A job-level scope with `write`
      (or `write-all`) is allowed only when ci/workflow-permissions.txt lists "<workflow file> <job> <scope>".
  R4  No pull_request_target and no workflow_run trigger.
  R5  Every actions/checkout step sets `persist-credentials: false`.
  R6  Every job has `timeout-minutes` (a job that calls a reusable workflow is exempt).
  R7  No `${{ github.event.* }}`, `github.head_ref`, `github.ref_name` or `inputs.*` inside a `run:` text: pass the
      value through `env:` and read the variable (script injection).
  R8  Gate parity: the gates that ci.yml runs (the ONLY: values, plus e2e when ci.yml calls e2e.yml) are exactly the
      gates of `ci/check-local.sh --list`. A gate without a CI job fails.
  R9  Toolchains come from the repository: `node-version-file: .node-version` and `go-version-file: go.mod`, never a
      literal `node-version:` or `go-version:`.
  R10 No secrets: no `${{ secrets.* }}` expression, no `secrets:` key (`secrets: inherit` of a reusable workflow call).
      `github.token` and the default GITHUB_TOKEN are not secrets in this sense.
  R11 A workflow that has both `workflow_call` and `schedule` triggers: inside a called workflow `github.event_name` is the
      event of the caller, so an `if:` that tests `github.event_name == 'schedule'` must also test `inputs.<name> == ''`
      (true only for the workflow's own schedule run).

--remote (needs network, for updating pins): `git ls-remote` for every pinned action and a comparison of the SHA with
the tag that the comment names.

The environment variable CHECK_WORKFLOWS_GATES (space-separated) replaces the output of `ci/check-local.sh --list`
(tests only).
"""

import glob
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_ROOT = os.path.dirname(HERE)

USES = re.compile(r"^(\s*)(?:-\s+)?uses:\s*(\S+)(?:\s+(#.*))?\s*$")
USES_ANY = re.compile(r"^(\s*)(?:-\s+)?uses:")
PINNED = re.compile(r"^(actions|github)/([A-Za-z0-9._-]+)(/[A-Za-z0-9._/-]+)?@([0-9a-f]{40})$")
VERSION_COMMENT = re.compile(r"^#\s*(v\d+\.\d+\.\d+)\s*$")
EXPR_UNSAFE = re.compile(r"\$\{\{[^}]*\b(github\.event\.|github\.head_ref|github\.ref_name|inputs\.)")
SECRETS_EXPR = re.compile(r"\$\{\{[^}]*\bsecrets\b")
SECRETS_KEY = re.compile(r"^\s*(?:-\s+)?secrets\s*:")
TRIGGER_KEY = re.compile(r"^\s*(pull_request_target|workflow_run)\s*:")
TRIGGER_INLINE = re.compile(r"^on\s*:.*\b(pull_request_target|workflow_run)\b")


def indent_of(line):
    return len(line) - len(line.lstrip(" "))


def is_blank_or_comment(line):
    s = line.strip()
    return s == "" or s.startswith("#")


class Checker:
    def __init__(self, root):
        self.root = root
        self.findings = []
        self.pins = {}  # "owner/name" -> (sha, version, file, line)
        self.allow = self.read_allowlist()

    def add(self, path, lineno, rule, msg):
        rel = os.path.relpath(path, self.root)
        self.findings.append((rel, lineno, rule, msg))

    def read_allowlist(self):
        allow = set()
        p = os.path.join(self.root, "ci", "workflow-permissions.txt")
        if not os.path.isfile(p):
            return allow
        with open(p, encoding="utf-8") as f:
            for raw in f:
                s = raw.split("#", 1)[0].strip()
                if not s:
                    continue
                parts = s.split()
                if len(parts) == 3:
                    allow.add(tuple(parts))
        return allow

    # ---- per file -------------------------------------------------------------------------------------------
    def check_file(self, path, is_workflow):
        with open(path, encoding="utf-8") as f:
            lines = f.read().split("\n")
        self.rule_uses(path, lines)
        self.rule_triggers(path, lines)
        self.rule_checkout(path, lines)
        self.rule_run_injection(path, lines)
        self.rule_toolchains(path, lines)
        self.rule_secrets(path, lines)
        if is_workflow:
            self.rule_schedule_conditions(path, lines)
            self.rule_permissions(path, lines)
            self.rule_timeouts(path, lines)

    def rule_uses(self, path, lines):
        for i, line in enumerate(lines, 1):
            if is_blank_or_comment(line) or not USES_ANY.match(line):
                continue
            m = USES.match(line)
            if not m:
                self.add(path, i, "R1", "cannot parse the uses: line")
                continue
            ref, comment = m.group(2), m.group(3)
            if ref.startswith("./"):
                continue
            p = PINNED.match(ref)
            if not p:
                self.add(path, i, "R1", "uses: %s is not a local path or an actions/* or github/* action pinned to a full commit SHA" % ref)
                continue
            vm = VERSION_COMMENT.match(comment or "")
            if not vm:
                self.add(path, i, "R1", "uses: %s needs a trailing comment with the release, for example '# v1.2.3'" % ref)
                continue
            key = "%s/%s" % (p.group(1), p.group(2))
            sha, ver = p.group(4), vm.group(1)
            if key in self.pins:
                psha, pver, pfile, pline = self.pins[key]
                if (psha, pver) != (sha, ver):
                    self.add(path, i, "R2", "%s is pinned to %s (%s) here but to %s (%s) in %s:%d" % (key, sha[:12], ver, psha[:12], pver, pfile, pline))
            else:
                self.pins[key] = (sha, ver, os.path.relpath(path, self.root), i)

    def rule_triggers(self, path, lines):
        for i, line in enumerate(lines, 1):
            if is_blank_or_comment(line):
                continue
            if TRIGGER_KEY.match(line) or TRIGGER_INLINE.match(line):
                self.add(path, i, "R4", "the trigger %s is forbidden" % re.search(r"pull_request_target|workflow_run", line).group(0))

    def step_bounds(self, lines, idx):
        """Line range [lo, hi) of the step that contains the key on line idx."""
        k = indent_of(lines[idx])
        if lines[idx].lstrip().startswith("- "):
            k += 2
        lo = idx
        while lo > 0:
            prev = lines[lo - 1]
            if is_blank_or_comment(prev):
                lo -= 1
                continue
            if indent_of(prev) < k or (indent_of(prev) == k - 2 and prev.lstrip().startswith("- ")):
                break
            lo -= 1
        # the step starts at its "- " line, which belongs to it
        if lo > 0 and indent_of(lines[lo - 1]) == k - 2 and lines[lo - 1].lstrip().startswith("- "):
            lo -= 1
        hi = idx + 1
        while hi < len(lines):
            cur = lines[hi]
            if is_blank_or_comment(cur):
                hi += 1
                continue
            if indent_of(cur) < k or (indent_of(cur) == k - 2 and cur.lstrip().startswith("- ")):
                break
            hi += 1
        return lo, hi

    def rule_checkout(self, path, lines):
        for i, line in enumerate(lines):
            m = USES.match(line)
            if not m or not m.group(2).startswith("actions/checkout@"):
                continue
            lo, hi = self.step_bounds(lines, i)
            block = [x for x in lines[lo:hi] if not x.lstrip().startswith("#")]
            if not any(re.match(r"^\s*persist-credentials:\s*false\s*(#.*)?$", x) for x in block):
                self.add(path, i + 1, "R5", "actions/checkout needs 'persist-credentials: false'")

    def rule_run_injection(self, path, lines):
        i = 0
        while i < len(lines):
            line = lines[i]
            m = re.match(r"^(\s*)(?:-\s+)?run:\s*(.*)$", line)
            if not m or is_blank_or_comment(line):
                i += 1
                continue
            value = m.group(2)
            key_indent = len(m.group(1)) + (2 if line.lstrip().startswith("- ") else 0)
            texts = [(i + 1, value)]
            if value.startswith(("|", ">")):
                j = i + 1
                while j < len(lines) and (lines[j].strip() == "" or indent_of(lines[j]) > key_indent):
                    texts.append((j + 1, lines[j]))
                    j += 1
                i = j
            else:
                i += 1
            for no, text in texts:
                if EXPR_UNSAFE.search(text):
                    self.add(path, no, "R7", "an untrusted expression inside run: pass it through env: instead")

    def rule_toolchains(self, path, lines):
        for i, line in enumerate(lines, 1):
            if is_blank_or_comment(line):
                continue
            m = re.match(r"^\s*(node-version|go-version)\s*:", line)
            if m:
                self.add(path, i, "R9", "a literal %s is forbidden: use %s-file" % (m.group(1), m.group(1)))
            m = re.match(r"^\s*node-version-file\s*:\s*(\S+)", line)
            if m and m.group(1).strip("'\"") != ".node-version":
                self.add(path, i, "R9", "node-version-file must be .node-version")
            m = re.match(r"^\s*go-version-file\s*:\s*(\S+)", line)
            if m and m.group(1).strip("'\"") != "go.mod":
                self.add(path, i, "R9", "go-version-file must be go.mod")

    def rule_permissions(self, path, lines):
        top = None
        for i, line in enumerate(lines):
            if re.match(r"^permissions\s*:", line):
                top = i
                break
        if top is None:
            self.add(path, 1, "R3", "the workflow has no top-level permissions: (contents: read)")
        else:
            entries = []
            j = top + 1
            inline = re.sub(r"^permissions\s*:\s*", "", lines[top]).split("#", 1)[0].strip()
            while j < len(lines) and (is_blank_or_comment(lines[j]) or indent_of(lines[j]) > 0):
                if not is_blank_or_comment(lines[j]):
                    entries.append(lines[j].split("#", 1)[0].strip())
                j += 1
            if inline or entries != ["contents: read"]:
                self.add(path, top + 1, "R3", "the top-level permissions must be exactly 'contents: read'")
        # job level
        jobs = self.jobs(lines)
        wf = os.path.basename(path)
        for name, (start, end) in jobs.items():
            i = start + 1
            while i < end:
                line = lines[i]
                m = re.match(r"^    permissions\s*:\s*(.*)$", line)
                if m and not is_blank_or_comment(line):
                    inline = m.group(1).split("#", 1)[0].strip()
                    if inline:
                        self.check_inline_permissions(path, i + 1, wf, name, inline)
                    else:
                        j = i + 1
                        while j < end and (is_blank_or_comment(lines[j]) or indent_of(lines[j]) >= 6):
                            mm = re.match(r"^\s{6}([A-Za-z-]+)\s*:\s*(\S+)", lines[j])
                            if mm and mm.group(2) == "write" and (wf, name, mm.group(1)) not in self.allow:
                                self.add(path, j + 1, "R3", "job %s: '%s: write' is not listed in ci/workflow-permissions.txt (%s %s %s)" % (name, mm.group(1), wf, name, mm.group(1)))
                            j += 1
                i += 1

    def check_inline_permissions(self, path, lineno, wf, job, value):
        """Job-level `permissions:` with a value on the same line: `read-all`, `{}` or a one-line flow mapping."""
        if value in ("read-all", "{}"):
            return
        if value == "write-all":
            self.add(path, lineno, "R3", "job %s: write-all is forbidden" % job)
            return
        m = re.match(r"^\{(.*)\}$", value)
        if not m:
            self.add(path, lineno, "R3", "job %s: cannot parse the permissions value '%s' (use block form or a one-line mapping)" % (job, value))
            return
        for item in m.group(1).split(","):
            item = item.strip()
            if not item:
                continue
            kv = re.match(r"^['\"]?([A-Za-z-]+)['\"]?\s*:\s*['\"]?([A-Za-z-]+)['\"]?$", item)
            if not kv:
                self.add(path, lineno, "R3", "job %s: cannot parse the permissions entry '%s'" % (job, item))
            elif kv.group(2) == "write" and (wf, job, kv.group(1)) not in self.allow:
                self.add(path, lineno, "R3", "job %s: '%s: write' is not listed in ci/workflow-permissions.txt (%s %s %s)" % (job, kv.group(1), wf, job, kv.group(1)))
            elif kv.group(2) not in ("read", "write", "none"):
                self.add(path, lineno, "R3", "job %s: unknown permission level '%s'" % (job, kv.group(2)))

    def rule_secrets(self, path, lines):
        for i, line in enumerate(lines, 1):
            if is_blank_or_comment(line):
                continue
            code = line.split(" #", 1)[0]
            if SECRETS_EXPR.search(code):
                self.add(path, i, "R10", "a secrets expression is forbidden: the workflows run without secrets")
            elif SECRETS_KEY.match(code):
                self.add(path, i, "R10", "a secrets: key (secrets: inherit or a mapping) is forbidden")

    def rule_schedule_conditions(self, path, lines):
        text = "\n".join(lines)
        if not (re.search(r"^  workflow_call\s*:", text, re.M) and re.search(r"^  schedule\s*:", text, re.M)):
            return
        for i, line in enumerate(lines, 1):
            if is_blank_or_comment(line):
                continue
            if re.match(r"^\s*(?:-\s+)?if\s*:", line) and "github.event_name == 'schedule'" in line and not re.search(r"inputs\.[A-Za-z_-]+ == ''", line):
                self.add(path, i, "R11", "in a workflow with workflow_call and schedule triggers, a schedule condition must also test inputs.<name> == ''")

    def jobs(self, lines):
        """job name -> (first line index, end line index exclusive)."""
        start = None
        for i, line in enumerate(lines):
            if re.match(r"^jobs\s*:\s*(#.*)?$", line):
                start = i + 1
                break
        if start is None:
            return {}
        end = len(lines)
        for i in range(start, len(lines)):
            if not is_blank_or_comment(lines[i]) and indent_of(lines[i]) == 0:
                end = i
                break
        result = {}
        names = []
        for i in range(start, end):
            m = re.match(r"^  ([A-Za-z0-9_-]+)\s*:\s*(#.*)?$", lines[i])
            if m:
                names.append((m.group(1), i))
        for n, (name, i) in enumerate(names):
            nxt = names[n + 1][1] if n + 1 < len(names) else end
            result[name] = (i, nxt)
        return result

    def rule_timeouts(self, path, lines):
        for name, (start, end) in self.jobs(lines).items():
            block = lines[start + 1:end]
            if any(re.match(r"^    uses\s*:", x) for x in block):
                continue
            if not any(re.match(r"^    timeout-minutes\s*:", x) for x in block):
                self.add(path, start + 1, "R6", "job %s has no timeout-minutes" % name)

    # ---- repository level -------------------------------------------------------------------------------------
    def rule_parity(self, ci_path):
        with open(ci_path, encoding="utf-8") as f:
            lines = f.read().split("\n")
        used = set()
        calls_e2e = False
        for line in lines:
            if is_blank_or_comment(line):
                continue
            m = re.match(r"^\s*ONLY\s*:\s*(.+?)\s*$", line)
            if m:
                value = m.group(1).strip("'\"")
                for g in value.split(","):
                    if g.strip():
                        used.add(g.strip())
            if re.match(r"^\s*uses\s*:\s*\./\.github/workflows/e2e\.yml\s*$", line):
                calls_e2e = True
        if calls_e2e:
            used.add("e2e")
        gates = os.environ.get("CHECK_WORKFLOWS_GATES")
        if gates is not None:
            want = set(gates.split())
        else:
            script = os.path.join(self.root, "ci", "check-local.sh")
            try:
                out = subprocess.run(["bash", script, "--list"], check=True, capture_output=True, text=True).stdout
            except (OSError, subprocess.CalledProcessError) as e:
                self.add(ci_path, 1, "R8", "cannot list the gates: %s" % e)
                return
            want = set(out.split())
        for g in sorted(want - used):
            self.add(ci_path, 1, "R8", "the gate '%s' of ci/check-local.sh has no job in ci.yml" % g)
        for g in sorted(used - want):
            self.add(ci_path, 1, "R8", "ci.yml runs '%s', which is not a gate of ci/check-local.sh" % g)

    def remote(self):
        status = 0
        for key, (sha, ver, f, line) in sorted(self.pins.items()):
            url = "https://github.com/%s.git" % key
            try:
                out = subprocess.run(["git", "ls-remote", "--tags", url, "refs/tags/" + ver, "refs/tags/" + ver + "^{}"],
                                     check=True, capture_output=True, text=True).stdout
            except (OSError, subprocess.CalledProcessError) as e:
                print("%s:%d: remote: cannot resolve %s %s: %s" % (f, line, key, ver, e))
                status = 1
                continue
            refs = {}
            for row in out.splitlines():
                parts = row.split()
                if len(parts) == 2:
                    refs[parts[1]] = parts[0]
            got = refs.get("refs/tags/" + ver + "^{}") or refs.get("refs/tags/" + ver)
            if got != sha:
                print("%s:%d: remote: %s %s resolves to %s, pinned %s" % (f, line, key, ver, got, sha))
                status = 1
        return status


def main(argv):
    root = DEFAULT_ROOT
    remote = False
    args = list(argv)
    while args:
        a = args.pop(0)
        if a == "--root" and args:
            root = os.path.abspath(args.pop(0))
        elif a == "--remote":
            remote = True
        elif a in ("-h", "--help"):
            print(__doc__)
            return 0
        else:
            sys.stderr.write("usage: check-workflows.py [--root DIR] [--remote]\n")
            return 2
    wf_dir = os.path.join(root, ".github", "workflows")
    workflows = sorted(glob.glob(os.path.join(wf_dir, "*.yml")))
    actions = sorted(glob.glob(os.path.join(root, ".github", "actions", "**", "action.yml"), recursive=True))
    if not workflows:
        sys.stderr.write("check-workflows: no workflow in %s\n" % wf_dir)
        return 2
    c = Checker(root)
    for p in workflows:
        c.check_file(p, True)
    for p in actions:
        c.check_file(p, False)
    ci = os.path.join(wf_dir, "ci.yml")
    if os.path.isfile(ci):
        c.rule_parity(ci)
    for rel, line, rule, msg in sorted(c.findings):
        print("%s:%d: %s: %s" % (rel, line, rule, msg))
    status = 1 if c.findings else 0
    if remote:
        status = c.remote() or status
    if status == 0:
        print("check-workflows: %d workflow(s), %d action(s): all consistent" % (len(workflows), len(actions)))
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
