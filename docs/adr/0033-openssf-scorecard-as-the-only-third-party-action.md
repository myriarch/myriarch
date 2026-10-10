# 0033. OpenSSF Scorecard as the only third-party action, and policy rules for the Scorecard and release workflows

- Status: Accepted
- Date: 2026-10-10
- Related: [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0032](0032-continuous-integration-on-github-actions.md) (amended by this record)

## Context

[ADR-0032](0032-continuous-integration-on-github-actions.md) allows only GitHub-owned actions (`actions/*`, `github/*`),
each pinned to a full commit SHA, and enforces that as rule R1 of `ci/check-workflows.py`. The organization's allowed-actions
policy says the same.

The project wants the OpenSSF Scorecard badge as a signal for adopters and for the path towards a foundation
([ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)). The Scorecard API publishes a result, and so the
badge, only for a run of `ossf/scorecard-action` with `publish_results: true`, authenticated by the OIDC token of that run.
No GitHub-owned action can do this. The API reads the workflow file at the commit of the run and rejects results from a
workflow that does not have the shape below. The restrictions were checked on 2026-10-10 against the README of
`ossf/scorecard-action` v2.4.4 (section "Workflow Restrictions") and against `api/app/server/verify_workflow.go` of
`ossf/scorecard-infra`, which implements them (last changed 2026-08-07):

- The workflow has no top-level `env` or `defaults` and no workflow-level write permission.
- Only the job that runs `ossf/scorecard-action` has `id-token: write`.
- That job has no `env`, no `defaults`, no `container` and no `services`, and runs on one GitHub-hosted Ubuntu runner
  (`ubuntu-latest` or `ubuntu-NN.NN` from 22.04, also the `-arm` variants).
- Every step of that job is a `uses:` step. A `run:` step is rejected. The actions are limited to `actions/checkout`,
  `actions/upload-artifact`, `github/codeql-action/upload-sarif`, `ossf/scorecard-action`, `step-security/harden-runner` and
  `actions/create-github-app-token`.
- A step that pins a commit SHA must name a commit of that action's own repository (a commit that exists only in a fork is
  rejected as an "imposter commit").
- Results are accepted only for the default branch, so the workflow runs on pushes to `main` and on a schedule.

Where the API is looser than this record, the rules below follow the stricter reading: they do not allow `harden-runner`,
`create-github-app-token`, `ubuntu-latest` or the `-arm` runners, they limit the job to the keys it needs and they fix its
permissions. If upstream tightens its rules, rule R12 is the place to follow; the README of the action is the reference
that the pull request should cite.

The release workflow does not exist yet. Its rules are decided first, so that the policy check holds from its first commit
(the design of the release workflow is a separate record).

## Decision

1. **`ossf/scorecard-action` is the only action outside `actions/*` and `github/*`.** It may appear only in
   `.github/workflows/scorecard.yml`, in the job `analysis`, pinned to a full commit SHA with the release as a trailing
   comment (`uses: ossf/scorecard-action@<40 hex> # v2.4.4`). It never appears in a local action or in another workflow, and
   it has no subpath. Dependabot's existing `github-actions` entry updates the pin, and `ci/check-workflows.py --remote`
   compares the SHA with the tag in the comment. This is rule R1 with a single entry in `THIRD_PARTY_ACTIONS`; every other
   rule (R2 for equal pins, the SHA pin itself) applies unchanged.
2. **The Scorecard workflow has a fixed shape (rule R12).** The triggers are `push` to `main` and `schedule`, nothing else.
   The top-level permissions are `contents: read`, there is no workflow `env` or `defaults`, and there is exactly one job,
   `analysis`, on a GitHub-hosted `ubuntu-NN.NN` runner with a timeout. The job has the keys `name`, `runs-on`,
   `timeout-minutes`, `permissions` and `steps` and no others. Its permissions are exactly `contents: read`,
   `actions: read`, `security-events: write` and `id-token: write`; both write scopes are listed in
   `ci/workflow-permissions.txt` like any other write scope (rule R3). Its steps are only `uses:` steps: `actions/checkout`
   with `persist-credentials: false`, `ossf/scorecard-action` with `publish_results: true`, `github/codeql-action/upload-sarif`
   and optionally `actions/upload-artifact`. The workflow has no secrets and no caches.
3. **The organization allows the action.** The allowed-actions policy of the organization lists `ossf/scorecard-action@*`
   next to the GitHub-owned actions, and the requirement of full SHA pins stays on. A maintainer sets this in the
   organization settings; no workflow depends on it before `scorecard.yml` exists.
4. **The release workflow has its rules before it exists (rule R13).** `.github/workflows/release.yml` runs only for a pushed
   tag `v*` (no `workflow_dispatch`, no branch, no pull request). It uses no cache: no `actions/cache`, no `cache:` input,
   `cache: false` on `actions/setup-go` and `package-manager-cache: false` on `actions/setup-node` (both cache by default),
   and no local action or called workflow. An `environment:` is the literal `release`, never an expression. Build code runs
   only in jobs that have no write scope: a job with a write scope, with `id-token` or `attestations`, or with an
   environment never runs `actions/setup-node`, `actions/setup-go` or one of the commands `pnpm`, `pnpx`, `npm`, `npx`,
   `corepack`, `node`, `nodejs`, `yarn`, `go`, `goreleaser`, `make` and `gmake`. The check matches command words at the start
   of a command (also behind wrappers such as `sudo`, `time` and `env -S`, and inside `sh -c`, `eval`, `find -exec` and
   command substitutions), not words in comments, strings, here-documents, `case` patterns, arrays or prose. A shell that
   reads its script from standard input (a here-document, a here-string, a pipe into `sh`, `bash -s`) is a finding in a
   privileged job because its script is not in the run text, and so is a `shell:` other than `bash` or `sh` (for example
   `node {0}`), in a privileged step or job and in the workflow defaults. A job `guard` exists, every other job needs it, and
   every privileged job has the environment `release` itself or needs a job that has it, so that an approval gates it. Two
   constructs would make `needs:` ineffective and are findings in every job: an `if:` that calls `always()`, `failure()`,
   `cancelled()` or `success()`, and `continue-on-error` (at job or step level) other than `false`.
5. **Any further exception needs its own record.** A second third-party action, another job that may use
   `ossf/scorecard-action`, or a release job that builds code with privileges is a change of this record or a new one that
   supersedes it, with its own entry in the policy check.

## Consequences

### Positive

- The badge and the code-scanning findings of Scorecard are available without a second system.
- The Scorecard job has no secrets and no write access to contents, packages or releases. Its OIDC identity
  (`scorecard.yml` on `refs/heads/main`) is a different identity from the release identity of
  [ADR-0029](0029-host-under-the-myriarch-organization.md) and is never accepted where that one is required.
- The exception is as narrow as the policy check can state it: one action, one file, one job, one pin per release, and a
  shape that is tested with negative cases.
- The rules for the release workflow exist before the first line of it, so the first commit of `release.yml` is already
  checked, and a later change cannot quietly move build code into a job that holds a signing or publishing privilege.

### Negative

- Third-party code runs with `id-token: write` and `security-events: write`. A compromised release of the action could
  request tokens for that workflow identity or change code-scanning alerts. Mitigations: the SHA pin, reviewed Dependabot
  updates, the narrow permissions and exact identity pins in every verification that uses a workflow identity.
- Rule R12 mirrors rules of an external API that may change; a change upstream breaks publishing until R12 and the workflow
  follow. The check fails closed for layouts it cannot read.
- The checks are line- and token-based. R13 cannot see a program that a privileged job starts through a variable, through
  a script, through an interpreter that is not a shell (`python`, `perl`) or through a wrapper that it does not know
  (`flock -c`, `su -c`, `watch`); the scripts that such jobs call are therefore tested separately with a `PATH` that has no
  `go`, `node` or `pnpm`.
- The ban of caches and of local actions makes release builds slower and repeats a few steps; this is the intended price.

## Alternatives considered

- **No badge**: rejected by the maintainer on 2026-10-10; the signal is wanted for the foundation path.
- **The Scorecard CLI, built from `ci/tools`, with an upload through `github/codeql-action/upload-sarif`**: no third-party
  action and no exception, with findings in code scanning, but the result is not published, so there is no badge. Kept as the
  fallback if the exception is withdrawn (for example when the organization policy cannot list the action or upstream
  changes its rules in a way R12 cannot follow).
- **Relying on the weekly public scan of the Scorecard project**: no control over coverage or timing, and the result is not
  tied to a workflow of this repository.
- **A wider exception (any action of a list of publishers)**: every entry would be a supply-chain dependency with the token in
  reach ([ADR-0032](0032-continuous-integration-on-github-actions.md)); one named action in one named job is what the
  maintainers can review.
- **Checking `release.yml` only by review**: the release workflow is the place where one mistake (a cache, a build tool in
  the signing job, an approval that can be skipped) costs the most, and a reviewer cannot be asked to find all of them again
  in every change.
