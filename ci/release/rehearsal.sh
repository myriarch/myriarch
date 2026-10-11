#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Rehearsal of the release build (make release-rehearsal; the CI job `build` runs it). It walks the path of a release in a snapshot,
# with a throwaway test CA in place of a signing provider, and publishes nothing:
#
#   1. goreleaser snapshot in stage collect, seal the unsigned Windows set, check dist/
#   2. a signing pass with the throwaway CA (ci/release/windows-sign-hook.sh, WINDOWS_SIGNING=test)
#   3. goreleaser snapshot in stage apply: every rebuilt .exe has to equal the unsigned build, the signatures are attached to the
#      rebuilt files and verified (chain, subject, RFC 3161 timestamp), check dist/
#   4. verify the Windows files in dist/ again, and compare both dist directories (the builds are reproducible)
#   5. stage the release files of dist/ the way the release workflow does (ci/release/stage-assets.sh), which checks the asset list of
#      deploy/release/assets.json against the real goreleaser output
#
# The test CA lives in a temporary directory outside the repository and is deleted on exit; its key never goes anywhere else. The
# staging areas are below .cache/release-rehearsal (git-ignored), the first pass is kept in .cache/release-rehearsal/pass1.
# Needs the built web UI (make web-build) and osslsigncode (a C compiler with the OpenSSL 3 and zlib headers; on Debian and Ubuntu
# the CI job installs libssl-dev and zlib1g-dev first).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
load_versions
cd "${ROOT}"

"${CI_DIR}/install-tools.sh" goreleaser syft go-winres yq osslsigncode
[ -f internal/webui/dist/index.html ] || die "internal/webui/dist/index.html is missing: build the web UI first (make web-build)"
K="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-rehearsal.XXXXXX")"
# the trap comes right after the directory exists: a failure of isolate_tools (a tool missing from the set) must not leave it behind
cleanup() {
  rm -rf "${K}"
  deploy/release/windows-signed.sh clean
}
trap cleanup EXIT
# The pinned tools are reached the way the release workflow reaches them: through TOOLS_BIN, a directory that is on no PATH (the
# former directory .cache/bin leaves PATH). goreleaser gives its sboms commands only a reduced environment, and a script that looks
# for a pinned tool anywhere else fails here, as it does in the release.
isolate_tools "${K}/tools" goreleaser syft go-winres yq osslsigncode

cfg=deploy/release/goreleaser.yaml
R=".cache/release-rehearsal"
rm -rf "${R}"
mkdir -p "${R}"
unset MYRIARCH_WINDOWS_STAGE MYRIARCH_WINDOWS_UNSIGNED_DIR MYRIARCH_WINDOWS_SIGNED_DIR MYRIARCH_AUTHENTICODE_DIR MYRIARCH_WINDOWS_SIGNING WINDOWS_SIGNING WINDOWS_SIGNING_TEST_DIR

section_start collect "pass 1: snapshot in stage collect"
MYRIARCH_WINDOWS_STAGE=collect MYRIARCH_WINDOWS_UNSIGNED_DIR="${R}/windows-unsigned" \
  "${TOOLS_BIN}/goreleaser" release --snapshot --clean --skip=publish -f "${cfg}"
MYRIARCH_WINDOWS_STAGE=collect MYRIARCH_WINDOWS_UNSIGNED_DIR="${R}/windows-unsigned" deploy/release/windows-signed.sh seal
MYRIARCH_WINDOWS_STAGE=collect deploy/release/windows-signed.sh check-dist
mv dist "${R}/pass1"

section_start sign "signing pass with a throwaway test CA"
ci/release/authenticode-test-ca.sh init "${K}/ca"
WINDOWS_SIGNING=test WINDOWS_SIGNING_TEST_DIR="${K}/ca" ci/release/windows-sign-hook.sh --in "${R}/windows-unsigned" --out "${R}/windows-signed"

section_start apply "pass 2: snapshot in stage apply"
MYRIARCH_WINDOWS_STAGE=apply MYRIARCH_WINDOWS_UNSIGNED_DIR="${R}/windows-unsigned" MYRIARCH_WINDOWS_SIGNED_DIR="${R}/windows-signed" MYRIARCH_WINDOWS_SIGNING=test \
  MYRIARCH_AUTHENTICODE_DIR="${K}/ca/trust" "${TOOLS_BIN}/goreleaser" release --snapshot --clean --skip=publish -f "${cfg}"
MYRIARCH_WINDOWS_STAGE=apply deploy/release/windows-signed.sh check-dist

section_start verify "verify the signed files and compare the two builds"
files=()
for id in myriarch-updater myrctl; do
  for arch in amd64 arm64; do
    f=(dist/"${id}"_windows_"${arch}"_*/"${id}.exe")
    [ -f "${f[0]}" ] || die "${id}.exe for windows/${arch} is missing in dist/"
    files+=("${f[0]}")
  done
done
files+=(".cache/release/windows/archive/windows/scripts/install-windows.ps1" ".cache/release/windows/archive/windows/windows/Set-ServiceEnvironment.ps1")
python3 -I ci/release/authenticode.py verify --trust "${K}/ca/trust" "${files[@]}" > /dev/null
python3 -I ci/release/dist-check.py compare --first "${R}/pass1" --second dist
section_start stage "stage the release assets and check them against deploy/release/assets.json"
snapshot_version="$(python3 -I -c 'import json; print(json.load(open("dist/metadata.json", encoding="utf-8"))["version"])')"
ci/release/stage-assets.sh --dist dist --root . --version "${snapshot_version}" --out "${R}/staged"
log "release rehearsal passed: $(wc -l < dist/checksums.txt | tr -d ' ') checksums, signed Windows files verified, builds reproducible, assets staged"
