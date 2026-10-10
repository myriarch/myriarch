#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks that every link of the built website into the project repository points at a path that exists (ADR-0031).

    python3 -I website/scripts/check-repo-links.py [website/public]

The link render hook maps a relative link of a repository Markdown file that is not a page of the site to a GitHub URL
(<repo>/blob|tree/<branch>/<path>), and the "edit this page" links use <repo>/edit/<branch>/<path>. lychee runs offline and
skips external URLs, so a typo, a deleted or a renamed file would give a link that returns 404 once the repository is public.
The check collects every such URL from the HTML files of the built site and fails when the path does not exist in the working
tree of the repository (the parent of the website directory), or when a "blob" link points at a directory or a "tree" link at
a file. Fragments are not checked. Repository URL and branch come from website/config/_default/params.yaml.

Exit status 0 = clean, 1 = findings, 2 = usage or environment error. Python 3.9 compatible, standard library only.
"""

import os
import re
import sys
import urllib.parse

HREF = re.compile(r'''\bhref=(?:"([^"]*)"|'([^']*)'|([^\s>"']+))''')


def website_dir():
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def repo_settings(params_text):
    """Returns (url, branch) of the repo block of params.yaml, or (None, None)."""
    m = re.search(r"^repo:\s*\n((?:[ \t]+.*\n?|\n)*)", params_text, re.M)
    if not m:
        return None, None
    block = m.group(1)
    url = re.search(r"^\s+url:\s*(\S+)", block, re.M)
    branch = re.search(r"^\s+branch:\s*(\S+)", block, re.M)
    return (url.group(1).rstrip("/") if url else None), (branch.group(1) if branch else None)


def repo_links(html, prefix, branch):
    """Yields (kind, repository path) of every href into the repository on the branch in an HTML text. prefix is '<repo url>/'."""
    for m in HREF.finditer(html):
        href = (m.group(1) or m.group(2) or m.group(3) or "").replace("&amp;", "&")
        if not href.startswith(prefix):
            continue
        rest = href[len(prefix):].split("#", 1)[0].split("?", 1)[0]
        parts = rest.split("/", 2)
        if len(parts) < 3 or parts[0] not in ("blob", "tree", "edit") or parts[1] != branch:
            continue
        yield parts[0], urllib.parse.unquote(parts[2]).rstrip("/")


def scan(root, repo, url, branch):
    """Returns (links checked, findings as (page, kind, path, problem))."""
    prefix = "%s/" % url
    findings, checked = [], 0
    seen = set()
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            if not name.endswith(".html"):
                continue
            path = os.path.join(base, name)
            rel = os.path.relpath(path, root).replace(os.sep, "/")
            with open(path, "rb") as fh:
                html = fh.read().decode("utf-8", errors="replace")
            for kind, target in repo_links(html, prefix, branch):
                key = (kind, target)
                checked += 1
                if not target or key in seen:
                    continue
                seen.add(key)
                full = os.path.join(repo, target)
                if not os.path.exists(full):
                    findings.append((rel, kind, target, "does not exist"))
                elif kind == "blob" and os.path.isdir(full):
                    findings.append((rel, kind, target, "is a directory (blob link)"))
                elif kind in ("tree",) and os.path.isfile(full):
                    findings.append((rel, kind, target, "is a file (tree link)"))
    return checked, findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-repo-links.py [website/public]", file=sys.stderr)
        return 2
    root = argv[0] if argv else "website/public"
    if not os.path.isdir(root):
        print("check-repo-links: %s is not a directory (build the site first: make website)" % root, file=sys.stderr)
        return 2
    try:
        with open(os.path.join(website_dir(), "config", "_default", "params.yaml"), "r", encoding="utf-8") as fh:
            url, branch = repo_settings(fh.read())
    except OSError as exc:
        print("check-repo-links: %s" % exc, file=sys.stderr)
        return 2
    if not url or not branch:
        print("check-repo-links: params.repo.url and params.repo.branch are not set in config/_default/params.yaml", file=sys.stderr)
        return 2
    repo = os.path.dirname(website_dir())
    checked, findings = scan(root, repo, url, branch)
    for rel, kind, target, problem in sorted(set(findings)):
        print("%s: %s link to %s %s" % (rel, kind, target, problem))
    print("check-repo-links: %d repository links checked, %d findings" % (checked, len(set(findings))))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
