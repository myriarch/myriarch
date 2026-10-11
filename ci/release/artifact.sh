#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Packs a directory into one deterministic tar file and unpacks such a file again: the artifact protocol of the release workflow
# (ADR-0035). A job hands a tree to the next job as ONE tar file (actions/upload-artifact with archive: false keeps the file modes),
# and the consumer unpacks it only after it has compared the SHA-256 that the producing job reported.
#
#   ci/release/artifact.sh pack   --dir DIR --out FILE [--only NAME]
#   ci/release/artifact.sh unpack --file FILE --sha256 HEX --into DIR
#
# pack     Writes FILE (it must not exist) from the tree below DIR, or from DIR/NAME only. The entries are sorted by name and carry
#          no time, owner or group, so the same tree always gives the same bytes; the paths are relative (no "./"), hidden files are
#          included, the permission bits are kept. A symbolic link or a special file in the tree is refused. stdout: sha256=<hex>.
#          Codes: artifact_exists, artifact_empty, artifact_entry_invalid.
# unpack   Computes the SHA-256 of FILE first and refuses a mismatch, lists the entries and refuses an absolute path, a ".."
#          component, a control character, a link, a device or a FIFO, refuses an INTO that exists and is not empty, and extracts with
#          --no-same-owner, keeping the modes. It needs only bash, tar and a SHA-256 program, so it also runs in Git Bash on
#          Windows. Codes: artifact_digest_invalid, artifact_digest_mismatch, artifact_entry_invalid, artifact_target_not_empty.
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="artifact.sh pack --dir DIR --out FILE [--only NAME] | unpack --file FILE --sha256 HEX --into DIR"

pack() {
  local dir="" out="" only=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --dir) need_value "$1" "$#"; dir="$2"; shift 2 ;;
      --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
      --only) need_value "$1" "$#"; only="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${dir}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
  check_text artifact_entry_invalid "--dir" "${dir}"
  check_text artifact_entry_invalid "--out" "${out}"
  check_text artifact_entry_invalid "--only" "${only}"
  [ -d "${dir}" ] || err artifact_empty "${dir} is not a directory"
  [ ! -e "${out}" ] && [ ! -L "${out}" ] || err artifact_exists "${out} exists already"
  have python3 || err tool_missing "python3 was not found"
  mkdir -p "$(dirname "${out}")"
  python3 -I - "${dir}" "${out}" "${only}" <<'PY'
import hashlib, os, re, stat, sys, tarfile

root, out, only = sys.argv[1], sys.argv[2], sys.argv[3]
SCRIPT = "artifact"


def refuse(code, msg):
    print("[%s] ERROR [%s] %s" % (SCRIPT, code, msg), file=sys.stderr)
    sys.exit(1)


def bad_name(rel):
    return any(ord(c) < 32 or ord(c) == 127 for c in rel)


start = root
if only:
    if only.startswith("/") or ".." in only.split("/") or "/" in only.strip("/"):
        refuse("artifact_entry_invalid", "--only must be one name below --dir")
    start = os.path.join(root, only)
    if not os.path.lexists(start):
        refuse("artifact_empty", "%s has no entry %s" % (root, only))

entries = []  # (relative name, absolute path, lstat)


def add(abs_path, rel):
    if bad_name(rel):
        refuse("artifact_entry_invalid", "an entry name below %s has a control character" % root)
    st = os.lstat(abs_path)
    if stat.S_ISLNK(st.st_mode):
        refuse("artifact_entry_invalid", "%s is a symbolic link" % rel)
    if stat.S_ISDIR(st.st_mode):
        entries.append((rel, abs_path, st))
        for name in sorted(os.listdir(abs_path), key=lambda n: n.encode("utf-8", "surrogateescape")):
            add(os.path.join(abs_path, name), rel + "/" + name if rel else name)
    elif stat.S_ISREG(st.st_mode):
        entries.append((rel, abs_path, st))
    else:
        refuse("artifact_entry_invalid", "%s is not a regular file or directory" % rel)


if only:
    add(start, only)
else:
    for name in sorted(os.listdir(root), key=lambda n: n.encode("utf-8", "surrogateescape")):
        add(os.path.join(root, name), name)
if not entries:
    refuse("artifact_empty", "%s holds nothing to pack" % root)

entries.sort(key=lambda e: e[0].encode("utf-8", "surrogateescape"))
with open(out, "xb") as raw:
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for rel, abs_path, st in entries:
            info = tarfile.TarInfo(rel)
            info.mtime = 0
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            if stat.S_ISDIR(st.st_mode):
                info.type = tarfile.DIRTYPE
                info.mode = stat.S_IMODE(st.st_mode) & 0o777
                tar.addfile(info)
            else:
                info.type = tarfile.REGTYPE
                info.mode = stat.S_IMODE(st.st_mode) & 0o777
                info.size = st.st_size
                with open(abs_path, "rb") as f:
                    tar.addfile(info, f)
h = hashlib.sha256()
with open(out, "rb") as f:
    for chunk in iter(lambda: f.read(1 << 20), b""):
        h.update(chunk)
print("sha256=" + h.hexdigest())
PY
}

unpack() {
  local file="" want="" into="" got names types n1 n2 line
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --file) need_value "$1" "$#"; file="$2"; shift 2 ;;
      --sha256) need_value "$1" "$#"; want="$2"; shift 2 ;;
      --into) need_value "$1" "$#"; into="$2"; shift 2 ;;
      *) usage_err "${USAGE}" ;;
    esac
  done
  [ -n "${file}" ] && [ -n "${want}" ] && [ -n "${into}" ] || usage_err "${USAGE}"
  check_match artifact_digest_invalid "--sha256" "${want}" "${SHA256_RE}"
  check_text artifact_entry_invalid "--file" "${file}"
  check_text artifact_entry_invalid "--into" "${into}"
  [ -f "${file}" ] || err artifact_entry_invalid "${file} is not a file"
  got="$(sha256_of "${file}")"
  [ "${got}" = "${want}" ] || err artifact_digest_mismatch "${file} has the SHA-256 ${got}, the producing job reported ${want}"
  have tar || err tool_missing "tar was not found"
  if [ -d "${into}" ] && [ -n "$(ls -A "${into}")" ]; then err artifact_target_not_empty "${into} exists and is not empty"; fi
  [ ! -e "${into}" ] || [ -d "${into}" ] || err artifact_target_not_empty "${into} exists and is not a directory"
  names="$(tar -tf "${file}" 2>/dev/null)" || err artifact_entry_invalid "${file} is not a readable tar file"
  types="$(tar -tvf "${file}" 2>/dev/null)" || err artifact_entry_invalid "${file} is not a readable tar file"
  n1="$(printf '%s\n' "${names}" | wc -l | tr -d ' ')"
  n2="$(printf '%s\n' "${types}" | wc -l | tr -d ' ')"
  [ "${n1}" = "${n2}" ] || err artifact_entry_invalid "the two listings of ${file} differ"
  while IFS= read -r line; do
    case "${line}" in
      /*|*/../*|../*|*/..|..) err artifact_entry_invalid "an entry of ${file} is an absolute path or has a .. component" ;;
      [A-Za-z]:*|*\\*) err artifact_entry_invalid "an entry of ${file} has a drive letter or a backslash" ;;
      *[[:cntrl:]]*) err artifact_entry_invalid "an entry of ${file} has a control character" ;;
    esac
  done <<EOF
${names}
EOF
  while IFS= read -r line; do
    # the first column is the mode string: "-" a regular file, "d" a directory; l, h, c, b and p are links and special files
    [[ "${line}" =~ ^[-d][-rwxsStT]{9} ]] || err artifact_entry_invalid "${file} holds an entry that is not a regular file or a directory"
  done <<EOF
${types}
EOF
  mkdir -p "${into}"
  tar -xpf "${file}" -C "${into}" --no-same-owner || err artifact_entry_invalid "${file} could not be unpacked"
  info "unpacked ${file} into ${into}"
}

[ "$#" -ge 1 ] || usage_err "${USAGE}"
cmd="$1"
shift
case "${cmd}" in
  pack) pack "$@" ;;
  unpack) unpack "$@" ;;
  -h|--help|help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
  *) usage_err "${USAGE}" ;;
esac
