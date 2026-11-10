# 0050. Tagline backed by the documentation

- Status: Proposed
- Date: 2026-11-10
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0016](0016-trademark-usage-and-name-protection.md), [ADR-0031](0031-website-with-hugo-and-docsy.md)

## Context

[ADR-0015](0015-project-name-and-naming-scheme.md) sets the tagline "Command ten thousand OpenTelemetry Collectors from one GitOps-governed control plane." and explains the name as "one control plane directing tens of thousands of collectors". The tagline is drawn on the Open Graph image of every page of the website and on the image prepared for the GitHub social preview (`assets/brand/social-preview.png`), and it is the meta description of the web UI.

The [architecture document](../architecture.md#scaling) gives 1,000 collectors plus 1,000 updaters, with headroom for 5,000 plus 5,000, and says that the numbers are design targets, not measured limits. The number 10,000 appears only as the default connection limit of one replica (`opamp.maxConnectionsPerReplica`), which counts updaters as well. On a card the line reads as a capacity claim, and the explanation of the name in ADR-0015 makes the same claim.

## Decision

We use the tagline "A GitOps-governed fleet manager for OpenTelemetry Collectors, built on OpAMP." It is the line of the README and the H1 of the landing page. It replaces the tagline item of [ADR-0015](0015-project-name-and-naming-scheme.md) everywhere the tagline is used: on the social images of the website and of the GitHub repository and in the description of the web UI.

The name keeps its etymology: *myriarchos*, "commander of ten thousand", as the brand guide explains it. This record also replaces the explanation of the name in [ADR-0015](0015-project-name-and-naming-scheme.md), "one control plane directing tens of thousands of collectors": the name states no number of collectors. A number of collectors appears only with the caveat of the documentation: designed for around 1,000, with headroom up to 5,000, as design targets, not measured limits.

## Consequences

### Positive

- The cards agree with the pages they preview and with the README and the GitHub description, and no card states a number of collectors that the documentation does not back.

### Negative

- The wordplay of the line is gone.
- Platforms show the old card until they scrape the page again.
- Once the record is accepted, ADR-0015 and the index must name it, for the tagline item and for the explanation of the name.

## Alternatives considered

- **Keep the tagline**: it reads as a capacity claim.
- **Keep it in the brand guide only and use another line on the cards**: two taglines, and still a record.
- **A line with a number**: a number on a card loses its caveat.
