---
title: "GitOps"
weight: 160
description: "GitOps sync status, history, manual sync, webhook."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Sync status, applied/latest commit, validation issues](#sync-status-appliedlatest-commit-validation-issues) | `GET /gitops/status` | `read` (viewer) |
| [Sync history (newest first)](#sync-history-newest-first) | `GET /gitops/syncs` | `read` (viewer) |
| [All synced objects with their source file](#all-synced-objects-with-their-source-file) | `GET /gitops/objects` | `read` (viewer) |
| [Request an immediate sync (performed by the leader replica)](#request-an-immediate-sync-performed-by-the-leader-replica) | `POST /gitops/actions/sync` | `gitops:sync` (operator) |
| [Push webhook from GitLab (X-Gitlab-Token) or GitHub (X-Hub-Signature-256)](#push-webhook-from-gitlab-x-gitlab-token-or-github-x-hub-signature-256) | `POST /gitops/webhook` | `custom` |

## Sync status, applied/latest commit, validation issues

`GET /api/v1/gitops/status`

Operation `getGitOpsStatus`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Status | `application/json` | [GitOpsSyncStatus](data-types.md#gitopssyncstatus) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Sync history (newest first)

`GET /api/v1/gitops/syncs`

Operation `listGitOpsSyncs`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Syncs | `application/json` | [GitOpsSyncPage](data-types.md#gitopssyncpage) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## All synced objects with their source file

`GET /api/v1/gitops/objects`

Operation `listGitOpsObjects`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Objects | `application/json` | [GitOpsObjectRefList](data-types.md#gitopsobjectreflist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Request an immediate sync (performed by the leader replica)

`POST /api/v1/gitops/actions/sync`

Operation `triggerGitOpsSync`. Permission `gitops:sync` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Sync requested | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Push webhook from GitLab (X-Gitlab-Token) or GitHub (X-Hub-Signature-256)

`POST /api/v1/gitops/webhook`

Operation `gitopsWebhook`. Permission `custom`. Neither a session nor an API token is checked: the operation checks its own credential (see below).

Authenticated with the shared secret gitops.webhookSecret (not a session/API token).

### Request body

Content type `application/json`, optional. Type: map of `string` to any.

```json
{}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Sync requested | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
