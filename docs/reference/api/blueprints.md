---
title: "Blueprints"
weight: 110
description: "Blueprints from GitOps (read-only), rendering preview."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [All blueprints](#all-blueprints) | `GET /blueprints` | `read` (viewer) |
| [Blueprint details (chain, merged parameters, usage, issues)](#blueprint-details-chain-merged-parameters-usage-issues) | `GET /blueprints/{name}` | `read` (viewer) |
| [Render a preview for an agent or hypothetical attributes (no side effects)](#render-a-preview-for-an-agent-or-hypothetical-attributes-no-side-effects) | `POST /blueprints/{name}/render` | `read` (viewer) |

## All blueprints

`GET /api/v1/blueprints`

Operation `listBlueprints`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Blueprints | `application/json` | [BlueprintSummaryList](data-types.md#blueprintsummarylist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Blueprint details (chain, merged parameters, usage, issues)

`GET /api/v1/blueprints/{name}`

Operation `getBlueprint`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Blueprint | `application/json` | [BlueprintDetail](data-types.md#blueprintdetail) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Render a preview for an agent or hypothetical attributes (no side effects)

`POST /api/v1/blueprints/{name}/render`

Operation `renderBlueprint`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [RenderRequest](data-types.md#renderrequest).

```json
{
  "agentId": "00000000-0000-4000-8000-000000000000",
  "attributes": {
    "key": "string"
  },
  "fleet": "string",
  "parameters": {},
  "validate": true
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Render result (issues instead of HTTP errors for rendering problems) | `application/json` | [RenderResponse](data-types.md#renderresponse) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
