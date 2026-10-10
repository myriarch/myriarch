#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""The places that state the product version, in one table (used by scripts/set-version.sh and ci/check-versions.sh).

    python3 -I ci/version-copies.py [--root DIR] list
    python3 -I ci/version-copies.py [--root DIR] read
    python3 -I ci/version-copies.py [--root DIR] check VERSION
    python3 -I ci/version-copies.py [--root DIR] write VERSION

The file VERSION at the repository root is the source ("one version source"). Every entry of COPIES names a file, a
description and a regular expression with exactly one capture group, the version. A copy must match exactly once: no match
or several matches is an error, so a changed layout of a file is noticed instead of silently skipped. `write` replaces the
captured text only. `check` prints one line per copy that differs and exits with 1; usage errors exit with 2.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# (path relative to the root, what it is, pattern with one capture group)
COPIES = [
    ("deploy/helm/myriarch/Chart.yaml", "Helm chart version",
     r'^version: ([^\s]+)[ \t]*$'),
    ("deploy/helm/myriarch/Chart.yaml", "Helm chart appVersion (default image tag)",
     r'^appVersion: "([^"\s]+)"[ \t]*$'),
    ("web/package.json", "web UI package version",
     r'^  "version": "([^"\s]+)",[ \t]*$'),
    ("api/openapi.yaml", "OpenAPI info.version (contract version of the REST API)",
     r'^info:\n(?:[ ]{2}.*\n)*?[ ]{2}version: ([^\s]+)[ \t]*$'),
    ("deploy/examples/production/myriarch-values.yaml", "image tag of the production example",
     r'^  tag: "([^"\s]+)"[ \t]*$'),
    ("gitops-template/.gitlab-ci.yml", "myrctl image tag of the GitOps template",
     r'^  MYRCTL_IMAGE: [^\s:]+:([^\s]+)[ \t]*$'),
]

VERSION_RE = re.compile(r'^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$')


def fail(msg, code=1):
    print("version-copies: " + msg, file=sys.stderr)
    sys.exit(code)


def read_text(root, rel):
    try:
        with open(os.path.join(root, rel), "r", encoding="utf-8", newline="") as f:
            return f.read()
    except OSError as e:
        fail("%s: cannot read (%s)" % (rel, e.strerror))


def locate(text, rel, desc, pattern):
    """The single match of the pattern in the text, or a failure."""
    found = list(re.finditer(pattern, text, re.MULTILINE))
    if len(found) != 1:
        fail("%s: expected exactly one place for the %s, found %d (ci/version-copies.py)" % (rel, desc, len(found)))
    return found[0]


def main(argv):
    root = ROOT
    args = argv[1:]
    if len(args) >= 2 and args[0] == "--root":
        root, args = os.path.abspath(args[1]), args[2:]
    if not args:
        fail("usage: version-copies.py [--root DIR] list|read|check VERSION|write VERSION", 2)
    cmd, rest = args[0], args[1:]

    if cmd in ("list", "read") and not rest:
        for rel, desc, pat in COPIES:
            if cmd == "list":
                print(rel)
            else:
                m = locate(read_text(root, rel), rel, desc, pat)
                print("%s\t%s\t%s" % (rel, desc, m.group(1)))
        return 0

    if cmd in ("check", "write") and len(rest) == 1:
        version = rest[0]
        if not VERSION_RE.fullmatch(version):
            fail("invalid version %r (expected X.Y.Z or X.Y.Z-rc.N, no leading zeros)" % version, 2)
        for rel, desc, pat in COPIES:   # every copy must be found before anything is written
            locate(read_text(root, rel), rel, desc, pat)
        bad = 0
        for rel, desc, pat in COPIES:
            text = read_text(root, rel)
            m = locate(text, rel, desc, pat)
            if m.group(1) == version:
                continue
            if cmd == "check":
                print("version-copies: %s: %s is %s, VERSION is %s" % (rel, desc, m.group(1), version), file=sys.stderr)
                bad = 1
            else:
                new = text[:m.start(1)] + version + text[m.end(1):]
                with open(os.path.join(root, rel), "w", encoding="utf-8", newline="") as f:
                    f.write(new)
                print("version-copies: %s: %s %s -> %s" % (rel, desc, m.group(1), version))
        return bad

    fail("usage: version-copies.py [--root DIR] list|read|check VERSION|write VERSION", 2)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
