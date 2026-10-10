#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks and writes the SPDX header of the source files (ADR-0018, REUSE specification 3.3).

Every source file starts with the two lines of HEADER_LINES below, in the comment syntax of its language (a block
comment for CSS, SCSS, HTML, Helm templates and Hugo templates; see header_block()), followed by an empty line where the language allows it.
"Preamble" lines that must stay first (a "#!" line, a Dockerfile parser directive, an HTML doctype and so on) come
before the header. All other files are covered by REUSE.toml.

    python3 -I scripts/license-headers.py --check [PATH...]   report files without the exact header at its position
    python3 -I scripts/license-headers.py --fix [PATH...]     insert the exact header where it is missing
    python3 -I scripts/license-headers.py --list              print "style<TAB>path" of every file in scope

Scope: the files of `git ls-files --cached --others --exclude-standard` (tracked files and untracked files that are
not ignored; regular files only, symlinks are skipped) whose extension or name is in STYLE_BY_* below. PATH arguments
narrow the scope to files or directory prefixes.

Exit status: 0 = clean, 1 = findings (--check) or files that need a manual fix (--fix), 2 = usage or environment error
(for example: not a git work tree). The output is sorted and contains no absolute paths.

--fix only inserts the header; it never rewrites anything else, and a second run changes nothing. It refuses (exit status
1, message "manual fix") a file that mentions the SPDX license tag or the copyright line of the project in its first
20 lines outside the expected header, and a Helm or Hugo template that starts with whitespace (the "-}}" of the header
would trim that whitespace from the rendered output).

Python 3.9 compatible, standard library only.
"""

import os
import re
import subprocess
import sys

# REUSE-IgnoreStart
COPYRIGHT_LINE = "Copyright The Myriarch Authors"
LICENSE_LINE = "SPDX-License-Identifier: Apache-2.0"
# Tags that, within the first lines of a file, mean "someone wrote a header (or part of one) already".
STRAY_TAG = re.compile(r"SPDX-License-Identifier:|SPDX-FileCopyrightText:|" + re.escape(COPYRIGHT_LINE))
# REUSE-IgnoreEnd

HEADER_LINES = (COPYRIGHT_LINE, LICENSE_LINE)
STRAY_WINDOW = 20  # a stray tag counts within this many lines from the top of the file
BOM = b"\xef\xbb\xbf"

# Preamble rules: how many leading lines must stay in front of the header. Each takes the list of lines (without
# the end of line) and returns that number.
SHEBANG = re.compile(r"^#!")
SHELLCHECK_DIRECTIVE = re.compile(r"^#\s*shellcheck\s")
CODING_LINE = re.compile(r"^[ \t\f]*#.*?coding[:=]")  # PEP 263
DOCKER_DIRECTIVE = re.compile(r"^#\s*(syntax|escape|check)\s*=", re.IGNORECASE)
HTML_DOCTYPE = re.compile(r"^\s*<!doctype\b", re.IGNORECASE)


def preamble_none(lines):
    return 0


def preamble_shebang(lines):
    return 1 if lines and SHEBANG.match(lines[0]) else 0


def preamble_shell(lines):
    # "#!" line, then the shellcheck directives that follow directly. A file without a shebang whose first lines
    # are directives keeps those lines first.
    n = preamble_shebang(lines)
    while n < len(lines) and SHELLCHECK_DIRECTIVE.match(lines[n]):
        n += 1
    return n


def preamble_python(lines):
    # "#!" line and the PEP 263 coding line (line 1 or 2); everything up to the coding line stays in front.
    for i in (0, 1):
        if i < len(lines) and CODING_LINE.match(lines[i]):
            return i + 1
    return preamble_shebang(lines)


def preamble_dockerfile(lines):
    n = 0
    while n < len(lines) and DOCKER_DIRECTIVE.match(lines[n]):
        n += 1
    return n


def preamble_html(lines):
    return 1 if lines and HTML_DOCTYPE.match(lines[0]) else 0


# One entry per style: name -> (kind, comment prefix, preamble rule, empty line after the header).
#   kind "line": every header line starts with the comment prefix and a space
#   kind "css", "html", "helm": block comments with the fixed layouts of header_block(); Hugo templates use the "helm" layout
#   (a Go template comment, so the header is not part of the rendered output)
STYLES = {
    "go":         ("line", "//", preamble_none,       True),
    "script-js":  ("line", "//", preamble_shebang,    True),
    "shell":      ("line", "#",  preamble_shell,      True),
    "python":     ("line", "#",  preamble_python,     True),
    "powershell": ("line", "#",  preamble_none,       True),
    "makefile":   ("line", "#",  preamble_none,       True),
    "dockerfile": ("line", "#",  preamble_dockerfile, True),
    "service":    ("line", "#",  preamble_none,       True),
    "template":   ("line", "#",  preamble_none,       True),
    "sql":        ("line", "--", preamble_none,       True),
    "css":        ("css",  None, preamble_none,       True),
    "html":       ("html", None, preamble_html,       False),
    "helm":       ("helm", None, preamble_none,       False),
    "hugo":       ("helm", None, preamble_none,       False),
}

# Style by file extension (lower case, with the dot) and by file name. ".d.ts" is ".ts".
STYLE_BY_EXTENSION = {
    ".go": "go",
    ".ts": "script-js", ".tsx": "script-js", ".js": "script-js", ".mjs": "script-js", ".cjs": "script-js",
    ".sh": "shell", ".bash": "shell",
    ".py": "python",
    ".ps1": "powershell",
    ".service": "service",
    ".tmpl": "template",  # the sed templates of scripts/e2e/ui-updates; every .tmpl is a "#" file
    ".sql": "sql",
    ".css": "css",
    ".scss": "css",  # website/assets/scss: block comments as in CSS
    ".html": "html",
    ".dockerfile": "dockerfile",  # *.Dockerfile
}
STYLE_BY_NAME = {
    "Makefile": "makefile",
    "Dockerfile": "dockerfile",
}
# Every file of a Helm chart's templates directory with one of these extensions (checked before the table above).
HELM_TEMPLATE = re.compile(r"^deploy/helm/[^/]+/templates/.+\.(yaml|yml|tpl|txt)$")
# The Hugo templates of the website (checked before the extension table, like the Helm templates): everything below
# website/layouts with a template extension, and the content adapters (_content.gotmpl) below website/content.
HUGO_TEMPLATE = re.compile(r"^website/(layouts/.+\.(html|xml|txt|json)|content/(.+/)?_content\.gotmpl)$")


def style_of(path):
    """Returns the style name of a repository path, or None when the file is not in scope."""
    if HELM_TEMPLATE.match(path):
        return "helm"
    if HUGO_TEMPLATE.match(path):
        return "hugo"
    base = path.rsplit("/", 1)[-1]
    if base in STYLE_BY_NAME:
        return STYLE_BY_NAME[base]
    dot = base.rfind(".")
    if dot > 0:
        return STYLE_BY_EXTENSION.get(base[dot:].lower())
    return None


def header_block(style):
    """Returns the lines of the header (without the end of line) for a style."""
    kind, prefix = STYLES[style][0], STYLES[style][1]
    if kind == "line":
        return [prefix + " " + line for line in HEADER_LINES]
    if kind == "css":
        return ["/*"] + [" * " + line for line in HEADER_LINES] + [" */"]
    if kind == "html":
        return ["<!--"] + ["  " + line for line in HEADER_LINES] + ["-->"]
    if kind == "helm":
        return ["{{- /*"] + list(HEADER_LINES) + ["*/ -}}"]
    raise ValueError(kind)


class Source:
    """A file split into lines on "\\n" only. A trailing "\\r" stays part of the line (CRLF files)."""

    def __init__(self, data):
        self.bom = data.startswith(BOM)
        if self.bom:
            data = data[len(BOM):]
        text = data.decode("utf-8", errors="surrogateescape")
        self.newline_at_end = text.endswith("\n")
        parts = text.split("\n")
        if self.newline_at_end:
            parts.pop()
        self.lines = [] if text == "" else parts
        first = text.find("\n")
        self.crlf = first > 0 and text[first - 1] == "\r"

    def plain(self, i):
        """Line i without a trailing carriage return."""
        line = self.lines[i]
        return line[:-1] if line.endswith("\r") else line

    def plain_lines(self):
        return [self.plain(i) for i in range(len(self.lines))]

    def encode(self):
        text = "\n".join(self.lines)
        if self.newline_at_end:
            text += "\n"
        return (BOM if self.bom else b"") + text.encode("utf-8", errors="surrogateescape")


def misplaced_preamble(style, rest):
    """True when the lines after the header start with a line that only works at the top of the file (a "#!" line, a
    Dockerfile parser directive, a PEP 263 coding line, an HTML doctype). A shellcheck directive after the header is
    fine: it still applies to the whole script."""
    rule = preamble_shebang if style == "shell" else STYLES[style][2]
    return rule(rest) > 0


def classify(path, data):
    """Returns (status, detail). status: ok | missing | wrong text | wrong position | refused."""
    style = style_of(path)
    kind, _prefix, preamble, blank_after = STYLES[style]
    src = Source(data)
    if kind == "helm" and src.lines and (src.lines[0] == "" or src.lines[0][0] in " \t\r"):
        return "refused", "template starts with whitespace"
    lines = src.plain_lines()
    header = header_block(style)
    n = len(header)
    p = preamble(lines)
    if lines[p:p + n] == header and (not blank_after or p + n >= len(lines) or lines[p + n] == ""):
        # The preamble is computed from the top of the file, so a header on line 0 hides a preamble line below it.
        rest = lines[p + n:]
        if blank_after and rest and rest[0] == "":
            rest = rest[1:]
        if misplaced_preamble(style, rest):
            return "wrong position", ""
        return "ok", ""
    window = lines[:STRAY_WINDOW]
    for i in range(0, max(0, len(window) - n + 1)):
        if window[i:i + n] == header:
            return "wrong position", ""
    if any(STRAY_TAG.search(line) for line in window):
        return "wrong text", ""
    return "missing", ""


def insert_header(path, data):
    """Returns the content of the file with the header inserted at its position."""
    style = style_of(path)
    _kind, _prefix, preamble, blank_after = STYLES[style]
    src = Source(data)
    cr = "\r" if src.crlf else ""
    p = preamble(src.plain_lines())
    new = [line + cr for line in header_block(style)]
    rest = src.lines[p:]
    if not rest:
        src.newline_at_end = True  # the header is the whole rest: it ends with a single newline
    elif blank_after and src.plain(p) != "":
        new.append(cr)
    src.lines = src.lines[:p] + new + rest
    return src.encode()


# ---- file selection -------------------------------------------------------------------------------------------

def git_root():
    try:
        out = subprocess.run(["git", "rev-parse", "--show-toplevel"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    except (OSError, subprocess.CalledProcessError):
        return None
    return out.stdout.decode("utf-8", errors="surrogateescape").strip() or None


def candidate_paths(root):
    out = subprocess.run(
        ["git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True,
    )
    names = out.stdout.decode("utf-8", errors="surrogateescape").split("\0")
    return sorted({n for n in names if n})


class UsageError(Exception):
    """A bad command line argument (exit status 2)."""


def narrow(paths, root, args):
    """Keeps the paths that equal one of the PATH arguments or lie below it. Raises UsageError on a bad argument."""
    if not args:
        return paths
    prefixes = []
    for arg in args:
        rel = os.path.relpath(os.path.abspath(arg), root).replace(os.sep, "/")
        if rel == ".." or rel.startswith("../"):
            raise UsageError("a PATH lies outside the repository")
        if not os.path.exists(os.path.abspath(arg)):
            raise UsageError("PATH does not exist: " + rel)
        prefixes.append("" if rel == "." else rel.rstrip("/"))
    return [p for p in paths if any(x == "" or p == x or p.startswith(x + "/") for x in prefixes)]


def files_in_scope(root, args):
    paths = []
    for p in candidate_paths(root):
        if style_of(p) is None:
            continue
        full = os.path.join(root, p)
        if os.path.islink(full) or not os.path.isfile(full):
            continue
        paths.append(p)
    return narrow(paths, root, args)


def read(root, path):
    with open(os.path.join(root, path), "rb") as f:
        return f.read()


# ---- commands -------------------------------------------------------------------------------------------------

def cmd_list(root, args):
    paths = files_in_scope(root, args)
    for p in paths:
        print(style_of(p) + "\t" + p)
    return 0


def cmd_check(root, args):
    paths = files_in_scope(root, args)
    findings = []
    for p in paths:
        status, detail = classify(p, read(root, p))
        if status != "ok":
            findings.append(p + ": " + status + (" (" + detail + ")" if detail else ""))
    for line in findings:
        print(line)
    if findings:
        print("license-headers: %d of %d files need attention (python3 -I scripts/license-headers.py --fix)" % (len(findings), len(paths)))
        return 1
    print("license-headers: %d files checked, all headers ok" % len(paths))
    return 0


def cmd_fix(root, args):
    paths = files_in_scope(root, args)
    changed, refused = [], []
    for p in paths:
        data = read(root, p)
        status, detail = classify(p, data)
        if status == "ok":
            continue
        if status == "missing":
            with open(os.path.join(root, p), "wb") as f:
                f.write(insert_header(p, data))
            changed.append(p)
        else:
            refused.append((p, status, detail))
    for p in changed:
        print(p)
    for p, status, detail in refused:
        reason = detail or ("a header-like line is already there (" + status + ")")
        print("%s: refused, manual fix needed: %s" % (p, reason), file=sys.stderr)
    print("license-headers: %d files changed, %d need a manual fix" % (len(changed), len(refused)), file=sys.stderr)
    return 1 if refused else 0


def usage_error(message):
    print("license-headers: " + message, file=sys.stderr)
    return 2


USAGE = "usage: python3 -I scripts/license-headers.py (--check | --fix | --list) [PATH...]"


def main(argv):
    if argv and argv[0] in ("-h", "--help"):
        print(USAGE)
        return 0
    if not argv:
        print(USAGE, file=sys.stderr)
        return 2
    mode, args = argv[0], argv[1:]
    if mode not in ("--check", "--fix", "--list") or any(a.startswith("-") for a in args):
        print(USAGE, file=sys.stderr)
        return 2
    root = git_root()
    if root is None:
        return usage_error("not a git work tree (or git is missing)")
    handler = {"--check": cmd_check, "--fix": cmd_fix, "--list": cmd_list}[mode]
    try:
        return handler(root, args)
    except UsageError as e:
        return usage_error(str(e))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
