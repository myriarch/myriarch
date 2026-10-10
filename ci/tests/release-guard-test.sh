#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016  # "A && ok || fail": ok/fail are counters; single quotes are intended (code for bash -c)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test for require_tag_on_release_branch (ci/lib.sh): a release tag must be on the release branch, otherwise nothing is
# signed. Local git repositories (origin = bare repo), no network.
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

# ---- release mode: tools without the cache, UI rebuilt from the tag ----------------------------------------------------------
# A copy of the build script runs in a scratch tree with stubs for the tools and gates; the stubs record what the signing job sees.
S="${TEST_TMP}/release-tree"
mkdir -p "${S}/ci" "${S}/scripts" "${S}/deploy/release" "${S}/contrib/enterprise-mirror" "${S}/internal/webui/dist" "${S}/bin"
cp "${CI_DIR}/lib.sh" "${CI_DIR}/build-release.sh" "${CI_DIR}/versions.env" "${S}/ci/"
printf 'key\n' > "${S}/deploy/release/cosign.pub"
printf '<html>planted by an earlier build</html>\n' > "${S}/internal/webui/dist/index.html"
cat > "${S}/ci/install-tools.sh" <<'STUB'
#!/usr/bin/env bash
echo "TOOLS_NO_CACHE=${TOOLS_NO_CACHE:-unset} tools=$*" >> "${RECORD}"
STUB
cat > "${S}/ci/check-release-licenses.sh" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
cp "${S}/ci/check-release-licenses.sh" "${S}/scripts/gen-third-party-notices.sh"
cp "${S}/ci/check-release-licenses.sh" "${S}/contrib/enterprise-mirror/verify-release.sh"
cat > "${S}/bin/goreleaser" <<'STUB'
#!/usr/bin/env bash
if [ -e internal/webui/dist/index.html ]; then echo "goreleaser: stale UI present" >> "${RECORD}"; else echo "goreleaser: no UI present" >> "${RECORD}"; fi
echo "goreleaser: TOOLS_NO_CACHE=${TOOLS_NO_CACHE:-unset}" >> "${RECORD}"
mkdir -p dist
STUB
chmod 0755 "${S}"/ci/*.sh "${S}"/scripts/*.sh "${S}"/contrib/enterprise-mirror/*.sh "${S}"/bin/goreleaser
git init -q -b main "${S}" && git -C "${S}" add -A && git -C "${S}" commit -q -m release && git -C "${S}" tag v1.2.3
RECORD="${TEST_TMP}/record.txt"; : > "${RECORD}"
t_release_mode() { ( cd "${S}" && env RECORD="${RECORD}" TOOLS_BIN="${S}/bin" RELEASE_GIT_DIR="${S}" RELEASE_ALLOW_ANY_COMMIT=1 RELEASE_TAG=v1.2.3 \
  COSIGN_KEY=key COSIGN_PASSWORD=pw AUTHENTICODE_PFX=pfx AUTHENTICODE_PFX_PASSWORD_FILE=pw TOOLS_NO_CACHE=0 bash ci/build-release.sh release ); }
OUT="$(t_release_mode 2>&1)" || true
if grep -q 'tools=goreleaser cosign' "${RECORD}" && ! grep 'tools=goreleaser cosign' "${RECORD}" | grep -vq 'TOOLS_NO_CACHE=1'; then ok "release mode installs the tools without the tool cache (TOOLS_NO_CACHE=1)"; else fail "release mode did not set TOOLS_NO_CACHE=1 for the tool installation"; cat "${RECORD}" "${OUT:+/dev/null}" >&2; printf '%s\n' "${OUT}" | sed 's/^/        /' >&2; fi
if grep -q 'goreleaser: TOOLS_NO_CACHE=1' "${RECORD}"; then ok "... and for goreleaser"; else fail "goreleaser did not see TOOLS_NO_CACHE=1"; fi
if grep -q 'goreleaser: no UI present' "${RECORD}"; then ok "release mode removes a leftover internal/webui/dist (the hook rebuilds it)"; else fail "a leftover internal/webui/dist is still present when goreleaser runs"; fi

finish_tests release-guard-test
