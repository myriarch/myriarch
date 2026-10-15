# 0038. Dependency license policy: a tripwire on the CNCF allowlist for pull requests, a narrower allowlist for releases

- Status: Accepted
- Date: 2026-10-15
- Related: [ADR-0018](0018-license-and-copyright.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md), [ADR-0030](0030-reuse-compliance-and-third-party-notices.md), [ADR-0032](0032-continuous-integration-on-github-actions.md), [ADR-0033](0033-openssf-scorecard-as-the-only-third-party-action.md)

## Context

[ADR-0018](0018-license-and-copyright.md) licenses the project under Apache-2.0 so that it stays acceptable to a foundation
([ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)), and says that the licenses of the dependencies are tracked
separately. [ADR-0030](0030-reuse-compliance-and-third-party-notices.md) generates the notices that the binaries owe their
dependencies. The check behind both is `ci/check-release-licenses.sh`: it runs `go-licenses` over the release builds and fails when
a linked Go package carries a license outside an allowlist of six (Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, MIT, Zlib), unless
`ci/license-overrides.txt` records an exception that someone checked by reading the license file.

That check is binding and runs for every pull request in the gate `go`, but it sees only the Go packages that a release build links. A
pull request that adds an npm package of the web UI, a Python tool or a Go tool module under a license that the CNCF would not admit
passes it, and the problem is found by a reader of the lockfile diff or at the release. The CNCF publishes the licenses that a project may use for
third-party components without a review by its Governing Board (the CNCF Allowlist License Policy, adopted on 2018-05-01); a
dependency outside that list is a problem for a project on the path to a foundation, and it is cheaper to notice it in the pull request.

GitHub offers the comparison as a service: the dependency graph knows the manifests and lockfiles of the pull request and of its
base, and `actions/dependency-review-action` (a GitHub-owned action, so it fits rule R1 of `ci/check-workflows.py`) reports the
license of each dependency that the change adds or updates (from the license data of ClearlyDefined) and its known advisories. It
supports pull requests and merge groups, takes an allowlist of SPDX identifiers and a list of package URLs that are exempt from the
license check, and ignores the version in such a package URL.

The numbers that decide how long the exception list is were read from the dependency graph of the repository on 2026-10-15: 2,253
entries (1,223 Go modules of the product and of the 15 CI tool modules, 1,003 npm packages of the web UI, the website and the brand
generator, 15 Python packages, 11 GitHub Actions and the repository itself). 81 packages carry a license expression that the CNCF
allowlist and Apache-2.0 do not cover, and 18 have no license information. Of the 81, four are Go modules of the product (three linked
into the release builds, one into the development build), 23 are Go modules of the CI tools, 4 are Python packages of tools, and 50 are
npm packages: the trees of the development tools of the web UI and of the website, the brand generator, and packages of the website and
the web UI with combined permissive licenses.

## Decision

1. **One source file, two lists.** `ci/license-allowlist.txt` names every license that dependencies may carry, each with the list it
   belongs to. The list `release` (Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, MIT and Zlib) is the binding allowlist of the release
   builds; `ci/check-release-licenses.sh` reads it from the file instead of carrying it in the script. The list `review` holds the rest
   of the CNCF allowlist (0BSD, BSD-2-Clause-FreeBSD, MIT-0, OpenSSL, OpenSSL-standalone, PSF-2.0, Python-2.0, Python-2.0.1,
   PostgreSQL, SSLeay-standalone, UPL-1.0, X11 and the Google patent license for Golang, under the identifier
   `LicenseRef-scancode-google-patent-license-golang` that the dependency graph uses for the PATENTS file of the Go modules). Apache-2.0
   is the license of the project and needs no review under the CNCF policy; it is on the list `release`. The `allow-licenses` of the
   dependency review are all the identifiers of the file, and `ci/tests/license-allowlist-test.sh` fails when the two differ.
2. **The dependency review of a pull request is a tripwire, and the release check stays binding.** The job `dependency-review` of
   `ci.yml` is part of `ci-ok`. For a pull request and for a merge group it runs `actions/dependency-review-action`, pinned to a commit
   like every action, with the configuration `.github/dependency-review-config.yml`, `fail-on-severity: high` and `fail-on-scopes:
   runtime`. It fails when a dependency that the change adds or updates carries a license outside the list or a known vulnerability of
   high or critical severity in the runtime scope. For every other event (a push to `main`, the nightly run, a manual run) a step says
   that there is nothing to compare, so the job succeeds instead of being skipped, because a skipped job fails `ci-ok`. A license that
   only the list `review` allows passes the pull request and fails the release license check until someone decides about it: the
   decision to widen the list `release` is a new decision record; a single package gets an entry in `ci/license-overrides.txt`.
3. **The job holds the read-only token and asks no third party.** It has no `permissions` key of its own and no secret; it writes no
   comment to the pull request (`comment-summary-in-pr: never`; the result is in the log and in the job summary); and it switches off
   the OpenSSF Scorecard lookup of the action (`show-openssf-scorecard: false`), which would send the repositories and names of the
   added packages to two other services (api.securityscorecards.dev and api.deps.dev). A dependency whose license is unknown is listed,
   not failed, as the action does; a license expression that is not valid SPDX fails.
4. **Exceptions are recorded with a reason.** `allow-dependencies-licenses` lists packages that may carry any license in the
   dependency review, as package URLs without a version, each group under a comment that gives the reason; the test refuses an entry
   with a version, a repeated entry and a group without a reason. The initial list holds the 80 packages that the review would
   otherwise report today: the four dependencies of the product that `ci/license-overrides.txt` or the development build already
   explain, the Go and Python modules of the CI tools (MPL-2.0 libraries of HashiCorp and Let's Encrypt, modules with several licenses in
   their test data or fonts, the REUSE checker), the development tool trees of the web UI and the website, the brand generator and the
   packages with combined permissive licenses. The action ignores the version of a package URL, so an exception also covers later
   versions of the package; whoever updates a package of the list looks at its license. The Go and Python modules of the CI tools are no
   part of any release artifact.
   A change of the lists, or of an exception, is a change of policy and is reviewed as one; the configuration is read from the checkout
   of the pull request, like every other file of the pipeline.
5. **`elkjs` is not excepted.** The npm package `elkjs` (EPL-2.0, a weak copyleft license) is a dependency of Mermaid, and the Mermaid
   chunks that the website serves include the ELK layout. The notices page of the website (`website/data/third_party.yaml`) lists Mermaid
   only. Whether the website owes a notice for `elkjs` is a question for the maintainer, not for a pipeline exception, so the first
   pull request that updates Mermaid will fail the review until it is answered.

## Consequences

### Positive

- A new dependency of any ecosystem (Go, npm, Python, GitHub Actions) is checked against the license policy in the pull request that
  adds it, with the advisories of the GitHub Advisory Database, and not at the release.
- The two lists cannot drift: one file, one test. The release check is as strict as before.
- The check uses a GitHub-owned action with the read-only token; no secret, no write scope, no new third party.
- The exception list documents the license landscape of the tool trees and the reason that each package is acceptable.

### Negative

- The license data comes from ClearlyDefined through the dependency graph. It can be wrong, partial or missing; a package without
  license information is only listed. A compound expression (`A AND B`) needs both parts on the list, so packages with a documentation
  license next to the code license need an exception.
- The review looks at the dependencies that a change adds or updates, not at the whole tree, and an exception covers every later version
  of its package.
- The list of exceptions is long at the start (80 packages), and the dependency graph does not say which npm packages it counts as
  development tools; entries for development-only trees may turn out to be unnecessary, and a Dependabot pull request that touches a
  package without an entry fails until one is added. The first runs show which.
- The action reads the dependency graph of GitHub. When GitHub cannot return the data for a revision range (a merge group that is not
  processed yet, for example), the job fails with a message and runs again.
- `elkjs` stays an open item (decision 5).

## Alternatives considered

- **Only the release license check**: binding, but Go-only and late. The feedback for an npm or Python dependency arrives after the merge.
- **A deny list (`deny-licenses`)**: a license that nobody thought of passes. The option is also deprecated in the action.
- **The release list as the allowlist of the review**: it would also trip on licenses that the CNCF admits and that the tool trees
  contain (Python-2.0 of `argparse`, 0BSD, MIT-0). That makes the tripwire noisier without protecting a release, because the release
  check already holds that line.
- **Third-party license scanners as actions** (FOSSA or Trivy, for example): not allowed by rule R1 of `ci/check-workflows.py`, and each
  would add a supply-chain dependency with the token in reach.
- **The configuration from the base branch** (`owner/repo/path@ref`): a pull request could then not loosen the policy that checks it.
  The action would fetch the file through the API, which counts against a rate limit, and the other policy files of the pipeline
  (workflows, allowlists, scripts) come from the checkout of the pull request as well, so reading this one from the base would protect
  little.
- **`fail-on-scopes: runtime, development`**: reports every license and advisory in the lockfiles of the development tools, which is noise
  that a project of this size cannot clear.
- **Generating the YAML configuration from the source file**: the action needs the file in the checkout, so it would have to be committed and
  its drift checked; a test that compares the two is less machinery than a generator.
