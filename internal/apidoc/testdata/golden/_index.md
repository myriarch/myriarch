---
title: "REST API"
weight: 30
description: "The OpenAPI 3.1 contract of the management API under /api/v1: authentication, roles, error format and every operation with its permission."
---

Generated from `api/openapi.yaml` (Mini API, contract version `0.1.0`).

A small contract for the tests of the generator.

## Conventions

- Base path `/api/v1`.
- Sorting: `sort=<field>` or `sort=a|b`.

## Authentication

- Browser: session cookie.
- Automation: bearer token.

### Security schemes

| Scheme | Type | Description |
|---|---|---|
| `sessionCookie` | API key in the cookie `mini_session` | Browser session. |
| `bearerToken` | HTTP `bearer` authentication | API token. |

## Roles and permissions

Every operation names the permission that it needs. The table shows the lowest role that holds each permission; a role also holds the permissions of the roles below it. It is derived from the authorization code of the server, so it cannot drift from it.

| Permission | Lowest role | Operations |
|---|---|---|
| `read` | viewer | 2 |
| `items:write` | operator | 2 |
| `public` | none, no login | 1 |
| `custom` | none, the operation checks its own credential | 1 |

## Errors

Errors are RFC 9457 problem details with the content type `application/problem+json` and the type [Problem](data-types.md#problem). The member `code` is a stable, machine-readable error code.

| Field | Type | Description |
|---|---|---|
| `title` (required) | `string` |  |
| `status` (required) | `integer` |  |
| `code` (required) | `string` | One of `unauthorized`, `validation_failed`. |

The codes: `unauthorized`, `validation_failed`.

The responses that many operations share:

| Status | Meaning |
|---|---|
| 401, 422 | Not authenticated (code unauthorized) |

Example:

```json
{
  "title": "string",
  "status": 0,
  "code": "unauthorized"
}
```

## Live updates

`GET /events` (Server-Sent Events).

## Non-REST endpoints

Not part of this document: `/healthz`.

## Operations by tag

| Tag | Description | Operations |
|---|---|---|
| [Items](items.md) | Items and their parts | 5 |
| [Live events](live-events.md) | Server-Sent Events | 1 |

The schemas that the operations use are on the page [Data types](data-types.md).
