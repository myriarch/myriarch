#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks that every shipped dependency of the website has a third-party notice (ADR-0031, ADR-0030).

    python3 -I website/scripts/check-notices.py [website/public]

website/data/third_party.yaml is the source of the page /legal/third-party/. The check fails when

  - a dependency of website/package.json (dependencies, not devDependencies) or the Docsy module of website/go.mod has no entry
    in the file, or the entry names another version,
  - an entry names a package that is not pinned (a leftover after a removal), or lacks one of the fields name, package, version,
    license, url, use, source and license_file,
  - the built site (the argument) has no non-empty file at the license_file path of an entry (MIT and OFL-1.1 require the license
    text to accompany every copy, so the texts are published under /licenses/),
  - a version in package.json is not an exact version (a range would let the shipped version drift from the notice),
  - the version of the Mermaid plugin in config/_default/params.yaml differs from the one in package.json (Docsy loads the plugin
    with this version; the self-hosted file comes from node_modules),
  - a devDependency is not named in content/legal/third-party.md, where the build tools are listed.

Without the built site (the directory does not exist) only the sources are checked. Output: names and counts. Exit status 0 = clean, 1 = findings, 2 = environment error. Python 3.9 compatible, standard library only.
"""

import json
import os
import re
import sys

FIELDS = ("name", "package", "version", "license", "url", "use", "source", "license_file")
EXACT = re.compile(r"^\d+\.\d+\.\d+$")
DOCSY = "github.com/docsy/docsy/theme"


def website_dir():
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def parse_entries(text):
    """Parses the list in data/third_party.yaml (a list of flat mappings, scalars only). Returns a list of dicts."""
    entries, cur = [], None
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        m = re.match(r"^- ([A-Za-z_]+):\s*(.*)$", raw)
        if m:
            cur = {}
            entries.append(cur)
        else:
            m = re.match(r"^  ([A-Za-z_]+):\s*(.*)$", raw)
        if not m or cur is None:
            raise ValueError("unsupported line in third_party.yaml: %r" % raw[:40])
        value = m.group(2).strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        cur[m.group(1)] = value
    return entries


def go_mod_versions(text):
    """Returns {module: version} of the require lines of a go.mod."""
    out = {}
    for line in text.splitlines():
        line = line.split("//")[0].strip()
        m = re.match(r"^(?:require\s+)?([A-Za-z0-9._~/-]+\.[A-Za-z0-9._~/-]+)\s+(v[0-9][^\s]*)$", line)
        if m:
            out[m.group(1)] = m.group(2)
    return out


def mermaid_plugin_version(params_text):
    m = re.search(r"^\s*mermaid:\s*\{[^}]*\bversion:\s*([0-9][0-9.]*)", params_text, re.M)
    return m.group(1) if m else None


def check(site, public=None):
    """Returns (number of entries, findings). public is the built site; the published license texts are checked when it is a directory."""
    findings = []
    with open(os.path.join(site, "package.json"), "r", encoding="utf-8") as fh:
        pkg = json.load(fh)
    with open(os.path.join(site, "go.mod"), "r", encoding="utf-8") as fh:
        gomod = go_mod_versions(fh.read())
    with open(os.path.join(site, "data", "third_party.yaml"), "r", encoding="utf-8") as fh:
        try:
            entries = parse_entries(fh.read())
        except ValueError as exc:
            return 0, [str(exc)]
    by_package = {}
    for e in entries:
        for f in FIELDS:
            if not e.get(f):
                findings.append("third_party.yaml: entry %s lacks the field %s" % (e.get("name", "?"), f))
        if e.get("package") in by_package:
            findings.append("third_party.yaml: %s is listed twice" % e["package"])
        by_package[e.get("package")] = e

    if public and os.path.isdir(public):
        for e in entries:
            lf = e.get("license_file", "")
            if not lf:
                continue
            target = os.path.join(public, lf.lstrip("/").replace("/", os.sep))
            if not lf.startswith("/licenses/") or ".." in lf:
                findings.append("third_party.yaml: the license_file of %s must be a path below /licenses/" % e.get("name", "?"))
            elif not os.path.isfile(target) or os.path.getsize(target) == 0:
                findings.append("third_party.yaml: the license text of %s is not published at %s" % (e.get("name", "?"), lf))

    wanted = {}
    deps = pkg.get("dependencies", {})
    for name, version in sorted(deps.items()):
        if not EXACT.match(version):
            findings.append("package.json: %s is pinned as %s, not as an exact version" % (name, version))
        wanted[name] = version
    if DOCSY in gomod:
        wanted[DOCSY] = gomod[DOCSY].lstrip("v")
    else:
        findings.append("go.mod: the Docsy module %s is not required" % DOCSY)

    for name, version in sorted(wanted.items()):
        e = by_package.get(name)
        if e is None:
            findings.append("third_party.yaml: no notice for %s %s" % (name, version))
        elif e.get("version") != version:
            findings.append("third_party.yaml: %s is %s in the notices but %s in the pins" % (name, e.get("version"), version))
    for name in sorted(p for p in by_package if p and p not in wanted):
        findings.append("third_party.yaml: %s is not pinned in package.json or go.mod" % name)

    with open(os.path.join(site, "config", "_default", "params.yaml"), "r", encoding="utf-8") as fh:
        plugin = mermaid_plugin_version(fh.read())
    if plugin != deps.get("mermaid"):
        findings.append("params.yaml: the Mermaid plugin version (%s) differs from package.json (%s)" % (plugin, deps.get("mermaid")))

    page = os.path.join(site, "content", "legal", "third-party.md")
    with open(page, "r", encoding="utf-8") as fh:
        page_text = fh.read()
    for name in sorted(pkg.get("devDependencies", {})):
        if name not in page_text:
            findings.append("content/legal/third-party.md: the build tool %s is not named" % name)
    return len(entries), findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-notices.py [website/public]", file=sys.stderr)
        return 2
    try:
        count, findings = check(website_dir(), argv[0] if argv else None)
    except (OSError, ValueError) as exc:
        print("check-notices: %s" % exc, file=sys.stderr)
        return 2
    for line in sorted(set(findings)):
        print(line)
    print("check-notices: %d notices checked, %d findings" % (count, len(set(findings))))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
