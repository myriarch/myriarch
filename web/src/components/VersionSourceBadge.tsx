// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Tooltip, type MantineSize } from '@mantine/core';
import { IconGitBranch, IconHandClick, IconPlayerPause, IconCpu } from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { RolloutOrigin, VersionTargetSource } from '@/api/types';

const SOURCE_STYLE: Record<VersionTargetSource, { color: string; icon: ReactNode }> = {
  git: { color: 'gray', icon: <IconGitBranch size={11} aria-hidden /> },
  ui: { color: 'blue', icon: <IconHandClick size={11} aria-hidden /> },
  hold: { color: 'orange', icon: <IconPlayerPause size={11} aria-hidden /> },
  none: { color: 'gray', icon: null },
};

/** Where the effective target version comes from: "aus Git", "per UI gesetzt", "angehalten". */
export function VersionSourceBadge({
  source,
  size = 'sm',
  short = false,
  tooltip,
}: {
  source: VersionTargetSource;
  size?: MantineSize;
  /** Mini variant for tables ("UI", "angehalten"); nothing for git/none. */
  short?: boolean;
  tooltip?: ReactNode;
}) {
  const { t } = useTranslation();
  if (short && (source === 'git' || source === 'none')) return null;
  const s = SOURCE_STYLE[source];
  const badge = (
    <Badge
      color={s.color}
      variant={source === 'git' || source === 'none' ? 'outline' : 'light'}
      size={size}
      leftSection={s.icon}
      data-testid={`version-source-${source}`}
      styles={{ label: { textTransform: 'none' } }}
    >
      {short ? t(`enum.versionSourceShort.${source}`) : t(`enum.versionSource.${source}`)}
    </Badge>
  );
  return tooltip ? <Tooltip label={tooltip}>{badge}</Tooltip> : badge;
}

const ORIGIN_STYLE: Record<RolloutOrigin, { color: string; icon: ReactNode }> = {
  gitops: { color: 'gray', icon: <IconGitBranch size={11} aria-hidden /> },
  ui: { color: 'blue', icon: <IconHandClick size={11} aria-hidden /> },
  system: { color: 'gray', icon: <IconCpu size={11} aria-hidden /> },
};

/** Who caused a rollout: Git, an operator in the UI, or the system. */
export function RolloutOriginBadge({
  origin,
  size = 'xs',
  short = false,
}: {
  origin: RolloutOrigin;
  size?: MantineSize;
  short?: boolean;
}) {
  const { t } = useTranslation();
  const s = ORIGIN_STYLE[origin];
  return (
    <Badge
      color={s.color}
      variant={origin === 'ui' ? 'light' : 'outline'}
      size={size}
      leftSection={s.icon}
      data-testid={`rollout-origin-${origin}`}
      styles={{ label: { textTransform: 'none' } }}
    >
      {short ? t(`enum.rolloutOriginShort.${origin}`) : t(`enum.rolloutOrigin.${origin}`)}
    </Badge>
  );
}
