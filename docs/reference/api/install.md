---
title: "Install"
weight: 190
description: "Installation of hosts from the server - plan, install grants, installer bundles and rendered commands (available from v0.2.0)."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Plan for installing a host from the server (authenticated by an enrollment token)](#plan-for-installing-a-host-from-the-server-authenticated-by-an-enrollment-token) | `POST /install/plan` | `custom` |

## Plan for installing a host from the server (authenticated by an enrollment token)

`POST /api/v1/install/plan`

Operation `getInstallPlan`. Permission `custom`. Neither a session nor an API token is checked: the operation checks its own credential (see below).

Available from v0.2.0. The installers call this before a host is enrolled, and so can tools. The request names the platform of the host,
the site attributes that it would report and, optionally, the fleet that the person expects and a collector version. The server decides the
fleet exactly as it does for an agent that connects (the attributes and the labels of the enrollment token), takes the version from the
effective target of the fleet and prepares the collector and the supervisor for the platform. The answer names the versions, the
URLs of the artifacts on the origin of the server and an install grant for each. The installer downloads an artifact with
`Authorization: Bearer <grant>` from `downloadArtifact` and fetches `<url>.sha256` and `<url>.sigstore.json` from the same route; it
verifies both against an identity that it builds itself, so the answer is data and never code.

The request is authenticated by the enrollment token that the host will use, as `Authorization: Bearer myre_...`, always: also
where `opamp.requireEnrollmentToken` is off, and with the client certificate rule of the OpAMP endpoint where
`opamp.requireClientCert` is set. The token never appears in a body, a URL or an answer. A request without a token and one with a token
that is unknown, revoked or expired get the same 401. Failed authentications are limited per client address, and the lookups of tokens
per replica (429 with `Retry-After`).

The plan is `text/plain` by default: one `key=value` line each, in the order of the keys below, with values from the characters
`A-Z a-z 0-9 . _ : / @ + = -` (the three URL values may also hold the brackets of an IPv6 literal), at most 64 lines and 8 KiB, so that bash
without `jq` and Windows PowerShell 5.1 can read it. `Accept: application/json` returns the object `InstallPlan`. The keys are
`format` (1; a reader stops at a format that it does not know), `fleet`, `version.source`, `opamp.endpoint`, `collector.version`,
`collector.url`, `collector.grant`, `supervisor.version`, `supervisor.url`, `supervisor.grant` and `updater.install`.

The version is the effective target of the fleet (`version.source` is `git` or `ui`). While a version rollout of the fleet is
pending, running or waiting for approval it is the version the fleet ran before (`rollout`), so that the new host becomes a straggler of
the last wave. A version that the host asks for is accepted if it is the target, the version the plan would give anyway or inside the
frame `spec.update.selection` in Git (`request`). The supervisor has the version of the collector, and the release tracker must know
both releases.

A refusal is a problem document. The class is its `code` and the reason is the `code` of `issues[0]`, which the installers show:
`conflict` with `fleet_mismatch`, `no_fleet`, `fleet_ambiguous` or `rollout_blocking`; `precondition_failed` with `no_target_version`,
`unsupported_distribution`, `unknown_version`, `unknown_supervisor_version` or `server_settings`; `validation_failed` with
`invalid_value`, `unknown_attribute`, `platform_not_supported`, `version_not_allowed`, `unknown_version` or `unknown_supervisor_version`;
`unavailable` with `preparing`. The first request for a platform makes the server download about 112 MB, so it can answer
`preparing` for minutes; the installer asks again after `Retry-After`.

### Request body

Content type `application/json`, required. Type: [InstallPlanRequest](data-types.md#installplanrequest).

```json
{
  "os": "linux",
  "arch": "amd64",
  "attributes": {
    "key": "string"
  },
  "fleet": "linux-hosts",
  "collectorVersion": "0.162.0"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | The plan (header Vary: Accept) | `text/plain`, `application/json` | `string`, [InstallPlan](data-types.md#installplan) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | The installation of hosts from the server is turned off (`install.enabled`; code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 409 | The host cannot be placed or a version rollout is in the way (code conflict; reasons fleet_mismatch, no_fleet, fleet_ambiguous, rollout_blocking) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | The server has no version to give (code precondition_failed; reasons no_target_version, unsupported_distribution, unknown_version, unknown_supervisor_version, server_settings) | `application/problem+json` | [Problem](data-types.md#problem) |
| 422 | The request is not acceptable (code validation_failed; the issues name the field in `path`) | `application/problem+json` | [Problem](data-types.md#problem) |
| 429 | Too many failed authentications from this client address, or too many token lookups on this replica (code too_many_requests, header Retry-After) | `application/problem+json` | [Problem](data-types.md#problem) |
| 503 | The artifacts are being prepared (code unavailable, reason preparing, header Retry-After) or the database is not available | `application/problem+json` | [Problem](data-types.md#problem) |
