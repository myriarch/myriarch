#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Packages the server chart (deploy/helm/myriarch) for a release with the digest of the signed server image written into it, and
# checks such a package (job `push-chart` and job `verify-chart` of the release workflow, ADR-0035). It needs no network and no
# registry: helm only packages and renders. The same script is the local check of the chart package (ci/tests/chart-package-test.sh).
#
#   TOOLS_BIN=... ci/release/chart-package.sh package --chart DIR --version X.Y.Z[-rc.N] --image-digest sha256:<64 hex> --out DIR
#   TOOLS_BIN=... ci/release/chart-package.sh verify --package FILE --image-digest sha256:<64 hex> [--version X.Y.Z[-rc.N]]
#
# package  copies the chart, writes the digest into the value image.digest of the copy and into the entry of the annotation
#          artifacthub.io/images (the chart in the repository keeps the empty value and the placeholder), and runs
#          `helm package --version V --app-version V` (the chart version is the app version is the tag without v). Then it
#          verifies the result. OUT must be new or empty. stdout: CHART_PACKAGE=<OUT>/myriarch-<V>.tgz
#            [chart_dir] [chart_digest_slot] [chart_annotation_slot]
# verify   renders the package with `helm template` for the default values and for the OpenShift profile and requires that every
#          `image:` of the output is <repository>@<digest> with exactly this digest, at least one image per rendering, that the
#          annotation artifacthub.io/images names the digest, and, with --version, that the chart version and the app version are
#          VERSION. A package without a digest fails here [image_not_digest]. [package_invalid] [chart_version]
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="chart-package.sh package --chart DIR --version VERSION --image-digest DIGEST --out DIR | verify --package FILE --image-digest DIGEST [--version VERSION]"
[ "$#" -ge 1 ] || usage_err "${USAGE}"
cmd="$1"
shift
chart="" version="" digest="" out="" package=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --chart) need_value "$1" "$#"; chart="$2"; shift 2 ;;
    --version) need_value "$1" "$#"; version="$2"; shift 2 ;;
    --image-digest) need_value "$1" "$#"; digest="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    --package) need_value "$1" "$#"; package="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${digest}" ] || usage_err "${USAGE}"
check_match digest_invalid "--image-digest" "${digest}" "${DIGEST_RE}"
[ -z "${version}" ] || check_match version_invalid "--version" "${version}" "${VERSION_RE}"
need_tool helm

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-chart.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

# render_images <package> <helm template arguments...>: the images of one rendering, one per line
render_images() {
  local pkg="$1" rendered
  shift
  rendered="${work}/rendered.yaml"
  "${TOOLS_BIN}/helm" template myriarch "${pkg}" "$@" > "${rendered}" 2> "${work}/helm.err" \
    || err package_invalid "helm template failed for ${pkg}: $(grep -v '^[[:space:]]*$' "${work}/helm.err" | tail -n 1 | cut -c 1-300)"
  sed -n 's/^[[:space:]-]*image:[[:space:]]*//p' "${rendered}" | sed -e 's/[[:space:]]*#.*$//' -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
}

do_verify() {
  local pkg="$1" want_version="$2" profile images n ref chart_yaml
  [ -f "${pkg}" ] || err package_invalid "${pkg} is not a file"
  check_text package_invalid "--package" "${pkg}"
  for profile in "default" "openshift"; do
    case "${profile}" in
      default) images="$(render_images "${pkg}")" ;;
      openshift) images="$(render_images "${pkg}" --set openshift.enabled=true)" ;;
    esac
    n=0
    while IFS= read -r ref; do
      [ -n "${ref}" ] || continue
      [[ "${ref}" == *"@${digest}" ]] && [[ "${ref}" =~ ^[a-z0-9.:/_-]+@sha256:[0-9a-f]{64}$ ]] \
        || err image_not_digest "the ${profile} rendering references an image that is not <repository>@${digest}: ${ref}"
      n=$((n + 1))
    done <<EOT
${images}
EOT
    [ "${n}" -gt 0 ] || err image_not_digest "the ${profile} rendering of ${pkg} references no image"
    info "${profile} rendering: ${n} image reference(s), all by the digest ${digest}"
  done
  chart_yaml="$("${TOOLS_BIN}/helm" show chart "${pkg}" 2> "${work}/helm.err")" || err package_invalid "helm show chart failed for ${pkg}"
  if ! printf '%s\n' "${chart_yaml}" | grep -Eq "^[[:space:]]+image: [a-z0-9.:/_-]+@${digest}\$"; then
    err chart_annotation_slot "the annotation artifacthub.io/images of ${pkg} does not name the digest ${digest}"
  fi
  if [ -n "${want_version}" ]; then
    printf '%s\n' "${chart_yaml}" | grep -Fxq "version: ${want_version}" || err chart_version "the chart version of ${pkg} is not ${want_version}"
    printf '%s\n' "${chart_yaml}" | grep -Fxq "appVersion: ${want_version}" || err chart_version "the app version of ${pkg} is not ${want_version}"
  fi
  info "${pkg} verified"
}

case "${cmd}" in
  package)
    [ -n "${chart}" ] && [ -n "${version}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
    check_text chart_dir "--chart" "${chart}"
    check_text chart_dir "--out" "${out}"
    [ -f "${chart}/Chart.yaml" ] && [ -f "${chart}/values.yaml" ] || err chart_dir "${chart} is not a chart directory"
    if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err chart_dir "${out} exists and is not empty"; fi
    cp -R "${chart}" "${work}/myriarch"
    python3 -I - "${work}/myriarch" "${digest}" <<'PY' || exit 1
import re, sys

chart, digest = sys.argv[1:3]


def fail(code, msg):
    print("[chart-package] ERROR [%s] %s" % (code, msg), file=sys.stderr)
    sys.exit(1)


# values.yaml: the line `digest: ""` inside the top-level block `image:`
path = chart + "/values.yaml"
lines = open(path, encoding="utf-8").read().split("\n")
inside, hits = False, []
for i, line in enumerate(lines):
    if re.match(r"^\S", line):
        inside = line.rstrip() == "image:"
    elif inside and line == '  digest: ""':
        hits.append(i)
if len(hits) != 1:
    fail("chart_digest_slot", 'values.yaml must have exactly one line `  digest: ""` in the block `image:`, it has %d' % len(hits))
lines[hits[0]] = '  digest: "%s"' % digest
open(path, "w", encoding="utf-8").write("\n".join(lines))

# Chart.yaml: the entry `image: <repository>` of the annotation artifacthub.io/images gets the digest
path = chart + "/Chart.yaml"
text = open(path, encoding="utf-8").read()
new, n = re.subn(r"(?m)^(\s+image: ghcr\.io/myriarch/myriarch)$", r"\1@" + digest, text)
if n != 1:
    fail("chart_annotation_slot", "Chart.yaml must have exactly one entry `image: ghcr.io/myriarch/myriarch` in artifacthub.io/images, it has %d" % n)
open(path, "w", encoding="utf-8").write(new)
PY
    mkdir -p "${out}"
    "${TOOLS_BIN}/helm" package "${work}/myriarch" --version "${version}" --app-version "${version}" --destination "${out}" > "${work}/helm.log" 2>&1 \
      || err package_invalid "helm package failed: $(tail -n 1 "${work}/helm.log" | cut -c 1-300)"
    [ -f "${out}/myriarch-${version}.tgz" ] || err package_invalid "helm package did not write myriarch-${version}.tgz"
    do_verify "${out}/myriarch-${version}.tgz" "${version}"
    printf 'CHART_PACKAGE=%s\n' "${out}/myriarch-${version}.tgz"
    ;;
  verify)
    [ -n "${package}" ] || usage_err "${USAGE}"
    do_verify "${package}" "${version}"
    ;;
  *) usage_err "${USAGE}" ;;
esac
