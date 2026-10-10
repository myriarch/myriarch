// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import type {
  ArtifactStatus,
  ConfigSyncState,
  ConnectionStatus,
  RolloutStatus,
  TargetStatus,
  WaveStatus,
} from '@/api/types';

/**
 * Status color language (single source of truth for badges, bars and charts):
 * online/healthy/success = green (teal), offline/none = gray, failed/error = red,
 * pending/warning = yellow/orange, drift = violet, info/running = blue.
 */
export type Tone = 'success' | 'neutral' | 'danger' | 'warning' | 'drift' | 'info' | 'accent';

export const toneColor: Record<Tone, string> = {
  success: 'teal',
  neutral: 'gray',
  danger: 'red',
  warning: 'yellow',
  drift: 'violet',
  info: 'blue',
  accent: 'brand',
};

export const connectionTone: Record<ConnectionStatus, Tone> = {
  online: 'success',
  offline: 'neutral',
};

export type HealthState = 'healthy' | 'unhealthy' | 'unknown';
export const healthTone: Record<HealthState, Tone> = {
  healthy: 'success',
  unhealthy: 'danger',
  unknown: 'neutral',
};

export function healthState(healthy: boolean | undefined): HealthState {
  if (healthy === undefined) return 'unknown';
  return healthy ? 'healthy' : 'unhealthy';
}

export const configSyncTone: Record<ConfigSyncState, Tone> = {
  in_sync: 'success',
  pending: 'warning',
  failed: 'danger',
  drift: 'drift',
  none: 'neutral',
};

export const rolloutTone: Record<RolloutStatus, Tone> = {
  pending: 'neutral',
  running: 'info',
  waiting_approval: 'warning',
  paused: 'warning',
  completed: 'success',
  failed: 'danger',
  aborted: 'neutral',
  rolling_back: 'drift',
  rolled_back: 'drift',
  superseded: 'neutral',
};

export const waveTone: Record<WaveStatus, Tone> = {
  pending: 'neutral',
  waiting_approval: 'warning',
  running: 'info',
  completed: 'success',
  failed: 'danger',
  skipped: 'neutral',
};

export const targetTone: Record<TargetStatus, Tone> = {
  pending: 'neutral',
  waiting_window: 'warning',
  in_progress: 'info',
  verifying: 'info',
  succeeded: 'success',
  failed: 'danger',
  skipped: 'neutral',
  cancelled: 'neutral',
  rolled_back: 'drift',
};

export const artifactTone: Record<ArtifactStatus, Tone> = {
  pending: 'neutral',
  downloading: 'info',
  ready: 'success',
  failed: 'danger',
};

export const severityTone = {
  error: 'danger',
  warning: 'warning',
  info: 'info',
  critical: 'danger',
} as const satisfies Record<string, Tone>;
