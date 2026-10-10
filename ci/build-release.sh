#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# goreleaser build (all platforms of the release matrix) - configuration: deploy/release/goreleaser.yaml.
#
#   ci/build-release.sh snapshot   --snapshot, unsigned, nothing published (LOCAL TEST ONLY)
#   ci/build-release.sh release    build and sign (cosign key, Authenticode) a tagged commit, WITHOUT publishing
#
# This is the interim, key-based release path. The release workflow replaces it with keyless signing
# (ADR-0023); the key-based tooling then lives on only in contrib/enterprise-mirror/.
#
# Release inputs (fail-closed):
#   RELEASE_TAG      tag to release (vX.Y.Z or vX.Y.Z-rc.N); default: the tag that points exactly at HEAD
#   RELEASE_COMMIT   commit to check against the release branch (default: HEAD)
#   RELEASE_BRANCH   release branch (default: main), RELEASE_REMOTE (default: origin); see ci/lib.sh
#   COSIGN_KEY       cosign key file or any KMS URI cosign supports (alias: COSIGN_PRIVATE_KEY), COSIGN_PASSWORD
#                    (may be empty for KMS keys)
#   AUTHENTICODE_PFX, AUTHENTICODE_PFX_PASSWORD_FILE   Windows code-signing certificate (files), plus osslsigncode
#   deploy/release/cosign.pub must be checked in (deploy/release/README.md).
# Release mode first runs the license gates (fail-closed): THIRD_PARTY_NOTICES must match the dependency tree (it is packed into
# every archive and image and compiled into myriarchd) and the linked packages must have allowed licenses
# (scripts/gen-third-party-notices.sh --check, ci/check-release-licenses.sh; they need web/node_modules and the module cache).
# The web UI: a snapshot reuses internal/webui/dist if it exists; the release mode removes it so that goreleaser builds it from
# the tagged commit, and it ignores the tool cache (TOOLS_NO_CACHE=1).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions
cd "${ROOT}"

mode="${1:-}"
cfg=deploy/release/goreleaser.yaml
case "${mode}" in
  snapshot)
    "${CI_DIR}/install-tools.sh" goreleaser
    # LOCAL TEST ONLY (snapshot): unsigned, never for releases
    goreleaser release --snapshot --clean --skip=sign,publish -f "${cfg}" ;;
  release)
    # The release workflow must expose the signing secrets (COSIGN_KEY, COSIGN_PASSWORD, AUTHENTICODE_*) only to jobs that
    # run on protected tags. The check below does not replace that: it only makes sure the tagged commit is reviewed history.
    RELEASE_TAG="${RELEASE_TAG:-$(git describe --tags --exact-match HEAD 2>/dev/null || true)}"
    [ -n "${RELEASE_TAG}" ] || die "RELEASE_TAG is not set and HEAD has no tag: set RELEASE_TAG=vX.Y.Z or check out the tag"
    git rev-parse -q --verify "refs/tags/${RELEASE_TAG}" >/dev/null || die "tag ${RELEASE_TAG} does not exist: create it on the commit to release"
    export RELEASE_TAG
    release_version >/dev/null
    require_tag_on_release_branch
    [ -r deploy/release/cosign.pub ] || die "deploy/release/cosign.pub is missing: commit the public key (deploy/release/README.md), otherwise hosts cannot verify anything"
    export COSIGN_KEY="${COSIGN_KEY:-${COSIGN_PRIVATE_KEY:-}}"
    need_env COSIGN_KEY AUTHENTICODE_PFX AUTHENTICODE_PFX_PASSWORD_FILE
    # COSIGN_PASSWORD may be empty for KMS keys, but not for key files
    case "${COSIGN_KEY}" in *://*) ;; *) need_env COSIGN_PASSWORD ;; esac
    # The job holds the signing keys: it runs the tools it builds itself (never a binary restored from .cache/bin), and the
    # embedded web UI is built from the tagged commit (a leftover internal/webui/dist would be embedded and signed).
    export TOOLS_NO_CACHE=1
    "${CI_DIR}/install-tools.sh" goreleaser cosign go-licenses apt:osslsigncode
    rm -rf "${ROOT}/internal/webui/dist"
    "${ROOT}/scripts/gen-third-party-notices.sh" --check || die "THIRD_PARTY_NOTICES is out of date or incomplete: run make notices and commit the result before tagging"
    "${CI_DIR}/check-release-licenses.sh" || die "the release license check failed"
    goreleaser release --clean --skip=publish -f "${cfg}"
    # Cross-check: verify all signatures with the checked-in public key (openssl, signature format contract)
    contrib/enterprise-mirror/verify-release.sh --public-key deploy/release/cosign.pub --dist dist --version "$(release_version)" --assets ;;
  *) die "usage: ci/build-release.sh snapshot|release" ;;
esac
log "goreleaser ${mode} done: $(find dist -maxdepth 1 -mindepth 1 2>/dev/null | wc -l | tr -d ' ') entries in dist/"
