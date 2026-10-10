#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the SBOM scripts of the release build:
#  - ci/release/pnpm-prod-lock.py reduces a pnpm lockfile to the production dependencies (synthetic input) and refuses input it does
#    not understand
#  - deploy/release/sbom.sh checks the pinned tool versions and the artifact, uses the committed syft configuration without --enrich,
#    drops SYFT_* variables of the caller and refuses an empty document (stubs for syft and yq on PATH)
#  - with the real syft and yq (TOOLS_BIN, pinned versions): the web UI SBOM of the real web/pnpm-lock.yaml lists the production
#    dependencies and none of the dev-only direct dependencies. Without the tools this part is skipped locally and fails when CI=true.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

PRODLOCK="${CI_DIR}/release/pnpm-prod-lock.py"
SBOM="${ROOT}/deploy/release/sbom.sh"

# ---- pnpm-prod-lock.py -------------------------------------------------------------------------------------------------------------
mkdir -p "${TEST_TMP}/lock"
cat > "${TEST_TMP}/lock/package.json" <<'JSON'
{"name": "x", "dependencies": {"a": "^1.0.0"}, "optionalDependencies": {"e": "^1.0.0"}, "devDependencies": {"b": "^1.0.0"}}
JSON
cat > "${TEST_TMP}/lock/lock.json" <<'JSON'
{
  "lockfileVersion": "9.0",
  "settings": {"autoInstallPeers": true},
  "importers": {".": {
    "dependencies": {"a": {"specifier": "^1.0.0", "version": "1.0.0(react@19.0.0)"}},
    "optionalDependencies": {"e": {"specifier": "^1.0.0", "version": "1.0.0"}},
    "devDependencies": {"b": {"specifier": "^1.0.0", "version": "1.0.0"}}
  }},
  "packages": {
    "a@1.0.0": {"resolution": {"integrity": "sha512-a"}}, "b@1.0.0": {"resolution": {"integrity": "sha512-b"}},
    "c@1.0.0": {"resolution": {"integrity": "sha512-c"}}, "d@1.0.0": {"resolution": {"integrity": "sha512-d"}},
    "e@1.0.0": {"resolution": {"integrity": "sha512-e"}}, "react@19.0.0": {"resolution": {"integrity": "sha512-r"}}
  },
  "snapshots": {
    "a@1.0.0(react@19.0.0)": {"dependencies": {"c": "1.0.0", "react": "19.0.0"}},
    "b@1.0.0": {"dependencies": {"c": "1.0.0", "d": "1.0.0"}},
    "c@1.0.0": {}, "d@1.0.0": {}, "e@1.0.0": {}, "react@19.0.0": {}
  }
}
JSON
prod() { python3 -I "${PRODLOCK}" --package-json "${TEST_TMP}/lock/package.json" < "$1"; }
prod "${TEST_TMP}/lock/lock.json" > "${TEST_TMP}/lock/out1.json" 2>"${TEST_TMP}/lock/err" && ok "pnpm-prod-lock reduces a lockfile" || { fail "pnpm-prod-lock failed: $(cat "${TEST_TMP}/lock/err")"; }
prod "${TEST_TMP}/lock/lock.json" > "${TEST_TMP}/lock/out2.json" 2>/dev/null
cmp -s "${TEST_TMP}/lock/out1.json" "${TEST_TMP}/lock/out2.json" && ok "the output is identical across two runs" || fail "the output differs between two runs"
python3 -I - "${TEST_TMP}/lock/out1.json" <<'PY' && ok "production packages stay, dev-only packages are gone (peer suffix resolved)" || fail "unexpected package set"
import json, sys
d = json.load(open(sys.argv[1]))
assert sorted(d["packages"]) == ["a@1.0.0", "c@1.0.0", "e@1.0.0", "react@19.0.0"], sorted(d["packages"])
assert sorted(d["snapshots"]) == ["a@1.0.0(react@19.0.0)", "c@1.0.0", "e@1.0.0", "react@19.0.0"], sorted(d["snapshots"])
assert sorted(d["importers"]["."]) == ["dependencies", "optionalDependencies"]
assert d["lockfileVersion"] == "9.0"
PY
mutate() { # name python-expression-on-d - writes <name>.json
  python3 -I - "${TEST_TMP}/lock/lock.json" "${TEST_TMP}/lock/$1.json" "$2" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
exec(sys.argv[3])
json.dump(d, open(sys.argv[2], "w"))
PY
}
mutate v6 'd["lockfileVersion"] = "6.0"'
mutate nosnap 'del d["snapshots"]["c@1.0.0"]'
mutate names 'd["importers"]["."]["dependencies"]["zzz"] = {"specifier": "1", "version": "1.0.0"}'
mutate link 'd["importers"]["."]["dependencies"]["a"]["version"] = "link:../a"'
expect_fail "pnpm-prod-lock: lockfileVersion 6.0 is refused" "lockfileVersion" prod "${TEST_TMP}/lock/v6.json"
expect_fail "pnpm-prod-lock: a missing snapshot is refused" "snapshot 'c@1.0.0' is missing" prod "${TEST_TMP}/lock/nosnap.json"
expect_fail "pnpm-prod-lock: importer and package.json that differ are refused" "importer '.' does not match web/package.json" prod "${TEST_TMP}/lock/names.json"
expect_fail "pnpm-prod-lock: a link: version is refused" "unsupported" prod "${TEST_TMP}/lock/link.json"
expect_fail "pnpm-prod-lock: input that is not JSON is refused" "not JSON" prod /dev/null

# ---- sbom.sh with stubs ----------------------------------------------------------------------------------------------------------
STUBS="${TEST_TMP}/stubs"; mkdir -p "${STUBS}"
cat > "${STUBS}/syft" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then printf 'Application: syft\nVersion:     %s\n' "${STUB_SYFT_VERSION}"; exit 0; fi
{ printf 'argv=%s\n' "$*"; printf 'enrich=%s\n' "${SYFT_ENRICH:-unset}"; printf 'update=%s\n' "${SYFT_CHECK_FOR_APP_UPDATE:-unset}"; } >> "${STUB_RECORD}"
for a in "$@"; do case "${a}" in spdx-json=*) out="${a#spdx-json=}" ;; esac; done
if [ "${STUB_EMPTY:-0}" = 1 ]; then echo '{"spdxVersion":"SPDX-2.3","packages":[]}' > "${out}"; else echo '{"spdxVersion":"SPDX-2.3","packages":[{"name":"p"}]}' > "${out}"; fi
STUB
cat > "${STUBS}/yq" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" = --version ]; then echo "yq (https://github.com/mikefarah/yq/) version ${STUB_YQ_VERSION}"; exit 0; fi
exec "${REAL_YQ}" "$@"
STUB
chmod +x "${STUBS}/syft" "${STUBS}/yq"
REAL_YQ="$(command -v yq 2>/dev/null || true)"
if [ -z "${REAL_YQ}" ] && [ -x "${TOOLS_BIN}/yq" ]; then REAL_YQ="${TOOLS_BIN}/yq"; fi
export STUB_RECORD="${TEST_TMP}/stub-record.txt"
mkdir -p "${TEST_TMP}/art"; : > "${TEST_TMP}/art/a.tar.gz"; : > "${TEST_TMP}/art/a.txt"
stub_sbom() { # mode args... - runs sbom.sh with the stubs first on PATH
  env TOOLS_BIN="${TEST_TMP}/no-tools" PATH="${STUBS}:${PATH}" STUB_SYFT_VERSION="${SYFT_VERSION}" STUB_YQ_VERSION="${YQ_VERSION}" REAL_YQ="${REAL_YQ}" "$@"
}
expect_fail "sbom.sh: a .txt artifact is refused" "\\[sbom_artifact\\]" stub_sbom "${SBOM}" archive "${TEST_TMP}/art/a.txt" "${TEST_TMP}/o.json"
expect_fail "sbom.sh: a missing artifact is refused" "\\[sbom_artifact\\]" stub_sbom "${SBOM}" archive "${TEST_TMP}/art/none.tar.gz" "${TEST_TMP}/o.json"
expect_fail "sbom.sh: another syft version is refused" "\\[syft_version\\] syft reports 0\\.0\\.1, expected ${SYFT_VERSION}" \
  stub_sbom STUB_SYFT_VERSION=0.0.1 "${SBOM}" archive "${TEST_TMP}/art/a.tar.gz" "${TEST_TMP}/o.json"
expect_fail "sbom.sh: a document with no package is refused" "\\[sbom_empty\\]" stub_sbom STUB_EMPTY=1 "${SBOM}" archive "${TEST_TMP}/art/a.tar.gz" "${TEST_TMP}/o.json"
: > "${STUB_RECORD}"
expect_ok "sbom.sh: an archive is scanned" stub_sbom SYFT_ENRICH=all SYFT_CHECK_FOR_APP_UPDATE=true "${SBOM}" archive "${TEST_TMP}/art/a.tar.gz" "${TEST_TMP}/o.json"
grep -q "^argv=scan -q -c ${ROOT}/deploy/release/syft.yaml --from file ${TEST_TMP}/art/a.tar.gz -o spdx-json=${TEST_TMP}/o.json\$" "${STUB_RECORD}" \
  && ok "syft gets the committed configuration, --from file and the output document" || { fail "unexpected syft arguments"; cat "${STUB_RECORD}" >&2; }
grep -q -- '--enrich' "${STUB_RECORD}" && fail "syft was called with --enrich" || ok "syft is not called with --enrich"
grep -q '^enrich=unset$' "${STUB_RECORD}" && ok "SYFT_ENRICH of the caller is not visible to syft" || fail "SYFT_ENRICH reached syft"
grep -q '^update=false$' "${STUB_RECORD}" && ok "SYFT_CHECK_FOR_APP_UPDATE=false" || fail "SYFT_CHECK_FOR_APP_UPDATE is not false"
if [ -n "${REAL_YQ}" ]; then
  : > "${STUB_RECORD}"
  expect_ok "sbom.sh: the web UI is scanned" stub_sbom "${SBOM}" webui 0.1.0-rc.1 "${TEST_TMP}/w.json"
  grep -q -- '--source-name myriarch-webui --source-version 0.1.0-rc.1' "${STUB_RECORD}" && ok "the web UI SBOM is named and versioned" || fail "unexpected web UI arguments"
  expect_fail "sbom.sh: a version that is not a version is refused" "\\[sbom_version\\]" stub_sbom "${SBOM}" webui latest "${TEST_TMP}/w.json"
  expect_fail "sbom.sh: another yq version is refused" "\\[yq_version\\]" stub_sbom STUB_YQ_VERSION=v0.0.1 "${SBOM}" webui 0.1.0 "${TEST_TMP}/w.json"
fi

# ---- the real tools --------------------------------------------------------------------------------------------------------------
real_syft="${TOOLS_BIN}/syft"; real_yq="${TOOLS_BIN}/yq"
have_real=1
[ -x "${real_syft}" ] && [ -x "${real_yq}" ] || have_real=0
if [ "${have_real}" = 1 ]; then
  [ "$(SYFT_CHECK_FOR_APP_UPDATE=false "${real_syft}" version 2>/dev/null | awk '$1 == "Version:" { print $2; exit }')" = "${SYFT_VERSION}" ] || have_real=0
fi
if [ "${have_real}" = 0 ]; then
  if [ "${CI:-}" = true ]; then fail "syft or yq at the pinned version missing in ${TOOLS_BIN}: ci/install-tools.sh syft yq"
  else echo "  skipped: syft or yq at the pinned version missing in ${TOOLS_BIN} (the web UI SBOM cases)"; fi
else
  if PATH="${TOOLS_BIN}:${PATH}" "${SBOM}" webui 0.1.0-rc.1 "${TEST_TMP}/real.json" 2>"${TEST_TMP}/real.err"; then
    ok "the web UI SBOM is made from the real lockfile"
    "${real_yq}" -o=json '.' "${ROOT}/web/pnpm-lock.yaml" | python3 -I "${PRODLOCK}" --package-json "${ROOT}/web/package.json" > "${TEST_TMP}/real-lock.json"
    # A direct dev dependency can still be a production package (a peer dependency of one): such a name is in the filtered lockfile
    # and then in the SBOM. The SBOM has to hold every production dependency, and no direct dev dependency that the filter dropped.
    python3 -I - "${TEST_TMP}/real.json" "${ROOT}/web/package.json" "${TEST_TMP}/real-lock.json" <<'PY' && ok "it lists every production dependency and none of the dev-only dependencies" || fail "the web UI SBOM does not match web/package.json"
import json, sys
names = {p["name"] for p in json.load(open(sys.argv[1]))["packages"]}
pkg = json.load(open(sys.argv[2]))
kept = {k.split("(")[0].rsplit("@", 1)[0] for k in json.load(open(sys.argv[3]))["packages"]}
missing = [n for n in pkg.get("dependencies", {}) if n not in names]
dev_only = [n for n in pkg.get("devDependencies", {}) if n not in kept and n in names]
dropped = [n for n in pkg.get("devDependencies", {}) if n not in kept]
assert not missing, "production dependencies missing: %s" % missing
assert not dev_only, "dev dependencies present: %s" % dev_only
assert dropped, "the test expects at least one dev-only dependency"
PY
  else
    fail "the web UI SBOM failed: $(cat "${TEST_TMP}/real.err")"
  fi
fi
finish_tests "release-sbom-test"
