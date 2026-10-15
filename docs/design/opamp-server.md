---
title: "Server behaviour"
weight: 20
description: "Normative server behaviour of myriarchd, grouped by component."
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
  (the database settings only, see [Start, migrations and schema compatibility](#start-migrations-and-schema-compatibility)).

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

A session advisory lock on a dedicated connection decides the leader. A candidate tries the lock every five seconds.
The leader checks its connection every five seconds with a short timeout and gives up leadership if the check fails.
The lock is released automatically when the session ends, so a killed leader is replaced within about 15 seconds.

Leader-only jobs: GitOps polling, the rollout planner and engine, the release tracker, artifact prefetch, the offline
sweeper, housekeeping and the periodic gauges. Everything else runs on every replica.

## Authentication and authorization

- Tokens. API tokens start with `myra_`, enrollment tokens with `myre_` and download tokens with `myrd_`. A token is 32
  random base62 characters; only the SHA-256 is stored, together with a display prefix. Lookups are by hash. Tokens can
  be revoked or expire, and a token created by another token is capped by the creator's role and expiry.
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
    and `config_failed`.
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
  seconds on one connection. The message carries exactly one entry, key `myriarch.yaml`, content type `text/yaml`, and
  the hash. An empty configuration map is never sent, because the supervisor would start its no-op pipeline.
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
  desired state, failed marker or offers must call `DesiredStateChanged`.

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
- Graceful shutdown uses `server.shutdownTimeout`.

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
