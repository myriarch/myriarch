// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Anchor, Badge, Group, Table, Text, TextInput } from '@mantine/core';
import { IconGitBranch, IconSearch, IconStack2 } from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useBlueprints } from '@/api/hooks/fleets';
import { PageHeader } from '@/components/PageHeader';
import { SectionCard } from '@/components/SectionCard';
import { EmptyState, ErrorState, TableSkeleton } from '@/components/states';
import { blueprintLink, fleetLink } from '@/lib/links';

export default function BlueprintsPage() {
  const { t } = useTranslation();
  const { data, isPending, error, refetch } = useBlueprints();
  const [q, setQ] = useState('');
  const items = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data?.items ?? []).filter(
      (b) =>
        !needle ||
        b.name.toLowerCase().includes(needle) ||
        (b.description ?? '').toLowerCase().includes(needle),
    );
  }, [data, q]);

  let body;
  if (isPending) body = <TableSkeleton cols={6} rows={5} />;
  else if (error) body = <ErrorState error={error} onRetry={() => void refetch()} />;
  else if (data.items.length === 0)
    body = (
      <EmptyState
        icon={<IconStack2 size={28} />}
        title={t('blueprints.empty')}
        text={t('blueprints.emptyHint')}
      />
    );
  else if (items.length === 0) body = <EmptyState compact title={t('blueprints.noMatch')} />;
  else
    body = (
      <div className="myr-scroll-x">
        <Table fz="sm" verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('common.name')}</Table.Th>
              <Table.Th>{t('blueprints.extends')}</Table.Th>
              <Table.Th ta="right">{t('blueprints.parameters')}</Table.Th>
              <Table.Th ta="right">{t('blueprints.fragments')}</Table.Th>
              <Table.Th>{t('blueprints.usedBy')}</Table.Th>
              <Table.Th>{t('common.issues')}</Table.Th>
              <Table.Th>{t('common.source')}</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {items.map((b) => (
              <Table.Tr key={b.name}>
                <Table.Td maw={340}>
                  <Anchor component={Link} to={blueprintLink(b.name)} fw={600}>
                    {b.name}
                  </Anchor>
                  <Text fz="xs" c="dimmed">
                    {b.description}
                  </Text>
                </Table.Td>
                <Table.Td>
                  {b.extends ? (
                    <Anchor
                      component={Link}
                      to={blueprintLink(b.extends)}
                      className="myr-mono"
                      fz="sm"
                    >
                      {b.extends}
                    </Anchor>
                  ) : (
                    <Text c="dimmed">–</Text>
                  )}
                </Table.Td>
                <Table.Td ta="right" className="myr-tabular">
                  {b.parameterCount}
                </Table.Td>
                <Table.Td ta="right" className="myr-tabular">
                  {b.fragmentCount}
                </Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {b.usedBy.map((f) => (
                      <Badge
                        key={f}
                        variant="outline"
                        color="gray"
                        component={Link}
                        to={fleetLink(f)}
                        style={{ textTransform: 'none', cursor: 'pointer' }}
                      >
                        {f}
                      </Badge>
                    ))}
                    {b.usedBy.length === 0 && (
                      <Text c="dimmed" fz="sm">
                        –
                      </Text>
                    )}
                  </Group>
                </Table.Td>
                <Table.Td>
                  {b.issueCount > 0 ? (
                    <Badge color="red" variant="light">
                      {b.issueCount}
                    </Badge>
                  ) : (
                    <Badge color="teal" variant="light">
                      {t('common.ok')}
                    </Badge>
                  )}
                </Table.Td>
                <Table.Td>
                  <Group gap={4} wrap="nowrap">
                    <IconGitBranch size={13} />
                    <Text className="myr-mono" fz="xs">
                      {b.source.path}
                    </Text>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
    );

  return (
    <>
      <PageHeader title={t('blueprints.title')} subtitle={t('blueprints.subtitle')} />
      <Alert variant="light" color="gray" mb="md" icon={<IconGitBranch size={16} />} py="xs">
        {t('common.readOnlyGit')}
      </Alert>
      <SectionCard
        padded={false}
        actions={
          <TextInput
            size="xs"
            w={240}
            leftSection={<IconSearch size={14} />}
            placeholder={t('blueprints.search')}
            aria-label={t('blueprints.search')}
            value={q}
            onChange={(e) => setQ(e.currentTarget.value)}
          />
        }
      >
        {body}
      </SectionCard>
    </>
  );
}
