---
title: "Data types"
weight: 200
description: "Every schema of the REST API with its fields, types, constraints and values."
---

The named types of the [REST API](_index.md), in alphabetical order. Field names are in the JSON spelling (camelCase). A field marked required is always present; required maps and arrays are never `null`.

## ActionAccepted

| Field | Type | Description |
|---|---|---|
| `accepted` (required) | `boolean` |  |
| `message` | `string` |  |

## ActorType

Type: `string`. Values:

- `user`
- `token`
- `system`
- `gitops`
- `agent`

## Agent

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `kind` (required) | [AgentKind](#agentkind) |  |
| `serviceName` (required) | `string` | Example `otelcol-contrib`. |
| `version` (required) | `string` | Example `0.162.0`. |
| `hostname` (required) | `string` |  |
| `osType` (required) | `string` | Example `windows`. |
| `osDescription` (required) | `string` |  |
| `arch` (required) | `string` | Example `amd64`. |
| `identifyingAttributes` (required) | [StringMap](#stringmap) |  |
| `nonIdentifyingAttributes` (required) | [StringMap](#stringmap) |  |
| `labels` (required) | [StringMap](#stringmap) |  |
| `capabilities` (required) | `integer` | OpAMP AgentCapabilities bitmask. Format `int64`. |
| `connection` (required) | [AgentConnection](#agentconnection) |  |
| `health` | [AgentHealth](#agenthealth) |  |
| `remoteConfig` (required) | [RemoteConfigState](#remoteconfigstate) |  |
| `assignment` (required) | [AgentAssignment](#agentassignment) |  |
| `updater` | [UpdaterLink](#updaterlink) |  |
| `packageStatus` | [PackageStatuses](#packagestatuses) |  |
| `effectiveConfigHash` | [Hash](#hash) |  |
| `effectiveConfigUpdatedAt` | `string` | Format `date-time`. |
| `availableComponentsHash` | [Hash](#hash) |  |
| `enrollmentTokenId` | `string` | Format `uuid`. |
| `firstSeenAt` (required) | `string` | Format `date-time`. |
| `updatedAt` (required) | `string` | Format `date-time`. |

## AgentAssignment

| Field | Type | Description |
|---|---|---|
| `fleetName` | `string` |  |
| `assignedConfigHash` | [Hash](#hash) |  |
| `desiredConfigHash` | [Hash](#hash) |  |
| `failedConfigHash` | [Hash](#hash) |  |
| `targetVersion` | `string` | Effective target version of the fleet (Git target or UI selection; absent while on hold). |
| `error` | `string` |  |
| `assignedAt` | `string` | Format `date-time`. |

## AgentConnection

| Field | Type | Description |
|---|---|---|
| `status` (required) | [ConnectionStatus](#connectionstatus) |  |
| `replicaId` | `string` |  |
| `transport` | `string` | One of `websocket`, `http`. |
| `remoteAddr` | `string` |  |
| `connectedAt` | `string` | Format `date-time`. |
| `disconnectedAt` | `string` | Format `date-time`. |
| `lastSeenAt` | `string` | Format `date-time`. |

## AgentCounts

| Field | Type | Description |
|---|---|---|
| `total` (required) | `integer` |  |
| `collectors` (required) | `integer` |  |
| `updaters` (required) | `integer` |  |
| `online` (required) | `integer` |  |
| `offline` (required) | `integer` |  |
| `healthy` (required) | `integer` |  |
| `unhealthy` (required) | `integer` |  |
| `healthUnknown` (required) | `integer` |  |

## AgentDetail

| Field | Type | Description |
|---|---|---|
| `agent` (required) | [Agent](#agent) |  |
| `configSync` (required) | [ConfigSyncState](#configsyncstate) |  |
| `outdated` (required) | `boolean` |  |
| `latestVersion` | `string` |  |
| `capabilityNames` (required) | array of `string` | Items: example `AcceptsRemoteConfig`. |
| `activeRolloutTargets` (required) | array of [RolloutTarget](#rollouttarget) |  |
| `linkedAgent` | [AgentSummary](#agentsummary) |  |

## AgentEvent

| Field | Type | Description |
|---|---|---|
| `id` (required) | `integer` | Format `int64`. |
| `agentId` (required) | `string` | Format `uuid`. |
| `time` (required) | `string` | Format `date-time`. |
| `type` (required) | `string` | One of `connected`, `disconnected`, `registered`, `description_changed`, `version_changed`, `health_changed`, `config_offered`, `config_applied`, `config_failed`, `assigned`, `package_offered`, `package_status`, `command`, `rollout_target`, `identity_conflict`, `bridge_locked`. |
| `severity` (required) | `string` | One of `info`, `warning`, `error`. |
| `message` (required) | `string` |  |
| `details` | [AnyMap](#anymap) |  |

## AgentEventList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [AgentEvent](#agentevent) |  |
| `nextBefore` (required) | `integer` | Format `int64`. |

## AgentFacets

| Field | Type | Description |
|---|---|---|
| `total` (required) | `integer` |  |
| `status` (required) | array of [FacetValue](#facetvalue) |  |
| `health` (required) | array of [FacetValue](#facetvalue) | values healthy \| unhealthy \| unknown. |
| `kinds` (required) | array of [FacetValue](#facetvalue) |  |
| `versions` (required) | array of [FacetValue](#facetvalue) |  |
| `fleets` (required) | array of [FacetValue](#facetvalue) | value "" = unassigned. |
| `osTypes` (required) | array of [FacetValue](#facetvalue) |  |
| `siteTypes` (required) | array of [FacetValue](#facetvalue) |  |
| `countries` (required) | array of [FacetValue](#facetvalue) |  |
| `configSync` (required) | array of [FacetValue](#facetvalue) |  |

## AgentHealth

| Field | Type | Description |
|---|---|---|
| `healthy` (required) | `boolean` |  |
| `status` | `string` | Example `StatusOK`. |
| `lastError` | `string` |  |
| `startTime` | `string` | Format `date-time`. |
| `components` | map of `string` to [ComponentHealth](#componenthealth) |  |
| `updatedAt` (required) | `string` | Format `date-time`. |

## AgentKind

bridge = the OpAMP Bridge of the OpenTelemetry Operator, recognised by the service.name io.opentelemetry.operator-opamp-bridge or the attribute opentelemetry.io/opamp.bridge.mode. The server sends it remote configuration only when a fleet sets spec.bridge.manage and the Bridge runs in standalone mode, never a restart command, and it is not part of version rollouts.

Type: `string`. Values:

- `collector`
- `updater`
- `bridge`
- `unknown`

## AgentSummary

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `kind` (required) | [AgentKind](#agentkind) |  |
| `hostname` (required) | `string` |  |
| `serviceName` (required) | `string` |  |
| `version` (required) | `string` |  |
| `outdated` (required) | `boolean` |  |
| `osType` (required) | `string` |  |
| `arch` (required) | `string` |  |
| `status` (required) | [ConnectionStatus](#connectionstatus) |  |
| `lastSeenAt` | `string` | Format `date-time`. |
| `healthy` | `boolean` |  |
| `healthStatus` | `string` |  |
| `fleetName` | `string` |  |
| `configSync` (required) | [ConfigSyncState](#configsyncstate) |  |
| `targetVersion` | `string` |  |
| `site` | [StringMap](#stringmap) | site.* attributes (site.type, site.id, site.name, site.country, site.region, site.timezone). |
| `labels` | [StringMap](#stringmap) |  |
| `hasUpdater` (required) | `boolean` |  |

## AgentSummaryPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [AgentSummary](#agentsummary) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## AnyMap

Type: map of `string` to any.

## APIToken

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `name` (required) | `string` |  |
| `displayPrefix` (required) | `string` | Example `myra_3kQ9zXw`. |
| `role` (required) | [Role](#role) |  |
| `createdBy` (required) | `string` |  |
| `createdAt` (required) | `string` | Format `date-time`. |
| `expiresAt` | `string` | Effective expiry: the earlier of the stored expiry and `createdAt` plus `auth.apiTokenMaxTtl`. Absent only for a token without an expiry on a server configured without a maximum. Format `date-time`. |
| `lastUsedAt` | `string` | Format `date-time`. |
| `revokedAt` | `string` | Format `date-time`. |

## APITokenList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [APIToken](#apitoken) |  |

## ApproveWaveRequest

| Field | Type | Description |
|---|---|---|
| `wave` (required) | `integer` | Minimum 0. |
| `reason` | `string` | At most 500 characters. |

## Artifact

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `distribution` (required) | `string` |  |
| `version` (required) | `string` |  |
| `os` (required) | `string` |  |
| `arch` (required) | `string` |  |
| `format` (required) | `string` | One of `tar.gz`, `zip`. |
| `filename` (required) | `string` |  |
| `size` (required) | `integer` | Format `int64`. |
| `sha256` | [Hash](#hash) |  |
| `signatureVerified` (required) | `boolean` |  |
| `signatureIdentity` | `string` |  |
| `verificationError` | `string` |  |
| `sourceUrl` (required) | `string` |  |
| `status` (required) | [ArtifactStatus](#artifactstatus) |  |
| `error` | `string` |  |
| `createdAt` (required) | `string` | Format `date-time`. |
| `updatedAt` (required) | `string` | Format `date-time`. |

## ArtifactList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [Artifact](#artifact) |  |

## ArtifactPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [Artifact](#artifact) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## ArtifactStatus

Type: `string`. Values:

- `pending`
- `downloading`
- `ready`
- `failed`

## AuditEvent

| Field | Type | Description |
|---|---|---|
| `id` (required) | `integer` | Format `int64`. |
| `time` (required) | `string` | Format `date-time`. |
| `actorType` (required) | [ActorType](#actortype) |  |
| `actorId` (required) | `string` |  |
| `actorName` (required) | `string` |  |
| `action` (required) | `string` | Example `rollout.pause`. |
| `targetType` | `string` | Example `rollout`. |
| `targetId` | `string` |  |
| `result` (required) | `string` | One of `success`, `failure`. |
| `details` | [AnyMap](#anymap) |  |
| `sourceIp` | `string` |  |
| `requestId` | `string` |  |

## AuditEventPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [AuditEvent](#auditevent) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## AuthConfigInfo

| Field | Type | Description |
|---|---|---|
| `oidcEnabled` (required) | `boolean` |  |
| `oidcDisplayName` | `string` |  |
| `devLoginEnabled` (required) | `boolean` |  |
| `bootstrapPasswordEnabled` (required) | `boolean` | The bootstrap admin is active and accepts its password (POST /auth/bootstrap-login with username and password). |
| `bootstrapTokenEnabled` (required) | `boolean` | The bootstrap admin is active and accepts a one-time token (POST /auth/bootstrap-login with token). |
| `ui` (required) | [UIConfigInfo](#uiconfiginfo) |  |

## Blueprint

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `description` | `string` |  |
| `labels` | [StringMap](#stringmap) |  |
| `extends` | `string` |  |
| `parameters` | array of [ParameterSpec](#parameterspec) |  |
| `config` | [AnyMap](#anymap) |  |
| `fragments` | array of [Fragment](#fragment) |  |
| `remove` | array of `string` |  |
| `source` (required) | [SourceRef](#sourceref) |  |

## BlueprintDetail

| Field | Type | Description |
|---|---|---|
| `blueprint` (required) | [Blueprint](#blueprint) |  |
| `chain` (required) | array of `string` |  |
| `parameters` (required) | array of [ParameterSpec](#parameterspec) |  |
| `usedBy` (required) | array of `string` |  |
| `issues` (required) | array of [Issue](#issue) |  |
| `webUrl` | `string` |  |

## BlueprintSummary

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `description` | `string` |  |
| `extends` | `string` |  |
| `parameterCount` (required) | `integer` |  |
| `fragmentCount` (required) | `integer` |  |
| `usedBy` (required) | array of `string` |  |
| `source` (required) | [SourceRef](#sourceref) |  |
| `issueCount` (required) | `integer` |  |

## BlueprintSummaryList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [BlueprintSummary](#blueprintsummary) |  |

## BootstrapLoginRequest

Exactly one of password (together with username) and token.

| Field | Type | Description |
|---|---|---|
| `username` | `string` | User name of the bootstrap admin (password sign-in). |
| `password` | `string` | Format `password`. |
| `token` | `string` | One-time token of the bootstrap admin (token sign-in). Format `password`. |

## BridgeSpec

| Field | Type | Description |
|---|---|---|
| `manage` (required) | `boolean` | Opts the fleet in to configure the OpAMP Bridge agents that its selector matches. Without it a Bridge never receives remote configuration from the fleet. The opt-in takes effect for Bridges in standalone mode only. Default `false`. |

## ChangelogEntry

| Field | Type | Description |
|---|---|---|
| `source` (required) | `string` | One of `core`, `contrib`, `releases`. |
| `file` (required) | `string` | Example `CHANGELOG.md`. |
| `version` (required) | `string` |  |
| `component` | `string` | Example `receiver/kafka`. |
| `text` (required) | `string` |  |
| `issues` | array of `integer` |  |

## ChannelInfo

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `type` (required) | `string` | One of `webhook`, `teams`, `slack`. |
| `events` (required) | array of `string` |  |
| `minSeverity` (required) | `string` | One of `info`, `warning`, `critical`. |
| `target` (required) | `string` |  |

## ChannelInfoList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [ChannelInfo](#channelinfo) |  |

## ClearFleetVersionSelectionRequest

| Field | Type | Description |
|---|---|---|
| `reason` | `string` | At most 500 characters. |

## CollectorSpec

| Field | Type | Description |
|---|---|---|
| `distribution` | `string` | Default `otelcol-contrib`. |
| `version` | `string` | Example `0.162.0`. |

## ComponentDetails

| Field | Type | Description |
|---|---|---|
| `module` | `string` |  |
| `metadata` | [StringMap](#stringmap) |  |

## ComponentHealth

| Field | Type | Description |
|---|---|---|
| `healthy` (required) | `boolean` |  |
| `status` | `string` |  |
| `lastError` | `string` |  |
| `startTime` | `string` | Format `date-time`. |
| `statusTime` | `string` | Format `date-time`. |
| `components` | map of `string` to [ComponentHealth](#componenthealth) |  |

## ComponentSet

| Field | Type | Description |
|---|---|---|
| `hash` (required) | [Hash](#hash) |  |
| `components` (required) | map of `string` to map of `string` to [ComponentDetails](#componentdetails) | kind ("receivers", "processors", "exporters", "extensions", "connectors") -> type -> details. |

## ConfigDocument

| Field | Type | Description |
|---|---|---|
| `hash` (required) | [Hash](#hash) |  |
| `contentType` (required) | `string` | Example `text/yaml`. |
| `body` (required) | `string` | YAML text. |
| `updatedAt` | `string` | Format `date-time`. |
| `source` (required) | `string` | One of `effective`, `desired`, `assigned`. |
| `redacted` | `boolean` | Secret-bearing values of an effective config were replaced by "[REDACTED]" (keys such as password, token, secret, api key, authorization, header values, credentials in URLs). The hash identifies the stored original. |

## ConfigSyncState

none = unassigned/no config; in_sync = agent applied the desired config; pending = offered/applying/offline;
failed = agent reported FAILED for the desired config; drift = desired differs from assigned (rollout pending or rolled back).

Type: `string`. Values:

- `none`
- `in_sync`
- `pending`
- `failed`
- `drift`

## ConnectionStatus

Type: `string`. Values:

- `online`
- `offline`

## CreateAPITokenRequest

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` | 1 to 100 characters. |
| `role` (required) | [Role](#role) |  |
| `expiresAt` | `string` | Expiry of the token. Optional: without it the token gets the maximum lifetime `auth.apiTokenMaxTtl` (default 90 days; no expiry only where the server is configured with 0). A later expiry is refused (422). Format `date-time`. |

## CreatedAPIToken

| Field | Type | Description |
|---|---|---|
| `token` (required) | [APIToken](#apitoken) |  |
| `secret` (required) | `string` | Full token "myra_..." - shown only once. |

## CreatedEnrollmentToken

| Field | Type | Description |
|---|---|---|
| `token` (required) | [EnrollmentToken](#enrollmenttoken) |  |
| `secret` (required) | `string` | Full token "myre_..." - shown only once. |

## CreateEnrollmentTokenRequest

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` | 1 to 100 characters. |
| `labels` | [StringMap](#stringmap) |  |
| `expiresAt` | `string` | Format `date-time`. |

## CreateSavedFilterRequest

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` | 1 to 100 characters. |
| `page` (required) | `string` | Example `agents`. |
| `query` (required) | [AnyMap](#anymap) |  |
| `shared` | `boolean` | Default `false`. |

## DatabaseInfo

| Field | Type | Description |
|---|---|---|
| `ok` (required) | `boolean` |  |
| `serverVersion` | `string` |  |
| `migrationVersion` (required) | `integer` | Format `int64`. |

## DevLoginRequest

| Field | Type | Description |
|---|---|---|
| `username` (required) | `string` |  |
| `password` (required) | `string` | Format `password`. |

## Duration

Go duration string

Type: `string`. Example `30s`.

## EnrollmentToken

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `name` (required) | `string` |  |
| `displayPrefix` (required) | `string` |  |
| `labels` | [StringMap](#stringmap) |  |
| `createdBy` (required) | `string` |  |
| `createdAt` (required) | `string` | Format `date-time`. |
| `expiresAt` | `string` | Format `date-time`. |
| `lastUsedAt` | `string` | Format `date-time`. |
| `revokedAt` | `string` | Format `date-time`. |
| `useCount` (required) | `integer` | Format `int64`. |

## EnrollmentTokenList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [EnrollmentToken](#enrollmenttoken) |  |

## ExecutorSummary

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `type` (required) | [ExecutorType](#executortype) |  |
| `description` | `string` |  |
| `target` (required) | `string` | Redacted destination URL. |
| `source` (required) | [SourceRef](#sourceref) |  |
| `usedBy` (required) | array of `string` |  |

## ExecutorSummaryList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [ExecutorSummary](#executorsummary) |  |

## ExecutorType

none = versions are only reported; updater = OpAMP package offers to myriarch-updater; webhook/awx = external
call (Executor object); manual = version rollouts are created and wait for the reported version without any
external call (the version is changed outside of myriarchd, e.g. image tag by merge request).

Type: `string`. Values:

- `none`
- `updater`
- `webhook`
- `awx`
- `manual`

## FacetValue

| Field | Type | Description |
|---|---|---|
| `value` (required) | `string` |  |
| `count` (required) | `integer` |  |

## FailureAction

Type: `string`. Values:

- `pause`
- `rollback`

## FailureThreshold

| Field | Type | Description |
|---|---|---|
| `maxFailed` | `integer` | Minimum 0. |
| `maxFailedPercent` | `integer` | Range 0 to 100. |

## FeatureFlags

| Field | Type | Description |
|---|---|---|
| `oidc` (required) | `boolean` |  |
| `devLogin` (required) | `boolean` |  |
| `bootstrapAdmin` (required) | `boolean` | The bootstrap admin (break-glass account) accepts sign-ins. |
| `gitops` (required) | `boolean` |  |
| `releases` (required) | `boolean` |  |
| `devMode` (required) | `boolean` |  |

## Fleet

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `description` | `string` |  |
| `labels` | [StringMap](#stringmap) |  |
| `displayName` | `string` |  |
| `priority` (required) | `integer` |  |
| `selector` (required) | [Selector](#selector) |  |
| `blueprint` (required) | `string` |  |
| `parameters` | [AnyMap](#anymap) |  |
| `collector` (required) | [CollectorSpec](#collectorspec) |  |
| `update` (required) | [UpdateSpec](#updatespec) |  |
| `rollout` (required) | [FleetRolloutSpec](#fleetrolloutspec) |  |
| `bridge` | [BridgeSpec](#bridgespec) |  |
| `source` (required) | [SourceRef](#sourceref) |  |

## FleetBehind

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | `string` |  |
| `targetVersion` (required) | `string` |  |
| `agents` (required) | `integer` |  |
| `versions` (required) | array of [VersionCount](#versioncount) |  |
| `breakingCount` (required) | `integer` |  |
| `sourcePath` (required) | `string` |  |

## FleetCounts

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | `string` |  |
| `total` (required) | `integer` |  |
| `online` (required) | `integer` |  |
| `healthy` (required) | `integer` |  |
| `inSync` (required) | `integer` |  |
| `pending` (required) | `integer` |  |
| `failed` (required) | `integer` |  |
| `targetVersion` | `string` | Effective target version of the fleet (Git target or UI selection). |
| `onTarget` (required) | `integer` |  |

## FleetDetail

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | [Fleet](#fleet) |  |
| `status` (required) | [FleetStatus](#fleetstatus) |  |
| `blueprintChain` (required) | array of `string` |  |
| `configPolicy` (required) | [RolloutPolicy](#rolloutpolicy) |  |
| `versionPolicy` (required) | [RolloutPolicy](#rolloutpolicy) |  |
| `executor` | [ExecutorSummary](#executorsummary) |  |
| `issues` (required) | array of [Issue](#issue) |  |
| `webUrl` | `string` |  |

## FleetRolloutSpec

| Field | Type | Description |
|---|---|---|
| `configPolicy` | `string` | Default `default`. |
| `versionPolicy` | `string` | Default `default`. |

## FleetStatus

| Field | Type | Description |
|---|---|---|
| `agents` (required) | [FleetCounts](#fleetcounts) |  |
| `versions` (required) | array of [VersionCount](#versioncount) |  |
| `activeRollouts` (required) | array of [RolloutSummary](#rolloutsummary) |  |
| `behindLatest` (required) | `boolean` | The effective target version is lower than the latest stable release. |
| `latestVersion` | `string` |  |
| `renderErrors` (required) | `integer` |  |
| `versionTarget` (required) | [FleetVersionTarget](#fleetversiontarget) |  |

## FleetSummary

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | [Fleet](#fleet) |  |
| `status` (required) | [FleetStatus](#fleetstatus) |  |

## FleetSummaryList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [FleetSummary](#fleetsummary) |  |

## FleetVersionOptions

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | `string` |  |
| `distribution` (required) | `string` | Example `otelcol-contrib`. |
| `allowed` (required) | `boolean` | An operator may select a version now (frame enabled, executor set, no blocking rollout). |
| `notAllowedCode` | `string` | One of `selection_disabled`, `no_executor`, `blocking_rollout`. |
| `notAllowedReason` | `string` |  |
| `target` (required) | [FleetVersionTarget](#fleetversiontarget) |  |
| `executor` (required) | [ExecutorType](#executortype) |  |
| `versionPolicy` (required) | `string` | Name of the rollout policy a selection uses (selection.rolloutPolicy, else spec.rollout.versionPolicy, else "default"). |
| `activeRollout` | [RolloutSummary](#rolloutsummary) |  |
| `options` (required) | array of [VersionOption](#versionoption) |  |
| `platforms` (required) | array of [PlatformCount](#platformcount) | os/arch of the member collectors (artifacts the update needs). |

## FleetVersionSelection

A target version chosen by an operator in the UI/API (one per fleet).

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | `string` |  |
| `version` (required) | `string` | Example `0.162.0`. |
| `state` (required) | [VersionSelectionState](#versionselectionstate) |  |
| `stateReason` | `string` | Why the selection is invalidated (English), e.g. "version 0.162.0 is outside the frame of commit 1a2b3c4 (>=0.161.0, &lt;0.162.0)". |
| `gitVersion` | `string` | spec.collector.version in Git when the selection was made (absent = none). |
| `gitCommit` | `string` | Applied GitOps revision when the selection was made. |
| `selectedBy` (required) | `string` | Display name of the operator (or API token); resolved by the server like RolloutWave.approvedBy. |
| `selectedAt` (required) | `string` | Format `date-time`. |
| `reason` | `string` |  |
| `rolloutId` | `string` | Version rollout created for this selection. Format `uuid`. |
| `plannedAt` | `string` | When the leader planned the fleet for this selection. Format `date-time`. |
| `updatedAt` (required) | `string` | Format `date-time`. |

## FleetVersionTarget

Effective target version of a fleet and where it comes from.

| Field | Type | Description |
|---|---|---|
| `version` | `string` | Effective target version (absent: source none or hold). |
| `source` (required) | [VersionTargetSource](#versiontargetsource) |  |
| `gitVersion` | `string` | spec.collector.version (absent = not set). |
| `selectionEnabled` (required) | `boolean` | spec.update.selection.enabled. |
| `frame` | [VersionSelectionSpec](#versionselectionspec) |  |
| `selection` | [FleetVersionSelection](#fleetversionselection) |  |

## Fragment

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `when` | `string` | Example `[[ eq (attr "os.type") "windows" ]]`. |
| `config` | [AnyMap](#anymap) |  |
| `remove` | array of `string` |  |

## GitCommit

| Field | Type | Description |
|---|---|---|
| `sha` (required) | `string` |  |
| `author` | `string` |  |
| `message` | `string` |  |
| `time` (required) | `string` | Format `date-time`. |

## GitOpsBrief

| Field | Type | Description |
|---|---|---|
| `state` (required) | `string` | One of `ok`, `error`, `syncing`, `never`. |
| `appliedCommit` | `string` |  |
| `issueCount` (required) | `integer` |  |

## GitOpsObjectRef

| Field | Type | Description |
|---|---|---|
| `kind` (required) | [ObjectKind](#objectkind) |  |
| `name` (required) | `string` |  |
| `path` (required) | `string` |  |
| `commit` (required) | `string` |  |
| `webUrl` | `string` |  |

## GitOpsObjectRefList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [GitOpsObjectRef](#gitopsobjectref) |  |

## GitOpsSync

| Field | Type | Description |
|---|---|---|
| `id` (required) | `integer` | Format `int64`. |
| `startedAt` (required) | `string` | Format `date-time`. |
| `finishedAt` | `string` | Format `date-time`. |
| `trigger` (required) | `string` | One of `poll`, `webhook`, `manual`, `startup`. |
| `commit` | [GitCommit](#gitcommit) |  |
| `result` (required) | `string` | One of `applied`, `unchanged`, `invalid`, `error`. |
| `issues` (required) | array of [Issue](#issue) |  |
| `added` | array of `string` |  |
| `changed` | array of `string` |  |
| `removed` | array of `string` |  |

## GitOpsSyncPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [GitOpsSync](#gitopssync) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## GitOpsSyncStatus

| Field | Type | Description |
|---|---|---|
| `source` (required) | `string` |  |
| `branch` | `string` |  |
| `path` | `string` |  |
| `state` (required) | `string` | One of `ok`, `error`, `syncing`, `never`. |
| `lastSyncAt` | `string` | Format `date-time`. |
| `lastSuccessAt` | `string` | Format `date-time`. |
| `appliedCommit` | [GitCommit](#gitcommit) |  |
| `latestCommit` | [GitCommit](#gitcommit) |  |
| `issues` (required) | array of [Issue](#issue) |  |
| `objectCounts` (required) | map of `string` to `integer` | kind -> count. |

## Hash

Lower-case hex encoded hash

Type: `string`. Pattern `^[0-9a-f]*$`.

## InstallBundle

The installer bundle for one platform.

| Field | Type | Description |
|---|---|---|
| `os` (required) | `string` | One of `linux`, `windows`. |
| `arch` (required) | `string` | One of `amd64`, `arm64`. |
| `version` (required) | `string` | The version of the bundle. Example `0.2.0`. |
| `filename` (required) | `string` | Example `myriarch-updater_0.2.0_linux_amd64.tar.gz`. |
| `status` (required) | `string` | One of `pending`, `preparing`, `ready`, `failed`. |
| `sha256` | `string` | The SHA-256 of the archive in lower case hex: its line in the signed checksums.txt of the release. The copied command pins it. Ready only. |
| `size` | `integer` | The size of the archive in bytes. Ready only. Format `int64`. |
| `error` | `string` | Why the last attempt failed. Failed only. |

## InstallBundleList

| Field | Type | Description |
|---|---|---|
| `source` (required) | `string` | release: the bundles of the release that this server runs. directory: the archives of a directory of a development server. none: the server has no bundles. One of `release`, `directory`, `none`. |
| `version` | `string` | The version of the bundles. Not for none. Example `0.2.0`. |
| `reason` | `string` | Why there are none (source none): the server is not a release of the project. One of `local_build`. |
| `items` (required) | array of [InstallBundle](#installbundle) | One bundle for each platform of install.platforms. |

## InstallCommand

| Field | Type | Description |
|---|---|---|
| `platform` (required) | `string` | Example `linux/amd64`. |
| `shell` (required) | `string` | The shell that the command is for, run as root (sudo) or in an elevated Windows PowerShell. One of `bash`, `powershell`. |
| `command` (required) | `string` | The text to copy. Lines end in LF. |
| `bundle` (required) | [InstallBundle](#installbundle) |  |
| `tokenFile` | `string` | The file with the token that the command reads (variant token_file). |

## InstallCommands

| Field | Type | Description |
|---|---|---|
| `variant` (required) | `string` | One of `token_inline`, `token_file`, `lab`. |
| `server` (required) | `string` | The address of the server in the commands (server.publicUrl). Example `https://fleet.example.com`. |
| `version` (required) | `string` | The version of the installer bundles. Example `0.2.0`. |
| `insecure` (required) | `boolean` | True for the variant lab: the commands lower the checks of the installer for a development server. |
| `tokenPlaceholder` | `string` | The text in each command that stands for the enrollment token. The person replaces it, in every command, with the token. Not for the variant token_file. Example `@@MYRIARCH_ENROLLMENT_TOKEN@@`. |
| `commands` (required) | array of [InstallCommand](#installcommand) |  |

## InstallCommandsRequest

The request for install commands. It holds no secret. `attributes` may hold `site.type` and `site.id` (required), `site.name`, `site.country`,
`site.region`, `site.timezone` and `deployment.environment.name`: the options of the installers for the site.

| Field | Type | Description |
|---|---|---|
| `variant` | `string` | The default is token_inline. lab is for development servers only. One of `token_inline`, `token_file`, `lab`. Example `token_inline`. |
| `platforms` | array of `string` | The platforms (os/arch) to render a command for, each of install.platforms. Without any, all of them. At most 8 items. Items: example `linux/amd64`. |
| `fleet` | `string` | The fleet that the person expects. The installer passes it to the plan, and the server refuses a host that it assigns elsewhere (fleet_mismatch). Example `linux-hosts`. |
| `attributes` | [StringMap](#stringmap) |  |
| `collectorVersion` | `string` | A collector version that the host asks for. The plan accepts it inside the Git frame of the fleet only. Example `0.162.0`. |
| `tokenFiles` | [InstallTokenFiles](#installtokenfiles) |  |
| `caFile` | `string` | The path of the PEM file with the CA of the server on a Linux host. A Windows host uses the certificate store of the machine. Example `/etc/ssl/myriarch-ca.pem`. |

## InstallPlan

The plan for a host, as JSON. The text form has the same content in the lines `format`, `fleet`, `version.source`, `opamp.endpoint`, `collector.version`, `collector.url`, `collector.grant`, `supervisor.version`, `supervisor.url`, `supervisor.grant` and `updater.install`.

| Field | Type | Description |
|---|---|---|
| `format` (required) | `integer` | One of `1`. |
| `fleet` (required) | `string` | The fleet that the host would join. Example `linux-hosts`. |
| `version` (required) | [InstallPlanVersion](#installplanversion) |  |
| `opamp` (required) | [InstallPlanOpAMP](#installplanopamp) |  |
| `collector` (required) | [InstallPlanArtifact](#installplanartifact) |  |
| `supervisor` (required) | [InstallPlanArtifact](#installplanartifact) |  |
| `updater` (required) | [InstallPlanUpdater](#installplanupdater) |  |

## InstallPlanArtifact

| Field | Type | Description |
|---|---|---|
| `version` (required) | `string` | The version without "v". Example `0.162.0`. |
| `url` (required) | `string` | The URL of the artifact on the origin of the server. Add ".sha256" and ".sigstore.json" for the checksum and the Sigstore bundle. |
| `grant` (required) | `string` | The install grant (a "myrd_" token) for this artifact, sent as a bearer token. It is bound to the enrollment token of the request and expires after install.grantTtl. |

## InstallPlanOpAMP

| Field | Type | Description |
|---|---|---|
| `endpoint` (required) | `string` | The OpAMP endpoint of the host: wss:// for an https public URL (ws:// for the http URL of a development server). Example `wss://fleet.example.com/v1/opamp`. |

## InstallPlanRequest

The request for a plan. It holds no secret. `attributes` may only hold `host.name`, `site.type`, `site.id`, `site.name`,
`site.country`, `site.region`, `site.timezone` and `deployment.environment.name` (values of 1 to 253 characters without control
characters or surrounding spaces); `os.type` and `host.arch` come from `os` and `arch`, and the labels come from the enrollment token.

| Field | Type | Description |
|---|---|---|
| `os` (required) | `string` | One of `linux`, `windows`. Example `linux`. |
| `arch` (required) | `string` | amd64 or arm64; aliases such as x86_64 and aarch64 are accepted. Windows is amd64 only. Example `amd64`. |
| `attributes` | [StringMap](#stringmap) |  |
| `fleet` | `string` | The fleet that the person expects. The server decides; a fleet that does not agree is refused with 409 fleet_mismatch. Example `linux-hosts`. |
| `collectorVersion` | `string` | A collector version without "v" that the host asks for, accepted inside the Git frame of the fleet only. Example `0.162.0`. |

## InstallPlanUpdater

| Field | Type | Description |
|---|---|---|
| `install` (required) | `boolean` | Whether the myriarch-updater is installed: the fleet updates its collectors through the updater executor. |

## InstallPlanVersion

| Field | Type | Description |
|---|---|---|
| `source` (required) | `string` | Where the version comes from: spec.collector.version (git), the choice in the UI (ui), the version before the running version rollout (rollout) or the version that the host asked for (request). One of `git`, `ui`, `rollout`, `request`. |

## InstallTokenFiles

The paths of the file with the token on the host (variant token_file), for each operating system that is rendered.

| Field | Type | Description |
|---|---|---|
| `linux` | `string` | Example `/etc/myriarch/enrollment-token`. |
| `windows` | `string` | Example `C:\ProgramData\Secrets\myriarch-token.txt`. |

## Issue

| Field | Type | Description |
|---|---|---|
| `severity` (required) | [IssueSeverity](#issueseverity) |  |
| `code` (required) | `string` | Example `unknown_component`. |
| `message` (required) | `string` |  |
| `file` | `string` |  |
| `line` | `integer` |  |
| `path` | `string` | Example `service::pipelines::logs`. |
| `object` | `string` | Example `Fleet/edge-nl`. |

## IssueSeverity

Type: `string`. Values:

- `error`
- `warning`

## LiveAgentRemovedData

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |

## LiveFleetUpdatedData

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` | Fleet name. |

## MaintenanceWindow

| Field | Type | Description |
|---|---|---|
| `days` | array of `string` | Items: one of `Mon`, `Tue`, `Wed`, `Thu`, `Fri`, `Sat`, `Sun`. |
| `start` (required) | `string` | Pattern `^([01][0-9]\|2[0-3]):[0-5][0-9]$`. Example `21:30`. |
| `end` (required) | `string` | Pattern `^([01][0-9]\|2[0-3]):[0-5][0-9]$`. Example `05:30`. |
| `timezone` | `string` | Default `agent`. Example `Europe/Brussels`. |

## MatchFleetsRequest

| Field | Type | Description |
|---|---|---|
| `agentId` | `string` | Format `uuid`. |
| `attributes` | [StringMap](#stringmap) |  |

## MatchFleetsResponse

| Field | Type | Description |
|---|---|---|
| `fleet` | `string` |  |
| `candidates` (required) | array of `string` |  |
| `error` | `string` |  |
| `inventoryLabels` | [StringMap](#stringmap) |  |

## Me

| Field | Type | Description |
|---|---|---|
| `type` (required) | `string` | One of `user`, `token`. |
| `id` (required) | `string` |  |
| `name` (required) | `string` |  |
| `email` | `string` |  |
| `role` (required) | [Role](#role) |  |
| `authMethod` (required) | `string` | One of `oidc`, `local`, `bootstrap`, `api_token`. |
| `permissions` (required) | array of `string` | fleets:set_version (operator+): select/clear the UI target version of a fleet within the Git frame. Items: one of `read`, `saved_filters:write`, `agents:operate`, `agents:delete`, `rollouts:operate`, `fleets:set_version`, `releases:acknowledge`, `gitops:sync`, `artifacts:manage`, `notifications:test`, `audit:read`, `tokens:manage`, `enrollment:manage`, `users:manage`. |
| `csrfToken` | `string` |  |

## ObjectKind

Type: `string`. Values:

- `Fleet`
- `Blueprint`
- `RolloutPolicy`
- `Executor`
- `Inventory`

## OutdatedSummary

| Field | Type | Description |
|---|---|---|
| `latestVersion` | `string` |  |
| `count` (required) | `integer` |  |
| `percent` (required) | `number` |  |

## OverviewStats

| Field | Type | Description |
|---|---|---|
| `generatedAt` (required) | `string` | Format `date-time`. |
| `agents` (required) | [AgentCounts](#agentcounts) |  |
| `versions` (required) | array of [VersionCount](#versioncount) |  |
| `outdated` (required) | [OutdatedSummary](#outdatedsummary) |  |
| `configSync` (required) | array of [FacetValue](#facetvalue) |  |
| `fleets` (required) | array of [FleetCounts](#fleetcounts) |  |
| `unassigned` (required) | `integer` |  |
| `activeRollouts` (required) | array of [RolloutSummary](#rolloutsummary) |  |
| `latestRelease` | [ReleaseSummary](#releasesummary) |  |
| `gitops` (required) | [GitOpsBrief](#gitopsbrief) |  |

## PackageInstallStatus

Type: `string`. Values:

- `installed`
- `install_pending`
- `installing`
- `install_failed`
- `downloading`

## PackageStatus

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `agentHasVersion` | `string` |  |
| `agentHasHash` | [Hash](#hash) |  |
| `serverOfferedVersion` | `string` |  |
| `serverOfferedHash` | [Hash](#hash) |  |
| `status` (required) | [PackageInstallStatus](#packageinstallstatus) |  |
| `errorMessage` | `string` |  |
| `downloadPercent` | `number` |  |

## PackageStatuses

| Field | Type | Description |
|---|---|---|
| `serverProvidedAllPackagesHash` | [Hash](#hash) |  |
| `errorMessage` | `string` |  |
| `packages` (required) | map of `string` to [PackageStatus](#packagestatus) |  |
| `updatedAt` (required) | `string` | Format `date-time`. |

## ParameterSpec

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `type` (required) | [ParameterType](#parametertype) |  |
| `description` | `string` |  |
| `default` | any |  |
| `required` | `boolean` |  |
| `enum` | array of any |  |
| `pattern` | `string` |  |
| `min` | `number` |  |
| `max` | `number` |  |
| `fromAttribute` | `string` |  |

## ParameterType

Type: `string`. Values:

- `string`
- `int`
- `number`
- `bool`
- `duration`
- `list`
- `map`

## PlatformCount

| Field | Type | Description |
|---|---|---|
| `os` (required) | `string` | Example `linux`. |
| `arch` (required) | `string` | Example `amd64`. |
| `agents` (required) | `integer` |  |

## PrefetchRequest

| Field | Type | Description |
|---|---|---|
| `distribution` (required) | `string` | Example `otelcol-contrib`. |
| `version` (required) | `string` | Example `0.162.0`. |
| `platforms` | array of `string` | Platforms (os/arch) to fetch. Omitted or empty means the server default: the configured artifacts.prefetchPlatforms (empty by default); when none are configured either, the distinct platforms of the connected collectors of the fleets that use this distribution. The request fails with 422 if that set is empty too. Items: example `windows/amd64`. |

## Problem

| Field | Type | Description |
|---|---|---|
| `type` (required) | `string` | URI reference ("about:blank"). |
| `title` (required) | `string` |  |
| `status` (required) | `integer` |  |
| `detail` | `string` |  |
| `instance` | `string` |  |
| `code` (required) | `string` | One of `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `precondition_failed`, `validation_failed`, `too_many_requests`, `internal`, `not_implemented`, `unavailable`, `csrf_failed`. |
| `requestId` | `string` |  |
| `issues` | array of [Issue](#issue) |  |

## ProgressSource

updater = steps reported by the myriarch-updater (health component "dev.myriarch.update"); package_status = only the
OpAMP package status of the updater is known (older or simulated updater without the component); executor =
webhook/awx/manual executors (no host-side steps: offered, then waiting for the reported version).

Type: `string`. Values:

- `updater`
- `package_status`
- `executor`

## Release

| Field | Type | Description |
|---|---|---|
| `distribution` (required) | `string` |  |
| `version` (required) | `string` |  |
| `tag` (required) | `string` |  |
| `publishedAt` | `string` | Format `date-time`. |
| `prerelease` (required) | `boolean` |  |
| `htmlUrl` | `string` |  |
| `notes` | `string` |  |
| `breakingChanges` (required) | array of [ChangelogEntry](#changelogentry) |  |
| `deprecations` (required) | array of [ChangelogEntry](#changelogentry) |  |
| `assets` (required) | array of [ReleaseAsset](#releaseasset) |  |
| `detectedAt` (required) | `string` | Format `date-time`. |
| `acknowledgedBy` | `string` | Display name of the user (or API token) that acknowledged the release; resolved by the server like RolloutWave.approvedBy. |
| `acknowledgedAt` | `string` | Format `date-time`. |

## ReleaseAsset

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `url` (required) | `string` |  |
| `size` (required) | `integer` | Format `int64`. |
| `os` | `string` |  |
| `arch` | `string` |  |
| `format` | `string` | One of `tar.gz`, `zip`, `msi`, `deb`, `rpm`, `apk`, `binary`. |
| `contentType` | `string` |  |

## ReleaseDetail

| Field | Type | Description |
|---|---|---|
| `release` (required) | [Release](#release) |  |
| `isLatest` (required) | `boolean` |  |
| `fleetsBehind` (required) | array of [FleetBehind](#fleetbehind) |  |
| `agentsBehind` (required) | `integer` |  |
| `gitopsSnippet` (required) | `string` | YAML snippet setting spec.collector.version. |
| `updatableFleets` (required) | array of [ReleaseFleetOption](#releasefleetoption) | Every fleet of the release's distribution and whether this release can be selected for it in the UI (selectable first, then by name). |

## ReleaseFleetOption

| Field | Type | Description |
|---|---|---|
| `fleet` (required) | `string` |  |
| `displayName` | `string` |  |
| `target` (required) | [FleetVersionTarget](#fleetversiontarget) |  |
| `selectable` (required) | `boolean` |  |
| `reasonCode` | `string` | One of `selection_disabled`, `no_executor`, `blocking_rollout`, `current_target`, `outside_frame`, `downgrade_not_allowed`, `prerelease_not_allowed`, `unknown_version`. |
| `reason` | `string` |  |
| `agentsToUpdate` (required) | `integer` |  |

## ReleaseSummary

| Field | Type | Description |
|---|---|---|
| `distribution` (required) | `string` |  |
| `version` (required) | `string` |  |
| `tag` (required) | `string` |  |
| `publishedAt` | `string` | Format `date-time`. |
| `prerelease` (required) | `boolean` |  |
| `htmlUrl` | `string` |  |
| `breakingCount` (required) | `integer` |  |
| `deprecationCount` (required) | `integer` |  |
| `acknowledged` (required) | `boolean` |  |
| `fleetsBehind` (required) | `integer` |  |
| `isLatest` (required) | `boolean` |  |

## ReleaseSummaryPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [ReleaseSummary](#releasesummary) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## RemoteConfigState

| Field | Type | Description |
|---|---|---|
| `status` (required) | [RemoteConfigStatus](#remoteconfigstatus) |  |
| `lastHash` | [Hash](#hash) |  |
| `errorMessage` | `string` |  |
| `updatedAt` | `string` | Format `date-time`. |

## RemoteConfigStatus

Type: `string`. Values:

- `unset`
- `applying`
- `applied`
- `failed`

## RenderRequest

| Field | Type | Description |
|---|---|---|
| `agentId` | `string` | Format `uuid`. |
| `attributes` | [StringMap](#stringmap) |  |
| `fleet` | `string` |  |
| `parameters` | [AnyMap](#anymap) |  |
| `validate` | `boolean` | false skips only the check against the agent's reported component list (unknown_component); structural checks always run. Default `true`. |

## RenderResponse

| Field | Type | Description |
|---|---|---|
| `blueprint` (required) | `string` |  |
| `fleet` | `string` |  |
| `layers` (required) | array of `string` |  |
| `parameters` (required) | [AnyMap](#anymap) |  |
| `parameterSources` (required) | map of `string` to `string` | Values: one of `default`, `fleet`, `attribute`, `inventory`, `override`. |
| `hash` | [Hash](#hash) |  |
| `body` (required) | `string` |  |
| `issues` (required) | array of [Issue](#issue) |  |

## Replica

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` |  |
| `hostname` (required) | `string` |  |
| `version` (required) | `string` |  |
| `startedAt` (required) | `string` | Format `date-time`. |
| `lastHeartbeatAt` (required) | `string` | Format `date-time`. |
| `isLeader` (required) | `boolean` |  |
| `connections` (required) | `integer` |  |

## Role

Type: `string`. Values:

- `viewer`
- `operator`
- `admin`

## Rollout

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `kind` (required) | [RolloutKind](#rolloutkind) |  |
| `fleetName` (required) | `string` |  |
| `policyName` (required) | `string` |  |
| `policy` (required) | [RolloutPolicy](#rolloutpolicy) |  |
| `status` (required) | [RolloutStatus](#rolloutstatus) |  |
| `statusReason` | `string` |  |
| `fromRef` | `string` |  |
| `toRef` (required) | `string` | Git commit (config rollouts) or version (version rollouts). |
| `gitCommit` | `string` |  |
| `executor` | [ExecutorType](#executortype) |  |
| `currentWave` (required) | `integer` |  |
| `waves` (required) | array of [RolloutWave](#rolloutwave) |  |
| `counts` (required) | [RolloutCounts](#rolloutcounts) |  |
| `createdBy` (required) | `string` | "gitops", "system", or the display name of the operator/API token for UI-triggered rollouts (stored as "user:&lt;uuid>"/"token:&lt;uuid>", resolved like RolloutWave.approvedBy). Example `gitops`. |
| `origin` (required) | [RolloutOrigin](#rolloutorigin) |  |
| `requestReason` | `string` | Reason the operator gave when selecting the version (origin ui). |
| `createdAt` (required) | `string` | Format `date-time`. |
| `startedAt` | `string` | Format `date-time`. |
| `updatedAt` (required) | `string` | Format `date-time`. |
| `finishedAt` | `string` | Format `date-time`. |
| `annotations` | [AnyMap](#anymap) | Engine annotations (persisted state, values may be objects): executorRef, distribution, updateParams (object), selection (object, origin ui: {version, selectedBy, selectedAt, reason}), ackedFailures, healthTrack, rollbackStartedAt, rollbackSent (internal). Clients must not assume string values. |

## RolloutActionRequest

| Field | Type | Description |
|---|---|---|
| `reason` | `string` | At most 500 characters. |

## RolloutCounts

| Field | Type | Description |
|---|---|---|
| `total` (required) | `integer` |  |
| `pending` (required) | `integer` |  |
| `waitingWindow` (required) | `integer` |  |
| `inProgress` (required) | `integer` |  |
| `succeeded` (required) | `integer` |  |
| `failed` (required) | `integer` |  |
| `skipped` (required) | `integer` |  |
| `cancelled` (required) | `integer` |  |
| `rolledBack` (required) | `integer` |  |

## RolloutKind

Type: `string`. Values:

- `config`
- `version`

## RolloutOrigin

What caused the rollout: gitops = the goal comes from Git (new commit, `spec.collector.version`; also after an
operator cleared a UI selection - the clear itself is in the audit log), ui = the target version was selected by
an operator in the UI/API (`selectFleetVersion`), system = anything else. Derived from createdBy for rollouts
created before this field existed ("gitops" -> gitops, "user:"/"token:" -> ui, else system).

Type: `string`. Values:

- `gitops`
- `ui`
- `system`

## RolloutPolicy

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `description` | `string` |  |
| `labels` | [StringMap](#stringmap) |  |
| `waves` (required) | array of [WaveSpec](#wavespec) |  |
| `maxConcurrent` | `integer` |  |
| `healthWait` | [Duration](#duration) |  |
| `applyTimeout` | [Duration](#duration) |  |
| `offlineGracePeriod` | [Duration](#duration) |  |
| `failureThreshold` (required) | [FailureThreshold](#failurethreshold) |  |
| `onFailure` | [FailureAction](#failureaction) |  |
| `maintenanceWindows` | array of [MaintenanceWindow](#maintenancewindow) |  |
| `timezoneAttribute` | `string` | Default `site.timezone`. |
| `defaultTimezone` | `string` |  |
| `source` (required) | [SourceRef](#sourceref) |  |

## RolloutPolicyList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [RolloutPolicy](#rolloutpolicy) |  |

## RolloutStatus

Type: `string`. Values:

- `pending`
- `running`
- `waiting_approval`
- `paused`
- `completed`
- `failed`
- `aborted`
- `rolling_back`
- `rolled_back`
- `superseded`

## RolloutSummary

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `kind` (required) | [RolloutKind](#rolloutkind) |  |
| `fleetName` (required) | `string` |  |
| `policyName` (required) | `string` |  |
| `status` (required) | [RolloutStatus](#rolloutstatus) |  |
| `statusReason` | `string` |  |
| `fromRef` | `string` |  |
| `toRef` (required) | `string` |  |
| `gitCommit` | `string` |  |
| `currentWave` (required) | `integer` |  |
| `waveCount` (required) | `integer` |  |
| `counts` (required) | [RolloutCounts](#rolloutcounts) |  |
| `origin` (required) | [RolloutOrigin](#rolloutorigin) |  |
| `createdBy` | `string` | Same as Rollout.createdBy (display name). |
| `createdAt` (required) | `string` | Format `date-time`. |
| `updatedAt` (required) | `string` | Format `date-time`. |
| `finishedAt` | `string` | Format `date-time`. |

## RolloutSummaryPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [RolloutSummary](#rolloutsummary) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## RolloutTarget

| Field | Type | Description |
|---|---|---|
| `rolloutId` (required) | `string` | Format `uuid`. |
| `agentId` (required) | `string` | Format `uuid`. |
| `hostname` | `string` |  |
| `wave` (required) | `integer` |  |
| `status` (required) | [TargetStatus](#targetstatus) |  |
| `fromRef` | `string` |  |
| `toRef` (required) | `string` |  |
| `attempts` (required) | `integer` |  |
| `startedAt` | `string` | Format `date-time`. |
| `verifiedAt` | `string` | Format `date-time`. |
| `finishedAt` | `string` | Format `date-time`. |
| `lastError` | `string` |  |
| `executorRef` | `string` |  |
| `updatedAt` (required) | `string` | Format `date-time`. |
| `progress` | [TargetProgress](#targetprogress) |  |

## RolloutTargetPage

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [RolloutTarget](#rollouttarget) |  |
| `total` (required) | `integer` |  |
| `limit` (required) | `integer` |  |
| `offset` (required) | `integer` |  |

## RolloutWave

| Field | Type | Description |
|---|---|---|
| `index` (required) | `integer` |  |
| `name` (required) | `string` |  |
| `selector` | [Selector](#selector) |  |
| `percent` | `integer` | Percentage waves only: cumulative share of all targets of the rollout that is reached with this wave (1 to 100). Not progress. |
| `requireApproval` (required) | `boolean` |  |
| `status` (required) | [WaveStatus](#wavestatus) |  |
| `approvedBy` | `string` | Display name of the user (or API token) that approved the wave; the server resolves the stored actor reference. Unresolvable references (deleted users) are returned as stored ("user:&lt;id>"). |
| `approvedAt` | `string` | Format `date-time`. |
| `startedAt` | `string` | Format `date-time`. |
| `finishedAt` | `string` | Format `date-time`. |
| `counts` (required) | [RolloutCounts](#rolloutcounts) |  |

## SavedFilter

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `userId` (required) | `string` | Format `uuid`. |
| `name` (required) | `string` |  |
| `page` (required) | `string` | Example `agents`. |
| `query` (required) | [AnyMap](#anymap) |  |
| `shared` (required) | `boolean` |  |
| `createdAt` (required) | `string` | Format `date-time`. |

## SavedFilterList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [SavedFilter](#savedfilter) |  |

## SelectFleetVersionRequest

| Field | Type | Description |
|---|---|---|
| `version` (required) | `string` | Pattern `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`. Example `0.162.0`. |
| `reason` | `string` | At most 500 characters. |
| `expectedTargetVersion` | `string` | Optimistic concurrency - the effective target the operator saw ("" = none). Mismatch -> 409. |

## SelectFleetVersionResponse

| Field | Type | Description |
|---|---|---|
| `target` (required) | [FleetVersionTarget](#fleetversiontarget) |  |
| `planned` (required) | `boolean` | The leader planned the fleet for this selection within the wait time. |
| `rolloutId` | `string` | Format `uuid`. |
| `targets` | `integer` | Targets of the rollout (when rolloutId is set). |
| `message` | `string` | English hint, e.g. "no collector of the fleet needs an update". |

## Selector

All matchAttributes and matchExpressions must match (AND). Empty selector matches all agents.

| Field | Type | Description |
|---|---|---|
| `matchAttributes` | [StringMap](#stringmap) |  |
| `matchExpressions` | array of [SelectorRequirement](#selectorrequirement) |  |

## SelectorOperator

Type: `string`. Values:

- `In`
- `NotIn`
- `Exists`
- `DoesNotExist`
- `Glob`

## SelectorRequirement

| Field | Type | Description |
|---|---|---|
| `key` (required) | `string` | Example `site.type`. |
| `operator` (required) | [SelectorOperator](#selectoroperator) |  |
| `values` | array of `string` |  |

## SettingsSummary

| Field | Type | Description |
|---|---|---|
| `gitopsSource` (required) | `string` |  |
| `gitopsBranch` (required) | `string` |  |
| `gitopsPollInterval` (required) | [Duration](#duration) |  |
| `releasesRepo` (required) | `string` |  |
| `releasesPollInterval` (required) | [Duration](#duration) |  |
| `trackedDistributions` (required) | array of `string` |  |
| `artifactSource` (required) | `string` |  |
| `signatureVerification` (required) | `boolean` |  |
| `defaultTimezone` (required) | `string` |  |
| `offlineAfter` (required) | [Duration](#duration) |  |
| `retentionAgentEvents` (required) | [Duration](#duration) |  |
| `retentionAudit` (required) | [Duration](#duration) |  |

## SourceRef

| Field | Type | Description |
|---|---|---|
| `path` (required) | `string` | Example `fleets/edge-nl.yaml`. |
| `commit` (required) | `string` |  |
| `contentHash` | [Hash](#hash) |  |

## StringMap

Type: map of `string` to `string`.

## SystemInfo

| Field | Type | Description |
|---|---|---|
| `version` (required) | `string` |  |
| `commit` (required) | `string` |  |
| `buildDate` (required) | `string` |  |
| `replicaId` (required) | `string` |  |
| `leaderId` | `string` |  |
| `replicas` (required) | array of [Replica](#replica) |  |
| `publicUrl` (required) | `string` |  |
| `opampEndpoint` (required) | `string` | Example `wss://fleet.example.com/v1/opamp`. |
| `database` (required) | [DatabaseInfo](#databaseinfo) |  |
| `features` (required) | [FeatureFlags](#featureflags) |  |
| `settings` (required) | [SettingsSummary](#settingssummary) |  |

## TargetProgress

Live progress of a version-rollout target, derived by the rollout engine (leader) from the target, the OpAMP
package status of the linked myriarch-updater and the myriarch-updater's update-phase health component
"dev.myriarch.update" (docs/design/version-selection.md, Target progress, and docs/design/updater-protocol.md,
Update health component). Refreshed on every engine tick after a change (about 1-2 s) and frozen when the
target reaches a final status. Absent for config rollouts and for targets that were never started.

| Field | Type | Description |
|---|---|---|
| `phase` (required) | [UpdatePhase](#updatephase) |  |
| `source` (required) | [ProgressSource](#progresssource) |  |
| `attempt` | `integer` | Target attempt this progress belongs to. |
| `fromVersion` | `string` |  |
| `toVersion` | `string` |  |
| `downloadPercent` | `number` | Range 0 to 100. |
| `error` | `string` | Error of the failed step or of the target (English). |
| `steps` (required) | array of [UpdateStep](#updatestep) | Fixed order offered, downloading, verifying, installing, restarting, health_check, rolling_back (only when a rollback happened), health_wait. Executor source: offered, installing (external update until the version is reported), health_wait. |
| `updatedAt` (required) | `string` | Format `date-time`. |

## TargetStatus

Type: `string`. Values:

- `pending`
- `waiting_window`
- `in_progress`
- `verifying`
- `succeeded`
- `failed`
- `skipped`
- `cancelled`
- `rolled_back`

## UIConfigInfo

Language options of the web UI (ui.* in the server configuration). Contains no secrets.

| Field | Type | Description |
|---|---|---|
| `defaultLanguage` (required) | `string` | BCP 47 tag the UI uses when the user has no stored choice and no browser language matches a shipped locale. |
| `languages` | array of `string` | Restricts the languages the UI offers. Entries are BCP 47 tags and match a shipped language by the full tag or the primary subtag ("de-DE" enables "de"). Omitted or empty means every language the UI ships. |

## UpdatePhase

Current phase of a version update on one target. offered = the update was handed to the executor (package
offered to the myriarch-updater / external call accepted) and the host has not started yet; downloading ..
health_check, rolling_back = reported by the myriarch-updater; health_wait = the collector reports the target
version and myriarchd's health wait (policy healthWait) runs; succeeded / failed / cancelled = final target state;
rolled_back = the host runs the previous version again (automatic rollback by the myriarch-updater, target
status failed, or rollout rollback, target status rolled_back).

Type: `string`. Values:

- `offered`
- `downloading`
- `verifying`
- `installing`
- `restarting`
- `health_check`
- `rolling_back`
- `health_wait`
- `succeeded`
- `failed`
- `rolled_back`
- `cancelled`

## UpdaterLink

| Field | Type | Description |
|---|---|---|
| `agentId` (required) | `string` | Format `uuid`. |
| `online` (required) | `boolean` |  |
| `ambiguous` | `boolean` | Collector side only - more than one online myriarch-updater with the same credential claims this collector; package offers are not made in that state. |

## UpdateSpec

| Field | Type | Description |
|---|---|---|
| `executor` | [ExecutorType](#executortype) |  |
| `executorRef` | `string` |  |
| `params` | [AnyMap](#anymap) |  |
| `selection` | [VersionSelectionSpec](#versionselectionspec) |  |

## UpdateStep

| Field | Type | Description |
|---|---|---|
| `name` (required) | [UpdateStepName](#updatestepname) |  |
| `status` (required) | [UpdateStepStatus](#updatestepstatus) |  |
| `startedAt` | `string` | Format `date-time`. |
| `finishedAt` | `string` | Format `date-time`. |
| `detail` | `string` | Short English detail, e.g. "sha256 + sigstore verified", or the error of a failed step. |

## UpdateStepName

Type: `string`. Values:

- `offered`
- `downloading`
- `verifying`
- `installing`
- `restarting`
- `health_check`
- `rolling_back`
- `health_wait`

## UpdateStepStatus

Type: `string`. Values:

- `pending`
- `running`
- `done`
- `failed`
- `skipped`

## UpdateUserRequest

| Field | Type | Description |
|---|---|---|
| `role` | [Role](#role) |  |
| `disabled` | `boolean` |  |

## User

| Field | Type | Description |
|---|---|---|
| `id` (required) | `string` | Format `uuid`. |
| `subject` (required) | `string` |  |
| `provider` (required) | `string` | One of `oidc`, `local`, `bootstrap`. |
| `email` | `string` |  |
| `name` | `string` |  |
| `role` (required) | [Role](#role) |  |
| `roleSource` (required) | `string` | One of `mapping`, `manual`. |
| `disabled` (required) | `boolean` |  |
| `createdAt` (required) | `string` | Format `date-time`. |
| `lastLoginAt` | `string` | Format `date-time`. |

## UserList

| Field | Type | Description |
|---|---|---|
| `items` (required) | array of [User](#user) |  |

## VersionComparison

| Field | Type | Description |
|---|---|---|
| `distribution` (required) | `string` |  |
| `from` (required) | `string` |  |
| `to` (required) | `string` |  |
| `versions` (required) | array of `string` |  |
| `breakingChanges` (required) | array of [ChangelogEntry](#changelogentry) |  |
| `deprecations` (required) | array of [ChangelogEntry](#changelogentry) |  |

## VersionCount

| Field | Type | Description |
|---|---|---|
| `version` (required) | `string` |  |
| `count` (required) | `integer` |  |
| `outdated` (required) | `boolean` |  |

## VersionOption

| Field | Type | Description |
|---|---|---|
| `version` (required) | `string` |  |
| `selectable` (required) | `boolean` |  |
| `reasonCode` | `string` | Why the version cannot be selected (absent when selectable). One of `current_target`, `outside_frame`, `downgrade_not_allowed`, `prerelease_not_allowed`, `unknown_version`. |
| `reason` | `string` | English explanation of reasonCode. |
| `known` (required) | `boolean` | The release tracker knows this release. |
| `prerelease` (required) | `boolean` |  |
| `isLatest` (required) | `boolean` | Latest stable release of the distribution. |
| `isCurrentTarget` (required) | `boolean` |  |
| `isGitVersion` (required) | `boolean` | Equals spec.collector.version. |
| `isDowngrade` (required) | `boolean` | Lower than the current effective target. |
| `publishedAt` | `string` | Format `date-time`. |
| `agentsToUpdate` (required) | `integer` | Member collectors whose reported version differs from this version. |
| `breakingCount` (required) | `integer` | Breaking changes between the current effective target (else the lowest member version) and this version; 0 if unknown. |
| `deprecationCount` (required) | `integer` |  |

## VersionSelectionSpec

`spec.update.selection` in Git: the frame within which operators may choose the target version of the fleet
in the UI/API (docs/concepts/fleets.md). Exactly one of versionRange / versions is set when enabled.

| Field | Type | Description |
|---|---|---|
| `enabled` (required) | `boolean` | UI/API-triggered version updates are allowed (default false). |
| `versionRange` | `string` | Semantic version constraint (Masterminds semver v3 syntax, versions without "v"). Example `>=0.161.0, <=0.162.0`. |
| `versions` | array of `string` | Explicit list of selectable versions (alternative to versionRange). Items: example `0.162.0`. |
| `allowDowngrade` (required) | `boolean` | Versions lower than the current effective target may be selected (default false). |
| `requireReason` (required) | `boolean` | A reason is mandatory when selecting or clearing (default false). |
| `rolloutPolicy` | `string` | RolloutPolicy for rollouts caused by a UI selection (default spec.rollout.versionPolicy). |

## VersionSelectionState

Type: `string`. Values:

- `active`
- `invalidated`

## VersionTargetSource

Where the effective target version of a fleet comes from: git = `spec.collector.version`; ui = an active UI
selection; hold = a UI selection that a later Git change excluded from the frame (no target, no new version
rollouts until an operator selects an allowed version, clears the selection or Git changes
`spec.collector.version`); none = no target (no Git version, no selection).

Type: `string`. Values:

- `git`
- `ui`
- `hold`
- `none`

## WaveSpec

| Field | Type | Description |
|---|---|---|
| `name` (required) | `string` |  |
| `selector` | [Selector](#selector) |  |
| `percent` | `integer` | Range 1 to 100. |
| `requireApproval` | `boolean` |  |

## WaveStatus

Type: `string`. Values:

- `pending`
- `waiting_approval`
- `running`
- `completed`
- `failed`
- `skipped`
