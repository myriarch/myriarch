#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# REUSE compliance check (REUSE specification 3.3): every file has a license and a copyright holder, either through its
# SPDX header or through REUSE.toml, and LICENSES/ holds exactly the licenses in use. Extra arguments go to `reuse lint`.
#
#   ci/reuse-lint.sh              lint the repository
#   ci/reuse-lint.sh --json       machine readable report
#
# The tool comes from ci/install-tools.sh reuse (make tools). LANGUAGE=en is mandatory: a reuse that was built where
# msgfmt exists ships translations and would print in the language of the environment.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

[ -x "${TOOLS_BIN}/reuse" ] || die "reuse not found in ${TOOLS_BIN}: run 'ci/install-tools.sh reuse' (make tools)"
LANGUAGE=en "${TOOLS_BIN}/reuse" --root "${ROOT}" lint "$@"
