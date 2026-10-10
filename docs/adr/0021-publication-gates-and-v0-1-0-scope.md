# 0021. Publication gates and scope of the first release (v0.1.0)

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0018](0018-license-and-copyright.md), [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0020](0020-english-as-project-language.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0025](0025-login-without-enterprise-idp.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md), [ADR-0027](0027-mcp-principles.md)

## Context

Making the repository public is irreversible. Some work must be complete before the first push, other work can follow in the open. A foundation's minimum-age clock starts with public development, so publishing early has value.

The Helm chart in the repository currently does not render with default values: it requires explicit ingress and trusted-proxy settings. A first release whose chart behaves like this, or that offers no login without an enterprise identity provider, pushes evaluators into development mode (see [ADR-0025](0025-login-without-enterprise-idp.md)).

## Decision

### Part 1: gates before the first public push (all mandatory)

- Content review: confidential, environment-specific or organization-specific data (hostnames, accounts, registries, credentials, internal names) is removed from code, docs, tests, examples and history. The public repository starts with a reviewed snapshot.
- Vendor-neutral content and examples ([ADR-0022](0022-white-label-and-neutral-examples.md)).
- LICENSE files and SPDX headers ([ADR-0018](0018-license-and-copyright.md)).
- DCO in place ([ADR-0019](0019-developer-certificate-of-origin.md)).
- Rename completed ([ADR-0015](0015-project-name-and-naming-scheme.md)).
- A control audit (defensive security review) of the published code, with findings fixed or tracked.
- Community baseline: README, CONTRIBUTING, CODE_OF_CONDUCT, SECURITY (private vulnerability reporting), MAINTAINERS and GOVERNANCE.

May follow publicly after the soft launch: English core documentation (required before the announcement, [ADR-0020](0020-english-as-project-language.md)) and runtime branding ([ADR-0022](0022-white-label-and-neutral-examples.md)).

### Part 2: contents of v0.1.0 (alpha)

- CI and signed multi-arch images ([ADR-0023](0023-keyless-release-signing.md), [ADR-0024](0024-container-registry-and-image-set.md)).
- English README and quickstart.
- A Helm chart from the repository that installs with default values.
- Login without an enterprise IdP via a bootstrap admin ([ADR-0025](0025-login-without-enterprise-idp.md)).
- Clearly stated support levels: container mode and Linux hosts are beta, Windows is experimental, the Helm chart is experimental.

Explicitly not in v0.1.0:

- Signed OCI chart publication, a kind quickstart and a collector chart (planned for v0.2.0, "Kubernetes-ready").
- The gitops-pr executor ([ADR-0026](0026-collector-version-updates-on-kubernetes.md)).
- The MCP server ([ADR-0027](0027-mcp-principles.md)).

## Consequences

### Positive

- Public development starts early with minimal irreversible risk.
- Evaluators get a usable default installation.

### Negative

- Soft launch with partially English documentation.
- Alpha support levels may deter production users.
- The gate list must be verified explicitly (checklist).

## Alternatives considered

- **Complete all hardening, docs and branding first**: rejected, it delays public development without reducing risk.
- **Content review only**: rejected, it publishes without licenses and without an audit.
- **Minimal release without chart and login**: rejected, it leads into the development-mode trap.
- **Full release including gitops-pr and MCP**: rejected, it delays the announcement.
- **Kubernetes-ready scope as v0.1.0**: deferred to v0.2.0; it delays the announcement without helping the minimum-age clock.
