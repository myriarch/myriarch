# 0048. Release scope of v0.3.0 to v0.6.0

- Status: Accepted
- Date: 2026-11-07
- Related: [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md), [ADR-0027](0027-mcp-principles.md), [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md)

## Context

[ADR-0021](0021-publication-gates-and-v0-1-0-scope.md) fixed the scope of v0.1.0 and named the plan for v0.2.0. The roadmap after v0.2.0 was a list under "Next" that did not put the items in a sequence: the `gitops-pr` executor ([ADR-0026](0026-collector-version-updates-on-kubernetes.md)), MCP access, stage 1 ([ADR-0027](0027-mcp-principles.md)), runtime branding ([ADR-0022](0022-white-label-and-neutral-examples.md)) and the operations manual, which was announced for v0.3.0. A production Docker Compose setup sat under "Later". The length of that list suggested that MCP and branding were as near as the rest.

**What is still missing for beta.** The conditions in "From alpha to beta" in the roadmap are stable interfaces, a tested upgrade path, operations guides, tests on real hosts and feedback from users. Two of them depend on no version: real hosts and feedback.

**Users.** The project has few users so far, and feedback from users is a condition for beta. What helps users to give feedback is an installation that they can run in production and keep up to date, and a first start that works without reading the whole documentation. MCP and branding serve users who already run the product.

**Operations.** The five operations pages (upgrades, backup and restore, monitoring, air-gapped installation, artifact mirror) are placeholders. CI upgrades the chart from v0.1.0 on kind and the database schema from v0.1.0 ([the upgrade notes](../operations/upgrade-to-v0-2-0.md) describe the upgrade to v0.2.0). The starting version is fixed, and no test upgrades a Linux host from the previous minor version.

**Windows.** [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md) ships unsigned Windows files for v0.1.0 and v0.2.0, each under a reviewed exception for exactly that tag, and requires a new, reviewed exception for every later final tag as long as signing is not in place. The signing service is not set up yet.

## Decision

We will release the next four minor versions in this order and with these themes. The decision names no dates. What is not done when a version is cut moves to the next version.

1. **v0.3.0, "Operations and updates on Kubernetes".**
   - An automatically tested upgrade path from the previous minor version to the release candidate, on kind and on a Linux host. Windows is not part of it.
   - The operations manual: upgrades, backup and restore, monitoring, air-gapped installation and the artifact mirror.
   - The `gitops-pr` executor of [ADR-0026](0026-collector-version-updates-on-kubernetes.md): an update opens a pull or merge request in the deployment repository that bumps the collector image. The version that the agent reports still decides completion. Container mode stays an opt-in.
2. **v0.4.0, "Easy to start".**
   - First-time setup in the web UI: a guided path from the first sign-in to the first connected host, empty pages with help, and error messages that name the next step.
   - Installation for production: Docker Compose with TLS, PostgreSQL and a Git source, tested in CI; a Helm profile for production that CI installs; a hardening guide; and a start page "first host in 15 minutes" whose commands run in CI.
   - The documentation split into a user manual and an administrator manual.
3. **v0.5.0, "Run it in your organization".**
   - A role for fleet operators.
   - Denied accesses in the audit log, and an export of audit data to a SIEM.
   - Published support and deprecation rules, and a security overview.
4. **v0.6.0, "Preparing beta".**
   - The GitOps format `myriarch.dev/v1beta1` with a documented migration from `v1alpha1`.
   - Runtime branding as in [ADR-0022](0022-white-label-and-neutral-examples.md).
   - An "Enterprise readiness" page, and a check of the conditions in "From alpha to beta".

MCP access, stage 1 ([ADR-0027](0027-mcp-principles.md)) follows v0.6.0 and has no version number. It moves from "Next" to "Later" in the roadmap. The change to beta is not promised by this order: the conditions in "From alpha to beta" stay as they are, and two of them, real hosts and feedback from users, depend on no version.

**Windows in v0.3.0.** v0.3.0 ships unsigned Windows files under a new, reviewed exception for exactly that tag, as [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md) point 8 requires for a later final tag. The pull request "Prepare v0.3.0" sets `WINDOWS_UNSIGNED_FINAL_TAG=v0.3.0` in `deploy/release/windows-signing.env` and keeps `WINDOWS_SIGNING=off`. The first pull request after the release empties the setting. The release notes carry the "unsigned" banner, and operators verify the Windows files with cosign before they run them. Authenticode signing stays under "Later". This record does not change ADR-0034.

**Order of the roadmap.** The roadmap shows the versions with their themes and refers to this record. A theme is a direction, not a promise ([ROADMAP.md](../../ROADMAP.md)).

## Consequences

### Positive

- The order follows what users need to start and to report back: a tested update, an operations manual and an easy start come before the features for people who already run the product.
- A tested upgrade path and the operations guides are two conditions for beta, and they come first.
- Users and contributors can see the order of the work in the roadmap.
- What is not finished when a version is cut moves to the next version, so a theme can slip without holding back a release.
- The production Compose setup, which the roadmap listed under "Later", now has a place in v0.4.0.

### Negative

- MCP access and runtime branding come later than the old roadmap suggested. Branding arrives with v0.6.0 and MCP after it.
- v0.3.0 ships unsigned Windows files, with the consequences that [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md) names: hosts with App Control need a hash rule for the release, Smart App Control may block the files, and operators have to verify the files with cosign.
- The four versions are a lot of work for a single maintainer. A version can take longer than planned or lose a theme to the next one.
- An order in the roadmap can be read as a promise although it is not one. The text of the roadmap says so, and it does not name dates.
- The order does not bring the change to beta closer by itself: real hosts and feedback from users depend on no version, and feedback needs people outside the maintainers.

## Alternatives considered

- **MCP, branding and a production Compose setup already in v0.4.0, and the preparation for beta in v0.5.0**: not chosen. The conditions for beta need feedback from users, and the project has few users. An easy start and a production installation come before features for existing installations.
- **Authenticode signing with SignPath already for v0.3.0**: not chosen. SignPath is not set up yet, and waiting for it would hold the release. A new, reviewed exception under the rule of [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md) covers the tag.
- **No version numbers in the roadmap**: not chosen. Users should see the order in which the work comes. Without version numbers the roadmap would show a list, as before.
