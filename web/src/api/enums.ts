// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Runtime lists of the OpenAPI enums (the generated file only has types). Each list is checked
 * against the generated union at compile time: forgetting a value (or adding an unknown one)
 * fails `pnpm typecheck`.
 */
import type {
  AgentKind,
  ArtifactStatus,
  ConfigSyncState,
  ConnectionStatus,
  ExecutorType,
  ObjectKind,
  PackageInstallStatus,
  ParameterType,
  Permission,
  Problem,
  RemoteConfigStatus,
  Role,
  RolloutKind,
  RolloutStatus,
  ProgressSource,
  RolloutOrigin,
  SelectorOperator,
  TargetStatus,
  UpdatePhase,
  UpdateStepName,
  UpdateStepStatus,
  VersionReasonCode,
  VersionTargetSource,
  NotAllowedCode,
  WaveStatus,
} from './types';

type Exhaustive<T, A extends readonly T[]> = [Exclude<T, A[number]>] extends [never] ? A : never;
function enumOf<T extends string>() {
  return <const A extends readonly T[]>(values: Exhaustive<T, A>): A => values;
}

export const AGENT_KINDS = enumOf<AgentKind>()(['collector', 'updater', 'unknown']);
export const CONNECTION_STATUSES = enumOf<ConnectionStatus>()(['online', 'offline']);
export const REMOTE_CONFIG_STATUSES = enumOf<RemoteConfigStatus>()([
  'unset',
  'applying',
  'applied',
  'failed',
]);
export const CONFIG_SYNC_STATES = enumOf<ConfigSyncState>()([
  'in_sync',
  'pending',
  'failed',
  'drift',
  'none',
]);
export const PACKAGE_INSTALL_STATUSES = enumOf<PackageInstallStatus>()([
  'installed',
  'install_pending',
  'installing',
  'install_failed',
  'downloading',
]);
export const ROLES = enumOf<Role>()(['viewer', 'operator', 'admin']);
export const EXECUTOR_TYPES = enumOf<ExecutorType>()([
  'none',
  'updater',
  'webhook',
  'awx',
  'manual',
]);
export const PARAMETER_TYPES = enumOf<ParameterType>()([
  'string',
  'int',
  'number',
  'bool',
  'duration',
  'list',
  'map',
]);
export const SELECTOR_OPERATORS = enumOf<SelectorOperator>()([
  'In',
  'NotIn',
  'Exists',
  'DoesNotExist',
  'Glob',
]);
export const ROLLOUT_KINDS = enumOf<RolloutKind>()(['config', 'version']);
export const ROLLOUT_STATUSES = enumOf<RolloutStatus>()([
  'pending',
  'running',
  'waiting_approval',
  'paused',
  'completed',
  'failed',
  'aborted',
  'rolling_back',
  'rolled_back',
  'superseded',
]);
export const WAVE_STATUSES = enumOf<WaveStatus>()([
  'pending',
  'waiting_approval',
  'running',
  'completed',
  'failed',
  'skipped',
]);
export const TARGET_STATUSES = enumOf<TargetStatus>()([
  'pending',
  'waiting_window',
  'in_progress',
  'verifying',
  'succeeded',
  'failed',
  'skipped',
  'cancelled',
  'rolled_back',
]);
export const ARTIFACT_STATUSES = enumOf<ArtifactStatus>()([
  'pending',
  'downloading',
  'ready',
  'failed',
]);
export const OBJECT_KINDS = enumOf<ObjectKind>()([
  'Fleet',
  'Blueprint',
  'RolloutPolicy',
  'Executor',
  'Inventory',
]);
export const PERMISSIONS = enumOf<Permission>()([
  'read',
  'saved_filters:write',
  'agents:operate',
  'agents:delete',
  'rollouts:operate',
  'fleets:set_version',
  'releases:acknowledge',
  'gitops:sync',
  'artifacts:manage',
  'notifications:test',
  'audit:read',
  'tokens:manage',
  'enrollment:manage',
  'users:manage',
]);

export const VERSION_TARGET_SOURCES = enumOf<VersionTargetSource>()(['git', 'ui', 'hold', 'none']);
export const ROLLOUT_ORIGINS = enumOf<RolloutOrigin>()(['gitops', 'ui', 'system']);
export const UPDATE_PHASES = enumOf<UpdatePhase>()([
  'offered',
  'downloading',
  'verifying',
  'installing',
  'restarting',
  'health_check',
  'rolling_back',
  'health_wait',
  'succeeded',
  'failed',
  'rolled_back',
  'cancelled',
]);
export const UPDATE_STEP_NAMES = enumOf<UpdateStepName>()([
  'offered',
  'downloading',
  'verifying',
  'installing',
  'restarting',
  'health_check',
  'rolling_back',
  'health_wait',
]);
export const UPDATE_STEP_STATUSES = enumOf<UpdateStepStatus>()([
  'pending',
  'running',
  'done',
  'failed',
  'skipped',
]);
export const PROGRESS_SOURCES = enumOf<ProgressSource>()(['updater', 'package_status', 'executor']);
export const VERSION_REASON_CODES = enumOf<VersionReasonCode>()([
  'selection_disabled',
  'no_executor',
  'blocking_rollout',
  'current_target',
  'outside_frame',
  'downgrade_not_allowed',
  'prerelease_not_allowed',
  'unknown_version',
]);
export const NOT_ALLOWED_CODES = enumOf<NotAllowedCode>()([
  'selection_disabled',
  'no_executor',
  'blocking_rollout',
]);
/** `issues[].code` values of a refused selection (422 validation_failed). */
export const SELECTION_ISSUE_CODES = [
  'version_not_allowed',
  'downgrade_not_allowed',
  'unknown_version',
  'prerelease_not_allowed',
  'reason_required',
] as const;

/** Non-terminal rollout states (mirrors the `active` filter of listRollouts). */
export const ACTIVE_ROLLOUT_STATUSES: readonly RolloutStatus[] = [
  'pending',
  'running',
  'waiting_approval',
  'paused',
  'rolling_back',
];

export const PROBLEM_CODES = enumOf<Problem['code']>()([
  'bad_request',
  'unauthorized',
  'forbidden',
  'not_found',
  'conflict',
  'precondition_failed',
  'validation_failed',
  'too_many_requests',
  'internal',
  'not_implemented',
  'unavailable',
  'csrf_failed',
]);

export const DISTRIBUTIONS = ['otelcol-contrib', 'opampsupervisor'] as const;
