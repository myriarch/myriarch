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

Supply chain of the installers. `deploy/scripts/install-linux.sh` and `deploy/scripts/install-windows.ps1` download only over https (also after redirects) and verify collector and supervisor with SHA-256 and a Sigstore bundle before anything runs (default `require`; verifier `cosign` 3.0.4 or newer, or `myriarch-updater verify-bundle`; an older `cosign` stops the installation with `sigstore_verifier_outdated`). The certificate identity passed to the verifier is built for the requested version and asset (`.../base-release.yaml@refs/tags/v<version>` for the collector, `.../base-binary-release.yaml@refs/tags/v<version>` for the supervisor, end-anchored), so that a genuine but older release served from a mirror is not accepted as the requested version. Without `--insecure` the Linux installer requires `curl` (it restricts redirects to https and sets a minimum TLS version); `wget` is accepted for labs only. Archives are extracted without their owner and mode, and the installed collector binary belongs to `root`. A CA bundle passed with `--ca-file` (`-CaFile`) is copied into the protected configuration directory and referenced there, because the supervisor and the updater send the enrollment token over connections that trust it. On Windows the installer takes `cosign.exe` from `PATH` only from a directory that only administrators can write, and a verifier passed with `-Verifier` must also be in such a directory (otherwise the installation stops with `directory_unsafe`, because the elevated installer would run a binary that unprivileged accounts can replace, and the verifier decides whether `checksums.txt`, and with it the SYSTEM updater, is trusted; an installed updater is already in the protected installation tree), creates its working directory with a protected DACL, hashes verified files again after copying them, resets the ACL of moved directories and, before the token is written to the service environment, replaces the ACL of the service key by SYSTEM and administrators and reads it back (any other allow entry stops the installation with `acl_failed`). The updater binary itself needs a trusted hash, either `--updater-sha256` (`-UpdaterSha256`) or the `checksums.txt` of the release, and that file is authenticated by its keyless Sigstore bundle (`checksums.txt.sigstore.json`, [ADR-0023](../adr/0023-keyless-release-signing.md), [ADR-0029](../adr/0029-host-under-the-myriarch-organization.md)). The identity passed to the verifier is `^https://github\.com/myriarch/myriarch/\.github/workflows/release\.yml@refs/tags/v<version>$` with the issuer `https://token.actions.githubusercontent.com`, bound to the updater version (`--updater-version`, or the version in the file name of `--updater-url`; both must agree), escaped and end-anchored, so that another tag, another workflow or another repository is refused. The verifier of that file is `cosign` 3.0.4 or newer or an already installed `myriarch-updater` (`--verifier`, or the installed one when a host is upgraded), resolved before the updater is downloaded and never the binary that is being installed, because its hash comes from the file that the verifier must check. Without a verifier the installation stops; a host without one verifies `checksums.txt` on an admin host and passes `--updater-sha256`. The check of `checksums.txt` cannot be switched off with `--verify-signatures skip` (the options conflict) and also applies with `--insecure`. The option `--signing-key` (`-SigningKey`) and the key-signed `checksums.txt.sig` were removed (`option_removed`); keyless signing leaves no long-lived signing key whose theft would let an attacker sign an updater for the hosts. `-UpdaterSignerThumbprint` stays for private PKIs; a pinned thumbprint breaks at every certificate renewal. A `cosign` that reports a pre-release of 3.0.4 (for example `v3.0.4-rc.1`) counts as older than 3.0.4, and on Linux any `--verifier` whose file name starts with `cosign` gets the version check. Residual risks: the installer script itself is only as trustworthy as its download, so verify the release archive that contains it before you run it (see [Verify releases](verify-releases.md)); and under App Control script enforcement an unsigned script runs in Constrained Language Mode and fails, so allow it by its hash. `--insecure` (`-Insecure`) lifts the remaining checks for labs and tests only.

Supply chain of the images. The published images (`ghcr.io/myriarch/myriarch`, `ghcr.io/myriarch/myriarch-updater`) are not compiled again: `scripts/build-image-crane.sh` with `IMAGE_SOURCE=dist` takes the binaries and the example repositories from the linux archives of the release build (the server image holds `myriarchd`, `myrctl` and `myriarch-sim`, each from its own archive) and refuses an archive whose SHA-256 differs from `checksums.txt` or is not listed there, a binary of another platform, a dynamically linked binary, one without the embedded root CAs, a server with the `devpg` tag or without the web UI, one that does not carry the release version, (in a release build from a clean tree) one that does not record the commit of the image, archives of one image whose license files differ, and an example tree that holds a link, a path outside the tree or no file. The image is `FROM scratch`, holds no shell and no package manager, runs as the numeric user and group 65532 and has no writable program file; the only location the user can write is the empty data directory `/var/lib/myriarchd` (0750, owned by 65532) of the server image and `/tmp`; the build is deterministic: the same archives give the same uncompressed layer everywhere and the same digests with the same zlib (the build logs its version). A release build (without `ALLOW_DIRTY=1`) also needs the `metadata.json` of goreleaser for the commit and version, refuses the test settings that would change what is published (a single platform, no SBOM, another source URL), and reads each archive once, so the bytes checked against `checksums.txt` are the bytes that are used. `ci/release/push-images.sh` pushes by digest only and refuses a reference without `@sha256:<64 hex>` (without a digest `crane` creates the tag `latest`). Because `crane` and some registries do not compare the digest of the reference with the content, the script computes the digest from the layout before the push and reads index and image back afterwards. It writes to `ghcr.io/myriarch/*` only when `GITHUB_WORKFLOW_REF` names `.github/workflows/release.yml` at a version tag (the registry host is compared after upper case, a trailing dot and a port are removed), and then requires the platforms `linux/amd64` and `linux/arm64` and the source annotation; this guards against accidents and is not a boundary, which is the permission of the workflow token. A build from source never pushes there. `myriarch-updater` embeds the Mozilla root CAs like the other binaries (used only when the host has none, so a scratch image can verify TLS servers); CAs configured through `server.caFile` of the updater (and `outbound.caFile` of `myriarchd`) are added on top. The tests cover each refusal (`ci/tests/image-crane-test.sh`). Residual risk: that ghcr.io rejects a manifest whose digest does not match is not verified (a successful release only shows that the push by digest is accepted), which is why the client computes and checks the digest itself.

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
| Log records cannot be forged through request data. Values that come from a request (paths, headers, user names, error texts) are logged only as structured attributes next to constant messages, and `newLogger` returns only the JSON or text handler of `log/slog`, which escape line breaks, carriage returns and other control characters in attribute values. A test (`cmd/myriarchd/logger_test.go`) fails if the logger stops doing so; a new log handler has to keep it green. | Always on; `telemetry.logFormat` selects `json` or `text`. |
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

## Helm chart values

The values of the Helm chart are checked before anything is rendered. The schema and the first four rows come with the chart of v0.2.0; the chart of v0.1.0 ignores a key that it does not know. The last row describes how the chart treats TLS: the checks are in the templates and in the server and are older than the schema, which adds the type of their switches.

| Control | Setting and default |
|---|---|
| A strict JSON schema (`deploy/helm/myriarch/values.schema.json`) validates the values in `helm install`, `upgrade`, `template` and `lint`. It refuses a key that the chart does not read (also inside `config`, whose keys come from the configuration structure of the server), a value of the wrong type and a malformed string (an image digest, a registry host, a Kubernetes name, a quantity, a duration, a port), and it names the path of the value. Only the Kubernetes objects that the chart passes on unchanged (such as volumes, security contexts, affinity terms and Gateway API references) and the `global` values of a parent chart are open objects; the API server validates the Kubernetes objects, and a test lists every open object with its reason. | Always on. `--skip-schema-validation` of Helm switches it off for one command; the checks of the templates remain. |
| Secrets are not values. The schema has no key for the database DSN, the OIDC client secret, the webhook secret, the URL of a notification channel or an enrollment token. The configuration reads them from files of the mounted Secret (`database.dsnFile`, `auth.oidc.clientSecretFile`, `gitops.webhookSecretFile`, `notifications.channels[].urlFile`), and each entry of `enrollmentTokens` names a Secret or lets the chart generate one. A reference to a Secret entry (`key`, `bootstrapAdmin.tokenKey`, `passwordHashKey`, `secretEnv[].key`) that has the form of an enrollment token (`myre_`) is refused, and a Secret name cannot have it, because the pattern of a name allows no underscore. A test fails when a setting of the configuration that can hold text (a string, a list of strings or a map of strings) names a token, secret, password, key, credential, passphrase, bearer token or DSN and is not a path or a prefix, so a new setting of that kind has to be excluded or written as a file setting on purpose; a second test runs a setting of every such shape through the check, so that a check that looks at strings only would fail. | Always on. The templates refuse the inline secrets of the configuration as well, for a run without the schema. |
| The chart never starts the development mode. The schema offers neither `config.dev` nor `config.auth.devLogin`, and the templates refuse both. The settings that the chart generates from top-level values (`config.auth.bootstrapAdmin` and `config.opamp.enrollmentTokens`) are not offered in `config` either. | Always on. |
| The image reference is built from `image.registry`, `image.repository` and the tag or the digest. The schema accepts only a registry host with an optional port, a repository in the form of the container registry specification (no tag, digest or scheme in it, and its path in lower case; the first segment counts as a registry host only when it contains a `.` or a `:` or is `localhost`, as for the container runtimes) and a digest of the form `sha256:` and 64 hexadecimal characters, so nothing else can hide in the reference. `image.digest` pins the image immutably, and the tag is ignored while it is set. An empty `image.registry` leaves the registry of a repository without a host to the container runtime, usually Docker Hub, where the project publishes no image: use it only for a repository in a registry that you control, and pin `image.digest`. | `image.registry` is `ghcr.io`, `image.digest` is empty. |
| The external entry points of the chart use TLS unless you opt out. `myriarchd` refuses a `server.publicUrl` that is not https in production, and with an https `server.publicUrl` rendering fails for an Ingress without `ingress.tls` and for an HTTPRoute without `httpRoute.tlsTerminatedAtGateway: true`; `ingress.allowWithoutTls: true` is the opt-out for an Ingress whose TLS ends in front of it. The OpenShift Route terminates TLS at the router (edge) and by default redirects plain HTTP requests to https. Plain HTTP at the router, and with it `ws://`, needs the explicit setting `insecureEdgeTerminationPolicy: Allow`; `None` serves no plain HTTP. The schema types the switches as booleans and the policy as one of its three values: a string such as `"false"` counts as true in a template and used to switch a check off, and is refused now. The install notes print the `wss://` endpoint. | `ingress.tls` empty, `ingress.allowWithoutTls: false`, `httpRoute.tlsTerminatedAtGateway: false`, `openshift.route.tls.insecureEdgeTerminationPolicy: Redirect`. |

Residual risk: the schema checks the form of a value, not its meaning, and it cannot know that a free-text value is a secret. A secret in an annotation, a label or in `extraEnv[].value` passes it; use `secretEnv` or `extraEnv[].valueFrom` with a Secret. `myriarchd config validate` checks the meaning of the rendered configuration.

Inside the cluster `myriarchd` serves plain HTTP on port 8080 (`config.server.tls.terminatedUpstream: true`, because TLS ends at the Ingress, the Gateway or the router), and agents of the project that run in the cluster, such as the updater in container mode, connect to the Service with `ws://`. With the NetworkPolicy (on by default) only its sources can reach the port, by default every pod of the release namespace (`networkPolicy.ingress.allowSameNamespace: true`), and that traffic is not encrypted. Use it in a cluster whose network you trust, and narrow the sources with `allowSameNamespace: false` and `networkPolicy.ingress.from` ([Kubernetes network policy](#kubernetes-network-policy)).

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

## Database schema compatibility

The server does not run on a database schema that it cannot work with. The behaviour is specified in [Server behaviour](../design/opamp-server.md) and decided in [ADR-0036](../adr/0036-database-schema-compatibility-and-rollback.md).

| Control | Setting and default |
|---|---|
| The database records a compatibility level (table `schema_compat`): the lowest schema level that a binary must have to run on it. At start, `myriarchd serve` and `myriarchd dev` refuse a database whose compatibility level is above their own schema level. The error names the schema version and both levels, nothing is changed and no component is started. A release that cannot work with the changes of a later migration (its author raised the level for them) therefore does not run on that schema, for example after a rollback of the deployment. | Always on, also with `database.migrateOnStart` off. Not configurable. |
| From migration 00006 on, a schema without the table `schema_compat` or without its row has no known compatibility level and is refused by `myriarchd serve`, `myriarchd dev` and `myriarchd migrate`; the error says to restore the database from a backup. | Always on. |
| A schema that lacks migrations of the binary is refused at once when migrations are not applied at start. The error names the setting and the command `myriarchd migrate`, where the server would otherwise start and fail later in some component. | `database.migrateOnStart`, default `true`. |
| A schema that is newer than the binary but compatible with it starts with a warning in the log that names the schema version, the compatibility level and the schema level of the binary. | Always on. |
| `myriarchd migrate` applies the migrations from the database settings alone and refuses an incompatible schema in the same way. It reads no secret of another feature (bootstrap admin, enrollment tokens, OIDC, Git), so the job that runs it needs none of them. `--check` changes nothing and reports the state by its exit code. | `database.dsn` or `database.dsnFile`, `MYRIARCH_DATABASE_*`. |

Residual risk:

- The check runs at start. Replicas that already run are not stopped when another replica migrates the database to a level they cannot work with. A migration that older replicas cannot work with says so in its header and in the upgrade notes; update with one replica or in a maintenance window then.
- Whether a migration is compatible with the previous version is the claim of its author. A test compares the claim with the effect on the compatibility level, but only the review of the migration can judge whether the older binary really works on the new schema.

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
  explicitly (`-Verifier`, from a directory that only administrators can write, for example `C:\Program Files\cosign`).
- **Execution policy of the Windows installer.** The documented start sets the execution policy for the one PowerShell process
  that runs the installer (`powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File ...`), after the archive passed the
  checks and `Unblock-File` ran on the script. It never uses `Bypass` and never changes the policy of the machine or of the
  user. A policy that Group Policy sets (`MachinePolicy`, `UserPolicy`) takes precedence over the parameter, and the installer
  does not work around it: `Restricted` and `AllSigned` stop the unsigned installer, and App Control for Business runs it in
  Constrained Language Mode, where it fails. Download, check and unpack in a folder that only administrators can read and
  write, and keep the token file there ([Connect a host](../tasks/connect-a-host.md#2-download-and-verify-the-installer) creates
  it with a DACL of its own for SYSTEM and the administrators). "Only administrators can write" protects the integrity of the
  checked files but not the token: a new folder below `C:\Program Files` can be read by every local user until its DACL is
  replaced. In a folder that the account can write, an unprivileged process of the same account can replace a checked file
  before the elevated installer runs it.
- **Directory and verifier of the Linux installer.** The script and its templates run as root. Download, check and unpack the
  archive in a directory that root owns and no other account can write, such as one below `/root`, in a root shell
  ([Connect a host](../tasks/connect-a-host.md#2-download-and-verify-the-installer)), and keep the token file there (mode
  `0600`). Pass a verifier that only root can replace: `--verifier /usr/local/bin/cosign`, or the `myriarch-updater` of the
  verified archive in that directory. In a directory or with a `cosign` that the invoking account can write, a process of that
  account can replace a checked file before root runs it. The Linux installer does not check the location of the verifier, so
  this control rests on the steps of the guide and not on the installer.
- **Trusted root on hosts.** A host with a stale `trusted_root.json` cannot verify new bundles until the file is
  refreshed ([Host-side checks of the updater](#host-side-checks-of-the-updater)). Without a local file the verifier of the
  installers fetches the trusted root through TUF, so the host needs https access to the public Sigstore repository during the
  installation; without the access and without `--trusted-root` (`-TrustedRoot`) the installer stops with
  `sigstore_verification_failed`.

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
- **No rollback to v0.1.0.** v0.1.0 does not read the compatibility level of the database, and its migration library accepts
  a database with higher versions, so it starts on a database that a later release has migrated without any check. The
  project does not support that. Back up the database before an upgrade, and go back by restoring the backup. Later
  releases check the level: a rollback between them is refused when the database needs a newer binary.

### Build and release

- **No signing key in the release build.** goreleaser signs nothing, `ci/build-release.sh` has only the snapshot mode, and the
  hook for a PFX file is removed. Releases are signed keyless by the release workflow
  (`.github/workflows/release.yml`, [ADR-0023](../adr/0023-keyless-release-signing.md),
  [ADR-0035](../adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)). The installers
  authenticate the updater through that keyless bundle only (see Supply chain of the installers above). The toolkit for
  organizations that mirror releases, keyless and unchanged or re-signed with their own key, is in `contrib/enterprise-mirror/`.
- **Acceptance check of signed Windows files.** A signing provider is not trusted with the content of a release. Stage apply of
  the release build (`deploy/release/README.md`) requires that the rebuilt file equals the unsigned build, extracts the signature
  from the provider's file and attaches it to the job's own build, and verifies the result against the committed chain: subject,
  SHA-256 digest and RFC 3161 timestamp (the output of osslsigncode is parsed, because it exits 0 for a missing timestamp). The
  text of the two PowerShell scripts before the signature block must equal the repository file. The trust anchors can be replaced
  only in snapshot builds, and test signing with a throwaway CA is refused on tags and in `release.yml`. A release build requires
  the configured signing mode (`MYRIARCH_WINDOWS_SIGNING`) and refuses a `signer.json` that names another provider, so a hook
  cannot turn a configured signature into an unsigned release. osslsigncode runs only at the version pinned in
  `ci/versions.env`, built from a verified source archive (with fortified libc calls and a stack protector) and taken from the tool
  cache, never from `PATH` (the distribution package predates verification fixes for crafted files). Staging happens only in git-ignored paths.
  `ci/tests/windows-hook-test.sh` covers the failure cases. The decision is
  [ADR-0034](../adr/0034-authenticode-signing-of-windows-release-artifacts.md).
- **CI controls.** The GitHub Actions workflows ([ADR-0032](../adr/0032-continuous-integration-on-github-actions.md)) use only
  GitHub-owned actions, each pinned to a full commit SHA (the one exception is `ossf/scorecard-action`, see "One third-party
  action: OpenSSF Scorecard" below), a read-only token (`permissions: contents: read`), no `pull_request_target` or
  `workflow_run` trigger, checkouts without persisted credentials, no secrets outside the listed deployment jobs (next item),
  and untrusted expressions only through `env:`. Caches that a pull request writes are scoped to it and never restored on
  `main`; the artifacts of a failed run are an allow-list of log files (no tokens, cookies or database files), because any
  signed-in user can download the artifacts of a public repository. Tools are built from `ci/tools` with `go.sum` or
  downloaded with a SHA-256 check, never installed by a third-party action, and the collector binaries of the end-to-end
  test are checked against the SHA-256 that the upstream release publishes. The `workflows` gate (`ci/check-workflows.py`,
  `actionlint`, `zizmor`) enforces these rules, and the commits of a pull request are scanned for secrets. A change of a workflow is a
  security-relevant change and is reviewed as one.
- **Secrets in deployment jobs.** A deployment job that `ci/workflow-secrets.txt` lists (workflow file, job, secret name)
  may read an environment secret. The job must declare `environment:`, so that the protection rules of the environment apply
  before it starts, and the secret must be the whole value of a key, `KEY: ${{ secrets.NAME }}`, in the `env:` of the job or
  of one of its steps. `ci/check-workflows.py` rejects every other way to reach a secret, also inside a listed job (`run:`
  text, `with:`, `if:`, the workflow-level `env:`, index syntax, `toJSON(secrets)`, `secrets: inherit`), and a stale,
  malformed or duplicate line of the list. A line is reviewed like a new privilege: which secret, why the job needs it and
  which environment protects it. Fork pull requests receive no secrets. The check reads every workflow file in
  `.github/workflows` and every action file below `.github/actions` with the extension `.yml` or `.yaml` (hidden names and
  upper-case spellings included), so the name of a file is no way around it. Residual risk: the check sees that a job
  declares an environment, not where the secret is defined, and it reads the text of a workflow, not the parsed YAML.
- **One third-party action: OpenSSF Scorecard.** [ADR-0033](../adr/0033-openssf-scorecard-as-the-only-third-party-action.md)
  allows `ossf/scorecard-action`, pinned to a commit SHA, in `.github/workflows/scorecard.yml`, job `analysis`, and nowhere
  else. `ci/check-workflows.py` enforces the place (rule R1) and the shape that the Scorecard API requires for published
  results, in places more strictly (rule R12): triggers `push` to `main` and `schedule`, one job on a GitHub-hosted Ubuntu runner with exactly the
  permissions `contents: read`, `actions: read`, `security-events: write` and `id-token: write`, only `uses:` steps from a
  fixed list, no secrets, no caches, no environment. Residual risk: the action runs third-party code with `id-token: write`
  and `security-events: write`; a compromised release could request tokens for the identity of that workflow or change
  code-scanning alerts. That identity (`scorecard.yml` on `refs/heads/main`) is not the release identity and is never
  accepted where the release identity is required. The pin is changed only by a reviewed update.
- **Dependency review of pull requests.** The job `dependency-review` of `.github/workflows/ci.yml`, a member of `ci-ok`
  ([ADR-0038](../adr/0038-dependency-license-policy.md)), compares the dependency graph of a pull request or merge group with its
  base and fails when a dependency that the change adds or updates, in the runtime scope, carries a license outside the CNCF
  allowlist and Apache-2.0 (`ci/license-allowlist.txt`, `.github/dependency-review-config.yml`) or has a known advisory of high or
  critical severity. Controls: a GitHub-owned action pinned to a commit, the read-only token of the workflow (no `permissions` key,
  no secret, no comment in the pull request), no request to a third party (the OpenSSF Scorecard lookup of the action is off) and a
  license expression that is not valid SPDX fails. The job never counts as skipped: on a pull request or merge group its steps run
  (a skipped job fails `ci-ok`, so a pull request cannot pass without the review), and on every other event a step says that
  there is nothing to compare. `ci/tests/license-allowlist-test.sh` pins the inputs, the place in `ci-ok` and the
  equality of the configuration with the license file, and refuses an exception with a version or without a reason. The binding
  license check of the release builds (`ci/check-release-licenses.sh`) allows fewer licenses (the list `release` of the same file)
  and is as strict as before: a license that only the pull request check allows fails it when a release build links the package.
  Residual risks: the license data comes from ClearlyDefined through the dependency graph and can be wrong or missing (a missing
  license is only listed); the check looks at the dependencies a change adds or updates, not at the whole tree; an exception names a
  package and covers all its later versions; the configuration is read from the checkout of the pull request, so a pull request that
  changes the policy and a dependency together depends on the review of the policy change (the maintainer owns every file in
  `.github/CODEOWNERS`); and `pkg:npm/elkjs` (EPL-2.0, a dependency of Mermaid that the website serves) has no exception until the
  question of its notice is decided.
- **Static analysis in CI: CodeQL, Scorecard and zizmor.** Three controls look at the code and the pipeline, none replaces review:
  - *CodeQL* (`.github/workflows/codeql.yml`) analyses the Go code (built for Linux with and without the `embedui` and `devpg`
    build tags, for Windows with and without them and for macOS, so that the Windows-only code of the updater, which runs as
    SYSTEM, and the `devpg` file for systems other than unix are included), the web UI and the JavaScript scripts of the
    website, the Python scripts of the repository (`ci/`, `scripts/`, `website/scripts/`, `assets/brand/source`,
    `contrib/enterprise-mirror`) and the workflows, with the `security-extended` queries, on pushes to `main`, on every pull
    request to `main` and weekly; results are code scanning alerts. Pull requests from forks and from Dependabot are analysed
    too. The job holds `security-events: write` (listed in `ci/workflow-permissions.txt` with its review) and builds the code of
    the pull request. The scope lets it write code scanning alerts and dismiss them, and gives no access to contents, packages
    or releases. The token of a fork pull request is read-only whatever the workflow asks for; a Dependabot pull request does
    receive the scope, because the `permissions` key of the job raises the read-only default of Dependabot. That stays harmless
    because the steps of the job compile the code and run none of it (no test, no package installation, no generator) and the
    job has no secret. Alerts do not block a merge until a ruleset requires them. It finds known classes of defects in code it
    can model; it does not find logic errors, design flaws, problems in dependencies (`govulncheck` covers called
    vulnerabilities) or the configuration of the repository and the organization. It analyses what the build and the extractor
    see: Go files for operating systems other than Linux, Windows and macOS, build tags other than `embedui` and `devpg`, and
    the compiled web UI are not covered, and the shell scripts, the Helm chart and the PowerShell installers are no CodeQL
    language (ShellCheck, the chart gate, the installer tests and review cover them). That the extractor follows the `GOOS` of
    the traced build is assumed, not proven: nobody has recorded that the extractor saw the files below. Check it in the logs or
    on the tool status page of code scanning, and again after every change of the build steps of the workflow, that the
    `*_windows.go` files (`internal/updater/service_windows.go`, `internal/artifact/open_windows.go`,
    `internal/deps/deps_windows.go`) and `internal/devpg/lock_other.go` were extracted; if they were not, the Windows build
    needs a job of its own.
  - *OpenSSF Scorecard* (`scorecard.yml`, ADR-0033) scores repository practices (token permissions, pinned dependencies, branch
    protection, review, signed releases, dependency update tooling, security policy) weekly and on pushes to `main` and publishes
    the result. It runs without `repo_token`, so checks that need administrative access (branch protection) are partly
    unknown, and the score reflects settings and files, not the quality of the code. A high score is a signal, not a
    guarantee.
  - *zizmor* (the `workflows` gate, `zizmor.yml`) audits the workflows, the local action and the Dependabot configuration for
    template injection, dangerous triggers, excessive permissions, persisted credentials, cache poisoning, unpinned or impostor
    actions and unsafe writes to the environment files. It runs offline in the local gate and with the read-only token of the
    run in CI for the audits that ask the GitHub API, which go to github.com only. It runs with the pedantic persona. Every
    ignored finding is listed in `zizmor.yml` or marked on its line, with the reason; no audit is disabled or configured, and
    rule R14 of `ci/check-workflows.py` rejects a `zizmor.yml` with anything but ignore entries (by the file name of a workflow,
    one audit per line, each under a reason with at least one word) and an inline ignore without rule names or reason. Rule R14
    also rejects a file that zizmor reads and that holds a character on which the policy check and a YAML parser disagree
    (U+0085, U+2028 and U+2029 are line breaks for the parser, not for the check, which splits at LF and CR; control characters
    and a byte order mark; a file that is not UTF-8), so both read the same lines. A file that zizmor cannot parse fails the
    gate (`--strict-collection`) instead of being skipped with a warning. It reads the files of the repository, so it does not
    see settings of the organization or the repository (allowed actions, environments, rulesets), secrets, or what a pinned
    action does at run time.
- **Known vulnerabilities in CI: govulncheck as a gate and in code scanning.** The gate `vulncheck` (job `compliance` of `ci.yml`,
  `ci/vulncheck.sh`) fails when the code calls a vulnerable symbol that `ci/vulncheck-allowlist.txt` does not accept (ID, review date,
  reason). `.github/workflows/vulncheck.yml` (pushes to `main`, a daily run and manual runs, never a pull request) reports the same
  analysis to code scanning as SARIF, in the categories `govulncheck/source-release`, `govulncheck/source-devpg` and
  `govulncheck/binaries`, and blocks nothing. Controls: the one job that holds `security-events: write` (`upload`, the only line of the
  workflow in `ci/workflow-permissions.txt`) runs no code of the repository: it checks out without credentials, downloads the reports
  that the two scan jobs of the same run made and uploads them; the scan jobs hold `contents: read`; the workflow has no secret, and
  `ci/tests/vulncheck-test.sh` fails when another job gets a write scope, when `upload` runs a script or another action, or when a
  pull request can start the workflow. The scan of the released binaries (`ci/vulncheck-binaries.sh`) starts only after the keyless
  signature of `checksums.txt` was verified with the identity of `release.yml` at the tag of the version (`ci/release/verify-blob.sh`,
  mode bound) and after the SHA-256 of every archive matched that signed list; it reads out only the member that is named like the
  program, as a regular file within a size limit, never runs a downloaded program, takes the tag only in the form of a release tag and
  fails closed on a download that fails, a signature that does not verify, an archive that does not match, a missing or irregular member
  and a failing tool (`ci/tests/vulncheck-test.sh` covers each). Reports leave out the findings that the allowlist accepts. Limits: govulncheck knows the Go vulnerability database, not
  other ecosystems (the dependency review and Dependabot cover the advisories of npm, pip and the actions); the binary mode lists the
  vulnerable symbols that a binary contains and cannot tell whether the program reaches them, so it reports more than the source
  analysis; the results about binaries are anchored at `go.mod` because a binary has no file or line; the binaries are the archives of
  the latest stable release unless a manual run names another; and a run that cannot reach GitHub or the vulnerability database fails
  visibly and leaves the earlier alerts as they are.
- **Rules for the release workflow (R13).** `ci/check-workflows.py` requires of
  `.github/workflows/release.yml`: a trigger for pushed `v*` tags only, no caches and no local actions, the literal environment
  `release`, a `guard` job that every other job needs, no Node.js, Go, pnpm, npm, goreleaser, make or similar build tool
  (the list is in the docstring of the check) in a job with a write scope, `id-token`, `attestations` or an environment, and
  an approval (a job with the environment `release`) before every privileged job. Two constructs that make `needs:`
  ineffective are rejected in every job: an `if:` that tests the status of other jobs (`always()`, `failure()`,
  `cancelled()`, `success()`) and `continue-on-error`. A privileged job may not use a shell that reads its script from
  standard input (here-document, `| sh`, `bash -s`) or a `shell:` other than `bash` or `sh`. The check reads the text of the
  workflow; it does not see a program that a privileged job starts through a variable, a script, an interpreter that is not
  a shell or a wrapper that it does not know.
- **Release workflow controls.** `.github/workflows/release.yml`
  ([ADR-0035](../adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)) builds, signs,
  verifies and publishes a release. Controls, in the order of the run:
  - *Trigger and guard.* The workflow runs only for a pushed tag `v*` (no `workflow_dispatch`, no pull request) and, in a fork,
    not at all. The job `guard` runs the release tag guard and `ci/release/meta.sh`, which refuses a run whose
    `GITHUB_WORKFLOW_REF` is not exactly `myriarch/myriarch/.github/workflows/release.yml@refs/tags/<tag>`, so a renamed
    workflow file or a call through a reusable workflow fails before anything is signed (the frozen identity).
  - *Job isolation (R13).* Build code runs only in jobs with `contents: read`; privileged jobs run reviewed scripts, cosign,
    crane, gh and `actions/attest` and take only artifacts of the same run, downloaded by ID and checked against the digest of
    the artifact service and the SHA-256 that the producing job reported. The tools are built from `ci/tools` in the run (no
    cache) and travel as two artifacts: the signing set (cosign, crane, gh) is built first, in a job of its own where nothing
    else is installed or run, and is the only set that jobs with a write scope, `id-token`, `attestations` or an environment
    execute; the build set (goreleaser, syft, yq, go-winres, osslsigncode) reaches only jobs with `contents: read`. Every consumer
    runs `ci/release/tools.sh check` after the unpack, and VERSIONS and SHA256SUMS of both sets are in the job summaries.
    Artifacts are kept one day. The release scripts and the goreleaser commands take the pinned tools only from `TOOLS_BIN`, the
    directory of the unpacked set, never from `PATH` (the workflow never writes `GITHUB_PATH`): `deploy/release/goreleaser.yaml`
    passes `TOOLS_BIN` to its `sboms` commands, `go-winres` is accepted only from `TOOLS_BIN` and only with the stamp of its pin,
    and `ci/tests/release-workflow-test.sh` and `ci/tests/release-scripts-test.sh` enforce this.
  - *Two approvals.* Exactly two jobs use the environment `release` (no administrator bypass, tags `v*` only): `sign-windows`,
    before anything is signed or pushed, and `finalize`, after the draft release exists and every verification passed. A gate is
    passed once per run, not once per attempt: "Re-run failed jobs" re-runs only the failed jobs and their dependents, so
    `sign-blobs`, `push-images` and `draft` run again without a new approval of gate 1. Whether `finalize` asks again after an
    approved attempt has not been observed: on the first published candidate, `v0.1.0-rc.3`, "Re-run failed jobs" after gate 2
    re-ran only a failed smoke job and asked for no approval. Gate 2 is a technical boundary for `finalize` only: the job `draft`
    holds `contents: write` without the environment and runs before it, with the signing tool set and the reviewed scripts as its
    whole trust base.
  - *Keyless signing and provenance.* `checksums.txt` is signed with a cosign bundle (it covers the 39 other files), and the
    build provenance of those files is attested with `actions/attest`. Both images are pushed by digest only (`push-images.sh`
    refuses any other reference, so the images have no release tag before verification), signed with `cosign sign --recursive`
    and given provenance and SBOM attestations, strictly one after the other. No long-lived signing key or registry secret
    exists; the registry token is `GITHUB_TOKEN`.
  - *Verification before publication.* The jobs `verify`, `verify-images` and `verify-windows` check the files and the images from
    which the draft was created (the artifacts of the run and the pushed digests, not the draft itself) with the commands that
    users run (exact identity with repository, ref, commit and trigger, the pattern of ADR-0029, the pattern bound to the
    version, provenance with `--deny-self-hosted-runners`, image index, configuration, signatures and attestations, absence of a
    `latest` tag, a run of both images on amd64 and arm64, version resources and Authenticode state of the Windows files). The
    contents of the draft are bound to that result only by `finalize`; the reviewer of gate 2 checks the count of 42 assets.
  - *Finalize.* After the second approval `finalize` checks that the draft still holds exactly the verified files by comparing
    GitHub's SHA-256 digest of every asset with the verified manifest, that the release is a draft of this tag by
    `github-actions[bot]`, that `refs/tags/<tag>` still resolves to the tagged commit (`tag_moved`; `draft.sh` also sets the commit
    as the target of the release) and that both packages are readable without credentials; it refuses to move an immutable image
    tag; it sets the image tags, reads the draft by ID again and repeats the checks of the draft (author, title, pre-release flag,
    the only release of the tag), the asset checks and the tag check directly before it publishes; a tag that no longer exists is
    reported as `tag_moved`. After publication it reads the release once more, repeats the identity and uniqueness checks and
    compares its assets with the verified manifest again
    (`release_assets_changed` says that the release is immutable and wrong), and it requires that the release is immutable and
    that GitHub's release attestation verifies.
  - *Smoke tests.* After publication, jobs of the same workflow with read scopes only download and verify the release
    anonymously, install it on amd64 and arm64, stage the Windows installer, run both images and install the Helm chart of the tag
    on kind.

  The release-time dependency, license and notices gates run in the job `ui`. Residual risks: one account can tag and approve
  both gates until a second reviewer exists (the second organization owner is for emergencies, ADR-0029); deleting the
  `sha256-<hex>` tags or package versions of a released digest breaks the verification of that image; the job token cannot read
  the setting for immutable releases, so a disabled setting is detected only after the release is published
  (`release_not_immutable`, the release stays mutable); artifacts live for one day; the release depends on the availability of
  GitHub, ghcr.io and the Sigstore public-good instance. The first push of a new package makes it private, and an organization
  owner makes it public by hand (irreversible) before the second approval; `finalize` refuses to continue while it is private.
  Further residual risks of the release workflow: (1) a replacement of an asset in the short window between the last read of the
  draft and the PATCH is found only afterwards, when the release is immutable; (2) every run, also a failed or abandoned one,
  creates valid credentials for the identity at the tag before verification and gate 2 (Rekor-logged cosign bundles of
  `checksums.txt`, image signatures and attestations in the `sha256-<hex>` tags, build provenance in GitHub's attestation API)
  and deleting a draft revokes none of them, so a check with the exact or version-bound identity does not prove that a file was
  released, only `gh release verify-asset` does (membership in the published, immutable release); (3) write access to Actions
  allows re-running the privileged jobs of a run that passed gate 1; (4) a KMS route for Windows signing that needs `id-token`
  makes `sign-windows` a holder of the release identity (ADR-0035, point 7); (5) the resume path of `finalize` cannot tell a
  release published by hand between two attempts from its own; (6) the signing tool set is built from the pinned modules in the
  run, so the dependency trees of cosign, crane and gh (and of `actions/attest` and the other GitHub-owned actions) are part of
  the trust base of the privileged jobs, no longer the trees of the build tools.
  The list of the release build is in [`deploy/release/README.md`](../../deploy/release/README.md), the maintainer's runbook is
  [Releasing](../releasing.md), the user's commands are [Verify releases](verify-releases.md).
- **Release tag guard.** `ci/release/guard.sh` runs as the first job (`guard`) of `release.yml`. It refuses a tag that is
  not `vX.Y.Z` or `vX.Y.Z-rc.N`, a lightweight tag, a tag that points at another commit than the run's, a commit that is
  not on `origin/main`, a `VERSION` or a copy of it that differs from the tag, a final tag without a dated `CHANGELOG.md`
  heading, an rc tag without entries in "Unreleased", and a commit without a successful `ci-ok` check run of GitHub
  Actions. It takes its inputs from the environment only, matches each value as a whole string and refuses control characters
  (no value can add a line to a command, a URL or the `curl` configuration), sends the token to `curl` through standard
  input, never prints it, and fails closed on any API error. A final tag needs list items under its `CHANGELOG.md` heading
  and none left under "Unreleased"; an rc tag needs list items under "Unreleased". The Windows signing rule is part of it: `deploy/release/windows-signing.env` names the
  mode (`off`, `signpath`, `kms`; anything else fails), and a final tag with `off` passes only when
  `WINDOWS_UNSIGNED_FINAL_TAG` is exactly that tag, so an unsigned final release needs a reviewed change for that one tag.
  Residual risk: the guard is a script of the repository and protects only as far as the release workflow runs it first and
  the tag ruleset limits who can push `v*` tags; the check of `ci-ok` trusts the check-run name and the GitHub Actions app.
- **Authenticode adds to Sigstore and never replaces it.** From the keyless release workflow on
  ([ADR-0023](../adr/0023-keyless-release-signing.md)), the trust anchor of installers and updater is its Sigstore identity;
  Authenticode is the signature that
  Windows evaluates (App Control for Business or AppLocker publisher rules, Smart App Control, the execution policy for the
  two PowerShell scripts). The target is a signature by the SignPath Foundation with a manual approval of every release in a
  second system, so that a leaked token or a compromised CI step cannot sign alone; the release workflow holds no long-lived
  signing secret ([ADR-0034](../adr/0034-authenticode-signing-of-windows-release-artifacts.md)). Release builds accept no PFX
  file any more.
- **Exception for v0.1.0: unsigned Windows files.** `v0.1.0` ships `myriarch-updater.exe`, `myrctl.exe` and the two PowerShell
  scripts without Authenticode signatures, under a reviewed exception for exactly that tag; the release notes carry an
  "unsigned" banner. Release candidates (`vX.Y.Z-rc.N`) may ship the same files unsigned, with the same "unsigned" banner and
  the same verification advice. Residual risk: Windows itself does not tell an operator who built an unsigned file, so integrity rests on
  the Sigstore signature alone. Verify the Windows files with cosign before you run them, starting with `install-windows.ps1`
  itself (use a `cosign.exe` from a folder that only Administrators can write); the verified installer may then verify the
  binaries it downloads. Use `Unblock-File` only after that. Hosts with App Control allow the unsigned installer,
  `myriarch-updater.exe`, `myrctl.exe` and `Set-ServiceEnvironment.ps1` by their hashes, one set of rules per release and each
  only after its cosign verification, because there is no publisher to trust; a hash rule for a file that was not verified first trusts whatever the
  file contains. Signing starts with a later release; a change of publisher resets SmartScreen reputation and breaks publisher
  rules that operators wrote, so it is announced in the release notes.
- **Website deployment uses a team-scoped Vercel token.** The token and the Vercel IDs exist only as secrets of the GitHub
  environments `website-preview` and `website-production`; the build job of `.github/workflows/website.yml`, which runs
  repository code, has no secrets, and the deploy job installs the pinned Vercel CLI without lifecycle scripts and uploads
  the artifact of the build job. Pull requests from forks never receive the token. The token is team-scoped with an
  expiration date and is rotated before it expires. Residual risk: every workflow run on a branch of the repository can use
  `website-preview` and so the token. Production is limited to `main` by the deployment branch policy of its environment
  ([Deployment](../../website/README.md#deployment)). Vercel makes the first deployment of a project a production
  deployment, whatever the CLI asks for; the deploy job therefore refuses a preview while the project has no production
  deployment, and after every deployment it checks that Vercel recorded the requested target, so that the build of a pull
  request cannot end up on the production domain unnoticed. The Vercel firewall runs Bot Protection and the rule for AI bots in
  log mode only, so visitors get no challenge and no cookie; blocking measures are for incidents
  ([Firewall incidents](../../website/README.md#firewall-incidents)).
- **Experimental platforms.** The Windows installer and service setup, the systemd units and the Helm chart are tested
  in staging mode and with simulated service managers; they are experimental in the first release
  ([ADR-0021](../adr/0021-publication-gates-and-v0-1-0-scope.md)).

For the export-control assessment of the published software see [Export control](export-control.md).

## Out of scope

- Compromise of the PostgreSQL server, the Kubernetes cluster or the host operating system.
- The security of the collector configuration that an operator writes: the server sends what Git declares, and the collector runs it with the rights of its service account.
- The upstream release process and the public Sigstore infrastructure.
