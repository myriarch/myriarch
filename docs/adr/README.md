---
title: "Architecture decision records"
weight: 80
description: "Index of the significant decisions about the project, recorded in MADR style."
---

This directory records significant decisions about myriarch in MADR style. Each record states the context, the decision, its consequences and the alternatives that were considered.

## Process

- A new record copies the shared template [0000-template.md](0000-template.md) with the sections Status, Date, Related, Context, Decision, Consequences (Positive and Negative) and Alternatives considered.
- It takes the next free four-digit number and is named `NNNN-kebab-title.md`.
- Status values are Proposed, Accepted, Deprecated and Superseded by ADR-NNNN. A record whose decision is replaced only in part keeps its status and names the superseding record ("Superseded in part by ADR-NNNN" or "Accepted; ... superseded by ADR-NNNN").
- Accepted records are not rewritten. A change of mind is a new record that supersedes the old one.
- Editorial changes (links, typos, front matter) are allowed and do not change a decision.
- Records are proposed via pull request with DCO sign-off ([ADR-0019](0019-developer-certificate-of-origin.md)).

## Records

The records 0001 to 0014 were written during early development, in German, inside the former architecture document. They were migrated to this directory with their original numbers and rewritten in English. Their date is 2026-10-07, the earliest date for which a record is available; the decisions themselves were taken before. The records 0015 and later were written as separate files.

Some early operational decisions were not kept as separate records. They are described in the reference text instead: one outbound proxy and CA setting for all connections, the layout of an artifact mirror without a URL template, Sigstore trust through a proxy or a TUF mirror, and configuration validation without starting the server are covered in the [security model](../security/model.md) and the [architecture](../architecture.md#artifact-cache-and-mirror-layout). The early decision to sign releases with a long-lived key is superseded by [ADR-0023](0023-keyless-release-signing.md).

| ADR | Title | Status |
|---|---|---|
| [0001](0001-single-server-binary.md) | A single server binary carries all server functions | Accepted |
| [0002](0002-postgresql-for-state-event-bus-and-leader-election.md) | PostgreSQL for state, event bus and leader election | Accepted |
| [0003](0003-gitops-as-source-of-truth.md) | GitOps is the source of truth; the UI operates within the Git frame | Accepted |
| [0004](0004-server-side-rendering-to-one-content-addressed-config.md) | Server-side rendering to one content-addressed configuration per agent | Accepted |
| [0005](0005-templating-with-double-brackets-and-typed-evaluation.md) | Templating with double brackets and typed whole-expression evaluation | Accepted |
| [0006](0006-version-updates-via-exchangeable-executors.md) | Version updates through exchangeable executors | Accepted |
| [0007](0007-package-offers-in-upstream-format-with-sigstore-bundles.md) | Package offers in the upstream draft format with Sigstore bundles | Accepted |
| [0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md) | Kubernetes: supervisor and collector in one pod | Accepted, superseded in part by [0024](0024-container-registry-and-image-set.md) (own combined image) and refined by [0026](0026-collector-version-updates-on-kubernetes.md) |
| [0009](0009-server-sent-events-for-live-ui-updates.md) | Server-sent events for live UI updates | Accepted |
| [0010](0010-standard-servemux-and-hand-written-dtos.md) | Standard ServeMux and hand-written DTOs with a contract test | Accepted |
| [0011](0011-no-containers-for-local-development-and-tests.md) | No containers for local development and tests | Accepted |
| [0012](0012-agent-access-via-enrollment-tokens.md) | Agent access through enrollment tokens | Accepted |
| [0013](0013-target-version-from-the-ui-within-the-git-frame.md) | Target version from the UI within the Git frame | Accepted |
| [0014](0014-binary-updates-in-the-pod-via-container-mode.md) | Binary updates in the pod through container mode | Accepted, refined by [0026](0026-collector-version-updates-on-kubernetes.md) |
| [0015](0015-project-name-and-naming-scheme.md) | Project name "myriarch" and naming scheme | Accepted, superseded in part by [0029](0029-host-under-the-myriarch-organization.md) |
| [0016](0016-trademark-usage-and-name-protection.md) | Trademark usage and protection of the project name | Accepted |
| [0017](0017-repository-hosting-and-public-identity.md) | Repository hosting, maintainer identity and later move to a neutral organization | Superseded in part by [0029](0029-host-under-the-myriarch-organization.md) |
| [0018](0018-license-and-copyright.md) | License: Apache-2.0 for code, CC-BY-4.0 for documentation | Accepted |
| [0019](0019-developer-certificate-of-origin.md) | Contribution model: Developer Certificate of Origin, no CLA | Accepted |
| [0020](0020-english-as-project-language.md) | English as the language of documentation and project communication | Accepted |
| [0021](0021-publication-gates-and-v0-1-0-scope.md) | Publication gates and scope of the first release (v0.1.0) | Accepted |
| [0022](0022-white-label-and-neutral-examples.md) | White-label meaning and vendor-neutral examples | Accepted |
| [0023](0023-keyless-release-signing.md) | Keyless release signing with Sigstore | Accepted, superseded in part by [0029](0029-host-under-the-myriarch-organization.md) |
| [0024](0024-container-registry-and-image-set.md) | Container registry (ghcr.io) and published image set | Accepted, superseded in part by [0029](0029-host-under-the-myriarch-organization.md) |
| [0025](0025-login-without-enterprise-idp.md) | Login without an enterprise identity provider: bootstrap admin | Accepted |
| [0026](0026-collector-version-updates-on-kubernetes.md) | Collector version updates on Kubernetes | Accepted |
| [0027](0027-mcp-principles.md) | Model Context Protocol (MCP) access: principles | Accepted, superseded in part by [0029](0029-host-under-the-myriarch-organization.md) |
| [0028](0028-relationship-to-opentelemetry-and-foundation-path.md) | Relationship to OpenTelemetry and path towards a foundation | Accepted, superseded in part by [0029](0029-host-under-the-myriarch-organization.md) |
| [0029](0029-host-under-the-myriarch-organization.md) | Host the project in the GitHub organization "myriarch" from the start | Accepted |
| [0030](0030-reuse-compliance-and-third-party-notices.md) | REUSE compliance and third-party notices | Accepted |
| [0031](0031-website-with-hugo-and-docsy.md) | Project website myriarch.dev with Hugo and Docsy, built from the repository and served as static files | Accepted |
| [0032](0032-continuous-integration-on-github-actions.md) | Continuous integration on GitHub Actions with GitHub-owned, SHA-pinned actions and one aggregate check | Accepted, amended by [0033](0033-openssf-scorecard-as-the-only-third-party-action.md) |
| [0033](0033-openssf-scorecard-as-the-only-third-party-action.md) | OpenSSF Scorecard as the only third-party action, and policy rules for the Scorecard and release workflows | Accepted |
| [0034](0034-authenticode-signing-of-windows-release-artifacts.md) | Authenticode signing of Windows release artifacts by the SignPath Foundation, with a defined fallback and an exception for v0.1.0 | Accepted |

Two later records change earlier decisions: [ADR-0024](0024-container-registry-and-image-set.md) supersedes the "own combined image" aspect of [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md) for published artifacts, and [ADR-0026](0026-collector-version-updates-on-kubernetes.md) refines [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md) and [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md) (gitops-pr as the Kubernetes default, container mode as an opt-in).
