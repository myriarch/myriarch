# 0012. Agent access through enrollment tokens

- Status: Accepted, refined by [ADR-0046](0046-host-installation-from-the-server.md)
- Date: 2026-10-07
- Related: [ADR-0025](0025-login-without-enterprise-idp.md), [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [Security model](../security/model.md#agent-authentication)

## Context

Thousands of supervisors and updaters connect to the OpAMP endpoint. Whoever can connect receives configuration and, through package offers, can be told to install software. Access must be controllable per group of agents, revocable, and cheap to operate on hosts that are provisioned by scripts.

The OpAMP specification offers connection settings that let a server hand out new credentials. The upstream supervisor keeps such settings only in memory and disables TLS verification when no certificate is configured ([upstream notes](../design/upstream-notes.md#remote-configuration-handling)).

## Decision

Agents authenticate with enrollment tokens, optionally combined with mutual TLS. The server does not rotate credentials over OpAMP.

- An enrollment token (`myre_` followed by 32 random base62 characters) is sent as a bearer token. Only its SHA-256 hash and a display prefix are stored. A token can be revoked, can expire and can carry labels (for example a channel or region). The intended granularity is one token per rollout channel or region, not per host. Tokens can also be declared in the server configuration, for example from mounted Kubernetes Secrets.
- Optional mTLS: the server terminates TLS with a client CA, or takes the client certificate from a trusted proxy. With `opamp.requireEnrollmentToken` set, the token is required even when a certificate is present.
- The instance UID of an agent is bound to the credential of its first registration, so another credential cannot take over an identity.
- Revocation takes effect at once on all replicas.
- Labels in the namespace `labels.*` belong to the server. Attributes that an agent reports under this prefix are discarded.
- Credential rotation is an operator action: create a new token, deploy it, revoke the old one. An agent whose token was revoked can be moved to a new one with an admin action, or automatically if `opamp.autoRebindOnTokenRotation` is enabled.

The details and limits are in the [security model](../security/model.md#agent-authentication). Human login is a separate topic ([ADR-0025](0025-login-without-enterprise-idp.md)).

## Consequences

### Positive

- Simple to provision with scripts, supervisors and the Helm chart.
- Revocable and scoped, and every token is auditable.
- Compatible with the stricter option of mTLS where an organization runs a PKI.

### Negative

- A bearer token is as strong as the place it is stored on the host; the installer restricts its file permissions.
- Rotation is manual, because the server pushes no new credentials over OpAMP.
- Attribute-based fleet selectors are a convenience, not an access boundary: attributes other than `labels.*` are self-reported by the host.

## Alternatives considered

- **Rotation of credentials through OpAMP connection settings**: not used in the first releases. The upstream supervisor does not persist offered settings and disables TLS verification when it has no certificate.
