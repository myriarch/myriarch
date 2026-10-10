#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Installs the CI tools into ${TOOLS_BIN} (default: .cache/bin, safe to cache between runs).
#
#   ci/install-tools.sh goreleaser cosign helm yq kustomize kubeconform gotestsum crane govulncheck go-licenses gitleaks reuse shellcheck pwsh kind kubectl hugo sass lychee actionlint syft gh go-winres zizmor osslsigncode
#   ci/install-tools.sh apt:libssl-dev apt:zlib1g-dev apt:python3
#
# Go tools are built from ci/tools/<tool>/ (go.mod + go.sum in the repository) through the configured GOPROXY (no
# downloads from GitHub needed); the versions are in ci/versions.env. A stamp per tool, version and content of its go.mod and
# go.sum (go_tool_stamp in ci/lib.sh) prevents rebuilds from the cache: a changed module file rebuilds the tool, also when an
# older tool cache was restored. Debian packages (apt:<package>) come from the package sources configured in the image or the
# environment (apt honors http_proxy/https_proxy).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_versions

[ "$#" -gt 0 ] || die "usage: ci/install-tools.sh <tool>... (goreleaser cosign helm yq kustomize kubeconform gotestsum crane govulncheck go-licenses gitleaks reuse shellcheck pwsh kind kubectl hugo sass lychee actionlint syft gh go-winres zizmor osslsigncode apt:<package>)"
mkdir -p "${TOOLS_BIN}"

# Built from ci/tools/<tool>/ (a separate Go module per tool with go.mod AND go.sum in the repository): the hashes of all
# dependencies are therefore in the repository and enforced with -mod=readonly - even if GOPROXY is a proxy and
# GOSUMDB was switched off. (`go install <module>@<version>` outside a module would have no committed hashes.)
# To update: change the version in ci/versions.env, then run ci/tools/update.sh <tool>; ci/check.sh compares both.
# Release builds with signing keys should run WITHOUT a restored tool cache; TOOLS_NO_CACHE=1 ignores existing binaries.
go_install() { # tool module-with-cmd version-var binary-name [no-trimpath] [main-toolchain]
  local tool="$1" module="$2" version="${!3}" bin="$4" trim="-trimpath" toolchain="local" suffix=""
  # go-licenses finds GOROOT through the binary; with -trimpath it reports nothing at all (exit status 0).
  if [ "${5:-}" = no-trimpath ]; then trim=""; fi
  # main-toolchain: build with the toolchain that the go directive of the main module selects (go-licenses must run the same
  # GOROOT as the `go list` it drives, otherwise it does not recognise the standard library when go switched toolchains).
  if [ "${6:-}" = main-toolchain ]; then
    toolchain="go$(awk '$1 == "go" { print $2; exit }' "${ROOT}/go.mod")"
    suffix="-${toolchain}"
  fi
  # The stamp names the content of the module files as well as the version. A tool cache that is restored under a new cache key
  # (restore-keys in .github/actions/setup) can hold the binary of the same version built from older requirements.
  local stamp
  stamp="${TOOLS_BIN}/$(go_tool_stamp "${tool}" "${version}" "${suffix}")"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${TOOLS_BIN}/${bin}" ] && [ -f "${stamp}" ]; then
    log "${tool} ${version}: from the cache"
    return 0
  fi
  need_cmd go
  log "building ${tool} ${version} (ci/tools/${tool}, go build -mod=readonly)"
  ( cd "${CI_DIR}/tools/${tool}" && GOBIN="" GOFLAGS=-mod=readonly GOTOOLCHAIN="${toolchain}" CGO_ENABLED=0 go build ${trim} -o "${TOOLS_BIN}/${bin}" "${module}" ) \
    || die "go build ${module} (ci/tools/${tool}) failed (GOPROXY/HTTPS_PROXY set? go.sum up to date?)"
  rm -f "${TOOLS_BIN}/.stamp-${tool}-"*
  : > "${stamp}"
}

# reuse (REUSE compliance checker) is a Python tool. It is installed into a virtual environment
# $(dirname ${TOOLS_BIN})/reuse/<version> (default: .cache/reuse/<version>) and linked as ${TOOLS_BIN}/reuse. Both pip
# runs use --require-hashes with the hash-pinned files of ci/tools/reuse/ (see ci/versions.env for the constraints and
# the regeneration commands): poetry-core first (binary wheel), then reuse and its dependencies without build isolation,
# so that reuse is built from its sdist with exactly that poetry-core. Python 3.9 or newer is required; REUSE_PYTHON
# selects the interpreter (default python3), and Debian needs python3-venv. pip honors the usual environment: set
# PIP_INDEX_URL (and PIP_CERT or REQUESTS_CA_BUNDLE for an extra CA) to install from an internal mirror.
py_install_reuse() {
  local version="${REUSE_VERSION}" bin="${TOOLS_BIN}/reuse" py="${REUSE_PYTHON:-python3}"
  local reqdir="${CI_DIR}/tools/reuse" venv hash stamp tmp got
  venv="$(dirname "${TOOLS_BIN}")/reuse/${version}"
  [ -f "${reqdir}/requirements.txt" ] && [ -f "${reqdir}/build-requirements.txt" ] || die "reuse: ci/tools/reuse/requirements.txt or build-requirements.txt is missing"
  tmp="$(mktemp)"
  trap 'rm -f "${tmp}"' EXIT
  cat "${reqdir}/build-requirements.txt" "${reqdir}/requirements.txt" > "${tmp}"
  hash="$(sha256_of "${tmp}")"
  rm -f "${tmp}"; trap - EXIT
  stamp="${TOOLS_BIN}/.stamp-reuse-${version}-${hash:0:12}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "reuse ${version}: from the cache"
    return 0
  fi
  have "${py}" || die "reuse: ${py} not found (Python 3.9 or newer; REUSE_PYTHON selects the interpreter)"
  "${py}" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' \
    || die "reuse: ${py} is older than Python 3.9 (REUSE_PYTHON selects the interpreter)"
  log "installing reuse ${version} into ${venv} (hash-pinned)"
  mkdir -p "$(dirname "${venv}")"
  "${py}" -m venv --clear "${venv}" || die "reuse: cannot create the virtual environment (Debian: apt install python3-venv)"
  (
    export PIP_DISABLE_PIP_VERSION_CHECK=1 PIP_NO_INPUT=1
    "${venv}/bin/python" -m pip install --quiet --require-hashes --no-deps --only-binary=:all: -r "${reqdir}/build-requirements.txt"
    "${venv}/bin/python" -m pip install --quiet --require-hashes --no-deps --no-build-isolation --only-binary=:all: --no-binary=reuse -r "${reqdir}/requirements.txt"
  ) || die "reuse: pip install failed (PIP_INDEX_URL for a mirror? Python 3.9 or newer?)"
  ln -sfn "${venv}/bin/reuse" "${bin}"
  got="$(LANGUAGE=en "${bin}" --version 2>/dev/null | head -n 1)" || got=""
  case "${got}" in *"${version}"*) ;; *) die "reuse: 'reuse --version' reports '${got:-nothing}', expected ${version}" ;; esac
  rm -f "${TOOLS_BIN}/.stamp-reuse-"*
  : > "${stamp}"
  log "reuse ${version}: installed (hash-pinned requirements)"
}

# ShellCheck is not a Go tool: it is downloaded as a release archive and verified against the SHA-256 pinned in
# ci/versions.env (SHELLCHECK_SHA256_<OS>_<ARCH>; the environment cannot override it). SHELLCHECK_BASE_URL points the download at an internal mirror.
release_install_shellcheck() {
  local version="${SHELLCHECK_VERSION}" bin="${TOOLS_BIN}/shellcheck"
  local stamp="${TOOLS_BIN}/.stamp-shellcheck-${version}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "shellcheck ${version}: cached"
    return 0
  fi
  local os arch asset_arch key_arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *)      os="$(uname -s)" ;;
  esac
  arch="$(uname -m)"
  case "${arch}" in
    arm64|aarch64) asset_arch=aarch64; key_arch=ARM64 ;;
    x86_64|amd64)  asset_arch=x86_64;  key_arch=AMD64 ;;
    *)             die "shellcheck: unsupported platform ${os}/${arch}" ;;
  esac
  case "${os}" in darwin|linux) ;; *) die "shellcheck: unsupported platform ${os}/${arch}" ;; esac
  local key want
  key="SHELLCHECK_SHA256_$(printf '%s' "${os}" | tr '[:lower:]' '[:upper:]')_${key_arch}"
  # The pin is read from the file, never from the environment: an exported SHELLCHECK_SHA256_* (together with
  # SHELLCHECK_BASE_URL) must not be able to replace it silently.
  want="$(sed -n "s/^${key}=//p" "${CI_DIR}/versions.env" | head -n 1)"
  if [ -n "${!key:-}" ] && [ "${!key}" != "${want}" ]; then
    warn "shellcheck: ignoring ${key} from the environment, using the pin in ci/versions.env"
  fi
  printf '%s' "${want}" | grep -Eq '^[0-9a-f]{64}$' || die "shellcheck: ${key} in ci/versions.env is missing or not a SHA-256"
  need_cmd curl tar
  local asset="shellcheck-${version}.${os}.${asset_arch}.tar.gz"
  local url="${SHELLCHECK_BASE_URL:-https://github.com/koalaman/shellcheck/releases/download}/${version}/${asset}"
  local tmp cargs=() a got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT   # die exits the script, so the temp dir (and a file that failed the check) is removed there
  while IFS= read -r a; do if [ -n "${a}" ]; then cargs+=("${a}"); fi; done < <(curl_ca_args)
  log "downloading shellcheck ${version} (${os}/${asset_arch})"
  curl -fsSL ${cargs[@]+"${cargs[@]}"} -o "${tmp}/${asset}" "${url}" || die "shellcheck: download of ${url} failed (HTTPS_PROXY set?)"
  got="$(sha256_of "${tmp}/${asset}")"
  [ "${got}" = "${want}" ] || die "shellcheck: SHA-256 mismatch for ${asset} (expected ${want}, got ${got})"
  tar -xzf "${tmp}/${asset}" -C "${tmp}" "shellcheck-${version}/shellcheck" || die "shellcheck: cannot extract ${asset}"
  install -m 755 "${tmp}/shellcheck-${version}/shellcheck" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  rm -f "${TOOLS_BIN}/.stamp-shellcheck-"*
  : > "${stamp}"
  log "shellcheck ${version}: installed (sha256 verified)"
}

# PowerShell 7 is not a Go tool either: the release archive is verified against the SHA-256 pinned in ci/versions.env
# (POWERSHELL_SHA256_<OS>_<ARCH>; the environment cannot override it), unpacked into .cache/pwsh/<version> and linked as
# ${TOOLS_BIN}/pwsh. It only runs the installer tests. POWERSHELL_BASE_URL points the download at an internal mirror.
release_install_pwsh() {
  local version="${POWERSHELL_VERSION}" bin="${TOOLS_BIN}/pwsh"
  local stamp="${TOOLS_BIN}/.stamp-pwsh-${version}" base
  base="$(dirname "${TOOLS_BIN}")/pwsh"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "pwsh ${version}: cached"
    return 0
  fi
  local os asset_os arch asset_arch key_arch
  case "$(uname -s)" in
    Darwin) os=darwin; asset_os=osx ;;
    Linux)  os=linux;  asset_os=linux ;;
    *)      die "pwsh: unsupported platform $(uname -s)" ;;
  esac
  arch="$(uname -m)"
  case "${arch}" in
    arm64|aarch64) asset_arch=arm64; key_arch=ARM64 ;;
    x86_64|amd64)  asset_arch=x64;   key_arch=AMD64 ;;
    *)             die "pwsh: unsupported platform ${os}/${arch}" ;;
  esac
  local key want
  key="POWERSHELL_SHA256_$(printf '%s' "${os}" | tr '[:lower:]' '[:upper:]')_${key_arch}"
  # As for shellcheck, the pin is read from the file only.
  want="$(sed -n "s/^${key}=//p" "${CI_DIR}/versions.env" | head -n 1)"
  if [ -n "${!key:-}" ] && [ "${!key}" != "${want}" ]; then
    warn "pwsh: ignoring ${key} from the environment, using the pin in ci/versions.env"
  fi
  printf '%s' "${want}" | grep -Eq '^[0-9a-f]{64}$' || die "pwsh: ${key} in ci/versions.env is missing or not a SHA-256"
  need_cmd curl tar
  local asset="powershell-${version}-${asset_os}-${asset_arch}.tar.gz"
  local url="${POWERSHELL_BASE_URL:-https://github.com/PowerShell/PowerShell/releases/download}/v${version}/${asset}"
  local tmp cargs=() a got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  while IFS= read -r a; do if [ -n "${a}" ]; then cargs+=("${a}"); fi; done < <(curl_ca_args)
  log "downloading pwsh ${version} (${os}/${asset_arch})"
  curl -fsSL ${cargs[@]+"${cargs[@]}"} -o "${tmp}/${asset}" "${url}" || die "pwsh: download of ${url} failed (HTTPS_PROXY set?)"
  got="$(sha256_of "${tmp}/${asset}")"
  [ "${got}" = "${want}" ] || die "pwsh: SHA-256 mismatch for ${asset} (expected ${want}, got ${got})"
  rm -rf "${base}"
  mkdir -p "${base}/${version}"
  tar -xzf "${tmp}/${asset}" -C "${base}/${version}" || die "pwsh: cannot extract ${asset}"
  chmod 755 "${base}/${version}/pwsh"
  ln -sfn "${base}/${version}/pwsh" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  rm -f "${TOOLS_BIN}/.stamp-pwsh-"*
  : > "${stamp}"
  log "pwsh ${version}: installed (sha256 verified)"
}

# kubectl is a release download too (dl.k8s.io), verified against the SHA-256 pinned in ci/versions.env
# (KUBECTL_SHA256_<OS>_<ARCH>; the environment cannot override it). KUBECTL_BASE_URL points the download at an internal mirror.
release_install_kubectl() {
  local version="${KUBECTL_VERSION}" bin="${TOOLS_BIN}/kubectl"
  local stamp="${TOOLS_BIN}/.stamp-kubectl-${version}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "kubectl ${version}: cached"
    return 0
  fi
  local os arch key_arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *)      die "kubectl: unsupported platform $(uname -s)" ;;
  esac
  arch="$(uname -m)"
  case "${arch}" in
    arm64|aarch64) arch=arm64; key_arch=ARM64 ;;
    x86_64|amd64)  arch=amd64; key_arch=AMD64 ;;
    *)             die "kubectl: unsupported platform ${os}/${arch}" ;;
  esac
  local key want
  key="KUBECTL_SHA256_$(printf '%s' "${os}" | tr '[:lower:]' '[:upper:]')_${key_arch}"
  # The pin is read from the file only, never from the environment (see shellcheck).
  want="$(sed -n "s/^${key}=//p" "${CI_DIR}/versions.env" | head -n 1)"
  if [ -n "${!key:-}" ] && [ "${!key}" != "${want}" ]; then
    warn "kubectl: ignoring ${key} from the environment, using the pin in ci/versions.env"
  fi
  printf '%s' "${want}" | grep -Eq '^[0-9a-f]{64}$' || die "kubectl: ${key} in ci/versions.env is missing or not a SHA-256"
  need_cmd curl
  local url="${KUBECTL_BASE_URL:-https://dl.k8s.io/release}/${version}/bin/${os}/${arch}/kubectl"
  local tmp cargs=() a got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  while IFS= read -r a; do if [ -n "${a}" ]; then cargs+=("${a}"); fi; done < <(curl_ca_args)
  log "downloading kubectl ${version} (${os}/${arch})"
  curl -fsSL ${cargs[@]+"${cargs[@]}"} -o "${tmp}/kubectl" "${url}" || die "kubectl: download of ${url} failed (HTTPS_PROXY set?)"
  got="$(sha256_of "${tmp}/kubectl")"
  [ "${got}" = "${want}" ] || die "kubectl: SHA-256 mismatch for ${url} (expected ${want}, got ${got})"
  install -m 755 "${tmp}/kubectl" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  rm -f "${TOOLS_BIN}/.stamp-kubectl-"*
  : > "${stamp}"
  log "kubectl ${version}: installed (sha256 verified)"
}

# The tools of the website (ADR-0031) are release downloads like shellcheck: verified against the SHA-256 pinned in ci/versions.env
# (the environment cannot override it) and mirrored through <TOOL>_BASE_URL. Shared steps: the pin of the current platform, and the
# download with the check. They set the globals PIN_SHA and DL_FILE.
website_pin() { # tool KEY-PREFIX os-var arch-var - sets PIN_SHA from the file only
  local tool="$1" prefix="$2" os="$3" key_arch="$4" key
  key="${prefix}_SHA256_$(printf '%s' "${os}" | tr '[:lower:]' '[:upper:]')_${key_arch}"
  PIN_SHA="$(sed -n "s/^${key}=//p" "${CI_DIR}/versions.env" | head -n 1)"
  if [ -n "${!key:-}" ] && [ "${!key}" != "${PIN_SHA}" ]; then
    warn "${tool}: ignoring ${key} from the environment, using the pin in ci/versions.env"
  fi
  printf '%s' "${PIN_SHA}" | grep -Eq '^[0-9a-f]{64}$' || die "${tool}: ${key} in ci/versions.env is missing or not a SHA-256"
}
website_download() { # tool url file sha - downloads and verifies
  local tool="$1" url="$2" file="$3" want="$4" cargs=() a got
  while IFS= read -r a; do if [ -n "${a}" ]; then cargs+=("${a}"); fi; done < <(curl_ca_args)
  curl -fsSL ${cargs[@]+"${cargs[@]}"} -o "${file}" "${url}" || die "${tool}: download of ${url} failed (HTTPS_PROXY set?)"
  got="$(sha256_of "${file}")"
  [ "${got}" = "${want}" ] || die "${tool}: SHA-256 mismatch for $(basename "${file}") (expected ${want}, got ${got})"
}

# Hugo extended generates the website. Linux: tar.gz; macOS: only a universal .pkg exists, which is expanded (pkgutil, without
# installing it) and the binary is taken from its payload. The binary lands in .cache/hugo/<version>/hugo (the same cache directory
# is HUGO_CACHEDIR of ci/website.sh) and is linked as ${TOOLS_BIN}/hugo. HUGO_BASE_URL points the download at an internal mirror.
release_install_hugo() {
  local version="${HUGO_VERSION}" bin="${TOOLS_BIN}/hugo" base stamp
  stamp="${TOOLS_BIN}/.stamp-hugo-${version}"
  base="$(dirname "${TOOLS_BIN}")/hugo"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "hugo ${version}: cached"
    return 0
  fi
  local os asset key_arch arch
  arch="$(uname -m)"
  case "$(uname -s)" in
    Darwin) os=darwin; key_arch=UNIVERSAL; asset="hugo_extended_${version}_darwin-universal.pkg" ;;
    Linux)
      os=linux
      case "${arch}" in
        arm64|aarch64) key_arch=ARM64; asset="hugo_extended_${version}_linux-arm64.tar.gz" ;;
        x86_64|amd64)  key_arch=AMD64; asset="hugo_extended_${version}_linux-amd64.tar.gz" ;;
        *)             die "hugo: unsupported platform ${os}/${arch}" ;;
      esac ;;
    *) die "hugo: unsupported platform $(uname -s)" ;;
  esac
  website_pin hugo HUGO "${os}" "${key_arch}"
  need_cmd curl
  local url="${HUGO_BASE_URL:-https://github.com/gohugoio/hugo/releases/download}/v${version}/${asset}" tmp got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  log "downloading hugo ${version} (${asset})"
  website_download hugo "${url}" "${tmp}/${asset}" "${PIN_SHA}"
  mkdir -p "${base}/${version}"
  if [ "${os}" = darwin ]; then
    need_cmd pkgutil
    pkgutil --expand-full "${tmp}/${asset}" "${tmp}/pkg" >/dev/null || die "hugo: cannot expand ${asset}"
    [ -f "${tmp}/pkg/Payload/hugo" ] || die "hugo: ${asset} has no Payload/hugo"
    install -m 755 "${tmp}/pkg/Payload/hugo" "${base}/${version}/hugo"
  else
    need_cmd tar
    tar -xzf "${tmp}/${asset}" -C "${tmp}" hugo || die "hugo: cannot extract ${asset}"
    install -m 755 "${tmp}/hugo" "${base}/${version}/hugo"
  fi
  rm -rf "${tmp}"; trap - EXIT
  ln -sfn "${base}/${version}/hugo" "${bin}"
  got="$("${bin}" version 2>/dev/null | head -n 1)" || got=""
  case "${got}" in
    *"v${version}"*"+extended"*) ;;
    *) die "hugo: 'hugo version' reports '${got:-nothing}', expected v${version}+extended" ;;
  esac
  rm -f "${TOOLS_BIN}/.stamp-hugo-"*
  : > "${stamp}"
  log "hugo ${version}: installed (sha256 verified)"
}

# Dart Sass compiles the SCSS of the website theme (Hugo calls the sass executable on PATH). The archive is unpacked into
# .cache/dart-sass/<version>/ and ${TOOLS_BIN}/sass links its launcher. DART_SASS_BASE_URL points the download at an internal mirror.
release_install_sass() {
  local version="${DART_SASS_VERSION}" bin="${TOOLS_BIN}/sass" base stamp
  stamp="${TOOLS_BIN}/.stamp-sass-${version}"
  base="$(dirname "${TOOLS_BIN}")/dart-sass"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "sass ${version}: cached"
    return 0
  fi
  local os asset_os arch asset_arch key_arch
  case "$(uname -s)" in
    Darwin) os=darwin; asset_os=macos ;;
    Linux)  os=linux;  asset_os=linux ;;
    *)      die "sass: unsupported platform $(uname -s)" ;;
  esac
  arch="$(uname -m)"
  case "${arch}" in
    arm64|aarch64) asset_arch=arm64; key_arch=ARM64 ;;
    x86_64|amd64)  asset_arch=x64;   key_arch=AMD64 ;;
    *)             die "sass: unsupported platform ${os}/${arch}" ;;
  esac
  website_pin sass DART_SASS "${os}" "${key_arch}"
  need_cmd curl tar
  local asset="dart-sass-${version}-${asset_os}-${asset_arch}.tar.gz"
  local url="${DART_SASS_BASE_URL:-https://github.com/sass/dart-sass/releases/download}/${version}/${asset}" tmp got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  log "downloading sass ${version} (${os}/${asset_arch})"
  website_download sass "${url}" "${tmp}/${asset}" "${PIN_SHA}"
  rm -rf "${base:?}/${version}"
  mkdir -p "${base}/${version}"
  tar -xzf "${tmp}/${asset}" -C "${base}/${version}" || die "sass: cannot extract ${asset}"
  [ -x "${base}/${version}/dart-sass/sass" ] || die "sass: ${asset} has no dart-sass/sass launcher"
  rm -rf "${tmp}"; trap - EXIT
  ln -sfn "${base}/${version}/dart-sass/sass" "${bin}"
  got="$("${bin}" --version 2>/dev/null | head -n 1)" || got=""
  case "${got}" in "${version}"*) ;; *) die "sass: 'sass --version' reports '${got:-nothing}', expected ${version}" ;; esac
  rm -f "${TOOLS_BIN}/.stamp-sass-"*
  : > "${stamp}"
  log "sass ${version}: installed (sha256 verified)"
}

# lychee checks the links of the built website. The archive contains lychee-<target>/lychee (macOS: -apple-darwin, Linux: static
# musl builds). LYCHEE_BASE_URL points the download at an internal mirror.
release_install_lychee() {
  local version="${LYCHEE_VERSION}" bin="${TOOLS_BIN}/lychee"
  local stamp="${TOOLS_BIN}/.stamp-lychee-${version}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "lychee ${version}: cached"
    return 0
  fi
  local os arch target key_arch
  arch="$(uname -m)"
  case "$(uname -s)" in
    Darwin) os=darwin; target=apple-darwin ;;
    Linux)  os=linux;  target=unknown-linux-musl ;;
    *)      die "lychee: unsupported platform $(uname -s)" ;;
  esac
  case "${arch}" in
    arm64|aarch64) target="aarch64-${target}"; key_arch=ARM64 ;;
    x86_64|amd64)  target="x86_64-${target}";  key_arch=AMD64 ;;
    *)             die "lychee: unsupported platform ${os}/${arch}" ;;
  esac
  website_pin lychee LYCHEE "${os}" "${key_arch}"
  need_cmd curl tar
  local asset="lychee-${target}.tar.gz"
  local url="${LYCHEE_BASE_URL:-https://github.com/lycheeverse/lychee/releases/download}/lychee-v${version}/${asset}" tmp got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  log "downloading lychee ${version} (${target})"
  website_download lychee "${url}" "${tmp}/${asset}" "${PIN_SHA}"
  tar -xzf "${tmp}/${asset}" -C "${tmp}" || die "lychee: cannot extract ${asset}"
  [ -f "${tmp}/lychee-${target}/lychee" ] || die "lychee: ${asset} has no lychee-${target}/lychee"
  install -m 755 "${tmp}/lychee-${target}/lychee" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  got="$("${bin}" --version 2>/dev/null | head -n 1)" || got=""
  case "${got}" in *"${version}"*) ;; *) die "lychee: 'lychee --version' reports '${got:-nothing}', expected ${version}" ;; esac
  rm -f "${TOOLS_BIN}/.stamp-lychee-"*
  : > "${stamp}"
  log "lychee ${version}: installed (sha256 verified)"
}

# syft (SBOM generation) is a release download like zizmor, verified against the SHA-256 pinned in ci/versions.env
# (SYFT_SHA256_<OS>_<ARCH>; the environment cannot override it). It is not built with go build: only the release binaries carry
# their version, which syft writes into the SBOMs it makes. The assets are syft_<version>_<os>_<arch>.tar.gz with the file syft
# next to LICENSE and README.md. SYFT_BASE_URL points the download at an internal mirror (the path below it is v<version>/<asset>).
release_install_syft() {
  local version="${SYFT_VERSION}" bin="${TOOLS_BIN}/syft"
  local stamp="${TOOLS_BIN}/.stamp-syft-${version}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "syft ${version}: cached"
    return 0
  fi
  local os arch key_arch
  arch="$(uname -m)"
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *)      die "syft: unsupported platform $(uname -s)" ;;
  esac
  case "${arch}" in
    arm64|aarch64) arch=arm64; key_arch=ARM64 ;;
    x86_64|amd64)  arch=amd64; key_arch=AMD64 ;;
    *)             die "syft: unsupported platform ${os}/${arch}" ;;
  esac
  website_pin syft SYFT "${os}" "${key_arch}"
  need_cmd curl tar
  local asset="syft_${version}_${os}_${arch}.tar.gz"
  local url="${SYFT_BASE_URL:-https://github.com/anchore/syft/releases/download}/v${version}/${asset}" tmp got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  log "downloading syft ${version} (${os}/${arch})"
  website_download syft "${url}" "${tmp}/${asset}" "${PIN_SHA}"
  tar -xzf "${tmp}/${asset}" -C "${tmp}" syft || die "syft: cannot extract ${asset}"
  install -m 755 "${tmp}/syft" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  # SYFT_CHECK_FOR_APP_UPDATE=false: the version command must not reach out to look for a newer release
  got="$(SYFT_CHECK_FOR_APP_UPDATE=false "${bin}" version 2>/dev/null | awk '$1 == "Version:" { print $2; exit }')" || got=""
  [ "${got}" = "${version}" ] || { rm -f "${bin}"; die "syft: 'syft version' reports '${got:-nothing}', expected ${version}"; }
  rm -f "${TOOLS_BIN}/.stamp-syft-"*
  : > "${stamp}"
  log "syft ${version}: installed (sha256 verified)"
}

# zizmor (static analysis of the GitHub Actions workflows) is a release download like lychee, verified against the SHA-256 pinned
# in ci/versions.env (ZIZMOR_SHA256_<OS>_<ARCH>; the environment cannot override it). The assets are zizmor-<target>.tar.gz with the
# single file zizmor; Linux gets the glibc ("gnu") build, the release has no musl build. ZIZMOR_BASE_URL points the download at an
# internal mirror (the path below it is v<version>/<asset>).
release_install_zizmor() {
  local version="${ZIZMOR_VERSION}" bin="${TOOLS_BIN}/zizmor"
  local stamp="${TOOLS_BIN}/.stamp-zizmor-${version}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "zizmor ${version}: cached"
    return 0
  fi
  local os arch target key_arch
  arch="$(uname -m)"
  case "$(uname -s)" in
    Darwin) os=darwin; target=apple-darwin ;;
    Linux)  os=linux;  target=unknown-linux-gnu ;;
    *)      die "zizmor: unsupported platform $(uname -s)" ;;
  esac
  case "${arch}" in
    arm64|aarch64) target="aarch64-${target}"; key_arch=ARM64 ;;
    x86_64|amd64)  target="x86_64-${target}";  key_arch=AMD64 ;;
    *)             die "zizmor: unsupported platform ${os}/${arch}" ;;
  esac
  website_pin zizmor ZIZMOR "${os}" "${key_arch}"
  need_cmd curl tar
  local asset="zizmor-${target}.tar.gz"
  local url="${ZIZMOR_BASE_URL:-https://github.com/zizmorcore/zizmor/releases/download}/v${version}/${asset}" tmp got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  log "downloading zizmor ${version} (${target})"
  website_download zizmor "${url}" "${tmp}/${asset}" "${PIN_SHA}"
  tar -xzf "${tmp}/${asset}" -C "${tmp}" zizmor || die "zizmor: cannot extract ${asset}"
  install -m 755 "${tmp}/zizmor" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  got="$("${bin}" --version 2>/dev/null | head -n 1)" || got=""
  [ "${got}" = "zizmor ${version}" ] || { rm -f "${bin}"; die "zizmor: 'zizmor --version' reports '${got:-nothing}', expected 'zizmor ${version}'"; }
  rm -f "${TOOLS_BIN}/.stamp-zizmor-"*
  : > "${stamp}"
  log "zizmor ${version}: installed (sha256 verified)"
}

# osslsigncode (Authenticode signing and verification of the Windows release files) is built from the source archive of the tag,
# verified against OSSLSIGNCODE_SOURCE_SHA256 of ci/versions.env (read from the file; the environment cannot override it).
# Upstream publishes no Linux binary, and a distribution package is too old (Ubuntu 24.04 ships 2.8, which predates the
# verification fixes of 2.12 and 2.13). The build needs no cmake: one cc call over the C files and a minimal config.h. It needs a
# C compiler, the OpenSSL 3 headers and zlib, which is why osslsigncode is not part of the default tool set of the Makefile.
# OSSLSIGNCODE_BASE_URL points the download at an internal mirror (the path below it is <version>.tar.gz);
# OSSLSIGNCODE_OPENSSL_PREFIX selects the OpenSSL 3 installation (default: /usr on Linux, Homebrew openssl@3 on macOS).
release_install_osslsigncode() {
  local version="${OSSLSIGNCODE_VERSION}" bin="${TOOLS_BIN}/osslsigncode"
  local stamp="${TOOLS_BIN}/.stamp-osslsigncode-${version}"
  if [ "${TOOLS_NO_CACHE:-0}" != 1 ] && [ -x "${bin}" ] && [ -f "${stamp}" ]; then
    log "osslsigncode ${version}: cached"
    return 0
  fi
  local want
  want="$(sed -n 's/^OSSLSIGNCODE_SOURCE_SHA256=//p' "${CI_DIR}/versions.env" | head -n 1)"
  if [ -n "${OSSLSIGNCODE_SOURCE_SHA256:-}" ] && [ "${OSSLSIGNCODE_SOURCE_SHA256}" != "${want}" ]; then
    warn "osslsigncode: ignoring OSSLSIGNCODE_SOURCE_SHA256 from the environment, using the pin in ci/versions.env"
  fi
  printf '%s' "${want}" | grep -Eq '^[0-9a-f]{64}$' || die "osslsigncode: OSSLSIGNCODE_SOURCE_SHA256 in ci/versions.env is missing or not a SHA-256"
  printf '%s' "${version}" | grep -Eq '^[0-9]+\.[0-9]+(\.[0-9]+)?$' || die "osslsigncode: OSSLSIGNCODE_VERSION '${version}' is not a plain version (for example 2.14)"
  need_cmd curl tar cc
  # OpenSSL 3 headers (and zlib on Linux; the macOS SDK provides zlib)
  local prefix="" cand header major
  case "$(uname -s)" in
    Darwin)
      local cands=()
      [ -z "${OSSLSIGNCODE_OPENSSL_PREFIX:-}" ] || cands+=("${OSSLSIGNCODE_OPENSSL_PREFIX}")
      if have brew; then cands+=("$(brew --prefix openssl@3 2>/dev/null || true)"); fi
      cands+=(/opt/homebrew/opt/openssl@3 /usr/local/opt/openssl@3)
      for cand in "${cands[@]}"; do
        if [ -n "${cand}" ] && [ -f "${cand}/include/openssl/opensslv.h" ]; then prefix="${cand}"; break; fi
      done ;;
    *)
      prefix="${OSSLSIGNCODE_OPENSSL_PREFIX:-/usr}"
      [ -f /usr/include/zlib.h ] || prefix="" ;;
  esac
  header="${prefix}/include/openssl/opensslv.h"
  major=""
  if [ -n "${prefix}" ] && [ -f "${header}" ]; then
    major="$(sed -n -E 's/^# *define OPENSSL_VERSION_MAJOR +([0-9]+).*/\1/p' "${header}" | head -n 1)"
  fi
  if [ -z "${major}" ] || [ "${major}" -lt 3 ]; then
    die "osslsigncode: the OpenSSL 3 and zlib headers were not found (Debian/Ubuntu: ci/install-tools.sh apt:libssl-dev apt:zlib1g-dev; macOS: brew install openssl@3, or set OSSLSIGNCODE_OPENSSL_PREFIX)"
  fi
  local url="${OSSLSIGNCODE_BASE_URL:-https://github.com/mtrojnar/osslsigncode/archive/refs/tags}/${version}.tar.gz" tmp src got
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  log "downloading the source of osslsigncode ${version}"
  website_download osslsigncode "${url}" "${tmp}/src.tar.gz" "${want}"
  tar -xzf "${tmp}/src.tar.gz" -C "${tmp}" || die "osslsigncode: cannot extract the source archive"
  src="${tmp}/osslsigncode-${version}"
  [ -f "${src}/osslsigncode.c" ] || die "osslsigncode: the source archive has no osslsigncode-${version}/osslsigncode.c"
  mkdir -p "${tmp}/build"
  {
    printf '#define VERSION_MAJOR "%s"\n' "${version%%.*}"
    printf '#define VERSION_MINOR "%s"\n' "$(printf '%s' "${version}" | cut -d. -f2)"
    printf '#define PACKAGE_STRING "osslsigncode %s"\n' "${version}"
    printf '#define HAVE_TERMIOS_H\n#define HAVE_GETPASS\n#define HAVE_SYS_MMAN_H\n#define HAVE_MMAP\n'
  } > "${tmp}/build/config.h"
  local incl=() libs=()
  if [ "${prefix}" != /usr ]; then incl=("-I${prefix}/include"); libs=("-L${prefix}/lib"); fi
  # -std=gnu11 (not c11): getpass and mmap are hidden under strict c11 on glibc. osslsigncode parses files that a signing provider
  # returns, so the build is hardened explicitly (Debian and Ubuntu compilers do it by default, Apple clang does not): fortified
  # libc calls, stack protector and, on Linux, immediate binding with a read-only relocation table.
  local harden=(-U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=2 -fstack-protector-strong)
  case "$(uname -s)" in Linux) harden+=("-Wl,-z,relro,-z,now") ;; esac
  ( cd "${src}" && cc -std=gnu11 -O2 "${harden[@]}" -Wno-deprecated-declarations -DHAVE_CONFIG_H=1 -I"${tmp}/build" ${incl[@]+"${incl[@]}"} \
      osslsigncode.c helpers.c utf.c msi.c pe.c cab.c cat.c appx.c script.c \
      ${libs[@]+"${libs[@]}"} -lssl -lcrypto -lz -o "${tmp}/osslsigncode" ) >&2 \
    || die "osslsigncode: the build failed (C compiler, OpenSSL 3 and zlib headers?)"
  install -m 755 "${tmp}/osslsigncode" "${bin}"
  rm -rf "${tmp}"; trap - EXIT
  got="$("${bin}" --version 2>/dev/null | head -n 1)" || got=""
  [ "${got}" = "osslsigncode ${version}, using:" ] || { rm -f "${bin}"; die "osslsigncode: '--version' reports '${got:-nothing}', expected 'osslsigncode ${version}, using:'"; }
  rm -f "${TOOLS_BIN}/.stamp-osslsigncode-"*
  : > "${stamp}"
  log "osslsigncode ${version}: built from source (sha256 verified)"
}

apt_install() { # package
  local pkg="$1"
  # A command of the same name is already present (for example in the job image or locally), or the package is installed: nothing to do
  if have "${pkg}" || dpkg -s "${pkg}" >/dev/null 2>&1; then log "${pkg}: already present"; return 0; fi
  have apt-get || die "apt-get not available: ${pkg} must be part of the job image"
  local sudo=""
  if [ "$(id -u)" != 0 ]; then
    if have sudo; then sudo=sudo; else die "installing ${pkg} needs root"; fi
  fi
  log "installing Debian package ${pkg}"
  ${sudo} apt-get update -qq >&2
  ${sudo} env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${pkg}" >&2
}

for t in "$@"; do
  case "${t}" in
    goreleaser)   go_install goreleaser   github.com/goreleaser/goreleaser/v2          GORELEASER_VERSION goreleaser ;;
    cosign)       go_install cosign       github.com/sigstore/cosign/v3/cmd/cosign     COSIGN_VERSION     cosign ;;
    gotestsum)    go_install gotestsum    gotest.tools/gotestsum                       GOTESTSUM_VERSION  gotestsum ;;
    helm)         go_install helm         helm.sh/helm/v4/cmd/helm                     HELM_VERSION       helm ;;
    kustomize)    go_install kustomize    sigs.k8s.io/kustomize/kustomize/v5           KUSTOMIZE_VERSION  kustomize ;;
    yq)           go_install yq           github.com/mikefarah/yq/v4                   YQ_VERSION         yq ;;
    kubeconform)  go_install kubeconform  github.com/yannh/kubeconform/cmd/kubeconform KUBECONFORM_VERSION kubeconform ;;
    crane)        go_install crane        github.com/google/go-containerregistry/cmd/crane CRANE_VERSION  crane ;;
    govulncheck)  go_install govulncheck  golang.org/x/vuln/cmd/govulncheck           GOVULNCHECK_VERSION govulncheck ;;
    gitleaks)     go_install gitleaks     github.com/zricethezav/gitleaks/v8              GITLEAKS_VERSION   gitleaks ;;
    go-licenses)  go_install go-licenses  github.com/google/go-licenses/v2             GOLICENSES_VERSION go-licenses no-trimpath main-toolchain ;;
    reuse)        py_install_reuse ;;
    actionlint)   go_install actionlint   github.com/rhysd/actionlint/cmd/actionlint   ACTIONLINT_VERSION actionlint ;;
    syft)         release_install_syft ;;
    gh)           go_install gh           github.com/cli/cli/v2/cmd/gh                 GH_VERSION         gh ;;
    go-winres)    go_install go-winres    github.com/tc-hib/go-winres                  GOWINRES_VERSION   go-winres ;;
    zizmor)       release_install_zizmor ;;
    osslsigncode) release_install_osslsigncode ;;
    kind)         go_install kind         sigs.k8s.io/kind                             KIND_VERSION       kind ;;
    kubectl)      release_install_kubectl ;;
    shellcheck)   release_install_shellcheck ;;
    pwsh)         release_install_pwsh ;;
    hugo)         release_install_hugo ;;
    sass)         release_install_sass ;;
    lychee)       release_install_lychee ;;
    apt:*)        apt_install "${t#apt:}" ;;
    *)            die "unknown tool: ${t}" ;;
  esac
done
