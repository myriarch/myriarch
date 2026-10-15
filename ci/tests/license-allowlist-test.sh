#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of the license policy of the dependencies (ADR-0038):
#  - ci/license-allowlist.txt is the one source of both lists: the "release" entries are the allowlist of ci/check-release-licenses.sh,
#    all entries are the allow-licenses of the dependency review in .github/dependency-review-config.yml;
#  - the exceptions of that configuration are package URLs without a version, and each group has a reason;
#  - the job dependency-review of ci.yml has the shape the policy needs and is part of ci-ok.
# The release check runs with a go-licenses double (no network, no module download); the files are the real ones, the negative controls
# are changed copies of them.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
cd "${ROOT}" || exit 1
have python3 || die "python3 is required"
have go || die "go is required (ci/check-release-licenses.sh needs it)"

ALLOWLIST="${ROOT}/ci/license-allowlist.txt"
CONFIG="${ROOT}/.github/dependency-review-config.yml"
WORKFLOW="${ROOT}/.github/workflows/ci.yml"
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


TOP = re.compile(r"^([a-z][a-z-]*):[ \t]*(.*)$")
ITEM = re.compile(r"^  - (.+)$")
KEYS = ("allow-licenses", "allow-ghsas", "allow-dependencies-licenses")


def has_word(text):
    return re.search(r"[^\W_]", text) is not None


def read_config(path):
    """The dependency review configuration: key -> list of (line number, value, reason). A reason is a comment line directly above the
    first item of a group of items, or the reason of the item above."""
    out = {}
    key = None
    prev = ""
    prev_reason = False
    for n, raw in enumerate(open(path, encoding="utf-8"), 1):
        line = raw.rstrip("\n")
        if not line.strip():
            prev, prev_reason = line, False
            continue
        if line.lstrip().startswith("#"):
            prev, prev_reason = line, has_word(line.lstrip()[1:])
            continue
        m = TOP.match(line)
        if m and not line.startswith(" "):
            key = m.group(1)
            if key not in KEYS:
                err("%s:%d: the key %s is not allowed here (the thresholds are set in the workflow)" % (path, n, key))
            if key in out:
                err("%s:%d: the key %s appears twice" % (path, n, key))
            out[key] = []
            if m.group(2) not in ("", "[]"):
                err("%s:%d: %s must be a block list or an empty list []" % (path, n, key))
            prev, prev_reason = line, False
            continue
        m = ITEM.match(line)
        if not m or key is None:
            err("%s:%d: unexpected line" % (path, n))
            prev, prev_reason = line, False
            continue
        value = m.group(1).strip()
        if value[:1] in "'\"":
            if len(value) < 2 or value[-1] != value[0]:
                err("%s:%d: unbalanced quote" % (path, n))
            value = value[1:-1]
        if " #" in value or "\t#" in value:
            err("%s:%d: a comment behind an item (put the reason above the group)" % (path, n))
        prev_is_item = ITEM.match(prev) is not None
        reason = prev_reason if prev_is_item else (prev.lstrip().startswith("#") and has_word(prev.lstrip()[1:]))
        out[key].append((n, value, reason))
        prev, prev_reason = line, reason
    for k in KEYS:
        if k not in out:
            err("%s: the key %s is missing" % (path, k))
    return out


def check_config(config_path, allowlist_path):
    entries = read_allowlist(allowlist_path)
    cfg = read_config(config_path)
    licenses = [v for _, v, _ in cfg.get("allow-licenses", [])]
    if len(licenses) != len(set(licenses)):
        err("%s: allow-licenses repeats an identifier" % config_path)
    for ident in sorted(set(entries) - set(licenses)):
        err("%s: allow-licenses lacks %s, which %s lists (%s)" % (config_path, ident, allowlist_path, entries[ident]))
    for ident in sorted(set(licenses) - set(entries)):
        err("%s: allow-licenses has %s, which %s does not list" % (config_path, ident, allowlist_path))
    seen = set()
    for n, purl, reason in cfg.get("allow-dependencies-licenses", []):
        if not re.match(r"^pkg:(golang|npm|pypi|githubactions|maven|nuget|cargo|gem|composer|pub|swift|hex)/[^@?#\s]+$", purl):
            err("%s:%d: %r is not a package URL without a version, qualifier or subpath (the action ignores the version)" % (config_path, n, purl))
        if purl.lower() in seen:
            err("%s:%d: %s is listed twice" % (config_path, n, purl))
        seen.add(purl.lower())
        if not reason:
            err("%s:%d: %s has no reason (a comment line with a word directly above its group)" % (config_path, n, purl))
    for n, ghsa, reason in cfg.get("allow-ghsas", []):
        if not re.match(r"^GHSA(-[23456789cfghjmpqrvwx]{4}){3}$", ghsa):
            err("%s:%d: %r is not a GHSA ID" % (config_path, n, ghsa))
        if not reason:
            err("%s:%d: %s has no reason" % (config_path, n, ghsa))
    finish("ok: %d licenses (%d release), %d exceptions" % (len(licenses), sum(1 for v in entries.values() if v == "release"), len(seen)))


def job_block(text, job):
    lines = text.split("\n")
    start = None
    for i, line in enumerate(lines):
        if line == "  %s:" % job:
            start = i
            break
    if start is None:
        return None
    end = len(lines)
    for i in range(start + 1, len(lines)):
        if re.match(r"^  [A-Za-z#]", lines[i]):
            end = i
            break
    return "\n".join(lines[start:end])


def check_workflow(path):
    text = open(path, encoding="utf-8").read()
    jobs_at = text.index("\njobs:\n")
    jobs = re.findall(r"^  ([A-Za-z][A-Za-z0-9_-]*):$", text[jobs_at:], re.M)
    ok_block = job_block(text, "ci-ok")
    if ok_block is None:
        err("%s: there is no job ci-ok" % path)
    else:
        m = re.search(r"^    needs: \[(.*)\]$", ok_block, re.M)
        needs = [x.strip() for x in m.group(1).split(",")] if m else []
        for job in jobs:
            if job != "ci-ok" and job not in needs:
                err("%s: ci-ok does not wait for the job %s (a job that ci-ok ignores is not a required check)" % (path, job))
        for job in needs:
            if job not in jobs:
                err("%s: ci-ok waits for %s, which is not a job" % (path, job))
    block = job_block(text, "dependency-review")
    if block is None:
        err("%s: there is no job dependency-review" % path)
    else:
        uses = re.findall(r"uses: actions/dependency-review-action@([0-9a-f]{40}) # (v[0-9]+\.[0-9]+\.[0-9]+)$", block, re.M)
        if len(uses) != 1:
            err("%s: the job must use actions/dependency-review-action once, pinned to a commit with its release in a comment" % path)
        for want in ("config-file: ./.github/dependency-review-config.yml", "fail-on-severity: high", "fail-on-scopes: runtime",
                     "comment-summary-in-pr: never", "show-openssf-scorecard: false"):
            if "          " + want not in block.split("\n"):
                err("%s: the dependency review lacks the input '%s'" % (path, want))
        if re.search(r"^    permissions:", block, re.M) or re.search(r"\bwrite(-all)?\b", block):
            err("%s: the job dependency-review must keep the read-only token of the workflow (no permissions: key, no write)" % path)
        if "timeout-minutes:" not in block:
            err("%s: the job dependency-review has no timeout" % path)
        # a pull request or a merge group runs the action, every other event passes through a step that says so (a skipped job fails ci-ok)
        if block.count("github.event_name == 'pull_request' || github.event_name == 'merge_group'") != 2:
            err("%s: the checkout and the review must run for pull_request and merge_group only" % path)
        if "github.event_name != 'pull_request' && github.event_name != 'merge_group'" not in block:
            err("%s: the job needs the step for the other events, so that it succeeds instead of being skipped" % path)
        if re.search(r"^    if:", block, re.M):
            err("%s: the job must not have a job-level if: (a skipped job fails ci-ok)" % path)
    finish("ok: %d jobs, all of them in ci-ok" % len(jobs))


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
    elif mode == "config":
        check_config(sys.argv[2], sys.argv[3])
    elif mode == "workflow":
        check_workflow(sys.argv[2])
    else:
        sys.exit("usage")


main()
PY
chk() { python3 -I "${CHECK}" "$@"; }

# ---- the real files -----------------------------------------------------------------------------------------------------------
expect_ok "allowlist: ci/license-allowlist.txt is well-formed" chk allowlist "${ALLOWLIST}"
expect_ok "config: the configuration matches the allowlist, and every exception has a reason" chk config "${CONFIG}" "${ALLOWLIST}"
expect_ok "workflow: dependency-review is wired and part of ci-ok" chk workflow "${WORKFLOW}"
RELEASE_IDS="$(chk release "${ALLOWLIST}" 2>/dev/null | sort | tr '\n' ' ')"
[ "${RELEASE_IDS}" = "Apache-2.0 BSD-2-Clause BSD-3-Clause ISC MIT Zlib " ] \
  && ok "allowlist: the release list is Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, MIT and Zlib" \
  || fail "allowlist: the release list changed ('${RELEASE_IDS}'); that is a change of policy and needs a decision record (ADR-0038)"
grep -Eq '^Apache-2\.0\|release\|' "${ALLOWLIST}" && grep -Eq '^MIT-0\|review\|' "${ALLOWLIST}" \
  && ok "allowlist: Apache-2.0 is a release license, and the rest of the CNCF list is review only" \
  || fail "allowlist: the two lists are not what ADR-0038 says"

# ---- negative controls: changed copies ---------------------------------------------------------------------------------------
M="${TEST_TMP}/mut"
mkdir -p "${M}"
mutate() { # source out old new: OUT is SOURCE with the one occurrence of OLD replaced by NEW (a test setup that cannot be made fails the test)
  python3 -I -c '
import sys
src, out, old, new = sys.argv[1:5]
text = open(src, encoding="utf-8").read()
if text.count(old) != 1:
    sys.exit("mutate: %r occurs %d times in %s" % (old, text.count(old), src))
open(out, "w", encoding="utf-8").write(text.replace(old, new))
' "$@" || fail "test setup: could not change $2"
}
mut_cfg() { mutate "${CONFIG}" "${M}/$1.yml" "$2" "$3"; }
mut_wf() { mutate "${WORKFLOW}" "${M}/$1.yml" "$2" "$3"; }

mut_cfg no-mit $'  - MIT\n' ''
expect_fail "config: a license of the allowlist that the configuration lacks fails" "allow-licenses lacks MIT" chk config "${M}/no-mit.yml" "${ALLOWLIST}"
mut_cfg extra-gpl $'  - Zlib\n' $'  - Zlib\n  - GPL-3.0-only\n'
expect_fail "config: a license that the allowlist does not list fails" "allow-licenses has GPL-3.0-only" chk config "${M}/extra-gpl.yml" "${ALLOWLIST}"
mut_cfg dup $'  - Zlib\n' $'  - Zlib\n  - Zlib\n'
expect_fail "config: a repeated license fails" "repeats an identifier" chk config "${M}/dup.yml" "${ALLOWLIST}"
mut_cfg version "  - 'pkg:pypi/pillow'" "  - 'pkg:pypi/pillow@12.3.0'"
expect_fail "config: an exception with a version fails" "not a package URL without a version" chk config "${M}/version.yml" "${ALLOWLIST}"
mut_cfg dup-purl "  - 'pkg:pypi/pillow'" $'  - \'pkg:pypi/pillow\'\n  - \'pkg:pypi/Pillow\''
expect_fail "config: an exception that is listed twice fails (case does not matter to the action)" "is listed twice" chk config "${M}/dup-purl.yml" "${ALLOWLIST}"
mut_cfg inline-comment "  - 'pkg:pypi/pillow'" "  - 'pkg:pypi/pillow' # fine"
expect_fail "config: a comment behind an exception fails (the reason goes above the group)" "a comment behind an item" chk config "${M}/inline-comment.yml" "${ALLOWLIST}"
mut_cfg no-reason $'(assets/brand/source).\n  - \'pkg:pypi/pillow\'' $'(assets/brand/source).\n\n  - \'pkg:pypi/pillow\''
expect_fail "config: an exception without a reason fails" "pkg:pypi/pillow has no reason" chk config "${M}/no-reason.yml" "${ALLOWLIST}"
mut_cfg threshold $'allow-ghsas: []\n' $'allow-ghsas: []\nfail-on-severity: low\n'
expect_fail "config: a threshold in the file fails (the workflow sets them)" "the key fail-on-severity is not allowed here" chk config "${M}/threshold.yml" "${ALLOWLIST}"
mut_cfg deny $'allow-ghsas: []\n' $'allow-ghsas: []\ndeny-licenses:\n  - GPL-3.0-only\n'
expect_fail "config: deny-licenses fails (it cannot be combined with allow-licenses)" "the key deny-licenses is not allowed here" chk config "${M}/deny.yml" "${ALLOWLIST}"
mut_cfg ghsa-no-reason $'allow-ghsas: []\n' $'allow-ghsas:\n  - GHSA-aaaa-bbbb-cccc\n'
expect_fail "config: an accepted advisory needs a reason" "GHSA-aaaa-bbbb-cccc has no reason" chk config "${M}/ghsa-no-reason.yml" "${ALLOWLIST}"
mut_cfg ghsa-bad $'allow-ghsas: []\n' $'allow-ghsas:\n  # reviewed\n  - GHSA-12345\n'
expect_fail "config: an accepted advisory must be a GHSA ID" "is not a GHSA ID" chk config "${M}/ghsa-bad.yml" "${ALLOWLIST}"
mut_cfg no-key $'allow-ghsas: []\n' ''
expect_fail "config: a missing key fails" "the key allow-ghsas is missing" chk config "${M}/no-key.yml" "${ALLOWLIST}"

mut_wf no-need "compliance, dependency-review, workflows" "compliance, workflows"
expect_fail "workflow: a ci-ok that does not wait for dependency-review fails" "ci-ok does not wait for the job dependency-review" chk workflow "${M}/no-need.yml"
mut_wf lower-severity "fail-on-severity: high" "fail-on-severity: critical"
expect_fail "workflow: another severity threshold fails" "lacks the input 'fail-on-severity: high'" chk workflow "${M}/lower-severity.yml"
mut_wf all-scopes "fail-on-scopes: runtime" "fail-on-scopes: runtime, development"
expect_fail "workflow: another scope fails" "lacks the input 'fail-on-scopes: runtime'" chk workflow "${M}/all-scopes.yml"
mut_wf scorecard "show-openssf-scorecard: false" "show-openssf-scorecard: true"
expect_fail "workflow: the Scorecard lookup of the action stays off" "lacks the input 'show-openssf-scorecard: false'" chk workflow "${M}/scorecard.yml"
mut_wf pr-comment "comment-summary-in-pr: never" "comment-summary-in-pr: always"
expect_fail "workflow: no comment in the pull request" "lacks the input 'comment-summary-in-pr: never'" chk workflow "${M}/pr-comment.yml"
mut_wf job-if $'  dependency-review:\n' $'  dependency-review:\n    if: github.event_name == \'pull_request\'\n'
expect_fail "workflow: a job-level if: fails (a skipped job fails ci-ok)" "must not have a job-level if" chk workflow "${M}/job-if.yml"
mut_wf write $'  dependency-review:\n' $'  dependency-review:\n    permissions:\n      pull-requests: write # comment\n'
expect_fail "workflow: a write scope fails" "must keep the read-only token" chk workflow "${M}/write.yml"
mut_wf unpinned "actions/dependency-review-action@a1d282b36b6f3519aa1f3fc636f609c47dddb294 # v5.0.0" "actions/dependency-review-action@v5"
expect_fail "workflow: an unpinned action fails" "pinned to a commit" chk workflow "${M}/unpinned.yml"
mut_wf only-pr "if: github.event_name != 'pull_request' && github.event_name != 'merge_group'" "if: github.event_name == 'push'"
expect_fail "workflow: a job that does nothing for the other events must say so, or it is skipped" "needs the step for the other events" chk workflow "${M}/only-pr.yml"
mut_wf extra-job $'  ci-ok:\n' $'  extra:\n    runs-on: ubuntu-24.04\n    timeout-minutes: 5\n    steps:\n      - run: echo extra\n\n  ci-ok:\n'
expect_fail "workflow: a job that ci-ok ignores fails" "ci-ok does not wait for the job extra" chk workflow "${M}/extra-job.yml"

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
