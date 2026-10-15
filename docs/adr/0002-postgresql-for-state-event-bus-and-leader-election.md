# 0002. PostgreSQL for state, event bus and leader election

- Status: Accepted, refined by [ADR-0036](0036-database-schema-compatibility-and-rollback.md)
- Date: 2026-10-07
- Related: [ADR-0001](0001-single-server-binary.md), [ADR-0011](0011-no-containers-for-local-development-and-tests.md)

## Context

With several equal replicas ([ADR-0001](0001-single-server-binary.md)) the system needs three things beyond ordinary storage:

- durable, shared state (agents, rendered configurations, rollouts, audit events, tokens);
- a way to tell the replica that holds an agent's connection to push a change that another replica decided on;
- a way to run singleton work (GitOps sync, rollout engine, release tracker) on exactly one replica at a time.

A relational database is needed for the state anyway. Every additional infrastructure component raises the cost of running the system.

## Decision

PostgreSQL is the only stateful dependency and also provides the other two functions.

- **State**: all durable state lives in PostgreSQL (accessed with pgx). Replicas keep only connections and caches. The schema is created by embedded migrations that run at start under an advisory lock (`database.migrateOnStart`).
- **Event bus**: cluster-wide events use `LISTEN`/`NOTIFY` on the channel `myriarch_events`. Delivery is best effort. This is acceptable because the OpAMP server reconciles the desired state on every agent message, and the engine also runs on a timer.
- **Leader election**: a session-level advisory lock (`pg_try_advisory_lock`, key `ha.leaderLockKey`, default 7012024) on a dedicated connection. If the leader loses that session, the leadership ends at once and another replica takes over.

## Consequences

### Positive

- No Redis, Kafka or separate lock service to deploy, secure and monitor.
- State changes and the events about them come from one system, which keeps the failure model small.
- The same database serves development and tests ([ADR-0011](0011-no-containers-for-local-development-and-tests.md)).

### Negative

- A connection pooler in transaction mode breaks both `LISTEN`/`NOTIFY` and session-level advisory locks. Operators connect directly or use a pooler in session mode.
- `NOTIFY` payloads are limited to 8,000 bytes and delivery is not guaranteed, so the bus carries identifiers and the code treats it as a hint, never as the only source of truth.
- PostgreSQL is a single point of failure for the control plane. High availability of the database is the operator's responsibility (managed service or a replicated cluster).
- `LISTEN`/`NOTIFY` is suited to a small number of replicas, which matches the design target.

## Alternatives considered

- **Redis or Kafka for the bus, a separate lock service for the leader**: more components for no gain at a few replicas.
