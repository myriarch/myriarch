#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# The one place that composes `cosign verify-blob` for the releases (ADR-0035). The release workflow, the verification before
# publication and the smoke tests call this script, so the identity they check cannot drift apart.
#
#   ci/release/verify-blob.sh --mode exact|pattern|bound --bundle FILE --repository OWNER/NAME --workflow PATH \
#     --tag TAG [--commit SHA] [--trusted-root FILE] BLOB
#
# Production callers pass the values of ci/release/identity.env (RELEASE_REPOSITORY, RELEASE_WORKFLOW). They are arguments so that the
# test can verify the committed OpenTelemetry fixture through the same code. The issuer and the trigger always come from identity.env.
#
# Modes
#   exact    the identity of exactly this run: --certificate-identity https://github.com/OWNER/NAME/PATH@refs/tags/TAG, the issuer, and
#            the claims workflow repository, ref (refs/tags/TAG), sha (SHA, required) and trigger (push) [needs --commit]
#   pattern  the form that users run (ADR-0029): --certificate-identity-regexp '^https://github\.com/OWNER/NAME/PATH-escaped@refs/tags/v'
#            and the issuer
#   bound    the form of the installers: the same regexp, bound to the version of TAG (TAG without "v", escaped, anchored with $), and
#            the issuer; nothing else
# --trusted-root FILE is passed through (offline verification). The cosign binary is ${TOOLS_BIN}/cosign at the pinned version.
#
# A verification that fails is [signature_invalid] with the last line of cosign's stderr (never the bundle).
#
# Exit status: 0 verified, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="verify-blob.sh --mode exact|pattern|bound --bundle FILE --repository OWNER/NAME --workflow PATH --tag TAG [--commit SHA] [--trusted-root FILE] BLOB"
mode="" bundle="" repository="" workflow="" tag="" commit="" trusted_root="" blob=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode) need_value "$1" "$#"; mode="$2"; shift 2 ;;
    --bundle) need_value "$1" "$#"; bundle="$2"; shift 2 ;;
    --repository) need_value "$1" "$#"; repository="$2"; shift 2 ;;
    --workflow) need_value "$1" "$#"; workflow="$2"; shift 2 ;;
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    --trusted-root) need_value "$1" "$#"; trusted_root="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    -*) usage_err "${USAGE}" ;;
    *) [ -z "${blob}" ] || usage_err "${USAGE}"; blob="$1"; shift ;;
  esac
done
[ -n "${mode}" ] && [ -n "${bundle}" ] && [ -n "${repository}" ] && [ -n "${workflow}" ] && [ -n "${tag}" ] && [ -n "${blob}" ] || usage_err "${USAGE}"
case "${mode}" in exact|pattern|bound) ;; *) usage_err "${USAGE}" ;; esac
load_identity
check_match repository_invalid "--repository" "${repository}" '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
check_match workflow_invalid "--workflow" "${workflow}" '^[A-Za-z0-9_.][A-Za-z0-9_./-]*$'
case "${workflow}" in *..*) err workflow_invalid "--workflow has a .. component" ;; esac
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_text file_missing "--bundle" "${bundle}"
check_text file_missing "BLOB" "${blob}"
[ -f "${bundle}" ] || err file_missing "the bundle ${bundle} does not exist"
[ -f "${blob}" ] || err file_missing "the file ${blob} does not exist"
if [ -n "${trusted_root}" ]; then
  check_text file_missing "--trusted-root" "${trusted_root}"
  [ -f "${trusted_root}" ] || err file_missing "the trusted root ${trusted_root} does not exist"
fi
if [ "${mode}" = exact ]; then
  [ -n "${commit}" ] || err commit_invalid "--commit is required in mode exact"
  check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
fi
need_tool cosign

args=(verify-blob --bundle "${bundle}")
case "${mode}" in
  exact)
    args+=(--certificate-identity "$(identity_exact "${tag}" "${repository}" "${workflow}")"
      --certificate-oidc-issuer "${RELEASE_OIDC_ISSUER}"
      --certificate-github-workflow-repository "${repository}"
      --certificate-github-workflow-ref "refs/tags/${tag}"
      --certificate-github-workflow-sha "${commit}"
      --certificate-github-workflow-trigger "${RELEASE_TRIGGER}")
    ;;
  pattern)
    args+=(--certificate-identity-regexp "$(identity_pattern "${repository}" "${workflow}")"
      --certificate-oidc-issuer "${RELEASE_OIDC_ISSUER}")
    ;;
  bound)
    args+=(--certificate-identity-regexp "$(identity_bound "${tag#v}" "${repository}" "${workflow}")"
      --certificate-oidc-issuer "${RELEASE_OIDC_ISSUER}")
    ;;
esac
if [ -n "${trusted_root}" ]; then args+=(--trusted-root "${trusted_root}"); fi
args+=("${blob}")

errfile="$(mktemp "${TMPDIR:-/tmp}/myriarch-verify-blob.XXXXXX")"
trap 'rm -f "${errfile}"' EXIT
if ! "${TOOLS_BIN}/cosign" "${args[@]}" > /dev/null 2> "${errfile}"; then
  last="$(grep -v '^[[:space:]]*$' "${errfile}" | tail -n 1 | cut -c 1-300 || true)"
  err signature_invalid "${mode} verification of ${blob} failed: ${last:-cosign printed nothing}"
fi
info "${mode} verification of ${blob} passed"
