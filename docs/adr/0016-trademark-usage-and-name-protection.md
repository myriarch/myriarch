# 0016. Trademark usage and protection of the project name

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)

## Context

The Linux Foundation trademark guidelines do not allow foundation marks such as OpenTelemetry, Kubernetes and CNCF to be used as part of a product name, including abbreviations. Names of OpenTelemetry components such as OpAMP are likewise used only descriptively. Whether the common abbreviation "otel" falls under this rule is untested, so the project avoids it ([ADR-0015](0015-project-name-and-naming-scheme.md)).

The new name needs practical protection without incurring cost that would later have to be transferred. If the project joins a foundation, its contribution agreement transfers project marks, registered or not, to the foundation's trademark holder ([ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)).

Statements in this document are not legal advice.

## Decision

1. **Third-party marks are used only descriptively and nominatively.** Examples: "for OpenTelemetry Collectors", "implements the OpAMP protocol", "runs on Kubernetes". They never appear in the project name, binary names, image names, logo, domain or organization name. No endorsement is implied. The README and the website carry a short trademark notice stating that OpenTelemetry, Kubernetes and CNCF are trademarks of The Linux Foundation and that myriarch is not affiliated with or endorsed by it.
2. **"myriarch" is not registered as a trademark before a possible foundation acceptance.** Instead:
   - first use is documented (date of the first public commit and release),
   - the project domains (`myriarch.dev`, `myriarch.io`) are held privately and are transferable,
   - the `myriarch` GitHub organization name is reserved ([ADR-0017](0017-repository-hosting-and-public-identity.md)).
3. **A professional clearance search in classes 9 and 42 must find no relevant conflict before the rename starts.** The search includes similar signs of the "Myriad" family. Otherwise the project falls back to the reserve name ([ADR-0015](0015-project-name-and-naming-scheme.md)).

Logo and branding follow the same rules ([ADR-0022](0022-white-label-and-neutral-examples.md)).

## Consequences

### Positive

- No registration cost and no later transfer of registrations.
- Consistent descriptive use of third-party marks keeps the foundation path open.
- Documented first use, domains and organization name give a practical basis for the project identity.

### Negative

- The legal position against a later conflicting registration is weaker until a registration exists.
- The project relies on documented first use as evidence.
- The clearance search is an open prerequisite and can delay the rename.

## Alternatives considered

- **Register the name now (national or EU)**: rejected. It costs money and the registration would have to be transferred if the project joins a foundation.
- **Never register**: rejected as a default. The decision is revisited at foundation onboarding.
