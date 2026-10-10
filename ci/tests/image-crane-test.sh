#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail" is intended in the tests (ok/fail are counters and never fail)
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Contract of the crane image scripts (scripts/build-image-crane.sh, scripts/build-collector-image-crane.sh,
# scripts/build-mirror-image-crane.sh) and of their shared helpers (scripts/lib/image-common.sh, ci/image-labels.env):
# IMAGE_REPO is required and validated, base images must be pinned by digest, the label file is validated, the source URL
# is derived without host special cases, and DOCKER_CONFIG is never defaulted. No network, no registry, runs in seconds.
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

finish_tests "image-crane-test"
