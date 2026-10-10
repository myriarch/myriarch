# 0024. Container registry (ghcr.io) and published image set

- Status: Accepted; the registry owner and the planned path change are superseded by [ADR-0029](0029-host-under-the-myriarch-organization.md)
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md), [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md), [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md), [ADR-0029](0029-host-under-the-myriarch-organization.md)

## Context

The current build produces two kinds of images:

- A server image containing the server binary, the load simulator and the CLI. `make image-crane` builds it without a container runtime, `FROM scratch`. The Dockerfiles in `deploy/docker` build separate per-binary images (server and simulator on distroless static, the CLI on a curl base image).
- A combined collector image (`make collector-image-crane`) that bundles the upstream OpAMP supervisor, `otelcol-contrib` of the same version and the host updater (`myriarch-updater`, [ADR-0015](0015-project-name-and-naming-scheme.md)).

Publishing the combined image means redistributing the content of the upstream supervisor image. That image is Alpine-based and contains packages under GPL-2.0, which adds license-compliance obligations (license texts, source offers). Redistributing the contrib collector binaries also raises trademark questions. The upstream `otelcol-contrib` image is `FROM scratch` and has no shell, so an init container based on it cannot use `cp` to hand its binary to another container.

A public project also needs one place where images, the Helm chart, signatures, SBOMs and attestations live together, without long-lived registry credentials in the release workflow.

## Decision

**Registry.** At the start we publish only to the GitHub Container Registry, under `ghcr.io/gilfy/...` (see [ADR-0017](0017-repository-hosting-and-public-identity.md)).

- The release workflow authenticates with its built-in `GITHUB_TOKEN`; no long-lived registry secret exists.
- Images, the Helm chart (as an OCI artifact), signatures, SBOMs and attestations are stored together as OCI artifacts in the same registry ([ADR-0023](0023-keyless-release-signing.md)).
- Public packages are free of charge.
- Further mirrors (for example Docker Hub or quay.io) may be added later in the release workflow with `crane copy` and `cosign copy`. The documentation explains how to mirror images into a private registry while keeping the signatures verifiable.

**Image set.**

| Image | Content |
|---|---|
| `ghcr.io/gilfy/myriarch` | The server; CLI and simulator where useful |
| `ghcr.io/gilfy/myriarch-updater` | The updater binary |

- Both images are built `FROM scratch` and are multi-arch (`linux/amd64`, `linux/arm64`).
- There is no combined collector image. The supervisor and the collector stay unmodified upstream images.
- In Kubernetes, an init container places the updater binary into a shared volume (`myriarch-updater install-self`). Collector and supervisor binaries are copied into the shared volume with `myriarch-updater copy`, because the contrib image has no shell. A later change adds both subcommands.
- A combined image may be offered later only as a clearly labelled community build that ships the complete upstream license texts.

This supersedes the "own combined image" part of [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md) for published artifacts. [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md) (container mode of the updater) is unaffected; see [ADR-0026](0026-collector-version-updates-on-kubernetes.md) for how updates work on Kubernetes.

## Consequences

### Positive

- Lean images with a minimal attack surface.
- No obligations from redistributing third-party image content, and no trademark questions about repackaged upstream binaries.
- A single, signed supply chain in one registry; no registry secret to leak or rotate.

### Negative

- Users can pull anonymously from ghcr.io only; there is no other public mirror at the start.
- Composing supervisor, collector and updater in Kubernetes needs init containers and shared volumes, which makes the Helm chart more complex.
- Mirroring into private registries is left to users at first (documented, not automated).
- Image paths change with the move to the new organization.

## Alternatives considered

- **ghcr.io plus Docker Hub at the start**: anonymous pulls are rate-limited and an extra secret is needed in the release workflow. Deferred.
- **ghcr.io plus quay.io at the start**: needs an extra secret. Deferred.
- **A combined collector image now**: rejected because of license compliance and trademark concerns. It may return as a labelled community build.
- **No images, build instructions only**: rejected; it makes evaluation and Kubernetes installation needlessly hard.
