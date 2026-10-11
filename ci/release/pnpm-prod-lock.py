#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Reduces a pnpm lockfile (version 9, read as JSON on stdin) to the production dependencies of the root project.

  yq -o=json '.' web/pnpm-lock.yaml | python3 -I ci/release/pnpm-prod-lock.py --package-json web/package.json > out/pnpm-lock.yaml

syft cannot drop the dev dependencies of a pnpm lockfile (its javascript.include-dev-dependencies setting has no effect on lockfile
version 9), so the SBOM of the web UI is made from a lockfile that only holds what the production bundle can depend on
(deploy/release/sbom.sh). The result is JSON, which is valid YAML. Standard library only; exit status 1 and a message on stderr for
anything unexpected.
"""

import argparse
import json
import sys


def die(message):
    print("pnpm-prod-lock: %s" % message, file=sys.stderr)
    sys.exit(1)


def snapshot_key(name, version):
    if not isinstance(version, str) or not version:
        die("dependency '%s' has no version" % name)
    if version.startswith(("link:", "file:")):
        die("dependency '%s' has an unsupported version '%s'" % (name, version))
    # an alias (name: realname@1.2.3) already carries the name
    return version if "@" in version.split("(")[0] else "%s@%s" % (name, version)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--package-json", required=True)
    args = ap.parse_args()
    try:
        lock = json.load(sys.stdin)
    except ValueError as e:
        die("stdin is not JSON: %s" % e)
    try:
        with open(args.package_json, "r", encoding="utf-8") as f:
            pkg = json.load(f)
    except (OSError, ValueError) as e:
        die("cannot read %s: %s" % (args.package_json, e))
    if not isinstance(lock, dict) or lock.get("lockfileVersion") != "9.0":
        die("lockfileVersion is not '9.0'")
    importer = (lock.get("importers") or {}).get(".")
    if not isinstance(importer, dict):
        die("importer '.' is missing")
    roots = {}
    for section in ("dependencies", "optionalDependencies"):
        for name, entry in (importer.get(section) or {}).items():
            if not isinstance(entry, dict) or "version" not in entry:
                die("importer '.' %s '%s' has no version" % (section, name))
            roots[name] = (section, entry)
    wanted = set(pkg.get("dependencies") or {}) | set(pkg.get("optionalDependencies") or {})
    if set(roots) != wanted:
        die("importer '.' does not match web/package.json")
    packages = lock.get("packages") or {}
    snapshots = lock.get("snapshots") or {}
    keep = {}
    todo = [snapshot_key(name, entry["version"]) for name, (_, entry) in sorted(roots.items())]
    while todo:
        key = todo.pop()
        if key in keep:
            continue
        snap = snapshots.get(key)
        if not isinstance(snap, dict):
            die("snapshot '%s' is missing" % key)
        keep[key] = snap
        for section in ("dependencies", "optionalDependencies"):
            for name, version in (snap.get(section) or {}).items():
                todo.append(snapshot_key(name, version))
    if not keep:
        die("the closure of the production dependencies is empty")
    kept_packages = {}
    for key in keep:
        base = key.split("(")[0]
        if base not in packages:
            die("package '%s' is missing" % base)
        kept_packages[base] = packages[base]
    out_importer = {}
    for section in ("dependencies", "optionalDependencies"):
        part = {n: e for n, (s, e) in roots.items() if s == section}
        if part:
            out_importer[section] = part
    out = {
        "lockfileVersion": lock["lockfileVersion"],
        "settings": lock.get("settings") or {},
        "importers": {".": out_importer},
        "packages": kept_packages,
        "snapshots": keep,
    }
    sys.stdout.write(json.dumps(out, sort_keys=True, indent=1) + "\n")


if __name__ == "__main__":
    main()
