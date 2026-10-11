#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of .github/workflows/release.yml (ADR-0035). The release workflow is the one place that signs and publishes, so its shape is
# pinned here in addition to the generic policy rules (ci/check-workflows.py, R1 to R13):
#   - the frozen signing identity: the file is named release.yml, the trigger is a pushed v* tag only, the repository and both image
#     repositories are the literals of ADR-0029 and ADR-0035;
#   - the job graph: ids, needs, permissions, environment, timeout and runner of every job equal the table of the design, exactly two
#     jobs use the environment release (sign-windows, finalize), and every output that a job reads from another job is declared in
#     its needs;
#   - the artifact protocol: every upload is one uncompressed tar with one day of retention, every download is by artifact ID with
#     the digest check, and every download is followed by an unpack against the SHA-256 that the producing job reported;
#   - no ${{ }} expression in a run text, no write to GITHUB_ENV or GITHUB_PATH, no DOCKER_CONFIG, no secret, no cache;
#   - the registry writes: push, cosign sign and the six attestations of both images run as strictly sequential steps of the job
#     push-images, each attestation after the signature of its image, and no other job pushes, signs or attests images;
#   - the pins: the six GitHub-owned actions at the SHAs the other workflows use (actions/attest is new);
#   - tool resolution: the pinned tools are reached only through TOOLS_BIN (the workflow never writes GITHUB_PATH). Every step that
#     calls a script or a program that needs a pinned tool sets TOOLS_BIN to the directory of a tool set that the same job unpacked
#     for its platform, and that set contains the tools. No run text calls a pinned tool by its bare name. Every sboms entry of
#     deploy/release/goreleaser.yaml that runs a script passes TOOLS_BIN on, because goreleaser starts such a command with a
#     reduced environment (the cause of the failure of the first release candidate).
# The checks run on the real file and, to prove that they bite, on copies with one change each. The test loads MiniYaml from
# ci/check-workflows.py (no second parser). No network, no ports.
# The mutations below are literal workflow text with ${{ }} expressions: nothing in them is meant to expand.
# shellcheck disable=SC2016
set -uo pipefail
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
need_cmd python3
WORKFLOW="${ROOT}/.github/workflows/release.yml"
CHECKER="${TEST_TMP}/check-release-workflow.py"

cat > "${CHECKER}" <<'PY'
import importlib.util
import os
import re
import sys

workflow, policy, goreleaser = sys.argv[1], sys.argv[2], sys.argv[3]
spec = importlib.util.spec_from_file_location("check_workflows", policy)
cw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cw)

problems = []
checks = 0


def check(cond, message):
    global checks
    checks += 1
    if not cond:
        problems.append(message)


# ---- the frozen identity and the pins (constants of the design) --------------------------------------------------------------
REPOSITORY = "myriarch/myriarch"
IMAGES = {"server": "ghcr.io/myriarch/myriarch", "updater": "ghcr.io/myriarch/myriarch-updater"}
PINS = {
    "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1": "v7.0.1",
    "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e": "v7.0.0",
    "actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1": "v7.1.0",
    "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9": "v7.0.2",
    "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333": "v8.0.2",
    "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6": "v4.2.2",
}
ATTEST = "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6"
UPLOAD = "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9"
DOWNLOAD = "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333"
CHECKOUT = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"

# job -> (needs, permissions, environment, timeout, runner), the table of design section 1.3
U, UA, W = "ubuntu-24.04", "${{ matrix.os }}", "windows-2025"
CR = {"contents": "read"}
JOBS = {
    "guard": ([], {"contents": "read", "checks": "read"}, None, 10, U),
    "signing-tools": (["guard"], CR, None, 30, U),
    "build-tools": (["guard"], CR, None, 30, U),
    "ui": (["guard"], CR, None, 30, U),
    "build-windows": (["guard", "build-tools"], CR, None, 30, U),
    "sign-windows": (["guard", "build-windows"], CR, "release", 30, U),
    "package": (["guard", "build-tools", "ui", "build-windows", "sign-windows"], CR, None, 45, U),
    "images": (["guard", "build-tools", "signing-tools", "package"], CR, None, 30, U),
    "sign-blobs": (["guard", "signing-tools", "package", "sign-windows"], {"contents": "read", "id-token": "write", "attestations": "write"}, None, 15, U),
    "push-images": (["guard", "signing-tools", "images", "sign-windows"], {"contents": "read", "id-token": "write", "attestations": "write", "packages": "write"}, None, 30, U),
    "draft": (["guard", "signing-tools", "package", "images", "sign-windows", "sign-blobs", "push-images"], {"contents": "write"}, None, 20, U),
    "verify": (["guard", "signing-tools", "package", "images", "sign-blobs", "push-images"], {"contents": "read", "packages": "read", "attestations": "read"}, None, 30, U),
    "verify-images": (["guard", "signing-tools", "images", "push-images"], {"contents": "read", "packages": "read"}, None, 15, UA),
    "verify-windows": (["guard", "package", "sign-windows"], CR, None, 20, W),
    "finalize": (["guard", "signing-tools", "images", "push-images", "draft", "verify", "verify-images", "verify-windows"], {"contents": "write", "packages": "write", "attestations": "read"}, "release", 20, U),
    "smoke-assets": (["guard", "signing-tools", "images", "finalize"], {"contents": "read", "attestations": "read"}, None, 20, U),
    "smoke-linux": (["guard", "signing-tools", "images", "finalize"], CR, None, 30, UA),
    "smoke-windows": (["guard", "signing-tools", "finalize"], CR, None, 30, W),
    "smoke-kind": (["guard", "images", "finalize"], CR, None, 45, U),
}
MATRIX_JOBS = {"verify-images", "smoke-linux"}
PRODUCERS = {"signing-tools", "build-tools", "ui", "build-windows", "sign-windows", "package", "images", "sign-blobs", "verify"}
# the only steps that may set TOOLS_NO_CACHE (it would make a consumer of the tools artifact ignore the stamps of the tools)
TOOLS_NO_CACHE_STEPS = {("signing-tools", "Build the signing tools"), ("build-tools", "Build the build tools"), ("ui", "Install go-licenses"),
                        ("smoke-kind", "Install kind, kubectl and helm")}
# the keys under which the token of the job may appear in a step env
TOKEN_KEYS = {"GITHUB_TOKEN", "GH_TOKEN", "REGISTRY_TOKEN"}

# ---- the file ----------------------------------------------------------------------------------------------------------------
check(os.path.basename(workflow) == "release.yml", "the workflow file must be named release.yml (the signing identity is frozen, ADR-0035)")
with open(workflow, encoding="utf-8") as f:
    text = f.read()
lines = text.split("\n")
code_lines = [(n, l) for n, l in enumerate(lines, 1) if l.strip() and not l.strip().startswith("#")]
code = "\n".join(l for _, l in code_lines)

root, errors = cw.MiniYaml(lines).parse()
check(not errors, "the file cannot be read by the policy reader: %s" % errors[:3])
top = cw.as_map(root) or {}


def tx(node):
    return cw.as_text(node)


def mp(node):
    return cw.as_map(node) or {}


def ls(node):
    return cw.as_list(node) or []


def walk(node):
    """Every scalar below a node as text (block scalars included)."""
    if node is None:
        return
    if isinstance(node.v, dict):
        for v in node.v.values():
            yield from walk(v)
    elif isinstance(node.v, list):
        for v in node.v:
            yield from walk(v)
    else:
        yield node.v


# ---- top level ----------------------------------------------------------------------------------------------------------------
check(list(top) == ["name", "on", "permissions", "concurrency", "defaults", "env", "jobs"], "the top-level keys are %s" % list(top))
check(tx(top.get("name")) == "release", "name must be release")
on = mp(top.get("on"))
check(list(on) == ["push"], "the only trigger is push (found %s)" % list(on))
push = mp(on.get("push"))
check(list(push) == ["tags"], "push has only tags (found %s)" % list(push))
check([tx(t) for t in ls(push.get("tags"))] == ["v*"], "the tag pattern must be exactly v*")
check({k: tx(v) for k, v in mp(top.get("permissions")).items()} == {"contents": "read"}, "the top-level permissions are exactly contents: read")
conc = mp(top.get("concurrency"))
check(tx(conc.get("group")) == "release-${{ github.ref }}" and tx(conc.get("cancel-in-progress")) == "false", "concurrency must group by ref and never cancel")
check(tx(mp(mp(top.get("defaults")).get("run")).get("shell")) == "bash", "defaults.run.shell must be bash")
check(set(mp(top.get("env"))) == {"GOTOOLCHAIN", "COREPACK_ENABLE_DOWNLOAD_PROMPT", "GH_NO_UPDATE_NOTIFIER", "GH_PROMPT_DISABLED", "SYFT_CHECK_FOR_APP_UPDATE"}, "the workflow env has changed")
check(tx(mp(top.get("env")).get("GOTOOLCHAIN")) == "local", "GOTOOLCHAIN must be local")

jobs = mp(top.get("jobs"))
check(set(jobs) == set(JOBS), "the job ids differ from the design: missing %s, extra %s" % (sorted(set(JOBS) - set(jobs)), sorted(set(jobs) - set(JOBS))))

# ---- every job ----------------------------------------------------------------------------------------------------------------
steps_of = {}
for jid, (needs, perms, env, timeout, runner) in JOBS.items():
    job = mp(jobs.get(jid))
    if not job:
        continue
    want_name = jid if jid not in MATRIX_JOBS else "%s (${{ matrix.os }})" % jid
    check(tx(job.get("name")) == want_name, "job %s: name must be '%s'" % (jid, want_name))
    got_needs = [tx(n) for n in ls(job.get("needs"))]
    check(sorted(got_needs) == sorted(needs), "job %s: needs is %s, expected %s" % (jid, got_needs, needs))
    got_perms = {k: tx(v) for k, v in mp(job.get("permissions")).items()}
    check(got_perms == perms, "job %s: permissions are %s, expected %s" % (jid, got_perms, perms))
    check(tx(job.get("environment")) == env, "job %s: environment is %s, expected %s" % (jid, tx(job.get("environment")), env))
    check(tx(job.get("timeout-minutes")) == str(timeout), "job %s: timeout-minutes is %s, expected %s" % (jid, tx(job.get("timeout-minutes")), timeout))
    check(tx(job.get("runs-on")) == runner, "job %s: runs-on is %s, expected %s" % (jid, tx(job.get("runs-on")), runner))
    if jid in MATRIX_JOBS:
        strategy = mp(job.get("strategy"))
        check(tx(strategy.get("fail-fast")) == "false", "job %s: fail-fast must be false" % jid)
        include = [mp(m) for m in ls(mp(strategy.get("matrix")).get("include"))]
        check([tx(m.get("os")) for m in include] == ["ubuntu-24.04", "ubuntu-24.04-arm"], "job %s: the matrix must run on ubuntu-24.04 and ubuntu-24.04-arm" % jid)
        check([(tx(m.get("tools")), tx(m.get("arch"))) for m in include] == [("linux-amd64", "amd64"), ("linux-arm64", "arm64")], "job %s: the matrix carries the wrong tools or arch" % jid)
    else:
        check("strategy" not in job, "job %s: only verify-images and smoke-linux use a matrix" % jid)
    check(set(job) <= {"name", "if", "needs", "runs-on", "timeout-minutes", "environment", "strategy", "permissions", "outputs", "steps"},
          "job %s: unexpected keys %s" % (jid, sorted(set(job) - {"name", "if", "needs", "runs-on", "timeout-minutes", "environment", "strategy", "permissions", "outputs", "steps"})))
    check("continue-on-error" not in job and "uses" not in job and "defaults" not in job and "env" not in job and "container" not in job and "services" not in job,
          "job %s: no continue-on-error, reusable workflow, defaults, job env, container or services" % jid)
    if jid == "guard":
        check(tx(job.get("if")) == "github.repository == '%s'" % REPOSITORY, "guard: the only if: is the repository test")
    else:
        check("if" not in job, "job %s: only guard has an if:" % jid)
    steps_of[jid] = [mp(s) for s in ls(job.get("steps"))]
    for s in steps_of[jid]:
        check(tx(s.get("name")) is not None, "job %s: a step has no name" % jid)
        check("if" not in s and "continue-on-error" not in s, "job %s, step '%s': no if: and no continue-on-error on steps" % (jid, tx(s.get("name"))))
    if jid in PRODUCERS:
        outputs = {k: tx(v) for k, v in mp(job.get("outputs")).items() if k in ("artifact-id", "sha256")}
        check(outputs == {"artifact-id": "${{ steps.upload.outputs.artifact-id }}", "sha256": "${{ steps.pack.outputs.sha256 }}"},
              "job %s: the artifact outputs must come from the steps upload and pack" % jid)

# exactly two jobs use the environment release
check(sorted(j for j, b in jobs.items() if "environment" in mp(b)) == ["finalize", "sign-windows"], "exactly sign-windows and finalize use an environment")
check(not re.search(r"^\s*environment:[ \t]+(?!release[ \t]*$)\S", code, re.M), "the environment is the literal release")

# the outputs that a job reads from other jobs are declared in its needs
for jid, (needs, _, _, _, _) in JOBS.items():
    for value in walk(jobs.get(jid)):
        for used in set(re.findall(r"needs\.([A-Za-z0-9_-]+)\.outputs", value)):
            check(used in needs, "job %s reads the outputs of %s, which is not in its needs" % (jid, used))

# ---- run texts, GITHUB_ENV, secrets, caches -----------------------------------------------------------------------------------
for jid, steps in steps_of.items():
    for s in steps:
        run = tx(s.get("run"))
        if run is not None:
            check("${{" not in run, "job %s, step '%s': a ${{ }} expression inside the run text (pass it through env:)" % (jid, tx(s.get("name"))))
check(not re.search(r"GITHUB_ENV|GITHUB_PATH", code), "the workflow writes to GITHUB_ENV or GITHUB_PATH")
check("DOCKER_CONFIG" not in code, "DOCKER_CONFIG is set (actions/attest reads only $HOME/.docker/config.json)")
check("secrets." not in code and "secrets:" not in code, "v0.1.0 reads no secret")
check("workflow_dispatch" not in code and "pull_request" not in code and "workflow_run" not in code and "schedule" not in code, "no trigger but the v* tag")
check(not re.search(r"^\s*(?:-\s+)?uses:\s*\./", code, re.M) and "actions/cache" not in code, "no local action and no cache")
check(not re.search(r"^\s*cache:[ \t]+(?!false[ \t]*$)\S", code, re.M), "a cache: input other than false")
check("secrets" not in [k for k in top], "no top-level secrets")

# TOOLS_NO_CACHE only where tools are built
seen = set()
for jid, steps in steps_of.items():
    for s in steps:
        env = mp(s.get("env"))
        if "TOOLS_NO_CACHE" in env:
            seen.add((jid, tx(s.get("name"))))
            check(tx(env["TOOLS_NO_CACHE"]) == "1", "job %s: TOOLS_NO_CACHE must be '1'" % jid)
check(seen == TOOLS_NO_CACHE_STEPS, "TOOLS_NO_CACHE is set on %s, expected %s" % (sorted(seen), sorted(TOOLS_NO_CACHE_STEPS)))
check(code.count("TOOLS_NO_CACHE") == len(TOOLS_NO_CACHE_STEPS), "TOOLS_NO_CACHE appears outside a step env")

# the token of the job and the ref only where the design puts them
for jid, steps in steps_of.items():
    for s in steps:
        for key, value in mp(s.get("env")).items():
            v = tx(value) or ""
            if "github.token" in v:
                check(key in TOKEN_KEYS and v == "${{ github.token }}", "job %s, step '%s': the token goes only into %s" % (jid, tx(s.get("name")), sorted(TOKEN_KEYS)))
            if re.search(r"github\.(sha|ref_name)", v):
                check(jid == "guard", "job %s reads github.sha or github.ref_name; only guard does" % jid)
for jid in JOBS:
    if jid != "guard":
        for value in walk(jobs.get(jid)):
            check(not re.search(r"github\.(sha|ref_name)", value), "job %s reads github.sha or github.ref_name; only guard does" % jid)
check(code.count("github.token") == len(re.findall(r"^\s*(?:GITHUB_TOKEN|GH_TOKEN|REGISTRY_TOKEN): \$\{\{ github\.token \}\}$", code, re.M)),
      "github.token appears outside the step env keys %s" % sorted(TOKEN_KEYS))

# ---- pins and setup actions ---------------------------------------------------------------------------------------------------
used = {}
for n, l in code_lines:
    m = re.match(r"^\s*(?:-\s+)?uses:\s*(\S+)\s+#\s*(v\S+)\s*$", l)
    if m:
        used[m.group(1)] = m.group(2)
    elif re.match(r"^\s*(?:-\s+)?uses:", l):
        check(False, "line %d: uses: without a version comment" % n)
check(set(used) <= set(PINS), "an action outside the allowed pins: %s" % sorted(set(used) - set(PINS)))
check(all(PINS[k] == v for k, v in used.items() if k in PINS), "a pin comment differs from the release of the design")
check(ATTEST in used, "actions/attest is not used")
for jid, steps in steps_of.items():
    privileged = JOBS[jid][2] is not None or any(p != "read" for p in JOBS[jid][1].values())
    for s in steps:
        uses = tx(s.get("uses")) or ""
        w = mp(s.get("with"))
        if uses.startswith("actions/checkout@"):
            check(tx(w.get("persist-credentials")) == "false", "job %s: checkout without persist-credentials: false" % jid)
            if "sparse-checkout" in w:
                check(tx(w.get("sparse-checkout-cone-mode")) == "false", "job %s: a sparse checkout needs sparse-checkout-cone-mode: false" % jid)
        if uses.startswith("actions/setup-go@"):
            check(tx(w.get("go-version-file")) == "go.mod" and tx(w.get("cache")) == "false", "job %s: setup-go needs go-version-file: go.mod and cache: false" % jid)
            check(not privileged, "job %s: setup-go in a privileged job" % jid)
        if uses.startswith("actions/setup-node@"):
            check(tx(w.get("node-version-file")) == ".node-version" and tx(w.get("package-manager-cache")) == "false", "job %s: setup-node needs .node-version and package-manager-cache: false" % jid)
            check(jid == "ui", "job %s: only ui sets up Node.js" % jid)

# ---- artifact protocol --------------------------------------------------------------------------------------------------------
for jid, steps in steps_of.items():
    for i, s in enumerate(steps):
        uses, w = tx(s.get("uses")) or "", mp(s.get("with"))
        name = tx(s.get("name"))
        if uses == UPLOAD:
            check(tx(w.get("archive")) == "false" and tx(w.get("retention-days")) == "1" and tx(w.get("if-no-files-found")) == "error",
                  "job %s, step '%s': an upload needs archive: false, retention-days: 1 and if-no-files-found: error" % (jid, name))
            check(tx(s.get("id")) == "upload" and "name" not in w, "job %s: the upload step has the id upload and no name: input" % jid)
            prev = steps[i - 1] if i else {}
            check(tx(prev.get("id")) == "pack" and "artifact.sh pack" in (tx(prev.get("run")) or ""), "job %s: an upload follows the pack step" % jid)
        if uses == DOWNLOAD:
            ids = tx(w.get("artifact-ids")) or ""
            m = re.fullmatch(r"\$\{\{ needs\.([A-Za-z0-9_-]+)\.outputs\.artifact-id \}\}", ids)
            check(m is not None and "name" not in w, "job %s, step '%s': download by artifact-ids of a needed job, never by name" % (jid, name))
            check(tx(w.get("digest-mismatch")) == "error", "job %s, step '%s': digest-mismatch must be error" % (jid, name))
            nxt = steps[i + 1] if i + 1 < len(steps) else {}
            tar = (tx(nxt.get("run")) or "")
            sha = tx(mp(nxt.get("env")).get("TAR_SHA256")) or ""
            if m:
                check(sha == "${{ needs.%s.outputs.sha256 }}" % m.group(1), "job %s, step '%s': the unpack must check the sha256 output of the same producer %s" % (jid, name, m.group(1)))
                check(m.group(1) in PRODUCERS, "job %s: %s does not produce an artifact" % (jid, m.group(1)))
            check("artifact.sh unpack" in tar and '--sha256 "${TAR_SHA256}"' in tar, "job %s, step '%s': an unpack step with --sha256 \"${TAR_SHA256}\" must follow" % (jid, name))
            if m and m.group(1) in ("signing-tools", "build-tools"):
                kind = m.group(1).split("-")[0]
                check(re.search(r"tools\.sh check --set %s --dir \S+ --platform \S+" % kind, tar) is not None,
                      "job %s, step '%s': the unpack of %s must be followed by tools.sh check --set %s in the same step" % (jid, name, m.group(1), kind))
            path = tx(w.get("path")) or ""
            base = path.rsplit("/", 1)[-1]
            check(path == "${{ runner.temp }}/in/%s" % base and ("/in/%s/%s.tar" % (base, base)) in tar, "job %s, step '%s': the unpack reads another file than the download wrote" % (jid, name))
for jid, steps in steps_of.items():
    uploads = sum(1 for s in steps if tx(s.get("uses")) == UPLOAD)
    check(uploads == (1 if jid in PRODUCERS else 0), "job %s: %d uploads" % (jid, uploads))

# ---- the two tool sets ------------------------------------------------------------------------------------------------------
# The jobs with id-token, attestations, packages or contents write (and the Windows jobs) execute only the signing set, which is built on a
# runner of its own before anything else is installed or run there; the build set (goreleaser, syft, yq, go-winres, osslsigncode, which
# needs apt packages and a C compiler) reaches only jobs with contents: read.
SIGNING_USERS = {"images", "sign-blobs", "push-images", "draft", "verify", "verify-images", "finalize", "smoke-assets", "smoke-linux", "smoke-windows"}
BUILD_USERS = {"build-windows", "package", "images"}
for jid, (needs, perms, env, _, _) in JOBS.items():
    uses_signing, uses_build = "signing-tools" in needs, "build-tools" in needs
    check(uses_signing == (jid in SIGNING_USERS), "job %s: needs signing-tools = %s, expected %s" % (jid, uses_signing, jid in SIGNING_USERS))
    check(uses_build == (jid in BUILD_USERS), "job %s: needs build-tools = %s, expected %s" % (jid, uses_build, jid in BUILD_USERS))
    if env is not None or any(v != "read" for v in perms.values()):
        check(not uses_build, "job %s has write scopes or an environment and must not execute the build tools" % jid)
        check("signing-tools" in needs or jid in ("sign-windows", "guard"), "job %s has write scopes or an environment: its tools come from the signing set" % jid)
for jid, flag in (("signing-tools", "signing"), ("build-tools", "build")):
    runs = [(tx(s.get("name")), tx(s.get("run")) or "", tx(s.get("uses")) or "") for s in steps_of.get(jid, [])]
    check([n for n, _, _ in runs] == ["Check out the tagged commit", "Set up Go", "Build the %s tools" % flag, "Pack %s-tools" % flag, "Upload %s-tools" % flag],
          "job %s: the steps must be checkout, setup-go, the build, pack and upload and nothing else (found %s)" % (jid, [n for n, _, _ in runs]))
    check(['ci/release/tools.sh build --set %s --out "${RUNNER_TEMP}/tools"' % flag] == [r for _, r, _ in runs if "tools.sh" in r],
          "job %s: the only build step is tools.sh build --set %s" % (jid, flag))

# ---- tool resolution ----------------------------------------------------------------------------------------------------------
# The workflow never writes GITHUB_PATH, so a pinned tool is found only through TOOLS_BIN (or CRANE for the image scripts). The first
# release candidate failed because a goreleaser command got no TOOLS_BIN; this part ties every step to the tool set it can use.
TOOLSET = {("signing", "linux-amd64"): {"cosign", "crane", "gh"}, ("signing", "linux-arm64"): {"cosign", "crane"},
           ("signing", "windows-amd64"): {"cosign"},
           ("build", "linux-amd64"): {"goreleaser", "syft", "yq", "go-winres", "osslsigncode"}}
BUILD_ALL = {"goreleaser", "syft", "yq", "go-winres", "osslsigncode"}
# entry point in a run text -> the pinned tools that it (or what it starts) takes from TOOLS_BIN
ENTRIES = [
    (r'"\$\{TOOLS_BIN\}/goreleaser"', BUILD_ALL),
    (r"windows-signed\.sh", {"go-winres", "osslsigncode"}),
    (r"ci/release/images\.sh", {"syft"}),
    (r"registry-login\.sh login", {"crane"}),
    (r"sign-checksums\.sh sign", {"cosign"}),
    (r"sign-image\.sh", {"cosign"}),
    (r"ci/release/draft\.sh", {"gh"}),
    (r"ci/release/verify\.sh", {"gh", "cosign", "crane"}),
    (r"ci/release/finalize\.sh", {"gh", "crane"}),
    (r"smoke\.sh assets", {"cosign", "crane", "gh"}),
    (r"smoke\.sh linux", {"cosign"}),
]
CRANE_USERS = r"ci/release/(images|push-images)\.sh"
PINNED_NAMES = ["cosign", "crane", "gh", "syft", "yq", "goreleaser", "go-winres", "osslsigncode", "kind", "kubectl", "helm"]


def matrix_platforms(jid):
    include = ls(mp(mp(mp(jobs.get(jid)).get("strategy")).get("matrix")).get("include"))
    return [tx(mp(i).get("tools")) for i in include]


def unpacked_sets(jid):
    """directory key (tools, signing-tools) -> (set kind, platforms) of the tool sets that the job unpacks and checks."""
    res = {}
    for s in steps_of[jid]:
        run = tx(s.get("run")) or ""
        for m in re.finditer(r'tools\.sh check --set (\w+) --dir "\$\{(?:RUNNER_TEMP|root)\}/([a-z-]+)" --platform (\S+)', run):
            kind, d, plat = m.group(1), m.group(2), m.group(3).strip('"')
            if plat == "${TOOLS_PLATFORM}":
                check(tx(mp(s.get("env")).get("TOOLS_PLATFORM")) == "${{ matrix.tools }}", "job %s: TOOLS_PLATFORM must be ${{ matrix.tools }}" % jid)
                plats = matrix_platforms(jid)
            else:
                plats = [plat]
            check(('--into "${RUNNER_TEMP}/%s"' % d in run) or ('--into "${root}/%s"' % d in run),
                  "job %s: the tool set in %s is checked in another directory than the one it was unpacked into" % (jid, d))
            res[d] = (kind, plats)
    return res


def tool_dir(value):
    """('tools'|'signing-tools', 'linux-amd64'|'linux-arm64'|matrix) of a TOOLS_BIN value of the form runner.temp/<dir>/<platform>."""
    m = re.fullmatch(r"\$\{\{ runner\.temp \}\}/(tools|signing-tools)/(linux-amd64|linux-arm64|\$\{\{ matrix\.tools \}\})", value or "")
    return (m.group(1), m.group(2)) if m else None


for jid, steps in steps_of.items():
    if JOBS[jid][4].startswith("windows-"):
        continue  # the pwsh scripts get explicit paths (-Cosign); the Git Bash steps only unpack and check
    un = unpacked_sets(jid)
    for s in steps:
        run, env, name = tx(s.get("run")) or "", mp(s.get("env")), tx(s.get("name"))
        need = set()
        for rx, tools in ENTRIES:
            if re.search(rx, run):
                need |= tools
        if need:
            td = tool_dir(tx(env.get("TOOLS_BIN")))
            check(td is not None, "job %s, step '%s': TOOLS_BIN must be runner.temp/<tools|signing-tools>/<platform>, found '%s'" % (jid, name, tx(env.get("TOOLS_BIN"))))
            if td:
                d, plat = td
                check(d in un, "job %s, step '%s': TOOLS_BIN points into %s, which this job does not unpack and check" % (jid, name, d))
                if d in un:
                    kind, plats = un[d]
                    if plat.startswith("${{"):
                        want = matrix_platforms(jid)
                    else:
                        want = [plat]
                        check(jid not in MATRIX_JOBS, "job %s, step '%s': TOOLS_BIN must follow the matrix (runner.temp/%s/${{ matrix.tools }}), not a fixed platform" % (jid, name, d))
                    for p in want:
                        check(p in plats, "job %s, step '%s': TOOLS_BIN names the platform %s, which this job does not unpack" % (jid, name, p))
                        missing = need - TOOLSET.get((kind, p), set())
                        check(not missing, "job %s, step '%s': the %s set of %s lacks %s" % (jid, name, kind, p, sorted(missing)))
        if re.search(CRANE_USERS, run):
            m = re.fullmatch(r"\$\{\{ runner\.temp \}\}/(tools|signing-tools)/(linux-amd64)/crane", tx(env.get("CRANE")) or "")
            check(m is not None and m.group(1) in un, "job %s, step '%s': CRANE must point into a tool set that the job unpacks (found '%s')" % (jid, name, tx(env.get("CRANE"))))
            if m and m.group(1) in un:
                check("crane" in TOOLSET.get((un[m.group(1)][0], m.group(2)), set()), "job %s, step '%s': CRANE points into a set without crane" % (jid, name))
        # no pinned tool by its bare name at the start of a command
        for line in run.split("\n"):
            for m in re.finditer(r"(?:^|[;&|(]\s*|\$\(\s*)(%s)(?=\s|$)" % "|".join(re.escape(t) for t in PINNED_NAMES), line.strip()):
                check(False, "job %s, step '%s': the pinned tool %s is called by its bare name (use \"${TOOLS_BIN}/%s\")" % (jid, name, m.group(1), m.group(1)))

# smoke-kind is the one exception: the install step and the smoke step both use the default directory .cache/bin
sk = steps_of.get("smoke-kind", [])
check(not any("TOOLS_BIN" in mp(s.get("env")) or "TOOLS_BIN" in (tx(s.get("run")) or "") for s in sk),
      "smoke-kind: no step sets TOOLS_BIN (the install step and the smoke script share the default directory)")
check([tx(s.get("run")) for s in sk if "install-tools" in (tx(s.get("run")) or "")] == ["ci/install-tools.sh kind kubectl helm"], "smoke-kind: the only install step is ci/install-tools.sh kind kubectl helm")

# the sboms entries of the goreleaser configuration: a command that runs a script gets TOOLS_BIN from the configuration, because
# goreleaser gives it only a reduced environment
with open(goreleaser, encoding="utf-8") as f:
    gr_lines = f.read().split("\n")
start = next((i for i, l in enumerate(gr_lines) if l.startswith("sboms:")), None)
check(start is not None, "deploy/release/goreleaser.yaml has no sboms block")
if start is not None:
    end = next((i for i in range(start + 1, len(gr_lines)) if gr_lines[i] and not gr_lines[i].startswith((" ", "#"))), len(gr_lines))
    sb_root, sb_errors = cw.MiniYaml(gr_lines[start:end]).parse()
    check(not sb_errors, "the sboms block cannot be read by the policy reader: %s" % sb_errors[:3])
    entries = [mp(e) for e in ls(mp(sb_root).get("sboms"))]
    check(len(entries) >= 2, "the sboms block has %d entries, expected the archives and the web UI" % len(entries))
    for e in entries:
        if tx(e.get("cmd")) == "syft":
            continue  # syft itself needs no TOOLS_BIN, but nothing else in the configuration may call it by name either
        check([tx(x) for x in ls(e.get("env"))] == ["TOOLS_BIN={{ .Env.TOOLS_BIN }}"],
              "sboms entry '%s': env must be exactly TOOLS_BIN={{ .Env.TOOLS_BIN }} (goreleaser starts the command with a reduced environment)" % tx(e.get("id")))

# ---- scripts: who may call what -----------------------------------------------------------------------------------------------
OWNERS = {
    "ci/release/guard.sh": {"guard"}, "ci/release/meta.sh": {"guard"},
    "ci/release/tools.sh build --set signing": {"signing-tools"}, "ci/release/tools.sh build --set build": {"build-tools"},
    "ci/release/tools.sh check --set signing": SIGNING_USERS, "ci/release/tools.sh check --set build": BUILD_USERS,
    
    "windows-sign-hook.sh": {"sign-windows"}, "ci/release/stage-assets.sh": {"package"}, "ci/release/images.sh": {"images"},
    "sign-checksums.sh": {"sign-blobs"}, "push-images.sh": {"push-images"}, "sign-image.sh": {"push-images"},
    "ci/release/notes.sh": {"draft"}, "ci/release/draft.sh": {"draft"}, "ci/release/verify.sh": {"verify"},
    "ci/release/finalize.sh": {"finalize"},
}
for script, owners in OWNERS.items():
    callers = {jid for jid, steps in steps_of.items() for s in steps if script in (tx(s.get("run")) or "")}
    check(callers == owners, "%s is called by %s, expected %s" % (script, sorted(callers), sorted(owners)))
for script, owner in (("registry-login.sh login", {"push-images", "verify", "verify-images", "finalize"}), ("registry-login.sh logout", {"push-images", "verify", "verify-images", "finalize"})):
    callers = {jid for jid, steps in steps_of.items() for s in steps if script in (tx(s.get("run")) or "")}
    check(callers == owner, "%s is called by %s, expected %s" % (script, sorted(callers), sorted(owner)))
for jid, steps in steps_of.items():
    for s in steps:
        run = tx(s.get("run")) or ""
        if jid not in ("push-images", "sign-blobs", "finalize"):
            check(not re.search(r"\b(crane (push|copy|tag)|cosign (sign|attest))\b", run), "job %s: only push-images, sign-blobs and finalize write to a registry or sign" % jid)
check(code.count("uses: " + ATTEST) == 7, "actions/attest must appear exactly 7 times")

# sign-blobs: sign, attest the checksums, name the provenance, in this order
sb = steps_of.get("sign-blobs", [])
kinds = []
for s in sb:
    run = tx(s.get("run")) or ""
    if tx(s.get("uses")) == ATTEST:
        w = mp(s.get("with"))
        check(set(w) == {"subject-checksums"} and tx(w.get("subject-checksums")) == "${{ runner.temp }}/release/assets/checksums.txt", "sign-blobs: the attestation takes only subject-checksums of checksums.txt")
        kinds.append("attest")
    elif "sign-checksums.sh sign" in run:
        kinds.append("sign")
    elif "sign-checksums.sh provenance" in run:
        kinds.append("provenance")
check(kinds == ["sign", "attest", "provenance"], "sign-blobs: the steps must be sign, attest, provenance (found %s)" % kinds)

# push-images: strictly sequential, every attestation after the signature of its image
pi = steps_of.get("push-images", [])
check("strategy" not in mp(jobs.get("push-images")), "push-images must not use a matrix")
sequence = []
for s in pi:
    run, uses, w = tx(s.get("run")) or "", tx(s.get("uses")) or "", mp(s.get("with"))
    if "registry-login.sh login" in run:
        sequence.append("login")
    elif "registry-login.sh logout" in run:
        sequence.append("logout")
    elif "push-images.sh" in run:
        kind = [k for k, r in IMAGES.items() if re.search(r'"%s@\$\{IMAGE_DIGEST\}"' % re.escape(r), run)]
        sequence.append("push:%s" % (kind[0] if kind else "?"))
        env = mp(s.get("env"))
        if kind:
            check(tx(env.get("IMAGE_DIGEST")) == "${{ needs.images.outputs.%s-index }}" % kind[0], "push-images: the %s push takes the index digest of the images job" % kind[0])
            check("%s/oci/%s\"" % ("${RUNNER_TEMP}", kind[0]) in run, "push-images: the %s push takes the %s layout" % (kind[0], kind[0]))
    elif "sign-image.sh" in run:
        kind = [k for k, r in IMAGES.items() if "--repository %s " % r in run]
        sequence.append("sign:%s" % (kind[0] if kind else "?"))
        if kind:
            env = mp(s.get("env"))
            for var, suffix in (("IMAGE_INDEX", "index"), ("IMAGE_AMD64", "amd64"), ("IMAGE_ARM64", "arm64")):
                check(tx(env.get(var)) == "${{ needs.images.outputs.%s-%s }}" % (kind[0], suffix), "push-images: sign %s takes %s from the images job" % (kind[0], suffix))
    elif uses == ATTEST:
        subject = tx(w.get("subject-name"))
        kind = [k for k, r in IMAGES.items() if r == subject]
        k = kind[0] if kind else "?"
        digest = tx(w.get("subject-digest")) or ""
        check(tx(w.get("push-to-registry")) == "true" and tx(w.get("create-storage-record")) == "false", "push-images: attestations push to the registry and create no storage record")
        if "sbom-path" in w:
            plat = "amd64" if digest.endswith("%s-amd64 }}" % k) else "arm64" if digest.endswith("%s-arm64 }}" % k) else "?"
            check(digest == "${{ needs.images.outputs.%s-%s }}" % (k, plat) and tx(w.get("sbom-path")) == "${{ runner.temp }}/oci/%s.sbom/linux_%s.spdx.json" % (k, plat),
                  "push-images: the SBOM attestation of %s %s takes the platform digest and its own SBOM file" % (k, plat))
            check(set(w) == {"subject-name", "subject-digest", "sbom-path", "push-to-registry", "create-storage-record"}, "push-images: unexpected inputs on an SBOM attestation")
            sequence.append("sbom:%s:%s" % (k, plat))
        else:
            check(digest == "${{ needs.images.outputs.%s-index }}" % k, "push-images: the provenance attestation of %s takes the index digest" % k)
            check(set(w) == {"subject-name", "subject-digest", "push-to-registry", "create-storage-record"}, "push-images: unexpected inputs on a provenance attestation")
            sequence.append("provenance:%s" % k)
    elif uses in (CHECKOUT, DOWNLOAD) or "artifact.sh unpack" in run:
        pass  # the scripts, the tools and the layouts
    else:
        sequence.append("other")
want = ["login"]
for k in ("server", "updater"):
    want += ["push:%s" % k, "sign:%s" % k, "provenance:%s" % k, "sbom:%s:amd64" % k, "sbom:%s:arm64" % k]
want.append("logout")
check(sequence == want, "push-images: the steps must run in the order %s (found %s)" % (want, sequence))
# no image repository outside the two literals
repos = set(re.findall(r"ghcr\.io/[A-Za-z0-9._/-]+", code))
check(repos == set(IMAGES.values()), "image repositories in the file: %s" % sorted(repos))
check(code.count(REPOSITORY) >= 1 and set(re.findall(r"myriarch/[a-z-]+", code)) >= {"myriarch/myriarch"}, "the repository literal is missing")

# ---- permissions are commented (zizmor pedantic: undocumented-permissions) ----------------------------------------------------
i = 0
while i < len(lines):
    l = lines[i]
    m = re.match(r"^(\s*)permissions:\s*$", l)
    if m:
        indent = len(m.group(1))
        j = i + 1
        while j < len(lines) and (not lines[j].strip() or len(lines[j]) - len(lines[j].lstrip()) > indent):
            if lines[j].strip() and not lines[j].strip().startswith("#"):
                check(re.search(r"\S\s+#\s*\S", lines[j]) is not None, "line %d: the permission '%s' has no comment" % (j + 1, lines[j].strip()))
            j += 1
        i = j
    else:
        i += 1

# ---- the commit reaches every step that binds the release to it (the exact identity, the tag-to-commit check, the release target) -----
COMMIT_STEPS = {
    ("draft", "Create the draft release"): '--commit "${RELEASE_COMMIT}"',
    ("finalize", "Check, tag and publish"): "",
    ("smoke-assets", "Smoke test the published assets and images"): '--commit "${RELEASE_COMMIT}"',
    ("smoke-linux", "Smoke test the Linux installation"): '--commit "${RELEASE_COMMIT}"',
    ("smoke-windows", "Smoke test the Windows installation"): "-Commit $env:RELEASE_COMMIT",
}
for (jid, step_name), needle in COMMIT_STEPS.items():
    found = [s for s in steps_of.get(jid, []) if tx(s.get("name")) == step_name]
    check(len(found) == 1, "job %s: the step '%s' is missing" % (jid, step_name))
    for s in found:
        check(tx(mp(s.get("env")).get("RELEASE_COMMIT")) == "${{ needs.guard.outputs.commit }}",
              "job %s, step '%s': RELEASE_COMMIT must be needs.guard.outputs.commit" % (jid, step_name))
        check(needle in (tx(s.get("run")) or ""), "job %s, step '%s': the script must be called with the commit (%s)" % (jid, step_name, needle))

# ---- Windows jobs keep the LF line endings of the scripts (GitHub's Windows images set core.autocrlf=true) -------------------
for jid, steps in steps_of.items():
    if not JOBS[jid][4].startswith("windows-"):
        continue
    first = steps[0] if steps else {}
    first_run = tx(first.get("run")) or ""
    check("git config --global core.autocrlf false" in first_run and "git config --global core.eol lf" in first_run and "shell" not in first,
          "job %s: the first step must set core.autocrlf false and core.eol lf (bash) before the checkout, or Git Bash cannot run the scripts" % jid)
    positions = [i for i, s in enumerate(steps) if (tx(s.get("uses")) or "").startswith("actions/checkout@")]
    check(positions and positions[0] > 0, "job %s: the checkout must come after the line-ending step" % jid)

# ---- Windows jobs use bash and pwsh only -------------------------------------------------------------------------------------
for jid, steps in steps_of.items():
    for s in steps:
        if "shell" in s:
            check(tx(s.get("shell")) == "pwsh" and jid in ("verify-windows", "smoke-windows"), "job %s: only the Windows jobs use shell: pwsh" % jid)

if problems:
    for p in problems:
        print("release-workflow: %s" % p)
    sys.exit(1)
print("release-workflow: %d checks passed" % checks)
PY

GORELEASER_CONFIG="${ROOT}/deploy/release/goreleaser.yaml"
run_checker() { python3 -I "${CHECKER}" "$1" "${CI_DIR}/check-workflows.py" "${2:-${GORELEASER_CONFIG}}"; }

expect_ok "release.yml has the shape of ADR-0035" run_checker "${WORKFLOW}"

# a copy of the workflow with one change; the checker must refuse it with a message that names the change
MUT=0
mutated() {
  local from="$1" to="$2"
  MUT=$((MUT + 1))
  mkdir -p "${TEST_TMP}/m${MUT}"
  MUT_FROM="${from}" MUT_TO="${to}" python3 -I -c '
import os, sys
text = open(sys.argv[1], encoding="utf-8").read()
if os.environ["MUT_FROM"] not in text:
    sys.exit("mutation source not found: " + os.environ["MUT_FROM"])
open(sys.argv[2], "w", encoding="utf-8").write(text.replace(os.environ["MUT_FROM"], os.environ["MUT_TO"], 1))
' "${WORKFLOW}" "${TEST_TMP}/m${MUT}/release.yml" || return 1
  run_checker "${TEST_TMP}/m${MUT}/release.yml"
}

expect_fail "an expression in a run text is refused" 'a \$\{\{ \}\} expression inside the run text' \
  mutated 'run: ci/release/meta.sh >> "${GITHUB_OUTPUT}"' 'run: ci/release/meta.sh "${{ github.sha }}" >> "${GITHUB_OUTPUT}"'
expect_fail "a write to GITHUB_ENV is refused" 'GITHUB_ENV or GITHUB_PATH' \
  mutated 'run: ci/release/meta.sh >> "${GITHUB_OUTPUT}"' 'run: ci/release/meta.sh >> "${GITHUB_ENV}"'
expect_fail "a third environment user is refused" 'exactly sign-windows and finalize|job smoke-assets: environment' \
  mutated $'    name: smoke-assets\n' $'    name: smoke-assets\n    environment: release\n'
expect_fail "a missing sign-windows in the needs of package is refused" 'job package: needs' \
  mutated 'needs: [guard, build-tools, ui, build-windows, sign-windows]' 'needs: [guard, build-tools, ui, build-windows]'
expect_fail "a wider permission is refused" 'job sign-blobs: permissions' \
  mutated $'      attestations: write # store the attestations in GitHub\'s attestation API\n    outputs:' \
          $'      attestations: write # store the attestations in GitHub\'s attestation API\n      packages: write # x\n    outputs:'
expect_fail "a permission without a comment is refused" "has no comment" \
  mutated 'checks: read # the tag guard reads the check run ci-ok of the tagged commit' 'checks: read'
expect_fail "another trigger is refused" 'the only trigger is push' \
  mutated $'    tags: [\'v*\']\n' $'    tags: [\'v*\']\n  workflow_dispatch: {}\n'
expect_fail "a branch trigger pattern is refused" 'the tag pattern must be exactly v' \
  mutated "tags: ['v*']" "tags: ['v*', 'release-*']"
expect_fail "an upload with compression is refused" 'an upload needs archive: false' \
  mutated 'archive: false' 'archive: true'
expect_fail "a longer retention is refused" 'retention-days: 1' \
  mutated 'retention-days: 1' 'retention-days: 7'
expect_fail "a download by name is refused" 'download by artifact-ids' \
  mutated 'artifact-ids: ${{ needs.build-tools.outputs.artifact-id }}' 'name: build-tools'
expect_fail "a download without the digest check is refused" 'digest-mismatch must be error' \
  mutated 'digest-mismatch: error' 'digest-mismatch: warn'
expect_fail "an unpack against another producer is refused" 'same producer' \
  mutated 'TAR_SHA256: ${{ needs.build-tools.outputs.sha256 }}' 'TAR_SHA256: ${{ needs.images.outputs.sha256 }}'
expect_fail "an attestation with a storage record is refused" 'create no storage record' \
  mutated 'create-storage-record: false' 'create-storage-record: true'
expect_fail "an eighth attestation is refused" 'exactly 7 times' \
  mutated $'      - name: Log out of ghcr.io\n        run: ci/release/registry-login.sh logout\n\n  # Creates the draft' $'      - name: Attest once more\n        uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6 # v4.2.2\n        with:\n          subject-checksums: x\n      - name: Log out of ghcr.io\n        run: ci/release/registry-login.sh logout\n\n  # Creates the draft'
expect_fail "a changed pin is refused" 'an action outside the allowed pins' \
  mutated 'actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6 # v4.2.2' 'actions/attest@0000000000000000000000000000000000000000 # v4.2.2'
expect_fail "another action is refused" 'an action outside the allowed pins' \
  mutated 'actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0' 'actions/cache@0000000000000000000000000000000000000000 # v4.2.2'
expect_fail "TOOLS_NO_CACHE in a consumer job is refused" 'TOOLS_NO_CACHE' \
  mutated $'      - name: Sign checksums.txt (keyless)\n        env:\n' $'      - name: Sign checksums.txt (keyless)\n        env:\n          TOOLS_NO_CACHE: \'1\'\n'
expect_fail "DOCKER_CONFIG is refused" 'DOCKER_CONFIG' \
  mutated $'      - name: Log in to ghcr.io\n        env:\n' $'      - name: Log in to ghcr.io\n        env:\n          DOCKER_CONFIG: /tmp/x\n'
expect_fail "a cache is refused" 'cache' \
  mutated 'cache: false' 'cache: true'
expect_fail "the wrong image order is refused" 'push-images: the steps must run in the order' \
  mutated 'subject-digest: ${{ needs.images.outputs.server-amd64 }}' 'subject-digest: ${{ needs.images.outputs.server-arm64 }}'
expect_fail "another image repository is refused" 'image repositories in the file' \
  mutated 'subject-name: ghcr.io/myriarch/myriarch-updater' 'subject-name: ghcr.io/example/other'
expect_fail "a changed timeout is refused" 'job finalize: timeout-minutes' \
  mutated $'    timeout-minutes: 20\n    environment: release\n' $'    timeout-minutes: 25\n    environment: release\n'
expect_fail "a changed runner is refused" 'job verify-windows: runs-on' \
  mutated 'runs-on: windows-2025' 'runs-on: windows-latest'
expect_fail "an if: on a job other than guard is refused" 'only guard has an if' \
  mutated $'    name: signing-tools\n' $'    name: signing-tools\n    if: always()\n'
expect_fail "continue-on-error on a step is refused" 'no if: and no continue-on-error' \
  mutated $'      - name: Run the tag guard\n' $'      - name: Run the tag guard\n        continue-on-error: true\n'
expect_fail "github.sha outside guard is refused" 'only guard does' \
  mutated 'RELEASE_COMMIT: ${{ needs.guard.outputs.commit }}' 'RELEASE_COMMIT: ${{ github.sha }}'
expect_fail "the token in another key is refused" 'the token goes only into' \
  mutated 'REGISTRY_TOKEN: ${{ github.token }}' 'OTHER_TOKEN: ${{ github.token }}'
expect_fail "a secret is refused" 'reads no secret' \
  mutated $'      - name: Run the tag guard\n        env:\n' $'      - name: Run the tag guard\n        env:\n          X: ${{ secrets.X }}\n'
expect_fail "a Windows job without the line-ending step is refused" 'job verify-windows: the first step must set core.autocrlf false' \
  mutated 'git config --global core.autocrlf false' 'git config --global core.autocrlf true'
expect_fail "finalize without RELEASE_COMMIT is refused" "job finalize, step 'Check, tag and publish': RELEASE_COMMIT" \
  mutated $'          RELEASE_COMMIT: ${{ needs.guard.outputs.commit }}\n          RELEASE_ID:' $'          RELEASE_ID:'
expect_fail "a smoke test without the commit is refused" "job smoke-linux, step 'Smoke test the Linux installation': the script must be called with the commit" \
  mutated 'smoke.sh linux --tag "${RELEASE_TAG}" --commit "${RELEASE_COMMIT}" --arch' 'smoke.sh linux --tag "${RELEASE_TAG}" --arch'
expect_fail "a Windows smoke test without the commit is refused" "job smoke-windows, step 'Smoke test the Windows installation': the script must be called with the commit" \
  mutated ' -Commit $env:RELEASE_COMMIT' ''
expect_fail "the build tools in a privileged job are refused" 'job sign-blobs: needs is' \
  mutated 'needs: [guard, signing-tools, package, sign-windows]' 'needs: [guard, signing-tools, build-tools, package, sign-windows]'
expect_fail "a consumer without the check of the tool set is refused" "job images, step 'Download signing-tools': the unpack of signing-tools must be followed by tools.sh check --set signing" \
  mutated $'          ci/release/tools.sh check --set signing --dir "${RUNNER_TEMP}/signing-tools" --platform linux-amd64\n' ''
expect_fail "an extra step in the signing-tools job is refused" 'job signing-tools: the steps must be' \
  mutated $'      - name: Pack signing-tools\n' $'      - name: Install more\n        run: ci/install-tools.sh yq\n      - name: Pack signing-tools\n'
# ---- tool resolution: one change each ---------------------------------------------------------------------------------------------
# a copy of goreleaser.yaml with one change; the workflow stays as it is
mutated_gr() {
  local from="$1" to="$2"
  MUT=$((MUT + 1))
  mkdir -p "${TEST_TMP}/m${MUT}"
  MUT_FROM="${from}" MUT_TO="${to}" python3 -I -c '
import os, sys
text = open(sys.argv[1], encoding="utf-8").read()
if os.environ["MUT_FROM"] not in text:
    sys.exit("mutation source not found: " + os.environ["MUT_FROM"])
open(sys.argv[2], "w", encoding="utf-8").write(text.replace(os.environ["MUT_FROM"], os.environ["MUT_TO"]))
' "${GORELEASER_CONFIG}" "${TEST_TMP}/m${MUT}/goreleaser.yaml" || return 1
  run_checker "${WORKFLOW}" "${TEST_TMP}/m${MUT}/goreleaser.yaml"
}
expect_fail "an sboms entry without the TOOLS_BIN env is refused (the failure of the first release candidate)" "sboms entry 'archives': env must be exactly TOOLS_BIN" \
  mutated_gr $'    env: ["TOOLS_BIN={{ .Env.TOOLS_BIN }}"]\n    args: ["../deploy/release/sbom.sh", "archive"' $'    args: ["../deploy/release/sbom.sh", "archive"'
expect_fail "an sboms entry with another env is refused" "sboms entry 'webui': env must be exactly TOOLS_BIN" \
  mutated_gr $'env: ["TOOLS_BIN={{ .Env.TOOLS_BIN }}"]\n    args: ["../deploy/release/sbom.sh", "webui"' $'env: ["TOOLS_BIN=/usr/bin"]\n    args: ["../deploy/release/sbom.sh", "webui"'
expect_fail "the package step without TOOLS_BIN is refused" "job package, step 'Build, package and check \\(stage apply\\)': TOOLS_BIN must be" \
  mutated $'      - name: Build, package and check (stage apply)\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/tools/linux-amd64\n' $'      - name: Build, package and check (stage apply)\n        env:\n'
expect_fail "the build-windows step without TOOLS_BIN is refused" "job build-windows, step 'Build the Windows targets \\(stage collect\\)': TOOLS_BIN must be" \
  mutated $'      - name: Build the Windows targets (stage collect)\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/tools/linux-amd64\n' $'      - name: Build the Windows targets (stage collect)\n        env:\n'
expect_fail "TOOLS_BIN in the wrong directory is refused" "job package, step 'Build, package and check \\(stage apply\\)': TOOLS_BIN points into signing-tools, which this job does not unpack" \
  mutated $'      - name: Build, package and check (stage apply)\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/tools/linux-amd64\n' $'      - name: Build, package and check (stage apply)\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/signing-tools/linux-amd64\n'
expect_fail "the signing set for goreleaser is refused" "job images, step 'Build the image layouts': the signing set of linux-amd64 lacks" \
  mutated $'        id: build\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/tools/linux-amd64\n' $'        id: build\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/signing-tools/linux-amd64\n'
expect_fail "the arm64 row of smoke-linux with the amd64 tools is refused" "job smoke-linux, step 'Smoke test the Linux installation': TOOLS_BIN must follow the matrix" \
  mutated 'TOOLS_BIN: ${{ runner.temp }}/tools/${{ matrix.tools }}
          RELEASE_TAG: ${{ needs.guard.outputs.tag }}
          RELEASE_VERSION: ${{ needs.guard.outputs.version }}
          RELEASE_COMMIT: ${{ needs.guard.outputs.commit }}
          ARCH:' 'TOOLS_BIN: ${{ runner.temp }}/tools/linux-amd64
          RELEASE_TAG: ${{ needs.guard.outputs.tag }}
          RELEASE_VERSION: ${{ needs.guard.outputs.version }}
          RELEASE_COMMIT: ${{ needs.guard.outputs.commit }}
          ARCH:'
expect_fail "a matrix row with a platform that the job did not unpack is refused" "job smoke-linux, step 'Smoke test the Linux installation': TOOLS_BIN names the platform" \
  mutated $'          ci/release/tools.sh check --set signing --dir "${RUNNER_TEMP}/tools" --platform "${TOOLS_PLATFORM}"\n      - name: Smoke test the Linux installation' $'          ci/release/tools.sh check --set signing --dir "${RUNNER_TEMP}/tools" --platform linux-amd64\n      - name: Smoke test the Linux installation'
expect_fail "the image build without CRANE is refused" "job images, step 'Build the image layouts': CRANE must point into a tool set" \
  mutated $'          CRANE: ${{ runner.temp }}/signing-tools/linux-amd64/crane\n          RELEASE_VERSION' $'          RELEASE_VERSION'
expect_fail "a push without CRANE is refused" "job push-images, step 'Push the server image by digest': CRANE must point into a tool set" \
  mutated $'          CRANE: ${{ runner.temp }}/tools/linux-amd64/crane\n          IMAGE_DIGEST: ${{ needs.images.outputs.server-index }}' $'          IMAGE_DIGEST: ${{ needs.images.outputs.server-index }}'
expect_fail "a pinned tool called by its bare name is refused" "the pinned tool cosign is called by its bare name" \
  mutated 'run: ci/release/registry-login.sh logout
' 'run: cosign version
'
expect_fail "a pinned tool after a pipe by its bare name is refused" "the pinned tool gh is called by its bare name" \
  mutated 'run: ci/release/meta.sh >> "${GITHUB_OUTPUT}"' 'run: ci/release/meta.sh | gh version'
expect_fail "smoke-kind with its own TOOLS_BIN is refused" "smoke-kind: no step sets TOOLS_BIN" \
  mutated $'      - name: Smoke test the chart on kind\n        env:\n' $'      - name: Smoke test the chart on kind\n        env:\n          TOOLS_BIN: ${{ runner.temp }}/tools\n'
expect_fail "a renamed workflow file is refused" 'must be named release.yml' \
  bash -c 'mkdir -p "$1/renamed" && cp "$2" "$1/renamed/publish.yml" && python3 -I "$3" "$1/renamed/publish.yml" "$4" "$5"' _ "${TEST_TMP}" "${WORKFLOW}" "${CHECKER}" "${CI_DIR}/check-workflows.py" "${GORELEASER_CONFIG}"

finish_tests "release-workflow-test"
