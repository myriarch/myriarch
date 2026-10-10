#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Authenticode handling of the Windows release files (standard library only).

Every use of osslsigncode goes through this file. It is called by deploy/release/windows-signed.sh (stage collect and apply) and by
ci/release/windows-sign-hook.sh (the hook between them); always as `python3 -I ci/release/authenticode.py <command>`.

  sign          --provider off|test --in DIR --out DIR [--material DIR]    the hook: copy the unsigned set, or sign it with a test CA
  accept-exe    --name N --fresh FILE --state S --unsigned U --signed S --trust T [--release]    stage apply, per rebuilt .exe
  accept-scripts --root R --state S --unsigned U --signed S --trust T [--release]    stage apply, the two Windows scripts
  seal          --root R --state S --dir U     stage collect: complete the unsigned set (scripts, SHA256SUMS)
  verify        --trust T FILE...               verify files against a trust directory and print what was found

The sets (directories) are described in deploy/release/README.md: the unsigned set has the four .exe files, install-windows.ps1,
Set-ServiceEnvironment.ps1 and SHA256SUMS; the signed set has the same names plus signer.json.

osslsigncode is taken only from ${TOOLS_BIN:-<root>/.cache/bin} (never from PATH, so that a distribution package cannot slip in) and
has to be the version pinned in ci/versions.env. Errors are printed as "[ci] ERROR: [token] message" and end with status 1.
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

EXES = (
    "myriarch-updater_windows_amd64.exe",
    "myriarch-updater_windows_arm64.exe",
    "myrctl_windows_amd64.exe",
    "myrctl_windows_arm64.exe",
)
SCRIPTS = {
    "install-windows.ps1": "deploy/scripts/install-windows.ps1",
    "Set-ServiceEnvironment.ps1": "deploy/windows/Set-ServiceEnvironment.ps1",
}
# staging directories of the archives (deploy/release/goreleaser.yaml): name -> path below STATE/archive/windows
SCRIPT_STAGE = {
    "install-windows.ps1": "scripts/install-windows.ps1",
    "Set-ServiceEnvironment.ps1": "windows/Set-ServiceEnvironment.ps1",
}
PAYLOAD = EXES + tuple(sorted(SCRIPTS))
SUMS = "SHA256SUMS"
SIGNER = "signer.json"
PROVIDERS = ("off", "test", "signpath", "kms")
MAX_FILE = 256 * 1024 * 1024
TIMEOUT = 120
SIG_BEGIN = b"# SIG # Begin signature block"
SIG_END = b"# SIG # End signature block"
SHA_RE = re.compile(r"^[0-9a-f]{64}$")


class Fail(Exception):
    def __init__(self, token, message):
        super().__init__("[%s] %s" % (token, message))


def fail(token, message):
    raise Fail(token, message)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


# ---- osslsigncode ---------------------------------------------------------------------------------------------------------------


class Tool:
    """The pinned osslsigncode from the tool cache."""

    def __init__(self, root):
        tools_bin = os.environ.get("TOOLS_BIN") or str(Path(root) / ".cache" / "bin")
        self.path = str(Path(tools_bin) / "osslsigncode")
        self.root = Path(root)
        self._checked = False

    def _pinned_version(self):
        text = (self.root / "ci" / "versions.env").read_text(encoding="utf-8")
        m = re.search(r"^OSSLSIGNCODE_VERSION=([0-9][0-9.]*)$", text, re.M)
        if not m:
            fail("osslsigncode_version", "OSSLSIGNCODE_VERSION is missing in ci/versions.env")
        return m.group(1)

    def check(self):
        if self._checked:
            return
        if not (os.path.isfile(self.path) and os.access(self.path, os.X_OK)):
            fail("osslsigncode_missing", "%s not found (ci/install-tools.sh osslsigncode)" % self.path)
        want = "osslsigncode %s, using:" % self._pinned_version()
        rc, out = self._run(["--version"])
        first = out.splitlines()[0] if out.splitlines() else ""
        if rc != 0 or first != want:
            fail("osslsigncode_version", "%s reports '%s', expected '%s'" % (self.path, first, want))
        self._checked = True

    def _run(self, args):
        env = {"LC_ALL": "C", "PATH": os.environ.get("PATH", "")}
        try:
            p = subprocess.run([self.path] + list(args), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                               env=env, timeout=TIMEOUT)
        except subprocess.TimeoutExpired:
            fail("osslsigncode_timeout", "osslsigncode %s did not finish within %d s" % (args[0] if args else "", TIMEOUT))
        return p.returncode, p.stdout.decode("utf-8", "replace")

    def run(self, args):
        self.check()
        return self._run(args)


# ---- sets and trust ---------------------------------------------------------------------------------------------------------------


class FileSet:
    def __init__(self, directory, kind, files, sums):
        self.dir = Path(directory)
        self.kind = kind
        self.files = files  # name -> Path
        self.sums = sums  # name -> sha256 (from SHA256SUMS)

    def path(self, name):
        return self.files[name]


def parse_sums(path, directory):
    data = Path(path).read_bytes()
    try:
        text = data.decode("ascii")
    except UnicodeDecodeError:
        fail("sums_mismatch", "%s/SHA256SUMS is not ASCII" % directory)
    if not text.endswith("\n"):
        fail("sums_mismatch", "%s/SHA256SUMS does not end with a newline" % directory)
    lines = text.split("\n")[:-1]
    entries = []
    for line in lines:
        m = re.match(r"^([0-9a-f]{64})  (\S+)$", line)
        if not m:
            fail("sums_mismatch", "%s/SHA256SUMS: malformed line '%s'" % (directory, line[:80]))
        entries.append((m.group(2), m.group(1)))
    names = [n for n, _ in entries]
    if len(entries) != len(PAYLOAD):
        fail("sums_mismatch", "%s/SHA256SUMS has %d lines, expected %d" % (directory, len(entries), len(PAYLOAD)))
    if names != sorted(names):
        fail("sums_mismatch", "%s/SHA256SUMS is not sorted" % directory)
    if sorted(names) != sorted(PAYLOAD):
        fail("sums_mismatch", "%s/SHA256SUMS: the names differ from the payload (%s)" % (directory, ", ".join(sorted(set(names) ^ set(PAYLOAD)))))
    return dict(entries)


def load_set(directory, kind):
    d = Path(directory)
    if not d.is_dir() or d.is_symlink():
        fail("set_incomplete", "%s is not a directory" % directory)
    want = set(PAYLOAD) | {SUMS}
    if kind == "signed":
        want.add(SIGNER)
    files = {}
    for entry in sorted(os.listdir(d)):
        p = d / entry
        if p.is_symlink() or not p.is_file():
            fail("set_incomplete", "%s: %s is not a regular file" % (directory, entry))
        if p.stat().st_size > MAX_FILE:
            fail("set_incomplete", "%s: %s is larger than %d MiB" % (directory, entry, MAX_FILE // (1024 * 1024)))
        files[entry] = p
    got = set(files)
    if got != want:
        fail("set_incomplete", "%s: missing %s, unexpected %s" % (directory, sorted(want - got) or "nothing", sorted(got - want) or "nothing"))
    sums = parse_sums(files[SUMS], directory)
    for name in PAYLOAD:
        actual = sha256_file(files[name])
        if actual != sums[name]:
            fail("sums_mismatch", "%s/SHA256SUMS: %s has sha256 %s, the list says %s" % (directory, name, actual, sums[name]))
    return FileSet(directory, kind, files, sums)


def load_trust(directory):
    d = Path(directory)
    ca, tsa, subj = d / "ca.pem", d / "tsa-ca.pem", d / "subject.txt"
    if not (ca.is_file() and tsa.is_file() and subj.is_file()):
        fail("trust_missing", "%s/ca.pem, tsa-ca.pem or subject.txt is missing: signed Windows files cannot be accepted before the "
                              "provider's chain is committed (deploy/release/authenticode/README.md)" % directory)
    lines = [ln for ln in subj.read_text(encoding="utf-8").splitlines() if ln.strip()]
    if len(lines) != 1:
        fail("trust_missing", "%s must hold exactly one non-empty line (the expected subject)" % subj)
    return {"ca": str(ca.resolve()), "tsa_ca": str(tsa.resolve()), "subject": lines[0].strip()}


# ---- verification -----------------------------------------------------------------------------------------------------------------


def verify(tool, path, trust):
    """Verifies one file against the trust anchors. osslsigncode exits 0 for a missing or invalid timestamp as long as the signing
    certificate is currently valid, so the output is parsed rule by rule."""
    rc, out = tool.run(["verify", "-CAfile", trust["ca"], "-TSA-CAfile", trust["tsa_ca"], "-in", str(path)])
    lines = [ln.strip() for ln in out.splitlines()]
    name = os.path.basename(str(path))
    if rc != 0:
        fail("signature_invalid", "%s: osslsigncode verify exited with status %d:\n%s" % (name, rc, "\n".join(lines[-20:])))
    if sum(1 for ln in lines if ln.startswith("Signature Index:")) != 1 or "Number of verified signatures: 1" not in lines:
        fail("signature_count", "%s must carry exactly one signature" % name)
    digest = None
    for ln in lines:
        m = re.match(r"^Message digest algorithm\s*:\s*(\S+)", ln)
        if m:
            digest = m.group(1)
            break
    if digest != "SHA256":
        fail("digest_weak", "%s: the message digest algorithm is %s, expected SHA256" % (name, digest))
    start = next((i for i, ln in enumerate(lines) if ln == "Signer's certificate:"), None)
    if start is None:
        fail("signature_invalid", "%s: no signer's certificate in the verify output" % name)

    def after(prefix, begin):
        for ln in lines[begin:]:
            if ln.startswith(prefix):
                return ln[len(prefix):].strip()
        return None

    subject = after("Subject:", start)
    if subject != trust["subject"]:
        fail("subject_mismatch", "%s: signed by '%s', expected '%s'" % (name, subject, trust["subject"]))
    if any(ln == "Timestamp is not available" for ln in lines):
        fail("timestamp_missing", "%s carries no RFC 3161 timestamp" % name)
    ts = [ln for ln in lines if ln.startswith("Timestamp Server Signature verification")]
    if ts != ["Timestamp Server Signature verification: ok"]:
        fail("timestamp_invalid", "%s: the timestamp does not verify against the committed timestamp roots (%s)" % (name, "; ".join(ts) or "no verification line"))
    sig = [ln for ln in lines if re.match(r"^Signature verification:", ln)]
    if not sig or sig[-1] != "Signature verification: ok" or any(ln.endswith("failed") for ln in sig):
        fail("signature_invalid", "%s: the signature does not verify against the committed chain" % name)
    return {
        "issuer": after("Issuer :", start) or "",
        "serial": after("Serial :", start) or "",
        "subject": subject,
        "timestamp": after("Timestamp time:", 0) or "",
    }


def attach_signature(tool, work, name, signed_file, unsigned_file, error_token):
    """Extracts the signature of signed_file and attaches it to the copy of unsigned_file. Returns the path of the result."""
    shutil.rmtree(work, ignore_errors=True)
    (work / "in").mkdir(parents=True)
    (work / "out").mkdir()
    shutil.copyfile(unsigned_file, work / "in" / name)
    sig = work / "sig.p7"
    rc, out = tool.run(["extract-signature", "-in", str(signed_file), "-out", str(sig)])
    if rc != 0 or not sig.is_file():
        fail("signature_unreadable", "no signature could be extracted from %s: %s" % (name, " ".join(out.split())[-200:]))
    result = work / "out" / name
    rc, out = tool.run(["attach-signature", "-sigin", str(sig), "-in", str(work / "in" / name), "-out", str(result)])
    if rc != 0 or not result.is_file():
        fail(error_token, "the signature in %s does not cover this build: the provider signed other content" % name)
    return result


# ---- signer.json ------------------------------------------------------------------------------------------------------------------


def check_signed(unsigned, signed, trust_dir, release):
    """Validates signer.json against both sets. Returns (info dict, trust or None)."""
    try:
        doc = json.loads(signed.files[SIGNER].read_bytes().decode("utf-8"))
    except (ValueError, UnicodeDecodeError) as e:
        fail("signer_json", "signer.json is not valid JSON: %s" % e)

    def bad(msg):
        fail("signer_json", msg)

    if not isinstance(doc, dict) or set(doc) != {"schema", "provider", "signed", "subject", "files"}:
        bad("expected an object with exactly the keys schema, provider, signed, subject and files")
    if type(doc["schema"]) is not int or doc["schema"] != 1:
        bad("schema must be 1")
    if doc["provider"] not in PROVIDERS:
        bad("provider must be one of %s" % ", ".join(PROVIDERS))
    if not isinstance(doc["signed"], bool):
        bad("signed must be a boolean")
    if (doc["provider"] == "off") != (doc["signed"] is False):
        bad("provider off and signed false belong together")
    if release and doc["provider"] == "test":
        fail("provider_test_release", "signer.json names the test provider; a release build accepts only off, signpath or kms")
    trust = None
    if doc["signed"]:
        trust = load_trust(trust_dir)
        if doc["subject"] != trust["subject"]:
            bad("subject '%s' is not the expected subject '%s'" % (doc["subject"], trust["subject"]))
    elif doc["subject"] is not None:
        bad("subject must be null when nothing is signed")
    files = doc["files"]
    if not isinstance(files, dict) or set(files) != set(PAYLOAD):
        bad("files must list exactly the six payload files")
    for name in PAYLOAD:
        entry = files[name]
        keys = {"sha256", "unsigned_sha256"}
        if doc["signed"]:
            keys |= {"subject", "issuer", "serial", "timestamp"}
        if not isinstance(entry, dict) or set(entry) != keys:
            bad("files.%s must have exactly the keys %s" % (name, ", ".join(sorted(keys))))
        if entry["unsigned_sha256"] != unsigned.sums[name]:
            bad("files.%s.unsigned_sha256 does not match the unsigned SHA256SUMS" % name)
        if entry["sha256"] != signed.sums[name]:
            bad("files.%s.sha256 does not match the signed SHA256SUMS" % name)
        if doc["signed"]:
            if entry["subject"] != doc["subject"]:
                bad("files.%s.subject differs from the top-level subject" % name)
            for k in ("issuer", "serial", "timestamp"):
                if not isinstance(entry[k], str) or not entry[k]:
                    bad("files.%s.%s must be a non-empty string" % (name, k))
    return doc, trust


# ---- commands ---------------------------------------------------------------------------------------------------------------------


def record_accepted(state, name, digest):
    d = Path(state) / "accepted"
    d.mkdir(parents=True, exist_ok=True)
    (d / (name + ".sha256")).write_text(digest + "\n", encoding="ascii")


def read_accepted(state, name):
    p = Path(state) / "accepted" / (name + ".sha256")
    try:
        value = p.read_text(encoding="ascii").strip()
    except OSError:
        return None
    return value if SHA_RE.match(value) else None


def cmd_accept_exe(a):
    tool = Tool(a.root)
    name = a.name
    if name not in EXES:
        fail("unexpected_target", "%s is not one of the Windows executables" % name)
    unsigned = load_set(a.unsigned, "unsigned")
    fresh = Path(a.fresh)
    fresh_sha = sha256_file(fresh)
    if fresh_sha != unsigned.sums[name]:
        fail("not_reproducible", "the rebuilt %s (sha256 %s) differs from the unsigned build (%s): the build is not reproducible or its "
                                 "inputs changed" % (name, fresh_sha, unsigned.sums[name]))
    signed = load_set(a.signed, "signed")
    doc, trust = check_signed(unsigned, signed, a.trust, a.release)
    if not doc["signed"]:
        if sha256_file(signed.path(name)) != fresh_sha:
            fail("unsigned_changed", "signer.json says unsigned, but %s differs from the unsigned build" % name)
        label = "unsigned"
    else:
        work = Path(a.state) / "work" / name
        result = attach_signature(tool, work, name, signed.path(name), fresh, "signature_mismatch")
        info = verify(tool, result, trust)
        if info["subject"] != doc["subject"]:
            fail("subject_mismatch", "%s: signed by '%s', signer.json says '%s'" % (name, info["subject"], doc["subject"]))
        tmp = fresh.with_name(fresh.name + ".accepted")
        shutil.copyfile(result, tmp)
        os.replace(tmp, fresh)
        label = "signed by %s" % info["subject"]
    record_accepted(a.state, name, sha256_file(fresh))
    print("accepted %s (%s)" % (name, label), file=sys.stderr)


def split_signed_script(name, path, data):
    """Returns the text before the signature block of a signed script, with the one line ending that precedes the block removed."""
    if data.count(SIG_BEGIN) != 1:
        fail("script_changed", "%s: the signed file must hold the '# SIG # Begin signature block' marker exactly once" % name)
    i = data.index(SIG_BEGIN)
    block = data[i:]
    if block.count(SIG_END) != 1 or not (block.endswith(SIG_END) or block.endswith(SIG_END + b"\r\n") or block.endswith(SIG_END + b"\n")):
        fail("script_changed", "%s: the signature block must end with '# SIG # End signature block' at the end of the file" % name)
    for line in block.replace(b"\r\n", b"\n").split(b"\n"):
        if line and not line.startswith(b"# "):
            fail("script_changed", "%s: text inside the signature block that is not a comment line" % name)
    prefix = data[:i]
    if prefix.endswith(b"\r\n"):
        return prefix[:-2]
    if prefix.endswith(b"\n"):
        return prefix[:-1]
    return None


def cmd_accept_scripts(a):
    tool = Tool(a.root)
    root = Path(a.root)
    unsigned = load_set(a.unsigned, "unsigned")
    signed = load_set(a.signed, "signed")
    doc, trust = check_signed(unsigned, signed, a.trust, a.release)
    for name, rel in sorted(SCRIPTS.items()):
        repo = root / rel
        repo_bytes = repo.read_bytes()
        if unsigned.sums[name] != hashlib.sha256(repo_bytes).hexdigest():
            fail("not_reproducible", "the unsigned build's %s differs from %s" % (name, rel))
        provided = signed.path(name).read_bytes()
        if not doc["signed"]:
            if provided != repo_bytes:
                fail("unsigned_changed", "signer.json says unsigned, but %s differs from %s" % (name, rel))
            staged = repo_bytes
            label = "unsigned"
        else:
            prefix = split_signed_script(name, rel, provided)
            if prefix != repo_bytes:
                fail("script_changed", "%s: the text before '# SIG # Begin signature block' differs from %s" % (name, rel))
            work = Path(a.state) / "work" / name
            result = attach_signature(tool, work, name, signed.path(name), repo, "signature_mismatch")
            info = verify(tool, result, trust)
            if info["subject"] != doc["subject"]:
                fail("subject_mismatch", "%s: signed by '%s', signer.json says '%s'" % (name, info["subject"], doc["subject"]))
            staged = result.read_bytes()
            label = "signed by %s" % info["subject"]
        target = Path(a.state) / "archive" / "windows" / SCRIPT_STAGE[name]
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(staged)
        os.chmod(target, 0o644)
        record_accepted(a.state, name, hashlib.sha256(staged).hexdigest())
        print("accepted %s (%s)" % (name, label), file=sys.stderr)


def write_sums(directory, sums):
    text = "".join("%s  %s\n" % (sums[n], n) for n in sorted(sums))
    Path(directory, SUMS).write_text(text, encoding="ascii")


def cmd_seal(a):
    root = Path(a.root)
    d = Path(a.dir)
    present = sorted(os.listdir(d)) if d.is_dir() else []
    if present != sorted(EXES):
        fail("set_incomplete", "%s: expected exactly the four .exe files, found %s" % (a.dir, present))
    sums = {}
    for name in EXES:
        p = d / name
        if p.is_symlink() or not p.is_file():
            fail("set_incomplete", "%s/%s is not a regular file" % (a.dir, name))
        digest = sha256_file(p)
        if read_accepted(a.state, name) != digest:
            fail("not_reproducible", "%s differs from the build recorded by the post-build hook" % name)
        sums[name] = digest
    for name, rel in SCRIPTS.items():
        shutil.copyfile(root / rel, d / name)
        os.chmod(d / name, 0o644)
        sums[name] = sha256_file(d / name)
    write_sums(d, sums)
    print("sealed %s: %d files" % (a.dir, len(PAYLOAD) + 1), file=sys.stderr)


def fresh_out_dir(path):
    p = Path(path)
    if p.exists() and (not p.is_dir() or os.listdir(p)):
        fail("staging_not_empty", "%s is not empty: remove it first" % path)
    p.mkdir(parents=True, exist_ok=True)
    return p


def copy_set(src, dst):
    for name in PAYLOAD:
        shutil.copyfile(src.path(name), dst / name)
        os.chmod(dst / name, 0o644)


def cmd_sign(a):
    unsigned = load_set(a.input, "unsigned")
    out = fresh_out_dir(a.output)
    infos = {}
    if a.provider == "off":
        copy_set(unsigned, out)
        for name in PAYLOAD:
            if sha256_file(out / name) != unsigned.sums[name]:
                fail("copy_mismatch", "%s was not copied byte for byte" % name)
        doc_signed, subject = False, None
    else:
        if not a.material:
            fail("material_missing", "--material is required for the test provider")
        tool = Tool(a.root)
        m = Path(a.material)
        sd = m / "signing"
        for f in ("signer.pem", "signer.key", "tsa.pem", "tsa.key"):
            if not (sd / f).is_file():
                fail("material_missing", "%s/signing/%s is missing" % (a.material, f))
        trust = load_trust(m / "trust")
        for name in PAYLOAD:
            rc, text = tool.run(["sign", "-certs", str(sd / "signer.pem"), "-key", str(sd / "signer.key"), "-h", "sha256", "-n", "Myriarch",
                                 "-i", "https://myriarch.dev/", "-TSA-certs", str(sd / "tsa.pem"), "-TSA-key", str(sd / "tsa.key"),
                                 "-TSA-time", str(int(time.time())), "-in", str(unsigned.path(name)), "-out", str(out / name)])
            if rc != 0 or not (out / name).is_file():
                fail("sign_failed", "osslsigncode could not sign %s: %s" % (name, " ".join(text.split())[-200:]))
            infos[name] = verify(tool, out / name, trust)
        subjects = {i["subject"] for i in infos.values()}
        if len(subjects) != 1:
            fail("subject_mismatch", "the signed files carry different subjects")
        doc_signed, subject = True, subjects.pop()
    sums = {n: sha256_file(out / n) for n in PAYLOAD}
    write_sums(out, sums)
    files = {}
    for n in PAYLOAD:
        entry = {"sha256": sums[n], "unsigned_sha256": unsigned.sums[n]}
        if doc_signed:
            entry.update(infos[n])
        files[n] = entry
    doc = {"files": files, "provider": a.provider, "schema": 1, "signed": doc_signed, "subject": subject}
    Path(out, SIGNER).write_text(json.dumps(doc, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print("windows-sign-hook: provider %s, signed %s, %d files" % (a.provider, "true" if doc_signed else "false", len(PAYLOAD)))


def cmd_verify(a):
    tool = Tool(a.root)
    trust = load_trust(a.trust)
    result = {}
    for f in a.files:
        result[f] = verify(tool, f, trust)
    print(json.dumps(result, indent=2, sort_keys=True))


def main(argv=None):
    default_root = str(Path(__file__).resolve().parents[2])
    ap = argparse.ArgumentParser(prog="authenticode.py", description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)

    def add(name, func):
        p = sub.add_parser(name)
        p.add_argument("--root", default=default_root)
        p.set_defaults(func=func)
        return p

    p = add("sign", cmd_sign)
    p.add_argument("--provider", required=True, choices=("off", "test"))
    p.add_argument("--in", dest="input", required=True)
    p.add_argument("--out", dest="output", required=True)
    p.add_argument("--material")
    p = add("accept-exe", cmd_accept_exe)
    p.add_argument("--name", required=True)
    p.add_argument("--fresh", required=True)
    p.add_argument("--state", required=True)
    p.add_argument("--unsigned", required=True)
    p.add_argument("--signed", required=True)
    p.add_argument("--trust", required=True)
    p.add_argument("--release", action="store_true")
    p = add("accept-scripts", cmd_accept_scripts)
    p.add_argument("--state", required=True)
    p.add_argument("--unsigned", required=True)
    p.add_argument("--signed", required=True)
    p.add_argument("--trust", required=True)
    p.add_argument("--release", action="store_true")
    p = add("seal", cmd_seal)
    p.add_argument("--state", required=True)
    p.add_argument("--dir", required=True)
    p = add("verify", cmd_verify)
    p.add_argument("--trust", required=True)
    p.add_argument("files", nargs="+")
    args = ap.parse_args(argv)
    try:
        args.func(args)
    except Fail as e:
        print("[ci] ERROR: %s" % e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
