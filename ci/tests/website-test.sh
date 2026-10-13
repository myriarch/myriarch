#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the website tooling that need neither Hugo nor a browser: the unit tests of the check scripts in website/scripts
# (static scan of the built site, local paths, override pins, placeholders, notices, Lighthouse scores), the argument handling of
# ci/website.sh, and the pure functions of the Node scripts. The build itself and the browser check run in the website gate
# (make website-check).
#
#   ci/tests/website-test.sh
#
# Needs python3 (3.9 or newer); node (any version that parses ES modules) extends the scope.
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

have python3 || die "python3 not found"
cd "${ROOT}" || die "cannot enter the repository"

OUT="$(python3 -I -m unittest discover -s website/scripts -p 'test_*.py' 2>&1)"; rc=$?
if [ "${rc}" = 0 ]; then ok "unit tests of website/scripts ($(printf '%s\n' "${OUT}" | sed -n 's/^Ran \([0-9]*\) tests.*/\1/p') tests)"; else fail "unit tests of website/scripts"; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi

# every check script of the gate runs from the repository root with -I and prints a summary line, also without a built site
for f in website/scripts/check-*.py website/scripts/lighthouse-scores.py; do
  expect_fail "${f}: an unknown option is a usage error" 'usage' python3 -I "${f}" --bogus
done

# ci/website.sh: arguments and reserved ports are refused before anything is installed
expect_fail "website.sh: no subcommand" 'usage: ci/website.sh' ci/website.sh
expect_fail "website.sh: unknown subcommand" 'usage: ci/website.sh' ci/website.sh bogus
expect_ok "website.sh: --help" ci/website.sh --help
for port in 8080 18080 18081 54339 19464 19469 19998 19999; do
  expect_fail "website.sh: port ${port} is reserved" 'reserved' env WEBSITE_PORT="${port}" ci/website.sh serve
done
expect_fail "website.sh: a port must be a number" 'must be a number' env WEBSITE_PORT=abc ci/website.sh serve
expect_fail "website.sh: an unknown environment is refused" 'must be production or preview' env WEBSITE_ENV=staging ci/website.sh build

# the Node scripts parse, and the origin test of the request check does what it says
if have node; then
  for f in website/scripts/cdp.mjs website/scripts/check-requests.mjs; do
    expect_ok "${f}: parses" node --check "${f}"
  done
  expect_ok "check-requests.mjs: isForeign" node --input-type=module -e '
    import { isForeign } from "./website/scripts/check-requests.mjs";
    const origin = "http://127.0.0.1:1313";
    const cases = [
      ["http://127.0.0.1:1313/js/main.js", false], ["http://127.0.0.1:1313", false],
      ["http://localhost:1313/x", true], ["http://127.0.0.1:1314/x", true], ["https://127.0.0.1:1313/x", true],
      ["https://unpkg.com/lunr", true], ["data:image/png;base64,AAAA", false], ["blob:http://127.0.0.1:1313/1", false],
      ["about:blank", false], ["not a url", true],
    ];
    for (const [url, want] of cases) if (isForeign(url, origin) !== want) { console.error("isForeign", url, "expected", want); process.exit(1); }
  '
  expect_ok "check-requests.mjs: isSearchIndex" node --input-type=module -e '
    import { isSearchIndex } from "./website/scripts/check-requests.mjs";
    const site = "http://127.0.0.1:1313";
    const cases = [
      [site + "/offline-search-index.c3c61a8a8ffa9a8faf2b10123a55ba3b.json", true], [site + "/offline-search-index.json", true],
      [site + "/offline-search-index.c3c61a8a8ffa9a8faf2b10123a55ba3b.json?v=1", true], [site + "/base/offline-search-index.json", true],
      [site + "/offline-search-index.json.map", false], [site + "/xoffline-search-index.json", false], [site + "/search-index.json", false],
      [site + "/docs/", false], [site + "/js/main.min.0d1312a7.js", false], ["not a url", false],
    ];
    for (const [url, want] of cases) if (isSearchIndex(url) !== want) { console.error("isSearchIndex", url, "expected", want); process.exit(1); }
  '
else
  echo "  skip  node not found: the Node scripts are not tested"
fi

finish_tests "website-test"
