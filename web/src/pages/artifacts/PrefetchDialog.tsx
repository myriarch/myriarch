// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Button, Group, Modal, MultiSelect, Select, Stack, TextInput } from '@mantine/core';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { DISTRIBUTIONS } from '@/api/enums';
import { usePrefetchArtifacts } from '@/api/hooks/admin';
import { useReleases } from '@/api/hooks/releases';
import { ErrorAlert } from '@/components/states';
import { compareVersions } from '@/lib/format';

const PLATFORMS = [
  'linux/amd64',
  'linux/arm64',
  'windows/amd64',
  'windows/arm64',
  'darwin/arm64',
  'darwin/amd64',
];

export function PrefetchDialog({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const [distribution, setDistribution] = useState<string>('otelcol-contrib');
  const [version, setVersion] = useState('');
  const [platforms, setPlatforms] = useState<string[]>([]);
  const prefetch = usePrefetchArtifacts();
  const releases = useReleases({
    distribution: distribution as 'otelcol-contrib',
    includePrerelease: false,
    limit: 30,
    offset: 0,
  });
  const versions = useMemo(
    () => (releases.data?.items ?? []).map((r) => r.version).sort((a, b) => compareVersions(b, a)),
    [releases.data],
  );

  const submit = () => {
    prefetch.mutate(
      {
        distribution,
        version: version.trim(),
        platforms: platforms.length > 0 ? platforms : undefined,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Modal opened={opened} onClose={onClose} title={t('artifacts.prefetch.title')} size="md">
      <Stack gap="md">
        <Select
          label={t('releases.distribution')}
          data={[...DISTRIBUTIONS]}
          value={distribution}
          onChange={(v) => v && setDistribution(v)}
        />
        <Select
          label={t('common.version')}
          description={t('artifacts.prefetch.versionHint')}
          searchable
          data={versions}
          value={version || null}
          onChange={(v) => setVersion(v ?? '')}
          placeholder="0.162.0"
          nothingFoundMessage={t('picker.nothingFound')}
          rightSection={undefined}
        />
        <MultiSelect
          label={t('artifacts.prefetch.platforms')}
          description={t('artifacts.prefetch.platformsHint')}
          data={PLATFORMS}
          value={platforms}
          onChange={setPlatforms}
          clearable
        />
        <TextInput
          label={t('artifacts.prefetch.customVersion')}
          description={t('artifacts.prefetch.customVersionHint')}
          value={version}
          onChange={(e) => setVersion(e.currentTarget.value)}
          placeholder="0.162.0"
        />
        <ErrorAlert error={prefetch.error} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} loading={prefetch.isPending} disabled={!version.trim()}>
            {t('artifacts.prefetch.start')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
