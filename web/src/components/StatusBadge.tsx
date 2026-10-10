// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Tooltip, type MantineSize } from '@mantine/core';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type {
  ArtifactStatus,
  ConfigSyncState,
  ConnectionStatus,
  IssueSeverity,
  RolloutStatus,
  TargetStatus,
  WaveStatus,
} from '@/api/types';
import {
  artifactTone,
  configSyncTone,
  connectionTone,
  healthState,
  healthTone,
  rolloutTone,
  targetTone,
  toneColor,
  waveTone,
  type Tone,
} from '@/theme/status';

interface StatusBadgeProps {
  tone: Tone;
  label: ReactNode;
  tooltip?: ReactNode;
  size?: MantineSize;
  /** Render a leading status dot (default) */
  dot?: boolean;
  variant?: 'light' | 'filled' | 'outline' | 'dot';
}

function Dot({ color }: { color: string }) {
  return (
    <span
      aria-hidden
      style={{
        display: 'inline-block',
        width: 7,
        height: 7,
        borderRadius: '50%',
        background: `var(--mantine-color-${color}-6)`,
      }}
    />
  );
}

/** The one badge used for every status in the app (consistent colors, see theme/status.ts). */
export function StatusBadge({
  tone,
  label,
  tooltip,
  size = 'sm',
  dot = true,
  variant = 'light',
}: StatusBadgeProps) {
  const color = toneColor[tone];
  const badge = (
    <Badge
      color={color}
      size={size}
      variant={variant}
      leftSection={dot ? <Dot color={color} /> : undefined}
      styles={{
        root: { minWidth: 'max-content' },
        label: { textTransform: 'none', fontWeight: 600 },
      }}
    >
      {label}
    </Badge>
  );
  return tooltip ? <Tooltip label={tooltip}>{badge}</Tooltip> : badge;
}

export function ConnectionBadge({
  status,
  size,
}: {
  status: ConnectionStatus;
  size?: MantineSize;
}) {
  const { t } = useTranslation();
  return (
    <StatusBadge size={size} tone={connectionTone[status]} label={t(`enum.connection.${status}`)} />
  );
}

export function HealthBadge({
  healthy,
  detail,
  size,
}: {
  healthy: boolean | undefined;
  detail?: string;
  size?: MantineSize;
}) {
  const { t } = useTranslation();
  const state = healthState(healthy);
  return (
    <StatusBadge
      size={size}
      tone={healthTone[state]}
      label={t(`enum.health.${state}`)}
      tooltip={detail && detail !== '' ? detail : undefined}
    />
  );
}

export function ConfigSyncBadge({ state, size }: { state: ConfigSyncState; size?: MantineSize }) {
  const { t } = useTranslation();
  return (
    <StatusBadge
      size={size}
      tone={configSyncTone[state]}
      label={t(`enum.configSync.${state}`)}
      tooltip={t(`enum.configSyncHint.${state}`)}
    />
  );
}

export function RolloutStatusBadge({
  status,
  size,
}: {
  status: RolloutStatus;
  size?: MantineSize;
}) {
  const { t } = useTranslation();
  return (
    <StatusBadge size={size} tone={rolloutTone[status]} label={t(`enum.rolloutStatus.${status}`)} />
  );
}

export function WaveStatusBadge({ status, size }: { status: WaveStatus; size?: MantineSize }) {
  const { t } = useTranslation();
  return <StatusBadge size={size} tone={waveTone[status]} label={t(`enum.waveStatus.${status}`)} />;
}

export function TargetStatusBadge({ status, size }: { status: TargetStatus; size?: MantineSize }) {
  const { t } = useTranslation();
  return (
    <StatusBadge size={size} tone={targetTone[status]} label={t(`enum.targetStatus.${status}`)} />
  );
}

export function ArtifactStatusBadge({
  status,
  size,
}: {
  status: ArtifactStatus;
  size?: MantineSize;
}) {
  const { t } = useTranslation();
  return (
    <StatusBadge
      size={size}
      tone={artifactTone[status]}
      label={t(`enum.artifactStatus.${status}`)}
    />
  );
}

export function SeverityBadge({
  severity,
  size,
}: {
  severity: IssueSeverity | 'info' | 'critical';
  size?: MantineSize;
}) {
  const { t } = useTranslation();
  const tone: Tone = severity === 'warning' ? 'warning' : severity === 'info' ? 'info' : 'danger';
  return <StatusBadge size={size} tone={tone} label={t(`enum.severity.${severity}`)} />;
}
