# 0010. Standard ServeMux and hand-written DTOs with a contract test

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0001](0001-single-server-binary.md), [ADR-0009](0009-server-sent-events-for-live-ui-updates.md)

## Context

The REST API is described by `api/openapi.yaml` (OpenAPI 3.1), which is the authoritative contract. The server is written in Go and the web UI in TypeScript.

Since Go 1.22 the standard `net/http.ServeMux` matches methods and path parameters in its patterns. Go code generators for OpenAPI support version 3.1 only partly. The TypeScript generator `openapi-typescript` supports 3.1.

## Decision

- Routing uses the standard library `ServeMux`. Each domain package supplies `api.Module` values with `api.Route` entries (method, pattern, operation ID, permission, authentication mode, handler). `internal/httpserver` mounts all modules under `/api/v1` and enforces authentication, CSRF and permission centrally, so no package edits a shared router file.
- Request and response types (DTOs) are written by hand in `internal/api/dto`. A contract test (`internal/api/dto/contract_test.go`) checks that Go types and OpenAPI schemas match field by field.
- The UI generates its TypeScript types from `api/openapi.yaml` with `openapi-typescript`.

## Consequences

### Positive

- Fewer dependencies and no generated Go code to review.
- The access decision of every route is declared next to the route, and a test enumerates all patterns.
- A drift between code and contract fails a test.

### Negative

- DTOs and schemas are maintained by hand on both sides; the contract test is the safety net.
- No generated Go server stubs or clients.

## Alternatives considered

- **A router library**: adds a dependency for functions the standard library now provides.
- **Go code generators from the OpenAPI document**: support for OpenAPI 3.1 is only partial.
