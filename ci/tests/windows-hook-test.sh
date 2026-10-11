#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intended (code for python)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the Windows stages, the signing hook and the acceptance check of the release build: deploy/release/windows-signed.sh,
# ci/release/windows-sign-hook.sh, ci/release/authenticode.py and ci/release/dist-check.py. A scratch git repository holds copies
# of the scripts; go-winres is a stub, the Windows binary is a tiny program built with GOOS=windows, and the signatures come from
# throwaway test CAs (ci/release/authenticode-test-ca.sh) and osslsigncode at the pinned version:
#  - stages: unset, invalid and off-for-releases fail closed; the before hook has to have run; staging directories inside the work
#    tree have to be ignored by git; version resources are passed to go-winres and have to be ignored
#  - hook: the modes, the refusal of test signing on tags and in release.yml, the input checks, the shape of signer.json
#  - acceptance (stage apply): a correct copy passes; a changed byte in the rebuilt file, a signature over other content, a foreign
#    CA, a missing or foreign timestamp, a wrong subject, a weak digest, an unsigned file that was changed and a changed script text
#    fail with their error token; the trust anchors can be replaced only in snapshot builds
#  - dist-check on a synthetic dist/
# Needs go, python3, OpenSSL 3 and osslsigncode in TOOLS_BIN (ci/install-tools.sh osslsigncode). Without them the test fails when
# CI=true; locally the cases that need the missing tool are reported as skipped.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

# ---- prerequisites -------------------------------------------------------------------------------------------------------------
OSSL="${TOOLS_BIN}/osslsigncode"
missing=()
have go || missing+=(go)
have python3 || missing+=(python3)
case "$(openssl version 2>/dev/null)" in "OpenSSL 3"*) ;; *) missing+=("openssl 3") ;; esac
have_ossl=1
[ -x "${OSSL}" ] && [ "$("${OSSL}" --version 2>/dev/null | head -n 1)" = "osslsigncode ${OSSLSIGNCODE_VERSION}, using:" ] || have_ossl=0
if [ "${#missing[@]}" -gt 0 ] || [ "${have_ossl}" = 0 ]; then
  what="${missing[*]:-}"; [ "${have_ossl}" = 1 ] || what="${what:+${what} }osslsigncode ${OSSLSIGNCODE_VERSION}"
  if [ "${CI:-}" = true ]; then fail "${what} missing: ci/install-tools.sh osslsigncode"; finish_tests "windows-hook-test"; exit $?; fi
  if [ "${#missing[@]}" -gt 0 ]; then echo "SKIPPED all cases: ${what} missing"; exit 0; fi
fi
SKIPPED=0
# cases that need osslsigncode (the argument is their number): counted as skipped when the tool is missing
needs_ossl() { [ "${have_ossl}" = 1 ] || { SKIPPED=$((SKIPPED + ${1:-1})); return 1; }; }

# ---- the scratch repository and its tools ----------------------------------------------------------------------------------------
S="${TEST_TMP}/repo"; TB="${TEST_TMP}/tools"; T="${TEST_TMP}/ca"; U="${TEST_TMP}/unsigned"; ST="${TEST_TMP}/state"
VER=0.1.0-rc.1
WSH="${S}/deploy/release/windows-signed.sh"
HOOK="${S}/ci/release/windows-sign-hook.sh"
AUTHPY="${S}/ci/release/authenticode.py"
ID_ARCHES=("myriarch-updater amd64" "myriarch-updater arm64" "myrctl amd64" "myrctl arm64")
export WINRES_LOG="${TEST_TMP}/winres.log"
mkdir -p "${S}/ci/release" "${S}/deploy/release/authenticode" "${S}/deploy/scripts" "${S}/deploy/windows" "${S}/cmd/myriarch-updater" "${S}/cmd/myrctl" "${TB}" "${T}"
cp "${CI_DIR}/lib.sh" "${CI_DIR}/versions.env" "${S}/ci/"
cp "${CI_DIR}/release/authenticode.py" "${CI_DIR}/release/dist-check.py" "${CI_DIR}/release/windows-sign-hook.sh" "${CI_DIR}/release/authenticode-test-ca.sh" "${S}/ci/release/"
cp "${ROOT}/deploy/release/windows-signed.sh" "${S}/deploy/release/"
echo "# Myriarch authenticode directory" > "${S}/deploy/release/authenticode/README.md"
printf '# installer\nWrite-Output "install"\n' > "${S}/deploy/scripts/install-windows.ps1"
printf '# service environment\nWrite-Output "service"\n' > "${S}/deploy/windows/Set-ServiceEnvironment.ps1"
: > "${S}/cmd/myriarch-updater/main.go"; : > "${S}/cmd/myrctl/main.go"
printf '/.cache/\n/dist/\n/cmd/myriarch-updater/rsrc_windows_*.syso\n/cmd/myrctl/rsrc_windows_*.syso\n' > "${S}/.gitignore"
chmod +x "${S}"/ci/release/*.sh "${S}"/ci/release/*.py "${S}/deploy/release/windows-signed.sh"
git init -q -b main "${S}"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
git -C "${S}" add -A && git -C "${S}" commit -q -m scratch

# tools directory of the scratch repository: a go-winres stub (records its arguments) and the real osslsigncode
cat > "${TB}/go-winres" <<'STUB'
#!/usr/bin/env bash
printf 'cwd=%s args=%s\n' "$(pwd)" "$*" >> "${WINRES_LOG}"
out=""; while [ "$#" -gt 0 ]; do case "$1" in --out) out="$2"; shift ;; esac; shift; done
: > "${out}_windows_amd64.syso"; : > "${out}_windows_arm64.syso"
STUB
chmod +x "${TB}/go-winres"
[ "${have_ossl}" = 1 ] && ln -s "${OSSL}" "${TB}/osslsigncode"

# a tiny Windows program (the real programs are far too big for a test)
mkdir -p "${TEST_TMP}/hello"
printf 'module example.test/hello\n\ngo 1.21\n' > "${TEST_TMP}/hello/go.mod"
printf 'package main\n\nimport "fmt"\n\nfunc main() { fmt.Println("hello") }\n' > "${TEST_TMP}/hello/main.go"
( cd "${TEST_TMP}/hello" && env -u GOFLAGS CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOTOOLCHAIN=local go build -trimpath -o "${TEST_TMP}/hello.exe" . ) \
  || { fail "go build of the test program failed"; finish_tests "windows-hook-test"; exit $?; }

# runs a command in the scratch repository with the stage variables cleared; leading VAR=value arguments set variables
ws() {
  ( cd "${S}" && env -u MYRIARCH_WINDOWS_STAGE -u MYRIARCH_AUTHENTICODE_DIR -u WINDOWS_SIGNING -u MYRIARCH_WINDOWS_SIGNING -u WINDOWS_SIGNING_TEST_DIR \
      -u GITHUB_REF -u GITHUB_REF_TYPE -u GITHUB_WORKFLOW_REF \
      TOOLS_BIN="${TB}" MYRIARCH_WINDOWS_UNSIGNED_DIR="${U}" MYRIARCH_WINDOWS_SIGNED_DIR="${TEST_TMP}/signed-default" "$@" )
}
fresh_dist() { # a dist/ with the test program under the four names
  rm -rf "${S}/dist"
  local ia id arch
  for ia in "${ID_ARCHES[@]}"; do
    id="${ia% *}"; arch="${ia#* }"
    mkdir -p "${S}/dist/${id}_windows_${arch}_v1"; cp "${TEST_TMP}/hello.exe" "${S}/dist/${id}_windows_${arch}_v1/${id}.exe"
  done
}
build_all() { # env-assignments... - the post-build hook for the four files (snapshot true unless SNAP is set)
  local ia id arch
  for ia in "${ID_ARCHES[@]}"; do
    id="${ia% *}"; arch="${ia#* }"
    ws "$@" bash "${WSH}" build "${id}" "dist/${id}_windows_${arch}_v1/${id}.exe" windows "${arch}" "${SNAP:-true}" "${VER}" || return 1
  done
}
make_unsigned() { # prepare + build + seal in stage collect
  rm -rf "${U}" "${S}/.cache"; fresh_dist
  ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" prepare "${VER}" true >/dev/null 2>&1 || return 1
  build_all MYRIARCH_WINDOWS_STAGE=collect >/dev/null 2>&1 || return 1
  ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" seal >/dev/null 2>&1
}
hook_in() { # in-dir out-dir [env-assignments...]
  local in="$1" out="$2"; shift 2
  ws "$@" bash "${HOOK}" --in "${in}" --out "${out}"
}

# ---- 1-4: stages fail closed -----------------------------------------------------------------------------------------------------
expect_fail "stage unset (prepare)"                    "\\[stage_missing\\]" ws bash "${WSH}" prepare "${VER}" true
expect_fail "stage unset (build for a Linux target)"   "\\[stage_missing\\]" ws bash "${WSH}" build myrctl dist/x/myrctl linux amd64 true "${VER}"
expect_fail "stage unset (seal)"                       "\\[stage_missing\\]" ws bash "${WSH}" seal
expect_fail "stage unset (check-dist)"                 "\\[stage_missing\\]" ws bash "${WSH}" check-dist
expect_fail "stage bogus"                              "\\[stage_invalid\\] MYRIARCH_WINDOWS_STAGE=sign " ws MYRIARCH_WINDOWS_STAGE=sign bash "${WSH}" prepare "${VER}" true
expect_fail "stage off with is-snapshot=false"         "\\[stage_off_release\\]" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" prepare "${VER}" false
expect_fail "a version that is not a version"          "\\[version_invalid\\]" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" prepare latest true
rm -rf "${S}/.cache"
expect_fail "build without the before hook"            "\\[stage_marker\\]" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myrctl dist/x/myrctl.exe windows amd64 true "${VER}"

# ---- 5: prepare in stage off ------------------------------------------------------------------------------------------------------
: > "${WINRES_LOG}"
expect_ok "prepare in stage off" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" prepare "${VER}" true
for want in "--arch amd64,arm64" "--manifest none" "--product-name Myriarch" "--product-version ${VER}" "--file-version ${VER}" \
            "--original-filename myriarch-updater.exe" "--original-filename myrctl.exe" "--copyright Copyright The Myriarch Authors"; do
  grep -qF -- "${want}" "${WINRES_LOG}" && ok "go-winres gets ${want}" || fail "go-winres did not get ${want}"
done
grep -q "cwd=.*/.cache/release/windows/winres " "${WINRES_LOG}" && ok "go-winres runs in an empty directory below the state" || fail "go-winres cwd: $(head -n 1 "${WINRES_LOG}")"
python3 -I - "${S}" <<'PY' && ok "the staged scripts equal the repository files (mode 0644)" || fail "the staged scripts differ from the repository files"
import os, stat, sys
s = sys.argv[1]
for rel, staged in (("deploy/scripts/install-windows.ps1", ".cache/release/windows/archive/windows/scripts/install-windows.ps1"),
                    ("deploy/windows/Set-ServiceEnvironment.ps1", ".cache/release/windows/archive/windows/windows/Set-ServiceEnvironment.ps1")):
    assert open(os.path.join(s, rel), "rb").read() == open(os.path.join(s, staged), "rb").read()
    assert stat.S_IMODE(os.stat(os.path.join(s, staged)).st_mode) == 0o644
PY

# ---- 6: a version resource that git does not ignore -------------------------------------------------------------------------------
cp "${S}/.gitignore" "${TEST_TMP}/gitignore.orig"
printf '/.cache/\n/dist/\n' > "${S}/.gitignore"; git -C "${S}" commit -q -am "stop ignoring the syso files"
expect_fail "a version resource that git does not ignore" "\\[syso_not_ignored\\] cmd/myriarch-updater/rsrc_windows_amd64.syso" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" prepare "${VER}" true
cp "${TEST_TMP}/gitignore.orig" "${S}/.gitignore"; git -C "${S}" commit -q -am "ignore the syso files again"

# ---- 7: stage collect -------------------------------------------------------------------------------------------------------------
expect_ok "collect: prepare, four build hooks and seal" make_unsigned
python3 -I - "${U}" <<'PY' && ok "the unsigned set has seven files and a correct SHA256SUMS" || fail "the unsigned set is wrong"
import hashlib, os, sys
d = sys.argv[1]
names = sorted(os.listdir(d))
assert names == ["SHA256SUMS", "Set-ServiceEnvironment.ps1", "install-windows.ps1", "myrctl_windows_amd64.exe", "myrctl_windows_arm64.exe",
                 "myriarch-updater_windows_amd64.exe", "myriarch-updater_windows_arm64.exe"], names
lines = open(os.path.join(d, "SHA256SUMS")).read().splitlines()
assert [l.split("  ")[1] for l in lines] == sorted(n for n in names if n != "SHA256SUMS")
for l in lines:
    h, n = l.split("  ")
    assert hashlib.sha256(open(os.path.join(d, n), "rb").read()).hexdigest() == h, n
PY
rm -rf "${S}/.cache"; fresh_dist
ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" prepare "${VER}" true >/dev/null 2>&1
rm -rf "${U}"; mkdir -p "${U}"
ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" prepare "${VER}" true >/dev/null 2>&1
ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" build myrctl dist/myrctl_windows_amd64_v1/myrctl.exe windows amd64 true "${VER}" >/dev/null 2>&1
expect_fail "seal with three of four files"            "\\[set_incomplete\\]" ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" seal
expect_fail "the same file collected twice"            "\\[duplicate\\]" ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" build myrctl dist/myrctl_windows_amd64_v1/myrctl.exe windows amd64 true "${VER}"
expect_fail "prepare with a non-empty unsigned directory" "\\[staging_not_empty\\]" ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" prepare "${VER}" true
expect_fail "build with another version than the marker" "\\[stage_marker\\]" ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" build myrctl dist/myrctl_windows_arm64_v1/myrctl.exe windows arm64 true 9.9.9
expect_fail "build in another stage than the marker"   "\\[stage_marker\\]" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myrctl dist/myrctl_windows_arm64_v1/myrctl.exe windows arm64 true "${VER}"
expect_fail "an unsigned directory inside the tree that git does not ignore" "\\[staging_dir\\]" \
  ws MYRIARCH_WINDOWS_STAGE=collect MYRIARCH_WINDOWS_UNSIGNED_DIR="${S}/unsigned-here" bash "${WSH}" prepare "${VER}" true
expect_ok "an unsigned directory inside the tree below .cache is fine" \
  ws MYRIARCH_WINDOWS_STAGE=collect MYRIARCH_WINDOWS_UNSIGNED_DIR="${S}/.cache/elsewhere" bash "${WSH}" prepare "${VER}" true

# ---- 8: the build hook checks its target -------------------------------------------------------------------------------------------
ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" prepare "${VER}" true >/dev/null 2>&1
mkdir -p "${TEST_TMP}/outside"; cp "${TEST_TMP}/hello.exe" "${TEST_TMP}/outside/myrctl.exe"
expect_fail "a path outside dist/"                     "\\[unexpected_target\\]" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myrctl "${TEST_TMP}/outside/myrctl.exe" windows amd64 true "${VER}"
expect_fail "an unknown build id"                      "\\[unexpected_target\\] build id 'myriarch-sim'" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myriarch-sim dist/myrctl_windows_amd64_v1/myrctl.exe windows amd64 true "${VER}"
expect_fail "a file with another name than the id"     "\\[unexpected_target\\]" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myriarch-updater dist/myrctl_windows_amd64_v1/myrctl.exe windows amd64 true "${VER}"
mkdir -p "${S}/dist/sym_windows_amd64_v1"; ln -sf "${TEST_TMP}/outside/myrctl.exe" "${S}/dist/sym_windows_amd64_v1/myrctl.exe"
expect_fail "a build target that is a symbolic link"   "\\[unexpected_target\\] dist/sym_windows_amd64_v1/myrctl.exe is not a regular file" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myrctl dist/sym_windows_amd64_v1/myrctl.exe windows amd64 true "${VER}"
expect_ok "a non-Windows target is ignored"            ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" build myrctl dist/x/myrctl linux amd64 true "${VER}"

# helpers for crafted signed sets and for the acceptance check
fresh_copy() { local n="$1"; cp "${U}/${n}" "${TEST_TMP}/fresh-${n}"; }
# helper: python tool for crafted signed sets
cat > "${TEST_TMP}/tamper.py" <<'PY'
import hashlib, json, os, sys
# tamper.py rewrite DIR UNSIGNED_DIR PROVIDER SIGNED SUBJECT: SHA256SUMS and signer.json that match the files in DIR
# tamper.py flip FILE: changes one byte in the middle
# tamper.py checksum FILE: flips one byte of the PE CheckSum field (e_lfanew + 24 + 64), which the signature does not cover
# tamper.py sigblock FILE MODE: damages the first base64 line of the signature block of a script (space, tab: appended; junk: a
# non-base64 character in the middle; blank: an empty line inserted before it; pad: a line "# AA==" with padding inserted in the middle
# of the stream; crcr: a second CR before the line ending). "tamper.py sigblock FILE lf" is not damage: it converts the block (the begin
# marker to the end of the file) to LF line endings.
def sha(p): return hashlib.sha256(open(p, "rb").read()).hexdigest()
cmd = sys.argv[1]
if cmd == "flip":
  b = bytearray(open(sys.argv[2], "rb").read()); b[len(b) // 2] ^= 1; open(sys.argv[2], "wb").write(bytes(b))
elif cmd == "checksum":
  b = bytearray(open(sys.argv[2], "rb").read()); off = int.from_bytes(b[0x3c:0x40], "little") + 24 + 64; b[off] ^= 0x5a; open(sys.argv[2], "wb").write(bytes(b))
elif cmd == "sigblock":
  raw = open(sys.argv[2], "rb").read(); mode = sys.argv[3]
  i = raw.index(b"# SIG # Begin signature block"); head, block = raw[:i], raw[i:]
  eol = b"\r\n" if b"\r\n" in block else b"\n"
  ls = block.split(eol)
  assert ls[1].startswith(b"# "), ls[1][:20]
  if mode == "space": ls[1] = ls[1] + b" "
  elif mode == "tab": ls[1] = ls[1] + b"\t"
  elif mode == "junk": ls[1] = ls[1][:10] + b"!" + ls[1][11:]
  elif mode == "blank": ls.insert(1, b"")
  elif mode == "pad": ls.insert(2, b"# AA==")
  elif mode == "crcr": ls[1] = ls[1] + b"\r"
  elif mode == "lf": ls = [l[:-1] if l.endswith(b"\r") else l for l in ls]; eol = b"\n"
  else: raise SystemExit("unknown mode " + mode)
  open(sys.argv[2], "wb").write(head + eol.join(ls))
elif cmd == "rewrite":
  d, u, provider, signed, subject = sys.argv[2:7]
  signed = signed == "true"
  usums = dict(l.split("  ")[::-1] for l in open(os.path.join(u, "SHA256SUMS")).read().splitlines())
  names = sorted(usums)
  sums = {n: sha(os.path.join(d, n)) for n in names}
  open(os.path.join(d, "SHA256SUMS"), "w").write("".join("%s  %s\n" % (sums[n], n) for n in names))
  files = {}
  for n in names:
      e = {"sha256": sums[n], "unsigned_sha256": usums[n]}
      if signed: e.update(subject=subject, issuer="issuer", serial="serial", timestamp="timestamp")
      files[n] = e
  doc = {"files": files, "provider": provider, "schema": 1, "signed": signed, "subject": subject if signed else None}
  open(os.path.join(d, "signer.json"), "w").write(json.dumps(doc, indent=2, sort_keys=True) + "\n")
PY
TAMPER() { python3 -I "${TEST_TMP}/tamper.py" "$@"; }
sign_as() { # material in out [tsa 1|0] [hash]
  local m="$1" in="$2" out="$3" tsa="${4:-1}" h="${5:-sha256}"
  local a=(sign -certs "${m}/signing/signer.pem" -key "${m}/signing/signer.key" -h "${h}" -n Myriarch)
  if [ "${tsa}" = 1 ]; then a+=(-TSA-certs "${m}/signing/tsa.pem" -TSA-key "${m}/signing/tsa.key" -TSA-time "$(date +%s)"); fi
  rm -f "${out}"
  "${OSSL}" "${a[@]}" -in "${in}" -out "${out}" > "${TEST_TMP}/sign.log" 2>&1 || { cat "${TEST_TMP}/sign.log" >&2; return 1; }
}
# accept one file: acc <name> <signed-dir> <trust-dir> [extra args]
acc() {
  local name="$1" d="$2" trust="$3"; shift 3
  rm -rf "${ST}"; mkdir -p "${ST}"; fresh_copy "${name}"
  env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" accept-exe --root "${S}" --name "${name}" --fresh "${TEST_TMP}/fresh-${name}" --state "${ST}" \
    --unsigned "${U}" --signed "${d}" --trust "${trust}" "$@"
}

NAME=myrctl_windows_amd64.exe

# ---- 9: the signing hook -----------------------------------------------------------------------------------------------------------
make_unsigned >/dev/null 2>&1 || fail "the unsigned set could not be made"
expect_fail "hook: unknown argument"                   "usage: windows-sign-hook.sh" ws bash "${HOOK}" --in "${U}" --out "${TEST_TMP}/h0" --force
expect_fail "hook: --out is required"                  "usage: windows-sign-hook.sh" ws bash "${HOOK}" --in "${U}"
expect_fail "hook: WINDOWS_SIGNING unset"              "\\[mode_missing\\]" hook_in "${U}" "${TEST_TMP}/h1"
expect_fail "hook: WINDOWS_SIGNING bogus"              "\\[mode_invalid\\]" hook_in "${U}" "${TEST_TMP}/h2" WINDOWS_SIGNING=bogus
expect_fail "hook: signpath is not implemented yet"    "\\[provider_unavailable\\]" hook_in "${U}" "${TEST_TMP}/h3" WINDOWS_SIGNING=signpath
expect_fail "hook: kms is not implemented yet"         "\\[provider_unavailable\\]" hook_in "${U}" "${TEST_TMP}/h3" WINDOWS_SIGNING=kms
expect_fail "hook: test mode on a tag ref"             "\\[test_mode_on_tag\\]" hook_in "${U}" "${TEST_TMP}/h4" WINDOWS_SIGNING=test GITHUB_REF=refs/tags/v1.0.0
expect_fail "hook: test mode with ref type tag"        "\\[test_mode_on_tag\\]" hook_in "${U}" "${TEST_TMP}/h4" WINDOWS_SIGNING=test GITHUB_REF_TYPE=tag
expect_fail "hook: test mode in release.yml"           "\\[test_mode_on_tag\\]" hook_in "${U}" "${TEST_TMP}/h4" WINDOWS_SIGNING=test GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/release.yml@refs/heads/main
expect_fail "hook: test mode without material"         "\\[test_material\\]" hook_in "${U}" "${TEST_TMP}/h4" WINDOWS_SIGNING=test
expect_fail "hook: an output directory in the tree that git does not ignore" "\\[staging_dir\\]" hook_in "${U}" "${S}/out-here" WINDOWS_SIGNING=off
mkdir -p "${TEST_TMP}/h5" && : > "${TEST_TMP}/h5/x"
expect_fail "hook: a non-empty output directory"       "\\[staging_not_empty\\]" hook_in "${U}" "${TEST_TMP}/h5" WINDOWS_SIGNING=off
cp -R "${U}" "${TEST_TMP}/in-extra"; echo x > "${TEST_TMP}/in-extra/extra.txt"
expect_fail "hook: an input with an extra file"        "\\[set_incomplete\\]" hook_in "${TEST_TMP}/in-extra" "${TEST_TMP}/h6" WINDOWS_SIGNING=off
cp -R "${U}" "${TEST_TMP}/in-sums"
python3 -I - "${TEST_TMP}/in-sums/SHA256SUMS" <<'PY'
import sys
p = sys.argv[1]; t = open(p).read()
open(p, "w").write(("0" if t[0] != "0" else "1") + t[1:])
PY
expect_fail "hook: a wrong SHA256SUMS"                 "\\[sums_mismatch\\]" hook_in "${TEST_TMP}/in-sums" "${TEST_TMP}/h7" WINDOWS_SIGNING=off
cp -R "${U}" "${TEST_TMP}/in-link"; rm "${TEST_TMP}/in-link/myrctl_windows_arm64.exe"; ln -s "${U}/myrctl_windows_arm64.exe" "${TEST_TMP}/in-link/myrctl_windows_arm64.exe"
expect_fail "hook: a symbolic link in the input"       "\\[set_incomplete\\].*not a regular file" hook_in "${TEST_TMP}/in-link" "${TEST_TMP}/h8" WINDOWS_SIGNING=off
SG_OFF="${TEST_TMP}/signed-off"
expect_ok "hook: off"                                  hook_in "${U}" "${SG_OFF}" WINDOWS_SIGNING=off
python3 -I - "${U}" "${SG_OFF}" <<'PY' && ok "off: the seven names plus signer.json, equal bytes, signed false" || fail "the output of mode off is wrong"
import json, os, sys
u, o = sys.argv[1:3]
assert sorted(os.listdir(o)) == sorted(os.listdir(u) + ["signer.json"]), sorted(os.listdir(o))
for n in os.listdir(u):
    assert open(os.path.join(u, n), "rb").read() == open(os.path.join(o, n), "rb").read(), n
d = json.load(open(os.path.join(o, "signer.json")))
assert d["provider"] == "off" and d["signed"] is False and d["subject"] is None and d["schema"] == 1
assert all(set(v) == {"sha256", "unsigned_sha256"} and v["sha256"] == v["unsigned_sha256"] for v in d["files"].values())
assert open(os.path.join(o, "signer.json")).read() == json.dumps(d, indent=2, sort_keys=True) + "\n"
PY

if needs_ossl 30; then
  # ---- throwaway CAs ---------------------------------------------------------------------------------------------------------------
  CA="${S}/ci/release/authenticode-test-ca.sh"
  expect_fail "test CA: a directory inside the repository is refused" "\\[inside_repository\\]" bash "${CA}" init "${S}/ca-here"
  bash "${CA}" init "${T}/ca" >/dev/null 2>&1 && bash "${CA}" init "${T}/foreign" >/dev/null 2>&1 \
    && bash "${CA}" leaf "${T}/ca" "Someone Else" "${T}/wrong" >/dev/null 2>&1 || fail "the test CAs could not be made"
  mkdir -p "${T}/foreign-tsa/signing" "${T}/foreign-tsa/trust"
  cp "${T}/ca/signing/signer.pem" "${T}/ca/signing/signer.key" "${T}/foreign/signing/tsa.pem" "${T}/foreign/signing/tsa.key" "${T}/foreign-tsa/signing/"
  cp "${T}/ca/trust/"* "${T}/foreign-tsa/trust/"
  [ "$(cat "${T}/ca/trust/subject.txt")" = "CN=Myriarch Test Signer,O=Myriarch Test" ] && ok "the test CA writes the RFC 2253 subject" || fail "subject.txt: $(cat "${T}/ca/trust/subject.txt")"

  SG_TEST="${TEST_TMP}/signed-test"
  expect_ok "hook: test" hook_in "${U}" "${SG_TEST}" WINDOWS_SIGNING=test WINDOWS_SIGNING_TEST_DIR="${T}/ca"
  python3 -I - "${SG_TEST}" <<'PY' && ok "test: signer.json says signed, with the subject and the signer fields" || fail "signer.json of mode test is wrong"
import json, os, sys
d = json.load(open(os.path.join(sys.argv[1], "signer.json")))
assert d["provider"] == "test" and d["signed"] is True and d["subject"] == "CN=Myriarch Test Signer,O=Myriarch Test"
for n, v in d["files"].items():
    assert set(v) == {"sha256", "unsigned_sha256", "subject", "issuer", "serial", "timestamp"}, n
    assert v["sha256"] != v["unsigned_sha256"] and v["subject"] == d["subject"] and v["timestamp"], n
PY

  # ---- 10: stage apply with an unsigned set ----------------------------------------------------------------------------------------
  apply_flow() { # signed-dir snapshot [env-assignments...]
    local sg="$1" snap="$2"; shift 2
    fresh_dist
    ws MYRIARCH_WINDOWS_STAGE=apply MYRIARCH_WINDOWS_SIGNED_DIR="${sg}" "$@" bash "${WSH}" prepare "${VER}" "${snap}" || return 1
    SNAP="${snap}" build_all MYRIARCH_WINDOWS_STAGE=apply MYRIARCH_WINDOWS_SIGNED_DIR="${sg}" "$@"
  }
  before="$(sha256_of "${TEST_TMP}/hello.exe")"
  expect_ok "apply with the output of mode off (snapshot)" apply_flow "${SG_OFF}" true
  [ "$(sha256_of "${S}/dist/myrctl_windows_amd64_v1/myrctl.exe")" = "${before}" ] && ok "... the file is unchanged" || fail "an unsigned file was changed"
  expect_ok "apply with the output of mode off (release)" apply_flow "${SG_OFF}" false MYRIARCH_WINDOWS_SIGNING=off
  cmp -s "${S}/.cache/release/windows/archive/windows/scripts/install-windows.ps1" "${S}/deploy/scripts/install-windows.ps1" \
    && ok "... the staged script is the repository file" || fail "the staged script differs from the repository file"
  expect_fail "apply: a release build without the configured mode" "\\[mode_missing\\]" apply_flow "${SG_OFF}" false
  expect_fail "apply: the configured mode is signpath, the hook returned unsigned files" "\\[provider_mismatch\\]" apply_flow "${SG_OFF}" false MYRIARCH_WINDOWS_SIGNING=signpath

  # ---- 11: stage apply with test signatures -----------------------------------------------------------------------------------------
  expect_ok "apply with test signatures and the test trust anchors (snapshot)" apply_flow "${SG_TEST}" true MYRIARCH_AUTHENTICODE_DIR="${T}/ca/trust"
  expect_ok "... the accepted files verify" env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" \
    "${S}/dist/myrctl_windows_amd64_v1/myrctl.exe" "${S}/dist/myriarch-updater_windows_arm64_v1/myriarch-updater.exe" \
    "${S}/.cache/release/windows/archive/windows/scripts/install-windows.ps1" "${S}/.cache/release/windows/archive/windows/windows/Set-ServiceEnvironment.ps1"
  [ "$(sha256_of "${S}/dist/myrctl_windows_amd64_v1/myrctl.exe")" != "${before}" ] && ok "... the rebuilt file now carries the signature" || fail "the file was not signed"
  python3 -I - "${S}" <<'PY' && ok "... the text of the staged scripts before the signature block is the repository file" || fail "a staged script differs before its signature block"
import os, sys
s = sys.argv[1]
for rel, staged in (("deploy/scripts/install-windows.ps1", ".cache/release/windows/archive/windows/scripts/install-windows.ps1"),
                    ("deploy/windows/Set-ServiceEnvironment.ps1", ".cache/release/windows/archive/windows/windows/Set-ServiceEnvironment.ps1")):
    repo = open(os.path.join(s, rel), "rb").read(); got = open(os.path.join(s, staged), "rb").read()
    assert got.startswith(repo) and b"# SIG # Begin signature block" in got
PY
  # the dist checks of this flow's accepted records are exercised below (22)

  # ---- 12: the trust anchors and the provider --------------------------------------------------------------------------------------
  expect_fail "apply: a release build cannot replace the trust anchors" "\\[trust_override\\]" apply_flow "${SG_TEST}" false MYRIARCH_AUTHENTICODE_DIR="${T}/ca/trust"
  expect_fail "apply: signed files without committed trust anchors" "\\[trust_missing\\]" apply_flow "${SG_TEST}" true
  expect_fail "apply: a signer.json with provider test is refused in a release build" "\\[provider_test_release\\]" acc "${NAME}" "${SG_TEST}" "${T}/ca/trust" --release
  expect_ok "accept: provider test is fine in a snapshot" acc "${NAME}" "${SG_TEST}" "${T}/ca/trust"

  # ---- 13: the rebuilt file differs --------------------------------------------------------------------------------------------------
  flip_flow() {
    fresh_dist
    ws MYRIARCH_WINDOWS_STAGE=apply MYRIARCH_WINDOWS_SIGNED_DIR="${SG_TEST}" MYRIARCH_AUTHENTICODE_DIR="${T}/ca/trust" bash "${WSH}" prepare "${VER}" true || return 1
    TAMPER flip "${S}/dist/myrctl_windows_amd64_v1/myrctl.exe"
    ws MYRIARCH_WINDOWS_STAGE=apply MYRIARCH_WINDOWS_SIGNED_DIR="${SG_TEST}" MYRIARCH_AUTHENTICODE_DIR="${T}/ca/trust" \
      bash "${WSH}" build myrctl dist/myrctl_windows_amd64_v1/myrctl.exe windows amd64 true "${VER}"
  }
  expect_fail "apply: a rebuilt file that differs from the unsigned build" "\\[not_reproducible\\] the rebuilt myrctl_windows_amd64.exe" flip_flow

  # ---- 14-19: the signature is not acceptable ---------------------------------------------------------------------------------------
  craft() { # dir material tsa hash subject-material-for-json [source-file] - replaces ${NAME} in a copy of the test set by a signing of the source
    local d="$1" m="$2" tsa="$3" h="$4" src="${5:-${U}/${NAME}}"
    rm -rf "${d}"; cp -R "${SG_TEST}" "${d}"
    sign_as "${m}" "${src}" "${d}/${NAME}" "${tsa}" "${h}" || return 1
    TAMPER rewrite "${d}" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  }
  cp "${U}/${NAME}" "${TEST_TMP}/flipped.exe"; TAMPER flip "${TEST_TMP}/flipped.exe"
  craft "${TEST_TMP}/c-other" "${T}/ca" 1 sha256 "${TEST_TMP}/flipped.exe" && expect_fail "the provider signed other content" "\\[signature_mismatch\\].*${NAME}" acc "${NAME}" "${TEST_TMP}/c-other" "${T}/ca/trust"
  craft "${TEST_TMP}/c-foreign" "${T}/foreign" 1 sha256 && expect_fail "a signature of a foreign CA" "\\[signature_invalid\\]" acc "${NAME}" "${TEST_TMP}/c-foreign" "${T}/ca/trust"
  craft "${TEST_TMP}/c-nots" "${T}/ca" 0 sha256 && expect_fail "a signature without a timestamp" "\\[timestamp_missing\\]" acc "${NAME}" "${TEST_TMP}/c-nots" "${T}/ca/trust"
  craft "${TEST_TMP}/c-fts" "${T}/foreign-tsa" 1 sha256 && expect_fail "a timestamp of a foreign authority" "\\[timestamp_invalid\\]" acc "${NAME}" "${TEST_TMP}/c-fts" "${T}/ca/trust"
  craft "${TEST_TMP}/c-wrong" "${T}/wrong" 1 sha256 && expect_fail "a signature with another subject" "\\[subject_mismatch\\].*Someone Else" acc "${NAME}" "${TEST_TMP}/c-wrong" "${T}/ca/trust"
  craft "${TEST_TMP}/c-sha1" "${T}/ca" 1 sha1 && expect_fail "a SHA-1 signature" "\\[digest_weak\\]" acc "${NAME}" "${TEST_TMP}/c-sha1" "${T}/ca/trust"
  rm -rf "${TEST_TMP}/c-plain"; cp -R "${SG_TEST}" "${TEST_TMP}/c-plain"; cp "${U}/${NAME}" "${TEST_TMP}/c-plain/${NAME}"
  TAMPER rewrite "${TEST_TMP}/c-plain" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  expect_fail "an unsigned file in a signed set" "\\[signature_unreadable\\]" acc "${NAME}" "${TEST_TMP}/c-plain" "${T}/ca/trust"
  expect_ok "a correct signed copy is accepted again (control)" acc "${NAME}" "${SG_TEST}" "${T}/ca/trust"
  # the release ships the job's own build with the provider's signature attached, never the provider's file: a provider file that
  # differs in a region that no signature covers (the PE CheckSum field) still verifies, but must not be what is staged
  reattach() { # provider-file unsigned-file out - the signature of the first attached to a copy of the second
    local w="${TEST_TMP}/reattach"; rm -rf "${w}"; mkdir -p "${w}"
    rm -f "$3"
    { "${OSSL}" extract-signature -in "$1" -out "${w}/sig.p7" > "${w}/log" 2>&1 && "${OSSL}" attach-signature -sigin "${w}/sig.p7" -in "$2" -out "$3" >> "${w}/log" 2>&1; } \
      || { cat "${w}/log" >&2; return 1; }
  }
  rm -rf "${TEST_TMP}/c-cks"; cp -R "${SG_TEST}" "${TEST_TMP}/c-cks"; TAMPER checksum "${TEST_TMP}/c-cks/${NAME}"
  TAMPER rewrite "${TEST_TMP}/c-cks" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  expect_ok "the provider file with another PE checksum still verifies" env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" "${TEST_TMP}/c-cks/${NAME}"
  [ "$(sha256_of "${TEST_TMP}/c-cks/${NAME}")" != "$(sha256_of "${SG_TEST}/${NAME}")" ] && ok "... and differs from the correct provider file" || fail "the PE checksum was not changed"
  expect_ok "accept: a provider file with another PE checksum" acc "${NAME}" "${TEST_TMP}/c-cks" "${T}/ca/trust"
  reattach "${TEST_TMP}/c-cks/${NAME}" "${U}/${NAME}" "${TEST_TMP}/cks-want" || fail "the signature could not be re-attached"
  [ "$(sha256_of "${TEST_TMP}/fresh-${NAME}")" = "$(sha256_of "${TEST_TMP}/cks-want")" ] \
    && ok "... the staged file is the signature attached to the job's own build" || fail "the staged file is not the re-attach output"
  [ "$(sha256_of "${TEST_TMP}/fresh-${NAME}")" != "$(sha256_of "${TEST_TMP}/c-cks/${NAME}")" ] \
    && ok "... and not the provider's file" || fail "the provider's file was staged"
  # exactly one signature (a nested signature over a signed file is a second one)
  rm -rf "${TEST_TMP}/c-nest"; cp -R "${SG_TEST}" "${TEST_TMP}/c-nest"; rm "${TEST_TMP}/c-nest/${NAME}"
  "${OSSL}" sign -nest -certs "${T}/ca/signing/signer.pem" -key "${T}/ca/signing/signer.key" -h sha256 -n Myriarch \
    -TSA-certs "${T}/ca/signing/tsa.pem" -TSA-key "${T}/ca/signing/tsa.key" -TSA-time "$(date +%s)" \
    -in "${SG_TEST}/${NAME}" -out "${TEST_TMP}/c-nest/${NAME}" > "${TEST_TMP}/sign.log" 2>&1 || cat "${TEST_TMP}/sign.log" >&2
  TAMPER rewrite "${TEST_TMP}/c-nest" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  expect_fail "a file with two signatures" "\\[signature_count\\]" env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" "${TEST_TMP}/c-nest/${NAME}"
  rm -rf "${TEST_TMP}/c-sj"; cp -R "${SG_TEST}" "${TEST_TMP}/c-sj"; TAMPER rewrite "${TEST_TMP}/c-sj" "${U}" test true "CN=Someone Else"
  expect_fail "a signer.json with another subject than the trust anchor" "\\[signer_json\\] subject" acc "${NAME}" "${TEST_TMP}/c-sj" "${T}/ca/trust"
fi

# ---- 20: the unsigned path ---------------------------------------------------------------------------------------------------------
rm -rf "${TEST_TMP}/u-changed"; cp -R "${SG_OFF}" "${TEST_TMP}/u-changed"
TAMPER flip "${TEST_TMP}/u-changed/${NAME}"; TAMPER rewrite "${TEST_TMP}/u-changed" "${U}" off false ""
expect_fail "unsigned: a file that differs from the unsigned build" "\\[unsigned_changed\\]" acc "${NAME}" "${TEST_TMP}/u-changed" "${S}/deploy/release/authenticode"
python3 -I - "${SG_OFF}" "${TEST_TMP}" <<'PY'
import json, os, shutil, sys
src, tmp = sys.argv[1:3]
def variant(name, mutate):
    d = os.path.join(tmp, name); shutil.rmtree(d, ignore_errors=True); shutil.copytree(src, d)
    p = os.path.join(d, "signer.json"); doc = json.load(open(p)); mutate(doc); open(p, "w").write(json.dumps(doc, indent=2, sort_keys=True) + "\n")
def provider(doc): doc["provider"] = "test"
def sha(doc): doc["files"]["myrctl_windows_amd64.exe"]["sha256"] = "0" * 64
def subj(doc): doc["subject"] = "CN=Someone"
def extra(doc): doc["note"] = "x"
for n, f in (("sj-provider", provider), ("sj-sha", sha), ("sj-subject", subj), ("sj-extra", extra)): variant(n, f)
PY
expect_fail "signer.json: signed false with provider test"  "\\[signer_json\\]" acc "${NAME}" "${TEST_TMP}/sj-provider" "${S}/deploy/release/authenticode"
expect_fail "signer.json: a wrong sha256"                    "\\[signer_json\\] files.${NAME}.sha256" acc "${NAME}" "${TEST_TMP}/sj-sha" "${S}/deploy/release/authenticode"
expect_fail "signer.json: a subject although nothing is signed" "\\[signer_json\\]" acc "${NAME}" "${TEST_TMP}/sj-subject" "${S}/deploy/release/authenticode"
expect_fail "signer.json: an unknown key"                    "\\[signer_json\\]" acc "${NAME}" "${TEST_TMP}/sj-extra" "${S}/deploy/release/authenticode"
expect_ok "unsigned: the unchanged copy is accepted"         acc "${NAME}" "${SG_OFF}" "${S}/deploy/release/authenticode"
cp "${TEST_TMP}/hello.exe" "${TEST_TMP}/not-the-build.exe"; TAMPER flip "${TEST_TMP}/not-the-build.exe"
expect_fail "unsigned: a rebuilt file that differs (accept-exe directly)" "\\[not_reproducible\\]" env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" accept-exe --root "${S}" --name "${NAME}" \
  --fresh "${TEST_TMP}/not-the-build.exe" --state "${ST}" --unsigned "${U}" --signed "${SG_OFF}" --trust "${S}/deploy/release/authenticode"
# the binding of the signing mode: a release build has to know the configured mode and signer.json has to name the same provider
AUTHDIR="${S}/deploy/release/authenticode"
expect_fail "mode: a release build without MYRIARCH_WINDOWS_SIGNING" "\\[mode_missing\\] MYRIARCH_WINDOWS_SIGNING is not set" acc "${NAME}" "${SG_OFF}" "${AUTHDIR}" --release
expect_fail "mode: the hook returned off, the configuration says signpath" "\\[provider_mismatch\\].*'off'.*'signpath'" acc "${NAME}" "${SG_OFF}" "${AUTHDIR}" --release --expect-provider signpath
expect_fail "mode: the hook returned off, the configuration says kms (snapshot)" "\\[provider_mismatch\\]" acc "${NAME}" "${SG_OFF}" "${AUTHDIR}" --expect-provider kms
expect_fail "mode: a configured mode that does not exist" "\\[mode_invalid\\] MYRIARCH_WINDOWS_SIGNING=bogus" acc "${NAME}" "${SG_OFF}" "${AUTHDIR}" --release --expect-provider bogus
expect_ok "mode: off and off in a release build" acc "${NAME}" "${SG_OFF}" "${AUTHDIR}" --release --expect-provider off

# stage collect: seal compares the files with the record of the build hook
rm -rf "${S}/.cache" "${U}"; fresh_dist
ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" prepare "${VER}" true >/dev/null 2>&1
build_all MYRIARCH_WINDOWS_STAGE=collect >/dev/null 2>&1
TAMPER flip "${U}/${NAME}"
expect_fail "seal: an .exe that changed after the build hook" "\\[not_reproducible\\] ${NAME} differs from the build recorded" ws MYRIARCH_WINDOWS_STAGE=collect bash "${WSH}" seal
make_unsigned >/dev/null 2>&1 || fail "the unsigned set could not be made again"

# ---- 21: the scripts in stage apply -----------------------------------------------------------------------------------------------
if needs_ossl 15; then
  accs() { # signed-dir trust-dir [extra]
    local d="$1" trust="$2"; shift 2
    rm -rf "${ST}"; mkdir -p "${ST}"
    env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" accept-scripts --root "${S}" --state "${ST}" --unsigned "${U}" --signed "${d}" --trust "${trust}" "$@"
  }
  # a restrictive umask must not leak into the staged scripts: the mode is set explicitly, like in the stage off staging
  accs_umask() { ( umask 077; accs "$@" ); }
  expect_ok "scripts: correct test signatures are accepted" accs_umask "${SG_TEST}" "${T}/ca/trust"
  python3 -I - "${ST}" <<'PY' && ok "... the staged scripts have mode 0644 under a restrictive umask" || fail "... the staged scripts do not have mode 0644"
import os, stat, sys
for rel in ("scripts/install-windows.ps1", "windows/Set-ServiceEnvironment.ps1"):
    assert stat.S_IMODE(os.stat(os.path.join(sys.argv[1], "archive", "windows", rel)).st_mode) == 0o644, rel
PY
  expect_ok "... the staged scripts verify" env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" \
    "${ST}/archive/windows/scripts/install-windows.ps1" "${ST}/archive/windows/windows/Set-ServiceEnvironment.ps1"
  cp "${S}/deploy/scripts/install-windows.ps1" "${TEST_TMP}/changed.ps1"; printf '# one more line\n' >> "${TEST_TMP}/changed.ps1"
  rm -rf "${TEST_TMP}/s-changed"; cp -R "${SG_TEST}" "${TEST_TMP}/s-changed"
  sign_as "${T}/ca" "${TEST_TMP}/changed.ps1" "${TEST_TMP}/s-changed/install-windows.ps1" 1 sha256 && TAMPER rewrite "${TEST_TMP}/s-changed" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  expect_fail "scripts: signed text that differs from the repository file" "\\[script_changed\\] install-windows.ps1" accs "${TEST_TMP}/s-changed" "${T}/ca/trust"
  rm -rf "${TEST_TMP}/s-foreign"; cp -R "${SG_TEST}" "${TEST_TMP}/s-foreign"
  sign_as "${T}/foreign" "${S}/deploy/scripts/install-windows.ps1" "${TEST_TMP}/s-foreign/install-windows.ps1" 1 sha256 && TAMPER rewrite "${TEST_TMP}/s-foreign" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  expect_fail "scripts: signed by a foreign CA" "\\[signature_invalid\\]" accs "${TEST_TMP}/s-foreign" "${T}/ca/trust"
  # The signature block has a defined format (ci/release/authenticode.py check_script_block): "# " and base64 characters per line, CRLF or
  # LF. Every damage is refused before osslsigncode sees the file, so that the OpenSSL base64 decoder of the build host cannot change the
  # outcome. The block osslsigncode writes (CRLF) is the case "correct test signatures are accepted" above. The LF variant is accepted
  # end to end below (accept-scripts extracts the signature and attaches it to the repository file, which does not depend on the line
  # endings of the block); only a direct "verify" of the provider's LF file fails, because osslsigncode takes the line ending of the
  # signed text from the begin marker line, which makes the digest differ. That path is not part of the release flow.
  expect_ok "scripts: the block format accepts CRLF and LF lines, with the end marker at the end of the file or followed by a line ending" \
    python3 -I -c 'import sys; sys.path.insert(0, sys.argv[1]); import authenticode as a
b = b"# SIG # Begin signature block\n# MIIabc+/0\n# AAAA==\n# SIG # End signature block"
for t in (b, b + b"\n", b.replace(b"\n", b"\r\n") + b"\r\n"):
    assert a.check_script_block("x.ps1", b"text\n" + t) == 5' "$(dirname "${AUTHPY}")"
  rm -rf "${TEST_TMP}/s-lf"; cp -R "${SG_TEST}" "${TEST_TMP}/s-lf"; TAMPER sigblock "${TEST_TMP}/s-lf/install-windows.ps1" lf
  TAMPER rewrite "${TEST_TMP}/s-lf" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
  grep -q "$(printf '\r')" <(sed -n '/# SIG # Begin signature block/,$p' "${TEST_TMP}/s-lf/install-windows.ps1") && fail "the LF block still holds a CR" || ok "scripts: the provider file with an LF signature block is prepared"
  expect_ok "scripts: a provider file with an LF signature block is accepted" accs "${TEST_TMP}/s-lf" "${T}/ca/trust"
  reattach "${TEST_TMP}/s-lf/install-windows.ps1" "${S}/deploy/scripts/install-windows.ps1" "${TEST_TMP}/lf-want.ps1" || fail "the signature could not be re-attached"
  [ "$(sha256_of "${ST}/archive/windows/scripts/install-windows.ps1")" = "$(sha256_of "${TEST_TMP}/lf-want.ps1")" ] \
    && ok "... the staged script is the signature attached to the repository file" || fail "the staged script is not the re-attach output"
  [ "$(sha256_of "${ST}/archive/windows/scripts/install-windows.ps1")" != "$(sha256_of "${TEST_TMP}/s-lf/install-windows.ps1")" ] \
    && ok "... and not the provider's file" || fail "the provider's script was staged"
  for dmg in space tab junk blank pad crcr; do
    rm -rf "${TEST_TMP}/s-${dmg}"; cp -R "${SG_TEST}" "${TEST_TMP}/s-${dmg}"; TAMPER sigblock "${TEST_TMP}/s-${dmg}/install-windows.ps1" "${dmg}"
    TAMPER rewrite "${TEST_TMP}/s-${dmg}" "${U}" test true "CN=Myriarch Test Signer,O=Myriarch Test"
    expect_fail "scripts: ${dmg} damage in the signature block is refused on verify" "\\[signature_malformed\\] install-windows.ps1" \
      env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" "${TEST_TMP}/s-${dmg}/install-windows.ps1"
    expect_fail "scripts: ... and on accept (${dmg})" "\\[signature_malformed\\] install-windows.ps1" accs "${TEST_TMP}/s-${dmg}" "${T}/ca/trust"
  done
  # verify picks the format check by the extension, ignoring case, like osslsigncode does; formats with another comment syntax are refused
  for ext in PS1 psm1 psd1; do
    cp "${TEST_TMP}/s-space/install-windows.ps1" "${TEST_TMP}/x.${ext}"
    expect_fail "scripts: verify checks the block of a .${ext} file" "\\[signature_malformed\\] x.${ext}" \
      env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" "${TEST_TMP}/x.${ext}"
  done
  for ext in ps1xml js mof; do
    cp "${S}/deploy/scripts/install-windows.ps1" "${TEST_TMP}/x.${ext}"
    expect_fail "scripts: verify refuses a .${ext} file" "\\[script_unsupported\\] x.${ext}" \
      env TOOLS_BIN="${TB}" python3 -I "${AUTHPY}" verify --root "${S}" --trust "${T}/ca/trust" "${TEST_TMP}/x.${ext}"
  done
  expect_fail "scripts: a release build without the configured mode" "\\[mode_missing\\]" accs "${SG_OFF}" "${AUTHDIR}" --release
  expect_fail "scripts: the configured mode is kms, the hook returned unsigned files" "\\[provider_mismatch\\]" accs "${SG_OFF}" "${AUTHDIR}" --release --expect-provider kms
  expect_ok "scripts: an unsigned set is accepted without trust anchors" accs "${SG_OFF}" "${S}/deploy/release/authenticode"
  expect_fail "scripts: a signed set without trust anchors" "\\[trust_missing\\]" accs "${SG_TEST}" "${S}/deploy/release/authenticode"
fi

# ---- 22: check-dist on a synthetic dist/ -----------------------------------------------------------------------------------------
cat > "${TEST_TMP}/mkdist.py" <<'PY'
import hashlib, io, json, os, shutil, sys, tarfile, zipfile
# mkdist.py OUT ROOT STATE VERSION VARIANT - a dist/ in the shape of goreleaser v2.18 with accepted records
out, root, state, ver, variant = sys.argv[1:6]
def sha(b): return hashlib.sha256(b).hexdigest()
for d in (out, root, state):
    shutil.rmtree(d, ignore_errors=True); os.makedirs(d)
os.makedirs(os.path.join(state, "accepted"))
open(os.path.join(state, "stage"), "w").write("stage=apply\nversion=%s\nsnapshot=true\n" % ver)
open(os.path.join(root, "LICENSE"), "w").write("license\n"); open(os.path.join(root, "THIRD_PARTY_NOTICES"), "w").write("notices\n")
scripts = {"install-windows.ps1": b"installer\n", "Set-ServiceEnvironment.ps1": b"service\n"}
for n, b in scripts.items(): open(os.path.join(state, "accepted", n + ".sha256"), "w").write(sha(b) + "\n")
def exe(i, a):
    return b"MZ" + "Myriarch".encode("utf-16-le") + ver.encode("utf-16-le") + (i + ".exe").encode("utf-16-le") + a.encode() * 8
arts, sums = [], {}
def write(name, data):
    open(os.path.join(out, name), "wb").write(data); return sha(data)
def add(name, path, typ, extra, **kw):
    a = {"name": name, "path": "dist/" + path, "type": typ, "extra": extra}; a.update(kw); arts.append(a)
for i in ("myriarch-updater", "myrctl"):
    for a in ("amd64", "arm64"):
        data = exe(i, a); h = sha(data)
        open(os.path.join(state, "accepted", "%s_windows_%s.exe.sha256" % (i, a)), "w").write(h + "\n")
        d = "%s_windows_%s_v1" % (i, a); os.makedirs(os.path.join(out, d))
        write(d + "/" + i + ".exe", data)
        add(i + ".exe", d + "/" + i + ".exe", "Binary", {"Binary": i + ".exe", "ID": i}, goos="windows", goarch=a)
        if i == "myriarch-updater":
            bare = "myriarch-updater_%s_windows_%s.exe" % (ver, a)
            add(bare, d + "/" + i + ".exe", "Binary", {"Format": "binary", "ID": "myriarch-updater-binary", "Checksum": "sha256:" + ("0" * 64 if variant == "badchecksum" and a == "amd64" else h)}, goos="windows", goarch=a)
            sums[bare] = h
        z = io.BytesIO()
        with zipfile.ZipFile(z, "w") as zf:
            zf.writestr(i + ".exe", data if variant != "zipmember" or (i, a) != ("myrctl", "amd64") else data + b"x")
            if i == "myriarch-updater":
                for n, rel in (("install-windows.ps1", "deploy/scripts/"), ("Set-ServiceEnvironment.ps1", "deploy/windows/")): zf.writestr(rel + n, scripts[n])
        name = "%s_%s_windows_%s.zip" % (i, ver, a)
        sums[name] = write(name, z.getvalue()); add(name, name, "Archive", {"ID": i}, goos="windows", goarch=a)
t = io.BytesIO()
with tarfile.open(fileobj=t, mode="w:gz") as tf:
    for n in ["deploy/scripts/install-linux.sh"] + (["deploy/scripts/install-windows.ps1"] if variant == "linuxps1" else []):
        ti = tarfile.TarInfo(n); ti.size = 4; tf.addfile(ti, io.BytesIO(b"abcd"))
name = "myriarch-updater_%s_linux_amd64.tar.gz" % ver
sums[name] = write(name, t.getvalue()); add(name, name, "Archive", {"ID": "myriarch-updater"}, goos="linux", goarch="amd64")
names = [a["name"] for a in arts if a["type"] == "Archive"]
if variant == "nosbom": names = names[1:]
for n in names + ["myriarch-webui_%s" % ver]:
    sn = n + ".sbom.json"; sums[sn] = write(sn, b'{"spdxVersion": "SPDX-2.3"}'); add(sn, sn, "SBOM", {"ID": "archives" if "webui" not in n else "webui"})
if variant == "extrasum": sums["unexpected.txt"] = "0" * 64
sums["LICENSE"] = sha(b"license\n"); sums["THIRD_PARTY_NOTICES"] = sha(b"notices\n")
open(os.path.join(out, "checksums.txt"), "w").write("".join("%s  %s\n" % (sums[n], n) for n in sorted(sums)))
open(os.path.join(out, "artifacts.json"), "w").write(json.dumps(arts))
PY
DC="${TEST_TMP}/dc"
mkdist() { python3 -I "${TEST_TMP}/mkdist.py" "${DC}/dist" "${DC}/root" "${DC}/state" "${VER}" "$1"; }
dist_check() { python3 -I "${S}/ci/release/dist-check.py" check --root "${DC}/root" --dist "${DC}/dist" --state "${DC}/state" --version "${VER}"; }
mkdist ok; expect_ok "check-dist: a matching dist/" dist_check
mkdist zipmember; expect_fail "check-dist: a zip member that is not the accepted file" "\\[dist_mismatch\\] myrctl.exe in myrctl_.*_windows_amd64.zip" dist_check
mkdist linuxps1; expect_fail "check-dist: a .ps1 inside the Linux updater archive" "\\[dist_mismatch\\] myriarch-updater_.*linux_amd64.tar.gz holds the Windows script" dist_check
mkdist ok; rm "${DC}/state/accepted/myrctl_windows_arm64.exe.sha256"
expect_fail "check-dist: an accepted record that is missing" "\\[dist_mismatch\\] no accepted record for myrctl_windows_arm64.exe" dist_check
mkdist ok; echo x >> "${DC}/dist/checksums.txt"
expect_fail "check-dist: a malformed checksums.txt" "\\[dist_mismatch\\] checksums.txt: malformed line" dist_check
mkdist ok; python3 -I - "${DC}/dist/checksums.txt" <<'PY'
import sys
p = sys.argv[1]; lines = [l for l in open(p).read().splitlines() if not l.endswith("  LICENSE")]
open(p, "w").write("".join(l + "\n" for l in lines))
PY
expect_fail "check-dist: LICENSE missing from checksums.txt" "\\[dist_mismatch\\] checksums.txt does not list LICENSE" dist_check
mkdist ok; python3 -I - "${DC}/dist/myrctl_windows_amd64_v1/myrctl.exe" <<'PY'
import sys
open(sys.argv[1], "ab").write(b"x")
PY
expect_fail "check-dist: a binary that is not the accepted file" "\\[dist_mismatch\\] myrctl_windows_amd64.exe in dist/ is not the file that the hook accepted" dist_check
mkdist ok; python3 -I - "${DC}/dist/myriarch-updater_windows_amd64_v1/myriarch-updater.exe" "${DC}/state/accepted/myriarch-updater_windows_amd64.exe.sha256" <<'PY'
import hashlib, sys
b = b"MZ" + b"nothing here"
open(sys.argv[1], "wb").write(b); open(sys.argv[2], "w").write(hashlib.sha256(b).hexdigest() + "\n")
PY
expect_fail "check-dist: a binary without version resources" "\\[dist_mismatch\\] myriarch-updater_windows_amd64.exe carries no version resource string" dist_check
mkdist badchecksum; expect_fail "check-dist: a bare binary whose recorded checksum is not the accepted one" "\\[dist_mismatch\\] the checksum goreleaser recorded for myriarch-updater_.*windows_amd64.exe" dist_check
mkdist extrasum; expect_fail "check-dist: an unexpected entry in checksums.txt" "\\[dist_mismatch\\] checksums.txt lists unexpected entries: unexpected.txt" dist_check
mkdist nosbom; expect_fail "check-dist: an archive without its SBOM" "\\[dist_mismatch\\] [0-9]+ SBOMs for [0-9]+ archives" dist_check
expect_fail "check-dist: stage off is refused" "\\[stage_invalid\\] check-dist needs stage collect or apply" ws MYRIARCH_WINDOWS_STAGE=off bash "${WSH}" check-dist

if [ "${SKIPPED}" -gt 0 ]; then echo "SKIPPED ${SKIPPED} cases: osslsigncode ${OSSLSIGNCODE_VERSION} missing in ${TOOLS_BIN}"; fi
finish_tests "windows-hook-test"
