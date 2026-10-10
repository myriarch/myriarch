# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Renders THIRD_PARTY_NOTICES from the data that scripts/gen-third-party-notices.sh collects.

The renderer does no network access and runs no tool. It reads:

  --reports FILE      go-licenses report lines, tab separated:
                      binary, build, package, module version, license, license file (absolute path)
  --packages FILE     `go list -deps` facts, tab separated:
                      binary, build, import path, module path, module version, module directory
                      (module columns are empty for time/tzdata)
  --vite-license FILE the JSON file of Vite's build.license output (name, version, identifier, text)
  --texts FILE        scripts/notices/third-party-texts.json (texts that cannot be read from a source tree)
  --overrides FILE    ci/license-overrides.txt
  --goroot DIR        optional: GOROOT, whose LICENSE and PATENTS must equal the stored texts when they exist
  --out FILE          output file
  --max-bytes N       fail when the output is larger than N bytes (default 1000000, the limit of the publication gate)

Every distinct license text is printed once in section 5 under a deterministic ID ([T1], [T2], ...; numbered by first use) and referenced by every
component that ships it. Two texts are the same only when they are byte-identical after normalize(); NOTICE files are
never shared and stay verbatim with their component.

The output is deterministic and contains no absolute path, timestamp, toolchain version or machine data.
Exit status: 0 = ok, 1 = missing or inconsistent license data (the message names the cause), 2 = usage error.
"""

import argparse
import hashlib
import json
import os
import re
import sys

TITLE = "Third-party software notices and licenses for myriarch"
BINARIES = ("myriarchd", "myrctl", "myriarch-updater", "myriarch-sim")
GENERATOR = "scripts/gen-third-party-notices.sh"
DEVPG = "devpg"
DEVPG_USED_BY = "myriarchd (development builds with the devpg tag only)"
WEB_USED_BY = "myriarchd (web UI)"
SEPARATOR = "-" * 80
ENTRY_HEADER_RE = re.compile(r"^==== \[T\d+\] ====$", re.M)  # the heading of an entry in section 5
MAX_BYTES = 1000000  # the publication gate rejects files over 1 MB
REF_PREFIX = "License text: "
PATENTS_LABEL = "Additional patent grant of the Go project"
LICENSE_FILE_RE = re.compile(r"^(LICEN[CS]E|COPYING|NOTICE)([-.].*)?$")
NOTICE_ROOT_NAMES = ("NOTICE", "NOTICE.txt", "NOTICE.md")

# Data that is part of the binaries but is not a Go package with a license file. The values are statements of fact
# about the dependencies and are verified by hand when a dependency changes.
LICENSE_DISPLAY = {"XZ": "Public domain"}  # go-licenses name -> name in the notices (the text is a public domain dedication)
X509ROOTS = "golang.org/x/crypto/x509roots/fallback"
EMBEDDED_POSTGRES = "github.com/fergusstrange/embedded-postgres"


class NoticeError(Exception):
    """Missing or inconsistent license data."""


def read_text(path):
    try:
        with open(path, "rb") as f:
            raw = f.read()
    except OSError as e:
        raise NoticeError("cannot read %s: %s" % (os.path.basename(path), e.strerror))
    try:
        return normalize(raw.decode("utf-8"))
    except UnicodeDecodeError:
        raise NoticeError("%s is not valid UTF-8" % os.path.basename(path))


def normalize(text):
    """LF line endings and exactly one trailing newline."""
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    return text.rstrip("\n") + "\n"


def sha256(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def read_tsv(path, columns, what):
    rows = []
    with open(path, "r", encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.rstrip("\n")
            if not line:
                continue
            fields = line.split("\t")
            if len(fields) != columns:
                raise NoticeError("%s line %d: expected %d fields, found %d" % (what, n, columns, len(fields)))
            rows.append(fields)
    return rows


def read_overrides(path):
    out = []
    with open(path, "r", encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.rstrip("\n")
            if not line.strip() or line.startswith("#"):
                continue
            fields = line.split("|", 5)
            if len(fields) != 6 or not all(x.strip() for x in fields):
                raise NoticeError("license overrides line %d is malformed (prefix|reported|declared|file|scope|reason)" % n)
            scope = None
            if fields[4] != "*":
                scope = {x.strip() for x in fields[4].split(",")}
                prefix = fields[0]
                if "" in scope or any(x != prefix and not x.startswith(prefix + "/") for x in scope):
                    raise NoticeError("license overrides line %d: the scope must list packages of %s" % (n, prefix))
            out.append({"prefix": fields[0], "reported": fields[1], "declared": fields[2], "file": fields[3],
                        "scope": scope, "reason": fields[5], "used": False, "line": n})
    return out


def override_for(overrides, package, license_name):
    for o in overrides:
        if o["reported"] == license_name and (package == o["prefix"] or package.startswith(o["prefix"] + "/")):
            o["used"] = True
            return o
    return None


def check_override_scopes(overrides, packages):
    """Fail closed: an override with an explicit package list covers only those packages of the module.

    go-licenses names a library after the module root, so a license reported for the root says nothing about other
    packages of the module that a dependency update may start to link."""
    for o in overrides:
        if o["scope"] is None or not o["used"]:
            continue
        prefix = o["prefix"]
        for binary, build, importpath, _modpath, _modver, _dir in packages:
            if (importpath == prefix or importpath.startswith(prefix + "/")) and importpath not in o["scope"]:
                raise NoticeError("%s is linked by %s (%s) but lies outside the scope of the license override for %s (line %d): "
                                  "review its license and extend or remove the override"
                                  % (importpath, binary, build, prefix, o["line"]))


def within(path, root):
    path, root = os.path.realpath(path), os.path.realpath(root)
    return path.startswith(root + os.sep)


class Component:
    def __init__(self, name, version, directory):
        self.name = name
        self.version = version
        self.dir = directory
        self.licenses = set()
        self.license_dirs = set()     # directories (relative to the module root) that hold license files
        self.explicit_files = set()   # files (relative to the module root) named by an override
        self.notes = set()
        self.builds = set()           # (binary, build) pairs


def collect_go(reports, packages, overrides):
    modules = {}
    for binary, build, _importpath, modpath, modver, moddir in packages:
        if modpath:
            modules.setdefault((binary, build), {})[modpath] = (modver, moddir)
    components = {}
    for binary, build, package, _version, license_name, license_path in reports:
        # go-licenses names a library by the common path prefix of its packages, which is not always a package.
        known = modules.get((binary, build), {})
        modpath = max((m for m in known if package == m or package.startswith(m + "/")), key=len, default=None)
        if modpath is None:
            raise NoticeError("library %s of the go-licenses report (%s, %s) is in no module of the go list data" % (package, binary, build))
        modver, moddir = known[modpath]
        comp = components.setdefault((modpath, modver), Component(modpath, modver, moddir))
        comp.builds.add((binary, build))
        override = override_for(overrides, package, license_name)
        if override is not None:
            declared = override["declared"]
            comp.notes.add(override["reason"])
            if override["file"] != "-":
                rel = override["file"]
                if rel.startswith("/") or ".." in rel.split("/"):
                    raise NoticeError("license override for %s names a file outside the module" % override["prefix"])
                full = os.path.join(moddir, rel)
                if not os.path.isfile(full):
                    raise NoticeError("license override for %s: file %s does not exist in %s" % (override["prefix"], rel, modpath))
                comp.license_dirs.add(os.path.dirname(rel))
                comp.explicit_files.add(rel)
                comp.licenses.add(declared)
                continue
            license_file = license_path
        else:
            if license_name == "Unknown":
                raise NoticeError("%s has an unknown license and no entry in the license overrides (ci/license-overrides.txt)" % package)
            declared = license_name
            license_file = license_path
        if not license_file or license_file == "Unknown" or not os.path.isabs(license_file):
            raise NoticeError("%s: go-licenses reports no license file" % package)
        if not within(license_file, moddir):
            raise NoticeError("%s: the license file is not inside the module directory of %s" % (package, modpath))
        rel = os.path.relpath(os.path.realpath(license_file), os.path.realpath(moddir))
        comp.licenses.add(declared)
        comp.license_dirs.add(os.path.dirname(rel))
    for o in overrides:
        if not o["used"]:
            raise NoticeError("the license override for %s (%s, line %d) matches no package: remove the stale entry"
                              % (o["prefix"], o["reported"], o["line"]))
    return components


def component_files(comp):
    """Relative path -> normalized text of every license and NOTICE file of the component."""
    files = {}
    root = os.path.realpath(comp.dir)
    names = []
    for rel_dir in sorted(comp.license_dirs):
        full = os.path.join(root, rel_dir)
        try:
            entries = sorted(os.listdir(full))
        except OSError:
            raise NoticeError("cannot read the license directory of %s" % comp.name)
        for entry in entries:
            if LICENSE_FILE_RE.match(entry) and os.path.isfile(os.path.join(full, entry)):
                names.append(os.path.normpath(os.path.join(rel_dir, entry)))
    for entry in NOTICE_ROOT_NAMES:
        if os.path.isfile(os.path.join(root, entry)):
            names.append(entry)
    names.extend(sorted(comp.explicit_files))
    for rel in names:
        if rel not in files:
            files[rel] = read_text(os.path.join(root, rel))
    if not any(not is_notice(rel) for rel in files):
        raise NoticeError("%s %s has no license text" % (comp.name, comp.version))
    return files


def is_notice(rel):
    return os.path.basename(rel).startswith("NOTICE")


def file_order(rel):
    return (rel.count("/"), 1 if is_notice(rel) else 0, rel)


def used_by(builds):
    release = {b for b, kind in builds if kind != DEVPG}
    if release:
        return ", ".join(b for b in BINARIES if b in release)
    return DEVPG_USED_BY


def first_line(text):
    """The first non-empty line of a text, shortened: a hint what the text is (a file can hold a text that its
    component's license name does not describe, such as the MPL-2.0 files of a module that is BSD-3-Clause)."""
    for line in text.split("\n"):
        if line.strip():
            line = " ".join(line.split())
            return line if len(line) <= 70 else line[:67] + "..."
    return ""


class TextRegistry:
    """The distinct license texts, numbered in the order of their first use ([T1], [T2], ...).

    Key: the text itself after normalize() and nothing else (no whitespace folding, no case folding), so only byte-identical
    texts share an ID. An entry names no license: the license expression of a component does not say which license a file of
    it holds (a BSD-3-Clause module can ship an MPL-2.0 file). It shows where the text first appeared and how it begins. Only
    a text with an explicit label (the Go patent grant) gets a heading label."""

    def __init__(self):
        self.ids = {}      # text -> number
        self.entries = []  # [text, label or None, first source, set of component entries]

    def ref(self, text, label, component, rel):
        if ENTRY_HEADER_RE.search(text):
            raise NoticeError("a license text of %s (%s) contains a line that looks like a heading of the License texts section" % (component, rel))
        n = self.ids.get(text)
        if n is None:
            n = len(self.entries) + 1
            self.ids[text] = n
            self.entries.append([text, label, "%s (%s)" % (component, rel), set()])
        entry = self.entries[n - 1]
        if label and not entry[1]:
            entry[1] = label
        entry[3].add(component)
        return "[T%d]" % n

    def section(self):
        lines = []
        for n, (text, label, first, components) in enumerate(self.entries, 1):
            count = len(components)
            lines += ["==== [T%d] ====" % n]
            if label:
                lines += ["Kind: " + label]
            lines += ["First used by: " + first, "Begins with: " + first_line(text),
                      "Referenced by %d component entr%s." % (count, "y" if count == 1 else "ies"), "", text.rstrip("\n"), ""]
        return lines


def component_block(name_line, licenses, used, notes, files, registry):
    """files: (relative path, normalized text, label or None) triples. NOTICE files stay verbatim with the component; every other
    license text is printed once in the License texts section and referenced here."""
    out = [SEPARATOR, "Component: " + name_line, "License: " + licenses, "Used by: " + used]
    out.extend("Note: " + n for n in notes)
    out.append("")
    for rel, text, label in files:
        out.append("--- %s ---" % rel)
        if is_notice(rel):
            out.append(text.rstrip("\n"))
        else:
            out.append(REF_PREFIX + registry.ref(text, label, name_line, rel))
        out.append("")
    return out


def load_texts(path):
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, ValueError) as e:
        raise NoticeError("cannot read the stored license texts (%s): %s" % (os.path.basename(path), e))
    for key in ("license", "patents"):
        entry = data.get("go", {}).get(key)
        if not entry or not entry.get("text"):
            raise NoticeError("the stored license texts lack go.%s" % key)
        if sha256(entry["text"]) != entry.get("sha256"):
            raise NoticeError("go.%s of the stored license texts does not match its sha256" % key)
    for entry in data.get("npm", []):
        if sha256(entry.get("text", "")) != entry.get("sha256"):
            raise NoticeError("the stored text of %s does not match its sha256" % entry.get("name"))
    return data


def check_goroot(goroot, go_texts):
    if not goroot:
        return
    for key, name in (("license", "LICENSE"), ("patents", "PATENTS")):
        p = os.path.join(goroot, name)
        if os.path.isfile(p) and read_text(p) != normalize(go_texts[key]["text"]):
            raise NoticeError("GOROOT/%s differs from the stored Go %s text: update scripts/notices/third-party-texts.json" % (name, name))


def go_stdlib_block(go_texts, used, registry):
    # the PATENTS text is not a license: it gets its own label in the License texts section
    files = [("LICENSE", normalize(go_texts["license"]["text"]), None),
             ("PATENTS", normalize(go_texts["patents"]["text"]), PATENTS_LABEL)]
    return component_block("Go standard library", "BSD-3-Clause", used, [
        "The PATENTS file is the additional patent grant of the Go project.",
    ], files, registry)


def section_heading(title):
    return [title, "=" * len(title), ""]


def data_blocks(packages, registry):
    """Section 3: data embedded in the binaries."""
    blocks = []
    version_by_builds = {}
    for binary, build, importpath, modpath, modver, _dir in packages:
        if modpath == X509ROOTS:
            version_by_builds.setdefault(modver, set()).add((binary, build))
    for modver in sorted(version_by_builds):
        blocks.append(component_block("%s %s" % (X509ROOTS, modver), "MPL-2.0", used_by(version_by_builds[modver]), [
            "Mozilla root certificate store data. The bundle is generated by gen_fallback_bundle.go from the NSS file "
            "certdata.txt, which is licensed under MPL-2.0. The Go code of the module is BSD-3-Clause (see section 2).",
            "Source form: the module source at https://proxy.golang.org and https://github.com/golang/crypto/tree/master/x509roots, "
            "and the NSS file at https://hg.mozilla.org/mozilla-central/file/tip/security/nss/lib/ckfw/builtins/certdata.txt.",
            "MPL-2.0 text: https://mozilla.org/MPL/2.0/",
        ], [], registry))
    tz = {(b, k) for b, k, ip, _m, _v, _d in packages if ip == "time/tzdata"}
    if tz:
        blocks.append(component_block("time/tzdata", "Public domain", used_by(tz), [
            "IANA Time Zone Database (https://www.iana.org/time-zones), which the IANA asserts is in the public domain. "
            "The data is part of the Go standard library and embedded by the time/tzdata package.",
        ], [], registry))
    pg = {(b, k) for b, k, _ip, m, _v, _d in packages if m == EMBEDDED_POSTGRES}
    if pg:
        blocks.append(component_block("PostgreSQL for `myriarchd dev`", "PostgreSQL License", used_by(pg), [
            "Not distributed by the project. In development mode, %s downloads PostgreSQL binaries at run time "
            "from https://repo1.maven.org/maven2 (io.zonky.test.postgres)." % EMBEDDED_POSTGRES,
        ], [], registry))
    return blocks


def collect_npm(vite_path, texts):
    try:
        with open(vite_path, "r", encoding="utf-8") as f:
            bundled = json.load(f)
    except (OSError, ValueError) as e:
        raise NoticeError("cannot read the Vite license output (%s): %s" % (os.path.basename(vite_path), e))
    if not isinstance(bundled, list) or not bundled:
        raise NoticeError("the Vite license output lists no packages")
    stored = {}
    for entry in texts.get("npm", []):
        stored[entry["name"]] = entry
    seen = set()
    result = []
    for pkg in bundled:
        name, version, identifier = pkg["name"], pkg["version"], pkg.get("identifier") or "UNKNOWN"
        seen.add(name)
        text = pkg.get("text")
        notes = []
        entry = stored.get(name)
        if text:
            text = normalize(text)
        else:
            if entry is None:
                raise NoticeError("the bundled package %s %s has no license text and no entry in the stored license texts" % (name, version))
            if entry["version"] != version:
                raise NoticeError("the stored license text of %s is for version %s, but %s is bundled: check the text and update the entry"
                                  % (name, entry["version"], version))
            text = normalize(entry["text"])
            notes = [entry.get("note") or "The package ships no license file.", "Source of the text: " + entry["source"]]
        if identifier == "UNKNOWN":
            raise NoticeError("the bundled package %s %s has no license identifier" % (name, version))
        result.append((name, version, identifier, text, notes))
    for name in sorted(stored):
        if name not in seen:
            raise NoticeError("the stored license text of %s is stale: the package is not bundled" % name)
    result.sort(key=lambda r: (r[0], r[1]))
    return result


def render(args):
    texts = load_texts(args.texts)
    check_goroot(args.goroot, texts["go"])
    overrides = read_overrides(args.overrides)
    reports = read_tsv(args.reports, 6, "go-licenses report")
    packages = read_tsv(args.packages, 6, "go list data")
    if not reports:
        raise NoticeError("the go-licenses report is empty")
    components = collect_go(reports, packages, overrides)
    check_override_scopes(overrides, packages)

    all_builds = {(r[0], r[1]) for r in reports}
    registry = TextRegistry()
    lines = [TITLE, "=" * len(TITLE), ""]
    lines += [
        "myriarch is licensed under the Apache License, Version 2.0 (see LICENSE). The binaries myriarchd, myrctl,",
        "myriarch-updater and myriarch-sim, and the web UI that is embedded in myriarchd, include the third-party",
        "software listed below. Each component keeps its own license.",
        "",
        "How to read this file: every distinct license text is printed once, verbatim, in section 5 under an ID such",
        "as [T1]. A line \"%s[T1]\" under a file name in a component entry means that the license file of the" % REF_PREFIX,
        "component is that text. Two texts share an ID only when they are identical byte for byte; a text that differs",
        "in any way (a copyright line, an appendix) has its own ID. An entry in section 5 does not name a license: the",
        "license of a component is the \"License:\" line of its entry, and the text shows what a file holds. NOTICE files",
        "are reproduced verbatim in the entry of the component that ships them.",
        "",
        "This file is generated by %s and is not edited by hand." % GENERATOR,
        "",
    ]
    lines += section_heading("1. Go standard library")
    lines += go_stdlib_block(texts["go"], used_by(all_builds), registry)

    lines += section_heading("2. Go modules")
    for key in sorted(components):
        comp = components[key]
        files = component_files(comp)
        ordered = [(rel, files[rel], None) for rel in sorted(files, key=file_order)]
        licenses = " AND ".join(sorted(LICENSE_DISPLAY.get(x, x) for x in comp.licenses))
        lines += component_block("%s %s" % (comp.name, comp.version), licenses,
                                 used_by(comp.builds), sorted(comp.notes), ordered, registry)

    lines += section_heading("3. Data embedded in the binaries")
    for block in data_blocks(packages, registry):
        lines += block

    lines += section_heading("4. Web UI dependencies (embedded in myriarchd)")
    for name, version, identifier, text, notes in collect_npm(args.vite_license, texts):
        lines += component_block("%s %s" % (name, version), identifier, WEB_USED_BY, notes, [("LICENSE", text, None)], registry)

    lines += section_heading("5. License texts")
    lines += registry.section()

    output = "\n".join(lines).rstrip("\n") + "\n"
    size = len(output.encode("utf-8"))
    if size > args.max_bytes:
        raise NoticeError("the output is %d bytes, which is over the limit of %d bytes (the publication gate rejects larger "
                          "files): reduce the dependencies or review what is reproduced" % (size, args.max_bytes))
    return output


def main(argv):
    ap = argparse.ArgumentParser(description="Renders THIRD_PARTY_NOTICES (called by scripts/gen-third-party-notices.sh).")
    ap.add_argument("--reports", required=True)
    ap.add_argument("--packages", required=True)
    ap.add_argument("--vite-license", required=True)
    ap.add_argument("--texts", required=True)
    ap.add_argument("--overrides", required=True)
    ap.add_argument("--goroot", default="")
    ap.add_argument("--out", required=True)
    ap.add_argument("--max-bytes", type=int, default=MAX_BYTES)
    args = ap.parse_args(argv)
    try:
        output = render(args)
    except NoticeError as e:
        sys.stderr.write("third_party_notices: error: %s\n" % e)
        return 1
    with open(args.out, "w", encoding="utf-8", newline="\n") as f:
        f.write(output)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
