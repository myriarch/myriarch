#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Checks of the goreleaser output of the release build (standard library only).

  dist-check.py check --root R --dist D --state S --version V
      after goreleaser in stage collect or apply (deploy/release/windows-signed.sh check-dist): the Windows files in dist/ are the
      files that the post-build hook accepted, the Windows scripts are only in the Windows updater archives, the binaries carry
      the version resources, and checksums.txt lists every release file (and LICENSE and THIRD_PARTY_NOTICES) with the right hash
  dist-check.py assets --dist D
      for ci/release/stage-assets.sh: prints "<name><TAB><path>" for every archive, bare binary, SBOM and the checksums file of dist/
      artifacts.json (the path is mapped onto D and is the file to copy); it refuses duplicate names, a name with a slash or a leading
      dot, a control character in a name or a path, and a dist without exactly one checksums file
  dist-check.py compare --first A --second B
      for the rehearsal: two dist directories of the same commit are identical (archives and binaries byte for byte, SBOMs without
      documentNamespace, creationInfo.created and syft's own temporary directory entry); the Windows files of myriarch-updater and myrctl are left out, because the
      second build holds the signed files, which stage apply and check-dist cover

It reads dist/artifacts.json of goreleaser v2.18: types Archive, Binary (the build outputs and, with extra.Format "binary", the bare
binaries), SBOM, Checksum and Metadata; paths start with "dist/". A different layout makes the checks fail closed. Problems are printed
one per line as "[ci] ERROR: [dist_mismatch] ..." (check) or "[not_reproducible] <name>" (compare); the exit status is 1 then.
"""

import argparse
import hashlib
import json
import re
import sys
import tarfile
import zipfile
from pathlib import Path

WIN_IDS = ("myriarch-updater", "myrctl")
SCRIPT_MEMBERS = {
    "install-windows.ps1": "deploy/scripts/install-windows.ps1",
    "Set-ServiceEnvironment.ps1": "deploy/windows/Set-ServiceEnvironment.ps1",
}
EXES = tuple("%s_windows_%s.exe" % (i, a) for i in WIN_IDS for a in ("amd64", "arm64"))
EXTRA_FILES = ("LICENSE", "THIRD_PARTY_NOTICES")


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def rebase(dist, path):
    """artifacts.json paths start with 'dist/' (the directory goreleaser wrote to): map them onto the directory that is checked."""
    if not path.startswith("dist/"):
        raise ValueError("artifact path '%s' does not start with dist/" % path)
    return Path(dist) / path[len("dist/"):]


def load_artifacts(dist):
    try:
        with open(Path(dist) / "artifacts.json", "r", encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, ValueError) as e:
        raise ValueError("cannot read %s/artifacts.json: %s" % (dist, e))
    if not isinstance(data, list):
        raise ValueError("artifacts.json is not a list")
    return data


def kind(art):
    """build / bare / archive / sbom / other"""
    t, extra = art.get("type"), art.get("extra") or {}
    if t == "Binary":
        return "bare" if extra.get("Format") == "binary" else "build"
    if t == "Archive":
        return "archive"
    if t == "SBOM":
        return "sbom"
    return "other"


def utf16(s):
    return s.encode("utf-16-le")


def check(args):
    problems = []

    def bad(msg):
        problems.append("[dist_mismatch] " + msg)

    dist = Path(args.dist)
    try:
        arts = load_artifacts(dist)
    except ValueError as e:
        print("[ci] ERROR: [dist_mismatch] %s" % e, file=sys.stderr)
        return 1
    state = Path(args.state)
    try:
        marker = dict(line.split("=", 1) for line in (state / "stage").read_text(encoding="utf-8").splitlines() if "=" in line)
    except OSError:
        marker = {}
    if marker.get("stage") not in ("collect", "apply"):
        bad("the marker of the before hook says stage '%s', expected collect or apply" % marker.get("stage"))
    version = args.version

    accepted = {}
    for name in list(EXES) + list(SCRIPT_MEMBERS):
        try:
            value = (state / "accepted" / (name + ".sha256")).read_text(encoding="ascii").strip()
        except OSError:
            value = ""
        if not re.match(r"^[0-9a-f]{64}$", value):
            bad("no accepted record for %s: the post-build hook did not run (--skip=post-hooks?)" % name)
        else:
            accepted[name] = value

    def art_path(a):
        try:
            return rebase(dist, a["path"])
        except (KeyError, ValueError) as e:
            bad("%s" % e)
            return None

    # ---- Windows binaries: build outputs, bare binaries, archive members
    seen_exes = set()
    for a in arts:
        k, extra = kind(a), a.get("extra") or {}
        if a.get("goos") != "windows":
            continue
        if k == "build" and extra.get("ID") in WIN_IDS:
            name = "%s_windows_%s.exe" % (extra["ID"], a.get("goarch"))
            seen_exes.add(name)
            p = art_path(a)
            if p is None or name not in accepted:
                continue
            if not p.is_file() or sha256_file(p) != accepted[name]:
                bad("%s in dist/ is not the file that the hook accepted" % name)
                continue
            data = p.read_bytes()
            for needle in ("Myriarch", version, "%s.exe" % extra["ID"]):
                if utf16(needle) not in data:
                    bad("%s carries no version resource string '%s'" % (name, needle))
        elif k == "bare" and extra.get("ID") == "myriarch-updater-binary":
            name = "myriarch-updater_windows_%s.exe" % a.get("goarch")
            p = art_path(a)
            if p is None or name not in accepted:
                continue
            if not p.is_file() or sha256_file(p) != accepted[name]:
                bad("the bare binary %s is not the file that the hook accepted" % a.get("name"))
            if extra.get("Checksum") != "sha256:" + accepted[name]:
                bad("the checksum goreleaser recorded for %s is not the accepted one" % a.get("name"))
        elif k == "archive" and extra.get("ID") in WIN_IDS:
            ident = extra["ID"]
            name = "%s_windows_%s.exe" % (ident, a.get("goarch"))
            p = art_path(a)
            if p is None or not p.is_file():
                bad("archive %s is missing" % a.get("name"))
                continue
            try:
                with zipfile.ZipFile(p) as z:
                    members = set(z.namelist())
                    if name in accepted:
                        if "%s.exe" % ident not in members:
                            bad("%s has no %s.exe" % (a["name"], ident))
                        elif sha256_bytes(z.read("%s.exe" % ident)) != accepted[name]:
                            bad("%s.exe in %s is not the file that the hook accepted" % (ident, a["name"]))
                    if ident == "myriarch-updater":
                        for sname, rel in SCRIPT_MEMBERS.items():
                            if rel not in members:
                                bad("%s has no %s" % (a["name"], rel))
                            elif sname in accepted and sha256_bytes(z.read(rel)) != accepted[sname]:
                                bad("%s in %s is not the file that the hook accepted" % (rel, a["name"]))
            except (zipfile.BadZipFile, OSError) as e:
                bad("%s cannot be read: %s" % (a["name"], e))
    missing = sorted(set(EXES) - seen_exes)
    if missing:
        bad("no Windows build artifact for %s" % ", ".join(missing))

    # ---- the Windows scripts are only in the Windows updater archives
    for a in arts:
        if kind(a) != "archive" or a.get("goos") == "windows":
            continue
        p = art_path(a)
        if p is None or not p.is_file() or not a["name"].endswith(".tar.gz"):
            continue
        try:
            with tarfile.open(p, "r:gz") as t:
                for m in t.getnames():
                    if m.endswith(".ps1"):
                        bad("%s holds the Windows script %s" % (a["name"], m))
        except (tarfile.TarError, OSError) as e:
            bad("%s cannot be read: %s" % (a["name"], e))

    # ---- checksums.txt
    sums = {}
    try:
        text = (dist / "checksums.txt").read_text(encoding="utf-8")
        for line in text.splitlines():
            m = re.match(r"^([0-9a-f]{64})  (\S+)$", line)
            if not m:
                bad("checksums.txt: malformed line '%s'" % line[:80])
            else:
                sums[m.group(2)] = m.group(1)
    except OSError:
        bad("checksums.txt is missing")
    expected = {}
    for a in arts:
        if kind(a) in ("archive", "bare", "sbom"):
            p = art_path(a)
            if p is not None:
                expected[a["name"]] = p
    root = Path(args.root)
    for name in EXTRA_FILES:
        expected[name] = root / name
    for name, p in sorted(expected.items()):
        if name not in sums:
            bad("checksums.txt does not list %s" % name)
        elif not p.is_file() or sha256_file(p) != sums[name]:
            bad("checksums.txt lists a hash for %s that differs from the file" % name)
    extra_names = sorted(set(sums) - set(expected))
    if extra_names:
        bad("checksums.txt lists unexpected entries: %s" % ", ".join(extra_names))
    archives = [a for a in arts if kind(a) == "archive"]
    sboms = [a for a in arts if kind(a) == "sbom"]
    if len(sboms) != len(archives) + 1:
        bad("%d SBOMs for %d archives (expected one per archive and one for the web UI)" % (len(sboms), len(archives)))
    webui = "myriarch-webui_%s.sbom.json" % version
    if webui not in {a["name"] for a in sboms} or not (dist / webui).is_file():
        bad("the web UI SBOM %s is missing" % webui)

    for p in problems:
        print("[ci] ERROR: %s" % p, file=sys.stderr)
    if not problems:
        print("dist check passed: %d checksums, %d archives, %d SBOMs" % (len(sums), len(archives), len(sboms)), file=sys.stderr)
    return 1 if problems else 0


def assets(args):
    """The release files that goreleaser built, as "<name>\t<path>" lines (stage-assets.sh copies them under their names)."""
    try:
        arts = load_artifacts(args.dist)
    except ValueError as e:
        print("[ci] ERROR: [dist_mismatch] %s" % e, file=sys.stderr)
        return 1
    rows, seen, sums = [], set(), 0
    for a in arts:
        k = kind(a)
        if a.get("type") == "Checksum":
            k, sums = "checksums", sums + 1
        if k not in ("archive", "bare", "sbom", "checksums"):
            continue
        name, path = a.get("name"), a.get("path")
        if not isinstance(name, str) or not isinstance(path, str) or not name or "/" in name or "\\" in name or name.startswith("."):
            print("[ci] ERROR: [dist_mismatch] artifact name '%s' is not a plain file name" % str(name)[:80], file=sys.stderr)
            return 1
        if any(ord(c) < 32 or ord(c) == 127 for c in name + path):
            print("[ci] ERROR: [dist_mismatch] an artifact has a control character in its name or path", file=sys.stderr)
            return 1
        if name in seen:
            print("[ci] ERROR: [dist_mismatch] artifact name '%s' occurs twice" % name, file=sys.stderr)
            return 1
        seen.add(name)
        try:
            rows.append((name, str(rebase(args.dist, path))))
        except ValueError as e:
            print("[ci] ERROR: [dist_mismatch] %s" % e, file=sys.stderr)
            return 1
    if sums != 1:
        print("[ci] ERROR: [dist_mismatch] artifacts.json lists %d checksums files, expected one" % sums, file=sys.stderr)
        return 1
    for name, path in sorted(rows, key=lambda r: r[0].encode()):
        print("%s\t%s" % (name, path))
    return 0


def strip_sbom(path):
    """The SBOM without what syft randomizes: documentNamespace, creationInfo.created and the entry for its own temporary extraction
    directory (a file named like .../syft-archive-contents-<random>, which nothing refers to)."""
    with open(path, "r", encoding="utf-8") as f:
        doc = json.load(f)
    doc.pop("documentNamespace", None)
    (doc.get("creationInfo") or {}).pop("created", None)
    dropped = {f.get("SPDXID") for f in doc.get("files") or [] if "syft-archive-contents-" in str(f.get("fileName"))}
    doc["files"] = [f for f in doc.get("files") or [] if f.get("SPDXID") not in dropped]
    doc["relationships"] = [r for r in doc.get("relationships") or []
                            if r.get("spdxElementId") not in dropped and r.get("relatedSpdxElement") not in dropped]
    return doc


def is_windows_pair(a):
    extra = a.get("extra") or {}
    return a.get("goos") == "windows" and extra.get("ID") in WIN_IDS + ("myriarch-updater-binary",)


def compare(args):
    problems = []
    try:
        first, second = load_artifacts(args.first), load_artifacts(args.second)
    except ValueError as e:
        print("[ci] ERROR: [not_reproducible] %s" % e, file=sys.stderr)
        return 1
    names1, names2 = {a["name"] + "|" + a.get("path", "") for a in first}, {a["name"] + "|" + a.get("path", "") for a in second}
    if names1 != names2:
        problems.append("the two builds produce different artifacts: %s" % ", ".join(sorted(names1 ^ names2)))
    index2 = {a["path"]: a for a in second}
    skipped_archives = {a["name"] for a in first if kind(a) == "archive" and is_windows_pair(a)}
    compared = 0
    for a in first:
        k = kind(a)
        b = index2.get(a.get("path"))
        if b is None or k not in ("build", "archive", "bare", "sbom"):
            continue
        p1, p2 = rebase(args.first, a["path"]), rebase(args.second, b["path"])
        if k == "sbom":
            if a["name"][:-len(".sbom.json")] in skipped_archives:
                continue
            try:
                same = strip_sbom(p1) == strip_sbom(p2)
            except (OSError, ValueError):
                same = False
        else:
            if is_windows_pair(a):
                continue
            same = p1.is_file() and p2.is_file() and sha256_file(p1) == sha256_file(p2)
        compared += 1
        if not same:
            problems.append(a["name"])
    for p in problems:
        print("[ci] ERROR: [not_reproducible] %s" % p, file=sys.stderr)
    if not problems:
        print("reproducibility check passed: %d artifacts compared" % compared, file=sys.stderr)
    return 1 if problems else 0


def main(argv=None):
    ap = argparse.ArgumentParser(prog="dist-check.py")
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("check")
    p.add_argument("--root", required=True)
    p.add_argument("--dist", required=True)
    p.add_argument("--state", required=True)
    p.add_argument("--version", required=True)
    p.set_defaults(func=check)
    p = sub.add_parser("assets")
    p.add_argument("--dist", required=True)
    p.set_defaults(func=assets)
    p = sub.add_parser("compare")
    p.add_argument("--first", required=True)
    p.add_argument("--second", required=True)
    p.set_defaults(func=compare)
    args = ap.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
