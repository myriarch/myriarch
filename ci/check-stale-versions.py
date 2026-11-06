#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Fails when a text promises a release that already exists (run by ci/check-versions.sh).

    python3 -I ci/check-stale-versions.py [--root DIR] VERSION

A sentence such as "planned for v0.2.0" or "until v0.2.0" is true while the release is still ahead. The pull request that
prepares the release (VERSION becomes X.Y.Z) has to turn it into text that holds afterwards, so this check looks at two kinds of
promise:

  * the phrases "planned for vX.Y.Z" and "until vX.Y.Z" in the tracked text files (.md, .yaml, .yml, .html, .txt, .gotmpl, .toml),
    with a version up to and including VERSION. Case does not matter, a line break (also one with a "> ", "# " or "| " marker in
    front of the next line) and a non-breaking space count as a space, and the words "the", "release", "version" and "of" after
    the phrase and Markdown or HTML around the version (a link, backticks, bold, italics, any tag) do not hide it. A release candidate tag (vX.Y.Z-rc.N) is
    another version and is not matched. Not matched either: a range ("v0.1.0-v0.2.0") and a version without a patch number.
  * the field `planned` of a placeholder page below website/content (website/scripts/check-placeholders.py), judged by the same
    rule: the page was promised for a release that exists, so write it or move the promise.

For a release candidate X.Y.Z-rc.N, X.Y.Z is still ahead, so only the promises of older versions count.

Not read: docs/adr (accepted records are not rewritten), CHANGELOG.md and changelog.d (they describe the past) and every directory
named testdata.

A false alarm is solved by rewording the text, not by a list of exceptions: "Before v0.2.0 the chart had no schema" says the same
as "Until v0.2.0 ..." and is not matched. Only the files that git tracks are read (git ls-files in the root; outside a git work tree, every file).
"""

import os
import re
import subprocess
import sys

TEXT_SUFFIXES = (".md", ".yaml", ".yml", ".html", ".txt", ".gotmpl", ".toml")
FINAL_RE = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
RC_RE = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$")

# A gap between two words of a phrase: blanks, or a line break with the comment or quote markers of the next line.
GAP = r"(?:[ \t\u00a0]+|[ \t\u00a0]*\r?\n[ \t\u00a0]*(?:(?:>|#|//|\*|\|)[ \t\u00a0]*)*)"
# Markdown or HTML that may stand before the v: any opening tag, a link bracket, backticks, emphasis, a parenthesis or a quote.
MARK = r"(?:<[a-zA-Z][^<>]*>|[\[`*_(\"'])*"
# What may stand between the phrase and the v: "the", "release", "version" and "of" (as in "the release of v0.2.0"), each with
# Markup before or after it.
WRAP = MARK + r"(?:(?:the|release|version|of)" + GAP + MARK + r")*"
PHRASE_RE = re.compile(
    r"(?<![A-Za-z0-9_])(planned" + GAP + r"for|until)" + GAP + WRAP + r"v(\d+)\.(\d+)\.(\d+)(?![0-9]|\.[0-9]|-)",
    re.IGNORECASE,
)
PLANNED_RE = re.compile(r"""^\s*planned:\s*["']?v?(\d+)\.(\d+)\.(\d+)["']?\s*(?:#.*)?$""")

HOW = ("Write the page or change the sentence in the pull request that prepares the release: say what is true now, in words that "
       "need no version (for example \"is not part of a release yet\", or \"Before v1.2.3 ...\" for the past).")


def fail(msg, code=1):
    print("check-stale-versions: " + msg, file=sys.stderr)
    sys.exit(code)


def tracked_files(root):
    """The files that git tracks; outside a git work tree (a source archive) every file below root except .git and node_modules."""
    try:
        out = subprocess.run(["git", "-C", root, "ls-files", "-z"], check=True, capture_output=True).stdout
    except (OSError, subprocess.CalledProcessError):
        found = []
        for base, dirs, files in os.walk(root):
            dirs[:] = [d for d in dirs if d not in (".git", "node_modules")]
            found += [os.path.relpath(os.path.join(base, f), root).replace(os.sep, "/") for f in files]
        return sorted(found)
    return [p for p in out.decode("utf-8", "surrogateescape").split("\0") if p]


def excluded(path):
    parts = path.split("/")
    return (path == "CHANGELOG.md" or path.startswith("docs/adr/") or path.startswith("changelog.d/") or "testdata" in parts[:-1])


def read(root, path):
    try:
        with open(os.path.join(root, path), encoding="utf-8") as f:
            return f.read()
    except (OSError, UnicodeDecodeError):
        return None


def front_matter(text):
    """The lines of the YAML front matter with their line numbers (empty when there is none)."""
    lines = text.split("\n")
    if not lines or lines[0].strip() != "---":
        return []
    out = []
    for i, line in enumerate(lines[1:], start=2):
        if line.strip() == "---":
            return out
        out.append((i, line))
    return []


def main(argv):
    root = "."
    args = list(argv)
    if len(args) >= 2 and args[0] == "--root":
        root, args = args[1], args[2:]
    if len(args) != 1:
        fail("usage: check-stale-versions.py [--root DIR] VERSION", 2)
    version = args[0]
    rc = RC_RE.match(version)
    if not rc and not FINAL_RE.match(version):
        fail("invalid version %r (X.Y.Z or X.Y.Z-rc.N)" % version, 2)
    # A release candidate of X.Y.Z has not released X.Y.Z: only the promises of older versions are stale.
    limit = tuple(int(x) for x in (rc or FINAL_RE.match(version)).groups())
    below = bool(rc)

    def released(found):
        return found < limit or (found == limit and not below)

    problems = []
    for path in tracked_files(root):
        if excluded(path):
            continue
        in_content = path.startswith("website/content/") and path.endswith(".md")
        if not (path.endswith(TEXT_SUFFIXES)):
            continue
        text = read(root, path)
        if text is None:
            continue
        for hit in PHRASE_RE.finditer(text):
            found = tuple(int(x) for x in hit.groups()[1:])
            if released(found):
                line = text.count("\n", 0, hit.start()) + 1
                said = re.sub(r"\s+", " ", re.sub(r"\s*\n[ \t]*(?:(?:>|#|//|\*|\|)[ \t\u00a0]*)*", " ", hit.group(0)))
                problems.append("%s:%d: says \"%s\", but v%d.%d.%d is released (VERSION is %s)" % ((path, line, said) + found + (version,)))
        if in_content:
            placeholder = False
            for number, line in front_matter(text):
                if re.match(r"^\s*placeholder:\s*(?:#.*)?$", line):
                    placeholder = True
                    continue
                hit = PLANNED_RE.match(line)
                if placeholder and hit and released(tuple(int(x) for x in hit.groups())):
                    found = tuple(int(x) for x in hit.groups())
                    problems.append("%s:%d: the placeholder page is planned for v%d.%d.%d, but that release exists (VERSION is %s)"
                                    % ((path, number) + found + (version,)))

    if problems:
        for p in problems:
            print("check-stale-versions: " + p, file=sys.stderr)
        print("check-stale-versions: %d stale promise(s). %s" % (len(problems), HOW), file=sys.stderr)
        return 1
    print("check-stale-versions: no text promises a release up to %s" % version, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
