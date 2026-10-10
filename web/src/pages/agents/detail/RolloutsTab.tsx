// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Table, Text } from '@mantine/core';
import { IconRocket } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import type { RolloutTarget } from '@/api/types';
import { SectionCard } from '@/components/SectionCard';
import { TargetStatusBadge } from '@/components/StatusBadge';
import { EmptyState } from '@/components/states';
import { TimeAgo } from '@/components/TimeAgo';
import { rolloutLink } from '@/lib/links';

export function RolloutsTab({ targets }: { targets: RolloutTarget[] }) {
  const { t } = useTranslation();
  if (targets.length === 0) {
    return (
      <SectionCard>
        <EmptyState
          icon={<IconRocket size={28} />}
          title={t('agentDetail.rollouts.none')}
          text={t('agentDetail.rollouts.noneHint')}
        />
      </SectionCard>
    );
  }
  return (
    <SectionCard padded={false} title={t('agentDetail.rollouts.title')}>
      <div className="myr-scroll-x">
        <Table fz="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('nav.rollouts')}</Table.Th>
              <Table.Th>{t('rollouts.wave')}</Table.Th>
              <Table.Th>{t('common.status')}</Table.Th>
              <Table.Th>{t('rollouts.fromTo')}</Table.Th>
              <Table.Th ta="right">{t('rollouts.attempts')}</Table.Th>
              <Table.Th>{t('common.updated')}</Table.Th>
              <Table.Th>{t('rollouts.lastError')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {targets.map((r) => (
              <Table.Tr key={r.rolloutId}>
                <Table.Td>
                  <Anchor
                    component={Link}
                    to={rolloutLink(r.rolloutId)}
                    className="myr-mono"
                    fz="sm"
                  >
                    {r.rolloutId.slice(0, 8)}
                  </Anchor>
                </Table.Td>
                <Table.Td>{r.wave + 1}</Table.Td>
                <Table.Td>
                  <TargetStatusBadge status={r.status} />
                </Table.Td>
                <Table.Td className="myr-mono">
                  {r.fromRef ?? '–'} → {r.toRef}
                </Table.Td>
                <Table.Td ta="right">{r.attempts}</Table.Td>
                <Table.Td>
                  <TimeAgo value={r.updatedAt} />
                </Table.Td>
                <Table.Td>
                  {r.lastError ? (
                    <Text c="red" fz="xs">
                      {r.lastError}
                    </Text>
                  ) : (
                    '–'
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
    </SectionCard>
  );
}
