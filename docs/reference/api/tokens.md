---
title: "Tokens"
weight: 30
description: "API tokens and agent enrollment tokens."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [List API tokens (without secrets)](#list-api-tokens-without-secrets) | `GET /tokens` | `tokens:manage` (admin) |
| [Create an API token (the secret is returned only once)](#create-an-api-token-the-secret-is-returned-only-once) | `POST /tokens` | `tokens:manage` (admin) |
| [Revoke an API token](#revoke-an-api-token) | `DELETE /tokens/{id}` | `tokens:manage` (admin) |
| [List agent enrollment tokens (without secrets)](#list-agent-enrollment-tokens-without-secrets) | `GET /enrollment-tokens` | `enrollment:manage` (admin) |
| [Create an enrollment token for supervisors/myriarch-updaters (secret returned once)](#create-an-enrollment-token-for-supervisorsmyriarch-updaters-secret-returned-once) | `POST /enrollment-tokens` | `enrollment:manage` (admin) |
| [Revoke an enrollment token (connected agents are not disconnected; reconnects fail)](#revoke-an-enrollment-token-connected-agents-are-not-disconnected-reconnects-fail) | `DELETE /enrollment-tokens/{id}` | `enrollment:manage` (admin) |

## List API tokens (without secrets)

`GET /api/v1/tokens`

Operation `listApiTokens`. Permission `tokens:manage` (lowest role: admin).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | API tokens | `application/json` | [APITokenList](data-types.md#apitokenlist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Create an API token (the secret is returned only once)

`POST /api/v1/tokens`

Operation `createApiToken`. Permission `tokens:manage` (lowest role: admin).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [CreateAPITokenRequest](data-types.md#createapitokenrequest).

```json
{
  "name": "string",
  "role": "viewer",
  "expiresAt": "2026-01-01T00:00:00Z"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 201 | Created | `application/json` | [CreatedAPIToken](data-types.md#createdapitoken) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |

## Revoke an API token

`DELETE /api/v1/tokens/{id}`

Operation `revokeApiToken`. Permission `tokens:manage` (lowest role: admin).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 204 | Revoked | - | - |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## List agent enrollment tokens (without secrets)

`GET /api/v1/enrollment-tokens`

Operation `listEnrollmentTokens`. Permission `enrollment:manage` (lowest role: admin).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Enrollment tokens | `application/json` | [EnrollmentTokenList](data-types.md#enrollmenttokenlist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Create an enrollment token for supervisors/myriarch-updaters (secret returned once)

`POST /api/v1/enrollment-tokens`

Operation `createEnrollmentToken`. Permission `enrollment:manage` (lowest role: admin).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [CreateEnrollmentTokenRequest](data-types.md#createenrollmenttokenrequest).

```json
{
  "name": "string",
  "labels": {
    "key": "string"
  },
  "expiresAt": "2026-01-01T00:00:00Z"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 201 | Created | `application/json` | [CreatedEnrollmentToken](data-types.md#createdenrollmenttoken) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |

## Revoke an enrollment token (connected agents are not disconnected; reconnects fail)

`DELETE /api/v1/enrollment-tokens/{id}`

Operation `revokeEnrollmentToken`. Permission `enrollment:manage` (lowest role: admin).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 204 | Revoked | - | - |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
