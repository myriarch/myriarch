# 0027. Model Context Protocol (MCP) access: principles

- Status: Accepted; the MCP registry name is superseded by [ADR-0029](0029-host-under-the-myriarch-organization.md) (io.github.myriarch/myriarch)
- Date: 2026-10-08
- Related: [ADR-0017](0017-repository-hosting-and-public-identity.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0025](0025-login-without-enterprise-idp.md), [ADR-0029](0029-host-under-the-myriarch-organization.md)

## Context

Operators want to query, and eventually operate, the fleet from AI assistants through the Model Context Protocol (MCP). No MCP code exists yet. This ADR fixes principles only; a detailed design ADR follows together with the implementation. MCP is not part of v0.1.0 ([ADR-0021](0021-publication-gates-and-v0-1-0-scope.md)).

Relevant forces:

- The MCP authorization specification (revision 2026-07-28) forbids token passthrough. A remote MCP server that acts with a shared token would be a confused deputy, and the audit log would lose the acting human.
- Current API tokens are automation tokens with a fixed role. A later change introduces personal tokens with an `mcp` purpose that act as their owner.
- Agent-reported strings (descriptions, attributes, health messages) reach the model as tool output and are a source of prompt injection.
- An MCP channel is a label, not a boundary: with a local stdio transport, only the token's purpose identifies the channel.

## Decision

We build MCP access in stages over one shared tool catalog.

1. **Stage 1: a separate binary `myriarch-mcp` over stdio, read-only.** It authenticates with a personal API token that has a dedicated `mcp` purpose (a new token purpose introduced by that change) and acts strictly as the token owner.
2. **Later stage: an embedded `/mcp` endpoint** with OAuth 2.1 according to the MCP authorization specification, without token passthrough.

Further rules:

- **Write tools come only after channel-independent controls exist:** four-eyes approval for risky operations across all channels (UI, API, CLI, MCP), a distinct audit channel attribute, rate limits, a dry-run mode and a control audit.
- **Rollout wave approvals are never possible through MCP.**
- Because the channel is a label for audit and policy and not a security boundary, hard controls must hold regardless of channel.
- Tool outputs mark agent-supplied text as untrusted data.
- The registry name is `io.github.gilfy/myriarch` (see [ADR-0017](0017-repository-hosting-and-public-identity.md)).

The details (tool catalog, token purpose, audit attributes, transport behavior) are specified in a later ADR.

## Consequences

### Positive

- Useful read access early, with minimal risk.
- Humans stay accountable in the audit log because every call runs as the token owner.
- All clients (UI, API, CLI, MCP) are subject to the same controls.

### Negative

- Write automation waits for four-eyes approval and the audit features.
- The stdio binary needs a local installation, and users must handle the token themselves.
- Eventually two transports have to be maintained.

## Alternatives considered

- **stdio binary only**: rejected as a long-term answer because it offers no central OAuth sign-in.
- **Embedded `/mcp` endpoint only, now**: rejected for now because it needs an OAuth authorization server first.
- **A remote binary with a shared token**: rejected as a confused deputy that erases the acting human from the audit log.
