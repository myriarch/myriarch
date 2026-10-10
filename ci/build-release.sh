#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# goreleaser snapshot of the release matrix (all platforms) - configuration: deploy/release/goreleaser.yaml.
#
#   ci/build-release.sh snapshot   --snapshot, unsigned, nothing published (LOCAL TEST ONLY; make release-snapshot)
#
# Releases are built only by the release workflow, which signs keyless (ADR-0023) and runs the license gates of
# ci/check-release-licenses.sh and scripts/gen-third-party-notices.sh --check itself. This script has no key, no signing and no release
# mode. The full path of a release (stage collect, signing hook, stage apply) is rehearsed by ci/release/rehearsal.sh
# (make release-rehearsal).
#
# The web UI has to be built already (make web-build): the release build never runs Node. The Windows files are not signed
# (MYRIARCH_WINDOWS_STAGE=off, which deploy/release/windows-signed.sh accepts only for snapshots), and the version resources that the
# build generates (git-ignored cmd/<binary>/rsrc_windows_*.syso) are removed on exit.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions
cd "${ROOT}"

mode="${1:-}"
cfg=deploy/release/goreleaser.yaml
case "${mode}" in
  snapshot)
    [ -f internal/webui/dist/index.html ] || die "internal/webui/dist/index.html is missing: build the web UI first (make web-build)"
    "${CI_DIR}/install-tools.sh" goreleaser syft go-winres yq
    export PATH="${TOOLS_BIN}:${PATH}"
    export MYRIARCH_WINDOWS_STAGE=off
    trap 'rm -f "${ROOT}"/cmd/myriarch-updater/rsrc_windows_*.syso "${ROOT}"/cmd/myrctl/rsrc_windows_*.syso' EXIT
    # LOCAL TEST ONLY (snapshot): unsigned, never for releases
    goreleaser release --snapshot --clean --skip=publish -f "${cfg}" ;;
  release) die "ci/build-release.sh release was removed: releases are built only by the release workflow; use 'ci/build-release.sh snapshot' or 'make release-rehearsal'" ;;
  *) die "usage: ci/build-release.sh snapshot" ;;
esac
log "goreleaser ${mode} done: $(find dist -maxdepth 1 -mindepth 1 2>/dev/null | wc -l | tr -d ' ') entries in dist/"
