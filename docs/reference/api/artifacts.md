---
title: "Artifacts"
weight: 170
description: "Artifact cache for collector binaries."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Cached artifacts](#cached-artifacts) | `GET /artifacts` | `read` (viewer) |
| [Download and verify artifacts for a version and platforms](#download-and-verify-artifacts-for-a-version-and-platforms) | `POST /artifacts/actions/prefetch` | `artifacts:manage` (operator) |
| [One artifact](#one-artifact) | `GET /artifacts/{id}` | `read` (viewer) |
| [Remove an artifact from the cache](#remove-an-artifact-from-the-cache) | `DELETE /artifacts/{id}` | `artifacts:manage` (operator) |
| [Download artifact bytes (used by myriarch-updater; Range supported)](#download-artifact-bytes-used-by-myriarch-updater-range-supported) | `GET /artifacts/{id}/content/{filename}` | `custom` |

## Cached artifacts

`GET /api/v1/artifacts`

Operation `listArtifacts`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `distribution` | query | `string` | no | One of `otelcol-contrib`, `opampsupervisor`. Default `otelcol-contrib`. |
| `version` | query | `string` | no |  |
| `status` | query | array of [ArtifactStatus](data-types.md#artifactstatus) | no | Repeat the parameter for several values. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Artifacts | `application/json` | [ArtifactPage](data-types.md#artifactpage) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Download and verify artifacts for a version and platforms

`POST /api/v1/artifacts/actions/prefetch`

Operation `prefetchArtifacts`. Permission `artifacts:manage` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [PrefetchRequest](data-types.md#prefetchrequest).

```json
{
  "distribution": "otelcol-contrib",
  "version": "0.162.0",
  "platforms": [
    "windows/amd64"
  ]
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Prefetch started; current artifact states | `application/json` | [ArtifactList](data-types.md#artifactlist) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |

## One artifact

`GET /api/v1/artifacts/{id}`

Operation `getArtifact`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Artifact | `application/json` | [Artifact](data-types.md#artifact) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Remove an artifact from the cache

`DELETE /api/v1/artifacts/{id}`

Operation `deleteArtifact`. Permission `artifacts:manage` (lowest role: operator).

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

## Download artifact bytes (used by myriarch-updater; Range supported)

`GET /api/v1/artifacts/{id}/content/{filename}`

Operation `downloadArtifact`. Permission `custom`. Neither a session nor an API token is checked: the operation checks its own credential (see below).

Authenticated with a short-lived download token issued in the OpAMP package offer
(`DownloadableFile.headers`: `Authorization: Bearer myrd_...`). The filename segment
lets agents detect the archive format from the URL suffix.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `filename` | path | `string` | yes |  |
| `Range` | header | `string` | no |  |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Full content | `application/octet-stream` | `string`. Format `binary`. |
| 206 | Partial content | `application/octet-stream` | `string`. Format `binary`. |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
