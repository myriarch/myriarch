# 0029. Host the project in the GitHub organization "myriarch" from the start

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0016](0016-trademark-usage-and-name-protection.md), [ADR-0017](0017-repository-hosting-and-public-identity.md) (superseded in part), [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0027](0027-mcp-principles.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)

## Context

[ADR-0017](0017-repository-hosting-and-public-identity.md) decided to start under the maintainer's personal GitHub account `gilfy` and to move to a neutral organization before a foundation application. It deferred the organization for simplicity: no organization administration at the start. Two prerequisites for committing to the name were also still open: the trademark clearance search ([ADR-0016](0016-trademark-usage-and-name-protection.md)) had not finished, and the project domain had not been acquired. If the search had found a conflict, the project would have fallen back to the reserve name ([ADR-0015](0015-project-name-and-naming-scheme.md)).

ADR-0017 accepted the cost of the later move: ghcr packages stay with the old owner and must be republished, image paths and the keyless signing identity ([ADR-0023](0023-keyless-release-signing.md)) change, and the Go module path changes unless a vanity import path is in place before the first tagged release. The chart reference and the MCP registry name embed the owner as well and would also change. A transition approach for verifiers, for example accepting both signing identities for a while, was left to be decided with the move.

As of 2026-10-08 the situation has changed:

- The trademark clearance search found no relevant conflict. The name myriarch is final.
- The project domain `myriarch.dev` has been acquired and will host the website.
- The GitHub organization `myriarch` exists. It is empty, on the Free plan and owned by the project owner.
- Nothing has been published yet, and the rename ([ADR-0015](0015-project-name-and-naming-scheme.md)) has not been carried out. Every identifier that embeds the owner can still be chosen once and kept.

The deferral no longer saves anything, and the planned move would only add breaking changes for early adopters.

## Decision

We host the project in the GitHub organization `myriarch` from the first public commit. There is no later move to another organization. This replaces part 1, part 3 and part 4 of the decision in ADR-0017. Part 2 (maintainer identity) stays in force. It also replaces the owner-specific values in [ADR-0015](0015-project-name-and-naming-scheme.md) (Go module and image rows), [ADR-0023](0023-keyless-release-signing.md) (certificate identity), [ADR-0024](0024-container-registry-and-image-set.md) (registry path and image names) and [ADR-0027](0027-mcp-principles.md) (registry name), and the precondition "move to a neutral organization" in [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md). Everything else in those records is unchanged.

Canonical public identity:

| Item | Value |
|---|---|
| GitHub organization | `github.com/myriarch` (owned by the project owner, Free plan) |
| Repository | `github.com/myriarch/myriarch` |
| Go module path | `github.com/myriarch/myriarch`, no vanity import path |
| Server image | `ghcr.io/myriarch/myriarch` |
| Updater image | `ghcr.io/myriarch/myriarch-updater` |
| Further images (when published) | `ghcr.io/myriarch/<binary name>` from [ADR-0015](0015-project-name-and-naming-scheme.md), for example `ghcr.io/myriarch/myriarch-mcp` |
| Helm chart (OCI) | `oci://ghcr.io/myriarch/charts/myriarch`; further charts under the same prefix, for example `oci://ghcr.io/myriarch/charts/myriarch-collector` |
| Keyless signing identity, releases | certificate identity `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v*` (verified with `--certificate-identity-regexp '^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v'`), issuer `https://token.actions.githubusercontent.com` |
| Keyless signing identity, edge builds | the same repository and issuer with `@refs/heads/main`; never accepted for release verification |
| MCP registry name | `io.github.myriarch/myriarch`, published from the release workflow with GitHub Actions OIDC |
| Website | `https://myriarch.dev` |
| Commit and DCO identity | personal GitHub account `gilfy`, commits authored with its GitHub noreply address (`<id>+gilfy@users.noreply.github.com`), `Signed-off-by:` with the maintainer's real name and the same noreply address ([ADR-0019](0019-developer-certificate-of-origin.md)) |
| MAINTAINERS entry | name, GitHub ID `gilfy`, organization "Independent" |
| Security contact | GitHub private vulnerability reporting on `myriarch/myriarch` as the primary channel; `security@myriarch.dev` as the fallback address |
| Other project addresses | `conduct@myriarch.dev` (Code of Conduct reports), `maintainers@myriarch.dev` |

The project addresses are forwarding addresses on the project domain. They do not expose a personal mailbox, they are tested before SECURITY.md and CODE_OF_CONDUCT.md are published, and they are listed in the private account inventory.

**MCP registry namespace.** The registry accepts a GitHub-based namespace (`io.github.<owner>/...`, authenticated through GitHub) and a DNS-based namespace (`dev.myriarch/...`, authenticated through a DNS or HTTP record that holds a public key). We use the GitHub-based form `io.github.myriarch/myriarch`:

- It can be published from the release workflow with GitHub Actions OIDC. The DNS-based form needs a long-lived private key to sign registry logins. That contradicts the principle behind [ADR-0023](0023-keyless-release-signing.md) and [ADR-0024](0024-container-registry-and-image-set.md) that the release workflow holds no long-lived signing or registry secret.
- The DNS-based form was mainly attractive as a name that survives an owner change. With the organization in place from the start, that benefit is gone: the organization and the domain are both transferable and move together under a foundation contribution agreement.
- The DNS-based form stays available. If the project ever needs it, it is added through a later ADR.

**Organization setup.** The settings that ADR-0017 postponed to the move apply from the start:

- two-factor authentication required for all members,
- base permission `read`, members cannot create repositories or change package visibility,
- packages public, linked to the repository,
- an organization-wide Actions policy: `GITHUB_TOKEN` read-only by default, only allowed actions, actions pinned to a full commit SHA, workflows from fork pull requests only after approval, Actions cannot create or approve pull requests,
- a second organization owner (a trusted individual) as emergency access,
- `myriarch.dev` verified for the organization where the plan allows it.

**Instead of the organization move.** The foundation path in [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md) no longer contains a move. Its precondition "the move to a neutral organization" is replaced by the following items before a foundation application:

- the organization is configured as above and listed in the private account inventory together with the domain, the registrar and DNS, the mail forwarding and the registry accounts,
- at onboarding, the foundation's checklist applies. It typically adds the foundation's designated account as an owner of the organization and transfers the domain and the project name under the contribution agreement. The organization and its repository are transferred in place; no path or identity in the table above changes.

## Consequences

### Positive

- Repository, module, image, chart, signing and registry identifiers are set once, before the first release, and stay stable through a later foundation acceptance.
- No transition period with two signing identities, no republishing of packages, no module path migration and no repository transfer.
- The vanity import path and the `go-import` hosting on the website are no longer needed.
- Organization-level controls (two-factor requirement, Actions policy, base permission, second owner) are available from the first public commit instead of being replaced by per-repository settings.
- The second owner reduces the bus-factor risk that ADR-0017 accepted until the move.
- A neutral organization from the start is a clearer signal of vendor neutrality for contributors and for a foundation review.

### Negative

- The organization has to be administered from day one: owner list, member policies and Actions policy.
- Some controls depend on what the Free plan offers; anything that is missing is covered by repository settings and workflow linting.
- The maintainer identity (`gilfy`) and the project identity (`myriarch`) differ. Documentation and the DCO check must make clear that personal accounts author commits while the organization owns the project.
- Accepted records are not rewritten, so ADR-0015, ADR-0017, ADR-0023, ADR-0024 and ADR-0027 still name `gilfy` as the owner, and ADR-0017, ADR-0023, ADR-0024 and ADR-0028 still describe the move. Readers must know that this record is authoritative for owner-specific identifiers; the ADR index says which parts it replaces.

## Alternatives considered

- **Personal account `gilfy` plus vanity import path `go.myriarch.dev/myriarch` (ADR-0017 with its option)**: rejected. The vanity path only stabilizes the Go module. Images, chart, signing identity and MCP registry name would still change with the move. It also adds a hosting dependency for `go-import` meta tags on the website.
- **Personal account `gilfy` without vanity import path (ADR-0017 as decided)**: rejected. The organization exists and the name is cleared, so the move would only create breaking changes for module path, images, chart, signing identity and registry entry, plus a transition period with two trusted identities.
- **Start under `gilfy` and transfer directly to a foundation-owned organization at acceptance**: rejected. It has the same breaking changes as the planned move, only later and with more adopters affected. It also leaves the outcome dependent on an acceptance that may not happen. Foundations typically take over a project-owned organization in place, so a project organization from the start costs nothing at onboarding.
