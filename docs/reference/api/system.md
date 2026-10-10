---
title: "System"
weight: 70
description: "System information and effective settings."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Version, replicas, leader, database, features and effective non-secret settings](#version-replicas-leader-database-features-and-effective-non-secret-settings) | `GET /system/info` | `read` (viewer) |

## Version, replicas, leader, database, features and effective non-secret settings

`GET /api/v1/system/info`

Operation `getSystemInfo`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | System info | `application/json` | [SystemInfo](data-types.md#systeminfo) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
