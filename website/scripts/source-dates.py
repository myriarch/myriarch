#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Writes website/data/source_dates.json: the date of the last commit that changed each source file of a content adapter.

    python3 -I website/scripts/source-dates.py [--root DIR] [--output FILE]

Hugo takes the date of an ordinary page (lastmod of the sitemap, modified time of the metadata) from Git (enableGitInfo in hugo.yaml).
The pages of the content adapters (the decision records, the root files, the READMEs that data/repo_pages.yaml lists) are not files of
the site for Hugo, so it knows no commit for them; the adapters read this file instead. The dates are those of the commit, in UTC, never
the time of the build, so two builds of one commit write the same file. The file is generated and ignored by Git.

Sources: every docs/adr/NNNN-*.md and every "source:" of website/data/repo_pages.yaml. A source without a commit (a new, uncommitted file)
gets no entry and therefore no lastmod, which check-seo.py reports. The script refuses a shallow clone: every file would carry the date
of the one commit that it holds. Exit status 0 = written, 2 = usage or environment error. Python 3.9 compatible, standard library only.
"""

import datetime
import glob
import json
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_ROOT = os.path.dirname(os.path.dirname(HERE))
ADR = re.compile(r"^docs/adr/[0-9]{4}-[^/]+\.md$")
SOURCE = re.compile(r"^\s*-?\s*source:\s*(\S+)\s*$")


class Error(Exception):
    pass


def git(root, *args):
    try:
        proc = subprocess.run(["git", "-C", root] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    except OSError as exc:
        raise Error("cannot run git: %s" % exc)
    if proc.returncode != 0:
        raise Error("git %s failed: %s" % (" ".join(args), proc.stderr.decode("utf-8", "replace").strip()))
    return proc.stdout.decode("utf-8", "replace").strip()


def sources(root):
    found = set()
    for path in glob.glob(os.path.join(root, "docs", "adr", "[0-9][0-9][0-9][0-9]-*.md")):
        rel = os.path.relpath(path, root).replace(os.sep, "/")
        if ADR.match(rel) and not rel.startswith("docs/adr/0000-"):
            found.add(rel)
    pages = os.path.join(root, "website", "data", "repo_pages.yaml")
    try:
        with open(pages, encoding="utf-8") as fh:
            for line in fh:
                m = SOURCE.match(line.split("#", 1)[0])
                if m:
                    found.add(m.group(1))
    except OSError as exc:
        raise Error("cannot read %s: %s" % (pages, exc))
    return sorted(found)


def commit_date(root, rel):
    """The date of the last commit that changed the file, as UTC with a Z, or None when no commit has."""
    out = git(root, "log", "-1", "--format=%cI", "--", rel)
    if not out:
        return None
    when = datetime.datetime.fromisoformat(out.replace("Z", "+00:00"))
    return when.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def build(root):
    if git(root, "rev-parse", "--is-shallow-repository") == "true":
        raise Error("this is a shallow clone: every file would get the date of the commit that it holds. Fetch the full history "
                    "(git fetch --unshallow; actions/checkout with fetch-depth: 0)")
    dates = {}
    for rel in sources(root):
        date = commit_date(root, rel)
        if date:
            dates[rel] = date
    return dates


def main(argv):
    args = list(argv)
    root, output = DEFAULT_ROOT, None
    for option in ("--root", "--output"):
        if option in args:
            i = args.index(option)
            if i + 1 >= len(args):
                print("usage: python3 -I website/scripts/source-dates.py [--root DIR] [--output FILE]", file=sys.stderr)
                return 2
            value = args[i + 1]
            del args[i:i + 2]
            if option == "--root":
                root = value
            else:
                output = value
    if args:
        print("usage: python3 -I website/scripts/source-dates.py [--root DIR] [--output FILE]", file=sys.stderr)
        return 2
    output = output or os.path.join(root, "website", "data", "source_dates.json")
    try:
        dates = build(root)
        os.makedirs(os.path.dirname(output), exist_ok=True)
        with open(output, "w", encoding="utf-8") as fh:
            json.dump(dates, fh, indent=1, sort_keys=True)
            fh.write("\n")
    except (Error, OSError) as exc:
        print("source-dates: %s" % exc, file=sys.stderr)
        return 2
    print("source-dates: %d dates written to %s" % (len(dates), os.path.relpath(output, root)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
