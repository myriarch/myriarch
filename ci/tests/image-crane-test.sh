#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Contract of the crane image scripts (scripts/build-image-crane.sh, scripts/build-collector-image-crane.sh,
# scripts/build-mirror-image-crane.sh) and of their shared helpers (scripts/lib/image-common.sh, ci/image-labels.env):
# IMAGE_REPO is required and validated, base images must be pinned by digest, the label file is validated, the source URL
# is derived without host special cases, and DOCKER_CONFIG is never defaulted. Then the release images: the build from
# the release archives (IMAGE_SOURCE=dist: contract, determinism, checks of the inputs, SBOMs) and the push by digest
# (ci/release/push-images.sh) against a registry on 127.0.0.1 that this test starts and stops. No network access except for
# the pinned tools that are not installed yet (ci/install-tools.sh crane syft).
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
W="${TEST_TMP}"
IMG="${REPO}/scripts/build-image-crane.sh"
COL="${REPO}/scripts/build-collector-image-crane.sh"
MIR="${REPO}/scripts/build-mirror-image-crane.sh"
COMMON="${REPO}/scripts/lib/image-common.sh"
DIGEST="sha256:$(printf '0%.0s' $(seq 1 64))"

# fingerprint of the build output directory of build-image-crane.sh: absent, or listing with sizes and mtimes
cache_state() {
  if [ -e "${REPO}/.cache/image" ]; then
    find "${REPO}/.cache/image" -exec ls -ld {} + 2>/dev/null | cksum
  else
    echo absent
  fi
}

# ---- (a) (b) no IMAGE_REPO: clear message, no side effects ----------------------------------------------------------------
before="$(cache_state)"
t_img_norepo() { env -u IMAGE_REPO bash "${IMG}"; }
expect_fail "image script without IMAGE_REPO fails" "IMAGE_REPO is not set" t_img_norepo
t_img_norepo_dry() { env -u IMAGE_REPO DRY_RUN=1 bash "${IMG}"; }
expect_fail "image script with DRY_RUN=1 still requires IMAGE_REPO" "IMAGE_REPO is not set" t_img_norepo_dry
if [ "$(cache_state)" = "${before}" ]; then ok "image script without IMAGE_REPO leaves .cache/image untouched"; else fail "image script without IMAGE_REPO changed .cache/image"; fi
if command -v make >/dev/null 2>&1; then
  t_make_norepo() { env -u IMAGE_REPO make -s -C "${REPO}" image-crane; }
  expect_fail "make image-crane without IMAGE_REPO fails" "IMAGE_REPO is not set" t_make_norepo
  if [ "$(cache_state)" = "${before}" ]; then ok "make image-crane without IMAGE_REPO leaves .cache/image untouched"; else fail "make image-crane without IMAGE_REPO changed .cache/image"; fi
else
  ok "make not available: Makefile case skipped"
fi

# ---- (c) invalid IMAGE_REPO ----------------------------------------------------------------------------------------------
t_img_tag() { IMAGE_REPO=registry.example.com/x/y:tag bash "${IMG}"; }
expect_fail "IMAGE_REPO with a tag is rejected" "invalid IMAGE_REPO" t_img_tag
t_img_digest() { IMAGE_REPO="registry.example.com/x/y@${DIGEST}" bash "${IMG}"; }
expect_fail "IMAGE_REPO with a digest is rejected" "invalid IMAGE_REPO" t_img_digest
t_img_nohost() { IMAGE_REPO=justaname bash "${IMG}"; }
expect_fail "IMAGE_REPO without a registry host is rejected" "invalid IMAGE_REPO" t_img_nohost
t_img_upper() { IMAGE_REPO=registry.example.com/Org/Name bash "${IMG}"; }
expect_fail "IMAGE_REPO with an uppercase path is rejected" "invalid IMAGE_REPO" t_img_upper

# ---- (d) collector -------------------------------------------------------------------------------------------------------
t_col_norepo() { env -u IMAGE_REPO bash "${COL}" 0.162.0; }
expect_fail "collector script without IMAGE_REPO fails" "IMAGE_REPO is not set" t_col_norepo
t_col_login() { env -u IMAGE_REPO bash "${COL}" login someone; }
expect_fail "collector login without IMAGE_REPO fails" "IMAGE_REPO is not set" t_col_login

# ---- (e) (f) mirror and base images --------------------------------------------------------------------------------------
t_mir_norepo() { env -u IMAGE_REPO -u OUT_DIR bash "${MIR}"; }
expect_fail "mirror script without IMAGE_REPO and OUT_DIR fails" "IMAGE_REPO is not set" t_mir_norepo
t_mir_tag() { env -u OUT_DIR IMAGE_REPO=registry.example.com/x/mirror BASE_IMAGE=docker.io/library/busybox:1.37 bash "${MIR}"; }
expect_fail "mirror script rejects a base image without digest" "pinned by digest" t_mir_tag
t_mir_login() { env -u IMAGE_REPO bash "${MIR}" login someone; }
expect_fail "mirror login without IMAGE_REPO fails" "IMAGE_REPO is not set" t_mir_login
t_img_base() { IMAGE_REPO=registry.example.com/x/y BASE_IMAGE=docker.io/library/alpine:3 bash "${IMG}"; }
expect_fail "image script rejects a base image without digest" "pinned by digest" t_img_base

# ---- (g) help works without IMAGE_REPO -----------------------------------------------------------------------------------
t_help() { env -u IMAGE_REPO bash "$1" help; }
expect_ok "build-image-crane.sh help" t_help "${IMG}"
expect_ok "build-collector-image-crane.sh help" t_help "${COL}"
expect_ok "build-mirror-image-crane.sh help" t_help "${MIR}"

# ---- (h) label loader ----------------------------------------------------------------------------------------------------
# bash -c snippet: the caller defines die/warn/log and ROOT before sourcing, like the scripts do
t_labels() { # file
  IMAGE_LABELS_FILE="$1" bash -c '
    die()  { printf "ERROR: %s\n" "$*" >&2; exit 1; }
    warn() { :; }
    log()  { :; }
    ROOT="$1"
    . "$1/scripts/lib/image-common.sh"
    image_labels_load
    printf "%s|%s|%s|%s|%s|%s\n" "${LABEL_VENDOR}" "${LABEL_LICENSES}" "${LABEL_LICENSES_MIRROR}" "${LABEL_TITLE}" "${LABEL_TITLE_COLLECTOR}" "${LABEL_TITLE_MIRROR}"
  ' _ "${REPO}"
}
expect_ok "the real ci/image-labels.env loads" t_labels "${REPO}/ci/image-labels.env"
if OUT="$(t_labels "${REPO}/ci/image-labels.env" 2>&1)" && [ "${OUT%%|*}" = "$(sed -n 's/^LABEL_VENDOR=//p' "${REPO}/ci/image-labels.env")" ]; then ok "loaded values match the file"; else fail "loaded values do not match the file: ${OUT}"; fi

good="${W}/labels-good.env"
sed -e '/^#/d' -e '/^$/d' "${REPO}/ci/image-labels.env" > "${good}"
expect_ok "a file without comments and blank lines loads" t_labels "${good}"

{ cat "${good}"; echo "# trailing comment"; echo; } > "${W}/labels-extra.env"
expect_ok "comments and blank lines are skipped" t_labels "${W}/labels-extra.env"

sed 's/^LABEL_VENDOR=.*/LABEL_VENDOR=one,two/' "${good}" > "${W}/labels-comma.env"
expect_fail "a value with a comma is rejected (with file and line)" "labels-comma\\.env:[0-9]+:.*comma" t_labels "${W}/labels-comma.env"

{ cat "${good}"; echo "IMAGE_REPO=x"; } > "${W}/labels-unknown.env"
expect_fail "an unknown key is rejected (with file and line)" "labels-unknown\\.env:[0-9]+:.*unknown key" t_labels "${W}/labels-unknown.env"

{ cat "${good}"; echo "not a key value line"; } > "${W}/labels-malformed.env"
expect_fail "a malformed line is rejected (with file and line)" "labels-malformed\\.env:[0-9]+:" t_labels "${W}/labels-malformed.env"

grep -v '^LABEL_LICENSES_MIRROR=' "${good}" > "${W}/labels-nomirrorlic.env"
expect_fail "a missing mirror licenses label is rejected" "LABEL_LICENSES_MIRROR is missing" t_labels "${W}/labels-nomirrorlic.env"

grep -v '^LABEL_TITLE_MIRROR=' "${good}" > "${W}/labels-missing.env"
expect_fail "a missing required key is rejected" "LABEL_TITLE_MIRROR" t_labels "${W}/labels-missing.env"

sed 's/^LABEL_LICENSES=.*/LABEL_LICENSES=/' "${good}" > "${W}/labels-empty.env"
expect_fail "an empty required value is rejected" "LABEL_LICENSES" t_labels "${W}/labels-empty.env"

# ---- (i) source URL ------------------------------------------------------------------------------------------------------
t_source() { # dir [SOURCE_URL] [LABEL_SOURCE]: prints image_source_url
  SRC_ENV="${2:-}" FALLBACK="${3:-}" bash -c '
    die()  { printf "ERROR: %s\n" "$*" >&2; exit 1; }
    warn() { :; }
    log()  { :; }
    ROOT="$1"
    . "$1/scripts/lib/image-common.sh"
    unset SOURCE_URL
    if [ -n "${SRC_ENV}" ]; then SOURCE_URL="${SRC_ENV}"; fi
    LABEL_SOURCE="${FALLBACK}"
    image_source_url "$2"
  ' _ "${REPO}" "$1"
}
GR="${W}/gitrepo"
mkdir -p "${GR}"
git -C "${GR}" init -q
want_url="https://git.example.com/group/project"
check_remote() { # name remote expected [fallback]
  local name="$1" remote="$2" expect="$3" fb="${4:-}" got
  git -C "${GR}" remote remove origin >/dev/null 2>&1 || true
  git -C "${GR}" remote add origin "${remote}"
  got="$(t_source "${GR}" "" "${fb}" 2>&1)"
  if [ "${got}" = "${expect}" ]; then ok "source URL: ${name}"; else fail "source URL: ${name}: expected '${expect}', got '${got}'"; fi
}
check_remote "scp-like remote" "git@git.example.com:group/project.git" "${want_url}"
check_remote "ssh:// remote with port" "ssh://git@git.example.com:2222/group/project.git" "${want_url}"
check_remote "https remote with credentials" "https://user:secret@git.example.com/group/project.git" "${want_url}"
check_remote "https remote with user only" "https://user@git.example.com/group/project.git" "${want_url}"
check_remote "https remote with trailing .git" "https://git.example.com/group/project.git" "${want_url}"
check_remote "https remote with trailing slash" "https://git.example.com/group/project/" "${want_url}"
check_remote "no host rewriting for ssh-style host names" "git@git.ssh.example.com:group/project.git" "https://git.ssh.example.com/group/project"
check_remote "local path falls back to LABEL_SOURCE" "${W}/some/local/path" "https://example.org/fallback" "https://example.org/fallback"
check_remote "file:// remote falls back to LABEL_SOURCE" "file:///srv/git/project.git" "https://example.org/fallback" "https://example.org/fallback"
check_remote "http:// remote falls back to LABEL_SOURCE" "http://git.example.com/group/project.git" "https://example.org/fallback" "https://example.org/fallback"
check_remote "local path without LABEL_SOURCE yields nothing" "${W}/some/local/path" ""
if [ "$(t_source "${GR}" "https://given.example.com/org/repo.git" "https://example.org/fallback" 2>&1)" = "https://given.example.com/org/repo.git" ]; then
  ok "SOURCE_URL wins over the remote and is used as given"
else
  fail "SOURCE_URL does not win over the remote"
fi
expect_fail_src() { t_source "${GR}" "https://example.com/a,b"; }
expect_fail "SOURCE_URL with a comma is rejected" "SOURCE_URL must not contain a comma" expect_fail_src
NG="${W}/norepo"
mkdir -p "${NG}"
git -C "${NG}" init -q
if [ "$(t_source "${NG}" "" "https://example.org/fallback" 2>&1)" = "https://example.org/fallback" ]; then ok "no remote falls back to LABEL_SOURCE"; else fail "no remote does not fall back to LABEL_SOURCE"; fi

# ---- repo / digest helpers directly --------------------------------------------------------------------------------------
t_helper() { # snippet
  bash -c '
    die()  { printf "ERROR: %s\n" "$*" >&2; exit 1; }
    warn() { :; }
    log()  { :; }
    ROOT="$1"
    . "$1/scripts/lib/image-common.sh"
    eval "$2"
  ' _ "${REPO}" "$1"
}
expect_ok "image_require_repo accepts registry:port/path" t_helper 'IMAGE_REPO=localhost:5000/myriarch; image_require_repo x y'
expect_ok "image_require_repo accepts a nested path" t_helper 'IMAGE_REPO=registry.example.com/org/team/name; image_require_repo x y'
expect_fail "image_require_repo rejects a path-less name" "invalid IMAGE_REPO" t_helper 'IMAGE_REPO=registry.example.com; image_require_repo x y'
expect_ok "image_require_digest_pin accepts repo:tag@sha256" t_helper "image_require_digest_pin BASE_IMAGE repo:tag@${DIGEST}"
expect_fail "image_require_digest_pin rejects a short digest" "BASE_IMAGE must be pinned by digest" t_helper 'image_require_digest_pin BASE_IMAGE repo@sha256:abc'
t_dc_unset() { env -u DOCKER_CONFIG bash -c '
    die() { exit 1; }
    ROOT="$1"
    . "$1/scripts/lib/image-common.sh"
    image_docker_config_prepare
    [ -z "${DOCKER_CONFIG:-}" ] && [ "$(image_docker_config_desc)" = "crane default (~/.docker)" ]
  ' _ "${REPO}"; }
expect_ok "DOCKER_CONFIG stays unset when the caller does not set it" t_dc_unset

# ---- (j0) IMAGE_PLATFORM --------------------------------------------------------------------------------------------------
t_img_plat() { IMAGE_PLATFORM=linux/s390x IMAGE_REPO=registry.example.com/x/y bash "${IMG}"; }
expect_fail "image script rejects an unsupported IMAGE_PLATFORM" "unsupported IMAGE_PLATFORM 'linux/s390x'" t_img_plat
t_img_plat_os() { IMAGE_PLATFORM=windows/amd64 IMAGE_REPO=registry.example.com/x/y bash "${IMG}"; }
expect_fail "image script rejects a non-linux IMAGE_PLATFORM" "unsupported IMAGE_PLATFORM 'windows/amd64'" t_img_plat_os
t_col_plat() { IMAGE_PLATFORM=linux/riscv64 IMAGE_REPO=registry.example.com/x/y bash "${COL}" 0.162.0; }
expect_fail "collector script rejects an unsupported IMAGE_PLATFORM" "unsupported IMAGE_PLATFORM 'linux/riscv64'" t_col_plat
t_plat_vars() { # platform -> "GOARCH machine suffix"
  env -u IMAGE_PLATFORM ${1:+IMAGE_PLATFORM="$1"} bash -c '
    die() { printf "ERROR: %s\n" "$*" >&2; exit 1; }
    ROOT="$1"
    . "$1/scripts/lib/image-common.sh"
    image_platform_check
    printf "%s %s %s %s" "${IMAGE_PLATFORM}" "${IMAGE_GOARCH}" "${IMAGE_ELF_MACHINE}" "${IMAGE_TAG_SUFFIX:-none}"
  ' _ "${REPO}"
}
if [ "$(t_plat_vars)" = "linux/amd64 amd64 62 none" ]; then ok "IMAGE_PLATFORM defaults to linux/amd64 (GOARCH amd64, ELF machine 62, no tag suffix)"; else fail "IMAGE_PLATFORM default: $(t_plat_vars)"; fi
if [ "$(t_plat_vars linux/arm64)" = "linux/arm64 arm64 183 -arm64" ]; then ok "IMAGE_PLATFORM=linux/arm64 maps to GOARCH arm64, ELF machine 183 and the tag suffix -arm64"; else fail "IMAGE_PLATFORM arm64: $(t_plat_vars linux/arm64)"; fi
# the scripts must use the variables and not a hard-coded architecture in their build steps
if grep -nE 'GOARCH=amd64|--platform linux/amd64|"architecture": "amd64"' "${IMG}" "${COL}" >/dev/null 2>&1; then
  fail "a crane build script hard-codes amd64 outside IMAGE_PLATFORM"
else
  ok "the crane build scripts do not hard-code amd64 in their build steps"
fi

# ---- (j) static: no DOCKER_CONFIG default ---------------------------------------------------------------------------------
# shellcheck disable=SC2016  # the pattern is literal text of the scripts
if grep -n 'DOCKER_CONFIG="${DOCKER_CONFIG:-' "${REPO}"/scripts/build-*-crane.sh >/dev/null 2>&1; then
  fail "a crane script assigns a default to DOCKER_CONFIG"
else
  ok "no crane script assigns a default to DOCKER_CONFIG"
fi
if grep -nE 'export DOCKER_CONFIG' "${REPO}"/scripts/build-*-crane.sh "${COMMON}" >/dev/null 2>&1; then
  fail "a crane script exports DOCKER_CONFIG"
else
  ok "no crane script exports DOCKER_CONFIG"
fi

# ---- (k) image variants and license files (no network, nothing is built) -------------------------------------------------
# `layout` prints the sorted archive paths of the files of the layer except the binaries.
t_layout() { env -u IMAGE_REPO -u IMAGE_VARIANT ${1:+IMAGE_VARIANT="$1"} bash "${IMG}" layout; }
DEV_LAYOUT="$(t_layout 2>&1)"
REL_LAYOUT="$(t_layout release 2>&1)"
EXPLICIT_DEV_LAYOUT="$(t_layout dev 2>&1)"
for want in usr/share/licenses/myriarch/LICENSE usr/share/licenses/myriarch/THIRD_PARTY_NOTICES opt/myriarch/examples/quickstart/README.md opt/myriarch/examples/sim/mixed-fleet.yaml; do
  if printf '%s\n' "${DEV_LAYOUT}" | grep -qx -- "${want}"; then ok "layout (default variant dev) contains ${want}"; else fail "layout (default variant dev) lacks ${want}"; fi
  if printf '%s\n' "${REL_LAYOUT}" | grep -qx -- "${want}"; then ok "layout (release) contains ${want}"; else fail "layout (release) lacks ${want}"; fi
done
if printf '%s\n' "${DEV_LAYOUT}" | grep -q '^opt/myriarch/release-fixtures/'; then ok "layout (dev) contains opt/myriarch/release-fixtures/..."; else fail "layout (dev) lacks the release fixtures"; fi
if printf '%s\n' "${REL_LAYOUT}" | grep -qi 'fixtures'; then fail "layout (release) contains release fixtures"; else ok "layout (release) contains no release fixtures"; fi
if [ "${DEV_LAYOUT}" = "${EXPLICIT_DEV_LAYOUT}" ]; then ok "the default variant is dev"; else fail "the default variant differs from IMAGE_VARIANT=dev"; fi
if printf '%s\n' "${DEV_LAYOUT}" | LC_ALL=C sort -c 2>/dev/null; then ok "layout is sorted"; else fail "layout is not sorted"; fi
if printf '%s\n' "${DEV_LAYOUT}" "${REL_LAYOUT}" | grep -q '^/\|^usr/local/bin/'; then fail "layout lists absolute paths or binaries"; else ok "layout lists no absolute paths and no binaries"; fi
t_layout_badvariant() { env -u IMAGE_REPO IMAGE_VARIANT=bogus bash "${IMG}" layout; }
expect_fail "layout rejects an invalid IMAGE_VARIANT" "invalid IMAGE_VARIANT 'bogus'" t_layout_badvariant
t_build_badvariant() { IMAGE_VARIANT=Release IMAGE_REPO=registry.example.com/x/y bash "${IMG}"; }
expect_fail "build rejects an invalid IMAGE_VARIANT before anything else" "invalid IMAGE_VARIANT 'Release'" t_build_badvariant
# the release variant must not build the devpg tag, the dev variant must
# shellcheck disable=SC2016  # the pattern is literal text of the script
if grep -q 'release) MYRIARCHD_TAGS="embedui" ;;' "${IMG}" && grep -q 'dev) MYRIARCHD_TAGS="embedui,devpg" ;;' "${IMG}"; then
  ok "the dev variant builds with embedui,devpg and the release variant with embedui only"
else
  fail "the build tags of the image variants changed"
fi
# the dev variant (the default) is refused for the published repository, also in a dry run; the release variant is not
t_pub_default() { env -u IMAGE_VARIANT IMAGE_REPO=ghcr.io/myriarch/myriarch DRY_RUN=1 bash "${IMG}"; }
expect_fail "the published repository refuses the default (dev) variant" "takes the release variant only" t_pub_default
t_pub_dev() { IMAGE_VARIANT=dev IMAGE_REPO=ghcr.io/myriarch/myriarch-sim DRY_RUN=1 bash "${IMG}"; }
expect_fail "the published repository refuses IMAGE_VARIANT=dev" "takes the release variant only" t_pub_dev
t_pub_case() { env -u IMAGE_VARIANT IMAGE_REPO=GHCR.io/MyriArch/myriarch DRY_RUN=1 bash "${IMG}"; }
expect_fail "... also with another spelling of the repository" "takes the release variant only" t_pub_case
# (the variant check alone: a full dry run would build the image and write into .cache/image)
t_other_default() { bash -c 'IMAGE_VARIANT=dev; IMAGE_REPO=ghcr.io/other/myriarch; die() { echo "$*"; exit 1; }; eval "$(sed -n "/^image_variant_check()/,/^}/p" "$1")"; image_variant_check && echo accepted' _ "${IMG}"; }
if printf '%s' "$(t_other_default 2>&1)" | grep -qx accepted; then ok "another repository keeps the dev default"; else fail "another repository must not be restricted"; fi

# collector image: upstream images are pinned and verified by default when the build pushes
FAKE_CRANE="${W}/crane-stub"; printf '#!/bin/sh\nexit 1\n' > "${FAKE_CRANE}"; chmod 0755 "${FAKE_CRANE}"
t_col_push() { env -u VERIFY_UPSTREAM -u SUPERVISOR_IMAGE -u CONTRIB_IMAGE IMAGE_REPO=registry.example.com/x/y CRANE="${FAKE_CRANE}" bash "${COL}" 0.162.0; }
expect_fail "a build that pushes verifies the upstream images by default" "VERIFY_UPSTREAM=1" t_col_push
t_col_pinned() { env -u VERIFY_UPSTREAM -u SUPERVISOR_IMAGE -u CONTRIB_IMAGE IMAGE_REPO=registry.example.com/x/y CRANE="${FAKE_CRANE}" bash "${COL}" 0.162.0; }
expect_fail "... with the reviewed digests of ci/versions.env as images" "opampsupervisor:0.162.0@sha256:[0-9a-f]{64}, contrib otel/opentelemetry-collector-contrib:0.162.0@sha256:[0-9a-f]{64}" t_col_pinned
t_col_dry() { env -u VERIFY_UPSTREAM IMAGE_REPO=registry.example.com/x/y DRY_RUN=1 CRANE="${FAKE_CRANE}" bash "${COL}" 0.162.0; }
expect_fail "a dry run does not verify unless asked" "VERIFY_UPSTREAM=0" t_col_dry
t_col_off() { IMAGE_REPO=registry.example.com/x/y VERIFY_UPSTREAM=0 CRANE="${FAKE_CRANE}" bash "${COL}" 0.162.0; }
expect_fail "VERIFY_UPSTREAM=0 switches it off explicitly" "VERIFY_UPSTREAM=0" t_col_off
t_col_tag() { IMAGE_REPO=registry.example.com/x/y SUPERVISOR_IMAGE=otel/opentelemetry-collector-opampsupervisor:0.162.0 CRANE="${FAKE_CRANE}" bash "${COL}" 0.162.0; }
expect_fail "an unpinned image override is refused" "must be pinned by digest" t_col_tag
t_col_tag_ok() { IMAGE_REPO=registry.example.com/x/y ALLOW_UNPINNED_IMAGES=1 SUPERVISOR_IMAGE=otel/opentelemetry-collector-opampsupervisor:0.162.0 CRANE="${FAKE_CRANE}" bash "${COL}" 0.162.0; }
if OUT="$(t_col_tag_ok 2>&1)"; then fail "unexpected success"; elif printf '%s' "${OUT}" | grep -q "must be pinned by digest"; then fail "ALLOW_UNPINNED_IMAGES=1 does not allow a tag"; else ok "ALLOW_UNPINNED_IMAGES=1 allows a tag for a lab build"; fi

# the collector Dockerfile builds from the reviewed digests
for arg in COLLECTOR_IMAGE SUPERVISOR_IMAGE GO_IMAGE; do
  if grep -Eq "^ARG ${arg}=[^ ]+@sha256:[0-9a-f]{64}\$" "${COL_DOCKERFILE:-${REPO}/deploy/k8s/collector/Dockerfile}"; then ok "collector Dockerfile: ${arg} is pinned by digest"; else fail "collector Dockerfile: ${arg} is not pinned by digest"; fi
done

# the collector image: license files of the project and of the OpenTelemetry software
COL_LAYOUT="$(env -u IMAGE_REPO bash "${COL}" layout 2>&1)"
for want in usr/share/licenses/myriarch/LICENSE usr/share/licenses/myriarch/THIRD_PARTY_NOTICES usr/share/licenses/opentelemetry-collector/LICENSE usr/share/licenses/opentelemetry-collector/README; do
  if printf '%s\n' "${COL_LAYOUT}" | grep -qx -- "${want}"; then ok "collector layout contains ${want}"; else fail "collector layout lacks ${want}"; fi
done
# and the Dockerfile of the collector image (same contract) names the same four files
COL_DOCKERFILE="${REPO}/deploy/k8s/collector/Dockerfile"
for want in /usr/share/licenses/myriarch/LICENSE /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES /usr/share/licenses/opentelemetry-collector/LICENSE /usr/share/licenses/opentelemetry-collector/README; do
  if grep -q -- "${want}" "${COL_DOCKERFILE}"; then ok "collector Dockerfile mentions ${want}"; else fail "collector Dockerfile lacks ${want}"; fi
done
# the generated README names what it covers, in the script and in the Dockerfile
for f in "${COL}" "${COL_DOCKERFILE}"; do
  for want in "Copyright The OpenTelemetry Authors" "Apache-2.0" "opampsupervisor" "otelcol-contrib" "cmd/opampsupervisor/v"; do
    if grep -q -- "${want}" "${f}"; then ok "$(basename "${f}") README text names '${want}'"; else fail "$(basename "${f}") README text lacks '${want}'"; fi
  done
done

# ---- (l) the published repositories are not written from this work tree ---------------------------------------------------
t_pub_push() { IMAGE_VARIANT=release IMAGE_REPO=ghcr.io/myriarch/myriarch bash "${IMG}"; }
expect_fail "a build from source refuses to push to the published repository" "is a published repository" t_pub_push
t_pub_push_case() { IMAGE_VARIANT=release IMAGE_REPO=GHCR.io/myriarch/myriarch-updater bash "${IMG}"; }
expect_fail "... also with another spelling of the host" "is a published repository" t_pub_push_case

# ---- (m) release images from the release archives (IMAGE_SOURCE=dist) ---------------------------------------------------
# Stand-ins for the release archives of goreleaser: small static linux binaries built from this repository (myrctl for
# myriarchd: same embedded roots, time zone database and build tags; the real myriarch-updater), archives with the files of
# the real ones, checksums.txt and metadata.json. Nothing is pushed anywhere in this section except to a registry on 127.0.0.1.
TOOLS_BIN="${TOOLS_BIN:-${REPO}/.cache/bin}"
export TOOLS_BIN
"${REPO}/ci/install-tools.sh" crane >/dev/null 2>&1
CRANE="${TOOLS_BIN}/crane"
RV="9.9.9-test.1"
EPOCH=1700000000
VERSION_PKG_FULL="github.com/myriarch/myriarch/internal/version"
BIN="${W}/bin"
mkdir -p "${BIN}/amd64" "${BIN}/arm64"
build_bin() { # out goarch package tags version
  ( cd "${REPO}" && CGO_ENABLED=0 GOOS=linux GOARCH="$2" go build -trimpath ${4:+-tags "$4"} \
      -ldflags "-s -w -X ${VERSION_PKG_FULL}.Version=${5:-${RV}}" -o "$1" "$3" ) >"${W}/build.log" 2>&1 || { cat "${W}/build.log" >&2; return 1; }
}
built=1
for arch in amd64 arm64; do
  build_bin "${BIN}/${arch}/myriarchd" "${arch}" ./cmd/myrctl embedui || built=0
  build_bin "${BIN}/${arch}/myriarch-updater" "${arch}" ./cmd/myriarch-updater || built=0
done
build_bin "${BIN}/amd64/devpg" amd64 ./cmd/myrctl embedui,devpg || built=0
build_bin "${BIN}/amd64/noui" amd64 ./cmd/myrctl "" || built=0
build_bin "${BIN}/amd64/oldversion" amd64 ./cmd/myrctl embedui 1.0.0 || built=0
mkdir -p "${W}/noroots"
printf 'package main\n\nfunc main() {}\n' > "${W}/noroots/main.go"
( cd "${W}/noroots" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "${BIN}/amd64/noroots" main.go ) >"${W}/build.log" 2>&1 || built=0
if [ "${built}" = 1 ]; then ok "stand-in binaries for the release archives are built"; else fail "stand-in binaries could not be built"; fi

cat > "${W}/mkdist.py" <<'PY'
import hashlib, io, json, os, sys, tarfile

dist, version, commit, name = sys.argv[1:5]
amd64, arm64 = sys.argv[5:7]
repo = sys.argv[7]
extra = sys.argv[8:]  # "omit=LICENSE" drops a file from the archives
omit = [e.split("=", 1)[1] for e in extra if e.startswith("omit=")]
os.makedirs(dist, exist_ok=True)
sums = []
for arch, binary in (("amd64", amd64), ("arm64", arm64)):
    archive = "%s_%s_linux_%s.tar.gz" % (name, version, arch)
    with tarfile.open(os.path.join(dist, archive), "w:gz") as tar:
        def add(arcname, data, mode):
            ti = tarfile.TarInfo(arcname)
            ti.size = len(data)
            ti.mode = mode
            ti.mtime = 1700000000
            tar.addfile(ti, io.BytesIO(data))
        add(name, open(binary, "rb").read(), 0o755)
        for f in ("LICENSE", "THIRD_PARTY_NOTICES"):
            if f not in omit:
                add(f, open(os.path.join(repo, f), "rb").read(), 0o644)
        add("README.md", b"readme\n", 0o644)
    sums.append("%s  %s\n" % (hashlib.sha256(open(os.path.join(dist, archive), "rb").read()).hexdigest(), archive))
open(os.path.join(dist, "checksums.txt"), "w").write("".join(sums))
json.dump({"project_name": "myriarch", "version": version, "commit": commit}, open(os.path.join(dist, "metadata.json"), "w"))
PY
HEAD_SHA="$(git -C "${REPO}" rev-parse HEAD)"
mkdist() { # dist name amd64-binary arm64-binary [version] [extra]
  python3 -I "${W}/mkdist.py" "$1" "${5:-${RV}}" "${HEAD_SHA}" "$2" "$3" "$4" "${REPO}" "${@:6}"
}
mkdist "${W}/dist-ok-server" myriarchd "${BIN}/amd64/myriarchd" "${BIN}/arm64/myriarchd"
mkdist "${W}/dist-ok-updater" myriarch-updater "${BIN}/amd64/myriarch-updater" "${BIN}/arm64/myriarch-updater"

run_dist() { # kind dist out [VAR=value ...]
  local kind="$1" dist="$2" out="$3"; shift 3
  env -u IMAGE_REPO -u IMAGE_RELEASE_VERSION -u SOURCE_URL -u IMAGE_PLATFORMS -u OUT_DIR -u SYFT \
    IMAGE_SOURCE=dist IMAGE_KIND="${kind}" DIST_DIR="${dist}" OUT_DIR="${out}" ALLOW_DIRTY=1 SOURCE_DATE_EPOCH="${EPOCH}" "$@" bash "${IMG}"
}
value_of() { sed -n "s/^$1=//p" <<<"$2"; }

# the contract of a built layout: platforms, user, entrypoint, one layer, no shell, licenses, labels, determinism of the layer
check_layout() { # layout kind amd64-binary arm64-binary
  python3 -I - "$1" "$2" "$3" "$4" "${REPO}" "${EPOCH}" "${RV}" <<'PY'
import gzip, hashlib, io, json, os, sys, tarfile

layout, kind, bin_amd64, bin_arm64, repo, epoch, version = sys.argv[1:8]
epoch = int(epoch)
binaries = {"amd64": bin_amd64, "arm64": bin_arm64}
name = {"server": "myriarchd", "updater": "myriarch-updater"}[kind]
errs = []

def blob(d):
    return open(os.path.join(layout, "blobs", "sha256", d[7:]), "rb").read()

raw_index = open(os.path.join(layout, "index.json"), "rb").read()
index = json.loads(raw_index)
if sorted(m["platform"]["architecture"] for m in index["manifests"]) != ["amd64", "arm64"]:
    errs.append("platforms")
if any(m["platform"]["os"] != "linux" for m in index["manifests"]):
    errs.append("os")
ann = index.get("annotations", {})
for k, v in {"org.opencontainers.image.source": "https://github.com/myriarch/myriarch",
             "org.opencontainers.image.licenses": "Apache-2.0", "org.opencontainers.image.version": version,
             "org.opencontainers.image.vendor": "The Myriarch Authors"}.items():
    if ann.get(k) != v:
        errs.append("index annotation %s=%r" % (k, ann.get(k)))
for k in ("title", "description", "revision", "created"):
    if not ann.get("org.opencontainers.image." + k):
        errs.append("index annotation " + k)
want_entries = {"etc", "etc/group", "etc/passwd", "tmp", "usr", "usr/local", "usr/local/bin", "usr/local/bin/" + name, "usr/share",
                "usr/share/licenses", "usr/share/licenses/myriarch", "usr/share/licenses/myriarch/LICENSE",
                "usr/share/licenses/myriarch/THIRD_PARTY_NOTICES"}
for m in index["manifests"]:
    arch = m["platform"]["architecture"]
    man = json.loads(blob(m["digest"]))
    if len(man["layers"]) != 1:
        errs.append("%s: %d layers" % (arch, len(man["layers"])))
    cfg = json.loads(blob(man["config"]["digest"]))
    c = cfg["config"]
    if cfg["architecture"] != arch or cfg["os"] != "linux":
        errs.append("%s: config platform" % arch)
    if c["User"] != "65532:65532":
        errs.append("%s: user %r" % (arch, c["User"]))
    if c["Entrypoint"] != ["/usr/local/bin/" + name]:
        errs.append("%s: entrypoint %r" % (arch, c["Entrypoint"]))
    if kind == "server" and c.get("Cmd") != ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"]:
        errs.append("%s: cmd %r" % (arch, c.get("Cmd")))
    if kind == "updater" and c.get("Cmd"):
        errs.append("%s: cmd %r" % (arch, c.get("Cmd")))
    if "Shell" in c or "Healthcheck" in c:
        errs.append("%s: shell or healthcheck set" % arch)
    for k, v in {"org.opencontainers.image.source": "https://github.com/myriarch/myriarch", "org.opencontainers.image.licenses": "Apache-2.0",
                 "org.opencontainers.image.version": version, "org.opencontainers.image.title": name if kind == "updater" else "myriarch",
                 "org.opencontainers.image.base.name": "scratch", "dev.myriarch.image.variant": "release"}.items():
        if c["Labels"].get(k) != v:
            errs.append("%s: label %s=%r" % (arch, k, c["Labels"].get(k)))
    for k in ("org.opencontainers.image.revision", "org.opencontainers.image.created", "org.opencontainers.image.description"):
        if not c["Labels"].get(k):
            errs.append("%s: label %s" % (arch, k))
    for k, v in ann.items():
        if c["Labels"].get(k) != v:
            errs.append("%s: index annotation %s differs from the label of the platform config" % (arch, k))
    raw = gzip.decompress(blob(man["layers"][0]["digest"]))
    if cfg["rootfs"]["diff_ids"] != ["sha256:" + hashlib.sha256(raw).hexdigest()]:
        errs.append("%s: diff id" % arch)
    tf = tarfile.open(fileobj=io.BytesIO(raw))
    members = {mem.name: mem for mem in tf.getmembers()}
    if set(members) != want_entries:
        errs.append("%s: layer entries %s" % (arch, sorted(set(members) ^ want_entries)))
        continue
    for mem in members.values():
        if (mem.uid, mem.gid, mem.mtime) != (0, 0, epoch):
            errs.append("%s: %s owner or mtime" % (arch, mem.name))
    if members["tmp"].mode != 0o1777 or not members["tmp"].isdir():
        errs.append("%s: /tmp" % arch)
    if members["usr/local/bin/" + name].mode != 0o755:
        errs.append("%s: binary mode" % arch)
    if tf.extractfile(members["usr/local/bin/" + name]).read() != open(binaries[arch], "rb").read():
        errs.append("%s: the binary is not the one of the archive" % arch)
    for f in ("LICENSE", "THIRD_PARTY_NOTICES"):
        mem = members["usr/share/licenses/myriarch/" + f]
        if mem.mode != 0o644 or tf.extractfile(mem).read() != open(os.path.join(repo, f), "rb").read():
            errs.append("%s: %s" % (arch, f))
    passwd = tf.extractfile(members["etc/passwd"]).read().decode()
    group = tf.extractfile(members["etc/group"]).read().decode()
    if "myriarch:x:65532:65532:" not in passwd or "myriarch:x:65532:" not in group:
        errs.append("%s: passwd or group" % arch)
    if any(n.endswith(("/sh", "/bash", "/busybox")) for n in members):
        errs.append("%s: a shell" % arch)
print("\n".join(errs) if errs else "contract ok")
sys.exit(1 if errs else 0)
PY
}

if [ "${built}" = 1 ]; then
  # --- the server image: contract, outputs, determinism
  S1="$(run_dist server "${W}/dist-ok-server" "${W}/out1" IMAGE_SBOM=0 2>"${W}/s1.err")"; rc=$?
  if [ "${rc}" = 0 ]; then ok "dist mode builds the server image"; else fail "dist mode (server) failed: $(cat "${W}/s1.err")"; fi
  IDX1="$(value_of IMAGE_INDEX_DIGEST "${S1}")"
  [[ "${IDX1}" =~ ^sha256:[0-9a-f]{64}$ ]] && ok "the output names the index digest" || fail "no index digest in: ${S1}"
  if [ "sha256:$(sha256_of "${W}/out1/server/index.json")" = "${IDX1}" ]; then ok "the index digest is the SHA-256 of index.json"; else fail "the index digest is not the SHA-256 of index.json"; fi
  for p in LINUX_AMD64 LINUX_ARM64; do
    [[ "$(value_of "IMAGE_MANIFEST_DIGEST_${p}" "${S1}")" =~ ^sha256:[0-9a-f]{64}$ ]] && ok "the output names the manifest digest of ${p}" || fail "no manifest digest for ${p}"
  done
  [ "$(value_of IMAGE_LAYOUT "${S1}")" = "${W}/out1/server" ] && [ -f "${W}/out1/server.env" ] && ok "the layout and the .env file are in OUT_DIR" || fail "layout or .env file missing"
  CL="$(check_layout "${W}/out1/server" server "${BIN}/amd64/myriarchd" "${BIN}/arm64/myriarchd" 2>&1)"
  if [ "${CL}" = "contract ok" ]; then ok "server image: platforms, user 65532:65532, entrypoint, one layer, no shell, licenses, labels, annotations"; else fail "server image contract: ${CL}"; fi
  S2="$(run_dist server "${W}/dist-ok-server" "${W}/out2" IMAGE_SBOM=0 2>/dev/null)"
  if [ "$(value_of IMAGE_INDEX_DIGEST "${S2}")" = "${IDX1}" ] && diff -r "${W}/out1/server" "${W}/out2/server" >/dev/null; then ok "two builds give the same index digest and the same bytes"; else fail "dist mode is not deterministic"; fi
  S3="$(run_dist server "${W}/dist-ok-server" "${W}/out3" IMAGE_SBOM=0 SOURCE_DATE_EPOCH=1700000001 2>/dev/null)"
  [ "$(value_of IMAGE_INDEX_DIGEST "${S3}")" != "${IDX1}" ] && ok "another commit time gives another digest (the time is an input, not the clock)" || fail "SOURCE_DATE_EPOCH does not reach the image"
  S4="$(run_dist server "${W}/dist-ok-server" "${W}/out1" IMAGE_SBOM=0 2>/dev/null)"
  [ "$(value_of IMAGE_INDEX_DIGEST "${S4}")" = "${IDX1}" ] && ok "a build into an existing OUT_DIR replaces the old layout" || fail "rebuilding into the same OUT_DIR failed"
  SU="$(run_dist updater "${W}/dist-ok-updater" "${W}/out1" IMAGE_SBOM=0 2>"${W}/u1.err")"; rc=$?
  if [ "${rc}" = 0 ]; then ok "dist mode builds the updater image"; else fail "dist mode (updater) failed: $(cat "${W}/u1.err")"; fi
  CL="$(check_layout "${W}/out1/updater" updater "${BIN}/amd64/myriarch-updater" "${BIN}/arm64/myriarch-updater" 2>&1)"
  if [ "${CL}" = "contract ok" ]; then ok "updater image: platforms, user 65532:65532, entrypoint, one layer, no shell, licenses, labels, annotations"; else fail "updater image contract: ${CL}"; fi
  [ "$(value_of IMAGE_INDEX_DIGEST "${SU}")" != "${IDX1}" ] && ok "the two images have different digests" || fail "server and updater image have the same digest"
  t_one_platform() { run_dist server "${W}/dist-ok-server" "${W}/out4" IMAGE_SBOM=0 IMAGE_PLATFORMS=linux/arm64; }
  if OUT="$(t_one_platform 2>&1)" && [ "$(python3 -I -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["manifests"]))' "${W}/out4/server/index.json")" = 1 ]; then ok "IMAGE_PLATFORMS limits the platforms"; else fail "IMAGE_PLATFORMS: ${OUT}"; fi

  # --- SBOMs: the pinned syft, without the update check (a stub records how it is called), then the real tool
  cat > "${W}/syft-stub" <<'STUB'
#!/bin/sh
# records the environment and the arguments, writes a minimal SPDX document to the file named by -o spdx-json=<file>
{ printf 'check=%s\n' "${SYFT_CHECK_FOR_APP_UPDATE:-unset}"; printf 'args=%s\n' "$*"; } >> "${SYFT_STUB_LOG}"
while [ "$#" -gt 0 ]; do
  case "$1" in -o) out="${2#spdx-json=}"; shift ;; esac
  shift
done
[ -z "${out:-}" ] || printf '{"spdxVersion":"SPDX-2.3","packages":[]}\n' > "${out}"
STUB
  chmod 0755 "${W}/syft-stub"
  SYFT_STUB_LOG="${W}/syft.log" run_dist server "${W}/dist-ok-server" "${W}/out5" SYFT="${W}/syft-stub" SYFT_STUB_LOG="${W}/syft.log" >"${W}/sbom.out" 2>&1
  if [ "$(grep -c '^check=false$' "${W}/syft.log" 2>/dev/null)" = 2 ] && grep -q "scan oci-dir:${W}/out5/server --platform linux/amd64" "${W}/syft.log" \
     && grep -q "scan oci-dir:${W}/out5/server --platform linux/arm64" "${W}/syft.log"; then
    ok "syft runs once per platform with SYFT_CHECK_FOR_APP_UPDATE=false"
  else
    fail "syft calls: $(cat "${W}/syft.log" 2>/dev/null)"
  fi
  if grep -q "^IMAGE_SBOM_LINUX_AMD64=${W}/out5/server.sbom/linux_amd64.spdx.json$" "${W}/sbom.out" && [ -s "${W}/out5/server.sbom/linux_arm64.spdx.json" ]; then ok "one SBOM file per platform is named in the output"; else fail "SBOM outputs: $(cat "${W}/sbom.out")"; fi
  t_syft_fail() { printf '#!/bin/sh\nexit 3\n' > "${W}/syft-fail"; chmod 0755 "${W}/syft-fail"; run_dist server "${W}/dist-ok-server" "${W}/out6" SYFT="${W}/syft-fail"; }
  expect_fail "a failing syft fails the build" "syft failed for linux/amd64" t_syft_fail
  if "${REPO}/ci/install-tools.sh" syft >/dev/null 2>&1; then
    SR="$(run_dist updater "${W}/dist-ok-updater" "${W}/out7" IMAGE_PLATFORMS=linux/amd64 2>"${W}/syft-real.err")"; rc=$?
    SB="$(value_of IMAGE_SBOM_LINUX_AMD64 "${SR}")"
    if [ "${rc}" = 0 ] && python3 -I - "${SB}" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
names = [p["name"] for p in d["packages"]]
ok = d["spdxVersion"].startswith("SPDX-2.") and any(n.startswith("golang.org/x/crypto") for n in names) and "ghcr.io/myriarch/myriarch-updater" in d["name"]
sys.exit(0 if ok else 1)
PY
    then ok "the pinned syft writes an SPDX SBOM that lists the Go modules of the updater"; else fail "real syft run (rc=${rc}): $(cat "${W}/syft-real.err")"; fi
  else
    fail "the pinned syft is not available (ci/install-tools.sh syft)"
  fi

  # --- failures of the inputs
  t_nokind() { run_dist "" "${W}/dist-ok-server" "${W}/outf"; }
  expect_fail "dist mode needs IMAGE_KIND" "IMAGE_KIND is not set" t_nokind
  t_badkind() { run_dist sim "${W}/dist-ok-server" "${W}/outf"; }
  expect_fail "dist mode rejects another kind" "invalid IMAGE_KIND 'sim'" t_badkind
  t_badplat() { run_dist server "${W}/dist-ok-server" "${W}/outf" IMAGE_PLATFORMS=linux/s390x; }
  expect_fail "dist mode rejects another platform" "unsupported platform 'linux/s390x'" t_badplat
  t_nodist() { run_dist server "${W}/does-not-exist" "${W}/outf"; }
  expect_fail "a missing dist directory is reported" "does not exist: run the release build first" t_nodist
  t_badver() { run_dist server "${W}/dist-ok-server" "${W}/outf" IMAGE_RELEASE_VERSION=9.9.9; }
  expect_fail "IMAGE_RELEASE_VERSION must equal the version of metadata.json" "differs from the version of" t_badver
  mkdist "${W}/dist-old" myriarchd "${BIN}/amd64/oldversion" "${BIN}/arm64/myriarchd"
  t_oldver() { run_dist server "${W}/dist-old" "${W}/outf"; }
  expect_fail "a binary of another release is refused" "does not contain the release version '${RV}'" t_oldver
  cp -R "${W}/dist-ok-server" "${W}/dist-sum" && printf 'x' >> "${W}/dist-sum/myriarchd_${RV}_linux_amd64.tar.gz"
  t_sum() { run_dist server "${W}/dist-sum" "${W}/outf"; }
  expect_fail "an archive that differs from checksums.txt is refused" "differs from checksums.txt" t_sum
  cp -R "${W}/dist-ok-server" "${W}/dist-nolist" && grep -v 'linux_arm64' "${W}/dist-ok-server/checksums.txt" > "${W}/dist-nolist/checksums.txt"
  t_nolist() { run_dist server "${W}/dist-nolist" "${W}/outf"; }
  expect_fail "an archive that checksums.txt does not list is refused" "does not list myriarchd_${RV}_linux_arm64.tar.gz" t_nolist
  mkdist "${W}/dist-arch" myriarchd "${BIN}/amd64/myriarchd" "${BIN}/amd64/myriarchd"
  t_arch() { run_dist server "${W}/dist-arch" "${W}/outf"; }
  expect_fail "a binary of the wrong architecture is refused" "not a linux/arm64 binary" t_arch
  mkdist "${W}/dist-noroots" myriarchd "${BIN}/amd64/noroots" "${BIN}/arm64/myriarchd"
  t_noroots() { run_dist server "${W}/dist-noroots" "${W}/outf"; }
  expect_fail "a binary without the embedded root CAs is refused" "does not embed the Mozilla root CAs" t_noroots
  mkdist "${W}/dist-devpg" myriarchd "${BIN}/amd64/devpg" "${BIN}/arm64/myriarchd"
  t_devpg() { run_dist server "${W}/dist-devpg" "${W}/outf"; }
  expect_fail "a server with the devpg tag is refused" "embedui without devpg" t_devpg
  mkdist "${W}/dist-noui" myriarchd "${BIN}/amd64/noui" "${BIN}/arm64/myriarchd"
  t_noui() { run_dist server "${W}/dist-noui" "${W}/outf"; }
  expect_fail "a server without the web UI is refused" "embedui without devpg" t_noui
  mkdist "${W}/dist-nolic" myriarchd "${BIN}/amd64/myriarchd" "${BIN}/arm64/myriarchd" "${RV}" omit=THIRD_PARTY_NOTICES
  t_nolic() { run_dist server "${W}/dist-nolic" "${W}/outf"; }
  expect_fail "an archive without a license file is refused" "lacks THIRD_PARTY_NOTICES" t_nolic
  python3 -I -c 'import json,sys; p=sys.argv[1]; d=json.load(open(p)); d["commit"]="0"*40; json.dump(d,open(p,"w"))' "${W}/dist-ok-server/metadata.json"
  t_commit() { run_dist server "${W}/dist-ok-server" "${W}/outf"; }
  expect_fail "archives of another commit are refused" "build the archives and the images from the same commit" t_commit
  python3 -I -c 'import json,sys; p=sys.argv[1]; d=json.load(open(p)); d["commit"]=sys.argv[2]; json.dump(d,open(p,"w"))' "${W}/dist-ok-server/metadata.json" "${HEAD_SHA}"
  # the strict mode of the release: the binary must record the clean commit it was built from
  t_strict() { python3 -I "${REPO}/scripts/lib/oci_image.py" dist --kind server --dist "${W}/dist-ok-server" --version "${RV}" --epoch "${EPOCH}" \
      --created 2023-11-14T22:13:20Z --uid 65532 --out "${W}/strict-out" --labels /dev/null --revision 0000000000000000000000000000000000000000 \
      --strict-vcs --platform linux/amd64; }
  expect_fail "the release mode requires the commit recorded in the binary" "was built from commit" t_strict
  if [ ! -e "${W}/strict-out" ]; then ok "a refused build leaves no layout behind"; else fail "a refused build left ${W}/strict-out"; fi
  t_published_dist() { run_dist server "${W}/dist-ok-server" "${W}/outf" IMAGE_SBOM=0 IMAGE_REPO=ghcr.io/myriarch/myriarch; }
  if OUT="$(t_published_dist 2>&1)"; then ok "dist mode builds without pushing, whatever IMAGE_REPO says"; else fail "dist mode with IMAGE_REPO: ${OUT}"; fi

  # --- push by digest to a registry on 127.0.0.1 (random port); only the process started here is stopped
  PUSH="${REPO}/ci/release/push-images.sh"
  PORT="$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
  "${CRANE}" registry serve --address "127.0.0.1:${PORT}" >"${W}/registry.log" 2>&1 &
  REG_PID=$!
  disown "${REG_PID}" 2>/dev/null || true
  stop_registry() { [ -z "${REG_PID:-}" ] || kill "${REG_PID}" >/dev/null 2>&1 || true; }
  trap 'stop_registry; cleanup_tests' EXIT
  for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:${PORT}/v2/" >/dev/null 2>&1 && break; sleep 0.1; done
  REG="127.0.0.1:${PORT}"
  push() { CRANE="${CRANE}" bash "${PUSH}" "$@"; }
  SRV_LAYOUT="${W}/out1/server"
  UPD_LAYOUT="${W}/out1/updater"
  UPD_DIGEST="$(value_of IMAGE_INDEX_DIGEST "${SU}")"
  PO="$(push "${SRV_LAYOUT}" "${REG}/myriarch/myriarch@${IDX1}" 2>"${W}/push.err")"; rc=$?
  if [ "${rc}" = 0 ] && [ "$(value_of IMAGE_PUSHED "${PO}")" = "${REG}/myriarch/myriarch@${IDX1}" ]; then ok "push by digest to a local registry"; else fail "push: ${PO} $(cat "${W}/push.err")"; fi
  [ "$(curl -fsS "http://${REG}/v2/myriarch/myriarch/tags/list" | tr -d ' \n')" = '{"name":"myriarch/myriarch","tags":null}' ] && ok "the push created no tag" || fail "tags after the push: $(curl -fsS "http://${REG}/v2/myriarch/myriarch/tags/list")"
  [ "$("${CRANE}" digest "${REG}/myriarch/myriarch@${IDX1}")" = "${IDX1}" ] && ok "the registry serves the index under the digest of index.json" || fail "digest in the registry"
  "${CRANE}" manifest "${REG}/myriarch/myriarch@$(value_of IMAGE_MANIFEST_DIGEST_LINUX_ARM64 "${S1}")" >/dev/null 2>&1 && ok "the platform manifests are in the registry" || fail "platform manifest missing in the registry"
  push "${SRV_LAYOUT}" "${REG}/myriarch/myriarch@${IDX1}" >/dev/null 2>&1 && ok "pushing the same digest again works" || fail "second push failed"
  push "${UPD_LAYOUT}" "${REG}/myriarch/myriarch-updater@${UPD_DIGEST}" >/dev/null 2>&1 && ok "push of the updater image" || fail "push of the updater image failed"
  [ "$(curl -fsS "http://${REG}/v2/myriarch/myriarch-updater/tags/list" | tr -d ' \n')" = '{"name":"myriarch/myriarch-updater","tags":null}' ] && ok "... also without a tag" || fail "tags of the updater repository"
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://${REG}/v2/myriarch/myriarch/manifests/latest")" = 404 ] && ok "there is no tag latest" || fail "a tag latest exists"

  # refusals before anything is sent
  CALLS="${W}/crane-calls"
  printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s"\nexit 1\n' "${CALLS}" > "${W}/crane-stub"; chmod 0755 "${W}/crane-stub"
  push_stub() { CRANE="${W}/crane-stub" bash "${PUSH}" "$@"; }
  expect_fail "a reference without digest is refused (crane would create latest)" "must be <registry>.*@sha256:<64 hex>" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/myriarch"
  expect_fail "a reference with a tag is refused" "must be <registry>.*@sha256:<64 hex>" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/myriarch:0.1.0"
  expect_fail "a reference with a tag and a digest is refused" "must be <registry>.*@sha256:<64 hex>" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/myriarch:0.1.0@${IDX1}"
  expect_fail "a short digest is refused" "must be <registry>.*@sha256:<64 hex>" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/myriarch@sha256:abc"
  expect_fail "a digest of another algorithm is refused" "must be <registry>.*@sha256:<64 hex>" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/myriarch@sha512:$(printf 'a%.0s' $(seq 1 64))"
  expect_fail "an upper case digest is refused" "must be <registry>.*@sha256:<64 hex>" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/myriarch@sha256:$(printf 'A%.0s' $(seq 1 64))"
  expect_fail "a missing argument is a usage error" "usage: ci/release/push-images.sh" push_stub "${SRV_LAYOUT}"
  expect_fail "a digest that is not the one of index.json is refused" "index.json has the digest ${IDX1}" push_stub "${SRV_LAYOUT}" "${REG}/myriarch/other@${UPD_DIGEST}"
  expect_fail "a missing layout is refused" "is not a directory" push_stub "${W}/no-layout" "${REG}/myriarch/myriarch@${IDX1}"
  cp -R "${SRV_LAYOUT}" "${W}/layout-tampered"
  layer_blob="$(find "${W}/layout-tampered/blobs/sha256" -type f -size +1k | head -n 1)"
  printf 'x' >> "${layer_blob}"
  expect_fail "a layout with a changed blob is refused" "does not match its digest" push_stub "${W}/layout-tampered" "${REG}/myriarch/myriarch@${IDX1}"
  cp -R "${SRV_LAYOUT}" "${W}/layout-missing"
  rm "${W}/layout-missing/blobs/sha256/$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["manifests"][0]["digest"][7:])' "${SRV_LAYOUT}/index.json")"
  expect_fail "a layout with a missing manifest is refused" "cannot read" push_stub "${W}/layout-missing" "${REG}/myriarch/myriarch@${IDX1}"
  # a nested layout (index.json holds a single descriptor of an index) would be wrapped by crane push --index
  mkdir -p "${W}/layout-nested/blobs/sha256"
  cp -R "${SRV_LAYOUT}/blobs/sha256/." "${W}/layout-nested/blobs/sha256/"
  cp "${SRV_LAYOUT}/oci-layout" "${W}/layout-nested/oci-layout"
  cp "${SRV_LAYOUT}/index.json" "${W}/layout-nested/blobs/sha256/${IDX1#sha256:}"
  python3 -I - "${W}/layout-nested/index.json" "${IDX1}" "$(wc -c < "${SRV_LAYOUT}/index.json" | tr -d ' ')" <<'PY'
import json, sys
out, digest, size = sys.argv[1], sys.argv[2], int(sys.argv[3])
doc = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
       "manifests": [{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": digest, "size": size}]}
open(out, "w").write(json.dumps(doc, sort_keys=True, separators=(",", ":")))
PY
  NESTED="sha256:$(sha256_of "${W}/layout-nested/index.json")"
  expect_fail "a layout with a nested index is refused" "expected image manifests only" push_stub "${W}/layout-nested" "${REG}/myriarch/myriarch@${NESTED}"
  # the guard for the published repositories: only the release workflow at a version tag
  GH_REF="ghcr.io/myriarch/myriarch@${IDX1}"
  rm -f "${CALLS}"
  expect_fail "ghcr.io/myriarch is refused without the release workflow" "published repositories are written only by .github/workflows/release.yml" env -u GITHUB_WORKFLOW_REF CRANE="${W}/crane-stub" bash "${PUSH}" "${SRV_LAYOUT}" "${GH_REF}"
  expect_fail "... from another workflow" "published repositories are written only" env GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/ci.yml@refs/tags/v0.1.0" CRANE="${W}/crane-stub" bash "${PUSH}" "${SRV_LAYOUT}" "${GH_REF}"
  expect_fail "... from the release workflow on a branch" "published repositories are written only" env GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/release.yml@refs/heads/main" CRANE="${W}/crane-stub" bash "${PUSH}" "${SRV_LAYOUT}" "${GH_REF}"
  expect_fail "... from a fork" "published repositories are written only" env GITHUB_WORKFLOW_REF="someone/myriarch/.github/workflows/release.yml@refs/tags/v0.1.0" CRANE="${W}/crane-stub" bash "${PUSH}" "${SRV_LAYOUT}" "${GH_REF}"
  expect_fail "... also with another spelling of the host" "published repositories are written only" env -u GITHUB_WORKFLOW_REF CRANE="${W}/crane-stub" bash "${PUSH}" "${SRV_LAYOUT}" "GHCR.io/myriarch/myriarch@${IDX1}"
  expect_fail "... for the updater repository as well" "published repositories are written only" env -u GITHUB_WORKFLOW_REF CRANE="${W}/crane-stub" bash "${PUSH}" "${UPD_LAYOUT}" "ghcr.io/myriarch/myriarch-updater@${UPD_DIGEST}"
  if [ ! -e "${CALLS}" ]; then ok "crane was not called by any refused push"; else fail "crane was called: $(cat "${CALLS}")"; fi
  # from the release workflow the script goes on: crane push gets the layout and the reference with the digest, and nothing else
  GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.1.0-rc.1" CRANE="${W}/crane-stub" bash "${PUSH}" "${SRV_LAYOUT}" "${GH_REF}" >/dev/null 2>&1
  if [ "$(cat "${CALLS}" 2>/dev/null)" = "push --index ${SRV_LAYOUT} ${GH_REF}" ]; then ok "from the release workflow: crane push --index <layout> <repo>@<digest>, no tag option"; else fail "crane call: $(cat "${CALLS}" 2>/dev/null)"; fi
  # a registry that reports another digest is caught on the client
  printf '#!/bin/sh\n[ "$1" = digest ] && { echo sha256:%s; exit 0; }\nexec "%s" "$@"\n' "$(printf '0%.0s' $(seq 1 64))" "${CRANE}" > "${W}/crane-liar"; chmod 0755 "${W}/crane-liar"
  expect_fail "a digest the registry reports differently is a failure" "the registry reports sha256:0000" env CRANE="${W}/crane-liar" bash "${PUSH}" "${SRV_LAYOUT}" "${REG}/myriarch/liar@${IDX1}"
  stop_registry
  REG_PID=""
else
  fail "release image tests skipped because the stand-in binaries could not be built"
fi

finish_tests "image-crane-test"
