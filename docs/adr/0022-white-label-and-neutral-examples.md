# 0022. White-label meaning and vendor-neutral examples

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0016](0016-trademark-usage-and-name-protection.md), [ADR-0018](0018-license-and-copyright.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md)

## Context

Organizations want to run the control plane under their own name and colors without forking. At the same time the project itself must not carry any organization's branding, domain vocabulary or fleet structure. Example structure, domain terms and fleet sizes can reveal a specific operator, and foundations exclude company-specific software.

Current state: the UI is embedded in the server binary. The server sets a restrictive security header set (`Content-Security-Policy: frame-ancestors 'none'` and `X-Frame-Options: DENY`, see `internal/httpserver`; the UI handler in `internal/webui` sets a stricter policy). Examples live in `examples/gitops-repo`, `examples/sim` and `gitops-template`.

## Decision

### Part 1: white-label in two stages

1. **Vendor neutrality is a publication gate** ([ADR-0021](0021-publication-gates-and-v0-1-0-scope.md)). There are no references to any specific operator. Defaults are neutral: product name myriarch, sample hostnames from reserved domains such as `example.com` and `.test`, documentation IP ranges (RFC 5737 and RFC 3849), project branding only.
2. **Runtime re-branding as an MVP after the soft launch.** Product name, logo, favicon, primary color and an environment banner are set via configuration. They are served by the server itself, with no external origins, and without loosening the CSP. This feature gets its own control audit because it renders operator-supplied content (validation of image types and sizes, no SVG scripting, escaping). Re-branding does not change binary, image or API names.

### Part 2: neutral examples

- The default quickstart uses a generic, neutral topology, for example environments and site groups with generic names and small counts.
- Domain-specific fixtures are neutralized and moved to `internal/testdata` for tests only. No industry showcase is shipped.
- Edge and branch-location deployments remain a documented use case, described in generic terms.

## Consequences

### Positive

- White-label operation without a fork.
- Neutral project identity.
- Examples that anyone can use.

### Negative

- The branding configuration adds attack surface and enlarges the test matrix.
- Neutral examples are less vivid.
- Neutrality needs a recurring check (a denylist in CI).

## Alternatives considered

- **Neutrality only, no runtime branding**: rejected, it forces operators to fork.
- **Branding only via fork or custom build**: rejected, same reason, and it fragments the project.
- **Keep the current domain-specific examples**: rejected, they tie the project to one kind of operator.
