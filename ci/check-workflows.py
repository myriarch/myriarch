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
  R10 Secrets only for listed deployment jobs. A `${{ secrets.NAME }}` expression is allowed when all three hold:
      (1) ci/workflow-secrets.txt lists "<workflow file> <job> <NAME>"; (2) the job declares `environment:` at job level
      (a name or a mapping with `name:`), so that the job waits for the protection rules of the environment; (3) the
      expression is the whole value of a key directly inside an `env:` mapping of that job or of one of its steps:
      `KEY: ${{ secrets.NAME }}` (NAME of [A-Za-z_][A-Za-z0-9_]*, optional spaces inside the braces). Everything else
      fails, also inside a listed job: a secrets expression in `run:` text, `with:`, `if:`, the workflow-level `env:`, the
      `env:` of a service or container, concatenated with other text, in a flow mapping or a block scalar, in index syntax
      (`secrets['NAME']`), as the argument of a function (`toJSON(secrets)`), in any letter case, and a `secrets:` key
      (`secrets: inherit`, the mapping of a reusable workflow call). A stale line of the list (no such workflow file or
      job, or a job that does not read the secret), a malformed line and a duplicate line fail too. `github.token` is not
      a secrets expression and needs no entry (`${{ secrets.GITHUB_TOKEN }}` is one: write `github.token`). The rule
      reads the text of the file: a YAML alias (`*name`) in a workflow with a secrets expression fails, because it could
      copy the secret into a job that is not listed, and YAML escapes that change what an expression is are out of reach.
  R11 A workflow that has both `workflow_call` and `schedule` triggers: inside a called workflow `github.event_name` is the
      event of the caller, so an `if:` that tests `github.event_name == 'schedule'` must also test `inputs.<name> == ''`
      (true only for the workflow's own schedule run).

--remote (needs network, for updating pins): `git ls-remote` for every pinned action and a comparison of the SHA with
the tag that the comment names.

The environment variable CHECK_WORKFLOWS_GATES (space-separated) replaces the output of `ci/check-local.sh --list`
(tests only).
"""

import bisect
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
SECRETS_WORD = re.compile(r"\bsecrets\b", re.I)  # GitHub resolves context names without regard to case
SECRETS_KEY = re.compile(r"""(?:^\s*(?:-\s+)*|[{,]\s*)["']?secrets["']?\s*:""", re.I)  # also in a flow mapping
SECRETS_ENTRY = re.compile(r"^ *[A-Za-z_][A-Za-z0-9_]*: +(\$\{\{ *secrets\.([A-Za-z_][A-Za-z0-9_]*) *\}\})(?:[ \t]+#.*|[ \t]*)$")
MAPPING_KEY = re.compile(r"""^(?:([A-Za-z0-9_][A-Za-z0-9_-]*)|"([^"]*)"|'([^']*)') *:(?: +(.*)|$)""")
BLOCK_SCALAR = re.compile(r"^[|>](?:[-+][0-9]?|[0-9][-+]?)? *(?:#.*)?$")
NODE_PROPERTIES = re.compile(r"^(?:[&!]\S*(?: +|$))+")  # anchors and tags in front of a value
ALLOW_WORKFLOW = re.compile(r"^[A-Za-z0-9._-]+\.yml$")
ALLOW_JOB = re.compile(r"^[A-Za-z0-9_-]+$")
ALLOW_SECRET = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
TRIGGER_KEY = re.compile(r"^\s*(pull_request_target|workflow_run)\s*:")
TRIGGER_INLINE = re.compile(r"^on\s*:.*\b(pull_request_target|workflow_run)\b")


def indent_of(line):
    return len(line) - len(line.lstrip(" "))


def is_blank_or_comment(line):
    s = line.strip()
    return s == "" or s.startswith("#")


def expressions(text):
    """Yield (offset, code) for every `${{ ... }}` of the text: the offset of the `${{` and the expression with the content
    of its string literals blanked out. An expression may span lines, and a `}}` inside a literal ('...', with '' for a
    quote) does not end it."""
    pos = 0
    while True:
        start = text.find("${{", pos)
        if start < 0:
            return
        code = []
        quote = False
        i = start + 3
        while i < len(text):
            c = text[i]
            if quote:
                if c == "'" and text[i + 1:i + 2] == "'":  # an escaped quote
                    code.append("  ")
                    i += 2
                    continue
                if c == "'":
                    quote = False
                code.append(c if c in "'\n" else " ")
            elif c == "'":
                quote = True
                code.append(c)
            elif text.startswith("}}", i):
                break
            else:
                code.append(c)
            i += 1
        yield start, "".join(code)
        pos = i + 2


def scan_flow(text, depth, quote):
    """Follow the brackets and quotes of a flow collection or a quoted scalar through one more line. Returns the new
    (depth, quote); (0, "") means that the collection or the scalar is closed."""
    i = 0
    while i < len(text):
        c = text[i]
        if quote:
            if quote == '"' and c == "\\":
                i += 1
            elif c == quote:
                if quote == "'" and text[i + 1:i + 2] == "'":
                    i += 1
                else:
                    quote = ""
        elif c in "\"'":
            quote = c
        elif c in "{[":
            depth += 1
        elif c in "}]":
            depth = max(depth - 1, 0)
        elif c == "#" and (i == 0 or text[i - 1] in " \t"):
            break
        i += 1
        if depth == 0 and not quote:
            break  # what follows the closing bracket or quote is a comment
    return depth, quote


def pop_parents(stack, indent, is_item):
    """Close the open parents at or beyond `indent`. A list item that sits at the indent of its key belongs to that key."""
    while stack and (stack[-1][0] > indent or (stack[-1][0] == indent and (stack[-1][1] == "-" or not is_item))):
        stack.pop()


def scalar_text(value):
    """The text of a one-line scalar without quotes and comment; empty for an empty or null value."""
    v = value.strip()
    if v[:1] in ("'", '"'):
        end = v.find(v[0], 1)
        v = v[1:end] if end > 0 else v[1:]
    else:
        v = re.split(r"(?:^|\s)#", v, maxsplit=1)[0].strip()
    return "" if v.lower() in ("", "null", "~") else v


def jobs_with_environment(where):
    """The ids of the jobs that declare `environment:` at job level: a name, or a mapping with a name."""
    found = set()
    for w in where:
        if w is None or w[0][:1] != ("jobs",):
            continue
        anc, key, value = w
        if len(anc) == 2 and key == "environment":
            if value.startswith("{"):
                if re.search(r"[{,] *['\"]?name['\"]? *: *[^\s,}]", value):
                    found.add(anc[1])
            elif scalar_text(value):
                found.add(anc[1])
        elif len(anc) == 3 and anc[2] == "environment" and key == "name" and scalar_text(value):
            found.add(anc[1])
    return found


class Checker:
    def __init__(self, root):
        self.root = root
        self.findings = []
        self.pins = {}  # "owner/name" -> (sha, version, file, line)
        self.allow = self.read_allowlist()
        self.secrets_file = os.path.join(self.root, "ci", "workflow-secrets.txt")
        self.secret_entries = self.read_secret_allowlist()  # (workflow file, job, NAME) -> line in ci/workflow-secrets.txt
        self.secret_uses = set()  # (workflow file, job, NAME) of every `KEY: ${{ secrets.NAME }}` in an env: of a job or a step
        self.job_ids = {}  # workflow file -> ids of its jobs

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

    def read_secret_allowlist(self):
        """ci/workflow-secrets.txt (R10): "<workflow file> <job> <NAME>" per line, '#' starts a comment, like R3's list. A
        malformed or a duplicate line is a finding of its own."""
        entries = {}
        p = self.secrets_file
        if not os.path.isfile(p):
            return entries
        with open(p, encoding="utf-8") as f:
            for no, raw in enumerate(f, 1):
                s = raw.split("#", 1)[0].strip()
                if not s:
                    continue
                parts = s.split()
                line = " ".join(parts)
                if len(parts) != 3 or not (ALLOW_WORKFLOW.match(parts[0]) and ALLOW_JOB.match(parts[1]) and ALLOW_SECRET.match(parts[2])):
                    self.add(p, no, "R10", "malformed line '%s' in ci/workflow-secrets.txt (expected '<workflow file> <job> <NAME>')" % line)
                elif tuple(parts) in entries:
                    self.add(p, no, "R10", "'%s' is listed again in ci/workflow-secrets.txt (first on line %d)" % (line, entries[tuple(parts)]))
                else:
                    entries[tuple(parts)] = no
        return entries

    # ---- per file -------------------------------------------------------------------------------------------
    def check_file(self, path, is_workflow):
        with open(path, encoding="utf-8") as f:
            lines = f.read().split("\n")
        self.rule_uses(path, lines)
        self.rule_triggers(path, lines)
        self.rule_checkout(path, lines)
        self.rule_run_injection(path, lines)
        self.rule_toolchains(path, lines)
        self.rule_secrets(path, lines, is_workflow)
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

    def structure(self, lines):
        """Where every line stands in the block structure of the file (two-space indentation, one key per line).

        Returns (where, code). where[i] is None for a line that is not part of the structure (blank, comment, the content of a
        block scalar, the continuation of a quoted scalar or of a flow collection) and otherwise (ancestors, key, value): the
        keys above the line ('-' stands for a list item, for example ('jobs', 'deploy', 'steps', '-', 'env')), the key of the
        line and the text after its colon. code[i] is the text of the line that can hold an expression: empty for a blank line
        or a comment, and without the trailing comment where the value is a plain scalar (the one case in which `#` surely
        starts a comment; the content of a block scalar or a quoted scalar is code as a whole)."""
        where = [None] * len(lines)
        code = [""] * len(lines)
        stack = []  # (indent, name) of the open parents: a key without a value of its own, or '-' for a list item
        block = None  # indent of the key whose block scalar is open
        flow = (0, "")  # open brackets and quote of a flow collection or quoted scalar that spans lines
        for i, line in enumerate(lines):
            if line.strip() == "":
                continue
            if flow != (0, ""):
                flow = scan_flow(line, *flow)
                code[i] = line
                continue
            ind = indent_of(line)
            if block is not None:
                if ind > block:
                    code[i] = line
                    continue
                block = None
            if line.lstrip().startswith("#"):
                continue
            code[i] = line
            body, k, item = line[ind:], ind, None
            m = re.match(r"- +|-$", body)
            while m:  # one or more list item markers
                pop_parents(stack, k, True)
                stack.append((k, "-"))
                item = k
                body, k = body[len(m.group(0)):], k + len(m.group(0))
                m = re.match(r"- +|-$", body)
            km = MAPPING_KEY.match(body)
            raw, at = body, len(line) - len(body)  # the value and where it starts in the line
            if km:
                raw = km.group(4) or ""
                at += km.start(4) if km.group(4) is not None else km.end()
            value = NODE_PROPERTIES.sub("", raw.rstrip())
            if value[:1] not in ("'", '"', "{", "[", "|", ">"):  # a plain scalar: ' #' starts a comment
                comment = re.search(r"(?:^|\s)#", raw)
                if comment:
                    code[i] = line[:at + comment.start()]
            if km:
                key = next(g for g in km.groups()[:3] if g is not None)
                pop_parents(stack, k, False)
                ancestors = tuple(name for _, name in stack)
                if value == "" or value.startswith("#"):
                    stack.append((k, key))
                elif BLOCK_SCALAR.match(value):
                    block = k
            else:
                key = None
                ancestors = tuple(name for _, name in stack)
                if item is not None and BLOCK_SCALAR.match(value):
                    block = item
            where[i] = (ancestors, key, value)
            if value[:1] in ("'", '"', "{", "["):
                flow = scan_flow(value, 0, "")
        return where, code

    def rule_secrets(self, path, lines, is_workflow):
        wf = os.path.basename(path)
        where, code = self.structure(lines)
        if is_workflow:
            self.job_ids[wf] = set(w[1] for w in where if w is not None and w[0] == ("jobs",) and w[1])
        declared = jobs_with_environment(where)
        # `secrets:` keys: `secrets: inherit` or the mapping of a reusable workflow call
        for i, row in enumerate(code):
            if SECRETS_KEY.search(row):
                self.add(path, i + 1, "R10", "a secrets: key (secrets: inherit, the secrets of a reusable workflow call or of a workflow_call trigger) is forbidden")
        # expressions: found in the text of the whole file, because they may span lines
        text = "\n".join(code)
        starts = [0]
        for line in code:
            starts.append(starts[-1] + len(line) + 1)
        found = False
        for start, expr in expressions(text):
            word = SECRETS_WORD.search(expr)
            if not word:
                continue
            found = True
            first = bisect.bisect_right(starts, start) - 1  # the line of the `${{`
            lineno = first + expr.count("\n", 0, word.start()) + 1
            entry = SECRETS_ENTRY.match(lines[first])
            job = None
            if is_workflow and entry and starts[first] + entry.start(1) == start and where[first] is not None:
                ancestors = where[first][0]
                if len(ancestors) >= 3 and ancestors[0] == "jobs" and ancestors[2:] in (("env",), ("steps", "-", "env")):
                    job = ancestors[1]
            if job is None:
                self.add(path, lineno, "R10", "a secrets expression is allowed only as the whole value of a key in the env: of a job or of a step ('KEY: ${{ secrets.NAME }}')")
                continue
            name = entry.group(2)
            self.secret_uses.add((wf, job, name))
            if (wf, job, name) not in self.secret_entries:
                self.add(path, lineno, "R10", "job %s: secrets.%s is not listed in ci/workflow-secrets.txt (%s %s %s)" % (job, name, wf, job, name))
            if job not in declared:
                self.add(path, lineno, "R10", "job %s: secrets.%s needs 'environment:' at job level, so that the protection rules of the environment guard the job" % (job, name))
        if found:
            for i, w in enumerate(where):
                if w is not None and w[2].startswith("*"):
                    self.add(path, i + 1, "R10", "an alias (*name) in a workflow with a secrets expression could copy the secret into a job that is not listed")

    def rule_secret_entries(self):
        """Stale entries of ci/workflow-secrets.txt: the workflow file, the job or the use of the secret is gone."""
        for (wf, job, name), no in sorted(self.secret_entries.items(), key=lambda item: item[1]):
            if wf not in self.job_ids:
                msg = "stale entry: the workflow file .github/workflows/%s does not exist" % wf
            elif job not in self.job_ids[wf]:
                msg = "stale entry: %s has no job %s" % (wf, job)
            elif (wf, job, name) not in self.secret_uses:
                msg = "stale entry: job %s of %s does not read secrets.%s in an env: of the job or of a step" % (job, wf, name)
            else:
                continue
            self.add(self.secrets_file, no, "R10", msg)

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
    c.rule_secret_entries()
    for rel, line, rule, msg in sorted(set(c.findings)):
        print("%s:%d: %s: %s" % (rel, line, rule, msg))
    status = 1 if c.findings else 0
    if remote:
        status = c.remote() or status
    if status == 0:
        print("check-workflows: %d workflow(s), %d action(s): all consistent" % (len(workflows), len(actions)))
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
