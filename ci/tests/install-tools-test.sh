#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test for the stamps of ci/install-tools.sh. A Go tool counts as installed only when its binary and a stamp exist, and the name
# of the stamp holds the tool, the version and a digest of ci/tools/<tool>/go.mod and go.sum. A tool cache that
# .github/actions/setup restores under an older key can hold the binary of the same version built from older requirements, and
# the digest is what makes such a tool rebuild. The installer runs from a copy in a scratch tree, next to a stand-in for `go`:
# no network and no real build.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

repo="${TEST_TMP}/repo"; tools="${TEST_TMP}/tools"; stub="${TEST_TMP}/stub"; calls="${TEST_TMP}/go-calls"
mkdir -p "${repo}/ci/tools/helm" "${repo}/ci/tools/go-licenses" "${tools}" "${stub}"
cp "${CI_DIR}/install-tools.sh" "${CI_DIR}/lib.sh" "${CI_DIR}/versions.env" "${repo}/ci/"
printf 'module example.test/root\n\ngo 1.27.2\n' > "${repo}/go.mod"   # go-licenses is built with the toolchain of this module
for t in helm go-licenses; do
  printf 'module example.test/ci/tools/%s\n\ngo 1.26.0\n' "${t}" > "${repo}/ci/tools/${t}/go.mod"
  printf 'example.test/dependency v1.0.0 h1:AAAA\nexample.test/dependency v1.0.0/go.mod h1:BBBB\n' > "${repo}/ci/tools/${t}/go.sum"
done
: > "${calls}"
cat > "${stub}/go" <<'STUB'
#!/bin/sh
# Stand-in for go: records the call, then writes the file that `go build -o <file>` would write
echo "go $* [GOTOOLCHAIN=${GOTOOLCHAIN:-} GOFLAGS=${GOFLAGS:-} dir=$(basename "$(pwd)")]" >> "${FAKE_GO_CALLS}"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; shift; fi
  shift
done
[ -n "${out}" ] || { echo "stand-in for go: no -o" >&2; exit 2; }
printf '#!/bin/sh\necho stand-in\n' > "${out}"
chmod +x "${out}"
STUB
chmod +x "${stub}/go"

installer() { # tool... - the copied installer with the stand-in for go; an exported TOOLS_NO_CACHE does not leak in
  ( unset TOOLS_NO_CACHE; PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" FAKE_GO_CALLS="${calls}" "${repo}/ci/install-tools.sh" "$@" )
}
installer_no_cache() { # tool...
  ( TOOLS_NO_CACHE=1 PATH="${stub}:${PATH}" TOOLS_BIN="${tools}" FAKE_GO_CALLS="${calls}" "${repo}/ci/install-tools.sh" "$@" )
}
go_calls() { wc -l < "${calls}" | tr -d ' '; }
stamps() { # tool - the names of its stamp files, one per line
  local f
  for f in "${tools}"/.stamp-"$1"-*; do
    if [ -e "${f}" ]; then basename "${f}"; fi
  done
}
digest() { # tool - what the stamp must hold, computed here and not with ci/lib.sh: 12 hex digits of SHA-256(go.mod + go.sum)
  local d
  cat "${repo}/ci/tools/$1/go.mod" "${repo}/ci/tools/$1/go.sum" > "${TEST_TMP}/module-files"
  d="$(sha256_of "${TEST_TMP}/module-files")"
  printf '%s' "${d:0:12}"
}
expect_calls() { # count what - the number of go builds so far
  [ "$(go_calls)" = "$1" ] && ok "$2" || fail "$2 (expected $1 go builds so far, saw $(go_calls))"
}
expect_stamps() { # tool stamp - the tool has exactly this one stamp
  [ "$(stamps "$1")" = "$2" ] && ok "stamp of $1: $2" || fail "stamp of $1 is '$(stamps "$1" | tr '\n' ' ')', expected '$2'"
}

# ---- a first install and a hit of the cache ---------------------------------------------------------------------------------------
expect_ok "a first install builds the tool" installer helm
expect_calls 1 "... with one go build"
grep -q "build .*-o ${tools}/helm .*\[GOTOOLCHAIN=local GOFLAGS=-mod=readonly dir=helm\]" "${calls}" \
  && ok "... in ci/tools/helm with -mod=readonly" || fail "the go build did not run as expected: $(cat "${calls}")"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
expect_ok "a second install finds the tool" installer helm
case "${OUT}" in *"from the cache"*) ok "... in the cache" ;; *) fail "the second install did not report the cache: ${OUT}" ;; esac
expect_calls 1 "... without another go build"

# ---- a change of a module file rebuilds the tool -----------------------------------------------------------------------------------
printf 'example.test/other v1.0.0 h1:CCCC\n' >> "${repo}/ci/tools/helm/go.sum"
expect_ok "a changed go.sum" installer helm
expect_calls 2 "... rebuilds the tool"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
printf '\nrequire example.test/dependency v1.0.1 // indirect\n' >> "${repo}/ci/tools/helm/go.mod"
expect_ok "a changed go.mod" installer helm
expect_calls 3 "... rebuilds the tool"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"

# ---- the stamp of a tool cache from before the digest, TOOLS_NO_CACHE and other tools --------------------------------------------
rm -f "${tools}"/.stamp-helm-*
: > "${tools}/.stamp-helm-${HELM_VERSION}"
expect_ok "a stamp without the digest (a tool cache from before the digest)" installer helm
expect_calls 4 "... does not count: the tool is rebuilt"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
expect_ok "TOOLS_NO_CACHE=1 with a matching stamp" installer_no_cache helm
expect_calls 5 "... rebuilds the tool"
expect_ok "another tool, whose go is chosen by the main module" installer go-licenses
expect_calls 6 "... is built"
expect_stamps go-licenses ".stamp-go-licenses-${GOLICENSES_VERSION}-$(digest go-licenses)-go1.27.2"
expect_stamps helm ".stamp-helm-${HELM_VERSION}-$(digest helm)"
tail -n 1 "${calls}" | grep -q "GOTOOLCHAIN=go1.27.2 .*dir=go-licenses" && ok "... with the toolchain of the main module" || fail "go-licenses was not built with go1.27.2: $(tail -n 1 "${calls}")"

# ---- a module file that is missing --------------------------------------------------------------------------------------------------
mv "${repo}/ci/tools/helm/go.sum" "${TEST_TMP}/go.sum.aside"
expect_fail "a missing go.sum, although binary and stamp exist" "ci/tools/helm/go.mod or go.sum is missing" installer helm
expect_calls 6 "... starts no build"
mv "${TEST_TMP}/go.sum.aside" "${repo}/ci/tools/helm/go.sum"
expect_ok "go.sum is back" installer helm
expect_calls 6 "... and the tool is still in the cache"
finish_tests "install-tools-test"
