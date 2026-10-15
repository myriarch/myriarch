# 0036. Database schema compatibility and rollback

- Status: Accepted
- Date: 2026-10-15
- Related: [ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md), [ADR-0011](0011-no-containers-for-local-development-and-tests.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md)

## Context

[ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md) makes PostgreSQL the only stateful dependency and lets the
server create the schema with embedded migrations at start (`database.migrateOnStart`, under an advisory lock). In v0.1.0
nothing checks whether a schema fits the binary beyond "the schema is not older":

- The migration library accepts a database that has migrations the binary does not know. A binary of an older release therefore
  starts on a newer schema without a message, for example after `helm rollback`, or when Kubernetes restarts a pod of the old
  ReplicaSet during a rolling update.
- Whether that works depends on the migration. One that only adds objects (a table, a nullable column, an index) leaves the
  older binary working. One that drops, renames or tightens something the older binary uses makes it fail in unrelated places,
  long after the start.
- With `database.migrateOnStart` off, a schema that is too old does not stop the start either: the server runs until the first
  component reads a missing column.
- `myriarchd migrate` loads and validates the whole server configuration. A job that migrates the database before the server is
  updated therefore needs the public URL, a login path and the secret files of features it does not use.

Operators on Kubernetes expect `helm rollback` to be safe after an upgrade whose migrations were additive, and a clear refusal
otherwise. v0.1.0 (schema level 5) is released and reads no compatibility information.

## Decision

1. **The database records a compatibility level.** Migration 00006 creates the table `schema_compat` with exactly one row. Its
   column `min_binary_schema` is the lowest schema level that a binary must have to run on the database. The schema level of a
   binary is the highest migration number it embeds. The level never exceeds the highest applied migration. It starts at 3, the
   level of every database that reaches 00006: 00003 is the last migration that earlier binaries cannot work with.
2. **Every migration states its effect.** From 00006 on, the header of a migration says what a replica of the previous version
   does on the changed schema, and whether the compatibility level stays (expand only) or is raised to a migration number. A
   migration that raises the level sets the value in its Up section and restores the previous value in its Down section. A test
   checks the headers and applies every migration up and down against them.
3. **The start refuses only a schema that is incompatible with the binary.**
   - If the compatibility level is higher than the schema level of the binary, the start is refused before anything is changed,
     whatever `database.migrateOnStart` says. The error names the schema version, the compatibility level and the schema level of
     the binary, and the two ways out: run a newer release, or restore a backup of the database from before the upgrade.
   - If the schema lacks migrations of the binary, they are applied when `database.migrateOnStart` is true, and the start is
     refused at once otherwise. The error names the setting and the command `myriarchd migrate`.
   - If the schema is newer than the binary but not above its level, the server logs a warning and starts.
4. **`database.migrateOnStart` stays on by default.** The server and `myriarchd migrate` use the same step. `myriarchd migrate`
   needs only the database settings, prints the schema version before and after, and with `--check` changes nothing and tells
   the state by its exit code. A job can therefore apply the migrations before a deployment is updated.
5. **A rollback to v0.1.0 is not supported.** v0.1.0 does not read the compatibility level, and its migration library accepts a
   database with higher versions, so it starts on any later schema without a check. We do not test or support running v0.1.0
   against a database that a later release has migrated. The upgrade notes tell users to back up the database before they
   upgrade; going back to v0.1.0 means restoring that backup. Between releases that contain this mechanism (v0.2.0 and later)
   a rollback is possible as far as the level allows: it works after migrations that left the level alone, and it is refused
   after the others.

## Consequences

### Positive

- A binary that cannot work with a schema says so at the start, with the numbers and the way out, instead of failing later.
- `helm rollback` and the restart of an old pod stay possible after additive migrations.
- `database.migrateOnStart` stays the default, so evaluators need no extra step.
- A migration job needs no server configuration.
- A new migration cannot skip the question: the header must state its effect, and a test compares the statement with the effect.

### Negative

- "Expand only" is a claim of the author. The test checks that header and effect agree; it cannot prove that an older binary
  works on the new schema. Review has to judge that.
- The check runs at start. Replicas that already run are not stopped when another replica raises the level. A migration that
  older replicas cannot work with still needs the documented procedure for the upgrade (one replica, or a maintenance window).
- Users of v0.1.0 can go back only by a restore, and v0.1.0 gives no warning when it is started against a migrated database.
- One more table, a header convention and a test to maintain.

## Alternatives considered

- **Refuse every schema that is newer than the binary.** Simple, but it breaks `helm rollback` after any migration, and it
  refuses the restart of an old pod during a rolling update although the schema would have worked.
- **Only warn, never refuse.** An older binary would run on a schema it cannot work with, which is the failure this record
  exists to prevent.
- **Decide from the migration numbers alone,** for example by a fixed window of versions. Numbers cannot say which migration is
  additive and which is not.
- **Keep the level in the key-value store.** The level has to change in the same transaction as the migration that needs it,
  must be readable before any store is built, and should be visible to `psql` and backups as a plain table with a check. The
  key-value store is written by application code.
- **Support the rollback to v0.1.0 with a patch release that reads the level.** It would protect only installations that update
  to the patch before they upgrade. v0.1.0 is the only release and the project is in alpha, so the second branch is not worth
  its cost.
