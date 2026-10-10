# 0019. Contribution model: Developer Certificate of Origin, no CLA

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0018](0018-license-and-copyright.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)

## Context

Contributions need a clear provenance statement so that the project can license them under Apache-2.0 ([ADR-0018](0018-license-and-copyright.md)).

Foundation IP policies, for example the CNCF's, require DCO sign-off for all new contributions. A Contributor License Agreement (CLA) is optional there and a known barrier for contributors. Automated project health checks, such as CLOMonitor, look for DCO presence.

## Decision

- Every commit carries a `Signed-off-by:` trailer (`git commit -s`) that matches the commit author, under the Developer Certificate of Origin 1.1. This applies from the very first public commit.
- A DCO check (the DCO GitHub App or an equivalent workflow) is a required status check on the default branch.
- Contributors sign off with a real, non-anonymous name ([ADR-0017](0017-repository-hosting-and-public-identity.md)).
- The project uses no Contributor License Agreement.
- CONTRIBUTING.md explains the sign-off, how to fix a missing one (`git rebase --signoff`), and that AI-assisted contributions are allowed but the human submitter certifies the DCO.
- Maintainers sign off their own commits like everyone else.

## Consequences

### Positive

- Low friction for contributors.
- Foundation-compatible.
- Inbound licensing equals outbound licensing.

### Negative

- The project holds no relicensing rights; relicensing would need the consent of all contributors.
- First-time contributors occasionally forget the sign-off and need guidance.
- The DCO is a statement, not a verified identity.

## Alternatives considered

- **Individual and corporate CLA**: rejected because of friction and administration effort.
- **No formalities**: rejected. Provenance would be unclear and the project would not be foundation-compatible.
