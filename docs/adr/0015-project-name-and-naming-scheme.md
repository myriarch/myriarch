# 0015. Project name "myriarch" and naming scheme

- Status: Accepted; the owner-specific identifiers (Go module, container images) are superseded by [ADR-0029](0029-host-under-the-myriarch-organization.md)
- Date: 2026-10-08
- Related: [ADR-0016](0016-trademark-usage-and-name-protection.md), [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0018](0018-license-and-copyright.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0027](0027-mcp-principles.md), [ADR-0029](0029-host-under-the-myriarch-organization.md)

## Context

The project grew under a working name that is unsuitable for a public release:

- The Go module, the binaries, the environment variable prefix and the telemetry attribute keys carried the former working name or abbreviations of it.
- The GitHub organization with the working name belongs to an unrelated, active project in the same domain.
- The working name embeds an abbreviation of a third-party trademark (details in [ADR-0016](0016-trademark-usage-and-name-protection.md)).
- The former server and CLI binary names collide with binaries of existing fleet-management tools, which makes packaging and shell completion ambiguous.

A rename after the first release would break the module path, image and chart references, the signing identity and existing installations. It therefore has to happen before the first public release.

## Decision

The project is called **myriarch**, from Ancient Greek *myriarchos*, "commander of ten thousand": one control plane directing tens of thousands of collectors.

- **Pronunciation:** "MIR-ee-ark". The README states it.
- **Tagline:** "Command ten thousand OpenTelemetry Collectors from one GitOps-governed control plane."
- **Spelling:** lowercase in identifiers; "Myriarch" at the start of a sentence and in the copyright line "The Myriarch Authors".

Naming scheme:

| Item | Name |
|---|---|
| Server (OpAMP server, REST API, UI, rollout engine) | `myriarchd` |
| CLI (`validate`, `render`) | `myrctl` (short alias `myr`) |
| Host agent for collector binary updates | `myriarch-updater` |
| Load simulator | `myriarch-sim` |
| MCP server | `myriarch-mcp` ([ADR-0027](0027-mcp-principles.md)) |
| Go module | `github.com/gilfy/myriarch` (optional vanity path `go.myriarch.dev/myriarch`, see [ADR-0017](0017-repository-hosting-and-public-identity.md)) |
| Container images | `ghcr.io/gilfy/myriarch`, `ghcr.io/gilfy/myriarch-updater` ([ADR-0024](0024-container-registry-and-image-set.md)) |
| Helm chart | `myriarch` |
| Environment variable prefix | `MYRIARCH_` |
| Attribute namespace | `dev.myriarch.*` (reverse DNS of the project domain) |
| Kubernetes label and annotation domain | `myriarch.dev/` |
| Copyright line | "The Myriarch Authors" ([ADR-0018](0018-license-and-copyright.md)) |

The rename was carried out in one step (imports, configuration, documentation, UI strings, tests, scripts) before the first public release. No compatibility aliases for the former names are kept.

A reserve name with a free namespace was kept in case the trademark clearance search ([ADR-0016](0016-trademark-usage-and-name-protection.md)) found a conflict. It was not needed.

## Consequences

### Positive

- The name is distinctive and was free of collisions on GitHub, npm, PyPI, crates.io, Docker Hub, Homebrew and Debian at decision time.
- One consistent scheme covers binaries, images, chart, environment variables, attributes and labels.
- Reverse-DNS attribute and label namespaces avoid collisions with OpenTelemetry semantic conventions and with other tools.
- No compatibility layer for old names has to be maintained.

### Negative

- The rename was a large mechanical change across imports, configuration, documentation, UI strings and tests.
- An unusual word needs a pronunciation hint.
- `myrctl` is less self-explanatory than a name that spells out its purpose.
- The clearance search found no conflict ([ADR-0029](0029-host-under-the-myriarch-organization.md)), so the reserve name was not needed. Had it failed, the rename would have been repeated with the reserve name.

## Alternatives considered

- **Keep the former working name**: rejected. The organization name is taken, it embeds a third-party mark abbreviation and the binary names collide with existing tools.
- **Rename shortly before v1.0**: rejected. It would be a breaking change for early adopters (module path, images, signing identity, attribute keys).
- **Other candidates from a naming round**: dropped because they collide with registered marks or with same-named tools in the same domain.
