# 0032. Continuous integration on GitHub Actions with GitHub-owned, SHA-pinned actions and one aggregate check

- Status: Accepted, amended by [ADR-0033](0033-openssf-scorecard-as-the-only-third-party-action.md) (one third-party action, rule R1)
- Date: 2026-10-10
- Related: [ADR-0011](0011-no-containers-for-local-development-and-tests.md), [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0030](0030-reuse-compliance-and-third-party-notices.md), [ADR-0031](0031-website-with-hugo-and-docsy.md)

## Context

The repository is public and `main` accepts pull requests only. Until now a maintainer ran `make check-local` on every
change. The gates are scripts (`ci/check-local.sh`) that already hold every command, so the work is to run them on GitHub
runners, to cover the platforms that a single developer machine cannot (Linux on amd64 and arm64, macOS, Windows), and to
do so without widening the attack surface of a public repository:

- A workflow of a public repository runs code from pull requests of strangers. The organization allows only GitHub-owned
  actions (`actions/*`, `github/*`), requires every action to be pinned to a full commit SHA, and sets a read-only default
  token. Workflows of first-time contributors need approval.
- Third-party actions run with the token and the cache of the job. Each one is a supply-chain dependency that updates
  itself unless it is pinned, and a pin does not help when the action downloads further code at run time.
- The branch ruleset needs stable check names. Job names that depend on a matrix, on skipped jobs or on path filters make
  a required check either impossible to name or able to stay "pending" forever.
- The project has no container runtime requirement for development ([ADR-0011](0011-no-containers-for-local-development-and-tests.md)).
  CI should not be the only place where the gates can run.

## Decision

1. **GitHub Actions runs the gates of `ci/check-local.sh`.** A job installs the toolchain through the local composite action
   `.github/actions/setup` and runs `ci/check-local.sh` with `ONLY=<gates>`. In GitHub Actions the runner streams each gate in a
   log group (`CHECK_LOCAL_STREAM`) and still writes the log files, which a failed job uploads. A gate added to
   `ci/check-local.sh` without a job in `ci.yml` fails the policy check (rule R8). Developers reproduce any job with
   `make check-local ONLY=...`.
2. **Only GitHub-owned actions, pinned to a commit SHA.** The workflows use `actions/checkout`, `setup-go`, `setup-node`,
   `cache` and `upload-artifact`, each as `uses: actions/<name>@<40 hex> # vX.Y.Z`. Every other tool comes from the
   repository's own pinned installer (`ci/install-tools.sh`: Go tools built from `ci/tools/<tool>` with `go.sum`, release
   downloads checked against SHA-256 values) and pnpm through corepack. Dependabot keeps the pins current.
3. **A read-only token and secrets only for listed deployment jobs.** Every workflow has `permissions: contents: read` at
   the top. A job may ask for more only when `ci/workflow-permissions.txt` lists the workflow, the job and the scope.
   `pull_request_target` and `workflow_run` are forbidden, checkouts never persist credentials, and untrusted expressions
   (`github.event.*`, `github.head_ref`) reach a script only through `env:`. The CI jobs use no secret. A deployment job may
   read a secret when `ci/workflow-secrets.txt` lists the workflow, the job and the secret, the job declares `environment:`
   (so the protection rules of the environment apply before it starts) and the secret is the whole value of a key,
   `KEY: ${{ secrets.NAME }}`, in the `env:` of the job or of one of its steps. Every other way to reach a secret fails the
   check, also inside a listed job: `run:` text, `with:`, `if:`, index syntax, `toJSON(secrets)`, `secrets: inherit`. Caches
   written by a pull request are scoped to that pull request by GitHub and are never restored on `main`; release builds will
   run without caches ([ADR-0023](0023-keyless-release-signing.md)). Uploaded logs are an allow-list of log files, never
   tokens, cookies or database files, because the artifacts of a public repository can be downloaded by any signed-in user.
4. **One required check: `ci-ok`.** The job `ci-ok` depends on every other job (including the called end-to-end workflow)
   and succeeds only when all of them succeeded; a skipped, cancelled or failed job fails it. The workflows have no path
   filters, so `ci-ok` always reports. The branch ruleset requires `ci-ok` and the DCO check, and nothing else.
5. **A platform matrix where the code depends on the platform.** Go tests run with `-race` on Linux amd64 and arm64 against
   a PostgreSQL service (`PGTEST_DSN`, same image digest as the compose file) and on macOS against the embedded PostgreSQL.
   Windows runs the tests without `-race` and without the database tests (`PGTEST_SKIP=1`). The `go` gate also vets the code
   for Windows and macOS and compiles the tests of the host updater for Windows. A wall-clock budget needs an idle machine, so
   the render budget of the server (5000 agents in under 5 s) is asserted only by a run of its own without `-race`
   (`GO_TEST_PERF`, `PERFTEST=1`), never inside the parallel `go test ./...`; Windows leaves that run out, because the server
   does not ship for Windows.
6. **End-to-end tests are part of CI.** `scripts/e2e.sh` runs on pull requests with the real supervisor and the UI smoke
   test; with `E2E_REAL=1` and `E2E_UI=1` a missing prerequisite is a failure, and the downloaded collector binaries are
   checked against the SHA-256 that upstream publishes on every run. The load run (1,000 collectors) and the UI-triggered
   version update run nightly.
7. **The policy is code.** `ci/check-workflows.py` enforces points 2 to 4 and the use of `go.mod` and `.node-version` for
   the toolchains; `actionlint` (pinned in `ci/tools/actionlint`) checks syntax, expressions and the shell blocks. Both run in
   the `workflows` gate, locally and in CI.

## Consequences

### Positive

- One source of commands: a contributor reproduces a failed job on their machine, and the maintainers do not maintain a
  second description of the checks.
- A small, reviewable supply chain: five GitHub-owned actions, pinned, updated by pull requests.
- A fork pull request cannot reach a secret or a write token: GitHub passes no secrets to the workflow of a fork, the CI jobs
  have none, and a deployment job needs a reviewed line in `ci/workflow-secrets.txt` and a protected environment.
- Windows, macOS and arm64 are tested before a release, not after.

### Negative

- Some steps are slower than a purpose-built action would be (tools are built from source on a cache miss; the cache has
  to be keyed per job).
- The nightly runs and the Windows tests need maintenance; a scheduled workflow is switched off by GitHub after 60 days
  without repository activity.
- The line-based policy check relies on the layout that actionlint accepts (two-space indentation, one key per line) and
  does not parse YAML anchors; the workflows do not use them, and an alias next to a secrets expression fails the check.
- Network-dependent gates (vulncheck, helm schemas, website, embedded PostgreSQL download) can fail for reasons outside the
  repository. A failed job is re-run; there are no hidden retries.

## Alternatives considered

- **Third-party actions (`pnpm/action-setup`, `goreleaser/goreleaser-action`, `docker/*`, `golangci/golangci-lint-action`,
  `sigstore/cosign-installer`, ...)**: not allowed by the organization policy, and each would add a dependency with the token
  in reach. The repository's installer does the same job with checksums the project controls.
- **One job that calls `make check-local`**: simple, but it serializes about an hour of work, hides which gate failed, cannot
  use a PostgreSQL service or other runner types, and gives no platform matrix.
- **Required checks per job**: the names of matrix jobs and conditional jobs are brittle in a ruleset, and a renamed job
  silently removes a requirement. A single aggregate check moves that decision into the workflow, where it is reviewed.
- **Container-based jobs (a CI image with all tools)**: reproducible, but needs a registry, a build pipeline for the image
  and a way to trust it; the pinned installer with a cache is enough and also works on macOS and Windows.
- **`pull_request_target` for faster feedback with write access**: runs the base branch's workflow with a write token on
  untrusted input; forbidden.
