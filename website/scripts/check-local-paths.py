#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks that the built website contains no path of the machine that built it (privacy rule of ADR-0031).

    python3 -I website/scripts/check-local-paths.py [website/public]

Hugo knows the absolute name of every file it reads. Templates that print it (an "edit this page" link built from
File.Filename, for example) would publish the layout of the build machine and the user name. The check fails when a text
file of the built site contains

  - the absolute path of the repository root (the parent of the website directory),
  - /Users/ or /home/ followed by a name, or the drive form C:\\Users\\ (user directories of macOS, Linux and Windows) -
    except in the self-hosted libraries under vendor/, where such strings may be example text.

The output names the file and the kind of finding, never the matched text beyond that. Exit status 0 = clean, 1 = findings,
2 = usage error. Python 3.9 compatible, standard library only.
"""

import os
import re
import sys

TEXT_EXTENSIONS = {".html", ".css", ".js", ".mjs", ".json", ".xml", ".txt", ".svg", ".webmanifest", ".map", ".md", ".yaml", ".yml"}
USER_DIR = re.compile(r"(?:/Users/|/home/)[A-Za-z0-9._-]+|[A-Za-z]:\\\\?Users\\\\?[A-Za-z0-9._-]+")
# example paths inside the self-hosted third-party libraries (Mermaid, lunr) are not ours; the repository root is checked everywhere
LIBRARY_PREFIXES = ("vendor/",)


def repo_root():
    here = os.path.dirname(os.path.abspath(__file__))
    return os.path.dirname(os.path.dirname(here))


def scan(root, repo):
    """Returns (files scanned, findings as (relative path, kind))."""
    findings = []
    scanned = 0
    repo = repo.rstrip("/")
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for name in sorted(files):
            if os.path.splitext(name)[1].lower() not in TEXT_EXTENSIONS:
                continue
            path = os.path.join(base, name)
            rel = os.path.relpath(path, root).replace(os.sep, "/")
            with open(path, "rb") as fh:
                text = fh.read().decode("utf-8", errors="replace")
            scanned += 1
            if repo and repo != "/" and repo in text:
                findings.append((rel, "contains the absolute path of the repository"))
            if not rel.startswith(LIBRARY_PREFIXES) and USER_DIR.search(text):
                findings.append((rel, "contains a user directory path"))
    return scanned, findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-local-paths.py [website/public]", file=sys.stderr)
        return 2
    root = argv[0] if argv else "website/public"
    if not os.path.isdir(root):
        print("check-local-paths: %s is not a directory (build the site first: make website)" % root, file=sys.stderr)
        return 2
    scanned, findings = scan(root, repo_root())
    for rel, kind in sorted(set(findings)):
        print("%s: %s" % (rel, kind))
    print("check-local-paths: %d files scanned, %d findings" % (scanned, len(set(findings))))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
