// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Badge, Group, Skeleton, Stack, Text } from '@mantine/core';
import { IconGitBranch } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { isApiError } from '@/api/errors';
import { useFleets, useRolloutPolicy } from '@/api/hooks/fleets';
import { PageHeader } from '@/components/PageHeader';
import { PolicyView } from '@/components/PolicyView';
import { SectionCard } from '@/components/SectionCard';
import { ErrorState } from '@/components/states';
import { fleetLink } from '@/lib/links';

export default function PolicyDetailPage() {
  const { t } = useTranslation();
  const { name } = useParams();
  const { data, isPending, error, refetch } = useRolloutPolicy(name);
  const fleets = useFleets();
  const crumbs = [
    { label: t('nav.fleets'), to: '/fleets' },
    { label: t('fleets.tabs.policies'), to: '/fleets?tab=policies' },
    { label: name ?? '' },
  ];

  if (isPending) {
    return (
      <>
        <PageHeader title={name ?? ''} breadcrumbs={crumbs} />
        <Stack>
          <Skeleton height={200} />
        </Stack>
      </>
    );
  }
  if (error || !data) {
    const notFound = isApiError(error) && error.status === 404;
    return (
      <>
        <PageHeader title={name ?? ''} breadcrumbs={crumbs} />
        <ErrorState
          error={error}
          title={notFound ? t('policy.notFound') : undefined}
          onRetry={notFound ? undefined : () => void refetch()}
        />
      </>
    );
  }
  const users = (fleets.data?.items ?? []).filter(
    (f) =>
      (f.fleet.rollout.configPolicy ?? 'default') === data.name ||
      (f.fleet.rollout.versionPolicy ?? 'default') === data.name,
  );
  return (
    <>
      <PageHeader
        title={data.name}
        subtitle={data.description}
        breadcrumbs={crumbs}
        documentTitle={data.name}
      />
      <Alert variant="light" color="gray" mb="md" icon={<IconGitBranch size={16} />} py="xs">
        {t('common.readOnlyGit')}{' '}
        <Text span className="myr-mono" fz="xs">
          {data.source.path}
        </Text>
      </Alert>
      <Stack gap="md">
        <SectionCard title={t('policy.definition')}>
          <PolicyView policy={data} />
        </SectionCard>
        <SectionCard title={t('policy.usedBy')}>
          <Group gap={6}>
            {users.map((f) => (
              <Badge
                key={f.fleet.name}
                component={Link}
                to={fleetLink(f.fleet.name)}
                variant="light"
                size="lg"
                style={{ textTransform: 'none', cursor: 'pointer' }}
              >
                {f.fleet.displayName || f.fleet.name}
              </Badge>
            ))}
            {users.length === 0 && (
              <Text c="dimmed" fz="sm">
                {t('policy.unused')}
              </Text>
            )}
          </Group>
        </SectionCard>
      </Stack>
    </>
  );
}
