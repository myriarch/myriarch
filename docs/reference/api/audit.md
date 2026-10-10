---
title: "Audit"
weight: 80
description: "Audit log."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Audit log (newest first)](#audit-log-newest-first) | `GET /audit` | `audit:read` (operator) |

## Audit log (newest first)

`GET /api/v1/audit`

Operation `listAuditEvents`. Permission `audit:read` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `actor` | query | `string` | no | Actor id or display name (exact match; the name case-insensitively). |
| `action` | query | `string` | no | Exact action or prefix with "*" (e.g. "rollout.*"). |
| `targetType` | query | `string` | no |  |
| `targetId` | query | `string` | no |  |
| `from` | query | `string` | no | Format `date-time`. |
| `to` | query | `string` | no | Format `date-time`. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Audit events | `application/json` | [AuditEventPage](data-types.md#auditeventpage) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 403 | Missing permission (code forbidden) or CSRF failure (code csrf_failed) | `application/problem+json` | [Problem](data-types.md#problem) |
