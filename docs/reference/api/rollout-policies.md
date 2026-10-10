---
title: "Rollout policies"
weight: 120
description: "Rollout policies from GitOps (read-only)."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [All rollout policies (including the built-in "default" if not overridden)](#all-rollout-policies-including-the-built-in-default-if-not-overridden) | `GET /rollout-policies` | `read` (viewer) |
| [One rollout policy](#one-rollout-policy) | `GET /rollout-policies/{name}` | `read` (viewer) |

## All rollout policies (including the built-in "default" if not overridden)

`GET /api/v1/rollout-policies`

Operation `listRolloutPolicies`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Policies | `application/json` | [RolloutPolicyList](data-types.md#rolloutpolicylist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## One rollout policy

`GET /api/v1/rollout-policies/{name}`

Operation `getRolloutPolicy`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Policy | `application/json` | [RolloutPolicy](data-types.md#rolloutpolicy) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
