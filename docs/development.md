---
title: "Development"
weight: 100
description: "How to build, test and extend myriarch: toolchain, repository layout, build tags, the local gates, the web UI, end-to-end tests and the documentation tooling."
---

This guide is for people who change the code, the web UI, the scripts or the documentation. To run the system once, read
[Local development](getting-started/local-dev.md) first. Contribution rules (sign-off, pull requests, AI-assisted work) are in
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Repository layout

```text
api/openapi.yaml          REST contract (OpenAPI 3.1)
assets/brand/             logo files and the brand guide
cmd/                      myriarchd, myrctl, myriarch-updater, myriarch-sim
internal/                 all Go packages (see below)
web/                      web UI: React, TypeScript, Vite
deploy/                   Helm chart, Docker Compose, Kubernetes manifests, installers, systemd units, supervisor configs
examples/quickstart/      a complete example GitOps repository
examples/sim/             simulator profiles and the component list
gitops-template/          template for your own GitOps repository (with CI)
hack/kind/                Kubernetes probe on a local kind cluster
scripts/                  end-to-end tests, image build scripts, the language guard
ci/                       gate scripts, pinned tool versions (ci/versions.env), script tests
contrib/                  optional operator tools (for example the offline mirror helpers)
docs/                     documentation, design contracts and decision records
website/                  project website (Hugo + Docsy): sources, checks and the build of myriarch.dev
```

The Go packages under `internal/` fall into these groups:

| Group | Packages |
|---|---|
| Domain model and interfaces | `domain`, `store`, `eventbus`, `leader`, `control`, `executor`, `notify`, `authz`, `api`, `audit`, `config` |
| Server surfaces | `opamp`, `authn`, `httpserver`, `coreapi`, `fleetapi`, `live`, `metrics`, `webui` (the embedded UI, build tag `embedui`) |
| Persistence and high availability | `store/postgres`, `eventbus/pgbus`, `leader/pglock`, `housekeeping` (retention jobs on the leader) |
| GitOps, rendering and rollouts | `gitops`, `blueprint`, `assignment`, `rollout`, `executor/*`, `release`, `artifact`, `myrctl` |
| Host agent and simulator | `updater`, `sim` |
| Cross-cutting helpers | `brand` (all product identifiers), `configload`, `netguard`, `secretfile`, `version`, `lru`, `actorname` |
| Test support | `testutil` (embedded PostgreSQL helper), `testdata` |

Product identifiers (binary names, cookie names, token prefixes, attribute keys) are defined once in `internal/brand`.

## Toolchain

| Tool | Version |
|---|---|
| Go | the version in `go.mod`; newer releases work, CI uses exactly that version |
| Node.js | the LTS release in `.node-version` (CI uses exactly that version); at least 22.22.2 or 24.15 (`engines` in `web/package.json`; the website needs 22 or newer) |
| pnpm | the version in the `packageManager` field of `web/package.json` and `website/package.json` (`corepack enable` provides it) |
| `make`, `curl`, `jq`, `python3`, `git` | any recent version |

No container runtime is needed to build, test or run the system locally (see
[ADR-0011](adr/0011-no-containers-for-local-development-and-tests.md)). Only the optional Kubernetes probe needs Docker.

## Build tags

| Tag | Effect |
|---|---|
| `embedui` | Embeds the built web UI (`internal/webui/dist`) into `myriarchd`. Without it, `myriarchd` serves a placeholder page. |
| `devpg` | Links the embedded PostgreSQL (`internal/devpg`, based on `github.com/fergusstrange/embedded-postgres`) so that `myriarchd dev` can start its own database. |

The Makefile builds the tag lists as comma-separated values:

| Variable | Value | Used by |
|---|---|---|
| `UI_TAGS` | `embedui` if `internal/webui/dist/index.html` exists, otherwise empty | building block of the others |
| `DEV_TAGS` | `UI_TAGS` + `devpg` | `make build`, `make dev`, `make e2e` (through `build`) |
| `RELEASE_TAGS` | `UI_TAGS` | `make release` |

Further places:

- `make test` and `make test-race` run `go test ./...` without tags (this covers the `devpg` stub) and additionally
  `go test -tags devpg ./internal/devpg/...` (the embedded PostgreSQL implementation).
- `scripts/build-image-crane.sh` (`make image-crane`) builds `myriarchd` with `embedui,devpg` by default
  (`IMAGE_VARIANT=dev`). This is the dev and simulator image, and `myriarchd dev` runs there. `IMAGE_VARIANT=release`
  builds `myriarchd` with `embedui` only and leaves out `/opt/myriarch/release-fixtures`, so the image holds no OpenTelemetry
  release fixtures. Both variants contain `LICENSE` and `THIRD_PARTY_NOTICES` under `/usr/share/licenses/myriarch/`. The
  subcommand `scripts/build-image-crane.sh layout` prints the archive paths of the data and license entries of the chosen
  variant without a registry, network or build. The crane scripts need `IMAGE_REPO` (there is no default registry) and
  respect `DOCKER_CONFIG` without setting it. These builds run as user and group 65532 and never push to `ghcr.io/myriarch/*`.
  The published images are built from the release archives instead: `make release-snapshot`, then
  `ALLOW_DIRTY=1 IMAGE_SOURCE=dist IMAGE_KIND=server scripts/build-image-crane.sh` (and `IMAGE_KIND=updater`) writes the OCI
  layouts and SBOMs below `.cache/image-dist/`; `ci/release/push-images.sh` pushes a layout by digest. The server image holds
  `myriarchd`, `myrctl`, `myriarch-sim`, the example repositories and the data directory `/var/lib/myriarchd` (owned by
  65532:65532); all of it is read from the release archives. The image contract, the
  Dockerfile build contexts and the image license paths are described in [deploy/README.md](../deploy/README.md).
- `deploy/release/goreleaser.yaml` builds `myriarchd` with `embedui` only.

### Release builds omit `devpg`

Release binaries must not link `embedded-postgres` or its dependency `github.com/xi2/xz`. `embedded-postgres` is MIT
licensed, but `xi2/xz` is public domain (not on the license allowlist), and the PostgreSQL binaries are downloaded at
runtime (see below), which is not wanted in a release artifact. `ci/check-release-deps.sh` verifies this with `go list -deps`
for linux, windows and darwin and fails if one of the forbidden modules is linked. The same script checks that the UI
bundle contains no font files and that `web/package.json` has no `@fontsource` dependency (the UI uses system font
stacks). `make check-local` runs it as part of the `go` gate, and the aggregate CI job `ci-ok`, which the release guard requires on the tagged commit,
covers it, so a released commit has passed it. The release build itself (`ci/build-release.sh`, goreleaser) does not run it again; the release
workflow has to run it, `ci/check-release-licenses.sh` and `scripts/gen-third-party-notices.sh --check` before it packages
(`deploy/release/README.md`, section on the release workflow).

### License check of the release builds

`ci/check-release-licenses.sh` runs `go-licenses` (pinned in `ci/versions.env`, installed with
`ci/install-tools.sh go-licenses`) over the release builds of all four commands for linux, windows and darwin, without the
`devpg` tag. It fails when a linked package has a license outside the allowlist (Apache-2.0, BSD-2-Clause, BSD-3-Clause,
ISC, MIT, Zlib). `make check-local` runs it as part of the `go` gate. Documented exceptions live in
`ci/license-overrides.txt`, which the notices generator reads too (see
[Licenses, headers and third-party notices](#licenses-headers-and-third-party-notices)). The entries are:

- `github.com/in-toto/attestation`, `github.com/in-toto/in-toto-golang` and
  `github.com/cyberphone/json-canonicalization`: `go-licenses` cannot classify their `LICENSE` files; all three contain the
  short Apache License 2.0 notice (copyright line and boilerplate), not the license text. The full text is in the `LICENSE` of
  the project.
- `github.com/cyphar/filepath-securejoin` (linked through go-git into `myriarchd` and `myrctl`): the module is
  `BSD-3-Clause AND MPL-2.0` (`COPYING.md`), so `go-licenses` reports both. The only linked packages are the root package
  and `internal/consts`, which are BSD-3-Clause (`LICENSE.BSD`). The entry lists exactly these two packages as its scope and fails
  closed: `go-licenses` names the library after the module root for every package, so when a dependency update starts to link
  another package of the module (for example `pathrs-lite`, which is MPL-2.0), the notices generator and
  `ci/check-release-licenses.sh` fail until the package is reviewed.

`go-licenses` is built without `-trimpath`: with it the tool finds no packages and exits with status 0. The script fails
on an empty report for that reason.

### `myriarchd dev` without the `devpg` tag

A `myriarchd` built without `devpg` cannot start an embedded database. `myriarchd dev` then fails with an error that points to
`--dsn`:

```text
start embedded postgres: embedded PostgreSQL is not built in: this myriarchd was built without embedded PostgreSQL
(build tag devpg): start `myriarchd dev --dsn postgres://...` against an external PostgreSQL, or rebuild with -tags devpg
(make build / make dev do this)
```

Give it an external PostgreSQL and it works as before:

```bash
myriarchd dev --dsn 'postgres://user:password@127.0.0.1:5432/myriarch?sslmode=disable'
```

This is how the container setups run `myriarchd dev` (Docker Compose, Kubernetes deployments with a separate PostgreSQL):
they use their own PostgreSQL and need no build tag.

### Runtime download of the PostgreSQL binaries

The PostgreSQL binaries are not part of the `myriarchd` binary. With the `devpg` tag, the first `myriarchd dev` start (and the
first `go test -tags devpg ./internal/devpg/...`) downloads them through `embedded-postgres` from Maven Central and
extracts them into the user cache directory:

```text
<user cache dir>/myriarch/embedded-postgres/pg-<version>-<os>-<arch>
```

`PGTEST_CACHE_DIR` overrides the cache directory (shared with the test helper `internal/testutil/pgtest`). The first start
therefore needs network access; later starts work offline. The data directory is `<data-dir>/postgres/data` (default
`.dev/postgres/data`) and survives restarts.

## Local tools

`make tools` (`ci/install-tools.sh`) puts every tool the gates need into `.cache/bin`. `TOOLS="helm shellcheck"` limits the
set. The Go tools are built from the pinned modules in `ci/tools/`. A stamp file next to each binary names the version and a
digest of the `go.mod` and `go.sum` of its module, so a new pin or a raised requirement rebuilds the tool, also from a tool cache
that CI restored from an older run; `TOOLS_NO_CACHE=1` ignores the stamps and installs everything again. shellcheck and PowerShell 7
(`pwsh`) are release downloads, verified against the SHA-256 values in `ci/versions.env`; `pwsh` is unpacked into
`.cache/pwsh/<version>` and linked as `.cache/bin/pwsh`. Put `.cache/bin` on your `PATH` to use the pinned versions:

```bash
make tools
export PATH="$PWD/.cache/bin:$PATH"
```

kind (the Kubernetes-in-Docker tool of the probe in `hack/kind`) is built like the other Go tools. kubectl is a release
download from `dl.k8s.io`, verified against the `KUBECTL_SHA256_*` values in `ci/versions.env`: its minor version has to
be within the supported skew of the kind node image, which is why the kubectl of a container desktop is not used.

The website tools are release downloads as well, verified against the SHA-256 values in `ci/versions.env`: Hugo extended
(`hugo`, unpacked from the `.pkg` on macOS and linked from `.cache/hugo/<version>`), Dart Sass (`sass`, in
`.cache/dart-sass/<version>`; Hugo compiles the theme's SCSS with it) and lychee (`lychee`, the link checker). They are
part of the default `TOOLS` and installed on demand by `ci/website.sh` (see [Website](#website)).

actionlint (the linter of the GitHub Actions workflows) is built like the other Go tools; its ShellCheck integration uses the pinned
shellcheck. It belongs to the `workflows` gate.

The release tooling and the workflow analyser are pinned the same way, so that a release does not depend on whatever a runner
ships:

- `gh` (the GitHub CLI) and `go-winres` (Windows version information and manifest resources, built from the `tc-hib/go-winres`
  module) are Go tools built from `ci/tools/<tool>/`.
- `syft` (SBOM generation) and `zizmor` (static analysis of GitHub Actions workflows) are release downloads like shellcheck,
  verified against the `SYFT_SHA256_*` and `ZIZMOR_SHA256_*` values in `ci/versions.env`. Both publish binaries for macOS and for
  Linux, for arm64 and amd64 (zizmor only with glibc, `unknown-linux-gnu`; there is no musl build). syft is not built from source:
  only the upstream release binaries carry their version, which syft records in every SBOM it makes (a `go build` reports
  "[not provided]"), and a Go module for it would bring its whole dependency graph into the repository. The `workflows` gate runs zizmor
  ([Security rules of the workflows](#security-rules-of-the-workflows)).
  The SBOM jobs run syft with `SYFT_CHECK_FOR_APP_UPDATE=false`, so that it does not look for a newer release.
- `osslsigncode` (signs and verifies the Authenticode signatures of the Windows release files) is built from the source archive of
  the pinned tag, verified against `OSSLSIGNCODE_SOURCE_SHA256` in `ci/versions.env` (`ci/install-tools.sh osslsigncode`). It is a C
  program, upstream publishes no Linux binary, and the build needs no cmake: one `cc` call over the C files and a minimal
  `config.h`. It is not part of the default `TOOLS`, because it needs a C compiler, the OpenSSL 3 headers and zlib (Debian and
  Ubuntu: `libssl-dev` and `zlib1g-dev`, for example `ci/install-tools.sh apt:libssl-dev apt:zlib1g-dev`; macOS: the Xcode command
  line tools and Homebrew `openssl@3`, or `OSSLSIGNCODE_OPENSSL_PREFIX`). `ci/release/rehearsal.sh` and the CI jobs `build` and `scripts` install it on demand.
  A distribution package is never used, because Ubuntu 24.04 ships 2.8, which predates the verification fixes of 2.12 and 2.13, and
  the release build runs it on files that a signing provider returns.

`ci/tools/update.sh gh go-winres` regenerates the modules after a version change. It also raises dependencies for which
`govulncheck -mode=binary -show verbose .cache/bin/<tool>` lists a fixed version (see `raised()` in the script: `golang.org/x/net`
in `gh`, `golang.org/x/image` in `go-winres`). A raise never lowers a module, and an entry that the tool itself has overtaken is
reported as obsolete. Findings in the standard library are fixed by the Go toolchain, and a finding without a fixed version cannot
be raised. `make vulncheck` itself does not scan `ci/tools`. `ci/check.sh` verifies that `go.mod` and `ci/versions.env` agree and
that the syft and zizmor pins are SHA-256 values. `ci/tests/tool-downloads-test.sh` covers the refusal of a syft or zizmor archive
whose checksum differs from the pin, and of a binary that reports another version.

PowerShell is only needed for the Windows installer tests: the `pwsh` cases in `internal/updater` and
`ci/tests/installer-verify.ps1`. Without it these tests are skipped and the report says so. `MYRIARCH_PWSH` points the Go
tests at a different `pwsh` binary.

## Gates

Before you open a pull request, run the checks that CI runs (see [Continuous integration](#continuous-integration)). The quick version for a code change:

```bash
go build ./... && go vet ./... && test -z "$(gofmt -l cmd internal)"
(ulimit -n 10240; go test -race -count=1 ./...)   # database tests use an embedded PostgreSQL, no Docker
cd web && pnpm install --frozen-lockfile && pnpm gen:api && pnpm typecheck && pnpm lint && pnpm test && pnpm build
cd .. && make build && make validate-examples
make docs-check                                   # when you touched documentation or flags
```

`make check-local` (`ci/check-local.sh`) runs every gate in one go, each in its own subshell with one log per gate:

| Gate | What it runs |
|---|---|
| `go` | `go build ./...` with and without the `devpg` tag, vet for Windows and macOS, `ci/lint-go.sh` (go mod verify, vet, gofmt), the version check (`ci/check-versions.sh`: `VERSION` and its copies agree, see [Releasing](releasing.md#one-version-source)), the release dependency check and the license check |
| `go-test` | `go test -race -count=1 ./...`, the tagged tests of `internal/devpg` and the render budget test of `internal/blueprint` in a run of its own without `-race` (`GO_TEST_RACE=0` drops `-race`, `GO_TEST_DEVPG=0` leaves out the `devpg` tests, `GO_TEST_PERF=0` leaves out the budget test) |
| `web` | `pnpm install --frozen-lockfile`, a check that `web/src/api/schema.d.ts` matches `pnpm gen:api`, typecheck, lint, the formatting check (`pnpm format:check`; `pnpm format` fixes it), tests and the production build |
| `build` | `make build` |
| `examples` | `make validate-examples` (`myrctl validate --strict` on `examples/quickstart`, `gitops-template/config` and `hack/kind/gitops`) |
| `shellcheck` | `ci/lint-shell.sh` over all shell scripts we ship or run |
| `workflows` | `ci/lint-workflows.sh`: `actionlint` over `.github/workflows`, `zizmor` over the workflows, the local action and `.github/dependabot.yml` (offline; `ZIZMOR_ONLINE=1` with `GH_TOKEN` adds the audits that ask the GitHub API, which the CI job does), and the policy check `ci/check-workflows.py` (pinned actions, read-only token, forbidden triggers, secrets only for listed deployment jobs, a timeout per job, parity with the gates, the shape of the Scorecard and release workflows; see [Continuous integration](#continuous-integration)) |
| `helm` | `ci/lint-helm.sh` and `ci/helm-conform.sh` (renders the chart with the example values and validates it against the schemas of Kubernetes 1.30.0, the chart floor, and 1.34.12) |
| `language` | the language guard for English (see below) |
| `docs` | `make docs-check`: generated CLI reference up to date, links, front matter |
| `website` | `ci/website.sh check`: the production build of the website with `--panicOnWarning`, `hugo mod verify`, lychee over the built pages (offline, with fragments), markdownlint, the site front matter, the checks of `website/scripts` and their unit tests (see [Website](#website)). The first run needs network access for the Go proxy, the npm registry and the tool downloads |
| `licenses` | the SPDX header check (`scripts/license-headers.py --check`), `ci/reuse-lint.sh` (`reuse lint`) and the drift check of `THIRD_PARTY_NOTICES` (`scripts/gen-third-party-notices.sh --check`) |
| `vulncheck` | `ci/vulncheck.sh`: `govulncheck` over the release and the `devpg` build; a vulnerability that the code calls fails the gate unless `ci/vulncheck-allowlist.txt` accepts it (ID, review date, reason). Module-level findings are listed. It reads the vulnerability database over the network: offline use `SKIP=vulncheck` |
| `secrets` | `ci/secrets-scan.sh`: `gitleaks` (configuration `.gitleaks.toml`, default rules plus allowlists limited to one file and one rule) over an export of `HEAD`, findings redacted. `SECRETS_SCAN_WORKTREE=1 ci/secrets-scan.sh` scans the work tree instead. Before publication scan the history once: `gitleaks git --redact -c .gitleaks.toml` |
| `e2e` | `scripts/e2e.sh` |

The `go` gate also vets the code for Windows and macOS (`GOOS=windows go vet ./...`, `GOOS=darwin GOARCH=arm64 go vet ./...`) and
compiles the tests of `internal/updater` for Windows, because the updater runs as SYSTEM there.

The gates run in the order `go go-test web build examples shellcheck workflows helm language docs website licenses vulncheck secrets e2e`. `ci/check-local.sh --list` prints them.

| Variable | Effect |
|---|---|
| `SKIP=e2e,go-test` | skip these gates |
| `ONLY=web,helm` | run only these gates (applied first, then `SKIP` removes) |
| `LANGUAGE_GUARD=strict\|warn\|off` | strict (default): a finding fails the gate; `warn`: the gate only warns; `off`: the gate is skipped |
| `LANGUAGE_GUARD_PATHS="..."` | space-separated pathspecs of the strict scope |
| `GO_TEST_RACE=0\|1` | `go-test`: `-race` on (default) or off |
| `GO_TEST_DEVPG=0\|1` | `go-test`: run the tagged `devpg` tests (default) or leave them out |
| `GO_TEST_PERF=0\|1` | `go-test`: run the render budget test alone and without `-race` (default) or leave it out; see [Render budget](#render-budget) |
| `HELM_KUBERNETES_VERSIONS="1.30.0 1.34.12"` | `helm`: Kubernetes versions of the schemas that the rendered manifests are validated against |
| `CHECK_LOCAL_STREAM=0\|1` | print every gate to the console in a `::group::` block as well as to its log (default 1 in GitHub Actions, 0 elsewhere) |
| `E2E_*` | passed through to `scripts/e2e.sh` |

```bash
make check-local                          # everything, including the end-to-end test (about 10 minutes or more)
make check-local SKIP=e2e                 # without the end-to-end test
make check-local ONLY=language,docs       # only the documentation checks
make check-local ONLY=website             # only the website build and its checks
```

Logs are written to `.cache/check-local/<gate>.log` and the summary to `.cache/check-local/summary.txt`. A missing tool is
reported with the hint to run `make tools`. The runner always continues after a failed gate and prints a table at the end;
the exit code is 1 if any gate failed.

### Render budget

The server renders a fleet of 5,000 agents in under 5 seconds (acceptance criterion). `TestRenderFleet5000UnderFiveSeconds` in
`internal/blueprint` always renders the 5,000 agents and checks the results (no errors, different configurations for different
agents), but it holds the duration against the budget only with `PERFTEST=1`. Inside `go test ./...` other packages run in
parallel, so the wall clock measures the machine as much as the renderer, and the race detector slows the renderer down by 5 to
10 times. The `go-test` gate therefore runs the test a second time, alone and without `-race`:

```bash
PERFTEST=1 go test -count=1 -v -run '^TestRenderFleet5000UnderFiveSeconds$' ./internal/blueprint
```

With `PERFTEST=1` the test renders the fleet three times, logs every duration and holds the median against the budget. A shared
CI runner stalls now and then; one stall does not move the median, while a slower renderer is slower in every render. The
`-v` log of a green run shows how much margin the budget has.

`GO_TEST_PERF=0` leaves that run out; the Windows job does, because the budget belongs to the server, which does not ship for
Windows. In a `-race` build the test skips the budget check even with `PERFTEST=1` and says so.

### Language guard

English is the language of the project ([ADR-0020](adr/0020-english-as-project-language.md)).
`scripts/check-language.sh` reports lines with German umlauts or with several German stop words in tracked text files.
It prints only the location and the category, never the line. A line that contains `check-language: ignore` is skipped.
Default excludes (translation catalogs, test fixtures, mock data of the web UI) are listed in
`scripts/check-language.exclude`.

The strict scope of `make check-local` is: `cmd internal scripts ci contrib deploy examples hack web/src docs website .github` and
the root files `README.md CHANGELOG.md CONTRIBUTING.md CODE_OF_CONDUCT.md SECURITY.md MAINTAINERS.md GOVERNANCE.md
CONTRIBUTOR_LADDER.md ADOPTERS.md ROADMAP.md AGENTS.md CLAUDE.md`. To check a path yourself:

```bash
scripts/check-language.sh --untracked -- docs README.md
```

## Continuous integration

GitHub Actions runs the gates of `ci/check-local.sh` on every pull request, on every push to `main`, in a merge queue and
nightly ([ADR-0032](adr/0032-continuous-integration-on-github-actions.md)). There are two workflows:

- `.github/workflows/ci.yml` has one job per gate group, the platform matrix of the Go tests, the script tests, the end-to-end
  smoke test (by calling `e2e.yml`) and the aggregate job `ci-ok`.
- `.github/workflows/e2e.yml` runs `scripts/e2e.sh` (the smoke test, called from `ci.yml`), and nightly the load run with
  1,000 simulated collectors and `make e2e-ui-updates`. A manual run (`workflow_dispatch`) picks the mode.

The local composite action `.github/actions/setup` installs Go (from `go.mod`), optionally Node.js (from `.node-version`) and pnpm
(through corepack), restores the caches and installs the pinned tools of `ci/install-tools.sh`.

### Jobs and gates

Every job runs `ci/check-local.sh` with `ONLY=<gates>`, so CI and your machine run the same commands. In GitHub Actions each gate
is a collapsible log group; a failed job uploads `.cache/check-local/` (the gate logs) for seven days.

| Job | Gates and commands | Runner |
|---|---|---|
| `go` | `go` | ubuntu-24.04 |
| `go-test-linux` | `go-test` with `-race` against a PostgreSQL 17 service (`PGTEST_DSN`, `max_connections` 300), then the render budget test | ubuntu-24.04 and ubuntu-24.04-arm |
| `go-test-native` | `go-test`: macOS with `-race`, the embedded PostgreSQL and the render budget test; Windows without `-race`, with `PGTEST_SKIP=1` and without the budget test (`GO_TEST_PERF=0`) | macos-latest, windows-latest |
| `web` | `web` | ubuntu-24.04 |
| `build` | `build`, `examples`, then `make release-rehearsal` (the full release matrix in stage collect, a signing pass with a throwaway test CA, stage apply, dist and reproducibility checks; nothing is published) | ubuntu-24.04 |
| `shell-helm` | `shellcheck`, `helm` | ubuntu-24.04 |
| `workflows` | `workflows` | ubuntu-24.04 |
| `docs` | `language`, `docs` | ubuntu-24.04 |
| `website` | `website` (build and checks, never a deployment) | ubuntu-24.04 |
| `compliance` | `licenses`, `vulncheck`, `secrets`; on pull requests also the commits of the pull request (`SECRETS_SCAN_RANGE=<base>..<head> ci/secrets-scan.sh`), because a secret that one commit adds and a later one removes stays in the history | ubuntu-24.04 |
| `scripts` | `make ci-test` (the script tests under `ci/` and `contrib/`; not a `check-local` gate) | ubuntu-24.04 |
| `e2e` | `e2e` gate: `scripts/e2e.sh` with the real supervisor and the UI smoke test (`e2e.yml`, mode `smoke`) | ubuntu-24.04 |
| `ci-ok` | succeeds only when every job above succeeded | ubuntu-24.04 |

`ci-ok` is the only required check of the branch ruleset (next to the DCO check). Job names can change without touching the ruleset,
and a skipped or cancelled job fails `ci-ok`. The workflows have no path filters, so `ci-ok` always reports. The policy check
fails when a gate of `ci/check-local.sh` has no job in `ci.yml`.

Nightly `ci.yml` (04:41 UTC) runs the full pipeline, including the smoke test, against the current vulnerability database and
upstream downloads. `e2e.yml` has its own schedule (03:17 UTC) for the heavy variants, the load run and the UI version update
test; they do not run inside `ci.yml`, whose call uses the mode `smoke`. GitHub disables scheduled workflows of a public
repository after 60 days without repository activity.

### Reproduce a job locally

```bash
make tools                                  # once: the pinned tools in .cache/bin
make check-local ONLY=web                   # the job "web"
make check-local ONLY=licenses,vulncheck,secrets      # the job "compliance"
GO_TEST_RACE=0 GO_TEST_DEVPG=0 GO_TEST_PERF=0 PGTEST_SKIP=1 make check-local ONLY=go-test    # the Windows variant of "go-test"
make ci-test                                # the job "scripts"
CHECK_LOCAL_STREAM=1 make check-local ONLY=docs       # with the log groups of GitHub Actions
```

For the Linux database tests against an external PostgreSQL see [Database tests against an existing PostgreSQL](#database-tests-against-an-existing-postgresql).
The Windows and macOS runners are the only place where `go test` runs on those systems; `GOOS=windows go vet ./...` and
`GOOS=windows go test -c -o /dev/null ./internal/updater` show compile errors earlier.

### Security rules of the workflows

The rules are enforced by `ci/check-workflows.py` (part of the `workflows` gate) and recorded in
[ADR-0032](adr/0032-continuous-integration-on-github-actions.md). The check reads the workflow files in `.github/workflows`
and the local actions below `.github/actions` (`action.yml`), each with the extension `.yml` or `.yaml` because GitHub reads
both, so a rule does not depend on how a file is named:

- **Only GitHub-owned actions** (`actions/*`, `github/*`), each pinned to a full commit SHA with the release in a comment:
  `uses: actions/checkout@<40 hex> # v7.0.1`. The same action has the same pin everywhere. Everything else comes from the
  repository's own installer (`ci/install-tools.sh`) or corepack. The one exception is `ossf/scorecard-action`
  ([ADR-0033](adr/0033-openssf-scorecard-as-the-only-third-party-action.md)): pinned the same way, and only in
  `.github/workflows/scorecard.yml`, job `analysis` (rule R1).
- **The Scorecard workflow has a fixed shape** (rule R12). `.github/workflows/scorecard.yml` runs on a push to `main` and on a
  schedule, has one job `analysis` on a GitHub-hosted `ubuntu-NN.NN` runner, no `env`, `defaults`, `container`, `services`
  or `environment`, only `uses:` steps (`actions/checkout`, `ossf/scorecard-action` with `publish_results: true`,
  `github/codeql-action/upload-sarif`, optionally `actions/upload-artifact`) and exactly the job permissions `contents: read`,
  `actions: read`, `security-events: write` and `id-token: write`. These are the restrictions of the Scorecard API for
  published results, applied more strictly where the API is looser (for example R12 allows only the keys `name`, `on`,
  `permissions` and `jobs` at the top of the file); the two write scopes need lines in `ci/workflow-permissions.txt`. A change
  to the shape starts with the README of `ossf/scorecard-action` ("Workflow Restrictions") and a change of rule R12.
- **The release workflow keeps build code away from privileges** (rule R13). `.github/workflows/release.yml` runs only for a
  pushed tag `v*` (no `workflow_dispatch`); uses no cache (no `actions/cache`, no `cache:` input, `cache: false` for
  `actions/setup-go`, `package-manager-cache: false` for `actions/setup-node`, no local action or called workflow); names
  only the literal environment `release`; has a job `guard` that every other job needs; and runs `setup-node`, `setup-go`
  and the build commands listed in the docstring of `ci/check-workflows.py` (`pnpm`, `npm`, `node`, `go`, `goreleaser`,
  `make` and similar) only in jobs without a write scope, `id-token`, `attestations` or an environment. Every such
  privileged job has the environment `release` itself or needs a job that has it. No job has an `if:` with `always()`,
  `failure()`, `cancelled()` or `success()` and none uses `continue-on-error`, because either makes `needs:` ineffective. A
  privileged job uses no shell that reads its script from standard input (`bash <<EOF`, `| sh`) and no `shell:` other than
  `bash` or `sh`. The check matches a command at the start of a command, so comments, quoted text, here-documents, `case`
  patterns and arrays that mention `go` or `make` are fine; a program started through a variable, a script or an interpreter
  that is not a shell is out of reach, so the scripts that privileged jobs call are tested with a `PATH` without `go`, `node`
  and `pnpm`.
- **zizmor audits the workflows.** `ci/lint-workflows.sh` runs the pinned `zizmor` (`ci/install-tools.sh zizmor`) between
  `actionlint` and the policy check: template injection, dangerous triggers, excessive permissions, undocumented permissions,
  persisted credentials, cache poisoning, unpinned and impostor actions, secrets outside environments, unsafe writes to
  `GITHUB_ENV` and `GITHUB_PATH`, missing concurrency limits and the cooldown of Dependabot updates. It uses the pedantic
  persona. A permission needs a trailing comment on its own line (`security-events: write # upload the results`), and a workflow
  needs a concurrency group; `scorecard.yml` has none, because rule R12 allows only the keys `name`, `on`, `permissions` and
  `jobs` at its top (stricter than the Scorecard API, which does not check concurrency), and is ignored for that audit in
  `zizmor.yml`. The gate runs offline, so it needs no network and no token. The `workflows` job of `ci.yml` sets
  `ZIZMOR_ONLINE=1` and passes the read-only token of the run as `GH_TOKEN`, which switches on the audits that ask the GitHub
  API about the pinned actions. To run them locally, export a `GH_TOKEN` and `ZIZMOR_ONLINE=1`. A finding fails the gate, and so
  does a file that zizmor cannot parse (`--strict-collection`; by default zizmor skips such a file with a warning). Fix the
  workflow; if the finding is a false positive, ignore it as narrowly as possible, with the reason next to the ignore: a
  trailing comment `# zizmor: ignore[<audit>]` on the line the finding names (it must be the line of the `run:` key for a
  script), with a comment line above it or text behind the bracket that gives the reason, or an entry under
  `rules.<audit>.ignore` in `zizmor.yml` (audit and workflow file, a line number where the finding is about one place) under a
  comment that gives the reason. An entry is the base name of the file (`ci.yml`), because zizmor compares nothing else: a
  directory never matches, and `action.yml` would match every local action, so a finding in a local action is ignored with a
  comment in the file. Rule R14 of the policy check enforces this: `zizmor.yml` has nothing but ignore entries (no `disable`, no
  `config`, no other key, no wildcard, no directory), written as a block with one audit per line, each under a comment line
  whose reason has at least one word; an inline ignore needs a reason with a word as well, and a bare `# zizmor: ignore` fails.
  R14 also makes sure that the policy check and zizmor read the same lines: a file that zizmor reads (`zizmor.yml`, the
  workflows, the local action, `.github/dependabot.yml`) fails when it is not UTF-8 or holds U+0085, U+2028 or U+2029 (a YAML
  parser takes them for line breaks, the check splits at LF and CR), a control character other than TAB, or a byte order mark.
  The environment variables `ZIZMOR_CONFIG`, `ZIZMOR_OFFLINE`, `ZIZMOR_NO_ONLINE_AUDITS` and `GH_HOST` are ignored by the
  script, which passes `--gh-hostname github.com` (the online audits and the token go to github.com and nowhere else).
  `ci/tests/lint-workflows-test.sh` checks with the real zizmor that a finding fails the gate, also one that only the pedantic
  persona reports, and that an ignore covers only its audit and file; with a stub it checks which options and environment
  variables reach zizmor.
- **CodeQL scans the code** (`.github/workflows/codeql.yml`, results in the code scanning alerts of the repository). One matrix
  job `analyze` per language, with the `security-extended` queries: `go` with a manual build that CodeQL traces
  (`go build ./...`, then with the tags `embedui,devpg` and a placeholder page for the git-ignored `internal/webui/dist`, then
  for `GOOS=windows` (also with the tags, because `internal/devpg/lock_other.go` is `devpg && !unix` and no other build compiles
  it) and `GOOS=darwin`, so that the code behind the build tags and the Windows-only code of the updater are analysed),
  `javascript-typescript` (the web UI and the JavaScript scripts of the website), `python` (`ci/`, `scripts/`,
  `website/scripts/`, `assets/brand/source`, `contrib/enterprise-mirror`) and `actions` (the workflows and the local action). It
  runs on pushes to `main`, on every pull request to `main` and weekly. Pull requests from forks and from Dependabot are
  analysed too, because GitHub always lets code scanning upload the results of a `pull_request` run (default setup would skip
  forks; advanced setup does not). The job's one write scope, `security-events`, is listed in `ci/workflow-permissions.txt` with
  its review: the token of a fork run is read-only, a Dependabot run receives the scope (the `permissions` key raises the
  read-only default of Dependabot), and the scope also allows dismissing alerts. That stays harmless because the steps of the
  job compile the code and run none of it, and the job has no secret. Default setup of code scanning must stay off in the
  repository settings, because this workflow replaces it. `init`, `analyze` and the `upload-sarif` step of `scorecard.yml` use
  the same pin of `github/codeql-action` (rule R2). A new language, build tag or operating system goes into the matrix and into
  the build step, and the first run has to show that the extractor saw the new files; a new build tag in `ci/check-local.sh`
  goes into the build step as well.
- **A read-only token**: `permissions: contents: read` at the top of every workflow. A job-level write scope needs a line in
  `ci/workflow-permissions.txt` (`<workflow file> <job> <scope>`) and a review of the job.
- **No `pull_request_target` and no `workflow_run`**, `persist-credentials: false` on every checkout, a timeout on
  every job, and no `github.event.*`, `github.head_ref` or `inputs.*` expression inside a `run:` block (pass it through `env:`).
- **Secrets only for listed deployment jobs.** A `${{ secrets.NAME }}` expression passes the check only when all three hold:
  `ci/workflow-secrets.txt` lists `<workflow file> <job> <NAME>`, the job declares `environment:` (so the protection rules of
  the environment apply before the job starts), and the expression is the whole value of a key in the `env:` of the job or of
  one of its steps (`KEY: ${{ secrets.NAME }}`). Everything else fails, also inside a listed job: a secret in `run:` text,
  `with:`, `if:`, the workflow-level `env:`, a flow mapping or a block scalar, in index syntax (`secrets['NAME']`) or as the
  argument of a function (`toJSON(secrets)`), and every `secrets:` key (`secrets: inherit`, also in a flow mapping that
  spans lines). The same words inside a `run:` script, another block scalar or a quoted string are text and no key, so a
  step may write a manifest that contains `secrets:`. A line of the list whose workflow file, job or secret is gone, a
  malformed line and a duplicate line fail too. `github.token` is not a secret in this sense: use it (not
  `secrets.GITHUB_TOKEN`) when a step needs the token of the job.
- **Toolchains from the repository**: `go-version-file: go.mod` and `node-version-file: .node-version`, never a literal version.
- **Caches** come from `actions/cache` and the `setup-*` actions only. A cache written by a pull request is not restored on `main`.
  A release workflow must not use caches (`TOOLS_NO_CACHE=1`; rule R13).
- **Artifacts** of a failed run are an allow-list of log files. Artifacts of a public repository can be downloaded by any signed-in
  user, so they never contain tokens, cookies or database files.
- First-time contributors' pull requests wait for a maintainer to approve the workflow run.

### Add a deployment secret

To let a deployment job read a secret:

1. Create the GitHub environment (repository settings, Environments) with its protection rules, for example a deployment
   branch policy or required reviewers, and store the secret in that environment, not in the repository.
2. Declare `environment: <name>` in the job and pass the secret as `KEY: ${{ secrets.NAME }}` in the `env:` of the job or of the
   step that needs it. The step reads the environment variable.
3. Add the line `<workflow file> <job> <NAME>` to `ci/workflow-secrets.txt`. The pull request says which secret, why the job
   needs it and which environment protects it. Fork pull requests receive no secrets from GitHub.

### Update an action pin

Dependabot proposes the updates weekly in one pull request and keeps the comment in step with the SHA ([Dependency
updates](#dependency-updates)). By hand: look up the commit of the release tag and replace the SHA and the comment in every place
the action is used (`ci/check-workflows.py` fails when the pins differ):

```bash
git ls-remote --tags https://github.com/actions/checkout.git 'refs/tags/v7.0.1' 'refs/tags/v7.0.1^{}'   # the ^{} line, if present, is the commit
python3 -I ci/check-workflows.py --remote        # compares every pin with its tag on GitHub (needs network)
```

The pin of `ossf/scorecard-action` is updated the same way (Dependabot's `github-actions` entry, or by hand with the tag of
the release); the SHA must be a commit of `ossf/scorecard-action` itself, which the Scorecard API verifies and `--remote`
checks through the tag.

Moving `.node-version` to a Node.js release without corepack (Node.js 25 and later) needs a pinned `npm install -g corepack@<version>`
step in `.github/actions/setup/action.yml`. Move it to the next LTS line only after its first LTS release.

### Release workflow

`.github/workflows/release.yml` is the workflow that builds, signs, verifies and publishes a release when a tag `v*` is pushed
([ADR-0035](adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md); the maintainer's runbook is
[Releasing](releasing.md)). It is not part of `ci-ok` and cannot be started by hand, so you test it in pieces:

```bash
bash ci/tests/run-all.sh static             # includes the three release tests below
bash ci/tests/release-scripts-test.sh       # the scripts of ci/release with stubs for cosign, gh and crane; the real cosign checks a fixture
bash ci/tests/release-smoke-test.sh         # the smoke tests (ci/release/smoke.sh) against test doubles
bash ci/tests/release-workflow-test.sh      # the shape of release.yml: jobs, needs, permissions, environments, artifacts
make release-rehearsal                      # the path of a release in a snapshot, including the staging of the 40 files
```

The real-cosign cases of `release-scripts-test.sh` need the pinned cosign (`make tools TOOLS=cosign`); they are skipped locally
without it and required when `CI=true`. The workflow itself is checked by the `workflows` gate (actionlint, `ci/check-workflows.py`,
zizmor), which must be clean from the first commit of any change.

Rules for a change to the release workflow:

- **Never rename `release.yml` and never move signing into a reusable workflow.** The file name is part of the signing identity
  that every installer, `myriarch-updater` and the documented commands check (ADR-0035, point 10, and ADR-0029). `ci/release/identity.env`
  holds the values, and a test pins them to the installers, `internal/brand` and the ADR. `ci/release/meta.sh` refuses a run
  whose `GITHUB_WORKFLOW_REF` is not this file at the tag.
- **A new job decides its place under R13 first.** If it builds or runs code of the repository (Go, Node.js, pnpm, goreleaser,
  make), it has `contents: read` and no environment, and it sits before the privileged jobs. If it needs a write scope,
  `id-token`, `attestations` or the environment, it calls only scripts of `ci/release/`, cosign, crane, gh or `actions/attest`,
  takes artifacts by ID and digest, and needs `guard` and the approval job transitively. Add the table row of
  `ci/tests/release-workflow-test.sh` in the same change, and the line of `ci/workflow-permissions.txt` with its reason for
  every write scope.
- **No `${{ }}` in `run:` text.** Values reach scripts through `env:` of the step, from `needs.guard.outputs.*` or other jobs'
  outputs. `github.token` appears only in the step-level `env:` of the steps that need it.
- **Scripts first.** Logic goes into a tested script of `ci/release/` with stable error codes (`[code]` in the message) and
  tools only from `TOOLS_BIN`, not into `run:` text.
- **Pins** of actions follow [Update an action pin](#update-an-action-pin); the release workflow uses the same SHAs as the others.

## Dependency updates

Dependabot (`.github/dependabot.yml`) proposes version updates every Monday, as one grouped pull request per directory, and
security updates as soon as an advisory has a fix. Every entry has a cooldown of 7 days (zizmor, audit `dependabot-cooldown`): a version
is proposed no earlier than a week after its release; security updates ignore the cooldown. The pull requests go through the same checks as any other change, and several
of them need a follow-up commit on the same branch:

| Ecosystem and directory | Group | What else the update has to change |
|---|---|---|
| GitHub Actions: `/` and `/.github/actions/setup` | `github-actions` | Nothing: Dependabot rewrites the SHA and the version comment ([Update an action pin](#update-an-action-pin)) |
| Go: `/`, the product | `go-modules` | `make notices`: `THIRD_PARTY_NOTICES` lists the version of every linked package, and the `licenses` gate fails on a drift |
| pnpm: `/web` | `web-ui` | `make notices` for the bundled packages. TypeScript majors are held back (`ignore` in `dependabot.yml`) until openapi-typescript and typescript-eslint support TypeScript 7 |
| pnpm: `/website` | `website` | `website/data/third_party.yaml` for a shipped package and the Mermaid version in `website/config/_default/params.yaml` (`website/scripts/check-notices.py` checks both) |
| pip: `/assets/brand/source` | `brand-python` | Compare the output of the brand generator (below) |
| npm: `/assets/brand/source` | `brand-node` | Compare the output of the brand generator (below) |
| Go: `/ci/tools/*`, the CI tools | none | Security updates only (below) |

**Sign-off.** DCO is a required check next to `ci-ok`. The DCO app (DCO-2) passes the commits that a bot authored without a
sign-off, so a Dependabot pull request is green as Dependabot made it. Leave its commits alone: `git rebase --signoff` and a force
push would replace the commits that GitHub signed for Dependabot with local ones, and Dependabot could no longer rebase or update
the pull request. A commit that you add to the branch, such as a follow-up of the table, is yours and carries your sign-off
(`git commit -s`, section "Sign-off" of [CONTRIBUTING.md](../CONTRIBUTING.md)). Dependabot stops rebasing a pull request once
someone has pushed to its branch, so push the follow-ups last. Merge by squash or rebase, and read the release notes before you
merge a major version.

**Security updates** need the repository setting "Dependabot security updates". They do not wait for Monday, and they ignore the
groups and the limit of open pull requests. The setting is not limited to the entries of `dependabot.yml`, so an update can also
arrive for a file that "Updated by hand" below lists. A pull request for `ci/tools/reuse` needs a look at the Python 3.9 floor: the
requirements are hash-pinned for Python 3.9 and newer, and CI runs a newer Python, so it cannot see a break of the floor. Compare
the pull request with the output of the `uv pip compile --python-version 3.9` commands in `ci/versions.env`, and build the tool once
with a Python 3.9 interpreter (`REUSE_PYTHON`).

**Website overrides.** The Vercel CLI and markdownlint-cli2 pin some of their own dependencies to vulnerable versions, and
`website/package.json` replaces those through `pnpm.overrides` (`website/README.md`, section "Node dependency overrides", gives the
reason for each entry). After an update of one of the two tools, run `pnpm install` and `pnpm audit` in `website/` and drop an entry
whose removal changes nothing in `pnpm-lock.yaml` but the `overrides:` block. `pnpm audit` still lists a few accepted findings, which
the README names with their reasons; any other finding needs a decision.

**CI tools.** The module of a tool (`ci/tools/<tool>`) names the tool only in a `tool` directive, so every requirement in it is
marked `// indirect`, and Dependabot proposes version updates for direct requirements only. The tool versions are the pins of
`ci/versions.env`, and `ci/check.sh` fails when a `go.mod` names another version. The entry therefore sets
`open-pull-requests-limit: 0` and exists for security updates only. Dependabot runs `go mod tidy` on every Go module it updates, so
such a pull request carries more than the vulnerable requirement: the tidy run adds `go.sum` lines for modules that have nothing to
do with the advisory (from under ten to a few hundred, depending on the tool), while `ci/tools/update.sh` does not run it. Read the
`go.mod` diff against the advisory. A change of either file rebuilds the tool (see "Local tools"), so CI builds and uses the raised
tool in the pull request. A security update of the tool itself fails `ci/check.sh` until `ci/versions.env` names the new version;
then run `ci/tools/update.sh <tool>`. That script builds `go.mod` and `go.sum` again from the pin, which drops a raised requirement
that the new release of the tool does not require itself, so look at the Dependabot alerts of the module after such an update.

**Brand generator.** No gate runs `assets/brand/source/make_assets.sh`, and the committed files depend on the versions of its
packages. After an update of one of them, run the script into an empty directory and compare its files with `assets/brand/`
(`assets/brand/README.md` says what to expect).

**Updated by hand.** The digests of the container images (`ci/pin-images.sh`; `ci/check.sh` keeps the Dockerfiles, the compose
file and `ci/versions.env` in step), `reuse` (`ci/tools/reuse`, hash-pinned for the Python 3.9 floor; see the security updates
above), the Docsy theme (`website/go.mod`; after an update `website/scripts/check-overrides.py` shows which upstream file changed)
and the pins of `ci/versions.env`: the tool versions and the downloads with a SHA-256, such as Hugo, Dart Sass and lychee.

## Licenses, headers and third-party notices

The project follows the REUSE specification 3.3 ([ADR-0030](adr/0030-reuse-compliance-and-third-party-notices.md)). Three
things are checked by the `licenses` gate (`make licenses-check` runs them without the other gates): the SPDX headers of
source files, `reuse lint`, and the drift of `THIRD_PARTY_NOTICES`.

### Headers

Every source file in scope starts with these two lines in its comment syntax, followed by one empty line (HTML and Helm
templates are the exception: their content follows the header directly):

```text
Copyright The Myriarch Authors
SPDX-License-Identifier: Apache-2.0
```

The line comment is `//` for Go, TypeScript and JavaScript, `#` for shell, PowerShell, Python, Dockerfiles, the Makefile,
service units and the `sed` templates under `scripts/e2e/ui-updates/`, and `--` for SQL. CSS uses a block comment, HTML a
comment block directly above the original content, and Helm templates a `{{- /* ... */ -}}` comment, which leaves the
rendered output unchanged. A shebang, a `shellcheck` directive, a Dockerfile parser directive (`# syntax=...`), a Python
coding line and a leading `<!doctype>` line stay in front of the header; a Go `//go:build` line and a package comment come
after it. A file that has to hold other SPDX tags or copyright lines (test data, generators of notices) wraps them in
`REUSE-IgnoreStart` and `REUSE-IgnoreEnd` comment lines.

```bash
make license-headers                            # python3 -I scripts/license-headers.py --fix
python3 -I scripts/license-headers.py --check   # reports "path: reason" (missing, wrong text, wrong position, refused)
python3 -I scripts/license-headers.py --list    # style and path of every file in scope
```

`--fix` only inserts missing headers, is idempotent and refuses (exit 1, "manual fix") a file that already holds an
`SPDX-License-Identifier:` outside the expected block. `web/src/api/schema.d.ts` is generated: `pnpm gen:api` adds the
header through `web/scripts/add-license-header.mjs`.

### REUSE.toml and `reuse lint`

Files without comments (YAML, JSON, data, images, Markdown) and third-party content are covered by annotations in
`REUSE.toml`. Each annotation has `precedence = "override"`, so when several annotations match a file the last one wins. A
comment above each annotation names the source of third-party content (URL, tag or commit). The license texts are in
`LICENSES/` (only `Apache-2.0` and `CC-BY-4.0`); `reuse lint` fails on an unused license. `LICENSE` in the repository root
is the unchanged text of the Apache License 2.0.

```bash
ci/install-tools.sh reuse    # builds the pinned reuse into .cache/reuse/<version>, linked to .cache/bin/reuse
ci/reuse-lint.sh             # LANGUAGE=en reuse --root <repo> lint
```

The wrapper sets `LANGUAGE=en`: a `reuse` that was built where `msgfmt` exists would otherwise print German.

`reuse` is pinned to 5.1.1 (`REUSE_VERSION` in `ci/versions.env`), the last release that supports Python 3.9; version 6 and
later need Python 3.10. The installer needs Python 3.9 or newer (`REUSE_PYTHON` selects the interpreter; Debian also needs
the package `python3-venv`). It creates a virtual environment and installs only hash-pinned packages
(`ci/tools/reuse/requirements.txt` and `ci/tools/reuse/build-requirements.txt`, with `poetry-core` for the build from source).
`ci/check.sh` verifies that the inputs pin the same version. To change the pin, edit `ci/versions.env` and the `.in` files,
then regenerate the lock files with [uv](https://docs.astral.sh/uv/) so that they resolve for every Python from 3.9:

```bash
uv pip compile --no-config --python-version 3.9 --generate-hashes --no-header --no-annotate \
  ci/tools/reuse/requirements.in -o ci/tools/reuse/requirements.txt
uv pip compile --no-config --python-version 3.9 --generate-hashes --no-header --no-annotate \
  ci/tools/reuse/build-requirements.in -o ci/tools/reuse/build-requirements.txt
```

### Third-party notices

`THIRD_PARTY_NOTICES` is generated and never edited by hand:

```bash
make notices                                   # scripts/gen-third-party-notices.sh, writes THIRD_PARTY_NOTICES
scripts/gen-third-party-notices.sh --check     # regenerates into a temporary file and fails on a difference
scripts/gen-third-party-notices.sh --out FILE  # writes somewhere else
```

The generator needs `go`, `go-licenses` in `.cache/bin` (`ci/install-tools.sh go-licenses`), `python3` and the installed web
dependencies (`pnpm install --frozen-lockfile` in `web/`). It combines:

- `go-licenses report` for every command (`myriarchd`, `myrctl`, `myriarch-updater`, `myriarch-sim`) and for linux, windows
  and darwin, plus one run with the `devpg` tag, whose packages are marked as used by development builds only. It reproduces
  every distinct license text verbatim and prints identical texts once (see below), and every `NOTICE` file of each linked
  module verbatim with its component.
- The Vite license report (`build.license` in `web/vite.config.ts`) for the packages bundled into the embedded web UI.
- `ci/license-overrides.txt`, shared with `ci/check-release-licenses.sh`. Each line maps a module path prefix to the license
  that `go-licenses` reports, the license to declare, the license file (or `-`), the scope (`*` for every package below the
  prefix, or a comma-separated list of exact import paths that fails closed when another package of the prefix is linked) and
  the reason. The generator fails on an
  override that matches nothing, and on any linked library without a license text.
- `scripts/notices/third-party-texts.json` with texts that cannot be read from the build: the Go `LICENSE` and `PATENTS`
  (the Homebrew toolchain ships neither) and npm packages that publish no license file.

The output has no absolute paths, timestamps or toolchain versions, so the drift check is stable. It also records the origin
of the embedded Mozilla root bundle, of `time/tzdata`, and the run-time download of PostgreSQL by `myriarchd dev`.

Identical license texts are printed once. Section 5, "License texts", lists every distinct text under an ID (`[T1]`, `[T2]`,
...; numbered in the order of first use, so adding a dependency can renumber the later texts), and each component entry
names its license files with a line `License text: [Tn]`. Two texts share an ID only when they are byte-identical after the
normalization of the generator (LF line endings, exactly one trailing newline); a text that differs in any byte, such as a
copyright line or an appendix, has its own ID. `NOTICE` files are never shared: they stay verbatim in the entry of the
component that ships them. An entry of section 5 gives the ID, the component and file where the text was first used and
its first line; it does not name a license, because the license expression of a component does not say which license a
file of it holds. The renderer fails when the file is larger than 1,000,000 bytes (`--max-bytes`), which is the limit
of the publication gate; this applies to `make notices` and to the drift check of the `licenses` gate.

If the generator fails because a bundled npm package has no license text, add an entry to the `npm` list of
`scripts/notices/third-party-texts.json` with `name`, `version` (the bundled version; the generator fails when it differs),
`identifier` (the SPDX expression from `package.json`), `source` (the URL of the upstream license file at the tag or commit
of that version), `sha256` of the text and the `text` itself. Download the file into a new, empty directory, check it against
the upstream repository at that tag, compute the SHA-256 yourself, and run `make notices`. Entries are refreshed when the
package version changes.

Binary archives (GoReleaser), container images and the running server ship the files: archives hold `LICENSE` and
`THIRD_PARTY_NOTICES` at their root, images hold them under `/usr/share/licenses/myriarch/`, and `myriarchd` serves them at
`/licenses/LICENSE` and `/licenses/THIRD_PARTY_NOTICES` without login. Settings > System links both.

## Kubernetes probe on kind (`hack/kind`)

`make kind-probe` runs the whole system on a local kind cluster from zero: it builds the pinned tools, creates the cluster with
a local registry, builds the server and collector images for the node architecture with the crane scripts (`IMAGE_PLATFORM`),
installs PostgreSQL, an SSH Git server, the Helm chart, the simulator and one real collector in container mode, and runs numbered
checks (sign-in with the bootstrap admin, registration with declared enrollment tokens, a configuration rollout, a collector
version update by the calls of the UI button with an automatic rollback and a retry, a pod restart, the leader takeover,
NetworkPolicy enforcement, the Dex quickstart, log and secret hygiene). It needs Docker, no sudo, no browser and no edits of
`/etc/hosts`; it exits 0 only without a failed check. `make kind-up` creates only the cluster, `make kind-down` removes it
(`ALL=1` also the registry and the work directory `.cache/kind`).

The probe is part of the language guard (`hack` is in the strict scope) and of the static gates (`ci/lint-shell.sh`,
`ci/check.sh` verifies the digest pins, `ci/helm-conform.sh` renders the probe values). Prerequisites, stages, environment
variables and troubleshooting: [`hack/kind/README.md`](../hack/kind/README.md).

## Web UI

The UI is React 19, TypeScript, Vite 8 and Mantine 9. It is built into static files in `internal/webui/dist` and embedded
into the `myriarchd` binary with `-tags embedui` (`make web-build && make build`). Run the commands below in `web/`.

| Script | Purpose |
|---|---|
| `pnpm dev` | Vite dev server (port 5173) with a proxy for `/api` (including the event stream) and `/v1` to `http://localhost:8080`; `MYRIARCH_BACKEND` overrides the target |
| `pnpm dev:mock` | Dev server **without a backend**: MSW answers every operation of `api/openapi.yaml` in the browser |
| `pnpm build` | Typecheck and production build into `../internal/webui/dist` (contains no mock code) |
| `pnpm build:mock` | Build with mock mode into `dist-mock/` (static, can be hosted for demos) |
| `pnpm gen:api` | Regenerate `src/api/schema.d.ts` from `../api/openapi.yaml` (with `--default-non-nullable false`) |
| `pnpm typecheck`, `pnpm lint`, `pnpm test` | Quality checks (Vitest, Testing Library, MSW) |

`make web-install`, `make web-gen`, `make web-build` and `make web-dev` wrap the install, `gen:api`, `build` and `dev` scripts.
After you change `api/openapi.yaml`, run `pnpm gen:api` and commit the generated `schema.d.ts`; the `web` gate fails when it
is out of date. Run `make docs-gen` as well: the REST API reference (`docs/reference/api`) is generated from the same file.

### Mock mode

`pnpm dev:mock` starts around 1,000 deterministic collectors in five fleets, a few unassigned agents and some
`myriarch-updater` agents, releases up to 0.163.0 with breaking changes, three rollouts (running, waiting for approval,
completed) and a GitOps status with a warning. Actions change the state in memory, and a timer simulates live events
(heartbeats, health changes, rollout progress).

- Sign-in: the user name `admin`, `operator` or `viewer` (any password) selects the role, so you can check permissions.
  The mock starts signed in as `admin`.
- Debug hooks in the browser console: `window.__myriarchMock.dropStream()` simulates a dropped event stream, and
  `window.__myriarchMock.db` is the mock state.

### Demo: update a version from the UI

Mock mode lets you play through the whole flow "choose in the UI, watch the update live" as `operator` or `admin`:

- A Kubernetes gateway fleet whose Git target is 0.161.0 and whose frame allows `>=0.161.0, <=0.162.0` offers the button to
  update to 0.162.0. The dialog starts the update and the rollout page shows the steps of the single target live: offered,
  download, verification, installation, restart, health check, probation, succeeded (about two seconds per step). The mock
  updates one collector only, so that the steps of a single target are easy to follow.
- Rollback variant: if the reason you enter contains the word `rollback`, the health check fails, the host rolls back
  (phase "rolled back") and the rollout pauses. "Retry failed" starts a second attempt that succeeds.
- Other fleets show the remaining states: a selection made in the UI that is running with a step bar per target, a selection
  that a narrower Git frame has made invalid ("on hold"), and fleets without a frame in Git, which have no button.
- To test delayed planning (the dialog waits for the leader), set `window.__myriarchMock.db.deferPlanning = true` before you
  submit.

### Source layout

```text
src/api/        generated types (schema.d.ts), openapi-fetch client (CSRF, 401), TanStack Query hooks
src/auth/       AuthProvider (GET /auth/config, /auth/me), permissions
src/live/       EventSource connection with backoff, cache patches per server-sent event
src/i18n/       en.json (source of truth) and de.json; same keys, enforced by a test; locale registry in locales.ts
src/theme/      Mantine theme, status colors, WCAG AA contrast variables
src/components/ shared building blocks (badges, pager, dialogs, YAML view and diff, ...)
src/pages/      one directory per page
src/mocks/      MSW handlers, deterministic fixtures, fake EventSource, update simulation (updateSim.ts)
```

The agent list keeps filter, sort order and page only in the URL, for example
`/agents?status=online&attr=site.type%3Dedge&sort=-version`. Facet counts come from the server (`getAgentFacets`).

## End-to-end tests

`scripts/e2e.sh` checks the whole system without containers and without network access (a local release mirror and a mock
receiver replace the outside world):

```bash
scripts/e2e.sh                       # smoke run, 32 simulated collectors, about 6 minutes
E2E_REAL=1 scripts/e2e.sh            # additionally a real supervisor and otelcol-contrib (downloaded into .e2e/real/dl and verified, darwin and linux)
E2E_AGENTS=1000 scripts/e2e.sh       # load run with 1,000 collectors
E2E_KEEP=1 scripts/e2e.sh            # leave myriarchd and the simulator running; clean up with: scripts/e2e.sh --stop
```

Steps (each prints PASS, FAIL or SKIP with evidence; the exit code is 0 only without FAIL): build; `myriarchd dev` with the test
data in `internal/testdata/gitops` (GitOps, fleets, releases from fixtures, UI); simulator (all connected, `in_sync`, fleet
distribution); version rollouts through the updater executor (artifacts, canary, approval, waves, real download with a
token, 401 without a token); configuration rollout; failure path (pause, rollback, drift, correction); webhook and AWX
executors against the mock (signature, `limit`, completion only through the reported version); release tracking and
notification; real supervisor (optional); two replicas with a configuration change on both, `kill -9` failover and rejoin
without duplicate agents; UI smoke test (optional); declarative enrollment tokens; log check. At the end, everything that was
started is stopped again (myriarchd, simulators, supervisor, collector, embedded PostgreSQL).

With more than 100 collectors (the load run), the script raises `maxConcurrent` of the `gateway` rollout policy from 1 to 10 in
its working copy `.e2e/gitops` (never in `internal/testdata`): one gateway at a time, a rollout of the 125 gateways takes about
13 minutes and exceeds `E2E_STEP_TIMEOUT`, and 10 equals the `batchSize` of the webhook executor, so the load run also sends
batched webhook calls, which the serial smoke run never does.

| Variable | Meaning |
|---|---|
| `E2E_AGENTS` | number of simulated collectors (default 32) |
| `E2E_PROFILE` | simulator profile (default `e2e`) |
| `E2E_HA=0` | skip the two-replica step |
| `E2E_UI=0\|1` | `0` skips the UI smoke test; `1` makes a missing browser or `playwright-core` a failure (default `auto`: run when the prerequisites are found, else skip) |
| `E2E_REAL=0\|1` | `0` switches the real supervisor step off; `1` downloads, verifies and runs it and turns an unsupported platform or a failed download or check into a failure (default `auto`: it runs only when `opampsupervisor` and `otelcol-contrib` are already in `E2E_REAL_DIR`, and then verifies them as well) |
| `E2E_REAL_DIR` | directory of the supervisor and collector (default `.e2e/real`; the verified downloads stay in its `dl/` subdirectory) |
| `E2E_REAL_BASE_URL` | download location of the upstream release files (default: the GitHub releases of `opentelemetry-collector-releases`; `file://` is accepted for tests) |
| `NO_BUILD=1` | use the existing binaries in `bin/` |
| `E2E_KEEP=1` | leave everything running at the end |
| `E2E_STEP_TIMEOUT` | seconds per rollout phase (default 600) |
| `E2E_DIR` | work directory (default `.e2e/`) |
| `E2E_PORT_A`, `E2E_PORT_B`, `E2E_PORT_PG` | ports of the two replicas (18080, 18081) and of PostgreSQL (54339); more `E2E_PORT_*` variables exist for the mock, mirror and simulator ports |

The work directory `.e2e/` (git-ignored) holds the logs `myriarchd-a.log`, `sim.log`, `mock.log`, `ui-smoke.log` and the
result list `results.txt`. The test uses fixed ports, so do not run two instances, and do not run it while `make dev` uses
the same ports.

**UI smoke test** (`scripts/e2e/ui-smoke.mjs`): signs in through the login form, visits every page in the light and dark
color scheme, checks console errors, failed API calls and the pagination of the agent list, and checks that the `viewer`
role sees no rollout actions. It needs `playwright-core` and a Chromium-based browser. `playwright-core` is a development dependency
of `web/` (no browser download; `cd web && pnpm install --frozen-lockfile` provides it), the browser is one that is already
installed (Chrome on the GitHub runner images):

```bash
(cd web && pnpm install --frozen-lockfile)                # playwright-core; or set E2E_PLAYWRIGHT_NODE_PATH to a folder with node_modules
E2E_BROWSER="/path/to/chromium-based-browser" scripts/e2e.sh
```

Without the prerequisites the step is skipped with a note (with `E2E_UI=1`, it fails).

**Real supervisor** (`E2E_REAL`): the upstream OpAMP Supervisor and `otelcol-contrib` of `OTELCOL_VERSION` (`ci/versions.env`) are
downloaded for darwin and linux on amd64 and arm64 (`scripts/lib/real-binaries.sh`). Each asset is compared with the SHA-256
that the release publishes next to it, on the download and again on every later run; a mismatch deletes the file and fails the
step. The check detects corrupt or changed files, not a compromised release. Test it without network access with
`ci/tests/e2e-real-download-test.sh`.

**UI-triggered version updates** (`make e2e-ui-updates`, `scripts/e2e-ui-updates.sh`) test the operator path of the version
update on your machine: `myriarchd dev`, a local mirror with the real release files of two collector versions, a real
supervisor and collector, and `myriarch-updater` in container mode. The test calls what the UI dialog calls (version options,
select version, then the live progress), triggers a real rollback by killing the new collector during the health check and
succeeds after a retry. It needs network access once for the release files. Options include `E2E_KEEP=1`, `NO_BUILD=1`,
`E2E_DIR` (default `.e2e/ui-updates`), `E2E_GIT_TARGET=git|none` and `E2E_UI=auto|1|0`; the header of the script lists them
all. It uses its own ports (myriarchd 18090, PostgreSQL 54340, mirror 19997).

## Database tests against an existing PostgreSQL

The Go tests that need a database start an **embedded** PostgreSQL per test program (`internal/testutil/pgtest`). It downloads
its binaries on first use and refuses to start as `root`. In CI runners without internet access, or as `root`, set
`PGTEST_DSN` to an existing server instead (a role with `CREATEDB`; a superuser is recommended):

```bash
export PGTEST_DSN='postgres://postgres:secret@127.0.0.1:5432/postgres?sslmode=disable'
ulimit -n 10240 && go test -race -count=1 ./...
```

Each test program creates its own template database `myriarch_tmpl_<pid>_<random>` (parallel test programs share the
server), migrates it and clones it per test. User, password and parameters of the DSN are kept; only the database name is
replaced. At the end, the template and the test databases are dropped (an aborted run leaves `myriarch_tmpl_*` and `t_*`
behind). The tests of `internal/devpg`, which start an embedded PostgreSQL themselves, are skipped in this mode.
`PGTEST_SKIP=1` skips all database tests. When several packages run in parallel, give the server a `max_connections` of at
least 300; the embedded server uses 100 per test program.

## Documentation

The documentation lives in `docs/` and is licensed under CC-BY-4.0 ([ADR-0018](adr/0018-license-and-copyright.md)); it is
written in English ([ADR-0020](adr/0020-english-as-project-language.md)). The structure follows the
[Diátaxis](https://diataxis.fr/) framework (tutorials, how-to guides, concepts and reference), plus operations, security and the
project documents. The site structure follows these sections:

| Section | Diátaxis group | Content | Weight |
|---|---|---|---|
| `getting-started/` | Tutorials | from source to a running system | 10 |
| `tasks/` | How-to guides | step-by-step guides | 20 |
| `concepts/` | Concepts | how the system works | 30 |
| `reference/` | Reference | GitOps schema, generated command-line reference, REST contract, Helm chart | 40 |
| `operations/` | Operations | authentication, production checklist, troubleshooting | 50 |
| `security/` | Security | security model | 60 |
| `architecture.md`, `adr/`, `design/`, `development.md`, `releasing.md` | Project | architecture, decision records, design contracts, this guide, versioning and release tags | 70, 80, 90, 100, 110 |

The directory `tasks/` keeps its name, because links and the URLs derived from it should stay stable; its title is "How-to
guides". The website adds pages that exist only there (placeholders for pages that are still being
written, the project landing page and the FAQ) and renders the Helm chart README, the GitOps template README and the brand
README as pages ([Website](#website)).

### Front matter and links

- Every Markdown file under `docs/` starts with YAML front matter with exactly these keys in this order: `title` and
  `description` as double-quoted strings and `weight` as an integer:

  ```yaml
  ---
  title: "Fleets"
  weight: 40
  description: "How fleets select agents and how the target version is chosen."
  ---
  ```

  The description is one sentence that ends with a period. The body does not repeat the title as an H1; sections start at H2.
- Decision records (`docs/adr/NNNN-*.md`) and the template keep the MADR layout without front matter.
- Section landing pages are named `_index.md`. `docs/adr/README.md` and `docs/design/README.md` keep their names so that
  existing links keep working.
- Pages in a section use the weights 10, 20, 30 and so on, in reading order.
- Links are relative and end in `.md` (for example `../concepts/fleets.md`); files outside `docs/` are linked relatively too.
  Do not use absolute `/path` links. Link to anchors only inside the same file.
- Use neutral examples ([ADR-0022](adr/0022-white-label-and-neutral-examples.md)): edge sites, data centers, cloud and
  Kubernetes; the quickstart fleets `linux-hosts`, `windows-hosts` and `k8s-gateway`; hosts under `example.com` or `.test`
  and addresses from RFC 5737.
- Root files (`README.md`, `CONTRIBUTING.md`, ...) and `.github/` files have no front matter.

### Generated references

Five references are generated and must not be edited by hand:

- `docs/reference/cli/` comes from the cobra command trees of the four binaries.
- `docs/reference/api/` comes from the OpenAPI contract `api/openapi.yaml` (internal package `internal/apidoc`): an index with the
  conventions, security schemes, the table of permissions with their lowest role (resolved from `internal/authz`) and the error
  format, one page per tag with every operation, and a page with the data types.
- `docs/reference/configuration.md` comes from the configuration struct in `internal/config` (types, the defaults of
  `Default()` and `DevDefaults()`, the doc comments as descriptions) and from the loader in `internal/configload`.
- `docs/reference/environment-variables.md` comes from the environment overrides of the loader, from a registry of the other
  environment reads in `internal/refdoc/env.go` and from the deployment templates under `deploy/supervisor` and `deploy/updater`.
- `docs/reference/metrics.md` comes from the metric definitions in the Go sources.

The last three pages are generated by `internal/refdoc`, which only tests import.

After you add or change a command, flag or help text, a configuration key, an environment read or a metric, or change the contract:

```bash
make docs-gen          # regenerates all five (TestCLIReference with CLIDOC_UPDATE=1, TestAPIReference with APIDOC_UPDATE=1, TestReferencePages with REFDOC_UPDATE=1)
```

`TestCLIReference` in `cmd/myriarchd`, `TestAPIReference` in `internal/apidoc` and `TestReferencePages` in `internal/refdoc` fail
when the committed files differ from the generated output (without the variable they only compare), and `ci/check-docs-gen.sh`
makes the same comparison for the `docs` gate. After a change of the contract also run `pnpm gen:api` in `web/` (the types of the
web UI come from the same file).

The generators for the configuration, environment and metrics pages rely on rules that tests enforce:

- **Doc comments of the configuration** (`TestConfigCommentsAreDocumentation`). The comments in `internal/config/config.go` are
  the descriptions on the page, so write them for the reader of the page. (1) Every field with a YAML key has a doc comment, the
  comment block above the field; a field whose type is a named struct of the package may rely on the doc comment of the type. A
  comment on the line of the field does not count. (2) The comment starts with the Go name of the field, as Go convention asks;
  the generator replaces that first word by the YAML key. (3) The text names no Go identifier of the package, neither a field or
  type name with an inner capital letter (`CertFile`, `DownloadTokenTTL`, `ArtifactsConfig`) nor a Go path
  (`TLS.TerminatedUpstream`): write the YAML key or path in backticks (`server.tls.terminatedUpstream`). Acronyms (`TLS`, `OIDC`) and
  ordinary words are fine. A duration in Go syntax has no unit for days, so write "30 days" and let the Default column show `720h`.
  The generator turns a mention of a file below `docs/` into a link, escapes characters that Markdown or Hugo would take for
  markup (`<base>` would disappear as raw HTML) outside of code spans, and refuses unpaired backticks.
- **Environment reads** (`TestEveryEnvironmentReadIsDocumented`). A string literal passed to `os.Getenv`, `os.LookupEnv` or a
  function called `getenv`, and every constant of `internal/brand` whose name ends in `Env`, must have an entry in the registry
  `envReads` in `internal/refdoc/env.go` (or, for a variable of the deployment templates, in `templateVarMeaning`). Reads in
  `internal/testutil` are exempt. The overrides of configuration keys need no entry: they come from the loader.
- **Metric definitions** (`ScanMetrics`). Every metric is a literal call of `prometheus.NewCounter`, `NewCounterVec`, `NewGauge`,
  `NewGaugeVec`, `NewHistogram`, `NewHistogramVec`, `NewSummary` or `NewSummaryVec` with a `prometheus.<Kind>Opts` literal that sets
  only `Name`, `Help` and `Buckets` (`Objectives` for a summary), all as literals. `Help` is a sentence that ends in a period, a
  histogram sets its `Buckets`, and the label names of a vector are a `[]string` literal. `Namespace`, `Subsystem`, `ConstLabels`,
  `promauto`, `NewDesc`, `NewGaugeFunc` and the other constructors are refused with the file and line; extend `internal/refdoc`
  in the same change if you need one. `cmd/myriarchd` compares the metrics of a running server with the page.
- **Golden tests.** `TestGoldenConfiguration` and `TestGoldenMetrics` compare the output for a small structure and for
  `internal/refdoc/testdata/metrics/ok` with `testdata/golden`; `REFDOC_UPDATE=1 go test ./internal/refdoc` rewrites them (and
  the three reference pages) and you review the diff.

The API generator renders a deliberately small subset of OpenAPI: the keywords type, format, enum, default, example, description,
properties, required, items, additionalProperties, the numeric, length and item limits, uniqueItems, pattern, `$ref` to
components, deprecated, readOnly and writeOnly, the contract info (title, version, summary, description, contact, license) and
exactly one server whose URL is the base path. `Validate` refuses everything else (`allOf`, `oneOf`, `anyOf`, `not`, `title`, a
type array, an inline nested object, a property description of more than one paragraph, a description or `required` where the
page has no place for it, an external `$ref`, more than one server) and names the JSON pointer. A keyword is either rendered in
every place where `Validate` accepts it or refused there, never dropped; `TestEveryKeywordIsRendered` sets every keyword in every
place and checks the output. If the contract needs more, extend the generator and its tests in the same change instead of
loosening the check. Every operation names its permission in `x-permission`; the generator fails for a permission that
`internal/authz` does not know (`public` and `custom` are the exceptions). The golden test `TestGolden` compares the output for `internal/apidoc/testdata/mini.yaml`
with `testdata/golden` (`APIDOC_UPDATE=1 go test -run TestGolden ./internal/apidoc` rewrites them).

The field reference `docs/reference/gitops-schema.md` is maintained by hand
and checked by `TestGitOpsSchemaReference` in `internal/gitops`: every kind, field and option of the GitOps format must appear
there, so a new field fails the test until it is documented.

### Checks

```bash
make docs-check        # generated reference up to date, links resolve, front matter is valid
```

`make docs-check` runs `ci/check-docs-gen.sh` and `scripts/check-docs.py` (subcommands `links` and `front-matter`). `links`
resolves every relative Markdown link and checks the anchors of links into the same file; `front-matter` checks the key set,
the order and the types. External links are not checked offline. The links check skips `website/` (the site content uses
site-absolute links, which Hugo and lychee validate); `front-matter --site` applies the rules of the site content to it. `make check-local` runs the same checks as the `docs` gate,
and the `language` gate covers the English text of `docs/`, the root files and `.github/`.

### Decision records

Significant decisions are recorded as ADRs in `docs/adr/` (process in [docs/adr/README.md](adr/README.md)). Accepted records
are not rewritten: a change of mind is a new record that supersedes the old one. Editorial fixes (links, typos) are allowed.
Design contracts in `docs/design/` are normative: code that deviates from them is a bug in the code or a reason to update the
contract in the same pull request.

## Website

The project website (myriarch.dev, [ADR-0031](adr/0031-website-with-hugo-and-docsy.md)) is built with Hugo extended and the
Docsy theme from the sources in `website/` and from the Markdown files of the repository that it mounts. It is a static
site: the output is the directory `website/public`. The site is developed now and published after the first public push; no
command here deploys anything.

### Commands

```bash
make website            # production build into website/public (hugo --panicOnWarning)
make website-serve      # Hugo development server on http://127.0.0.1:1313 with live reload
make website-preview    # production build served as static files on http://127.0.0.1:1313
make website-check      # the whole website gate: build and every check
```

All four call `ci/website.sh`, which puts `.cache/bin` on the `PATH`, installs the pinned tools (`hugo`, `sass`, `lychee`),
runs `pnpm install --frozen-lockfile` in `website/` and keeps Hugo's module cache in `.cache/hugo`. The first run needs network
access (Go proxy for the theme, npm registry, release downloads); later runs use the caches. `WEBSITE_PORT` changes the port
(never 8080 or the ports of the end-to-end tests; `ci/website.sh` refuses them), `WEBSITE_BASEURL` overrides the base URL for
a preview on another host, and `WEBSITE_ENV=preview` builds the `preview` environment (`noindex`, `Disallow: /`). The build needs Go (Hugo
fetches the Docsy theme as a Go module), Node.js and pnpm; `make website-check` and the browser checks need Node.js 22 or newer
(`markdownlint-cli2`) and, for the latter, a Chrome (`CHROME` selects the executable).

### What is mounted and how links work

`docs/` is the content of `/docs/`, never copied: `website/config/_default/module.yaml` mounts it. The decision records
(`docs/adr/NNNN-*.md`) and the root files `ROADMAP.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, `GOVERNANCE.md`, `SECURITY.md`,
`CODE_OF_CONDUCT.md`, `MAINTAINERS.md`, `ADOPTERS.md` and `CONTRIBUTOR_LADDER.md` are rendered through content adapters
(`website/content/**/_content.gotmpl`, data in `website/data/repo_pages.yaml`): the H1 line becomes the title and the file is
otherwise shown as it is. URLs mirror the repository paths (`docs/concepts/gitops.md` is `/docs/concepts/gitops/`, `ROADMAP.md`
is `/project/roadmap/`). `api/openapi.yaml` (offered for download; the REST API reference in `docs/reference/api` is generated from it) and the brand files of `assets/brand` are mounted as well. Pages that exist only
for the website live in `website/content` (front matter checked by `scripts/check-docs.py front-matter --site`).

A link render hook maps the links of the Markdown files: a relative link is resolved against the repository path of the
page and becomes a link to the page that renders the target, or to the file on GitHub when the site has no page for it.
Write the links as you do for GitHub (relative, ending in `.md`, with the anchor). Do not write a double opening brace followed by `<` or `%`
in a Markdown file that the site renders: Hugo takes it for a shortcode, also inside code blocks, and the build fails. Site-absolute links (`/docs/`) are only
allowed in `website/content`; a link to a page that does not exist fails the build. Images must be resources of the site: an
image that cannot be resolved fails the build, because an image from another host would be a third-party request.

Some pages of the theme are replaced by modified copies in `website/layouts` (the offline search script, the favicons, the
"edit this page" links, Mermaid, the footer, the 404 page). `website/overrides.txt` pins the SHA-256 of the upstream file
of each one; after a Docsy update `website/scripts/check-overrides.py` shows which upstream file changed.

### Adding a page and placeholders

A page that belongs to the documentation is a Markdown file in `docs/` with the usual front matter; it appears in the site
and in the sidebar by its weight. A page that exists only on the website goes to `website/content/` with `title` and
`description` (`weight` is optional). A page that a later change will write gets a placeholder at the website path of
the page it will replace:

```yaml
---
title: "Upgrade myriarch"
weight: 50
description: "Upgrade the server, the database schema and the updaters without downtime."
params:
  placeholder:
    tracking: "operations-manual"
    replaces: "docs/operations/upgrade.md"
    planned: "v0.2.0"
---
```

The body is a single call of the `placeholder` shortcode (copy an existing placeholder page), which renders the notice and links to the closest existing pages. The file is `website/content/docs/operations/upgrade.md`. The tracking value is never rendered; it is a lowercase topic slug (`operations-manual`) or a GitHub issue reference, which must be quoted (`"#123"`; an unquoted `#` starts a YAML comment). An optional `planned: "v0.2.0"` under `placeholder` makes the notice say
that the page is planned for that release; without it the notice makes no promise. When the real page is
added to `docs/`, the placeholder must be deleted in the same change: `website/scripts/check-placeholders.py` fails otherwise,
so two pages for one URL cannot exist.

### Checks

`make website-check` builds the site and runs, in this order: `hugo mod verify`; lychee over `website/public` (offline, with
fragments; the config is `website/lychee.toml`); markdownlint (`website/.markdownlint-cli2.jsonc`) over `docs/`, the mounted
root files and `website/content`; `scripts/check-docs.py front-matter --site`; every `website/scripts/check-*.py` over the built
site (`check-third-party.py`: no resource from another host in any page, style sheet or script; `check-local-paths.py`: no local
path of the build machine; `check-repo-links.py`: every link into the GitHub repository (blob, tree, edit) names an existing path; `check-overrides.py`; `check-placeholders.py`; `check-notices.py`: every shipped dependency has a
notice in `website/data/third_party.yaml` and a published license text under `/licenses/`); and the unit tests of those checks
(`python3 -I -m unittest discover -s website/scripts -p 'test_*.py'`, also part of `make ci-test` as `ci/tests/website-test.sh`).
Add a check by adding `website/scripts/check-<name>.py`; the runner picks it up. A check receives the built site as its argument
and prints paths and counts only.

Two checks need a browser and are not part of the gate:

```bash
make website-preview &                      # production build on 127.0.0.1:1313
node website/scripts/check-requests.mjs     # every page of request-pages.txt in light and dark: only same-origin requests,
                                            # no CSP violation or console error, the search finds results, diagrams render
```

and Lighthouse (version 13, needs Node.js 22.19 or newer, therefore not in the lockfile):

```bash
pnpm dlx lighthouse@13.5.0 http://127.0.0.1:1313/ --output=json --output-path=/tmp/lh-home.json \
  --chrome-flags="--headless=new" --only-categories=performance,accessibility,best-practices,seo
python3 -I website/scripts/lighthouse-scores.py --min 90 /tmp/lh-home.json
```

The server behind `make website-preview` (`website/scripts/preview-server.py`) behaves like a static host: directory index files,
`/404.html` with the 404 status and gzip for text files. Without compression the search index (about 0.7 MB of JSON) and the style
sheet (about 0.4 MB) would cost about 30 points of the performance score. Stop the preview server afterwards. Add pages to
`website/scripts/request-pages.txt` when you add a page with its own scripts.

The REST API reference is made of static pages without scripts of their own; the data types page is long (about 150 types), which is
why it is a separate page. `check-requests.mjs` checks that a tag page renders its tables and that the site search finds the operations
(`approveRolloutWave`) and permissions (`rollouts:operate`) of the contract, and that the mark on the landing page plays its animation and
ends on identity transforms (and does not animate with reduced motion).

### Privacy rules

The site makes no request to a third party when a page loads, sets no cookies and has no analytics. Do not add a CDN script, a
web font from a provider, an embedded frame, a tracking pixel or a remote image; the checks above fail on them, and production
pages carry a Content Security Policy (`website/layouts/_partials/csp.html`) that allows only the site itself. Third-party
code comes from `website/package.json` (exact versions, `pnpm-lock.yaml`) and is served from the site; a new component needs a
notice in `website/data/third_party.yaml`. The only value stored in the browser is the light or dark choice (`localStorage`);
the page `/legal/privacy/` says so. A new feature that needs more has to be decided in an ADR first.

### Hosting

The first host is Vercel, but the site is plain static files without functions or host configuration, so it can move to
Netlify or GitHub Pages. Build it with the project's own tooling (`make website-check`) and deploy the directory
`website/public`; do not use the build image of the host, which has no Dart Sass and would run repository code. Headers such as
HSTS and `frame-ancestors` cannot be set in a meta tag and belong to the deployment. `website/README.md` has the details.
