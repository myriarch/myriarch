# 0017. Repository hosting, maintainer identity and later move to a neutral organization

- Status: Superseded in part by [ADR-0029](0029-host-under-the-myriarch-organization.md) (decision parts 1, 3 and 4; part 2, the maintainer identity, stays in force)
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md), [ADR-0029](0029-host-under-the-myriarch-organization.md)

## Context

The Go module path, image paths, the chart reference, the MCP registry name and the keyless signing identity ([ADR-0023](0023-keyless-release-signing.md)) all embed the GitHub owner and repository name. The choice of owner is therefore a compatibility decision.

Foundation onboarding expects the project to live in a neutral organization. The Developer Certificate of Origin ([ADR-0019](0019-developer-certificate-of-origin.md)) requires contributions under a real identity, not an anonymous one. The MAINTAINERS file needs an organization or affiliation column.

## Decision

1. **Start under the maintainer's personal GitHub account.**
   - Repository: `github.com/gilfy/myriarch`
   - Go module: `github.com/gilfy/myriarch`
   - Images: `ghcr.io/gilfy/<image>`
   - Chart: `oci://ghcr.io/gilfy/charts/myriarch`
   - MCP registry name: `io.github.gilfy/myriarch`
   - Signing identity: `https://github.com/gilfy/myriarch/.github/workflows/release.yml@refs/tags/v*`
2. **The maintainer uses a known, non-anonymous name.** Commits are signed off with that name and the GitHub noreply address. MAINTAINERS lists the affiliation as "Independent".
3. **Before a foundation application, a later change moves the repository to a neutral GitHub organization.** The organization name `myriarch` is reserved. The costs of the move are accepted and planned:
   - ghcr packages stay with the old owner and must be republished,
   - image paths and the signing identity change,
   - the module path changes unless a vanity import path is used.
4. **Optional vanity import path `go.myriarch.dev/myriarch`**, served through `go-import` meta tags from the project domain. It keeps the Go module path stable across the move. If adopted, it must be in place before the first tagged release; otherwise introducing it is a breaking change as well.

## Consequences

### Positive

- Simplest possible start without organization administration.
- The identity is transparent and compatible with the DCO.
- The move is planned and documented from the beginning.

### Negative

- The later move is a breaking change for image paths, the verification identity and possibly the module path. Release notes document the move; a transition approach (for example dual publishing) is decided with the move.
- A single owner is a bus-factor risk until the move; a second organization owner mitigates this after the move.

## Alternatives considered

- **Neutral organization from day one**: deferred for simplicity. It remains the target state.
- **Anonymous pseudonym**: rejected. It is incompatible with the DCO guidance on real identities.
