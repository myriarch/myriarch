---
title: "Users"
weight: 20
description: "Users and roles."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [List users](#list-users) | `GET /users` | `users:manage` (admin) |
| [Change role (sets roleSource=manual) or disable a user](#change-role-sets-rolesourcemanual-or-disable-a-user) | `PATCH /users/{id}` | `users:manage` (admin) |

## List users

`GET /api/v1/users`

Operation `listUsers`. Permission `users:manage` (lowest role: admin).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Users | `application/json` | [UserList](data-types.md#userlist) |
| 403 | Missing permission (code forbidden) or CSRF failure (code csrf_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Change role (sets roleSource=manual) or disable a user

`PATCH /api/v1/users/{id}`

Operation `updateUser`. Permission `users:manage` (lowest role: admin).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [UpdateUserRequest](data-types.md#updateuserrequest).

```json
{
  "role": "viewer",
  "disabled": false
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated user | `application/json` | [User](data-types.md#user) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |
