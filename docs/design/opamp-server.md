---
title: "Server behaviour"
weight: 20
description: "Normative behaviour of myriarchd, grouped by component: startup, OpAMP endpoint, persistence, rendering, rollouts and executors."
---

Normative decisions for myriarchd, grouped by component: persistence, event bus and leader lock, authentication, the OpAMP
endpoint, the HTTP and live interfaces, the GitOps reader, blueprint rendering, assignment, the rollout planner and
engine, executors, the artifact cache, the release tracker and the notifier. The last section covers how collectors are
installed and configured on hosts.

The rules about choosing a version from the UI are in [version-selection.md](version-selection.md). The contract with
the host updater is in [updater-protocol.md](updater-protocol.md). Facts about third-party software are collected in
[upstream-notes.md](upstream-notes.md).

Each section describes what the code guarantees today. Numbers given as defaults are the shipped defaults and can be
changed in the configuration unless stated otherwise.

## Configuration and startup

- Configuration is a YAML file with strict decoding (unknown keys are errors), starting from built-in defaults or the
  development defaults. Durations are written in Go syntax (`30s`).
- `${VAR}` and `${VAR:-default}` are expanded in all string values. Environment variables of the form
  `MYRIARCH_<PATH>` override single keys. Keys ending in `File` (`dsnFile`, `clientSecretFile`, `tokenFile`, ...) read a
  secret from a file. An empty secret file is an error.
- Validation collects all problems into one error. In production it refuses, among others: a plain-HTTP public URL,
  a plain listener without `server.tls.terminatedUpstream`, an enabled dev login, `cookieSecure: false`, disabling the
  enrollment token requirement without client certificates, WebSocket compression without an explicit acknowledgement,
  and plain `http://` download base URLs without an acknowledgement.
- `myriarchd config validate` runs the same checks, optionally also reading the referenced files, so a configuration can
  be verified in CI before it is deployed.
- Modes: `myriarchd serve` (production), `myriarchd dev` (embedded database, development login and token), `myriarchd migrate`
  (the database settings only, see [Start, migrations and schema compatibility](#start-migrations-and-schema-compatibility)) and
  `myriarchd healthcheck` (a client of a running server, see [Health check](#health-check)).
- `myriarchd dev --install-bundle-dir DIR` serves the installer bundles from a directory instead of those of a release
  ([Installer bundles](#installer-bundles)). The flag exists in development mode only: `serve` refuses it with a message that
  says why, and the start refuses a directory outside development mode before it touches the database.

## Start, migrations and schema compatibility

Three numbers describe the fit between a database and a binary:

- The **schema version** of a database is its highest applied migration (0 for an empty database).
- The **schema level** of a binary is the highest migration it embeds.
- The **compatibility level** of a database is the lowest schema level that a binary must have to run on it. It is the column
  `min_binary_schema` of the table `schema_compat`, which holds exactly one row (migration 00006), and it never exceeds the schema
  version. A database that is older than migration 00006 has no compatibility level (the tools print `none`). A schema from
  migration 00006 on without the table or without its row is damaged: without the level nobody can say whether a binary fits,
  so the start and `myriarchd migrate` fail with an error that asks for a restore from a backup.

**Start.** `myriarchd serve` and `myriarchd dev` connect to the database, read the schema version and the compatibility level
and decide before any other component is created:

| State of the database | `database.migrateOnStart: true` (default) | `database.migrateOnStart: false` |
|---|---|---|
| Compatibility level above the schema level of the binary (incompatible) | Refuse the start, change nothing | Refuse the start, change nothing |
| Schema version below the schema level of the binary | Apply the pending migrations under the advisory lock, read the state again, then go on (refuse if the schema is incompatible by now) | Refuse the start at once, change nothing |
| Schema version equal to the schema level | Start | Start |
| Schema version above the schema level of the binary, compatibility level not above it | Log a warning and start | Log a warning and start |

A refusal ends the process with exit status 1 and one of these messages (`<...>` stands for a number):

- Incompatible: `the database schema is incompatible with this myriarchd: the database is at schema version <V> and needs a myriarchd
  with schema level <C> or higher (compatibility level <C>), this myriarchd has schema level <S>; start a newer myriarchd release or
  restore a backup of the database from before the upgrade`.
- Older, with `database.migrateOnStart` off: `the database schema is at version <V> but this myriarchd needs version <S> and
  database.migrateOnStart is false: run "myriarchd migrate" first, or set database.migrateOnStart to true`.

The warning for a newer compatible schema has the level WARN and the text `the database schema is newer than this myriarchd but
compatible with it (after a rollback, or while a rolling update is under way): run the release that matches the schema as soon as
you can`, with the attributes `schemaVersion`, `compatibilityLevel` and `schemaLevel`. After the schema step the log line `database
schema ready` names the same three numbers.

The check is made at start. A replica that runs is not stopped when another replica raises the compatibility level. `/readyz`
checks the schema version only until that check first succeeds, which the start already guarantees; after that it reads neither
the schema version nor the compatibility level again. It therefore notices neither a later restore of an older backup nor a raised
compatibility level: after a restore, restart every replica.

**`myriarchd migrate`** loads the database settings and nothing else: the section `database` of the file (an unknown key inside it
is an error, every other section is ignored), `${VAR}` references in it, the variables `MYRIARCH_DATABASE_*` and `database.dsnFile`.
It validates only these settings, so it runs where the public URL, the login path and the secrets of other features do not exist,
for example in a job before the server is updated. It runs the schema step of the start with `database.migrateOnStart` set: it
applies the pending migrations, refuses an incompatible schema and warns about a newer compatible one, and a second run changes
nothing. Results go to stdout, log lines and errors to stderr.

- Without `--check` it prints `schema version before: <V>`, `schema version after: <V>` and `compatibility level: <C>` (`none`
  when there is none).
- With `--check` it changes nothing and prints `schema version`, `compatibility level`, `schema level of this myriarchd` and
  `status` (`current`, `pending`, `newer` or `incompatible`).
- Exit codes: 0 done (with `--check`: nothing is pending and this binary can run on the schema), 1 any error (settings,
  connection, a failed migration), 3 `--check` found pending migrations, 4 the schema is incompatible with this binary (with and
  without `--check`).

**Writing migrations.** A migration is never edited after it may have been applied anywhere. From 00006 on, its header (the comment
before `-- +goose Up`) has two lines that agree with each other. `-- Rolling update:` says what a replica of the previous version
does on the changed schema. `-- Compatibility level:` is `unchanged`, or `raised to 00NNN` when binaries below the level NNN
cannot work with the schema. There are two valid combinations:

- `-- Rolling update: expand only.` (an explanation may follow) with `-- Compatibility level: unchanged.` *Expand only* is a
  promise that the previous version keeps working: new tables, nullable columns and columns with a default, indexes that are not
  unique, a constraint that is relaxed. The level stays. A new unique index or a new `NOT NULL` or `CHECK` constraint tightens what
  the previous version may write, so it is not expand only.
- `-- Rolling update: not compatible, <what stops working>` with `-- Compatibility level: raised to 00NNN.` The migration is one
  that binaries below some level cannot work with (it drops, renames or tightens something that they use, or changes what stored
  data means). Up sets `min_binary_schema` to NNN, which is greater than the previous value and at most the number of the
  migration, and Down sets the previous value again. The level NNN is the number of the oldest migration whose binaries still
  work on the new schema.
- A text that starts with neither form, and any other combination ("expand only" with a raised level, "not compatible" with an
  unchanged level), fails `TestMigrationHeaders`. `TestCompatibilityLevelFollowsTheHeaders` and `TestLevelCheckRejectsViolations`
  in `internal/store/migrations` apply every migration up and down and compare the level in the database with the header.
- Every consequence that a user must know before an upgrade goes into the section "Upgrade notes" of the changelog; the
  [development guide](../development.md) describes the steps.

**Rollback.** A binary that is rolled back to an older release which has this mechanism starts on the newer schema when the
compatibility level allows it (with the warning), and is refused otherwise. v0.1.0 does not read the compatibility level, and its
migration library accepts a database with higher versions, so it starts on any later schema without a check. A rollback to
v0.1.0 is not supported ([ADR-0036](../adr/0036-database-schema-compatibility-and-rollback.md)): back up the database before an
upgrade, and go back by restoring the backup.

## Health check

**Available from v0.2.0.** `myriarchd healthcheck` asks a running server whether it is ready and whether its OpAMP endpoint is in
place, and tells the result by its exit code. The release image has neither a shell nor curl, so this command is the health check of a
container (`["CMD", "/usr/local/bin/myriarchd", "healthcheck"]`) and the program of the Pod that `helm test` runs. It is a client of
the server: it reads no configuration, opens no database connection and needs no loopback address.

| Flag | Default | Meaning |
|---|---|---|
| `--url` | `http://127.0.0.1:8080` | Base URL of the server: `http` or `https`, a host and an optional port. A path, a query, a fragment and credentials are refused. |
| `--opamp-path` | `/v1/opamp` | Path of the OpAMP endpoint (`opamp.path` of the server). |
| `--token-file` | none | File with an enrollment token. |
| `--ca-file` | none | PEM file with the CAs that may have signed the certificate of an `https` server, and no others. Without it the system roots apply. |
| `--timeout` | `10s` | Time allowed for both requests together. |

**Requests.** Two requests, in this order; the second is made only when the first succeeded.

1. `GET /readyz` must be answered with 200. The server answers 503 while its database does not answer, the schema does not fit or
   its event bus is not connected.
2. A WebSocket upgrade at `--opamp-path`: the request of an OpAMP agent, without its first message. With `--token-file` it carries
   `Authorization: Bearer <token>` and must be answered with 101. Without it the server must answer 401, which shows that the route and
   the authentication are in place. A server that takes connections without a token (the development mode,
   `opamp.requireEnrollmentToken: false`) answers 101, which is accepted as well. After 101 the command sends the normal WebSocket
   close message (code 1000), waits at most two seconds for the answer and ends the connection.

**What the command does not do.**

- It sends no `AgentToServer` message. No session is created, no agent is registered and nothing is written to the database. What
  the server records belongs to the connection: the gauge and counters of the OpAMP connections, the use of the token (its usage
  statistics) and the access log at debug level. Because the command ends the connection with the normal close message, the server
  logs at most a debug line about it, and no warning.
- It sends no token to another host. It follows no redirect (a 3xx answer is a failure) and uses no proxy, whatever the proxy
  variables say, and it checks the certificate of an `https` server (there is no switch that skips the check).
- It does not take the token from an argument, from the environment or from standard input, and it never prints it. The file must be a
  regular file of at most 4 KiB that holds one enrollment token (`myre_`, then 32 to 120 letters, digits, `_` or `-`; white space around
  it is ignored). Anything else, such as the database DSN or the token of the bootstrap admin, is refused with exit code 2 before a
  request is made.

**Metrics.** A check without a token is an unauthorized connect: `myriarch_opamp_connect_rejected_total{reason="unauthorized"}` and
`myriarch_auth_agent_requests_total{result="unauthorized"}` rise by one for each check. The alert `MyriarchUnauthorizedConnects` of the
[example rules](../../deploy/examples/production/monitoring/prometheusrule.yaml), which the Helm chart renders with
`prometheusRule.enabled`, compares the rate of the first over ten minutes with one per second, so a check every ten seconds (0.1 per
second) stays far below it. A check with a token counts as an authenticated connect and as a use of the token.

**Output.** One line per request that succeeded goes to stdout, with `<url>` standing for the address of the server:

```text
readyz ok: GET <url>/readyz answered 200
opamp ok: the upgrade at /v1/opamp was refused with 401 (no token given, as expected)
opamp ok: the upgrade at /v1/opamp was accepted with 101 (the server accepts the token)
opamp ok: the upgrade at /v1/opamp was accepted with 101 (no token given; the server takes connections without one)
```

The first line is followed by one of the other three. The reason for a failure goes to stderr as one line `myriarchd: <request> failed: <reason>`,
for example `myriarchd: readyz failed: GET <url>/readyz answered 503, want 200: the server is not ready, its log names the cause`.

| Exit code | Meaning |
|---|---|
| 0 | Healthy: `/readyz` answered 200 and the OpAMP endpoint answered as expected. |
| 1 | Not healthy: the server answered, but not as expected (`/readyz` is not 200, the upgrade was refused although a token was given, or it was answered with something other than 101 or 401). |
| 2 | Wrong usage: a flag or value is invalid, or the token file or the CA file cannot be used. No request has been made. |
| 3 | No answer: the connection was refused or reset, the name was not resolved, the certificate was not trusted, or `--timeout` ran out. |

## Persistence

The store interfaces define the semantics; the PostgreSQL implementation follows them.

- Errors: "no rows" maps to `ErrNotFound`, a unique violation to `ErrConflict`. JSONB maps are never returned as nil.
  JSON that carries domain values (GitOps objects, policy snapshots, annotations, details) is always read with a decoder
  that keeps integers as integers, otherwise renderings would differ from those of `myrctl`.
- Callers assign UUIDs and creation times. The store assigns serial IDs and the update time.
- Transactions run through an `InTx` helper whose sub-stores use the transaction.
- Agents. The description upsert inserts or updates the descriptive columns, sets `labels` only on insert (from the
  enrollment token), and parses the version into major, minor and patch columns (NULL when unparsable). Reads return
  fully hydrated agents, including the linked updater in both directions and the package status.
  - Listing translates the filter into SQL. A text query matches host name, ID, version and attribute values. Attribute
    filters combine with OR inside one key and AND across keys, and match identifying, non-identifying and `labels.<k>`
    attributes. "Outdated" and "up to date" compare the version triple, and imply the kind `collector`. Sorting always
    ends with the ID so pagination is stable.
  - Facets are disjunctive: the count of one facet ignores the filter on its own dimension. Facets exist for status,
    health, kind, version, fleet (empty means unassigned), OS, site type, country and config sync state, sorted by count.
  - `last_seen` is written in batches. Setting the desired config clears the failed marker when the hash changes. A
    conditional variant sets the desired config only if none is set (bootstrap).
- GitOps state. Applying a snapshot is one transaction: upsert every object as JSON (including its source), delete the
  ones that disappeared, detect changes by content hash, record the sync, and return the changed object keys
  (`<Kind>/<name>`, sorted). The active snapshot is the one of the last sync with result `applied`; before any sync the
  status is `never`.
- Rendered configurations are content addressed by the SHA-256 of the canonical YAML. Writing an existing hash only
  touches it. Unreferenced configurations are removed after the retention period (7 days by default).
- Rollouts follow a strict concurrency model: status changes only by compare-and-set, wave approvals only through their
  own operation, progress through its own update, and targets by an optimistic update guarded by their update time.
  Conflicts are re-evaluated on the next tick. Get and list return waves and counts (`in_progress` counts `verifying`
  as well).
- Releases are ordered by semantic version, descending; prereleases are flagged.
- Audit filters accept an action prefix with a trailing `*`.
- Migrations: the first migration is a contract and never edited. Further changes are new numbered up/down files with
  an advisory lock so that concurrent replicas are safe. The header of every migration from 00006 on states whether
  the previous version keeps working, and the database records the result as its compatibility level ([Start, migrations and
  schema compatibility](#start-migrations-and-schema-compatibility)).

## Event bus

- Events are JSON envelopes with ID, time and origin replica. The size limit is 7,500 bytes (PostgreSQL allows 8,000 for
  a notification payload); ID lists are chunked by 100.
- The PostgreSQL bus holds one dedicated connection running `LISTEN` on the channel `myriarch_events` and publishes
  with `pg_notify`. Delivery is best effort: consumers re-read the authoritative state from the database.
- Each local subscriber has a buffer of 256 events. On overflow the event is dropped and counted.
- After a lost connection the bus reconnects with backoff from 1 to 30 seconds, listens again and delivers a local
  `BusReconnected` event so that caches and the live hub resynchronize.
- A pooler in transaction mode breaks `LISTEN` and session advisory locks, so myriarchd needs a direct or session pooled
  connection.

## Leader election

A session advisory lock on a dedicated connection decides the leader. A candidate tries the lock every five seconds
(with 20 percent jitter).

The failover after a hard failure of the leader's node (no FIN or RST) is bounded by `ha.leaderSessionTimeout` T (default
30 seconds, 10 seconds to 10 minutes) as one end-to-end budget: the server must drop the silent session within
D = T − 1.2 × min(5 s, T/6) − T/20 (whole seconds), the time the follower needs for its retry (every min(5 s, T/6),
20 percent jitter) and the acquisition fill the rest. The lock session and the listener session of the event bus get their
own pool (`internal/pgsession`) and send `tcp_keepalives_idle` = `tcp_keepalives_interval` = I = D/6 (at least 1 second),
`tcp_keepalives_count` = D/I − 1 and `tcp_user_timeout` = D − I in the startup packet. On Linux the user timeout replaces the count
as the drop criterion, so PostgreSQL drops a silent client at the first probe time (multiples of I) that is at least D − I
after the last packet, i.e. after D − I and before D; a server without `tcp_user_timeout` drops after ⌊D/I⌋ × I, which is above D − I and at most D. A server that
refuses `tcp_user_timeout` (SQLSTATE 22023) is used without it and a warning is logged. The pool of ordinary connections is
not changed. A unit test checks the budget with models of both drop rules for all timeouts.

The leader pings its lock session every T/6 with a timeout of T/3 and gives up leadership at the latest T/2 after the last
answer: it first clears the leader flag (`IsLeader` and the metric `myriarch_leader`), then cancels the context of the leader
jobs. That is before the server can release the lock, so there is no second leader. After giving up, the replica asks the
server over a regular connection to end the lock session (`pg_terminate_backend`, only a backend that holds the lock), and
then competes for the lock like a follower. A planned stop unlocks explicitly and closes the session, so a follower takes
over within one retry interval. Operations: [High availability](../operations/high-availability.md).

Leader-only jobs: GitOps polling, the rollout planner and engine, the release tracker, artifact prefetch, the offline
sweeper, housekeeping and the periodic gauges. Everything else runs on every replica.

## Authentication and authorization

- Tokens. API tokens start with `myra_`, enrollment tokens with `myre_` and download tokens with `myrd_`. A token is 32
  random base62 characters; only the SHA-256 is stored, together with a display prefix. Lookups are by hash. Tokens can
  be revoked or expire, and a token created by another token is capped by the creator's role and expiry. A download token
  is signed, not stored, and comes in two kinds: the token of a package offer (bound to one updater and one artifact) and,
  from v0.2.0, the install grant of a host that is not enrolled yet ([Artifact cache](#artifact-cache)).
- Browser sessions are server side. The cookie holds 32 random bytes and the database key is their SHA-256. The cookie is
  `HttpOnly`, `SameSite=Lax` and `Secure` by configuration. A session has a sliding idle timeout (default 2 hours) and
  an absolute lifetime (default 12 hours), after which identity provider changes take effect.
- CSRF. A cookie-authenticated request with a mutating method must carry the session's token in a header (double
  submit); otherwise the answer is 403 `csrf_failed`. Bearer-token requests are exempt.
- OIDC login uses the authorization code flow with PKCE, state and nonce, held in a short-lived HMAC-signed cookie whose
  key is shared between replicas. The ID token is verified (audience, nonce). The role is the highest one matched by
  the group mapping, else the default role; an empty default role denies login (403). Manually assigned roles survive.
  The post-login redirect must be a relative path. Failures redirect to the login page with `access_denied` or
  `oidc_error` and are audited. Identity providers that omit the groups claim for users with many groups need app
  roles as the claim source.
- Roles are viewer, operator and admin. Permissions are checked per route from the OpenAPI `x-permission` value.
- Development login exists only when explicitly enabled outside production and compares in constant time.
- Caches: sessions and API tokens for 5 seconds, enrollment tokens for 60 seconds (unknown tokens 10 seconds, kept apart from the valid ones).
  Logout, revocation, disabling and role changes publish invalidation events, so every replica drops its cache at once.
- Failed logins and invalid bearer tokens are rate limited per client IP; IPv6 clients are counted per /64 prefix, and an
  IPv4-mapped address counts as the IPv4 address, in every authentication failure limiter and in the OpAMP connect
  guard (`opamp.connectRatePerIp`; one key function, `realip.LimiterKey`). Behind a proxy the client IP is real only for
  addresses listed in `server.trustedProxies`.
- Enrollment token lookups. Only tokens of the documented form (`myre_` followed by 32 to 120 letters, digits, `_` or
  `-`) are looked up. Unknown tokens are cached negatively for 10 seconds in a separate cache of 1024 entries, so they
  cannot evict valid tokens from the cache of 4096 entries; concurrent lookups of one token are coalesced; lookups of
  tokens without any cache entry are limited per replica to 100 per second (burst 200) and answered with `429` and
  `Retry-After` beyond that. Refreshing a token seen before is not charged. Usage statistics are written with one
  statement per token every 5 seconds.
- Agent authentication. An `Authorization: Bearer myre_...` header must be valid when present. With
  `opamp.requireEnrollmentToken` a token is mandatory. With `opamp.requireClientCert` a verified client certificate is
  mandatory too (token and certificate both). A certificate is either verified by myriarchd's own TLS or forwarded in a
  configured header by a trusted proxy and verified against the client CA.
- Install routes. The plan for installing a host from the server and its downloads need a valid enrollment token,
  always: also where `opamp.requireEnrollmentToken` is off ([Installation from the server](#installation-from-the-server)).
  The rules of the OpAMP endpoint apply, including the client certificate rule, the token cache and the bound on lookups.
  Failed authentications on these routes are limited per client address as well, because the routes can be reached by
  anyone and share the lookup bound with the OpAMP endpoint. The download of an installer bundle is such a route. Rendering
  the commands that a person copies to a host is not: it is an ordinary call with a session or an API token and the
  permission `read`.
- Credential binding. An instance UID is bound to the enrollment token (or certificate subject) it first registered
  with. A connection that presents another credential for that UID is rejected, an `identity_conflict` event and an
  audit entry are written, and an administrator re-enrolls the agent. Optionally an agent whose bound token was revoked
  or expired moves to a valid one automatically (`agent.rebind`).
- The download-token secret (32 bytes) comes from a file or is created once in the key-value store.

## OpAMP server

myriarchd mounts the OpAMP handler of the Go library into its own HTTP server, so UI, REST and OpAMP share one listener. The
library's connection-context hook must be installed on that server; without it the plain HTTP transport fails.

### Connection admission

For each WebSocket handshake and each HTTP poll, in this order:

1. During shutdown: 503 with `Retry-After: 10`.
2. Per-client-IP token bucket: 429 with `Retry-After: 5`.
3. WebSocket only: above `maxConnectionsPerReplica` (default 10,000): 503 with `Retry-After: 30`.
4. Authentication (above): 401 with `WWW-Authenticate: Bearer`, or 503 when the credential store is unavailable.
5. Replica-wide token bucket (default 200 new connections per second, burst twice that): 429 with `Retry-After: 5`.
   It is charged after authentication so that bad credentials cannot exhaust it for real agents.
6. Optional per-token connection limit.

The connection then gets its own callbacks bound to its state: credential, remote address, transport and the last
sequence number.

### Message processing

For each agent message, in this order:

1. The instance UID must be 16 bytes, else a bad-request error response. A WebSocket connection speaks for exactly one
   instance UID (plus a UID the server issued on it); a switch closes the connection.
2. Check the credential binding and detect cloned images. When a message for a UID comes from another host while the
   original is still online, the newer connection receives a new UUIDv7 through `AgentIdentification` and a request for
   the full state, plus an `identity_conflict` event.
3. The first message of a WebSocket registers the connection under its UID and replaces an older connection of the same
   agent. A known agent is marked online with replica, transport and address; an unknown one only after step 6.
4. `agent_disconnect` is handled like the end of the connection.
5. Last-seen is recorded in memory and flushed in batches (15 seconds).
6. The server asks for the full state when the message has no description and the agent is unknown, the message is the
   first of its connection, or the sequence number does not follow the previous one. Reconnecting clients send an almost
   empty message, so this request is essential.
7. A description updates the agent: attributes are converted to strings (bool as `true`/`false`, integers in decimal,
   floats in the shortest form, bytes in base64, lists and maps as JSON). The kind is `updater` for the updater's
   service name, `collector` when the agent accepts remote config, else `unknown`. Events: `registered`,
   `version_changed`, `description_changed`. For collectors the assignment runs synchronously (errors are logged and the
   connection stays up). For updaters the collector link is stored, subject to the linking rule.
8. Health is stored recursively with times; an event is written when healthiness flips.
9. The effective configuration is stored by hash when it changed and is within the size limit (1 MiB default); an empty
   content type means YAML.
10. A remote config status is stored. `FAILED` for the desired hash marks that hash as failed. Events `config_applied`
    and `config_failed`. A report of `APPLYING` also feeds the rule for a
    [stale `APPLYING`](#stale-applying-after-a-reconnect).
11. Available components are stored by hash. If only the hash arrives and the set is unknown, the server asks for the
    components. When the set of a collector changed, the assignment runs again, because the rendering validates against it.
12. Package statuses are stored; an event on status change. Download progress is compared at whole percent.
13. If anything changed, `AgentStateChanged` is published.
14. The reply is built by the reconcile step.

### Reconcile: what the reply contains

- The server capabilities are always set (accepts status, offers remote config, accepts effective config, offers
  packages, accepts package status). The library does not set them.
- Remote config is offered when the agent accepts it, a desired hash exists, the agent has not reported that hash as
  applied, and the hash is not marked failed (a resync clears the mark). The same hash is not offered twice within 60
  seconds on one connection. A hash that the agent reports as `APPLYING` is not offered again, except under the rule for a
  [stale `APPLYING`](#stale-applying-after-a-reconnect). The message carries exactly one entry, key `myriarch.yaml`,
  content type `text/yaml`, and the hash. An empty configuration map is never sent, because the supervisor would start its
  no-op pipeline.
- A supervisor that rolls back reports `APPLIED` for the old hash, so the failed marker must be respected or the
  offer would loop. A repeated failure of the same hash sets the marker again.
- Package offers go only to updaters that accept packages, only when offers exist and the reported package hash
  differs. An empty offer set is never sent because the client would delete its local packages. Download tokens are
  minted when the offer is sent (see [updater-protocol.md](updater-protocol.md#package-offers)).
- A restart command is always a separate message, because a command replaces all other fields. It is sent only to agents
  that accept it; otherwise the REST caller gets a precondition error.
- Connection settings are not offered. The supervisor stores them only in memory and disables certificate checks when
  no certificate is offered.
- For performance, heartbeats must not query the database. Each connection caches the desired and failed hashes, the
  reported hash, the offer hash and the capabilities. The cache is refreshed on own writes, on desired-state,
  assignment and command events, after a bus reconnect and at least every five minutes. Every write that changes
  desired state, failed marker or offers must call `DesiredStateChanged`. A status that another replica stores is none of
  these events. The one decision that rests on the stored status is the offer for a stale `APPLYING` of a pooled HTTP
  session (see [Stale APPLYING after a reconnect](#stale-applying-after-a-reconnect)).

### Stale APPLYING after a reconnect

A supervisor that restarts while it applies a config has stored the status `APPLYING`, reports it again after it
reconnects, and has no timer that ends it
([upstream notes](upstream-notes.md#restart-while-a-remote-config-is-applying)). The base rule never offers a hash again
that the agent reports as `APPLYING`, so the target of a configuration rollout would wait for the `applyTimeout` of its
policy (10 minutes for configuration by default). The server therefore offers the desired config once more under this
rule. It is evaluated per connection from the cached state of the session, so heartbeats stay free of database access;
only a pooled HTTP session reads the agent, once, when an offer is due.

- **A window opens** when the agent reports `APPLYING` for a hash on a connection and either this connection did not
  offer that hash itself (a new WebSocket connection, another replica, a new pooled HTTP session), or the server had
  stored the very same status already (the agent reported it again: after a reconnect, after a restart on a long-lived
  pooled HTTP session, or because the full state was requested). The window starts with that report. A status that this
  connection caused with its own offer opens no window: the `applyTimeout` of the rollout bounds it. Any other status
  closes the window, and `APPLYING` for another hash is judged by the same rules.
- **A status that the server stores anew is a new episode.** The status differs from the one stored before. It replaces an
  open window, also one for the same hash, and ends the record of an earlier stale offer. A session that did not see the
  report that ended an earlier episode (the polls of one agent are spread over the replicas) therefore does not reuse its
  old window when the agent applies the same hash again, as after a rollback.
- **The offer** goes out with the next reply of the connection (a heartbeat, a poll or a push) when all of these hold:
  `opamp.staleApplyingAfter` is greater than 0; the window of the desired hash has been open for at least that long
  (default 90 seconds); the cached status of the agent is still `APPLYING` for the desired hash; the hash is not marked
  failed; this connection has not made such an offer for the hash yet; and the conditions of every offer hold (the agent
  accepts remote config, the rendered body exists and is not empty, the same hash is not sent twice within 60 seconds on
  one connection). The message is the ordinary remote config with one entry. A supervisor answers it with `APPLIED` at once
  when its merged configuration did not change, without restarting the collector.
- **A pooled HTTP session decides on the stored status.** It shares its agent with the sessions of the other replicas, and a
  status that one of them stores does not reach its cache: after `APPLYING` on one replica and `APPLIED` on another, the
  first still caches `APPLYING` until its next refresh. When the cache says that an offer is due, the session therefore
  reads the agent again, replaces its cache and makes the offer only if the stored status is still `APPLYING` for the
  desired hash. A read that fails makes no offer, and the next reply tries again. That is one read for an offer that is due.
  Heartbeats before that, WebSocket sessions (their connection is the only writer of the status of its agent) and a
  resync stay free of database access.
- **At most once per connection and hash.** The record of that offer ends when the agent reports another status or the
  server stores a new episode of the hash.
- **The time counts from the report on the connection**, not from the time the status was stored. A connection that ends
  before the time has passed never causes an offer, so an agent that flaps cannot be offered the same config on every
  connection, and an agent that comes back after a long absence is not offered the config before it has told its current
  status. An agent that reports no status in its full state opens no window, and its target waits for the `applyTimeout` as
  before. The time of the store only postpones an offer: the status cannot have been unchanged for longer than it has been
  stored, so when another replica stored a newer status of the same hash after this session opened its window, the count
  starts at that time. A stored time in the future of the clock of this replica is ignored.
- **Visible** as one agent event `config_offered` with severity `warning` and `details.reason` `stale_applying` (with
  `hash`, `resync: false` and `applyingSeconds`), as the counter `myriarch_opamp_config_reoffers_total{reason="stale_applying"}`
  and as a log line. The rollout engine is unchanged: the target reaches `verifying` when `APPLIED` arrives.
- **Off** with `opamp.staleApplyingAfter: 0`. The value must not be negative. Keep it above the `config_apply_timeout` of the
  supervisors (30 seconds in the shipped templates), so that a supervisor that is merely slow is not offered the config
  again: the repeated offer makes it report `APPLIED` before the collector has proven healthy, and with
  `automatic_config_rollback` it records the configuration as the last working one.

### Push path

A subscription on desired-state, command and assignment events reloads each affected local WebSocket agent, reconciles,
and sends a non-empty reply. The library's send call ignores its context and a write deadline on the socket is reset
by the WebSocket library, so each send runs in a goroutine and a connection that does not accept the data within
`sendTimeout` (10 seconds) is closed. At most one push is in flight per connection. HTTP agents receive changes with
their next poll response. A resync command clears the failed marker and requests the full state.

### Connection end, leader jobs and shutdown

- When the current WebSocket connection of an agent ends, last-seen is flushed, the agent is marked offline and
  `AgentStateChanged` is published. HTTP polls never mark an agent offline; the sweeper does.
- Leader jobs: mark agents stale after `opamp.offlineAfter` (90 seconds, three heartbeats) every 15 seconds; remove
  replicas without a heartbeat for 30 seconds every 10 seconds and mark their agents offline; refresh gauges every 30
  seconds. Every replica sends its own heartbeat with connection count and leader flag.
- Shutdown: new connections get 503, existing ones are closed gradually (at most 100 per second), last-seen is flushed
  and the replica's agents are marked offline.

### Pitfalls to remember

- The connected callback receives an already cancelled request context for WebSocket, and the message callback receives
  a background context: use explicit timeouts for database calls.
- The full state arrives only with the first connection of a client; the available components arrive as a hash first.
- The first effective configuration of a supervisor comes from the supervisor itself with an empty content type, and the
  collector's follows (about 30 KB with defaults). Only a changed hash is stored.
- Health status strings are `StatusStarting`, `StatusOK` and `StatusStopping`; the top-level `healthy` flag decides.
- Heartbeats are empty messages with an increasing sequence number.
- A remote config status that comes back after a reconnect is not progress. `APPLYING` can be the stored status of a
  supervisor that restarted while it applied (see [Stale APPLYING after a reconnect](#stale-applying-after-a-reconnect)).

## HTTP server

- Routes are registered from the route table as `METHOD /api/v1<pattern>`. Duplicate operation IDs and pattern
  conflicts are startup errors. `x-permission: public` maps to no authentication, `custom` to handler-level
  authentication (used for webhook and artifact download), anything else to a required permission.
- Middleware order: request ID, real client IP, access log, metrics, recovery, body size limit, authentication by route
  mode, CSRF, permission check (403).
- The OpAMP path skips user authentication; `/healthz`, `/readyz` and `/metrics` (on an optional second listener) exist;
  `/licenses` serves `LICENSE` and `THIRD_PARTY_NOTICES` as `text/plain` without login (route decision `public`);
  everything else serves the embedded UI.
- Only a header read timeout is set. A global write or read timeout would cut WebSocket and SSE connections. Response
  writer wrappers must pass through hijacking (WebSocket upgrade) and flushing (SSE).
- Graceful shutdown uses `server.shutdownTimeout`; the grace period of the pod must be at least `preStopSleepSeconds` + `server.shutdownTimeout` + 5 seconds ([High availability](../operations/high-availability.md#size-the-shutdown)).

## REST notes for the core resources

- Agent list: `outdated=true` compares with the newest stable release of the distribution (unknown release: none are
  outdated), repeated `attr=k=v` parameters follow the OR-within-key rule, and a summary carries site attributes,
  config sync state and whether an updater is linked.
- Restart requires the capability and an online agent, else 412. Resync clears the failed marker first, also for offline
  agents. Delete works only for offline agents. All three are audited.
- Rollout targets list: without `sort` (or with `sort=wave`) targets are ordered by wave, hostname and agent ID, stable
  while statuses change. `sort=activity` orders by status: in_progress and verifying, waiting_window, failed and
  rolled_back, pending, succeeded, skipped, cancelled; ties by wave, hostname, agent ID. Rows can move between pages while
  the rollout runs. The UI uses `activity`. Any other value is a 400. The optional `agent` (agent ID) filter returns only
  the target of that agent; the UI polls it for the open detail view, which therefore stays current when the target moves
  to another page. An `agent` that is not a UUID is a 400.
- The system info endpoint derives the OpAMP URL from the public URL (`http` to `ws`, `https` to `wss`) plus the OpAMP path.

## Live stream

`GET /events/stream` is a server-sent event stream. The hub turns bus events into `agent.updated` (coalesced per agent,
at most one per second), `agent.removed`, `rollout.updated`, `fleet.updated`, `gitops.synced`, `release.new`,
`stats.changed` and `resync`. A client with a full buffer (256) is sent `resync` and its buffer is cleared. A `: ping`
comment goes out every 15 seconds. Headers include `Cache-Control: no-cache` and `X-Accel-Buffering: no`, and the
stream is flushed after every event. There is no replay: after a reconnect the client refetches what it shows.

## GitOps reader

- Source: a local directory (`dir:<path>`, revision is a hash over sorted paths and contents) or a Git remote. Git uses
  HTTPS with a token file or SSH with a key and a known-hosts file, a configurable CA, a sub-path and an in-process
  clone that is fetched and hard reset to the remote branch.
- Parsing. All `*.yaml` and `*.yml` files below the path are read, except directories starting with `.` and `samples/`.
  A file may hold several documents. Each has the envelope `apiVersion: myriarch.dev/v1alpha1`, `kind`, `metadata.name`
  (lowercase DNS-label style) and `spec`. Unknown fields are errors, found by walking the YAML node tree against the
  target types so that line numbers survive; free-form areas are exempt (blueprint config, fragment config, fleet
  parameters, update params, inventory parameters, executor extra variables). Duplicate names per kind are errors.
- Bounds: symbolic links and other non-regular files are rejected (`not_regular_file`); a file may be at most 4 MiB;
  there is a limit on the number of YAML files (2,000) and their total size (64 MiB); a time budget applies. Violations
  are errors (`file_too_large`, `too_many_files`, `repository_too_large`, `repository_too_complex`,
  `time_budget_exceeded`).
- Secrets never belong in the repository. Blueprints use `${env:VAR}` or `${file:/path}`, which the collector resolves
  on its host. Executor definitions only name environment variables or files on the myriarchd host.
- A sync computes the revision. If unchanged, it only updates a status timestamp (an "unchanged" entry is logged at most
  hourly). Otherwise it parses, validates the whole snapshot including a rendering of every sample agent, and applies it
  only if it is completely valid. An invalid state keeps the previous one, sets the status to `error` and sends a
  `gitops.sync_failed` notification once per commit. After applying, assignment and the planner are triggered and an
  audit entry `gitops.apply` is written. A failing post-apply step is retried in the next cycle.
- Trigger: initial sync at startup, polling, a manual request and a webhook (`POST /gitops/webhook`). The webhook accepts
  a shared-secret header or an HMAC signature header (`X-Hub-Signature-256`), compared in constant time, and answers 404
  when no secret is configured.
- Sample agents (`samples/agents/*.yaml`) are inputs for `myrctl` and validation; they are not stored as objects.

## Blueprint rendering

Rendering is a deterministic function of the snapshot, a fleet, the agent's attribute view, inventory parameters and
optional preview overrides. myriarchd and `myrctl` run the same code.

### Attribute view

A flat string map: all non-identifying attributes, overlaid by identifying attributes, plus server labels under the
prefix `labels.`. Server labels come from the enrollment token and from inventory entries. The prefix belongs to the
server: reported attributes with it are discarded. Except for `labels.*`, attributes are self reported, so an attribute
selector is a convenience and not an access boundary. Where fleets must be kept apart, select on `labels.*`.

### Algorithm

1. Resolve the chain from the fleet's blueprint through `extends` to the root. Missing blueprints and cycles are errors.
2. Declare parameters from root to leaf. A child may redeclare a parameter with the same type; the redeclaration
   replaces the parent's completely. A different type is `parameter_type_conflict`. Resolve values; later wins:
   default, fleet parameters, value from an attribute (`fromAttribute`), inventory parameters, preview overrides. Then
   check type, enum, pattern (RE2, whole string) and range. Missing required values are `missing_parameter`; a fleet
   parameter that is not declared is `unknown_parameter`.
3. Merge layers without evaluating templates: for each blueprint in the chain merge its config, then each fragment whose
   `when` is true (in file order, merge then apply its `remove`), then apply the blueprint's `remove`. Record the applied
   layers.
4. Evaluate templates over the finished tree (keys and string values).
5. Validate. Structural validation always runs; component validation runs when the agent's component set is known.
   Errors produce no result.
6. Serialize canonically and hash.

### Merge semantics

Maps merge recursively. Everything else, including lists, scalars and `null`, is replaced entirely by the overlay.
A `null` in an overlay means "default configuration of the component", not removal; removal is only through `remove`,
which uses confmap paths with `::` separators. Extending a list therefore means writing the complete list. Keys that
contain `::` are forbidden. The supervisor additionally concatenates `service::extensions` and merges resource
attributes by name when it combines the remote configuration with local files, so blueprints need not mention the
`opamp` extension.

### Templating

- Delimiters are `[[` and `]]` (Go templates, missing keys are errors). Collector syntax (`${env:...}`, `${file:...}`,
  `$$`) is not interpreted. Values starting with `[[` must be quoted in YAML; the validator reports `unquoted_template`
  for the typical mistake.
- A string that is exactly one expression is evaluated typed: the result keeps its type (a number stays a number, a list
  a list, a string with leading zeros stays a string). Anything else is an interpolation that yields a string.
- Rule GIT-4: lists and maps are never printed implicitly into text. Interpolating one is a `template_error`; the author
  first converts it with `join`, `toJson` or `str`.
- Data available: `.params`, `.agent.id`, `.agent.attributes`, `.fleet.name`, `.fleet.labels`, `.blueprint.name`.
  Functions: `attr`, `attrOr`, `hasAttr`, `param`, `default`, `required`, string helpers, `contains`, `hasPrefix`,
  `hasSuffix`, `join`, `split`, `list`, `dict`, conversions, integer arithmetic and `toJson`. `define`, `template` and
  `block` are forbidden; templates cannot reach the environment, files or the network.
- Resource bounds, which apply to every template string including map keys and executor extra variables: text produced
  by any helper or direct output is limited to 64 KiB, and a value is checked before it is converted to text so that
  composite values cannot blow up memory; all helpers together produce at most 8 MiB per string; `list` and `split`
  yield at most 10,000 elements; `printf` allows no `*` width or argument index and widths of at most 256; `range`
  accepts only lists and maps, nests three deep and iterates at most 10,000 elements; evaluation of one string is limited
  to 5 seconds, checked on every pipeline that executes.
- Error messages never quote untrusted values in full: type errors name only the type, values are quoted up to 40
  characters, and the message of `required` up to 200.

### Canonical form and hash

The result tree holds only maps, lists, strings, booleans, integers, floats and null. It is serialized with the YAML
library at indent 2, sorted keys, no comments and no document marker. The configuration hash is the SHA-256 of exactly
those bytes. The golden files in the repository are the byte-exact reference.

### Validation

`ValidateSnapshot` checks schema, references, cycles, parameters, templates, selectors, policies and executors (statically
only: no environment variables, no network access), then renders each fleet with each matching sample agent and validates
the result. `fleet_ambiguous` and `sample_unassigned` are warnings. `ValidateConfig` checks structure (top-level keys,
component IDs, pipelines referencing defined components, unused components) and, with a component list, that every
component type exists. Deprecated component names are accepted as aliases (warning `deprecated_component`), and aliases
resolve in both directions because the reported component sets contain old and new names while the CLI list holds only
the new ones. The exporter `otlp` is an alias, the receiver `otlp` is not. There is no binary validation of rendered
configurations on the server: a collector's `validate` fails on receivers that exist only on another operating system.
The supervisor's `validate_config` option validates on the agent instead.

Issue codes are stable. The classes are: syntax and schema (`yaml_syntax`, `unknown_field`, `invalid_name`,
`duplicate_object`, `invalid_field`, `not_regular_file`), references (`blueprint_not_found`, `policy_not_found`,
`executor_not_found`), blueprint structure (`extends_cycle`, parameter and template codes), policies (`wave_invalid`,
`window_invalid`, `policy_invalid`), executors (`executor_invalid`), selection (`selection_invalid`), and configuration
checks (`config_invalid_toplevel`, `invalid_component_id`, `invalid_pipeline`, `undefined_component`,
`unknown_component`, `deprecated_component`, `unused_component`, `env_syntax`). `myrctl validate` exits 0 when valid
(warnings allowed), 1 on validation errors and 2 on usage or internal errors; `--strict` turns warnings into failures
and `--output json` prints the issues.

## Fleet assignment

Fleets and agents are matched by selectors over the attribute view.

- Selectors combine `matchAttributes` (exact equality) and `matchExpressions` (`In`, `NotIn`, `Exists`, `DoesNotExist`,
  `Glob`) with AND. An empty selector matches everything.
- Inventory labels apply first. Among the matching fleets the highest priority wins. Equal highest priorities leave the
  agent unassigned with the error `ambiguous: fleets a, b have equal priority N`. No candidate means unassigned without
  an error. Only collectors are assigned; an updater follows its collector.
- Each replica keeps the active snapshot in memory and reloads it on GitOps and bus-reconnect events and at least every
  five minutes. `AssignAgent` is called synchronously during registration and must be fast.
- For one agent: compute effective labels (token labels plus inventory labels, inventory wins) and persist them when
  they changed; match; render with the agent's component set when known; on a render error keep the previous assigned
  hash and record the error; store the rendering with its provenance; record the assignment (fleet, assigned hash,
  target version, error, time).
- Bootstrap versus rollout. Each agent has three hashes: assigned (what the current GitOps state renders for it), desired
  (what is offered) and failed. A new agent, or one that changes fleet, gets desired set to assigned at once. Otherwise
  desired changes only through rollouts. A rollout that finished with an offline agent skipped leaves it behind; if the
  agent's assigned hash equals the skipped target's hash, the change is treated as approved and desired is set
  directly (catch-up), without a new rollout.
- Reassigning everyone runs on the leader with a small worker pool, then publishes desired-state changes and an
  assignment event in chunks.

## Rollout planner and engine

### Policy

A `RolloutPolicy` defines waves, `maxConcurrent`, `healthWait`, `applyTimeout`, `offlineGracePeriod`, a failure threshold,
`onFailure` (`pause` or `rollback`), a timezone attribute and default timezone, and maintenance windows. Defaults, which
are copied into the policy snapshot of each rollout:

| Setting | Default |
|---|---|
| waves | one wave `all` at 100 percent |
| `maxConcurrent` | 0 (unlimited) in a policy, 50 in the built-in policy |
| `healthWait` | 60 seconds |
| `applyTimeout` | 10 minutes for configuration, 30 minutes for versions |
| `offlineGracePeriod` | 15 minutes |
| failure threshold | `maxFailed: 1` when both numbers are 0; setting only a percentage gives a pure percentage threshold |
| `onFailure` | `pause` |
| timezone attribute | `site.timezone`, then the policy default, then `rollouts.defaultTimezone` |

A fleet without a reference uses the policy `default`; when no object has that name the built-in policy applies.

### Waves

A wave has either a selector or a percentage from 1 to 100, never both (`wave_invalid`). Percentages must strictly
increase. Targets are assigned when the rollout is created: a selector wave takes all unassigned targets that match; a
percentage wave takes unassigned targets in a stable pseudo-random order (ascending `sha256(rolloutID || agentID)`)
until the cumulative share `ceil(percent * total / 100)` is reached. If the last wave is below 100 percent an implicit
`rest` wave is added. Empty waves are `skipped`. Waves run in sequence: the next one starts when all targets of the
current one are final and the failure threshold was not reached. A wave with `requireApproval` waits in
`waiting_approval` until `approveRolloutWave`. The `percent` of a wave is a cumulative share of all targets of the
rollout, not progress; the UI labels it as such ("up to N % of all targets").

### Targets

A target is one agent in a rollout. Statuses: `pending`, `waiting_window`, `in_progress`, `verifying`, `succeeded`,
`failed`, `skipped`, `cancelled`, `rolled_back`.

- Start (pending to in_progress) requires: the agent is online, its local maintenance window is open and `maxConcurrent`
  is not exhausted. A configuration rollout sets the desired hash (clearing a failed marker for a retry first) and pushes.
  A version rollout calls the executor once per tick and wave with all startable targets, after an environment-dependent
  `Validate` once per rollout and tick. Errors fail the affected targets with the message.
- Report (in_progress to verifying): for configuration, `APPLIED` for the target hash; for version, the collector reports
  the target version.
- Success (verifying to succeeded): the agent stays online and healthy for `healthWait`. Unhealthy reports that were
  sent before entering `verifying` (the old process stopping) do not count, and the target waits up to two minutes for
  the new process. The normal start-up sequence of the new process (unhealthy with `StatusStarting`, `StatusStopping` or
  no status and no error) does not count either, for at most `applyTimeout`. Any other unhealthy report fails the
  target. Crash loop (version rollouts): if the collector was healthy on the new version once and then falls back to a
  start-up report more than once, the target fails. A single restart after the update, for example due to a configuration
  that was applied meanwhile, is allowed.
- The health wait ends at `verifiedAt + healthWait` of the policy snapshot (60 seconds when the policy sets none, see
  [Policy](#policy)). The engine evaluates it on each tick, and only for a rollout that is `running`, `paused` or
  `waiting_approval`: the `verifying` targets of a `failed` rollout stay as they are until an operator decides. The API has
  no field for the end. A client computes it from `verifiedAt` of the target (`GET /rollouts/{id}/targets`) and
  `policy.healthWait` of the rollout (`GET /rollouts/{id}`), and the web UI shows the remaining time that way, for a
  target and for the latest end among the targets of a wave, with the name, `healthWait`, path and commit of the policy
  snapshot as the source.
- Failure: `FAILED` for the target configuration hash, `INSTALL_FAILED` of the updater for the offered version (only
  reports after the start of the current attempt count), unhealthy during the wait, offline during the wait,
  `applyTimeout` exceeded, or an executor error.
- Offline targets wait; after `offlineGracePeriod` from the start of the wave they are `skipped`. Targets outside their
  window are `waiting_window`. When a rollout is aborted, targets that never started become `cancelled`.
- At most one configuration rollout and one version rollout run per fleet at the same time. If a version update
  restarts a host while its configuration target is in the wait, that configuration target fails (retry it, or finish
  the version rollout first).

### Maintenance windows

A target may start only inside at least one window; running targets are never interrupted. No windows means always. The
zone comes from the window's `timezone`, or `agent` (the agent's timezone attribute), falling back to the policy and
then the server default, using the IANA database including daylight saving. `days` names the weekday on which the window
begins. If `end <= start` the window crosses midnight; `start == end` means 24 hours from the start.

### Failure threshold, pause and rollback

The threshold is reached per wave when `maxFailed > 0 and failed >= maxFailed`, or `maxFailedPercent > 0 and
failed * 100 >= maxFailedPercent * targets of the wave`. The wave becomes `failed`.

- `pause`: the rollout is `paused` with a reason and a notification. Running targets finish their evaluation, no new ones
  start. The operator can resume (which acknowledges the failures so far), retry failed targets, abort or roll back.
  Retrying failed targets increases the attempt counter, so a repeated failure of the same agent counts again.
- `rollback`: all started targets return to their original state (configuration: desired is set back to the previous
  hash; version: the previous version is offered or the executor is called with it) and become `rolled_back`; the rollout
  goes `rolling_back` then `rolled_back`.
- The rollout status `failed` arises only if a rollback cannot finish within `applyTimeout`, or the rollout cannot proceed
  at all (for example every startable target of a wave fails in the executor).
- A rollback contradicts Git: the agent shows drift until Git is reset or a new rollout starts. Targets cancelled by a
  rollback are not rolled out again automatically.

### Planner

The planner runs on the leader after each applied GitOps state and every minute. Runs are serialized. There is at most
one active rollout per fleet and kind.

Relevant agents are assigned collectors without an assignment error: for configuration those with a desired hash different
from the assigned one; for versions those whose version differs from the effective target, provided the executor is not
`none` and a target exists (not in hold). Exclusions, evaluated from the latest target of the agent for this target value:

- The latest target is `failed` or `rolled_back`: only a new commit, a new version or an operator's retry starts it again.
- The latest target is `cancelled` in a rollout that is `aborted`, `rolling_back` or `rolled_back`: the operator or the
  failure policy stopped it on purpose.
- `cancelled` through `superseded` does not exclude.
- A `skipped` target in a completed configuration rollout leads to catch-up. For versions there is no catch-up: such agents
  are relevant again, so that windows and `maxConcurrent` apply, except an agent whose target was skipped because it was
  offline, which becomes relevant only after it has connected since.

Per fleet and kind: first the version selections are reconciled with Git (see [version-selection.md](version-selection.md)).
If an active rollout has open targets (pending, waiting_window, in_progress, verifying) with a different target than
their agent's current one, it becomes `superseded` (open targets cancelled, the executor is told to cancel), and a new
rollout may be created. If all open targets still match, relevant agents without a target ("latecomers") are added to the
last wave. With no active rollout a new one is created for all relevant, non-excluded agents; none relevant means none
created. A rollout in `failed` or `rolling_back` is never replaced automatically. A `paused` rollout can be superseded by
a fix. Creating a rollout writes the audit entry `rollout.create` and the notification `rollout.started`.

After a fleet was planned without an error, the planner records the effective target version of the fleet in the key-value store
(`rollout/planned-target/<fleet>`; the value is the version without `v`, and empty for a fleet without a target), when the record changes
and after it created the rollout for that target; for a version chosen in the UI it also sets `plannedAt` of the selection. Both say
that the planner has handled the target. A target reaches every replica (the event of a Git sync or of a selection) before the leader
has created the rollout for it, and the installation of hosts from the server reads these marks, so that a new host does not take the
new version past the waves ([Installation from the server](#installation-from-the-server)). A leader writes the records of all fleets
again when it starts a term, because another leader may have written them in between. They are not purged.

### Engine

The engine is stateless between ticks, so leadership can change at any moment and every step is idempotent. A tick runs
every `rollouts.tickInterval` (5 seconds) and earlier after agent or rollout events, throttled to once per second. Each
tick for an active rollout: derive target transitions from agent state, check the failure threshold, advance waves
(notification on approval wait and completion), start startable targets, and write targets, progress and status through
the guarded store calls. Operator actions (`pause`, `resume`, `abort`, `approve`, `rollback`, `retry-failed`) run on any
replica, change state only by compare-and-set and are audited; an invalid transition is a precondition error.

## Executors

The executor interface has `Validate` (environment dependent), `Start` (per wave, with all startable targets) and `Cancel`.

- `updater`: for each target, find the linked updater (error if none or offline), ensure the artifact for the host's
  operating system and architecture, store a package offer with the target version, and notify that the updater's desired
  state changed. `Cancel` clears the offers. See [updater-protocol.md](updater-protocol.md).
- `manual`: see [version-selection.md](version-selection.md#executor-manual).
- `webhook`: a JSON payload (`event: collector.update`, `deliveryId`, `rolloutId`, `wave`, `fleet`, `distribution`,
  `toVersion`, `params`, and `targets[]` with agent ID, host name, OS, architecture, current version and attributes), sent
  in batches. Headers `X-Myriarch-Delivery`, `X-Myriarch-Timestamp` and `X-Myriarch-Signature: sha256=<hex>` where the
  MAC is HMAC-SHA256 over `timestamp + "." + body`. A 2xx answer means accepted. Delivery is at least once, with a
  deterministic delivery ID for deduplication. Cancel sends `collector.update.cancel`.
- `awx`: `POST <url>/api/v2/job_templates/<id>/launch/` with a bearer token and `{"limit": "<hosts>", "extra_vars": {...,
  "myriarch": <payload>}}`. The limit comes from an agent attribute (default `host.name`) and every value must be a plain
  host name, otherwise only that target fails. The job ID is stored as `awx-job:<id>`; cancel is `POST .../jobs/<id>/cancel/`
  on a best-effort basis.
- Secrets. A repository names an environment variable or a file; the myriarchd configuration decides what is readable: names
  must start with a configured prefix, files are relative to a secret directory without `..`, and in production every
  secret is bound to the hosts it may be sent to, and target URLs must be in an allowlist. HMAC secrets have at least 32
  bytes. Static checks happen in validation, environment checks in `Validate` when a rollout starts.
- The end of a version update is always recognized from the version the collector reports.

## Artifact cache

- Source layout: `<sourceBaseUrl>/<tag>/<asset>` with `<asset>.sha256` and `<asset>.sigstore.json` beside it. The
  default source is the public release page of the upstream collector releases; an internal mirror must keep the
  layout.
- `Ensure` creates a database entry, loads the checksum file (64 hex characters), loads the Sigstore bundle, verifies the
  bundle against the digest (identity, issuer and the requirement that the tag equals the artifact version, using the
  trusted root file or TUF), then streams the archive into a temporary file, compares its SHA-256 with the checksum file,
  moves it atomically into the cache and marks it ready. Concurrent calls for the same artifact run once. Downloads
  per replica are limited to three at a time, with an idle timeout, an overall size cap and allowed redirect hosts.
- Releases are mutable upstream. Once an artifact is ready, its pinned SHA-256 wins; a different value later is logged as
  an error.
- Download endpoint (`downloadArtifact`): verifies the token (signature, artifact ID, and that the agent currently has an
  offer referencing this artifact; age only when a TTL is set). A replica that lacks the file fetches it from the source
  and checks it against the stored SHA-256. It serves ranges with `Content-Type: application/gzip`.
- Install grants (from v0.2.0). The endpoint also accepts the second kind of `myrd_` token, the install grant of a host
  that is not enrolled yet. The signed payload holds the artifact ID, the ID of the enrollment token that the host
  presented to get its plan, the time and the kind `i` (the token of an offer has no kind, and the kind is part of what is
  signed, so neither can be turned into the other; an unknown kind or a grant without an enrollment token is refused). A
  grant is accepted while it is younger than `install.grantTtl` and only while its enrollment token exists and is neither
  revoked nor expired; the token is read from the database on every request, so a revocation applies at once on every
  replica, and a token that is gone, revoked or expired looks the same. It is accepted only while `install.enabled` is on:
  a server that runs with the installation turned off (also after a restart on the same database) refuses every grant with
  401, whatever its lifetime is, and mints none. It needs no package offer. Besides the artifact it
  gets `<filename>.sha256` (`<hex>  <filename>`, like `sha256sum`) and `<filename>.sigstore.json` from the record, the files
  that the installer checks the artifact with; the token of an offer gets neither. A grant is issued, and a file is served,
  only for an artifact that is `ready`, has a pinned SHA-256 and, where signatures are required, a verified bundle. The log
  names the token of a grant by its ID, never the grant.
- Preparation without waiting (`Prepare`). The install plan needs an artifact now and cannot wait for a download of
  100 MB. `Prepare` returns a ready artifact, and otherwise starts `Ensure` in the background (once per replica however
  many callers ask) and reports that it is pending. A record in the status `downloading` that changed within ten minutes
  belongs to another replica or to the prefetch of the leader and is not started again; an older one belongs to a replica that
  is gone and is taken over. A failed record is tried again after a minute, and until then `Prepare` reports the failure, so
  a host that asks every few seconds cannot make the server request an upstream release in a loop. `Ensure` also fetches a
  record that claims to be ready but has no pinned hash or no verified bundle again, instead of returning it.
- The installer bundles of the release that the server runs are not artifacts. They have no row in the table `artifacts`
  (the API enum `Distribution` knows `otelcol-contrib` and `opampsupervisor` only), are kept by `internal/install` in the
  subdirectory `install` of the cache directory, and the eviction below neither counts nor removes them ([Installer
  bundles](#installer-bundles)). No migration is needed.
- Prefetch on the leader every 10 minutes: for each fleet with executor `updater` and a target, fetch the platforms of
  the members plus `prefetchPlatforms`, then evict least recently used artifacts until the size cap is met. Target
  versions of fleets are never evicted.

## Release tracker

- Polls the releases list of the upstream collector releases repository (`per_page=30`, always the same parameters, so
  the ETag matches) with a conditional request; a 304 changes nothing. A token raises the rate limit; even 304 answers
  count against the unauthenticated limit of 60 per hour, so a token is recommended, and rate-limit answers are shown in
  the status. A GitHub Enterprise API base URL or a fixture directory can replace the source.
- Tags `v0.X.Y` are distribution releases, `cmd/opampsupervisor/v0.X.Y` are supervisor releases; everything else
  (nightlies, builder tags) is ignored, as are drafts. Prereleases follow `includePrereleases`. Versions below
  `minVersion` are ignored on the first import. Assets are parsed into operating system, architecture and format.
- Breaking changes and deprecations come from the upstream changelogs at the release tag: a version heading is a
  token with major version 0 and no prerelease; sections whose heading contains "Breaking changes" or "Deprecations"
  are read with a bullet pattern that has a component, a text and optional issue numbers, with continuation lines.
  Combined headings such as `## v1.1.0/v0.162.0` occur. If the file is missing at that tag, the changelog of the newest
  tag is used because it contains all older sections. Supervisor releases use the entries of the component
  `cmd/opampsupervisor` from the contrib changelog.
- A new version is stored, announced on the bus and notified as `release.new`, or `release.breaking` when it has breaking
  changes (dedup key per distribution and version). Comparing two versions reads the stored releases in the interval.

## Notifier

A dispatcher routes notifications by event name (a trailing `*` is a wildcard) and minimum severity. Duplicates are
suppressed with a dedup key in the key-value store. Each channel is sent in parallel with a 10 second timeout and three
attempts. Events: `release.new`, `release.breaking`, `rollout.started`, `rollout.waiting_approval`, `rollout.paused`,
`rollout.failed`, `rollout.completed`, `rollout.rolled_back`, `gitops.sync_failed` and `test`.

- `webhook`: the JSON notification with an absolute link, headers `X-Myriarch-Delivery`, `X-Myriarch-Timestamp` and, when a
  secret file is set, `X-Myriarch-Signature` with the same MAC scheme as the webhook executor.
- `teams` and `slack`: an adaptive card or blocks with title, summary, facts and a link button.
- A notification's link is a UI path that the dispatcher prefixes with the public URL. Channel listings redact URLs to scheme
  and host.

## Local database for development and tests

`myriarchd dev` and the test helper start an embedded PostgreSQL. Decisions that keep it reliable: a free port with retry, a
private data directory per process, a shared binary directory protected by a file lock, locale C and UTC, TCP only, a
watchdog for orphaned servers, and a template database per test run. It must never run as root. Tests may use an
external database through a DSN variable; their queries must then be limited to their own database.

## Collector installation on hosts

These decisions concern how a collector and its supervisor are installed and configured on hosts. The behaviour of the
update itself is in [updater-protocol.md](updater-protocol.md).

### Supervisor configuration

The shipped templates for Linux, Windows and Kubernetes use placeholders written as `@@NAME@@`, which the install
scripts replace (no Go templating, so `${env:...}` stays untouched). Required content:

- `server.endpoint` and `server.headers.Authorization: "Bearer ${env:MYRIARCH_ENROLLMENT_TOKEN}"`: the token reaches the
  supervisor through the service environment, never through the file. An optional `server.tls.ca_file`.
- Capabilities: `accepts_remote_config`, `accepts_restart_command`, `reports_effective_config`, `reports_health`,
  `reports_available_components` and `reports_heartbeat` on. `accepts_opamp_connection_settings` stays off on purpose
  (see above). The defaults of `accepts_remote_config` and `reports_available_components` are `false` upstream, so they
  must be set.
- Agent: `executable` points at the stable path of the active collector (`current/<binary>` on Linux, `bin/<binary>` on
  Windows); `config_apply_timeout` 30 seconds, `bootstrap_timeout` 15 seconds, `validate_config: true`,
  `automatic_config_rollback: true`, and `startup_fallback_configs` with a minimal valid configuration for the case that
  the server is unreachable at start.
- Site attributes under `agent.description.non_identifying_attributes`: `site.type`, `site.id`, `site.name`,
  `site.country`, `site.region`, `site.timezone` and `deployment.environment.name` (all strings, leading zeros kept).
- `storage.directory` must not be pre-filled in golden images: `persistent_state.yaml` holds the instance UID, and cloned
  state produces duplicate UIDs.
- A local health endpoint (`healthcheck.endpoint`) for the supervisor.
- Kubernetes variant: `passthrough_logs: true` so collector logs reach stdout; pod and node attributes through the
  downward API with `${env:...}`; storage on a persistent volume (workloads) or a host path (per-node agents).

### Installation scripts and services

- Linux and Windows scripts are idempotent. They download the collector archive and its `.sha256`, verify the checksum
  and the Sigstore bundle, unpack into `versions/<v>`, set the `current` link (or copy on Windows), install the
  supervisor binary after the same verification, render the supervisor and fallback configuration, write the token to a
  file readable only by the service account (`0600`) or to the service's environment in the registry with a restricted
  ACL, and enable and start the service. The updater binary is installed only when its hash comes from a trusted source,
  because it runs as root or SYSTEM.
- Operating system packages and installers of the collector are deliberately not used: the contrib deb and rpm start
  their own service immediately and the MSI installs a second service, which would run a second collector next to the
  supervisor.
- The service runs as a dedicated unprivileged user with restart on failure, a raised file-descriptor limit and
  hardening options. Windows assets are `.tar.gz` too and unpack with the built-in `tar`.
- The supervisor reads `agent.executable` at start and detects version and components during bootstrap. After a binary
  change the supervisor service is restarted.

### Installation from the server

*Available from v0.2.0.* [ADR-0046](../adr/0046-host-installation-from-the-server.md) decides it: the server provides data
(versions, artifacts, the endpoint) and the host verifies code (SHA-256 and Sigstore, against an identity that the
installer builds itself, never one that the server names). Hosts download from `myriarchd` only. The settings are
`install.enabled` (default true; off, every route of the tag `install` answers 404 before it looks at a token),
`install.grantTtl`, `install.bundleSourceBaseUrl` and `install.platforms` (`linux/amd64`, `linux/arm64` and
`windows/amd64`); see the [configuration reference](../reference/configuration.md#install). Four routes serve it: the plan
(`getInstallPlan`) and the download of an installer bundle (`downloadInstallBundle`) with an enrollment token, the list of
the bundles (`listInstallBundles`) and the rendering of the commands (`renderInstallCommands`) with a session or an API
token. A person copies the commands to a host; they download the bundle, and the installer in it asks for the plan.

**Authentication.** A request needs a valid enrollment token, always: `Authorization: Bearer myre_...`, also where
`opamp.requireEnrollmentToken` is off. The token authenticates the request and binds the grants; it is never part of a
body, a URL, an answer, a log line, a metric label or an audit entry (the log names it by its ID). Unknown, revoked, expired
and missing tokens get the same 401. The rules are those of the OpAMP endpoint (see [Authentication and
authorization](#authentication-and-authorization)); in addition 30 failed authentications per client address are free,
then one more is allowed per second, and an address without budget gets 429 before any lookup happens.

**The plan.** `POST /api/v1/install/plan` (`getInstallPlan`; a body of at most 16 KiB that holds no secret). The request
names the platform (`os` `linux` or `windows`, `arch` `amd64` or `arm64`; `x86_64` and `aarch64` are accepted), the
`attributes` the host would report, an optional `fleet` and an optional `collectorVersion`. The attributes may be
`host.name`, `site.type`, `site.id`, `site.name`, `site.country`, `site.region`, `site.timezone` and
`deployment.environment.name`, with values of 1 to 253 characters without control characters or surrounding spaces;
`os.type` and `host.arch` come from the platform and the labels from the token. The server checks all of this against
allowlists first (422, one issue per problem) and decides, in this order:

1. **The fleet.** `assignment.Service.Match`, the function that assigns a connected agent, with the attributes, the
   platform and the `labels.*` of the token. No fleet: 409 `no_fleet`. Several fleets of the same highest priority: 409
   `fleet_ambiguous`. A `fleet` in the request that is another than the one found: 409 `fleet_mismatch`; nothing is
   delivered. A fleet that selects on other attributes than the ones above cannot be found. A distribution other than
   `otelcol-contrib`: 412 `unsupported_distribution`.
2. **The version** (below).
3. **The releases.** The release tracker must know the release of the collector and the release of the supervisor for the
   version (the supervisor has the version of the collector). Otherwise 412 `unknown_version` or
   `unknown_supervisor_version`; for a version that the host asked for, 422 with the same codes.
4. **The artifacts.** `Prepare` starts both downloads at once. Until both are ready the answer is 503 with the reason
   `preparing` and `Retry-After` (10 seconds; 30 after a failed attempt). The first request for a platform downloads about
   112 MB, so this can last minutes. Nothing is started for a request that failed one of the steps before.

**The version.** The plan gives the effective target of the fleet (`version.source` is `git` or `ui`; see
[Version selection](version-selection.md)). A fleet on hold or without a target: 412 `no_target_version`. While a version
rollout of the fleet is `pending`, `running` or `waiting_approval`, it gives the `fromRef` of that rollout instead
(`version.source` is `rollout`), so that the new host becomes a straggler of the last wave and waves and approvals apply to
new hosts as well; a rollout that is `paused`, `failed` or `rolling_back`, or whose hosts start from different versions
(no uniform `fromRef`), is 409 `rollout_blocking`. A `collectorVersion` of the request is accepted if it equals the version
the plan would give, the target of the fleet or a version inside the frame `spec.update.selection` in Git (`version.source`
is then `request`); otherwise 422 `version_not_allowed`.

**A target that the planner has not handled.** A target reaches every replica (through the event of a Git sync or of a selection in the
UI) before the leader has created the rollout that brings the fleet to it. A host that asked in between would find the new target and no
rollout, take the new version at once and skip the waves and approvals. So the plan does not hand out a target before the rollout planner
has handled it ([Planner](#planner)): for a version chosen in the UI until the selection has `plannedAt` (a new selection of the fleet,
also of the same version, clears it), for any other target (the one in Git, also after a selection was cleared) until the record of the
planner for the fleet equals the target. Until then the answer is 503 `preparing` with `Retry-After` (5 seconds), and nothing is prepared.
The planner usually needs seconds. A fleet that the planner has never handled has no record, so a new fleet waits for its first run, and so
does every fleet right after an update from v0.1.0 (the records stay in the database, so the restart of a leader does not make hosts wait
again). Without a leader nothing is planned, and the hosts of a fleet whose target changed wait until there is one; the other fleets are not
affected. If the planning of a fleet keeps failing, its hosts wait and the log of the leader names the reason.

The reads are ordered so that a concurrent change cannot send a host past the waves: the target comes from the memory of the assignment
service, which follows the database and never runs ahead of it, then the record of the planner, and the rollouts last. The planner writes
its record after it created the rollout, so whoever sees the record that belongs to the target sees the rollout as well; a rollout that
the leader creates after the target was read is seen with the `fromRef` that belongs to the previous target, or the target is not handed out
yet. The combination "new target, no rollout yet" does not reach a host.

**The answer.** A plan of format 1 with the OpAMP endpoint (`wss://` for an https public URL, plus `opamp.path`), the fleet,
the source of the version, the version, the URL of the artifact on the origin of `server.publicUrl` and an install grant for
each of collector and supervisor, and `updater.install` (true when the executor of the fleet is `updater`). It is
`text/plain` by default, with `Vary: Accept`, and JSON with `Accept: application/json`. The text is one `key=value` line per
key, in this order: `format`, `fleet`, `version.source`, `opamp.endpoint`, `collector.version`, `collector.url`,
`collector.grant`, `supervisor.version`, `supervisor.url`, `supervisor.grant`, `updater.install`. A value consists of the
characters `A-Z a-z 0-9 . _ : / @ + = -` and nothing else; the three URL values may also contain the brackets of an IPv6 literal in
the authority (`wss://[2001:db8::1]:8443/v1/opamp`); there are at most 64 lines and 8 KiB; lines end in `\n`; a reader accepts
only these keys, each once, with `format` first, and stops at a format that it does not know. The server writes nothing that
breaks the rules (a `server.publicUrl` with another character is a 412 `server_settings`), and `install.ParseText` is the
reference reader with the rules that the installers implement. The plan is data: the installer takes the values as they are,
checks each against a pattern and runs nothing that it contains. The URLs of `artifacts.agentBaseUrls` do not apply to a host
that is not enrolled.

| Status | `code` | Reason (`issues[0].code`) |
|---|---|---|
| 400 | `bad_request` | invalid JSON, an unknown field, a body above 16 KiB (415 for another content type) |
| 401 | `unauthorized` | none: the same answer for every token problem |
| 404 | `not_found` | `install.enabled` is off |
| 409 | `conflict` | `fleet_mismatch`, `no_fleet`, `fleet_ambiguous`, `rollout_blocking` |
| 412 | `precondition_failed` | `no_target_version`, `unsupported_distribution`, `unknown_version`, `unknown_supervisor_version`, `server_settings` |
| 422 | `validation_failed` | `invalid_value`, `unknown_attribute`, `platform_not_supported`, `version_not_allowed`, `unknown_version`, `unknown_supervisor_version` |
| 429 | `too_many_requests` | none; `Retry-After` |
| 503 | `unavailable` | `preparing` (with `Retry-After`: the artifacts are being prepared, or the planner has not handled the new target of the fleet), or none for a database failure |

**Observability.** `myriarch_install_plans_total{result}` counts the requests (`ok`, `disabled`, `unauthorized`,
`rate_limited`, `bad_request`, `invalid_request`, `unavailable`, `error`, or the reason of a refusal such as `fleet_mismatch`
or `preparing`). A plan that is issued is logged at info level with the ID of the token, the fleet, the platform and the
version; it is not an audit entry, because it is a call of a host with its token and changes no configuration (the
creation of the token is audited).

#### Installer bundles

The installer scripts, the templates that they render and the `myriarch-updater` binary reach a host as one archive: the
updater archive of the release, `myriarch-updater_<version>_<os>_<arch>.tar.gz` for Linux and `.zip` for Windows, the file that
a person would download from the release by hand. The server serves the archive of the release that it runs itself and no
other. It is the bytes of the release and nothing else: the server does not template, repack or re-encode them, so the hash
rules of App Control (which match byte-identical files) and later Authenticode signatures stay valid.

- **Which server has bundles.** One that runs a release of the project: the version is `MAJOR.MINOR.PATCH` or
  `MAJOR.MINOR.PATCH-rc.N`, from 0.2.0 on. A build from a Git checkout (`git describe`), a version with build metadata or any other
  text is a local build and has none: the list says `source` `none` with the reason `local_build`, the server logs it once at the
  start, and rendering commands is 412 `local_build`. A development server can serve a directory instead
  (`myriarchd dev --install-bundle-dir`, `source` `directory`): the archives in it are named like the ones of a release, the
  highest version in the directory wins, only regular files count (links are not followed), and the directory is read again each time commands
  are rendered. Nothing in it is signed, because the developer put it there; the server copies each archive into its store and
  delivers it like the others.
- **Where it comes from, and what is checked first.** The base is `install.bundleSourceBaseUrl` with the layout
  `<base>/v<version>/<asset>`, which a mirror keeps. The server loads `checksums.txt` and `checksums.txt.sigstore.json` and verifies
  the keyless signature of `checksums.txt` with the trusted root of `artifacts.verify` (a file, or TUF). The identity is
  `^<brand.Repository>/\.github/workflows/release\.yml@refs/tags/v<version>$` with the issuer `https://token.actions.githubusercontent.com`
  and the tag equal to the version: the release workflow of this project at the tag of the version this server runs, derived from
  the code and not from a setting, whatever source delivered the files. Then `checksums.txt` has to have exactly one line for the
  archive (two lines are as bad as none, also with equal hashes), and the archive has to have the SHA-256 of that line. Only then it
  is stored. The signature is checked before any archive is requested, and a failure of any step leaves the bundle `failed` and
  delivers nothing.
- **How it is fetched.** Like every outbound connection of the server: the destination policy for every resolved address
  (`outbound.allowedCidrs`), the proxy of `artifacts.proxyUrl`, redirects to https and to `artifacts.allowedRedirectHosts` (the GitHub asset
  hosts by default) and the host of the source only, 1 MiB for each of the two small files and 256 MiB for an archive, 60 seconds without
  data, one download at a time per replica. The source gets no credential: it is the public release, or a mirror that can be read without
  one.
- **Where it is kept.** Under its SHA-256 as `<artifacts.dir>/install/sha256/<hex>` (mode 0640), in the subdirectory that the eviction of the
  artifact cache leaves alone. The state is in memory: every replica keeps its own bundles, and the list shows the replica that answers. A
  restart finds the files, hashes them again and fetches only the two small documents again. Archives of an earlier version are removed
  after a day.
- **When.** Shortly after the start (15 seconds, as the artifact prefetch does), on every replica, and whenever commands are rendered or a
  bundle is asked for and a bundle is pending or failed a minute ago. A failed attempt is not repeated within a minute, so hosts that ask every
  few seconds cannot make the server go to the source in a loop. A server that is stopped stops the preparation and waits for it.

| `status` | Meaning |
|---|---|
| `pending` | Not fetched yet |
| `preparing` | The server is fetching and checking it |
| `ready` | Stored, with a pinned SHA-256 (`sha256`, `size`); delivered |
| `failed` | The last attempt failed (`error` says why); nothing is delivered; the server tries again after a minute |

**Delivery.** `GET /api/v1/install/bundles/{filename}` needs an enrollment token, with the rules of the plan: the same 401 for every
problem of a token, 429 for too many failed authentications or lookups, and the token in the header only (a request that carries it in the
URL has none). A name that is not a bundle of this server (another version, another platform, a checksum or a path) is 404; the answer repeats no
part of the path. Before every delivery the server opens the stored file without following a link and hashes it again against the pinned
SHA-256, and it sends the bytes of that open file (`Range` is supported), so a path that is replaced meanwhile changes nothing. An archive that
is gone or differs is discarded, the bundle becomes `pending` and is fetched and checked again, and the request gets 503 with `Retry-After`. The
answer has `Content-Type` `application/gzip` or `application/zip`, `Content-Disposition: attachment`, `Cache-Control: private, no-store` and
`ETag: "sha256-<hex>"`. A bundle that is not ready is 503 with `Retry-After` and the reason `bundle_preparing` (5 seconds) or
`bundle_failed` (30 seconds).

**Observability.** `myriarch_install_bundle_downloads_total{result}` counts the requests (`ok`, `disabled`, `unauthorized`, `rate_limited`,
`not_found`, `preparing`, `failed`, `unavailable`, `error`). A delivery is logged at info level with the ID of the token, the file and its
hash, not as an audit entry; the token is in no log line, label or answer.

#### Install commands

`POST /api/v1/install/commands` (`renderInstallCommands`, permission `read`, the CSRF header for a cookie session; a body of at most 16 KiB)
renders the command for each platform that is asked for. The server renders it and never sees a token. The person who copies it (the dialog
of the web UI) puts the token in.

| Request field | Meaning |
|---|---|
| `variant` | `token_inline` (the default): the token is part of the command, at the place `tokenPlaceholder` (`@@MYRIARCH_ENROLLMENT_TOKEN@@`), which the UI replaces in the browser, in every command. `token_file`: the command reads the token from a file on the host and has no place for a token. `lab`: `token_inline` over plain http with `--insecure` (`-Insecure`) for the installer; only a development server renders it (422 `variant_not_available` otherwise) |
| `platforms` | `os/arch` entries of `install.platforms`; none means all of them |
| `fleet` | The fleet that the person expects. The installer passes it to the plan, which refuses a host that the server assigns elsewhere |
| `attributes` | `site.type` and `site.id` (required: the installers stop with `option_missing` without them), and optional `site.name`, `site.country`, `site.region`, `site.timezone`, `deployment.environment.name`. The installers get them as their options |
| `collectorVersion` | A version that the host asks for; the plan accepts it inside the Git frame only |
| `tokenFiles` | `linux` and `windows`: the path of the token file on the host, required for each operating system that is rendered under `token_file` |
| `caFile` | The path of the PEM file with the CA of the server on a Linux host (`--cacert` for the download, `--ca-file` for the installer); a Windows host uses the certificate store of the machine |

The answer has the `variant`, the `server` that the commands use (`server.publicUrl`), the `version` of the bundles, `insecure`, the
`tokenPlaceholder` and for each platform the `shell` (`bash` or `powershell`), the `command`, the `bundle` that it pins (name, `sha256`, `size`)
and, under `token_file`, the `tokenFile`. A command that carries a token needs `server.publicUrl` to be https (412 `server_settings`). A server with
`opamp.requireClientCert` renders no command at all (412 `server_settings`, checked before the bundles): the plan and the download of a bundle ask for a verified client
certificate there, as the OpAMP endpoint does, and neither `curl` nor `Invoke-WebRequest` in the command presents one, so every copy would fail at the download. Hosts of
such a server are installed on the manual path ([Connect a host](../tasks/connect-a-host.md)). A bundle that is not
`ready` is 503 `bundle_preparing` or `bundle_failed` and the request starts the preparation; the commands of all platforms that are asked for
come together or not at all.

**Inputs.** The text of each command is a constant. Every value (the server, the name and SHA-256 of the bundle, the options of the installer) is an
argument of its own, quoted with single quotes, so that no value is ever pasted into code; a value is checked first, and a refusal is 422 with the issue at
its path (`invalid_value`, `unknown_attribute`, `platform_not_supported`, `variant_not_available`) and never repeats the value.

| Value | Accepted |
|---|---|
| `site.type`, `deployment.environment.name` | A letter or a digit, then letters, digits and `. _ -`, at most 63 characters |
| `site.id` | The same and `:` |
| `site.country` | The same, at most 16 characters |
| `site.name`, `site.region` | A letter or a digit, then letters, digits, spaces (not at the end) and `. , : / @ + ( ) # % = ~ -`, at most 100 characters |
| `site.timezone` | A letter, then letters, digits and `. _ + / -`, at most 64 characters |
| `fleet`, `collectorVersion` | The rules of the plan: the name of a fleet, a version such as `0.162.0` |
| Paths (`tokenFiles`, `caFile`) | An absolute path in the form of the host (`/etc/...`, `C:\...`), at most 240 characters, without `.` or `..` segments, without a space or a separator at the end |

Refused everywhere: quotation marks of every kind (`'`, `"` and the typographic ones of U+2018 to U+201E and U+00AB, U+00BB, which PowerShell reads as quotes), the backtick,
`$`, `;`, control characters, everything outside printable ASCII, `@@`, a value that looks like a token (`myre_`, `myra_`, `myrd_`), a leading dash (it could pass
for an option) and a value above its length. A second check refuses to quote a value that has a quote, so the quoting needs no escape. The check looks at the value as it came, before any normalization: `collectorVersion` and each platform are refused (422) with white space or a control
character anywhere, also at the edges. A platform may be written in upper case or with the alias of an architecture (`LINUX/x86_64`), and a version with a leading `v`; the plan
request (`getInstallPlan`) trims and folds its values the same way, because it renders nothing: the planner only compares them with what the server knows.

**What the Linux command does.** `sudo sh -c '<script>' sh <values> <<'MYRIARCH_TOKEN_END'` followed by the placeholder, for a root shell (bash or
another POSIX shell; the here document does not exist in fish). The script, in this order:

1. stops unless it is root, and unless `curl`, `tar`, `gzip`, `sha256sum` and `mktemp` exist (`privilege_required`, `tool_missing`);
2. makes a private directory with `mktemp -d` below `/root` (`directory_unsafe`) and removes it, with everything in it, when the command ends, also after an error or an interrupt;
3. reads the token with the builtin `read` from the standard input (variant `token_inline`) or from the file (variant `token_file`), and stops unless it consists of
   `A-Z a-z 0-9 _ . ~ + / = -` (`option_missing` for no token or an unreplaced placeholder, `option_invalid`, `file_unreadable`); under `token_inline` the builtin `printf`
   writes it to a file of the private directory (mode 0600);
4. downloads the bundle with `curl -fsSg --retry 3 --proto =https --tlsv1.2 -K -`, to which `printf` gives the header `Authorization: Bearer <token>` on the standard
   input (`--proto =http,https` for the lab variant): the token is in no argument vector and no environment variable, the URL is built from the server and the name
   of the bundle only, and redirects are not followed (`download_failed`);
5. checks the SHA-256 that the command pins with `sha256sum -c` **before it unpacks anything** (`checksum_mismatch`), unpacks the archive in the private directory
   (`archive_invalid`) and starts `bash deploy/scripts/install-linux.sh --server <server> --token-file <file> [--ca-file ...] <options>` from there, with no standard
   input; the exit status of the installer is the status of the command.

**What the Windows command does.** A script block `& { param([hashtable]$P) ... } @{ ... }` for an elevated Windows PowerShell or a session of SYSTEM, which is
the constant script followed by the values in a hashtable. It uses cmdlets and native programs only (`whoami.exe`, `icacls.exe`, `powershell.exe`) and no
.NET method or type, because under App Control the typed command runs in Constrained Language Mode. It, in this order:

1. stops unless the session is elevated or runs as SYSTEM (`privilege_required`, from the mandatory integrity level that `whoami.exe /groups` lists: High,
   `S-1-16-12288`, is an elevated administrator; System, `S-1-16-16384`, is a session of SYSTEM, the context of scheduled tasks, software distribution and management
   agents, which run the variant `token_file`; an administrator who is not elevated has Medium and is refused, although the group Administrators is in the token "for deny
   only") and unless Group Policy leaves room for a script: a policy of the scopes `MachinePolicy` or `UserPolicy` that is `AllSigned` or `Restricted` wins over `-ExecutionPolicy`
   and would stop the unsigned installer later, so the command says so first (`execution_policy_blocked`); then it checks the token as above;
2. makes the working directory directly below `%ProgramFiles%` with a name that cannot be guessed. Standard users and processes without elevation cannot write there, so no handle
   with write rights to the new directory (which could set a reparse point) can exist. Users may read there, so the command replaces its DACL with
   `icacls.exe /inheritance:r /grant:r *S-1-5-18:(OI)(CI)F *S-1-5-32-544:(OI)(CI)F` (SYSTEM and the administrators) **before the first download** (`acl_failed`) and makes the
   administrators its owner (`icacls.exe /setowner *S-1-5-32-544`, also `acl_failed`). The directory is removed in a `finally`. This sequence can be checked on real Windows only
   (the `host-e2e` workflow); the tests of the command cover its structure and its order;
3. writes the token to a file of the working directory with `Set-Content` (variant `token_inline`) and downloads the bundle with `Invoke-WebRequest -Headers`, without following a redirect
   (`download_failed`); .NET has to negotiate TLS 1.2, which Windows 10 and Windows Server 2019 and newer do by default;
4. checks the SHA-256 with `Get-FileHash` before it unpacks (`checksum_mismatch`), unpacks with `Expand-Archive`, runs `Unblock-File` on the verified script and starts it
   as `powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File install-windows.ps1 -Server ... -TokenFile ...`: the policy is set for that process only, never
   `Bypass`, never for the machine or the user (`installer_failed` with the exit code when it fails).

The codes are those of the installers ([Connect a host](../tasks/connect-a-host.md)); `execution_policy_blocked` and `installer_failed` are new and belong to the command.

**What the command cannot hide.** Under `token_inline` the token is text of the command, and what records typed text keeps it: the clipboard, the history of a shell, the
I/O log of `sudo` (`log_input`), the history file of PSReadLine, PowerShell transcription and script block logging (event 4104), the recording of a terminal session. A token that such a record kept counts as
leaked. The variant `token_file` keeps the token out of the command, and fits production, automation and every host that records its input. The first installation
trusts the server that displayed the command (trust on first use): the pinned hash shows that the host received the bundle that the server vouched for after it verified
`checksums.txt`, not that the server is honest. The manual path of [Connect a host](../tasks/connect-a-host.md) stays for sites that cannot accept this.
