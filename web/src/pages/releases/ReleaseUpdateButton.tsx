// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Button, Group, Modal, Paper, Stack, Text, UnstyledButton } from '@mantine/core';
import { IconRocket } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { ReleaseFleetOption } from '@/api/types';
import { Can } from '@/components/Can';
import { VersionBadge } from '@/components/VersionBadge';
import { VersionSourceBadge } from '@/components/VersionSourceBadge';
import { sortFleetOptions } from '@/lib/updates';
import { UpdateVersionDialog } from '@/pages/fleets/UpdateVersionDialog';

function FleetChoice({
  option,
  onPick,
}: {
  option: ReleaseFleetOption;
  onPick: (fleet: string) => void;
}) {
  const { t } = useTranslation();
  const content = (
    <Paper
      withBorder
      p="sm"
      radius="md"
      style={{ opacity: option.selectable ? 1 : 0.6 }}
      data-testid={`fleet-choice-${option.fleet}`}
    >
      <Group justify="space-between" wrap="nowrap" gap="md">
        <Stack gap={2} style={{ minWidth: 0 }}>
          <Text fw={600} fz="sm">
            {option.displayName || option.fleet}
          </Text>
          <Group gap={6} wrap="wrap">
            <Text fz="xs" c="dimmed">
              {t('versionUpdate.release.currentTarget')}:
            </Text>
            <VersionBadge version={option.target.version} size="xs" />
            <VersionSourceBadge source={option.target.source} size="xs" />
          </Group>
          {!option.selectable && option.reasonCode && (
            <Text fz="xs" c="orange.8">
              {t(`enum.versionReason.${option.reasonCode}`)}
            </Text>
          )}
        </Stack>
        {option.selectable && (
          <Badge variant="light" color="blue" styles={{ label: { textTransform: 'none' } }}>
            {t('versionUpdate.option.agentsToUpdate', { count: option.agentsToUpdate })}
          </Badge>
        )}
      </Group>
    </Paper>
  );
  if (!option.selectable) return <div aria-disabled="true">{content}</div>;
  return (
    <UnstyledButton
      onClick={() => onPick(option.fleet)}
      aria-label={t('versionUpdate.release.chooseFleet', { fleet: option.fleet })}
      style={{ display: 'block', width: '100%' }}
    >
      {content}
    </UnstyledButton>
  );
}

/** "Auf Version X aktualisieren" on the release page: choose the fleet, then the update dialog. */
export function ReleaseUpdateButton({
  version,
  fleets,
}: {
  version: string;
  fleets: readonly ReleaseFleetOption[];
}) {
  const { t } = useTranslation();
  const [pickOpen, setPickOpen] = useState(false);
  const [fleet, setFleet] = useState<string | null>(null);
  const anySelectable = fleets.some((f) => f.selectable);
  return (
    <Can permission="fleets:set_version">
      <Button
        size="xs"
        leftSection={<IconRocket size={14} />}
        disabled={!anySelectable}
        onClick={() => setPickOpen(true)}
        data-testid="release-update"
        title={anySelectable ? undefined : t('versionUpdate.release.none')}
      >
        {t('versionUpdate.section.updateTo', { version })}
      </Button>
      <Modal
        opened={pickOpen}
        onClose={() => setPickOpen(false)}
        title={t('versionUpdate.release.pickTitle', { version })}
        size="md"
      >
        <Stack gap="xs">
          <Text fz="sm" c="dimmed">
            {t('versionUpdate.release.pickHint')}
          </Text>
          {sortFleetOptions(fleets).map((o) => (
            <FleetChoice
              key={o.fleet}
              option={o}
              onPick={(name) => {
                setPickOpen(false);
                setFleet(name);
              }}
            />
          ))}
        </Stack>
      </Modal>
      {fleet && (
        <UpdateVersionDialog
          fleet={fleet}
          initialVersion={version}
          opened
          onClose={() => setFleet(null)}
        />
      )}
    </Can>
  );
}
