---
title: "Agents"
weight: 40
description: "OpAMP agents (collectors and myriarch-updaters)."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [List agents (server-side filtering, sorting, pagination)](#list-agents-server-side-filtering-sorting-pagination) | `GET /agents` | `read` (viewer) |
| [Value counts for filter UIs (same filters as listAgents)](#value-counts-for-filter-uis-same-filters-as-listagents) | `GET /agents/facets` | `read` (viewer) |
| [Agent details](#agent-details) | `GET /agents/{id}` | `read` (viewer) |
| [Forget an agent (only allowed while offline; 412 otherwise)](#forget-an-agent-only-allowed-while-offline-412-otherwise) | `DELETE /agents/{id}` | `agents:delete` (operator) |
| [Effective config reported by the agent (expanded, includes defaults, secrets redacted by the collector)](#effective-config-reported-by-the-agent-expanded-includes-defaults-secrets-redacted-by-the-collector) | `GET /agents/{id}/effective-config` | `read` (viewer) |
| [Rendered config currently offered (desired) or assigned by GitOps](#rendered-config-currently-offered-desired-or-assigned-by-gitops) | `GET /agents/{id}/desired-config` | `read` (viewer) |
| [Available components reported by the agent](#available-components-reported-by-the-agent) | `GET /agents/{id}/components` | `read` (viewer) |
| [Event history of the agent (newest first, cursor pagination)](#event-history-of-the-agent-newest-first-cursor-pagination) | `GET /agents/{id}/events` | `read` (viewer) |
| [Send an OpAMP restart command (agent must have AcceptsRestartCommand)](#send-an-opamp-restart-command-agent-must-have-acceptsrestartcommand) | `POST /agents/{id}/actions/restart` | `agents:operate` (operator) |
| [Force resync - clear failed config hash, re-send desired config, request full state](#force-resync---clear-failed-config-hash-re-send-desired-config-request-full-state) | `POST /agents/{id}/actions/resync` | `agents:operate` (operator) |
| [Clear the credential binding of an agent (the instance uid is bound to the enrollment token / client certificate of its first registration; the next connection with a valid credential claims it)](#clear-the-credential-binding-of-an-agent-the-instance-uid-is-bound-to-the-enrollment-token--client-certificate-of-its-first-registration-the-next-connection-with-a-valid-credential-claims-it) | `POST /agents/{id}/actions/reenroll` | `enrollment:manage` (admin) |

## List agents (server-side filtering, sorting, pagination)

`GET /api/v1/agents`

Operation `listAgents`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `q` | query | `string` | no | Case-insensitive substring search in hostname, id, version and attribute values (at most 200 characters; multi-value filters accept at most 50 values, attr at most 20 pairs). At most 200 characters. |
| `kind` | query | array of [AgentKind](data-types.md#agentkind) | no | Repeat the parameter for several values. |
| `status` | query | array of [ConnectionStatus](data-types.md#connectionstatus) | no | Repeat the parameter for several values. |
| `healthy` | query | `boolean` | no |  |
| `fleet` | query | array of `string` | no | Repeat the parameter for several values. |
| `unassigned` | query | `boolean` | no | Only agents without a fleet. Combined with `fleet` by AND (so the result is empty when a fleet is given). |
| `version` | query | array of `string` | no | Repeat the parameter for several values. |
| `outdated` | query | `boolean` | no | true = collectors below the latest stable otelcol-contrib release; false = collectors at or above it (implies kind=collector). |
| `os` | query | array of `string` | no | Repeat the parameter for several values. Items: example `windows`. |
| `attr` | query | array of `string` | no | Exact attribute match "key=value" (repeatable; values of the same key are ORed, different keys ANDed). Keys: agent attributes or "labels.&lt;key>". Repeat the parameter for several values. Items: example `site.type=edge`. |
| `configSync` | query | array of [ConfigSyncState](data-types.md#configsyncstate) | no | Repeat the parameter for several values. |
| `sort` | query | `string` | no | Sort field, prefix "-" for descending. One of `hostname`, `-hostname`, `version`, `-version`, `status`, `-status`, `fleet`, `-fleet`, `lastSeenAt`, `-lastSeenAt`, `firstSeenAt`, `-firstSeenAt`, `health`, `-health`, `configSync`, `-configSync`, `osType`, `-osType`, `siteId`, `-siteId`. Default `hostname`. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `50`. |
| `offset` | query | `integer` | no | Range 0 to 100000. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Page of agents | `application/json` | [AgentSummaryPage](data-types.md#agentsummarypage) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |

## Value counts for filter UIs (same filters as listAgents)

`GET /api/v1/agents/facets`

Operation `getAgentFacets`. Permission `read` (lowest role: viewer).

Counts are disjunctive: the counts of a facet ignore the filter of that facet's own dimension (so selecting one fleet still lists the other fleets with their counts and a multi-select is possible) and apply all other filters. `total` counts the agents matching all filters.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `q` | query | `string` | no | Case-insensitive substring search in hostname, id, version and attribute values (at most 200 characters; multi-value filters accept at most 50 values, attr at most 20 pairs). At most 200 characters. |
| `kind` | query | array of [AgentKind](data-types.md#agentkind) | no | Repeat the parameter for several values. |
| `status` | query | array of [ConnectionStatus](data-types.md#connectionstatus) | no | Repeat the parameter for several values. |
| `healthy` | query | `boolean` | no |  |
| `fleet` | query | array of `string` | no | Repeat the parameter for several values. |
| `unassigned` | query | `boolean` | no | Only agents without a fleet. Combined with `fleet` by AND (so the result is empty when a fleet is given). |
| `version` | query | array of `string` | no | Repeat the parameter for several values. |
| `outdated` | query | `boolean` | no | true = collectors below the latest stable otelcol-contrib release; false = collectors at or above it (implies kind=collector). |
| `os` | query | array of `string` | no | Repeat the parameter for several values. Items: example `windows`. |
| `attr` | query | array of `string` | no | Exact attribute match "key=value" (repeatable; values of the same key are ORed, different keys ANDed). Keys: agent attributes or "labels.&lt;key>". Repeat the parameter for several values. Items: example `site.type=edge`. |
| `configSync` | query | array of [ConfigSyncState](data-types.md#configsyncstate) | no | Repeat the parameter for several values. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Facets | `application/json` | [AgentFacets](data-types.md#agentfacets) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Agent details

`GET /api/v1/agents/{id}`

Operation `getAgent`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Agent | `application/json` | [AgentDetail](data-types.md#agentdetail) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Forget an agent (only allowed while offline; 412 otherwise)

`DELETE /api/v1/agents/{id}`

Operation `deleteAgent`. Permission `agents:delete` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 204 | Deleted | - | - |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Effective config reported by the agent (expanded, includes defaults, secrets redacted by the collector)

`GET /api/v1/agents/{id}/effective-config`

Operation `getAgentEffectiveConfig`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Effective config | `application/json` | [ConfigDocument](data-types.md#configdocument) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Rendered config currently offered (desired) or assigned by GitOps

`GET /api/v1/agents/{id}/desired-config`

Operation `getAgentDesiredConfig`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |
| `which` | query | `string` | no | One of `desired`, `assigned`. Default `desired`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Rendered config | `application/json` | [ConfigDocument](data-types.md#configdocument) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Available components reported by the agent

`GET /api/v1/agents/{id}/components`

Operation `getAgentComponents`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Component set | `application/json` | [ComponentSet](data-types.md#componentset) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Event history of the agent (newest first, cursor pagination)

`GET /api/v1/agents/{id}/events`

Operation `listAgentEvents`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |
| `limit` | query | `integer` | no | Range 1 to 500. Default `100`. |
| `before` | query | `integer` | no | Return events with id &lt; before (0 = newest). Format `int64`. Minimum 0. Default `0`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Events | `application/json` | [AgentEventList](data-types.md#agenteventlist) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Send an OpAMP restart command (agent must have AcceptsRestartCommand)

`POST /api/v1/agents/{id}/actions/restart`

Operation `restartAgent`. Permission `agents:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Command queued | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |

## Force resync - clear failed config hash, re-send desired config, request full state

`POST /api/v1/agents/{id}/actions/resync`

Operation `resyncAgent`. Permission `agents:operate` (lowest role: operator).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Command queued | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Clear the credential binding of an agent (the instance uid is bound to the enrollment token / client certificate of its first registration; the next connection with a valid credential claims it)

`POST /api/v1/agents/{id}/actions/reenroll`

Operation `reenrollAgent`. Permission `enrollment:manage` (lowest role: admin).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | `string` | yes | OpAMP instance_uid as UUID string. Format `uuid`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 202 | Binding cleared | `application/json` | [ActionAccepted](data-types.md#actionaccepted) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
