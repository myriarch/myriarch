#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intended (code for bash -c)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test for require_tag_on_release_branch (ci/lib.sh): a release tag must be on the release branch, otherwise nothing is
# signed. Local git repositories (origin = bare repo), no network. The second part tests ci/build-release.sh, which has only the
# snapshot mode: the removed key path (release mode, COSIGN_KEY, AUTHENTICODE_PFX) must not come back.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
have git || { echo "git is missing: test skipped"; exit 0; }
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
o="${TEST_TMP}/origin.git"; w="${TEST_TMP}/work"
git init -q --bare -b main "${o}"
git init -q -b main "${w}"
git -C "${w}" remote add origin "${o}"
c() { echo "$1" > "${w}/$1"; git -C "${w}" add "$1"; git -C "${w}" commit -q -m "$1"; git -C "${w}" rev-parse HEAD; }
a="$(c a)"; b="$(c b)"
git -C "${w}" push -q origin main
git -C "${w}" checkout -q -b feature
cc="$(c c)"                      # only on the feature branch, never merged into main
git -C "${w}" push -q origin feature

guard() { # sha [extra env...]
  local sha="$1"; shift
  ( cd "${w}" && env RELEASE_GIT_DIR="${w}" RELEASE_COMMIT="${sha}" RELEASE_TAG=v1.0.0 RELEASE_BRANCH=main "$@" \
      bash -c '. "$1"; require_tag_on_release_branch' _ "${CI_DIR}/lib.sh" )
}
expect_ok   "tag on the tip of main"                            guard "${b}"
expect_ok   "tag on an ancestor of main"                        guard "${a}"
expect_fail "tag on a commit that exists only on the feature branch" "is not on branch main" guard "${cc}"
expect_fail "unknown commit"                                    "is not on branch main" guard "0123456789012345678901234567890123456789"
expect_fail "RELEASE_BRANCH that does not exist on origin"      "could not fetch branch no-such-branch" guard "${b}" RELEASE_BRANCH=no-such-branch
expect_ok   "RELEASE_BRANCH overrides the default branch"       guard "${cc}" RELEASE_BRANCH=feature
expect_ok   "RELEASE_ALLOW_ANY_COMMIT=1 (local only)"           guard "${cc}" RELEASE_ALLOW_ANY_COMMIT=1
# RELEASE_COMMIT unset: HEAD is checked (the work tree is on the feature branch at this point)
head_guard() { ( cd "${w}" && env -u RELEASE_COMMIT RELEASE_GIT_DIR="${w}" RELEASE_TAG=v1.0.0 "$@" bash -c '. "$1"; require_tag_on_release_branch' _ "${CI_DIR}/lib.sh" ); }
expect_fail "RELEASE_COMMIT defaults to HEAD (feature commit is rejected)" "commit ${cc:0:12} .* is not on branch main" head_guard
git -C "${w}" checkout -q main
expect_ok   "RELEASE_COMMIT defaults to HEAD (main tip is accepted)" head_guard
# an existing tag must point at the checked commit, not only at some commit on the branch
git -C "${w}" tag v2.0.0 "${b}"
expect_ok   "existing tag on the checked commit"                guard "${b}" RELEASE_TAG=v2.0.0
expect_fail "existing tag on another commit of main"            "tag v2.0.0 points at ${b:0:12}, not at the commit being released .${a:0:12}" guard "${a}" RELEASE_TAG=v2.0.0
# shallow clone: the check fetches the full history and still reaches the right result
sh_="${TEST_TMP}/shallow"
git clone -q --depth 1 -b feature "file://${o}" "${sh_}" 2>/dev/null
shallow_guard() { ( cd "${sh_}" && env RELEASE_GIT_DIR="${sh_}" RELEASE_COMMIT="$1" RELEASE_TAG=v1.0.0 RELEASE_BRANCH=main bash -c '. "$1"; require_tag_on_release_branch' _ "${CI_DIR}/lib.sh" ); }
expect_fail "shallow clone: a feature commit is rejected" "is not on branch main" shallow_guard "${cc}"
expect_ok   "shallow clone: a commit on main is accepted" shallow_guard "${a}"

# ---- snapshot mode: the only mode of ci/build-release.sh ------------------------------------------------------------------------
# A copy of the build script runs in a scratch tree with stubs for the tools; the stubs record what goreleaser sees.
S="${TEST_TMP}/release-tree"
mkdir -p "${S}/ci" "${S}/bin"
cp "${CI_DIR}/lib.sh" "${CI_DIR}/build-release.sh" "${CI_DIR}/versions.env" "${S}/ci/"
cat > "${S}/ci/install-tools.sh" <<'STUB'
#!/usr/bin/env bash
echo "tools=$*" >> "${RECORD}"
STUB
cat > "${S}/bin/goreleaser" <<'STUB'
#!/usr/bin/env bash
echo "goreleaser: argv=$*" >> "${RECORD}"
echo "goreleaser: stage=${MYRIARCH_WINDOWS_STAGE:-unset}" >> "${RECORD}"
echo "goreleaser: cosign=${COSIGN_KEY:-unset} pfx=${AUTHENTICODE_PFX:-unset}" >> "${RECORD}"
echo "goreleaser: tools_bin=${TOOLS_BIN:-unset}" >> "${RECORD}"
case ":${PATH}:" in *":${ORIGINAL_TOOLS_BIN}:"*) echo "goreleaser: path=holds-tools" >> "${RECORD}" ;; *) echo "goreleaser: path=clean" >> "${RECORD}" ;; esac
[ -x "${TOOLS_BIN}/syft" ] && [ -x "${TOOLS_BIN}/yq" ] && [ -x "${TOOLS_BIN}/go-winres" ] && [ -f "$(ls "${TOOLS_BIN}"/.stamp-syft-* | head -n 1)" ] \
  && echo "goreleaser: linked=yes" >> "${RECORD}"
mkdir -p dist
STUB
printf '#!/usr/bin/env bash\nexit 0\n' > "${S}/bin/tool-stub"
for t in syft yq go-winres; do cp "${S}/bin/tool-stub" "${S}/bin/${t}"; done
rm "${S}/bin/tool-stub"
: > "${S}/bin/.stamp-syft-1.2.3"
chmod 0755 "${S}"/ci/*.sh "${S}"/bin/goreleaser "${S}"/bin/syft "${S}"/bin/yq "${S}"/bin/go-winres
git init -q -b main "${S}" && git -C "${S}" add -A && git -C "${S}" commit -q -m release
RECORD="${TEST_TMP}/record.txt"; : > "${RECORD}"
build() { ( cd "${S}" && env RECORD="${RECORD}" TOOLS_BIN="${S}/bin" ORIGINAL_TOOLS_BIN="${S}/bin" TMPDIR="${TEST_TMP}" PATH="${S}/bin:${PATH}" "$@" bash ci/build-release.sh "${BUILD_MODE-}" ); }
BUILD_MODE=release;  expect_fail "release mode was removed"           "release was removed" build
BUILD_MODE=;         expect_fail "no argument prints the usage"       "usage" build
BUILD_MODE=snapshot; expect_fail "snapshot without the built UI"      "make web-build" build
mkdir -p "${S}/internal/webui/dist"; echo '<html></html>' > "${S}/internal/webui/dist/index.html"
: > "${RECORD}"
BUILD_MODE=snapshot; expect_ok "snapshot with the built UI"           build
grep -q '^tools=goreleaser syft go-winres yq$' "${RECORD}" && ok "snapshot installs goreleaser, syft, go-winres and yq" || fail "unexpected tools: $(grep '^tools=' "${RECORD}")"
grep -q '^goreleaser: argv=release --snapshot --clean --skip=publish -f deploy/release/goreleaser.yaml$' "${RECORD}" && ok "goreleaser runs a snapshot without a sign skip" || fail "unexpected goreleaser arguments: $(grep argv "${RECORD}")"
grep -q 'skip=.*sign' "${RECORD}" && fail "the arguments mention signing"
grep -q '^goreleaser: stage=off$' "${RECORD}" && ok "the Windows stage is off" || fail "the Windows stage is not off: $(grep stage "${RECORD}")"
# the pinned tools reach goreleaser through a TOOLS_BIN of their own, not through PATH (the way the release workflow runs it)
grep -q "^goreleaser: tools_bin=" "${RECORD}" && ! grep -q "^goreleaser: tools_bin=${S}/bin\$" "${RECORD}" \
  && ok "goreleaser gets a TOOLS_BIN of its own (symlinks to the pinned tools)" || fail "unexpected TOOLS_BIN: $(grep tools_bin "${RECORD}")"
grep -q '^goreleaser: path=clean$' "${RECORD}" && ok "the directory of the tools is not on PATH" || fail "PATH still holds the directory of the tools"
grep -q '^goreleaser: linked=yes$' "${RECORD}" && ok "syft, yq, go-winres and the stamps are linked into it" || fail "the tools are not linked into the new TOOLS_BIN"
[ -z "$(ls -d "${TEST_TMP}"/myriarch-snapshot.* 2>/dev/null)" ] && ok "the directory of the links is removed on exit" || fail "the directory of the links is left behind"
# the removed key path: signing variables in the environment change nothing
: > "${RECORD}"
BUILD_MODE=snapshot; expect_ok "snapshot with signing variables in the environment" build COSIGN_KEY=key COSIGN_PASSWORD=pw AUTHENTICODE_PFX=pfx AUTHENTICODE_PFX_PASSWORD_FILE=pw
grep -q '^goreleaser: argv=release --snapshot --clean --skip=publish -f deploy/release/goreleaser.yaml$' "${RECORD}" && ok "... the arguments do not change" || fail "the signing variables changed the goreleaser arguments"
grep -q '^goreleaser: stage=off$' "${RECORD}" && ok "... the stage stays off" || fail "the signing variables changed the stage"

finish_tests release-guard-test
