---
title: "REST API"
weight: 30
description: "The OpenAPI 3.1 contract of the management API under /api/v1: authentication, roles, error format and every operation with its permission."
---

Generated from `api/openapi.yaml` (Myriarch REST API, contract version `0.1.0`). Management API of myriarchd (OpAMP based OpenTelemetry Collector fleet management).

REST API used by the myriarch web UI, myrctl and automation.

## Conventions

- Base path `/api/v1`. JSON (UTF-8) request and response bodies; field names are camelCase.
- Timestamps are RFC 3339 in UTC. Durations are Go duration strings (`"30s"`, `"5m"`).
- Hashes are lower-case hex strings (SHA-256 unless stated otherwise).
- Collector versions are given WITHOUT a leading `v` (`"0.162.0"`), except package offers.
- Maps and arrays marked as required are never `null` (empty `{}` / `[]`).
- Errors use RFC 9457 problem details (`application/problem+json`, schema `Problem`) with a
  stable machine readable `code`.
- Paginated lists: `limit` (1..500, default 50) and `offset` (0..100000) query parameters; response
  `{items, total, limit, offset}`. Unpaginated lists: `{items}`.
- Sorting: `sort=<field>` ascending, `sort=-<field>` descending.

## Authentication

- Browser: server-side session cookie `myriarch_session` (OIDC login or dev login); with Secure
  cookies (the production setting) the cookie is named `__Host-myriarch_session`. Mutating
  requests (POST/PUT/PATCH/DELETE) authenticated by the cookie MUST send the header
  `X-CSRF-Token` with the value of the `myriarch_csrf` cookie (also returned by `GET /auth/me`).
- Automation: `Authorization: Bearer myra_...` (API token with a fixed role, never higher than the
  role of its creator; it stops working when the creator is disabled and after the maximum token
  lifetime `auth.apiTokenMaxTtl`, default 90 days). No CSRF header needed.
- Request bodies must be `application/json` (other content types: 415). Sessions have an idle
  timeout and an absolute maximum age. Repeated failed logins or invalid bearer tokens are rate
  limited per client address (429 with `Retry-After`).
- Roles: `viewer` (read), `operator` (viewer + operational actions), `admin` (operator + users,
  API tokens, enrollment tokens). Each operation lists its permission in `x-permission`.

### Security schemes

| Scheme | Type | Description |
|---|---|---|
| `sessionCookie` | API key in the cookie `myriarch_session` | Browser session. With Secure cookies (the production setting) the cookie is named `__Host-myriarch_session`. Mutating requests also need header X-CSRF-Token (= cookie myriarch_csrf). |
| `bearerToken` | HTTP `bearer` authentication | API token "myra_..." created via POST /tokens. |

## Roles and permissions

Every operation names the permission that it needs. The table shows the lowest role that holds each permission; a role also holds the permissions of the roles below it. It is derived from the authorization code of the server, so it cannot drift from it.

| Permission | Lowest role | Operations |
|---|---|---|
| `read` | viewer | 38 |
| `saved_filters:write` | viewer | 2 |
| `agents:delete` | operator | 1 |
| `agents:operate` | operator | 2 |
| `artifacts:manage` | operator | 2 |
| `audit:read` | operator | 1 |
| `fleets:set_version` | operator | 2 |
| `gitops:sync` | operator | 1 |
| `notifications:test` | operator | 1 |
| `releases:acknowledge` | operator | 2 |
| `rollouts:operate` | operator | 6 |
| `enrollment:manage` | admin | 4 |
| `tokens:manage` | admin | 3 |
| `users:manage` | admin | 2 |
| `public` | none, no login | 5 |
| `custom` | none, the operation checks its own credential | 4 |

## Errors

Errors are RFC 9457 problem details with the content type `application/problem+json` and the type [Problem](data-types.md#problem). The member `code` is a stable, machine-readable error code.

| Field | Type | Description |
|---|---|---|
| `type` (required) | `string` | URI reference ("about:blank"). |
| `title` (required) | `string` |  |
| `status` (required) | `integer` |  |
| `detail` | `string` |  |
| `instance` | `string` |  |
| `code` (required) | `string` | One of `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `precondition_failed`, `validation_failed`, `too_many_requests`, `internal`, `not_implemented`, `unavailable`, `csrf_failed`. |
| `requestId` | `string` |  |
| `issues` | array of [Issue](data-types.md#issue) |  |

The codes: `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `precondition_failed`, `validation_failed`, `too_many_requests`, `internal`, `not_implemented`, `unavailable`, `csrf_failed`.

The responses that many operations share:

| Status | Meaning |
|---|---|
| 400 | Invalid request (code bad_request) |
| 401 | Not authenticated (code unauthorized) |
| 403 | Missing permission (code forbidden) or CSRF failure (code csrf_failed) |
| 404 | Not found (code not_found) |
| 409 | Conflict (code conflict) |
| 412 | Invalid state transition or precondition (code precondition_failed) |
| 422 | Validation failed (code validation_failed, details in issues) |

Example:

```json
{
  "type": "string",
  "title": "string",
  "status": 0,
  "detail": "string",
  "instance": "string",
  "code": "bad_request",
  "requestId": "string",
  "issues": [
    {
      "severity": "error",
      "code": "unknown_component",
      "message": "string",
      "file": "string",
      "line": 0,
      "path": "service::pipelines::logs",
      "object": "Fleet/edge-nl"
    }
  ]
}
```

## Live updates

`GET /events/stream` (Server-Sent Events), see operation `streamEvents`.

## Non-REST endpoints

Not part of this document: OpAMP `/v1/opamp` (WebSocket + HTTP,
agents authenticate with `Authorization: Bearer myre_...` enrollment tokens), `/healthz`
(liveness), `/readyz` (readiness), `/metrics` (Prometheus, by default on the separate
metrics listener), `/licenses` (`LICENSE` and `THIRD_PARTY_NOTICES` as `text/plain`, no login).

## Operations by tag

| Tag | Description | Operations |
|---|---|---|
| [Authentication](auth.md) | Login, logout, current principal | 7 |
| [Users](users.md) | Users and roles | 2 |
| [Tokens](tokens.md) | API tokens and agent enrollment tokens | 6 |
| [Agents](agents.md) | OpAMP agents (collectors and myriarch-updaters) | 11 |
| [Saved filters](saved-filters.md) | Saved agent list filters | 3 |
| [Stats](stats.md) | Dashboard aggregates | 1 |
| [System](system.md) | System information and effective settings | 1 |
| [Audit](audit.md) | Audit log | 1 |
| [Live updates](live.md) | Server-Sent Events for live UI updates | 1 |
| [Fleets](fleets.md) | Fleets from GitOps (read-only), fleet matching, UI version selection within the Git frame | 6 |
| [Blueprints](blueprints.md) | Blueprints from GitOps (read-only), rendering preview | 3 |
| [Rollout policies](rollout-policies.md) | Rollout policies from GitOps (read-only) | 2 |
| [Executors](executors.md) | Update executor definitions from GitOps (read-only) | 1 |
| [Rollouts](rollouts.md) | Config and version rollouts, operator actions | 9 |
| [Releases](releases.md) | Upstream collector releases, breaking changes | 6 |
| [GitOps](gitops.md) | GitOps sync status, history, manual sync, webhook | 5 |
| [Artifacts](artifacts.md) | Artifact cache for collector binaries | 5 |
| [Notifications](notifications.md) | Notification channels | 2 |
| [Install](install.md) | Installation of hosts from the server - plan, install grants, installer bundles and rendered commands (available from v0.2.0) | 4 |

The schemas that the operations use are on the page [Data types](data-types.md).

## About the contract

| Item | Value |
|---|---|
| License | Apache License 2.0, SPDX `Apache-2.0` |
| Contact | [The Myriarch Authors](https://myriarch.dev) |
