---
title: "Stats"
weight: 60
description: "Dashboard aggregates."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Dashboard aggregates](#dashboard-aggregates) | `GET /stats/overview` | `read` (viewer) |

## Dashboard aggregates

`GET /api/v1/stats/overview`

Operation `getOverviewStats`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Overview | `application/json` | [OverviewStats](data-types.md#overviewstats) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
