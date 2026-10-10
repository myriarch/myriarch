# 0028. Relationship to OpenTelemetry and path towards a foundation

- Status: Accepted; the precondition "move to a neutral organization" is superseded by [ADR-0029](0029-host-under-the-myriarch-organization.md) (the project is hosted in the myriarch organization from the start)
- Date: 2026-10-08
- Related: [ADR-0016](0016-trademark-usage-and-name-protection.md), [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0018](0018-license-and-copyright.md), [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0029](0029-host-under-the-myriarch-organization.md)

## Context

Myriarch is an OpAMP server and fleet control plane for OpenTelemetry Collectors running under the upstream OpAMP Supervisor. It is a server and backend system. The OpenTelemetry project does not host backend or server systems under its umbrella and recommends independent projects. The CNCF TOC expects applicants to document how they differ from and relate to OpenTelemetry.

CNCF Sandbox requirements include Apache-2.0 ([ADR-0018](0018-license-and-copyright.md)), DCO ([ADR-0019](0019-developer-certificate-of-origin.md)), a MAINTAINERS file with affiliations, a public repository with at least six months of public, active development, and reusable vendor-neutral software ([ADR-0022](0022-white-label-and-neutral-examples.md)). Applications with one or two maintainers and no adopters have recently been deferred.

## Decision

- Myriarch remains an independent project with foundation-compatible governance from the start: GOVERNANCE, MAINTAINERS with an organization column, CODE_OF_CONDUCT, SECURITY and a public roadmap.
- It contributes upstream instead of forking: participation in the OpAMP SIG, upstream fixes to opamp-go and the supervisor, alignment with the OpAMP specification and semantic conventions. It uses OpenTelemetry names only descriptively ([ADR-0016](0016-trademark-usage-and-name-protection.md)).
- A CNCF Sandbox application is a later goal, realistically targeted for mid to late 2027. Self-set preconditions go beyond the formal minimum:
  - at least three maintainers from at least two organizations
  - first public adopters (`ADOPTERS.md`)
  - the move to a neutral organization ([ADR-0017](0017-repository-hosting-and-public-identity.md))
- The documentation contains a "relationship to OpenTelemetry" page. It states what myriarch adds (GitOps-governed configuration rendering, rollouts in waves, version updates via executors, audit) and what stays upstream (protocol, supervisor, collector).

## Consequences

### Positive

- Clear scope.
- Contribution options towards OpenTelemetry stay open.
- Governance is ready when the criteria are met.

### Negative

- No foundation backing or visibility for the first year.
- Maintainer diversity is a hard dependency.
- Governance overhead early on.

## Alternatives considered

- **Donate to OpenTelemetry**: rejected, backend systems are out of scope for OpenTelemetry.
- **Upstream contributions only, no own project**: rejected, the control plane has no upstream home.
- **Another foundation, or none**: not pursued now, may be revisited.
