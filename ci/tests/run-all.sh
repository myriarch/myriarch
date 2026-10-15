#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Runs the script tests. Groups:
#   static             ci/check.sh (consistency of versions.env, Dockerfiles, script paths), check-language-test.sh (language
#                      guard scripts/check-language.sh), check-docs-test.sh (link and front-matter checker
#                      scripts/check-docs.py), image-crane-test.sh (the crane image build scripts),
#                      helm-messages-test.sh (value-path error messages of the Helm chart), license-headers-test.sh (SPDX
#                      header tool scripts/license-headers.py), third-party-notices-test.sh (THIRD_PARTY_NOTICES generator),
#                      license-allowlist-test.sh (ci/license-allowlist.txt as the source of the release allowlist and of the dependency
#                      review: parity with its configuration, the dependency-review job, ci/check-release-licenses.sh with a go-licenses double),
#                      website-test.sh (unit tests of the website checks in website/scripts, ci/website.sh arguments),
#                      check-workflows-test.sh (workflow policy check ci/check-workflows.py), lint-workflows-test.sh (the zizmor
#                      step of ci/lint-workflows.sh, with the real zizmor),
#                      versions-test.sh (VERSION, scripts/set-version.sh, ci/check-versions.sh and the release tag guard
#                      ci/release/guard.sh),
#                      e2e-real-download-test.sh (download and checksum check of the real binaries, scripts/lib/real-binaries.sh),
#                      tool-downloads-test.sh (checksum check of the syft and zizmor downloads and the source build of osslsigncode in
#                      ci/install-tools.sh, pin checks of ci/check.sh),
#                      release-sbom-test.sh (SBOM scripts: pnpm-prod-lock.py, deploy/release/sbom.sh in the environment that goreleaser gives
#                      its sboms commands, also with the real goreleaser),
#                      release-scripts-test.sh (the release scripts of ci/release: identity.env, meta.sh, artifact.sh, stage-assets.sh,
#                      verify-dist.sh, verify-blob.sh with the real cosign, sign-*.sh, registry-login.sh, notes.sh, draft.sh, verify.sh,
#                      finalize.sh; a scan for pinned tools called by their bare name),
#                      release-smoke-test.sh (smoke tests of a published release: ci/release/smoke.sh with stubs, parameter handling
#                      of ci/release/smoke-windows.ps1 with pwsh),
#                      release-workflow-test.sh (shape of .github/workflows/release.yml: frozen identity, job graph, artifact
#                      protocol, order of the registry writes, tool resolution through TOOLS_BIN; with one-change copies that prove the
#                      checks bite),
#                      host-e2e-test.sh (the harness of the host end-to-end test, scripts/host-e2e: the guard, the scrubber and the
#                      allow-list of the artifacts, the throwaway CA, the https file server, the GitOps fixture; no host is changed),
#                      workflow-artifacts-test.sh (the artifact allow-lists and the shape of host-e2e.yml and e2e-kind.yml, with
#                      ci/tests/workflow-artifacts-check.py and copies of the workflows that are broken on purpose)
#   installers         installer-verify.sh (bundle verification in install-linux.sh/.ps1), install-tools-test.sh (stamps of
#                      ci/install-tools.sh), verify-testenv-test.sh, release-guard-test.sh (tag-on-release-branch guard of ci/lib.sh,
#                      snapshot mode of ci/build-release.sh), windows-hook-test.sh (Windows stages, signing hook and acceptance
#                      check of the release build), download-retry-test.sh (retry policy of the downloads of install-linux.sh and
#                      ci/release/smoke.sh against a local HTTP fault server; needs curl and python3)
#   enterprise-mirror  contrib/enterprise-mirror/tests: sign-blob-test.sh, publish-mirror-test.sh, verify-release-test.sh,
#                      ca-env-test.sh
#   pwsh               installer-verify.ps1, download-retry.ps1 (retry policy of install-windows.ps1 and smoke-windows.ps1 against a
#                      local HTTP fault server; skipped without python3); only with pwsh in PATH
# Without arguments: static installers enterprise-mirror (pwsh only when pwsh is available).
# Required tools (cosign, helm, crane, zizmor): ci/install-tools.sh cosign helm crane zizmor; the tests report it when they are missing.
set -uo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
groups=("$@")
if [ "${#groups[@]}" -eq 0 ]; then groups=(static installers enterprise-mirror); have pwsh && groups+=(pwsh); fi
rc=0
run() { printf '\n===== %s =====\n' "$*"; "$@" || rc=1; }
mirror_tests="${ROOT}/contrib/enterprise-mirror/tests"
for g in "${groups[@]}"; do
  case "${g}" in
    static)            run "${CI_DIR}/check.sh"; run "${CI_DIR}/tests/check-language-test.sh"; run "${CI_DIR}/tests/check-docs-test.sh"; run "${CI_DIR}/tests/image-crane-test.sh"; run "${CI_DIR}/tests/helm-messages-test.sh"; run "${CI_DIR}/tests/license-headers-test.sh"; run "${CI_DIR}/tests/third-party-notices-test.sh"; run "${CI_DIR}/tests/license-allowlist-test.sh"; run "${CI_DIR}/tests/vulncheck-test.sh"; run "${CI_DIR}/tests/secrets-scan-test.sh"; run "${CI_DIR}/tests/dockerignore-test.sh"; run "${CI_DIR}/tests/website-test.sh"; run "${CI_DIR}/tests/check-workflows-test.sh"; run "${CI_DIR}/tests/lint-workflows-test.sh"; run "${CI_DIR}/tests/versions-test.sh"; run "${CI_DIR}/tests/e2e-real-download-test.sh"; run "${CI_DIR}/tests/tool-downloads-test.sh"; run "${CI_DIR}/tests/release-sbom-test.sh"; run "${CI_DIR}/tests/release-scripts-test.sh"; run "${CI_DIR}/tests/release-smoke-test.sh"; run "${CI_DIR}/tests/release-workflow-test.sh"; run "${CI_DIR}/tests/host-e2e-test.sh"; run "${CI_DIR}/tests/workflow-artifacts-test.sh" ;;
    installers)        run "${CI_DIR}/tests/installer-verify.sh"; run "${CI_DIR}/tests/install-tools-test.sh"; run "${CI_DIR}/tests/verify-testenv-test.sh"; run "${CI_DIR}/tests/release-guard-test.sh"; run "${CI_DIR}/tests/windows-hook-test.sh"; run "${CI_DIR}/tests/download-retry-test.sh" ;;
    enterprise-mirror) run "${mirror_tests}/sign-blob-test.sh"; run "${mirror_tests}/publish-mirror-test.sh"; run "${mirror_tests}/verify-release-test.sh"; run "${mirror_tests}/ca-env-test.sh" ;;
    pwsh)              work="$(mktemp -d)"
                       run pwsh -NoProfile -NonInteractive -File "${CI_DIR}/tests/installer-verify.ps1" \
                         -Installer "${ROOT}/deploy/scripts/install-windows.ps1" -Templates "${ROOT}/deploy" -Work "${work}/win"
                       run pwsh -NoProfile -NonInteractive -File "${CI_DIR}/tests/download-retry.ps1" \
                         -Installer "${ROOT}/deploy/scripts/install-windows.ps1" -SmokeWindows "${ROOT}/ci/release/smoke-windows.ps1" \
                         -Server "${CI_DIR}/tests/http-fault-server.py" -Work "${work}/retry"
                       rm -rf "${work}" ;;
    *) echo "unknown group: ${g}" >&2; rc=1 ;;
  esac
done
exit "${rc}"
