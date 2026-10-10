#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks the REST API reference of the built website against the committed contract (ADR-0031).

    python3 -I website/scripts/check-api-spec.py [website/public]

The reference is generated from api/openapi.yaml (make docs-gen, docs/reference/api) and rendered as ordinary pages: an index at
/docs/reference/api/, one page per tag and /docs/reference/api/data-types/. The check fails when

  - the index, the data types page or the page of a tag is missing (the tags are the generated files in docs/reference/api),
  - the index does not link exactly one fingerprinted /api/openapi.<hash>.yaml, that file is missing from the site, differs from
    api/openapi.yaml byte for byte, or is not the only contract file below /api/ (a stale copy would be a second contract),
  - a page loads a script whose name mentions an API viewer (redoc, swagger, scalar, rapidoc) or the site ships such a file,
  - the operation sections of the tag pages (each starts with "Operation <operationId>.") are not exactly the operations of
    api/openapi.yaml: a missing, an unknown or a duplicated operationId.

The operationIds of the contract are read from its text (stdlib only, no YAML parser). The web UI types (web/src/api/schema.d.ts)
are checked for drift by the web gate, not here. Output: one line per finding, then a summary line. Exit status 0 = clean,
1 = findings, 2 = environment error. Python 3.9 compatible, standard library only.
"""

import os
import re
import sys
from html.parser import HTMLParser

API_DIR = os.path.join("docs", "reference", "api")
INDEX = os.path.join(API_DIR, "index.html")
SPEC_URL = re.compile(r"^/api/openapi\.[0-9a-f]{16,}\.ya?ml$")
OPERATION = re.compile(r"Operation <code>([A-Za-z0-9_.-]+)</code>\.")
OPERATION_ID = re.compile(r"^\s{2,}operationId:\s*['\"]?([A-Za-z0-9_.-]+)['\"]?\s*$", re.M)
VIEWER = re.compile(r"redoc|swagger|scalar|rapidoc", re.I)


def website_dir():
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class Links(HTMLParser):
    """Collects the references to contract files and the scripts of a page."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.spec_refs = []  # every href or src that points below /api/ and ends in .yaml or .yml
        self.scripts = []  # src of every script element with one

    def handle_starttag(self, tag, attrs):
        a = dict((k, v if v is not None else "") for k, v in attrs)
        for key in ("href", "src"):
            value = a.get(key, "")
            # An anchor to the file on GitHub (the page's "view source" link) is a link, not the contract the site offers.
            if key == "href" and re.match(r"^(?:[a-z][a-z0-9+.-]*:|//)", value, re.I):
                continue
            if value.endswith((".yaml", ".yml")) and "/api/" in value:
                self.spec_refs.append(value)
        if tag == "script" and a.get("src"):
            self.scripts.append(a["src"])


def read(path):
    with open(path, "rb") as fh:
        return fh.read()


def local_path(site, url):
    return os.path.join(site, url.lstrip("/").replace("/", os.sep))


def tag_pages(repo):
    """The stems of the generated tag pages: docs/reference/api/<stem>.md except the index and the data types page."""
    api = os.path.join(repo, "docs", "reference", "api")
    if not os.path.isdir(api):
        return []
    return sorted(n[:-3] for n in os.listdir(api) if n.endswith(".md") and n not in ("_index.md", "data-types.md"))


def contract_operations(repo):
    text = read(os.path.join(repo, "api", "openapi.yaml")).decode("utf-8")
    return OPERATION_ID.findall(text)


def page_text(path):
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        return fh.read()


def check(site, repo):
    """Returns (number of operations in the contract, findings). site is the built site, repo the repository root."""
    findings = []
    idx = os.path.join(site, INDEX)
    if not os.path.isfile(idx):
        return 0, ["%s: the API reference page is missing" % INDEX.replace(os.sep, "/")]
    page = INDEX.replace(os.sep, "/")

    # the pages
    stems = tag_pages(repo) + ["data-types"]
    if len(stems) < 2:
        findings.append("docs/reference/api: no generated tag page in the repository (run make docs-gen)")
    for stem in stems:
        rel = os.path.join(API_DIR, stem, "index.html")
        if not os.path.isfile(os.path.join(site, rel)):
            findings.append("%s: the page is missing" % rel.replace(os.sep, "/"))

    # one contract
    parser = Links()
    parser.feed(page_text(idx))
    refs = sorted(set(parser.spec_refs))
    if len(refs) != 1:
        findings.append("%s: the page must link exactly one contract file, found %d (%s)" % (page, len(refs), ", ".join(refs) or "none"))
    spec = read(os.path.join(repo, "api", "openapi.yaml"))
    for ref in refs:
        if not SPEC_URL.match(ref):
            findings.append("%s: the contract link %s is not a local fingerprinted file below /api/" % (page, ref))
            continue
        path = local_path(site, ref)
        if not os.path.isfile(path):
            findings.append("%s: the contract file %s is not in the site" % (page, ref))
        elif read(path) != spec:
            findings.append("%s: %s differs from api/openapi.yaml" % (page, ref))
    api_dir = os.path.join(site, "api")
    if os.path.isdir(api_dir):
        files = sorted(n for n in os.listdir(api_dir) if n.endswith((".yaml", ".yml", ".json")))
        if len(files) > 1:
            findings.append("api/: more than one contract file in the site (%s)" % ", ".join(files))

    # no API viewer
    for base, dirs, names in os.walk(site):
        dirs.sort()
        for name in sorted(names):
            full = os.path.join(base, name)
            rel = os.path.relpath(full, site).replace(os.sep, "/")
            if name.endswith((".js", ".mjs")) and VIEWER.search(name):
                findings.append("%s: a file of an API viewer is part of the site" % rel)
            if name.endswith(".html"):
                links = Links()
                links.feed(page_text(full))
                for src in links.scripts:
                    if VIEWER.search(src):
                        findings.append("%s: loads the script %s; the API reference is static and loads no viewer" % (rel, src))

    # the operations
    want = contract_operations(repo)
    if not want:
        findings.append("api/openapi.yaml: no operationId found")
    seen = {}
    for stem in stems:
        if stem == "data-types":
            continue
        path = os.path.join(site, API_DIR, stem, "index.html")
        if not os.path.isfile(path):
            continue
        for op in OPERATION.findall(page_text(path)):
            seen.setdefault(op, []).append(stem)
    for op in sorted(set(want) - set(seen)):
        findings.append("%s: the operation %s of the contract has no section on a tag page" % (page, op))
    for op in sorted(set(seen) - set(want)):
        findings.append("%s: the tag page %s has a section for %s, which is not in the contract" % (page, seen[op][0], op))
    for op in sorted(seen):
        if len(seen[op]) > 1:
            findings.append("%s: the operation %s has more than one section (%s)" % (page, op, ", ".join(seen[op])))
    for op in sorted(set(want)):
        if want.count(op) > 1:
            findings.append("api/openapi.yaml: the operationId %s is not unique" % op)
    return len(set(want)), findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-api-spec.py [website/public]", file=sys.stderr)
        return 2
    web = website_dir()
    site = argv[0] if argv else os.path.join(web, "public")
    if not os.path.isdir(site):
        print("check-api-spec: %s is not a directory (build the site first: make website)" % site, file=sys.stderr)
        return 2
    repo = os.path.dirname(web)
    if not os.path.isfile(os.path.join(repo, "api", "openapi.yaml")):
        print("check-api-spec: api/openapi.yaml not found", file=sys.stderr)
        return 2
    try:
        _, findings = check(site, repo)
    except OSError as exc:
        print("check-api-spec: %s" % exc, file=sys.stderr)
        return 2
    for line in sorted(set(findings)):
        print(line)
    print("check-api-spec: %d findings" % len(set(findings)))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
