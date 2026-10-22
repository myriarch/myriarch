// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Convenience aliases for the generated OpenAPI types (src/api/schema.d.ts).
 * No DTO is declared by hand: every type here is derived from `components["schemas"]`
 * or `operations`, so the UI cannot drift from api/openapi.yaml.
 */
import type { components, operations, paths } from './schema';

export type { components, operations, paths };

type S = components['schemas'];

export type Problem = S['Problem'];
export type Issue = S['Issue'];
export type IssueSeverity = S['IssueSeverity'];
export type SourceRef = S['SourceRef'];
export type FacetValue = S['FacetValue'];
export type ActionAccepted = S['ActionAccepted'];

export type AgentKind = S['AgentKind'];
export type ConnectionStatus = S['ConnectionStatus'];
export type RemoteConfigStatus = S['RemoteConfigStatus'];
export type ConfigSyncState = S['ConfigSyncState'];
export type PackageInstallStatus = S['PackageInstallStatus'];
export type Role = S['Role'];
export type ActorType = S['ActorType'];
export type ExecutorType = S['ExecutorType'];
export type ParameterType = S['ParameterType'];
export type SelectorOperator = S['SelectorOperator'];
export type FailureAction = S['FailureAction'];
export type RolloutKind = S['RolloutKind'];
export type RolloutStatus = S['RolloutStatus'];
export type WaveStatus = S['WaveStatus'];
export type TargetStatus = S['TargetStatus'];
export type ArtifactStatus = S['ArtifactStatus'];
export type ObjectKind = S['ObjectKind'];

export type Agent = S['Agent'];
export type AgentConnection = S['AgentConnection'];
export type AgentHealth = S['AgentHealth'];
export type ComponentHealth = S['ComponentHealth'];
export type RemoteConfigState = S['RemoteConfigState'];
export type AgentAssignment = S['AgentAssignment'];
export type PackageStatuses = S['PackageStatuses'];
export type PackageStatus = S['PackageStatus'];
export type AgentSummary = S['AgentSummary'];
export type AgentSummaryPage = S['AgentSummaryPage'];
export type AgentDetail = S['AgentDetail'];
export type ConfigDocument = S['ConfigDocument'];
export type ComponentSet = S['ComponentSet'];
export type AgentEvent = S['AgentEvent'];
export type AgentEventList = S['AgentEventList'];
export type AgentFacets = S['AgentFacets'];
export type SavedFilter = S['SavedFilter'];
export type CreateSavedFilterRequest = S['CreateSavedFilterRequest'];

export type OverviewStats = S['OverviewStats'];
export type AgentCounts = S['AgentCounts'];
export type VersionCount = S['VersionCount'];
export type FleetCounts = S['FleetCounts'];
export type GitOpsBrief = S['GitOpsBrief'];
export type SystemInfo = S['SystemInfo'];
export type Replica = S['Replica'];
export type SettingsSummary = S['SettingsSummary'];

export type AuthConfigInfo = S['AuthConfigInfo'];
export type Me = S['Me'];
export type Permission = Me['permissions'][number];
export type User = S['User'];
export type UpdateUserRequest = S['UpdateUserRequest'];
export type BootstrapLoginRequest = S['BootstrapLoginRequest'];
export type APIToken = S['APIToken'];
export type CreateAPITokenRequest = S['CreateAPITokenRequest'];
export type CreatedAPIToken = S['CreatedAPIToken'];
export type EnrollmentToken = S['EnrollmentToken'];
export type CreateEnrollmentTokenRequest = S['CreateEnrollmentTokenRequest'];
export type CreatedEnrollmentToken = S['CreatedEnrollmentToken'];
export type InstallBundle = S['InstallBundle'];
export type InstallBundleList = S['InstallBundleList'];
export type InstallCommandsRequest = S['InstallCommandsRequest'];
export type InstallCommands = S['InstallCommands'];
export type InstallCommand = S['InstallCommand'];

export type AuditEvent = S['AuditEvent'];
export type AuditEventPage = S['AuditEventPage'];

export type Selector = S['Selector'];
export type SelectorRequirement = S['SelectorRequirement'];
export type Fleet = S['Fleet'];
export type VersionSelectionSpec = S['VersionSelectionSpec'];
export type VersionSelectionState = S['VersionSelectionState'];
export type FleetVersionSelection = S['FleetVersionSelection'];
export type FleetVersionTarget = S['FleetVersionTarget'];
export type VersionTargetSource = S['VersionTargetSource'];
export type VersionOption = S['VersionOption'];
export type FleetVersionOptions = S['FleetVersionOptions'];
export type PlatformCount = S['PlatformCount'];
export type SelectFleetVersionRequest = S['SelectFleetVersionRequest'];
export type SelectFleetVersionResponse = S['SelectFleetVersionResponse'];
export type ClearFleetVersionSelectionRequest = S['ClearFleetVersionSelectionRequest'];
/** Why a version cannot be selected (VersionOption.reasonCode / ReleaseFleetOption.reasonCode). */
export type VersionReasonCode = NonNullable<S['ReleaseFleetOption']['reasonCode']>;
export type NotAllowedCode = NonNullable<S['FleetVersionOptions']['notAllowedCode']>;
export type FleetStatus = S['FleetStatus'];
export type FleetSummary = S['FleetSummary'];
export type FleetDetail = S['FleetDetail'];
export type MatchFleetsRequest = S['MatchFleetsRequest'];
export type MatchFleetsResponse = S['MatchFleetsResponse'];
export type ExecutorSummary = S['ExecutorSummary'];

export type ParameterSpec = S['ParameterSpec'];
export type Fragment = S['Fragment'];
export type Blueprint = S['Blueprint'];
export type BlueprintSummary = S['BlueprintSummary'];
export type BlueprintDetail = S['BlueprintDetail'];
export type RenderRequest = S['RenderRequest'];
export type RenderResponse = S['RenderResponse'];

export type WaveSpec = S['WaveSpec'];
export type MaintenanceWindow = S['MaintenanceWindow'];
export type RolloutPolicy = S['RolloutPolicy'];

export type RolloutCounts = S['RolloutCounts'];
export type RolloutWave = S['RolloutWave'];
export type Rollout = S['Rollout'];
export type RolloutSummary = S['RolloutSummary'];
export type RolloutSummaryPage = S['RolloutSummaryPage'];
export type RolloutTarget = S['RolloutTarget'];
export type RolloutOrigin = S['RolloutOrigin'];
export type TargetProgress = S['TargetProgress'];
export type UpdateStep = S['UpdateStep'];
export type UpdatePhase = S['UpdatePhase'];
export type UpdateStepName = S['UpdateStepName'];
export type UpdateStepStatus = S['UpdateStepStatus'];
export type ProgressSource = S['ProgressSource'];
export type RolloutTargetPage = S['RolloutTargetPage'];

export type ChangelogEntry = S['ChangelogEntry'];
export type ReleaseAsset = S['ReleaseAsset'];
export type Release = S['Release'];
export type ReleaseSummary = S['ReleaseSummary'];
export type ReleaseSummaryPage = S['ReleaseSummaryPage'];
export type FleetBehind = S['FleetBehind'];
export type ReleaseDetail = S['ReleaseDetail'];
export type ReleaseFleetOption = S['ReleaseFleetOption'];
export type VersionComparison = S['VersionComparison'];

export type GitCommit = S['GitCommit'];
export type GitOpsSyncStatus = S['GitOpsSyncStatus'];
export type GitOpsSync = S['GitOpsSync'];
export type GitOpsSyncPage = S['GitOpsSyncPage'];
export type GitOpsObjectRef = S['GitOpsObjectRef'];

export type Artifact = S['Artifact'];
export type ArtifactPage = S['ArtifactPage'];
export type PrefetchRequest = S['PrefetchRequest'];

export type ChannelInfo = S['ChannelInfo'];

export type LiveAgentRemovedData = S['LiveAgentRemovedData'];
export type LiveFleetUpdatedData = S['LiveFleetUpdatedData'];

/** Query parameters of GET /agents (derived from the operation, not hand-written). */
export type ListAgentsQuery = NonNullable<operations['listAgents']['parameters']['query']>;
export type AgentSort = NonNullable<ListAgentsQuery['sort']>;
export type DistributionName = NonNullable<
  NonNullable<operations['listReleases']['parameters']['query']>['distribution']
>;
