---
title: "Security model"
weight: 10
description: "Trust boundaries and security controls of myriarchd, its agents and the myriarch-updater, with the setting and default of each control and the risk that remains."
---

This page describes what the system does to protect itself and what is left to the operator. For each area it names the control, the setting that governs it with its default, and the residual risk. It is a description of controls, not a guide for attackers. Settings are keys of the `myriarchd` configuration file (`myriarchd.yaml`); keys can also be set through environment variables named `MYRIARCH_<PATH>` (for example `MYRIARCH_SERVER_PUBLICURL`).

To report a vulnerability, see [SECURITY.md](../../SECURITY.md). Sign-in is covered in depth by [Authentication](../operations/authentication.md), and the settings to check before going live are in the [production checklist](../operations/production-checklist.md).

## Scope and trust boundaries

| Boundary | Who is on the other side | What protects it |
|---|---|---|
| Agents to server (OpAMP, `/v1/opamp`) | Supervisors and updaters on hosts and in pods | Enrollment tokens, optional mutual TLS, identity binding, limits ([Agent authentication](#agent-authentication), [OpAMP interface limits](#opamp-interface-limits)) |
| People and automation to server (UI, REST) | Operators, scripts, CI | Sign-in, sessions, API tokens, roles, CSRF protection, rate limits, audit ([Users and automation](#users-and-automation)) |
| Git repository to server | Authors of declarative objects | Review in Git, validation of the whole revision, input limits ([GitOps input limits](#gitops-input-limits)) |
| Server to the outside | Git hosts, identity provider, release sources, mirrors, webhook targets | One outbound policy for every connection ([Outbound connections](#outbound-connections)) |
| Server to hosts (software delivery) | Hosts that run `myriarch-updater` | Checksums, Sigstore verification, signed download tokens, host-side checks ([Artifact verification](#artifact-verification), [Host-side checks of the updater](#host-side-checks-of-the-updater)) |
| Network inside Kubernetes | Other pods | NetworkPolicy of the Helm chart ([Kubernetes network policy](#kubernetes-network-policy)) |

Assumptions: the operator controls the Git repository, the PostgreSQL database and the deployment of `myriarchd`. A host is trusted only as far as its own administrators are. The collector configuration that the server sends is code that runs on the host with the rights of the collector's service account.

## Token formats

Three kinds of tokens exist. Their prefixes are fixed so that secret scanners can find them.

| Token | Format | Used for | Storage |
|---|---|---|---|
| API token | `myra_` followed by 32 base62 characters | Scripts and CI calling the REST API | SHA-256 hash and a display prefix of 12 characters |
| Enrollment token | `myre_` followed by 32 base62 characters (declarative tokens read from a file allow 32 to 120 characters from letters, digits, `_` and `-`) | Agents connecting to the OpAMP endpoint | SHA-256 hash and a display prefix of 12 characters |
| Download token | `myrd_`, a base64url payload, a dot and a base64url HMAC-SHA256 signature | Downloads of one artifact by one updater while its package offer is pending, until the token expires (`artifacts.downloadTokenTtl`) | Not stored; signed on demand |

Patterns for secret scanning of generated tokens: `myr[ae]_[0-9A-Za-z]{32}` and `myrd_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`.

The development enrollment token `myre_devtoken_insecure_do_not_use_in_prod` is intentionally weak. It exists only for `myriarchd dev`; see [Development mode](#development-mode).

## Agent authentication

| Control | Setting and default |
|---|---|
| Agents must present a valid enrollment token as `Authorization: Bearer <token>`. | `opamp.requireEnrollmentToken`, default `true`. Production refuses `false` unless `opamp.requireClientCert` is set. |
| Tokens can be revoked, can expire and carry labels (for example `channel=edge-eu`). Use one token per rollout channel or region, not per host. | Managed in the UI and API, or declared in `opamp.enrollmentTokens` (token read from `tokenFile`). |
| Mutual TLS is optional. `myriarchd` terminates TLS with a client CA, or takes the certificate from a trusted proxy header. | `server.tls.certFile`, `server.tls.keyFile`, `server.tls.clientCaFile`; `opamp.requireClientCert` (default `false`); `opamp.clientCertHeader` (empty). With a header, `server.trustedProxies` must list the proxy. |
| With `opamp.requireEnrollmentToken=true` the token is mandatory even when a certificate is presented (token and certificate). Pure mutual TLS is `requireEnrollmentToken=false` together with `requireClientCert=true`. | As above. |
| The instance UID of an agent is bound to the credential of its first registration (the enrollment token and, when present, the certificate subject). A connection with another credential for the same UID is answered with an error, closed and recorded as the agent event `identity_conflict` and the audit entry `agent.identity_conflict`. The agent's data stays unchanged. This holds for WebSocket and for every HTTP poll. | Always on. An admin releases the binding with `POST /agents/{id}/actions/reenroll` (audit `agent.reenroll`). |
| An agent whose bound token was revoked or has expired can move to a new valid token (with the same certificate subject) without an admin action, if allowed. Each move is audited as `agent.rebind`. | `opamp.autoRebindOnTokenRotation`, default `false`. |
| Revocation applies at once. On revoking or expiring a token, the server publishes `enrollment_token.revoked`; every replica clears its token cache, closes the connections of that token and drops pooled HTTP sessions (audit `agent.disconnect`). Each replica also checks the tokens of its open connections against the database every minute, in case an event is lost. | Always on. |
| The namespace `labels.*` belongs to the server (enrollment token labels, inventory). Attributes reported by an agent under this prefix are discarded with a warning event. | Always on. |
| An updater is linked to a collector only if both enrolled with the same token (or certificate) and, when both report a host name, the same `host.name`. The link is checked when reading and before an offer is made. Several online updaters for one collector are ambiguous and the target fails with "ambiguous updater link". | Always on. |
| The server never sends OpAMP connection settings, because the upstream supervisor keeps them only in memory and disables TLS verification when the offer has no certificate ([upstream notes](../design/upstream-notes.md#remote-configuration-handling)). Credentials are rotated by creating a new token, deploying it and revoking the old one. | Not configurable. |

Residual risk:

- A bearer token is as secret as the place it is stored on the host. The installers write the files that hold it readable only by `root` (Linux) or `SYSTEM` and administrators (Windows). The supervisor receives the token through its service environment, and the collector runs under the same service account and inherits that environment, so the collector account can read it (see [Known limitations](#known-limitations)).
- Attributes other than `labels.*` (for example `site.*`, `host.*`) are reported by the agent from local host configuration. They are only as trustworthy as the host. Fleets that must stay separated should be selected by `labels.*` (from enrollment tokens, one token per channel or region), not by self-reported attributes. Attribute selectors are a convenience for assignment, not an access boundary.
- Behind a proxy or ingress the per-address limits only work if `server.trustedProxies` lists it; otherwise all agents share the address of the proxy. Production refuses a range that covers every address (`0.0.0.0/0`, `::/0`) in `server.trustedProxies`, because forwarded client addresses and the certificate header would then be trusted from any peer. A range shorter than /8 (IPv4) or /32 (IPv6) is reported as a warning at start, and refused while `opamp.clientCertHeader` is set.

## OpAMP interface limits

| Limit | Setting and default |
|---|---|
| Message size | `opamp.maxMessageSize`, default 4 MiB. |
| WebSocket compression (permessage-deflate) is off, because the library limits only the compressed frame and a decompressed message would not be covered by the size limit. HTTP gzip bodies are limited after decompression. Production refuses `enableCompression: true` unless `allowUnboundedCompression: true` is set as well (then a warning is logged at start). | `opamp.enableCompression`, `opamp.allowUnboundedCompression`, both `false`. |
| Stored effective configuration per agent. Larger bodies are not stored and raise a warning event. | `opamp.maxEffectiveConfigBytes`, default 1 MiB. |
| Structure of a message: hashes up to 64 bytes, up to 16 metadata entries per component, bounded depth of health trees and bounded counts of packages and component kinds. A single agent may store about six new configuration or component sets in a row, then one every 10 seconds. | Fixed in code. |
| New connections per client address, applied before authentication. IPv6 clients share one bucket per /64 prefix and an IPv4-mapped address counts as the IPv4 address, like in every authentication failure limit. | `opamp.connectRatePerIp`, default 50 per second (burst twice that). |
| Lookups of enrollment tokens. Only tokens of the documented form (`myre_` and 32 to 120 letters, digits, `_`, `-`) reach the cache and the database. Unknown tokens are remembered for 10 seconds in a separate, smaller cache, so a stream of unknown tokens cannot evict valid tokens from the cache that absorbs reconnect storms. Concurrent lookups of one token share one database round trip, and lookups of tokens without a cache entry are limited per replica to 100 per second (burst 200). Beyond that the answer is `429` with `Retry-After` (metric `myriarch_auth_agent_requests_total{result="rate_limited"}`). | Fixed in code. |
| Usage statistics of enrollment tokens (use count, last use) are written every 5 seconds with one statement per token. | Fixed in code. |
| New authenticated connections per second and replica. The bucket is charged only after successful authentication, so unauthenticated requests cannot use it up. Excess gets `429` with `Retry-After`. | `opamp.connectRateLimit`, default 200. |
| Concurrent WebSocket connections per token and replica. | `opamp.maxConnectionsPerToken`, default 0 (unlimited). |
| Connections per replica; above the limit new connections get `503` with `Retry-After`. Pooled HTTP sessions are bounded as well (least recently used are evicted). | `opamp.maxConnectionsPerReplica`, default 10000. |
| A WebSocket connection must send its first message within 30 seconds and is closed after twice `opamp.offlineAfter` without a message. | Fixed first-message timeout; `opamp.offlineAfter`, default 90 seconds. |

Residual risk: a large number of authenticated agents can still load a replica up to the configured limits. Size the limits to the fleet (see [Scaling](../architecture.md#scaling)).

## Artifact verification

`myriarchd` caches collector (and supervisor) release artifacts and offers them to updaters. Verification happens on the server and again on the host.

| Control | Setting and default |
|---|---|
| The SHA-256 of every artifact is checked against the per-asset checksum file, and the Sigstore bundle of the asset is verified against the certificate identity of the upstream release workflow and the OIDC issuer `https://token.actions.githubusercontent.com`. The tag in the identity must equal the artifact version. | `artifacts.verify.requireChecksum` and `artifacts.verify.requireSignature`, both `true`. Production refuses `false` unless `artifacts.allowUnverified: true` (lab use, warning at start). `artifacts.verify.certificateIdentityRegexp`, `artifacts.verify.certificateOidcIssuer`. |
| Only artifacts in status `ready`, with a 32-byte SHA-256 and (when required) a verified bundle are offered. | Always on. |
| The cache is content-addressed under `<dir>/sha256/<hex>`. Before serving, a file is checked once per process and file state against its pinned SHA-256 (opened without following symbolic links); a changed file is discarded and fetched again. Deleting an artifact removes the file on all replicas. | `artifacts.dir`; `artifacts.maxCacheBytes`, default 20 GiB (least recently used versions are evicted). |
| The source URL is derived from the current configuration, not from the stored record. Redirects are limited to 5 hops, https and the allowed hosts (the GitHub asset hosts plus the host of the source base URL by default). Downloads abort after 60 seconds without data, and at most 3 run at the same time per replica. | `artifacts.sourceBaseUrl`, `artifacts.allowedRedirectHosts`. |
| Download tokens are bound to one updater and one artifact, are accepted only while that updater has a package offer for the artifact, and expire. They are bearer tokens: while the offer is pending a token works for repeated downloads of that artifact until it expires. The token is created when the offer is sent (it is not part of the package hash) and renewed after half of its lifetime for an offer that is not finished yet. | `artifacts.downloadTokenTtl`, default 24 hours; production requires a positive value. The signing key comes from `auth.downloadTokenSecretFile` (32 or more random bytes); if empty, a key is generated and stored in the database. Set the file in production so that all replicas and restarts use a key you control. |
| The download base URL can differ per updater, chosen from an allowlist; free-form URLs are not possible. | `artifacts.agentBaseUrls` (absolute `http(s)://host[:port]` without path, query, fragment or credentials). Production allows `http://` entries only with `artifacts.allowInsecureAgentBaseUrls`. |
| Mirror credentials are sent only to the same origin (scheme, host, port) as `artifacts.sourceBaseUrl`, also after redirects, and never appear in logs or errors. They are read again when the file changes. | `artifacts.sourceAuth` with `tokenFile` or `username` and `passwordFile`. |

Sigstore trust. Without `artifacts.verify.trustedRootFile` the server fetches the Sigstore trusted root through TUF, using the same checked HTTP client as all other outbound connections (proxy, `outbound.caFile`, `outbound.allowedCidrs`; mirror credentials only for the same origin). The default repository is `https://tuf-repo-cdn.sigstore.dev`. `artifacts.verify.tufRepositoryUrl` can point to a 1:1 mirror; the embedded Sigstore root stays the trust anchor, and the mirror must not cache `timestamp.json` for long because it changes daily. A fixed `trusted_root.json` is the fallback, and it is what hosts use for offline verification.

Mirror. Hosts without internet access can download from an internal mirror that copies the upstream release downloads one to one (`<sourceBaseUrl>/<tag>/<asset>`, including the `.sha256` and `.sigstore.json` files). The mirror needs no trust: checksum and bundle verification still happen in `myriarchd` and again on the host.

Residual risk: verification proves that an artifact is a genuine upstream release for the offered version. It does not prove that the version is the right one for a fleet; that decision is governed by Git and the operator, and by the host policy described next.

## Host-side checks of the updater

`myriarch-updater` runs with elevated rights (root or SYSTEM) because it replaces binaries and restarts services. It therefore does not rely on the server.

| Control | Setting and default |
|---|---|
| Downloads are accepted only from configured URL prefixes. | `update.allowedUrlPrefixes`; by default `<scheme>://<host>/api/v1/artifacts/` of the OpAMP endpoint, which means `server.publicUrl` must have the same host. |
| SHA-256 and the Sigstore bundle are verified on the host, offline against a trusted root file, or via TUF if none is configured. The certificate identity must end with `@refs/tags/v<offered version>`, so a valid signature of another release cannot be reused. | `update.requireSignature` (default `true`), `update.trustedRootFile`, `update.certificateIdentityRegexp`, `update.certificateOidcIssuer`. |
| Anti-downgrade: offers below the minimum version are refused, and so are versions older than the active one. Only the step back to the previous version is allowed. The check runs before the download; a refusal is reported as `INSTALL_FAILED`. The server decides the version and the signature proves only a genuine release, so the host keeps its own floor. | `update.minimumVersion` (empty), `update.allowDowngrade` (`false`). |
| A failed version is not retried for ten minutes. | `update.failedRetryBackoff`, default 10 minutes. |
| Preflight: the new collector must report its version, and the effective configuration is validated with `otelcol-contrib validate`. The configuration file belongs to the collector's service account, so the check runs under that account on Linux (the file is read without following symbolic links, only as a regular file, size-limited). Where privileges cannot be dropped (Windows), a bounded copy is checked only if it uses no config provider other than `env`. The output of `validate` stays in the local debug log and is not sent to the server. The check runs in its own process group with a time limit; the unit keeps `CAP_KILL` for that. | `update.preflightValidate` (default `true`), `update.preflightTimeout` (60 seconds). |
| Health check after the swap and automatic rollback to the previous version. | `update.healthTimeout` (120 seconds), `update.healthInterval` (5 seconds), `update.healthSuccesses` (3). |
| Size limits for downloads and binaries, and an idle timeout. | `update.maxDownloadBytes`, `update.maxBinaryBytes` (1 GiB each), `update.downloadIdleTimeout` (60 seconds). |
| Plain `ws://` or `http://` to the server is refused (loopback excepted), because the token would travel in clear text. | `server.allowInsecureTransport` in the updater configuration, for lab setups only. |

Least privilege on the host:

- Supervisor and collector run under their own service account: the user `otelcol` on Linux and the virtual account `NT SERVICE\otelcol-supervisor` on Windows (not LocalSystem), because remote configuration runs with these rights. Only `myriarch-updater` is privileged.
- Its systemd unit is hardened: `ProtectSystem=strict`, a reduced capability bounding set, and restricted namespaces and kernel interfaces.
- The unit of the supervisor and collector (`otelcol-supervisor.service`) runs Git-declared configuration with the rights of its account, so it drops all capabilities (`CapabilityBoundingSet=` and `AmbientCapabilities=` empty) and sets `RestrictSUIDSGID`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectClock`, `ProtectControlGroups`, `RestrictNamespaces`, `LockPersonality`, `SystemCallArchitectures=native` and `UMask=0027`. `ProtectSystem` stays `full` so that file exporters can write below `/var`. A receiver that needs more (a port below 1024, for example) gets a systemd drop-in with the capability it needs.
- `myriarch-updater` warns at start when the file named by `server.tokenFile` (or the configuration file that holds an inline `server.token`) is writable by group or others, readable by others, or (for the token file) owned by neither `root` nor the service account. The message names the setting and the path, never the content.
- On Windows the installer never adopts an existing directory under `C:\ProgramData\Myriarch`. If it is not owned by SYSTEM or the administrators, or is a reparse point, installation stops. Otherwise the ACL of the whole tree is reset (SYSTEM and administrators, plus targeted rights for the service account) and verified. The enrollment token is readable only by SYSTEM and administrators.

Supply chain of the installers. `deploy/scripts/install-linux.sh` and `deploy/scripts/install-windows.ps1` download only over https (also after redirects) and verify collector and supervisor with SHA-256 and a Sigstore bundle before anything runs (default `require`; verifier `cosign` or `myriarch-updater verify-bundle`). The certificate identity passed to the verifier is built for the requested version and asset (`.../base-release.yaml@refs/tags/v<version>` for the collector, `.../base-binary-release.yaml@refs/tags/v<version>` for the supervisor, end-anchored), so that a genuine but older release served from a mirror is not accepted as the requested version. Without `--insecure` the Linux installer requires `curl` (it restricts redirects to https and sets a minimum TLS version); `wget` is accepted for labs only. Archives are extracted without their owner and mode, and the installed collector binary belongs to `root`. A CA bundle passed with `--ca-file` (`-CaFile`) is copied into the protected configuration directory and referenced there, because the supervisor and the updater send the enrollment token over connections that trust it. On Windows the installer prefers the verified updater over `cosign.exe` from `PATH` (it accepts `cosign.exe` only from a directory that only administrators can write), creates its working directory with a protected DACL, hashes verified files again after copying them, resets the ACL of moved directories and, before the token is written to the service environment, replaces the ACL of the service key by SYSTEM and administrators and reads it back (any other allow entry stops the installation with `acl_failed`). The updater binary itself needs a trusted hash, either `--updater-sha256` (`-UpdaterSha256`) or the signed `checksums.txt` of the release. `--insecure` (`-Insecure`) lifts the remaining checks for labs and tests only. The direction for release signing of the project's own artifacts is keyless signing ([ADR-0023](../adr/0023-keyless-release-signing.md)).

Residual risk: the updater trusts the Sigstore root and the upstream release workflow identity. A compromise of the upstream release process is outside the control of this project. A host with a stale trusted root file cannot verify new bundles until the file is refreshed.

## Users and automation

### Sign-in

| Control | Setting and default |
|---|---|
| `myriarchd serve` refuses to start without a login path: OIDC or the bootstrap admin. The development login is refused in production. | `auth.oidc.enabled`, `auth.bootstrapAdmin.*`, `auth.devLogin.enabled` (must be `false`). |
| OIDC uses the authorization code flow with PKCE. The login state can be used once (a marker in the database shared by all replicas). Subjects with the prefix `local:` are rejected. Users are identified by provider, issuer and subject. Group changes at the identity provider take effect at the latest after the maximum session age. | `auth.oidc.*`; see [Authentication](../operations/authentication.md). |
| Roles come from groups or users in `auth.oidc.roleMapping`; the highest role wins, and an admin can override a role manually. Deny by default: a user without a mapped group gets no session (`access_denied`, audit `no_role`). The configuration check requires a role mapping or an explicitly set default role, and warns at start if `defaultRole` is set. | `auth.oidc.roleMapping`, `auth.oidc.defaultRole` (empty). |
| Client authentication at the token endpoint is a secret or a client certificate (`private_key_jwt`, RS256, assertion valid for 5 minutes, with `x5t` and `x5t#S256`), not both. Certificate files are read again when they change. | `auth.oidc.clientSecretFile` or `clientCertificateFile` and `clientKeyFile`. |
| The bootstrap admin is one configuration-managed administrator for first setup and emergencies. Its credentials come from files only (a bcrypt hash of a password, cost 10 or higher, and/or a one-time token that is consumed atomically across replicas). It is deactivated while OIDC is enabled unless kept deliberately. Every sign-in attempt is audited (`auth.bootstrap.login`), and the UI shows a banner while signed in with it. Sign-in is limited to 5 attempts per minute and client address; after 5 consecutive failures the account is refused for 1 second, doubling up to 15 minutes. The limits are kept per replica, in memory, and cleared on restart ([Authentication, section 2.7](../operations/authentication.md#27-rate-limit-and-backoff)). | `auth.bootstrapAdmin.enabled` (`false`), `username` (`admin`), `passwordHashFile`, `tokenFile`, `keepWithOidc` (`false`). Create a hash with `myriarchd auth hash-password`. |
| Sign-in attempts per client address and user name for the development login: 5 per minute with a burst of 10, then `429` with `Retry-After` (metric `myriarch_auth_logins_total{result="rate_limited"}`). IPv6 clients share one bucket per /64 prefix. The limit is kept per replica, in memory, and cleared on restart ([Authentication, section 2.7](../operations/authentication.md#27-rate-limit-and-backoff)). | Fixed in code. |
| The OIDC endpoints are public. Each failed callback of a client address (one bucket per /64 for IPv6) uses one token of a bucket of 10 that refills one token per 6 seconds; with an empty bucket both `GET /auth/login` and `GET /auth/callback` answer with a redirect to `/login?error=oidc_error` and `Retry-After`, before any state is stored and before the identity provider is contacted (metric `myriarch_auth_logins_total{method="oidc",result="rate_limited"}`). Failures before a valid signed login state was presented are audited at most once per minute and address; failures after that keep their audit entry. Responses of the identity provider (discovery, key set, token response) are limited to 1 MiB. The limit is kept per replica, in memory, and cleared on restart ([Authentication, section 2.7](../operations/authentication.md#27-rate-limit-and-backoff)). | Fixed in code. |
| Group names of `auth.oidc.roleMapping` are compared exactly, e-mail entries ignoring the case of ASCII letters only; there is no Unicode case folding. | Fixed in code. |

### Sessions, CSRF and API tokens

| Control | Setting and default |
|---|---|
| Sessions are stored in PostgreSQL. The session cookie is `HttpOnly`, `Secure` and `SameSite=Lax`; with Secure cookies (the production setting) it is named `__Host-myriarch_session` (`Path=/`, no `Domain`), so that a sibling host cannot plant a cookie of the same name for the parent domain. Where cookies are not Secure (development mode over plain HTTP) the name is `myriarch_session`. A session ends after an idle time (sliding) and after an absolute maximum age. | `auth.sessionTtl`, default 2 hours; `auth.sessionMaxAge`, default 12 hours; `auth.cookieSecure`, default `true` (production refuses `false`). |
| CSRF protection for state-changing requests uses a double-submit token: cookie `myriarch_csrf` and header `X-CSRF-Token`. Public routes that change state (sign-in) require `Content-Type: application/json` (a missing or other type gives `415`) and refuse cross-site requests based on `Sec-Fetch-Site` and `Origin`. | Always on. |
| API tokens (`myra_`) carry a fixed role. A token never has more rights than its creator (creating one with a higher role than your own gives `403`), and it stops working when the creator is disabled. A token created with another API token inherits that token's creator and never outlives its parent. Tokens have a maximum lifetime: a token created without `expiresAt` gets it, a later `expiresAt` is refused with `422`, and an older token stops working (effective expiry is the earlier of `expiresAt` and the creation time plus the maximum; the API and the web UI show this effective expiry). A token whose creator is a local development account is refused where the development login is off. A token keeps the role of its creator as stored in myriarch: removing a user from a group or from the identity provider ends the sessions of that user within `auth.sessionMaxAge`, but not the API tokens; disable the user in myriarch to end them at once ([Authentication](../operations/authentication.md#offboarding-a-user)). | Managed with `tokens:manage` (admin). `auth.apiTokenMaxTtl`, default 90 days (`0` means unlimited and logs a warning at start). |
| Logout, token revocation, disabling a user and role changes publish `auth.invalidate`; every replica clears its session and token caches at once (a 5-second cache lifetime is only the fallback). Disabling a user deletes the sessions and revokes the API tokens of that user. | Always on. |
| Live event streams (SSE) re-check their credential on every ping and after every invalidation, end after at most one hour, are limited to 10 per user or token, and deliver an event type only to principals with the matching permission; unknown event types go to nobody. | Fixed in code. |
| Failed bearer-token lookups are counted per client address and per distinct token, so a client that keeps sending one stale token is not delayed. After 5 distinct invalid tokens the address is delayed exponentially, up to 5 minutes, and audited at most once per minute (`auth.token_failed`, metric `myriarch_auth_token_failures_total`). During the delay only tokens already known to the process are checked, with no database lookups: tokens in the 5-second cache and tokens that were valid within the last 15 minutes keep working, so other clients behind the same address are not locked out. The tracker is kept per replica, in memory, and cleared on restart ([Authentication, section 2.7](../operations/authentication.md#27-rate-limit-and-backoff)). | Fixed in code. Behind an ingress, `server.trustedProxies` must list it for the real client address to be seen. The Helm chart refuses to render an ingress or HTTPRoute without `trustedProxies` unless `allowWithoutTrustedProxies` is set. |

### Roles

| Role | Rights |
|---|---|
| `viewer` | Read everything except audit log, users and tokens; manage own saved filters. |
| `operator` | Viewer, plus agent actions (restart, resync, forget an offline agent), rollout actions (pause, resume, abort, approve, rollback, retry), choosing or clearing the target version of a fleet within the Git frame (`fleets:set_version`), acknowledging releases, triggering a GitOps sync, managing artifacts, test notifications and reading the audit log. |
| `admin` | Operator, plus users, API tokens and enrollment tokens. |

The table is implemented in `internal/authz`. Declarative objects are not editable in the UI at all ([ADR-0003](../adr/0003-gitops-as-source-of-truth.md)).

### REST interface and browser

| Control | Detail |
|---|---|
| Every route declares its access decision (a permission, `public`, `custom`, `agent-enrollment`, `metrics` or `ui`). A test enumerates all patterns and checks that authenticated routes answer anonymous requests with `401`. | Always on. |
| The listeners bound what a client can hold open: every request gets 60 seconds to deliver its body and an idle write timeout of 60 seconds (every write to the client extends the deadline, so large downloads over slow links continue); WebSocket upgrades and event streams keep their own deadlines, but only after the connection is handed over or the request is authenticated: a rejected request, with or without an `Upgrade` header, keeps both budgets. Request headers are limited to 64 KiB on both listeners. `/readyz` is public: the result of the database check is cached for 2 seconds and concurrent checks are coalesced, so it costs at most one database round trip per period and replica. | Fixed in code. |
| Handlers run under a 30-second deadline (streams excepted). The database aborts statements after `database.statementTimeout` (default 15 seconds; maintenance and migrations excepted). Page size is at most 500, offset at most 100000, search text at most 200 characters, at most 50 values per filter. | Fixed except `database.statementTimeout`. |
| Errors are `application/problem+json` without internal details. A `503` carries only a request ID, and `/readyz` says only "not ready"; the cause is in the log. Database errors that become 4xx answers (not found, already exists, invalid value) carry a generic text; the operation, constraint name and driver message are logged with the request ID. | Always on. |
| `/metrics` is unauthenticated and is meant for a separate listener. With an empty `server.metricsListenAddr` it is served on the main listener, which is usually reachable through the ingress; production logs a warning at start naming the setting. | `server.metricsListenAddr`, default `:9090`. |
| Effective configurations come from the agent and may contain expanded credentials. They are redacted on the server for every role: values of keys that name credentials (such as password, token, secret, API key, authorization, private key, cookie), all header values and credentials in URLs become `[REDACTED]`; `${env:...}` references stay visible. The response carries `redacted: true`. | Always on. |
| UI responses carry a Content-Security-Policy that allows scripts only from the application itself, with no framing and connections only to the own origin, plus `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` and `Cross-Origin-Opener-Policy: same-origin`. API responses carry `frame-ancestors 'none'` and are not cached. | Always on. |
| `GET /licenses` and the subtree `GET /licenses/` are the only unauthenticated routes that serve static project text: the embedded `LICENSE` and `THIRD_PARTY_NOTICES`, as `text/plain` with `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`, `X-Frame-Options: DENY` and `Cache-Control: no-cache`. The content is public information about the build, contains no user data or configuration, and any other name or deeper path answers `404`. The route decision is `public`, and the route enumeration test covers it. | Always on. |
| `Strict-Transport-Security` (one year, `includeSubDomains`) is sent when `myriarchd` terminates TLS itself or `server.publicUrl` is https. A TLS-terminating proxy in front should keep its own HSTS enabled. A plain-HTTP listener in production requires `server.tls.terminatedUpstream: true` (TLS ends in front of `myriarchd`); a warning is logged at start. | `server.publicUrl` must be https in production. |

### Audit

Every state-changing action is recorded in `audit_events`: users, tokens, the rollout engine, GitOps (including each webhook call as `gitops.webhook`), saved filters, and the choice of a target version (`fleet.version.select` and `fleet.version.clear`, including rejected attempts with the reason). Failed and rejected rollout actions, sign-in attempts and requests with invalid bearer tokens (rate limited) are recorded too. Entries carry the request ID and the source address. Approvals (`approvedBy`) and acknowledgements (`acknowledgedBy`) are stored as stable references (`user:<uuid>`) and shown with the display name. A test enumerates all state-changing routes and requires a declared audit action for each.

Retention: `retention.audit`, default 400 days.

Residual risk: the audit log is in the same database as the data it describes. Operators who need tamper evidence should export it.

## Secrets handling

Secrets never belong in Git. The database DSN, the OIDC client secret or certificate, Git credentials, the release-tracker token, mirror credentials, webhook and AWX secrets and notification URLs come from files (for example mounted Kubernetes Secrets) or from environment variables, never from the GitOps repository.

- Files are read at start, and only for features that are active. At start the server warns about each secret file that other users of the host can read or write (outside Windows). The message names the setting and the path, never the content. The check also covers files that Git may reference through `executors.secretFileHosts` and the directory `executors.secretDir` itself (it should not be writable by group or others or readable by others; a Kubernetes Secret mount with the sticky bit is not reported). Aim for mode `0400` or `0440`.
- A secret that is set as a literal value in the configuration file (the database DSN, the OIDC client secret, the webhook secret, notification URLs) is reported at start as an inline secret; the message names the setting only. The Helm chart refuses to render such values into the ConfigMap. Credentials in `artifacts.proxyUrl` are reported too, and `myriarchd config print` redacts them in every URL-valued setting.
- In production the server warns when the database connection goes to a remote host with an `sslmode` that does not verify the server certificate (anything but `verify-ca` and `verify-full`); the message names `database.dsn`, never its value.
- Configuration values can use `${VAR}` and `${VAR:-default}`. `myriarchd config print` shows the effective configuration with secrets redacted.
- The GitOps repository names secrets only. Which environment variables and files are reachable at all is decided by the `myriarchd` configuration alone ([Executors](#executors-in-gitops-objects)).
- Collector configurations reference secrets as `${env:NAME}` or `${file:path}`, which the collector resolves locally. They never appear in the rendered configuration in clear text.
- Generic webhook notification channels need a `secretFile` in production (at least 32 bytes). Payloads are signed with HMAC-SHA256 over `Timestamp.Body` in the `X-Myriarch-Signature` header; receivers should check `X-Myriarch-Timestamp` within plus or minus 300 seconds and deduplicate by `X-Myriarch-Delivery`.
- Notification delivery runs through a bounded queue (256 entries, 4 workers, 30 seconds per delivery). When the queue overflows, notifications are dropped and counted, so they never block a leader loop.

Residual risk: whoever can read the process environment or a mounted secret file on the server can use that secret. Restrict access to the pod and the node.

## Executors in GitOps objects

Executor objects in Git configure where version-update calls go. The following controls keep Git from reaching arbitrary hosts or secrets.

| Control | Setting and default |
|---|---|
| Webhook and AWX targets must match an allowlist. An empty list refuses every target (fail closed, in every mode); the server still starts. Plain `http` needs an explicit `http://host` entry. | `executors.allowedWebhookHosts`, default empty. |
| Environment variables referenced from Git must start with a prefix. Files are read only below one directory, with relative names, no `..`, through `os.OpenInRoot`. Without `executors.secretDir`, file references are refused. | `executors.secretEnvPrefix`, default `MYRIARCH_EXEC_`; `executors.secretDir`, default empty. |
| Every secret is bound to destination hosts: through `executors.secrets` with `allowedHosts`, or through `executors.secretEnvHosts` and `executors.secretFileHosts`. In production a reference without a binding is refused, also during validation; a bound secret is sent only to its hosts, even when another host is on the allowlist. | The bindings are empty by default. |
| Webhook secrets need at least 32 bytes. Timeouts requested in Git are capped. | `executors.maxTimeout`, default 5 minutes; `executors.defaultTimeout`, default 30 seconds. |
| The AWX `limit` list is built from agent attributes. Each value must be a plain host name (`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`, not `all` or `ungrouped`), otherwise that target fails. | Fixed in code. |
| Executors and notification channels follow no redirects. | Fixed in code. |
| Secret files named by the operator in `executors.secrets` (absolute paths) are read as regular files of at most 1 MiB, like the files below `executors.secretDir`. | Fixed in code. |
| Webhook signature: `X-Myriarch-Signature` is an HMAC-SHA256 over the timestamp and the body (`X-Myriarch-Timestamp`, then a dot, then the body). It does not cover the method, the URL or other headers. Receivers should reject timestamps outside plus or minus 300 seconds and drop repeated deliveries by `X-Myriarch-Delivery` within that window ([Executors](../concepts/executors.md)). | Fixed in code. |

## GitOps input limits

| Control | Setting and default |
|---|---|
| Production accepts only `https://`, `ssh://` and `git@host:path` repository URLs without embedded credentials. Use `gitops.auth.tokenFile` or an SSH key file. | `gitops.repoUrl`, `gitops.auth.*`. |
| The clone always follows `gitops.repoUrl`: every fetch uses the configured URL, and an existing clone directory whose remote differs is deleted and cloned again, so a changed URL never applies the previous repository and credentials are never presented to it. | Always on. |
| The working directory is opened as a root: symbolic links and other non-regular files are errors and are never read. | Always on. |
| Size limits: file size (before reading), number of YAML files, total YAML size, repository size (also applied to Git HTTP responses). The clone is shallow (`depth=1`). | `gitops.maxFiles` (2000), `gitops.maxYamlBytes` (64 MiB), `gitops.maxRepoBytes` (256 MiB). |
| Time budgets: validation of one revision 2 minutes, a whole sync cycle including reassignment 10 minutes. An exhausted node budget of the strict check is an error, not a silent skip. | Fixed in code. |
| Templates have no access to the environment, files or the network. Output of one string is capped at 64 KiB, and so is every intermediate result of the helper functions. `printf` is bounded (no `*`, no argument indexes), `range` only over lists and maps, at most 3 nested ranges, at most 10,000 elements, 8 MiB of helper output and 5 seconds of compute per template string. `define`, `template` and `block` are not allowed. A rendered configuration is at most 1 MiB. Lists and maps are never printed implicitly. | Fixed in code. |
| The webhook that triggers a sync is authenticated with a shared secret (GitLab `X-Gitlab-Token` or GitHub `X-Hub-Signature-256`), rate limited to 1 request per second per replica with a burst of 10, deduplicated by delivery ID, and audited. | `gitops.webhookSecretFile`. |
| An invalid revision is never applied. The previously applied state stays active and the status shows the error. | Always on. |

## Outbound connections

Everything `myriarchd` itself connects to goes through one set of HTTP clients in `internal/netguard`: OIDC discovery and token exchange, Git over https and ssh, the GitHub API and raw files, the artifact upstream or mirror, the Sigstore TUF repository, webhook and AWX executors, and notifications.

| Control | Setting and default |
|---|---|
| The resolved address of every single connection attempt is checked, including every redirect and every new name resolution. Blocked: loopback, private networks (RFC 1918), `100.64.0.0/10`, link-local including the cloud metadata address (`169.254.0.0/16`), unique local addresses (`fc00::/7`), multicast and reserved ranges, the documentation ranges of IPv4 and IPv6, `192.88.99.0/24`, the discard prefix `100::/64`, Teredo (`2001::/32`), 6to4 (`2002::/16`) and IPv4-compatible IPv6 (`::/96`), also in IPv4-mapped and NAT64 form. Git over SSH uses the same check. Internal targets (Git server, AWX, mirror, identity provider) are opened explicitly. | `outbound.allowedCidrs`, default empty. The check is off in `myriarchd dev`. |
| Corporate proxy: the standard `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` variables are honored. Only the connection to the proxy (its exact host and port) is exempt from the address check, and only in the transport that uses that proxy (the proxy is trusted infrastructure and enforces its own egress policy); `ALL_PROXY` is not used. Because the proxy makes the connection to the target, address literals in blocked ranges, `localhost` (also with a trailing dot) and numeric host spellings that are not canonical address literals (decimal, octal, hexadecimal or shortened IPv4) in a request URL are refused locally before they are handed to the proxy; host names are left to the egress policy of the proxy. | Environment; `artifacts.proxyUrl` additionally for the artifact cache and TUF fetches. |
| Internal CA: a PEM file with additional trusted CA certificates, added to the system roots for all outbound TLS. The server refuses to start if the file is missing or has no certificate. `gitops.auth.caFile` and the `caFile` of an AWX executor add to this store, they do not replace it. The file is read at start. | `outbound.caFile`, default empty. |
| Minimum standards: TLS 1.2 or later, HTTP/2 enabled, dial timeout 10 seconds, TLS handshake 10 seconds, response headers 30 seconds, response header size 64 KiB, bounded response sizes. Redirects are limited to https and a few hops. | Fixed in code. |
| In production, source URLs with credentials (Git, release API, notifications, artifact mirror) must use https. This includes notification URLs read from `urlFile`. Plain HTTP is allowed in dev mode only. | Checked by the configuration validation. |

Residual risk: an address allowed through `outbound.allowedCidrs` is allowed for every outbound client, not only for the one you had in mind. Keep the ranges narrow.

## Kubernetes network policy

The Helm chart ships a NetworkPolicy, enabled by default (`networkPolicy.enabled`).

- Ingress to port 8080 comes from the sources in `networkPolicy.ingress.from`, from the OpenShift router when the OpenShift profile is used, and, by default, from every pod of the release namespace (`networkPolicy.ingress.allowSameNamespace: true`). Rendering fails if `allowSameNamespace` is `true` while `config.server.trustedProxies` or `config.opamp.clientCertHeader` is set, because any pod of the namespace could forge the forwarded headers. In that setup, list the permitted sources in `networkPolicy.ingress.from`.
- The metrics port 9090 (unauthenticated) is reachable only from `networkPolicy.metrics.from`; empty means nobody.
- Egress is limited to DNS and the TCP ports in `networkPolicy.egress.ports` (default 5432 and 443), the Git SSH port when `config.gitops` is enabled, and any `extraPorts` or `extraRules`. The cloud metadata range `169.254.0.0/16` is excluded. `myriarchd` additionally checks every outbound connection itself (`outbound.allowedCidrs`).
- `deploy/k8s/collector` contains policies for collector gateways and node agents.

`server.trustedProxies` and `opamp.clientCertHeader` are safe only together with this policy or an equivalent one. The chart is experimental in the first release ([ADR-0021](../adr/0021-publication-gates-and-v0-1-0-scope.md)).

## Development mode

`myriarchd dev` ships well-known logins and a known enrollment token. It therefore listens only on `127.0.0.1:8080` by default and refuses to start with OIDC, TLS, an https or non-loopback `server.publicUrl`, a non-loopback `server.metricsListenAddr`, a non-loopback `--listen` address or a non-loopback `--dsn`, unless `--allow-nonlocal` is given. A banner with the credentials is logged as a warning at start and every 30 minutes. `myriarchd serve` revokes a known development enrollment token that it finds in the database (with an audit entry), and sessions and API tokens of local accounts are not accepted without an active development login. The Helm chart and the production examples never use dev mode.

Development mode weakens these controls, and `--allow-nonlocal` exposes all of them:

- Agents connect without an enrollment token (`opamp.requireEnrollmentToken` is off), and a well-known enrollment token (`myre_devtoken_insecure_do_not_use_in_prod`) is created.
- Three well-known logins (`admin`, `operator` and `viewer`, `auth.devLogin`) are active.
- Session cookies are not `Secure` (`auth.cookieSecure` is off) and the session cookie has no `__Host-` prefix.
- `/metrics` is served on the main listener (`server.metricsListenAddr` is empty), without authentication.
- The outbound address check is off, so the server can reach loopback and private addresses.
- An embedded PostgreSQL with well-known credentials is used unless `--dsn` is given.

## Configuration check without starting

`myriarchd config validate --config FILE [--skip-files]` loads the configuration exactly as `myriarchd serve` does (defaults, YAML with unknown keys rejected, `${VAR}`, `MYRIARCH_*`) and runs every check that `serve` performs before touching the database or the network. There is one validation, not two. Without `--skip-files` it also reads the referenced files (secrets not empty, CA file with a certificate, key matching certificate, readable `trusted_root.json`). Exit codes: 0 valid (warnings allowed), 1 invalid, 2 usage error. Use it in CI and in deployment pipelines so that a misconfiguration is found before the pod starts. See the [command reference](../reference/cli/myriarchd_config_validate.md).

## Known limitations

The controls above leave the following limitations. They are listed so that operators can compensate for them; the
guidance after each item names what to do.

### Users and automation

- **Limits are kept per replica.** Sign-in rate limits, the bootstrap admin backoff and the delay for invalid bearer
  tokens are held in memory on each replica. With several replicas the effective limit is a multiple of the configured
  one, and a restart clears the counters. The tables are bounded, so clients that use many addresses or IPv6 prefixes
  get separate buckets. For internet-facing deployments add rate limiting at the ingress or load balancer.
- **The bootstrap admin backoff is account-wide.** Failed attempts from any client delay the break-glass sign-in for
  everyone (up to 15 minutes per replica). Restrict network access to `/api/v1/auth/bootstrap-login`
  ([Authentication](../operations/authentication.md)).
- **Unknown session cookies are looked up each time.** A request with an unknown session cookie costs one database
  lookup; unlike invalid bearer tokens it is not delayed per client address.
- **API tokens follow myriarch, not the identity provider.** A token carries one role, capped at its creator's role as
  stored in myriarch; the stored role changes at the creator's next sign-in or by an administrator. Group changes or
  removal at the identity provider do not reach existing API tokens before they expire (`auth.apiTokenMaxTtl`). To
  offboard a user at once, disable the user in myriarch: this ends the sessions and revokes the API tokens.
- **Manual roles override the mapping.** A role that an administrator sets stays in force when the user's groups
  change, as long as a mapping or `auth.oidc.defaultRole` still admits the user.
- **No sign-out of all sessions, no last-admin guard.** Disabling a user is the way to end all of its sessions.
  Administrators can demote or disable each other; the bootstrap admin is the recovery path.
- **Denied requests are not audited.** Authorization denials (`403`) and CSRF failures appear in the access log and in
  the HTTP metrics, not in the audit log.
- **ID token audience.** The `aud` claim must contain the client ID; the `azp` claim of ID tokens with several
  audiences is not checked.
- **Public login configuration.** `GET /api/v1/auth/config` tells anonymous clients which sign-in methods are enabled,
  because the login page needs it.
- **Cache fallback.** If an invalidation event is lost, a revoked session or API token can be accepted for up to 5
  seconds, and a revoked enrollment token for up to one minute plus the next connection check.
- **Request IDs are correlation aids.** Clients may supply `X-Request-Id` (up to 64 characters of `[A-Za-z0-9._-]`); it
  is not proof of origin.
- **Inline styles in the UI.** The Content-Security-Policy of the UI allows inline styles (`style-src 'unsafe-inline'`)
  for the component library and the editor; scripts are limited to the application itself.

### Agents and hosts

- **Agent identity follows the credential, not the host.** An instance UID is bound to the enrollment token and, with
  mutual TLS, to the certificate subject. Any host that holds the same credential can present that UID; clone detection
  applies only while the original agent is online and reports another `host.name`. Per-host client certificates give a
  per-host binding.
- **The certificate identity is the common name.** Binding uses the subject common name (the full subject when it is
  empty), and every CA in `server.tls.clientCaFile` is trusted equally. Use one CA dedicated to agents and unique common
  names.
- **Forwarded client certificates.** A certificate forwarded by a proxy proves a valid chain, not possession of the
  private key. Its trust rests on `server.trustedProxies` and the network policy.
- **Enrollment tokens are fleet credentials.** A token can register any number of agents. `opamp.maxConnectionsPerToken`
  limits WebSocket connections only and is unlimited by default, and HTTP-polling agents share the pooled session table
  of a replica (`opamp.maxConnectionsPerReplica`). Use one token per channel or region and revoke a token that may have
  leaked. Declarative tokens removed from the configuration stay valid until they are revoked (a warning is logged at
  every start).
- **The collector can read the enrollment token.** The supervisor receives the token through its service environment;
  the collector runs under the same service account and inherits that environment. Treat the collector account as able
  to read the token, and never reference the token variable in collector configurations.
- **Shared addresses.** Agents behind one NAT address share `opamp.connectRatePerIp`. Size it for the largest group of
  agents behind one address and the reconnect time you accept.
- **Self-reported attributes.** Attributes other than `labels.*` come from the host and are only as trustworthy as the
  host.
- **Updater health checks are local.** The updater judges a new version by the local health endpoints of the collector
  and the supervisor; the collector configuration can influence that judgement and therefore the rollback decision.
- **Container mode shares one account.** In container mode the updater, the supervisor and the collector run under the
  same user ID and share the binary volume, so installed versions are not protected against the collector process.
- **Scope of upstream signatures.** Sigstore verification proves a genuine artifact of the upstream release workflow
  for the offered version; it does not bind the asset name, platform or distribution. The binary-name extraction and
  the version check before activation catch mismatches.
- **Installer integrity anchors.** The per-asset `.sha256` files come from the same origin as the assets; integrity
  rests on the Sigstore bundle. `--insecure` (`-Insecure`) turns checks off and is meant for labs only.
- **Elevated Windows sessions.** User Account Control is not a security boundary. Run the Windows installer from an
  administrator session on a host where no untrusted process runs under the same account, and pass the verifier
  explicitly (`-Verifier`).
- **Trusted root on hosts.** A host with a stale `trusted_root.json` cannot verify new bundles until the file is
  refreshed ([Host-side checks of the updater](#host-side-checks-of-the-updater)).

### Server, Git and outbound connections

- **Concurrent connections.** `myriarchd` bounds the time of each request but does not limit the number of concurrent
  connections per client. Put an ingress or load balancer with connection limits in front of internet-facing
  endpoints.
- **The GitOps webhook limit is replica-wide.** It applies before the shared secret is checked. While it is exhausted,
  deliveries receive `429` and changes are applied at the next poll (`gitops.pollInterval`).
- **Git over SSH.** The repository size limit applies to Git over HTTPS while the response streams; for SSH it is
  checked after the fetch, within the fetch time budget. The Git server is trusted infrastructure.
- **Forward proxies.** With `HTTPS_PROXY`, `HTTP_PROXY` or `artifacts.proxyUrl`, requests to host names are subject to
  the proxy's egress policy, not to the check in `myriarchd`; only direct connections and address literals are checked
  locally.
- **Allowed ranges are global.** A range in `outbound.allowedCidrs` is open to every outbound client, not only to the
  one it was added for. Keep the ranges narrow.
- **Webhook signatures.** The signature of executor and notification webhooks covers the timestamp and the body, not
  the method, the URL or other headers. Receivers enforce the time window and drop repeated deliveries.
- **Download tokens are bearer tokens.** A download token works for repeated downloads of one artifact by one updater
  while its offer is pending, until `artifacts.downloadTokenTtl` ends. Without `auth.downloadTokenSecretFile` the signing
  key is stored in the database, so whoever can read the database can create download tokens for offered artifacts.
- **Local cache directories.** The artifact cache re-hashes a file only when its size or modification time changes,
  and without explicit settings the cache and the Git clone use the temporary directory. On hosts with other local
  users set `artifacts.dir` and `gitops.cloneDir` to directories that only the server account can write; the updater
  verifies every package again.
- **Certificate rotation needs a restart.** The TLS certificate, key and client CA of `myriarchd` are read at start;
  rotate them with a (rolling) restart.
- **Metrics are unauthenticated.** `/metrics` relies on network restriction (the separate listener and
  `networkPolicy.metrics.from`). Labels carry no identifiers or secrets.
- **Repository links.** `gitops.uiRepoWebUrl` inserts repository paths unescaped; the paths come from the reviewed
  repository and the host part from the configuration.
- **Restoring a backup.** A replica started from a restored database can become leader and continue rollouts and
  executor calls. Start a restored copy without ingress, with an empty `executors.allowedWebhookHosts`, without executor
  secrets and without Git and notification channels until it is meant to take over.

### Build and release

- **Release signing is key-based for now.** Until the keyless release workflow exists
  ([ADR-0023](../adr/0023-keyless-release-signing.md)), the project's own artifacts are signed with a key and verified
  against the committed `deploy/release/cosign.pub`, without a transparency log.
- **Release pipeline controls are pending.** Protected release environments, signing jobs without caches, SBOM and
  provenance attestations and a verification step before publishing belong to the release workflow, which does not
  exist yet. No release has been published.
- **Experimental platforms.** The Windows installer and service setup, the systemd units and the Helm chart are tested
  in staging mode and with simulated service managers; they are experimental in the first release
  ([ADR-0021](../adr/0021-publication-gates-and-v0-1-0-scope.md)).

For the export-control assessment of the published software see [Export control](export-control.md).

## Out of scope

- Compromise of the PostgreSQL server, the Kubernetes cluster or the host operating system.
- The security of the collector configuration that an operator writes: the server sends what Git declares, and the collector runs it with the rights of its service account.
- The upstream release process and the public Sigstore infrastructure.
