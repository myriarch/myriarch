// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Group, Text, Tooltip } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { useLiveStatus } from '@/live/context';

const COLOR = {
  open: 'var(--mantine-color-teal-6)',
  connecting: 'var(--mantine-color-yellow-6)',
  reconnecting: 'var(--mantine-color-orange-6)',
  closed: 'var(--mantine-color-gray-5)',
} as const;

/** Header chip showing the state of the SSE connection. */
export function LiveIndicator() {
  const { t } = useTranslation();
  const status = useLiveStatus();
  const label =
    status === 'open'
      ? t('live.connected')
      : status === 'connecting'
        ? t('live.connecting')
        : status === 'reconnecting'
          ? t('live.reconnecting')
          : t('live.offline');
  const tip =
    status === 'open'
      ? t('live.tooltipConnected')
      : status === 'connecting'
        ? t('live.tooltipConnecting')
        : status === 'reconnecting'
          ? t('live.tooltipReconnecting')
          : t('live.tooltipOffline');
  return (
    <Tooltip label={tip}>
      <Group
        gap={6}
        wrap="nowrap"
        role="status"
        aria-live="polite"
        data-testid="live-indicator"
        data-status={status}
      >
        <span
          aria-hidden
          style={{
            width: 8,
            height: 8,
            borderRadius: '50%',
            background: COLOR[status],
            boxShadow:
              status === 'open'
                ? `0 0 0 3px color-mix(in srgb, ${COLOR.open} 25%, transparent)`
                : undefined,
          }}
        />
        <Text fz="xs" fw={600} c="dimmed" visibleFrom="sm">
          {label}
        </Text>
      </Group>
    </Tooltip>
  );
}
