---
title: "Releases"
weight: 150
description: "Upstream collector releases, breaking changes."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Detected upstream releases (highest version first)](#detected-upstream-releases-highest-version-first) | `GET /releases` | `read` (viewer) |
| [Latest stable release of a distribution](#latest-stable-release-of-a-distribution) | `GET /releases/latest` | `read` (viewer) |
| [Breaking changes and deprecations for versions in (from, to]](#breaking-changes-and-deprecations-for-versions-in-from-to) | `GET /releases/compare` | `read` (viewer) |
| [Poll upstream releases now](#poll-upstream-releases-now) | `POST /releases/actions/refresh` | `releases:acknowledge` (operator) |
| [Release details incl. fleets behind and GitOps snippet](#release-details-incl-fleets-behind-and-gitops-snippet) | `GET /releases/{distribution}/{version}` | `read` (viewer) |
| [Acknowledge the release notice](#acknowledge-the-release-notice) | `POST /releases/{distribution}/{version}/actions/acknowledge` | `releases:acknowledge` (operator) |

## Detected upstream releases (highest version first)

`GET /api/v1/releases`

Operation `listReleases`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `distribution` | query | `string` | no | One of `otelcol-contrib`, `opampsupervisor`. Default `otelcol-contrib`. |
| `includePrerelease` | query | `boolean` | no | Default `false`. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Releases | `application/json` | [ReleaseSummaryPage](data-types.md#releasesummarypage) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Latest stable release of a distribution

`GET /api/v1/releases/latest`

Operation `getLatestRelease`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `distribution` | query | `string` | no | One of `otelcol-contrib`, `opampsupervisor`. Default `otelcol-contrib`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Latest release | `application/json` | [ReleaseDetail](data-types.md#releasedetail) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Breaking changes and deprecations for versions in (from, to]

`GET /api/v1/releases/compare`

Operation `compareVersions`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `distribution` | query | `string` | no | One of `otelcol-contrib`, `opampsupervisor`. Default `otelcol-contrib`. |
| `from` | query | `string` | yes | Example `0.150.0`. |
| `to` | query | `string` | yes | Example `0.162.0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Comparison | `application/json` | [VersionComparison](data-types.md#versioncomparison) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |

## Poll upstream releases now

`POST /api/v1/releases/actions/refresh`

Operation `refreshReleases`. Permission `releases:acknowledge` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Refresh requested | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Release details incl. fleets behind and GitOps snippet

`GET /api/v1/releases/{distribution}/{version}`

Operation `getRelease`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `distribution` | path | `string` | yes | Example `otelcol-contrib`. |
| `version` | path | `string` | yes | Example `0.162.0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Release | `application/json` | [ReleaseDetail](data-types.md#releasedetail) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Acknowledge the release notice

`POST /api/v1/releases/{distribution}/{version}/actions/acknowledge`

Operation `acknowledgeRelease`. Permission `releases:acknowledge` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `distribution` | path | `string` | yes |  |
| `version` | path | `string` | yes |  |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Acknowledged release | `application/json` | [Release](data-types.md#release) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
