---
title: "Install"
weight: 190
description: "Installation of hosts from the server - plan, install grants, installer bundles and rendered commands (available from v0.2.0)."
---

All paths are relative to the base path `/api/v1` (same origin as the UI). Authentication, roles, pagination and the error format are described in the [overview](_index.md); the types are on the page [Data types](data-types.md).

| Operation | Request | Permission |
|---|---|---|
| [Plan for installing a host from the server (authenticated by an enrollment token)](#plan-for-installing-a-host-from-the-server-authenticated-by-an-enrollment-token) | `POST /install/plan` | `custom` |
| [The installer bundles of this server and their state](#the-installer-bundles-of-this-server-and-their-state) | `GET /install/bundles` | `read` (viewer) |
| [Download an installer bundle (authenticated by an enrollment token; Range supported)](#download-an-installer-bundle-authenticated-by-an-enrollment-token-range-supported) | `GET /install/bundles/{filename}` | `custom` |
| [Render the install commands for hosts (a command for each platform)](#render-the-install-commands-for-hosts-a-command-for-each-platform) | `POST /install/commands` | `read` (viewer) |

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

## The installer bundles of this server and their state

`GET /api/v1/install/bundles`

Operation `listInstallBundles`. Permission `read` (lowest role: viewer).

Available from v0.2.0. The installer bundle is the updater archive of the release that the server itself runs
(`myriarch-updater_<version>_<os>_<arch>.tar.gz` or `.zip`: the installer scripts, the templates and the myriarch-updater binary),
which a host downloads with `downloadInstallBundle` before it runs the installer from it. The server fetches the archives from
`install.bundleSourceBaseUrl` and keeps one for each platform of `install.platforms`, but only after it verified `checksums.txt` of the
release keyless against the release workflow of this project and found the archive in it. Reading this list changes nothing: the
preparation starts shortly after the start of the server and when somebody renders commands or asks for a bundle.

`source` is `release` (the bundles of this release), `directory` (a development server that serves the archives of a directory) or
`none`. A server that is not a release of the project (a local build, `reason` is `local_build`) has no bundles. `status` is `pending`,
`preparing`, `ready` or `failed`; a failed bundle has the reason in `error`, is not delivered and is tried again after a minute.
Each replica keeps its own bundles, so the list shows the replica that answers.

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | The bundles | `application/json` | [InstallBundleList](data-types.md#installbundlelist) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | The installation of hosts from the server is turned off (`install.enabled`; code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |

## Download an installer bundle (authenticated by an enrollment token; Range supported)

`GET /api/v1/install/bundles/{filename}`

Operation `downloadInstallBundle`. Permission `custom`. Neither a session nor an API token is checked: the operation checks its own credential (see below).

Available from v0.2.0. The copied install command downloads the bundle from here and checks the SHA-256 that the command pins before
it unpacks anything. The request is authenticated by the enrollment token that the host will use, as `Authorization: Bearer myre_...`,
always, and with the rules of `getInstallPlan`: a request without a token and one with a token that is unknown, revoked or expired get the
same 401, and failed authentications and lookups are limited (429). The token is never part of the URL.

The server delivers the bytes of the release and nothing else. Before every delivery it hashes the stored archive again against the SHA-256 of
its line in the signed `checksums.txt`; an archive that is gone or changed is discarded and fetched again, and the request gets 503 with
`Retry-After`. The `ETag` is `"sha256-<hex>"`.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `filename` | path | `string` | yes | The name of the archive, for example `myriarch-updater_0.2.0_linux_amd64.tar.gz` (`.zip` for Windows). Only the bundles of this server exist. Pattern `^myriarch-updater_[0-9A-Za-z.+-]+_(linux\|windows)_(amd64\|arm64)\.(tar\.gz\|zip)$`. |
| `Range` | header | `string` | no |  |

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | The archive | `application/gzip`, `application/zip` | `string`. Format `binary`., `string`. Format `binary`. |
| 206 | Partial content | `application/gzip`, `application/zip` | `string`. Format `binary`., `string`. Format `binary`. |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | There is no such bundle, or the installation of hosts from the server is turned off (code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 429 | Too many failed authentications from this client address, or too many token lookups on this replica (code too_many_requests, header Retry-After) | `application/problem+json` | [Problem](data-types.md#problem) |
| 503 | The bundle is being prepared or failed, or the archive on the disk was found changed and is fetched again (code unavailable, reason bundle_preparing or bundle_failed, header Retry-After), or the database is not available | `application/problem+json` | [Problem](data-types.md#problem) |

## Render the install commands for hosts (a command for each platform)

`POST /api/v1/install/commands`

Operation `renderInstallCommands`. Permission `read` (lowest role: viewer).

Available from v0.2.0. The command that a person copies to a host: it downloads the installer bundle of this server, checks the
SHA-256 that it pins, unpacks the bundle in a private directory and starts the installer from it, which asks the server for the plan.
The server renders the command and never sees a token. The variant `token_inline` has the placeholder `tokenPlaceholder` where the token
goes; the web UI replaces it in the browser with the token that the person created or pasted, in every command. The variant `token_file`
reads the token from a file on the host (`tokenFiles`, one path for each operating system that is rendered) and has no placeholder. The
variant `lab` is `token_inline` for a development server over plain http, with `--insecure` for the installer; any other server refuses it.
A command that carries a token needs `server.publicUrl` to be an https URL. A server that requires a client certificate
(`opamp.requireClientCert`) renders no command (412, reason `server_settings`): the routes of the installation ask for a verified client
certificate there too, and the commands cannot present one.

The text of a command is a constant. Every value (the server, the bundle, its SHA-256, the options of the installer) is an argument of its own,
quoted with single quotes, and has been checked against an allowlist: printable ASCII, no quotation mark of any kind (also not the typographic
ones of U+2018 to U+201E), no backtick, `$`, `;` or control character, no `@@`, nothing that looks like a token, and a length limit. Anything else
is 422 and nothing is rendered. The installers need the type and the ID of the site, so `site.type` and `site.id` are required.

The commands are for a root shell on Linux (they start with `sudo`) and an elevated Windows PowerShell. Reading them is `read`, creating a
token for them needs `enrollment:manage` as before.

A bundle that is not `ready` yet is 503 with `Retry-After` (reason `bundle_preparing`, or `bundle_failed` with the cause); the request starts the
preparation. A server that is not a release has no bundle: 412 with the reason `local_build`.

### Parameters

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `X-CSRF-Token` | header | `string` | no | Required for cookie-authenticated mutating requests (value of cookie myriarch_csrf). |

### Request body

Content type `application/json`, required. Type: [InstallCommandsRequest](data-types.md#installcommandsrequest).

```json
{
  "variant": "token_inline",
  "platforms": [
    "linux/amd64"
  ],
  "fleet": "linux-hosts",
  "attributes": {
    "key": "string"
  },
  "collectorVersion": "0.162.0",
  "tokenFiles": {
    "linux": "/etc/myriarch/enrollment-token",
    "windows": "C:\\ProgramData\\Secrets\\myriarch-token.txt"
  },
  "caFile": "/etc/ssl/myriarch-ca.pem"
}
```

### Responses

| Status | Description | Content type | Type |
|---|---|---|---|
| 200 | The commands (header Cache-Control: no-store) | `application/json` | [InstallCommands](data-types.md#installcommands) |
| 400 | Invalid request (code bad_request) | `application/problem+json` | [Problem](data-types.md#problem) |
| 401 | Not authenticated (code unauthorized) | `application/problem+json` | [Problem](data-types.md#problem) |
| 404 | The installation of hosts from the server is turned off (`install.enabled`; code not_found) | `application/problem+json` | [Problem](data-types.md#problem) |
| 412 | The server cannot give the commands (code precondition_failed; reasons local_build, server_settings) | `application/problem+json` | [Problem](data-types.md#problem) |
| 422 | The request is not acceptable (code validation_failed; the issues name the field in `path`; reasons invalid_value, unknown_attribute, platform_not_supported, variant_not_available) | `application/problem+json` | [Problem](data-types.md#problem) |
| 503 | A bundle is being prepared or failed (code unavailable, reason bundle_preparing or bundle_failed, header Retry-After) | `application/problem+json` | [Problem](data-types.md#problem) |
