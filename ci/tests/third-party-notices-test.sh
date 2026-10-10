#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intended
# Test for the THIRD_PARTY_NOTICES generator: the renderer scripts/lib/third_party_notices.py on small fixtures (fake
# go-licenses report lines, fake module directories with CRLF texts and NOTICE files, a fake Vite license.json, fake stored
# texts), the usage of scripts/gen-third-party-notices.sh and the license overrides parser of ci/check-release-licenses.sh.
# No network, no go-licenses run, no web build.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
have python3 || { echo "python3 is missing: test skipped"; exit 0; }
RENDERER="${ROOT}/scripts/lib/third_party_notices.py"
GEN="${ROOT}/scripts/gen-third-party-notices.sh"
F="${TEST_TMP}/fx"
mkdir -p "${F}"

# ---- fixtures -------------------------------------------------------------------------------------------------------
# REUSE-IgnoreStart
# The license texts below are fake. They contain copyright and SPDX lines on purpose.
mkdir -p "${F}/mods/a" "${F}/mods/b/sub" "${F}/mods/c" "${F}/mods/d/lib" "${F}/mods/dev"
printf 'Fake License A\r\nCopyright (c) 2020 Example Corp\r\nSPDX-License-Identifier: MIT\r\n' > "${F}/mods/a/LICENSE"
printf 'Fake NOTICE A\n\n\n' > "${F}/mods/a/NOTICE"
printf 'Fake License B\n' > "${F}/mods/b/LICENSE.txt"
printf 'Fake License B sub\n' > "${F}/mods/b/sub/LICENSE"
printf 'Fake License C (unclassified)\n' > "${F}/mods/c/LICENSE"
printf 'Fake License D lib\n' > "${F}/mods/d/lib/LICENSE"
printf 'Fake NOTICE D\n' > "${F}/mods/d/NOTICE.md"
printf 'Fake License DEV\n' > "${F}/mods/dev/LICENSE"
# REUSE-IgnoreEnd

tab() { local IFS=$'\t'; printf '%s\n' "$*"; }
{
  tab myriarchd release example.test/a v1.0.0 MIT "${F}/mods/a/LICENSE"
  tab myrctl release example.test/a v1.0.0 MIT "${F}/mods/a/LICENSE"
  tab myriarchd release example.test/b v2.0.0 BSD-3-Clause "${F}/mods/b/LICENSE.txt"
  tab myriarchd release example.test/b/sub v2.0.0 MIT "${F}/mods/b/sub/LICENSE"
  tab myriarchd release example.test/c/pkg v0.1.0 Unknown Unknown
  tab myriarchd release example.test/d/lib v0.2.0 ISC "${F}/mods/d/lib/LICENSE"
  tab myriarchd devpg example.test/a v1.0.0 MIT "${F}/mods/a/LICENSE"
  tab myriarchd devpg example.test/dev v3.0.0 MIT "${F}/mods/dev/LICENSE"
} > "${F}/reports.tsv"
{
  tab myriarchd release example.test/a/x example.test/a v1.0.0 "${F}/mods/a"
  tab myrctl release example.test/a/x example.test/a v1.0.0 "${F}/mods/a"
  tab myriarchd release example.test/b/y example.test/b v2.0.0 "${F}/mods/b"
  tab myriarchd release example.test/b/sub/z example.test/b v2.0.0 "${F}/mods/b"
  tab myriarchd release example.test/c/pkg example.test/c v0.1.0 "${F}/mods/c"
  tab myriarchd release example.test/d/lib example.test/d v0.2.0 "${F}/mods/d"
  tab myriarchd release golang.org/x/crypto/x509roots/fallback/bundle golang.org/x/crypto/x509roots/fallback v0.0.0-20260101000000-abcdef123456 "${F}/mods/none"
  tab myriarchd release time/tzdata "" "" ""
  tab myriarchd devpg example.test/a/x example.test/a v1.0.0 "${F}/mods/a"
  tab myriarchd devpg example.test/dev example.test/dev v3.0.0 "${F}/mods/dev"
  tab myriarchd devpg github.com/fergusstrange/embedded-postgres github.com/fergusstrange/embedded-postgres v1.0.0 "${F}/mods/dev"
} > "${F}/packages.tsv"
printf '# comment\n\nexample.test/c|Unknown|Apache-2.0|LICENSE|*|Fake reason for the unclassified license.\n' > "${F}/overrides.txt"
printf '# comment\n' > "${F}/overrides-empty.txt"
printf '# comment\nexample.test/c|Unknown|Apache-2.0|LICENSE|*|Fake reason.\nexample.test/nothing|MIT|MIT|-|*|Matches nothing.\n' > "${F}/overrides-stale.txt"

# fail-closed scope: the override covers example.test/c/pkg only (the fixtures link exactly that package) ...
printf 'example.test/c|Unknown|Apache-2.0|LICENSE|example.test/c/pkg|Fake reason, scoped.\n' > "${F}/overrides-scoped.txt"
printf 'example.test/c|Unknown|Apache-2.0|LICENSE|example.test/c/pkg,example.test/c/other|Fake reason, scoped.\n' > "${F}/overrides-scoped-wider.txt"
printf 'example.test/c|Unknown|Apache-2.0|LICENSE|example.test/other/pkg|Fake reason, scope outside the prefix.\n' > "${F}/overrides-scope-foreign.txt"
# ... and a build that also links a second package of the module
{ cat "${F}/packages.tsv"; tab myriarchd release example.test/c/mpl example.test/c v0.1.0 "${F}/mods/c"; } > "${F}/packages-extra.tsv"

# mk_texts FILE [BETA_VERSION] [EXTRA_ENTRY_NAME] [GO_LICENSE_TEXT]: stored texts with correct sha256 values
mk_texts() {
  python3 -I - "$@" <<'PY'
import hashlib, json, sys
out = sys.argv[1]
beta = sys.argv[2] if len(sys.argv) > 2 else "2.0.0"
extra = sys.argv[3] if len(sys.argv) > 3 else ""
golic = sys.argv[4] if len(sys.argv) > 4 else "Fake Go license\n"
def ent(text, **kw):
    d = {"text": text, "sha256": hashlib.sha256(text.encode()).hexdigest(), "source": "https://example.test/src"}
    d.update(kw)
    return d
npm = []
if beta != "none":
    npm.append(ent("Fake beta license\r\n", name="beta", version=beta, identifier="MIT", note="Fake note for beta."))
if extra:
    npm.append(ent("Fake extra license\n", name=extra, version="1.0.0", identifier="MIT"))
json.dump({"go": {"license": ent(golic), "patents": ent("Fake Go patents\n")}, "npm": npm}, open(out, "w"), indent=2, sort_keys=True)
PY
}
printf '[{"name":"alpha","version":"1.0.0","identifier":"MIT","text":"Fake alpha license\\r\\n"},{"name":"beta","version":"2.0.0","identifier":"MIT AND ISC"}]\n' > "${F}/vite.json"
printf '[{"name":"alpha","version":"1.0.0","identifier":"MIT","text":"Fake alpha license\\n"},{"name":"beta","version":"2.0.0","identifier":"MIT"}]\n' > "${F}/vite-beta-text.json"
mk_texts "${F}/texts.json"
mk_texts "${F}/texts-beta-1.json" 1.0.0
mk_texts "${F}/texts-stale.json" 2.0.0 gamma
mk_texts "${F}/texts-nonpm.json" none
printf '{"go": {"license": {"text": "x\\n", "sha256": "0000", "source": "s"}, "patents": {"text": "y\\n", "sha256": "0000", "source": "s"}}, "npm": []}\n' > "${F}/texts-badsum.json"
printf '[{"name":"alpha","version":"1.0.0","identifier":"MIT","text":"Fake alpha license\\n"}]\n' > "${F}/vite-alpha.json"
mkdir -p "${F}/goroot-ok" "${F}/goroot-bad"
printf 'Fake Go license\r\n' > "${F}/goroot-ok/LICENSE"; printf 'Fake Go patents\n' > "${F}/goroot-ok/PATENTS"
printf 'Another license\n' > "${F}/goroot-bad/LICENSE"

# render OUT [REPORTS PACKAGES VITE TEXTS OVERRIDES [GOROOT]]
render() {
  local out="$1" rep="${2:-reports.tsv}" pkg="${3:-packages.tsv}" vite="${4:-vite.json}" txt="${5:-texts.json}" ov="${6:-overrides.txt}" goroot="${7:-}"
  python3 -I "${RENDERER}" --reports "${F}/${rep}" --packages "${F}/${pkg}" --vite-license "${F}/${vite}" --texts "${F}/${txt}" \
    --overrides "${F}/${ov}" ${goroot:+--goroot "${F}/${goroot}"} --out "${out}"
}

# ---- renderer -------------------------------------------------------------------------------------------------------
expect_ok "renderer: fixtures render" render "${F}/out1.txt"
expect_ok "renderer: second run succeeds" render "${F}/out2.txt"
cmp -s "${F}/out1.txt" "${F}/out2.txt" && ok "renderer: output is deterministic" || fail "renderer: output differs between two runs"
head -n 1 "${F}/out1.txt" | grep -qx 'Third-party software notices and licenses for myriarch' && ok "output: first line" || fail "output: first line"
sed -n 2p "${F}/out1.txt" | grep -qx '======================================================' && ok "output: second line has the length of the title" || fail "output: second line"
check_has() { # name fixed-string
  grep -qxF -- "$2" "${F}/out1.txt" && ok "output: $1" || fail "output: $1 (line '$2' missing)"
}
check_has "section 1" "1. Go standard library"
check_has "section 2" "2. Go modules"
check_has "section 3" "3. Data embedded in the binaries"
check_has "section 4" "4. Web UI dependencies (embedded in myriarchd)"
check_has "stdlib component" "Component: Go standard library"
check_has "module component" "Component: example.test/a v1.0.0"
check_has "used-by order" "Used by: myriarchd, myrctl"
check_has "devpg-only component" "Component: example.test/dev v3.0.0"
check_has "devpg-only used-by" "Used by: myriarchd (development builds with the devpg tag only)"
check_has "npm component" "Component: alpha 1.0.0"
check_has "npm used-by" "Used by: myriarchd (web UI)"
check_has "npm license identifier" "License: MIT AND ISC"
check_has "NOTICE marker" "--- NOTICE ---"
check_has "NOTICE.md in the module root next to a license in a subdirectory" "--- NOTICE.md ---"
check_has "license in a subdirectory" "--- lib/LICENSE ---"
check_has "license of a sub-package" "--- sub/LICENSE ---"
check_has "override declares the license" "License: Apache-2.0"
check_has "override reason as note" "Note: Fake reason for the unclassified license."
check_has "stored npm text note" "Note: Fake note for beta."
check_has "stored npm text source" "Note: Source of the text: https://example.test/src"
check_has "x509roots data entry" "Component: golang.org/x/crypto/x509roots/fallback v0.0.0-20260101000000-abcdef123456"
check_has "tzdata data entry" "Component: time/tzdata"
check_has "embedded PostgreSQL data entry" 'Component: PostgreSQL for `myriarchd dev`'
check_has "CRLF text is stored with LF" "Copyright (c) 2020 Example Corp"
[ "$(grep -c $'\r' "${F}/out1.txt")" = 0 ] && ok "output: no CR characters" || fail "output: contains CR characters"
grep -qF "${TEST_TMP}" "${F}/out1.txt" && fail "output: contains an absolute path" || ok "output: no absolute path"
[ "$(grep -cE '^--- NOTICE(\.txt|\.md)? ---$' "${F}/out1.txt")" = 2 ] && ok "output: two NOTICE sections" || fail "output: NOTICE section count"
[ -n "$(tail -n 1 "${F}/out1.txt")" ] && [ "$(tail -c 1 "${F}/out1.txt" | wc -l | tr -d ' ')" = 1 ] && ok "output: ends with one newline" || fail "output: ending"
# a NOTICE text keeps its content but ends with exactly one newline
awk '/^--- NOTICE ---$/ {getline; print; getline; print; exit}' "${F}/out1.txt" | { read -r l1; read -r l2; [ "${l1}" = "Fake NOTICE A" ] && [ -z "${l2}" ]; } \
  && ok "output: NOTICE text is verbatim, separated by one empty line" || fail "output: NOTICE text"

# drift: a changed license text changes the output
printf 'Fake License A changed\n' > "${F}/mods/a/LICENSE"
render "${F}/out3.txt" >/dev/null 2>&1
cmp -s "${F}/out1.txt" "${F}/out3.txt" && fail "drift: a changed license text is not detected" || ok "drift: a changed license text changes the output"
printf 'Fake License A\r\nCopyright (c) 2020 Example Corp\r\nSPDX-License-Identifier: MIT\r\n' > "${F}/mods/a/LICENSE" # REUSE-IgnoreLine

# failures
expect_fail "fail: Unknown license without override" "example.test/c/pkg has an unknown license" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides-empty.txt
expect_fail "fail: npm package without text and without entry" "beta 2.0.0 has no license text and no entry" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts-nonpm.json overrides.txt
expect_fail "fail: npm version mismatch" "stored license text of beta is for version 1.0.0, but 2.0.0 is bundled" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts-beta-1.json overrides.txt
expect_fail "fail: stale override" "license override for example.test/nothing .* matches no package" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides-stale.txt
expect_fail "fail: stale npm text entry" "stored license text of gamma is stale" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts-stale.json overrides.txt
expect_fail "fail: stored text does not match its sha256" "does not match its sha256" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts-badsum.json overrides.txt
expect_ok   "GOROOT texts equal to the stored texts (CRLF-insensitive)" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides.txt goroot-ok
expect_fail "fail: GOROOT text differs" "GOROOT/LICENSE differs from the stored Go LICENSE text" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides.txt goroot-bad
tab myriarchd release example.test/ghost v1.0.0 MIT "${F}/mods/a/LICENSE" > "${F}/reports-ghost.tsv"
expect_fail "fail: library in no module" "example.test/ghost .* is in no module" render "${F}/x.txt" reports-ghost.tsv packages.tsv vite.json texts.json overrides-empty.txt
expect_ok   "scope: an override with a package list renders when only listed packages are linked" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides-scoped.txt
expect_ok   "scope: a package list may name packages that are not linked" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides-scoped-wider.txt
expect_fail "scope: a linked package outside the list fails closed" "example.test/c/mpl is linked by myriarchd \(release\) but lies outside the scope" render "${F}/x.txt" reports.tsv packages-extra.tsv vite.json texts.json overrides-scoped.txt
expect_fail "scope: a scope outside the prefix is malformed" "the scope must list packages of example.test/c" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides-scope-foreign.txt
expect_ok   "scope: the wildcard scope keeps the prefix semantics" render "${F}/x.txt" reports.tsv packages-extra.tsv vite.json texts.json overrides.txt
rm "${F}/mods/b/LICENSE.txt" "${F}/mods/b/sub/LICENSE"
expect_fail "fail: module without a license file" "has no license text|go-licenses reports no license" render "${F}/x.txt" reports.tsv packages.tsv vite.json texts.json overrides.txt
printf 'x\n' > "${F}/bad.tsv"
expect_fail "fail: malformed input line" "expected 6 fields" render "${F}/x.txt" bad.tsv packages.tsv vite.json texts.json overrides.txt

# ---- deduplicated license texts ---------------------------------------------------------------------------------------
# e1 and e2 ship the same text (e2 with CRLF line endings), e3 differs from it in one byte, e4 and e5 ship the same text and the
# same NOTICE; the npm package "shared" ships the text of e1 as well.
D="${F}/dedupe"
mkdir -p "${D}/mods/e1" "${D}/mods/e2" "${D}/mods/e3" "${D}/mods/e4" "${D}/mods/e5"
# REUSE-IgnoreStart
printf 'Shared License Text\nCopyright (c) 2020 Example Corp\nPermission is granted.\n' > "${D}/mods/e1/LICENSE"
printf 'Shared License Text\r\nCopyright (c) 2020 Example Corp\r\nPermission is granted.\r\n\r\n' > "${D}/mods/e2/LICENSE"
printf 'Shared License Text\nCopyright (c) 2020 Example Corq\nPermission is granted.\n' > "${D}/mods/e3/LICENSE"
printf 'Other License Text\n' > "${D}/mods/e4/LICENSE"
printf 'Other License Text\n' > "${D}/mods/e5/LICENSE"
# REUSE-IgnoreEnd
printf 'Shared NOTICE Text\nline two\n' > "${D}/mods/e4/NOTICE"
printf 'Shared NOTICE Text\nline two\n' > "${D}/mods/e5/NOTICE"
{
  for m in e1 e2 e3 e4 e5; do
    tab myriarchd release "example.test/${m}" v1.0.0 MIT "${D}/mods/${m}/LICENSE"
  done
  tab myrctl release example.test/e1 v1.0.0 MIT "${D}/mods/e1/LICENSE"
} > "${D}/reports.tsv"
{
  for m in e1 e2 e3 e4 e5; do
    tab myriarchd release "example.test/${m}/x" "example.test/${m}" v1.0.0 "${D}/mods/${m}"
  done
  tab myrctl release example.test/e1/x example.test/e1 v1.0.0 "${D}/mods/e1"
} > "${D}/packages.tsv"
printf '[{"name":"shared","version":"1.0.0","identifier":"MIT","text":"Shared License Text\\nCopyright (c) 2020 Example Corp\\nPermission is granted.\\n"}]\n' > "${D}/vite.json"
render_dedupe() { # OUT [extra renderer arguments]
  local out="$1"; shift
  python3 -I "${RENDERER}" --reports "${D}/reports.tsv" --packages "${D}/packages.tsv" --vite-license "${D}/vite.json" \
    --texts "${F}/texts-nonpm.json" --overrides "${F}/overrides-empty.txt" --out "${out}" "$@"
}
cat > "${D}/check.py" <<'PY'
import re, sys

out = open(sys.argv[2], encoding="utf-8").read()
SEP = "-" * 80
head, _, rest = out.partition("\n5. License texts\n================\n")
assert rest, "section 5 is missing"
# The entries are found by their consecutive headers, not by a separator: a license text can hold any line, such as 80 dashes.
texts = {}    # id -> (text, count)
meta = {}     # id -> the lines between the header and the text
rlines = rest.split("\n")
starts = []
for i, line in enumerate(rlines):
    if line == "==== [T%d] ====" % (len(starts) + 1):
        starts.append(i)
for k, i in enumerate(starts):
    j = starts[k + 1] if k + 1 < len(starts) else len(rlines)
    entry = rlines[i + 1:j]
    blank = entry.index("")
    m = re.match(r"Referenced by (\d+) component entr(?:y|ies)\.$", entry[blank - 1])
    assert m, entry[:blank]
    assert entry[blank - 2].startswith("Begins with: ") and entry[blank - 3].startswith("First used by: "), entry[:blank]
    texts["T%d" % (k + 1)] = ("\n".join(entry[blank + 1:]).rstrip("\n") + "\n", int(m.group(1)))
    meta["T%d" % (k + 1)] = entry[:blank]
refs = {}   # component -> {file: id}
blocks = re.split("\n" + SEP + "\nComponent: ", head)[1:]
for b in blocks:
    name = b.split("\n", 1)[0].split(" ")[0]
    refs[name] = {f: t for f, t in re.findall(r"^--- (.+) ---\nLicense text: \[(T\d+)\]$", b, re.M)}
def ref(name, f="LICENSE"):
    return refs[name][f]
checks = {}
def check(fn):
    checks[fn.__name__] = fn
    return fn

@check
def identical_once():
    assert out.count("Permission is granted.") == 2, "expected the shared text and the e3 variant, once each"
    assert sum(1 for t, _ in texts.values() if t == "Shared License Text\nCopyright (c) 2020 Example Corp\nPermission is granted.\n") == 1
@check
def identical_referenced_by_all():
    ids = {ref("example.test/e1"), ref("example.test/e2"), ref("shared")}
    assert len(ids) == 1, ids
    assert texts[ids.pop()][1] == 3, "the shared text is referenced by e1, e2 and the npm package"
    assert ref("example.test/e4") == ref("example.test/e5")
@check
def differing_stay_separate():
    assert ref("example.test/e3") != ref("example.test/e1")
    assert "Example Corq" in texts[ref("example.test/e3")][0]
@check
def notices_verbatim_per_component():
    assert out.count("--- NOTICE ---\nShared NOTICE Text\nline two\n\n") == 2
    for m in ("e4", "e5"):
        assert "NOTICE" not in refs["example.test/%s" % m], "a NOTICE file must not be a reference"
@check
def references_resolve():
    used = {t for r in refs.values() for t in r.values()}
    assert used == set(texts), (used, set(texts))
    assert sorted(texts, key=lambda t: int(t[1:])) == ["T%d" % n for n in range(1, len(texts) + 1)]
    assert len(re.findall(r"^License text: \[T\d+\]$", head, re.M)) == sum(len(r) for r in refs.values())
@check
def counts_match():
    for tid, (_, n) in texts.items():
        assert n == sum(1 for r in refs.values() if tid in r.values()), tid  # component entries, not references
@check
def metadata_kept():
    assert "Component: example.test/e1 v1.0.0\nLicense: MIT\nUsed by: myriarchd, myrctl\n" in out
    assert "Component: shared 1.0.0\nLicense: MIT\nUsed by: myriarchd (web UI)\n" in out
@check
def explanation_on_top():
    assert "How to read this file" in head.split("1. Go standard library")[0] and "[T1]" in head.split("1. Go standard library")[0]

@check
def no_license_claimed_in_headings():
    # the license expression of a component must not become the name of a text it does not describe
    mpl = ref("example.test/f1", "LICENSE.MPL-2.0")
    assert "Mozilla" in texts[mpl][0] and mpl != ref("example.test/f1")
    for tid, lines in meta.items():
        for line in lines:
            if not line.startswith("Begins with: "):
                assert not re.search(r"\b(MIT|BSD-3-Clause|ISC|Apache-2\.0)\b", line), (tid, line)
    assert meta[mpl][0] == "First used by: example.test/f1 v1.0.0 (LICENSE.MPL-2.0)", meta[mpl]
@check
def patents_label_kept():
    assert any(l == "Kind: Additional patent grant of the Go project" for ls in meta.values() for l in ls)
@check
def no_further_normalization():
    base = ref("example.test/g0")
    others = [ref("example.test/g%d" % n) for n in range(1, 6)]
    assert len({base, *others}) == 6, "whitespace and case variants must keep their own IDs"
@check
def same_component_counted_once():
    tid = ref("example.test/h1")
    assert tid == ref("example.test/h1", "sub/LICENSE")
    assert texts[tid][1] == 1, texts[tid]
    assert "Referenced by 1 component entry." in meta[tid]
@check
def separator_line_inside_a_text():
    tid = ref("example.test/i1")
    assert texts[tid][0] == "Dashed License\n" + SEP + "\nafter the dashes\n", texts[tid]
    assert ref("example.test/i2") == "T%d" % (int(tid[1:]) + 1)
    assert texts["T%d" % (int(tid[1:]) + 1)][0] == "Last License\n"

checks[sys.argv[1]]()
PY
expect_ok "dedupe: fixtures render" render_dedupe "${D}/out1.txt"
render_dedupe "${D}/out2.txt" >/dev/null 2>&1
cmp -s "${D}/out1.txt" "${D}/out2.txt" && ok "dedupe: output is identical across two runs" || fail "dedupe: output differs between two runs"
for chk in identical_once identical_referenced_by_all differing_stay_separate notices_verbatim_per_component references_resolve counts_match metadata_kept explanation_on_top; do
  expect_ok "dedupe: ${chk}" python3 -I "${D}/check.py" "${chk}" "${D}/out1.txt"
done
# Headings, whitespace and case, counts, separator lines: f1 is an MIT module that also ships an MPL-2.0 text (its heading must not
# claim MIT); g1..g5 differ from g0 only in a trailing space, an extra blank line, a tab and the case of a word or of the title;
# h1 ships the same text twice (LICENSE and sub/LICENSE); i1 holds a line of 80 dashes.
W="${F}/headings"
mkdir -p "${W}/mods/f1" "${W}/mods/h1/sub"
for m in g0 g1 g2 g3 g4 g5 i1 i2; do mkdir -p "${W}/mods/${m}"; done
# REUSE-IgnoreStart
printf 'MIT License fixture\nCopyright (c) 2020 Example Corp\n' > "${W}/mods/f1/LICENSE"
printf 'Mozilla Public License fixture\nVersion 2.0\n' > "${W}/mods/f1/LICENSE.MPL-2.0"
printf 'Spaced License\nline one\n\nline two\n' > "${W}/mods/g0/LICENSE"
printf 'Spaced License\nline one \n\nline two\n' > "${W}/mods/g1/LICENSE"
printf 'Spaced License\nline one\n\n\nline two\n' > "${W}/mods/g2/LICENSE"
printf 'Spaced License\nline\tone\n\nline two\n' > "${W}/mods/g3/LICENSE"
printf 'Spaced License\nline one\n\nline TWO\n' > "${W}/mods/g4/LICENSE"
printf 'spaced license\nline one\n\nline two\n' > "${W}/mods/g5/LICENSE"
printf 'Same License\n' > "${W}/mods/h1/LICENSE"
printf 'Same License\n' > "${W}/mods/h1/sub/LICENSE"
printf 'Dashed License\n%s\nafter the dashes\n' "$(printf -- '-%.0s' $(seq 80))" > "${W}/mods/i1/LICENSE"
printf 'Last License\n' > "${W}/mods/i2/LICENSE"
# REUSE-IgnoreEnd
{
  for m in f1 g0 g1 g2 g3 g4 g5 h1 i1 i2; do tab myriarchd release "example.test/${m}" v1.0.0 MIT "${W}/mods/${m}/LICENSE"; done
  tab myriarchd release example.test/h1/sub v1.0.0 MIT "${W}/mods/h1/sub/LICENSE"
} > "${W}/reports.tsv"
for m in f1 g0 g1 g2 g3 g4 g5 h1 i1 i2; do tab myriarchd release "example.test/${m}/x" "example.test/${m}" v1.0.0 "${W}/mods/${m}"; done > "${W}/packages.tsv"
tab myriarchd release example.test/h1/sub/x example.test/h1 v1.0.0 "${W}/mods/h1" >> "${W}/packages.tsv"
printf '[{"name":"w","version":"1.0.0","identifier":"MIT","text":"W License\\n"}]\n' > "${W}/vite.json"
render_headings() { # OUT [REPORTS]
  python3 -I "${RENDERER}" --reports "${W}/${2:-reports.tsv}" --packages "${W}/packages.tsv" --vite-license "${W}/vite.json" \
    --texts "${F}/texts-nonpm.json" --overrides "${F}/overrides-empty.txt" --out "$1"
}
expect_ok "headings: fixtures render" render_headings "${W}/out1.txt"
for chk in no_license_claimed_in_headings patents_label_kept no_further_normalization same_component_counted_once separator_line_inside_a_text counts_match references_resolve; do
  expect_ok "headings: ${chk}" python3 -I "${D}/check.py" "${chk}" "${W}/out1.txt"
done
# a text with a line that looks like a heading of section 5 is refused
mkdir -p "${W}/mods/j1"
printf 'Heading Lookalike\n==== [T1] ====\n' > "${W}/mods/j1/LICENSE"
{ cat "${W}/reports.tsv"; tab myriarchd release example.test/j1 v1.0.0 MIT "${W}/mods/j1/LICENSE"; } > "${W}/reports-j1.tsv"
{ cat "${W}/packages.tsv"; tab myriarchd release example.test/j1/x example.test/j1 v1.0.0 "${W}/mods/j1"; } > "${W}/packages-j1.tsv"
expect_fail "headings: a text with a heading lookalike is refused" "looks like a heading of the License texts section" \
  python3 -I "${RENDERER}" --reports "${W}/reports-j1.tsv" --packages "${W}/packages-j1.tsv" --vite-license "${W}/vite.json" \
    --texts "${F}/texts-nonpm.json" --overrides "${F}/overrides-empty.txt" --out "${W}/x.txt"
[ "$(grep -c $'\r' "${D}/out1.txt")" = 0 ] && ok "dedupe: no CR characters" || fail "dedupe: contains CR characters"
size="$(wc -c < "${D}/out1.txt" | tr -d ' ')"
expect_ok   "size guard: output exactly at the limit passes" render_dedupe "${D}/x.txt" --max-bytes "${size}"
expect_fail "size guard: output over the limit fails" "the output is ${size} bytes, which is over the limit of $((size - 1)) bytes" render_dedupe "${D}/x.txt" --max-bytes "$((size - 1))"
render_dedupe "${D}/x.txt" --max-bytes 10 >/dev/null 2>&1; [ $? -eq 1 ] && ok "size guard: exits 1" || fail "size guard: exit status"
grep -q 'MAX_BYTES = 1000000' "${RENDERER}" && ok "size guard: the default limit is 1,000,000 bytes" || fail "size guard: default limit"
if [ -f "${ROOT}/THIRD_PARTY_NOTICES" ]; then
  [ "$(wc -c < "${ROOT}/THIRD_PARTY_NOTICES" | tr -d ' ')" -le 1000000 ] && ok "THIRD_PARTY_NOTICES is at most 1,000,000 bytes" || fail "THIRD_PARTY_NOTICES is over 1,000,000 bytes"
fi

# ---- the stored texts of the repository --------------------------------------------------------------------------------
TEXTS="${ROOT}/scripts/notices/third-party-texts.json"
expect_ok "stored texts: sha256 values match" python3 -I -B -c '
import sys
sys.path.insert(0, sys.argv[1])
import third_party_notices as t
t.load_texts(sys.argv[2])' "${ROOT}/scripts/lib" "${TEXTS}"
expect_ok "stored texts: sorted keys, 2-space indent, final newline" python3 -I -c '
import json, sys
raw = open(sys.argv[1], encoding="utf-8").read()
want = json.dumps(json.loads(raw), indent=2, sort_keys=True, ensure_ascii=False) + "\n"
sys.exit(0 if raw == want else 1)' "${TEXTS}"

# ---- the shell script -------------------------------------------------------------------------------------------------
expect_ok   "script: --help"              "${GEN}" --help
expect_fail "script: unknown flag"        "unknown argument" "${GEN}" --bogus
expect_fail "script: --out without file"  "--out needs a file" "${GEN}" --out
"${GEN}" --bogus >/dev/null 2>&1; [ $? -eq 2 ] && ok "script: unknown flag exits 2" || fail "script: unknown flag exit status"
"${GEN}" --check --out "${F}/missing-notices" >/dev/null 2>&1; [ $? -eq 1 ] && ok "script: --check with a missing file exits 1" || fail "script: --check missing file exit status"
mkdir -p "${F}/sub"
expect_fail "script: a relative --out is resolved before the script changes directory" "/fx/missing-notices does not exist" bash -c "cd '${F}/sub' && '${GEN}' --check --out ../missing-notices"
expect_fail "script: --check names the regenerate hint" "run scripts/gen-third-party-notices.sh \(make notices\)" "${GEN}" --check --out "${F}/missing-notices"

# ---- ci/check-release-licenses.sh reads ci/license-overrides.txt ------------------------------------------------------------
if have go-licenses; then
  printf 'github.com/example/x|Unknown|Apache-2.0|LICENSE|*\n' > "${F}/ov-malformed.txt"
  printf 'github.com/example/x|Unknown|GPL-3.0-only|LICENSE|*|not allowed\n' > "${F}/ov-badlicense.txt"
  # the real module is linked with internal/consts, so a list with the root package only must fail the scope check
  printf 'github.com/cyphar/filepath-securejoin|MPL-2.0|BSD-3-Clause|-|github.com/cyphar/filepath-securejoin|Fake reason.\n' > "${F}/ov-narrow-scope.txt"
  expect_fail "overrides: a linked package outside the scope of an exception fails" "outside the scope of the license exception" env LICENSE_OVERRIDES="${F}/ov-narrow-scope.txt" "${CI_DIR}/check-release-licenses.sh"
  expect_fail "overrides: a malformed line fails the release license check" "malformed entry" env LICENSE_OVERRIDES="${F}/ov-malformed.txt" "${CI_DIR}/check-release-licenses.sh"
  expect_fail "overrides: a declared license outside the allowlist fails" "not on the allowlist" env LICENSE_OVERRIDES="${F}/ov-badlicense.txt" "${CI_DIR}/check-release-licenses.sh"
else
  echo "  skip  overrides parser tests (go-licenses is missing)"
fi
real_overrides_ok() { # every non-comment line of ci/license-overrides.txt has six non-empty fields
  awk -F'|' '!/^#/ && NF > 0 { if (NF < 6 || $1 == "" || $2 == "" || $3 == "" || $4 == "" || $5 == "" || $6 == "") bad = 1 } END { exit bad }' "${CI_DIR}/license-overrides.txt"
}
real_overrides_ok && ok "overrides: ci/license-overrides.txt is well-formed" || fail "overrides: ci/license-overrides.txt is malformed"

finish_tests "third-party-notices-test"
