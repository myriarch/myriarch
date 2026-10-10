---
title: "Authentication"
weight: 10
description: "How people and agents authenticate to myriarchd: login paths, the bootstrap admin, OpenID Connect recipes and declarative enrollment tokens."
---

This guide describes how people and agents authenticate to `myriarchd`: the login paths of `serve` mode, the bootstrap
admin (break-glass account), OpenID Connect (OIDC) single sign-on with recipes for seven identity providers, and
declarative enrollment tokens for agents. All examples use the placeholders `example.com`, `.test` and the documentation
ranges of RFC 5737.

Contents: [1 Login paths](#1-login-paths) · [2 Bootstrap admin](#2-bootstrap-admin) · [3 OIDC reference](#3-oidc-reference) ·
[4 Provider hints](#4-provider-hints-what-myriarchd-checks) · [5 Provider recipes](#5-provider-recipes) ·
[6 Declarative enrollment tokens](#6-declarative-enrollment-tokens) · [7 Troubleshooting](#7-troubleshooting)

---

## 1. Login paths

`myriarchd serve` offers two ways for people to sign in. At least one of them must be configured:

| Login path | Purpose | Keys |
|---|---|---|
| Single sign-on (OIDC) | Day-to-day operation with the accounts, groups and MFA of your identity provider | `auth.oidc.*` (section 3) |
| Bootstrap admin | Initial setup and emergencies, independent of any identity provider | `auth.bootstrapAdmin.*` (section 2) |

`myriarchd dev` has a third path, the development login with well-known users. It exists only in dev mode, is refused
by `serve` and is never used by the Helm chart or by any production example.

**Startup check.** `serve` refuses to start when neither path is configured. `myriarchd config validate`, `serve` and
`migrate` report the same text and exit with status 1:

```text
ERROR: auth: no login path is configured for serve mode: enable auth.oidc (single sign-on) or auth.bootstrapAdmin with passwordHashFile or tokenFile (break-glass administrator); the dev login exists only in "myriarchd dev"
```

Agents do not use these paths. They authenticate with enrollment tokens or client certificates (section 6).

**Recommended setup.** Start with the bootstrap admin to reach the UI, configure OIDC, check that an administrator can
sign in through it, then deactivate the bootstrap admin (section 2.5). Keep the credential in a vault for emergencies.

---

## 2. Bootstrap admin

The bootstrap admin is a single, configuration-managed administrator account. It is meant for the initial setup and for
emergencies (for example an unreachable identity provider). It is not a general local user management: there is exactly
one account, its role is always `admin`, and the configuration is the only source of truth.

### 2.1 Configuration keys

```yaml
auth:
  bootstrapAdmin:
    enabled: true
    username: admin                                   # default "admin"; 1 to 64 characters: letters, digits, ".", "_", "@", "-"
    passwordHashFile: /etc/myriarchd/bootstrap/password-hash   # bcrypt hash (password sign-in)
    tokenFile: /etc/myriarchd/bootstrap/token                  # one-time token (token sign-in)
    keepWithOidc: false                               # keep the account active while auth.oidc is enabled
```

| Key | Environment override | Meaning |
|---|---|---|
| `auth.bootstrapAdmin.enabled` | `MYRIARCH_AUTH_BOOTSTRAPADMIN_ENABLED` | Switch. Default `false`. |
| `auth.bootstrapAdmin.username` | `MYRIARCH_AUTH_BOOTSTRAPADMIN_USERNAME` | User name for password sign-in. |
| `auth.bootstrapAdmin.passwordHashFile` | `MYRIARCH_AUTH_BOOTSTRAPADMIN_PASSWORDHASHFILE` | Absolute path of a file with a bcrypt hash. |
| `auth.bootstrapAdmin.tokenFile` | `MYRIARCH_AUTH_BOOTSTRAPADMIN_TOKENFILE` | Absolute path of a file with the one-time token. |
| `auth.bootstrapAdmin.keepWithOidc` | `MYRIARCH_AUTH_BOOTSTRAPADMIN_KEEPWITHOIDC` | See section 2.5. |

At least one of `passwordHashFile` and `tokenFile` is required while the account is enabled. Both may be set; the login
page then offers both. The credentials are read from files only. There is no inline password or token in the
configuration, so Git, logs and `myriarchd config print` never contain them.

### 2.2 Password sign-in

Create a password hash with the built-in helper. The password must be piped in on standard input (first line; a terminal
is refused so that the password is never echoed). The helper requires 12 to 72 bytes and prints a bcrypt hash. The cost defaults to 12 and may be 10 to 14 (`--cost N`). Exit status:
0 success, 1 invalid input, 2 usage error.

```bash
# Generate a password and keep it in your password manager, then hash it:
PASSWORD="$(openssl rand -base64 24 | tr -d '\n')"
printf '%s' "$PASSWORD" | myriarchd auth hash-password > password-hash
unset PASSWORD
```

The hash file must contain a bcrypt hash (`$2a$`, `$2b$` or `$2y$`) with cost 10 or higher. Mount it read-only into the
pod or container and point `passwordHashFile` at it. Verification runs `bcrypt` on every attempt, also when the user
name does not match, so timing does not reveal whether the user name exists.

**File permissions.** Every secret file the server reads should be readable by the server account only (mode 0400 or
0440; the chart mounts its Secrets with 0440). Outside Windows the server logs a warning at start for each such file that
other users of the host can read or write. The message names the setting and the path (`setting=database.dsnFile
file=/etc/myriarchd/secrets/database-dsn mode=0644`), never the content. The check covers the files of the features that
are active:

| Files | Settings |
|---|---|
| Bootstrap admin | `auth.bootstrapAdmin.passwordHashFile`, `tokenFile` |
| Declarative enrollment tokens | `opamp.enrollmentTokens[].tokenFile` |
| Database and listener | `database.dsnFile`, `server.tls.keyFile` |
| Authentication | `auth.downloadTokenSecretFile`, `auth.oidc.clientSecretFile` and `clientKeyFile` (while `auth.oidc.enabled`) |
| GitOps and releases | `gitops.auth.tokenFile`, `gitops.auth.sshKeyFile` and `gitops.webhookSecretFile` (while `gitops.enabled`), `releases.tokenFile` (while `releases.enabled`) |
| Artifact mirror | `artifacts.sourceAuth.tokenFile`, `artifacts.sourceAuth.passwordFile` |
| Notifications and executors | `notifications.channels[].urlFile` and `secretFile`, `executors.secrets.<name>.file` |

Certificates, CA bundles and known-hosts files hold no secret and are not checked. Secret files that a GitOps executor
names below `executors.secretDir` are read when an executor runs and are not checked at start.

### 2.3 One-time token sign-in

A token is a random secret of 32 to 512 characters without whitespace. It works for exactly one successful sign-in.

```bash
openssl rand -base64 36 | tr -d '\n' > bootstrap-token     # 48 characters
chmod 0600 bootstrap-token
```

Properties:

- The file is read again on **every** attempt (at most 4 KiB, whitespace trimmed). Rewriting the file rotates the token
  without a restart.
- The comparison is constant-time over SHA-256 digests.
- A successful sign-in consumes the token atomically: myriarchd stores the SHA-256 digest of the token in the key-value
  store under `authn/bootstrap-token-used/` before it creates the session. The marker is shared by all replicas and is
  never purged, so a used token stays unusable even if the file keeps its content. Write a new value to the file to
  sign in again.
- If the file cannot be read, the attempt fails with `503` (reason `credential_unavailable`, a warning in the log without
  file content).

Use the password mode when you want a credential that stays valid for emergencies; use the token mode for the first
sign-in of an installation and rotate it afterwards.

### 2.4 Sign-in, UI and API

The login page shows a bootstrap section when the account is active. If single sign-on or the dev login is offered, the
section is collapsed behind the link "Use the bootstrap admin"; if it is the only method, the form is expanded. After
sign-in every page shows a banner ("Signed in with the bootstrap admin") until the user signs out, and the user list
marks the account as managed by configuration: its role and status cannot be changed in the UI. `PATCH /api/v1/users/{id}`
on this user answers `412` (`the bootstrap admin is managed by configuration (auth.bootstrapAdmin)`).

API: `POST /api/v1/auth/bootstrap-login` with exactly one of

```json
{ "username": "admin", "password": "<password>" }
```

```json
{ "token": "<one-time token>" }
```

The response is the signed-in user (`authMethod: "bootstrap"`) with the session and CSRF cookies, like any other login.
`GET /api/v1/auth/config` reports `bootstrapPasswordEnabled` and `bootstrapTokenEnabled`, and `GET /api/v1/system/info`
reports `features.bootstrapAdmin`. Status codes: `400` (not exactly one of password and token), `401` (one message for
every failure: `invalid bootstrap credentials`), `404` (the bootstrap admin is not active), `429` (rate limited, header
`Retry-After`), `503` (credential file unreadable).

### 2.5 Activation and deactivation

The account is **active** when `enabled` is true, a credential file is set, and either `auth.oidc` is disabled or
`keepWithOidc` is true. The configuration decides, and every start reconciles the stored user with it:

| Situation | Result |
|---|---|
| `enabled: true`, OIDC disabled | Active. The server logs a warning: the bootstrap admin is the only login path. |
| `enabled: true`, OIDC enabled, `keepWithOidc: false` | Inactive. The user is disabled, its sessions are removed and its API tokens revoked, `info` log at start. |
| `enabled: true`, OIDC enabled, `keepWithOidc: true` | Active next to OIDC. A warning reminds you that every sign-in is audited as `auth.bootstrap.login`. |
| `enabled: false` | Inactive. The user is disabled, its sessions are removed and its API tokens revoked. |
| `username` changed | The user of the old name is disabled, its sessions are removed and its API tokens revoked; a new user is created. |

Deactivation also revokes the API tokens created by the bootstrap admin for good (the count is part of the audit entry
`auth.bootstrap.deactivate` as `apiTokensRevoked`): enabling the account again later does not bring them back. The
revocation converges across restarts:

- The steps for one account (revoke the tokens, remove the sessions, disable the account) run in one database
  transaction, so a failing start leaves the account either as it was or completely deactivated. The transaction starts
  with a lock on the user row and decides on the locked state: when several replicas start together, one does the work
  and writes the audit entry, the others find nothing to change and write nothing (and revoke nothing that was issued
  after the first one finished).
- Every start checks the accounts that are disabled already and removes sessions and tokens they still hold, for example
  after an older version disabled an account without revoking its tokens. That start writes an
  `auth.bootstrap.deactivate` entry with `alreadyDisabled: true`; a start that finds nothing to remove writes nothing.
- When an inactive account becomes active again, its sessions are removed and its tokens revoked **before** it is enabled
  (one transaction). The counts are part of the entry `auth.bootstrap.activate`.

Every replica checks the configuration it runs with: a replica that is not configured for the
bootstrap admin rejects its sessions and tokens even if another replica has not yet reconciled the database.

To retire the account after the initial setup, set `auth.bootstrapAdmin.enabled: false` (or enable OIDC and leave
`keepWithOidc` unset) and roll the change out to all replicas.

### 2.6 Audit, metrics and logs

| Audit action | When | Details |
|---|---|---|
| `auth.bootstrap.login` | Every sign-in attempt, result `success` or `failure` | `method: "bootstrap"`, `credential: "password"` or `"token"`; on failure `reason`: `bad_credentials`, `token_used`, `rate_limited`, `credential_unavailable` or `user_disabled`; for `rate_limited` also `limitedBy`: `client address` or `account`. Source address and request ID as for other logins. |
| `auth.bootstrap.activate` | The account was created or re-enabled | `username`, `credentials` (`["password"]`, `["token"]` or both), `reason`: `created` or `enabled_in_config`; for `enabled_in_config` also `sessionsRemoved` and `apiTokensRevoked` (what the account held from its earlier life). Actor: system. |
| `auth.bootstrap.deactivate` | The account was disabled, or an account that was disabled earlier still held sessions or API tokens | `username`, `reason`: `oidc_enabled`, `disabled_in_config` or `username_changed`, `sessionsRemoved`, `apiTokensRevoked`; `alreadyDisabled: true` when only leftovers were removed. Actor: system. |
| `user.update` (failure) | Someone tried to change the account through the API | - |

A failed attempt records the given user name (at most 64 characters) as `bootstrap:<name>`, or `bootstrap:token` for a
token attempt. Passwords, tokens, hashes and file contents never appear in audit details or logs. A restart with an
unchanged configuration writes no audit entry. The metric `myriarch_auth_logins_total` counts the attempts with
`method="bootstrap"` and `result` `success`, `failure` or `rate_limited`. Alert on `auth.bootstrap.login` events when
`keepWithOidc` is on: the account should be used rarely.

### 2.7 Rate limit and backoff

Two limits are checked before any file is read and before any bcrypt runs, so floods are cheap to reject:

- **Client address:** 5 attempts per minute, burst 5. The address is the one `server.trustedProxies` resolves; behind an
  ingress without `trustedProxies` all clients share the address of the proxy. IPv6 clients share one limit per /64
  prefix. The same grouping applies to the other failure limiters (invalid API tokens, dev login).
- **Account:** the first 5 consecutive failures are free; every further failure delays the next attempt by 1 s, doubling
  up to 15 minutes. The counter is forgotten after 15 minutes without a failure and reset by a success. Only one
  verification runs at a time per replica: parallel attempts that arrive while another one is being checked are answered
  with `429` (reason `account`), so the failure counter cannot be passed by sending attempts in parallel.

Both answer `429` with `Retry-After`. The limits are kept **per replica**, in memory, and cleared on restart: with N
replicas the effective limit is up to N times higher. Audit entries for rate limiting are written at most once per minute and key. Every failed attempt is
answered after a fixed delay of 300 ms with the same message.

**Trade-off: the account backoff is shared by every client.** The account counter counts failures of the account, not of
a client address, so it cannot be bypassed by rotating source addresses. The price is that anyone who can reach
`POST /api/v1/auth/bootstrap-login` can provoke it: wrong credentials from any client keep the break-glass login in
delays of up to 15 minutes, and the legitimate administrator is refused with `429` during them. The address limit slows
one source down but does not prevent this. An attempt that is refused during a delay does not extend it, so the delay
ends at most 15 minutes after the last failed verification. OIDC sign-in is not affected.

**Recovery.** The counter lives in memory, per replica, and is not stored in the database, so restarting the pod clears
it: `kubectl -n <namespace> rollout restart deployment/<fullname>` (or delete the pod; with several replicas restart every
replica that answers `429`). Without a restart the delay ends by itself. A restart during an attack only buys time:
restrict the access first.

**Recommendation: restrict network access to the bootstrap login endpoint** (`/api/v1/auth/bootstrap-login`) to the
networks from which administrators need the break-glass account:

- Add an allow-list rule for this path at the ingress controller, Gateway or WAF (for example a separate Ingress or Route
  for the path with a source-range restriction); the rest of the host stays reachable for the UI and the API.
- Or block the path at the edge and use `kubectl port-forward` when the account is needed (the NetworkPolicy does not
  affect a port-forward).
- Set `server.trustedProxies`, so that the server itself sees the real client address (otherwise the address limit and
  the audit entries see only the proxy).
- Alert on the audit entries `auth.bootstrap.login` with `reason: rate_limited` and `limitedBy: account`, which show
  that the account is in a delay.

When the account is retired (`enabled: false`), the endpoint answers `404` and none of this applies.

### 2.8 Sessions

A bootstrap session uses the same cookies, CSRF protection and lifetimes as other sessions (`auth.sessionTtl` idle,
default 2 h; `auth.sessionMaxAge` absolute, default 12 h). Sign-out removes it. A session is valid only while the
replica serving the request considers the account active.

### 2.9 Helm chart

The chart renders the configuration and mounts the credentials. Values (top level, not under `config`):

```yaml
bootstrapAdmin:
  enabled: true            # default
  username: admin
  existingSecret: ""       # empty: the chart generates a one-time token Secret
  tokenKey: token          # key of the token in the Secret
  passwordHashKey: ""      # key of a bcrypt hash in existingSecret (password sign-in)
  keepWithOidc: false
```

- **Generated token (default).** With `existingSecret` empty the chart creates the Secret `<fullname>-bootstrap` with a
  random 48-character token under the key `token` and keeps it across upgrades (through `lookup`). It is mounted at
  `/etc/myriarchd/bootstrap/token`. `helm install` prints how to read it; the token is never part of the output:

  ```bash
  kubectl -n <namespace> get secret <fullname>-bootstrap -o jsonpath='{.data.token}' | base64 -d
  ```

  The token works for one sign-in. For a new one, delete the Secret and run `helm upgrade`, or write a new value into
  the Secret.
- **Your own Secret.** Set `existingSecret` and `tokenKey` and/or `passwordHashKey`. The keys are mounted as
  `/etc/myriarchd/bootstrap/token` and `/etc/myriarchd/bootstrap/password-hash`. `passwordHashKey` requires
  `existingSecret`. Create a hash Secret like this:

  ```bash
  printf '%s' "$PASSWORD" | myriarchd auth hash-password > password-hash
  kubectl -n <namespace> create secret generic myriarch-bootstrap --from-file=password-hash=password-hash
  ```

- **OIDC enabled.** The bootstrap admin is active only while `config.auth.oidc.enabled` (or `oidcFromEnvironment`, see
  below) is false or `bootstrapAdmin.keepWithOidc` is true; the chart mounts the credential Secret only then. With OIDC enabled and
  `keepWithOidc` unset the chart still renders `auth.bootstrapAdmin` (without effect), so the server deactivates an
  existing account with the audit reason `oidc_enabled`.
- **No login path.** `helm template` and `helm install` fail (`bootstrapAdmin.enabled: no login path ...`) when
  `config.auth.oidc.enabled`, `oidcFromEnvironment` and `bootstrapAdmin.enabled` are all false.
- **OIDC switched on through the environment.** The chart reads `config.auth.oidc.enabled`. If single sign-on is enabled
  with environment variables instead (`MYRIARCH_AUTH_OIDC_ENABLED=true` in `extraEnv`, or from a ConfigMap or Secret),
  the chart cannot see it. Set `oidcFromEnvironment: true` to declare it: the login path check passes, the bootstrap
  Secret is mounted only with `keepWithOidc` (as the server decides), and the `config.auth.oidc.*File` fields stay in the
  rendered configuration (an empty value drops one, for example `clientSecretFile` when the client secret comes from
  `MYRIARCH_AUTH_OIDC_CLIENTSECRET`). The value only declares; the server reads the environment and refuses to start
  when no login path remains.

  ```yaml
  oidcFromEnvironment: true
  bootstrapAdmin:
    enabled: false
  extraEnv:
    - {name: MYRIARCH_AUTH_OIDC_ENABLED, value: "true"}
    - {name: MYRIARCH_AUTH_OIDC_CLIENTID, value: myriarch}
  ```

- **GitOps tools and `helm template`.** `helm template` cannot read existing Secrets, so a generated value changes on
  every render. With Argo CD, Flux or `helm template | kubectl apply` use `existingSecret`.
- **Pod restarts.** With `secretChecksum.enabled` (default) the pod template carries a checksum of the mounted Secrets,
  so a changed Secret rolls the pods on `helm upgrade` (derived from the uid and resourceVersion of the Secrets, not from
  their content). An upgrade that creates a generated Secret, for example the new bootstrap token after you deleted the
  Secret, rolls the pods as well, so the new token is valid as soon as the rollout is complete.

### 2.10 Security controls at a glance

| Control | Implementation |
|---|---|
| Credential storage | Files only; password as bcrypt hash (cost 10 or higher), token compared as SHA-256 digest; nothing inline in the configuration |
| Brute force | Client-address limit and account backoff before any file read or bcrypt; fixed failure delay; per replica. The account backoff is shared by every client: restrict network access to the endpoint (section 2.7) |
| Replay | Tokens are single use (cluster-wide marker); every sign-in creates a new session |
| Timing | Constant-time comparisons; bcrypt also runs for an unknown user name |
| Single account | One user (`bootstrap:<username>`), role `admin`, no creation or editing through API or UI |
| Source of truth | Configuration; deactivation removes sessions; every replica re-checks the configuration on each request |
| Visibility | Banner on every page, audit event for every attempt, warnings at start, metric by result |
| Information leaks | One error message for every failure; no secrets in logs, audit details or `config print` |

---

## 3. OIDC reference

OIDC uses the authorization code flow with PKCE. `myriarchd` discovers the endpoints from `<issuerUrl>/.well-known/openid-configuration`,
compares the issuer of the discovery document **exactly** with `issuerUrl` (character by character, including a trailing
slash), and validates the ID token (signature, issuer, audience, nonce, expiry).

```yaml
server:
  publicUrl: https://myriarch.example.com
auth:
  oidc:
    enabled: true
    displayName: Example SSO                    # label of the login button
    issuerUrl: https://idp.example.com/realms/ops
    clientId: myriarch
    clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
    scopes: [openid, profile, email]            # default
    groupsClaim: groups                         # default
    nameClaim: name                             # default
    emailClaim: email                           # default
    roleMapping:
      admin: [myriarch-admins]
      operator: [myriarch-operators]
      viewer: [myriarch-viewers]
      users:
        admin: ["email:alice@example.com"]
    defaultRole: ""                             # default: deny
```

| Key | Meaning |
|---|---|
| `issuerUrl` | HTTPS URL of the issuer, exactly as in the discovery document. Provider-specific formats: section 5. |
| `clientId` | Client ID of the registration. |
| `clientSecretFile` / `clientSecret` | Client secret (a file is preferred). Alternative: `clientCertificateFile` + `clientKeyFile` for `private_key_jwt` (PEM certificate and RSA key, PKCS#1 or PKCS#8 without passphrase); exactly one method; certificate files are read again when they change. |
| `scopes` | Must contain `openid`. Default `openid profile email`. Add the scope your provider needs for groups (section 5). |
| `groupsClaim` | Claim with the groups or roles. A literal key is tried first (URL-like claim names), then a dotted path (`realm_access.roles`). The claim may be a string, a list of strings or an **object**; for an object the keys are used (Zitadel project roles). Default `groups`. |
| `nameClaim` | Claim for the display name; falls back to `preferred_username` when empty. Default `name`. |
| `emailClaim` | Claim for the e-mail address that is shown in the UI. Default `email`. `email:` entries of `roleMapping.users` need the default `email`: the verification flag `email_verified` attests only that claim, so with another `emailClaim` the configuration is refused (use `sub:` entries instead). |
| `roleMapping.admin` / `operator` / `viewer` | Group or role values per role. |
| `roleMapping.users.admin` / `operator` / `viewer` | Individual users: `sub:<subject>` matches the subject exactly; `email:<address>` matches the e-mail claim case-insensitively and **only when the ID token has `email_verified` true** (boolean `true` or the string `"true"`). |
| `defaultRole` | Role for users without a match. Default empty: they get `access_denied` and an audit entry with reason `no_role`. |
| `displayName` | Text of the login button. |
| `server.publicUrl` | Required. It defines the redirect URI. |

Environment overrides follow the usual scheme, for example `MYRIARCH_AUTH_OIDC_ISSUERURL`,
`MYRIARCH_AUTH_OIDC_NAMECLAIM` and `MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_ADMIN` (comma list).

**Role resolution.** The role is the highest of the group mapping and the users mapping; if none matches, `defaultRole`;
if that is empty too, the user is denied. Group names are compared exactly (case-sensitive, no Unicode folding), and so
is `sub:` in the users mapping; an `email:` entry is compared ignoring the case of ASCII letters only, and only when the
identity provider states that the address is verified. Create the groups of the mapping so that nobody who can name
groups at the identity provider can create a look-alike. A role changed manually in the UI stays until an administrator changes it again.
Changes in the identity provider apply at the next sign-in, at the latest after `auth.sessionMaxAge`. At least one group
or user entry is required while OIDC is enabled and `defaultRole` is empty, otherwise nobody could sign in and
`myriarchd` refuses to start.

**`defaultRole` caution.** `defaultRole` gives every authenticated user of the identity provider application access.
Set it only if the provider restricts who may use the application (assignment required, internal consent screen). With
a provider that lets any account sign in (for example a Google client with an external consent screen) it turns the UI
into a public one. `myriarchd` logs a warning when it is set.

**Redirect URI.** Register `<server.publicUrl>/api/v1/auth/callback` exactly, for example
`https://myriarch.example.com/api/v1/auth/callback`. Most providers require HTTPS except for `localhost`.

**Private identity providers.**

- `outbound.allowedCidrs` - `myriarchd` blocks outbound connections to loopback, private, link-local and similar ranges.
  Allow the address of an IdP in a private network, for example `outbound.allowedCidrs: [192.0.2.0/24]`.
- `outbound.caFile` - PEM file with additional trusted CAs for all outbound TLS connections (OIDC, Git, mirror, ...).
  Needed when the IdP certificate comes from an internal CA (`x509: certificate signed by unknown authority`).
- Kubernetes `NetworkPolicy` - the chart allows egress to TCP 443 and 5432 by default. For an IdP on another port add it
  to `networkPolicy.egress.extraPorts`. In-cluster destinations (a Dex deployment, for example) are reachable because the
  egress rule also matches all namespaces. `deploy/helm/myriarch/examples/quickstart-dex.yaml` shows the combination for a
  Dex in the same cluster.

**Rate limit of the OIDC endpoints.** `GET /api/v1/auth/login` and `GET /api/v1/auth/callback` are public. Every failed
callback uses one token of a bucket per client address (one per /64 prefix for IPv6; 10 tokens, one new token every 6
seconds). With an empty bucket both endpoints redirect to `/login?error=oidc_error` with a `Retry-After` header, before
any login state is stored and before the identity provider is contacted; the metric
`myriarch_auth_logins_total{method="oidc",result="rate_limited"}` counts these answers. Failures before a valid signed
login state was presented are written to the audit log (`auth.login`) at most once per minute and address. Behind an
ingress set `server.trustedProxies`, otherwise all users share the address of the proxy. The limit is kept per replica,
in memory, and cleared on restart.

**Sessions.** After the callback the server creates a session (cookie `__Host-myriarch_session` where cookies are Secure,
which is the production setting; `myriarch_session` in development mode over plain HTTP) with the lifetimes
`auth.sessionTtl` (idle, default 2 h) and `auth.sessionMaxAge` (absolute, default 12 h). Login state is single use on all
replicas.

### Offboarding a user

Removing a user from a group or from the identity provider ends the sessions of that user within `auth.sessionMaxAge`
(default 12 h), because the role is evaluated at every sign-in. **API tokens do not follow the identity provider:** a
token keeps the role of its creator as stored in myriarch (capped at it) until the token expires, and the stored role
changes at the creator's next sign-in or by an administrator. Tokens have a maximum lifetime, `auth.apiTokenMaxTtl`
(default 90 days; a token created without `expiresAt` gets it, a later `expiresAt` is refused, and `0` means unlimited and
logs a warning at start). To offboard a user at once, **disable the user** in myriarch (Users): this deletes the sessions
and revokes the API tokens of that user on every replica.

---

## 4. Provider hints: what myriarchd checks

`myriarchd` recognizes common providers by their issuer URL and then checks the issuer format and warns about frequent
claim problems. The checks run in `serve` and in `myriarchd config validate`; issuer problems are errors under
`auth.oidc.issuerUrl`, claim problems are warnings. The first matching row applies. All other issuers (authentik,
Zitadel, anything else) are accepted as they are. The table lives in `internal/configload/providers.go`.

| Provider | Recognized by | Error when | Warning when |
|---|---|---|---|
| Microsoft Entra ID | host `login.microsoftonline.com`, `login.microsoftonline.us` or `login.partner.microsoftonline.cn` | The path is not `/<tenant GUID>/v2.0` (for example `common`, `organizations`, a domain name or a trailing slash) | `groupsClaim` is not `roles`: Entra omits the groups claim for users in more than 200 groups (group overage) |
| Keycloak | path starts with `/realms/` or `/auth/realms/` | The whole path is not `/realms/<realm>` (or `/auth/realms/<realm>`): trailing slash or endpoint path | - |
| Okta | host ends with `.okta.com`, `.oktapreview.com` or `.okta-emea.com` | The path is neither empty (org authorization server) nor `/oauth2/<id>` with a single segment other than `v1` | - |
| Dex | path is `/dex` or ends with `/dex` | - | `roleMapping` maps groups, but `scopes` lack `groups`: Dex sends the groups claim only for that scope |
| Google | host `accounts.google.com` | The path is not empty | `roleMapping` maps groups: Google ID tokens carry no groups claim; map users with `roleMapping.users` (`email:<address>`) |

Messages (the part after the key path):

- Keycloak: `Keycloak issuers have the form https://<host>/realms/<realm> (no trailing slash, no endpoint path); got "<path>"`
- Okta: `Okta issuers are https://<org>.okta.com (org authorization server) or https://<org>.okta.com/oauth2/<authorization-server-id>, without endpoint path or trailing slash; got "<path>"`
- Google: `the Google issuer is https://accounts.google.com (no path, no trailing slash); got "<path>"`

Further checks that apply to every provider: `issuerUrl` is an HTTPS URL, `clientId` is set, exactly one client
authentication method is configured, `server.publicUrl` is set, `scopes` contains `openid`, `nameClaim` and `emailClaim`
are not empty, every `roleMapping.users` entry is `sub:<subject>` or `email:<address>` (`email:` only with `emailClaim: email`), and a role mapping exists unless
`defaultRole` is set. Validate a configuration without starting the server:

```bash
myriarchd config validate --config myriarchd.yaml --skip-files
```

---

## 5. Provider recipes

Every recipe follows the same steps: register a confidential client with the redirect URI
`https://myriarch.example.com/api/v1/auth/callback`, store the client secret in a file (for example
`/etc/myriarchd/secrets/oidc-client-secret`), make the groups or roles appear in the **ID token**, and map them in
`auth.oidc.roleMapping`. Menu names change between provider versions; the concepts stay the same. To see what a
provider puts into the ID token, decode a test ID token (it is a JWT; the payload is Base64URL-encoded JSON).

### 5.1 Keycloak

- **Issuer:** `https://keycloak.example.com/realms/ops` (older distributions: `https://keycloak.example.com/auth/realms/ops`).
  No trailing slash. The host name must be the one Keycloak uses for its own URLs (its hostname setting), otherwise the
  discovery document names another issuer.
- **Client:** in the realm create a client with protocol OpenID Connect, "Client authentication" on (confidential) and
  only the "Standard flow" enabled. Valid redirect URI: `https://myriarch.example.com/api/v1/auth/callback`. Copy the
  secret from the "Credentials" tab.
- **Groups:** under "Client scopes", create a scope `groups` (or use the dedicated scope of the client) and add a mapper
  of type "Group Membership": token claim name `groups`, "Full group path" off, "Add to ID token" on. Add the scope to
  the client as "Default".
- **Realm roles instead of groups:** in the client scope `roles` edit the mapper "realm roles" and turn "Add to ID
  token" on, or add a mapper of type "User Realm Role" with the claim name `roles`. Then set `groupsClaim` to
  `realm_access.roles` (dotted path) or `roles`.
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: Keycloak
      issuerUrl: https://keycloak.example.com/realms/ops
      clientId: myriarch
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      scopes: [openid, profile, email]
      groupsClaim: groups
      roleMapping:
        admin: [myriarch-admins]
        operator: [myriarch-operators]
        viewer: [myriarch-viewers]
  ```

- **Typical errors:** `oidc: issuer did not match` (a trailing slash, `http` instead of `https`, or the internal host
  name); `Invalid parameter: redirect_uri` on the Keycloak page (redirect URI not registered exactly); `access_denied`
  after a successful Keycloak login (the groups are not in the ID token, or "Full group path" is on so the values are
  `/myriarch-admins`).

### 5.2 Dex

- **Issuer:** exactly the `issuer` of the Dex configuration, for example `https://dex.example.test/dex`. Dex compares
  it character by character; a trailing slash only works if Dex is configured with one.
- **Client:** a static client in the Dex configuration:

  ```yaml
  staticClients:
    - id: myriarch
      name: Myriarch
      secret: <client secret>           # placeholder: never commit a real value
      redirectURIs:
        - https://myriarch.example.test/api/v1/auth/callback
  ```

- **Groups:** Dex sends the groups of a connector only for the scope `groups`. Add it to `auth.oidc.scopes`;
  `myriarchd` warns when `roleMapping` maps groups and the scope is missing. Whether groups exist depends on the
  connector (LDAP, GitHub, upstream OIDC); Dex's static passwords have none.
- **Static users:** map them with the users mapping by e-mail. Dex marks the address of its static passwords as
  verified; if your Dex version does not, use `sub:` entries (the subject Dex issues for the static user).
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: Dex
      issuerUrl: https://dex.example.test/dex
      clientId: myriarch
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      scopes: [openid, profile, email, groups]
      roleMapping:
        admin: [myriarch-admins]           # groups from a connector
        users:
          admin: ["email:admin@example.com"]   # static user
  ```

- **Quickstart:** `deploy/helm/myriarch/examples/quickstart-dex.yaml` (chart values) and
  `deploy/helm/myriarch/examples/dex/dex.yaml` (values for the upstream Dex chart) wire a demo Dex and Myriarch
  together. The Dex values contain placeholders for the bcrypt hash and the client secret. The issuer of the quickstart
  names the port of the Dex Service (`https://dex.example.test:5554/dex`) in both files; the browser reaches Dex through
  `/etc/hosts` and a port-forward to 5554.
- **Typical errors:** `issuer did not match` (the configured and the served issuer differ); `access_denied` for a static
  user (no `users` entry for the e-mail address, or `email_verified` is not true); Dex returns `Invalid client` (wrong
  client ID or secret); the redirect URI of the static client differs from the one derived from `server.publicUrl`.

### 5.3 Microsoft Entra ID

- **Issuer:** `https://login.microsoftonline.com/<tenant GUID>/v2.0`. Use the directory (tenant) ID as GUID, not
  `common`, `organizations` or a domain name, and no trailing slash. `myriarchd` rejects other forms at start.
- **App registration:** register an application (web platform), redirect URI
  `https://myriarch.example.com/api/v1/auth/callback`. Authenticate with a client secret or, alternatively, with a
  certificate: upload the public certificate under "Certificates & secrets" and configure `clientCertificateFile` and
  `clientKeyFile` instead of the secret.
- **App roles:** define app roles in the registration with the values `myriarch.admin`, `myriarch.operator` and
  `myriarch.viewer` (allowed member types: users/groups). In the enterprise application set "Assignment required" to
  yes and assign users or groups to the roles.
- **Why roles and not groups:** Entra omits the groups claim for users in more than 200 groups (group overage).
  App roles are not affected, so use `groupsClaim: roles`. `myriarchd` warns when an Entra issuer is combined with another
  claim.
- **No `email_verified`:** Entra ID tokens carry no `email_verified` claim, so `email:` entries never match. Use group
  or role mappings, or `sub:` entries (the subject is specific to the app registration).
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: Microsoft Entra ID
      issuerUrl: https://login.microsoftonline.com/00000000-0000-0000-0000-000000000000/v2.0   # your tenant GUID
      clientId: 11111111-1111-1111-1111-111111111111                                           # application (client) ID
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      groupsClaim: roles
      roleMapping:
        admin: [myriarch.admin]
        operator: [myriarch.operator]
        viewer: [myriarch.viewer]
  ```

- **Typical errors:** `AADSTS50011` (redirect URI not registered); `AADSTS7000215` (invalid client secret: use the secret
  *value*, not the secret ID); `AADSTS50105` (assignment required and the user has no role); `access_denied` after a
  successful login (the role value in `roleMapping` differs from the app role value, or the claim is not `roles`); start
  error `Entra ID needs the tenant ID (GUID)` (issuer form).

### 5.4 Okta

- **Issuer:** either the org authorization server `https://example.okta.com` or a custom authorization server
  `https://example.okta.com/oauth2/default` (the segment is the authorization server ID). No trailing slash, and no
  `/oauth2/v1/...` endpoint path.
- **Application:** create an OIDC web application with the authorization code grant, sign-in redirect URI
  `https://myriarch.example.com/api/v1/auth/callback`, and assign people or groups to it ("Assignments"). Copy client ID
  and secret.
- **Groups claim:** with the org authorization server, edit the application, "Sign On" tab, "OpenID Connect ID Token":
  groups claim type "Filter", claim name `groups`, a filter such as "Starts with" `myriarch-`. With a custom
  authorization server add a claim under "Security", "API", "Authorization Servers", "Claims": name `groups`, include in
  "ID Token" (always), value type "Groups", with a filter. If the claim depends on the `groups` scope, add `groups` to
  `auth.oidc.scopes`.
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: Okta
      issuerUrl: https://example.okta.com/oauth2/default
      clientId: <client id>
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      scopes: [openid, profile, email, groups]
      groupsClaim: groups
      roleMapping:
        admin: [myriarch-admins]
        viewer: [myriarch-viewers]
  ```

- **Typical errors:** the start error `Okta issuers are ...` (endpoint path or trailing slash in the issuer);
  `The 'redirect_uri' parameter must be a Login redirect URI in the client app settings`; `access_denied` after login
  (the groups claim is missing in the ID token: filter does not match, claim not included in the ID token, or scope
  missing); `User is not assigned to the client application`.

### 5.5 authentik

- **Issuer:** `https://authentik.example.com/application/o/myriarch/` - **with** the trailing slash and with the slug of
  the application. authentik serves the issuer with the slash, and the exact comparison requires it in `issuerUrl`.
  Take the value from the provider's "OpenID Configuration Issuer" field.
- **Provider and application:** create an OAuth2/OpenID provider (client type "Confidential", strict redirect URI
  `https://myriarch.example.com/api/v1/auth/callback`), select a **signing key** (otherwise ID tokens are signed with the
  client secret using HS256, which `myriarchd` does not accept), and create an application with the slug `myriarch` that
  uses it. Restrict access to the application with policy bindings (group or user).
- **Groups:** the default scope mapping "profile" adds the claim `groups` with the names of the user's groups. The default
  `scopes` (`openid profile email`) are enough.
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: authentik
      issuerUrl: https://authentik.example.com/application/o/myriarch/
      clientId: <client id>
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      groupsClaim: groups
      roleMapping:
        admin: [myriarch-admins]
        operator: [myriarch-operators]
        viewer: [myriarch-viewers]
  ```

- **Typical errors:** `oidc: issuer did not match` (the trailing slash is missing); `id token signed with unsupported
  algorithm HS256` (no signing key on the provider); `Redirect URI Error` on the authentik page; `access_denied` after
  login (the user's groups are not in `roleMapping`, or the `profile` scope mapping was removed).

### 5.6 Zitadel

- **Issuer:** the instance URL without path, for example `https://zitadel.example.com` or
  `https://example.zitadel.cloud`. No trailing slash.
- **Application:** in a project create a web application with the authorization code flow (PKCE) and the
  authentication method "Basic" (client secret). Redirect URI `https://myriarch.example.com/api/v1/auth/callback`.
  Copy client ID and secret.
- **Roles:** in the project define roles with the keys `myriarch-admin`, `myriarch-operator` and `myriarch-viewer` and
  grant them to users. Zitadel reports them in the claim `urn:zitadel:iam:org:project:roles` as an **object**
  (`{"myriarch-admin": {"<org id>": "<org domain>"}}`); `myriarchd` uses the keys of the object. The claim only appears
  with these settings:
  - project setting "Assert Roles on Authentication" on, and
  - application token settings "User roles inside ID Token" on (and "User Info inside ID Token" on, to get name and
    e-mail in the ID token).
  If the roles of a different project are needed, request the scope
  `urn:zitadel:iam:org:project:id:<project id>:aud` or `urn:zitadel:iam:org:projects:roles` in `scopes`.
  If users should only sign in when they hold a role, enable the project's check for roles on authentication.
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: Zitadel
      issuerUrl: https://zitadel.example.com
      clientId: <client id>
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      groupsClaim: urn:zitadel:iam:org:project:roles
      roleMapping:
        admin: [myriarch-admin]
        operator: [myriarch-operator]
        viewer: [myriarch-viewer]
  ```

- **Typical errors:** `access_denied` after login although the user has a role (the roles are not asserted in the ID
  token: see the settings above); `issuer did not match` (a path or trailing slash in `issuerUrl`, or a custom domain
  that differs from the configured instance domain); `Errors.App.NotFound` (wrong client ID).

### 5.7 Google

- **Issuer:** `https://accounts.google.com` (no path, no trailing slash).
- **Client:** in the Google Cloud console create an OAuth client of type "Web application" with the authorized redirect
  URI `https://myriarch.example.com/api/v1/auth/callback` (HTTPS required). On the OAuth consent screen choose the user
  type "Internal" to limit sign-in to your Google Workspace organization.
- **No groups claim:** Google ID tokens contain no groups. Map people one by one with the users mapping. Google sets
  `email_verified`, so `email:` entries work. `myriarchd` warns when `roleMapping` maps groups.
- **Never set `defaultRole`:** with an external consent screen any Google account can sign in, and a default role would
  hand it to all of them. Even with an internal consent screen prefer explicit entries.
- **Configuration:**

  ```yaml
  auth:
    oidc:
      enabled: true
      displayName: Google
      issuerUrl: https://accounts.google.com
      clientId: 123456789-example.apps.googleusercontent.com
      clientSecretFile: /etc/myriarchd/secrets/oidc-client-secret
      roleMapping:
        users:
          admin: ["email:alice@example.com"]
          viewer: ["email:bob@example.com"]
  ```

- **Typical errors:** `Error 400: redirect_uri_mismatch` (the redirect URI is not registered exactly);
  `access_denied` for a known person (the address differs in the users mapping; comparison ignores case but not
  aliases) or `email_verified` is false; `Error 403: org_internal` (the person is not part of the Workspace
  organization); the start error `the Google issuer is https://accounts.google.com` (path or slash in `issuerUrl`).

---

## 6. Declarative enrollment tokens

Agents (supervisors and updaters) enroll with tokens that start with `myre_`. Besides tokens created in the UI, the
server configuration can declare tokens. This lets an installation (Helm, GitOps) provision agent credentials without a
manual step. Only the SHA-256 hash and a 12-character display prefix are stored in the database; the plaintext token
exists only in the mounted file and in memory during start.

### 6.1 Configuration

```yaml
opamp:
  enrollmentTokens:
    - name: sites-2027                               # DNS label: lower case letters, digits, "-"
      tokenFile: /etc/myriarchd/enrollment/sites-2027   # absolute path
      labels: { fleet: sites }                       # copied to agents that register with this token
      expiresAt: "2027-12-31T23:59:59Z"              # RFC 3339; empty: no expiry
```

| Key | Rule |
|---|---|
| `name` | 1 to 63 characters, lower case letters, digits and `-`, starting and ending with a letter or digit; unique in the list |
| `tokenFile` | Required, absolute path |
| `labels` | At most 20; keys 1 to 63 characters without whitespace or `=`; values at most 253 characters (the same rule as for tokens created through the API) |
| `expiresAt` | RFC 3339 timestamp such as `2027-01-01T00:00:00Z` |

The token is the file content (trimmed) and must match `myre_` followed by 32 to 120 letters, digits, `_` or `-`.
Generate one:

```bash
printf 'myre_%s' "$(openssl rand -hex 24)" > sites-2027-token
chmod 0600 sites-2027-token
```

The well-known development token and duplicate values across entries are rejected. `opamp.enrollmentTokens` is a
list and cannot be set through environment variables; `${VAR}` expansion works inside it.

### 6.2 Behavior

- **Start-up.** All files are read and checked first; any problem aborts the start before a database change
  (`opamp.enrollmentTokens[i].tokenFile: <reason>`). `myriarchd config validate` (without `--skip-files`) performs the
  same checks.
- **Create.** A token whose hash is unknown is created with the creator `config` (visible in the UI with a
  "Configuration" badge and in `GET /api/v1/enrollment-tokens` as `createdBy: config`).
- **Update.** If the token exists, is not revoked and was created by the configuration, a changed name, labels or expiry
  is updated in place.
- **Idempotent.** A second start with the same configuration changes nothing and writes no audit entry. Changes are
  audited as `enrollment_token.create` and `enrollment_token.update` (actor system, `source: "config"`).
- **Never un-revoked.** A revoked token stays revoked; `myriarchd` logs a warning (`declarative enrollment token is
  revoked; rotate its value to issue a new one`). A token with the same value that was created in the UI is left
  unchanged and logged as well.
- **No automatic revocation.** A token that is removed from the configuration keeps working. At every start the server
  logs `declarative enrollment token is no longer configured but still valid; revoke it in the UI or with DELETE
  /api/v1/enrollment-tokens/{id}` for each such token.
- **Expiry.** An `expiresAt` in the past is reported as a warning at start (`opamp.enrollmentTokens[<i>] (<name>)
  expired at <time>: agents cannot enroll with it`). Expiry is enforced at the OpAMP handshake like for any token.

### 6.3 Rotation

Rotate without a gap by running two entries side by side:

1. Add a second entry (`sites-2027-b`) with a new token file and roll out the server configuration.
2. Switch the agents to the new token (their `MYRIARCH_ENROLLMENT_TOKEN` or token file).
3. Remove the old entry from the configuration, then revoke the old token in the UI (Settings, Tokens) or with
   `DELETE /api/v1/enrollment-tokens/{id}`. Agents that stay connected keep their connection; reconnects with the
   revoked token fail.

### 6.4 Helm chart

```yaml
enrollmentTokens:
  - name: sites-2027
    existingSecret: ""     # empty: the chart generates the token
    key: token             # key in existingSecret
    labels: { fleet: sites }
    expiresAt: ""
```

For each entry the chart mounts the token at `/etc/myriarchd/enrollment/<name>` through one projected volume and renders
the matching `opamp.enrollmentTokens` entry. With `existingSecret` empty the chart creates the Secret
`<fullname>-enrollment-<name>` with the key `token` (`myre_` plus 40 random characters), kept across upgrades. Agents in
the same cluster mount the same Secret. Retrieve the value for agents outside the cluster with
`kubectl -n <namespace> get secret <fullname>-enrollment-<name> -o jsonpath='{.data.token}' | base64 -d`. As with the
bootstrap Secret, use `existingSecret` for GitOps tools, because `helm template` cannot see existing Secrets.

---

## 7. Troubleshooting

| Symptom | Cause and remedy |
|---|---|
| `myriarchd` does not start: `no login path is configured for serve mode` | Neither `auth.oidc.enabled` nor `auth.bootstrapAdmin.enabled` is set. Enable one; the bootstrap admin needs `passwordHashFile` or `tokenFile`. |
| `auth.bootstrapAdmin: needs passwordHashFile or tokenFile when enabled` | The account is enabled without a credential file. |
| `file <file> does not contain a bcrypt hash ($2a$, $2b$ or $2y$) with cost 10 or higher` | The hash file holds a plain password, a hash with a lower cost, or extra text. Create it with `myriarchd auth hash-password`. |
| `file <file> must contain a token of 32 to 512 characters without whitespace` | The token is too short or contains a line break inside. Use `tr -d '\n'` when generating it. |
| Login page shows no bootstrap section | The account is inactive: OIDC is enabled without `keepWithOidc`, or `enabled` is false. Check `GET /api/v1/auth/config` (`bootstrapPasswordEnabled`, `bootstrapTokenEnabled`) and the start log. |
| `invalid bootstrap credentials` | Wrong user name, password or token; a used token; the replica reads another file. The audit entry `auth.bootstrap.login` carries the reason (`bad_credentials`, `token_used`). |
| `429` on bootstrap sign-in | Rate limit or account backoff (section 2.7). Wait for `Retry-After`, or restart the pod: the counter is in memory and a restart clears it. Behind an ingress set `server.trustedProxies`, otherwise all clients share one limit. Restrict network access to `/api/v1/auth/bootstrap-login`, otherwise anyone who reaches it can keep the account in a delay. |
| `503` on bootstrap sign-in | The credential file is unreadable on this replica (mount, permissions). The log names the path. |
| Bootstrap token worked once and is now refused | By design: tokens are single use. Write a new value into the file (or delete the Secret and `helm upgrade`). |
| OIDC: `oidc: issuer did not match` | `issuerUrl` differs from the issuer of the discovery document: trailing slash, scheme, host name or path. Fetch `<issuerUrl>/.well-known/openid-configuration` and copy the `issuer` value. |
| OIDC: `x509: certificate signed by unknown authority` | Set `outbound.caFile` to a PEM file with the CA of the IdP. |
| OIDC: `not allowed by the outbound policy` | The IdP address is in a private range. Add it to `outbound.allowedCidrs`. |
| OIDC: timeout during discovery in Kubernetes | The `NetworkPolicy` blocks the port (`networkPolicy.egress.extraPorts`) or the proxy is not set (`HTTPS_PROXY`, `NO_PROXY`). |
| OIDC sign-in ends with `access_denied` | No role matched (audit reason `no_role`): the groups claim is missing or has another name, the values differ from `roleMapping`, `email_verified` is not true for an `email:` entry, or the user is disabled. Compare the claim names of the ID token with `groupsClaim`, `nameClaim` and `emailClaim`. |
| The role of a user does not change | Role changes apply at the next sign-in. A role set manually in the UI overrides the mapping until an administrator changes it. |
| `auth.oidc.roleMapping: is empty and no defaultRole is set: nobody could log in` | The mapping is empty and no `defaultRole` is set. Add groups or `users` entries. |
| `auth.oidc.roleMapping.users.admin[0]: must be "sub:<subject>" or "email:<address>"` | A users entry lacks the prefix. |
| Warning `auth.oidc.defaultRole is ...` | Every user of the application gets this role. Remove it unless the provider restricts access. |
| Declarative token: `file <file> must contain an enrollment token` | The file does not hold `myre_` plus 32 to 120 characters. Generate it as in section 6.1. |
| Declarative token: `declarative enrollment token is revoked` | Revoked tokens are never re-activated. Change the content of the token file to issue a new token. |
