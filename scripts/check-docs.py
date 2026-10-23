#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks of the Markdown documentation: internal links and front matter.

Run it isolated and from the repository root (stdlib only, Python 3.9 or newer):

    python3 -I scripts/check-docs.py links [--root DIR] [--assume-exists FILE] [paths...]
    python3 -I scripts/check-docs.py front-matter [--root DIR] [--site] [paths...]

links
    Checks inline links [text](target) and [text](<target>), reference definitions [id]: target and the HTML
    attributes href, src and srcset. Fenced code blocks, inline code, HTML comments and front matter are ignored.
    Links with the schemes http, https, mailto, tel, ftp and data (and protocol-relative //host links) are skipped
    (external links are not checked offline). Every other target is resolved relative to the file that contains it
    (relative to the repository root for a file below changelog.d/, whose entries move into CHANGELOG.md):
      - an absolute link such as /docs/x.md is a finding,
      - the target must exist in the work tree (file or directory, spelled with exactly the same case), must stay
        inside the repository and must not be ignored by git,
      - an anchor (#section or file.md#section) is checked when the target is a Markdown file. Headings get GitHub
        slugs (lower case, characters other than letters, digits, spaces, "-" and "_" dropped, spaces become "-",
        duplicates get -1, -2, ...); <a id="x"> and <a name="x"> are anchors too.
    Default scope: the Markdown files of the repository (git ls-files '*.md', plus untracked files that are not
    ignored) except paths below a testdata or node_modules directory, and except the website sources below website/
    (their links are site-absolute and are validated by Hugo and by lychee, see ADR-0031). Paths narrow the scope;
    they are relative to the root and may be files or directories, and a path below website/ is checked when named.
    --assume-exists FILE names a file with one repository-relative path per line (blank lines and lines starting
    with # are ignored). A link to such a path (or to a directory that contains one) counts as existing, without an
    anchor check, even when the file is not there yet. It lets parallel writers check links into files that another
    package is still writing.

front-matter
    Every Markdown file below the given paths (default: docs/) except the decision records
    docs/adr/NNNN-*.md must start with a front matter block that holds exactly the keys title (non-empty
    double-quoted string), weight (integer) and description (non-empty double-quoted string), in this order,
    and must not repeat the title as a "# " heading in the body.
    With --site the rules of the website sources apply (default path: website/content): title and description
    (non-empty double-quoted strings) are required in this order, an optional integer weight sits between them,
    and the only other keys are linkTitle, type, layout, params, aliases, toc_hide, hide_summary, no_list,
    simple_list, outputs, cascade and menu (their values may continue on indented lines). The body must not
    repeat the title as a "# " heading either.

Output: one line "path:line: message" per finding on stdout, nothing else. Exit status: 0 clean, 1 findings,
2 usage or environment error (message on stderr).
"""

import argparse
import html
import json
import os
import posixpath
import re
import subprocess
import sys
import urllib.parse

SKIP_SCHEMES = ("http", "https", "mailto", "tel", "ftp", "data")
SKIP_DIR_NAMES = ("testdata", "node_modules")
ADR_RE = re.compile(r"^docs/adr/[0-9]{4}-[^/]*\.md$")
WEBSITE_PREFIX = "website/"


class UsageError(Exception):
    pass


# --------------------------------------------------------------------------------------------------------------
# repository access


def run_git(root, args, stdin=None):
    try:
        proc = subprocess.run(
            ["git", "-C", root] + args,
            input=stdin,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
    except OSError as exc:
        raise UsageError("cannot run git: %s" % exc)
    return proc


def find_root(arg):
    if arg:
        root = os.path.abspath(arg)
        if not os.path.isdir(root):
            raise UsageError("--root %s is not a directory" % arg)
    else:
        proc = run_git(os.getcwd(), ["rev-parse", "--show-toplevel"])
        if proc.returncode != 0:
            raise UsageError("not inside a git repository (use --root)")
        root = proc.stdout.decode("utf-8", "replace").strip()
    proc = run_git(root, ["rev-parse", "--git-dir"])
    if proc.returncode != 0:
        raise UsageError("%s is not a git work tree" % root)
    return root


def list_markdown(root):
    """Markdown files of the repository: tracked plus untracked-not-ignored, as sorted relative paths."""
    proc = run_git(root, ["-c", "core.quotepath=off", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.md"])
    if proc.returncode != 0:
        raise UsageError("git ls-files failed: %s" % proc.stderr.decode("utf-8", "replace").strip())
    files = set()
    for raw in proc.stdout.split(b"\0"):
        if not raw:
            continue
        rel = raw.decode("utf-8", "replace")
        parts = rel.split("/")
        if any(p in SKIP_DIR_NAMES for p in parts[:-1]):
            continue
        if os.path.isfile(os.path.join(root, rel)):
            files.add(rel)
    return sorted(files)


def narrow(root, files, paths):
    if not paths:
        return files
    keep = set()
    for p in paths:
        abs_p = p if os.path.isabs(p) else os.path.join(root, p)
        if not os.path.exists(abs_p):
            raise UsageError("path not found: %s" % p)
        rel = posixpath.normpath(os.path.relpath(abs_p, root).replace(os.sep, "/"))
        if rel.startswith(".."):
            raise UsageError("path outside the root: %s" % p)
        for f in files:
            if rel == "." or f == rel or f.startswith(rel + "/"):
                keep.add(f)
    return sorted(keep)


def read_text(root, rel):
    with open(os.path.join(root, rel), "rb") as fh:
        data = fh.read()
    return data.decode("utf-8", "replace").replace("\r\n", "\n").replace("\r", "\n")


# --------------------------------------------------------------------------------------------------------------
# Markdown preparation


def blank_out(text, start, end):
    return text[:start] + re.sub(r"[^\n]", " ", text[start:end]) + text[end:]


def blank_front_matter(lines):
    if lines and lines[0] == "---":
        for i in range(1, len(lines)):
            if lines[i] in ("---", "..."):
                for j in range(i + 1):
                    lines[j] = ""
                return
    return


FENCE_RE = re.compile(r"^(\s*)(`{3,}|~{3,})(.*)$")


def blank_fences(lines):
    """Blanks fenced code blocks (including the fence lines); an unclosed fence runs to the end of the file."""
    fence = None
    for i, line in enumerate(lines):
        m = FENCE_RE.match(line)
        if fence is None:
            if m and not (m.group(2)[0] == "`" and "`" in m.group(3)):
                fence = (m.group(2)[0], len(m.group(2)))
                lines[i] = ""
        else:
            if m and m.group(2)[0] == fence[0] and len(m.group(2)) >= fence[1] and m.group(3).strip() == "":
                fence = None
            lines[i] = ""


CODE_SPAN_RE = re.compile(r"(?<![\\`])(`+)(?!`)(.+?)(?<!`)\1(?!`)", re.S)
COMMENT_RE = re.compile(r"<!--.*?-->", re.S)


def blank_code_spans(lines):
    out = []
    group = []

    def flush():
        if group:
            para = "\n".join(group)
            para = CODE_SPAN_RE.sub(lambda m: re.sub(r"[^\n]", " ", m.group(0)), para)
            out.extend(para.split("\n"))
            del group[:]

    for line in lines:
        if line.strip() == "":
            flush()
            out.append(line)
        else:
            group.append(line)
    flush()
    return out


def blank_comments(text):
    return COMMENT_RE.sub(lambda m: re.sub(r"[^\n]", " ", m.group(0)), text)


def prepare(text):
    """Returns (heading_text, link_text): the Markdown with front matter, fences and comments blanked (headings), and
    additionally with inline code blanked (links, anchors). Line numbers are preserved."""
    lines = text.split("\n")
    blank_front_matter(lines)
    blank_fences(lines)
    headings = blank_comments("\n".join(lines))
    links = blank_comments("\n".join(blank_code_spans(lines)))
    return headings, links


ATX_RE = re.compile(r"^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$")
INLINE_LINK_RE = re.compile(r"!?\[([^\]]*)\]\([^)]*\)")
PLACEHOLDER = "\x00%d\x00"


def heading_slug_text(raw):
    """Plain text of a heading as GitHub renders it (inline markup removed)."""
    codes = []

    def keep_code(m):
        codes.append(m.group(2))
        return PLACEHOLDER % (len(codes) - 1)

    s = CODE_SPAN_RE.sub(keep_code, raw)
    s = INLINE_LINK_RE.sub(lambda m: m.group(1), s)
    s = re.sub(r"\[([^\]]*)\]\[[^\]]*\]", r"\1", s)
    s = re.sub(r"<[^>]+>", "", s)
    s = html.unescape(s)
    s = s.replace("*", "")
    s = re.sub(r"(?<![A-Za-z0-9])_+([^_].*?)_+(?![A-Za-z0-9])", r"\1", s)
    s = re.sub(r"\\([!-/:-@\[-`{-~])", r"\1", s)
    s = re.sub("\x00([0-9]+)\x00", lambda m: codes[int(m.group(1))], s)
    return s.strip()


def github_slug(text):
    out = []
    for ch in text.lower():
        if ch.isalnum() or ch in "-_":
            out.append(ch)
        elif ch == " ":
            out.append("-")
    return "".join(out)


def collect_anchors(heading_text, link_text):
    anchors = set()
    used = {}
    for line in heading_text.split("\n"):
        m = ATX_RE.match(line)
        if not m:
            continue
        slug = github_slug(heading_slug_text(m.group(2) or ""))
        if slug in used:
            n = used[slug]
            cand = "%s-%d" % (slug, n)
            while cand in anchors:
                n += 1
                cand = "%s-%d" % (slug, n)
            used[slug] = n + 1
            slug = cand
        else:
            used[slug] = 1
        anchors.add(slug)
    for m in re.finditer(r"<a\s[^>]*?\b(?:id|name)\s*=\s*(?:\"([^\"]+)\"|'([^']+)')", link_text, re.I):
        anchors.add(m.group(1) or m.group(2))
    return anchors


# --------------------------------------------------------------------------------------------------------------
# link extraction


def parse_inline_dest(s, i):
    """Parses the destination of an inline link; i is the index after "(". Returns (dest, end_index) or None."""
    n = len(s)
    while i < n and s[i] in " \t\n":
        i += 1
    if i < n and s[i] == "<":
        j = s.find(">", i + 1)
        if j < 0 or "\n" in s[i + 1 : j]:
            return None
        dest, j = s[i + 1 : j], j + 1
    else:
        depth = 0
        j = i
        while j < n:
            c = s[j]
            if c == "\\" and j + 1 < n:
                j += 2
                continue
            if c in " \t\n":
                break
            if c == "(":
                depth += 1
            elif c == ")":
                if depth == 0:
                    break
                depth -= 1
            j += 1
        dest = s[i:j]
    while j < n and s[j] in " \t\n":
        j += 1
    if j < n and s[j] in "\"'(":
        closer = {'"': '"', "'": "'", "(": ")"}[s[j]]
        k = s.find(closer, j + 1)
        if k < 0:
            return None
        j = k + 1
        while j < n and s[j] in " \t\n":
            j += 1
    if j >= n or s[j] != ")":
        return None
    return dest, j + 1


def unescape_dest(dest):
    return re.sub(r"\\([!-/:-@\[-`{-~])", r"\1", dest).strip()


REF_DEF_RE = re.compile(r"^ {0,3}\[(?!\^)[^\]\n]+\]:[ \t]*(<[^>\n]*>|\S+)", re.M)
HTML_ATTR_RE = re.compile(r"\b(href|src|srcset)\s*=\s*(?:\"([^\"]*)\"|'([^']*)')", re.I)


def extract_links(text):
    """Yields (line, destination) for every link in the (already blanked) text."""
    found = []

    def line_of(pos):
        return text.count("\n", 0, pos) + 1

    pos = 0
    while True:
        pos = text.find("](", pos)
        if pos < 0:
            break
        parsed = parse_inline_dest(text, pos + 2)
        if parsed:
            found.append((line_of(pos), unescape_dest(parsed[0])))
            pos = parsed[1]
        else:
            pos += 2
    for m in REF_DEF_RE.finditer(text):
        dest = m.group(1)
        if dest.startswith("<") and dest.endswith(">"):
            dest = dest[1:-1]
        found.append((line_of(m.start(1)), unescape_dest(dest)))
    for m in HTML_ATTR_RE.finditer(text):
        value = html.unescape(m.group(2) if m.group(2) is not None else m.group(3))
        if m.group(1).lower() == "srcset":
            for cand in value.split(","):
                parts = cand.split()
                if parts:
                    found.append((line_of(m.start()), parts[0]))
        else:
            found.append((line_of(m.start()), value.strip()))
    return found


# --------------------------------------------------------------------------------------------------------------
# link checking


class Checker:
    def __init__(self, root, assumed):
        self.root = root
        self.assumed = assumed
        self.findings = []
        self._dirs = {}
        self._anchors = {}
        self._targets = []  # (finding index placeholder data) for the ignore check

    def add(self, path, line, msg):
        self.findings.append((path, line, msg))

    def listdir(self, rel_dir):
        if rel_dir not in self._dirs:
            try:
                self._dirs[rel_dir] = set(os.listdir(os.path.join(self.root, rel_dir)))
            except OSError:
                self._dirs[rel_dir] = None
        return self._dirs[rel_dir]

    def exists_exact(self, rel):
        """True when rel exists with exactly this spelling (also on case-insensitive file systems)."""
        if rel in ("", "."):
            return True
        cur = ""
        for part in rel.split("/"):
            names = self.listdir(cur)
            if names is None or part not in names:
                return False
            cur = part if cur == "" else cur + "/" + part
        return True

    def is_assumed(self, rel):
        for a in self.assumed:
            if a == rel or a.startswith(rel.rstrip("/") + "/"):
                return True
        return False

    def anchors_of(self, rel):
        if rel not in self._anchors:
            headings, links = prepare(read_text(self.root, rel))
            self._anchors[rel] = collect_anchors(headings, links)
        return self._anchors[rel]

    def check_file(self, rel):
        headings, links = prepare(read_text(self.root, rel))
        self._anchors[rel] = collect_anchors(headings, links)
        for line, dest in extract_links(links):
            self.check_link(rel, line, dest)

    def check_link(self, rel, line, dest):
        if dest == "" or dest == "#":
            return
        if dest.startswith("//"):
            return
        m = re.match(r"^([A-Za-z][A-Za-z0-9+.\-]*):", dest)
        if m and m.group(1).lower() in SKIP_SCHEMES:
            return
        path, _, frag = dest.partition("#")
        path = path.split("?", 1)[0]
        path = urllib.parse.unquote(path)
        frag = urllib.parse.unquote(frag)
        if path.startswith("/"):
            self.add(rel, line, "absolute link %s (use a relative link)" % dest)
            return
        if path == "":
            target = rel
            trailing_slash = False
        else:
            trailing_slash = path.endswith("/")
            # An entry in changelog.d/ is folded into CHANGELOG.md later, so its links are relative to the root.
            base = "" if rel.startswith("changelog.d/") else posixpath.dirname(rel)
            target = posixpath.normpath(posixpath.join(base, path))
            if target == ".." or target.startswith("../"):
                self.add(rel, line, "link leaves the repository: %s" % dest)
                return
        exists = self.exists_exact(target)
        if not exists:
            if self.is_assumed(target):
                return
            self.add(rel, line, "broken link %s (no such file or directory)" % dest)
            return
        full = os.path.join(self.root, target)
        if trailing_slash and not os.path.isdir(full):
            self.add(rel, line, "link %s ends with / but is not a directory" % dest)
            return
        self._targets.append((rel, line, dest, target))
        if frag != "" and os.path.isfile(full) and target.lower().endswith(".md"):
            if frag not in self.anchors_of(target):
                self.add(rel, line, "broken anchor %s (no such heading or anchor in %s)" % (dest, target))

    def check_ignored(self):
        """Reports links to targets that git ignores."""
        uniq = sorted(set(t[3] for t in self._targets if t[3] not in ("", ".")))
        if not uniq:
            return
        proc = run_git(self.root, ["check-ignore", "-z", "--stdin"], stdin=("\0".join(uniq) + "\0").encode("utf-8"))
        if proc.returncode not in (0, 1):
            raise UsageError("git check-ignore failed: %s" % proc.stderr.decode("utf-8", "replace").strip())
        ignored = set(x.decode("utf-8", "replace") for x in proc.stdout.split(b"\0") if x)
        for rel, line, dest, target in self._targets:
            if target in ignored:
                self.add(rel, line, "link %s points to a path that git ignores" % dest)


def cmd_links(args):
    root = find_root(args.root)
    assumed = set()
    if args.assume_exists:
        try:
            with open(args.assume_exists, "r", encoding="utf-8") as fh:
                for raw in fh:
                    s = raw.strip()
                    if s and not s.startswith("#"):
                        assumed.add(posixpath.normpath(s.replace("\\", "/")))
        except OSError as exc:
            raise UsageError("cannot read --assume-exists file: %s" % exc)
    files = list_markdown(root)
    if not args.paths:
        files = [f for f in files if not f.startswith(WEBSITE_PREFIX)]
    files = narrow(root, files, args.paths)
    chk = Checker(root, assumed)
    for rel in files:
        chk.check_file(rel)
    chk.check_ignored()
    return chk.findings


# --------------------------------------------------------------------------------------------------------------
# front matter


FM_KEYS = ("title", "weight", "description")
# The website sources (--site): weight is optional and these keys may follow.
SITE_EXTRA_KEYS = ("linkTitle", "type", "layout", "params", "aliases", "toc_hide", "hide_summary", "no_list", "simple_list", "outputs", "cascade", "menu")
H1_RE = re.compile(r"^ {0,3}#[ \t]+\S")


def check_front_matter(rel, text, site=False):
    out = []
    lines = text.split("\n")
    if not lines or lines[0] != "---":
        return [(rel, 1, "missing front matter (the file must start with a line ---)")]
    end = None
    for i in range(1, len(lines)):
        if lines[i] == "---":
            end = i
            break
    if end is None:
        return [(rel, 1, "front matter is not closed (missing closing line ---)")]
    seen = []
    for i in range(1, end):
        line = lines[i]
        if line.strip() == "":
            out.append((rel, i + 1, "empty line inside the front matter"))
            continue
        if site and (line[0] in " \t-" or line.startswith("#")):
            continue  # a continuation of a structured value (params, cascade, ...) or a comment
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*):[ \t]*(.*)$", line)
        if not m:
            out.append((rel, i + 1, "front matter line is not 'key: value'"))
            continue
        key, value = m.group(1), m.group(2).rstrip()
        seen.append(key)
        if site:
            allowed = FM_KEYS + SITE_EXTRA_KEYS
        else:
            allowed = FM_KEYS
        if key not in allowed:
            out.append((rel, i + 1, "unexpected front matter key %s (allowed: %s)" % (key, ", ".join(allowed))))
        elif key in ("title", "description"):
            try:
                parsed = json.loads(value) if value.startswith('"') else None
            except ValueError:
                parsed = None
            if not isinstance(parsed, str):
                out.append((rel, i + 1, "%s must be a double-quoted string" % key))
            elif parsed.strip() == "":
                out.append((rel, i + 1, "%s must not be empty" % key))
        elif key == "weight":
            if not re.match(r"^-?[0-9]+$", value):
                out.append((rel, i + 1, "weight must be an integer"))
    required = ("title", "description") if site else FM_KEYS
    for key in required:
        if key not in seen:
            out.append((rel, 1, "front matter key %s is missing" % key))
    if len(seen) != len(set(seen)):
        out.append((rel, 2, "duplicate front matter key"))
    elif not out:
        # the three core keys keep their order; in site mode weight may be left out and other keys may follow or interleave
        core = tuple(k for k in seen if k in FM_KEYS)
        if core != tuple(k for k in FM_KEYS if k in core) or (not site and tuple(seen) != FM_KEYS):
            out.append((rel, 2, "front matter keys must appear in the order title, weight, description"))
    body = lines[:]
    for i in range(end + 1):
        body[i] = ""
    blank_fences(body)
    body = blank_comments("\n".join(body)).split("\n")
    for i, line in enumerate(body):
        if H1_RE.match(line):
            out.append((rel, i + 1, "the body repeats the title as a '# ' heading (sections start at ##)"))
    return out


def cmd_front_matter(args):
    root = find_root(args.root)
    paths = args.paths or (["website/content"] if args.site else ["docs"])
    files = narrow(root, list_markdown(root), paths)
    findings = []
    for rel in files:
        if not args.site and ADR_RE.match(rel):
            continue
        findings.extend(check_front_matter(rel, read_text(root, rel), site=args.site))
    return findings


# --------------------------------------------------------------------------------------------------------------


def build_parser():
    ap = argparse.ArgumentParser(
        prog="check-docs.py",
        description="Checks of the Markdown documentation (internal links, front matter).",
    )
    sub = ap.add_subparsers(dest="cmd")
    sub.required = True
    p = sub.add_parser("links", help="check internal links and anchors")
    p.add_argument("--root", help="repository root (default: the git work tree of the current directory)")
    p.add_argument("--assume-exists", metavar="FILE", help="file with repository-relative paths that count as existing")
    p.add_argument("paths", nargs="*", help="files or directories (relative to the root) that narrow the scope")
    p.set_defaults(func=cmd_links)
    p = sub.add_parser("front-matter", help="check the front matter of the pages below docs/ (or of the website sources)")
    p.add_argument("--root", help="repository root (default: the git work tree of the current directory)")
    p.add_argument("--site", action="store_true", help="rules of the website sources (default path: website/content)")
    p.add_argument("paths", nargs="*", help="files or directories (default: docs, with --site: website/content)")
    p.set_defaults(func=cmd_front_matter)
    return ap


def main(argv):
    ap = build_parser()
    args = ap.parse_args(argv)
    try:
        findings = args.func(args)
    except UsageError as exc:
        sys.stderr.write("check-docs: %s\n" % exc)
        return 2
    findings = sorted(set(findings), key=lambda f: (f[0], f[1], f[2]))
    for path, line, msg in findings:
        sys.stdout.write("%s:%d: %s\n" % (path, line, msg))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
