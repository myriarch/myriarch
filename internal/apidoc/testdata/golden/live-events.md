---
title: "Live events"
weight: 20
description: "Server-Sent Events."
---

All paths are relative to the base path `/api/v1`. Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Live updates as Server-Sent Events](#live-updates-as-server-sent-events) | `GET /events` | `read` (viewer) |

## Live updates as Server-Sent Events

`GET /api/v1/events`

Operation `streamEvents`. Permission `read` (lowest role: viewer).

`text/event-stream`. Each message has `event: <type>` and `data: <JSON>`:

| event | data schema |
|---|---|
| `item.updated` | `Item` |
| `resync` | `{}` - refetch all views |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Event stream | `text/event-stream` | `string` |
