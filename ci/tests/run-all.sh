#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Runs the script tests. Groups:
#   static             ci/check.sh (consistency of versions.env, Dockerfiles, script paths), check-language-test.sh (language
#                      guard scripts/check-language.sh), check-docs-test.sh (link and front-matter checker
#                      scripts/check-docs.py), image-crane-test.sh (the crane image build scripts),
#                      helm-messages-test.sh (value-path error messages of the Helm chart), license-headers-test.sh (SPDX
#                      header tool scripts/license-headers.py), third-party-notices-test.sh (THIRD_PARTY_NOTICES generator),
#                      website-test.sh (unit tests of the website checks in website/scripts, ci/website.sh arguments),
#                      check-workflows-test.sh (workflow policy check ci/check-workflows.py),
#                      e2e-real-download-test.sh (download and checksum check of the real binaries, scripts/lib/real-binaries.sh)
#   installers         installer-verify.sh (signature verification in install-linux.sh/.ps1), install-tools-test.sh (stamps of
#                      ci/install-tools.sh), verify-testenv-test.sh, release-guard-test.sh (tag-on-release-branch guard of ci/lib.sh)
#   enterprise-mirror  contrib/enterprise-mirror/tests: sign-blob-test.sh, publish-mirror-test.sh, verify-release-test.sh,
#                      ca-env-test.sh
#   pwsh               installer-verify.ps1 (only with pwsh in PATH)
# Without arguments: static installers enterprise-mirror (pwsh only when pwsh is available).
# Required tools (cosign, helm, crane): ci/install-tools.sh cosign helm crane; the tests report it when they are missing.
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
    static)            run "${CI_DIR}/check.sh"; run "${CI_DIR}/tests/check-language-test.sh"; run "${CI_DIR}/tests/check-docs-test.sh"; run "${CI_DIR}/tests/image-crane-test.sh"; run "${CI_DIR}/tests/helm-messages-test.sh"; run "${CI_DIR}/tests/license-headers-test.sh"; run "${CI_DIR}/tests/third-party-notices-test.sh"; run "${CI_DIR}/tests/vulncheck-test.sh"; run "${CI_DIR}/tests/secrets-scan-test.sh"; run "${CI_DIR}/tests/dockerignore-test.sh"; run "${CI_DIR}/tests/website-test.sh"; run "${CI_DIR}/tests/check-workflows-test.sh"; run "${CI_DIR}/tests/e2e-real-download-test.sh" ;;
    installers)        run "${CI_DIR}/tests/installer-verify.sh"; run "${CI_DIR}/tests/install-tools-test.sh"; run "${CI_DIR}/tests/verify-testenv-test.sh"; run "${CI_DIR}/tests/release-guard-test.sh" ;;
    enterprise-mirror) run "${mirror_tests}/sign-blob-test.sh"; run "${mirror_tests}/publish-mirror-test.sh"; run "${mirror_tests}/verify-release-test.sh"; run "${mirror_tests}/ca-env-test.sh" ;;
    pwsh)              work="$(mktemp -d)"
                       run pwsh -NoProfile -NonInteractive -File "${CI_DIR}/tests/installer-verify.ps1" \
                         -Installer "${ROOT}/deploy/scripts/install-windows.ps1" -Templates "${ROOT}/deploy" -Work "${work}/win" -SignBlob "${ROOT}/contrib/enterprise-mirror/sign-blob.sh"
                       rm -rf "${work}" ;;
    *) echo "unknown group: ${g}" >&2; rc=1 ;;
  esac
done
exit "${rc}"
