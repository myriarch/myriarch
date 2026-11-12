#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the website tooling that need neither Hugo nor a browser: the unit tests of the check scripts in website/scripts
# (static scan of the built site, local paths, override pins, placeholders, notices, Lighthouse scores), the argument handling of
# ci/website.sh, the pure functions of the Node scripts and the generated routes of the website deploy job (website.yml, read as
# text). The build itself and the browser check run in the website gate (make website-check).
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

# website.yml: the routes of the deploy job. The JSON and the Python heredoc of the step "Assemble and guard the output" are cut out of
# the workflow as text and run before any deployment; broken copies prove that the check bites.
RT="${TEST_TMP}/routes"  # below TEST_TMP, which the EXIT trap of lib-test.sh removes
mkdir -p "${RT}"
extract_heredoc() { # delimiter file: the lines between <<'DELIM' and DELIM, without their YAML indentation
  awk -v d="$1" '
    index($0, "<<\047" d "\047") { on = 1; match($0, /^ */); n = RLENGTH; next }
    on && $0 ~ "^[ ]*" d "$" { exit }
    on { print substr($0, n + 1) }' "$2"
}
WF=.github/workflows/website.yml
extract_heredoc JSON "${WF}" > "${RT}/config.json"
extract_heredoc PY "${WF}" > "${RT}/check.py"
if [ -s "${RT}/config.json" ] && [ -s "${RT}/check.py" ]; then
  expect_ok "website.yml: the routes of the deploy job pass their own test" python3 -I "${RT}/check.py" "${RT}/config.json"
  sed 's/myriarch\.vercel\.app/myriarch-x.vercel.app/' "${RT}/config.json" > "${RT}/other-host.json"
  expect_fail "website.yml: the routes test fails for another alias host" 'vercel.app alias' python3 -I "${RT}/check.py" "${RT}/other-host.json"
  python3 -I -c 'import json, sys; c = json.load(open(sys.argv[1])); c["routes"].pop(0); json.dump(c, open(sys.argv[2], "w"))' "${RT}/config.json" "${RT}/no-alias.json"
  expect_fail "website.yml: the routes test fails without the alias route" '::error::' python3 -I "${RT}/check.py" "${RT}/no-alias.json"
  cat > "${RT}/direct.py" <<'RTPY'
import json, sys
routes = json.load(open(sys.argv[1]))["routes"]
alias = {"src": "^/(.*)$", "has": [{"type": "host", "value": "myriarch.vercel.app"}], "status": 308, "headers": {"Location": "https://myriarch.dev/$1"}}
assert routes[0] == alias, "the alias route is not the exact route"
assert [r for r in routes if "has" in r] == [alias], "another route matches on a host"
assert all("*" not in h["value"] for r in routes for h in r.get("has", [])), "a host value has a wildcard"
assert routes[-2] == {"handle": "filesystem"}, "the second last route is not the filesystem handle"
assert routes[-1] == {"src": "/.*", "status": 404, "dest": "/404.html"}, "the last route is not the 404 route"
RTPY
  RTOUT="$(python3 -I "${RT}/direct.py" "${RT}/config.json" 2>&1)"; rc=$?
  if [ "${rc}" = 0 ]; then ok "website.yml: alias route, single host match, no wildcard, handle and 404 last"; else fail "website.yml: the generated routes"; printf '%s\n' "${RTOUT}" | sed 's/^/        /' >&2; fi
else
  fail "website.yml: the JSON or Python heredoc of the route step was not found"
fi

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
