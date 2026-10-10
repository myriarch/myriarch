// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Button, Group, Select, Skeleton, Stack, Tabs, Text } from '@mantine/core';
import { IconArrowRight } from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCompareVersions, useReleases } from '@/api/hooks/releases';
import type { DistributionName } from '@/api/types';
import { SectionCard } from '@/components/SectionCard';
import { ErrorState } from '@/components/states';
import { compareVersions } from '@/lib/format';
import { ChangeList } from './ChangeList';

/** "from → to" comparison of all breaking changes and deprecations in (from, to]. */
export function VersionCompare({
  distribution,
  initialFrom,
  initialTo,
}: {
  distribution: DistributionName;
  initialFrom?: string;
  initialTo?: string;
}) {
  const { t } = useTranslation();
  const list = useReleases({ distribution, includePrerelease: false, limit: 200, offset: 0 });
  const versions = useMemo(
    () => [...(list.data?.items ?? [])].map((r) => r.version).sort((a, b) => compareVersions(b, a)),
    [list.data],
  );
  const [from, setFrom] = useState<string | null>(initialFrom ?? null);
  const [to, setTo] = useState<string | null>(initialTo ?? null);
  const [submitted, setSubmitted] = useState<{ from: string; to: string } | null>(
    initialFrom && initialTo ? { from: initialFrom, to: initialTo } : null,
  );
  const cmp = useCompareVersions(distribution, submitted?.from, submitted?.to);
  const order = from && to ? compareVersions(from, to) : 0;

  return (
    <SectionCard title={t('releases.compare.title')} description={t('releases.compare.subtitle')}>
      <Stack gap="md">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <Select
            label={t('releases.compare.from')}
            w={180}
            searchable
            data={versions}
            value={from}
            onChange={setFrom}
            placeholder="0.150.0"
            nothingFoundMessage={t('picker.nothingFound')}
          />
          <IconArrowRight size={18} style={{ marginBottom: 9, opacity: 0.6 }} aria-hidden />
          <Select
            label={t('releases.compare.to')}
            w={180}
            searchable
            data={versions}
            value={to}
            onChange={setTo}
            placeholder="0.162.0"
            nothingFoundMessage={t('picker.nothingFound')}
          />
          <Button
            disabled={!from || !to || order >= 0}
            onClick={() => setSubmitted({ from: from!, to: to! })}
          >
            {t('releases.compare.run')}
          </Button>
        </Group>
        {from && to && order >= 0 && (
          <Text fz="sm" c="orange">
            {t('releases.compare.order')}
          </Text>
        )}
        {submitted && cmp.isPending && <Skeleton height={120} />}
        {submitted && cmp.error && (
          <ErrorState compact error={cmp.error} onRetry={() => void cmp.refetch()} />
        )}
        {submitted && cmp.data && (
          <Stack gap="sm" data-testid="compare-result">
            <Alert variant="light" color={cmp.data.breakingChanges.length > 0 ? 'orange' : 'teal'}>
              {t('releases.compare.summary', {
                from: cmp.data.from,
                to: cmp.data.to,
                versions: cmp.data.versions.length,
                breaking: cmp.data.breakingChanges.length,
                deprecations: cmp.data.deprecations.length,
              })}
            </Alert>
            <Tabs defaultValue="breaking">
              <Tabs.List mb="sm">
                <Tabs.Tab value="breaking">
                  {t('releases.breakingChanges')} ({cmp.data.breakingChanges.length})
                </Tabs.Tab>
                <Tabs.Tab value="deprecations">
                  {t('releases.deprecations')} ({cmp.data.deprecations.length})
                </Tabs.Tab>
              </Tabs.List>
              <Tabs.Panel value="breaking">
                <ChangeList
                  entries={cmp.data.breakingChanges}
                  emptyTitle={t('releases.noBreaking')}
                  showVersion
                />
              </Tabs.Panel>
              <Tabs.Panel value="deprecations">
                <ChangeList
                  entries={cmp.data.deprecations}
                  emptyTitle={t('releases.noDeprecations')}
                  showVersion
                />
              </Tabs.Panel>
            </Tabs>
          </Stack>
        )}
      </Stack>
    </SectionCard>
  );
}
