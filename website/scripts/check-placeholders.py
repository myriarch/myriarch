#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks the placeholder pages of the website (ADR-0031).

    python3 -I website/scripts/check-placeholders.py [website/public]

A placeholder is a page in website/content that stands in for a page that a later change will write. Its front matter says
which repository file it replaces and under which topic or issue the page is tracked:

    params:
      placeholder:
        tracking: "operations-manual"
        replaces: "docs/operations/upgrade.md"
        planned: "v0.2.0"

The author of the real page adds the file named in `replaces`; the placeholder must go in the same change, because two pages for one
URL path would duplicate the content. The check fails when

  - the file named in `replaces` already exists in the repository (delete the placeholder),
  - `replaces` is not a repository path below docs/ ending in .md, or `tracking` is missing or neither a lowercase
    topic slug (operations-manual) nor a GitHub issue reference (#123),
  - the optional `planned` is not a release (v0.2.0); the notice then says that the page is planned for that release,
  - the placeholder does not live at the website path that the replaced file would get (docs/x/y.md -> website/content/docs/x/y.md),
  - the body does not use the placeholder shortcode (the shortcode renders the notice; the tracking value is never printed).

The argument (the built site) is accepted for the common calling convention of the checks and not used. Output: paths and counts.
Exit status 0 = clean, 1 = findings, 2 = environment error. Python 3.9 compatible, standard library only.
"""

import os
import re
import sys


def website_dir():
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def front_matter(text):
    """Returns (front matter lines, body) or (None, text)."""
    lines = text.split("\n")
    if not lines or lines[0] != "---":
        return None, text
    for i in range(1, len(lines)):
        if lines[i] == "---":
            return lines[1:i], "\n".join(lines[i + 1:])
    return None, text


def placeholder_of(fm_lines):
    """Returns {"tracking": ..., "replaces": ...} (values may be missing) or None when the page has no placeholder."""
    out, inside, indent = None, False, 0
    for line in fm_lines:
        if re.match(r"^\s*placeholder:\s*$", line):
            out, inside = {}, True
            indent = len(line) - len(line.lstrip())
            continue
        if inside:
            if not line.strip():
                continue
            cur = len(line) - len(line.lstrip())
            if cur <= indent:
                inside = False
                continue
            m = re.match(r"^\s+(tracking|replaces|planned):\s*(.*?)\s*$", line)
            if m:
                value = m.group(2)
                if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                    value = value[1:-1]
                elif value.startswith("#"):
                    # YAML (and so Hugo) reads an unquoted # as the start of a comment: the value would be null
                    out["unquoted_" + m.group(1)] = True
                out[m.group(1)] = value
    return out


def check(site, repo):
    """Returns (number of placeholders, findings)."""
    findings = []
    count = 0
    content = os.path.join(site, "content")
    for base, dirs, files in os.walk(content):
        dirs.sort()
        for name in sorted(files):
            if not name.endswith(".md"):
                continue
            path = os.path.join(base, name)
            rel = os.path.relpath(path, repo).replace(os.sep, "/")
            with open(path, "r", encoding="utf-8") as fh:
                fm, body = front_matter(fh.read())
            if fm is None:
                continue
            ph = placeholder_of(fm)
            if ph is None:
                continue
            count += 1
            replaces = ph.get("replaces", "")
            if ph.get("unquoted_tracking"):
                findings.append("%s: placeholder.tracking must be quoted when it is an issue reference (tracking: \"#123\"); an unquoted # starts a YAML comment" % rel)
            elif not ph.get("tracking"):
                findings.append("%s: placeholder.tracking is missing" % rel)
            elif not re.match(r"^([a-z][a-z0-9]*(-[a-z0-9]+)*|#[0-9]+)$", ph["tracking"]):
                findings.append("%s: placeholder.tracking must be a lowercase topic slug such as operations-manual or a GitHub issue reference such as #123" % rel)
            if ph.get("planned") and not re.match(r"^v[0-9]+\.[0-9]+\.[0-9]+$", ph["planned"]):
                findings.append("%s: placeholder.planned must be a release such as v0.2.0" % rel)
            if not re.match(r"^docs/[A-Za-z0-9._/-]+\.md$", replaces):
                findings.append("%s: placeholder.replaces must be a repository path below docs/ ending in .md" % rel)
                continue
            if os.path.exists(os.path.join(repo, replaces)):
                findings.append("%s: %s exists now; delete the placeholder" % (rel, replaces))
            want = "website/content/" + replaces
            if rel != want:
                findings.append("%s: a placeholder for %s must live at %s" % (rel, replaces, want))
            if "{{< placeholder >}}" not in body:
                findings.append("%s: the body must use the placeholder shortcode" % rel)
    return count, findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-placeholders.py [website/public]", file=sys.stderr)
        return 2
    site = website_dir()
    try:
        count, findings = check(site, os.path.dirname(site))
    except OSError as exc:
        print("check-placeholders: %s" % exc, file=sys.stderr)
        return 2
    for line in sorted(set(findings)):
        print(line)
    print("check-placeholders: %d placeholders checked, %d findings" % (count, len(set(findings))))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
