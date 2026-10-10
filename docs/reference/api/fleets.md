---
title: "Fleets"
weight: 100
description: "Fleets from GitOps (read-only), fleet matching, UI version selection within the Git frame."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [All fleets with live status](#all-fleets-with-live-status) | `GET /fleets` | `read` (viewer) |
| [Explain which fleet an (existing or hypothetical) agent belongs to](#explain-which-fleet-an-existing-or-hypothetical-agent-belongs-to) | `POST /fleets/match` | `read` (viewer) |
| [Fleet details](#fleet-details) | `GET /fleets/{name}` | `read` (viewer) |
| [Versions an operator may select for the fleet (frame from Git, dialog data)](#versions-an-operator-may-select-for-the-fleet-frame-from-git-dialog-data) | `GET /fleets/{name}/version-options` | `read` (viewer) |
| [Set the target collector version of the fleet within the Git frame and start the version rollout](#set-the-target-collector-version-of-the-fleet-within-the-git-frame-and-start-the-version-rollout) | `POST /fleets/{name}/actions/select-version` | `fleets:set_version` (operator) |
| [Remove the UI version selection; the Git target (spec.collector.version) applies again](#remove-the-ui-version-selection-the-git-target-speccollectorversion-applies-again) | `POST /fleets/{name}/actions/clear-version-selection` | `fleets:set_version` (operator) |

## All fleets with live status

`GET /api/v1/fleets`

Operation `listFleets`. Permission `read` (lowest role: viewer).

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Fleets | `application/json` | [FleetSummaryList](data-types.md#fleetsummarylist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |

## Explain which fleet an (existing or hypothetical) agent belongs to

`POST /api/v1/fleets/match`

Operation `matchFleets`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [MatchFleetsRequest](data-types.md#matchfleetsrequest).

```json
{
  "agentId": "00000000-0000-4000-8000-000000000000",
  "attributes": {
    "key": "string"
  }
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Match result | `application/json` | [MatchFleetsResponse](data-types.md#matchfleetsresponse) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Fleet details

`GET /api/v1/fleets/{name}`

Operation `getFleet`. Permission `read` (lowest role: viewer).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Fleet | `application/json` | [FleetDetail](data-types.md#fleetdetail) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Versions an operator may select for the fleet (frame from Git, dialog data)

`GET /api/v1/fleets/{name}/version-options`

Operation `getFleetVersionOptions`. Permission `read` (lowest role: viewer).

Returns the effective target, the frame from Git (`spec.update.selection`) and every candidate version with
the information the update dialog shows. Candidates are the known non-prerelease releases of the fleet's
distribution that satisfy the frame, the versions listed in `spec.update.selection.versions`, the Git
target (`spec.collector.version`) and the version of an existing UI selection; newest first. A candidate
that cannot be selected carries `selectable=false` and a `reasonCode`. Works for every fleet: without a
frame `allowed` is false (`notAllowedCode=selection_disabled`) and `options` lists only the Git target.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Version options | `application/json` | [FleetVersionOptions](data-types.md#fleetversionoptions) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Set the target collector version of the fleet within the Git frame and start the version rollout

`POST /api/v1/fleets/{name}/actions/select-version`

Operation `selectFleetVersion`. Permission `fleets:set_version` (lowest role: operator).

Stores a UI version selection for the fleet (who, when, why; audited as `fleet.version.select`, also when
refused) and asks the leader to plan the fleet. The version rollout is created by the planner on the leader
with the policy `spec.update.selection.rolloutPolicy` (default `spec.rollout.versionPolicy`), `origin=ui`,
`createdBy` = the operator and `requestReason` = `reason`; an active version rollout to another version is
superseded. If `version` already is the effective target, nothing is stored (200, `planned=false`, message).

The handler waits up to 10 s for the leader to plan the fleet. `planned=true`: the planner has run for this
selection; `rolloutId` is the version rollout that brings the fleet to the selected version (absent if no
member collector needs an update). `planned=false`: not planned within the wait time (leader busy or
changing); the rollout follows, the UI waits for `fleet.updated` / `rollout.updated`.

Preconditions (412 `precondition_failed`): `spec.update.selection.enabled` is true, the fleet has an update
executor (not `none`), and the fleet has no version rollout in status `failed` or `rolling_back` (abort it
first). Validation (422 `validation_failed`, `issues[].code`): `version_not_allowed` (outside the frame),
`downgrade_not_allowed` (lower than the current effective target and `allowDowngrade` is false),
`unknown_version` (not a known release of the distribution and not listed in `selection.versions`),
`prerelease_not_allowed`, `reason_required` (`selection.requireReason` and no reason). 409 `conflict`:
`expectedTargetVersion` does not match the current effective target (someone else changed it).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [SelectFleetVersionRequest](data-types.md#selectfleetversionrequest).

```json
{
  "version": "0.162.0",
  "reason": "string",
  "expectedTargetVersion": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Selection stored (and usually planned) | `application/json` | [SelectFleetVersionResponse](data-types.md#selectfleetversionresponse) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 403 | Missing permission (code forbidden) or CSRF failure (code csrf_failed) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 409 | Conflict (code conflict) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | Invalid state transition or precondition (code precondition_failed) | `application/problem+json` | [Problem](data-types.md#problem) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |

## Remove the UI version selection; the Git target (spec.collector.version) applies again

`POST /api/v1/fleets/{name}/actions/clear-version-selection`

Operation `clearFleetVersionSelection`. Permission `fleets:set_version` (lowest role: operator).

Deletes the UI selection (active or invalidated) of the fleet; audited as `fleet.version.clear`. The effective
target becomes `spec.collector.version` (or none). The planner then supersedes a version rollout to the
previously selected version and, if the Git target differs from the members' versions, creates a new rollout
(this can be a downgrade: the UI warns before). Allowed even when `spec.update.selection.enabled` is false.
Without a selection the call changes nothing and returns the current target (200). With
`spec.update.selection.requireReason` a reason is mandatory (422 `validation_failed`, issue `reason_required`).

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | `string` | yes | Pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`. |
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, optional. Type: [ClearFleetVersionSelectionRequest](data-types.md#clearfleetversionselectionrequest).

```json
{
  "reason": "string"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | Effective target after clearing | `application/json` | [FleetVersionTarget](data-types.md#fleetversiontarget) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 403 | Missing permission (code forbidden) or CSRF failure (code csrf_failed) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | Not found (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 422 | Validation failed (code validation_failed, details in issues) | `application/problem+json` | [Problem](data-types.md#problem) |
