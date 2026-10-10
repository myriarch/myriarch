---
title: "Notifications"
weight: 180
description: "Notification channels."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Configured channels (secrets redacted)](#configured-channels-secrets-redacted) | `GET /notifications/channels` | `read` (viewer) |
| [Send a test notification](#send-a-test-notification) | `POST /notifications/channels/{name}/actions/test` | `notifications:test` (operator) |

## Configured channels (secrets redacted)

`GET /api/v1/notifications/channels`

Operation `listNotificationChannels`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Channels | `application/json` | [ChannelInfoList](data-types.md#channelinfolist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Send a test notification

`POST /api/v1/notifications/channels/{name}/actions/test`

Operation `testNotificationChannel`. Permission `notifications:test` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Sent | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
