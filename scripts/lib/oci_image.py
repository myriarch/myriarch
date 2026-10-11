# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Writes deterministic OCI image layouts without a container runtime (helper of scripts/build-image-crane.sh).

Two subcommands:

  layout   Writes a layout from layer tars that already exist (the single-platform build from source). One layer per
           platform, `--platform linux/amd64=layer.tar` once per platform.
  dist     Builds the layers of a release image from the linux archives that goreleaser produced
           (<program>_<version>_linux_<arch>.tar.gz in --dist: myriarchd, myrctl and myriarch-sim for the server image,
           myriarch-updater for the updater image) and writes the layout of ALL platforms.

The layout is flat: index.json IS the image index (the manifests of the platforms and, optionally, annotations), so
`crane push --index <layout> <repo>@sha256:<sha256 of index.json>` publishes exactly these bytes and the digest of the index
is known before anything is pushed (ci/release/push-images.sh). Nothing in it depends on the build time: layer
entries are sorted with a fixed owner, mode and mtime (--epoch), the gzip header has no name and no timestamp, the JSON
documents have sorted keys, the config carries the commit time. The same inputs give the same bytes.

Release image contract (`dist`): FROM scratch, ONE layer. The server image holds myriarchd (the entrypoint), myrctl and
myriarch-sim in /usr/local/bin (each from the archive of that program), the example repositories /opt/myriarch/examples/quickstart
(from the myriarchd archive) and /opt/myriarch/examples/sim (from the myriarch-sim archive), and the empty data directory
/var/lib/myriarchd (0750, owned by --uid:--uid, so that a named volume or a PVC mounted there is writable for the non-root user).
The updater image holds myriarch-updater only. Both hold LICENSE and THIRD_PARTY_NOTICES in /usr/share/licenses/myriarch (0644;
every archive of the image must hold the same two files), /etc/passwd and /etc/group with the user and group --uid (IMAGE_UID of
the build script) and an empty /tmp (1777). Everything else is owned by root and world readable (programs 0755, data 0644,
directories 0755), the image runs as <uid>:<uid>. There is no shell and no package database.

Each binary is checked before it goes into a layer: static ELF of the platform, a Go build for linux with the platform's
GOARCH and CGO_ENABLED=0, the embedded Mozilla roots (no CA bundle in the image) and the release version (--version) as a
string in the binary (the release build stamps it with -X; Go does not record the linker flags, so this is a plausibility check
that the binary belongs to the archive name); myriarchd additionally carries the web UI (-tags embedui, no devpg), and myriarchd
and myrctl the time zone database. With --strict-vcs the binary must also record the build from the commit --revision of a clean
tree (vcs.revision, vcs.modified=false). Each archive must be listed in checksums.txt of --dist with its SHA-256. The example
trees are read from the archives (regular files only: a link, device or path outside the tree is refused), so the whole image is
covered by checksums.txt.

Exit status: 0 = ok, 1 = a check failed (the message names the cause), 2 = usage error.
"""

import argparse
import gzip
import hashlib
import io
import json
import os
import struct
import subprocess
import sys
import tarfile
import tempfile
import zlib

LICENSE_FILES = ("LICENSE", "THIRD_PARTY_NOTICES")
LICENSE_DIR = "usr/share/licenses/myriarch"
OCI_INDEX = "application/vnd.oci.image.index.v1+json"
OCI_MANIFEST = "application/vnd.oci.image.manifest.v1+json"
OCI_CONFIG = "application/vnd.oci.image.config.v1+json"
OCI_LAYER = "application/vnd.oci.image.layer.v1.tar+gzip"
ROOTS_PKG = "golang.org/x/crypto/x509roots/fallback"
# e_machine of the supported platforms (ELF header)
ELF_MACHINE = {"amd64": 62, "arm64": 183}
# Checks that go beyond the common ones (static ELF, Go build settings, embedded roots, version) per program: "ui" = the web UI
# (-tags embedui, never devpg), "tzdata" = the embedded time zone database (the image has no zoneinfo files).
PROGRAMS = {
    "myriarchd": {"ui": True, "tzdata": True},
    "myrctl": {"ui": False, "tzdata": True},
    "myriarch-sim": {"ui": False, "tzdata": False},
    "myriarch-updater": {"ui": False, "tzdata": False},
}
# the contract of the images (what deploy manifests may rely on); the user is the same for every image.
#   binary      the program of the entrypoint; its archive also holds the license files of the image
#   companions  further programs of the image, each from the archive of the same name
#   trees       (program of the archive, directory in the archive, directory in the image): data that is read from an archive
#   dirs        (directory in the image, mode): empty directories that belong to the user of the image
KINDS = {
    "server": {
        "binary": "myriarchd",
        "entrypoint": "/usr/local/bin/myriarchd",
        "companions": ["myrctl", "myriarch-sim"],
        "trees": [
            ("myriarchd", "examples/quickstart", "opt/myriarch/examples/quickstart"),
            ("myriarch-sim", "examples/sim", "opt/myriarch/examples/sim"),
        ],
        "dirs": [("var/lib/myriarchd", 0o750)],
        "cmd": ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"],
        "ports": ["8080/tcp", "9090/tcp", "9464/tcp"],
        "history": "scripts/build-image-crane.sh (FROM scratch): myriarchd, myrctl and myriarch-sim and the examples from the release archives",
    },
    "updater": {
        "binary": "myriarch-updater",
        "entrypoint": "/usr/local/bin/myriarch-updater",
        "companions": [],
        "trees": [],
        "dirs": [],
        "cmd": [],
        "ports": [],
        "history": "scripts/build-image-crane.sh (FROM scratch): myriarch-updater from the release archive",
    },
}
ENV = ["HOME=/tmp", "PATH=/usr/local/bin"]
WORKDIR = "/"
# a tzdata entry that only exists in the embedded zip of time/tzdata (the zip stores its names uncompressed)
TZDATA_MARKER = b"Europe/Amsterdam"


class CheckError(Exception):
    pass


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def dump(obj):
    return json.dumps(obj, sort_keys=True, separators=(",", ":")).encode()


def deterministic_gzip(raw):
    """gzip with a fixed header (no name, no timestamp, OS unknown) and zlib level 9, so the bytes depend on raw only."""
    co = zlib.compressobj(9, zlib.DEFLATED, -15, 9)
    gz = (b"\x1f\x8b\x08\x00\x00\x00\x00\x00\x02\xff" + co.compress(raw) + co.flush()
          + struct.pack("<II", zlib.crc32(raw) & 0xFFFFFFFF, len(raw) & 0xFFFFFFFF))
    if gzip.decompress(gz) != raw:
        raise CheckError("internal error: the gzipped layer does not round-trip")
    return gz


def read_pairs(path):
    """key=value lines (labels, annotations); blank lines are skipped. The first '=' separates the key."""
    out = {}
    if not path:
        return out
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.rstrip("\n")
            if line:
                key, value = line.split("=", 1)
                out[key] = value
    return out


def parse_platform(text):
    os_name, _, arch = text.partition("/")
    if os_name != "linux" or arch not in ELF_MACHINE:
        raise CheckError("unsupported platform '%s' (linux/amd64 or linux/arm64)" % text)
    return os_name, arch


# ---- layout -----------------------------------------------------------------------------------------------------------
def write_layout(outdir, platforms, created, user, entrypoint, cmd, env, workdir, ports, labels, annotations, history):
    """platforms: list of (os, arch, layer tar path). Returns the meta dict (digests). outdir must not exist."""
    blobs = os.path.join(outdir, "blobs", "sha256")
    os.makedirs(blobs)

    def put(data):
        digest = sha256(data)
        with open(os.path.join(blobs, digest), "wb") as f:
            f.write(data)
        return digest

    meta = {}
    descriptors = []
    for os_name, arch, layer_path in sorted(platforms):
        with open(layer_path, "rb") as f:
            raw = f.read()
        gz = deterministic_gzip(raw)
        config = {
            "created": created,
            "architecture": arch,
            "os": os_name,
            "config": {
                "User": user,
                "Env": list(env),
                "Entrypoint": [entrypoint],
                "WorkingDir": workdir,
                "Labels": dict(labels),
            },
            "rootfs": {"type": "layers", "diff_ids": ["sha256:" + sha256(raw)]},
            "history": [{"created": created, "created_by": history}],
        }
        if ports:
            config["config"]["ExposedPorts"] = {p: {} for p in ports}
        if cmd:
            config["config"]["Cmd"] = list(cmd)
        config_bytes = dump(config)
        layer_digest = put(gz)
        config_digest = put(config_bytes)
        manifest_bytes = dump({
            "schemaVersion": 2,
            "mediaType": OCI_MANIFEST,
            "config": {"mediaType": OCI_CONFIG, "digest": "sha256:" + config_digest, "size": len(config_bytes)},
            "layers": [{"mediaType": OCI_LAYER, "digest": "sha256:" + layer_digest, "size": len(gz)}],
        })
        manifest_digest = put(manifest_bytes)
        descriptors.append({
            "mediaType": OCI_MANIFEST,
            "digest": "sha256:" + manifest_digest,
            "size": len(manifest_bytes),
            "platform": {"architecture": arch, "os": os_name},
        })
        suffix = "%s_%s" % (os_name, arch)
        meta["layer_digest." + suffix] = "sha256:" + layer_digest
        meta["layer_size." + suffix] = str(len(gz))
        meta["layer_diff_id." + suffix] = "sha256:" + sha256(raw)
        meta["config_digest." + suffix] = "sha256:" + config_digest
        meta["manifest_digest." + suffix] = "sha256:" + manifest_digest

    index = {"schemaVersion": 2, "mediaType": OCI_INDEX, "manifests": descriptors}
    if annotations:
        index["annotations"] = dict(annotations)
    index_bytes = dump(index)
    with open(os.path.join(outdir, "oci-layout"), "w") as f:
        f.write('{"imageLayoutVersion":"1.0.0"}')
    with open(os.path.join(outdir, "index.json"), "wb") as f:
        f.write(index_bytes)
    meta["index_digest"] = "sha256:" + sha256(index_bytes)
    return meta


def print_meta(meta):
    for key in sorted(meta):
        print("%s=%s" % (key, meta[key]))


# ---- checks of the release binaries -----------------------------------------------------------------------------------
def check_elf(data, name, arch):
    """A static 64-bit little endian ELF executable of the architecture."""
    h = data[:64]
    if len(h) < 64 or h[:4] != b"\x7fELF" or h[4] != 2 or h[5] != 1:
        raise CheckError("%s: not a 64-bit little endian ELF binary" % name)
    machine = int.from_bytes(h[18:20], "little")
    if machine != ELF_MACHINE[arch]:
        raise CheckError("%s: not a linux/%s binary (e_machine %d, expected %d)" % (name, arch, machine, ELF_MACHINE[arch]))
    phoff = int.from_bytes(h[32:40], "little")
    phentsize = int.from_bytes(h[54:56], "little")
    phnum = int.from_bytes(h[56:58], "little")
    for i in range(phnum):
        start = phoff + i * phentsize
        if int.from_bytes(data[start:start + 4], "little") == 3:  # PT_INTERP: dynamically linked
            raise CheckError("%s: dynamically linked (static CGO_ENABLED=0 build expected)" % name)


def go_build_info(data, name):
    """The output of `go version -m` for the binary (the binary is written to a temporary directory)."""
    with tempfile.TemporaryDirectory(prefix="myriarch-image-") as tmp:
        path = os.path.join(tmp, "binary")
        with open(path, "wb") as f:
            f.write(data)
        try:
            res = subprocess.run(["go", "version", "-m", path], capture_output=True, text=True, check=False, cwd=tmp)
        except FileNotFoundError:
            raise CheckError("go not found (the build information of the binaries is read with `go version -m`)")
    if res.returncode != 0:
        raise CheckError("%s is not a Go binary: %s" % (name, res.stderr.strip()))
    return res.stdout


def check_binary(data, name, program, arch, version, revision, strict_vcs):
    check_elf(data, name, arch)
    info = go_build_info(data, name)
    settings = {}
    deps = set()
    for line in info.splitlines():
        parts = line.strip().split("\t")
        if len(parts) >= 2 and parts[0] == "dep":
            deps.add(parts[1])
        elif len(parts) >= 2 and parts[0] == "build":
            key, _, value = parts[1].partition("=")
            settings[key] = value
    for key, want in (("GOOS", "linux"), ("GOARCH", arch), ("CGO_ENABLED", "0")):
        if settings.get(key) != want:
            raise CheckError("%s: build setting %s is '%s', expected '%s'" % (name, key, settings.get(key), want))
    if ROOTS_PKG not in deps:
        raise CheckError("%s does not embed the Mozilla root CAs: cmd/%s must import _ \"%s\" (the image has no CA bundle)"
                         % (name, name, ROOTS_PKG))
    if version.encode() not in data:
        raise CheckError("%s does not contain the release version '%s' (is the archive from another release?)" % (name, version))
    if strict_vcs:
        if settings.get("vcs.revision") != revision:
            raise CheckError("%s was built from commit '%s', the image from '%s'" % (name, settings.get("vcs.revision", "none"), revision))
        if settings.get("vcs.modified") != "false":
            raise CheckError("%s was built from a modified work tree (vcs.modified=%s)" % (name, settings.get("vcs.modified", "unknown")))
    checks = PROGRAMS[program]
    if checks["ui"]:
        tags = settings.get("-tags", "").split(",")
        if "embedui" not in tags or "devpg" in tags:
            raise CheckError("%s: build tags '%s' (the release server carries the web UI and not the embedded PostgreSQL: "
                             "embedui without devpg)" % (name, settings.get("-tags", "")))
    if checks["tzdata"] and TZDATA_MARKER not in data:
        raise CheckError("%s does not embed the time zone database (import _ \"time/tzdata\"; the image has no zoneinfo files)" % name)


# ---- release archives -------------------------------------------------------------------------------------------------
def archive_checksum(dist, archive_name):
    """The SHA-256 that checksums.txt of the release lists for the archive."""
    path = os.path.join(dist, "checksums.txt")
    if not os.path.isfile(path):
        raise CheckError("%s is missing: the archives of a release are bound to its checksums" % path)
    found = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            fields = line.split()
            if len(fields) == 2 and fields[1] == archive_name:
                found.append(fields[0])
    if not found:
        raise CheckError("%s does not list %s" % (path, archive_name))
    if len(found) > 1:
        raise CheckError("%s lists %s %d times (one checksum per archive)" % (path, archive_name, len(found)))
    return found[0]


def read_archive(data, label, wanted, tree=None):
    """The regular files of the archive root named in wanted (name -> bytes); anything else in the archive is ignored.
    tree: a directory of the archive (e.g. "examples/sim") whose files are returned as well, as {path relative to tree: bytes}.
    Below the tree only regular files and directories are accepted, and no path may leave it. The tree must not be empty.
    data holds the bytes of the archive: the bytes that were checked against checksums.txt are the bytes that are read."""
    found = {}
    files = {}
    try:
        tf = tarfile.open(fileobj=io.BytesIO(data), mode="r:gz")
    except (tarfile.TarError, OSError, EOFError, zlib.error) as e:
        raise CheckError("%s is not a gzipped tar archive: %s" % (label, e))
    with tf:
        for member in tf.getmembers():
            name = member.name[2:] if member.name.startswith("./") else member.name
            if tree and (name == tree or name.startswith(tree + "/")):
                rel = name[len(tree) + 1:]
                if not rel:
                    if not member.isdir():
                        raise CheckError("%s: %s is not a directory" % (label, tree))
                    continue
                parts = rel.rstrip("/").split("/")
                if any(part in ("", ".", "..") for part in parts):
                    raise CheckError("%s: %s is not a clean path below %s" % (label, name, tree))
                if member.isdir():
                    continue
                if not member.isreg():
                    raise CheckError("%s: %s is not a regular file" % (label, name))
                if rel in files:
                    raise CheckError("%s: %s occurs twice" % (label, name))
                files[rel] = tf.extractfile(member).read()
                continue
            if name not in wanted:
                continue
            if not member.isreg():
                raise CheckError("%s: %s is not a regular file" % (label, name))
            if name in found:
                raise CheckError("%s: %s occurs twice" % (label, name))
            found[name] = tf.extractfile(member).read()
    missing = [n for n in wanted if n not in found]
    if missing:
        raise CheckError("%s lacks %s at its root" % (label, ", ".join(missing)))
    if tree is None:
        return found
    if not files:
        raise CheckError("%s holds no files below %s" % (label, tree))
    return found, files


def build_layer(programs, licenses, trees, dirs, entrypoint_user, uid, epoch, out_path):
    """The single layer of a release image.
    programs: name -> bytes (installed in /usr/local/bin, 0755); licenses: name -> bytes (LICENSE_FILES, 0644);
    trees: archive path of the directory in the image -> {relative path: bytes} (0644 below directories of 0755);
    dirs: [(archive path, mode)], empty directories owned by uid:uid; entrypoint_user: the name in /etc/passwd."""
    entries = {}

    def add_dir(path, mode=0o755, owner=0):
        parts = path.split("/")
        for i in range(1, len(parts)):
            entries.setdefault("/".join(parts[:i]), ("dir", None, 0o755, 0))
        entries[path] = ("dir", None, mode, owner)

    def add_file(path, data, mode):
        add_dir(os.path.dirname(path))
        entries[path] = ("file", data, mode, 0)

    for name, data in programs.items():
        add_file("usr/local/bin/" + name, data, 0o755)
    for name in LICENSE_FILES:
        add_file(LICENSE_DIR + "/" + name, licenses[name], 0o644)
    for root, files in trees.items():
        add_dir(root)
        for rel, data in files.items():
            add_file(root + "/" + rel, data, 0o644)
    for path, mode in dirs:
        add_dir(path, mode, uid)
    # There is no base image to bring /etc/passwd: the user the image runs as, and a group of the same number.
    add_file("etc/passwd", ("root:x:0:0:root:/root:/sbin/nologin\n%s:x:%d:%d:%s:/tmp:/sbin/nologin\n"
                            % (entrypoint_user, uid, uid, entrypoint_user)).encode(), 0o644)
    add_file("etc/group", ("root:x:0:\n%s:x:%d:\n" % (entrypoint_user, uid)).encode(), 0o644)
    # empty, world writable with sticky bit (a read-only root file system still gets an emptyDir over it)
    add_dir("tmp", 0o1777)

    with tarfile.open(out_path, "w", format=tarfile.PAX_FORMAT) as tar:
        for path in sorted(entries):
            kind, payload, mode, owner = entries[path]
            ti = tarfile.TarInfo(path)
            ti.uid = ti.gid = owner
            ti.uname = ti.gname = "root" if owner == 0 else entrypoint_user
            ti.mtime = epoch
            ti.mode = mode
            if kind == "dir":
                ti.type = tarfile.DIRTYPE
                tar.addfile(ti)
            else:
                ti.type = tarfile.REGTYPE
                ti.size = len(payload)
                tar.addfile(ti, io.BytesIO(payload))


# ---- subcommands ------------------------------------------------------------------------------------------------------
def cmd_layout(args):
    platforms = []
    for spec in args.platform:
        plat, _, layer = spec.partition("=")
        os_name, arch = parse_platform(plat)
        platforms.append((os_name, arch, layer))
    meta = write_layout(
        args.out, platforms, args.created, args.user, args.entrypoint, args.cmd.split(",") if args.cmd else [],
        args.env.split(","), args.workdir, args.ports.split(",") if args.ports else [], read_pairs(args.labels),
        read_pairs(args.annotations), args.history)
    print_meta(meta)


def load_archive(args, program, os_name, arch):
    """The bytes of the archive of the program for the platform, after the check against checksums.txt."""
    name = "%s_%s_%s_%s.tar.gz" % (program, args.version, os_name, arch)
    path = os.path.join(args.dist, name)
    if not os.path.isfile(path):
        raise CheckError("%s is missing: run the release build first (make release-snapshot, or goreleaser for a release)" % path)
    want = archive_checksum(args.dist, name)
    with open(path, "rb") as f:
        archive = f.read()
    got = sha256(archive)
    if got != want:
        raise CheckError("%s: SHA-256 %s differs from checksums.txt (%s)" % (name, got, want))
    return name, archive


def cmd_dist(args):
    kind = KINDS[args.kind]
    main_program = kind["binary"]
    platforms = []
    # The compressed layer (and with it the manifest and index digests) depends on the zlib implementation; the uncompressed
    # layer (diff_id) does not. The version is logged so that a rebuild with other digests can be traced to this cause.
    print("[image] zlib %s (layer compression)" % zlib.ZLIB_RUNTIME_VERSION, file=sys.stderr)
    with tempfile.TemporaryDirectory(prefix="myriarch-image-layers-") as tmp:
        for spec in args.platform:
            os_name, arch = parse_platform(spec)
            programs = {}
            licenses = {}
            trees = {}
            for program in [main_program] + kind["companions"]:
                name, archive = load_archive(args, program, os_name, arch)
                tree_dirs = [t for t in kind["trees"] if t[0] == program]
                if len(tree_dirs) > 1:
                    raise CheckError("internal error: more than one tree of %s" % program)
                if tree_dirs:
                    files, tree_files = read_archive(archive, name, (program,) + LICENSE_FILES, tree_dirs[0][1])
                    trees[tree_dirs[0][2]] = tree_files
                else:
                    files = read_archive(archive, name, (program,) + LICENSE_FILES)
                check_binary(files[program], "%s (%s/%s)" % (program, os_name, arch), program, arch, args.version,
                             args.revision, args.strict_vcs)
                programs[program] = files[program]
                # the license files of the image come from the archive of the entrypoint; the others must carry the same texts
                for lic in LICENSE_FILES:
                    if program == main_program:
                        licenses[lic] = files[lic]
                    elif files[lic] != licenses[lic]:
                        raise CheckError("%s: %s differs from the one in %s_%s_%s_%s.tar.gz (the archives are not from one build)"
                                         % (name, lic, main_program, args.version, os_name, arch))
            layer = os.path.join(tmp, "layer-%s-%s.tar" % (os_name, arch))
            build_layer(programs, licenses, trees, kind["dirs"], "myriarch", args.uid, args.epoch, layer)
            platforms.append((os_name, arch, layer))
        meta = write_layout(
            args.out, platforms, args.created, "%d:%d" % (args.uid, args.uid), kind["entrypoint"], kind["cmd"], ENV, WORKDIR,
            kind["ports"], read_pairs(args.labels), read_pairs(args.annotations), kind["history"])
    print_meta(meta)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("layout", help="layout from existing layer tars")
    p.add_argument("--out", required=True)
    p.add_argument("--created", required=True)
    p.add_argument("--user", required=True)
    p.add_argument("--entrypoint", required=True)
    p.add_argument("--cmd", default="")
    p.add_argument("--env", required=True)
    p.add_argument("--workdir", required=True)
    p.add_argument("--ports", default="")
    p.add_argument("--labels", required=True)
    p.add_argument("--annotations", default="")
    p.add_argument("--history", required=True)
    p.add_argument("--platform", action="append", required=True, help="linux/<arch>=<layer tar>")
    p.set_defaults(func=cmd_layout)

    p = sub.add_parser("dist", help="layout of a release image from the goreleaser archives")
    p.add_argument("--kind", choices=sorted(KINDS), required=True)
    p.add_argument("--dist", required=True, help="goreleaser dist directory")
    p.add_argument("--version", required=True, help="release version without v (the part of the archive names)")
    p.add_argument("--epoch", type=int, required=True, help="mtime of all layer entries")
    p.add_argument("--created", required=True)
    p.add_argument("--uid", type=int, required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--labels", required=True)
    p.add_argument("--annotations", default="")
    p.add_argument("--revision", default="", help="commit the binaries must record (with --strict-vcs)")
    p.add_argument("--strict-vcs", action="store_true", help="require vcs.revision == --revision and vcs.modified=false")
    p.add_argument("--platform", action="append", required=True, help="linux/<arch>")
    p.set_defaults(func=cmd_dist)

    args = parser.parse_args(argv)
    try:
        args.func(args)
    except CheckError as e:
        print("[image] ERROR: %s" % e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
