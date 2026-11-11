#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Packages a chart of the release for a release with the digest of the signed image written into it, turns the package into an OCI
# layout, and checks such a package (job `chart` and job `verify-chart` of the release workflow, ADR-0035). Two charts are released: the
# server chart (deploy/helm/myriarch, name myriarch, which references the server image) and the collector chart
# (deploy/helm/myriarch-collector, name myriarch-collector, which references the updater image and two unchanged upstream images). It needs
# no network and no real registry: helm only packages and renders, and the layout comes from a throwaway registry on 127.0.0.1. The same
# script is the local check of the chart package (ci/tests/chart-package-test.sh).
#
#   TOOLS_BIN=... ci/release/chart-package.sh package [--chart-name NAME] --chart DIR --version X.Y.Z[-rc.N] --image-digest sha256:<64 hex> --out DIR
#   TOOLS_BIN=... ci/release/chart-package.sh layout [--chart-name NAME] --package FILE --out DIR
#   TOOLS_BIN=... ci/release/chart-package.sh verify [--chart-name NAME] --package FILE --image-digest sha256:<64 hex> [--version X.Y.Z[-rc.N]]
#
# --chart-name selects the chart: myriarch (the default, the server chart) or myriarch-collector [chart_name]. For `package` it must be
#          the name in Chart.yaml of --chart, for `verify` the name in the package [chart_dir] [package_invalid]. --image-digest is the
#          digest of the image index that the chart references: RELEASE_IMAGE_SERVER for myriarch, RELEASE_IMAGE_UPDATER for
#          myriarch-collector (the two upstream images of the collector chart are pinned in values.yaml and never change here).
#
# package  copies the chart, writes the digest into the slot of the copy and into the entry of the annotation
#          artifacthub.io/images (the chart in the repository keeps the empty value and the placeholder), and runs
#          `helm package --version V --app-version V` (the chart version is the app version is the tag without v). The slot is the
#          line `  digest: ""` of the block `image:` (myriarch) or the line `    digest: ""` of `images:` -> `updater:` (myriarch-collector),
#          the entry is `image: RELEASE_IMAGE_SERVER` or `image: RELEASE_IMAGE_UPDATER`; each must exist exactly once. It rewrites every
#          relative link of README.md in the copy to the repository at the tag v<version> (a file /blob/, a directory /tree/, the
#          anchor kept; links with a scheme, #anchors and text in fenced code blocks, code spans and HTML comments stay), because Artifact Hub shows the README of the package and
#          relative links do not resolve there. The targets are read from `git ls-tree` of HEAD, so a sparse checkout is enough; a link
#          to a path that HEAD does not have, one that leaves the repository, starts with /, is a relative image or cannot be rewritten
#          fails [chart_readme_link]. The README in the repository keeps its relative links. It needs git and a checkout of the
#          repository. Then it verifies the result. OUT must be new or empty. stdout: CHART_PACKAGE=<OUT>/<NAME>-<V>.tgz
#            [chart_dir] [chart_digest_slot] [chart_annotation_slot]
# layout   pushes the package with `helm push --plain-http` to a throwaway registry (`crane registry serve` on 127.0.0.1, an ephemeral
#          port, stopped at the end) and pulls the manifest by digest into the OCI layout OUT, which must be new or empty. The layout
#          holds exactly what `helm push` writes to ghcr.io, so its digest is the digest of the published chart; push-chart.sh pushes it
#          by digest, without a tag. stdout: chart-digest=<digest> (a GITHUB_OUTPUT line).  [chart_dir] [layout_failed]
# verify   renders the package with `helm template` and requires that every `image:` of the output is as the chart allows, at least
#          one image per rendering, that the annotation artifacthub.io/images names the image of the release at the digest, and, with
#          --version, that the chart version and the app version are VERSION, and that README.md of the package has no relative link
#          [chart_readme_link]. A package without a digest, or with the digest of another repository, fails here
#          [image_not_digest] [package_invalid] [chart_version] [chart_readme_link]
#            myriarch            default values and the OpenShift profile: every image is RELEASE_IMAGE_SERVER@<digest>
#            myriarch-collector  default (StatefulSet), mode=daemonset, containerMode.enabled and the OpenShift profile: every image is
#                                RELEASE_IMAGE_UPDATER@<digest> or one of the two upstream pins of ci/versions.env
#                                (OTELCOL_CONTRIB_IMAGE, OTELCOL_SUPERVISOR_IMAGE, rendered as docker.io/<repository>@<digest>), and
#                                each rendering has the updater and both upstream images
#
# Exit status: 0 ok, 1 refused or failed, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="chart-package.sh package [--chart-name NAME] --chart DIR --version VERSION --image-digest DIGEST --out DIR | layout [--chart-name NAME] --package FILE --out DIR | verify [--chart-name NAME] --package FILE --image-digest DIGEST [--version VERSION]"
[ "$#" -ge 1 ] || usage_err "${USAGE}"
cmd="$1"
shift
chart="" version="" digest="" out="" package="" name="myriarch"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --chart-name) need_value "$1" "$#"; name="$2"; shift 2 ;;
    --chart) need_value "$1" "$#"; chart="$2"; shift 2 ;;
    --version) need_value "$1" "$#"; version="$2"; shift 2 ;;
    --image-digest) need_value "$1" "$#"; digest="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    --package) need_value "$1" "$#"; package="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
if [ "${cmd}" != layout ]; then
  [ -n "${digest}" ] || usage_err "${USAGE}"
  check_match digest_invalid "--image-digest" "${digest}" "${DIGEST_RE}"
fi
case "${name}" in myriarch|myriarch-collector) ;; *) err chart_name "--chart-name must be myriarch or myriarch-collector" ;; esac
[ -z "${version}" ] || check_match version_invalid "--version" "${version}" "${VERSION_RE}"
load_identity
need_tool helm
case "${name}" in
  myriarch) release_image="${RELEASE_IMAGE_SERVER}" ;;
  myriarch-collector) release_image="${RELEASE_IMAGE_UPDATER}" ;;
esac

work="$(mktemp -d "${TMPDIR:-/tmp}/myriarch-chart.XXXXXX")"
regpid=""
trap '[ -z "${regpid}" ] || kill "${regpid}" 2> /dev/null || true; rm -rf "${work}"' EXIT

# render_images <package> <helm template arguments...>: the images of one rendering, one per line
render_images() {
  local pkg="$1" rendered
  shift
  rendered="${work}/rendered.yaml"
  "${TOOLS_BIN}/helm" template myriarch "${pkg}" "$@" > "${rendered}" 2> "${work}/helm.err" \
    || err package_invalid "helm template failed for ${pkg}: $(grep -v '^[[:space:]]*$' "${work}/helm.err" | tail -n 1 | cut -c 1-300)"
  sed -n 's/^[[:space:]-]*image:[[:space:]]*//p' "${rendered}" | sed -e 's/[[:space:]]*#.*$//' -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
}

# upstream_pin <NAME>: the reference that the collector chart renders for the pin NAME of ci/versions.env (`repo:tag@sha256:...`),
# which is docker.io/<repo>@sha256:... (the tag is dropped, the digest decides)
upstream_pin() {
  local value
  value="$(pin "$1")"
  [[ "${value}" =~ ^([a-z0-9][a-z0-9._/-]*):[A-Za-z0-9._-]+@(sha256:[0-9a-f]{64})$ ]] || err tool_version "the pin $1 of ci/versions.env is not repository:tag@sha256:<64 hex>"
  printf 'docker.io/%s@%s' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
}

# readme_links rewrite <README> <tag> <chart path in the repository> <ls-tree file>: writes every relative link of the README as an
# absolute link to the repository at the tag (a file /blob/, a directory /tree/, query and anchor kept) and fails [chart_readme_link]
# for a target that the tree does not have, that leaves the repository, starts with /, is a relative image, or has a form that cannot be
# rewritten. readme_links check <README> <label>: fails [chart_readme_link] when a relative link is left. Links with a scheme, #anchors
# and everything in fenced code blocks, code spans and HTML comments stay. The tree file is `git ls-tree -r -t -z` of the commit.
readme_links() {
  python3 -I - "${RELEASE_REPOSITORY}" "$@" <<'PY' || exit 1
import posixpath, re, sys
from urllib.parse import quote, unquote, urlsplit

repo, mode = sys.argv[1:3]


def fail(msg):
    print("[chart-package] ERROR [chart_readme_link] %s" % msg, file=sys.stderr)
    sys.exit(1)


def mask(text):
    # a copy of the text in which fenced code blocks, HTML comments and code spans are NUL characters (newlines and offsets stay)
    if "\0" in text:
        fail("README.md contains a NUL character")
    out = list(text)

    def blank(a, b):
        for i in range(a, b):
            if out[i] != "\n":
                out[i] = "\0"

    pos, fence, start = 0, None, 0
    for line in text.splitlines(True):
        body = line.rstrip("\r\n")
        if fence is None:
            m = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", body)
            if m and not (m.group(1)[0] == "`" and "`" in m.group(2)):
                fence, start = m.group(1), pos
        else:
            m = re.match(r"^ {0,3}(" + re.escape(fence[0]) + "{" + str(len(fence)) + r",})[ \t]*$", body)
            if m:
                blank(start, pos + len(body))
                fence = None
        pos += len(line)
    if fence is not None:
        blank(start, len(text))
    # one left-to-right pass: the construct that starts first wins (a comment or a code span). A code span may continue over a line
    # ending inside a paragraph, but not over a blank or masked line, and not into a list item, heading or table row.
    stop = r"\n[ \t>]*(?:\n|\0|[-*+][ \t]|\d{1,9}[.)][ \t]|#{1,6}[ \t]|\|)"
    for m in re.finditer(r"<!--.*?(?:-->|\Z)|(?<!`)(`+)(?!`)(?:(?!" + stop + r").)+?(?<!`)\1(?!`)", "".join(out), re.S):
        blank(m.start(), m.end())
    return "".join(out)


SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
INLINE = re.compile(
    r"(?P<img>!?)\[(?:[^\[\]]|\[[^\[\]]*\])*\]\([ \t\r\n]*(?P<target><[^<>\r\n]*>|[^\s()<>]+)"
    r"""(?:[ \t\r\n]+(?:"[^"]*"|'[^']*'|\([^()]*\)))?[ \t\r\n]*\)"""
)
REFDEF = re.compile(r"(?m)^ {0,3}\[[^\]\r\n]+\]:[ \t]*(?:\r?\n[ \t]*)?(?P<target><[^<>\r\n]*>|\S+)")
LEFT = [
    re.compile(r"\]\([ \t\r\n]*(<[^<>\r\n]*>|[^\s()<>]*)"),
    REFDEF,
    re.compile(r"""(?i)\b(?:href|src)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))"""),
]


def lineno(text, off):
    return text.count("\n", 0, off) + 1


def leftovers(masked):
    # the relative targets that are left: (offset, target)
    found = []
    for rx in LEFT:
        for m in rx.finditer(masked):
            g = [x for x in m.groups() if x is not None]
            t = g[-1] if g else ""
            if t.startswith("<") and t.endswith(">"):
                t = t[1:-1]
            if not (SCHEME.match(t) or t.startswith("#")):
                found.append((m.start(), t))
    return sorted(found)


if mode == "check":
    path, label = sys.argv[3:5]
    text = open(path, encoding="utf-8", newline="").read()
    left = leftovers(mask(text))
    if left:
        print("[chart-package] ERROR [chart_readme_link] %s: README.md still has the relative link %s on line %d" % (label, left[0][1], lineno(text, left[0][0])), file=sys.stderr)
        sys.exit(1)
    sys.exit(0)

path, tag, chart, treefile = sys.argv[3:7]
types = {}
for rec in open(treefile, "rb").read().split(b"\0"):
    meta, _, p = rec.partition(b"\t")
    if p:
        types[p.decode("utf-8")] = meta.split()[1].decode("ascii")
types["."] = "tree"
text = open(path, encoding="utf-8", newline="").read()
masked = mask(text)
base = "https://github.com/%s" % repo
edits = []
for rx in (INLINE, REFDEF):
    for m in rx.finditer(masked):
        t, a, b = m.group("target"), m.start("target"), m.end("target")
        angle = t.startswith("<") and t.endswith(">")
        if angle:
            t = t[1:-1]
        where = "README.md, line %d: " % lineno(text, a)
        if SCHEME.match(t) or t.startswith("#"):
            continue
        if t.startswith("//") or t.startswith("/"):
            fail(where + "the link %s is site-absolute or protocol-relative; write it relative to the README" % t)
        if t == "":
            fail(where + "the link has an empty target")
        if "img" in rx.groupindex and m.group("img"):
            fail(where + "the image %s is relative and does not render from a blob URL; use an https URL" % t)
        sp = urlsplit(t)
        rel = posixpath.normpath(posixpath.join(chart, unquote(sp.path)))
        if rel == ".." or rel.startswith("../"):
            fail(where + "the link %s leaves the repository" % t)
        kind = types.get(rel)
        if kind not in ("blob", "tree"):
            fail(where + "the link %s names %s, which is not a file or directory of the commit HEAD (is it committed?)" % (t, rel))
        url = "%s/%s/%s%s" % (base, kind, tag, "" if rel == "." else "/" + quote(rel, safe="/"))
        if sp.query:
            url += "?" + sp.query
        if sp.fragment:
            url += "#" + sp.fragment
        if angle:
            url = "<" + url + ">"
        edits.append((a, b, url))
for a, b, url in sorted(edits, reverse=True):
    text = text[:a] + url + text[b:]
left = leftovers(mask(text))
if left:
    fail("README.md, line %d: the link %s cannot be rewritten (write it as [text](relative/path))" % (lineno(text, left[0][0]), left[0][1]))
open(path, "w", encoding="utf-8", newline="").write(text)
print("[chart-package] README.md: %d relative link(s) now point to %s/{blob,tree}/%s/" % (len(edits), base, tag), file=sys.stderr)
PY
}

do_verify() {
  local pkg="$1" want_version="$2" profile profiles images n ref chart_yaml pkg_name want_ref i ok seen
  local -a profile_args allowed
  [ -f "${pkg}" ] || err package_invalid "${pkg} is not a file"
  check_text package_invalid "--package" "${pkg}"
  want_ref="${release_image}@${digest}"
  case "${name}" in
    myriarch) allowed=("${want_ref}"); profiles="default openshift" ;;
    myriarch-collector)
      allowed=("${want_ref}" "$(upstream_pin OTELCOL_CONTRIB_IMAGE)" "$(upstream_pin OTELCOL_SUPERVISOR_IMAGE)")
      profiles="default daemonset container openshift" ;;
  esac
  for profile in ${profiles}; do
    case "${profile}" in
      default) profile_args=() ;;
      daemonset) profile_args=(--set mode=daemonset) ;;
      container) profile_args=(--set containerMode.enabled=true --set containerMode.trustedRoot.existingConfigMap=x) ;;
      openshift) profile_args=(--set openshift.enabled=true) ;;
    esac
    images="$(render_images "${pkg}" ${profile_args[@]+"${profile_args[@]}"})"
    n=0
    seen=" "
    while IFS= read -r ref; do
      [ -n "${ref}" ] || continue
      ok=""
      for i in "${!allowed[@]}"; do
        if [ "${ref}" = "${allowed[$i]}" ]; then ok=1; seen="${seen}${i} "; fi
      done
      if [ -z "${ok}" ]; then
        if [ "${name}" = myriarch ]; then
          err image_not_digest "the ${profile} rendering references an image that is not ${want_ref}: ${ref}"
        fi
        err image_not_digest "the ${profile} rendering references an image that is neither ${want_ref} nor an upstream pin of ci/versions.env: ${ref}"
      fi
      n=$((n + 1))
    done <<EOT
${images}
EOT
    [ "${n}" -gt 0 ] || err image_not_digest "the ${profile} rendering of ${pkg} references no image"
    if [ "${name}" = myriarch ]; then
      info "${profile} rendering: ${n} image reference(s), all ${want_ref}"
    else
      for i in "${!allowed[@]}"; do
        case "${seen}" in *" ${i} "*) ;; *) err image_not_digest "the ${profile} rendering does not reference ${allowed[$i]}" ;; esac
      done
      info "${profile} rendering: ${n} image reference(s), the updater at ${digest} and the two upstream pins"
    fi
  done
  chart_yaml="$("${TOOLS_BIN}/helm" show chart "${pkg}" 2> "${work}/helm.err")" || err package_invalid "helm show chart failed for ${pkg}"
  pkg_name="$(printf '%s\n' "${chart_yaml}" | sed -n 's/^name: //p' | head -n 1)"
  [ "${pkg_name}" = "${name}" ] || err package_invalid "${pkg} is the chart '${pkg_name}', not ${name}"
  if ! printf '%s\n' "${chart_yaml}" | awk -v want="image: ${want_ref}" '{ sub(/^[ \t]+/, "") } $0 == want { found = 1 } END { exit !found }'; then
    err chart_annotation_slot "the annotation artifacthub.io/images of ${pkg} does not name ${want_ref}"
  fi
  if [ -n "${want_version}" ]; then
    printf '%s\n' "${chart_yaml}" | grep -Fxq "version: ${want_version}" || err chart_version "the chart version of ${pkg} is not ${want_version}"
    printf '%s\n' "${chart_yaml}" | grep -Fxq "appVersion: ${want_version}" || err chart_version "the app version of ${pkg} is not ${want_version}"
  fi
  # the README of the package is what Artifact Hub shows: no relative link may be left in it
  "${TOOLS_BIN}/helm" show readme "${pkg}" > "${work}/readme.md" 2> "${work}/helm.err" || err package_invalid "helm show readme failed for ${pkg}"
  [ -s "${work}/readme.md" ] || err chart_readme_link "${pkg} has no README.md"
  readme_links check "${work}/readme.md" "${pkg}"
  info "${pkg} verified"
}

case "${cmd}" in
  package)
    [ -n "${chart}" ] && [ -n "${version}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
    check_text chart_dir "--chart" "${chart}"
    check_text chart_dir "--out" "${out}"
    [ -f "${chart}/Chart.yaml" ] && [ -f "${chart}/values.yaml" ] || err chart_dir "${chart} is not a chart directory"
    [ -f "${chart}/README.md" ] || err chart_dir "${chart} has no README.md"
    have git || err tool_missing "git is needed to check the links of README.md"
    if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err chart_dir "${out} exists and is not empty"; fi
    [ "$(sed -n 's/^name:[[:space:]]*//p' "${chart}/Chart.yaml" | head -n 1)" = "${name}" ] || err chart_dir "${chart} is not the chart ${name} (name in Chart.yaml)"
    cp -R "${chart}" "${work}/${name}"
    python3 -I - "${work}/${name}" "${digest}" "${name}" "${release_image}" <<'PY' || exit 1
import re, sys

chart, digest, name, image = sys.argv[1:5]


def fail(code, msg):
    print("[chart-package] ERROR [%s] %s" % (code, msg), file=sys.stderr)
    sys.exit(1)


# values.yaml: the server chart has the line `  digest: ""` inside the top-level block `image:`; the collector chart has the line
# `    digest: ""` inside the block `updater:` of the top-level block `images:` (the two upstream images keep their pins)
if name == "myriarch":
    top, sub, slot, where = "image:", None, '  digest: ""', "the block `image:`"
else:
    top, sub, slot, where = "images:", "  updater:", '    digest: ""', "the block `updater:` of `images:`"
path = chart + "/values.yaml"
lines = open(path, encoding="utf-8").read().split("\n")
inside, in_sub, hits = False, sub is None, []
for i, line in enumerate(lines):
    if re.match(r"^\S", line):
        inside = line.rstrip() == top
        in_sub = sub is None
    elif inside and sub is not None and re.match(r"^  \S", line) and not line.startswith("  #"):
        in_sub = line.rstrip() == sub
    elif inside and in_sub and line == slot:
        hits.append(i)
if len(hits) != 1:
    fail("chart_digest_slot", 'values.yaml must have exactly one line `%s` in %s, it has %d' % (slot.strip(), where, len(hits)))
lines[hits[0]] = slot[: len(slot) - 2] + '"%s"' % digest
open(path, "w", encoding="utf-8").write("\n".join(lines))

# Chart.yaml: the entry `image: <repository>` of the annotation artifacthub.io/images gets the digest
path = chart + "/Chart.yaml"
text = open(path, encoding="utf-8").read()
new, n = re.subn(r"(?m)^(\s+image: " + re.escape(image) + r")$", lambda m: m.group(1) + "@" + digest, text)
if n != 1:
    fail("chart_annotation_slot", "Chart.yaml must have exactly one entry `image: %s` in artifacthub.io/images, it has %d" % (image, n))
open(path, "w", encoding="utf-8").write(new)
PY
    # the relative links of the README of the copy become links to the repository at the tag; the targets come from the tree of HEAD
    # (never from the work tree: the job `chart` checks out only a few paths, but the trees of HEAD are complete)
    git -C "${ROOT}" ls-tree -r -t -z --full-tree HEAD > "${work}/tree" 2> "${work}/git.err" \
      || err chart_readme_link "git ls-tree HEAD failed in ${ROOT}: $(grep -v '^[[:space:]]*$' "${work}/git.err" | tail -n 1 | cut -c 1-300)"
    readme_links rewrite "${work}/${name}/README.md" "v${version}" "deploy/helm/${name}" "${work}/tree"
    mkdir -p "${out}"
    "${TOOLS_BIN}/helm" package "${work}/${name}" --version "${version}" --app-version "${version}" --destination "${out}" > "${work}/helm.log" 2>&1 \
      || err package_invalid "helm package failed: $(tail -n 1 "${work}/helm.log" | cut -c 1-300)"
    [ -f "${out}/${name}-${version}.tgz" ] || err package_invalid "helm package did not write ${name}-${version}.tgz"
    do_verify "${out}/${name}-${version}.tgz" "${version}"
    printf 'CHART_PACKAGE=%s\n' "${out}/${name}-${version}.tgz"
    ;;
  layout)
    [ -n "${package}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
    check_text chart_dir "--package" "${package}"
    check_text chart_dir "--out" "${out}"
    need_tool crane
    [ -f "${package}" ] || err chart_dir "${package} is not a file"
    if [ -d "${out}" ] && [ -n "$(ls -A "${out}")" ]; then err chart_dir "${out} exists and is not empty"; fi
    # a throwaway registry on an ephemeral port of the loopback interface; it holds the chart in memory and ends with this script
    "${TOOLS_BIN}/crane" registry serve --address 127.0.0.1:0 > /dev/null 2> "${work}/registry.log" &
    regpid=$!
    disown "${regpid}" 2> /dev/null || true
    port=""
    for _ in $(seq 1 50); do
      port="$(sed -n 's/.*serving on port \([0-9][0-9]*\).*/\1/p' "${work}/registry.log" | head -n 1)"
      [ -z "${port}" ] || break
      kill -0 "${regpid}" 2> /dev/null || break
      sleep 0.2
    done
    [ -n "${port}" ] || err layout_failed "the throwaway registry did not start: $(grep -v '^[[:space:]]*$' "${work}/registry.log" | tail -n 1 | cut -c 1-300)"
    : > "${work}/registry-config.json"
    HELM_REGISTRY_CONFIG="${work}/registry-config.json" "${TOOLS_BIN}/helm" push "${package}" "oci://127.0.0.1:${port}/charts" --plain-http > "${work}/push.out" 2> "${work}/helm.err" \
      || err layout_failed "helm push to the throwaway registry failed: $(grep -v '^[[:space:]]*$' "${work}/helm.err" | tail -n 1 | cut -c 1-300)"
    pushed="$(sed -n 's/^Digest: //p' "${work}/push.out" | head -n 1)"
    check_match layout_failed "the digest that helm push printed" "${pushed}" "${DIGEST_RE}"
    mkdir -p "$(dirname "${out}")"
    "${TOOLS_BIN}/crane" pull --format oci "127.0.0.1:${port}/charts/${name}@${pushed}" "${out}" > /dev/null 2> "${work}/crane.err" \
      || err layout_failed "crane could not pull the chart as a layout: $(grep -v '^[[:space:]]*$' "${work}/crane.err" | tail -n 1 | cut -c 1-300)"
    python3 -I - "${out}" "${pushed}" <<'PY' || exit 1
import json, sys

out, want = sys.argv[1:3]
index = json.load(open(out + "/index.json", encoding="utf-8"))
manifests = index.get("manifests") or []
if len(manifests) != 1 or manifests[0].get("digest") != want:
    print("[chart-package] ERROR [layout_failed] the layout must hold exactly the manifest %s" % want, file=sys.stderr)
    sys.exit(1)
PY
    printf 'chart-digest=%s\n' "${pushed}"
    ;;
  verify)
    [ -n "${package}" ] || usage_err "${USAGE}"
    do_verify "${package}" "${version}"
    ;;
  *) usage_err "${USAGE}" ;;
esac
