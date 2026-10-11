#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# The signing hook for the Windows release files: the contract between the build (stage collect) and the packaging (stage apply) of
# deploy/release/windows-signed.sh. The release workflow calls it in its sign-windows job; deploy/release/README.md describes the
# stages.
#
#   WINDOWS_SIGNING=<mode> ci/release/windows-sign-hook.sh --in <unsigned-dir> --out <signed-dir>
#
# Input   the unsigned set: myriarch-updater_windows_{amd64,arm64}.exe, myrctl_windows_{amd64,arm64}.exe, install-windows.ps1,
#         Set-ServiceEnvironment.ps1 and SHA256SUMS (sha256sum format, sorted by name).
# Output  the same seven names (SHA256SUMS over the outputs) plus signer.json (schema 1, see deploy/release/README.md): who signed,
#         with which subject, and the unsigned and signed hashes of every file. The files of a signing provider are not trusted on
#         their own: stage apply attaches their signatures to its own build and verifies chain, subject and RFC 3161 timestamp
#         (ci/release/authenticode.py), so a hook may return anything and still cannot get an unreviewed binary into a release.
#
# WINDOWS_SIGNING is the only input that selects the mode. The hook never reads deploy/release/windows-signing.env itself; the release
# workflow parses that file (data, not shell) and exports the value.
#   off              copy the files unchanged and say so in signer.json (signed false)
#   signpath, kms    the provider clients come with the provider pull request; until then they fail closed
#   test             sign with a throwaway CA from WINDOWS_SIGNING_TEST_DIR (signing/ and trust/, see ci/release/authenticode-test-ca.sh).
#                    For the rehearsal only: it is not a committed mode and is refused on tags and in release.yml.
# The output directory has to be new or empty, and inside the work tree it has to be git-ignored.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"

in_dir=""; out_dir=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --in)  [ "$#" -ge 2 ] || die "usage: windows-sign-hook.sh --in DIR --out DIR"; in_dir="$2"; shift 2 ;;
    --out) [ "$#" -ge 2 ] || die "usage: windows-sign-hook.sh --in DIR --out DIR"; out_dir="$2"; shift 2 ;;
    *) die "usage: windows-sign-hook.sh --in DIR --out DIR (unknown argument '$1')" ;;
  esac
done
[ -n "${in_dir}" ] && [ -n "${out_dir}" ] || die "usage: windows-sign-hook.sh --in DIR --out DIR"
[ -d "${in_dir}" ] || die "[input_missing] ${in_dir} is not a directory"

abs_path() { python3 -I -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "$1"; }
abs_out="$(abs_path "${out_dir}")"
root_real="$(abs_path "${ROOT}")"
case "${abs_out}/" in
  "${root_real}/"*)
    git -C "${ROOT}" check-ignore -q -- "${abs_out#"${root_real}"/}" \
      || die "[staging_dir] ${out_dir} is inside the work tree and not ignored by git: use a path below .cache/ or outside the repository" ;;
esac
if [ -e "${out_dir}" ] && { [ ! -d "${out_dir}" ] || [ -n "$(ls -A "${out_dir}")" ]; }; then
  die "[staging_not_empty] ${out_dir} is not empty: remove it first"
fi

AUTH="${ROOT}/ci/release/authenticode.py"
mode="${WINDOWS_SIGNING-}"
case "${mode}" in
  '') die "[mode_missing] WINDOWS_SIGNING is not set (the release workflow takes it from deploy/release/windows-signing.env)" ;;
  off)
    python3 -I "${AUTH}" sign --root "${ROOT}" --provider off --in "${in_dir}" --out "${out_dir}" ;;
  test)
    case "${GITHUB_REF-}" in refs/tags/*) die "[test_mode_on_tag] WINDOWS_SIGNING=test signs with a throwaway CA and is for the rehearsal only" ;; esac
    [ "${GITHUB_REF_TYPE-}" != tag ] || die "[test_mode_on_tag] WINDOWS_SIGNING=test signs with a throwaway CA and is for the rehearsal only"
    case "${GITHUB_WORKFLOW_REF-}" in */.github/workflows/release.yml@*) die "[test_mode_on_tag] WINDOWS_SIGNING=test signs with a throwaway CA and is for the rehearsal only" ;; esac
    [ -n "${WINDOWS_SIGNING_TEST_DIR-}" ] && [ -d "${WINDOWS_SIGNING_TEST_DIR}/signing" ] && [ -d "${WINDOWS_SIGNING_TEST_DIR}/trust" ] \
      || die "[test_material] WINDOWS_SIGNING_TEST_DIR has to name a directory with signing/ and trust/ (ci/release/authenticode-test-ca.sh init)"
    python3 -I "${AUTH}" sign --root "${ROOT}" --provider test --in "${in_dir}" --out "${out_dir}" --material "${WINDOWS_SIGNING_TEST_DIR}" ;;
  signpath|kms)
    die "[provider_unavailable] WINDOWS_SIGNING=${mode}: the provider client is not implemented yet (it comes with the provider pull request); use off" ;;
  *) die "[mode_invalid] WINDOWS_SIGNING=${mode} is not off, signpath or kms" ;;
esac
