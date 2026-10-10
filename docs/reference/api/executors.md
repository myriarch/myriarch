---
title: "Executors"
weight: 130
description: "Update executor definitions from GitOps (read-only)."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Executor definitions (webhook/AWX) without secrets](#executor-definitions-webhookawx-without-secrets) | `GET /executors` | `read` (viewer) |

## Executor definitions (webhook/AWX) without secrets

`GET /api/v1/executors`

Operation `listExecutors`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Executors | `application/json` | [ExecutorSummaryList](data-types.md#executorsummarylist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
