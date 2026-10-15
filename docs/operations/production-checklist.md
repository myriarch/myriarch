---
title: "Production checklist"
weight: 20
description: "The settings of myriarchd serve that matter for security, what the server refuses at startup, and how to check a configuration without starting it."
---

`myriarchd serve` (and the Helm chart `deploy/helm/myriarch`, which renders the same configuration) checks its
configuration at startup and refuses unsafe values. This page lists what is refused, the settings you must decide for a real
installation, and how to check a configuration before you roll it out. The reasoning behind the controls is in the
[security model](../security/model.md); how people sign in is in [Authentication](authentication.md). A complete,
validated example is [deploy/examples/production/myriarchd.yaml](../../deploy/examples/production/myriarchd.yaml).

## Refused at startup

In production mode the server does not start when one of these holds. Each message names the setting.

| Setting | Rule |
|---|---|
| `server.publicUrl` | Required, and an https URL (session cookies are `Secure`). |
| `server.tls.terminatedUpstream` | Must be `true` when `myriarchd` serves plain HTTP (no `server.tls.certFile`). It confirms that TLS ends in front of the server and that the plain port is reachable only from there. |
| `database.dsn` or `database.dsnFile` | Required. |
| `auth` | At least one login path: `auth.oidc` or `auth.bootstrapAdmin` with `passwordHashFile` or `tokenFile`. The development login is never accepted. |
| `auth.devLogin.enabled`, `auth.cookieSecure` | The development login must be off and `cookieSecure` must stay `true`. |
| `opamp.requireEnrollmentToken` | May be `false` only together with `opamp.requireClientCert: true` (pure mTLS). |
| `opamp.enableCompression` | Refused unless `opamp.allowUnboundedCompression: true` acknowledges that decompressed messages are not bounded by `opamp.maxMessageSize`. |
| `artifacts.verify.requireChecksum`, `requireSignature` | Must stay `true` unless `artifacts.allowUnverified: true` (lab use). |
| `install.grantTtl`, `install.bundleSourceBaseUrl` | While `install.enabled` is `true` (the default): the grant lifetime must be positive and at most `artifacts.downloadTokenTtl`, and the bundle source an https URL without credentials, query or fragment. Available from v0.2.0. |
| URLs | `artifacts.sourceBaseUrl`, `releases.githubApiUrl`, `releases.rawBaseUrl` and notification URLs must be https. `gitops.repoUrl` must be https or ssh; `dir:` and local paths are for development only, and the Git URL must not carry inline credentials (use `gitops.auth.tokenFile` or an SSH key). |
| `notifications.channels[]` | A channel of type `webhook` needs `secretFile` (at least 32 bytes). |

## Settings to decide

| Setting | Meaning |
|---|---|
| `server.tls.terminatedUpstream: true` | The ingress or load balancer terminates TLS 1.2 or newer, and only trusted networks reach the plain port. With the Helm chart these are the sources of its NetworkPolicy and, by default, every pod of the release namespace (`networkPolicy.ingress.allowSameNamespace`). Alternatively set `server.tls.certFile` and `keyFile` and let `myriarchd` terminate TLS. |
| `server.trustedProxies` | CIDRs of the ingress or gateway pods, whose `X-Forwarded-*` headers are honored. Without them every client looks like the proxy: per-address rate limits, audit source addresses and the brute-force protection then count the proxy address for everyone. The Helm chart requires it while an ingress is enabled. Production refuses ranges that cover every address (`0.0.0.0/0`, `::/0`); a range shorter than /8 (IPv4) or /32 (IPv6) is a warning, and is refused while `opamp.clientCertHeader` is set. |
| `server.metricsListenAddr` | Default `:9090` serves `/metrics` (and `/healthz`) on a separate port without authentication. Restrict it by network (the chart's NetworkPolicy does). An empty value serves `/metrics` on the main listener, which an ingress usually exposes; production logs a warning, so block `/metrics` at the ingress then. |
| `auth.oidc.roleMapping` | Maps groups or roles of the identity provider to `admin`, `operator` and `viewer`. **Deny by default**: users without a mapped group get no access, and `auth.oidc.defaultRole` stays empty. |
| `auth.oidc.clientSecretFile` **or** `clientCertificateFile` + `clientKeyFile` | Client authentication at the token endpoint: a secret, or a certificate (`private_key_jwt`, RSA key in PKCS#1 or PKCS#8 form). Exactly one of the two. |
| `auth.sessionTtl`, `auth.sessionMaxAge` | Idle timeout (default 2 h, sliding) and absolute session lifetime (default 12 h). |
| `auth.apiTokenMaxTtl` | Maximum lifetime of API tokens (default 90 days). Removing a user at the identity provider does not end the API tokens of that user; disable the user in myriarch for that ([Authentication](authentication.md#offboarding-a-user)). `0` means unlimited and logs a warning. |
| `auth.bootstrapAdmin` | The break-glass administrator. Keep it for emergencies with the credential in a vault, or turn it off after single sign-on works; with OIDC enabled it is off unless `keepWithOidc: true`. |
| `auth.downloadTokenSecretFile` | A fixed HMAC key (32 or more random bytes) for artifact download tokens. By default one is generated and stored in the database. `artifacts.downloadTokenTtl` (default 24 h, must be positive in production) limits their age. |
| `opamp.requireEnrollmentToken`, `opamp.requireClientCert` | By default every agent needs an enrollment token. Client certificates (mTLS) need `server.tls.clientCaFile`, and either `server.tls.certFile` or `opamp.clientCertHeader` from a trusted proxy. |
| `opamp.autoRebindOnTokenRotation` | Default `false`: an agent whose bound token was revoked needs `POST /api/v1/agents/{id}/actions/reenroll`. `true` lets it move to a valid new token itself (audit event `agent.rebind`). |
| `executors.allowedWebhookHosts` | Permitted webhook and AWX targets. An empty list refuses every target (fail closed, in every mode) and does not block startup. Secrets reach executors only through `MYRIARCH_EXEC_*` environment variables, `executors.secretDir` or `executors.secrets`, and each secret must be bound to its destination hosts (`executors.secretEnvHosts`, `executors.secretFileHosts`, or `allowedHosts` in `executors.secrets`). |
| `outbound.allowedCidrs` | Opens internal destinations (Git server, AWX, mirror, identity provider) for outbound connections. Loopback, private ranges, link-local and cloud metadata addresses, and unique-local addresses are blocked otherwise. |
| `outbound.caFile` | A PEM file with **additional** trusted CAs, for **all** outbound TLS connections: OIDC, Git, GitHub, the artifact mirror, TUF, executors and notifications. The server does not start if the file is missing or holds no certificate. Proxies come from the environment of the process: `HTTPS_PROXY`, `NO_PROXY`. |
| `artifacts.sourceBaseUrl`, `artifacts.sourceAuth` | An internal mirror of the release downloads (layout `<base>/<tag>/<asset>`) and its credentials: `tokenFile` (bearer) or `username` + `passwordFile` (basic). Credentials go only to the origin (scheme, host, port) of `sourceBaseUrl`, never to redirect targets on other hosts, and the files are re-read when they change. `artifacts.allowedRedirectHosts` lists the hosts a mirror may redirect to. |
| `artifacts.verify.tufRepositoryUrl`, `artifacts.verify.trustedRootFile` | The Sigstore trust base behind a proxy. The default is `tuf-repo-cdn.sigstore.dev` over the proxy. Use a 1:1 TUF mirror (https; do not cache `timestamp.json` for long) or, as a fallback, a fixed `trusted_root.json`. |
| `artifacts.agentBaseUrls` | Base URLs that updaters download from, chosen per updater by host and port. `http://` entries need `artifacts.allowInsecureAgentBaseUrls: true` and belong only to cluster-internal services. |
| `install.enabled`, `install.grantTtl`, `install.platforms` | *Available from v0.2.0.* The installation of hosts from the server (the plan for a host and the install grants for its downloads, [Server behaviour](../design/opamp-server.md#installation-from-the-server)) is on by default. Set `install.enabled: false` where hosts are installed on the manual path only: the routes then answer 404. `install.grantTtl` (default 1 h) is the lifetime of the grants of a plan, `install.platforms` lists the platforms. The routes need an enrollment token whatever `opamp.requireEnrollmentToken` says, and the plan names only releases that the release tracker knows (`releases.enabled`). |
| `notifications.channels[].secretFile` | HMAC key (at least 32 bytes) for webhook channels. Receivers check the timestamp and drop repeated `X-Myriarch-Delivery` values. |
| `gitops.auth`, `gitops.webhookSecretFile` | Read-only credentials for the repository (token, or SSH key plus known hosts) and the shared secret for `POST /api/v1/gitops/webhook`. |
| `database.dsnFile` | The connection string with `sslmode=verify-full` (or `verify-ca`): `require` encrypts but does not verify the server, and production warns about a remote database without verification. No connection pooler in transaction mode. |
| `artifacts.dir`, `gitops.cloneDir` | Directories that only the server account can write. Empty values use fixed names in the temporary directory, which production reports as a warning. |
| Certificates | The TLS certificate, key and client CA of `myriarchd` are read at start: rotate them with a (rolling) restart. |

The defaults are restrictive on purpose: WebSocket compression off, `opamp.maxMessageSize` 4 MiB, download tokens valid for
24 hours, checksum and Sigstore verification on. Secret files should be readable by the server account only (mode `0400`
or `0440`); the server logs a warning at start for every secret file that others can read (see
[Authentication](authentication.md)).

At start `myriarchd` logs a warning for every setting that weakens a control: a non-empty `auth.oidc.defaultRole`,
provider-specific OIDC hints (see [Authentication](authentication.md)), the bootstrap admin as the only login path or next
to OIDC, expired declarative enrollment tokens, enabled compression, empty `server.trustedProxies`, disabled artifact
verification, plain `http://` entries in `artifacts.agentBaseUrls`, and mirror credentials combined with the public GitHub
URL, the installation from the server with the release tracker off, a metrics endpoint on the main listener, a database connection to a remote host without certificate verification,
the default cache and clone directories, secrets set as literal values in the configuration file, credentials in
`artifacts.proxyUrl` and an unlimited `auth.apiTokenMaxTtl`. `myriarchd config validate` prints the same warnings.

## Check a configuration without starting the server

```bash
myriarchd config validate --config myriarchd.yaml                 # also reads the files it refers to (secrets, CA, certificates)
myriarchd config validate --config myriarchd.yaml --skip-files    # without files: for CI and `helm template`, where secrets are absent
myriarchd config print    --config myriarchd.yaml --skip-files    # the effective configuration, secrets redacted
```

The command loads the configuration exactly like `myriarchd serve`: defaults, the YAML file (unknown keys are errors),
`${VAR}` expansion and `MYRIARCH_*` environment overrides. It runs every check that `serve` makes before it touches the
database or the network. Nothing is started and no connection is made. On success it prints `OK: <file> is valid` and one
`WARNING: <text>` line per warning on standard output; each problem is a line `ERROR: <path>: <message>` on standard error.
`config print` shows the effective configuration as YAML, with the DSN, client secrets, webhook secrets, notification URLs
and development passwords replaced by `<redacted>`; it validates first. The [server configuration](../reference/configuration.md)
reference lists every key with its type, default and effect.

Exit codes: `0` valid (warnings are allowed), `1` invalid (YAML syntax, an unknown key, a failed check), `2` wrong usage (no
`--config`, a file that cannot be read, an unknown option). An example:

```text
$ myriarchd config validate --config deploy/examples/production/myriarchd.yaml --skip-files
OK: deploy/examples/production/myriarchd.yaml is valid
$ myriarchd config validate --config broken.yaml --skip-files
ERROR: auth: no login path is configured for serve mode: enable auth.oidc (single sign-on) or auth.bootstrapAdmin with passwordHashFile or tokenFile (break-glass administrator); the dev login exists only in "myriarchd dev"
```

Without `--skip-files`, file and certificate problems appear as errors; they disappear when the secrets are mounted. Use
`--skip-files` in CI. The options are in the [`myriarchd config validate` reference](../reference/cli/myriarchd_config_validate.md);
the other commands are in the [`myriarchd` reference](../reference/cli/myriarchd.md) and the server start in the
[`myriarchd serve` reference](../reference/cli/myriarchd_serve.md).

For Kubernetes, the settings above are values of the Helm chart; see [deploy/helm/myriarch/README.md](../../deploy/helm/myriarch/README.md)
and the troubleshooting page for symptoms: [Troubleshooting](troubleshooting.md).
