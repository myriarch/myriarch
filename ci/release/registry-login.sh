#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Logs in to ghcr.io for the release workflow in the one way that every step of the push job can use (ADR-0035).
#
#   REGISTRY_TOKEN=... GITHUB_ACTOR=... TOOLS_BIN=... ci/release/registry-login.sh login|check|logout
#
# Why this form: actions/attest reads registry credentials only from $HOME/.docker/config.json (an `auths` entry with a base64
# `auth`); it ignores DOCKER_CONFIG, credsStore and credHelpers. So the login runs with the default HOME, never with DOCKER_CONFIG,
# and the result is asserted. cosign and crane read the same file.
#
# login   `crane auth login ghcr.io -u $GITHUB_ACTOR --password-stdin` with the token on stdin; then the checks of `check`.
# check   $HOME/.docker/config.json exists, has the mode 0600 (login corrects it), holds a non-empty base64 `auth` for ghcr.io whose
#         decoded text starts with "<GITHUB_ACTOR>:", and has neither `credsStore` nor `credHelpers` (a helper would hide the entry
#         from actions/attest).                                                                         [login_unusable]
# logout  removes the ghcr.io entry from the file (the file itself when it held nothing else).
#
# Refused: DOCKER_CONFIG set [docker_config_set]; an empty token or actor [login_input]; a crane failure [login_failed] (crane's output
# is not printed, because it could echo its input). The token is read from the environment only; it is never an argument and never
# printed, and the file is never printed.
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="registry-login.sh login|check|logout (environment: REGISTRY_TOKEN, GITHUB_ACTOR, TOOLS_BIN)"
HOST=ghcr.io

[ "$#" -eq 1 ] || usage_err "${USAGE}"
cmd="$1"
case "${cmd}" in login|check|logout) ;; -h|--help|help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;; *) usage_err "${USAGE}" ;; esac

[ -z "${DOCKER_CONFIG+set}" ] || err docker_config_set "DOCKER_CONFIG is set: actions/attest ignores it, so the login would be invisible to the attestation steps"
[ -n "${HOME:-}" ] && [ -d "${HOME}" ] || err login_input "HOME is not set or not a directory"
check_text login_input "HOME" "${HOME}"
config="${HOME}/.docker/config.json"

actor="${GITHUB_ACTOR:-}"
if [ "${cmd}" != logout ]; then
  [ -n "${actor}" ] || err login_input "GITHUB_ACTOR is empty"
  check_text login_input "GITHUB_ACTOR" "${actor}"
  [[ "${actor}" =~ ^[^[:space:]:]+$ ]] || err login_input "GITHUB_ACTOR has an unexpected format"
fi

assert_config() {
  [ -f "${config}" ] || err login_unusable "${config} does not exist after the login"
  chmod 600 "${config}"
  python3 -I - "${config}" "${HOST}" "${actor}" <<'PY' || err login_unusable "${config} has no usable ${HOST} entry (see the notes in ci/release/registry-login.sh)"
import base64, binascii, json, sys

path, host, actor = sys.argv[1:4]
try:
    cfg = json.load(open(path, encoding="utf-8"))
    assert isinstance(cfg, dict)
    assert "credsStore" not in cfg and "credHelpers" not in cfg
    entry = cfg["auths"][host]
    auth = entry["auth"]
    assert isinstance(auth, str) and auth
    text = base64.b64decode(auth, validate=True).decode("utf-8")
    assert text.startswith(actor + ":") and len(text) > len(actor) + 1
except (OSError, ValueError, KeyError, TypeError, AssertionError, binascii.Error):
    sys.exit(1)
PY
}

case "${cmd}" in
  login)
    [ -n "${REGISTRY_TOKEN:-}" ] || err login_input "REGISTRY_TOKEN is empty"
    check_text login_input "REGISTRY_TOKEN" "${REGISTRY_TOKEN}"
    need_tool crane
    if ! printf '%s' "${REGISTRY_TOKEN}" | "${TOOLS_BIN}/crane" auth login "${HOST}" -u "${actor}" --password-stdin >/dev/null 2>&1; then
      err login_failed "crane auth login failed"
    fi
    assert_config
    info "logged in to ${HOST} as ${actor} (${config}, mode 0600)"
    ;;
  check)
    assert_config
    info "the login for ${HOST} is usable"
    ;;
  logout)
    if [ -f "${config}" ]; then
      python3 -I - "${config}" "${HOST}" <<'PY' || err login_unusable "${config} could not be updated"
import json, os, sys

path, host = sys.argv[1:3]
try:
    cfg = json.load(open(path, encoding="utf-8"))
    assert isinstance(cfg, dict)
except (OSError, ValueError, AssertionError):
    sys.exit(1)
auths = cfg.get("auths")
if isinstance(auths, dict):
    auths.pop(host, None)
    if not auths:
        del cfg["auths"]
if not cfg:
    os.remove(path)
else:
    fd = os.open(path, os.O_WRONLY | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        json.dump(cfg, f)
PY
    fi
    info "logged out of ${HOST}"
    ;;
esac
