---
title: "Items"
weight: 10
description: "Items and their parts."
---

All paths are relative to the base path `/api/v1`. Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [List items](#list-items) | `GET /items` | `read` (viewer) |
| [Create an item](#create-an-item) | `POST /items` | `items:write` (operator) |
| [Delete an item](#delete-an-item) | `DELETE /items/{id}` | `items:write` (operator) |
| [Sign in](#sign-in) | `GET /login` | `public` |
| [Receive a webhook](#receive-a-webhook) | `POST /hook` | `custom` |

## List items

`GET /api/v1/items`

Operation `listItems`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `kind` | query | array of [Kind](data-types.md#kind) | no | Filter by kind. Values are ORed: "a\|b". Repeat the parameter for several values. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Items | `application/json` | [ItemList](data-types.md#itemlist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Create an item

`POST /api/v1/items`

Operation `createItem`. Permission `items:write` (lowest role: operator).

Creates an item. The name must be unique; a &lt;placeholder> is escaped and `<code>` is not.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests. |

### Request body

Content type `application/json`, required. Type: [CreateItem](data-types.md#createitem).

```json
{
  "name": "string",
  "kind": "alpha",
  "weight": 1.5
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 201 | Created | `application/json` | [Item](data-types.md#item) |
| 422 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Delete an item

`DELETE /api/v1/items/{id}`

Operation `deleteItem`. Permission `items:write` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 204 | Deleted | - | - |

## Sign in

`GET /api/v1/login`

Operation `login`. Permission `public`. No login is needed.

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 302 | Redirect to the identity provider | - | - |

## Receive a webhook

`POST /api/v1/hook`

Operation `hook`. Permission `custom`. Neither a session nor an API token is checked: the operation checks its own credential (see below).

Authenticated with a shared secret.

### Request body

Content type `application/json`, optional. Type: map of `string` to any.

```json
{}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Accepted | - | - |
