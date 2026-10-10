---
title: "Rollouts"
weight: 140
description: "Config and version rollouts, operator actions."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Rollouts (newest first)](#rollouts-newest-first) | `GET /rollouts` | `read` (viewer) |
| [Rollout with waves, counts and policy snapshot](#rollout-with-waves-counts-and-policy-snapshot) | `GET /rollouts/{id}` | `read` (viewer) |
| [Targets (agents) of a rollout](#targets-agents-of-a-rollout) | `GET /rollouts/{id}/targets` | `read` (viewer) |
| [Pause (no new targets are started; in-progress targets finish)](#pause-no-new-targets-are-started-in-progress-targets-finish) | `POST /rollouts/{id}/actions/pause` | `rollouts:operate` (operator) |
| [Resume a paused rollout](#resume-a-paused-rollout) | `POST /rollouts/{id}/actions/resume` | `rollouts:operate` (operator) |
| [Abort - stop starting targets, cancel pending ones (updated agents keep the new state)](#abort---stop-starting-targets-cancel-pending-ones-updated-agents-keep-the-new-state) | `POST /rollouts/{id}/actions/abort` | `rollouts:operate` (operator) |
| [Approve a wave that requires approval](#approve-a-wave-that-requires-approval) | `POST /rollouts/{id}/actions/approve` | `rollouts:operate` (operator) |
| [Roll back all touched targets to their previous config/version](#roll-back-all-touched-targets-to-their-previous-configversion) | `POST /rollouts/{id}/actions/rollback` | `rollouts:operate` (operator) |
| [Reset failed/skipped targets to pending (rollout must be paused, running or failed)](#reset-failedskipped-targets-to-pending-rollout-must-be-paused-running-or-failed) | `POST /rollouts/{id}/actions/retry-failed` | `rollouts:operate` (operator) |

## Rollouts (newest first)

`GET /api/v1/rollouts`

Operation `listRollouts`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `fleet` | query | `string` | no |  |
| `kind` | query | [RolloutKind](data-types.md#rolloutkind) | no |  |
| `status` | query | array of [RolloutStatus](data-types.md#rolloutstatus) | no | Repeat the parameter for several values. |
| `active` | query | `boolean` | no | Only non-terminal rollouts. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Rollouts | `application/json` | [RolloutSummaryPage](data-types.md#rolloutsummarypage) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Rollout with waves, counts and policy snapshot

`GET /api/v1/rollouts/{id}`

Operation `getRollout`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Targets (agents) of a rollout

`GET /api/v1/rollouts/{id}/targets`

Operation `listRolloutTargets`. Permission `read` (lowest role: viewer).

Without `sort`, targets are ordered by wave, hostname and agent ID.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `wave` | query | `integer` | no | Minimum 0. |
| `status` | query | array of [TargetStatus](data-types.md#targetstatus) | no | Repeat the parameter for several values. |
| `agent` | query | `string` | no | Only the target of this agent (at most one item). The UI uses it to keep an open detail view current while the target moves between pages of the `activity` order. Format `uuid`. |
| `sort` | query | `string` | no | `wave` (default) orders by wave, hostname and agent ID. This order does not change while target statuses change, so use it to page through all targets. `activity` ranks by status first (in_progress and verifying, waiting_window, failed and rolled_back, pending, succeeded, skipped, cancelled), then by wave, hostname and agent ID. Statuses change while a rollout runs, so a target can move between pages between two requests. One of `wave`, `activity`. Default `wave`. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Targets | `application/json` | [RolloutTargetPage](data-types.md#rollouttargetpage) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Pause (no new targets are started; in-progress targets finish)

`POST /api/v1/rollouts/{id}/actions/pause`

Operation `pauseRollout`. Permission `rollouts:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, optional. Type: [RolloutActionRequest](data-types.md#rolloutactionrequest).

```json
{
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Resume a paused rollout

`POST /api/v1/rollouts/{id}/actions/resume`

Operation `resumeRollout`. Permission `rollouts:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, optional. Type: [RolloutActionRequest](data-types.md#rolloutactionrequest).

```json
{
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Abort - stop starting targets, cancel pending ones (updated agents keep the new state)

`POST /api/v1/rollouts/{id}/actions/abort`

Operation `abortRollout`. Permission `rollouts:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, optional. Type: [RolloutActionRequest](data-types.md#rolloutactionrequest).

```json
{
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Approve a wave that requires approval

`POST /api/v1/rollouts/{id}/actions/approve`

Operation `approveRolloutWave`. Permission `rollouts:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [ApproveWaveRequest](data-types.md#approvewaverequest).

```json
{
  "wave": 0,
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Roll back all touched targets to their previous config/version

`POST /api/v1/rollouts/{id}/actions/rollback`

Operation `rollbackRollout`. Permission `rollouts:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, optional. Type: [RolloutActionRequest](data-types.md#rolloutactionrequest).

```json
{
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Reset failed/skipped targets to pending (rollout must be paused, running or failed)

`POST /api/v1/rollouts/{id}/actions/retry-failed`

Operation `retryFailedRolloutTargets`. Permission `rollouts:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, optional. Type: [RolloutActionRequest](data-types.md#rolloutactionrequest).

```json
{
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Updated rollout | `application/json` | [Rollout](data-types.md#rollout) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |
