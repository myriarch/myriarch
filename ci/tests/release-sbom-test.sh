#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the SBOM scripts of the release build:
#  - ci/release/pnpm-prod-lock.py reduces a pnpm lockfile to the production dependencies (synthetic input) and refuses input it does
#    not understand
#  - deploy/release/sbom.sh checks the pinned tool versions and the artifact, uses the committed syft configuration without --enrich,
#    drops SYFT_* variables of the caller and refuses an empty document (stubs for syft and yq in TOOLS_BIN)
#  - the tools of sbom.sh come from TOOLS_BIN only: without TOOLS_BIN the script stops, a syft or yq on PATH is not used. The
#    environment of goreleaser is emulated (HOME, PATH, TMPDIR and the sboms[].env of deploy/release/goreleaser.yaml, nothing else),
#    and with the real goreleaser the same is checked end to end.
#  - with the real syft and yq (TOOLS_BIN, pinned versions): the web UI SBOM of the real web/pnpm-lock.yaml lists the production
#    dependencies and none of the dev-only direct dependencies. Without the tools this part is skipped locally and fails when CI=true.
#  - with the real goreleaser (a module of one file, the sboms block of deploy/release/goreleaser.yaml): both SBOMs are written, and
#    they are not when sboms[].env is removed. Same rule for missing tools. This pins what goreleaser passes to an sboms command.
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
if [ "${STUB_EMPTY:-0}" = 1 ]; then echo '{"spdxVersion":"SPDX-2.3","packages":[]}' > "${out}"
elif [ "${STUB_TMPDIR_ENTRY:-0}" = 1 ]; then
  echo '{"spdxVersion":"SPDX-2.3","documentNamespace":"urn:x","packages":[{"name":"p","SPDXID":"SPDXRef-p"}],"files":[{"SPDXID":"SPDXRef-f1","fileName":"a.txt"},{"SPDXID":"SPDXRef-f2","fileName":"private/var/T/syft-archive-contents-123"}],"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relatedSpdxElement":"SPDXRef-p","relationshipType":"DESCRIBES"},{"spdxElementId":"SPDXRef-p","relatedSpdxElement":"SPDXRef-f2","relationshipType":"CONTAINS"}]}' > "${out}"
else echo '{"spdxVersion":"SPDX-2.3","packages":[{"name":"p"}]}' > "${out}"; fi
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
# PATH of the sbom.sh runs: the system directories and the one of python3, never the pinned tools (they have to come from TOOLS_BIN)
SYS_PATH="$(dirname "$(command -v python3)"):/usr/bin:/bin"
stub_sbom() { # args... - runs a command with the stubs as TOOLS_BIN and no tool on PATH
  env TOOLS_BIN="${STUBS}" PATH="${SYS_PATH}" STUB_SYFT_VERSION="${SYFT_VERSION}" STUB_YQ_VERSION="${YQ_VERSION}" REAL_YQ="${REAL_YQ}" "$@"
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
expect_ok "sbom.sh: a document with the temporary extraction directory of syft" stub_sbom STUB_TMPDIR_ENTRY=1 "${SBOM}" archive "${TEST_TMP}/art/a.tar.gz" "${TEST_TMP}/tmpdir.json"
python3 -I - "${TEST_TMP}/tmpdir.json" <<'PY' && ok "... the entry and its relationships are dropped, the rest is unchanged" || fail "the temporary extraction directory is still in the SBOM"
import json, sys
d = json.load(open(sys.argv[1]))
assert [f["fileName"] for f in d["files"]] == ["a.txt"], d["files"]
assert [r["relationshipType"] for r in d["relationships"]] == ["DESCRIBES"], d["relationships"]
assert d["documentNamespace"] == "urn:x" and d["packages"] == [{"name": "p", "SPDXID": "SPDXRef-p"}]
assert "syft-archive-contents-" not in open(sys.argv[1]).read()
PY
if [ -n "${REAL_YQ}" ]; then
  : > "${STUB_RECORD}"
  expect_ok "sbom.sh: the web UI is scanned" stub_sbom "${SBOM}" webui 0.1.0-rc.1 "${TEST_TMP}/w.json"
  grep -q -- '--source-name myriarch-webui --source-version 0.1.0-rc.1' "${STUB_RECORD}" && ok "the web UI SBOM is named and versioned" || fail "unexpected web UI arguments"
  expect_fail "sbom.sh: a version that is not a version is refused" "\\[sbom_version\\]" stub_sbom "${SBOM}" webui latest "${TEST_TMP}/w.json"
  expect_fail "sbom.sh: another yq version is refused" "\\[yq_version\\]" stub_sbom STUB_YQ_VERSION=v0.0.1 "${SBOM}" webui 0.1.0 "${TEST_TMP}/w.json"
fi

# ---- sbom.sh in the environment of goreleaser ------------------------------------------------------------------------------------
# goreleaser starts an sboms command with HOME, USER, TMPDIR, PATH and a few more variables, plus the sboms[].env of the configuration.
# The emulation uses exactly that: a copy of the files sbom.sh needs in a root without .cache/bin, the env lines of
# deploy/release/goreleaser.yaml (read with yq, TOOLS_BIN rendered with the stub directory) and nothing else. The run on the commit
# of the first release candidate would have failed here with "[syft_version] syft not found".
GR_ROOT="${TEST_TMP}/gr-root"
mkdir -p "${GR_ROOT}/deploy/release" "${GR_ROOT}/ci/release" "${GR_ROOT}/web" "${GR_ROOT}/dist"
cp "${SBOM}" "${ROOT}/deploy/release/syft.yaml" "${GR_ROOT}/deploy/release/"
cp "${CI_DIR}/lib.sh" "${CI_DIR}/versions.env" "${GR_ROOT}/ci/"
cp "${PRODLOCK}" "${GR_ROOT}/ci/release/"
cp "${ROOT}/web/package.json" "${ROOT}/web/pnpm-lock.yaml" "${GR_ROOT}/web/"
: > "${GR_ROOT}/dist/a.tar.gz"
gr_env_lines() { # id [config] - the env entries of one sboms entry, one per line
  "${REAL_YQ}" -r ".sboms[] | select(.id == \"$1\") | .env[]?" "${2:-${ROOT}/deploy/release/goreleaser.yaml}"
}
gr_run() { # id tools-dir command... - the command in the environment goreleaser gives to the sboms entry <id>
  local id="$1" tools="$2" line envs=()
  shift 2
  while IFS= read -r line; do
    [ -n "${line}" ] || continue
    line="${line//\{\{ .Env.TOOLS_BIN \}\}/${tools}}"
    case "${line}" in *"{{"*) fail "an sboms env entry the test cannot render: ${line}"; return 1 ;; esac
    envs+=("${line}")
  done < <(gr_env_lines "${id}")
  # the stub variables are test harness, not part of the goreleaser environment
  ( cd "${GR_ROOT}/dist" && env -i HOME="${HOME}" USER="${USER:-test}" TMPDIR="${TEST_TMP}" PATH="${SYS_PATH}" \
      STUB_SYFT_VERSION="${SYFT_VERSION}" STUB_YQ_VERSION="${YQ_VERSION}" REAL_YQ="${REAL_YQ}" STUB_RECORD="${STUB_RECORD}" \
      ${envs[@]+"${envs[@]}"} "$@" )
}
if [ -z "${REAL_YQ}" ]; then
  if [ "${CI:-}" = true ]; then fail "yq is missing: the environment of goreleaser cannot be emulated (ci/install-tools.sh yq)"
  else echo "  skipped: yq is missing (the environment of goreleaser)"; fi
else
  GR_SBOM="${GR_ROOT}/deploy/release/sbom.sh"
  expect_ok "goreleaser environment: the archive SBOM is written (TOOLS_BIN from sboms[].env)" \
    gr_run archives "${STUBS}" bash "${GR_SBOM}" archive "${GR_ROOT}/dist/a.tar.gz" "${GR_ROOT}/dist/a.tar.gz.sbom.json"
  [ -s "${GR_ROOT}/dist/a.tar.gz.sbom.json" ] && ok "... and the document exists" || fail "the archive SBOM was not written"
  expect_ok "goreleaser environment: the web UI SBOM is written (TOOLS_BIN from sboms[].env)" \
    gr_run webui "${STUBS}" bash "${GR_SBOM}" webui 0.1.0-rc.2 "${GR_ROOT}/dist/webui.sbom.json"
  [ -s "${GR_ROOT}/dist/webui.sbom.json" ] && ok "... and the document exists" || fail "the web UI SBOM was not written"
  # an entry whose env is missing (the first release candidate): the script stops and names the variable
  expect_fail "goreleaser environment: without TOOLS_BIN the script stops" "\\[tools_bin_missing\\].*TOOLS_BIN" \
    gr_run none-such "${STUBS}" bash "${GR_SBOM}" archive "${GR_ROOT}/dist/a.tar.gz" "${GR_ROOT}/dist/none.json"
  expect_fail "goreleaser environment: TOOLS_BIN that is empty counts as not set" "\\[tools_bin_missing\\]" \
    gr_run none-such "${STUBS}" env TOOLS_BIN= bash "${GR_SBOM}" archive "${GR_ROOT}/dist/a.tar.gz" "${GR_ROOT}/dist/none.json"
  # a pinned tool that is only on PATH is not used, even when it reports the pinned version
  expect_fail "goreleaser environment: a syft on PATH is not used" "\\[syft_version\\] syft not found in TOOLS_BIN" \
    gr_run archives "${TEST_TMP}/no-tools" env PATH="${STUBS}:${SYS_PATH}" bash "${GR_SBOM}" archive "${GR_ROOT}/dist/a.tar.gz" "${GR_ROOT}/dist/none.json"
  mkdir -p "${TEST_TMP}/syft-only"; cp "${STUBS}/syft" "${TEST_TMP}/syft-only/syft"
  expect_fail "goreleaser environment: a yq on PATH is not used" "\\[yq_version\\] yq not found in TOOLS_BIN" \
    gr_run webui "${TEST_TMP}/syft-only" env PATH="${STUBS}:${SYS_PATH}" bash "${GR_SBOM}" webui 0.1.0-rc.2 "${GR_ROOT}/dist/none.json"
  # every sboms entry has the env line (the entries of the real configuration are the ones above)
  n_entries="$("${REAL_YQ}" '.sboms | length' "${ROOT}/deploy/release/goreleaser.yaml")"
  n_env="$("${REAL_YQ}" '[.sboms[] | select(.env[] == "TOOLS_BIN={{ .Env.TOOLS_BIN }}")] | length' "${ROOT}/deploy/release/goreleaser.yaml")"
  [ "${n_entries}" -ge 2 ] && [ "${n_entries}" = "${n_env}" ] && ok "every sboms entry of goreleaser.yaml passes TOOLS_BIN" \
    || fail "${n_env} of ${n_entries} sboms entries pass TOOLS_BIN"
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
  python3 -I -c 'import sys, zipfile; z = zipfile.ZipFile(sys.argv[1], "w"); z.writestr("hello.txt", "hello\n"); z.close()' "${TEST_TMP}/real.zip"
  if env PATH="${SYS_PATH}" "${SBOM}" archive "${TEST_TMP}/real.zip" "${TEST_TMP}/real-archive.json" 2>"${TEST_TMP}/real-archive.err"; then
    python3 -I - "${TEST_TMP}/real-archive.json" <<'PY' && ok "the SBOM of a real archive names no temporary extraction directory" || fail "the SBOM of a real archive holds a temporary path"
import json, sys
raw = open(sys.argv[1]).read()
d = json.loads(raw)
assert "syft-archive-contents-" not in raw, "temporary extraction directory"
assert d["spdxVersion"] == "SPDX-2.3" and len(d["packages"]) >= 1
for r in d.get("relationships", []):
    ids = {f["SPDXID"] for f in d.get("files", [])} | {p["SPDXID"] for p in d["packages"]} | {"SPDXRef-DOCUMENT"}
    assert r["spdxElementId"] in ids and r["relatedSpdxElement"] in ids, r
PY
  else
    fail "the SBOM of a real archive failed: $(cat "${TEST_TMP}/real-archive.err")"
  fi
  if env PATH="${SYS_PATH}" "${SBOM}" webui 0.1.0-rc.1 "${TEST_TMP}/real.json" 2>"${TEST_TMP}/real.err"; then
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
# ---- the real goreleaser ---------------------------------------------------------------------------------------------------------
# One-file Go module, the sboms block of deploy/release/goreleaser.yaml (sbom.sh by its absolute path), goreleaser in a reduced
# environment: PATH holds the system directories and go, TOOLS_BIN is outside the PATH. Both SBOMs have to be written. Without the
# env lines the run has to fail: that pins what this goreleaser version passes to an sboms command.
real_gr="${TOOLS_BIN}/goreleaser"
have_gr=1
[ "${have_real}" = 1 ] && [ -x "${real_gr}" ] && have go && have git || have_gr=0
if [ "${have_gr}" = 1 ]; then
  [ "$("${real_gr}" --version 2>/dev/null | awk '$1 == "GitVersion:" { print $2; exit }')" = "${GORELEASER_VERSION#v}" ] \
    || [ "$("${real_gr}" --version 2>/dev/null | awk '$1 == "GitVersion:" { print $2; exit }')" = "${GORELEASER_VERSION}" ] || have_gr=0
fi
if [ "${have_gr}" = 0 ]; then
  if [ "${CI:-}" = true ]; then fail "goreleaser ${GORELEASER_VERSION:-}, syft, yq, go or git missing: the sboms environment of goreleaser cannot be tested (ci/install-tools.sh goreleaser syft yq)"
  else echo "  skipped: goreleaser, syft, yq, go or git at the pinned version missing (the goreleaser sboms cases)"; fi
else
  GR_PATH="$(dirname "$(command -v go)"):/usr/bin:/bin"
  # copy of the files sbom.sh needs, without .cache/bin; its web UI part reads the real lockfile
  GR_MOD="${TEST_TMP}/gr-mod"
  mkdir -p "${GR_MOD}"
  printf 'package main\n\nfunc main() {}\n' > "${GR_MOD}/main.go"
  printf 'module example.test/sbomprobe\n\ngo 1.21\n' > "${GR_MOD}/go.mod"
  gr_conf() { # file with-env(1|0)
    {
      printf 'version: 2\nproject_name: sbomprobe\nbuilds:\n  - main: .\n    binary: sbomprobe\n    goos: [linux]\n    goarch: [amd64]\n    env: [CGO_ENABLED=0]\n'
      printf 'archives:\n  - formats: [tar.gz]\nchecksum:\n  disable: true\nrelease:\n  disable: true\nchangelog:\n  disable: true\n'
      if [ "$2" = 1 ]; then "${real_yq}" '{"sboms": .sboms}' "${ROOT}/deploy/release/goreleaser.yaml"
      else "${real_yq}" '{"sboms": (.sboms | map(del(.env)))}' "${ROOT}/deploy/release/goreleaser.yaml"; fi
    } | sed "s#\\.\\./deploy/release/sbom\\.sh#${GR_ROOT}/deploy/release/sbom.sh#" > "$1"
  }
  gr_conf "${GR_MOD}/with-env.yaml" 1
  gr_conf "${GR_MOD}/without-env.yaml" 0
  ( cd "${GR_MOD}" && git init -q . && git add . \
      && git -c user.name=test -c user.email=test@example.test -c commit.gpgsign=false commit -q -m probe && git -c tag.gpgsign=false tag v0.0.1 ) >/dev/null 2>&1
  gr_goreleaser() { # config
    ( cd "${GR_MOD}" && env -i HOME="${HOME}" USER="${USER:-test}" TMPDIR="${TEST_TMP}" PATH="${GR_PATH}" TOOLS_BIN="${TOOLS_BIN}" \
        GOFLAGS=-mod=mod GOTOOLCHAIN=local GOCACHE="${TEST_TMP}/gocache" "${real_gr}" release --snapshot --clean --skip=publish -f "$1" )
  }
  expect_ok "real goreleaser: the sboms entries get TOOLS_BIN and write both SBOMs" gr_goreleaser with-env.yaml
  n_docs="$(find "${GR_MOD}/dist" -name '*.sbom.json' 2>/dev/null | wc -l | tr -d ' ')"
  [ "${n_docs}" = 2 ] && ok "... the archive SBOM and the web UI SBOM exist" || fail "expected 2 SBOM documents in dist/, found ${n_docs}"
  expect_fail "real goreleaser: without the env lines sbom.sh stops (goreleaser does not pass TOOLS_BIN on)" "\\[tools_bin_missing\\]" \
    gr_goreleaser without-env.yaml
fi
finish_tests "release-sbom-test"
