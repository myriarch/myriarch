// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Badge, Group, Stack, Text, UnstyledButton } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import type { RolloutSummary } from '@/api/types';
import { RolloutProgress } from '@/components/RolloutProgress';
import { RolloutOriginBadge } from '@/components/VersionSourceBadge';
import { SectionCard } from '@/components/SectionCard';
import { RolloutStatusBadge } from '@/components/StatusBadge';
import { EmptyState } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { rolloutLink } from '@/lib/links';
import { IconRocket } from '@tabler/icons-react';

export function ActiveRollouts({ rollouts }: { rollouts: RolloutSummary[] }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <SectionCard
      h="100%"
      title={t('dashboard.rollouts.title')}
      actions={
        <Anchor component={Link} to="/rollouts" fz="sm">
          {t('common.viewAll')}
        </Anchor>
      }
    >
      {rollouts.length === 0 ? (
        <EmptyState
          compact
          icon={<IconRocket size={22} />}
          title={t('dashboard.rollouts.empty')}
          text={t('dashboard.rollouts.emptyHint')}
        />
      ) : (
        <Stack gap="sm">
          {rollouts.map((r) => (
            <UnstyledButton
              key={r.id}
              onClick={() => void navigate(rolloutLink(r.id))}
              p="sm"
              style={{
                border: '1px solid var(--myr-border)',
                borderRadius: 'var(--mantine-radius-md)',
              }}
            >
              <Stack gap={6}>
                <Group justify="space-between" wrap="nowrap" gap="xs">
                  <Group gap={8} wrap="nowrap" style={{ minWidth: 0 }}>
                    <Text fw={600} fz="sm" truncate>
                      {r.fleetName}
                    </Text>
                    <Badge size="xs" variant="outline" color="gray">
                      {t(`enum.rolloutKind.${r.kind}`)}
                    </Badge>
                    <RolloutOriginBadge origin={r.origin} short />
                    <Text className="myr-mono" fz="xs" c="dimmed" truncate>
                      {r.kind === 'version'
                        ? `${r.fromRef ?? '?'} → ${r.toRef}`
                        : r.toRef.slice(0, 8)}
                    </Text>
                  </Group>
                  <RolloutStatusBadge status={r.status} />
                </Group>
                <RolloutProgress counts={r.counts} />
                <Group justify="space-between" gap="xs">
                  <Text fz="xs" c="dimmed">
                    {t('rollouts.waveOf', {
                      current: Math.min(r.currentWave + 1, r.waveCount),
                      total: r.waveCount,
                    })}
                  </Text>
                  <Text fz="xs" c="dimmed">
                    <TimeAgo value={r.updatedAt} />
                  </Text>
                </Group>
              </Stack>
            </UnstyledButton>
          ))}
        </Stack>
      )}
    </SectionCard>
  );
}
