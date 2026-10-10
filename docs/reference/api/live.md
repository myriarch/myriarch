---
title: "Live updates"
weight: 90
description: "Server-Sent Events for live UI updates."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Live updates as Server-Sent Events](#live-updates-as-server-sent-events) | `GET /events/stream` | `read` (viewer) |

## Live updates as Server-Sent Events

`GET /api/v1/events/stream`

Operation `streamEvents`. Permission `read` (lowest role: viewer).

`text/event-stream`. Each message has `event: <type>`, `id: <monotonic id>` and
`data: <JSON>`:

| event | data schema |
|---|---|
| `agent.updated` | `AgentSummary` (coalesced, max 1/s per agent) |
| `agent.removed` | `LiveAgentRemovedData` |
| `rollout.updated` | `RolloutSummary` |
| `fleet.updated` | `LiveFleetUpdatedData` - the effective target version or the UI version selection of a fleet changed (select, clear, superseded/invalidated by Git): refetch fleet views |
| `gitops.synced` | `GitOpsBrief` |
| `release.new` | `ReleaseSummary` |
| `stats.changed` | `{}` - hint to refetch the dashboard (max 1 per 5 s) |
| `resync` | `{}` - client fell behind or the server lost events: refetch all views |

A comment line `: ping` is sent every 15 s. Clients use `EventSource` (cookie auth) and
reconnect automatically; after a reconnect they should refetch visible data
(no replay; `Last-Event-ID` is ignored).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Event stream | `text/event-stream` | `string` |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
