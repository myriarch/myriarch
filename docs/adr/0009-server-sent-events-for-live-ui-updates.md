# 0009. Server-sent events for live UI updates

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0001](0001-single-server-binary.md), [ADR-0010](0010-standard-servemux-and-hand-written-dtos.md)

## Context

The web UI shows agents, rollouts and releases that change while the page is open. Operators who start a rollout expect to watch it progress. The data flows from the server to the browser; the browser sends its commands through the REST API.

The server may sit behind proxies and ingress controllers. Authentication for the UI uses a session cookie.

## Decision

The server pushes live updates to the browser as server-sent events (SSE) over a normal HTTP response, not over a WebSocket.

- Each replica serves its own streams and subscribes to the cluster event bus ([ADR-0002](0002-postgresql-for-state-event-bus-and-leader-election.md)), so a stream shows events caused on any replica.
- The browser's `EventSource` authenticates with the session cookie.
- Updates are coalesced (about one per second) and the server sends a comment line as ping every 15 seconds.
- A stream re-validates its credential on every ping and after every invalidation event, ends after one hour at the latest (the browser reconnects), and is limited to 10 streams per user or token. An event type is delivered only to principals that hold the matching permission.

## Consequences

### Positive

- One-directional flow is all that is needed, and SSE is simpler than a WebSocket protocol.
- Works through proxies that handle plain HTTP responses, and browsers reconnect automatically.
- Cookie authentication works with `EventSource` without extra tokens in the URL.

### Negative

- Each open UI tab holds one long-lived connection on a replica.
- Servers and proxies in front of them must not buffer or time out streaming responses too early.
- Data in the other direction cannot use the stream; it goes through REST.

## Alternatives considered

- **WebSocket for live updates**: two-way communication is not needed here, and a WebSocket is harder to run behind proxies.
