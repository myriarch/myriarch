---
title: "Saved filters"
weight: 50
description: "Saved agent list filters."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Own and shared saved filters](#own-and-shared-saved-filters) | `GET /saved-filters` | `read` (viewer) |
| [Save a filter](#save-a-filter) | `POST /saved-filters` | `saved_filters:write` (viewer) |
| [Delete an own saved filter](#delete-an-own-saved-filter) | `DELETE /saved-filters/{id}` | `saved_filters:write` (viewer) |

## Own and shared saved filters

`GET /api/v1/saved-filters`

Operation `listSavedFilters`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `page` | query | `string` | no | Default `agents`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Saved filters | `application/json` | [SavedFilterList](data-types.md#savedfilterlist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Save a filter

`POST /api/v1/saved-filters`

Operation `createSavedFilter`. Permission `saved_filters:write` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [CreateSavedFilterRequest](data-types.md#createsavedfilterrequest).

```json
{
  "name": "string",
  "page": "agents",
  "query": {},
  "shared": false
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 201 | Created | `application/json` | [SavedFilter](data-types.md#savedfilter) |
| 409 | Conflict (code conflict) | `application/problem+json` | [Problem](data-types.md#problem) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |

## Delete an own saved filter

`DELETE /api/v1/saved-filters/{id}`

Operation `deleteSavedFilter`. Permission `saved_filters:write` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 204 | Deleted | - | - |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
