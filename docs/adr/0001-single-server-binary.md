# 0001. A single server binary carries all server functions

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md), [ADR-0010](0010-standard-servemux-and-hand-written-dtos.md), [ADR-0011](0011-no-containers-for-local-development-and-tests.md), [ADR-0015](0015-project-name-and-naming-scheme.md)

## Context

The system has to serve three kinds of clients at the same time: OpAMP agents over WebSocket, people and automation over the REST API, and browsers that load the web UI and keep a live event stream open. Behind them sit background work (GitOps sync, rollout engine, release tracker, artifact cache, notifications) and a database.

The design target is 1,000 to 5,000 collectors plus their updaters (see [Scaling](../architecture.md#scaling)). Operators run the server through a Helm chart, Docker Compose or a single process on a laptop. Every extra moving part is an additional thing to deploy, secure, upgrade and debug.

## Decision

We ship one server binary, `myriarchd`, that contains all server functions: the OpAMP endpoint, the REST API, the live event stream, the embedded web UI, the GitOps syncer, the rollout engine, the release tracker, the artifact cache and the notifier.

- UI, REST, live events and OpAMP share one HTTP listener (port 8080 by default). Metrics are served on a separate listener (port 9090 by default) so that network policy can treat them differently.
- All replicas are equal and run the same code. Work that must happen exactly once (for example the rollout engine) runs only on the replica that holds the leader lock ([ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md)).
- The same binary offers the development mode (`myriarchd dev`) that starts everything in one process ([ADR-0011](0011-no-containers-for-local-development-and-tests.md)).

## Consequences

### Positive

- One artifact to build, sign, deploy and upgrade; the Helm chart, the Compose file and the development mode all start the same program.
- No internal service-to-service API and no network hops between the functions; they call each other in process.
- One listener and one set of authentication, rate limit and audit rules for every client type.

### Negative

- All functions scale together. A burst of artifact downloads or UI sessions competes with OpAMP connections for the resources of the same process.
- A defect in one function can affect the whole process; the background jobs are therefore supervised and restarted individually, and they carry time budgets.
- Leader-only jobs run inside serving replicas, so a leader that is busy serving also runs the engine.

## Alternatives considered

- **Separate services** (OpAMP gateway, API, engine, UI): more operational effort and more interfaces to secure, without a benefit at the target size of a few thousand agents.
