#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016,SC2034  # "A && ok || fail": ok/fail are counters; single quotes are intended (code for python); eval-ed variables
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Tests of the release scripts in ci/release (ADR-0035): identity.env, meta.sh, artifact.sh, tools.sh (check), stage-assets.sh and
# verify-dist.sh with deploy/release/assets.json, verify-blob.sh, sign-checksums.sh, sign-image.sh, registry-login.sh, notes.sh,
# draft.sh, verify.sh and finalize.sh, plus the hook in ci/release/rehearsal.sh. No network, no ports, no container runtime.
#
#  - Fixtures are generated in a temporary directory (a fake dist, the 42 release files, a crafted provenance bundle, image
#    manifests and configs, release lists). The only committed fixture is the upstream Sigstore set of internal/updater/testdata/sigstore.
#  - cosign, gh and crane are stubs in a TOOLS_BIN directory that log their arguments (one per line, "----" between calls) and serve
#    answers from state files. The scripts that the privileged jobs of the release workflow call run with a PATH that has no go, node,
#    pnpm, npm, make or goreleaser (the case asserts that); the tests prove the scripts need none of them.
#  - Case 6 runs the REAL pinned cosign (.cache/bin/cosign, built by `ci/install-tools.sh cosign`) offline against the upstream fixture:
#    positive controls for the three modes and the refusals with cosign's own texts. Without the binary the case is skipped locally and
#    FAILS when CI=true.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
load_versions

REL="${ROOT}/ci/release"
S="${TEST_TMP}/stubs"            # STUB_DIR: state of the stubs
SB="${TEST_TMP}/stubbin"         # TOOLS_BIN of the stub cases
RB="${TEST_TMP}/restricted"      # PATH of the privileged scripts
GEN="${TEST_TMP}/gen/gen.py"
HOME_DIR="${TEST_TMP}/home"
mkdir -p "${S}/calls" "${S}/crane" "${SB}" "${RB}" "${TEST_TMP}/gen" "${HOME_DIR}"

# ---- the PATH without go, node, pnpm, npm, make and goreleaser -----------------------------------------------------------------------
for t in bash sh env python3 git tar gzip sha256sum shasum sed awk grep find mktemp date curl cat cp ln mv rm mkdir chmod ls head tail cut tr wc sort \
         sleep basename dirname readlink base64 uniq diff cmp touch; do
  p="$(command -v "${t}" 2>/dev/null || true)"
  [ -n "${p}" ] && [ -x "${p}" ] || continue
  printf '#!/bin/sh\nexec "%s" "$@"\n' "${p}" > "${RB}/${t}"
  chmod +x "${RB}/${t}"
done
( export PATH="${RB}"; for t in go node pnpm npm make goreleaser corepack; do ! command -v "${t}" >/dev/null 2>&1 || exit 1; done ) \
  && ok "the restricted PATH has no go, node, pnpm, npm, make, goreleaser or corepack" || fail "the restricted PATH is not restricted"

# ---- stubs ---------------------------------------------------------------------------------------------------------------------------
cat > "${SB}/cosign" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = version ]; then printf 'GitVersion:    v3.1.3\n'; exit 0; fi
{ printf '%s\n' "$@"; echo '----'; } >> "${d}/calls/cosign.log"
echo "cosign $1" >> "${d}/calls/all.log"
all="$*"
if [ -f "${d}/cosign.fail" ] && [[ "${all}" =~ $(cat "${d}/cosign.fail") ]]; then
  echo "Error: configured stub failure" >&2
  echo "Error: no matching signatures: stub" >&2
  exit 1
fi
if [ "$1" = sign-blob ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --bundle ]; then printf '{"stub":true}\n' > "$2"; break; fi
    shift
  done
fi
if [ "$1" = verify ]; then
  # like cosign v3: one JSON entry per verified Sigstore bundle of the digest; cosign.type picks what the registry holds
  ref="${*: -1}"
  type="https://sigstore.dev/cosign/sign/v1"
  [ ! -f "${d}/cosign.type" ] || type="$(cat "${d}/cosign.type")"
  printf '[{"critical":{"identity":{"docker-reference":"%s"},"image":{"docker-manifest-digest":"%s"},"type":"%s"},"optional":{}}]\n' "${ref%@*}" "${ref#*@}" "${type}"
fi
exit 0
STUB
cat > "${SB}/gh" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = --version ]; then printf 'gh version 2.102.0 (2026-10-01)\nhttps://github.com/cli/cli/releases/tag/v2.102.0\n'; exit 0; fi
{ printf '%s\n' "$@"; echo '----'; } >> "${d}/calls/gh.log"
echo "gh $*" >> "${d}/calls/all.log"
all="$*"
if [ -f "${d}/gh.fail" ] && [[ "${all}" =~ $(cat "${d}/gh.fail") ]]; then echo "Error: configured stub failure" >&2; exit 1; fi
counter() { local n=0; [ -f "${d}/$1" ] && n="$(cat "${d}/$1")"; n=$((n + 1)); echo "${n}" > "${d}/$1"; echo "${n}"; }
case "$1" in
  api)
    shift
    method=GET path=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -X) method="$2"; shift 2 ;;
        --paginate|--slurp) shift ;;
        -F|-f) shift 2 ;;
        *) path="$1"; shift ;;
      esac
    done
    case "${method}:${path}" in
      GET:repos/*/releases\?per_page=100) n="$(counter list.count)"; f="${d}/list.${n}.json"; [ -f "${f}" ] || f="${d}/list.json"; cat "${f}" ;;
      GET:repos/*/git/ref/tags/*) n="$(counter ref.count)"; f="${d}/ref.${n}.json"; [ -f "${f}" ] || f="${d}/ref.json"
        # ref.<n>.404 (or ref.404): the n-th read of the ref answers like the API for a missing ref
        if [ -f "${d}/ref.${n}.404" ] || [ -f "${d}/ref.404" ]; then echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
        cat "${f}" ;;
      GET:repos/*/git/tags/*) if [ -f "${d}/tagobj.${path##*/}.404" ]; then echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
        cat "${d}/tagobj.${path##*/}.json" ;;
      GET:repos/*/releases/*)
        # the n-th read of a release is release.<id>.<n>.json when that file exists (a release that changes between two reads)
        # after the PATCH the verified state is published (draft false); a numbered variant is returned as it is
        n="$(counter "release.count.${path##*/}")"; f="${d}/release.${path##*/}.${n}.json"
        if [ -f "${f}" ]; then cat "${f}"
        elif [ -f "${d}/patched" ]; then sed 's/"draft": true/"draft": false/' "${d}/release.${path##*/}.json"
        else cat "${d}/release.${path##*/}.json"; fi ;;
      PATCH:*) [ ! -f "${d}/patch.rc" ] || exit "$(cat "${d}/patch.rc")"; : > "${d}/patched" ;;
    esac ;;
  release)
    case "${2:-}" in
      create) [ ! -f "${d}/create.rc" ] || exit "$(cat "${d}/create.rc")" ;;
      view) cat "${d}/view.json" ;;
      verify|verify-asset)
        f="${d}/${2}.failures"
        if [ -f "${f}" ] && [ "$(cat "${f}")" -gt 0 ]; then echo $(( $(cat "${f}") - 1 )) > "${f}"; echo "Error: no attestation yet" >&2; exit 1; fi ;;
    esac ;;
esac
exit 0
STUB
cat > "${SB}/crane" <<'STUB'
#!/usr/bin/env bash
set -u
d="${STUB_DIR:?}"
if [ "${1:-}" = version ]; then echo v0.22.1; exit 0; fi
{ printf '%s\n' "$@"; echo "DOCKER_CONFIG=${DOCKER_CONFIG-unset}"; echo '----'; } >> "${d}/calls/crane.log"
echo "crane $1 ${2-}" >> "${d}/calls/all.log"
key() { printf '%s' "$1" | tr '/:@' '___'; }
cfg_dir="${DOCKER_CONFIG:-${HOME}/.docker}"
case "$1" in
  auth)
    tok="$(cat)"
    user=""
    args=("$@")
    for i in "${!args[@]}"; do [ "${args[$i]}" = -u ] && user="${args[$((i + 1))]}"; done
    [ ! -f "${d}/crane.login.rc" ] || exit "$(cat "${d}/crane.login.rc")"
    mkdir -p "${cfg_dir}"
    auth="$(printf '%s:%s' "${user}" "${tok}" | base64 | tr -d '\n')"
    # a usable auth entry together with a helper: only the credsStore/credHelpers rule of registry-login.sh may refuse it
    if [ -f "${d}/crane.login.helper" ]; then printf '{"credsStore":"desktop","auths":{"ghcr.io":{"auth":"%s"}}}\n' "${auth}" > "${cfg_dir}/config.json"
    elif [ -f "${d}/crane.login.credhelpers" ]; then printf '{"credHelpers":{"ghcr.io":"desktop"},"auths":{"ghcr.io":{"auth":"%s"}}}\n' "${auth}" > "${cfg_dir}/config.json"
    else printf '{"auths":{"ghcr.io":{"auth":"%s"}}}\n' "${auth}" > "${cfg_dir}/config.json"; fi
    chmod 644 "${cfg_dir}/config.json"
    echo "logged in with ${tok}" >&2 ;;
  manifest|config|digest|ls)
    if [ -f "${d}/crane.anon.fail" ] && [ -n "${DOCKER_CONFIG:-}" ]; then echo "UNAUTHORIZED: stub" >&2; exit 1; fi
    if [ -f "${d}/crane.anon.fail.chart" ] && [ -n "${DOCKER_CONFIG:-}" ] && [[ "$2" == */charts/* ]]; then echo "UNAUTHORIZED: stub" >&2; exit 1; fi
    f="${d}/crane/$1.$(key "$2")"
    [ -f "${f}" ] || { echo "stub crane: no state for $1 $2" >&2; exit 1; }
    cat "${f}" ;;
  tag)
    [ ! -f "${d}/crane.tag.rc" ] || exit "$(cat "${d}/crane.tag.rc")"
    repo="${2%@*}"; digest="${2#*@}"
    [ ! -f "${d}/crane.tag.wrong" ] || digest="$(cat "${d}/crane.tag.wrong")"
    echo "${digest}" > "${d}/crane/digest.$(key "${repo}:$3")"
    echo "$3" >> "${d}/crane/ls.$(key "${repo}")" ;;
esac
exit 0
STUB
chmod +x "${SB}/cosign" "${SB}/gh" "${SB}/crane"

# reset_stubs: a clean STUB_DIR (call logs and state files)
reset_stubs() { rm -rf "${S}"; mkdir -p "${S}/calls" "${S}/crane"; rm -rf "${HOME_DIR}"; mkdir -p "${HOME_DIR}"; }
# run_priv <command...>: the command with the restricted PATH, the stub tools and a private HOME
run_priv() { ( export PATH="${RB}" TOOLS_BIN="${SB}" STUB_DIR="${S}" HOME="${HOME_DIR}"; unset DOCKER_CONFIG GITHUB_STEP_SUMMARY
  if [ -n "${RP_DOCKER_CONFIG:-}" ]; then export DOCKER_CONFIG="${RP_DOCKER_CONFIG}"; fi
  if [ -n "${RP_SUMMARY:-}" ]; then export GITHUB_STEP_SUMMARY="${RP_SUMMARY}"; fi
  "$@" ); }
# calls <tool>: the call log; call_count <tool> <regex over the arguments of a call joined by spaces>
calls() { cat "${S}/calls/$1.log" 2>/dev/null || true; }
call_count() { python3 -I - "${S}/calls/$1.log" "$2" <<'PY'
import re, sys
try:
    text = open(sys.argv[1], encoding="utf-8").read()
except OSError:
    print(0); sys.exit(0)
print(sum(1 for c in text.split("----\n") if c.strip() and re.search(sys.argv[2], " ".join(c.split("\n")).strip())))
PY
}
# call_line <tool> <n>: the arguments of the n-th call (1-based) joined by single spaces
call_line() { python3 -I - "${S}/calls/$1.log" "$2" <<'PY'
import sys
calls = [c for c in open(sys.argv[1], encoding="utf-8").read().split("----\n") if c.strip()]
print(" ".join(x for x in calls[int(sys.argv[2]) - 1].split("\n") if x))
PY
}
has_call() { [ "$(call_count "$1" "$2")" -ge 1 ]; }
no_call() { [ "$(call_count "$1" "$2")" -eq 0 ]; }

# ---- the generator of fixtures (python3 -I) ----------------------------------------------------------------------------------------------
cat > "${GEN}" <<'PYGEN'
import base64, hashlib, json, os, sys

REPO_S, REPO_U = "ghcr.io/myriarch/myriarch", "ghcr.io/myriarch/myriarch-updater"
SOURCE = "https://github.com/myriarch/myriarch"


def sha(b):
    return hashlib.sha256(b).hexdigest()


def key(ref):
    return ref.replace("/", "_").replace(":", "_").replace("@", "_")


def expected_names(v):
    """The 42 names of a release, written out independently of deploy/release/assets.json."""
    archives = (["myriarchd_%s_%s.tar.gz" % (v, p) for p in ("darwin_arm64", "linux_amd64", "linux_arm64")]
                + ["myriarch-updater_%s_%s.tar.gz" % (v, p) for p in ("linux_amd64", "linux_arm64")]
                + ["myriarch-updater_%s_windows_%s.zip" % (v, a) for a in ("amd64", "arm64")]
                + ["myrctl_%s_%s.tar.gz" % (v, p) for p in ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64")]
                + ["myrctl_%s_windows_%s.zip" % (v, a) for a in ("amd64", "arm64")]
                + ["myriarch-sim_%s_%s.tar.gz" % (v, p) for p in ("darwin_arm64", "linux_amd64", "linux_arm64")])
    binaries = (["myriarch-updater_%s_linux_%s" % (v, a) for a in ("amd64", "arm64")]
                + ["myriarch-updater_%s_windows_%s.exe" % (v, a) for a in ("amd64", "arm64")])
    sboms = [a + ".sbom.json" for a in archives] + ["myriarch-webui_%s.sbom.json" % v]
    listed = archives + binaries + sboms + ["LICENSE", "THIRD_PARTY_NOTICES"]
    final = listed + ["checksums.txt", "checksums.txt.sigstore.json", "myriarch_%s_provenance.intoto.jsonl" % v]
    return archives, binaries, sboms, sorted(listed), sorted(final)


def content(name, v):
    return ("content of %s %s\n" % (name, v)).encode()


def write(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)


def sums_text(listed, v, overrides=None):
    lines = []
    for n in listed:
        lines.append("%s  %s" % ((overrides or {}).get(n) or sha(content(n, v)), n))
    return ("\n".join(lines) + "\n").encode()


def bundle(sums_path, mode="ok"):
    subjects = []
    for line in open(sums_path, encoding="utf-8").read().split("\n"):
        if line:
            h, n = line.split("  ")
            subjects.append({"name": n, "digest": {"sha256": h}})
    pred = "https://slsa.dev/provenance/v1"
    media = "application/vnd.dev.sigstore.bundle.v0.3+json"
    ptype = "application/vnd.in-toto+json"
    if mode == "missing":
        subjects = subjects[1:]
    elif mode == "extra":
        subjects.append({"name": "extra-file", "digest": {"sha256": "0" * 64}})
    elif mode == "wrongdigest":
        subjects[0]["digest"]["sha256"] = "f" * 64
    elif mode == "wrongpredicate":
        pred = "https://slsa.dev/provenance/v0.2"
    elif mode == "badmedia":
        media = "application/json"
    elif mode == "nopayloadtype":
        ptype = "text/plain"
    statement = {"_type": "https://in-toto.io/Statement/v1", "subject": subjects, "predicateType": pred, "predicate": {}}
    env = {"payload": base64.b64encode(json.dumps(statement).encode()).decode(), "payloadType": ptype, "signatures": [{"sig": "AAAA"}]}
    line = json.dumps({"mediaType": media, "verificationMaterial": {}, "dsseEnvelope": env})
    return (line + "\n" + (line + "\n" if mode == "twolines" else "")).encode()


def cmd_assets(v, assets_dir, sig_dir, *opts):
    """The 40 files of stage unsigned in assets_dir, the signature and the provenance file in sig_dir."""
    archives, binaries, sboms, listed, final = expected_names(v)
    for n in listed:
        write(os.path.join(assets_dir, n), content(n, v))
    write(os.path.join(assets_dir, "checksums.txt"), sums_text(listed, v))
    write(os.path.join(sig_dir, "checksums.txt.sigstore.json"), b'{"stub":true}\n')
    write(os.path.join(sig_dir, "myriarch_%s_provenance.intoto.jsonl" % v), bundle(os.path.join(assets_dir, "checksums.txt")))


def cmd_bundle(sums, out, mode):
    write(out, bundle(sums, mode))


def cmd_dist(dist, root, v, *opts):
    """A fake goreleaser dist/ and a repository root with LICENSE and THIRD_PARTY_NOTICES."""
    o = dict(x.split("=", 1) for x in opts)
    archives, binaries, sboms, listed, final = expected_names(v)
    arts = [{"name": "metadata.json", "path": "dist/metadata.json", "type": "Metadata"}]
    for n in archives:
        write(os.path.join(dist, n), content(n, v))
        arts.append({"name": n, "path": "dist/" + n, "type": "Archive", "extra": {"Format": "tar.gz", "ID": n.split("_")[0]}})
    for n in sboms:
        write(os.path.join(dist, n), content(n, v))
        arts.append({"name": n, "path": "dist/" + n, "type": "SBOM", "extra": {"ID": "archives"}})
    for n in binaries:
        sub = "dist/bin_%s/%s" % (n.replace(".", "_"), "myriarch-updater")
        write(os.path.join(dist, sub[len("dist/"):]), content(n, v))
        arts.append({"name": n, "path": sub, "type": "Binary", "extra": {"Format": "binary", "ID": "myriarch-updater-binary"}})
    arts.append({"name": "myriarchd", "path": "dist/myriarchd_linux_amd64_v1/myriarchd", "type": "Binary", "extra": {"ID": "myriarchd"}})
    for n in ("LICENSE", "THIRD_PARTY_NOTICES"):
        write(os.path.join(root, n), content(n, v))
    overrides = {}
    if o.get("badhash"):
        overrides[o["badhash"]] = "a" * 64
    write(os.path.join(dist, "checksums.txt"), sums_text(listed, v, overrides))
    arts.append({"name": "checksums.txt", "path": "dist/checksums.txt", "type": "Checksum"})
    if o.get("missing"):
        arts = [a for a in arts if a["name"] != o["missing"]]
    if o.get("extra"):
        write(os.path.join(dist, o["extra"]), b"extra\n")
        arts.append({"name": o["extra"], "path": "dist/" + o["extra"], "type": "Archive", "extra": {"Format": "tar.gz", "ID": "x"}})
    if o.get("nofile"):
        os.remove(os.path.join(dist, o["nofile"]))
    write(os.path.join(dist, "artifacts.json"), json.dumps(arts).encode())
    write(os.path.join(dist, "metadata.json"), json.dumps({"project_name": "myriarch", "version": o.get("metaversion", v)}).encode())


def cmd_images(state, commit, v, *opts):
    """Manifests, configs and tag lists for both images in the crane stub's state directory; prints the digest assignments."""
    o = dict(x.split("=", 1) for x in opts)
    out = []
    for kind, repo, ep in (("SERVER", REPO_S, "/usr/local/bin/myriarchd"), ("UPDATER", REPO_U, "/usr/local/bin/myriarch-updater")):
        labels = {"org.opencontainers.image.version": v, "org.opencontainers.image.revision": o.get("labelrevision", commit),
                  "org.opencontainers.image.source": SOURCE}
        plats = {}
        for arch in ("amd64", "arm64"):
            cfg = {"architecture": arch, "os": "linux",
                   "config": {"User": o.get("user", "65532:65532"), "Entrypoint": [o.get("entrypoint", ep)], "Labels": labels},
                   "rootfs": {"type": "layers", "diff_ids": ["sha256:" + sha(("layer" + kind + arch).encode())]}}
            cfg_raw = json.dumps(cfg, sort_keys=True).encode()
            layers = [{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + sha(b"l" + arch.encode()), "size": 10}]
            if o.get("layers") == "2":
                layers.append(layers[0])
            man = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                   "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + sha(cfg_raw), "size": len(cfg_raw)},
                   "layers": layers}
            man_raw = json.dumps(man, sort_keys=True).encode()
            d = "sha256:" + sha(man_raw)
            plats[arch] = (d, len(man_raw))
            write(os.path.join(state, "manifest." + key("%s@%s" % (repo, d))), man_raw)
            write(os.path.join(state, "config." + key("%s@%s" % (repo, d))), cfg_raw)
        manifests = [{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": d, "size": n,
                      "platform": {"os": "linux", "architecture": a}} for a, (d, n) in plats.items()]
        if o.get("noarm"):
            manifests = manifests[:1]
        if o.get("thirdplatform"):
            manifests.append({"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + "1" * 64, "size": 5,
                              "platform": {"os": "linux", "architecture": "s390x"}})
        idx = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": manifests,
               "annotations": {"org.opencontainers.image.source": SOURCE, "org.opencontainers.image.version": v,
                               "org.opencontainers.image.revision": o.get("indexrevision", commit)}}
        idx_raw = json.dumps(idx, sort_keys=True).encode()
        di = "sha256:" + sha(idx_raw)
        write(os.path.join(state, "manifest." + key("%s@%s" % (repo, di))), idx_raw)
        tags = ["sha256-" + sha(b"t%d" % i + kind.encode()) for i in (1, 2, 3)] + [t for t in o.get("tags", "").split(",") if t]
        write(os.path.join(state, "ls." + key(repo)), ("\n".join(tags) + "\n").encode())
        out.append("%s_INDEX_DIGEST=%s" % (kind, di))
        out.append("%s_AMD64_DIGEST=%s" % (kind, plats["amd64"][0]))
        out.append("%s_ARM64_DIGEST=%s" % (kind, plats["arm64"][0]))
    print("\n".join(out))


def cmd_verified(out_dir, assets_dir, sig_dir, v, s_idx, s_amd, s_arm, u_idx, u_amd, u_arm):
    """The artifact of the verify job."""
    archives, binaries, sboms, listed, final = expected_names(v)
    lines = []
    for n in final:
        p = os.path.join(assets_dir, n)
        if not os.path.exists(p):
            p = os.path.join(sig_dir, n)
        data = open(p, "rb").read()
        lines.append("%s  %d  %s" % (sha(data), len(data), n))
    write(os.path.join(out_dir, "verified-manifest.txt"), ("\n".join(lines) + "\n").encode())
    img = []
    for repo, i, a, r in ((REPO_S, s_idx, s_amd, s_arm), (REPO_U, u_idx, u_amd, u_arm)):
        img += ["%s index %s" % (repo, i), "%s linux/amd64 %s" % (repo, a), "%s linux/arm64 %s" % (repo, r)]
    write(os.path.join(out_dir, "verified-images.txt"), ("\n".join(img) + "\n").encode())
    write(os.path.join(out_dir, "checksums.txt"), open(os.path.join(assets_dir, "checksums.txt"), "rb").read())


def release_obj(rid, rtag, v, manifest_path, **o):
    rows = [l.split("  ", 2) for l in open(manifest_path, encoding="utf-8").read().split("\n") if l]
    assets = []
    for h, size, name in rows:
        a = {"name": name, "state": o.get("state", "uploaded"), "uploader": {"login": o.get("uploader", "github-actions[bot]")},
             "size": int(size), "digest": "sha256:" + h}
        if o.get("digest_wrong") == name:
            a["digest"] = "sha256:" + "e" * 64
        if o.get("digest_null") == name:
            a["digest"] = None
        if o.get("size_wrong") == name:
            a["size"] += 1
        assets.append(a)
    if o.get("drop"):
        assets = [a for a in assets if a["name"] != o["drop"]]
    if o.get("extra"):
        assets.append({"name": o["extra"], "state": "uploaded", "uploader": {"login": "github-actions[bot]"}, "size": 1, "digest": "sha256:" + "d" * 64})
    return {"id": int(rid), "tag_name": o.get("tag", rtag), "name": o.get("name", rtag), "draft": o.get("draft", "1") == "1",
            "prerelease": o.get("prerelease", "1" if "-rc." in rtag else "0") == "1", "author": {"login": o.get("author", "github-actions[bot]")},
            "assets": assets}


def cmd_release(state, rid, tag, v, manifest_path, *opts):
    """release.<id>.json and list.json (the --slurp shape: a list of pages) of the gh stub."""
    o = dict(x.split("=", 1) for x in opts)
    r = release_obj(rid, tag, v, manifest_path, **o)
    write(os.path.join(state, "release.%s.json" % rid), json.dumps(r).encode())
    others = []
    for spec in filter(None, o.get("others", "").split(";")):
        oid, odraft, oauthor = spec.split(",")
        others.append(release_obj(oid, tag, v, manifest_path, draft=odraft, author=oauthor))
    write(os.path.join(state, "list.json"), json.dumps([[r] + others]).encode())


def cmd_list(state, name, tag, specs):
    """A release list from 'id,draft,author,asset names separated by +;...' (assets of the names given)."""
    rels = []
    for spec in filter(None, specs.split(";")):
        rid, draft, author, names, pre = (spec.split(",") + ["", ""])[:5]
        rels.append({"id": int(rid), "tag_name": tag, "name": tag, "draft": draft == "1", "prerelease": ("-rc." in tag) if pre == "" else pre == "1",
                     "author": {"login": author}, "assets": [{"name": n} for n in names.split("+") if n]})
    write(os.path.join(state, name), json.dumps([rels]).encode())


def cmd_names(v, which):
    archives, binaries, sboms, listed, final = expected_names(v)
    print("\n".join({"final": final, "listed": listed, "unsigned": sorted(listed + ["checksums.txt"])}[which]))


if __name__ == "__main__":
    globals()["cmd_" + sys.argv[1]](*sys.argv[2:])
PYGEN
gen() { python3 -I "${GEN}" "$@" || { echo "gen $1 failed" >&2; return 1; }; }
ver=9.9.9

section() { printf '\n== %s\n' "$*"; }
# eq <name> <expected> <actual>
eq() { if [ "$2" = "$3" ]; then ok "$1"; else fail "$1 (expected '$2', got '$3')"; fi; }
# contains <name> <fixed string> <text>
contains() { case "$3" in *"$2"*) ok "$1" ;; *) fail "$1 (missing '$2')" ;; esac; }
lacks() { case "$3" in *"$2"*) fail "$1 (found '$2')" ;; *) ok "$1" ;; esac; }
file_mode() { python3 -I -c 'import os, stat, sys; print(oct(stat.S_IMODE(os.stat(sys.argv[1]).st_mode)))' "$1"; }
C40=0123456789abcdef0123456789abcdef01234567
C40B=fedcba9876543210fedcba9876543210fedcba98

# ==== 1. identity.env ================================================================================================================
section "1. identity.env is the frozen identity"
ID="${REL}/identity.env"
idv() { sed -n "s/^$1=//p" "${ID}"; }
eq "repository" "myriarch/myriarch" "$(idv RELEASE_REPOSITORY)"
eq "workflow" ".github/workflows/release.yml" "$(idv RELEASE_WORKFLOW)"
eq "issuer" "https://token.actions.githubusercontent.com" "$(idv RELEASE_OIDC_ISSUER)"
eq "trigger" "push" "$(idv RELEASE_TRIGGER)"
eq "server image" "ghcr.io/myriarch/myriarch" "$(idv RELEASE_IMAGE_SERVER)"
eq "updater image" "ghcr.io/myriarch/myriarch-updater" "$(idv RELEASE_IMAGE_UPDATER)"
eq "chart" "ghcr.io/myriarch/charts/myriarch" "$(idv RELEASE_CHART)"
eq "seven keys only" "7" "$(grep -cv '^#\|^$' "${ID}")"
repo_brand="$(sed -n 's|^[[:space:]]*Repository[[:space:]]*=[[:space:]]*"https://github.com/\([^"]*\)".*|\1|p' "${ROOT}/internal/brand/brand.go" | head -n 1)"
eq "internal/brand Repository" "$(idv RELEASE_REPOSITORY)" "${repo_brand}"
eq "install-linux.sh RELEASE_REPOSITORY" "$(idv RELEASE_REPOSITORY)" "$(sed -n 's/^readonly RELEASE_REPOSITORY="\(.*\)"$/\1/p' "${ROOT}/deploy/scripts/install-linux.sh")"
eq "install-windows.ps1 ReleaseRepository" "$(idv RELEASE_REPOSITORY)" "$(sed -n "s/^\$script:ReleaseRepository = '\(.*\)'\$/\1/p" "${ROOT}/deploy/scripts/install-windows.ps1")"
adr29="$(cat "${ROOT}"/docs/adr/0029-*.md)"
contains "ADR-0029 names the identity" "https://github.com/$(idv RELEASE_REPOSITORY)/$(idv RELEASE_WORKFLOW)@refs/tags/v*" "${adr29}"
contains "ADR-0029 names the issuer" "$(idv RELEASE_OIDC_ISSUER)" "${adr29}"
eq "image-labels.env LABEL_SOURCE" "https://github.com/$(idv RELEASE_REPOSITORY)" "$(sed -n 's/^LABEL_SOURCE=//p' "${ROOT}/ci/image-labels.env")"
push_re="$(grep -o '\^myriarch/myriarch/\\.github/workflows/release\\.yml@refs/tags/v' "${REL}/push-images.sh" | head -n 1)"
want_re="^$(printf '%s/%s' "$(idv RELEASE_REPOSITORY)" "$(idv RELEASE_WORKFLOW)" | sed -e 's/[.]/\\./g')@refs/tags/v"
eq "push-images.sh checks the same workflow ref" "${want_re}" "${push_re}"
for key in ghcr.io/myriarch/myriarch ghcr.io/myriarch/myriarch-updater; do
  grep -q "${key}" "${ROOT}/docs/adr/0029-"*.md && ok "ADR-0029 names ${key}" || fail "ADR-0029 does not name ${key}"
done

# the asset list: 42 names, written out independently of deploy/release/assets.json
section "1b. deploy/release/assets.json"
asset_names() { ( . "${REL}/common.sh"; asset_set "$ver" "$1" ); }
eq "assets.json: the 42 names" "$(gen names "${ver}" final)" "$(asset_names names)"
eq "assets.json: the 39 listed names" "$(gen names "${ver}" listed)" "$(asset_names listed)"
eq "assets.json: the 40 names of stage unsigned" "$(gen names "${ver}" unsigned)" "$(asset_names unsigned)"
eq "assets.json: counts per kind" "16 4 17 2 1 1 1" "$(asset_names kinds | awk '{c[$2]++} END {print c["archive"], c["binary"], c["sbom"], c["license"], c["checksums"], c["signature"], c["provenance"]}')"
eq "assets.json: sorted by name in the file" "$(grep -o '"name": "[^"]*"' "${ROOT}/deploy/release/assets.json" | sed 's/"name": //' | LC_ALL=C sort)" "$(grep -o '"name": "[^"]*"' "${ROOT}/deploy/release/assets.json" | sed 's/"name": //')"

# ==== 2. meta.sh =====================================================================================================================
section "2. meta.sh"
MR="${TEST_TMP}/metaroot"
mkdir -p "${MR}/ci/release" "${MR}/deploy/release"
cp "${REL}/common.sh" "${REL}/meta.sh" "${REL}/identity.env" "${MR}/ci/release/"
cp "${ROOT}/ci/versions.env" "${MR}/ci/"
# a fixed signing file (mode off, no exception): the exception that a final release sets in the committed file must not change the outputs below
printf 'WINDOWS_SIGNING=off\nWINDOWS_UNSIGNED_FINAL_TAG=\n' > "${MR}/deploy/release/windows-signing.env"
(
  cd "${MR}" && git init -q -b main . && git config user.email t@example.com && git config user.name t && git add -A \
    && git -c commit.gpgsign=false commit -q -m init && for t in v0.1.0-rc.1 v0.1.0-rc.2 v0.1.0 v0.1.1-rc.1 v0.2.0 v0.10.0 vnext notatag; do git tag "$t"; done
)
meta_run() { local tag="$1"; shift; env RELEASE_TAG="${tag}" RELEASE_COMMIT="${C40}" GITHUB_REPOSITORY=myriarch/myriarch GITHUB_REF="refs/tags/${tag}" \
  GITHUB_WORKFLOW_REF="myriarch/myriarch/.github/workflows/release.yml@refs/tags/${tag}" "$@" "${MR}/ci/release/meta.sh"; }
meta_out() { meta_run "$@" 2>/dev/null; }
meta_key() { local tag="$1" key="$2"; shift 2; meta_out "${tag}" "$@" | sed -n "s/^${key}=//p"; }
eq "rc: all keys in order" "tag=v0.1.0-rc.1
version=0.1.0-rc.1
base-version=0.1.0
commit=${C40}
kind=rc
prerelease=true
latest=false
image-tags=0.1.0-rc.1
notes-start-tag=
windows-signing=off
windows-unsigned-final-tag=" "$(meta_out v0.1.0-rc.1)"
eq "final: all keys in order" "tag=v0.2.0
version=0.2.0
base-version=0.2.0
commit=${C40}
kind=final
prerelease=false
latest=false
image-tags=0.2.0,0.2
notes-start-tag=v0.1.0
windows-signing=off
windows-unsigned-final-tag=" "$(meta_out v0.2.0)"
eq "image tags of an rc" "0.1.1-rc.1" "$(meta_key v0.1.1-rc.1 image-tags)"
eq "image tags of a final tag never hold latest" "0.10.0,0.10" "$(meta_key v0.10.0 image-tags)"
eq "final v0.1.0 has no previous final" "" "$(meta_key v0.1.0 notes-start-tag)"
eq "final v0.2.0 starts at v0.1.0 (no rc)" "v0.1.0" "$(meta_key v0.2.0 notes-start-tag)"
eq "final v0.10.0 starts at v0.2.0 (numeric order)" "v0.2.0" "$(meta_key v0.10.0 notes-start-tag)"
eq "rc v0.1.0-rc.1 has no previous tag" "" "$(meta_key v0.1.0-rc.1 notes-start-tag)"
eq "rc v0.1.0-rc.2 starts at rc.1" "v0.1.0-rc.1" "$(meta_key v0.1.0-rc.2 notes-start-tag)"
eq "rc v0.1.1-rc.1 starts at v0.1.0" "v0.1.0" "$(meta_key v0.1.1-rc.1 notes-start-tag)"
eq "latest for the highest final" "true" "$(meta_key v0.10.0 latest)"
eq "not latest below a higher final" "false" "$(meta_key v0.1.1 latest)"
eq "an rc is never latest" "false" "$(meta_key v0.1.1-rc.1 latest)"
eq "an rc above every final is never latest" "false" "$(meta_key v0.11.0-rc.1 latest)"
eq "the rc of the next version after the highest final is not latest" "false" "$(meta_key v0.10.1-rc.1 latest)"
eq "kind of a final tag" "final" "$(meta_key v0.1.1 kind)"
expect_fail "workflow_ref_mismatch: renamed workflow file" "workflow_ref_mismatch" \
  meta_run v0.1.0 GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/release2.yml@refs/tags/v0.1.0
expect_fail "workflow_ref_mismatch: other repository" "workflow_ref_mismatch" \
  meta_run v0.1.0 GITHUB_WORKFLOW_REF=other/repo/.github/workflows/release.yml@refs/tags/v0.1.0
expect_fail "workflow_ref_mismatch: a branch ref" "workflow_ref_mismatch" \
  meta_run v0.1.0 GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/release.yml@refs/heads/main
expect_fail "workflow_ref_mismatch: another tag" "workflow_ref_mismatch" \
  meta_run v0.1.0 GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.1.1
expect_fail "workflow_ref_mismatch: a caller of a reusable workflow" "workflow_ref_mismatch.*frozen identity" \
  meta_run v0.1.0 GITHUB_WORKFLOW_REF=myriarch/myriarch/.github/workflows/caller.yml@refs/tags/v0.1.0
expect_fail "repository_mismatch" "repository_mismatch" meta_run v0.1.0 GITHUB_REPOSITORY=someone/myriarch
expect_fail "ref_mismatch" "ref_mismatch" meta_run v0.1.0 GITHUB_REF=refs/tags/v0.1.1
expect_fail "tag_invalid: leading zero" "tag_invalid" meta_run v01.0.0
expect_fail "tag_invalid: rc.0" "tag_invalid" meta_run v0.1.0-rc.0
expect_fail "tag_invalid: control character" "tag_invalid" meta_run "$(printf 'v0.1.0\nx')"
expect_fail "commit_invalid" "commit_invalid" meta_run v0.1.0 RELEASE_COMMIT=0123456789ABCDEF0123456789abcdef01234567
cp "${MR}/deploy/release/windows-signing.env" "${TEST_TMP}/signing.env.orig"
for bad in 'WINDOWS_SIGNING=maybe' 'WINDOWS_SIGNING=off
WINDOWS_SIGNING=off' 'EXTRA=1'; do
  printf '%s\nWINDOWS_UNSIGNED_FINAL_TAG=\n' "${bad}" > "${MR}/deploy/release/windows-signing.env"
  expect_fail "signing_env: $(printf '%s' "${bad}" | head -n 1)" "signing_env" meta_run v0.1.0
done
printf 'WINDOWS_SIGNING=off\n' > "${MR}/deploy/release/windows-signing.env"
expect_fail "signing_env: the exception line must exist" "signing_env" meta_run v0.1.0
printf 'WINDOWS_SIGNING=kms\nWINDOWS_UNSIGNED_FINAL_TAG=v0.1.0\n' > "${MR}/deploy/release/windows-signing.env"
eq "signing values are passed through" "kms v0.1.0" "$(meta_key v0.1.0 windows-signing) $(meta_key v0.1.0 windows-unsigned-final-tag)"
cp "${TEST_TMP}/signing.env.orig" "${MR}/deploy/release/windows-signing.env"
# the committed signing file parses, and meta.sh passes its two values through, whether or not it names an exception
cp "${ROOT}/deploy/release/windows-signing.env" "${MR}/deploy/release/windows-signing.env"
eq "the committed signing file is passed through" \
  "$(sed -n 's/^WINDOWS_SIGNING=//p' "${ROOT}/deploy/release/windows-signing.env") $(sed -n 's/^WINDOWS_UNSIGNED_FINAL_TAG=//p' "${ROOT}/deploy/release/windows-signing.env")" \
  "$(meta_key v0.1.0 windows-signing) $(meta_key v0.1.0 windows-unsigned-final-tag)"
cp "${TEST_TMP}/signing.env.orig" "${MR}/deploy/release/windows-signing.env"

# ==== 3. artifact.sh ===================================================================================================================
section "3. artifact.sh"
AR="${TEST_TMP}/art"
mkdir -p "${AR}/src/sub"
printf 'one\n' > "${AR}/src/a.txt"; printf '#!/bin/sh\n' > "${AR}/src/sub/run.sh"; chmod 755 "${AR}/src/sub/run.sh"; printf 'h\n' > "${AR}/src/.hidden"
sha1="$(run_priv "${REL}/artifact.sh" pack --dir "${AR}/src" --out "${AR}/one.tar" 2>/dev/null | sed -n 's/^sha256=//p')"
sha2="$(run_priv "${REL}/artifact.sh" pack --dir "${AR}/src" --out "${AR}/two.tar" 2>/dev/null | sed -n 's/^sha256=//p')"
eq "pack twice gives the same sha256" "${sha1}" "${sha2}"
cmp -s "${AR}/one.tar" "${AR}/two.tar" && ok "pack twice gives the same bytes" || fail "pack twice differs"
eq "the reported sha256 is the one of the file" "$(sha256_of "${AR}/one.tar")" "${sha1}"
expect_ok "unpack under the restricted PATH" run_priv "${REL}/artifact.sh" unpack --file "${AR}/one.tar" --sha256 "${sha1}" --into "${AR}/out"
eq "an executable stays executable" "0o755" "$(file_mode "${AR}/out/sub/run.sh")"
[ -f "${AR}/out/.hidden" ] && ok "hidden files are packed" || fail "hidden file lost"
expect_fail "unpack into a non-empty directory" "artifact_target_not_empty" run_priv "${REL}/artifact.sh" unpack --file "${AR}/one.tar" --sha256 "${sha1}" --into "${AR}/out"
expect_fail "unpack with a wrong digest" "artifact_digest_mismatch.*${sha1}" run_priv "${REL}/artifact.sh" unpack --file "${AR}/one.tar" --sha256 "$(printf '0%.0s' $(seq 64))" --into "${AR}/out2"
[ ! -e "${AR}/out2" ] && ok "nothing is unpacked on a digest mismatch" || fail "unpacked despite the mismatch"
expect_fail "unpack with a malformed digest" "artifact_digest_invalid" run_priv "${REL}/artifact.sh" unpack --file "${AR}/one.tar" --sha256 ABC --into "${AR}/out3"
expect_fail "pack refuses an existing file" "artifact_exists" run_priv "${REL}/artifact.sh" pack --dir "${AR}/src" --out "${AR}/one.tar"
mkdir -p "${AR}/empty"
expect_fail "pack refuses an empty directory" "artifact_empty" run_priv "${REL}/artifact.sh" pack --dir "${AR}/empty" --out "${AR}/e.tar"
mkdir -p "${AR}/withlink"; ln -s /etc/hosts "${AR}/withlink/l"
expect_fail "pack refuses a symbolic link" "artifact_entry_invalid" run_priv "${REL}/artifact.sh" pack --dir "${AR}/withlink" --out "${AR}/l.tar"
expect_ok "pack --only packs one name" run_priv "${REL}/artifact.sh" pack --dir "${AR}/src" --only sub --out "${AR}/only.tar"
eq "pack --only lists only that name" "sub/
sub/run.sh" "$(tar -tf "${AR}/only.tar")"
# hostile tar files, crafted with python
python3 -I - "${AR}" <<'PY'
import io, sys, tarfile
d = sys.argv[1]
def make(name, entries):
    with tarfile.open("%s/%s.tar" % (d, name), "w") as t:
        for info, data in entries:
            t.addfile(info, io.BytesIO(data) if data is not None else None)
def reg(n, data=b"x"):
    i = tarfile.TarInfo(n); i.size = len(data); return (i, data)
def lnk(n, target, hard=False):
    i = tarfile.TarInfo(n); i.type = tarfile.LNKTYPE if hard else tarfile.SYMTYPE; i.linkname = target; return (i, None)
make("dotdot", [reg("../x")])
make("dotdot2", [reg("a/../../x")])
make("abs", [reg("/abs/x")])
make("symlink", [lnk("l", "/etc/passwd")])
make("hardlink", [reg("f"), lnk("h", "f", True)])
dev = tarfile.TarInfo("dev"); dev.type = tarfile.CHRTYPE; make("device", [(dev, None)])
fifo = tarfile.TarInfo("fifo"); fifo.type = tarfile.FIFOTYPE; make("fifo", [(fifo, None)])
make("good", [reg("ok/x")])
open("%s/notatar.tar" % d, "w").write("this is not a tar file\n")
PY
for hostile in dotdot dotdot2 abs symlink hardlink device fifo notatar; do
  case "${hostile}" in
    dotdot|dotdot2|abs) want="artifact_entry_invalid.*absolute path or has a \\.\\. component" ;; # the check of the script itself, on every tar
    *) want="artifact_entry_invalid" ;;
  esac
  expect_fail "unpack refuses ${hostile}" "${want}" run_priv "${REL}/artifact.sh" unpack --file "${AR}/${hostile}.tar" --sha256 "$(sha256_of "${AR}/${hostile}.tar")" --into "${AR}/h_${hostile}"
  [ ! -e "${AR}/h_${hostile}" ] || [ -z "$(ls -A "${AR}/h_${hostile}")" ] && ok "nothing extracted for ${hostile}" || fail "files were extracted for ${hostile}"
done
expect_ok "unpack accepts an ordinary tar" run_priv "${REL}/artifact.sh" unpack --file "${AR}/good.tar" --sha256 "$(sha256_of "${AR}/good.tar")" --into "${AR}/h_good"
expect_fail "usage error exits 2" "usage:" run_priv "${REL}/artifact.sh" pack
run_priv "${REL}/artifact.sh" pack >/dev/null 2>&1; eq "usage exit status" "2" "$?"

# ==== 4. stage-assets.sh and verify-dist.sh ============================================================================================
section "4. stage-assets.sh and verify-dist.sh"
D1="${TEST_TMP}/dist1"; R1="${TEST_TMP}/root1"; O1="${TEST_TMP}/staged1"
gen dist "${D1}" "${R1}" "${ver}"
expect_ok "stage a complete dist" run_priv "${REL}/stage-assets.sh" --dist "${D1}" --root "${R1}" --version "${ver}" --out "${O1}"
eq "40 files are staged" "40" "$(ls -A "${O1}/assets" | wc -l | tr -d ' ')"
eq "staged names are the 40 of stage unsigned" "$(gen names "${ver}" unsigned)" "$(ls -A "${O1}/assets" | LC_ALL=C sort)"
[ -f "${O1}/assets/myriarch-updater_${ver}_linux_amd64" ] && [ -f "${O1}/assets/myriarch-updater_${ver}_windows_arm64.exe" ] && ok "bare binaries are staged under their upload names" || fail "bare binaries missing"
eq "staged files have mode 0644" "0o644" "$(file_mode "${O1}/assets/checksums.txt")"
[ ! -L "${O1}/assets/LICENSE" ] && cmp -s "${R1}/LICENSE" "${O1}/assets/LICENSE" && ok "LICENSE is copied from the root" || fail "LICENSE not copied"
[ -f "${O1}/meta/metadata.json" ] && [ -f "${O1}/meta/artifacts.json" ] && ok "meta/ holds metadata.json and artifacts.json" || fail "meta/ incomplete"
expect_ok "verify-dist: the staged set (stage unsigned)" run_priv "${REL}/verify-dist.sh" --dir "${O1}/assets" --version "${ver}" --stage unsigned
contains "verify-dist reports 40 assets and 39 checksums" "ok: 40 assets, 39 checksums" "${OUT}"
expect_fail "stage-assets: out not empty" "out_not_empty" run_priv "${REL}/stage-assets.sh" --dist "${D1}" --root "${R1}" --version "${ver}" --out "${O1}"
gen dist "${TEST_TMP}/dmiss" "${TEST_TMP}/rmiss" "${ver}" "missing=myrctl_${ver}_darwin_amd64.tar.gz"
expect_fail "stage-assets: a missing artifact" "asset_missing" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/dmiss" --root "${TEST_TMP}/rmiss" --version "${ver}" --out "${TEST_TMP}/omiss"
gen dist "${TEST_TMP}/dextra" "${TEST_TMP}/rextra" "${ver}" "extra=extra_${ver}.tar.gz"
expect_fail "stage-assets: an extra artifact" "asset_unexpected" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/dextra" --root "${TEST_TMP}/rextra" --version "${ver}" --out "${TEST_TMP}/oextra"
gen dist "${TEST_TMP}/dmeta" "${TEST_TMP}/rmeta" "${ver}" "metaversion=9.9.8"
expect_fail "stage-assets: metadata version mismatch" "version_mismatch" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/dmeta" --root "${TEST_TMP}/rmeta" --version "${ver}" --out "${TEST_TMP}/ometa"
gen dist "${TEST_TMP}/dbad" "${TEST_TMP}/rbad" "${ver}" "badhash=myrctl_${ver}_linux_amd64.tar.gz"
expect_fail "stage-assets: a hash in checksums.txt that differs" "checksum_mismatch.*myrctl_${ver}_linux_amd64" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/dbad" --root "${TEST_TMP}/rbad" --version "${ver}" --out "${TEST_TMP}/obad"
gen dist "${TEST_TMP}/dnof" "${TEST_TMP}/rnof" "${ver}" "nofile=myrctl_${ver}_linux_amd64.tar.gz"
expect_fail "stage-assets: a listed file that is not there" "file_missing" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/dnof" --root "${TEST_TMP}/rnof" --version "${ver}" --out "${TEST_TMP}/onof"
expect_fail "stage-assets: -beta.1 is refused" "version_invalid" run_priv "${REL}/stage-assets.sh" --dist "${D1}" --root "${R1}" --version 9.9.9-beta.1 --out "${TEST_TMP}/obeta"
gen dist "${TEST_TMP}/dsnap" "${TEST_TMP}/rsnap" 9.9.9-snapshot
expect_ok "stage-assets: -snapshot is accepted" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/dsnap" --root "${TEST_TMP}/rsnap" --version 9.9.9-snapshot --out "${TEST_TMP}/osnap"
gen dist "${TEST_TMP}/drc" "${TEST_TMP}/rrc" 9.9.9-rc.2
expect_ok "stage-assets: -rc.N is accepted" run_priv "${REL}/stage-assets.sh" --dist "${TEST_TMP}/drc" --root "${TEST_TMP}/rrc" --version 9.9.9-rc.2 --out "${TEST_TMP}/orc"
expect_fail "verify-dist: -beta.1 is refused" "version_invalid" run_priv "${REL}/verify-dist.sh" --dir "${O1}/assets" --version 9.9.9-beta.1 --stage unsigned

# mutations of a complete set
SIGS="${TEST_TMP}/sigs1"
gen assets "${ver}" "${TEST_TMP}/fa" "${SIGS}"
mut() { # name: a fresh copy of the unsigned set in ${TEST_TMP}/m
  rm -rf "${TEST_TMP}/m"; mkdir -p "${TEST_TMP}/m"; cp -R "${O1}/assets/." "${TEST_TMP}/m/"; chmod -R u+w "${TEST_TMP}/m"
}
vd() { run_priv "${REL}/verify-dist.sh" --dir "${TEST_TMP}/m" --version "${ver}" --stage "${1:-unsigned}"; }
mut; first_hash="$(sed -n '1p' "${TEST_TMP}/m/checksums.txt" | cut -c 1)"; sed -i.bak "1s/^./$([ "${first_hash}" = a ] && echo b || echo a)/" "${TEST_TMP}/m/checksums.txt"; rm -f "${TEST_TMP}/m/checksums.txt.bak"
expect_fail "verify-dist: a changed hash" "checksum_mismatch" vd
mut; sed -i.bak '/  LICENSE$/d' "${TEST_TMP}/m/checksums.txt"; rm -f "${TEST_TMP}/m/checksums.txt.bak"
expect_fail "verify-dist: a missing line" "checksums_incomplete.*LICENSE" vd
mut; printf '%s  checksums.txt\n' "$(printf 'c%.0s' $(seq 64))" >> "${TEST_TMP}/m/checksums.txt"
expect_fail "verify-dist: an extra line" "checksums_unexpected.*checksums.txt" vd
mut; printf 'not a checksum line\n' >> "${TEST_TMP}/m/checksums.txt"
expect_fail "verify-dist: a malformed line" "checksums_malformed" vd
mut; sed -n '1p' "${TEST_TMP}/m/checksums.txt" >> "${TEST_TMP}/m/checksums.txt"
expect_fail "verify-dist: a name listed twice" "checksums_malformed.*twice" vd
mut; printf '%s  sub/dir\n' "$(printf 'c%.0s' $(seq 64))" >> "${TEST_TMP}/m/checksums.txt"
expect_fail "verify-dist: a name with a slash" "checksums_malformed" vd
mut; rm "${TEST_TMP}/m/LICENSE"; ln -s "${R1}/LICENSE" "${TEST_TMP}/m/LICENSE"
expect_fail "verify-dist: a link instead of a file" "asset_not_regular" vd
mut; mkdir "${TEST_TMP}/m/dir"
expect_fail "verify-dist: a directory" "asset_unexpected" vd
mut; printf 'x' > "${TEST_TMP}/m/.hidden"
expect_fail "verify-dist: a hidden file" "asset_unexpected" vd
mut; printf 'x' > "${TEST_TMP}/m/extra.txt"
expect_fail "verify-dist: an extra file" "asset_unexpected.*extra.txt" vd
mut; rm "${TEST_TMP}/m/THIRD_PARTY_NOTICES"
expect_fail "verify-dist: a missing file" "asset_missing.*THIRD_PARTY_NOTICES" vd
mut; cp "${SIGS}/checksums.txt.sigstore.json" "${TEST_TMP}/m/"
expect_fail "verify-dist: the signature file is not part of stage unsigned" "asset_unexpected" vd
mut
expect_fail "verify-dist: stage final without the signature files" "asset_missing" vd final
mut; cp "${SIGS}/checksums.txt.sigstore.json" "${TEST_TMP}/m/"
expect_fail "verify-dist: stage final without the provenance" "asset_missing.*provenance" vd final
mut; cp "${SIGS}/"* "${TEST_TMP}/m/"
expect_ok "verify-dist: stage final (42 files)" vd final
contains "verify-dist reports 42 assets" "ok: 42 assets, 39 checksums" "${OUT}"
: > "${TEST_TMP}/m/myriarch_${ver}_provenance.intoto.jsonl"
expect_fail "verify-dist: an empty provenance file" "asset_empty" vd final
mut; cp "${SIGS}/"* "${TEST_TMP}/m/"; printf '%s  checksums.txt.sigstore.json\n' "$(printf 'c%.0s' $(seq 64))" >> "${TEST_TMP}/m/checksums.txt"
expect_fail "verify-dist: the signature file must not be listed" "checksums_unexpected" vd final

# ==== 5. verify-blob.sh with stubs =====================================================================================================
section "5. verify-blob.sh (stub cosign)"
BL="${TEST_TMP}/blob"; mkdir -p "${BL}"
printf 'blob\n' > "${BL}/checksums.txt"; printf '{}\n' > "${BL}/bundle.json"; printf '{}\n' > "${BL}/root.json"
ISS=https://token.actions.githubusercontent.com
vb() { local mode="$1"; shift; run_priv "${REL}/verify-blob.sh" --mode "${mode}" --bundle "${BL}/bundle.json" --repository myriarch/myriarch \
  --workflow .github/workflows/release.yml --tag v0.1.0-rc.1 "$@" "${BL}/checksums.txt"; }
reset_stubs
expect_ok "verify-blob exact" vb exact --commit "${C40}"
eq "exact: the cosign arguments" "verify-blob --bundle ${BL}/bundle.json --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v0.1.0-rc.1 --certificate-oidc-issuer ${ISS} --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/v0.1.0-rc.1 --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push ${BL}/checksums.txt" "$(call_line cosign 1)"
reset_stubs
expect_ok "verify-blob pattern" vb pattern
eq "pattern: the cosign arguments" "verify-blob --bundle ${BL}/bundle.json --certificate-identity-regexp ^https://github\\.com/myriarch/myriarch/\\.github/workflows/release\\.yml@refs/tags/v --certificate-oidc-issuer ${ISS} ${BL}/checksums.txt" "$(call_line cosign 1)"
reset_stubs
expect_ok "verify-blob bound" vb bound
eq "bound: the version is escaped and anchored" "verify-blob --bundle ${BL}/bundle.json --certificate-identity-regexp ^https://github\\.com/myriarch/myriarch/\\.github/workflows/release\\.yml@refs/tags/v0\\.1\\.0-rc\\.1\$ --certificate-oidc-issuer ${ISS} ${BL}/checksums.txt" "$(call_line cosign 1)"
reset_stubs
expect_ok "verify-blob passes --trusted-root through" vb pattern --trusted-root "${BL}/root.json"
eq "trusted root: before the blob" "verify-blob --bundle ${BL}/bundle.json --certificate-identity-regexp ^https://github\\.com/myriarch/myriarch/\\.github/workflows/release\\.yml@refs/tags/v --certificate-oidc-issuer ${ISS} --trusted-root ${BL}/root.json ${BL}/checksums.txt" "$(call_line cosign 1)"
reset_stubs; echo 'verify-blob' > "${S}/cosign.fail"
expect_fail "verify-blob: a failing verification" "signature_invalid.*exact verification of ${BL}/checksums.txt failed.*no matching signatures" vb exact --commit "${C40}"
reset_stubs
expect_fail "verify-blob: exact needs a commit" "commit_invalid" vb exact
expect_fail "verify-blob: a malformed commit" "commit_invalid" vb exact --commit abc
expect_fail "verify-blob: an invalid mode" "usage:" vb nonsense
expect_fail "verify-blob: an invalid tag" "tag_invalid" run_priv "${REL}/verify-blob.sh" --mode pattern --bundle "${BL}/bundle.json" --repository myriarch/myriarch --workflow .github/workflows/release.yml --tag main "${BL}/checksums.txt"
expect_fail "verify-blob: an invalid repository" "repository_invalid" run_priv "${REL}/verify-blob.sh" --mode pattern --bundle "${BL}/bundle.json" --repository 'a b/c' --workflow .github/workflows/release.yml --tag v0.1.0 "${BL}/checksums.txt"
expect_fail "verify-blob: a missing bundle" "file_missing" run_priv "${REL}/verify-blob.sh" --mode pattern --bundle "${BL}/none.json" --repository myriarch/myriarch --workflow .github/workflows/release.yml --tag v0.1.0 "${BL}/checksums.txt"
no_call cosign . && ok "no cosign call on refused inputs" || fail "cosign was called on refused inputs"

# ==== 6. verify-blob.sh with the real pinned cosign ====================================================================================
section "6. verify-blob.sh (real cosign, offline, upstream fixture)"
REALCOSIGN="${ROOT}/.cache/bin/cosign"
FX="${ROOT}/internal/updater/testdata/sigstore"
real_ok=0
if [ -x "${REALCOSIGN}" ] && [ "$("${REALCOSIGN}" version 2>&1 | sed -n 's/^GitVersion: *//p' | head -n 1)" = "${COSIGN_VERSION}" ]; then real_ok=1; fi
if [ "${real_ok}" = 0 ]; then
  if [ "${CI:-}" = true ]; then fail "the real-cosign cases are required in CI: cosign ${COSIGN_VERSION} is missing in .cache/bin (ci/install-tools.sh cosign)"
  else echo "  SKIPPED the real-cosign cases: cosign ${COSIGN_VERSION} is not in .cache/bin (ci/install-tools.sh cosign)"; fi
else
  OTEL_REPO=open-telemetry/opentelemetry-collector-releases
  OTEL_WF=.github/workflows/base-release.yaml
  OTEL_SHA=f6159a775dad1e21433c49bd8450673dd13ed4d3
  FXBLOB="${FX}/otelcol-contrib_0.162.0_linux_amd64.tar.gz.sha256"
  FXBUNDLE="${FXBLOB}.sigstore.json"
  mkdir -p "${TEST_TMP}/rhome" "${TEST_TMP}/rtuf"
  real_vb() { # mode tag extra-args...
    local mode="$1" tag="$2"; shift 2
    ( export PATH="${RB}" TOOLS_BIN="${ROOT}/.cache/bin" HOME="${TEST_TMP}/rhome" TUF_ROOT="${TEST_TMP}/rtuf"
      "${REL}/verify-blob.sh" --mode "${mode}" --bundle "${FXBUNDLE}" --repository "${OTEL_REPO}" --workflow "${OTEL_WF}" --tag "${tag}" \
        --trusted-root "${FX}/trusted_root.json" "$@" "${FXBLOB}" )
  }
  expect_ok "real cosign: exact (identity, issuer, repository, ref, sha, trigger)" real_vb exact v0.162.0 --commit "${OTEL_SHA}"
  expect_ok "real cosign: pattern" real_vb pattern v0.162.0
  expect_ok "real cosign: bound to the version" real_vb bound v0.162.0
  expect_fail "real cosign: exact refuses another commit" "signature_invalid.*expected GithubWorkflowSHA" real_vb exact v0.162.0 --commit "${C40}"
  expect_fail "real cosign: exact refuses another tag" "signature_invalid" real_vb exact v0.162.1 --commit "${OTEL_SHA}"
  expect_fail "real cosign: bound refuses a version that is only a prefix match without escaping" "signature_invalid.*expected SAN value to match regex" real_vb bound v0.16.0
  myr_vb() { ( export PATH="${RB}" TOOLS_BIN="${ROOT}/.cache/bin" HOME="${TEST_TMP}/rhome" TUF_ROOT="${TEST_TMP}/rtuf"
    "${REL}/verify-blob.sh" --mode "$1" --bundle "${FXBUNDLE}" --repository myriarch/myriarch --workflow .github/workflows/release.yml --tag v0.162.0 \
      --trusted-root "${FX}/trusted_root.json" "${@:2}" "${FXBLOB}" ); }
  expect_fail "real cosign: the Myriarch identity is refused (exact)" "signature_invalid.*expected SAN value" myr_vb exact --commit "${OTEL_SHA}"
  expect_fail "real cosign: the Myriarch identity is refused (pattern)" "signature_invalid.*expected SAN value to match regex" myr_vb pattern
  cp "${FXBLOB}" "${TEST_TMP}/tampered.sha256"; printf 'x' >> "${TEST_TMP}/tampered.sha256"
  tampered_vb() { ( export PATH="${RB}" TOOLS_BIN="${ROOT}/.cache/bin" HOME="${TEST_TMP}/rhome" TUF_ROOT="${TEST_TMP}/rtuf"
    "${REL}/verify-blob.sh" --mode pattern --bundle "${FXBUNDLE}" --repository "${OTEL_REPO}" --workflow "${OTEL_WF}" --tag v0.162.0 \
      --trusted-root "${FX}/trusted_root.json" "${TEST_TMP}/tampered.sha256" ); }
  expect_fail "real cosign: a tampered file is refused" "signature_invalid.*invalid signature" tampered_vb
  # the trigger is fixed by identity.env: call the pinned cosign with the flag set of verify-blob.sh and another trigger
  trigger_call() { ( export HOME="${TEST_TMP}/rhome" TUF_ROOT="${TEST_TMP}/rtuf"
    "${REALCOSIGN}" verify-blob --bundle "${FXBUNDLE}" \
      --certificate-identity "https://github.com/${OTEL_REPO}/${OTEL_WF}@refs/tags/v0.162.0" --certificate-oidc-issuer "${ISS}" \
      --certificate-github-workflow-repository "${OTEL_REPO}" --certificate-github-workflow-ref refs/tags/v0.162.0 \
      --certificate-github-workflow-sha "${OTEL_SHA}" --certificate-github-workflow-trigger workflow_dispatch \
      --trusted-root "${FX}/trusted_root.json" "${FXBLOB}" ); }
  expect_fail "real cosign enforces the trigger claim that verify-blob.sh relies on" "expected GithubWorkflowTrigger" trigger_call
fi

# ==== 7. sign-checksums.sh ===================================================================================================================
section "7. sign-checksums.sh"
FA="${TEST_TMP}/fa"
SIGOUT="${TEST_TMP}/sigout"
reset_stubs
expect_ok "sign-checksums sign" run_priv "${REL}/sign-checksums.sh" sign --assets "${FA}" --out "${SIGOUT}" --tag v9.9.9 --commit "${C40}"
eq "sign: sign-blob first, keyless and without extra flags" "sign-blob --yes --bundle ${SIGOUT}/checksums.txt.sigstore.json ${FA}/checksums.txt" "$(call_line cosign 1)"
eq "sign: then the exact verification with the identity.env values" "verify-blob --bundle ${SIGOUT}/checksums.txt.sigstore.json --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --certificate-oidc-issuer ${ISS} --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/v9.9.9 --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push ${FA}/checksums.txt" "$(call_line cosign 2)"
eq "sign: exactly two cosign calls" "2" "$(call_count cosign '.')"
no_call cosign '(^| )--(key|tlog-upload|use-signing-config|identity-token)' && ok "sign: no key, no tlog or signing-config flags" || fail "sign: forbidden flag in the cosign call"
[ -s "${SIGOUT}/checksums.txt.sigstore.json" ] && ok "sign: the bundle exists" || fail "sign: no bundle"
expect_fail "sign: a second run refuses the existing bundle" "bundle_exists" run_priv "${REL}/sign-checksums.sh" sign --assets "${FA}" --out "${SIGOUT}" --tag v9.9.9 --commit "${C40}"
reset_stubs; echo 'verify-blob' > "${S}/cosign.fail"
expect_fail "sign: a failing verification fails the job" "signature_invalid" run_priv "${REL}/sign-checksums.sh" sign --assets "${FA}" --out "${TEST_TMP}/sigout2" --tag v9.9.9 --commit "${C40}"
reset_stubs; echo 'sign-blob' > "${S}/cosign.fail"
expect_fail "sign: a failing signature" "sign_failed" run_priv "${REL}/sign-checksums.sh" sign --assets "${FA}" --out "${TEST_TMP}/sigout3" --tag v9.9.9 --commit "${C40}"
expect_fail "sign: an invalid tag" "tag_invalid" run_priv "${REL}/sign-checksums.sh" sign --assets "${FA}" --out "${TEST_TMP}/sigout4" --tag 9.9.9 --commit "${C40}"
# provenance
PV="${TEST_TMP}/prov"; mkdir -p "${PV}"
prov() { run_priv "${REL}/sign-checksums.sh" provenance --bundle "$1" --assets "${FA}" --out "$2" --version "${ver}"; }
cp "${SIGS}/myriarch_${ver}_provenance.intoto.jsonl" "${PV}/ok.jsonl"
expect_ok "provenance: a bundle with the 39 subjects" prov "${PV}/ok.jsonl" "${PV}/out1"
cmp -s "${PV}/ok.jsonl" "${PV}/out1/myriarch_${ver}_provenance.intoto.jsonl" && ok "provenance: copied to the asset name" || fail "provenance: asset missing or different"
expect_fail "provenance: an existing target" "provenance_exists" prov "${PV}/ok.jsonl" "${PV}/out1"
for mode in missing extra wrongdigest twolines wrongpredicate badmedia nopayloadtype; do
  gen bundle "${FA}/checksums.txt" "${PV}/${mode}.jsonl" "${mode}"
  expect_fail "provenance: ${mode}" "provenance_invalid" prov "${PV}/${mode}.jsonl" "${PV}/out_${mode}"
  [ ! -e "${PV}/out_${mode}/myriarch_${ver}_provenance.intoto.jsonl" ] && ok "provenance: nothing copied for ${mode}" || fail "provenance: copied despite ${mode}"
done
printf 'not json\n' > "${PV}/garbage.jsonl"
expect_fail "provenance: not JSON" "provenance_invalid" prov "${PV}/garbage.jsonl" "${PV}/out_garbage"
: > "${PV}/empty.jsonl"
expect_fail "provenance: an empty file" "provenance_invalid" prov "${PV}/empty.jsonl" "${PV}/out_empty"

# ==== 8. sign-image.sh ======================================================================================================================
section "8. sign-image.sh"
IDX="sha256:$(printf 'a%.0s' $(seq 64))"; AMD="sha256:$(printf 'b%.0s' $(seq 64))"; ARM="sha256:$(printf 'c%.0s' $(seq 64))"
si() { run_priv "${REL}/sign-image.sh" --repository "$1" --index "${2:-${IDX}}" --platform "${AMD}" --platform "${ARM}" --tag v9.9.9 --commit "${C40}"; }
reset_stubs
expect_ok "sign-image: server image" si ghcr.io/myriarch/myriarch
eq "sign-image: recursive keyless signature of the index" "sign --yes --recursive ghcr.io/myriarch/myriarch@${IDX}" "$(call_line cosign 1)"
for n in 2 3 4; do
  d="$(printf '%s\n%s\n%s\n' "${IDX}" "${AMD}" "${ARM}" | sed -n "$((n - 1))p")"
  eq "sign-image: verify call $((n - 1)) (exact identity)" "verify --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --certificate-oidc-issuer ${ISS} --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/v9.9.9 --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push ghcr.io/myriarch/myriarch@${d}" "$(call_line cosign "${n}")"
done
eq "sign-image: four cosign calls" "4" "$(call_count cosign '.')"
reset_stubs
expect_ok "sign-image: updater image" si ghcr.io/myriarch/myriarch-updater
expect_fail "sign-image: a repository outside identity.env" "repository_invalid" si ghcr.io/other/image
expect_fail "sign-image: a repository with a tag" "repository_invalid" si ghcr.io/myriarch/myriarch:latest
expect_fail "sign-image: a malformed index digest" "digest_invalid" si ghcr.io/myriarch/myriarch sha256:abc
expect_fail "sign-image: equal digests" "digest_invalid" run_priv "${REL}/sign-image.sh" --repository ghcr.io/myriarch/myriarch --index "${IDX}" --platform "${AMD}" --platform "${AMD}" --tag v9.9.9 --commit "${C40}"
reset_stubs; echo 'verify .*@sha256:c' > "${S}/cosign.fail"
expect_fail "sign-image: a failing verification of the arm64 digest" "signature_invalid" si ghcr.io/myriarch/myriarch
reset_stubs; echo '^sign ' > "${S}/cosign.fail"
expect_fail "sign-image: a failing signature" "sign_failed" si ghcr.io/myriarch/myriarch
reset_stubs; echo 'https://spdx.dev/Document' > "${S}/cosign.type"
expect_fail "sign-image: an attestation is not a signature" "signature_invalid.*no cosign signature" si ghcr.io/myriarch/myriarch

# ==== 9. registry-login.sh ===================================================================================================================
section "9. registry-login.sh"
TOKEN="unit-test-token-value-0001"
rl() { REGISTRY_TOKEN="${TOKEN}" GITHUB_ACTOR=octo-actor run_priv "${REL}/registry-login.sh" "$@"; }
reset_stubs
expect_ok "login: the stub crane writes the config" rl login
contains "login: no token in the output" "logged in to ghcr.io as octo-actor" "${OUT}"
lacks "login: the token is not printed" "${TOKEN}" "${OUT}"
lacks "login: the token is not in the crane arguments" "${TOKEN}" "$(calls crane)"
eq "login: crane auth login with the actor and --password-stdin" "auth login ghcr.io -u octo-actor --password-stdin" "$(call_line crane 1 | sed 's/ DOCKER_CONFIG=.*//')"
CFG="${HOME_DIR}/.docker/config.json"
eq "login: the file has mode 0600" "0o600" "$(file_mode "${CFG}")"
contains "login: the auths entry exists" '"ghcr.io"' "$(cat "${CFG}")"
contains "login: DOCKER_CONFIG was not set for crane" "DOCKER_CONFIG=unset" "$(calls crane)"
expect_ok "check: the assertions of login" rl check
expect_ok "logout removes the entry" rl logout
[ ! -e "${CFG}" ] && ok "logout: the file held only that entry and is removed" || fail "logout: file remains"
reset_stubs; mkdir -p "${HOME_DIR}/.docker"; printf '{"auths":{"ghcr.io":{"auth":"eDp5"},"example.com":{"auth":"eDp5"}}}\n' > "${HOME_DIR}/.docker/config.json"
expect_ok "logout keeps other registries" rl logout
contains "logout: the other entry stays" '"example.com"' "$(cat "${CFG}")"
lacks "logout: the ghcr.io entry is gone" '"ghcr.io"' "$(cat "${CFG}")"
reset_stubs
rl_dc() { RP_DOCKER_CONFIG="${TEST_TMP}/dc" REGISTRY_TOKEN="${TOKEN}" GITHUB_ACTOR=a run_priv "${REL}/registry-login.sh" login; }
rl_notoken() { REGISTRY_TOKEN="" GITHUB_ACTOR=a run_priv "${REL}/registry-login.sh" login; }
rl_noactor() { REGISTRY_TOKEN="${TOKEN}" GITHUB_ACTOR="" run_priv "${REL}/registry-login.sh" login; }
expect_fail "login: DOCKER_CONFIG set" "docker_config_set" rl_dc
expect_fail "login: an empty token" "login_input" rl_notoken
expect_fail "login: an empty actor" "login_input" rl_noactor
reset_stubs; touch "${S}/crane.login.helper"
expect_fail "login: a credsStore hides the entry" "login_unusable" rl login
reset_stubs; touch "${S}/crane.login.credhelpers"
expect_fail "login: a credHelpers entry hides the credentials" "login_unusable" rl login
reset_stubs; echo 1 > "${S}/crane.login.rc"
expect_fail "login: a crane failure" "login_failed.*crane auth login failed" rl login
reset_stubs
expect_fail "check: no config file" "login_unusable" rl check
mkdir -p "${HOME_DIR}/.docker"; printf '{"auths":{"ghcr.io":{"auth":"%s"}}}\n' "$(printf 'someone-else:tok' | base64 | tr -d '\n')" > "${HOME_DIR}/.docker/config.json"
expect_fail "check: the entry belongs to another user" "login_unusable" rl check

# ==== 10. notes.sh =========================================================================================================================
section "10. notes.sh"
NT="${TEST_TMP}/notes"; mkdir -p "${NT}"
CL="${NT}/CHANGELOG.md"
cat > "${CL}" <<'CHG'
# Changelog

## Unreleased

### Added

- unreleased entry alpha

<!-- a hidden comment entry -->

```text
## not a heading
- fenced line
```

## [0.1.0] - 2026-10-20

- final entry beta

## [0.0.9] - 2026-01-01

- old entry gamma
CHG
printf '%s\n' '{"schema":1,"signed":false,"provider":"off","subject":null,"files":{}}' > "${NT}/signer-off.json"
printf '%s\n' '{"schema":1,"signed":true,"provider":"signpath","subject":"CN=Example Org,O=Example Org","files":{}}' > "${NT}/signer-signed.json"
printf '%s\n' '{"schema":1,"signed":true,"provider":"signpath","subject":"","files":{}}' > "${NT}/signer-nosubject.json"
printf '%s\n' '{"schema":1,"signed":false,"provider":"signpath","subject":null,"files":{}}' > "${NT}/signer-unsigned-signpath.json"
printf '%s\n' '{"schema":1,"signed":true,"provider":"off","subject":"CN=x","files":{}}' > "${NT}/signer-off-signed.json"
printf '%s\n' '{"schema":1,"signed":true,"provider":"kms","subject":"CN=Example Org","files":{}}' > "${NT}/signer-kms.json"
SD="sha256:$(printf '1%.0s' $(seq 64))"; UD="sha256:$(printf '2%.0s' $(seq 64))"
notes_run() { # tag mode exception signer out [extra...]
  local tag="$1" mode="$2" exc="$3" signer="$4" out="$5" v base tags
  shift 5
  v="${tag#v}"; base="${v%%-*}"
  case "${v}" in *-rc.*) tags="${v}" ;; *) tags="${base},${base%.*}" ;; esac
  WINDOWS_SIGNING="${mode}" WINDOWS_UNSIGNED_FINAL_TAG="${exc}" IMAGE_TAGS="${tags}" SERVER_INDEX_DIGEST="${SD}" UPDATER_INDEX_DIGEST="${UD}" \
    run_priv "${REL}/notes.sh" --tag "${tag}" --commit "${C40}" --signer "${signer}" --out "${out}" --changelog "${CL}" "$@"
}
common_checks() { # name file tag
  local t; t="$(cat "$2")"
  lacks "$1: no {{" "{{" "${t}"; lacks "$1: no }}" "}}" "${t}"
  lacks "$1: no <!-- if" "<!-- if" "${t}"; lacks "$1: no <!-- end" "<!-- end" "${t}"
  lacks "$1: no SPDX line" "SPDX-License-Identifier" "${t}"
  contains "$1: the tag" "$3" "${t}"; contains "$1: the commit" "${C40}" "${t}"
  contains "$1: the server digest" "${SD}" "${t}"; contains "$1: the updater digest" "${UD}" "${t}"
  contains "$1: the exact identity" "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/$3" "${t}"
  contains "$1: the ADR-0029 pattern" '^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v' "${t}"
  contains "$1: the bound pattern" "release\\.yml@refs/tags/v$(printf '%s' "${3#v}" | sed -e 's/[.]/\\./g')\$" "${t}"
  contains "$1: the gh signer workflow" "--signer-workflow myriarch/myriarch/.github/workflows/release.yml@refs/tags/$3" "${t}"
  contains "$1: the provenance asset" "myriarch_${3#v}_provenance.intoto.jsonl" "${t}"
  contains "$1: the Linux updater" "myriarch-updater_${3#v}_linux_amd64" "${t}"
  contains "$1: the sha256- tags warning" "sha256-<hex>" "${t}"
  contains "$1: cosign verify accepts attestations, the cosign signature is the entry of type sign/v1" "also accepts the attestations of the same identity; the JSON entry of the type \`https://sigstore.dev/cosign/sign/v1\` is the cosign signature. " "${t}"
  contains "$1: verify-asset is the proof of release, in a sentence of its own" ". \`gh release verify-asset\` (membership of a file in this immutable release) is the proof that a file was released." "${t}"
  [ "$(tail -c 1 "$2" | od -An -c | tr -d ' ')" = '\n' ] && [ "$(tail -c 2 "$2" | od -An -c | tr -d ' ')" != '\n\n' ] && ok "$1: ends with one newline" || fail "$1: does not end with exactly one newline"
}
expect_ok "rc, unsigned" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/rc.md"
n="$(cat "${NT}/rc.md")"
common_checks "rc notes" "${NT}/rc.md" v0.1.0-rc.1
contains "rc: the rc banner" "**Release candidate.**" "${n}"
contains "rc: the unsigned banner" "**Windows files are not Authenticode-signed.**" "${n}"
lacks "rc: no exception sentence" "reviewed exception" "${n}"
lacks "rc: no alpha statement" "alpha software" "${n}"
contains "rc: the image tag" '| `0.1.0-rc.1` |' "${n}"
contains "rc: the Unreleased entries" "unreleased entry alpha" "${n}"
contains "rc: a fenced heading stays in the text" "## not a heading" "${n}"
lacks "rc: the Unreleased heading itself" "## Unreleased" "${n}"
lacks "rc: HTML comments are removed" "hidden comment" "${n}"
lacks "rc: not the final section" "final entry beta" "${n}"
contains "rc: the generated list sentence" "generated by GitHub" "${n}"
expect_ok "final, unsigned, under the exception" notes_run v0.1.0 off v0.1.0 "${NT}/signer-off.json" "${NT}/final-off.md"
n="$(cat "${NT}/final-off.md")"
common_checks "final unsigned notes" "${NT}/final-off.md" v0.1.0
lacks "final: no rc banner" "Release candidate" "${n}"
contains "final: the unsigned banner" "not Authenticode-signed" "${n}"
contains "final: the exception sentence" "reviewed exception for exactly \`v0.1.0\`" "${n}"
contains "final: the alpha statement" "alpha software" "${n}"
contains "final: both image tags" '`0.1.0` and `0.1`' "${n}"
contains "final: the dated section" "final entry beta" "${n}"
lacks "final: not the Unreleased entries" "unreleased entry alpha" "${n}"
lacks "final: not older sections" "old entry gamma" "${n}"
lacks "final: the dated heading itself" "## [0.1.0]" "${n}"
expect_ok "final, signed" notes_run v0.1.0 signpath "" "${NT}/signer-signed.json" "${NT}/final-signed.md"
n="$(cat "${NT}/final-signed.md")"
common_checks "final signed notes" "${NT}/final-signed.md" v0.1.0
contains "signed: the signer sentence" 'Authenticode-signed by `CN=Example Org,O=Example Org` (signpath) with an RFC 3161 timestamp' "${n}"
lacks "signed: no unsigned banner" "not Authenticode-signed" "${n}"
lacks "signed: no exception sentence" "reviewed exception" "${n}"
expect_ok "rc, signed (kms)" notes_run v0.1.0-rc.2 kms "" "${NT}/signer-kms.json" "${NT}/rc-signed.md"
contains "rc signed: the Release candidate banner and the signer" "Release candidate" "$(cat "${NT}/rc-signed.md")"
expect_fail "signer_mismatch: off with signed true" "signer_mismatch" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off-signed.json" "${NT}/x1.md"
expect_fail "signer_mismatch: signpath with signed false" "signer_mismatch" notes_run v0.1.0-rc.1 signpath "" "${NT}/signer-unsigned-signpath.json" "${NT}/x2.md"
expect_fail "signer_mismatch: signpath with the signer file of off" "signer_mismatch" notes_run v0.1.0-rc.1 signpath "" "${NT}/signer-off.json" "${NT}/x3.md"
expect_fail "signer_mismatch: no subject" "signer_mismatch" notes_run v0.1.0-rc.1 signpath "" "${NT}/signer-nosubject.json" "${NT}/x4.md"
expect_fail "signer_mismatch: another provider in the file" "signer_mismatch" notes_run v0.1.0-rc.1 signpath "" "${NT}/signer-kms.json" "${NT}/x5.md"
expect_fail "exception_mismatch: no exception" "exception_mismatch" notes_run v0.1.0 off "" "${NT}/signer-off.json" "${NT}/x6.md"
expect_fail "exception_mismatch: the exception is for another tag" "exception_mismatch" notes_run v0.1.0 off v0.0.9 "${NT}/signer-off.json" "${NT}/x7.md"
expect_fail "changelog_missing: no dated section" "changelog_missing" notes_run v0.2.0 off v0.2.0 "${NT}/signer-off.json" "${NT}/x8.md"
printf '# Changelog\n\n## [0.1.0] - 2026-10-20\n\n## [0.0.9] - 2026-01-01\n\n- x\n' > "${NT}/empty-section.md"
cl_save="${CL}"; CL="${NT}/empty-section.md"
expect_fail "changelog_missing: an empty section" "changelog_missing" notes_run v0.1.0 off v0.1.0 "${NT}/signer-off.json" "${NT}/x9.md"
expect_fail "changelog_missing: no Unreleased section" "changelog_missing" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/x10.md"
printf '# Changelog\n\n```text\n## Unreleased\n- hidden in a fence\n```\n' > "${NT}/fenced-only.md"
CL="${NT}/fenced-only.md"
expect_fail "changelog_missing: a heading in a code block does not count" "changelog_missing" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/x11.md"
CL="${cl_save}"
expect_fail "the output must not exist" "out_exists" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/rc.md"
bad_tags() { WINDOWS_SIGNING=off WINDOWS_UNSIGNED_FINAL_TAG="" IMAGE_TAGS=latest SERVER_INDEX_DIGEST="${SD}" UPDATER_INDEX_DIGEST="${UD}" \
  run_priv "${REL}/notes.sh" --tag v0.1.0-rc.1 --commit "${C40}" --signer "${NT}/signer-off.json" --out "${NT}/bad-tags.md" --changelog "${CL}"; }
expect_fail "IMAGE_TAGS must fit the tag" "image_tags_invalid" bad_tags
tmpl() { { printf '# Copyright header\n# SPDX-License-Identifier: Apache-2.0\n\n'; cat; } > "${NT}/$1.tmpl"; }
printf 'Hello {{NOPE}}\n' | tmpl unknown
printf '<!-- if rc -->\n<!-- if final -->\nx\n<!-- end final -->\n<!-- end rc -->\n' | tmpl nested
printf '<!-- if rc -->\nx\n' | tmpl unclosed
printf '<!-- if banana -->\nx\n<!-- end banana -->\n' | tmpl badblock
printf '<!-- end rc -->\n' | tmpl endonly
printf '{{WINDOWS_SIGNER_SUBJECT}}\n' | tmpl restricted
printf '{{ TAG }}\n' | tmpl leftover1
printf '<!-- if rc --> trailing\n' | tmpl leftover2
printf '## T\n\n{{TAG}}\n\n{{CHANGELOG_SECTION}}\n' | tmpl minimal
for t in unknown nested unclosed badblock endonly restricted; do
  expect_fail "template_invalid: ${t}" "template_invalid" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/t_${t}.md" --template "${NT}/${t}.tmpl"
done
for t in leftover1 leftover2; do
  expect_fail "template_leftover: ${t}" "template_leftover" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/t_${t}.md" --template "${NT}/${t}.tmpl"
done
expect_ok "a minimal template" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/t_min.md" --template "${NT}/minimal.tmpl"
eq "the minimal template: header dropped, blank lines collapsed" "## T

v0.1.0-rc.1

### Added

- unreleased entry alpha

\`\`\`text
## not a heading
- fenced line
\`\`\`" "$(cat "${NT}/t_min.md")"
# a very long changelog section is replaced by a link; a very long template is refused
python3 -I - "${NT}" <<'PY'
import sys
d = sys.argv[1]
with open(d + "/long-changelog.md", "w") as f:
    f.write("# Changelog\n\n## Unreleased\n\n" + "".join("- entry number %05d with some words to fill the line up\n" % i for i in range(1700)) + "\n## [0.0.1] - 2026-01-01\n\n- x\n")
with open(d + "/mid-changelog.md", "w") as f:
    f.write("# Changelog\n\n## Unreleased\n\n" + "".join("- entry number %05d with some words to fill the line up\n" % i for i in range(1000)) + "\n## [0.0.1] - 2026-01-01\n\n- x\n")
with open(d + "/huge.tmpl", "w") as f:
    f.write("# header\n\n## T\n\n" + "".join("filler line %05d of the template padding it out and out\n" % i for i in range(800)) + "\n{{CHANGELOG_SECTION}}\n")
PY
cl_save="${CL}"; CL="${NT}/long-changelog.md"
expect_ok "a changelog section of more than 60,000 characters" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/long.md"
n="$(cat "${NT}/long.md")"
contains "the long section is replaced by the link" "[CHANGELOG.md](https://github.com/myriarch/myriarch/blob/v0.1.0-rc.1/CHANGELOG.md)" "${n}"
lacks "the long section is not in the notes" "entry number 00001" "${n}"
CL="${NT}/mid-changelog.md"
expect_fail "notes_too_long" "notes_too_long" notes_run v0.1.0-rc.1 off "" "${NT}/signer-off.json" "${NT}/toolong.md" --template "${NT}/huge.tmpl"
CL="${cl_save}"

# ==== 11. draft.sh ===========================================================================================================================
section "11. draft.sh"
NOTES_FILE="${NT}/rc.md"
ALLN="$(gen names "${ver}" final | paste -sd+ -)"
gen assets 9.9.9-rc.1 "${TEST_TMP}/far" "${TEST_TMP}/sigsr"
ALLN_RC="$(gen names 9.9.9-rc.1 final | paste -sd+ -)"
bot='github-actions[bot]'
dr() { # tag assets-dir sig-dir [extra...] : with GH_TOKEN, RELEASE_PRERELEASE (from the tag) and NOTES_START_TAG
  local tag="$1" a="$2" sg="$3" pre=false; shift 3
  case "${tag}" in *-rc.*) pre=true ;; esac
  GH_TOKEN=stub-token-not-secret RELEASE_PRERELEASE="${RP_PRE:-${pre}}" NOTES_START_TAG="${RP_START:-}" run_priv "${REL}/draft.sh" --tag "${tag}" --commit "${DR_COMMIT:-${C40}}" --notes "${NOTES_FILE}" --assets "${a}" --signatures "${sg}" "$@"
}
create_view() { # n: head options, file basenames of the n-th gh call
  python3 -I - "${S}/calls/gh.log" "$1" <<'PY'
import os, sys
calls = [c for c in open(sys.argv[1], encoding="utf-8").read().split("----\n") if c.strip()]
toks = [x for x in calls[int(sys.argv[2]) - 1].split("\n") if x]
i = toks.index("--generate-notes")
rest = toks[i + 1:]
files = [t for t in rest if t.startswith("/")]
opts = [t for t in rest if not t.startswith("/")]
dirs = {os.path.dirname(f) for f in files}
print(" ".join(toks[:i + 1]).replace(toks[toks.index("--notes-file") + 1], "NOTES"), "|", " ".join(opts), "|", len(files), "files in", len(dirs), "directory")
print("\n".join(os.path.basename(f) for f in files))
PY
}
reset_stubs
gen list "${S}" list.1.json v9.9.9 ""
gen list "${S}" list.2.json v9.9.9 "101,1,${bot},${ALLN}"
expect_ok "draft: no releases yet" dr v9.9.9 "${FA}" "${SIGS}"
contains "draft: stdout is release-id" "release-id=101" "${OUT}"
eq "draft: the first call lists the releases" "api --paginate --slurp repos/myriarch/myriarch/releases?per_page=100" "$(call_line gh 1)"
eq "draft: the create call (final, no notes-start-tag)" "release create v9.9.9 --repo myriarch/myriarch --draft --verify-tag --target ${C40} --title v9.9.9 --notes-file NOTES --generate-notes |  | 42 files in 1 directory" "$(create_view 2 | head -n 1)"
eq "draft: the 42 files sorted by name" "$(gen names "${ver}" final)" "$(create_view 2 | tail -n +2)"
no_call gh '(^| )--(prerelease|latest)( |$)' && ok "draft: neither --prerelease nor --latest for a final tag" || fail "draft: forbidden flag"
no_call gh '(^| )(-X DELETE)' && ok "draft: nothing to delete" || fail "draft: unexpected DELETE"
eq "draft: three gh calls (list, create, list)" "3" "$(call_count gh '.')"
reset_stubs
gen list "${S}" list.1.json v9.9.9-rc.1 ""
gen list "${S}" list.2.json v9.9.8-rc.1 "30,0,someone,${ALLN_RC}"
gen list "${S}" list.3.json v9.9.9-rc.1 "55,1,${bot},${ALLN_RC}"
RP_START=v9.9.8-rc.1 expect_ok "draft: an rc with a start tag that has a published release" dr v9.9.9-rc.1 "${TEST_TMP}/far" "${TEST_TMP}/sigsr"
contains "draft rc: the draft id" "release-id=55" "${OUT}"
contains "draft rc: the start tag is logged" "the generated change list starts at v9.9.8-rc.1" "${OUT}"
eq "draft rc: --notes-start-tag then --prerelease" "release create v9.9.9-rc.1 --repo myriarch/myriarch --draft --verify-tag --target ${C40} --title v9.9.9-rc.1 --notes-file NOTES --generate-notes | --notes-start-tag v9.9.8-rc.1 --prerelease | 42 files in 1 directory" "$(create_view 3 | head -n 1)"
eq "draft rc: the start tag is looked up after the listing of the tag" "4" "$(call_count gh '.')"
# the start tag was never published (the attempt failed before publication) or only exists as a draft: the option is left out
reset_stubs
gen list "${S}" list.1.json v9.9.9-rc.1 ""
gen list "${S}" list.2.json v9.9.8-rc.1 ""
gen list "${S}" list.3.json v9.9.9-rc.1 "56,1,${bot},${ALLN_RC}"
RP_START=v9.9.8-rc.1 expect_ok "draft: an rc whose start tag has no release" dr v9.9.9-rc.1 "${TEST_TMP}/far" "${TEST_TMP}/sigsr"
contains "draft rc: the missing release of the start tag is logged" "v9.9.8-rc.1 has no published release" "${OUT}"
eq "draft rc: no --notes-start-tag" "release create v9.9.9-rc.1 --repo myriarch/myriarch --draft --verify-tag --target ${C40} --title v9.9.9-rc.1 --notes-file NOTES --generate-notes | --prerelease | 42 files in 1 directory" "$(create_view 3 | head -n 1)"
reset_stubs
gen list "${S}" list.1.json v9.9.9-rc.1 ""
gen list "${S}" list.2.json v9.9.8-rc.1 "31,1,${bot},${ALLN_RC}"
gen list "${S}" list.3.json v9.9.9-rc.1 "57,1,${bot},${ALLN_RC}"
RP_START=v9.9.8-rc.1 expect_ok "draft: an rc whose start tag only has a draft" dr v9.9.9-rc.1 "${TEST_TMP}/far" "${TEST_TMP}/sigsr"
eq "draft rc: a draft of the start tag does not count" "release create v9.9.9-rc.1 --repo myriarch/myriarch --draft --verify-tag --target ${C40} --title v9.9.9-rc.1 --notes-file NOTES --generate-notes | --prerelease | 42 files in 1 directory" "$(create_view 3 | head -n 1)"
reset_stubs
gen list "${S}" list.1.json v9.9.9-rc.1 "7,1,${bot},;8,1,${bot},old.txt"
gen list "${S}" list.2.json v9.9.9-rc.1 "56,1,${bot},${ALLN_RC}"
expect_ok "draft: stale drafts of the bot are deleted" dr v9.9.9-rc.1 "${TEST_TMP}/far" "${TEST_TMP}/sigsr"
eq "draft: DELETE of the first stale draft" "api -X DELETE repos/myriarch/myriarch/releases/7" "$(call_line gh 2)"
eq "draft: DELETE of the second stale draft" "api -X DELETE repos/myriarch/myriarch/releases/8" "$(call_line gh 3)"
has_call gh '^release create ' && [ "$(grep -n '^gh release create' "${S}/calls/all.log" | cut -d: -f1)" -gt "$(grep -n 'DELETE' "${S}/calls/all.log" | tail -n 1 | cut -d: -f1)" ] && ok "draft: the create call comes after the deletes" || fail "draft: wrong order"
contains "draft: the deleted ids are logged" "deleting the stale draft 7" "${OUT}"
reset_stubs
gen list "${S}" list.1.json v9.9.9 "9,0,${bot},${ALLN}"
expect_fail "draft: a published release of the tag" "release_exists.*never released again" dr v9.9.9 "${FA}" "${SIGS}"
no_call gh '^release create' && ok "draft: no create call after release_exists" || fail "draft: created despite a published release"
reset_stubs
gen list "${S}" list.1.json v9.9.9 "10,1,someone-else,"
expect_fail "draft: a draft of a human" "draft_foreign" dr v9.9.9 "${FA}" "${SIGS}"
no_call gh '-X DELETE|^release create' && ok "draft: nothing deleted or created for a foreign draft" || fail "draft: acted on a foreign draft"
reset_stubs
gen list "${S}" list.1.json v9.9.9 ""
gen list "${S}" list.2.json v9.9.9 "101,1,${bot},${ALLN};102,1,${bot},${ALLN}"
expect_fail "draft: two drafts after the create" "draft_ambiguous" dr v9.9.9 "${FA}" "${SIGS}"
gen list "${S}" list.2.json v9.9.9 ""
rm -f "${S}/list.count"
expect_fail "draft: no draft after the create" "draft_missing" dr v9.9.9 "${FA}" "${SIGS}"
gen list "${S}" list.2.json v9.9.9 "101,1,${bot},$(gen names "${ver}" final | grep -v THIRD_PARTY | paste -sd+ -)"
rm -f "${S}/list.count"
expect_fail "draft: the draft lacks an asset" "asset_missing.*THIRD_PARTY_NOTICES" dr v9.9.9 "${FA}" "${SIGS}"
gen list "${S}" list.2.json v9.9.9 "101,1,${bot},${ALLN}+stray.bin"
rm -f "${S}/list.count"
expect_fail "draft: the draft has an extra asset" "asset_unexpected.*stray.bin" dr v9.9.9 "${FA}" "${SIGS}"
gen list "${S}" list.2.json v9.9.9 "101,1,${bot},${ALLN},1"
rm -f "${S}/list.count"
expect_fail "draft: the prerelease flag differs" "prerelease_mismatch" dr v9.9.9 "${FA}" "${SIGS}"
gen list "${S}" list.2.json v9.9.9 "101,1,${bot},${ALLN}"; rm -f "${S}/list.count"; echo 1 > "${S}/create.rc"
expect_fail "draft: a failing gh release create" "create_failed" dr v9.9.9 "${FA}" "${SIGS}"
rm -f "${S}/create.rc" "${S}/list.count"
RP_PRE=true expect_fail "draft: a final tag with RELEASE_PRERELEASE=true" "prerelease_invalid" dr v9.9.9 "${FA}" "${SIGS}"
reset_stubs
DR_COMMIT=abc expect_fail "draft: a commit that is not 40 hex digits" "commit_invalid" dr v9.9.9 "${FA}" "${SIGS}"
no_call gh '.' && ok "draft: no gh call for a malformed commit" || fail "draft: gh called for a malformed commit"
reset_stubs
gen list "${S}" list.1.json v9.9.9 ""
python3 -I -c '
import json, sys
names = sys.argv[1].split("+")
json.dump([[{"id": "12x", "tag_name": "v9.9.9", "name": "v9.9.9", "draft": True, "prerelease": False, "author": {"login": "github-actions[bot]"},
             "assets": [{"name": n} for n in names]}]], open(sys.argv[2], "w"))
' "${ALLN}" "${S}/list.2.json"
expect_fail "draft: an id that is not a number never reaches GITHUB_OUTPUT" "release_id_invalid" dr v9.9.9 "${FA}" "${SIGS}"
case "${OUT}" in *release-id=*) fail "draft: release-id was printed for a malformed id" ;; *) ok "draft: no release-id line for a malformed id" ;; esac
mkdir -p "${TEST_TMP}/fa_short"; cp -R "${FA}/." "${TEST_TMP}/fa_short/"; rm "${TEST_TMP}/fa_short/LICENSE"
expect_fail "draft: a missing file stops before any gh call" "asset_missing" dr v9.9.9 "${TEST_TMP}/fa_short" "${SIGS}"

# ==== 12. verify.sh ===========================================================================================================================
section "12. verify.sh"
ckey() { printf '%s' "$1" | tr '/:@' '___'; }
REPO_S=ghcr.io/myriarch/myriarch; REPO_U=ghcr.io/myriarch/myriarch-updater
vsetup() { # [image options...]
  reset_stubs
  eval "$(gen images "${S}/crane" "${C40}" "${ver}" "$@")"
  export SERVER_INDEX_DIGEST SERVER_AMD64_DIGEST SERVER_ARM64_DIGEST UPDATER_INDEX_DIGEST UPDATER_AMD64_DIGEST UPDATER_ARM64_DIGEST
}
vr() { local o="${VOUT:-$(mktemp -d "${TEST_TMP}/vout.XXXXXX")/o}"
  GH_TOKEN=stub-token-not-secret RELEASE_TAG="${RP_TAG:-v9.9.9}" RELEASE_COMMIT="${C40}" run_priv "${REL}/verify.sh" --assets "${VA:-${FA}}" --signatures "${VS:-${SIGS}}" --out "${o}"; }
vsetup
lastout="${TEST_TMP}/vout.happy"
VOUT="${lastout}" RP_SUMMARY="${TEST_TMP}/summary.md" expect_ok "verify: the happy path" vr
eq "verify: nine cosign calls" "9" "$(call_count cosign '.')"
eq "verify: three verify-blob calls" "3" "$(call_count cosign '^verify-blob ')"
eq "verify: six image verifications" "6" "$(call_count cosign '^verify --certificate-identity ')"
strip_paths() { sed -e 's# [^ ]*/checksums.txt.sigstore.json # BUNDLE #' -e 's# [^ ]*/checksums.txt$# BLOB#'; }
eq "verify: verify-blob exact" "verify-blob --bundle BUNDLE --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --certificate-oidc-issuer ${ISS} --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/v9.9.9 --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push BLOB" "$(call_line cosign 1 | strip_paths)"
eq "verify: verify-blob pattern" "verify-blob --bundle BUNDLE --certificate-identity-regexp ^https://github\\.com/myriarch/myriarch/\\.github/workflows/release\\.yml@refs/tags/v --certificate-oidc-issuer ${ISS} BLOB" "$(call_line cosign 2 | strip_paths)"
eq "verify: verify-blob bound" "verify-blob --bundle BUNDLE --certificate-identity-regexp ^https://github\\.com/myriarch/myriarch/\\.github/workflows/release\\.yml@refs/tags/v9\\.9\\.9\$ --certificate-oidc-issuer ${ISS} BLOB" "$(call_line cosign 3 | strip_paths)"
for d in "${SERVER_INDEX_DIGEST}" "${SERVER_AMD64_DIGEST}" "${SERVER_ARM64_DIGEST}"; do
  has_call cosign "^verify --certificate-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --certificate-oidc-issuer ${ISS} --certificate-github-workflow-repository myriarch/myriarch --certificate-github-workflow-ref refs/tags/v9.9.9 --certificate-github-workflow-sha ${C40} --certificate-github-workflow-trigger push ${REPO_S}@${d}\$" \
    && ok "verify: cosign verify of ${REPO_S}@${d:0:15}" || fail "verify: no cosign verify of ${d}"
done
for d in "${UPDATER_INDEX_DIGEST}" "${UPDATER_AMD64_DIGEST}" "${UPDATER_ARM64_DIGEST}"; do
  has_call cosign " ${REPO_U}@${d}\$" && ok "verify: cosign verify of ${REPO_U}@${d:0:15}" || fail "verify: no cosign verify of ${REPO_U}@${d}"
done
eq "verify: 47 gh attestation calls" "47" "$(call_count gh '^attestation verify ')"
eq "verify: 39 file attestations with the bundle" "39" "$(call_count gh "^attestation verify [^ ]+ --bundle [^ ]*/myriarch_9.9.9_provenance.intoto.jsonl --repo myriarch/myriarch --cert-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --source-ref refs/tags/v9.9.9 --source-digest ${C40} --deny-self-hosted-runners\$")"
eq "verify: the bare updater through the attestation API" "1" "$(call_count gh "^attestation verify [^ ]*/myriarch-updater_9.9.9_linux_amd64 --repo myriarch/myriarch --signer-workflow myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --source-ref refs/tags/v9.9.9 --source-digest ${C40} --deny-self-hosted-runners\$")"
eq "verify: the updater archive through the attestation API" "1" "$(call_count gh "^attestation verify [^ ]*/myriarch-updater_9.9.9_linux_amd64.tar.gz --repo myriarch/myriarch --signer-workflow ")"
eq "verify: six image attestations" "6" "$(call_count gh '^attestation verify oci://')"
eq "verify: provenance of both indexes" "2" "$(call_count gh "^attestation verify oci://[^ ]+@(${SERVER_INDEX_DIGEST}|${UPDATER_INDEX_DIGEST}) --repo myriarch/myriarch --bundle-from-oci --cert-identity https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v9.9.9 --source-ref refs/tags/v9.9.9 --source-digest ${C40} --deny-self-hosted-runners\$")"
eq "verify: SBOMs of the four platform images" "4" "$(call_count gh '--bundle-from-oci --predicate-type https://spdx.dev/Document/v2.3 --cert-identity ')"
eq "verify: the manifest has 42 lines" "42" "$(wc -l < "${lastout}/verified-manifest.txt" | tr -d ' ')"
mkdir -p "${TEST_TMP}/vexp"; gen verified "${TEST_TMP}/vexp" "${FA}" "${SIGS}" "${ver}" "${SERVER_INDEX_DIGEST}" "${SERVER_AMD64_DIGEST}" "${SERVER_ARM64_DIGEST}" "${UPDATER_INDEX_DIGEST}" "${UPDATER_AMD64_DIGEST}" "${UPDATER_ARM64_DIGEST}"
cmp -s "${TEST_TMP}/vexp/verified-manifest.txt" "${lastout}/verified-manifest.txt" && ok "verify: verified-manifest.txt (sha256, size, name; sorted)" || fail "verify: verified-manifest.txt differs"
cmp -s "${TEST_TMP}/vexp/verified-images.txt" "${lastout}/verified-images.txt" && ok "verify: verified-images.txt (six lines)" || fail "verify: verified-images.txt differs"
cmp -s "${FA}/checksums.txt" "${lastout}/checksums.txt" && ok "verify: checksums.txt is copied" || fail "verify: checksums.txt differs"
eq "verify: the manifest is sorted bytewise" "$(cut -d' ' -f5- "${lastout}/verified-manifest.txt" | LC_ALL=C sort)" "$(cut -d' ' -f5- "${lastout}/verified-manifest.txt")"
contains "verify: the step summary" "## Verified release v9.9.9" "$(cat "${TEST_TMP}/summary.md")"
contains "verify: the summary names the digests" "${SERVER_INDEX_DIGEST}" "$(cat "${TEST_TMP}/summary.md")"
# failures
vfail() { local name="$1" want="$2"; shift 2; vsetup "$@"; expect_fail "verify: ${name}" "${want}" vr; }
vfail "an index with a third platform" "image_index" thirdplatform=1
vfail "an index with a missing platform" "image_index" noarm=1
vfail "an index of another commit" "image_index.*revision" indexrevision=ffffffffffffffffffffffffffffffffffffffff
vfail "user 1001:0" "image_config.*1001:0" user=1001:0
vfail "two layers" "image_config.*layers" layers=2
vfail "a label revision of another commit" "image_config.*revision" labelrevision=ffffffffffffffffffffffffffffffffffffffff
vfail "a tag latest" "image_tag_latest" tags=latest
vfail "an unexpected tag foo" "image_tag_unexpected.*foo" tags=foo
vfail "a tag with a v prefix" "image_tag_unexpected" tags=v1.0.0
vfail "a shell as the entrypoint" "image_config.*entrypoint" entrypoint=/bin/sh
vfail "the entrypoint of the updater in the server image" "image_config.*entrypoint" entrypoint=/usr/local/bin/myriarch-updater
vsetup; printf ' ' >> "${S}/crane/manifest.$(ckey "${REPO_S}@${SERVER_INDEX_DIGEST}")"
expect_fail "verify: an index that does not hash to the digest of this run" "image_index.*does not hash to ${SERVER_INDEX_DIGEST}" vr
vsetup; printf ' ' >> "${S}/crane/manifest.$(ckey "${REPO_U}@${UPDATER_AMD64_DIGEST}")"
expect_fail "verify: a platform manifest that does not hash to its digest" "image_config.*does not hash to ${UPDATER_AMD64_DIGEST}" vr
vsetup; echo 'https://spdx.dev/Document' > "${S}/cosign.type"
expect_fail "verify: an attestation of the identity is not a signature of the image" "signature_invalid.*no cosign signature" vr
vsetup; echo 'https://sigstore.dev/cosign/sign/v1' > "${S}/cosign.type"
expect_ok "verify: the signature type of cosign sign is accepted" vr
vsetup "tags=0.1.0-rc.1,0.1.0,0.1,sha256-$(printf 'a%.0s' $(seq 64))"
expect_ok "verify: version tags of earlier releases and signature tags are accepted" vr
vsetup; echo "verify .*@${SERVER_ARM64_DIGEST}" > "${S}/cosign.fail"
expect_fail "verify: cosign verify failing for the arm64 digest" "signature_invalid" vr
vsetup; echo "^verify-blob" > "${S}/cosign.fail"
expect_fail "verify: the signature of checksums.txt" "signature_invalid" vr
no_call gh '^attestation' && ok "verify: no provenance check after a failed signature" || fail "verify: continued after a failed signature"
vsetup; echo '^attestation verify [^ ]*/LICENSE ' > "${S}/gh.fail"
expect_fail "verify: a failing file attestation" "provenance_invalid.*LICENSE" vr
vsetup; echo 'predicate-type' > "${S}/gh.fail"
expect_fail "verify: a failing SBOM attestation" "attestation_invalid" vr
vsetup; echo "oci://${REPO_U}@${UPDATER_INDEX_DIGEST}" > "${S}/gh.fail"
expect_fail "verify: a failing image provenance" "attestation_invalid" vr
vsetup; rm "${S}/crane/manifest.$(ckey "${REPO_S}@${SERVER_INDEX_DIGEST}")"
expect_fail "verify: an index that cannot be read" "image_index" vr
vsetup
VA="${TEST_TMP}/fa_short" expect_fail "verify: a missing file" "asset_missing" vr
no_call cosign '.' && ok "verify: no cosign call after a missing file" || fail "verify: cosign called"
vsetup
RP_TAG=v9.9.8 expect_fail "verify: files of another version" "asset_unexpected" vr

# ==== 13. finalize.sh ===========================================================================================================================
section "13. finalize.sh"
fsetup() { # tag [image options...]
  FTAG="$1"; shift
  FV="${FTAG#v}"
  case "${FV}" in *-rc.*) FA_="${TEST_TMP}/far"; FS_="${TEST_TMP}/sigsr"; FPRE=true; FLATEST=false; FTAGS="${FV}" ;;
    *) FA_="${FA}"; FS_="${SIGS}"; FPRE=false; FLATEST=true; FTAGS="${FV},${FV%.*}" ;; esac
  reset_stubs
  eval "$(gen images "${S}/crane" "${C40}" "${FV}" "$@")"
  export SERVER_INDEX_DIGEST SERVER_AMD64_DIGEST SERVER_ARM64_DIGEST UPDATER_INDEX_DIGEST UPDATER_AMD64_DIGEST UPDATER_ARM64_DIGEST
  FVER="${TEST_TMP}/fver_${FV}"; rm -rf "${FVER}"
  gen verified "${FVER}" "${FA_}" "${FS_}" "${FV}" "${SERVER_INDEX_DIGEST}" "${SERVER_AMD64_DIGEST}" "${SERVER_ARM64_DIGEST}" "${UPDATER_INDEX_DIGEST}" "${UPDATER_AMD64_DIGEST}" "${UPDATER_ARM64_DIGEST}"
  gen release "${S}" 4711 "${FTAG}" "${FV}" "${FVER}/verified-manifest.txt"
  echo '{}' > "${S}/crane/manifest.ghcr.io_myriarch_charts_myriarch_sha256_${FR_CHART#sha256:}"
  : > "${S}/crane/ls.ghcr.io_myriarch_charts_myriarch"
  printf '{"tagName":"%s","isDraft":false,"isPrerelease":%s,"isImmutable":true,"databaseId":4711}\n' "${FTAG}" "${FPRE}" > "${S}/view.json"
  printf '{"ref":"refs/tags/%s","object":{"type":"commit","sha":"%s"}}\n' "${FTAG}" "${C40}" > "${S}/ref.json"
}
FR_CHART="sha256:$(printf '3%.0s' $(seq 64))"
fr() {
  GH_TOKEN=stub-token-not-secret CHART_DIGEST="${FR_CHART_DIGEST-${FR_CHART}}" RELEASE_COMMIT="${FR_COMMIT:-${C40}}" RELEASE_PRERELEASE="${FPRE}" RELEASE_LATEST="${FR_LATEST:-${FLATEST}}" IMAGE_TAGS="${FR_TAGS:-${FTAGS}}" \
    GITHUB_RUN_ATTEMPT="${FR_ATTEMPT:-1}" RELEASE_RETRY_DELAY=0 run_priv "${REL}/finalize.sh" --tag "${FTAG}" --release-id "${FR_ID:-4711}" --verified "${FVER}"
}
seq_line() { grep -n "$1" "${S}/calls/all.log" | head -n 1 | cut -d: -f1; }
seq_last() { grep -n "$1" "${S}/calls/all.log" | tail -n 1 | cut -d: -f1; }
fsetup v9.9.9-rc.1
expect_ok "finalize: an rc" fr
contains "finalize rc: stdout is the release URL" "release-url=https://github.com/myriarch/myriarch/releases/tag/v9.9.9-rc.1" "${OUT}"
eq "finalize rc: seven anonymous manifest reads (six images, the chart) with an empty DOCKER_CONFIG" "7" "$(call_count crane '^manifest [^ ]+ DOCKER_CONFIG=/')"
eq "finalize rc: no other crane call uses an anonymous config" "0" "$(call_count crane '^(ls|tag|digest|config) .*DOCKER_CONFIG=/')"
eq "finalize rc: crane tag for both image repositories and the chart" "3" "$(call_count crane '^tag ')"
has_call crane "^tag ghcr.io/myriarch/charts/myriarch@${FR_CHART} 9.9.9-rc.1 " && ok "finalize rc: the chart gets its version tag by digest" || fail "finalize rc: the chart was not tagged"
has_call crane "^tag ${REPO_S}@${SERVER_INDEX_DIGEST} 9.9.9-rc.1 " && has_call crane "^tag ${REPO_U}@${UPDATER_INDEX_DIGEST} 9.9.9-rc.1 " && ok "finalize rc: tag 9.9.9-rc.1 on the index digests" || fail "finalize rc: wrong crane tag calls"
no_call crane '(^| )latest( |$)' && ok "finalize rc: no call with latest" || fail "finalize rc: a call with latest"
eq "finalize rc: the publish call (draft=false, make_latest=false)" "api -X PATCH repos/myriarch/myriarch/releases/4711 -F draft=false -f make_latest=false" "$(call_line gh 7)"
eq "finalize rc: gh calls: read, list, tag, read again, list again, tag again, PATCH, view x2, read published, list again, verify, verify-asset" "13" "$(call_count gh '.')"
eq "finalize rc: the releases of the tag are listed three times (draft, directly before the PATCH, published)" "3" "$(call_count gh '^api --paginate --slurp repos/myriarch/myriarch/releases\?per_page=100$')"
eq "finalize rc: the release is read three times (draft, directly before the PATCH, published)" "3" "$(call_count gh '^api repos/myriarch/myriarch/releases/4711$')"
eq "finalize rc: the tag is resolved twice" "2" "$(call_count gh '^api repos/myriarch/myriarch/git/ref/tags/v9.9.9-rc.1$')"
eq "finalize rc: the second read and the second listing come directly before the tag check and the PATCH" "api repos/myriarch/myriarch/releases/4711|api --paginate --slurp repos/myriarch/myriarch/releases?per_page=100|api repos/myriarch/myriarch/git/ref/tags/v9.9.9-rc.1|api -X PATCH repos/myriarch/myriarch/releases/4711 -F draft=false -f make_latest=false" "$(call_line gh 4)|$(call_line gh 5)|$(call_line gh 6)|$(call_line gh 7)"
has_call gh '^release verify v9.9.9-rc.1 --repo myriarch/myriarch$' && has_call gh "^release verify-asset v9.9.9-rc.1 ${FVER}/checksums.txt --repo myriarch/myriarch\$" && ok "finalize rc: release verify and verify-asset" || fail "finalize rc: post checks missing"
[ "$(seq_last 'crane manifest')" -lt "$(seq_line 'crane tag')" ] && [ "$(seq_last 'crane tag')" -lt "$(seq_line 'PATCH')" ] && ok "finalize rc: anonymous reads, then tags, then the publication" || fail "finalize rc: wrong order"
fsetup v9.9.9
expect_ok "finalize: a final tag" fr
eq "finalize final: five crane tag calls (X.Y.Z and X.Y per image, X.Y.Z for the chart)" "5" "$(call_count crane '^tag ')"
has_call crane "^tag ghcr.io/myriarch/charts/myriarch@${FR_CHART} 9.9.9 " && ok "finalize final: the chart gets 9.9.9" || fail "finalize final: the chart was not tagged 9.9.9"
no_call crane "^tag ghcr.io/myriarch/charts/myriarch@[^ ]+ 9\\.9 " && ok "finalize final: the chart gets no moving tag" || fail "finalize final: the chart got X.Y"
for r in "${REPO_S}@${SERVER_INDEX_DIGEST}" "${REPO_U}@${UPDATER_INDEX_DIGEST}"; do
  has_call crane "^tag ${r} 9.9.9 " && has_call crane "^tag ${r} 9.9 " && ok "finalize final: 9.9.9 and 9.9 on ${r:0:40}" || fail "finalize final: tags missing on ${r}"
done
contains "finalize final: make_latest=true" "make_latest=true" "$(call_line gh 7)"
# the moving tag X.Y
other="sha256:$(printf 'e%.0s' $(seq 64))"
mv_setup() { # label (empty for none) [digest]
  fsetup v9.9.9
  echo "${2:-${other}}" > "${S}/crane/digest.$(ckey "${REPO_S}:9.9")"
  echo 9.9 >> "${S}/crane/ls.$(ckey "${REPO_S}")"
  [ -z "$1" ] || printf '{"config":{"Labels":{"org.opencontainers.image.version":"%s"}}}\n' "$1" > "${S}/crane/config.$(ckey "${REPO_S}:9.9")"
}
mv_setup "" "${SERVER_INDEX_DIGEST}"
expect_ok "finalize: X.Y already at the index" fr
eq "finalize X.Y same digest: no tag call for 9.9 on the server image" "0" "$(call_count crane "^tag ${REPO_S}@[^ ]+ 9\\.9 ")"
mv_setup 9.9.8
expect_ok "finalize: X.Y at an older version" fr
has_call crane "^tag ${REPO_S}@${SERVER_INDEX_DIGEST} 9.9 " && ok "finalize X.Y older: re-tagged" || fail "finalize X.Y older: not re-tagged"
mv_setup 9.9.10
expect_ok "finalize: X.Y at a newer version" fr
eq "finalize X.Y newer: not moved" "0" "$(call_count crane "^tag ${REPO_S}@[^ ]+ 9\\.9 ")"
contains "finalize X.Y newer: the notice" "stays at 9.9.10" "${OUT}"
mv_setup ""
expect_fail "finalize: X.Y with an unreadable label" "image_tag_conflict" fr
no_call gh '-X PATCH' && no_call crane '^tag ' && ok "finalize X.Y unreadable: nothing tagged or published" || fail "finalize X.Y unreadable: acted"
fsetup v9.9.9
echo "${other}" > "${S}/crane/digest.$(ckey "${REPO_S}:9.9.9")"; echo 9.9.9 >> "${S}/crane/ls.$(ckey "${REPO_S}")"
expect_fail "finalize: 9.9.9 on another digest" "image_tag_conflict" fr
no_call gh '-X PATCH' && no_call crane '^tag ' && ok "finalize conflict: no tag call and no PATCH" || fail "finalize conflict: acted"
# refusals with no PATCH call
refuse_case() { # name regex  (state prepared by the caller)
  expect_fail "finalize: $1" "$2" fr
  no_call gh '-X PATCH' && ok "finalize: $1 - no PATCH call" || fail "finalize: $1 - PATCH was called"
}
fsetup v9.9.9-rc.1; touch "${S}/crane.anon.fail"
refuse_case "anonymous read fails" "ghcr_not_public.*cannot be read without credentials.*Change visibility"
no_call crane '^tag ' && ok "finalize: no crane tag before the visibility check passes" || fail "finalize: tagged although not public"
fsetup v9.9.9-rc.1; touch "${S}/crane.anon.fail.chart"
refuse_case "the chart is private" "ghcr_not_public.*charts/myriarch cannot be read without credentials.*Change visibility"
no_call crane '^tag ' && ok "finalize: no crane tag while the chart is private" || fail "finalize: tagged although the chart is private"
fsetup v9.9.9-rc.1; FR_CHART_DIGEST="" refuse_case "no chart digest" "verified_mismatch.*CHART_DIGEST"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" draft=0
refuse_case "published and attempt 1" "release_not_draft.*never publish the draft by hand"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" tag=v9.9.8-rc.1
refuse_case "tag mismatch" "draft_foreign"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" author=someone
refuse_case "a human author" "draft_foreign"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" name=other
refuse_case "another title" "draft_foreign"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" prerelease=0
refuse_case "another prerelease flag" "draft_foreign"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "others=4712,1,github-actions[bot]"
refuse_case "a second release of the tag" "draft_ambiguous"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "drop=LICENSE"
refuse_case "a missing asset" "asset_missing.*LICENSE"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "extra=stray.bin"
refuse_case "an extra asset" "asset_unexpected.*stray.bin"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "digest_wrong=LICENSE"
refuse_case "a digest mismatch" "asset_digest_mismatch.*LICENSE"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "digest_null=LICENSE"
refuse_case "a digest that is null" "asset_digest_missing.*LICENSE"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "size_wrong=LICENSE"
refuse_case "a size mismatch" "asset_size_mismatch.*LICENSE"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "uploader=someone"
refuse_case "an asset of another uploader" "asset_foreign"
fsetup v9.9.9-rc.1; gen release "${S}" 4711 v9.9.9-rc.1 9.9.9-rc.1 "${FVER}/verified-manifest.txt" "state=starter"
refuse_case "an asset that is not uploaded" "asset_state"
fsetup v9.9.9-rc.1; echo 9.9.9-rc.1 >> "${S}/crane/ls.ghcr.io_myriarch_charts_myriarch"; echo "sha256:$(printf '4%.0s' $(seq 64))" > "${S}/crane/digest.ghcr.io_myriarch_charts_myriarch_9.9.9-rc.1"
refuse_case "a chart tag that points at another digest" "chart_tag_conflict.*never moved"
no_call crane '^tag ' && ok "finalize: no tag at all after a chart tag conflict" || fail "finalize: tagged although the chart tag conflicts"
fsetup v9.9.9-rc.1; echo latest >> "${S}/crane/ls.ghcr.io_myriarch_charts_myriarch"
refuse_case "latest in the chart repository" "image_tag_latest"
fsetup v9.9.9-rc.1; echo 9.9.9-rc.1 >> "${S}/crane/ls.ghcr.io_myriarch_charts_myriarch"; echo "${FR_CHART}" > "${S}/crane/digest.ghcr.io_myriarch_charts_myriarch_9.9.9-rc.1"
expect_ok "finalize: a chart tag that already points at the chart digest" fr
no_call crane "^tag ghcr.io/myriarch/charts/myriarch" && ok "finalize: the chart tag is kept" || fail "finalize: the kept chart tag was set again"
fsetup v9.9.9-rc.1; FR_TAGS="9.9.9-rc.1,latest"
refuse_case "latest in IMAGE_TAGS" "image_tag_latest"
unset FR_TAGS
fsetup v9.9.9-rc.1; echo latest >> "${S}/crane/ls.$(ckey "${REPO_U}")"
refuse_case "latest in the repository" "image_tag_latest"
fsetup v9.9.9-rc.1; FR_ID=99
refuse_case "a release id that is not the draft" "draft_foreign|gh_failed"
unset FR_ID
fsetup v9.9.9-rc.1; sed -i.bak '1d' "${FVER}/verified-manifest.txt"; rm -f "${FVER}/verified-manifest.txt.bak"
refuse_case "a manifest with 41 lines" "verified_mismatch"
fsetup v9.9.9-rc.1; sed -i.bak "s/${SERVER_INDEX_DIGEST}/${other}/" "${FVER}/verified-images.txt"; rm -f "${FVER}/verified-images.txt.bak"
refuse_case "verified images of another digest" "verified_mismatch"
fsetup v9.9.9-rc.1; FR_LATEST=true
refuse_case "latest=true for an rc" "prerelease_invalid"
unset FR_LATEST
# a re-run after the publication
fsetup v9.9.9
for t in 9.9.9 9.9; do for r in "${REPO_S}:${SERVER_INDEX_DIGEST}" "${REPO_U}:${UPDATER_INDEX_DIGEST}"; do
  echo "${r#*:}" > "${S}/crane/digest.$(ckey "${r%%:*}:${t}")"; echo "${t}" >> "${S}/crane/ls.$(ckey "${r%%:*}")"
done; done
echo "${FR_CHART}" > "${S}/crane/digest.ghcr.io_myriarch_charts_myriarch_9.9.9"; echo 9.9.9 >> "${S}/crane/ls.ghcr.io_myriarch_charts_myriarch"
gen release "${S}" 4711 v9.9.9 9.9.9 "${FVER}/verified-manifest.txt" draft=0
FR_ATTEMPT=2 expect_ok "finalize: a re-run after the publication" fr
no_call gh '-X PATCH' && ok "finalize resume: no PATCH" || fail "finalize resume: PATCH was called"
no_call crane '^tag ' && ok "finalize resume: the tags are kept as they are" || fail "finalize resume: crane tag was called"
has_call gh '^release verify ' && ok "finalize resume: the post checks run" || fail "finalize resume: no post checks"
# post checks
fsetup v9.9.9-rc.1; sed -i.bak 's/"isImmutable":true/"isImmutable":false/' "${S}/view.json"
expect_fail "finalize: a release that is not immutable" "release_not_immutable.*published but NOT immutable.*stays mutable" fr
has_call gh '-X PATCH' && ok "finalize: not immutable is detected after the publication" || fail "finalize: no PATCH before the immutability check"
eq "finalize: the immutability check is retried 10 times" "11" "$(call_count gh '^release view ')"
fsetup v9.9.9-rc.1; echo 10 > "${S}/verify.failures"
expect_fail "finalize: gh release verify failing 10 times" "release_attestation" fr
fsetup v9.9.9-rc.1; echo 2 > "${S}/verify.failures"
expect_ok "finalize: gh release verify failing twice, then passing" fr
eq "finalize: three attempts of gh release verify" "3" "$(call_count gh '^release verify v9.9.9-rc.1 ')"
fsetup v9.9.9-rc.1; echo 10 > "${S}/verify-asset.failures"
expect_fail "finalize: gh release verify-asset failing" "release_asset_attestation" fr
fsetup v9.9.9-rc.1; echo 1 > "${S}/patch.rc"
expect_fail "finalize: a failing publication" "publish_failed" fr
# the draft and the tag are checked again directly before the publication, and the published release once more
alt_release() { # n options...: the n-th read of release 4711 returns the variant, the others the verified state
  local n="$1"; shift
  gen release "${S}" 4711 "${FTAG}" "${FV}" "${FVER}/verified-manifest.txt" "$@"
  mv "${S}/release.4711.json" "${S}/release.4711.${n}.json"
  gen release "${S}" 4711 "${FTAG}" "${FV}" "${FVER}/verified-manifest.txt"
}
fsetup v9.9.9-rc.1; alt_release 2 "extra=stray.bin"
refuse_case "an asset replaced between the check and the publication" "asset_unexpected.*stray.bin"
no_call gh '^release (view|verify)' && ok "finalize: nothing is checked after the refusal before the PATCH" || fail "finalize: post checks ran"
fsetup v9.9.9-rc.1; alt_release 2 "digest_wrong=LICENSE"
refuse_case "a changed digest between the check and the publication" "asset_digest_mismatch.*LICENSE"
fsetup v9.9.9-rc.1; alt_release 2 draft=0
refuse_case "the draft published by hand while the tags were set" "release_not_draft.*published while this job set the image tags"
fsetup v9.9.9-rc.1; alt_release 3 "extra=stray.bin" draft=0
expect_fail "finalize: an asset added after the publication" "release_assets_changed.*immutable.*asset_unexpected.*stray.bin.*publish a new tag" fr
has_call gh '-X PATCH' && ok "finalize: the difference after the publication is found after the PATCH" || fail "finalize: no PATCH before the post read"
no_call gh '^release verify ' && ok "finalize: no attestation check passes over a changed release" || fail "finalize: verified a changed release"
fsetup v9.9.9-rc.1; alt_release 3 "digest_wrong=LICENSE" draft=0
expect_fail "finalize: a changed digest after the publication" "release_assets_changed.*asset_digest_mismatch.*LICENSE" fr
fsetup v9.9.9-rc.1; alt_release 3 draft=1
expect_fail "finalize: a release that is a draft after the PATCH" "release_state" fr
# a refusal of the identity check of a re-read ends the script with its own code, not with release_not_draft or release_state
fsetup v9.9.9-rc.1; alt_release 2 author=someone
refuse_case "a foreign author between the check and the publication" "draft_foreign"
lacks "finalize: the foreign draft before the PATCH is not reported as release_not_draft" "release_not_draft" "${OUT}"
fsetup v9.9.9-rc.1; alt_release 3 author=someone draft=0
expect_fail "finalize: a foreign author after the publication" "draft_foreign" fr
lacks "finalize: the foreign release after the PATCH is not reported as release_state" "release_state" "${OUT}"
no_call gh '^release verify ' && ok "finalize: no attestation check passes over a foreign release" || fail "finalize: verified a foreign release"
# the release of the tag must still be the only one at every re-read
alt_list() { # n options...: the n-th listing of the releases returns the variant, the others the verified state
  local n="$1"; shift
  gen release "${S}" 4711 "${FTAG}" "${FV}" "${FVER}/verified-manifest.txt" "$@"
  mv "${S}/list.json" "${S}/list.${n}.json"
  gen release "${S}" 4711 "${FTAG}" "${FV}" "${FVER}/verified-manifest.txt"
}
fsetup v9.9.9-rc.1; alt_list 2 "others=4712,1,github-actions[bot]"
refuse_case "a second release of the tag between the check and the publication" "draft_ambiguous.*4712"
no_call gh '^release (view|verify)' && ok "finalize: nothing is checked after the second release appeared before the PATCH" || fail "finalize: post checks ran"
fsetup v9.9.9-rc.1; alt_list 3 "others=4712,0,github-actions[bot]"
expect_fail "finalize: a second release of the tag after the publication" "draft_ambiguous.*4712" fr
has_call gh '-X PATCH' && ok "finalize: the second release is found after the PATCH" || fail "finalize: no PATCH before the post listing"
no_call gh '^release verify ' && ok "finalize: no attestation check passes over an ambiguous tag" || fail "finalize: verified an ambiguous tag"
# a tag that does not exist (any more) is tag_moved, not gh_failed
fsetup v9.9.9-rc.1; touch "${S}/ref.404"
refuse_case "a tag that does not exist" "tag_moved.*does not exist"
lacks "finalize: a missing tag is not reported as gh_failed" "gh_failed" "${OUT}"
fsetup v9.9.9-rc.1; touch "${S}/ref.2.404"
refuse_case "a tag that was deleted while the image tags were set" "tag_moved.*does not exist"
has_call crane '^tag ' && ok "finalize: the deleted tag is found after the image tags" || fail "finalize: no crane tag before the second tag check"
fsetup v9.9.9-rc.1; T40="$(printf '7%.0s' $(seq 40))"
printf '{"object":{"type":"tag","sha":"%s"}}\n' "${T40}" > "${S}/ref.json"; touch "${S}/tagobj.${T40}.404"
refuse_case "an annotated tag whose object does not exist" "tag_moved.*does not exist"
# the tag must still point at the commit that was signed and verified
fsetup v9.9.9-rc.1; FR_COMMIT="$(printf 'f%.0s' $(seq 40))"
refuse_case "a tag on another commit than RELEASE_COMMIT" "tag_moved.*resolves to ${C40}"
no_call crane '^tag ' && ok "finalize: no image tag is set for a moved git tag" || fail "finalize: tagged although the git tag moved"
unset FR_COMMIT
fsetup v9.9.9-rc.1; printf '{"object":{"type":"commit","sha":"%s"}}\n' "$(printf 'f%.0s' $(seq 40))" > "${S}/ref.2.json"
refuse_case "a tag that moved while the image tags were set" "tag_moved"
has_call crane '^tag ' && ok "finalize: the second tag check comes after the image tags" || fail "finalize: no crane tag before the second tag check"
fsetup v9.9.9-rc.1; T40="$(printf '7%.0s' $(seq 40))"
printf '{"object":{"type":"tag","sha":"%s"}}\n' "${T40}" > "${S}/ref.json"
printf '{"object":{"type":"commit","sha":"%s"}}\n' "${C40}" > "${S}/tagobj.${T40}.json"
expect_ok "finalize: an annotated tag is dereferenced to the commit" fr
has_call gh "^api repos/myriarch/myriarch/git/tags/${T40}\$" && ok "finalize: the tag object was read" || fail "finalize: no read of the tag object"
fsetup v9.9.9-rc.1
printf '{"object":{"type":"tag","sha":"%s"}}\n' "${T40}" > "${S}/ref.json"
printf '{"object":{"type":"commit","sha":"%s"}}\n' "$(printf 'f%.0s' $(seq 40))" > "${S}/tagobj.${T40}.json"
refuse_case "an annotated tag on another commit" "tag_moved"
fsetup v9.9.9-rc.1
printf '{"object":{"type":"tag","sha":"%s"}}\n' "${T40}" > "${S}/ref.json"
printf '{"object":{"type":"tag","sha":"%s"}}\n' "${T40}" > "${S}/tagobj.${T40}.json"
refuse_case "a chain of tag objects that never reaches a commit" "tag_moved"
fsetup v9.9.9-rc.1; printf '{"object":{"type":"blob","sha":"%s"}}\n' "${C40}" > "${S}/ref.json"
refuse_case "a tag that is not a commit or a tag object" "tag_moved"
fsetup v9.9.9-rc.1; FR_COMMIT=abc
refuse_case "RELEASE_COMMIT that is not a commit" "commit_invalid"
unset FR_COMMIT
fsetup v9.9.9-rc.1; echo "sha256:$(printf 'e%.0s' $(seq 64))" > "${S}/crane.tag.wrong"
refuse_case "an image tag that does not point at the index after tagging" "image_tag_mismatch"

# ==== 14. the rehearsal hook ======================================================================================================================
section "14. rehearsal.sh stages the assets after pass 2"
rh="$(grep -n '^ci/release/stage-assets.sh' "${REL}/rehearsal.sh" | head -n 1 | cut -d: -f1)"
pass2="$(grep -n 'MYRIARCH_WINDOWS_STAGE=apply deploy/release/windows-signed.sh check-dist' "${REL}/rehearsal.sh" | head -n 1 | cut -d: -f1)"
[ -n "${rh}" ] && [ -n "${pass2}" ] && [ "${rh}" -gt "${pass2}" ] && ok "rehearsal.sh calls stage-assets.sh after the check of pass 2" || fail "rehearsal.sh: stage-assets.sh is missing or before pass 2"
grep -q 'stage-assets.sh --dist dist --root \. --version "\${snapshot_version}" --out "\${R}/staged"' "${REL}/rehearsal.sh" && ok "rehearsal.sh: the call stages dist/ into .cache/release-rehearsal/staged" || fail "rehearsal.sh: unexpected stage-assets.sh call"

# ==== 15. tools.sh (check) and images.sh ======================================================================================================
section "15. tools.sh check, images.sh"
# a tree like the one that `tools.sh build --set signing` writes (the build itself needs the network and a C compiler)
TL="${TEST_TMP}/tools"
mkdir -p "${TL}/linux-amd64" "${TL}/linux-arm64" "${TL}/windows-amd64"
printf 'a\n' > "${TL}/linux-amd64/cosign"; printf 'b\n' > "${TL}/linux-amd64/.stamp-cosign-v1-abc"; printf 'c\n' > "${TL}/linux-arm64/cosign"; printf 'd\n' > "${TL}/windows-amd64/cosign.exe"
printf 'g\n' > "${TL}/linux-amd64/crane"; printf 'h\n' > "${TL}/linux-amd64/gh"; printf 'i\n' > "${TL}/linux-amd64/helm"
printf 'cosign v1\ncrane v2\ngh v3\nhelm v4\n' > "${TL}/VERSIONS"
mksums() { ( cd "$1" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do printf '%s  %s\n' "$(sha256_of "${f}")" "${f}"; done > SHA256SUMS ); }
mksums "${TL}"
tc() { run_priv "${REL}/tools.sh" check --set "$1" --dir "${2:-${TL}}" --platform "$3"; }
for pl in linux-amd64 linux-arm64 windows-amd64; do expect_ok "tools check: the signing set, ${pl}" tc signing "${TL}" "${pl}"; done
expect_fail "tools check: a tree of the signing set is not the build set" "tools_sums.*VERSIONS names 'cosign crane gh helm', the build set is" tc build "${TL}" linux-amd64
expect_fail "tools check: the set is required" "usage:" run_priv "${REL}/tools.sh" check --dir "${TL}" --platform linux-amd64
expect_fail "tools check: an unknown set" "usage:" tc other "${TL}" linux-amd64
printf 'x' >> "${TL}/linux-arm64/cosign"
expect_fail "tools check: a changed file" "tools_sums.*linux-arm64/cosign" tc signing "${TL}" linux-arm64
expect_ok "tools check: the other platforms are not affected" tc signing "${TL}" linux-amd64
printf 'e\n' > "${TL}/linux-amd64/extra"
expect_fail "tools check: a file that SHA256SUMS does not list" "tools_sums.*extra" tc signing "${TL}" linux-amd64
expect_fail "tools check: an unknown platform" "usage:" tc signing "${TL}" macos
TB="${TEST_TMP}/tools-build"; mkdir -p "${TB}/linux-amd64"
for b in goreleaser syft yq go-winres osslsigncode; do printf '%s\n' "${b}" > "${TB}/linux-amd64/${b}"; done
printf 'go-winres v1\ngoreleaser v2\nosslsigncode 3\nsyft v4\nyq v5\n' > "${TB}/VERSIONS"; mksums "${TB}"
expect_ok "tools check: the build set" tc build "${TB}" linux-amd64
expect_fail "tools check: a tree of the build set is not the signing set" "tools_sums.*the signing set is" tc signing "${TB}" linux-amd64
printf 'go-winres v1\ngoreleaser v2\nosslsigncode 3\nsyft v4\n' > "${TB}/VERSIONS"; mksums "${TB}"
expect_fail "tools check: VERSIONS lacks a tool of the set" "tools_sums.*VERSIONS names" tc build "${TB}" linux-amd64
rm "${TB}/VERSIONS"
expect_fail "tools check: VERSIONS is missing" "tools_sums.*VERSIONS is missing" tc build "${TB}" linux-amd64
expect_fail "tools build: a non-empty output directory" "out_not_empty" run_priv "${REL}/tools.sh" build --set signing --out "${TL}"
expect_fail "tools build: the set is required" "usage:" run_priv "${REL}/tools.sh" build --out "${TEST_TMP}/tools-x"
expect_fail "tools build: an unknown set" "usage:" run_priv "${REL}/tools.sh" build --set all --out "${TEST_TMP}/tools-x"
grep -q 'TOOLS_NO_CACHE=1 TOOLS_BIN="${bin}" "${ROOT}/ci/install-tools.sh" cosign crane gh helm ' "${REL}/tools.sh" \
  && ok "tools build: the signing set is cosign, crane, gh and helm without the cache" || fail "tools build: unexpected install-tools.sh call for the signing set"
grep -q 'TOOLS_NO_CACHE=1 TOOLS_BIN="${bin}" "${ROOT}/ci/install-tools.sh" goreleaser syft go-winres yq osslsigncode ' "${REL}/tools.sh" \
  && ok "tools build: the build set is goreleaser, syft, go-winres, yq and osslsigncode without the cache" || fail "tools build: unexpected install-tools.sh call for the build set"
# the signing set is built before anything else is installed, and never needs apt or a C compiler
signing_part="$(sed -n '/if \[ "\${set}" = signing \]; then/,/^  else$/p' "${REL}/tools.sh")"
case "${signing_part}" in *apt:*|*osslsigncode*|*goreleaser*|*syft*|*yq*) fail "tools build: the signing branch installs or runs a build tool" ;; *) ok "tools build: the signing branch installs and runs no build tool" ;; esac
for c in 'cosign github.com/sigstore/cosign/v3/cmd/cosign linux arm64' 'crane github.com/google/go-containerregistry/cmd/crane linux arm64' 'cosign github.com/sigstore/cosign/v3/cmd/cosign windows amd64'; do
  grep -q "cross_build ${c} " "${REL}/tools.sh" && ok "tools build: cross build ${c}" || fail "tools build: missing cross build ${c}"
done
contains "tools build: the job summary records VERSIONS and SHA256SUMS" 'summary "## Tools of this run: ${set}"' "$(cat "${REL}/tools.sh")"
# images.sh with a stub of scripts/build-image-crane.sh
IR="${TEST_TMP}/imgroot"
mkdir -p "${IR}/ci/release" "${IR}/scripts"
cp "${REL}/common.sh" "${REL}/images.sh" "${REL}/identity.env" "${IR}/ci/release/"; cp "${ROOT}/ci/versions.env" "${IR}/ci/"
cat > "${IR}/scripts/build-image-crane.sh" <<'STUB'
#!/usr/bin/env bash
set -eu
echo "kind=${IMAGE_KIND} source=${IMAGE_SOURCE} dist=$(ls "${DIST_DIR}" | wc -l | tr -d ' ') meta=$([ -f "${DIST_DIR}/metadata.json" ] && echo yes) tools=${TOOLS_BIN}" >> "${STUB_DIR}/build.log"
[ ! -f "${STUB_DIR}/build.fail" ] || exit 1
mkdir -p "${OUT_DIR}/${IMAGE_KIND}" "${OUT_DIR}/${IMAGE_KIND}.sbom"
echo '{}' > "${OUT_DIR}/${IMAGE_KIND}/index.json"
d() { printf 'sha256:%s' "$(printf '%s' "$1$IMAGE_KIND" | python3 -I -c 'import hashlib,sys; print(hashlib.sha256(sys.stdin.read().encode()).hexdigest())')"; }
echo '{}' > "${OUT_DIR}/${IMAGE_KIND}.sbom/linux_amd64.spdx.json"
[ -f "${STUB_DIR}/build.nosbom" ] || echo '{}' > "${OUT_DIR}/${IMAGE_KIND}.sbom/linux_arm64.spdx.json"
echo "IMAGE_KIND=${IMAGE_KIND}"
echo "IMAGE_INDEX_DIGEST=$(d index)"
if [ -f "${STUB_DIR}/build.baddigest" ]; then echo "IMAGE_MANIFEST_DIGEST_LINUX_AMD64=sha256:abc"; else echo "IMAGE_MANIFEST_DIGEST_LINUX_AMD64=$(d amd64)"; fi
echo "IMAGE_MANIFEST_DIGEST_LINUX_ARM64=$(d arm64)"
echo "IMAGE_SBOM_LINUX_AMD64=${OUT_DIR}/${IMAGE_KIND}.sbom/linux_amd64.spdx.json"
echo "IMAGE_SBOM_LINUX_ARM64=${OUT_DIR}/${IMAGE_KIND}.sbom/linux_arm64.spdx.json"
STUB
chmod +x "${IR}/scripts/build-image-crane.sh" "${IR}/ci/release/images.sh"
RELDIR="${TEST_TMP}/relroot"; rm -rf "${RELDIR}"; mkdir -p "${RELDIR}/assets" "${RELDIR}/meta"; cp -R "${O1}/assets/." "${RELDIR}/assets/"; cp "${O1}/meta/metadata.json" "${RELDIR}/meta/"
img() { run_priv "${IR}/ci/release/images.sh" --release "${RELDIR}" --version "${ver}" --out "${TEST_TMP}/imgout${IMGN:-0}"; }
reset_stubs
IMGN=1 expect_ok "images.sh: both kinds" img
out="$(IMGN=1 run_priv "${IR}/ci/release/images.sh" --release "${RELDIR}" --version "${ver}" --out "${TEST_TMP}/imgout1b" 2>/dev/null)"
eq "images.sh: stdout keys in order" "server-index
server-amd64
server-arm64
updater-index
updater-amd64
updater-arm64" "$(printf '%s\n' "${out}" | cut -d= -f1)"
eq "images.sh: every value is a digest" "6" "$(printf '%s\n' "${out}" | grep -Ec '=sha256:[0-9a-f]{64}$')"
contains "images.sh: the build runs from dist with the assets and metadata.json" "kind=server source=dist dist=41 meta=yes tools=${SB}" "$(cat "${S}/build.log")"
contains "images.sh: and for the updater" "kind=updater source=dist" "$(cat "${S}/build.log")"
reset_stubs; touch "${S}/build.baddigest"
IMGN=2 expect_fail "images.sh: a malformed digest" "image_output" img
reset_stubs; touch "${S}/build.nosbom"
IMGN=3 expect_fail "images.sh: a missing SBOM" "image_output" img
reset_stubs; touch "${S}/build.fail"
IMGN=4 expect_fail "images.sh: a failing build" "image_build" img
reset_stubs
ALLOW_DIRTY=1 IMGN=5 expect_fail "images.sh: a test knob of the build script" "image_env" img
expect_fail "images.sh: a version that metadata.json does not state" "version_mismatch" run_priv "${IR}/ci/release/images.sh" --release "${RELDIR}" --version 9.9.8 --out "${TEST_TMP}/imgout6"
expect_fail "images.sh: a non-empty output" "out_not_empty" run_priv "${IR}/ci/release/images.sh" --release "${RELDIR}" --version "${ver}" --out "${TEST_TMP}/imgout1"

# ---- no pinned tool by its bare name -----------------------------------------------------------------------------------------------
# The release workflow never puts the pinned tools on PATH; goreleaser hands its sboms commands only a reduced environment. So no
# script that the workflow or deploy/release/goreleaser.yaml runs may call a pinned tool by its bare name or look it up with have,
# need_cmd or command -v: the tools are ${TOOLS_BIN}/<name> (the first release candidate failed because sbom.sh looked for syft on PATH).
# Quoted text, comments and case labels are not code; command substitutions inside double quotes are.
# One tokenizer (ci/tests/bare-tools.py) serves this scan and the run-text check of ci/tests/release-workflow-test.sh. It knows the
# command positions (line start, after ; && || | ( $( ` {, then, do, else, if, while, until), NAME=value words and the prefixes exec,
# env, command, time, nohup, xargs and sudo in front of a command.
SCAN_FILES=("${ROOT}"/ci/release/*.sh "${ROOT}"/deploy/release/*.sh "${ROOT}/scripts/build-image-crane.sh")
scan_bare() { python3 -I "${ROOT}/ci/tests/bare-tools.py" "$@"; }
expect_ok "no release script calls a pinned tool by its bare name (${#SCAN_FILES[@]} scripts)" scan_bare "${SCAN_FILES[@]}"
mkdir -p "${TEST_TMP}/scan"
printf '%s\n' '#!/usr/bin/env bash' 'have syft || exit 1' > "${TEST_TMP}/scan/have.sh"
printf '%s\n' '#!/usr/bin/env bash' 'x=1; yq -o=json . f.yaml' > "${TEST_TMP}/scan/cmd.sh"
printf '%s\n' '#!/usr/bin/env bash' 'got="$(syft version | head -n 1)"' > "${TEST_TMP}/scan/subst.sh"
printf '%s\n' '#!/usr/bin/env bash' 'need_cmd go-winres git' > "${TEST_TMP}/scan/need.sh"
printf '%s\n' '#!/usr/bin/env bash' 'if cosign version; then :; fi' > "${TEST_TMP}/scan/if.sh"
printf '%s\n' '#!/usr/bin/env bash' 'X=1 syft version' > "${TEST_TMP}/scan/assign.sh"
printf '%s\n' '#!/usr/bin/env bash' 'exec gh version' > "${TEST_TMP}/scan/exec.sh"
printf '%s\n' '#!/usr/bin/env bash' '{ kubectl get pods; }' > "${TEST_TMP}/scan/group.sh"
printf '%s\n' '#!/usr/bin/env bash' 'env A=b yq .' > "${TEST_TMP}/scan/env.sh"
printf '%s\n' '#!/usr/bin/env bash' 'xargs -n1 helm lint' 'sudo -E crane ls x' 'time command cosign version' 'nohup kind create cluster' > "${TEST_TMP}/scan/prefix.sh"
printf '%s\n' '#!/usr/bin/env bash' 'MYRIARCH_X=1 \' '  goreleaser release' > "${TEST_TMP}/scan/cont.sh"
printf '%s\n' '#!/usr/bin/env bash' '"${TOOLS_BIN}/syft" version' 'die "syft not found (ci/install-tools.sh syft)"' '# syft is a comment' "printf 'gh is text\\n'" 'case "$1" in gh) : ;; esac' \
  'cat <<EOF' 'cosign is in a here document' 'EOF' > "${TEST_TMP}/scan/clean.sh"
expect_fail "the scan finds a have lookup" "have.sh:2: the pinned tool syft" scan_bare "${TEST_TMP}/scan/have.sh"
expect_fail "the scan finds a command after a semicolon" "cmd.sh:2: the pinned tool yq" scan_bare "${TEST_TMP}/scan/cmd.sh"
expect_fail "the scan finds a command substitution inside double quotes" "subst.sh:2: the pinned tool syft" scan_bare "${TEST_TMP}/scan/subst.sh"
expect_fail "the scan finds a need_cmd lookup" "need.sh:2: the pinned tool go-winres" scan_bare "${TEST_TMP}/scan/need.sh"
expect_fail "the scan finds a command after if" "if.sh:2: the pinned tool cosign" scan_bare "${TEST_TMP}/scan/if.sh"
expect_fail "the scan finds a command after a variable assignment" "assign.sh:2: the pinned tool syft" scan_bare "${TEST_TMP}/scan/assign.sh"
expect_fail "the scan finds a command after exec" "exec.sh:2: the pinned tool gh" scan_bare "${TEST_TMP}/scan/exec.sh"
expect_fail "the scan finds a command in a group" "group.sh:2: the pinned tool kubectl" scan_bare "${TEST_TMP}/scan/group.sh"
expect_fail "the scan finds a command after env and its assignment" "env.sh:2: the pinned tool yq" scan_bare "${TEST_TMP}/scan/env.sh"
expect_fail "the scan finds a command after xargs with an option" "prefix.sh:2: the pinned tool helm" scan_bare "${TEST_TMP}/scan/prefix.sh"
expect_fail "the scan finds a command after sudo with an option" "prefix.sh:3: the pinned tool crane" scan_bare "${TEST_TMP}/scan/prefix.sh"
expect_fail "the scan finds a command after time and command" "prefix.sh:4: the pinned tool cosign" scan_bare "${TEST_TMP}/scan/prefix.sh"
expect_fail "the scan finds a command after nohup" "prefix.sh:5: the pinned tool kind" scan_bare "${TEST_TMP}/scan/prefix.sh"
expect_fail "the scan finds a command behind a continued line" "cont.sh:2: the pinned tool goreleaser" scan_bare "${TEST_TMP}/scan/cont.sh"
expect_ok "the scan accepts TOOLS_BIN paths, comments, quoted text, case labels and here documents" scan_bare "${TEST_TMP}/scan/clean.sh"

finish_tests "release-scripts-test"
