#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Policy check of the GitHub Actions workflows (ADR-0032 and ADR-0033), the part that actionlint does not cover.

    python3 -I ci/check-workflows.py [--root DIR] [--remote]

Scope: the workflow files in <root>/.github/workflows and the action files below <root>/.github/actions (root defaults
to the repository). GitHub runs a workflow with the extension .yml or .yaml and reads a local action from action.yml or
action.yaml, so the check reads both spellings (in any letter case, hidden names included). The check is line based
(standard library only, Python 3.9 compatible) and relies on the layout that actionlint accepts: two-space indentation,
one key per line. It prints one line per finding, "<file>:<line>: <rule>: <message>", and exits with 1 when there is a
finding, 2 on a usage error and 0 otherwise.

Rules
  R1  `uses:` is a local path (./...) or an action of the owners actions or github, pinned to a full commit SHA with
      the release in a trailing comment: `uses: actions/checkout@<40 hex> # v1.2.3`. No docker:// references, no
      tags, no branches, no other owners. The one exception (ADR-0033) is ossf/scorecard-action, pinned the same way,
      in .github/workflows/scorecard.yml, job `analysis`, and nowhere else (not in another job, another workflow or a
      local action; the action has no subpath).
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
      (`secrets: inherit`, the mapping of a reusable workflow call), also in a flow mapping that spans lines. A key is
      looked for where YAML puts keys: the word in the text of a block scalar (a `run:` script that writes a manifest with
      `secrets:`) or of a quoted scalar is no key. A stale line of the list (no such workflow file or job, or a job that
      does not read the secret), a malformed line and a duplicate line fail too. `github.token` is not a secrets
      expression and needs no entry (`${{ secrets.GITHUB_TOKEN }}` is one: write `github.token`). The rule reads the text
      of the file: a YAML alias (`*name`) in a workflow with a secrets expression fails, because it could copy the secret
      into a job that is not listed, and YAML escapes that change what an expression is are out of reach.
  R11 A workflow that has both `workflow_call` and `schedule` triggers: inside a called workflow `github.event_name` is the
      event of the caller, so an `if:` that tests `github.event_name == 'schedule'` must also test `inputs.<name> == ''`
      (true only for the workflow's own schedule run).
  R12 .github/workflows/scorecard.yml has the shape that the OpenSSF Scorecard API accepts for published results, and
      no more (ADR-0033). Top level: only name, on, permissions and jobs; no env and no defaults. Triggers: push with
      `branches: [main]` and schedule (a list of `- cron:` entries), nothing else. Exactly one job, `analysis`, with
      only the keys name, runs-on, timeout-minutes, permissions and steps (so no env, defaults, container, services,
      environment, strategy or needs), on one GitHub-hosted Ubuntu runner (ubuntu-NN.NN, 22.04 or later), with exactly
      the permissions contents: read, actions: read, security-events: write and id-token: write. Every step has
      `uses:` and only the keys name, id, uses, with and if (no run, shell or env); the actions are actions/checkout,
      ossf/scorecard-action, github/codeql-action/upload-sarif and optionally actions/upload-artifact, and the first
      three must all be there. The Scorecard step sets `publish_results: true`. No secrets expression anywhere. The
      workflow-level `contents: read` is R3's. The restrictions come from the Scorecard API (README of
      ossf/scorecard-action, "Workflow Restrictions", and verify_workflow.go in ossf/scorecard-infra); this rule is
      stricter where it can be (no harden-runner, no ubuntu-latest, no -arm runners, a fixed set of permissions).
  R13 .github/workflows/release.yml (and release.yaml) keeps build code away from the privileges (ADR-0033). Trigger:
      only `push` with `tags:` whose patterns start with v; no branches, pull_request or workflow_dispatch. No caches:
      no actions/cache, no `cache:` input other than `false`, `cache: false` on actions/setup-go and
      `package-manager-cache: false` on actions/setup-node (both cache by default), and no local action or reusable
      workflow (`uses: ./...`), which could restore one. An `environment:` is the literal `release`, never an
      expression. A privileged job (any write scope, `write-all`, id-token or attestations, or an environment) runs
      neither actions/setup-node nor actions/setup-go, and no `run:` text of it starts one of pnpm, pnpx, npm, npx,
      corepack, node, nodejs, yarn, go, goreleaser, make or gmake. A command counts at the start of a simple command: at
      the beginning of a line or after `|`, `;`, `&`, `&&`, `||`, a parenthesis, `then`, `do` and similar, behind
      assignments and wrappers (`sudo`, `doas`, `env` also with `-S`, `exec`, `time`, `builtin`, `coproc`, `timeout 5`,
      ...), inside `$(...)`, backticks, `sh -c '...'`, `eval` and `find -exec`; words in comments, quoted strings,
      here-documents, the patterns of a `case` clause, the members of an array (`tools=(go node)`) and arguments
      (`echo go`, `command -v go`) are no commands. A shell that reads its script from standard input (`bash <<EOF`,
      `bash <<< text`, `echo ... | sh`, `bash -s`, `. /dev/stdin`) is a finding in a privileged job, because its script
      is not in the run text. A `shell:` (of a step in a privileged job, of the `defaults: run:` of a privileged job, and of
      the workflow `defaults: run:`) is bash or sh: another interpreter (`node {0}`, `python {0}`) would read the run
      text as its own language. A program started through a variable, an interpreter that is not a shell or a script is
      out of reach: the scripts that privileged jobs call are tested with a PATH that has no go, node or pnpm. The job
      `guard` exists, every other job needs it (directly or through other jobs), and every privileged job either has the
      environment release itself or needs a job that has it, so that an approval gates it. Two constructs would make
      `needs:` ineffective and are findings in every job: an `if:` that calls always(), failure(), cancelled() or
      success() (the job runs although a job in `needs:` failed or was not approved) and `continue-on-error` other than
      `false`, at job and at step level (a failed guard, approval or verification counts as passed). R12 and R13 read the
      YAML with a small reader for block style (anchors, aliases, tags, multi-line flow collections and multi-line
      scalars are findings of their own).
  R14 zizmor stays strict (ADR-0032). zizmor.yml in the repository root holds ignore entries and nothing else: the only
      top-level key is `rules`, each audit under it has only the key `ignore` (so no `disable`, no `config` and no other
      key) with a list of workflow or action files, optionally with `:line` or `:line:column`, no wildcards, and a comment
      line directly above the audit that gives the reason. An inline ignore in a file below .github needs the rule names
      in brackets (`# zizmor: ignore[github-env]`, never a bare `# zizmor: ignore`) and a reason: a comment line directly
      above it, or text behind the closing bracket. The rule reads the configuration with the reader of R12 and R13.
      The files that zizmor reads (zizmor.yml, the workflows, the local actions and .github/dependabot.yml or .yaml) must
      read the same for this check and for a YAML parser: a file that is not UTF-8 is a finding, and so is a file with
      U+0085, U+2028 or U+2029 (line breaks for a parser, zizmor's reader of zizmor.yml included, but not for this check,
      which splits at LF and CR), a control character other than TAB, LF and CR, DEL, a C1 control character or a byte order
      mark. The rules R1 to R14 read lines, so every one of them depends on this.

--remote (needs network, for updating pins): `git ls-remote` for every pinned action and a comparison of the SHA with
the tag that the comment names.

The environment variable CHECK_WORKFLOWS_GATES (space-separated) replaces the output of `ci/check-local.sh --list`
(tests only).
"""

import bisect
import os
import re
import shlex
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_ROOT = os.path.dirname(HERE)

USES = re.compile(r"^(\s*)(?:-\s+)?uses:\s*(\S+)(?:\s+(#.*))?\s*$")
USES_ANY = re.compile(r"^(\s*)(?:-\s+)?uses:")
PINNED = re.compile(r"^(actions|github)/([A-Za-z0-9._-]+)(/[A-Za-z0-9._/-]+)?@([0-9a-f]{40})$")
PINNED_ANY = re.compile(r"^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)(/[A-Za-z0-9._/-]+)?@([0-9a-f]{40})$")
# The actions of another owner than actions and github (ADR-0033): "owner/name" -> (workflow file, job) in which it may appear.
THIRD_PARTY_ACTIONS = {"ossf/scorecard-action": ("scorecard.yml", "analysis")}
# R12: the workflow and the job of the Scorecard analysis
SCORECARD_STEP_ACTIONS = ("actions/checkout", "ossf/scorecard-action", "github/codeql-action/upload-sarif", "actions/upload-artifact")
SCORECARD_REQUIRED = ("actions/checkout", "ossf/scorecard-action", "github/codeql-action/upload-sarif")
SCORECARD_PERMISSIONS = {"contents": "read", "actions": "read", "security-events": "write", "id-token": "write"}
UBUNTU_RUNNER = re.compile(r"^ubuntu-(\d{2})\.(\d{2})$")
# R13: programs that build code; a job with a write scope, id-token or the environment release does not start them
RELEASE_FORBIDDEN_COMMANDS = frozenset(["pnpm", "pnpx", "npm", "npx", "corepack", "node", "nodejs", "yarn", "go", "goreleaser", "make", "gmake"])
# R13: a function that tests the status of other jobs or steps; in an `if:` it lets a job run although `needs:` did not succeed
STATUS_FUNCTION = re.compile(r"\b(?:always|failure|cancelled|success)\s*\(", re.I)
RELEASE_SHELLS = frozenset(["bash", "sh"])  # the values of `shell:` that R13 accepts (first word)
VERSION_COMMENT = re.compile(r"^#\s*(v\d+\.\d+\.\d+)\s*$")
EXPR_UNSAFE = re.compile(r"\$\{\{[^}]*\b(github\.event\.|github\.head_ref|github\.ref_name|inputs\.)")
SECRETS_WORD = re.compile(r"\bsecrets\b", re.I)  # GitHub resolves context names without regard to case
# a `secrets:` key at the start of a line or inside a flow collection (an entry of a flow sequence may be a one-pair mapping)
SECRETS_KEY = re.compile(r"""(?:^\s*(?:-\s+)*|[\[{,]\s*)["']?secrets["']?\s*:""", re.I)
SECRETS_ENTRY = re.compile(r"^ *[A-Za-z_][A-Za-z0-9_]*: +(\$\{\{ *secrets\.([A-Za-z_][A-Za-z0-9_]*) *\}\})(?:[ \t]+#.*|[ \t]*)$")
MAPPING_KEY = re.compile(r"""^(?:([A-Za-z0-9_][A-Za-z0-9_-]*)|"([^"]*)"|'([^']*)') *:(?: +(.*)|$)""")
BLOCK_SCALAR = re.compile(r"^[|>](?:[-+][0-9]?|[0-9][-+]?)? *(?:#.*)?$")
NODE_PROPERTIES = re.compile(r"^(?:[&!]\S*(?: +|$))+")  # anchors and tags in front of a value
ALLOW_WORKFLOW = re.compile(r"^[A-Za-z0-9._-]+\.ya?ml$", re.I)
WORKFLOW_FILE = re.compile(r"\.ya?ml$", re.I)  # a file below .github/workflows that GitHub runs as a workflow
ACTION_FILE = re.compile(r"^action\.ya?ml$", re.I)  # the metadata file of a local action
ALLOW_JOB = re.compile(r"^[A-Za-z0-9_-]+$")
ALLOW_SECRET = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
ZIZMOR_AUDIT = re.compile(r"^[a-z][a-z0-9-]*$")
ZIZMOR_IGNORE_PATH = re.compile(r"^[A-Za-z0-9._/-]+\.ya?ml(?::[0-9]+(?::[0-9]+)?)?$")
ZIZMOR_COMMENT = re.compile(r"#\s*zizmor\s*:\s*ignore", re.I)
ZIZMOR_COMMENT_OK = re.compile(r"#\s*zizmor\s*:\s*ignore\[\s*[a-z][a-z0-9-]*(?:\s*,\s*[a-z][a-z0-9-]*)*\s*\](.*)$", re.I)
# R14: characters with which a YAML parser reads a file differently from this check, which splits it into lines at LF and CR only.
# NEL (U+0085), LS (U+2028) and PS (U+2029) are line breaks for the parser (zizmor's reader of zizmor.yml is one), so the two
# disagree about what a comment is. A parser also rejects or skips control characters (C0 but TAB, LF and CR; DEL; C1) and a byte
# order mark. Each of them is a finding, because every rule of this check reads lines.
UNREADABLE = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f\u2028\u2029\ufeff]")
LINE_BREAK_CHARACTERS = {"\x85": "NEXT LINE", "\u2028": "LINE SEPARATOR", "\u2029": "PARAGRAPH SEPARATOR"}
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
    """Follow the brackets and quotes of a flow collection or a quoted scalar through one more line. Returns (depth, quote,
    end): the new state, where (0, "") means that the collection or the scalar is closed, and the offset in `text` at which
    the scan stopped: its end, the place just behind the closing bracket or quote, or the `#` that starts a comment."""
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
    return depth, quote, min(i, len(text))


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


def find_files(directory, name, recursive=False):
    """The sorted paths of the files in `directory` (and below it, with `recursive`) whose name matches the pattern `name`.
    Names that begin with a dot count, and so do directories that do: GitHub reads them as well. A link that leads nowhere
    (the lock file `.#ci.yml` of an editor) is no file."""
    found = []
    for dirpath, _, filenames in os.walk(directory, followlinks=recursive):
        found += [p for p in (os.path.join(dirpath, n) for n in filenames if name.search(n)) if os.path.isfile(p)]
        if not recursive:
            break
    return sorted(found)


# ---- a small reader for the YAML that rules R12 and R13 look at -------------------------------------------------------------
# Block mappings and sequences, plain and quoted scalars, block scalars and one-line flow collections, with the line of every
# value. Anchors, aliases, tags, multi-line flow collections and multi-line scalars are not read: the reader reports them, and
# the rules turn that into a finding, so that a layout the reader cannot follow fails the check instead of passing it.


class Node:
    """A value of the document and the number of the line it starts on (the line of its key or of its list marker)."""
    __slots__ = ("v", "line")

    def __init__(self, v, line):
        self.v = v  # dict (key -> Node), list (of Node) or str
        self.line = line


class Block(str):
    """The text of a block scalar (`|` or `>`), for example the script of a `run:` step."""


def skip_spaces(s, i):
    while i < len(s) and s[i] in " \t":
        i += 1
    return i


def read_quoted(s, i):
    """The quoted scalar that starts at s[i]. Returns (text, index behind the closing quote) or raises ValueError."""
    q, out, j = s[i], [], i + 1
    while j < len(s):
        c = s[j]
        if q == "'" and c == "'":
            if s[j + 1:j + 2] == "'":
                out.append("'")
                j += 2
                continue
            return "".join(out), j + 1
        if q == '"' and c == "\\":
            out.append(s[j + 1:j + 2])
            j += 2
            continue
        if q == '"' and c == '"':
            return "".join(out), j + 1
        out.append(c)
        j += 1
    raise ValueError("a quoted scalar that does not end on its line")


def read_flow(s, i, line):
    """The flow value (a one-line collection or a scalar) at s[i]. Returns (Node, index behind it) or raises ValueError."""
    i = skip_spaces(s, i)
    if i >= len(s):
        raise ValueError("a flow collection that does not end on its line")
    c = s[i]
    if c == "[":
        items, i = [], i + 1
        while True:
            i = skip_spaces(s, i)
            if i >= len(s):
                raise ValueError("a flow collection that does not end on its line")
            if s[i] == "]":
                return Node(items, line), i + 1
            node, i = read_flow(s, i, line)
            items.append(node)
            i = skip_spaces(s, i)
            if i < len(s) and s[i] == ",":
                i += 1
            elif i >= len(s) or s[i] != "]":
                raise ValueError("a flow sequence that is not well formed")
    if c == "{":
        pairs, i = {}, i + 1
        while True:
            i = skip_spaces(s, i)
            if i >= len(s):
                raise ValueError("a flow collection that does not end on its line")
            if s[i] == "}":
                return Node(pairs, line), i + 1
            if s[i] in "\"'":
                key, i = read_quoted(s, i)
            else:
                j = i
                while j < len(s) and s[j] not in ":,}":
                    j += 1
                key, i = s[i:j].strip(), j
            i = skip_spaces(s, i)
            if i < len(s) and s[i] == ":":
                i = skip_spaces(s, i + 1)
                if i < len(s) and s[i] in ",}":
                    value = Node("", line)
                else:
                    value, i = read_flow(s, i, line)
            else:
                value = Node("", line)
            if key in pairs:
                raise ValueError("the key '%s' twice" % key)
            pairs[key] = value
            i = skip_spaces(s, i)
            if i < len(s) and s[i] == ",":
                i += 1
            elif i >= len(s) or s[i] != "}":
                raise ValueError("a flow mapping that is not well formed")
    if c in "\"'":
        text, i = read_quoted(s, i)
        return Node(text, line), i
    j = i
    while j < len(s) and s[j] not in ",]}":
        j += 1
    return Node(s[i:j].strip(), line), j


def fold_block(text):
    """The text of a folded block scalar (`>`): lines of equal indentation are joined by a space."""
    out = []
    for row in text.split("\n"):
        if out and row.strip() and not row.startswith((" ", "\t")) and out[-1].strip() and not out[-1].startswith((" ", "\t")):
            out[-1] += " " + row
        else:
            out.append(row)
    return "\n".join(out)


class MiniYaml:
    """Reads the lines of one YAML file into Node objects. parse() returns (root Node, [(line number, problem)])."""

    def __init__(self, lines):
        self.lines = [x.rstrip("\r") for x in lines]  # a file with Windows line ends is read like any other
        lines = self.lines
        self.errors = []
        self.rows = []  # [indent, text, line number] of every line that is neither blank nor a comment
        for no, raw in enumerate(lines, 1):
            text = raw.strip()
            if text and not text.startswith("#"):
                self.rows.append([indent_of(raw), text, no, raw])
        self.i = 0

    def err(self, no, msg):
        self.errors.append((no, msg))

    def peek(self):
        return self.rows[self.i] if self.i < len(self.rows) else None

    def parse(self):
        if self.peek() is not None and self.peek()[1] == "---":
            self.i += 1
        row = self.peek()
        if row is None:
            return Node({}, 1), self.errors
        root = self.block(row[0])
        while self.peek() is not None:
            self.err(self.peek()[2], "unexpected content")
            self.i += 1
        return root, self.errors

    def block(self, indent):
        text = self.peek()[1]
        if text == "-" or text.startswith("- "):
            return self.sequence(indent)
        return self.mapping(indent)

    def mapping(self, indent):
        pairs, first = {}, self.peek()[2]
        while True:
            row = self.peek()
            if row is None or row[0] < indent:
                break
            if row[0] > indent:
                self.err(row[2], "unexpected indentation")
                self.i += 1
                continue
            text, no = row[1], row[2]
            if text == "-" or text.startswith("- "):
                break  # a sequence at the indent of the key above belongs to that key; anything else is reported by the caller
            if "\t" in row[3][:indent + 1]:
                self.err(no, "a tab in the indentation")
            m = MAPPING_KEY.match(text)
            if not m:
                self.err(no, "not a 'key: value' line")
                self.i += 1
                continue
            key = next(g for g in m.groups()[:3] if g is not None)
            self.i += 1
            if key == "<<":
                self.err(no, "a merge key (<<)")
            if key in pairs:
                self.err(no, "the key '%s' twice" % key)
            pairs[key] = self.value(m.group(4) or "", indent, no, True)
        return Node(pairs, first)

    def sequence(self, indent):
        items, first = [], self.peek()[2]
        while True:
            row = self.peek()
            if row is None or row[0] < indent:
                break
            if row[0] > indent:
                self.err(row[2], "unexpected indentation")
                self.i += 1
                continue
            text, no = row[1], row[2]
            if not (text == "-" or text.startswith("- ")):
                break
            rest = text[1:].lstrip(" ")
            column = indent + len(text) - len(rest)
            if rest == "" or rest.startswith("#"):
                self.i += 1
                nxt = self.peek()
                if nxt is not None and nxt[0] > indent:
                    node = self.block(nxt[0])
                    node.line = no
                else:
                    node = Node("", no)
            elif rest == "-" or rest.startswith("- "):
                self.err(no, "a list inside a list item")
                self.i += 1
                node = Node("", no)
            elif rest[0] not in "[{\"'" and MAPPING_KEY.match(rest):
                row[0], row[1] = column, rest  # the rest of the line is the first key of the mapping that the item holds
                node = self.mapping(column)
                node.line = no
            else:
                self.i += 1
                node = self.value(rest, indent, no, False)
            items.append(node)
        return Node(items, first)

    def value(self, rest, indent, no, in_mapping):
        """The value that follows `key:` (or `-`) on line `no`; `indent` is the indent of the key (or of the marker)."""
        rest = rest.strip()
        if rest == "" or rest.startswith("#"):
            nxt = self.peek()
            if nxt is not None and nxt[0] > indent:
                node = self.block(nxt[0])
                node.line = no
                return node
            if in_mapping and nxt is not None and nxt[0] == indent and (nxt[1] == "-" or nxt[1].startswith("- ")):
                node = self.sequence(indent)
                node.line = no
                return node
            return Node("", no)
        if BLOCK_SCALAR.match(rest):
            return self.block_scalar(rest, indent, no)
        if rest[0] in "&*!":
            self.err(no, "an anchor, alias or tag")
            return Node(rest, no)
        if rest[0] in "[{":
            try:
                node, end = read_flow(rest, 0, no)
                if rest[end:].strip() and not rest[end:].strip().startswith("#"):
                    raise ValueError("text behind a flow collection")
                return node
            except ValueError as e:
                self.err(no, "%s (write a flow collection on one line)" % e)
                return Node("", no)
        if rest[0] in "\"'":
            try:
                text, end = read_quoted(rest, 0)
                if rest[end:].strip() and not rest[end:].strip().startswith("#"):
                    raise ValueError("text behind a quoted scalar")
                return Node(text, no)
            except ValueError as e:
                self.err(no, str(e))
                return Node("", no)
        nxt = self.peek()
        if nxt is not None and nxt[0] > indent:
            self.err(no, "a scalar that continues on the next line")
        return Node(re.split(r"\s#", rest, maxsplit=1)[0].rstrip(), no)

    def block_scalar(self, header, indent, no):
        j = no  # self.lines[j] is the line after the header
        while j < len(self.lines) and (self.lines[j].strip() == "" or indent_of(self.lines[j]) > indent):
            j += 1
        body = self.lines[no:j]
        while body and body[-1].strip() == "":
            body.pop()
        width = min([indent_of(x) for x in body if x.strip()] or [0])
        text = "\n".join(x[width:] for x in body)
        if header[0] == ">":
            text = fold_block(text)
        while self.peek() is not None and self.peek()[2] <= j:
            self.i += 1
        return Node(Block(text), no)


def as_map(node):
    return node.v if node is not None and isinstance(node.v, dict) else None


def as_list(node):
    return node.v if node is not None and isinstance(node.v, list) else None


def as_text(node):
    """The text of a scalar (None for a collection and for a missing node)."""
    return node.v if node is not None and isinstance(node.v, str) else None


# ---- the shell commands of a `run:` text (rule R13) --------------------------------------------------------------------------

ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
SHELL_SKIP = frozenset(["!", "{", "}", "if", "then", "else", "elif", "while", "until", "do"])  # words in front of a command
# a redirection in front of a command (`2>&1 go`, `> log make`, `<<< text bash`); the target may be a word of its own
REDIRECTION = re.compile(r"^(?:[0-9]*|&)(?:<<<|<<-?|<>|>>|>&|<&|>\||>|<)")
ARRAY_ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*(?:\[[^\]]*\])?\+?=$")  # `tools=` in front of `(go node)`
SHELLS = frozenset(["sh", "bash", "dash", "zsh", "ksh", "ash"])
SHELL_OPTIONS_WITH_VALUE = frozenset(["-o", "+o", "-O", "+O", "--rcfile", "--init-file"])
FIND_EXEC = frozenset(["-exec", "-execdir", "-ok", "-okdir"])
# a shell that reads its script from standard input (`bash <<EOF`, `echo ... | sh`, `bash -s`, `. /dev/stdin`): its text is not in the run text
STDIN_SHELL = "a shell that reads its script from standard input"
# wrappers that start another command: the options that take a value, and the number of positional arguments in front of it
WRAPPERS = {
    "env": (frozenset(["-u", "-C"]), 0),
    "sudo": (frozenset(["-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U"]), 0),
    "exec": (frozenset(["-a"]), 0),
    "nohup": (frozenset(), 0),
    "command": (frozenset(), 0),
    "nice": (frozenset(["-n"]), 0),
    "ionice": (frozenset(["-c", "-n", "-p"]), 0),
    "timeout": (frozenset(["-s", "-k"]), 1),
    "xargs": (frozenset(["-I", "-L", "-n", "-P", "-s", "-d", "-E", "-a"]), 0),
    "stdbuf": (frozenset(["-i", "-o", "-e"]), 0),
    "setsid": (frozenset(), 0),
    "time": (frozenset(["-f", "-o", "--format", "--output"]), 0),
    "builtin": (frozenset(), 0),
    "coproc": (frozenset(), 0),
    "doas": (frozenset(["-u", "-C", "-a"]), 0),
}


def matching_paren(text, i):
    """The index just behind the `)` that closes the `(` before text[i] (the end of the text when it is not closed)."""
    depth = 1
    while i < len(text):
        c = text[i]
        if c == "\\":
            i += 1
        elif c == "'":
            j = text.find("'", i + 1)
            i = j if j >= 0 else len(text)
        elif c == '"':
            j = i + 1
            while j < len(text) and text[j] != '"':
                j += 2 if text[j] == "\\" else 1
            i = j
        elif c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                return i + 1
        i += 1
    return len(text)


def skip_blank(text, i):
    """The index of the next character that is no white space and not part of a comment line."""
    n = len(text)
    while i < n:
        if text[i] in " \t\n":
            i += 1
        elif text[i] == "#":
            while i < n and text[i] != "\n":
                i += 1
        else:
            break
    return i


def end_of_case_pattern(text, i):
    """The index just behind the `)` that ends the pattern list of a `case` clause that starts at text[i]."""
    n = len(text)
    if text[i:i + 1] == "(":
        i += 1
    depth = 0
    while i < n:
        c = text[i]
        if c == "\\":
            i += 2
            continue
        if c == "'":
            j = text.find("'", i + 1)
            i = (j if j >= 0 else n) + 1
            continue
        if c == '"':
            i += 1
            while i < n and text[i] != '"':
                i += 2 if text[i] == "\\" else 1
            i += 1
            continue
        if c == "(":
            depth += 1
        elif c == ")":
            if depth == 0:
                return i + 1
            depth -= 1
        i += 1
    return n


def is_case_header(words):
    """True for `case WORD in` (behind reserved words such as `then`)."""
    k = 0
    while k < len(words) and words[k] in SHELL_SKIP:
        k += 1
    return len(words) == k + 3 and words[k] == "case" and words[k + 2] == "in"


def shell_commands(text, depth=0):
    """The simple commands of a shell script as [(words, line offset)]: quotes removed, a command ends at a newline, `;`, `&`,
    `|`, `&&`, `||` or a parenthesis. Comments, the bodies of here-documents, the text of quotes, the patterns of a `case`
    clause and the members of an array assignment (`tools=(go node)`) are no commands; the text of a command substitution
    (`$(...)` and backticks) is scanned as well."""
    cmds, words, cur = [], [], None
    here = []  # delimiters of the here-documents that start on the current line
    cmd_line = 0
    case_depth = 0  # the number of open `case` commands
    patterns = False  # the next text is the pattern list of a case clause
    i, n = 0, len(text)

    def end_word():
        nonlocal cur, case_depth, patterns
        if cur is not None:
            words.append(cur)
            cur = None
            if is_case_header(words):
                cmds.append((list(words), cmd_line))
                del words[:]
                case_depth += 1
                patterns = True

    def end_command():
        nonlocal cmd_line, case_depth
        end_word()
        if words:
            if words[0] == "esac" and case_depth > 0:
                case_depth -= 1
            cmds.append((list(words), cmd_line))
            del words[:]

    def add(s):
        nonlocal cur, cmd_line
        if cur is None:
            if not words:
                cmd_line = text.count("\n", 0, i)
            cur = ""
        cur += s

    def substitution(inner):
        if depth < 8:
            base = text.count("\n", 0, i)
            cmds.extend((w, base + off) for w, off in shell_commands(inner, depth + 1))

    while i < n:
        if patterns and cur is None and not words and not here:
            patterns = False
            j = skip_blank(text, i)
            if not (text.startswith("esac", j) and text[j + 4:j + 5] in ("", " ", "\t", "\n", ";", "&", "|", "(", ")")):
                i = end_of_case_pattern(text, j)
                continue
        c = text[i]
        if c == "\\":
            if text[i + 1:i + 2] == "\n":
                i += 2
            else:
                add(text[i + 1:i + 2])
                i += 2
        elif c == "'":
            j = text.find("'", i + 1)
            j = n if j < 0 else j
            add(text[i + 1:j])
            i = j + 1
        elif c == '"':
            i += 1
            buf = []
            while i < n and text[i] != '"':
                if text[i] == "\\":
                    buf.append(text[i + 1:i + 2])
                    i += 2
                elif text.startswith("$((", i):
                    i = matching_paren(text, i + 2)
                elif text.startswith("$(", i):
                    end = matching_paren(text, i + 2)
                    substitution(text[i + 2:end - 1])
                    i = end
                elif text[i] == "`":
                    end = text.find("`", i + 1)
                    end = n if end < 0 else end
                    substitution(text[i + 1:end])
                    i = end + 1
                else:
                    buf.append(text[i])
                    i += 1
            i += 1
            add("".join(buf))
        elif text.startswith("$((", i):
            i = matching_paren(text, i + 2)
            add("0")
        elif text.startswith("$(", i):
            end = matching_paren(text, i + 2)
            substitution(text[i + 2:end - 1])
            i = end
            add("x")
        elif c == "`":
            end = text.find("`", i + 1)
            end = n if end < 0 else end
            substitution(text[i + 1:end])
            i = end + 1
            add("x")
        elif c in " \t":
            end_word()
            i += 1
        elif c == "\n":
            end_command()
            i += 1
            while here:
                delimiter = here.pop(0)
                while i < n:
                    j = text.find("\n", i)
                    j = n if j < 0 else j
                    line = text[i:j].strip()
                    i = j + 1
                    if line == delimiter:
                        break
        elif c == "#" and cur is None:
            while i < n and text[i] != "\n":
                i += 1
        elif c == "&" and ((cur or "")[-1:] in ("<", ">") or text[i + 1:i + 2] == ">"):
            add(c)  # a redirection: 2>&1, &>file
            i += 1
        elif c == ";" and case_depth > 0 and text[i + 1:i + 2] in (";", "&"):  # the end of a case clause: `;;`, `;&`, `;;&`
            end_command()
            i += 3 if text.startswith(";;&", i) else 2
            patterns = True
        elif c == "(" and cur is not None and ARRAY_ASSIGNMENT.match(cur):  # tools=(go node): the members are words
            end = matching_paren(text, i + 1)
            substitution("echo " + text[i + 1:end - 1].replace("\n", "\necho "))
            i = end
            add("x")
        elif c in ";&|()":
            end_command()
            i += 1
        elif text.startswith("<<", i) and not text.startswith("<<<", i):
            j = skip_spaces(text, i + 2 + (1 if text[i + 2:i + 3] == "-" else 0))
            m = re.match(r"""(['"]?)([^\s'"<>|&;()]+)\1""", text[j:])
            if m:
                here.append(m.group(2))
                i = j + len(m.group(0))
            else:
                i += 2
            end_word()
        else:
            add(c)
            i += 1
    end_command()
    return cmds


def command_word(words):
    """The words of the command that starts a simple command, without assignments (FOO=1), reserved words (`then`) and
    wrappers (`sudo`, `env`, `time -p`, `timeout 5`; `env -S 'go build'` is split into words); None when it starts none
    (`command -v go` only looks a program up)."""
    i = 0
    while i < len(words):
        w = words[i]
        if ASSIGNMENT.match(w) or w in SHELL_SKIP:
            i += 1
            continue
        redirection = REDIRECTION.match(w)
        if redirection:
            i += 2 if redirection.end() == len(w) else 1  # the target of a lone `>` is the next word
            continue
        base = w.rsplit("/", 1)[-1]
        wrapper = WRAPPERS.get(base)
        if wrapper is None:
            return words[i:]
        with_value, positional = wrapper
        if base == "command" and any(x in ("-v", "-V") for x in words[i + 1:i + 3]):
            return None
        i += 1
        while i < len(words):
            x = words[i]
            if x == "--":
                i += 1
                break
            if base == "env" and (x.startswith("-S") or x.startswith("--split-string")):
                if x.startswith("--split-string="):
                    value, used = x.split("=", 1)[1], 1
                elif x in ("-S", "--split-string"):
                    value, used = (words[i + 1] if i + 1 < len(words) else ""), 2
                else:
                    value, used = x[2:], 1
                try:
                    split = shlex.split(value)
                except ValueError:
                    split = value.split()
                return command_word(split + words[i + used:])
            if ASSIGNMENT.match(x):
                i += 1
            elif x.startswith("-") and len(x) > 1:
                i += 2 if x in with_value else 1
            else:
                break
        i += positional
    return None


def shell_script_source(args):
    """How a shell that is started with `args` gets its script: ("c", text) for `-c text`, ("file", None) for a file operand
    and ("stdin", None) when there is neither (or `-s`)."""
    has_s, k = False, 0
    while k < len(args):
        x = args[k]
        redirection = REDIRECTION.match(x)
        if redirection:
            k += 2 if redirection.end() == len(x) else 1
        elif x == "--":
            k += 1
            break
        elif x in SHELL_OPTIONS_WITH_VALUE:
            k += 2
        elif x[:1] in ("-", "+") and len(x) > 1:
            if x[0] == "-" and not x.startswith("--") and "c" in x[1:]:
                return ("c", args[k + 1]) if k + 1 < len(args) else ("file", None)
            has_s = has_s or (x[0] == "-" and not x.startswith("--") and "s" in x[1:])
            k += 1
        else:
            break
    while k < len(args) and REDIRECTION.match(args[k]):  # redirections behind `--` are no operands
        k += 2 if REDIRECTION.match(args[k]).end() == len(args[k]) else 1
    return ("stdin", None) if has_s or k >= len(args) else ("file", None)


def programs_of_words(words, depth):
    """The names of the programs that one simple command starts: the command itself and what it runs through `sh -c '...'`,
    `eval` and `find -exec`. The name is the last component of the path."""
    rest = command_word(words)
    if not rest:
        return []
    name = rest[0].rsplit("/", 1)[-1]
    found = []
    if name in SHELLS and depth < 4:
        kind, script = shell_script_source(rest[1:])
        if kind == "c":
            found.extend(p for p, _ in started_programs(script, depth + 1))
        elif kind == "stdin":
            found.append(STDIN_SHELL)
    elif name in ("source", ".") and any(x.startswith(("/dev/", "/proc/")) for x in rest[1:]):
        found.append(STDIN_SHELL)
    elif name == "eval" and depth < 4:
        found.extend(p for p, _ in started_programs(" ".join(rest[1:]), depth + 1))
    elif name == "find" and depth < 4:
        for k, x in enumerate(rest):
            if x in FIND_EXEC:
                end = next((j for j in range(k + 1, len(rest)) if rest[j] in (";", "+")), len(rest))
                found.extend(programs_of_words(rest[k + 1:end], depth + 1))
    found.append(name)
    return found


def started_programs(text, depth=0):
    """[(program name, line offset)] of every command that the shell script starts: the first word of each simple command, also
    behind a wrapper and inside `sh -c '...'`, `eval` and `find -exec`. The name is the last component of the path. A shell
    that reads its script from standard input is reported as STDIN_SHELL."""
    found = []
    for words, offset in shell_commands(text):
        found.extend((program, offset) for program in programs_of_words(words, depth))
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
        self.unreadable = set()  # workflow files that are not UTF-8: the other rules cannot read them (R14 reports the file)

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

    def read_lines(self, path):
        """The lines of a file that zizmor reads as well (a workflow, a local action, zizmor.yml, the Dependabot configuration),
        or None for a file that is not UTF-8. LF, CR and CRLF end a line. A character with which a YAML parser reads the file
        differently (UNREADABLE) is an R14 finding, one per character, at its first line; the lines are returned all the same."""
        with open(path, "rb") as f:
            data = f.read()
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError as e:
            line = len(re.findall(rb"\r\n|\r|\n", data[:e.start])) + 1
            self.add(path, line, "R14", "the file is not valid UTF-8 (%s at byte %d)" % (e.reason, e.start))
            return None
        text = text.replace("\r\n", "\n").replace("\r", "\n")
        reported = set()
        for m in UNREADABLE.finditer(text):
            char = m.group(0)
            if char in reported:
                continue
            reported.add(char)
            name = LINE_BREAK_CHARACTERS.get(char, "BYTE ORDER MARK" if char == "\ufeff" else "control character")
            how = "read as a line break by" if char in LINE_BREAK_CHARACTERS else "rejected or skipped by"
            self.add(path, text.count("\n", 0, m.start()) + 1, "R14",
                     "the file contains U+%04X (%s), which is %s YAML parsers such as zizmor's, but not by this check: remove it" % (ord(char), name, how))
        return text.split("\n")

    # ---- per file -------------------------------------------------------------------------------------------
    def check_file(self, path, is_workflow):
        lines = self.read_lines(path)
        if lines is None:
            if is_workflow:
                self.unreadable.add(os.path.basename(path))
            return
        self.rule_uses(path, lines, is_workflow)
        self.rule_triggers(path, lines)
        self.rule_checkout(path, lines)
        self.rule_run_injection(path, lines)
        self.rule_toolchains(path, lines)
        self.rule_secrets(path, lines, is_workflow)
        if is_workflow:
            self.rule_schedule_conditions(path, lines)
            self.rule_permissions(path, lines)
            self.rule_timeouts(path, lines)
            if os.path.basename(path) == "scorecard.yml":
                self.rule_scorecard(path, lines)
            if os.path.basename(path).lower() in ("release.yml", "release.yaml"):
                self.rule_release(path, lines)
        self.rule_zizmor_comments(path, lines)

    def job_at(self, lines, index):
        """The id of the job that contains the line with the index `index`, or None."""
        for name, (start, end) in self.jobs(lines).items():
            if start <= index < end:
                return name
        return None

    def rule_uses(self, path, lines, is_workflow):
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
                p = PINNED_ANY.match(ref)
                key = "%s/%s" % (p.group(1), p.group(2)) if p else ""
                if not p or key not in THIRD_PARTY_ACTIONS or p.group(3):
                    self.add(path, i, "R1", "uses: %s is not a local path or an actions/* or github/* action pinned to a full commit SHA" % ref)
                    continue
                wf, job = THIRD_PARTY_ACTIONS[key]
                if not is_workflow or os.path.basename(path) != wf or self.job_at(lines, i - 1) != job:
                    self.add(path, i, "R1", "uses: %s is allowed only in .github/workflows/%s, job %s (ADR-0033)" % (ref, wf, job))
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

        Returns (where, code, keys). where[i] is None for a line that is not part of the structure (blank, comment, the content
        of a block scalar, the continuation of a quoted scalar or of a flow collection) and otherwise (ancestors, key, value):
        the keys above the line ('-' stands for a list item, for example ('jobs', 'deploy', 'steps', '-', 'env')), the key of
        the line and the text after its colon. code[i] is the text of the line that can hold an expression: empty for a blank
        line or a comment, and without the trailing comment where the value is a plain scalar (the one case in which `#` surely
        starts a comment; the content of a block scalar or a quoted scalar is code as a whole). keys[i] is the part of the line
        in which a mapping key can stand: the text in front of the value of a structural line, that value too where it opens a
        flow collection, and the lines of a flow collection up to its end. It is empty for the content of a block scalar (a
        `run:` script) and of a quoted scalar, where text such as `secrets:` is no key."""
        where = [None] * len(lines)
        code = [""] * len(lines)
        keys = [""] * len(lines)
        stack = []  # (indent, name) of the open parents: a key without a value of its own, or '-' for a list item
        block = None  # indent of the key whose block scalar is open
        flow = (0, "")  # open brackets and quote of a flow collection or quoted scalar that spans lines
        for i, line in enumerate(lines):
            if line.strip() == "":
                continue
            if flow != (0, ""):
                depth, quote, end = scan_flow(line, *flow)
                if flow[0] > 0:  # inside a flow collection, also when a string of it spans lines: keys can stand anywhere
                    keys[i] = line[:end]
                flow = (depth, quote)
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
            start = at + len(raw.rstrip()) - len(value)  # where the value starts in the line
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
            # Where a key can stand: in front of the value, and in the value if it opens a flow collection (up to the end of
            # its first line). A quoted or plain scalar holds no key. A line that is no `key: value` is read whole, to be safe.
            if value[:1] in ("'", '"', "{", "["):
                depth, quote, end = scan_flow(value, 0, "")
                flow = (depth, quote)
                keys[i] = line[:start + (end if value[0] in "{[" else 0)]
            else:
                keys[i] = line[:start] if km else code[i]
        return where, code, keys

    def rule_secrets(self, path, lines, is_workflow):
        wf = os.path.basename(path)
        where, code, keys = self.structure(lines)
        if is_workflow:
            self.job_ids[wf] = set(w[1] for w in where if w is not None and w[0] == ("jobs",) and w[1])
        declared = jobs_with_environment(where)
        # `secrets:` keys: `secrets: inherit` or the mapping of a reusable workflow call. They are looked for where a key can
        # stand: the same words in a `run:` script, another block scalar or a quoted scalar are text.
        for i, row in enumerate(keys):
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

    # ---- R14: zizmor stays strict ---------------------------------------------------------------------------
    def rule_zizmor_comments(self, path, lines):
        """An inline `# zizmor: ignore[...]` names its rules and says why: a comment line right above or text behind the bracket."""
        for i, line in enumerate(lines):
            m = ZIZMOR_COMMENT.search(line)
            if not m:
                continue
            ok = ZIZMOR_COMMENT_OK.search(line)
            if not ok:
                self.add(path, i + 1, "R14", "an inline zizmor ignore must name its rules: `# zizmor: ignore[<audit>]`, not a bare ignore")
            elif not (ok.group(1).strip() or (i > 0 and lines[i - 1].strip().startswith("#"))):
                self.add(path, i + 1, "R14", "an inline zizmor ignore needs its reason in a comment line directly above or behind the bracket")

    def rule_zizmor_config(self):
        """zizmor.yml in the repository root: `rules: {<audit>: {ignore: [<file>[:line[:column]]]}}` and nothing else."""
        path = os.path.join(self.root, "zizmor.yml")
        if not os.path.isfile(path):
            return
        lines = self.read_lines(path)
        if lines is None:
            return
        root, errors = MiniYaml(lines).parse()
        if errors:
            no, problem = min(errors)
            self.add(path, no, "R14", "cannot read zizmor.yml: %s (rule R14 reads block style, one key per line)" % problem)
            return
        top = as_map(root)
        if top is None:
            self.add(path, root.line, "R14", "zizmor.yml must be a mapping with the key rules")
            return
        for key, node in top.items():
            if key != "rules":
                self.add(path, node.line, "R14", "zizmor.yml may only have the key rules, not '%s'" % key)
        rules_node = top.get("rules")
        if rules_node is None or as_text(rules_node) == "":
            return
        rules = as_map(rules_node)
        if rules is None:
            self.add(path, rules_node.line, "R14", "rules: must be a mapping of audits")
            return
        for audit, node in rules.items():
            if not ZIZMOR_AUDIT.match(audit):
                self.add(path, node.line, "R14", "'%s' is not the name of an audit" % audit)
            body = as_map(node)
            if body is None:
                self.add(path, node.line, "R14", "audit %s: only `ignore:` with a list of files is allowed" % audit)
                continue
            for key, sub in body.items():
                if key != "ignore":
                    self.add(path, sub.line, "R14", "audit %s: only `ignore:` is allowed, not '%s' (no audit is switched off or weakened)" % (audit, key))
            entries = body.get("ignore")
            if entries is None:
                continue
            items = as_list(entries)
            if items is None:
                self.add(path, entries.line, "R14", "audit %s: ignore: must be a list of files" % audit)
                continue
            for item in items:
                text = as_text(item)
                if text is None or not ZIZMOR_IGNORE_PATH.match(text):
                    self.add(path, item.line, "R14", "audit %s: an ignore entry is a workflow or action file, optionally with :line or :line:column (no wildcards)" % audit)
            if node.line < 2 or not lines[node.line - 2].strip().startswith("#"):
                self.add(path, node.line, "R14", "audit %s: a comment line directly above the audit must give the reason for its ignores" % audit)

    def rule_secret_entries(self):
        """Stale entries of ci/workflow-secrets.txt: the workflow file, the job or the use of the secret is gone."""
        for (wf, job, name), no in sorted(self.secret_entries.items(), key=lambda item: item[1]):
            if wf in self.unreadable:
                continue
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

    # ---- the Scorecard workflow (R12) and the release workflow (R13) ---------------------------------------------------------
    def read_tree(self, path, lines, rule):
        """The parsed workflow, or None (after a finding of `rule`) when the reader cannot follow its layout."""
        root, errors = MiniYaml(lines).parse()
        if errors:  # the first problem is the cause, the others follow from it
            no, problem = min(errors)
            self.add(path, no, rule, "cannot read the workflow: %s (rule %s reads block style, one key per line)" % (problem, rule))
            return None
        return as_map(root)

    def job_list(self, path, rule, top):
        """The jobs of the workflow as {id: Node}; a job that is not a mapping is a finding."""
        jobs = {}
        for job, node in (as_map(top.get("jobs")) or {}).items():
            if as_map(node) is None:
                self.add(path, node.line, rule, "job %s: cannot read the job" % job)
            else:
                jobs[job] = node
        return jobs

    def step_list(self, path, rule, job, body):
        """The steps of a job as [{key: Node}]; a `steps:` value or a step that is not a block list or mapping is a finding."""
        steps = []
        node = body.get("steps")
        if node is None:
            return steps
        items = as_list(node)
        if items is None:
            self.add(path, node.line, rule, "job %s: steps: must be a block list" % job)
            return steps
        for item in items:
            if as_map(item) is None:
                self.add(path, item.line, rule, "job %s: cannot read the step" % job)
            else:
                steps.append(as_map(item))
        return steps

    def rule_scorecard(self, path, lines):
        """R12: the shape of .github/workflows/scorecard.yml (ADR-0033)."""
        top = self.read_tree(path, lines, "R12")
        if top is None:
            return

        def add(line, msg):
            self.add(path, line, "R12", msg)

        for key, node in top.items():
            if key == "env":
                add(node.line, "the workflow has no top-level env: (the Scorecard API rejects the results)")
            elif key == "defaults":
                add(node.line, "the workflow has no top-level defaults: (the Scorecard API rejects the results)")
            elif key not in ("name", "on", "permissions", "jobs"):
                add(node.line, "the key '%s' is not allowed at the top level (only name, on, permissions and jobs)" % key)
        text = "\n".join(self.structure(lines)[1])  # without comments
        for start, expr in expressions(text):
            if SECRETS_WORD.search(expr):
                add(text.count("\n", 0, start) + 1, "the workflow uses no secrets")
        self.scorecard_triggers(top, add)
        jobs = as_map(top.get("jobs")) or {}
        if "analysis" not in jobs:
            add(top["jobs"].line if "jobs" in top else 1, "the workflow needs the job 'analysis'")
        for job, body in jobs.items():
            if job != "analysis":
                add(body.line, "job %s: the workflow has exactly one job, 'analysis'" % job)
        if "analysis" in jobs:
            self.scorecard_job(path, as_map(jobs["analysis"]), jobs["analysis"].line, add)

    def scorecard_triggers(self, top, add):
        node = top.get("on")
        if node is None:
            add(1, "the workflow needs an on: mapping with push (branches: [main]) and schedule")
            return
        triggers = as_map(node)
        if triggers is None:
            add(node.line, "on: must be a block mapping with push (branches: [main]) and schedule")
            return
        for key, value in triggers.items():
            if key not in ("push", "schedule"):
                add(value.line, "the trigger '%s' is not allowed (only push to main and schedule)" % key)
        for key in ("push", "schedule"):
            if key not in triggers:
                add(node.line, "the trigger '%s' is missing" % key)
        push = triggers.get("push")
        if push is not None:
            pushed = as_map(push)
            branches = as_list(pushed.get("branches")) if pushed else None
            if pushed is None or set(pushed) != {"branches"} or branches is None or [as_text(b) for b in branches] != ["main"]:
                add(push.line, "push must have exactly 'branches: [main]': the Scorecard API accepts results of the default branch only")
        schedule = triggers.get("schedule")
        if schedule is not None:
            entries = as_list(schedule)
            if not entries or any(as_map(e) is None or set(as_map(e)) != {"cron"} for e in entries):
                add(schedule.line, "schedule must be a list of '- cron: ...' entries")

    def scorecard_job(self, path, job, line, add):
        if job is None:
            add(line, "job analysis: cannot read the job")
            return
        for key, node in job.items():
            if key in ("env", "defaults", "container", "services", "environment", "strategy"):
                add(node.line, "job analysis: %s: is not allowed (the Scorecard API rejects it or the job would get more than it needs)" % key)
            elif key not in ("name", "runs-on", "timeout-minutes", "permissions", "steps"):
                add(node.line, "job analysis: the key '%s' is not allowed (only name, runs-on, timeout-minutes, permissions and steps)" % key)
        runs_on = job.get("runs-on")
        label = as_text(runs_on)
        m = UBUNTU_RUNNER.match(label or "")
        if not m or (int(m.group(1)), int(m.group(2))) < (22, 4):
            add(runs_on.line if runs_on else line, "job analysis: runs-on must be one GitHub-hosted Ubuntu runner, ubuntu-NN.NN with 22.04 or later")
        perms = job.get("permissions")
        scopes = as_map(perms)
        if scopes is None:
            add(perms.line if perms else line, "job analysis: permissions must list exactly contents: read, actions: read, security-events: write and id-token: write")
        else:
            for scope, node in scopes.items():
                if scope not in SCORECARD_PERMISSIONS:
                    add(node.line, "job analysis: the permission '%s' is not allowed" % scope)
            for scope, level in SCORECARD_PERMISSIONS.items():
                if scope not in scopes:
                    add(perms.line, "job analysis: the permission '%s: %s' is missing" % (scope, level))
                elif as_text(scopes[scope]) != level:
                    add(scopes[scope].line, "job analysis: the permission '%s' must be '%s'" % (scope, level))
        seen = set()
        for step in self.step_list(path, "R12", "analysis", job):
            uses = as_text(step.get("uses"))
            where = (step.get("uses") or Node("", line)).line
            for key, node in step.items():
                if key == "run" or key == "shell" or key == "env" or key == "working-directory":
                    add(node.line, "job analysis: a step may only use an action (%s: is not allowed)" % key)
                elif key not in ("name", "id", "uses", "with", "if"):
                    add(node.line, "job analysis: the step key '%s' is not allowed (only name, id, uses, with and if)" % key)
            if uses is None:
                if "run" not in step:
                    add(line, "job analysis: every step must have uses:")
                continue
            name = uses.split("@", 1)[0]
            if name not in SCORECARD_STEP_ACTIONS:
                add(where, "job analysis: the action %s is not allowed (only %s)" % (name, ", ".join(SCORECARD_STEP_ACTIONS)))
                continue
            seen.add(name)
            if name == "ossf/scorecard-action":
                options = as_map(step.get("with")) or {}
                if as_text(options.get("publish_results")) != "true":
                    add(where, "job analysis: ossf/scorecard-action needs 'publish_results: true' (without it there is no badge and the API check does not apply)")
        for name in SCORECARD_REQUIRED:
            if name not in seen:
                add(line, "job analysis: a step must use %s" % name)

    def rule_release(self, path, lines):
        """R13: .github/workflows/release.yml (ADR-0033)."""
        top = self.read_tree(path, lines, "R13")
        if top is None:
            return

        def add(line, msg):
            self.add(path, line, "R13", msg)

        self.release_triggers(top, add)
        shell = self.default_shell(top.get("defaults"))
        if shell is not None and not self.plain_shell(shell):
            add(shell.line, "defaults: run: shell: must be bash or sh (a script interpreter would read the run text as its own language)")
        jobs = self.job_list(path, "R13", top)
        info = {}
        for job, body in jobs.items():
            info[job] = self.release_job(path, job, body, add)
        if "guard" not in jobs:
            add(top["jobs"].line if "jobs" in top else 1, "the workflow needs a job 'guard' that checks the tag before any other job runs")

        def closure(job, seen=None):
            seen = set() if seen is None else seen
            for need in info[job]["needs"]:
                if need in info and need not in seen:
                    seen.add(need)
                    closure(need, seen)
            return seen

        for job, data in info.items():
            needs = closure(job)
            if job != "guard" and "guard" in jobs and "guard" not in needs:
                add(data["line"], "job %s: it must need the job guard (directly or through another job)" % job)
            if data["privileged"] and not data["release"] and not any(info[n]["release"] for n in needs):
                add(data["line"], "job %s (%s): it must run after a job that uses environment release (needs:), so that the approval gates it" % (job, data["privileged"]))

    @staticmethod
    def default_shell(defaults):
        """The node of `defaults: run: shell:` (None when there is none)."""
        return ((as_map((as_map(defaults) or {}).get("run")) or {}).get("shell"))

    @staticmethod
    def plain_shell(node):
        """True when a `shell:` value starts a POSIX shell that reads the run text as shell commands: bash or sh."""
        words = (as_text(node) or "").split()
        return bool(words) and words[0] in RELEASE_SHELLS

    def release_triggers(self, top, add):
        node = top.get("on")
        triggers = as_map(node)
        if triggers is None:
            add(node.line if node else 1, "on: must be a block mapping with push of tags only")
            return
        for key, value in triggers.items():
            if key != "push":
                add(value.line, "the trigger '%s' is not allowed: the release workflow runs only for a pushed tag" % key)
        push = triggers.get("push")
        if push is None:
            add(node.line, "the trigger push (tags) is missing")
            return
        pushed = as_map(push)
        tags = as_list(pushed.get("tags")) if pushed else None
        if pushed is None or set(pushed) != {"tags"} or not tags:
            add(push.line, "push must have 'tags:' and nothing else (no branches, paths or ignore lists)")
        elif any(not (as_text(t) or "").startswith("v") for t in tags):
            add(push.line, "the tag patterns must start with v")

    def release_job(self, path, job, node, add):
        """Checks one job of the release workflow and returns {line, needs, release, privileged}."""
        body, line = as_map(node), node.line
        for key in ("uses",):
            if key in body:
                add(body[key].line, "job %s: a job that calls a workflow is not allowed (it could restore a cache or hold a secret)" % job)
        # a job that runs although its needs: failed, and a failure that does not stop the job
        node = body.get("if")
        if node is not None and (as_text(node) is None or STATUS_FUNCTION.search(as_text(node))):
            add(node.line, "job %s: 'if:' must not test the status of other jobs (always(), failure(), cancelled(), success()): the job would run although a job in needs: failed or was not approved" % job)
        if "continue-on-error" in body and as_text(body["continue-on-error"]) != "false":
            add(body["continue-on-error"].line, "job %s: 'continue-on-error' lets a failed job count as passed, so that needs: does not gate the jobs behind it" % job)
        # needs
        needs = set()
        node = body.get("needs")
        if node is not None:
            if as_text(node) is not None:
                needs.add(as_text(node))
            elif as_list(node) is not None:
                needs.update(as_text(n) or "" for n in as_list(node))
            else:
                add(node.line, "job %s: cannot read needs:" % job)
        # environment: a literal name
        release = False
        node = body.get("environment")
        if node is not None:
            name = as_text(node) if as_text(node) is not None else as_text((as_map(node) or {}).get("name"))
            if not name:
                add(node.line, "job %s: environment: needs a name" % job)
            elif "${{" in name:
                add(node.line, "job %s: the name of the environment must be a literal, not an expression" % job)
            elif name != "release":
                add(node.line, "job %s: the only environment is release (not '%s')" % (job, name))
            release = bool(name)  # a job with an environment waits for its approval, whatever the finding about its name
        # privileges
        reasons = []
        perms = body.get("permissions")
        if perms is not None:
            scopes = as_map(perms)
            if scopes is None:
                if as_text(perms) not in ("read-all", "{}"):
                    reasons.append("permissions: %s" % (as_text(perms) or "?"))
            else:
                reasons.extend("%s: %s" % (scope, as_text(level) or "?") for scope, level in scopes.items() if as_text(level) not in ("read", "none"))
        if release:
            reasons.append("environment")
        privileged = ", ".join(reasons)
        job_shell = self.default_shell(body.get("defaults"))
        if privileged and job_shell is not None and not self.plain_shell(job_shell):
            add(job_shell.line, "job %s (%s): defaults: run: shell: must be bash or sh (a script interpreter would read the run text as its own language)" % (job, privileged))
        for step in self.step_list(path, "R13", job, body):
            self.release_step(job, step, privileged, line, add)
        return {"line": line, "needs": needs, "release": release, "privileged": privileged}

    def release_step(self, job, step, privileged, line, add):
        uses = as_text(step.get("uses"))
        where = (step.get("uses") or step.get("run") or Node("", line)).line
        options = as_map(step.get("with")) or {}
        name = (uses or "").split("@", 1)[0]
        if uses is not None and (uses.startswith("./") or uses.startswith("../")):
            add(where, "job %s: a local action is not allowed (it could restore a cache)" % job)
        if name.startswith("actions/cache"):
            add(where, "job %s: %s: the release workflow uses no cache" % (job, name))
        if "cache" in options and as_text(options["cache"]) != "false":
            add(options["cache"].line, "job %s: 'cache:' restores a cache: the release workflow uses none" % job)
        if name == "actions/setup-go" and as_text(options.get("cache")) != "false":
            add(where, "job %s: actions/setup-go caches by default: set 'cache: false'" % job)
        if name == "actions/setup-node" and as_text(options.get("package-manager-cache")) != "false":
            add(where, "job %s: actions/setup-node caches by default: set 'package-manager-cache: false'" % job)
        if "continue-on-error" in step and as_text(step["continue-on-error"]) != "false":
            add(step["continue-on-error"].line, "job %s: 'continue-on-error' lets a failed step count as passed, so that a check (the tag guard, a verification) cannot stop the workflow" % job)
        if not privileged:
            return
        if "shell" in step and not self.plain_shell(step["shell"]):
            add(step["shell"].line, "job %s (%s): shell: must be bash or sh (a script interpreter would read the run text as its own language)" % (job, privileged))
        if name in ("actions/setup-go", "actions/setup-node"):
            add(where, "job %s (%s): %s must not run in a job with these privileges (build code runs only in jobs with contents: read)" % (job, privileged, name))
        script = step.get("run")
        if script is not None and as_text(script) is not None:
            base = script.line + (1 if isinstance(script.v, Block) else 0)
            for program, offset in started_programs(script.v):
                if program == STDIN_SHELL:
                    add(base + offset, "job %s (%s): %s: the check cannot read it (write the commands into the run text or call a script)" % (job, privileged, program))
                elif program in RELEASE_FORBIDDEN_COMMANDS:
                    add(base + offset, "job %s (%s): the command %s must not run in a job with these privileges (build code runs only in jobs with contents: read)" % (job, privileged, program))

    # ---- repository level -------------------------------------------------------------------------------------
    def rule_parity(self, ci_path):
        lines = self.read_lines(ci_path)
        if lines is None:
            return
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
    workflows = find_files(wf_dir, WORKFLOW_FILE)
    actions = find_files(os.path.join(root, ".github", "actions"), ACTION_FILE, recursive=True)
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
    c.rule_zizmor_config()
    for name in ("dependabot.yml", "dependabot.yaml"):
        dep = os.path.join(root, ".github", name)
        if os.path.isfile(dep):
            dep_lines = c.read_lines(dep)
            if dep_lines is not None:
                c.rule_zizmor_comments(dep, dep_lines)
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
