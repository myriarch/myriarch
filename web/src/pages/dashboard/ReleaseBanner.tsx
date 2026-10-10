// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Anchor, Button, Collapse, Group, List, Stack, Text } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconChevronDown, IconChevronUp, IconSparkles } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useAcknowledgeRelease, useLatestRelease } from '@/api/hooks/releases';
import type { ReleaseSummary } from '@/api/types';
import { Can } from '@/components/Can';
import { releaseLink } from '@/lib/links';

/** "New collector version x.y.z (n breaking changes)" notice with link and acknowledge action. */
export function ReleaseBanner({ release }: { release: ReleaseSummary }) {
  const { t } = useTranslation();
  const [open, { toggle }] = useDisclosure(false);
  const ack = useAcknowledgeRelease();
  const detail = useLatestRelease(release.distribution as 'otelcol-contrib' | 'opampsupervisor');
  const breaking = detail.data?.release.breakingChanges ?? [];

  if (release.acknowledged) return null;

  return (
    <Alert
      variant="light"
      color={release.breakingCount > 0 ? 'orange' : 'brand'}
      icon={<IconSparkles size={20} />}
      title={
        <Group gap={6}>
          <Text fw={700} span>
            {t('releases.banner.title', { version: release.version })}
          </Text>
          <Text span fw={500}>
            {t('releases.banner.breaking', { count: release.breakingCount })}
          </Text>
        </Group>
      }
      data-testid="release-banner"
    >
      <Stack gap="xs">
        <Text fz="sm">
          {release.fleetsBehind > 0
            ? t('releases.banner.fleetsBehind', { count: release.fleetsBehind })
            : t('releases.banner.upToDate')}
        </Text>
        {release.breakingCount > 0 && breaking.length > 0 && (
          <>
            <Anchor
              component="button"
              type="button"
              fz="sm"
              onClick={toggle}
              style={{ width: 'fit-content' }}
            >
              <Group gap={4} component="span">
                {open ? <IconChevronUp size={14} /> : <IconChevronDown size={14} />}
                {t('releases.banner.showBreaking')}
              </Group>
            </Anchor>
            <Collapse expanded={open}>
              <List size="sm" spacing={4} withPadding>
                {breaking.slice(0, 5).map((b, i) => (
                  <List.Item key={i}>
                    {b.component && (
                      <Text span fw={600} className="myr-mono">
                        {b.component}:{' '}
                      </Text>
                    )}
                    {b.text}
                  </List.Item>
                ))}
              </List>
              {breaking.length > 5 && (
                <Text fz="xs" c="dimmed" mt={4}>
                  {t('releases.banner.more', { count: breaking.length - 5 })}
                </Text>
              )}
            </Collapse>
          </>
        )}
        <Group gap="xs">
          <Button
            size="xs"
            component={Link}
            to={releaseLink(release.distribution, release.version)}
          >
            {t('releases.banner.view')}
          </Button>
          <Can permission="releases:acknowledge">
            <Button
              size="xs"
              variant="default"
              loading={ack.isPending}
              onClick={() =>
                ack.mutate({ distribution: release.distribution, version: release.version })
              }
            >
              {t('releases.acknowledge')}
            </Button>
          </Can>
        </Group>
      </Stack>
    </Alert>
  );
}
