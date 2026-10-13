#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks the pins of the Docsy overrides (website/overrides.txt, ADR-0031).

    python3 -I website/scripts/check-overrides.py [website/public]

Some files of website/layouts and website/assets replace a file of the Docsy theme with a modified copy (Hugo's union file system
lets the file of the project win). website/overrides.txt lists every such file with the SHA-256 of the upstream file that the copy
was made from, one per line:

    <path inside the theme> <sha256 of the upstream file>      (for example: layouts/_partials/head.html 3f9a...)

The check fails when

  - an upstream file changed (the pinned digest differs: Docsy was updated; compare the override with the new upstream file,
    port the changes and update the digest),
  - a listed override is missing from website/, or the upstream file is gone,
  - a file of website/layouts or website/assets exists at the same path in the theme but is not listed (an unpinned override),
  - the path list of the REUSE.toml annotation that adds the Docsy copyright (aggregate) differs from the list of overrides.txt
    (a new override would silently miss the attribution of Docsy).

One exception: the three project style files of the theme (PROJECT_HOOKS) are placeholders that Docsy asks every project to
replace. The project's file is its own, not a modified copy, so it needs neither a pin nor the Docsy attribution; the check
fails only when Docsy puts content into such a placeholder, because the project's file would then hide it.

Docsy is located through `hugo config mounts` (a stream of JSON objects, one per module), which needs hugo on PATH and the module
cache of the build (HUGO_CACHEDIR). The argument (the built site) is accepted for the common calling convention of the checks and
not used. Output: paths and counts. Exit status 0 = clean, 1 = findings, 2 = environment error.
Python 3.9 compatible, standard library only.
"""

import hashlib
import json
import os
import re
import subprocess
import sys

THEME_MODULE = "github.com/docsy/docsy/theme"
SHA256 = re.compile(r"^[0-9a-f]{64}$")

# The directories of website/ whose files shadow the file of the theme at the same path.
OVERRIDE_DIRS = ("layouts", "assets")

# The style files that Docsy documents as the place for the styles of a project (https://www.docsy.dev/docs/content/lookandfeel/#project-style-files).
# The theme ships each of them as a placeholder that holds only a comment and says "Projects can override this file".
PROJECT_HOOKS = frozenset({
    "assets/scss/_variables_project.scss",
    "assets/scss/_variables_project_after_bs.scss",
    "assets/scss/_styles_project.scss",
})


def website_dir():
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def parse_overrides(text):
    """Returns (entries as {path: sha256}, errors as [message])."""
    entries, errors = {}, []
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) != 2 or not SHA256.match(parts[1]):
            errors.append("overrides.txt:%d: expected '<path inside the theme> <sha256>'" % n)
            continue
        if parts[0] in entries:
            errors.append("overrides.txt:%d: %s is listed twice" % (n, parts[0]))
            continue
        entries[parts[0]] = parts[1]
    return entries, errors


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def theme_dir_from_mounts(stream):
    """Finds the directory of the Docsy module in the output of `hugo config mounts`."""
    dec = json.JSONDecoder()
    i = 0
    while i < len(stream):
        while i < len(stream) and stream[i].isspace():
            i += 1
        if i >= len(stream):
            break
        obj, i = dec.raw_decode(stream, i)
        if obj.get("path") == THEME_MODULE and obj.get("dir"):
            return obj["dir"]
    return None


def locate_theme(site):
    try:
        proc = subprocess.run(["hugo", "config", "mounts"], cwd=site, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    except OSError as exc:
        raise RuntimeError("cannot run hugo: %s" % exc)
    if proc.returncode != 0:
        raise RuntimeError("hugo config mounts failed (module cache missing? run make website first)")
    theme = theme_dir_from_mounts(proc.stdout.decode("utf-8", "replace"))
    if not theme or not os.path.isdir(theme):
        raise RuntimeError("the module %s was not found in the mounts" % THEME_MODULE)
    return theme


def has_code(text):
    """True when a style file holds more than comments and white space (the placeholders of the theme hold only a comment)."""
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
    text = re.sub(r"//[^\n]*", "", text)
    return bool(text.strip())


def list_files(base):
    out = []
    for root, dirs, files in os.walk(base):
        dirs.sort()
        for name in sorted(files):
            out.append(os.path.relpath(os.path.join(root, name), base).replace(os.sep, "/"))
    return out


def reuse_docsy_paths(text):
    """Paths of the REUSE.toml annotations that carry the Docsy copyright (stdlib only: no TOML parser before Python 3.11)."""
    paths = set()
    for block in text.split("[[annotations]]")[1:]:
        if "Copyright The Docsy Authors" not in block:
            continue
        m = re.search(r"^path\s*=\s*\[(.*?)\]", block, re.S | re.M)
        if m:
            paths.update(re.findall(r'"([^"]+)"', m.group(1)))
    return paths


def check_reuse(site, entries):
    """Findings when REUSE.toml and overrides.txt list different files. entries: the keys of overrides.txt."""
    reuse = os.path.join(os.path.dirname(site), "REUSE.toml")
    if not os.path.isfile(reuse):
        return []
    with open(reuse, "r", encoding="utf-8") as fh:
        listed = reuse_docsy_paths(fh.read())
    want = {"website/" + p for p in entries}
    findings = []
    for path in sorted(want - listed):
        findings.append("%s: listed in overrides.txt but not in the Docsy annotation of REUSE.toml (add it there)" % path)
    for path in sorted(listed - want):
        findings.append("%s: in the Docsy annotation of REUSE.toml but not listed in overrides.txt" % path)
    return findings


def check(site, theme):
    """Returns (number of overrides, findings)."""
    findings = []
    overrides_file = os.path.join(site, "overrides.txt")
    with open(overrides_file, "r", encoding="utf-8") as fh:
        entries, errors = parse_overrides(fh.read())
    findings.extend(errors)
    for path, want in sorted(entries.items()):
        mine = os.path.join(site, path)
        upstream = os.path.join(theme, path)
        if not os.path.isfile(mine):
            findings.append("%s: listed in overrides.txt but missing in website/" % path)
        if not os.path.isfile(upstream):
            findings.append("%s: the upstream file no longer exists in Docsy (remove the override or the entry)" % path)
            continue
        got = sha256_file(upstream)
        if got != want:
            findings.append("%s: the upstream file changed (pinned %s, now %s); compare the override and update the digest" % (path, want[:12], got[:12]))
    for base in OVERRIDE_DIRS:
        root = os.path.join(site, base)
        if not os.path.isdir(root):
            continue
        for rel in list_files(root):
            path = base + "/" + rel
            upstream = os.path.join(theme, path)
            if path in entries or not os.path.isfile(upstream):
                continue
            if path in PROJECT_HOOKS:
                with open(upstream, "r", encoding="utf-8") as fh:
                    if has_code(fh.read()):
                        findings.append("%s: the upstream file is no longer an empty placeholder for the styles of a project; pin it in overrides.txt and port its content" % path)
                continue
            findings.append("%s: replaces a Docsy file but is not listed in overrides.txt" % path)
    findings.extend(check_reuse(site, entries))
    return len(entries), findings


def main(argv):
    if len(argv) > 1 or (argv and argv[0].startswith("-")):
        print("usage: python3 -I website/scripts/check-overrides.py [website/public]", file=sys.stderr)
        return 2
    site = website_dir()
    try:
        theme = locate_theme(site)
        count, findings = check(site, theme)
    except (RuntimeError, OSError) as exc:
        print("check-overrides: %s" % exc, file=sys.stderr)
        return 2
    for line in sorted(set(findings)):
        print(line)
    print("check-overrides: %d overrides checked, %d findings" % (count, len(set(findings))))
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
