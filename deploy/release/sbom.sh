#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# SBOMs of the release build in SPDX JSON, made with the pinned syft. goreleaser calls it from deploy/release/goreleaser.yaml (sboms:)
# with the working directory dist/.
#
#   sbom.sh archive <artifact> <document>      one SBOM for a .tar.gz or .zip archive
#   sbom.sh webui <version> <document>         one SBOM for the production dependencies of web/pnpm-lock.yaml
#
# Every run uses deploy/release/syft.yaml (-c, which also stops syft from reading a .syft.yaml of the working directory or of the user):
# no network access and no --enrich, so the SBOM depends only on the scanned files. SYFT_* variables of the caller are dropped, and
# SYFT_CHECK_FOR_APP_UPDATE=false keeps syft from looking for a newer release. The pins (syft, yq) are read from ci/versions.env,
# not from the environment, and the tools have to report exactly the pinned version.
#
# The tools come from ${TOOLS_BIN} only, never from PATH and never from the default directory of ci/lib.sh: goreleaser starts an
# sboms command with a reduced environment (HOME, USER, TMPDIR, PATH and a few more, plus sboms[].env), so deploy/release/goreleaser.yaml
# passes TOOLS_BIN explicitly. Without TOOLS_BIN the script stops instead of looking around.
#
# The web UI SBOM is made from a lockfile that only holds the production dependencies (ci/release/pnpm-prod-lock.py), because syft
# cannot drop the dev dependencies of a pnpm lockfile. SPDX output is not reproducible (syft writes a random documentNamespace and the
# creation time); the rehearsal compares SBOMs without these two fields.
set -euo pipefail
if [ -z "${TOOLS_BIN:-}" ]; then
  printf '[ci] ERROR: [tools_bin_missing] TOOLS_BIN is not set: deploy/release/goreleaser.yaml passes it to the sboms commands of goreleaser\n' >&2
  exit 1
fi
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../../ci/lib.sh"
SYFT="${TOOLS_BIN}/syft"
YQ="${TOOLS_BIN}/yq"

pin() { sed -n "s/^$1=//p" "${CI_DIR}/versions.env" | head -n 1; }
SYFT_PIN="$(pin SYFT_VERSION)"
YQ_PIN="$(pin YQ_VERSION)"
[ -n "${SYFT_PIN}" ] && [ -n "${YQ_PIN}" ] || die "[pins_missing] SYFT_VERSION or YQ_VERSION is missing in ci/versions.env"

while IFS= read -r v; do unset "${v}"; done < <(compgen -e | grep '^SYFT_' || true)
export SYFT_CHECK_FOR_APP_UPDATE=false

[ -x "${SYFT}" ] || die "[syft_version] syft not found in TOOLS_BIN (${TOOLS_BIN}): ci/install-tools.sh syft"
got="$("${SYFT}" version 2>/dev/null | awk '$1 == "Version:" { print $2; exit }')" || got=""
[ "${got}" = "${SYFT_PIN}" ] || die "[syft_version] syft reports ${got:-nothing}, expected ${SYFT_PIN} (ci/install-tools.sh syft)"
CONFIG="${ROOT}/deploy/release/syft.yaml"

mode="${1:-}"
case "${mode}" in
  archive)
    [ "$#" = 3 ] || die "usage: sbom.sh archive <artifact> <document>"
    artifact="$2"; document="$3"
    case "${artifact}" in *.tar.gz|*.zip) ;; *) die "[sbom_artifact] ${artifact} is not a .tar.gz or .zip archive" ;; esac
    [ -f "${artifact}" ] && [ ! -L "${artifact}" ] || die "[sbom_artifact] ${artifact} is not a regular file"
    "${SYFT}" scan -q -c "${CONFIG}" --from file "${artifact}" -o "spdx-json=${document}" ;;
  webui)
    [ "$#" = 3 ] || die "usage: sbom.sh webui <version> <document>"
    version="$2"; document="$3"
    [[ "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || die "[sbom_version] '${version}' is not a version"
    [ -x "${YQ}" ] || die "[yq_version] yq not found in TOOLS_BIN (${TOOLS_BIN}): ci/install-tools.sh yq"
    case "$("${YQ}" --version 2>/dev/null)" in
      *"version v${YQ_PIN#v}") ;;
      *) die "[yq_version] yq does not report version v${YQ_PIN#v} (ci/install-tools.sh yq)" ;;
    esac
    tmp="$(mktemp -d)"
    trap 'rm -rf "${tmp}"' EXIT
    "${YQ}" -o=json '.' "${ROOT}/web/pnpm-lock.yaml" > "${tmp}/lock.json"
    python3 -I "${ROOT}/ci/release/pnpm-prod-lock.py" --package-json "${ROOT}/web/package.json" < "${tmp}/lock.json" > "${tmp}/pnpm-lock.yaml"
    "${SYFT}" scan -q -c "${CONFIG}" --from file "${tmp}/pnpm-lock.yaml" --source-name myriarch-webui --source-version "${version}" \
      -o "spdx-json=${document}" ;;
  *) die "usage: sbom.sh archive <artifact> <document> | webui <version> <document>" ;;
esac

# Syft lists its own temporary extraction directory of an archive as a file (".../syft-archive-contents-<random>", with the build
# host's TMPDIR in the path). It is not in the archive: it is dropped with the relationships that name it, the rest of the document
# (documentNamespace and creationInfo.created included) stays as syft wrote it. Then the sanity check.
python3 -I - "${document}" <<'PY' || die "[sbom_empty] ${document} is not an SPDX 2.3 JSON document with at least one package"
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    doc = json.load(f)
assert doc.get("spdxVersion") == "SPDX-2.3" and len(doc.get("packages") or []) >= 1
files = doc.get("files")
if files:
    dropped = {f.get("SPDXID") for f in files if "syft-archive-contents-" in str(f.get("fileName"))}
    if dropped:
        kept = [f for f in files if f.get("SPDXID") not in dropped]
        if kept:
            doc["files"] = kept
        else:
            del doc["files"]
        if "relationships" in doc:
            doc["relationships"] = [r for r in doc["relationships"]
                                    if r.get("spdxElementId") not in dropped and r.get("relatedSpdxElement") not in dropped]
        for p in doc["packages"]:
            if "hasFiles" in p:
                p["hasFiles"] = [x for x in p["hasFiles"] if x not in dropped]
        with open(sys.argv[1], "w", encoding="utf-8") as f:
            json.dump(doc, f, separators=(",", ":"))
            f.write("\n")
PY
