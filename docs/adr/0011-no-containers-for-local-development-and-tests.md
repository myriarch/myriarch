# 0011. No containers for local development and tests

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md), [ADR-0001](0001-single-server-binary.md)

## Context

The server needs PostgreSQL ([ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md)). A contributor should be able to build, run and test the whole system without installing a container runtime or a database server. Many tests need a real database, because the behavior depends on `LISTEN`/`NOTIFY`, advisory locks and generated columns.

## Decision

Local development and tests run on an embedded PostgreSQL ([embedded-postgres](https://github.com/fergusstrange/embedded-postgres)), not in containers.

- `myriarchd dev` starts everything in one process, including the embedded PostgreSQL. The embedded server is linked only when the build tag `devpg` is set; release builds deliberately leave it out.
- Tests that need a database use the helper `internal/testutil/pgtest`. It starts one embedded server per test binary and gives every test an isolated database cloned from a migrated template. It addresses known pitfalls of the library: a random free port per process, a private runtime path per process, a cross-process lock around the extraction of binaries, TCP only, locale `C`, and a watchdog that removes the server if the test binary dies.
- With `PGTEST_DSN` set, the tests use an external PostgreSQL server instead (for example in CI).

The Kubernetes probe under `hack/kind` is a separate, optional check of the Helm chart and does not belong to the development loop.

## Consequences

### Positive

- `make dev` and `go test ./...` need no Docker or other container runtime.
- Tests run against real PostgreSQL behavior, not a mock.
- Tests are isolated from each other and from a locally installed PostgreSQL.

### Negative

- The first start downloads and unpacks the PostgreSQL binaries (about ten seconds when cold; under a second when warm).
- The helper carries platform-specific code (locking, watchdog) that has to be maintained.
- The embedded server cannot run as root.

## Alternatives considered

- **Containers (Docker or Compose) for the database during development and tests**: rejected by this decision to avoid a container runtime as a prerequisite.
