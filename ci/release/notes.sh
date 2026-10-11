#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Renders the release notes of a tag from deploy/release/notes.md.tmpl and CHANGELOG.md (job `draft` of the release workflow,
# ADR-0035). Privileged job: it runs with a PATH without go, node, pnpm, make and goreleaser.
#
#   ci/release/notes.sh --tag TAG --commit SHA --signer FILE --out FILE [--template FILE] [--changelog FILE]
#
# Environment: WINDOWS_SIGNING (off|signpath|kms), WINDOWS_UNSIGNED_FINAL_TAG (empty or one final tag), IMAGE_TAGS (comma separated:
# X.Y.Z-rc.N for an rc tag, X.Y.Z,X.Y for a final tag, as meta.sh computes them), SERVER_INDEX_DIGEST, UPDATER_INDEX_DIGEST.
# --signer is the signer.json of the Windows signing hook (deploy/release/README.md). The defaults of the other options are
# deploy/release/notes.md.tmpl and CHANGELOG.md of this checkout.
#
# Rendering (python3 -I)
#   1. The header of the template (its leading "#" lines and blank lines) is dropped. "##" headings are kept.
#   2. Blocks "<!-- if NAME -->" ... "<!-- end NAME -->" (each on its own line, no nesting, all closed) are kept or removed. NAME is
#      rc (rc tag), final (final tag), unsigned (WINDOWS_SIGNING=off), unsigned-exception (final tag with off) or signed (any other
#      mode). The signer file has to agree with the mode: off needs signed false and provider off; another mode needs signed
#      true, provider equal to the mode and a subject [signer_mismatch]; the exception needs WINDOWS_UNSIGNED_FINAL_TAG equal to the
#      tag [exception_mismatch].
#   3. {{NAME}} placeholders are replaced; an unknown one, an unknown block name or a broken block is [template_invalid], and
#      anything of the template syntax left in the text before the changelog is inserted is [template_leftover].
#   4. CHANGELOG_SECTION: for an rc tag the body of "## Unreleased" (or "## [Unreleased]"), for a final tag the body of
#      "## [X.Y.Z] - YYYY-MM-DD", up to the next "## " heading, without the heading. Fenced code blocks keep their text but hide
#      headings; HTML comments are removed. An empty section is [changelog_missing]. A section of more than 60,000 characters is
#      replaced by one line that links CHANGELOG.md at the tag.
#   5. The text is at most 100,000 characters [notes_too_long], contains no SPDX line, and ends with exactly one newline.
#
# Exit status: 0 ok, 1 refused, 2 usage error.
set -euo pipefail
# shellcheck source=ci/release/common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

USAGE="notes.sh --tag TAG --commit SHA --signer FILE --out FILE [--template FILE] [--changelog FILE]"
tag="" commit="" signer="" out="" template="${ROOT}/deploy/release/notes.md.tmpl" changelog="${ROOT}/CHANGELOG.md"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) need_value "$1" "$#"; tag="$2"; shift 2 ;;
    --commit) need_value "$1" "$#"; commit="$2"; shift 2 ;;
    --signer) need_value "$1" "$#"; signer="$2"; shift 2 ;;
    --out) need_value "$1" "$#"; out="$2"; shift 2 ;;
    --template) need_value "$1" "$#"; template="$2"; shift 2 ;;
    --changelog) need_value "$1" "$#"; changelog="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e '/^set -euo/d' -e 's/^# \{0,1\}//'; exit 0 ;;
    *) usage_err "${USAGE}" ;;
  esac
done
[ -n "${tag}" ] && [ -n "${commit}" ] && [ -n "${signer}" ] && [ -n "${out}" ] || usage_err "${USAGE}"
load_identity
check_match tag_invalid "--tag" "${tag}" "${TAG_RE}"
check_match commit_invalid "--commit" "${commit}" "${COMMIT_RE}"
for v in "${signer}" "${out}" "${template}" "${changelog}"; do check_text file_missing "a path argument" "${v}"; done
[ -f "${signer}" ] || err file_missing "the signer file ${signer} does not exist"
[ -f "${template}" ] || err file_missing "the template ${template} does not exist"
[ -f "${changelog}" ] || err file_missing "${changelog} does not exist"
[ ! -e "${out}" ] || err out_exists "${out} exists already"

mode="${WINDOWS_SIGNING:-}"
exception="${WINDOWS_UNSIGNED_FINAL_TAG:-}"
image_tags="${IMAGE_TAGS:-}"
server_digest="${SERVER_INDEX_DIGEST:-}"
updater_digest="${UPDATER_INDEX_DIGEST:-}"
check_text signer_mismatch "WINDOWS_SIGNING" "${mode}"
case "${mode}" in off|signpath|kms) ;; *) err signer_mismatch "WINDOWS_SIGNING is not off, signpath or kms" ;; esac
check_text exception_mismatch "WINDOWS_UNSIGNED_FINAL_TAG" "${exception}"
check_match image_tags_invalid "IMAGE_TAGS" "${image_tags}" '^[0-9A-Za-z.,-]+$'
check_match image_digest_invalid "SERVER_INDEX_DIGEST" "${server_digest}" "${DIGEST_RE}"
check_match image_digest_invalid "UPDATER_INDEX_DIGEST" "${updater_digest}" "${DIGEST_RE}"

version="${tag#v}"
base="${version%%-*}"
kind=final
want_tags="${base},${base%.*}"
case "${version}" in *-rc.*) kind=rc; want_tags="${version}" ;; esac
[ "${image_tags}" = "${want_tags}" ] || err image_tags_invalid "IMAGE_TAGS '${image_tags}' is not '${want_tags}', the tags of ${tag}"

# the tags as Markdown: `0.1.0` and `0.1`
tags_md=""
old_ifs="${IFS}"
IFS=,
for t in ${image_tags}; do
  if [ -z "${tags_md}" ]; then tags_md="\`${t}\`"; else tags_md="${tags_md} and \`${t}\`"; fi
done
IFS="${old_ifs}"

python3 -I - "${template}" "${changelog}" "${signer}" "${out}" "${tag}" "${kind}" "${mode}" "${exception}" \
  "TAG=${tag}" "VERSION=${version}" "BASE_VERSION=${base}" "COMMIT=${commit}" "REPOSITORY=${RELEASE_REPOSITORY}" \
  "IDENTITY=$(identity_exact "${tag}")" "IDENTITY_REGEXP=$(identity_pattern)" "IDENTITY_BOUND_REGEXP=$(identity_bound "${version}")" \
  "OIDC_ISSUER=${RELEASE_OIDC_ISSUER}" "SIGNER_WORKFLOW=$(signer_workflow "${tag}")" \
  "SERVER_IMAGE=${RELEASE_IMAGE_SERVER}" "UPDATER_IMAGE=${RELEASE_IMAGE_UPDATER}" "SERVER_DIGEST=${server_digest}" "UPDATER_DIGEST=${updater_digest}" \
  "IMAGE_TAGS=${tags_md}" "PROVENANCE_ASSET=myriarch_${version}_provenance.intoto.jsonl" \
  "UPDATER_ASSET_LINUX_AMD64=myriarch-updater_${version}_linux_amd64" \
  "CHANGELOG_URL=https://github.com/${RELEASE_REPOSITORY}/blob/${tag}/CHANGELOG.md" <<'PY'
import datetime, json, re, sys

(template_path, changelog_path, signer_path, out_path, tag, kind, mode, exception) = sys.argv[1:9]
values = {}
for arg in sys.argv[9:]:
    name, _, value = arg.partition("=")
    values[name] = value
SCRIPT = "notes"
SECTION_LIMIT = 60000
NOTES_LIMIT = 100000


def fail(code, msg):
    print("[%s] ERROR [%s] %s" % (SCRIPT, code, msg), file=sys.stderr)
    sys.exit(1)


# ---- the signer file against the mode -----------------------------------------------------------------------------------------
try:
    signer = json.load(open(signer_path, encoding="utf-8"))
    assert isinstance(signer, dict)
except (OSError, ValueError, AssertionError):
    fail("signer_mismatch", "the signer file is not a JSON object")
if mode == "off":
    if signer.get("signed") is not False or signer.get("provider") != "off":
        fail("signer_mismatch", "WINDOWS_SIGNING is off but the signer file does not say signed false and provider off")
else:
    subject = signer.get("subject")
    if signer.get("signed") is not True or signer.get("provider") != mode or not isinstance(subject, str) or not subject.strip():
        fail("signer_mismatch", "WINDOWS_SIGNING is %s but the signer file does not say signed true, provider %s and a subject" % (mode, mode))
    if len(subject) > 300 or re.search(r"[\x00-\x1f\x7f`]|\{\{|<!--", subject):
        fail("signer_mismatch", "the signer subject has characters that cannot go into the notes")
    values["WINDOWS_SIGNER_SUBJECT"] = subject
    values["WINDOWS_SIGNER_PROVIDER"] = mode
if kind == "final" and mode == "off" and exception != tag:
    fail("exception_mismatch", "a final tag with unsigned Windows files needs WINDOWS_UNSIGNED_FINAL_TAG equal to %s" % tag)

active = {
    "rc": kind == "rc",
    "final": kind == "final",
    "unsigned": mode == "off",
    "unsigned-exception": kind == "final" and mode == "off",
    "signed": mode != "off",
}

# ---- the template ---------------------------------------------------------------------------------------------------------------
lines = open(template_path, encoding="utf-8").read().split("\n")
while lines and (lines[0].strip() == "" or lines[0] == "#" or lines[0].startswith("# ")):
    lines.pop(0)
body, block = [], None
RESTRICTED = ("WINDOWS_SIGNER_SUBJECT", "WINDOWS_SIGNER_PROVIDER")
KNOWN = set(values) | set(RESTRICTED) | {"CHANGELOG_SECTION"}
for line in lines:
    m = re.match(r"^<!-- (if|end) ([A-Za-z0-9-]+) -->\s*$", line)
    if m:
        op, name = m.groups()
        if name not in active:
            fail("template_invalid", "unknown block name '%s'" % name)
        if op == "if":
            if block is not None:
                fail("template_invalid", "block '%s' starts inside block '%s' (blocks do not nest)" % (name, block))
            block = name
        else:
            if block != name:
                fail("template_invalid", "'end %s' does not close the open block" % name)
            block = None
        continue
    for p in re.findall(r"\{\{([A-Za-z0-9_]*)\}\}", line):
        if p not in KNOWN:
            fail("template_invalid", "unknown placeholder {{%s}}" % p)
        if p in RESTRICTED and block != "signed":
            fail("template_invalid", "{{%s}} is only allowed inside the 'signed' block" % p)
    if block is None or active[block]:
        body.append(line)
if block is not None:
    fail("template_invalid", "block '%s' is not closed" % block)

# one blank line at most between the parts of the template, no blank lines at its ends
text, blank = [], False
for line in body:
    if line.strip() == "":
        if blank or not text:
            continue
        blank = True
    else:
        blank = False
    text.append(line)
while text and text[-1].strip() == "":
    text.pop()
text = "\n".join(text)

SENTINEL = "\x00CHANGELOG\x00"
text = text.replace("{{CHANGELOG_SECTION}}", SENTINEL)
text = re.sub(r"\{\{([A-Za-z0-9_]+)\}\}", lambda m: values[m.group(1)], text)
if "{{" in text or "}}" in text or "<!-- if" in text or "<!-- end" in text:
    fail("template_leftover", "the rendered text still has template syntax")


# ---- the changelog section --------------------------------------------------------------------------------------------------------
def sections(path):
    """[(heading, [lines])] of the '## ' sections. A '## ' line in a fenced code block or an HTML comment is no heading; fenced text
    stays verbatim, HTML comments are removed."""
    out, fence, comment = [], None, False
    for raw in open(path, encoding="utf-8").read().split("\n"):
        line = raw.rstrip("\r")
        if fence is None and not comment:
            m = re.match(r"^ {0,3}(`{3,}|~{3,})", line)
            if m:
                fence = m.group(1)[0] * 3
                if out:
                    out[-1][1].append(line)
                continue
        elif fence is not None:
            if re.match(r"^ {0,3}" + re.escape(fence[0]) + r"{3,}\s*$", line):
                fence = None
            if out:
                out[-1][1].append(line)
            continue
        kept, rest = "", line
        while rest:
            if comment:
                end = rest.find("-->")
                if end < 0:
                    rest = ""
                else:
                    comment = False
                    rest = rest[end + 3:]
            else:
                start = rest.find("<!--")
                if start < 0:
                    kept += rest
                    rest = ""
                else:
                    kept += rest[:start]
                    comment = True
                    rest = rest[start + 4:]
        if kept == "" and line != "":
            continue
        if kept.startswith("## "):
            out.append((kept, []))
        elif out:
            if kept.strip() == "" and out[-1][1] and out[-1][1][-1].strip() == "":
                continue  # a removed comment leaves two blank lines: keep one
            out[-1][1].append(kept)
    return out


version = tag[1:]
if kind == "rc":
    pick = [b for h, b in sections(changelog_path) if re.match(r"^## \[?Unreleased\]?\s*$", h)]
else:
    pat = re.compile(r"^## \[" + re.escape(version) + r"\] - (\d{4}-\d{2}-\d{2})\s*$")
    pick = []
    for h, b in sections(changelog_path):
        m = pat.match(h)
        if m:
            try:
                datetime.date.fromisoformat(m.group(1))
            except ValueError:
                continue
            pick.append(b)
if not pick:
    fail("changelog_missing", "CHANGELOG.md has no section for %s" % ("Unreleased" if kind == "rc" else version))
body = list(pick[0])
while body and body[0].strip() == "":
    body.pop(0)
while body and body[-1].strip() == "":
    body.pop()
section = "\n".join(body)
if not section.strip():
    fail("changelog_missing", "the CHANGELOG.md section for %s is empty" % ("Unreleased" if kind == "rc" else version))
if len(section) > SECTION_LIMIT:
    info = "The changes of this release are listed in [CHANGELOG.md](%s)." % values["CHANGELOG_URL"]
    print("[%s] the changelog section has %d characters; the notes link to CHANGELOG.md instead" % (SCRIPT, len(section)), file=sys.stderr)
    section = info
text = text.replace(SENTINEL, section)

text = text.rstrip("\n") + "\n"
if len(text) > NOTES_LIMIT:
    fail("notes_too_long", "the notes have %d characters, the limit is %d" % (len(text), NOTES_LIMIT))
if "SPDX-License-Identifier" in text:
    fail("template_leftover", "the notes contain an SPDX line (the header of the template must be dropped)")
with open(out_path, "x", encoding="utf-8") as f:
    f.write(text)
print("[%s] wrote %s (%d characters)" % (SCRIPT, out_path, len(text)), file=sys.stderr)
PY
