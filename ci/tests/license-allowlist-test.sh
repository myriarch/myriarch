#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the license policy of the dependencies (ADR-0038): ci/license-allowlist.txt is the source of the release allowlist of
# ci/check-release-licenses.sh. The release check runs with a go-licenses double (no network, no module download); the file is the real
# one, the negative controls are changed copies of it.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
cd "${ROOT}" || exit 1
have python3 || die "python3 is required"
have go || die "go is required (ci/check-release-licenses.sh needs it)"

ALLOWLIST="${ROOT}/ci/license-allowlist.txt"
RELEASE_CHECK="${CI_DIR}/check-release-licenses.sh"

# ---- the checker of the files (stdlib only) -----------------------------------------------------------------------------------
CHECK="${TEST_TMP}/license-policy-check.py"
cat > "${CHECK}" <<'PY'
import re
import sys

ERRORS = []


def err(msg):
    ERRORS.append(msg)


def finish(ok_message):
    if ERRORS:
        for e in ERRORS:
            print(e, file=sys.stderr)
        sys.exit(1)
    if ok_message:
        print(ok_message)


ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.+-]*$")


def read_allowlist(path):
    """ci/license-allowlist.txt: identifier -> list ("release" or "review")."""
    entries = {}
    for n, raw in enumerate(open(path, encoding="utf-8"), 1):
        line = raw.rstrip("\n")
        if not line or line.startswith("#"):
            continue
        parts = line.split("|")
        if len(parts) != 3:
            err("%s:%d: expected <identifier>|<list>|<note>" % (path, n))
            continue
        ident, lst, note = (p.strip() for p in parts)
        if not ID.match(ident):
            err("%s:%d: %r is not a license identifier" % (path, n, ident))
        if lst not in ("release", "review"):
            err("%s:%d: the list must be release or review, not %r" % (path, n, lst))
        if not note:
            err("%s:%d: the note is empty" % (path, n))
        if ident in entries:
            err("%s:%d: %s is listed twice" % (path, n, ident))
        entries[ident] = lst
    if "release" not in entries.values():
        err("%s: there is no entry of the list release" % path)
    return entries


def main():
    mode = sys.argv[1]
    if mode == "allowlist":
        read_allowlist(sys.argv[2])
        finish("ok")
    elif mode == "release":
        for ident, lst in read_allowlist(sys.argv[2]).items():
            if lst == "release":
                print(ident)
        finish("")
    else:
        sys.exit("usage")


main()
PY
chk() { python3 -I "${CHECK}" "$@"; }

# ---- the real files -----------------------------------------------------------------------------------------------------------
expect_ok "allowlist: ci/license-allowlist.txt is well-formed" chk allowlist "${ALLOWLIST}"
RELEASE_IDS="$(chk release "${ALLOWLIST}" 2>/dev/null | sort | tr '\n' ' ')"
[ "${RELEASE_IDS}" = "Apache-2.0 BSD-2-Clause BSD-3-Clause ISC MIT Zlib " ] \
  && ok "allowlist: the release list is Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, MIT and Zlib" \
  || fail "allowlist: the release list changed ('${RELEASE_IDS}'); that is a change of policy and needs a decision record (ADR-0038)"
grep -Eq '^Apache-2\.0\|release\|' "${ALLOWLIST}" && grep -Eq '^MIT-0\|review\|' "${ALLOWLIST}" \
  && ok "allowlist: Apache-2.0 is a release license, and the rest of the CNCF list is review only" \
  || fail "allowlist: the two lists are not what ADR-0038 says"

# ---- ci/check-release-licenses.sh reads the release list ---------------------------------------------------------------------
# go-licenses is a double that prints the report in STUB_REPORT ("<package>,<license>" per line) for every build.
TB="${TEST_TMP}/tools"
mkdir -p "${TB}"
cat > "${TB}/go-licenses" <<'SH'
#!/bin/sh
cat "${STUB_REPORT}"
SH
chmod 0755 "${TB}/go-licenses"
printf 'github.com/example/x|Unknown|Apache-2.0|LICENSE|*|a double for the test\n' > "${TEST_TMP}/overrides.txt"
release_check() { # report-file [env...]
  local report="$1"; shift
  env TOOLS_BIN="${TB}" STUB_REPORT="${report}" LICENSE_OVERRIDES="${TEST_TMP}/overrides.txt" "$@" "${RELEASE_CHECK}"
}
report() { # name line...
  local f="${TEST_TMP}/report-$1.txt"; shift
  printf '%s\n' "$@" > "${f}"
  printf '%s' "${f}"
}
expect_ok   "release check: licenses of the release list pass" release_check "$(report ok example.test/a,MIT example.test/b,Apache-2.0 example.test/c,BSD-3-Clause example.test/d,ISC example.test/e,Zlib example.test/f,BSD-2-Clause)"
expect_fail "release check: a CNCF license that only the dependency review allows fails" "links example.test/pg with license PostgreSQL" release_check "$(report pg example.test/a,MIT example.test/pg,PostgreSQL)"
expect_fail "release check: MPL-2.0 fails" "links example.test/m with license MPL-2.0" release_check "$(report mpl example.test/m,MPL-2.0)"
expect_fail "release check: the dot of an identifier is no wildcard" "links example.test/x with license ApacheX2Y0" release_check "$(report dot example.test/x,ApacheX2Y0)"
expect_ok   "release check: an override of the overrides file still passes a package" release_check "$(report override github.com/example/x/sub,Unknown example.test/a,MIT)"
cat > "${TEST_TMP}/allow-mit-review.txt" <<'EOF'
Apache-2.0|release|test
MIT|review|test
EOF
expect_fail "release check: the list of the file decides (MIT as review only fails)" "links example.test/a with license MIT" release_check "$(report mit example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/allow-mit-review.txt"
cat > "${TEST_TMP}/allow-pg-release.txt" <<'EOF'
Apache-2.0|release|test
PostgreSQL|release|test
EOF
expect_ok   "release check: the list of the file decides (PostgreSQL as release passes)" release_check "$(report pg2 example.test/pg,PostgreSQL)" LICENSE_ALLOWLIST="${TEST_TMP}/allow-pg-release.txt"
printf 'MIT|release\n' > "${TEST_TMP}/bad-fields.txt"
expect_fail "release check: a malformed entry fails" "malformed entry" release_check "$(report ok2 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/bad-fields.txt"
printf 'MIT|bogus|note\n' > "${TEST_TMP}/bad-list.txt"
expect_fail "release check: an unknown list fails" "the list must be release or review" release_check "$(report ok3 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/bad-list.txt"
printf 'MIT|release|note\nMIT|review|note\n' > "${TEST_TMP}/dup-id.txt"
expect_fail "release check: a repeated identifier fails" "MIT is listed twice" release_check "$(report ok4 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/dup-id.txt"
printf 'MIT|release|\n' > "${TEST_TMP}/no-note.txt"
expect_fail "release check: an empty note fails" "the note is empty" release_check "$(report ok5 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/no-note.txt"
printf 'M IT|release|note\n' > "${TEST_TMP}/bad-id.txt"
expect_fail "release check: an identifier with a space fails" "is not a license identifier" release_check "$(report ok6 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/bad-id.txt"
printf '# nothing\nMIT|review|note\n' > "${TEST_TMP}/no-release.txt"
expect_fail "release check: a file without a release entry fails (an empty list would pass nothing, or everything)" "has no entry of the list release" release_check "$(report ok7 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/no-release.txt"
expect_fail "release check: a missing file fails" "license allowlist is missing" release_check "$(report ok8 example.test/a,MIT)" LICENSE_ALLOWLIST="${TEST_TMP}/none.txt"
printf 'github.com/example/x|Unknown|PostgreSQL|LICENSE|*|a license of the review list only\n' > "${TEST_TMP}/overrides-review.txt"
expect_fail "release check: an override must declare a license of the release list" "declared license PostgreSQL is not on the allowlist" \
  env TOOLS_BIN="${TB}" STUB_REPORT="$(report ok9 example.test/a,MIT)" LICENSE_OVERRIDES="${TEST_TMP}/overrides-review.txt" "${RELEASE_CHECK}"

finish_tests "license-allowlist-test"
