// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Group, Text, Tooltip } from '@mantine/core';
import { IconAlertTriangle } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';

/** Version in monospace; optionally marked as outdated (with the latest version in the tooltip). */
export function VersionBadge({
  version,
  outdated,
  latest,
  size = 'sm',
}: {
  version: string | undefined;
  outdated?: boolean;
  latest?: string;
  size?: 'xs' | 'sm' | 'md';
}) {
  const { t } = useTranslation();
  if (!version) return <Text c="dimmed">–</Text>;
  return (
    <Group gap={6} wrap="nowrap" style={{ whiteSpace: 'nowrap' }}>
      <Text className="myr-mono myr-tabular" fz={size} span>
        {version}
      </Text>
      {outdated && (
        <Tooltip label={latest ? t('version.outdatedHint', { latest }) : t('version.outdated')}>
          <Badge
            color="orange"
            size="xs"
            variant="light"
            leftSection={<IconAlertTriangle size={10} />}
            styles={{ label: { textTransform: 'none' } }}
          >
            {t('version.outdated')}
          </Badge>
        </Tooltip>
      )}
    </Group>
  );
}
