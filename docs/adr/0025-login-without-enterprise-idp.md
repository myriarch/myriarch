# 0025. Login without an enterprise identity provider: bootstrap admin

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0015](0015-project-name-and-naming-scheme.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0012](0012-agent-access-via-enrollment-tokens.md)

## Context

The server binary (`myriarchd`, [ADR-0015](0015-project-name-and-naming-scheme.md)) has two run modes:

- `myriarchd dev` is a local developer mode. It seeds local users (configuration `auth.devLogin`) and a development enrollment token with well-known values, and it logs a recurring warning banner (`cmd/myriarchd/devguard.go`). It refuses non-local settings unless explicitly overridden.
- `myriarchd serve` is the production mode. The Helm chart starts it with `args: ["serve", ...]`, and it authenticates humans through OIDC.

Evaluators who have no enterprise identity provider therefore either cannot log in to serve mode at all, or fall back to dev mode with known credentials, which is unsafe as soon as the server is reachable from a network.

## Decision

- Serve mode supports a **bootstrap admin** (break-glass) account. Its credential is read from a secret, either a file or an environment reference such as a Kubernetes Secret, and never from inline configuration.
- The account is meant for initial setup and emergencies. Every use is audited, and its use is clearly visible in the UI and the audit log. It should be disabled once OIDC is configured.
- The quickstart ships Dex as an example OIDC provider.
- A startup check makes serve mode refuse to start when no login path is configured, that is, neither OIDC nor the bootstrap admin.
- Dev mode is never used by the Helm chart or by any production example.
- The bootstrap admin gets its own control audit before release. It covers credential storage as a hash, rate limiting, lockout or backoff, session handling and audit events.
- There is no general local user management.

A later change implements the bootstrap admin and the startup check.

## Consequences

### Positive

- The default installation has a safe login without external dependencies.
- Dev mode stays a local developer tool.
- A misconfigured server fails at startup with a clear message instead of running with no way to log in.

### Negative

- One more credential must be protected and rotated.
- Break-glass accounts are an attractive target and need monitoring.
- Dex adds a component to the quickstart.

## Alternatives considered

- **OIDC only, with Dex in the quickstart**: rejected because there would still be no login path without running an identity provider.
- **Local user accounts**: rejected because they create a permanent attack surface and a password lifecycle (reset, rotation, policy) the project does not want to own.
