# 0018. License: Apache-2.0 for code, CC-BY-4.0 for documentation

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)

## Context

The repository currently has no LICENSE file and no SPDX headers.

Foundations in the cloud-native space, such as the CNCF, require Apache-2.0 for code already before acceptance and do not accept a later switch. The OpenTelemetry ecosystem the project integrates with uses Apache-2.0. An explicit patent grant matters for infrastructure software.

## Decision

- **Code:** all source code (Go, TypeScript, shell, Helm templates, build files) is licensed under Apache-2.0.
- **Documentation:** `docs/**`, website text and ADRs are licensed under CC-BY-4.0. Code snippets inside documentation are Apache-2.0; this is stated explicitly in the documentation license notice.
- **Copyright line:** "Copyright The Myriarch Authors", with no years and no individual names. Authorship is tracked by Git history.
- **Headers:** every source file carries `SPDX-License-Identifier: Apache-2.0` and the copyright line. A CI check enforces this.
- **No project NOTICE file.** Section 4(d) of Apache-2.0 would oblige every redistributor to carry it, which conflicts with white-label use ([ADR-0022](0022-white-label-and-neutral-examples.md)).
- **Dependencies:** third-party licenses are tracked separately (license report and SBOM). Bundled notices of dependencies are honored where their licenses require it.
- **Generated code and vendored assets** keep their original license headers.

## Consequences

### Positive

- Foundation-compatible from the first release ([ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md)).
- Explicit patent grant and alignment with the ecosystem.
- Permissive for embedders and white-label builds.

### Negative

- Every file needs a header: a one-time change plus a CI guard.
- Dual licensing of code and documentation requires care for snippets in documentation, which is why they are declared Apache-2.0.

## Alternatives considered

- **MIT**: no explicit patent grant and not accepted by the target foundation.
- **MPL-2.0 or other copyleft licenses**: file-level or stronger copyleft deters embedders and is not accepted by the target foundation.
