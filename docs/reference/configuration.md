---
title: "Server configuration"
weight: 50
description: "Every key of the myriarchd configuration file, with its type, default and effect."
---

This page is generated from the configuration struct in `internal/config/config.go`, its defaults and its doc comments by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.

## How the configuration is loaded

`myriarchd serve`, `myriarchd dev`, `myriarchd config validate` and `myriarchd config print` load the configuration the same way (`myriarchd migrate` needs only the database settings, see the last item of this list):

- The configuration file is YAML. An unknown key is an error.
- The loader starts from the defaults in the **Default** column. `myriarchd dev` starts from other defaults, shown as "(dev: ...)" where they differ.
- In string values, `${VAR}` and `${VAR:-default}` are replaced after the file is decoded, with the value of the environment variable of the process. An unset or empty variable gives the default, or the empty string when there is none.
- Then the environment variables `MYRIARCH_<PATH>` override single settings, see [environment variables](environment-variables.md#configuration-overrides). They win over the file.
- `database.dsnFile` and `notifications.channels[].urlFile` are always read at startup, `auth.oidc.clientSecretFile` only while `auth.oidc.enabled` is `true` and `gitops.webhookSecretFile` only while `gitops.enabled` is `true`. The content of the file (without surrounding white space) wins over the inline value next to it. A file that cannot be read or is empty is an error. The other `...File` settings name files (certificates, keys, tokens, CA bundles) that the components read themselves.
- Durations use Go syntax such as `30s`, `5m` or `720h`. A plain integer is rejected, and `d` is not a unit: write `720h` for 30 days.
- [`myriarchd config print`](cli/myriarchd_config_print.md) shows the effective configuration with secrets redacted, [`myriarchd config validate`](cli/myriarchd_config_validate.md) checks a file.
- [`myriarchd dev`](cli/myriarchd_dev.md) sets some keys from its flags: `--listen` sets `server.listenAddr` (and `server.publicUrl`, unless the file or the environment sets it), `--gitops-dir` sets `gitops.repoUrl` to `dir:<directory>`, `--data-dir` sets `dev.dataDir` (and with it `artifacts.dir` to `<data-dir>/artifacts` and `gitops.cloneDir` to `<data-dir>/gitops`), `--releases-fixtures` sets `releases.fixtureDir`, `--replica-id` sets `ha.replicaId` and `--dsn` sets `database.dsn`.
- [`myriarchd migrate`](cli/myriarchd_migrate.md) loads the database settings and nothing else, so that it runs where the server configuration does not exist: the `database` section of the file (an unknown key inside it is an error; every other section is ignored, whatever it holds), `${VAR}` references in its values, the `MYRIARCH_DATABASE_*` variables and `database.dsnFile`. It validates only the database settings.

How to read the tables: **Type** `duration` is a string in Go syntax. **Default** `empty` is the empty string, an empty list or an empty map, `-` marks a group of keys (the defaults are on its keys), `none` means that a list or map has no entries and its fields have no default of their own, and a size in bytes shows its binary unit in parentheses. A key such as `notifications.channels[].name` is a field of every entry of the list, and `<name>` in `executors.secrets.<name>.env` stands for the key of the map entry.

## server

`server` configures the main HTTP listener (UI, REST API, OpAMP).

| Key | Type | Default | Description |
|---|---|---|---|
| `server.listenAddr` | string | `":8080"` | `listenAddr` is the address of the main HTTP listener, as host:port or :port (default ":8080"). |
| `server.publicUrl` | string | empty (dev: `"http://localhost:8080"`) | `publicUrl` is the externally reachable base URL (used for OIDC redirects, artifact download URLs, notification links), e.g. `"https://fleet.example.com"`. Required in production. |
| `server.tls` | object | - | `tls` configures server-side TLS. If `server.tls.certFile` and `server.tls.keyFile` are set, myriarchd terminates TLS itself; otherwise TLS terminates at the ingress and `server.tls.terminatedUpstream` must be set explicitly in production. |
| `server.tls.certFile` | string | empty | `certFile` is the PEM file with the server certificate. Set it together with `server.tls.keyFile`; with both set, myriarchd terminates TLS itself. |
| `server.tls.keyFile` | string | empty | `keyFile` is the PEM file with the private key of the server certificate. Set it together with `server.tls.certFile`. |
| `server.tls.clientCaFile` | string | empty | `clientCaFile` enables (optional) client certificate verification for the OpAMP endpoint (mTLS). Clients without certificate are still accepted if `opamp.requireClientCert` is false. |
| `server.tls.terminatedUpstream` | boolean | `false` | `terminatedUpstream` declares that TLS is terminated in front of myriarchd (ingress, load balancer) and that the plaintext listener is only reachable from there. Production refuses to start with a plaintext listener unless this is set (the Helm chart sets it); myriarchd logs a warning at startup. |
| `server.trustedProxies` | list of strings | empty | `trustedProxies` lists the proxies (CIDRs) whose X-Forwarded-For, X-Forwarded-Proto and X-Forwarded-Client-Cert headers are honored. |
| `server.metricsListenAddr` | string | `":9090"` (dev: empty) | `metricsListenAddr` is the address of a separate listener that serves /metrics (default ":9090"; empty = serve /metrics on `server.listenAddr`). |
| `server.shutdownTimeout` | duration | `30s` | `shutdownTimeout` limits the graceful shutdown (default 30s). |

## opamp

`opamp` configures the OpAMP endpoint.

| Key | Type | Default | Description |
|---|---|---|---|
| `opamp.path` | string | `"/v1/opamp"` | `path` is the URL path of the OpAMP endpoint (default "/v1/opamp"). |
| `opamp.requireEnrollmentToken` | boolean | `true` (dev: `false`) | `requireEnrollmentToken` rejects connections without a valid "Authorization: Bearer myre\_..." header (default true; dev mode false). |
| `opamp.requireClientCert` | boolean | `false` | `requireClientCert` rejects connections without a verified client certificate (mTLS, either terminated by myriarchd or forwarded by a trusted proxy). |
| `opamp.clientCertHeader` | string | empty | `clientCertHeader` is the header that carries the URL-encoded PEM client certificate from a trusted proxy (e.g. "X-Forwarded-Client-Cert" or "ssl-client-cert"). |
| `opamp.offlineAfter` | duration | `1m30s` | `offlineAfter` marks an agent offline when it sent no message for this long (default 90s = 3 x the supervisor default heartbeat of 30s). |
| `opamp.staleApplyingAfter` | duration | `1m30s` | `staleApplyingAfter` is how long one connection of an agent may keep reporting APPLYING for the desired config, unchanged, before the server offers the config once more (once per connection; default 90s, 3 x the supervisor default heartbeat of 30s; 0 turns it off). A supervisor that restarts while it applies a config reports the stored APPLYING again after it reconnects, and nothing ends it; without this setting a rollout waits for the `applyTimeout` of its policy. The time counts from the first report on the connection, so short connections never cause an offer. Keep it above the `config_apply_timeout` of the supervisors (30s in the shipped templates) and below the `applyTimeout` of the rollout policies (10 minutes for configuration by default). |
| `opamp.lastSeenFlushInterval` | duration | `15s` | `lastSeenFlushInterval` is the batch interval for the `last_seen_at` writes (default 15s). |
| `opamp.maxMessageSize` | integer | `4194304` (4 MiB) | `maxMessageSize` is the largest accepted OpAMP message in bytes (default 4 MiB; the default of the OpAMP library is 64 MiB). |
| `opamp.autoRebindOnTokenRotation` | boolean | `false` | `autoRebindOnTokenRotation` lets an agent whose bound enrollment token was revoked or expired move to another valid token (same client certificate subject) without an admin action; the move is audited as agent.rebind (default false: `POST /agents/{id}/actions/reenroll` is required). |
| `opamp.maxEffectiveConfigBytes` | integer | `1048576` (1 MiB) | `maxEffectiveConfigBytes` limits the effective config body of one agent that is stored (default 1 MiB); larger bodies are not stored and produce a warning event for the agent. |
| `opamp.enableCompression` | boolean | `false` | `enableCompression` turns on WebSocket permessage-deflate (default false). The read limit of the WebSocket library applies to the compressed frame only, so decompressed messages would not be bounded by `opamp.maxMessageSize`; enable it only if that is acceptable. Production refuses it unless `opamp.allowUnboundedCompression` acknowledges the risk. |
| `opamp.allowUnboundedCompression` | boolean | `false` | `allowUnboundedCompression` acknowledges that decompressed WebSocket messages are not bounded by `opamp.maxMessageSize` (default false). Only consulted in production, where `opamp.enableCompression` requires it. |
| `opamp.sendTimeout` | duration | `10s` | `sendTimeout` is the write deadline for pushing a message to one agent (default 10s). The send call of the OpAMP library ignores context cancellation, therefore the server sets the deadline on the underlying network connection. |
| `opamp.maxConnectionsPerReplica` | integer | `10000` | `maxConnectionsPerReplica` limits the OpAMP connections of one replica: above it, new connections get 503 with Retry-After (default 10000). |
| `opamp.connectRateLimit` | number | `200` | `connectRateLimit` limits authenticated new connections per second per replica (token bucket, burst = 2x; default 200). Excess gets 429 + Retry-After. The bucket is charged after authentication, so unauthenticated requests cannot exhaust it. |
| `opamp.connectRatePerIp` | number | `50` | `connectRatePerIp` limits connection attempts per second and client IP (token bucket, burst = 2x; default 50; 0 = default). Behind a proxy the client IP is only known if `server.trustedProxies` lists the proxy; otherwise all agents share the proxy address. |
| `opamp.maxConnectionsPerToken` | integer | `0` | `maxConnectionsPerToken` limits concurrent WebSocket connections per enrollment token and replica (default 0 = unlimited). |
| `opamp.enrollmentTokens` | list of objects | none | `enrollmentTokens` declares enrollment tokens in the configuration (for example from mounted Kubernetes Secrets). At startup every entry is reconciled into the database: only the SHA-256 hash and the display prefix are stored. Entries are never revoked automatically; removing one only logs a notice (revoke the token in the UI or with `DELETE /api/v1/enrollment-tokens/{id}`). ${VAR} expansion works inside the entries. Not settable through an environment variable. |
| `opamp.enrollmentTokens[].name` | string | none | `name` is a DNS label (lower case letters, digits and "-", 1 to 63 characters) and unique within the list. |
| `opamp.enrollmentTokens[].tokenFile` | string | none | `tokenFile` is the absolute path of a file with the token ("myre\_" followed by 32 to 120 letters, digits, "\_" or "-"). |
| `opamp.enrollmentTokens[].labels` | map of strings | none | `labels` are copied to the server-side labels of the agents that enroll with the token (same rules as `POST /enrollment-tokens`). |
| `opamp.enrollmentTokens[].expiresAt` | string | none | `expiresAt` is an RFC 3339 timestamp; empty = no expiry. |

## database

`database` configures PostgreSQL.

| Key | Type | Default | Description |
|---|---|---|---|
| `database.dsn` | string | empty | `dsn` is the PostgreSQL connection string, e.g. "postgres://myriarch:secret@db:5432/myriarch?sslmode=verify-full". Secret: `myriarchd config print` shows `<redacted>`. |
| `database.dsnFile` | string | empty | `dsnFile` reads the DSN from a file (takes precedence over `database.dsn`). |
| `database.maxConns` | integer | `20` | `maxConns` is the maximum size of the connection pool (default 20). |
| `database.migrateOnStart` | boolean | `true` | `migrateOnStart` runs pending migrations at startup (default true). Migrations take a Postgres advisory lock, so concurrent replicas are safe. When it is false and the schema lacks migrations of this version, myriarchd refuses to start: run `myriarchd migrate` first. A schema that is newer than this version but compatible with it starts with a warning, and a schema that is incompatible with it is refused, whatever this setting says. |
| `database.statementTimeout` | duration | `15s` | `statementTimeout` is the server-side limit of one SQL statement of the application pool (default 15s; migrations and housekeeping are exempt). |

## auth

`auth` configures authentication and authorization.

| Key | Type | Default | Description |
|---|---|---|---|
| `auth.oidc` | object | - | `oidc` configures single sign-on with an OpenID Connect identity provider. |
| `auth.oidc.enabled` | boolean | `false` | `enabled` turns on single sign-on through OpenID Connect (default false). |
| `auth.oidc.issuerUrl` | string | empty | `issuerUrl` is the issuer of the identity provider exactly as its discovery document states it, e.g. `"https://idp.example.com/realms/otel"` (no trailing slash). Some providers need a specific form, which myriarchd checks when it loads the configuration. |
| `auth.oidc.clientId` | string | empty | `clientId` is the client identifier that is registered for myriarchd at the identity provider. |
| `auth.oidc.clientSecret` | string | empty | `clientSecret` is the client secret that is registered for myriarchd. Prefer `auth.oidc.clientSecretFile`. A client secret or a client certificate is required while `auth.oidc.enabled` is true. Secret: `myriarchd config print` shows `<redacted>`. |
| `auth.oidc.clientSecretFile` | string | empty | `clientSecretFile` reads the client secret from a file (takes precedence over `auth.oidc.clientSecret`; read only while `auth.oidc.enabled` is true). |
| `auth.oidc.clientCertificateFile` | string | empty | `clientCertificateFile` authenticates the client with a certificate instead of a secret (`private_key_jwt`, for providers that register a client certificate): the PEM certificate of the client registration. Set it together with `auth.oidc.clientKeyFile`; it excludes `auth.oidc.clientSecret` and `auth.oidc.clientSecretFile`. The file is read again when it changes. |
| `auth.oidc.clientKeyFile` | string | empty | `clientKeyFile` is the RSA private key (PKCS#1 or PKCS#8, PEM) that belongs to `auth.oidc.clientCertificateFile`. Set both or none. The file is read again when it changes. |
| `auth.oidc.scopes` | list of strings | `["openid", "profile", "email"]` | `scopes` are the OAuth scopes that myriarchd requests at login; the list must contain "openid" (default: openid, profile, email). |
| `auth.oidc.groupsClaim` | string | `"groups"` | `groupsClaim` is the claim that holds the groups of the user (default "groups"). The claim may hold a string, a list of strings or an object (its keys count); a dotted path reaches into nested objects. |
| `auth.oidc.nameClaim` | string | `"name"` | `nameClaim` is the claim that holds the display name (default "name"; "preferred\_username" is the fallback when it is empty). |
| `auth.oidc.emailClaim` | string | `"email"` | `emailClaim` is the claim that holds the e-mail address (default "email"). "email:" entries of `auth.oidc.roleMapping.users` require the default: `email_verified` attests only the standard claim. |
| `auth.oidc.roleMapping` | object | - | `roleMapping` maps groups (or roles) and individual users to roles; the highest role wins. |
| `auth.oidc.roleMapping.admin` | list of strings | empty | `admin` lists the groups (or roles) whose members get the admin role. |
| `auth.oidc.roleMapping.operator` | list of strings | empty | `operator` lists the groups (or roles) whose members get the operator role. |
| `auth.oidc.roleMapping.viewer` | list of strings | empty | `viewer` lists the groups (or roles) whose members get the viewer role. |
| `auth.oidc.roleMapping.users` | object | - | `users` maps individual users to roles, for identity providers without a groups claim. |
| `auth.oidc.roleMapping.users.admin` | list of strings | empty | `admin` lists the users who get the admin role. |
| `auth.oidc.roleMapping.users.operator` | list of strings | empty | `operator` lists the users who get the operator role. |
| `auth.oidc.roleMapping.users.viewer` | list of strings | empty | `viewer` lists the users who get the viewer role. |
| `auth.oidc.defaultRole` | string | empty | `defaultRole` is the role of users without a matching group. Default "" = deny (`access_denied`, audited). Only set it deliberately (e.g. "viewer" when every user of the IdP application may read); myriarchd logs a warning. |
| `auth.oidc.displayName` | string | `"SSO"` | `displayName` is shown on the login button (e.g. "Company SSO"). |
| `auth.bootstrapAdmin` | object | - | `bootstrapAdmin` is the break-glass administrator that works without an identity provider (see [docs/operations/authentication.md](../operations/authentication.md)). |
| `auth.bootstrapAdmin.enabled` | boolean | `false` | `enabled` turns the bootstrap admin on (default false). The account accepts sign-ins only when a credential file is set and single sign-on does not replace it (see `auth.bootstrapAdmin.keepWithOidc`). |
| `auth.bootstrapAdmin.username` | string | `"admin"` | `username` of the account (default "admin"): 1 to 64 characters from letters, digits, ".", "\_", "@" and "-". |
| `auth.bootstrapAdmin.passwordHashFile` | string | empty | `passwordHashFile` holds a bcrypt hash (cost 10 or higher) of the password, created with "myriarchd auth hash-password". Re-read on every sign-in attempt, so a rotation needs no restart. |
| `auth.bootstrapAdmin.tokenFile` | string | empty | `tokenFile` holds a one-time token (32 to 512 characters). It works for one sign-in; writing a new value into the file issues a new one. |
| `auth.bootstrapAdmin.keepWithOidc` | boolean | `false` | `keepWithOidc` keeps the account active while `auth.oidc.enabled` is true (default false: single sign-on replaces the bootstrap admin). |
| `auth.devLogin` | object | - | `devLogin` enables local username/password login (dev mode only). |
| `auth.devLogin.enabled` | boolean | `false` (dev: `true`) | `enabled` turns local login on (default false). `myriarchd dev` enables it; production refuses it. |
| `auth.devLogin.users` | list of objects | none | `users` lists the local accounts. `myriarchd dev` defines the accounts admin, operator and viewer, each with its own name as the password. Not settable through an environment variable. |
| `auth.devLogin.users[].username` | string | none | `username` is the login name of the account; unique within the list. |
| `auth.devLogin.users[].password` | string | none | `password` is the password of the account, in plain text (development only). Secret: `myriarchd config print` shows `<redacted>`. |
| `auth.devLogin.users[].role` | string | none | `role` is the role of the account: viewer, operator or admin. |
| `auth.sessionTtl` | duration | `2h` | `sessionTtl` is the idle timeout of a session (default 2h), sliding. |
| `auth.sessionMaxAge` | duration | `12h` | `sessionMaxAge` is the absolute lifetime of a session, independent of activity (default 12h). After it users sign in again, so identity provider changes (groups, disabled accounts) take effect. |
| `auth.apiTokenMaxTtl` | duration | `2160h` | `apiTokenMaxTtl` is the maximum lifetime of API tokens (default 90 days): a token created without expiresAt gets it, a longer expiresAt is refused, and an older token stops working. 0 means unlimited (production logs a warning). It is the only way a removal at the identity provider reaches API tokens besides disabling the user in Myriarch. |
| `auth.cookieSecure` | boolean | `true` (dev: `false`) | `cookieSecure` forces the Secure cookie flag (default true; dev false). With it the session cookie is named \_\_Host-myriarch\_session. |
| `auth.downloadTokenSecretFile` | string | empty | `downloadTokenSecretFile` is the file with the HMAC key for artifact download tokens (32+ random bytes). The key is generated and stored in the KV store if this is empty. Tokens: see `artifacts.downloadTokenTtl`. |

## gitops

`gitops` configures the GitOps source.

| Key | Type | Default | Description |
|---|---|---|---|
| `gitops.enabled` | boolean | `true` | `enabled` turns the GitOps syncer on (default true). |
| `gitops.repoUrl` | string | empty (dev: `"dir:examples/quickstart"`) | `repoUrl` is the repository: `https://`..., ssh://git@..., git@host:org/repo.git or "dir:///abs/path" / "dir:relative/path" for a plain directory (dev, tests; revision = content hash). |
| `gitops.branch` | string | `"main"` | `branch` is the branch that myriarchd follows (default "main"). |
| `gitops.path` | string | empty | `path` is the directory inside the repository that holds the GitOps files (default "" = root). |
| `gitops.auth` | object | - | `auth` holds the credentials for the repository. |
| `gitops.auth.username` | string | empty | `username` is the user name for HTTPS, used together with `gitops.auth.tokenFile` (GitLab: username "oauth2" or any). |
| `gitops.auth.tokenFile` | string | empty | `tokenFile` is the file with the access token for HTTPS, used together with `gitops.auth.username`. |
| `gitops.auth.sshKeyFile` | string | empty | `sshKeyFile` is the private key file for ssh repositories. |
| `gitops.auth.knownHostsFile` | string | empty | `knownHostsFile` is the known\_hosts file for ssh repositories. It is required for them: host key verification is mandatory. |
| `gitops.auth.caFile` | string | empty | `caFile` is a PEM file with the CA certificate of a self-signed Git server. |
| `gitops.pollInterval` | duration | `1m` (dev: `10s`) | `pollInterval` is how often the repository is fetched (default 60s). |
| `gitops.webhookSecret` | string | empty | `webhookSecret` is the shared secret for `POST /api/v1/gitops/webhook` (GitLab "X-Gitlab-Token" or GitHub "X-Hub-Signature-256"). Secret: `myriarchd config print` shows `<redacted>`. |
| `gitops.webhookSecretFile` | string | empty | `webhookSecretFile` reads the webhook secret from a file (takes precedence over `gitops.webhookSecret`; read only while `gitops.enabled` is true). |
| `gitops.maxRepoBytes` | integer | `268435456` (256 MiB) | `maxRepoBytes` limits the size of the checked-out repository (default 256 MiB). |
| `gitops.maxFiles` | integer | `2000` | `maxFiles` limits the number of YAML files in the repository (default 2000). |
| `gitops.maxYamlBytes` | integer | `67108864` (64 MiB) | `maxYamlBytes` limits the total size of the YAML files (default 64 MiB). |
| `gitops.cloneDir` | string | empty (dev: `".dev/gitops"`) | `cloneDir` is the directory of the local clone. Default: a directory in the temporary directory, named after the repository URL; production setups set a directory that only the server account can write (a warning names an empty value). |
| `gitops.uiRepoWebUrl` | string | empty | `uiRepoWebUrl` builds the "view in Git" links, e.g. `"https://gitlab.example.com/otel/fleet-config/-/blob/{commit}/{path}"`. |

## releases

`releases` configures the upstream release tracker.

| Key | Type | Default | Description |
|---|---|---|---|
| `releases.enabled` | boolean | `true` | `enabled` turns the release tracker on (default true). |
| `releases.githubApiUrl` | string | `"https://api.github.com"` | `githubApiUrl` is the GitHub API that is polled for releases (default `"https://api.github.com"`; GitHub Enterprise: `"https://ghe.example.com/api/v3"`). |
| `releases.rawBaseUrl` | string | `"https://raw.githubusercontent.com"` | `rawBaseUrl` is the base URL for the CHANGELOG files (default `"https://raw.githubusercontent.com"`; GHES: `"https://ghe.example.com/raw"`). |
| `releases.tokenFile` | string | empty | `tokenFile` is a file with a GitHub token (raises the rate limit from 60 to 5000/h). |
| `releases.releasesRepo` | string | `"open-telemetry/opentelemetry-collector-releases"` | `releasesRepo` is the repository of the collector distributions (default "open-telemetry/opentelemetry-collector-releases"). |
| `releases.coreRepo` | string | `"open-telemetry/opentelemetry-collector"` | `coreRepo` is the repository of the collector core (default "open-telemetry/opentelemetry-collector"). |
| `releases.contribRepo` | string | `"open-telemetry/opentelemetry-collector-contrib"` | `contribRepo` is the repository of the collector contrib components (default "open-telemetry/opentelemetry-collector-contrib"). |
| `releases.distributions` | list of strings | `["otelcol-contrib", "opampsupervisor"]` | `distributions` lists the distributions to track (default \["otelcol-contrib", "opampsupervisor"\]). |
| `releases.pollInterval` | duration | `1h` | `pollInterval` is how often the upstream releases are polled (default 1h). |
| `releases.includePrereleases` | boolean | `false` | `includePrereleases` also tracks pre-releases (default false). |
| `releases.minVersion` | string | `"0.120.0"` | `minVersion` ignores older releases on the first import (default "0.120.0"). |
| `releases.fixtureDir` | string | empty | `fixtureDir` reads releases and changelogs from recorded files instead of the network (tests, offline dev). |

## artifacts

`artifacts` configures the artifact cache.

| Key | Type | Default | Description |
|---|---|---|---|
| `artifacts.dir` | string | empty (dev: `".dev/artifacts"`) | `dir` is the directory for cached files, per replica. Default: a fixed directory in the temporary directory; production setups set a directory that only the server account can write (a warning names an empty value). |
| `artifacts.sourceBaseUrl` | string | `"https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download"` | `sourceBaseUrl` is where release assets are fetched from. Default `"https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download"`. An internal mirror must use the same layout: `<base>/<tag>/<asset>`. |
| `artifacts.agentBaseUrls` | list of strings | empty | `agentBaseUrls` is an allowlist of base URLs updaters download from (`"http://myriarchd:8080"`): absolute http(s) URLs without path, query, fragment or credentials. For every package offer myriarchd picks, per updater, the entry whose host:port equals the host:port of the attribute dev.myriarch.updater.server\_endpoint the updater reported (the OpAMP endpoint it is connected to); updaters without a match download from `server.publicUrl`. This lets agents inside a cluster use an internal service name while agents elsewhere keep the public route. http:// entries need `artifacts.allowInsecureAgentBaseUrls` in production. |
| `artifacts.allowInsecureAgentBaseUrls` | boolean | `false` | `allowInsecureAgentBaseUrls` permits http:// entries in `artifacts.agentBaseUrls` in production (a cluster-internal service whose traffic never leaves the namespace); myriarchd logs a warning. Package contents are verified by SHA-256 and sigstore on the updater in any case. |
| `artifacts.prefetchPlatforms` | list of strings | empty | `prefetchPlatforms` lists the platforms to prefetch on new target versions, e.g. \["linux/amd64", "windows/amd64"\]. Default: empty. Member platforms of updater fleets are always added; an explicit prefetch request that names no platforms falls back to the platforms of the connected collectors that use the requested distribution. |
| `artifacts.verify` | object | - | `verify` configures the verification of upstream artifacts. |
| `artifacts.verify.requireChecksum` | boolean | `true` | `requireChecksum` verifies SHA-256 against the per-asset "\<asset\>.sha256" file (releases \>= v0.158.0; older releases: shared checksums.txt) (default true). |
| `artifacts.verify.requireSignature` | boolean | `true` | `requireSignature` verifies the per-asset sigstore bundle "\<asset\>.sigstore.json" (cosign keyless, Rekor + TSA) (default true). |
| `artifacts.verify.certificateIdentityRegexp` | string | `"^https://github\\.com/open-telemetry/opentelemetry-collector-releases/\\.github/workflows/base-(binary-)?release\\.yaml@refs/tags/v[0-9]+\\.[0-9]+\\.[0-9]+$"` | `certificateIdentityRegexp` is the regular expression that the SAN of the Fulcio certificate must match. The default accepts the contrib workflow (base-release.yaml) and the supervisor workflow (base-binary-release.yaml) of the upstream release repository for plain vX.Y.Z tags. Additionally the verifier requires the tag to equal the artifact version. |
| `artifacts.verify.certificateOidcIssuer` | string | `"https://token.actions.githubusercontent.com"` | `certificateOidcIssuer` is the OIDC issuer of the Fulcio certificate (default `"https://token.actions.githubusercontent.com"`). |
| `artifacts.verify.trustedRootFile` | string | empty | `trustedRootFile` is the sigstore trusted\_root.json for offline verification; empty = fetch via TUF from the public Sigstore instance. |
| `artifacts.verify.tufRepositoryUrl` | string | empty | `tufRepositoryUrl` is the base URL of the Sigstore TUF repository or of a 1:1 mirror of it (default "" = `https://tuf-repo-cdn.sigstore.dev`); the embedded Sigstore root remains the trust anchor. Ignored if `artifacts.verify.trustedRootFile` is set. TUF requests use the same HTTP client as the other outbound connections (proxy environment, `outbound.caFile`, `outbound.allowedCidrs`, `artifacts.sourceAuth` for the origin of `artifacts.sourceBaseUrl`). A mirror must not cache timestamp.json for long (it changes daily). |
| `artifacts.maxCacheBytes` | integer | `21474836480` (20 GiB) | `maxCacheBytes` limits the size of the cache (default 20 GiB); least recently used versions are evicted. |
| `artifacts.downloadTokenTtl` | duration | `24h` | `downloadTokenTtl` limits the age of updater download tokens (default 24h; 0 = no expiry). A token (bound to artifact id and updater agent id) is accepted only while that agent has a package offer referencing the artifact; the offer is re-sent with a fresh token once the token is older than half of the TTL. |
| `artifacts.sourceAuth` | object | - | `sourceAuth` are the credentials myriarchd sends to the artifact mirror (`artifacts.sourceBaseUrl`), e.g. an Artifactory access token or a Nexus user. They are sent ONLY to requests with the same origin (scheme, host, port) as `artifacts.sourceBaseUrl`, never to redirect targets on other hosts. Hosts that download from the mirror themselves are not affected. |
| `artifacts.sourceAuth.username` | string | empty | `username` is the user for basic authentication (Nexus); only together with `artifacts.sourceAuth.passwordFile`. |
| `artifacts.sourceAuth.passwordFile` | string | empty | `passwordFile` holds the password or user token for basic authentication. |
| `artifacts.sourceAuth.tokenFile` | string | empty | `tokenFile` holds a bearer token (Artifactory access token). It excludes `artifacts.sourceAuth.username` and `artifacts.sourceAuth.passwordFile`. |
| `artifacts.proxyUrl` | string | empty | `proxyUrl` is the forward proxy for outbound downloads (optional). |
| `artifacts.allowedRedirectHosts` | list of strings | empty | `allowedRedirectHosts` lists the hosts upstream downloads may be redirected to (exact or `*.suffix`; https only, at most 5 hops). Default: the GitHub release asset hosts plus the host of `artifacts.sourceBaseUrl`. |
| `artifacts.allowUnverified` | boolean | `false` | `allowUnverified` permits `artifacts.verify.requireChecksum` = false or `artifacts.verify.requireSignature` = false in production (lab use only). |

## install

`install` configures the installation of hosts from the server (ADR-0046): the plan that a host asks for with its enrollment token, the install grants for its downloads, the installer bundles of the release that the server runs and the rendered install commands.

| Key | Type | Default | Description |
|---|---|---|---|
| `install.enabled` | boolean | `true` | `enabled` turns the installation of hosts from the server on (default true). When it is off, every route under `/install` answers 404 and the server fetches no installer bundle. |
| `install.grantTtl` | duration | `1h` | `grantTtl` is the lifetime of an install grant, the credential with which a host downloads the collector and the supervisor after it asked for its plan (default 1h; when neither the file nor the environment sets it, it is `artifacts.downloadTokenTtl` if that is shorter). A grant is also bound to the enrollment token of that request and stops working when the token is revoked or expires. In production the value must be positive and at most `artifacts.downloadTokenTtl`. |
| `install.bundleSourceBaseUrl` | string | `"https://github.com/myriarch/myriarch/releases/download"` | `bundleSourceBaseUrl` is where the installer bundles of the release that the server runs come from (default `"https://github.com/myriarch/myriarch/releases/download"`). A mirror keeps the layout `<base>/v<version>/<asset>` with `checksums.txt` and its Sigstore bundle `checksums.txt.sigstore.json` next to the archives; the server verifies both against the identity of the release workflow, whichever server they come from. Production requires https. The value must not be empty while `install.enabled` is on. |
| `install.platforms` | list of strings | `["linux/amd64", "linux/arm64", "windows/amd64"]` | `platforms` lists the platforms that a host can be installed on from the server, as "os/arch" (default linux/amd64, linux/arm64 and windows/amd64, which is all that is supported). The plan refuses a host on another platform, and the server prepares an installer bundle for each platform listed. |

## outbound

`outbound` is the destination policy of all outbound connections.

| Key | Type | Default | Description |
|---|---|---|---|
| `outbound.allowedCidrs` | list of strings | empty | `allowedCidrs` opens address ranges (or single IPs) that are denied by default: loopback, private (RFC 1918), shared, link-local (metadata), unique-local and reserved ranges. Example: \["10.20.0.0/16"\] for an AWX server or Git server in the internal network. Applies to the resolved address of every connection, including redirects. |
| `outbound.caFile` | string | empty | `caFile` is a PEM file with ADDITIONAL trusted CA certificates (on top of the system roots) for ALL outbound TLS connections of myriarchd: OIDC, Git over HTTPS, GitHub API and raw, the artifact upstream or mirror, the Sigstore TUF repository, webhook and AWX executors, notification channels. Typically the internal CA of the company. myriarchd refuses to start if the file is unreadable or contains no certificate. |

## executors

`executors` holds executor defaults.

| Key | Type | Default | Description |
|---|---|---|---|
| `executors.allowedWebhookHosts` | list of strings | empty | `allowedWebhookHosts` restricts webhook/AWX targets (host, host:port, `*.example.com`; `http://host` allows plain HTTP). An empty list refuses every webhook and AWX target (fail closed) in every mode; it does not stop startup. |
| `executors.defaultTimeout` | duration | `30s` | `defaultTimeout` is the timeout for outbound executor calls (default 30s). |
| `executors.maxTimeout` | duration | `5m` | `maxTimeout` caps the timeouts requested by Executor objects in Git (default 5m). |
| `executors.secretEnvPrefix` | string | `"MYRIARCH_EXEC_"` | `secretEnvPrefix` is the prefix that environment variables must start with when an Executor object references them directly (secretEnv/tokenEnv; default `MYRIARCH_EXEC_`), so Git cannot read arbitrary variables. |
| `executors.secretDir` | string | empty | `secretDir` is the only directory executor secret and CA files (secretFile/tokenFile/caFile) are read from; names must be relative and may not leave it. Empty = file references are refused. |
| `executors.secrets` | map of objects | none | `secrets` is the registry of named secrets. An Executor object may use a registry name as secretEnv/tokenEnv/secretFile/tokenFile value; the secret is then only sent to the listed hosts. Not settable through an environment variable. |
| `executors.secrets.<name>.env` | string | none | `env` is the environment variable that holds the secret (or `executors.secrets.<name>.file`). |
| `executors.secrets.<name>.file` | string | none | `file` is a file below `executors.secretDir` (or an absolute path configured here by the operator). |
| `executors.secrets.<name>.allowedHosts` | list of strings | none | `allowedHosts` lists the only destinations this secret may be sent to (host, host:port or `*.example.com`). Required. |
| `executors.secretEnvHosts` | map of string lists | empty | `secretEnvHosts` binds environment variables that are referenced by their prefixed name (secretEnv/tokenEnv) to the only destinations they may be sent to (host, host:port or `*.example.com`). In production an unbound prefixed variable is refused. Not settable through an environment variable. |
| `executors.secretFileHosts` | map of string lists | empty | `secretFileHosts` does for files below `executors.secretDir` (secretFile/tokenFile) what `executors.secretEnvHosts` does for environment variables: it binds them, keyed by the relative file name, to the only destinations they may be sent to. Not settable through an environment variable. |

## rollouts

`rollouts` configures the rollout engine.

| Key | Type | Default | Description |
|---|---|---|---|
| `rollouts.tickInterval` | duration | `5s` (dev: `2s`) | `tickInterval` is the interval of the engine loop on the leader (default 5s). |
| `rollouts.defaultTimezone` | string | `"UTC"` | `defaultTimezone` is the time zone for maintenance windows when an agent has no valid timezone attribute (default "UTC"). |
| `rollouts.autoStart` | boolean | `true` | `autoStart` starts rollouts created by GitOps immediately (default true). |
| `rollouts.executorTimeout` | duration | `2m` | `executorTimeout` is the hard limit of one executor call (Start/Cancel of the webhook/AWX/updater executors; default 2m). The engine never waits for a slow executor inside a tick: calls run in the background and their results are applied on a later tick. |

## notifications

`notifications` configures notification channels and routing.

| Key | Type | Default | Description |
|---|---|---|---|
| `notifications.channels` | list of objects | none | `channels` lists the notification channels. ${VAR} expansion works inside the entries. Not settable through an environment variable. |
| `notifications.channels[].name` | string | none | `name` identifies the channel; unique within the list. |
| `notifications.channels[].type` | string | none | `type` is the kind of the channel: "webhook", "teams" or "slack". |
| `notifications.channels[].url` | string | none | `url` is the target of the channel (incoming webhook URLs are secrets). Either this key or `notifications.channels[].urlFile` is required; production requires https. Secret: `myriarchd config print` shows `<redacted>`. |
| `notifications.channels[].urlFile` | string | none | `urlFile` reads the URL from a file (takes precedence over `notifications.channels[].url`; always read). |
| `notifications.channels[].secretFile` | string | none | `secretFile` holds the HMAC secret for type webhook (header X-Myriarch-Signature). |
| `notifications.channels[].events` | list of strings | none | `events` lists the events routed to this channel, e.g. \["release.new", "rollout.\*"\]; empty = all. |
| `notifications.channels[].minSeverity` | string | none | `minSeverity` is the lowest severity routed to this channel: "info" (default), "warning" or "critical". |

## retention

`retention` configures housekeeping (leader job, runs hourly). A value of 0 turns off the cleanup of that kind of data.

| Key | Type | Default | Description |
|---|---|---|---|
| `retention.agentEvents` | duration | `720h` | `agentEvents` is how long agent events are kept (default 30 days). |
| `retention.audit` | duration | `9600h` | `audit` is how long audit events are kept (default 400 days). |
| `retention.offlineAgents` | duration | `720h` | `offlineAgents` is how long an agent may stay offline before housekeeping deletes it (default 30 days; 0 = keep). |
| `retention.finishedRollouts` | duration | `4320h` | `finishedRollouts` is how long finished rollouts are kept (default 180 days). |
| `retention.unreferencedConfig` | duration | `168h` | `unreferencedConfig` is the age after which rendered and effective configurations that nothing references are removed (default 7 days). |
| `retention.gitopsSyncHistory` | duration | `720h` | `gitopsSyncHistory` is how long the history of GitOps syncs is kept (default 30 days). |

## telemetry

`telemetry` configures myriarchd's own logs and metrics.

| Key | Type | Default | Description |
|---|---|---|---|
| `telemetry.logLevel` | string | `"info"` (dev: `"debug"`) | `logLevel` is the log level: debug, info, warn or error (default info). |
| `telemetry.logFormat` | string | `"json"` (dev: `"text"`) | `logFormat` is the log format: json or text (default json; dev text). |
| `telemetry.metrics` | boolean | `true` | `metrics` enables /metrics (default true). |

## ha

`ha` configures multi-replica operation.

| Key | Type | Default | Description |
|---|---|---|---|
| `ha.replicaId` | string | empty | `replicaId` identifies the replica (default: hostname + "-" + random suffix). |
| `ha.leaderLockKey` | integer | `7012024` | `leaderLockKey` is the Postgres advisory lock key of the leader election (default 7012024). |
| `ha.heartbeatInterval` | duration | `10s` | `heartbeatInterval` is the interval of the replica registry heartbeat (default 10s). |

## ui

`ui` configures the language options the web UI receives through the public `GET /auth/config` endpoint.

| Key | Type | Default | Description |
|---|---|---|---|
| `ui.defaultLanguage` | string | `"en"` | `defaultLanguage` is the BCP 47 tag the UI uses when the user has no stored choice and no browser language matches a shipped locale (default "en"). |
| `ui.languages` | list of strings | empty | `languages` restricts the languages the UI offers (empty = every language the UI ships). When set, it must contain `ui.defaultLanguage`. Entries match a shipped language by the full tag or the primary subtag ("de-DE" enables "de"). |

## dev

`dev` holds the settings of the development mode. Used only by `myriarchd dev`.

| Key | Type | Default | Description |
|---|---|---|---|
| `dev.dataDir` | string | `".dev"` | `dataDir` is the directory for embedded Postgres, the clone and the artifacts (default ".dev"). |
| `dev.postgresPort` | integer | `54329` | `postgresPort` is the port of the embedded Postgres (default 54329). |
| `dev.seedEnrollmentToken` | string | `"myre_devtoken_insecure_do_not_use_in_prod"` | `seedEnrollmentToken` is the enrollment token that is created at startup (default "myre\_devtoken\_insecure\_do\_not\_use\_in\_prod"). Secret: `myriarchd config print` shows `<redacted>`. |
